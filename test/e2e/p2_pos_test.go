package e2e

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sseWait opens an SSE stream and reports the time until an event whose
// name has the given prefix arrives (after trigger runs).
func sseWait(t *testing.T, c *Client, path, prefix string, trigger func()) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.in.Server.URL+path, nil)
	req.Header.Set("X-Property-Id", c.Property.String())
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream %s: %d", path, resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	ready := false
	var start time.Time
	for sc.Scan() {
		line := sc.Text()
		if !ready && strings.HasPrefix(line, "event: ready") {
			ready = true
			time.Sleep(200 * time.Millisecond) // listener attached
			start = time.Now()
			go trigger()
			continue
		}
		if ready && strings.HasPrefix(line, "event: "+prefix) {
			return time.Since(start)
		}
	}
	t.Fatalf("no %s event on %s", prefix, path)
	return 0
}

// EP-20/21/22 acceptance: split bill paid with different methods incl.
// member charge; caddy tablet order on the KDS in < 2 s; offline sales sync
// without duplicates and the Z report equals the payments; theoretical
// food cost from recipes.
func TestP2POSKitchenBOM(t *testing.T) {
	f := setupP2(t)
	sa := f.SA
	// BOM foundation
	kg := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "KG", "name": "Kilogram", "kind": "mass"}))
	g := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "G", "name": "Gram", "kind": "mass"}))
	pcs := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "PCS", "name": "Piece"}))
	portion := idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": "PORTION", "name": "Portion", "kind": "portion"}))
	sa.Must(201, "POST", "/api/v1/inventory/uom-conversions", map[string]any{"fromUomId": kg, "toUomId": g, "factor": "1000"})
	rice := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "RICE", "name": "Beras", "baseUomId": kg, "standardCost": "15000"}))
	egg := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "EGG", "name": "Telur", "baseUomId": pcs, "standardCost": "2500"}))
	chili := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "CHILI", "name": "Cabai", "baseUomId": kg, "standardCost": "40000"}))
	garlic := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "GARLIC", "name": "Bawang Putih", "baseUomId": kg, "standardCost": "30000"}))
	sambalItem := idOf(sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "SAMBAL", "name": "Sambal", "itemType": "semi_finished", "baseUomId": g}))
	sambal := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "R-SAMBAL", "name": "Sambal (batch)", "recipeType": "sub_recipe",
		"outputItemId": sambalItem, "yieldQuantity": "500", "yieldUomId": g}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": sambal, "itemId": chili, "quantity": "400", "uomId": g})
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": sambal, "itemId": garlic, "quantity": "100", "uomId": g})

	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "SPIKE", "name": "The Spike Bar Restaurant", "outletType": "restaurant",
		"taxCodes": []string{"P2SVC", "P2PB1"}, "pricingMode": "plus_plus", "kdsStations": []string{"kitchen", "bar"}}))
	halfway := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "HALFWAY-P2", "name": "Halfway House", "outletType": "restaurant",
		"kdsStations": []string{"halfway"}}))
	nasi := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "NASGOR", "name": "Nasi Goreng", "category": "Main",
		"productType": "food", "price": "85000", "memberPrice": "75000", "kitchenStation": "kitchen"}))
	teh := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "ESTEH", "name": "Es Teh Tawar", "category": "Drinks",
		"productType": "beverage", "price": "20000", "kitchenStation": "bar"}))
	shirt := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "SHIRT", "name": "Golf Shirt", "productType": "retail",
		"price": "450000", "barcode": "8991234567890"}))
	sa.Must(201, "POST", "/api/v1/commercial/product-variants", map[string]any{"code": "SHIRT-L", "name": "L", "productId": shirt, "priceDelta": "0", "barcode": "8991234567891"})
	grp := idOf(sa.Must(201, "POST", "/api/v1/commercial/modifier-groups", map[string]any{"code": "EXTRA", "name": "Extra", "minSelect": 0, "maxSelect": 2, "productIds": []string{nasi}}))
	extraEgg := idOf(sa.Must(201, "POST", "/api/v1/commercial/modifiers", map[string]any{"code": "EXTRA-EGG", "name": "Extra telur", "groupId": grp, "priceDelta": "8000"}))
	sa.Must(201, "POST", "/api/v1/commercial/menus", map[string]any{"code": "SPIKE-ALLDAY", "name": "All Day", "outletId": outlet, "productIds": []string{nasi, teh},
		"channels": []string{"pos", "member_app", "caddy_tablet"}})
	nasiRecipe := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "R-NASGOR", "name": "Nasi Goreng", "productId": nasi, "yieldQuantity": "1", "yieldUomId": portion}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": nasiRecipe, "itemId": rice, "quantity": "200", "uomId": g})
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": nasiRecipe, "itemId": egg, "quantity": "1", "uomId": pcs})
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": nasiRecipe, "subRecipeId": sambal, "quantity": "30", "uomId": g})
	sa.Must(201, "POST", "/api/v1/inventory/modifier-impacts", map[string]any{"modifierId": extraEgg, "action": "add", "itemId": egg, "quantity": "1", "uomId": pcs})
	// 0.2 kg × 15,000 + 1 × 2,500 + 30 g × (16,000 + 3,000) / 500 g = 3,000 + 2,500 + 1,140
	cost := sa.Must(200, "GET", "/api/v1/inventory/recipes/"+nasiRecipe+"/cost", nil).JSON()
	if cost["unitCost"] != "6640" || cost["foodCostPercent"] != "7.81" {
		t.Fatalf("recipe cost: %v", cost)
	}
	if menu := sa.Must(200, "GET", "/api/v1/commercial/outlets/"+outlet+"/menu?channel=caddy_tablet", nil).Items(); len(menu) != 2 {
		t.Fatalf("menu: %v", menu)
	}

	// Shift and a 4-person dinner split per item, each paid differently incl. member charge.
	shift := sa.Must(201, "POST", "/api/v1/commercial/shifts:open", map[string]any{"outletId": outlet, "openingCash": "1000000"}).JSON()
	sid := str(shift["id"])
	dinner := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": sid, "tableNo": "12", "guestCount": 4, "send": true,
		"lines": []map[string]any{{"productId": nasi, "modifierIds": []string{extraEgg}}, {"productId": nasi}, {"productId": teh, "quantity": "2"}, {"productId": nasi}}}).JSON()
	lines := dinner["lines"].([]any)
	l0 := lines[0].(map[string]any)
	// 85,000 + 8,000 = 93,000 ++ (5% + 10% on net+service) = 107,415
	if l0["totalAmount"] != "107415" {
		t.Fatalf("priced line: %v", l0)
	}
	tickets := sa.Must(200, "GET", "/api/v1/commercial/kitchen-orders", nil).Items()
	stations := map[string]bool{}
	for _, tk := range tickets {
		if tk["orderId"] == dinner["id"] {
			stations[str(tk["station"])] = true
		}
	}
	if !stations["kitchen"] || !stations["bar"] {
		t.Fatalf("tickets per station: %v", tickets)
	}
	did := str(dinner["id"])
	split := sa.Must(200, "POST", "/api/v1/commercial/orders/"+did+":split", map[string]any{"mode": "per_item", "bills": []map[string]any{
		{"label": "Hendra", "customerId": f.CustomerA, "lineIds": []any{l0["id"]}},
		{"label": "Budi", "lineIds": []any{lines[1].(map[string]any)["id"]}},
		{"label": "Teh", "lineIds": []any{lines[2].(map[string]any)["id"]}},
		{"label": "Rina", "lineIds": []any{lines[3].(map[string]any)["id"]}}}}).JSON()
	bills := split["bills"].([]any)
	methods := []map[string]any{{"methodType": "member_account"}, {"methodType": "qris", "reference": "QR-1"}, {"methodType": "cash"}, {"methodType": "card", "reference": "EDC-9"}}
	var paid map[string]any
	for i, b := range bills {
		paid = sa.Must(200, "POST", "/api/v1/commercial/orders/"+did+":pay", map[string]any{"billId": b.(map[string]any)["id"], "shiftId": sid,
			"tenders": []map[string]any{methods[i]}}, "Idempotency-Key", newKey()).JSON()
	}
	if paid["status"] != "paid" {
		t.Fatalf("dinner paid: %v", paid["status"])
	}
	// member charge reached the member account (statement shows pos line)
	st := sa.Must(200, "GET", "/api/v1/billing/member-accounts/"+f.AccountA+"/statement", nil).JSON()
	foundPOS := false
	for _, l := range st["lines"].([]any) {
		if strings.Contains(str(l.(map[string]any)["description"]), "Signing bill") && l.(map[string]any)["businessLine"] == "pos" {
			foundPOS = true
		}
	}
	if !foundPOS {
		t.Fatalf("member charge on statement: %v", st["lines"])
	}
	// Kitchen states; SSE delivers the Ready state in < 2 s.
	var kitchenTicket string
	for _, tk := range tickets {
		if tk["orderId"] == dinner["id"] && tk["station"] == "kitchen" {
			kitchenTicket = str(tk["id"])
		}
	}
	sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+kitchenTicket+":state", map[string]any{"state": "preparing"})
	lat := sseWait(t, sa, "/api/v1/commercial/kds/stream", "ticket_ready", func() {
		sa.Do("POST", "/api/v1/commercial/kitchen-orders/"+kitchenTicket+":state", map[string]any{"state": "ready"})
	})
	if lat > 2*time.Second {
		t.Fatalf("KDS latency %v", lat)
	}
	sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+kitchenTicket+":state", map[string]any{"state": "served"})
	if r := sa.Do("POST", "/api/v1/commercial/kitchen-orders/"+kitchenTicket+":state", map[string]any{"state": "preparing"}); r.Status != 409 {
		t.Fatalf("served → preparing: %s", r)
	}

	// On-course order from the caddy tablet at hole 6 reaches the halfway house KDS in < 2 s.
	sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "AIR", "name": "Air Mineral", "productType": "beverage", "price": "15000",
		"kitchenStation": "halfway", "outletIds": []string{halfway}})
	var air string
	sysQueryRow(t, inst, `SELECT id::text FROM commercial.products WHERE code = 'AIR'`, nil, &air)
	var onCourse map[string]any
	lat = sseWait(t, sa, "/api/v1/commercial/kds/stream", "ticket_received", func() {
		onCourse = sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": halfway, "orderType": "on_course", "source": "caddy_tablet",
			"customerId": f.CustomerA, "servingDestination": "hole", "destinationRef": "6", "lines": []map[string]any{{"productId": air, "quantity": "4"}}}).JSON()
	})
	if lat > 2*time.Second {
		t.Fatalf("on-course order latency %v", lat)
	}
	oc := sa.Must(200, "GET", "/api/v1/commercial/kitchen-orders?station=halfway", nil).Items()
	if len(oc) == 0 {
		t.Fatal("halfway ticket")
	}
	sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+str(oc[0]["id"])+":state", map[string]any{"state": "ready"})
	sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+str(oc[0]["id"])+":state", map[string]any{"state": "out_for_delivery"})
	sa.Must(200, "POST", "/api/v1/commercial/kitchen-orders/"+str(oc[0]["id"])+":state", map[string]any{"state": "served"})
	if s := sa.Must(200, "GET", "/api/v1/commercial/orders/"+str(onCourse["id"]), nil).JSON()["serviceStatus"]; s != "served" {
		t.Fatalf("served visible to the tablet: %v", s)
	}
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(onCourse["id"])+":pay", map[string]any{"tenders": []map[string]any{{"methodType": "member_account"}}})

	// Discount: cashier capped by POS Policies; supervisor may exceed.
	cashier := roleUser(t, inst, "cashier")
	small := cashier.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "lines": []map[string]any{{"productId": nasi}}}).JSON()
	sl := small["lines"].([]any)[0].(map[string]any)
	if r := cashier.Do("POST", "/api/v1/commercial/orders/"+str(small["id"])+"/lines/"+str(sl["id"])+":discount", map[string]any{"percent": "50", "reason": "friend"}); r.Status != 403 {
		t.Fatalf("cashier 50%% discount must need a supervisor: %s", r)
	}
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(small["id"])+"/lines/"+str(sl["id"])+":discount", map[string]any{"percent": "10", "reason": "member birthday"})
	sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(small["id"])+"/lines/"+str(sl["id"])+":discount", map[string]any{"percent": "50", "reason": "service recovery"})
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(small["id"])+"/lines", map[string]any{"lines": []map[string]any{{"productId": teh}}})
	sm := cashier.Must(200, "GET", "/api/v1/commercial/orders/"+str(small["id"]), nil).JSON()
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(small["id"])+"/lines/"+str(sm["lines"].([]any)[1].(map[string]any)["id"])+":void", map[string]any{"reason": "wrong item"})
	cashier.Must(200, "POST", "/api/v1/commercial/orders/"+str(small["id"])+":void", map[string]any{"reason": "guest left"})

	// Charge to a running stay / reservation folio; equal split; refund.
	stayFolio := idOf(sa.Must(201, "POST", "/api/v1/billing/folios", map[string]any{"folioType": "walk_in", "businessLine": "stay", "customerId": f.CustomerB}))
	ch2 := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "servingDestination": "bungalow", "destinationRef": "E-01",
		"lines": []map[string]any{{"productId": nasi}}}).JSON()
	if c := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(ch2["id"])+":charge", map[string]any{"folioId": stayFolio}).JSON(); c["status"] != "charged" {
		t.Fatalf("charge to stay: %v", c["status"])
	}
	eq := sa.Must(201, "POST", "/api/v1/commercial/orders", map[string]any{"outletId": outlet, "shiftId": sid, "lines": []map[string]any{{"productId": nasi, "quantity": "3"}}}).JSON()
	eqs := sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(eq["id"])+":split", map[string]any{"mode": "equal", "persons": 3}).JSON()
	for _, b := range eqs["bills"].([]any) {
		sa.Must(200, "POST", "/api/v1/commercial/orders/"+str(eq["id"])+":pay", map[string]any{"billId": b.(map[string]any)["id"], "shiftId": sid,
			"tenders": []map[string]any{{"methodType": "cash"}}})
	}
	eqDone := sa.Must(200, "GET", "/api/v1/commercial/orders/"+str(eq["id"]), nil).JSON()
	if eqDone["status"] != "paid" {
		t.Fatalf("equal split paid: %v", eqDone["bills"])
	}
	ref := sa.Must(202, "POST", "/api/v1/commercial/orders/"+str(eq["id"])+":refund", map[string]any{"reason": "food quality"}).JSON()
	if ref["status"] != "applied" {
		t.Fatalf("refund: %v", ref)
	}
	if st := sa.Must(200, "GET", "/api/v1/commercial/orders/"+str(eq["id"]), nil).JSON()["status"]; st != "refunded" {
		t.Fatalf("refunded: %v", st)
	}
	// Retail by barcode, receipt, favourites, repeat.
	lk := sa.Must(200, "GET", "/api/v1/commercial/products:lookup?barcode=8991234567891", nil).JSON()
	if lk["name"] != "Golf Shirt (L)" {
		t.Fatalf("barcode: %v", lk)
	}
	rec := sa.Must(200, "GET", "/api/v1/commercial/orders/"+did+"/receipt", nil).JSON()
	if !strings.Contains(str(rec["text"]), "TOTAL") {
		t.Fatalf("receipt: %v", rec)
	}
	sa.Must(202, "POST", "/api/v1/commercial/orders/"+did+":send-receipt", map[string]any{"email": "hendra@p2.test"})
	if fav := sa.Must(200, "GET", "/api/v1/commercial/customers/"+f.CustomerA+"/favorites", nil).Items(); len(fav) == 0 {
		t.Fatal("favorites")
	}
	sa.Must(201, "POST", "/api/v1/commercial/orders/"+str(onCourse["id"])+":repeat", map[string]any{"outletId": halfway})

	// Offline POS: 40 sales queued, synced twice (retry) — no duplicates.
	var items []map[string]any
	for i := 0; i < 40; i++ {
		oid := uuid.Must(uuid.NewV7()).String()
		items = append(items, map[string]any{"id": newKey(), "action": "commercial.pos_order", "payload": map[string]any{
			"order":   map[string]any{"id": oid, "outletId": outlet, "shiftId": sid, "lines": []map[string]any{{"productId": teh}}},
			"payment": map[string]any{"shiftId": sid, "tenders": []map[string]any{{"methodType": "cash"}}}}})
	}
	r1 := sa.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()
	for _, x := range r1["results"].([]any) {
		if x.(map[string]any)["status"] != "accepted" {
			t.Fatalf("offline sync: %v", x)
		}
	}
	r2 := sa.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": items}).JSON()
	for _, x := range r2["results"].([]any) {
		if x.(map[string]any)["status"] != "duplicate" {
			t.Fatalf("resync must be duplicate: %v", x)
		}
	}
	// resubmitting the same order id under a new queue item is also harmless
	again := items[0]
	again["id"] = newKey()
	sa.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{again}})
	var orders int
	sysQueryRow(t, inst, `SELECT count(*) FROM commercial.orders WHERE shift_id = $1 AND offline`, []any{mustUUID(sid)}, &orders)
	if orders != 40 {
		t.Fatalf("offline orders %d", orders)
	}
	sa.Must(200, "POST", "/api/v1/commercial/shifts/"+sid+"/cash-movements", map[string]any{"kind": "cash_out", "amount": "50000", "reason": "petty cash"})
	x := sa.Must(200, "GET", "/api/v1/commercial/shifts/"+sid+"/report", nil).JSON()
	z := sa.Must(200, "POST", "/api/v1/commercial/shifts/"+sid+":close", map[string]any{"countedCash": x["expectedCash"]}).JSON()
	var payTotal float64
	for _, p := range z["payments"].([]any) {
		var v float64
		_, _ = fmt.Sscan(str(p.(map[string]any)["amount"]), &v)
		payTotal += v
	}
	var sales float64
	_, _ = fmt.Sscan(str(z["sales"]), &sales)
	var refunded float64
	sysQueryRow(t, inst, `SELECT coalesce(sum(amount),0)::float FROM billing.payments WHERE shift_id = $1 AND kind = 'refund'`, []any{mustUUID(sid)}, &refunded)
	if z["kind"] != "z" || payTotal+0.0001 < sales-refunded-1 || payTotal > sales+1 {
		t.Fatalf("Z report: sales %v payments %v refunded %v", sales, payTotal, refunded)
	}
	if z["shift"].(map[string]any)["variance"] != "0" {
		t.Fatalf("variance: %v", z["shift"])
	}
	if n := len(sa.Must(200, "GET", "/api/v1/commercial/shifts?filter[status]=closed", nil).Items()); n < 1 {
		t.Fatal("shifts")
	}
	if n := len(sa.Must(200, "GET", "/api/v1/commercial/orders?filter[shiftId]="+sid, nil).Items()); n < 40 {
		t.Fatalf("orders list %d", n)
	}
	// Theoretical consumption and food cost after the outbox dispatch.
	waitFor(t, 20*time.Second, "consumption ledger", func() bool {
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM inventory.consumption_ledger WHERE item_id = $1`, []any{mustUUID(rice)}, &n)
		return n >= 3
	})
	var eggs string
	sysQueryRow(t, inst, `SELECT trim_scale(sum(quantity))::text FROM inventory.consumption_ledger WHERE order_id = $1 AND item_id = $2`, []any{mustUUID(did), mustUUID(egg)}, &eggs)
	if eggs != "4" { // 3 nasi goreng + 1 extra egg
		t.Fatalf("egg consumption %s", eggs)
	}
	fc := sa.Must(200, "GET", "/api/v1/inventory/food-cost?groupBy=product", nil).Items()
	foundCost := false
	for _, r := range fc {
		if r["name"] == "Nasi Goreng" && r["theoreticalCost"] != "0" && r["linesWithoutRecipe"].(float64) == 0 {
			foundCost = true
		}
	}
	if !foundCost {
		t.Fatalf("food cost: %v", fc)
	}
	if n := len(sa.Must(200, "GET", "/api/v1/inventory/consumption", nil).Items()); n < 4 {
		t.Fatalf("consumption %d", n)
	}
	sa.Must(200, "GET", "/api/v1/inventory/food-cost?groupBy=outlet", nil)
	_ = garlic
}
