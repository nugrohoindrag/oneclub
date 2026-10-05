package inventory

// PRD P4 EP-03 Store Requisition & Stock Transfer, EP-04 Stock Adjustment,
// EP-07 Production & Waste and FR-STK-03 Issue Stock: the documents that
// move stock. Each action posts through Stock.Post (append-only ledger) and
// is audited in the same transaction.

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
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// StockLineInput is a document line (quantity in uomId or the item's stock UOM).
type StockLineInput struct {
	ItemID     uuid.UUID  `json:"itemId"`
	Quantity   string     `json:"quantity" doc:"Decimal in uomId or the item's stock UOM (adjustments: signed)"`
	UOMID      *uuid.UUID `json:"uomId,omitempty"`
	BatchNo    string     `json:"batchNo,omitempty"`
	ExpiryDate string     `json:"expiryDate,omitempty" doc:"YYYY-MM-DD of an incoming batch"`
	SerialNos  []string   `json:"serialNos,omitempty"`
	UnitCost   string     `json:"unitCost,omitempty" doc:"Incoming stock: cost per stock UOM (empty = current cost)"`
	Notes      string     `json:"notes,omitempty"`
}

// StockReasonInput carries the reason of an action.
type StockReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

// baseLines converts entered lines into posting lines (stock UOM).
func (s *Stock) baseLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, lines []StockLineInput, sign int, positive bool) ([]PostLine, error) {
	if len(lines) == 0 {
		return nil, errs.Validation("lines_required", "at least one line is required", errs.Field("lines", "required", "at least one line"))
	}
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return nil, err
	}
	out := make([]PostLine, 0, len(lines))
	for i, l := range lines {
		it, err := loadItem(ctx, q, property, l.ItemID, cfg.ValuationMethod)
		if err != nil {
			return nil, errs.Validation("item_not_found", fmt.Sprintf("line %d: item not found", i+1), errs.Field(lineField(i, "itemId"), "not_found", "item not found"))
		}
		qv, err := qty(lineField(i, "quantity"), l.Quantity, positive)
		if err != nil {
			return nil, err
		}
		base, err := toBase(ctx, q, it, qv, l.UOMID)
		if err != nil {
			return nil, err
		}
		cost, err := optCost(lineField(i, "unitCost"), l.UnitCost)
		if err != nil {
			return nil, err
		}
		exp, err := optDate(lineField(i, "expiryDate"), l.ExpiryDate)
		if err != nil {
			return nil, err
		}
		if sign < 0 {
			base = base.Neg()
		}
		out = append(out, PostLine{ItemID: it.ID, Quantity: base, UnitCost: cost, BatchNo: l.BatchNo, ExpiryDate: exp, SerialNos: l.SerialNos})
	}
	return out, nil
}

// unitCostAt is the current valuation cost of an item in a warehouse
// (moving average of the balance, else the latest cost, else standard).
func unitCostAt(ctx context.Context, q dbtx.Querier, wh, item uuid.UUID) (decimal.Decimal, error) {
	var c string
	err := q.QueryRow(ctx, `SELECT coalesce(
		(SELECT CASE WHEN sum(quantity) > 0 AND sum(value) > 0 THEN round(sum(value) / sum(quantity), 6) END FROM inventory.stock_balances
		  WHERE warehouse_id = $1 AND item_id = $2),
		(SELECT l.unit_cost FROM inventory.stock_movement_lines l WHERE l.item_id = $2 AND l.unit_cost > 0 ORDER BY l.seq DESC LIMIT 1),
		(SELECT standard_cost FROM inventory.items WHERE id = $2), 0)::text`, wh, item).Scan(&c)
	return dec(c), err
}

func (s *Stock) submitApproval(ctx context.Context, tx pgx.Tx, dt string, docID uuid.UUID, ref, title string, property uuid.UUID, attrs map[string]any) (uuid.UUID, string, error) {
	if s.Approvals == nil {
		return uuid.Nil, approval.StatusApproved, nil
	}
	return s.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: dt, DocumentID: docID, DocumentRef: ref, Title: title, PropertyID: property,
		Attributes: attrs})
}

// withdraw cancels a pending approval request when the caller requested it.
func (s *Stock) withdraw(ctx context.Context, tx pgx.Tx, rid *uuid.UUID, reason string) {
	if s.Approvals == nil || rid == nil {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return
	}
	if err := s.Approvals.Cancel(ctx, sp, *rid, reason); err != nil {
		_ = sp.Rollback(ctx)
		return
	}
	_ = sp.Commit(ctx)
}

// ══ Store Requisition ═════════════════════════════════════════════════════

// StoreRequisitionInput creates a store requisition (FR-REQ-01).
type StoreRequisitionInput struct {
	RequestType           string           `json:"requestType,omitempty" enum:"store,department,spare_part" doc:"store: replenish a sub-store (requestingWarehouseId); department: issue to a cost center; spare_part: parts for an asset"`
	SourceWarehouseID     *uuid.UUID       `json:"sourceWarehouseId,omitempty" doc:"Store that issues (default: the store that replenishes the requesting warehouse)"`
	RequestingWarehouseID *uuid.UUID       `json:"requestingWarehouseId,omitempty"`
	CostCenter            string           `json:"costCenter,omitempty"`
	DepartmentID          *uuid.UUID       `json:"departmentId,omitempty"`
	AssetID               *uuid.UUID       `json:"assetId,omitempty"`
	MaintenanceRecordID   *uuid.UUID       `json:"maintenanceRecordId,omitempty"`
	NeededBy              string           `json:"neededBy,omitempty" doc:"YYYY-MM-DD"`
	Reason                string           `json:"reason,omitempty"`
	Notes                 string           `json:"notes,omitempty"`
	Submit                bool             `json:"submit,omitempty" doc:"Submit right away (approval per Inventory Policies)"`
	Origin                string           `json:"origin,omitempty" enum:"manual,ops" doc:"ops: entered at a workstation (Kitchen / Outlet / Warehouse)"`
	Lines                 []StockLineInput `json:"lines"`
}

// StoreRequisition is a store requisition with lines and fulfilments.
type StoreRequisition struct {
	ID                    uuid.UUID               `json:"id" db:"id"`
	Number                string                  `json:"number" db:"number"`
	RequestType           string                  `json:"requestType" db:"request_type" enum:"store,department,spare_part,issue"`
	Origin                string                  `json:"origin" db:"origin" enum:"manual,ops,replenishment,maintenance"`
	SourceWarehouseID     uuid.UUID               `json:"sourceWarehouseId" db:"source_warehouse_id"`
	SourceWarehouse       string                  `json:"sourceWarehouse" db:"source_warehouse"`
	RequestingWarehouseID *uuid.UUID              `json:"requestingWarehouseId" db:"requesting_warehouse_id"`
	RequestingWarehouse   *string                 `json:"requestingWarehouse" db:"requesting_warehouse"`
	CostCenter            *string                 `json:"costCenter" db:"cost_center"`
	DepartmentID          *uuid.UUID              `json:"departmentId" db:"department_id"`
	AssetID               *uuid.UUID              `json:"assetId" db:"asset_id"`
	AssetCode             *string                 `json:"assetCode" db:"asset_code"`
	MaintenanceRecordID   *uuid.UUID              `json:"maintenanceRecordId" db:"maintenance_record_id"`
	NeededBy              *string                 `json:"neededBy" db:"needed_by"`
	Status                string                  `json:"status" db:"status" enum:"draft,submitted,approved,rejected,partially_issued,issued,closed,cancelled"`
	ApprovalRequestID     *uuid.UUID              `json:"approvalRequestId" db:"approval_request_id"`
	EstimatedValue        string                  `json:"estimatedValue" db:"estimated_value"`
	Reason                *string                 `json:"reason" db:"reason"`
	Notes                 *string                 `json:"notes" db:"notes"`
	RejectionReason       *string                 `json:"rejectionReason" db:"rejection_reason"`
	PurchaseRequestedAt   *time.Time              `json:"purchaseRequestedAt" db:"purchase_requested_at"`
	RequestedBy           *uuid.UUID              `json:"requestedBy" db:"requested_by"`
	RequestedByName       *string                 `json:"requestedByName" db:"requested_by_name"`
	SubmittedAt           *time.Time              `json:"submittedAt" db:"submitted_at"`
	ApprovedAt            *time.Time              `json:"approvedAt" db:"approved_at"`
	ClosedAt              *time.Time              `json:"closedAt" db:"closed_at"`
	CreatedAt             time.Time               `json:"createdAt" db:"created_at"`
	Lines                 []StoreRequisitionLine  `json:"lines" db:"-"`
	Issues                []StoreRequisitionIssue `json:"issues" db:"-"`
}

// StoreRequisitionLine is one requested item.
type StoreRequisitionLine struct {
	ID                uuid.UUID `json:"id" db:"id"`
	LineNo            int       `json:"lineNo" db:"line_no"`
	ItemID            uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode          string    `json:"itemCode" db:"item_code"`
	ItemName          string    `json:"itemName" db:"item_name"`
	UOM               string    `json:"uom" db:"uom" doc:"Stock UOM"`
	EnteredUOM        *string   `json:"enteredUom" db:"entered_uom"`
	Quantity          string    `json:"quantity" db:"quantity" doc:"As entered"`
	BaseQuantity      string    `json:"baseQuantity" db:"base_quantity"`
	IssuedQuantity    string    `json:"issuedQuantity" db:"issued_quantity"`
	CancelledQuantity string    `json:"cancelledQuantity" db:"cancelled_quantity"`
	RemainingQuantity string    `json:"remainingQuantity" db:"remaining_quantity"`
	AvailableAtSource string    `json:"availableAtSource" db:"available_at_source"`
	Notes             *string   `json:"notes" db:"notes"`
}

// StoreRequisitionIssue is one (partial) fulfilment.
type StoreRequisitionIssue struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	IssuedAt  time.Time  `json:"issuedAt" db:"issued_at"`
	IssuedBy  *uuid.UUID `json:"issuedBy" db:"issued_by"`
	TotalCost string     `json:"totalCost" db:"total_cost"`
	Movements []string   `json:"movements" db:"movements" doc:"Stock movement numbers"`
}

const requisitionSelect = `SELECT r.id, r.number, r.request_type, r.origin, r.source_warehouse_id, sw.name AS source_warehouse, r.requesting_warehouse_id,
	rw.name AS requesting_warehouse, r.cost_center, r.department_id, r.asset_id, a.code AS asset_code, r.maintenance_record_id,
	to_char(r.needed_by, 'YYYY-MM-DD') AS needed_by, r.status, r.approval_request_id, trim_scale(r.estimated_value)::text AS estimated_value, r.reason,
	r.notes, r.rejection_reason, r.purchase_requested_at, r.requested_by, u.full_name AS requested_by_name, r.submitted_at, r.approved_at, r.closed_at,
	r.created_at
	FROM inventory.requisitions r JOIN inventory.warehouses sw ON sw.id = r.source_warehouse_id
	LEFT JOIN inventory.warehouses rw ON rw.id = r.requesting_warehouse_id LEFT JOIN inventory.assets a ON a.id = r.asset_id
	LEFT JOIN platform.users u ON u.id = r.requested_by`

// GetRequisition returns a requisition with lines and fulfilments.
func GetRequisition(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (StoreRequisition, error) {
	rows, err := q.Query(ctx, requisitionSelect+` WHERE r.id = $1`, rid)
	r, err := handle.One[StoreRequisition](rows, err, "requisition")
	if err != nil {
		return r, err
	}
	if r.Lines, err = handle.List[StoreRequisitionLine](q.Query(ctx, `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		eu.code AS entered_uom, trim_scale(l.quantity)::text AS quantity, trim_scale(l.base_quantity)::text AS base_quantity,
		trim_scale(l.issued_quantity)::text AS issued_quantity, trim_scale(l.cancelled_quantity)::text AS cancelled_quantity,
		trim_scale(greatest(l.base_quantity - l.issued_quantity - l.cancelled_quantity, 0))::text AS remaining_quantity,
		trim_scale(coalesce((SELECT sum(b.quantity) FROM inventory.stock_balances b WHERE b.warehouse_id = $2 AND b.item_id = l.item_id), 0))::text AS available_at_source,
		l.notes FROM inventory.requisition_lines l JOIN inventory.items i ON i.id = l.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		LEFT JOIN inventory.uoms eu ON eu.id = l.uom_id WHERE l.requisition_id = $1 ORDER BY l.line_no`, rid, r.SourceWarehouseID)); err != nil {
		return r, err
	}
	r.Issues, err = handle.List[StoreRequisitionIssue](q.Query(ctx, `SELECT x.id, x.issued_at, x.issued_by, trim_scale(x.total_cost)::text AS total_cost,
		coalesce((SELECT array_agg(m.number ORDER BY m.seq) FROM inventory.stock_movements m WHERE m.source_type = 'requisition' AND m.source_id = x.id), '{}') AS movements
		FROM inventory.requisition_issues x WHERE x.requisition_id = $1 ORDER BY x.issued_at`, rid))
	return r, err
}

// CreateRequisition creates a draft requisition (submitted when asked).
func (s *Stock) CreateRequisition(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StoreRequisitionInput, origin string) (StoreRequisition, error) {
	if in.RequestType == "" {
		in.RequestType = "store"
		if in.RequestingWarehouseID == nil {
			in.RequestType = "department"
		}
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return StoreRequisition{}, err
	}
	switch in.RequestType {
	case "store":
		if in.RequestingWarehouseID == nil {
			return StoreRequisition{}, handle.Invalid("requestingWarehouseId", "required", "a store requisition needs the requesting warehouse")
		}
		rw, err := loadWarehouse(ctx, tx, property, *in.RequestingWarehouseID)
		if err != nil {
			return StoreRequisition{}, err
		}
		if in.SourceWarehouseID == nil {
			in.SourceWarehouseID = rw.ParentID
		}
	case "department", "issue":
		if strings.TrimSpace(in.CostCenter) == "" && in.DepartmentID == nil && in.AssetID == nil {
			return StoreRequisition{}, handle.Invalid("costCenter", "required", "a department requisition needs a cost center or department")
		}
	case "spare_part":
		if in.AssetID == nil {
			return StoreRequisition{}, handle.Invalid("assetId", "required", "a spare part request needs the asset")
		}
		if in.SourceWarehouseID == nil {
			if in.SourceWarehouseID, err = warehouseByCode(ctx, tx, property, cfg.SparePartWarehouse); err != nil {
				return StoreRequisition{}, err
			}
		}
		if in.CostCenter == "" {
			in.CostCenter = "engineering"
		}
	default:
		return StoreRequisition{}, handle.Invalid("requestType", "invalid", "requestType must be store, department or spare_part")
	}
	if in.SourceWarehouseID == nil {
		return StoreRequisition{}, handle.Invalid("sourceWarehouseId", "required", "the issuing store is required")
	}
	src, err := loadWarehouse(ctx, tx, property, *in.SourceWarehouseID)
	if err != nil {
		return StoreRequisition{}, err
	}
	if in.RequestingWarehouseID != nil && *in.RequestingWarehouseID == src.ID {
		return StoreRequisition{}, handle.Invalid("sourceWarehouseId", "same_warehouse", "a warehouse cannot request from itself")
	}
	if in.AssetID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.assets WHERE id = $1 AND property_id = $2)`, *in.AssetID, property).Scan(&ok); err != nil {
			return StoreRequisition{}, err
		}
		if !ok {
			return StoreRequisition{}, handle.Invalid("assetId", "not_found", "asset not found")
		}
	}
	needed, err := optDate("neededBy", in.NeededBy)
	if err != nil {
		return StoreRequisition{}, err
	}
	lines, err := s.baseLines(ctx, tx, property, in.Lines, 1, true)
	if err != nil {
		return StoreRequisition{}, err
	}
	rid := id.New()
	number, err := numbering.Next(ctx, tx, property, "SRQ", localNow(ctx, tx))
	if err != nil {
		return StoreRequisition{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.requisitions (id, property_id, number, request_type, origin, source_warehouse_id, requesting_warehouse_id,
		cost_center, department_id, asset_id, maintenance_record_id, needed_by, reason, notes, requested_by, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$15)`, rid, property, number, in.RequestType, origin, src.ID, in.RequestingWarehouseID,
		nullStr(in.CostCenter), in.DepartmentID, in.AssetID, in.MaintenanceRecordID, needed, nullStr(in.Reason), nullStr(in.Notes), uid); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return StoreRequisition{}, errs.Validation("invalid_reference", "a referenced record does not exist")
		}
		return StoreRequisition{}, err
	}
	for i, l := range lines {
		qv, _ := qty("quantity", in.Lines[i].Quantity, true)
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.requisition_lines (id, property_id, requisition_id, line_no, item_id, uom_id, quantity, base_quantity, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8::numeric,$9)`, id.New(), property, rid, i+1, l.ItemID, in.Lines[i].UOMID, qv.String(), l.Quantity.String(),
			nullStr(in.Lines[i].Notes)); err != nil {
			return StoreRequisition{}, err
		}
	}
	r, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if err := record(ctx, tx, property, "inventory.requisition", rid, number, "create", nil, r, ""); err != nil {
		return r, err
	}
	if in.Submit {
		return s.SubmitRequisition(ctx, tx, property, rid)
	}
	return r, nil
}

// SubmitRequisition submits a draft: approved directly up to the Inventory
// Policies threshold, otherwise through the approval workflow.
func (s *Stock) SubmitRequisition(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (StoreRequisition, error) {
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "draft" {
		return r, conflictStatus("Requisition", r.Number, r.Status, "submitted")
	}
	value := decimal.Zero
	for _, l := range r.Lines {
		c, err := unitCostAt(ctx, tx, r.SourceWarehouseID, l.ItemID)
		if err != nil {
			return r, err
		}
		value = value.Add(dec(l.BaseQuantity).Mul(c))
	}
	value = value.Round(2)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'submitted', submitted_at = now(), estimated_value = $2::numeric, updated_by = $3
		WHERE id = $1`, rid, value.String(), uuidOrNil(handle.UserID(ctx))); err != nil {
		return r, err
	}
	if value.GreaterThan(dec(pol.RequisitionApprovalAbove)) {
		var srcCode string
		_ = tx.QueryRow(ctx, `SELECT code FROM inventory.warehouses WHERE id = $1`, r.SourceWarehouseID).Scan(&srcCode)
		aid, _, err := s.submitApproval(ctx, tx, DocRequisition.Code, rid, r.Number, "Store Requisition "+r.Number+" ("+value.String()+")", property,
			map[string]any{"amount": value.InexactFloat64(), "requestType": r.RequestType, "warehouse": srcCode})
		if err != nil {
			return r, err
		}
		if aid != uuid.Nil {
			if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET approval_request_id = $2 WHERE id = $1`, rid, aid); err != nil {
				return r, err
			}
		} else if err := s.approveRequisition(ctx, tx, rid); err != nil {
			return r, err
		}
	} else if err := s.approveRequisition(ctx, tx, rid); err != nil {
		return r, err
	}
	after, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.requisition", rid, r.Number, "submit", map[string]any{"status": r.Status},
		map[string]any{"status": after.Status, "estimatedValue": value.String()}, "")
}

func lockRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (StoreRequisition, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.requisitions WHERE id = $1 FOR UPDATE`, rid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return StoreRequisition{}, errs.NotFound("requisition")
	}
	if err != nil {
		return StoreRequisition{}, err
	}
	return GetRequisition(ctx, tx, rid)
}

func (s *Stock) approveRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'approved', approved_at = now() WHERE id = $1 AND status = 'submitted'`, rid)
	return err
}

func (s *Stock) RequisitionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.Status {
	case approval.StatusApproved:
		return s.approveRequisition(ctx, tx, d.DocumentID)
	case approval.StatusRejected:
		_, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'rejected', rejection_reason = $2, closed_at = now() WHERE id = $1 AND status = 'submitted'`,
			d.DocumentID, nullStr(d.Reason))
		return err
	case approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'draft', approval_request_id = NULL WHERE id = $1 AND status = 'submitted'`, d.DocumentID)
		return err
	}
	return nil
}

// RequisitionFulfillInput issues (part of) an approved requisition (FR-REQ-02).
type RequisitionFulfillInput struct {
	Lines          []RequisitionFulfillLine `json:"lines,omitempty" doc:"Empty: every remaining quantity"`
	CloseRemaining bool                     `json:"closeRemaining,omitempty" doc:"Cancel what is not issued now and close the requisition"`
	Notes          string                   `json:"notes,omitempty"`
}

// RequisitionFulfillLine is the quantity issued for one requisition line.
type RequisitionFulfillLine struct {
	LineID    uuid.UUID `json:"lineId"`
	Quantity  string    `json:"quantity" doc:"Stock UOM"`
	BatchNo   string    `json:"batchNo,omitempty" doc:"Empty: first-expired-first-out"`
	SerialNos []string  `json:"serialNos,omitempty"`
}

// FulfillRequisition issues stock: a transfer from the store to the
// requesting warehouse, or an issue to the cost center.
func (s *Stock) FulfillRequisition(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in RequisitionFulfillInput) (StoreRequisition, error) {
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "approved" && r.Status != "partially_issued" {
		return r, conflictStatus("Requisition", r.Number, r.Status, "issued")
	}
	byID := map[uuid.UUID]StoreRequisitionLine{}
	for _, l := range r.Lines {
		byID[l.ID] = l
	}
	type issue struct {
		line StoreRequisitionLine
		qty  decimal.Decimal
		pl   PostLine
	}
	var issues []issue
	if len(in.Lines) == 0 {
		for _, l := range r.Lines {
			if rem := dec(l.RemainingQuantity); rem.IsPositive() {
				issues = append(issues, issue{l, rem, PostLine{ItemID: l.ItemID, Quantity: rem.Neg()}})
			}
		}
	}
	for i, fl := range in.Lines {
		l, ok := byID[fl.LineID]
		if !ok {
			return r, handle.Invalid(lineField(i, "lineId"), "not_found", "line not on this requisition")
		}
		qv, err := qty(lineField(i, "quantity"), fl.Quantity, true)
		if err != nil {
			return r, err
		}
		if qv.GreaterThan(dec(l.RemainingQuantity)) {
			return r, handle.Invalid(lineField(i, "quantity"), "too_much", fmt.Sprintf("only %s %s remain on line %d", l.RemainingQuantity, l.UOM, l.LineNo))
		}
		issues = append(issues, issue{l, qv, PostLine{ItemID: l.ItemID, Quantity: qv.Neg(), BatchNo: fl.BatchNo, SerialNos: fl.SerialNos}})
	}
	if len(issues) == 0 && !in.CloseRemaining {
		return r, errs.Validation("nothing_to_issue", "nothing remains to be issued")
	}
	total := decimal.Zero
	if len(issues) > 0 {
		iid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.requisition_issues (id, property_id, requisition_id, issued_by, notes) VALUES ($1,$2,$3,$4,$5)`,
			iid, property, rid, uuidOrNil(handle.UserID(ctx)), nullStr(in.Notes)); err != nil {
			return r, err
		}
		pls := make([]PostLine, 0, len(issues))
		var keys []stockKey
		for _, x := range issues {
			pls = append(pls, x.pl)
			keys = append(keys, stockKey{r.SourceWarehouseID, x.line.ItemID})
			if r.RequestingWarehouseID != nil {
				keys = append(keys, stockKey{*r.RequestingWarehouseID, x.line.ItemID})
			}
		}
		if err := lockStock(ctx, tx, keys); err != nil {
			return r, err
		}
		reason := "Requisition " + r.Number
		if r.RequestingWarehouseID != nil {
			out, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferOut, SourceType: "requisition", SourceID: &iid, WarehouseID: r.SourceWarehouseID,
				CounterWarehouseID: r.RequestingWarehouseID, Reason: reason, Lines: pls})
			if err != nil {
				return r, err
			}
			if _, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferIn, SourceType: "requisition", SourceID: &iid, WarehouseID: *r.RequestingWarehouseID,
				CounterWarehouseID: &r.SourceWarehouseID, Reason: reason, Lines: mirror(out), IgnoreFreeze: true}); err != nil {
				return r, err
			}
			total = dec(out.TotalCost).Neg()
		} else {
			cc := ""
			if r.CostCenter != nil {
				cc = *r.CostCenter
			}
			out, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveIssue, SourceType: "requisition", SourceID: &iid, WarehouseID: r.SourceWarehouseID,
				CostCenter: cc, DepartmentID: r.DepartmentID, AssetID: r.AssetID, Reason: reason, Lines: pls})
			if err != nil {
				return r, err
			}
			total = dec(out.TotalCost).Neg()
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisition_issues SET total_cost = $2::numeric WHERE id = $1`, iid, total.String()); err != nil {
			return r, err
		}
		for _, x := range issues {
			if _, err := tx.Exec(ctx, `UPDATE inventory.requisition_lines SET issued_quantity = issued_quantity + $2::numeric WHERE id = $1`,
				x.line.ID, x.qty.String()); err != nil {
				return r, err
			}
		}
		if r.MaintenanceRecordID != nil {
			if _, err := tx.Exec(ctx, `UPDATE inventory.maintenance_records SET parts_cost = parts_cost + $2::numeric WHERE id = $1`, *r.MaintenanceRecordID,
				total.String()); err != nil {
				return r, err
			}
		}
	}
	if in.CloseRemaining {
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisition_lines SET cancelled_quantity = greatest(base_quantity - issued_quantity, 0) WHERE requisition_id = $1`,
			rid); err != nil {
			return r, err
		}
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.requisition_lines WHERE requisition_id = $1
		AND issued_quantity + cancelled_quantity < base_quantity)`, rid).Scan(&open); err != nil {
		return r, err
	}
	var anyCancelled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.requisition_lines WHERE requisition_id = $1 AND cancelled_quantity > 0)`, rid).
		Scan(&anyCancelled); err != nil {
		return r, err
	}
	status := "partially_issued"
	switch {
	case !open && anyCancelled:
		status = "closed"
	case !open:
		status = "issued"
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = $2, closed_at = CASE WHEN $2 IN ('issued', 'closed') THEN now() END,
		updated_by = $3 WHERE id = $1`, rid, status, uuidOrNil(handle.UserID(ctx))); err != nil {
		return r, err
	}
	after, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.requisition", rid, r.Number, "fulfill", map[string]any{"status": r.Status},
		map[string]any{"status": status, "issuedCost": total.String()}, in.Notes)
}

// CancelRequisition cancels an open requisition; a partially issued one is
// closed (the rest is cancelled).
func (s *Stock) CancelRequisition(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, reason string) (StoreRequisition, error) {
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	switch r.Status {
	case "draft", "submitted", "approved":
		if r.Status == "submitted" {
			s.withdraw(ctx, tx, r.ApprovalRequestID, reason)
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'cancelled', closed_at = now(), updated_by = $2 WHERE id = $1`, rid,
			uuidOrNil(handle.UserID(ctx))); err != nil {
			return r, err
		}
	case "partially_issued":
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisition_lines SET cancelled_quantity = greatest(base_quantity - issued_quantity, 0) WHERE requisition_id = $1`,
			rid); err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'closed', closed_at = now(), updated_by = $2 WHERE id = $1`, rid,
			uuidOrNil(handle.UserID(ctx))); err != nil {
			return r, err
		}
	default:
		return r, conflictStatus("Requisition", r.Number, r.Status, "cancelled")
	}
	after, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.requisition", rid, r.Number, "cancel", map[string]any{"status": r.Status},
		map[string]any{"status": after.Status}, reason)
}

// RequestPurchase turns what the store cannot supply into a purchase
// requisition request (inventory.reorder_needed, FR-REQ-02).
func (s *Stock) RequestPurchase(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (StoreRequisition, error) {
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "approved" && r.Status != "partially_issued" {
		return r, conflictStatus("Requisition", r.Number, r.Status, "sent to purchasing")
	}
	var items []map[string]any
	day := today(ctx, tx)
	for _, l := range r.Lines {
		short := dec(l.RemainingQuantity).Sub(dec(l.AvailableAtSource))
		if !short.IsPositive() {
			continue
		}
		var base uuid.UUID
		var supplier *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT i.base_uom_id, coalesce(rp.preferred_supplier_id, i.preferred_supplier_id) FROM inventory.items i
			LEFT JOIN inventory.reorder_points rp ON rp.item_id = i.id AND rp.warehouse_id = $2 WHERE i.id = $1`, l.ItemID, r.SourceWarehouseID).
			Scan(&base, &supplier); err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.reorder_requests (id, property_id, warehouse_id, item_id, business_date, on_hand, reorder_point, par_level,
			suggested_quantity, source, requisition_id) VALUES ($1,$2,$3,$4,$5,$6::numeric,0,0,$7::numeric,'requisition',$8)`,
			id.New(), property, r.SourceWarehouseID, l.ItemID, day, l.AvailableAtSource, short.String(), rid); err != nil {
			return r, err
		}
		items = append(items, map[string]any{"itemId": l.ItemID, "onHand": l.AvailableAtSource, "reorderPoint": "0", "parLevel": "0",
			"suggestedQuantity": short.String(), "uomId": base, "preferredSupplierId": supplier})
	}
	if len(items) == 0 {
		return r, errs.Conflict("stock_available", "the store has enough stock for every remaining line")
	}
	if s.Events != nil {
		if _, err := s.Events.Publish(ctx, tx, EventReorderNeeded, "inventory.requisition", &rid, &property, map[string]any{"warehouseId": r.SourceWarehouseID,
			"items": items, "source": "requisition", "requisitionId": rid, "requisitionNumber": r.Number}); err != nil {
			return r, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET purchase_requested_at = now() WHERE id = $1`, rid); err != nil {
		return r, err
	}
	after, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.requisition", rid, r.Number, "request_purchase", nil, map[string]any{"items": len(items)}, "")
}

// StockIssueInput issues stock directly to a department / cost center (FR-STK-03).
type StockIssueInput struct {
	WarehouseID  uuid.UUID        `json:"warehouseId"`
	CostCenter   string           `json:"costCenter,omitempty"`
	DepartmentID *uuid.UUID       `json:"departmentId,omitempty"`
	AssetID      *uuid.UUID       `json:"assetId,omitempty"`
	Reason       string           `json:"reason"`
	Notes        string           `json:"notes,omitempty"`
	Origin       string           `json:"origin,omitempty" enum:"manual,ops"`
	Lines        []StockLineInput `json:"lines"`
}

// Issue records a direct issue as a fulfilled requisition (Issuing in `ops`).
func (s *Stock) Issue(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StockIssueInput, origin string) (StoreRequisition, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return StoreRequisition{}, err
	}
	src := in.WarehouseID
	r, err := s.CreateRequisition(ctx, tx, property, StoreRequisitionInput{RequestType: "issue", SourceWarehouseID: &src, CostCenter: in.CostCenter,
		DepartmentID: in.DepartmentID, AssetID: in.AssetID, Reason: in.Reason, Notes: in.Notes, Lines: in.Lines}, origin)
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.requisitions SET status = 'approved', submitted_at = now(), approved_at = now() WHERE id = $1`, r.ID); err != nil {
		return r, err
	}
	var fl []RequisitionFulfillLine
	for i, l := range r.Lines {
		fl = append(fl, RequisitionFulfillLine{LineID: l.ID, Quantity: l.BaseQuantity, BatchNo: in.Lines[i].BatchNo, SerialNos: in.Lines[i].SerialNos})
	}
	return s.FulfillRequisition(ctx, tx, property, r.ID, RequisitionFulfillInput{Lines: fl, Notes: in.Notes})
}

// ══ Stock Transfer ════════════════════════════════════════════════════════

// StockTransferInput creates a stock transfer (FR-REQ-03).
type StockTransferInput struct {
	FromWarehouseID uuid.UUID        `json:"fromWarehouseId"`
	ToWarehouseID   uuid.UUID        `json:"toWarehouseId"`
	Notes           string           `json:"notes,omitempty"`
	Ship            bool             `json:"ship,omitempty" doc:"Ship right away (In Transit)"`
	Lines           []StockLineInput `json:"lines"`
}

// StockTransfer is a stock transfer.
type StockTransfer struct {
	ID                 uuid.UUID           `json:"id" db:"id"`
	Number             string              `json:"number" db:"number"`
	FromWarehouseID    uuid.UUID           `json:"fromWarehouseId" db:"from_warehouse_id"`
	FromWarehouse      string              `json:"fromWarehouse" db:"from_warehouse"`
	ToWarehouseID      uuid.UUID           `json:"toWarehouseId" db:"to_warehouse_id"`
	ToWarehouse        string              `json:"toWarehouse" db:"to_warehouse"`
	TransitWarehouseID *uuid.UUID          `json:"transitWarehouseId" db:"transit_warehouse_id"`
	Status             string              `json:"status" db:"status" enum:"draft,in_transit,received,cancelled"`
	Notes              *string             `json:"notes" db:"notes"`
	ShippedAt          *time.Time          `json:"shippedAt" db:"shipped_at"`
	ReceivedAt         *time.Time          `json:"receivedAt" db:"received_at"`
	ShippedValue       string              `json:"shippedValue" db:"shipped_value"`
	DiscrepancyValue   string              `json:"discrepancyValue" db:"discrepancy_value"`
	CreatedAt          time.Time           `json:"createdAt" db:"created_at"`
	Lines              []StockTransferLine `json:"lines" db:"-"`
}

// StockTransferLine is one transferred item.
type StockTransferLine struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	LineNo            int        `json:"lineNo" db:"line_no"`
	ItemID            uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode          string     `json:"itemCode" db:"item_code"`
	ItemName          string     `json:"itemName" db:"item_name"`
	UOM               string     `json:"uom" db:"uom"`
	BatchID           *uuid.UUID `json:"batchId" db:"batch_id"`
	BatchNo           *string    `json:"batchNo" db:"batch_no"`
	SerialNos         []string   `json:"serialNos" db:"serial_nos"`
	Quantity          string     `json:"quantity" db:"quantity"`
	ShippedQuantity   string     `json:"shippedQuantity" db:"shipped_quantity"`
	ReceivedQuantity  string     `json:"receivedQuantity" db:"received_quantity"`
	ShippedValue      string     `json:"shippedValue" db:"shipped_value"`
	DiscrepancyReason *string    `json:"discrepancyReason" db:"discrepancy_reason"`
}

const transferSelect = `SELECT t.id, t.number, t.from_warehouse_id, fw.name AS from_warehouse, t.to_warehouse_id, tw.name AS to_warehouse, t.transit_warehouse_id,
	t.status, t.notes, t.shipped_at, t.received_at, trim_scale(t.shipped_value)::text AS shipped_value, trim_scale(t.discrepancy_value)::text AS discrepancy_value,
	t.created_at FROM inventory.transfers t JOIN inventory.warehouses fw ON fw.id = t.from_warehouse_id JOIN inventory.warehouses tw ON tw.id = t.to_warehouse_id`

// GetTransfer returns a transfer with its lines.
func GetTransfer(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (StockTransfer, error) {
	rows, err := q.Query(ctx, transferSelect+` WHERE t.id = $1`, tid)
	t, err := handle.One[StockTransfer](rows, err, "transfer")
	if err != nil {
		return t, err
	}
	t.Lines, err = handle.List[StockTransferLine](q.Query(ctx, `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		l.batch_id, b.batch_no, coalesce(l.serial_nos, '{}') AS serial_nos, trim_scale(l.quantity)::text AS quantity,
		trim_scale(l.shipped_quantity)::text AS shipped_quantity, trim_scale(l.received_quantity)::text AS received_quantity,
		trim_scale(l.shipped_value)::text AS shipped_value, l.discrepancy_reason
		FROM inventory.transfer_lines l JOIN inventory.items i ON i.id = l.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		LEFT JOIN inventory.batches b ON b.id = l.batch_id WHERE l.transfer_id = $1 ORDER BY l.line_no`, tid))
	return t, err
}

// CreateTransfer creates a draft transfer (shipped when asked).
func (s *Stock) CreateTransfer(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StockTransferInput) (StockTransfer, error) {
	if in.FromWarehouseID == in.ToWarehouseID {
		return StockTransfer{}, handle.Invalid("toWarehouseId", "same_warehouse", "source and destination must differ")
	}
	if _, err := loadWarehouse(ctx, tx, property, in.FromWarehouseID); err != nil {
		return StockTransfer{}, err
	}
	if _, err := loadWarehouse(ctx, tx, property, in.ToWarehouseID); err != nil {
		return StockTransfer{}, err
	}
	lines, err := s.baseLines(ctx, tx, property, in.Lines, 1, true)
	if err != nil {
		return StockTransfer{}, err
	}
	seen := map[uuid.UUID]bool{}
	for i, l := range lines {
		if seen[l.ItemID] {
			return StockTransfer{}, handle.Invalid(lineField(i, "itemId"), "duplicate", "the item is listed twice")
		}
		seen[l.ItemID] = true
	}
	tid := id.New()
	number, err := numbering.Next(ctx, tx, property, "STF", localNow(ctx, tx))
	if err != nil {
		return StockTransfer{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.transfers (id, property_id, number, from_warehouse_id, to_warehouse_id, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, tid, property, number, in.FromWarehouseID, in.ToWarehouseID, nullStr(in.Notes), uid); err != nil {
		return StockTransfer{}, err
	}
	for i, l := range lines {
		var bid *uuid.UUID
		if b := strings.TrimSpace(l.BatchNo); b != "" {
			var x uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM inventory.batches WHERE property_id = $1 AND item_id = $2 AND batch_no = $3`, property, l.ItemID, b).Scan(&x)
			if dbtx.IsNoRows(err) {
				return StockTransfer{}, handle.Invalid(lineField(i, "batchNo"), "not_found", "batch not found")
			}
			if err != nil {
				return StockTransfer{}, err
			}
			bid = &x
		}
		var serials any
		if len(l.SerialNos) > 0 {
			serials = l.SerialNos
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.transfer_lines (id, property_id, transfer_id, line_no, item_id, batch_id, serial_nos, quantity)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric)`, id.New(), property, tid, i+1, l.ItemID, bid, serials, l.Quantity.String()); err != nil {
			return StockTransfer{}, err
		}
	}
	t, err := GetTransfer(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if err := record(ctx, tx, property, "inventory.transfer", tid, number, "create", nil, t, ""); err != nil {
		return t, err
	}
	if in.Ship {
		return s.ShipTransfer(ctx, tx, property, tid)
	}
	return t, nil
}

// ensureTransit returns the In Transit warehouse of the property (created
// when missing).
func ensureTransit(ctx context.Context, tx pgx.Tx, property uuid.UUID, code string) (uuid.UUID, error) {
	var wid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM inventory.warehouses WHERE property_id = $1 AND upper(code) = upper($2)`, property, code).Scan(&wid)
	if err == nil {
		return wid, nil
	}
	if !dbtx.IsNoRows(err) {
		return wid, err
	}
	wid = id.New()
	_, err = tx.Exec(ctx, `INSERT INTO inventory.warehouses (id, property_id, code, name, location_type, cost_center) VALUES ($1,$2,$3,'In Transit','transit','transit')`,
		wid, property, strings.ToUpper(code))
	return wid, err
}

func lockTransfer(ctx context.Context, tx pgx.Tx, tid uuid.UUID) (StockTransfer, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.transfers WHERE id = $1 FOR UPDATE`, tid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return StockTransfer{}, errs.NotFound("transfer")
	}
	if err != nil {
		return StockTransfer{}, err
	}
	return GetTransfer(ctx, tx, tid)
}

// ShipTransfer moves the goods into the transit warehouse (In Transit).
func (s *Stock) ShipTransfer(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (StockTransfer, error) {
	t, err := lockTransfer(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "draft" {
		return t, conflictStatus("Transfer", t.Number, t.Status, "shipped")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return t, err
	}
	transit, err := ensureTransit(ctx, tx, property, cfg.TransitWarehouse)
	if err != nil {
		return t, err
	}
	var lines []PostLine
	var keys []stockKey
	for _, l := range t.Lines {
		lines = append(lines, PostLine{ItemID: l.ItemID, Quantity: dec(l.Quantity).Neg(), BatchID: l.BatchID, SerialNos: l.SerialNos})
		keys = append(keys, stockKey{t.FromWarehouseID, l.ItemID}, stockKey{transit, l.ItemID})
	}
	if err := lockStock(ctx, tx, keys); err != nil {
		return t, err
	}
	out, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferOut, SourceType: "transfer", SourceID: &tid, WarehouseID: t.FromWarehouseID,
		CounterWarehouseID: &t.ToWarehouseID, Reason: "Transfer " + t.Number, Lines: lines})
	if err != nil {
		return t, err
	}
	if _, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferIn, SourceType: "transfer", SourceID: &tid, WarehouseID: transit,
		CounterWarehouseID: &t.FromWarehouseID, Reason: "Transfer " + t.Number + " in transit", Lines: mirror(out), IgnoreFreeze: true}); err != nil {
		return t, err
	}
	value := map[uuid.UUID]decimal.Decimal{}
	for _, l := range out.Lines {
		value[l.ItemID] = value[l.ItemID].Add(dec(l.TotalCost).Neg())
	}
	total := decimal.Zero
	for _, l := range t.Lines {
		if _, err := tx.Exec(ctx, `UPDATE inventory.transfer_lines SET shipped_quantity = quantity, shipped_value = $2::numeric WHERE id = $1`, l.ID,
			value[l.ItemID].String()); err != nil {
			return t, err
		}
		total = total.Add(value[l.ItemID])
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.transfers SET status = 'in_transit', transit_warehouse_id = $2, shipped_at = now(), shipped_by = $3,
		shipped_value = $4::numeric WHERE id = $1`, tid, transit, uuidOrNil(handle.UserID(ctx)), total.String()); err != nil {
		return t, err
	}
	after, err := GetTransfer(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.transfer", tid, t.Number, "ship", map[string]any{"status": t.Status},
		map[string]any{"status": after.Status, "shippedValue": total.String()}, "")
}

// TransferReceiveInput receives an in-transit transfer; differences are recorded.
type TransferReceiveInput struct {
	Lines []TransferReceiveLine `json:"lines,omitempty" doc:"Empty: everything shipped is received"`
	Notes string                `json:"notes,omitempty"`
}

// TransferReceiveLine is the quantity received for one transfer line.
type TransferReceiveLine struct {
	LineID           uuid.UUID `json:"lineId"`
	ReceivedQuantity string    `json:"receivedQuantity"`
	Reason           string    `json:"reason,omitempty" doc:"Reason of a shipped–received difference"`
}

// ReceiveTransfer moves the goods from transit into the destination; the
// shipped–received difference leaves transit as an adjustment (FR-REQ-03).
func (s *Stock) ReceiveTransfer(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TransferReceiveInput) (StockTransfer, error) {
	t, err := lockTransfer(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "in_transit" || t.TransitWarehouseID == nil {
		return t, conflictStatus("Transfer", t.Number, t.Status, "received")
	}
	transit := *t.TransitWarehouseID
	received := map[uuid.UUID]decimal.Decimal{}
	reasons := map[uuid.UUID]string{}
	for _, l := range t.Lines {
		received[l.ID] = dec(l.ShippedQuantity)
	}
	for i, rl := range in.Lines {
		found := false
		for _, l := range t.Lines {
			if l.ID != rl.LineID {
				continue
			}
			found = true
			rq, err := handle.Decimal(lineField(i, "receivedQuantity"), rl.ReceivedQuantity, decimal.Zero)
			if err != nil {
				return t, err
			}
			if rq.IsNegative() || rq.GreaterThan(dec(l.ShippedQuantity)) {
				return t, handle.Invalid(lineField(i, "receivedQuantity"), "invalid", "between 0 and the shipped quantity")
			}
			received[l.ID] = rq
			reasons[l.ID] = rl.Reason
		}
		if !found {
			return t, handle.Invalid(lineField(i, "lineId"), "not_found", "line not on this transfer")
		}
	}
	// The goods in transit of this transfer, as shipped (cost, batch, serials).
	var transitIn uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM inventory.stock_movements WHERE source_type = 'transfer' AND source_id = $1 AND movement_type = 'transfer_in'
		AND warehouse_id = $2 AND reversal_of IS NULL`, tid, transit).Scan(&transitIn); err != nil {
		return t, err
	}
	shipped, err := GetMovement(ctx, tx, transitIn)
	if err != nil {
		return t, err
	}
	var recv, lost []PostLine
	var keys []stockKey
	for _, l := range t.Lines {
		left := received[l.ID]
		keys = append(keys, stockKey{transit, l.ItemID}, stockKey{t.ToWarehouseID, l.ItemID})
		for _, ml := range shipped.Lines {
			if ml.ItemID != l.ItemID {
				continue
			}
			q, c := dec(ml.Quantity), dec(ml.UnitCost)
			take := decimal.Min(q, left)
			left = left.Sub(take)
			n := int(take.IntPart())
			var sIn, sLost []string
			if len(ml.SerialNos) > 0 {
				n = min(n, len(ml.SerialNos))
				sIn, sLost = ml.SerialNos[:n], ml.SerialNos[n:]
			}
			cost := c
			if take.IsPositive() {
				recv = append(recv, PostLine{ItemID: l.ItemID, Quantity: take.Neg(), UnitCost: &cost, BatchID: ml.BatchID, SerialNos: sIn})
			}
			if rest := q.Sub(take); rest.IsPositive() {
				lost = append(lost, PostLine{ItemID: l.ItemID, Quantity: rest.Neg(), UnitCost: &cost, BatchID: ml.BatchID, SerialNos: sLost})
			}
		}
	}
	if err := lockStock(ctx, tx, keys); err != nil {
		return t, err
	}
	if len(recv) > 0 {
		out, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferOut, SourceType: "transfer", SourceID: &tid, WarehouseID: transit,
			CounterWarehouseID: &t.ToWarehouseID, Reason: "Transfer " + t.Number + " received", Lines: recv, ExplicitCost: true, IgnoreFreeze: true})
		if err != nil {
			return t, err
		}
		if _, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveTransferIn, SourceType: "transfer", SourceID: &tid, WarehouseID: t.ToWarehouseID,
			CounterWarehouseID: &t.FromWarehouseID, Reason: "Transfer " + t.Number, Lines: mirror(out), IgnoreFreeze: true}); err != nil {
			return t, err
		}
	}
	discrepancy := decimal.Zero
	if len(lost) > 0 {
		m, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveAdjustment, SourceType: "transfer", SourceID: &tid, WarehouseID: transit,
			CounterWarehouseID: &t.ToWarehouseID, Reason: "transfer_discrepancy", Notes: "Transfer " + t.Number + ": shipped and received quantities differ",
			Lines: lost, ExplicitCost: true, IgnoreFreeze: true})
		if err != nil {
			return t, err
		}
		discrepancy = dec(m.TotalCost).Neg()
	}
	for _, l := range t.Lines {
		if _, err := tx.Exec(ctx, `UPDATE inventory.transfer_lines SET received_quantity = $2::numeric, discrepancy_reason = $3 WHERE id = $1`,
			l.ID, received[l.ID].String(), nullStr(reasons[l.ID])); err != nil {
			return t, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.transfers SET status = 'received', received_at = now(), received_by = $2, discrepancy_value = $3::numeric,
		notes = coalesce($4, notes) WHERE id = $1`, tid, uuidOrNil(handle.UserID(ctx)), discrepancy.String(), nullStr(in.Notes)); err != nil {
		return t, err
	}
	after, err := GetTransfer(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.transfer", tid, t.Number, "receive", map[string]any{"status": t.Status},
		map[string]any{"status": after.Status, "discrepancyValue": discrepancy.String()}, in.Notes)
}

// CancelTransfer cancels a draft transfer.
func (s *Stock) CancelTransfer(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, reason string) (StockTransfer, error) {
	t, err := lockTransfer(ctx, tx, tid)
	if err != nil {
		return t, err
	}
	if t.Status != "draft" {
		return t, conflictStatus("Transfer", t.Number, t.Status, "cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.transfers SET status = 'cancelled', updated_by = $2 WHERE id = $1`, tid, uuidOrNil(handle.UserID(ctx))); err != nil {
		return t, err
	}
	after, err := GetTransfer(ctx, tx, tid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.transfer", tid, t.Number, "cancel", map[string]any{"status": t.Status},
		map[string]any{"status": "cancelled"}, reason)
}

// ══ Stock Adjustment ══════════════════════════════════════════════════════

// AdjustmentReasons are the reasons of a stock adjustment.
var AdjustmentReasons = []string{"damage", "expired", "theft", "count_correction", "found", "opening_balance", "other"}

// StockAdjustmentInput creates a stock adjustment (FR-OPN-03).
type StockAdjustmentInput struct {
	WarehouseID  uuid.UUID        `json:"warehouseId"`
	Reason       string           `json:"reason" enum:"damage,expired,theft,count_correction,found,opening_balance,other"`
	BusinessDate string           `json:"businessDate,omitempty" doc:"YYYY-MM-DD (default today; not in a closed period)"`
	Notes        string           `json:"notes,omitempty"`
	Submit       bool             `json:"submit,omitempty"`
	Lines        []StockLineInput `json:"lines" doc:"Signed quantities: + found / − lost"`
}

// StockAdjustment is a stock adjustment.
type StockAdjustment struct {
	ID                uuid.UUID             `json:"id" db:"id"`
	Number            string                `json:"number" db:"number"`
	WarehouseID       uuid.UUID             `json:"warehouseId" db:"warehouse_id"`
	Warehouse         string                `json:"warehouse" db:"warehouse"`
	Reason            string                `json:"reason" db:"reason"`
	Status            string                `json:"status" db:"status" enum:"draft,pending_approval,posted,rejected,cancelled"`
	BusinessDate      *string               `json:"businessDate" db:"business_date"`
	ApprovalRequestID *uuid.UUID            `json:"approvalRequestId" db:"approval_request_id"`
	MovementID        *uuid.UUID            `json:"movementId" db:"movement_id"`
	MovementNumber    *string               `json:"movementNumber" db:"movement_number"`
	TotalValue        string                `json:"totalValue" db:"total_value"`
	Notes             *string               `json:"notes" db:"notes"`
	RejectionReason   *string               `json:"rejectionReason" db:"rejection_reason"`
	SubmittedAt       *time.Time            `json:"submittedAt" db:"submitted_at"`
	PostedAt          *time.Time            `json:"postedAt" db:"posted_at"`
	CreatedAt         time.Time             `json:"createdAt" db:"created_at"`
	Lines             []StockAdjustmentLine `json:"lines" db:"-"`
}

// StockAdjustmentLine is one adjusted item.
type StockAdjustmentLine struct {
	ID         uuid.UUID `json:"id" db:"id"`
	LineNo     int       `json:"lineNo" db:"line_no"`
	ItemID     uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode   string    `json:"itemCode" db:"item_code"`
	ItemName   string    `json:"itemName" db:"item_name"`
	UOM        string    `json:"uom" db:"uom"`
	Quantity   string    `json:"quantity" db:"quantity"`
	UnitCost   *string   `json:"unitCost" db:"unit_cost"`
	BatchNo    *string   `json:"batchNo" db:"batch_no"`
	ExpiryDate *string   `json:"expiryDate" db:"expiry_date"`
	SerialNos  []string  `json:"serialNos" db:"serial_nos"`
	Notes      *string   `json:"notes" db:"notes"`
}

const adjustmentSelect = `SELECT a.id, a.number, a.warehouse_id, w.name AS warehouse, a.reason, a.status, to_char(a.business_date, 'YYYY-MM-DD') AS business_date,
	a.approval_request_id, a.movement_id, m.number AS movement_number, trim_scale(a.total_value)::text AS total_value, a.notes, a.rejection_reason,
	a.submitted_at, a.posted_at, a.created_at FROM inventory.adjustments a JOIN inventory.warehouses w ON w.id = a.warehouse_id
	LEFT JOIN inventory.stock_movements m ON m.id = a.movement_id`

// GetAdjustment returns an adjustment with its lines.
func GetAdjustment(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (StockAdjustment, error) {
	rows, err := q.Query(ctx, adjustmentSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[StockAdjustment](rows, err, "adjustment")
	if err != nil {
		return a, err
	}
	a.Lines, err = handle.List[StockAdjustmentLine](q.Query(ctx, `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		trim_scale(l.quantity)::text AS quantity, trim_scale(l.unit_cost)::text AS unit_cost, l.batch_no, to_char(l.expiry_date, 'YYYY-MM-DD') AS expiry_date,
		coalesce(l.serial_nos, '{}') AS serial_nos, l.notes FROM inventory.adjustment_lines l JOIN inventory.items i ON i.id = l.item_id
		JOIN inventory.uoms u ON u.id = i.base_uom_id WHERE l.adjustment_id = $1 ORDER BY l.line_no`, aid))
	return a, err
}

// CreateAdjustment creates a draft adjustment (submitted when asked).
func (s *Stock) CreateAdjustment(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StockAdjustmentInput) (StockAdjustment, error) {
	if !contains(AdjustmentReasons, in.Reason) {
		return StockAdjustment{}, handle.Invalid("reason", "invalid", "reason must be one of "+strings.Join(AdjustmentReasons, ", "))
	}
	if _, err := loadWarehouse(ctx, tx, property, in.WarehouseID); err != nil {
		return StockAdjustment{}, err
	}
	bd, err := optDate("businessDate", in.BusinessDate)
	if err != nil {
		return StockAdjustment{}, err
	}
	lines, err := s.baseLines(ctx, tx, property, in.Lines, 1, false)
	if err != nil {
		return StockAdjustment{}, err
	}
	aid := id.New()
	number, err := numbering.Next(ctx, tx, property, "SAJ", localNow(ctx, tx))
	if err != nil {
		return StockAdjustment{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.adjustments (id, property_id, number, warehouse_id, reason, business_date, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, aid, property, number, in.WarehouseID, in.Reason, bd, nullStr(in.Notes), uid); err != nil {
		return StockAdjustment{}, err
	}
	for i, l := range lines {
		var cost any
		if l.UnitCost != nil {
			cost = l.UnitCost.String()
		}
		var serials any
		if len(l.SerialNos) > 0 {
			serials = l.SerialNos
		}
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.adjustment_lines (id, property_id, adjustment_id, line_no, item_id, quantity, unit_cost, batch_no, expiry_date,
			serial_nos, notes) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8,$9,$10,$11)`, id.New(), property, aid, i+1, l.ItemID, l.Quantity.String(), cost,
			nullStr(l.BatchNo), l.ExpiryDate, serials, nullStr(in.Lines[i].Notes)); err != nil {
			return StockAdjustment{}, err
		}
	}
	a, err := GetAdjustment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if err := record(ctx, tx, property, "inventory.adjustment", aid, number, "create", nil, a, in.Reason); err != nil {
		return a, err
	}
	if in.Submit {
		return s.SubmitAdjustment(ctx, tx, property, aid)
	}
	return a, nil
}

func lockAdjustment(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (StockAdjustment, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.adjustments WHERE id = $1 FOR UPDATE`, aid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return StockAdjustment{}, errs.NotFound("adjustment")
	}
	if err != nil {
		return StockAdjustment{}, err
	}
	return GetAdjustment(ctx, tx, aid)
}

// SubmitAdjustment posts the adjustment, through approval above the
// Inventory Policies threshold.
func (s *Stock) SubmitAdjustment(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) (StockAdjustment, error) {
	a, err := lockAdjustment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status != "draft" {
		return a, conflictStatus("Adjustment", a.Number, a.Status, "submitted")
	}
	value := decimal.Zero
	for _, l := range a.Lines {
		var c decimal.Decimal
		if l.UnitCost != nil && dec(l.Quantity).IsPositive() {
			c = dec(*l.UnitCost)
		} else if c, err = unitCostAt(ctx, tx, a.WarehouseID, l.ItemID); err != nil {
			return a, err
		}
		value = value.Add(dec(l.Quantity).Abs().Mul(c))
	}
	value = value.Round(2)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return a, err
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.adjustments SET status = 'pending_approval', submitted_at = now(), updated_by = $2 WHERE id = $1`, aid,
		uuidOrNil(handle.UserID(ctx))); err != nil {
		return a, err
	}
	if value.GreaterThan(dec(pol.AdjustmentApprovalAbove)) && a.Reason != "opening_balance" {
		var whCode string
		_ = tx.QueryRow(ctx, `SELECT code FROM inventory.warehouses WHERE id = $1`, a.WarehouseID).Scan(&whCode)
		rid, _, err := s.submitApproval(ctx, tx, DocAdjustment.Code, aid, a.Number, "Stock Adjustment "+a.Number+" ("+value.String()+")", property,
			map[string]any{"amount": value.InexactFloat64(), "reason": a.Reason, "warehouse": whCode})
		if err != nil {
			return a, err
		}
		if rid != uuid.Nil {
			if _, err := tx.Exec(ctx, `UPDATE inventory.adjustments SET approval_request_id = $2 WHERE id = $1 AND status = 'pending_approval'`, aid, rid); err != nil {
				return a, err
			}
		} else if err := s.postAdjustment(ctx, tx, property, aid); err != nil {
			return a, err
		}
	} else if err := s.postAdjustment(ctx, tx, property, aid); err != nil {
		return a, err
	}
	after, err := GetAdjustment(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.adjustment", aid, a.Number, "submit", map[string]any{"status": a.Status},
		map[string]any{"status": after.Status, "value": value.String()}, "")
}

// postAdjustment posts an adjustment pending approval.
func (s *Stock) postAdjustment(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) error {
	a, err := GetAdjustment(ctx, tx, aid)
	if err != nil {
		return err
	}
	if a.Status != "pending_approval" {
		return nil
	}
	var lines []PostLine
	for _, l := range a.Lines {
		pl := PostLine{ItemID: l.ItemID, Quantity: dec(l.Quantity), SerialNos: l.SerialNos}
		if l.BatchNo != nil {
			pl.BatchNo = *l.BatchNo
		}
		if l.ExpiryDate != nil {
			if d, err := parseDate(*l.ExpiryDate); err == nil {
				pl.ExpiryDate = &d
			}
		}
		if l.UnitCost != nil {
			c := dec(*l.UnitCost)
			pl.UnitCost = &c
		}
		lines = append(lines, pl)
	}
	var bd time.Time
	if a.BusinessDate != nil {
		bd, _ = parseDate(*a.BusinessDate)
	}
	m, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveAdjustment, SourceType: "adjustment", SourceID: &aid, WarehouseID: a.WarehouseID,
		Reason: a.Reason, Notes: "Stock Adjustment " + a.Number, BusinessDate: bd, Lines: lines})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE inventory.adjustments SET status = 'posted', posted_at = now(), movement_id = $2, total_value = $3::numeric,
		business_date = $4::date WHERE id = $1`, aid, m.ID, m.TotalCost, m.BusinessDate)
	return err
}

func (s *Stock) AdjustmentDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.Status {
	case approval.StatusApproved:
		return s.postAdjustment(ctx, tx, d.PropertyID, d.DocumentID)
	case approval.StatusRejected:
		_, err := tx.Exec(ctx, `UPDATE inventory.adjustments SET status = 'rejected', rejection_reason = $2 WHERE id = $1 AND status = 'pending_approval'`,
			d.DocumentID, nullStr(d.Reason))
		return err
	case approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE inventory.adjustments SET status = 'draft', approval_request_id = NULL WHERE id = $1 AND status = 'pending_approval'`, d.DocumentID)
		return err
	}
	return nil
}

// CancelAdjustment cancels a draft or pending adjustment.
func (s *Stock) CancelAdjustment(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, reason string) (StockAdjustment, error) {
	a, err := lockAdjustment(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if a.Status != "draft" && a.Status != "pending_approval" {
		return a, conflictStatus("Adjustment", a.Number, a.Status, "cancelled")
	}
	if a.Status == "pending_approval" {
		s.withdraw(ctx, tx, a.ApprovalRequestID, reason)
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.adjustments SET status = 'cancelled', updated_by = $2 WHERE id = $1`, aid, uuidOrNil(handle.UserID(ctx))); err != nil {
		return a, err
	}
	after, err := GetAdjustment(ctx, tx, aid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.adjustment", aid, a.Number, "cancel", map[string]any{"status": a.Status},
		map[string]any{"status": "cancelled"}, reason)
}

// ══ Production ════════════════════════════════════════════════════════════

// ProductionInput creates a production order for a semi-finished item.
type ProductionOrderInput struct {
	RecipeID        uuid.UUID `json:"recipeId" doc:"Sub-recipe with an output item"`
	WarehouseID     uuid.UUID `json:"warehouseId" doc:"Kitchen producing it"`
	PlannedQuantity string    `json:"plannedQuantity" doc:"Output quantity in the output item's stock UOM"`
	ScheduledFor    string    `json:"scheduledFor,omitempty" doc:"YYYY-MM-DD"`
	Notes           string    `json:"notes,omitempty"`
}

// CompleteProductionInput records the actual yield (FR-PRD-01).
type CompleteProductionInput struct {
	ActualQuantity string                 `json:"actualQuantity,omitempty" doc:"Actual yield in the output stock UOM (default planned)"`
	Inputs         []ProductionActualLine `json:"inputs,omitempty" doc:"Actual consumption per ingredient (default planned)"`
	BatchNo        string                 `json:"batchNo,omitempty" doc:"Batch of the output (default the order number when the item is batch-tracked)"`
	ExpiryDate     string                 `json:"expiryDate,omitempty"`
}

// ProductionLine is an actual ingredient quantity.
type ProductionActualLine struct {
	ItemID   uuid.UUID `json:"itemId"`
	Quantity string    `json:"quantity" doc:"Stock UOM"`
}

// ProductionOrder is a production order.
type ProductionOrder struct {
	ID              uuid.UUID            `json:"id" db:"id"`
	Number          string               `json:"number" db:"number"`
	RecipeID        uuid.UUID            `json:"recipeId" db:"recipe_id"`
	RecipeName      string               `json:"recipeName" db:"recipe_name"`
	OutputItemID    uuid.UUID            `json:"outputItemId" db:"output_item_id"`
	OutputItem      string               `json:"outputItem" db:"output_item"`
	UOM             string               `json:"uom" db:"uom"`
	WarehouseID     uuid.UUID            `json:"warehouseId" db:"warehouse_id"`
	Warehouse       string               `json:"warehouse" db:"warehouse"`
	PlannedQuantity string               `json:"plannedQuantity" db:"planned_quantity"`
	ActualQuantity  *string              `json:"actualQuantity" db:"actual_quantity"`
	Status          string               `json:"status" db:"status" enum:"draft,completed,cancelled"`
	ScheduledFor    *string              `json:"scheduledFor" db:"scheduled_for"`
	SourceType      *string              `json:"sourceType" db:"source_type" doc:"banquet_event: scheduled from a BEO (FR-PRD-05)"`
	SourceID        *uuid.UUID           `json:"sourceId" db:"source_id"`
	SourceRef       *string              `json:"sourceRef" db:"source_ref"`
	BatchID         *uuid.UUID           `json:"batchId" db:"batch_id"`
	InputCost       string               `json:"inputCost" db:"input_cost"`
	OutputUnitCost  string               `json:"outputUnitCost" db:"output_unit_cost"`
	Notes           *string              `json:"notes" db:"notes"`
	CompletedAt     *time.Time           `json:"completedAt" db:"completed_at"`
	CreatedAt       time.Time            `json:"createdAt" db:"created_at"`
	Inputs          []ProductionInputRow `json:"inputs" db:"-"`
}

// ProductionInputRow is a planned / actual ingredient.
type ProductionInputRow struct {
	ItemID          uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode        string    `json:"itemCode" db:"item_code"`
	ItemName        string    `json:"itemName" db:"item_name"`
	UOM             string    `json:"uom" db:"uom"`
	PlannedQuantity string    `json:"plannedQuantity" db:"planned_quantity"`
	ActualQuantity  *string   `json:"actualQuantity" db:"actual_quantity"`
	TotalCost       string    `json:"totalCost" db:"total_cost"`
}

const productionSelect = `SELECT p.id, p.number, p.recipe_id, r.name AS recipe_name, p.output_item_id, i.name AS output_item, u.code AS uom, p.warehouse_id,
	w.name AS warehouse, trim_scale(p.planned_quantity)::text AS planned_quantity, trim_scale(p.actual_quantity)::text AS actual_quantity, p.status,
	to_char(p.scheduled_for, 'YYYY-MM-DD') AS scheduled_for, p.source_type, p.source_id, p.source_ref, p.batch_id, trim_scale(p.input_cost)::text AS input_cost,
	trim_scale(p.output_unit_cost)::text AS output_unit_cost, p.notes, p.completed_at, p.created_at
	FROM inventory.production_orders p JOIN inventory.recipes r ON r.id = p.recipe_id JOIN inventory.items i ON i.id = p.output_item_id
	JOIN inventory.uoms u ON u.id = i.base_uom_id JOIN inventory.warehouses w ON w.id = p.warehouse_id`

// GetProduction returns a production order with its inputs.
func GetProduction(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (ProductionOrder, error) {
	rows, err := q.Query(ctx, productionSelect+` WHERE p.id = $1`, pid)
	p, err := handle.One[ProductionOrder](rows, err, "production order")
	if err != nil {
		return p, err
	}
	p.Inputs, err = handle.List[ProductionInputRow](q.Query(ctx, `SELECT x.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
		trim_scale(x.planned_quantity)::text AS planned_quantity, trim_scale(x.actual_quantity)::text AS actual_quantity, trim_scale(x.total_cost)::text AS total_cost
		FROM inventory.production_inputs x JOIN inventory.items i ON i.id = x.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id
		WHERE x.production_order_id = $1 ORDER BY i.code`, pid))
	return p, err
}

// CreateProduction plans a production order from a sub-recipe.
func (s *Stock) CreateProduction(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ProductionOrderInput) (ProductionOrder, error) {
	r, _, err := loadRecipe(ctx, tx, `id = $1`, in.RecipeID)
	if err != nil {
		return ProductionOrder{}, err
	}
	if r.OutputItem == nil {
		return ProductionOrder{}, handle.Invalid("recipeId", "no_output", "the recipe has no output item (semi-finished)")
	}
	if _, err := loadWarehouse(ctx, tx, property, in.WarehouseID); err != nil {
		return ProductionOrder{}, err
	}
	planned, err := qty("plannedQuantity", in.PlannedQuantity, true)
	if err != nil {
		return ProductionOrder{}, err
	}
	sched, err := optDate("scheduledFor", in.ScheduledFor)
	if err != nil {
		return ProductionOrder{}, err
	}
	inputs, err := s.recipeInputs(ctx, tx, property, r, planned)
	if err != nil {
		return ProductionOrder{}, err
	}
	pid := id.New()
	number, err := numbering.Next(ctx, tx, property, "PRO", localNow(ctx, tx))
	if err != nil {
		return ProductionOrder{}, err
	}
	uid := uuidOrNil(handle.UserID(ctx))
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.production_orders (id, property_id, number, recipe_id, output_item_id, warehouse_id, planned_quantity,
		scheduled_for, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$10)`, pid, property, number, r.ID, *r.OutputItem,
		in.WarehouseID, planned.String(), sched, nullStr(in.Notes), uid); err != nil {
		return ProductionOrder{}, err
	}
	for item, q := range inputs {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.production_inputs (id, property_id, production_order_id, item_id, planned_quantity)
			VALUES ($1,$2,$3,$4,$5::numeric)`, id.New(), property, pid, item, q.Round(6).String()); err != nil {
			return ProductionOrder{}, err
		}
	}
	p, err := GetProduction(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	return p, record(ctx, tx, property, "inventory.production_order", pid, number, "create", nil, p, "")
}

// recipeInputs returns the ingredients of `output` stock units of a
// sub-recipe's output item.
func (s *Stock) recipeInputs(ctx context.Context, q dbtx.Querier, property uuid.UUID, r recipe, output decimal.Decimal) (map[uuid.UUID]decimal.Decimal, error) {
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return nil, err
	}
	units := output
	if r.YieldUOM != nil && r.OutputItem != nil {
		var base uuid.UUID
		if err := q.QueryRow(ctx, `SELECT base_uom_id FROM inventory.items WHERE id = $1`, *r.OutputItem).Scan(&base); err != nil {
			return nil, err
		}
		if units, err = convert(ctx, q, r.OutputItem, output, base, *r.YieldUOM); err != nil {
			return nil, err
		}
	}
	into := map[uuid.UUID]decimal.Decimal{}
	if err := s.stockExplode(ctx, q, r.ID, units, 0, into, cfg); err != nil {
		return nil, err
	}
	if len(into) == 0 {
		return nil, errs.Validation("empty_recipe", "the recipe has no ingredients")
	}
	return into, nil
}

func lockProduction(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (ProductionOrder, error) {
	var st string
	err := tx.QueryRow(ctx, `SELECT status FROM inventory.production_orders WHERE id = $1 FOR UPDATE`, pid).Scan(&st)
	if dbtx.IsNoRows(err) {
		return ProductionOrder{}, errs.NotFound("production order")
	}
	if err != nil {
		return ProductionOrder{}, err
	}
	return GetProduction(ctx, tx, pid)
}

// CompleteProduction consumes the ingredients (production_out) and puts the
// output into stock at the cost of the ingredients (production_in).
func (s *Stock) CompleteProduction(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID, in CompleteProductionInput) (ProductionOrder, error) {
	p, err := lockProduction(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if p.Status != "draft" {
		return p, conflictStatus("Production order", p.Number, p.Status, "completed")
	}
	actual := dec(p.PlannedQuantity)
	if strings.TrimSpace(in.ActualQuantity) != "" {
		if actual, err = qty("actualQuantity", in.ActualQuantity, true); err != nil {
			return p, err
		}
	}
	use := map[uuid.UUID]decimal.Decimal{}
	for _, x := range p.Inputs {
		use[x.ItemID] = dec(x.PlannedQuantity)
	}
	for i, x := range in.Inputs {
		q, err := handle.Decimal(fmt.Sprintf("inputs[%d].quantity", i), x.Quantity, decimal.Zero)
		if err != nil {
			return p, err
		}
		if q.IsNegative() {
			return p, handle.Invalid(fmt.Sprintf("inputs[%d].quantity", i), "invalid", "must not be negative")
		}
		use[x.ItemID] = q
	}
	var lines []PostLine
	keys := []stockKey{{p.WarehouseID, p.OutputItemID}}
	for item, q := range use {
		if q.IsPositive() {
			lines = append(lines, PostLine{ItemID: item, Quantity: q.Neg()})
			keys = append(keys, stockKey{p.WarehouseID, item})
		}
	}
	if err := lockStock(ctx, tx, keys); err != nil {
		return p, err
	}
	cost := decimal.Zero
	costs := map[uuid.UUID]decimal.Decimal{}
	if len(lines) > 0 {
		out, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveProductionOut, SourceType: "production", SourceID: &pid, WarehouseID: p.WarehouseID,
			Reason: "Production " + p.Number, Lines: lines})
		if err != nil {
			return p, err
		}
		cost = dec(out.TotalCost).Neg()
		for _, l := range out.Lines {
			costs[l.ItemID] = costs[l.ItemID].Add(dec(l.TotalCost).Neg())
		}
	}
	unit := cost.Div(actual).Round(6)
	batchNo := strings.TrimSpace(in.BatchNo)
	var batched bool
	if err := tx.QueryRow(ctx, `SELECT track_batch OR track_expiry FROM inventory.items WHERE id = $1`, p.OutputItemID).Scan(&batched); err != nil {
		return p, err
	}
	if batched && batchNo == "" {
		batchNo = p.Number
	}
	exp, err := optDate("expiryDate", in.ExpiryDate)
	if err != nil {
		return p, err
	}
	inm, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveProductionIn, SourceType: "production", SourceID: &pid, WarehouseID: p.WarehouseID,
		Reason: "Production " + p.Number, Lines: []PostLine{{ItemID: p.OutputItemID, Quantity: actual, UnitCost: &unit, BatchNo: batchNo, ExpiryDate: exp}}})
	if err != nil {
		return p, err
	}
	for item, q := range use {
		if _, err := tx.Exec(ctx, `INSERT INTO inventory.production_inputs (id, property_id, production_order_id, item_id, planned_quantity, actual_quantity, total_cost)
			VALUES ($1,$2,$3,$4,0,$5::numeric,$6::numeric) ON CONFLICT (production_order_id, item_id) DO UPDATE SET actual_quantity = EXCLUDED.actual_quantity,
			total_cost = EXCLUDED.total_cost`, id.New(), property, pid, item, q.String(), costs[item].String()); err != nil {
			return p, err
		}
	}
	var bid *uuid.UUID
	if len(inm.Lines) > 0 {
		bid = inm.Lines[0].BatchID
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.production_orders SET status = 'completed', actual_quantity = $2::numeric, input_cost = $3::numeric,
		output_unit_cost = $4::numeric, batch_id = $5, completed_at = now(), completed_by = $6 WHERE id = $1`, pid, actual.String(), cost.String(), unit.String(),
		bid, uuidOrNil(handle.UserID(ctx))); err != nil {
		return p, err
	}
	after, err := GetProduction(ctx, tx, pid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.production_order", pid, p.Number, "complete", map[string]any{"status": p.Status},
		map[string]any{"status": "completed", "actualQuantity": actual.String(), "inputCost": cost.String()}, "")
}

// CancelProduction cancels a draft production order.
func (s *Stock) CancelProduction(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID, reason string) (ProductionOrder, error) {
	p, err := lockProduction(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if p.Status != "draft" {
		return p, conflictStatus("Production order", p.Number, p.Status, "cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory.production_orders SET status = 'cancelled', updated_by = $2 WHERE id = $1`, pid, uuidOrNil(handle.UserID(ctx))); err != nil {
		return p, err
	}
	after, err := GetProduction(ctx, tx, pid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, property, "inventory.production_order", pid, p.Number, "cancel", map[string]any{"status": p.Status},
		map[string]any{"status": "cancelled"}, reason)
}

// ══ Waste ═════════════════════════════════════════════════════════════════

// WasteReasons are the reasons of waste / spoilage (FR-PRD-02).
var WasteReasons = []string{"expired", "damaged", "spoiled", "wrong_preparation", "overproduction", "breakage", "other"}

// WasteInput records waste / spoilage.
type WasteRecordInput struct {
	WarehouseID  uuid.UUID        `json:"warehouseId"`
	Reason       string           `json:"reason" enum:"expired,damaged,spoiled,wrong_preparation,overproduction,breakage,other"`
	BusinessDate string           `json:"businessDate,omitempty"`
	Notes        string           `json:"notes,omitempty"`
	Lines        []StockLineInput `json:"lines" doc:"Positive quantities wasted"`
}

// WasteRecord is a recorded waste.
type WasteRecord struct {
	ID             uuid.UUID           `json:"id" db:"id"`
	Number         string              `json:"number" db:"number"`
	WarehouseID    uuid.UUID           `json:"warehouseId" db:"warehouse_id"`
	Warehouse      string              `json:"warehouse" db:"warehouse"`
	Reason         string              `json:"reason" db:"reason"`
	BusinessDate   string              `json:"businessDate" db:"business_date"`
	MovementID     *uuid.UUID          `json:"movementId" db:"movement_id"`
	MovementNumber *string             `json:"movementNumber" db:"movement_number"`
	TotalCost      string              `json:"totalCost" db:"total_cost"`
	Notes          *string             `json:"notes" db:"notes"`
	RecordedBy     *uuid.UUID          `json:"recordedBy" db:"recorded_by"`
	RecordedByName *string             `json:"recordedByName" db:"recorded_by_name"`
	CreatedAt      time.Time           `json:"createdAt" db:"created_at"`
	Lines          []StockMovementLine `json:"lines" db:"-"`
}

const wasteSelect = `SELECT x.id, x.number, x.warehouse_id, w.name AS warehouse, x.reason, to_char(x.business_date, 'YYYY-MM-DD') AS business_date, x.movement_id,
	m.number AS movement_number, trim_scale(x.total_cost)::text AS total_cost, x.notes, x.recorded_by, u.full_name AS recorded_by_name, x.created_at
	FROM inventory.waste_records x JOIN inventory.warehouses w ON w.id = x.warehouse_id LEFT JOIN inventory.stock_movements m ON m.id = x.movement_id
	LEFT JOIN platform.users u ON u.id = x.recorded_by`

// GetWaste returns a waste record with its lines.
func GetWaste(ctx context.Context, q dbtx.Querier, wid uuid.UUID) (WasteRecord, error) {
	rows, err := q.Query(ctx, wasteSelect+` WHERE x.id = $1`, wid)
	w, err := handle.One[WasteRecord](rows, err, "waste record")
	if err != nil || w.MovementID == nil {
		return w, err
	}
	w.Lines, err = handle.List[StockMovementLine](q.Query(ctx, movementLineSelect+` WHERE l.movement_id = $1 ORDER BY l.line_no`, *w.MovementID))
	return w, err
}

// RecordWaste posts waste / spoilage at valuation cost.
func (s *Stock) RecordWaste(ctx context.Context, tx pgx.Tx, property uuid.UUID, in WasteRecordInput) (WasteRecord, error) {
	if !contains(WasteReasons, in.Reason) {
		return WasteRecord{}, handle.Invalid("reason", "invalid", "reason must be one of "+strings.Join(WasteReasons, ", "))
	}
	bd, err := optDate("businessDate", in.BusinessDate)
	if err != nil {
		return WasteRecord{}, err
	}
	lines, err := s.baseLines(ctx, tx, property, in.Lines, -1, true)
	if err != nil {
		return WasteRecord{}, err
	}
	wid := id.New()
	number, err := numbering.Next(ctx, tx, property, "WST", localNow(ctx, tx))
	if err != nil {
		return WasteRecord{}, err
	}
	var date time.Time
	if bd != nil {
		date = *bd
	}
	m, _, err := s.Post(ctx, tx, property, PostInput{Type: MoveWaste, SourceType: "waste", SourceID: &wid, WarehouseID: in.WarehouseID, Reason: in.Reason,
		Notes: "Waste " + number, BusinessDate: date, Lines: lines})
	if err != nil {
		return WasteRecord{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory.waste_records (id, property_id, number, warehouse_id, reason, business_date, movement_id, total_cost, notes, recorded_by)
		VALUES ($1,$2,$3,$4,$5,$6::date,$7,$8::numeric,$9,$10)`, wid, property, number, in.WarehouseID, in.Reason, m.BusinessDate, m.ID,
		dec(m.TotalCost).Neg().String(), nullStr(in.Notes), uuidOrNil(handle.UserID(ctx))); err != nil {
		return WasteRecord{}, err
	}
	w, err := GetWaste(ctx, tx, wid)
	if err != nil {
		return w, err
	}
	return w, record(ctx, tx, property, "inventory.waste", wid, number, "create", nil, w, in.Reason)
}

// ══ Reversal ══════════════════════════════════════════════════════════════

// ReverseMovement posts the opposite of a consumption, issue or waste at
// the same cost (refunded sale, issue returned, waste recorded by mistake;
// FR-CNS-02). The ledger stays append-only.
func (s *Stock) ReverseMovement(ctx context.Context, tx pgx.Tx, property, mid uuid.UUID, reason string) (StockMovement, error) {
	if err := handle.Required("reason", reason); err != nil {
		return StockMovement{}, err
	}
	m, err := GetMovement(ctx, tx, mid)
	if err != nil {
		return m, err
	}
	if m.ReversalOf != nil || m.ReversedBy != nil {
		return m, errs.Conflict("already_reversed", "movement "+m.Number+" is a reversal or already reversed")
	}
	switch m.MovementType {
	case MoveConsumption, MoveIssue, MoveWaste:
	default:
		return m, errs.Conflict("not_reversible", m.MovementType+" movements are corrected through their document")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return m, err
	}
	if m.SourceType == "sale" && !cfg.RefundRestock {
		return m, errs.Conflict("refund_restock_disabled", "Inventory Configuration does not return refunded sales to stock")
	}
	var lines []PostLine
	for _, l := range m.Lines {
		c := dec(l.UnitCost)
		lines = append(lines, PostLine{ItemID: l.ItemID, Quantity: dec(l.Quantity).Neg(), UnitCost: &c, BatchID: l.BatchID, SerialNos: l.SerialNos})
	}
	cc := ""
	if m.CostCenter != nil {
		cc = *m.CostCenter
	}
	rev, _, err := s.Post(ctx, tx, property, PostInput{Type: m.MovementType, SourceType: m.SourceType, SourceID: m.SourceID, WarehouseID: m.WarehouseID,
		CounterWarehouseID: m.CounterWarehouseID, CostCenter: cc, OutletID: m.OutletID, DepartmentID: m.DepartmentID, AssetID: m.AssetID,
		Reason: "reversal", Notes: reason, ReversalOf: &mid, ExplicitCost: true, AllowNegative: true, Lines: lines})
	if err != nil {
		return rev, err
	}
	return rev, record(ctx, tx, property, "inventory.stock_movement", rev.ID, rev.Number, "reverse", map[string]any{"movement": m.Number},
		map[string]any{"reversal": rev.Number, "totalCost": rev.TotalCost}, reason)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
