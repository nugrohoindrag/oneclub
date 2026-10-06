package hrtime

// Employee Self Service of time & attendance (EP-16 FR-ESS-02/03/05,
// EP-26): My Schedule (offline cache, swaps), Clock In / Out (mobile GPS,
// the personal kiosk QR, the attendance PIN), Attendance History (with
// corrections), and the manager's Approvals, Team Schedule and Team
// Attendance (review queue of out-of-area clock-ins, overtime exceptions).
// The sections are registered in hris (p5_time.go).

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ESSColleague is a colleague a shift can be swapped with.
type ESSColleague struct {
	ID          uuid.UUID             `json:"id"`
	EmployeeNo  string                `json:"employeeNo"`
	FullName    string                `json:"fullName"`
	Position    *string               `json:"position"`
	Assignments []ShiftAssignmentView `json:"assignments"`
}

// MyShiftSchedule is the schedule of the employee (FR-ESS-02, FR-SCH-07).
type MyShiftSchedule struct {
	From        string                `json:"from"`
	To          string                `json:"to"`
	Assignments []ShiftAssignmentView `json:"assignments"`
	Leave       []LeaveCalendarEntry  `json:"leave"`
	Holidays    []ESSHoliday          `json:"holidays"`
	Swaps       []ShiftSwap           `json:"swaps"`
	Colleagues  []ESSColleague        `json:"colleagues"`
	CachedAt    time.Time             `json:"cachedAt" doc:"Server time; the app keeps the schedule for offline use"`
}

// ESSHoliday is a holiday of the period.
type ESSHoliday struct {
	Date string `json:"date" db:"d"`
	Name string `json:"name" db:"name"`
	Kind string `json:"kind" db:"kind"`
}

// AttendanceGeofenceRef is a geofence shown on the clock screen.
type AttendanceGeofenceRef struct {
	Name         string  `json:"name" db:"name"`
	Latitude     float64 `json:"latitude" db:"latitude"`
	Longitude    float64 `json:"longitude" db:"longitude"`
	RadiusMeters int     `json:"radiusMeters" db:"radius_meters"`
}

// AttendanceClockStatus is the state of the Clock In / Out screen.
type AttendanceClockStatus struct {
	Today         string                     `json:"today"`
	Shift         *ShiftAssignmentView       `json:"shift"`
	Day           hris.AttendanceDaySnapshot `json:"day"`
	LastEvent     *AttendanceEventView       `json:"lastEvent"`
	NextDirection string                     `json:"nextDirection" enum:"in,out"`
	MobileGPS     bool                       `json:"mobileGps" doc:"The employee may clock in with mobile GPS"`
	Geofences     []AttendanceGeofenceRef    `json:"geofences"`
	PINSet        bool                       `json:"pinSet"`
	ServerTime    time.Time                  `json:"serverTime"`
}

// MyAttendance is the attendance history of the employee.
type MyAttendance struct {
	Days   []AttendanceDayView   `json:"days"`
	Events []AttendanceEventView `json:"events"`
}

// AttendanceQR is the personal QR of the kiosk.
type AttendanceQR struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// AttendancePINResult confirms a PIN change.
type AttendancePINResult struct {
	PINSet bool `json:"pinSet"`
}

// TimePendingApproval is a request waiting for the manager (FR-ESS-05).
type TimePendingApproval struct {
	Kind         string    `json:"kind" enum:"leave,permission,overtime,shift_swap,attendance_correction" db:"kind"`
	RequestID    uuid.UUID `json:"requestId" db:"request_id"`
	Number       string    `json:"number" db:"number"`
	EmployeeID   uuid.UUID `json:"employeeId" db:"employee_id"`
	EmployeeName string    `json:"employeeName" db:"employee_name"`
	Level        string    `json:"level" db:"level"`
	Direct       bool      `json:"direct" db:"direct" doc:"I am the approver of the step (else a manager above)"`
	SubmittedAt  time.Time `json:"submittedAt" db:"created_at"`
	Summary      string    `json:"summary" db:"-"`
	Status       string    `json:"status" db:"-" doc:"Request status after the decision (submitted while further levels wait)"`
}

// TeamAttendance is the attendance of the manager's team on a day.
type TeamAttendance struct {
	Date          string                `json:"date"`
	Days          []AttendanceDayView   `json:"days"`
	PendingReview []AttendanceEventView `json:"pendingReview"`
	Exceptions    []OvertimeException   `json:"exceptions"`
}

func (m *Module) registerESS(reg *route.Registry) {
	tag := "Employee Self Service"
	ess := func(rt route.Route) {
		if rt.Permission == "" {
			rt.Permission = hris.PermissionESS
		}
		add(reg, tag, rt)
	}
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/schedule", Summary: "My schedule (published shifts, swaps, colleagues)", Response: MyShiftSchedule{},
		Query: []route.Param{{Name: "from"}, {Name: "to"}}, Handler: handle.Read(m.DB, m.essScheduleHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/clock-status", Summary: "My shift and attendance of today", Response: AttendanceClockStatus{},
		Handler: handle.Read(m.DB, m.clockStatusHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/attendance:clock", Summary: "Clock in / out with mobile GPS", Request: AttendanceClockRequest{},
		Response: AttendanceClockResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essClockHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/attendance", Summary: "My attendance history", Response: MyAttendance{},
		Query: []route.Param{{Name: "from"}, {Name: "to"}}, Handler: handle.Read(m.DB, m.essAttendanceHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/attendance-qr", Summary: "My personal QR for the Attendance Kiosk (valid 2 minutes)",
		Response: AttendanceQR{}, Handler: handle.Read(m.DB, m.essQRHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/attendance-pin", Summary: "Set my attendance PIN for the kiosk", Request: AttendancePINRequest{},
		Response: AttendancePINResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essPINHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/approvals", Summary: "Requests of my team waiting for me", Permission: hris.PermissionTeamApprove,
		Response: TimePendingApproval{}, List: true, Handler: listRead(m.DB, m.essApprovalsHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/approvals/{kind}/{id}:approve", Summary: "Approve a request of my team",
		Permission: hris.PermissionTeamApprove, Request: TimeDecision{}, Response: TimePendingApproval{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.essDecideHTTP(true))})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/approvals/{kind}/{id}:reject", Summary: "Reject a request of my team",
		Permission: hris.PermissionTeamApprove, Request: TimeDecision{}, Response: TimePendingApproval{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.essDecideHTTP(false))})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team-schedule", Summary: "Schedule of my team", Permission: hris.PermissionTeam,
		Response: MyShiftSchedule{}, Query: []route.Param{{Name: "from"}, {Name: "to"}}, Handler: handle.Read(m.DB, m.teamScheduleHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team-attendance", Summary: "Attendance of my team on a day", Permission: hris.PermissionTeam,
		Response: TeamAttendance{}, Query: []route.Param{{Name: "date"}}, Handler: handle.Read(m.DB, m.teamAttendanceHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/attendance-events/{id}:review", Summary: "Accept or reject a flagged clock-in of my team",
		Permission: hris.PermissionTeamApprove, Request: AttendanceReviewRequest{}, Response: AttendanceEventView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reviewHTTP(false))})
}

func period(r *http.Request, def time.Time, days int) (time.Time, time.Time, error) {
	from, err := handle.QueryDate(r, "from", def)
	if err != nil {
		return from, from, err
	}
	to, err := handle.QueryDate(r, "to", from.AddDate(0, 0, days))
	if err != nil {
		return from, to, err
	}
	if to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		return from, to, errs.BadRequest("invalid_period", "a period of at most 63 days")
	}
	return from, to, nil
}

func (m *Module) scheduleOf(ctx context.Context, tx pgx.Tx, property uuid.UUID, ids []uuid.UUID, from, to time.Time) (MyShiftSchedule, error) {
	out := MyShiftSchedule{From: ymd(from), To: ymd(to), Swaps: []ShiftSwap{}, Colleagues: []ESSColleague{}, CachedAt: clock.Now()}
	var err error
	if out.Assignments, err = handle.List[ShiftAssignmentView](tx.Query(ctx, assignmentSelect+` WHERE a.employee_id = ANY ($1) AND s.status = 'published'
		AND a.status <> 'cancelled' AND a.work_date BETWEEN $2::date AND $3::date ORDER BY a.work_date, e.full_name`, ids, ymd(from), ymd(to))); err != nil {
		return out, err
	}
	if out.Leave, err = handle.List[LeaveCalendarEntry](tx.Query(ctx, calendarSQL+` WHERE r.employee_id = ANY ($1) AND r.status IN ('submitted', 'approved')
		AND r.start_date <= $3::date AND r.end_date >= $2::date ORDER BY r.start_date`, ids, ymd(from), ymd(to))); err != nil {
		return out, err
	}
	out.Holidays, err = handle.List[ESSHoliday](tx.Query(ctx, `SELECT to_char(holiday_date, 'YYYY-MM-DD') AS d, name, kind FROM hris.holidays
		WHERE property_id = $1 AND holiday_date BETWEEN $2::date AND $3::date AND status = 'active' AND archived_at IS NULL
		UNION SELECT to_char(day, 'YYYY-MM-DD'), name, 'public_holiday' FROM platform.calendar_days WHERE kind = 'public_holiday' AND status = 'active'
		  AND day BETWEEN $2::date AND $3::date AND (property_id IS NULL OR property_id = $1) ORDER BY 1`, property, ymd(from), ymd(to)))
	return out, err
}

func (m *Module) essScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (MyShiftSchedule, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return MyShiftSchedule{}, err
	}
	tday := today(ctx, tx, e.PropertyID)
	from, to, err := period(r, tday.AddDate(0, 0, -int(isoWeekday(tday)-1)), 13)
	if err != nil {
		return MyShiftSchedule{}, err
	}
	out, err := m.scheduleOf(ctx, tx, e.PropertyID, []uuid.UUID{e.ID}, from, to)
	if err != nil {
		return out, err
	}
	if out.Swaps, err = handle.List[ShiftSwap](tx.Query(ctx, swapSelect+` WHERE (s.requester_employee_id = $1 OR s.counterpart_employee_id = $1)
		AND (s.status IN ('requested', 'submitted') OR s.created_at > now() - interval '14 days') ORDER BY s.created_at DESC`, e.ID)); err != nil {
		return out, err
	}
	for i := range out.Swaps {
		out.Swaps[i].Approvals = []TimeApprovalStep{}
	}
	if e.OrgUnitID != nil {
		cols, err := employeesQuery(ctx, tx, hris.EmployeeSelect+` WHERE e.org_unit_id = $1 AND e.id <> $2 AND e.status = 'active' AND e.archived_at IS NULL
			ORDER BY e.full_name`, *e.OrgUnitID, e.ID)
		if err != nil {
			return out, err
		}
		ids := empIDs(cols)
		shifts, err := handle.List[ShiftAssignmentView](tx.Query(ctx, assignmentSelect+` WHERE a.employee_id = ANY ($1) AND s.status = 'published'
			AND a.status = 'scheduled' AND a.work_date BETWEEN $2::date AND $3::date ORDER BY a.work_date`, ids, ymd(maxTime(from, tday)), ymd(to)))
		if err != nil {
			return out, err
		}
		for _, c := range cols {
			ec := ESSColleague{ID: c.ID, EmployeeNo: c.EmployeeNo, FullName: c.FullName, Position: c.PositionName, Assignments: []ShiftAssignmentView{}}
			for _, a := range shifts {
				if a.EmployeeID == c.ID {
					ec.Assignments = append(ec.Assignments, a)
				}
			}
			out.Colleagues = append(out.Colleagues, ec)
		}
	}
	return out, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func isoWeekday(t time.Time) int {
	w := int(t.Weekday())
	if w == 0 {
		return 7
	}
	return w
}

func (m *Module) clockStatusHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) (AttendanceClockStatus, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return AttendanceClockStatus{}, err
	}
	now := clock.Now()
	loc := location(ctx, tx, e.PropertyID)
	tday := dateOf(now, loc)
	out := AttendanceClockStatus{Today: ymd(tday), ServerTime: now, Geofences: []AttendanceGeofenceRef{}}
	// a night shift of yesterday still running counts as today's shift
	day, err := workDateFor(ctx, tx, e, now, "out", loc)
	if err != nil {
		return out, err
	}
	shifts, err := handle.List[ShiftAssignmentView](tx.Query(ctx, assignmentSelect+` WHERE a.employee_id = $1 AND a.work_date = $2::date
		AND s.status = 'published' AND a.status <> 'cancelled'`, e.ID, ymd(day)))
	if err != nil {
		return out, err
	}
	if len(shifts) == 0 && !day.Equal(tday) {
		day = tday
		if shifts, err = handle.List[ShiftAssignmentView](tx.Query(ctx, assignmentSelect+` WHERE a.employee_id = $1 AND a.work_date = $2::date
			AND s.status = 'published' AND a.status <> 'cancelled'`, e.ID, ymd(day))); err != nil {
			return out, err
		}
	}
	if len(shifts) > 0 {
		out.Shift = &shifts[0]
	}
	if out.Day, err = snapshotOf(ctx, tx, e.ID, day); err != nil {
		return out, err
	}
	evs, err := handle.List[AttendanceEventView](tx.Query(ctx, eventSelect+` WHERE v.employee_id = $1 AND v.voided_at IS NULL ORDER BY v.occurred_at DESC LIMIT 1`, e.ID))
	if err != nil {
		return out, err
	}
	if len(evs) > 0 {
		out.LastEvent = &evs[0]
	}
	last, err := lastDirection(ctx, tx, e.ID, now)
	if err != nil {
		return out, err
	}
	out.NextDirection = map[bool]string{true: "out", false: "in"}[last == "in"]
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, tx, e.PropertyID, now)
	if err != nil {
		return out, err
	}
	codes, err := orgUnitCodes(ctx, tx, e.OrgUnitID)
	if err != nil {
		return out, err
	}
	out.MobileGPS = slices.Contains(cfg.Methods, hris.MethodMobileGPS) &&
		(len(cfg.MobileGPSOrgUnits) == 0 || slices.ContainsFunc(codes, func(c string) bool { return slices.Contains(cfg.MobileGPSOrgUnits, c) }))
	if out.Geofences, err = handle.List[AttendanceGeofenceRef](tx.Query(ctx, `SELECT name, latitude::float8 AS latitude, longitude::float8 AS longitude, radius_meters
		FROM hris.geofences WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND (cardinality(org_unit_codes) = 0
		OR org_unit_codes && $2::text[]) ORDER BY name`, e.PropertyID, nonNilStrings(codes))); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT coalesce((SELECT pin_hash IS NOT NULL FROM hris.attendance_profiles WHERE employee_id = $1), false)`, e.ID).Scan(&out.PINSet)
	return out, err
}

// essClock records a mobile GPS clock-in / out of the signed-in employee.
func (m *Module) essClock(ctx context.Context, tx pgx.Tx, req AttendanceClockRequest) (clockOutcome, hris.Employee, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return clockOutcome{}, e, err
	}
	at := clock.Now()
	if req.Offline {
		if at, err = parseInstant("occurredAt", req.OccurredAt); err != nil {
			return clockOutcome{}, e, err
		}
	}
	o, err := m.recordEvent(ctx, tx, clockInput{Emp: e, Direction: req.Direction, At: at, Method: hris.MethodMobileGPS, Source: "ess",
		Lat: req.Latitude, Lng: req.Longitude, Acc: req.AccuracyMeters, ClientEventID: req.ClientEventID, Offline: req.Offline})
	return o, e, err
}

func (m *Module) essClockHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceClockRequest) (AttendanceClockResult, error) {
	o, e, err := m.essClock(ctx, tx, req)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	action := "clock"
	if o.Duplicate {
		action = "clock_duplicate"
	}
	return o.result(), audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: action, EntityType: "hris.attendance_event",
		EntityID: o.Event.ID.String(), EntityLabel: e.EmployeeNo + " · " + o.Event.Direction + " " + ymd(o.Event.WorkDate), PropertyID: &e.PropertyID,
		After: map[string]any{"method": o.Event.Method, "flags": o.Event.Flags, "reviewStatus": o.Event.ReviewStatus, "offline": req.Offline}})
}

func (m *Module) essAttendanceHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (MyAttendance, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return MyAttendance{}, err
	}
	tday := today(ctx, tx, e.PropertyID)
	from, to, err := period(r, tday.AddDate(0, 0, -30), 30)
	if err != nil {
		return MyAttendance{}, err
	}
	out := MyAttendance{}
	if out.Days, err = handle.List[AttendanceDayView](tx.Query(ctx, daysSQL, e.PropertyID, ymd(from), ymd(to), []uuid.UUID{e.ID}, nil, "")); err != nil {
		return out, err
	}
	out.Events, err = handle.List[AttendanceEventView](tx.Query(ctx, eventSelect+` WHERE v.employee_id = $1 AND v.work_date BETWEEN $2::date AND $3::date
		ORDER BY v.occurred_at DESC LIMIT 500`, e.ID, ymd(from), ymd(to)))
	return out, err
}

func (m *Module) essQRHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) (AttendanceQR, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return AttendanceQR{}, err
	}
	tok, exp := m.QRToken(e.ID, clock.Now())
	return AttendanceQR{Token: tok, ExpiresAt: exp}, nil
}

func (m *Module) essPINHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendancePINRequest) (AttendancePINResult, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return AttendancePINResult{}, err
	}
	var has bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT pin_hash IS NOT NULL FROM hris.attendance_profiles WHERE employee_id = $1), false)`, e.ID).
		Scan(&has); err != nil {
		return AttendancePINResult{}, err
	}
	if has {
		if err := m.verifyPIN(ctx, tx, e, req.CurrentPIN); err != nil {
			return AttendancePINResult{}, handle.Invalid("currentPin", "invalid", "the current PIN is wrong")
		}
	}
	if err := setPIN(ctx, tx, e, req.PIN); err != nil {
		return AttendancePINResult{}, err
	}
	return AttendancePINResult{PINSet: true}, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "set_pin", Category: audit.CategorySecurity,
		EntityType: "hris.attendance_profile", EntityID: e.ID.String(), EntityLabel: e.EmployeeNo, PropertyID: &e.PropertyID})
}

// pendingSQL lists the pending manager steps of a set of employees.
const pendingSQL = `SELECT a.request_kind AS kind, a.request_id, coalesce(lr.number, pr.number, orq.number, sw.number, co.number) AS number,
	e.id AS employee_id, e.full_name AS employee_name, a.level, coalesce(a.approver_employee_id = $2, false) AS direct, a.created_at
	FROM hris.request_approvals a
	LEFT JOIN hris.leave_requests lr ON a.request_kind = 'leave' AND lr.id = a.request_id
	LEFT JOIN hris.permission_requests pr ON a.request_kind = 'permission' AND pr.id = a.request_id
	LEFT JOIN hris.overtime_requests orq ON a.request_kind = 'overtime' AND orq.id = a.request_id
	LEFT JOIN hris.shift_swaps sw ON a.request_kind = 'shift_swap' AND sw.id = a.request_id
	LEFT JOIN hris.attendance_corrections co ON a.request_kind = 'attendance_correction' AND co.id = a.request_id
	JOIN hris.employees e ON e.id = coalesce(lr.employee_id, pr.employee_id, orq.employee_id, sw.requester_employee_id, co.employee_id)
	WHERE a.status = 'pending' AND a.level IN ('supervisor', 'department_head') AND (a.approver_employee_id = $2 OR e.id = ANY ($1))
	ORDER BY a.created_at`

func (m *Module) essApprovalsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]TimePendingApproval, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[TimePendingApproval](tx.Query(ctx, pendingSQL, empIDs(team), e.ID))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Summary, _, err = m.describe(ctx, tx, list[i].Kind, list[i].RequestID); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (m *Module) essDecideHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (TimePendingApproval, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (TimePendingApproval, error) {
		kind := chi.URLParam(r, "kind")
		s, err := spec(kind)
		if err != nil {
			return TimePendingApproval{}, err
		}
		rid, err := handle.ID(r)
		if err != nil {
			return TimePendingApproval{}, err
		}
		h, err := loadHeader(ctx, tx, s, rid, false)
		if err != nil {
			return TimePendingApproval{}, err
		}
		var level string
		_ = tx.QueryRow(ctx, `SELECT level FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2 AND status = 'pending'
			ORDER BY step_no LIMIT 1`, kind, rid).Scan(&level)
		if err := m.decide(ctx, tx, kind, rid, approve, req.Note, false); err != nil {
			return TimePendingApproval{}, err
		}
		after, err := loadHeader(ctx, tx, s, rid, false)
		if err != nil {
			return TimePendingApproval{}, err
		}
		emp, err := hris.EmployeeByID(ctx, tx, h.EmployeeID)
		if err != nil {
			return TimePendingApproval{}, err
		}
		summary, _, err := m.describe(ctx, tx, kind, rid)
		if err != nil {
			return TimePendingApproval{}, err
		}
		out := TimePendingApproval{Kind: kind, RequestID: rid, Number: h.Number, EmployeeID: emp.ID, EmployeeName: emp.FullName, Level: level,
			SubmittedAt: clock.Now(), Summary: summary, Status: after.Status}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris." + kind, EntityID: rid.String(), EntityLabel: h.Number + " · " + emp.FullName, PropertyID: &h.PropertyID, Reason: req.Note,
			Before: map[string]any{"status": h.Status}, After: map[string]any{"status": after.Status}})
	}
}

func (m *Module) teamScheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (MyShiftSchedule, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return MyShiftSchedule{}, err
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return MyShiftSchedule{}, err
	}
	tday := today(ctx, tx, e.PropertyID)
	from, to, err := period(r, tday.AddDate(0, 0, -int(isoWeekday(tday)-1)), 6)
	if err != nil {
		return MyShiftSchedule{}, err
	}
	out, err := m.scheduleOf(ctx, tx, e.PropertyID, empIDs(team), from, to)
	if err != nil {
		return out, err
	}
	for _, t := range team {
		out.Colleagues = append(out.Colleagues, ESSColleague{ID: t.ID, EmployeeNo: t.EmployeeNo, FullName: t.FullName, Position: t.PositionName,
			Assignments: []ShiftAssignmentView{}})
	}
	return out, nil
}

func (m *Module) teamAttendanceHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (TeamAttendance, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return TeamAttendance{}, err
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return TeamAttendance{}, err
	}
	day, err := handle.QueryDate(r, "date", today(ctx, tx, e.PropertyID))
	if err != nil {
		return TeamAttendance{}, err
	}
	ids := empIDs(team)
	out := TeamAttendance{Date: ymd(day), Days: []AttendanceDayView{}}
	if len(ids) == 0 {
		out.PendingReview, out.Exceptions = []AttendanceEventView{}, []OvertimeException{}
		return out, nil
	}
	if out.Days, err = handle.List[AttendanceDayView](tx.Query(ctx, daysSQL, e.PropertyID, ymd(day), ymd(day), ids, nil, "")); err != nil {
		return out, err
	}
	if out.PendingReview, err = handle.List[AttendanceEventView](tx.Query(ctx, eventSelect+` WHERE v.employee_id = ANY ($1) AND v.review_status = 'pending'
		AND v.voided_at IS NULL ORDER BY v.occurred_at DESC LIMIT 200`, ids)); err != nil {
		return out, err
	}
	out.Exceptions, err = handle.List[OvertimeException](tx.Query(ctx, exceptionSQL+` AND d.employee_id = ANY ($1) AND d.work_date BETWEEN $2::date - 14 AND $2::date
		ORDER BY d.work_date DESC`, ids, ymd(day)))
	return out, err
}
