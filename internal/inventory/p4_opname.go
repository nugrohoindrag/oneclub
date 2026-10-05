package inventory

// PRD P4 EP-04 Stock Opname: snapshot of the system quantities, blind
// counting (barcode, `ops`), variance with tolerance per category, recount
// or approval above tolerance, and posting of the variance as an opname
// movement at valuation cost. Counting with a recorded cut-off: movements
// posted between the snapshot and the count are part of the expected
// quantity, so the warehouse may keep operating (FR-OPN-04); with freeze,
// manual movements are blocked.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// StockOpnameInput starts a stock opname.
type StockOpnameInput struct {
	WarehouseID     uuid.UUID   `json:"warehouseId"`
	CategoryID      *uuid.UUID  `json:"categoryId,omitempty" doc:"Count one category (with its sub-categories)"`
	StockLocationID *uuid.UUID  `json:"stockLocationId,omitempty" doc:"Count the items stored at one stock location (par stock location)"`
	ItemIDs         []uuid.UUID `json:"itemIds,omitempty" doc:"Spot check of these items only"`
	Blind           *bool       `json:"blind,omitempty" doc:"Counters do not see system quantities (default Inventory Policies)"`
	Freeze          *bool       `json:"freeze,omitempty" doc:"Block manual movements while counting (default Inventory Policies; otherwise cut-off)"`
	Notes           string      `json:"notes,omitempty"`
}

// StockOpname is a stock opname with its count sheet.
type StockOpname struct {
	ID                uuid.UUID         `json:"id" db:"id"`
	Number            string            `json:"number" db:"number"`
	WarehouseID       uuid.UUID         `json:"warehouseId" db:"warehouse_id"`
	Warehouse         string            `json:"warehouse" db:"warehouse"`
	CategoryID        *uuid.UUID        `json:"categoryId" db:"category_id"`
	StockLocationID   *uuid.UUID        `json:"stockLocationId" db:"stock_location_id"`
	Blind             bool              `json:"blind" db:"blind"`
	Freeze            bool              `json:"freeze" db:"freeze"`
	Status            string            `json:"status" db:"status" enum:"in_progress,counted,pending_approval,posted,cancelled"`
	SnapshotAt        time.Time         `json:"snapshotAt" db:"snapshot_at"`
	CountedAt         *time.Time        `json:"countedAt" db:"counted_at"`
	PostedAt          *time.Time        `json:"postedAt" db:"posted_at"`
	ApprovalRequestID *uuid.UUID        `json:"approvalRequestId" db:"approval_request_id"`
	MovementID        *uuid.UUID        `json:"movementId" db:"movement_id"`
	MovementNumber    *string           `json:"movementNumber" db:"movement_number"`
	TolerancePercent  string            `json:"tolerancePercent" db:"tolerance_percent"`
	SystemValue       string            `json:"systemValue" db:"system_value"`
	VarianceValue     string            `json:"varianceValue" db:"variance_value"`
	Notes             *string           `json:"notes" db:"notes"`
	CreatedAt         time.Time         `json:"createdAt" db:"created_at"`
	LineCount         int               `json:"lineCount" db:"line_count"`
	CountedLines      int               `json:"countedLines" db:"counted_lines"`
	OverTolerance     int               `json:"overTolerance" db:"over_tolerance"`
	Lines             []StockOpnameLine `json:"lines,omitempty" db:"-"`
}

// StockOpnameLine is one line of the count sheet.
type StockOpnameLine struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	ItemID           uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode         string     `json:"itemCode" db:"item_code"`
	ItemName         string     `json:"itemName" db:"item_name"`
	Barcode          *string    `json:"barcode" db:"barcode"`
	UOM              string     `json:"uom" db:"uom"`
	BatchID          *uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo          *string    `json:"batchNo" db:"batch_no"`
	ExpiryDate       *string    `json:"expiryDate" db:"expiry_date"`
	SystemQuantity   *string    `json:"systemQuantity" db:"system_quantity" doc:"Hidden while a blind count is in progress"`
	CountedQuantity  *string    `json:"countedQuantity" db:"counted_quantity"`
	CountedAt        *time.Time `json:"countedAt" db:"counted_at"`
	ExpectedQuantity *string    `json:"expectedQuantity" db:"expected_quantity" doc:"Snapshot + movements until the count (cut-off)"`
	VarianceQuantity *string    `json:"varianceQuantity" db:"variance_quantity"`
	UnitCost         string     `json:"unitCost" db:"unit_cost"`
	VarianceValue    *string    `json:"varianceValue" db:"variance_value"`
	TolerancePercent string     `json:"tolerancePercent" db:"tolerance_percent"`
	OverTolerance    bool       `json:"overTolerance" db:"over_tolerance"`
	Recounts         int        `json:"recounts" db:"recounts"`
	Notes            *string    `json:"notes" db:"notes"`
}

const opnameSelect = `SELECT o.id, o.number, o.warehouse_id, w.name AS warehouse, o.category_id, o.stock_location_id, o.blind, o.freeze_movements AS freeze, o.status, o.snapshot_at,
	o.counted_at, o.posted_at, o.approval_request_id, o.movement_id, m.number AS movement_number, trim_scale(o.tolerance_percent)::text AS tolerance_percent,
	trim_scale(o.system_value)::text AS system_value, trim_scale(o.variance_value)::text AS variance_value, o.notes, o.created_at,
	(SELECT count(*) FROM inventory.stock_opname_lines l WHERE l.opname_id = o.id)::int AS line_count,
	(SELECT count(*) FROM inventory.stock_opname_lines l WHERE l.opname_id = o.id AND l.counted_quantity IS NOT NULL)::int AS counted_lines,
	(SELECT count(*) FROM inventory.stock_opname_lines l WHERE l.opname_id = o.id AND l.over_tolerance)::int AS over_tolerance
	FROM inventory.stock_opnames o JOIN inventory.warehouses w ON w.id = o.warehouse_id LEFT JOIN inventory.stock_movements m ON m.id = o.movement_id`

// GetOpname returns an opname with its count sheet (system quantities are
// hidden during a blind count).
func GetOpname(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (StockOpname, error) {
	rows, err := q.Query(ctx, opnameSelect+` WHERE o.id = $1`, oid)
	o, err := handle.One[StockOpname](rows, err, "stock opname")
	if err != nil {
		return o, err
	}
	hide := o.Blind && o.Status == "in_progress"
	o.Lines, err = handle.List[StockOpnameLine](q.Query(ctx, `SELECT l.id, l.item_id, i.code AS item_code, i.name AS item_name, i.barcode, u.code AS uom, l.batch_id,
		b.batch_no, to_char(b.expiry_date, 'YYYY-MM-DD') AS expiry_date,
		CASE WHEN $2 THEN NULL ELSE trim_scale(l.system_quantity)::text END AS system_quantity, trim_scale(l.counted_quantity)::text AS counted_quantity,
		l.counted_at, CASE WHEN $2 THEN NULL ELSE trim_scale(l.expected_quantity)::text END AS expected_quantity,
		CASE WHEN $2 THEN NULL ELSE trim_scale(l.variance_quantity)::text END AS variance_quantity, trim_scale(l.unit_cost)::text AS unit_cost,
		CASE WHEN $2 THEN NULL ELSE trim_scale(l.variance_value)::text END AS variance_value, trim_scale(l.tolerance_percent)::text AS tolerance_percent,
		l.over_tolerance, l.recounts, l.notes
		FROM inventory.stock_opname_lines l JOIN inventory.items i ON i.id = l.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		LEFT JOIN inventory.batches b ON b.id = l.batch_id WHERE l.opname_id = $1 ORDER BY i.code, b.expiry_date NULLS LAST, b.batch_no`, oid, hide))
	return o, err
}

func lockOpname(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (StockOpname, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.stock_opnames WHERE id = $1 FOR UPDATE`, oid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return StockOpname{}, errs.NotFound("stock opname")
	}
	if err != nil {
		return StockOpname{}, err
	}
	return GetOpname(ctx, tx, oid)
}

// StartOpname snapshots the system quantities of the warehouse (FR-OPN-01).
func (s *Stock) StartOpname(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StockOpnameInput) (StockOpname, error) {
	wh, err := loadWarehouse(ctx, tx, property, in.WarehouseID)
	if err != nil {
		return StockOpname{}, err
	}
	var busy string
	err = tx.QueryRow(ctx, `SELECT number FROM inventory.stock_opnames WHERE warehouse_id = $1 AND status IN ('in_progress', 'counted', 'pending_approval')`,
		wh.ID).Scan(&busy)
	if err == nil {
		return StockOpname{}, errs.Conflict("opname_open", "stock opname "+busy+" of "+wh.Code+" is still open")
	}
	if !dbtx.IsNoRows(err) {
		return StockOpname{}, err
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return StockOpname{}, err
	}
	blind, freeze := pol.BlindCount, pol.FreezeDuringOpname
	if in.Blind != nil {
		blind = *in.Blind
	}
	if in.Freeze != nil {
		freeze = *in.Freeze
	}
	tol := dec(pol.OpnameTolerancePercent)
	oid := id.New()
	number, err := numbering.Next(ctx, tx, property, "SOP", localNow(ctx, tx))
	if err != nil {
		return StockOpname{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_opnames (id, property_id, number, warehouse_id, category_id, stock_location_id, blind, freeze_movements,
		tolerance_percent, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$11)`, oid, property, number, wh.ID, in.CategoryID,
		in.StockLocationID, blind, freeze, tol.String(), nullStr(in.Notes), uid); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return StockOpname{}, errs.Validation("invalid_reference", "category or stock location not found")
		}
		return StockOpname{}, err
	}
	items := in.ItemIDs
	if items == nil {
		items = []uuid.UUID{}
	}
	type snap struct {
		item  uuid.UUID
		batch *uuid.UUID
		qty   string
		tol   *string
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE cats AS (SELECT id FROM inventory.item_categories WHERE id = $2
		UNION ALL SELECT c.id FROM inventory.item_categories c JOIN cats ON c.parent_id = cats.id),
		x AS (SELECT b.item_id, b.batch_id, b.quantity FROM inventory.stock_balances b WHERE b.warehouse_id = $1 AND (b.quantity <> 0 OR b.batch_id IS NULL)
		  UNION ALL SELECT p.item_id, NULL::uuid, 0 FROM inventory.par_stocks p WHERE p.warehouse_id = $1
		  AND NOT EXISTS (SELECT 1 FROM inventory.stock_balances b WHERE b.warehouse_id = $1 AND b.item_id = p.item_id)
		  UNION ALL SELECT i.id, NULL::uuid, 0 FROM inventory.items i WHERE i.id = ANY($4)
		  AND NOT EXISTS (SELECT 1 FROM inventory.stock_balances b WHERE b.warehouse_id = $1 AND b.item_id = i.id))
		SELECT DISTINCT ON (x.item_id, x.batch_id) x.item_id, x.batch_id, x.quantity::text, c.opname_tolerance_percent::text
		FROM x JOIN inventory.items i ON i.id = x.item_id LEFT JOIN inventory.item_categories c ON c.id = i.category_id
		WHERE i.item_type NOT IN ('service', 'non_stock')
		AND ($2::uuid IS NULL OR i.category_id IN (SELECT id FROM cats))
		AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM inventory.par_stocks ps WHERE ps.warehouse_id = $1 AND ps.item_id = x.item_id AND ps.stock_location_id = $3))
		AND (cardinality($4::uuid[]) = 0 OR x.item_id = ANY($4))
		ORDER BY x.item_id, x.batch_id`, wh.ID, in.CategoryID, in.StockLocationID, items)
	if err != nil {
		return StockOpname{}, err
	}
	var snaps []snap
	for rows.Next() {
		var sn snap
		if err := rows.Scan(&sn.item, &sn.batch, &sn.qty, &sn.tol); err != nil {
			rows.Close()
			return StockOpname{}, err
		}
		snaps = append(snaps, sn)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return StockOpname{}, err
	}
	if len(snaps) == 0 {
		return StockOpname{}, errs.Validation("nothing_to_count", "no stock item to count in "+wh.Code)
	}
	value := decimal.Zero
	for _, sn := range snaps {
		c, err := unitCostAt(ctx, tx, wh.ID, sn.item)
		if err != nil {
			return StockOpname{}, err
		}
		t := tol
		if sn.tol != nil {
			t = dec(*sn.tol)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_opname_lines (id, property_id, opname_id, item_id, batch_id, system_quantity, unit_cost, tolerance_percent)
			VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric)`, id.New(), property, oid, sn.item, sn.batch, sn.qty, c.String(), t.String()); err != nil {
			return StockOpname{}, err
		}
		value = value.Add(dec(sn.qty).Mul(c))
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET system_value = $2::numeric WHERE id = $1`, oid, value.Round(4).String()); err != nil {
		return StockOpname{}, err
	}
	o, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	return o, record(ctx, tx, property, "inventory.stock_opname", oid, number, "create", nil,
		map[string]any{"warehouse": wh.Code, "lines": o.LineCount, "blind": blind, "freeze": freeze}, "")
}

// OpnameCountInput records counted quantities (barcode scanning in `ops`).
type OpnameCountInput struct {
	Lines []OpnameCountLine `json:"lines"`
}

// OpnameCountLine is one count entry.
type OpnameCountLine struct {
	ItemID   *uuid.UUID `json:"itemId,omitempty"`
	Barcode  string     `json:"barcode,omitempty" doc:"Scanned item or carton barcode"`
	BatchNo  string     `json:"batchNo,omitempty"`
	Quantity string     `json:"quantity" doc:"Counted quantity in uomId / the barcode's UOM / the stock UOM"`
	UOMID    *uuid.UUID `json:"uomId,omitempty"`
	Mode     string     `json:"mode,omitempty" enum:"set,add" doc:"set (default) replaces the count; add accumulates scans"`
	Notes    string     `json:"notes,omitempty"`
}

// lookupBarcode resolves an item or carton barcode to item and UOM.
func lookupBarcode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (uuid.UUID, *uuid.UUID, error) {
	var item uuid.UUID
	var uom *uuid.UUID
	err := q.QueryRow(ctx, `SELECT item_id, uom_id FROM (
		SELECT id AS item_id, NULL::uuid AS uom_id, 0 AS prio FROM inventory.items WHERE property_id = $1 AND barcode = $2 AND archived_at IS NULL
		UNION ALL SELECT item_id, uom_id, 1 FROM inventory.item_barcodes WHERE property_id = $1 AND barcode = $2) x ORDER BY prio LIMIT 1`,
		property, strings.TrimSpace(code)).Scan(&item, &uom)
	if dbtx.IsNoRows(err) {
		return item, nil, errs.NotFound("barcode")
	}
	return item, uom, err
}

// CountOpname records counts; unknown items found in the warehouse are added.
func (s *Stock) CountOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in OpnameCountInput) (StockOpname, error) {
	o, err := lockOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "in_progress" {
		return o, conflictStatus("Stock opname", o.Number, o.Status, "counted")
	}
	if len(in.Lines) == 0 {
		return o, errs.Validation("lines_required", "at least one count is required")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return o, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	for i, c := range in.Lines {
		var itemID uuid.UUID
		uom := c.UOMID
		switch {
		case c.ItemID != nil:
			itemID = *c.ItemID
		case strings.TrimSpace(c.Barcode) != "":
			var bu *uuid.UUID
			if itemID, bu, err = lookupBarcode(ctx, tx, property, c.Barcode); err != nil {
				return o, handle.Invalid(lineField(i, "barcode"), "unknown_barcode", "barcode "+c.Barcode+" is not known")
			}
			if uom == nil {
				uom = bu
			}
		default:
			return o, handle.Invalid(lineField(i, "itemId"), "required", "itemId or barcode is required")
		}
		it, err := loadItem(ctx, tx, property, itemID, cfg.ValuationMethod)
		if err != nil {
			return o, err
		}
		q, err := handle.Decimal(lineField(i, "quantity"), c.Quantity, decimal.Zero)
		if err != nil {
			return o, err
		}
		if q.IsNegative() {
			return o, handle.Invalid(lineField(i, "quantity"), "invalid", "counted quantity must not be negative")
		}
		if q, err = toBase(ctx, tx, it, q, uom); err != nil {
			return o, err
		}
		var batch *uuid.UUID
		if b := strings.TrimSpace(c.BatchNo); b != "" {
			bid := id.New()
			if err := tx.QueryRow(ctx, `INSERT INTO inventory.batches (id, property_id, item_id, batch_no) VALUES ($1,$2,$3,$4)
				ON CONFLICT (property_id, item_id, batch_no) DO UPDATE SET batch_no = EXCLUDED.batch_no RETURNING id`, bid, property, it.ID, b).Scan(&bid); err != nil {
				return o, err
			}
			batch = &bid
		}
		var lid uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM inventory.stock_opname_lines WHERE opname_id = $1 AND item_id = $2 AND batch_id IS NOT DISTINCT FROM $3`,
			oid, it.ID, batch).Scan(&lid)
		if dbtx.IsNoRows(err) {
			cost, err := unitCostAt(ctx, tx, o.WarehouseID, it.ID)
			if err != nil {
				return o, err
			}
			lid = id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO inventory.stock_opname_lines (id, property_id, opname_id, item_id, batch_id, system_quantity, unit_cost,
				tolerance_percent, notes) SELECT $1,$2,$3,$4,$5,0,$6::numeric, coalesce(c.opname_tolerance_percent, $7::numeric), 'found during count'
				FROM inventory.items i LEFT JOIN inventory.item_categories c ON c.id = i.category_id WHERE i.id = $4`,
				lid, property, oid, it.ID, batch, cost.String(), o.TolerancePercent); err != nil {
				return o, err
			}
		} else if err != nil {
			return o, err
		}
		expr := `$2::numeric`
		if c.Mode == "add" {
			expr = `coalesce(counted_quantity, 0) + $2::numeric`
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opname_lines SET counted_quantity = `+expr+`, counted_at = now(), counted_by = $3,
			notes = coalesce($4, notes) WHERE id = $1`, lid, q.String(), uid, nullStr(c.Notes)); err != nil {
			return o, err
		}
	}
	after, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.stock_opname", oid, o.Number, "count", nil, map[string]any{"entries": len(in.Lines),
		"countedLines": after.CountedLines}, "")
}

// SyncCountAction is the offline sync action of opname counts (FR-OPS-P4-04).
const SyncCountAction = "inventory.opname_count"

// OpnameCountSync is the payload of an offline count.
type OpnameCountSync struct {
	OpnameID uuid.UUID         `json:"opnameId"`
	Lines    []OpnameCountLine `json:"lines"`
}

// SyncCount applies a count queued offline by a warehouse device; the sync
// service runs it once per queue item id (idempotent sync).
func (s *Stock) SyncCount(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in OpnameCountSync
	if err := json.Unmarshal(payload, &in); err != nil || in.OpnameID == uuid.Nil {
		return nil, errs.Validation("invalid_payload", "invalid stock opname count")
	}
	property := handle.Property(ctx)
	if p := authz.From(ctx); p == nil || !p.Can("inventory.stock_opname.count", &property) {
		return nil, errs.Forbidden("inventory.stock_opname.count")
	}
	if err := inProperty(ctx, tx, "inventory.stock_opnames", in.OpnameID, property, "stock opname"); err != nil {
		return nil, err
	}
	o, err := s.CountOpname(ctx, tx, property, in.OpnameID, OpnameCountInput{Lines: in.Lines})
	if err != nil {
		return nil, err
	}
	return map[string]any{"opnameId": o.ID, "number": o.Number, "countedLines": o.CountedLines, "lineCount": o.LineCount}, nil
}

// SubmitOpnameInput closes the count.
type SubmitOpnameInput struct {
	ZeroUncounted bool `json:"zeroUncounted,omitempty" doc:"Lines not counted are counted as zero"`
}

// SubmitOpname computes the variance per line (In Progress → Counted).
func (s *Stock) SubmitOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in SubmitOpnameInput) (StockOpname, error) {
	o, err := lockOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "in_progress" {
		return o, conflictStatus("Stock opname", o.Number, o.Status, "submitted")
	}
	if missing := o.LineCount - o.CountedLines; missing > 0 {
		if !in.ZeroUncounted {
			return o, errs.Validation("not_counted", fmt.Sprintf("%d line(s) are not counted (count them or submit with zeroUncounted)", missing))
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opname_lines SET counted_quantity = 0, counted_at = now(), counted_by = $2
			WHERE opname_id = $1 AND counted_quantity IS NULL`, oid, uuidOrNil(handle.UserID(ctx))); err != nil {
			return o, err
		}
	}
	// Expected = snapshot + movements between the snapshot and the count.
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opname_lines l SET expected_quantity = l.system_quantity + coalesce((SELECT sum(ml.quantity)
		FROM inventory.stock_movement_lines ml JOIN inventory.stock_movements m ON m.id = ml.movement_id
		WHERE m.warehouse_id = $2 AND ml.item_id = l.item_id AND ml.batch_id IS NOT DISTINCT FROM l.batch_id AND m.posted_at > $3
		AND m.posted_at <= l.counted_at AND m.source_type <> 'opname'), 0) WHERE l.opname_id = $1`, oid, o.WarehouseID, o.SnapshotAt); err != nil {
		return o, err
	}
	rows, err := tx.Query(ctx, `SELECT id, item_id, expected_quantity::text, counted_quantity::text, tolerance_percent::text FROM inventory.stock_opname_lines
		WHERE opname_id = $1`, oid)
	if err != nil {
		return o, err
	}
	type ln struct {
		id, item             uuid.UUID
		expected, counted, t string
	}
	var lines []ln
	for rows.Next() {
		var l ln
		if err := rows.Scan(&l.id, &l.item, &l.expected, &l.counted, &l.t); err != nil {
			rows.Close()
			return o, err
		}
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return o, err
	}
	total := decimal.Zero
	for _, l := range lines {
		exp, cnt := dec(l.expected), dec(l.counted)
		variance := cnt.Sub(exp)
		cost, err := unitCostAt(ctx, tx, o.WarehouseID, l.item)
		if err != nil {
			return o, err
		}
		over := false
		if !variance.IsZero() {
			base := decimal.Max(exp.Abs(), cnt.Abs())
			pct := decimal.NewFromInt(100)
			if base.IsPositive() {
				pct = variance.Abs().Div(base).Mul(decimal.NewFromInt(100))
			}
			over = pct.GreaterThan(dec(l.t))
		}
		vv := variance.Mul(cost).Round(4)
		total = total.Add(vv)
		if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opname_lines SET variance_quantity = $2::numeric, unit_cost = $3::numeric, variance_value = $4::numeric,
			over_tolerance = $5 WHERE id = $1`, l.id, variance.String(), cost.String(), vv.String(), over); err != nil {
			return o, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'counted', counted_at = now(), variance_value = $2::numeric, updated_by = $3 WHERE id = $1`,
		oid, total.String(), uuidOrNil(handle.UserID(ctx))); err != nil {
		return o, err
	}
	after, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.stock_opname", oid, o.Number, "submit", map[string]any{"status": o.Status},
		map[string]any{"status": "counted", "varianceValue": total.String(), "overTolerance": after.OverTolerance}, "")
}

// PostOpname posts the variance; lines above tolerance need approval first
// (FR-OPN-02).
func (s *Stock) PostOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID) (StockOpname, error) {
	o, err := lockOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "counted" {
		return o, conflictStatus("Stock opname", o.Number, o.Status, "posted")
	}
	if o.OverTolerance > 0 {
		var amount, pct string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(abs(variance_value)), 0)::text, coalesce(max(CASE WHEN greatest(abs(expected_quantity), abs(counted_quantity)) > 0
			THEN abs(variance_quantity) / greatest(abs(expected_quantity), abs(counted_quantity)) * 100 ELSE 100 END), 0)::text
			FROM inventory.stock_opname_lines WHERE opname_id = $1 AND over_tolerance`, oid).Scan(&amount, &pct); err != nil {
			return o, err
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'pending_approval' WHERE id = $1`, oid); err != nil {
			return o, err
		}
		var whCode string
		_ = tx.QueryRow(ctx, `SELECT code FROM inventory.warehouses WHERE id = $1`, o.WarehouseID).Scan(&whCode)
		rid, _, err := s.submitApproval(ctx, tx, DocOpname.Code, oid, o.Number, fmt.Sprintf("Stock Opname %s variance (%d line(s) above tolerance)", o.Number,
			o.OverTolerance), property, map[string]any{"amount": dec(amount).InexactFloat64(), "variancePercent": dec(pct).Round(2).InexactFloat64(), "warehouse": whCode})
		if err != nil {
			return o, err
		}
		if rid != uuid.Nil {
			if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET approval_request_id = $2 WHERE id = $1 AND status = 'pending_approval'`, oid, rid); err != nil {
				return o, err
			}
		} else if err := s.postOpname(ctx, tx, property, oid); err != nil {
			return o, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'pending_approval' WHERE id = $1`, oid); err != nil {
			return o, err
		}
		if err := s.postOpname(ctx, tx, property, oid); err != nil {
			return o, err
		}
	}
	after, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.stock_opname", oid, o.Number, "post", map[string]any{"status": o.Status},
		map[string]any{"status": after.Status, "varianceValue": after.VarianceValue}, "")
}

// postOpname posts the variance movement of an opname pending approval.
func (s *Stock) postOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID) error {
	o, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return err
	}
	if o.Status != "pending_approval" {
		return nil
	}
	var lines []PostLine
	for _, l := range o.Lines {
		if l.VarianceQuantity == nil || dec(*l.VarianceQuantity).IsZero() {
			continue
		}
		c := dec(l.UnitCost)
		lines = append(lines, PostLine{ItemID: l.ItemID, Quantity: dec(*l.VarianceQuantity), UnitCost: &c, BatchID: l.BatchID})
	}
	var mid *uuid.UUID
	value := "0"
	if len(lines) > 0 {
		m, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveOpname, SourceType: "opname", SourceID: &oid, WarehouseID: o.WarehouseID,
			Reason: "stock_opname", Notes: "Stock Opname " + o.Number, IgnoreFreeze: true, AllowNegative: true, Lines: lines})
		if err != nil {
			return err
		}
		mid, value = &m.ID, m.TotalCost
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'posted', posted_at = now(), movement_id = $2, variance_value = $3::numeric WHERE id = $1`,
		oid, mid, value); err != nil {
		return err
	}
	if s.Events == nil {
		return nil
	}
	// PRD P4 §11: inventory.opname_posted (the journal comes with the movement's inventory.movement_posted)
	var whCode string
	var bd *string
	if err := tx.QueryRow(ctx, `SELECT w.code, to_char(m.business_date, 'YYYY-MM-DD') FROM inventory.warehouses w
		LEFT JOIN inventory.stock_movements m ON m.id = $2 WHERE w.id = $1`, o.WarehouseID, mid).Scan(&whCode, &bd); err != nil {
		return err
	}
	_, err = s.Events.Publish(ctx, tx, EventOpnamePosted, "inventory.stock_opname", &oid, &property, map[string]any{"opnameId": oid, "number": o.Number,
		"warehouseId": o.WarehouseID, "warehouseCode": whCode, "categoryId": o.CategoryID, "movementId": mid, "businessDate": bd, "varianceValue": value,
		"lines": len(o.Lines), "linesWithVariance": len(lines), "approvalRequestId": o.ApprovalRequestID})
	return err
}

func (s *Stock) OpnameDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.Status {
	case approval.StatusApproved:
		return s.postOpname(ctx, tx, d.PropertyID, d.DocumentID)
	case approval.StatusRejected, approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'counted', approval_request_id = NULL,
			notes = concat_ws(E'\n', notes, 'Variance not approved: ' || coalesce($2, '')) WHERE id = $1 AND status = 'pending_approval'`, d.DocumentID, nullStr(d.Reason))
		return err
	}
	return nil
}

// OpnameRecountInput reopens lines for a recount.
type OpnameRecountInput struct {
	LineIDs []uuid.UUID `json:"lineIds,omitempty" doc:"Default: every line above tolerance"`
}

// RecountOpname reopens lines of a counted opname (Counted → In Progress).
func (s *Stock) RecountOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, in OpnameRecountInput) (StockOpname, error) {
	o, err := lockOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "counted" {
		return o, conflictStatus("Stock opname", o.Number, o.Status, "recounted")
	}
	ids := in.LineIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	tag, err := tx.Exec(ctx, `UPDATE inventory.stock_opname_lines SET counted_quantity = NULL, counted_at = NULL, expected_quantity = NULL, variance_quantity = NULL,
		variance_value = NULL, over_tolerance = false, recounts = recounts + 1 WHERE opname_id = $1
		AND ((cardinality($2::uuid[]) = 0 AND over_tolerance) OR id = ANY($2))`, oid, ids)
	if err != nil {
		return o, err
	}
	if tag.RowsAffected() == 0 {
		return o, errs.Validation("nothing_to_recount", "no line to recount")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'in_progress' WHERE id = $1`, oid); err != nil {
		return o, err
	}
	after, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.stock_opname", oid, o.Number, "recount", map[string]any{"status": o.Status},
		map[string]any{"status": "in_progress", "lines": tag.RowsAffected()}, "")
}

// CancelOpname cancels an open opname.
func (s *Stock) CancelOpname(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, reason string) (StockOpname, error) {
	o, err := lockOpname(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	switch o.Status {
	case "in_progress", "counted":
	case "pending_approval":
		s.withdraw(ctx, tx, o.ApprovalRequestID, reason)
	default:
		return o, conflictStatus("Stock opname", o.Number, o.Status, "cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.stock_opnames SET status = 'cancelled', updated_by = $2 WHERE id = $1`, oid, uuidOrNil(handle.UserID(ctx))); err != nil {
		return o, err
	}
	after, err := GetOpname(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.stock_opname", oid, o.Number, "cancel", map[string]any{"status": o.Status},
		map[string]any{"status": "cancelled"}, reason)
}
