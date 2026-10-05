package hris

// Payroll contract of time & attendance (EP-09 reads EP-07/08): per
// employee and payroll period the days present / late / absent, paid and
// unpaid leave, unpaid permission and the approved overtime hours by
// multiplier tier, plus period locks so a calculated payroll period no
// longer changes. All functions take the caller's querier (RLS applies);
// documented in docs/p5-contracts.md.

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// OvertimeLine is one approved overtime request of the period.
type OvertimeLine struct {
	RequestID       uuid.UUID      `json:"requestId"`
	Number          string         `json:"number"`
	WorkDate        string         `json:"workDate"`
	DayKind         string         `json:"dayKind" enum:"workday,rest_day,shortest_day"`
	ApprovedHours   string         `json:"approvedHours"`
	PayableHours    string         `json:"payableHours"`
	MultipliedHours string         `json:"multipliedHours"`
	Tiers           []OvertimeTier `json:"tiers"`
}

// OvertimeSummary is the approved overtime of an employee in a period.
type OvertimeSummary struct {
	Requests        int             `json:"requests"`
	ApprovedHours   decimal.Decimal `json:"approvedHours" doc:"Hours approved on the requests"`
	PayableHours    decimal.Decimal `json:"payableHours" doc:"Approved hours capped by the attendance of each day (paid basis)"`
	MultipliedHours decimal.Decimal `json:"multipliedHours" doc:"Σ payable hours × multiplier; pay = hourly wage (1/173) × multiplied hours"`
	Tiers           []OvertimeTier  `json:"tiers" doc:"Payable hours per multiplier, e.g. 1.5 → 2 h, 2 → 3 h"`
	Lines           []OvertimeLine  `json:"lines"`
}

// TimeSummary is the time & attendance of one employee in a payroll period
// (local dates From–To inclusive).
type TimeSummary struct {
	EmployeeID            uuid.UUID       `json:"employeeId"`
	EmployeeNo            string          `json:"employeeNo"`
	FullName              string          `json:"fullName"`
	ScheduledDays         int             `json:"scheduledDays" doc:"Days with a shift on a published schedule"`
	PresentDays           int             `json:"presentDays" doc:"Days clocked in (Present, Late, Early Leave)"`
	LateDays              int             `json:"lateDays"`
	LateMinutes           int             `json:"lateMinutes"`
	EarlyLeaveDays        int             `json:"earlyLeaveDays"`
	EarlyLeaveMinutes     int             `json:"earlyLeaveMinutes"`
	AbsentDays            int             `json:"absentDays"`
	PaidLeaveDays         decimal.Decimal `json:"paidLeaveDays"`
	UnpaidLeaveDays       decimal.Decimal `json:"unpaidLeaveDays" doc:"Days of approved unpaid leave (payroll deduction basis)"`
	OffDays               int             `json:"offDays"`
	HolidayDays           int             `json:"holidayDays"`
	WorkedHours           decimal.Decimal `json:"workedHours"`
	UnpaidPermissionHours decimal.Decimal `json:"unpaidPermissionHours" doc:"Approved unpaid permission (izin) hours"`
	OpenDays              int             `json:"openDays" doc:"Days of the period not closed yet (future or still running)"`
	Exceptions            int             `json:"exceptions" doc:"Missing clock-in / out, clock-ins waiting for review and corrections waiting for approval"`
	Overtime              OvertimeSummary `json:"overtime"`
}

// AttendanceFactor is days present ÷ scheduled days (service charge
// attendance factor, §16 #5); paid leave days count as present when
// paidLeaveCounts. 1 when nothing was scheduled.
func (s TimeSummary) AttendanceFactor(paidLeaveCounts bool) decimal.Decimal {
	if s.ScheduledDays <= 0 {
		return decimal.NewFromInt(1)
	}
	present := decimal.NewFromInt(int64(s.PresentDays))
	if paidLeaveCounts {
		present = present.Add(s.PaidLeaveDays)
	}
	f := present.Div(decimal.NewFromInt(int64(s.ScheduledDays)))
	if f.GreaterThan(decimal.NewFromInt(1)) {
		return decimal.NewFromInt(1)
	}
	return f.Round(4)
}

func dateArg(t time.Time) string { return t.Format("2006-01-02") }

// TimeSummaries returns the time & attendance of a payroll period for the
// given employees (empty = every employee employed during the period),
// ordered by name. Call FinalizeAttendance first so every past day is
// closed (Absent for missing clock-ins).
func TimeSummaries(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employeeIDs []uuid.UUID) ([]TimeSummary, error) {
	if to.Before(from) {
		return nil, errs.Validation("invalid_period", "the period ends before it starts")
	}
	rows, err := q.Query(ctx, `SELECT e.id, e.employee_no, e.full_name FROM hris.employees e
		WHERE e.property_id = $1 AND e.archived_at IS NULL AND (e.join_date IS NULL OR e.join_date <= $3::date)
		  AND (e.termination_date IS NULL OR e.termination_date > $2::date)
		  AND (cardinality($4::uuid[]) = 0 OR e.id = ANY ($4))
		ORDER BY e.full_name, e.id`, property, dateArg(from), dateArg(to), nonNilIDs(employeeIDs))
	if err != nil {
		return nil, err
	}
	var out []TimeSummary
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var s TimeSummary
		if err := rows.Scan(&s.EmployeeID, &s.EmployeeNo, &s.FullName); err != nil {
			rows.Close()
			return nil, err
		}
		s.PaidLeaveDays, s.UnpaidLeaveDays, s.WorkedHours, s.UnpaidPermissionHours = decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
		s.Overtime = OvertimeSummary{ApprovedHours: decimal.Zero, PayableHours: decimal.Zero, MultipliedHours: decimal.Zero, Tiers: []OvertimeTier{},
			Lines: []OvertimeLine{}}
		index[s.EmployeeID] = len(out)
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []TimeSummary{}, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, s := range out {
		ids[i] = s.EmployeeID
	}
	days, err := q.Query(ctx, `SELECT employee_id,
		count(*) FILTER (WHERE scheduled_start IS NOT NULL)::int,
		count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::int,
		count(*) FILTER (WHERE status = 'late')::int, coalesce(sum(late_minutes), 0)::int,
		count(*) FILTER (WHERE early_leave_minutes > 0)::int, coalesce(sum(early_leave_minutes), 0)::int,
		count(*) FILTER (WHERE status = 'absent')::int,
		coalesce(sum(leave_fraction) FILTER (WHERE leave_paid), 0), coalesce(sum(leave_fraction) FILTER (WHERE leave_paid = false), 0),
		count(*) FILTER (WHERE status = 'off')::int, count(*) FILTER (WHERE status = 'holiday')::int,
		round(coalesce(sum(worked_minutes), 0) / 60.0, 2), round(coalesce(sum(unpaid_permission_minutes), 0) / 60.0, 2),
		count(*) FILTER (WHERE status = 'scheduled' OR NOT finalized)::int,
		count(*) FILTER (WHERE flags && '{missing_out,missing_in,pending_review}'::text[])::int
		FROM hris.attendance_days WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date AND employee_id = ANY ($4)
		GROUP BY employee_id`, property, dateArg(from), dateArg(to), ids)
	if err != nil {
		return nil, err
	}
	for days.Next() {
		var eid uuid.UUID
		var s TimeSummary
		if err := days.Scan(&eid, &s.ScheduledDays, &s.PresentDays, &s.LateDays, &s.LateMinutes, &s.EarlyLeaveDays, &s.EarlyLeaveMinutes, &s.AbsentDays,
			&s.PaidLeaveDays, &s.UnpaidLeaveDays, &s.OffDays, &s.HolidayDays, &s.WorkedHours, &s.UnpaidPermissionHours, &s.OpenDays, &s.Exceptions); err != nil {
			days.Close()
			return nil, err
		}
		t := &out[index[eid]]
		s.EmployeeID, s.EmployeeNo, s.FullName, s.Overtime = t.EmployeeID, t.EmployeeNo, t.FullName, t.Overtime
		*t = s
	}
	days.Close()
	if err := days.Err(); err != nil {
		return nil, err
	}
	pending, err := q.Query(ctx, `SELECT employee_id, count(*)::int FROM hris.attendance_corrections WHERE property_id = $1 AND status = 'submitted'
		AND work_date BETWEEN $2::date AND $3::date AND employee_id = ANY ($4) GROUP BY employee_id`, property, dateArg(from), dateArg(to), ids)
	if err != nil {
		return nil, err
	}
	for pending.Next() {
		var eid uuid.UUID
		var n int
		if err := pending.Scan(&eid, &n); err != nil {
			pending.Close()
			return nil, err
		}
		out[index[eid]].Exceptions += n
	}
	pending.Close()
	if err := pending.Err(); err != nil {
		return nil, err
	}
	ot, err := ApprovedOvertime(ctx, q, property, from, to, ids)
	if err != nil {
		return nil, err
	}
	for eid, o := range ot {
		if i, ok := index[eid]; ok {
			out[i].Overtime = o
		}
	}
	return out, nil
}

// ApprovedOvertime returns the approved overtime of a period per employee
// (empty ids = all), with payable hours by multiplier tier (FR-OVT-02/03).
func ApprovedOvertime(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employeeIDs []uuid.UUID) (map[uuid.UUID]OvertimeSummary, error) {
	rows, err := q.Query(ctx, `SELECT employee_id, id, number, to_char(work_date, 'YYYY-MM-DD'), day_kind, hours, payable_hours, multiplied_hours, tiers
		FROM hris.overtime_requests WHERE property_id = $1 AND status = 'approved' AND work_date BETWEEN $2::date AND $3::date
		  AND (cardinality($4::uuid[]) = 0 OR employee_id = ANY ($4)) ORDER BY work_date, number`, property, dateArg(from), dateArg(to), nonNilIDs(employeeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]OvertimeSummary{}
	tierSums := map[uuid.UUID]map[string]decimal.Decimal{}
	for rows.Next() {
		var eid uuid.UUID
		var l OvertimeLine
		var approved, payable, multiplied decimal.Decimal
		if err := rows.Scan(&eid, &l.RequestID, &l.Number, &l.WorkDate, &l.DayKind, &approved, &payable, &multiplied, &l.Tiers); err != nil {
			return nil, err
		}
		if l.Tiers == nil {
			l.Tiers = []OvertimeTier{}
		}
		l.ApprovedHours, l.PayableHours, l.MultipliedHours = approved.String(), payable.String(), multiplied.String()
		s, ok := out[eid]
		if !ok {
			s = OvertimeSummary{ApprovedHours: decimal.Zero, PayableHours: decimal.Zero, MultipliedHours: decimal.Zero, Tiers: []OvertimeTier{}, Lines: []OvertimeLine{}}
			tierSums[eid] = map[string]decimal.Decimal{}
		}
		s.Requests++
		s.ApprovedHours = s.ApprovedHours.Add(approved)
		s.PayableHours = s.PayableHours.Add(payable)
		s.MultipliedHours = s.MultipliedHours.Add(multiplied)
		s.Lines = append(s.Lines, l)
		for _, t := range l.Tiers {
			tierSums[eid][t.Factor] = tierSums[eid][t.Factor].Add(t.Hours)
		}
		out[eid] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for eid, sums := range tierSums {
		s := out[eid]
		for f, h := range sums {
			if h.IsPositive() {
				s.Tiers = append(s.Tiers, OvertimeTier{Factor: f, Hours: h})
			}
		}
		sort.Slice(s.Tiers, func(i, j int) bool { return Dec(s.Tiers[i].Factor).LessThan(Dec(s.Tiers[j].Factor)) })
		out[eid] = s
	}
	return out, nil
}

// UnpaidLeaveDays returns the approved unpaid leave days of a period per
// employee (empty ids = all).
func UnpaidLeaveDays(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, employeeIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	rows, err := q.Query(ctx, `SELECT employee_id, sum(leave_fraction) FROM hris.attendance_days WHERE property_id = $1 AND leave_paid = false
		AND work_date BETWEEN $2::date AND $3::date AND (cardinality($4::uuid[]) = 0 OR employee_id = ANY ($4)) GROUP BY employee_id`,
		property, dateArg(from), dateArg(to), nonNilIDs(employeeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]decimal.Decimal{}
	for rows.Next() {
		var eid uuid.UUID
		var d decimal.Decimal
		if err := rows.Scan(&eid, &d); err != nil {
			return nil, err
		}
		out[eid] = d
	}
	return out, rows.Err()
}

// ── payroll period locks ─────────────────────────────────────────────────

// TimeLock is a locked payroll period.
type TimeLock struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	PropertyID  uuid.UUID  `json:"propertyId" db:"property_id"`
	PeriodStart time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd   time.Time  `json:"periodEnd" db:"period_end"`
	Reference   string     `json:"reference" db:"reference"`
	Status      string     `json:"status" db:"status" enum:"locked,released"`
	LockedAt    time.Time  `json:"lockedAt" db:"locked_at"`
	ReleasedAt  *time.Time `json:"releasedAt" db:"released_at"`
	ReleaseNote *string    `json:"releaseNote" db:"release_note"`
}

// LockTimePeriod locks the attendance, leave, permission and overtime of a
// period (e.g. when payroll is calculated): corrections, approvals and
// recalculation of those days are refused until the lock is released. The
// caller records the audit entry.
func LockTimePeriod(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, reference string, by *uuid.UUID) (uuid.UUID, error) {
	if to.Before(from) {
		return uuid.Nil, errs.Validation("invalid_period", "the period ends before it starts")
	}
	lid, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO hris.time_locks (id, property_id, period_start, period_end, reference, locked_by) VALUES ($1,$2,$3,$4,$5,$6)`,
		lid, property, dateArg(from), dateArg(to), reference, by); err != nil {
		return uuid.Nil, err
	}
	_, err = q.Exec(ctx, `UPDATE hris.attendance_days SET locked = true WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date AND NOT locked`,
		property, dateArg(from), dateArg(to))
	return lid, err
}

// ReleaseTimeLock releases a lock (e.g. a payroll run sent back for
// recalculation); days stay locked when another lock covers them.
func ReleaseTimeLock(ctx context.Context, q dbtx.Querier, property, lockID uuid.UUID, by *uuid.UUID, note string) error {
	var from, to time.Time
	err := q.QueryRow(ctx, `UPDATE hris.time_locks SET status = 'released', released_by = $3, released_at = now(), release_note = nullif($4, '')
		WHERE id = $1 AND property_id = $2 AND status = 'locked' RETURNING period_start, period_end`, lockID, property, by, note).Scan(&from, &to)
	if dbtx.IsNoRows(err) {
		return errs.Conflict("lock_not_active", "the period lock is not active")
	}
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE hris.attendance_days d SET locked = false WHERE d.property_id = $1 AND d.work_date BETWEEN $2::date AND $3::date AND d.locked
		AND NOT EXISTS (SELECT 1 FROM hris.time_locks l WHERE l.property_id = d.property_id AND l.status = 'locked'
		  AND d.work_date BETWEEN l.period_start AND l.period_end)`, property, dateArg(from), dateArg(to))
	return err
}

// TimeLocked reports whether a day is in a locked payroll period (and the
// lock reference).
func TimeLocked(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (bool, string, error) {
	var ref string
	err := q.QueryRow(ctx, `SELECT reference FROM hris.time_locks WHERE property_id = $1 AND status = 'locked' AND $2::date BETWEEN period_start AND period_end
		ORDER BY locked_at DESC LIMIT 1`, property, dateArg(day)).Scan(&ref)
	if dbtx.IsNoRows(err) {
		return false, "", nil
	}
	return err == nil, ref, err
}

// ErrTimeLocked is the refusal for a change in a locked period.
func ErrTimeLocked(day time.Time, reference string) error {
	return errs.Conflict("period_locked", "attendance of "+dateArg(day)+" is locked by payroll ("+reference+")")
}

// ── closing attendance days ──────────────────────────────────────────────

var (
	finalizerMu sync.RWMutex
	finalizer   func(ctx context.Context, tx pgx.Tx, property uuid.UUID, from, to time.Time) error
)

// SetAttendanceFinalizer installs the function closing attendance days
// (hris/hrtime registers it when the module is built).
func SetAttendanceFinalizer(f func(ctx context.Context, tx pgx.Tx, property uuid.UUID, from, to time.Time) error) {
	finalizerMu.Lock()
	defer finalizerMu.Unlock()
	finalizer = f
}

// FinalizeAttendance evaluates and closes every attendance day of a period
// up to yesterday (Absent for shifts without clock-in, Missing Clock-out,
// approved leave as On Leave) so TimeSummaries is complete. The daily job
// does the same for yesterday; payroll calls it before calculating.
func FinalizeAttendance(ctx context.Context, tx pgx.Tx, property uuid.UUID, from, to time.Time) error {
	finalizerMu.RLock()
	f := finalizer
	finalizerMu.RUnlock()
	if f == nil {
		return nil
	}
	return f(ctx, tx, property, from, to)
}
