package e2e

// PRD P3 gap fixes, package Channels & UI: Customer / Corporate 360 banquet
// sections, points as a POS tender with a customer and personal promo code,
// website inquiry consent, online payment of payment schedule lines.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// FR-C360-01 / FR-C360-04: the Banquet / Event section of the Customer 360
// (placeholder "live") and of the Corporate 360 with contract value, money
// received and outstanding.
func TestP3FixChannelsCustomer360Banquet(t *testing.T) {
	sa := superAdmin(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	taxes := bqTax(t, sa, "C3"+sfx)
	typ := bqType(t, sa, "C3"+sfx, "mice")
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "C3K" + sfx, "name": "PT Seminar " + sfx}))
	cust := idOf(sa.Must(201, "POST", "/api/v1/crm/customers", map[string]any{"code": "C3P" + sfx, "name": "PIC Seminar " + sfx, "phone": "+62829" + sfx}))
	room := bqVenue(t, sa, map[string]any{"code": "C3R" + sfx, "name": "Opal Room " + sfx, "venueType": "function_room", "maxCapacity": 60})
	pkg := idOf(sa.Must(201, "POST", "/api/v1/banquet/packages", map[string]any{"code": "C3M" + sfx, "name": "Half Day Meeting " + sfx, "category": "mice",
		"pricingMethod": "per_pax", "price": "300000", "pricingMode": "nett", "taxCodes": taxes, "minPax": 10, "durationHours": 4}))
	start := bqDay(45, 9, 0)
	ev := sa.Must(201, "POST", "/api/v1/banquet/events", map[string]any{"title": "Seminar " + sfx, "eventTypeId": typ, "customerId": cust,
		"corporateAccountId": corp, "start": rfc(start), "end": rfc(start.Add(4 * time.Hour)), "expectedPax": 20, "packageId": pkg,
		"venues": []map[string]any{{"venueId": room["id"], "functionName": "Seminar", "layout": ""}}}).JSON()
	contract := bilDec(ev["contractTotal"])
	if !contract.IsPositive() {
		t.Fatalf("event contract: %v", ev)
	}
	section := func(path string) map[string]any {
		t.Helper()
		v := sa.Must(200, "GET", path, nil).JSON()
		s, _ := v["sections"].(map[string]any)["banquet"].(map[string]any)
		if s == nil {
			t.Fatalf("%s has no banquet section: %v", path, v["sections"])
		}
		return s
	}
	eventOf := func(s map[string]any) map[string]any {
		t.Helper()
		for _, e := range asMaps(s["events"]) {
			if e["id"] == ev["id"] {
				return e
			}
		}
		t.Fatalf("event %v not in the banquet section: %v", ev["number"], s)
		return nil
	}
	c360 := sa.Must(200, "GET", "/api/v1/crm/customers/"+cust+"/360", nil).JSON()
	if c360["placeholders"].(map[string]any)["banquet"] != "live" {
		t.Fatalf("Customer 360 banquet placeholder: %v", c360["placeholders"])
	}
	e := eventOf(section("/api/v1/crm/customers/" + cust + "/360"))
	if e["status"] != ev["status"] || !bilDec(e["outstanding"]).Equal(contract) || e["companyName"] != "PT Seminar "+sfx {
		t.Fatalf("Customer 360 event: %v", e)
	}
	// The down payment received lowers the outstanding (Corporate 360 too).
	bl := sa.Must(201, "POST", "/api/v1/banquet/events/"+str(ev["id"])+"/payment-schedule", map[string]any{}).JSON()
	sc := bl["schedule"].(map[string]any)
	dp := asMaps(sc["lines"])[0]
	sa.Must(201, "POST", "/api/v1/billing/payment-schedules/"+str(sc["id"])+"/lines/"+str(dp["id"])+":pay",
		map[string]any{"methodType": "bank_transfer", "reference": "C3-" + sfx}, "Idempotency-Key", newKey())
	bqDispatch(t, "event definite on the DP", func() bool {
		return sa.Must(200, "GET", "/api/v1/banquet/events/"+str(ev["id"]), nil).JSON()["status"] == "definite"
	})
	corpSec := section("/api/v1/crm/corporate-accounts/" + corp + "/360")
	e = eventOf(corpSec)
	want := contract.Sub(bilDec(dp["amount"]))
	if !bilDec(e["received"]).Equal(bilDec(dp["amount"])) || !bilDec(e["outstanding"]).Equal(want) || !bilDec(corpSec["outstanding"]).Equal(want) {
		t.Fatalf("Corporate 360 event after the DP %v: %v / %v", dp["amount"], e, corpSec)
	}
	if !bilDec(corpSec["contractTotal"]).Equal(contract) || decimal.NewFromFloat(corpSec["upcoming"].(float64)).IntPart() < 1 {
		t.Fatalf("Corporate 360 banquet totals: %v", corpSec)
	}
}

// FR-LOY-05 / FR-OPS-P3-03 / §9.4: at the POS the cashier picks the customer
// (member price, personal promo code) and takes part of the bill in loyalty
// points; the rest is paid in cash. Points are redeemed once, through the
// offline sync queue too (the idempotency key of the queued sale).
func TestP3FixChannelsPOSPoints(t *testing.T) {
	sa := superAdmin(t, inst)
	cashier := roleUser(t, inst, "cashier")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "LPO" + sfx, "name": "Points Cafe " + sfx, "outletType": "cafe",
		"pricingMode": "nett"}))
	coffee := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "LPC" + sfx, "name": "Kopi " + sfx, "category": "Coffee",
		"productType": "beverage", "price": "85000"}))
	sa.Must(201, "POST", "/api/v1/commercial/menus", map[string]any{"code": "LPM" + sfx, "name": "Cafe " + sfx, "outletId": outlet, "productIds": []string{coffee},
		"channels": []string{"pos"}})
	shift := str(cashier.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "0"}).JSON()["id"])
	cust := customer(t, sa, "LPP"+sfx, "Poin Pelanggan "+sfx, map[string]any{"phone": "+62815" + sfx})
	// the POS customer picker searches CRM customers and shows the loyalty account
	if found := cashier.Must(200, "GET", "/api/v1/crm/customers?q=Poin+Pelanggan+"+sfx+"&filter[status]=active", nil).Items(); !containsID(found, cust) {
		t.Fatalf("customer search at the POS: %v", found)
	}
	aid := str(sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts", map[string]any{"customerId": cust}).JSON()["id"])
	sa.Must(201, "POST", "/api/v1/crm/loyalty/accounts/"+aid+":adjust", map[string]any{"points": 600, "reason": "Welcome points " + sfx})
	acct := cashier.Must(200, "GET", "/api/v1/crm/loyalty/accounts?filter[customerId]="+cust, nil).Items()
	if len(acct) != 1 || acct[0]["balance"].(float64) != 600 {
		t.Fatalf("loyalty account at the POS: %v", acct)
	}
	value := bilDec(acct[0]["redemptionValue"])

	// personal promo code of the customer: priced only with the customer
	promo := idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "LPP-" + sfx, "name": "Kopi 20% " + sfx,
		"promoType": "promo_code", "discountPercent": "20", "productIds": []string{coffee}, "channels": []string{"pos"}}))
	sa.Must(200, "POST", "/api/v1/commercial/promotions/"+promo+":activate", nil)
	code := str(asMaps(sa.Must(201, "POST", "/api/v1/commercial/promo-codes:generate", map[string]any{"promotionId": promo, "customerIds": []string{cust},
		"prefix": "LP", "length": 6}).JSON()["codes"])[0]["code"])
	cart := map[string]any{"channel": "pos", "businessLine": "pos", "outletId": outlet, "promoCodes": []string{code},
		"lines": []map[string]any{{"key": "0", "productId": coffee, "quantity": "2", "unitPrice": "85000"}}}
	if ev := cashier.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", cart).JSON(); !bilDec(ev["discount"]).IsZero() {
		t.Fatalf("personal code without the customer: %v", ev)
	}
	cart["customerId"] = cust
	if ev := cashier.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", cart).JSON(); !bilDec(ev["discount"]).Equal(decimal.NewFromInt(34000)) {
		t.Fatalf("personal code with the customer: %v", ev)
	}

	// online sale: 300 points + cash
	o := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift, "customerId": cust, "promoCodes": []string{code},
		"lines": []map[string]any{{"productId": coffee, "quantity": "2"}}}, "Idempotency-Key", newKey()).JSON()
	total := bilDec(o["total"])
	if !total.Equal(decimal.NewFromInt(136000)) || str(o["customerId"]) != cust {
		t.Fatalf("order with customer and personal code: %v", o)
	}
	paid := cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(o["id"])+":pay", map[string]any{"shiftId": shift, "tenders": []map[string]any{
		{"methodType": "loyalty_points", "tender": map[string]any{"points": 300}}, {"methodType": "cash"}}}, "Idempotency-Key", newKey()).JSON()
	if paid["status"] != "paid" {
		t.Fatalf("order paid with points and cash: %v", paid)
	}
	if b := engBalance(t, sa, aid); b < 300 || b >= 600 {
		t.Fatalf("300 of 600 points redeemed (plus points earned on the cash), balance %d", b)
	}
	var pts, cash string
	sysQueryRow(t, inst, `SELECT coalesce(sum(amount) FILTER (WHERE method_type = 'loyalty_points'), 0)::text,
		coalesce(sum(amount) FILTER (WHERE method_type = 'cash'), 0)::text FROM billing.payments
		WHERE folio_id = (SELECT folio_id FROM commercial.order_bills WHERE order_id = $1 LIMIT 1)`, []any{mustUUID(str(o["id"]))}, &pts, &cash)
	if !bilDec(pts).Equal(value.Mul(decimal.NewFromInt(300))) || !bilDec(pts).Add(bilDec(cash)).Equal(total) {
		t.Fatalf("tenders: points %s + cash %s for %s", pts, cash, total)
	}
	if h := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid+"/ledger?filter[kind]=redeemed", nil).Items(); len(h) != 1 || h[0]["points"].(float64) != -300 {
		t.Fatalf("points ledger after the POS sale: %v", h)
	}

	// the same tender through the POS sync queue, sent twice: redeemed once
	item := map[string]any{"id": newKey(), "action": "commercial.pos_order", "payload": map[string]any{
		"order": map[string]any{"id": uuid.Must(uuid.NewV7()).String(), "outletId": outlet, "shiftId": shift, "customerId": cust, "lines": []map[string]any{{"productId": coffee}}},
		"payment": map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "loyalty_points", "tender": map[string]any{"points": 100}},
			{"methodType": "cash"}}}}}
	for i := 0; i < 2; i++ {
		r := cashier.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()
		if st := r["results"].([]any)[0].(map[string]any)["status"]; st != "accepted" && st != "duplicate" {
			t.Fatalf("queued POS sale with points: %v", r)
		}
	}
	// (earned points of the settled cash may arrive meanwhile: count the redemptions)
	if h := sa.Must(200, "GET", "/api/v1/crm/loyalty/accounts/"+aid+"/ledger?filter[kind]=redeemed", nil).Items(); len(h) != 2 ||
		h[0]["points"].(float64)+h[1]["points"].(float64) != -400 {
		t.Fatalf("queued sale redeems 100 points once: %v", h)
	}
}

// FR-WEB-P3-02 / FR-LEAD-09: the website inquiry and contact forms send the
// visitor's explicit marketing consent (unticked by default) and offer
// corporate golf and tournament; the consent is stored on the lead and, for
// the contact form, on the customer; no consent ⇒ none recorded.
func TestP3FixChannelsWebsiteConsent(t *testing.T) {
	sa := superAdmin(t, inst)
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)
	guest := func(who string) map[string]any {
		return map[string]any{"name": "Web " + who + " " + sfx, "email": who + sfx + "@consent.test"}
	}
	lead := func(who string) map[string]any {
		t.Helper()
		items := sa.Must(200, "GET", "/api/v1/crm/leads?q="+who+sfx, nil).Items()
		if len(items) != 1 {
			t.Fatalf("lead of %s: %v", who, items)
		}
		return items[0]
	}
	// inquiry form: corporate golf with company, consent ticked
	pub.Must(202, "POST", "/api/v1/public/inquiries", map[string]any{"propertyId": inst.Main, "guest": guest("corpgolf"), "line": "golf",
		"eventType": "tournament", "companyName": "PT Golf Day " + sfx, "pax": 60, "message": "Corporate golf day for our clients",
		"consent": true, "channel": "website_corporate_golf"})
	if l := lead("corpgolf"); l["line"] != "golf" || l["marketingConsent"] != true || l["companyName"] != "PT Golf Day "+sfx {
		t.Fatalf("corporate golf inquiry: %v", l)
	}
	// tournament inquiry without consent (the box stays unticked)
	pub.Must(202, "POST", "/api/v1/public/inquiries", map[string]any{"propertyId": inst.Main, "guest": guest("trnq"), "line": "tournament",
		"message": "Charity tournament for 100 players", "consent": false, "channel": "website_tournament"})
	if l := lead("trnq"); l["line"] != "tournament" || l["marketingConsent"] != false {
		t.Fatalf("tournament inquiry: %v", l)
	}
	// contact form: consent goes to the customer and the lead
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": inst.Main, "guest": guest("contactyes"), "topic": "corporate golf",
		"message": "Golf outing for 40 staff", "consent": true})
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": inst.Main, "guest": guest("contactno"), "topic": "tournament",
		"message": "Do you host club tournaments?"})
	if l := lead("contactyes"); l["line"] != "golf" || l["marketingConsent"] != true {
		t.Fatalf("contact lead with consent: %v", l)
	}
	if l := lead("contactno"); l["line"] != "tournament" || l["marketingConsent"] != false {
		t.Fatalf("contact lead without consent: %v", l)
	}
	var yes, no bool
	sysQueryRow(t, inst, `SELECT marketing_opt_in FROM crm.customers WHERE email = $1`, []any{"contactyes" + sfx + "@consent.test"}, &yes)
	sysQueryRow(t, inst, `SELECT marketing_opt_in FROM crm.customers WHERE email = $1`, []any{"contactno" + sfx + "@consent.test"}, &no)
	if !yes || no {
		t.Fatalf("customer marketing consent from the contact form: with %v, without %v", yes, no)
	}
	// a later message with the box unticked never revokes it
	pub.Must(202, "POST", "/api/v1/public/contact", map[string]any{"propertyId": inst.Main, "guest": guest("contactyes"), "topic": "general",
		"message": "Thanks!"})
	sysQueryRow(t, inst, `SELECT marketing_opt_in FROM crm.customers WHERE email = $1`, []any{"contactyes" + sfx + "@consent.test"}, &yes)
	if !yes {
		t.Fatal("an unticked box revoked the consent")
	}
}

// fixSchedule creates a payment schedule (DP 30% due today, settlement 70%)
// of a customer on a folio of that customer.
func fixSchedule(t *testing.T, sa *Client, cust, title string) (sid, dp, final string) {
	t.Helper()
	loc := clubLoc(inst)
	folio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"holderName": title, "customerId": cust}))
	sch := sa.Must(201, "POST", "/api/v1/billing/payment-schedules", map[string]any{"title": title, "folioId": folio, "customerId": cust,
		"sourceType": "other", "totalAmount": "10000000", "lines": []map[string]any{
			{"label": "DP 30%", "kind": "down_payment", "percent": "30", "dueDate": time.Now().In(loc).Format("2006-01-02")},
			{"label": "Pelunasan", "kind": "final", "percent": "70", "dueDate": time.Now().In(loc).AddDate(0, 0, 30).Format("2006-01-02")}}},
		"Idempotency-Key", newKey()).JSON()
	lines := asMaps(sch["lines"])
	return str(sch["id"]), str(lines[0]["id"]), str(lines[1]["id"])
}

// fixLineStatus is the status of a schedule line.
func fixLineStatus(t *testing.T, line string) string {
	t.Helper()
	var st string
	sysQueryRow(t, inst, `SELECT status FROM billing.payment_schedule_lines WHERE id = $1`, []any{mustUUID(line)}, &st)
	return st
}

// FR-APP-P3-07 / FR-WEB-P3-05 / FR-INT-P3-04: a DP or termin is paid online
// (payment gateway) from the Member App, from the schedule's payment link
// (also sent in the schedule reminder) and right after the online
// acceptance of a quotation ("Pay down payment") — without an invoice first.
func TestP3FixChannelsSchedulePayOnline(t *testing.T) {
	sa := superAdmin(t, inst)
	sx := roleUser(t, inst, "sales_executive")
	pub := anon(t, inst)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e7)

	// Member App: Pay on my schedule line; never on somebody else's.
	mc, memberCust := trnMemberClient(t, sa)
	sid, dp, final := fixSchedule(t, sa, memberCust, "Family wedding "+sfx)
	found := false
	for _, s := range mc.Must(200, "GET", "/api/v1/member/payment-schedules", nil).Items() {
		found = found || s["id"] == sid
	}
	if !found {
		t.Fatal("my payment schedules")
	}
	other := customer(t, sa, "SPO"+sfx, "Other Payer "+sfx, map[string]any{"email": "other" + sfx + "@pay.test"})
	osid, odp, _ := fixSchedule(t, sa, other, "Other wedding "+sfx)
	mc.Must(404, "POST", "/api/v1/member/payment-schedules/"+osid+"/lines/"+odp+":pay-online", map[string]any{"method": "qris"})
	mc.Must(404, "POST", "/api/v1/member/payment-schedules/"+sid+"/lines/"+odp+":pay-online", map[string]any{"method": "qris"})
	mc.Must(422, "POST", "/api/v1/member/payment-schedules/"+sid+"/lines/"+dp+":pay-online", map[string]any{"method": "cash"})
	op := mc.Must(201, "POST", "/api/v1/member/payment-schedules/"+sid+"/lines/"+dp+":pay-online", map[string]any{"method": "qris"}).JSON()
	if op["status"] != "pending" || op["channel"] != "online" || !bilDec(op["amount"]).Equal(decimal.NewFromInt(3_000_000)) {
		t.Fatalf("member DP checkout: %v", op)
	}
	bilSettleOnline(t, op["externalId"], func() bool { return fixLineStatus(t, dp) == "paid" })

	// Payment link of the schedule (website /payment/{token}): the settlement.
	var token string
	sysQueryRow(t, inst, `SELECT public_token FROM billing.payment_schedules WHERE id = $1`, []any{mustUUID(sid)}, &token)
	if len(token) < 32 {
		t.Fatalf("schedule payment link token: %q", token)
	}
	pub.Must(404, "GET", "/api/v1/public/payment-schedules/not-a-token", nil)
	ps := pub.Must(200, "GET", "/api/v1/public/payment-schedules/"+token, nil).JSON()
	if str(ps["nextLineId"]) != final || ps["token"] != token || len(asMaps(ps["lines"])) != 2 || asMaps(ps["lines"])[0]["payable"] != false {
		t.Fatalf("public schedule: %v", ps)
	}
	pub.Must(404, "POST", "/api/v1/public/payment-schedules/"+token+"/lines/"+odp+":pay", map[string]any{"method": "qris"})
	lp := pub.Must(201, "POST", "/api/v1/public/payment-schedules/"+token+"/lines/"+final+":pay", map[string]any{"method": "virtual_account"}).JSON()
	if lp["status"] != "pending" || !bilDec(lp["amount"]).Equal(decimal.NewFromInt(7_000_000)) {
		t.Fatalf("payment link checkout: %v", lp)
	}
	bilSettleOnline(t, lp["externalId"], func() bool { return fixLineStatus(t, final) == "paid" })
	if s := sa.Must(200, "GET", "/api/v1/billing/payment-schedules/"+sid, nil).JSON(); s["status"] != "completed" {
		t.Fatalf("schedule paid online: %v", s)
	}
	if ps := pub.Must(200, "GET", "/api/v1/public/payment-schedules/"+token, nil).JSON(); ps["nextLineId"] != nil || ps["status"] != "completed" {
		t.Fatalf("paid schedule behind the link: %v", ps)
	}

	// The schedule reminder carries the payment link.
	var otoken string
	sysQueryRow(t, inst, `SELECT public_token FROM billing.payment_schedules WHERE id = $1`, []any{mustUUID(osid)}, &otoken)
	sysExec(t, inst, `UPDATE billing.payment_schedule_lines SET due_date = current_date - 1 WHERE id = $1`, mustUUID(odp))
	ctx := dbtx.System(t.Context())
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := inst.App.BillingHTTP.ScheduleReminders(ctx, tx, inst.Main)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var body string
	sysQueryRow(t, inst, `SELECT coalesce((SELECT body FROM platform.notification_deliveries WHERE event_code = 'billing.payment_schedule_reminder'
		AND recipient = $1 ORDER BY created_at DESC LIMIT 1), '')`, []any{"other" + sfx + "@pay.test"}, &body)
	if !strings.Contains(body, "/payment/"+otoken) {
		t.Fatalf("schedule reminder without the payment link: %q", body)
	}

	// Quotation accepted online → "Pay down payment" on the same page.
	qc := customer(t, sa, "SPQ"+sfx, "Golf Day Host "+sfx, map[string]any{"email": "host" + sfx + "@pay.test"})
	q := sx.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": qc, "title": "Golf day " + sfx, "line": "golf", "pricingMode": "nett",
		"paymentTerms": []map[string]any{{"label": "Down Payment 50%", "percent": "50", "dueDays": 3}, {"label": "Final Payment", "percent": "50", "dueDays": 10}},
		"lines": []map[string]any{{"itemType": "service", "description": "Green fee", "quantity": "20", "unitPrice": "1000000"}}}, "Idempotency-Key", newKey()).JSON()
	tok := slsToken(t, sx.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":send", map[string]any{}).JSON())
	pub.Must(404, "GET", "/api/v1/public/quotations/"+tok+"/payment-schedule", nil) // not accepted yet
	pub.Must(200, "POST", "/api/v1/public/quotations/"+tok+":accept", map[string]any{"name": "Golf Day Host", "termsAccepted": true})
	var qs map[string]any
	slsDispatch(t, "quotation schedule for the DP step", func() bool {
		r := pub.Do("GET", "/api/v1/public/quotations/"+tok+"/payment-schedule", nil)
		if r.Status == 200 {
			qs = r.JSON()
		}
		return qs != nil
	})
	ql := asMaps(qs["lines"])
	if len(ql) != 2 || str(qs["nextLineId"]) != str(ql[0]["id"]) || ql[0]["kind"] != "down_payment" || !bilDec(ql[0]["amount"]).Equal(decimal.NewFromInt(10_000_000)) {
		t.Fatalf("quotation payment schedule: %v", qs)
	}
	qp := pub.Must(201, "POST", "/api/v1/public/payment-schedules/"+str(qs["token"])+"/lines/"+str(ql[0]["id"])+":pay", map[string]any{"method": "qris"}).JSON()
	bilSettleOnline(t, qp["externalId"], func() bool { return fixLineStatus(t, str(ql[0]["id"])) == "paid" })
}
