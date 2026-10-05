package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func init() {
	// Promotions need a benefit matching their type and packages publish
	// with components: their CRUD is covered by TestP3CommercialPromotions
	// and TestP3CommercialPackages.
	for _, k := range []string{"commercial.promotion", "commercial.promo_code", "commercial.package", "commercial.package_component"} {
		resourceCRUDSkip[k] = "covered by TestP3Commercial*"
	}
}

// pcDispatch runs the outbox until cond holds.
func pcDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 15*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// pcLocal returns the local time of the club on the most recent day before
// today with the weekday at hh:mm (a past moment for offline sales).
func pcLocal(wd time.Weekday, hh, mm int) time.Time {
	loc := clubLoc(inst)
	d := time.Now().In(loc).AddDate(0, 0, -1)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, -1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, loc)
}

// pcEvents counts outbox events of a type whose payload has key = value.
func pcEvents(t *testing.T, eventType, key, value string) int {
	t.Helper()
	var n int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox WHERE event_type = $1 AND payload->>$2 = $3`, []any{eventType, key, value}, &n)
	return n
}

// pcPolicy sets a club policy.
func pcPolicy(t *testing.T, c *Client, category, code string, value map[string]any) {
	t.Helper()
	c.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": category, "code": code, "name": code, "value": value})
}

func pcLine(o map[string]any, product string) map[string]any {
	for _, l := range o["lines"].([]any) {
		m := l.(map[string]any)
		if m["productId"] == product && m["status"] == "active" {
			return m
		}
	}
	return nil
}

// EP-10 (FR-PRC-P3-01, FR-PRM-01..10, FR-POL-P3-02, FR-OPS-P3-03, FR-WEB-P3-07,
// FR-APP-P3-02) acceptance: "Buy 2 Only 200K" in its weekday and weekend
// hours at the POS online and offline (same price, mismatch flagged), two
// non-stackable promotions give the same result on the POS, website and
// member app, stacking groups and ceiling, promo codes (general, personal,
// limits, public check with rate limit), budgets, the activation approval,
// cashier removal with permission, the redemption ledger (K3), the
// Corporate / Holiday / Peak rate dimensions, reports and dashboard.
func TestP3CommercialPromotions(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	om := roleUser(t, inst, "outlet_manager")
	cashier := roleUser(t, inst, "cashier")
	gm := roleUser(t, inst, "general_manager")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	loc := clubLoc(inst)

	sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PC" + sfx, "name": "PPN nett " + sfx, "kind": "tax", "ratePercent": "11",
		"basis": "net_amount", "pricingMode": "nett", "effectiveFrom": past()})
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "PCO" + sfx, "name": "Promo Bar " + sfx, "outletType": "bar",
		"taxCodes": []string{"PC" + sfx}, "pricingMode": "nett"}))
	product := func(code, cat, price string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": code + sfx, "name": code + " " + sfx, "category": cat,
			"productType": "beverage", "price": price}))
	}
	beer := product("PCBEER", "Beer", "150000")
	juice := product("PCJUICE", "Juice", "50000")
	coffee := product("PCCOF", "Coffee", "40000")
	cake := product("PCCAKE", "Cake", "60000")
	tea := product("PCTEA", "Tea", "100000")
	shift := str(sa.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "0"}).JSON()["id"])

	// ── master data, validation, simulation (FR-PRM-01/02/08) ────────────
	sa.Must(422, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-BAD" + sfx, "name": "No benefit", "promoType": "percent_discount"})
	sa.Must(422, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-HH0" + sfx, "name": "No window", "promoType": "happy_hour",
		"discountPercent": "10"})
	sa.Must(422, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-BUN0" + sfx, "name": "Bundle of one", "promoType": "bundle",
		"bundlePrice": "1000", "bundleItems": []map[string]any{{"productId": coffee, "quantity": 1}}})
	hh := sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-HH" + sfx, "name": "Buy 2 Only 200K " + sfx,
		"promoType": "buy_n_price_x", "buyQuantity": 2, "bundlePrice": "200000", "productIds": []string{beer}, "channels": []string{"pos", "website", "member_app"},
		"timeWindows": []map[string]any{{"days": []int{1, 2, 3, 4, 5}, "start": "16:00", "end": "19:00"}, {"days": []int{6, 7}, "start": "14:00", "end": "17:00"}},
		"priority":    10, "description": "Happy hour"}).JSON()
	hhID := str(hh["id"])
	if hh["status"] != "draft" || hh["version"].(float64) != 1 {
		t.Fatalf("new promotion: %v", hh)
	}
	sa.Must(200, "PATCH", "/api/v1/commercial/promotions/"+hhID, map[string]any{"terms": "Two beers per bill"})
	scen := func(label string, at time.Time) map[string]any {
		return map[string]any{"label": label, "at": at.UTC().Format(time.RFC3339), "channel": "pos",
			"lines": []map[string]any{{"productId": beer, "quantity": "2", "unitPrice": "150000"}}}
	}
	sim := sa.Must(200, "POST", "/api/v1/commercial/promotions/"+hhID+":simulate", map[string]any{"scenarios": []map[string]any{
		scen("weekday 17:00", pcLocal(time.Wednesday, 17, 0)), scen("weekday 20:00", pcLocal(time.Wednesday, 20, 0)),
		scen("saturday 15:00", pcLocal(time.Saturday, 15, 0)), scen("saturday 18:00", pcLocal(time.Saturday, 18, 0))}}).JSON()
	want := []int64{200000, 300000, 200000, 300000}
	for i, s := range sim["scenarios"].([]any) {
		if got := dec(s.(map[string]any)["total"]); !got.Equal(decimal.NewFromInt(want[i])) {
			t.Fatalf("simulation %d: %v", i, s)
		}
	}

	// ── activation with approval (FR-PRM-10, FR-POL-P3-02) ──────────────
	sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "promotion_activation", "name": "Big promotions " + sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "General Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "general_manager"),
			"conditions": []map[string]any{{"attribute": "discountPercent", "operator": "gte", "value": 40}}}}})
	big := idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-BIG" + sfx, "name": "Half price tea", "promoType": "percent_discount",
		"discountPercent": "50", "productIds": []string{tea}}))
	if st := om.Must(200, "POST", "/api/v1/commercial/promotions/"+big+":activate", nil).JSON(); st["status"] != "pending" || st["approvalRequestId"] == nil {
		t.Fatalf("big promotion waits for approval: %v", st)
	}
	om.Must(409, "PATCH", "/api/v1/commercial/promotions/"+big, map[string]any{"name": "edit while pending"})
	if st := gm.Must(200, "POST", "/api/v1/commercial/promotions/"+big+":approve", map[string]any{"reason": "ok for launch"}).JSON(); st["status"] != "active" {
		t.Fatalf("approved promotion: %v", st)
	}
	gm.Must(409, "POST", "/api/v1/commercial/promotions/"+big+":approve", map[string]any{})
	if st := om.Must(200, "POST", "/api/v1/commercial/promotions/"+hhID+":activate", nil).JSON(); st["status"] != "active" {
		t.Fatalf("small promotion active at once: %v", st)
	}
	om.Must(409, "POST", "/api/v1/commercial/promotions/"+hhID+":activate", nil)

	// ── AC 1: Buy 2 Only 200K at the POS, online and offline (FR-PRM-09) ─
	cache := cashier.Must(200, "GET", "/api/v1/commercial/pos/promotions?outletId="+outlet, nil).JSON()
	cached := false
	for _, p := range cache["promotions"].([]any) {
		cached = cached || p.(map[string]any)["id"] == hhID
	}
	if !cached || cache["validUntil"] == nil {
		t.Fatalf("offline promotion cache: %v", cache)
	}
	offline := func(at time.Time, client string) map[string]any {
		oid := uuid.Must(uuid.NewV7()).String()
		item := map[string]any{"id": newKey(), "action": "commercial.pos_order", "payload": map[string]any{
			"order": map[string]any{"id": oid, "outletId": outlet, "shiftId": shift, "clientCreatedAt": at.UTC().Format(time.RFC3339), "clientTotal": client,
				"lines": []map[string]any{{"productId": beer, "quantity": "2"}}},
			"payment": map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "cash"}}}}}
		r := cashier.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()
		if x := r["results"].([]any)[0].(map[string]any); x["status"] != "accepted" {
			t.Fatalf("offline sale: %v", x)
		}
		return sa.Must(200, "GET", "/api/v1/commercial/orders/"+oid, nil).JSON()
	}
	o1 := offline(pcLocal(time.Tuesday, 17, 30), "200000")
	if !dec(o1["total"]).Equal(decimal.NewFromInt(200000)) || o1["promotionMismatch"] == true || o1["status"] != "paid" {
		t.Fatalf("offline happy hour: %v", o1)
	}
	if l := pcLine(o1, beer); !dec(l["promotionDiscount"]).Equal(decimal.NewFromInt(100000)) || len(l["promotions"].([]any)) != 1 {
		t.Fatalf("line promotion: %v", l)
	}
	o2 := offline(pcLocal(time.Sunday, 18, 0), "300000")
	if !dec(o2["total"]).Equal(decimal.NewFromInt(300000)) || o2["promotionMismatch"] == true {
		t.Fatalf("outside the hours: %v", o2)
	}
	// a terminal with a stale cache: the server price stands, flagged for review
	o3 := offline(pcLocal(time.Saturday, 14, 30), "300000")
	if !dec(o3["total"]).Equal(decimal.NewFromInt(200000)) || o3["promotionMismatch"] != true || o3["needsReview"] != true {
		t.Fatalf("offline mismatch: %v", o3)
	}
	for _, o := range []map[string]any{o1, o3} {
		pr := o["promotions"].([]any)
		if len(pr) != 1 || pr[0].(map[string]any)["status"] != "redeemed" {
			t.Fatalf("redeemed with the sale: %v", pr)
		}
	}
	if pcEvents(t, "commercial.promotion_applied", "sourceId", str(o1["id"])) != 1 {
		t.Fatal("commercial.promotion_applied (K3)")
	}

	// ── AC 2: non-stackable promotions — the higher priority wins in every channel ─
	pctID := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-P10" + sfx, "name": "Juice 10%",
		"promoType": "percent_discount", "discountPercent": "10", "productIds": []string{juice}, "priority": 50}))
	amtID := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-A5" + sfx, "name": "Juice −5.000",
		"promoType": "amount_discount", "discountAmount": "5000", "perUnit": true, "productIds": []string{juice}, "priority": 20}))
	for _, p := range []string{pctID, amtID} {
		om.Must(200, "POST", "/api/v1/commercial/promotions/"+p+":activate", nil)
	}
	for _, ch := range []string{"pos", "website", "member_app"} {
		ev := om.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", map[string]any{"channel": ch,
			"lines": []map[string]any{{"key": "j", "productId": juice, "quantity": "2", "unitPrice": "50000"}}}).JSON()
		ap := ev["applied"].([]any)
		if len(ap) != 1 || ap[0].(map[string]any)["promotionId"] != amtID || !dec(ev["discount"]).Equal(decimal.NewFromInt(10000)) {
			t.Fatalf("%s: %v", ch, ev)
		}
	}
	ord := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift,
		"lines": []map[string]any{{"productId": juice, "quantity": "2"}}}, "Idempotency-Key", newKey()).JSON()
	oid := str(ord["id"])
	if l := pcLine(ord, juice); !dec(l["promotionDiscount"]).Equal(decimal.NewFromInt(10000)) || !dec(ord["total"]).Equal(decimal.NewFromInt(90000)) {
		t.Fatalf("POS priority: %v", ord)
	}
	// the cashier cannot remove a promotion; the outlet manager can (FR-PRM-06)
	cashier.Must(403, "POST", "/api/v1/commercial/orders/"+oid+":remove-promotion", map[string]any{"promotionId": amtID, "reason": "customer asks"})
	om.Must(422, "POST", "/api/v1/commercial/orders/"+oid+":remove-promotion", map[string]any{"promotionId": amtID})
	rm := om.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":remove-promotion", map[string]any{"promotionId": amtID, "reason": "prefers the 10%"}).JSON()
	if o := rm["order"].(map[string]any); !dec(o["total"]).Equal(decimal.NewFromInt(90000)) || pcLine(o, juice)["promotions"].([]any)[0].(map[string]any)["promotionId"] != pctID {
		t.Fatalf("next promotion after removal: %v", rm)
	}
	om.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":remove-promotion", map[string]any{"promotionId": pctID, "reason": "staff meal"})
	back := cashier.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":apply-promotion", map[string]any{"promotionId": amtID}).JSON()
	if o := back["order"].(map[string]any); !dec(o["total"]).Equal(decimal.NewFromInt(90000)) {
		t.Fatalf("restored promotion: %v", back)
	}
	cashier.Must(422, "POST", "/api/v1/commercial/orders/"+oid+":apply-promotion", map[string]any{})
	// voiding the sale reverses its promotions
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":void", map[string]any{"reason": "test"})
	var reversed int
	sysQueryRow(t, inst, `SELECT count(*) FROM commercial.promotion_redemptions WHERE source_id = $1 AND status = 'reversed'`, []any{mustUUID(oid)}, &reversed)
	if reversed != 1 {
		t.Fatalf("void reverses the promotion: %d", reversed)
	}
	// the POS screen sends the promotions removed before the order exists (supervisor permission)
	exOrder := map[string]any{"outletId": outlet, "shiftId": shift, "promotionExclusions": []string{amtID}, "lines": []map[string]any{{"productId": juice, "quantity": "2"}}}
	cashier.Must(403, "POST", "/api/v1/commercial/orders", exOrder, "Idempotency-Key", newKey())
	if ex := om.Must(201, "POST", "/api/v1/commercial/orders", exOrder, "Idempotency-Key", newKey()).JSON(); pcLine(ex, juice)["promotions"].([]any)[0].(map[string]any)["promotionId"] != pctID {
		t.Fatalf("removed before the order: %v", ex)
	}

	// ── stacking groups and the ceiling (FR-PRM-03) ─────────────────────
	s1 := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-S1" + sfx, "name": "Coffee 20%", "promoType": "percent_discount",
		"discountPercent": "20", "productIds": []string{coffee}, "stackable": true, "stackGroup": "COF", "priority": 30}))
	s2 := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-S2" + sfx, "name": "Coffee 10%", "promoType": "percent_discount",
		"discountPercent": "10", "productIds": []string{coffee}, "stackable": true, "stackGroup": "COF", "priority": 40}))
	s3 := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-S3" + sfx, "name": "Coffee other group",
		"promoType": "amount_discount", "discountAmount": "1000", "productIds": []string{coffee}, "stackable": true, "stackGroup": "OTHER", "priority": 60}))
	for _, p := range []string{s1, s2, s3} {
		om.Must(200, "POST", "/api/v1/commercial/promotions/"+p+":activate", nil)
	}
	ev := om.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", map[string]any{"lines": []map[string]any{{"productId": coffee, "unitPrice": "40000"}}}).JSON()
	// 20% of 40.000 = 8.000, then 10% of the remaining 32.000 = 3.200; the other group does not stack
	if len(ev["applied"].([]any)) != 2 || !dec(ev["discount"]).Equal(decimal.NewFromInt(11200)) {
		t.Fatalf("stacking: %v", ev)
	}
	pcPolicy(t, sa, "Promotion Policies", "commercial.promotion", map[string]any{"requireApproval": true, "selection": "priority", "maxStackedPercent": "25",
		"offlineTolerance": "0", "codeCheckPerMinute": 5, "offlineCacheHours": 12})
	ev = om.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", map[string]any{"lines": []map[string]any{{"productId": coffee, "unitPrice": "40000"}}}).JSON()
	if !dec(ev["discount"]).Equal(decimal.NewFromInt(10000)) {
		t.Fatalf("stacking ceiling 25%%: %v", ev)
	}
	// best price selection
	pcPolicy(t, sa, "Promotion Policies", "commercial.promotion", map[string]any{"requireApproval": true, "selection": "best_price", "maxStackedPercent": "100",
		"offlineTolerance": "0", "codeCheckPerMinute": 5, "offlineCacheHours": 12})
	ev = om.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", map[string]any{"lines": []map[string]any{{"key": "j", "productId": juice, "quantity": "2",
		"unitPrice": "50000"}}}).JSON()
	if ap := ev["applied"].([]any); len(ap) != 1 || ap[0].(map[string]any)["promotionId"] != amtID {
		t.Fatalf("best price (equal discounts keep the priority): %v", ev)
	}
	pcPolicy(t, sa, "Promotion Policies", "commercial.promotion", map[string]any{"requireApproval": true, "selection": "priority", "maxStackedPercent": "100",
		"offlineTolerance": "0", "codeCheckPerMinute": 5, "offlineCacheHours": 24})
	for _, p := range []string{s1, s2, s3} {
		om.Must(200, "POST", "/api/v1/commercial/promotions/"+p+":deactivate", map[string]any{"reason": "end of test"})
	}
	om.Must(409, "POST", "/api/v1/commercial/promotions/"+s3+":deactivate", map[string]any{})

	// ── promo codes: general with limits, personal, budget (FR-PRM-04/05) ─
	codePromo := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-CODE" + sfx, "name": "Cake 25% with code",
		"promoType": "promo_code", "discountPercent": "25", "productIds": []string{cake}, "minPurchase": "50000", "budgetAmount": "30000",
		"maxPerCustomer": 2}))
	gen := idOf(om.Must(201, "POST", "/api/v1/commercial/promo-codes", map[string]any{"promotionId": codePromo, "code": "cake" + sfx, "maxUses": 1}))
	om.Must(200, "PATCH", "/api/v1/commercial/promo-codes/"+gen, map[string]any{"maxUsesPerCustomer": 1})
	om.Must(422, "POST", "/api/v1/commercial/promo-codes", map[string]any{"promotionId": codePromo, "code": "x"})
	drop := idOf(om.Must(201, "POST", "/api/v1/commercial/promo-codes", map[string]any{"promotionId": codePromo, "code": "DROP" + sfx}))
	om.Must(204, "DELETE", "/api/v1/commercial/promo-codes/"+drop, nil)
	if c := om.Must(200, "POST", "/api/v1/commercial/promo-codes:check", map[string]any{"code": "CAKE" + sfx}).JSON(); c["valid"] != false {
		t.Fatalf("code of a draft promotion: %v", c)
	}
	om.Must(200, "POST", "/api/v1/commercial/promotions/"+codePromo+":activate", nil)
	if c := om.Must(200, "POST", "/api/v1/commercial/promo-codes:check", map[string]any{"code": "cake" + sfx, "amount": "60000", "channel": "pos"}).JSON(); c["valid"] != true || c["usesLeft"].(float64) != 1 {
		t.Fatalf("code check: %v", c)
	}
	// without the code no discount; with it 25%
	cord := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift,
		"lines": []map[string]any{{"productId": cake, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	if !dec(cord["total"]).Equal(decimal.NewFromInt(60000)) {
		t.Fatalf("no code: %v", cord)
	}
	ap := cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(cord["id"])+":apply-promotion", map[string]any{"promoCode": "cake" + sfx}).JSON()
	if o := ap["order"].(map[string]any); !dec(o["total"]).Equal(decimal.NewFromInt(45000)) {
		t.Fatalf("code applied: %v", ap)
	}
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(cord["id"])+":pay", map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "cash"}}}, "Idempotency-Key", newKey())
	if pcEvents(t, "commercial.promotion_applied", "promoCode", "CAKE"+sfx) != 1 {
		t.Fatal("promo code redemption event")
	}
	// the single-use code is used up
	used := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift, "promoCodes": []string{"CAKE" + sfx},
		"lines": []map[string]any{{"productId": cake, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	if !dec(used["total"]).Equal(decimal.NewFromInt(60000)) {
		t.Fatalf("used-up code: %v", used)
	}
	// personal codes: only for their customer; the budget caps the discount
	cust1 := customer(t, sa, "PCC1"+sfx, "Promo Customer One", map[string]any{"email": "pc1" + sfx + "@promo.test"})
	cust2 := customer(t, sa, "PCC2"+sfx, "Promo Customer Two", nil)
	g := om.Must(201, "POST", "/api/v1/commercial/promo-codes:generate", map[string]any{"promotionId": codePromo, "customerIds": []string{cust1, cust2},
		"prefix": "PC", "length": 6, "maxUses": 1, "campaignRef": "TEST"}).JSON()
	if g["count"].(float64) != 2 {
		t.Fatalf("generated: %v", g)
	}
	om.Must(201, "POST", "/api/v1/commercial/promo-codes:generate", map[string]any{"promotionId": codePromo, "count": 3, "length": 8})
	om.Must(422, "POST", "/api/v1/commercial/promo-codes:generate", map[string]any{"promotionId": codePromo, "count": 0})
	c1 := str(g["codes"].([]any)[0].(map[string]any)["code"])
	if c := om.Must(200, "POST", "/api/v1/commercial/promo-codes:check", map[string]any{"code": c1, "customerId": cust2}).JSON(); c["valid"] != false {
		t.Fatalf("personal code of another customer: %v", c)
	}
	pord := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift, "customerId": cust1,
		"promoCodes": []string{c1}, "lines": []map[string]any{{"productId": cake, "quantity": "2"}}}, "Idempotency-Key", newKey()).JSON()
	// 25% of 120.000 = 30.000, the budget left after 15.000 is 15.000
	if !dec(pord["total"]).Equal(decimal.NewFromInt(105000)) {
		t.Fatalf("budget caps the discount: %v", pord)
	}
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(pord["id"])+":pay", map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "cash"}}}, "Idempotency-Key", newKey())
	pp := om.Must(200, "GET", "/api/v1/commercial/promotions/"+codePromo, nil).JSON()
	if !dec(pp["usedAmount"]).Equal(decimal.NewFromInt(30000)) || pp["usedCount"].(float64) != 2 {
		t.Fatalf("usage counters: %v", pp)
	}
	if rs := om.Must(200, "GET", "/api/v1/commercial/promotions/"+codePromo+"/redemptions", nil).Items(); len(rs) != 2 {
		t.Fatalf("redemption ledger: %v", rs)
	}
	c2 := str(g["codes"].([]any)[1].(map[string]any)["code"])
	if c := om.Must(200, "POST", "/api/v1/commercial/promotions:evaluate", map[string]any{"customerId": cust2, "promoCodes": []string{c2},
		"lines": []map[string]any{{"productId": cake, "unitPrice": "60000"}}}).JSON(); !dec(c["discount"]).IsZero() || len(c["rejected"].([]any)) == 0 {
		t.Fatalf("budget used up: %v", c)
	}

	// ── member discount by membership segment, booking channel and snapshot ─
	member := memberCustomer(t, sa)
	mdisc := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-MEM" + sfx, "name": "Members 15% court",
		"promoType": "member_discount", "discountPercent": "15", "segments": []string{"member"}, "serviceTypes": []string{"sport_court"},
		"itemRefs": []string{"PCCOURT" + sfx}}))
	om.Must(200, "POST", "/api/v1/commercial/promotions/"+mdisc+":activate", nil)
	rule(t, sa, map[string]any{"code": "PCR" + sfx, "name": "Court " + sfx, "serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "unit": "hour",
		"price": "200000", "taxCodes": []string{"PC" + sfx}})
	start := time.Now().In(loc).AddDate(0, 0, 3)
	start = time.Date(start.Year(), start.Month(), start.Day(), 10, 0, 0, 0, loc)
	lp := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "start": rfc(start), "end": rfc(start.Add(time.Hour)),
		"segment": "member"})
	if !dec(lp["discount"]).Equal(decimal.NewFromInt(30000)) || !dec(total(lp)).Equal(decimal.NewFromInt(170000)) {
		t.Fatalf("member discount on a booking price: %v", lp)
	}
	if lp := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "start": rfc(start), "end": rfc(start.Add(time.Hour)),
		"segment": "non_member"}); !dec(lp["discount"]).IsZero() {
		t.Fatalf("not for non-members: %v", lp)
	}
	snap := sa.Must(200, "POST", "/api/v1/commercial/pricing:resolve-line", map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx,
		"start": rfc(start), "end": rfc(start.Add(time.Hour)), "segment": "member", "customerId": member, "persist": true}).JSON()
	var snapPromos, applied int
	sysQueryRow(t, inst, `SELECT jsonb_array_length(promotions) FROM commercial.pricing_snapshots WHERE id = $1`, []any{mustUUID(str(snap["snapshotId"]))}, &snapPromos)
	sysQueryRow(t, inst, `SELECT count(*) FROM commercial.promotion_redemptions WHERE source_type = 'pricing_snapshot' AND source_id = $1 AND status = 'applied'`,
		[]any{mustUUID(str(snap["snapshotId"]))}, &applied)
	if snapPromos != 1 || applied != 1 {
		t.Fatalf("snapshot records the promotion (%d) and its redemption (%d)", snapPromos, applied)
	}
	if lp := sa.Must(200, "POST", "/api/v1/commercial/pricing:resolve-line", map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx,
		"start": rfc(start), "end": rfc(start.Add(time.Hour)), "segment": "member", "noPromotions": true}).JSON(); !dec(lp["discount"]).IsZero() {
		t.Fatalf("list price without promotions: %v", lp)
	}

	// ── FR-PRC-P3-01: Corporate Rate, Holiday Rate, Peak / Off-Peak Rate ─
	corp := idOf(sa.Must(201, "POST", "/api/v1/crm/corporate-accounts", map[string]any{"code": "PCCORP" + sfx, "name": "PT Promo " + sfx}))
	rule(t, sa, map[string]any{"code": "PCRC" + sfx, "name": "Court contract", "serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "unit": "hour",
		"price": "150000", "corporateAccountId": corp, "taxCodes": []string{"PC" + sfx}})
	if lp := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "start": rfc(start), "end": rfc(start.Add(time.Hour)),
		"corporateAccountId": corp, "noPromotions": true}); !dec(lp["unitPrice"]).Equal(decimal.NewFromInt(150000)) {
		t.Fatalf("corporate rate: %v", lp)
	}
	hday := start.AddDate(0, 0, 150)
	sa.Must(201, "POST", "/api/v1/platform/calendar-days", map[string]any{"day": hday.Format("2006-01-02"), "kind": "public_holiday", "name": "Promo holiday " + sfx})
	rule(t, sa, map[string]any{"code": "PCRH" + sfx, "name": "Court holiday", "serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "unit": "hour",
		"price": "250000", "holiday": true, "taxCodes": []string{"PC" + sfx}})
	if lp := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "start": rfc(hday), "end": rfc(hday.Add(time.Hour)),
		"noPromotions": true}); !dec(lp["unitPrice"]).Equal(decimal.NewFromInt(250000)) {
		t.Fatalf("holiday rate: %v", lp)
	}
	pday := start.AddDate(0, 0, 151)
	pcPolicy(t, sa, "Pricing Policies", "commercial.pricing", map[string]any{"peakPeriods": []map[string]any{{"name": "Lebaran", "from": pday.Format("2006-01-02"),
		"to": pday.Format("2006-01-02")}}, "peakHours": []any{}, "holidaysArePeak": false, "manualDiscountLimits": map[string]any{"cashier": "5", "outlet_manager": "20"}})
	// manual discount limit per role (FR-POL-P3-02): a cashier up to 5%, the outlet manager up to 20%
	dord := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift,
		"lines": []map[string]any{{"productId": cake, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	dline := str(pcLine(dord, cake)["id"])
	cashier.Must(403, "POST", "/api/v1/commercial/orders/"+str(dord["id"])+"/lines/"+dline+":discount", map[string]any{"percent": "8", "reason": "regular"})
	om.Must(200, "POST", "/api/v1/commercial/orders/"+str(dord["id"])+"/lines/"+dline+":discount", map[string]any{"percent": "15", "reason": "regular"})
	rule(t, sa, map[string]any{"code": "PCRP" + sfx, "name": "Court peak", "serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "unit": "hour",
		"price": "300000", "peak": true, "taxCodes": []string{"PC" + sfx}})
	if lp := price(t, sa, map[string]any{"serviceType": "sport_court", "itemRef": "PCCOURT" + sfx, "start": rfc(pday), "end": rfc(pday.Add(time.Hour)),
		"noPromotions": true}); !dec(lp["unitPrice"]).Equal(decimal.NewFromInt(300000)) {
		t.Fatalf("peak rate: %v", lp)
	}
	pcPolicy(t, sa, "Pricing Policies", "commercial.pricing", map[string]any{"peakPeriods": []any{}, "peakHours": []any{}, "holidaysArePeak": false,
		"manualDiscountLimits": map[string]any{}})

	// ── website & member app (FR-WEB-P3-01/07, FR-APP-P3-02) ────────────
	pub := anon(t, inst)
	webPromo := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-WEB" + sfx, "name": "Website juice deal",
		"promoType": "period", "discountPercent": "5", "productIds": []string{juice}, "validFrom": time.Now().In(loc).Format("2006-01-02"),
		"validTo": time.Now().In(loc).AddDate(0, 1, 0).Format("2006-01-02"), "channels": []string{"website", "member_app"}, "priority": 90}))
	om.Must(200, "POST", "/api/v1/commercial/promotions/"+webPromo+":activate", nil)
	if l := pub.Must(200, "GET", "/api/v1/public/promotions?propertyId="+inst.Main.String(), nil).Items(); !containsID(l, webPromo) || containsID(l, codePromo) {
		t.Fatalf("public promotions: %v", l)
	}
	mc := roleUser(t, inst, "member")
	mem := memberCustomer(t, sa)
	g2 := om.Must(201, "POST", "/api/v1/commercial/promo-codes:generate", map[string]any{"promotionId": codePromo, "customerIds": []string{mem}}).JSON()
	offers := mc.Must(200, "GET", "/api/v1/member/offers", nil).JSON()
	if !containsID(asMaps(offers["promotions"]), webPromo) {
		t.Fatalf("member offers: %v", offers)
	}
	myCode := false
	for _, c := range offers["promoCodes"].([]any) {
		myCode = myCode || c.(map[string]any)["code"] == g2["codes"].([]any)[0].(map[string]any)["code"]
	}
	if !myCode {
		t.Fatalf("personal promo code in Offers: %v", offers["promoCodes"])
	}

	// ── CRM campaign personal codes (crm.campaign_sent, FR-CMP-05) ──────
	camp := uuid.New()
	payload, _ := json.Marshal(map[string]any{"campaignId": camp, "code": "CMP" + sfx, "promoMode": "unique", "promoCode": "CAKE" + sfx,
		"recipients": []map[string]any{{"customerId": cust1, "promoCode": "CMP" + sfx + "-A1"}, {"customerId": cust2, "promoCode": "bad code"}}})
	sysExec(t, inst, `INSERT INTO platform.outbox (id, event_type, aggregate_type, aggregate_id, property_id, payload) VALUES ($1,'crm.campaign_sent','crm.campaign',$2,$3,$4)`,
		uuid.New(), camp, inst.Main, payload)
	pcDispatch(t, "campaign codes", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM commercial.promo_codes WHERE campaign_id = $1 AND promotion_id = $2`, []any{camp, mustUUID(codePromo)}, &n)
		return n == 2
	})
	var personal string
	sysQueryRow(t, inst, `SELECT code FROM commercial.promo_codes WHERE campaign_id = $1 AND customer_id = $2`, []any{camp, mustUUID(cust1)}, &personal)
	if personal != "CMP"+sfx+"-A1" {
		t.Fatalf("campaign code: %s", personal)
	}

	// ── reports, dashboard, export (FR-RPT-P3-03/05, FR-INT-P3-05) ──────
	from, to := time.Now().In(loc).AddDate(0, 0, -30).Format("2006-01-02"), time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/commercial.promotion_performance?params[from]="+from+"&params[to]="+to, nil).JSON()
	found := false
	for _, r := range rep["rows"].([]any) {
		m := r.(map[string]any)
		if m["code"] == "PC-CODE"+sfx {
			found = dec(m["discount"]).Equal(decimal.NewFromInt(30000)) && m["redemptions"].(float64) == 2
		}
	}
	if !found {
		t.Fatalf("promotion performance: %v", rep["rows"])
	}
	dash := sa.Must(200, "GET", "/api/v1/reporting/dashboards/commercial-performance?from="+from+"&to="+to, nil).JSON()
	keys := map[string]any{}
	for _, k := range dash["kpis"].([]any) {
		keys[str(k.(map[string]any)["key"])] = k.(map[string]any)["value"]
	}
	for _, k := range []string{"total_sales", "package_sales", "promotion_usage", "discount_cost", "voucher_sold", "voucher_redeemed", "average_transaction"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("commercial performance misses %s: %v", k, keys)
		}
	}
	if !dec(keys["discount_cost"]).IsPositive() {
		t.Fatalf("discount cost: %v", keys)
	}
	exp := accountingExport(t, sa)
	if !strings.Contains(exp, "promotion_discount") {
		t.Fatalf("accounting export rows: %s", exp)
	}

	// ── expiry, archive and the public code check rate limit ────────────
	old := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-OLD" + sfx, "name": "Old", "promoType": "percent_discount",
		"discountPercent": "5", "productIds": []string{tea}}))
	om.Must(200, "POST", "/api/v1/commercial/promotions/"+old+":activate", nil)
	sysExec(t, inst, `UPDATE commercial.promotions SET valid_from = current_date - 10, valid_to = current_date - 2 WHERE id = $1`, mustUUID(old))
	if _, err := inst.App.Promo.Module.ExpirePromotions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := om.Must(200, "GET", "/api/v1/commercial/promotions/"+old, nil).JSON(); p["status"] != "expired" {
		t.Fatalf("expired promotion: %v", p)
	}
	draft := idOf(om.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PC-DEL" + sfx, "name": "Draft", "promoType": "percent_discount",
		"discountPercent": "5"}))
	om.Must(204, "DELETE", "/api/v1/commercial/promotions/"+draft, nil)
	for _, p := range []string{hhID, pctID, amtID, big, codePromo, mdisc, webPromo} {
		om.Must(200, "POST", "/api/v1/commercial/promotions/"+p+":deactivate", map[string]any{"reason": "end of test"})
	}
	limited := false
	for i := 0; i < 8 && !limited; i++ {
		r := pub.Do("POST", "/api/v1/public/promo-codes:check", map[string]any{"propertyId": inst.Main.String(), "code": "NOPE" + sfx})
		switch r.Status {
		case 200:
			if r.JSON()["valid"] != false {
				t.Fatalf("unknown code: %s", r)
			}
		case 429:
			limited = true
		default:
			t.Fatalf("public check: %s", r)
		}
	}
	if !limited {
		t.Fatal("public promo code check is not rate limited (Promotion Policies: 5 per minute)")
	}
}

// pcWeekdays returns n distinct club weekdays from today+minDays.
func pcWeekdays(minDays, n int) []string {
	d := time.Now().In(clubLoc(inst)).AddDate(0, 0, minDays)
	var out []string
	for len(out) < n {
		if isWeekday(d.Weekday()) {
			out = append(out, d.Format("2006-01-02"))
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// pkgComponents indexes the components of a package booking by name.
func pkgComponents(b map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range b["components"].([]any) {
		m := c.(map[string]any)
		out[str(m["name"])] = m
	}
	return out
}

// EP-11 (FR-PKG-01..09, FR-APP-P3-05, FR-WEB-P3-04, FR-QUO-06) acceptance:
// Stay & Golf on a day whose tee times are full is refused as a whole with
// no bungalow held; a Rp3.500.000 package is allocated to bungalow, green
// fee, caddy fee (liability) and breakfast for exactly its price. Also:
// versions, availability, add-ons, daily quota, promotions on packages,
// consumption with the BOM (K6), check-in and voucher redemption consuming
// components, cancellation fee and release, website (held until paid) and
// member app bookings, hold and component expiry, the quotation conversion
// with its payment schedule, reports and the accounting export.
func TestP3CommercialPackages(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	rs := roleUser(t, inst, "reservation_staff")
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e6)
	loc := clubLoc(inst)
	days := pcWeekdays(20, 8)

	sa.Must(201, "POST", "/api/v1/commercial/tax-service-rules", map[string]any{"code": "PK" + sfx, "name": "PPN nett pkg " + sfx, "kind": "tax", "ratePercent": "11",
		"basis": "net_amount", "pricingMode": "nett", "effectiveFrom": past()})
	// a night resource type with one bungalow, a golf course with its tee sheet, a breakfast with its recipe, a F&B voucher
	rt := "pkb" + sfx
	sa.Must(201, "POST", "/api/v1/reservation/resource-types", map[string]any{"code": rt, "name": "Package bungalow " + sfx, "businessLine": "stay",
		"reservationModel": "night", "allocationMode": "exclusive", "checkInTime": "14:00", "checkOutTime": "12:00"})
	bgl := resourceOf(t, sa, "PKB-01-"+sfx, rt, nil, nil)
	gc := setupGolfCourse(t, sa, "PG"+sfx[:4])
	var courseCode string
	sysQueryRow(t, inst, `SELECT code FROM golf.courses WHERE id = $1`, []any{mustUUID(gc.Course)}, &courseCode)
	day := days[0]
	teeTimes(t, sa, gc.Course, day)
	uom := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "PKU" + sfx, "name": "Portion " + sfx}))
	egg := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "PKEGG" + sfx, "name": "Egg " + sfx, "baseUomId": uom, "standardCost": "2500"}))
	bf := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "PKBF" + sfx, "name": "Breakfast " + sfx, "productType": "food",
		"price": "250000"}))
	recipe := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "PKR" + sfx, "name": "Breakfast " + sfx, "recipeType": "menu",
		"productId": bf, "yieldQuantity": "1", "yieldUomId": uom}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": recipe, "itemId": egg, "quantity": "2", "uomId": uom})
	sa.Must(201, "POST", "/api/v1/commercial/voucher-types", map[string]any{"code": "PKV" + sfx, "name": "F&B voucher " + sfx, "kind": "value",
		"category": "fnb", "unit": "rupiah", "faceValue": "100000", "price": "100000", "validityMonths": 3})

	// ── FR-PKG-01/02/05/09: package, components, publish, versions ──────
	sa.Must(422, "POST", "/api/v1/commercial/packages", map[string]any{"code": "PKX" + sfx, "name": "Bad", "price": "1", "status": "active"})
	pkg := sa.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "SG" + sfx, "name": "Stay & Golf " + sfx, "packageType": "stay_golf",
		"pricingMode": "fixed", "price": "3400000", "minPax": 2, "maxPax": 2, "nights": 1, "startTime": "07:00", "taxMode": "nett", "taxCodes": []string{"PK" + sfx},
		"allocationMethod": "standalone", "cancellationHours": 24 * 365, "cancellationFeePercent": "10", "channels": []string{"back_office", "website", "member_app",
			"quotation"}}).JSON()
	pid := str(pkg["id"])
	sa.Must(422, "POST", "/api/v1/commercial/packages/"+pid+":publish", nil)
	comp := func(body map[string]any) string {
		body["packageId"] = pid
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/package-components", body))
	}
	sa.Must(422, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pid, "componentType": "reservation", "name": "No resource"})
	sa.Must(422, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pid, "componentType": "voucher", "name": "Bad voucher", "refCode": "NOPE"})
	comp(map[string]any{"seq": 1, "componentType": "reservation", "name": "Bungalow", "resourceTypeCode": rt, "standalonePrice": "1500000",
		"revenueComponent": "bungalow", "businessLine": "stay"})
	comp(map[string]any{"seq": 2, "componentType": "tee_time", "name": "Green Fee", "refCode": courseCode, "perPax": true, "durationMinutes": 30,
		"standalonePrice": "1200000", "revenueComponent": "green_fee", "businessLine": "golf"})
	caddy := comp(map[string]any{"seq": 3, "componentType": "service", "name": "Caddy Fee", "standalonePrice": "300000", "revenueComponent": "caddy_fee",
		"businessLine": "golf", "liability": true})
	comp(map[string]any{"seq": 4, "componentType": "fnb", "name": "Breakfast", "productId": bf, "recipeId": recipe, "perPax": true, "dayOffset": 1,
		"startTime": "07:00", "standalonePrice": "250000", "revenueComponent": "fnb", "businessLine": "pos"})
	sa.Must(422, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": pid, "componentType": "fnb", "name": "Dinner", "optional": true})
	dinner := comp(map[string]any{"seq": 5, "componentType": "fnb", "name": "Dinner (add-on)", "optional": true, "addonPrice": "200000", "perPax": true,
		"revenueComponent": "fnb", "businessLine": "pos"})
	scratch := comp(map[string]any{"seq": 9, "componentType": "service", "name": "Scratch"})
	sa.Must(200, "PATCH", "/api/v1/commercial/package-components/"+caddy, map[string]any{"standalonePrice": "300000", "liability": true})
	sa.Must(204, "DELETE", "/api/v1/commercial/package-components/"+scratch, nil)
	if p := sa.Must(200, "POST", "/api/v1/commercial/packages/"+pid+":publish", nil).JSON(); p["version"].(float64) != 1 || p["status"] != "active" {
		t.Fatalf("published v1: %v", p)
	}
	sa.Must(200, "PATCH", "/api/v1/commercial/packages/"+pid, map[string]any{"price": "3500000"})
	if p := sa.Must(200, "POST", "/api/v1/commercial/packages/"+pid+":publish", nil).JSON(); p["version"].(float64) != 2 || p["price"] != "3500000" {
		t.Fatalf("published v2: %v", p)
	}
	if v := sa.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/versions", nil).Items(); len(v) != 2 {
		t.Fatalf("package history: %v", v)
	}
	tmp := idOf(sa.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "PKT" + sfx, "name": "Temporary", "price": "100"}))
	sa.Must(204, "DELETE", "/api/v1/commercial/packages/"+tmp, nil)

	// a golf-only package (tee time + F&B voucher) with a daily quota
	golf := idOf(sa.Must(201, "POST", "/api/v1/commercial/packages", map[string]any{"code": "GD" + sfx, "name": "Golf Day " + sfx, "packageType": "golf_day",
		"pricingMode": "per_pax", "price": "1000000", "minPax": 1, "startTime": "07:00", "taxMode": "nett", "taxCodes": []string{"PK" + sfx}, "dailyQuota": 1}))
	sa.Must(201, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": golf, "componentType": "tee_time", "name": "Tee Time",
		"refCode": courseCode, "perPax": true, "durationMinutes": 30, "standalonePrice": "900000", "revenueComponent": "green_fee", "businessLine": "golf"})
	sa.Must(201, "POST", "/api/v1/commercial/package-components", map[string]any{"packageId": golf, "componentType": "voucher", "name": "F&B Voucher",
		"refCode": "PKV" + sfx, "standalonePrice": "100000", "revenueComponent": "fnb", "businessLine": "pos"})
	sa.Must(200, "POST", "/api/v1/commercial/packages/"+golf+":publish", nil)

	cust := customer(t, sa, "PKC"+sfx, "Package Guest "+sfx, map[string]any{"email": "pk" + sfx + "@pkg.test", "phone": "+62813" + sfx})
	av := rs.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/availability?date="+day+"&pax=2", nil).Items()
	if len(av) != 1 || av[0]["available"] != true || len(av[0]["components"].([]any)) != 4 {
		t.Fatalf("availability: %v", av)
	}

	// ── AC 1: tee times full → the whole Stay & Golf is refused, no bungalow held ─
	fill := rs.Must(201, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": golf, "startDate": day, "pax": 12, "customerId": cust},
		"Idempotency-Key", newKey()).JSON()
	if fill["status"] != "confirmed" {
		t.Fatalf("golf day for 12: %v", fill)
	}
	if av := rs.Must(200, "GET", "/api/v1/commercial/packages/"+pid+"/availability?date="+day+"&pax=2", nil).Items(); av[0]["available"] != false {
		t.Fatalf("full tee times: %v", av)
	}
	full := rs.Do("POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": day, "pax": 2, "customerId": cust},
		"Idempotency-Key", newKey())
	if full.Status != 409 || !strings.Contains(string(full.Body), "Green Fee") {
		t.Fatalf("refused as a whole: %s", full)
	}
	var held int
	sysQueryRow(t, inst, `SELECT count(*) FROM reservation.allocations WHERE resource_id = $1 AND status IN ('held', 'confirmed')`, []any{mustUUID(bgl)}, &held)
	if held != 0 {
		t.Fatalf("a bungalow stays held after the refusal: %d", held)
	}
	// daily quota (FR-PKG-03): one golf day per date
	if r := rs.Do("POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": golf, "startDate": day, "pax": 1, "guestName": "Late golfer"},
		"Idempotency-Key", newKey()); r.Status != 409 || !strings.Contains(string(r.Body), "package_sold_out") {
		t.Fatalf("daily quota: %s", r)
	}
	cx := rs.Must(200, "POST", "/api/v1/commercial/package-bookings/"+str(fill["id"])+":cancel", map[string]any{"reason": "group cancelled"}).JSON()
	if cx["booking"].(map[string]any)["status"] != "cancelled" || cx["released"].(float64) < 1 {
		t.Fatalf("cancel releases the tee times: %v", cx)
	}
	rs.Must(422, "POST", "/api/v1/commercial/package-bookings/"+str(fill["id"])+":cancel", map[string]any{})

	// ── AC 2: Rp3.500.000 allocated per component, exactly the price ─────
	key := newKey()
	b := rs.Must(201, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": day, "pax": 2, "customerId": cust},
		"Idempotency-Key", key).JSON()
	bid := str(b["id"])
	if b["status"] != "confirmed" || !dec(b["total"]).Equal(decimal.NewFromInt(3500000)) || b["packageVersion"].(float64) != 2 {
		t.Fatalf("Stay & Golf: %v", b)
	}
	sum := decimal.Zero
	cs := pkgComponents(b)
	for _, c := range cs {
		sum = sum.Add(dec(c["allocatedTotal"]))
		if c["allocationRef"] == nil && (c["componentType"] == "reservation" || c["componentType"] == "tee_time") {
			t.Fatalf("not allocated: %v", c)
		}
	}
	if !sum.Equal(decimal.NewFromInt(3500000)) || len(cs) != 4 || cs["Caddy Fee"]["liability"] != true || !dec(cs["Bungalow"]["allocatedTotal"]).IsPositive() {
		t.Fatalf("revenue allocation %s: %v", sum, cs)
	}
	if pcEvents(t, "commercial.package_booked", "bookingId", bid) != 1 {
		t.Fatal("commercial.package_booked")
	}
	if l := rs.Must(200, "GET", "/api/v1/commercial/package-bookings?filter[packageId]="+pid, nil).Items(); !containsID(l, bid) {
		t.Fatalf("booking list: %v", l)
	}
	if b["folio"] == nil {
		t.Fatalf("folio: %v", b)
	}

	// ── consumption with the BOM (FR-PKG-06/07, K6) ─────────────────────
	breakfast := str(cs["Breakfast"]["id"])
	ck := newKey()
	c1 := rs.Must(201, "POST", "/api/v1/commercial/package-bookings/"+bid+"/consumption", map[string]any{"bookingComponentId": breakfast, "quantity": "1",
		"reference": "table 4"}, "Idempotency-Key", ck).JSON()
	if c1["componentStatus"] != "unused" || len(c1["consumption"].([]any)) != 1 || c1["duplicate"] == true {
		t.Fatalf("first breakfast: %v", c1)
	}
	rs.Must(422, "POST", "/api/v1/commercial/package-bookings/"+bid+"/consumption", map[string]any{"bookingComponentId": breakfast, "quantity": "5"},
		"Idempotency-Key", newKey())
	c2 := rs.Must(201, "POST", "/api/v1/commercial/package-bookings/"+bid+"/consumption", map[string]any{"bookingComponentId": breakfast},
		"Idempotency-Key", newKey()).JSON()
	if c2["componentStatus"] != "consumed" ||
		!dec(c1["revenue"].(map[string]any)["total"]).Add(dec(c2["revenue"].(map[string]any)["total"])).Equal(dec(cs["Breakfast"]["allocatedTotal"])) {
		t.Fatalf("breakfast consumed with its allocated revenue: %v %v", c2, cs["Breakfast"])
	}
	if pcEvents(t, "commercial.package_consumed", "bookingComponentId", breakfast) != 2 {
		t.Fatal("commercial.package_consumed (K6)")
	}
	if l := rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+bid+"/consumption", nil).Items(); len(l) != 2 {
		t.Fatalf("consumption log: %v", l)
	}
	// the bungalow is consumed when the reservation checks in (reservation.checked_in)
	resv := str(cs["Bungalow"]["allocationId"])
	sa.Must(200, "POST", "/api/v1/reservation/reservations/"+resv+":check-in", nil)
	pcDispatch(t, "bungalow consumed at check-in", func() bool {
		return pkgComponents(rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+bid, nil).JSON())["Bungalow"]["status"] == "consumed"
	})

	// ── FR-PKG-08: cancellation of the unused part with the policy fee ───
	cr := rs.Must(200, "POST", "/api/v1/commercial/package-bookings/"+bid+":cancel", map[string]any{"reason": "rain"}).JSON()
	after := pkgComponents(cr["booking"].(map[string]any))
	unused := dec(cs["Green Fee"]["allocatedTotal"]).Add(dec(cs["Caddy Fee"]["allocatedTotal"]))
	if !dec(cr["fee"]).Equal(unused.Mul(decimal.NewFromInt(10)).Div(decimal.NewFromInt(100)).Round(0)) || after["Green Fee"]["status"] != "cancelled" ||
		after["Bungalow"]["status"] != "consumed" || after["Breakfast"]["status"] != "consumed" {
		t.Fatalf("partial cancellation: %v", cr)
	}

	// ── promotions on packages and the voucher component ────────────────
	pp := idOf(sa.Must(201, "POST", "/api/v1/commercial/promotions", map[string]any{"code": "PKP" + sfx, "name": "Golf day 10%", "promoType": "promo_code",
		"discountPercent": "10", "serviceTypes": []string{"package"}, "itemRefs": []string{"GD" + sfx}}))
	sa.Must(200, "POST", "/api/v1/commercial/promotions/"+pp+":activate", nil)
	sa.Must(201, "POST", "/api/v1/commercial/promo-codes", map[string]any{"promotionId": pp, "code": "GOLF" + sfx})
	gday := days[1]
	teeTimes(t, sa, gc.Course, gday)
	gb := rs.Must(201, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": golf, "startDate": gday, "pax": 2, "customerId": cust,
		"promoCodes": []string{"golf" + sfx}}, "Idempotency-Key", newKey()).JSON()
	if !dec(gb["discountTotal"]).Equal(decimal.NewFromInt(200000)) || !dec(gb["total"]).Equal(decimal.NewFromInt(1800000)) {
		t.Fatalf("promotion on a package: %v", gb)
	}
	if pr := gb["promotions"].([]any); len(pr) != 1 || pr[0].(map[string]any)["status"] != "redeemed" {
		t.Fatalf("package promotion redeemed at confirmation: %v", pr)
	}
	vc := pkgComponents(gb)["F&B Voucher"]
	code := strings.TrimSpace(strings.Split(str(vc["allocationRef"]), ",")[0])
	sa.Must(200, "POST", "/api/v1/commercial/vouchers:redeem", map[string]any{"code": code, "quantity": "50000", "serviceType": "pos"},
		"Idempotency-Key", newKey())
	pcDispatch(t, "voucher consumed", func() bool {
		return pkgComponents(rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+str(gb["id"]), nil).JSON())["F&B Voucher"]["status"] == "consumed"
	})
	// an add-on chosen; an included component is not an add-on; a guest is required
	sday := days[2]
	teeTimes(t, sa, gc.Course, sday)
	ab := rs.Must(201, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": sday, "pax": 2, "guestName": "Add-on Guest",
		"addons": []string{dinner}}, "Idempotency-Key", newKey()).JSON()
	if !dec(ab["total"]).Equal(decimal.NewFromInt(3900000)) || len(ab["components"].([]any)) != 5 {
		t.Fatalf("add-on: %v", ab)
	}
	rs.Must(422, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": days[3], "pax": 2,
		"guestName": "x", "addons": []string{caddy}}, "Idempotency-Key", newKey())
	rs.Must(422, "POST", "/api/v1/commercial/package-bookings", map[string]any{"packageId": pid, "startDate": days[3], "pax": 2},
		"Idempotency-Key", newKey())

	// ── website (FR-WEB-P3-01/04): held until paid, then confirmed ───────
	pub := anon(t, inst)
	if l := pub.Must(200, "GET", "/api/v1/public/packages?propertyId="+inst.Main.String(), nil).Items(); !containsID(l, pid) {
		t.Fatalf("public packages: %v", l)
	}
	wday := days[4]
	teeTimes(t, sa, gc.Course, wday)
	det := pub.Must(200, "GET", "/api/v1/public/packages/sg"+sfx+"?propertyId="+inst.Main.String()+"&date="+wday+"&pax=2", nil).JSON()
	if len(det["availability"].([]any)) != 1 || det["availability"].([]any)[0].(map[string]any)["available"] != true {
		t.Fatalf("public package detail: %v", det)
	}
	wb := pub.Must(201, "POST", "/api/v1/public/package-bookings", map[string]any{"propertyId": inst.Main.String(), "packageCode": "SG" + sfx,
		"startDate": wday, "pax": 2, "guest": map[string]any{"name": "Web Guest " + sfx, "email": "web" + sfx + "@pkg.test", "phone": "+62817" + sfx}}).JSON()
	web := wb["booking"].(map[string]any)
	if web["status"] != "pending" || web["holdExpiresAt"] == nil || web["channel"] != "website" {
		t.Fatalf("website booking held: %v", web)
	}
	sa.Must(201, "POST", "/api/v1/billing/payments", map[string]any{"folioId": web["folioId"], "methodType": "cash", "channel": "venue"}, "Idempotency-Key", newKey())
	pcDispatch(t, "website package confirmed when paid", func() bool {
		return rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+str(web["id"]), nil).JSON()["status"] == "confirmed"
	})
	// an unpaid hold expires and releases its allocations
	xday := days[5]
	teeTimes(t, sa, gc.Course, xday)
	xb := pub.Must(201, "POST", "/api/v1/public/package-bookings", map[string]any{"propertyId": inst.Main.String(), "packageCode": "SG" + sfx,
		"startDate": xday, "pax": 2, "guest": map[string]any{"name": "Late Payer " + sfx, "phone": "+62818" + sfx}}).JSON()["booking"].(map[string]any)
	sysExec(t, inst, `UPDATE commercial.package_bookings SET hold_expires_at = now() - interval '1 minute' WHERE id = $1`, mustUUID(str(xb["id"])))
	if _, _, err := inst.App.Promo.Module.RunPackageExpiry(context.Background()); err != nil {
		t.Fatal(err)
	}
	if x := rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+str(xb["id"]), nil).JSON(); x["status"] != "expired" {
		t.Fatalf("expired hold: %v", x)
	}
	sysQueryRow(t, inst, `SELECT count(*) FROM reservation.allocations WHERE resource_id = $1 AND status IN ('held', 'confirmed')
		AND (lower(period) AT TIME ZONE 'Asia/Jakarta')::date = $2::date`, []any{mustUUID(bgl), xday}, &held)
	if held != 0 {
		t.Fatalf("expired hold still holds the bungalow: %d", held)
	}

	// ── member app (FR-APP-P3-05) ───────────────────────────────────────
	mc := roleUser(t, inst, "member")
	memberCustomer(t, sa)
	mday := days[6]
	teeTimes(t, sa, gc.Course, mday)
	mb := mc.Must(201, "POST", "/api/v1/member/package-bookings", map[string]any{"packageCode": "SG" + sfx, "startDate": mday, "pax": 2,
		"paymentMethod": "member_account"}).JSON()
	if mb["booking"].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("member app booking: %v", mb)
	}
	mbid := str(mb["booking"].(map[string]any)["id"])
	if l := mc.Must(200, "GET", "/api/v1/member/package-bookings", nil).Items(); !containsID(l, mbid) {
		t.Fatalf("my packages: %v", l)
	}
	if o := mc.Must(200, "GET", "/api/v1/member/offers", nil).JSON(); !containsID(asMaps(o["packages"]), pid) {
		t.Fatalf("packages in Offers: %v", o["packages"])
	}

	// ── component expiry (FR-PKG-06) ────────────────────────────────────
	sysExec(t, inst, `UPDATE commercial.package_booking_components SET expires_at = now() - interval '1 minute' WHERE booking_id = $1 AND status = 'unused'`,
		mustUUID(mbid))
	if _, n, err := inst.App.Promo.Module.RunPackageExpiry(context.Background()); err != nil || n < 4 {
		t.Fatalf("component expiry: %d %v", n, err)
	}
	if x := rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+mbid, nil).JSON(); x["status"] != "completed" {
		t.Fatalf("all components expired: %v", x)
	}

	// ── quotation line "package" → package booking with its schedule (FR-QUO-06) ─
	qday := days[7]
	teeTimes(t, sa, gc.Course, qday)
	q := sa.Must(201, "POST", "/api/v1/crm/quotations", map[string]any{"customerId": cust, "title": "Stay & Golf for two", "line": "package",
		"eventDate": qday, "pax": 2, "packageRef": "SG" + sfx, "pricingMode": "nett",
		"paymentTerms": []map[string]any{{"label": "DP 30%", "percent": "30", "dueDays": 1}, {"label": "Final", "percent": "70", "daysBeforeEvent": 7}},
		"lines":        []map[string]any{{"itemType": "package", "itemRef": "SG" + sfx, "description": "Stay & Golf", "quantity": "1", "unitPrice": "3200000"}}},
		"Idempotency-Key", newKey()).JSON()
	sent := sa.Must(200, "POST", "/api/v1/crm/quotations/"+str(q["id"])+":send", map[string]any{}).JSON()
	pub.Must(200, "POST", "/api/v1/public/quotations/"+slsToken(t, sent)+":accept", map[string]any{"name": "Package Guest", "termsAccepted": true})
	var qb map[string]any
	pcDispatch(t, "package booked from the quotation", func() bool {
		for _, x := range rs.Must(200, "GET", "/api/v1/commercial/package-bookings?filter[customerId]="+cust+"&limit=100", nil).Items() {
			if x["sourceId"] == q["id"] {
				qb = x
			}
		}
		return qb != nil
	})
	qb = rs.Must(200, "GET", "/api/v1/commercial/package-bookings/"+str(qb["id"]), nil).JSON()
	if !dec(qb["total"]).Equal(dec(q["total"])) || qb["channel"] != "quotation" || qb["schedule"] == nil ||
		len(qb["schedule"].(map[string]any)["lines"].([]any)) != 2 {
		t.Fatalf("quotation package: %v", qb)
	}
	var schedules int
	sysQueryRow(t, inst, `SELECT count(*) FROM billing.payment_schedules WHERE source_type = 'quotation' AND source_id = $1`, []any{mustUUID(str(q["id"]))}, &schedules)
	if schedules != 0 {
		t.Fatalf("the generic quotation schedule must not double the package schedule: %d", schedules)
	}

	// ── reports and the accounting export (FR-RPT-P3-05, FR-INT-P3-05) ──
	from, to := time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02"), time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/commercial.package_sales?params[from]="+from+"&params[to]="+to, nil).JSON()
	okRep := false
	for _, r := range rep["rows"].([]any) {
		m := r.(map[string]any)
		okRep = okRep || (m["code"] == "SG"+sfx && m["bookings"].(float64) >= 3 && m["cancelled"].(float64) >= 2)
	}
	if !okRep {
		t.Fatalf("package sales report: %v", rep["rows"])
	}
	al := sa.Must(200, "GET", "/api/v1/reporting/reports/commercial.package_allocation?params[from]="+from+"&params[to]="+to, nil).JSON()
	if len(al["rows"].([]any)) == 0 {
		t.Fatalf("allocation report: %v", al)
	}
	if exp := accountingExport(t, sa); !strings.Contains(exp, "package_allocation") || !strings.Contains(exp, "package_consumption") {
		t.Fatalf("accounting export: %s", exp)
	}
	sa.Must(200, "POST", "/api/v1/commercial/promotions/"+pp+":deactivate", map[string]any{"reason": "end of test"})
}
