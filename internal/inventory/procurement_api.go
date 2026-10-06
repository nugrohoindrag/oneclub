package inventory

// Public reads for Procurement (PRD P4 EP-11..EP-15, same back-office
// layer): item master data and UOM conversion to the stock (base) UOM, so
// requisitions, orders and goods receipts carry base-UOM quantities and
// costs (contract procurement.goods_received). Procurement only reads;
// stock is posted by inventory from procurement events.

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// ProcurementItem is the procurement view of an inventory item.
type ProcurementItem struct {
	ID            uuid.UUID
	Code          string
	Name          string
	Category      string
	ItemType      string
	Status        string
	BaseUOMID     uuid.UUID
	PurchaseUOMID *uuid.UUID
	StandardCost  decimal.Decimal
}

// ProcurementItemByID returns one item of the caller's property scope.
func ProcurementItemByID(ctx context.Context, q dbtx.Querier, itemID uuid.UUID) (ProcurementItem, error) {
	var it ProcurementItem
	var category *string
	var cost string
	err := q.QueryRow(ctx, `SELECT id, code, name, category, item_type, status, base_uom_id, purchase_uom_id, standard_cost::text
		FROM inventory.items WHERE id = $1`, itemID).
		Scan(&it.ID, &it.Code, &it.Name, &category, &it.ItemType, &it.Status, &it.BaseUOMID, &it.PurchaseUOMID, &cost)
	if dbtx.IsNoRows(err) {
		return it, errs.Validation("invalid_item", "item not found", errs.Field("itemId", "not_found", "item not found in this property"))
	}
	if err != nil {
		return it, err
	}
	if category != nil {
		it.Category = *category
	}
	it.StandardCost = dec(cost)
	return it, nil
}

// ProcurementConvert converts qty of an item between two UOMs with the
// inventory conversion table (item-specific conversions win over general
// ones; the inverse direction is used when needed).
func ProcurementConvert(ctx context.Context, q dbtx.Querier, itemID *uuid.UUID, qty decimal.Decimal, from, to uuid.UUID) (decimal.Decimal, error) {
	return convert(ctx, q, itemID, qty, from, to)
}

// ProcurementToBase converts qty in uom to the item's stock (base) UOM and
// returns the base quantity and the base UOM.
func ProcurementToBase(ctx context.Context, q dbtx.Querier, itemID uuid.UUID, qty decimal.Decimal, uom uuid.UUID) (decimal.Decimal, uuid.UUID, error) {
	it, err := ProcurementItemByID(ctx, q, itemID)
	if err != nil {
		return qty, uuid.Nil, err
	}
	b, err := convert(ctx, q, &itemID, qty, uom, it.BaseUOMID)
	return b, it.BaseUOMID, err
}
