package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/accounting"
	"oneclub/internal/kernel/dbtx"
)

// P4 gap fixes, Finance UX (package F).

// fxDocs returns the documents of a list response with their type and id.
func fxHasDoc(docs []any, typ, id, number string) bool {
	for _, d := range docs {
		m := d.(map[string]any)
		if m["documentType"] == typ && str(m["documentId"]) == id && (number == "" || str(m["number"]) == number) {
			return true
		}
	}
	return false
}

// PRD P4 FR-FIN-04 (drill-down to journals and source documents), §7.1 /
// §7.3 (e-Faktur number and status on Back Office and Member invoices) and
// FR-TRS-03 (parallel-run comparison with the Excel Finance trial balance).
func TestP4FixFinanceDrillDownAndEFaktur(t *testing.T) {
	c := accSetupMDR(t)
	accEfaktur(t)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	day := accBusinessDay(t, c)
	m := inst.MDR

	// A corporate invoice with a PPN line (output tax invoice drafted, K2).
	corp := idOf(c.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "FXF" + sfx, "name": "PT Drill Down " + sfx,
		"email": "fx" + sfx + "@acc.test", "npwp": "02.222.333.4-555.000", "address": "Jl. Sudirman 2, Jakarta"}, "Idempotency-Key", newKey()))
	cf := str(c.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp}).JSON()["id"])
	f1 := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "PT Drill Down meeting", "sourceRef": "Meeting " + sfx}))
	accTaxedCharge(t, f1, "other", "Meeting package", 2_000_000, 220_000, nil)
	c.Must(200, "POST", "/api/v1/billing/customer-folios/"+cf+":merge", map[string]any{"folioIds": []string{f1}})
	iid := str(c.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": cf, "corporateAccountId": corp, "termsDays": 30},
		"Idempotency-Key", newKey()).JSON()["id"])
	c.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":issue", nil)
	accDispatch(t)
	invNo := str(c.Must(200, "GET", "/api/v1/billing/invoices/"+iid, nil).JSON()["number"])

	// FR-FIN-04: the journal of the invoice lists the invoice by its number.
	js := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+iid, nil).Items()
	if len(js) != 1 {
		t.Fatalf("invoice journal: %v", js)
	}
	docs := c.Must(200, "GET", accBase+"/journals/"+str(js[0]["id"])+"/documents", nil).JSON()["items"].([]any)
	if !fxHasDoc(docs, "invoice", iid, invNo) {
		t.Fatalf("journal documents: %v", docs)
	}
	c.Must(404, "GET", accBase+"/journals/"+newKey()+"/documents", nil)
	// … and every GL line of the AR control account of the day carries its document.
	ar := accAccount(t, "1142")
	q := "?accountId=" + ar + "&from=" + day + "&to=" + day + "&propertyId=" + m.String()
	gl := c.Must(200, "GET", accBase+"/general-ledger"+q, nil).JSON()["lines"].([]any)
	gld := c.Must(200, "GET", accBase+"/general-ledger/documents"+q, nil).Items()
	if len(gl) != len(gld) || len(gld) == 0 {
		t.Fatalf("GL lines %d vs documents %d", len(gl), len(gld))
	}
	found := false
	for _, l := range gld {
		found = found || fxHasDoc(l["documents"].([]any), "invoice", iid, invNo)
	}
	if !found {
		t.Fatalf("GL documents miss invoice %s: %v", invNo, gld)
	}
	c.Must(400, "GET", accBase+"/general-ledger/documents?from="+day, nil)

	// A credit note links to its invoice (related document).
	cn := c.Must(201, "POST", "/api/v1/billing/credit-notes", map[string]any{"invoiceId": iid, "amount": "111000", "reason": "Late start"},
		"Idempotency-Key", newKey()).JSON()
	accDispatch(t)
	cnj := c.Must(200, "GET", accBase+"/journals?filter[sourceId]="+str(cn["id"]), nil).Items()
	if len(cnj) != 1 {
		t.Fatalf("credit note journal: %v", cnj)
	}
	cnd := c.Must(200, "GET", accBase+"/journals/"+str(cnj[0]["id"])+"/documents", nil).JSON()["items"].([]any)
	if !fxHasDoc(cnd, "credit_note", str(cn["id"]), str(cn["number"])) {
		t.Fatalf("credit note documents: %v", cnd)
	}
	for _, d := range cnd {
		if x := d.(map[string]any); x["documentType"] == "credit_note" && (x["relatedType"] != "invoice" || str(x["relatedId"]) != iid || str(x["relatedNumber"]) != invNo) {
			t.Fatalf("credit note related invoice: %v", x)
		}
	}

	// A manual journal and its reversal resolve to the manual journal and the reversed journal.
	line := func(code, debit, credit string) map[string]any {
		return map[string]any{"accountId": accAccount(t, code), "debit": debit, "credit": credit}
	}
	mj := c.Must(201, "POST", accBase+"/manual-journals", map[string]any{"journalDate": day, "journalType": "manual", "description": "Drill-down " + sfx,
		"submit": true, "lines": []map[string]any{line("1122", "150000", ""), line("4890", "", "150000")}}).JSON()
	if mj["status"] != "posted" {
		t.Fatalf("manual journal: %v", mj)
	}
	mjd := c.Must(200, "GET", accBase+"/journals/"+str(mj["journalId"])+"/documents", nil).JSON()["items"].([]any)
	if !fxHasDoc(mjd, "manual_journal", str(mj["id"]), str(mj["number"])) {
		t.Fatalf("manual journal documents: %v", mjd)
	}
	rev := c.Must(200, "POST", accBase+"/journals/"+str(mj["journalId"])+":reverse", map[string]any{"reason": "drill-down test"}).JSON()
	orig := c.Must(200, "GET", accBase+"/journals/"+str(mj["journalId"]), nil).JSON()
	rvd := c.Must(200, "GET", accBase+"/journals/"+str(rev["id"])+"/documents", nil).JSON()["items"].([]any)
	if !fxHasDoc(rvd, "journal", str(mj["journalId"]), str(orig["number"])) {
		t.Fatalf("reversal documents: %v", rvd)
	}

	// Every document resolver runs (unknown ids stay "other"; event types map to their document).
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var refs [][2]string
		for _, typ := range accounting.SourceDocumentTypes() {
			refs = append(refs, [2]string{typ, newKey()})
		}
		refs = append(refs, [2]string{"billing.invoice_issued", iid}, [2]string{"some.unknown_event", "not-a-uuid"})
		out, err := accounting.ResolveSourceDocuments(ctx, tx, refs)
		if err != nil {
			return err
		}
		for i, d := range out[:len(out)-2] {
			if d.DocumentType != "other" || d.DocumentID != nil {
				return fmt.Errorf("%s with an unknown id resolved: %+v", refs[i][0], d)
			}
		}
		if ev := out[len(out)-2]; ev.DocumentType != "invoice" || ev.DocumentID == nil || ev.DocumentID.String() != iid {
			return fmt.Errorf("event type → document: %+v", ev)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// §7.1: the Back Office Invoices screen shows the e-Faktur status, then the number after the upload.
	ef := c.Must(200, "GET", accBase+"/invoice-efaktur?filter[invoiceId]="+iid, nil).Items()
	if len(ef) != 1 || ef[0]["status"] != "draft" || ef[0]["fakturNumber"] != nil || str(ef[0]["invoiceNumber"]) != invNo {
		t.Fatalf("invoice e-Faktur (draft): %v", ef)
	}
	up := c.Must(200, "POST", accBase+"/tax-invoices/"+str(ef[0]["taxInvoiceId"])+":upload", map[string]any{}).JSON()
	ef = c.Must(200, "GET", accBase+"/invoice-efaktur?filter[invoiceId]="+iid, nil).Items()
	if len(ef) != 1 || ef[0]["status"] != "uploaded" || str(ef[0]["fakturNumber"]) == "" || ef[0]["fakturNumber"] != up["fakturNumber"] {
		t.Fatalf("invoice e-Faktur (uploaded): %v / %v", ef, up)
	}
	if all := c.Must(200, "GET", accBase+"/invoice-efaktur", nil).Items(); len(all) == 0 {
		t.Fatal("invoice e-Faktur list")
	}
	c.Must(422, "GET", accBase+"/invoice-efaktur?filter[invoiceId]=x", nil)
	// A sales executive (billing.invoice.view at MAIN, no accounting permission) reads it on their property.
	sx := roleUser(t, inst, "sales_executive")
	sx.Property = inst.Main
	sx.Must(200, "GET", accBase+"/invoice-efaktur", nil)
	sx.Must(403, "GET", accBase+"/tax-invoices", nil)

	// §7.3: the member sees the e-Faktur of their own invoice in the Member App.
	pw := "Fx#Member2026" + sfx
	email := "fx.member" + sfx + "@acc.test"
	mu := c.Must(201, "POST", "/api/v1/platform/users", map[string]any{"email": email, "fullName": "Faktur Member " + sfx, "password": pw,
		"assignments": []map[string]any{{"roleId": roleID(t, c, "member"), "propertyId": m}}}).JSON()
	cust := customer(t, c, "FXM"+sfx, "Faktur Member "+sfx, map[string]any{"userId": str(mu["id"]), "email": email})
	mcf := str(c.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"customerId": cust}).JSON()["id"])
	mf := idOf(c.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Faktur Member " + sfx, "sourceRef": "Member " + sfx}))
	accTaxedCharge(t, mf, "other", "Member function", 1_000_000, 110_000, nil)
	c.Must(200, "POST", "/api/v1/billing/customer-folios/"+mcf+":merge", map[string]any{"folioIds": []string{mf}})
	miid := str(c.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": mcf, "billTo": map[string]any{"npwp": "09.876.543.2-100.000"}},
		"Idempotency-Key", newKey()).JSON()["id"])
	c.Must(200, "POST", "/api/v1/billing/invoices/"+miid+":issue", nil)
	accDispatch(t)
	mc := login(t, inst, email, pw)
	mc.Must(204, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": pw, "newPassword": pw + "x"})
	mine := mc.Must(200, "GET", "/api/v1/member/invoice-efaktur", nil).Items()
	if len(mine) != 1 || str(mine[0]["invoiceId"]) != miid || mine[0]["status"] != "draft" {
		t.Fatalf("member invoice e-Faktur: %v", mine)
	}
	if inv := mc.Must(200, "GET", "/api/v1/member/invoices", nil).Items(); len(inv) != 1 || str(inv[0]["id"]) != miid {
		t.Fatalf("member invoices: %v", inv)
	}
	sx.Must(403, "GET", "/api/v1/member/invoice-efaktur", nil)

	// FR-TRS-03: the Excel Finance trial balance of the month against OneClub's (mapped items, one difference, one unknown item).
	from := day[:8] + "01"
	tb := c.Must(200, "GET", accBase+"/trial-balance?from="+from+"&to="+day+"&propertyId="+m.String(), nil).JSON()
	c.Must(201, "POST", accBase+"/account-mappings", map[string]any{"sourceCode": "XLS-AR-" + sfx, "sourceName": "Piutang usaha", "accountId": ar})
	var csv bytes.Buffer
	csv.WriteString("account,debit,credit\n")
	var bumped string
	for _, r := range tb["rows"].([]any) {
		row := r.(map[string]any)
		code, closing := str(row["code"]), dec(row["closing"])
		if closing.IsZero() {
			continue
		}
		acct := code
		if code == "1142" {
			acct = "XLS-AR-" + sfx
		} else if bumped == "" {
			bumped, closing = code, closing.Add(decimal.NewFromInt(1000))
		}
		if closing.IsNegative() {
			fmt.Fprintf(&csv, "%s,0,\"%s\"\n", acct, closing.Neg().StringFixed(2))
		} else {
			fmt.Fprintf(&csv, "%s,\"%s\",0\n", acct, closing.StringFixed(2))
		}
	}
	csv.WriteString("XLS-UNKNOWN-" + sfx + ",5000,0\n")
	cmp := c.Must(200, "POST", accBase+"/reconciliations:excel-trial-balance", map[string]any{"from": from, "to": day, "propertyId": m,
		"content": csv.String()}).JSON()
	if cmp["matched"] != false || cmp["differences"].(float64) != 1 || len(cmp["unmapped"].([]any)) != 1 || !dec(cmp["totalDifference"]).Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("Excel TB comparison: %v", cmp)
	}
	for _, r := range cmp["rows"].([]any) {
		row := r.(map[string]any)
		switch str(row["code"]) {
		case "1142":
			if row["matched"] != true || fmt.Sprint(row["excelItems"]) != "[XLS-AR-"+sfx+"]" {
				t.Fatalf("mapped AR row: %v", row)
			}
		case bumped:
			if row["matched"] != false || !dec(row["difference"]).Equal(decimal.NewFromInt(-1000)) {
				t.Fatalf("bumped row: %v", row)
			}
		}
	}
	c.Must(422, "POST", accBase+"/reconciliations:excel-trial-balance", map[string]any{"to": day, "content": "foo,bar\n1,2\n"})
	c.Must(422, "POST", accBase+"/reconciliations:excel-trial-balance", map[string]any{"to": day, "content": "account,balance\n"})
	var compared int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE module = 'accounting' AND action = 'compare' AND entity_type = 'accounting.trial_balance'
		AND entity_id = $1`, []any{from + ".." + day}, &compared)
	if compared == 0 {
		t.Fatal("comparison not audited")
	}
}

// PRD P4 FR-FIN-05 / FR-RPT-P4-05: financial reports export to PDF.
func TestP4FixFinancePDFExport(t *testing.T) {
	c := accSetupMDR(t)
	day := accBusinessDay(t, c)
	ex := c.Must(202, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": "accounting.trial_balance", "format": "pdf",
		"params": map[string]any{"from": day[:8] + "01", "to": day}}, "Idempotency-Key", newKey()).JSON()
	if ex["format"] != "pdf" {
		t.Fatalf("export: %v", ex)
	}
	var url string
	waitFor(t, 60*time.Second, "PDF export completed", func() bool {
		for _, e := range c.Must(200, "GET", "/api/v1/reporting/exports", nil).Items() {
			if e["id"] == ex["id"] && e["status"] == "completed" {
				url = str(e["fileUrl"])
				return true
			}
			if e["id"] == ex["id"] && e["status"] == "failed" {
				t.Fatalf("PDF export failed: %v", e)
			}
		}
		return false
	})
	file := c.Must(200, "GET", url, nil)
	if !bytes.HasPrefix(file.Body, []byte("%PDF-")) || !bytes.Contains(file.Body, []byte("(Trial Balance)")) || !bytes.Contains(file.Body, []byte("%%EOF")) {
		t.Fatalf("PDF export file: %q", file.Body[:min(len(file.Body), 200)])
	}
	c.Must(422, "POST", "/api/v1/reporting/exports", map[string]any{"reportCode": "accounting.trial_balance", "format": "docx"}, "Idempotency-Key", newKey())
}

// PRD P4 FR-ACC-09 / §16 #18: the Auditor role is read-only on Accounting &
// Reports, assigned for the audit period only (expiry enforced at
// authentication, renewable) and every read is in the audit log.
func TestP4FixFinanceAuditor(t *testing.T) {
	sa := accSetupMDR(t)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	m := inst.MDR
	role := roleID(t, sa, "auditor")
	email := "auditor" + sfx + "@kap.test"
	pw := "Au#Dit2026" + sfx
	body := func(until any) map[string]any {
		a := map[string]any{"roleId": role, "propertyId": m}
		if until != nil {
			a["validUntil"] = until
		}
		return map[string]any{"email": email, "fullName": "KAP Auditor " + sfx, "password": pw, "assignments": []map[string]any{a}}
	}
	if e := sa.Must(422, "POST", "/api/v1/platform/users", body(nil)).JSON(); !strings.Contains(fmt.Sprint(e), "valid_until_required") {
		t.Fatalf("auditor without expiry: %v", e)
	}
	sa.Must(422, "POST", "/api/v1/platform/users", body(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)))
	until := time.Now().Add(7 * 24 * time.Hour).UTC().Truncate(time.Second)
	u := sa.Must(201, "POST", "/api/v1/platform/users", body(until.Format(time.RFC3339))).JSON()
	uid := str(u["id"])
	as := sa.Must(200, "GET", "/api/v1/platform/role-assignments?filter[userId]="+uid, nil).Items()
	if len(as) != 1 || as[0]["roleCode"] != "auditor" || as[0]["validUntil"] == nil {
		t.Fatalf("auditor assignment: %v", as)
	}
	if ud := sa.Must(200, "GET", "/api/v1/platform/users/"+uid, nil).JSON(); ud["assignments"].([]any)[0].(map[string]any)["validUntil"] == nil {
		t.Fatalf("user assignment expiry: %v", ud)
	}

	ac := login(t, inst, email, pw)
	ac.Must(204, "POST", "/api/v1/auth/password/change", map[string]any{"currentPassword": pw, "newPassword": pw + "x"})
	ac.Property = m
	day := accBusinessDay(t, sa)
	tbPath := accBase + "/trial-balance?propertyId=" + m.String() + "&to=" + day
	// read-only finance & reports …
	ac.Must(200, "GET", tbPath, nil)
	ac.Must(200, "GET", accBase+"/journals", nil)
	ac.Must(200, "GET", "/api/v1/reporting/reports/accounting.trial_balance", nil)
	ac.Must(200, "GET", "/api/v1/audit/logs", nil)
	ac.Must(403, "POST", accBase+"/manual-journals", map[string]any{"journalDate": day, "description": "not allowed", "lines": []any{}})
	ac.Must(403, "POST", accBase+"/postings:run", map[string]any{})
	ac.Must(403, "GET", "/api/v1/billing/invoices", nil)
	ac.Must(403, "GET", "/api/v1/procurement/purchase-orders", nil)
	// … every read logged with the route and its query.
	var reads int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE actor_id = $1 AND action = 'view' AND category = 'security'
		AND entity_id = 'GET /api/v1/accounting/trial-balance' AND metadata->>'query' LIKE '%propertyId=%'`, []any{mustUUID(uid)}, &reads)
	if reads != 1 {
		t.Fatalf("trial balance reads logged: %d", reads)
	}
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log WHERE actor_id = $1 AND action = 'view' AND module IN ('accounting', 'reporting', 'audit')`,
		[]any{mustUUID(uid)}, &reads)
	if reads < 4 {
		t.Fatalf("auditor reads logged: %d", reads)
	}
	// Other roles' reads are not logged.
	sa.Must(200, "GET", tbPath, nil)
	var others int
	sysQueryRow(t, inst, `SELECT count(*) FROM audit.audit_log a JOIN platform.users u ON u.id = a.actor_id WHERE u.email = 'role.super_admin@matrix.test'
		AND a.action = 'view' AND a.entity_id = 'GET /api/v1/accounting/trial-balance'`, nil, &others)
	if others != 0 {
		t.Fatalf("super admin reads logged: %d", others)
	}

	// The access period ends: the next request is refused without logging out.
	sysExec(t, inst, `UPDATE platform.role_assignments SET valid_until = now() - interval '1 second' WHERE user_id = $1`, mustUUID(uid))
	ac.Must(403, "GET", tbPath, nil)
	// A new audit period renews the expired assignment.
	next := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	sa.Must(422, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": uid, "roleId": role, "propertyId": m})
	rn := sa.Must(201, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": uid, "roleId": role, "propertyId": m, "validUntil": next}).JSON()
	if str(rn["id"]) != str(as[0]["id"]) || rn["validUntil"] == nil {
		t.Fatalf("renewed assignment: %v", rn)
	}
	ac.Must(200, "GET", tbPath, nil)
	sa.Must(409, "POST", "/api/v1/platform/role-assignments", map[string]any{"userId": uid, "roleId": role, "propertyId": m, "validUntil": next})
	// End of the audit engagement: the assignment is removed.
	sa.Must(204, "DELETE", "/api/v1/platform/role-assignments/"+str(rn["id"]), nil)
	ac.Must(403, "GET", tbPath, nil)
}
