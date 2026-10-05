package hrtime

// Leave (EP-08 FR-LVE-01–03, §16 #3): leave types of the Leave Policy
// (annual with a balance, per event, none), balances per calendar year
// with the 12-month accrual, carry-over of at most 6 days lapsing on 31
// March and collective leave deductions; leave requests from ESS or HR
// with the approval levels of the policy (the balance is reduced when
// approved, pending days are reserved); the team leave calendar with
// schedule conflicts; adjustments and the cut-over import of balances
// (EP-28 FR-MIG-P5-02).

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// LeaveTypeView is a leave or permission type of the Leave Policy.
type LeaveTypeView struct {
	Kind                string `json:"kind" enum:"leave,permission"`
	Code                string `json:"code"`
	Name                string `json:"name"`
	Paid                bool   `json:"paid"`
	Days                int    `json:"days"`
	Accrual             string `json:"accrual"`
	EligibleAfterMonths int    `json:"eligibleAfterMonths"`
	RequiresDocument    bool   `json:"requiresDocument"`
	Gender              string `json:"gender,omitempty"`
	CountsWorkingDays   bool   `json:"countsWorkingDays"`
	HasBalance          bool   `json:"hasBalance"`
	MaxHours            string `json:"maxHours,omitempty"`
	PolicyVersion       int    `json:"policyVersion"`
}

// LeaveBalanceView is a balance of a year.
type LeaveBalanceView struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	EmployeeID         uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo         string     `json:"employeeNo" db:"employee_no"`
	EmployeeName       string     `json:"employeeName" db:"employee_name"`
	OrgUnitName        *string    `json:"orgUnitName" db:"org_unit_name"`
	LeaveType          string     `json:"leaveType" db:"leave_type"`
	Year               int        `json:"year" db:"year"`
	Entitled           string     `json:"entitled" db:"entitled"`
	CarriedOver        string     `json:"carriedOver" db:"carried_over"`
	CarryOverExpiresOn *time.Time `json:"carryOverExpiresOn" db:"carry_over_expires_on"`
	CarriedExpired     string     `json:"carriedExpired" db:"carried_expired"`
	Adjusted           string     `json:"adjusted" db:"adjusted"`
	Used               string     `json:"used" db:"used"`
	Pending            string     `json:"pending" db:"pending" doc:"Days of requests waiting for approval (reserved)"`
	Available          string     `json:"available" db:"available" doc:"entitled + carried over − expired + adjusted − used − pending"`
	GrantedOn          *time.Time `json:"grantedOn" db:"granted_on"`
}

const balanceSelect = `SELECT b.id, b.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, b.leave_type, b.year,
	trim_scale(b.entitled)::text AS entitled, trim_scale(b.carried_over)::text AS carried_over, b.carry_over_expires_on,
	trim_scale(b.carried_expired)::text AS carried_expired, trim_scale(b.adjusted)::text AS adjusted, trim_scale(b.used)::text AS used,
	trim_scale(coalesce(p.days, 0))::text AS pending,
	trim_scale(b.entitled + b.carried_over - b.carried_expired + b.adjusted - b.used - coalesce(p.days, 0))::text AS available, b.granted_on
	FROM hris.leave_balances b JOIN hris.employees e ON e.id = b.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
	LEFT JOIN LATERAL (SELECT sum(r.days) AS days FROM hris.leave_requests r WHERE r.employee_id = b.employee_id AND r.leave_type = b.leave_type
	  AND r.status = 'submitted' AND extract(year FROM r.start_date)::int = b.year) p ON true`

// LeaveRequestView is a leave request.
type LeaveRequestView struct {
	ID                uuid.UUID          `json:"id" db:"id"`
	PropertyID        uuid.UUID          `json:"propertyId" db:"property_id"`
	Number            string             `json:"number" db:"number"`
	EmployeeID        uuid.UUID          `json:"employeeId" db:"employee_id"`
	EmployeeNo        string             `json:"employeeNo" db:"employee_no"`
	EmployeeName      string             `json:"employeeName" db:"employee_name"`
	OrgUnitName       *string            `json:"orgUnitName" db:"org_unit_name"`
	LeaveType         string             `json:"leaveType" db:"leave_type"`
	StartDate         time.Time          `json:"startDate" db:"start_date"`
	EndDate           time.Time          `json:"endDate" db:"end_date"`
	HalfDay           *string            `json:"halfDay" db:"half_day" enum:"am,pm"`
	Days              string             `json:"days" db:"days"`
	Paid              bool               `json:"paid" db:"paid"`
	Reason            *string            `json:"reason" db:"reason"`
	DocumentFileID    *uuid.UUID         `json:"documentFileId" db:"document_file_id"`
	ScheduleConflicts int                `json:"scheduleConflicts" db:"schedule_conflicts" doc:"Published shifts in the period (FR-LVE-03)"`
	Status            string             `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled"`
	DecisionNote      *string            `json:"decisionNote" db:"decision_note"`
	DecidedAt         *time.Time         `json:"decidedAt" db:"decided_at"`
	CreatedAt         time.Time          `json:"createdAt" db:"created_at"`
	Approvals         []TimeApprovalStep `json:"approvals" db:"-"`
}

const leaveSelect = `SELECT r.id, r.property_id, r.number, r.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, r.leave_type,
	r.start_date, r.end_date, r.half_day, trim_scale(r.days)::text AS days, r.paid, r.reason, r.document_file_id, r.schedule_conflicts, r.status,
	r.decision_note, r.decided_at, r.created_at
	FROM hris.leave_requests r JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

// LeaveRequestInput requests leave.
type LeaveRequestInput struct {
	EmployeeID     *uuid.UUID `json:"employeeId,omitempty" doc:"HR only"`
	LeaveType      string     `json:"leaveType"`
	StartDate      string     `json:"startDate"`
	EndDate        string     `json:"endDate,omitempty" doc:"Default: the start date"`
	HalfDay        string     `json:"halfDay,omitempty" enum:"am,pm"`
	Reason         string     `json:"reason,omitempty"`
	DocumentFileID *uuid.UUID `json:"documentFileId,omitempty" doc:"e.g. a doctor's letter (POST /api/v1/hris/document-files)"`
}

// BalanceAdjustRequest corrects a balance.
type BalanceAdjustRequest struct {
	EmployeeID uuid.UUID `json:"employeeId"`
	LeaveType  string    `json:"leaveType"`
	Year       int       `json:"year"`
	Days       string    `json:"days" doc:"Positive adds, negative deducts"`
	Note       string    `json:"note"`
}

// LeaveAccrueRequest runs the accrual, carry-over and expiry now.
type LeaveAccrueRequest struct {
	EmployeeID *uuid.UUID `json:"employeeId,omitempty"`
}

// LeaveAccrualReport counts what the accrual did.
type LeaveAccrualReport struct {
	Granted     int `json:"granted"`
	CarriedOver int `json:"carriedOver"`
	Expired     int `json:"expired"`
	Collective  int `json:"collective" doc:"Collective leave days deducted"`
}

// LeaveImportRequest loads balances at cut-over (EP-28 FR-MIG-P5-02).
type LeaveImportRequest struct {
	CSV    string `json:"csv" doc:"Columns: employeeNo, leaveType, year, entitled, carriedOver, carryOverExpiresOn, used, note"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// LeaveImportIssue is a rejected row.
type LeaveImportIssue struct {
	Row     int    `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// LeaveImportReport summarises a balance import.
type LeaveImportReport struct {
	DryRun   bool               `json:"dryRun"`
	Rows     int                `json:"rows"`
	Inserted int                `json:"inserted"`
	Updated  int                `json:"updated"`
	Failed   int                `json:"failed"`
	Issues   []LeaveImportIssue `json:"issues"`
}

// LeaveCalendarEntry is leave of a team member in a period (FR-LVE-03).
type LeaveCalendarEntry struct {
	RequestID         uuid.UUID `json:"requestId" db:"id"`
	Number            string    `json:"number" db:"number"`
	EmployeeID        uuid.UUID `json:"employeeId" db:"employee_id"`
	EmployeeName      string    `json:"employeeName" db:"employee_name"`
	OrgUnitName       *string   `json:"orgUnitName" db:"org_unit_name"`
	LeaveType         string    `json:"leaveType" db:"leave_type"`
	StartDate         time.Time `json:"startDate" db:"start_date"`
	EndDate           time.Time `json:"endDate" db:"end_date"`
	Days              string    `json:"days" db:"days"`
	Status            string    `json:"status" db:"status"`
	ScheduleConflicts int       `json:"scheduleConflicts" db:"conflicts" doc:"Shifts on the leave days (not yet covered)"`
}

// LeaveImportColumns is the CSV header of the balance import.
var LeaveImportColumns = []string{"employeeNo", "leaveType", "year", "entitled", "carriedOver", "carryOverExpiresOn", "used", "note"}

func (m *Module) registerLeave(reg *route.Registry) {
	tag := "HRIS Leave"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/leave-types", Summary: "Leave and permission types (Leave Policy)",
		Permission: PermLeaveView, Response: LeaveTypeView{}, List: true, Handler: listRead(m.DB, m.leaveTypesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/leave-balances", Summary: "Leave balances", Permission: PermBalanceView,
		Response: LeaveBalanceView{}, List: true, Query: []route.Param{{Name: "year"}, {Name: "employeeId"}, {Name: "orgUnitId"}, {Name: "leaveType"}},
		Handler: listRead(m.DB, m.balancesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-balances:adjust", Summary: "Adjust a leave balance",
		Permission: PermBalanceAdjust, Request: BalanceAdjustRequest{}, Response: LeaveBalanceView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.adjustHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-balances:accrue", Summary: "Run the leave accrual, carry-over and expiry now",
		Permission: PermBalanceAdjust, Request: LeaveAccrueRequest{}, Response: LeaveAccrualReport{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.accrueHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-balances:import", Summary: "Import leave balances from CSV (migration)",
		Permission: PermBalanceImport, Request: LeaveImportRequest{}, Response: LeaveImportReport{}, Status: http.StatusOK, Handler: m.importHTTP})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/leave-requests", Summary: "Leave requests", Permission: PermLeaveView,
		Response: LeaveRequestView{}, List: true, Query: []route.Param{{Name: "status", Enum: []string{"submitted", "approved", "rejected", "cancelled"}},
			{Name: "from"}, {Name: "to"}, {Name: "employeeId"}, {Name: "orgUnitId"}}, Handler: listRead(m.DB, m.hrLeaveListHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-requests", Summary: "Request leave for an employee",
		Permission: PermLeaveCreate, Request: LeaveRequestInput{}, Response: LeaveRequestView{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createLeaveHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/leave-requests/{id}", Summary: "Leave request with its approvals",
		Permission: PermLeaveView, Response: LeaveRequestView{}, Handler: handle.Read(m.DB, m.getLeaveHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-requests/{id}:approve", Summary: "Approve a leave request",
		Permission: PermLeaveApprove, Request: TimeDecision{}, Response: LeaveRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideLeave(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-requests/{id}:reject", Summary: "Reject a leave request",
		Permission: PermLeaveApprove, Request: TimeDecision{}, Response: LeaveRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideLeave(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/leave-requests/{id}:cancel", Summary: "Cancel a leave request (approved leave is restored to the balance)",
		Permission: PermLeaveCancel, Request: TimeDecision{}, Response: LeaveRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelLeaveHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/leave-calendar", Summary: "Leave calendar with schedule conflicts",
		Permission: PermLeaveView, Response: LeaveCalendarEntry{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "orgUnitId"}},
		Handler: listRead(m.DB, m.calendarHTTP)})

	ess := "Employee Self Service"
	essAdd := func(rt route.Route) {
		if rt.Permission == "" {
			rt.Permission = hris.PermissionESS
		}
		add(reg, ess, rt)
	}
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/leave-types", Summary: "Leave and permission types I may request", Response: LeaveTypeView{},
		List: true, Handler: listRead(m.DB, m.essLeaveTypesHTTP)})
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/leave-balances", Summary: "My leave balances", Response: LeaveBalanceView{}, List: true,
		Handler: listRead(m.DB, m.essBalancesHTTP)})
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/leave-requests", Summary: "My leave requests", Response: LeaveRequestView{}, List: true,
		Handler: listRead(m.DB, m.essLeaveListHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/leave-requests", Summary: "Request leave", Request: LeaveRequestInput{},
		Response: LeaveRequestView{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createLeaveHTTP(false))})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/leave-requests/{id}:cancel", Summary: "Withdraw or cancel my leave (before it starts)",
		Request: TimeDecision{}, Response: LeaveRequestView{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.cancelLeaveHTTP(false))})
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team-calendar", Summary: "Leave calendar of my team", Permission: hris.PermissionTeam,
		Response: LeaveCalendarEntry{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}}, Handler: listRead(m.DB, m.teamCalendarHTTP)})
}

// ── types ────────────────────────────────────────────────────────────────

func typeViews(lp hris.LeavePolicy, version int) []LeaveTypeView {
	out := []LeaveTypeView{}
	for _, t := range lp.LeaveTypes {
		out = append(out, LeaveTypeView{Kind: "leave", Code: t.Code, Name: t.Name, Paid: t.Paid, Days: t.Days, Accrual: t.Accrual,
			EligibleAfterMonths: t.EligibleAfterMonths, RequiresDocument: t.RequiresDocument, Gender: t.Gender, CountsWorkingDays: t.CountsWorkingDays,
			HasBalance: t.HasBalance(), PolicyVersion: version})
	}
	for _, t := range lp.PermissionTypes {
		out = append(out, LeaveTypeView{Kind: "permission", Code: t.Code, Name: t.Name, Paid: t.Paid, MaxHours: t.MaxHours, PolicyVersion: version})
	}
	return out
}

func (m *Module) leaveTypesHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]LeaveTypeView, error) {
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, handle.Property(ctx), clock.Now())
	return typeViews(lp, ref.Version), err
}

func (m *Module) essLeaveTypesHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]LeaveTypeView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, e.PropertyID, clock.Now())
	if err != nil {
		return nil, err
	}
	tday := today(ctx, tx, e.PropertyID)
	var out []LeaveTypeView
	for _, v := range typeViews(lp, ref.Version) {
		if v.Gender != "" && (e.Gender == nil || *e.Gender != v.Gender) {
			continue
		}
		if v.Kind == "leave" && v.Accrual != "annual" && v.EligibleAfterMonths > 0 && (e.JoinDate == nil ||
			hris.ServiceMonths(*e.JoinDate, tday) < v.EligibleAfterMonths) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// ── balances ─────────────────────────────────────────────────────────────

type balanceRow struct {
	ID                                                             uuid.UUID
	Entitled, Carried, UsedCarried, CarriedExpired, Adjusted, Used decimal.Decimal
	Expires                                                        *time.Time
	GrantedOn                                                      *time.Time
}

func (b balanceRow) available() decimal.Decimal {
	return b.Entitled.Add(b.Carried).Sub(b.CarriedExpired).Add(b.Adjusted).Sub(b.Used)
}

func (b balanceRow) carriedLeft() decimal.Decimal {
	return decimal.Max(b.Carried.Sub(b.UsedCarried).Sub(b.CarriedExpired), decimal.Zero)
}

func loadBalance(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, leaveType string, year int, lock bool) (*balanceRow, error) {
	var b balanceRow
	sql := `SELECT id, entitled, carried_over, used_carried, carried_expired, adjusted, used, carry_over_expires_on, granted_on FROM hris.leave_balances
		WHERE employee_id = $1 AND leave_type = $2 AND year = $3`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, employeeID, leaveType, year).Scan(&b.ID, &b.Entitled, &b.Carried, &b.UsedCarried, &b.CarriedExpired, &b.Adjusted, &b.Used,
		&b.Expires, &b.GrantedOn)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func ledger(ctx context.Context, tx pgx.Tx, property uuid.UUID, b uuid.UUID, employee uuid.UUID, kind string, days decimal.Decimal, request *uuid.UUID,
	note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO hris.leave_balance_entries (id, property_id, balance_id, employee_id, kind, days, leave_request_id, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, b, employee, kind, days, request, nullStr(note), actor(ctx))
	return err
}

// ensureBalance creates the balance of an annual leave type for a year up
// to the current year when the employee is eligible (accrual, §16 #3),
// carrying over the remaining days of the year before. nil when not
// eligible.
func (m *Module) ensureBalance(ctx context.Context, tx pgx.Tx, emp hris.Employee, lt hris.LeaveType, lp hris.LeavePolicy, version, year int,
	tday time.Time, rep *LeaveAccrualReport) (*balanceRow, error) {
	b, err := loadBalance(ctx, tx, emp.ID, lt.Code, year, true)
	if err != nil || b != nil {
		return b, err
	}
	if emp.JoinDate == nil || year > tday.Year() {
		return nil, nil
	}
	grantOn, ok := hris.AnnualGrant(lt, *emp.JoinDate, year)
	if !ok {
		return nil, nil
	}
	carried := decimal.Zero
	var expires *time.Time
	prev, err := loadBalance(ctx, tx, emp.ID, lt.Code, year-1, true)
	if err != nil {
		return nil, err
	}
	if prev != nil {
		carried = lp.CarryOver(prev.available())
		if carried.IsPositive() {
			e := lp.CarryOverExpiryDate(year)
			expires = &e
		}
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_balances (id, property_id, employee_id, leave_type, year, entitled, carried_over, carry_over_expires_on,
		granted_on, policy_version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, bid, emp.PropertyID, emp.ID, lt.Code, year, lt.Days, carried, expires,
		ymd(grantOn), version); err != nil {
		return nil, err
	}
	if err := ledger(ctx, tx, emp.PropertyID, bid, emp.ID, "accrual", decimal.NewFromInt(int64(lt.Days)), nil,
		fmt.Sprintf("%s %d granted on %s", lt.Code, year, ymd(grantOn))); err != nil {
		return nil, err
	}
	if rep != nil {
		rep.Granted++
	}
	if carried.IsPositive() {
		if err := ledger(ctx, tx, emp.PropertyID, bid, emp.ID, "carry_over", carried, nil, fmt.Sprintf("carried from %d, lapses %s", year-1,
			ymd(*expires))); err != nil {
			return nil, err
		}
		if rep != nil {
			rep.CarriedOver++
		}
	}
	return loadBalance(ctx, tx, emp.ID, lt.Code, year, true)
}

// accrue runs the accrual of the current year, the expiry of carried-over
// days and the collective leave deductions for a property (daily job).
func (m *Module) accrue(ctx context.Context, tx pgx.Tx, property uuid.UUID, only *uuid.UUID) (LeaveAccrualReport, error) {
	rep := LeaveAccrualReport{}
	tday := today(ctx, tx, property)
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, property, hris.PolicyTime(tday))
	if err != nil {
		return rep, err
	}
	f := hris.EmployeeFilter{PropertyID: property, ActiveOn: &tday}
	if only != nil {
		f.IDs = []uuid.UUID{*only}
	}
	emps, err := hris.Employees(ctx, tx, f)
	if err != nil {
		return rep, err
	}
	for _, e := range emps {
		for _, lt := range lp.LeaveTypes {
			if !lt.HasBalance() {
				continue
			}
			if grantOn, ok := hris.AnnualGrant(lt, derefTime(e.JoinDate), tday.Year()); !ok || grantOn.After(tday) || e.JoinDate == nil {
				continue
			}
			if _, err := m.ensureBalance(ctx, tx, e, lt, lp, ref.Version, tday.Year(), tday, &rep); err != nil {
				return rep, err
			}
		}
	}
	// carried-over days lapse after the expiry date
	rows, err := tx.Query(ctx, `UPDATE hris.leave_balances SET carried_expired = carried_over - used_carried
		WHERE property_id = $1 AND carry_over_expires_on < $2::date AND carried_over - used_carried - carried_expired > 0
		  AND ($3::uuid IS NULL OR employee_id = $3)
		RETURNING id, employee_id, carried_expired`, property, ymd(tday), only)
	if err != nil {
		return rep, err
	}
	type expired struct {
		id, emp uuid.UUID
		days    decimal.Decimal
	}
	var ex []expired
	for rows.Next() {
		var x expired
		if err := rows.Scan(&x.id, &x.emp, &x.days); err != nil {
			rows.Close()
			return rep, err
		}
		ex = append(ex, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	for _, x := range ex {
		if err := ledger(ctx, tx, property, x.id, x.emp, "expiry", x.days.Neg(), nil, "carried-over days lapsed"); err != nil {
			return rep, err
		}
		rep.Expired++
	}
	// collective leave (cuti bersama) deducts annual leave
	if n, err := m.collectiveLeave(ctx, tx, property, lp, tday, only); err != nil {
		return rep, err
	} else {
		rep.Collective = n
	}
	return rep, nil
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// collectiveLeave deducts one day of annual leave per collective leave
// holiday (this year, up to today) from every balance, once.
func (m *Module) collectiveLeave(ctx context.Context, tx pgx.Tx, property uuid.UUID, lp hris.LeavePolicy, tday time.Time, only *uuid.UUID) (int, error) {
	var annual string
	for _, lt := range lp.LeaveTypes {
		if lt.HasBalance() {
			annual = lt.Code
			break
		}
	}
	if annual == "" {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `SELECT h.id, h.name, h.holiday_date, b.id, b.employee_id FROM hris.holidays h
		JOIN hris.leave_balances b ON b.property_id = h.property_id AND b.leave_type = $3 AND b.year = extract(year FROM h.holiday_date)::int
		  AND (b.granted_on IS NULL OR b.granted_on <= h.holiday_date)
		WHERE h.property_id = $1 AND h.deducts_annual_leave AND h.status = 'active' AND h.archived_at IS NULL
		  AND h.holiday_date <= $2::date AND extract(year FROM h.holiday_date)::int = $4
		  AND ($5::uuid IS NULL OR b.employee_id = $5)
		  AND NOT EXISTS (SELECT 1 FROM hris.leave_balance_entries x WHERE x.balance_id = b.id AND x.note = 'collective_leave:' || h.id::text)`,
		property, ymd(tday), annual, tday.Year(), only)
	if err != nil {
		return 0, err
	}
	type item struct {
		hid, bid, emp uuid.UUID
		name          string
		day           time.Time
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.hid, &it.name, &it.day, &it.bid, &it.emp); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `UPDATE hris.leave_balances SET used = used + 1 WHERE id = $1`, it.bid); err != nil {
			return 0, err
		}
		if err := ledger(ctx, tx, property, it.bid, it.emp, "usage", decimal.NewFromInt(-1), nil, "collective_leave:"+it.hid.String()); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func (m *Module) balancesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]LeaveBalanceView, error) {
	property := handle.Property(ctx)
	year := handle.QueryInt(r, "year", today(ctx, tx, property).Year())
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	return handle.List[LeaveBalanceView](tx.Query(ctx, balanceSelect+` WHERE b.property_id = $1 AND b.year = $2 AND ($3::uuid IS NULL OR b.employee_id = $3)
		AND ($4::uuid IS NULL OR e.org_unit_id = $4) AND ($5 = '' OR b.leave_type = $5) ORDER BY e.full_name, b.leave_type LIMIT 2000`,
		property, year, emp, unit, r.URL.Query().Get("leaveType")))
}

func (m *Module) essBalancesHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]LeaveBalanceView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	tday := today(ctx, tx, e.PropertyID)
	return handle.List[LeaveBalanceView](tx.Query(ctx, balanceSelect+` WHERE b.employee_id = $1 AND b.year IN ($2, $2 - 1) ORDER BY b.year DESC, b.leave_type`,
		e.ID, tday.Year()))
}

func (m *Module) adjustHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req BalanceAdjustRequest) (LeaveBalanceView, error) {
	property := handle.Property(ctx)
	emp, err := employeeAt(ctx, tx, property, req.EmployeeID)
	if err != nil {
		return LeaveBalanceView{}, err
	}
	days, err := handle.Decimal("days", req.Days, decimal.Zero)
	if err != nil {
		return LeaveBalanceView{}, err
	}
	if days.IsZero() || days.Abs().GreaterThan(decimal.NewFromInt(366)) {
		return LeaveBalanceView{}, handle.Invalid("days", "invalid", "a non-zero number of days")
	}
	if strings.TrimSpace(req.Note) == "" {
		return LeaveBalanceView{}, handle.Invalid("note", "required", "explain the adjustment")
	}
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, property, clock.Now())
	if err != nil {
		return LeaveBalanceView{}, err
	}
	lt, ok := lp.LeaveType(req.LeaveType)
	if !ok || !lt.HasBalance() {
		return LeaveBalanceView{}, handle.Invalid("leaveType", "invalid", "choose a leave type with a balance")
	}
	tday := today(ctx, tx, property)
	if req.Year < 2000 || req.Year > tday.Year()+1 {
		return LeaveBalanceView{}, handle.Invalid("year", "invalid", "choose a year up to next year")
	}
	b, err := m.ensureBalance(ctx, tx, emp, lt, lp, ref.Version, req.Year, tday, nil)
	if err != nil {
		return LeaveBalanceView{}, err
	}
	if b == nil {
		bid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_balances (id, property_id, employee_id, leave_type, year, policy_version) VALUES ($1,$2,$3,$4,$5,$6)`,
			bid, property, emp.ID, lt.Code, req.Year, ref.Version); err != nil {
			return LeaveBalanceView{}, err
		}
		if b, err = loadBalance(ctx, tx, emp.ID, lt.Code, req.Year, true); err != nil {
			return LeaveBalanceView{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.leave_balances SET adjusted = adjusted + $2 WHERE id = $1`, b.ID, days); err != nil {
		return LeaveBalanceView{}, err
	}
	if err := ledger(ctx, tx, property, b.ID, emp.ID, "adjustment", days, nil, req.Note); err != nil {
		return LeaveBalanceView{}, err
	}
	out, err := getOne[LeaveBalanceView]("leave balance")(tx.Query(ctx, balanceSelect+` WHERE b.id = $1`, b.ID))
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "adjust", EntityType: "hris.leave_balance", EntityID: b.ID.String(),
		EntityLabel: fmt.Sprintf("%s · %s %d", emp.EmployeeNo, lt.Code, req.Year), PropertyID: &property, Reason: req.Note,
		Before: map[string]any{"available": b.available().String()}, After: map[string]any{"available": out.Available, "adjusted": days.String()}})
}

func (m *Module) accrueHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req LeaveAccrueRequest) (LeaveAccrualReport, error) {
	property := handle.Property(ctx)
	if req.EmployeeID != nil {
		if _, err := employeeAt(ctx, tx, property, *req.EmployeeID); err != nil {
			return LeaveAccrualReport{}, err
		}
	}
	rep, err := m.accrue(ctx, tx, property, req.EmployeeID)
	if err != nil {
		return rep, err
	}
	return rep, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "accrue", EntityType: "hris.leave_balance", EntityLabel: "Leave accrual",
		PropertyID: &property, After: rep})
}

// ── requests ─────────────────────────────────────────────────────────────

func (m *Module) loadLeave(ctx context.Context, q pgx.Tx, rid uuid.UUID) (LeaveRequestView, error) {
	r, err := getOne[LeaveRequestView]("leave request")(q.Query(ctx, leaveSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return r, err
	}
	r.Approvals, err = m.steps(ctx, q, KindLeave, rid)
	return r, err
}

func (m *Module) hrLeaveListHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]LeaveRequestView, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", today(ctx, tx, property).AddDate(0, -3, 0))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(1, 0, 0))
	if err != nil {
		return nil, err
	}
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	list, err := handle.List[LeaveRequestView](tx.Query(ctx, leaveSelect+` WHERE r.property_id = $1 AND r.end_date >= $2::date AND r.start_date <= $3::date
		AND ($4 = '' OR r.status = $4) AND ($5::uuid IS NULL OR r.employee_id = $5) AND ($6::uuid IS NULL OR e.org_unit_id = $6)
		ORDER BY (r.status = 'submitted') DESC, r.start_date DESC LIMIT 1000`, property, ymd(from), ymd(to), r.URL.Query().Get("status"), emp, unit))
	for i := range list {
		list[i].Approvals = []TimeApprovalStep{}
	}
	return list, err
}

func (m *Module) getLeaveHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (LeaveRequestView, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return LeaveRequestView{}, err
	}
	l, err := m.loadLeave(ctx, tx, rid)
	if err == nil && l.PropertyID != handle.Property(ctx) {
		return l, errs.NotFound("leave request")
	}
	return l, err
}

func (m *Module) essLeaveListHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]LeaveRequestView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[LeaveRequestView](tx.Query(ctx, leaveSelect+` WHERE r.employee_id = $1 ORDER BY r.start_date DESC LIMIT 100`, e.ID))
	for i := range list {
		if list[i].Approvals, err = m.steps(ctx, tx, KindLeave, list[i].ID); err != nil {
			return nil, err
		}
	}
	return list, err
}

// leaveDays counts the days of a leave period: working days of the
// employee (published shift, else a working weekday that is no holiday) for
// types counting working days, else calendar days; half a day for half-day
// leave. It also returns the published shifts in the period.
func leaveDays(ctx context.Context, tx pgx.Tx, emp hris.Employee, lt hris.LeaveType, from, to time.Time, half bool) (decimal.Decimal, int, error) {
	ww, err := workWeekOf(ctx, tx, emp, from)
	if err != nil {
		return decimal.Zero, 0, err
	}
	days, shifts := decimal.Zero, 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		a, err := loadAssignment(ctx, tx, emp.ID, d)
		if err != nil {
			return decimal.Zero, 0, err
		}
		if a != nil && a.Kind == "shift" {
			shifts++
		}
		counts := true
		if lt.CountsWorkingDays {
			hol, _, err := holidayOn(ctx, tx, emp.PropertyID, d)
			if err != nil {
				return decimal.Zero, 0, err
			}
			counts = workingDay(a, hol, d.Weekday(), ww)
		}
		if counts {
			days = days.Add(decimal.NewFromInt(1))
		}
	}
	if half && days.IsPositive() {
		days = decimal.RequireFromString("0.5")
	}
	return days, shifts, nil
}

func (m *Module) createLeaveHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req LeaveRequestInput) (LeaveRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req LeaveRequestInput) (LeaveRequestView, error) {
		var emp hris.Employee
		var err error
		if viaHR {
			if req.EmployeeID == nil {
				return LeaveRequestView{}, handle.Invalid("employeeId", "required", "choose the employee")
			}
			emp, err = employeeAt(ctx, tx, handle.Property(ctx), *req.EmployeeID)
		} else {
			if req.EmployeeID != nil {
				return LeaveRequestView{}, handle.Invalid("employeeId", "not_allowed", "you request your own leave")
			}
			emp, err = me(ctx, tx)
		}
		if err != nil {
			return LeaveRequestView{}, err
		}
		rid, err := m.createLeave(ctx, tx, emp, req, viaHR)
		if err != nil {
			return LeaveRequestView{}, err
		}
		return m.loadLeave(ctx, tx, rid)
	}
}

// createLeave validates and submits a leave request.
func (m *Module) createLeave(ctx context.Context, tx pgx.Tx, emp hris.Employee, req LeaveRequestInput, viaHR bool) (uuid.UUID, error) {
	start, err := mustDate("startDate", req.StartDate)
	if err != nil {
		return uuid.Nil, err
	}
	end := start
	if p, err := parseDate("endDate", req.EndDate); err != nil {
		return uuid.Nil, err
	} else if p != nil {
		end = *p
	}
	if end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return uuid.Nil, handle.Invalid("endDate", "invalid", "the leave ends on or after its start (at most a year)")
	}
	half := strings.TrimSpace(req.HalfDay)
	if half != "" && (half != "am" && half != "pm") {
		return uuid.Nil, enumErr("halfDay", []string{"am", "pm"})
	}
	if half != "" && !end.Equal(start) {
		return uuid.Nil, handle.Invalid("halfDay", "invalid", "half-day leave is for a single day")
	}
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, emp.PropertyID, hris.PolicyTime(start))
	if err != nil {
		return uuid.Nil, err
	}
	lt, ok := lp.LeaveType(req.LeaveType)
	if !ok {
		return uuid.Nil, handle.Invalid("leaveType", "invalid", "choose a leave type of the Leave Policy")
	}
	if lt.Gender != "" && (emp.Gender == nil || *emp.Gender != lt.Gender) {
		return uuid.Nil, handle.Invalid("leaveType", "not_eligible", lt.Name+" is for "+lt.Gender+" employees")
	}
	if lt.EligibleAfterMonths > 0 && (emp.JoinDate == nil || hris.ServiceMonths(*emp.JoinDate, start) < lt.EligibleAfterMonths) {
		return uuid.Nil, handle.Invalid("leaveType", "not_eligible", fmt.Sprintf("%s needs %d months of service", lt.Name, lt.EligibleAfterMonths))
	}
	if lt.RequiresDocument && req.DocumentFileID == nil {
		return uuid.Nil, handle.Invalid("documentFileId", "required", lt.Name+" needs a supporting document (e.g. a doctor's letter)")
	}
	tday := today(ctx, tx, emp.PropertyID)
	if !viaHR && lt.HasBalance() && lp.MinNoticeDays > 0 && start.Before(tday.AddDate(0, 0, lp.MinNoticeDays)) {
		return uuid.Nil, handle.Invalid("startDate", "notice", fmt.Sprintf("%s is requested at least %d days ahead", lt.Name, lp.MinNoticeDays))
	}
	if !emp.EmployedOn(start) || !emp.EmployedOn(end) {
		return uuid.Nil, handle.Invalid("startDate", "not_employed", "the employee is not employed for the whole period")
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, start, end); err != nil {
		return uuid.Nil, err
	}
	var overlap string
	err = tx.QueryRow(ctx, `SELECT number FROM hris.leave_requests WHERE employee_id = $1 AND status IN ('submitted', 'approved')
		AND start_date <= $3::date AND end_date >= $2::date LIMIT 1`, emp.ID, ymd(start), ymd(end)).Scan(&overlap)
	if err == nil {
		return uuid.Nil, errs.Conflict("leave_overlap", "the period overlaps leave "+overlap)
	}
	if !dbtx.IsNoRows(err) {
		return uuid.Nil, err
	}
	days, conflicts, err := leaveDays(ctx, tx, emp, lt, start, end, half != "")
	if err != nil {
		return uuid.Nil, err
	}
	if !days.IsPositive() {
		return uuid.Nil, handle.Invalid("startDate", "no_working_days", "the period has no working days")
	}
	if lt.Accrual == "per_event" && lt.Days > 0 && days.GreaterThan(decimal.NewFromInt(int64(lt.Days))) {
		return uuid.Nil, handle.Invalid("endDate", "too_long", fmt.Sprintf("%s is at most %d days per event", lt.Name, lt.Days))
	}
	var balanceYear *int
	if lt.HasBalance() {
		if start.Year() != end.Year() {
			return uuid.Nil, handle.Invalid("endDate", "crosses_year", "request the days of each year separately")
		}
		y := start.Year()
		balanceYear = &y
		b, err := m.ensureBalance(ctx, tx, emp, lt, lp, ref.Version, y, tday, nil)
		if err != nil {
			return uuid.Nil, err
		}
		if b == nil || (b.GrantedOn != nil && start.Before(*b.GrantedOn)) {
			from := ""
			if g, ok := hris.AnnualGrant(lt, derefTime(emp.JoinDate), y); ok {
				from = " from " + ymd(g)
			}
			return uuid.Nil, handle.Invalid("leaveType", "no_balance", lt.Name+" is not available yet"+from)
		}
		var pending decimal.Decimal
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(days), 0) FROM hris.leave_requests WHERE employee_id = $1 AND leave_type = $2 AND status = 'submitted'
			AND extract(year FROM start_date)::int = $3`, emp.ID, lt.Code, y).Scan(&pending); err != nil {
			return uuid.Nil, err
		}
		if avail := b.available().Sub(pending); !lp.AllowNegativeBalance && days.GreaterThan(avail) {
			return uuid.Nil, handle.Invalid("endDate", "insufficient_balance", fmt.Sprintf("only %s days of %s are available", avail.String(), lt.Name))
		}
	}
	number, err := yearlyNumber(ctx, tx, emp.PropertyID, "LV", tday.Year())
	if err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_requests (id, property_id, number, employee_id, leave_type, start_date, end_date, half_day, days,
		balance_year, paid, reason, document_file_id, schedule_conflicts, policy_version, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)`, rid, emp.PropertyID, number, emp.ID, lt.Code, ymd(start), ymd(end),
		nullStr(half), days, balanceYear, lt.Paid, nullStr(req.Reason), req.DocumentFileID, conflicts, ref.Version, actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return uuid.Nil, handle.Invalid("documentFileId", "not_found", "upload the document first")
		}
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.leave_request", EntityID: rid.String(),
		EntityLabel: number + " · " + emp.FullName, PropertyID: &emp.PropertyID, After: map[string]any{"leaveType": lt.Code, "startDate": ymd(start),
			"endDate": ymd(end), "days": days.String(), "viaHR": viaHR}}); err != nil {
		return uuid.Nil, err
	}
	summary, attrs, err := m.describe(ctx, tx, KindLeave, rid)
	if err != nil {
		return uuid.Nil, err
	}
	// the approval levels in force today (entitlement rules: at the start date)
	now, _, err := hris.LoadLeavePolicy(ctx, tx, emp.PropertyID, clock.Now())
	if err != nil {
		return uuid.Nil, err
	}
	return rid, m.startApproval(ctx, tx, KindLeave, rid, emp, now.ApprovalLevels, summary, attrs)
}

func (m *Module) hrDecideLeave(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (LeaveRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (LeaveRequestView, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return LeaveRequestView{}, err
		}
		if err := m.decide(ctx, tx, KindLeave, rid, approve, req.Note, true); err != nil {
			return LeaveRequestView{}, err
		}
		out, err := m.loadLeave(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.leave_request", EntityID: rid.String(), EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &out.PropertyID,
			Reason: req.Note, After: map[string]any{"status": out.Status}})
	}
}

// applyLeave reduces the balance (carried-over days first while valid),
// marks the shifts of the period On Leave, re-evaluates the days up to
// today and publishes hris.leave_approved.
func (m *Module) applyLeave(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	var emp hris.Employee
	var lt string
	var start, end time.Time
	var days decimal.Decimal
	var half *string
	var paid bool
	var year *int
	var number string
	var version int
	var eid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT employee_id, leave_type, start_date, end_date, days, half_day, paid, balance_year, number, policy_version
		FROM hris.leave_requests WHERE id = $1`, rid).Scan(&eid, &lt, &start, &end, &days, &half, &paid, &year, &number, &version); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return err
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, start, end); err != nil {
		return err
	}
	lp, _, err := hris.LoadLeavePolicy(ctx, tx, emp.PropertyID, hris.PolicyTime(start))
	if err != nil {
		return err
	}
	var remaining *string
	if year != nil {
		b, err := loadBalance(ctx, tx, emp.ID, lt, *year, true)
		if err != nil {
			return err
		}
		if b == nil {
			return errs.Conflict("no_balance", "the leave balance of "+itoa(*year)+" is missing")
		}
		if !lp.AllowNegativeBalance && days.GreaterThan(b.available()) {
			return errs.Conflict("insufficient_balance", "only "+b.available().String()+" days are available")
		}
		carried := decimal.Zero
		if b.Expires != nil && !start.After(*b.Expires) {
			carried = decimal.Min(days, b.carriedLeft())
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.leave_balances SET used = used + $2, used_carried = used_carried + $3 WHERE id = $1`, b.ID, days, carried); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.leave_requests SET carried_days = $2 WHERE id = $1`, rid, carried); err != nil {
			return err
		}
		if err := ledger(ctx, tx, emp.PropertyID, b.ID, emp.ID, "usage", days.Neg(), &rid, number); err != nil {
			return err
		}
		r := b.available().Sub(days).String()
		remaining = &r
	}
	if half == nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'on_leave' WHERE employee_id = $1 AND work_date BETWEEN $2::date AND $3::date
			AND status = 'scheduled'`, emp.ID, ymd(start), ymd(end)); err != nil {
			return err
		}
	}
	if err := m.reevaluate(ctx, tx, emp, start, end); err != nil {
		return err
	}
	ltName := lt
	if t, ok := lp.LeaveType(lt); ok {
		ltName = t.Name
	}
	_, err = m.Events.Publish(ctx, tx, hris.EventLeaveApproved, "hris.leave_request", &rid, &emp.PropertyID, hris.LeaveApproved{LeaveRequestID: rid,
		Number: number, PropertyID: emp.PropertyID, EmployeeID: emp.ID, EmployeeNo: emp.EmployeeNo, FullName: emp.FullName, LeaveType: lt,
		LeaveTypeName: ltName, Paid: paid, StartDate: ymd(start), EndDate: ymd(end), HalfDay: half, Days: days.String(), BalanceYear: year,
		RemainingBalance: remaining, PolicyVersion: version})
	return err
}

// reevaluate recomputes the attendance days of a period up to today.
func (m *Module) reevaluate(ctx context.Context, tx pgx.Tx, emp hris.Employee, from, to time.Time) error {
	tday := today(ctx, tx, emp.PropertyID)
	if to.After(tday) {
		to = tday
	}
	pol := newPolicies(emp.PropertyID)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if _, err := m.recomputeDay(ctx, tx, emp, d, d.Before(tday), pol); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) cancelLeaveHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (LeaveRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (LeaveRequestView, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return LeaveRequestView{}, err
		}
		h, err := loadHeader(ctx, tx, specs[KindLeave], rid, true)
		if err != nil {
			return LeaveRequestView{}, err
		}
		if viaHR && h.PropertyID != handle.Property(ctx) {
			return LeaveRequestView{}, errs.NotFound("leave request")
		}
		if !viaHR {
			e, err := me(ctx, tx)
			if err != nil {
				return LeaveRequestView{}, err
			}
			if h.EmployeeID != e.ID {
				return LeaveRequestView{}, errs.NotFound("leave request")
			}
		}
		switch h.Status {
		case hris.RequestSubmitted:
			if err := m.withdraw(ctx, tx, KindLeave, h, req.Note); err != nil {
				return LeaveRequestView{}, err
			}
		case hris.RequestApproved:
			if viaHR && strings.TrimSpace(req.Note) == "" {
				return LeaveRequestView{}, handle.Invalid("note", "required", "explain the cancellation")
			}
			if err := m.reverseLeave(ctx, tx, rid, viaHR); err != nil {
				return LeaveRequestView{}, err
			}
			if err := m.finalizeCancelled(ctx, tx, KindLeave, rid, req.Note); err != nil {
				return LeaveRequestView{}, err
			}
		default:
			return LeaveRequestView{}, statusErr("the leave request", h.Status)
		}
		out, err := m.loadLeave(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.leave_request", EntityID: rid.String(),
			EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &out.PropertyID, Reason: req.Note, Before: map[string]any{"status": h.Status},
			After: map[string]any{"status": out.Status}})
	}
}

// finalizeCancelled marks an approved request cancelled (no effect hook).
func (m *Module) finalizeCancelled(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, note string) error {
	_, err := tx.Exec(ctx, `UPDATE `+specs[kind].table+` SET status = 'cancelled', decision_note = coalesce(nullif($2, ''), decision_note), updated_by = $3
		WHERE id = $1`, id, note, actor(ctx))
	if err == nil && kind == KindLeave {
		_, err = tx.Exec(ctx, `UPDATE hris.leave_requests SET cancelled_at = now() WHERE id = $1`, id)
	}
	return err
}

// reverseLeave restores the balance and the shifts of cancelled leave. The
// employee cancels only leave that has not started.
func (m *Module) reverseLeave(ctx context.Context, tx pgx.Tx, rid uuid.UUID, viaHR bool) error {
	var eid uuid.UUID
	var lt, number string
	var start, end time.Time
	var days, carried decimal.Decimal
	var year *int
	if err := tx.QueryRow(ctx, `SELECT employee_id, leave_type, start_date, end_date, days, carried_days, balance_year, number FROM hris.leave_requests
		WHERE id = $1`, rid).Scan(&eid, &lt, &start, &end, &days, &carried, &year, &number); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return err
	}
	if !viaHR && !start.After(today(ctx, tx, emp.PropertyID)) {
		return errs.Conflict("leave_started", "leave that has started is cancelled by HR")
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, start, end); err != nil {
		return err
	}
	if year != nil {
		b, err := loadBalance(ctx, tx, emp.ID, lt, *year, true)
		if err != nil {
			return err
		}
		if b != nil {
			if _, err := tx.Exec(ctx, `UPDATE hris.leave_balances SET used = greatest(used - $2, 0), used_carried = greatest(used_carried - $3, 0) WHERE id = $1`,
				b.ID, days, carried); err != nil {
				return err
			}
			if err := ledger(ctx, tx, emp.PropertyID, b.ID, emp.ID, "reversal", days, &rid, number+" cancelled"); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'scheduled' WHERE employee_id = $1 AND work_date BETWEEN $2::date AND $3::date
		AND status = 'on_leave'`, emp.ID, ymd(start), ymd(end)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.leave_requests SET status = 'cancelled' WHERE id = $1`, rid); err != nil {
		return err
	}
	return m.reevaluate(ctx, tx, emp, start, end)
}

// ── calendar ─────────────────────────────────────────────────────────────

const calendarSQL = `SELECT r.id, r.number, r.employee_id, e.full_name AS employee_name, ou.name AS org_unit_name, r.leave_type, r.start_date, r.end_date,
	trim_scale(r.days)::text AS days, r.status,
	(SELECT count(*) FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id AND s.status = 'published'
	 WHERE a.employee_id = r.employee_id AND a.work_date BETWEEN r.start_date AND r.end_date AND a.kind = 'shift'
	   AND a.status = CASE WHEN r.status = 'approved' THEN 'on_leave' ELSE 'scheduled' END)::int AS conflicts
	FROM hris.leave_requests r JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

func (m *Module) calendarHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]LeaveCalendarEntry, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	return handle.List[LeaveCalendarEntry](tx.Query(ctx, calendarSQL+` WHERE r.property_id = $1 AND r.status IN ('submitted', 'approved')
		AND r.start_date <= $3::date AND r.end_date >= $2::date AND ($4::uuid IS NULL OR e.org_unit_id IN (WITH RECURSIVE u AS (SELECT id FROM hris.org_units
		WHERE id = $4 UNION ALL SELECT c.id FROM hris.org_units c JOIN u ON c.parent_id = u.id) SELECT id FROM u))
		ORDER BY r.start_date, e.full_name`, property, ymd(from), ymd(to), unit))
}

func (m *Module) teamCalendarHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]LeaveCalendarEntry, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	from, err := handle.QueryDate(r, "from", today(ctx, tx, e.PropertyID))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	return handle.List[LeaveCalendarEntry](tx.Query(ctx, calendarSQL+` WHERE r.employee_id = ANY ($1) AND r.status IN ('submitted', 'approved')
		AND r.start_date <= $3::date AND r.end_date >= $2::date ORDER BY r.start_date, e.full_name`, empIDs(team), ymd(from), ymd(to)))
}

// ── import (EP-28 FR-MIG-P5-02) ──────────────────────────────────────────

func (m *Module) importHTTP(w http.ResponseWriter, r *http.Request) {
	handle.Write(m.DB, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, req LeaveImportRequest) (LeaveImportReport, error) {
		property := handle.Property(ctx)
		rep, err := m.importBalances(ctx, tx, property, strings.NewReader(req.CSV), req.DryRun)
		if err != nil {
			return rep, err
		}
		return rep, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "import", EntityType: "hris.leave_balance", EntityLabel: "Leave balances",
			PropertyID: &property, After: map[string]any{"rows": rep.Rows, "inserted": rep.Inserted, "updated": rep.Updated, "failed": rep.Failed,
				"dryRun": rep.DryRun}})
	})(w, r)
}

// ImportLeaveBalances loads leave balances at a property from CSV in its
// own transaction (`oneclub import hris --leave-balances`); ctx carries the
// caller (system for the CLI) with the property selected.
func (m *Module) ImportLeaveBalances(ctx context.Context, property uuid.UUID, src io.Reader, dryRun bool) (LeaveImportReport, error) {
	var rep LeaveImportReport
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		rep, err = m.importBalances(ctx, tx, property, src, dryRun)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "import", EntityType: "hris.leave_balance", EntityLabel: "Leave balances (CLI)",
			PropertyID: &property, After: map[string]any{"rows": rep.Rows, "inserted": rep.Inserted, "updated": rep.Updated, "failed": rep.Failed,
				"dryRun": rep.DryRun}})
	})
	return rep, err
}

// importBalances upserts balances per employee, leave type and year (one
// savepoint per row; repeatable: the same file updates the same rows).
func (m *Module) importBalances(ctx context.Context, tx pgx.Tx, property uuid.UUID, src io.Reader, dryRun bool) (LeaveImportReport, error) {
	rep := LeaveImportReport{DryRun: dryRun, Issues: []LeaveImportIssue{}}
	rd := csv.NewReader(src)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	records, err := rd.ReadAll()
	if err != nil {
		return rep, handle.Invalid("csv", "invalid", "malformed CSV: "+err.Error())
	}
	if len(records) < 2 {
		return rep, handle.Invalid("csv", "empty", "a header row and at least one data row are required")
	}
	col := map[string]int{}
	for i, h := range records[0] {
		h = strings.TrimSpace(h)
		if !containsStr(LeaveImportColumns, h) {
			return rep, handle.Invalid("csv", "unknown_column", "unknown column "+h+"; columns: "+strings.Join(LeaveImportColumns, ", "))
		}
		col[h] = i
	}
	for _, req := range []string{"employeeNo", "leaveType", "year"} {
		if _, ok := col[req]; !ok {
			return rep, handle.Invalid("csv", "missing_column", "column "+req+" is required")
		}
	}
	lp, ref, err := hris.LoadLeavePolicy(ctx, tx, property, clock.Now())
	if err != nil {
		return rep, err
	}
	outer, err := tx.Begin(ctx)
	if err != nil {
		return rep, err
	}
	for i, rec := range records[1:] {
		rep.Rows++
		get := func(k string) string {
			if j, ok := col[k]; ok && j < len(rec) {
				return strings.TrimSpace(rec[j])
			}
			return ""
		}
		key := get("employeeNo") + "/" + get("leaveType") + "/" + get("year")
		fail := func(msg string) {
			rep.Failed++
			rep.Issues = append(rep.Issues, LeaveImportIssue{Row: i + 2, Key: key, Message: msg})
		}
		emp, err := hris.EmployeeByNo(ctx, outer, property, get("employeeNo"))
		if err != nil {
			return rep, err
		}
		if emp == nil {
			fail("employee not found")
			continue
		}
		lt, ok := lp.LeaveType(get("leaveType"))
		if !ok || !lt.HasBalance() {
			fail("leave type unknown or without a balance")
			continue
		}
		year, err := strconv.Atoi(get("year"))
		if err != nil || year < 2000 || year > 2100 {
			fail("year must be a year")
			continue
		}
		num := func(k string) (decimal.Decimal, bool) {
			v := get(k)
			if v == "" {
				return decimal.Zero, true
			}
			d, err := decimal.NewFromString(v)
			return d, err == nil && !d.IsNegative() && d.LessThanOrEqual(decimal.NewFromInt(366))
		}
		entitled, ok1 := num("entitled")
		carried, ok2 := num("carriedOver")
		used, ok3 := num("used")
		if !ok1 || !ok2 || !ok3 {
			fail("entitled, carriedOver and used are numbers of days (0–366)")
			continue
		}
		var expires *string
		if v := get("carryOverExpiresOn"); v != "" {
			if _, err := time.Parse("2006-01-02", v); err != nil {
				fail("carryOverExpiresOn must be YYYY-MM-DD")
				continue
			}
			expires = &v
		} else if carried.IsPositive() {
			e := ymd(lp.CarryOverExpiryDate(year))
			expires = &e
		}
		sp, err := outer.Begin(ctx)
		if err != nil {
			return rep, err
		}
		var bid uuid.UUID
		var inserted bool
		err = sp.QueryRow(ctx, `INSERT INTO hris.leave_balances (id, property_id, employee_id, leave_type, year, entitled, carried_over, carry_over_expires_on,
			used, policy_version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (employee_id, leave_type, year) DO UPDATE SET entitled = EXCLUDED.entitled, carried_over = EXCLUDED.carried_over,
			  carry_over_expires_on = EXCLUDED.carry_over_expires_on, used = greatest(EXCLUDED.used, hris.leave_balances.used_carried),
			  carried_expired = least(hris.leave_balances.carried_expired, EXCLUDED.carried_over)
			RETURNING id, (xmax = 0)`, id.New(), property, emp.ID, lt.Code, year, entitled, carried, expires, used, ref.Version).Scan(&bid, &inserted)
		if err == nil {
			err = ledger(ctx, sp, property, bid, emp.ID, "import", entitled.Add(carried).Sub(used), nil, "imported: "+get("note"))
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				fail(de.Message)
				continue
			}
			if ok, _ := dbtx.IsCheckViolation(err); ok {
				fail("the balance is inconsistent (used carried-over days exceed the carry-over)")
				continue
			}
			return rep, err
		}
		if err := sp.Commit(ctx); err != nil {
			return rep, err
		}
		if inserted {
			rep.Inserted++
		} else {
			rep.Updated++
		}
	}
	if dryRun {
		return rep, outer.Rollback(ctx)
	}
	return rep, outer.Commit(ctx)
}
