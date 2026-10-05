package e2e

// PRD P5 Recruitment (EP-03) and Performance Review (EP-05): job
// requisitions with approval and headcount, candidates and the pipeline,
// interviews and scorecards, offers with approval and the offer letter, the
// hire through Core HR (employee, contract, login, onboarding checklist,
// hris.employee_hired), the public careers page with consent and CV,
// applicant retention and erasure (§16 #10), review templates and cycles,
// self assessment and manager review in ESS, calibration, results to the
// employment history, promotion through Core HR, the demo data and the
// recruitment / performance reports and KPIs (EP-27).

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

func init() {
	resourceCRUDSkip["hris.candidate"] = "contact, de-duplication and erasure rules: covered by TestP5HRTalentRecruitment"
}

// talentWorkflow adds an approval workflow (General Manager step under a
// condition) and deactivates it at the end of the test.
func talentWorkflow(t *testing.T, sa *Client, docType, attr string, min float64) {
	t.Helper()
	w := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": docType, "name": "Talent " + hrSuffix(),
		"steps": []map[string]any{{"stepNo": 1, "name": "General Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "general_manager"),
			"conditions": []map[string]any{{"attribute": attr, "operator": "gte", "value": min}}}}}).JSON()
	wid := str(w["id"])
	t.Cleanup(func() {
		sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+wid, map[string]any{"status": "inactive"})
	})
}

// talentDaily runs the daily recruitment & review job.
func talentDaily(t *testing.T) (erased, expired, reminders int) {
	t.Helper()
	rep, err := inst.App.HR.Talent.RunDaily(context.Background())
	if err != nil {
		t.Fatalf("talent daily: %v", err)
	}
	return rep.Erased, rep.OffersExpired, rep.ReviewReminders
}

// talentCriteria scores every criterion of the default scorecard.
func talentCriteria(score string) []map[string]any {
	var out []map[string]any
	for _, c := range hris.NewRecruitmentConfiguration().InterviewCriteria {
		out = append(out, map[string]any{"code": c.Code, "score": score})
	}
	return out
}

func talentCandidate(t *testing.T, c *Client, name, email string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"fullName": name, "email": email, "phone": "0812" + hrSuffix() + "1", "source": "referral"}
	for k, v := range extra {
		body[k] = v
	}
	return idOf(c.Must(201, "POST", hrBase+"/candidates", body, "Idempotency-Key", newKey()))
}

func talentApply(t *testing.T, c *Client, req, cand string) string {
	t.Helper()
	return idOf(c.Must(201, "POST", hrBase+"/applications", map[string]any{"requisitionId": req, "candidateId": cand}, "Idempotency-Key", newKey()))
}

func talentApp(t *testing.T, c *Client, id string) map[string]any {
	t.Helper()
	return c.Must(200, "GET", hrBase+"/applications/"+id, nil).JSON()["application"].(map[string]any)
}

// EP-03 (FR-RCT-01–06): requisition with approval, careers page, pipeline,
// interviews, offer with approval and letter, hire through Core HR, offer
// decline / cancel / expiry, retention and erasure, reports.
func TestP5HRTalentRecruitment(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	head := login(t, inst, "dept.head@demo.oneclub.id", demoPassword)
	sa := superAdmin(t, inst)
	pub := anon(t, inst)
	sfx := hrSuffix()
	loc := clubLoc(inst)
	waiter := hrID(t, "positions", "WAITER")
	hendro := hrEmployeeID(t, "EMP-00005")

	// Recruitment and Performance Review Configuration are versioned HR policies (H6).
	defs := map[string]string{}
	for _, d := range rules.PolicyDefs() {
		defs[d.Code] = d.Category
	}
	if defs[hris.RecruitmentConfigurationCode] != "HR Configuration" || defs[hris.PerformanceConfigurationCode] != "HR Configuration" {
		t.Fatalf("talent policies: %v", defs)
	}
	roleUser(t, inst, "cashier").Must(403, "GET", hrBase+"/job-requisitions", nil)

	// FR-RCT-01: the F&B Manager requests staff; the budget is masked for him.
	talentWorkflow(t, sa, "hris.job_requisition", "salaryMax", 6500000)
	talentWorkflow(t, sa, "hris.job_offer", "baseSalary", 6000000)
	r1 := head.Must(201, "POST", hrBase+"/job-requisitions", map[string]any{"positionId": waiter, "headcount": 1, "contractType": "pkwt",
		"salaryMin": "5000000", "salaryMax": "7000000", "isPublic": true, "location": "Terrace " + sfx, "description": "Terrace service " + sfx,
		"requirements": "SMK hospitality", "targetStartDate": hrDay(20)}, "Idempotency-Key", newKey()).JSON()
	r1id := str(r1["id"])
	if r1["status"] != "draft" || r1["title"] != "Waiter" || r1["salaryMax"] != "[REDACTED]" || r1["hiringManagerId"] != hendro ||
		!strings.HasPrefix(str(r1["number"]), "REQ-") {
		t.Fatalf("requisition: %v", r1)
	}
	if g := gm.Must(200, "GET", hrBase+"/job-requisitions/"+r1id, nil).JSON(); g["salaryMax"] != "7000000" {
		t.Fatalf("GM sees the budget: %v", g["salaryMax"])
	}
	head.Must(422, "POST", hrBase+"/job-requisitions", map[string]any{"positionId": waiter, "headcount": 0})
	head.Must(422, "POST", hrBase+"/job-requisitions", map[string]any{"positionId": waiter, "headcount": 1, "reason": "replacement"})
	r1 = head.Must(200, "POST", hrBase+"/job-requisitions/"+r1id+":submit", map[string]any{}).JSON()
	if r1["status"] != "submitted" || r1["approvalRequestId"] == nil {
		t.Fatalf("submitted: %v", r1)
	}
	head.Must(403, "POST", hrBase+"/job-requisitions/"+r1id+":approve", map[string]any{})
	hr.Must(403, "POST", hrBase+"/job-requisitions/"+r1id+":approve", map[string]any{}) // not the approver of the step
	r1 = gm.Must(200, "POST", hrBase+"/job-requisitions/"+r1id+":approve", map[string]any{"reason": "Season staffing"}).JSON()
	if r1["status"] != "open" || r1["approvedAt"] == nil {
		t.Fatalf("approved: %v", r1)
	}
	head.Must(409, "PATCH", hrBase+"/job-requisitions/"+r1id, map[string]any{"headcount": 3})
	head.Must(200, "PATCH", hrBase+"/job-requisitions/"+r1id, map[string]any{"description": "Terrace and banquet service " + sfx})
	// A rejected requisition is edited and then cancelled.
	r2 := idOf(head.Must(201, "POST", hrBase+"/job-requisitions", map[string]any{"title": "Banquet Waiter " + sfx, "positionId": waiter, "headcount": 4,
		"salaryMax": "9000000"}, "Idempotency-Key", newKey()))
	head.Must(200, "POST", hrBase+"/job-requisitions/"+r2+":submit", map[string]any{})
	gm.Must(422, "POST", hrBase+"/job-requisitions/"+r2+":reject", map[string]any{})
	if r := gm.Must(200, "POST", hrBase+"/job-requisitions/"+r2+":reject", map[string]any{"reason": "Use casual staff"}).JSON(); r["status"] != "rejected" ||
		r["decisionNote"] != "Use casual staff" {
		t.Fatalf("rejected: %v", r)
	}
	head.Must(200, "PATCH", hrBase+"/job-requisitions/"+r2, map[string]any{"headcount": 2})
	head.Must(403, "POST", hrBase+"/job-requisitions/"+r2+":close", map[string]any{"reason": "x"}) // closing is HR's
	hr.Must(422, "POST", hrBase+"/job-requisitions/"+r2+":close", map[string]any{})
	if r := hr.Must(200, "POST", hrBase+"/job-requisitions/"+r2+":close", map[string]any{"reason": "Not needed"}).JSON(); r["status"] != "cancelled" {
		t.Fatalf("cancelled: %v", r)
	}

	// FR-RCT-05: the careers page lists the open public requisition; on hold it disappears.
	careers := func() []map[string]any {
		return pub.Must(200, "GET", "/api/v1/public/careers?propertyId="+inst.Main.String(), nil).Items()
	}
	listed := func() bool {
		for _, p := range careers() {
			if p["id"] == r1id {
				return true
			}
		}
		return false
	}
	if !listed() {
		t.Fatal("open public requisition not on the careers page")
	}
	hr.Must(200, "POST", hrBase+"/job-requisitions/"+r1id+":hold", map[string]any{"reason": "Budget review"})
	if listed() {
		t.Fatal("a requisition on hold is still on the careers page")
	}
	hr.Must(200, "POST", hrBase+"/job-requisitions/"+r1id+":reopen", map[string]any{})
	pos := pub.Must(200, "GET", "/api/v1/public/careers/"+r1id+"?propertyId="+inst.Main.String(), nil).JSON()
	if pos["title"] != "Waiter" || pos["department"] != "Food & Beverage" || pos["salaryMax"] != nil {
		t.Fatalf("public position: %v", pos)
	}
	cv := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"))
	webEmail := "rani" + sfx + "@mail.test"
	apply := map[string]any{"propertyId": inst.Main, "requisitionId": r1id, "fullName": "Rani Website " + sfx, "email": webEmail, "phone": "08129" + sfx,
		"education": "d3", "experienceYears": "2", "coverLetter": "I love service.", "cv": map[string]any{"filename": "rani.pdf", "contentBase64": cv},
		"talentPoolConsent": true}
	pub.Must(422, "POST", "/api/v1/public/careers/applications", apply) // no consent
	apply["consent"] = true
	got := pub.Must(202, "POST", "/api/v1/public/careers/applications", apply).JSON()
	if got["status"] != "received" || !strings.HasPrefix(str(got["number"]), "APP-") {
		t.Fatalf("website application: %v", got)
	}
	pub.Must(409, "POST", "/api/v1/public/careers/applications", apply)
	bot := map[string]any{"propertyId": inst.Main, "requisitionId": r1id, "fullName": "Bot", "email": "bot" + sfx + "@spam.test", "phone": "0811111111",
		"consent": true, "website": "http://spam"}
	pub.Must(202, "POST", "/api/v1/public/careers/applications", bot)
	var bots int
	sysQueryRow(t, inst, `SELECT count(*) FROM hris.candidates WHERE email = $1`, []any{"bot" + sfx + "@spam.test"}, &bots)
	if bots != 0 {
		t.Fatal("honeypot submission created a candidate")
	}

	// FR-RCT-02: candidate master with masking; CV upload private to HR.
	c2 := talentCandidate(t, hr, "Bima Candidate "+sfx, "bima"+sfx+"@mail.test", map[string]any{"expectedSalary": "6000000", "city": "Tangerang",
		"consentAt": time.Now().UTC().Format(time.RFC3339)})
	hr.Must(409, "POST", hrBase+"/candidates", map[string]any{"fullName": "Bima Again", "email": "BIMA" + sfx + "@mail.test"})
	hr.Must(422, "POST", hrBase+"/candidates", map[string]any{"fullName": "No Contact"})
	if c := head.Must(200, "GET", hrBase+"/candidates/"+c2, nil).JSON(); c["expectedSalary"] != "[REDACTED]" {
		t.Fatalf("expected salary masked: %v", c["expectedSalary"])
	}
	body, ctype := multipartBody(t, nil, "file", "bima.pdf", "%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
	fid := idOf(hr.Must(201, "POST", hrBase+"/recruitment-files", body, "Content-Type", ctype))
	bad, btype := multipartBody(t, nil, "file", "x.sh", "#!/bin/sh\necho hi\n")
	hr.Must(422, "POST", hrBase+"/recruitment-files", bad, "Content-Type", btype)
	hr.Must(200, "PATCH", hrBase+"/candidates/"+c2, map[string]any{"cvFileId": fid})
	if r := hr.Must(200, "GET", hrBase+"/candidates/"+c2+"/cv", nil); !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatalf("cv: %s", r.Body)
	}
	head.Must(403, "GET", hrBase+"/candidates/"+c2+"/cv", nil)
	a2 := talentApply(t, hr, r1id, c2)
	hr.Must(409, "POST", hrBase+"/applications", map[string]any{"requisitionId": r1id, "candidateId": c2})
	hr.Must(409, "POST", hrBase+"/applications", map[string]any{"requisitionId": r2, "candidateId": c2}) // requisition cancelled
	apps := hr.Must(200, "GET", hrBase+"/applications?requisitionId="+r1id, nil).Items()
	var web string
	for _, a := range apps {
		if a["source"] == "website" {
			web = str(a["id"])
		}
	}
	if len(apps) != 2 || web == "" {
		t.Fatalf("pipeline of the requisition: %v", apps)
	}
	// The website applicant is screened and rejected: kept in the talent pool for the HR Configuration retention.
	hr.Must(200, "POST", hrBase+"/applications/"+web+":move-stage", map[string]any{"stage": "screening", "screeningScore": "2"})
	hr.Must(422, "POST", hrBase+"/applications/"+web+":reject", map[string]any{})
	if a := hr.Must(200, "POST", hrBase+"/applications/"+web+":reject", map[string]any{"reason": "No F&B experience"}).JSON(); a["stage"] != "rejected" ||
		a["closedStage"] != "screening" {
		t.Fatalf("rejected: %v", a)
	}
	webCand := hr.Must(200, "GET", hrBase+"/candidates/"+str(talentApp(t, hr, web)["candidateId"]), nil).JSON()
	if webCand["talentPoolConsent"] != true || webCand["consentAt"] == nil || webCand["source"] != "website" || webCand["cvFileId"] == nil ||
		str(webCand["retentionUntil"]) != time.Now().In(loc).AddDate(1, 0, 0).Format("2006-01-02") {
		t.Fatalf("talent pool retention (12 months, §16 #10): %v", webCand)
	}

	// FR-RCT-03: screening, interviews and scorecards.
	hr.Must(422, "POST", hrBase+"/applications/"+a2+":move-stage", map[string]any{"stage": "screening", "screeningScore": "9"})
	hr.Must(422, "POST", hrBase+"/applications/"+a2+":move-stage", map[string]any{"stage": "hired"})
	hr.Must(200, "POST", hrBase+"/applications/"+a2+":move-stage", map[string]any{"stage": "screening", "screeningScore": "4", "note": "Good fit"})
	hr.Must(409, "POST", hrBase+"/applications/"+a2+":move-stage", map[string]any{"stage": "screening"})
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1)
	at := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 10, 0, 0, 0, loc).Format(time.RFC3339)
	iv := hr.Must(201, "POST", hrBase+"/applications/"+a2+"/interviews", map[string]any{"scheduledAt": at, "interviewerIds": []string{hendro},
		"interviewType": "onsite", "location": "Clubhouse", "inviteCandidate": true}, "Idempotency-Key", newKey()).JSON()
	ivID := str(iv["id"])
	if iv["round"] != float64(1) || iv["status"] != "scheduled" || talentApp(t, hr, a2)["stage"] != "interview" {
		t.Fatalf("interview: %v", iv)
	}
	hr.Must(422, "POST", hrBase+"/applications/"+a2+"/interviews", map[string]any{"scheduledAt": at, "interviewerIds": []string{}})
	hr.Must(200, "PATCH", hrBase+"/interviews/"+ivID, map[string]any{"durationMinutes": 45})
	iv2 := idOf(hr.Must(201, "POST", hrBase+"/applications/"+a2+"/interviews", map[string]any{"scheduledAt": at, "interviewerIds": []string{hendro},
		"interviewType": "panel"}, "Idempotency-Key", newKey()))
	hr.Must(200, "POST", hrBase+"/interviews/"+iv2+":cancel", map[string]any{"reason": "Merged with round 1"})
	mine := head.Must(200, "GET", hrBase+"/interviews?mine=true", nil).Items()
	can := false
	for _, x := range mine {
		if x["id"] == ivID {
			can = x["canScore"] == true
		}
	}
	if !can {
		t.Fatalf("the interviewer sees the interview to score: %v", mine)
	}
	gm.Must(403, "POST", hrBase+"/interviews/"+ivID+"/scorecards", map[string]any{"scores": talentCriteria("4"), "recommendation": "yes"})
	head.Must(422, "POST", hrBase+"/interviews/"+ivID+"/scorecards", map[string]any{"scores": talentCriteria("4")[:2], "recommendation": "yes"})
	sc := head.Must(201, "POST", hrBase+"/interviews/"+ivID+"/scorecards", map[string]any{"scores": talentCriteria("4"), "recommendation": "yes",
		"comments": "Calm under pressure"}, "Idempotency-Key", newKey()).JSON()
	if cards := sc["scorecards"].([]any); len(cards) != 1 || cards[0].(map[string]any)["overallScore"] != "4" {
		t.Fatalf("scorecard: %v", sc["scorecards"])
	}
	head.Must(409, "POST", hrBase+"/interviews/"+ivID+"/scorecards", map[string]any{"scores": talentCriteria("5"), "recommendation": "yes"})
	done := hr.Must(200, "POST", hrBase+"/interviews/"+ivID+":complete", map[string]any{"notes": "Hire"}).JSON()
	if done["status"] != "completed" || done["result"] != "pass" || done["score"] != "4" || talentApp(t, hr, a2)["rating"] != "4" {
		t.Fatalf("completed interview: %v", done)
	}
	hr.Must(409, "POST", hrBase+"/interviews/"+ivID+":cancel", map[string]any{"reason": "late"})

	// FR-RCT-04: the offer above 6 M needs the GM; rejected, edited, approved, letter, sent, accepted.
	start := hrDay(14)
	end := time.Now().In(loc).AddDate(1, 0, 13).Format("2006-01-02")
	hr.Must(422, "POST", hrBase+"/applications/"+a2+":offer", map[string]any{"startDate": start, "baseSalary": "6500000"}) // PKWT without end date
	hr.Must(422, "POST", hrBase+"/applications/"+a2+":offer", map[string]any{"startDate": start, "endDate": end, "probationMonths": 3, "baseSalary": "6500000"})
	of := hr.Must(201, "POST", hrBase+"/applications/"+a2+":offer", map[string]any{"startDate": start, "endDate": end, "baseSalary": "6500000",
		"allowances": []map[string]any{{"name": "Meal allowance", "amount": "400000"}}}, "Idempotency-Key", newKey()).JSON()
	ofID := str(of["id"])
	if of["status"] != "submitted" || of["contractType"] != "pkwt" || talentApp(t, hr, a2)["stage"] != "offered" || !strings.HasPrefix(str(of["number"]), "OFR-") {
		t.Fatalf("offer: %v", of)
	}
	hr.Must(409, "POST", hrBase+"/applications/"+a2+":offer", map[string]any{"startDate": start, "endDate": end, "baseSalary": "6000000"})
	hr.Must(409, "PATCH", hrBase+"/job-offers/"+ofID, map[string]any{"baseSalary": "6100000"}) // submitted
	if o := gm.Must(200, "POST", hrBase+"/job-offers/"+ofID+":reject", map[string]any{"reason": "Above the waiter band"}).JSON(); o["status"] != "rejected" {
		t.Fatalf("offer rejected: %v", o)
	}
	if st := talentApp(t, hr, a2)["stage"]; st != "interview" {
		t.Fatalf("rejected offer sends the application back to interview: %v", st)
	}
	hr.Must(200, "PATCH", hrBase+"/job-offers/"+ofID, map[string]any{"baseSalary": "6200000"})
	hr.Must(200, "POST", hrBase+"/job-offers/"+ofID+":submit", map[string]any{"reason": "Revised"})
	if o := gm.Must(200, "POST", hrBase+"/job-offers/"+ofID+":approve", map[string]any{}).JSON(); o["status"] != "approved" || o["baseSalary"] != "6200000" {
		t.Fatalf("offer approved (GM sees the salary): %v", o)
	}
	if o := head.Must(200, "GET", hrBase+"/job-offers/"+ofID, nil).JSON(); o["baseSalary"] != "[REDACTED]" {
		t.Fatalf("offer salary masked: %v", o["baseSalary"])
	}
	if letter := hr.Must(200, "GET", hrBase+"/job-offers/"+ofID+"/letter", nil); !strings.HasPrefix(string(letter.Body), "%PDF") ||
		letter.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("offer letter: %s", letter.Header)
	}
	head.Must(403, "GET", hrBase+"/job-offers/"+ofID+"/letter", nil)
	hr.Must(409, "POST", hrBase+"/applications/"+a2+":hire", map[string]any{}) // not accepted yet
	if o := hr.Must(200, "POST", hrBase+"/job-offers/"+ofID+":send", map[string]any{"email": true}).JSON(); o["status"] != "sent" ||
		str(o["expiresOn"])[:10] != hrDay(7) {
		t.Fatalf("offer sent: %v", o)
	}
	hr.Must(200, "POST", hrBase+"/job-offers/"+ofID+":accept", map[string]any{"reason": "Signed the offer letter"})

	// Hired: employee + active contract + login + onboarding checklist through Core HR; requisition filled.
	hire := hr.Must(200, "POST", hrBase+"/applications/"+a2+":hire", map[string]any{"workEmail": "bima" + sfx + "@club.test"}).JSON()
	emp := hire["employee"].(map[string]any)
	eid := str(emp["employeeId"])
	if hire["application"].(map[string]any)["stage"] != "hired" || emp["userId"] == nil || !strings.HasPrefix(str(emp["employeeNo"]), "EMP-") ||
		hire["requisition"].(map[string]any)["status"] != "filled" || len(hire["onboarding"].([]any)) < 5 {
		t.Fatalf("hire: %v", hire)
	}
	if hrEvents(t, hris.EventEmployeeHired, eid) != 1 {
		t.Fatal("hris.employee_hired not published for the hire")
	}
	e := hr.Must(200, "GET", hrBase+"/employees/"+eid, nil).JSON()
	if e["fullName"] != "Bima Candidate "+sfx || e["positionId"] != waiter || e["employmentStatus"] != "contract" || str(e["joinDate"])[:10] != start ||
		e["supervisorId"] != hendro {
		t.Fatalf("hired employee: %v", e)
	}
	ctrs := hr.Must(200, "GET", hrBase+"/contracts?employeeId="+eid, nil).Items()
	if len(ctrs) != 1 || ctrs[0]["status"] != "active" || ctrs[0]["contractType"] != "pkwt" || ctrs[0]["baseSalary"] != "6200000" {
		t.Fatalf("contract of the hire: %v", ctrs)
	}
	onb := hr.Must(200, "GET", hrBase+"/employees/"+eid+"/onboarding", nil).Items()
	var item string
	loginDone := false
	for _, o := range onb {
		if o["code"] == "login" {
			loginDone = o["status"] == "done"
		}
		if o["code"] == "bpjs" {
			item = str(o["id"])
		}
	}
	if !loginDone || item == "" {
		t.Fatalf("onboarding checklist: %v", onb)
	}
	if o := hr.Must(200, "PATCH", hrBase+"/onboarding-items/"+item, map[string]any{"status": "done", "notes": "BPJS card requested"}).JSON(); o["status"] != "done" ||
		o["doneByName"] == nil {
		t.Fatalf("onboarding item: %v", o)
	}
	hr.Must(422, "PATCH", hrBase+"/onboarding-items/"+item, map[string]any{"status": "maybe"})
	hr.Must(409, "POST", hrBase+"/applications/"+a2+":hire", map[string]any{})
	if c := hr.Must(200, "GET", hrBase+"/candidates/"+c2, nil).JSON(); c["status"] != "hired" || c["employeeId"] != eid {
		t.Fatalf("hired candidate: %v", c)
	}
	if listed() {
		t.Fatal("a filled requisition is still on the careers page")
	}
	hr.Must(409, "POST", hrBase+"/job-requisitions/"+r1id+":reopen", map[string]any{})

	// Decline, cancel, withdraw and expiry on a PKWTT requisition without approval step.
	r3 := hr.Must(201, "POST", hrBase+"/job-requisitions", map[string]any{"title": "Cook " + sfx, "positionId": hrID(t, "positions", "COOK"), "headcount": 3,
		"contractType": "pkwtt"}, "Idempotency-Key", newKey()).JSON()
	r3id := str(r3["id"])
	if r := hr.Must(200, "POST", hrBase+"/job-requisitions/"+r3id+":submit", map[string]any{}).JSON(); r["status"] != "open" {
		t.Fatalf("no approval step: open at once: %v", r)
	}
	offer := func(app string) string {
		hr.Must(200, "POST", hrBase+"/applications/"+app+":move-stage", map[string]any{"stage": "interview"})
		o := hr.Must(201, "POST", hrBase+"/applications/"+app+":offer", map[string]any{"startDate": start, "probationMonths": 3, "baseSalary": "5500000"},
			"Idempotency-Key", newKey()).JSON()
		if o["status"] != "approved" {
			t.Fatalf("offer without approval step: %v", o)
		}
		return str(o["id"])
	}
	d1 := talentApply(t, hr, r3id, talentCandidate(t, hr, "Decline "+sfx, "d1"+sfx+"@mail.test", nil))
	o1 := offer(d1)
	hr.Must(409, "POST", hrBase+"/job-offers/"+o1+":submit", map[string]any{})
	hr.Must(200, "POST", hrBase+"/job-offers/"+o1+":send", map[string]any{})
	if o := hr.Must(200, "POST", hrBase+"/job-offers/"+o1+":decline", map[string]any{"reason": "Salary too low"}).JSON(); o["status"] != "declined" {
		t.Fatalf("declined: %v", o)
	}
	if a := talentApp(t, hr, d1); a["stage"] != "withdrawn" {
		t.Fatalf("declined offer withdraws the application: %v", a)
	}
	d2 := talentApply(t, hr, r3id, talentCandidate(t, hr, "Cancel "+sfx, "d2"+sfx+"@mail.test", nil))
	o2 := offer(d2)
	hr.Must(422, "POST", hrBase+"/job-offers/"+o2+":cancel", map[string]any{})
	hr.Must(200, "POST", hrBase+"/job-offers/"+o2+":cancel", map[string]any{"reason": "Position re-graded"})
	if a := talentApp(t, hr, d2); a["stage"] != "interview" {
		t.Fatalf("cancelled offer: back to interview: %v", a)
	}
	if a := hr.Must(200, "POST", hrBase+"/applications/"+d2+":withdraw", map[string]any{"reason": "Moved abroad"}).JSON(); a["stage"] != "withdrawn" {
		t.Fatalf("withdrawn: %v", a)
	}
	hr.Must(409, "POST", hrBase+"/applications/"+d2+":reject", map[string]any{"reason": "late"})
	d3 := talentApply(t, hr, r3id, talentCandidate(t, hr, "Expire "+sfx, "d3"+sfx+"@mail.test", nil))
	o3 := offer(d3)
	hr.Must(200, "POST", hrBase+"/job-offers/"+o3+":send", map[string]any{"expiresOn": hrDay(1)})
	sysExec(t, inst, `UPDATE hris.job_offers SET expires_on = $2::date WHERE id = $1`, o3, hrDay(-2))
	if _, expired, _ := talentDaily(t); expired < 1 {
		t.Fatal("daily job expires unanswered offers")
	}
	if o := hr.Must(200, "GET", hrBase+"/job-offers/"+o3, nil).JSON(); o["status"] != "expired" || talentApp(t, hr, d3)["stage"] != "withdrawn" {
		t.Fatalf("expired offer: %v", o)
	}

	// FR-RCT-06: without talent pool consent the applicant is erased after the short retention; on request at once.
	c4 := talentCandidate(t, hr, "Erase Me "+sfx, "d4"+sfx+"@mail.test", nil)
	d4 := talentApply(t, hr, r3id, c4)
	hr.Must(409, "POST", hrBase+"/candidates/"+c4+":erase", map[string]any{"reason": "Request"}) // application open
	hr.Must(200, "POST", hrBase+"/applications/"+d4+":reject", map[string]any{"reason": "Not suitable"})
	if c := hr.Must(200, "GET", hrBase+"/candidates/"+c4, nil).JSON(); str(c["retentionUntil"]) != hrDay(30) {
		t.Fatalf("retention without consent (30 days): %v", c["retentionUntil"])
	}
	sysExec(t, inst, `UPDATE hris.candidates SET retention_until = $2::date WHERE id = $1`, c4, hrDay(-2))
	if erased, _, _ := talentDaily(t); erased < 1 {
		t.Fatal("daily job erases applicants past retention")
	}
	if c := hr.Must(200, "GET", hrBase+"/candidates/"+c4, nil).JSON(); c["status"] != "erased" || c["fullName"] != "Erased applicant" || c["email"] != nil ||
		c["phone"] != nil {
		t.Fatalf("erased candidate: %v", c)
	}
	hr.Must(409, "PATCH", hrBase+"/candidates/"+c4, map[string]any{"city": "Bogor"})
	c5 := talentCandidate(t, hr, "Forget Me "+sfx, "d5"+sfx+"@mail.test", map[string]any{"cvFileId": idOf(hr.Must(201, "POST", hrBase+"/recruitment-files", body,
		"Content-Type", ctype))})
	d5 := talentApply(t, hr, r3id, c5)
	hr.Must(200, "POST", hrBase+"/applications/"+d5+":withdraw", map[string]any{"reason": "Changed mind"})
	hr.Must(422, "POST", hrBase+"/candidates/"+c5+":erase", map[string]any{})
	if c := hr.Must(200, "POST", hrBase+"/candidates/"+c5+":erase", map[string]any{"reason": "Data subject request"}).JSON(); c["status"] != "erased" ||
		c["hasCv"] != false {
		t.Fatalf("erase on request: %v", c)
	}
	hr.Must(404, "GET", hrBase+"/candidates/"+c5+"/cv", nil)
	hr.Must(409, "POST", hrBase+"/candidates/"+c5+":erase", map[string]any{"reason": "again"})
	hr.Must(409, "POST", hrBase+"/candidates/"+c2+":erase", map[string]any{"reason": "hired"})
	unused := talentCandidate(t, hr, "Unused "+sfx, "u"+sfx+"@mail.test", nil)
	hr.Must(204, "DELETE", hrBase+"/candidates/"+unused, nil)

	// EP-27: Recruitment Funnel and Time to Hire reports; HR Performance KPIs.
	from, to := hrDay(-30), hrDay(30)
	funnel := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.recruitment_funnel?params[from]="+from+"&params[to]="+to, nil).JSON()
	found := false
	for _, row := range funnel["rows"].([]any) {
		r := row.(map[string]any)
		if r["requisition"] == r1["number"] {
			found = true
			if r["applications"] != float64(2) || r["hired"] != float64(1) || r["rejected"] != float64(1) || r["interview"] != float64(1) || r["conversion"] != "0.5" {
				t.Fatalf("funnel row: %v", r)
			}
		}
	}
	if !found {
		t.Fatalf("funnel misses the requisition: %v", funnel["rows"])
	}
	tth := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.time_to_hire?params[from]="+from+"&params[to]="+to, nil).JSON()
	found = false
	for _, row := range tth["rows"].([]any) {
		r := row.(map[string]any)
		if r["application"] == talentApp(t, hr, a2)["number"] {
			found = r["timeToHire"] == float64(0) && r["source"] == "referral"
		}
	}
	if !found {
		t.Fatalf("time to hire: %v", tth["rows"])
	}
	head.Must(403, "GET", "/api/v1/reporting/reports/hris.time_to_hire", nil)
	kpis := map[string]map[string]any{}
	for _, k := range hr.Must(200, "GET", "/api/v1/reporting/hr-performance?from="+from+"&to="+to, nil).JSON()["kpis"].([]any) {
		kpis[str(k.(map[string]any)["key"])] = k.(map[string]any)
	}
	for _, key := range []string{"time_to_hire", "open_positions", "review_completion", "review_score"} {
		if kpis[key] == nil || kpis[key]["status"] != "available" || kpis[key]["value"] == nil {
			t.Errorf("HR Performance KPI %s: %v", key, kpis[key])
		}
	}

	// The careers page can be switched off (Recruitment Configuration).
	hrPolicy(t, sa, hris.RecruitmentConfigurationCode, "HR Configuration", map[string]any{"careersPage": false})
	pub.Must(404, "GET", "/api/v1/public/careers?propertyId="+inst.Main.String(), nil)
}

// EP-05 (FR-PRF-HR-01–04): templates, cycle launch, self assessment and
// manager review in ESS, HR reopen / manager review, calibration against
// the guide, close (history, event, notification), acknowledgment,
// promotion through Core HR, probation cycle, reminders and reports.
func TestP5HRTalentPerformance(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	sfx := hrSuffix()
	loc := clubLoc(inst)

	// Organization of the test: a unit under the GM with a manager and two staff.
	unit := idOf(hr.Must(201, "POST", hrBase+"/org-units", map[string]any{"code": "PRF" + sfx, "name": "Review Unit " + sfx,
		"parentId": hrID(t, "org_units", "GM-OFFICE")}, "Idempotency-Key", newKey()))
	mgrPos := hr.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "PRF-MGR" + sfx, "name": "Review Manager", "orgUnitId": unit,
		"gradeId": hrID(t, "grades", "G5"), "isHead": true}, "Idempotency-Key", newKey()).JSON()
	staffPos := hr.Must(201, "POST", hrBase+"/positions", map[string]any{"code": "PRF-STF" + sfx, "name": "Review Staff", "orgUnitId": unit,
		"gradeId": hrID(t, "grades", "G1")}, "Idempotency-Key", newKey()).JSON()
	newEmp := func(name, pos string, extra map[string]any) string {
		b := map[string]any{"fullName": name + " " + sfx, "positionId": pos, "joinDate": hrDay(-500), "employmentStatus": "permanent",
			"email": strings.ToLower(strings.Fields(name)[0]) + sfx + "@club.test"}
		for k, v := range extra {
			b[k] = v
		}
		return idOf(hr.Must(201, "POST", hrBase+"/employees", b, "Idempotency-Key", newKey()))
	}
	mgr := newEmp("Maya Manager", str(mgrPos["id"]), nil)
	e1 := newEmp("Edo Staff", str(staffPos["id"]), map[string]any{"supervisorId": mgr})
	e2 := newEmp("Eva Staff", str(staffPos["id"]), map[string]any{"supervisorId": mgr})
	hr.Must(200, "PATCH", hrBase+"/org-units/"+unit, map[string]any{"headEmployeeId": mgr})
	account := func(eid string, roles []string) *Client {
		acct := hr.Must(200, "POST", hrBase+"/employees/"+eid+":create-account", map[string]any{"roleCodes": roles}).JSON()["account"].(map[string]any)
		sysExec(t, inst, `UPDATE platform.users SET password_hash = (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id'),
			password_changed_at = now() WHERE id = $1`, str(acct["userId"]))
		return login(t, inst, str(acct["email"]), demoPassword)
	}
	mc := account(mgr, []string{"employee_self_service", "department_head"})
	ec := account(e1, nil)

	// FR-PRF-HR-01: template per position with competencies and KPIs (validated).
	hr.Must(422, "POST", hrBase+"/review-templates", map[string]any{"code": "BAD" + sfx, "name": "Bad", "competencies": []map[string]any{{"code": "a b", "label": "x"}}})
	hr.Must(422, "POST", hrBase+"/review-templates", map[string]any{"code": "BAD" + sfx, "name": "Bad", "positionCodes": []string{"NOPE" + sfx}})
	tpl := hr.Must(201, "POST", hrBase+"/review-templates", map[string]any{"code": "PRF-T" + sfx, "name": "Review Staff " + sfx, "reviewType": "annual",
		"positionCodes": []string{"PRF-STF" + sfx}, "competencyWeight": "60",
		"competencies": []map[string]any{{"code": "service", "label": "Service", "weight": "2"}, {"code": "teamwork", "label": "Teamwork"}},
		"kpis":         []map[string]any{{"code": "attendance", "label": "Attendance", "target": "≥ 97%"}}}, "Idempotency-Key", newKey()).JSON()
	if len(tpl["competencies"].([]any)) != 2 || tpl["competencies"].([]any)[1].(map[string]any)["weight"] != "1" {
		t.Fatalf("template: %v", tpl)
	}
	hr.Must(200, "PATCH", hrBase+"/review-templates/"+str(tpl["id"]), map[string]any{"description": "Annual staff review"})
	spare := idOf(hr.Must(201, "POST", hrBase+"/review-templates", map[string]any{"code": "SPARE" + sfx, "name": "Spare"}, "Idempotency-Key", newKey()))
	hr.Must(204, "DELETE", hrBase+"/review-templates/"+spare, nil)

	// Cycle: draft, edited, launched with the reviews of the unit (reviewer = supervisor).
	year := time.Now().In(loc).Year()
	cyc := hr.Must(201, "POST", hrBase+"/review-cycles", map[string]any{"code": "PRF-" + sfx, "name": "Review " + sfx, "cycleType": "annual",
		"periodStart": time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), "periodEnd": time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02"),
		"selfDue": hrDay(10), "managerDue": hrDay(20), "orgUnitId": unit, "defaultTemplateId": tpl["id"]}, "Idempotency-Key", newKey()).JSON()
	cid := str(cyc["id"])
	hr.Must(409, "POST", hrBase+"/review-cycles", map[string]any{"code": "PRF-" + sfx, "name": "Dup", "cycleType": "annual", "periodStart": hrDay(0), "periodEnd": hrDay(1)})
	hr.Must(422, "POST", hrBase+"/review-cycles", map[string]any{"code": "X" + sfx, "name": "Bad", "cycleType": "annual", "periodStart": hrDay(5), "periodEnd": hrDay(1)})
	hr.Must(200, "PATCH", hrBase+"/review-cycles/"+cid, map[string]any{"calibrationDue": hrDay(30)})
	launch := hr.Must(200, "POST", hrBase+"/review-cycles/"+cid+":launch", map[string]any{}).JSON()
	if launch["created"] != float64(3) || launch["cycle"].(map[string]any)["status"] != "in_progress" {
		t.Fatalf("launch: %v", launch)
	}
	hr.Must(409, "PATCH", hrBase+"/review-cycles/"+cid, map[string]any{"periodStart": hrDay(-1)})
	if again := hr.Must(200, "POST", hrBase+"/review-cycles/"+cid+":launch", map[string]any{}).JSON(); again["created"] != float64(0) {
		t.Fatalf("relaunch adds no duplicate: %v", again)
	}
	reviewOf := func(eid string) map[string]any {
		list := hr.Must(200, "GET", hrBase+"/reviews?cycleId="+cid+"&employeeId="+eid, nil).Items()
		if len(list) != 1 {
			t.Fatalf("review of %s: %v", eid, list)
		}
		return list[0]
	}
	r1 := reviewOf(e1)
	r1id := str(r1["id"])
	if r1["status"] != "self_assessment" || r1["reviewerId"] != mgr || r1["templateId"] != tpl["id"] {
		t.Fatalf("review: %v", r1)
	}
	if r := reviewOf(mgr); r["reviewerId"] != hrEmployeeID(t, "EMP-00001") {
		t.Fatalf("the manager's reviewer is the head of the parent unit: %v", r)
	}

	// FR-PRF-HR-02 in ESS: the employee's self assessment.
	me := ec.Must(200, "GET", "/api/v1/ess/me", nil).JSON()
	keys := map[string]bool{}
	for _, s := range me["sections"].([]any) {
		keys[str(s.(map[string]any)["key"])] = true
	}
	if !keys["reviews"] || keys["team-reviews"] {
		t.Fatalf("ESS sections of the employee: %v", keys)
	}
	if mine := ec.Must(200, "GET", "/api/v1/ess/reviews", nil).Items(); len(mine) != 1 || mine[0]["id"] != r1id {
		t.Fatalf("my reviews: %v", mine)
	}
	det := ec.Must(200, "GET", "/api/v1/ess/reviews/"+r1id, nil).JSON()
	if det["canEditSelf"] != true || len(det["scores"].([]any)) != 3 {
		t.Fatalf("my review: %v", det)
	}
	ec.Must(200, "PATCH", "/api/v1/ess/reviews/"+r1id, map[string]any{"scores": []map[string]any{{"itemKind": "competency", "code": "service", "score": "4"}}})
	ec.Must(409, "POST", "/api/v1/ess/reviews/"+r1id+":submit", map[string]any{})
	ec.Must(422, "PATCH", "/api/v1/ess/reviews/"+r1id, map[string]any{"scores": []map[string]any{{"itemKind": "competency", "code": "teamwork", "score": "7"}}})
	ec.Must(422, "PATCH", "/api/v1/ess/reviews/"+r1id, map[string]any{"scores": []map[string]any{{"itemKind": "kpi", "code": "nope", "score": "3"}}})
	ec.Must(200, "PATCH", "/api/v1/ess/reviews/"+r1id, map[string]any{"comment": "Good year", "scores": []map[string]any{
		{"itemKind": "competency", "code": "teamwork", "score": "4"}, {"itemKind": "kpi", "code": "attendance", "score": "5", "actual": "99%"}}})
	sub := ec.Must(200, "POST", "/api/v1/ess/reviews/"+r1id+":submit", map[string]any{}).JSON()["review"].(map[string]any)
	// self: competencies (4×2 + 4×1)/3 = 4, KPI 5 → 4 × 60% + 5 × 40% = 4.4
	if sub["status"] != "manager_review" || sub["selfScore"] != "4.4" || sub["managerScore"] != nil {
		t.Fatalf("self assessment submitted: %v", sub)
	}
	ec.Must(409, "PATCH", "/api/v1/ess/reviews/"+r1id, map[string]any{"comment": "late"})
	ec.Must(403, "GET", "/api/v1/ess/team-reviews", nil)

	// The manager reviews the team in ESS.
	mme := mc.Must(200, "GET", "/api/v1/ess/me", nil).JSON()
	keys = map[string]bool{}
	for _, s := range mme["sections"].([]any) {
		keys[str(s.(map[string]any)["key"])] = true
	}
	if !keys["team-reviews"] {
		t.Fatalf("ESS sections of the manager: %v", keys)
	}
	team := mc.Must(200, "GET", "/api/v1/ess/team-reviews", nil).Items()
	if len(team) != 2 || team[0]["id"] != r1id {
		t.Fatalf("team reviews (manager review first): %v", team)
	}
	r2id := str(reviewOf(e2)["id"])
	mc.Must(409, "PATCH", "/api/v1/ess/team-reviews/"+r2id, map[string]any{"comment": "too early"}) // self assessment pending
	td := mc.Must(200, "GET", "/api/v1/ess/team-reviews/"+r1id, nil).JSON()
	if td["canEditManager"] != true || td["review"].(map[string]any)["selfScore"] != "4.4" {
		t.Fatalf("team review: %v", td)
	}
	mc.Must(200, "PATCH", "/api/v1/ess/team-reviews/"+r1id, map[string]any{"comment": "Solid", "strengths": "Members love Edo", "goals": "Lead a shift",
		"scores": []map[string]any{{"itemKind": "competency", "code": "service", "score": "4"}, {"itemKind": "competency", "code": "teamwork", "score": "3"},
			{"itemKind": "kpi", "code": "attendance", "score": "4"}}})
	ms := mc.Must(200, "POST", "/api/v1/ess/team-reviews/"+r1id+":submit", map[string]any{}).JSON()["review"].(map[string]any)
	// manager: (8+3)/3 = 3.67, KPI 4 → 3.67×0.6 + 4×0.4 = 3.8 → Exceeds Expectations
	if ms["status"] != "submitted" || ms["managerScore"] != "3.8" || ms["recommendedRating"] != "exceeds" {
		t.Fatalf("manager review submitted: %v", ms)
	}
	if mine := ec.Must(200, "GET", "/api/v1/ess/reviews/"+r1id, nil).JSON()["review"].(map[string]any); mine["managerScore"] != nil || mine["recommendedRating"] != nil {
		t.Fatalf("the employee does not see the manager review before completion: %v", mine)
	}

	// HR sends it back, completes the manager review and submits it.
	hr.Must(422, "POST", hrBase+"/reviews/"+r1id+":reopen", map[string]any{})
	hr.Must(200, "POST", hrBase+"/reviews/"+r1id+":reopen", map[string]any{"reason": "Score teamwork again"})
	hr.Must(200, "PATCH", hrBase+"/reviews/"+r1id, map[string]any{"scores": []map[string]any{{"itemKind": "competency", "code": "teamwork", "score": "5"},
		{"itemKind": "competency", "code": "service", "score": "5"}, {"itemKind": "kpi", "code": "attendance", "score": "5"}}, "improvements": "Wine knowledge"})
	hs := hr.Must(200, "POST", hrBase+"/reviews/"+r1id+":submit", map[string]any{}).JSON()["review"].(map[string]any)
	if hs["managerScore"] != "5" || hs["recommendedRating"] != "outstanding" {
		t.Fatalf("HR submitted: %v", hs)
	}

	// Reminders N days before the due date (Performance Review Configuration reminderDays 3).
	hr.Must(200, "PATCH", hrBase+"/review-cycles/"+cid, map[string]any{"selfDue": hrDay(3)})
	if _, _, rem := talentDaily(t); rem < 1 {
		t.Fatal("no review reminder sent")
	}

	// Calibration against the guide; rating overrides need a note.
	hr.Must(200, "POST", hrBase+"/review-cycles/"+cid+":start-calibration", map[string]any{})
	hr.Must(409, "POST", hrBase+"/review-cycles/"+cid+":start-calibration", map[string]any{})
	cal := hr.Must(200, "GET", hrBase+"/review-cycles/"+cid+"/calibration", nil).JSON()
	dist := cal["distribution"].([]any)
	if len(cal["reviews"].([]any)) != 1 || dist[0].(map[string]any)["code"] != "outstanding" || dist[0].(map[string]any)["count"] != float64(1) ||
		dist[0].(map[string]any)["over"] != true {
		t.Fatalf("calibration view: %v", cal)
	}
	hr.Must(409, "POST", hrBase+"/reviews/"+r2id+":calibrate", map[string]any{})
	hr.Must(422, "POST", hrBase+"/reviews/"+r1id+":calibrate", map[string]any{"finalRating": "exceeds"})
	hr.Must(422, "POST", hrBase+"/reviews/"+r1id+":calibrate", map[string]any{"finalRating": "superstar", "note": "x"})
	cr := hr.Must(200, "POST", hrBase+"/reviews/"+r1id+":calibrate", map[string]any{"finalRating": "exceeds", "note": "Guide: 10% outstanding"}).JSON()["review"].(map[string]any)
	if cr["status"] != "calibrated" || cr["finalRating"] != "exceeds" || cr["finalScore"] != "5" || cr["increasePercent"] != "7" ||
		cr["bonusMonths"] != "1.5" || cr["recommendation"] != "salary_increase" {
		t.Fatalf("calibrated: %v", cr)
	}

	// Close: open reviews block unless forced; results go to the history, the event and the employee.
	hr.Must(409, "POST", hrBase+"/review-cycles/"+cid+":close", map[string]any{})
	hr.Must(422, "POST", hrBase+"/review-cycles/"+cid+":close", map[string]any{"force": true})
	closed := hr.Must(200, "POST", hrBase+"/review-cycles/"+cid+":close", map[string]any{"force": true, "reason": "Year end"}).JSON()
	if closed["status"] != "completed" || closed["completed"] != float64(1) || closed["reviews"] != float64(1) {
		t.Fatalf("closed cycle: %v", closed)
	}
	if r := reviewOf(e2); r["status"] != "cancelled" {
		t.Fatalf("forced close cancels open reviews: %v", r)
	}
	if hrEvents(t, hris.EventPerformanceReviewCompleted, r1id) != 1 {
		t.Fatal("hris.performance_review_completed not published")
	}
	hist := hr.Must(200, "GET", hrBase+"/employees/"+e1+"/history", nil).Items()
	inHistory := false
	for _, h := range hist {
		if h["kind"] == "performance_review" && strings.Contains(str(h["reason"]), "Exceeds") {
			inHistory = true
		}
	}
	if !inHistory {
		t.Fatalf("review result in the employment history: %v", hist)
	}
	ctx := dbtx.System(context.Background())
	var res *hris.ReviewResult
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = hris.LatestReviewResult(ctx, tx, mustUUID(e1))
		return err
	}); err != nil || res == nil || res.FinalRating != "exceeds" || res.IncreasePercent == nil || *res.IncreasePercent != "7" {
		t.Fatalf("latest review result (payroll basis): %+v %v", res, err)
	}
	mine := ec.Must(200, "GET", "/api/v1/ess/reviews/"+r1id, nil).JSON()["review"].(map[string]any)
	if mine["finalRating"] != "exceeds" || mine["increasePercent"] != nil {
		t.Fatalf("the employee sees the rating, not the pay consequence: %v", mine)
	}
	ec.Must(200, "POST", "/api/v1/ess/reviews/"+r1id+":acknowledge", map[string]any{})
	ec.Must(409, "POST", "/api/v1/ess/reviews/"+r1id+":acknowledge", map[string]any{})
	if h := hr.Must(200, "GET", hrBase+"/employees/"+e1+"/reviews", nil).Items(); len(h) != 1 || h[0]["acknowledgedAt"] == nil {
		t.Fatalf("review history of the employee: %v", h)
	}

	// FR-PRF-HR-04: promotion from the review through Core HR :promote.
	hr.Must(422, "POST", hrBase+"/reviews/"+r1id+":promote", map[string]any{"effectiveDate": hrDay(0)}) // no position / grade change
	pr := hr.Must(200, "POST", hrBase+"/reviews/"+r1id+":promote", map[string]any{"gradeId": hrID(t, "grades", "G2"), "effectiveDate": hrDay(0)}).JSON()
	if pr["review"].(map[string]any)["employmentChangeId"] == nil || pr["review"].(map[string]any)["recommendation"] != "promotion" {
		t.Fatalf("promotion: %v", pr["review"])
	}
	if e := hr.Must(200, "GET", hrBase+"/employees/"+e1, nil).JSON(); e["gradeId"] != hrID(t, "grades", "G2") {
		t.Fatalf("promoted grade: %v", e["gradeId"])
	}
	hr.Must(409, "POST", hrBase+"/reviews/"+r1id+":promote", map[string]any{"gradeId": hrID(t, "grades", "G3"), "effectiveDate": hrDay(0)})

	// Probation cycle: the employee whose probation ends in the period; cancelled.
	e3 := newEmp("Pria Probation", str(staffPos["id"]), map[string]any{"supervisorId": mgr, "employmentStatus": "probation", "joinDate": hrDay(-80)})
	hr.Must(201, "POST", hrBase+"/contracts", map[string]any{"employeeId": e3, "contractType": "pkwtt", "startDate": hrDay(-80), "probationMonths": 3,
		"baseSalary": "5500000", "activate": true}, "Idempotency-Key", newKey())
	hr.Must(201, "POST", hrBase+"/review-templates", map[string]any{"code": "PRB-T" + sfx, "name": "Probation " + sfx, "reviewType": "probation",
		"positionCodes": []string{"PRF-STF" + sfx}, "competencies": []map[string]any{{"code": "attitude", "label": "Attitude"}}}, "Idempotency-Key", newKey())
	pc := idOf(hr.Must(201, "POST", hrBase+"/review-cycles", map[string]any{"code": "PRB-" + sfx, "name": "Probation " + sfx, "cycleType": "probation",
		"periodStart": hrDay(-30), "periodEnd": hrDay(30), "orgUnitId": unit}, "Idempotency-Key", newKey()))
	pl := hr.Must(200, "POST", hrBase+"/review-cycles/"+pc+":launch", map[string]any{}).JSON()
	if pl["created"] != float64(1) {
		t.Fatalf("probation launch: %v", pl)
	}
	if rv := hr.Must(200, "GET", hrBase+"/reviews?cycleId="+pc, nil).Items(); len(rv) != 1 || rv[0]["employeeId"] != e3 || rv[0]["cycleType"] != "probation" {
		t.Fatalf("probation review: %v", rv)
	}
	hr.Must(422, "POST", hrBase+"/review-cycles/"+pc+":cancel", map[string]any{})
	if c := hr.Must(200, "POST", hrBase+"/review-cycles/"+pc+":cancel", map[string]any{"reason": "Re-plan"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled cycle: %v", c)
	}
	hr.Must(409, "POST", hrBase+"/review-cycles/"+pc+":launch", map[string]any{})

	// EP-27: Performance Review and Rating Distribution reports.
	from, to := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	rep := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.performance_review?params[from]="+from+"&params[to]="+to, nil).JSON()
	found := false
	for _, row := range rep["rows"].([]any) {
		r := row.(map[string]any)
		if r["cycle"] == "Review "+sfx && r["employee"] == "Edo Staff "+sfx {
			found = r["rating"] == "exceeds" && r["promoted"] == "yes" && r["acknowledged"] == "yes" && r["finalScore"] == "5"
		}
	}
	if !found {
		t.Fatalf("performance review report: %v", rep["rows"])
	}
	dr := hr.Must(200, "GET", "/api/v1/reporting/reports/hris.performance_distribution?params[from]="+from+"&params[to]="+to, nil).JSON()
	found = false
	for _, row := range dr["rows"].([]any) {
		r := row.(map[string]any)
		if r["cycle"] == "Review "+sfx && r["rating"] == "exceeds" {
			found = r["reviews"] == float64(1) && r["share"] == "1" && r["completion"] == "1"
		}
	}
	if !found {
		t.Fatalf("distribution report: %v", dr["rows"])
	}
}

// Demo data (brief): two open requisitions with candidates in different
// stages and a review cycle in progress usable by the ESS demo users.
func TestP5HRTalentDemo(t *testing.T) {
	hr := login(t, inst, "hr@demo.oneclub.id", demoPassword)
	emp := login(t, inst, "employee@demo.oneclub.id", demoPassword)
	head := login(t, inst, "dept.head@demo.oneclub.id", demoPassword)
	open := map[string]string{}
	for _, r := range hr.Must(200, "GET", hrBase+"/job-requisitions?status=open", nil).Items() {
		if r["title"] == "Lifeguard" || r["title"] == "Waiter / Waitress" {
			open[str(r["title"])] = str(r["id"])
		}
	}
	if len(open) != 2 {
		t.Fatalf("demo requisitions: %v", open)
	}
	stages := map[string]bool{}
	for _, id := range open {
		for _, a := range hr.Must(200, "GET", hrBase+"/applications?requisitionId="+id, nil).Items() {
			stages[str(a["stage"])] = true
		}
	}
	for _, s := range []string{"applied", "screening", "interview", "offered", "rejected", "withdrawn"} {
		if !stages[s] {
			t.Errorf("demo pipeline lacks stage %s: %v", s, stages)
		}
	}
	running := false
	for _, c := range hr.Must(200, "GET", hrBase+"/review-cycles?status=in_progress", nil).Items() {
		if strings.HasPrefix(str(c["code"]), "FNB-ANNUAL-") {
			running = c["reviews"].(float64) >= 5 && c["submitted"].(float64) >= 1 && c["managerPending"].(float64) >= 1
		}
	}
	if !running {
		t.Fatal("demo review cycle in progress")
	}
	self := false
	for _, r := range emp.Must(200, "GET", "/api/v1/ess/reviews", nil).Items() {
		self = self || (r["status"] == "self_assessment" && strings.HasPrefix(str(r["cycleCode"]), "FNB-ANNUAL-"))
	}
	if !self {
		t.Fatal("the ESS demo employee has a self assessment to do")
	}
	waiting := false
	for _, r := range head.Must(200, "GET", "/api/v1/ess/team-reviews?status=manager_review", nil).Items() {
		waiting = waiting || strings.HasPrefix(str(r["cycleCode"]), "FNB-ANNUAL-")
	}
	if !waiting {
		t.Fatal("the F&B Manager has a team review to do")
	}
}
