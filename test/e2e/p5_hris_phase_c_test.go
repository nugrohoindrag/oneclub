package e2e

// HRIS improvement phase C (docs/HRIS_Product_Requirements_UI_Backend_Audit.md
// §39.5): comments and attachments on approvals, reimbursement claims,
// benefit plans with payroll deductions, timesheets and open shifts.

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"oneclub/internal/hris/payroll"
)

// TestP5ReimbursementClaims: an employee claims an expense with the receipt
// in ESS; Finance comments on the approval with an attachment, approves it;
// HR sends it to Finance, which pays it (journal Dr 6113 / Cr bank). HR
// claims for an employee and cancels after approval; an ESS claim is
// withdrawn.
func TestP5ReimbursementClaims(t *testing.T) {
	tm := prSetup(t)
	sa := tm.sa
	emp := prEmployee(t, tm, "Rina Reimburse", "TK/0", prMonth(14, nil), "6000000", nil, "BCA", true, nil)
	hr := prUser(t, "hr_manager")
	fin := prUser(t, "finance_manager")
	cat := idOf(hr.Must(201, "POST", hrBase+"/reimbursement-categories", map[string]any{"code": "TRV" + tm.sfx, "name": "Travel " + tm.sfx,
		"maxAmount": "500000", "receiptRequired": true}, "Idempotency-Key", newKey()))
	wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": payroll.ReimbursementDocumentType.Code,
		"name": "Reimbursement MDR " + tm.sfx, "propertyId": inst.MDR, "steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager",
			"approverType": "role", "approverRoleId": roleID(t, sa, "finance_manager")}}}).JSON()
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
	})

	prRole(t, "lifeguard")
	ess := ttLink(t, "lifeguard", emp)
	up, ctype := multipartBody(t, nil, "file", "taxi.pdf", "%PDF-1.4\n% taxi receipt\n")
	r := ess.Do("POST", "/api/v1/ess/reimbursement-files", up, "Content-Type", ctype)
	if r.Status != 201 {
		t.Fatalf("upload receipt: %s", r)
	}
	file := str(r.JSON()["id"])
	claim := func(body map[string]any) map[string]any {
		body["categoryId"] = cat
		return body
	}
	ess.Must(422, "POST", "/api/v1/ess/reimbursements", claim(map[string]any{"expenseDate": hrDay(0), "amount": "120000", "description": "Taxi"}),
		"Idempotency-Key", newKey()) // receipt required
	ess.Must(422, "POST", "/api/v1/ess/reimbursements", claim(map[string]any{"expenseDate": hrDay(0), "amount": "900000", "description": "Taxi",
		"fileId": file}), "Idempotency-Key", newKey()) // over the ceiling
	c := ess.Must(201, "POST", "/api/v1/ess/reimbursements", claim(map[string]any{"expenseDate": hrDay(-1), "amount": "120000",
		"description": "Taxi to the supplier", "fileId": file}), "Idempotency-Key", newKey()).JSON()
	if c["status"] != "submitted" || c["requestSource"] != "ess" || !strings.HasPrefix(str(c["number"]), "RMB-") || c["approvalRequestId"] == nil {
		t.Fatalf("claim: %v", c)
	}
	cid, aid := str(c["id"]), str(c["approvalRequestId"])

	// §27 comments with an attachment on the approval request
	note, nctype := multipartBody(t, map[string]string{"body": "Please attach the trip approval"}, "file", "policy.pdf", "%PDF-1.4\n% travel policy\n")
	if r := fin.Do("POST", "/api/v1/platform/approvals/"+aid+"/comments", note, "Content-Type", nctype); r.Status != 201 || r.JSON()["fileId"] == nil {
		t.Fatalf("comment with attachment: %s", r)
	}
	ess.Must(422, "POST", "/api/v1/platform/approvals/"+aid+"/comments", map[string]any{"body": " "})
	ess.Must(201, "POST", "/api/v1/platform/approvals/"+aid+"/comments", map[string]any{"body": "Approved by my manager by phone"})
	detail := ess.Must(200, "GET", "/api/v1/platform/approvals/"+aid, nil).JSON()
	cs := detail["comments"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["fileName"] != "policy.pdf" {
		t.Fatalf("comments: %v", cs)
	}
	first := str(cs[0].(map[string]any)["id"])
	if f := ess.Do("GET", "/api/v1/platform/approvals/"+aid+"/comments/"+first+"/file", nil); f.Status != 200 || !strings.Contains(f.String(), "travel policy") {
		t.Fatalf("attachment download: %s", f)
	}
	roleUser(t, inst, "outlet_manager").Must(404, "GET", "/api/v1/platform/approvals/"+aid+"/comments/"+first+"/file", nil)
	if rc := ess.Do("GET", "/api/v1/ess/reimbursements/"+cid+"/receipt", nil); rc.Status != 200 || !strings.Contains(rc.String(), "taxi receipt") {
		t.Fatalf("my receipt: %s", rc)
	}
	if rc := hr.Do("GET", hrBase+"/reimbursements/"+cid+"/receipt", nil); rc.Status != 200 {
		t.Fatalf("receipt: %s", rc)
	}

	fin.Must(200, "POST", "/api/v1/platform/approvals/"+aid+":approve", map[string]any{})
	fin.Must(409, "POST", hrBase+"/reimbursements/"+cid+":mark-paid", map[string]any{"paidOn": hrDay(0), "method": "bank_transfer"})
	if s := hr.Must(200, "POST", hrBase+"/reimbursements/"+cid+":send-to-finance", map[string]any{}).JSON(); s["status"] != "sent_to_finance" {
		t.Fatalf("sent: %v", s)
	}
	hr.Must(403, "POST", hrBase+"/reimbursements/"+cid+":mark-paid", map[string]any{"paidOn": hrDay(0), "method": "bank_transfer"})
	paid := fin.Must(200, "POST", hrBase+"/reimbursements/"+cid+":mark-paid", map[string]any{"paidOn": hrDay(0), "method": "bank_transfer",
		"reference": "TRF-R" + tm.sfx}).JSON()
	if paid["status"] != "paid" {
		t.Fatalf("paid: %v", paid)
	}
	accDispatch(t)
	ev := prEvent(t, payroll.EventReimbursementPaid, cid)
	if s, n := accProcessed(t, ev); s != "posted" || n != 1 {
		t.Fatalf("reimbursement journal: %s %d", s, n)
	}
	j := accLines(accJournalOf(t, fin, ev))
	prEq(t, "employee reimbursements 6113", j["6113"], di(120_000))
	prEq(t, "bank 1121", j["1121"], di(-120_000))

	// HR claims for the employee (receipt uploaded by HR), cancelled after approval
	up2, ctype2 := multipartBody(t, nil, "file", "parking.png", "\x89PNG\r\n\x1a\n parking")
	hf := hr.Do("POST", hrBase+"/reimbursement-files", up2, "Content-Type", ctype2)
	if hf.Status != 201 {
		t.Fatalf("hr upload: %s", hf)
	}
	h := hr.Must(201, "POST", hrBase+"/reimbursements", claim(map[string]any{"employeeId": emp, "expenseDate": hrDay(0), "amount": "50000",
		"description": "Parking", "fileId": str(hf.JSON()["id"])}), "Idempotency-Key", newKey()).JSON()
	fin.Must(200, "POST", "/api/v1/platform/approvals/"+str(h["approvalRequestId"])+":approve", map[string]any{})
	if x := hr.Must(200, "POST", hrBase+"/reimbursements/"+str(h["id"])+":cancel", map[string]any{"note": "Paid by the club card"}).JSON(); x["status"] != "cancelled" {
		t.Fatalf("hr cancel: %v", x)
	}
	// ESS withdraws a waiting claim
	w := ess.Must(201, "POST", "/api/v1/ess/reimbursements", claim(map[string]any{"expenseDate": hrDay(0), "amount": "30000", "description": "Toll",
		"fileId": file}), "Idempotency-Key", newKey()).JSON()
	if x := ess.Must(200, "POST", "/api/v1/ess/reimbursements/"+str(w["id"])+":cancel", map[string]any{"note": "Duplicate"}).JSON(); x["status"] != "cancelled" {
		t.Fatalf("ess cancel: %v", x)
	}
	if l := ess.Must(200, "GET", "/api/v1/ess/reimbursements", nil).Items(); len(l) != 3 {
		t.Fatalf("my claims: %d", len(l))
	}
	if l := hr.Must(200, "GET", hrBase+"/reimbursements?status=paid&employeeId="+emp, nil).Items(); len(l) != 1 {
		t.Fatalf("claims list: %v", l)
	}
}

// TestP5BenefitEnrollments: a plan with eligibility; the eligible employee is
// enrolled, the employee contribution is deducted by the regular payroll
// run (BENEFIT_EE) and booked to Benefit Contributions Payable; the
// enrollment ends.
func TestP5BenefitEnrollments(t *testing.T) {
	tm := prSetup(t)
	p := prMonth(6, notDecember) // after the cut-over of the MDR book; months 2–5, 7 and 9 are used by other tests
	period := p.Format("2006-01")
	emp := prEmployee(t, tm, "Bayu Benefit", "TK/0", prMonth(20, nil), "7000000", nil, "BCA", true, nil)
	pkwt := prEmployee(t, tm, "Kiki Kontrak", "TK/0", prMonth(20, nil), "5000000", nil, "BCA", true,
		map[string]any{"contractType": "pkwt", "endDate": ymdOf(prMonth(-6, nil))})
	hr := prUser(t, "hr_manager")
	fin := prUser(t, "finance_manager")
	plan := idOf(hr.Must(201, "POST", hrBase+"/benefit-plans", map[string]any{"code": "HLT" + tm.sfx, "name": "Health " + tm.sfx,
		"benefitType": "health_insurance", "provider": "Asuransi Sehat", "employerContribution": "300000", "employeeContribution": "150000",
		"eligibleStatuses": []string{"permanent"}, "minServiceMonths": 3}, "Idempotency-Key", newKey()))
	el := map[string]map[string]any{}
	for _, x := range hr.Must(200, "GET", hrBase+"/benefit-plans/"+plan+"/eligibility", nil).Items() {
		el[str(x["employeeId"])] = x
	}
	if el[emp]["eligible"] != true || el[pkwt]["eligible"] != false {
		t.Fatalf("eligibility: %v / %v", el[emp], el[pkwt])
	}
	from := ymdOf(prMonth(15, nil))
	hr.Must(422, "POST", hrBase+"/benefit-enrollments", map[string]any{"employeeId": pkwt, "planId": plan, "effectiveFrom": from}, "Idempotency-Key", newKey())
	en := hr.Must(201, "POST", hrBase+"/benefit-enrollments", map[string]any{"employeeId": emp, "planId": plan, "effectiveFrom": from},
		"Idempotency-Key", newKey()).JSON()
	if en["status"] != "active" || str(en["employeeContribution"]) != "150000" {
		t.Fatalf("enrollment: %v", en)
	}
	hr.Must(409, "POST", hrBase+"/benefit-enrollments", map[string]any{"employeeId": emp, "planId": plan, "effectiveFrom": from}, "Idempotency-Key", newKey())

	prRole(t, "golf_staff")
	ess := ttLink(t, "golf_staff", emp)
	if l := ess.Must(200, "GET", "/api/v1/ess/benefits", nil).Items(); len(l) != 1 || l[0]["planId"] != plan {
		t.Fatalf("my benefits: %v", l)
	}

	run := hr.Must(201, "POST", hrBase+"/payroll-runs", map[string]any{"runType": "regular", "periodCode": period, "orgUnitId": tm.unitID},
		"Idempotency-Key", newKey()).JSON()
	rid := str(run["id"])
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{})
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":calculate", map[string]any{}) // recalculation reuses the deduction
	prEq(t, "benefit deduction", prLine(prSlip(t, hr, rid, emp), payroll.BenefitComponent), di(150_000))
	prEq(t, "no deduction for the contract employee", prLine(prSlip(t, hr, rid, pkwt), payroll.BenefitComponent), di(0))
	hr.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":approve", map[string]any{})
	fin.Must(200, "POST", hrBase+"/payroll-runs/"+rid+":post", map[string]any{})
	got := hr.Must(200, "GET", hrBase+"/benefit-enrollments?employeeId="+emp, nil).Items()
	if len(got) != 1 || str(got[0]["deducted"]) != "150000" {
		t.Fatalf("deducted: %v", got)
	}
	accDispatch(t)
	pev := prEvent(t, "hris.payroll_posted", rid)
	if s, n := accProcessed(t, pev); s != "posted" {
		var reason, msg string
		sysQueryRow(t, inst, `SELECT coalesce(string_agg(reason, ','), ''), coalesce(string_agg(message, ' | '), '') FROM accounting.posting_exceptions WHERE event_id = $1`,
			[]any{pev}, &reason, &msg)
		t.Fatalf("payroll journal: %s %d %s %s", s, n, reason, msg)
	}
	j := accLines(accJournalOf(t, fin, pev))
	prEq(t, "benefit contributions payable 2177", j["2177"], di(-150_000))

	hr.Must(422, "POST", hrBase+"/benefit-enrollments/"+str(en["id"])+":end", map[string]any{"effectiveTo": hrDay(0)})
	end := hr.Must(200, "POST", hrBase+"/benefit-enrollments/"+str(en["id"])+":end", map[string]any{"effectiveTo": hrDay(0),
		"reason": "Moved to the family plan"}).JSON()
	if end["status"] != "ended" {
		t.Fatalf("ended: %v", end)
	}
}

// TestP5Timesheets: the employee records time per activity and submits;
// the supervisor approves in ESS; a rejected timesheet is corrected and
// resubmitted, a submitted one withdrawn; HR approves and rejects too.
func TestP5Timesheets(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	week := func(from int) map[string]any {
		return map[string]any{"periodStart": hrDay(from), "periodEnd": hrDay(from + 6), "entries": []map[string]any{
			{"workDate": hrDay(from), "hours": "6", "activity": "Banquet set-up", "reference": "BEO-1"},
			{"workDate": hrDay(from), "hours": "2", "activity": "Inventory count"},
			{"workDate": hrDay(from + 1), "hours": "8", "activity": "Banquet service"}}}
	}
	bad := week(-20)
	bad["entries"] = []map[string]any{{"workDate": hrDay(-20), "hours": "25", "activity": "x"}}
	tm.a.Must(422, "POST", "/api/v1/ess/timesheets", bad, "Idempotency-Key", newKey())
	b1 := week(-20)
	b1["submit"] = true
	ts := tm.a.Must(201, "POST", "/api/v1/ess/timesheets", b1, "Idempotency-Key", newKey()).JSON()
	if ts["status"] != "submitted" || str(ts["totalHours"]) != "16" || len(ts["days"].([]any)) != 7 || len(ts["steps"].([]any)) != 1 {
		t.Fatalf("timesheet: %v", ts)
	}
	tm.a.Must(409, "POST", "/api/v1/ess/timesheets", week(-18), "Idempotency-Key", newKey()) // overlaps
	var queued bool
	for _, x := range tm.mgr.Must(200, "GET", "/api/v1/ess/approvals", nil).Items() {
		queued = queued || (x["kind"] == "timesheet" && x["requestId"] == ts["id"])
	}
	if !queued {
		t.Fatal("timesheet in the supervisor's approvals")
	}
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/timesheet/"+str(ts["id"])+":approve", map[string]any{})
	if x := tm.a.Must(200, "GET", "/api/v1/ess/timesheets/"+str(ts["id"]), nil).JSON(); x["status"] != "approved" {
		t.Fatalf("approved: %v", x["status"])
	}

	// rejected → corrected → resubmitted → withdrawn → resubmitted → HR decides
	b2 := week(-10)
	b2["submit"] = true
	t2 := str(tm.a.Must(201, "POST", "/api/v1/ess/timesheets", b2, "Idempotency-Key", newKey()).JSON()["id"])
	tm.mgr.Must(200, "POST", "/api/v1/ess/approvals/timesheet/"+t2+":reject", map[string]any{"note": "Split the banquet hours by event"})
	fix := week(-10)
	fix["submit"] = true
	if x := tm.a.Must(200, "PATCH", "/api/v1/ess/timesheets/"+t2, fix).JSON(); x["status"] != "submitted" {
		t.Fatalf("corrected: %v", x["status"])
	}
	if x := tm.a.Must(200, "POST", "/api/v1/ess/timesheets/"+t2+":withdraw", map[string]any{"note": "Forgot one day"}).JSON(); x["status"] != "draft" {
		t.Fatalf("withdrawn: %v", x["status"])
	}
	tm.a.Must(200, "POST", "/api/v1/ess/timesheets/"+t2+":submit", map[string]any{})
	hr.Must(422, "POST", hrBase+"/timesheets/"+t2+":reject", map[string]any{})
	if x := hr.Must(200, "POST", hrBase+"/timesheets/"+t2+":approve", map[string]any{"note": "OK"}).JSON(); x["status"] != "approved" {
		t.Fatalf("hr approved: %v", x["status"])
	}
	b3 := week(-3)
	b3["submit"] = true
	t3 := str(tm.b.Must(201, "POST", "/api/v1/ess/timesheets", b3, "Idempotency-Key", newKey()).JSON()["id"])
	if x := hr.Must(200, "POST", hrBase+"/timesheets/"+t3+":reject", map[string]any{"note": "Wrong week"}).JSON(); x["status"] != "rejected" {
		t.Fatalf("hr rejected: %v", x["status"])
	}
	if l := hr.Must(200, "GET", hrBase+"/timesheets?employeeId="+tm.aID, nil).Items(); len(l) != 2 {
		t.Fatalf("timesheets of A: %d", len(l))
	}
	tm.b.Must(404, "GET", "/api/v1/ess/timesheets/"+t2, nil)
}

// TestP5OpenShifts: a scheduler posts open shifts on a published schedule;
// employees of the unit claim them; the approved claim becomes the
// assignment and fills the shift; a claim is rejected, one withdrawn and an
// open shift cancelled.
func TestP5OpenShifts(t *testing.T) {
	tm := ttSetup(t, [3]string{"department_head", "kitchen_staff", "pos_staff"})
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	shift := ttTemplate(t, hr, "OS"+tm.sfx, "09:00", "17:00", "")
	sid := ttPublishedDays(t, hr, tm, shift, hrDay(1), hrDay(5), tm.mgrID)
	post := func(day string, slots int) string {
		return idOf(hr.Must(201, "POST", hrBase+"/schedules/"+sid+"/open-shifts", map[string]any{"workDate": day, "shiftTemplateId": shift,
			"slots": slots, "notes": "Wedding banquet"}, "Idempotency-Key", newKey()))
	}
	hr.Must(422, "POST", hrBase+"/schedules/"+sid+"/open-shifts", map[string]any{"workDate": hrDay(9), "shiftTemplateId": shift, "slots": 1},
		"Idempotency-Key", newKey())
	o1 := post(hrDay(2), 1)
	var seen bool
	for _, x := range tm.b.Must(200, "GET", "/api/v1/ess/open-shifts", nil).Items() {
		seen = seen || x["id"] == o1
	}
	if !seen {
		t.Fatal("open shift in ESS")
	}
	tm.a.Must(200, "POST", "/api/v1/ess/open-shifts/"+o1+":claim", map[string]any{"note": "Happy to help"})
	tm.b.Must(200, "POST", "/api/v1/ess/open-shifts/"+o1+":claim", map[string]any{})
	tm.b.Must(409, "POST", "/api/v1/ess/open-shifts/"+o1+":claim", map[string]any{})
	claims := map[string]string{}
	for _, x := range hr.Must(200, "GET", hrBase+"/open-shifts?scheduleId="+sid, nil).Items() {
		if x["id"] == o1 {
			for _, c := range x["claims"].([]any) {
				m := c.(map[string]any)
				claims[str(m["employeeId"])] = str(m["id"])
			}
		}
	}
	got := hr.Must(200, "POST", hrBase+"/open-shift-claims/"+claims[tm.bID]+":approve", map[string]any{}).JSON()
	if got["status"] != "filled" || got["filled"] != float64(1) {
		t.Fatalf("filled: %v", got)
	}
	for _, c := range got["claims"].([]any) {
		m := c.(map[string]any)
		if m["employeeId"] == tm.aID && m["status"] != "rejected" {
			t.Fatalf("remaining claim declined: %v", m)
		}
	}
	if ttCount(t, `SELECT count(*) FROM hris.shift_assignments WHERE employee_id = $1 AND work_date = $2 AND status = 'scheduled' AND kind = 'shift'`,
		mustUUID(tm.bID), hrDay(2)) != 1 {
		t.Fatal("assignment of the approved claim")
	}

	o2 := post(hrDay(3), 2)
	tm.a.Must(200, "POST", "/api/v1/ess/open-shifts/"+o2+":claim", map[string]any{})
	tm.b.Must(200, "POST", "/api/v1/ess/open-shifts/"+o2+":claim", map[string]any{})
	var aClaim, bClaim string
	for _, x := range hr.Must(200, "GET", hrBase+"/open-shifts?status=open", nil).Items() {
		if x["id"] == o2 {
			for _, c := range x["claims"].([]any) {
				m := c.(map[string]any)
				if m["employeeId"] == tm.aID {
					aClaim = str(m["id"])
				} else {
					bClaim = str(m["id"])
				}
			}
		}
	}
	hr.Must(422, "POST", hrBase+"/open-shift-claims/"+aClaim+":reject", map[string]any{})
	hr.Must(200, "POST", hrBase+"/open-shift-claims/"+aClaim+":reject", map[string]any{"note": "Needed in the kitchen"})
	if w := tm.b.Must(200, "POST", "/api/v1/ess/open-shift-claims/"+bClaim+":withdraw", map[string]any{}).JSON(); *strPtr(w["myClaim"]) != "withdrawn" {
		t.Fatalf("withdrawn: %v", w["myClaim"])
	}
	if c := hr.Must(200, "POST", hrBase+"/open-shifts/"+o2+":cancel", map[string]any{"note": "Event moved"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", c)
	}
	tm.a.Must(404, "POST", "/api/v1/ess/open-shift-claims/"+uuid.NewString()+":withdraw", map[string]any{})
}

func strPtr(v any) *string {
	s := str(v)
	return &s
}
