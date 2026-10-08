package hrtime

// Presence Form: clock in / out from the login page without signing in.
// The employee identifies with employee number + attendance PIN (the kiosk
// PIN, 5 wrong PINs lock it 15 minutes) and the browser's GPS position; the
// event follows the mobile GPS rules (Attendance Configuration, geofence,
// Attendance Policy) like Employee Self Service.

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/iam/password"
)

// AttendancePresenceRequest is a clock-in / out from the Presence Form.
type AttendancePresenceRequest struct {
	PropertyID     uuid.UUID `json:"propertyId"`
	EmployeeNo     string    `json:"employeeNo"`
	PIN            string    `json:"pin" doc:"Attendance PIN (6 digits, set in Employee Self Service)"`
	Direction      string    `json:"direction" enum:"in,out"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	AccuracyMeters *float64  `json:"accuracyMeters,omitempty"`
	ClientEventID  string    `json:"clientEventId,omitempty" doc:"Client UUID; a resubmission returns the recorded event"`
}

// presenceLimiter limits Presence Form submissions per client (PIN guessing).
var presenceLimiter = &handle.Limiter{N: 20, Period: time.Minute}

func (m *Module) registerPresence(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/public/attendance:clock", Module: hris.Module, Tag: "HRIS Attendance Presence Form",
		Auth: route.AuthPublic, Summary: "Clock in / out from the login page (employee number, attendance PIN and GPS; rate limited)",
		Request: AttendancePresenceRequest{}, Response: AttendanceClockResult{}, Status: http.StatusOK, Handler: presenceLimiter.Wrap(m.presenceClockHTTP)})
}

func (m *Module) presenceClockHTTP(w http.ResponseWriter, r *http.Request) {
	var req AttendancePresenceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.PropertyID == uuid.Nil {
		httpx.WriteError(w, r, handle.Invalid("propertyId", "required", "propertyId is required"))
		return
	}
	if req.Direction != "in" && req.Direction != "out" {
		httpx.WriteError(w, r, enumErr("direction", []string{"in", "out"}))
		return
	}
	if req.Latitude == nil || req.Longitude == nil {
		httpx.WriteError(w, r, handle.Invalid("latitude", "required", "turn on GPS to clock in"))
		return
	}
	ctx := reqctx.WithProperty(dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{req.PropertyID}}), req.PropertyID)
	var out AttendanceClockResult
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		e, err := hris.EmployeeByNo(ctx, tx, req.PropertyID, strings.TrimSpace(req.EmployeeNo))
		if err != nil {
			return err
		}
		if e == nil {
			password.VerifyDummy(req.PIN)
			return errs.Unauthorized("employee number or PIN is wrong")
		}
		if err := m.verifyPIN(ctx, tx, *e, req.PIN); err != nil {
			return err
		}
		o, err := m.recordEvent(ctx, tx, clockInput{Emp: *e, Direction: req.Direction, At: clock.Now(), Method: hris.MethodMobileGPS, Source: "ess",
			Lat: req.Latitude, Lng: req.Longitude, Acc: req.AccuracyMeters, ClientEventID: strings.TrimSpace(req.ClientEventID)})
		if err != nil {
			return err
		}
		action := "clock"
		if o.Duplicate {
			action = "clock_duplicate"
		}
		out = o.result()
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: action, EntityType: "hris.attendance_event",
			EntityID: o.Event.ID.String(), EntityLabel: e.EmployeeNo + " · " + o.Event.Direction + " " + ymd(o.Event.WorkDate), PropertyID: &e.PropertyID,
			After: map[string]any{"form": "presence", "method": o.Event.Method, "flags": o.Event.Flags, "reviewStatus": o.Event.ReviewStatus}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
