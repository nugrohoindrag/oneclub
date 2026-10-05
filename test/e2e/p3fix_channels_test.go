package e2e

// PRD P3 gap fixes, package Channels & UI: Customer / Corporate 360 banquet
// sections, points as a POS tender with a customer and personal promo code,
// website inquiry consent, online payment of payment schedule lines.

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
