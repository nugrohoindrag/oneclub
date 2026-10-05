package procurement

// PRD P4 EP-14 Goods Receipt & Purchase Return (ops Warehouse "Goods
// Receipt", PRD P4 §6 #8): receipt against PO lines, partial, accepted /
// rejected quantity with reason, batch / expiry / serial capture, delivery
// note photos, over-receipt tolerance (rejected or sent for approval),
// receipt without PO for allowed categories with approval, conversion to
// the stock UOM (inventory root conversion). Posting publishes
// procurement.goods_received (inventory posts the stock, accounting the
// GRNI). A purchase return publishes procurement.purchase_returned (stock
// out) and issues a debit note (procurement.debit_note_issued, AP).

import (
	"bytes"
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
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PurchaseGoodsReceipt is a goods receipt (GR).
type PurchaseGoodsReceipt struct {
	ID                uuid.UUID                  `json:"id" db:"id"`
	Number            string                     `json:"number" db:"number"`
	PurchaseOrderID   *uuid.UUID                 `json:"purchaseOrderId" db:"purchase_order_id"`
	PONumber          *string                    `json:"poNumber" db:"po_number"`
	SupplierID        uuid.UUID                  `json:"supplierId" db:"supplier_id"`
	SupplierName      string                     `json:"supplierName" db:"supplier_name"`
	WarehouseID       uuid.UUID                  `json:"warehouseId" db:"warehouse_id"`
	ReceivedDate      string                     `json:"receivedDate" db:"received_date"`
	DeliveryNoteNo    *string                    `json:"deliveryNoteNo" db:"delivery_note_no"`
	Notes             *string                    `json:"notes" db:"notes"`
	Status            string                     `json:"status" db:"status" enum:"pending_approval,posted,rejected"`
	ApprovalReason    *string                    `json:"approvalReason" db:"approval_reason" enum:"over_receipt,without_po"`
	ApprovalRequestID *uuid.UUID                 `json:"approvalRequestId" db:"approval_request_id"`
	Currency          string                     `json:"currency" db:"currency"`
	Total             string                     `json:"total" db:"total"`
	AttachmentFileIDs []uuid.UUID                `json:"attachmentFileIds" db:"attachment_file_ids"`
	ReceivedBy        *uuid.UUID                 `json:"receivedBy" db:"received_by"`
	PostedAt          *time.Time                 `json:"postedAt" db:"posted_at"`
	CreatedAt         time.Time                  `json:"createdAt" db:"created_at"`
	Lines             []PurchaseGoodsReceiptLine `json:"lines,omitempty" db:"-"`
}

// PurchaseGoodsReceiptLine is one received line.
type PurchaseGoodsReceiptLine struct {
	ID                  uuid.UUID  `json:"id" db:"id"`
	LineNo              int        `json:"lineNo" db:"line_no"`
	PurchaseOrderLineID *uuid.UUID `json:"purchaseOrderLineId" db:"purchase_order_line_id"`
	ItemID              *uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode            *string    `json:"itemCode" db:"item_code"`
	Description         string     `json:"description" db:"description"`
	UOMID               *uuid.UUID `json:"uomId" db:"uom_id"`
	UOM                 *string    `json:"uom" db:"uom"`
	DeliveredQuantity   string     `json:"deliveredQuantity" db:"delivered_quantity"`
	AcceptedQuantity    string     `json:"acceptedQuantity" db:"accepted_quantity"`
	RejectedQuantity    string     `json:"rejectedQuantity" db:"rejected_quantity"`
	RejectionReason     *string    `json:"rejectionReason" db:"rejection_reason"`
	BaseQuantity        string     `json:"baseQuantity" db:"base_quantity"`
	UnitCost            string     `json:"unitCost" db:"unit_cost"`
	BaseUnitCost        string     `json:"baseUnitCost" db:"base_unit_cost"`
	TotalCost           string     `json:"totalCost" db:"total_cost"`
	TaxPercent          string     `json:"taxPercent" db:"tax_percent"`
	TaxCode             *string    `json:"taxCode" db:"tax_code"`
	BatchNo             *string    `json:"batchNo" db:"batch_no"`
	ExpiryDate          *string    `json:"expiryDate" db:"expiry_date"`
	SerialNos           []string   `json:"serialNos" db:"serial_nos"`
	ReturnedQuantity    string     `json:"returnedQuantity" db:"returned_quantity"`
	Notes               *string    `json:"notes" db:"notes"`
}

// PurchaseReceiptLineInput is one received line.
type PurchaseReceiptLineInput struct {
	PurchaseOrderLineID *uuid.UUID `json:"purchaseOrderLineId,omitempty"`
	ItemID              *uuid.UUID `json:"itemId,omitempty" doc:"Receipt without PO"`
	Description         string     `json:"description,omitempty"`
	UOMID               *uuid.UUID `json:"uomId,omitempty" doc:"Receipt without PO (default purchase UOM)"`
	UnitCost            string     `json:"unitCost,omitempty" doc:"Receipt without PO (default standard cost)"`
	AcceptedQuantity    string     `json:"acceptedQuantity"`
	RejectedQuantity    string     `json:"rejectedQuantity,omitempty"`
	RejectionReason     string     `json:"rejectionReason,omitempty" doc:"Required with a rejected quantity"`
	BatchNo             string     `json:"batchNo,omitempty"`
	ExpiryDate          string     `json:"expiryDate,omitempty"`
	SerialNos           []string   `json:"serialNos,omitempty"`
	Notes               string     `json:"notes,omitempty"`
}

// PurchaseReceiptInput is a goods receipt.
type PurchaseReceiptInput struct {
	PurchaseOrderID   *uuid.UUID                 `json:"purchaseOrderId,omitempty" doc:"Empty: receipt without PO (allowed categories, approval)"`
	SupplierID        *uuid.UUID                 `json:"supplierId,omitempty" doc:"Receipt without PO"`
	WarehouseID       *uuid.UUID                 `json:"warehouseId,omitempty" doc:"Default: the PO's receiving warehouse"`
	ReceivedDate      string                     `json:"receivedDate,omitempty"`
	DeliveryNoteNo    string                     `json:"deliveryNoteNo,omitempty" doc:"Surat jalan"`
	Notes             string                     `json:"notes,omitempty"`
	Reason            string                     `json:"reason,omitempty" doc:"Justification of a receipt without PO / over-receipt"`
	AttachmentFileIDs []uuid.UUID                `json:"attachmentFileIds,omitempty" doc:"Delivery note photos / documents (platform files)"`
	Lines             []PurchaseReceiptLineInput `json:"lines"`
}

const receiptSelect = `SELECT g.id, g.number, g.purchase_order_id, o.number AS po_number, g.supplier_id, s.name AS supplier_name, g.warehouse_id,
	to_char(g.received_date, 'YYYY-MM-DD') AS received_date, g.delivery_note_no, g.notes, g.status, g.approval_reason, g.approval_request_id, g.currency,
	trim_scale(g.total)::text AS total, g.attachment_file_ids, g.received_by, g.posted_at, g.created_at
	FROM procurement.goods_receipts g JOIN procurement.suppliers s ON s.id = g.supplier_id LEFT JOIN procurement.purchase_orders o ON o.id = g.purchase_order_id`

const receiptLineSelect = `SELECT l.id, l.line_no, l.purchase_order_line_id, l.item_id, i.code AS item_code, l.description, l.uom_id, u.code AS uom,
	trim_scale(l.delivered_quantity)::text AS delivered_quantity, trim_scale(l.accepted_quantity)::text AS accepted_quantity,
	trim_scale(l.rejected_quantity)::text AS rejected_quantity, l.rejection_reason, trim_scale(l.base_quantity)::text AS base_quantity,
	trim_scale(l.unit_cost)::text AS unit_cost, trim_scale(l.base_unit_cost)::text AS base_unit_cost, trim_scale(l.total_cost)::text AS total_cost,
	trim_scale(l.tax_percent)::text AS tax_percent, l.tax_code, l.batch_no, to_char(l.expiry_date, 'YYYY-MM-DD') AS expiry_date, l.serial_nos,
	trim_scale(l.returned_quantity)::text AS returned_quantity, l.notes
	FROM procurement.goods_receipt_lines l LEFT JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.uoms u ON u.id = l.uom_id`

// GetReceipt loads a goods receipt with lines.
func GetReceipt(ctx context.Context, q dbtx.Querier, gid uuid.UUID) (PurchaseGoodsReceipt, error) {
	g, err := oneOf[PurchaseGoodsReceipt]("goods receipt")(q.Query(ctx, receiptSelect+` WHERE g.id = $1`, gid))
	if err != nil {
		return g, err
	}
	g.Lines, err = handle.List[PurchaseGoodsReceiptLine](q.Query(ctx, receiptLineSelect+` WHERE l.goods_receipt_id = $1 ORDER BY l.line_no`, gid))
	return g, err
}

type receiptLine struct {
	poLine            *uuid.UUID
	item, uom         *uuid.UUID
	desc              string
	delivered, accept decimal.Decimal
	rejected          decimal.Decimal
	rejectReason      *string
	base, unitCost    decimal.Decimal
	baseUnitCost      decimal.Decimal
	totalCost         decimal.Decimal
	taxPct            decimal.Decimal
	taxCode           *string
	batch             *string
	expiry            *time.Time
	serials           []string
	notes             *string
}

func parseReceiptQty(f string, in PurchaseReceiptLineInput) (decimal.Decimal, decimal.Decimal, error) {
	acc, err := nonNegative(f+".acceptedQuantity", in.AcceptedQuantity, decimal.Zero)
	if err != nil {
		return acc, decimal.Zero, err
	}
	rej, err := nonNegative(f+".rejectedQuantity", in.RejectedQuantity, decimal.Zero)
	if err != nil {
		return acc, rej, err
	}
	if !acc.Add(rej).IsPositive() {
		return acc, rej, handle.Invalid(f+".acceptedQuantity", "required", "receive a positive quantity")
	}
	if rej.IsPositive() && strings.TrimSpace(in.RejectionReason) == "" {
		return acc, rej, handle.Invalid(f+".rejectionReason", "required", "give the reason of the rejected quantity")
	}
	if len(in.SerialNos) > 0 && (!acc.IsInteger() || int64(len(in.SerialNos)) != acc.IntPart()) {
		return acc, rej, handle.Invalid(f+".serialNos", "count", "one serial number per accepted unit")
	}
	return acc, rej, nil
}

// ReceiveGoods records a goods receipt (FR-GR-01..03). Within tolerance it
// is posted at once; over-receipt (policy "approval") and receipts without
// PO wait for approval.
func (m *Module) ReceiveGoods(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PurchaseReceiptInput) (PurchaseGoodsReceipt, error) {
	if len(in.Lines) == 0 {
		return PurchaseGoodsReceipt{}, errs.Validation("lines_required", "receive at least one line", errs.Field("lines", "required", "receive at least one line"))
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	today := localToday(ctx, tx, property)
	rdate, err := parseDate("receivedDate", in.ReceivedDate)
	if err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	if rdate == nil {
		rdate = &today
	}
	if err := m.openPeriod(ctx, tx, property, *rdate, "receivedDate"); err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	var lines []receiptLine
	var supplier uuid.UUID
	var warehouse *uuid.UUID
	var cur string
	approvalReason := ""
	maxExcess := decimal.Zero
	if in.PurchaseOrderID != nil {
		o, err := lockOrder(ctx, tx, *in.PurchaseOrderID)
		if err != nil {
			return PurchaseGoodsReceipt{}, err
		}
		if o.OrderType != "goods" {
			return PurchaseGoodsReceipt{}, errs.Conflict("service_order", "services are confirmed on the order (:confirm-service), not received")
		}
		if o.Status != "approved" && o.Status != "sent" && o.Status != "partially_received" {
			return PurchaseGoodsReceipt{}, errs.Conflict("order_not_receivable", "only approved, sent or partially received orders can be received (status "+o.Status+")")
		}
		supplier, warehouse, cur = o.SupplierID, o.WarehouseID, o.Currency
		if in.WarehouseID != nil {
			warehouse = in.WarehouseID
		}
		seen := map[uuid.UUID]bool{}
		for i, x := range in.Lines {
			f := fmt.Sprintf("lines[%d]", i)
			if x.PurchaseOrderLineID == nil {
				return PurchaseGoodsReceipt{}, handle.Invalid(f+".purchaseOrderLineId", "required", "select the order line")
			}
			var pl *PurchaseOrderLine
			for j := range o.Lines {
				if o.Lines[j].ID == *x.PurchaseOrderLineID {
					pl = &o.Lines[j]
				}
			}
			if pl == nil || seen[pl.ID] {
				return PurchaseGoodsReceipt{}, handle.Invalid(f+".purchaseOrderLineId", "not_found", "not a line of this order (or repeated)")
			}
			seen[pl.ID] = true
			acc, rej, err := parseReceiptQty(f, x)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			effective := dec(pl.Quantity).Sub(dec(pl.CancelledQuantity))
			newNet := dec(pl.ReceivedQuantity).Sub(dec(pl.ReturnedQuantity)).Add(acc)
			if effective.IsPositive() && newNet.GreaterThan(effective.Mul(tolerance(pol.OverReceiptTolerancePercent))) {
				excess := newNet.Div(effective).Sub(decimal.NewFromInt(1)).Mul(hundred).Round(2)
				if pol.OverReceiptAction != "approval" {
					return PurchaseGoodsReceipt{}, errs.Validation("over_receipt", "the receipt exceeds the ordered quantity beyond the tolerance (Procurement Policies)",
						errs.Field(f+".acceptedQuantity", "over_receipt", fmt.Sprintf("outstanding %s %s", pl.OutstandingQuantity, deref(pl.UOM))))
				}
				approvalReason = "over_receipt"
				if excess.GreaterThan(maxExcess) {
					maxExcess = excess
				}
			} else if !effective.IsPositive() && acc.IsPositive() {
				return PurchaseGoodsReceipt{}, handle.Invalid(f+".purchaseOrderLineId", "cancelled", "the line is cancelled")
			}
			unit := netPrice(dec(pl.UnitPrice), dec(pl.DiscountPercent))
			base := acc
			if pl.BaseQuantity != nil && dec(pl.Quantity).IsPositive() {
				base = dec(*pl.BaseQuantity).Mul(acc).Div(dec(pl.Quantity)).Round(6)
			}
			total := money(acc.Mul(unit), cur)
			rl := receiptLine{poLine: &pl.ID, item: pl.ItemID, uom: pl.UOMID, desc: pl.Description, accept: acc, rejected: rej, delivered: acc.Add(rej),
				rejectReason: nz(x.RejectionReason), base: base, unitCost: unit, totalCost: total, taxPct: dec(pl.TaxPercent), taxCode: pl.TaxCode,
				batch: nz(x.BatchNo), serials: x.SerialNos, notes: nz(x.Notes)}
			if base.IsPositive() {
				rl.baseUnitCost = total.Div(base).Round(6)
			}
			if rl.expiry, err = parseDate(f+".expiryDate", x.ExpiryDate); err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			lines = append(lines, rl)
		}
	} else {
		// FR-GR-03: receipt without PO, allowed categories only, with approval.
		if in.SupplierID == nil {
			return PurchaseGoodsReceipt{}, handle.Invalid("supplierId", "required", "select the supplier (or the purchase order)")
		}
		if err := handle.Required("reason", in.Reason); err != nil {
			return PurchaseGoodsReceipt{}, err
		}
		s, err := usableSupplier(ctx, tx, *in.SupplierID)
		if err != nil {
			return PurchaseGoodsReceipt{}, err
		}
		supplier, warehouse, cur, approvalReason = s.ID, in.WarehouseID, s.Currency, "without_po"
		allowed := map[string]bool{}
		for _, c := range pol.ReceiptWithoutPOCategories {
			allowed[strings.ToLower(strings.TrimSpace(c))] = true
		}
		for i, x := range in.Lines {
			f := fmt.Sprintf("lines[%d]", i)
			if x.ItemID == nil {
				return PurchaseGoodsReceipt{}, handle.Invalid(f+".itemId", "required", "select the item")
			}
			it, err := inventory.ProcurementItemByID(ctx, tx, *x.ItemID)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			// Allowed by item type, legacy category text, or the code / name
			// of the item category or one of its parents.
			var cats []string
			if err := tx.QueryRow(ctx, `WITH RECURSIVE c AS (SELECT ic.id, ic.parent_id, ic.code, ic.name FROM inventory.item_categories ic
				JOIN inventory.items i ON i.category_id = ic.id WHERE i.id = $1
				UNION ALL SELECT p.id, p.parent_id, p.code, p.name FROM inventory.item_categories p JOIN c ON c.parent_id = p.id)
				SELECT coalesce(array_agg(lower(code)) || array_agg(lower(name)), '{}') FROM c`, *x.ItemID).Scan(&cats); err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			ok := allowed[strings.ToLower(it.Category)] || allowed[strings.ToLower(it.ItemType)]
			for _, c := range cats {
				ok = ok || allowed[c]
			}
			if !ok {
				return PurchaseGoodsReceipt{}, errs.Validation("receipt_without_po_not_allowed",
					"items of category "+it.Category+" need a purchase order (Procurement Policies: receipt without PO)",
					errs.Field(f+".itemId", "not_allowed", "category not allowed without PO"))
			}
			acc, rej, err := parseReceiptQty(f, x)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			uom := it.BaseUOMID
			if it.PurchaseUOMID != nil {
				uom = *it.PurchaseUOMID
			}
			if x.UOMID != nil {
				uom = *x.UOMID
			}
			base, err := inventory.ProcurementConvert(ctx, tx, x.ItemID, acc, uom, it.BaseUOMID)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			perUOM, err := inventory.ProcurementConvert(ctx, tx, x.ItemID, decimal.NewFromInt(1), uom, it.BaseUOMID)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			unit, err := nonNegative(f+".unitCost", x.UnitCost, it.StandardCost.Mul(perUOM).Round(6))
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			desc := strings.TrimSpace(x.Description)
			if desc == "" {
				desc = it.Name
			}
			total := money(acc.Mul(unit), cur)
			rl := receiptLine{item: x.ItemID, uom: &uom, desc: desc, accept: acc, rejected: rej, delivered: acc.Add(rej), rejectReason: nz(x.RejectionReason),
				base: base.Round(6), unitCost: unit, totalCost: total, batch: nz(x.BatchNo), serials: x.SerialNos, notes: nz(x.Notes)}
			if rl.base.IsPositive() {
				rl.baseUnitCost = total.Div(rl.base).Round(6)
			}
			if rl.expiry, err = parseDate(f+".expiryDate", x.ExpiryDate); err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			lines = append(lines, rl)
		}
	}
	if warehouse == nil {
		return PurchaseGoodsReceipt{}, handle.Invalid("warehouseId", "required", "select the receiving warehouse")
	}
	if approvalReason == "over_receipt" && strings.TrimSpace(in.Reason) == "" {
		return PurchaseGoodsReceipt{}, errs.Validation("reason_required", "explain the over-receipt (approval)", errs.Field("reason", "required", "required for an over-receipt"))
	}
	num, err := nextNumber(ctx, tx, property, "GR")
	if err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	gid := id.New()
	status := "posted"
	if approvalReason != "" {
		status = "pending_approval"
	}
	files := in.AttachmentFileIDs
	if files == nil {
		files = []uuid.UUID{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.goods_receipts (id, property_id, number, purchase_order_id, supplier_id, warehouse_id, received_date,
		delivery_note_no, notes, status, approval_reason, currency, attachment_file_ids, received_by, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending_approval',$10,$11,$12,$13,$13,$13)`, gid, property, num, in.PurchaseOrderID, supplier, *warehouse, rdate,
		nz(in.DeliveryNoteNo), nz(in.Notes), nz(approvalReason), cur, files, actor(ctx)); err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	total := decimal.Zero
	for i, l := range lines {
		total = total.Add(l.totalCost)
		serials := l.serials
		if serials == nil {
			serials = []string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.goods_receipt_lines (id, property_id, goods_receipt_id, line_no, purchase_order_line_id, item_id,
			description, uom_id, delivered_quantity, accepted_quantity, rejected_quantity, rejection_reason, base_quantity, unit_cost, base_unit_cost,
			total_cost, tax_percent, tax_code, batch_no, expiry_date, serial_nos, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12,$13::numeric,$14::numeric,$15::numeric,$16::numeric,$17::numeric,$18,
			$19,$20,$21,$22)`, id.New(), property, gid, i+1, l.poLine, l.item, l.desc, l.uom, l.delivered.String(), l.accept.String(), l.rejected.String(),
			l.rejectReason, l.base.String(), l.unitCost.String(), l.baseUnitCost.String(), l.totalCost.String(), l.taxPct.String(), l.taxCode, l.batch,
			l.expiry, serials, l.notes); err != nil {
			return PurchaseGoodsReceipt{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.goods_receipts SET total = $2::numeric WHERE id = $1`, gid, total.String()); err != nil {
		return PurchaseGoodsReceipt{}, err
	}
	g, err := GetReceipt(ctx, tx, gid)
	if err != nil {
		return g, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.goods_receipt",
		EntityID: gid.String(), EntityLabel: num, PropertyID: &property, Reason: in.Reason, After: g}); err != nil {
		return g, err
	}
	if status == "posted" {
		if err := m.postReceipt(ctx, tx, property, gid); err != nil {
			return g, err
		}
		return GetReceipt(ctx, tx, gid)
	}
	ft, _ := total.Float64()
	fe, _ := maxExcess.Float64()
	areq, _, err := m.submitApproval(ctx, tx, GoodsReceiptDocumentType, gid, property, num, "Goods Receipt "+num+" ("+strings.ReplaceAll(approvalReason, "_", " ")+")",
		map[string]any{"amount": ft, "reason": approvalReason, "excessPercent": fe})
	if err != nil {
		return g, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.goods_receipts SET approval_request_id = $2 WHERE id = $1`, gid, areq); err != nil {
		return g, err
	}
	return GetReceipt(ctx, tx, gid)
}

// SyncGoodsReceiptAction is the offline sync action of goods receipts
// recorded on a warehouse device with a weak connection (FR-OPS-P4-04).
const SyncGoodsReceiptAction = "procurement.goods_receipt"

// SyncGoodsReceipt records a goods receipt queued offline; the sync service
// runs it once per queue item id (the client UUIDv7 is the idempotency key).
func (m *Module) SyncGoodsReceipt(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in PurchaseReceiptInput
	jd := json.NewDecoder(bytes.NewReader(payload))
	jd.DisallowUnknownFields()
	if err := jd.Decode(&in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid goods receipt: "+err.Error())
	}
	property := handle.Property(ctx)
	if p := authz.From(ctx); p == nil || !p.Can("procurement.goods_receipt.create", &property) {
		return nil, errs.Forbidden("procurement.goods_receipt.create")
	}
	g, err := m.ReceiveGoods(ctx, tx, property, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"goodsReceiptId": g.ID, "number": g.Number, "status": g.Status, "total": g.Total}, nil
}

// postReceipt posts a receipt: order quantities, status, and the
// procurement.goods_received event (contract: inventory stock, GRNI).
func (m *Module) postReceipt(ctx context.Context, tx pgx.Tx, property, gid uuid.UUID) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM procurement.goods_receipts WHERE id = $1 FOR UPDATE`, gid).Scan(&status); err != nil {
		return err
	}
	if status != "pending_approval" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.goods_receipts SET status = 'posted', posted_at = now() WHERE id = $1`, gid); err != nil {
		return err
	}
	g, err := GetReceipt(ctx, tx, gid)
	if err != nil {
		return err
	}
	type evLine struct {
		ItemID       uuid.UUID  `json:"itemId"`
		Quantity     string     `json:"quantity"`
		UOMID        *uuid.UUID `json:"uomId"`
		BaseQuantity string     `json:"baseQuantity"`
		UnitCost     string     `json:"unitCost"`
		BaseUnitCost string     `json:"baseUnitCost"`
		TotalCost    string     `json:"totalCost"`
		TaxCode      *string    `json:"taxCode,omitempty"`
		BatchNo      *string    `json:"batchNo,omitempty"`
		ExpiryDate   *string    `json:"expiryDate,omitempty"`
		SerialNos    []string   `json:"serialNos,omitempty"`
	}
	lines := []evLine{}
	total := decimal.Zero
	for _, l := range g.Lines {
		if l.PurchaseOrderLineID != nil {
			if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines SET received_quantity = received_quantity + $2::numeric WHERE id = $1`,
				*l.PurchaseOrderLineID, l.AcceptedQuantity); err != nil {
				return err
			}
		}
		if l.ItemID == nil || !dec(l.AcceptedQuantity).IsPositive() {
			continue
		}
		total = total.Add(dec(l.TotalCost))
		lines = append(lines, evLine{ItemID: *l.ItemID, Quantity: l.AcceptedQuantity, UOMID: l.UOMID, BaseQuantity: l.BaseQuantity, UnitCost: l.UnitCost,
			BaseUnitCost: l.BaseUnitCost, TotalCost: l.TotalCost, TaxCode: l.TaxCode, BatchNo: l.BatchNo, ExpiryDate: l.ExpiryDate, SerialNos: l.SerialNos})
	}
	if g.PurchaseOrderID != nil {
		if err := recomputeOrder(ctx, tx, *g.PurchaseOrderID); err != nil {
			return err
		}
	}
	if err := m.publish(ctx, tx, EventGoodsReceived, "procurement.goods_receipt", gid, property, map[string]any{
		"goodsReceiptId": g.ID, "number": g.Number, "purchaseOrderId": g.PurchaseOrderID, "poNumber": g.PONumber, "supplierId": g.SupplierID,
		"warehouseId": g.WarehouseID, "receivedDate": g.ReceivedDate, "currency": g.Currency, "total": total.String(), "lines": lines}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "post", EntityType: "procurement.goods_receipt", EntityID: gid.String(),
		EntityLabel: g.Number, PropertyID: &property, Before: map[string]any{"status": status}, After: map[string]any{"status": "posted", "total": total.String()}})
}

func (m *Module) receiptDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status == approval.StatusApproved {
		return m.postReceipt(ctx, tx, d.PropertyID, d.DocumentID)
	}
	tag, err := tx.Exec(ctx, `UPDATE procurement.goods_receipts SET status = 'rejected' WHERE id = $1 AND status = 'pending_approval'`, d.DocumentID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.goods_receipt",
		EntityID: d.DocumentID.String(), PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": "pending_approval"},
		After: map[string]any{"status": "rejected"}})
}

// ── purchase return & debit note (FR-GR-04) ──────────────────────────────

// PurchaseReturn returns received goods to the supplier.
type PurchaseReturn struct {
	ID              uuid.UUID            `json:"id" db:"id"`
	Number          string               `json:"number" db:"number"`
	GoodsReceiptID  uuid.UUID            `json:"goodsReceiptId" db:"goods_receipt_id"`
	GRNumber        string               `json:"grNumber" db:"gr_number"`
	PurchaseOrderID *uuid.UUID           `json:"purchaseOrderId" db:"purchase_order_id"`
	SupplierID      uuid.UUID            `json:"supplierId" db:"supplier_id"`
	SupplierName    string               `json:"supplierName" db:"supplier_name"`
	WarehouseID     uuid.UUID            `json:"warehouseId" db:"warehouse_id"`
	ReturnDate      string               `json:"returnDate" db:"return_date"`
	Reason          string               `json:"reason" db:"reason"`
	Status          string               `json:"status" db:"status" enum:"posted"`
	Currency        string               `json:"currency" db:"currency"`
	Subtotal        string               `json:"subtotal" db:"subtotal"`
	TaxAmount       string               `json:"taxAmount" db:"tax_amount"`
	Total           string               `json:"total" db:"total"`
	DebitNoteID     *uuid.UUID           `json:"debitNoteId" db:"debit_note_id"`
	DebitNoteNumber *string              `json:"debitNoteNumber" db:"debit_note_number"`
	CreatedAt       time.Time            `json:"createdAt" db:"created_at"`
	Lines           []PurchaseReturnLine `json:"lines,omitempty" db:"-"`
}

// PurchaseReturnLine is one returned line.
type PurchaseReturnLine struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	LineNo             int        `json:"lineNo" db:"line_no"`
	GoodsReceiptLineID uuid.UUID  `json:"goodsReceiptLineId" db:"goods_receipt_line_id"`
	ItemID             *uuid.UUID `json:"itemId" db:"item_id"`
	Description        string     `json:"description" db:"description"`
	Quantity           string     `json:"quantity" db:"quantity"`
	UOMID              *uuid.UUID `json:"uomId" db:"uom_id"`
	BaseQuantity       string     `json:"baseQuantity" db:"base_quantity"`
	UnitCost           string     `json:"unitCost" db:"unit_cost"`
	BaseUnitCost       string     `json:"baseUnitCost" db:"base_unit_cost"`
	TotalCost          string     `json:"totalCost" db:"total_cost"`
	TaxAmount          string     `json:"taxAmount" db:"tax_amount"`
	BatchNo            *string    `json:"batchNo" db:"batch_no"`
	SerialNos          []string   `json:"serialNos" db:"serial_nos"`
	Reason             *string    `json:"reason" db:"reason"`
}

// PurchaseReturnLineInput is one returned GR line.
type PurchaseReturnLineInput struct {
	GoodsReceiptLineID uuid.UUID `json:"goodsReceiptLineId"`
	Quantity           string    `json:"quantity"`
	SerialNos          []string  `json:"serialNos,omitempty"`
	Reason             string    `json:"reason,omitempty"`
}

// PurchaseReturnInput creates a purchase return.
type PurchaseReturnInput struct {
	GoodsReceiptID uuid.UUID                 `json:"goodsReceiptId"`
	ReturnDate     string                    `json:"returnDate,omitempty"`
	Reason         string                    `json:"reason"`
	Lines          []PurchaseReturnLineInput `json:"lines"`
}

const returnSelect = `SELECT r.id, r.number, r.goods_receipt_id, g.number AS gr_number, r.purchase_order_id, r.supplier_id, s.name AS supplier_name,
	r.warehouse_id, to_char(r.return_date, 'YYYY-MM-DD') AS return_date, r.reason, r.status, r.currency, trim_scale(r.subtotal)::text AS subtotal,
	trim_scale(r.tax_amount)::text AS tax_amount, trim_scale(r.total)::text AS total, d.id AS debit_note_id, d.number AS debit_note_number, r.created_at
	FROM procurement.purchase_returns r JOIN procurement.goods_receipts g ON g.id = r.goods_receipt_id JOIN procurement.suppliers s ON s.id = r.supplier_id
	LEFT JOIN procurement.debit_notes d ON d.purchase_return_id = r.id`

const returnLineSelect = `SELECT id, line_no, goods_receipt_line_id, item_id, description, trim_scale(quantity)::text AS quantity, uom_id,
	trim_scale(base_quantity)::text AS base_quantity, trim_scale(unit_cost)::text AS unit_cost, trim_scale(base_unit_cost)::text AS base_unit_cost,
	trim_scale(total_cost)::text AS total_cost, trim_scale(tax_amount)::text AS tax_amount, batch_no, serial_nos, reason
	FROM procurement.purchase_return_lines`

// GetReturn loads a purchase return with lines.
func GetReturn(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (PurchaseReturn, error) {
	r, err := oneOf[PurchaseReturn]("purchase return")(q.Query(ctx, returnSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return r, err
	}
	r.Lines, err = handle.List[PurchaseReturnLine](q.Query(ctx, returnLineSelect+` WHERE purchase_return_id = $1 ORDER BY line_no`, rid))
	return r, err
}

// CreateReturn returns received goods: stock out (procurement.purchase_returned)
// and a debit note reducing the payable (procurement.debit_note_issued).
func (m *Module) CreateReturn(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PurchaseReturnInput) (PurchaseReturn, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return PurchaseReturn{}, err
	}
	if len(in.Lines) == 0 {
		return PurchaseReturn{}, errs.Validation("lines_required", "return at least one line", errs.Field("lines", "required", "return at least one line"))
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.goods_receipts WHERE id = $1 FOR UPDATE`, in.GoodsReceiptID); err != nil {
		return PurchaseReturn{}, err
	}
	g, err := GetReceipt(ctx, tx, in.GoodsReceiptID)
	if err != nil {
		return PurchaseReturn{}, err
	}
	if g.Status != "posted" {
		return PurchaseReturn{}, errs.Conflict("receipt_not_posted", "only posted goods receipts can be returned")
	}
	rdate, err := parseDate("returnDate", in.ReturnDate)
	if err != nil {
		return PurchaseReturn{}, err
	}
	if rdate == nil {
		t := localToday(ctx, tx, property)
		rdate = &t
	}
	if err := m.openPeriod(ctx, tx, property, *rdate, "returnDate"); err != nil {
		return PurchaseReturn{}, err
	}
	num, err := nextNumber(ctx, tx, property, "PRT")
	if err != nil {
		return PurchaseReturn{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_returns (id, property_id, number, goods_receipt_id, purchase_order_id, supplier_id,
		warehouse_id, return_date, reason, currency, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, rid, property, num, g.ID, g.PurchaseOrderID,
		g.SupplierID, g.WarehouseID, rdate, in.Reason, g.Currency, actor(ctx)); err != nil {
		return PurchaseReturn{}, err
	}
	type evLine struct {
		ItemID       uuid.UUID `json:"itemId"`
		BaseQuantity string    `json:"baseQuantity"`
		BaseUnitCost string    `json:"baseUnitCost"`
		TotalCost    string    `json:"totalCost"`
		BatchNo      *string   `json:"batchNo,omitempty"`
	}
	evLines := []evLine{}
	sub, tax := decimal.Zero, decimal.Zero
	poLines := []uuid.UUID{}
	for i, x := range in.Lines {
		f := fmt.Sprintf("lines[%d]", i)
		var gl *PurchaseGoodsReceiptLine
		for j := range g.Lines {
			if g.Lines[j].ID == x.GoodsReceiptLineID {
				gl = &g.Lines[j]
			}
		}
		if gl == nil {
			return PurchaseReturn{}, handle.Invalid(f+".goodsReceiptLineId", "not_found", "not a line of this goods receipt")
		}
		q, err := positive(f+".quantity", x.Quantity)
		if err != nil {
			return PurchaseReturn{}, err
		}
		left := dec(gl.AcceptedQuantity).Sub(dec(gl.ReturnedQuantity))
		if q.GreaterThan(left) {
			return PurchaseReturn{}, handle.Invalid(f+".quantity", "above_received", "at most the accepted quantity not yet returned ("+left.String()+")")
		}
		base := q
		if acc := dec(gl.AcceptedQuantity); acc.IsPositive() {
			base = dec(gl.BaseQuantity).Mul(q).Div(acc).Round(6)
		}
		cost := money(q.Mul(dec(gl.UnitCost)), g.Currency)
		if q.Equal(left) { // the last unit returns the remaining cost exactly
			var returnedCost string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(total_cost), 0)::text FROM procurement.purchase_return_lines WHERE goods_receipt_line_id = $1`,
				gl.ID).Scan(&returnedCost); err != nil {
				return PurchaseReturn{}, err
			}
			cost = dec(gl.TotalCost).Sub(dec(returnedCost))
		}
		lineTax := money(cost.Mul(dec(gl.TaxPercent)).Div(hundred), g.Currency)
		sub, tax = sub.Add(cost), tax.Add(lineTax)
		baseUnit := dec(gl.BaseUnitCost)
		serials := x.SerialNos
		if serials == nil {
			serials = []string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_return_lines (id, property_id, purchase_return_id, line_no, goods_receipt_line_id, item_id,
			description, quantity, uom_id, base_quantity, unit_cost, base_unit_cost, total_cost, tax_percent, tax_amount, batch_no, serial_nos, reason)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16,$17,$18)`,
			id.New(), property, rid, i+1, gl.ID, gl.ItemID, gl.Description, q.String(), gl.UOMID, base.String(), gl.UnitCost, baseUnit.String(), cost.String(),
			gl.TaxPercent, lineTax.String(), gl.BatchNo, serials, nz(x.Reason)); err != nil {
			return PurchaseReturn{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.goods_receipt_lines SET returned_quantity = returned_quantity + $2::numeric WHERE id = $1`,
			gl.ID, q.String()); err != nil {
			return PurchaseReturn{}, err
		}
		if gl.PurchaseOrderLineID != nil {
			poLines = append(poLines, *gl.PurchaseOrderLineID)
			if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines SET returned_quantity = returned_quantity + $2::numeric WHERE id = $1`,
				*gl.PurchaseOrderLineID, q.String()); err != nil {
				return PurchaseReturn{}, err
			}
		}
		if gl.ItemID != nil {
			evLines = append(evLines, evLine{ItemID: *gl.ItemID, BaseQuantity: base.String(), BaseUnitCost: baseUnit.String(), TotalCost: cost.String(),
				BatchNo: gl.BatchNo})
		}
	}
	total := sub.Add(tax)
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_returns SET subtotal = $2::numeric, tax_amount = $3::numeric, total = $4::numeric WHERE id = $1`,
		rid, sub.String(), tax.String(), total.String()); err != nil {
		return PurchaseReturn{}, err
	}
	if g.PurchaseOrderID != nil {
		if err := recomputeOrder(ctx, tx, *g.PurchaseOrderID); err != nil {
			return PurchaseReturn{}, err
		}
	}
	if err := m.publish(ctx, tx, EventPurchaseReturned, "procurement.purchase_return", rid, property, map[string]any{
		"purchaseReturnId": rid, "number": num, "goodsReceiptId": g.ID, "supplierId": g.SupplierID, "warehouseId": g.WarehouseID, "currency": g.Currency,
		"total": sub.String(), "lines": evLines}); err != nil {
		return PurchaseReturn{}, err
	}
	// The debit note refers to the vendor invoice already billing the
	// returned goods, when there is one.
	var invoice *uuid.UUID
	if len(poLines) > 0 {
		if err := tx.QueryRow(ctx, `SELECT v.id FROM procurement.vendor_invoices v JOIN procurement.vendor_invoice_lines l ON l.vendor_invoice_id = v.id
			WHERE l.purchase_order_line_id = ANY($1) AND v.status IN ('approved', 'partially_paid', 'paid') ORDER BY v.approved_at DESC LIMIT 1`, poLines).
			Scan(&invoice); err != nil && !dbtx.IsNoRows(err) {
			return PurchaseReturn{}, err
		}
	}
	if _, err := m.issueDebitNote(ctx, tx, property, debitNote{supplier: g.SupplierID, purchaseReturn: &rid, order: g.PurchaseOrderID, invoice: invoice,
		date: *rdate, reason: "Purchase return " + num + ": " + in.Reason, subtotal: sub, tax: tax, currency: g.Currency}); err != nil {
		return PurchaseReturn{}, err
	}
	out, err := GetReturn(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.purchase_return",
		EntityID: rid.String(), EntityLabel: num, PropertyID: &property, Reason: in.Reason, After: out})
}

// VendorDebitNote reduces the payable to a supplier.
type VendorDebitNote struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	Number           string     `json:"number" db:"number"`
	SupplierID       uuid.UUID  `json:"supplierId" db:"supplier_id"`
	SupplierName     string     `json:"supplierName" db:"supplier_name"`
	PurchaseReturnID *uuid.UUID `json:"purchaseReturnId" db:"purchase_return_id"`
	PurchaseOrderID  *uuid.UUID `json:"purchaseOrderId" db:"purchase_order_id"`
	VendorInvoiceID  *uuid.UUID `json:"vendorInvoiceId" db:"vendor_invoice_id"`
	IssueDate        string     `json:"issueDate" db:"issue_date"`
	Reason           string     `json:"reason" db:"reason"`
	Subtotal         string     `json:"subtotal" db:"subtotal"`
	TaxAmount        string     `json:"taxAmount" db:"tax_amount"`
	Amount           string     `json:"amount" db:"amount"`
	Currency         string     `json:"currency" db:"currency"`
	Status           string     `json:"status" db:"status" enum:"issued,applied"`
	CreatedAt        time.Time  `json:"createdAt" db:"created_at"`
}

// VendorDebitNoteInput is a manual debit note on a vendor invoice (price
// difference, mismatch resolution, FR-VIN-03).
type VendorDebitNoteInput struct {
	VendorInvoiceID uuid.UUID `json:"vendorInvoiceId"`
	Subtotal        string    `json:"subtotal"`
	TaxAmount       string    `json:"taxAmount,omitempty"`
	Reason          string    `json:"reason"`
	IssueDate       string    `json:"issueDate,omitempty"`
}

const debitNoteSelect = `SELECT d.id, d.number, d.supplier_id, s.name AS supplier_name, d.purchase_return_id, d.purchase_order_id, d.vendor_invoice_id,
	to_char(d.issue_date, 'YYYY-MM-DD') AS issue_date, d.reason, trim_scale(d.subtotal)::text AS subtotal, trim_scale(d.tax_amount)::text AS tax_amount,
	trim_scale(d.amount)::text AS amount, d.currency, d.status, d.created_at FROM procurement.debit_notes d JOIN procurement.suppliers s ON s.id = d.supplier_id`

type debitNote struct {
	supplier                       uuid.UUID
	purchaseReturn, order, invoice *uuid.UUID
	date                           time.Time
	reason, currency               string
	subtotal, tax                  decimal.Decimal
}

// issueDebitNote stores a debit note, applies it to its invoice and
// publishes procurement.debit_note_issued.
func (m *Module) issueDebitNote(ctx context.Context, tx pgx.Tx, property uuid.UUID, d debitNote) (VendorDebitNote, error) {
	amount := d.subtotal.Add(d.tax)
	if !amount.IsPositive() {
		return VendorDebitNote{}, handle.Invalid("subtotal", "invalid", "the debit note amount must be positive")
	}
	num, err := nextNumber(ctx, tx, property, "DN")
	if err != nil {
		return VendorDebitNote{}, err
	}
	did := id.New()
	status := "issued"
	if d.invoice != nil {
		status = "applied"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.debit_notes (id, property_id, number, supplier_id, purchase_return_id, purchase_order_id,
		vendor_invoice_id, issue_date, reason, subtotal, tax_amount, amount, currency, status, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11::numeric,$12::numeric,$13,$14,$15)`, did, property, num, d.supplier, d.purchaseReturn, d.order,
		d.invoice, d.date, d.reason, d.subtotal.String(), d.tax.String(), amount.String(), d.currency, status, actor(ctx)); err != nil {
		return VendorDebitNote{}, err
	}
	if d.invoice != nil {
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET debit_note_total = debit_note_total + $2::numeric WHERE id = $1`,
			*d.invoice, amount.String()); err != nil {
			return VendorDebitNote{}, err
		}
		if err := refreshInvoicePayment(ctx, tx, *d.invoice); err != nil {
			return VendorDebitNote{}, err
		}
	}
	if err := m.publish(ctx, tx, EventDebitNoteIssued, "procurement.debit_note", did, property, map[string]any{
		"debitNoteId": did, "number": num, "supplierId": d.supplier, "vendorInvoiceId": d.invoice, "amount": amount.String(), "currency": d.currency,
		"reason": d.reason, "subtotal": d.subtotal.String(), "taxAmount": d.tax.String(), "purchaseReturnId": d.purchaseReturn,
		"purchaseOrderId": d.order}); err != nil {
		return VendorDebitNote{}, err
	}
	dn, err := oneOf[VendorDebitNote]("debit note")(tx.Query(ctx, debitNoteSelect+` WHERE d.id = $1`, did))
	if err != nil {
		return dn, err
	}
	return dn, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.debit_note",
		EntityID: did.String(), EntityLabel: num, PropertyID: &property, Reason: d.reason, After: dn})
}

// CreateDebitNote issues a manual debit note on a vendor invoice.
func (m *Module) CreateDebitNote(ctx context.Context, tx pgx.Tx, property uuid.UUID, in VendorDebitNoteInput) (VendorDebitNote, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return VendorDebitNote{}, err
	}
	inv, err := lockInvoice(ctx, tx, in.VendorInvoiceID)
	if err != nil {
		return VendorDebitNote{}, err
	}
	if inv.Status == "draft" || inv.Status == "cancelled" || inv.Status == "paid" {
		return VendorDebitNote{}, errs.Conflict("invoice_not_open", "a debit note applies to a matched, held or approved, unpaid vendor invoice")
	}
	sub, err := positive("subtotal", in.Subtotal)
	if err != nil {
		return VendorDebitNote{}, err
	}
	tax, err := nonNegative("taxAmount", in.TaxAmount, decimal.Zero)
	if err != nil {
		return VendorDebitNote{}, err
	}
	if sub.Add(tax).GreaterThan(dec(inv.Outstanding)) {
		return VendorDebitNote{}, handle.Invalid("subtotal", "above_outstanding", "the debit note exceeds the invoice outstanding ("+inv.Outstanding+")")
	}
	date, err := parseDate("issueDate", in.IssueDate)
	if err != nil {
		return VendorDebitNote{}, err
	}
	if date == nil {
		t := localToday(ctx, tx, property)
		date = &t
	}
	if err := m.openPeriod(ctx, tx, property, *date, "issueDate"); err != nil {
		return VendorDebitNote{}, err
	}
	var order *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT pl.purchase_order_id FROM procurement.vendor_invoice_lines l JOIN procurement.purchase_order_lines pl
		ON pl.id = l.purchase_order_line_id WHERE l.vendor_invoice_id = $1 LIMIT 1`, inv.ID).Scan(&order)
	return m.issueDebitNote(ctx, tx, property, debitNote{supplier: inv.SupplierID, order: order, invoice: &inv.ID, date: *date, reason: in.Reason,
		subtotal: sub, tax: tax, currency: inv.Currency})
}

// PurchaseReceivableOrder is an order waiting for goods (ops Goods Receipt list).
type PurchaseReceivableOrder struct {
	ID           uuid.UUID           `json:"id" db:"id"`
	Number       string              `json:"number" db:"number"`
	SupplierName string              `json:"supplierName" db:"supplier_name"`
	WarehouseID  *uuid.UUID          `json:"warehouseId" db:"warehouse_id"`
	ExpectedDate *string             `json:"expectedDate" db:"expected_date"`
	Status       string              `json:"status" db:"status"`
	Lines        []PurchaseOrderLine `json:"lines" db:"-"`
}

func (m *Module) registerReceipts(reg *route.Registry) {
	db := m.DB
	add := func(tag string, rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", tag, route.ScopeProperty
		reg.Add(rt)
	}
	const tgr, tprt, tdn = "Goods Receipts", "Purchase Returns", "Debit Notes"
	const gr = "/api/v1/procurement/goods-receipts"
	add(tgr, route.Route{Method: http.MethodGet, Path: gr, Summary: "Goods receipts (GR log)", Permission: "procurement.goods_receipt.view",
		Response: PurchaseGoodsReceipt{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[purchaseOrderId]"},
			{Name: "filter[supplierId]"}, {Name: "filter[warehouseId]"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseGoodsReceipt], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PurchaseGoodsReceipt](tx.Query(ctx, receiptSelect+` WHERE g.property_id = $1
				AND ($2 = '' OR g.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR g.purchase_order_id::text = $3)
				AND ($4 = '' OR g.supplier_id::text = $4) AND ($5 = '' OR g.warehouse_id::text = $5)
				AND ($6 = '' OR g.received_date >= $6::date) AND ($7 = '' OR g.received_date <= $7::date)
				AND ($8 = '' OR g.number ILIKE '%' || $8 || '%' OR o.number ILIKE '%' || $8 || '%' OR g.delivery_note_no ILIKE '%' || $8 || '%')
				ORDER BY g.created_at DESC LIMIT $9`, handle.Property(ctx), lp.Filters["status"], lp.Filters["purchaseOrderId"], lp.Filters["supplierId"],
				lp.Filters["warehouseId"], r.URL.Query().Get("from"), r.URL.Query().Get("to"), lp.Q, lp.Limit)))
		})})
	add(tgr, route.Route{Method: http.MethodPost, Path: gr, Summary: "Receive Goods against a PO (partial, accepted / rejected, batch / expiry / serial) or without PO",
		Permission: "procurement.goods_receipt.create", Request: PurchaseReceiptInput{}, Response: PurchaseGoodsReceipt{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in PurchaseReceiptInput) (PurchaseGoodsReceipt, error) {
			return m.ReceiveGoods(ctx, tx, handle.Property(ctx), in)
		})})
	add(tgr, route.Route{Method: http.MethodGet, Path: gr + "/{id}", Summary: "Goods receipt with lines", Permission: "procurement.goods_receipt.view",
		Response: PurchaseGoodsReceipt{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PurchaseGoodsReceipt, error) {
			gid, err := handle.ID(r)
			if err != nil {
				return PurchaseGoodsReceipt{}, err
			}
			return GetReceipt(ctx, tx, gid)
		})})
	add(tgr, route.Route{Method: http.MethodGet, Path: "/api/v1/procurement/receivable-orders",
		Summary: "Orders waiting for goods with outstanding lines (ops Warehouse Goods Receipt)", Permission: "procurement.goods_receipt.view",
		Response: PurchaseReceivableOrder{}, List: true, Query: []route.Param{{Name: "warehouseId"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseReceivableOrder], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[PurchaseReceivableOrder](tx.Query(ctx, `SELECT o.id, o.number, s.name AS supplier_name, o.warehouse_id,
				to_char(o.expected_date, 'YYYY-MM-DD') AS expected_date, o.status FROM procurement.purchase_orders o JOIN procurement.suppliers s ON s.id = o.supplier_id
				WHERE o.property_id = $1 AND o.order_type = 'goods' AND o.status IN ('approved', 'sent', 'partially_received')
				AND ($2 = '' OR o.warehouse_id::text = $2) AND ($3 = '' OR o.number ILIKE '%' || $3 || '%' OR s.name ILIKE '%' || $3 || '%')
				ORDER BY o.expected_date NULLS LAST, o.number LIMIT $4`, handle.Property(ctx), r.URL.Query().Get("warehouseId"), lp.Q, lp.Limit))
			if err != nil {
				return httpx.Page[PurchaseReceivableOrder]{}, err
			}
			for i := range items {
				lines, err := handle.List[PurchaseOrderLine](tx.Query(ctx, orderLineSelect+` WHERE l.purchase_order_id = $1 ORDER BY l.line_no`, items[i].ID))
				if err != nil {
					return httpx.Page[PurchaseReceivableOrder]{}, err
				}
				items[i].Lines = []PurchaseOrderLine{}
				for _, l := range lines {
					if dec(l.OutstandingQuantity).IsPositive() {
						items[i].Lines = append(items[i].Lines, l)
					}
				}
			}
			return handle.Page(items, nil)
		})})

	const prt = "/api/v1/procurement/purchase-returns"
	add(tprt, route.Route{Method: http.MethodGet, Path: prt, Summary: "Purchase returns", Permission: "procurement.purchase_return.view",
		Response: PurchaseReturn{}, List: true, Query: []route.Param{{Name: "filter[supplierId]"}, {Name: "filter[goodsReceiptId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseReturn], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PurchaseReturn](tx.Query(ctx, returnSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.supplier_id::text = $2)
				AND ($3 = '' OR r.goods_receipt_id::text = $3) ORDER BY r.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["supplierId"],
				lp.Filters["goodsReceiptId"], lp.Limit)))
		})})
	add(tprt, route.Route{Method: http.MethodPost, Path: prt, Summary: "Create Purchase Return (stock out, debit note)",
		Permission: "procurement.purchase_return.create", Request: PurchaseReturnInput{}, Response: PurchaseReturn{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in PurchaseReturnInput) (PurchaseReturn, error) {
			return m.CreateReturn(ctx, tx, handle.Property(ctx), in)
		})})
	add(tprt, route.Route{Method: http.MethodGet, Path: prt + "/{id}", Summary: "Purchase return with lines and debit note",
		Permission: "procurement.purchase_return.view", Response: PurchaseReturn{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PurchaseReturn, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return PurchaseReturn{}, err
			}
			return GetReturn(ctx, tx, rid)
		})})

	const dn = "/api/v1/procurement/debit-notes"
	add(tdn, route.Route{Method: http.MethodGet, Path: dn, Summary: "Debit notes", Permission: "procurement.debit_note.view", Response: VendorDebitNote{}, List: true,
		Query: []route.Param{{Name: "filter[supplierId]"}, {Name: "filter[vendorInvoiceId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorDebitNote], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[VendorDebitNote](tx.Query(ctx, debitNoteSelect+` WHERE d.property_id = $1 AND ($2 = '' OR d.supplier_id::text = $2)
				AND ($3 = '' OR d.vendor_invoice_id::text = $3) ORDER BY d.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["supplierId"],
				lp.Filters["vendorInvoiceId"], lp.Limit)))
		})})
	add(tdn, route.Route{Method: http.MethodPost, Path: dn, Summary: "Issue a debit note on a vendor invoice (price difference)",
		Permission: "procurement.debit_note.create", Request: VendorDebitNoteInput{}, Response: VendorDebitNote{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in VendorDebitNoteInput) (VendorDebitNote, error) {
			return m.CreateDebitNote(ctx, tx, handle.Property(ctx), in)
		})})
	add(tdn, route.Route{Method: http.MethodGet, Path: dn + "/{id}", Summary: "Debit note", Permission: "procurement.debit_note.view", Response: VendorDebitNote{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (VendorDebitNote, error) {
			did, err := handle.ID(r)
			if err != nil {
				return VendorDebitNote{}, err
			}
			return oneOf[VendorDebitNote]("debit note")(tx.Query(ctx, debitNoteSelect+` WHERE d.id = $1`, did))
		})})
}
