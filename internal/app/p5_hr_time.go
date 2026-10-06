package app

// PRD P5 EP-06–08: shift scheduling, attendance (kiosk, devices, geofence) and leave, permission & overtime. internal/app/p5.go calls these functions; the area fills them.
//
// Time & attendance (owner: hris/hrtime) sits in the back office layer.
// The composition root plugs in the approval engine (document types of
// leave, permission, overtime, shift swap and attendance correction), the
// offline sync actions of the kiosk and ESS clock, the operational demand
// of golf, banquet and sport club shown while scheduling (FR-SCH-04; hris
// does not import the business lines) and the removal of leavers from the
// attendance devices (hris.employee_terminated).

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf"
	"oneclub/internal/hris"
	"oneclub/internal/hris/hrtime"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// p5HRTime holds the services of the area.
type p5HRTime struct {
	Module *hrtime.Module
}

func p5HRTimeContributions() []catalog.Contribution {
	return []catalog.Contribution{(&hrtime.Module{}).Contribution()}
}
func p5HRTimeDocumentTypes() []provision.DocumentType { return hrtime.DocumentTypes() }
func p5HRTimeTemplates() []provision.Template         { return hrtime.Templates() }

// buildP5HRTime wires routes, hooks, approval decisions and jobs.
func (a *App) buildP5HRTime(reg *route.Registry, cfg *config.Config, db *dbtx.DB, files *storage.Files) {
	m := &hrtime.Module{DB: db, Engine: a.Engine, Events: a.Bus, Approvals: a.Approvals, Notify: a.Notification, Files: files,
		Demand: staffingDemand, StaffURL: func() string { return cfg.PublicBaseURL }, Location: a.Instance.Location, Secret: []byte(cfg.AppSecret),
		Partners: hrPartners{}}
	m.Register(reg)
	m.RegisterDecisions(a.Approvals.RegisterDocumentType)
	m.RegisterJobs(a.Registrar, a.Instance.Location)
	a.Sync.Handle("hris.kiosk_clock", m.SyncKioskClock)
	a.Sync.Handle("hris.ess_clock", m.SyncESSClock)
	a.Time.Module = m
}

// subscribeP5HRTime registers the event subscribers: a leaver is removed
// from the biometric devices (PRD P5 §16 #10).
func (a *App) subscribeP5HRTime() {
	a.Bus.Subscribe(hris.EventEmployeeTerminated, "hris.attendance_device_removal", a.Time.Module.OnEmployeeTerminated)
	// FR-ATT-08: a caddy's first device clock-in of the day marks the caddy
	// present in Caddy Master (joins the Caddy Queue) unless the caddy master
	// already recorded the day.
	a.Bus.Subscribe(hris.EventPartnerAttendanceRecorded, "golf.caddy_device_attendance", a.caddyDeviceAttendance)
}

// caddyDeviceAttendance records the attendance of a caddy who clocked in on
// a biometric device (golf never imports hris).
func (a *App) caddyDeviceAttendance(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p hris.PartnerAttendanceRecorded
	if err := e.Decode(&p); err != nil {
		return nil //nolint:nilerr // foreign payload
	}
	if p.HolderKind != hris.HolderCaddy || p.Direction != "in" {
		return nil
	}
	ctx = reqctx.WithProperty(dbtx.System(ctx), p.PropertyID)
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM golf.caddy_attendance WHERE caddy_id = $1 AND work_date = $2::date`, p.PartnerID, p.WorkDate).Scan(&status)
	if err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	if status != "" {
		return nil // recorded by the caddy master (or an earlier clock-in)
	}
	local := p.OccurredAt
	if loc := a.Instance.Location(); loc != nil {
		local = local.In(loc)
	}
	_, err = a.Golf.RecordAttendance(ctx, tx, p.PropertyID, golf.AttendanceRequest{Date: p.WorkDate, Entries: []golf.AttendanceEntry{{CaddyID: p.PartnerID,
		Status: "present", Notes: "Clock-in " + local.Format("15:04") + " on " + p.DeviceCode + " (" + strings.ReplaceAll(p.Method, "_", " ") + ")"}}})
	return err
}

// staffingDemand reads the operational demand of a period (FR-SCH-04): golf
// bookings and players (P1), banquet events and expected pax (P3), sport
// club class sessions (P2) per local date.
func staffingDemand(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]hrtime.StaffingDemandDay, error) {
	rows, err := q.Query(ctx, `WITH tz AS (SELECT coalesce((SELECT timezone FROM platform.properties WHERE id = $1 AND timezone <> ''),
		  (SELECT timezone FROM platform.instance)) AS z)
		SELECT d, k, sum(v)::int FROM (
		  SELECT play_date AS d, 'golfBookings' AS k, 1 AS v FROM golf.bookings WHERE property_id = $1 AND play_date BETWEEN $2::date AND $3::date
		    AND status IN ('confirmed', 'checked_in', 'completed') AND parent_booking_id IS NULL
		  UNION ALL SELECT play_date, 'golfers', player_count FROM golf.bookings WHERE property_id = $1 AND play_date BETWEEN $2::date AND $3::date
		    AND status IN ('confirmed', 'checked_in', 'completed') AND parent_booking_id IS NULL
		  UNION ALL SELECT (start_at AT TIME ZONE (SELECT z FROM tz))::date, 'events', 1 FROM banquet.events WHERE property_id = $1
		    AND status IN ('tentative', 'definite') AND (start_at AT TIME ZONE (SELECT z FROM tz))::date BETWEEN $2::date AND $3::date
		  UNION ALL SELECT (start_at AT TIME ZONE (SELECT z FROM tz))::date, 'eventPax', coalesce(final_pax, guaranteed_pax, expected_pax) FROM banquet.events
		    WHERE property_id = $1 AND status IN ('tentative', 'definite') AND (start_at AT TIME ZONE (SELECT z FROM tz))::date BETWEEN $2::date AND $3::date
		  UNION ALL SELECT (lower(period) AT TIME ZONE (SELECT z FROM tz))::date, 'classSessions', 1 FROM sportclub.class_sessions WHERE property_id = $1
		    AND status <> 'cancelled' AND (lower(period) AT TIME ZONE (SELECT z FROM tz))::date BETWEEN $2::date AND $3::date) x
		GROUP BY d, k ORDER BY d, k`, property, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	byDay := map[string]map[string]int{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		byDay[d.Format(time.DateOnly)] = map[string]int{"golfBookings": 0, "golfers": 0, "events": 0, "eventPax": 0, "classSessions": 0}
	}
	err = forEachRow(rows, func(r pgx.Rows) error {
		var d time.Time
		var k string
		var v int
		if err := r.Scan(&d, &k, &v); err != nil {
			return err
		}
		if m, ok := byDay[d.Format(time.DateOnly)]; ok {
			m[k] = v
		}
		return nil
	})
	out := make([]hrtime.StaffingDemandDay, 0, len(byDay))
	for d, v := range byDay {
		out = append(out, hrtime.StaffingDemandDay{Date: d, Values: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, err
}

func forEachRow(rows pgx.Rows, fn func(pgx.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// demoP5HRTime seeds the demo data of the area: shift templates, staffing
// requirements, holidays, a geofence and devices, published rosters of this
// and next week, 30 days of attendance (some late / absent), leave balances
// and requests, permission and overtime.
func demoP5HRTime(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	return hrtime.SeedDemo(ctx, tx, property)
}
