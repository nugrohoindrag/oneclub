package hrtime

// Attendance (EP-07): clock-in / out from ESS with mobile GPS and geofence
// (FR-ATT-05: out of the area → flagged for the supervisor's review or
// refused per Attendance Policy), the Attendance Kiosk on a registered ops
// device with personal QR / PIN as non-biometric alternative (FR-ATT-02),
// HR records on behalf, offline events synchronised idempotently by their
// client event id (FR-ATT-07); every accepted event re-evaluates the day
// (FR-ATT-03) and publishes hris.attendance_recorded.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/iam/password"
)

// AttendanceEventView is a clock event.
type AttendanceEventView struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	EmployeeID     uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo     string     `json:"employeeNo" db:"employee_no"`
	EmployeeName   string     `json:"employeeName" db:"employee_name"`
	WorkDate       time.Time  `json:"workDate" db:"work_date"`
	Direction      string     `json:"direction" db:"direction" enum:"in,out"`
	OccurredAt     time.Time  `json:"occurredAt" db:"occurred_at"`
	Method         string     `json:"method" db:"method"`
	Source         string     `json:"source" db:"source" enum:"ess,kiosk,device,hr,correction"`
	DeviceName     *string    `json:"deviceName" db:"device_name"`
	AccuracyMeters *float64   `json:"accuracyMeters" db:"accuracy_meters"`
	DistanceMeters *float64   `json:"distanceMeters" db:"distance_meters"`
	GeofenceName   *string    `json:"geofenceName" db:"geofence_name"`
	WithinGeofence *bool      `json:"withinGeofence" db:"within_geofence"`
	Flags          []string   `json:"flags" db:"flags"`
	ReviewStatus   string     `json:"reviewStatus" db:"review_status" enum:"not_required,pending,accepted,rejected"`
	ReviewNote     *string    `json:"reviewNote" db:"review_note"`
	Offline        bool       `json:"offline" db:"offline"`
	Voided         bool       `json:"voided" db:"voided"`
	Note           *string    `json:"note" db:"note"`
	ReceivedAt     time.Time  `json:"receivedAt" db:"received_at"`
	CorrectionID   *uuid.UUID `json:"correctionId" db:"correction_id"`
}

const eventSelect = `SELECT v.id, v.employee_id, e.employee_no, e.full_name AS employee_name, v.work_date, v.direction, v.occurred_at, v.method, v.source,
	d.name AS device_name, v.accuracy_meters::float8, v.distance_meters::float8, g.name AS geofence_name, v.within_geofence, v.flags, v.review_status,
	v.review_note, v.offline, v.voided_at IS NOT NULL AS voided, v.note, v.received_at, v.correction_id
	FROM hris.attendance_events v JOIN hris.employees e ON e.id = v.employee_id LEFT JOIN hris.attendance_devices d ON d.id = v.device_id
	LEFT JOIN hris.geofences g ON g.id = v.geofence_id`

// AttendanceDayView is an attendance day of an employee.
type AttendanceDayView struct {
	EmployeeID        uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo        string     `json:"employeeNo" db:"employee_no"`
	EmployeeName      string     `json:"employeeName" db:"employee_name"`
	OrgUnitName       *string    `json:"orgUnitName" db:"org_unit_name"`
	WorkDate          time.Time  `json:"workDate" db:"work_date"`
	ShiftCode         *string    `json:"shiftCode" db:"shift_code"`
	ShiftName         *string    `json:"shiftName" db:"shift_name"`
	ScheduledStart    *time.Time `json:"scheduledStart" db:"scheduled_start"`
	ScheduledEnd      *time.Time `json:"scheduledEnd" db:"scheduled_end"`
	FirstIn           *time.Time `json:"firstIn" db:"first_in"`
	LastOut           *time.Time `json:"lastOut" db:"last_out"`
	WorkedMinutes     int        `json:"workedMinutes" db:"worked_minutes"`
	LateMinutes       int        `json:"lateMinutes" db:"late_minutes"`
	EarlyLeaveMinutes int        `json:"earlyLeaveMinutes" db:"early_leave_minutes"`
	OvertimeMinutes   int        `json:"overtimeMinutes" db:"overtime_minutes"`
	Status            string     `json:"status" db:"status" enum:"scheduled,present,late,early_leave,absent,on_leave,off,holiday"`
	DayKind           string     `json:"dayKind" db:"day_kind" enum:"workday,rest_day,shortest_day"`
	HolidayName       *string    `json:"holidayName" db:"holiday_name"`
	LeaveType         *string    `json:"leaveType" db:"leave_type"`
	Flags             []string   `json:"flags" db:"flags"`
	Finalized         bool       `json:"finalized" db:"finalized"`
	Locked            bool       `json:"locked" db:"locked"`
}

// daysSQL lists the attendance days of a period: computed days plus
// published assignments not evaluated yet (Scheduled / Off). $1 property,
// $2 from, $3 to, $4 employee ids (empty = all), $5 org unit (subtree),
// $6 status.
const daysSQL = `WITH base AS (
	  SELECT a.employee_id, a.work_date FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id AND s.status = 'published'
	  WHERE a.property_id = $1 AND a.work_date BETWEEN $2::date AND $3::date AND a.status <> 'cancelled'
	  UNION SELECT employee_id, work_date FROM hris.attendance_days WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date)
	SELECT b.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, b.work_date, t.code AS shift_code, t.name AS shift_name,
	  coalesce(d.scheduled_start, a.starts_at) AS scheduled_start, coalesce(d.scheduled_end, a.ends_at) AS scheduled_end, d.first_in, d.last_out,
	  coalesce(d.worked_minutes, 0) AS worked_minutes, coalesce(d.late_minutes, 0) AS late_minutes, coalesce(d.early_leave_minutes, 0) AS early_leave_minutes,
	  coalesce(d.overtime_minutes, 0) AS overtime_minutes,
	  coalesce(d.status, CASE WHEN a.kind = 'off' THEN 'off' ELSE 'scheduled' END) AS status, coalesce(d.day_kind, 'workday') AS day_kind, d.holiday_name,
	  d.leave_type, coalesce(d.flags, '{}') AS flags, coalesce(d.finalized, false) AS finalized, coalesce(d.locked, false) AS locked
	FROM base b JOIN hris.employees e ON e.id = b.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
	LEFT JOIN hris.attendance_days d ON d.employee_id = b.employee_id AND d.work_date = b.work_date
	LEFT JOIN LATERAL (SELECT x.kind, x.starts_at, x.ends_at, x.shift_template_id FROM hris.shift_assignments x JOIN hris.schedules xs ON xs.id = x.schedule_id
	  AND xs.status = 'published' WHERE x.employee_id = b.employee_id AND x.work_date = b.work_date AND x.status <> 'cancelled' LIMIT 1) a ON true
	LEFT JOIN hris.shift_templates t ON t.id = coalesce(d.shift_template_id, a.shift_template_id)
	WHERE (cardinality($4::uuid[]) = 0 OR b.employee_id = ANY ($4))
	  AND ($5::uuid IS NULL OR e.org_unit_id IN (WITH RECURSIVE u AS (SELECT id FROM hris.org_units WHERE id = $5
	    UNION ALL SELECT c.id FROM hris.org_units c JOIN u ON c.parent_id = u.id) SELECT id FROM u))
	  AND ($6 = '' OR coalesce(d.status, CASE WHEN a.kind = 'off' THEN 'off' ELSE 'scheduled' END) = $6)
	ORDER BY b.work_date DESC, e.full_name LIMIT 2000`

// AttendanceClockRequest is a clock-in / out from Employee Self Service (mobile GPS).
type AttendanceClockRequest struct {
	Direction      string   `json:"direction,omitempty" enum:"in,out" doc:"Empty: in when the last event was an out"`
	Latitude       *float64 `json:"latitude,omitempty"`
	Longitude      *float64 `json:"longitude,omitempty"`
	AccuracyMeters *float64 `json:"accuracyMeters,omitempty"`
	ClientEventID  string   `json:"clientEventId,omitempty" doc:"Client UUID; a resubmission returns the recorded event (offline sync)"`
	OccurredAt     string   `json:"occurredAt,omitempty" doc:"RFC 3339; only for offline events (offline=true), else the server time"`
	Offline        bool     `json:"offline,omitempty"`
}

// HRClockRequest records attendance on behalf of an employee.
type HRClockRequest struct {
	EmployeeID uuid.UUID `json:"employeeId"`
	Direction  string    `json:"direction" enum:"in,out"`
	OccurredAt string    `json:"occurredAt" doc:"RFC 3339"`
	Note       string    `json:"note"`
}

// AttendanceKioskClockRequest is a clock-in / out at the Attendance Kiosk: the
// personal QR of ESS, or the employee number with the attendance PIN.
type AttendanceKioskClockRequest struct {
	QRToken       string `json:"qrToken,omitempty"`
	EmployeeNo    string `json:"employeeNo,omitempty"`
	PIN           string `json:"pin,omitempty"`
	Direction     string `json:"direction,omitempty" enum:"in,out"`
	ClientEventID string `json:"clientEventId,omitempty"`
	OccurredAt    string `json:"occurredAt,omitempty" doc:"RFC 3339; only for offline events"`
	Offline       bool   `json:"offline,omitempty"`
}

// AttendanceKioskSyncRequest sends the offline queue of a kiosk.
type AttendanceKioskSyncRequest struct {
	Events []AttendanceKioskClockRequest `json:"events"`
}

// AttendanceClockResult is the outcome of a clock-in / out.
type AttendanceClockResult struct {
	Event     AttendanceEventView        `json:"event"`
	Day       hris.AttendanceDaySnapshot `json:"day"`
	Duplicate bool                       `json:"duplicate" doc:"The event was already recorded (resubmission)"`
	Message   string                     `json:"message"`
}

// AttendanceSyncItemResult is one event of an offline batch.
type AttendanceSyncItemResult struct {
	ClientEventID string                 `json:"clientEventId"`
	Status        string                 `json:"status" enum:"accepted,duplicate,rejected"`
	Error         string                 `json:"error,omitempty"`
	Result        *AttendanceClockResult `json:"result,omitempty"`
}

// AttendanceKioskSyncResult is the outcome of an offline batch.
type AttendanceKioskSyncResult struct {
	Results []AttendanceSyncItemResult `json:"results"`
}

// AttendanceReviewRequest accepts or rejects a flagged clock event.
type AttendanceReviewRequest struct {
	Decision string `json:"decision" enum:"accepted,rejected"`
	Note     string `json:"note,omitempty"`
}

// AttendanceRecalculateRequest re-evaluates attendance days.
type AttendanceRecalculateRequest struct {
	From       string     `json:"from"`
	To         string     `json:"to"`
	EmployeeID *uuid.UUID `json:"employeeId,omitempty"`
}

// AttendanceRecalculateResult counts the evaluated days.
type AttendanceRecalculateResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

// AttendanceKioskInfo describes the kiosk of the signed-in device.
type AttendanceKioskInfo struct {
	DeviceID   uuid.UUID             `json:"deviceId"`
	Code       string                `json:"code"`
	Name       string                `json:"name"`
	Location   *string               `json:"location"`
	Methods    []string              `json:"methods"`
	ServerTime time.Time             `json:"serverTime"`
	Recent     []AttendanceEventView `json:"recent"`
}

func (m *Module) registerAttendance(reg *route.Registry) {
	tag := "HRIS Attendance"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/attendance-days", Summary: "Attendance days", Permission: PermAttendanceView,
		Response: AttendanceDayView{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "orgUnitId"}, {Name: "employeeId"},
			{Name: "status", Enum: hris.DayStatuses}}, Handler: listRead(m.DB, m.daysHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/attendance-events", Summary: "Clock events (review queue with review=pending)",
		Permission: PermAttendanceView, Response: AttendanceEventView{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}, {Name: "employeeId"},
			{Name: "review", Enum: []string{"pending", "accepted", "rejected"}}}, Handler: listRead(m.DB, m.eventsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance:clock", Summary: "Record a clock-in / out on behalf of an employee",
		Permission: PermAttendanceRecord, Request: HRClockRequest{}, Response: AttendanceClockResult{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.hrClockHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance:recalculate", Summary: "Re-evaluate attendance days of a period",
		Permission: PermAttendanceRecord, Request: AttendanceRecalculateRequest{}, Response: AttendanceRecalculateResult{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.recalculateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-events/{id}:review", Summary: "Accept or reject a flagged clock event",
		Permission: PermAttendanceReview, Request: AttendanceReviewRequest{}, Response: AttendanceEventView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reviewHTTP(true))})
	kiosk := "HRIS Attendance Kiosk"
	add(reg, kiosk, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/attendance/kiosk", Summary: "The Attendance Kiosk of this registered device",
		Permission: PermKiosk, Response: AttendanceKioskInfo{}, Handler: handle.Read(m.DB, m.kioskInfoHTTP)})
	add(reg, kiosk, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance/kiosk:clock", Summary: "Clock in / out at the kiosk (QR or PIN)",
		Permission: PermKiosk, Request: AttendanceKioskClockRequest{}, Response: AttendanceClockResult{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.kioskClockHTTP)})
	add(reg, kiosk, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance/kiosk:sync", Summary: "Send the offline queue of the kiosk (idempotent)",
		Permission: PermKiosk, Request: AttendanceKioskSyncRequest{}, Response: AttendanceKioskSyncResult{}, Status: http.StatusOK, Handler: m.kioskSyncHTTP})
}

// ── listing ──────────────────────────────────────────────────────────────

func (m *Module) daysHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]AttendanceDayView, error) {
	property := handle.Property(ctx)
	to, err := handle.QueryDate(r, "to", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	from, err := handle.QueryDate(r, "from", to.AddDate(0, 0, -6))
	if err != nil {
		return nil, err
	}
	unit, err := handle.QueryUUID(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	if emp != nil {
		ids = append(ids, *emp)
	}
	return handle.List[AttendanceDayView](tx.Query(ctx, daysSQL, property, ymd(from), ymd(to), ids, unit, r.URL.Query().Get("status")))
}

func (m *Module) eventsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]AttendanceEventView, error) {
	property := handle.Property(ctx)
	to, err := handle.QueryDate(r, "to", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	from, err := handle.QueryDate(r, "from", to.AddDate(0, 0, -13))
	if err != nil {
		return nil, err
	}
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	return handle.List[AttendanceEventView](tx.Query(ctx, eventSelect+` WHERE v.property_id = $1 AND v.work_date BETWEEN $2::date AND $3::date
		AND ($4::uuid IS NULL OR v.employee_id = $4) AND ($5 = '' OR v.review_status = $5) ORDER BY v.occurred_at DESC LIMIT 1000`,
		property, ymd(from), ymd(to), emp, r.URL.Query().Get("review")))
}

// ── recording ────────────────────────────────────────────────────────────

type clockInput struct {
	Emp           hris.Employee
	Direction     string
	At            time.Time
	Method        string
	Source        string
	DeviceID      *uuid.UUID
	Lat, Lng, Acc *float64
	ClientEventID string
	Offline       bool
	Note          string
	CorrectionID  *uuid.UUID
	// WorkDate overrides the work date (corrections).
	WorkDate *time.Time
	// Trusted skips method and geofence rules (HR, corrections).
	Trusted bool
}

type clockOutcome struct {
	Event     AttendanceEventView
	Day       hris.AttendanceDaySnapshot
	Duplicate bool
}

func (o clockOutcome) result() AttendanceClockResult {
	msg := "Clocked " + o.Event.Direction + " at " + o.Event.OccurredAt.UTC().Format(time.RFC3339)
	if o.Duplicate {
		msg = "Already recorded"
	} else if o.Event.ReviewStatus == "pending" {
		msg += " — waiting for your supervisor's review (" + strings.ReplaceAll(strings.Join(o.Event.Flags, ", "), "_", " ") + ")"
	}
	return AttendanceClockResult{Event: o.Event, Day: o.Day, Duplicate: o.Duplicate, Message: msg}
}

// orgUnitCodes are the codes of the employee's unit and its parents.
func orgUnitCodes(ctx context.Context, q dbtx.Querier, unit *uuid.UUID) ([]string, error) {
	if unit == nil {
		return nil, nil
	}
	rows, err := q.Query(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, code, 0 AS depth FROM hris.org_units WHERE id = $1
		UNION ALL SELECT p.id, p.parent_id, p.code, up.depth + 1 FROM hris.org_units p JOIN up ON p.id = up.parent_id WHERE up.depth < 20)
		SELECT code FROM up ORDER BY depth`, *unit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// workDateFor assigns an event to a work date: a clock-out (or a late
// clock-in) after midnight belongs to the night shift of the day before.
func workDateFor(ctx context.Context, q dbtx.Querier, emp hris.Employee, at time.Time, direction string, loc *time.Location) (time.Time, error) {
	d := dateOf(at, loc)
	prev := d.AddDate(0, 0, -1)
	pa, err := loadAssignment(ctx, q, emp.ID, prev)
	if err != nil {
		return d, err
	}
	if pa != nil && pa.Kind == "shift" && pa.EndsAt != nil && dateOf(*pa.EndsAt, loc).After(prev) && !at.After(pa.EndsAt.Add(6*time.Hour)) {
		if direction == "out" || at.Before(*pa.EndsAt) {
			return prev, nil
		}
	}
	return d, nil
}

// lastDirection is the direction of the employee's last counted event.
func lastDirection(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, before time.Time) (string, error) {
	var dir string
	err := q.QueryRow(ctx, `SELECT direction FROM hris.attendance_events WHERE employee_id = $1 AND voided_at IS NULL AND review_status <> 'rejected'
		AND occurred_at <= $2 AND occurred_at > $2 - interval '20 hours' ORDER BY occurred_at DESC LIMIT 1`, employeeID, before).Scan(&dir)
	if dbtx.IsNoRows(err) {
		return "", nil
	}
	return dir, err
}

func (m *Module) loadEvent(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (AttendanceEventView, error) {
	return getOne[AttendanceEventView]("attendance event")(q.Query(ctx, eventSelect+` WHERE v.id = $1`, eid))
}

// recordEvent validates and stores a clock event, re-evaluates the day and
// publishes hris.attendance_recorded. A known client event id returns the
// recorded event (Duplicate).
func (m *Module) recordEvent(ctx context.Context, tx pgx.Tx, in clockInput) (clockOutcome, error) {
	emp := in.Emp
	if in.ClientEventID != "" {
		var eid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM hris.attendance_events WHERE employee_id = $1 AND client_event_id = $2`, emp.ID, in.ClientEventID).Scan(&eid)
		if err == nil {
			ev, err := m.loadEvent(ctx, tx, eid)
			if err != nil {
				return clockOutcome{}, err
			}
			day, err := snapshotOf(ctx, tx, emp.ID, ev.WorkDate)
			return clockOutcome{Event: ev, Day: day, Duplicate: true}, err
		}
		if !dbtx.IsNoRows(err) {
			return clockOutcome{}, err
		}
	}
	now := clock.Now()
	if in.At.After(now.Add(5 * time.Minute)) {
		return clockOutcome{}, handle.Invalid("occurredAt", "in_future", "the clock time is in the future")
	}
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, tx, emp.PropertyID, now)
	if err != nil {
		return clockOutcome{}, err
	}
	if in.Offline && cfg.OfflineSyncMaxHours > 0 && now.Sub(in.At) > time.Duration(cfg.OfflineSyncMaxHours)*time.Hour {
		return clockOutcome{}, errs.Conflict("offline_too_old",
			fmt.Sprintf("offline clock events older than %d hours are not accepted; request an attendance correction", cfg.OfflineSyncMaxHours))
	}
	loc := location(ctx, tx, emp.PropertyID)
	if in.Direction == "" {
		last, err := lastDirection(ctx, tx, emp.ID, in.At)
		if err != nil {
			return clockOutcome{}, err
		}
		in.Direction = map[bool]string{true: "out", false: "in"}[last == "in"]
	}
	if in.Direction != "in" && in.Direction != "out" {
		return clockOutcome{}, enumErr("direction", []string{"in", "out"})
	}
	day, err := workDateFor(ctx, tx, emp, in.At, in.Direction, loc)
	if err != nil {
		return clockOutcome{}, err
	}
	if in.WorkDate != nil {
		day = *in.WorkDate
	}
	if !emp.EmployedOn(day) {
		return clockOutcome{}, errs.Conflict("not_employed", emp.FullName+" is not employed on "+ymd(day))
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
		return clockOutcome{}, err
	}
	ap, _, err := hris.LoadAttendancePolicy(ctx, tx, emp.PropertyID, now)
	if err != nil {
		return clockOutcome{}, err
	}
	flags := []string{}
	review := "not_required"
	var dist *float64
	var geofence *uuid.UUID
	var within *bool
	if in.Offline {
		flags = append(flags, hris.FlagOffline)
	}
	if !in.Trusted {
		codes, err := orgUnitCodes(ctx, tx, emp.OrgUnitID)
		if err != nil {
			return clockOutcome{}, err
		}
		if !slices.Contains(cfg.Methods, in.Method) {
			return clockOutcome{}, errs.Conflict("method_disabled", "clock-in by "+strings.ReplaceAll(in.Method, "_", " ")+" is not enabled (Attendance Configuration)")
		}
		for _, c := range codes {
			if allowed, ok := ap.MethodsByLocation[c]; ok && len(allowed) > 0 {
				if !slices.Contains(allowed, in.Method) {
					return clockOutcome{}, errs.Conflict("method_not_allowed", "clock-in by "+strings.ReplaceAll(in.Method, "_", " ")+" is not allowed for "+c+
						" (Attendance Policy)")
				}
				break
			}
		}
		if in.Method == hris.MethodMobileGPS {
			if len(cfg.MobileGPSOrgUnits) > 0 && !slices.ContainsFunc(codes, func(c string) bool { return slices.Contains(cfg.MobileGPSOrgUnits, c) }) {
				return clockOutcome{}, errs.Forbidden("mobile GPS clock-in is for field staff (" + strings.Join(cfg.MobileGPSOrgUnits, ", ") +
					"); clock in at the kiosk or a device")
			}
			if in.Lat == nil || in.Lng == nil || math.Abs(*in.Lat) > 90 || math.Abs(*in.Lng) > 180 {
				return clockOutcome{}, handle.Invalid("latitude", "required", "share your location to clock in")
			}
			g, d, radius, err := nearestGeofence(ctx, tx, emp.PropertyID, codes, *in.Lat, *in.Lng)
			if err != nil {
				return clockOutcome{}, err
			}
			if g != nil {
				geofence, dist = g, &d
				if radius <= 0 {
					radius = float64(ap.GeofenceRadiusMeters)
				}
				ok := d <= radius
				within = &ok
				if !ok {
					if ap.OutOfAreaAction == "reject" {
						return clockOutcome{}, errs.Conflict("out_of_area", fmt.Sprintf("you are %.0f m from the club area (allowed %.0f m)", d, radius))
					}
					flags = append(flags, hris.FlagOutOfArea)
					review = "pending"
				}
			}
			if in.Acc != nil && cfg.MinimumGPSAccuracyMeters > 0 && *in.Acc > float64(cfg.MinimumGPSAccuracyMeters) {
				flags = append(flags, hris.FlagLowAccuracy)
				review = "pending"
			}
		}
		if cfg.ShiftScheduleRequiredForClock {
			a, err := loadAssignment(ctx, tx, emp.ID, day)
			if err != nil {
				return clockOutcome{}, err
			}
			if a == nil || a.Kind != "shift" {
				flags = append(flags, hris.FlagNoSchedule)
				review = "pending"
			}
		}
	}
	eid := id.New()
	var client *string
	if in.ClientEventID != "" {
		client = &in.ClientEventID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_events (id, property_id, employee_id, work_date, direction, occurred_at, method, source, device_id,
		latitude, longitude, accuracy_meters, distance_meters, geofence_id, within_geofence, flags, review_status, client_event_id, offline, correction_id,
		note, recorded_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		eid, emp.PropertyID, emp.ID, ymd(day), in.Direction, in.At, in.Method, in.Source, in.DeviceID, in.Lat, in.Lng, in.Acc, dist, geofence, within, flags,
		review, client, in.Offline, in.CorrectionID, nullStr(in.Note), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return clockOutcome{}, errs.Conflict("duplicate_event", "the event was recorded concurrently; send it again")
		}
		return clockOutcome{}, err
	}
	if in.DeviceID != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_devices SET last_event_at = greatest(coalesce(last_event_at, $2), $2) WHERE id = $1`,
			*in.DeviceID, in.At); err != nil {
			return clockOutcome{}, err
		}
	}
	tday := dateOf(now, loc)
	snap, err := m.recomputeDay(ctx, tx, emp, day, day.Before(tday.AddDate(0, 0, -1)), nil)
	if err != nil {
		return clockOutcome{}, err
	}
	ev, err := m.loadEvent(ctx, tx, eid)
	if err != nil {
		return clockOutcome{}, err
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventAttendanceRecorded, "hris.attendance_event", &eid, &emp.PropertyID, hris.AttendanceRecorded{
		EventID: eid, PropertyID: emp.PropertyID, EmployeeID: emp.ID, EmployeeNo: emp.EmployeeNo, WorkDate: ymd(day), Direction: in.Direction,
		OccurredAt: in.At.UTC().Format(time.RFC3339), Method: in.Method, Source: in.Source, DeviceID: in.DeviceID, Offline: in.Offline, Flags: flags,
		ReviewStatus: review, Day: snap}); err != nil {
		return clockOutcome{}, err
	}
	if review == "pending" {
		if err := m.notifyReviewers(ctx, tx, emp, ev); err != nil {
			return clockOutcome{}, err
		}
	}
	return clockOutcome{Event: ev, Day: snap}, nil
}

// nearestGeofence returns the closest active geofence for the employee's
// org units (empty unit list = all units) with the distance and radius.
func nearestGeofence(ctx context.Context, q dbtx.Querier, property uuid.UUID, codes []string, lat, lng float64) (*uuid.UUID, float64, float64, error) {
	rows, err := q.Query(ctx, `SELECT id, latitude::float8, longitude::float8, radius_meters FROM hris.geofences WHERE property_id = $1 AND status = 'active'
		AND archived_at IS NULL AND (cardinality(org_unit_codes) = 0 OR org_unit_codes && $2::text[])`, property, nonNilStrings(codes))
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var best *uuid.UUID
	bestD, bestR := math.MaxFloat64, 0.0
	for rows.Next() {
		var gid uuid.UUID
		var glat, glng float64
		var radius int
		if err := rows.Scan(&gid, &glat, &glng, &radius); err != nil {
			return nil, 0, 0, err
		}
		d := hris.DistanceMeters(lat, lng, glat, glng)
		// inside any geofence wins; otherwise the nearest edge
		if best == nil || d-float64(radius) < bestD-bestR {
			g := gid
			best, bestD, bestR = &g, d, float64(radius)
		}
	}
	return best, math.Round(bestD*10) / 10, bestR, rows.Err()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// notifyReviewers tells the supervisor (else HR reviewers) about a flagged
// clock event (EP-07 AC: review queue of the supervisor).
func (m *Module) notifyReviewers(ctx context.Context, tx pgx.Tx, emp hris.Employee, ev AttendanceEventView) error {
	var users []uuid.UUID
	if sup, err := hris.Supervisor(ctx, tx, emp.ID); err != nil {
		return err
	} else if sup != nil {
		if u := userOf(ctx, tx, sup.ID); u != nil {
			users = append(users, *u)
		}
	}
	if len(users) == 0 {
		users = holders(ctx, tx, emp.PropertyID, PermAttendanceReview)
	}
	distance, geofence := "?", "the club"
	if ev.DistanceMeters != nil {
		distance = strconv.FormatFloat(*ev.DistanceMeters, 'f', 0, 64)
	}
	if ev.GeofenceName != nil {
		geofence = *ev.GeofenceName
	}
	return m.notifyUsers(ctx, tx, emp.PropertyID, users, NotifyOutOfArea, "/ops/ess/team-attendance", map[string]any{"employeeName": emp.FullName,
		"direction": ev.Direction, "time": ev.OccurredAt.In(location(ctx, tx, emp.PropertyID)).Format("02 Jan 15:04"), "distance": distance,
		"geofence": geofence, "reason": strings.ReplaceAll(strings.Join(ev.Flags, ", "), "_", " ")})
}

func (m *Module) hrClockHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req HRClockRequest) (AttendanceClockResult, error) {
	property := handle.Property(ctx)
	emp, err := employeeAt(ctx, tx, property, req.EmployeeID)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	at, err := parseInstant("occurredAt", req.OccurredAt)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	if strings.TrimSpace(req.Note) == "" {
		return AttendanceClockResult{}, handle.Invalid("note", "required", "explain why HR records this event")
	}
	if req.Direction != "in" && req.Direction != "out" {
		return AttendanceClockResult{}, enumErr("direction", []string{"in", "out"})
	}
	out, err := m.recordEvent(ctx, tx, clockInput{Emp: emp, Direction: req.Direction, At: at, Method: hris.MethodManual, Source: "hr", Note: req.Note,
		Trusted: true})
	if err != nil {
		return AttendanceClockResult{}, err
	}
	return out.result(), audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "record", EntityType: "hris.attendance_event",
		EntityID: out.Event.ID.String(), EntityLabel: emp.EmployeeNo + " · " + req.Direction + " " + ymd(out.Event.WorkDate), PropertyID: &property,
		Reason: req.Note, After: map[string]any{"direction": req.Direction, "occurredAt": at, "day": out.Day.Status}})
}

func (m *Module) recalculateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceRecalculateRequest) (AttendanceRecalculateResult, error) {
	property := handle.Property(ctx)
	from, err := mustDate("from", req.From)
	if err != nil {
		return AttendanceRecalculateResult{}, err
	}
	to, err := mustDate("to", req.To)
	if err != nil {
		return AttendanceRecalculateResult{}, err
	}
	if to.Before(from) || to.Sub(from) > 62*24*time.Hour {
		return AttendanceRecalculateResult{}, handle.Invalid("to", "invalid", "a period of at most 63 days")
	}
	var ids []uuid.UUID
	if req.EmployeeID != nil {
		if _, err := employeeAt(ctx, tx, property, *req.EmployeeID); err != nil {
			return AttendanceRecalculateResult{}, err
		}
		ids = []uuid.UUID{*req.EmployeeID}
	}
	if err := m.evaluateRange(ctx, tx, property, from, to, ids, true); err != nil {
		return AttendanceRecalculateResult{}, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hris.attendance_days WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date
		AND (cardinality($4::uuid[]) = 0 OR employee_id = ANY ($4))`, property, ymd(from), ymd(to), nonNilIDs(ids)).Scan(&n); err != nil {
		return AttendanceRecalculateResult{}, err
	}
	res := AttendanceRecalculateResult{From: ymd(from), To: ymd(to), Days: n}
	return res, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "recalculate", EntityType: "hris.attendance_day", EntityLabel: res.From + " – " + res.To,
		PropertyID: &property, After: res})
}

func nonNilIDs(s []uuid.UUID) []uuid.UUID {
	if s == nil {
		return []uuid.UUID{}
	}
	return s
}

// reviewHTTP accepts / rejects a flagged event: HR (viaHR) or the manager
// of the employee in ESS.
func (m *Module) reviewHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceReviewRequest) (AttendanceEventView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceReviewRequest) (AttendanceEventView, error) {
		eid, err := handle.ID(r)
		if err != nil {
			return AttendanceEventView{}, err
		}
		var property, employeeID uuid.UUID
		var status string
		var workDate time.Time
		if err := tx.QueryRow(ctx, `SELECT property_id, employee_id, review_status, work_date FROM hris.attendance_events WHERE id = $1 FOR UPDATE`, eid).
			Scan(&property, &employeeID, &status, &workDate); err != nil {
			if dbtx.IsNoRows(err) {
				return AttendanceEventView{}, errs.NotFound("attendance event")
			}
			return AttendanceEventView{}, err
		}
		if viaHR && property != handle.Property(ctx) {
			return AttendanceEventView{}, errs.NotFound("attendance event")
		}
		if !viaHR {
			mine, err := me(ctx, tx)
			if err != nil {
				return AttendanceEventView{}, err
			}
			ok, err := hris.IsManagerOf(ctx, tx, mine.ID, employeeID)
			if err != nil {
				return AttendanceEventView{}, err
			}
			if !ok {
				return AttendanceEventView{}, errs.Forbidden("only a manager of the employee reviews the event")
			}
		}
		if status != "pending" {
			return AttendanceEventView{}, statusErr("the event review", status)
		}
		if req.Decision != "accepted" && req.Decision != "rejected" {
			return AttendanceEventView{}, enumErr("decision", []string{"accepted", "rejected"})
		}
		if req.Decision == "rejected" && strings.TrimSpace(req.Note) == "" {
			return AttendanceEventView{}, handle.Invalid("note", "required", "a reason is required to reject")
		}
		if err := checkLocked(ctx, tx, property, workDate); err != nil {
			return AttendanceEventView{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_events SET review_status = $2, reviewed_by = $3, reviewed_at = now(), review_note = $4 WHERE id = $1`,
			eid, req.Decision, actor(ctx), nullStr(req.Note)); err != nil {
			return AttendanceEventView{}, err
		}
		emp, err := hris.EmployeeByID(ctx, tx, employeeID)
		if err != nil {
			return AttendanceEventView{}, err
		}
		tday := today(ctx, tx, property)
		if _, err := m.recomputeDay(ctx, tx, emp, workDate, workDate.Before(tday), nil); err != nil {
			return AttendanceEventView{}, err
		}
		ev, err := m.loadEvent(ctx, tx, eid)
		if err != nil {
			return ev, err
		}
		return ev, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "review", EntityType: "hris.attendance_event", EntityID: eid.String(),
			EntityLabel: emp.EmployeeNo + " · " + ev.Direction + " " + ymd(workDate), PropertyID: &property, Reason: req.Note,
			Before: map[string]any{"reviewStatus": status}, After: map[string]any{"reviewStatus": req.Decision}})
	}
}

// ── ESS QR & kiosk ───────────────────────────────────────────────────────

// qrValidity is how long a personal attendance QR is valid.
const qrValidity = 2 * time.Minute

func (m *Module) signQR(employeeID uuid.UUID, exp int64) string {
	mac := hmac.New(sha256.New, append([]byte("hris-attendance-qr:"), m.Secret...))
	mac.Write([]byte(employeeID.String() + "." + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// QRToken issues a personal attendance QR (ocq1.<employee>.<expiry>.<mac>).
func (m *Module) QRToken(employeeID uuid.UUID, now time.Time) (string, time.Time) {
	exp := now.Add(qrValidity)
	return "ocq1." + employeeID.String() + "." + strconv.FormatInt(exp.Unix(), 10) + "." + m.signQR(employeeID, exp.Unix()), exp
}

// parseQR verifies a personal attendance QR (valid until expiry; an offline
// kiosk event is checked against its own time).
func (m *Module) parseQR(token string, at time.Time) (uuid.UUID, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	bad := errs.Validation("invalid_qr", "the QR code is not a valid attendance QR", errs.Field("qrToken", "invalid", "not a valid attendance QR"))
	if len(parts) != 4 || parts[0] != "ocq1" {
		return uuid.Nil, bad
	}
	eid, err := uuid.Parse(parts[1])
	if err != nil {
		return uuid.Nil, bad
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !hmac.Equal([]byte(m.signQR(eid, exp)), []byte(parts[3])) {
		return uuid.Nil, bad
	}
	if at.Unix() > exp {
		return uuid.Nil, errs.Validation("qr_expired", "the QR code expired; refresh it in Employee Self Service",
			errs.Field("qrToken", "expired", "refresh the QR code"))
	}
	return eid, nil
}

var pinRe = regexp.MustCompile(`^\d{6}$`)

// kioskDevice returns the kiosk mapped to the signed-in registered device.
func kioskDevice(ctx context.Context, q dbtx.Querier, property uuid.UUID) (uuid.UUID, string, string, *string, []string, error) {
	p := authz.From(ctx)
	if p == nil || p.DeviceID == nil {
		return uuid.Nil, "", "", nil, nil, errs.Forbidden("the Attendance Kiosk runs on a registered device (device sign-in with PIN)")
	}
	var did uuid.UUID
	var code, name string
	var loc *string
	var methods []string
	err := q.QueryRow(ctx, `SELECT id, code, name, location_point, methods FROM hris.attendance_devices WHERE platform_device_id = $1 AND property_id = $2
		AND device_kind = 'kiosk' AND status = 'active' AND archived_at IS NULL`, *p.DeviceID, property).Scan(&did, &code, &name, &loc, &methods)
	if dbtx.IsNoRows(err) {
		return uuid.Nil, "", "", nil, nil, errs.Forbidden("this device is not registered as an Attendance Kiosk (HRIS → Attendance → Devices)")
	}
	return did, code, name, loc, methods, err
}

func (m *Module) kioskInfoHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) (AttendanceKioskInfo, error) {
	property := handle.Property(ctx)
	did, code, name, loc, methods, err := kioskDevice(ctx, tx, property)
	if err != nil {
		return AttendanceKioskInfo{}, err
	}
	recent, err := handle.List[AttendanceEventView](tx.Query(ctx, eventSelect+` WHERE v.device_id = $1 ORDER BY v.occurred_at DESC LIMIT 10`, did))
	return AttendanceKioskInfo{DeviceID: did, Code: code, Name: name, Location: loc, Methods: methods, ServerTime: clock.Now(), Recent: recent}, err
}

// kioskClock identifies the employee (QR or PIN) and records the event.
func (m *Module) kioskClock(ctx context.Context, tx pgx.Tx, property, device uuid.UUID, methods []string, req AttendanceKioskClockRequest) (clockOutcome, error) {
	at := clock.Now()
	if req.Offline {
		t, err := parseInstant("occurredAt", req.OccurredAt)
		if err != nil {
			return clockOutcome{}, err
		}
		at = t
	}
	var emp hris.Employee
	method := hris.MethodKioskQR
	switch {
	case strings.TrimSpace(req.QRToken) != "":
		eid, err := m.parseQR(req.QRToken, at)
		if err != nil {
			return clockOutcome{}, err
		}
		if emp, err = employeeAt(ctx, tx, property, eid); err != nil {
			return clockOutcome{}, err
		}
	case strings.TrimSpace(req.EmployeeNo) != "":
		method = hris.MethodKioskPIN
		e, err := hris.EmployeeByNo(ctx, tx, property, strings.TrimSpace(req.EmployeeNo))
		if err != nil {
			return clockOutcome{}, err
		}
		if e == nil {
			return clockOutcome{}, errs.Unauthorized("employee number or PIN is wrong")
		}
		if err := m.verifyPIN(ctx, tx, *e, req.PIN); err != nil {
			return clockOutcome{}, err
		}
		emp = *e
	default:
		return clockOutcome{}, handle.Invalid("qrToken", "required", "scan your QR or enter your employee number and PIN")
	}
	if len(methods) > 0 && !slices.Contains(methods, method) {
		return clockOutcome{}, errs.Conflict("method_not_allowed", "this kiosk does not accept "+strings.ReplaceAll(method, "_", " "))
	}
	return m.recordEvent(ctx, tx, clockInput{Emp: emp, Direction: req.Direction, At: at, Method: method, Source: "kiosk", DeviceID: &device,
		ClientEventID: strings.TrimSpace(req.ClientEventID), Offline: req.Offline})
}

// verifyPIN checks the attendance PIN; 5 wrong PINs lock it 15 minutes
// (the failure is counted in its own transaction, the request fails).
func (m *Module) verifyPIN(ctx context.Context, tx pgx.Tx, emp hris.Employee, pin string) error {
	var hash *string
	var locked *time.Time
	err := tx.QueryRow(ctx, `SELECT pin_hash, locked_until FROM hris.attendance_profiles WHERE employee_id = $1`, emp.ID).Scan(&hash, &locked)
	if dbtx.IsNoRows(err) || (err == nil && hash == nil) {
		password.VerifyDummy(pin)
		return errs.Unauthorized("employee number or PIN is wrong")
	}
	if err != nil {
		return err
	}
	if locked != nil && locked.After(clock.Now()) {
		return errs.Locked("the attendance PIN is locked for a few minutes after wrong attempts")
	}
	if ok, _ := password.Verify(pin, *hash); !ok {
		if err := m.DB.WithTx(dbtx.System(context.WithoutCancel(ctx)), func(t pgx.Tx) error {
			_, err := t.Exec(ctx, `UPDATE hris.attendance_profiles SET
				locked_until = CASE WHEN failed_pin_count + 1 >= 5 THEN now() + interval '15 minutes' ELSE locked_until END,
				failed_pin_count = CASE WHEN failed_pin_count + 1 >= 5 THEN 0 ELSE failed_pin_count + 1 END WHERE employee_id = $1`, emp.ID)
			return err
		}); err != nil {
			return err
		}
		return errs.Unauthorized("employee number or PIN is wrong")
	}
	_, err = tx.Exec(ctx, `UPDATE hris.attendance_profiles SET failed_pin_count = 0, locked_until = NULL WHERE employee_id = $1 AND failed_pin_count > 0`, emp.ID)
	return err
}

func (m *Module) kioskClockHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceKioskClockRequest) (AttendanceClockResult, error) {
	property := handle.Property(ctx)
	did, code, _, _, methods, err := kioskDevice(ctx, tx, property)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	out, err := m.kioskClock(ctx, tx, property, did, methods, req)
	if err != nil {
		return AttendanceClockResult{}, err
	}
	action := "clock"
	if out.Duplicate {
		action = "clock_duplicate"
	}
	return out.result(), audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: action, EntityType: "hris.attendance_event",
		EntityID: out.Event.ID.String(), EntityLabel: out.Event.EmployeeNo + " · " + out.Event.Direction + " " + ymd(out.Event.WorkDate), PropertyID: &property,
		After: map[string]any{"kiosk": code, "method": out.Event.Method, "direction": out.Event.Direction, "offline": req.Offline}})
}

// kioskSyncHTTP processes an offline batch, one transaction per event (a
// rejected event does not stop the batch; resubmissions are duplicates).
func (m *Module) kioskSyncHTTP(w http.ResponseWriter, r *http.Request) {
	handle.Write(m.DB, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceKioskSyncRequest) (AttendanceKioskSyncResult, error) {
		property := handle.Property(ctx)
		did, code, _, _, methods, err := kioskDevice(ctx, tx, property)
		if err != nil {
			return AttendanceKioskSyncResult{}, err
		}
		if len(req.Events) == 0 || len(req.Events) > 200 {
			return AttendanceKioskSyncResult{}, handle.Invalid("events", "invalid", "send 1 to 200 events")
		}
		out := AttendanceKioskSyncResult{Results: []AttendanceSyncItemResult{}}
		accepted, dups := 0, 0
		for _, ev := range req.Events {
			ev.Offline = true
			res := AttendanceSyncItemResult{ClientEventID: ev.ClientEventID}
			if strings.TrimSpace(ev.ClientEventID) == "" {
				res.Status, res.Error = "rejected", "clientEventId is required"
				out.Results = append(out.Results, res)
				continue
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return out, err
			}
			o, err := m.kioskClock(ctx, sp, property, did, methods, ev)
			if err != nil {
				_ = sp.Rollback(ctx)
				if pe, ok := errs.As(err); ok && pe.Kind != errs.KindInternal {
					res.Status, res.Error = "rejected", pe.Message
					out.Results = append(out.Results, res)
					continue
				}
				return out, err
			}
			if err := sp.Commit(ctx); err != nil {
				return out, err
			}
			cr := o.result()
			res.Result = &cr
			res.Status = "accepted"
			accepted++
			if o.Duplicate {
				res.Status = "duplicate"
				dups++
				accepted--
			}
			out.Results = append(out.Results, res)
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "sync", EntityType: "hris.attendance_event", EntityLabel: code,
			PropertyID: &property, After: map[string]any{"events": len(req.Events), "accepted": accepted, "duplicates": dups}})
	})(w, r)
}

// SyncKioskClock is the platform offline-sync action hris.kiosk_clock of
// the Attendance Kiosk (FR-ATT-07; the sync queue is idempotent per item and
// the client event id per employee).
func (m *Module) SyncKioskClock(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var req AttendanceKioskClockRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid kiosk clock payload")
	}
	property := handle.Property(ctx)
	did, code, _, _, methods, err := kioskDevice(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	if req.ClientEventID == "" {
		return nil, errs.Validation("invalid_payload", "clientEventId is required")
	}
	req.Offline = req.OccurredAt != ""
	o, err := m.kioskClock(ctx, tx, property, did, methods, req)
	if err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "clock", EntityType: "hris.attendance_event", EntityID: o.Event.ID.String(),
		EntityLabel: o.Event.EmployeeNo + " · " + o.Event.Direction, PropertyID: &property, After: map[string]any{"kiosk": code, "offline": true,
			"duplicate": o.Duplicate}}); err != nil {
		return nil, err
	}
	return o.result(), nil
}

// SyncESSClock is the platform offline-sync action hris.ess_clock of the
// ESS Clock In / Out.
func (m *Module) SyncESSClock(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var req AttendanceClockRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid clock payload")
	}
	if req.ClientEventID == "" {
		return nil, errs.Validation("invalid_payload", "clientEventId is required")
	}
	req.Offline = req.OccurredAt != ""
	o, emp, err := m.essClock(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	return o.result(), audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "clock", EntityType: "hris.attendance_event",
		EntityID: o.Event.ID.String(), EntityLabel: o.Event.EmployeeNo + " · " + o.Event.Direction, PropertyID: &emp.PropertyID,
		After: map[string]any{"offline": req.Offline, "duplicate": o.Duplicate}})
}
