package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"oneclub/internal/accounting"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/procurement"
)

func init() {
	// Contacts, addresses, bank accounts, documents and supplier items run
	// through the generic CRUD test; the P0 supplier stays with P0.
	resourceCRUDModules["procurement"] = true
}

// ── helpers ───────────────────────────────────────────────────────────────

// prcSupplier creates an active PKP supplier with an e-mail.
func prcSupplier(t *testing.T, sa *Client, code string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"code": code, "name": "Supplier " + code, "email": strings.ToLower(code) + "@supplier.test", "pkp": true,
		"categories": []string{"F&B"}, "paymentTermDays": 30, "leadTimeDays": 2}
	for k, v := range extra {
		body[k] = v
	}
	return idOf(sa.Must(201, "POST", "/api/v1/procurement/suppliers", body))
}

// prcDispatch dispatches the outbox until cond holds.
func prcDispatch(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, 20*time.Second, what, func() bool {
		_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
		return cond()
	})
}

// prcEvents returns the payloads of the events of a type for an aggregate.
func prcEvents(t *testing.T, typ, agg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range invEvents(t, typ, agg) {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// prcApprove approves (or rejects) the pending step of an approval request.
func prcDecide(t *testing.T, c *Client, requestID any, approve bool, reason string) map[string]any {
	t.Helper()
	act := ":approve"
	if !approve {
		act = ":reject"
	}
	return c.Must(200, "POST", "/api/v1/platform/approvals/"+str(requestID)+act, map[string]any{"reason": reason}).JSON()
}

// prcPolicy writes the Procurement Policies (defaults + overrides) and
// restores the defaults at the end of the test.
func prcPolicy(t *testing.T, sa *Client, overrides map[string]any) {
	t.Helper()
	write := func(o map[string]any) {
		raw, _ := json.Marshal(procurement.DefaultPolicy)
		var v map[string]any
		_ = json.Unmarshal(raw, &v)
		for k, x := range o {
			v[k] = x
		}
		sa.Must(201, "POST", "/api/v1/platform/club-policies", map[string]any{"category": procurement.PolicyCategory, "code": procurement.PolicyCode,
			"name": "Procurement Policies", "value": v})
	}
	write(overrides)
	t.Cleanup(func() { write(nil) })
}

// prcTokens returns the tokens of the supplier links e-mailed to an address.
func prcTokens(t *testing.T, event, email, pattern string) []string {
	t.Helper()
	var out []string
	ctx := dbtx.System(context.Background())
	re := regexp.MustCompile(pattern)
	if err := inst.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT body FROM platform.notification_deliveries WHERE event_code = $1 AND recipient = $2 AND channel = 'email'
			ORDER BY created_at`, event, email)
		if err != nil {
			return err
		}
		bodies, err := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, b := range bodies {
			if m := re.FindStringSubmatch(b); m != nil {
				out = append(out, m[1])
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func prcLine(doc map[string]any, i int) map[string]any {
	return doc["lines"].([]any)[i].(map[string]any)
}

func prcStatus(t *testing.T, doc map[string]any, want string) {
	t.Helper()
	if doc["status"] != want {
		t.Fatalf("status %v, want %s: %v", doc["status"], want, doc)
	}
}

// prcPublish publishes an event of the MAIN property as its owner would.
func prcPublish(t *testing.T, typ string, agg uuid.UUID, payload any) uuid.UUID {
	return invPublish(t, typ, agg, payload)
}

// ── EP-08 / EP-11..15 procure-to-pay (PRD P4 §9.1) ────────────────────────

// PRD P4 §9.1: stock below the reorder point → automatic PR (once a day) →
// approval Procurement Manager → RFQ to 3 suppliers (e-mail, supplier link)
// → vendor quotations compared, the cheapest selected (a dearer one needs
// a reason and approval) → PO approved and sent (PDF link) → GR 80 % → GR
// of the rest (stock in through inventory) → vendor invoice → 3-way
// matching Matched → approved for AP (procurement.vendor_invoice_approved)
// → paid by accounting (accounting.vendor_payment_made) → PO closed.
func TestP4ProcurementProcureToPay(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	k := newInvKit(t, sa)
	ms := k.warehouse(t, sa, "PS", nil)
	soap := k.item(t, sa, "PSOAP", k.pcs, nil)
	var sups []string
	for _, c := range []string{"PA", "PB", "PC"} {
		sups = append(sups, prcSupplier(t, sa, c+k.sfx, nil))
	}

	// EP-08 AC: par 60, reorder point 20, stock 10, nothing on order → one
	// automatic PR of 50, not duplicated when the job runs again.
	sa.Must(201, "POST", "/api/v1/inventory/par-stocks", map[string]any{"itemId": soap, "warehouseId": ms, "parLevel": "60"})
	sa.Must(201, "POST", "/api/v1/inventory/reorder-points", map[string]any{"itemId": soap, "warehouseId": ms, "reorderPoint": "20", "preferredSupplierId": sups[1]})
	sa.Must(201, "POST", "/api/v1/inventory/adjustments", map[string]any{"warehouseId": ms, "reason": "found", "submit": true,
		"lines": []map[string]any{{"itemId": soap, "quantity": "10", "unitCost": "4800"}}})
	sa.Must(200, "POST", "/api/v1/inventory/replenishment:run", nil)
	var prs []map[string]any
	prcDispatch(t, "automatic purchase requisition", func() bool {
		prs = sa.Must(200, "GET", "/api/v1/procurement/requisitions?filter[source]=reorder&filter[warehouseId]="+ms, nil).Items()
		return len(prs) == 1
	})
	sa.Must(200, "POST", "/api/v1/inventory/replenishment:run", nil)
	_, _ = inst.App.Dispatcher.DispatchPending(t.Context())
	pr := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(prs[0]["id"]), nil).JSON()
	if len(pr["lines"].([]any)) != 1 || pr["status"] != "draft" {
		t.Fatalf("automatic PR (once a day): %v", pr)
	}
	l0 := prcLine(pr, 0)
	invEq(t, "PR quantity = par − stock − on order", l0["quantity"], "50")
	if l0["suggestedSupplierId"] != sups[1] || l0["itemId"] != soap {
		t.Fatalf("PR line: %v", l0)
	}

	// FR-PR-03: submitted to the approval matrix; the Procurement Manager approves.
	pr = sa.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(pr["id"])+":submit", nil).JSON()
	prcStatus(t, pr, "submitted")
	pr = pm.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(pr["id"])+":approve", map[string]any{"reason": "Stock low"}).JSON()
	prcStatus(t, pr, "approved")
	if ev := prcEvents(t, "procurement.requisition_approved", str(pr["id"])); len(ev) != 1 {
		t.Fatalf("requisition approved event: %v", ev)
	}
	if open := sa.Must(200, "GET", "/api/v1/procurement/requisition-lines?filter[itemId]="+soap, nil).Items(); len(open) != 1 {
		t.Fatalf("open requisition lines: %v", open)
	}

	// FR-RFQ-01: RFQ to three suppliers, e-mailed with a response link.
	rfq := sa.Must(201, "POST", "/api/v1/procurement/rfqs", map[string]any{"title": "Bungalow soap " + k.sfx, "requisitionLineIds": []any{l0["id"]},
		"supplierIds": sups, "send": true}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, rfq, "sent")
	if rfq["warehouseId"] != ms || len(rfq["suppliers"].([]any)) != 3 || len(rfq["lines"].([]any)) != 1 {
		t.Fatalf("RFQ: %v", rfq)
	}
	rl := prcLine(rfq, 0)
	sa.Must(200, "GET", "/api/v1/procurement/rfqs/"+str(rfq["id"])+"/pdf", nil)
	tokA := prcTokens(t, "procurement.rfq", "pa"+k.sfx+"@supplier.test", `/supplier/rfq/([A-Za-z0-9_-]+)`)
	if len(tokA) != 1 {
		t.Fatalf("RFQ e-mail to supplier A: %v", tokA)
	}
	// Supplier A answers through the link (FR-RFQ-02); B and C by the buyer.
	pub := anon(t, inst)
	view := pub.Must(200, "GET", "/api/v1/public/procurement/rfqs/"+tokA[0], nil).JSON()
	if view["supplierName"] != "Supplier PA"+k.sfx || len(view["lines"].([]any)) != 1 || view["responded"] == true {
		t.Fatalf("supplier view: %v", view)
	}
	pub.Must(404, "GET", "/api/v1/public/procurement/rfqs/not-a-token", nil)
	qa := pub.Must(201, "POST", "/api/v1/public/procurement/rfqs/"+tokA[0]+":quote", map[string]any{"supplierReference": "QA-1", "leadTimeDays": 1,
		"lines": []map[string]any{{"rfqLineId": rl["id"], "unitPrice": "5200"}}}).JSON()
	if qa["status"] != "received" {
		t.Fatalf("supplier quote: %v", qa)
	}
	valid := time.Now().In(clubLoc(inst)).AddDate(0, 0, 14).Format("2006-01-02")
	qb := sa.Must(201, "POST", "/api/v1/procurement/vendor-quotations", map[string]any{"rfqId": rfq["id"], "supplierId": sups[1], "validUntil": valid,
		"leadTimeDays": 2, "paymentTermDays": 30, "lines": []map[string]any{{"rfqLineId": rl["id"], "unitPrice": "5000"}}}, "Idempotency-Key", newKey()).JSON()
	qc := sa.Must(201, "POST", "/api/v1/procurement/vendor-quotations", map[string]any{"rfqId": rfq["id"], "supplierId": sups[2], "validUntil": valid,
		"lines": []map[string]any{{"rfqLineId": rl["id"], "unitPrice": "5300", "discountPercent": "2"}}}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "quotation B total incl. PPN 11%", qb["total"], "277500")
	qc = sa.Must(200, "PATCH", "/api/v1/procurement/vendor-quotations/"+str(qc["id"]), map[string]any{"deliveryTerms": "Franco gudang"}).JSON()
	if qc["deliveryTerms"] != "Franco gudang" {
		t.Fatalf("quotation correction: %v", qc)
	}
	cmp := sa.Must(200, "GET", "/api/v1/procurement/rfqs/"+str(rfq["id"])+"/comparison", nil).JSON()
	offers := cmp["lines"].([]any)[0].(map[string]any)["offers"].([]any)
	if len(offers) != 3 || offers[0].(map[string]any)["supplierId"] != sups[1] || offers[0].(map[string]any)["cheapest"] != true {
		t.Fatalf("comparison: %v", cmp)
	}
	invEq(t, "best total", cmp["bestTotal"], "250000")
	// FR-RFQ-03: a dearer quotation needs a reason and approval.
	var qaID string
	for _, q := range sa.Must(200, "GET", "/api/v1/procurement/vendor-quotations?filter[rfqId]="+str(rfq["id"]), nil).Items() {
		if q["supplierId"] == sups[0] {
			qaID = str(q["id"])
		}
	}
	sa.Must(422, "POST", "/api/v1/procurement/vendor-quotations/"+qaID+":select", map[string]any{})
	sel := sa.Must(200, "POST", "/api/v1/procurement/vendor-quotations/"+qaID+":select", map[string]any{"reason": "Faster delivery"}).JSON()
	prcStatus(t, sel, "pending_approval")
	prcDecide(t, pm, sel["approvalRequestId"], false, "Take the cheapest")
	if q := sa.Must(200, "GET", "/api/v1/procurement/vendor-quotations/"+qaID, nil).JSON(); q["status"] != "received" {
		t.Fatalf("rejected selection: %v", q)
	}
	sel = sa.Must(200, "POST", "/api/v1/procurement/vendor-quotations/"+str(qb["id"])+":select", map[string]any{"createPurchaseOrder": true}).JSON()
	prcStatus(t, sel, "selected")
	if r := sa.Must(200, "GET", "/api/v1/procurement/rfqs/"+str(rfq["id"]), nil).JSON(); r["status"] != "awarded" {
		t.Fatalf("RFQ awarded: %v", r)
	}
	if q := sa.Must(200, "GET", "/api/v1/procurement/vendor-quotations/"+qaID, nil).JSON(); q["status"] != "not_selected" {
		t.Fatalf("other quotation: %v", q)
	}

	// FR-PO-01/02: the PO of the selected quotation, approved and sent.
	pos := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders?filter[supplierId]="+sups[1], nil).Items()
	if len(pos) != 1 || pos[0]["quotationId"] != qb["id"] || pos[0]["warehouseId"] != ms {
		t.Fatalf("PO from quotation: %v", pos)
	}
	po := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+str(pos[0]["id"]), nil).JSON()
	invEq(t, "PO total", po["total"], "277500")
	if p := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(pr["id"]), nil).JSON(); p["status"] != "ordered" {
		t.Fatalf("requisition after ordering: %v", p)
	}
	poID := str(po["id"])
	po = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":submit", nil).JSON()
	prcStatus(t, po, "pending_approval")
	po = pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":approve", map[string]any{}).JSON()
	prcStatus(t, po, "approved")
	po = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":send", map[string]any{}).JSON()
	prcStatus(t, po, "sent")
	if po["sentTo"] != "pb"+k.sfx+"@supplier.test" {
		t.Fatalf("PO sent to: %v", po["sentTo"])
	}
	tok := prcTokens(t, "procurement.purchase_order", "pb"+k.sfx+"@supplier.test", `/purchase-orders/([A-Za-z0-9_-]+)/pdf`)
	if len(tok) != 1 {
		t.Fatalf("PO e-mail: %v", tok)
	}
	if r := pub.Do("GET", "/api/v1/public/procurement/purchase-orders/"+tok[0]+"/pdf", nil); r.Status != 200 || !strings.HasPrefix(string(r.Body), "%PDF") {
		t.Fatalf("supplier PDF: %d", r.Status)
	}
	if r := sa.Do("GET", "/api/v1/procurement/purchase-orders/"+poID+"/pdf", nil); r.Status != 200 || r.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("PO PDF: %d", r.Status)
	}
	if ev := prcEvents(t, "procurement.po_approved", poID); len(ev) != 1 {
		t.Fatalf("po_approved: %v", ev)
	}
	pl := prcLine(po, 0)

	// FR-GR-01: GR of 80 % with batch and delivery note, then the rest.
	gr1 := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID, "deliveryNoteNo": "SJ-1",
		"lines": []map[string]any{{"purchaseOrderLineId": pl["id"], "acceptedQuantity": "40", "batchNo": "SB-" + k.sfx}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, gr1, "posted")
	if o := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+poID, nil).JSON(); o["status"] != "partially_received" {
		t.Fatalf("PO after 80%%: %v", o["status"])
	}
	ev := prcEvents(t, "procurement.goods_received", str(gr1["id"]))
	if len(ev) != 1 || ev[0]["purchaseOrderId"] != poID || ev[0]["warehouseId"] != ms {
		t.Fatalf("goods_received: %v", ev)
	}
	el := ev[0]["lines"].([]any)[0].(map[string]any)
	if el["itemId"] != soap || el["batchNo"] != "SB-"+k.sfx {
		t.Fatalf("goods_received line: %v", el)
	}
	invEq(t, "baseQuantity", el["baseQuantity"], "40")
	invEq(t, "baseUnitCost", el["baseUnitCost"], "5000")
	prcDispatch(t, "stock received by inventory", func() bool { return invOnHand(t, sa, ms, soap).Equal(decimal.NewFromInt(50)) })
	gr2 := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID,
		"lines": []map[string]any{{"purchaseOrderLineId": pl["id"], "acceptedQuantity": "10"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, gr2, "posted")
	if o := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+poID, nil).JSON(); o["status"] != "received" {
		t.Fatalf("PO after the rest: %v", o["status"])
	}
	prcDispatch(t, "stock after the second receipt", func() bool { return invOnHand(t, sa, ms, soap).Equal(decimal.NewFromInt(60)) })
	if g := sa.Must(200, "GET", "/api/v1/procurement/goods-receipts?filter[purchaseOrderId]="+poID, nil).Items(); len(g) != 2 {
		t.Fatalf("GR list: %v", g)
	}

	// FR-VIN-01/02/05: vendor invoice, 3-way matching Matched → AP.
	vi := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"purchaseOrderId": poID, "supplierInvoiceNo": "INV-" + k.sfx,
		"taxInvoiceNo": "010.000-26." + k.sfx, "match": true}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, vi, "approved")
	if vi["matchType"] != "three_way" || prcLine(vi, 0)["matchStatus"] != "matched" {
		t.Fatalf("3-way matching: %v", vi)
	}
	invEq(t, "AP total", vi["total"], "277500")
	invEq(t, "outstanding", vi["outstanding"], "277500")
	ap := prcEvents(t, "procurement.vendor_invoice_approved", str(vi["id"]))
	if len(ap) != 1 || ap[0]["supplierInvoiceNo"] != "INV-"+k.sfx || len(ap[0]["goodsReceiptIds"].([]any)) != 2 || ap[0]["withholding"] != "0" {
		t.Fatalf("vendor_invoice_approved: %v", ap)
	}
	if al := ap[0]["lines"].([]any)[0].(map[string]any); al["accountHint"] != "inventory" || al["itemId"] != soap {
		t.Fatalf("AP line: %v", al)
	}
	if ev := prcEvents(t, "procurement.vendor_invoice_matched", str(vi["id"])); len(ev) != 1 {
		t.Fatalf("matched event: %v", ev)
	}
	if runs := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices/"+str(vi["id"])+"/match-runs", nil).Items(); len(runs) != 1 || runs[0]["result"] != "matched" {
		t.Fatalf("match runs: %v", runs)
	}
	sa.Must(422, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"purchaseOrderId": poID, "supplierInvoiceNo": "inv-" + k.sfx})

	// Paid status from accounting.vendor_payment_made (idempotent).
	pay := func(amount string, pid uuid.UUID) {
		e := prcPublish(t, "accounting.vendor_payment_made", pid, map[string]any{"paymentId": pid, "number": "PAY-" + k.sfx, "supplierId": sups[1],
			"paidDate": invToday(), "currency": "IDR", "allocations": []map[string]any{{"vendorInvoiceId": vi["id"], "amount": amount}}})
		invHandled(t, e, "procurement.vendor_invoice_payment")
	}
	p1 := uuid.New()
	pay("100000", p1)
	if v := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices/"+str(vi["id"]), nil).JSON(); v["status"] != "partially_paid" {
		t.Fatalf("partially paid: %v", v)
	}
	pay("100000", p1) // the same payment again changes nothing
	pay("177500", uuid.New())
	v := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices/"+str(vi["id"]), nil).JSON()
	prcStatus(t, v, "paid")
	invEq(t, "paid", v["paidAmount"], "277500")
	if p := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices/"+str(vi["id"])+"/payments", nil).Items(); len(p) != 2 {
		t.Fatalf("payments: %v", p)
	}

	// FR-PO-04: the received order is closed.
	po = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":close", map[string]any{}).JSON()
	prcStatus(t, po, "closed")
	if o := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders?outstanding=true&filter[supplierId]="+sups[1], nil).Items(); len(o) != 0 {
		t.Fatalf("outstanding after close: %v", o)
	}
	// FR-SUP-03: the supplier's performance.
	perf := sa.Must(200, "GET", "/api/v1/procurement/vendor-performance?supplierId="+sups[1], nil).Items()
	if len(perf) != 1 || perf[0]["receipts"] != float64(2) || perf[0]["orders"] != float64(1) || perf[0]["onTimeRate"] != "1" || perf[0]["grade"] == "n/a" {
		t.Fatalf("vendor performance: %v", perf)
	}
	if fmt.Sprint(perf[0]["responseHours"]) == "<nil>" {
		t.Fatalf("RFQ response time missing: %v", perf[0])
	}
}

// prcOrder creates, submits and approves (Procurement Manager) a direct
// purchase order and returns it.
func prcOrder(t *testing.T, sa, pm *Client, body map[string]any) map[string]any {
	t.Helper()
	body["submit"] = true
	po := sa.Must(201, "POST", "/api/v1/procurement/purchase-orders", body, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, po, "pending_approval")
	return pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+str(po["id"])+":approve", map[string]any{}).JSON()
}

// ── EP-15 3-way matching, debit note, override, 2-way services ────────────

// EP-15 AC: PO 100 @ Rp10.000, GR 95, invoice 100 → Mismatch / On Hold;
// corrected to 95 → Matched and AP Rp950.000 + PPN. A price above the
// tolerance is held, partly covered by a debit note and approved as an
// override by Finance; the approved price differs from the receipt cost so
// inventory revalues the stock (procurement.invoice_price_variance). A
// service order is matched 2-way after the service confirmation, with PPh
// 23 withheld.
func TestP4ProcurementMatching(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	fm := roleUser(t, inst, "finance_manager")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "PM", nil)
	rice := k.item(t, sa, "PRICE", k.kg, nil)
	oil := k.item(t, sa, "POIL", k.btl, nil)
	sup := prcSupplier(t, sa, "PMS"+k.sfx, nil)

	po := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh,
		"lines": []map[string]any{{"itemId": rice, "quantity": "100", "unitPrice": "10000"}}})
	poID, line := str(po["id"]), str(prcLine(po, 0)["id"])
	invEq(t, "PO total", po["total"], "1110000")
	sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID,
		"lines": []map[string]any{{"purchaseOrderLineId": line, "acceptedQuantity": "95"}}}, "Idempotency-Key", newKey())
	vi := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"supplierId": sup, "supplierInvoiceNo": "M1-" + k.sfx, "match": true,
		"lines": []map[string]any{{"purchaseOrderLineId": line, "quantity": "100", "unitPrice": "10000"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, vi, "on_hold")
	if l := prcLine(vi, 0); l["matchStatus"] != "quantity_mismatch" || !dec(l["expectedQuantity"]).Equal(decimal.NewFromInt(95)) {
		t.Fatalf("quantity mismatch: %v", l)
	}
	sa.Must(409, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi["id"])+":approve", map[string]any{})
	vi = sa.Must(200, "PATCH", "/api/v1/procurement/vendor-invoices/"+str(vi["id"]), map[string]any{"match": true,
		"lines": []map[string]any{{"purchaseOrderLineId": line, "quantity": "95", "unitPrice": "10000"}}}).JSON()
	prcStatus(t, vi, "approved")
	invEq(t, "AP subtotal", vi["subtotal"], "950000")
	invEq(t, "AP PPN", vi["taxAmount"], "104500")
	invEq(t, "AP total", vi["total"], "1054500")
	if runs := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices/"+str(vi["id"])+"/match-runs", nil).Items(); len(runs) != 2 {
		t.Fatalf("match history: %v", runs)
	}
	if ev := prcEvents(t, "procurement.vendor_invoice_approved", str(vi["id"])); len(ev) != 1 || ev[0]["total"] != "1054500" {
		t.Fatalf("AP event: %v", ev)
	}
	// The remaining 5 kg are not delivered: cancel them, the order is received.
	po = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":cancel-lines", map[string]any{"reason": "Short shipment",
		"lines": []map[string]any{{"lineId": line}}}).JSON()
	prcStatus(t, po, "received")

	// Price above the tolerance: on hold, debit note, override by Finance.
	po2 := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh,
		"lines": []map[string]any{{"itemId": oil, "quantity": "10", "unitPrice": "20000"}}})
	l2 := str(prcLine(po2, 0)["id"])
	gr := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po2["id"],
		"lines": []map[string]any{{"purchaseOrderLineId": l2, "acceptedQuantity": "10"}}}, "Idempotency-Key", newKey()).JSON()
	grLine := str(prcLine(gr, 0)["id"])
	prcDispatch(t, "oil received", func() bool { return invOnHand(t, sa, wh, oil).Equal(decimal.NewFromInt(10)) })
	vi2 := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"supplierId": sup, "supplierInvoiceNo": "M2-" + k.sfx,
		"lines": []map[string]any{{"goodsReceiptLineId": grLine, "quantity": "10", "unitPrice": "21000"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, vi2, "draft")
	vi2 = sa.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi2["id"])+":match", map[string]any{}).JSON()
	prcStatus(t, vi2, "on_hold")
	if l := prcLine(vi2, 0); l["matchStatus"] != "price_mismatch" || l["purchaseOrderLineId"] != l2 {
		t.Fatalf("price mismatch: %v", l)
	}
	var held int
	sysQueryRow(t, inst, `SELECT count(*) FROM platform.notification_deliveries WHERE event_code = 'procurement.invoice_on_hold' AND body LIKE '%' || $1 || '%'`,
		[]any{str(vi2["number"])}, &held)
	if held == 0 {
		t.Fatal("hold notification missing")
	}
	dn := sa.Must(201, "POST", "/api/v1/procurement/debit-notes", map[string]any{"vendorInvoiceId": vi2["id"], "subtotal": "5000", "taxAmount": "550",
		"reason": "Price difference agreed"}, "Idempotency-Key", newKey()).JSON()
	if dn["status"] != "applied" {
		t.Fatalf("debit note: %v", dn)
	}
	if ev := prcEvents(t, "procurement.debit_note_issued", str(dn["id"])); len(ev) != 1 || ev[0]["amount"] != "5550" || ev[0]["vendorInvoiceId"] != vi2["id"] {
		t.Fatalf("debit note event: %v", ev)
	}
	sa.Must(200, "GET", "/api/v1/procurement/debit-notes/"+str(dn["id"]), nil)
	if l := sa.Must(200, "GET", "/api/v1/procurement/debit-notes?filter[vendorInvoiceId]="+str(vi2["id"]), nil).Items(); len(l) != 1 {
		t.Fatalf("debit notes: %v", l)
	}
	vi2 = sa.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi2["id"])+":release-hold", map[string]any{"reason": "Price increase accepted"}).JSON()
	prcStatus(t, vi2, "on_hold")
	if vi2["approvalKind"] != "override" || vi2["approvalRequestId"] == nil {
		t.Fatalf("override approval: %v", vi2)
	}
	vi2 = fm.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi2["id"])+":approve", map[string]any{"reason": "OK"}).JSON()
	prcStatus(t, vi2, "approved")
	invEq(t, "debit notes", vi2["debitNoteTotal"], "5550")
	invEq(t, "outstanding = total − debit notes", vi2["outstanding"], "227550")
	pv := prcEvents(t, "procurement.invoice_price_variance", str(vi2["id"]))
	if len(pv) != 1 || pv[0]["goodsReceiptId"] != gr["id"] || pv[0]["warehouseId"] != wh {
		t.Fatalf("invoice price variance: %v", pv)
	}
	invEq(t, "invoiced base cost", pv[0]["lines"].([]any)[0].(map[string]any)["invoicedBaseUnitCost"], "21000")
	prcDispatch(t, "stock revalued at the invoice price", func() bool {
		return len(invEvents(t, "inventory.revaluation_posted", str(vi2["id"]))) == 1
	})
	val := sa.Must(200, "GET", "/api/v1/inventory/valuation?warehouseId="+wh, nil).JSON()
	invEq(t, "revalued stock", val["total"], "1160000") // 95 kg × 10.000 + 10 × 21.000

	// Hold and cancel a disputed invoice.
	vi3 := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"supplierId": sup, "supplierInvoiceNo": "M3-" + k.sfx,
		"lines": []map[string]any{{"description": "Delivery charge", "quantity": "1", "unitPrice": "50000"}}}, "Idempotency-Key", newKey()).JSON()
	vi3 = sa.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi3["id"])+":hold", map[string]any{"reason": "Not agreed"}).JSON()
	prcStatus(t, vi3, "on_hold")
	vi3 = sa.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vi3["id"])+":cancel", map[string]any{"reason": "Wrong supplier"}).JSON()
	prcStatus(t, vi3, "cancelled")

	// FR-PO-05 / FR-VIN-04: service order, 2-way matching after the service
	// confirmation, PPh 23 withheld.
	svc := prcSupplier(t, sa, "PMT"+k.sfx, map[string]any{"withholdingType": "pph23", "pkp": false})
	so := prcOrder(t, sa, pm, map[string]any{"supplierId": svc, "orderType": "service",
		"lines": []map[string]any{{"description": "Buggy fleet maintenance", "quantity": "1", "unitPrice": "3000000"}}})
	sl := str(prcLine(so, 0)["id"])
	if prcLine(so, 0)["accountHint"] != "expense" {
		t.Fatalf("service line: %v", prcLine(so, 0))
	}
	sa.Must(409, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": so["id"],
		"lines": []map[string]any{{"purchaseOrderLineId": sl, "acceptedQuantity": "1"}}})
	vs := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"supplierId": svc, "supplierInvoiceNo": "S1-" + k.sfx, "match": true,
		"lines": []map[string]any{{"purchaseOrderLineId": sl, "quantity": "1", "unitPrice": "3000000"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, vs, "on_hold")
	if prcLine(vs, 0)["matchStatus"] != "not_received" {
		t.Fatalf("service not confirmed: %v", vs)
	}
	so = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+str(so["id"])+":confirm-service", map[string]any{"notes": "Work done",
		"lines": []map[string]any{{"lineId": sl, "quantity": "1"}}}).JSON()
	prcStatus(t, so, "received")
	vs = sa.Must(200, "POST", "/api/v1/procurement/vendor-invoices/"+str(vs["id"])+":release-hold", map[string]any{"reason": "Service confirmed"}).JSON()
	prcStatus(t, vs, "approved")
	if vs["matchType"] != "two_way" {
		t.Fatalf("2-way: %v", vs)
	}
	invEq(t, "PPh 23 2%", vs["withholdingAmount"], "60000")
	invEq(t, "payable after PPh", vs["outstanding"], "2940000")
	if ev := prcEvents(t, "procurement.vendor_invoice_approved", str(vs["id"])); len(ev) != 1 || ev[0]["withholding"] != "60000" ||
		ev[0]["lines"].([]any)[0].(map[string]any)["accountHint"] != "expense" {
		t.Fatalf("service AP event: %v", ev)
	}
	if l := sa.Must(200, "GET", "/api/v1/procurement/vendor-invoices?filter[supplierId]="+sup+"&filter[status]=approved", nil).Items(); len(l) != 2 {
		t.Fatalf("approved invoices: %v", l)
	}
}

// ── EP-13 / EP-14 order lifecycle, goods receipt & purchase return ────────

// FR-PO-02/03, FR-GR-01..04, FR-OPS-P4-04: a draft PO is edited, rejected
// and approved; goods are received with batch / expiry / serial numbers and
// a rejected quantity; an over-receipt waits for approval (or is refused by
// the policy); receipts without PO only for allowed categories, with
// approval; a purchase return takes stock out with a debit note that the
// vendor invoice of the order absorbs; a goods receipt queued offline on a
// warehouse device syncs once; revisions are versioned (re-approval above
// the approved total); an order without receipts is cancelled; documents in
// a closed accounting period are refused.
func TestP4ProcurementReceipts(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "PG", nil)
	milk := k.item(t, sa, "PMILK", k.btl, map[string]any{"trackBatch": true, "trackExpiry": true})
	racket := k.item(t, sa, "PRKT", k.pcs, map[string]any{"trackSerial": true})
	sup := prcSupplier(t, sa, "PGS"+k.sfx, nil)
	exp := time.Now().In(clubLoc(inst)).AddDate(0, 2, 0).Format("2006-01-02")

	// Draft → edit → submit → reject → submit → approve.
	po := sa.Must(201, "POST", "/api/v1/procurement/purchase-orders", map[string]any{"supplierId": sup, "warehouseId": wh,
		"lines": []map[string]any{{"itemId": milk, "quantity": "10", "unitPrice": "15000"}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, po, "draft")
	poID := str(po["id"])
	po = sa.Must(200, "PATCH", "/api/v1/procurement/purchase-orders/"+poID, map[string]any{"notes": "Keep chilled", "lines": []map[string]any{
		{"itemId": milk, "quantity": "20", "unitPrice": "15000"}, {"itemId": racket, "quantity": "2", "unitPrice": "450000", "accountHint": "asset"}}}).JSON()
	if len(po["lines"].([]any)) != 2 || po["notes"] != "Keep chilled" {
		t.Fatalf("edited PO: %v", po)
	}
	sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":submit", nil)
	po = pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":reject", map[string]any{"reason": "Check the racket price"}).JSON()
	prcStatus(t, po, "draft")
	if po["rejectedReason"] != "Check the racket price" {
		t.Fatalf("rejected PO: %v", po)
	}
	sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":submit", nil)
	po = pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+poID+":approve", map[string]any{}).JSON()
	prcStatus(t, po, "approved")
	lm, lr := str(prcLine(po, 0)["id"]), str(prcLine(po, 1)["id"])
	if rcv := sa.Must(200, "GET", "/api/v1/procurement/receivable-orders?warehouseId="+wh, nil).Items(); len(rcv) != 1 || len(rcv[0]["lines"].([]any)) != 2 {
		t.Fatalf("ops receivable orders: %v", rcv)
	}

	// FR-GR-01: batch / expiry, serials, rejected quantity with reason.
	sa.Must(422, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID,
		"lines": []map[string]any{{"purchaseOrderLineId": lr, "acceptedQuantity": "2", "serialNos": []string{"R1-" + k.sfx}}}})
	sa.Must(422, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID,
		"lines": []map[string]any{{"purchaseOrderLineId": lm, "acceptedQuantity": "12", "rejectedQuantity": "1"}}})
	gr := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": poID, "deliveryNoteNo": "SJ-" + k.sfx,
		"lines": []map[string]any{{"purchaseOrderLineId": lm, "acceptedQuantity": "12", "rejectedQuantity": "1", "rejectionReason": "Leaking",
			"batchNo": "MB-" + k.sfx, "expiryDate": exp},
			{"purchaseOrderLineId": lr, "acceptedQuantity": "2", "serialNos": []string{"R1-" + k.sfx, "R2-" + k.sfx}}}}, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, gr, "posted")
	if g := prcLine(gr, 0); !dec(g["deliveredQuantity"]).Equal(decimal.NewFromInt(13)) || g["expiryDate"] != exp {
		t.Fatalf("GR line: %v", g)
	}
	sa.Must(200, "GET", "/api/v1/procurement/goods-receipts/"+str(gr["id"]), nil)
	prcDispatch(t, "milk and rackets in stock", func() bool {
		return invOnHand(t, sa, wh, milk).Equal(decimal.NewFromInt(12)) && invOnHand(t, sa, wh, racket).Equal(decimal.NewFromInt(2))
	})
	if b := sa.Must(200, "GET", "/api/v1/inventory/stock-balances?itemId="+milk+"&byBatch=true", nil).Items(); len(b) != 1 || b[0]["batchNo"] != "MB-"+k.sfx ||
		b[0]["expiryDate"] != exp {
		t.Fatalf("batch received: %v", b)
	}

	// FR-GR-02: above the ordered quantity → reason and approval.
	over := map[string]any{"purchaseOrderId": poID, "lines": []map[string]any{{"purchaseOrderLineId": lm, "acceptedQuantity": "10",
		"batchNo": "MB-" + k.sfx, "expiryDate": exp}}}
	sa.Must(422, "POST", "/api/v1/procurement/goods-receipts", over)
	over["reason"] = "Supplier sent a full case"
	g2 := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", over, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, g2, "pending_approval")
	if g2["approvalReason"] != "over_receipt" {
		t.Fatalf("over-receipt: %v", g2)
	}
	prcDecide(t, pm, g2["approvalRequestId"], true, "Accept the case")
	g2 = sa.Must(200, "GET", "/api/v1/procurement/goods-receipts/"+str(g2["id"]), nil).JSON()
	prcStatus(t, g2, "posted")
	if o := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+poID, nil).JSON(); o["status"] != "received" {
		t.Fatalf("PO fully received: %v", o["status"])
	}

	// FR-GR-04: purchase return → stock out and a debit note.
	ret := sa.Must(201, "POST", "/api/v1/procurement/purchase-returns", map[string]any{"goodsReceiptId": gr["id"], "reason": "Short expiry",
		"lines": []map[string]any{{"goodsReceiptLineId": prcLine(gr, 0)["id"], "quantity": "2"}}}, "Idempotency-Key", newKey()).JSON()
	invEq(t, "return value incl. PPN", ret["total"], "33300")
	if ret["debitNoteNumber"] == nil {
		t.Fatalf("debit note of the return: %v", ret)
	}
	sa.Must(422, "POST", "/api/v1/procurement/purchase-returns", map[string]any{"goodsReceiptId": gr["id"], "reason": "Too many",
		"lines": []map[string]any{{"goodsReceiptLineId": prcLine(gr, 0)["id"], "quantity": "11"}}})
	if ev := prcEvents(t, "procurement.purchase_returned", str(ret["id"])); len(ev) != 1 || ev[0]["goodsReceiptId"] != gr["id"] ||
		ev[0]["lines"].([]any)[0].(map[string]any)["batchNo"] != "MB-"+k.sfx {
		t.Fatalf("purchase_returned: %v", ev)
	}
	prcDispatch(t, "returned milk out of stock", func() bool { return invOnHand(t, sa, wh, milk).Equal(decimal.NewFromInt(20)) })
	sa.Must(200, "GET", "/api/v1/procurement/purchase-returns/"+str(ret["id"]), nil)
	if l := sa.Must(200, "GET", "/api/v1/procurement/purchase-returns?filter[goodsReceiptId]="+str(gr["id"]), nil).Items(); len(l) != 1 {
		t.Fatalf("returns: %v", l)
	}
	dns := sa.Must(200, "GET", "/api/v1/procurement/debit-notes?filter[supplierId]="+sup, nil).Items()
	if len(dns) != 1 || dns[0]["status"] != "issued" {
		t.Fatalf("open debit note: %v", dns)
	}
	// The vendor invoice of the order (received quantities) absorbs it.
	vi := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"purchaseOrderId": poID, "supplierInvoiceNo": "G1-" + k.sfx, "match": true},
		"Idempotency-Key", newKey()).JSON()
	prcStatus(t, vi, "approved")
	invEq(t, "debit note applied", vi["debitNoteTotal"], "33300")
	if d := sa.Must(200, "GET", "/api/v1/procurement/debit-notes/"+str(dns[0]["id"]), nil).JSON(); d["status"] != "applied" || d["vendorInvoiceId"] != vi["id"] {
		t.Fatalf("debit note after the invoice: %v", d)
	}

	// FR-GR-03: receipt without PO only for allowed categories, with approval.
	free := map[string]any{"supplierId": sup, "warehouseId": wh, "reason": "Emergency purchase", "lines": []map[string]any{{"itemId": milk,
		"acceptedQuantity": "3", "unitCost": "16000", "batchNo": "MX-" + k.sfx, "expiryDate": exp}}}
	sa.Must(422, "POST", "/api/v1/procurement/goods-receipts", free)
	prcPolicy(t, sa, map[string]any{"receiptWithoutPOCategories": []string{"CAT" + k.sfx}, "overReceiptAction": "reject"})
	g3 := sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", free, "Idempotency-Key", newKey()).JSON()
	prcStatus(t, g3, "pending_approval")
	if g3["approvalReason"] != "without_po" || g3["purchaseOrderId"] != nil {
		t.Fatalf("receipt without PO: %v", g3)
	}
	prcDecide(t, pm, g3["approvalRequestId"], true, "")
	prcDispatch(t, "receipt without PO in stock", func() bool { return invOnHand(t, sa, wh, milk).Equal(decimal.NewFromInt(23)) })

	// The policy refuses an over-receipt.
	po2 := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": racket, "quantity": "1",
		"unitPrice": "450000"}}})
	l2 := str(prcLine(po2, 0)["id"])
	if r := sa.Do("POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po2["id"], "reason": "More",
		"lines": []map[string]any{{"purchaseOrderLineId": l2, "acceptedQuantity": "2", "serialNos": []string{"R3-" + k.sfx, "R4-" + k.sfx}}}}); r.Status != 422 ||
		!strings.Contains(string(r.Body), "over_receipt") {
		t.Fatalf("over-receipt refused: %s", r)
	}

	// FR-OPS-P4-04: a goods receipt queued offline syncs once.
	ws := roleUser(t, inst, "warehouse_staff")
	item := map[string]any{"id": uuid.Must(uuid.NewV7()), "action": procurement.SyncGoodsReceiptAction, "clientTime": time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{"purchaseOrderId": po2["id"], "deliveryNoteNo": "OFF-1", "lines": []map[string]any{{"purchaseOrderLineId": l2,
			"acceptedQuantity": "1", "serialNos": []string{"R3-" + k.sfx}}}}}
	res := ws.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()
	r0 := res["results"].([]any)[0].(map[string]any)
	if r0["status"] != "accepted" {
		t.Fatalf("offline receipt: %v", res)
	}
	res = ws.Must(200, "POST", "/api/v1/platform/sync", map[string]any{"items": []any{item}}).JSON()
	if r1 := res["results"].([]any)[0].(map[string]any); r1["status"] != "duplicate" {
		t.Fatalf("offline receipt resent: %v", res)
	}
	if g := sa.Must(200, "GET", "/api/v1/procurement/goods-receipts?filter[purchaseOrderId]="+str(po2["id"]), nil).Items(); len(g) != 1 || g[0]["status"] != "posted" {
		t.Fatalf("one receipt from the offline queue: %v", g)
	}
	ws.Must(403, "POST", "/api/v1/procurement/purchase-orders", map[string]any{})

	// FR-PO-03: versioned revisions; above the approved total → re-approval.
	po3 := prcOrder(t, sa, pm, map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": milk, "quantity": "10",
		"unitPrice": "15000"}}})
	p3, l3 := str(po3["id"]), prcLine(po3, 0)["id"]
	sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+p3+":send", map[string]any{"email": "orders@pgs.test"})
	rv := sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+p3+":revise", map[string]any{"reason": "More milk",
		"lines": []map[string]any{{"lineId": l3, "quantity": "15"}}}).JSON()
	prcStatus(t, rv, "pending_approval")
	if rv["version"] != float64(2) {
		t.Fatalf("revision version: %v", rv)
	}
	rv = pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+p3+":reject", map[string]any{"reason": "Not needed"}).JSON()
	prcStatus(t, rv, "approved")
	rv = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+p3+":revise", map[string]any{"reason": "Fewer", "expectedDate": exp,
		"lines": []map[string]any{{"lineId": l3, "quantity": "8"}}}).JSON()
	prcStatus(t, rv, "approved")
	invEq(t, "revised total", rv["total"], "133200")
	if revs := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+p3+"/revisions", nil).Items(); len(revs) != 2 || revs[0]["version"] != float64(1) {
		t.Fatalf("revisions: %v", revs)
	}
	rv = sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+p3+":cancel", map[string]any{"reason": "Supplier out of stock"}).JSON()
	prcStatus(t, rv, "cancelled")
	sa.Must(409, "POST", "/api/v1/procurement/purchase-orders/"+poID+":cancel", map[string]any{"reason": "Too late"})

	// K10: documents dated in a closed accounting period are refused.
	inst.App.Purchasing.Procurement.SetPeriodGuard(func(context.Context, dbtx.Querier, uuid.UUID, time.Time) (string, error) { return "closed", nil })
	r := sa.Do("POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po2["id"], "lines": []map[string]any{{"purchaseOrderLineId": l2,
		"acceptedQuantity": "1"}}})
	inst.App.Purchasing.Procurement.SetPeriodGuard(accounting.PeriodStatus)
	if r.Status != 422 || !strings.Contains(string(r.Body), "period_closed") {
		t.Fatalf("closed period: %s", r)
	}
}

// ── EP-10 supplier master, EP-11 requisitions (manual, banquet K1, store) ─

// FR-SUP-01..04 and FR-PR-01..05: supplier master with price list, masked
// bank account / NPWP, blacklist with approval; manual requisition edited,
// rejected and approved through the approval matrix (Finance above Rp5 jt);
// two requisitions consolidated into one RFQ line, then into one PO per
// supplier; the banquet requisition of a BEO follows its revisions; a store
// requisition the store cannot supply becomes a requisition; monthly vendor
// scorecards.
func TestP4ProcurementRequisitions(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	fm := roleUser(t, inst, "finance_manager")
	gm := roleUser(t, inst, "general_manager")
	ps := roleUser(t, inst, "procurement_staff")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "PQ", nil)
	beef := k.item(t, sa, "PBEEF", k.kg, nil)
	salt := k.item(t, sa, "PSALT", k.kg, nil)

	// FR-SUP-01/02: supplier master, contact, bank account, price list.
	sx := prcSupplier(t, sa, "PQX"+k.sfx, map[string]any{"legalName": "PT Daging Segar", "npwp": "01.234.567.8-901.000", "withholdingType": "none",
		"contractSupplier": false, "currency": "IDR", "website": "https://daging.test"})
	sy := prcSupplier(t, sa, "PQY"+k.sfx, nil)
	if s := sa.Must(200, "PATCH", "/api/v1/procurement/suppliers/"+sx, map[string]any{"leadTimeDays": 1, "categories": []string{"F&B", "Banquet"}}).JSON(); s["leadTimeDays"] != float64(1) {
		t.Fatalf("supplier edit: %v", s)
	}
	sa.Must(204, "DELETE", "/api/v1/procurement/suppliers/"+prcSupplier(t, sa, "PQZ"+k.sfx, nil), nil)
	sa.Must(201, "POST", "/api/v1/procurement/supplier-contacts", map[string]any{"supplierId": sx, "name": "Rina", "email": "orders@daging.test",
		"isPrimary": true, "receivesOrders": true})
	sa.Must(201, "POST", "/api/v1/procurement/supplier-bank-accounts", map[string]any{"supplierId": sx, "bankName": "BCA", "accountNumber": "1234567890",
		"accountName": "PT Daging Segar"})
	if b := ps.Must(200, "GET", "/api/v1/procurement/supplier-bank-accounts?filter[supplierId]="+sx, nil).Items(); len(b) != 1 || b[0]["accountNumber"] != "*******890" {
		t.Fatalf("masked bank account: %v", b)
	}
	if b := fm.Must(200, "GET", "/api/v1/procurement/supplier-bank-accounts?filter[supplierId]="+sx, nil).Items(); b[0]["accountNumber"] != "1234567890" {
		t.Fatalf("finance sees the bank account: %v", b)
	}
	if s := ps.Must(200, "GET", "/api/v1/procurement/suppliers/"+sx, nil).JSON(); s["npwp"] == "01.234.567.8-901.000" || s["legalName"] != "PT Daging Segar" {
		t.Fatalf("supplier for staff: %v", s)
	}
	sa.Must(201, "POST", "/api/v1/procurement/supplier-items", map[string]any{"supplierId": sx, "itemId": beef, "uomId": k.kg, "unitPrice": "180000",
		"supplierItemCode": "DG-01", "minOrderQuantity": "5", "leadTimeDays": 1, "preferred": true})

	// Manual PR: priced from the price list; edit; rejected; approved by
	// the Procurement Manager and Finance (above Rp5 jt, approval matrix).
	pr := sa.Must(201, "POST", "/api/v1/procurement/requisitions", map[string]any{"title": "Banquet stock", "warehouseId": wh, "costCenter": "kitchen",
		"category": "F&B", "lines": []map[string]any{{"itemId": beef, "quantity": "40", "suggestedSupplierId": sx},
			{"description": "Knife sharpening service", "quantity": "1", "estimatedUnitPrice": "250000"}}}, "Idempotency-Key", newKey()).JSON()
	if l := prcLine(pr, 0); !dec(l["estimatedUnitPrice"]).Equal(decimal.NewFromInt(180000)) {
		t.Fatalf("price list estimate: %v", l)
	}
	pr = sa.Must(200, "PATCH", "/api/v1/procurement/requisitions/"+str(pr["id"]), map[string]any{"neededBy": invToday(),
		"lines": []map[string]any{{"itemId": beef, "quantity": "45", "suggestedSupplierId": sx}, {"itemId": salt, "quantity": "10", "suggestedSupplierId": sy,
			"estimatedUnitPrice": "12000"}}}).JSON()
	invEq(t, "estimated total", pr["estimatedTotal"], "8220000")
	prID := str(pr["id"])
	sa.Must(200, "POST", "/api/v1/procurement/requisitions/"+prID+":submit", nil)
	pr = pm.Must(200, "POST", "/api/v1/procurement/requisitions/"+prID+":reject", map[string]any{"reason": "Split the salt"}).JSON()
	prcStatus(t, pr, "rejected")
	sa.Must(409, "POST", "/api/v1/procurement/requisitions/"+prID+":submit", nil)
	pr = sa.Must(200, "PATCH", "/api/v1/procurement/requisitions/"+prID, map[string]any{"notes": "Salt kept"}).JSON()
	prcStatus(t, pr, "draft")
	sa.Must(200, "POST", "/api/v1/procurement/requisitions/"+prID+":submit", nil)
	pr = pm.Must(200, "POST", "/api/v1/procurement/requisitions/"+prID+":approve", map[string]any{}).JSON()
	prcStatus(t, pr, "submitted") // second step: Finance Manager above Rp5 jt
	pm.Must(403, "POST", "/api/v1/procurement/requisitions/"+prID+":approve", map[string]any{})
	pr = fm.Must(200, "POST", "/api/v1/procurement/requisitions/"+prID+":approve", map[string]any{}).JSON()
	prcStatus(t, pr, "approved")
	ps.Must(403, "POST", "/api/v1/procurement/requisitions/"+prID+":approve", map[string]any{})
	// A draft is cancelled with a reason.
	dr := sa.Must(201, "POST", "/api/v1/procurement/requisitions", map[string]any{"lines": []map[string]any{{"itemId": salt, "quantity": "2"}}},
		"Idempotency-Key", newKey()).JSON()
	sa.Must(422, "POST", "/api/v1/procurement/requisitions/"+str(dr["id"])+":cancel", map[string]any{})
	dr = sa.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(dr["id"])+":cancel", map[string]any{"reason": "Duplicate"}).JSON()
	prcStatus(t, dr, "cancelled")

	// FR-PR-05: a second requisition of salt, consolidated with the first
	// into one RFQ line; the RFQ is cancelled, then both are ordered on one
	// PO per suggested supplier.
	pr2 := sa.Must(201, "POST", "/api/v1/procurement/requisitions", map[string]any{"warehouseId": wh, "submit": true,
		"lines": []map[string]any{{"itemId": salt, "quantity": "5", "suggestedSupplierId": sy, "estimatedUnitPrice": "12000"}}}, "Idempotency-Key", newKey()).JSON()
	pr2 = pm.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(pr2["id"])+":approve", map[string]any{}).JSON()
	prcStatus(t, pr2, "approved")
	saltLines := []any{prcLine(pr, 1)["id"], prcLine(pr2, 0)["id"]}
	rfq := sa.Must(201, "POST", "/api/v1/procurement/rfqs", map[string]any{"title": "Salt " + k.sfx, "requisitionLineIds": saltLines,
		"supplierIds": []string{sy}}, "Idempotency-Key", newKey()).JSON()
	if len(rfq["lines"].([]any)) != 1 || !dec(prcLine(rfq, 0)["quantity"]).Equal(decimal.NewFromInt(15)) || len(prcLine(rfq, 0)["requisitionLineIds"].([]any)) != 2 {
		t.Fatalf("consolidated RFQ: %v", rfq)
	}
	rfq = sa.Must(200, "PATCH", "/api/v1/procurement/rfqs/"+str(rfq["id"]), map[string]any{"title": "Salt (consolidated)", "supplierIds": []string{sy, sx}}).JSON()
	if len(rfq["suppliers"].([]any)) != 2 {
		t.Fatalf("RFQ suppliers: %v", rfq)
	}
	sa.Must(200, "POST", "/api/v1/procurement/rfqs/"+str(rfq["id"])+":send", nil)
	rfq = sa.Must(200, "POST", "/api/v1/procurement/rfqs/"+str(rfq["id"])+":close", nil).JSON()
	prcStatus(t, rfq, "closed")
	sa.Must(409, "POST", "/api/v1/procurement/requisitions/"+prID+":cancel", map[string]any{"reason": "In RFQ"})
	rfq = sa.Must(200, "POST", "/api/v1/procurement/rfqs/"+str(rfq["id"])+":cancel", map[string]any{"reason": "Contract price instead"}).JSON()
	prcStatus(t, rfq, "cancelled")
	pos := sa.Must(201, "POST", "/api/v1/procurement/purchase-orders:from-requisitions", map[string]any{"requisitionLineIds": append(saltLines,
		prcLine(pr, 0)["id"]), "submit": true}, "Idempotency-Key", newKey()).Items()
	if len(pos) != 2 {
		t.Fatalf("one PO per supplier: %v", pos)
	}
	for _, o := range pos {
		if o["supplierId"] == sy && (len(o["lines"].([]any)) != 1 || !dec(prcLine(o, 0)["quantity"]).Equal(decimal.NewFromInt(15))) {
			t.Fatalf("consolidated PO: %v", o)
		}
		if o["supplierId"] == sx && !dec(prcLine(o, 0)["unitPrice"]).Equal(decimal.NewFromInt(180000)) {
			t.Fatalf("PO priced from the price list: %v", o)
		}
	}
	if p := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+prID, nil).JSON(); p["status"] != "ordered" {
		t.Fatalf("requisition ordered: %v", p)
	}
	// Cancelling the salt PO gives the quantities back to the requisitions.
	for _, o := range pos {
		if o["supplierId"] == sy {
			sa.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+str(o["id"])+":cancel", map[string]any{"reason": "Re-quote"})
		} else {
			pm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+str(o["id"])+":approve", map[string]any{})
			if a := fm.Must(200, "POST", "/api/v1/procurement/purchase-orders/"+str(o["id"])+":approve", map[string]any{}).JSON(); a["status"] != "approved" {
				t.Fatalf("PO above Rp5 jt after Finance: %v", a)
			}
		}
	}
	if p := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(pr2["id"]), nil).JSON(); p["status"] != "approved" {
		t.Fatalf("requisition released: %v", p)
	}
	if p := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+prID, nil).JSON(); p["status"] != "partially_ordered" {
		t.Fatalf("requisition partially ordered: %v", p)
	}
	var last *string
	sysQueryRow(t, inst, `SELECT trim_scale(last_price)::text FROM procurement.supplier_items WHERE supplier_id = $1 AND item_id = $2`, []any{sx, beef}, &last)
	if last == nil || *last != "180000" {
		t.Fatalf("last purchase price: %v", last)
	}

	// FR-PR-04 / K1: the banquet requisition follows the BEO; quantities
	// already ordered are kept and the revision is flagged.
	beo, ev := uuid.New(), uuid.New()
	beoEvent := func(typ string, version, pax int, qty string) uuid.UUID {
		return prcPublish(t, typ, beo, map[string]any{"beoId": beo, "beoNumber": "BEO-" + k.sfx, "version": version, "eventId": ev,
			"eventNumber": "EVT-" + k.sfx, "eventDate": time.Now().In(clubLoc(inst)).AddDate(0, 0, 30).Format("2006-01-02"), "pax": pax,
			"requirements": []map[string]any{{"itemId": beef, "itemCode": "PBEEF" + k.sfx, "itemName": "Beef tenderloin", "quantity": qty, "uomId": k.kg,
				"uom": "kg", "source": "menu"}}})
	}
	invHandled(t, beoEvent("banquet.beo_issued", 1, 300, "45"), "procurement.banquet_requisition")
	bq := sa.Must(200, "GET", "/api/v1/procurement/requisitions?filter[beoId]="+beo.String(), nil).Items()
	if len(bq) != 1 || bq[0]["source"] != "banquet" || bq[0]["eventId"] != ev.String() {
		t.Fatalf("banquet requisition: %v", bq)
	}
	b := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(bq[0]["id"]), nil).JSON()
	invEq(t, "300 pax", prcLine(b, 0)["quantity"], "45")
	invHandled(t, beoEvent("banquet.beo_revised", 2, 320, "48"), "procurement.banquet_requisition_revised")
	b = sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(bq[0]["id"]), nil).JSON()
	var open []map[string]any
	for _, l := range b["lines"].([]any) {
		if l.(map[string]any)["status"] == "open" {
			open = append(open, l.(map[string]any))
		}
	}
	if len(open) != 1 || !dec(open[0]["quantity"]).Equal(decimal.NewFromInt(48)) || b["beoVersion"] != float64(2) || !strings.Contains(str(b["title"]), "320 pax") {
		t.Fatalf("revised to 320 pax (replace, not add): %v", b)
	}
	sa.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(b["id"])+":submit", nil)
	b = pm.Must(200, "POST", "/api/v1/procurement/requisitions/"+str(b["id"])+":approve", map[string]any{}).JSON()
	prcStatus(t, b, "approved")
	sa.Must(201, "POST", "/api/v1/procurement/purchase-orders", map[string]any{"supplierId": sx, "rfqSkipReason": "Banquet urgent", "lines": []map[string]any{
		{"requisitionLineId": open[0]["id"], "quantity": "30"}}}, "Idempotency-Key", newKey())
	invHandled(t, beoEvent("banquet.beo_revised", 3, 330, "49.5"), "procurement.banquet_requisition_revised")
	invHandled(t, beoEvent("banquet.beo_revised", 3, 330, "49.5"), "procurement.banquet_requisition_revised") // repeated delivery
	bq = sa.Must(200, "GET", "/api/v1/procurement/requisitions?filter[beoId]="+beo.String(), nil).Items()
	if len(bq) != 2 {
		t.Fatalf("revision after ordering: %v", bq)
	}
	for _, x := range bq {
		d := sa.Must(200, "GET", "/api/v1/procurement/requisitions/"+str(x["id"]), nil).JSON()
		if x["id"] == b["id"] && (d["attention"] == nil || d["status"] != "ordered") {
			t.Fatalf("ordered requisition flagged: %v", d)
		}
		if x["id"] != b["id"] && (len(d["lines"].([]any)) != 1 || !dec(prcLine(d, 0)["quantity"]).Equal(decimal.RequireFromString("19.5"))) {
			t.Fatalf("additional quantity of the revision: %v", d)
		}
	}

	// FR-PR-01: a store requisition the store cannot supply (inventory).
	srq := uuid.New()
	srqEvent := func() uuid.UUID {
		return prcPublish(t, "inventory.reorder_needed", srq, map[string]any{"warehouseId": wh, "source": "requisition", "requisitionId": srq,
			"requisitionNumber": "SRQ-" + k.sfx, "items": []map[string]any{{"itemId": salt, "onHand": "0", "reorderPoint": "0", "parLevel": "0",
				"suggestedQuantity": "7", "uomId": k.kg}}})
	}
	invHandled(t, srqEvent(), "procurement.auto_requisition")
	invHandled(t, srqEvent(), "procurement.auto_requisition")
	sr := sa.Must(200, "GET", "/api/v1/procurement/requisitions?filter[source]=store_requisition&q=SRQ-"+k.sfx, nil).Items()
	if len(sr) != 1 || sr[0]["sourceRef"] != "SRQ-"+k.sfx || !dec(sr[0]["estimatedTotal"]).IsPositive() {
		t.Fatalf("store requisition PR: %v", sr)
	}

	// FR-SUP-04: blacklist with reason and approval (General Manager).
	sa.Must(422, "PATCH", "/api/v1/procurement/suppliers/"+sy, map[string]any{"status": "blocked"})
	sa.Must(422, "POST", "/api/v1/procurement/suppliers/"+sy+":block", map[string]any{})
	req := sa.Must(201, "POST", "/api/v1/procurement/suppliers/"+sy+":block", map[string]any{"reason": "Repeated late deliveries"}).JSON()
	prcStatus(t, req, "pending")
	sa.Must(409, "POST", "/api/v1/procurement/suppliers/"+sy+":block", map[string]any{"reason": "Again"})
	prcDecide(t, gm, req["approvalRequestId"], true, "Agreed")
	if s := sa.Must(200, "GET", "/api/v1/procurement/suppliers/"+sy, nil).JSON(); s["status"] != "blocked" || s["blockedReason"] != "Repeated late deliveries" {
		t.Fatalf("blocked supplier: %v", s)
	}
	if r := sa.Do("POST", "/api/v1/procurement/purchase-orders", map[string]any{"supplierId": sy, "lines": []map[string]any{{"itemId": salt, "quantity": "1",
		"unitPrice": "12000"}}}); r.Status != 422 || !strings.Contains(string(r.Body), "supplier_not_active") {
		t.Fatalf("blocked supplier ordered: %s", r)
	}
	sa.Must(409, "PATCH", "/api/v1/procurement/suppliers/"+sy, map[string]any{"status": "active"})
	un := sa.Must(201, "POST", "/api/v1/procurement/suppliers/"+sy+":unblock", map[string]any{"reason": "Performance improved"}).JSON()
	prcDecide(t, gm, un["approvalRequestId"], true, "")
	if s := sa.Must(200, "GET", "/api/v1/procurement/suppliers/"+sy, nil).JSON(); s["status"] != "active" {
		t.Fatalf("unblocked supplier: %v", s)
	}
	if l := sa.Must(200, "GET", "/api/v1/procurement/supplier-status-requests?filter[supplierId]="+sy, nil).Items(); len(l) != 2 {
		t.Fatalf("status requests: %v", l)
	}

	// FR-SUP-03: monthly scorecards (stored).
	month := time.Now().In(clubLoc(inst)).Format("2006-01")
	cards := sa.Must(200, "POST", "/api/v1/procurement/vendor-performance:compute", map[string]any{"period": month}).Items()
	if !containsID(func() []map[string]any {
		var out []map[string]any
		for _, c := range cards {
			out = append(out, map[string]any{"id": c["supplierId"]})
		}
		return out
	}(), sx) {
		t.Fatalf("scorecards: %v", cards)
	}
	if l := sa.Must(200, "GET", "/api/v1/procurement/vendor-scorecards?filter[period]="+month+"&filter[supplierId]="+sx, nil).Items(); len(l) != 1 {
		t.Fatalf("stored scorecard: %v", l)
	}
}

// ── EP-29 migration, EP-27 policies, EP-28 reports & dashboard, menus ─────

// FR-MIG-P4-04 / -06 / -07: suppliers (with bank account and contact),
// supplier items and open purchase orders with their received quantities
// are imported with a dry run (nothing written), validation errors per row,
// reconciliation totals and idempotent re-runs; the remaining quantity of a
// migrated order is received and invoiced (the part received before the
// cut-over is not billed again).
func TestP4ProcurementMigration(t *testing.T) {
	sa := superAdmin(t, inst)
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "PX", nil)
	k.item(t, sa, "PXA", k.kg, nil)
	k.item(t, sa, "PXB", k.pcs, nil)
	imp := func(entity, mode, csv string) map[string]any {
		return sa.Must(200, "POST", "/api/v1/procurement/migration:import", map[string]any{"entity": entity, "mode": mode, "csv": csv,
			"filename": entity + ".csv"}).JSON()
	}
	s1, s2 := "MX1"+k.sfx, "MX2"+k.sfx
	sup := "code,name,npwp,email,categories,pkp,withholdingType,paymentTermDays,leadTimeDays,contactName,bankName,bankAccountNumber,bankAccountName\n" +
		s1 + ",PT Migrasi Satu,01.111.111.1-111.000,satu@mx.test,F&B|Banquet,true,none,45,3,Budi,BCA,555000111,PT Migrasi Satu\n" +
		s2 + ",CV Migrasi Dua,,dua@mx.test,General,false,pph23,30,,,,,\n"
	if r := imp(procurement.ImportSuppliers, "preview", sup+"BAD"+k.sfx+",Bad,,bad@mx.test,,maybe,none,,,,,,\n"); r["status"] != "failed" ||
		len(r["errors"].([]any)) != 1 || r["errors"].([]any)[0].(map[string]any)["row"] != float64(4) {
		t.Fatalf("supplier preview errors: %v", r)
	}
	r := imp(procurement.ImportSuppliers, "preview", sup)
	if r["status"] != "valid" || r["created"] != float64(2) || r["reconciliation"].(map[string]any)["suppliers"] != float64(2) {
		t.Fatalf("supplier dry run: %v", r)
	}
	if l := sa.Must(200, "GET", "/api/v1/procurement/suppliers?q="+s1, nil).Items(); len(l) != 0 {
		t.Fatalf("the dry run wrote: %v", l)
	}
	if r = imp(procurement.ImportSuppliers, "commit", sup); r["status"] != "completed" || r["created"] != float64(2) {
		t.Fatalf("supplier commit: %v", r)
	}
	if r = imp(procurement.ImportSuppliers, "commit", sup); r["status"] != "completed" || r["created"] != float64(0) || r["updated"] != float64(2) {
		t.Fatalf("supplier re-run (idempotent): %v", r)
	}
	ls := sa.Must(200, "GET", "/api/v1/procurement/suppliers?q="+s1, nil).Items()
	if len(ls) != 1 || ls[0]["paymentTermDays"] != float64(45) || ls[0]["pkp"] != true {
		t.Fatalf("migrated supplier: %v", ls)
	}
	if b := sa.Must(200, "GET", "/api/v1/procurement/supplier-bank-accounts?filter[supplierId]="+str(ls[0]["id"]), nil).Items(); len(b) != 1 {
		t.Fatalf("migrated bank account (once): %v", b)
	}

	items := "supplierCode,itemCode,unitPrice,uom,supplierItemCode,minOrderQuantity,leadTimeDays,validFrom,preferred\n" +
		s1 + ",PXA" + k.sfx + ",12500,KG" + k.sfx + ",S1-A,10,2," + invToday() + ",true\n" + s2 + ",PXB" + k.sfx + ",3000,,,,,,\n"
	if r = imp(procurement.ImportSupplierItems, "preview", items+s1+",NOPE,1,,,,,,\n"); r["status"] != "failed" {
		t.Fatalf("supplier item errors: %v", r)
	}
	if r = imp(procurement.ImportSupplierItems, "commit", items); r["created"] != float64(2) {
		t.Fatalf("supplier items: %v", r)
	}
	if r = imp(procurement.ImportSupplierItems, "commit", items); r["updated"] != float64(2) || r["created"] != float64(0) {
		t.Fatalf("supplier items re-run: %v", r)
	}

	po := "LEG-" + k.sfx
	open := "poNumber,supplierCode,orderDate,warehouseCode,itemCode,quantity,unitPrice,receivedQuantity,taxPercent,expectedDate\n" +
		po + "," + s1 + "," + invToday() + ",PX" + k.sfx + ",PXA" + k.sfx + ",10,1000,4,11,\n" +
		po + "," + s1 + "," + invToday() + ",PX" + k.sfx + ",PXB" + k.sfx + ",5,2000,0,11,\n"
	if r = imp(procurement.ImportOpenOrders, "preview", open+"LEG2-"+k.sfx+","+s1+","+invToday()+",PX"+k.sfx+",PXA"+k.sfx+",3,1000,3,11,\n"); r["status"] != "failed" ||
		!strings.Contains(fmt.Sprint(r["errors"]), "not_open") {
		t.Fatalf("closed line refused: %v", r)
	}
	r = imp(procurement.ImportOpenOrders, "preview", open)
	rec := r["reconciliation"].(map[string]any)
	if r["status"] != "valid" || rec["purchaseOrders"] != float64(1) || rec["orderLines"] != float64(2) {
		t.Fatalf("open PO dry run: %v", r)
	}
	invEq(t, "ordered value", rec["orderedValue"], "20000")
	invEq(t, "received before cut-over", rec["receivedValue"], "4000")
	invEq(t, "outstanding PO value", rec["outstandingValue"], "16000")
	if r = imp(procurement.ImportOpenOrders, "commit", open); r["status"] != "completed" || r["created"] != float64(1) {
		t.Fatalf("open PO commit: %v", r)
	}
	if r = imp(procurement.ImportOpenOrders, "commit", open); r["skipped"] != float64(1) || r["created"] != float64(0) {
		t.Fatalf("open PO re-run: %v", r)
	}
	lst := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders?q="+po, nil).Items()
	if len(lst) != 1 || lst[0]["status"] != "partially_received" || lst[0]["source"] != "migration" || lst[0]["warehouseId"] != wh {
		t.Fatalf("migrated PO: %v", lst)
	}
	invEq(t, "outstanding", lst[0]["outstandingValue"], "16000")
	o := sa.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+str(lst[0]["id"]), nil).JSON()
	la := prcLine(o, 0)
	invEq(t, "opening received", la["openingReceivedQuantity"], "4")
	sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": o["id"],
		"lines": []map[string]any{{"purchaseOrderLineId": la["id"], "acceptedQuantity": "6"}}}, "Idempotency-Key", newKey())
	vi := sa.Must(201, "POST", "/api/v1/procurement/vendor-invoices", map[string]any{"purchaseOrderId": o["id"], "supplierInvoiceNo": "MX-" + k.sfx, "match": true},
		"Idempotency-Key", newKey()).JSON()
	if len(vi["lines"].([]any)) != 1 || !dec(prcLine(vi, 0)["quantity"]).Equal(decimal.NewFromInt(6)) {
		t.Fatalf("invoice of the post cut-over receipt only: %v", vi)
	}
	prcStatus(t, vi, "approved")
	roleUser(t, inst, "procurement_staff").Must(403, "POST", "/api/v1/procurement/migration:import", map[string]any{})
}

// FR-POL-P4-02, FR-RPT-P4-02/04/05 and the menus: Procurement Policies and
// Configuration in the club policy catalogue (an RFQ threshold refuses a
// direct order above it without reason), every procurement report runs
// with its filters, Outstanding PO = the dashboard KPI, the Procurement
// Performance dashboard, the Back Office / management / Warehouse menus
// and role permissions.
func TestP4ProcurementReports(t *testing.T) {
	sa := superAdmin(t, inst)
	pm := roleUser(t, inst, "procurement_manager")
	k := newInvKit(t, sa)
	wh := k.warehouse(t, sa, "PY", nil)
	tea := k.item(t, sa, "PTEA", k.pcs, nil)
	sup := prcSupplier(t, sa, "PYS"+k.sfx, nil)

	found := map[string]bool{}
	for _, e := range sa.Must(200, "GET", "/api/v1/platform/club-policies/catalog", nil).Items() {
		if e["category"] == procurement.PolicyCategory || e["category"] == procurement.ConfigurationCategory {
			found[str(e["code"])] = true
		}
	}
	if !found[procurement.PolicyCode] || !found[procurement.ConfigurationCode] {
		t.Fatalf("procurement policies missing from the catalogue: %v", found)
	}
	// FR-RFQ-04: above the RFQ threshold a direct order needs an RFQ, a
	// contract supplier or a reason.
	prcPolicy(t, sa, map[string]any{"rfqRequiredAbove": "100000"})
	big := map[string]any{"supplierId": sup, "warehouseId": wh, "lines": []map[string]any{{"itemId": tea, "quantity": "100", "unitPrice": "2500"}}}
	if r := sa.Do("POST", "/api/v1/procurement/purchase-orders", big); r.Status != 422 || !strings.Contains(string(r.Body), "rfq_required") {
		t.Fatalf("RFQ threshold: %s", r)
	}
	big["rfqSkipReason"] = "Sole distributor"
	po := prcOrder(t, sa, pm, big)
	sa.Must(201, "POST", "/api/v1/procurement/goods-receipts", map[string]any{"purchaseOrderId": po["id"],
		"lines": []map[string]any{{"purchaseOrderLineId": prcLine(po, 0)["id"], "acceptedQuantity": "40", "rejectedQuantity": "2", "rejectionReason": "Torn"}}},
		"Idempotency-Key", newKey())

	loc := clubLoc(inst)
	from, to := time.Now().In(loc).AddDate(0, 0, -30).Format("2006-01-02"), invToday()
	n := 0
	for _, r := range sa.Must(200, "GET", "/api/v1/reporting/reports", nil).Items() {
		if r["module"] != "procurement" {
			continue
		}
		n++
		res := sa.Must(200, "GET", "/api/v1/reporting/reports/"+str(r["code"])+"?params[from]="+from+"&params[to]="+to, nil).JSON()
		if res["rows"] == nil {
			t.Fatalf("%s: %v", r["code"], res)
		}
		sa.Must(200, "GET", "/api/v1/reporting/reports/"+str(r["code"])+"?params[from]="+from+"&params[to]="+to+"&params[supplier]=PYS"+k.sfx+
			"&params[warehouse]=PY"+k.sfx, nil)
	}
	if n != 10 {
		t.Fatalf("procurement reports: %d", n)
	}
	rows := sa.Must(200, "GET", "/api/v1/reporting/reports/procurement.outstanding_po?params[to]="+to+"&params[supplier]=PYS"+k.sfx, nil).JSON()["rows"].([]any)
	if len(rows) != 1 || !dec(rows[0].(map[string]any)["outstandingValue"]).Equal(decimal.NewFromInt(150000)) {
		t.Fatalf("Outstanding PO Report: %v", rows)
	}
	gr := sa.Must(200, "GET", "/api/v1/reporting/reports/procurement.goods_receipt?params[from]="+from+"&params[to]="+to+"&params[supplier]=PYS"+k.sfx, nil).JSON()["rows"].([]any)
	if len(gr) != 1 || gr[0].(map[string]any)["rejectionReason"] != "Torn" || gr[0].(map[string]any)["onTime"] != true {
		t.Fatalf("Goods Receipt Report: %v", gr)
	}
	vp := sa.Must(200, "GET", "/api/v1/reporting/reports/procurement.vendor_performance?params[from]="+from+"&params[to]="+to+"&params[supplier]=PYS"+k.sfx, nil).JSON()["rows"].([]any)
	if len(vp) != 1 || !dec(vp[0].(map[string]any)["qualityRate"]).Equal(decimal.RequireFromString("0.9524")) {
		t.Fatalf("Vendor Performance Report: %v", vp)
	}
	d := sa.Must(200, "GET", "/api/v1/reporting/dashboards/procurement-performance?from="+from+"&to="+to, nil).JSON()
	if d["name"] != "Procurement Performance" || len(d["kpis"].([]any)) != 8 {
		t.Fatalf("dashboard: %v", d)
	}
	var total decimal.Decimal
	for _, row := range sa.Must(200, "GET", "/api/v1/reporting/reports/procurement.outstanding_po?params[to]="+to+"&limit=5000", nil).JSON()["rows"].([]any) {
		total = total.Add(dec(row.(map[string]any)["outstandingValue"]))
	}
	for _, kp := range d["kpis"].([]any) {
		if m := kp.(map[string]any); m["key"] == "outstanding_po" && !dec(m["value"]).Equal(total) {
			t.Fatalf("Outstanding PO KPI %v ≠ report %s", m["value"], total)
		}
	}

	// Menus: Back Office Procurement, management dashboard, Warehouse GR.
	hasKey := func(c *Client, shell, key string) bool {
		var walk func(items []any) bool
		walk = func(items []any) bool {
			for _, it := range items {
				m := it.(map[string]any)
				if m["key"] == key && m["comingSoon"] != true {
					return true
				}
				if ch, ok := m["children"].([]any); ok && walk(ch) {
					return true
				}
			}
			return false
		}
		return walk(c.Must(200, "GET", "/api/v1/platform/navigation?shell="+shell, nil).JSON()["items"].([]any))
	}
	if !hasKey(sa, "backoffice", "vendor-invoices") || !hasKey(sa, "management", "procurement-performance") || !hasKey(sa, "ops", "warehouse-goods-receipt") {
		t.Fatal("procurement menus missing")
	}
	ws := roleUser(t, inst, "warehouse_staff")
	if !hasKey(ws, "ops", "warehouse-goods-receipt") || hasKey(ws, "backoffice", "vendor-invoices") {
		t.Fatal("warehouse staff menus")
	}
	ws.Must(200, "GET", "/api/v1/procurement/receivable-orders", nil)
	ws.Must(403, "GET", "/api/v1/procurement/vendor-invoices", nil)
	stf := roleUser(t, inst, "procurement_staff")
	stf.Must(403, "POST", "/api/v1/procurement/purchase-orders/"+str(po["id"])+":approve", map[string]any{})
	stf.Must(200, "GET", "/api/v1/procurement/purchase-orders/"+str(po["id"]), nil)
	roleUser(t, inst, "golf_staff").Must(403, "GET", "/api/v1/procurement/purchase-orders", nil)
}
