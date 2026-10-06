package payouts

// Service Charge Distribution (PRD P5 EP-11, §16 #5): the month's pool of
// Accounting (contract H4, accounting.service_charge_pools) is distributed
// by the Service Charge Policy — 5% reserve for breakage & loss, 95% split
// equally per eligible employee (permanent and contract employees past
// probation; not probation, daily workers, SP2 or worse, unpaid leave the
// whole period; caddies are partners and never in it) × attendance factor
// (days present ÷ working days, from time & attendance). :simulate shows
// every employee with the amount or the exclusion reason; :approve goes
// through the approval engine and posts the distribution
// (hris.service_charge_distributed: the pool leaves the service charge
// liability for the distribution payable, the reserve and the rounding);
// the lines are payroll inputs (SERVICE_CHARGE) and the distribution is Paid
// once the payroll that pays them is posted. Every amount reconciles:
// collected = reserve + distributed + undistributed + rounding (FR-SVC-04).

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/accounting"
	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Distribution statuses.
var DistributionStatuses = []string{"draft", "simulated", "pending_approval", "approved", "paid", "cancelled"}

// ServiceChargeDistribution is a month's distribution with its totals.
type ServiceChargeDistribution struct {
	ID                  uuid.UUID  `json:"id" db:"id"`
	PropertyID          uuid.UUID  `json:"propertyId" db:"property_id"`
	Number              string     `json:"number" db:"number"`
	Year                int        `json:"year" db:"year"`
	Month               int        `json:"month" db:"month"`
	PeriodStart         time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd           time.Time  `json:"periodEnd" db:"period_end"`
	PayPeriod           string     `json:"payPeriod" db:"pay_period" doc:"YYYY-MM of the payroll that pays the lines"`
	PoolID              *uuid.UUID `json:"poolId" db:"pool_id"`
	PoolStatus          *string    `json:"poolStatus" db:"pool_status" doc:"draft | approved (Accounting → Service Charge Pool)"`
	Collected           string     `json:"collected" db:"collected"`
	ReservePercent      string     `json:"reservePercent" db:"reserve_percent"`
	DistributedPercent  string     `json:"distributedPercent" db:"distributed_percent"`
	Reserve             string     `json:"reserve" db:"reserve"`
	Distributable       string     `json:"distributable" db:"distributable"`
	Distributed         string     `json:"distributed" db:"distributed"`
	Undistributed       string     `json:"undistributed" db:"undistributed"`
	RoundingDifference  string     `json:"roundingDifference" db:"rounding_difference"`
	RoundingAccountCode *string    `json:"roundingAccountCode" db:"rounding_account_code"`
	Method              string     `json:"method" db:"method" enum:"equal,points"`
	AttendanceFactor    bool       `json:"attendanceFactor" db:"attendance_factor"`
	Redistribute        bool       `json:"redistribute" db:"redistribute"`
	Eligible            int        `json:"eligible" db:"eligible"`
	Excluded            int        `json:"excluded" db:"excluded"`
	PolicyVersion       int        `json:"policyVersion" db:"policy_version" doc:"Service Charge Policy version used (FR-POL-P5-07)"`
	Status              string     `json:"status" db:"status" enum:"draft,simulated,pending_approval,approved,paid,cancelled"`
	ApprovalRequestID   *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	SimulatedAt         *time.Time `json:"simulatedAt" db:"simulated_at"`
	ApprovedAt          *time.Time `json:"approvedAt" db:"approved_at"`
	PaidAt              *time.Time `json:"paidAt" db:"paid_at"`
	CancelledAt         *time.Time `json:"cancelledAt" db:"cancelled_at"`
	CancelReason        *string    `json:"cancelReason" db:"cancel_reason"`
	DecisionNote        *string    `json:"decisionNote" db:"decision_note"`
	Notes               *string    `json:"notes" db:"notes"`
	CreatedAt           time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt           time.Time  `json:"updatedAt" db:"updated_at"`
}

const distSelect = `SELECT id, property_id, number, year, month, period_start, period_end, pay_period, pool_id, pool_status,
	trim_scale(collected)::text AS collected, trim_scale(reserve_percent)::text AS reserve_percent, trim_scale(distributed_percent)::text AS distributed_percent,
	trim_scale(reserve)::text AS reserve, trim_scale(distributable)::text AS distributable, trim_scale(distributed)::text AS distributed,
	trim_scale(undistributed)::text AS undistributed, trim_scale(rounding_difference)::text AS rounding_difference, rounding_account_code, method,
	attendance_factor, redistribute, eligible, excluded, policy_version, status, approval_request_id, simulated_at, approved_at, paid_at, cancelled_at,
	cancel_reason, decision_note, notes, created_at, updated_at FROM hris.service_charge_distributions`

// ServiceChargeDistributionLine is one employee of a distribution.
type ServiceChargeDistributionLine struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	EmployeeID       uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo       string     `json:"employeeNo" db:"employee_no"`
	FullName         string     `json:"fullName" db:"full_name"`
	OrgUnitCode      *string    `json:"orgUnitCode" db:"org_unit_code"`
	OrgUnitName      *string    `json:"orgUnitName" db:"org_unit_name"`
	GradeCode        *string    `json:"gradeCode" db:"grade_code"`
	EmploymentStatus string     `json:"employmentStatus" db:"employment_status"`
	WorkerCategory   string     `json:"workerCategory" db:"worker_category"`
	WarningLevel     int        `json:"warningLevel" db:"warning_level"`
	ScheduledDays    int        `json:"scheduledDays" db:"scheduled_days"`
	PresentDays      string     `json:"presentDays" db:"present_days" doc:"Present days (paid leave counts as present)"`
	UnpaidLeaveDays  string     `json:"unpaidLeaveDays" db:"unpaid_leave_days"`
	Eligible         bool       `json:"eligible" db:"eligible"`
	ExclusionReason  *string    `json:"exclusionReason" db:"exclusion_reason" enum:"status_not_eligible,probation,worker_category,warning_letter,unpaid_leave_full_period,no_department_share,no_points,not_employed_in_period"`
	ShareGroup       *string    `json:"shareGroup" db:"share_group" doc:"Department share (org unit code) or * (one pool)"`
	Points           string     `json:"points" db:"points"`
	AttendanceFactor string     `json:"attendanceFactor" db:"attendance_factor"`
	BaseShare        string     `json:"baseShare" db:"base_share"`
	AttendanceAmount string     `json:"attendanceAmount" db:"attendance_amount" doc:"Base share × attendance factor"`
	Redistributed    string     `json:"redistributed" db:"redistributed" doc:"Part of the amount forfeited by attendance, redistributed by the rule"`
	Amount           string     `json:"amount" db:"amount"`
	PayrollRunID     *uuid.UUID `json:"payrollRunId" db:"payroll_run_id"`
	ConsumedAt       *time.Time `json:"consumedAt" db:"consumed_at"`
}

const distLineSelect = `SELECT id, employee_id, employee_no, full_name, org_unit_code, org_unit_name, grade_code, employment_status, worker_category,
	warning_level, scheduled_days, trim_scale(present_days)::text AS present_days, trim_scale(unpaid_leave_days)::text AS unpaid_leave_days, eligible,
	exclusion_reason, share_group, trim_scale(points)::text AS points, trim_scale(attendance_factor)::text AS attendance_factor,
	trim_scale(base_share)::text AS base_share, trim_scale(attendance_amount)::text AS attendance_amount, trim_scale(redistributed)::text AS redistributed,
	trim_scale(amount)::text AS amount, payroll_run_id, consumed_at FROM hris.service_charge_lines`

// ServiceChargeDistributionDetail is a distribution with its lines.
type ServiceChargeDistributionDetail struct {
	ServiceChargeDistribution
	Lines []ServiceChargeDistributionLine `json:"lines"`
}

// ServiceChargeDistributionInput creates the distribution of a month.
type ServiceChargeDistributionInput struct {
	Year      int    `json:"year"`
	Month     int    `json:"month"`
	PayPeriod string `json:"payPeriod,omitempty" doc:"YYYY-MM; default the month after the pool month"`
	Notes     string `json:"notes,omitempty"`
}

// ServiceChargeAction carries a reason / note.
type ServiceChargeAction struct {
	Reason string `json:"reason,omitempty"`
}

func (m *Module) registerServiceCharge(reg *route.Registry) {
	tag := "HRIS Service Charge"
	base := "/api/v1/hris/service-charge-distributions"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Service charge distributions", Permission: PermServiceChargeView,
		Response: ServiceChargeDistribution{}, List: true, Query: []route.Param{{Name: "year"}, {Name: "status", Enum: DistributionStatuses}},
		Handler: listRead(m.DB, m.listDistributionsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Start the distribution of a month's service charge pool",
		Permission: PermServiceChargeManage, Request: ServiceChargeDistributionInput{}, Response: ServiceChargeDistributionDetail{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createDistributionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Service charge distribution with the result per employee",
		Permission: PermServiceChargeView, Response: ServiceChargeDistributionDetail{},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ServiceChargeDistributionDetail, error) {
			did, err := handle.ID(r)
			if err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
			return m.Distribution(ctx, tx, did)
		})})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:simulate",
		Summary:    "Simulate the distribution (pool, eligibility, attendance factor, amount per employee); repeatable until approval",
		Permission: PermServiceChargeManage, Request: handle.Empty{}, Response: ServiceChargeDistributionDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.simulateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:approve",
		Summary: "Approve the distribution (submits it to the approval engine, or approves the current step)", Permission: PermServiceChargeApprove,
		Request: ServiceChargeAction{}, Response: ServiceChargeDistributionDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideDistributionHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject the distribution at the current approval step",
		Permission: PermServiceChargeApprove, Request: ServiceChargeAction{}, Response: ServiceChargeDistributionDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideDistributionHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a distribution not yet approved",
		Permission: PermServiceChargeManage, Request: ServiceChargeAction{}, Response: ServiceChargeDistributionDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelDistributionHTTP)})
}

func (m *Module) listDistributionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ServiceChargeDistribution, error) {
	status := filterParam(r, "status")
	if status != "" && !oneOf(DistributionStatuses, status) {
		return nil, enumErr("status", DistributionStatuses)
	}
	return handle.List[ServiceChargeDistribution](tx.Query(ctx, distSelect+` WHERE property_id = $1 AND ($2 = 0 OR year = $2) AND ($3 = '' OR status = $3)
		ORDER BY year DESC, month DESC, created_at DESC`, handle.Property(ctx), intParam(r, "year"), status))
}

// Distribution loads a distribution of the request's property with its lines.
func (m *Module) Distribution(ctx context.Context, tx pgx.Tx, did uuid.UUID) (ServiceChargeDistributionDetail, error) {
	d, err := getOne[ServiceChargeDistribution]("service charge distribution")(tx.Query(ctx, distSelect+` WHERE id = $1 AND property_id = $2`, did,
		handle.Property(ctx)))
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	lines, err := handle.List[ServiceChargeDistributionLine](tx.Query(ctx, distLineSelect+` WHERE distribution_id = $1
		ORDER BY eligible DESC, org_unit_name NULLS LAST, full_name, employee_id`, did))
	return ServiceChargeDistributionDetail{ServiceChargeDistribution: d, Lines: lines}, err
}

func lockDistribution(ctx context.Context, tx pgx.Tx, did uuid.UUID) (ServiceChargeDistribution, error) {
	return getOne[ServiceChargeDistribution]("service charge distribution")(tx.Query(ctx, distSelect+` WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		did, handle.Property(ctx)))
}

func distAudit(d ServiceChargeDistribution) map[string]any {
	return map[string]any{"status": d.Status, "collected": d.Collected, "reserve": d.Reserve, "distributed": d.Distributed,
		"undistributed": d.Undistributed, "roundingDifference": d.RoundingDifference, "eligible": d.Eligible}
}

// pool returns the Accounting service charge pool of a month (H4).
func pool(ctx context.Context, tx pgx.Tx, property uuid.UUID, year, month int) (*accounting.ServiceChargePool, error) {
	pools, err := accounting.ListServiceChargePools(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	for i := range pools {
		if pools[i].Year == year && pools[i].Month == month {
			return &pools[i], nil
		}
	}
	return nil, nil
}

func (m *Module) createDistributionHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, in ServiceChargeDistributionInput) (ServiceChargeDistributionDetail, error) {
	property := handle.Property(ctx)
	if in.Year < 2000 || in.Year > 2100 || in.Month < 1 || in.Month > 12 {
		return ServiceChargeDistributionDetail{}, handle.Invalid("month", "invalid", "a year and a month (1–12)")
	}
	start := time.Date(in.Year, time.Month(in.Month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	if end.After(today(ctx, tx, property)) {
		return ServiceChargeDistributionDetail{}, handle.Invalid("month", "month_not_closed", "the month must be over before its pool is distributed")
	}
	payPeriod := start.AddDate(0, 1, 0).Format("2006-01")
	if v := strings.TrimSpace(in.PayPeriod); v != "" {
		pp, err := time.Parse("2006-01", v)
		if err != nil || pp.Before(start) {
			return ServiceChargeDistributionDetail{}, handle.Invalid("payPeriod", "invalid", "YYYY-MM, not before the pool month")
		}
		payPeriod = v
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.service_charge_distributions WHERE property_id = $1 AND year = $2 AND month = $3
		AND status <> 'cancelled')`, property, in.Year, in.Month).Scan(&exists); err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	if exists {
		return ServiceChargeDistributionDetail{}, errs.Conflict("distribution_exists", "the pool of "+start.Format("2006-01")+" already has a distribution")
	}
	p, err := pool(ctx, tx, property, in.Year, in.Month)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	if p == nil {
		return ServiceChargeDistributionDetail{}, errs.Conflict("pool_missing",
			"compute the service charge pool of "+start.Format("2006-01")+" in Accounting → Service Charge Pool first")
	}
	no, err := yearlyNumber(ctx, tx, property, "SCD", in.Year)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	did := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.service_charge_distributions (id, property_id, number, year, month, period_start, period_end, pay_period,
		pool_id, pool_status, collected, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12,$13,$13)`,
		did, property, no, in.Year, in.Month, start, end, payPeriod, p.ID, p.Status, p.Collected, nullStr(in.Notes), actor(ctx)); err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	out, err := m.Distribution(ctx, tx, did)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.service_charge_distribution",
		EntityID: did.String(), EntityLabel: no + " · " + start.Format("2006-01"), PropertyID: &property, After: distAudit(out.ServiceChargeDistribution)})
}

// orgChains maps an org unit to its code and the codes of its parents.
func orgChains(ctx context.Context, tx pgx.Tx, property uuid.UUID) (map[uuid.UUID][]string, error) {
	units, err := hris.OrgUnits(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID]hris.OrgUnit{}
	for _, u := range units {
		byID[u.ID] = u
	}
	out := map[uuid.UUID][]string{}
	for _, u := range units {
		var chain []string
		cur, seen := u, map[uuid.UUID]bool{}
		for {
			chain = append(chain, cur.Code)
			seen[cur.ID] = true
			if cur.ParentID == nil || seen[*cur.ParentID] {
				break
			}
			p, ok := byID[*cur.ParentID]
			if !ok {
				break
			}
			cur = p
		}
		out[u.ID] = chain
	}
	return out, nil
}

func (m *Module) simulateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ServiceChargeDistributionDetail, error) {
	did, err := handle.ID(r)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	before, err := lockDistribution(ctx, tx, did)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	if before.Status != "draft" && before.Status != "simulated" {
		return ServiceChargeDistributionDetail{}, errs.Conflict("distribution_not_open", "the distribution is "+before.Status)
	}
	if err := m.Simulate(ctx, tx, before); err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	out, err := m.Distribution(ctx, tx, did)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "simulate", EntityType: "hris.service_charge_distribution",
		EntityID: did.String(), EntityLabel: out.Number, PropertyID: &out.PropertyID, Before: distAudit(before), After: distAudit(out.ServiceChargeDistribution)})
}

// Simulate (re)computes the lines of a distribution from the pool and the
// time & attendance of its month.
func (m *Module) Simulate(ctx context.Context, tx pgx.Tx, d ServiceChargeDistribution) error {
	return simulate(ctx, tx, d, true)
}

// simulate computes the lines; finalize closes the attendance days of the
// month first (Absent for shifts without clock-in).
func simulate(ctx context.Context, tx pgx.Tx, d ServiceChargeDistribution, finalize bool) error {
	property := d.PropertyID
	p, err := pool(ctx, tx, property, d.Year, d.Month)
	if err != nil {
		return err
	}
	if p == nil {
		return errs.Conflict("pool_missing", "the service charge pool of the month no longer exists in Accounting")
	}
	pol, ref, err := hris.LoadServiceChargePolicy(ctx, tx, property, hris.PolicyTime(d.PeriodEnd))
	if err != nil {
		return err
	}
	if finalize {
		if err := hris.FinalizeAttendance(ctx, tx, property, d.PeriodStart, d.PeriodEnd); err != nil {
			return err
		}
	}
	sums, err := hris.TimeSummaries(ctx, tx, property, d.PeriodStart, d.PeriodEnd, nil)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(sums))
	for i, s := range sums {
		ids[i] = s.EmployeeID
	}
	emps, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: property, IDs: ids})
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]hris.Employee{}
	for _, e := range emps {
		byID[e.ID] = e
	}
	chains, err := orgChains(ctx, tx, property)
	if err != nil {
		return err
	}
	cands := make([]hris.ServiceChargeCandidate, 0, len(sums))
	kept := make([]hris.Employee, 0, len(sums))
	for _, s := range sums {
		e, ok := byID[s.EmployeeID]
		if !ok {
			continue
		}
		warning, err := hris.WarningLevel(ctx, tx, e.ID, d.PeriodEnd)
		if err != nil {
			return err
		}
		c := hris.ServiceChargeCandidate{EmployeeID: e.ID, EmploymentStatus: e.EmploymentStatus, WorkerCategory: e.WorkerCategory, WarningLevel: warning,
			EmployedInPeriod: true, ScheduledDays: s.ScheduledDays, PresentDays: decimal.NewFromInt(int64(s.PresentDays)).Add(s.PaidLeaveDays),
			UnpaidLeaveDays: s.UnpaidLeaveDays, InProbation: e.ProbationEndDate != nil && e.ProbationEndDate.After(d.PeriodEnd)}
		if e.GradeCode != nil {
			c.GradeCode = *e.GradeCode
		}
		if e.OrgUnitID != nil {
			c.OrgUnitCodes = chains[*e.OrgUnitID]
		}
		cands = append(cands, c)
		kept = append(kept, e)
	}
	collected := dec(p.Collected)
	res := hris.DistributeServiceCharge(pol, collected, cands)
	if _, err := tx.Exec(ctx, `DELETE FROM hris.service_charge_lines WHERE distribution_id = $1`, d.ID); err != nil {
		return err
	}
	excluded := 0
	for i, s := range res.Shares {
		e := kept[i]
		c := cands[i]
		if !s.Eligible {
			excluded++
		}
		var group *string
		if s.Group != "" {
			group = &s.Group
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.service_charge_lines (id, distribution_id, property_id, employee_id, employee_no, full_name, org_unit_code,
			org_unit_name, grade_code, employment_status, worker_category, warning_level, scheduled_days, present_days, unpaid_leave_days, eligible,
			exclusion_reason, share_group, points, attendance_factor, base_share, attendance_amount, redistributed, amount)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15::numeric,$16,$17,$18,$19::numeric,$20::numeric,$21::numeric,$22::numeric,
			$23::numeric,$24::numeric)`, id.New(), d.ID, property, e.ID, e.EmployeeNo, e.FullName, e.OrgUnitCode, e.OrgUnitName, e.GradeCode,
			e.EmploymentStatus, e.WorkerCategory, c.WarningLevel, c.ScheduledDays, c.PresentDays.String(), c.UnpaidLeaveDays.String(), s.Eligible,
			nullStr(s.Reason), group, s.Points.String(), s.Factor.String(), s.BaseShare.String(), s.AttendanceAmount.String(), s.Redistributed.String(),
			s.Amount.String()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = 'simulated', pool_id = $2, pool_status = $3, collected = $4::numeric,
		reserve_percent = $5::numeric, distributed_percent = $6::numeric, reserve = $7::numeric, distributable = $8::numeric, distributed = $9::numeric,
		undistributed = $10::numeric, rounding_difference = $11::numeric, rounding_account_code = $12, method = $13, attendance_factor = $14,
		redistribute = $15, eligible = $16, excluded = $17, policy_version = $18, simulated_at = now(), simulated_by = $19, decision_note = NULL,
		updated_by = $19 WHERE id = $1`, d.ID, p.ID, p.Status, collected.String(), hris.Dec(pol.ReservePercent).String(),
		hris.Dec(pol.DistributedPercent).String(), res.Reserve.String(), res.Distributable.String(), res.Distributed.String(), res.Undistributed.String(),
		res.RoundingDifference.String(), nullStr(pol.RoundingAccountCode), pol.Method, pol.AttendanceFactor, pol.RedistributeForfeited, res.Eligible,
		excluded, ref.Version, actor(ctx))
	return err
}

func (m *Module) decideDistributionHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, in ServiceChargeAction) (ServiceChargeDistributionDetail, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, in ServiceChargeAction) (ServiceChargeDistributionDetail, error) {
		did, err := handle.ID(r)
		if err != nil {
			return ServiceChargeDistributionDetail{}, err
		}
		d, err := lockDistribution(ctx, tx, did)
		if err != nil {
			return ServiceChargeDistributionDetail{}, err
		}
		switch {
		case d.Status == "pending_approval" && d.ApprovalRequestID != nil:
			if err := m.Approvals.Decide(ctx, tx, *d.ApprovalRequestID, approve, in.Reason); err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
		case d.Status == "simulated" && approve:
			p, err := pool(ctx, tx, d.PropertyID, d.Year, d.Month)
			if err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
			if p == nil || p.Status != "approved" {
				return ServiceChargeDistributionDetail{}, errs.Conflict("pool_not_approved",
					"Finance must approve the service charge pool of the month (Accounting → Service Charge Pool) before the distribution")
			}
			if !dec(p.Collected).Equal(dec(d.Collected)) {
				return ServiceChargeDistributionDetail{}, errs.Conflict("pool_changed", "the pool changed since the simulation: simulate again")
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = 'pending_approval', pool_status = $2, updated_by = $3 WHERE id = $1`,
				did, p.Status, actor(ctx)); err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit_approval", EntityType: "hris.service_charge_distribution",
				EntityID: did.String(), EntityLabel: d.Number, PropertyID: &d.PropertyID, Reason: in.Reason, Before: map[string]any{"status": d.Status},
				After: map[string]any{"status": "pending_approval"}}); err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
			aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: DistributionDocumentType.Code, DocumentID: did,
				DocumentRef: d.Number, Title: fmt.Sprintf("Service charge %04d-%02d · %s to %d employees", d.Year, d.Month, money(dec(d.Distributed)), d.Eligible),
				PropertyID: d.PropertyID, Attributes: map[string]any{"amount": dec(d.Distributed).InexactFloat64(), "employees": float64(d.Eligible)}})
			if err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET approval_request_id = $2 WHERE id = $1`, did, aid); err != nil {
				return ServiceChargeDistributionDetail{}, err
			}
		default:
			return ServiceChargeDistributionDetail{}, errs.Conflict("distribution_not_submittable", "the distribution is "+d.Status+
				" (simulate it before approving)")
		}
		return m.Distribution(ctx, tx, did)
	}
}

// DistributionDecision applies the approval decision (approval engine
// hook): approved → posted (hris.service_charge_distributed) and the
// employees see their share; rejected → back to Simulated.
func (m *Module) DistributionDecision(ctx context.Context, tx pgx.Tx, ad approval.Decision) error {
	d, err := getOne[ServiceChargeDistribution]("service charge distribution")(tx.Query(ctx, distSelect+` WHERE id = $1 FOR UPDATE`, ad.DocumentID))
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil
		}
		return err
	}
	if d.Status != "pending_approval" {
		return nil
	}
	next := "simulated"
	if ad.Status == approval.StatusApproved {
		next = "approved"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = $2, decision_note = $3,
		approved_at = CASE WHEN $2 = 'approved' THEN now() END, posted_at = CASE WHEN $2 = 'approved' THEN now() END WHERE id = $1`,
		d.ID, next, nullStr(ad.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + ad.Status, EntityType: "hris.service_charge_distribution",
		EntityID: d.ID.String(), EntityLabel: d.Number, PropertyID: &d.PropertyID, Reason: ad.Reason, Before: map[string]any{"status": d.Status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	period := fmt.Sprintf("%04d-%02d", d.Year, d.Month)
	var creator *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT created_by FROM hris.service_charge_distributions WHERE id = $1`, d.ID).Scan(&creator)
	decision := map[string]string{"approved": "approved", "simulated": "rejected"}[next]
	if ad.Status == approval.StatusCancelled {
		decision = "withdrawn"
	}
	var users []uuid.UUID
	if creator != nil {
		users = append(users, *creator)
	}
	if err := m.notifyUsers(ctx, tx, d.PropertyID, users, "hris.service_charge_decided", "/hris/service-charge/"+d.ID.String(),
		map[string]any{"number": d.Number, "period": period, "amount": money(dec(d.Distributed)), "employees": d.Eligible, "decision": decision,
			"reason": ad.Reason}); err != nil {
		return err
	}
	if next != "approved" {
		return nil
	}
	var pid uuid.UUID
	if d.PoolID != nil {
		pid = *d.PoolID
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventServiceChargeDistributed, "hris.service_charge_distribution", &d.ID, &d.PropertyID,
		hris.ServiceChargeDistributed{DistributionID: d.ID, Number: d.Number, PropertyID: d.PropertyID, Year: d.Year, Month: d.Month, PoolID: pid,
			PayPeriod: d.PayPeriod, Collected: d.Collected, Reserve: d.Reserve, Distributed: d.Distributed, Undistributed: d.Undistributed,
			RoundingDifference: d.RoundingDifference, RoundingAccountCode: deref(d.RoundingAccountCode), Employees: d.Eligible,
			PolicyVersion: d.PolicyVersion}); err != nil {
		return err
	}
	// the employees' share (ESS → Service Charge)
	rows, err := tx.Query(ctx, `SELECT u.id, trim_scale(l.amount)::text, trim_scale(l.attendance_factor)::text FROM hris.service_charge_lines l
		JOIN platform.users u ON u.employee_id = l.employee_id AND u.status = 'active' WHERE l.distribution_id = $1 AND l.eligible AND l.amount > 0`, d.ID)
	if err != nil {
		return err
	}
	type share struct {
		user           uuid.UUID
		amount, factor string
	}
	var shares []share
	for rows.Next() {
		var s share
		if err := rows.Scan(&s.user, &s.amount, &s.factor); err != nil {
			rows.Close()
			return err
		}
		shares = append(shares, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, s := range shares {
		if err := m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{s.user}, "hris.service_charge_statement", "/ops/ess/service-charge",
			map[string]any{"period": period, "amount": money(dec(s.amount)), "factor": s.factor, "payPeriod": d.PayPeriod}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) cancelDistributionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, in ServiceChargeAction) (ServiceChargeDistributionDetail, error) {
	did, err := handle.ID(r)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	d, err := lockDistribution(ctx, tx, did)
	if err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	if !slices.Contains([]string{"draft", "simulated", "pending_approval"}, d.Status) {
		return ServiceChargeDistributionDetail{}, errs.Conflict("distribution_posted", "an approved distribution cannot be cancelled")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return ServiceChargeDistributionDetail{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if d.Status == "pending_approval" && d.ApprovalRequestID != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = 'simulated' WHERE id = $1`, did); err != nil {
			return ServiceChargeDistributionDetail{}, err
		}
		if err := m.Approvals.Cancel(ctx, tx, *d.ApprovalRequestID, "distribution cancelled"); err != nil {
			return ServiceChargeDistributionDetail{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.service_charge_distributions SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3
		WHERE id = $1`, did, in.Reason, actor(ctx)); err != nil {
		return ServiceChargeDistributionDetail{}, err
	}
	out, err := m.Distribution(ctx, tx, did)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.service_charge_distribution",
		EntityID: did.String(), EntityLabel: d.Number, PropertyID: &d.PropertyID, Reason: in.Reason, Before: distAudit(d),
		After: map[string]any{"status": "cancelled"}})
}
