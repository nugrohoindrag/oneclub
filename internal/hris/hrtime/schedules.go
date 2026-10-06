package hrtime

// Shift Schedule (EP-06): weekly / monthly rosters per org unit
// (FR-SCH-02) built from shift templates (FR-SCH-01) by assignment, copying
// a previous week and repeating patterns; validation (FR-SCH-03) against
// the staffing requirements, mandatory certifications (G5: nobody is
// scheduled with an expired certificate), working hours and rest days of
// the Attendance Configuration / Overtime Policy and approved leave; the
// operational demand as reference (FR-SCH-04); publishing to ESS with
// hris.schedule_published and notifications of later changes (FR-SCH-05).
// Department heads schedule the units they head; HR schedules every unit.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
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
)

// ShiftSchedule is a roster of an org unit.
type ShiftSchedule struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	PropertyID    uuid.UUID  `json:"propertyId" db:"property_id"`
	OrgUnitID     uuid.UUID  `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitCode   string     `json:"orgUnitCode" db:"org_unit_code"`
	OrgUnitName   string     `json:"orgUnitName" db:"org_unit_name"`
	Name          string     `json:"name" db:"name"`
	PeriodStart   time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd     time.Time  `json:"periodEnd" db:"period_end"`
	Status        string     `json:"status" db:"status" enum:"draft,published,cancelled"`
	Version       int        `json:"version" db:"version"`
	PublishedAt   *time.Time `json:"publishedAt" db:"published_at"`
	Notes         *string    `json:"notes" db:"notes"`
	Shifts        int        `json:"shifts" db:"shifts"`
	EmployeeCount int        `json:"employeeCount" db:"employees"`
	CreatedAt     time.Time  `json:"createdAt" db:"created_at"`
}

const scheduleSelect = `SELECT s.id, s.property_id, s.org_unit_id, ou.code AS org_unit_code, ou.name AS org_unit_name, s.name, s.period_start, s.period_end,
	s.status, s.version, s.published_at, s.notes, s.created_at,
	(SELECT count(*) FROM hris.shift_assignments a WHERE a.schedule_id = s.id AND a.kind = 'shift' AND a.status <> 'cancelled')::int AS shifts,
	(SELECT count(DISTINCT a.employee_id) FROM hris.shift_assignments a WHERE a.schedule_id = s.id AND a.status <> 'cancelled')::int AS employees
	FROM hris.schedules s JOIN hris.org_units ou ON ou.id = s.org_unit_id`

// ShiftAssignmentView is one day of an employee on a schedule.
type ShiftAssignmentView struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	ScheduleID      uuid.UUID  `json:"scheduleId" db:"schedule_id"`
	EmployeeID      uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo      string     `json:"employeeNo" db:"employee_no"`
	EmployeeName    string     `json:"employeeName" db:"employee_name"`
	WorkDate        time.Time  `json:"workDate" db:"work_date"`
	Kind            string     `json:"kind" db:"kind" enum:"shift,off"`
	ShiftTemplateID *uuid.UUID `json:"shiftTemplateId" db:"shift_template_id"`
	ShiftCode       *string    `json:"shiftCode" db:"shift_code"`
	ShiftName       *string    `json:"shiftName" db:"shift_name"`
	Color           *string    `json:"color" db:"color"`
	StartsAt        *time.Time `json:"startsAt" db:"starts_at"`
	EndsAt          *time.Time `json:"endsAt" db:"ends_at"`
	StartTime       *string    `json:"startTime" db:"start_time"`
	EndTime         *string    `json:"endTime" db:"end_time"`
	WorkMinutes     int        `json:"workMinutes" db:"work_minutes"`
	WorkforceRole   *string    `json:"workforceRole" db:"workforce_role"`
	Status          string     `json:"status" db:"status" enum:"scheduled,on_leave,cancelled"`
	SwappedFrom     *string    `json:"swappedFrom" db:"swapped_from"`
	Notes           *string    `json:"notes" db:"notes"`
	ScheduleStatus  string     `json:"scheduleStatus" db:"schedule_status"`
	OrgUnitName     string     `json:"orgUnitName" db:"org_unit_name"`
}

const assignmentSelect = `SELECT a.id, a.schedule_id, a.employee_id, e.employee_no, e.full_name AS employee_name, a.work_date, a.kind, a.shift_template_id,
	t.code AS shift_code, t.name AS shift_name, t.color, a.starts_at, a.ends_at, to_char(t.start_time, 'HH24:MI') AS start_time,
	to_char(t.end_time, 'HH24:MI') AS end_time, a.work_minutes, a.workforce_role, a.status, sw.full_name AS swapped_from, a.notes,
	s.status AS schedule_status, ou.name AS org_unit_name
	FROM hris.shift_assignments a JOIN hris.employees e ON e.id = a.employee_id JOIN hris.schedules s ON s.id = a.schedule_id
	JOIN hris.org_units ou ON ou.id = s.org_unit_id
	LEFT JOIN hris.shift_templates t ON t.id = a.shift_template_id LEFT JOIN hris.employees sw ON sw.id = a.swapped_from_id`

// ShiftScheduleEmployee is an employee of the schedule grid.
type ShiftScheduleEmployee struct {
	ID               uuid.UUID `json:"id"`
	EmployeeNo       string    `json:"employeeNo"`
	FullName         string    `json:"fullName"`
	Position         *string   `json:"position"`
	WorkforceRole    *string   `json:"workforceRole"`
	ScheduledMinutes int       `json:"scheduledMinutes"`
	Shifts           int       `json:"shifts"`
}

// ShiftCoverage is the staffing of a requirement on a date (FR-SCH-03).
type ShiftCoverage struct {
	Date          string  `json:"date"`
	RequirementID string  `json:"requirementId"`
	Position      *string `json:"position"`
	Shift         *string `json:"shift"`
	Required      int     `json:"required"`
	Assigned      int     `json:"assigned"`
	Short         int     `json:"short"`
}

// ShiftScheduleIssue is a validation finding; errors block publishing.
type ShiftScheduleIssue struct {
	Severity   string     `json:"severity" enum:"error,warning"`
	Code       string     `json:"code"`
	Message    string     `json:"message"`
	EmployeeID *uuid.UUID `json:"employeeId,omitempty"`
	Date       *string    `json:"date,omitempty"`
}

// ShiftLeaveMark is approved / pending leave inside the schedule period.
type ShiftLeaveMark struct {
	EmployeeID uuid.UUID `json:"employeeId" db:"employee_id"`
	Date       time.Time `json:"date" db:"d"`
	LeaveType  string    `json:"leaveType" db:"leave_type"`
	Status     string    `json:"status" db:"status"`
}

// ShiftScheduleDetail is a schedule with its grid, coverage, issues and the
// operational demand of the period.
type ShiftScheduleDetail struct {
	ShiftSchedule
	Employees   []ShiftScheduleEmployee `json:"employees"`
	Assignments []ShiftAssignmentView   `json:"assignments"`
	Coverage    []ShiftCoverage         `json:"coverage"`
	Issues      []ShiftScheduleIssue    `json:"issues"`
	Leave       []ShiftLeaveMark        `json:"leave"`
	Demand      []StaffingDemandDay     `json:"demand"`
	CanEdit     bool                    `json:"canEdit"`
}

// ShiftScheduleCreateRequest creates a draft schedule.
type ShiftScheduleCreateRequest struct {
	OrgUnitID        uuid.UUID  `json:"orgUnitId"`
	Name             string     `json:"name,omitempty"`
	PeriodStart      string     `json:"periodStart" doc:"First day (YYYY-MM-DD)"`
	PeriodEnd        string     `json:"periodEnd,omitempty" doc:"Last day; default one week"`
	CopyFromSchedule *uuid.UUID `json:"copyFromScheduleId,omitempty" doc:"Copy the assignments of another schedule (copy week)"`
	Notes            string     `json:"notes,omitempty"`
}

// ShiftScheduleUpdateRequest edits a schedule header.
type ShiftScheduleUpdateRequest struct {
	Name  *string `json:"name,omitempty"`
	Notes *string `json:"notes,omitempty"`
}

// ShiftAssignmentInput sets one day of an employee.
type ShiftAssignmentInput struct {
	EmployeeID      uuid.UUID  `json:"employeeId"`
	WorkDate        string     `json:"workDate"`
	ShiftTemplateID *uuid.UUID `json:"shiftTemplateId,omitempty" doc:"The shift; empty with off=true for a day off"`
	Off             bool       `json:"off,omitempty"`
	Clear           bool       `json:"clear,omitempty" doc:"Remove the assignment of the day"`
	Notes           string     `json:"notes,omitempty"`
}

// ShiftAssignRequest sets assignments of a schedule.
type ShiftAssignRequest struct {
	Assignments []ShiftAssignmentInput `json:"assignments"`
}

// ShiftCopyRequest copies another schedule (or the previous period).
type ShiftCopyRequest struct {
	FromScheduleID *uuid.UUID `json:"fromScheduleId,omitempty" doc:"Default: the schedule of the unit for the period just before"`
}

// ShiftPatternRequest repeats a pattern over the period (FR-SCH-02 pola berulang).
type ShiftPatternRequest struct {
	EmployeeIDs []uuid.UUID  `json:"employeeIds"`
	Pattern     []*uuid.UUID `json:"pattern" doc:"Cycle of shift template ids (null = day off), repeated from startDate"`
	StartDate   string       `json:"startDate,omitempty" doc:"Day the cycle starts (default: period start)"`
}

// ShiftScheduleMutation is the result of an edit (the detail plus what was
// skipped).
type ShiftScheduleMutation struct {
	Schedule ShiftScheduleDetail  `json:"schedule"`
	Applied  int                  `json:"applied"`
	Skipped  []ShiftScheduleIssue `json:"skipped"`
}

func (m *Module) registerSchedules(reg *route.Registry) {
	tag := "HRIS Schedules"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/schedules", Summary: "Shift schedules", Permission: PermScheduleView,
		Response: ShiftSchedule{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "orgUnitId"},
			{Name: "status", Enum: []string{"draft", "published", "cancelled"}}}, Handler: listRead(m.DB, m.listSchedulesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules", Summary: "Create a shift schedule (draft)",
		Permission: PermScheduleCreate, Request: ShiftScheduleCreateRequest{}, Response: ShiftScheduleDetail{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createScheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/schedules/{id}", Summary: "Shift schedule with grid, coverage and issues",
		Permission: PermScheduleView, Response: ShiftScheduleDetail{}, Handler: handle.Read(m.DB, m.getScheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/schedules/{id}", Summary: "Edit a shift schedule",
		Permission: PermScheduleUpdate, Request: ShiftScheduleUpdateRequest{}, Response: ShiftScheduleDetail{},
		Handler: handle.Write(m.DB, http.StatusOK, m.updateScheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}:assign", Summary: "Set shifts and days off",
		Permission: PermScheduleUpdate, Request: ShiftAssignRequest{}, Response: ShiftScheduleMutation{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.assignHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}:copy", Summary: "Copy the assignments of another schedule",
		Permission: PermScheduleUpdate, Request: ShiftCopyRequest{}, Response: ShiftScheduleMutation{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.copyHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}:apply-pattern", Summary: "Repeat a shift pattern over the period",
		Permission: PermScheduleUpdate, Request: ShiftPatternRequest{}, Response: ShiftScheduleMutation{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.patternHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}:publish", Summary: "Publish the schedule to Employee Self Service",
		Permission: PermSchedulePublish, Request: handle.Empty{}, Response: ShiftScheduleDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.publishHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}:cancel", Summary: "Cancel a draft schedule",
		Permission: PermScheduleUpdate, Request: handle.Empty{}, Response: ShiftSchedule{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelScheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/staffing-demand", Summary: "Operational demand as scheduling reference",
		Permission: PermScheduleView, Response: StaffingDemandDay{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: listRead(m.DB, m.demandHTTP)})
}

// canManageUnit: HR manages every unit; a department head the units they
// head and their sub-units.
func canManageUnit(ctx context.Context, q dbtx.Querier, property, unit uuid.UUID) (bool, error) {
	if can(ctx, PermAttendanceRecord, property) {
		return true, nil
	}
	mine := meOptional(ctx, q)
	if mine == nil {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, head_employee_id, 0 AS depth FROM hris.org_units WHERE id = $1
		  UNION ALL SELECT p.id, p.parent_id, p.head_employee_id, up.depth + 1 FROM hris.org_units p JOIN up ON p.id = up.parent_id WHERE up.depth < 20)
		SELECT EXISTS (SELECT 1 FROM up WHERE head_employee_id = $2)`, unit, mine.ID).Scan(&ok)
	return ok, err
}

func (m *Module) loadSchedule(ctx context.Context, q dbtx.Querier, sid uuid.UUID, lock bool) (ShiftSchedule, error) {
	if lock {
		if _, err := q.Exec(ctx, `SELECT 1 FROM hris.schedules WHERE id = $1 FOR UPDATE`, sid); err != nil {
			return ShiftSchedule{}, err
		}
	}
	return getOne[ShiftSchedule]("schedule")(q.Query(ctx, scheduleSelect+` WHERE s.id = $1`, sid))
}

func (m *Module) listSchedulesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ShiftSchedule, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", today(ctx, tx, property).AddDate(0, 0, -35))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(0, 0, 120))
	if err != nil {
		return nil, err
	}
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	return handle.List[ShiftSchedule](tx.Query(ctx, scheduleSelect+` WHERE s.property_id = $1 AND s.period_end >= $2::date AND s.period_start <= $3::date
		AND ($4::uuid IS NULL OR s.org_unit_id = $4) AND ($5 = '' OR s.status = $5) ORDER BY s.period_start DESC, ou.name LIMIT 500`,
		property, ymd(from), ymd(to), unit, r.URL.Query().Get("status")))
}

func (m *Module) getScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ShiftScheduleDetail, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	s, err := m.loadSchedule(ctx, tx, sid, false)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	if s.PropertyID != handle.Property(ctx) {
		return ShiftScheduleDetail{}, errs.NotFound("schedule")
	}
	return m.detail(ctx, tx, s)
}

// detail builds the grid, coverage, validation and demand of a schedule.
func (m *Module) detail(ctx context.Context, q pgx.Tx, s ShiftSchedule) (ShiftScheduleDetail, error) {
	d := ShiftScheduleDetail{ShiftSchedule: s, Employees: []ShiftScheduleEmployee{}, Coverage: []ShiftCoverage{}, Issues: []ShiftScheduleIssue{},
		Demand: []StaffingDemandDay{}}
	var err error
	if d.Assignments, err = handle.List[ShiftAssignmentView](q.Query(ctx, assignmentSelect+` WHERE a.schedule_id = $1 AND a.status <> 'cancelled'
		ORDER BY e.full_name, a.work_date`, s.ID)); err != nil {
		return d, err
	}
	emps, err := scheduleEmployees(ctx, q, s)
	if err != nil {
		return d, err
	}
	byEmp := map[uuid.UUID]*ShiftScheduleEmployee{}
	for _, e := range emps {
		se := ShiftScheduleEmployee{ID: e.ID, EmployeeNo: e.EmployeeNo, FullName: e.FullName, Position: e.PositionName, WorkforceRole: e.WorkforceRole}
		d.Employees = append(d.Employees, se)
	}
	for i := range d.Employees {
		byEmp[d.Employees[i].ID] = &d.Employees[i]
	}
	for _, a := range d.Assignments {
		if se, ok := byEmp[a.EmployeeID]; ok && a.Kind == "shift" {
			se.ScheduledMinutes += a.WorkMinutes
			se.Shifts++
		}
	}
	if d.Leave, err = handle.List[ShiftLeaveMark](q.Query(ctx, `SELECT r.employee_id, g::date AS d, r.leave_type, r.status FROM hris.leave_requests r,
		generate_series(greatest(r.start_date, $2::date), least(r.end_date, $3::date), interval '1 day') g
		WHERE r.property_id = $1 AND r.status IN ('submitted', 'approved') AND r.start_date <= $3::date AND r.end_date >= $2::date
		  AND r.employee_id = ANY ($4) ORDER BY 1, 2`, s.PropertyID, ymd(s.PeriodStart), ymd(s.PeriodEnd), empIDs(emps))); err != nil {
		return d, err
	}
	if d.Coverage, err = coverage(ctx, q, s); err != nil {
		return d, err
	}
	for _, c := range d.Coverage {
		if c.Short > 0 {
			date := c.Date
			what := "staff"
			if c.Position != nil {
				what = *c.Position
			}
			if c.Shift != nil {
				what += " on " + *c.Shift
			}
			d.Issues = append(d.Issues, ShiftScheduleIssue{Severity: "warning", Code: "understaffed", Date: &date,
				Message: fmt.Sprintf("%s: %d of %d %s scheduled", date, c.Assigned, c.Required, what)})
		}
	}
	issues, err := m.validateSchedule(ctx, q, s, d.Assignments, emps)
	if err != nil {
		return d, err
	}
	d.Issues = append(d.Issues, issues...)
	if m.Demand != nil {
		if dem, err := m.Demand(ctx, q, s.PropertyID, s.PeriodStart, s.PeriodEnd); err == nil && dem != nil {
			d.Demand = dem
		}
	}
	d.CanEdit, err = canManageUnit(ctx, q, s.PropertyID, s.OrgUnitID)
	d.CanEdit = d.CanEdit && s.Status != "cancelled" && can(ctx, PermScheduleUpdate, s.PropertyID)
	return d, err
}

func empIDs(list []hris.Employee) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(list))
	for _, e := range list {
		out = append(out, e.ID)
	}
	return out
}

// scheduleEmployees are the employees of the unit (and its sub-units)
// employed during the period plus everyone assigned on the schedule.
func scheduleEmployees(ctx context.Context, q dbtx.Querier, s ShiftSchedule) ([]hris.Employee, error) {
	return employeesQuery(ctx, q, hris.EmployeeSelect+` WHERE e.property_id = $1 AND e.archived_at IS NULL AND (
		  (e.org_unit_id IN (WITH RECURSIVE d AS (SELECT id FROM hris.org_units WHERE id = $2 UNION ALL
		     SELECT c.id FROM hris.org_units c JOIN d ON c.parent_id = d.id) SELECT id FROM d)
		   AND e.status = 'active' AND (e.join_date IS NULL OR e.join_date <= $4::date) AND (e.termination_date IS NULL OR e.termination_date > $3::date))
		  OR e.id IN (SELECT employee_id FROM hris.shift_assignments WHERE schedule_id = $5 AND status <> 'cancelled'))
		ORDER BY e.full_name, e.id`, s.PropertyID, s.OrgUnitID, ymd(s.PeriodStart), ymd(s.PeriodEnd), s.ID)
}

func employeesQuery(ctx context.Context, q dbtx.Querier, sql string, args ...any) ([]hris.Employee, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[hris.Employee])
	if out == nil {
		out = []hris.Employee{}
	}
	return out, err
}

// coverage compares assignments with the staffing requirements of the unit.
func coverage(ctx context.Context, q dbtx.Querier, s ShiftSchedule) ([]ShiftCoverage, error) {
	rows, err := q.Query(ctx, `SELECT to_char(g::date, 'YYYY-MM-DD'), r.id::text, p.name, t.name, r.min_staff,
		(SELECT count(*) FROM hris.shift_assignments a JOIN hris.schedules sc ON sc.id = a.schedule_id AND sc.status <> 'cancelled'
		   JOIN hris.employees e ON e.id = a.employee_id
		 WHERE a.work_date = g::date AND a.kind = 'shift' AND a.status = 'scheduled' AND (sc.id = $5 OR sc.org_unit_id = r.org_unit_id)
		   AND (r.shift_template_id IS NULL OR a.shift_template_id = r.shift_template_id) AND (r.position_id IS NULL OR e.position_id = r.position_id))::int
		FROM hris.staffing_requirements r LEFT JOIN hris.positions p ON p.id = r.position_id LEFT JOIN hris.shift_templates t ON t.id = r.shift_template_id,
		generate_series($3::date, $4::date, interval '1 day') g
		WHERE r.property_id = $1 AND r.org_unit_id = $2 AND r.status = 'active' AND r.archived_at IS NULL
		  AND extract(isodow FROM g)::int = ANY (r.weekdays)
		ORDER BY 1, 3 NULLS FIRST, 4 NULLS FIRST`, s.PropertyID, s.OrgUnitID, ymd(s.PeriodStart), ymd(s.PeriodEnd), s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShiftCoverage{}
	for rows.Next() {
		var c ShiftCoverage
		if err := rows.Scan(&c.Date, &c.RequirementID, &c.Position, &c.Shift, &c.Required, &c.Assigned); err != nil {
			return nil, err
		}
		c.Short = max(c.Required-c.Assigned, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// validateSchedule checks hours, rest days, certifications and leave of
// the assignments (FR-SCH-03); errors block publishing.
func (m *Module) validateSchedule(ctx context.Context, q pgx.Tx, s ShiftSchedule, list []ShiftAssignmentView, emps []hris.Employee) ([]ShiftScheduleIssue, error) {
	issues := []ShiftScheduleIssue{}
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, q, s.PropertyID, hris.PolicyTime(s.PeriodStart))
	if err != nil {
		return nil, err
	}
	ot, _, err := hris.LoadOvertimePolicy(ctx, q, s.PropertyID, hris.PolicyTime(s.PeriodStart))
	if err != nil {
		return nil, err
	}
	standard := hris.Dec(cfg.StandardWeeklyHours).Mul(decimal.NewFromInt(60))
	limit := standard.Add(hris.Dec(ot.MaxHoursPerWeek).Mul(decimal.NewFromInt(60)))
	names := map[uuid.UUID]hris.Employee{}
	for _, e := range emps {
		names[e.ID] = e
	}
	type weekKey struct {
		emp  uuid.UUID
		week string
	}
	minutes := map[weekKey]int{}
	shifts := map[weekKey]int{}
	for _, a := range list {
		y, w := a.WorkDate.ISOWeek()
		k := weekKey{a.EmployeeID, fmt.Sprintf("%d-W%02d", y, w)}
		if a.Kind == "shift" && a.Status == "scheduled" {
			minutes[k] += a.WorkMinutes
			shifts[k]++
		}
	}
	keys := make([]weekKey, 0, len(minutes))
	for k := range minutes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].week+keys[i].emp.String() < keys[j].week+keys[j].emp.String() })
	for _, k := range keys {
		e := names[k.emp]
		emp := k.emp
		ww, err := workWeekOf(ctx, q, e, s.PeriodStart)
		if err != nil {
			return nil, err
		}
		mins := decimal.NewFromInt(int64(minutes[k]))
		switch {
		case limit.IsPositive() && mins.GreaterThan(limit):
			issues = append(issues, ShiftScheduleIssue{Severity: "error", Code: "hours_over_limit", EmployeeID: &emp,
				Message: fmt.Sprintf("%s: %s hours in %s exceed the %s weekly hours plus %s overtime hours", e.FullName, mins.Div(decimal.NewFromInt(60)).Round(1),
					k.week, cfg.StandardWeeklyHours, ot.MaxHoursPerWeek)})
		case standard.IsPositive() && mins.GreaterThan(standard):
			issues = append(issues, ShiftScheduleIssue{Severity: "warning", Code: "planned_overtime", EmployeeID: &emp,
				Message: fmt.Sprintf("%s: %s hours in %s are above the %s standard weekly hours (overtime)", e.FullName, mins.Div(decimal.NewFromInt(60)).Round(1),
					k.week, cfg.StandardWeeklyHours)})
		}
		if shifts[k] > ww {
			issues = append(issues, ShiftScheduleIssue{Severity: "error", Code: "rest_days", EmployeeID: &emp,
				Message: fmt.Sprintf("%s: %d shifts in %s; a %d-day work week needs %d rest days", e.FullName, shifts[k], k.week, ww, 7-ww)})
		}
	}
	for _, a := range list {
		if a.Kind != "shift" || a.Status != "scheduled" {
			continue
		}
		emp, date := a.EmployeeID, ymd(a.WorkDate)
		role := ""
		if a.WorkforceRole != nil {
			role = *a.WorkforceRole
		}
		chk, err := hris.CheckEmployee(ctx, q, a.EmployeeID, role, a.WorkDate)
		if err != nil {
			return nil, err
		}
		if e := chk.Err(a.EmployeeName); e != nil {
			pe, _ := errs.As(e)
			issues = append(issues, ShiftScheduleIssue{Severity: "error", Code: "certification_invalid", EmployeeID: &emp, Date: &date,
				Message: date + ": " + pe.Message})
		}
		var lt, st string
		err = q.QueryRow(ctx, `SELECT leave_type, status FROM hris.leave_requests WHERE employee_id = $1 AND status IN ('submitted', 'approved')
			AND $2::date BETWEEN start_date AND end_date ORDER BY status LIMIT 1`, a.EmployeeID, date).Scan(&lt, &st)
		if err != nil && !dbtx.IsNoRows(err) {
			return nil, err
		}
		if err == nil {
			sev := "warning"
			if st == hris.RequestApproved {
				sev = "error"
			}
			issues = append(issues, ShiftScheduleIssue{Severity: sev, Code: "leave_conflict", EmployeeID: &emp, Date: &date,
				Message: fmt.Sprintf("%s: %s has %s leave (%s)", date, a.EmployeeName, strings.ToLower(lt), st)})
		}
	}
	return issues, nil
}

func (m *Module) createScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftScheduleCreateRequest) (ShiftScheduleDetail, error) {
	property := handle.Property(ctx)
	start, err := mustDate("periodStart", req.PeriodStart)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	end := start.AddDate(0, 0, 6)
	if p, err := parseDate("periodEnd", req.PeriodEnd); err != nil {
		return ShiftScheduleDetail{}, err
	} else if p != nil {
		end = *p
	}
	if end.Before(start) || end.Sub(start) > 41*24*time.Hour {
		return ShiftScheduleDetail{}, handle.Invalid("periodEnd", "invalid", "a schedule covers 1 to 42 days")
	}
	var code, uname string
	if err := tx.QueryRow(ctx, `SELECT code, name FROM hris.org_units WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, req.OrgUnitID, property).
		Scan(&code, &uname); err != nil {
		return ShiftScheduleDetail{}, handle.Invalid("orgUnitId", "not_found", "org unit not found")
	}
	if ok, err := canManageUnit(ctx, tx, property, req.OrgUnitID); err != nil || !ok {
		if err != nil {
			return ShiftScheduleDetail{}, err
		}
		return ShiftScheduleDetail{}, errs.Forbidden("you schedule only the units you head")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		y, w := start.ISOWeek()
		name = fmt.Sprintf("%s %d-W%02d", uname, y, w)
		if end.Sub(start) > 7*24*time.Hour {
			name = fmt.Sprintf("%s %s – %s", uname, ymd(start), ymd(end))
		}
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.schedules (id, property_id, org_unit_id, name, period_start, period_end, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, sid, property, req.OrgUnitID, name, ymd(start), ymd(end), nullStr(req.Notes), actor(ctx)); err != nil {
		if dbtx.IsExclusionViolation(err) {
			return ShiftScheduleDetail{}, errs.Conflict("schedule_overlaps", "the unit already has a schedule in this period")
		}
		return ShiftScheduleDetail{}, err
	}
	s, err := m.loadSchedule(ctx, tx, sid, false)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	copied := 0
	if req.CopyFromSchedule != nil {
		res, err := m.copyInto(ctx, tx, s, *req.CopyFromSchedule)
		if err != nil {
			return ShiftScheduleDetail{}, err
		}
		copied = res.applied
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.schedule", EntityID: sid.String(),
		EntityLabel: name, PropertyID: &property, After: map[string]any{"orgUnit": code, "periodStart": ymd(start), "periodEnd": ymd(end), "copied": copied}}); err != nil {
		return ShiftScheduleDetail{}, err
	}
	if s, err = m.loadSchedule(ctx, tx, sid, false); err != nil {
		return ShiftScheduleDetail{}, err
	}
	return m.detail(ctx, tx, s)
}

// editable loads a schedule for an edit by someone managing its unit.
func (m *Module) editable(ctx context.Context, tx pgx.Tx, r *http.Request) (ShiftSchedule, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return ShiftSchedule{}, err
	}
	s, err := m.loadSchedule(ctx, tx, sid, true)
	if err != nil {
		return s, err
	}
	if s.PropertyID != handle.Property(ctx) {
		return s, errs.NotFound("schedule")
	}
	if s.Status == "cancelled" {
		return s, statusErr("the schedule", s.Status)
	}
	ok, err := canManageUnit(ctx, tx, s.PropertyID, s.OrgUnitID)
	if err != nil {
		return s, err
	}
	if !ok {
		return s, errs.Forbidden("you schedule only the units you head")
	}
	return s, nil
}

func (m *Module) updateScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftScheduleUpdateRequest) (ShiftScheduleDetail, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return ShiftScheduleDetail{}, handle.Invalid("name", "required", "is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.schedules SET name = coalesce($2, name), notes = CASE WHEN $3::text IS NULL THEN notes ELSE nullif($3, '') END,
		updated_by = $4 WHERE id = $1`, s.ID, req.Name, req.Notes, actor(ctx)); err != nil {
		return ShiftScheduleDetail{}, err
	}
	after, err := m.loadSchedule(ctx, tx, s.ID, false)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.schedule", EntityID: s.ID.String(),
		EntityLabel: after.Name, PropertyID: &s.PropertyID, Before: map[string]any{"name": s.Name, "notes": s.Notes},
		After: map[string]any{"name": after.Name, "notes": after.Notes}}); err != nil {
		return ShiftScheduleDetail{}, err
	}
	return m.detail(ctx, tx, after)
}

// mutation collects the outcome of assignment changes.
type mutation struct {
	applied  int
	skipped  []ShiftScheduleIssue
	changed  map[uuid.UUID][]string // employee → dates (published schedules)
	pastDays map[uuid.UUID][]time.Time
}

func newMutation() *mutation {
	return &mutation{skipped: []ShiftScheduleIssue{}, changed: map[uuid.UUID][]string{}, pastDays: map[uuid.UUID][]time.Time{}}
}

type templateRow struct {
	ID                         uuid.UUID
	Code, Name                 string
	StartH, StartM, EndH, EndM int
	Break, Work                int
	Role                       *string
	Status                     string
}

func loadTemplate(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID) (*templateRow, error) {
	var t templateRow
	var start, end string
	err := q.QueryRow(ctx, `SELECT id, code, name, to_char(start_time, 'HH24:MI'), to_char(end_time, 'HH24:MI'), break_minutes, work_minutes, workforce_role,
		status FROM hris.shift_templates WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, tid, property).
		Scan(&t.ID, &t.Code, &t.Name, &start, &end, &t.Break, &t.Work, &t.Role, &t.Status)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fmt.Sscanf(start, "%d:%d", &t.StartH, &t.StartM) //nolint:errcheck // format from to_char
	fmt.Sscanf(end, "%d:%d", &t.EndH, &t.EndM)       //nolint:errcheck // format from to_char
	return &t, nil
}

// window is the instants of a template on a date.
func (t templateRow) window(day time.Time, loc *time.Location) (time.Time, time.Time) {
	start := localAt(day, t.StartH, t.StartM, loc)
	end := localAt(day, t.EndH, t.EndM, loc)
	if !end.After(start) {
		end = localAt(day.AddDate(0, 0, 1), t.EndH, t.EndM, loc)
	}
	return start.UTC(), end.UTC()
}

// setAssignment applies one assignment input to a schedule; a refused input
// is reported in mu.skipped when soft, else returned as error.
func (m *Module) setAssignment(ctx context.Context, tx pgx.Tx, s ShiftSchedule, in ShiftAssignmentInput, mu *mutation, soft bool,
	templates map[uuid.UUID]*templateRow, loc *time.Location, tday time.Time) error {
	skip := func(code, msg string, day *string) error {
		emp := in.EmployeeID
		if !soft {
			if code == "certification_invalid" {
				return errs.Conflict(code, msg)
			}
			return errs.Validation(code, msg, errs.Field("assignments", code, msg))
		}
		mu.skipped = append(mu.skipped, ShiftScheduleIssue{Severity: "error", Code: code, Message: msg, EmployeeID: &emp, Date: day})
		return nil
	}
	day, err := time.Parse("2006-01-02", strings.TrimSpace(in.WorkDate))
	if err != nil {
		return handle.Invalid("workDate", "invalid_date", "must be a date (YYYY-MM-DD)")
	}
	d := ymd(day)
	if day.Before(s.PeriodStart) || day.After(s.PeriodEnd) {
		return skip("outside_period", d+" is outside the schedule period", &d)
	}
	if err := checkLocked(ctx, tx, s.PropertyID, day); err != nil {
		return skip("period_locked", err.Error(), &d)
	}
	emp, err := hris.EmployeeByID(ctx, tx, in.EmployeeID)
	if err != nil || emp.PropertyID != s.PropertyID {
		return skip("employee_not_found", "employee not found", &d)
	}
	var existing *uuid.UUID
	var existingSchedule uuid.UUID
	var existingName string
	err = tx.QueryRow(ctx, `SELECT a.id, a.schedule_id, s.name FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id
		WHERE a.employee_id = $1 AND a.work_date = $2::date AND a.status <> 'cancelled' FOR UPDATE OF a`, in.EmployeeID, d).
		Scan(&existing, &existingSchedule, &existingName)
	if err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	if existing != nil && existingSchedule != s.ID {
		return skip("already_scheduled", fmt.Sprintf("%s is already scheduled on %s in %s", emp.FullName, d, existingName), &d)
	}
	mark := func() {
		if s.Status == "published" {
			mu.changed[emp.ID] = append(mu.changed[emp.ID], d)
		}
		if !day.After(tday) {
			mu.pastDays[emp.ID] = append(mu.pastDays[emp.ID], day)
		}
	}
	changedFlag := s.Status == "published"
	if in.Clear {
		if existing == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'cancelled', changed_after_publish = $2, updated_by = $3 WHERE id = $1`,
			*existing, changedFlag, actor(ctx)); err != nil {
			return err
		}
		mu.applied++
		mark()
		return nil
	}
	if !emp.EmployedOn(day) {
		return skip("not_employed", fmt.Sprintf("%s is not employed on %s", emp.FullName, d), &d)
	}
	kind := "off"
	var tmpl *templateRow
	if !in.Off {
		if in.ShiftTemplateID == nil {
			return skip("shift_required", "choose a shift or a day off", &d)
		}
		kind = "shift"
		t, ok := templates[*in.ShiftTemplateID]
		if !ok {
			if t, err = loadTemplate(ctx, tx, s.PropertyID, *in.ShiftTemplateID); err != nil {
				return err
			}
			templates[*in.ShiftTemplateID] = t
		}
		if t == nil || t.Status != "active" {
			return skip("shift_not_found", "shift template not found or inactive", &d)
		}
		tmpl = t
		role := ""
		if t.Role != nil {
			role = *t.Role
		}
		chk, err := hris.CheckEmployee(ctx, tx, emp.ID, role, day)
		if err != nil {
			return err
		}
		if e := chk.Err(emp.FullName); e != nil {
			pe, _ := errs.As(e)
			return skip("certification_invalid", d+": "+pe.Message, &d)
		}
		var lt string
		err = tx.QueryRow(ctx, `SELECT leave_type FROM hris.leave_requests WHERE employee_id = $1 AND status = 'approved'
			AND $2::date BETWEEN start_date AND end_date AND half_day IS NULL LIMIT 1`, emp.ID, d).Scan(&lt)
		if err == nil {
			return skip("on_leave", fmt.Sprintf("%s is on approved %s leave on %s", emp.FullName, strings.ToLower(lt), d), &d)
		}
		if !dbtx.IsNoRows(err) {
			return err
		}
	}
	var tid *uuid.UUID
	var starts, ends *time.Time
	brk, work := 0, 0
	var role *string
	if tmpl != nil {
		tid = &tmpl.ID
		st, en := tmpl.window(day, loc)
		starts, ends, brk, work = &st, &en, tmpl.Break, tmpl.Work
		role = tmpl.Role
		if role == nil {
			role = emp.WorkforceRole
		}
		// a night shift of the day before must have ended
		var overlap bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_assignments WHERE employee_id = $1 AND status <> 'cancelled' AND kind = 'shift'
			AND work_date IN ($2::date - 1, $2::date + 1) AND tstzrange(starts_at, ends_at) && tstzrange($3, $4))`, emp.ID, d, st, en).Scan(&overlap); err != nil {
			return err
		}
		if overlap {
			return skip("overlap", fmt.Sprintf("%s: the shift overlaps the shift of an adjacent day", d), &d)
		}
	}
	if existing != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET kind = $2, shift_template_id = $3, starts_at = $4, ends_at = $5, break_minutes = $6,
			work_minutes = $7, workforce_role = $8, notes = $9, status = 'scheduled', changed_after_publish = $10, updated_by = $11 WHERE id = $1`,
			*existing, kind, tid, starts, ends, brk, work, role, nullStr(in.Notes), changedFlag, actor(ctx)); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `INSERT INTO hris.shift_assignments (id, property_id, schedule_id, employee_id, work_date, kind, shift_template_id,
		starts_at, ends_at, break_minutes, work_minutes, workforce_role, notes, changed_after_publish, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`, id.New(), s.PropertyID, s.ID, emp.ID, d, kind, tid, starts, ends, brk, work, role,
		nullStr(in.Notes), changedFlag, actor(ctx)); err != nil {
		return err
	}
	mu.applied++
	mark()
	return nil
}

// afterMutation recomputes past days, publishes changes of a published
// schedule (hris.schedule_published, notifications) and audits.
func (m *Module) afterMutation(ctx context.Context, tx pgx.Tx, s ShiftSchedule, mu *mutation, action string, meta map[string]any) (ShiftScheduleMutation, error) {
	pol := newPolicies(s.PropertyID)
	tday := today(ctx, tx, s.PropertyID)
	for eid, days := range mu.pastDays {
		emp, err := hris.EmployeeByID(ctx, tx, eid)
		if err != nil {
			return ShiftScheduleMutation{}, err
		}
		for _, d := range days {
			if _, err := m.recomputeDay(ctx, tx, emp, d, d.Before(tday), pol); err != nil {
				return ShiftScheduleMutation{}, err
			}
		}
	}
	if s.Status == "published" && len(mu.changed) > 0 {
		if err := m.publishChanges(ctx, tx, s, mu.changed); err != nil {
			return ShiftScheduleMutation{}, err
		}
	}
	after, err := m.loadSchedule(ctx, tx, s.ID, false)
	if err != nil {
		return ShiftScheduleMutation{}, err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["applied"], meta["skipped"] = mu.applied, len(mu.skipped)
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: action, EntityType: "hris.schedule", EntityID: s.ID.String(),
		EntityLabel: s.Name, PropertyID: &s.PropertyID, After: meta}); err != nil {
		return ShiftScheduleMutation{}, err
	}
	d, err := m.detail(ctx, tx, after)
	return ShiftScheduleMutation{Schedule: d, Applied: mu.applied, Skipped: mu.skipped}, err
}

func (m *Module) assignHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftAssignRequest) (ShiftScheduleMutation, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return ShiftScheduleMutation{}, err
	}
	if len(req.Assignments) == 0 || len(req.Assignments) > 1000 {
		return ShiftScheduleMutation{}, handle.Invalid("assignments", "invalid", "send 1 to 1000 assignments")
	}
	mu := newMutation()
	loc := location(ctx, tx, s.PropertyID)
	tday := today(ctx, tx, s.PropertyID)
	templates := map[uuid.UUID]*templateRow{}
	for _, in := range req.Assignments {
		if err := m.setAssignment(ctx, tx, s, in, mu, false, templates, loc, tday); err != nil {
			return ShiftScheduleMutation{}, err
		}
	}
	return m.afterMutation(ctx, tx, s, mu, "assign", map[string]any{"assignments": len(req.Assignments)})
}

type copyResult struct{ applied int }

// copyInto copies the assignments of another schedule, shifted by the
// distance between the period starts (copy week).
func (m *Module) copyInto(ctx context.Context, tx pgx.Tx, s ShiftSchedule, from uuid.UUID) (copyResult, error) {
	mu := newMutation()
	src, err := m.loadSchedule(ctx, tx, from, false)
	if err != nil {
		return copyResult{}, err
	}
	if src.PropertyID != s.PropertyID || src.ID == s.ID {
		return copyResult{}, handle.Invalid("fromScheduleId", "invalid", "choose another schedule of this property")
	}
	if err := m.copyAssignments(ctx, tx, s, src, mu); err != nil {
		return copyResult{}, err
	}
	return copyResult{applied: mu.applied}, nil
}

func (m *Module) copyAssignments(ctx context.Context, tx pgx.Tx, s, src ShiftSchedule, mu *mutation) error {
	offset := int(s.PeriodStart.Sub(src.PeriodStart).Hours() / 24)
	list, err := handle.List[ShiftAssignmentView](tx.Query(ctx, assignmentSelect+` WHERE a.schedule_id = $1 AND a.status <> 'cancelled'
		ORDER BY a.work_date, e.full_name`, src.ID))
	if err != nil {
		return err
	}
	loc := location(ctx, tx, s.PropertyID)
	tday := today(ctx, tx, s.PropertyID)
	templates := map[uuid.UUID]*templateRow{}
	for _, a := range list {
		day := a.WorkDate.AddDate(0, 0, offset)
		if day.Before(s.PeriodStart) || day.After(s.PeriodEnd) {
			continue
		}
		in := ShiftAssignmentInput{EmployeeID: a.EmployeeID, WorkDate: ymd(day), ShiftTemplateID: a.ShiftTemplateID, Off: a.Kind == "off"}
		if err := m.setAssignment(ctx, tx, s, in, mu, true, templates, loc, tday); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) copyHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftCopyRequest) (ShiftScheduleMutation, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return ShiftScheduleMutation{}, err
	}
	var src ShiftSchedule
	if req.FromScheduleID != nil {
		if src, err = m.loadSchedule(ctx, tx, *req.FromScheduleID, false); err != nil {
			return ShiftScheduleMutation{}, err
		}
	} else {
		var sid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM hris.schedules WHERE org_unit_id = $1 AND status <> 'cancelled' AND period_end < $2::date
			ORDER BY period_end DESC LIMIT 1`, s.OrgUnitID, ymd(s.PeriodStart)).Scan(&sid); err != nil {
			if dbtx.IsNoRows(err) {
				return ShiftScheduleMutation{}, errs.Conflict("nothing_to_copy", "the unit has no earlier schedule")
			}
			return ShiftScheduleMutation{}, err
		}
		if src, err = m.loadSchedule(ctx, tx, sid, false); err != nil {
			return ShiftScheduleMutation{}, err
		}
	}
	if src.PropertyID != s.PropertyID || src.ID == s.ID {
		return ShiftScheduleMutation{}, handle.Invalid("fromScheduleId", "invalid", "choose another schedule of this property")
	}
	mu := newMutation()
	if err := m.copyAssignments(ctx, tx, s, src, mu); err != nil {
		return ShiftScheduleMutation{}, err
	}
	return m.afterMutation(ctx, tx, s, mu, "copy", map[string]any{"from": src.Name})
}

func (m *Module) patternHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftPatternRequest) (ShiftScheduleMutation, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return ShiftScheduleMutation{}, err
	}
	if len(req.EmployeeIDs) == 0 || len(req.EmployeeIDs) > 200 {
		return ShiftScheduleMutation{}, handle.Invalid("employeeIds", "invalid", "choose 1 to 200 employees")
	}
	if len(req.Pattern) == 0 || len(req.Pattern) > 28 {
		return ShiftScheduleMutation{}, handle.Invalid("pattern", "invalid", "a pattern has 1 to 28 days")
	}
	start := s.PeriodStart
	if p, err := parseDate("startDate", req.StartDate); err != nil {
		return ShiftScheduleMutation{}, err
	} else if p != nil {
		start = *p
	}
	mu := newMutation()
	loc := location(ctx, tx, s.PropertyID)
	tday := today(ctx, tx, s.PropertyID)
	templates := map[uuid.UUID]*templateRow{}
	n := len(req.Pattern)
	for _, eid := range req.EmployeeIDs {
		for day := s.PeriodStart; !day.After(s.PeriodEnd); day = day.AddDate(0, 0, 1) {
			idx := int(day.Sub(start).Hours()/24) % n
			if idx < 0 {
				idx += n
			}
			t := req.Pattern[idx]
			in := ShiftAssignmentInput{EmployeeID: eid, WorkDate: ymd(day), ShiftTemplateID: t, Off: t == nil}
			if err := m.setAssignment(ctx, tx, s, in, mu, true, templates, loc, tday); err != nil {
				return ShiftScheduleMutation{}, err
			}
		}
	}
	return m.afterMutation(ctx, tx, s, mu, "apply_pattern", map[string]any{"employees": len(req.EmployeeIDs), "patternDays": n})
}

func (m *Module) publishHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ShiftScheduleDetail, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return ShiftScheduleDetail{}, err
	}
	if s.Status != "draft" {
		return ShiftScheduleDetail{}, statusErr("the schedule", s.Status)
	}
	d, err := m.detail(ctx, tx, s)
	if err != nil {
		return d, err
	}
	var blocking []string
	for _, i := range d.Issues {
		if i.Severity == "error" {
			blocking = append(blocking, i.Message)
		}
	}
	if len(blocking) > 0 {
		if len(blocking) > 5 {
			blocking = append(blocking[:5], fmt.Sprintf("… %d more", len(blocking)-5))
		}
		return d, errs.Conflict("schedule_invalid", "fix the schedule before publishing: "+strings.Join(blocking, "; "))
	}
	if d.Shifts == 0 {
		return d, errs.Conflict("schedule_empty", "the schedule has no shifts")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.schedules SET status = 'published', version = version + 1, published_at = now(), published_by = $2, updated_by = $2
		WHERE id = $1`, s.ID, actor(ctx)); err != nil {
		return d, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET changed_after_publish = false WHERE schedule_id = $1`, s.ID); err != nil {
		return d, err
	}
	after, err := m.loadSchedule(ctx, tx, s.ID, false)
	if err != nil {
		return d, err
	}
	// recompute days already started
	tday := today(ctx, tx, s.PropertyID)
	if !s.PeriodStart.After(tday) {
		to := s.PeriodEnd
		if to.After(tday) {
			to = tday
		}
		if err := m.evaluateRange(ctx, tx, s.PropertyID, s.PeriodStart, to, empIDsOf(d.Assignments), true); err != nil {
			return d, err
		}
	}
	perEmp := map[uuid.UUID]int{}
	minutes := 0
	for _, a := range d.Assignments {
		if _, ok := perEmp[a.EmployeeID]; !ok {
			perEmp[a.EmployeeID] = 0
		}
		if a.Kind == "shift" {
			perEmp[a.EmployeeID]++
			minutes += a.WorkMinutes
		}
	}
	ids := make([]uuid.UUID, 0, len(perEmp))
	for e := range perEmp {
		ids = append(ids, e)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if _, err := m.Events.Publish(ctx, tx, hris.EventSchedulePublished, "hris.schedule", &s.ID, &s.PropertyID, hris.SchedulePublished{
		ScheduleID: s.ID, PropertyID: s.PropertyID, OrgUnitID: s.OrgUnitID, OrgUnitCode: s.OrgUnitCode, OrgUnitName: s.OrgUnitName, Name: s.Name,
		PeriodStart: ymd(s.PeriodStart), PeriodEnd: ymd(s.PeriodEnd), Version: after.Version, Shifts: d.Shifts, EmployeeIDs: ids,
		ScheduledHours: decimal.NewFromInt(int64(minutes)).Div(decimal.NewFromInt(60)).Round(2).String()}); err != nil {
		return d, err
	}
	for _, eid := range ids {
		if u := userOf(ctx, tx, eid); u != nil {
			if err := m.notifyUsers(ctx, tx, s.PropertyID, []uuid.UUID{*u}, NotifySchedulePublished, "/ops/ess/schedule", map[string]any{
				"orgUnit": s.OrgUnitName, "name": s.Name, "periodStart": ymd(s.PeriodStart), "periodEnd": ymd(s.PeriodEnd), "shifts": perEmp[eid]},
				"in_app", "email", "whatsapp"); err != nil {
				return d, err
			}
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "publish", EntityType: "hris.schedule", EntityID: s.ID.String(),
		EntityLabel: s.Name, PropertyID: &s.PropertyID, Before: map[string]any{"status": s.Status},
		After: map[string]any{"status": "published", "version": after.Version, "shifts": d.Shifts, "employees": len(ids)}}); err != nil {
		return d, err
	}
	return m.detail(ctx, tx, after)
}

func empIDsOf(list []ShiftAssignmentView) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, a := range list {
		if !seen[a.EmployeeID] {
			seen[a.EmployeeID] = true
			out = append(out, a.EmployeeID)
		}
	}
	return out
}

// publishChanges notifies the employees whose days changed on a published
// schedule and republishes the event (version + 1).
func (m *Module) publishChanges(ctx context.Context, tx pgx.Tx, s ShiftSchedule, changed map[uuid.UUID][]string) error {
	var version int
	if err := tx.QueryRow(ctx, `UPDATE hris.schedules SET version = version + 1 WHERE id = $1 RETURNING version`, s.ID).Scan(&version); err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(changed))
	for e := range changed {
		ids = append(ids, e)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	var shifts, minutes int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'shift'), coalesce(sum(work_minutes), 0) FROM hris.shift_assignments
		WHERE schedule_id = $1 AND status <> 'cancelled'`, s.ID).Scan(&shifts, &minutes); err != nil {
		return err
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventSchedulePublished, "hris.schedule", &s.ID, &s.PropertyID, hris.SchedulePublished{
		ScheduleID: s.ID, PropertyID: s.PropertyID, OrgUnitID: s.OrgUnitID, OrgUnitCode: s.OrgUnitCode, OrgUnitName: s.OrgUnitName, Name: s.Name,
		PeriodStart: ymd(s.PeriodStart), PeriodEnd: ymd(s.PeriodEnd), Version: version, Republished: true, Shifts: shifts, EmployeeIDs: ids,
		ScheduledHours: decimal.NewFromInt(int64(minutes)).Div(decimal.NewFromInt(60)).Round(2).String()}); err != nil {
		return err
	}
	for _, eid := range ids {
		if u := userOf(ctx, tx, eid); u != nil {
			dates := slices.Compact(slices.Sorted(slices.Values(changed[eid])))
			if err := m.notifyUsers(ctx, tx, s.PropertyID, []uuid.UUID{*u}, NotifyScheduleChanged, "/ops/ess/schedule", map[string]any{
				"orgUnit": s.OrgUnitName, "name": s.Name, "dates": strings.Join(dates, ", ")}, "in_app", "email", "whatsapp"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) cancelScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ShiftSchedule, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return s, err
	}
	if s.Status != "draft" {
		return s, errs.Conflict("invalid_status", "only a draft schedule can be cancelled; change the shifts of a published one")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.schedules SET status = 'cancelled', updated_by = $2 WHERE id = $1`, s.ID, actor(ctx)); err != nil {
		return s, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'cancelled' WHERE schedule_id = $1`, s.ID); err != nil {
		return s, err
	}
	after, err := m.loadSchedule(ctx, tx, s.ID, false)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: "hris.schedule",
		EntityID: s.ID.String(), EntityLabel: s.Name, PropertyID: &s.PropertyID, Before: map[string]any{"status": s.Status},
		After: map[string]any{"status": after.Status}})
}

func (m *Module) demandHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]StaffingDemandDay, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(0, 0, 6))
	if err != nil {
		return nil, err
	}
	if to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		return nil, handle.Invalid("to", "invalid", "a period of at most 63 days")
	}
	if m.Demand == nil {
		return []StaffingDemandDay{}, nil
	}
	return m.Demand(ctx, tx, property, from, to)
}
