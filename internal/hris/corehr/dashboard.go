package corehr

// HR Dashboard of the Back Office (HR Manager / HR Admin home, HRIS →
// Dashboard): one read of what needs HR's attention today — headcount,
// attendance of the day, workforce coverage against the staffing
// requirements, employee movement of the month, the latest payroll run and
// the open queues (requests waiting for approval, attendance exceptions,
// expiring contracts and documents). Every figure is read from the HRIS
// tables; each section is filled only when the user may open the screen it
// links to, so the dashboard never shows more than the menus do.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// Permissions of the screens the dashboard sections link to.
const (
	permAttendanceView = "hris.attendance.view"
	permScheduleView   = "hris.schedule.view"
	permLeaveView      = "hris.leave_request.view"
	permPermissionView = "hris.permission_request.view"
	permOvertimeView   = "hris.overtime_request.view"
	permSwapView       = "hris.shift_swap.view"
	permPayrollView    = "hris.payroll_run.view"
	permContractView   = "hris.contract.view"
	permDocumentView   = "hris.employee_document.view"
	permCertView       = "hris.certification.view"
	permProfileView    = "hris.profile_change.view"
)

// dashboardWindow is the look-ahead of expiring contracts and documents and
// the look-back of the attendance exceptions (days).
const dashboardWindow = 30

// HRHeadcount counts the active employees by employment status.
type HRHeadcount struct {
	Total     int `json:"total" doc:"Active employees who have joined"`
	Permanent int `json:"permanent"`
	Contract  int `json:"contract"`
	Probation int `json:"probation"`
	Leaving   int `json:"leaving" doc:"Resignation or termination scheduled"`
}

// HRAttendanceToday is the attendance of the day (attendance days).
type HRAttendanceToday struct {
	Scheduled    int `json:"scheduled" doc:"Employees with a shift today"`
	Present      int `json:"present" doc:"Clocked in (on time, late or leaving early)"`
	Late         int `json:"late"`
	Absent       int `json:"absent"`
	OnLeave      int `json:"onLeave"`
	MissingClock int `json:"missingClock" doc:"Missing clock-in or clock-out"`
}

// HRWorkforceUnit is the coverage of one org unit today.
type HRWorkforceUnit struct {
	OrgUnitID uuid.UUID `json:"orgUnitId"`
	Name      string    `json:"name"`
	Required  int       `json:"required" doc:"Minimum staff of the staffing requirements of the day"`
	Scheduled int       `json:"scheduled" doc:"Shifts assigned that count towards the requirements"`
	Gap       int       `json:"gap" doc:"Staff short (0 when covered)"`
}

// HRWorkforce is the coverage of the staffing requirements today.
type HRWorkforce struct {
	Required  int               `json:"required"`
	Scheduled int               `json:"scheduled"`
	Gap       int               `json:"gap"`
	Units     []HRWorkforceUnit `json:"units"`
}

// HRMovement counts the employment changes of the month.
type HRMovement struct {
	NewJoiners   int `json:"newJoiners"`
	Transfers    int `json:"transfers" doc:"Transfers and rotations"`
	Promotions   int `json:"promotions" doc:"Promotions and demotions"`
	Terminations int `json:"terminations" doc:"Resignations and terminations effective this month"`
}

// HRPayrollStatus is the latest regular payroll run.
type HRPayrollStatus struct {
	RunID      uuid.UUID `json:"runId"`
	Number     string    `json:"number"`
	PeriodCode string    `json:"periodCode"`
	Status     string    `json:"status" enum:"draft,calculated,submitted,approved,posted,paid"`
	Headcount  int       `json:"headcount"`
	Net        string    `json:"net"`
	Warnings   int       `json:"warnings" doc:"Employees with calculation warnings"`
	PaymentDay string    `json:"paymentDate"`
}

// HRDepartment is the headcount of an org unit (without sub-units).
type HRDepartment struct {
	OrgUnitID uuid.UUID `json:"orgUnitId"`
	Name      string    `json:"name"`
	Headcount int       `json:"headcount"`
}

// HRAttention is one queue of work waiting for HR.
type HRAttention struct {
	Key string `json:"key" enum:"attendance_review,attendance_missing,attendance_corrections,leave_requests,permission_requests,overtime_requests,overtime_unapproved,shift_swaps,profile_changes,contracts_expiring,documents_expiring,documents_expired,certifications_expired,payroll_warnings,payroll_posting_failed,loan_requests,loans_to_pay,employees_draft,employees_suspended"`
	// Count of open items.
	Count int `json:"count"`
}

// HRDashboard is the HR Dashboard of a property. Sections the user may not
// open are nil.
type HRDashboard struct {
	Date        string             `json:"date" doc:"Today at the property (YYYY-MM-DD)"`
	MonthStart  string             `json:"monthStart"`
	Headcount   HRHeadcount        `json:"headcount"`
	Attendance  *HRAttendanceToday `json:"attendance"`
	Workforce   *HRWorkforce       `json:"workforce"`
	Movement    HRMovement         `json:"movement"`
	Payroll     *HRPayrollStatus   `json:"payroll"`
	Departments []HRDepartment     `json:"departments"`
	Attention   []HRAttention      `json:"attention" doc:"Open queues with at least one item, most urgent first"`
}

func (m *Module) registerDashboard(reg *route.Registry) {
	add(reg, "HRIS Dashboard", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/dashboard",
		Summary:    "HR Dashboard: headcount, attendance today, workforce coverage, movement, payroll status and the HR queues",
		Permission: "hris.employee.view", Response: HRDashboard{},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, _ *http.Request) (HRDashboard, error) {
			p := handle.Property(ctx)
			return m.Dashboard(ctx, tx, p, today(ctx, tx, p), func(perm string) bool { return can(ctx, perm, p) })
		})})
}

// Dashboard builds the HR Dashboard of property on day; allowed tells
// whether the user may open a screen (sections and queues are skipped
// otherwise).
func (m *Module) Dashboard(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, allowed func(string) bool) (HRDashboard, error) {
	monthStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	d := HRDashboard{Date: ymd(day), MonthStart: ymd(monthStart), Departments: []HRDepartment{}, Attention: []HRAttention{}}

	if err := q.QueryRow(ctx, `SELECT count(*)::int,
		  count(*) FILTER (WHERE employment_status = 'permanent')::int,
		  count(*) FILTER (WHERE employment_status = 'contract')::int,
		  count(*) FILTER (WHERE employment_status = 'probation')::int,
		  count(*) FILTER (WHERE termination_status = 'scheduled')::int
		FROM hris.employees WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND (join_date IS NULL OR join_date <= $2::date)`,
		property, ymd(day)).Scan(&d.Headcount.Total, &d.Headcount.Permanent, &d.Headcount.Contract, &d.Headcount.Probation,
		&d.Headcount.Leaving); err != nil {
		return d, err
	}

	rows, err := q.Query(ctx, `SELECT u.id, u.name, count(e.id)::int FROM hris.org_units u
		JOIN hris.employees e ON e.org_unit_id = u.id AND e.status = 'active' AND e.archived_at IS NULL AND (e.join_date IS NULL OR e.join_date <= $2::date)
		WHERE u.property_id = $1 AND u.archived_at IS NULL GROUP BY u.id, u.name ORDER BY 3 DESC, u.name`, property, ymd(day))
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var x HRDepartment
		if err := rows.Scan(&x.OrgUnitID, &x.Name, &x.Headcount); err != nil {
			rows.Close()
			return d, err
		}
		d.Departments = append(d.Departments, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}

	if err := q.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM hris.employees WHERE property_id = $1 AND archived_at IS NULL AND join_date BETWEEN $2::date AND $3::date)::int,
		  count(*) FILTER (WHERE kind IN ('transfer', 'rotation'))::int,
		  count(*) FILTER (WHERE kind IN ('promotion', 'demotion'))::int,
		  count(*) FILTER (WHERE kind = 'termination')::int
		FROM hris.employment_history WHERE property_id = $1 AND status <> 'cancelled' AND effective_date BETWEEN $2::date AND $3::date`,
		property, ymd(monthStart), ymd(monthEnd(monthStart))).Scan(&d.Movement.NewJoiners, &d.Movement.Transfers, &d.Movement.Promotions,
		&d.Movement.Terminations); err != nil {
		return d, err
	}

	if allowed(permAttendanceView) {
		a := &HRAttendanceToday{}
		if err := q.QueryRow(ctx, `SELECT
			  count(*) FILTER (WHERE scheduled_start IS NOT NULL)::int,
			  count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::int,
			  count(*) FILTER (WHERE status = 'late')::int,
			  count(*) FILTER (WHERE status = 'absent')::int,
			  count(*) FILTER (WHERE status = 'on_leave')::int,
			  count(*) FILTER (WHERE flags && ARRAY[$3, $4]::text[])::int
			FROM hris.attendance_days WHERE property_id = $1 AND work_date = $2::date`,
			property, ymd(day), hris.FlagMissingIn, hris.FlagMissingOut).Scan(&a.Scheduled, &a.Present, &a.Late, &a.Absent, &a.OnLeave,
			&a.MissingClock); err != nil {
			return d, err
		}
		d.Attendance = a
	}

	if allowed(permScheduleView) {
		w, err := workforceToday(ctx, q, property, day)
		if err != nil {
			return d, err
		}
		d.Workforce = w
	}

	if allowed(permPayrollView) {
		var pr HRPayrollStatus
		var pay time.Time
		err := q.QueryRow(ctx, `SELECT id, number, period_code, status, headcount, net::text, warnings, payment_date FROM hris.payroll_runs
			WHERE property_id = $1 AND run_type = 'regular' AND status <> 'cancelled' ORDER BY period_code DESC, created_at DESC LIMIT 1`, property).
			Scan(&pr.RunID, &pr.Number, &pr.PeriodCode, &pr.Status, &pr.Headcount, &pr.Net, &pr.Warnings, &pay)
		switch {
		case err == pgx.ErrNoRows:
		case err != nil:
			return d, err
		default:
			pr.PaymentDay = ymd(pay)
			d.Payroll = &pr
		}
	}

	// $2 = today, $3 = start of the look-back, $4 = end of the look-ahead.
	from, until := ymd(day.AddDate(0, 0, -dashboardWindow)), ymd(day.AddDate(0, 0, dashboardWindow))
	queues := []struct {
		key, perm, sql string
	}{
		{"attendance_review", permAttendanceView, `SELECT count(*) FROM hris.attendance_events
			WHERE property_id = $1 AND review_status = 'pending' AND voided_at IS NULL AND work_date BETWEEN $3::date AND $2::date`},
		{"attendance_missing", permAttendanceView, `SELECT count(*) FROM hris.attendance_days
			WHERE property_id = $1 AND work_date BETWEEN $3::date AND $2::date AND NOT locked AND flags && ARRAY['missing_in', 'missing_out']::text[]`},
		{"attendance_corrections", permAttendanceView, `SELECT count(*) FROM hris.attendance_corrections WHERE property_id = $1 AND status = 'submitted'`},
		{"leave_requests", permLeaveView, `SELECT count(*) FROM hris.leave_requests WHERE property_id = $1 AND status = 'submitted'`},
		{"permission_requests", permPermissionView, `SELECT count(*) FROM hris.permission_requests WHERE property_id = $1 AND status = 'submitted'`},
		{"overtime_requests", permOvertimeView, `SELECT count(*) FROM hris.overtime_requests WHERE property_id = $1 AND status = 'submitted'`},
		{"overtime_unapproved", permOvertimeView, `SELECT count(*) FROM hris.attendance_days
			WHERE property_id = $1 AND work_date BETWEEN $3::date AND $2::date AND NOT locked AND 'unapproved_overtime' = ANY (flags)`},
		{"shift_swaps", permSwapView, `SELECT count(*) FROM hris.shift_swaps WHERE property_id = $1 AND status = 'submitted'`},
		{"profile_changes", permProfileView, `SELECT count(*) FROM hris.profile_change_requests WHERE property_id = $1 AND status = 'submitted'`},
		{"contracts_expiring", permContractView, `SELECT count(*) FROM hris.contracts
			WHERE property_id = $1 AND status IN ('active', 'expiring') AND end_date BETWEEN $2::date AND $4::date`},
		{"documents_expired", permDocumentView, `SELECT count(*) FROM hris.employee_documents d JOIN hris.employees e ON e.id = d.employee_id
			WHERE d.property_id = $1 AND d.archived_at IS NULL AND d.status = 'active' AND d.expires_on < $2::date AND e.status = 'active'`},
		{"documents_expiring", permDocumentView, `SELECT count(*) FROM hris.employee_documents d JOIN hris.employees e ON e.id = d.employee_id
			WHERE d.property_id = $1 AND d.archived_at IS NULL AND d.status = 'active' AND d.expires_on BETWEEN $2::date AND $4::date AND e.status = 'active'`},
		{"certifications_expired", permCertView, `SELECT count(*) FROM hris.certifications c JOIN hris.employees e ON e.id = c.employee_id
			WHERE c.property_id = $1 AND c.status = 'expired' AND e.status = 'active'
			  AND NOT EXISTS (SELECT 1 FROM hris.certifications n WHERE n.employee_id = c.employee_id
			    AND n.certification_type_id = c.certification_type_id AND n.status = 'active')`},
		// HRIS phase B: payroll posting, loans and cash advances, lifecycle statuses
		{"payroll_posting_failed", "hris.payroll_run.view", `SELECT count(*) FROM hris.payroll_runs WHERE property_id = $1 AND finance_status = 'failed'`},
		{"loan_requests", "hris.employee_loan.view", `SELECT count(*) FROM hris.employee_loans WHERE property_id = $1 AND status = 'submitted'
			AND archived_at IS NULL`},
		{"loans_to_pay", "hris.employee_loan.view", `SELECT count(*) FROM hris.employee_loans WHERE property_id = $1 AND status = 'approved'
			AND archived_at IS NULL`},
		{"employees_draft", "hris.employee.view", `SELECT count(*) FROM hris.employees WHERE property_id = $1 AND status = 'draft' AND archived_at IS NULL`},
		{"employees_suspended", "hris.employee.view", `SELECT count(*) FROM hris.employees WHERE property_id = $1 AND status = 'active'
			AND archived_at IS NULL AND suspended_from <= $2::date AND (suspended_until IS NULL OR suspended_until >= $2::date)`},
	}
	for _, x := range queues {
		if !allowed(x.perm) {
			continue
		}
		// Every query gets the same four parameters; unused ones are typed
		// by a no-op reference so the statement accepts them.
		var n int
		if err := q.QueryRow(ctx, `WITH p AS (SELECT $2::date, $3::date, $4::date) `+x.sql, property, ymd(day), from, until).Scan(&n); err != nil {
			return d, err
		}
		if n > 0 {
			d.Attention = append(d.Attention, HRAttention{Key: x.key, Count: n})
		}
	}
	if d.Payroll != nil && d.Payroll.Warnings > 0 && d.Payroll.Status != "posted" && d.Payroll.Status != "paid" {
		d.Attention = append(d.Attention, HRAttention{Key: "payroll_warnings", Count: d.Payroll.Warnings})
	}
	return d, nil
}

// workforceToday compares the shifts of the day with the staffing
// requirements per org unit (the coverage rule of the schedules).
func workforceToday(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (*HRWorkforce, error) {
	rows, err := q.Query(ctx, `SELECT u.id, u.name, r.min_staff,
		(SELECT count(*) FROM hris.shift_assignments a JOIN hris.schedules sc ON sc.id = a.schedule_id AND sc.status <> 'cancelled'
		   JOIN hris.employees e ON e.id = a.employee_id
		 WHERE a.property_id = $1 AND a.work_date = $2::date AND a.kind = 'shift' AND a.status = 'scheduled' AND sc.org_unit_id = r.org_unit_id
		   AND (r.shift_template_id IS NULL OR a.shift_template_id = r.shift_template_id) AND (r.position_id IS NULL OR e.position_id = r.position_id))::int
		FROM hris.staffing_requirements r JOIN hris.org_units u ON u.id = r.org_unit_id
		WHERE r.property_id = $1 AND r.status = 'active' AND r.archived_at IS NULL AND extract(isodow FROM $2::date)::int = ANY (r.weekdays)
		ORDER BY u.name, u.id`, property, ymd(day))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	w := &HRWorkforce{Units: []HRWorkforceUnit{}}
	idx := map[uuid.UUID]int{}
	for rows.Next() {
		var unit uuid.UUID
		var name string
		var required, assigned int
		if err := rows.Scan(&unit, &name, &required, &assigned); err != nil {
			return nil, err
		}
		i, ok := idx[unit]
		if !ok {
			i = len(w.Units)
			idx[unit] = i
			w.Units = append(w.Units, HRWorkforceUnit{OrgUnitID: unit, Name: name})
		}
		u := &w.Units[i]
		u.Required += required
		u.Scheduled += min(assigned, required)
		u.Gap += max(required-assigned, 0)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, u := range w.Units {
		w.Required += u.Required
		w.Scheduled += u.Scheduled
		w.Gap += u.Gap
	}
	return w, nil
}

func monthEnd(t time.Time) time.Time { return t.AddDate(0, 1, -1) }
