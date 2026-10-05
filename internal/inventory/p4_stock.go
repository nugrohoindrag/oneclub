package inventory

// PRD P4 EP-02 Stock Movement & Balance and EP-05 Stock Valuation: the
// append-only stock ledger. Every stock change of every document and of
// automatic consumption goes through Stock.Post, which
//
//   - is idempotent per source (source type, source id, movement type,
//     warehouse — FR-CNS-05),
//   - rejects movements dated in a Closed accounting period (K10, FR-STK-05)
//     and manual movements of a warehouse frozen by a stock opname (FR-OPN-04),
//   - values every line with the valuation method of the item (moving
//     average or FIFO cost layers, FR-VAL-01/02),
//   - picks batches first-expired-first-out and serial numbers (FR-STK-07,
//     FR-PRD-03/04), refuses negative stock unless allowed (FR-STK-04),
//   - updates the balance per warehouse × item × batch (= Σ movement lines)
//   - and publishes inventory.movement_posted with costs for the inventory
//     journal (FR-STK-06, FR-VAL-04).

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/resource"
)

// Events published by inventory (docs/p3-p4-contracts.md).
const (
	EventMovementPosted   = "inventory.movement_posted"
	EventReorderNeeded    = "inventory.reorder_needed"
	EventAssetDepreciated = "inventory.asset_depreciated"
	EventConsignmentSold  = "inventory.consignment_sold"
	// EventAssetDisposed is additive (not yet in the contracts page).
	EventAssetDisposed = "inventory.asset_disposed"
	// PRD P4 §11 outbox events (additive, docs/p3-p4-contracts.md).
	EventOpnamePosted        = "inventory.opname_posted"
	EventProductionCompleted = "inventory.production_completed"
	EventStockLow            = "inventory.stock_low"
	EventMaintenanceDue      = "inventory.asset_maintenance_due"
)

// Movement types (contract inventory.movement_posted).
const (
	MoveReceipt       = "receipt"
	MoveIssue         = "issue"
	MoveTransferOut   = "transfer_out"
	MoveTransferIn    = "transfer_in"
	MoveAdjustment    = "adjustment"
	MoveOpname        = "opname"
	MoveWaste         = "waste"
	MoveProductionIn  = "production_in"
	MoveProductionOut = "production_out"
	MoveConsumption   = "consumption"
	MoveReturnOut     = "return_out"
)

// Publisher publishes domain events through the outbox (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Stock is the PRD P4 inventory service (stock ledger, documents,
// automatic consumption, replenishment, assets). It extends the P2 module.
type Stock struct {
	*Module
	Events    Publisher
	Approvals *approval.Engine
	Notify    notify.Sender
	Engine    *resource.Engine
	period    atomic.Pointer[PeriodStatusFunc]
}

// PostLine is one stock change in the item's base UOM.
type PostLine struct {
	ItemID          uuid.UUID
	Quantity        decimal.Decimal // signed: in > 0, out < 0
	UnitCost        *decimal.Decimal
	BatchID         *uuid.UUID
	BatchNo         string
	ExpiryDate      *time.Time
	SerialNos       []string
	StockLocationID *uuid.UUID
	SalesAmount     *decimal.Decimal // net sales of a consignment item line
}

// PostInput is one movement of one warehouse.
type PostInput struct {
	Type               string
	SourceType         string
	SourceID           *uuid.UUID
	WarehouseID        uuid.UUID
	CounterWarehouseID *uuid.UUID
	CostCenter         string
	Reason             string
	Notes              string
	OutletID           *uuid.UUID
	DepartmentID       *uuid.UUID
	AssetID            *uuid.UUID
	SupplierID         *uuid.UUID
	BusinessDate       time.Time // zero: today (instance timezone)
	// Auto marks automatic postings (events): they are never refused for
	// stock, freeze or period reasons; negative stock is flagged for review.
	Auto bool
	// AllowNegative lets this movement take the warehouse below zero.
	AllowNegative bool
	// IgnoreFreeze lets the stock opname post into its frozen warehouse.
	IgnoreFreeze bool
	// ExplicitCost values outbound lines at their UnitCost (transit,
	// purchase returns, reversals) instead of the valuation method.
	ExplicitCost bool
	ReversalOf   *uuid.UUID
	Lines        []PostLine
}

// StockMovement is a posted stock movement.
type StockMovement struct {
	ID                 uuid.UUID           `json:"id" db:"id"`
	Number             string              `json:"number" db:"number"`
	MovementType       string              `json:"movementType" db:"movement_type" enum:"receipt,issue,transfer_out,transfer_in,adjustment,opname,waste,production_in,production_out,consumption,return_out"`
	BusinessDate       string              `json:"businessDate" db:"business_date"`
	WarehouseID        uuid.UUID           `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode      string              `json:"warehouseCode" db:"warehouse_code"`
	WarehouseName      string              `json:"warehouseName" db:"warehouse_name"`
	CounterWarehouseID *uuid.UUID          `json:"counterWarehouseId" db:"counter_warehouse_id"`
	CostCenter         *string             `json:"costCenter" db:"cost_center"`
	OutletID           *uuid.UUID          `json:"outletId" db:"outlet_id"`
	DepartmentID       *uuid.UUID          `json:"departmentId" db:"department_id"`
	AssetID            *uuid.UUID          `json:"assetId" db:"asset_id"`
	SupplierID         *uuid.UUID          `json:"supplierId" db:"supplier_id"`
	SourceType         string              `json:"sourceType" db:"source_type"`
	SourceID           *uuid.UUID          `json:"sourceId" db:"source_id"`
	ReversalOf         *uuid.UUID          `json:"reversalOf" db:"reversal_of"`
	ReversedBy         *uuid.UUID          `json:"reversedBy" db:"reversed_by"`
	Reason             *string             `json:"reason" db:"reason"`
	Notes              *string             `json:"notes" db:"notes"`
	Currency           string              `json:"currency" db:"currency"`
	TotalCost          string              `json:"totalCost" db:"total_cost"`
	Flagged            bool                `json:"flagged" db:"flagged" doc:"Negative stock posted by automatic consumption, to be reviewed"`
	PostedAt           time.Time           `json:"postedAt" db:"posted_at"`
	PostedBy           *uuid.UUID          `json:"postedBy" db:"posted_by"`
	Lines              []StockMovementLine `json:"lines,omitempty" db:"-"`
}

// StockMovementLine is one line of a movement.
type StockMovementLine struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	LineNo       int        `json:"lineNo" db:"line_no"`
	ItemID       uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode     string     `json:"itemCode" db:"item_code"`
	ItemName     string     `json:"itemName" db:"item_name"`
	UOM          string     `json:"uom" db:"uom"`
	BatchID      *uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo      *string    `json:"batchNo" db:"batch_no"`
	ExpiryDate   *string    `json:"expiryDate" db:"expiry_date"`
	SerialNos    []string   `json:"serialNos" db:"serial_nos"`
	Quantity     string     `json:"quantity" db:"quantity"`
	UnitCost     string     `json:"unitCost" db:"unit_cost"`
	TotalCost    string     `json:"totalCost" db:"total_cost"`
	Consignment  bool       `json:"consignment" db:"consignment"`
	BalanceAfter string     `json:"balanceAfter" db:"balance_after"`
	ValueAfter   string     `json:"valueAfter" db:"value_after"`
	Negative     bool       `json:"negative" db:"negative"`
}

const movementSelect = `SELECT m.id, m.number, m.movement_type, to_char(m.business_date, 'YYYY-MM-DD') AS business_date, m.warehouse_id, w.code AS warehouse_code,
	w.name AS warehouse_name, m.counter_warehouse_id, m.cost_center, m.outlet_id, m.department_id, m.asset_id, m.supplier_id, m.source_type, m.source_id,
	m.reversal_of, (SELECT r.id FROM inventory.stock_movements r WHERE r.reversal_of = m.id) AS reversed_by, m.reason, m.notes, m.currency,
	trim_scale(m.total_cost)::text AS total_cost, m.flagged, m.posted_at, m.posted_by
	FROM inventory.stock_movements m JOIN inventory.warehouses w ON w.id = m.warehouse_id`

const movementLineSelect = `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom, l.batch_id, b.batch_no,
	to_char(b.expiry_date, 'YYYY-MM-DD') AS expiry_date, coalesce(l.serial_nos, '{}') AS serial_nos, trim_scale(l.quantity)::text AS quantity,
	trim_scale(l.unit_cost)::text AS unit_cost, trim_scale(l.total_cost)::text AS total_cost, l.consignment, trim_scale(l.balance_after)::text AS balance_after,
	trim_scale(l.value_after)::text AS value_after, l.negative
	FROM inventory.stock_movement_lines l JOIN inventory.items i ON i.id = l.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
	LEFT JOIN inventory.batches b ON b.id = l.batch_id`

// GetMovement returns a movement with its lines.
func GetMovement(ctx context.Context, q dbtx.Querier, mid uuid.UUID) (StockMovement, error) {
	rows, err := q.Query(ctx, movementSelect+` WHERE m.id = $1`, mid)
	m, err := handle.One[StockMovement](rows, err, "stock movement")
	if err != nil {
		return m, err
	}
	m.Lines, err = handle.List[StockMovementLine](q.Query(ctx, movementLineSelect+` WHERE l.movement_id = $1 ORDER BY l.line_no`, mid))
	return m, err
}

// ── reference data ────────────────────────────────────────────────────────

type whInfo struct {
	ID            uuid.UUID
	Code          string
	Name          string
	Type          string
	AllowNegative bool
	CostCenter    *string
	Active        bool
	ParentID      *uuid.UUID
}

func loadWarehouse(ctx context.Context, q dbtx.Querier, property, wid uuid.UUID) (whInfo, error) {
	var w whInfo
	err := q.QueryRow(ctx, `SELECT id, code, name, location_type, allow_negative, cost_center, status = 'active' AND archived_at IS NULL, parent_id
		FROM inventory.warehouses WHERE id = $1 AND property_id = $2`, wid, property).
		Scan(&w.ID, &w.Code, &w.Name, &w.Type, &w.AllowNegative, &w.CostCenter, &w.Active, &w.ParentID)
	if dbtx.IsNoRows(err) {
		return w, errs.Validation("warehouse_not_found", "warehouse not found in this property", errs.Field("warehouseId", "not_found", "warehouse not found"))
	}
	return w, err
}

// warehouseByCode returns the active warehouse with a code, or nil.
func warehouseByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*uuid.UUID, error) {
	if strings.TrimSpace(code) == "" {
		return nil, nil
	}
	var wid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM inventory.warehouses WHERE property_id = $1 AND upper(code) = upper($2) AND status = 'active' AND archived_at IS NULL`,
		property, code).Scan(&wid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wid, nil
}

type itemInfo struct {
	ID              uuid.UUID
	Code            string
	Name            string
	Type            string
	BaseUOM         uuid.UUID
	UOMCode         string
	StockValuation  string
	Batched         bool
	Serial          bool
	ShelfLife       *int
	Consignment     bool
	ConsignSupplier *uuid.UUID
	Commission      decimal.Decimal
	StandardCost    decimal.Decimal
	Active          bool
	PurchaseUOM     *uuid.UUID
}

func (it itemInfo) stocked() bool { return it.Type != "service" && it.Type != "non_stock" }

func loadItem(ctx context.Context, q dbtx.Querier, property, itemID uuid.UUID, defValuation string) (itemInfo, error) {
	var it itemInfo
	var comm, std string
	err := q.QueryRow(ctx, `SELECT i.id, i.code, i.name, i.item_type, i.base_uom_id, u.code, coalesce(i.valuation_method, c.valuation_method, $3),
		i.track_batch OR i.track_expiry, i.track_serial, i.shelf_life_days, i.consignment, i.consignment_supplier_id,
		coalesce(i.consignment_commission_percent, 0)::text, i.standard_cost::text, i.status = 'active' AND i.archived_at IS NULL, i.purchase_uom_id
		FROM inventory.items i JOIN inventory.uoms u ON u.id = i.base_uom_id LEFT JOIN inventory.item_categories c ON c.id = i.category_id
		WHERE i.id = $1 AND i.property_id = $2`, itemID, property, defValuation).
		Scan(&it.ID, &it.Code, &it.Name, &it.Type, &it.BaseUOM, &it.UOMCode, &it.StockValuation, &it.Batched, &it.Serial, &it.ShelfLife, &it.Consignment,
			&it.ConsignSupplier, &comm, &std, &it.Active, &it.PurchaseUOM)
	if dbtx.IsNoRows(err) {
		return it, errs.Validation("item_not_found", "item not found in this property", errs.Field("itemId", "not_found", itemID.String()))
	}
	it.Commission, it.StandardCost = dec(comm), dec(std)
	return it, err
}

// toBase converts a quantity entered in uom into the item's base UOM.
func toBase(ctx context.Context, q dbtx.Querier, it itemInfo, qty decimal.Decimal, uom *uuid.UUID) (decimal.Decimal, error) {
	if uom == nil || *uom == it.BaseUOM {
		return qty, nil
	}
	return convert(ctx, q, &it.ID, qty, *uom, it.BaseUOM)
}

// ── dates & periods ───────────────────────────────────────────────────────

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// today returns the business date in the instance timezone.
func today(ctx context.Context, q dbtx.Querier) time.Time {
	return dateOnly(time.Now().In(calendar.Location(ctx, q)))
}

func localNow(ctx context.Context, q dbtx.Querier) time.Time {
	return time.Now().In(calendar.Location(ctx, q))
}

// PeriodStatusFunc returns the accounting period status of a date (K10:
// open, soft_closed or closed); accounting.PeriodStatus has this shape.
type PeriodStatusFunc func(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (string, error)

// SetPeriodGuard registers the period status source (internal/app wires
// Accounting; nil = every period is open).
func (s *Stock) SetPeriodGuard(f PeriodStatusFunc) {
	if f == nil {
		s.period.Store(nil)
		return
	}
	s.period.Store(&f)
}

// periodStatus returns the accounting period status of a date (K10).
func (s *Stock) periodStatus(ctx context.Context, q dbtx.Querier, property uuid.UUID, d time.Time) (string, error) {
	f := s.period.Load()
	if f == nil || *f == nil {
		return "open", nil
	}
	st, err := (*f)(ctx, q, property, d)
	if st == "" {
		st = "open"
	}
	return st, err
}

// postingDate resolves the business date of a movement: today when empty;
// a date in a Closed period is refused for documents and moved to today for
// automatic postings (late POS events, FR-CNS-05).
func (s *Stock) postingDate(ctx context.Context, q dbtx.Querier, property uuid.UUID, d time.Time, auto bool) (time.Time, error) {
	t := today(ctx, q)
	if d.IsZero() {
		d = t
	}
	d = dateOnly(d)
	st, err := s.periodStatus(ctx, q, property, d)
	if err != nil {
		return d, err
	}
	if st == "closed" {
		if auto {
			return t, nil // posted on the current business day (never lost)
		}
		return d, errs.Validation("period_closed", fmt.Sprintf("the accounting period %s is closed", d.Format("2006-01")),
			errs.Field("businessDate", "period_closed", "the accounting period is closed"))
	}
	return d, nil
}

// ── locking ───────────────────────────────────────────────────────────────

type stockKey struct{ wh, item uuid.UUID }

// lockStock serialises postings per warehouse × item (moving average and
// FIFO layers stay consistent under concurrency). Keys are locked in a
// fixed order; advisory transaction locks are re-entrant.
func lockStock(ctx context.Context, tx pgx.Tx, keys []stockKey) error {
	ks := make([]string, 0, len(keys))
	for _, k := range keys {
		ks = append(ks, k.wh.String()+"/"+k.item.String())
	}
	sort.Strings(ks)
	ks = slices.Compact(ks)
	for _, k := range ks {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7041))`, k); err != nil {
			return err
		}
	}
	return nil
}

// ── posting ───────────────────────────────────────────────────────────────

type itemState struct {
	qty, value decimal.Decimal
	lastCost   *decimal.Decimal
}

type layer struct {
	id        uuid.UUID
	remaining decimal.Decimal
	cost      decimal.Decimal
	changed   bool
}

type piece struct {
	item       itemInfo
	qty        decimal.Decimal
	unitCost   decimal.Decimal
	total      decimal.Decimal
	batchID    *uuid.UUID
	serials    []string
	location   *uuid.UUID
	negative   bool
	sales      *decimal.Decimal
	newLayer   decimal.Decimal // FIFO: remaining of the layer created by an inbound piece
	balAfter   decimal.Decimal
	valueAfter decimal.Decimal
	lineID     uuid.UUID
}

type poster struct {
	s        *Stock
	tx       pgx.Tx
	property uuid.UUID
	in       PostInput
	wh       whInfo
	cfg      Configuration
	pol      Policy
	items    map[uuid.UUID]itemInfo
	states   map[uuid.UUID]*itemState
	batches  map[uuid.UUID]map[uuid.UUID]decimal.Decimal // item → batch → qty
	layers   map[string][]*layer                         // item/batch → open FIFO layers
	pieces   []*piece
	flagged  bool
	allowNeg bool
}

// Post posts one movement (idempotent per source) and returns it; created is
// false when the source had already been posted.
func (s *Stock) Post(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PostInput) (StockMovement, bool, error) {
	if len(in.Lines) == 0 {
		return StockMovement{}, false, errs.Validation("no_lines", "a movement needs at least one line")
	}
	if in.SourceID != nil && in.ReversalOf == nil {
		var existing uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM inventory.stock_movements WHERE property_id = $1 AND source_type = $2 AND source_id = $3
			AND movement_type = $4 AND warehouse_id = $5 AND reversal_of IS NULL`, property, in.SourceType, *in.SourceID, in.Type, in.WarehouseID).Scan(&existing)
		if err == nil {
			m, err := GetMovement(ctx, tx, existing)
			return m, false, err
		}
		if !dbtx.IsNoRows(err) {
			return StockMovement{}, false, err
		}
	}
	wh, err := loadWarehouse(ctx, tx, property, in.WarehouseID)
	if err != nil {
		return StockMovement{}, false, err
	}
	if !wh.Active && !in.Auto {
		return StockMovement{}, false, errs.Validation("warehouse_inactive", "warehouse "+wh.Code+" is inactive")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return StockMovement{}, false, err
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return StockMovement{}, false, err
	}
	bd, err := s.postingDate(ctx, tx, property, in.BusinessDate, in.Auto)
	if err != nil {
		return StockMovement{}, false, err
	}
	in.BusinessDate = bd
	if !in.Auto && !in.IgnoreFreeze {
		var n string
		err := tx.QueryRow(ctx, `SELECT number FROM inventory.stock_opnames WHERE warehouse_id = $1 AND freeze_movements
			AND status IN ('in_progress', 'counted', 'pending_approval') LIMIT 1`, wh.ID).Scan(&n)
		if err == nil {
			return StockMovement{}, false, errs.Conflict("warehouse_frozen", "stock opname "+n+" freezes movements of "+wh.Code)
		}
		if !dbtx.IsNoRows(err) {
			return StockMovement{}, false, err
		}
	}
	p := &poster{s: s, tx: tx, property: property, in: in, wh: wh, cfg: cfg, pol: pol, items: map[uuid.UUID]itemInfo{},
		states: map[uuid.UUID]*itemState{}, batches: map[uuid.UUID]map[uuid.UUID]decimal.Decimal{}, layers: map[string][]*layer{}}
	p.allowNeg = in.AllowNegative || wh.AllowNegative || pol.AllowNegativeStock || slices.ContainsFunc(pol.NegativeStockWarehouses, func(c string) bool {
		return strings.EqualFold(c, wh.Code)
	})
	keys := make([]stockKey, 0, len(in.Lines))
	for _, l := range in.Lines {
		keys = append(keys, stockKey{wh.ID, l.ItemID})
	}
	if err := lockStock(ctx, tx, keys); err != nil {
		return StockMovement{}, false, err
	}
	for i, l := range in.Lines {
		if l.Quantity.IsZero() {
			return StockMovement{}, false, errs.Validation("zero_quantity", fmt.Sprintf("line %d has no quantity", i+1),
				errs.Field(fmt.Sprintf("lines[%d].quantity", i), "zero", "must not be zero"))
		}
		if err := p.line(ctx, i, l); err != nil {
			return StockMovement{}, false, err
		}
	}
	if len(p.pieces) == 0 {
		return StockMovement{}, false, errs.Validation("no_stock_lines", "no stock-tracked item in the movement")
	}
	return p.write(ctx)
}

func (p *poster) item(ctx context.Context, itemID uuid.UUID) (itemInfo, error) {
	if it, ok := p.items[itemID]; ok {
		return it, nil
	}
	it, err := loadItem(ctx, p.tx, p.property, itemID, p.cfg.ValuationMethod)
	if err != nil {
		return it, err
	}
	p.items[itemID] = it
	return it, nil
}

func (p *poster) state(ctx context.Context, it itemInfo) (*itemState, error) {
	if st, ok := p.states[it.ID]; ok {
		return st, nil
	}
	var q, v string
	if err := p.tx.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text, coalesce(sum(value), 0)::text FROM inventory.stock_balances
		WHERE warehouse_id = $1 AND item_id = $2`, p.wh.ID, it.ID).Scan(&q, &v); err != nil {
		return nil, err
	}
	st := &itemState{qty: dec(q), value: dec(v)}
	p.states[it.ID] = st
	return st, nil
}

// lastCost is the latest unit cost of the item in the warehouse, else in any
// warehouse, else its standard cost.
func (p *poster) lastCost(ctx context.Context, it itemInfo, st *itemState) (decimal.Decimal, error) {
	if st.lastCost != nil {
		return *st.lastCost, nil
	}
	var c *string
	err := p.tx.QueryRow(ctx, `SELECT l.unit_cost::text FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id
		WHERE l.item_id = $1 AND l.unit_cost > 0 ORDER BY (m.warehouse_id = $2) DESC, l.seq DESC LIMIT 1`, it.ID, p.wh.ID).Scan(&c)
	if err != nil && !dbtx.IsNoRows(err) {
		return decimal.Zero, err
	}
	v := it.StandardCost
	if c != nil {
		v = dec(*c)
	}
	st.lastCost = &v
	return v, nil
}

// averageCost is the moving average of the warehouse (last cost when empty).
func (p *poster) averageCost(ctx context.Context, it itemInfo, st *itemState) (decimal.Decimal, error) {
	if st.qty.IsPositive() && st.value.IsPositive() {
		return st.value.Div(st.qty).Round(6), nil
	}
	return p.lastCost(ctx, it, st)
}

func (p *poster) resolveBatch(ctx context.Context, it itemInfo, l PostLine) (*uuid.UUID, error) {
	if l.BatchID != nil {
		var ok bool
		if err := p.tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.batches WHERE id = $1 AND item_id = $2)`, *l.BatchID, it.ID).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			return nil, errs.Validation("batch_not_found", "batch not found for item "+it.Code)
		}
		return l.BatchID, nil
	}
	no := strings.TrimSpace(l.BatchNo)
	if no == "" {
		return nil, nil
	}
	if l.Quantity.IsNegative() {
		var bid uuid.UUID
		err := p.tx.QueryRow(ctx, `SELECT id FROM inventory.batches WHERE property_id = $1 AND item_id = $2 AND batch_no = $3`, p.property, it.ID, no).Scan(&bid)
		if dbtx.IsNoRows(err) {
			return nil, errs.Validation("batch_not_found", "batch "+no+" of "+it.Code+" not found")
		}
		return &bid, err
	}
	var expiry *time.Time
	if l.ExpiryDate != nil {
		e := dateOnly(*l.ExpiryDate)
		expiry = &e
	} else if it.ShelfLife != nil {
		e := p.in.BusinessDate.AddDate(0, 0, *it.ShelfLife)
		expiry = &e
	}
	bid := id.New()
	err := p.tx.QueryRow(ctx, `INSERT INTO inventory.batches (id, property_id, item_id, batch_no, expiry_date, supplier_id) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (property_id, item_id, batch_no) DO UPDATE SET expiry_date = coalesce(inventory.batches.expiry_date, EXCLUDED.expiry_date)
		RETURNING id`, bid, p.property, it.ID, no, expiry, p.in.SupplierID).Scan(&bid)
	return &bid, err
}

// batchStock returns the positive batch quantities of an item (FEFO order).
func (p *poster) batchStock(ctx context.Context, it itemInfo) (map[uuid.UUID]decimal.Decimal, []uuid.UUID, error) {
	rows, err := p.tx.Query(ctx, `SELECT b.batch_id, b.quantity::text FROM inventory.stock_balances b JOIN inventory.batches bt ON bt.id = b.batch_id
		WHERE b.warehouse_id = $1 AND b.item_id = $2 AND b.batch_id IS NOT NULL
		ORDER BY bt.expiry_date NULLS LAST, bt.received_at, bt.batch_no`, p.wh.ID, it.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	if _, ok := p.batches[it.ID]; !ok {
		p.batches[it.ID] = map[uuid.UUID]decimal.Decimal{}
	}
	cache := p.batches[it.ID]
	var order []uuid.UUID
	for rows.Next() {
		var bid uuid.UUID
		var q string
		if err := rows.Scan(&bid, &q); err != nil {
			return nil, nil, err
		}
		if _, ok := cache[bid]; !ok {
			cache[bid] = dec(q)
		}
		order = append(order, bid)
	}
	return cache, order, rows.Err()
}

func layerKey(item uuid.UUID, batch *uuid.UUID) string {
	if batch == nil {
		return item.String() + "/-"
	}
	return item.String() + "/" + batch.String()
}

func (p *poster) openLayers(ctx context.Context, it itemInfo, batch *uuid.UUID) ([]*layer, error) {
	k := layerKey(it.ID, batch)
	if ls, ok := p.layers[k]; ok {
		return ls, nil
	}
	rows, err := p.tx.Query(ctx, `SELECT id, remaining::text, unit_cost::text FROM inventory.cost_layers WHERE warehouse_id = $1 AND item_id = $2
		AND batch_id IS NOT DISTINCT FROM $3 AND remaining > 0 ORDER BY received_at, id FOR UPDATE`, p.wh.ID, it.ID, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ls []*layer
	for rows.Next() {
		var l layer
		var r, c string
		if err := rows.Scan(&l.id, &r, &c); err != nil {
			return nil, err
		}
		l.remaining, l.cost = dec(r), dec(c)
		ls = append(ls, &l)
	}
	p.layers[k] = ls
	return ls, rows.Err()
}

// entered reports lines typed by a user that must carry their batch and
// serial numbers (adjustments, opening stock, consignment receipts).
func (p *poster) entered() bool {
	return !p.in.Auto && p.in.ReversalOf == nil && (p.in.SourceType == "adjustment" || p.in.SourceType == "consignment")
}

func (p *poster) fifo(it itemInfo) bool {
	return it.StockValuation == "fifo" && p.wh.Type != "transit" && !it.Consignment
}

// line turns one input line into valued pieces.
func (p *poster) line(ctx context.Context, idx int, l PostLine) error {
	it, err := p.item(ctx, l.ItemID)
	if err != nil {
		if p.in.Auto {
			return err
		}
		return errs.Validation("item_not_found", fmt.Sprintf("line %d: item not found", idx+1), errs.Field(fmt.Sprintf("lines[%d].itemId", idx), "not_found", "item not found"))
	}
	if !it.stocked() {
		if p.in.Auto {
			return nil // services and non-stock items are not deducted
		}
		return errs.Validation("not_stock_item", it.Code+" is not a stock item", errs.Field(fmt.Sprintf("lines[%d].itemId", idx), "not_stock", "not a stock item"))
	}
	if it.Serial && len(l.SerialNos) > 0 && !l.Quantity.Abs().Equal(decimal.NewFromInt(int64(len(l.SerialNos)))) {
		return errs.Validation("serial_count", fmt.Sprintf("%s: %d serial numbers for quantity %s", it.Code, len(l.SerialNos), l.Quantity.Abs()),
			errs.Field(fmt.Sprintf("lines[%d].serialNos", idx), "count", "one serial number per unit"))
	}
	st, err := p.state(ctx, it)
	if err != nil {
		return err
	}
	if l.Quantity.IsPositive() {
		return p.inbound(ctx, idx, it, st, l)
	}
	return p.outbound(ctx, idx, it, st, l)
}

func (p *poster) inbound(ctx context.Context, idx int, it itemInfo, st *itemState, l PostLine) error {
	batch, err := p.resolveBatch(ctx, it, l)
	if err != nil {
		return err
	}
	if it.Batched && batch == nil && p.entered() {
		return errs.Validation("batch_required", it.Code+" is tracked by batch / expiry: batchNo is required",
			errs.Field(fmt.Sprintf("lines[%d].batchNo", idx), "required", "batch number required"))
	}
	if it.Serial && len(l.SerialNos) == 0 && p.entered() {
		return errs.Validation("serial_required", it.Code+" is tracked by serial number: serialNos are required",
			errs.Field(fmt.Sprintf("lines[%d].serialNos", idx), "required", "serial numbers required"))
	}
	var cost decimal.Decimal
	switch {
	case it.Consignment:
		cost = decimal.Zero
	case l.UnitCost != nil:
		cost = l.UnitCost.Round(6)
	default:
		if cost, err = p.averageCost(ctx, it, st); err != nil {
			return err
		}
	}
	pc := &piece{item: it, qty: l.Quantity, unitCost: cost, total: l.Quantity.Mul(cost).Round(4), batchID: batch, serials: l.SerialNos,
		location: l.StockLocationID, lineID: id.New()}
	if p.fifo(it) {
		rem := l.Quantity
		if st.qty.IsNegative() {
			rem = decimal.Max(l.Quantity.Add(st.qty), decimal.Zero)
		}
		pc.newLayer = rem
	}
	st.qty, st.value = st.qty.Add(pc.qty), st.value.Add(pc.total)
	if batch != nil {
		if p.batches[it.ID] == nil {
			p.batches[it.ID] = map[uuid.UUID]decimal.Decimal{}
		}
		if _, ok := p.batches[it.ID][*batch]; ok {
			p.batches[it.ID][*batch] = p.batches[it.ID][*batch].Add(pc.qty)
		}
	}
	pc.balAfter, pc.valueAfter = st.qty, st.value
	p.pieces = append(p.pieces, pc)
	return nil
}

type alloc struct {
	batch *uuid.UUID
	qty   decimal.Decimal // positive
}

func (p *poster) outbound(ctx context.Context, idx int, it itemInfo, st *itemState, l PostLine) error {
	need := l.Quantity.Neg()
	var allocs []alloc
	batch, err := p.resolveBatch(ctx, it, l)
	if err != nil {
		return err
	}
	switch {
	case batch != nil:
		cache, _, err := p.batchStock(ctx, it)
		if err != nil {
			return err
		}
		if have := cache[*batch]; have.LessThan(need) && !p.allowNeg && !p.in.Auto && !p.in.ExplicitCost {
			return errs.Validation("insufficient_stock", fmt.Sprintf("%s batch has %s, %s requested", it.Code, have, need),
				errs.Field(fmt.Sprintf("lines[%d].quantity", idx), "insufficient", "insufficient batch stock"))
		}
		allocs = []alloc{{batch, need}}
	case it.Batched:
		cache, order, err := p.batchStock(ctx, it)
		if err != nil {
			return err
		}
		left := need
		for _, b := range order {
			have := cache[b]
			if !have.IsPositive() || !left.IsPositive() {
				continue
			}
			take := decimal.Min(have, left)
			bb := b
			allocs = append(allocs, alloc{&bb, take})
			left = left.Sub(take)
		}
		if left.IsPositive() {
			allocs = append(allocs, alloc{nil, left})
		}
	default:
		allocs = []alloc{{nil, need}}
	}
	if newQty := st.qty.Sub(need); newQty.IsNegative() && !p.allowNeg {
		if !p.in.Auto && !p.in.ExplicitCost {
			return errs.Validation("insufficient_stock", fmt.Sprintf("%s: %s %s in %s, %s requested", it.Code, st.qty, it.UOMCode, p.wh.Code, need),
				errs.Field(fmt.Sprintf("lines[%d].quantity", idx), "insufficient", "insufficient stock"))
		}
	}
	serials := l.SerialNos
	if it.Serial && len(serials) == 0 {
		if p.entered() || (!p.in.Auto && p.in.ReversalOf == nil && (p.in.SourceType == "requisition" || p.in.SourceType == "transfer" || p.in.SourceType == "waste")) {
			return errs.Validation("serial_required", it.Code+" is tracked by serial number: serialNos are required",
				errs.Field(fmt.Sprintf("lines[%d].serialNos", idx), "required", "serial numbers required"))
		}
		n := need.IntPart()
		rows, err := p.tx.Query(ctx, `SELECT serial_no FROM inventory.serials WHERE item_id = $1 AND warehouse_id = $2 AND status = 'in_stock'
			ORDER BY received_at, serial_no LIMIT $3 FOR UPDATE`, it.ID, p.wh.ID, n)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sn string
			if err := rows.Scan(&sn); err != nil {
				rows.Close()
				return err
			}
			serials = append(serials, sn)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for ai, a := range allocs {
		var sn []string
		if ai == 0 {
			sn = serials
		}
		if err := p.costOut(ctx, it, st, a, l, sn); err != nil {
			return err
		}
	}
	return nil
}

// costOut values one outbound allocation (one batch).
func (p *poster) costOut(ctx context.Context, it itemInfo, st *itemState, a alloc, l PostLine, serials []string) error {
	add := func(qty, cost, total decimal.Decimal) {
		st.qty, st.value = st.qty.Sub(qty), st.value.Sub(total)
		pc := &piece{item: it, qty: qty.Neg(), unitCost: cost, total: total.Neg(), batchID: a.batch, serials: serials, location: l.StockLocationID,
			sales: l.SalesAmount, lineID: id.New()}
		serials = nil
		if st.qty.IsNegative() {
			pc.negative = true
			if !p.allowNeg {
				p.flagged = true
			}
		}
		pc.balAfter, pc.valueAfter = st.qty, st.value
		p.pieces = append(p.pieces, pc)
	}
	if a.batch != nil {
		if c, ok := p.batches[it.ID][*a.batch]; ok {
			p.batches[it.ID][*a.batch] = c.Sub(a.qty)
		}
	}
	switch {
	case it.Consignment:
		add(a.qty, decimal.Zero, decimal.Zero)
		return nil
	case p.in.ExplicitCost && l.UnitCost != nil:
		cost := l.UnitCost.Round(6)
		if p.fifo(it) {
			if err := p.consumeLayers(ctx, it, a, nil); err != nil {
				return err
			}
		}
		add(a.qty, cost, a.qty.Mul(cost).Round(4))
		return nil
	case p.fifo(it):
		var parts []costPart
		if err := p.consumeLayers(ctx, it, a, &parts); err != nil {
			return err
		}
		for _, cp := range parts {
			add(cp.qty, cp.cost, cp.qty.Mul(cp.cost).Round(4))
		}
		return p.clearResidual(it, st)
	default:
		cost, err := p.averageCost(ctx, it, st)
		if err != nil {
			return err
		}
		total := a.qty.Mul(cost).Round(4)
		if a.qty.Equal(st.qty) && st.value.IsPositive() {
			total = st.value // the last units take the remaining value
			cost = total.Div(a.qty).Round(6)
		}
		add(a.qty, cost, total)
		return nil
	}
}

type costPart struct{ qty, cost decimal.Decimal }

// consumeLayers takes quantity from the open FIFO layers of the allocation's
// batch, oldest first; what the layers cannot cover is valued at last cost.
func (p *poster) consumeLayers(ctx context.Context, it itemInfo, a alloc, parts *[]costPart) error {
	ls, err := p.openLayers(ctx, it, a.batch)
	if err != nil {
		return err
	}
	left := a.qty
	for _, l := range ls {
		if !left.IsPositive() {
			break
		}
		if !l.remaining.IsPositive() {
			continue
		}
		take := decimal.Min(l.remaining, left)
		l.remaining, l.changed = l.remaining.Sub(take), true
		left = left.Sub(take)
		if parts != nil {
			if n := len(*parts); n > 0 && (*parts)[n-1].cost.Equal(l.cost) {
				(*parts)[n-1].qty = (*parts)[n-1].qty.Add(take)
			} else {
				*parts = append(*parts, costPart{take, l.cost})
			}
		}
	}
	if left.IsPositive() && parts != nil {
		st := p.states[it.ID]
		c, err := p.lastCost(ctx, it, st)
		if err != nil {
			return err
		}
		*parts = append(*parts, costPart{left, c})
	}
	return nil
}

// clearResidual books the rounding residue of FIFO into the last piece when
// the warehouse runs empty, so value is zero when quantity is zero.
func (p *poster) clearResidual(it itemInfo, st *itemState) error {
	if !st.qty.IsZero() || st.value.IsZero() || len(p.pieces) == 0 {
		return nil
	}
	last := p.pieces[len(p.pieces)-1]
	if last.item.ID != it.ID {
		return nil
	}
	last.total = last.total.Sub(st.value)
	last.unitCost = last.total.Div(last.qty).Round(6)
	st.value = decimal.Zero
	last.valueAfter = decimal.Zero
	return nil
}

func (p *poster) serialStatus() string {
	switch p.in.Type {
	case MoveConsumption:
		return "sold"
	case MoveIssue, MoveProductionOut:
		return "issued"
	case MoveTransferOut:
		return "in_transit"
	case MoveReturnOut:
		return "returned"
	}
	return "written_off"
}

// write stores the movement, lines, balances, layers and serials and
// publishes the events.
func (p *poster) write(ctx context.Context) (StockMovement, bool, error) {
	ctx0, tx, in := ctx, p.tx, p.in
	total := decimal.Zero
	for _, pc := range p.pieces {
		total = total.Add(pc.total)
	}
	mid := id.New()
	number, err := numbering.Next(ctx, tx, p.property, "SMV", localNow(ctx, tx))
	if err != nil {
		return StockMovement{}, false, err
	}
	costCenter := in.CostCenter
	if costCenter == "" && p.wh.CostCenter != nil {
		costCenter = *p.wh.CostCenter
	}
	uid := handle.UserID(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_movements (id, property_id, number, movement_type, business_date, warehouse_id, counter_warehouse_id,
		cost_center, outlet_id, department_id, asset_id, supplier_id, source_type, source_id, reversal_of, reason, notes, currency, total_cost, flagged, posted_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::numeric,$20,$21)`,
		mid, p.property, number, in.Type, in.BusinessDate, p.wh.ID, in.CounterWarehouseID, nullStr(costCenter), in.OutletID, in.DepartmentID, in.AssetID,
		in.SupplierID, in.SourceType, in.SourceID, in.ReversalOf, nullStr(in.Reason), nullStr(in.Notes), p.cfg.Currency, total.String(), p.flagged,
		uuidOrNil(uid)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return StockMovement{}, false, errs.Conflict("already_posted", "this source has already been posted")
		}
		return StockMovement{}, false, err
	}
	var lines []map[string]any
	for i, pc := range p.pieces {
		var serials any
		if len(pc.serials) > 0 {
			serials = pc.serials
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_movement_lines (id, property_id, movement_id, line_no, item_id, batch_id, serial_nos,
			stock_location_id, quantity, unit_cost, total_cost, consignment, balance_after, value_after, negative)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12,$13::numeric,$14::numeric,$15)`,
			pc.lineID, p.property, mid, i+1, pc.item.ID, pc.batchID, serials, pc.location, pc.qty.String(), pc.unitCost.String(), pc.total.String(),
			pc.item.Consignment, pc.balAfter.String(), pc.valueAfter.String(), pc.negative); err != nil {
			return StockMovement{}, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_balances (id, property_id, warehouse_id, item_id, batch_id, quantity, value, last_movement_at)
			VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric, now())
			ON CONFLICT (warehouse_id, item_id, (coalesce(batch_id, '00000000-0000-0000-0000-000000000000'::uuid)))
			DO UPDATE SET quantity = inventory.stock_balances.quantity + EXCLUDED.quantity, value = inventory.stock_balances.value + EXCLUDED.value,
			last_movement_at = now(), updated_at = now()`, id.New(), p.property, p.wh.ID, pc.item.ID, pc.batchID, pc.qty.String(), pc.total.String()); err != nil {
			return StockMovement{}, false, err
		}
		if pc.qty.IsPositive() && p.fifo(pc.item) {
			if _, err := tx.Exec(ctx, `INSERT INTO inventory.cost_layers (id, property_id, warehouse_id, item_id, batch_id, movement_line_id, received_at,
				quantity, remaining, unit_cost) VALUES ($1,$2,$3,$4,$5,$6, clock_timestamp(), $7::numeric, $8::numeric, $9::numeric)`,
				id.New(), p.property, p.wh.ID, pc.item.ID, pc.batchID, pc.lineID, pc.qty.String(), pc.newLayer.String(), pc.unitCost.String()); err != nil {
				return StockMovement{}, false, err
			}
		}
		if len(pc.serials) > 0 {
			if err := p.writeSerials(ctx, pc, mid); err != nil {
				return StockMovement{}, false, err
			}
		}
		lines = append(lines, map[string]any{"itemId": pc.item.ID, "quantity": pc.qty.String(), "unitCost": pc.unitCost.String(),
			"totalCost": pc.total.String(), "consignment": pc.item.Consignment, "itemCode": pc.item.Code, "batchId": pc.batchID})
	}
	for _, ls := range p.layers {
		for _, l := range ls {
			if l.changed {
				if _, err := tx.Exec(ctx, `UPDATE inventory.cost_layers SET remaining = $2::numeric, updated_at = now() WHERE id = $1`, l.id, l.remaining.String()); err != nil {
					return StockMovement{}, false, err
				}
			}
		}
	}
	if p.s.Events != nil {
		var cc any
		if costCenter != "" {
			cc = costCenter
		}
		if _, err := p.s.Events.Publish(ctx0, tx, EventMovementPosted, "inventory.stock_movement", &mid, &p.property, map[string]any{
			"movementId": mid, "number": number, "movementType": in.Type, "businessDate": in.BusinessDate.Format("2006-01-02"), "warehouseId": p.wh.ID,
			"warehouseCode": p.wh.Code, "counterWarehouseId": in.CounterWarehouseID, "costCenter": cc, "outletId": in.OutletID, "sourceType": eventSourceType(in),
			"sourceId": in.SourceID, "reversalOf": in.ReversalOf, "reason": nullStr(in.Reason), "currency": p.cfg.Currency, "totalCost": total.String(),
			"lines": lines}); err != nil {
			return StockMovement{}, false, err
		}
	}
	if err := p.consignmentSales(ctx, mid); err != nil {
		return StockMovement{}, false, err
	}
	m, err := GetMovement(ctx, tx, mid)
	return m, true, err
}

// eventSourceType is the sourceType published with inventory.movement_posted:
// the opening stock (an adjustment with reason opening_balance, posted by the
// opening stock import) is published as opening_stock so the ledger books it
// against opening balance equity instead of the stock variance (P&L).
func eventSourceType(in PostInput) string {
	if in.SourceType == "adjustment" && in.Reason == OpeningStockReason {
		return "opening_stock"
	}
	return in.SourceType
}

func (p *poster) writeSerials(ctx context.Context, pc *piece, mid uuid.UUID) error {
	if pc.qty.IsPositive() {
		for _, sn := range pc.serials {
			tag, err := p.tx.Exec(ctx, `INSERT INTO inventory.serials (id, property_id, item_id, serial_no, batch_id, warehouse_id, status, unit_cost, last_movement_id)
				VALUES ($1,$2,$3,$4,$5,$6,'in_stock',$7::numeric,$8)
				ON CONFLICT (property_id, item_id, serial_no) DO UPDATE SET warehouse_id = EXCLUDED.warehouse_id, status = 'in_stock',
				batch_id = coalesce(EXCLUDED.batch_id, inventory.serials.batch_id), last_movement_id = EXCLUDED.last_movement_id, updated_at = now()
				WHERE inventory.serials.status <> 'in_stock'`, id.New(), p.property, pc.item.ID, sn, pc.batchID, p.wh.ID, pc.unitCost.String(), mid)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errs.Conflict("serial_in_stock", "serial number "+sn+" of "+pc.item.Code+" is already in stock")
			}
		}
		return nil
	}
	tag, err := p.tx.Exec(ctx, `UPDATE inventory.serials SET status = $5, last_movement_id = $6, updated_at = now()
		WHERE property_id = $1 AND item_id = $2 AND warehouse_id = $3 AND serial_no = ANY($4) AND status = 'in_stock'`,
		p.property, pc.item.ID, p.wh.ID, pc.serials, p.serialStatus(), mid)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(pc.serials) && !p.in.Auto {
		return errs.Validation("serial_not_in_stock", fmt.Sprintf("%s: serial numbers not in stock at %s", pc.item.Code, p.wh.Code))
	}
	return nil
}

// consignmentSales records consignment items sold through sales, packages
// and banquets (inventory.consignment_sold, FR-INV-07).
func (p *poster) consignmentSales(ctx context.Context, mid uuid.UUID) error {
	switch p.in.SourceType {
	case "sale", "package", "banquet", "golf_round":
	default:
		return nil
	}
	if p.in.ReversalOf != nil {
		return nil
	}
	for _, pc := range p.pieces {
		if !pc.item.Consignment || !pc.qty.IsNegative() || pc.item.ConsignSupplier == nil {
			continue
		}
		qty := pc.qty.Neg()
		sales := decimal.Zero
		var payable, commission decimal.Decimal
		if pc.sales != nil && pc.sales.IsPositive() {
			sales = *pc.sales
			commission = sales.Mul(pc.item.Commission).Div(decimal.NewFromInt(100)).Round(2)
			payable = sales.Sub(commission)
		} else {
			payable = qty.Mul(pc.item.StandardCost).Round(2)
		}
		unit := payable.Div(qty).Round(6)
		if _, err := p.tx.Exec(ctx, `INSERT INTO inventory.consignment_sales (id, property_id, supplier_id, item_id, warehouse_id, movement_id, source_type, source_id,
			quantity, sales_amount, commission_percent, commission_amount, unit_cost, payable_amount, currency, business_date)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15,$16)
			ON CONFLICT (movement_id, item_id) DO UPDATE SET quantity = inventory.consignment_sales.quantity + EXCLUDED.quantity,
			sales_amount = inventory.consignment_sales.sales_amount + EXCLUDED.sales_amount,
			commission_amount = inventory.consignment_sales.commission_amount + EXCLUDED.commission_amount,
			payable_amount = inventory.consignment_sales.payable_amount + EXCLUDED.payable_amount`,
			id.New(), p.property, *pc.item.ConsignSupplier, pc.item.ID, p.wh.ID, mid, p.in.SourceType, p.in.SourceID, qty.String(), sales.String(),
			pc.item.Commission.String(), commission.String(), unit.String(), payable.String(), p.cfg.Currency, p.in.BusinessDate); err != nil {
			return err
		}
		if p.s.Events != nil {
			if _, err := p.s.Events.Publish(ctx, p.tx, EventConsignmentSold, "inventory.stock_movement", &mid, &p.property, map[string]any{
				"supplierId": *pc.item.ConsignSupplier, "itemId": pc.item.ID, "quantity": qty.String(), "unitCost": unit.String(), "totalCost": payable.String(),
				"currency": p.cfg.Currency, "sourceType": p.in.SourceType, "sourceId": p.in.SourceID, "movementId": mid, "salesAmount": sales.String(),
				"commissionAmount": commission.String(), "businessDate": p.in.BusinessDate.Format("2006-01-02")}); err != nil {
				return err
			}
		}
	}
	return nil
}

// mirror turns the posted lines of an outbound movement into inbound lines
// at the same cost, batch and serials (transfers between warehouses).
func mirror(m StockMovement) []PostLine {
	var out []PostLine
	for _, l := range m.Lines {
		q := dec(l.Quantity).Neg()
		c := dec(l.UnitCost)
		pl := PostLine{ItemID: l.ItemID, Quantity: q, UnitCost: &c, BatchID: l.BatchID, SerialNos: l.SerialNos}
		out = append(out, pl)
	}
	return out
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func uuidOrNil(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}
