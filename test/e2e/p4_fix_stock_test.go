package e2e

// P4 gap fixes, package G (inventory, POS & procurement operations):
// refund restock from POS events (FR-CNS-02), sold-out at POS from K9
// (FR-CNS-07), the PRD P3 §16 #5 POS discount tiers, modifier / combo
// consumption (FR-CNS-01), requisition and adjustment approvals
// (FR-REQ-01, FR-OPN-03), golf cart usage hours for maintenance (FR-AST-02),
// the §11 inventory events, the §16 #8/#9 seeds, delivery note files of
// goods receipts (FR-GR-01) and landed cost (FR-VAL-03).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/inventory"
)

// fixLatest is the latest outbox event of a type for an aggregate.
func fixLatest(t *testing.T, typ, agg string) uuid.UUID {
	t.Helper()
	var eid uuid.UUID
	sysQueryRow(t, inst, `SELECT id FROM platform.outbox WHERE event_type = $1 AND aggregate_id::text = $2 ORDER BY occurred_at DESC LIMIT 1`,
		[]any{typ, agg}, &eid)
	return eid
}

// fixInvConfig writes the Inventory Configuration (defaults + overrides)
// and restores the defaults at the end of the test.
func fixInvConfig(t *testing.T, sa *Client, overrides map[string]any) {
	t.Helper()
	write := func(o map[string]any) {
		raw, _ := json.Marshal(inventory.DefaultConfiguration)
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		for k, x := range o {
			v[k] = x
		}
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": inventory.CategoryConfiguration, "code": inventory.ConfigurationCode,
			"name": "Inventory Configuration", "value": v})
	}
	write(overrides)
	t.Cleanup(func() { write(nil) })
}

// fixValue is the stock value of an item in a warehouse.
func fixValue(t *testing.T, c *Client, wh, item string) decimal.Decimal {
	t.Helper()
	total := decimal.Zero
	for _, r := range c.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+wh+"&itemId="+item+"&includeZero=true", nil).Items() {
		total = total.Add(dec(r["value"]))
	}
	return total
}

// FR-CNS-02: commercial publishes commercial.sale_refunded / sale_voided;
// inventory returns the consumption of a refunded sale to stock per the
// Inventory Configuration — retail items always, F&B only when the kitchen
// had not started it (or refundRestockPrepared) — at the sale's cost, once.
func TestP4FixStockRefundRestock(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "RF" + k.sfx, "name": "Refund Bar " + k.sfx, "outletType": "restaurant",
		"kdsStations": []string{"kitchen"}}))
	food := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "RMG" + k.sfx, "name": "Mie Goreng " + k.sfx, "productType": "food",
		"price": "40000", "kitchenStation": "kitchen", "outletIds": []string{outlet}}))
	ballP := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "RGB" + k.sfx, "name": "Golf Balls " + k.sfx, "productType": "retail",
		"price": "50000", "outletIds": []string{outlet}}))
	kit := k.warehouse(t, sa, "RFK", map[string]any{"locationType": "kitchen", "outletId": outlet})
	noodle := k.item(t, sa, "NOODLE", k.kg, nil)
	ball := k.item(t, sa, "BALL", k.pcs, map[string]any{"itemType": "retail", "productId": ballP})
	menu := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "RMG" + k.sfx, "name": "Mie Goreng " + k.sfx, "recipeType": "menu",
		"productId": food, "yieldQuantity": "1"}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": menu, "itemId": noodle, "quantity": "0.1", "uomId": k.kg})
	invReceive(t, kit, map[string]any{"itemId": noodle, "quantity": "10", "unitCost": "20000"}, map[string]any{"itemId": ball, "quantity": "20", "unitCost": "30000"})
	shift := str(sa.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "0"}).JSON()["id"])

	order := func(send bool, lines ...map[string]any) string {
		return str(sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift, "send": send, "lines": lines},
			"Idempotency-Key", newKey()).JSON()["id"])
	}
	prepare := func(oid, state string) {
		for _, tk := range sa.Must(200, "GET", "/api/v1/commercial/kitchen-orders?station=kitchen", nil).Items() {
			if tk["orderId"] == oid {
				sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+str(tk["id"])+":state", map[string]any{"state": state})
				return
			}
		}
		t.Fatalf("kitchen ticket of %s missing", oid)
	}
	pay := func(oid string) {
		sa.Must(200, "POST", "/api/v1/commercial/orders/"+oid+":pay", map[string]any{"shiftId": shift, "tenders": []map[string]any{{"methodType": "cash"}}},
			"Idempotency-Key", newKey())
		invHandled(t, fixLatest(t, "commercial.sale_completed", oid), "inventory.stock_consumption")
	}
	refund := func(oid string) {
		if r := sa.Must(202, "POST", "/api/v1/commercial/orders/"+oid+":refund", map[string]any{"reason": "Guest complaint"}).JSON(); r["status"] != "applied" {
			t.Fatalf("refund: %v", r)
		}
		invHandled(t, fixLatest(t, "commercial.sale_refunded", oid), "inventory.refund_restock")
	}
	movements := func(oid string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, m := range sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+oid, nil).Items() {
			out[str(m["sourceType"])] = m
		}
		return out
	}

	// Sale A: 2 noodles started by the kitchen + 3 retail balls, refunded.
	a := order(true, map[string]any{"productId": food, "quantity": "2"}, map[string]any{"productId": ballP, "quantity": "3"})
	prepare(a, "preparing")
	pay(a)
	invEq(t, "noodle after sale A", invOnHand(t, sa, kit, noodle), "9.8")
	invEq(t, "balls after sale A", invOnHand(t, sa, kit, ball), "17")
	refund(a)
	invEq(t, "prepared food stays consumed", invOnHand(t, sa, kit, noodle), "9.8")
	invEq(t, "retail balls back in stock", invOnHand(t, sa, kit, ball), "20")
	ev := prcEvents(t, "commercial.sale_refunded", a)
	if len(ev) != 1 || ev[0]["refundRequestId"] == nil || ev[0]["outletId"] != outlet {
		t.Fatalf("commercial.sale_refunded: %v", ev)
	}
	prepared := map[string]bool{}
	for _, l := range ev[0]["lines"].([]any) {
		m := l.(map[string]any)
		prepared[str(m["productId"])] = m["prepared"] == true
	}
	if !prepared[food] || prepared[ballP] {
		t.Fatalf("prepared flags: %v", ev[0]["lines"])
	}
	mv := movements(a)
	restock := mv["sale_refund"]
	if restock == nil || restock["movementType"] != "consumption" {
		t.Fatalf("refund restock movement: %v", mv)
	}
	invEq(t, "restocked at the sale cost", restock["totalCost"], "90000")
	if pe := invEvents(t, "inventory.movement_posted", str(restock["id"])); len(pe) != 1 || !strings.Contains(pe[0], `"sourceType": "sale_refund"`) {
		t.Fatalf("restock journal event: %v", pe)
	}
	// The same event again restocks nothing more; the manual reversal is refused.
	invHandled(t, invPublish(t, "commercial.sale_refunded", uuid.MustParse(a), ev[0]), "inventory.refund_restock")
	invEq(t, "refund restock is idempotent", invOnHand(t, sa, kit, ball), "20")
	if r := sa.Do("POST", "/api/v1/inventory/stock-movements/"+str(mv["sale"]["id"])+":reverse", map[string]any{"reason": "refund"}); r.Status != 409 ||
		!strings.Contains(string(r.Body), "already_restocked") {
		t.Fatalf("manual reversal after the automatic restock: %s", r)
	}

	// Sale B: a noodle sent to the kitchen but not started comes back.
	b := order(false, map[string]any{"productId": food, "quantity": "1"})
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+b+":send", map[string]any{})
	pay(b)
	invEq(t, "noodle after sale B", invOnHand(t, sa, kit, noodle), "9.7")
	refund(b)
	invEq(t, "unprepared food returns to stock", invOnHand(t, sa, kit, noodle), "9.8")
	if l := prcEvents(t, "commercial.sale_refunded", b)[0]["lines"].([]any)[0].(map[string]any); l["sent"] != true || l["prepared"] != false {
		t.Fatalf("sent, not prepared: %v", l)
	}

	// refundRestockPrepared: prepared food comes back too.
	fixInvConfig(t, sa, map[string]any{"refundRestockPrepared": true})
	c := order(true, map[string]any{"productId": food, "quantity": "1"})
	prepare(c, "ready")
	pay(c)
	refund(c)
	invEq(t, "prepared food returned (refundRestockPrepared)", invOnHand(t, sa, kit, noodle), "9.8")

	// refundRestock off: nothing comes back.
	fixInvConfig(t, sa, map[string]any{"refundRestock": false})
	d := order(false, map[string]any{"productId": ballP, "quantity": "1"})
	pay(d)
	refund(d)
	invEq(t, "no restock when refundRestock is off", invOnHand(t, sa, kit, ball), "19")
	if movements(d)["sale_refund"] != nil {
		t.Fatal("refundRestock off must not post a restock")
	}

	// Voids of unpaid orders are published (nothing was consumed).
	e := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": shift, "send": true,
		"lines": []map[string]any{{"productId": food, "quantity": "1"}, {"productId": ballP, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	eid := str(e["id"])
	var ballLine string
	for _, l := range e["lines"].([]any) {
		if m := l.(map[string]any); m["productId"] == ballP {
			ballLine = str(m["id"])
		}
	}
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+eid+"/lines/"+ballLine+":void", map[string]any{"reason": "Wrong item"})
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+eid+":void", map[string]any{"reason": "Guest left"})
	voids := prcEvents(t, "commercial.sale_voided", eid)
	if len(voids) != 2 || voids[0]["scope"] != "line" || voids[1]["scope"] != "order" || len(voids[1]["lines"].([]any)) != 1 {
		t.Fatalf("commercial.sale_voided: %v", voids)
	}
	if len(movements(eid)) != 0 {
		t.Fatal("a voided unpaid order consumes nothing")
	}
}

// FR-CNS-07: the POS menu flags stock-tracked retail items of the outlet's
// warehouses with their available stock (K9 reporting.stock_availability)
// and marks them Sold Out at zero (POS Policies markSoldOut).
func TestP4FixStockSoldOut(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "SO" + k.sfx, "name": "Pro Shop " + k.sfx, "outletType": "retail"}))
	capP := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "SOC" + k.sfx, "name": "Cap " + k.sfx, "productType": "retail",
		"price": "150000", "outletIds": []string{outlet}}))
	tea := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "SOT" + k.sfx, "name": "Tea " + k.sfx, "productType": "beverage",
		"price": "20000", "outletIds": []string{outlet}}))
	shop := k.warehouse(t, sa, "SOW", map[string]any{"locationType": "outlet", "outletId": outlet})
	capItem := k.item(t, sa, "CAP", k.pcs, map[string]any{"itemType": "retail", "productId": capP})
	item := func(pid string) map[string]any {
		for _, m := range sa.Must(200, "GET", "/api/v1/commercial/outlets/"+outlet+"/menu", nil).Items() {
			if m["productId"] == pid {
				return m
			}
		}
		t.Fatalf("product %s not on the menu", pid)
		return nil
	}
	if m := item(capP); m["stockTracked"] != false || m["soldOut"] != false {
		t.Fatalf("never stocked at the outlet: %v", m)
	}
	invReceive(t, shop, map[string]any{"itemId": capItem, "quantity": "2", "unitCost": "90000"})
	if m := item(capP); m["stockTracked"] != true || m["soldOut"] != false || !dec(m["available"]).Equal(decimal.NewFromInt(2)) {
		t.Fatalf("in stock: %v", m)
	}
	sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": shop, "costCenter": "pro_shop", "reason": "Display",
		"lines": []map[string]any{{"itemId": capItem, "quantity": "2"}}})
	if m := item(capP); m["stockTracked"] != true || m["soldOut"] != true || !dec(m["available"]).IsZero() {
		t.Fatalf("sold out: %v", m)
	}
	if m := item(tea); m["stockTracked"] != false || m["soldOut"] != false {
		t.Fatalf("recipe / untracked product: %v", m)
	}
	pos := func(mark bool) {
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "POS Policies", "code": "pos.policy", "name": "POS Policies",
			"value": map[string]any{"maxDiscountPercent": "10", "offlineMemberCharge": true, "scheduledLeadMinutes": 30, "defaultKitchenStation": "kitchen",
				"markSoldOut": mark}})
	}
	pos(false)
	t.Cleanup(func() { pos(true) })
	if m := item(capP); m["stockTracked"] != true || m["soldOut"] != false {
		t.Fatalf("markSoldOut off: %v", m)
	}
}

// PRD P3 §16 #5: the Pricing Policies default the POS manual discount
// limits to the quotation tiers; above the role's limit a supervisor
// (commercial.pos.discount_override) is needed.
func TestP4FixStockDiscountTiers(t *testing.T) {
	sa := superAdmin(t, inst)
	var def map[string]any
	for _, e := range sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if e["code"] == "commercial.pricing" {
			def, _ = e["default"].(map[string]any)
		}
	}
	limits, _ := def["manualDiscountLimits"].(map[string]any)
	// PRD P3 §16 #5 tiers (one table for the POS and the quotations, PO decision 4b)
	want := map[string]string{"sales_executive": "5", "banquet_sales": "5", "cashier": "5", "pos_staff": "5", "marketing_staff": "5",
		"crm_admin": "10", "membership_admin": "10", "golf_admin": "10",
		"banquet_manager": "20", "event_manager": "20", "outlet_manager": "20", "membership_manager": "20", "golf_manager": "20",
		"sport_club_manager": "20", "club_manager": "20", "resort_manager": "20", "general_manager": "100"}
	if len(limits) != len(want) {
		t.Fatalf("default POS tiers %v, want %v", limits, want)
	}
	for role, v := range want {
		if limits[role] != v {
			t.Fatalf("tier of %s: %v, want %s", role, limits[role], v)
		}
	}
	cashier := roleUser(t, inst, "cashier")
	om := roleUser(t, inst, "outlet_manager")
	sfx := invSfx()
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "DT" + sfx, "name": "Tier Cafe " + sfx, "outletType": "restaurant"}))
	p := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "DTP" + sfx, "name": "Club Sandwich " + sfx, "productType": "food",
		"price": "100000", "outletIds": []string{outlet}}))
	o := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": p}}}).JSON()
	path := "/api/v1/commercial/orders/" + str(o["id"]) + "/lines/" + str(o["lines"].([]any)[0].(map[string]any)["id"]) + ":discount"
	cashier.Must(403, "POST", path, map[string]any{"percent": "6", "reason": "Regular"})
	cashier.Must(200, "POST", path, map[string]any{"percent": "5", "reason": "Regular"})
	om.Must(200, "POST", path, map[string]any{"percent": "20", "reason": "Service recovery"})
	om.Must(200, "POST", path, map[string]any{"percent": "30", "reason": "Supervisor override"})
}

// FR-CNS-01 (K7): modifier impacts (add, remove, replace) and combo
// components are deducted from the outlet warehouse.
func TestP4FixStockConsumptionModifiersCombos(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "MC" + k.sfx, "name": "Burger Bar " + k.sfx, "outletType": "restaurant"}))
	burger := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "MCB" + k.sfx, "name": "Burger " + k.sfx, "productType": "food", "price": "60000"}))
	sodaP := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "MCS" + k.sfx, "name": "Soda " + k.sfx, "productType": "retail", "price": "15000"}))
	combo := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "MCC" + k.sfx, "name": "Burger Combo " + k.sfx, "productType": "food",
		"price": "80000", "comboItems": []map[string]any{{"productId": burger, "quantity": 1}, {"productId": sodaP, "quantity": 2}}}))
	kit := k.warehouse(t, sa, "MCK", map[string]any{"locationType": "kitchen", "outletId": outlet})
	bun, patty, cheese, chicken := k.item(t, sa, "BUN", k.pcs, nil), k.item(t, sa, "PATTY", k.pcs, nil), k.item(t, sa, "CHEESE", k.pcs, nil), k.item(t, sa, "CHICK", k.pcs, nil)
	soda := k.item(t, sa, "SODA", k.pcs, map[string]any{"itemType": "retail", "productId": sodaP})
	rec := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "MCR" + k.sfx, "name": "Burger " + k.sfx, "recipeType": "menu",
		"productId": burger, "yieldQuantity": "1"}))
	for _, it := range []string{bun, patty, cheese} {
		sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": rec, "itemId": it, "quantity": "1", "uomId": k.pcs})
	}
	grp := idOf(sa.Must(201, "POST", "/api/v1/commercial/modifier-groups", map[string]any{"code": "MCG" + k.sfx, "name": "Burger options", "minSelect": 0,
		"maxSelect": 3, "productIds": []string{burger}}))
	mod := func(code string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/commercial/modifiers", map[string]any{"code": code + k.sfx, "name": code, "groupId": grp, "priceDelta": "0"}))
	}
	extra, noCheese, chick := mod("XCH"), mod("NCH"), mod("CHK")
	sa.Must(201, "POST", "/api/v1/inventory/modifier-impacts", map[string]any{"modifierId": extra, "action": "add", "itemId": cheese, "quantity": "1", "uomId": k.pcs})
	sa.Must(201, "POST", "/api/v1/inventory/modifier-impacts", map[string]any{"modifierId": noCheese, "action": "remove", "itemId": cheese})
	sa.Must(201, "POST", "/api/v1/inventory/modifier-impacts", map[string]any{"modifierId": chick, "action": "replace", "itemId": patty, "replaceItemId": chicken})
	for _, it := range []string{bun, patty, cheese, chicken, soda} {
		invReceive(t, kit, map[string]any{"itemId": it, "quantity": "10", "unitCost": "5000"})
	}
	order := uuid.New()
	invHandled(t, invPublish(t, "commercial.sale_completed", order, map[string]any{"orderId": order, "orderNo": "MC-" + k.sfx, "outletId": outlet,
		"at": time.Now().UTC().Format(time.RFC3339), "lines": []map[string]any{
			{"lineId": uuid.New(), "productId": burger, "quantity": "1", "modifierIds": []string{extra}, "netAmount": "60000"},
			{"lineId": uuid.New(), "productId": burger, "quantity": "1", "modifierIds": []string{noCheese, chick}, "netAmount": "60000"},
			{"lineId": uuid.New(), "productId": combo, "quantity": "1", "modifierIds": []string{}, "netAmount": "80000"}}}), "inventory.stock_consumption")
	for name, want := range map[string]struct{ item, qty string }{"bun": {bun, "7"}, "patty (one replaced)": {patty, "8"},
		"cheese (extra + removed + combo)": {cheese, "7"}, "chicken (replacement)": {chicken, "9"}, "soda (2 per combo)": {soda, "8"}} {
		invEq(t, name, invOnHand(t, sa, kit, want.item), want.qty)
	}
}

// FR-REQ-01 / FR-OPN-03: a store requisition and a stock adjustment go
// through their approval workflow — approved (issued / posted) and rejected.
func TestP4FixStockApprovals(t *testing.T) {
	sa := superAdmin(t, inst)
	fm := roleUser(t, inst, "finance_manager")
	ks := roleUser(t, inst, "kitchen_staff")
	ws := roleUser(t, inst, "warehouse_staff")
	k := newInvKit(t, sa)
	store := k.warehouse(t, sa, "AP", nil)
	kit := k.warehouse(t, sa, "APK", map[string]any{"parentId": store, "locationType": "kitchen"})
	oil := k.item(t, sa, "AOIL", k.btl, nil)
	invReceive(t, store, map[string]any{"itemId": oil, "quantity": "10", "unitCost": "25000"})
	for _, doc := range []string{"store_requisition", "stock_adjustment"} {
		wf := sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": doc, "name": doc + " " + k.sfx,
			"steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "finance_manager"),
				"conditions": []map[string]any{{"attribute": "warehouse", "operator": "eq", "value": "AP" + k.sfx}}}}}).JSON()
		t.Cleanup(func() {
			sa.Must(200, "PATCH", "/api/v1/platform/approval-workflows/"+str(wf["id"]), map[string]any{"status": "inactive"})
		})
	}
	req := func() map[string]any {
		r := ks.Must(201, "POST", "/api/v1/inventory/requisitions", map[string]any{"requestType": "store", "requestingWarehouseId": kit, "submit": true,
			"lines": []map[string]any{{"itemId": oil, "quantity": "2"}}}, "Idempotency-Key", newKey()).JSON()
		if r["status"] != "submitted" || r["approvalRequestId"] == nil {
			t.Fatalf("requisition waiting for approval: %v", r)
		}
		return r
	}
	r1 := req()
	ws.Must(409, "POST", "/api/v1/inventory/requisitions/"+str(r1["id"])+":fulfill", map[string]any{})
	prcDecide(t, fm, r1["approvalRequestId"], true, "Needed for the weekend")
	if r := sa.Must(200, "GET", "/api/v1/inventory/requisitions/"+str(r1["id"]), nil).JSON(); r["status"] != "approved" {
		t.Fatalf("approved requisition: %v", r)
	}
	if r := ws.Must(200, "POST", "/api/v1/inventory/requisitions/"+str(r1["id"])+":fulfill", map[string]any{}).JSON(); r["status"] != "issued" {
		t.Fatalf("issued requisition: %v", r)
	}
	r2 := req()
	prcDecide(t, fm, r2["approvalRequestId"], false, "Use the open stock first")
	if r := sa.Must(200, "GET", "/api/v1/inventory/requisitions/"+str(r2["id"]), nil).JSON(); r["status"] != "rejected" || r["rejectionReason"] == nil {
		t.Fatalf("rejected requisition: %v", r)
	}

	adj := func() map[string]any {
		a := ws.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": store, "reason": "damage",
			"lines": []map[string]any{{"itemId": oil, "quantity": "-1"}}}, "Idempotency-Key", newKey()).JSON()
		a = ws.Must(200, "POST", "/api/v1/inventory/adjustments/"+str(a["id"])+":submit", nil).JSON()
		if a["status"] != "pending_approval" || a["approvalRequestId"] == nil {
			t.Fatalf("adjustment waiting for approval: %v", a)
		}
		return a
	}
	a1 := adj()
	invEq(t, "nothing posted before approval", invOnHand(t, sa, store, oil), "8")
	prcDecide(t, fm, a1["approvalRequestId"], true, "Broken bottle confirmed")
	if a := sa.Must(200, "GET", "/api/v1/inventory/adjustments/"+str(a1["id"]), nil).JSON(); a["status"] != "posted" {
		t.Fatalf("approved adjustment: %v", a)
	}
	invEq(t, "after the approved adjustment", invOnHand(t, sa, store, oil), "7")
	a2 := adj()
	prcDecide(t, fm, a2["approvalRequestId"], false, "Recount first")
	if a := sa.Must(200, "GET", "/api/v1/inventory/adjustments/"+str(a2["id"]), nil).JSON(); a["status"] != "rejected" || a["rejectionReason"] == nil {
		t.Fatalf("rejected adjustment: %v", a)
	}
	invEq(t, "a rejected adjustment posts nothing", invOnHand(t, sa, store, oil), "7")
}

// PRD P4 §11 events (inventory.opname_posted, production_completed,
// stock_low, asset_maintenance_due) and FR-AST-02: the hours of returned P2
// golf cart assignments feed the usage-based schedule of the linked asset.
func TestP4FixStockEventsAndCartUsage(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "EV", nil)
	salt := k.item(t, sa, "SALT", k.kg, nil)
	sauce := k.item(t, sa, "SAUCE", k.g, map[string]any{"itemType": "semi_finished"})
	invReceive(t, wh, map[string]any{"itemId": salt, "quantity": "10", "unitCost": "10000"})

	// opname within tolerance → posted, inventory.opname_posted
	o := sa.Must(201, "POST", "/api/v1/inventory/stock-opnames", map[string]any{"warehouseId": wh, "itemIds": []string{salt}, "blind": false}).JSON()
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o["id"])+":count", map[string]any{"lines": []map[string]any{{"itemId": salt, "quantity": "10"}}})
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o["id"])+":submit", map[string]any{})
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o["id"])+":post", nil)
	if ev := prcEvents(t, "inventory.opname_posted", str(o["id"])); len(ev) != 1 || ev[0]["number"] != o["number"] || ev[0]["warehouseId"] != wh ||
		ev[0]["movementId"] != nil || !dec(ev[0]["varianceValue"]).IsZero() {
		t.Fatalf("inventory.opname_posted: %v", ev)
	}

	// production → inventory.production_completed
	rec := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "EVR" + k.sfx, "name": "Sauce " + k.sfx, "recipeType": "sub_recipe",
		"outputItemId": sauce, "yieldQuantity": "1000", "yieldUomId": k.g}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": rec, "itemId": salt, "quantity": "1", "uomId": k.kg})
	po := sa.Must(201, "POST", "/api/v1/inventory/production-orders", map[string]any{"recipeId": rec, "warehouseId": wh, "plannedQuantity": "1000"}).JSON()
	sa.Must(200, "POST", "/api/v1/inventory/production-orders/"+str(po["id"])+":complete", map[string]any{"actualQuantity": "900"})
	if ev := prcEvents(t, "inventory.production_completed", str(po["id"])); len(ev) != 1 || ev[0]["actualQuantity"] != "900" || ev[0]["outputItemId"] != sauce ||
		!dec(ev[0]["inputCost"]).Equal(decimal.NewFromInt(10000)) || len(ev[0]["inputs"].([]any)) != 1 {
		t.Fatalf("inventory.production_completed: %v", ev)
	}

	// below the reorder point → inventory.stock_low (with reorder_needed)
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": salt, "warehouseId": wh, "parLevel": "30"})
	sa.Must(201, "POST", "/api/v1/inventory/reorder-points", map[string]any{"itemId": salt, "warehouseId": wh, "reorderPoint": "20"})
	sa.Must(200, "POST", "/api/v1/inventory/replenishment:run", nil)
	if ev := prcEvents(t, "inventory.stock_low", wh); len(ev) != 1 || !strings.Contains(fmtJSON(ev[0]["items"]), salt) || ev[0]["warehouseCode"] != "EV"+k.sfx {
		t.Fatalf("inventory.stock_low: %v", ev)
	}

	// FR-AST-02: golf cart hours → usage-based schedule due → inventory.asset_maintenance_due
	cart := idOf(sa.Must(201, "POST", "/api/v1/golf/golf-carts", map[string]any{"code": "EVC" + k.sfx, "name": "Buggy " + k.sfx}))
	cat := idOf(sa.Must(201, "POST", "/api/v1/inventory/asset-categories", map[string]any{"code": "EVA" + k.sfx, "name": "Carts " + k.sfx, "assetClass": "golf_cart",
		"depreciationMethod": "straight_line", "usefulLifeMonths": 60}))
	asset := idOf(sa.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "EVB" + k.sfx, "name": "Buggy " + k.sfx, "categoryId": cat,
		"acquisitionDate": invToday(), "acquisitionCost": "50000000", "golfCartRef": cart}))
	sched := idOf(sa.Must(201, "POST", "/api/v1/inventory/maintenance-schedules", map[string]any{"assetId": asset, "name": "Battery check", "triggerType": "usage_hours",
		"intervalHours": "4"}))
	day := clubDay(inst, 35, isWeekday)
	slots := slotsOf(teeTimes(t, sa, demoCourse(t, inst), day), "afternoon", 10)
	flight := uuid.New()
	sysExec(t, inst, `INSERT INTO golf.flights (id, property_id, tee_time_id, course_id, play_date, flight_no, status)
		SELECT $1, property_id, id, course_id, $3::date, 90, 'completed' FROM golf.tee_times WHERE id = $2`, flight, str(slots[len(slots)-1]["id"]), day)
	sysExec(t, inst, `INSERT INTO golf.golf_cart_assignments (id, property_id, golf_cart_id, flight_id, play_date, period, status, out_at, returned_at)
		VALUES ($1, $2, $3, $4, $5::date, tstzrange(now() - interval '6 hours', now() - interval '1 hour'), 'returned', now() - interval '6 hours',
		now() - interval '1 hour')`, uuid.New(), inst.Main, cart, flight, day)
	for i := 0; i < 2; i++ { // the second run adds nothing
		if _, err := inst.App.Stock.Service.RunDaily(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	h := sa.Must(200, "GET", "/api/v1/inventory/assets/"+asset+"/history", nil).JSON()
	invEq(t, "usage hours from the golf cart", h["asset"].(map[string]any)["usageHours"], "5")
	carts := 0
	for _, u := range h["usages"].([]any) {
		if u.(map[string]any)["usageType"] == "golf_cart" {
			carts++
		}
	}
	if carts != 1 {
		t.Fatalf("golf cart usages: %v", h["usages"])
	}
	if ev := prcEvents(t, "inventory.asset_maintenance_due", sched); len(ev) != 1 || ev[0]["assetId"] != asset || ev[0]["triggerType"] != "usage_hours" {
		t.Fatalf("inventory.asset_maintenance_due: %v", ev)
	}
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// PRD P4 §16 #8 / #9 demo seed: moving average and the opname tolerances
// per category, the warehouses and outlet sub-stores, and the Stock Opname
// approval to the Finance Manager.
func TestP4FixStockSeeds(t *testing.T) {
	sa := superAdmin(t, inst)
	cats := map[string]map[string]any{}
	for _, c := range sa.Must(200, "GET", "/api/v1/inventory/categories?limit=500", nil).Items() {
		cats[str(c["code"])] = c
	}
	for code, tol := range map[string]string{"FNB": "2", "BEV": "0.5", "PROSHOP": "0.5", "GOLFOPS": "1", "SPORT": "1", "AMENITY": "1", "SPARE": "1", "GENERAL": "1"} {
		if c := cats[code]; c == nil || c["valuationMethod"] != "moving_average" || !dec(c["opnameTolerancePercent"]).Equal(decimal.RequireFromString(tol)) {
			t.Fatalf("category %s: %v", code, c)
		}
	}
	whs := map[string]string{}
	for _, w := range sa.Must(200, "GET", "/api/v1/inventory/warehouses?limit=500", nil).Items() {
		whs[str(w["code"])] = str(w["name"])
	}
	for code, name := range map[string]string{"MAIN-STORE": "Main Store", "COLD-STORE": "Cold Store", "BEV-STORE": "Beverage Store", "PRO-SHOP": "Pro Shop Store",
		"ENGINEERING": "Engineering Store", "CLUBHOUSE": "Clubhouse Restaurant", "BAR": "Bar", "HALFWAY": "Halfway House", "KITCHEN": "Banquet Kitchen",
		"SPORT-CAFE": "Sport Club Café", "GOLF-OPS": "Golf Ops", "TRANSIT": "In Transit"} {
		if whs[code] != name {
			t.Fatalf("warehouse %s: %q, want %q", code, whs[code], name)
		}
	}
	var role string
	sysQueryRow(t, inst, `SELECT r.code FROM platform.approval_workflows w JOIN platform.approval_workflow_steps s ON s.workflow_id = w.id
		JOIN platform.roles r ON r.id = s.approver_role_id WHERE w.document_type = 'stock_opname' ORDER BY w.created_at LIMIT 1`, nil, &role)
	if role != "finance_manager" {
		t.Fatalf("stock opname approver: %s", role)
	}
}

// FR-GR-01: the delivery note photo / document is uploaded, attached to the
// goods receipt and downloaded through the receipt.
func TestP4FixStockDeliveryNote(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	ws := roleUser(t, inst, "warehouse_staff")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "DN", nil)
	rice := k.item(t, sa, "DNR", k.kg, nil)
	sup := prcSupplier(t, sa, "DNS"+k.sfx, nil)
	po := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": rice, "quantity": "10", "unitPrice": "10000"}}})
	pdf := "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n"
	body, ctype := multipartBody(t, nil, "file", "surat-jalan.pdf", pdf)
	f1 := ws.Must(201, "POST", "/api/v1/procurement/delivery-note-files", body, "Content-Type", ctype).JSON()
	if f1["contentType"] != "application/pdf" || f1["public"] != false || f1["purpose"] != "attachment" {
		t.Fatalf("delivery note file: %v", f1)
	}
	body, ctype = multipartBody(t, nil, "file", "photo.png", cmsPNG(t, 8, 8))
	f2 := ws.Must(201, "POST", "/api/v1/procurement/delivery-note-files", body, "Content-Type", ctype).JSON()
	body, ctype = multipartBody(t, nil, "file", "notes.txt", "plain text")
	ws.Must(422, "POST", "/api/v1/procurement/delivery-note-files", body, "Content-Type", ctype)
	gr := ws.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po["id"], "deliveryNoteNo": "SJ-" + k.sfx,
		"attachmentFileIds": []any{f1["id"], f2["id"]}, "lines": []map[string]any{{"purchaseOrderLineId": prcLine(po, 0)["id"], "acceptedQuantity": "10"}}},
		"Idempotency-Key", newKey()).JSON()
	got := sa.Must(200, "GET", "/api/v1/procurement/goods-receipts/"+str(gr["id"]), nil).JSON()
	if ids := got["attachmentFileIds"].([]any); len(ids) != 2 || ids[0] != f1["id"] || ids[1] != f2["id"] {
		t.Fatalf("attachments of the receipt: %v", got["attachmentFileIds"])
	}
	dl := sa.Must(200, "GET", "/api/v1/procurement/goods-receipts/"+str(gr["id"])+"/attachments/"+str(f1["id"]), nil)
	if string(dl.Body) != pdf {
		t.Fatalf("downloaded delivery note: %q", dl.Body)
	}
	sa.Must(404, "GET", "/api/v1/procurement/goods-receipts/"+str(gr["id"])+"/attachments/"+uuid.NewString(), nil)
}

// FR-VAL-03 landed cost: freight on the goods invoice allocated by value,
// and import duty of a forwarder allocated by quantity to another receipt,
// are valued into the stock still on hand and the used part (revaluation).
func TestP4FixStockLandedCost(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "LC", nil)
	rice := k.item(t, sa, "LCR", k.kg, nil)
	flour, sugar := k.item(t, sa, "LCF", k.kg, nil), k.item(t, sa, "LCS", k.kg, nil)
	sup := prcSupplier(t, sa, "LCS"+k.sfx, nil)
	fwd := prcSupplier(t, sa, "LCF"+k.sfx, nil)
	receive := func(po map[string]any) map[string]any {
		var lines []map[string]any
		for _, l := range po["lines"].([]any) {
			lines = append(lines, map[string]any{"purchaseOrderLineId": l.(map[string]any)["id"], "acceptedQuantity": l.(map[string]any)["quantity"]})
		}
		return sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po["id"], "lines": lines}, "Idempotency-Key", newKey()).JSON()
	}
	po := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": rice, "quantity": "100", "unitPrice": "10000"}}})
	receive(po)
	prcDispatch(t, "rice received", func() bool { return invOnHand(t, sa, wh, rice).Equal(decimal.NewFromInt(100)) })
	sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "kitchen", "reason": "Lunch service",
		"lines": []map[string]any{{"itemId": rice, "quantity": "40"}}})

	vi := "/api/v1/procurement/vendor-invoices"
	sa.Must(422, "POST", vi, map[string]any{"supplierId": sup, "supplierInvoiceNo": "LCX-" + k.sfx, "lines": []map[string]any{{"quantity": "1", "unitPrice": "1000",
		"description": "Freight", "landedCost": "value", "itemId": rice}}})
	sa.Must(422, "POST", vi, map[string]any{"supplierId": sup, "supplierInvoiceNo": "LCY-" + k.sfx, "lines": []map[string]any{{"quantity": "1", "unitPrice": "1000",
		"description": "Freight", "landedCost": "weight"}}})
	inv := sa.Must(201, "POST", vi, map[string]any{"supplierId": sup, "supplierInvoiceNo": "LC1-" + k.sfx, "match": true, "lines": []map[string]any{
		{"purchaseOrderLineId": prcLine(po, 0)["id"], "quantity": "100", "unitPrice": "10000"},
		{"description": "Freight Jakarta", "quantity": "1", "unitPrice": "200000", "taxPercent": "0", "landedCost": "value"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, inv, "approved")
	if l := prcLine(inv, 1); l["matchStatus"] != "matched" || l["landedCost"] != "value" || l["accountHint"] != "inventory" {
		t.Fatalf("landed cost line: %v", l)
	}
	if ev := prcEvents(t, "procurement.invoice_price_variance", str(inv["id"])); len(ev) != 1 ||
		!dec(ev[0]["lines"].([]any)[0].(map[string]any)["invoicedBaseUnitCost"]).Equal(decimal.NewFromInt(12000)) {
		t.Fatalf("landed cost in the price variance: %v", ev)
	}
	prcDispatch(t, "rice revalued", func() bool { return len(prcEvents(t, "inventory.revaluation_posted", str(inv["id"]))) == 1 })
	rv := prcEvents(t, "inventory.revaluation_posted", str(inv["id"]))[0]["lines"].([]any)[0].(map[string]any)
	invEq(t, "landed cost on stock", rv["stockAmount"], "120000")
	invEq(t, "landed cost already used", rv["consumedAmount"], "80000")
	invEq(t, "rice value with freight", fixValue(t, sa, wh, rice), "720000")

	// A forwarder's import duty on another receipt, by quantity.
	po2 := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": flour, "quantity": "10", "unitPrice": "1000"},
		{"itemId": sugar, "quantity": "30", "unitPrice": "2000"}}})
	gr2 := receive(po2)
	prcDispatch(t, "flour and sugar received", func() bool { return invOnHand(t, sa, wh, sugar).Equal(decimal.NewFromInt(30)) })
	duty := sa.Must(201, "POST", vi, map[string]any{"supplierId": fwd, "supplierInvoiceNo": "LC2-" + k.sfx, "match": true, "lines": []map[string]any{
		{"description": "Import duty", "quantity": "1", "unitPrice": "40000", "taxPercent": "0", "landedCost": "quantity", "landedGoodsReceiptId": gr2["id"]}}},
		"Idempotency-Key", newKey()).JSON()
	prcStatus(t, duty, "approved")
	prcDispatch(t, "duty revalued", func() bool { return len(prcEvents(t, "inventory.revaluation_posted", str(duty["id"]))) == 1 })
	invEq(t, "flour value with duty", fixValue(t, sa, wh, flour), "20000")
	invEq(t, "sugar value with duty", fixValue(t, sa, wh, sugar), "90000")

	// Nothing received to allocate to: not received, held.
	held := sa.Must(201, "POST", vi, map[string]any{"supplierId": fwd, "supplierInvoiceNo": "LC3-" + k.sfx, "match": true, "lines": []map[string]any{
		{"description": "Freight", "quantity": "1", "unitPrice": "1000", "taxPercent": "0", "landedCost": "value"}}}, "Idempotency-Key", newKey()).JSON()
	if l := prcLine(held, 0); held["status"] == "approved" || l["matchStatus"] != "not_received" {
		t.Fatalf("landed cost without stock: %v", held)
	}
}
