package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// EP-19 acceptance: voucher 5x entry weekday Rp635,000 recognises 127,000 per
// redemption; an expiring voucher with 2 left books breakage 254,000; retries
// redeem only once; liability always equals the deferred sub-ledger (G6).
func TestP2VoucherPrepaid(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	vt := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "ENTRY5-WD", "name": "Voucher Masuk 5x Weekday",
		"kind": "quota", "category": "sport_entry", "unit": "entry", "faceValue": "5", "price": "635000", "validityMonths": 6,
		"applicableServices": []string{"facility_entry"}, "revenueComponent": "sport_entry", "transferable": true}))
	sale := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "customerId": f.CustomerB,
		"payment": map[string]any{"methodType": "cash"}}, "Idempotency-Key", newKey()).JSON()
	v := sale["vouchers"].([]any)[0].(map[string]any)
	code := str(v["code"])
	if len(code) != 17 || str(v["unitValue"]) != "127000" || v["status"] != "active" {
		t.Fatalf("voucher: %v", v)
	}
	if st := sale["folio"].(map[string]any)["folio"].(map[string]any)["status"]; st != "closed" {
		t.Fatalf("paid sale should close the folio, got %v", st)
	}
	key := newKey()
	red := sa.Must(200, "POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "serviceType": "facility_entry", "terminal": "SPORT-1"},
		"Idempotency-Key", key).JSON()
	if red["recognizedRevenue"] != "127000" || red["voucher"].(map[string]any)["remainingQuantity"] != "4" {
		t.Fatalf("redeem: %v", red)
	}
	// retry with the same key (offline replay) only counts once
	again := sa.Must(200, "POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "serviceType": "facility_entry"}, "Idempotency-Key", key).JSON()
	if again["duplicate"] != true || again["voucher"].(map[string]any)["remainingQuantity"] != "4" {
		t.Fatalf("idempotent retry: %v", again)
	}
	// not applicable to other services
	if r := sa.Do("POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "serviceType": "sport_court"}); r.Status != 409 {
		t.Fatalf("wrong service: %s", r)
	}
	// two receptions at the same time for the last places: never more than remaining
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := sa.Do("POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "serviceType": "facility_entry"}, "Idempotency-Key", newKey())
			if r.Status == 200 {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 4 {
		t.Fatalf("concurrent redemptions accepted %d, want 4", okCount)
	}
	bal := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+code+"/balance", nil).JSON()
	if bal["remaining"] != "0" || bal["status"] != "redeemed" {
		t.Fatalf("balance: %v", bal)
	}
	vid := str(v["id"])
	ledger := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+vid+"/ledger", nil).Items()
	if len(ledger) != 6 { // sale + 5 redemptions
		t.Fatalf("ledger: %d entries", len(ledger))
	}
	var liab string
	sysQueryRow(t, inst, `SELECT coalesce(sum(amount),0)::text FROM billing.deferred_revenue_entries WHERE ref_id = $1`, []any{mustUUID(vid)}, &liab)
	if liab != "0.0000" {
		t.Fatalf("fully used voucher must leave no liability, got %s", liab)
	}

	// Breakage: a voucher with 2 left that expires books 254,000.
	sale2 := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": vt, "customerId": f.CustomerA,
		"payment": map[string]any{"methodType": "card", "reference": "EDC-1"}}, "Idempotency-Key", newKey()).JSON()
	v2 := sale2["vouchers"].([]any)[0].(map[string]any)
	for i := 0; i < 3; i++ {
		sa.Must(200, "POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": v2["code"], "serviceType": "facility_entry"}, "Idempotency-Key", newKey())
	}
	sysExec(t, inst, `UPDATE commercial.vouchers SET expires_at = now() - interval '1 second' WHERE id = $1`, mustUUID(str(v2["id"])))
	if n, err := inst.App.Vouchers.RunVoucherExpiry(context.Background()); err != nil || n < 1 {
		t.Fatalf("expiry: %d %v", n, err)
	}
	var breakage string
	sysQueryRow(t, inst, `SELECT amount::text FROM billing.deferred_revenue_entries WHERE ref_id = $1 AND entry_type = 'breakage'`, []any{mustUUID(str(v2["id"]))}, &breakage)
	if breakage != "-254000.0000" {
		t.Fatalf("breakage = %s, want -254000", breakage)
	}
	if st := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+str(v2["id"]), nil).JSON()["status"]; st != "expired" {
		t.Fatalf("status %v", st)
	}
	// G6: voucher liability = sum over vouchers of remaining × unit value.
	ds := sa.Must(200, "GET", "/api/v1/billing/deferred-revenue", nil).JSON()
	var expect string
	sysQueryRow(t, inst, `SELECT trim_scale(coalesce(sum(round(remaining_quantity * unit_value)),0))::text FROM commercial.vouchers WHERE property_id = $1
		AND status IN ('active','partially_redeemed')`, []any{inst.Main}, &expect)
	var voucherLiab float64
	for _, row := range ds["byType"].([]any) {
		rm := row.(map[string]any)
		if rm["liabilityType"] == "voucher" || rm["liabilityType"] == "prepaid" {
			var v float64
			_, _ = fmt.Sscan(str(rm["amount"]), &v)
			voucherLiab += v
		}
	}
	var expF float64
	_, _ = fmt.Sscan(expect, &expF)
	if voucherLiab != expF {
		t.Fatalf("voucher liability %v vs vouchers %s (%v)", voucherLiab, expect, ds["byType"])
	}

	// Value voucher as tender (C3) and Prepaid Balance of balls.
	gift := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "GIFT500", "name": "Gift Voucher 500K",
		"kind": "value", "category": "gift", "unit": "rupiah", "faceValue": "500000", "price": "500000", "validityMonths": 12}))
	gv := sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": gift, "guestName": "Buyer",
		"payment": map[string]any{"methodType": "bank_transfer", "reference": "TRF-77"}}).JSON()["vouchers"].([]any)[0].(map[string]any)
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": "pos", "customerId": f.CustomerB}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/charges", map[string]any{"businessLine": "pos", "revenueComponent": "fnb",
		"description": "Nasi Goreng", "amount": "120000"}, "Idempotency-Key", newKey())
	pay := sa.Must(201, "POST", "/api/v1/billing/folios/"+folio+"/payments", map[string]any{"methodType": "voucher_prepaid", "amount": "120000",
		"tender": map[string]any{"code": gv["code"]}}, "Idempotency-Key", newKey()).JSON()
	if pay["amount"] != "120000" || pay["tenderRef"].(map[string]any)["remaining"] != "380000" {
		t.Fatalf("voucher tender: %v", pay)
	}
	sa.Must(200, "POST", "/api/v1/billing/folios/"+folio+":close", map[string]any{})

	balls := idOf(sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "BALL5000", "name": "Paket 5.000 Bola",
		"kind": "quota", "category": "driving_range_balls", "unit": "ball", "faceValue": "5000", "price": "4600000", "validityMonths": 6, "prepaid": true}))
	sa.Must(201, "POST", "/api/v1/commercial/vouchers:sell", map[string]any{"voucherTypeId": balls, "customerId": f.CustomerA,
		"payment": map[string]any{"methodType": "member_account"}}, "Idempotency-Key", newKey())
	use := sa.Must(200, "POST", "/api/v1/commercial/prepaid-balances:redeem?category=driving_range_balls", map[string]any{"customerId": f.CustomerA,
		"quantity": "50", "serviceType": "driving_range"}, "Idempotency-Key", newKey()).Items()
	if len(use) != 1 || use[0]["recognizedRevenue"] != "46000" {
		t.Fatalf("ball redemption: %v", use)
	}
	pb := sa.Must(200, "GET", "/api/v1/commercial/prepaid-balances?filter[customerId]="+f.CustomerA, nil).Items()
	if len(pb) != 1 || pb[0]["remaining"] != "4950" {
		t.Fatalf("prepaid balance: %v", pb)
	}

	// Issue complimentary (approval auto-approves without workflow), transfer, extend, adjust, void.
	iss := sa.Must(201, "POST", "/api/v1/commercial/vouchers:issue", map[string]any{"voucherTypeId": vt, "customerId": f.CustomerA, "reason": "Member complaint"}).JSON()
	if iss["status"] != "active" || iss["pricePaid"] != "0" {
		t.Fatalf("issue: %v", iss)
	}
	sa.Must(200, "POST", "/api/v1/commercial/vouchers/"+str(iss["id"])+":transfer", map[string]any{"customerId": f.CustomerB, "reason": "gift"})
	ext := sa.Must(202, "POST", "/api/v1/commercial/vouchers/"+str(iss["id"])+":extend", map[string]any{"expiresAt": rfc(time.Now().AddDate(1, 0, 0)), "reason": "service recovery"}).JSON()
	if ext["status"] != "approved" {
		t.Fatalf("extend: %v", ext)
	}
	sa.Must(202, "POST", "/api/v1/commercial/vouchers/"+str(iss["id"])+":adjust", map[string]any{"quantity": "-1", "reason": "manual correction"})
	if rem := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+str(iss["id"]), nil).JSON()["remainingQuantity"]; rem != "4" {
		t.Fatalf("adjusted remaining %v", rem)
	}
	sa.Must(200, "POST", "/api/v1/commercial/vouchers/"+str(iss["id"])+":void", map[string]any{"reason": "issued by mistake"})
	if r := sa.Do("POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"voucherId": iss["id"]}); r.Status != 409 {
		t.Fatalf("void voucher must not redeem: %s", r)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/commercial/vouchers?filter[customerId]="+f.CustomerB, nil).Items()); n < 2 {
		t.Fatalf("voucher list %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/commercial/voucher-ledger?filter[entryType]=redemption", nil).Items()); n < 8 {
		t.Fatalf("redemption history %d", n)
	}
	// Public code check is rate limited and shows no customer data.
	pub := anon(t, inst)
	chk := pub.Must(200, "POST", "/api/v1/public/vouchers:check", map[string]any{"code": gv["code"], "propertyId": inst.Main}).JSON()
	if chk["remaining"] != "380000" || chk["customerName"] != nil {
		t.Fatalf("public check: %v", chk)
	}
	limited := false
	for i := 0; i < 12; i++ {
		if pub.Do("POST", "/api/v1/public/vouchers:check", map[string]any{"code": "WRONG-CODE", "propertyId": inst.Main}).Status == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("public voucher check must be rate limited")
	}
	// Accounting export: voucher sales are a liability, redemptions and breakage deferred movements.
	exp := string(sa.Must(200, "GET", "/api/v1/billing/accounting-export", nil).Body)
	for _, want := range []string{"liability,voucher,voucher_deferred", "deferred_recognition,voucher,sport_entry", "deferred_breakage,voucher,breakage"} {
		if !contains(exp, want) {
			t.Fatalf("accounting export lacks %q:\n%s", want, exp)
		}
	}
	// Reminder job runs without error (customers with e-mail get H-30/H-7 notices).
	if _, err := inst.App.Vouchers.SendExpiryReminders(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// EP-03 acceptance: golf, futsal and restaurant charges signed to the member
// account appear on one Member Statement whose closing balance equals the
// Member Account balance.
func TestP2MemberStatement(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	before := sa.Must(200, "GET", "/api/v1/billing/member-accounts/"+f.AccountA, nil).JSON()
	for _, c := range []struct{ line, comp, desc, amt string }{
		{"golf", "green_fee", "Green fee 18 holes", "640000"}, {"sportclub", "court", "Futsal court", "245000"}, {"pos", "fnb", "The Spike Bar dinner", "380000"},
	} {
		fo := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": c.line, "customerId": f.CustomerA}))
		sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/charges", map[string]any{"businessLine": c.line, "revenueComponent": c.comp, "description": c.desc, "amount": c.amt})
		sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/payments", map[string]any{"methodType": "member_account", "amount": c.amt})
		sa.Must(200, "POST", "/api/v1/billing/folios/"+fo+":close", map[string]any{})
	}
	st := sa.Must(200, "GET", "/api/v1/billing/member-accounts/"+f.AccountA+"/statement", nil).JSON()
	lines := map[string]string{}
	for _, s := range st["sections"].([]any) {
		sm := s.(map[string]any)
		lines[str(sm["businessLine"])] = str(sm["charges"])
	}
	if lines["golf"] != "640000" || lines["sportclub"] != "245000" || lines["pos"] == "" {
		t.Fatalf("statement sections: %v", st["sections"])
	}
	if st["closingBalance"] != st["accountBalance"] {
		t.Fatalf("closing %v != account %v (before %v)", st["closingBalance"], st["accountBalance"], before["balance"])
	}
	// credit limit enforced online; payment against the account reduces it
	sa.Must(200, "PATCH", "/api/v1/billing/member-accounts/"+f.AccountA, map[string]any{"creditLimit": st["accountBalance"]})
	fo := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": "pos", "customerId": f.CustomerA}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/charges", map[string]any{"businessLine": "pos", "revenueComponent": "fnb", "description": "Coffee", "amount": "35000"})
	if r := sa.Do("POST", "/api/v1/billing/folios/"+fo+"/payments", map[string]any{"methodType": "member_account", "amount": "35000"}); r.Status != 409 {
		t.Fatalf("credit limit: %s", r)
	}
	sa.Must(200, "POST", "/api/v1/billing/member-accounts/"+f.AccountA+"/entries", map[string]any{"entryType": "payment", "amount": "500000", "description": "Bank transfer"})
	sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/payments", map[string]any{"methodType": "member_account", "amount": "35000"})
	sa.Must(200, "PATCH", "/api/v1/billing/member-accounts/"+f.AccountA, map[string]any{"creditLimit": "50000000"})
	// refund, void, reopen, reconciliation, export
	p := sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/payments", map[string]any{"methodType": "cash", "amount": "10000"}).JSON()
	sa.Must(200, "POST", "/api/v1/billing/payments/"+str(p["id"])+":refund", map[string]any{"reason": "overpaid"})
	det := sa.Must(200, "GET", "/api/v1/billing/folios/"+fo, nil).JSON()
	lineID := str(det["lines"].([]any)[0].(map[string]any)["id"])
	sa.Must(201, "POST", "/api/v1/billing/folios/"+fo+"/charges", map[string]any{"businessLine": "pos", "revenueComponent": "fnb", "description": "Wrong", "amount": "1000"})
	det = sa.Must(200, "GET", "/api/v1/billing/folios/"+fo, nil).JSON()
	for _, l := range det["lines"].([]any) {
		lm := l.(map[string]any)
		if lm["description"] == "Wrong" {
			sa.Must(204, "POST", "/api/v1/billing/folio-lines/"+str(lm["id"])+":void", map[string]any{"reason": "keyed in error"})
		}
	}
	_ = lineID
	sa.Must(200, "POST", "/api/v1/billing/folios/"+fo+":close", map[string]any{})
	sa.Must(200, "POST", "/api/v1/billing/folios/"+fo+":reopen", map[string]any{"reason": "late tip"})
	if n := len(sa.Must(200, "GET", "/api/v1/billing/payment-summary", nil).Items()); n == 0 {
		t.Fatal("payment summary")
	}
	exp := sa.Must(200, "GET", "/api/v1/billing/accounting-export", nil)
	if !contains(string(exp.Body), "revenue,golf,green_fee") || !contains(string(exp.Body), "payment,back_office,member_account") {
		t.Fatalf("export: %s", exp.Body)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/folios?filter[customerId]="+f.CustomerA, nil).Items()); n < 3 {
		t.Fatalf("folios %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/member-accounts?q=Hendra", nil).Items()); n != 1 {
		t.Fatalf("accounts %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/member-accounts:limit-cache", nil).Items()); n < 1 {
		t.Fatalf("limit cache %d", n)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
