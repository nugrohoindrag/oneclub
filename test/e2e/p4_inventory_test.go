package e2e

import (
	"context"
	"fmt"
	"oneclub/internal/accounting"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

func init() {
	resourceCRUDSkip["inventory.supplier_item"] = "needs a supplier: covered by TestP4InventoryMaster"
}

// ── helpers ───────────────────────────────────────────────────────────────

// invSfx is a unique code suffix per call.
func invSfx() string { return fmt.Sprint(time.Now().UnixNano() % 1e8) }

// invPublish publishes a domain event of the MAIN property (as the owning
// module would) and returns its id.
func invPublish(t *testing.T, typ string, agg uuid.UUID, payload any) uuid.UUID {
	t.Helper()
	ctx := dbtx.System(context.Background())
	p := inst.Main
	var eid uuid.UUID
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		eid, err = inst.App.Bus.Publish(ctx, tx, typ, "test."+typ, &agg, &p, payload)
		return err
	}); err != nil {
		t.Fatalf("publish %s: %v", typ, err)
	}
	return eid
}

// invHandled dispatches the outbox until a subscriber processed the event.
func invHandled(t *testing.T, eid uuid.UUID, subscriber string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		var n int
		sysQueryRow(t, inst, `SELECT count(*) FROM platform.outbox_processed WHERE subscriber = $1 AND event_id = $2`, []any{subscriber, eid}, &n)
		if n > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	var lastErr *string
	sysQueryRow(t, inst, `SELECT last_error FROM platform.outbox WHERE id = $1`, []any{eid}, &lastErr)
	t.Fatalf("event %s not handled by %s (last error %v)", eid, subscriber, lastErr)
}

// invEvents returns the payloads of outbox events of a type for an aggregate.
func invEvents(t *testing.T, typ string, agg string) []string {
	t.Helper()
	var out []string
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload::text FROM platform.outbox WHERE event_type = $1 AND aggregate_id::text = $2 ORDER BY occurred_at`, typ, agg)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func invEq(t *testing.T, what string, got any, want string) {
	t.Helper()
	if !dec(got).Equal(decimal.RequireFromString(want)) {
		t.Fatalf("%s: got %v, want %s", what, got, want)
	}
}

// invKit is the item master of one test.
type invKit struct {
	sfx                  string
	kg, g, btl, ctn, pcs string // UOM ids
	cat, fifoCat         string
}

func newInvKit(t *testing.T, sa *Client) invKit {
	t.Helper()
	k := invKit{sfx: invSfx()}
	uom := func(code, kind string) string {
		return idOf(sa.Must(201, "POST", "/api/v1/inventory/uoms", map[string]any{"code": code + k.sfx, "name": code, "kind": kind}))
	}
	k.kg, k.g, k.btl, k.ctn, k.pcs = uom("KG", "mass"), uom("G", "mass"), uom("BTL", "count"), uom("CTN", "count"), uom("PCS", "count")
	sa.Must(201, "POST", "/api/v1/inventory/uom-conversions", map[string]any{"fromUomId": k.kg, "toUomId": k.g, "factor": "1000"})
	k.cat = idOf(sa.Must(201, "POST", "/api/v1/inventory/categories", map[string]any{"code": "CAT" + k.sfx, "name": "Dry Goods " + k.sfx,
		"valuationMethod": "moving_average", "opnameTolerancePercent": "2"}))
	k.fifoCat = idOf(sa.Must(201, "POST", "/api/v1/inventory/categories", map[string]any{"code": "FIF" + k.sfx, "name": "FIFO Goods " + k.sfx,
		"valuationMethod": "fifo", "parentId": k.cat}))
	return k
}

func (k invKit) item(t *testing.T, sa *Client, code, uom string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"code": code + k.sfx, "name": code + " " + k.sfx, "baseUomId": uom, "categoryId": k.cat, "itemType": "raw", "standardCost": "1"}
	for key, v := range extra {
		body[key] = v
	}
	return idOf(sa.Must(201, "POST", "/api/v1/inventory/items", body))
}

func (k invKit) warehouse(t *testing.T, sa *Client, code string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"code": code + k.sfx, "name": code + " " + k.sfx, "locationType": "store"}
	for key, v := range extra {
		body[key] = v
	}
	return idOf(sa.Must(201, "POST", "/api/v1/inventory/warehouses", body))
}

// invReceive posts a goods receipt through procurement.goods_received.
func invReceive(t *testing.T, wh string, lines ...map[string]any) uuid.UUID {
	t.Helper()
	gr := uuid.New()
	for _, l := range lines {
		if l["baseUnitCost"] == nil {
			l["baseUnitCost"] = l["unitCost"]
		}
		if l["baseQuantity"] == nil {
			l["baseQuantity"] = l["quantity"]
		}
	}
	eid := invPublish(t, "procurement.goods_received", gr, map[string]any{"goodsReceiptId": gr, "number": "GR-" + invSfx(), "purchaseOrderId": uuid.New(),
		"poNumber": "PO-T", "supplierId": uuid.Nil, "warehouseId": wh, "receivedDate": time.Now().In(clubLoc(inst)).Format("2006-01-02"), "currency": "IDR",
		"total": "0", "lines": lines})
	invHandled(t, eid, "inventory.goods_receipt")
	return gr
}

func invOnHand(t *testing.T, c *Client, wh, item string) decimal.Decimal {
	t.Helper()
	total := decimal.Zero
	for _, r := range c.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+wh+"&itemId="+item+"&includeZero=true", nil).Items() {
		total = total.Add(dec(r["quantity"]))
	}
	return total
}

func invToday() string { return time.Now().In(clubLoc(inst)).Format("2006-01-02") }

// ── EP-01 item master & setup, EP-27 configuration ───────────────────────

// FR-INV-01..06: hierarchical categories with valuation and accounts,
// item master (types, barcode, tracking, consignment, retail product),
// carton barcodes and UOM conversions, supplier items, warehouses with
// their replenishing store (no cycles), stock locations, par stock and
// reorder points; barcode scanning; Inventory Policies / Configuration.
func TestP4InventoryMaster(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	// Category cycle refused.
	sa.Must(422, "PATCH", "/api/v1/inventory/categories/"+k.cat, map[string]any{"parentId": k.fifoCat})
	// Item master with tracking and barcodes.
	water := k.item(t, sa, "WTR", k.btl, map[string]any{"purchaseUomId": k.ctn, "barcode": "899" + k.sfx, "itemType": "consumable", "trackExpiry": true,
		"storageCondition": "ambient"})
	sa.Must(201, "POST", "/api/v1/inventory/uom-conversions", map[string]any{"itemId": water, "fromUomId": k.ctn, "toUomId": k.btl, "factor": "24"})
	sa.Must(201, "POST", "/api/v1/inventory/item-barcodes", map[string]any{"itemId": water, "barcode": "CTN899" + k.sfx, "uomId": k.ctn})
	if r := sa.Do("POST", "/api/v1/inventory/items", map[string]any{"code": "DUP" + k.sfx, "name": "dup", "baseUomId": k.btl, "barcode": "CTN899" + k.sfx}); r.Status != 422 {
		t.Fatalf("a barcode used as carton barcode must be refused: %s", r)
	}
	if r := sa.Do("POST", "/api/v1/inventory/items", map[string]any{"code": "CSG" + k.sfx, "name": "consignment", "baseUomId": k.pcs, "consignment": true}); r.Status != 422 {
		t.Fatalf("a consignment item needs its supplier: %s", r)
	}
	scan := sa.Must(200, "GET", "/api/v1/inventory/barcodes/CTN899"+k.sfx, nil).JSON()
	if scan["itemId"] != water || !dec(scan["baseFactor"]).Equal(decimal.NewFromInt(24)) || !strings.Contains(fmt.Sprint(scan["tracking"]), "expiry") {
		t.Fatalf("carton barcode scan: %v", scan)
	}
	sa.Must(404, "GET", "/api/v1/inventory/barcodes/NOPE"+k.sfx, nil)
	item := sa.Must(200, "GET", "/api/v1/inventory/items/"+water, nil).JSON()
	if item["category"] != "Dry Goods "+k.sfx || item["trackExpiry"] != true {
		t.Fatalf("item master: %v", item)
	}
	// Supplier items (purchase UOM, last price).
	sup := idOf(sa.Must(201, "POST", "/api/v1/procurement/suppliers", map[string]any{"code": "SUP" + k.sfx, "name": "Aqua Distributor " + k.sfx}))
	si := idOf(sa.Must(201, "POST", "/api/v1/inventory/supplier-items", map[string]any{"itemId": water, "supplierId": sup, "supplierItemCode": "AQ-600",
		"purchaseUomId": k.ctn, "lastPrice": "3000", "preferred": true}))
	sa.Must(200, "PATCH", "/api/v1/inventory/supplier-items/"+si, map[string]any{"leadTimeDays": 2})
	sa.Must(204, "DELETE", "/api/v1/inventory/supplier-items/"+si, nil)
	sa.Must(201, "POST", "/api/v1/inventory/items", map[string]any{"code": "CSG" + k.sfx, "name": "Consignment putter", "baseUomId": k.pcs, "itemType": "retail",
		"consignment": true, "consignmentSupplierId": sup, "consignmentCommissionPercent": "10"})
	// Warehouse structure (§7.5) without cycles.
	main := k.warehouse(t, sa, "MS", nil)
	kit := k.warehouse(t, sa, "KT", map[string]any{"parentId": main, "locationType": "kitchen", "costCenter": "kitchen"})
	sa.Must(422, "PATCH", "/api/v1/inventory/warehouses/"+main, map[string]any{"parentId": kit})
	loc := idOf(sa.Must(201, "POST", "/api/v1/inventory/locations", map[string]any{"warehouseId": main, "code": "BIN" + k.sfx, "name": "Bin A1", "locationType": "shelf"}))
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": water, "warehouseId": kit, "parLevel": "48", "minStock": "12", "stockLocationId": loc})
	sa.Must(201, "POST", "/api/v1/inventory/reorder-points", map[string]any{"itemId": water, "warehouseId": main, "reorderPoint": "100", "preferredSupplierId": sup})
	sa.Must(422, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": water, "warehouseId": kit, "parLevel": "10"})
	// EP-27: Inventory Policies and Inventory Configuration in the club policies catalogue.
	found := map[string]bool{}
	for _, e := range sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		found[str(e["code"])] = true
	}
	if !found["inventory.policy"] || !found["inventory.configuration"] {
		t.Fatalf("inventory policies missing from the catalogue: %v", found)
	}
	// Resource definitions drive the Back Office master data screens.
	keys := map[string]bool{}
	for _, d := range sa.Must(200, "GET", "/api/v1/platform/resource-definitions", nil).Items() {
		keys[str(d["key"])] = true
	}
	for _, key := range []string{"inventory.item_category", "inventory.warehouse", "inventory.stock_location", "inventory.par_stock", "inventory.reorder_point",
		"inventory.asset", "inventory.asset_category", "inventory.maintenance_schedule", "inventory.supplier_item", "inventory.item_barcode"} {
		if !keys[key] {
			t.Fatalf("resource definition %s missing", key)
		}
	}
}

// ── EP-02 stock movement & balance, EP-05 valuation ───────────────────────

// AC EP-05: 10 kg @ 100,000 then 10 kg @ 120,000 average 110,000; issuing 5 kg
// records COGS 550,000. FIFO consumes the oldest layer first. Balance = Σ
// movements, the ledger is append-only, negative stock is refused, a closed
// accounting period (K10) refuses documents and moves late automatic postings
// to today, purchase returns and reversals post at their cost, and unknown
// references become posting exceptions.
func TestP4InventoryValuation(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "VAL", nil)
	beef := k.item(t, sa, "BEEF", k.kg, nil)
	salmon := k.item(t, sa, "SALMON", k.kg, map[string]any{"categoryId": k.fifoCat})
	invReceive(t, wh, map[string]any{"itemId": beef, "quantity": "10", "unitCost": "100000"}, map[string]any{"itemId": salmon, "quantity": "10", "unitCost": "100000"})
	invReceive(t, wh, map[string]any{"itemId": beef, "quantity": "10", "unitCost": "120000"}, map[string]any{"itemId": salmon, "quantity": "10", "unitCost": "120000"})
	bal := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+wh+"&itemId="+beef, nil).Items()
	if len(bal) != 1 {
		t.Fatalf("balance rows: %v", bal)
	}
	invEq(t, "average cost", bal[0]["unitCost"], "110000")
	invEq(t, "value", bal[0]["value"], "2200000")

	// Issue Stock to a cost center: COGS at average cost.
	iss := sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "kitchen", "reason": "Staff meal",
		"lines": []map[string]any{{"itemId": beef, "quantity": "5"}, {"itemId": salmon, "quantity": "15"}}}, "Idempotency-Key", newKey()).JSON()
	if iss["status"] != "issued" || iss["requestType"] != "issue" {
		t.Fatalf("issue: %v", iss)
	}
	issID := str(iss["id"])
	movs := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?itemId="+beef+"&filter[movementType]=issue", nil).Items()
	if len(movs) != 1 {
		t.Fatalf("issue movements: %v", movs)
	}
	mv := sa.Must(200, "GET", "/api/v1/inventory/stock-movements/"+str(movs[0]["id"]), nil).JSON()
	cost := map[string]decimal.Decimal{}
	for _, l := range mv["lines"].([]any) {
		lm := l.(map[string]any)
		cost[str(lm["itemId"])] = cost[str(lm["itemId"])].Add(dec(lm["totalCost"]))
	}
	invEq(t, "COGS of 5 kg beef (moving average)", cost[beef], "-550000")
	invEq(t, "COGS of 15 kg salmon (FIFO 10 × 100,000 + 5 × 120,000)", cost[salmon], "-1600000")
	if ev := invEvents(t, "inventory.movement_posted", str(mv["id"])); len(ev) != 1 || !strings.Contains(ev[0], `"movementType": "issue"`) ||
		!strings.Contains(ev[0], `"totalCost": "-2150000"`) || !strings.Contains(ev[0], `"costCenter": "kitchen"`) {
		t.Fatalf("inventory.movement_posted: %v", ev)
	}
	// Negative stock is refused for manual movements.
	if r := sa.Do("POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "kitchen", "reason": "too much",
		"lines": []map[string]any{{"itemId": beef, "quantity": "100"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "insufficient_stock") {
		t.Fatalf("negative stock: %s", r)
	}
	// Balance = Σ movements (stock card); the ledger is append-only.
	card := sa.Must(200, "GET", "/api/v1/inventory/stock-card?itemId="+beef+"&warehouseId="+wh+"&from="+invToday()+"&to="+invToday(), nil).JSON()
	invEq(t, "stock card closing", card["closingQuantity"], "15")
	invEq(t, "stock card on hand", card["onHand"], "15")
	if len(card["lines"].([]any)) != 3 {
		t.Fatalf("stock card lines: %v", card["lines"])
	}
	ctx := dbtx.System(context.Background())
	if err := inst.DB.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE inventory.stock_movement_lines SET quantity = 1 WHERE item_id = $1`, beef)
		return err
	}); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("movement lines must be append-only: %v", err)
	}
	// Reversal of the issue (returned to stock) at the same cost.
	sa.Must(422, "POST", "/api/v1/inventory/stock-movements/"+str(mv["id"])+":reverse", map[string]any{})
	rev := sa.Must(200, "POST", "/api/v1/inventory/stock-movements/"+str(mv["id"])+":reverse", map[string]any{"reason": "Not used"}).JSON()
	invEq(t, "reversal value", rev["totalCost"], "2150000")
	sa.Must(409, "POST", "/api/v1/inventory/stock-movements/"+str(mv["id"])+":reverse", map[string]any{"reason": "again"})
	invEq(t, "beef after reversal", invOnHand(t, sa, wh, beef), "20")
	if r := sa.Must(200, "GET", "/api/v1/inventory/requisitions/"+issID, nil).JSON(); len(r["issues"].([]any)) != 1 {
		t.Fatalf("issue fulfilment: %v", r)
	}

	// Purchase return at the receipt cost.
	pr := uuid.New()
	eid := invPublish(t, "procurement.purchase_returned", pr, map[string]any{"purchaseReturnId": pr, "number": "PRT-" + k.sfx, "goodsReceiptId": uuid.New(),
		"supplierId": uuid.New(), "warehouseId": wh, "currency": "IDR", "total": "240000",
		"lines": []map[string]any{{"itemId": beef, "baseQuantity": "2", "baseUnitCost": "120000", "totalCost": "240000"}}})
	invHandled(t, eid, "inventory.purchase_return")
	invEq(t, "beef after return", invOnHand(t, sa, wh, beef), "18")
	if m := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+pr.String(), nil).Items(); len(m) != 1 || m[0]["movementType"] != "return_out" {
		t.Fatalf("return movement: %v", m)
	}

	// K10: a closed period refuses documents dated in it; late events post today.
	loc := clubLoc(inst)
	last := time.Now().In(loc).AddDate(0, -1, 0)
	inst.App.Stock.Service.SetPeriodGuard(func(_ context.Context, _ dbtx.Querier, _ uuid.UUID, d time.Time) (string, error) {
		if d.Year() == last.Year() && d.Month() == last.Month() {
			return "closed", nil
		}
		return "open", nil
	})
	defer inst.App.Stock.Service.SetPeriodGuard(accounting.PeriodStatus)
	if r := sa.Do("POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": wh, "reason": "found", "businessDate": last.Format("2006-01-02"),
		"submit": true, "lines": []map[string]any{{"itemId": beef, "quantity": "1", "unitCost": "110000"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "period_closed") {
		t.Fatalf("closed period: %s", r)
	}
	gr := uuid.New()
	eid = invPublish(t, "procurement.goods_received", gr, map[string]any{"goodsReceiptId": gr, "number": "GR-LATE", "warehouseId": wh,
		"receivedDate": last.Format("2006-01-02"), "lines": []map[string]any{{"itemId": beef, "baseQuantity": "1", "baseUnitCost": "110000"}}})
	invHandled(t, eid, "inventory.goods_receipt")
	if m := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+gr.String(), nil).Items(); len(m) != 1 || m[0]["businessDate"] != invToday() {
		t.Fatalf("late receipt in a closed period: %v", m)
	}
	// The wired guard reads Accounting's periods (K10).
	inst.App.Stock.Service.SetPeriodGuard(accounting.PeriodStatus)
	var whProperty uuid.UUID
	sysQueryRow(t, inst, `SELECT property_id FROM inventory.warehouses WHERE id = $1`, []any{mustUUID(wh)}, &whProperty)
	sysExec(t, inst, `INSERT INTO accounting.periods (id, property_id, year, month, start_date, end_date, status)
		VALUES ($1, $2, 2020, 3, '2020-03-01', '2020-03-31', 'closed')`, uuid.New(), whProperty)
	t.Cleanup(func() {
		sysExec(t, inst, `DELETE FROM accounting.periods WHERE property_id = $1 AND year = 2020 AND month = 3`, whProperty)
	})
	if r := sa.Do("POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": wh, "reason": "found", "businessDate": "2020-03-15",
		"submit": true, "lines": []map[string]any{{"itemId": beef, "quantity": "1", "unitCost": "110000"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "period_closed") {
		t.Fatalf("period closed in Accounting: %s", r)
	}

	// Unknown warehouse → posting exception, resolved by staff.
	bad := uuid.New()
	eid = invPublish(t, "procurement.goods_received", bad, map[string]any{"goodsReceiptId": bad, "number": "GR-BAD", "warehouseId": uuid.New(),
		"lines": []map[string]any{{"itemId": beef, "baseQuantity": "1", "baseUnitCost": "1"}}})
	invHandled(t, eid, "inventory.goods_receipt")
	var xid string
	for _, x := range sa.Must(200, "GET", "/api/v1/inventory/posting-exceptions?filter[status]=open", nil).Items() {
		if x["sourceId"] == bad.String() {
			xid = str(x["id"])
		}
	}
	if xid == "" {
		t.Fatal("posting exception missing")
	}
	sa.Must(422, "POST", "/api/v1/inventory/posting-exceptions/"+xid+":resolve", map[string]any{})
	sa.Must(200, "POST", "/api/v1/inventory/posting-exceptions/"+xid+":resolve", map[string]any{"resolution": "Received again at the right store"})
	sa.Must(409, "POST", "/api/v1/inventory/posting-exceptions/"+xid+":resolve", map[string]any{"resolution": "twice"})

	// FR-VAL-05 / EP-28 AC: valuation as of today = dashboard Inventory Value
	// = Stock Valuation Report.
	val := sa.Must(200, "GET", "/api/v1/inventory/valuation?groupBy=item&warehouseId="+wh, nil).JSON()
	invEq(t, "valuation of the warehouse", val["total"], dec(sa.Must(200, "GET", "/api/v1/inventory/valuation?groupBy=warehouse&warehouseId="+wh, nil).JSON()["total"]).String())
	all := sa.Must(200, "GET", "/api/v1/inventory/valuation?groupBy=category", nil).JSON()
	dash := sa.Must(200, "GET", "/api/v1/reporting/dashboards/inventory-performance?from="+invToday()+"&to="+invToday(), nil).JSON()
	var kpiValue any
	for _, kp := range dash["kpis"].([]any) {
		if km := kp.(map[string]any); km["key"] == "inventory_value" {
			kpiValue = km["value"]
		}
	}
	invEq(t, "Inventory Value = Stock Valuation", kpiValue, str(all["total"]))
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/inventory.stock_valuation?params[from]="+invToday()+"&params[to]="+invToday(), nil).JSON()
	sum := decimal.Zero
	for _, r := range rep["rows"].([]any) {
		sum = sum.Add(dec(r.(map[string]any)["value"]))
	}
	if sum.Sub(dec(all["total"])).Abs().GreaterThan(decimal.NewFromFloat(0.01).Mul(decimal.NewFromInt(int64(len(rep["rows"].([]any)) + 1)))) {
		t.Fatalf("Stock Valuation Report %s vs valuation %v", sum, all["total"])
	}
}

// FR-VAL-06: the invoiced price differs from the receipt cost after part of
// the goods was used: the stock part is revalued, the used part goes to
// COGS (inventory.revaluation_posted), once per invoice and receipt.
// FR-PRD-04: serial numbers from the goods receipt until sold.
func TestP4InventoryRevaluationSerial(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "SR", nil)
	flour := k.item(t, sa, "FLOUR", k.kg, nil)
	gr := invReceive(t, wh, map[string]any{"itemId": flour, "quantity": "10", "unitCost": "10000"})
	sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "bakery", "reason": "Bread",
		"lines": []map[string]any{{"itemId": flour, "quantity": "4"}}})
	vi := uuid.New()
	pv := map[string]any{"vendorInvoiceId": vi, "number": "VI-" + k.sfx, "goodsReceiptId": gr, "warehouseId": wh,
		"lines": []map[string]any{{"itemId": flour, "invoicedBaseUnitCost": "11000"}}}
	invHandled(t, invPublish(t, "procurement.invoice_price_variance", vi, pv), "inventory.price_revaluation")
	invHandled(t, invPublish(t, "procurement.invoice_price_variance", vi, pv), "inventory.price_revaluation")
	invEq(t, "revalued stock (6 kg × 11,000)", sa.Must(200, "GET", "/api/v1/inventory/valuation?warehouseId="+wh, nil).JSON()["total"], "66000")
	invEq(t, "quantity unchanged", invOnHand(t, sa, wh, flour), "6")
	if m := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?warehouseId="+wh+"&filter[sourceType]=revaluation", nil).Items(); len(m) != 1 {
		t.Fatalf("revaluation movements: %v", m)
	}
	if ev := invEvents(t, "inventory.revaluation_posted", vi.String()); len(ev) != 1 || !strings.Contains(ev[0], `"consumedAmount": "4000"`) ||
		!strings.Contains(ev[0], `"stockAmount": "6000"`) || !strings.Contains(ev[0], `"consumedTo": "cogs"`) {
		t.Fatalf("revaluation_posted: %v", ev)
	}

	// Serial numbers: received with the GR, the oldest is sold through POS.
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "SO" + k.sfx, "name": "Pro Shop S " + k.sfx, "outletType": "retail"}))
	prod := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "DR" + k.sfx, "name": "Driver " + k.sfx, "productType": "retail", "price": "8000000"}))
	shop := k.warehouse(t, sa, "SS", map[string]any{"locationType": "outlet", "outletId": outlet})
	driver := k.item(t, sa, "DRIVER", k.pcs, map[string]any{"itemType": "retail", "trackSerial": true, "productId": prod})
	invReceive(t, shop, map[string]any{"itemId": driver, "quantity": "3", "unitCost": "5000000", "serialNos": []string{"A1" + k.sfx, "A2" + k.sfx, "A3" + k.sfx}})
	sa.Must(422, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": shop, "costCenter": "demo", "reason": "Demo club",
		"lines": []map[string]any{{"itemId": driver, "quantity": "1"}}})
	order := uuid.New()
	invHandled(t, invPublish(t, "commercial.sale_completed", order, map[string]any{"orderId": order, "orderNo": "S-" + k.sfx, "outletId": outlet,
		"lines": []map[string]any{{"lineId": uuid.New(), "productId": prod, "quantity": "1", "netAmount": "8000000"}}}), "inventory.stock_consumption")
	status := func(sn string) string {
		var st string
		sysQueryRow(t, inst, `SELECT status FROM inventory.serials WHERE serial_no = $1`, []any{sn}, &st)
		return st
	}
	if status("A1"+k.sfx) != "sold" || status("A2"+k.sfx) != "in_stock" {
		t.Fatalf("serial status: %s / %s", status("A1"+k.sfx), status("A2"+k.sfx))
	}
	iss := sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": shop, "costCenter": "demo", "reason": "Demo club",
		"lines": []map[string]any{{"itemId": driver, "quantity": "1", "serialNos": []string{"A3" + k.sfx}}}}).JSON()
	if iss["status"] != "issued" || status("A3"+k.sfx) != "issued" {
		t.Fatalf("serial issue: %v", iss)
	}
}

// linesByItem maps the lines of a document by item id.
func linesByItem(doc map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, l := range doc["lines"].([]any) {
		lm := l.(map[string]any)
		out[str(lm["itemId"])] = lm
	}
	return out
}

// ── EP-03 store requisition & stock transfer ──────────────────────────────

// FR-REQ-01..04: a kitchen requisition from `ops` is approved per Inventory
// Policies (no workflow ⇒ approved at once), fulfilled partially by the
// warehouse, the shortage becomes a purchase request (reorder_needed), the
// rest is cancelled; department requisitions issue to a cost center;
// transfers go In Transit and record the shipped − received difference.
func TestP4InventoryRequisitionTransfer(t *testing.T) {
	sa := superAdmin(t, inst)
	ks := roleUser(t, inst, "kitchen_staff")
	ws := roleUser(t, inst, "warehouse_staff")
	k := newInvKit(t, sa)
	main := k.warehouse(t, sa, "RS", nil)
	kit := k.warehouse(t, sa, "RK", map[string]any{"parentId": main, "locationType": "kitchen"})
	rice := k.item(t, sa, "RICE", k.kg, nil)
	oil := k.item(t, sa, "OIL", k.btl, nil)
	invReceive(t, main, map[string]any{"itemId": rice, "quantity": "20", "unitCost": "15000"}, map[string]any{"itemId": oil, "quantity": "10", "unitCost": "25000"})
	tomorrow := time.Now().In(clubLoc(inst)).AddDate(0, 0, 1).Format("2006-01-02")

	r := ks.Must(201, "POST", "/api/v1/inventory/requisitions", map[string]any{"requestType": "store", "requestingWarehouseId": kit, "neededBy": tomorrow,
		"origin": "ops", "submit": true, "lines": []map[string]any{{"itemId": rice, "quantity": "30"}, {"itemId": oil, "quantity": "2", "uomId": k.btl}}},
		"Idempotency-Key", newKey()).JSON()
	if r["status"] != "approved" || r["sourceWarehouseId"] != main || r["origin"] != "ops" || r["neededBy"] != tomorrow || dec(r["estimatedValue"]).IsZero() {
		t.Fatalf("submitted requisition: %v", r)
	}
	rid := str(r["id"])
	ls := linesByItem(r)
	// Partial fulfilment: all the rice the store has, 2 bottles of oil.
	ks.Must(403, "POST", "/api/v1/inventory/requisitions/"+rid+":fulfill", map[string]any{})
	ws.Must(422, "POST", "/api/v1/inventory/requisitions/"+rid+":fulfill", map[string]any{"lines": []map[string]any{{"lineId": ls[rice]["id"], "quantity": "31"}}})
	r = ws.Must(200, "POST", "/api/v1/inventory/requisitions/"+rid+":fulfill", map[string]any{"lines": []map[string]any{{"lineId": ls[rice]["id"], "quantity": "20"},
		{"lineId": ls[oil]["id"], "quantity": "2"}}, "notes": "First batch"}).JSON()
	ls = linesByItem(r)
	if r["status"] != "partially_issued" || !dec(ls[rice]["remainingQuantity"]).Equal(decimal.NewFromInt(10)) || len(r["issues"].([]any)) != 1 {
		t.Fatalf("partial fulfilment: %v", r)
	}
	invEq(t, "kitchen rice", invOnHand(t, sa, kit, rice), "20")
	invEq(t, "store rice", invOnHand(t, sa, main, rice), "0")
	// What the store cannot supply becomes a purchase request.
	ws.Must(200, "POST", "/api/v1/inventory/requisitions/"+rid+":request-purchase", nil)
	if ev := invEvents(t, "inventory.reorder_needed", rid); len(ev) != 1 || !strings.Contains(ev[0], `"suggestedQuantity": "10"`) || !strings.Contains(ev[0], rice) {
		t.Fatalf("reorder_needed of the requisition: %v", ev)
	}
	r = ks.Must(200, "POST", "/api/v1/inventory/requisitions/"+rid+":cancel", map[string]any{"reason": "Menu changed"}).JSON()
	if r["status"] != "closed" {
		t.Fatalf("closing the rest: %v", r)
	}
	ws.Must(409, "POST", "/api/v1/inventory/requisitions/"+rid+":fulfill", map[string]any{})
	// Draft → submit → cancel.
	d := sa.Must(201, "POST", "/api/v1/inventory/requisitions", map[string]any{"requestingWarehouseId": kit, "lines": []map[string]any{{"itemId": oil, "quantity": "1"}}}).JSON()
	if d["status"] != "draft" || d["requestType"] != "store" {
		t.Fatalf("draft: %v", d)
	}
	sa.Must(200, "POST", "/api/v1/inventory/requisitions/"+str(d["id"])+":submit", nil)
	sa.Must(409, "POST", "/api/v1/inventory/requisitions/"+str(d["id"])+":submit", nil)
	if c := sa.Must(200, "POST", "/api/v1/inventory/requisitions/"+str(d["id"])+":cancel", map[string]any{"reason": "Duplicate"}).JSON(); c["status"] != "cancelled" {
		t.Fatalf("cancel: %v", c)
	}
	// Department requisition: issue to a cost center.
	dep := sa.Must(201, "POST", "/api/v1/inventory/requisitions", map[string]any{"requestType": "department", "sourceWarehouseId": main, "costCenter": "housekeeping",
		"submit": true, "lines": []map[string]any{{"itemId": oil, "quantity": "1"}}}).JSON()
	dep = ws.Must(200, "POST", "/api/v1/inventory/requisitions/"+str(dep["id"])+":fulfill", map[string]any{}).JSON()
	if dep["status"] != "issued" {
		t.Fatalf("department requisition: %v", dep)
	}
	if items := sa.Must(200, "GET", "/api/v1/inventory/requisitions?warehouseId="+kit+"&filter[status]=closed,cancelled", nil).Items(); !containsID(items, rid) ||
		!containsID(items, str(d["id"])) {
		t.Fatalf("requisition list: %v", items)
	}

	// Stock Transfer: In Transit, received with a difference.
	tr := ws.Must(201, "POST", "/api/v1/inventory/transfers", map[string]any{"fromWarehouseId": main, "toWarehouseId": kit, "notes": "Weekly",
		"lines": []map[string]any{{"itemId": oil, "quantity": "5"}}}, "Idempotency-Key", newKey()).JSON()
	tid := str(tr["id"])
	tr = ws.Must(200, "POST", "/api/v1/inventory/transfers/"+tid+":ship", nil).JSON()
	if tr["status"] != "in_transit" || tr["transitWarehouseId"] == nil {
		t.Fatalf("shipped: %v", tr)
	}
	invEq(t, "store oil after shipping", invOnHand(t, sa, main, oil), "2")
	invEq(t, "in transit", invOnHand(t, sa, str(tr["transitWarehouseId"]), oil), "5")
	line := tr["lines"].([]any)[0].(map[string]any)
	ks.Must(422, "POST", "/api/v1/inventory/transfers/"+tid+":receive", map[string]any{"lines": []map[string]any{{"lineId": line["id"], "receivedQuantity": "6"}}})
	tr = ks.Must(200, "POST", "/api/v1/inventory/transfers/"+tid+":receive", map[string]any{"lines": []map[string]any{{"lineId": line["id"], "receivedQuantity": "4",
		"reason": "1 bottle broken"}}}).JSON()
	if tr["status"] != "received" || !dec(tr["discrepancyValue"]).Equal(decimal.NewFromInt(25000)) {
		t.Fatalf("received: %v", tr)
	}
	invEq(t, "kitchen oil", invOnHand(t, sa, kit, oil), "6")
	invEq(t, "nothing left in transit", invOnHand(t, sa, str(tr["transitWarehouseId"]), oil), "0")
	if m := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+tid, nil).Items(); len(m) != 5 {
		t.Fatalf("transfer movements (out, in transit, out of transit, in, difference): %v", m)
	}
	t2 := sa.Must(201, "POST", "/api/v1/inventory/transfers", map[string]any{"fromWarehouseId": kit, "toWarehouseId": main,
		"lines": []map[string]any{{"itemId": rice, "quantity": "1"}}}).JSON()
	sa.Must(200, "POST", "/api/v1/inventory/transfers/"+str(t2["id"])+":cancel", map[string]any{"reason": "Not needed"})
	sa.Must(409, "POST", "/api/v1/inventory/transfers/"+str(t2["id"])+":ship", nil)
	if items := sa.Must(200, "GET", "/api/v1/inventory/transfers?filter[status]=received&warehouseId="+kit, nil).Items(); !containsID(items, tid) {
		t.Fatalf("transfer list: %v", items)
	}
	sa.Must(200, "GET", "/api/v1/inventory/transfers/"+tid, nil)
}

// ── EP-04 stock opname & adjustment ───────────────────────────────────────

// AC EP-04: system 120 bottles, counted 117: above the tolerance the
// variance needs approval; after approval an opname adjustment of −3 at the
// average cost is posted (journal of the inventory difference through
// inventory.movement_posted). Blind count, freeze, carton barcode scans with
// offline replay (Idempotency-Key), recount; manual adjustments with reason.
func TestP4InventoryOpname(t *testing.T) {
	sa := superAdmin(t, inst)
	gm := roleUser(t, inst, "general_manager")
	ws := roleUser(t, inst, "warehouse_staff")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "OP", nil)
	beer := k.item(t, sa, "BEER", k.btl, map[string]any{"purchaseUomId": k.ctn, "barcode": "BB" + k.sfx})
	sa.Must(201, "POST", "/api/v1/inventory/uom-conversions", map[string]any{"itemId": beer, "fromUomId": k.ctn, "toUomId": k.btl, "factor": "24"})
	sa.Must(201, "POST", "/api/v1/inventory/item-barcodes", map[string]any{"itemId": beer, "barcode": "BC" + k.sfx, "uomId": k.ctn})
	cola := k.item(t, sa, "COLA", k.btl, nil)
	invReceive(t, wh, map[string]any{"itemId": beer, "quantity": "5", "uomId": k.ctn, "unitCost": "480000", "baseQuantity": "120", "baseUnitCost": "20000"},
		map[string]any{"itemId": cola, "quantity": "50", "unitCost": "5000"})
	bal := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+wh+"&itemId="+beer, nil).Items()[0]
	if bal["packQuantity"] != "5 CTN"+k.sfx {
		t.Fatalf("pack quantity: %v", bal)
	}
	// PRD P4 §16 #8: the seeded Stock Opname workflow — above tolerance the Finance Manager approves.
	fm := roleUser(t, inst, "finance_manager")

	o := ws.Must(201, "POST", "/api/v1/inventory/stock-opnames", map[string]any{"warehouseId": wh, "freeze": true, "blind": true}, "Idempotency-Key", newKey()).JSON()
	oid := str(o["id"])
	if o["status"] != "in_progress" || o["lineCount"].(float64) != 2 || linesByItem(o)[beer]["systemQuantity"] != nil {
		t.Fatalf("started opname (blind): %v", o)
	}
	ws.Must(409, "POST", "/api/v1/inventory/stock-opnames", map[string]any{"warehouseId": wh})
	if r := sa.Do("POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wh, "costCenter": "bar", "reason": "x",
		"lines": []map[string]any{{"itemId": cola, "quantity": "1"}}}); r.Status != 409 || !strings.Contains(string(r.Body), "warehouse_frozen") {
		t.Fatalf("frozen warehouse: %s", r)
	}
	// Scans: 4 cartons (carton barcode, replayed offline with the same key) + 21 bottles.
	key := newKey()
	body := map[string]any{"lines": []map[string]any{{"barcode": "BC" + k.sfx, "quantity": "4", "mode": "add"}}}
	ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":count", body, "Idempotency-Key", key)
	ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":count", body, "Idempotency-Key", key)
	o = ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":count", map[string]any{"lines": []map[string]any{{"barcode": "BB" + k.sfx, "quantity": "21",
		"mode": "add"}, {"itemId": cola, "quantity": "50"}}}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "counted beer (offline replay counted once)", linesByItem(o)[beer]["countedQuantity"], "117")
	ws.Must(422, "POST", "/api/v1/inventory/stock-opnames/"+oid+":count", map[string]any{"lines": []map[string]any{{"barcode": "UNKNOWN", "quantity": "1"}}})
	o = ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":submit", map[string]any{}).JSON()
	if o["status"] != "counted" || o["overTolerance"].(float64) != 1 {
		t.Fatalf("counted: %v", o)
	}
	invEq(t, "variance", linesByItem(o)[beer]["varianceQuantity"], "-3")
	// Recount the line above tolerance: same result.
	o = ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":recount", map[string]any{}).JSON()
	if o["status"] != "in_progress" || linesByItem(o)[beer]["countedQuantity"] != nil || linesByItem(o)[beer]["recounts"].(float64) != 1 {
		t.Fatalf("recount: %v", o)
	}
	ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":count", map[string]any{"lines": []map[string]any{{"itemId": beer, "quantity": "117"}}})
	ws.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":submit", map[string]any{})
	ws.Must(403, "POST", "/api/v1/inventory/stock-opnames/"+oid+":post", nil)
	o = sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+oid+":post", nil).JSON()
	if o["status"] != "pending_approval" || o["approvalRequestId"] == nil {
		t.Fatalf("variance above tolerance needs approval: %v", o)
	}
	invEq(t, "nothing posted before approval", invOnHand(t, sa, wh, beer), "120")
	if r := gm.Do("POST", "/api/v1/platform/approvals/"+str(o["approvalRequestId"])+":approve", map[string]any{"reason": "Breakage confirmed"}); r.Status < 400 {
		t.Fatalf("the General Manager is not the seeded opname approver: %s", r)
	}
	fm.Must(200, "POST", "/api/v1/platform/approvals/"+str(o["approvalRequestId"])+":approve", map[string]any{"reason": "Breakage confirmed"})
	o = sa.Must(200, "GET", "/api/v1/inventory/stock-opnames/"+oid, nil).JSON()
	if o["status"] != "posted" || o["movementId"] == nil {
		t.Fatalf("posted opname: %v", o)
	}
	invEq(t, "variance value at average cost", o["varianceValue"], "-60000")
	invEq(t, "beer after opname", invOnHand(t, sa, wh, beer), "117")
	if ev := invEvents(t, "inventory.movement_posted", str(o["movementId"])); len(ev) != 1 || !strings.Contains(ev[0], `"movementType": "opname"`) ||
		!strings.Contains(ev[0], `"totalCost": "-60000"`) {
		t.Fatalf("inventory difference journal event: %v", ev)
	}
	// Within tolerance: posted without approval; a cancelled count.
	o2 := sa.Must(201, "POST", "/api/v1/inventory/stock-opnames", map[string]any{"warehouseId": wh, "itemIds": []string{cola}, "blind": false, "freeze": false}).JSON()
	if linesByItem(o2)[cola]["systemQuantity"] == nil {
		t.Fatalf("an open count shows the system quantity: %v", o2)
	}
	sa.Must(422, "POST", "/api/v1/inventory/stock-opnames/"+str(o2["id"])+":submit", map[string]any{})
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o2["id"])+":submit", map[string]any{"zeroUncounted": true})
	o2 = sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o2["id"])+":post", nil).JSON()
	if o2["status"] != "pending_approval" {
		// zero counted against 50 is above tolerance: approval required
		t.Fatalf("zero count: %v", o2)
	}
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o2["id"])+":cancel", map[string]any{"reason": "Counted the wrong shelf"})
	o3 := sa.Must(201, "POST", "/api/v1/inventory/stock-opnames", map[string]any{"warehouseId": wh, "itemIds": []string{cola}, "blind": false}).JSON()
	// FR-OPS-P4-04: a count recorded offline on the warehouse device syncs once.
	qi := map[string]any{"id": uuid.Must(uuid.NewV7()).String(), "action": "inventory.opname_count", "clientTime": time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{"opnameId": o3["id"], "lines": []map[string]any{{"itemId": cola, "quantity": "25", "mode": "add"}}}}
	for i, want := range []string{"accepted", "duplicate"} {
		res := ws.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{qi}}).JSON()["results"].([]any)[0].(map[string]any)
		if res["status"] != want {
			t.Fatalf("offline count sync %d: %v", i, res)
		}
	}
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o3["id"])+":count", map[string]any{"lines": []map[string]any{{"itemId": cola, "quantity": "25",
		"mode": "add"}}})
	sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o3["id"])+":submit", map[string]any{})
	if o3 = sa.Must(200, "POST", "/api/v1/inventory/stock-opnames/"+str(o3["id"])+":post", nil).JSON(); o3["status"] != "posted" || o3["movementId"] != nil {
		t.Fatalf("count without variance: %v", o3)
	}
	if items := sa.Must(200, "GET", "/api/v1/inventory/stock-opnames?warehouseId="+wh+"&filter[status]=posted", nil).Items(); len(items) != 2 {
		t.Fatalf("opname list: %v", items)
	}

	// FR-OPN-03 Stock Adjustment with reason (no workflow ⇒ posted on submit).
	sa.Must(422, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": wh, "reason": "lost", "lines": []map[string]any{{"itemId": cola, "quantity": "-1"}}})
	a := ws.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": wh, "reason": "damage", "notes": "Dropped crate",
		"lines": []map[string]any{{"itemId": cola, "quantity": "-2"}}}, "Idempotency-Key", newKey()).JSON()
	if a["status"] != "draft" {
		t.Fatalf("draft adjustment: %v", a)
	}
	a = ws.Must(200, "POST", "/api/v1/inventory/adjustments/"+str(a["id"])+":submit", nil).JSON()
	if a["status"] != "posted" || !dec(a["totalValue"]).Equal(decimal.NewFromInt(-10000)) {
		t.Fatalf("posted adjustment: %v", a)
	}
	a2 := sa.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": wh, "reason": "found", "lines": []map[string]any{{"itemId": cola,
		"quantity": "1", "unitCost": "5000"}}}).JSON()
	sa.Must(200, "POST", "/api/v1/inventory/adjustments/"+str(a2["id"])+":cancel", map[string]any{"reason": "Miscounted"})
	sa.Must(409, "POST", "/api/v1/inventory/adjustments/"+str(a2["id"])+":submit", nil)
	if items := sa.Must(200, "GET", "/api/v1/inventory/adjustments?warehouseId="+wh, nil).Items(); len(items) != 2 {
		t.Fatalf("adjustment list: %v", items)
	}
	sa.Must(200, "GET", "/api/v1/inventory/adjustments/"+str(a["id"]), nil)
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/inventory.opname_variance?params[from]="+invToday()+"&params[to]="+invToday(), nil).JSON()
	if !strings.Contains(fmt.Sprint(rep["rows"]), str(o["number"])) {
		t.Fatalf("Stock Opname Variance Report: %v", rep["rows"])
	}
}

// ── EP-06 automatic consumption, EP-07 production / waste, consignment ───

// FR-CNS-01..06 / FR-PRD-01,02,05 / FR-INV-05,07: a POS sale (K7) consumes the
// outlet kitchen per recipe with the produced semi-finished sub-recipe, and
// the same event received twice deducts once (AC); package (K6) and banquet
// (K6) consumption; the BEO (K1) schedules banquet production, a revision
// replaces it; production with actual yield costs the output from its
// ingredients; waste; theoretical vs actual per outlet; 100 golf rounds with a
// one-bottle service BOM take 100 bottles = 4 cartons + 4 bottles from Golf
// Ops (AC); consignment sold through POS is payable to the supplier and
// settled monthly.
func TestP4InventoryConsumption(t *testing.T) {
	sa := superAdmin(t, inst)
	ks := roleUser(t, inst, "kitchen_staff")
	k := newInvKit(t, sa)
	outlet := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "IO" + k.sfx, "name": "Spike Bar " + k.sfx, "outletType": "restaurant"}))
	product := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "NG" + k.sfx, "name": "Nasi Goreng " + k.sfx, "productType": "food",
		"price": "50000"}))
	main := k.warehouse(t, sa, "CM", nil)
	kit := k.warehouse(t, sa, "CK", map[string]any{"parentId": main, "locationType": "kitchen", "outletId": outlet})
	rice := k.item(t, sa, "RICE", k.kg, map[string]any{"standardCost": "15000"})
	chili := k.item(t, sa, "CHILI", k.kg, map[string]any{"standardCost": "40000"})
	sambal := k.item(t, sa, "SAMBAL", k.g, map[string]any{"itemType": "semi_finished"})
	sub := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "SMB" + k.sfx, "name": "Sambal " + k.sfx, "recipeType": "sub_recipe",
		"outputItemId": sambal, "yieldQuantity": "1000", "yieldUomId": k.g}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": sub, "itemId": chili, "quantity": "0.5", "uomId": k.kg})
	menu := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "MNG" + k.sfx, "name": "Nasi Goreng " + k.sfx, "recipeType": "menu",
		"productId": product, "yieldQuantity": "1"}))
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": menu, "itemId": rice, "quantity": "0.2", "uomId": k.kg})
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": menu, "subRecipeId": sub, "quantity": "50", "uomId": k.g})
	invReceive(t, kit, map[string]any{"itemId": rice, "quantity": "10", "unitCost": "15000"}, map[string]any{"itemId": chili, "quantity": "5", "unitCost": "40000"})

	// FR-PRD-01 production with actual yield.
	po := ks.Must(201, "POST", "/api/v1/inventory/production-orders", map[string]any{"recipeId": sub, "warehouseId": kit, "plannedQuantity": "2000",
		"scheduledFor": invToday()}, "Idempotency-Key", newKey()).JSON()
	if po["status"] != "draft" || len(po["inputs"].([]any)) != 1 {
		t.Fatalf("production order: %v", po)
	}
	invEq(t, "planned chili", po["inputs"].([]any)[0].(map[string]any)["plannedQuantity"], "1")
	po = ks.Must(200, "POST", "/api/v1/inventory/production-orders/"+str(po["id"])+":complete", map[string]any{"actualQuantity": "1800"}).JSON()
	if po["status"] != "completed" {
		t.Fatalf("completed: %v", po)
	}
	invEq(t, "input cost", po["inputCost"], "40000")
	invEq(t, "output unit cost", po["outputUnitCost"], "22.222222")
	invEq(t, "sambal produced", invOnHand(t, sa, kit, sambal), "1800")
	invEq(t, "chili left", invOnHand(t, sa, kit, chili), "4")
	po2 := ks.Must(201, "POST", "/api/v1/inventory/production-orders", map[string]any{"recipeId": sub, "warehouseId": kit, "plannedQuantity": "500"}).JSON()
	ks.Must(200, "POST", "/api/v1/inventory/production-orders/"+str(po2["id"])+":cancel", map[string]any{"reason": "Enough sambal"})
	ks.Must(409, "POST", "/api/v1/inventory/production-orders/"+str(po2["id"])+":complete", map[string]any{})
	sa.Must(422, "POST", "/api/v1/inventory/production-orders", map[string]any{"recipeId": menu, "warehouseId": kit, "plannedQuantity": "1"})

	// FR-CNS-01 / AC: the same POS event twice deducts once.
	order := uuid.New()
	sale := map[string]any{"orderId": order, "orderNo": "ORD-" + k.sfx, "outletId": outlet, "at": time.Now().UTC().Format(time.RFC3339),
		"lines": []map[string]any{{"lineId": uuid.New(), "productId": product, "quantity": "3", "modifierIds": []string{}, "netAmount": "150000"}}}
	invHandled(t, invPublish(t, "commercial.sale_completed", order, sale), "inventory.stock_consumption")
	invHandled(t, invPublish(t, "commercial.sale_completed", order, sale), "inventory.stock_consumption")
	if m := sa.Must(200, "GET", "/api/v1/inventory/stock-movements?sourceId="+order.String(), nil).Items(); len(m) != 1 || m[0]["movementType"] != "consumption" {
		t.Fatalf("sale consumption: %v", m)
	}
	invEq(t, "rice after 3 portions", invOnHand(t, sa, kit, rice), "9.4")
	invEq(t, "sambal after 3 portions (semi-finished from stock)", invOnHand(t, sa, kit, sambal), "1650")

	// K6 package and banquet consumption (idempotent per component / event).
	comp := uuid.New()
	pkg := map[string]any{"bookingId": uuid.New(), "bookingComponentId": comp, "packageId": uuid.New(), "componentType": "fnb", "consumedAt": time.Now().UTC().Format(time.RFC3339),
		"businessDate": invToday(), "quantity": "2", "outletId": outlet, "consumption": []map[string]any{{"itemId": rice, "quantity": "0.4", "uomId": k.kg}}}
	invHandled(t, invPublish(t, "commercial.package_consumed", comp, pkg), "inventory.package_consumption")
	invHandled(t, invPublish(t, "commercial.package_consumed", comp, pkg), "inventory.package_consumption")
	invEq(t, "rice after the package", invOnHand(t, sa, kit, rice), "9")
	ev := uuid.New()
	ban := map[string]any{"eventId": ev, "number": "EVT-" + k.sfx, "completedAt": time.Now().UTC().Format(time.RFC3339), "businessDate": invToday(), "finalPax": 10,
		"outletId": outlet, "consumption": []map[string]any{{"itemId": rice, "quantity": "2", "uomId": k.kg}, {"itemId": uuid.New(), "quantity": "1"}}}
	invHandled(t, invPublish(t, "banquet.event_completed", ev, ban), "inventory.banquet_consumption")
	invEq(t, "rice after the banquet", invOnHand(t, sa, kit, rice), "7")
	unknown := false
	for _, x := range sa.Must(200, "GET", "/api/v1/inventory/posting-exceptions", nil).Items() {
		unknown = unknown || (x["sourceId"] == ev.String() && x["reason"] == "unknown item")
	}
	if !unknown {
		t.Fatal("an unknown banquet item must become a posting exception")
	}

	// K1 / FR-PRD-05: BEO schedules the banquet production per pax; a revision replaces it.
	bev := uuid.New()
	eventDate := time.Now().In(clubLoc(inst)).AddDate(0, 0, 7)
	beo := map[string]any{"beoId": uuid.New(), "beoNumber": "BEO-" + k.sfx, "version": 1, "eventId": bev, "eventNumber": "EVT-B" + k.sfx,
		"eventDate": eventDate.Format("2006-01-02"), "pax": 100, "outletId": outlet,
		"requirements": []map[string]any{{"itemId": rice, "quantity": "20", "uomId": k.kg, "neededBy": eventDate.AddDate(0, 0, -1).Format("2006-01-02"),
			"source": "menu", "recipeId": menu}}}
	invHandled(t, invPublish(t, "banquet.beo_issued", bev, beo), "inventory.banquet_production_schedule")
	planned := func() map[string]any {
		var out map[string]any
		for _, p := range sa.Must(200, "GET", "/api/v1/inventory/production-orders?warehouseId="+kit+"&filter[status]=draft", nil).Items() {
			if p["sourceId"] == bev.String() {
				if out != nil {
					t.Fatalf("two production orders for the BEO")
				}
				out = p
			}
		}
		if out == nil || out["outputItemId"] != sambal {
			t.Fatalf("BEO production order: %v", out)
		}
		return out
	}
	p1 := planned()
	invEq(t, "sambal for 100 pax", p1["plannedQuantity"], "5000")
	if p1["scheduledFor"] != eventDate.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatalf("scheduled: %v", p1)
	}
	beo["version"], beo["pax"] = 2, 120
	invHandled(t, invPublish(t, "banquet.beo_revised", bev, beo), "inventory.banquet_production_revision")
	p2 := planned()
	invEq(t, "sambal for 120 pax", p2["plannedQuantity"], "6000")
	if p2["id"] != p1["id"] {
		t.Fatal("the revision must update the draft production order")
	}
	invEq(t, "chili planned for 6 kg sambal", sa.Must(200, "GET", "/api/v1/inventory/production-orders/"+str(p2["id"]), nil).JSON()["inputs"].([]any)[0].(map[string]any)["plannedQuantity"], "3")

	// FR-PRD-02 waste with reason.
	sa.Must(422, "POST", "/api/v1/inventory/waste", map[string]any{"warehouseId": kit, "reason": "eaten", "lines": []map[string]any{{"itemId": rice, "quantity": "1"}}})
	w := ks.Must(201, "POST", "/api/v1/inventory/waste", map[string]any{"warehouseId": kit, "reason": "spoiled", "notes": "Fridge failure",
		"lines": []map[string]any{{"itemId": rice, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "waste cost", w["totalCost"], "15000")
	if items := sa.Must(200, "GET", "/api/v1/inventory/waste?warehouseId="+kit+"&reason=spoiled", nil).Items(); !containsID(items, str(w["id"])) {
		t.Fatalf("waste list: %v", items)
	}
	sa.Must(200, "GET", "/api/v1/inventory/waste/"+str(w["id"]), nil)

	// FR-CNS-06 theoretical vs actual for the outlet kitchen.
	var riceRow map[string]any
	for _, r := range sa.Must(200, "GET", "/api/v1/inventory/consumption-variance?warehouseId="+kit+"&from="+invToday()+"&to="+invToday(), nil).Items() {
		if r["itemId"] == rice {
			riceRow = r
		}
	}
	if riceRow == nil {
		t.Fatal("variance row for rice missing")
	}
	invEq(t, "theoretical rice (3 portions)", riceRow["theoretical"], "0.6")
	invEq(t, "actual rice (10 received − 6 closing)", riceRow["actual"], "4")
	invEq(t, "rice variance", riceRow["variance"], "3.4")
	for _, code := range []string{"inventory.consumption_variance", "inventory.food_cost", "inventory.waste", "inventory.stock_movement"} {
		rep := sa.Must(200, "GET", "/api/v1/reporting/reports/"+code+"?params[from]="+invToday()+"&params[to]="+invToday(), nil).JSON()
		if len(rep["rows"].([]any)) == 0 {
			t.Fatalf("%s is empty", code)
		}
	}

	// AC FR-CNS-04: 100 golf rounds × 1 bottle at Golf Ops = 4 cartons + 4 bottles.
	var golfOps string
	sysQueryRow(t, inst, `SELECT id::text FROM inventory.warehouses WHERE property_id = $1 AND code = 'GOLF-OPS'`, []any{inst.Main}, &golfOps)
	water := k.item(t, sa, "WATER", k.btl, map[string]any{"purchaseUomId": k.ctn})
	sa.Must(201, "POST", "/api/v1/inventory/uom-conversions", map[string]any{"itemId": water, "fromUomId": k.ctn, "toUomId": k.btl, "factor": "24"})
	invReceive(t, golfOps, map[string]any{"itemId": water, "quantity": "10", "uomId": k.ctn, "unitCost": "72000", "baseQuantity": "240", "baseUnitCost": "3000"})
	svc := idOf(sa.Must(201, "POST", "/api/v1/inventory/recipes", map[string]any{"code": "GRB" + k.sfx, "name": "Golf round amenities", "recipeType": "service",
		"serviceRef": "golf.round", "yieldQuantity": "1"}))
	t.Cleanup(func() { sa.Must(200, "PATCH", "/api/v1/inventory/recipes/"+svc, map[string]any{"status": "inactive"}) })
	sa.Must(201, "POST", "/api/v1/inventory/recipe-lines", map[string]any{"recipeId": svc, "itemId": water, "quantity": "1", "uomId": k.btl})
	var flights []uuid.UUID
	for i := 0; i < 25; i++ {
		f := uuid.New()
		flights = append(flights, f)
		invPublish(t, "golf.round_finished", f, map[string]any{"flightId": f, "holesPlayed": 18, "players": 4})
	}
	for _, f := range flights {
		var eid uuid.UUID
		sysQueryRow(t, inst, `SELECT id FROM platform.outbox WHERE aggregate_id = $1 AND event_type = 'golf.round_finished'`, []any{f}, &eid)
		invHandled(t, eid, "inventory.golf_round_consumption")
	}
	var used string
	sysQueryRow(t, inst, `SELECT coalesce(-sum(l.quantity), 0)::text FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id
		WHERE m.source_type = 'golf_round' AND l.item_id = $1`, []any{water}, &used)
	invEq(t, "bottles used by 100 rounds", used, "100")
	gb := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+golfOps+"&itemId="+water, nil).Items()[0]
	if gb["packQuantity"] != "5 CTN"+k.sfx+" + 20 BTL"+k.sfx {
		t.Fatalf("Golf Ops water: %v", gb)
	}

	// FR-INV-07 consignment: supplier-owned stock, sold 1:1 through POS, settled monthly.
	sup := idOf(sa.Must(201, "POST", "/api/v1/procurement/suppliers", map[string]any{"code": "CS" + k.sfx, "name": "Putter Brand " + k.sfx}))
	pro := idOf(sa.Must(201, "POST", "/api/v1/commercial/outlets", map[string]any{"code": "PS" + k.sfx, "name": "Pro Shop " + k.sfx, "outletType": "retail"}))
	putterProduct := idOf(sa.Must(201, "POST", "/api/v1/commercial/products", map[string]any{"code": "PT" + k.sfx, "name": "Putter " + k.sfx, "productType": "retail",
		"price": "1500000"}))
	shop := k.warehouse(t, sa, "PS", map[string]any{"locationType": "outlet", "outletId": pro})
	putter := k.item(t, sa, "PUTTER", k.pcs, map[string]any{"itemType": "retail", "productId": putterProduct, "consignment": true, "consignmentSupplierId": sup,
		"consignmentCommissionPercent": "10"})
	other := k.item(t, sa, "GLOVE", k.pcs, nil)
	sa.Must(422, "POST", "/api/v1/inventory/consignment-movements", map[string]any{"direction": "receipt", "supplierId": sup, "warehouseId": shop,
		"lines": []map[string]any{{"itemId": other, "quantity": "1"}}})
	cm := sa.Must(201, "POST", "/api/v1/inventory/consignment-movements", map[string]any{"direction": "receipt", "supplierId": sup, "warehouseId": shop,
		"reference": "DN-001", "lines": []map[string]any{{"itemId": putter, "quantity": "5"}}}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "consignment has no value", cm["totalCost"], "0")
	sa.Must(201, "POST", "/api/v1/inventory/consignment-movements", map[string]any{"direction": "return", "supplierId": sup, "warehouseId": shop,
		"lines": []map[string]any{{"itemId": putter, "quantity": "1"}}})
	po3 := uuid.New()
	invHandled(t, invPublish(t, "commercial.sale_completed", po3, map[string]any{"orderId": po3, "orderNo": "PS-" + k.sfx, "outletId": pro,
		"lines": []map[string]any{{"lineId": uuid.New(), "productId": putterProduct, "quantity": "2", "netAmount": "3000000"}}}), "inventory.stock_consumption")
	invEq(t, "putters left", invOnHand(t, sa, shop, putter), "2")
	if cs := sa.Must(200, "GET", "/api/v1/inventory/consignment-stock?supplierId="+sup, nil).Items(); len(cs) != 1 || !dec(cs[0]["onHand"]).Equal(decimal.NewFromInt(2)) {
		t.Fatalf("consignment stock: %v", cs)
	}
	var sold string
	sysQueryRow(t, inst, `SELECT payload::text FROM platform.outbox WHERE event_type = 'inventory.consignment_sold' AND payload->>'sourceId' = $1`, []any{po3.String()}, &sold)
	if !strings.Contains(sold, `"totalCost": "2700000"`) || !strings.Contains(sold, sup) {
		t.Fatalf("inventory.consignment_sold: %s", sold)
	}
	period := time.Now().In(clubLoc(inst)).Format("2006-01")
	prev := sa.Must(200, "GET", "/api/v1/inventory/consignment-settlements?period="+period+"&supplierId="+sup, nil).Items()
	if len(prev) != 1 || !dec(prev[0]["payableAmount"]).Equal(decimal.NewFromInt(2700000)) || !dec(prev[0]["commissionAmount"]).Equal(decimal.NewFromInt(300000)) {
		t.Fatalf("settlement preview: %v", prev)
	}
	st := sa.Must(201, "POST", "/api/v1/inventory/consignment-settlements", map[string]any{"supplierId": sup, "period": period}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "settlement payable", st["payableAmount"], "2700000")
	sa.Must(422, "POST", "/api/v1/inventory/consignment-settlements", map[string]any{"supplierId": sup, "period": period})
}

// ── EP-08 replenishment, FR-PRD-03 expiry ─────────────────────────────────

// AC EP-08: par 50, reorder point 20, stock 18, on order 0 ⇒ one automatic
// PR of 32 per day, not duplicated when the job runs again; the goods
// receipt closes it. Sub-stores below par get a draft requisition. Low stock
// and slow moving lists; FEFO picking and H-N expiry alerts once per batch.
func TestP4InventoryReplenishment(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	ms := k.warehouse(t, sa, "RP", nil)
	sub := k.warehouse(t, sa, "RB", map[string]any{"parentId": ms, "locationType": "outlet"})
	soap := k.item(t, sa, "SOAP", k.pcs, nil)
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": soap, "warehouseId": ms, "parLevel": "50"})
	sa.Must(201, "POST", "/api/v1/inventory/reorder-points", map[string]any{"itemId": soap, "warehouseId": ms, "reorderPoint": "20"})
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": soap, "warehouseId": sub, "parLevel": "10", "minStock": "4"})
	sa.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": ms, "reason": "found", "submit": true,
		"lines": []map[string]any{{"itemId": soap, "quantity": "18", "unitCost": "5000"}}})
	var sg map[string]any
	for _, s := range sa.Must(200, "GET", "/api/v1/inventory/replenishment?warehouseId="+ms, nil).Items() {
		if s["itemId"] == soap {
			sg = s
		}
	}
	if sg == nil || sg["kind"] != "purchase" {
		t.Fatalf("suggestion: %v", sg)
	}
	invEq(t, "suggested PR (par − stock − on order)", sg["suggestedQuantity"], "32")
	pr := func(res map[string]any) map[string]any {
		for _, p := range res["purchase"].([]any) {
			if pm := p.(map[string]any); pm["itemId"] == soap && pm["warehouseId"] == ms {
				return pm
			}
		}
		return nil
	}
	run := sa.Must(200, "POST", "/api/v1/inventory/replenishment:run", nil).JSON()
	if p := pr(run); p == nil || !dec(p["suggestedQuantity"]).Equal(decimal.NewFromInt(32)) {
		t.Fatalf("automatic PR: %v", run)
	}
	if !strings.Contains(fmt.Sprint(run["requisitions"]), "SRQ") {
		t.Fatalf("sub-store requisition: %v", run)
	}
	again := sa.Must(200, "POST", "/api/v1/inventory/replenishment:run", nil).JSON()
	if pr(again) != nil {
		t.Fatalf("the PR must not be duplicated: %v", again)
	}
	if ev := invEvents(t, "inventory.reorder_needed", ms); len(ev) != 1 || !strings.Contains(ev[0], `"suggestedQuantity": "32"`) || !strings.Contains(ev[0], `"source": "reorder"`) {
		t.Fatalf("reorder_needed: %v", ev)
	}
	reqs := sa.Must(200, "GET", "/api/v1/inventory/requisitions?warehouseId="+sub+"&filter[status]=draft", nil).Items()
	if len(reqs) != 1 || reqs[0]["origin"] != "replenishment" || reqs[0]["sourceWarehouseId"] != ms {
		t.Fatalf("outlet requisition suggestion: %v", reqs)
	}
	// The goods receipt of the PR closes the purchase request.
	invReceive(t, ms, map[string]any{"itemId": soap, "quantity": "32", "unitCost": "5000"})
	var st string
	sysQueryRow(t, inst, `SELECT status FROM inventory.reorder_requests WHERE warehouse_id = $1 AND item_id = $2`, []any{ms, soap}, &st)
	if st != "received" {
		t.Fatalf("purchase request after receipt: %s", st)
	}
	// FR-RPL-04 low stock and slow moving.
	towel := k.item(t, sa, "TOWEL", k.pcs, nil)
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": towel, "warehouseId": ms, "parLevel": "20", "minStock": "10"})
	sa.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": ms, "reason": "found", "submit": true,
		"lines": []map[string]any{{"itemId": towel, "quantity": "5", "unitCost": "30000"}}})
	if low := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+ms+"&belowReorder=true", nil).Items(); len(low) != 1 || low[0]["itemId"] != towel {
		t.Fatalf("low stock: %v", low)
	}
	if slow := sa.Must(200, "GET", "/api/v1/inventory/slow-moving?days=0&warehouseId="+ms, nil).Items(); len(slow) != 2 {
		t.Fatalf("slow moving: %v", slow)
	}
	// FEFO and expiry alerts (H-N).
	loc := clubLoc(inst)
	yog := k.item(t, sa, "YOG", k.pcs, map[string]any{"trackExpiry": true})
	soon, later := time.Now().In(loc).AddDate(0, 0, 3).Format("2006-01-02"), time.Now().In(loc).AddDate(0, 0, 40).Format("2006-01-02")
	invReceive(t, ms, map[string]any{"itemId": yog, "quantity": "5", "unitCost": "8000", "batchNo": "L" + k.sfx, "expiryDate": later},
		map[string]any{"itemId": yog, "quantity": "10", "unitCost": "8000", "batchNo": "S" + k.sfx, "expiryDate": soon})
	picks := sa.Must(200, "GET", "/api/v1/inventory/pick-suggestions?warehouseId="+ms+"&itemId="+yog+"&quantity=12", nil).Items()
	if len(picks) != 2 || picks[0]["batchNo"] != "S"+k.sfx || picks[0]["pick"] != "10" || picks[1]["pick"] != "2" {
		t.Fatalf("FEFO picks: %v", picks)
	}
	sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": ms, "costCenter": "fnb", "reason": "Breakfast",
		"lines": []map[string]any{{"itemId": yog, "quantity": "3"}}})
	for _, b := range sa.Must(200, "GET", "/api/v1/inventory/stock-balances?warehouseId="+ms+"&itemId="+yog+"&byBatch=true", nil).Items() {
		if b["batchNo"] == "S"+k.sfx && !dec(b["quantity"]).Equal(decimal.NewFromInt(7)) {
			t.Fatalf("FEFO issue must take the batch expiring first: %v", b)
		}
	}
	exp := sa.Must(200, "GET", "/api/v1/inventory/expiry?days=7&warehouseId="+ms, nil).Items()
	if len(exp) != 1 || exp[0]["batchNo"] != "S"+k.sfx {
		t.Fatalf("expiring batches: %v", exp)
	}
	if _, err := inst.App.Stock.Service.RunDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.App.Stock.Service.RunDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	var alerts int
	sysQueryRow(t, inst, `SELECT count(*) FROM inventory.expiry_alerts a JOIN inventory.batches b ON b.id = a.batch_id WHERE b.item_id = $1`, []any{yog}, &alerts)
	if alerts != 1 {
		t.Fatalf("expiry alert once per batch: %d", alerts)
	}
	rep := sa.Must(200, "GET", "/api/v1/reporting/reports/inventory.expiry?params[days]=7", nil).JSON()
	if !strings.Contains(fmt.Sprint(rep["rows"]), "S"+k.sfx) {
		t.Fatalf("Expiry Report: %v", rep["rows"])
	}
	rep = sa.Must(200, "GET", "/api/v1/reporting/reports/inventory.low_stock", nil).JSON()
	if !strings.Contains(fmt.Sprint(rep["rows"]), "TOWEL"+k.sfx) {
		t.Fatalf("Low Stock Report: %v", rep["rows"])
	}
}

// ── EP-09 assets & equipment ──────────────────────────────────────────────

// FR-AST-01..06 / FR-OPS-P4-03 / FR-MIG-P4-05: asset register, rental out and
// back, usage hours and usage / time based maintenance schedules, Golf Staff
// spare part request, work orders that issue spare parts from Engineering,
// monthly straight-line depreciation (inventory.asset_depreciated, also by
// the daily job), disposal through approval, migrated assets with their
// accumulated depreciation.
func TestP4InventoryAssets(t *testing.T) {
	sa := superAdmin(t, inst)
	gs := roleUser(t, inst, "golf_staff")
	fm := roleUser(t, inst, "finance_manager")
	k := newInvKit(t, sa)
	loc := clubLoc(inst)
	now := time.Now().In(loc)
	cat := idOf(sa.Must(201, "POST", "/api/v1/inventory/asset-categories", map[string]any{"code": "AC" + k.sfx, "name": "Golf Carts " + k.sfx, "assetClass": "golf_cart",
		"depreciationMethod": "straight_line", "usefulLifeMonths": 60}))
	rentCat := idOf(sa.Must(201, "POST", "/api/v1/inventory/asset-categories", map[string]any{"code": "AR" + k.sfx, "name": "Rental " + k.sfx,
		"assetClass": "rental_equipment", "usefulLifeMonths": 36, "depreciationMethod": "declining_balance"}))
	eng := k.warehouse(t, sa, "EN", map[string]any{"costCenter": "engineering"})
	part := k.item(t, sa, "BATT", k.pcs, map[string]any{"itemType": "spare_part"})
	invReceive(t, eng, map[string]any{"itemId": part, "quantity": "4", "unitCost": "500000"})
	acq := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0).Format("2006-01-02")
	cart := sa.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "GC" + k.sfx, "name": "Buggy 07", "categoryId": cat, "acquisitionDate": acq,
		"acquisitionCost": "60000000", "warehouseId": eng, "serialNo": "EZ-" + k.sfx, "warrantyUntil": now.AddDate(1, 0, 0).Format("2006-01-02")}).JSON()
	cid := str(cart["id"])
	rental := idOf(sa.Must(201, "POST", "/api/v1/inventory/assets", map[string]any{"code": "RC" + k.sfx, "name": "Rental club set", "categoryId": rentCat,
		"acquisitionDate": acq, "acquisitionCost": "3000000", "rentable": true}))
	sa.Must(422, "PATCH", "/api/v1/inventory/assets/"+cid, map[string]any{"status": "disposed"})

	// FR-AST-04 rental out / back.
	if a := sa.Must(200, "POST", "/api/v1/inventory/assets/"+rental+":checkout", map[string]any{"customerRef": "M-001", "reference": "Tee time 08:00"}).JSON(); a["rentalStatus"] != "out" {
		t.Fatalf("checkout: %v", a)
	}
	sa.Must(409, "POST", "/api/v1/inventory/assets/"+rental+":checkout", map[string]any{})
	sa.Must(409, "POST", "/api/v1/inventory/assets/"+cid+":checkout", map[string]any{})
	if a := sa.Must(200, "POST", "/api/v1/inventory/assets/"+rental+":return", map[string]any{"notes": "Good condition"}).JSON(); a["rentalStatus"] != "available" {
		t.Fatalf("return: %v", a)
	}

	// FR-AST-02/03 usage hours and schedules.
	sched := idOf(sa.Must(201, "POST", "/api/v1/inventory/maintenance-schedules", map[string]any{"assetId": cid, "name": "Battery service", "triggerType": "usage_hours",
		"intervalHours": "100"}))
	sa.Must(422, "POST", "/api/v1/inventory/maintenance-schedules", map[string]any{"assetId": cid, "name": "No interval", "triggerType": "usage_hours"})
	tsched := sa.Must(201, "POST", "/api/v1/inventory/maintenance-schedules", map[string]any{"assetId": rental, "name": "Grip check", "triggerType": "time",
		"intervalDays": 30, "lastDoneOn": now.AddDate(0, 0, -40).Format("2006-01-02")}).JSON()
	if tsched["nextDueOn"] != now.AddDate(0, 0, -10).Format("2006-01-02") {
		t.Fatalf("next due: %v", tsched)
	}
	sa.Must(200, "POST", "/api/v1/inventory/assets/"+cid+":record-usage", map[string]any{"hours": "120", "reference": "Fleet hour meter"})
	due := func(id string) bool {
		for _, m := range sa.Must(200, "GET", "/api/v1/inventory/maintenance-due?dueOnly=true", nil).Items() {
			if m["id"] == id {
				return true
			}
		}
		return false
	}
	if !due(sched) || !due(str(tsched["id"])) {
		t.Fatal("usage and time based schedules must be due")
	}

	// FR-OPS-P4-03 Golf Staff spare part request.
	spr := gs.Must(201, "POST", "/api/v1/inventory/spare-part-requests", map[string]any{"assetId": cid, "warehouseId": eng, "notes": "Battery weak",
		"lines": []map[string]any{{"itemId": part, "quantity": "1"}}}, "Idempotency-Key", newKey()).JSON()
	if spr["requestType"] != "spare_part" || spr["status"] != "approved" || spr["assetCode"] != "GC"+k.sfx {
		t.Fatalf("spare part request: %v", spr)
	}
	sa.Must(200, "POST", "/api/v1/inventory/requisitions/"+str(spr["id"])+":fulfill", map[string]any{})
	// Work order with spare parts from Engineering.
	wo := sa.Must(201, "POST", "/api/v1/inventory/maintenance-records", map[string]any{"assetId": cid, "scheduleId": sched, "description": "Replace battery"},
		"Idempotency-Key", newKey()).JSON()
	if wo["maintenanceType"] != "preventive" || wo["status"] != "open" {
		t.Fatalf("work order: %v", wo)
	}
	sa.Must(200, "POST", "/api/v1/inventory/maintenance-records/"+str(wo["id"])+":start", nil)
	if a := sa.Must(200, "GET", "/api/v1/inventory/assets/"+cid, nil).JSON(); a["status"] != "maintenance" {
		t.Fatalf("asset in maintenance: %v", a)
	}
	wo = sa.Must(200, "POST", "/api/v1/inventory/maintenance-records/"+str(wo["id"])+":complete", map[string]any{"laborCost": "150000", "warehouseId": eng,
		"spareParts": []map[string]any{{"itemId": part, "quantity": "1"}}, "notes": "Done"}).JSON()
	if wo["status"] != "completed" {
		t.Fatalf("completed: %v", wo)
	}
	invEq(t, "parts cost", wo["partsCost"], "500000")
	invEq(t, "total cost", wo["totalCost"], "650000")
	invEq(t, "spare parts left", invOnHand(t, sa, eng, part), "2")
	if due(sched) {
		t.Fatal("the schedule restarts after the service")
	}
	wo2 := sa.Must(201, "POST", "/api/v1/inventory/maintenance-records", map[string]any{"assetId": rental, "description": "Loose grip"}).JSON()
	sa.Must(200, "POST", "/api/v1/inventory/maintenance-records/"+str(wo2["id"])+":cancel", map[string]any{"reason": "Fixed on the spot"})
	if items := sa.Must(200, "GET", "/api/v1/inventory/maintenance-records?assetId="+cid, nil).Items(); len(items) != 1 {
		t.Fatalf("work orders: %v", items)
	}
	sa.Must(200, "GET", "/api/v1/inventory/maintenance-records/"+str(wo["id"]), nil)
	h := sa.Must(200, "GET", "/api/v1/inventory/assets/"+cid+"/history", nil).JSON()
	if len(h["maintenance"].([]any)) != 1 || len(h["spareParts"].([]any)) != 2 || len(h["usages"].([]any)) != 1 {
		t.Fatalf("asset history: %v", h)
	}

	// FR-AST-05 depreciation: an earlier month by hand, the previous by the daily job.
	p2 := now.AddDate(0, -2, 0).Format("2006-01")
	run := sa.Must(201, "POST", "/api/v1/inventory/depreciation-runs", map[string]any{"period": p2}, "Idempotency-Key", newKey()).JSON()
	var cartLine map[string]any
	for _, l := range run["lines"].([]any) {
		if lm := l.(map[string]any); lm["assetId"] == cid {
			cartLine = lm
		}
	}
	if cartLine == nil {
		t.Fatalf("depreciation run: %v", run)
	}
	invEq(t, "straight line 60,000,000 / 60", cartLine["amount"], "1000000")
	sa.Must(409, "POST", "/api/v1/inventory/depreciation-runs", map[string]any{"period": p2})
	sa.Must(422, "POST", "/api/v1/inventory/depreciation-runs", map[string]any{"period": "2026/01"})
	if ev := invEvents(t, "inventory.asset_depreciated", str(run["id"])); len(ev) != 1 || !strings.Contains(ev[0], "GC"+k.sfx) || !strings.Contains(ev[0], p2) {
		t.Fatalf("asset_depreciated: %v", ev)
	}
	if runs := sa.Must(200, "GET", "/api/v1/inventory/depreciation-runs", nil).Items(); !containsID(runs, str(run["id"])) {
		t.Fatalf("runs: %v", runs)
	}
	sa.Must(200, "GET", "/api/v1/inventory/depreciation-runs/"+str(run["id"]), nil)
	if _, err := inst.App.Stock.Service.RunDaily(t.Context()); err != nil {
		t.Fatal(err)
	}
	a := sa.Must(200, "GET", "/api/v1/inventory/assets/"+cid, nil).JSON()
	invEq(t, "accumulated after two months", a["accumulatedDepreciation"], "2000000")
	invEq(t, "book value", a["bookValue"], "58000000")

	// FR-AST-06 disposal through approval.
	sa.Must(201, "POST", "/api/v1/platform/approval-workflows", map[string]any{"documentType": "asset_disposal", "name": "Asset disposal " + k.sfx,
		"steps": []map[string]any{{"stepNo": 1, "name": "Finance Manager", "approverType": "role", "approverRoleId": roleID(t, sa, "finance_manager")}}})
	sa.Must(422, "POST", "/api/v1/inventory/assets/"+rental+":dispose", map[string]any{})
	d := sa.Must(200, "POST", "/api/v1/inventory/assets/"+rental+":dispose", map[string]any{"reason": "Broken shafts", "proceeds": "250000"}).JSON()
	if d["status"] != "active" || d["disposalApprovalId"] == nil {
		t.Fatalf("disposal waiting for approval: %v", d)
	}
	sa.Must(409, "POST", "/api/v1/inventory/assets/"+rental+":dispose", map[string]any{"reason": "again"})
	fm.Must(200, "POST", "/api/v1/platform/approvals/"+str(d["disposalApprovalId"])+":approve", map[string]any{"reason": "Write off"})
	if d = sa.Must(200, "GET", "/api/v1/inventory/assets/"+rental, nil).JSON(); d["status"] != "disposed" {
		t.Fatalf("disposed: %v", d)
	}
	if ev := invEvents(t, "inventory.asset_disposed", rental); len(ev) != 1 || !strings.Contains(ev[0], `"proceeds": "250000"`) {
		t.Fatalf("asset_disposed: %v", ev)
	}
	sa.Must(409, "PATCH", "/api/v1/inventory/assets/"+rental, map[string]any{"name": "x"})

	// FR-MIG-P4-05 migrated assets keep their accumulated depreciation.
	csv := "code,name,categoryId,acquisitionDate,acquisitionCost,openingAccumulated\nIMP" + k.sfx + ",Imported treadmill," + cat + ",2024-01-01,12000000,4000000\n"
	imp := sa.OK("POST", "/api/v1/platform/imports", map[string]any{"entity": "inventory.asset", "mode": "commit", "filename": "assets.csv", "csv": csv}).JSON()
	if imp["insertedRows"].(float64) != 1 {
		t.Fatalf("asset import: %v", imp)
	}
	var acc, book string
	sysQueryRow(t, inst, `SELECT accumulated_depreciation::text, book_value::text FROM inventory.assets WHERE code = $1`, []any{"IMP" + k.sfx}, &acc, &book)
	invEq(t, "migrated accumulated", acc, "4000000")
	invEq(t, "migrated book value", book, "8000000")
	for _, code := range []string{"inventory.asset_register", "inventory.maintenance_schedule", "inventory.depreciation"} {
		rep := sa.Must(200, "GET", "/api/v1/reporting/reports/"+code, nil).JSON()
		if !strings.Contains(fmt.Sprint(rep["rows"]), "GC"+k.sfx) {
			t.Fatalf("%s misses the golf cart: %v", code, rep["rows"])
		}
	}
}

// ── EP-29 opening stock, EP-27 policies, permissions, reports ─────────────

// FR-MIG-P4-03/06: opening stock per warehouse with value (preview, commit,
// no double posting) and the item / warehouse master through the Master Data
// Import; FR-POL-P4-01 negative stock per warehouse from Inventory Policies;
// role permissions; every inventory report and the dashboard run.
func TestP4InventoryImport(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	wa := k.warehouse(t, sa, "IA", nil)
	wb := k.warehouse(t, sa, "IB", nil)
	flour := k.item(t, sa, "FLOUR", k.kg, nil)
	egg := k.item(t, sa, "EGG", k.pcs, map[string]any{"trackBatch": true})
	exp := time.Now().In(clubLoc(inst)).AddDate(0, 0, 20).Format("2006-01-02")
	head := "warehouseCode,itemCode,quantity,unitCost,uom,batchNo,expiryDate\n"
	bad := head + "IA" + k.sfx + ",FLOUR" + k.sfx + ",25,9000,,,\nIA" + k.sfx + ",NOPE,1,1,,,\nIB" + k.sfx + ",EGG" + k.sfx + ",30,2000,,,\n"
	res := sa.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "preview", "csv": bad}).JSON()
	if res["status"] != "failed" || len(res["errors"].([]any)) != 2 {
		t.Fatalf("preview with errors: %v", res)
	}
	good := head + "IA" + k.sfx + ",FLOUR" + k.sfx + ",25,9000,,,\nIB" + k.sfx + ",FLOUR" + k.sfx + ",10000,9,G" + k.sfx + ",,\nIB" + k.sfx + ",EGG" + k.sfx + ",30,2000,,B-01," + exp + "\n"
	res = sa.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "preview", "csv": good, "filename": "cutover.csv"}).JSON()
	if res["status"] != "valid" || len(res["warehouses"].([]any)) != 2 {
		t.Fatalf("valid preview: %v", res)
	}
	invEq(t, "opening value = cut-over opname", res["totalValue"], "375000")
	invEq(t, "nothing posted by a preview", invOnHand(t, sa, wa, flour), "0")
	res = sa.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "commit", "csv": good, "filename": "cutover.csv",
		"businessDate": invToday()}).JSON()
	if res["status"] != "completed" || res["warehouses"].([]any)[0].(map[string]any)["adjustment"] == nil {
		t.Fatalf("commit: %v", res)
	}
	invEq(t, "flour at IA", invOnHand(t, sa, wa, flour), "25")
	b := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?itemId="+egg+"&byBatch=true", nil).Items()
	if len(b) != 1 || b[0]["batchNo"] != "B-01" || b[0]["expiryDate"] != exp {
		t.Fatalf("opening batch: %v", b)
	}
	val := sa.Must(200, "GET", "/api/v1/inventory/valuation?warehouseId="+wa, nil).JSON()
	invEq(t, "IA value", val["total"], "225000")
	if res = sa.Must(200, "POST", "/api/v1/inventory/opening-stock:import", map[string]any{"mode": "commit", "csv": good}).JSON(); res["status"] != "failed" ||
		!strings.Contains(fmt.Sprint(res["errors"]), "already_imported") {
		t.Fatalf("second commit must not post twice: %v", res)
	}
	// Master Data Import of items and warehouses (FR-INV-08).
	items := "code,name,baseUomId,itemType,categoryId,barcode\nIMPI" + k.sfx + ",Imported sugar," + k.kg + ",raw," + k.cat + ",IMPB" + k.sfx + "\n"
	if r := sa.OK("POST", "/api/v1/platform/imports", map[string]any{"entity": "inventory.item", "mode": "commit", "csv": items}).JSON(); r["insertedRows"].(float64) != 1 {
		t.Fatalf("item import: %v", r)
	}
	whs := "code,name,locationType\nIMW" + k.sfx + ",Imported store,store\n"
	if r := sa.OK("POST", "/api/v1/platform/imports", map[string]any{"entity": "inventory.warehouse", "mode": "commit", "csv": whs}).JSON(); r["insertedRows"].(float64) != 1 {
		t.Fatalf("warehouse import: %v", r)
	}

	// FR-POL-P4-01: negative stock allowed (flagged) only for listed warehouses.
	sa.Must(422, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wa, "costCenter": "bakery", "reason": "Rush",
		"lines": []map[string]any{{"itemId": flour, "quantity": "30"}}})
	pol := map[string]any{"opnameTolerancePercent": "2", "adjustmentApprovalAbove": "0", "requisitionApprovalAbove": "0", "allowNegativeStock": false,
		"negativeStockWarehouses": []string{"IA" + k.sfx}, "expiryAlertDays": 7, "slowMovingDays": 90, "autoPurchaseRequisition": true, "autoStoreRequisition": true,
		"freezeDuringOpname": false, "blindCount": true}
	sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Inventory Policies", "code": "inventory.policy", "name": "Inventory Policies",
		"value": pol})
	t.Cleanup(func() {
		pol["negativeStockWarehouses"] = []string{}
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": "Inventory Policies", "code": "inventory.policy", "name": "Inventory Policies",
			"value": pol})
	})
	sa.Must(201, "POST", "/api/v1/inventory/issues", map[string]any{"warehouseId": wa, "costCenter": "bakery", "reason": "Rush",
		"lines": []map[string]any{{"itemId": flour, "quantity": "30"}}})
	invEq(t, "negative stock allowed for IA", invOnHand(t, sa, wa, flour), "-5")

	// Role permissions (Product Overview §44).
	ws := roleUser(t, inst, "warehouse_staff")
	ws.Must(403, "POST", "/api/v1/inventory/depreciation-runs", map[string]any{"period": "2026-01"})
	ws.Must(403, "GET", "/api/v1/inventory/valuation", nil)
	ws.Must(200, "GET", "/api/v1/inventory/stock-balances", nil)
	fm := roleUser(t, inst, "finance_manager")
	fm.Must(200, "GET", "/api/v1/inventory/valuation", nil)
	fm.Must(403, "POST", "/api/v1/inventory/issues", map[string]any{})
	ps := roleUser(t, inst, "pos_staff")
	ps.Must(201, "POST", "/api/v1/inventory/waste", map[string]any{"warehouseId": wb, "reason": "breakage", "lines": []map[string]any{{"itemId": egg, "quantity": "1",
		"batchNo": "B-01"}}})
	ps.Must(403, "POST", "/api/v1/inventory/adjustments", map[string]any{})
	roleUser(t, inst, "golf_staff").Must(403, "GET", "/api/v1/inventory/valuation", nil)

	// Every inventory report and the Inventory Performance dashboard run.
	from, to := time.Now().In(clubLoc(inst)).AddDate(0, 0, -30).Format("2006-01-02"), invToday()
	n := 0
	for _, r := range sa.Must(200, "GET", "/api/v1/reporting/reports", nil).Items() {
		if r["module"] != "inventory" {
			continue
		}
		n++
		sa.Must(200, "GET", "/api/v1/reporting/reports/"+str(r["code"])+"?params[from]="+from+"&params[to]="+to, nil)
	}
	if n < 13 {
		t.Fatalf("inventory reports: %d", n)
	}
	d := sa.Must(200, "GET", "/api/v1/reporting/dashboards/inventory-performance?from="+from+"&to="+to, nil).JSON()
	if d["name"] != "Inventory Performance" || len(d["kpis"].([]any)) != 7 {
		t.Fatalf("dashboard: %v", d)
	}
	var avail int
	sysQueryRow(t, inst, `SELECT count(*) FROM reporting.stock_availability WHERE item_id = $1 AND on_hand > 0`, []any{egg}, &avail)
	if avail != 1 {
		t.Fatalf("K9 stock availability: %d", avail)
	}
}
