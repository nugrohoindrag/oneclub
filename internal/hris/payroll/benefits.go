package payroll

// Benefits (HRIS improvement phase C, spec §24): benefit plans (resource
// hris.benefit_plan) with eligibility — employment statuses, worker
// categories, minimum service — and the employer / employee contributions;
// enrollments of employees with an effective period; the employee
// contribution of each regular payroll period is a payroll input
// (deduction BENEFIT_EE, source "benefit") marked as consumed by the posted
// run, booked by Accounting to Benefit Contributions Payable. Employees see
// their benefits in Employee Self Service.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Benefit permissions and resource key.
const (
	KeyBenefitPlan     = "hris.benefit_plan"
	PermBenefitView    = "hris.benefit_enrollment.view"
	PermBenefitManage  = "hris.benefit_enrollment.manage"
	BenefitComponent   = "BENEFIT_EE"
	benefitInputSource = "benefit"
	benefitSourceType  = "hris.benefit_deduction"
)

// BenefitTypes are the kinds of benefit plans.
var BenefitTypes = []string{"health_insurance", "life_insurance", "pension", "allowance", "facility", "other"}

func benefitPlanDef() *resource.Def {
	return &resource.Def{
		Key: KeyBenefitPlan, Module: hris.Module, Perm: KeyBenefitPlan, Path: "/api/v1/hris/benefit-plans", Table: "hris.benefit_plans",
		Name: "Benefit Plan", Plural: "Benefit Plans", SchemaName: "BenefitPlan", Tag: "HRIS Benefits", PropertyScoped: true, Archive: true,
		CodeField: "code", OrderBy: "code, id",
		Fields: []resource.Field{
			{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 30, Upper: true, Pattern: componentCodeRe,
				PatternMsg: "1–30 characters: A–Z, 0–9, - or _", Search: true, CreateOnly: true},
			resource.Name(),
			{Name: "benefitType", Column: "benefit_type", Label: "Type", Kind: resource.Enum, Enum: BenefitTypes, Default: "health_insurance", Filter: true},
			{Name: "provider", Column: "provider", Label: "Provider", Kind: resource.String, Max: 120},
			{Name: "employerContribution", Column: "employer_contribution", Label: "Employer Contribution per Month", Kind: resource.Decimal,
				Min: resource.Min(0)},
			{Name: "employeeContribution", Column: "employee_contribution", Label: "Employee Contribution per Month (payroll deduction)",
				Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "eligibleStatuses", Column: "eligible_statuses", Label: "Eligible Employment Statuses (empty = all)", Kind: resource.StringList,
				Enum: []string{hris.StatusProbation, hris.StatusContract, hris.StatusPermanent}},
			{Name: "eligibleCategories", Column: "eligible_categories", Label: "Eligible Worker Categories (empty = all)", Kind: resource.StringList,
				Enum: []string{"regular", "daily", "intern"}},
			{Name: "minServiceMonths", Column: "min_service_months", Label: "Minimum Service (months)", Kind: resource.Int, Default: int64(0),
				Min: resource.Min(0), MaxN: resource.Max(600)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			resource.Status("active", "inactive")},
	}
}

// BenefitEnrollment is an employee's enrollment in a plan.
type BenefitEnrollment struct {
	ID                   uuid.UUID  `json:"id" db:"id"`
	EmployeeID           uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo           string     `json:"employeeNo" db:"employee_no"`
	EmployeeName         string     `json:"employeeName" db:"employee_name"`
	PlanID               uuid.UUID  `json:"planId" db:"plan_id"`
	PlanCode             string     `json:"planCode" db:"plan_code"`
	PlanName             string     `json:"planName" db:"plan_name"`
	BenefitType          string     `json:"benefitType" db:"benefit_type"`
	Provider             *string    `json:"provider" db:"provider"`
	EffectiveFrom        time.Time  `json:"effectiveFrom" db:"effective_from"`
	EffectiveTo          *time.Time `json:"effectiveTo" db:"effective_to"`
	EmployerContribution string     `json:"employerContribution" db:"employer_contribution"`
	EmployeeContribution string     `json:"employeeContribution" db:"employee_contribution"`
	Status               string     `json:"status" db:"status" enum:"active,ended,cancelled"`
	Notes                *string    `json:"notes" db:"notes"`
	EndReason            *string    `json:"endReason" db:"end_reason"`
	Deducted             string     `json:"deducted" db:"deducted" doc:"Employee contributions deducted by posted payroll runs"`
}

// BenefitEnrollmentInput enrolls an employee.
type BenefitEnrollmentInput struct {
	EmployeeID           uuid.UUID `json:"employeeId"`
	PlanID               uuid.UUID `json:"planId"`
	EffectiveFrom        string    `json:"effectiveFrom"`
	EmployerContribution string    `json:"employerContribution,omitempty" doc:"Default: the plan"`
	EmployeeContribution string    `json:"employeeContribution,omitempty" doc:"Default: the plan"`
	Notes                string    `json:"notes,omitempty"`
}

// BenefitEndInput ends an enrollment.
type BenefitEndInput struct {
	EffectiveTo string `json:"effectiveTo"`
	Reason      string `json:"reason"`
}

// BenefitEligibility is one employee checked against a plan.
type BenefitEligibility struct {
	EmployeeID   uuid.UUID `json:"employeeId"`
	EmployeeNo   string    `json:"employeeNo"`
	EmployeeName string    `json:"employeeName"`
	Eligible     bool      `json:"eligible"`
	Reason       string    `json:"reason,omitempty"`
	Enrolled     bool      `json:"enrolled"`
}

const enrollmentSelect = `SELECT b.id, b.employee_id, e.employee_no, e.full_name AS employee_name, b.plan_id, p.code AS plan_code, p.name AS plan_name,
	p.benefit_type, p.provider, b.effective_from, b.effective_to, trim_scale(b.employer_contribution)::text AS employer_contribution,
	trim_scale(b.employee_contribution)::text AS employee_contribution, b.status, b.notes, b.end_reason,
	(SELECT trim_scale(coalesce(sum(d.amount), 0))::text FROM hris.benefit_deductions d WHERE d.enrollment_id = b.id AND d.consumed_at IS NOT NULL) AS deducted
	FROM hris.benefit_enrollments b JOIN hris.employees e ON e.id = b.employee_id JOIN hris.benefit_plans p ON p.id = b.plan_id`

func init() {
	hris.RegisterPayrollInputSource(hris.PayrollInputSource{Code: benefitInputSource, Lines: benefitLines, Consumed: benefitConsumed})
	hris.RegisterESSSection(hris.ESSSection{Key: "benefits", Label: "My Benefits", LabelID: "Benefit Saya", Icon: "verified_user",
		Path: "/ops/ess/benefits", Order: 63})
}

func (m *Module) registerBenefits(reg *route.Registry) {
	tag := "HRIS Benefits"
	base := "/api/v1/hris/benefit-enrollments"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Benefit enrollments", Permission: PermBenefitView, Response: BenefitEnrollment{},
		List: true, Query: []route.Param{{Name: "planId"}, {Name: "employeeId"}, {Name: "status", Enum: []string{"active", "ended", "cancelled"}}},
		Handler: listRead(m.DB, m.enrollmentsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Enroll an eligible employee in a benefit plan", Permission: PermBenefitManage,
		Request: BenefitEnrollmentInput{}, Response: BenefitEnrollment{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.enrollHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:end", Summary: "End an enrollment (last covered day; cancelled before it starts)",
		Permission: PermBenefitManage, Request: BenefitEndInput{}, Response: BenefitEnrollment{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.endEnrollmentHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/benefit-plans/{id}/eligibility",
		Summary: "Active employees checked against the plan's eligibility", Permission: PermBenefitView, Response: BenefitEligibility{}, List: true,
		Handler: listRead(m.DB, m.eligibilityHTTP)})
	add(reg, "Employee Self Service", route.Route{Method: http.MethodGet, Path: "/api/v1/ess/benefits", Summary: "My benefits",
		Permission: hris.PermissionESS, Response: BenefitEnrollment{}, List: true, Handler: listRead(m.DB, m.myBenefitsHTTP)})
}

func (m *Module) enrollmentsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]BenefitEnrollment, error) {
	plan, err := uuidParam(r, "planId")
	if err != nil {
		return nil, err
	}
	emp, err := uuidParam(r, "employeeId")
	if err != nil {
		return nil, err
	}
	return handle.List[BenefitEnrollment](tx.Query(ctx, enrollmentSelect+` WHERE b.property_id = $1 AND ($2::uuid IS NULL OR b.plan_id = $2)
		AND ($3::uuid IS NULL OR b.employee_id = $3) AND ($4 = '' OR b.status = $4) ORDER BY b.status, p.code, e.full_name LIMIT 1000`,
		handle.Property(ctx), plan, emp, filterParam(r, "status")))
}

func (m *Module) enrollment(ctx context.Context, tx pgx.Tx, bid uuid.UUID) (BenefitEnrollment, error) {
	rows, err := tx.Query(ctx, enrollmentSelect+` WHERE b.id = $1`, bid)
	return handle.One[BenefitEnrollment](rows, err, "benefit enrollment")
}

type planRow struct {
	code, name           string
	employer, employee   decimal.Decimal
	statuses, categories []string
	minMonths            int
	status               string
}

func loadPlan(ctx context.Context, q dbtx.Querier, pid, property uuid.UUID) (planRow, error) {
	var p planRow
	err := q.QueryRow(ctx, `SELECT code, name, employer_contribution, employee_contribution, eligible_statuses, eligible_categories, min_service_months, status
		FROM hris.benefit_plans WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, pid, property).
		Scan(&p.code, &p.name, &p.employer, &p.employee, &p.statuses, &p.categories, &p.minMonths, &p.status)
	if dbtx.IsNoRows(err) {
		return p, handle.Invalid("planId", "not_found", "benefit plan not found")
	}
	return p, err
}

// eligible reports whether an employee meets the plan's rules on a day.
func (p planRow) eligible(e hris.Employee, on time.Time) (bool, string) {
	switch {
	case e.Status != "active":
		return false, "not an active employee"
	case e.TerminationDate != nil && !e.TerminationDate.After(on):
		return false, "leaves before the start"
	case len(p.statuses) > 0 && !slices.Contains(p.statuses, e.EmploymentStatus):
		return false, "employment status " + e.EmploymentStatus + " is not eligible"
	case len(p.categories) > 0 && !slices.Contains(p.categories, e.WorkerCategory):
		return false, "worker category " + e.WorkerCategory + " is not eligible"
	}
	if p.minMonths > 0 {
		if e.JoinDate == nil || hris.ServiceMonths(*e.JoinDate, on) < p.minMonths {
			return false, "less than " + itoa(p.minMonths) + " month(s) of service"
		}
	}
	return true, ""
}

func (m *Module) enrollHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req BenefitEnrollmentInput) (BenefitEnrollment, error) {
	property := handle.Property(ctx)
	p, err := loadPlan(ctx, tx, req.PlanID, property)
	if err != nil {
		return BenefitEnrollment{}, err
	}
	if p.status != "active" {
		return BenefitEnrollment{}, handle.Invalid("planId", "inactive", "the plan is inactive")
	}
	e, err := hris.EmployeeByID(ctx, tx, req.EmployeeID)
	if err != nil || e.PropertyID != property {
		return BenefitEnrollment{}, handle.Invalid("employeeId", "not_found", "employee not found")
	}
	from, err := parseDate("effectiveFrom", req.EffectiveFrom)
	if err != nil {
		return BenefitEnrollment{}, err
	}
	if from == nil {
		return BenefitEnrollment{}, handle.Invalid("effectiveFrom", "required", "is required")
	}
	if ok, why := p.eligible(e, *from); !ok {
		return BenefitEnrollment{}, handle.Invalid("employeeId", "not_eligible", e.FullName+": "+why)
	}
	employer, employee := p.employer, p.employee
	if strings.TrimSpace(req.EmployerContribution) != "" {
		if employer, err = handle.Decimal("employerContribution", req.EmployerContribution, dec("0")); err != nil {
			return BenefitEnrollment{}, err
		}
	}
	if strings.TrimSpace(req.EmployeeContribution) != "" {
		if employee, err = handle.Decimal("employeeContribution", req.EmployeeContribution, dec("0")); err != nil {
			return BenefitEnrollment{}, err
		}
	}
	if employer.IsNegative() || employee.IsNegative() {
		return BenefitEnrollment{}, handle.Invalid("employeeContribution", "invalid", "contributions cannot be negative")
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.benefit_enrollments (id, property_id, employee_id, plan_id, effective_from, employer_contribution,
		employee_contribution, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, bid, property, e.ID, req.PlanID, ymd(*from), employer,
		employee, nullStr(req.Notes), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return BenefitEnrollment{}, errs.Conflict("already_enrolled", e.FullName+" is already enrolled in "+p.name)
		}
		return BenefitEnrollment{}, err
	}
	out, err := m.enrollment(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.benefit_enrollment",
		EntityID: bid.String(), EntityLabel: p.code + " · " + e.FullName, PropertyID: &property, After: map[string]any{"plan": p.code,
			"effectiveFrom": ymd(*from), "employerContribution": employer.String(), "employeeContribution": employee.String()}})
}

func (m *Module) endEnrollmentHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req BenefitEndInput) (BenefitEnrollment, error) {
	property := handle.Property(ctx)
	bid, err := handle.ID(r)
	if err != nil {
		return BenefitEnrollment{}, err
	}
	var status string
	var from time.Time
	err = tx.QueryRow(ctx, `SELECT status, effective_from FROM hris.benefit_enrollments WHERE id = $1 AND property_id = $2 FOR UPDATE`, bid, property).
		Scan(&status, &from)
	if dbtx.IsNoRows(err) {
		return BenefitEnrollment{}, errs.NotFound("benefit enrollment")
	}
	if err != nil {
		return BenefitEnrollment{}, err
	}
	if status != "active" {
		return BenefitEnrollment{}, errs.Conflict("enrollment_closed", "the enrollment is "+status)
	}
	to, err := parseDate("effectiveTo", req.EffectiveTo)
	if err != nil {
		return BenefitEnrollment{}, err
	}
	if to == nil {
		return BenefitEnrollment{}, handle.Invalid("effectiveTo", "required", "the last covered day")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return BenefitEnrollment{}, handle.Invalid("reason", "required", "is required")
	}
	next := "ended"
	if to.Before(from) {
		next = "cancelled" // never started: nothing to cover or deduct
		_, err = tx.Exec(ctx, `UPDATE hris.benefit_enrollments SET status = 'cancelled', end_reason = $2, updated_by = $3 WHERE id = $1`, bid,
			strings.TrimSpace(req.Reason), actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE hris.benefit_enrollments SET status = 'ended', effective_to = $2, end_reason = $3, updated_by = $4 WHERE id = $1`, bid,
			ymd(*to), strings.TrimSpace(req.Reason), actor(ctx))
	}
	if err != nil {
		return BenefitEnrollment{}, err
	}
	// deductions not yet paid by a posted run for periods after the end are dropped
	if _, err := tx.Exec(ctx, `DELETE FROM hris.benefit_deductions WHERE enrollment_id = $1 AND consumed_at IS NULL AND period_start > $2`, bid,
		ymd(*to)); err != nil {
		return BenefitEnrollment{}, err
	}
	out, err := m.enrollment(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "end", EntityType: "hris.benefit_enrollment", EntityID: bid.String(),
		EntityLabel: out.PlanCode + " · " + out.EmployeeName, PropertyID: &property, Reason: req.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": next, "effectiveTo": ymd(*to)}})
}

func (m *Module) eligibilityHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]BenefitEligibility, error) {
	property := handle.Property(ctx)
	pid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	p, err := loadPlan(ctx, tx, pid, property)
	if err != nil {
		return nil, errs.NotFound("benefit plan")
	}
	all, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: property})
	if err != nil {
		return nil, err
	}
	enrolled := map[uuid.UUID]bool{}
	rows, err := tx.Query(ctx, `SELECT employee_id FROM hris.benefit_enrollments WHERE plan_id = $1 AND status = 'active'`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e uuid.UUID
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		enrolled[e] = true
	}
	rows.Close()
	day := today(ctx, tx, property)
	out := []BenefitEligibility{}
	for _, e := range all {
		if e.Status != "active" {
			continue
		}
		ok, why := p.eligible(e, day)
		out = append(out, BenefitEligibility{EmployeeID: e.ID, EmployeeNo: e.EmployeeNo, EmployeeName: e.FullName, Eligible: ok, Reason: why,
			Enrolled: enrolled[e.ID]})
	}
	return out, nil
}

func (m *Module) myBenefitsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]BenefitEnrollment, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[BenefitEnrollment](tx.Query(ctx, enrollmentSelect+` WHERE b.employee_id = $1 AND b.status <> 'cancelled'
		ORDER BY b.status, b.effective_from DESC`, e.ID))
}

// benefitLines are the employee contributions of the enrollments covering
// the payroll period (one per enrollment and period, kept in
// hris.benefit_deductions so a recalculation reuses it and a posted run
// consumes it once).
func benefitLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employees []uuid.UUID) ([]hris.PayrollInputLine, error) {
	rows, err := q.Query(ctx, `SELECT b.id, b.employee_id, b.employee_contribution, p.code, p.name FROM hris.benefit_enrollments b
		JOIN hris.benefit_plans p ON p.id = b.plan_id
		WHERE b.property_id = $1 AND b.status IN ('active', 'ended') AND b.employee_contribution > 0 AND b.effective_from <= $3::date
		  AND (b.effective_to IS NULL OR b.effective_to >= $2::date) AND b.employee_id = ANY ($4)
		  AND NOT EXISTS (SELECT 1 FROM hris.benefit_deductions d WHERE d.enrollment_id = b.id AND d.period_start = $2::date AND d.consumed_at IS NOT NULL)`,
		property, ymd(from), ymd(to), employees)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, emp    uuid.UUID
		amount     decimal.Decimal
		code, name string
	}
	var list []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.id, &x.emp, &x.amount, &x.code, &x.name); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []hris.PayrollInputLine{}
	for _, x := range list {
		var did uuid.UUID
		if err := q.QueryRow(ctx, `INSERT INTO hris.benefit_deductions (id, property_id, enrollment_id, period_start, amount) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (enrollment_id, period_start) DO UPDATE SET amount = EXCLUDED.amount RETURNING id`, id.New(), property, x.id, ymd(from),
			x.amount).Scan(&did); err != nil {
			return nil, err
		}
		out = append(out, hris.PayrollInputLine{EmployeeID: x.emp, Source: benefitInputSource, ComponentCode: BenefitComponent,
			Description: "Benefit " + x.name + " (" + x.code + ")", Kind: hris.PayrollInputDeduction, Amount: x.amount, SourceType: benefitSourceType,
			SourceID: did})
	}
	return out, nil
}

// benefitConsumed marks the deductions paid by a posted run.
func benefitConsumed(ctx context.Context, q dbtx.Querier, _ uuid.UUID, run uuid.UUID, lines []hris.PayrollInputLine) error {
	ids := make([]uuid.UUID, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, l.SourceID)
	}
	_, err := q.Exec(ctx, `UPDATE hris.benefit_deductions SET run_id = $2, consumed_at = now() WHERE id = ANY ($1) AND consumed_at IS NULL`, ids, run)
	return err
}
