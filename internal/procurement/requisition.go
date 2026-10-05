package procurement

// PRD P4 EP-11 Purchase Requisition & Approval (FR-PR-01..05) and the
// procurement side of EP-08 (automatic PR from inventory.reorder_needed)
// and contract K1 (banquet material PR from banquet.beo_issued /
// beo_revised). Statuses (PRD P4 §7.6): Draft → Submitted → Approved /
// Rejected → Partially Ordered → Ordered (+ Cancelled).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/inventory"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PurchaseRequisition is a purchase requisition.
type PurchaseRequisition struct {
	ID                uuid.UUID                 `json:"id" db:"id"`
	Number            string                    `json:"number" db:"number"`
	Source            string                    `json:"source" db:"source" enum:"manual,reorder,banquet,store_requisition"`
	Status            string                    `json:"status" db:"status" enum:"draft,submitted,approved,rejected,partially_ordered,ordered,cancelled"`
	Title             *string                   `json:"title" db:"title"`
	DepartmentID      *uuid.UUID                `json:"departmentId" db:"department_id"`
	OutletID          *uuid.UUID                `json:"outletId" db:"outlet_id"`
	WarehouseID       *uuid.UUID                `json:"warehouseId" db:"warehouse_id"`
	CostCenter        *string                   `json:"costCenter" db:"cost_center"`
	BudgetCode        *string                   `json:"budgetCode" db:"budget_code"`
	Category          *string                   `json:"category" db:"category"`
	NeededBy          *string                   `json:"neededBy" db:"needed_by"`
	Currency          string                    `json:"currency" db:"currency"`
	EstimatedTotal    string                    `json:"estimatedTotal" db:"estimated_total"`
	Notes             *string                   `json:"notes" db:"notes"`
	SourceRef         *string                   `json:"sourceRef" db:"source_ref" doc:"BEO no., store requisition no. or reorder run"`
	EventID           *uuid.UUID                `json:"eventId" db:"event_id"`
	BEOID             *uuid.UUID                `json:"beoId" db:"beo_id"`
	BEOVersion        *int                      `json:"beoVersion" db:"beo_version"`
	EventDate         *string                   `json:"eventDate" db:"event_date"`
	Attention         *string                   `json:"attention" db:"attention" doc:"Flag for the buyer, e.g. BEO revised after ordering"`
	RequestedBy       *uuid.UUID                `json:"requestedBy" db:"requested_by"`
	SubmittedAt       *time.Time                `json:"submittedAt" db:"submitted_at"`
	ApprovalRequestID *uuid.UUID                `json:"approvalRequestId" db:"approval_request_id"`
	ApprovedAt        *time.Time                `json:"approvedAt" db:"approved_at"`
	RejectedReason    *string                   `json:"rejectedReason" db:"rejected_reason"`
	CancelledReason   *string                   `json:"cancelledReason" db:"cancelled_reason"`
	AgeDays           int                       `json:"ageDays" db:"age_days"`
	Version           int                       `json:"version" db:"version"`
	CreatedAt         time.Time                 `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time                 `json:"updatedAt" db:"updated_at"`
	Lines             []PurchaseRequisitionLine `json:"lines,omitempty" db:"-"`
}

// PurchaseRequisitionLine is one requested item or service.
type PurchaseRequisitionLine struct {
	ID                  uuid.UUID       `json:"id" db:"id"`
	RequisitionID       uuid.UUID       `json:"requisitionId" db:"requisition_id"`
	LineNo              int             `json:"lineNo" db:"line_no"`
	ItemID              *uuid.UUID      `json:"itemId" db:"item_id"`
	ItemCode            *string         `json:"itemCode" db:"item_code"`
	Description         string          `json:"description" db:"description"`
	Quantity            string          `json:"quantity" db:"quantity"`
	UOMID               *uuid.UUID      `json:"uomId" db:"uom_id"`
	UOM                 *string         `json:"uom" db:"uom"`
	BaseQuantity        *string         `json:"baseQuantity" db:"base_quantity"`
	EstimatedUnitPrice  string          `json:"estimatedUnitPrice" db:"estimated_unit_price"`
	EstimatedTotal      string          `json:"estimatedTotal" db:"estimated_total"`
	NeededBy            *string         `json:"neededBy" db:"needed_by"`
	CostCenter          *string         `json:"costCenter" db:"cost_center"`
	WarehouseID         *uuid.UUID      `json:"warehouseId" db:"warehouse_id"`
	SuggestedSupplierID *uuid.UUID      `json:"suggestedSupplierId" db:"suggested_supplier_id"`
	OrderedQuantity     string          `json:"orderedQuantity" db:"ordered_quantity"`
	OpenQuantity        string          `json:"openQuantity" db:"open_quantity"`
	RFQID               *uuid.UUID      `json:"rfqId" db:"rfq_id"`
	Status              string          `json:"status" db:"status" enum:"open,cancelled"`
	SourceData          json.RawMessage `json:"sourceData" db:"source_data" doc:"Reorder figures or BEO requirement"`
	Notes               *string         `json:"notes" db:"notes"`
}

// PurchaseRequisitionLineInput is a requested line.
type PurchaseRequisitionLineInput struct {
	ItemID              *uuid.UUID `json:"itemId,omitempty" doc:"Inventory item; empty for a service / non-stock line"`
	Description         string     `json:"description,omitempty" doc:"Default: item name"`
	Quantity            string     `json:"quantity"`
	UOMID               *uuid.UUID `json:"uomId,omitempty" doc:"Default: purchase UOM of the item, else its stock UOM"`
	EstimatedUnitPrice  string     `json:"estimatedUnitPrice,omitempty" doc:"Default: supplier price list, else standard cost"`
	NeededBy            string     `json:"neededBy,omitempty"`
	CostCenter          string     `json:"costCenter,omitempty"`
	WarehouseID         *uuid.UUID `json:"warehouseId,omitempty"`
	SuggestedSupplierID *uuid.UUID `json:"suggestedSupplierId,omitempty"`
	Notes               string     `json:"notes,omitempty"`
}

// PurchaseRequisitionInput creates or edits a requisition.
type PurchaseRequisitionInput struct {
	Source       string                         `json:"source,omitempty" enum:"manual,store_requisition"`
	SourceRef    string                         `json:"sourceRef,omitempty" doc:"Store requisition that could not be fulfilled (FR-REQ-02)"`
	Title        string                         `json:"title,omitempty"`
	DepartmentID *uuid.UUID                     `json:"departmentId,omitempty"`
	OutletID     *uuid.UUID                     `json:"outletId,omitempty"`
	WarehouseID  *uuid.UUID                     `json:"warehouseId,omitempty" doc:"Destination warehouse / stock location"`
	CostCenter   string                         `json:"costCenter,omitempty"`
	BudgetCode   string                         `json:"budgetCode,omitempty"`
	Category     string                         `json:"category,omitempty"`
	NeededBy     string                         `json:"neededBy,omitempty"`
	Currency     string                         `json:"currency,omitempty"`
	Notes        string                         `json:"notes,omitempty"`
	Lines        []PurchaseRequisitionLineInput `json:"lines"`
	Submit       bool                           `json:"submit,omitempty" doc:"Submit for approval right away"`
}

// ProcurementReasonInput carries a mandatory or optional reason.
type ProcurementReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

const requisitionSelect = `SELECT r.id, r.number, r.source, r.status, r.title, r.department_id, r.outlet_id, r.warehouse_id, r.cost_center, r.budget_code,
	r.category, to_char(r.needed_by, 'YYYY-MM-DD') AS needed_by, r.currency, trim_scale(r.estimated_total)::text AS estimated_total, r.notes, r.source_ref,
	r.event_id, r.beo_id, r.beo_version, to_char(r.event_date, 'YYYY-MM-DD') AS event_date, r.attention, r.requested_by, r.submitted_at,
	r.approval_request_id, r.approved_at, r.rejected_reason, r.cancelled_reason, greatest(0, billing.local_date(r.property_id)
	  - (r.created_at AT TIME ZONE coalesce((SELECT nullif(p.timezone, '') FROM platform.properties p WHERE p.id = r.property_id),
	    (SELECT timezone FROM platform.instance)))::date) AS age_days,
	r.version, r.created_at, r.updated_at FROM procurement.purchase_requisitions r`

const requisitionLineSelect = `SELECT l.id, l.requisition_id, l.line_no, l.item_id, i.code AS item_code, l.description, trim_scale(l.quantity)::text AS quantity,
	l.uom_id, u.code AS uom, trim_scale(l.base_quantity)::text AS base_quantity, trim_scale(l.estimated_unit_price)::text AS estimated_unit_price,
	trim_scale(l.estimated_total)::text AS estimated_total, to_char(l.needed_by, 'YYYY-MM-DD') AS needed_by, l.cost_center, l.warehouse_id,
	l.suggested_supplier_id, trim_scale(l.ordered_quantity)::text AS ordered_quantity,
	trim_scale(CASE WHEN l.status = 'open' THEN greatest(l.quantity - l.ordered_quantity, 0) ELSE 0 END)::text AS open_quantity,
	l.rfq_id, l.status, l.source_data, l.notes
	FROM procurement.purchase_requisition_lines l LEFT JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.uoms u ON u.id = l.uom_id`

// GetRequisition loads a requisition with its lines.
func GetRequisition(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (PurchaseRequisition, error) {
	r, err := oneOf[PurchaseRequisition]("purchase requisition")(q.Query(ctx, requisitionSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return r, err
	}
	r.Lines, err = handle.List[PurchaseRequisitionLine](q.Query(ctx, requisitionLineSelect+` WHERE l.requisition_id = $1 ORDER BY l.line_no`, rid))
	return r, err
}

func lockRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PurchaseRequisition, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.purchase_requisitions WHERE id = $1 FOR UPDATE`, rid); err != nil {
		return PurchaseRequisition{}, err
	}
	return GetRequisition(ctx, tx, rid)
}

// preparedLine is a validated requisition / order line.
type preparedLine struct {
	ItemID      *uuid.UUID
	Description string
	Quantity    decimal.Decimal
	UOMID       *uuid.UUID
	BaseQty     *decimal.Decimal
	Price       decimal.Decimal
	NeededBy    *time.Time
	CostCenter  *string
	WarehouseID *uuid.UUID
	Supplier    *uuid.UUID
	Notes       *string
	SourceData  map[string]any
}

// prepareItemLine validates the item / UOM / quantity of a line and
// computes the base quantity; price is the given estimate, the supplier
// price list or the item's standard cost.
func prepareItemLine(ctx context.Context, q dbtx.Querier, field string, itemID, uomID *uuid.UUID, desc, qty, price string, supplier *uuid.UUID,
	on time.Time) (preparedLine, error) {
	var pl preparedLine
	quantity, err := positive(field+".quantity", qty)
	if err != nil {
		return pl, err
	}
	pl.Quantity = quantity
	pl.Description = strings.TrimSpace(desc)
	if itemID == nil {
		if pl.Description == "" {
			return pl, errs.Validation("description_required", "a line without an item needs a description",
				errs.Field(field+".description", "required", "describe the service or non-stock item"))
		}
		pl.UOMID = uomID
		pl.Price, err = nonNegative(field+".estimatedUnitPrice", price, decimal.Zero)
		return pl, err
	}
	it, err := inventory.ProcurementItemByID(ctx, q, *itemID)
	if err != nil {
		return pl, err
	}
	if it.Status != "active" {
		return pl, errs.Validation("item_inactive", "item "+it.Code+" is inactive", errs.Field(field+".itemId", "inactive", "item is inactive"))
	}
	pl.ItemID = itemID
	if pl.Description == "" {
		pl.Description = it.Name
	}
	u := it.BaseUOMID
	if it.PurchaseUOMID != nil {
		u = *it.PurchaseUOMID
	}
	if uomID != nil {
		u = *uomID
	}
	pl.UOMID = &u
	base, err := inventory.ProcurementConvert(ctx, q, itemID, quantity, u, it.BaseUOMID)
	if err != nil {
		return pl, err
	}
	base = base.Round(6)
	pl.BaseQty = &base
	if strings.TrimSpace(price) != "" {
		pl.Price, err = nonNegative(field+".estimatedUnitPrice", price, decimal.Zero)
		return pl, err
	}
	if supplier != nil {
		if p, err := supplierPriceFor(ctx, q, *supplier, *itemID, u, on); err != nil {
			return pl, err
		} else if p != nil {
			pl.Price = dec(*p)
			return pl, nil
		}
	}
	perUOM, err := inventory.ProcurementConvert(ctx, q, itemID, decimal.NewFromInt(1), u, it.BaseUOMID)
	if err != nil {
		return pl, err
	}
	pl.Price = it.StandardCost.Mul(perUOM).Round(6)
	return pl, nil
}

func (m *Module) prepareRequisitionLines(ctx context.Context, tx pgx.Tx, in []PurchaseRequisitionLineInput, on time.Time) ([]preparedLine, error) {
	if len(in) == 0 {
		return nil, errs.Validation("lines_required", "add at least one line", errs.Field("lines", "required", "add at least one line"))
	}
	out := make([]preparedLine, 0, len(in))
	for i, l := range in {
		f := fmt.Sprintf("lines[%d]", i)
		if l.SuggestedSupplierID != nil {
			if _, err := loadSupplier(ctx, tx, *l.SuggestedSupplierID); err != nil {
				return nil, err
			}
		}
		pl, err := prepareItemLine(ctx, tx, f, l.ItemID, l.UOMID, l.Description, l.Quantity, l.EstimatedUnitPrice, l.SuggestedSupplierID, on)
		if err != nil {
			return nil, err
		}
		if pl.NeededBy, err = parseDate(f+".neededBy", l.NeededBy); err != nil {
			return nil, err
		}
		pl.CostCenter, pl.WarehouseID, pl.Supplier, pl.Notes = nz(l.CostCenter), l.WarehouseID, l.SuggestedSupplierID, nz(l.Notes)
		out = append(out, pl)
	}
	return out, nil
}

// insertRequisitionLines appends lines after the highest line number and
// returns the estimated total of the inserted lines.
func insertRequisitionLines(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, cur string, lines []preparedLine) (decimal.Decimal, error) {
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(line_no), 0) FROM procurement.purchase_requisition_lines WHERE requisition_id = $1`, rid).Scan(&next); err != nil {
		return decimal.Zero, err
	}
	total := decimal.Zero
	for _, l := range lines {
		next++
		lt := money(l.Quantity.Mul(l.Price), cur)
		total = total.Add(lt)
		var base *string
		if l.BaseQty != nil {
			s := l.BaseQty.String()
			base = &s
		}
		src := l.SourceData
		if src == nil {
			src = map[string]any{}
		}
		raw, _ := json.Marshal(src)
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_requisition_lines (id, property_id, requisition_id, line_no, item_id, description,
			quantity, uom_id, base_quantity, estimated_unit_price, estimated_total, needed_by, cost_center, warehouse_id, suggested_supplier_id, source_data, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9::numeric,$10::numeric,$11::numeric,$12,$13,$14,$15,$16,$17)`,
			id.New(), property, rid, next, l.ItemID, l.Description, l.Quantity.String(), l.UOMID, base, l.Price.String(), lt.String(), l.NeededBy,
			l.CostCenter, l.WarehouseID, l.Supplier, raw, l.Notes); err != nil {
			return total, err
		}
	}
	return total, nil
}

func refreshRequisitionTotal(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET estimated_total = coalesce((SELECT sum(estimated_total)
		FROM procurement.purchase_requisition_lines WHERE requisition_id = $1 AND status = 'open'), 0) WHERE id = $1`, rid)
	return err
}

// CreateRequisition creates a manual (or unfulfilled store requisition) PR.
func (m *Module) CreateRequisition(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PurchaseRequisitionInput) (PurchaseRequisition, error) {
	source := in.Source
	if source == "" {
		source = "manual"
	}
	if source != "manual" && source != "store_requisition" {
		return PurchaseRequisition{}, handle.Invalid("source", "invalid", "manual or store_requisition (reorder and banquet requisitions are automatic)")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return PurchaseRequisition{}, err
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = cfg.DefaultCurrency
	}
	needed, err := parseDate("neededBy", in.NeededBy)
	if err != nil {
		return PurchaseRequisition{}, err
	}
	lines, err := m.prepareRequisitionLines(ctx, tx, in.Lines, localToday(ctx, tx, property))
	if err != nil {
		return PurchaseRequisition{}, err
	}
	num, err := nextNumber(ctx, tx, property, "PR")
	if err != nil {
		return PurchaseRequisition{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_requisitions (id, property_id, number, source, title, department_id, outlet_id, warehouse_id,
		cost_center, budget_code, category, needed_by, currency, notes, source_ref, requested_by, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,$16)`,
		rid, property, num, source, nz(in.Title), in.DepartmentID, in.OutletID, in.WarehouseID, nz(in.CostCenter), nz(in.BudgetCode), nz(in.Category),
		needed, cur, nz(in.Notes), nz(in.SourceRef), actor(ctx)); err != nil {
		if dbtx.IsForeignKeyViolation(err) {
			return PurchaseRequisition{}, handle.Invalid("departmentId", "not_found", "department not found")
		}
		return PurchaseRequisition{}, err
	}
	if _, err := insertRequisitionLines(ctx, tx, property, rid, cur, lines); err != nil {
		return PurchaseRequisition{}, err
	}
	if err := refreshRequisitionTotal(ctx, tx, rid); err != nil {
		return PurchaseRequisition{}, err
	}
	out, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.purchase_requisition",
		EntityID: rid.String(), EntityLabel: num, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if in.Submit {
		return m.SubmitRequisition(ctx, tx, rid)
	}
	return out, nil
}

// UpdateRequisition edits a draft (or rejected) requisition; lines are replaced.
func (m *Module) UpdateRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID, in PurchaseRequisitionInput) (PurchaseRequisition, error) {
	before, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" && before.Status != "rejected" {
		return before, errs.Conflict("requisition_not_editable", "only draft or rejected requisitions can be edited")
	}
	property := propertyOf(ctx)
	needed, err := parseDate("neededBy", in.NeededBy)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET title = coalesce($2, title), department_id = coalesce($3, department_id),
		outlet_id = coalesce($4, outlet_id), warehouse_id = coalesce($5, warehouse_id), cost_center = coalesce($6, cost_center),
		budget_code = coalesce($7, budget_code), category = coalesce($8, category), needed_by = coalesce($9, needed_by), notes = coalesce($10, notes),
		status = 'draft', rejected_reason = NULL, version = version + 1, updated_by = $11 WHERE id = $1`,
		rid, nz(in.Title), in.DepartmentID, in.OutletID, in.WarehouseID, nz(in.CostCenter), nz(in.BudgetCode), nz(in.Category), needed, nz(in.Notes),
		actor(ctx)); err != nil {
		return before, err
	}
	if len(in.Lines) > 0 {
		lines, err := m.prepareRequisitionLines(ctx, tx, in.Lines, localToday(ctx, tx, property))
		if err != nil {
			return before, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM procurement.purchase_requisition_lines WHERE requisition_id = $1`, rid); err != nil {
			return before, err
		}
		if _, err := insertRequisitionLines(ctx, tx, property, rid, before.Currency, lines); err != nil {
			return before, err
		}
	}
	if err := refreshRequisitionTotal(ctx, tx, rid); err != nil {
		return before, err
	}
	after, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionUpdate, EntityType: "procurement.purchase_requisition",
		EntityID: rid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after})
}

// SubmitRequisition sends a draft requisition to approval (FR-PR-03).
func (m *Module) SubmitRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PurchaseRequisition, error) {
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "draft" {
		return r, errs.Conflict("requisition_not_draft", "only draft requisitions can be submitted")
	}
	open := 0
	for _, l := range r.Lines {
		if l.Status == "open" {
			open++
		}
	}
	if open == 0 {
		return r, errs.Validation("lines_required", "the requisition has no lines", errs.Field("lines", "required", "add at least one line"))
	}
	property := propertyOf(ctx)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'submitted', submitted_at = now(),
		requested_by = coalesce(requested_by, $2), updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
		return r, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "submit", EntityType: "procurement.purchase_requisition",
		EntityID: rid.String(), EntityLabel: r.Number, PropertyID: &property, Before: map[string]any{"status": r.Status},
		After: map[string]any{"status": "submitted", "estimatedTotal": r.EstimatedTotal}}); err != nil {
		return r, err
	}
	attrs := amountAttrs(pol, dec(r.EstimatedTotal))
	attrs["source"], attrs["costCenter"], attrs["category"] = r.Source, deref(r.CostCenter), deref(r.Category)
	if r.DepartmentID != nil {
		attrs["departmentId"] = r.DepartmentID.String()
	}
	title := "Purchase Requisition " + r.Number
	if r.Title != nil {
		title += " – " + *r.Title
	}
	areq, _, err := m.submitApproval(ctx, tx, RequisitionDocumentType, rid, property, r.Number, title, attrs)
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET approval_request_id = $2 WHERE id = $1`, rid, areq); err != nil {
		return r, err
	}
	return GetRequisition(ctx, tx, rid)
}

func (m *Module) requisitionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	r, err := lockRequisition(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	if r.Status != "submitted" {
		return nil // edited or cancelled meanwhile
	}
	switch d.Status {
	case approval.StatusApproved:
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'approved', approved_at = now(), decided_at = now(),
			approved_amount = estimated_total WHERE id = $1`, r.ID); err != nil {
			return err
		}
		if err := m.publish(ctx, tx, EventRequisitionApproved, "procurement.purchase_requisition", r.ID, d.PropertyID, map[string]any{
			"requisitionId": r.ID, "number": r.Number, "source": r.Source, "currency": r.Currency, "estimatedTotal": r.EstimatedTotal,
			"warehouseId": r.WarehouseID, "eventId": r.EventID}); err != nil {
			return err
		}
	case approval.StatusRejected:
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'rejected', rejected_reason = $2, decided_at = now() WHERE id = $1`,
			r.ID, nz(d.Reason)); err != nil {
			return err
		}
	default: // the request was withdrawn: back to draft
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'draft', approval_request_id = NULL WHERE id = $1`, r.ID); err != nil {
			return err
		}
	}
	after := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected"}[d.Status]
	if after == "" {
		after = "draft"
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.purchase_requisition",
		EntityID: r.ID.String(), EntityLabel: r.Number, PropertyID: &d.PropertyID, Reason: d.Reason,
		Before: map[string]any{"status": r.Status}, After: map[string]any{"status": after}})
}

// CancelRequisition cancels a requisition with nothing ordered yet.
func (m *Module) CancelRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) (PurchaseRequisition, error) {
	if err := handle.Required("reason", reason); err != nil {
		return PurchaseRequisition{}, err
	}
	r, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	if r.Status == "cancelled" || r.Status == "ordered" || r.Status == "partially_ordered" {
		return r, errs.Conflict("requisition_not_cancellable", "a requisition that is ordered (or cancelled) cannot be cancelled")
	}
	var inRFQ bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l JOIN procurement.rfqs q ON q.id = l.rfq_id
		WHERE l.requisition_id = $1 AND q.status IN ('draft', 'sent', 'closed'))`, rid).Scan(&inRFQ); err != nil {
		return r, err
	}
	if inRFQ {
		return r, errs.Conflict("requisition_in_rfq", "the requisition is part of an open RFQ; cancel the RFQ first")
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'cancelled', cancelled_reason = $2, updated_by = $3 WHERE id = $1`,
		rid, reason, actor(ctx)); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisition_lines SET status = 'cancelled', cancel_reason = $2 WHERE requisition_id = $1
		AND status = 'open'`, rid, reason); err != nil {
		return r, err
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "cancel", EntityType: "procurement.purchase_requisition",
		EntityID: rid.String(), EntityLabel: r.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": r.Status},
		After: map[string]any{"status": "cancelled"}}); err != nil {
		return r, err
	}
	return GetRequisition(ctx, tx, rid)
}

// recomputeRequisition updates the ordered status of a requisition from
// its lines (FR-PR-02: Partially Ordered → Ordered).
func recomputeRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions r SET status = CASE
		WHEN NOT EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l WHERE l.requisition_id = r.id AND l.status = 'open'
		     AND l.ordered_quantity < l.quantity)
		  AND EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l WHERE l.requisition_id = r.id AND l.status = 'open') THEN 'ordered'
		WHEN EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l WHERE l.requisition_id = r.id AND l.ordered_quantity > 0) THEN 'partially_ordered'
		ELSE 'approved' END
		WHERE r.id = $1 AND r.status IN ('approved', 'partially_ordered', 'ordered')`, rid)
	return err
}

// requisitionLineSource is an open, orderable requisition line.
type requisitionLineSource struct {
	ID          uuid.UUID
	Requisition uuid.UUID
	Number      string
	ItemID      *uuid.UUID
	Description string
	OpenQty     decimal.Decimal
	UOMID       *uuid.UUID
	Price       decimal.Decimal
	NeededBy    *time.Time
	WarehouseID *uuid.UUID
	Supplier    *uuid.UUID
	RFQID       *uuid.UUID
	Currency    string
}

// orderableLines loads approved, open requisition lines for an RFQ or a PO
// (consolidation, FR-PR-05).
func orderableLines(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) ([]requisitionLineSource, error) {
	if len(ids) == 0 {
		return nil, errs.Validation("lines_required", "select requisition lines", errs.Field("requisitionLineIds", "required", "select requisition lines"))
	}
	rows, err := tx.Query(ctx, `SELECT l.id, l.requisition_id, r.number, l.item_id, l.description, (l.quantity - l.ordered_quantity)::text, l.uom_id,
		l.estimated_unit_price::text, l.needed_by, coalesce(l.warehouse_id, r.warehouse_id), l.suggested_supplier_id, l.rfq_id, r.currency, r.status, l.status
		FROM procurement.purchase_requisition_lines l JOIN procurement.purchase_requisitions r ON r.id = l.requisition_id
		WHERE l.id = ANY($1) ORDER BY r.number, l.line_no FOR UPDATE OF l`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []requisitionLineSource
	for rows.Next() {
		var s requisitionLineSource
		var open, price, prStatus, lineStatus string
		if err := rows.Scan(&s.ID, &s.Requisition, &s.Number, &s.ItemID, &s.Description, &open, &s.UOMID, &price, &s.NeededBy, &s.WarehouseID, &s.Supplier,
			&s.RFQID, &s.Currency, &prStatus, &lineStatus); err != nil {
			return nil, err
		}
		if prStatus != "approved" && prStatus != "partially_ordered" {
			return nil, errs.Validation("requisition_not_approved", "requisition "+s.Number+" is "+prStatus,
				errs.Field("requisitionLineIds", "not_approved", "only approved requisitions can be ordered"))
		}
		s.OpenQty, s.Price = dec(open), dec(price)
		if lineStatus != "open" || !s.OpenQty.IsPositive() {
			return nil, errs.Validation("line_not_open", "a line of "+s.Number+" is already ordered or cancelled",
				errs.Field("requisitionLineIds", "not_open", "the line has no open quantity"))
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(ids) {
		return nil, errs.Validation("line_not_found", "a requisition line was not found", errs.Field("requisitionLineIds", "not_found", "requisition line not found"))
	}
	return out, nil
}

// ── automatic requisitions (EP-08, K1) ──────────────────────────────────

// ReorderPayload is inventory.reorder_needed (docs/p3-p4-contracts.md).
type ReorderPayload struct {
	WarehouseID       uuid.UUID  `json:"warehouseId"`
	Source            string     `json:"source"` // reorder | requisition
	BusinessDate      string     `json:"businessDate"`
	RequisitionID     *uuid.UUID `json:"requisitionId"`
	RequisitionNumber string     `json:"requisitionNumber"`
	Items             []struct {
		ItemID              uuid.UUID  `json:"itemId"`
		ItemCode            string     `json:"itemCode"`
		OnHand              string     `json:"onHand"`
		OnOrder             string     `json:"onOrder"`
		ReorderPoint        string     `json:"reorderPoint"`
		ParLevel            string     `json:"parLevel"`
		SuggestedQuantity   string     `json:"suggestedQuantity"`
		UOMID               uuid.UUID  `json:"uomId"`
		PreferredSupplierID *uuid.UUID `json:"preferredSupplierId"`
	} `json:"items"`
}

// OnReorderNeeded turns inventory.reorder_needed into an automatic
// purchase requisition (FR-RPL-02, FR-PR-01). Inventory requests par −
// stock − on order once per warehouse × item × business day (its open
// requests are the "on order"), so the suggested quantity is requested as
// is: reorder requests go to the open draft reorder requisition of the
// warehouse, a store requisition the store cannot supply gets its own
// requisition. A repeated delivery of the same request (warehouse, item,
// business day / store requisition) is ignored.
func (m *Module) OnReorderNeeded(ctx context.Context, tx pgx.Tx, property uuid.UUID, p ReorderPayload) (*PurchaseRequisition, error) {
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil || pol.AutoRequisitionMode == "off" {
		return nil, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	today := localToday(ctx, tx, property)
	day := strings.TrimSpace(p.BusinessDate)
	if day == "" {
		day = today.Format("2006-01-02")
	}
	fromStore := p.Source == "requisition"
	ref := "reorder " + day
	if fromStore {
		ref = p.RequisitionNumber
		if ref == "" && p.RequisitionID != nil {
			ref = p.RequisitionID.String()
		}
	}
	var lines []preparedLine
	for i, it := range p.Items {
		want := dec(it.SuggestedQuantity).Round(6)
		if !want.IsPositive() {
			continue
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l JOIN procurement.purchase_requisitions r
			ON r.id = l.requisition_id WHERE r.property_id = $1 AND r.source = $2 AND coalesce(l.warehouse_id, r.warehouse_id) = $3 AND l.item_id = $4
			AND l.source_data->>'requestKey' = $5)`, property, map[bool]string{true: "store_requisition", false: "reorder"}[fromStore], p.WarehouseID,
			it.ItemID, ref).Scan(&dup); err != nil {
			return nil, err
		}
		if dup {
			continue
		}
		uom := it.UOMID
		var uomp *uuid.UUID
		if uom != uuid.Nil {
			uomp = &uom
		}
		pl, err := prepareItemLine(ctx, tx, fmt.Sprintf("items[%d]", i), &it.ItemID, uomp, "", want.String(), "", it.PreferredSupplierID, today)
		if err != nil {
			return nil, fmt.Errorf("reorder item %s: %w", it.ItemID, err)
		}
		pl.WarehouseID, pl.Supplier = &p.WarehouseID, it.PreferredSupplierID
		pl.SourceData = map[string]any{"requestKey": ref, "businessDate": day, "onHand": it.OnHand, "onOrder": it.OnOrder, "reorderPoint": it.ReorderPoint,
			"parLevel": it.ParLevel, "suggestedQuantity": it.SuggestedQuantity}
		lines = append(lines, pl)
	}
	if len(lines) == 0 {
		return nil, nil
	}
	var rid uuid.UUID
	created := false
	if !fromStore {
		err = tx.QueryRow(ctx, `SELECT id FROM procurement.purchase_requisitions WHERE property_id = $1 AND warehouse_id = $2 AND source = 'reorder'
			AND status = 'draft' FOR UPDATE`, property, p.WarehouseID).Scan(&rid)
		if err != nil && !dbtx.IsNoRows(err) {
			return nil, err
		}
	}
	if rid == uuid.Nil {
		num, err := nextNumber(ctx, tx, property, "PR")
		if err != nil {
			return nil, err
		}
		rid, created = id.New(), true
		source, title := "reorder", "Reorder "+day
		if fromStore {
			source, title = "store_requisition", "Store requisition "+ref+" (not available in store)"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_requisitions (id, property_id, number, source, title, warehouse_id, currency, source_ref)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, rid, property, num, source, title, p.WarehouseID, cfg.DefaultCurrency, ref); err != nil {
			return nil, err
		}
	}
	if _, err := insertRequisitionLines(ctx, tx, property, rid, cfg.DefaultCurrency, lines); err != nil {
		return nil, err
	}
	if err := refreshRequisitionTotal(ctx, tx, rid); err != nil {
		return nil, err
	}
	r, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return nil, err
	}
	action := audit.ActionUpdate
	if created {
		action = audit.ActionCreate
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: action, Category: audit.CategorySystem, ActorName: "Inventory (inventory.reorder_needed)",
		EntityType: "procurement.purchase_requisition", EntityID: rid.String(), EntityLabel: r.Number, PropertyID: &property, After: r,
		Metadata: map[string]any{"source": p.Source, "requestKey": ref, "lines": len(lines)}}); err != nil {
		return nil, err
	}
	reason := "Reorder point"
	if fromStore {
		reason = "Store requisition " + ref
	}
	return &r, m.notifyHolders(ctx, tx, property, "procurement.requisition.submit", "procurement.requisition_review",
		map[string]any{"number": r.Number, "lines": len(r.Lines), "reason": reason}, "/procurement/requisitions?open="+rid.String())
}

// BEOPayload is banquet.beo_issued / banquet.beo_revised (K1).
type BEOPayload struct {
	BEOID        uuid.UUID  `json:"beoId"`
	BEONumber    string     `json:"beoNumber"`
	Version      int        `json:"version"`
	EventID      uuid.UUID  `json:"eventId"`
	EventNumber  string     `json:"eventNumber"`
	EventDate    string     `json:"eventDate"`
	Pax          int        `json:"pax"`
	OutletID     *uuid.UUID `json:"outletId"`
	Requirements []struct {
		ItemID   uuid.UUID  `json:"itemId"`
		ItemCode string     `json:"itemCode"`
		ItemName string     `json:"itemName"`
		Quantity string     `json:"quantity"`
		UOMID    uuid.UUID  `json:"uomId"`
		UOM      string     `json:"uom"`
		NeededBy string     `json:"neededBy"`
		Source   string     `json:"source"`
		RecipeID *uuid.UUID `json:"recipeId"`
	} `json:"requirements"`
}

// OnBEO maintains the banquet material requisition of a BEO (FR-PR-04):
// the issued BEO creates a draft PR; a revision carries the full
// requirement list of the new version and replaces the open (not yet
// ordered) lines. Quantities already ordered count against the new
// requirement; a requisition revised after ordering is flagged and the
// additional quantity goes to a new requisition for approval.
func (m *Module) OnBEO(ctx context.Context, tx pgx.Tx, property uuid.UUID, p BEOPayload, revised bool) (*PurchaseRequisition, error) {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	eventDate, _ := parseDate("eventDate", p.EventDate)
	// Requisitions of the BEO, oldest first; skip versions already applied.
	rows, err := tx.Query(ctx, `SELECT id, status, coalesce(beo_version, 0) FROM procurement.purchase_requisitions WHERE property_id = $1 AND beo_id = $2
		AND source = 'banquet' AND status <> 'cancelled' ORDER BY created_at FOR UPDATE`, property, p.BEOID)
	if err != nil {
		return nil, err
	}
	type prRow struct {
		id      uuid.UUID
		status  string
		version int
	}
	var prs []prRow
	for rows.Next() {
		var x prRow
		if err := rows.Scan(&x.id, &x.status, &x.version); err != nil {
			rows.Close()
			return nil, err
		}
		prs = append(prs, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, x := range prs {
		if x.version >= p.Version && p.Version > 0 {
			return nil, nil // already applied (out-of-order or repeated delivery)
		}
	}
	reason := fmt.Sprintf("replaced by BEO %s v%d", p.BEONumber, p.Version)
	// Committed (ordered) quantity per item in base UOM, and the remainder of
	// partially ordered lines closed: they are replaced by the new list.
	committed := map[uuid.UUID]decimal.Decimal{}
	ordered := false
	var editable *prRow
	for i := range prs {
		x := prs[i]
		lrows, err := tx.Query(ctx, `SELECT id, item_id, quantity::text, ordered_quantity::text, coalesce(base_quantity, quantity)::text
			FROM procurement.purchase_requisition_lines WHERE requisition_id = $1 AND status = 'open'`, x.id)
		if err != nil {
			return nil, err
		}
		type ln struct {
			id       uuid.UUID
			item     *uuid.UUID
			qty, ord decimal.Decimal
			base     decimal.Decimal
		}
		var ls []ln
		for lrows.Next() {
			var l ln
			var q, o, b string
			if err := lrows.Scan(&l.id, &l.item, &q, &o, &b); err != nil {
				lrows.Close()
				return nil, err
			}
			l.qty, l.ord, l.base = dec(q), dec(o), dec(b)
			ls = append(ls, l)
		}
		lrows.Close()
		if err := lrows.Err(); err != nil {
			return nil, err
		}
		for _, l := range ls {
			if l.ord.IsPositive() {
				ordered = true
				if l.item != nil && l.qty.IsPositive() {
					committed[*l.item] = committed[*l.item].Add(l.base.Mul(l.ord).Div(l.qty))
				}
				if l.ord.LessThan(l.qty) { // close the open remainder
					if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisition_lines SET quantity = ordered_quantity,
						base_quantity = base_quantity * ordered_quantity / quantity, estimated_total = round(estimated_unit_price * ordered_quantity, 4),
						notes = coalesce(notes || ' · ', '') || $2 WHERE id = $1`, l.id, reason); err != nil {
						return nil, err
					}
				}
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisition_lines SET status = 'cancelled', cancel_reason = $2, rfq_id = NULL WHERE id = $1`,
				l.id, reason); err != nil {
				return nil, err
			}
		}
		if err := refreshRequisitionTotal(ctx, tx, x.id); err != nil {
			return nil, err
		}
		if err := recomputeRequisition(ctx, tx, x.id); err != nil {
			return nil, err
		}
		if x.status == "draft" || x.status == "submitted" || x.status == "approved" || x.status == "rejected" {
			var any bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines WHERE requisition_id = $1 AND ordered_quantity > 0)`,
				x.id).Scan(&any); err != nil {
				return nil, err
			}
			if !any {
				editable = &prs[i]
			}
		}
	}
	// New open lines: requirement − committed.
	today := localToday(ctx, tx, property)
	var lines []preparedLine
	for i, r := range p.Requirements {
		qty := dec(r.Quantity)
		if c, ok := committed[r.ItemID]; ok && c.IsPositive() {
			base, _, err := inventory.ProcurementToBase(ctx, tx, r.ItemID, decimal.NewFromInt(1), r.UOMID)
			if err != nil {
				return nil, err
			}
			if base.IsPositive() {
				qty = qty.Sub(c.Div(base))
			}
		}
		qty = qty.Round(6)
		if !qty.IsPositive() {
			continue
		}
		uom := r.UOMID
		pl, err := prepareItemLine(ctx, tx, fmt.Sprintf("requirements[%d]", i), &r.ItemID, &uom, r.ItemName, qty.String(), "", nil, today)
		if err != nil {
			return nil, err
		}
		if pl.NeededBy, _ = parseDate("neededBy", r.NeededBy); pl.NeededBy == nil {
			pl.NeededBy = eventDate
		}
		pl.SourceData = map[string]any{"beoId": p.BEOID, "beoVersion": p.Version, "requirement": r.Quantity, "source": r.Source, "recipeId": r.RecipeID}
		lines = append(lines, pl)
	}
	title := fmt.Sprintf("Banquet %s – %s (%d pax)", p.EventNumber, p.BEONumber, p.Pax)
	var rid uuid.UUID
	created := false
	switch {
	case editable != nil:
		rid = editable.id
	case len(lines) > 0:
		num, err := nextNumber(ctx, tx, property, "PR")
		if err != nil {
			return nil, err
		}
		rid, created = id.New(), true
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_requisitions (id, property_id, number, source, title, outlet_id, needed_by, currency,
			source_ref, event_id, beo_id, beo_version, event_date) VALUES ($1,$2,$3,'banquet',$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			rid, property, num, title, p.OutletID, eventDate, cfg.DefaultCurrency, p.BEONumber, p.EventID, p.BEOID, p.Version, eventDate); err != nil {
			return nil, err
		}
	default:
		if len(prs) == 0 {
			return nil, nil
		}
		rid = prs[len(prs)-1].id
	}
	if len(lines) > 0 {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT currency FROM procurement.purchase_requisitions WHERE id = $1`, rid).Scan(&cur); err != nil {
			return nil, err
		}
		if _, err := insertRequisitionLines(ctx, tx, property, rid, cur, lines); err != nil {
			return nil, err
		}
	}
	var attention *string
	if revised && ordered {
		a := fmt.Sprintf("BEO %s revised to v%d after ordering: check the ordered quantities", p.BEONumber, p.Version)
		attention = &a
	}
	// An approved requisition whose lines changed goes back for approval.
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET beo_version = $2, event_date = $3, title = $4,
		status = CASE WHEN status IN ('approved', 'rejected') AND NOT EXISTS (SELECT 1 FROM procurement.purchase_requisition_lines l
		  WHERE l.requisition_id = $1 AND l.ordered_quantity > 0) AND $5 THEN 'draft' ELSE status END,
		version = version + CASE WHEN $5 THEN 1 ELSE 0 END WHERE id = $1`, rid, p.Version, eventDate, title, revised); err != nil {
		return nil, err
	}
	if attention != nil {
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET attention = $2 WHERE beo_id = $1 AND source = 'banquet'
			AND status IN ('partially_ordered', 'ordered')`, p.BEOID, *attention); err != nil {
			return nil, err
		}
	}
	if err := refreshRequisitionTotal(ctx, tx, rid); err != nil {
		return nil, err
	}
	r, err := GetRequisition(ctx, tx, rid)
	if err != nil {
		return nil, err
	}
	action := audit.ActionUpdate
	if created {
		action = audit.ActionCreate
	}
	name := "Banquet BEO (banquet.beo_issued)"
	if revised {
		name = "Banquet BEO (banquet.beo_revised)"
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: action, Category: audit.CategorySystem, ActorName: name,
		EntityType: "procurement.purchase_requisition", EntityID: rid.String(), EntityLabel: r.Number, PropertyID: &property, After: r,
		Metadata: map[string]any{"beoId": p.BEOID, "beoVersion": p.Version}}); err != nil {
		return nil, err
	}
	why := "Banquet BEO " + p.BEONumber
	if revised {
		why += " revised"
	}
	return &r, m.notifyHolders(ctx, tx, property, "procurement.requisition.submit", "procurement.requisition_review",
		map[string]any{"number": r.Number, "lines": len(r.Lines), "reason": why}, "/procurement/requisitions?open="+rid.String())
}

// ── routes ───────────────────────────────────────────────────────────────

func (m *Module) registerRequisitions(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Purchase Requisitions", route.ScopeProperty
		reg.Add(rt)
	}
	const base = "/api/v1/procurement/requisitions"
	add(route.Route{Method: http.MethodGet, Path: base, Summary: "Purchase requisitions (status, source, aging)", Permission: "procurement.requisition.view",
		Response: PurchaseRequisition{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[source]"},
			{Name: "filter[warehouseId]"}, {Name: "filter[eventId]"}, {Name: "filter[beoId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseRequisition], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PurchaseRequisition](tx.Query(ctx, requisitionSelect+` WHERE r.property_id = $1
				AND ($2 = '' OR r.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR r.source = $3) AND ($4 = '' OR r.warehouse_id::text = $4)
				AND ($5 = '' OR r.event_id::text = $5) AND ($6 = '' OR r.beo_id::text = $6)
				AND ($7 = '' OR r.number ILIKE '%' || $7 || '%' OR r.title ILIKE '%' || $7 || '%' OR r.source_ref ILIKE '%' || $7 || '%')
				ORDER BY r.created_at DESC LIMIT $8`, handle.Property(ctx), lp.Filters["status"], lp.Filters["source"], lp.Filters["warehouseId"],
				lp.Filters["eventId"], lp.Filters["beoId"], lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: base, Summary: "Create Purchase Requisition (manual or unfulfilled store requisition)",
		Permission: "procurement.requisition.create", Request: PurchaseRequisitionInput{}, Response: PurchaseRequisition{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in PurchaseRequisitionInput) (PurchaseRequisition, error) {
			return m.CreateRequisition(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Purchase requisition with lines", Permission: "procurement.requisition.view",
		Response: PurchaseRequisition{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PurchaseRequisition, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PurchaseRequisition{}, err
			}
			return GetRequisition(ctx, tx, rid)
		})})
	add(route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a draft / rejected requisition (lines replaced when given)",
		Permission: "procurement.requisition.update", Request: PurchaseRequisitionInput{}, Response: PurchaseRequisition{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PurchaseRequisitionInput) (PurchaseRequisition, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PurchaseRequisition{}, err
			}
			return m.UpdateRequisition(ctx, tx, rid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:submit", Summary: "Submit Requisition (approval matrix by amount)",
		Permission: "procurement.requisition.submit", Response: PurchaseRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PurchaseRequisition, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PurchaseRequisition{}, err
			}
			return m.SubmitRequisition(ctx, tx, rid)
		})})
	for _, act := range []struct {
		path, summary string
		approve       bool
	}{{":approve", "Approve Requisition (current approval step)", true}, {":reject", "Reject Requisition (reason required)", false}} {
		act := act
		add(route.Route{Method: http.MethodPost, Path: base + "/{id}" + act.path, Summary: act.summary, Permission: "procurement.requisition.approve",
			Request: ProcurementReasonInput{}, Response: PurchaseRequisition{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementReasonInput) (PurchaseRequisition, error) {
				rid, err := handle.ID(r)
				if err != nil {
					return PurchaseRequisition{}, err
				}
				pr, err := GetRequisition(ctx, tx, rid)
				if err != nil {
					return pr, err
				}
				if pr.Status != "submitted" {
					return pr, errs.Conflict("requisition_not_submitted", "the requisition is not waiting for approval")
				}
				if err := m.decide(ctx, tx, pr.ApprovalRequestID, act.approve, in.Reason); err != nil {
					return pr, err
				}
				return GetRequisition(ctx, tx, rid)
			})})
	}
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a requisition (nothing ordered yet)",
		Permission: "procurement.requisition.cancel", Request: ProcurementReasonInput{}, Response: PurchaseRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementReasonInput) (PurchaseRequisition, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PurchaseRequisition{}, err
			}
			return m.CancelRequisition(ctx, tx, rid, in.Reason)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/requisition-lines", Summary: "Open requisition lines to consolidate into an RFQ / PO",
		Permission: "procurement.requisition.view", Response: PurchaseRequisitionLine{}, List: true,
		Query: []route.Param{{Name: "filter[itemId]"}, {Name: "filter[supplierId]"}, {Name: "filter[warehouseId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseRequisitionLine], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PurchaseRequisitionLine](tx.Query(ctx, requisitionLineSelect+`
				JOIN procurement.purchase_requisitions r ON r.id = l.requisition_id
				WHERE r.property_id = $1 AND r.status IN ('approved', 'partially_ordered') AND l.status = 'open' AND l.ordered_quantity < l.quantity
				AND (l.rfq_id IS NULL OR NOT EXISTS (SELECT 1 FROM procurement.rfqs q WHERE q.id = l.rfq_id AND q.status IN ('draft', 'sent', 'closed', 'awarded')))
				AND ($2 = '' OR l.item_id::text = $2) AND ($3 = '' OR l.suggested_supplier_id::text = $3)
				AND ($4 = '' OR coalesce(l.warehouse_id, r.warehouse_id)::text = $4)
				ORDER BY l.needed_by NULLS LAST, r.number, l.line_no LIMIT $5`, handle.Property(ctx), lp.Filters["itemId"], lp.Filters["supplierId"],
				lp.Filters["warehouseId"], lp.Limit)))
		})})
}
