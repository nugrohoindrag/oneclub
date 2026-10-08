package e2e

// HRIS improvement phase B (docs/HRIS_Product_Requirements_UI_Backend_Audit.md
// §39.4): Request Revision in the approval engine, loans and cash advances
// (request, approval, payment by Finance with its journal, repayment,
// statement, Employee Self Service), the payroll exception queue and the
// Finance & Accounting posting status of a run, the employee lifecycle
// statuses (draft, suspended, on leave) and workforce plans per department.

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/hris/payroll"
)

// TestP5LoanRequestsWithRevision: HR requests a loan through the approval
// workflow; Finance returns it for revision, HR adjusts and resubmits,
// Finance approves and pays it (journal Dr employee receivables / Cr bank),
// the employee repays part in cash (Dr cash / Cr receivables); a second
// request is cancelled after approval, an ESS request is withdrawn.
func TestP5LoanRequestsWithRevision(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	emp := prEmployee(t, tm, "Lina Loan", "TK/0", prMonth(14, nil), "6000000", nil, "BCA", true, nil)
	hr := prUser(t, "hr_manager")
	fin := prUser(t, "finance_manager")

	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": payroll.LoanDocumentType.Code,
		"name": "Loans MDR " + tm.sfx, "propertyId": inst.MDR, "steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "role",
			"approverRoleId": roleID(t, sa, "finance_manager")}}}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})

	// a loan entered through the resource is already paid; requests use :request
	sa.Must(422, "POST", hrBase+"/employee-loans", map[string]any{"employeeId": emp, "principal": "100000", "installment": "100000",
		"startPeriod": prMonth(0, nil).Format("2006-01"), "status": "submitted"}, "Idempotency-Key", newKey())
	hr.Must(422, "POST", hrBase+"/employee-loans:request", map[string]any{"employeeId": emp, "loanType": "loan", "principal": "3000000"},
		"Idempotency-Key", newKey()) // purpose required
	loan := hr.Must(201, "POST", hrBase+"/employee-loans:request", map[string]any{"employeeId": emp, "loanType": "loan", "principal": "3000000",
		"installment": "500000", "purpose": "Motorbike repair"}, "Idempotency-Key", newKey()).JSON()
	if loan["status"] != "submitted" || loan["approvalRequestId"] == nil || !strings.HasPrefix(str(loan["number"]), "LOAN-") ||
		loan["installments"] != float64(6) || loan["requestSource"] != "hr" {
		t.Fatalf("loan request: %v", loan)
	}
	lid, rid := str(loan["id"]), str(loan["approvalRequestId"])
	if q := hr.Must(200, "GET", hrBase+"/dashboard", nil).JSON(); !hasQueue(q, "loan_requests") {
		t.Fatalf("dashboard loan requests: %v", q["attention"])
	}
	// the approver cannot pay before approval; HR cannot request a revision of its own request
	fin.Must(409, "POST", hrBase+"/employee-loans/"+lid+":disburse", map[string]any{"disbursedOn": hrDay(0), "method": "bank_transfer"})
	hr.Must(403, "POST", "/api/v1/platform/approvals/"+rid+":request-revision", map[string]any{"reason": "x"})
	fin.Must(422, "POST", "/api/v1/platform/approvals/"+rid+":request-revision", map[string]any{})

	// Request Revision: back to draft with the reason, steps rebuilt on resubmission
	rv := fin.Must(200, "POST", "/api/v1/platform/approvals/"+rid+":request-revision", map[string]any{"reason": "Six installments are too long"}).JSON()
	if rv["status"] != "draft" || str(rv["decisionReason"]) != "Six installments are too long" || rv["steps"] != nil {
		t.Fatalf("revision requested: %v", rv)
	}
	if v := hr.Must(200, "GET", hrBase+"/employee-loans/"+lid, nil).JSON(); v["status"] != "submitted" {
		t.Fatalf("loan while revised: %v", v)
	}
	hr.Must(200, "PATCH", hrBase+"/employee-loans/"+lid, map[string]any{"installment": "1000000"})
	re := hr.Must(200, "POST", "/api/v1/platform/approvals/"+rid+":submit", map[string]any{}).JSON()
	if re["status"] != "pending" || len(re["steps"].([]any)) != 1 {
		t.Fatalf("resubmitted: %v", re)
	}
	ap := fin.Must(200, "POST", "/api/v1/platform/approvals/"+rid+":approve", map[string]any{"reason": "ok"}).JSON()
	actions := []string{}
	for _, h := range ap["history"].([]any) {
		actions = append(actions, str(h.(map[string]any)["action"]))
	}
	if strings.Join(actions, ",") != "create,approval_revision_requested,approval_submitted,approval_approved" {
		t.Fatalf("approval history: %v", actions)
	}
	loan = hr.Must(200, "GET", hrBase+"/employee-loans/"+lid+"/statement", nil).JSON()["loan"].(map[string]any)
	if loan["status"] != "approved" || loan["installments"] != float64(3) {
		t.Fatalf("approved loan: %v", loan)
	}
	hr.Must(422, "PATCH", hrBase+"/employee-loans/"+lid, map[string]any{"principal": "5000000"}) // approved as submitted

	// Finance pays it: active, payroll deducts from now on; journal Dr 1145 / Cr bank
	hr.Must(403, "POST", hrBase+"/employee-loans/"+lid+":disburse", map[string]any{"disbursedOn": hrDay(0), "method": "bank_transfer"})
	fin.Must(422, "POST", hrBase+"/employee-loans/"+lid+":disburse", map[string]any{"disbursedOn": hrDay(0), "method": "cheque"})
	paid := fin.Must(200, "POST", hrBase+"/employee-loans/"+lid+":disburse", map[string]any{"disbursedOn": hrDay(0), "method": "bank_transfer",
		"reference": "TRF-" + tm.sfx}).JSON()
	if paid["status"] != "active" || paid["disbursementMethod"] != "bank_transfer" || str(paid["outstanding"]) != "3000000" {
		t.Fatalf("paid: %v", paid)
	}
	accDispatch(t)
	ev := prEvent(t, payroll.EventLoanDisbursed, lid)
	if s, n := accProcessed(t, ev); s != "posted" || n != 1 {
		t.Fatalf("loan payment journal: %s %d", s, n)
	}
	j := accLines(accJournalOf(t, fin, ev))
	prEq(t, "employee receivable 1145", j["1145"], di(3_000_000))
	prEq(t, "bank 1121", j["1121"], di(-3_000_000))

	// repayment outside payroll (cash returned): Dr cash / Cr receivables
	fin.Must(422, "POST", hrBase+"/employee-loans/"+lid+":repay", map[string]any{"paidOn": hrDay(0), "amount": "3500000", "method": "cash"})
	rp := fin.Must(200, "POST", hrBase+"/employee-loans/"+lid+":repay", map[string]any{"paidOn": hrDay(0), "amount": "1000000", "method": "cash",
		"reference": "KW-" + tm.sfx}).JSON()
	if str(rp["outstanding"]) != "2000000" || rp["status"] != "active" || rp["installments"] != float64(2) {
		t.Fatalf("repaid: %v", rp)
	}
	accDispatch(t)
	ev = prEvent(t, payroll.EventLoanRepaid, lid)
	j = accLines(accJournalOf(t, fin, ev))
	prEq(t, "cash 1111", j["1111"], di(1_000_000))
	prEq(t, "employee receivable 1145 repaid", j["1145"], di(-1_000_000))
	st := fin.Must(200, "GET", hrBase+"/employee-loans/"+lid+"/statement", nil).JSON()
	mv := st["movements"].([]any)
	if len(mv) != 2 || mv[0].(map[string]any)["kind"] != "disbursement" || str(mv[1].(map[string]any)["balance"]) != "2000000" {
		t.Fatalf("statement: %v", mv)
	}
	if list := fin.Must(200, "GET", hrBase+"/loans?status=active&employeeId="+emp, nil).Items(); len(list) != 1 || list[0]["id"] != lid {
		t.Fatalf("loans list: %v", list)
	}

	// a second request: approved, then cancelled before payment
	ca := hr.Must(201, "POST", hrBase+"/employee-loans:request", map[string]any{"employeeId": emp, "loanType": "cash_advance", "principal": "750000",
		"purpose": "Tournament travel"}, "Idempotency-Key", newKey()).JSON()
	if !strings.HasPrefix(str(ca["number"]), "CADV-") || str(ca["installment"]) != "750000" {
		t.Fatalf("cash advance: %v", ca)
	}
	fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(ca["approvalRequestId"])+":approve", map[string]any{})
	if q := hr.Must(200, "GET", hrBase+"/dashboard", nil).JSON(); !hasQueue(q, "loans_to_pay") {
		t.Fatalf("dashboard loans to pay: %v", q["attention"])
	}
	hr.Must(422, "POST", hrBase+"/employee-loans/"+str(ca["id"])+":cancel", map[string]any{})
	if c := hr.Must(200, "POST", hrBase+"/employee-loans/"+str(ca["id"])+":cancel", map[string]any{"note": "Trip cancelled"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", c)
	}
	hr.Must(409, "POST", hrBase+"/employee-loans/"+lid+":cancel", map[string]any{"note": "too late"})

	// Employee Self Service: request and withdraw
	prRole(t, "lifeguard")
	ess := ttLink(t, "lifeguard", emp)
	mine := ess.Must(201, "POST", "/api/v1/ess/loans", map[string]any{"loanType": "cash_advance", "principal": "400000", "purpose": "Medical"},
		"Idempotency-Key", newKey()).JSON()
	if mine["status"] != "submitted" || mine["requestSource"] != "ess" || mine["employeeId"] != emp {
		t.Fatalf("ESS request: %v", mine)
	}
	if list := ess.Must(200, "GET", "/api/v1/ess/loans", nil).Items(); len(list) != 3 {
		t.Fatalf("my loans: %d", len(list))
	}
	if s := ess.Must(200, "GET", "/api/v1/ess/loans/"+lid+"/statement", nil).JSON(); len(s["movements"].([]any)) != 2 {
		t.Fatalf("my statement: %v", s)
	}
	if w := ess.Must(200, "POST", "/api/v1/ess/loans/"+str(mine["id"])+":cancel", map[string]any{"note": "Covered by insurance"}).JSON(); w["status"] != "cancelled" {
		t.Fatalf("withdrawn: %v", w)
	}
	ess.Must(404, "GET", "/api/v1/ess/loans/"+uuid.NewString()+"/statement", nil)
}

func hasQueue(d map[string]any, key string) bool {
	for _, x := range d["attention"].([]any) {
		if x.(map[string]any)["key"] == key {
			return true
		}
	}
	return false
}

// TestP5PayrollExceptionsAndFinanceStatus: the exception queue of a run
// (warnings; the employee in another run of the period and type is an error
// that blocks the submission) and the Posted to Finance / Posting Failed
// status that follows accounting's events.
func TestP5PayrollExceptionsAndFinanceStatus(t *testing.T) {
	tm := prSetup(t)
	emp := prEmployee(t, tm, "Exa Exception", "TK/0", prMonth(14, nil), "5000000", nil, "", true, nil)
	hr := prUser(t, "hr_manager")
	fin := prUser(t, "finance_manager")
	period := prMonth(7, notDecember).Format("2006-01")
	// each run pays one approved adjustment of the employee (an adjustment run pays adjustments only)
	newRun := func() string {
		hr.Must(201, "POST", hrBase+"/payroll-adjustments", map[string]any{"employeeId": emp, "componentCode": "BONUS", "amount": "100000",
			"periodCode": period, "targetRunType": "adjustment", "reason": "Exception queue test"}, "Idempotency-Key", newKey())
		rid := idOf(hr.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "adjustment", "periodCode": period, "orgUnitId": tm.unitID},
			"Idempotency-Key", newKey()))
		hr.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{})
		return rid
	}
	first := newRun()
	codes := func(rid string) map[string]string {
		out := map[string]string{}
		for _, x := range hr.Must(200, "GET", hrBase+"/payroll-runs/"+rid+"/exceptions", nil).Items() {
			if x["employeeId"] == emp {
				out[str(x["code"])] = str(x["severity"])
			}
		}
		return out
	}
	if c := codes(first); c["missing_bank"] != "warning" || c["duplicate_payroll"] != "" {
		t.Fatalf("exceptions of the first run: %v", c)
	}
	second := newRun()
	if c := codes(second); c["duplicate_payroll"] != "error" {
		t.Fatalf("exceptions of the second run: %v", c)
	}
	if e := hr.Must(200, "GET", hrBase+"/payroll-runs/"+second+"/exceptions?severity=error", nil).Items(); len(e) == 0 {
		t.Fatal("error filter")
	}
	hr.Must(409, "POST", hrBase+"/payroll-runs/"+second+":approve", map[string]any{})
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+second+":cancel", map[string]any{"note": "duplicate"})
	if c := codes(first); c["duplicate_payroll"] != "" {
		t.Fatalf("revalidated: %v", c)
	}
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+first+":approve", map[string]any{})
	run := fin.Must(200, "POST", hrBase+"/payroll-runs/"+first+":post", map[string]any{}).JSON()
	if run["financeStatus"] != "pending" {
		t.Fatalf("posted run waits for Finance: %v", run["financeStatus"])
	}

	// Accounting books the payroll journal of the run: Posted to Finance
	var postEvent uuid.UUID
	sysQueryRow(t, inst, `SELECT posting_event_id FROM hris.payroll_runs WHERE id = $1`, []any{mustUUID(first)}, &postEvent)
	finance := func(want string) map[string]any {
		t.Helper()
		var r map[string]any
		waitFor(t, 30*time.Second, "finance status "+want, func() bool {
			if _, err := inst.App.Dispatcher.DispatchPending(t.Context()); err != nil {
				t.Logf("dispatch: %v", err)
			}
			r = hr.Must(200, "GET", hrBase+"/payroll-runs/"+first, nil).JSON()
			return r["financeStatus"] == want
		})
		return r
	}
	accDispatch(t)
	r := finance("posted")
	if len(r["financeJournals"].([]any)) != 1 {
		t.Fatalf("payroll journal: %v", r["financeJournals"])
	}
	// a later exception of the posting event (e.g. after a reversal and repost): Posting Failed
	failedJournal := uuid.New()
	accPublish(t, inst.MDR, "accounting.posting_exception", map[string]any{"exceptionId": uuid.New(), "eventId": postEvent,
		"eventType": "hris.payroll_posted", "reason": "missing_account", "message": "no account for role salary_expense", "journalId": failedJournal})
	r = finance("failed")
	if !strings.Contains(str(r["financeMessage"]), "missing_account") {
		t.Fatalf("posting failed: %v", r)
	}
	if q := hr.Must(200, "GET", hrBase+"/dashboard", nil).JSON(); !hasQueue(q, "payroll_posting_failed") {
		t.Fatalf("dashboard posting failed: %v", q["attention"])
	}
	// the journal booked together with the exception keeps the failure; a journal after the fix clears it
	accPublish(t, inst.MDR, "accounting.journal_posted", map[string]any{"journalId": failedJournal, "number": "JV-PART-" + tm.sfx,
		"sourceType": "hris.payroll_posted", "sourceId": first})
	if r = finance("failed"); !strings.Contains(str(r["financeMessage"]), "missing_account") {
		t.Fatalf("journal booked with the exception cleared the failure: %v", r)
	}
	accPublish(t, inst.MDR, "accounting.journal_posted", map[string]any{"journalId": uuid.New(), "number": "JV-FIX-" + tm.sfx,
		"sourceType": "hris.payroll_posted", "sourceId": first})
	r = finance("posted")
	if r["financeMessage"] != nil || len(r["financeJournals"].([]any)) != 3 {
		t.Fatalf("posted to finance: %v", r)
	}
}

// TestP5EmployeeLifecycleStatuses: a draft employee is hired on activation;
// a suspension shows as the work status and is withdrawn by reinstatement.
func TestP5EmployeeLifecycleStatuses(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	hr.Must(422, "POST", hrBase+"/employees", map[string]any{"fullName": "Bad " + sfx, "positionId": hrID(t, "positions", "WAITER"),
		"status": "inactive"}, "Idempotency-Key", newKey())
	d := hrNewEmployee(t, hr, "Dara Draft "+sfx, "WAITER", map[string]any{"status": "draft", "joinDate": hrDay(10)})
	did := str(d["id"])
	if d["status"] != "draft" {
		t.Fatalf("draft: %v", d)
	}
	var hires int
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.employment_history WHERE employee_id = $1 AND kind = 'hire'`, []any{mustUUID(did)}, &hires)
	if hires != 0 {
		t.Fatal("a draft is not hired")
	}
	ws := func(eid string) string {
		for _, w := range hr.Must(200, "GET", hrBase+"/employee-work-status", nil).Items() {
			if w["employeeId"] == eid {
				return str(w["workStatus"])
			}
		}
		return "active"
	}
	if ws(did) != "draft" {
		t.Fatal("work status draft")
	}
	if p := hr.Must(200, "GET", hrBase+"/employees/"+did+"/profile", nil).JSON(); p["workStatus"] != "draft" {
		t.Fatalf("profile work status: %v", p["workStatus"])
	}
	act := hr.Must(200, "POST", hrBase+"/employees/"+did+":activate", map[string]any{"joinDate": hrDay(0)}).JSON()
	if act["employee"].(map[string]any)["status"] != "active" || act["workStatus"] != "active" {
		t.Fatalf("activated: %v", act["employee"])
	}
	hr.Must(409, "POST", hrBase+"/employees/"+did+":activate", map[string]any{})
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.employment_history WHERE employee_id = $1 AND kind = 'hire'`, []any{mustUUID(did)}, &hires)
	if hires != 1 {
		t.Fatalf("hire history: %d", hires)
	}

	hr.Must(422, "POST", hrBase+"/employees/"+did+":suspend", map[string]any{"from": hrDay(0)})
	hr.Must(422, "POST", hrBase+"/employees/"+did+":suspend", map[string]any{"from": hrDay(0), "until": hrDay(-1), "reason": "x"})
	s := hr.Must(200, "POST", hrBase+"/employees/"+did+":suspend", map[string]any{"from": hrDay(0), "until": hrDay(6),
		"reason": "Investigation of a cash difference"}).JSON()
	if s["workStatus"] != "suspended" || ws(did) != "suspended" {
		t.Fatalf("suspended: %v", s["workStatus"])
	}
	if q := hr.Must(200, "GET", hrBase+"/dashboard", nil).JSON(); !hasQueue(q, "employees_suspended") {
		t.Fatalf("dashboard suspended: %v", q["attention"])
	}
	if l := hr.Must(200, "GET", hrBase+"/employee-work-status?workStatus=suspended", nil).Items(); len(l) == 0 {
		t.Fatal("work status filter")
	}
	r := hr.Must(200, "POST", hrBase+"/employees/"+did+":reinstate", map[string]any{"reason": "Cleared"}).JSON()
	if r["workStatus"] != "active" {
		t.Fatalf("reinstated: %v", r["workStatus"])
	}
	hr.Must(409, "POST", hrBase+"/employees/"+did+":reinstate", map[string]any{})
	var changes int
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.employment_history WHERE employee_id = $1 AND kind = 'status_change'`, []any{mustUUID(did)}, &changes)
	if changes != 2 {
		t.Fatalf("status history: %d", changes)
	}
}

// TestP5WorkforcePlans: a department plan with requirements per position
// and shift, the gap against the scheduled and available staff, adjustment,
// prefill from the staffing requirements, export, activation and archive.
func TestP5WorkforcePlans(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	today, tomorrow := hrDay(0), hrDay(1)
	shift := ttTemplate(t, hr, "WP"+tm.sfx, "08:00", "17:00", "")
	ttPublishedDays(t, hr, tm, shift, today, today, tm.aID)

	hr.Must(422, "POST", hrBase+"/workforce-plans", map[string]any{"orgUnitId": tm.unitID, "periodStart": today, "periodEnd": hrDay(120)},
		"Idempotency-Key", newKey())
	plan := hr.Must(201, "POST", hrBase+"/workforce-plans", map[string]any{"orgUnitId": tm.unitID, "periodStart": today, "periodEnd": tomorrow,
		"scenario": "tournament", "name": "Club Championship " + tm.sfx, "lines": []map[string]any{
			{"positionId": tm.posID, "shiftTemplateId": shift, "required": 3},
			{"positionId": tm.posID, "workDate": tomorrow, "required": 1, "notes": "Prize giving"}}}, "Idempotency-Key", newKey()).JSON()
	pid := str(plan["id"])
	if plan["status"] != "draft" || len(plan["lines"].([]any)) != 2 || !strings.HasPrefix(str(plan["number"]), "WFP-") {
		t.Fatalf("plan: %v", plan)
	}
	gap := hr.Must(200, "GET", hrBase+"/workforce-plans/"+pid+"/gap", nil).JSON()
	var first map[string]any
	for _, x := range gap["rows"].([]any) {
		if r := x.(map[string]any); strings.HasPrefix(str(r["date"]), today) && r["lineNo"] == float64(1) {
			first = r
		}
	}
	// three rows: line 1 today and tomorrow, line 2 tomorrow
	if len(gap["rows"].([]any)) != 3 || first == nil || first["required"] != float64(3) || first["scheduled"] != float64(1) || first["gap"] != float64(2) ||
		first["available"].(float64) < 3 {
		t.Fatalf("gap: %v", gap)
	}
	if gap["required"] != float64(7) {
		t.Fatalf("gap totals: %v", gap["required"])
	}
	adj := hr.Must(200, "PATCH", hrBase+"/workforce-plans/"+pid, map[string]any{"lines": []map[string]any{
		{"positionId": tm.posID, "shiftTemplateId": shift, "required": 2}}}).JSON()
	if len(adj["lines"].([]any)) != 1 {
		t.Fatalf("adjusted: %v", adj)
	}
	hr.Must(422, "PATCH", hrBase+"/workforce-plans/"+pid, map[string]any{"lines": []map[string]any{{"workDate": hrDay(5), "required": 1}}})
	if g := hr.Must(200, "GET", hrBase+"/workforce-plans/"+pid+"/gap", nil).JSON(); g["gap"] != float64(3) {
		t.Fatalf("adjusted gap: %v", g["gap"])
	}
	csv := hr.Must(200, "GET", hrBase+"/workforce-plans/"+pid+"/export", nil).String()
	if !strings.Contains(csv, "Plan,Department,Scenario,Date") || strings.Count(csv, "\n") != 3 {
		t.Fatalf("export: %q", csv)
	}
	if a := hr.Must(200, "POST", hrBase+"/workforce-plans/"+pid+":activate", map[string]any{}).JSON(); a["status"] != "active" {
		t.Fatalf("activated: %v", a)
	}
	hr.Must(409, "POST", hrBase+"/workforce-plans/"+pid+":activate", map[string]any{})
	if l := hr.Must(200, "GET", hrBase+"/workforce-plans?orgUnitId="+tm.unitID+"&date="+today+"&status=active", nil).Items(); len(l) != 1 {
		t.Fatalf("plans of the day: %v", l)
	}

	// prefilled from the staffing requirements (weekdays only → one dated line per matching day)
	hr.Must(201, "POST", hrBase+"/staffing-requirements", map[string]any{"orgUnitId": tm.unitID, "positionId": tm.posID, "minStaff": 2},
		"Idempotency-Key", newKey())
	pre := hr.Must(201, "POST", hrBase+"/workforce-plans", map[string]any{"orgUnitId": tm.unitID, "periodStart": today, "periodEnd": hrDay(6),
		"fromRequirements": true}, "Idempotency-Key", newKey()).JSON()
	if lines := pre["lines"].([]any); len(lines) != 1 || lines[0].(map[string]any)["required"] != float64(2) || pre["scenario"] != "normal" {
		t.Fatalf("prefilled: %v", pre)
	}
	if a := hr.Must(200, "POST", hrBase+"/workforce-plans/"+str(pre["id"])+":archive", map[string]any{}).JSON(); a["status"] != "archived" {
		t.Fatalf("archived: %v", a)
	}
	hr.Must(409, "PATCH", hrBase+"/workforce-plans/"+str(pre["id"]), map[string]any{"name": "x"})
	tm.a.Must(403, "GET", hrBase+"/workforce-plans", nil)
}
