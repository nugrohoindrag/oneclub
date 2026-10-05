package hrtime

// Attendance days (FR-ATT-03): every clock event, approved leave /
// permission / correction, schedule change and the daily job re-evaluates
// the day of an employee with hris.EvaluateDay against the published shift,
// the holiday calendar, approved leave and permission and the Attendance
// Policy, then updates the payable hours of the approved overtime of that
// day (FR-OVT-02) and flags unapproved overtime (FR-OVT-04). Days in a
// locked payroll period are never changed.

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
)

// assignmentRow is the published assignment of an employee on a date.
type assignmentRow struct {
	ID           uuid.UUID
	Kind         string
	TemplateID   *uuid.UUID
	StartsAt     *time.Time
	EndsAt       *time.Time
	BreakMinutes int
	WorkMinutes  int
	Role         *string
	Status       string
}

func loadAssignment(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) (*assignmentRow, error) {
	var a assignmentRow
	err := q.QueryRow(ctx, `SELECT a.id, a.kind, a.shift_template_id, a.starts_at, a.ends_at, a.break_minutes, a.work_minutes, a.workforce_role, a.status
		FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id AND s.status = 'published'
		WHERE a.employee_id = $1 AND a.work_date = $2::date AND a.status <> 'cancelled'`, employeeID, ymd(day)).
		Scan(&a.ID, &a.Kind, &a.TemplateID, &a.StartsAt, &a.EndsAt, &a.BreakMinutes, &a.WorkMinutes, &a.Role, &a.Status)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// holidayOn returns the holiday of a date ("" = none) and whether it is a
// collective leave day deducting annual leave.
func holidayOn(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (string, bool, error) {
	var name string
	var deducts bool
	err := q.QueryRow(ctx, `SELECT name, deducts FROM (
		  SELECT name, deducts_annual_leave AS deducts, 0 AS o FROM hris.holidays WHERE property_id = $1 AND holiday_date = $2::date
		    AND status = 'active' AND archived_at IS NULL
		  UNION ALL SELECT name, false, 1 FROM platform.calendar_days WHERE day = $2::date AND kind = 'public_holiday' AND status = 'active'
		    AND (property_id IS NULL OR property_id = $1)) h ORDER BY o LIMIT 1`, property, ymd(day)).Scan(&name, &deducts)
	if dbtx.IsNoRows(err) {
		return "", false, nil
	}
	return name, deducts, err
}

// workWeekOf is the work week of an employee on a date: the contract in
// force, else the Attendance Configuration.
func workWeekOf(ctx context.Context, q dbtx.Querier, emp hris.Employee, day time.Time) (int, error) {
	c, err := hris.ContractAt(ctx, q, emp.ID, day)
	if err != nil {
		return 5, err
	}
	if c != nil && (c.WorkWeekDays == 5 || c.WorkWeekDays == 6) {
		return c.WorkWeekDays, nil
	}
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, q, emp.PropertyID, hris.PolicyTime(day))
	if err != nil {
		return 5, err
	}
	if cfg.WorkWeekDays == 6 {
		return 6, nil
	}
	return 5, nil
}

// workingDay reports whether a date is a working day of the employee: a
// shift on the published schedule, else a working weekday that is no
// holiday (no assignment).
func workingDay(a *assignmentRow, holiday string, weekday time.Weekday, workWeek int) bool {
	if a != nil {
		return a.Kind == "shift"
	}
	return holiday == "" && hris.IsWorkingWeekday(weekday, workWeek)
}

// dayLeave returns the approved leave of a date.
func dayLeave(ctx context.Context, q dbtx.Querier, lp hris.LeavePolicy, employeeID uuid.UUID, day time.Time, working bool) (*hris.DayLeave, *uuid.UUID, error) {
	var id uuid.UUID
	var lt string
	var paid bool
	var half *string
	err := q.QueryRow(ctx, `SELECT id, leave_type, paid, half_day FROM hris.leave_requests WHERE employee_id = $1 AND status = 'approved'
		AND $2::date BETWEEN start_date AND end_date ORDER BY start_date LIMIT 1`, employeeID, ymd(day)).Scan(&id, &lt, &paid, &half)
	if dbtx.IsNoRows(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	t, ok := lp.LeaveType(lt)
	if (!ok || t.CountsWorkingDays) && !working {
		return nil, nil, nil
	}
	l := &hris.DayLeave{Type: lt, Paid: paid, Fraction: decimal.NewFromInt(1)}
	if half != nil {
		l.HalfDay = *half
		l.Fraction = decimal.RequireFromString("0.5")
	}
	return l, &id, nil
}

func dayPermissions(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) ([]hris.PermissionWindow, error) {
	rows, err := q.Query(ctx, `SELECT permission_type, starts_at, ends_at, paid FROM hris.permission_requests WHERE employee_id = $1 AND work_date = $2::date
		AND status = 'approved' ORDER BY starts_at`, employeeID, ymd(day))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hris.PermissionWindow
	for rows.Next() {
		var p hris.PermissionWindow
		if err := rows.Scan(&p.Type, &p.Start, &p.End, &p.Paid); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// dayEvents returns the counted events of a day (rejected and voided ones
// excluded) and whether some wait for review or come from a correction.
func dayEvents(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) ([]hris.ClockEvent, bool, bool, error) {
	rows, err := q.Query(ctx, `SELECT direction, occurred_at, review_status, source FROM hris.attendance_events WHERE employee_id = $1 AND work_date = $2::date
		AND voided_at IS NULL AND review_status <> 'rejected' ORDER BY occurred_at`, employeeID, ymd(day))
	if err != nil {
		return nil, false, false, err
	}
	defer rows.Close()
	var out []hris.ClockEvent
	var pending, corrected bool
	for rows.Next() {
		var e hris.ClockEvent
		var review, source string
		if err := rows.Scan(&e.Direction, &e.At, &review, &source); err != nil {
			return nil, false, false, err
		}
		pending = pending || review == "pending"
		corrected = corrected || source == "correction"
		out = append(out, e)
	}
	return out, pending, corrected, rows.Err()
}

// policies caches the policies of one evaluation run.
type policies struct {
	att  map[string]hris.AttendancePolicy
	lv   map[string]hris.LeavePolicy
	ot   map[string]hris.OvertimePolicy
	otV  map[string]int
	prop uuid.UUID
}

func newPolicies(property uuid.UUID) *policies {
	return &policies{att: map[string]hris.AttendancePolicy{}, lv: map[string]hris.LeavePolicy{}, ot: map[string]hris.OvertimePolicy{},
		otV: map[string]int{}, prop: property}
}

func (p *policies) attendance(ctx context.Context, q dbtx.Querier, day time.Time) (hris.AttendancePolicy, error) {
	k := ymd(day)
	if v, ok := p.att[k]; ok {
		return v, nil
	}
	v, _, err := hris.LoadAttendancePolicy(ctx, q, p.prop, hris.PolicyTime(day))
	p.att[k] = v
	return v, err
}

func (p *policies) leave(ctx context.Context, q dbtx.Querier, day time.Time) (hris.LeavePolicy, error) {
	k := ymd(day)
	if v, ok := p.lv[k]; ok {
		return v, nil
	}
	v, _, err := hris.LoadLeavePolicy(ctx, q, p.prop, hris.PolicyTime(day))
	p.lv[k] = v
	return v, err
}

func (p *policies) overtime(ctx context.Context, q dbtx.Querier, day time.Time) (hris.OvertimePolicy, int, error) {
	k := ymd(day)
	if v, ok := p.ot[k]; ok {
		return v, p.otV[k], nil
	}
	v, ref, err := hris.LoadOvertimePolicy(ctx, q, p.prop, hris.PolicyTime(day))
	p.ot[k], p.otV[k] = v, ref.Version
	return v, ref.Version, err
}

// recomputeDay evaluates and stores the attendance day of an employee.
// finalize closes the day (the date is over). Locked days are returned as
// stored.
func (m *Module) recomputeDay(ctx context.Context, tx pgx.Tx, emp hris.Employee, day time.Time, finalize bool, pol *policies) (hris.AttendanceDaySnapshot, error) {
	if pol == nil {
		pol = newPolicies(emp.PropertyID)
	}
	var locked bool
	err := tx.QueryRow(ctx, `SELECT locked FROM hris.attendance_days WHERE employee_id = $1 AND work_date = $2::date`, emp.ID, ymd(day)).Scan(&locked)
	if err != nil && !dbtx.IsNoRows(err) {
		return hris.AttendanceDaySnapshot{}, err
	}
	if locked {
		return snapshotOf(ctx, tx, emp.ID, day)
	}
	if lk, _, err := hris.TimeLocked(ctx, tx, emp.PropertyID, day); err != nil || lk {
		if err != nil {
			return hris.AttendanceDaySnapshot{}, err
		}
		return snapshotOf(ctx, tx, emp.ID, day)
	}
	a, err := loadAssignment(ctx, tx, emp.ID, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	holiday, _, err := holidayOn(ctx, tx, emp.PropertyID, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	ww, err := workWeekOf(ctx, tx, emp, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	ap, err := pol.attendance(ctx, tx, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	lp, err := pol.leave(ctx, tx, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	working := workingDay(a, holiday, day.Weekday(), ww)
	leave, leaveID, err := dayLeave(ctx, tx, lp, emp.ID, day, working)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	perms, err := dayPermissions(ctx, tx, emp.ID, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	events, pending, corrected, err := dayEvents(ctx, tx, emp.ID, day)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	in := hris.DayInput{Holiday: holiday, WorkingWeekday: hris.IsWorkingWeekday(day.Weekday(), ww), Leave: leave, Permissions: perms, Events: events,
		Now: clock.Now(), Finalize: finalize, Policy: ap}
	var assignmentID, templateID *uuid.UUID
	var schedStart, schedEnd *time.Time
	schedMinutes := 0
	if a != nil {
		assignmentID = &a.ID
		if a.Kind == "shift" && a.StartsAt != nil && a.EndsAt != nil {
			in.Shift = &hris.ShiftWindow{Start: *a.StartsAt, End: *a.EndsAt, BreakMinutes: a.BreakMinutes}
			templateID, schedStart, schedEnd, schedMinutes = a.TemplateID, a.StartsAt, a.EndsAt, a.WorkMinutes
		} else {
			in.Off = true
		}
	}
	r := hris.EvaluateDay(in)
	flags := r.Flags
	if pending {
		flags = append(flags, hris.FlagPendingReview)
	}
	if corrected {
		flags = append(flags, hris.FlagCorrected)
	}
	restDay := (a != nil && a.Kind == "off") || (a == nil && !hris.IsWorkingWeekday(day.Weekday(), ww))
	kind := hris.DayKindOf(holiday != "", restDay, ww, day.Weekday())
	var leaveType *string
	var leavePaid *bool
	if leave != nil {
		leaveType, leavePaid = &leave.Type, &leave.Paid
	}
	// approved overtime of the day follows the actual overtime (FR-OVT-02)
	unapproved, err := m.updateDayOvertime(ctx, tx, emp, day, r.OvertimeMinutes, kind, ww, pol)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	if unapproved {
		flags = append(flags, hris.FlagUnapprovedOvertime)
	}
	slices.Sort(flags)
	flags = slices.Compact(flags)
	var holidayName *string
	if holiday != "" {
		holidayName = &holiday
	}
	_, err = tx.Exec(ctx, `INSERT INTO hris.attendance_days (id, property_id, employee_id, work_date, assignment_id, shift_template_id, scheduled_start,
		  scheduled_end, scheduled_minutes, first_in, last_out, worked_minutes, late_minutes, early_leave_minutes, overtime_minutes, status, day_kind,
		  holiday_name, leave_request_id, leave_type, leave_paid, leave_fraction, permission_minutes, unpaid_permission_minutes, flags, finalized, computed_at)
		VALUES (gen_random_uuid(), $1, $2, $3::date, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, now())
		ON CONFLICT (employee_id, work_date) DO UPDATE SET assignment_id = EXCLUDED.assignment_id, shift_template_id = EXCLUDED.shift_template_id,
		  scheduled_start = EXCLUDED.scheduled_start, scheduled_end = EXCLUDED.scheduled_end, scheduled_minutes = EXCLUDED.scheduled_minutes,
		  first_in = EXCLUDED.first_in, last_out = EXCLUDED.last_out, worked_minutes = EXCLUDED.worked_minutes, late_minutes = EXCLUDED.late_minutes,
		  early_leave_minutes = EXCLUDED.early_leave_minutes, overtime_minutes = EXCLUDED.overtime_minutes, status = EXCLUDED.status,
		  day_kind = EXCLUDED.day_kind, holiday_name = EXCLUDED.holiday_name, leave_request_id = EXCLUDED.leave_request_id, leave_type = EXCLUDED.leave_type,
		  leave_paid = EXCLUDED.leave_paid, leave_fraction = EXCLUDED.leave_fraction, permission_minutes = EXCLUDED.permission_minutes,
		  unpaid_permission_minutes = EXCLUDED.unpaid_permission_minutes, flags = EXCLUDED.flags,
		  finalized = EXCLUDED.finalized OR hris.attendance_days.finalized AND EXCLUDED.status <> 'scheduled', computed_at = now()`,
		emp.PropertyID, emp.ID, ymd(day), assignmentID, templateID, schedStart, schedEnd, schedMinutes, r.FirstIn, r.LastOut, r.WorkedMinutes,
		r.LateMinutes, r.EarlyLeaveMinutes, r.OvertimeMinutes, r.Status, kind, holidayName, leaveID, leaveType, leavePaid, r.LeaveFraction,
		r.PermissionMinutes, r.UnpaidPermissionMinutes, flags, r.Final)
	if err != nil {
		return hris.AttendanceDaySnapshot{}, err
	}
	return snapshotFrom(r), nil
}

func fmtTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func snapshotFrom(r hris.DayResult) hris.AttendanceDaySnapshot {
	return hris.AttendanceDaySnapshot{Status: r.Status, FirstIn: fmtTime(r.FirstIn), LastOut: fmtTime(r.LastOut), WorkedMinutes: r.WorkedMinutes,
		LateMinutes: r.LateMinutes, EarlyLeaveMinutes: r.EarlyLeaveMinutes, OvertimeMinutes: r.OvertimeMinutes}
}

func snapshotOf(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) (hris.AttendanceDaySnapshot, error) {
	var s hris.AttendanceDaySnapshot
	var in, out *time.Time
	err := q.QueryRow(ctx, `SELECT status, first_in, last_out, worked_minutes, late_minutes, early_leave_minutes, overtime_minutes FROM hris.attendance_days
		WHERE employee_id = $1 AND work_date = $2::date`, employeeID, ymd(day)).Scan(&s.Status, &in, &out, &s.WorkedMinutes, &s.LateMinutes,
		&s.EarlyLeaveMinutes, &s.OvertimeMinutes)
	if dbtx.IsNoRows(err) {
		return hris.AttendanceDaySnapshot{Status: hris.DayScheduled}, nil
	}
	s.FirstIn, s.LastOut = fmtTime(in), fmtTime(out)
	return s, err
}

// updateDayOvertime sets the payable hours of the approved overtime of a
// day from its actual overtime minutes (requests in order), and reports
// whether countable overtime exceeds the approved hours (FR-OVT-04).
func (m *Module) updateDayOvertime(ctx context.Context, tx pgx.Tx, emp hris.Employee, day time.Time, actualMinutes int, dayKind string, ww int,
	pol *policies) (bool, error) {
	p, _, err := pol.overtime(ctx, tx, day)
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT id, hours FROM hris.overtime_requests WHERE employee_id = $1 AND work_date = $2::date AND status = 'approved'
		ORDER BY starts_at, number`, emp.ID, ymd(day))
	if err != nil {
		return false, err
	}
	type req struct {
		id    uuid.UUID
		hours decimal.Decimal
	}
	var reqs []req
	for rows.Next() {
		var r req
		if err := rows.Scan(&r.id, &r.hours); err != nil {
			rows.Close()
			return false, err
		}
		reqs = append(reqs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	countable := p.CountableOvertime(actualMinutes)
	remaining := countable
	approved := decimal.Zero
	for _, r := range reqs {
		approved = approved.Add(r.hours)
		payable := decimal.Max(decimal.Min(r.hours, remaining), decimal.Zero)
		if lim := hris.Dec(p.MaxHoursPerDay); lim.IsPositive() && payable.GreaterThan(lim) {
			payable = lim
		}
		remaining = remaining.Sub(payable)
		tiers := p.Tiers(payable, dayKind, ww)
		raw, _ := json.Marshal(tiers)
		actual := decimal.NewFromInt(int64(actualMinutes)).Div(decimal.NewFromInt(60)).Round(2)
		if _, err := tx.Exec(ctx, `UPDATE hris.overtime_requests SET actual_hours = $2, payable_hours = $3, multiplied_hours = $4, tiers = $5, day_kind = $6,
			work_week_days = $7 WHERE id = $1`, r.id, actual, payable, hris.MultipliedOf(tiers), raw, dayKind, ww); err != nil {
			return false, err
		}
	}
	return countable.GreaterThan(approved), nil
}

// FinalizePeriod closes the attendance days of a period up to yesterday:
// every employee with a published assignment, a clock event or approved
// leave on a day gets the day evaluated as final (hris.FinalizeAttendance).
func (m *Module) FinalizePeriod(ctx context.Context, tx pgx.Tx, property uuid.UUID, from, to time.Time) error {
	yesterday := today(ctx, tx, property).AddDate(0, 0, -1)
	if to.After(yesterday) {
		to = yesterday
	}
	if to.Before(from) {
		return nil
	}
	return m.evaluateRange(ctx, tx, property, from, to, nil, true)
}

// evaluateRange (re)evaluates the days of a period for the employees with
// an assignment, an event, leave or an existing day (filtered by ids).
func (m *Module) evaluateRange(ctx context.Context, tx pgx.Tx, property uuid.UUID, from, to time.Time, ids []uuid.UUID, finalize bool) error {
	if ids == nil {
		ids = []uuid.UUID{}
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT employee_id, d FROM (
		  SELECT a.employee_id, a.work_date AS d FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id AND s.status = 'published'
		    WHERE a.property_id = $1 AND a.work_date BETWEEN $2::date AND $3::date AND a.status <> 'cancelled'
		  UNION ALL SELECT employee_id, work_date FROM hris.attendance_events WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date
		  UNION ALL SELECT employee_id, work_date FROM hris.attendance_days WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date AND NOT locked
		  UNION ALL SELECT r.employee_id, g::date FROM hris.leave_requests r, generate_series(greatest(r.start_date, $2::date), least(r.end_date, $3::date),
		    interval '1 day') g WHERE r.property_id = $1 AND r.status = 'approved' AND r.start_date <= $3::date AND r.end_date >= $2::date) x
		WHERE cardinality($4::uuid[]) = 0 OR employee_id = ANY ($4) ORDER BY 2, 1`, property, ymd(from), ymd(to), ids)
	if err != nil {
		return err
	}
	type pair struct {
		emp uuid.UUID
		day time.Time
	}
	var todo []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.emp, &p.day); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	pol := newPolicies(property)
	emps := map[uuid.UUID]hris.Employee{}
	tday := today(ctx, tx, property)
	for _, p := range todo {
		e, ok := emps[p.emp]
		if !ok {
			var err error
			if e, err = hris.EmployeeByID(ctx, tx, p.emp); err != nil {
				return err
			}
			emps[p.emp] = e
		}
		day := time.Date(p.day.Year(), p.day.Month(), p.day.Day(), 0, 0, 0, 0, time.UTC)
		if _, err := m.recomputeDay(ctx, tx, e, day, finalize && day.Before(tday), pol); err != nil {
			return err
		}
	}
	return nil
}
