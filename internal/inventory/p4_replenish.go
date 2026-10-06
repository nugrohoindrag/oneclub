package inventory

// PRD P4 EP-08 Replenishment & Automatic PR and FR-PRD-03 expiry alerts:
//
//   - main stores (warehouses without a replenishing store) below their
//     reorder point request a purchase requisition through
//     inventory.reorder_needed for par − stock − on order (FR-RPL-02), once
//     per warehouse × item × business day (re-runs never duplicate);
//   - sub-stores below par get a draft store requisition to their store
//     (FR-RPL-03);
//   - batches expiring within the alert days notify the inventory managers.
//
// The daily jobs run per property; the same logic serves
// POST /inventory/replenishment:run and the read-only suggestions.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
)

// ReplenishmentSuggestion is one replenishment suggestion.
type ReplenishmentSuggestion struct {
	Kind                string     `json:"kind" db:"kind" enum:"purchase,requisition" doc:"purchase: PR to procurement; requisition: store requisition to the replenishing store"`
	WarehouseID         uuid.UUID  `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode       string     `json:"warehouseCode" db:"warehouse_code"`
	SupplyWarehouseID   *uuid.UUID `json:"supplyWarehouseId" db:"supply_warehouse_id"`
	ItemID              uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode            string     `json:"itemCode" db:"item_code"`
	ItemName            string     `json:"itemName" db:"item_name"`
	UOMID               uuid.UUID  `json:"uomId" db:"uom_id"`
	UOM                 string     `json:"uom" db:"uom"`
	OnHand              string     `json:"onHand" db:"on_hand"`
	OnOrder             string     `json:"onOrder" db:"on_order" doc:"Open purchase requests and transfers in transit"`
	ReorderPoint        string     `json:"reorderPoint" db:"reorder_point"`
	ParLevel            string     `json:"parLevel" db:"par_level"`
	MinStock            string     `json:"minStock" db:"min_stock"`
	SuggestedQuantity   string     `json:"suggestedQuantity" db:"suggested_quantity"`
	PreferredSupplierID *uuid.UUID `json:"preferredSupplierId" db:"preferred_supplier_id"`
}

// Suggestions lists the items to replenish (warehouse optional).
func Suggestions(ctx context.Context, q dbtx.Querier, property uuid.UUID, warehouse *uuid.UUID) ([]ReplenishmentSuggestion, error) {
	rows, err := handle.List[ReplenishmentSuggestion](q.Query(ctx, `WITH k AS (
		  SELECT warehouse_id, item_id FROM inventory.reorder_points WHERE property_id = $1
		  UNION SELECT warehouse_id, item_id FROM inventory.par_stocks WHERE property_id = $1),
		s AS (SELECT k.warehouse_id, k.item_id, w.code AS warehouse_code, w.parent_id, i.code AS item_code, i.name AS item_name, i.base_uom_id AS uom_id,
		  u.code AS uom, coalesce((SELECT sum(b.quantity) FROM inventory.stock_balances b WHERE b.warehouse_id = k.warehouse_id AND b.item_id = k.item_id), 0) AS on_hand,
		  coalesce((SELECT sum(r.suggested_quantity - r.received_quantity) FROM inventory.reorder_requests r WHERE r.warehouse_id = k.warehouse_id
		    AND r.item_id = k.item_id AND r.status = 'open'), 0)
		  + coalesce((SELECT sum(l.shipped_quantity) FROM inventory.transfer_lines l JOIN inventory.transfers t ON t.id = l.transfer_id
		    WHERE t.to_warehouse_id = k.warehouse_id AND t.status = 'in_transit' AND l.item_id = k.item_id), 0)
		  + coalesce((SELECT sum(rl.base_quantity - rl.issued_quantity - rl.cancelled_quantity) FROM inventory.requisition_lines rl
		    JOIN inventory.requisitions rq ON rq.id = rl.requisition_id WHERE rq.requesting_warehouse_id = k.warehouse_id AND rl.item_id = k.item_id
		    AND rq.status IN ('draft', 'submitted', 'approved', 'partially_issued')), 0) AS on_order,
		  rp.reorder_point, rp.reorder_quantity, coalesce(ps.par_level, 0) AS par_level, coalesce(ps.min_stock, 0) AS min_stock,
		  coalesce(rp.preferred_supplier_id, i.preferred_supplier_id) AS preferred_supplier_id
		  FROM k JOIN inventory.warehouses w ON w.id = k.warehouse_id JOIN inventory.items i ON i.id = k.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		  LEFT JOIN inventory.reorder_points rp ON rp.warehouse_id = k.warehouse_id AND rp.item_id = k.item_id
		  LEFT JOIN inventory.par_stocks ps ON ps.warehouse_id = k.warehouse_id AND ps.item_id = k.item_id
		  WHERE w.status = 'active' AND w.archived_at IS NULL AND i.status = 'active' AND i.archived_at IS NULL AND NOT i.consignment
		  AND ($2::uuid IS NULL OR k.warehouse_id = $2))
		SELECT CASE WHEN parent_id IS NULL THEN 'purchase' ELSE 'requisition' END AS kind, warehouse_id, warehouse_code, parent_id AS supply_warehouse_id,
		  item_id, item_code, item_name, uom_id, uom, trim_scale(on_hand)::text AS on_hand, trim_scale(on_order)::text AS on_order,
		  trim_scale(coalesce(reorder_point, min_stock))::text AS reorder_point, trim_scale(par_level)::text AS par_level, trim_scale(min_stock)::text AS min_stock,
		  trim_scale(CASE WHEN reorder_quantity IS NOT NULL AND parent_id IS NULL THEN reorder_quantity
		    ELSE greatest(par_level, coalesce(reorder_point, min_stock)) - on_hand - on_order END)::text AS suggested_quantity, preferred_supplier_id
		FROM s WHERE on_hand + on_order < CASE WHEN parent_id IS NULL THEN coalesce(reorder_point, min_stock) ELSE greatest(par_level, coalesce(reorder_point, 0)) END
		AND (CASE WHEN reorder_quantity IS NOT NULL AND parent_id IS NULL THEN reorder_quantity
		    ELSE greatest(par_level, coalesce(reorder_point, min_stock)) - on_hand - on_order END) > 0
		ORDER BY warehouse_code, item_code`, property, warehouse))
	return rows, err
}

// ReplenishmentResult reports a replenishment run.
type ReplenishmentResult struct {
	BusinessDate string                    `json:"businessDate"`
	Purchase     []ReplenishmentSuggestion `json:"purchase" doc:"Requested from procurement now (inventory.reorder_needed)"`
	Requisitions []string                  `json:"requisitions" doc:"Draft store requisitions created for sub-stores"`
	Skipped      int                       `json:"skipped" doc:"Suggestions already requested today"`
}

// Replenish runs the replenishment of a property (job and manual run).
func (s *Stock) Replenish(ctx context.Context, tx pgx.Tx, property uuid.UUID) (ReplenishmentResult, error) {
	ctx = reqctx.WithProperty(ctx, property)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return ReplenishmentResult{}, err
	}
	day := today(ctx, tx)
	res := ReplenishmentResult{BusinessDate: day.Format("2006-01-02"), Purchase: []ReplenishmentSuggestion{}, Requisitions: []string{}}
	sugs, err := Suggestions(ctx, tx, property, nil)
	if err != nil {
		return res, err
	}
	byWh := map[uuid.UUID][]ReplenishmentSuggestion{}
	subStores := map[uuid.UUID][]ReplenishmentSuggestion{}
	for _, sg := range sugs {
		if sg.Kind == "purchase" {
			if !pol.AutoPurchaseRequisition {
				continue
			}
			var rid uuid.UUID
			err := tx.QueryRow(ctx, `INSERT INTO inventory.reorder_requests (id, property_id, warehouse_id, item_id, business_date, on_hand, reorder_point, par_level,
				suggested_quantity) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9::numeric)
				ON CONFLICT (warehouse_id, item_id, business_date) WHERE source = 'reorder' DO NOTHING RETURNING id`,
				id.New(), property, sg.WarehouseID, sg.ItemID, day, sg.OnHand, sg.ReorderPoint, sg.ParLevel, sg.SuggestedQuantity).Scan(&rid)
			if dbtx.IsNoRows(err) {
				res.Skipped++
				continue
			}
			if err != nil {
				return res, err
			}
			byWh[sg.WarehouseID] = append(byWh[sg.WarehouseID], sg)
			res.Purchase = append(res.Purchase, sg)
			continue
		}
		if pol.AutoStoreRequisition && sg.SupplyWarehouseID != nil {
			subStores[sg.WarehouseID] = append(subStores[sg.WarehouseID], sg)
		}
	}
	for wh, list := range byWh {
		var items []map[string]any
		var text []string
		for _, sg := range list {
			items = append(items, map[string]any{"itemId": sg.ItemID, "onHand": sg.OnHand, "reorderPoint": sg.ReorderPoint, "parLevel": sg.ParLevel,
				"suggestedQuantity": sg.SuggestedQuantity, "uomId": sg.UOMID, "preferredSupplierId": sg.PreferredSupplierID, "itemCode": sg.ItemCode,
				"onOrder": sg.OnOrder})
			text = append(text, fmt.Sprintf("- %s %s: %s %s (stock %s)", sg.ItemCode, sg.ItemName, sg.SuggestedQuantity, sg.UOM, sg.OnHand))
		}
		if s.Events != nil {
			whID := wh
			// PRD P4 §11: inventory.stock_low (the notification below stays)
			if _, err := s.Events.Publish(ctx, tx, EventStockLow, "inventory.warehouse", &whID, &property, map[string]any{"warehouseId": wh,
				"warehouseCode": list[0].WarehouseCode, "items": items, "businessDate": res.BusinessDate}); err != nil {
				return res, err
			}
			eid, err := s.Events.Publish(ctx, tx, EventReorderNeeded, "inventory.warehouse", &whID, &property, map[string]any{"warehouseId": wh, "items": items,
				"businessDate": res.BusinessDate, "source": "reorder"})
			if err != nil {
				return res, err
			}
			if _, err := tx.Exec(ctx, `UPDATE inventory.reorder_requests SET event_id = $3 WHERE warehouse_id = $1 AND business_date = $2 AND event_id IS NULL
				AND source = 'reorder'`, wh, day, eid); err != nil {
				return res, err
			}
		}
		if s.Notify != nil {
			users, err := notify.Holders(ctx, tx, property, "inventory.replenishment.view")
			if err != nil {
				return res, err
			}
			if len(users) > 0 {
				p := property
				if err := s.Notify.Send(ctx, tx, notify.Message{Event: "inventory.low_stock", Category: "system", UserIDs: users, PropertyID: &p,
					Link: "/inventory/replenishment", Data: map[string]any{"count": len(list), "warehouse": list[0].WarehouseCode,
						"lines": strings.Join(text, "\n")}}); err != nil {
					return res, err
				}
			}
		}
	}
	for wh, list := range subStores {
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.requisitions WHERE requesting_warehouse_id = $1 AND origin = 'replenishment'
			AND status IN ('draft', 'submitted', 'approved', 'partially_issued'))`, wh).Scan(&open); err != nil {
			return res, err
		}
		if open {
			res.Skipped += len(list)
			continue
		}
		whID := wh
		var lines []StockLineInput
		for _, sg := range list {
			lines = append(lines, StockLineInput{ItemID: sg.ItemID, Quantity: sg.SuggestedQuantity})
		}
		r, err := s.CreateRequisition(ctx, tx, property, StoreRequisitionInput{RequestType: "store", RequestingWarehouseID: &whID, SourceWarehouseID: list[0].SupplyWarehouseID,
			Reason: "Below par stock (replenishment)", Lines: lines}, "replenishment")
		if err != nil {
			return res, err
		}
		res.Requisitions = append(res.Requisitions, r.Number)
	}
	return res, nil
}

// ExpiringBatch is a batch in stock expiring soon.
type ExpiringBatch struct {
	WarehouseID   uuid.UUID `json:"warehouseId" db:"warehouse_id"`
	WarehouseCode string    `json:"warehouseCode" db:"warehouse_code"`
	ItemID        uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode      string    `json:"itemCode" db:"item_code"`
	ItemName      string    `json:"itemName" db:"item_name"`
	UOM           string    `json:"uom" db:"uom"`
	BatchID       uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo       string    `json:"batchNo" db:"batch_no"`
	ExpiryDate    string    `json:"expiryDate" db:"expiry_date"`
	DaysLeft      int       `json:"daysLeft" db:"days_left"`
	Quantity      string    `json:"quantity" db:"quantity"`
	Value         string    `json:"value" db:"value"`
}

// Expiring lists batches in stock expiring within days (expired included).
func Expiring(ctx context.Context, q dbtx.Querier, property uuid.UUID, days int, warehouse *uuid.UUID) ([]ExpiringBatch, error) {
	t := today(ctx, q)
	return handle.List[ExpiringBatch](q.Query(ctx, `SELECT b.warehouse_id, w.code AS warehouse_code, b.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		b.batch_id, bt.batch_no, to_char(bt.expiry_date, 'YYYY-MM-DD') AS expiry_date, (bt.expiry_date - $2::date)::int AS days_left,
		trim_scale(b.quantity)::text AS quantity, trim_scale(b.value)::text AS value
		FROM inventory.stock_balances b JOIN inventory.batches bt ON bt.id = b.batch_id JOIN inventory.items i ON i.id = b.item_id
		JOIN inventory.uoms u ON u.id = i.base_uom_id JOIN inventory.warehouses w ON w.id = b.warehouse_id
		WHERE b.property_id = $1 AND b.quantity > 0 AND bt.expiry_date IS NOT NULL AND bt.expiry_date <= $2::date + $3::int
		AND ($4::uuid IS NULL OR b.warehouse_id = $4) ORDER BY bt.expiry_date, i.code`, property, t, days, warehouse))
}

// ExpiryAlerts notifies the holders of inventory.expiry.view about batches
// expiring within the Inventory Policies alert days (once per batch).
func (s *Stock) ExpiryAlerts(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	ctx = reqctx.WithProperty(ctx, property)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	list, err := Expiring(ctx, tx, property, pol.ExpiryAlertDays, nil)
	if err != nil {
		return 0, err
	}
	day := today(ctx, tx)
	var lines []string
	n := 0
	for _, b := range list {
		tag, err := tx.Exec(ctx, `INSERT INTO inventory.expiry_alerts (property_id, batch_id, warehouse_id, quantity, expiry_date, alerted_on)
			VALUES ($1,$2,$3,$4::numeric,$5::date,$6) ON CONFLICT DO NOTHING`, property, b.BatchID, b.WarehouseID, b.Quantity, b.ExpiryDate, day)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		n++
		lines = append(lines, fmt.Sprintf("- %s %s batch %s at %s: %s %s, expires %s", b.ItemCode, b.ItemName, b.BatchNo, b.WarehouseCode, b.Quantity, b.UOM,
			b.ExpiryDate))
	}
	if n == 0 || s.Notify == nil {
		return n, nil
	}
	users, err := notify.Holders(ctx, tx, property, "inventory.expiry.view")
	if err != nil || len(users) == 0 {
		return n, err
	}
	p := property
	return n, s.Notify.Send(ctx, tx, notify.Message{Event: "inventory.expiry_alert", Category: "system", UserIDs: users, PropertyID: &p,
		Link: "/inventory/expiry", Data: map[string]any{"count": n, "days": pol.ExpiryAlertDays, "lines": strings.Join(lines, "\n")}})
}

// ── jobs ──────────────────────────────────────────────────────────────────

// DailyArgs runs the daily inventory jobs: replenishment (automatic PR),
// expiry alerts, golf cart usage hours, maintenance reminders and the
// monthly depreciation.
type DailyArgs struct{}

func (DailyArgs) Kind() string { return "inventory_daily" }

func (DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 5}
}

// DailyWorker is the River worker of DailyArgs.
type DailyWorker struct {
	river.WorkerDefaults[DailyArgs]
	S *Stock
}

func (w *DailyWorker) Work(ctx context.Context, _ *river.Job[DailyArgs]) error {
	_, err := w.S.RunDaily(ctx)
	return err
}

// DailyReport summarises a daily run.
type DailyReport struct {
	Purchase     int
	Requisitions int
	Expiring     int
	Maintenance  int
	Depreciation int
	// GolfCartUsages are the P2 cart assignments added to asset usage hours.
	GolfCartUsages int
}

// RunDaily runs the daily jobs for every property (exported for tests and ops).
func (s *Stock) RunDaily(ctx context.Context) (DailyReport, error) {
	sys := dbtx.System(ctx)
	var rep DailyReport
	var props []uuid.UUID
	if err := s.DB.WithReadTx(sys, func(tx pgx.Tx) error {
		rows, err := tx.Query(sys, `SELECT id FROM platform.properties ORDER BY created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p uuid.UUID
			if err := rows.Scan(&p); err != nil {
				return err
			}
			props = append(props, p)
		}
		return rows.Err()
	}); err != nil {
		return rep, err
	}
	for _, p := range props {
		err := s.DB.WithTx(sys, func(tx pgx.Tx) error {
			r, err := s.Replenish(sys, tx, p)
			if err != nil {
				return err
			}
			rep.Purchase += len(r.Purchase)
			rep.Requisitions += len(r.Requisitions)
			n, err := s.ExpiryAlerts(sys, tx, p)
			rep.Expiring += n
			if err != nil {
				return err
			}
			g, err := s.GolfCartUsage(sys, tx, p)
			rep.GolfCartUsages += g
			if err != nil {
				return err
			}
			m, err := s.MaintenanceReminders(sys, tx, p)
			rep.Maintenance += m
			if err != nil {
				return err
			}
			d, err := s.AutoDepreciation(sys, tx, p)
			rep.Depreciation += d
			return err
		})
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// RegisterJobs adds the daily inventory job (06:40 instance time).
func (s *Stock) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &DailyWorker{S: s})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 6, Minute: 40, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return DailyArgs{}, nil }, nil))
}

func monthEnd(year int, month time.Month) time.Time {
	return time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
}

func docNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string) (string, error) {
	return numbering.Next(ctx, tx, property, prefix, localNow(ctx, tx))
}

var _ = decimal.Zero
