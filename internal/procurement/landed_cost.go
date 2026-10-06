package procurement

// PRD P4 FR-VAL-03 (Should) landed cost: freight, duty or insurance billed
// on a vendor invoice line marked landedCost (value | quantity) is allocated
// to the stock lines of the goods receipt it belongs to (landedGoodsReceiptId,
// else the receipts of the invoice's other lines), by received value or base
// quantity. The line is posted like received stock (account hint inventory:
// it clears GRNI) and the approval publishes the allocated cost per base unit
// through procurement.invoice_price_variance, so inventory revalues the part
// still in stock and sends the used part to cost of sales — the existing
// revaluation path of FR-VAL-06.

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// landedBases are the allocation bases of a landed cost line.
var landedBases = map[string]bool{"value": true, "quantity": true}

// checkLandedLine validates a landed cost input line: no order / receipt
// line or item, a posted receipt of the property when given.
func checkLandedLine(ctx context.Context, tx pgx.Tx, property uuid.UUID, f string, l VendorInvoiceLineInput) error {
	if !landedBases[l.LandedCost] {
		return handle.Invalid(f+".landedCost", "invalid", "value or quantity")
	}
	if l.PurchaseOrderLineID != nil || l.GoodsReceiptLineID != nil || l.ItemID != nil {
		return handle.Invalid(f+".landedCost", "invalid", "a landed cost line has no order line, receipt line or item")
	}
	if strings.TrimSpace(l.Description) == "" {
		return handle.Invalid(f+".description", "required", "describe the landed cost (freight, duty, insurance)")
	}
	if l.LandedGoodsReceiptID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.goods_receipts WHERE id = $1 AND property_id = $2 AND status = 'posted')`,
			*l.LandedGoodsReceiptID, property).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid(f+".landedGoodsReceiptId", "not_found", "posted goods receipt not found")
		}
	}
	return nil
}

// landedTargets are the goods receipts a landed cost line is allocated to.
func landedTargets(ctx context.Context, q dbtx.Querier, invoice uuid.UUID, l VendorInvoiceLine) ([]uuid.UUID, error) {
	if l.LandedGoodsReceiptID != nil {
		return []uuid.UUID{*l.LandedGoodsReceiptID}, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT g.id FROM procurement.vendor_invoice_lines il JOIN procurement.goods_receipt_lines gl
		ON gl.id = il.goods_receipt_line_id OR (il.goods_receipt_line_id IS NULL AND gl.purchase_order_line_id = il.purchase_order_line_id)
		JOIN procurement.goods_receipts g ON g.id = gl.goods_receipt_id AND g.status = 'posted'
		WHERE il.vendor_invoice_id = $1 AND il.landed_cost_basis IS NULL ORDER BY g.id`, invoice)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// matchLanded is the matching result of a landed cost line: matched when
// there is stock received to allocate it to.
func matchLanded(ctx context.Context, q dbtx.Querier, v VendorInvoice, l VendorInvoiceLine) (string, string, error) {
	grs, err := landedTargets(ctx, q, v.ID, l)
	if err != nil {
		return "", "", err
	}
	var n int
	if len(grs) > 0 {
		if err := q.QueryRow(ctx, `SELECT count(*) FROM procurement.goods_receipt_lines WHERE goods_receipt_id = ANY($1) AND item_id IS NOT NULL
			AND base_quantity > 0 AND accepted_quantity > 0`, grs).Scan(&n); err != nil {
			return "", "", err
		}
	}
	if n == 0 {
		return "not_received", "no received stock to allocate the landed cost to", nil
	}
	return "matched", fmt.Sprintf("landed cost allocated by %s to %d receipt line(s)", deref(l.LandedCost), n), nil
}

// landedKey is a goods receipt × item.
type landedKey struct{ gr, item uuid.UUID }

// landedShare is the landed cost per base unit of a receipt item.
type landedShare struct {
	wh       uuid.UUID
	received decimal.Decimal // receipt base unit cost
	perUnit  decimal.Decimal
}

// landedShares allocates the landed cost lines of an invoice to the stock
// lines of their goods receipts (per base unit of each receipt item).
func landedShares(ctx context.Context, q dbtx.Querier, v VendorInvoice) (map[landedKey]*landedShare, []landedKey, error) {
	out := map[landedKey]*landedShare{}
	var order []landedKey
	type gline struct {
		key       landedKey
		wh        uuid.UUID
		qty, cost decimal.Decimal
		weight    decimal.Decimal
	}
	for _, l := range v.Lines {
		if l.LandedCost == nil {
			continue
		}
		grs, err := landedTargets(ctx, q, v.ID, l)
		if err != nil || len(grs) == 0 {
			return out, order, err
		}
		rows, err := q.Query(ctx, `SELECT gl.goods_receipt_id, g.warehouse_id, gl.item_id, gl.base_quantity::text, gl.base_unit_cost::text
			FROM procurement.goods_receipt_lines gl JOIN procurement.goods_receipts g ON g.id = gl.goods_receipt_id
			WHERE gl.goods_receipt_id = ANY($1) AND gl.item_id IS NOT NULL AND gl.base_quantity > 0 AND gl.accepted_quantity > 0
			ORDER BY g.number, gl.line_no`, grs)
		if err != nil {
			return out, order, err
		}
		var lines []gline
		total := decimal.Zero
		for rows.Next() {
			var x gline
			var qty, cost string
			if err := rows.Scan(&x.key.gr, &x.wh, &x.key.item, &qty, &cost); err != nil {
				rows.Close()
				return out, order, err
			}
			x.qty, x.cost = dec(qty), dec(cost)
			x.weight = x.qty
			if *l.LandedCost == "value" {
				x.weight = x.qty.Mul(x.cost)
			}
			total = total.Add(x.weight)
			lines = append(lines, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, order, err
		}
		if !total.IsPositive() {
			continue
		}
		amount := dec(l.LineSubtotal)
		qtyOf := map[landedKey]decimal.Decimal{}
		for _, x := range lines {
			qtyOf[x.key] = qtyOf[x.key].Add(x.qty)
		}
		for _, x := range lines {
			s, ok := out[x.key]
			if !ok {
				s = &landedShare{wh: x.wh, received: x.cost}
				out[x.key] = s
				order = append(order, x.key)
			}
			s.perUnit = s.perUnit.Add(amount.Mul(x.weight).Div(total).Div(qtyOf[x.key]))
		}
	}
	for _, k := range order {
		out[k].perUnit = out[k].perUnit.Round(6)
	}
	return out, order, nil
}
