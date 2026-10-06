package e2e

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"oneclub/internal/platform/integration"
)

// slsOtpCode is the latest acceptance code sent for a quotation number. Only
// a hash is stored with the quotation and the delivery erases the code once
// sent (PO decision 4f), so the code is read from the message the sandbox
// adapter (mock WhatsApp, mock / log e-mail) sent in this process.
func slsOtpCode(t *testing.T, number string) string {
	t.Helper()
	var did, status, stored, body string
	sysQueryRow(t, inst, `SELECT id::text FROM platform.notification_deliveries WHERE event_code = 'crm.quotation_otp'
		AND payload->>'number' = $1 ORDER BY created_at DESC LIMIT 1`, []any{number}, &did)
	waitFor(t, 30*time.Second, "acceptance code of "+number+" sent", func() bool {
		var pending int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'crm.quotation_otp'
			AND payload->>'number' = $1 AND status = 'pending'`, []any{number}, &pending)
		return pending == 0
	})
	sysQueryRow(t, inst, `SELECT status, coalesce(payload->>'otpCode', ''), body FROM platform.notification_deliveries WHERE id = $1`,
		[]any{did}, &status, &stored, &body)
	msgs := integration.SandboxMessages(func(m integration.SandboxMessage) bool {
		return (m.Template == "crm.quotation_otp" && m.Named["number"] == number) ||
			(m.Channel == "email" && strings.Contains(m.Subject, number) && otpInText.MatchString(m.Text))
	})
	if status != "sent" || len(msgs) == 0 {
		t.Fatalf("acceptance code of %s: delivery %s, %d sandbox messages", number, status, len(msgs))
	}
	last := msgs[len(msgs)-1]
	code := last.Named["otpCode"]
	if code == "" {
		if m := otpInText.FindStringSubmatch(last.Text); m != nil {
			code = m[1]
		}
	}
	if len(code) != 6 {
		t.Fatalf("acceptance code of %s: %q (%s)", number, code, last.Text)
	}
	if stored != "[REDACTED]" || strings.Contains(body, code) || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("the sent delivery keeps no code: payload %q, body %q", stored, body)
	}
	return code
}

var otpInText = regexp.MustCompile(`(\d{6}) (?:is your code|adalah kode)`)

// slsAcceptPublic accepts a quotation on its public link with a one-time
// code (Sales Policies requireAcceptanceOtp, PRD P3 §16 #18).
func slsAcceptPublic(t *testing.T, c *Client, token string, body map[string]any) map[string]any {
	t.Helper()
	c.Must(200, "POST", "/api/v1/public/quotations/"+token+":request-otp", nil)
	q := c.Must(200, "GET", "/api/v1/public/quotations/"+token, nil).JSON()
	in := map[string]any{"otpCode": slsOtpCode(t, str(q["number"]))}
	for k, v := range body {
		in[k] = v
	}
	return c.Must(200, "POST", "/api/v1/public/quotations/"+token+":accept", in).JSON()
}

func otpProblem(t *testing.T, r Resp, status int, code string) {
	t.Helper()
	if r.Status != status || r.JSON()["code"] != code {
		t.Fatalf("want %d %s, got %s", status, code, r.String())
	}
}

// PRD P3 §16 #18 (FR-QUO-05): acceptance on the public link with a one-time
// code — request, delivery to the contact on file (WhatsApp / e-mail),
// cooldown, wrong codes, lock, expiry, a new code, acceptance with the
// right code and its evidence and audit trail; the policy switch; no
// contact on file; e-Meterai on quotations above Rp5 jt stamped by the
// sandbox adapter on acceptance, pending while the integration is disabled,
// failed on a provider error and stamped by the staff retry.
func TestP3QuotationOtpAndEMeterai(t *testing.T) {
	sa := superAdmin(t, inst)
	pa := platformAdmin(t, inst)
	gm := login(t, inst, "gm@demo.oneclub.id", demoPassword)
	sx := roleUser(t, inst, "sales_executive")
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	emt := integrationID(t, inst, "mock-emeterai")
	t.Cleanup(func() {
		pa.Do("PATCH", "/api/v1/platform/integrations/"+emt, map[string]any{"enabled": true, "settings": map[string]any{}})
		sa.Do("POST", "/api/v1/platform/club-policies", map[string]any{"category": "Sales Policies", "code": "crm.sales", "name": "crm.sales",
			"value": map[string]any{"requireAcceptanceOtp": true}})
	})
	quote := func(customer, title, price string) map[string]any {
		t.Helper()
		q := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": customer, "title": title, "pricingMode": "nett",
			"lines": []map[string]any{{"itemType": "venue", "description": "Ballroom " + title, "quantity": "1", "unitPrice": price}}},
			"Idempotency-Key", newKey()).JSON()
		return sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":send", map[string]any{"channels": []string{"email"}}).JSON()
	}
	pdfOf := func(qid string) []byte {
		t.Helper()
		return sx.Must(200, "GET", "/api/v1/crm/quotations/"+qid+"/pdf", nil).Body
	}
	auditCount := func(qid, action string) int {
		t.Helper()
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE entity_type = 'crm.quotation' AND entity_id = $1 AND action = $2`,
			[]any{qid, action}, &n)
		return n
	}

	// ── 1. OTP lifecycle on a quotation above the e-Meterai threshold ─────
	phone := "+62817" + sfx
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "OTP" + sfx, "name": "Otp Buyer " + sfx,
		"email": "otp" + sfx + "@buyer.test", "phone": phone}))
	qa := quote(cust, "Gala "+sfx, "6000000")
	qaID, tokA := str(qa["id"]), slsToken(t, qa)
	if qa["eMeteraiRequired"] != true || qa["eMeterai"].(map[string]any)["threshold"] != "5000000" {
		t.Fatalf("staff view flags the e-Meterai: %v", qa["eMeterai"])
	}
	if v := pub.Must(200, "GET", "/api/v1/public/quotations/"+tokA, nil).JSON(); v["otpRequired"] != true || v["eMeteraiRequired"] != true {
		t.Fatalf("public view: %v", v)
	}
	if b := pdfOf(qaID); !bytes.Contains(b, []byte("e-Meterai will be applied on acceptance")) {
		t.Fatal("the PDF announces the e-Meterai before acceptance")
	}
	accept := map[string]any{"name": "Otp Buyer", "termsAccepted": true}
	with := func(code string) map[string]any {
		m := map[string]any{"otpCode": code}
		for k, v := range accept {
			m[k] = v
		}
		return m
	}
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", accept), 422, "otp_required")
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", with("123456")), 422, "otp_not_requested")

	r := pub.Must(200, "POST", "/api/v1/public/quotations/"+tokA+":request-otp", nil)
	sent := r.JSON()
	code := slsOtpCode(t, str(qa["number"]))
	if sent["channel"] != "whatsapp" || !strings.HasSuffix(str(sent["destinationMasked"]), phone[len(phone)-3:]) ||
		!strings.Contains(str(sent["destinationMasked"]), "*") || sent["expiresAt"] == nil || sent["resendAfter"] == nil ||
		strings.Contains(string(r.Body), code) {
		t.Fatalf("code request: %s", r.Body)
	}
	var ch, to string
	sysQueryRow(t, inst, `SELECT channel, recipient FROM platform.notification_deliveries WHERE event_code = 'crm.quotation_otp'
		AND payload->>'number' = $1 ORDER BY created_at DESC LIMIT 1`, []any{qa["number"]}, &ch, &to)
	if ch != "whatsapp" || to != phone {
		t.Fatalf("delivery to the contact on file: %s %s", ch, to)
	}
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":request-otp", nil), 429, "otp_resend_too_soon")
	wrong := fmt.Sprintf("%06d", (atoiT(t, code)+1)%1_000_000)
	for i := 1; i <= 4; i++ {
		otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", with(wrong)), 422, "otp_invalid")
	}
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", with(wrong)), 429, "otp_locked")
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", with(code)), 429, "otp_locked")
	if a, f, l := auditCount(qaID, "otp_requested"), auditCount(qaID, "otp_failed"), auditCount(qaID, "otp_locked"); a != 1 || f != 4 || l != 1 {
		t.Fatalf("audit trail of the code: requested %d, failed %d, locked %d", a, f, l)
	}
	var leaked int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE entity_id = $1 AND (metadata::text LIKE '%' || $2 || '%'
		OR coalesce(after::text, '') LIKE '%' || $2 || '%')`, []any{qaID, code}, &leaked)
	var ip, dest string
	sysQueryRow(t, inst, `SELECT metadata->>'ip', metadata->>'destination' FROM audit.audit_log WHERE entity_id = $1 AND action = 'otp_locked'`,
		[]any{qaID}, &ip, &dest)
	if leaked != 0 || ip == "" || dest != str(sent["destinationMasked"]) {
		t.Fatalf("audit evidence (ip %q, destination %q, code leaked %d)", ip, dest, leaked)
	}

	// A new code after the cooldown; an expired code is refused.
	sysExec(t, inst, `UPDATE crm.sales_quotation_otps SET resend_after = now() - interval '1 second' WHERE quotation_id = $1`, qaID)
	pub.Must(200, "POST", "/api/v1/public/quotations/"+tokA+":request-otp", nil)
	code2 := slsOtpCode(t, str(qa["number"]))
	sysExec(t, inst, `UPDATE crm.sales_quotation_otps SET expires_at = now() - interval '1 second', resend_after = now() - interval '1 second'
		WHERE quotation_id = $1 AND status = 'active'`, qaID)
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+tokA+":accept", with(code2)), 422, "otp_expired")
	pub.Must(200, "POST", "/api/v1/public/quotations/"+tokA+":request-otp", nil)
	code3 := slsOtpCode(t, str(qa["number"]))
	var hash string
	var superseded int
	sysQueryRow(t, inst, `SELECT code_hash, (SELECT count(*) FROM crm.sales_quotation_otps WHERE quotation_id = $1 AND status = 'superseded')
		FROM crm.sales_quotation_otps WHERE quotation_id = $1 AND status = 'active'`, []any{qaID}, &hash, &superseded)
	if len(hash) != 64 || strings.Contains(hash, code3) || superseded != 1 {
		t.Fatalf("only a hash is stored (%q), the earlier code is superseded (%d)", hash, superseded)
	}
	acc := pub.Must(200, "POST", "/api/v1/public/quotations/"+tokA+":accept", with(code3)).JSON()
	em, _ := acc["eMeterai"].(map[string]any)
	if acc["status"] != "accepted" || em == nil || em["status"] != "stamped" || !strings.HasPrefix(str(em["serialNumber"]), "MOCK-EMT-") ||
		em["sandbox"] != true {
		t.Fatalf("accepted with the code, stamped by the sandbox: %v", acc)
	}
	serial := str(em["serialNumber"])
	pub.Must(409, "POST", "/api/v1/public/quotations/"+tokA+":accept", with(code3))
	d := sx.Must(200, "GET", "/api/v1/crm/quotations/"+qaID, nil).JSON()
	ev, _ := d["acceptanceEvidence"].(map[string]any)
	if ev == nil || ev["ip"] == nil || ev["userAgent"] == nil || ev["otpChannel"] != "whatsapp" || ev["otpVerifiedAt"] == nil ||
		ev["otpDestination"] != sent["destinationMasked"] || d["eMeterai"].(map[string]any)["serialNumber"] != serial {
		t.Fatalf("acceptance evidence on the staff view: %v / %v", d["acceptanceEvidence"], d["eMeterai"])
	}
	var status string
	sysQueryRow(t, inst, `SELECT status FROM crm.sales_quotation_otps WHERE quotation_id = $1 ORDER BY created_at DESC LIMIT 1`, []any{qaID}, &status)
	if status != "consumed" {
		t.Fatalf("the code is consumed by the acceptance: %s", status)
	}
	var otpCh string
	sysQueryRow(t, inst, `SELECT metadata->>'otpChannel' FROM audit.audit_log WHERE entity_id = $1 AND action = 'accept'`, []any{qaID}, &otpCh)
	if otpCh != "whatsapp" || auditCount(qaID, "e_meterai_stamped") != 1 {
		t.Fatalf("acceptance audit (otp channel %q, stamped %d)", otpCh, auditCount(qaID, "e_meterai_stamped"))
	}
	if b := pdfOf(qaID); !bytes.Contains(b, []byte(serial)) || !bytes.Contains(b, []byte("MOCK / TRIAL")) {
		t.Fatal("the PDF of the stamped quotation shows the e-Meterai serial and the trial marker")
	}
	var stampLog int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.integration_logs WHERE integration_code = 'mock-emeterai' AND operation = 'stamp'
		AND request->>'documentRef' = $1 AND success`, []any{qaID}, &stampLog)
	if stampLog != 1 {
		t.Fatalf("e-Meterai call log: %d", stampLog)
	}
	if v := pub.Must(200, "GET", "/api/v1/public/quotations/"+tokA, nil).JSON(); v["eMeterai"].(map[string]any)["serialNumber"] != serial {
		t.Fatalf("public view shows the e-Meterai serial: %v", v["eMeterai"])
	}

	// ── 2. Policy off; a quotation at or below the threshold is not stamped ─
	sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Sales Policies", "code": "crm.sales", "name": "crm.sales",
		"value": map[string]any{"requireAcceptanceOtp": false}})
	qb := quote(cust, "Meeting "+sfx, "5000000")
	tokB := slsToken(t, qb)
	if v := pub.Must(200, "GET", "/api/v1/public/quotations/"+tokB, nil).JSON(); v["otpRequired"] != false || v["eMeteraiRequired"] != false ||
		v["eMeterai"] != nil {
		t.Fatalf("public view without OTP and e-Meterai: %v", v)
	}
	accB := pub.Must(200, "POST", "/api/v1/public/quotations/"+tokB+":accept", accept).JSON()
	if accB["status"] != "accepted" || accB["eMeteraiRequired"] != false {
		t.Fatalf("accepted without a code when the policy is off: %v", accB)
	}
	db := sx.Must(200, "GET", "/api/v1/crm/quotations/"+str(qb["id"]), nil).JSON()
	if db["eMeterai"].(map[string]any)["status"] != "not_required" || db["acceptanceEvidence"].(map[string]any)["otpChannel"] != nil ||
		db["acceptanceEvidence"].(map[string]any)["ip"] == nil {
		t.Fatalf("not stamped, no code evidence: %v", db)
	}
	if b := pdfOf(str(qb["id"])); bytes.Contains(b, []byte("e-Meterai")) {
		t.Fatal("no e-Meterai on a quotation at the threshold")
	}
	otpProblem(t, gm.Do("POST", "/api/v1/crm/quotations/"+str(qb["id"])+":stamp-e-meterai", nil), 409, "e_meterai_not_pending")
	sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Sales Policies", "code": "crm.sales", "name": "crm.sales",
		"value": map[string]any{"requireAcceptanceOtp": true}})

	// ── 3. e-mail channel (code redacted in the call log); e-Meterai disabled → pending → failed → stamped by the retry ─
	mail := "otpmail" + sfx + "@buyer.test"
	custC := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "OTM" + sfx, "name": "Mail Buyer " + sfx, "email": mail}))
	qc := quote(custC, "Wedding "+sfx, "7500000")
	qcID, tokC := str(qc["id"]), slsToken(t, qc)
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+emt, map[string]any{"enabled": false})
	sentC := pub.Must(200, "POST", "/api/v1/public/quotations/"+tokC+":request-otp", nil).JSON()
	if sentC["channel"] != "email" || !strings.Contains(str(sentC["destinationMasked"]), "***@buyer.test") {
		t.Fatalf("e-mail code request: %v", sentC)
	}
	codeC := slsOtpCode(t, str(qc["number"]))
	var callLog string
	waitFor(t, 30*time.Second, "acceptance code e-mail sent", func() bool {
		sysQueryRow(t, inst, `SELECT coalesce(string_agg(request::text || coalesce(response::text, ''), ' '), '') FROM platform.integration_logs
			WHERE operation = 'send_email' AND request::text LIKE '%' || $1 || '%' AND (request::text LIKE '%Kode verifikasi%'
			OR request::text LIKE '%Verification code%')`, []any{qc["number"]}, &callLog)
		return callLog != ""
	})
	if strings.Contains(callLog, codeC) || !strings.Contains(callLog, "[REDACTED]") {
		t.Fatalf("the code is redacted in the integration call log: %s", callLog)
	}
	accC := pub.Must(200, "POST", "/api/v1/public/quotations/"+tokC+":accept", with(codeC)).JSON()
	if accC["status"] != "accepted" || accC["eMeterai"].(map[string]any)["status"] != "pending" {
		t.Fatalf("accepted with the e-Meterai pending: %v", accC)
	}
	if b := pdfOf(qcID); !bytes.Contains(b, []byte("e-Meterai: pending")) {
		t.Fatal("the PDF shows the pending e-Meterai")
	}
	otpProblem(t, gm.Do("POST", "/api/v1/crm/quotations/"+qcID+":stamp-e-meterai", nil), 409, "e_meterai_not_configured")
	sx.Must(403, "POST", "/api/v1/crm/quotations/"+qcID+":stamp-e-meterai", nil)
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+emt, map[string]any{"enabled": true, "settings": map[string]any{"alwaysFail": true}})
	f := gm.Must(200, "POST", "/api/v1/crm/quotations/"+qcID+":stamp-e-meterai", nil).JSON()["eMeterai"].(map[string]any)
	if f["status"] != "failed" || f["error"] == nil {
		t.Fatalf("provider failure: %v", f)
	}
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+emt, map[string]any{"settings": map[string]any{}})
	s := gm.Must(200, "POST", "/api/v1/crm/quotations/"+qcID+":stamp-e-meterai", nil).JSON()["eMeterai"].(map[string]any)
	if s["status"] != "stamped" || !strings.HasPrefix(str(s["serialNumber"]), "MOCK-EMT-") || s["error"] != nil {
		t.Fatalf("stamped by the retry: %v", s)
	}
	otpProblem(t, gm.Do("POST", "/api/v1/crm/quotations/"+qcID+":stamp-e-meterai", nil), 409, "e_meterai_not_pending")
	if auditCount(qcID, "e_meterai_pending") != 1 || auditCount(qcID, "e_meterai_failed") != 1 || auditCount(qcID, "e_meterai_stamped") != 1 {
		t.Fatal("e-Meterai audit trail: pending, failed, stamped")
	}

	// ── 4. No contact on file ──────────────────────────────────────────────
	custD := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "OTN" + sfx, "name": "No Contact " + sfx,
		"email": "gone" + sfx + "@buyer.test"}))
	qd := quote(custD, "Lunch "+sfx, "1000000")
	sysExec(t, inst, `UPDATE crm.customers SET email = NULL, phone = NULL WHERE id = $1`, custD)
	otpProblem(t, pub.Do("POST", "/api/v1/public/quotations/"+slsToken(t, qd)+":request-otp", nil), 409, "no_contact")
	pub.Must(404, "POST", "/api/v1/public/quotations/not-a-token:request-otp", nil)
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
