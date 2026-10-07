package hrtime

// Demo of the Attendance Form (presence domain, mobile GPS): the field staff
// of the org units on mobile GPS (Attendance Configuration: Course
// Maintenance, Sales & Banquet) get the demo attendance PIN, and the last
// two weeks of their mobile GPS clock-ins: inside the club geofence, a few
// with a weak GPS fix, and sales visits outside the area — accepted by the
// supervisor, the most recent ones still waiting for review. Idempotent;
// also runs on an instance whose time & attendance demo was seeded before.

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/iam/password"
)

// DemoAttendancePIN is the attendance PIN of the demo employees (kiosk and Attendance Form).
const DemoAttendancePIN = "246810"

func seedPresenceDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var gid uuid.UUID
	var glat, glng float64
	var radius int
	err := tx.QueryRow(ctx, `SELECT id, latitude::float8, longitude::float8, radius_meters FROM hris.geofences WHERE property_id = $1 AND code = 'CLUB'`,
		property).Scan(&gid, &glat, &glng, &radius)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ctx = reqctx.WithProperty(ctx, property)
	loc := location(ctx, tx, property)
	tday := today(ctx, tx, property)
	cfg, _, err := hris.LoadAttendanceConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	emps, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: property, ActiveOn: &tday})
	if err != nil {
		return err
	}
	var field []hris.Employee
	for _, e := range emps {
		codes, err := orgUnitCodes(ctx, tx, e.OrgUnitID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(codes, func(c string) bool { return slices.Contains(cfg.MobileGPSOrgUnits, c) }) {
			field = append(field, e)
		}
	}
	if len(field) == 0 {
		return nil
	}
	pinHash, err := password.Hash(DemoAttendancePIN)
	if err != nil {
		return err
	}
	for _, e := range field {
		if _, err := ensureProfile(ctx, tx, e); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET pin_hash = $2, pin_set_at = now() WHERE employee_id = $1 AND pin_hash IS NULL`,
			e.ID, pinHash); err != nil {
			return err
		}
	}

	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.attendance_events WHERE property_id = $1 AND client_event_id LIKE 'DEMO-PRESENCE:%')`,
		property).Scan(&done); err != nil || done {
		return err
	}
	now := clock.Now()
	m, pol := &Module{}, newPolicies(property)
	for _, e := range field {
		greens := e.PositionCode != nil && (*e.PositionCode == "GREENKEEPER" || *e.PositionCode == "COURSE-SUPT")
		sales := e.PositionCode != nil && *e.PositionCode == "SALES-EXEC"
		for back := 14; back >= 1; back-- {
			day := tday.AddDate(0, 0, -back)
			if wd := day.Weekday(); wd == time.Sunday || (wd == time.Saturday && !greens) {
				continue
			}
			h := demoHash(e.EmployeeNo, ymd(day), "gps")
			if h%20 == 0 {
				continue // absent
			}
			start := localAt(day, 8, 0, loc)
			if greens {
				start = localAt(day, 6, 0, loc)
			}
			in := start.Add(-time.Duration(2+h%10) * time.Minute)
			if h%9 == 0 {
				in = start.Add(time.Duration(15+h%20) * time.Minute) // late
			}
			out := start.Add(9*time.Hour + time.Duration(h%20)*time.Minute)
			for i, ev := range []struct {
				dir string
				at  time.Time
			}{{"in", in}, {"out", out}} {
				if ev.at.After(now) {
					continue
				}
				g := demoHash(e.EmployeeNo, ymd(day), ev.dir)
				// on the course / in the clubhouse: up to ±150 m from the club point
				lat := glat + (float64(g%300)-150)/111320
				lng := glng + (float64(g/300%300)-150)/111320
				acc := float64(6 + g%20)
				flags, review := []string{}, "not_required"
				if sales && i == 0 && (h%4 == 0 || back <= 2) {
					lat += 0.03 // a client visit about 3 km away
					flags = append(flags, hris.FlagOutOfArea)
				}
				if g%23 == 0 {
					acc = float64(120 + g%60) // weak GPS fix
					flags = append(flags, hris.FlagLowAccuracy)
				}
				var reviewed *time.Time
				var note *string
				if len(flags) > 0 {
					review = "pending"
					if back > 3 {
						accepted := "Accepted by the supervisor (demo)"
						review, reviewed, note = "accepted", &ev.at, &accepted
					}
				}
				d := hris.DistanceMeters(lat, lng, glat, glng)
				within := d <= float64(radius)
				if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_events (id, property_id, employee_id, work_date, direction, occurred_at, method, source,
					latitude, longitude, accuracy_meters, distance_meters, geofence_id, within_geofence, flags, review_status, reviewed_at, review_note,
					client_event_id) VALUES ($1,$2,$3,$4,$5,$6,'mobile_gps','ess',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
					id.New(), property, e.ID, ymd(day), ev.dir, ev.at, lat, lng, acc, float64(int(d*10))/10, gid, within, flags, review, reviewed,
					note,
					fmt.Sprintf("DEMO-PRESENCE:%s:%s:%s", e.EmployeeNo, ymd(day), ev.dir)); err != nil {
					return err
				}
			}
			if _, err := m.recomputeDay(ctx, tx, e, day, true, pol); err != nil {
				return err
			}
		}
	}
	return nil
}
