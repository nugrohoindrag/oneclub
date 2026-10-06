package inventory

// Read side of PRD P4 inventory: Stock Balance per warehouse × item (× batch)
// with value and pack quantities (FR-STK-02, FR-INV-03), barcode lookup for
// `ops` scanning (FR-OPS-P4-01), the movement ledger and the stock card with
// running balance (FR-STK-01 AC), FEFO pick suggestions (FR-STK-07), the
// as-of Stock Valuation (FR-VAL-05), Theoretical vs Actual consumption per
// outlet (FR-CNS-06), slow moving items (FR-RPL-04) and the document lists.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// packFactorSQL is the number of base units in one purchase unit of item i.
const packFactorSQL = `(SELECT CASE WHEN c.from_uom_id = i.purchase_uom_id THEN c.factor ELSE 1 / c.factor END FROM inventory.uom_conversions c
	WHERE i.purchase_uom_id IS NOT NULL AND i.purchase_uom_id <> i.base_uom_id
	AND ((c.from_uom_id = i.purchase_uom_id AND c.to_uom_id = i.base_uom_id) OR (c.from_uom_id = i.base_uom_id AND c.to_uom_id = i.purchase_uom_id))
	AND (c.item_id IS NULL OR c.item_id = i.id) ORDER BY (c.item_id IS NOT NULL) DESC LIMIT 1)`

// Packs renders a base quantity in purchase units and the rest, e.g. 100
// bottles with 24 per carton = "4 CTN + 4 BTL" (FR-INV-03).
func Packs(qty decimal.Decimal, factor *string, packUOM *string, baseUOM string) string {
	if factor == nil || packUOM == nil || !dec(*factor).GreaterThan(decimal.NewFromInt(1)) {
		return qty.String() + " " + baseUOM
	}
	f := dec(*factor)
	sign := ""
	if qty.IsNegative() {
		sign, qty = "-", qty.Neg()
	}
	n := qty.Div(f).Floor()
	rest := qty.Sub(n.Mul(f))
	switch {
	case n.IsZero():
		return sign + rest.String() + " " + baseUOM
	case rest.IsZero():
		return sign + n.String() + " " + *packUOM
	}
	return sign + n.String() + " " + *packUOM + " + " + rest.String() + " " + baseUOM
}

// StockBalanceRow is the stock of an item in a warehouse (or one batch).
type StockBalanceRow struct {
	WarehouseID    uuid.UUID  `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode  string     `json:"warehouseCode" db:"warehouse_code"`
	WarehouseName  string     `json:"warehouseName" db:"warehouse_name"`
	LocationType   string     `json:"locationType" db:"location_type"`
	ItemID         uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode       string     `json:"itemCode" db:"item_code"`
	ItemName       string     `json:"itemName" db:"item_name"`
	Barcode        *string    `json:"barcode" db:"barcode"`
	Category       *string    `json:"category" db:"category"`
	UOM            string     `json:"uom" db:"uom" doc:"Stock UOM"`
	PurchaseUOM    *string    `json:"purchaseUom" db:"purchase_uom"`
	PackFactor     *string    `json:"packFactor" db:"pack_factor" doc:"Stock units per purchase unit"`
	BatchID        *uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo        *string    `json:"batchNo" db:"batch_no"`
	ExpiryDate     *string    `json:"expiryDate" db:"expiry_date"`
	Quantity       string     `json:"quantity" db:"quantity"`
	PackQuantity   string     `json:"packQuantity" db:"-" doc:"Quantity in purchase units and the rest, e.g. 4 CTN + 4 BTL"`
	Value          string     `json:"value" db:"value"`
	UnitCost       string     `json:"unitCost" db:"unit_cost" doc:"Value ÷ quantity (moving average / FIFO layers)"`
	ReorderPoint   *string    `json:"reorderPoint" db:"reorder_point"`
	ParLevel       *string    `json:"parLevel" db:"par_level"`
	MinStock       *string    `json:"minStock" db:"min_stock"`
	BelowReorder   bool       `json:"belowReorder" db:"below_reorder"`
	Consignment    bool       `json:"consignment" db:"consignment"`
	LastMovementAt *time.Time `json:"lastMovementAt" db:"last_movement_at"`
}

// BalanceFilter selects stock balance rows.
type BalanceFilter struct {
	WarehouseID  *uuid.UUID
	ItemID       *uuid.UUID
	CategoryID   *uuid.UUID
	Q            string
	BelowReorder bool
	ByBatch      bool
	IncludeZero  bool
	Limit        int
}

// StockBalances lists the stock per warehouse × item (or × batch).
func StockBalances(ctx context.Context, q dbtx.Querier, property uuid.UUID, f BalanceFilter) ([]StockBalanceRow, error) {
	batchCols, batchGroup := `NULL::uuid AS batch_id, NULL::text AS batch_no, NULL::text AS expiry_date`, ``
	if f.ByBatch {
		batchCols = `b.batch_id, bt.batch_no, to_char(bt.expiry_date, 'YYYY-MM-DD') AS expiry_date`
		batchGroup = `, b.batch_id, bt.batch_no, bt.expiry_date`
	}
	if f.Limit <= 0 {
		f.Limit = 500
	}
	rows, err := handle.List[StockBalanceRow](q.Query(ctx, `WITH x AS (
		SELECT b.warehouse_id, b.item_id, `+batchCols+`, sum(b.quantity) AS quantity, sum(b.value) AS value, max(b.last_movement_at) AS last_movement_at
		FROM inventory.stock_balances b LEFT JOIN inventory.batches bt ON bt.id = b.batch_id
		WHERE b.property_id = $1 AND ($2::uuid IS NULL OR b.warehouse_id = $2) AND ($3::uuid IS NULL OR b.item_id = $3)
		GROUP BY b.warehouse_id, b.item_id`+batchGroup+`)
		SELECT x.warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name, w.location_type, x.item_id, i.code AS item_code, i.name AS item_name,
		i.barcode, coalesce(c.name, i.category) AS category, u.code AS uom, pu.code AS purchase_uom, trim_scale(`+packFactorSQL+`)::text AS pack_factor,
		x.batch_id, x.batch_no, x.expiry_date, trim_scale(x.quantity)::text AS quantity, trim_scale(round(x.value, 2))::text AS value,
		trim_scale(CASE WHEN x.quantity > 0 THEN round(x.value / x.quantity, 4) ELSE 0 END)::text AS unit_cost,
		trim_scale(rp.reorder_point)::text AS reorder_point, trim_scale(ps.par_level)::text AS par_level, trim_scale(ps.min_stock)::text AS min_stock,
		coalesce(x.quantity < coalesce(rp.reorder_point, nullif(ps.min_stock, 0)), false) AS below_reorder, i.consignment, x.last_movement_at
		FROM x JOIN inventory.items i ON i.id = x.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id LEFT JOIN inventory.uoms pu ON pu.id = i.purchase_uom_id
		JOIN inventory.warehouses w ON w.id = x.warehouse_id LEFT JOIN inventory.item_categories c ON c.id = i.category_id
		LEFT JOIN inventory.reorder_points rp ON rp.warehouse_id = x.warehouse_id AND rp.item_id = x.item_id
		LEFT JOIN inventory.par_stocks ps ON ps.warehouse_id = x.warehouse_id AND ps.item_id = x.item_id
		WHERE ($4::uuid IS NULL OR i.category_id IN (WITH RECURSIVE cats AS (SELECT id FROM inventory.item_categories WHERE id = $4
		  UNION ALL SELECT k.id FROM inventory.item_categories k JOIN cats ON k.parent_id = cats.id) SELECT id FROM cats))
		AND ($5 = '' OR i.code ILIKE '%' || $5 || '%' OR i.name ILIKE '%' || $5 || '%' OR i.barcode = $5)
		AND ($6 OR x.quantity <> 0) AND (NOT $7 OR coalesce(x.quantity < coalesce(rp.reorder_point, nullif(ps.min_stock, 0)), false))
		ORDER BY w.code, i.code, x.expiry_date NULLS LAST LIMIT $8`, property, f.WarehouseID, f.ItemID, f.CategoryID, strings.TrimSpace(f.Q), f.IncludeZero,
		f.BelowReorder, f.Limit))
	for i := range rows {
		rows[i].PackQuantity = Packs(dec(rows[i].Quantity), rows[i].PackFactor, rows[i].PurchaseUOM, rows[i].UOM)
	}
	return rows, err
}

// BarcodeItem is the item behind a scanned barcode with its stock.
type BarcodeLookup struct {
	Barcode    string            `json:"barcode"`
	ItemID     uuid.UUID         `json:"itemId"`
	ItemCode   string            `json:"itemCode"`
	ItemName   string            `json:"itemName"`
	UOMID      uuid.UUID         `json:"uomId" doc:"UOM of the barcode (a carton barcode counts cartons)"`
	UOM        string            `json:"uom"`
	BaseUOM    string            `json:"baseUom"`
	BaseFactor string            `json:"baseFactor" doc:"Stock units per scanned unit"`
	Tracking   []string          `json:"tracking" doc:"batch, serial, expiry"`
	Balances   []StockBalanceRow `json:"balances"`
}

// LookupBarcode resolves an item or carton barcode (`ops` scanning).
func LookupBarcode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (BarcodeLookup, error) {
	item, uom, err := lookupBarcode(ctx, q, property, code)
	if err != nil {
		return BarcodeLookup{}, err
	}
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return BarcodeLookup{}, err
	}
	it, err := loadItem(ctx, q, property, item, cfg.ValuationMethod)
	if err != nil {
		return BarcodeLookup{}, err
	}
	out := BarcodeLookup{Barcode: strings.TrimSpace(code), ItemID: it.ID, ItemCode: it.Code, ItemName: it.Name, UOMID: it.BaseUOM, UOM: it.UOMCode,
		BaseUOM: it.UOMCode, BaseFactor: "1", Tracking: []string{}}
	if uom != nil && *uom != it.BaseUOM {
		f, err := convert(ctx, q, &it.ID, decimal.NewFromInt(1), *uom, it.BaseUOM)
		if err != nil {
			return out, err
		}
		out.UOMID, out.BaseFactor = *uom, f.String()
		if err := q.QueryRow(ctx, `SELECT code FROM inventory.uoms WHERE id = $1`, *uom).Scan(&out.UOM); err != nil {
			return out, err
		}
	}
	var batch, serial, expiry bool
	if err := q.QueryRow(ctx, `SELECT track_batch, track_serial, track_expiry FROM inventory.items WHERE id = $1`, it.ID).Scan(&batch, &serial, &expiry); err != nil {
		return out, err
	}
	for k, on := range map[string]bool{"batch": batch, "serial": serial, "expiry": expiry} {
		if on {
			out.Tracking = append(out.Tracking, k)
		}
	}
	out.Balances, err = StockBalances(ctx, q, property, BalanceFilter{ItemID: &it.ID, ByBatch: batch || expiry})
	return out, err
}

// MovementFilter selects movements.
type MovementFilter struct {
	WarehouseID  *uuid.UUID
	ItemID       *uuid.UUID
	MovementType string
	SourceType   string
	SourceID     *uuid.UUID
	From, To     *time.Time
	Flagged      bool
	Limit        int
}

// ListMovements lists movement headers, newest first.
func ListMovements(ctx context.Context, q dbtx.Querier, property uuid.UUID, f MovementFilter) ([]StockMovement, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	return handle.List[StockMovement](q.Query(ctx, movementSelect+` WHERE m.property_id = $1 AND ($2::uuid IS NULL OR m.warehouse_id = $2)
		AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM inventory.stock_movement_lines l WHERE l.movement_id = m.id AND l.item_id = $3))
		AND ($4 = '' OR m.movement_type = $4) AND ($5 = '' OR m.source_type = $5) AND ($6::uuid IS NULL OR m.source_id = $6)
		AND ($7::date IS NULL OR m.business_date >= $7) AND ($8::date IS NULL OR m.business_date <= $8) AND (NOT $9 OR m.flagged)
		ORDER BY m.seq DESC LIMIT $10`, property, f.WarehouseID, f.ItemID, f.MovementType, f.SourceType, f.SourceID, f.From, f.To, f.Flagged, f.Limit))
}

// StockCardLine is one movement line of an item in a warehouse.
type StockCardLine struct {
	MovementID   uuid.UUID  `json:"movementId" db:"movement_id"`
	Number       string     `json:"number" db:"number"`
	BusinessDate string     `json:"businessDate" db:"business_date"`
	PostedAt     time.Time  `json:"postedAt" db:"posted_at"`
	MovementType string     `json:"movementType" db:"movement_type"`
	SourceType   string     `json:"sourceType" db:"source_type"`
	Reason       *string    `json:"reason" db:"reason"`
	BatchNo      *string    `json:"batchNo" db:"batch_no"`
	Quantity     string     `json:"quantity" db:"quantity"`
	UnitCost     string     `json:"unitCost" db:"unit_cost"`
	TotalCost    string     `json:"totalCost" db:"total_cost"`
	BalanceAfter string     `json:"balanceAfter" db:"balance_after"`
	ValueAfter   string     `json:"valueAfter" db:"value_after"`
	ReversalOf   *uuid.UUID `json:"reversalOf" db:"reversal_of"`
}

// StockCard is the ledger of one item in one warehouse: the balance equals
// the sum of the movements (FR-STK-01 AC).
type StockCard struct {
	ItemID          uuid.UUID       `json:"itemId"`
	ItemCode        string          `json:"itemCode"`
	ItemName        string          `json:"itemName"`
	UOM             string          `json:"uom"`
	WarehouseID     uuid.UUID       `json:"warehouseId"`
	WarehouseCode   string          `json:"warehouseCode"`
	From            string          `json:"from"`
	To              string          `json:"to"`
	OpeningQuantity string          `json:"openingQuantity"`
	OpeningValue    string          `json:"openingValue"`
	InQuantity      string          `json:"inQuantity"`
	OutQuantity     string          `json:"outQuantity"`
	ClosingQuantity string          `json:"closingQuantity"`
	ClosingValue    string          `json:"closingValue"`
	OnHand          string          `json:"onHand" doc:"Current balance (stock_balances)"`
	Lines           []StockCardLine `json:"lines"`
}

// GetStockCard returns the stock card of an item in a warehouse.
func GetStockCard(ctx context.Context, q dbtx.Querier, property, item, wh uuid.UUID, from, to time.Time) (StockCard, error) {
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return StockCard{}, err
	}
	it, err := loadItem(ctx, q, property, item, cfg.ValuationMethod)
	if err != nil {
		return StockCard{}, err
	}
	w, err := loadWarehouse(ctx, q, property, wh)
	if err != nil {
		return StockCard{}, err
	}
	c := StockCard{ItemID: it.ID, ItemCode: it.Code, ItemName: it.Name, UOM: it.UOMCode, WarehouseID: w.ID, WarehouseCode: w.Code,
		From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(l.quantity) FILTER (WHERE m.business_date < $3), 0))::text,
		trim_scale(coalesce(sum(l.total_cost) FILTER (WHERE m.business_date < $3), 0))::text,
		trim_scale(coalesce(sum(l.quantity) FILTER (WHERE m.business_date BETWEEN $3 AND $4 AND l.quantity > 0), 0))::text,
		trim_scale(coalesce(-sum(l.quantity) FILTER (WHERE m.business_date BETWEEN $3 AND $4 AND l.quantity < 0), 0))::text,
		trim_scale(coalesce(sum(l.quantity) FILTER (WHERE m.business_date <= $4), 0))::text,
		trim_scale(coalesce(sum(l.total_cost) FILTER (WHERE m.business_date <= $4), 0))::text,
		trim_scale((SELECT coalesce(sum(quantity), 0) FROM inventory.stock_balances WHERE warehouse_id = $2 AND item_id = $1))::text
		FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id WHERE l.item_id = $1 AND m.warehouse_id = $2`,
		item, wh, from, to).Scan(&c.OpeningQuantity, &c.OpeningValue, &c.InQuantity, &c.OutQuantity, &c.ClosingQuantity, &c.ClosingValue, &c.OnHand); err != nil {
		return c, err
	}
	c.Lines, err = handle.List[StockCardLine](q.Query(ctx, `SELECT m.id AS movement_id, m.number, to_char(m.business_date, 'YYYY-MM-DD') AS business_date,
		m.posted_at, m.movement_type, m.source_type, m.reason, b.batch_no, trim_scale(l.quantity)::text AS quantity, trim_scale(l.unit_cost)::text AS unit_cost,
		trim_scale(l.total_cost)::text AS total_cost, trim_scale(l.balance_after)::text AS balance_after, trim_scale(l.value_after)::text AS value_after,
		m.reversal_of FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id
		LEFT JOIN inventory.batches b ON b.id = l.batch_id
		WHERE l.item_id = $1 AND m.warehouse_id = $2 AND m.business_date BETWEEN $3 AND $4 ORDER BY m.business_date, l.seq LIMIT 2000`, item, wh, from, to))
	return c, err
}

// FefoPickLine is a FEFO pick suggestion (FR-STK-07).
type FefoPickLine struct {
	BatchID    uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo    string    `json:"batchNo" db:"batch_no"`
	ExpiryDate *string   `json:"expiryDate" db:"expiry_date"`
	Available  string    `json:"available" db:"available"`
	Pick       string    `json:"pick" db:"-"`
}

// PickSuggestion picks batches first-expired-first-out for a quantity.
func PickSuggestion(ctx context.Context, q dbtx.Querier, wh, item uuid.UUID, need decimal.Decimal) ([]FefoPickLine, error) {
	rows, err := handle.List[FefoPickLine](q.Query(ctx, `SELECT b.batch_id, bt.batch_no, to_char(bt.expiry_date, 'YYYY-MM-DD') AS expiry_date,
		trim_scale(b.quantity)::text AS available FROM inventory.stock_balances b JOIN inventory.batches bt ON bt.id = b.batch_id
		WHERE b.warehouse_id = $1 AND b.item_id = $2 AND b.quantity > 0 ORDER BY bt.expiry_date NULLS LAST, bt.received_at, bt.batch_no`, wh, item))
	if err != nil {
		return nil, err
	}
	out := []FefoPickLine{}
	for _, r := range rows {
		if !need.IsPositive() {
			break
		}
		take := decimal.Min(dec(r.Available), need)
		need = need.Sub(take)
		r.Pick = take.String()
		out = append(out, r)
	}
	return out, nil
}

// StockValuationRow is the stock value of a group as of a date.
type StockValuationRow struct {
	Key      uuid.UUID `json:"key" db:"key"`
	Code     string    `json:"code" db:"code"`
	Name     string    `json:"name" db:"name"`
	Group    *string   `json:"group" db:"grp" doc:"Category (item rows) or location type (warehouse rows)"`
	UOM      *string   `json:"uom" db:"uom"`
	Quantity *string   `json:"quantity" db:"quantity"`
	Value    string    `json:"value" db:"value"`
	UnitCost *string   `json:"unitCost" db:"unit_cost"`
}

// StockValuation is the Stock Valuation as of a date (FR-VAL-05): the sum of
// the valued movement lines up to the date, the basis of the inventory
// account in the GL (consignment stock has no value).
type StockValuation struct {
	AsOf    string              `json:"asOf"`
	GroupBy string              `json:"groupBy" enum:"item,category,warehouse"`
	Total   string              `json:"total"`
	Rows    []StockValuationRow `json:"rows"`
}

// StockValuation values the stock as of a date grouped by item, category or warehouse.
func ValuateStock(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time, groupBy string, wh *uuid.UUID) (StockValuation, error) {
	out := StockValuation{AsOf: asOf.Format("2006-01-02"), GroupBy: groupBy, Rows: []StockValuationRow{}}
	var sql string
	switch groupBy {
	case "warehouse":
		sql = `SELECT w.id AS key, w.code, w.name, w.location_type AS grp, NULL::text AS uom, NULL::text AS quantity,
			trim_scale(round(sum(l.total_cost), 2))::text AS value, NULL::text AS unit_cost
			FROM v l JOIN inventory.warehouses w ON w.id = l.warehouse_id GROUP BY w.id, w.code, w.name, w.location_type
			HAVING sum(l.quantity) <> 0 OR sum(l.total_cost) <> 0 ORDER BY w.code`
	case "category":
		sql = `SELECT coalesce(c.id, '00000000-0000-0000-0000-000000000000'::uuid) AS key, coalesce(c.code, '-') AS code,
			coalesce(c.name, 'Uncategorised') AS name, NULL::text AS grp, NULL::text AS uom, NULL::text AS quantity,
			trim_scale(round(sum(l.total_cost), 2))::text AS value, NULL::text AS unit_cost
			FROM v l JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.item_categories c ON c.id = i.category_id
			GROUP BY 1, 2, 3 HAVING sum(l.quantity) <> 0 OR sum(l.total_cost) <> 0 ORDER BY 2`
	default:
		out.GroupBy = "item"
		sql = `SELECT i.id AS key, i.code, i.name, coalesce(c.name, i.category) AS grp, u.code AS uom, trim_scale(sum(l.quantity))::text AS quantity,
			trim_scale(round(sum(l.total_cost), 2))::text AS value,
			trim_scale(CASE WHEN sum(l.quantity) > 0 THEN round(sum(l.total_cost) / sum(l.quantity), 4) END)::text AS unit_cost
			FROM v l JOIN inventory.items i ON i.id = l.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
			LEFT JOIN inventory.item_categories c ON c.id = i.category_id
			GROUP BY i.id, i.code, i.name, c.name, i.category, u.code HAVING sum(l.quantity) <> 0 OR sum(l.total_cost) <> 0 ORDER BY i.code`
	}
	var err error
	out.Rows, err = handle.List[StockValuationRow](q.Query(ctx, `WITH v AS (SELECT m.warehouse_id, l.item_id, l.quantity, l.total_cost
		FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id
		WHERE m.property_id = $1 AND m.business_date <= $2 AND NOT l.consignment AND ($3::uuid IS NULL OR m.warehouse_id = $3)) `+sql, property, asOf, wh))
	if err != nil {
		return out, err
	}
	err = q.QueryRow(ctx, `SELECT trim_scale(round(coalesce(sum(l.total_cost), 0), 2))::text FROM inventory.stock_movement_lines l
		JOIN inventory.stock_movements m ON m.id = l.movement_id WHERE m.property_id = $1 AND m.business_date <= $2 AND NOT l.consignment
		AND ($3::uuid IS NULL OR m.warehouse_id = $3)`, property, asOf, wh).Scan(&out.Total)
	return out, err
}

// ConsumptionVarianceRow is Theoretical vs Actual consumption of an item in an outlet
// warehouse (FR-CNS-06).
type ConsumptionVarianceRow struct {
	WarehouseID        uuid.UUID  `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode      string     `json:"warehouseCode" db:"warehouse_code"`
	OutletID           *uuid.UUID `json:"outletId" db:"outlet_id"`
	ItemID             uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode           string     `json:"itemCode" db:"item_code"`
	ItemName           string     `json:"itemName" db:"item_name"`
	UOM                string     `json:"uom" db:"uom"`
	Opening            string     `json:"opening" db:"opening"`
	Received           string     `json:"received" db:"received" doc:"Receipts, transfers in and production in"`
	TransferredOut     string     `json:"transferredOut" db:"transferred_out" doc:"Transfers out and returns to suppliers"`
	Closing            string     `json:"closing" db:"closing"`
	Actual             string     `json:"actual" db:"actual" doc:"Opening + received − transferred out − closing"`
	ActualCost         string     `json:"actualCost" db:"actual_cost"`
	Theoretical        string     `json:"theoretical" db:"theoretical" doc:"Recipe consumption of the sales (P2 ledger)"`
	TheoreticalCost    string     `json:"theoreticalCost" db:"theoretical_cost" doc:"At standard cost"`
	Variance           string     `json:"variance" db:"variance"`
	VarianceValue      string     `json:"varianceValue" db:"variance_value"`
	VariancePercentage *string    `json:"variancePercent" db:"variance_percent"`
}

// ConsumptionVariance compares theoretical and actual consumption per outlet
// warehouse and item for a period.
func ConsumptionVariance(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, wh *uuid.UUID) ([]ConsumptionVarianceRow, error) {
	loc := localNow(ctx, q).Location()
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	end := time.Date(to.Year(), to.Month(), to.Day()+1, 0, 0, 0, 0, loc)
	return handle.List[ConsumptionVarianceRow](q.Query(ctx, `WITH w AS (SELECT id, code, outlet_id FROM inventory.warehouses
		  WHERE property_id = $1 AND ($4::uuid IS NULL OR id = $4) AND ($4::uuid IS NOT NULL OR outlet_id IS NOT NULL)),
		a AS (SELECT m.warehouse_id, l.item_id,
		  coalesce(sum(l.quantity) FILTER (WHERE m.business_date < $2), 0) AS opening,
		  coalesce(sum(l.quantity) FILTER (WHERE m.business_date BETWEEN $2 AND $3 AND m.movement_type IN ('receipt', 'transfer_in', 'production_in')), 0) AS received,
		  coalesce(-sum(l.quantity) FILTER (WHERE m.business_date BETWEEN $2 AND $3 AND m.movement_type IN ('transfer_out', 'return_out')), 0) AS transferred_out,
		  coalesce(sum(l.quantity) FILTER (WHERE m.business_date <= $3), 0) AS closing,
		  coalesce(-sum(l.total_cost) FILTER (WHERE m.business_date BETWEEN $2 AND $3
		    AND m.movement_type NOT IN ('receipt', 'transfer_in', 'production_in', 'transfer_out', 'return_out')), 0) AS actual_cost
		  FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id JOIN w ON w.id = m.warehouse_id
		  WHERE m.business_date <= $3 GROUP BY 1, 2),
		t AS (SELECT w.id AS warehouse_id, c.item_id, sum(c.quantity) AS qty, sum(c.cost_amount) AS cost FROM inventory.consumption_ledger c
		  JOIN w ON w.outlet_id = c.outlet_id WHERE c.property_id = $1 AND c.occurred_at >= $5 AND c.occurred_at < $6 GROUP BY 1, 2),
		k AS (SELECT warehouse_id, item_id FROM a UNION SELECT warehouse_id, item_id FROM t)
		SELECT k.warehouse_id, w.code AS warehouse_code, w.outlet_id, k.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		trim_scale(coalesce(a.opening, 0))::text AS opening, trim_scale(coalesce(a.received, 0))::text AS received,
		trim_scale(coalesce(a.transferred_out, 0))::text AS transferred_out, trim_scale(coalesce(a.closing, 0))::text AS closing,
		trim_scale(coalesce(a.opening + a.received - a.transferred_out - a.closing, 0))::text AS actual,
		trim_scale(round(coalesce(a.actual_cost, 0), 2))::text AS actual_cost, trim_scale(round(coalesce(t.qty, 0), 6))::text AS theoretical,
		trim_scale(round(coalesce(t.cost, 0), 2))::text AS theoretical_cost,
		trim_scale(round(coalesce(a.opening + a.received - a.transferred_out - a.closing, 0) - coalesce(t.qty, 0), 6))::text AS variance,
		trim_scale(round((coalesce(a.opening + a.received - a.transferred_out - a.closing, 0) - coalesce(t.qty, 0)) *
		  coalesce(CASE WHEN coalesce(a.opening + a.received - a.transferred_out - a.closing, 0) <> 0
		    THEN a.actual_cost / (a.opening + a.received - a.transferred_out - a.closing) END, i.standard_cost), 2))::text AS variance_value,
		trim_scale(CASE WHEN coalesce(t.qty, 0) <> 0 THEN round((coalesce(a.opening + a.received - a.transferred_out - a.closing, 0) - t.qty) / t.qty * 100, 2)
		  END)::text AS variance_percent
		FROM k JOIN w ON w.id = k.warehouse_id JOIN inventory.items i ON i.id = k.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		LEFT JOIN a ON a.warehouse_id = k.warehouse_id AND a.item_id = k.item_id LEFT JOIN t ON t.warehouse_id = k.warehouse_id AND t.item_id = k.item_id
		WHERE NOT i.consignment ORDER BY w.code, i.code`, property, from, to, wh, start, end))
}

// SlowMovingRow is an item in stock without outbound movement for N days.
type SlowMovingRow struct {
	WarehouseID   uuid.UUID `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode string    `json:"warehouseCode" db:"warehouse_code"`
	ItemID        uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode      string    `json:"itemCode" db:"item_code"`
	ItemName      string    `json:"itemName" db:"item_name"`
	UOM           string    `json:"uom" db:"uom"`
	Quantity      string    `json:"quantity" db:"quantity"`
	Value         string    `json:"value" db:"value"`
	LastOutbound  *string   `json:"lastOutbound" db:"last_outbound"`
	DaysIdle      *int      `json:"daysIdle" db:"days_idle"`
}

// SlowMoving lists stock without an outbound movement within days (FR-RPL-04).
func SlowMoving(ctx context.Context, q dbtx.Querier, property uuid.UUID, days int, wh *uuid.UUID) ([]SlowMovingRow, error) {
	t := today(ctx, q)
	return handle.List[SlowMovingRow](q.Query(ctx, `WITH s AS (SELECT warehouse_id, item_id, sum(quantity) AS quantity, sum(value) AS value
		  FROM inventory.stock_balances WHERE property_id = $1 AND ($4::uuid IS NULL OR warehouse_id = $4) GROUP BY 1, 2 HAVING sum(quantity) > 0),
		o AS (SELECT m.warehouse_id, l.item_id, max(m.business_date) AS last_out FROM inventory.stock_movement_lines l
		  JOIN inventory.stock_movements m ON m.id = l.movement_id JOIN s ON s.warehouse_id = m.warehouse_id AND s.item_id = l.item_id
		  WHERE l.quantity < 0 AND m.movement_type <> 'opname' GROUP BY 1, 2),
		f AS (SELECT m.warehouse_id, l.item_id, min(m.business_date) AS first_in FROM inventory.stock_movement_lines l
		  JOIN inventory.stock_movements m ON m.id = l.movement_id JOIN s ON s.warehouse_id = m.warehouse_id AND s.item_id = l.item_id GROUP BY 1, 2)
		SELECT s.warehouse_id, w.code AS warehouse_code, s.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		trim_scale(s.quantity)::text AS quantity, trim_scale(round(s.value, 2))::text AS value, to_char(o.last_out, 'YYYY-MM-DD') AS last_outbound,
		($2::date - coalesce(o.last_out, f.first_in))::int AS days_idle
		FROM s JOIN inventory.warehouses w ON w.id = s.warehouse_id JOIN inventory.items i ON i.id = s.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		LEFT JOIN o ON o.warehouse_id = s.warehouse_id AND o.item_id = s.item_id LEFT JOIN f ON f.warehouse_id = s.warehouse_id AND f.item_id = s.item_id
		WHERE coalesce(o.last_out, f.first_in) <= $2::date - $3::int ORDER BY days_idle DESC, w.code, i.code`, property, t, days, wh))
}

// InventoryPostingException is an event line that could not be posted.
type InventoryPostingException struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	EventType   string     `json:"eventType" db:"event_type"`
	EventID     *uuid.UUID `json:"eventId" db:"event_id"`
	SourceType  string     `json:"sourceType" db:"source_type"`
	SourceID    *uuid.UUID `json:"sourceId" db:"source_id"`
	ItemID      *uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode    *string    `json:"itemCode" db:"item_code"`
	WarehouseID *uuid.UUID `json:"warehouseId" db:"warehouse_id"`
	Quantity    *string    `json:"quantity" db:"quantity"`
	Reason      string     `json:"reason" db:"reason"`
	Status      string     `json:"status" db:"status" enum:"open,resolved"`
	Resolution  *string    `json:"resolution" db:"resolution"`
	ResolvedAt  *time.Time `json:"resolvedAt" db:"resolved_at"`
	CreatedAt   time.Time  `json:"createdAt" db:"created_at"`
}

const exceptionSelect = `SELECT x.id, x.event_type, x.event_id, x.source_type, x.source_id, x.item_id, i.code AS item_code, x.warehouse_id,
	trim_scale(x.quantity)::text AS quantity, x.reason, x.status, x.resolution, x.resolved_at, x.created_at
	FROM inventory.posting_exceptions x LEFT JOIN inventory.items i ON i.id = x.item_id`

// ListExceptions lists posting exceptions (status optional).
func ListExceptions(ctx context.Context, q dbtx.Querier, property uuid.UUID, status string, limit int) ([]InventoryPostingException, error) {
	return handle.List[InventoryPostingException](q.Query(ctx, exceptionSelect+` WHERE x.property_id = $1 AND ($2 = '' OR x.status = $2)
		ORDER BY x.created_at DESC LIMIT $3`, property, status, limit))
}

// PostingExceptionResolveInput closes a posting exception.
type PostingExceptionResolveInput struct {
	Resolution string `json:"resolution" doc:"What was done (e.g. adjustment SAJ-…, outlet mapped)"`
}

// ResolveException marks a posting exception resolved.
func (s *Stock) ResolveException(ctx context.Context, tx dbtx.Querier, property, xid uuid.UUID, in PostingExceptionResolveInput) (InventoryPostingException, error) {
	if err := handle.Required("resolution", in.Resolution); err != nil {
		return InventoryPostingException{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE inventory.posting_exceptions SET status = 'resolved', resolved_at = now(), resolved_by = $3, resolution = $4
		WHERE id = $1 AND property_id = $2 AND status = 'open'`, xid, property, uuidOrNil(handle.UserID(ctx)), in.Resolution)
	if err != nil {
		return InventoryPostingException{}, err
	}
	if tag.RowsAffected() == 0 {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.posting_exceptions WHERE id = $1 AND property_id = $2)`, xid, property).Scan(&ok); err != nil {
			return InventoryPostingException{}, err
		}
		if !ok {
			return InventoryPostingException{}, errs.NotFound("posting exception")
		}
		return InventoryPostingException{}, errs.Conflict("already_resolved", "the posting exception is already resolved")
	}
	rows, err := tx.Query(ctx, exceptionSelect+` WHERE x.id = $1`, xid)
	return handle.One[InventoryPostingException](rows, err, "posting exception")
}

// ── document lists ────────────────────────────────────────────────────────

// DocFilter selects documents.
type DocFilter struct {
	Status      string
	Kind        string
	WarehouseID *uuid.UUID
	AssetID     *uuid.UUID
	Q           string
	From, To    *time.Time
	Limit       int
}

func (f DocFilter) limit() int {
	if f.Limit <= 0 {
		return 100
	}
	return f.Limit
}

// ListRequisitions lists requisitions (kind = request type).
func ListRequisitions(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]StoreRequisition, error) {
	return handle.List[StoreRequisition](q.Query(ctx, requisitionSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = ANY(string_to_array($2, ',')))
		AND ($3 = '' OR r.request_type = $3) AND ($4::uuid IS NULL OR r.source_warehouse_id = $4 OR r.requesting_warehouse_id = $4)
		AND ($5::uuid IS NULL OR r.asset_id = $5) AND ($6 = '' OR r.number ILIKE '%' || $6 || '%') ORDER BY r.created_at DESC LIMIT $7`,
		property, f.Status, f.Kind, f.WarehouseID, f.AssetID, f.Q, f.limit()))
}

// ListTransfers lists stock transfers.
func ListTransfers(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]StockTransfer, error) {
	return handle.List[StockTransfer](q.Query(ctx, transferSelect+` WHERE t.property_id = $1 AND ($2 = '' OR t.status = ANY(string_to_array($2, ',')))
		AND ($3::uuid IS NULL OR t.from_warehouse_id = $3 OR t.to_warehouse_id = $3) AND ($4 = '' OR t.number ILIKE '%' || $4 || '%')
		ORDER BY t.created_at DESC LIMIT $5`, property, f.Status, f.WarehouseID, f.Q, f.limit()))
}

// ListAdjustments lists stock adjustments.
func ListAdjustments(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]StockAdjustment, error) {
	return handle.List[StockAdjustment](q.Query(ctx, adjustmentSelect+` WHERE a.property_id = $1 AND ($2 = '' OR a.status = ANY(string_to_array($2, ',')))
		AND ($3::uuid IS NULL OR a.warehouse_id = $3) AND ($4 = '' OR a.reason = $4) ORDER BY a.created_at DESC LIMIT $5`,
		property, f.Status, f.WarehouseID, f.Kind, f.limit()))
}

// ListOpnames lists stock opnames.
func ListOpnames(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]StockOpname, error) {
	return handle.List[StockOpname](q.Query(ctx, opnameSelect+` WHERE o.property_id = $1 AND ($2 = '' OR o.status = ANY(string_to_array($2, ',')))
		AND ($3::uuid IS NULL OR o.warehouse_id = $3) ORDER BY o.created_at DESC LIMIT $4`, property, f.Status, f.WarehouseID, f.limit()))
}

// ListProduction lists production orders.
func ListProduction(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]ProductionOrder, error) {
	return handle.List[ProductionOrder](q.Query(ctx, productionSelect+` WHERE p.property_id = $1 AND ($2 = '' OR p.status = ANY(string_to_array($2, ',')))
		AND ($3::uuid IS NULL OR p.warehouse_id = $3) AND ($4::date IS NULL OR p.scheduled_for >= $4) AND ($5::date IS NULL OR p.scheduled_for <= $5)
		ORDER BY p.scheduled_for NULLS LAST, p.created_at DESC LIMIT $6`, property, f.Status, f.WarehouseID, f.From, f.To, f.limit()))
}

// ListWaste lists waste records.
func ListWaste(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]WasteRecord, error) {
	return handle.List[WasteRecord](q.Query(ctx, wasteSelect+` WHERE x.property_id = $1 AND ($2::uuid IS NULL OR x.warehouse_id = $2) AND ($3 = '' OR x.reason = $3)
		AND ($4::date IS NULL OR x.business_date >= $4) AND ($5::date IS NULL OR x.business_date <= $5) ORDER BY x.created_at DESC LIMIT $6`,
		property, f.WarehouseID, f.Kind, f.From, f.To, f.limit()))
}

// ListMaintenance lists maintenance work orders.
func ListMaintenance(ctx context.Context, q dbtx.Querier, property uuid.UUID, f DocFilter) ([]AssetMaintenanceRecord, error) {
	return handle.List[AssetMaintenanceRecord](q.Query(ctx, maintenanceSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = ANY(string_to_array($2, ',')))
		AND ($3::uuid IS NULL OR r.asset_id = $3) ORDER BY r.created_at DESC LIMIT $4`, property, f.Status, f.AssetID, f.limit()))
}

// ListDepreciationRuns lists depreciation runs.
func ListDepreciationRuns(ctx context.Context, q dbtx.Querier, property uuid.UUID, limit int) ([]AssetDepreciationRun, error) {
	return handle.List[AssetDepreciationRun](q.Query(ctx, `SELECT id, number, period, currency, trim_scale(total)::text AS total, assets, status, posted_at
		FROM inventory.depreciation_runs WHERE property_id = $1 ORDER BY period DESC LIMIT $2`, property, limit))
}

// ListSettlements lists issued consignment settlements.
func ListSettlements(ctx context.Context, q dbtx.Querier, property uuid.UUID, limit int) ([]ConsignmentSettlement, error) {
	return handle.List[ConsignmentSettlement](q.Query(ctx, settlementSelect+` WHERE s.property_id = $1 ORDER BY s.period DESC, sp.name LIMIT $2`, property, limit))
}

// inProperty checks that a row belongs to the property.
func inProperty(ctx context.Context, q dbtx.Querier, table string, rid, property uuid.UUID, what string) error {
	var ok bool
	if err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND property_id = $2)`, table), rid, property).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.NotFound(what)
	}
	return nil
}
