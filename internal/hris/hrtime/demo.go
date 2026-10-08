package hrtime

// Demo data of time & attendance (Modern Golf & Country Club): shift
// templates per department, staffing requirements, the holiday calendar,
// the club geofence (300 m, §16 #7), six mock biometric devices and a kiosk,
// the mobile GPS clock-ins of the field staff (demo_presence.go),
// attendance profiles with biometric consent, published rosters of the last
// four weeks, this week and next week (F&B, Kitchen, Sport Club, Golf
// Operations), 30 days of clock events (mostly on time, some late, a few
// absent), leave balances with carry-over from last year, leave and
// permission requests (approved and waiting for the department head) and
// overtime. Idempotent: nothing is seeded twice; no assignment breaks a
// certification rule (the lifeguard with an expired certificate is not
// scheduled).

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/iam/password"
)

type demoTemplate struct {
	code, name, unit, start, end string
	brk                          int
	role, color                  string
}

var demoTemplates = []demoTemplate{
	{"FNB-AM", "F&B Morning", "FNB", "07:00", "15:00", 60, "", "#4f8a8b"},
	{"FNB-PM", "F&B Evening", "FNB", "14:00", "22:00", 60, "", "#7a5c99"},
	{"KIT-AM", "Kitchen Morning", "KITCHEN", "06:00", "14:00", 60, "food_handler", "#c47f2c"},
	{"KIT-PM", "Kitchen Evening", "KITCHEN", "13:00", "21:00", 60, "food_handler", "#a0522d"},
	{"POOL-AM", "Pool Morning", "SPORT", "07:00", "15:00", 60, "lifeguard", "#2c7fb8"},
	{"POOL-PM", "Pool Afternoon", "SPORT", "12:00", "20:00", 60, "lifeguard", "#41b6c4"},
	{"SPORT-DESK", "Sport Reception", "SPORT", "08:00", "17:00", 60, "", "#7fcdbb"},
	{"STARTER-AM", "Starter Early", "GOLF-OPS", "05:30", "13:30", 60, "starter", "#31a354"},
	{"STARTER-PM", "Starter Late", "GOLF-OPS", "11:00", "19:00", 60, "starter", "#78c679"},
	{"OFFICE", "Office Hours", "", "08:00", "17:00", 60, "", "#636363"},
	{"NIGHT-SEC", "Night Security", "ENG", "22:00", "06:00", 60, "security", "#252525"},
}

// demoRoster: per unit the templates employees rotate on (by position).
var demoRoster = map[string]map[string][]string{
	"FNB":      {"WAITER": {"FNB-AM", "FNB-PM"}, "BARTENDER": {"FNB-PM"}, "FNB-MGR": {"OFFICE"}},
	"KITCHEN":  {"COOK": {"KIT-AM", "KIT-PM"}, "EXEC-CHEF": {"KIT-AM"}},
	"SPORT":    {"LIFEGUARD": {"POOL-AM", "POOL-PM"}, "SPORT-RECEPT": {"SPORT-DESK"}, "GYM-INSTR": {"SPORT-DESK"}, "SPORT-MGR": {"OFFICE"}},
	"GOLF-OPS": {"STARTER": {"STARTER-AM", "STARTER-PM"}, "GOLF-ADMIN": {"OFFICE"}, "CADDY-MASTER": {"OFFICE"}, "GOLF-MGR": {"OFFICE"}},
}

var demoHolidays = []struct {
	day, name, kind string
	deducts         bool
}{
	{"2026-12-24", "Cuti Bersama Natal", "collective_leave", true}, {"2026-12-25", "Hari Raya Natal", "public_holiday", false},
	{"2027-01-01", "Tahun Baru Masehi", "public_holiday", false}, {"2027-02-06", "Tahun Baru Imlek", "public_holiday", false},
	{"2027-03-10", "Isra Mikraj", "public_holiday", false}, {"2027-03-29", "Hari Suci Nyepi", "public_holiday", false},
	{"2026-08-17", "Hari Kemerdekaan RI", "public_holiday", false}, {"2026-09-04", "Maulid Nabi Muhammad SAW", "public_holiday", false},
	{"2026-01-01", "Tahun Baru Masehi", "public_holiday", false}, {"2026-03-20", "Hari Raya Idul Fitri", "public_holiday", false},
	{"2026-03-21", "Hari Raya Idul Fitri", "public_holiday", false}, {"2026-05-01", "Hari Buruh", "public_holiday", false},
	{"2026-05-27", "Hari Raya Idul Adha", "public_holiday", false}, {"2026-06-01", "Hari Lahir Pancasila", "public_holiday", false},
}

var demoDevices = []struct{ code, point string }{
	{"BIO-CLUB", "Staff entrance clubhouse"}, {"BIO-CADDY", "Caddy house / golf operations"}, {"BIO-SPORT", "Sport club"},
	{"BIO-FNB", "Back of house F&B"}, {"BIO-BQT", "Banquet"}, {"BIO-ENG", "Engineering & warehouse"},
}

func demoHash(parts ...string) uint32 {
	h := fnv.New32a()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return h.Sum32()
}

type demoEmp struct {
	hris.Employee
	unit string
}

// SeedDemo seeds the time & attendance demo of a property (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	var seeded, hr bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_templates WHERE property_id = $1 AND code = 'FNB-AM'),
		EXISTS (SELECT 1 FROM hris.employees WHERE property_id = $1 AND employee_no = 'EMP-00001')`, property).Scan(&seeded, &hr); err != nil {
		return err
	}
	if !hr {
		return nil
	}
	if seeded {
		return seedPresenceDemo(ctx, tx, property) // added after the first demo seed (demo_presence.go)
	}
	ctx = reqctx.WithProperty(ctx, property)
	loc := location(ctx, tx, property)
	tday := today(ctx, tx, property)
	units := map[string]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT code, id FROM hris.org_units WHERE property_id = $1`, property)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c string
		var u uuid.UUID
		if err := rows.Scan(&c, &u); err != nil {
			rows.Close()
			return err
		}
		units[c] = u
	}
	rows.Close()
	// templates
	tmpl := map[string]*templateRow{}
	for _, t := range demoTemplates {
		tid := id.New()
		var unit *uuid.UUID
		if u, ok := units[t.unit]; ok {
			unit = &u
		}
		var role *string
		if t.role != "" {
			r := t.role
			role = &r
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.shift_templates (id, property_id, code, name, org_unit_id, start_time, end_time, break_minutes,
			workforce_role, color) VALUES ($1,$2,$3,$4,$5,$6::time,$7::time,$8,$9,$10)`, tid, property, t.code, t.name, unit, t.start, t.end, t.brk, role,
			t.color); err != nil {
			return err
		}
		if tmpl[t.code], err = loadTemplate(ctx, tx, property, tid); err != nil {
			return err
		}
	}
	// staffing requirements
	for _, r := range []struct {
		unit, position, shift string
		n                     int
	}{{"FNB", "WAITER", "FNB-AM", 2}, {"FNB", "WAITER", "FNB-PM", 2}, {"KITCHEN", "COOK", "KIT-AM", 1}, {"KITCHEN", "COOK", "KIT-PM", 1},
		{"SPORT", "LIFEGUARD", "POOL-AM", 1}, {"SPORT", "LIFEGUARD", "POOL-PM", 1}, {"GOLF-OPS", "STARTER", "STARTER-AM", 1}} {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.staffing_requirements (id, property_id, org_unit_id, position_id, shift_template_id, min_staff)
			SELECT $1, $2, $3, p.id, $5, $6 FROM hris.positions p WHERE p.property_id = $2 AND p.code = $4`, id.New(), property, units[r.unit], r.position,
			tmpl[r.shift].ID, r.n); err != nil {
			return err
		}
	}
	for _, h := range demoHolidays {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.holidays (id, property_id, holiday_date, name, kind, deducts_annual_leave) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT DO NOTHING`, id.New(), property, h.day, h.name, h.kind, h.deducts); err != nil {
			return err
		}
	}
	var gid uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO hris.geofences (id, property_id, code, name, latitude, longitude, radius_meters, notes)
		VALUES ($1,$2,'CLUB','Modern Golf & Country Club',-6.229700,106.689400,300,'Club area (PRD P5 §16 #7: 300 m)') RETURNING id`, id.New(), property).
		Scan(&gid); err != nil {
		return err
	}
	for _, d := range demoDevices {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_devices (id, property_id, code, name, device_kind, location_point, methods, vendor, serial_no,
			geofence_id, notes) VALUES ($1,$2,$3,$4,'biometric',$5,'{face_recognition,fingerprint}','mock',$6,$7,'Trial adapter (mock); ZKTeco through the bridge agent in production')`,
			id.New(), property, d.code, "Face & Fingerprint "+d.point, d.point, "MOCK-"+d.code, gid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_devices (id, property_id, code, name, device_kind, location_point, methods, vendor, geofence_id,
		notes) VALUES ($1,$2,'KIOSK-CLUB','Attendance Kiosk Clubhouse','kiosk','Staff entrance clubhouse','{kiosk_qr,kiosk_pin}','other',$3,
		'Link the registered tablet (Settings → Devices) to run the kiosk')`, id.New(), property, gid); err != nil {
		return err
	}
	// employees, profiles, consent
	emps, err := hris.Employees(ctx, tx, hris.EmployeeFilter{PropertyID: property, ActiveOn: &tday})
	if err != nil {
		return err
	}
	pinHash, err := password.Hash(DemoAttendancePIN)
	if err != nil {
		return err
	}
	byNo := map[string]hris.Employee{}
	for _, e := range emps {
		byNo[e.EmployeeNo] = e
		if _, err := ensureProfile(ctx, tx, e); err != nil {
			return err
		}
		consent := demoHash(e.EmployeeNo)%7 != 0
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_profiles SET biometric_consent = $2, consent_signed_on = CASE WHEN $2 THEN $3::date END,
			enrolled_at = CASE WHEN $2 THEN now() END, pin_hash = CASE WHEN $4 THEN $5 END, pin_set_at = CASE WHEN $4 THEN now() END WHERE employee_id = $1`,
			e.ID, consent, ymd(tday.AddDate(0, 0, -60)), e.EmployeeNo == "EMP-00021" || e.EmployeeNo == "EMP-00024", pinHash); err != nil {
			return err
		}
	}
	// rosters: four weeks back, this week and next week
	monday := tday.AddDate(0, 0, -(isoWeekday(tday) - 1))
	var scheduled []demoEmp
	for _, e := range emps {
		if e.OrgUnitCode == nil || e.PositionCode == nil {
			continue
		}
		if r, ok := demoRoster[*e.OrgUnitCode]; ok && len(r[*e.PositionCode]) > 0 && e.EmployeeNo != "EMP-00017" {
			scheduled = append(scheduled, demoEmp{Employee: e, unit: *e.OrgUnitCode})
		}
	}
	type key struct {
		emp uuid.UUID
		day string
	}
	shiftOf := map[key]*templateRow{}
	for w := -4; w <= 1; w++ {
		start := monday.AddDate(0, 0, 7*w)
		end := start.AddDate(0, 0, 6)
		for unitCode := range demoRoster {
			uid, ok := units[unitCode]
			if !ok {
				continue
			}
			sid := id.New()
			y, wk := start.ISOWeek()
			if _, err := tx.Exec(ctx, `INSERT INTO hris.schedules (id, property_id, org_unit_id, name, period_start, period_end, status, version, published_at)
				SELECT $1, $2, $3, name || ' ' || $6, $4, $5, 'published', 1, now() FROM hris.org_units WHERE id = $3`, sid, property, uid, ymd(start), ymd(end),
				fmt.Sprintf("%d-W%02d", y, wk)); err != nil {
				return err
			}
			for i, e := range scheduled {
				if e.unit != unitCode {
					continue
				}
				opts := demoRoster[unitCode][*e.PositionCode]
				offA := (i + w + 10) % 7 // two rest days per week, rotating
				for d := 0; d < 7; d++ {
					day := start.AddDate(0, 0, d)
					if e.JoinDate != nil && e.JoinDate.After(day) {
						continue
					}
					office := len(opts) == 1 && opts[0] == "OFFICE"
					off := d == offA || d == (offA+1)%7
					if office {
						off = d >= 5
					}
					if off {
						if _, err := tx.Exec(ctx, `INSERT INTO hris.shift_assignments (id, property_id, schedule_id, employee_id, work_date, kind)
							VALUES ($1,$2,$3,$4,$5,'off')`, id.New(), property, sid, e.ID, ymd(day)); err != nil {
							return err
						}
						continue
					}
					t := tmpl[opts[(d/3+i+w+10)%len(opts)]]
					st, en := t.window(day, loc)
					role := t.Role
					if role == nil {
						role = e.WorkforceRole
					}
					if _, err := tx.Exec(ctx, `INSERT INTO hris.shift_assignments (id, property_id, schedule_id, employee_id, work_date, kind, shift_template_id,
						starts_at, ends_at, break_minutes, work_minutes, workforce_role) VALUES ($1,$2,$3,$4,$5,'shift',$6,$7,$8,$9,$10,$11)`, id.New(), property,
						sid, e.ID, ymd(day), t.ID, st, en, t.Break, t.Work, role); err != nil {
						return err
					}
					shiftOf[key{e.ID, ymd(day)}] = t
				}
			}
		}
	}
	// leave balances: last year (for the carry-over) and this year's accrual
	m := &Module{}
	lp := hris.NewLeavePolicy()
	annual, _ := lp.LeaveType("ANNUAL")
	for _, e := range emps {
		if e.JoinDate == nil {
			continue
		}
		if _, ok := hris.AnnualGrant(annual, *e.JoinDate, tday.Year()-1); !ok {
			continue
		}
		used := 6 + int(demoHash(e.EmployeeNo, "used")%7)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_balances (id, property_id, employee_id, leave_type, year, entitled, used, granted_on)
			VALUES ($1,$2,$3,'ANNUAL',$4,12,$5,$6)`, id.New(), property, e.ID, tday.Year()-1, used, fmt.Sprintf("%d-01-01", tday.Year()-1)); err != nil {
			return err
		}
	}
	if _, err := m.accrue(ctx, tx, property, nil); err != nil {
		return err
	}
	// past leave & absences shape the attendance
	onLeave := map[key]bool{}
	approveLeave := func(no, lt string, from, to time.Time, reason string) error {
		e, ok := byNo[no]
		if !ok {
			return nil
		}
		days := decimal.Zero
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			if shiftOf[key{e.ID, ymd(d)}] != nil {
				days = days.Add(decimal.NewFromInt(1))
				onLeave[key{e.ID, ymd(d)}] = true
			}
		}
		if days.IsZero() {
			return nil
		}
		number, err := yearlyNumber(ctx, tx, property, "LV", tday.Year())
		if err != nil {
			return err
		}
		rid := id.New()
		t, _ := lp.LeaveType(lt)
		var year *int
		if t.HasBalance() {
			y := from.Year()
			year = &y
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_requests (id, property_id, number, employee_id, leave_type, start_date, end_date, days, balance_year,
			paid, reason, status, decided_at, decision_note) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'approved',now(),'Demo')`, rid, property, number, e.ID,
			lt, ymd(from), ymd(to), days, year, t.Paid, reason); err != nil {
			return err
		}
		if year != nil {
			b, err := loadBalance(ctx, tx, e.ID, lt, *year, true)
			if err != nil || b == nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.leave_balances SET used = used + $2 WHERE id = $1`, b.ID, days); err != nil {
				return err
			}
			if err := ledger(ctx, tx, property, b.ID, e.ID, "usage", days.Neg(), &rid, number); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'on_leave' WHERE employee_id = $1 AND work_date BETWEEN $2::date AND $3::date
			AND kind = 'shift'`, e.ID, ymd(from), ymd(to))
		return err
	}
	if err := approveLeave("EMP-00024", "ANNUAL", tday.AddDate(0, 0, -16), tday.AddDate(0, 0, -14), "Family visit to Yogyakarta"); err != nil {
		return err
	}
	if err := approveLeave("EMP-00023", "UNPAID", tday.AddDate(0, 0, -9), tday.AddDate(0, 0, -9), "Personal matter"); err != nil {
		return err
	}
	if err := approveLeave("EMP-00016", "ANNUAL", tday.AddDate(0, 0, 9), tday.AddDate(0, 0, 10), "Holiday"); err != nil {
		return err
	}
	// clock events of the past 30 days (device or kiosk)
	devs := map[string]uuid.UUID{}
	drows, err := tx.Query(ctx, `SELECT code, id FROM hris.attendance_devices WHERE property_id = $1`, property)
	if err != nil {
		return err
	}
	for drows.Next() {
		var c string
		var d uuid.UUID
		if err := drows.Scan(&c, &d); err != nil {
			drows.Close()
			return err
		}
		devs[c] = d
	}
	drows.Close()
	unitDevice := map[string]string{"FNB": "BIO-FNB", "KITCHEN": "BIO-FNB", "SPORT": "BIO-SPORT", "GOLF-OPS": "BIO-CADDY"}
	now := clock.Now()
	pol := newPolicies(property)
	for _, e := range scheduled {
		for back := 30; back >= 0; back-- {
			day := tday.AddDate(0, 0, -back)
			t := shiftOf[key{e.ID, ymd(day)}]
			if t == nil || onLeave[key{e.ID, ymd(day)}] {
				continue
			}
			h := demoHash(e.EmployeeNo, ymd(day))
			st, en := t.window(day, loc)
			if h%25 == 0 {
				continue // absent
			}
			in := st.Add(-time.Duration(3+h%12) * time.Minute)
			if h%11 == 0 {
				in = st.Add(time.Duration(15+h%30) * time.Minute) // late
			}
			out := en.Add(time.Duration(h%14) * time.Minute)
			if h%17 == 0 {
				out = en.Add(2 * time.Hour) // stayed for overtime
			}
			dev := devs[unitDevice[e.unit]]
			method := map[bool]string{true: hris.MethodFaceRecognition, false: hris.MethodFingerprint}[h%3 != 0]
			for _, ev := range []struct {
				dir string
				at  time.Time
			}{{"in", in}, {"out", out}} {
				if ev.at.After(now) {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_events (id, property_id, employee_id, work_date, direction, occurred_at, method, source,
					device_id, client_event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,'device',$8,$9)`, id.New(), property, e.ID, ymd(day), ev.dir, ev.at, method, dev,
					fmt.Sprintf("DEMO:%s:%s:%s", e.EmployeeNo, ymd(day), ev.dir)); err != nil {
					return err
				}
			}
			if h%17 == 0 && out.Before(now) {
				// approved overtime for the extra two hours (after the fact)
				number, err := yearlyNumber(ctx, tx, property, "OT", tday.Year())
				if err != nil {
					return err
				}
				kind, ww, err := dayKindFor(ctx, tx, e.Employee, day)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO hris.overtime_requests (id, property_id, number, employee_id, work_date, starts_at, ends_at, hours, timing,
					reason, day_kind, work_week_days, status, decided_at, decision_note) VALUES ($1,$2,$3,$4,$5,$6,$7,2,'after',$8,$9,$10,'approved',now(),'Demo')`,
					id.New(), property, number, e.ID, ymd(day), en, en.Add(2*time.Hour), "Covering the evening event", kind, ww); err != nil {
					return err
				}
			}
		}
	}
	// evaluate the days (yesterday and before final)
	for _, e := range scheduled {
		for back := 30; back >= 0; back-- {
			day := tday.AddDate(0, 0, -back)
			if _, ok := shiftOf[key{e.ID, ymd(day)}]; !ok {
				continue
			}
			if _, err := m.recomputeDay(ctx, tx, e.Employee, day, back > 0, pol); err != nil {
				return err
			}
		}
	}
	// requests waiting for the department head / HR
	pending := func(kind, table string, rid uuid.UUID, e hris.Employee) error {
		sup, err := hris.Supervisor(ctx, tx, e.ID)
		if err != nil || sup == nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.request_approvals (id, property_id, request_kind, request_id, step_no, level, approver_employee_id, status)
			VALUES ($1,$2,$3,$4,1,'supervisor',$5,'pending')`, id.New(), property, kind, rid, sup.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE `+table+` SET approval_step = 1 WHERE id = $1`, rid)
		return err
	}
	if e, ok := byNo["EMP-00025"]; ok {
		from := tday.AddDate(0, 0, 12)
		number, err := yearlyNumber(ctx, tx, property, "LV", tday.Year())
		if err != nil {
			return err
		}
		rid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO hris.leave_requests (id, property_id, number, employee_id, leave_type, start_date, end_date, days, balance_year,
			paid, reason) VALUES ($1,$2,$3,$4,'ANNUAL',$5,$6,3,$7,true,'Sister''s wedding in Bandung')`, rid, property, number, e.ID, ymd(from),
			ymd(from.AddDate(0, 0, 2)), from.Year()); err != nil {
			return err
		}
		if err := pending(KindLeave, "hris.leave_requests", rid, e); err != nil {
			return err
		}
	}
	if e, ok := byNo["EMP-00021"]; ok {
		day := tday.AddDate(0, 0, 2)
		number, err := yearlyNumber(ctx, tx, property, "PM", tday.Year())
		if err != nil {
			return err
		}
		rid := id.New()
		st := localAt(day, 7, 0, loc)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.permission_requests (id, property_id, number, employee_id, permission_type, work_date, starts_at, ends_at,
			minutes, paid, reason) VALUES ($1,$2,$3,$4,'LATE_ARRIVAL',$5,$6,$7,90,true,'Motorbike service')`, rid, property, number, e.ID, ymd(day), st,
			st.Add(90*time.Minute)); err != nil {
			return err
		}
		if err := pending(KindPermission, "hris.permission_requests", rid, e); err != nil {
			return err
		}
		number, err = yearlyNumber(ctx, tx, property, "OT", tday.Year())
		if err != nil {
			return err
		}
		oid := id.New()
		kind, ww, err := dayKindFor(ctx, tx, e, tday.AddDate(0, 0, 3))
		if err != nil {
			return err
		}
		ost := localAt(tday.AddDate(0, 0, 3), 22, 0, loc)
		tiers, _ := json.Marshal([]hris.OvertimeTier{})
		if _, err := tx.Exec(ctx, `INSERT INTO hris.overtime_requests (id, property_id, number, employee_id, work_date, starts_at, ends_at, hours, timing, reason,
			day_kind, work_week_days, tiers) VALUES ($1,$2,$3,$4,$5,$6,$7,2,'before','Wedding banquet clean-up',$8,$9,$10)`, oid, property, number, e.ID,
			ymd(tday.AddDate(0, 0, 3)), ost, ost.Add(2*time.Hour), kind, ww, tiers); err != nil {
			return err
		}
		if err := pending(KindOvertime, "hris.overtime_requests", oid, e); err != nil {
			return err
		}
	}
	return seedPresenceDemo(ctx, tx, property)
}
