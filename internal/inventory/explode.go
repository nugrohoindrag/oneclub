package inventory

// Public recipe explosion for the P3 ↔ P4 contracts K1 (procurement
// requirement from BEO and packages) and K6 (consumption of packages and
// banquets): business lines call it through the module's root package
// (Technical Doc §4.2 rule 3) to turn "N portions of recipe R" into base-UOM
// item quantities.

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// Requirement is one item of an exploded recipe in the item's base UOM.
type Requirement struct {
	ItemID   uuid.UUID       `json:"itemId"`
	ItemCode string          `json:"itemCode"`
	ItemName string          `json:"itemName"`
	Quantity decimal.Decimal `json:"quantity"`
	UOMID    uuid.UUID       `json:"uomId"`
	UOM      string          `json:"uom"`
}

// ExplodeRecipe returns the base-UOM item quantities of `units` yield units
// of a recipe, sub-recipes resolved, waste grossed up (FR-BOM-02), ordered by
// item code.
func ExplodeRecipe(ctx context.Context, q dbtx.Querier, recipeID uuid.UUID, units decimal.Decimal) ([]Requirement, error) {
	into := map[uuid.UUID]decimal.Decimal{}
	if err := explode(ctx, q, recipeID, units, 0, into); err != nil {
		return nil, err
	}
	return requirements(ctx, q, into)
}

// ProductRecipe returns the active menu recipe of a product, if any.
func ProductRecipe(ctx context.Context, q dbtx.Querier, productID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM inventory.recipes WHERE product_id = $1 AND status = 'active' AND archived_at IS NULL
		ORDER BY created_at LIMIT 1`, productID).Scan(&id)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// MergeRequirements adds requirement lists item by item.
func MergeRequirements(lists ...[]Requirement) []Requirement {
	by := map[uuid.UUID]*Requirement{}
	var order []uuid.UUID
	for _, l := range lists {
		for _, r := range l {
			if x, ok := by[r.ItemID]; ok {
				x.Quantity = x.Quantity.Add(r.Quantity)
				continue
			}
			c := r
			by[r.ItemID] = &c
			order = append(order, r.ItemID)
		}
	}
	out := make([]Requirement, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ItemCode < out[j].ItemCode })
	return out
}

func requirements(ctx context.Context, q dbtx.Querier, into map[uuid.UUID]decimal.Decimal) ([]Requirement, error) {
	out := make([]Requirement, 0, len(into))
	for item, qty := range into {
		r := Requirement{ItemID: item, Quantity: qty.Round(6)}
		if err := q.QueryRow(ctx, `SELECT i.code, i.name, i.base_uom_id, u.code FROM inventory.items i JOIN inventory.uoms u ON u.id = i.base_uom_id
			WHERE i.id = $1`, item).Scan(&r.ItemCode, &r.ItemName, &r.UOMID, &r.UOM); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemCode < out[j].ItemCode })
	return out, nil
}
