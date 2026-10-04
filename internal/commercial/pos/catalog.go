package pos

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
)

// ComboItems are the components of a combo / package product with their
// quantity (theoretical food cost, FR-BOM-03).
func (m *Module) ComboItems(ctx context.Context, q dbtx.Querier, productID uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT combo_items FROM commercial.products WHERE id = $1`, productID).Scan(&raw); err != nil {
		if dbtx.IsNoRows(err) {
			return map[uuid.UUID]decimal.Decimal{}, nil
		}
		return nil, err
	}
	var items []struct {
		ProductID uuid.UUID `json:"productId"`
		Quantity  any       `json:"quantity"`
	}
	_ = json.Unmarshal(raw, &items)
	out := map[uuid.UUID]decimal.Decimal{}
	for _, it := range items {
		q, _ := decimal.NewFromString(fmt.Sprint(it.Quantity))
		out[it.ProductID] = out[it.ProductID].Add(q)
	}
	return out, nil
}

// SellingPrice is the list price of a product.
func (m *Module) SellingPrice(ctx context.Context, q dbtx.Querier, productID uuid.UUID) (decimal.Decimal, error) {
	var raw string
	err := q.QueryRow(ctx, `SELECT coalesce(price, 0)::text FROM commercial.products WHERE id = $1`, productID).Scan(&raw)
	d, _ := decimal.NewFromString(raw)
	return d, err
}

// ProductNames returns the names of products.
func (m *Module) ProductNames(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return names(ctx, q, `SELECT id, name FROM commercial.products WHERE id = ANY($1)`, ids)
}

// OutletNames returns the names of outlets.
func (m *Module) OutletNames(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return names(ctx, q, `SELECT id, name FROM commercial.outlets WHERE id = ANY($1)`, ids)
}

func names(ctx context.Context, q dbtx.Querier, sql string, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := q.Query(ctx, sql, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// DefaultOutlet is the outlet that prepares banquet / catering orders when
// none is chosen.
func (m *Module) DefaultOutlet(ctx context.Context, q dbtx.Querier, property uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM commercial.outlets WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		ORDER BY (outlet_type = 'banquet') DESC, (outlet_type = 'restaurant') DESC, name LIMIT 1`, property).Scan(&id)
	return id, err
}
