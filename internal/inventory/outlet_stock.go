package inventory

// Outlet stock for the business lines (tee houses, demo feedback 9 Oct
// 2026): an outlet gets its own warehouse — POS sales of the outlet consume
// there — replenished from the store of a template outlet, with the
// template's par stock; the stock monitoring shows each outlet's balance
// against its par / minimum.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
)

// EnsureOutletWarehouse creates (once) the warehouse of an outlet, copying
// the parent store and the par stock of the template warehouse.
func EnsureOutletWarehouse(ctx context.Context, tx pgx.Tx, property, outlet uuid.UUID, code, name, costCenter, templateCode string) (uuid.UUID, error) {
	var tmpl, parent *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id, parent_id FROM inventory.warehouses WHERE property_id = $1 AND code = $2`, property, templateCode).Scan(&tmpl, &parent)
	if parent == nil {
		_ = tx.QueryRow(ctx, `SELECT id FROM inventory.warehouses WHERE property_id = $1 AND code = 'MAIN-STORE'`, property).Scan(&parent)
	}
	var wid uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO inventory.warehouses (id, property_id, code, name, location_type, parent_id, outlet_id, cost_center, created_by)
		VALUES ($1,$2,$3,$4,'outlet',$5,$6,$7,$8) ON CONFLICT (property_id, code) DO UPDATE SET outlet_id = coalesce(inventory.warehouses.outlet_id, EXCLUDED.outlet_id)
		RETURNING id`, id.New(), property, code, name, parent, outlet, costCenter, handle.UserID(ctx)).Scan(&wid); err != nil {
		return uuid.Nil, err
	}
	if tmpl != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.par_stocks (id, property_id, item_id, warehouse_id, par_level, min_stock, created_by)
			SELECT gen_random_uuid(), property_id, item_id, $2, par_level, min_stock, $3 FROM inventory.par_stocks WHERE warehouse_id = $1
			ON CONFLICT (warehouse_id, item_id) DO NOTHING`, *tmpl, wid, handle.UserID(ctx)); err != nil {
			return wid, err
		}
	}
	return wid, nil
}

// OutletStockLine is one item of an outlet's warehouse against its par.
type OutletStockLine struct {
	OutletID    uuid.UUID `json:"outletId" db:"outlet_id"`
	WarehouseID uuid.UUID `json:"warehouseId" db:"warehouse_id"`
	Warehouse   string    `json:"warehouse" db:"warehouse"`
	ItemID      uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode    string    `json:"itemCode" db:"item_code"`
	ItemName    string    `json:"itemName" db:"item_name"`
	Unit        string    `json:"unit" db:"unit"`
	OnHand      string    `json:"onHand" db:"on_hand"`
	ParLevel    *string   `json:"parLevel" db:"par_level"`
	MinStock    *string   `json:"minStock" db:"min_stock"`
	Status      string    `json:"status" db:"status" enum:"ok,low,out" doc:"low: at or under the minimum (or half the par); out: nothing left"`
	Refill      string    `json:"refill" db:"refill" doc:"Quantity to bring the item back to par"`
}

// OutletStock lists the stock of the outlets' warehouses against par.
func OutletStock(ctx context.Context, q dbtx.Querier, property uuid.UUID, outlets []uuid.UUID) ([]OutletStockLine, error) {
	return handle.List[OutletStockLine](q.Query(ctx, `WITH lines AS (
		  SELECT w.outlet_id, w.id AS warehouse_id, w.name AS warehouse, i.id AS item_id, i.code AS item_code, i.name AS item_name, u.code AS unit,
		    coalesce((SELECT sum(b.quantity) FROM inventory.stock_balances b WHERE b.warehouse_id = w.id AND b.item_id = i.id), 0) AS on_hand,
		    p.par_level, p.min_stock
		  FROM inventory.warehouses w
		  JOIN (SELECT warehouse_id, item_id FROM inventory.par_stocks UNION SELECT warehouse_id, item_id FROM inventory.stock_balances WHERE quantity <> 0) k ON k.warehouse_id = w.id
		  JOIN inventory.items i ON i.id = k.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		  LEFT JOIN inventory.par_stocks p ON p.warehouse_id = w.id AND p.item_id = i.id
		  WHERE w.property_id = $1 AND w.outlet_id = ANY($2) AND w.archived_at IS NULL)
		SELECT outlet_id, warehouse_id, warehouse, item_id, item_code, item_name, unit, trim_scale(on_hand)::text AS on_hand,
		  trim_scale(par_level)::text AS par_level, trim_scale(min_stock)::text AS min_stock,
		  CASE WHEN on_hand <= 0 THEN 'out'
		    WHEN on_hand <= greatest(coalesce(min_stock, 0), coalesce(par_level, 0) / 2) THEN 'low' ELSE 'ok' END AS status,
		  trim_scale(greatest(coalesce(par_level, 0) - on_hand, 0))::text AS refill
		FROM lines ORDER BY warehouse, (CASE WHEN on_hand <= 0 THEN 0 WHEN on_hand <= greatest(coalesce(min_stock, 0), coalesce(par_level, 0) / 2) THEN 1 ELSE 2 END), item_name`,
		property, outlets))
}
