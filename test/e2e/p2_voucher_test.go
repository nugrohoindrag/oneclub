package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
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
	if st := sale["folio"].(map[string]any)["status"]; st != "closed" {
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
	// The value voucher pays a restaurant bill at the POS (tender hook C3).
	resto := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "VCH-RESTO", "name": "Voucher Restaurant", "outletType": "restaurant"}))
	nasi := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "VCH-NASI", "name": "Nasi Goreng", "productType": "food", "price": "120000"}))
	ord := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": resto, "lines": []map[string]any{{"productId": nasi}}}).JSON()
	paid := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(ord["id"])+":pay", map[string]any{
		"tenders": []map[string]any{{"methodType": "voucher_prepaid", "tender": map[string]any{"code": gv["code"]}}}}, "Idempotency-Key", newKey()).JSON()
	if paid["status"] != "paid" {
		t.Fatalf("voucher tender: %v", paid)
	}
	giftLeft := dec("500000").Sub(dec(paid["total"])).String()
	if gb := sa.Must(200, "GET", "/api/v1/commercial/vouchers/"+str(gv["code"])+"/balance", nil).JSON(); str(gb["remaining"]) != giftLeft {
		t.Fatalf("gift voucher balance %v, want %s", gb["remaining"], giftLeft)
	}

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
	if str(chk["remaining"]) != giftLeft || chk["customerName"] != nil {
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
	exp := accountingExport(t, sa)
	for _, want := range []string{"liability,voucher_deferred,", "revenue,fnb,", "deferred_recognition,sport_entry,voucher recognition", "deferred_breakage,breakage,voucher breakage"} {
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
// Member Account balance (FR-BIL-P2-02). Every charge comes from its real
// flow: a golf walk-in folio, a court booking and a POS order.
func TestP2MemberStatement(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "P2-STMT", "name": "Surya Statement", "email": "surya@p2.test"}))
	acct := idOf(sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": cust, "accountType": "member", "creditLimit": "50000000"}))
	charged := map[string]decimal.Decimal{}
	memberCharge := func(line, folio string) {
		p := sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": folio, "methodType": "member_account"}, "Idempotency-Key", newKey()).JSON()
		charged[line] = charged[line].Add(dec(p["amount"]))
	}

	// Golf: a walk-in folio at the golf front desk.
	golf := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"customerId": cust, "holderName": "Surya Statement"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+golf+"/lines", map[string]any{"chargeType": "other", "description": "Range balls & rental set", "unitPrice": "640000"})
	memberCharge("golf", golf)
	sa.Must(200, "POST", "/api/v1/billing/folios/"+golf+":close", nil)

	// Futsal: a court booking whose folio is signed to the member account.
	rule(t, sa, map[string]any{"code": "STMT-FUTSAL", "name": "Statement futsal Mon–Thu 16–21", "serviceType": "sport_court", "itemRef": "STMT-FUTSAL",
		"lineDayTypeId": f.MonThu, "timeBandId": f.Evening, "unit": "slot", "unitMinutes": 60, "price": "245000", "pricingMode": "nett",
		"taxCodes": []string{"P2VAT"}, "revenueComponent": "court"})
	court := resourceOf(t, sa, "STMT-COURT", "sport_court", nil, map[string]any{"priceItem": "STMT-FUTSAL"})
	cd := nextWeekday(f.Loc, time.Tuesday, 2)
	res := sa.Must(201, "POST", "/api/v1/reservation/reservations", map[string]any{"lines": []map[string]any{{"resourceId": court,
		"start": rfc(at(cd, 17, 0)), "end": rfc(at(cd, 18, 0))}}, "customerId": cust, "charge": true, "segment": "walk_in", "confirm": true},
		"Idempotency-Key", newKey()).JSON()
	if res["folioId"] == nil {
		t.Fatalf("court booking has no folio: %v", res)
	}
	memberCharge("sportclub", str(res["folioId"]))

	// Restaurant: a POS order paid by member charge.
	resto := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "STMT-RESTO", "name": "Statement Restaurant", "outletType": "restaurant"}))
	steak := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "STMT-STEAK", "name": "Sirloin Steak", "productType": "food", "price": "380000"}))
	ord := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": resto, "customerId": cust, "lines": []map[string]any{{"productId": steak}}}).JSON()
	paid := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(ord["id"])+":pay", map[string]any{"tenders": []map[string]any{{"methodType": "member_account"}}},
		"Idempotency-Key", newKey()).JSON()
	if paid["status"] != "paid" {
		t.Fatalf("POS member charge: %v", paid)
	}
	charged["pos"] = dec(paid["total"])

	// Credit limit is enforced online; a payment against the account frees it.
	bal := sa.Must(200, "GET", "/api/v1/billing/customer-accounts/"+acct, nil).JSON()["balance"]
	sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": cust, "accountType": "member", "creditLimit": str(bal)})
	cafe := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"customerId": cust, "holderName": "Surya Statement"}))
	sa.Must(201, "POST", "/api/v1/billing/folios/"+cafe+"/lines", map[string]any{"chargeType": "other", "description": "Coffee", "unitPrice": "35000"})
	if r := sa.Do("POST", "/api/v1/billing/payments", map[string]any{"folioId": cafe, "methodType": "member_account"}); r.Status != 409 {
		t.Fatalf("credit limit: %s", r)
	}
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"accountId": acct, "methodType": "bank_transfer", "amount": "500000", "reference": "TRF-STMT"},
		"Idempotency-Key", newKey())
	memberCharge("golf", cafe)
	sa.Must(201, "POST", "/api/v1/billing/customer-accounts", map[string]any{"customerId": cust, "accountType": "member", "creditLimit": "50000000"})

	// A wrong line is voided; an overpayment is refunded; the folio closes and reopens.
	extra := sa.Must(201, "POST", "/api/v1/billing/folios/"+cafe+"/lines", map[string]any{"chargeType": "other", "description": "Wrong", "unitPrice": "1000"}).JSON()
	for _, l := range extra["lines"].([]any) {
		if lm := l.(map[string]any); lm["description"] == "Wrong" {
			sa.Must(200, "POST", "/api/v1/billing/folios/"+cafe+"/lines/"+str(lm["id"])+":void", map[string]any{"reason": "keyed in error"})
		}
	}
	sa.Must(201, "POST", "/api/v1/billing/folios/"+cafe+"/lines", map[string]any{"chargeType": "other", "description": "Tip", "unitPrice": "10000"})
	cash := sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": cafe, "methodType": "cash", "amount": "20000"}, "Idempotency-Key", newKey()).JSON()
	if rf := sa.Must(201, "POST", "/api/v1/billing/refunds", map[string]any{"paymentId": cash["id"], "amount": "10000", "reason": "overpaid"}).JSON(); rf["status"] != "completed" {
		t.Fatalf("refund: %v", rf)
	}
	sa.Must(200, "POST", "/api/v1/billing/folios/"+cafe+":close", nil)
	sa.Must(200, "POST", "/api/v1/billing/folios/"+cafe+":reopen", map[string]any{"reason": "late tip"})

	// The monthly statement: one section per business line, closing = account balance.
	period := time.Now().In(clubLoc(inst)).Format("2006-01")
	sa.Must(200, "POST", "/api/v1/billing/member-statements:generate", map[string]any{"period": period})
	sts := sa.Must(200, "GET", "/api/v1/billing/member-statements?filter[accountId]="+acct, nil).Items()
	if len(sts) != 1 {
		t.Fatalf("statements: %v", sts)
	}
	byLine := map[string]decimal.Decimal{}
	for _, l := range sts[0]["lines"].([]any) {
		lm := l.(map[string]any)
		if a := dec(lm["amount"]); a.IsPositive() {
			byLine[str(lm["businessLine"])] = byLine[str(lm["businessLine"])].Add(a)
		}
	}
	for _, line := range []string{"golf", "sportclub", "pos"} {
		if !byLine[line].Equal(charged[line]) {
			t.Fatalf("statement %s charges %s, want %s (lines %v)", line, byLine[line], charged[line], sts[0]["lines"])
		}
	}
	acc := sa.Must(200, "GET", "/api/v1/billing/customer-accounts/"+acct, nil).JSON()
	if !dec(sts[0]["closingBalance"]).Equal(dec(acc["balance"])) {
		t.Fatalf("closing %v != account %v", sts[0]["closingBalance"], acc["balance"])
	}

	if !contains(accountingExport(t, sa), "member_charge,member_account,") {
		t.Fatal("accounting export lacks member charges")
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/folios?filter[customerId]="+cust, nil).Items()); n < 3 {
		t.Fatalf("folios %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/customer-accounts?q=Surya", nil).Items()); n != 1 {
		t.Fatalf("accounts %d", n)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/billing/customer-accounts:limit-cache", nil).Items()); n < 1 {
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
