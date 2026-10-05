package e2e

// PRD P5 gap closure (docs/p5-gap-audit.md): the HR / CRM / package /
// tournament reports and the core HR KPIs through the BI registry
// (EP-27), device clock-in of partner caddies (FR-ATT-08), push
// notifications of the PWAs (FR-INT-P5-05), the HR migration of the
// organization and documents and its reconciliation with HR and Finance
// sign-off (EP-28), and the §9.1 flow up to approved overtime.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/reporting"
)

// EP-27 FR-RPT-P5-01..04: every non-payroll P5 report runs from the BI
// registry with its own permission; the core HR KPIs replace the defaults
// of HR Performance; the HR datasets serve the report builder.
func TestP5GapsBIRegistry(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range reporting.P5Reports() {
		registered[r.Code] = true
	}
	want := map[string]string{
		// FR-RPT-P5-02 (non-payroll HR reports)
		"hris.headcount": "hr_manager", "hris.turnover": "hr_manager", "hris.contract_expiry": "hr_admin", "hris.certification_expiry": "hr_admin",
		"hris.training": "hr_manager", "hris.attendance": "hr_manager", "hris.late_absence": "hr_manager", "hris.overtime": "hr_manager",
		"hris.leave_balance": "hr_manager", "hris.shift_coverage": "hr_manager", "hris.performance_review": "hr_manager",
		// FR-RPT-P5-03
		"crm.journey_performance": "crm_admin", "crm.loyalty_tier": "crm_admin", "crm.reward_redemption": "crm_admin", "crm.nps_analytics": "crm_admin",
		"crm.complaint_sla": "crm_admin", "crm.sales_performance": "crm_admin", "commercial.package_profitability": "finance_manager",
		"golf.tournament_series": "golf_manager",
	}
	from, to := hrDay(-30), hrDay(0)
	formats := []string{"csv", "xlsx", "pdf"}
	exports := map[string]*Client{}
	i := 0
	for code, role := range want {
		if !registered[code] {
			t.Errorf("%s is not in the BI registry", code)
			continue
		}
		c := roleUser(t, inst, role)
		c.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+from+"&params[to]="+to, nil)
		// FR-RPT-P5-04: CSV / XLSX / PDF export with the same permission
		exp := c.Must(202, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": code, "format": formats[i%3],
			"params": map[string]any{"from": from, "to": to}}, "Idempotency-Key", newKey()).JSON()
		exports[str(exp["id"])] = c
		i++
	}
	waitFor(t, 60*time.Second, "report exports completed", func() bool {
		for eid, c := range exports {
			for _, e := range c.Must(200, "GET", "/api/v1/reporting/exports", nil).Items() {
				if e["id"] == eid {
					if e["status"] == "failed" {
						t.Fatalf("export %v failed: %v", e["reportCode"], e["error"])
					}
					if e["status"] != "completed" {
						return false
					}
				}
			}
		}
		return true
	})
	// permission per report: no HR report for the cashier, no CRM report for HR
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/reporting/reports/hris.headcount", nil)
	roleUser(t, inst, "hr_admin").Must(403, "GET", "/api/v1/reporting/reports/crm.journey_performance", nil)

	// FR-RPT-P5-01: Headcount (HRIS master), Turnover and Certification Compliance are served, equal to their definitions
	hp := roleUser(t, inst, "hr_manager").Must(200, "GET", "/api/v1/reporting/hr-performance?from="+from+"&to="+to, nil).JSON()
	kpis := map[string]map[string]any{}
	for _, k := range hp["kpis"].([]any) {
		km := k.(map[string]any)
		kpis[str(km["key"])] = km
	}
	for _, k := range reporting.HRCoreKPIs {
		got := kpis[k.Key]
		if got == nil || got["status"] != "available" || got["value"] == nil {
			t.Fatalf("HR Performance %s: %v", k.Key, got)
		}
		var v string
		sysQueryRow(t, inst, k.SQL, []any{from, to, clubLoc(inst).String()}, &v)
		if !dec(got["value"]).Equal(dec(v)) {
			t.Errorf("%s: HR Performance %v ≠ definition %s", k.Key, got["value"], v)
		}
	}
	for _, k := range []string{"attendance_rate", "overtime_hours", "caddy_attendance", "caddy_rating"} {
		if kpis[k] == nil || kpis[k]["status"] != "available" {
			t.Errorf("HR Performance %s: %v", k, kpis[k])
		}
	}

	// FR-BI-06: HR datasets with the permission of their report
	hr := roleUser(t, inst, "hr_manager")
	sets := map[string]bool{}
	for _, d := range hr.Must(200, "GET", "/api/v1/reporting/datasets", nil).Items() {
		sets[str(d["code"])] = true
	}
	for _, c := range []string{"hr_attendance", "hr_overtime", "hr_leave"} {
		if !sets[c] {
			t.Fatalf("dataset %s not offered to HR: %v", c, sets)
		}
	}
	res := hr.Must(200, "GET", "/api/v1/reporting/datasets/hr_attendance/query?dimensions=org_unit,status&metrics=days,present,worked_hours&from="+hrDay(-30)+
		"&to="+hrDay(0), nil).JSON()
	if len(res["rows"].([]any)) == 0 {
		t.Fatalf("attendance dataset: %v", res)
	}
	hr.Must(200, "GET", "/api/v1/reporting/datasets/hr_overtime/query?dimensions=month,day_kind&metrics=requests,payable_hours&period=last_30_days", nil)
	hr.Must(200, "GET", "/api/v1/reporting/datasets/hr_leave/query?dimensions=leave_type,paid&metrics=requests,days&period=year_to_date", nil)
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/reporting/datasets/hr_attendance/query?dimensions=status&metrics=days", nil)
}

// FR-ATT-08 / FR-INT-P5-01: a partner caddy enrolled with consent clocks in
// on a biometric device (mock adapter and bridge agent); the first clock-in
// of the day marks the caddy present in Caddy Master; resubmissions are
// recorded once; no consent, no event.
func TestP5GapsPartnerClockIn(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	cm := roleUser(t, inst, "caddy_manager")
	sa := superAdmin(t, inst)
	sfx := hrSuffix()
	caddy := idOf(sa.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "Q" + sfx, "name": "Caddy Device " + sfx, "gender": "female"}))
	t.Cleanup(func() { sa.Do("DELETE", "/api/v1/golf/caddies/"+caddy, nil) })
	roleUser(t, inst, "cashier").Must(403, "GET", hrBase+"/partner-attendance-profiles", nil)
	cm.Must(422, "POST", hrBase+"/partner-attendance-profiles", map[string]any{"holderKind": "caddy", "partnerId": caddy, "consent": true}) // no date
	cm.Must(422, "POST", hrBase+"/partner-attendance-profiles", map[string]any{"holderKind": "caddy", "partnerId": newKey()})
	p := cm.Must(201, "POST", hrBase+"/partner-attendance-profiles", map[string]any{"holderKind": "caddy", "partnerId": caddy},
		"Idempotency-Key", newKey()).JSON()
	pid := str(p["id"])
	if p["partnerName"] != "Caddy Device "+sfx || p["biometricConsent"] != false || dec(p["deviceUserNo"]).LessThan(dec(hris.PartnerDeviceUserBase)) {
		t.Fatalf("partner profile: %v", p)
	}
	cm.Must(409, "POST", hrBase+"/partner-attendance-profiles", map[string]any{"holderKind": "caddy", "partnerId": caddy})
	mock := idOf(hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "PC" + sfx, "name": "Caddy House " + sfx,
		"deviceKind": "biometric"}, "Idempotency-Key", newKey()))
	// consent is required before a partner is sent to a device or clocks in
	cm.Must(409, "POST", hrBase+"/attendance-devices/"+mock+":simulate-partner", map[string]any{"profileId": pid})
	cm.Must(422, "POST", hrBase+"/partner-attendance-profiles/"+pid+":consent", map[string]any{"consent": true})
	cm.Must(200, "POST", hrBase+"/partner-attendance-profiles/"+pid+":consent", map[string]any{"consent": true, "signedOn": hrDay(0)})
	if s := hr.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":sync-employees", map[string]any{}).JSON(); s["partners"].(float64) < 1 {
		t.Fatalf("sync with partners: %v", s)
	}
	today := hrDay(0)
	at := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	sim := cm.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":simulate-partner", map[string]any{"profileId": pid, "method": "face_recognition",
		"eventId": "P1", "occurredAt": at}).JSON()
	ev := sim["event"].(map[string]any)
	if ev["direction"] != "in" || ev["holderKind"] != "caddy" || sim["duplicate"] != false {
		t.Fatalf("partner clock-in: %v", sim)
	}
	if again := cm.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":simulate-partner", map[string]any{"profileId": pid, "method": "face_recognition",
		"eventId": "P1", "occurredAt": at}).JSON(); again["duplicate"] != true {
		t.Fatalf("device resend is recorded once: %v", again)
	}
	if n := hrEvents(t, hris.EventPartnerAttendanceRecorded, str(ev["id"])); n != 1 {
		t.Fatalf("hris.partner_attendance_recorded published %d times", n)
	}
	// golf: the caddy is present in Caddy Master (joins the queue)
	waitFor(t, 20*time.Second, "caddy attendance from the device clock-in", func() bool {
		hrDispatch(t)
		return ttCount(t, `SELECT count(*) FROM golf.caddy_attendance WHERE caddy_id = $1 AND work_date = $2::date AND status = 'present'
			AND queue_no IS NOT NULL AND notes LIKE 'Clock-in %'`, caddy, today) == 1
	})
	// a second clock-in of the day goes out, the attendance stays as recorded
	out := cm.Must(200, "POST", hrBase+"/attendance-devices/"+mock+":simulate-partner", map[string]any{"profileId": pid, "eventId": "P2"}).JSON()
	if out["event"].(map[string]any)["direction"] != "out" {
		t.Fatalf("second event: %v", out)
	}
	if l := cm.Must(200, "GET", hrBase+"/partner-attendance-events?holderKind=caddy&profileId="+pid+"&from="+today+"&to="+today, nil).Items(); len(l) != 2 {
		t.Fatalf("partner events: %v", l)
	}
	// bridge agent device: the partner's device number is routed to the partner
	agent := sa.Must(201, "POST", "/api/v1/platform/bridge-agents", map[string]any{"name": "Partner agent " + sfx}).JSON()
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
	hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "PZ" + sfx, "name": "ZK caddy " + sfx, "deviceKind": "biometric",
		"vendor": "zkteco", "serialNo": "PSN" + sfx, "bridgeAgentId": agent["id"]}, "Idempotency-Key", newKey())
	bridge := anon(t, inst)
	bridge.Bearer = str(agent["token"])
	push := map[string]any{"deviceSerial": "PSN" + sfx, "events": []map[string]any{
		{"eventId": "B1", "deviceUserNo": p["deviceUserNo"], "occurredAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "method": "fingerprint"},
		{"eventId": "B1", "deviceUserNo": p["deviceUserNo"], "occurredAt": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "method": "fingerprint"}}}
	pr := bridge.Must(200, "POST", "/api/v1/bridge/hris/attendance-events", push).JSON()["results"].([]any)
	if pr[0].(map[string]any)["status"] != "accepted" || pr[0].(map[string]any)["partnerResult"] == nil || pr[1].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("bridge push of a partner: %v", pr)
	}
	// consent withdrawn: removed from the devices, no more events
	if w := cm.Must(200, "POST", hrBase+"/partner-attendance-profiles/"+pid+":consent", map[string]any{"consent": false}).JSON(); w["biometricConsent"] != false {
		t.Fatalf("withdrawn: %v", w)
	}
	cm.Must(409, "POST", hrBase+"/attendance-devices/"+mock+":simulate-partner", map[string]any{"profileId": pid, "eventId": "P3"})
	if l := hr.Must(200, "GET", hrBase+"/partner-attendance-profiles?holderKind=caddy&q="+sfx, nil).Items(); len(l) != 1 || l[0]["lastEventAt"] == nil {
		t.Fatalf("profiles: %v", l)
	}
}

// FR-INT-P5-05 / FR-ESS-07: a device subscribes to push; in-app
// notifications are mirrored to push (log pusher of the trial instance),
// the user may opt out per category, and unsubscribe.
func TestP5GapsPush(t *testing.T) {
	sa := superAdmin(t, inst)
	emp := roleUser(t, inst, "hr_admin")
	cfg := emp.Must(200, "GET", "/api/v1/platform/push-config", nil).JSON()
	if cfg["enabled"] != true || len(str(cfg["publicKey"])) != 87 {
		t.Fatalf("push config: %v", cfg)
	}
	browser, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	enc := base64.RawURLEncoding
	endpoint := "https://push.e2e.test/sub/" + hrSuffix() + newKey()
	emp.Must(422, "POST", "/api/v1/platform/push-subscriptions", map[string]any{"endpoint": "http://insecure.test/x", "surface": "staff",
		"keys": map[string]any{"p256dh": enc.EncodeToString(browser.PublicKey().Bytes()), "auth": enc.EncodeToString(auth)}})
	emp.Must(422, "POST", "/api/v1/platform/push-subscriptions", map[string]any{"endpoint": endpoint, "surface": "staff",
		"keys": map[string]any{"p256dh": "bad", "auth": enc.EncodeToString(auth)}})
	sub := emp.Must(201, "POST", "/api/v1/platform/push-subscriptions", map[string]any{"endpoint": endpoint, "surface": "staff", "userAgent": "e2e",
		"keys": map[string]any{"p256dh": enc.EncodeToString(browser.PublicKey().Bytes()), "auth": enc.EncodeToString(auth)}}).JSON()
	sid := str(sub["id"])
	t.Cleanup(func() { emp.Do("DELETE", "/api/v1/platform/push-subscriptions/"+sid, nil) })
	if sub["service"] != "push.e2e.test" || sub["surface"] != "staff" {
		t.Fatalf("subscription: %v", sub)
	}
	if l := emp.Must(200, "GET", "/api/v1/platform/push-subscriptions", nil).Items(); len(l) != 1 {
		t.Fatalf("my devices: %v", l)
	}
	var uid string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = $1`, []any{"role.hr_admin@matrix.test"}, &uid)
	before := ttCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE user_id = $1 AND channel = 'push'`, uid)
	sa.Must(202, "POST", "/api/v1/platform/notifications:send-test", map[string]any{"userId": uid, "channels": []string{"in_app"}})
	waitFor(t, 30*time.Second, "push delivery sent", func() bool {
		return ttCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE user_id = $1 AND channel = 'push' AND status = 'sent'
			AND recipient = '1 device(s)'`, uid) == before+1
	})
	if d := sa.Must(200, "GET", "/api/v1/platform/notification-deliveries?filter[channel]=push&filter[eventCode]=system.test", nil).Items(); len(d) == 0 ||
		d[0]["channel"] != "push" {
		t.Fatalf("delivery history shows the push: %v", d)
	}
	if n := ttCount(t, `SELECT count(*) FROM platform.push_subscriptions WHERE id = $1 AND last_success_at IS NOT NULL`, sid); n != 1 {
		t.Fatal("subscription not marked as reached")
	}
	// opt-out of push for the category keeps the in-app notification only
	prefs := emp.Must(200, "GET", "/api/v1/platform/notification-preferences", nil).Items()
	var body []map[string]any
	for _, p := range prefs {
		if p["push"] == nil {
			t.Fatalf("preference without push: %v", p)
		}
		if p["category"] == "system" {
			p["push"] = false
		}
		delete(p, "label")
		delete(p, "mandatory")
		body = append(body, p)
	}
	emp.Must(200, "PUT", "/api/v1/platform/notification-preferences", map[string]any{"preferences": body})
	t.Cleanup(func() {
		for _, p := range body {
			p["push"] = true
		}
		emp.Must(200, "PUT", "/api/v1/platform/notification-preferences", map[string]any{"preferences": body})
	})
	sent := ttCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE user_id = $1 AND channel = 'push'`, uid)
	sa.Must(202, "POST", "/api/v1/platform/notifications:send-test", map[string]any{"userId": uid, "channels": []string{"in_app"}})
	if n := ttCount(t, `SELECT count(*) FROM platform.notification_deliveries WHERE user_id = $1 AND channel = 'push'`, uid); n != sent {
		t.Fatalf("push sent despite the opt-out: %d → %d", sent, n)
	}
	emp.Must(204, "DELETE", "/api/v1/platform/push-subscriptions/"+sid, nil)
	emp.Must(404, "DELETE", "/api/v1/platform/push-subscriptions/"+sid, nil)
	if l := emp.Must(200, "GET", "/api/v1/platform/push-subscriptions", nil).Items(); len(l) != 0 {
		t.Fatalf("unsubscribed: %v", l)
	}
}

// FR-MIG-P5-01: grades, org units (hierarchy) and positions (reporting
// line) by code, employee documents with their files; dry run and
// repeatable loads.
func TestP5GapsHRImportOrganization(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	grades := "code,name,level,minSalary,maxSalary\nGX" + sfx[:3] + ",Import Grade " + sfx + ",9,4000000,6000000\n"
	if g := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "grades", "csv": grades}).JSON(); g["inserted"] != float64(1) {
		t.Fatalf("grades: %v", g)
	}
	units := "code,name,parentCode,unitType,costCenter,headEmployeeNo\n" +
		"IC" + sfx + ",Import Child " + sfx + ",IP" + sfx + ",section,CC-IMP,EMP-00005\n" +
		"IP" + sfx + ",Import Parent " + sfx + ",SPORT,department,CC-IMP,\n" +
		"IB" + sfx + ",Bad Parent " + sfx + ",NOPE,section,,\n"
	dry := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "org_units", "csv": units, "dryRun": true}).JSON()
	if dry["inserted"] != float64(3) || ttCount(t, `SELECT count(*) FROM hris.org_units WHERE code LIKE $1`, "I_"+sfx) != 0 {
		t.Fatalf("dry run: %v", dry)
	}
	ou := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "org_units", "csv": units}).JSON()
	if ou["inserted"] != float64(3) || len(ou["issues"].([]any)) != 1 || !strings.Contains(str(ou["issues"]), "parentCode") {
		t.Fatalf("org units: %v", ou)
	}
	var parent, head string
	sysQueryRow(t, inst, `SELECT p.code, coalesce(e.employee_no, '') FROM hris.org_units u JOIN hris.org_units p ON p.id = u.parent_id
		LEFT JOIN hris.employees e ON e.id = u.head_employee_id WHERE u.code = $1 AND u.property_id = $2`, []any{"IC" + sfx, inst.Main}, &parent, &head)
	if parent != "IP"+sfx || head != "EMP-00005" {
		t.Fatalf("hierarchy: parent %s head %s", parent, head)
	}
	if again := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "org_units", "csv": units}).JSON(); again["updated"] != float64(3) {
		t.Fatalf("repeatable: %v", again)
	}
	pos := "code,name,orgUnitCode,gradeCode,reportsToCode,isHead,workforceRole,requiredCertifications,headcount\n" +
		"IPS" + sfx + ",Import Lifeguard,IC" + sfx + ",GX" + sfx[:3] + ",IPH" + sfx + ",false,lifeguard,LIFEGUARD|CPR_BLS,3\n" +
		"IPH" + sfx + ",Import Head,IP" + sfx + ",G4,,true,,,1\n" +
		"IPX" + sfx + ",Bad Unit,NOPE,,,,,,\n"
	pr := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "positions", "csv": pos}).JSON()
	if pr["inserted"] != float64(2) || pr["failed"] != float64(1) {
		t.Fatalf("positions: %v", pr)
	}
	var reports, certs string
	sysQueryRow(t, inst, `SELECT r.code, array_to_string(p.required_certifications, ',') FROM hris.positions p JOIN hris.positions r ON r.id = p.reports_to_position_id
		WHERE p.code = $1 AND p.property_id = $2`, []any{"IPS" + sfx, inst.Main}, &reports, &certs)
	if reports != "IPH"+sfx || certs != "LIFEGUARD,CPR_BLS" {
		t.Fatalf("position: reports to %s certs %s", reports, certs)
	}
	// documents with an uploaded scan (the CLI uploads --documents-dir files the same way)
	fid := hrUploadPDF(t, hr, "ktp-"+sfx+".pdf")
	docs := "employeeNo,documentType,title,documentNo,issuedOn,expiresOn,warningLevel,confidential,file,notes\n" +
		"EMP-00021,ktp,,3603" + sfx + "0001,2020-01-01,,,," + fid + ",migrated\n" +
		"EMP-00021,warning_letter,,SP-" + sfx + ",2026-01-10,2026-07-10,1,,,migrated\n" +
		"EMP-00021,ktp,,,,,,,not-a-file,\n" +
		"NOPE,ktp,,,,,,,,\n"
	dr := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "documents", "csv": docs}).JSON()
	if dr["inserted"] != float64(2) || dr["failed"] != float64(2) {
		t.Fatalf("documents: %v", dr)
	}
	if again := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "documents", "csv": docs}).JSON(); again["skipped"] != float64(2) {
		t.Fatalf("documents repeatable: %v", again)
	}
	if n := ttCount(t, `SELECT count(*) FROM hris.employee_documents d JOIN hris.employees e ON e.id = d.employee_id WHERE e.employee_no = 'EMP-00021'
		AND d.document_no = $1 AND d.file_id = $2::uuid AND d.status = 'active'`, "3603"+sfx+"0001", fid); n != 1 {
		t.Fatal("imported KTP with its file")
	}
	// the CLI path uploads the files of a directory
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/npwp.pdf", []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sys := reqctx.WithProperty(dbtx.System(context.Background()), inst.Main)
	csv := "employeeNo,documentType,documentNo,issuedOn,file\nEMP-00021,npwp,NP" + sfx + ",2021-03-01,npwp.pdf\n"
	rewritten, err := inst.App.HR.Module.UploadImportFiles(sys, inst.Main, strings.NewReader(csv), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := inst.App.HR.Module.ImportCSV(sys, inst.Main, "documents", strings.NewReader(rewritten), false)
	if err != nil || cli.Inserted != 1 {
		t.Fatalf("CLI documents: %+v %v", cli, err)
	}
	hr.Must(422, "POST", hrBase+"/imports", map[string]any{"entity": "org_units", "csv": "code,name,unknown\nX,Y,Z\n"})
}

// FR-MIG-P5-05 (Exit Criteria #1): headcount and leave balances of the
// legacy system against OneClub at cutover, signed by the HR Manager and the
// Finance Manager (two people); a mismatch needs an explanation.
func TestP5GapsReconciliation(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	cutover := hrDay(0)
	metrics := hr.Must(200, "GET", hrBase+"/migration-reconciliation-metrics", nil).Items()
	if len(metrics) < 2 {
		t.Fatalf("metrics: %v", metrics)
	}
	var head, fnb, annual string
	sysQueryRow(t, inst, `SELECT count(*)::text FROM hris.employees WHERE property_id = $1 AND archived_at IS NULL AND (join_date IS NULL OR join_date <= $2::date)
		AND (termination_date IS NULL OR termination_date > $2::date) AND (status = 'active' OR termination_date IS NOT NULL)`, []any{inst.Main, cutover}, &head)
	sysQueryRow(t, inst, `SELECT count(*)::text FROM hris.employees e JOIN hris.org_units u ON u.id = e.org_unit_id WHERE e.property_id = $1 AND u.code = 'FNB'
		AND e.archived_at IS NULL AND (e.join_date IS NULL OR e.join_date <= $2::date) AND (e.termination_date IS NULL OR e.termination_date > $2::date)
		AND (e.status = 'active' OR e.termination_date IS NOT NULL)`, []any{inst.Main, cutover}, &fnb)
	sysQueryRow(t, inst, `SELECT trim_scale(coalesce(sum(b.entitled + b.carried_over - b.carried_expired + b.adjusted - b.used), 0))::text
		FROM hris.leave_balances b JOIN hris.employees e ON e.id = b.employee_id WHERE b.property_id = $1 AND b.year = $2 AND upper(b.leave_type) = 'ANNUAL'
		AND e.archived_at IS NULL AND (e.termination_date IS NULL OR e.termination_date > $3::date)`,
		[]any{inst.Main, time.Now().In(clubLoc(inst)).Year(), cutover}, &annual)
	hr.Must(422, "POST", hrBase+"/migration-reconciliations", map[string]any{"cutoverDate": cutover, "legacySystem": "Excel HR",
		"lines": []map[string]any{{"metric": "salary", "key": "TOTAL", "legacy": "1"}}})
	rec := hr.Must(201, "POST", hrBase+"/migration-reconciliations", map[string]any{"cutoverDate": cutover, "legacySystem": "Excel HR",
		"lines": []map[string]any{{"metric": "headcount", "key": "TOTAL", "legacy": head}, {"metric": "headcount", "key": "FNB", "legacy": fnb},
			{"metric": "leave_balance", "key": "ANNUAL", "legacy": dec(annual).Add(dec("2")).String()}}}, "Idempotency-Key", newKey()).JSON()
	rid := str(rec["id"])
	lines := map[string]map[string]any{}
	for _, l := range rec["lines"].([]any) {
		lm := l.(map[string]any)
		lines[str(lm["metric"])+"/"+str(lm["key"])] = lm
	}
	if lines["headcount/TOTAL"]["match"] != true || lines["headcount/FNB"]["match"] != true || lines["leave_balance/ANNUAL"]["match"] != false ||
		lines["leave_balance/ANNUAL"]["difference"] != "-2" || rec["status"] != "draft" || !strings.HasPrefix(str(rec["number"]), "HRREC-") {
		t.Fatalf("reconciliation: %v", rec)
	}
	// org units present in OneClub but missing in the legacy breakdown are reported
	if len(rec["lines"].([]any)) <= 3 || rec["mismatches"].(float64) < 2 {
		t.Fatalf("OneClub-only org units: %v", rec["lines"])
	}
	hr.Must(200, "POST", hrBase+"/migration-reconciliations/"+rid+":recalculate", map[string]any{})
	// sign-off: each role signs its part, a mismatch needs an explanation
	hr.Must(403, "POST", hrBase+"/migration-reconciliations/"+rid+":sign-off", map[string]any{"role": "finance", "note": "x"})
	hr.Must(422, "POST", hrBase+"/migration-reconciliations/"+rid+":sign-off", map[string]any{"role": "hr"})
	s1 := hr.Must(200, "POST", hrBase+"/migration-reconciliations/"+rid+":sign-off", map[string]any{"role": "hr",
		"note": "2 days of annual leave granted after the legacy export; org units without legacy breakdown"}).JSON()
	if s1["status"] != "hr_signed" || s1["hrSignedName"] == nil {
		t.Fatalf("HR sign-off: %v", s1)
	}
	hr.Must(409, "POST", hrBase+"/migration-reconciliations/"+rid+":sign-off", map[string]any{"role": "hr", "note": "again"})
	hr.Must(409, "POST", hrBase+"/migration-reconciliations/"+rid+":recalculate", map[string]any{})
	s2 := fin.Must(200, "POST", hrBase+"/migration-reconciliations/"+rid+":sign-off", map[string]any{"role": "finance", "note": "Differences explained by HR"}).JSON()
	if s2["status"] != "signed_off" || s2["financeSignedAt"] == nil {
		t.Fatalf("Finance sign-off: %v", s2)
	}
	if c := hr.Must(200, "GET", hrBase+"/migration-reconciliations/"+rid+"/csv", nil); !strings.Contains(string(c.Body), "MISMATCH") ||
		!strings.Contains(string(c.Body), "Finance Manager") {
		t.Fatalf("csv: %s", c.Body)
	}
	hr.Must(409, "POST", hrBase+"/migration-reconciliations/"+rid+":cancel", map[string]any{})
	// four eyes: one person cannot sign both parts (a user holding both permissions)
	sa := superAdmin(t, inst)
	r2 := idOf(hr.Must(201, "POST", hrBase+"/migration-reconciliations", map[string]any{"cutoverDate": cutover, "legacySystem": "Excel HR",
		"lines": []map[string]any{{"metric": "headcount", "key": "TOTAL", "legacy": head}}}, "Idempotency-Key", newKey()))
	sa.Must(200, "POST", hrBase+"/migration-reconciliations/"+r2+":sign-off", map[string]any{"role": "hr"})
	sa.Must(409, "POST", hrBase+"/migration-reconciliations/"+r2+":sign-off", map[string]any{"role": "finance"})
	if c := hr.Must(200, "POST", hrBase+"/migration-reconciliations/"+r2+":cancel", map[string]any{}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", c)
	}
	if l := fin.Must(200, "GET", hrBase+"/migration-reconciliations", nil).Items(); len(l) < 2 {
		t.Fatalf("list: %v", l)
	}
	roleUser(t, inst, "cashier").Must(403, "GET", hrBase+"/migration-reconciliations", nil)
}

// PRD P5 §9.1 Hire to First Pay, up to the payroll inputs (payroll is the
// payroll area): requisition → offer → hired (employee, PKWT, ESS login) →
// lifeguard certificates → pool shift (refused before the certificates) →
// face recognition clock-in → 2 hours of overtime approved.
func TestP5GapsHireToWork(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	unit := idOf(hr.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": "HW" + sfx, "name": "Pool " + sfx, "parentId": hrID(t, "org_units", "SPORT"),
		"unitType": "section"}, "Idempotency-Key", newKey()))
	pos := idOf(hr.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "HWL" + sfx, "name": "Pool Lifeguard " + sfx, "orgUnitId": unit,
		"workforceRole": "lifeguard", "requiredCertifications": []string{"LIFEGUARD", "CPR_BLS"}}, "Idempotency-Key", newKey()))
	// EP-03: requisition, candidate, interview, offer, hire
	rq := hr.Must(201, "POST", hrBase+"/job-requisitions", map[string]any{"positionId": pos, "headcount": 1, "contractType": "pkwt",
		"salaryMax": "5500000", "targetStartDate": hrDay(0)}, "Idempotency-Key", newKey()).JSON()
	rid := str(rq["id"])
	if r := hr.Must(200, "POST", hrBase+"/job-requisitions/"+rid+":submit", map[string]any{}).JSON(); r["status"] != "open" {
		gm.Must(200, "POST", hrBase+"/job-requisitions/"+rid+":approve", map[string]any{})
	}
	cand := talentCandidate(t, hr, "Lifeguard "+sfx, "lg"+sfx+"@mail.test", nil)
	app := talentApply(t, hr, rid, cand)
	hr.Must(200, "POST", hrBase+"/applications/"+app+":move-stage", map[string]any{"stage": "screening", "screeningScore": "4"})
	hr.Must(200, "POST", hrBase+"/applications/"+app+":move-stage", map[string]any{"stage": "interview"})
	start := hrDay(-20)
	of := hr.Must(201, "POST", hrBase+"/applications/"+app+":offer", map[string]any{"startDate": start, "endDate": time.Now().In(clubLoc(inst)).AddDate(1, 0, -21).Format("2006-01-02"),
		"baseSalary": "5190000"}, "Idempotency-Key", newKey()).JSON()
	ofID := str(of["id"])
	if of["status"] == "submitted" {
		gm.Must(200, "POST", hrBase+"/job-offers/"+ofID+":approve", map[string]any{})
	}
	hr.Must(200, "POST", hrBase+"/job-offers/"+ofID+":send", map[string]any{})
	hr.Must(200, "POST", hrBase+"/job-offers/"+ofID+":accept", map[string]any{"reason": "Signed"})
	hire := hr.Must(200, "POST", hrBase+"/applications/"+app+":hire", map[string]any{"workEmail": "lg" + sfx + "@club.test"}).JSON()
	emp := hire["employee"].(map[string]any)
	eid := str(emp["employeeId"])
	if emp["userId"] == nil || hrEvents(t, hris.EventEmployeeHired, eid) != 1 {
		t.Fatalf("hired (EP-01 employee + login): %v", hire)
	}
	if c := hr.Must(200, "GET", hrBase+"/contracts?employeeId="+eid, nil).Items(); len(c) != 1 || c[0]["contractType"] != "pkwt" || c[0]["status"] != "active" {
		t.Fatalf("EP-02 PKWT contract: %v", c)
	}
	// EP-06 / G5: the pool shift is refused while the lifeguard certificate on file is expired
	tmpl := ttTemplate(t, hr, "HWP"+sfx, "07:00", "15:00", "lifeguard")
	day := ttPastWorkday(t, 1)
	sch := hr.Must(201, "POST", hrBase+"/schedules", map[string]any{"orgUnitId": unit, "periodStart": day, "periodEnd": day}, "Idempotency-Key", newKey()).JSON()
	assign := map[string]any{"assignments": []map[string]any{{"employeeId": eid, "workDate": day, "shiftTemplateId": tmpl}}}
	hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "LIFEGUARD"), "employeeId": eid,
		"issuedOn": hrDay(-800), "expiresOn": hrDay(-70), "certificateNo": "OLD-" + sfx}, "Idempotency-Key", newKey())
	hr.Must(409, "POST", hrBase+"/schedules/"+str(sch["id"])+":assign", assign)
	// EP-04: renewed certificates uploaded
	for _, code := range []string{"LIFEGUARD", "CPR_BLS"} {
		hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", code), "employeeId": eid,
			"issuedOn": hrDay(-30), "certificateNo": code + "-" + sfx, "fileId": hrUploadPDF(t, hr, code+".pdf")}, "Idempotency-Key", newKey())
	}
	hr.Must(200, "POST", hrBase+"/schedules/"+str(sch["id"])+":assign", assign)
	hr.Must(200, "POST", hrBase+"/schedules/"+str(sch["id"])+":publish", map[string]any{})
	// EP-07: face recognition clock-in on the pool device, clock-out after 2 extra hours
	dev := idOf(hr.Must(201, "POST", hrBase+"/attendance-devices", map[string]any{"code": "HWD" + sfx, "name": "Pool gate " + sfx, "deviceKind": "biometric"},
		"Idempotency-Key", newKey()))
	hr.Must(200, "POST", hrBase+"/attendance-profiles/"+eid+":consent", map[string]any{"consent": true, "signedOn": start})
	hr.Must(200, "POST", hrBase+"/attendance-devices/"+dev+":simulate", map[string]any{"employeeId": eid, "method": "face_recognition", "direction": "in",
		"occurredAt": ttRFC(day, "06:55"), "eventId": "HW-IN"})
	hr.Must(200, "POST", hrBase+"/attendance-devices/"+dev+":simulate", map[string]any{"employeeId": eid, "method": "face_recognition", "direction": "out",
		"occurredAt": ttRFC(day, "17:05"), "eventId": "HW-OUT"})
	if d := ttDay(t, hr, eid, day); d == nil || d["status"] != "present" {
		t.Fatalf("attendance day: %v", d)
	}
	// EP-08: 2 hours of overtime requested and approved; payable for payroll
	ot := hr.Must(201, "POST", hrBase+"/overtime-requests", map[string]any{"employeeId": eid, "workDate": day, "startTime": "15:00", "endTime": "17:00",
		"reason": "Swimming gala"}, "Idempotency-Key", newKey()).JSON()
	if ot["status"] != "approved" {
		hr.Must(200, "POST", hrBase+"/overtime-requests/"+str(ot["id"])+":approve", map[string]any{})
	}
	sum := hr.Must(200, "GET", hrBase+"/time-summary?from="+day+"&to="+day+"&employeeId="+eid, nil).Items()
	if len(sum) != 1 || sum[0]["overtime"].(map[string]any)["payableHours"] != "2" || sum[0]["presentDays"] != float64(1) {
		t.Fatalf("payroll inputs (time summary): %v", sum)
	}
}
