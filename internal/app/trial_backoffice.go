package app

// Trial dataset: purchasing and stock control. Every week the inventory
// manager raises purchase requisitions for the stores below their
// three-week level; they are approved (procurement approval matrix),
// consolidated into purchase orders per supplier, approved and sent; the
// goods arrive the next day (goods receipt into the store), the supplier's
// invoice follows (3-way matching, AP) and Finance pays the AP falling due
// in a weekly payment run from the BCA account. At every month end one
// store is counted (stock opname with small variances, approval above the
// tolerance).

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

func init() {
	registerTrialSeeder(trialSeeder{Name: "purchasing", Order: 80, Day: trialPurchasingDay})
}

const (
	trialInventoryManager = "inventory@demo.oneclub.id"
	trialWarehouse        = "warehouse@demo.oneclub.id"
	trialProcurement      = "procurement@demo.oneclub.id"
)

// supplierOf is the supplier of an item category.
func trialSupplierOf(category string) string {
	if category == "PROSHOP" {
		return "SUP-PRIMA"
	}
	return "SUP-SINAR"
}

// trialStoreTargets is the level each store is purchased up to: the
// outlets it supplies at par plus three weeks of their use.
func trialStoreTargets() map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for wh, m := range trialUsage() {
		for item, daily := range m {
			st := trialStoreOf(item)
			if out[st] == nil {
				out[st] = map[string]float64{}
			}
			out[st][item] += trialPar(daily, 21)
			if st == wh {
				out[st][item] += trialPar(daily, 10)
			}
		}
	}
	return out
}

func trialPurchasingDay(ctx context.Context, t *Trial, day time.Time) error {
	switch day.Weekday() {
	case time.Tuesday:
		t.At(day, "09:00")
		trialRequisitions(ctx, t, day)
	case time.Wednesday:
		t.At(day, "10:00")
		trialReceive(t, day)
	case time.Thursday:
		t.At(day, "14:00")
		trialVendorInvoices(ctx, t, day)
	case time.Friday:
		t.At(day, "15:00")
		trialPaymentRun(ctx, t, day)
	}
	if day.AddDate(0, 0, 1).Day() == 1 { // month end: stock opname of one store
		t.At(day, "20:00")
		trialOpname(ctx, t, day)
	}
	return nil
}

func trialRequisitions(ctx context.Context, t *Trial, day time.Time) {
	inv := t.As(trialInventoryManager)
	wh := t.ids("warehouse", "/api/v1/inventory/warehouses?limit=100", trialInventoryManager)
	item := t.ids("item", "/api/v1/inventory/items?limit=200", trialInventoryManager)
	supplier := t.ids("supplier", "/api/v1/procurement/suppliers?limit=100", trialProcurement)
	cat := map[string]string{}
	for _, it := range trialItems {
		cat[it.code] = it.category
	}
	var lineIDs []string
	targets := trialStoreTargets()
	for _, st := range slices.Sorted(maps.Keys(targets)) {
		m := targets[st]
		onHand := map[string]float64{}
		for _, b := range inv.Items("/api/v1/inventory/stock-balances?warehouseId=" + wh(st) + "&limit=500") {
			q, _ := decimal.NewFromString(b.S("quantity"))
			onHand[b.S("itemId")] += q.InexactFloat64()
		}
		var lines []J
		for _, code := range slices.Sorted(maps.Keys(m)) {
			target := m[code]
			need := target - onHand[item(code)]
			if need < target/3 {
				continue
			}
			lines = append(lines, J{"itemId": item(code), "quantity": fmt.Sprint(math.Ceil(need)), "suggestedSupplierId": supplier(trialSupplierOf(cat[code])),
				"estimatedUnitPrice": fmt.Sprint(trialCost(code)), "neededBy": day.AddDate(0, 0, 1).Format(time.DateOnly)})
		}
		if len(lines) == 0 {
			continue
		}
		pr := inv.Post("/api/v1/procurement/requisitions", J{"title": "Weekly replenishment " + st + " " + day.Format("2 Jan"), "warehouseId": wh(st),
			"category": "Inventory", "neededBy": day.AddDate(0, 0, 1).Format(time.DateOnly), "lines": lines, "submit": true})
		t.ApprovePending(ctx)
		pr = inv.Get("/api/v1/procurement/requisitions/" + pr.S("id"))
		if pr.S("status") != "approved" {
			t.fail("requisition %s: %s", pr.S("number"), pr.S("status"))
		}
		for _, l := range pr.A("lines") {
			lineIDs = append(lineIDs, l.S("id"))
		}
	}
	if len(lineIDs) == 0 {
		return
	}
	prc := t.As(trialProcurement)
	res := prc.Post("/api/v1/procurement/purchase-orders:from-requisitions", J{"requisitionLineIds": lineIDs, "expectedDate": day.AddDate(0, 0, 1).Format(time.DateOnly),
		"rfqSkipReason": "Weekly replenishment from the contracted supplier", "submit": true})
	t.ApprovePending(ctx)
	for _, po := range res.Items() {
		o := prc.Get("/api/v1/procurement/purchase-orders/" + po.S("id"))
		if o.S("status") == "approved" {
			prc.Post("/api/v1/procurement/purchase-orders/"+po.S("id")+":send", J{})
		}
	}
}

// trialReceive receives the open purchase orders in full at their store.
func trialReceive(t *Trial, day time.Time) {
	ws := t.As(trialWarehouse)
	for _, po := range ws.Items("/api/v1/procurement/purchase-orders?outstanding=true&limit=100") {
		o := ws.Get("/api/v1/procurement/purchase-orders/" + po.S("id"))
		var lines []J
		for _, l := range o.A("lines") {
			open, _ := decimal.NewFromString(l.S("quantity"))
			rec, _ := decimal.NewFromString(l.S("receivedQuantity"))
			if q := open.Sub(rec); q.IsPositive() {
				lines = append(lines, J{"purchaseOrderLineId": l.S("id"), "acceptedQuantity": q.String()})
			}
		}
		if len(lines) == 0 {
			continue
		}
		ws.Post("/api/v1/procurement/goods-receipts", J{"purchaseOrderId": po.S("id"), "deliveryNoteNo": fmt.Sprintf("SJ/%s/%s", day.Format("0102"), o.S("number")),
			"lines": lines})
	}
}

// trialVendorInvoices books the suppliers' invoices of the received orders.
func trialVendorInvoices(ctx context.Context, t *Trial, day time.Time) {
	acc := t.As(trialAccountant)
	prc := t.As(trialProcurement)
	billed := map[string]bool{}
	for _, vi := range prc.Items("/api/v1/procurement/vendor-invoices?limit=500") {
		if vi.S("status") != "cancelled" {
			for _, id := range jstrs(vi["purchaseOrderIds"]) {
				billed[id] = true
			}
		}
	}
	for _, po := range prc.Items("/api/v1/procurement/purchase-orders?filter[status]=received&limit=100") {
		if billed[po.S("id")] {
			continue
		}
		r := t.Rand("vi:" + po.S("number"))
		acc.Post("/api/v1/procurement/vendor-invoices", J{"purchaseOrderId": po.S("id"), "supplierInvoiceNo": "INV/" + po.S("number"),
			"taxInvoiceNo": fmt.Sprintf("010.%03d-26.%08d", r.IntN(1000), r.IntN(100000000)), "invoiceDate": day.Format(time.DateOnly), "match": true})
	}
	t.ApprovePending(ctx)
}

// trialPaymentRun pays the supplier invoices due within a week.
func trialPaymentRun(ctx context.Context, t *Trial, day time.Time) {
	fin := t.As(trialFinance)
	var bank string
	for _, b := range fin.Items("/api/v1/accounting/bank-accounts?limit=20") {
		if b.S("code") == "BCA-OPS" {
			bank = b.S("id")
		}
	}
	var items []J
	due := day.AddDate(0, 0, 7).Format(time.DateOnly)
	for _, p := range fin.Items("/api/v1/accounting/payables?limit=200") {
		if (p.S("status") == "open" || p.S("status") == "partially_paid") && p.S("dueDate") <= due {
			items = append(items, J{"apItemId": p.S("id")})
		}
	}
	if len(items) == 0 {
		return
	}
	run := fin.Post("/api/v1/accounting/payment-runs", J{"paymentDate": day.Format(time.DateOnly), "bankAccountId": bank, "method": "transfer",
		"notes": "Weekly supplier payments", "items": items})
	fin.Post("/api/v1/accounting/payment-runs/"+run.S("id")+":submit", J{})
	t.ApprovePending(ctx)
	if r := fin.Get("/api/v1/accounting/payment-runs/" + run.S("id")); r.S("status") == "approved" {
		fin.Post("/api/v1/accounting/payment-runs/"+run.S("id")+":execute", J{})
	}
}

// trialOpname counts one store at month end (rotating).
func trialOpname(ctx context.Context, t *Trial, day time.Time) {
	stores := []string{"MAIN-STORE", "BEV-STORE", "PRO-SHOP", "COLD-STORE"}
	st := stores[int(day.Month())%len(stores)]
	ws := t.As(trialWarehouse)
	wh := t.ids("warehouse", "/api/v1/inventory/warehouses?limit=100")
	r := t.Rand("opname:" + day.Format(time.DateOnly))
	o := ws.Post("/api/v1/inventory/stock-opnames", J{"warehouseId": wh(st), "blind": false, "freeze": false, "notes": "Month-end stock opname"})
	o = ws.Get("/api/v1/inventory/stock-opnames/" + o.S("id"))
	var lines []J
	for _, l := range o.A("lines") {
		sys, _ := decimal.NewFromString(l.S("systemQuantity"))
		counted := sys
		if r.IntN(5) == 0 && sys.GreaterThan(decimal.NewFromInt(5)) { // breakage, spillage, counting slips
			counted = sys.Sub(decimal.NewFromInt(int64(1 + r.IntN(2))))
		}
		lines = append(lines, J{"itemId": l.S("itemId"), "quantity": counted.String()})
	}
	if len(lines) == 0 {
		ws.Post("/api/v1/inventory/stock-opnames/"+o.S("id")+":cancel", J{"reason": "Nothing in stock"})
		return
	}
	ws.Post("/api/v1/inventory/stock-opnames/"+o.S("id")+":count", J{"lines": lines})
	ws.Post("/api/v1/inventory/stock-opnames/"+o.S("id")+":submit", J{})
	t.As(trialInventoryManager).Post("/api/v1/inventory/stock-opnames/"+o.S("id")+":post", nil)
	t.ApprovePending(ctx)
}
