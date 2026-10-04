package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/platform/integration"
)

// bilDec parses a decimal JSON value.
func bilDec(v any) decimal.Decimal {
	d, _ := decimal.NewFromString(str(v))
	return d
}

// bilLineID returns the id of the folio line with the given description.
func bilLineID(t *testing.T, folio map[string]any, desc string) string {
	t.Helper()
	for _, l := range folio["lines"].([]any) {
		if m := l.(map[string]any); m["description"] == desc {
			return str(m["id"])
		}
	}
	t.Fatalf("line %q not in folio %v", desc, folio["number"])
	return ""
}

// bilSettleOnline settles a pending gateway payment through the signed mock
// webhook and dispatches the outbox until cond holds.
func bilSettleOnline(t *testing.T, externalID any, cond func() bool) {
	t.Helper()
	pa := platformAdmin(t, inst)
	mp := integrationID(t, inst, "mock-payment")
	pa.Must(200, "PATCH", "/api/v1/platform/integrations/"+mp, map[string]any{"enabled": true, "settings": map[string]any{"autoPay": false}})
	secret := str(pa.Must(200, "POST", "/api/v1/platform/integrations/"+mp+":rotate-webhook-secret", nil).JSON()["webhookSecret"])
	body, _ := json.Marshal(map[string]any{"id": "evt_" + uuid.NewString(), "type": "payment.paid", "data": map[string]any{"externalId": externalID, "status": "paid"}})
	hook := anon(t, inst)
	hook.Property = uuid.Nil
	hook.Must(200, "POST", "/api/v1/webhooks/mock-payment", body, integration.SignatureHeader, integration.Sign(secret, body, time.Now()))
	waitFor(t, 15*time.Second, "online payment settled", func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// EP-17 (FR-BIL-P3-01..10) acceptance: a corporate event across lines is
// billed on one customer folio and one invoice to the company; split bill,
// credit limit with an approved override, invoice issue / send / PDF /
// public payment link, partial payment, credit note, write-off approval,
// allocation, ageing equal to the AR Aging Report, statements, void and the
// K2 events.
func TestP3BillingCorporateInvoices(t *testing.T) {
	sa := superAdmin(t, inst)
	fin := login(t, inst, "finance@demo.oneclub.id", demoPassword)
	loc := clubLoc(inst)
	today := time.Now().In(loc).Format("2006-01-02")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "BIL" + sfx, "name": "PT Bilangan " + sfx,
		"email": "ar" + sfx + "@bil.test", "npwp": "01.111.222.3-444.000", "address": "Jl. Sudirman 1, Jakarta"}))
	person := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BILP" + sfx, "name": "Dewi Nominee", "email": "dewi" + sfx + "@bil.test"}))

	// Customer folio of the company (idempotent) with folios of two lines merged.
	cf := sa.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp}).JSON()
	cfID := str(cf["id"])
	if again := idOf(sa.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"corporateAccountId": corp})); again != cfID {
		t.Fatalf("one customer folio per company: %s vs %s", again, cfID)
	}
	f1 := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "PT Bilangan meeting", "sourceRef": "Meeting room"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+f1+"/lines", map[string]any{"chargeType": "other", "description": "Meeting room full day", "unitPrice": "5000000"})
	f2 := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "PT Bilangan golf", "sourceRef": "Corporate golf"}))
	f2d := sa.Must(201, "POST", "/api/v1/billing/folios/"+f2+"/lines", map[string]any{"chargeType": "other", "description": "Green fee x4", "unitPrice": "1500000",
		"quantity": "4"}).JSON()
	golfLine := bilLineID(t, f2d, "Green fee x4")
	merged := sa.Must(200, "POST", "/api/v1/billing/customer-folios/"+cfID+":merge", map[string]any{"folioIds": []string{f1, f2}}).JSON()
	if n := len(merged["folioList"].([]any)); n != 2 {
		t.Fatalf("merged folios: %v", merged["folioList"])
	}
	sa.Must(409, "POST", "/api/v1/billing/customer-folios/"+idOf(sa.Must(200, "POST", "/api/v1/billing/customer-folios",
		map[string]any{"customerId": person}))+":merge", map[string]any{"folioIds": []string{f1}})
	list := sa.Must(200, "GET", "/api/v1/billing/customer-folios?filter[corporateAccountId]="+corp, nil).Items()
	if len(list) != 1 || str(list[0]["id"]) != cfID {
		t.Fatalf("customer folio list: %v", list)
	}

	// Cross-line split bill: half of the green fees is paid by the nominee.
	split := sa.Must(200, "POST", "/api/v1/billing/customer-folios:split", map[string]any{"lineIds": []string{golfLine}, "percent": "50",
		"customerId": person, "reason": "Personal guests"}).JSON()
	if split["targetFolio"] == nil {
		t.Fatalf("split: %v", split)
	}
	det := sa.Must(200, "GET", "/api/v1/billing/customer-folios/"+cfID, nil).JSON()
	charges := bilDec(det["charges"])
	if !charges.Equal(decimal.NewFromInt(8_000_000)) {
		t.Fatalf("company charges after split = %s, want 8,000,000 (5,000,000 + 3,000,000): %v", charges, det)
	}

	// One invoice to the company from the customer folio; issue, send, PDF.
	inv := sa.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": cfID, "corporateAccountId": corp, "termsDays": 30,
		"notes": "Corporate outing"}, "Idempotency-Key", newKey()).JSON()
	iid := str(inv["id"])
	if inv["status"] != "draft" || !bilDec(inv["total"]).Equal(charges) {
		t.Fatalf("draft invoice: %v", inv)
	}
	issued := fin.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":issue", nil).JSON()
	if issued["status"] != "issued" || str(issued["number"]) == "" || str(issued["dueDate"]) == today {
		t.Fatalf("issued invoice: %v", issued)
	}
	fin.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":send", map[string]any{"email": "finance" + sfx + "@bil.test"})
	if r := fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid+"/pdf", nil); len(r.Body) < 100 || string(r.Body[:4]) != "%PDF" {
		t.Fatalf("invoice pdf: %d bytes", len(r.Body))
	}
	if items := fin.Must(200, "GET", "/api/v1/billing/invoices?filter[corporateAccountId]="+corp, nil).Items(); len(items) != 1 {
		t.Fatalf("invoices of the company: %v", items)
	}

	// Partial bank transfer, credit note and a write-off through approval.
	fin.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":pay", map[string]any{"methodType": "bank_transfer", "amount": "3000000", "reference": "TRF-" + sfx})
	if st := fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid, nil).JSON(); st["status"] != "partially_paid" || !bilDec(st["outstanding"]).Equal(decimal.NewFromInt(5_000_000)) {
		t.Fatalf("after partial payment: %v", st)
	}
	fin.Must(201, "POST", "/api/v1/billing/credit-notes", map[string]any{"invoiceId": iid, "amount": "500000", "reason": "Room set-up late"}, "Idempotency-Key", newKey())
	if cns := fin.Must(200, "GET", "/api/v1/billing/credit-notes?filter[invoiceId]="+iid, nil).Items(); len(cns) != 1 {
		t.Fatalf("credit notes: %v", cns)
	}
	wo := fin.Must(200, "POST", "/api/v1/billing/invoices/"+iid+":write-off", map[string]any{"amount": "100000", "reason": "Bank charges absorbed"}).JSON()
	if wo["status"] != "approved" {
		t.Fatalf("write-off without a workflow is approved at once: %v", wo)
	}

	// Ageing as of 45 days later: everything still open is 31–60 days and
	// overdue; the AR Aging Report shows the same balance.
	asOf := time.Now().In(loc).AddDate(0, 0, 45).Format("2006-01-02")
	ag := fin.Must(200, "GET", "/api/v1/billing/aging?asOf="+asOf, nil).JSON()
	var mine map[string]any
	for _, r := range ag["rows"].([]any) {
		if m := r.(map[string]any); str(m["corporateAccountId"]) == corp {
			mine = m
		}
	}
	if mine == nil || !bilDec(mine["days31to60"]).Equal(decimal.NewFromInt(4_400_000)) || !bilDec(mine["overdue"]).Equal(decimal.NewFromInt(4_400_000)) {
		t.Fatalf("ageing of the company: %v", mine)
	}
	rep := fin.Must(200, "GET", "/api/v1/reporting/reports/billing.ar_aging?params[from]="+today+"&params[to]="+asOf, nil).JSON()
	repTotal := decimal.Zero
	for _, r := range rep["rows"].([]any) {
		repTotal = repTotal.Add(bilDec(r.(map[string]any)["total"]))
	}
	if !repTotal.Equal(bilDec(ag["totals"].(map[string]any)["total"])) {
		t.Fatalf("AR Aging Report %s ≠ open invoices %s", repTotal, ag["totals"])
	}

	// Corporate statement and its e-mail.
	st := fin.Must(200, "GET", "/api/v1/billing/corporate-accounts/"+corp+"/statement", nil).JSON()
	if len(st["openInvoices"].([]any)) != 1 {
		t.Fatalf("statement open invoices: %v", st["openInvoices"])
	}
	fin.Must(200, "POST", "/api/v1/billing/corporate-accounts/"+corp+"/statement:send", map[string]any{})

	// The rest is paid online through the public payment link (FR-INT-P3-04).
	var token string
	sysQueryRow(t, inst, `SELECT public_token FROM billing.invoices WHERE id = $1`, []any{mustUUID(iid)}, &token)
	pub := anon(t, inst)
	if pi := pub.Must(200, "GET", "/api/v1/public/invoices/"+token, nil).JSON(); pi["number"] != issued["number"] {
		t.Fatalf("public invoice: %v", pi)
	}
	op := pub.Must(201, "POST", "/api/v1/public/invoices/"+token+":pay", map[string]any{"method": "qris"}).JSON()
	if op["status"] != "pending" {
		t.Fatalf("payment link checkout: %v", op)
	}
	bilSettleOnline(t, op["externalId"], func() bool {
		return fin.Must(200, "GET", "/api/v1/billing/invoices/"+iid, nil).JSON()["status"] == "paid"
	})
	if al := fin.Must(200, "GET", "/api/v1/billing/payment-allocations?filter[invoiceId]="+iid, nil).Items(); len(al) != 2 {
		t.Fatalf("allocations of the invoice: %v", al)
	}

	// City ledger: a folio charged to the company beyond its credit limit
	// needs an approved credit override (FR-BIL-P3-05 AC).
	f3 := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "PT Bilangan dinner", "sourceRef": "Gala dinner"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+f3+"/lines", map[string]any{"chargeType": "other", "description": "Gala dinner 100 pax", "unitPrice": "60000000"})
	if r := sa.Do("POST", "/api/v1/billing/folios/"+f3+":charge-to-account", map[string]any{"corporateAccountId": corp}); r.Status < 400 {
		t.Fatalf("a charge above the default corporate limit is refused: %s", r)
	}
	var acct string
	sysQueryRow(t, inst, `SELECT id::text FROM billing.customer_accounts WHERE corporate_account_id = $1`, []any{mustUUID(corp)}, &acct)
	ov := fin.Must(201, "POST", "/api/v1/billing/customer-accounts/"+acct+":credit-override", map[string]any{"amount": "20000000",
		"reason": "Annual gala, CFO approval"}).JSON()
	if ov["status"] != "approved" {
		t.Fatalf("credit override: %v", ov)
	}
	if ovs := fin.Must(200, "GET", "/api/v1/billing/credit-overrides?filter[accountId]="+acct, nil).Items(); len(ovs) != 1 {
		t.Fatalf("credit overrides: %v", ovs)
	}
	sa.Must(200, "POST", "/api/v1/billing/folios/"+f3+":charge-to-account", map[string]any{"corporateAccountId": corp})
	// Monthly account invoice of the city ledger charges, paid by a bank
	// transfer allocated later (FR-BIL-P3-06).
	ainv := fin.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"corporateAccountId": corp, "from": today, "to": today, "issue": true},
		"Idempotency-Key", newKey()).JSON()
	if ainv["status"] != "issued" || !bilDec(ainv["total"]).Equal(decimal.NewFromInt(60_000_000)) {
		t.Fatalf("account invoice: %v", ainv)
	}
	pay := fin.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"accountId": acct, "methodType": "bank_transfer", "channel": "venue",
		"amount": "25000000", "reference": "TRF-A-" + sfx}).JSON()
	alloc := fin.Must(200, "POST", "/api/v1/billing/payment-allocations", map[string]any{"paymentId": pay["id"],
		"allocations": []map[string]any{{"invoiceId": ainv["id"], "amount": "25000000"}}}).Items()
	if len(alloc) != 1 {
		t.Fatalf("allocation: %v", alloc)
	}
	if a := fin.Must(200, "GET", "/api/v1/billing/invoices/"+str(ainv["id"]), nil).JSON(); a["status"] != "partially_paid" {
		t.Fatalf("account invoice after allocation: %v", a)
	}

	// A draft invoice of the nominee's split folio is voided.
	pf := sa.Must(200, "POST", "/api/v1/billing/customer-folios", map[string]any{"customerId": person}).JSON()
	dinv := fin.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"customerFolioId": pf["id"]}, "Idempotency-Key", newKey()).JSON()
	if v := fin.Must(200, "POST", "/api/v1/billing/invoices/"+str(dinv["id"])+":void", map[string]any{"reason": "Paid at the venue instead"}).JSON(); v["status"] != "void" {
		t.Fatalf("void: %v", v)
	}

	// K2 events for AR P4.
	var issuedEvents, paidEvents int
	sysQueryRow(t, inst, `SELECT count(*) FILTER (WHERE event_type = 'billing.invoice_issued'), count(*) FILTER (WHERE event_type = 'billing.invoice_paid')
		FROM platform.outbox WHERE aggregate_id = $1`, []any{mustUUID(iid)}, &issuedEvents, &paidEvents)
	if issuedEvents != 1 || paidEvents != 1 {
		t.Fatalf("invoice events issued=%d paid=%d", issuedEvents, paidEvents)
	}
	// The member sees and pays his own invoice in the Member App (FR-APP-P3-07).
	bilMemberInvoice(t, sa, fin)
	// Reports of billing run (FR-RPT-P3-05).
	from := time.Now().In(loc).AddDate(0, 0, -30).Format("2006-01-02")
	for _, code := range []string{"billing.invoice", "billing.payment_schedule", "billing.daily_revenue", "billing.night_audit", "billing.cashier_shift"} {
		if r := fin.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+from+"&params[to]="+asOf, nil).JSON(); r["rows"] == nil {
			t.Fatalf("%s: %v", code, r)
		}
	}
	if exp := accountingExport(t, sa); !contains(exp, "receivable,ar_outstanding,") || !contains(exp, "business_day,") {
		t.Fatalf("accounting export P3 sections missing:\n%s", exp)
	}
}

func bilMemberInvoice(t *testing.T, sa, fin *Client) {
	mc := roleUser(t, inst, "member")
	uid := userID(t, "member")
	var cust string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT id::text FROM crm.customers WHERE user_id = $1 AND property_id = $2 LIMIT 1), '')`,
		[]any{mustUUID(uid), inst.Main}, &cust)
	if cust == "" {
		cust = customer(t, sa, fmt.Sprintf("BILM%d", time.Now().UnixNano()%1e6), "Bil Member", map[string]any{"userId": uid})
	}
	f := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Family dinner", "customerId": cust}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+f+"/lines", map[string]any{"chargeType": "other", "description": "Family dinner", "unitPrice": "750000"})
	inv := fin.Must(201, "POST", "/api/v1/billing/invoices", map[string]any{"folioId": f, "issue": true}, "Idempotency-Key", newKey()).JSON()
	found := false
	for _, i := range mc.Must(200, "GET", "/api/v1/member/invoices", nil).Items() {
		found = found || i["id"] == inv["id"]
	}
	if !found {
		t.Fatal("my invoices")
	}
	mc.Must(200, "GET", "/api/v1/member/invoices/"+str(inv["id"]), nil)
	op := mc.Must(201, "POST", "/api/v1/member/invoices/"+str(inv["id"])+":pay-online", map[string]any{"method": "qris"}).JSON()
	bilSettleOnline(t, op["externalId"], func() bool {
		return fin.Must(200, "GET", "/api/v1/billing/invoices/"+str(inv["id"]), nil).JSON()["status"] == "paid"
	})
	mc.Must(200, "GET", "/api/v1/member/payment-schedules", nil)
}

// FR-BIL-P3-03/07: payment schedules with a down payment, terms and an
// installment plan; schedule invoices with payment links; cancellation.
func TestP3BillingPaymentSchedules(t *testing.T) {
	sa := superAdmin(t, inst)
	loc := clubLoc(inst)
	day := func(n int) string { return time.Now().In(loc).AddDate(0, 0, n).Format("2006-01-02") }
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "BILS" + sfx, "name": "Andi & Sari", "email": "wedding" + sfx + "@bil.test"}))
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "Wedding Andi & Sari", "customerId": cust}))
	sch := sa.Must(201, "POST", "/api/v1/billing/payment-schedules", map[string]any{"title": "Wedding Andi & Sari", "folioId": folio, "customerId": cust,
		"sourceType": "other", "totalAmount": "100000000", "lines": []map[string]any{
			{"label": "DP 30%", "kind": "down_payment", "percent": "30", "dueDate": day(0)},
			{"label": "Termin 2", "kind": "installment", "percent": "40", "dueDate": day(30)},
			{"label": "Pelunasan", "kind": "final", "percent": "30", "dueDate": day(60)}}}, "Idempotency-Key", newKey()).JSON()
	lines := sch["lines"].([]any)
	if len(lines) != 3 || !bilDec(lines[0].(map[string]any)["amount"]).Equal(decimal.NewFromInt(30_000_000)) {
		t.Fatalf("schedule: %v", sch)
	}
	sid := str(sch["id"])
	l1, l2 := str(lines[0].(map[string]any)["id"]), str(lines[1].(map[string]any)["id"])
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+sid+"/lines/"+l1+":pay", map[string]any{"methodType": "bank_transfer", "reference": "DP-" + sfx},
		"Idempotency-Key", newKey())
	inv := sa.Must(200, "POST", "/api/v1/billing/payment-schedules/"+sid+"/lines/"+l2+":invoice", nil).JSON()
	if inv["status"] != "issued" || !bilDec(inv["total"]).Equal(decimal.NewFromInt(40_000_000)) {
		t.Fatalf("schedule invoice: %v", inv)
	}
	sa.Must(200, "POST", "/api/v1/billing/invoices/"+str(inv["id"])+":pay", map[string]any{"methodType": "cash"})
	got := sa.Must(200, "GET", "/api/v1/billing/payment-schedules/"+sid, nil).JSON()
	gl := got["lines"].([]any)
	if gl[0].(map[string]any)["status"] != "paid" || gl[1].(map[string]any)["status"] != "paid" || gl[2].(map[string]any)["status"] != "pending" {
		t.Fatalf("schedule after payments: %v", gl)
	}
	if items := sa.Must(200, "GET", "/api/v1/billing/payment-schedules?filter[customerId]="+cust, nil).Items(); len(items) != 1 {
		t.Fatalf("schedules of the customer: %v", items)
	}
	// Installment plan: DP below the Payment Configuration minimum is refused.
	sa.Must(422, "POST", "/api/v1/billing/payment-schedules", map[string]any{"title": "Membership fee", "customerId": cust, "folioId": folio,
		"sourceType": "membership", "totalAmount": "60000000", "installments": map[string]any{"downPaymentPercent": "10", "installments": 6}},
		"Idempotency-Key", newKey())
	ip := sa.Must(201, "POST", "/api/v1/billing/payment-schedules", map[string]any{"title": "Membership fee", "customerId": cust, "folioId": folio,
		"sourceType": "membership", "totalAmount": "60000000", "installments": map[string]any{"downPaymentPercent": "30", "installments": 6}},
		"Idempotency-Key", newKey()).JSON()
	if n := len(ip["lines"].([]any)); n != 7 {
		t.Fatalf("installment plan lines: %d", n)
	}
	if c := sa.Must(200, "POST", "/api/v1/billing/payment-schedules/"+str(ip["id"])+":cancel", map[string]any{"reason": "Paid in full instead"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancelled schedule: %v", c)
	}
}

// EP-18 acceptance on the second property: the night audit cannot close
// while a cashier shift is open; after the close the Daily Revenue Report
// equals the day's charges and the payments equal the shifts; later
// transactions belong to the next business day; reopening needs approval.
func TestP3BillingCashierShiftsNightAudit(t *testing.T) {
	base := superAdmin(t, inst)
	sa := *base
	sa.Property = inst.MDR
	mdr := &sa

	days := mdr.Must(200, "GET", "/api/v1/billing/business-days", nil).Items()
	if len(days) == 0 || days[0]["current"] != true {
		t.Fatalf("business days: %v", days)
	}
	day := str(days[0]["businessDate"])

	sh := mdr.Must(201, "POST", "/api/v1/billing/cashier-shifts", map[string]any{"station": "front_desk", "openingFloat": "500000"}).JSON()
	sid := str(sh["id"])
	mdr.Must(409, "POST", "/api/v1/billing/cashier-shifts", map[string]any{"station": "front_desk"})
	if cur := mdr.Must(200, "GET", "/api/v1/billing/cashier-shifts/current", nil).JSON(); cur["id"] != sh["id"] {
		t.Fatalf("current shift: %v", cur)
	}
	folio := idOf(mdr.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": "MDR walk-in"}))
	mdr.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/lines", map[string]any{"chargeType": "other", "description": "Court rental", "unitPrice": "300000"})
	mdr.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "amount": "300000", "methodType": "cash", "channel": "venue"})
	mdr.Must(201, "POST", "/api/v1/billing/cashier-shifts/"+sid+"/cash-movements", map[string]any{"kind": "cash_out", "amount": "50000", "reason": "Petty cash"})
	got := mdr.Must(200, "GET", "/api/v1/billing/cashier-shifts/"+sid, nil).JSON()
	if !bilDec(got["expected"]).Equal(decimal.NewFromInt(750_000)) {
		t.Fatalf("expected cash = 500,000 + 300,000 − 50,000: %v", got)
	}

	// Blocked while the shift is open.
	run := mdr.Must(200, "POST", "/api/v1/billing/business-days:night-audit", nil).JSON()
	if run["status"] != "blocked" || run["businessDate"] != day {
		t.Fatalf("night audit with an open shift: %v", run)
	}
	mdr.Must(422, "POST", "/api/v1/billing/cashier-shifts/"+sid+":close", map[string]any{"countedCash": "740000"})
	closed := mdr.Must(200, "POST", "/api/v1/billing/cashier-shifts/"+sid+":close", map[string]any{"countedCash": "740000", "note": "Rp10.000 short"}).JSON()
	if closed["status"] != "closed" || !bilDec(closed["variance"]).Equal(decimal.NewFromInt(-10_000)) {
		t.Fatalf("closed shift: %v", closed)
	}
	if l := mdr.Must(200, "GET", "/api/v1/billing/cashier-shifts?filter[status]=closed", nil).Items(); len(l) == 0 {
		t.Fatal("closed shifts list")
	}

	run = mdr.Must(200, "POST", "/api/v1/billing/business-days:night-audit", nil).JSON()
	if run["status"] != "completed" {
		t.Fatalf("night audit: %v", run)
	}
	dr := mdr.Must(200, "GET", "/api/v1/billing/daily-revenue?date="+day, nil).JSON()
	if dr["status"] != "closed" || dr["frozen"] != true || !bilDec(dr["charges"]).Equal(decimal.NewFromInt(300_000)) ||
		!bilDec(dr["paymentTotal"]).Equal(bilDec(dr["shiftTotal"])) {
		t.Fatalf("daily revenue of the closed day: %v", dr)
	}
	// A charge after the close belongs to the next business day.
	mdr.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/lines", map[string]any{"chargeType": "other", "description": "Late drink", "unitPrice": "25000"})
	next := mdr.Must(200, "GET", "/api/v1/billing/daily-revenue", nil).JSON()
	if next["businessDate"] == day || !bilDec(next["charges"]).Equal(decimal.NewFromInt(25_000)) {
		t.Fatalf("next business day: %v", next)
	}
	if runs := mdr.Must(200, "GET", "/api/v1/billing/night-audit-runs", nil).Items(); len(runs) < 2 {
		t.Fatalf("night audit runs: %v", runs)
	}
	// Reopen (no workflow configured: approved at once) and close again.
	re := mdr.Must(200, "POST", "/api/v1/billing/business-days/"+day+":reopen", map[string]any{"reason": "Missing banquet charge"}).JSON()
	if re["status"] != "open" {
		t.Fatalf("reopened day: %v", re)
	}
	if again := mdr.Must(200, "POST", "/api/v1/billing/business-days:night-audit", nil).JSON(); again["status"] != "completed" || again["businessDate"] != day {
		t.Fatalf("night audit after reopen: %v", again)
	}
	var events int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = 'billing.business_day_closed' AND property_id = $1`, []any{inst.MDR}, &events)
	if events < 2 {
		t.Fatalf("billing.business_day_closed events: %d", events)
	}
}
