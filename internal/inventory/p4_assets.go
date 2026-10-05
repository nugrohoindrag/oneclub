package inventory

// PRD P4 EP-09 Asset & Equipment: asset register (resource definition),
// rental equipment out / back (FR-AST-04), usage history and golf cart
// hours from the P2 fleet (FR-AST-03), maintenance work orders with spare
// parts issued from the Engineering store (FR-AST-02/03), spare part
// requests of Golf Staff (FR-OPS-P4-03), straight-line / declining-balance
// depreciation runs with inventory.asset_depreciated (FR-AST-05, decision
// PRD P4 §16 #5) and disposal through approval (FR-AST-06).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

// AssetSummary is the asset view of the actions.
type AssetSummary struct {
	ID                      uuid.UUID  `json:"id" db:"id"`
	Code                    string     `json:"code" db:"code"`
	Name                    string     `json:"name" db:"name"`
	CategoryID              uuid.UUID  `json:"categoryId" db:"category_id"`
	Category                string     `json:"category" db:"category"`
	AssetClass              string     `json:"assetClass" db:"asset_class"`
	Status                  string     `json:"status" db:"status" enum:"active,maintenance,out_of_service,disposed"`
	RentalStatus            string     `json:"rentalStatus" db:"rental_status" enum:"available,out"`
	Rentable                bool       `json:"rentable" db:"rentable"`
	UsageHours              string     `json:"usageHours" db:"usage_hours"`
	AcquisitionCost         string     `json:"acquisitionCost" db:"acquisition_cost"`
	AccumulatedDepreciation string     `json:"accumulatedDepreciation" db:"accumulated_depreciation"`
	BookValue               string     `json:"bookValue" db:"book_value"`
	GolfCartRef             *uuid.UUID `json:"golfCartRef" db:"golf_cart_ref"`
	DisposedOn              *string    `json:"disposedOn" db:"disposed_on"`
	DisposalReason          *string    `json:"disposalReason" db:"disposal_reason"`
	DisposalApprovalID      *uuid.UUID `json:"disposalApprovalId" db:"disposal_approval_id"`
}

const assetSelect = `SELECT a.id, a.code, a.name, a.category_id, c.name AS category, c.asset_class, a.status, a.rental_status, a.rentable,
	trim_scale(a.usage_hours)::text AS usage_hours, trim_scale(a.acquisition_cost)::text AS acquisition_cost,
	trim_scale(a.accumulated_depreciation)::text AS accumulated_depreciation, trim_scale(a.book_value)::text AS book_value, a.golf_cart_ref,
	to_char(a.disposed_on, 'YYYY-MM-DD') AS disposed_on, a.disposal_reason, a.disposal_approval_id
	FROM inventory.assets a JOIN inventory.asset_categories c ON c.id = a.category_id`

// GetAsset returns the asset summary.
func GetAsset(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (AssetSummary, error) {
	rows, err := q.Query(ctx, assetSelect+` WHERE a.id = $1`, aid)
	return handle.One[AssetSummary](rows, err, "asset")
}

func lockAsset(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (AssetSummary, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.assets WHERE id = $1 FOR UPDATE`, aid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return AssetSummary{}, errs.NotFound("asset")
	}
	if err != nil {
		return AssetSummary{}, err
	}
	return GetAsset(ctx, tx, aid)
}

// AssetDisposeInput requests disposal / write-off of an asset.
type AssetDisposeInput struct {
	Reason     string `json:"reason"`
	DisposedOn string `json:"disposedOn,omitempty" doc:"YYYY-MM-DD (default today)"`
	Proceeds   string `json:"proceeds,omitempty" doc:"Sale proceeds (0 = write-off)"`
}

// DisposeAsset submits the disposal for approval (FR-AST-06).
func (s *Stock) DisposeAsset(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in AssetDisposeInput) (AssetSummary, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return AssetSummary{}, err
	}
	a, err := lockAsset(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status == "disposed" {
		return a, errs.Conflict("asset_disposed", "asset "+a.Code+" is already disposed")
	}
	if a.DisposalApprovalID != nil {
		var st string
		if err := tx.QueryRow(ctx, `SELECT status FROM platform.approval_requests WHERE id = $1`, *a.DisposalApprovalID).Scan(&st); err == nil && st == approval.StatusPending {
			return a, errs.Conflict("disposal_pending", "the disposal of "+a.Code+" is waiting for approval")
		}
	}
	on := today(ctx, tx)
	if d, err := optDate("disposedOn", in.DisposedOn); err != nil {
		return a, err
	} else if d != nil {
		on = *d
	}
	proceeds, err := handle.Decimal("proceeds", in.Proceeds, decimal.Zero)
	if err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET disposed_on = $2, disposal_reason = $3, disposal_proceeds = $4::numeric, updated_by = $5 WHERE id = $1`,
		aid, on, in.Reason, proceeds.String(), uuidOrNil(handle.UserID(ctx))); err != nil {
		return a, err
	}
	rid, st, err := s.submitApproval(ctx, tx, DocAssetDisposal.Code, aid, a.Code, "Asset disposal "+a.Code+" "+a.Name+" (book value "+a.BookValue+")", property,
		map[string]any{"amount": dec(a.BookValue).InexactFloat64(), "assetClass": a.AssetClass})
	if err != nil {
		return a, err
	}
	if rid == uuid.Nil && st == approval.StatusApproved {
		if err := s.disposeNow(ctx, tx, property, aid); err != nil {
			return a, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET disposal_approval_id = $2 WHERE id = $1 AND status <> 'disposed'`, aid, rid); err != nil {
		return a, err
	}
	after, err := GetAsset(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.asset", aid, a.Code, "dispose", map[string]any{"status": a.Status},
		map[string]any{"status": after.Status, "disposedOn": on.Format("2006-01-02"), "proceeds": proceeds.String()}, in.Reason)
}

// disposeNow writes the disposal off (approved) and publishes it.
func (s *Stock) disposeNow(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) error {
	var code, name, class, category, assetAcct, accAcct string
	var on time.Time
	var cost, acc, book string
	var proceeds *string
	var reason *string
	err := tx.QueryRow(ctx, `UPDATE inventory.assets a SET status = 'disposed', rental_status = 'available' FROM inventory.asset_categories c
		WHERE a.id = $1 AND c.id = a.category_id AND a.status <> 'disposed'
		RETURNING a.code, a.name, c.asset_class, c.name, coalesce(a.disposed_on, billing.local_date(a.property_id)), a.acquisition_cost::text, a.accumulated_depreciation::text,
		a.book_value::text, a.disposal_proceeds::text, a.disposal_reason, coalesce(c.asset_account, ''), coalesce(c.accumulated_account, '')`, aid).
		Scan(&code, &name, &class, &category, &on, &cost, &acc, &book, &proceeds, &reason, &assetAcct, &accAcct)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Events == nil {
		return nil
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	pr := "0"
	if proceeds != nil {
		pr = *proceeds
	}
	_, err = s.Events.Publish(ctx, tx, EventAssetDisposed, "inventory.asset", &aid, &property, map[string]any{"assetId": aid, "assetCode": code, "name": name,
		"assetClass": class, "category": category, "disposedOn": on.Format("2006-01-02"), "acquisitionCost": dec(cost).String(),
		"accumulatedDepreciation": dec(acc).String(), "bookValue": dec(book).String(), "proceeds": dec(pr).String(), "currency": cfg.Currency, "reason": reason,
		"assetAccount": assetAcct, "accumulatedAccount": accAcct})
	return err
}

func (s *Stock) DisposalDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.Status {
	case approval.StatusApproved:
		return s.disposeNow(ctx, tx, d.PropertyID, d.DocumentID)
	case approval.StatusRejected, approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE inventory.assets SET disposed_on = NULL, disposal_reason = NULL, disposal_proceeds = NULL, disposal_approval_id = NULL
			WHERE id = $1 AND status <> 'disposed'`, d.DocumentID)
		return err
	}
	return nil
}

// AssetRentalInput takes rental equipment out or back (FR-AST-04).
type AssetRentalInput struct {
	CustomerRef string `json:"customerRef,omitempty" doc:"Customer / member / booking reference"`
	Reference   string `json:"reference,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

// CheckoutAsset lends rental equipment.
func (s *Stock) CheckoutAsset(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in AssetRentalInput) (AssetSummary, error) {
	a, err := lockAsset(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if !a.Rentable || a.Status != "active" || a.RentalStatus != "available" {
		return a, errs.Conflict("not_available", "asset "+a.Code+" is not available for rental")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.asset_usages (id, property_id, asset_id, usage_type, started_at, customer_ref, reference, notes, created_by)
		VALUES ($1,$2,$3,'rental',now(),$4,$5,$6,$7)`, id.New(), property, aid, nullStr(in.CustomerRef), nullStr(in.Reference), nullStr(in.Notes),
		uuidOrNil(handle.UserID(ctx))); err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET rental_status = 'out' WHERE id = $1`, aid); err != nil {
		return a, err
	}
	after, err := GetAsset(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.asset", aid, a.Code, "checkout", map[string]any{"rentalStatus": a.RentalStatus},
		map[string]any{"rentalStatus": "out", "customerRef": in.CustomerRef}, "")
}

// ReturnAsset takes rental equipment back.
func (s *Stock) ReturnAsset(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in AssetRentalInput) (AssetSummary, error) {
	a, err := lockAsset(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.RentalStatus != "out" {
		return a, errs.Conflict("not_out", "asset "+a.Code+" is not out")
	}
	var hours string
	if err := tx.QueryRow(ctx, `UPDATE inventory.asset_usages SET ended_at = now(), hours = round((extract(epoch FROM now() - started_at) / 3600)::numeric, 2),
		notes = coalesce($2, notes) WHERE id = (SELECT id FROM inventory.asset_usages WHERE asset_id = $1 AND usage_type = 'rental' AND ended_at IS NULL
		ORDER BY started_at DESC LIMIT 1) RETURNING hours::text`, aid, nullStr(in.Notes)).Scan(&hours); err != nil && !dbtx.IsNoRows(err) {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET rental_status = 'available', usage_hours = usage_hours + coalesce($2::numeric, 0) WHERE id = $1`,
		aid, nullStr(hours)); err != nil {
		return a, err
	}
	after, err := GetAsset(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.asset", aid, a.Code, "return", map[string]any{"rentalStatus": a.RentalStatus},
		map[string]any{"rentalStatus": "available", "hours": hours}, "")
}

// AssetUsageInput records operating hours of an asset (FR-AST-03).
type AssetUsageInput struct {
	Hours     string `json:"hours" doc:"Operating hours"`
	StartedAt string `json:"startedAt,omitempty" doc:"RFC 3339 (default now − hours)"`
	Reference string `json:"reference,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// RecordUsage adds operating hours (usage-based maintenance).
func (s *Stock) RecordUsage(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in AssetUsageInput) (AssetSummary, error) {
	a, err := lockAsset(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status == "disposed" {
		return a, errs.Conflict("asset_disposed", "asset "+a.Code+" is disposed")
	}
	h, err := qty("hours", in.Hours, true)
	if err != nil {
		return a, err
	}
	end := time.Now()
	start := end.Add(-time.Duration(h.Mul(decimal.NewFromInt(3600)).IntPart()) * time.Second)
	if strings.TrimSpace(in.StartedAt) != "" {
		if start, err = time.Parse(time.RFC3339, in.StartedAt); err != nil {
			return a, handle.Invalid("startedAt", "invalid", "startedAt must be RFC 3339")
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.asset_usages (id, property_id, asset_id, usage_type, started_at, ended_at, hours, reference, notes, created_by)
		VALUES ($1,$2,$3,'operation',$4,$5,$6::numeric,$7,$8,$9)`, id.New(), property, aid, start, end, h.String(), nullStr(in.Reference), nullStr(in.Notes),
		uuidOrNil(handle.UserID(ctx))); err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET usage_hours = usage_hours + $2::numeric WHERE id = $1`, aid, h.String()); err != nil {
		return a, err
	}
	after, err := GetAsset(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.asset", aid, a.Code, "record_usage", nil, map[string]any{"hours": h.String()}, "")
}

// GolfCartUsage adds the hours of the returned P2 golf cart assignments
// (reporting.golf_cart_usage) to the usage hours of the linked assets
// (golfCartRef), so usage-based maintenance schedules follow the fleet's
// real use (FR-AST-02). One usage record per assignment (idempotent); run
// by the daily inventory job before the maintenance reminders.
func (s *Stock) GolfCartUsage(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	ctx = reqctx.WithProperty(ctx, property)
	rows, err := tx.Query(ctx, `SELECT a.id, u.assignment_id, u.out_at, u.returned_at, round(u.minutes_out::numeric / 60, 2)::text, u.golf_cart_code
		FROM inventory.assets a JOIN reporting.golf_cart_usage u ON u.golf_cart_id = a.golf_cart_ref
		WHERE a.property_id = $1 AND a.status <> 'disposed' AND u.status = 'returned' AND u.minutes_out > 0
		AND NOT EXISTS (SELECT 1 FROM inventory.asset_usages x WHERE x.asset_id = a.id AND x.usage_type = 'golf_cart' AND x.reference = u.assignment_id::text)
		ORDER BY u.returned_at`, property)
	if err != nil {
		return 0, err
	}
	type use struct {
		asset, assignment uuid.UUID
		out, back         time.Time
		hours, cart       string
	}
	var list []use
	for rows.Next() {
		var u use
		if err := rows.Scan(&u.asset, &u.assignment, &u.out, &u.back, &u.hours, &u.cart); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, u := range list {
		tag, err := tx.Exec(ctx, `INSERT INTO inventory.asset_usages (id, property_id, asset_id, usage_type, started_at, ended_at, hours, reference, notes)
			VALUES ($1,$2,$3,'golf_cart',$4,$5,$6::numeric,$7,$8) ON CONFLICT (asset_id, reference) WHERE usage_type = 'golf_cart' DO NOTHING`,
			id.New(), property, u.asset, u.out, u.back, u.hours, u.assignment.String(), "Golf cart "+u.cart+" assignment (P2)")
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET usage_hours = usage_hours + $2::numeric WHERE id = $1`, u.asset, u.hours); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// AssetHistory is the usage, maintenance, spare part and depreciation
// history of an asset.
type AssetHistory struct {
	Asset         AssetSummary             `json:"asset"`
	GolfCartHours string                   `json:"golfCartHours" doc:"Hours out of the linked P2 golf cart"`
	Usages        []AssetUsage             `json:"usages"`
	Maintenance   []AssetMaintenanceRecord `json:"maintenance"`
	SpareParts    []StockMovementLine      `json:"spareParts"`
	Depreciation  []AssetDepreciationLine  `json:"depreciation"`
	Schedules     []AssetMaintenanceDue    `json:"schedules"`
}

// AssetUsage is one usage record.
type AssetUsage struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	UsageType   string     `json:"usageType" db:"usage_type" enum:"rental,operation,golf_cart"`
	StartedAt   time.Time  `json:"startedAt" db:"started_at"`
	EndedAt     *time.Time `json:"endedAt" db:"ended_at"`
	Hours       *string    `json:"hours" db:"hours"`
	CustomerRef *string    `json:"customerRef" db:"customer_ref"`
	Reference   *string    `json:"reference" db:"reference"`
	Notes       *string    `json:"notes" db:"notes"`
}

// AssetMaintenanceDue is a schedule with its due state.
type AssetMaintenanceDue struct {
	ID            uuid.UUID `json:"id" db:"id"`
	AssetID       uuid.UUID `json:"assetId" db:"asset_id"`
	AssetCode     string    `json:"assetCode" db:"asset_code"`
	AssetName     string    `json:"assetName" db:"asset_name"`
	Name          string    `json:"name" db:"name"`
	TriggerType   string    `json:"triggerType" db:"trigger_type"`
	NextDueOn     *string   `json:"nextDueOn" db:"next_due_on"`
	HoursSince    string    `json:"hoursSince" db:"hours_since"`
	IntervalHours *string   `json:"intervalHours" db:"interval_hours"`
	Due           bool      `json:"due" db:"due"`
}

// GetAssetHistory returns the history of an asset.
func GetAssetHistory(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (AssetHistory, error) {
	a, err := GetAsset(ctx, q, aid)
	if err != nil {
		return AssetHistory{}, err
	}
	h := AssetHistory{Asset: a, GolfCartHours: "0"}
	if a.GolfCartRef != nil {
		if err := q.QueryRow(ctx, `SELECT trim_scale(round(coalesce(sum(minutes_out), 0)::numeric / 60, 2))::text FROM reporting.golf_cart_usage
			WHERE golf_cart_id = $1`, *a.GolfCartRef).Scan(&h.GolfCartHours); err != nil {
			return h, err
		}
	}
	if h.Usages, err = handle.List[AssetUsage](q.Query(ctx, `SELECT id, usage_type, started_at, ended_at, trim_scale(hours)::text AS hours, customer_ref, reference, notes
		FROM inventory.asset_usages WHERE asset_id = $1 ORDER BY started_at DESC LIMIT 200`, aid)); err != nil {
		return h, err
	}
	if h.Maintenance, err = handle.List[AssetMaintenanceRecord](q.Query(ctx, maintenanceSelect+` WHERE r.asset_id = $1 ORDER BY r.created_at DESC LIMIT 200`, aid)); err != nil {
		return h, err
	}
	if h.SpareParts, err = handle.List[StockMovementLine](q.Query(ctx, movementLineSelect+` JOIN inventory.stock_movements mm ON mm.id = l.movement_id
		WHERE mm.asset_id = $1 ORDER BY l.seq DESC LIMIT 200`, aid)); err != nil {
		return h, err
	}
	if h.Depreciation, err = handle.List[AssetDepreciationLine](q.Query(ctx, depreciationLineSelect+` WHERE d.asset_id = $1 ORDER BY r.period DESC`, aid)); err != nil {
		return h, err
	}
	h.Schedules, err = MaintenanceDueList(ctx, q, uuid.Nil, &aid, 0)
	return h, err
}

// MaintenanceDueList lists active schedules with their due state (asset
// optional); within days of the due date counts as due.
func MaintenanceDueList(ctx context.Context, q dbtx.Querier, property uuid.UUID, asset *uuid.UUID, days int) ([]AssetMaintenanceDue, error) {
	t := today(ctx, q)
	return handle.List[AssetMaintenanceDue](q.Query(ctx, `SELECT s.id, s.asset_id, a.code AS asset_code, a.name AS asset_name, s.name, s.trigger_type,
		to_char(s.next_due_on, 'YYYY-MM-DD') AS next_due_on, trim_scale(a.usage_hours - s.last_done_hours)::text AS hours_since,
		trim_scale(s.interval_hours)::text AS interval_hours,
		CASE WHEN s.trigger_type = 'time' THEN s.next_due_on IS NOT NULL AND s.next_due_on <= $2::date + greatest($4::int, s.reminder_days_before)
		  ELSE s.interval_hours IS NOT NULL AND a.usage_hours - s.last_done_hours >= s.interval_hours END AS due
		FROM inventory.maintenance_schedules s JOIN inventory.assets a ON a.id = s.asset_id
		WHERE s.status = 'active' AND a.status <> 'disposed' AND ($1::uuid = '00000000-0000-0000-0000-000000000000' OR s.property_id = $1)
		AND ($3::uuid IS NULL OR s.asset_id = $3) ORDER BY s.next_due_on NULLS LAST, a.code`, property, t, asset, days))
}

// MaintenanceReminders notifies the holders of inventory.maintenance.view of
// due schedules, once per due date.
func (s *Stock) MaintenanceReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	ctx = reqctx.WithProperty(ctx, property)
	list, err := MaintenanceDueList(ctx, tx, property, nil, 0)
	if err != nil {
		return 0, err
	}
	day := today(ctx, tx)
	n := 0
	var users []uuid.UUID
	for _, m := range list {
		if !m.Due {
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE inventory.maintenance_schedules SET last_reminded_on = $2 WHERE id = $1
			AND (last_reminded_on IS NULL OR last_reminded_on < coalesce(next_due_on, $2) - reminder_days_before OR
			  (trigger_type = 'usage_hours' AND last_reminded_on < $2 - 7))`, m.ID, day)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		n++
		if s.Events != nil { // PRD P4 §11: inventory.asset_maintenance_due (the notification stays)
			sid, p := m.ID, property
			if _, err := s.Events.Publish(ctx, tx, EventMaintenanceDue, "inventory.maintenance_schedule", &sid, &p, map[string]any{"scheduleId": m.ID,
				"assetId": m.AssetID, "assetCode": m.AssetCode, "assetName": m.AssetName, "schedule": m.Name, "triggerType": m.TriggerType,
				"nextDueOn": m.NextDueOn, "hoursSince": m.HoursSince, "intervalHours": m.IntervalHours, "businessDate": day.Format("2006-01-02")}); err != nil {
				return n, err
			}
		}
		if s.Notify == nil {
			continue
		}
		if users == nil {
			if users, err = notify.Holders(ctx, tx, property, "inventory.maintenance.view"); err != nil {
				return n, err
			}
		}
		if len(users) == 0 {
			continue
		}
		due := "now"
		if m.NextDueOn != nil {
			due = *m.NextDueOn
		}
		p := property
		if err := s.Notify.Send(ctx, tx, notify.Message{Event: "inventory.maintenance_due", Category: "system", UserIDs: users, PropertyID: &p,
			Link: "/inventory/assets/" + m.AssetID.String(), Data: map[string]any{"asset": m.AssetCode + " " + m.AssetName, "schedule": m.Name, "dueOn": due}}); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ── maintenance work orders ───────────────────────────────────────────────

// AssetMaintenanceInput opens a maintenance work order.
type AssetMaintenanceInput struct {
	AssetID         uuid.UUID  `json:"assetId"`
	ScheduleID      *uuid.UUID `json:"scheduleId,omitempty"`
	MaintenanceType string     `json:"maintenanceType,omitempty" enum:"preventive,corrective,inspection"`
	Description     string     `json:"description"`
	ScheduledOn     string     `json:"scheduledOn,omitempty" doc:"YYYY-MM-DD"`
	Notes           string     `json:"notes,omitempty"`
}

// AssetMaintenanceRecord is a maintenance work order.
type AssetMaintenanceRecord struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	Number          string     `json:"number" db:"number"`
	AssetID         uuid.UUID  `json:"assetId" db:"asset_id"`
	AssetCode       string     `json:"assetCode" db:"asset_code"`
	AssetName       string     `json:"assetName" db:"asset_name"`
	ScheduleID      *uuid.UUID `json:"scheduleId" db:"schedule_id"`
	MaintenanceType string     `json:"maintenanceType" db:"maintenance_type" enum:"preventive,corrective,inspection"`
	Description     string     `json:"description" db:"description"`
	Status          string     `json:"status" db:"status" enum:"open,in_progress,completed,cancelled"`
	ScheduledOn     *string    `json:"scheduledOn" db:"scheduled_on"`
	StartedAt       *time.Time `json:"startedAt" db:"started_at"`
	CompletedAt     *time.Time `json:"completedAt" db:"completed_at"`
	UsageHoursAt    *string    `json:"usageHoursAt" db:"usage_hours_at"`
	LaborCost       string     `json:"laborCost" db:"labor_cost"`
	VendorCost      string     `json:"vendorCost" db:"vendor_cost"`
	PartsCost       string     `json:"partsCost" db:"parts_cost"`
	TotalCost       string     `json:"totalCost" db:"total_cost"`
	RequisitionID   *uuid.UUID `json:"requisitionId" db:"requisition_id"`
	Notes           *string    `json:"notes" db:"notes"`
	CreatedAt       time.Time  `json:"createdAt" db:"created_at"`
}

const maintenanceSelect = `SELECT r.id, r.number, r.asset_id, a.code AS asset_code, a.name AS asset_name, r.schedule_id, r.maintenance_type, r.description, r.status,
	to_char(r.scheduled_on, 'YYYY-MM-DD') AS scheduled_on, r.started_at, r.completed_at, trim_scale(r.usage_hours_at)::text AS usage_hours_at,
	trim_scale(r.labor_cost)::text AS labor_cost, trim_scale(r.vendor_cost)::text AS vendor_cost, trim_scale(r.parts_cost)::text AS parts_cost,
	trim_scale(r.labor_cost + r.vendor_cost + r.parts_cost)::text AS total_cost, r.requisition_id, r.notes, r.created_at
	FROM inventory.maintenance_records r JOIN inventory.assets a ON a.id = r.asset_id`

// GetMaintenance returns a work order.
func GetMaintenance(ctx context.Context, q dbtx.Querier, mid uuid.UUID) (AssetMaintenanceRecord, error) {
	rows, err := q.Query(ctx, maintenanceSelect+` WHERE r.id = $1`, mid)
	return handle.One[AssetMaintenanceRecord](rows, err, "maintenance record")
}

func lockMaintenance(ctx context.Context, tx pgx.Tx, mid uuid.UUID) (AssetMaintenanceRecord, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.maintenance_records WHERE id = $1 FOR UPDATE`, mid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return AssetMaintenanceRecord{}, errs.NotFound("maintenance record")
	}
	if err != nil {
		return AssetMaintenanceRecord{}, err
	}
	return GetMaintenance(ctx, tx, mid)
}

// CreateMaintenance opens a work order.
func (s *Stock) CreateMaintenance(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AssetMaintenanceInput) (AssetMaintenanceRecord, error) {
	if err := handle.Required("description", in.Description); err != nil {
		return AssetMaintenanceRecord{}, err
	}
	a, err := GetAsset(ctx, tx, in.AssetID)
	if err != nil {
		return AssetMaintenanceRecord{}, handle.Invalid("assetId", "not_found", "asset not found")
	}
	if a.Status == "disposed" {
		return AssetMaintenanceRecord{}, errs.Conflict("asset_disposed", "asset "+a.Code+" is disposed")
	}
	if in.MaintenanceType == "" {
		in.MaintenanceType = "corrective"
		if in.ScheduleID != nil {
			in.MaintenanceType = "preventive"
		}
	}
	if !contains([]string{"preventive", "corrective", "inspection"}, in.MaintenanceType) {
		return AssetMaintenanceRecord{}, handle.Invalid("maintenanceType", "invalid", "preventive, corrective or inspection")
	}
	sched, err := optDate("scheduledOn", in.ScheduledOn)
	if err != nil {
		return AssetMaintenanceRecord{}, err
	}
	mid := id.New()
	number, err := docNumber(ctx, tx, property, "MWO")
	if err != nil {
		return AssetMaintenanceRecord{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.maintenance_records (id, property_id, number, asset_id, schedule_id, maintenance_type, description, scheduled_on,
		notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, mid, property, number, in.AssetID, in.ScheduleID, in.MaintenanceType,
		in.Description, sched, nullStr(in.Notes), uid); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return AssetMaintenanceRecord{}, handle.Invalid("scheduleId", "not_found", "maintenance schedule not found")
		}
		return AssetMaintenanceRecord{}, err
	}
	r, err := GetMaintenance(ctx, tx, mid)
	if err != nil {
		return r, err
	}
	return r, record(ctx, tx, property, "inventory.maintenance_record", mid, number, "create", nil, r, "")
}

// StartMaintenance takes the asset into maintenance.
func (s *Stock) StartMaintenance(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID) (AssetMaintenanceRecord, error) {
	r, err := lockMaintenance(ctx, tx, mid)
	if err != nil {
		return r, err
	}
	if r.Status != "open" {
		return r, conflictStatus("Maintenance", r.Number, r.Status, "started")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_records SET status = 'in_progress', started_at = now(),
		usage_hours_at = (SELECT usage_hours FROM inventory.assets WHERE id = $2) WHERE id = $1`, mid, r.AssetID); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET status = 'maintenance' WHERE id = $1 AND status = 'active'`, r.AssetID); err != nil {
		return r, err
	}
	after, err := GetMaintenance(ctx, tx, mid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.maintenance_record", mid, r.Number, "start", map[string]any{"status": r.Status},
		map[string]any{"status": "in_progress"}, "")
}

// CompleteMaintenanceInput closes a work order with costs and spare parts.
type AssetMaintenanceCompleteInput struct {
	LaborCost   string           `json:"laborCost,omitempty"`
	VendorCost  string           `json:"vendorCost,omitempty"`
	WarehouseID *uuid.UUID       `json:"warehouseId,omitempty" doc:"Store issuing the spare parts (default Inventory Configuration spare part warehouse)"`
	SpareParts  []StockLineInput `json:"spareParts,omitempty" doc:"Spare parts used: issued from stock to the asset"`
	Notes       string           `json:"notes,omitempty"`
}

// CompleteMaintenance issues the spare parts, records the costs, moves the
// schedule forward and returns the asset to service.
func (s *Stock) CompleteMaintenance(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, in AssetMaintenanceCompleteInput) (AssetMaintenanceRecord, error) {
	r, err := lockMaintenance(ctx, tx, mid)
	if err != nil {
		return r, err
	}
	if r.Status != "open" && r.Status != "in_progress" {
		return r, conflictStatus("Maintenance", r.Number, r.Status, "completed")
	}
	labor, err := handle.Decimal("laborCost", in.LaborCost, decimal.Zero)
	if err != nil {
		return r, err
	}
	vendor, err := handle.Decimal("vendorCost", in.VendorCost, decimal.Zero)
	if err != nil {
		return r, err
	}
	var reqID *uuid.UUID
	if len(in.SpareParts) > 0 {
		wh := in.WarehouseID
		if wh == nil {
			cfg, err := LoadConfiguration(ctx, tx, property)
			if err != nil {
				return r, err
			}
			if wh, err = warehouseByCode(ctx, tx, property, cfg.SparePartWarehouse); err != nil {
				return r, err
			}
			if wh == nil {
				return r, handle.Invalid("warehouseId", "required", "no spare part warehouse configured")
			}
		}
		aid := r.AssetID
		req, err := s.Issue(ctx, tx, property, StockIssueInput{WarehouseID: *wh, CostCenter: "engineering", AssetID: &aid, Reason: "Maintenance " + r.Number,
			Lines: in.SpareParts}, "maintenance")
		if err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET request_type = 'spare_part', maintenance_record_id = $2 WHERE id = $1`, req.ID, mid); err != nil {
			return r, err
		}
		parts := decimal.Zero
		for _, x := range req.Issues {
			parts = parts.Add(dec(x.TotalCost))
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_records SET parts_cost = parts_cost + $2::numeric WHERE id = $1`, mid, parts.String()); err != nil {
			return r, err
		}
		reqID = &req.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_records SET status = 'completed', completed_at = now(), labor_cost = $2::numeric, vendor_cost = $3::numeric,
		requisition_id = coalesce($4, requisition_id), notes = coalesce($5, notes), started_at = coalesce(started_at, now()),
		usage_hours_at = coalesce(usage_hours_at, (SELECT usage_hours FROM inventory.assets WHERE id = $6)) WHERE id = $1`,
		mid, labor.String(), vendor.String(), reqID, nullStr(in.Notes), r.AssetID); err != nil {
		return r, err
	}
	day := today(ctx, tx)
	if r.ScheduleID != nil {
		if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_schedules s SET last_done_on = $2, last_done_hours = a.usage_hours,
			next_due_on = CASE WHEN s.interval_days IS NOT NULL THEN $2::date + s.interval_days ELSE s.next_due_on END, last_reminded_on = NULL
			FROM inventory.assets a WHERE s.id = $1 AND a.id = s.asset_id`, *r.ScheduleID, day); err != nil {
			return r, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET status = 'active' WHERE id = $1 AND status = 'maintenance'`, r.AssetID); err != nil {
		return r, err
	}
	after, err := GetMaintenance(ctx, tx, mid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.maintenance_record", mid, r.Number, "complete", map[string]any{"status": r.Status},
		map[string]any{"status": "completed", "totalCost": after.TotalCost}, in.Notes)
}

// CancelMaintenance cancels an open work order.
func (s *Stock) CancelMaintenance(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, reason string) (AssetMaintenanceRecord, error) {
	r, err := lockMaintenance(ctx, tx, mid)
	if err != nil {
		return r, err
	}
	if r.Status != "open" && r.Status != "in_progress" {
		return r, conflictStatus("Maintenance", r.Number, r.Status, "cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_records SET status = 'cancelled', notes = coalesce($2, notes) WHERE id = $1`, mid, nullStr(reason)); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET status = 'active' WHERE id = $1 AND status = 'maintenance'`, r.AssetID); err != nil {
		return r, err
	}
	after, err := GetMaintenance(ctx, tx, mid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.maintenance_record", mid, r.Number, "cancel", map[string]any{"status": r.Status},
		map[string]any{"status": "cancelled"}, reason)
}

// SparePartRequestInput is the Spare Part Request of Golf Staff (`ops`).
type SparePartRequestInput struct {
	AssetID             uuid.UUID        `json:"assetId"`
	MaintenanceRecordID *uuid.UUID       `json:"maintenanceRecordId,omitempty"`
	WarehouseID         *uuid.UUID       `json:"warehouseId,omitempty" doc:"Default: the spare part warehouse (Engineering)"`
	NeededBy            string           `json:"neededBy,omitempty"`
	Notes               string           `json:"notes,omitempty"`
	Lines               []StockLineInput `json:"lines"`
}

// RequestSpareParts submits a spare part requisition for an asset.
func (s *Stock) RequestSpareParts(ctx context.Context, tx pgx.Tx, property uuid.UUID, in SparePartRequestInput) (StoreRequisition, error) {
	aid := in.AssetID
	return s.CreateRequisition(ctx, tx, property, StoreRequisitionInput{RequestType: "spare_part", SourceWarehouseID: in.WarehouseID, AssetID: &aid,
		MaintenanceRecordID: in.MaintenanceRecordID, NeededBy: in.NeededBy, Notes: in.Notes, Reason: "Spare part request", Submit: true, Lines: in.Lines}, "ops")
}

// ── depreciation (FR-AST-05) ──────────────────────────────────────────────

// DepreciationInput runs the depreciation of a month.
type DepreciationRunInput struct {
	Period string `json:"period" doc:"YYYY-MM"`
}

// AssetDepreciationRun is a posted monthly depreciation.
type AssetDepreciationRun struct {
	ID       uuid.UUID               `json:"id" db:"id"`
	Number   string                  `json:"number" db:"number"`
	Period   string                  `json:"period" db:"period"`
	Currency string                  `json:"currency" db:"currency"`
	Total    string                  `json:"total" db:"total"`
	Assets   int                     `json:"assets" db:"assets"`
	Status   string                  `json:"status" db:"status" enum:"posted"`
	PostedAt time.Time               `json:"postedAt" db:"posted_at"`
	Lines    []AssetDepreciationLine `json:"lines,omitempty" db:"-"`
}

// AssetDepreciationLine is the depreciation of one asset in a run.
type AssetDepreciationLine struct {
	RunID            uuid.UUID `json:"runId" db:"run_id"`
	Period           string    `json:"period" db:"period"`
	AssetID          uuid.UUID `json:"assetId" db:"asset_id"`
	AssetCode        string    `json:"assetCode" db:"asset_code"`
	AssetName        string    `json:"assetName" db:"asset_name"`
	Category         string    `json:"category" db:"category"`
	Amount           string    `json:"amount" db:"amount"`
	AccumulatedAfter string    `json:"accumulatedAfter" db:"accumulated_after"`
	BookValueAfter   string    `json:"bookValueAfter" db:"book_value_after"`
}

const depreciationLineSelect = `SELECT d.run_id, r.period, d.asset_id, a.code AS asset_code, a.name AS asset_name, c.name AS category,
	trim_scale(d.amount)::text AS amount, trim_scale(d.accumulated_after)::text AS accumulated_after, trim_scale(d.book_value_after)::text AS book_value_after
	FROM inventory.depreciation_lines d JOIN inventory.depreciation_runs r ON r.id = d.run_id JOIN inventory.assets a ON a.id = d.asset_id
	JOIN inventory.asset_categories c ON c.id = a.category_id`

// GetDepreciationRun returns a run with its lines.
func GetDepreciationRun(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (AssetDepreciationRun, error) {
	rows, err := q.Query(ctx, `SELECT id, number, period, currency, trim_scale(total)::text AS total, assets, status, posted_at FROM inventory.depreciation_runs
		WHERE id = $1`, rid)
	r, err := handle.One[AssetDepreciationRun](rows, err, "depreciation run")
	if err != nil {
		return r, err
	}
	r.Lines, err = handle.List[AssetDepreciationLine](q.Query(ctx, depreciationLineSelect+` WHERE d.run_id = $1 ORDER BY a.code`, rid))
	return r, err
}

// RunDepreciation posts the depreciation of a month for every active asset
// (straight line by default, declining balance configurable) and publishes
// inventory.asset_depreciated for the journal.
func (s *Stock) RunDepreciation(ctx context.Context, tx pgx.Tx, property uuid.UUID, period string) (AssetDepreciationRun, error) {
	pt, err := time.Parse("2006-01", strings.TrimSpace(period))
	if err != nil {
		return AssetDepreciationRun{}, handle.Invalid("period", "invalid", "period must be YYYY-MM")
	}
	period = pt.Format("2006-01")
	end := monthEnd(pt.Year(), pt.Month())
	if end.After(today(ctx, tx).AddDate(0, 1, 0)) {
		return AssetDepreciationRun{}, handle.Invalid("period", "future", "the period has not started")
	}
	if st, err := s.periodStatus(ctx, tx, property, end); err != nil {
		return AssetDepreciationRun{}, err
	} else if st == "closed" {
		return AssetDepreciationRun{}, errs.Validation("period_closed", "the accounting period "+period+" is closed")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.depreciation_runs WHERE property_id = $1 AND period = $2)`, property, period).Scan(&exists); err != nil {
		return AssetDepreciationRun{}, err
	}
	if exists {
		return AssetDepreciationRun{}, errs.Conflict("already_posted", "the depreciation of "+period+" is already posted")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return AssetDepreciationRun{}, err
	}
	type asset struct {
		id                        uuid.UUID
		code, category            string
		cost, residual, acc, rate decimal.Decimal
		life                      int
		method                    string
		expenseAcct, accAcct      string // ledger accounts of the asset category (FR-AST-05)
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.code, c.name, a.acquisition_cost::text,
		(CASE WHEN a.residual_value > 0 THEN a.residual_value ELSE round(a.acquisition_cost * c.residual_percent / 100, 4) END)::text,
		a.accumulated_depreciation::text, coalesce(c.declining_rate_percent, 0)::text, coalesce(a.useful_life_months, c.useful_life_months),
		coalesce(a.depreciation_method, c.depreciation_method), coalesce(c.expense_account, ''), coalesce(c.accumulated_account, '')
		FROM inventory.assets a JOIN inventory.asset_categories c ON c.id = a.category_id
		WHERE a.property_id = $1 AND a.archived_at IS NULL AND a.acquisition_cost > 0
		AND (a.status <> 'disposed' OR a.disposed_on > $2::date)
		AND coalesce(a.depreciation_start, a.acquisition_date) <= $2::date
		AND NOT EXISTS (SELECT 1 FROM inventory.depreciation_lines d JOIN inventory.depreciation_runs r ON r.id = d.run_id WHERE d.asset_id = a.id AND r.period = $3)
		ORDER BY a.code FOR UPDATE OF a`, property, end, period)
	if err != nil {
		return AssetDepreciationRun{}, err
	}
	var list []asset
	for rows.Next() {
		var x asset
		var cost, res, acc, rate string
		if err := rows.Scan(&x.id, &x.code, &x.category, &cost, &res, &acc, &rate, &x.life, &x.method, &x.expenseAcct, &x.accAcct); err != nil {
			rows.Close()
			return AssetDepreciationRun{}, err
		}
		x.cost, x.residual, x.acc, x.rate = dec(cost), dec(res), dec(acc), dec(rate)
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AssetDepreciationRun{}, err
	}
	type line struct {
		a           asset
		amount, acc decimal.Decimal
	}
	var computed []line
	total := decimal.Zero
	var lines []map[string]any
	for _, a := range list {
		base := a.cost.Sub(a.residual)
		left := base.Sub(a.acc)
		if !left.IsPositive() {
			continue
		}
		var amount decimal.Decimal
		if a.method == "declining_balance" {
			rate := a.rate
			if !rate.IsPositive() {
				rate = decimal.NewFromInt(200).Div(decimal.NewFromInt(int64(max(a.life, 12))).Div(decimal.NewFromInt(12))) // double declining
			}
			amount = a.cost.Sub(a.acc).Mul(rate).Div(decimal.NewFromInt(1200)).Round(2)
		} else {
			amount = base.Div(decimal.NewFromInt(int64(a.life))).Round(2)
		}
		amount = decimal.Min(amount, left)
		if !amount.IsPositive() {
			continue
		}
		computed = append(computed, line{a, amount, a.acc.Add(amount)})
		total = total.Add(amount)
		lines = append(lines, map[string]any{"assetId": a.id, "assetCode": a.code, "category": a.category, "amount": amount.String(),
			"expenseAccount": a.expenseAcct, "accumulatedAccount": a.accAcct})
	}
	if len(computed) == 0 {
		return AssetDepreciationRun{}, errs.Validation("nothing_to_depreciate", "no asset to depreciate in "+period)
	}
	rid := id.New()
	number, err := docNumber(ctx, tx, property, "DEP")
	if err != nil {
		return AssetDepreciationRun{}, err
	}
	// The run and its lines are append-only (like journals).
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.depreciation_runs (id, property_id, number, period, period_end, currency, total, assets, posted_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9)`, rid, property, number, period, end, cfg.Currency, total.String(), len(computed),
		uuidOrNil(handle.UserID(ctx))); err != nil {
		return AssetDepreciationRun{}, err
	}
	for _, l := range computed {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.depreciation_lines (id, property_id, run_id, asset_id, amount, accumulated_after, book_value_after)
			VALUES ($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric)`, id.New(), property, rid, l.a.id, l.amount.String(), l.acc.String(),
			l.a.cost.Sub(l.acc).String()); err != nil {
			return AssetDepreciationRun{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.assets SET accumulated_depreciation = $2::numeric WHERE id = $1`, l.a.id, l.acc.String()); err != nil {
			return AssetDepreciationRun{}, err
		}
	}
	if s.Events != nil {
		if _, err := s.Events.Publish(ctx, tx, EventAssetDepreciated, "inventory.depreciation_run", &rid, &property, map[string]any{"runId": rid, "number": number,
			"period": period, "periodEnd": end.Format("2006-01-02"), "lines": lines, "total": total.String(), "currency": cfg.Currency}); err != nil {
			return AssetDepreciationRun{}, err
		}
	}
	return GetDepreciationRun(ctx, tx, rid)
}

// AutoDepreciation posts the previous month's depreciation when the
// Inventory Configuration asks for it (daily job; idempotent).
func (s *Stock) AutoDepreciation(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	ctx = reqctx.WithProperty(ctx, property)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil || !cfg.AutoDepreciation {
		return 0, err
	}
	prev := today(ctx, tx).AddDate(0, -1, 0).Format("2006-01")
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.depreciation_runs WHERE property_id = $1 AND period = $2)`, property, prev).Scan(&exists); err != nil {
		return 0, err
	}
	if exists {
		return 0, nil
	}
	var any bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.assets WHERE property_id = $1 AND acquisition_cost > 0 AND status <> 'disposed'
		AND archived_at IS NULL)`, property).Scan(&any); err != nil || !any {
		return 0, err
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, err
	}
	r, err := s.RunDepreciation(ctx, sp, property, prev)
	if err != nil {
		_ = sp.Rollback(ctx)
		if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
			return 0, nil // closed period or nothing to do
		}
		return 0, err
	}
	return r.Assets, sp.Commit(ctx)
}

var _ = fmt.Sprint
