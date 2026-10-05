package inventory

// PRD P4 FR-VAL-06 (and FR-VAL-03 landed cost allocated after the receipt):
// when the vendor invoice price of received goods differs from the receipt
// cost, procurement's 3-way matching publishes
// procurement.invoice_price_variance. The part of the received quantity
// still in stock is revalued with a value-only adjustment (the received
// quantity out at the receipt cost, back in at the invoiced cost); the part
// already used goes to COGS or the price variance account per Inventory
// Configuration (inventory.revaluation_posted for the journal).

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/platform/outbox"
)

// EventRevaluationPosted is published for every revalued receipt line.
const EventRevaluationPosted = "inventory.revaluation_posted"

// OnInvoicePriceVariance revalues receipts whose invoiced cost differs.
func (s *Stock) OnInvoicePriceVariance(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		VendorInvoiceID uuid.UUID `json:"vendorInvoiceId"`
		Number          string    `json:"number"`
		GoodsReceiptID  uuid.UUID `json:"goodsReceiptId"`
		WarehouseID     uuid.UUID `json:"warehouseId"`
		Lines           []struct {
			ItemID               uuid.UUID `json:"itemId"`
			InvoicedBaseUnitCost string    `json:"invoicedBaseUnitCost"`
		} `json:"lines"`
	}
	if err := e.Decode(&p); err != nil || e.PropertyID == nil || p.VendorInvoiceID == uuid.Nil || p.GoodsReceiptID == uuid.Nil {
		return nil //nolint:nilerr // not a price variance event
	}
	property := *e.PropertyID
	ctx = eventCtx(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	src := p.GoodsReceiptID
	var lines []PostLine
	var out []map[string]any
	for _, l := range p.Lines {
		var recvQty, recvCost string
		err := tx.QueryRow(ctx, `SELECT coalesce(sum(ml.quantity), 0)::text, coalesce(max(ml.unit_cost), 0)::text FROM inventory.stock_movement_lines ml
			JOIN inventory.stock_movements m ON m.id = ml.movement_id WHERE m.property_id = $1 AND m.source_type = 'goods_receipt' AND m.source_id = $2
			AND m.movement_type = 'receipt' AND m.reversal_of IS NULL AND ml.item_id = $3`, property, p.GoodsReceiptID, l.ItemID).Scan(&recvQty, &recvCost)
		if err != nil {
			return err
		}
		received, oldCost, newCost := dec(recvQty), dec(recvCost), dec(l.InvoicedBaseUnitCost)
		it := l.ItemID
		if !received.IsPositive() {
			if err := exception(ctx, tx, property, e, "revaluation", &src, &it, &p.WarehouseID, nil, "goods receipt line not found"); err != nil {
				return err
			}
			continue
		}
		diff := newCost.Sub(oldCost)
		if diff.IsZero() || newCost.IsNegative() {
			continue
		}
		var hand string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text FROM inventory.stock_balances WHERE warehouse_id = $1 AND item_id = $2`,
			p.WarehouseID, l.ItemID).Scan(&hand); err != nil {
			return err
		}
		inStock := decimal.Max(decimal.Min(dec(hand), received), decimal.Zero)
		used := received.Sub(inStock)
		if inStock.IsPositive() {
			oc, nc := oldCost, newCost
			lines = append(lines, PostLine{ItemID: l.ItemID, Quantity: inStock.Neg(), UnitCost: &oc}, PostLine{ItemID: l.ItemID, Quantity: inStock, UnitCost: &nc})
		}
		out = append(out, map[string]any{"itemId": l.ItemID, "receivedQuantity": received.String(), "inStockQuantity": inStock.String(),
			"consumedQuantity": used.String(), "receivedUnitCost": oldCost.String(), "invoicedUnitCost": newCost.String(),
			"stockAmount": inStock.Mul(diff).Round(4).String(), "consumedAmount": used.Mul(diff).Round(4).String()})
	}
	if len(out) == 0 {
		return nil
	}
	var mid *uuid.UUID
	if len(lines) > 0 {
		rid := uuid.NewSHA1(p.VendorInvoiceID, []byte(p.GoodsReceiptID.String()))
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.stock_movements WHERE source_type = 'revaluation' AND source_id = $1)`, rid).
			Scan(&exists); err != nil || exists {
			return err // posted already (idempotent)
		}
		m, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveAdjustment, SourceType: "revaluation", SourceID: &rid, WarehouseID: p.WarehouseID,
			Reason: "price_revaluation", Notes: "Vendor invoice " + p.Number, Auto: true, ExplicitCost: true, AllowNegative: true,
			Lines: lines})
		if err != nil {
			return err
		}
		mid = &m.ID
	}
	if s.Events == nil {
		return nil
	}
	_, err = s.Events.Publish(ctx, tx, EventRevaluationPosted, "inventory.revaluation", &p.VendorInvoiceID, &property, map[string]any{
		"vendorInvoiceId": p.VendorInvoiceID, "number": p.Number, "goodsReceiptId": p.GoodsReceiptID, "warehouseId": p.WarehouseID, "movementId": mid,
		"consumedTo": cfg.PriceVarianceTo, "currency": cfg.Currency, "lines": out})
	return err
}
