package e2e

// PRD P5 time & attendance (EP-06 Shift Scheduling, EP-07 Attendance,
// EP-08 Leave, Permission & Overtime, their ESS sections of EP-16, the
// EP-25/26 devices, kiosk and offline sync, the EP-27 reports / KPIs and the
// EP-28 leave balance import). Each test builds its own org unit with a
// department head and two employees linked to matrix users (unlinked at
// the end) and works on dates relative to the club date (Asia/Jakarta).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/app"
	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/reporting"
)

// ttTeam is the org unit of a test: a department head M and employees A and
// B with ESS logins.
type ttTeam struct {
	sfx, unitID, unitCode, posID string
	mgrID, aID, bID              string
	mgrNo, aNo, bNo              string
	mgr, a, b                    *Client
}

// ttLink links a matrix user to an employee for the test.
func ttLink(t *testing.T, role, employeeID string) *Client {
	t.Helper()
	email := "role." + role + "@matrix.test"
	sysExec(t, inst, `UPDATE platform.users SET employee_id = NULL WHERE email = $1`, email)
	sysExec(t, inst, `UPDATE platform.users SET employee_id = $2 WHERE email = $1`, email, employeeID)
	t.Cleanup(func() { sysExec(t, inst, `UPDATE platform.users SET employee_id = NULL WHERE email = $1`, email) })
	return roleUser(t, inst, role)
}

// ttSetup builds the team of a test below Sales & Banquet (mobile GPS org
// unit of the Attendance Configuration).
func ttSetup(t *testing.T, roles [3]string) ttTeam {
	t.Helper()
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	tm := ttTeam{sfx: hrSuffix()}
	tm.unitCode = "TT" + tm.sfx
	tm.unitID = idOf(hr.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": tm.unitCode, "name": "Time Test " + tm.sfx,
		"parentId": hrID(t, "org_units", "SALES"), "unitType": "section"}, "Idempotency-Key", newKey()))
	tm.posID = idOf(hr.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "TTP" + tm.sfx, "name": "Time Tester", "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()))
	m := hrNewEmployee(t, hr, "Manager "+tm.sfx, "TTP"+tm.sfx, map[string]any{"joinDate": hrDay(-1500)})
	tm.mgrID, tm.mgrNo = str(m["id"]), str(m["employeeNo"])
	hr.Must(200, "PATCH", hrBase+"/org-units/"+tm.unitID, map[string]any{"headEmployeeId": tm.mgrID})
	a := hrNewEmployee(t, hr, "Ayu "+tm.sfx, "TTP"+tm.sfx, map[string]any{"joinDate": hrDay(-800), "supervisorId": tm.mgrID, "gender": "female"})
	b := hrNewEmployee(t, hr, "Bima "+tm.sfx, "TTP"+tm.sfx, map[string]any{"joinDate": hrDay(-400), "supervisorId": tm.mgrID})
	tm.aID, tm.aNo, tm.bID, tm.bNo = str(a["id"]), str(a["employeeNo"]), str(b["id"]), str(b["employeeNo"])
	tm.mgr = ttLink(t, roles[0], tm.mgrID)
	tm.a = ttLink(t, roles[1], tm.aID)
	tm.b = ttLink(t, roles[2], tm.bID)
	return tm
}

// ttAt is the instant of a local club date and time.
func ttAt(day, hhmm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", day+" "+hhmm, clubLoc(inst))
	if err != nil {
		panic(err)
	}
	return t
}

func ttRFC(day, hhmm string) string { return ttAt(day, hhmm).UTC().Format(time.RFC3339) }

// ttTemplate creates a shift template.
func ttTemplate(t *testing.T, c *Client, code, start, end, role string) string {
	t.Helper()
	body := map[string]any{"code": code, "name": "Shift " + code, "startTime": start, "endTime": end, "breakMinutes": 60}
	if role != "" {
		body["workforceRole"] = role
	}
	return idOf(c.Must(201, "POST", hrBase+"/shift-templates", body, "Idempotency-Key", newKey()))
}

// ttPublishedDay schedules (and publishes) one shift per employee on a day.
func ttPublishedDays(t *testing.T, hr *Client, tm ttTeam, tmpl, from, to string, emps ...string) string {
	t.Helper()
	sch := hr.Must(201, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": from, "periodEnd": to},
		"Idempotency-Key", newKey()).JSON()
	var as []map[string]any
	f, _ := time.Parse("2006-01-02", from)
	l, _ := time.Parse("2006-01-02", to)
	for d := f; !d.After(l); d = d.AddDate(0, 0, 1) {
		for _, e := range emps {
			as = append(as, map[string]any{"employeeId": e, "workDate": d.Format("2006-01-02"), "shiftTemplateId": tmpl})
		}
	}
	hr.Must(200, "POST", hrBase+"/schedules/"+str(sch["id"])+":assign", map[string]any{"assignments": as})
	hr.Must(200, "POST", hrBase+"/schedules/"+str(sch["id"])+":publish", map[string]any{})
	return str(sch["id"])
}

func ttDay(t *testing.T, c *Client, emp, day string) map[string]any {
	t.Helper()
	for _, d := range c.Must(200, "GET", hrBase+"/attendance-days?from="+day+"&to="+day+"&employeeId="+emp, nil).Items() {
		if strings.HasPrefix(str(d["workDate"]), day) {
			return d
		}
	}
	return nil
}

func ttCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	sysQueryRow(t, inst, sql, args, &n)
	return n
}

// ── EP-06 Shift Scheduling ────────────────────────────────────────────────

// FR-SCH-01–06 and G5: templates, roster with copy and pattern, validation
// (coverage, certification, hours, rest days, leave), publish with
// notification and event, changes after publishing, shift swap with the
// colleague's acceptance and the manager's approval.
func TestP5HRTimeSchedules(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	loc := clubLoc(inst)
	day := ttTemplate(t, hr, "TD"+tm.sfx, "08:00", "17:00", "")
	night := ttTemplate(t, hr, "TN"+tm.sfx, "22:00", "06:00", "")
	tmpl := hr.Must(200, "GET", hrBase+"/shift-templates/"+night, nil).JSON()
	if tmpl["crossesMidnight"] != true || dec(tmpl["workMinutes"]).IntPart() != 420 {
		t.Fatalf("night template: %v", tmpl)
	}
	hr.Must(422, "POST", hrBase+"/shift-templates", map[string]any{"code": "TX" + tm.sfx, "name": "Bad", "startTime": "08:00", "endTime": "08:00"})
	hr.Must(201, "POST", hrBase+"/staffing-requirements", map[string]any{"orgUnitId": tm.unitID, "positionId": tm.posID, "shiftTemplateId": day,
		"minStaff": 3}, "Idempotency-Key", newKey())

	monday := nextWeekday(loc, time.Monday, 14)
	w1, w2 := monday.Format("2006-01-02"), monday.AddDate(0, 0, 6).Format("2006-01-02")
	// department heads schedule only the units they head
	roleUser(t, inst, "outlet_manager").Must(403, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": w1})
	sch := tm.mgr.Must(201, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": w1}, "Idempotency-Key", newKey()).JSON()
	sid := str(sch["id"])
	if sch["status"] != "draft" || !strings.HasPrefix(str(sch["periodEnd"]), w2) || len(sch["employees"].([]any)) != 3 {
		t.Fatalf("schedule: %v", sch)
	}
	tm.mgr.Must(409, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": monday.AddDate(0, 0, 3).Format("2006-01-02")})
	// FR-SCH-02: repeating pattern (5 days on, 2 off): A Monday–Friday, B
	// Wednesday–Sunday
	pattern := []any{day, day, day, day, day, nil, nil}
	tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":apply-pattern", map[string]any{"employeeIds": []string{tm.aID}, "pattern": pattern})
	pat := tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":apply-pattern", map[string]any{"employeeIds": []string{tm.bID}, "pattern": pattern,
		"startDate": monday.AddDate(0, 0, 2).Format("2006-01-02")}).JSON()
	if pat["applied"] != float64(7) {
		t.Fatalf("pattern: %v", pat)
	}
	detail := pat["schedule"].(map[string]any)
	codes := map[string]bool{}
	for _, i := range detail["issues"].([]any) {
		codes[str(i.(map[string]any)["code"])] = true
	}
	if !codes["understaffed"] || codes["rest_days"] {
		t.Fatalf("coverage warnings: %v", detail["issues"])
	}
	cov := detail["coverage"].([]any)
	if len(cov) != 7 || cov[0].(map[string]any)["assigned"] != float64(1) || cov[0].(map[string]any)["short"] != float64(2) {
		t.Fatalf("coverage: %v", cov)
	}
	// G5 / FR-TRC-03: a lifeguard with an expired certificate is never scheduled
	pool := hrID(t, "shift_templates", "POOL-AM")
	hr.Must(409, "POST", hrBase+"/schedules/"+sid+":assign", map[string]any{"assignments": []map[string]any{{"employeeId": hrEmployeeID(t, "EMP-00017"),
		"workDate": w1, "shiftTemplateId": pool}}})
	// a sixth shift breaks the rest days of a 5-day week: publishing is refused
	tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":assign", map[string]any{"assignments": []map[string]any{
		{"employeeId": tm.aID, "workDate": monday.AddDate(0, 0, 5).Format("2006-01-02"), "shiftTemplateId": day},
		{"employeeId": tm.mgrID, "workDate": w1, "shiftTemplateId": day}}})
	r := tm.mgr.Do("POST", hrBase+"/schedules/"+sid+":publish", map[string]any{})
	if r.Status != 409 || !strings.Contains(r.String(), "rest days") {
		t.Fatalf("publish with errors: %s", r.String())
	}
	tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":assign", map[string]any{"assignments": []map[string]any{
		{"employeeId": tm.aID, "workDate": monday.AddDate(0, 0, 5).Format("2006-01-02"), "off": true},
		{"employeeId": tm.aID, "workDate": monday.AddDate(0, 0, 6).Format("2006-01-02"), "clear": true}}})
	tm.mgr.Must(200, "PATCH", hrBase+"/schedules/"+sid, map[string]any{"notes": "Banquet week"})
	pub := tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":publish", map[string]any{}).JSON()
	if pub["status"] != "published" || pub["version"] != float64(1) {
		t.Fatalf("published: %v", pub)
	}
	if hrEvents(t, hris.EventSchedulePublished, sid) != 1 {
		t.Fatal("hris.schedule_published")
	}
	waitFor(t, 20*time.Second, "schedule notification (FR-SCH-05)", func() bool {
		return ttCount(t, `SELECT count(*) FROM platform.notifications n JOIN platform.users u ON u.id = n.user_id
			WHERE u.email = 'role.kitchen_staff@matrix.test' AND n.event_code = 'hris.schedule_published'`) > 0
	})
	tm.mgr.Must(409, "POST", hrBase+"/schedules/"+sid+":cancel", map[string]any{})
	// FR-ESS-02 / FR-SCH-07: My Schedule (cached offline by the app)
	my := tm.a.Must(200, "GET", "/api/v1/ess/schedule?from="+w1+"&to="+w2, nil).JSON()
	if len(my["assignments"].([]any)) != 6 || len(my["colleagues"].([]any)) < 2 {
		t.Fatalf("my schedule: %v", my)
	}
	// a change after publishing notifies the employee and republishes
	tm.mgr.Must(200, "POST", hrBase+"/schedules/"+sid+":assign", map[string]any{"assignments": []map[string]any{
		{"employeeId": tm.bID, "workDate": monday.AddDate(0, 0, 1).Format("2006-01-02"), "shiftTemplateId": night}}})
	if hrEvents(t, hris.EventSchedulePublished, sid) != 2 {
		t.Fatal("changes republish hris.schedule_published")
	}
	// copy week into the following week
	nw := monday.AddDate(0, 0, 7)
	s2 := hr.Must(201, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": nw.Format("2006-01-02")},
		"Idempotency-Key", newKey()).JSON()
	cp := hr.Must(200, "POST", hrBase+"/schedules/"+str(s2["id"])+":copy", map[string]any{}).JSON()
	if cp["applied"].(float64) < 12 {
		t.Fatalf("copy week: %v", cp)
	}
	s3 := hr.Must(201, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": tm.unitID, "periodStart": nw.AddDate(0, 0, 7).Format("2006-01-02"),
		"copyFromScheduleId": str(s2["id"])}, "Idempotency-Key", newKey()).JSON()
	if s3["shifts"].(float64) < 12 {
		t.Fatalf("create with copy: %v", s3)
	}
	if c := hr.Must(200, "POST", hrBase+"/schedules/"+str(s3["id"])+":cancel", map[string]any{}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel draft: %v", c)
	}
	list := hr.Must(200, "GET", hrBase+"/schedules?orgUnitId="+tm.unitID+"&from="+w1, nil).Items()
	if len(list) != 3 {
		t.Fatalf("schedules: %v", list)
	}
	// FR-SCH-04: operational demand as reference
	if dem := hr.Must(200, "GET", hrBase+"/staffing-demand?from="+w1+"&to="+w2, nil).Items(); len(dem) != 7 || dem[0]["values"] == nil {
		t.Fatalf("demand: %v", dem)
	}

	// FR-SCH-06: A asks B to take Monday and takes B's Saturday
	var aWed, bSat, aMon string
	for _, x := range tm.mgr.Must(200, "GET", hrBase+"/schedules/"+sid, nil).JSON()["assignments"].([]any) {
		a := x.(map[string]any)
		d := str(a["workDate"])[:10]
		switch {
		case a["employeeId"] == tm.aID && d == monday.AddDate(0, 0, 2).Format("2006-01-02"):
			aWed = str(a["id"])
		case a["employeeId"] == tm.bID && d == monday.AddDate(0, 0, 5).Format("2006-01-02"):
			bSat = str(a["id"])
		case a["employeeId"] == tm.aID && d == w1:
			aMon = str(a["id"])
		}
	}
	// B works Wednesday already: a cover is refused
	tm.a.Must(409, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aWed, "counterpartEmployeeId": tm.bID, "reason": "Doctor"})
	sw := tm.a.Must(201, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aMon, "counterpartEmployeeId": tm.bID,
		"counterpartAssignmentId": bSat, "reason": "Family event"}, "Idempotency-Key", newKey()).JSON()
	if sw["status"] != "requested" {
		t.Fatalf("swap: %v", sw)
	}
	tm.mgr.Must(404, "POST", "/api/v1/ess/shift-swaps/"+str(sw["id"])+":accept", map[string]any{})
	acc := tm.b.Must(200, "POST", "/api/v1/ess/shift-swaps/"+str(sw["id"])+":accept", map[string]any{}).JSON()
	if acc["status"] != "submitted" {
		t.Fatalf("accepted: %v", acc)
	}
	pend := tm.mgr.Must(200, "GET", "/api/v1/ess/approvals", nil).Items()
	if len(pend) != 1 || pend[0]["kind"] != "shift_swap" || pend[0]["direct"] != true {
		t.Fatalf("manager approvals: %v", pend)
	}
	tm.a.Must(403, "POST", "/api/v1/ess/approvals/shift_swap/"+str(sw["id"])+":approve", map[string]any{})
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/shift_swap/"+str(sw["id"])+":approve", map[string]any{})
	if got := tm.a.Must(200, "GET", "/api/v1/ess/shift-swaps", nil).Items()[0]; got["status"] != "approved" {
		t.Fatalf("swap approved: %v", got)
	}
	var who string
	sysQueryRow(t, inst, `SELECT employee_id::text FROM hris.shift_assignments WHERE id = $1`, []any{aMon}, &who)
	if who != tm.bID {
		t.Fatal("the shift moved to the colleague")
	}
	// declined and withdrawn swaps; HR approves / rejects (B now works A's
	// Monday, A works B's Saturday)
	tm.a.Must(422, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aMon, "counterpartEmployeeId": tm.bID, "reason": "not mine"})
	sw2 := tm.a.Must(201, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": bSat, "counterpartEmployeeId": tm.bID, "reason": "x"},
		"Idempotency-Key", newKey()).JSON()
	if dec := tm.b.Must(200, "POST", "/api/v1/ess/shift-swaps/"+str(sw2["id"])+":decline", map[string]any{"note": "Busy"}).JSON(); dec["status"] != "declined" {
		t.Fatalf("declined: %v", dec)
	}
	sw3 := tm.b.Must(201, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aMon, "counterpartEmployeeId": tm.aID, "reason": "Exam"},
		"Idempotency-Key", newKey()).JSON()
	if cn := tm.b.Must(200, "POST", "/api/v1/ess/shift-swaps/"+str(sw3["id"])+":cancel", map[string]any{}).JSON(); cn["status"] != "cancelled" {
		t.Fatalf("withdrawn: %v", cn)
	}
	sw4 := tm.b.Must(201, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aMon, "counterpartEmployeeId": tm.aID, "reason": "Exam"},
		"Idempotency-Key", newKey()).JSON()
	tm.a.Must(200, "POST", "/api/v1/ess/shift-swaps/"+str(sw4["id"])+":accept", map[string]any{})
	hr.Must(422, "POST", hrBase+"/shift-swaps/"+str(sw4["id"])+":reject", map[string]any{})
	if rj := hr.Must(200, "POST", hrBase+"/shift-swaps/"+str(sw4["id"])+":reject", map[string]any{"note": "Short staffed"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("HR reject: %v", rj)
	}
	sw5 := tm.b.Must(201, "POST", "/api/v1/ess/shift-swaps", map[string]any{"assignmentId": aMon, "counterpartEmployeeId": tm.aID, "reason": "Exam"},
		"Idempotency-Key", newKey()).JSON()
	tm.a.Must(200, "POST", "/api/v1/ess/shift-swaps/"+str(sw5["id"])+":accept", map[string]any{})
	if ap := hr.Must(200, "POST", hrBase+"/shift-swaps/"+str(sw5["id"])+":approve", map[string]any{}).JSON(); ap["status"] != "approved" {
		t.Fatalf("HR approve: %v", ap)
	}
	if all := hr.Must(200, "GET", hrBase+"/shift-swaps", nil).Items(); len(all) < 5 {
		t.Fatalf("swaps: %d", len(all))
	}
	// Team Schedule (FR-ESS-05)
	ts := tm.mgr.Must(200, "GET", "/api/v1/ess/team-schedule?from="+w1+"&to="+w2, nil).JSON()
	if len(ts["assignments"].([]any)) < 10 {
		t.Fatalf("team schedule: %v", ts)
	}
	tm.a.Must(403, "GET", "/api/v1/ess/team-schedule", nil)
}

// ── EP-07 Attendance ──────────────────────────────────────────────────────

// FR-ATT-01–07, EP-07 AC, EP-25/26: day statuses from clock events vs the
// published shift, corrections with approval, mobile GPS with geofence (out
// of the area → the supervisor's review queue), the Attendance Kiosk with
// PIN / QR on a registered device and its idempotent offline sync, mock and
// bridge-agent biometric devices without stored templates.
func TestP5HRTimeAttendance(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	d1, d2, d3 := hrDay(-1), hrDay(-2), hrDay(-3)
	tmpl := ttTemplate(t, hr, "TA"+tm.sfx, "08:00", "17:00", "")
	ttPublishedDays(t, hr, tm, tmpl, d3, d1, tm.aID, tm.bID)
	// FR-ATT-03: published shifts without clock-in are Absent once the day is over
	if d := ttDay(t, hr, tm.bID, d2); d == nil || d["status"] != "absent" {
		t.Fatalf("absent: %v", d)
	}
	hr.Must(422, "POST", hrBase+"/attendance:clock", map[string]any{"employeeId": tm.aID, "direction": "in", "occurredAt": ttRFC(d2, "08:25")})
	hr.Must(201, "POST", hrBase+"/attendance:clock", map[string]any{"employeeId": tm.aID, "direction": "in", "occurredAt": ttRFC(d2, "08:25"),
		"note": "Device down"}, "Idempotency-Key", newKey())
	out := hr.Must(201, "POST", hrBase+"/attendance:clock", map[string]any{"employeeId": tm.aID, "direction": "out", "occurredAt": ttRFC(d2, "17:05"),
		"note": "Device down"}, "Idempotency-Key", newKey()).JSON()
	if day := out["day"].(map[string]any); day["status"] != "late" || day["lateMinutes"] != float64(25) || day["workedMinutes"] != float64(460) {
		t.Fatalf("late day: %v", day)
	}
	if hrEvents(t, hris.EventAttendanceRecorded, str(out["event"].(map[string]any)["id"])) != 1 {
		t.Fatal("hris.attendance_recorded")
	}
	rc := hr.Must(200, "POST", hrBase+"/attendance:recalculate", map[string]any{"from": d3, "to": d1, "employeeId": tm.aID}).JSON()
	if rc["days"] != float64(3) {
		t.Fatalf("recalculate: %v", rc)
	}

	// FR-ATT-04: corrections with reason and approval
	tm.b.Must(422, "POST", "/api/v1/ess/attendance-corrections", map[string]any{"workDate": d2, "clockIn": "08:00", "clockOut": "17:00"})
	tm.b.Must(422, "POST", "/api/v1/ess/attendance-corrections", map[string]any{"workDate": hrDay(-30), "clockIn": "08:00", "reason": "late"})
	co := tm.b.Must(201, "POST", "/api/v1/ess/attendance-corrections", map[string]any{"workDate": d2, "clockIn": "08:00", "clockOut": "17:00",
		"reason": "Forgot to clock"}, "Idempotency-Key", newKey()).JSON()
	tm.b.Must(409, "POST", "/api/v1/ess/attendance-corrections", map[string]any{"workDate": d2, "clockIn": "08:00", "reason": "again"})
	if co["status"] != "submitted" || len(co["approvals"].([]any)) != 1 {
		t.Fatalf("correction: %v", co)
	}
	tm.mgr.Must(422, "POST", "/api/v1/ess/approvals/attendance_correction/"+str(co["id"])+":reject", map[string]any{})
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/attendance_correction/"+str(co["id"])+":approve", map[string]any{"note": "Confirmed by CCTV"})
	if d := ttDay(t, hr, tm.bID, d2); d["status"] != "present" || !strings.Contains(fmt.Sprint(d["flags"]), "corrected") {
		t.Fatalf("corrected day: %v", d)
	}
	cw := tm.b.Must(201, "POST", "/api/v1/ess/attendance-corrections", map[string]any{"workDate": d1, "clockIn": "08:00", "reason": "Forgot"},
		"Idempotency-Key", newKey()).JSON()
	if c := tm.b.Must(200, "POST", "/api/v1/ess/attendance-corrections/"+str(cw["id"])+":cancel", map[string]any{}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("withdrawn: %v", c)
	}
	hc := hr.Must(201, "POST", hrBase+"/attendance-corrections", map[string]any{"employeeId": tm.aID, "workDate": d3, "clockIn": "07:58",
		"clockOut": "17:01", "reason": "Badge reader fault"}, "Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/attendance-corrections/"+str(hc["id"])+":approve", map[string]any{})
	if d := ttDay(t, hr, tm.aID, d3); d["status"] != "present" {
		t.Fatalf("HR correction: %v", d)
	}
	hj := hr.Must(201, "POST", hrBase+"/attendance-corrections", map[string]any{"employeeId": tm.bID, "workDate": d1, "clockOut": "17:00",
		"reason": "?"}, "Idempotency-Key", newKey()).JSON()
	if rj := hr.Must(200, "POST", hrBase+"/attendance-corrections/"+str(hj["id"])+":reject", map[string]any{"note": "No evidence"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("rejected: %v", rj)
	}
	if l := hr.Must(200, "GET", hrBase+"/attendance-corrections?status=approved", nil).Items(); len(l) < 2 {
		t.Fatalf("corrections: %v", l)
	}

	// FR-ATT-05 / EP-07 AC: 400 m from a 150 m geofence → Out of Area, in
	// the supervisor's review queue
	lat, lng := -6.3012, 106.6510
	hr.Must(201, "POST", hrBase+"/geofences", map[string]any{"code": "GF" + tm.sfx, "name": "Field " + tm.sfx, "latitude": fmt.Sprint(lat),
		"longitude": fmt.Sprint(lng), "radiusMeters": 150, "orgUnitCodes": []string{tm.unitCode}}, "Idempotency-Key", newKey())
	far := tm.a.Must(200, "POST", "/api/v1/ess/attendance:clock", map[string]any{"latitude": lat + 400.0/111320.0, "longitude": lng, "accuracyMeters": 12,
		"clientEventId": uuid.NewString()}).JSON()
	fe := far["event"].(map[string]any)
	if fe["reviewStatus"] != "pending" || !strings.Contains(fmt.Sprint(fe["flags"]), "out_of_area") || dec(fe["distanceMeters"]).IntPart() < 390 {
		t.Fatalf("out of area: %v", far)
	}
	ta := tm.mgr.Must(200, "GET", "/api/v1/ess/team-attendance", nil).JSON()
	if len(ta["pendingReview"].([]any)) != 1 {
		t.Fatalf("review queue: %v", ta)
	}
	tm.mgr.Must(200, "POST", "/api/v1/ess/attendance-events/"+str(fe["id"])+":review", map[string]any{"decision": "accepted", "note": "Client visit"})
	near := tm.a.Must(200, "POST", "/api/v1/ess/attendance:clock", map[string]any{"latitude": lat, "longitude": lng + 0.0005, "accuracyMeters": 8}).JSON()
	if ne := near["event"].(map[string]any); ne["reviewStatus"] != "not_required" || ne["direction"] != "out" || ne["withinGeofence"] != true {
		t.Fatalf("inside: %v", near)
	}
	bf := tm.b.Must(200, "POST", "/api/v1/ess/attendance:clock", map[string]any{"latitude": lat + 0.01, "longitude": lng}).JSON()["event"].(map[string]any)
	hr.Must(422, "POST", hrBase+"/attendance-events/"+str(bf["id"])+":review", map[string]any{"decision": "rejected"})
	hr.Must(200, "POST", hrBase+"/attendance-events/"+str(bf["id"])+":review", map[string]any{"decision": "rejected", "note": "Not at work"})
	if ev := hr.Must(200, "GET", hrBase+"/attendance-events?employeeId="+tm.bID+"&review=rejected", nil).Items(); len(ev) != 1 {
		t.Fatalf("rejected events: %v", ev)
	}
	tm.a.Must(422, "POST", "/api/v1/ess/attendance:clock", map[string]any{}) // location required
	// F&B staff clock in at the kiosk or a device, not with mobile GPS
	login(t, inst, "employee@demo.oneclub.id", demoPassword).Must(403, "POST", "/api/v1/ess/attendance:clock", map[string]any{"latitude": lat,
		"longitude": lng})
	cs := tm.a.Must(200, "GET", "/api/v1/ess/clock-status", nil).JSON()
	if cs["mobileGps"] != true || len(cs["geofences"].([]any)) == 0 || cs["lastEvent"] == nil {
		t.Fatalf("clock status: %v", cs)
	}

	// FR-ATT-02: Attendance Kiosk on a registered device with PIN / QR
	dev := sa.Must(201, "POST", "/api/v1/platform/devices", map[string]any{"name": "Kiosk " + tm.sfx, "deviceType": "kiosk"}).JSON()
	hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "K" + tm.sfx, "name": "Kiosk " + tm.sfx, "deviceKind": "kiosk",
		"platformDeviceId": dev["id"]}, "Idempotency-Key", newKey())
	kiosk := anon(t, inst)
	kiosk.Must(200, "POST", "/api/v1/auth/device-login", map[string]any{"deviceToken": dev["deviceToken"], "email": "dept.head@demo.oneclub.id",
		"pin": app.DemoPIN})
	if info := kiosk.Must(200, "GET", hrBase+"/attendance/kiosk", nil).JSON(); info["code"] != "K"+tm.sfx {
		t.Fatalf("kiosk: %v", info)
	}
	tm.mgr.Must(403, "GET", hrBase+"/attendance/kiosk", nil) // not a registered device
	tm.a.Must(422, "POST", "/api/v1/ess/attendance-pin", map[string]any{"pin": "123456"})
	tm.a.Must(200, "POST", "/api/v1/ess/attendance-pin", map[string]any{"pin": "135790"})
	tm.a.Must(422, "POST", "/api/v1/ess/attendance-pin", map[string]any{"pin": "246802", "currentPin": "000000"})
	kiosk.Must(401, "POST", hrBase+"/attendance/kiosk:clock", map[string]any{"employeeNo": tm.aNo, "pin": "111111"})
	kp := kiosk.Must(200, "POST", hrBase+"/attendance/kiosk:clock", map[string]any{"employeeNo": tm.aNo, "pin": "135790"}).JSON()
	if kp["event"].(map[string]any)["method"] != "kiosk_pin" {
		t.Fatalf("kiosk PIN: %v", kp)
	}
	qr := tm.b.Must(200, "GET", "/api/v1/ess/attendance-qr", nil).JSON()
	kiosk.Must(422, "POST", hrBase+"/attendance/kiosk:clock", map[string]any{"qrToken": str(qr["token"]) + "x"})
	if kq := kiosk.Must(200, "POST", hrBase+"/attendance/kiosk:clock", map[string]any{"qrToken": qr["token"]}).JSON(); kq["event"].(map[string]any)["employeeId"] != tm.bID {
		t.Fatalf("kiosk QR: %v", kq)
	}
	// FR-ATT-07 / EP-07 AC: an offline event sent twice is recorded once
	cid := uuid.NewString()
	ev := map[string]any{"clientEventId": cid, "employeeNo": tm.aNo, "pin": "135790", "occurredAt": time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
		"direction": "in"}
	res := kiosk.Must(200, "POST", hrBase+"/attendance/kiosk:sync", map[string]any{"events": []any{ev, ev}}).JSON()["results"].([]any)
	if res[0].(map[string]any)["status"] != "accepted" || res[1].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("offline sync: %v", res)
	}
	if again := kiosk.Must(200, "POST", hrBase+"/attendance/kiosk:sync", map[string]any{"events": []any{ev}}).JSON()["results"].([]any); again[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("resent: %v", again)
	}
	if n := ttCount(t, `SELECT count(*) FROM hris.attendance_events WHERE client_event_id = $1`, cid); n != 1 {
		t.Fatalf("recorded %d times", n)
	}
	// the same through the offline queue of the ops shell (platform sync)
	item := map[string]any{"id": uuid.NewString(), "action": "hris.kiosk_clock", "payload": map[string]any{"clientEventId": uuid.NewString(),
		"qrToken": tm.b.Must(200, "GET", "/api/v1/ess/attendance-qr", nil).JSON()["token"]}}
	for _, want := range []string{"accepted", "duplicate"} {
		if r := kiosk.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()["results"].([]any)[0].(map[string]any); r["status"] != want {
			t.Fatalf("platform sync %s: %v", want, r)
		}
	}
	esync := map[string]any{"id": uuid.NewString(), "action": "hris.ess_clock", "payload": map[string]any{"clientEventId": uuid.NewString(),
		"latitude": lat, "longitude": lng, "occurredAt": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)}}
	if r := tm.a.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{esync}}).JSON()["results"].([]any)[0].(map[string]any); r["status"] != "accepted" {
		t.Fatalf("ESS offline clock: %v", r)
	}

	// FR-ATT-01/06, FR-INT-P5-01: biometric devices (mock trial adapter and a
	// bridge agent device); templates stay on the device, consent is required
	mock := idOf(hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "M" + tm.sfx, "name": "Mock " + tm.sfx, "deviceKind": "biometric"},
		"Idempotency-Key", newKey()))
	hr.Must(422, "POST", hrBase+"/attendance-profiles/"+tm.aID+":consent", map[string]any{"consent": true})
	hr.Must(200, "POST", hrBase+"/attendance-profiles/"+tm.aID+":consent", map[string]any{"consent": true, "signedOn": hrDay(0)})
	if s := hr.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":sync-employees", map[string]any{}).JSON(); s["status"] != "synced" || s["users"].(float64) < 1 {
		t.Fatalf("mock sync: %v", s)
	}
	hr.Must(409, "POST", hrBase+"/attendance-devices/"+mock+":simulate", map[string]any{"employeeId": tm.bID})
	sim := hr.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":simulate", map[string]any{"employeeId": tm.aID, "method": "fingerprint",
		"eventId": "E1"}).JSON()
	if sim["event"].(map[string]any)["method"] != "fingerprint" || sim["event"].(map[string]any)["deviceName"] != "Mock "+tm.sfx {
		t.Fatalf("simulate: %v", sim)
	}
	if again := hr.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":simulate", map[string]any{"employeeId": tm.aID, "method": "fingerprint",
		"eventId": "E1"}).JSON(); again["duplicate"] != true {
		t.Fatalf("device resend: %v", again)
	}
	hr.Must(200, "POST", hrBase+"/attendance-profiles/"+tm.bID+":set-pin", map[string]any{"pin": "975310"})
	var aNo string
	for _, p := range hr.Must(200, "GET", hrBase+"/attendance-profiles?q="+tm.sfx, nil).Items() {
		if p["employeeId"] == tm.aID {
			aNo = str(p["deviceUserNo"])
			if p["biometricConsent"] != true || p["enrolledAt"] == nil {
				t.Fatalf("profile: %v", p)
			}
		}
	}
	agent := sa.Must(201, "POST", "/api/v1/platform/bridge-agents", map[string]any{"name": "Agent " + tm.sfx}).JSON()
	// The MAIN property keeps the agent of TestBridgeAgent alone: remove this one afterwards.
	t.Cleanup(func() {
		ctx := reqctx.WithProperty(dbtx.System(context.Background()), inst.Main)
		if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
			for _, q := range []string{`UPDATE hris.attendance_devices SET bridge_agent_id = NULL WHERE bridge_agent_id = $1`,
				`DELETE FROM platform.bridge_commands WHERE agent_id = $1`, `DELETE FROM platform.bridge_agents WHERE id = $1`} {
				if _, err := tx.Exec(ctx, q, agent["id"]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Errorf("remove bridge agent: %v", err)
		}
	})
	zk := idOf(hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "Z" + tm.sfx, "name": "ZK " + tm.sfx, "deviceKind": "biometric",
		"vendor": "zkteco", "serialNo": "SN" + tm.sfx, "bridgeAgentId": agent["id"]}, "Idempotency-Key", newKey()))
	hr.Must(409, "POST", hrBase+"/attendance-devices/"+zk+":sync-employees", map[string]any{}) // agent offline
	bridge := anon(t, inst)
	bridge.Bearer = str(agent["token"])
	bridge.Must(200, "POST", "/api/v1/bridge/heartbeat", map[string]any{"agentVersion": "1.0", "hardware": []map[string]any{{"device": "Z" + tm.sfx,
		"kind": "attendance_terminal"}}})
	if q := hr.Must(200, "POST", hrBase+"/attendance-devices/"+zk+":sync-employees", map[string]any{}).JSON(); q["status"] != "queued" || q["commandId"] == nil {
		t.Fatalf("bridge sync: %v", q)
	}
	push := map[string]any{"deviceSerial": "SN" + tm.sfx, "events": []map[string]any{
		{"eventId": "Z1", "deviceUserNo": aNo, "occurredAt": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339), "method": "face_recognition"},
		{"eventId": "Z1", "deviceUserNo": aNo, "occurredAt": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339), "method": "face_recognition"},
		{"eventId": "Z2", "deviceUserNo": "999999", "occurredAt": time.Now().Add(-4 * time.Minute).UTC().Format(time.RFC3339), "method": "fingerprint"}}}
	pr := bridge.Must(200, "POST", "/api/v1/bridge/hris/attendance-events", push).JSON()["results"].([]any)
	if pr[0].(map[string]any)["status"] != "accepted" || pr[1].(map[string]any)["status"] != "duplicate" || pr[2].(map[string]any)["status"] != "rejected" {
		t.Fatalf("bridge push: %v", pr)
	}
	anon(t, inst).Must(401, "POST", "/api/v1/bridge/hris/attendance-events", push)
	if p := hr.Must(200, "POST", hrBase+"/attendance-profiles/"+tm.aID+":consent", map[string]any{"consent": false}).JSON(); p["biometricConsent"] != false ||
		p["removalRequestedAt"] == nil {
		t.Fatalf("consent withdrawn: %v", p)
	}
	// FR-ATT-06: no column holds biometric templates
	if n := ttCount(t, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'hris' AND (column_name LIKE '%template%'
		AND column_name <> 'shift_template_id' OR column_name LIKE '%biometric_data%')`); n != 0 {
		t.Fatal("no biometric template is stored")
	}
	// history
	hist := tm.a.Must(200, "GET", "/api/v1/ess/attendance?from="+d3+"&to="+hrDay(0), nil).JSON()
	if len(hist["days"].([]any)) < 3 || len(hist["events"].([]any)) < 5 {
		t.Fatalf("attendance history: %v", hist)
	}
	if days := hr.Must(200, "GET", hrBase+"/attendance-days?from="+d3+"&to="+d1+"&orgUnitId="+tm.unitID, nil).Items(); len(days) != 6 {
		t.Fatalf("attendance days: %v", days)
	}
}

// ttPastWorkday is the most recent day (at least minBack days ago) that is
// a Monday–Friday and no holiday.
func ttPastWorkday(t *testing.T, minBack int) string {
	t.Helper()
	for back := minBack; back < minBack+14; back++ {
		d := time.Now().In(clubLoc(inst)).AddDate(0, 0, -back)
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		day := d.Format("2006-01-02")
		if ttCount(t, `SELECT count(*) FROM hris.holidays WHERE holiday_date = $1::date AND archived_at IS NULL AND property_id = $2`, day, inst.Main)+
			ttCount(t, `SELECT count(*) FROM platform.calendar_days WHERE day = $1::date AND kind = 'public_holiday'`, day) == 0 {
			return day
		}
	}
	t.Fatal("no past workday")
	return ""
}

// ttPolicy versions a HR policy from its defaults with changes (the
// version is deactivated at the end of the test).
func ttPolicy(t *testing.T, code, category string, def any, change map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(def)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	for k, x := range change {
		v[k] = x
	}
	hrPolicy(t, superAdmin(t, inst), code, category, v)
}

// ── EP-08 Leave ───────────────────────────────────────────────────────────

// FR-LVE-01–03, §16 #3, EP-28 FR-MIG-P5-02: accrual (12 days after 12
// months), carry-over (max 6, lapsing 31 March), requests from ESS with the
// policy's approval levels and the approval engine workflow, balance
// reserved / reduced / restored, leave calendar with schedule conflicts,
// collective leave, HR leave on behalf (unpaid → payroll), import.
func TestP5HRTimeLeave(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	loc := clubLoc(inst)
	now := time.Now().In(loc)
	year := now.Year()

	// last year's balance of A (9 left) → this year's carry-over of 6
	adj := hr.Must(200, "POST", hrBase+"/leave-balances:adjust", map[string]any{"employeeId": tm.aID, "leaveType": "ANNUAL", "year": year - 1,
		"days": "-3", "note": "Migrated usage"}).JSON()
	if adj["available"] != "9" {
		t.Fatalf("adjust: %v", adj)
	}
	hr.Must(422, "POST", hrBase+"/leave-balances:adjust", map[string]any{"employeeId": tm.aID, "leaveType": "SICK", "year": year, "days": "1", "note": "x"})
	acc := hr.Must(200, "POST", hrBase+"/leave-balances:accrue", map[string]any{"employeeId": tm.aID}).JSON()
	if acc["granted"] != float64(1) || acc["carriedOver"] != float64(1) {
		t.Fatalf("accrual: %v", acc)
	}
	bal := hr.Must(200, "GET", hrBase+"/leave-balances?employeeId="+tm.aID+fmt.Sprintf("&year=%d", year), nil).Items()
	expired := "0"
	if now.Format("01-02") > "03-31" {
		expired = "6" // the carry-over lapsed on 31 March
	}
	if len(bal) != 1 || bal[0]["entitled"] != "12" || bal[0]["carriedOver"] != "6" || bal[0]["carriedExpired"] != expired {
		t.Fatalf("balance with carry-over: %v", bal)
	}
	hr.Must(200, "POST", hrBase+"/leave-balances:accrue", map[string]any{"employeeId": tm.bID})
	// leave types per employee (gender, eligibility)
	codes := func(c *Client) string {
		var out []string
		for _, x := range c.Must(200, "GET", "/api/v1/ess/leave-types", nil).Items() {
			out = append(out, str(x["code"]))
		}
		return strings.Join(out, ",")
	}
	if !strings.Contains(codes(tm.a), "MATERNITY") || strings.Contains(codes(tm.b), "MATERNITY") || !strings.Contains(codes(tm.b), "PATERNITY") {
		t.Fatalf("leave types: %s / %s", codes(tm.a), codes(tm.b))
	}
	if lt := hr.Must(200, "GET", hrBase+"/leave-types", nil).Items(); len(lt) < 12 {
		t.Fatalf("policy types: %v", lt)
	}

	// FR-LVE-02: B requests two days; the balance is reserved, then reduced
	mon := nextWeekday(loc, time.Monday, 7)
	m1, m2 := mon.Format("2006-01-02"), mon.AddDate(0, 0, 1).Format("2006-01-02")
	tm.b.Must(422, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": hrDay(1)})                                                  // notice
	tm.b.Must(422, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "MATERNITY", "startDate": m1})                                                     // gender
	tm.b.Must(422, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "SICK", "startDate": m1})                                                          // doctor's letter
	tm.b.Must(422, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": m1, "endDate": mon.AddDate(0, 0, 20).Format("2006-01-02")}) // balance
	lv := tm.b.Must(201, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": m1, "endDate": m2,
		"reason": "Family trip"}, "Idempotency-Key", newKey()).JSON()
	if lv["days"] != "2" || lv["status"] != "submitted" {
		t.Fatalf("leave: %v", lv)
	}
	tm.b.Must(409, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": m2})
	if b := tm.b.Must(200, "GET", "/api/v1/ess/leave-balances", nil).Items(); b[0]["pending"] != "2" || b[0]["available"] != "10" {
		t.Fatalf("reserved: %v", b)
	}
	if cal := tm.mgr.Must(200, "GET", "/api/v1/ess/team-calendar?from="+m1+"&to="+m2, nil).Items(); len(cal) != 1 {
		t.Fatalf("team calendar: %v", cal)
	}
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/leave/"+str(lv["id"])+":approve", map[string]any{})
	got := hr.Must(200, "GET", hrBase+"/leave-requests/"+str(lv["id"]), nil).JSON()
	if got["status"] != "approved" || len(got["approvals"].([]any)) != 2 {
		t.Fatalf("approved: %v", got)
	}
	if hrEvents(t, hris.EventLeaveApproved, str(lv["id"])) != 1 {
		t.Fatal("hris.leave_approved")
	}
	if b := tm.b.Must(200, "GET", "/api/v1/ess/leave-balances", nil).Items(); b[0]["used"] != "2" || b[0]["available"] != "10" {
		t.Fatalf("reduced: %v", b)
	}
	if cal := hr.Must(200, "GET", hrBase+"/leave-calendar?from="+m1+"&to="+m2+"&orgUnitId="+tm.unitID, nil).Items(); len(cal) != 1 {
		t.Fatalf("leave calendar: %v", cal)
	}
	// the employee cancels leave that has not started: the balance returns
	if c := tm.b.Must(200, "POST", "/api/v1/ess/leave-requests/"+str(lv["id"])+":cancel", map[string]any{}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", c)
	}
	if b := tm.b.Must(200, "GET", "/api/v1/ess/leave-balances", nil).Items(); b[0]["used"] != "0" {
		t.Fatalf("restored: %v", b)
	}
	wd := tm.b.Must(201, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": m1}, "Idempotency-Key", newKey()).JSON()
	if w := tm.b.Must(200, "POST", "/api/v1/ess/leave-requests/"+str(wd["id"])+":cancel", map[string]any{}).JSON(); w["status"] != "cancelled" {
		t.Fatalf("withdrawn: %v", w)
	}

	// approval berjenjang: supervisor, then HR, then the configured workflow
	ttPolicy(t, hris.LeavePolicyCode, hris.CategoryHRPolicies, hris.NewLeavePolicy(), map[string]any{"approvalLevels": []string{"supervisor", "hr"}})
	sa := superAdmin(t, inst)
	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "hris.leave_request", "name": "Annual leave " + tm.sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "HR Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "hr_manager"),
			"conditions": []map[string]any{{"attribute": "orgUnit", "operator": "eq", "value": tm.unitCode},
				{"attribute": "leaveType", "operator": "eq", "value": "ANNUAL"}}}}}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})
	ml := tm.b.Must(201, "POST", "/api/v1/ess/leave-requests", map[string]any{"leaveType": "ANNUAL", "startDate": m2, "halfDay": "am"},
		"Idempotency-Key", newKey()).JSON()
	if ml["days"] != "0.5" {
		t.Fatalf("half day: %v", ml)
	}
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/leave/"+str(ml["id"])+":approve", map[string]any{})
	tm.mgr.Must(403, "POST", "/api/v1/ess/approvals/leave/"+str(ml["id"])+":approve", map[string]any{}) // HR level
	hr.Must(200, "POST", hrBase+"/leave-requests/"+str(ml["id"])+":approve", map[string]any{})
	var rid string
	sysQueryRow(t, inst, `SELECT approval_request_id::text FROM hris.leave_requests WHERE id = $1`, []any{ml["id"]}, &rid)
	if s := hr.Must(200, "GET", hrBase+"/leave-requests/"+str(ml["id"]), nil).JSON(); s["status"] != "submitted" {
		t.Fatalf("waiting in the workflow: %v", s)
	}
	hr.Must(409, "POST", hrBase+"/leave-requests/"+str(ml["id"])+":approve", map[string]any{})
	roleUser(t, inst, "hr_manager").Must(200, "POST", "/api/v1/platform/approvals/"+rid+":approve", map[string]any{})
	if s := hr.Must(200, "GET", hrBase+"/leave-requests/"+str(ml["id"]), nil).JSON(); s["status"] != "approved" || len(s["approvals"].([]any)) != 3 {
		t.Fatalf("approved through the workflow: %v", s)
	}

	// HR records unpaid leave of a past day: payroll sees it (TimeSummaries)
	past := ttPastWorkday(t, 2)
	ul := hr.Must(201, "POST", hrBase+"/leave-requests", map[string]any{"employeeId": tm.aID, "leaveType": "UNPAID", "startDate": past,
		"reason": "Personal"}, "Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/leave-requests/"+str(ul["id"])+":approve", map[string]any{})
	hr.Must(200, "POST", hrBase+"/leave-requests/"+str(ul["id"])+":approve", map[string]any{})
	if d := ttDay(t, hr, tm.aID, past); d == nil || d["status"] != "on_leave" {
		t.Fatalf("leave day: %v", d)
	}
	sum := hr.Must(200, "GET", hrBase+"/time-summary?from="+past+"&to="+past+"&employeeId="+tm.aID, nil).Items()
	if len(sum) != 1 || sum[0]["unpaidLeaveDays"] != "1" {
		t.Fatalf("unpaid leave for payroll: %v", sum)
	}
	hr.Must(422, "POST", hrBase+"/leave-requests/"+str(ul["id"])+":cancel", map[string]any{})
	hr.Must(200, "POST", hrBase+"/leave-requests/"+str(ul["id"])+":cancel", map[string]any{"note": "Entered by mistake"})
	if d := ttDay(t, hr, tm.aID, past); d != nil && d["status"] == "on_leave" {
		t.Fatalf("leave reversed: %v", d)
	}
	rj := hr.Must(201, "POST", hrBase+"/leave-requests", map[string]any{"employeeId": tm.aID, "leaveType": "MARRIAGE", "startDate": m1},
		"Idempotency-Key", newKey()).JSON()
	if r := hr.Must(200, "POST", hrBase+"/leave-requests/"+str(rj["id"])+":reject", map[string]any{"note": "Bring the certificate"}).JSON(); r["status"] != "rejected" {
		t.Fatalf("rejected: %v", r)
	}
	if l := hr.Must(200, "GET", hrBase+"/leave-requests?orgUnitId="+tm.unitID, nil).Items(); len(l) < 5 {
		t.Fatalf("requests: %d", len(l))
	}

	// collective leave (cuti bersama) deducts annual leave once
	h := hr.Must(201, "POST", hrBase+"/holidays", map[string]any{"holidayDate": hrDay(0), "name": "Cuti Bersama " + tm.sfx, "kind": "collective_leave",
		"deductsAnnualLeave": true}, "Idempotency-Key", newKey()).JSON()
	t.Cleanup(func() { hr.Must(204, "DELETE", hrBase+"/holidays/"+str(h["id"]), nil) })
	hr.Must(422, "POST", hrBase+"/holidays", map[string]any{"holidayDate": hrDay(1), "name": "X", "kind": "public_holiday", "deductsAnnualLeave": true})
	if c := hr.Must(200, "POST", hrBase+"/leave-balances:accrue", map[string]any{"employeeId": tm.aID}).JSON(); c["collective"] != float64(1) {
		t.Fatalf("collective leave: %v", c)
	}
	if c := hr.Must(200, "POST", hrBase+"/leave-balances:accrue", map[string]any{"employeeId": tm.aID}).JSON(); c["collective"] != float64(0) {
		t.Fatalf("collective leave once: %v", c)
	}

	// EP-28 FR-MIG-P5-02: balances at cut-over (repeatable, dry run)
	csv := "employeeNo,leaveType,year,entitled,carriedOver,used,note\n" + tm.bNo + fmt.Sprintf(",ANNUAL,%d,12,2,1,cutover\n", year) +
		fmt.Sprintf("NOPE%s,ANNUAL,%d,12,0,0,\n", tm.sfx, year)
	dry := hr.Must(200, "POST", hrBase+"/leave-balances:import", map[string]any{"csv": csv, "dryRun": true}).JSON()
	if dry["updated"] != float64(1) || dry["failed"] != float64(1) {
		t.Fatalf("dry run: %v", dry)
	}
	rep := hr.Must(200, "POST", hrBase+"/leave-balances:import", map[string]any{"csv": csv}).JSON()
	if rep["updated"] != float64(1) || len(rep["issues"].([]any)) != 1 {
		t.Fatalf("import: %v", rep)
	}
	if b := hr.Must(200, "GET", hrBase+"/leave-balances?employeeId="+tm.bID, nil).Items(); b[0]["carriedOver"] != "2" || b[0]["used"] != "1" {
		t.Fatalf("imported balance: %v", b)
	}
	hr.Must(422, "POST", hrBase+"/leave-balances:import", map[string]any{"csv": "who\nx\n"})
	sys := reqctx.WithProperty(dbtx.System(context.Background()), inst.Main)
	cli, err := inst.App.Time.Module.ImportLeaveBalances(sys, inst.Main, strings.NewReader(csv), false)
	if err != nil || cli.Updated != 1 || cli.Failed != 1 {
		t.Fatalf("CLI import: %+v %v", cli, err)
	}
}

// ── EP-08 Overtime & Permission ──────────────────────────────────────────

// FR-OVT-01–04 and the EP-08 AC (Rp5,190,000 ÷ 173 = Rp30,000 / hour; 3 h
// on a workday = Rp45,000 + Rp120,000 = Rp165,000); payable hours from the
// attendance; limits; exceptions; permission excusing late arrival.
func TestP5HRTimeOvertime(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	hr.Must(201, "POST", hrBase+"/contracts", map[string]any{"employeeId": tm.aID, "contractType": "pkwtt", "startDate": hrDay(-800),
		"baseSalary": "5190000", "activate": true}, "Idempotency-Key", newKey())
	day := ttPastWorkday(t, 1)
	tmpl := ttTemplate(t, hr, "TO"+tm.sfx, "08:00", "17:00", "")
	ttPublishedDays(t, hr, tm, tmpl, day, day, tm.aID, tm.bID)
	clock := func(emp, dir, hhmm string) {
		hr.Must(201, "POST", hrBase+"/attendance:clock", map[string]any{"employeeId": emp, "direction": dir, "occurredAt": ttRFC(day, hhmm),
			"note": "Test clock"}, "Idempotency-Key", newKey())
	}
	clock(tm.aID, "in", "07:55")
	clock(tm.aID, "out", "20:02")
	clock(tm.bID, "in", "09:20")
	clock(tm.bID, "out", "19:10")
	// FR-OVT-04: overtime without approval is an exception for the manager
	if ex := hr.Must(200, "GET", hrBase+"/overtime-exceptions?from="+day+"&to="+day, nil).Items(); len(ex) < 2 {
		t.Fatalf("exceptions: %v", ex)
	}
	if ta := tm.mgr.Must(200, "GET", "/api/v1/ess/team-attendance?date="+day, nil).JSON(); len(ta["exceptions"].([]any)) != 2 || len(ta["days"].([]any)) != 2 {
		t.Fatalf("team attendance: %v", ta)
	}
	tm.a.Must(422, "POST", "/api/v1/ess/overtime-requests", map[string]any{"workDate": day, "startTime": "17:00", "endTime": "22:00", "reason": "Banquet"})
	tm.a.Must(422, "POST", "/api/v1/ess/overtime-requests", map[string]any{"workDate": hrDay(-10), "startTime": "17:00", "endTime": "19:00", "reason": "Old"})
	ot := tm.a.Must(201, "POST", "/api/v1/ess/overtime-requests", map[string]any{"workDate": day, "startTime": "17:00", "endTime": "20:00",
		"reason": "Wedding banquet"}, "Idempotency-Key", newKey()).JSON()
	if ot["timing"] != "after" || ot["hours"] != "3" || ot["dayKind"] != "workday" {
		t.Fatalf("overtime: %v", ot)
	}
	tm.a.Must(409, "POST", "/api/v1/ess/overtime-requests", map[string]any{"workDate": day, "startTime": "19:00", "endTime": "20:00", "reason": "x"})
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/overtime/"+str(ot["id"])+":approve", map[string]any{})
	mine := tm.a.Must(200, "GET", "/api/v1/ess/overtime-requests", nil).Items()[0]
	tiers := fmt.Sprint(mine["tiers"])
	if mine["status"] != "approved" || mine["payableHours"] != "3" || mine["multipliedHours"] != "5.5" || mine["estimatedPay"] != "165000" ||
		!strings.Contains(tiers, "factor:1.5 hours:1") || !strings.Contains(tiers, "factor:2 hours:2") {
		t.Fatalf("EP-08 AC: %v", mine)
	}
	if hrEvents(t, hris.EventOvertimeApproved, str(ot["id"])) != 1 {
		t.Fatal("hris.overtime_approved")
	}
	if l := hr.Must(200, "GET", hrBase+"/overtime-requests?employeeId="+tm.aID, nil).Items(); l[0]["estimatedPay"] != "165000" {
		t.Fatalf("HR sees the pay: %v", l)
	}
	if l := roleUser(t, inst, "general_manager").Must(200, "GET", hrBase+"/overtime-requests?employeeId="+tm.aID, nil).Items(); l[0]["estimatedPay"] != nil {
		t.Fatalf("no salary data for the GM: %v", l)
	}
	// the payroll contract: payable hours by tier
	sum := hr.Must(200, "GET", hrBase+"/time-summary?from="+day+"&to="+day+"&employeeId="+tm.aID, nil).Items()[0]
	ov := sum["overtime"].(map[string]any)
	if sum["presentDays"] != float64(1) || ov["payableHours"] != "3" || ov["multipliedHours"] != "5.5" || len(ov["tiers"].([]any)) != 2 {
		t.Fatalf("time summary: %v", sum)
	}
	// FR-LVE-02 permission: B's late arrival is excused once approved
	if d := ttDay(t, hr, tm.bID, day); d["status"] != "late" {
		t.Fatalf("late before permission: %v", d)
	}
	tm.b.Must(422, "POST", "/api/v1/ess/permission-requests", map[string]any{"permissionType": "LATE_ARRIVAL", "workDate": day, "startTime": "08:00",
		"endTime": "11:00", "reason": "Flat tyre"})
	pm := tm.b.Must(201, "POST", "/api/v1/ess/permission-requests", map[string]any{"permissionType": "LATE_ARRIVAL", "workDate": day,
		"startTime": "08:00", "endTime": "09:30", "reason": "Flat tyre"}, "Idempotency-Key", newKey()).JSON()
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/permission/"+str(pm["id"])+":approve", map[string]any{})
	if d := ttDay(t, hr, tm.bID, day); d["status"] != "present" || d["lateMinutes"] != float64(0) {
		t.Fatalf("excused: %v", d)
	}
	pw := tm.b.Must(201, "POST", "/api/v1/ess/permission-requests", map[string]any{"permissionType": "PERSONAL", "workDate": hrDay(3),
		"startTime": "13:00", "endTime": "15:00", "reason": "Bank"}, "Idempotency-Key", newKey()).JSON()
	tm.b.Must(200, "POST", "/api/v1/ess/permission-requests/"+str(pw["id"])+":cancel", map[string]any{})
	pr := tm.b.Must(201, "POST", "/api/v1/ess/permission-requests", map[string]any{"permissionType": "PERSONAL", "workDate": hrDay(6),
		"startTime": "10:00", "endTime": "12:00", "reason": "Errand"}, "Idempotency-Key", newKey()).JSON()
	tm.mgr.Must(422, "POST", "/api/v1/ess/approvals/permission/"+str(pr["id"])+":reject", map[string]any{})
	if rj := tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/permission/"+str(pr["id"])+":reject", map[string]any{"note": "Stock take"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("manager reject: %v", rj)
	}
	hp := hr.Must(201, "POST", hrBase+"/permission-requests", map[string]any{"employeeId": tm.aID, "permissionType": "PERSONAL", "workDate": hrDay(4),
		"startTime": "13:00", "endTime": "15:00", "reason": "Notary"}, "Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/permission-requests/"+str(hp["id"])+":approve", map[string]any{})
	hp2 := hr.Must(201, "POST", hrBase+"/permission-requests", map[string]any{"employeeId": tm.aID, "permissionType": "EARLY_LEAVE", "workDate": hrDay(5),
		"startTime": "15:00", "endTime": "17:00", "reason": "School"}, "Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/permission-requests/"+str(hp2["id"])+":reject", map[string]any{"note": "Busy day"})
	if l := hr.Must(200, "GET", hrBase+"/permission-requests", nil).Items(); len(l) < 4 {
		t.Fatalf("permissions: %v", l)
	}
	if l := tm.b.Must(200, "GET", "/api/v1/ess/permission-requests", nil).Items(); len(l) != 3 {
		t.Fatalf("my permissions: %v", l)
	}
	// planned overtime: payable once worked; HR requests, approves, rejects
	fut := hr.Must(201, "POST", hrBase+"/overtime-requests", map[string]any{"employeeId": tm.bID, "workDate": hrDay(2), "startTime": "18:00",
		"endTime": "20:00", "reason": "Gala dinner"}, "Idempotency-Key", newKey()).JSON()
	if a := hr.Must(200, "POST", hrBase+"/overtime-requests/"+str(fut["id"])+":approve", map[string]any{}).JSON(); a["status"] != "approved" || a["payableHours"] != "0" {
		t.Fatalf("planned: %v", a)
	}
	f2 := hr.Must(201, "POST", hrBase+"/overtime-requests", map[string]any{"employeeId": tm.bID, "workDate": hrDay(2), "startTime": "20:00",
		"endTime": "21:00", "reason": "Clean-up"}, "Idempotency-Key", newKey()).JSON()
	hr.Must(200, "POST", hrBase+"/overtime-requests/"+str(f2["id"])+":reject", map[string]any{"note": "Not needed"})
	f3 := tm.b.Must(201, "POST", "/api/v1/ess/overtime-requests", map[string]any{"workDate": hrDay(2), "startTime": "06:00", "endTime": "07:00",
		"reason": "Setup"}, "Idempotency-Key", newKey()).JSON()
	if c := tm.b.Must(200, "POST", "/api/v1/ess/overtime-requests/"+str(f3["id"])+":cancel", map[string]any{}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("withdrawn: %v", c)
	}
	// payroll period lock: changes of the period are refused until released
	lock := hr.Must(201, "POST", hrBase+"/time-locks", map[string]any{"periodStart": day, "periodEnd": day, "reference": "PAY-" + tm.sfx},
		"Idempotency-Key", newKey()).JSON()
	hr.Must(409, "POST", hrBase+"/attendance:clock", map[string]any{"employeeId": tm.aID, "direction": "out", "occurredAt": ttRFC(day, "21:00"),
		"note": "late entry"})
	hr.Must(409, "POST", hrBase+"/attendance-corrections", map[string]any{"employeeId": tm.bID, "workDate": day, "clockIn": "08:00", "reason": "x"})
	if l := hr.Must(200, "GET", hrBase+"/time-locks", nil).Items(); len(l) == 0 || l[0]["status"] != "locked" {
		t.Fatalf("locks: %v", l)
	}
	hr.Must(422, "POST", hrBase+"/time-locks/"+str(lock["id"])+":release", map[string]any{})
	hr.Must(200, "POST", hrBase+"/time-locks/"+str(lock["id"])+":release", map[string]any{"note": "Recalculate payroll"})
	hr.Must(201, "POST", hrBase+"/attendance-corrections", map[string]any{"employeeId": tm.bID, "workDate": day, "clockIn": "08:00", "reason": "x"},
		"Idempotency-Key", newKey())
	// root API used by payroll
	ctx := reqctx.WithProperty(dbtx.System(context.Background()), inst.Main)
	var sums []hris.TimeSummary
	err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		d, _ := time.Parse("2006-01-02", day)
		var err error
		sums, err = hris.TimeSummaries(ctx, tx, inst.Main, d, d, []uuid.UUID{uuid.MustParse(tm.aID), uuid.MustParse(tm.bID)})
		return err
	})
	if err != nil || len(sums) != 2 {
		t.Fatalf("TimeSummaries: %v %v", sums, err)
	}
}

// ── ESS, reports, KPIs, demo, daily job ──────────────────────────────────

// FR-ESS-02/03/05, FR-SCH-07, EP-27 FR-RPT-P5-01/02 and the demo: the ESS
// sections of time & attendance, the demo roster and requests waiting for
// the department head, the reports and HR Performance KPIs, the daily job.
func TestP5HRTimeSelfServiceAndReports(t *testing.T) {
	emp := login(t, inst, "employee@demo.oneclub.id", demoPassword)
	head := login(t, inst, "dept.head@demo.oneclub.id", demoPassword)
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	keys := func(c *Client) map[string]bool {
		out := map[string]bool{}
		for _, s := range c.Must(200, "GET", "/api/v1/ess/me", nil).JSON()["sections"].([]any) {
			out[str(s.(map[string]any)["key"])] = true
		}
		return out
	}
	k := keys(emp)
	for _, s := range []string{"schedule", "clock", "attendance", "leave", "overtime"} {
		if !k[s] {
			t.Fatalf("ESS section %s missing: %v", s, k)
		}
	}
	if k["approvals"] || !keys(head)["approvals"] || !keys(head)["team-attendance"] || !keys(head)["team-schedule"] {
		t.Fatal("manager sections")
	}
	if s := emp.Must(200, "GET", "/api/v1/ess/schedule", nil).JSON(); len(s["assignments"].([]any)) < 5 {
		t.Fatalf("demo roster in My Schedule: %v", s)
	}
	if a := emp.Must(200, "GET", "/api/v1/ess/attendance", nil).JSON(); len(a["days"].([]any)) < 10 {
		t.Fatalf("demo attendance: %v", a)
	}
	if p := head.Must(200, "GET", "/api/v1/ess/approvals", nil).Items(); len(p) < 3 {
		t.Fatalf("demo requests waiting for the department head: %v", p)
	}
	if b := hr.Must(200, "GET", hrBase+"/leave-balances", nil).Items(); len(b) < 20 {
		t.Fatalf("demo balances: %d", len(b))
	}
	emp.Must(200, "GET", "/api/v1/ess/leave-requests", nil)
	emp.Must(200, "GET", "/api/v1/ess/attendance-corrections", nil)
	emp.Must(200, "GET", "/api/v1/ess/shift-swaps", nil)
	emp.Must(403, "GET", hrBase+"/attendance-days", nil)
	// EP-27 reports for HR (and the GM)
	for _, code := range []string{"hris.attendance", "hris.late_absence", "hris.overtime", "hris.leave_balance", "hris.shift_coverage"} {
		r := hr.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+hrDay(-30)+"&params[to]="+hrDay(7), nil).JSON()
		if len(r["rows"].([]any)) == 0 {
			t.Errorf("report %s has no rows", code)
		}
	}
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/reporting/reports/hris.attendance", nil)
	hp := hr.Must(200, "GET", "/api/v1/reporting/hr-performance?from="+hrDay(-30)+"&to="+hrDay(-1), nil).JSON()
	found := 0
	for _, x := range hp["kpis"].([]any) {
		km := x.(map[string]any)
		if (km["key"] == "attendance_rate" || km["key"] == "overtime_hours") && km["status"] == "available" && km["value"] != nil {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("HR Performance attendance / overtime: %v", hp["kpis"])
	}
	var rate string
	sysQueryRow(t, inst, `SELECT trim_scale(round(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::numeric
		/ nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave', 'absent')), 0), 2))::text FROM hris.attendance_days
		WHERE property_id = $1 AND work_date BETWEEN $2::date AND $3::date`, []any{inst.Main, hrDay(-30), hrDay(-1)}, &rate)
	if dec(rate).LessThan(dec("0.8")) || dec(rate).GreaterThanOrEqual(dec("1")) {
		t.Fatalf("demo attendance rate (some absent): %s", rate)
	}
	_ = reporting.KindRate
	// the daily job closes yesterday and runs the accrual
	rep, err := inst.App.Time.Module.RunDaily(context.Background())
	if err != nil || rep.Properties == 0 {
		t.Fatalf("daily job: %+v %v", rep, err)
	}
}
