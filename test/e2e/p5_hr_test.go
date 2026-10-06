package e2e

// PRD P5 Core HR & Employee Self Service (EP-01, EP-02, EP-04, EP-16, EP-24,
// EP-28 HR part, contracts H6 / H7): organization, employees and the P0
// facade, masking, employment changes, offboarding and the H7 login
// deactivation, contracts (PKWT limits, renewal, permanent), documents,
// certifications (enforcement in golf and sport club), training, ESS,
// policies, imports and the core HR reports.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/reporting"
)

func init() {
	resourceCRUDModules["hris"] = true
	resourceCRUDSkip["hris.employee"] = "employee lifecycle: covered by TestP5HREmployees"
	resourceCRUDSkip["hris.certification"] = "holder rules: covered by TestP5HRCertificationsAndTraining"
	resourceCRUDSkip["hris.bank_account"] = "account number format and masking: covered by TestP5HREmployees"
	resourceCRUDSkip["hris.training_session"] = "session period and completion: covered by TestP5HRCertificationsAndTraining"
}

const hrBase = "/api/v1/hris"

// hrSuffix makes codes unique per run.
func hrSuffix() string { return fmt.Sprintf("%05d", time.Now().UnixNano()/1000%100000) }

// hrID returns the id of a code in an hris table at the MAIN property.
func hrID(t *testing.T, table, code string) string {
	t.Helper()
	var v string
	q := `SELECT id::text FROM hris.` + table + ` WHERE code = $1 AND property_id = $2`
	args := []any{code, inst.Main}
	if table == "grades" || table == "certification_types" {
		q, args = `SELECT id::text FROM hris.`+table+` WHERE code = $1`, []any{code}
	}
	sysQueryRow(t, inst, q, args, &v)
	return v
}

func hrEmployeeID(t *testing.T, no string) string {
	t.Helper()
	var v string
	sysQueryRow(t, inst, `SELECT id::text FROM hris.employees WHERE employee_no = $1 AND property_id = $2`, []any{no, inst.Main}, &v)
	return v
}

// hrDaily runs the daily Core HR job.
func hrDaily(t *testing.T) {
	t.Helper()
	if _, err := inst.App.HR.Module.RunDaily(context.Background()); err != nil {
		t.Fatalf("hris daily: %v", err)
	}
}

// hrEvents counts outbox events of a type for an aggregate.
func hrEvents(t *testing.T, eventType, aggregate string) int {
	t.Helper()
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = $1 AND aggregate_id = $2::uuid`, []any{eventType, aggregate}, &n)
	return n
}

// hrDispatch delivers the pending outbox events.
func hrDispatch(t *testing.T) {
	t.Helper()
	if _, err := inst.App.Dispatcher.DispatchPending(context.Background()); err != nil {
		t.Logf("dispatch: %v", err)
	}
}

func hrDay(days int) string {
	return time.Now().In(clubLoc(inst)).AddDate(0, 0, days).Format("2006-01-02")
}

// hrNewEmployee creates an employee through HRIS.
func hrNewEmployee(t *testing.T, c *Client, name, position string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"fullName": name, "positionId": hrID(t, "positions", position), "gender": "male", "joinDate": hrDay(-400)}
	for k, v := range extra {
		body[k] = v
	}
	return c.Must(201, "POST", hrBase+"/employees", body, "Idempotency-Key", newKey()).JSON()
}

func hrUploadPDF(t *testing.T, c *Client, name string) string {
	t.Helper()
	body, ctype := multipartBody(t, nil, "file", name, "%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
	return idOf(c.Must(201, "POST", hrBase+"/document-files", body, "Content-Type", ctype))
}

// hrPolicy creates a property version of a HR policy and deactivates it at
// the end of the test (shared configuration is left as it was found).
func hrPolicy(t *testing.T, c *Client, code, category string, value map[string]any) {
	t.Helper()
	main := inst.Main
	r := c.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": category, "code": code, "name": "Test " + code,
		"propertyId": main, "value": value})
	rid := idOf(r)
	t.Cleanup(func() {
		c.Must(200, "POST", "/api/v1/platform/club-policies/"+rid+":set-status", map[string]any{"status": "inactive"})
	})
}

// EP-24 (FR-POL-P5-01–05, FR-POL-P5-07) and H6: the HR policies and
// configurations are registered with versioned defaults; roles and the
// HRIS module are seeded for provisioning.
func TestP5HRPoliciesAndCatalog(t *testing.T) {
	sa := superAdmin(t, inst)
	codes := map[string]string{}
	for _, it := range sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if strings.HasPrefix(str(it["code"]), "hris.") {
			codes[str(it["code"])] = str(it["category"])
		}
	}
	for code, cat := range map[string]string{hris.HRConfigurationCode: "HR Configuration", hris.PayrollConfigurationCode: "Payroll Configuration",
		hris.AttendanceConfigurationCode: "Attendance Configuration", hris.LeavePolicyCode: "HR Policies", hris.OvertimePolicyCode: "HR Policies",
		hris.AttendancePolicyCode: "HR Policies", hris.ServiceChargePolicyCode: "HR Policies"} {
		if codes[code] != cat {
			t.Errorf("policy %s: category %q, want %q", code, codes[code], cat)
		}
	}
	// a configured version is validated against the type and versioned
	sa.Must(422, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "HR Policies", "code": hris.OvertimePolicyCode, "name": "Bad",
		"propertyId": inst.Main, "value": map[string]any{"maxHoursPerDayX": "3"}})
	ctx := dbtx.System(context.Background())
	var before hris.OvertimePolicy
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		before, _, err = hris.LoadOvertimePolicy(ctx, tx, inst.Main, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if before.HourlyDivisor != "173" || before.MaxHoursPerDay != "4" || before.MaxHoursPerWeek != "18" {
		t.Fatalf("overtime defaults (§16 #3): %+v", before)
	}
	hrPolicy(t, sa, hris.OvertimePolicyCode, "HR Policies", map[string]any{"maxHoursPerDay": "3"})
	var after hris.OvertimePolicy
	var ref struct{ Version int }
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		p, r, err := hris.LoadOvertimePolicy(ctx, tx, inst.Main, time.Now())
		after, ref.Version = p, r.Version
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if after.MaxHoursPerDay != "3" || after.HourlyDivisor != "173" || ref.Version < 1 {
		t.Fatalf("configured version (kept defaults, version recorded): %+v v%d", after, ref.Version)
	}
	// H6: the HRIS module, HR roles and their permissions are seeded
	var enabled bool
	sysQueryRow(t, inst, `SELECT enabled FROM platform.modules WHERE code = 'hris'`, nil, &enabled)
	if !enabled {
		t.Fatal("hris module enabled by default")
	}
	for role, perms := range map[string][]string{
		"hr_admin":              {"hris.employee.create", "hris.employee.view_sensitive", "hris.contract.view_salary", "hris.profile_change.review"},
		"hr_manager":            {"hris.employee.terminate", "hris.team.approve", "hris.import.create"},
		"employee_self_service": {"hris.ess.use", "platform.ops.access"},
		"department_head":       {"hris.ess.use", "hris.team.view", "hris.team.approve", "platform.ops.access"},
		"outlet_manager":        {"hris.team.approve"},
		"cashier":               {"hris.ess.use"},
	} {
		for _, p := range perms {
			var ok bool
			sysQueryRow(t, inst, `SELECT EXISTS (SELECT 1 FROM platform.role_permissions rp JOIN platform.roles r ON r.id = rp.role_id
				WHERE r.code = $1 AND rp.permission_code = $2)`, []any{role, p}, &ok)
			if !ok {
				t.Errorf("role %s lacks %s", role, p)
			}
		}
	}
	var gmSensitive bool
	sysQueryRow(t, inst, `SELECT EXISTS (SELECT 1 FROM platform.role_permissions rp JOIN platform.roles r ON r.id = rp.role_id
		WHERE r.code = 'general_manager' AND rp.permission_code IN ('hris.employee.view_sensitive', 'hris.contract.view_salary'))`, nil, &gmSensitive)
	if gmSensitive {
		t.Fatal("the General Manager must not see identity numbers or salaries (FR-HR-03)")
	}
}

// EP-01: organization, employee master (P0 facade, FR-HR-08), masking
// (FR-HR-03), onboarding login (FR-HR-04), transfer / promotion with
// effective date (FR-HR-05), offboarding (FR-HR-06), org chart (FR-HR-07)
// and the termination acceptance criterion (H7).
func TestP5HREmployees(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	sfx := hrSuffix()

	// Organization: a section below Sport Club with a lifeguard position.
	unit := hr.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": "POOL" + sfx, "name": "Pool " + sfx, "parentId": hrID(t, "org_units", "SPORT"),
		"unitType": "section", "costCenter": "sport"}, "Idempotency-Key", newKey()).JSON()
	if unit["costCenter"] != "SPORT" {
		t.Fatalf("org unit: %v", unit)
	}
	hr.Must(422, "PATCH", hrBase+"/org-units/"+hrID(t, "org_units", "SPORT"), map[string]any{"parentId": unit["id"]}) // cycle
	pos := hr.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "POOL-LG" + sfx, "name": "Pool Lifeguard", "orgUnitId": unit["id"],
		"gradeId": hrID(t, "grades", "G1"), "workforceRole": "lifeguard", "requiredCertifications": []string{"first_aid"}, "headcount": 2},
		"Idempotency-Key", newKey()).JSON()
	hr.Must(422, "POST", hrBase+"/positions", map[string]any{"code": "BAD" + sfx, "name": "Bad", "orgUnitId": unit["id"], "requiredCertifications": []string{"NOPE"}})
	// P0 facade: a department of Settings → Organization is an HRIS org unit
	dep := sa.Must(201, "POST", "/api/v1/platform/departments", map[string]any{"code": "P0D" + sfx, "name": "P0 Department"}).JSON()
	hr.Must(200, "GET", hrBase+"/org-units/"+str(dep["id"]), nil)

	// Employee through HRIS: numbered, placed, mirrored to the P0 master.
	e := hr.Must(201, "POST", hrBase+"/employees", map[string]any{"fullName": "Budi Lifeguard " + sfx, "positionId": pos["id"], "gender": "male",
		"nik": "3603011208950001", "npwp": "09.254.294.3-407.000", "ptkpStatus": "K/1", "bpjsKesehatanNo": "0001234567890", "email": "budi" + sfx + "@club.test",
		"joinDate": hrDay(-300), "employmentStatus": "contract", "healthNotes": "Asthma"}, "Idempotency-Key", newKey()).JSON()
	eid := str(e["id"])
	if !strings.HasPrefix(str(e["employeeNo"]), "EMP-") || e["orgUnitId"] != unit["id"] || e["jobTitle"] != "Pool Lifeguard" || e["npwp"] != "092542943407000" {
		t.Fatalf("employee: %v", e)
	}
	if hrEvents(t, hris.EventEmployeeHired, eid) != 1 {
		t.Fatal("hris.employee_hired not published")
	}
	p0 := sa.Must(200, "GET", "/api/v1/platform/employees/"+eid, nil).JSON()
	if p0["employeeNo"] != e["employeeNo"] || p0["departmentId"] != unit["id"] || p0["jobTitle"] != "Pool Lifeguard" {
		t.Fatalf("P0 facade mirror: %v", p0)
	}
	// and a P0 employee is an HRIS employee (existing staff: Permanent)
	pe := sa.Must(201, "POST", "/api/v1/platform/employees", map[string]any{"employeeNo": "P0E" + sfx, "fullName": "Facade Person", "departmentId": dep["id"]}).JSON()
	he := hr.Must(200, "GET", hrBase+"/employees/"+str(pe["id"]), nil).JSON()
	if he["employmentStatus"] != "permanent" || he["orgUnitId"] != dep["id"] {
		t.Fatalf("P0 employee in HRIS: %v", he)
	}
	sa.Must(200, "PATCH", "/api/v1/platform/employees/"+str(pe["id"]), map[string]any{"jobTitle": "Clerk"})
	if hr.Must(200, "GET", hrBase+"/employees/"+str(pe["id"]), nil).JSON()["jobTitle"] != "Clerk" {
		t.Fatal("P0 edit not mirrored")
	}

	// FR-HR-03: identity numbers and health data are masked without the
	// sensitive permission; masked values sent back keep the data.
	seen := gm.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON()
	if seen["nik"] != "************0001" || seen["healthNotes"] != "[REDACTED]" || seen["bpjsKesehatanNo"] != "*********7890" {
		t.Fatalf("masked for the GM: nik %v health %v bpjs %v", seen["nik"], seen["healthNotes"], seen["bpjsKesehatanNo"])
	}
	if full := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON(); full["nik"] != "3603011208950001" || full["healthNotes"] != "Asthma" {
		t.Fatalf("HR sees the data: %v", full)
	}
	hr.Must(200, "PATCH", hrBase+"/employees/"+eid, map[string]any{"nik": "************0001", "phone": "+628121112222"})
	if full := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON(); full["nik"] != "3603011208950001" || full["phone"] != "+628121112222" {
		t.Fatalf("masked value kept: %v", full)
	}
	hr.Must(422, "PATCH", hrBase+"/employees/"+eid, map[string]any{"orgUnitId": dep["id"]}) // placement changes through :transfer
	bank := hr.Must(201, "POST", hrBase+"/bank-accounts", map[string]any{"employeeId": eid, "bankCode": "bca", "bankName": "BCA", "accountNo": "5270012345",
		"accountName": "Budi"}, "Idempotency-Key", newKey()).JSON()
	if gmBank := gm.Do("GET", hrBase+"/bank-accounts/"+str(bank["id"]), nil); gmBank.Status != 403 {
		t.Fatalf("the GM does not read bank accounts: %s", gmBank)
	}
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	if v := fin.Must(200, "GET", hrBase+"/bank-accounts/"+str(bank["id"]), nil).JSON(); v["accountNo"] != "5270012345" {
		t.Fatalf("payroll (finance) sees the account: %v", v)
	}
	second := hr.Must(201, "POST", hrBase+"/bank-accounts", map[string]any{"employeeId": eid, "bankName": "Mandiri", "accountNo": "1230009876",
		"accountName": "Budi"}, "Idempotency-Key", newKey()).JSON()
	if hr.Must(200, "GET", hrBase+"/bank-accounts/"+str(bank["id"]), nil).JSON()["isPrimary"] != false || second["isPrimary"] != true {
		t.Fatal("one primary salary account")
	}
	hr.Must(422, "POST", hrBase+"/bank-accounts", map[string]any{"employeeId": eid, "bankName": "X", "accountNo": "abc", "accountName": "Budi"})
	hr.Must(200, "PATCH", hrBase+"/bank-accounts/"+str(bank["id"]), map[string]any{"branch": "Tangerang", "accountNo": "******2345"})
	hr.Must(204, "DELETE", hrBase+"/bank-accounts/"+str(bank["id"]), nil)
	ec := hr.Must(201, "POST", hrBase+"/emergency-contacts", map[string]any{"employeeId": eid, "name": "Sari", "relationship": "spouse", "phone": "+628129999"},
		"Idempotency-Key", newKey()).JSON()
	hr.Must(200, "PATCH", hrBase+"/emergency-contacts/"+str(ec["id"]), map[string]any{"isPrimary": true})

	// FR-HR-05: a future transfer is scheduled, applied on its date; a
	// change can be cancelled before; promotions need a grade or position.
	sched := hr.Must(201, "POST", hrBase+"/employees/"+eid+":transfer", map[string]any{"effectiveDate": hrDay(5), "orgUnitId": hrID(t, "org_units", "SPORT"),
		"positionId": hrID(t, "positions", "LIFEGUARD"), "reason": "Main pool"}).JSON()
	if sched["status"] != "scheduled" {
		t.Fatalf("future transfer: %v", sched)
	}
	prof := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/profile", nil).JSON()
	if len(prof["scheduled"].([]any)) != 1 || prof["employee"].(map[string]any)["orgUnitId"] != unit["id"] {
		t.Fatalf("profile before the date: %v", prof["scheduled"])
	}
	sysExec(t, inst, `UPDATE hris.employment_history SET effective_date = current_date WHERE id = $1`, sched["id"])
	hrDaily(t)
	if got := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON(); got["positionId"] != hrID(t, "positions", "LIFEGUARD") ||
		got["orgUnitId"] != hrID(t, "org_units", "SPORT") || got["jobTitle"] != "Lifeguard" {
		t.Fatalf("transfer applied on its date: %v", got)
	}
	back := hr.Must(201, "POST", hrBase+"/employees/"+eid+":transfer", map[string]any{"effectiveDate": hrDay(30), "positionId": pos["id"], "kind": "rotation",
		"reason": "Rotation"}).JSON()
	hr.Must(422, "POST", hrBase+"/employment-changes/"+str(back["id"])+":cancel", map[string]any{"reason": ""})
	if c := hr.Must(200, "POST", hrBase+"/employment-changes/"+str(back["id"])+":cancel", map[string]any{"reason": "Plan changed"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}
	hr.Must(422, "POST", hrBase+"/employees/"+eid+":promote", map[string]any{"effectiveDate": hrDay(0), "supervisorId": hrEmployeeID(t, "EMP-00004"),
		"reason": "x"}) // a promotion changes position or grade
	promo := hr.Must(201, "POST", hrBase+"/employees/"+eid+":promote", map[string]any{"effectiveDate": hrDay(0), "gradeId": hrID(t, "grades", "G2"),
		"supervisorId": hrEmployeeID(t, "EMP-00009"), "reason": "Senior lifeguard"}).JSON()
	if promo["status"] != "applied" || promo["toGrade"] != "G2" {
		t.Fatalf("promotion: %v", promo)
	}
	hr.Must(422, "POST", hrBase+"/employees/"+hrEmployeeID(t, "EMP-00009")+":transfer", map[string]any{"effectiveDate": hrDay(0),
		"supervisorId": eid, "reason": "cycle"}) // the supervisor reports to the employee
	hist := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/history", nil).Items()
	kinds := map[string]bool{}
	for _, h := range hist {
		kinds[str(h["kind"])] = true
	}
	if !kinds["hire"] || !kinds["transfer"] || !kinds["promotion"] || !kinds["rotation"] {
		t.Fatalf("history: %v", kinds)
	}
	if len(hr.Must(200, "GET", hrBase+"/employment-changes?status=applied&kind=promotion", nil).Items()) == 0 {
		t.Fatal("employment changes list")
	}

	// FR-HR-07 org chart
	chart := hr.Must(200, "GET", hrBase+"/org-chart?employees=true", nil).Items()
	var gmUnit map[string]any
	for _, n := range chart {
		if n["code"] == "GM-OFFICE" {
			gmUnit = n
		}
	}
	if gmUnit == nil || gmUnit["headName"] != "Agus Santoso" || int(gmUnit["total"].(float64)) < 39 {
		t.Fatalf("org chart: %v", gmUnit)
	}

	// FR-HR-04: onboarding login with the self-service role
	acct := hr.Must(200, "POST", hrBase+"/employees/"+eid+":create-account", map[string]any{"locale": "id"}).JSON()["account"].(map[string]any)
	if acct["email"] != "budi"+sfx+"@club.test" || !strings.Contains(fmt.Sprint(acct["roles"]), "employee_self_service") {
		t.Fatalf("account: %v", acct)
	}
	hr.Must(409, "POST", hrBase+"/employees/"+eid+":create-account", map[string]any{})
	uid := str(acct["userId"])
	sysExec(t, inst, `UPDATE platform.users SET password_hash = (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id'),
		password_changed_at = now() WHERE id = $1`, uid)
	me := login(t, inst, "budi"+sfx+"@club.test", demoPassword)
	if got := me.Must(200, "GET", "/api/v1/ess/me", nil).JSON(); got["employee"].(map[string]any)["employeeNo"] != e["employeeNo"] {
		t.Fatalf("ESS of the new login: %v", got)
	}

	// EP-01 AC / H7: terminated on the effective date → no login from
	// that date, the approvals waiting for the leaver move to the
	// supervisor (Nadia, HR Manager), the checklist is created.
	var gmUser, hrUser string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'gm@demo.oneclub.id'`, nil, &gmUser)
	sysQueryRow(t, inst, `SELECT id::text FROM platform.users WHERE email = 'hr@demo.oneclub.id'`, nil, &hrUser)
	req := uuid.NewString()
	sysExec(t, inst, `INSERT INTO platform.approval_requests (id, property_id, document_type, document_id, document_ref, title, attributes, status, requested_by,
		current_step_no) VALUES ($1, $2, 'test_approval', gen_random_uuid(), 'T-1', 'Leaver approval', '{}', 'pending', $3, 1)`, req, inst.Main, gmUser)
	sysExec(t, inst, `INSERT INTO platform.approval_request_steps (id, request_id, property_id, step_no, name, approver_type, approver_user_id, status)
		VALUES (gen_random_uuid(), $1, $2, 1, 'Leaver', 'user', $3, 'pending')`, req, inst.Main, uid)
	hr.Must(422, "POST", hrBase+"/employees/"+eid+":terminate", map[string]any{"terminationType": "fired", "effectiveDate": hrDay(0), "reason": "x"})
	hr.Must(200, "POST", hrBase+"/employees/"+eid+":terminate", map[string]any{"terminationType": "resigned", "effectiveDate": hrDay(3), "reason": "New job"})
	if p := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/profile", nil).JSON()["employee"].(map[string]any); p["terminationStatus"] != "scheduled" {
		t.Fatalf("scheduled resignation: %v", p)
	}
	hr.Must(409, "POST", hrBase+"/employees/"+eid+":terminate", map[string]any{"terminationType": "resigned", "effectiveDate": hrDay(3), "reason": "again"})
	me.Must(200, "GET", "/api/v1/ess/me", nil) // still works before the date
	hr.Must(200, "POST", hrBase+"/employees/"+eid+":cancel-termination", map[string]any{"reason": "Stays"})
	hr.Must(409, "POST", hrBase+"/employees/"+eid+":cancel-termination", map[string]any{"reason": "again"})
	hr.Must(200, "POST", hrBase+"/employees/"+eid+":terminate", map[string]any{"terminationType": "resigned", "effectiveDate": hrDay(3), "reason": "New job"})
	items := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/offboarding", nil).Items()
	if len(items) < 5 {
		t.Fatalf("offboarding checklist: %v", items)
	}
	done := hr.Must(200, "PATCH", hrBase+"/offboarding-items/"+str(items[0]["id"]), map[string]any{"status": "done", "notes": "Handed over"}).JSON()
	if done["status"] != "done" || done["doneByName"] == nil {
		t.Fatalf("checklist tick: %v", done)
	}
	// the effective date arrives
	sysExec(t, inst, `UPDATE hris.employees SET termination_date = current_date WHERE id = $1`, eid)
	sysExec(t, inst, `UPDATE hris.employment_history SET effective_date = current_date WHERE employee_id = $1 AND kind = 'termination' AND status = 'scheduled'`, eid)
	hrDaily(t)
	if hrEvents(t, hris.EventEmployeeTerminated, eid) != 1 {
		t.Fatal("hris.employee_terminated not published")
	}
	waitFor(t, 60*time.Second, "IAM to deactivate the leaver", func() bool {
		hrDispatch(t)
		var st string
		sysQueryRow(t, inst, `SELECT status FROM platform.users WHERE id = $1`, []any{uid}, &st)
		return st == "inactive"
	})
	if r := anon(t, inst).Do("POST", "/api/v1/auth/login", map[string]any{"email": "budi" + sfx + "@club.test", "password": demoPassword}); r.Status == 200 {
		t.Fatalf("a leaver cannot log in: %s", r)
	}
	if me.Do("GET", "/api/v1/ess/me", nil).Status != 401 {
		t.Fatal("the leaver's session is revoked")
	}
	var approver string
	sysQueryRow(t, inst, `SELECT approver_user_id::text FROM platform.approval_request_steps WHERE request_id = $1`, []any{req}, &approver)
	if approver != hrUser {
		t.Fatalf("pending approval moved to the supervisor: %s, want %s", approver, hrUser)
	}
	left := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/profile", nil).JSON()
	if emp := left["employee"].(map[string]any); emp["employmentStatus"] != "resigned" || emp["status"] != "inactive" || emp["terminationStatus"] != "completed" {
		t.Fatalf("after the effective date: %v", emp)
	}
	hr.Must(409, "POST", hrBase+"/employees/"+eid+":transfer", map[string]any{"effectiveDate": hrDay(0), "positionId": pos["id"], "reason": "x"})
	sysExec(t, inst, `UPDATE platform.approval_requests SET status = 'cancelled' WHERE id = $1`, req)

	// an immediate termination takes effect at once
	other := hrNewEmployee(t, hr, "Short Stay "+sfx, "WAITER", nil)
	hr.Must(200, "POST", hrBase+"/employees/"+str(other["id"])+":terminate", map[string]any{"terminationType": "terminated", "effectiveDate": hrDay(0),
		"reason": "Probation not passed"})
	if got := hr.Must(200, "GET", hrBase+"/employees/"+str(other["id"]), nil).JSON(); got["employmentStatus"] != "terminated" || got["status"] != "inactive" {
		t.Fatalf("immediate termination: %v", got)
	}
	// a record created by mistake is archived (and leaves the P0 facade too)
	typo := hrNewEmployee(t, hr, "Typo "+sfx, "WAITER", nil)
	hr.Must(204, "DELETE", hrBase+"/employees/"+str(typo["id"]), nil)
	if p0 := sa.Must(200, "GET", "/api/v1/platform/employees/"+str(typo["id"]), nil).JSON(); p0["archivedAt"] == nil {
		t.Fatalf("archive mirrored to the P0 master: %v", p0)
	}
}

// EP-02 FR-CTR-01–03: PKWT / PKWTT with the HR Configuration limits,
// renewal, conversion to permanent, reminders H-30 / H-7
// (hris.contract_expiring), salary visibility, signed file and letters
// (FR-CTR-05).
func TestP5HRContracts(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	e := hrNewEmployee(t, hr, "Kontrak "+sfx, "WAITER", map[string]any{"employmentStatus": "contract", "joinDate": hrDay(-335)})
	eid := str(e["id"])
	base := map[string]any{"employeeId": eid, "contractType": "pkwt", "startDate": hrDay(-335), "baseSalary": "5190000",
		"allowances": []map[string]any{{"code": "transport", "name": "Transport", "amount": "500000"}}}
	with := func(m map[string]any, kv ...any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1]
		}
		return out
	}
	hr.Must(422, "POST", hrBase+"/contracts", with(base, "endDate", hrDay(25), "probationMonths", 3))    // no probation on PKWT
	hr.Must(422, "POST", hrBase+"/contracts", with(base, "endDate", hrDay(-335+365*5+2)))                // more than 5 years
	hr.Must(422, "POST", hrBase+"/contracts", with(base, "contractType", "pkwtt", "probationMonths", 4)) // probation at most 3 months
	hr.Must(422, "POST", hrBase+"/contracts", base)                                                      // a PKWT has an end
	c1 := hr.Must(201, "POST", hrBase+"/contracts", with(base, "endDate", hrDay(25)), "Idempotency-Key", newKey()).JSON()
	if c1["status"] != "draft" || !strings.HasPrefix(str(c1["number"]), "CTR-") {
		t.Fatalf("draft: %v", c1)
	}
	c1 = hr.Must(200, "PATCH", hrBase+"/contracts/"+str(c1["id"]), map[string]any{"baseSalary": "5500000", "workWeekDays": 6}).JSON()
	if c1["baseSalary"] != "5500000" || c1["workWeekDays"] != float64(6) {
		t.Fatalf("draft edit: %v", c1)
	}
	c1 = hr.Must(200, "POST", hrBase+"/contracts/"+str(c1["id"])+":activate", map[string]any{}).JSON()
	if c1["status"] != "active" {
		t.Fatalf("activate: %v", c1)
	}
	hr.Must(409, "PATCH", hrBase+"/contracts/"+str(c1["id"]), map[string]any{"baseSalary": "1"})
	if got := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON(); got["employmentStatus"] != "contract" {
		t.Fatalf("PKWT → Contract: %v", got)
	}
	// FR-HR-03: salaries only for HR / payroll
	if v := gm.Must(200, "GET", hrBase+"/contracts/"+str(c1["id"]), nil).JSON(); v["baseSalary"] != nil || len(v["allowances"].([]any)) != 0 {
		t.Fatalf("GM sees no salary: %v", v)
	}
	// H-30 reminder: Expiring + hris.contract_expiring
	hrDaily(t)
	if got := hr.Must(200, "GET", hrBase+"/contracts/"+str(c1["id"]), nil).JSON(); got["status"] != "expiring" || got["daysRemaining"] != float64(25) {
		t.Fatalf("H-30: %v", got)
	}
	if hrEvents(t, hris.EventContractExpiring, str(c1["id"])) != 1 {
		t.Fatal("hris.contract_expiring at H-30")
	}
	hrDaily(t) // once per reminder
	if hrEvents(t, hris.EventContractExpiring, str(c1["id"])) != 1 {
		t.Fatal("hris.contract_expiring sent twice")
	}
	sysExec(t, inst, `UPDATE hris.contracts SET end_date = $2::date WHERE id = $1`, c1["id"], hrDay(6))
	hrDaily(t)
	if hrEvents(t, hris.EventContractExpiring, str(c1["id"])) != 2 {
		t.Fatal("hris.contract_expiring at H-7")
	}
	sysExec(t, inst, `UPDATE hris.contracts SET end_date = $2::date WHERE id = $1`, c1["id"], hrDay(25))
	exp := hr.Must(200, "GET", hrBase+"/contracts?expiringWithin=30", nil).Items()
	found := false
	for _, x := range exp {
		found = found || x["id"] == c1["id"]
	}
	if !found {
		t.Fatal("expiring contracts list")
	}
	// renewal (PKWT 2) within 5 years; beyond it the employee must become permanent
	hr.Must(422, "POST", hrBase+"/contracts/"+str(c1["id"])+":renew", map[string]any{"endDate": hrDay(-335 + 365*5 + 5)})
	hr.Must(422, "POST", hrBase+"/contracts/"+str(c1["id"])+":renew", map[string]any{"startDate": hrDay(27), "endDate": hrDay(400)}) // gap after the end
	c2 := hr.Must(201, "POST", hrBase+"/contracts/"+str(c1["id"])+":renew", map[string]any{"startDate": hrDay(0), "endDate": hrDay(365),
		"baseSalary": "5800000"}).JSON()
	if c2["sequenceNo"] != float64(2) || c2["status"] != "active" || c2["startDate"] != hrDay(0)+"T00:00:00Z" {
		t.Fatalf("renewal: %v", c2)
	}
	if old := hr.Must(200, "GET", hrBase+"/contracts/"+str(c1["id"]), nil).JSON(); old["status"] != "renewed" || old["supersededById"] != c2["id"] {
		t.Fatalf("renewed contract: %v", old)
	}
	hr.Must(409, "POST", hrBase+"/contracts/"+str(c1["id"])+":renew", map[string]any{"endDate": hrDay(400)})
	// conversion to PKWTT from today: Permanent
	c3 := hr.Must(201, "POST", hrBase+"/contracts/"+str(c2["id"])+":make-permanent", map[string]any{"startDate": hrDay(0), "notes": "Good review"}).JSON()
	if c3["contractType"] != "pkwtt" || c3["endDate"] != nil || c3["sequenceNo"] != float64(3) {
		t.Fatalf("permanent: %v", c3)
	}
	if got := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON(); got["employmentStatus"] != "permanent" || got["permanentDate"] != hrDay(0) {
		t.Fatalf("PKWTT → Permanent: %v", got)
	}
	hr.Must(409, "POST", hrBase+"/contracts/"+str(c3["id"])+":renew", map[string]any{"endDate": hrDay(400)})
	// signed contract scan and HR letters
	fid := hrUploadPDF(t, hr, "pkwtt-signed.pdf")
	hr.Must(200, "PATCH", hrBase+"/contracts/"+str(c3["id"]), map[string]any{"signedFileId": fid})
	if r := hr.Must(200, "GET", hrBase+"/contracts/"+str(c3["id"])+"/file", nil); !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatal("signed contract file")
	}
	for _, code := range []string{"SK-KERJA", "PKWT-AGREEMENT"} {
		r := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/letters/"+code, nil)
		if !strings.HasPrefix(string(r.Body), "%PDF") || !strings.Contains(r.Header.Get("Content-Type"), "pdf") {
			t.Fatalf("letter %s", code)
		}
	}
	hr.Must(404, "GET", hrBase+"/employees/"+eid+"/letters/NOPE", nil)
	// end early
	hr.Must(422, "POST", hrBase+"/contracts/"+str(c3["id"])+":end", map[string]any{"endDate": hrDay(10), "reason": ""})
	ended := hr.Must(200, "POST", hrBase+"/contracts/"+str(c3["id"])+":end", map[string]any{"endDate": hrDay(10), "reason": "Mutual agreement"}).JSON()
	if ended["endedOn"] != hrDay(10)+"T00:00:00Z" || ended["status"] != "active" {
		t.Fatalf("end: %v", ended)
	}
	// PKWTT with probation for a new hire; a draft can be cancelled
	n := hrNewEmployee(t, hr, "Probation "+sfx, "COOK", map[string]any{"joinDate": hrDay(-10)})
	pc := hr.Must(201, "POST", hrBase+"/contracts", map[string]any{"employeeId": n["id"], "contractType": "pkwtt", "startDate": hrDay(-10), "probationMonths": 3,
		"baseSalary": "5500000", "activate": true}, "Idempotency-Key", newKey()).JSON()
	if got := hr.Must(200, "GET", hrBase+"/employees/"+str(n["id"]), nil).JSON(); got["employmentStatus"] != "probation" || pc["probationEndDate"] == nil {
		t.Fatalf("probation: %v / %v", got, pc)
	}
	hr.Must(409, "POST", hrBase+"/contracts", map[string]any{"employeeId": n["id"], "contractType": "pkwtt", "startDate": hrDay(0), "baseSalary": "1"})
	d := hrNewEmployee(t, hr, "Draft "+sfx, "COOK", nil)
	dup := hr.Must(201, "POST", hrBase+"/contracts", map[string]any{"employeeId": d["id"], "contractType": "pkwtt", "startDate": hrDay(0), "baseSalary": "1"},
		"Idempotency-Key", newKey()).JSON()
	if c := hr.Must(200, "POST", hrBase+"/contracts/"+str(dup["id"])+":cancel", map[string]any{"reason": "Duplicate"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}
	hr.Must(409, "POST", hrBase+"/contracts/"+str(dup["id"])+":activate", map[string]any{})
	// probation ends → Permanent (daily job)
	sysExec(t, inst, `UPDATE hris.contracts SET probation_end_date = current_date - 1 WHERE id = $1`, pc["id"])
	hrDaily(t)
	if got := hr.Must(200, "GET", hrBase+"/employees/"+str(n["id"]), nil).JSON(); got["employmentStatus"] != "permanent" {
		t.Fatalf("probation passed: %v", got)
	}
	// payroll reads the contract in force through the public API
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		c, err := hris.ContractAt(ctx, tx, uuid.MustParse(eid), time.Now().AddDate(0, 0, 1))
		if err != nil {
			return err
		}
		if c == nil || c.ContractType != "pkwtt" || c.FixedWage().String() != "6300000" {
			return fmt.Errorf("contract in force: %+v", c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// EP-02 FR-CTR-04: documents with validity, restricted access and expiry
// follow-up; warning letters feed service charge eligibility.
func TestP5HRDocuments(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	e := hrNewEmployee(t, hr, "Dokumen "+sfx, "WAITER", nil)
	eid := str(e["id"])
	fid := hrUploadPDF(t, hr, "ktp.pdf")
	bad, ctype := multipartBody(t, nil, "file", "x.txt", "plain text")
	hr.Must(422, "POST", hrBase+"/document-files", bad, "Content-Type", ctype)
	ktp := hr.Must(201, "POST", hrBase+"/employees/"+eid+"/documents", map[string]any{"documentType": "ktp", "documentNo": "3603010101900002",
		"issuedOn": hrDay(-1000), "fileId": fid}, "Idempotency-Key", newKey()).JSON()
	if ktp["title"] != "KTP" || ktp["validity"] != "no_expiry" || ktp["documentNo"] != "3603010101900002" {
		t.Fatalf("KTP: %v", ktp)
	}
	hr.Must(422, "POST", hrBase+"/employees/"+eid+"/documents", map[string]any{"documentType": "warning_letter"}) // SP level required
	sp := hr.Must(201, "POST", hrBase+"/employees/"+eid+"/documents", map[string]any{"documentType": "warning_letter", "warningLevel": 2,
		"issuedOn": hrDay(-10), "expiresOn": hrDay(170), "title": "SP2 test"}, "Idempotency-Key", newKey()).JSON()
	if sp["confidential"] != true {
		t.Fatal("warning letters are confidential")
	}
	medical := hr.Must(201, "POST", hrBase+"/employees/"+eid+"/documents", map[string]any{"documentType": "passport", "documentNo": "A1234567",
		"issuedOn": hrDay(-1800), "expiresOn": hrDay(20)}, "Idempotency-Key", newKey()).JSON()
	// restricted access: no confidential documents and masked numbers for the GM
	gmDocs := gm.Must(200, "GET", hrBase+"/employee-documents?employeeId="+eid, nil).Items()
	if len(gmDocs) != 2 {
		t.Fatalf("GM sees the non-confidential documents only: %v", gmDocs)
	}
	for _, d := range gmDocs {
		if d["documentType"] == "ktp" && d["documentNo"] != "************0002" {
			t.Fatalf("masked document number: %v", d["documentNo"])
		}
	}
	gm.Must(404, "GET", hrBase+"/employee-documents/"+str(sp["id"])+"/file", nil)
	if len(hr.Must(200, "GET", hrBase+"/employee-documents?employeeId="+eid, nil).Items()) != 3 {
		t.Fatal("HR sees every document")
	}
	if r := gm.Must(200, "GET", hrBase+"/employee-documents/"+str(ktp["id"])+"/file", nil); !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatal("document file")
	}
	// expiry follow-up and reminder (HR Configuration: H-30)
	expiring := hr.Must(200, "GET", hrBase+"/employee-documents?expiringWithin=30", nil).Items()
	found := false
	for _, d := range expiring {
		found = found || d["id"] == medical["id"]
	}
	if !found {
		t.Fatal("expiring documents list")
	}
	hrDaily(t)
	var sent []int32
	sysQueryRow(t, inst, `SELECT reminders_sent FROM hris.employee_documents WHERE id = $1`, []any{medical["id"]}, &sent)
	if len(sent) != 1 || sent[0] != 30 {
		t.Fatalf("document reminder: %v", sent)
	}
	upd := hr.Must(200, "PATCH", hrBase+"/employee-documents/"+str(medical["id"]), map[string]any{"expiresOn": hrDay(400), "documentNo": "**34567"}).JSON()
	if upd["validity"] != "valid" || upd["documentNo"] != "A1234567" {
		t.Fatalf("renewed passport: %v", upd)
	}
	hr.Must(422, "PATCH", hrBase+"/employee-documents/"+str(medical["id"]), map[string]any{"expiresOn": hrDay(-4000)})
	// service charge eligibility reads the warning level (PRD P5 §16 #5)
	ctx := dbtx.System(context.Background())
	var level int
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		level, err = hris.WarningLevel(ctx, tx, uuid.MustParse(eid), time.Now())
		return err
	}); err != nil || level != 2 {
		t.Fatalf("warning level: %d %v", level, err)
	}
	hr.Must(204, "DELETE", hrBase+"/employee-documents/"+str(sp["id"]), nil)
	hr.Must(404, "PATCH", hrBase+"/employee-documents/"+str(sp["id"]), map[string]any{"title": "x"})
}

// EP-04: certification types and certificates of employees and partners,
// reminders and hris.certification_expired (H7), enforcement in golf
// (Caddy Queue / assignment) and sport club (class schedules), training
// sessions issuing certificates, the mandatory training matrix.
func TestP5HRCertificationsAndTraining(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "golf.manager@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	sfx := hrSuffix()
	lg := hrNewEmployee(t, hr, "Penjaga Kolam "+sfx, "LIFEGUARD", nil)
	lgID := str(lg["id"])
	chk := hr.Must(200, "GET", hrBase+"/employees/"+lgID+"/certification-check", nil).JSON()
	if fmt.Sprint(chk["required"]) != "[CPR_BLS LIFEGUARD]" || len(chk["gaps"].([]any)) != 0 {
		t.Fatalf("no certificate on file, enforcement expired: %v", chk)
	}
	// HR Configuration "required": a missing certificate is a gap too
	hrPolicy(t, sa, hris.HRConfigurationCode, "HR Configuration", map[string]any{"certificationEnforcement": "required"})
	if gaps := hr.Must(200, "GET", hrBase+"/employees/"+lgID+"/certification-check", nil).JSON()["gaps"].([]any); len(gaps) != 2 {
		t.Fatalf("required mode: %v", gaps)
	}
	// an expired lifeguard certificate (FR-TRC-03 AC)
	lgType := hrID(t, "certification_types", "LIFEGUARD")
	old := hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": lgType, "employeeId": lgID, "issuedOn": hrDay(-731),
		"certificateNo": "LG-OLD-" + sfx}, "Idempotency-Key", newKey()).JSON()
	if old["expiresOn"] != addMonths(hrDay(-731), 24, -1) || old["holderName"] != lg["fullName"] || old["issuer"] != "Balawista Indonesia" {
		t.Fatalf("certificate from type: %v", old)
	}
	hr.Must(422, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": lgType}) // holder required
	cpr := hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "CPR_BLS"), "employeeId": lgID,
		"issuedOn": hrDay(-30)}, "Idempotency-Key", newKey()).JSON()
	gaps := hr.Must(200, "GET", hrBase+"/employees/"+lgID+"/certification-check", nil).JSON()["gaps"].([]any)
	if len(gaps) != 1 || gaps[0].(map[string]any)["reason"] != "expired" || gaps[0].(map[string]any)["typeCode"] != "LIFEGUARD" {
		t.Fatalf("expired lifeguard certificate: %v", gaps)
	}
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		c, err := hris.CheckEmployee(ctx, tx, uuid.MustParse(lgID), "", time.Now())
		if err == nil && c.Err("Lifeguard") == nil {
			err = fmt.Errorf("the scheduling check must refuse the lifeguard: %+v", c)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// H7: the daily job marks it Expired and publishes the event once
	hrDaily(t)
	if hr.Must(200, "GET", hrBase+"/certifications/"+str(old["id"]), nil).JSON()["status"] != "expired" ||
		hrEvents(t, hris.EventCertificationExpired, str(old["id"])) != 1 {
		t.Fatal("hris.certification_expired")
	}
	hrDaily(t)
	if hrEvents(t, hris.EventCertificationExpired, str(old["id"])) != 1 {
		t.Fatal("expired event published twice")
	}
	status := hr.Must(200, "GET", hrBase+"/certification-status?employeeId="+lgID+"&validity=expired", nil).Items()
	if len(status) != 1 {
		t.Fatalf("certification status: %v", status)
	}
	// compliance per workforce role
	comp := hr.Must(200, "GET", hrBase+"/certification-compliance?role=lifeguard", nil).Items()
	foundGap := false
	for _, c := range comp {
		if c["holderId"] == lgID {
			foundGap = c["compliant"] == false
		}
	}
	if !foundGap {
		t.Fatalf("compliance: %v", comp)
	}

	// Training: a refresher session issues the new certificate (FR-TRC-04)
	prog := hr.Must(201, "POST", hrBase+"/training-programs", map[string]any{"code": "LG-T" + sfx, "name": "Lifeguard Test Course", "category": "safety",
		"certificationTypeId": lgType, "requiredPositions": []string{"LIFEGUARD"}, "costPerParticipant": "400000", "refresherMonths": 24},
		"Idempotency-Key", newKey()).JSON()
	hr.Must(422, "POST", hrBase+"/training-programs", map[string]any{"code": "BAD" + sfx, "name": "Bad", "requiredPositions": []string{"NOPE"}})
	start := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Hour)
	hr.Must(422, "POST", hrBase+"/training-sessions", map[string]any{"programId": prog["id"], "startsAt": start.Format(time.RFC3339),
		"endsAt": start.Add(-time.Hour).Format(time.RFC3339)})
	sess := hr.Must(201, "POST", hrBase+"/training-sessions", map[string]any{"programId": prog["id"], "startsAt": start.Format(time.RFC3339),
		"endsAt": start.Add(6 * time.Hour).Format(time.RFC3339), "location": "Pool", "capacity": 3}, "Idempotency-Key", newKey()).JSON()
	if sess["title"] != "Lifeguard Test Course" || sess["status"] != "planned" {
		t.Fatalf("session: %v", sess)
	}
	hr.Must(200, "PATCH", hrBase+"/training-sessions/"+str(sess["id"]), map[string]any{"trainer": "Coach"})
	hr.Must(422, "PATCH", hrBase+"/training-sessions/"+str(sess["id"]), map[string]any{"status": "completed"})
	p2 := hrNewEmployee(t, hr, "Peserta Dua "+sfx, "LIFEGUARD", nil)
	p3 := hrNewEmployee(t, hr, "Peserta Tiga "+sfx, "LIFEGUARD", nil)
	parts := hr.Must(200, "POST", hrBase+"/training-sessions/"+str(sess["id"])+"/participants", map[string]any{"employeeIds": []string{lgID, str(p2["id"]),
		str(p3["id"])}}).Items()
	if len(parts) != 3 {
		t.Fatalf("participants: %v", parts)
	}
	extra := hrNewEmployee(t, hr, "Peserta Empat "+sfx, "LIFEGUARD", nil)
	hr.Must(409, "POST", hrBase+"/training-sessions/"+str(sess["id"])+"/participants", map[string]any{"employeeIds": []string{str(extra["id"])}}) // full
	byEmp := map[string]string{}
	for _, p := range parts {
		byEmp[str(p["employeeId"])] = str(p["id"])
	}
	hr.Must(422, "PATCH", hrBase+"/training-participants/"+byEmp[lgID], map[string]any{"result": "passed"}) // attend first
	pp := hr.Must(200, "PATCH", hrBase+"/training-participants/"+byEmp[lgID], map[string]any{"attendance": "attended", "result": "passed", "score": "88.5"}).JSON()
	if pp["result"] != "passed" || pp["score"] != "88.5" {
		t.Fatalf("result: %v", pp)
	}
	hr.Must(200, "PATCH", hrBase+"/training-participants/"+byEmp[str(p2["id"])], map[string]any{"attendance": "attended", "result": "failed"})
	hr.Must(204, "DELETE", hrBase+"/training-participants/"+byEmp[str(p3["id"])], nil)
	res := hr.Must(200, "POST", hrBase+"/training-sessions/"+str(sess["id"])+":complete", map[string]any{}).JSON()
	if res["attended"] != float64(2) || res["passed"] != float64(1) || res["costTotal"] != "800000" || len(res["certifications"].([]any)) != 1 {
		t.Fatalf("complete: %v", res)
	}
	hr.Must(409, "POST", hrBase+"/training-sessions/"+str(sess["id"])+":complete", map[string]any{})
	hr.Must(409, "PATCH", hrBase+"/training-sessions/"+str(sess["id"]), map[string]any{"trainer": "x"})
	if gaps := hr.Must(200, "GET", hrBase+"/employees/"+lgID+"/certification-check", nil).JSON()["gaps"].([]any); len(gaps) != 0 {
		t.Fatalf("renewed by training: %v", gaps)
	}
	if hr.Must(200, "GET", hrBase+"/certifications/"+str(old["id"]), nil).JSON()["status"] != "renewed" {
		t.Fatal("the expired certificate is Renewed")
	}
	matrix := hr.Must(200, "GET", hrBase+"/training-matrix?orgUnitId="+hrID(t, "org_units", "SPORT"), nil).Items()
	var lgRow, p2Row string
	for _, m := range matrix {
		if m["programId"] == prog["id"] && m["employeeId"] == lgID {
			lgRow = str(m["status"])
		}
		if m["programId"] == prog["id"] && m["employeeId"] == p2["id"] {
			p2Row = str(m["status"])
		}
	}
	if lgRow != "compliant" || p2Row != "missing" {
		t.Fatalf("training matrix: %s / %s", lgRow, p2Row)
	}
	// revocation
	newCert := str(res["certifications"].([]any)[0])
	rv := hr.Must(200, "POST", hrBase+"/certifications/"+newCert+":revoke", map[string]any{"reason": "Fraudulent document"}).JSON()
	if rv["status"] != "revoked" {
		t.Fatalf("revoke: %v", rv)
	}
	hr.Must(409, "POST", hrBase+"/certifications/"+newCert+":revoke", map[string]any{"reason": "again"})
	hr.Must(409, "PATCH", hrBase+"/certifications/"+newCert, map[string]any{"notes": "x"})
	// certificate edits and archive; a planned session can be removed
	hr.Must(200, "PATCH", hrBase+"/certifications/"+str(cpr["id"]), map[string]any{"notes": "Renew with PMI", "certificateNo": "CPR-" + sfx})
	hr.Must(204, "DELETE", hrBase+"/certifications/"+str(cpr["id"]), nil)
	spare := hr.Must(201, "POST", hrBase+"/training-sessions", map[string]any{"programId": prog["id"], "startsAt": start.Add(30 * 24 * time.Hour).Format(time.RFC3339),
		"endsAt": start.Add(30*24*time.Hour + 2*time.Hour).Format(time.RFC3339)}, "Idempotency-Key", newKey()).JSON()
	hr.Must(204, "DELETE", hrBase+"/training-sessions/"+str(spare["id"]), nil)

	// FR-TRC-03 AC: a caddy with an expired certificate is not offered in
	// the Caddy Queue and cannot be assigned.
	caddy := gm.Must(201, "POST", "/api/v1/golf/caddies", map[string]any{"code": "HRC" + sfx, "name": "Caddy Expired " + sfx, "gender": "female"}).JSON()
	t.Cleanup(func() { sa.Do("PATCH", "/api/v1/golf/caddies/"+str(caddy["id"]), map[string]any{"status": "inactive"}) })
	cc := hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "CADDY"), "holderKind": "caddy",
		"partnerId": caddy["id"], "issuedOn": hrDay(-800), "expiresOn": hrDay(-1)}, "Idempotency-Key", newKey()).JSON()
	if cc["holderName"] != caddy["name"] {
		t.Fatalf("partner holder: %v", cc)
	}
	hr.Must(422, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "CADDY"), "holderKind": "caddy",
		"partnerId": uuid.NewString(), "issuedOn": hrDay(-1)})
	day := clubDay(inst, 45, isWeekday)
	gm.Must(200, "PUT", "/api/v1/golf/caddy-availability", map[string]any{"date": day, "entries": []map[string]any{{"caddyId": caddy["id"], "status": "present"}}})
	for _, c := range gm.Must(200, "GET", "/api/v1/golf/caddy-availability?date="+day, nil).Items() {
		if c["caddyId"] == caddy["id"] && c["status"] != "not_available" {
			t.Fatalf("uncertified caddy offered in the queue: %v", c)
		}
	}
	slots := slotsOf(teeTimes(t, gm, demoCourse(t, inst), day), "afternoon", 1)
	bk := gm.Must(201, "POST", "/api/v1/golf/bookings", map[string]any{"bookingType": "non_member", "channel": "walk_in", "teeTimeId": slots[len(slots)-1]["id"],
		"contactName": "HR Test", "contactPhone": "+628129990777", "players": []map[string]any{{"playerType": "non_member", "name": "HR Test",
			"phone": "+628129990777"}, {"playerType": "non_member", "name": "HR Test Two", "phone": "+628129990778"}}}).JSON()
	t.Cleanup(func() {
		sa.Do("POST", "/api/v1/golf/bookings/"+str(bk["id"])+":cancel", map[string]any{"reason": "test cleanup", "waiveFee": true})
	})
	r := gm.Do("POST", "/api/v1/golf/caddy-assignments", map[string]any{"flightId": firstFlight(bk), "assignments": []map[string]any{{"caddyId": caddy["id"],
		"playerIds": playerIDs(bk)[:1]}}})
	if r.Status != 409 || !strings.Contains(string(r.Body), "caddy_not_certified") {
		t.Fatalf("assignment of an uncertified caddy: %s", r)
	}
	// renewed: assignable again
	hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "CADDY"), "holderKind": "caddy",
		"partnerId": caddy["id"], "issuedOn": hrDay(0)}, "Idempotency-Key", newKey())
	for _, c := range gm.Must(200, "GET", "/api/v1/golf/caddy-availability?date="+day, nil).Items() {
		if c["caddyId"] == caddy["id"] && c["status"] != "available" {
			t.Fatalf("renewed caddy available: %v", c)
		}
	}

	// sport club: no class schedule for an instructor whose certificate expired
	instr := sa.Must(201, "POST", "/api/v1/sportclub/instructors", map[string]any{"code": "HRI" + sfx, "name": "Coach Expired " + sfx,
		"disciplines": []string{"aerobic"}}).JSON()
	hr.Must(201, "POST", hrBase+"/certifications", map[string]any{"certificationTypeId": hrID(t, "certification_types", "SPORT_INSTRUCTOR"),
		"holderKind": "instructor", "partnerId": instr["id"], "issuedOn": hrDay(-800), "expiresOn": hrDay(-2)}, "Idempotency-Key", newKey())
	prg := sa.Must(201, "POST", "/api/v1/sportclub/class-programs", map[string]any{"code": "HRP" + sfx, "name": "HR Aerobic", "discipline": "aerobic",
		"capacity": 10}).JSON()
	sr := sa.Do("POST", "/api/v1/sportclub/class-schedules", map[string]any{"programId": prg["id"], "instructorId": instr["id"], "weekdays": []int{1, 2, 3, 4, 5, 6, 7},
		"startTime": "05:00", "startDate": hrDay(60), "endDate": hrDay(66)})
	if sr.Status != 409 || !strings.Contains(string(sr.Body), "certification_invalid") {
		t.Fatalf("class schedule of an uncertified instructor: %s", sr)
	}
}

// addMonths returns date + n months + d days (YYYY-MM-DD).
func addMonths(date string, n, d int) string {
	t, _ := time.Parse("2006-01-02", date)
	return t.AddDate(0, n, d).Format("2006-01-02")
}

// EP-16 / EP-26 FR-OPS-P5-01: Employee Self Service (profile, documents,
// training, data changes verified by HR, manager's team).
func TestP5HRSelfService(t *testing.T) {
	emp := login(t, inst, "employee@demo.oneclub.id", demoPassword)
	head := login(t, inst, "dept.head@demo.oneclub.id", demoPassword)
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	me := emp.Must(200, "GET", "/api/v1/ess/me", nil).JSON()
	self := me["employee"].(map[string]any)
	if self["employeeNo"] != "EMP-00021" || !strings.HasPrefix(str(self["nik"]), "****") || me["isManager"] != false {
		t.Fatalf("ESS me: %v", me)
	}
	keys := map[string]bool{}
	for _, s := range me["sections"].([]any) {
		keys[str(s.(map[string]any)["key"])] = true
	}
	if !keys["profile"] || !keys["documents"] || !keys["training"] || keys["team"] {
		t.Fatalf("sections: %v", keys)
	}
	if docs := emp.Must(200, "GET", "/api/v1/ess/documents", nil).Items(); len(docs) < 3 {
		t.Fatalf("my documents: %v", docs)
	}
	tr := emp.Must(200, "GET", "/api/v1/ess/training", nil).JSON()
	if len(tr["sessions"].([]any)) == 0 {
		t.Fatalf("my training: %v", tr)
	}
	emp.Must(403, "GET", "/api/v1/ess/team", nil)
	emp.Must(403, "GET", hrBase+"/employees", nil) // no HR data
	// a role without an employee profile has no ESS
	roleUser(t, inst, "cashier").Must(404, "GET", "/api/v1/ess/me", nil)
	// ops navigation has the ESS area
	nav := emp.Must(200, "GET", "/api/v1/platform/navigation?shell=ops", nil).JSON()["items"].([]any)
	hasESS := false
	for _, it := range nav {
		hasESS = hasESS || it.(map[string]any)["key"] == "ess"
	}
	if !hasESS {
		t.Fatal("ops menu has Employee Self Service")
	}

	// FR-ESS-05: the department head's team
	hm := head.Must(200, "GET", "/api/v1/ess/me", nil).JSON()
	if hm["isManager"] != true || hm["teamSize"].(float64) < 5 {
		t.Fatalf("department head: %v", hm)
	}
	team := head.Must(200, "GET", "/api/v1/ess/team", nil).Items()
	var waiter map[string]any
	for _, m := range team {
		if m["employeeNo"] == "EMP-00021" {
			waiter = m
		}
	}
	if waiter == nil || waiter["contractEnds"] == nil {
		t.Fatalf("team: %v", team)
	}

	// FR-ESS-06: data change → HR verifies → applied
	emp.Must(422, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{}})
	emp.Must(422, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"bankAccount": map[string]any{"bankName": "BCA",
		"accountNo": "abc", "accountName": "Andi"}}})
	ch := emp.Must(201, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"phone": "+6281299990021", "address": "Jl. Baru 1",
		"emergencyContacts": []map[string]any{{"name": "Ibu Andi", "relationship": "parent", "phone": "+62811000021"}},
		"bankAccount":       map[string]any{"bankName": "BNI", "accountNo": "0099887766", "accountName": "Andi Kurniawan"}}}, "Idempotency-Key", newKey()).JSON()
	if ch["status"] != "submitted" || len(ch["fields"].([]any)) != 4 {
		t.Fatalf("data change: %v", ch)
	}
	emp.Must(409, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"city": "Jakarta"}})
	list := hr.Must(200, "GET", hrBase+"/profile-changes?status=submitted", nil).Items()
	found := false
	for _, c := range list {
		found = found || c["id"] == ch["id"]
	}
	if !found {
		t.Fatal("HR sees the data change")
	}
	hr.Must(200, "POST", hrBase+"/profile-changes/"+str(ch["id"])+":approve", map[string]any{"note": "Verified with the bank book"})
	hr.Must(409, "POST", hrBase+"/profile-changes/"+str(ch["id"])+":approve", map[string]any{})
	after := emp.Must(200, "GET", "/api/v1/ess/me", nil).JSON()["employee"].(map[string]any)
	if after["phone"] != "+6281299990021" || after["bankName"] != "BNI" || after["bankAccountNo"] != "******7766" ||
		len(after["emergencyContacts"].([]any)) != 1 {
		t.Fatalf("applied: %v", after)
	}
	// withdraw and reject
	c2 := emp.Must(201, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"city": "Jakarta"}}, "Idempotency-Key", newKey()).JSON()
	if w := emp.Must(200, "POST", "/api/v1/ess/profile-changes/"+str(c2["id"])+":cancel", map[string]any{}).JSON(); w["status"] != "cancelled" {
		t.Fatalf("withdraw: %v", w)
	}
	c3 := emp.Must(201, "POST", "/api/v1/ess/profile-changes", map[string]any{"changes": map[string]any{"ptkpStatus": "K/2"}}, "Idempotency-Key", newKey()).JSON()
	hr.Must(422, "POST", hrBase+"/profile-changes/"+str(c3["id"])+":reject", map[string]any{"note": ""})
	if rj := hr.Must(200, "POST", hrBase+"/profile-changes/"+str(c3["id"])+":reject", map[string]any{"note": "Attach the family card"}).JSON(); rj["status"] != "rejected" {
		t.Fatalf("reject: %v", rj)
	}
	if mine := emp.Must(200, "GET", "/api/v1/ess/profile-changes", nil).Items(); len(mine) < 3 {
		t.Fatalf("my data changes: %v", mine)
	}
	// own document download
	docs := emp.Must(200, "GET", "/api/v1/ess/documents", nil).Items()
	emp.Must(404, "GET", "/api/v1/ess/documents/"+str(docs[0]["id"])+"/file", nil) // no file on the demo KTP
	emp.Must(404, "GET", "/api/v1/ess/documents/"+uuid.NewString()+"/file", nil)
}

// EP-28 FR-MIG-P5-01/04: CSV imports of employees, contracts and
// certifications (dry run, upsert, issues).
func TestP5HRImport(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	emps := "employeeNo,fullName,orgUnitCode,positionCode,gradeCode,supervisorNo,employmentStatus,joinDate,gender,nik,ptkpStatus,email,bankName,accountNo\n" +
		"IMP" + sfx + "A,Impor Satu,FNB,WAITER,G1,EMP-00005,contract,2025-02-01,male,3603019901010001,TK/0,imp1" + sfx + "@club.test,BCA,1112223334\n" +
		"IMP" + sfx + "B,Impor Dua,KITCHEN,COOK,,IMP" + sfx + "A,permanent,2024-05-01,female,,K/1,,,\n" +
		"IMP" + sfx + "C,Bad Unit,NOPE,,,,,2024-05-01,,,,,,\n"
	dry := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "employees", "csv": emps, "dryRun": true}).JSON()
	if dry["inserted"] != float64(2) || dry["failed"] != float64(1) {
		t.Fatalf("dry run: %v", dry)
	}
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.employees WHERE employee_no LIKE $1`, []any{"IMP" + sfx + "%"}, &n)
	if n != 0 {
		t.Fatal("a dry run saves nothing")
	}
	rep := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "employees", "csv": emps}).JSON()
	if rep["inserted"] != float64(2) || rep["failed"] != float64(1) || len(rep["issues"].([]any)) != 1 {
		t.Fatalf("employees: %v", rep)
	}
	again := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "employees", "csv": emps}).JSON()
	if again["updated"] != float64(2) {
		t.Fatalf("repeatable: %v", again)
	}
	b := hrEmployeeID(t, "IMP"+sfx+"B")
	var sup string
	sysQueryRow(t, inst, `SELECT e2.employee_no FROM hris.employees e JOIN hris.employees e2 ON e2.id = e.supervisor_id WHERE e.id = $1`, []any{b}, &sup)
	if sup != "IMP"+sfx+"A" {
		t.Fatalf("supervisor: %s", sup)
	}
	ctr := "employeeNo,contractType,startDate,endDate,baseSalary,allowances,status\n" +
		"IMP" + sfx + "A,pkwt,2025-02-01," + hrDay(120) + ",5300000,MEAL:Meal:400000;TRANSPORT:Transport:300000,active\n" +
		"IMP" + sfx + "B,pkwtt,2024-05-01,,6100000,,active\n" +
		"IMP" + sfx + "B,pkwt,2020-01-01,2026-01-01,1,,active\n"
	cr := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "contracts", "csv": ctr}).JSON()
	if cr["inserted"] != float64(2) || cr["failed"] != float64(1) {
		t.Fatalf("contracts: %v", cr)
	}
	if again := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "contracts", "csv": ctr}).JSON(); again["skipped"] != float64(2) {
		t.Fatalf("contracts repeatable: %v", again)
	}
	cert := "holderKind,employeeNo,partnerCode,typeCode,certificateNo,issuedOn,expiresOn\n" +
		"employee,IMP" + sfx + "B,,FOOD_HANDLER,FH-" + sfx + ",2024-06-01,\n" +
		"caddy,,C001,CADDY,CDY-IMP-" + sfx + "," + hrDay(-10) + ",\n" +
		"caddy,,NOPE,CADDY,X,2024-01-01,\n"
	ce := hr.Must(200, "POST", hrBase+"/imports", map[string]any{"entity": "certifications", "csv": cert}).JSON()
	if ce["inserted"] != float64(2) || ce["failed"] != float64(1) {
		t.Fatalf("certifications: %v", ce)
	}
	// the CLI path (`oneclub import hris`): a system context on the property
	sys := reqctx.WithProperty(dbtx.System(context.Background()), inst.Main)
	cli, err := inst.App.HR.Module.ImportCSV(sys, inst.Main, "employees", strings.NewReader("employeeNo,fullName,positionCode,joinDate\n"+
		"CLI"+sfx+",Lewat CLI,STARTER,2025-01-02\n"), false)
	if err != nil || cli.Inserted != 1 {
		t.Fatalf("CLI import: %+v %v", cli, err)
	}
	hr.Must(422, "POST", hrBase+"/imports", map[string]any{"entity": "employees", "csv": "unknownColumn\nx\n"})
	hr.Must(422, "POST", hrBase+"/imports", map[string]any{"entity": "payroll", "csv": "a\nb\n"})
}

// EP-27 FR-RPT-P5-02 (core HR part): Headcount, Turnover, Contract Expiry,
// Certification Expiry and Training reports for HR and the GM.
func TestP5HRReports(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	for _, code := range []string{"hris.headcount", "hris.turnover", "hris.contract_expiry", "hris.certification_expiry", "hris.training"} {
		r := hr.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+hrDay(-120)+"&params[to]="+hrDay(0), nil).JSON()
		if len(r["rows"].([]any)) == 0 {
			t.Errorf("report %s has no rows", code)
		}
	}
	head := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.headcount?params[to]="+hrDay(0), nil).JSON()["rows"].([]any)
	total := 0
	for _, r := range head {
		total += int(r.(map[string]any)["active"].(float64))
	}
	if total < 39 {
		t.Fatalf("headcount: %d", total)
	}
	turn := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.turnover?params[from]="+hrDay(-90)+"&params[to]="+hrDay(0), nil).JSON()["rows"].([]any)
	leavers := 0
	for _, r := range turn {
		leavers += int(r.(map[string]any)["leavers"].(float64))
	}
	if leavers < 1 {
		t.Fatalf("turnover: %v", turn)
	}
	roleUser(t, inst, "cashier").Must(403, "GET", "/api/v1/reporting/reports/hris.headcount", nil)
	// the HR Performance KPIs prepared for the BI registry
	for _, k := range reporting.HRCoreKPIs {
		var v string
		sysQueryRow(t, inst, k.SQL, []any{hrDay(-30), hrDay(0), "Asia/Jakarta"}, &v)
		if k.Key == "headcount" && dec(v).IntPart() < 39 {
			t.Fatalf("headcount KPI: %s", v)
		}
		if k.Key == "certification_compliance" && (dec(v).IsZero() || dec(v).GreaterThanOrEqual(dec("1"))) {
			t.Fatalf("certification compliance (demo has expired certificates): %s", v)
		}
		if k.Breakdown != "" {
			sysQueryRow(t, inst, `SELECT count(*)::text FROM (`+k.Breakdown+`) b`, []any{hrDay(-30), hrDay(0), "Asia/Jakarta"}, &v)
		}
	}
}
