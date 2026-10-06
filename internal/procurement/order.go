package procurement

// PRD P4 EP-13 Purchase Order: from requisitions (consolidated), from a
// selected vendor quotation, or direct (RFQ threshold and contract
// suppliers per Procurement Policies, FR-RFQ-04); approval by amount, sent
// to the supplier by e-mail with a PDF link, versioned revisions,
// cancellation of lines not received, delivery schedule per line, service
// orders with service confirmation (2-way matching), Partially Received →
// Received → Closed and Outstanding PO (FR-PO-01..05).

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

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/kernel/secret"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
)

// PurchaseOrder is a purchase order.
type PurchaseOrder struct {
	ID                uuid.UUID           `json:"id" db:"id"`
	Number            string              `json:"number" db:"number"`
	Version           int                 `json:"version" db:"version"`
	OrderType         string              `json:"orderType" db:"order_type" enum:"goods,service"`
	SupplierID        uuid.UUID           `json:"supplierId" db:"supplier_id"`
	SupplierCode      string              `json:"supplierCode" db:"supplier_code"`
	SupplierName      string              `json:"supplierName" db:"supplier_name"`
	Status            string              `json:"status" db:"status" enum:"draft,pending_approval,approved,sent,partially_received,received,closed,cancelled"`
	Source            string              `json:"source" db:"source" enum:"oneclub,migration"`
	OrderDate         string              `json:"orderDate" db:"order_date"`
	ExpectedDate      *string             `json:"expectedDate" db:"expected_date"`
	WarehouseID       *uuid.UUID          `json:"warehouseId" db:"warehouse_id"`
	DeliveryAddress   *string             `json:"deliveryAddress" db:"delivery_address"`
	Currency          string              `json:"currency" db:"currency"`
	PaymentTermDays   int                 `json:"paymentTermDays" db:"payment_term_days"`
	Subtotal          string              `json:"subtotal" db:"subtotal"`
	DiscountTotal     string              `json:"discountTotal" db:"discount_total"`
	TaxTotal          string              `json:"taxTotal" db:"tax_total"`
	Total             string              `json:"total" db:"total"`
	OutstandingValue  string              `json:"outstandingValue" db:"outstanding_value" doc:"Ordered value not yet received (excl. tax)"`
	QuotationID       *uuid.UUID          `json:"quotationId" db:"quotation_id"`
	RFQID             *uuid.UUID          `json:"rfqId" db:"rfq_id"`
	RFQSkipReason     *string             `json:"rfqSkipReason" db:"rfq_skip_reason"`
	ContactEmail      *string             `json:"contactEmail" db:"contact_email"`
	Notes             *string             `json:"notes" db:"notes"`
	Terms             *string             `json:"terms" db:"terms"`
	ApprovalRequestID *uuid.UUID          `json:"approvalRequestId" db:"approval_request_id"`
	SubmittedAt       *time.Time          `json:"submittedAt" db:"submitted_at"`
	ApprovedAt        *time.Time          `json:"approvedAt" db:"approved_at"`
	RejectedReason    *string             `json:"rejectedReason" db:"rejected_reason"`
	SentAt            *time.Time          `json:"sentAt" db:"sent_at"`
	SentTo            *string             `json:"sentTo" db:"sent_to"`
	RevisionReason    *string             `json:"revisionReason" db:"revision_reason"`
	RevisedAt         *time.Time          `json:"revisedAt" db:"revised_at"`
	ClosedAt          *time.Time          `json:"closedAt" db:"closed_at"`
	CloseReason       *string             `json:"closeReason" db:"close_reason"`
	CancelledAt       *time.Time          `json:"cancelledAt" db:"cancelled_at"`
	CancelReason      *string             `json:"cancelReason" db:"cancel_reason"`
	CreatedAt         time.Time           `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time           `json:"updatedAt" db:"updated_at"`
	Lines             []PurchaseOrderLine `json:"lines,omitempty" db:"-"`
}

// PurchaseOrderLine is one ordered item or service.
type PurchaseOrderLine struct {
	ID                       uuid.UUID   `json:"id" db:"id"`
	LineNo                   int         `json:"lineNo" db:"line_no"`
	ItemID                   *uuid.UUID  `json:"itemId" db:"item_id"`
	ItemCode                 *string     `json:"itemCode" db:"item_code"`
	Description              string      `json:"description" db:"description"`
	Quantity                 string      `json:"quantity" db:"quantity"`
	UOMID                    *uuid.UUID  `json:"uomId" db:"uom_id"`
	UOM                      *string     `json:"uom" db:"uom"`
	BaseQuantity             *string     `json:"baseQuantity" db:"base_quantity"`
	UnitPrice                string      `json:"unitPrice" db:"unit_price"`
	DiscountPercent          string      `json:"discountPercent" db:"discount_percent"`
	TaxPercent               string      `json:"taxPercent" db:"tax_percent"`
	TaxCode                  *string     `json:"taxCode" db:"tax_code"`
	LineSubtotal             string      `json:"lineSubtotal" db:"line_subtotal"`
	TaxAmount                string      `json:"taxAmount" db:"tax_amount"`
	LineTotal                string      `json:"lineTotal" db:"line_total"`
	ExpectedDate             *string     `json:"expectedDate" db:"expected_date"`
	ReceivedQuantity         string      `json:"receivedQuantity" db:"received_quantity"`
	ReturnedQuantity         string      `json:"returnedQuantity" db:"returned_quantity"`
	CancelledQuantity        string      `json:"cancelledQuantity" db:"cancelled_quantity"`
	InvoicedQuantity         string      `json:"invoicedQuantity" db:"invoiced_quantity"`
	ServiceConfirmedQuantity string      `json:"serviceConfirmedQuantity" db:"service_confirmed_quantity"`
	OpeningReceivedQuantity  string      `json:"openingReceivedQuantity" db:"opening_received_quantity" doc:"Received before the cut-over (migrated open PO)"`
	OutstandingQuantity      string      `json:"outstandingQuantity" db:"outstanding_quantity"`
	QuotationLineID          *uuid.UUID  `json:"quotationLineId" db:"quotation_line_id"`
	AccountHint              *string     `json:"accountHint" db:"account_hint" enum:"inventory,expense,asset"`
	Notes                    *string     `json:"notes" db:"notes"`
	RequisitionLineIDs       []uuid.UUID `json:"requisitionLineIds" db:"requisition_line_ids"`
}

// PurchaseOrderLineInput is an ordered line.
type PurchaseOrderLineInput struct {
	RequisitionLineID  *uuid.UUID  `json:"requisitionLineId,omitempty" doc:"Requisition line ordered (open quantity by default)"`
	RequisitionLineIDs []uuid.UUID `json:"requisitionLineIds,omitempty" doc:"Several requisition lines of the same item and UOM consolidated into this line (FR-PR-05)"`
	ItemID             *uuid.UUID  `json:"itemId,omitempty"`
	Description        string      `json:"description,omitempty"`
	Quantity           string      `json:"quantity,omitempty"`
	UOMID              *uuid.UUID  `json:"uomId,omitempty"`
	UnitPrice          string      `json:"unitPrice,omitempty" doc:"Default: supplier price list, else the requisition estimate"`
	DiscountPercent    string      `json:"discountPercent,omitempty"`
	TaxPercent         string      `json:"taxPercent,omitempty" doc:"Default: PPN for PKP suppliers"`
	TaxCode            string      `json:"taxCode,omitempty"`
	ExpectedDate       string      `json:"expectedDate,omitempty" doc:"Delivery schedule of the line"`
	AccountHint        string      `json:"accountHint,omitempty" enum:"inventory,expense,asset"`
	Notes              string      `json:"notes,omitempty"`
}

// PurchaseOrderInput creates (or edits) a purchase order.
type PurchaseOrderInput struct {
	SupplierID      *uuid.UUID               `json:"supplierId,omitempty" doc:"Required unless quotationId"`
	OrderType       string                   `json:"orderType,omitempty" enum:"goods,service"`
	QuotationID     *uuid.UUID               `json:"quotationId,omitempty" doc:"Selected vendor quotation: its selected lines are ordered"`
	OrderDate       string                   `json:"orderDate,omitempty"`
	ExpectedDate    string                   `json:"expectedDate,omitempty"`
	WarehouseID     *uuid.UUID               `json:"warehouseId,omitempty" doc:"Receiving warehouse / stock location"`
	DeliveryAddress string                   `json:"deliveryAddress,omitempty"`
	Currency        string                   `json:"currency,omitempty"`
	PaymentTermDays *int                     `json:"paymentTermDays,omitempty"`
	ContactEmail    string                   `json:"contactEmail,omitempty"`
	Notes           string                   `json:"notes,omitempty"`
	Terms           string                   `json:"terms,omitempty"`
	RFQSkipReason   string                   `json:"rfqSkipReason,omitempty" doc:"Required for a direct order above the RFQ threshold (non-contract supplier)"`
	Lines           []PurchaseOrderLineInput `json:"lines,omitempty"`
	Submit          bool                     `json:"submit,omitempty" doc:"Submit for approval right away"`
}

// PurchaseOrderFromRequisitionsInput consolidates requisition lines into orders, one per
// supplier.
type PurchaseOrderFromRequisitionsInput struct {
	RequisitionLineIDs []uuid.UUID `json:"requisitionLineIds"`
	SupplierID         *uuid.UUID  `json:"supplierId,omitempty" doc:"Default: the suggested supplier of each line"`
	ExpectedDate       string      `json:"expectedDate,omitempty"`
	WarehouseID        *uuid.UUID  `json:"warehouseId,omitempty"`
	RFQSkipReason      string      `json:"rfqSkipReason,omitempty"`
	Submit             bool        `json:"submit,omitempty"`
}

// PurchaseOrderReviseLineInput changes an existing line (lineId) or adds one.
type PurchaseOrderReviseLineInput struct {
	LineID *uuid.UUID `json:"lineId,omitempty"`
	PurchaseOrderLineInput
}

// PurchaseOrderReviseInput is a PO revision (new version, FR-PO-03).
type PurchaseOrderReviseInput struct {
	Reason       string                         `json:"reason"`
	ExpectedDate string                         `json:"expectedDate,omitempty"`
	Notes        string                         `json:"notes,omitempty"`
	Terms        string                         `json:"terms,omitempty"`
	Lines        []PurchaseOrderReviseLineInput `json:"lines,omitempty"`
}

// PurchaseOrderCancelLinesInput cancels the outstanding quantity of lines.
type PurchaseOrderCancelLinesInput struct {
	Reason string `json:"reason"`
	Lines  []struct {
		LineID   uuid.UUID `json:"lineId"`
		Quantity string    `json:"quantity,omitempty" doc:"Default: the whole outstanding quantity"`
	} `json:"lines"`
}

// PurchaseOrderSendInput overrides the supplier e-mail.
type PurchaseOrderSendInput struct {
	Email string `json:"email,omitempty"`
}

// PurchaseOrderConfirmServiceInput confirms delivered services (2-way matching, FR-VIN-04).
type PurchaseOrderConfirmServiceInput struct {
	Lines []struct {
		LineID   uuid.UUID `json:"lineId"`
		Quantity string    `json:"quantity"`
	} `json:"lines"`
	Notes string `json:"notes,omitempty"`
}

// PurchaseOrderRevision is a previous version of a PO.
type PurchaseOrderRevision struct {
	Version   int             `json:"version" db:"version"`
	Reason    string          `json:"reason" db:"reason"`
	Snapshot  json.RawMessage `json:"snapshot" db:"snapshot"`
	CreatedAt time.Time       `json:"createdAt" db:"created_at"`
	CreatedBy *uuid.UUID      `json:"createdBy" db:"created_by"`
}

const orderSelect = `SELECT o.id, o.number, o.version, o.order_type, o.source, o.supplier_id, s.code AS supplier_code, s.name AS supplier_name, o.status,
	to_char(o.order_date, 'YYYY-MM-DD') AS order_date, to_char(o.expected_date, 'YYYY-MM-DD') AS expected_date, o.warehouse_id, o.delivery_address,
	o.currency, o.payment_term_days, trim_scale(o.subtotal)::text AS subtotal, trim_scale(o.discount_total)::text AS discount_total,
	trim_scale(o.tax_total)::text AS tax_total, trim_scale(o.total)::text AS total,
	trim_scale(coalesce((SELECT sum(round(l.unit_price * (1 - l.discount_percent / 100) * greatest(l.quantity - l.cancelled_quantity
	  - CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity ELSE l.received_quantity - l.returned_quantity END, 0), 4))
	  FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id), 0))::text AS outstanding_value,
	o.quotation_id, o.rfq_id, o.rfq_skip_reason, o.contact_email, o.notes, o.terms, o.approval_request_id, o.submitted_at, o.approved_at,
	o.rejected_reason, o.sent_at, o.sent_to, o.revision_reason, o.revised_at, o.closed_at, o.close_reason, o.cancelled_at, o.cancel_reason, o.created_at,
	o.updated_at FROM procurement.purchase_orders o JOIN procurement.suppliers s ON s.id = o.supplier_id`

const orderLineSelect = `SELECT l.id, l.line_no, l.item_id, i.code AS item_code, l.description, trim_scale(l.quantity)::text AS quantity, l.uom_id,
	u.code AS uom, trim_scale(l.base_quantity)::text AS base_quantity, trim_scale(l.unit_price)::text AS unit_price,
	trim_scale(l.discount_percent)::text AS discount_percent, trim_scale(l.tax_percent)::text AS tax_percent, l.tax_code,
	trim_scale(l.line_subtotal)::text AS line_subtotal, trim_scale(l.tax_amount)::text AS tax_amount, trim_scale(l.line_total)::text AS line_total,
	to_char(l.expected_date, 'YYYY-MM-DD') AS expected_date, trim_scale(l.received_quantity)::text AS received_quantity,
	trim_scale(l.returned_quantity)::text AS returned_quantity, trim_scale(l.cancelled_quantity)::text AS cancelled_quantity,
	trim_scale(l.invoiced_quantity)::text AS invoiced_quantity, trim_scale(l.service_confirmed_quantity)::text AS service_confirmed_quantity,
	trim_scale(l.opening_received_quantity)::text AS opening_received_quantity,
	trim_scale(greatest(l.quantity - l.cancelled_quantity - CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity
	  ELSE l.received_quantity - l.returned_quantity END, 0))::text AS outstanding_quantity,
	l.quotation_line_id, l.account_hint, l.notes,
	coalesce((SELECT array_agg(x.requisition_line_id) FROM procurement.purchase_order_line_sources x WHERE x.purchase_order_line_id = l.id), '{}')
	  AS requisition_line_ids
	FROM procurement.purchase_order_lines l JOIN procurement.purchase_orders o ON o.id = l.purchase_order_id
	LEFT JOIN inventory.items i ON i.id = l.item_id LEFT JOIN inventory.uoms u ON u.id = l.uom_id`

// GetOrder loads a purchase order with lines.
func GetOrder(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (PurchaseOrder, error) {
	o, err := oneOf[PurchaseOrder]("purchase order")(q.Query(ctx, orderSelect+` WHERE o.id = $1`, oid))
	if err != nil {
		return o, err
	}
	o.Lines, err = handle.List[PurchaseOrderLine](q.Query(ctx, orderLineSelect+` WHERE l.purchase_order_id = $1 ORDER BY l.line_no`, oid))
	return o, err
}

func lockOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (PurchaseOrder, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.purchase_orders WHERE id = $1 FOR UPDATE`, oid); err != nil {
		return PurchaseOrder{}, err
	}
	return GetOrder(ctx, tx, oid)
}

// orderLine is a validated order line ready to insert.
type orderLine struct {
	preparedLine
	Discount, Tax decimal.Decimal
	TaxCode       *string
	Expected      *time.Time
	AccountHint   string
	QuotationLine *uuid.UUID
	Sources       []lineSource
}

type lineSource struct {
	ID  uuid.UUID
	Qty decimal.Decimal
}

func (m *Module) prepareOrderLine(ctx context.Context, tx pgx.Tx, f string, s supplierInfo, cfg ProcurementConfiguration, orderType string, in PurchaseOrderLineInput, on time.Time) (orderLine, error) {
	var ol orderLine
	itemID, uomID, desc, qty, price := in.ItemID, in.UOMID, in.Description, in.Quantity, in.UnitPrice
	ids := in.RequisitionLineIDs
	if in.RequisitionLineID != nil {
		ids = append([]uuid.UUID{*in.RequisitionLineID}, ids...)
	}
	if len(ids) > 0 {
		src, err := orderableLines(ctx, tx, ids)
		if err != nil {
			return ol, err
		}
		pr := src[0]
		open := decimal.Zero
		for _, x := range src {
			if !sameRef(x.ItemID, pr.ItemID) || !sameRef(x.UOMID, pr.UOMID) {
				return ol, handle.Invalid(f+".requisitionLineIds", "mixed_items", "consolidate only lines of the same item and UOM")
			}
			open = open.Add(x.OpenQty)
			if x.NeededBy != nil && (pr.NeededBy == nil || x.NeededBy.Before(*pr.NeededBy)) {
				pr.NeededBy = x.NeededBy
			}
		}
		pr.OpenQty = open
		if itemID == nil {
			itemID = pr.ItemID
		}
		if uomID == nil {
			uomID = pr.UOMID
		}
		if desc == "" {
			desc = pr.Description
		}
		if strings.TrimSpace(qty) == "" {
			qty = pr.OpenQty.String()
		}
		if strings.TrimSpace(price) == "" && itemID != nil && uomID != nil {
			if p, err := supplierPriceFor(ctx, tx, s.ID, *itemID, *uomID, on); err != nil {
				return ol, err
			} else if p != nil {
				price = *p
			}
		}
		if strings.TrimSpace(price) == "" {
			price = pr.Price.String()
		}
		q, err := positive(f+".quantity", qty)
		if err != nil {
			return ol, err
		}
		left := q
		for _, x := range src {
			if take := decimal.Min(left, x.OpenQty); take.IsPositive() {
				ol.Sources = append(ol.Sources, lineSource{ID: x.ID, Qty: take})
				left = left.Sub(take)
			}
		}
		if in.ExpectedDate == "" && pr.NeededBy != nil {
			in.ExpectedDate = pr.NeededBy.Format("2006-01-02")
		}
	}
	if strings.TrimSpace(price) == "" && itemID != nil {
		u := uomID
		if u == nil {
			it, err := inventoryItem(ctx, tx, *itemID)
			if err != nil {
				return ol, err
			}
			u = &it
		}
		if p, err := supplierPriceFor(ctx, tx, s.ID, *itemID, *u, on); err != nil {
			return ol, err
		} else if p != nil {
			price = *p
		}
	}
	pl, err := prepareItemLine(ctx, tx, f, itemID, uomID, desc, qty, price, &s.ID, on)
	if err != nil {
		return ol, err
	}
	ol.preparedLine = pl
	if ol.Discount, err = percent(f+".discountPercent", in.DiscountPercent, decimal.Zero); err != nil {
		return ol, err
	}
	defTax := decimal.Zero
	if s.PKP {
		defTax = dec(cfg.DefaultTaxPercent)
	}
	if ol.Tax, err = percent(f+".taxPercent", in.TaxPercent, defTax); err != nil {
		return ol, err
	}
	ol.TaxCode = nz(in.TaxCode)
	if ol.TaxCode == nil && ol.Tax.IsPositive() {
		ol.TaxCode = nz(cfg.DefaultTaxCode)
	}
	if ol.Expected, err = parseDate(f+".expectedDate", in.ExpectedDate); err != nil {
		return ol, err
	}
	ol.AccountHint = in.AccountHint
	if ol.AccountHint == "" {
		ol.AccountHint = "expense"
		if itemID != nil && orderType == "goods" {
			ol.AccountHint = "inventory"
		}
	}
	if ol.AccountHint != "inventory" && ol.AccountHint != "expense" && ol.AccountHint != "asset" {
		return ol, handle.Invalid(f+".accountHint", "invalid", "inventory, expense or asset")
	}
	ol.Notes = nz(in.Notes)
	return ol, nil
}

func sameRef(a, b *uuid.UUID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func inventoryItem(ctx context.Context, q dbtx.Querier, item uuid.UUID) (uuid.UUID, error) {
	var u uuid.UUID
	err := q.QueryRow(ctx, `SELECT coalesce(purchase_uom_id, base_uom_id) FROM inventory.items WHERE id = $1`, item).Scan(&u)
	if dbtx.IsNoRows(err) {
		return u, errs.Validation("invalid_item", "item not found", errs.Field("itemId", "not_found", "item not found"))
	}
	return u, err
}

// insertOrderLines appends lines, links requisition sources and updates the
// ordered quantity of the requisitions.
func insertOrderLines(ctx context.Context, tx pgx.Tx, property, oid uuid.UUID, cur string, lines []orderLine) error {
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(line_no), 0) FROM procurement.purchase_order_lines WHERE purchase_order_id = $1`, oid).Scan(&next); err != nil {
		return err
	}
	prs := map[uuid.UUID]bool{}
	for _, l := range lines {
		next++
		sub, tax, total, _ := lineAmounts(l.Quantity, l.Price, l.Discount, l.Tax, cur)
		var base *string
		if l.BaseQty != nil {
			s := l.BaseQty.String()
			base = &s
		}
		lid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_order_lines (id, property_id, purchase_order_id, line_no, item_id, description, quantity,
			uom_id, base_quantity, unit_price, discount_percent, tax_percent, tax_code, line_subtotal, tax_amount, line_total, expected_date,
			quotation_line_id, account_hint, notes) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13,
			$14::numeric,$15::numeric,$16::numeric,$17,$18,$19,$20)`, lid, property, oid, next, l.ItemID, l.Description, l.Quantity.String(), l.UOMID, base,
			l.Price.String(), l.Discount.String(), l.Tax.String(), l.TaxCode, sub.String(), tax.String(), total.String(), l.Expected, l.QuotationLine,
			l.AccountHint, l.Notes); err != nil {
			return err
		}
		for _, src := range l.Sources {
			if !src.Qty.IsPositive() {
				continue
			}
			var rid uuid.UUID
			if err := tx.QueryRow(ctx, `UPDATE procurement.purchase_requisition_lines SET ordered_quantity = ordered_quantity + $2::numeric WHERE id = $1
				RETURNING requisition_id`, src.ID, src.Qty.String()).Scan(&rid); err != nil {
				return err
			}
			prs[rid] = true
			if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_order_line_sources (purchase_order_line_id, requisition_line_id, property_id, quantity)
				VALUES ($1,$2,$3,$4::numeric)`, lid, src.ID, property, src.Qty.String()); err != nil {
				return err
			}
		}
	}
	for rid := range prs {
		if err := recomputeRequisition(ctx, tx, rid); err != nil {
			return err
		}
	}
	return nil
}

// releaseOrderSources gives back the requisition quantities of order lines
// (cancelled order / replaced draft lines).
func releaseOrderSources(ctx context.Context, tx pgx.Tx, oid uuid.UUID, onlyLine *uuid.UUID, ratio *decimal.Decimal) error {
	rows, err := tx.Query(ctx, `SELECT x.purchase_order_line_id, x.requisition_line_id, x.quantity::text FROM procurement.purchase_order_line_sources x
		JOIN procurement.purchase_order_lines l ON l.id = x.purchase_order_line_id WHERE l.purchase_order_id = $1 AND ($2::uuid IS NULL OR l.id = $2)`,
		oid, onlyLine)
	if err != nil {
		return err
	}
	type src struct {
		line, req uuid.UUID
		qty       decimal.Decimal
	}
	var list []src
	for rows.Next() {
		var s src
		var q string
		if err := rows.Scan(&s.line, &s.req, &q); err != nil {
			rows.Close()
			return err
		}
		s.qty = dec(q)
		list = append(list, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	prs := map[uuid.UUID]bool{}
	for _, s := range list {
		q := s.qty
		if ratio != nil {
			q = q.Mul(*ratio).Round(6)
		}
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE procurement.purchase_requisition_lines SET ordered_quantity = greatest(ordered_quantity - $2::numeric, 0)
			WHERE id = $1 RETURNING requisition_id`, s.req, q.String()).Scan(&rid); err != nil {
			return err
		}
		prs[rid] = true
		if ratio == nil || !s.qty.Sub(q).IsPositive() {
			if _, err := tx.Exec(ctx, `DELETE FROM procurement.purchase_order_line_sources WHERE purchase_order_line_id = $1 AND requisition_line_id = $2`,
				s.line, s.req); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_line_sources SET quantity = quantity - $3::numeric
			WHERE purchase_order_line_id = $1 AND requisition_line_id = $2`, s.line, s.req, q.String()); err != nil {
			return err
		}
	}
	for rid := range prs {
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_requisitions SET status = 'approved' WHERE id = $1 AND status IN ('partially_ordered', 'ordered')`,
			rid); err != nil {
			return err
		}
		if err := recomputeRequisition(ctx, tx, rid); err != nil {
			return err
		}
	}
	return nil
}

func refreshOrderTotals(ctx context.Context, tx pgx.Tx, oid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders o SET subtotal = t.sub, tax_total = t.tax, total = t.sub + t.tax, discount_total = t.disc
		FROM (SELECT coalesce(sum(line_subtotal), 0) AS sub, coalesce(sum(tax_amount), 0) AS tax,
		  coalesce(sum(round(quantity * unit_price, 4) - line_subtotal), 0) AS disc FROM procurement.purchase_order_lines WHERE purchase_order_id = $1) t
		WHERE o.id = $1`, oid)
	return err
}

// recomputeOrder derives the receipt status of an approved order.
func recomputeOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders o SET status = CASE
		WHEN NOT EXISTS (SELECT 1 FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id AND
		  (CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity ELSE l.received_quantity - l.returned_quantity END)
		  < l.quantity - l.cancelled_quantity)
		  AND EXISTS (SELECT 1 FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id
		    AND (l.received_quantity > 0 OR l.service_confirmed_quantity > 0)) THEN 'received'
		WHEN EXISTS (SELECT 1 FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id
		  AND (l.received_quantity > 0 OR l.service_confirmed_quantity > 0)) THEN 'partially_received'
		WHEN NOT EXISTS (SELECT 1 FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id AND l.cancelled_quantity < l.quantity) THEN 'cancelled'
		WHEN o.sent_at IS NOT NULL THEN 'sent' ELSE 'approved' END
		WHERE o.id = $1 AND o.status IN ('approved', 'sent', 'partially_received', 'received')`, oid)
	return err
}

// CreateOrder creates a draft purchase order (FR-PO-01).
func (m *Module) CreateOrder(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PurchaseOrderInput) (PurchaseOrder, error) {
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return PurchaseOrder{}, err
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return PurchaseOrder{}, err
	}
	orderType := in.OrderType
	if orderType == "" {
		orderType = "goods"
	}
	if orderType != "goods" && orderType != "service" {
		return PurchaseOrder{}, handle.Invalid("orderType", "invalid", "goods or service")
	}
	today := localToday(ctx, tx, property)
	var quotation *VendorQuotation
	if in.QuotationID != nil {
		v, err := GetQuotation(ctx, tx, *in.QuotationID)
		if err != nil {
			return PurchaseOrder{}, err
		}
		if v.Status != "selected" {
			return PurchaseOrder{}, errs.Validation("quotation_not_selected", "order a selected quotation", errs.Field("quotationId", "not_selected", "select the quotation first"))
		}
		if in.SupplierID != nil && *in.SupplierID != v.SupplierID {
			return PurchaseOrder{}, handle.Invalid("supplierId", "mismatch", "the quotation is from another supplier")
		}
		var ordered bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM procurement.purchase_orders WHERE quotation_id = $1 AND status <> 'cancelled')`, v.ID).
			Scan(&ordered); err != nil {
			return PurchaseOrder{}, err
		}
		if ordered {
			return PurchaseOrder{}, errs.Conflict("quotation_ordered", "this quotation is already on a purchase order")
		}
		quotation = &v
		in.SupplierID = &v.SupplierID
	}
	if in.SupplierID == nil {
		return PurchaseOrder{}, handle.Invalid("supplierId", "required", "select the supplier")
	}
	s, err := usableSupplier(ctx, tx, *in.SupplierID)
	if err != nil {
		return PurchaseOrder{}, err
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = s.Currency
		if quotation != nil {
			cur = quotation.Currency
		}
	}
	odate, err := parseDate("orderDate", in.OrderDate)
	if err != nil {
		return PurchaseOrder{}, err
	}
	if odate == nil {
		odate = &today
	}
	expected, err := parseDate("expectedDate", in.ExpectedDate)
	if err != nil {
		return PurchaseOrder{}, err
	}
	term := s.TermDays
	if quotation != nil && quotation.PaymentTermDays != nil {
		term = *quotation.PaymentTermDays
	}
	if in.PaymentTermDays != nil {
		term = *in.PaymentTermDays
	}
	if expected == nil {
		lead := s.LeadDays
		if quotation != nil && quotation.LeadTimeDays != nil {
			lead = *quotation.LeadTimeDays
		}
		e := odate.AddDate(0, 0, lead)
		expected = &e
	}
	var lines []orderLine
	var rfqID *uuid.UUID
	if quotation != nil {
		rfqID = quotation.RFQID
		for i, ql := range quotation.Lines {
			if !ql.Selected {
				continue
			}
			ol, err := m.prepareOrderLine(ctx, tx, fmt.Sprintf("lines[%d]", i), s, cfg, orderType, PurchaseOrderLineInput{ItemID: ql.ItemID, UOMID: ql.UOMID,
				Description: ql.Description, Quantity: ql.Quantity, UnitPrice: ql.UnitPrice, DiscountPercent: ql.DiscountPercent, TaxPercent: ql.TaxPercent},
				today)
			if err != nil {
				return PurchaseOrder{}, err
			}
			qlid := ql.ID
			ol.QuotationLine = &qlid
			if ql.LeadTimeDays != nil {
				e := odate.AddDate(0, 0, *ql.LeadTimeDays)
				ol.Expected = &e
			}
			if ql.RFQLineID != nil { // requisition lines behind the RFQ line
				srcs, err := rfqLineSources(ctx, tx, *ql.RFQLineID)
				if err != nil {
					return PurchaseOrder{}, err
				}
				left := ol.Quantity
				for _, x := range srcs {
					q := decimal.Min(left, x.Qty)
					if q.IsPositive() {
						ol.Sources = append(ol.Sources, lineSource{ID: x.ID, Qty: q})
						left = left.Sub(q)
					}
				}
			}
			lines = append(lines, ol)
		}
	}
	for i, l := range in.Lines {
		ol, err := m.prepareOrderLine(ctx, tx, fmt.Sprintf("lines[%d]", i), s, cfg, orderType, l, today)
		if err != nil {
			return PurchaseOrder{}, err
		}
		lines = append(lines, ol)
	}
	if len(lines) == 0 {
		return PurchaseOrder{}, errs.Validation("lines_required", "add at least one line", errs.Field("lines", "required", "add at least one line"))
	}
	// Receiving warehouse: given, else the destination of the requisitions
	// ordered, else the warehouse of the RFQ.
	if in.WarehouseID == nil {
		for _, l := range lines {
			for _, src := range l.Sources {
				var wh *uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT coalesce(l.warehouse_id, r.warehouse_id) FROM procurement.purchase_requisition_lines l
					JOIN procurement.purchase_requisitions r ON r.id = l.requisition_id WHERE l.id = $1`, src.ID).Scan(&wh); err != nil {
					return PurchaseOrder{}, err
				}
				if wh != nil && in.WarehouseID == nil {
					in.WarehouseID = wh
				}
			}
		}
	}
	if in.WarehouseID == nil && rfqID != nil {
		if err := tx.QueryRow(ctx, `SELECT warehouse_id FROM procurement.rfqs WHERE id = $1`, *rfqID).Scan(&in.WarehouseID); err != nil {
			return PurchaseOrder{}, err
		}
	}
	// RFQ threshold (FR-RFQ-04): a direct order above it needs a contract
	// supplier or a reason (recorded and passed to the approval).
	total := decimal.Zero
	for _, l := range lines {
		_, _, t, _ := lineAmounts(l.Quantity, l.Price, l.Discount, l.Tax, cur)
		total = total.Add(t)
	}
	if quotation == nil && !s.Contract && total.GreaterThan(dec(pol.RFQRequiredAbove)) && strings.TrimSpace(in.RFQSkipReason) == "" {
		return PurchaseOrder{}, errs.Validation("rfq_required", "purchases above the RFQ threshold need an RFQ (Procurement Policies), a contract supplier or a reason",
			errs.Field("rfqSkipReason", "required", "required above the RFQ threshold"))
	}
	num, err := nextNumber(ctx, tx, property, "PO")
	if err != nil {
		return PurchaseOrder{}, err
	}
	oid := id.New()
	contact := nz(in.ContactEmail)
	if contact == nil {
		contact = nz(orderEmail(ctx, tx, s.ID, s.Email))
	}
	terms := nz(in.Terms)
	if terms == nil {
		terms = nz(cfg.PurchaseOrderTerms)
	}
	var qid *uuid.UUID
	if quotation != nil {
		qid = &quotation.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_orders (id, property_id, number, order_type, supplier_id, order_date, expected_date,
		warehouse_id, delivery_address, currency, payment_term_days, quotation_id, rfq_id, rfq_skip_reason, contact_email, notes, terms, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`, oid, property, num, orderType, s.ID, odate, expected, in.WarehouseID,
		nz(in.DeliveryAddress), cur, term, qid, rfqID, nz(in.RFQSkipReason), contact, nz(in.Notes), terms, actor(ctx)); err != nil {
		return PurchaseOrder{}, err
	}
	for i := range lines {
		if lines[i].Expected == nil {
			lines[i].Expected = expected
		}
	}
	if err := insertOrderLines(ctx, tx, property, oid, cur, lines); err != nil {
		return PurchaseOrder{}, err
	}
	if err := refreshOrderTotals(ctx, tx, oid); err != nil {
		return PurchaseOrder{}, err
	}
	out, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.purchase_order",
		EntityID: oid.String(), EntityLabel: num + " · " + s.Name, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if in.Submit {
		return m.SubmitOrder(ctx, tx, oid)
	}
	return out, nil
}

func rfqLineSources(ctx context.Context, tx pgx.Tx, rfqLine uuid.UUID) ([]lineSource, error) {
	rows, err := tx.Query(ctx, `SELECT s.requisition_line_id, least(s.quantity, greatest(l.quantity - l.ordered_quantity, 0))::text
		FROM procurement.rfq_line_sources s JOIN procurement.purchase_requisition_lines l ON l.id = s.requisition_line_id
		WHERE s.rfq_line_id = $1 AND l.status = 'open' ORDER BY l.needed_by NULLS LAST, l.id`, rfqLine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lineSource
	for rows.Next() {
		var s lineSource
		var q string
		if err := rows.Scan(&s.ID, &q); err != nil {
			return nil, err
		}
		s.Qty = dec(q)
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateOrdersFromRequisitions consolidates requisition lines into draft
// orders, one per supplier (FR-PR-05).
func (m *Module) CreateOrdersFromRequisitions(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PurchaseOrderFromRequisitionsInput) ([]PurchaseOrder, error) {
	src, err := orderableLines(ctx, tx, in.RequisitionLineIDs)
	if err != nil {
		return nil, err
	}
	groups := map[uuid.UUID][]PurchaseOrderLineInput{}
	byID := map[uuid.UUID]requisitionLineSource{}
	for _, x := range src {
		byID[x.ID] = x
	}
	var order []uuid.UUID
	for _, s := range src {
		sid := s.Supplier
		if in.SupplierID != nil {
			sid = in.SupplierID
		}
		if sid == nil {
			return nil, errs.Validation("supplier_required", "line of "+s.Number+" has no suggested supplier: select one",
				errs.Field("supplierId", "required", "select the supplier"))
		}
		if _, ok := groups[*sid]; !ok {
			order = append(order, *sid)
		}
		merged := false
		for i, g := range groups[*sid] {
			first := byID[g.RequisitionLineIDs[0]]
			if s.ItemID != nil && sameRef(first.ItemID, s.ItemID) && sameRef(first.UOMID, s.UOMID) {
				groups[*sid][i].RequisitionLineIDs = append(groups[*sid][i].RequisitionLineIDs, s.ID)
				merged = true
				break
			}
		}
		if !merged {
			groups[*sid] = append(groups[*sid], PurchaseOrderLineInput{RequisitionLineIDs: []uuid.UUID{s.ID}})
		}
	}
	out := make([]PurchaseOrder, 0, len(order))
	for _, sid := range order {
		sid := sid
		wh := in.WarehouseID
		if wh == nil {
			for _, s := range src {
				if s.WarehouseID != nil {
					wh = s.WarehouseID
					break
				}
			}
		}
		o, err := m.CreateOrder(ctx, tx, property, PurchaseOrderInput{SupplierID: &sid, ExpectedDate: in.ExpectedDate, WarehouseID: wh, RFQSkipReason: in.RFQSkipReason,
			Lines: groups[sid], Submit: in.Submit})
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// UpdateOrder edits a draft order; lines are replaced when given.
func (m *Module) UpdateOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PurchaseOrderInput) (PurchaseOrder, error) {
	before, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" {
		return before, errs.Conflict("order_not_draft", "only draft orders can be edited; use Revise for approved orders")
	}
	property := propertyOf(ctx)
	expected, err := parseDate("expectedDate", in.ExpectedDate)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET expected_date = coalesce($2, expected_date), warehouse_id = coalesce($3, warehouse_id),
		delivery_address = coalesce($4, delivery_address), payment_term_days = coalesce($5, payment_term_days), contact_email = coalesce($6, contact_email),
		notes = coalesce($7, notes), terms = coalesce($8, terms), rfq_skip_reason = coalesce($9, rfq_skip_reason), updated_by = $10 WHERE id = $1`,
		oid, expected, in.WarehouseID, nz(in.DeliveryAddress), in.PaymentTermDays, nz(in.ContactEmail), nz(in.Notes), nz(in.Terms), nz(in.RFQSkipReason),
		actor(ctx)); err != nil {
		return before, err
	}
	if len(in.Lines) > 0 {
		s, err := loadSupplier(ctx, tx, before.SupplierID)
		if err != nil {
			return before, err
		}
		cfg, err := LoadConfiguration(ctx, tx, property)
		if err != nil {
			return before, err
		}
		if err := releaseOrderSources(ctx, tx, oid, nil, nil); err != nil {
			return before, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM procurement.purchase_order_lines WHERE purchase_order_id = $1`, oid); err != nil {
			return before, err
		}
		var lines []orderLine
		for i, l := range in.Lines {
			ol, err := m.prepareOrderLine(ctx, tx, fmt.Sprintf("lines[%d]", i), s, cfg, before.OrderType, l, localToday(ctx, tx, property))
			if err != nil {
				return before, err
			}
			lines = append(lines, ol)
		}
		if err := insertOrderLines(ctx, tx, property, oid, before.Currency, lines); err != nil {
			return before, err
		}
		if err := refreshOrderTotals(ctx, tx, oid); err != nil {
			return before, err
		}
	}
	after, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionUpdate, EntityType: "procurement.purchase_order",
		EntityID: oid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after})
}

// SubmitOrder sends a draft order to approval (FR-PO-02).
func (m *Module) SubmitOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (PurchaseOrder, error) {
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "draft" {
		return o, errs.Conflict("order_not_draft", "only draft orders can be submitted")
	}
	if _, err := usableSupplier(ctx, tx, o.SupplierID); err != nil {
		return o, err
	}
	property := propertyOf(ctx)
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'pending_approval', submitted_at = now(), rejected_reason = NULL,
		updated_by = $2 WHERE id = $1`, oid, actor(ctx)); err != nil {
		return o, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "submit", EntityType: "procurement.purchase_order", EntityID: oid.String(),
		EntityLabel: o.Number, PropertyID: &property, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "pending_approval", "total": o.Total}}); err != nil {
		return o, err
	}
	if err := m.submitOrderApproval(ctx, tx, o, property); err != nil {
		return o, err
	}
	return GetOrder(ctx, tx, oid)
}

func (m *Module) submitOrderApproval(ctx context.Context, tx pgx.Tx, o PurchaseOrder, property uuid.UUID) error {
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	attrs := amountAttrs(pol, dec(o.Total))
	attrs["orderType"], attrs["supplierId"] = o.OrderType, o.SupplierID.String()
	attrs["rfqSkipped"], attrs["revision"] = 0, 0
	if o.RFQSkipReason != nil {
		attrs["rfqSkipped"] = 1
	}
	if o.Version > 1 {
		attrs["revision"] = 1
	}
	areq, _, err := m.submitApproval(ctx, tx, PurchaseOrderDocumentType, o.ID, property, o.Number,
		fmt.Sprintf("Purchase Order %s v%d – %s", o.Number, o.Version, o.SupplierName), attrs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE procurement.purchase_orders SET approval_request_id = $2 WHERE id = $1`, o.ID, areq)
	return err
}

func (m *Module) orderDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	o, err := lockOrder(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	if o.Status != "pending_approval" {
		return nil
	}
	after := "approved"
	switch d.Status {
	case approval.StatusApproved:
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'approved', approved_at = now(), approved_by = $2, approved_total = total
			WHERE id = $1`, o.ID, nilIfZero(d.DecidedBy)); err != nil {
			return err
		}
		if err := recomputeOrder(ctx, tx, o.ID); err != nil { // a revised order keeps its receipts
			return err
		}
		// Last purchase price on the supplier price list (FR-SUP-02).
		if _, err := tx.Exec(ctx, `UPDATE procurement.supplier_items si SET last_price = round(l.unit_price * (1 - l.discount_percent / 100), 6),
			last_price_at = now() FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = $1 AND si.supplier_id = $2
			AND si.item_id = l.item_id AND si.uom_id = l.uom_id`, o.ID, o.SupplierID); err != nil {
			return err
		}
		if err := m.publish(ctx, tx, EventPurchaseOrderApproved, "procurement.purchase_order", o.ID, d.PropertyID, map[string]any{
			"purchaseOrderId": o.ID, "number": o.Number, "version": o.Version, "supplierId": o.SupplierID, "orderType": o.OrderType,
			"warehouseId": o.WarehouseID, "expectedDate": o.ExpectedDate, "currency": o.Currency, "total": o.Total}); err != nil {
			return err
		}
	default:
		after = "draft"
		reason := d.Reason
		if d.Status == approval.StatusCancelled {
			reason = "approval request withdrawn"
		}
		if o.Version > 1 { // a rejected revision returns to the previous approved state
			after = "approved"
			if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'approved', rejected_reason = $2 WHERE id = $1`, o.ID, nz(reason)); err != nil {
				return err
			}
			if err := recomputeOrder(ctx, tx, o.ID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'draft', rejected_reason = $2, approval_request_id = NULL
			WHERE id = $1`, o.ID, nz(reason)); err != nil {
			return err
		}
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.purchase_order",
		EntityID: o.ID.String(), EntityLabel: o.Number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": after, "decision": d.Status}})
}

func nilIfZero(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

func (m *Module) orderLink(token string) string {
	base := ""
	if m.PublicURL != nil {
		base = m.PublicURL()
	}
	return base + "/api/v1/public/procurement/purchase-orders/" + token + "/pdf"
}

// SendOrder e-mails the approved order with its PDF link (status Sent).
func (m *Module) SendOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PurchaseOrderSendInput) (PurchaseOrder, error) {
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "approved" && o.Status != "sent" && o.Status != "partially_received" {
		return o, errs.Conflict("order_not_approved", "only approved orders can be sent")
	}
	email := strings.TrimSpace(in.Email)
	if email == "" {
		email = deref(o.ContactEmail)
	}
	if email == "" {
		return o, errs.Validation("email_required", "the supplier has no e-mail; enter one", errs.Field("email", "required", "enter the supplier e-mail"))
	}
	property := propertyOf(ctx)
	token := secret.RandomToken(24)
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET public_token_hash = $2, sent_at = now(), sent_to = $3, updated_by = $4,
		status = CASE WHEN status = 'approved' THEN 'sent' ELSE status END WHERE id = $1`, oid, secret.HashToken(token), email, actor(ctx)); err != nil {
		return o, err
	}
	if m.Notify != nil {
		if err := m.Notify.Send(ctx, tx, notify.Message{Event: "procurement.purchase_order", Category: "procurement", Email: email, Name: o.SupplierName,
			Channels: []string{notify.ChannelEmail}, PropertyID: &property, Data: map[string]any{"number": o.Number, "version": o.Version,
				"club": clubName(ctx, tx), "supplier": o.SupplierName, "currency": o.Currency, "total": o.Total, "expectedDate": deref(o.ExpectedDate),
				"link": m.orderLink(token)}}); err != nil {
			return o, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "send", EntityType: "procurement.purchase_order", EntityID: oid.String(),
		EntityLabel: o.Number, PropertyID: &property, Before: map[string]any{"status": o.Status}, After: map[string]any{"status": "sent", "sentTo": email},
		Metadata: map[string]any{"version": o.Version}}); err != nil {
		return o, err
	}
	return GetOrder(ctx, tx, oid)
}

// ReviseOrder creates a new version of an approved order (FR-PO-03): the
// previous version is kept; a revision above the approved total goes back
// to approval; the new version must be sent again.
func (m *Module) ReviseOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PurchaseOrderReviseInput) (PurchaseOrder, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return PurchaseOrder{}, err
	}
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "approved" && o.Status != "sent" && o.Status != "partially_received" {
		return o, errs.Conflict("order_not_revisable", "only approved, sent or partially received orders can be revised")
	}
	property := propertyOf(ctx)
	snap, _ := json.Marshal(o)
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.purchase_order_revisions (id, property_id, purchase_order_id, version, reason, snapshot, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id.New(), property, oid, o.Version, in.Reason, snap, actor(ctx)); err != nil {
		return o, err
	}
	s, err := loadSupplier(ctx, tx, o.SupplierID)
	if err != nil {
		return o, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return o, err
	}
	today := localToday(ctx, tx, property)
	var added []orderLine
	for i, l := range in.Lines {
		f := fmt.Sprintf("lines[%d]", i)
		if l.LineID == nil {
			ol, err := m.prepareOrderLine(ctx, tx, f, s, cfg, o.OrderType, l.PurchaseOrderLineInput, today)
			if err != nil {
				return o, err
			}
			added = append(added, ol)
			continue
		}
		var cur *PurchaseOrderLine
		for j := range o.Lines {
			if o.Lines[j].ID == *l.LineID {
				cur = &o.Lines[j]
			}
		}
		if cur == nil {
			return o, handle.Invalid(f+".lineId", "not_found", "not a line of this order")
		}
		qty := dec(cur.Quantity)
		if strings.TrimSpace(l.Quantity) != "" {
			if qty, err = positive(f+".quantity", l.Quantity); err != nil {
				return o, err
			}
		}
		done := dec(cur.ReceivedQuantity).Add(dec(cur.CancelledQuantity))
		if o.OrderType == "service" {
			done = dec(cur.ServiceConfirmedQuantity).Add(dec(cur.CancelledQuantity))
		}
		if qty.LessThan(done) {
			return o, handle.Invalid(f+".quantity", "below_received", "cannot be less than the quantity received / cancelled ("+done.String()+")")
		}
		price, disc, tax := dec(cur.UnitPrice), dec(cur.DiscountPercent), dec(cur.TaxPercent)
		changedPrice := false
		if strings.TrimSpace(l.UnitPrice) != "" {
			if price, err = nonNegative(f+".unitPrice", l.UnitPrice, price); err != nil {
				return o, err
			}
			changedPrice = !price.Equal(dec(cur.UnitPrice))
		}
		if strings.TrimSpace(l.DiscountPercent) != "" {
			if disc, err = percent(f+".discountPercent", l.DiscountPercent, disc); err != nil {
				return o, err
			}
			changedPrice = changedPrice || !disc.Equal(dec(cur.DiscountPercent))
		}
		if strings.TrimSpace(l.TaxPercent) != "" {
			if tax, err = percent(f+".taxPercent", l.TaxPercent, tax); err != nil {
				return o, err
			}
		}
		if changedPrice && dec(cur.ReceivedQuantity).IsPositive() {
			return o, handle.Invalid(f+".unitPrice", "received", "the price of a line already received cannot change")
		}
		expected, err := parseDate(f+".expectedDate", l.ExpectedDate)
		if err != nil {
			return o, err
		}
		sub, taxAmt, total, _ := lineAmounts(qty, price, disc, tax, o.Currency)
		base := (*string)(nil)
		if cur.BaseQuantity != nil && dec(cur.Quantity).IsPositive() {
			b := dec(*cur.BaseQuantity).Mul(qty).Div(dec(cur.Quantity)).Round(6).String()
			base = &b
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines SET quantity = $2::numeric, base_quantity = $3::numeric, unit_price = $4::numeric,
			discount_percent = $5::numeric, tax_percent = $6::numeric, line_subtotal = $7::numeric, tax_amount = $8::numeric, line_total = $9::numeric,
			expected_date = coalesce($10, expected_date), notes = coalesce($11, notes) WHERE id = $1`, cur.ID, qty.String(), base, price.String(),
			disc.String(), tax.String(), sub.String(), taxAmt.String(), total.String(), expected, nz(l.Notes)); err != nil {
			return o, err
		}
	}
	if len(added) > 0 {
		if err := insertOrderLines(ctx, tx, property, oid, o.Currency, added); err != nil {
			return o, err
		}
	}
	expected, err := parseDate("expectedDate", in.ExpectedDate)
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET version = version + 1, revision_reason = $2, revised_at = now(),
		expected_date = coalesce($3, expected_date), notes = coalesce($4, notes), terms = coalesce($5, terms), sent_at = NULL, updated_by = $6 WHERE id = $1`,
		oid, in.Reason, expected, nz(in.Notes), nz(in.Terms), actor(ctx)); err != nil {
		return o, err
	}
	if err := refreshOrderTotals(ctx, tx, oid); err != nil {
		return o, err
	}
	if err := recomputeOrder(ctx, tx, oid); err != nil {
		return o, err
	}
	after, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	var approved *string
	if err := tx.QueryRow(ctx, `SELECT approved_total::text FROM procurement.purchase_orders WHERE id = $1`, oid).Scan(&approved); err != nil {
		return after, err
	}
	reapprove := dec(after.Total).GreaterThan(decp(approved))
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "revise", EntityType: "procurement.purchase_order", EntityID: oid.String(),
		EntityLabel: after.Number, PropertyID: &property, Reason: in.Reason, Before: o, After: after,
		Metadata: map[string]any{"version": after.Version, "reapproval": reapprove}}); err != nil {
		return after, err
	}
	if reapprove {
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'pending_approval', submitted_at = now() WHERE id = $1`, oid); err != nil {
			return after, err
		}
		after.Status = "pending_approval"
		if err := m.submitOrderApproval(ctx, tx, after, property); err != nil {
			return after, err
		}
	}
	return GetOrder(ctx, tx, oid)
}

// CancelOrder cancels an order with nothing received; requisition
// quantities are released.
func (m *Module) CancelOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (PurchaseOrder, error) {
	if err := handle.Required("reason", reason); err != nil {
		return PurchaseOrder{}, err
	}
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	switch o.Status {
	case "draft", "pending_approval", "approved", "sent":
	default:
		return o, errs.Conflict("order_not_cancellable", "an order with receipts, closed or cancelled cannot be cancelled; cancel the open lines or close it")
	}
	for _, l := range o.Lines {
		if dec(l.ReceivedQuantity).IsPositive() || dec(l.ServiceConfirmedQuantity).IsPositive() {
			return o, errs.Conflict("order_received", "goods were received on this order")
		}
	}
	if err := releaseOrderSources(ctx, tx, oid, nil, nil); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3
		WHERE id = $1`, oid, reason, actor(ctx)); err != nil {
		return o, err
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "cancel", EntityType: "procurement.purchase_order", EntityID: oid.String(),
		EntityLabel: o.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "cancelled"}}); err != nil {
		return o, err
	}
	return GetOrder(ctx, tx, oid)
}

// CancelOrderLines cancels outstanding quantities (lines not received).
func (m *Module) CancelOrderLines(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PurchaseOrderCancelLinesInput) (PurchaseOrder, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return PurchaseOrder{}, err
	}
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "approved" && o.Status != "sent" && o.Status != "partially_received" {
		return o, errs.Conflict("order_not_open", "only open approved orders have lines to cancel")
	}
	if len(in.Lines) == 0 {
		return o, errs.Validation("lines_required", "select the lines to cancel", errs.Field("lines", "required", "select lines"))
	}
	for i, x := range in.Lines {
		f := fmt.Sprintf("lines[%d]", i)
		var cur *PurchaseOrderLine
		for j := range o.Lines {
			if o.Lines[j].ID == x.LineID {
				cur = &o.Lines[j]
			}
		}
		if cur == nil {
			return o, handle.Invalid(f+".lineId", "not_found", "not a line of this order")
		}
		out := dec(cur.OutstandingQuantity)
		q := out
		if strings.TrimSpace(x.Quantity) != "" {
			if q, err = positive(f+".quantity", x.Quantity); err != nil {
				return o, err
			}
		}
		if !out.IsPositive() || q.GreaterThan(out) {
			return o, handle.Invalid(f+".quantity", "above_outstanding", "at most the outstanding quantity ("+out.String()+")")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines SET cancelled_quantity = cancelled_quantity + $2::numeric,
			notes = coalesce(notes || ' · ', '') || $3 WHERE id = $1`, cur.ID, q.String(), "cancelled "+q.String()+": "+in.Reason); err != nil {
			return o, err
		}
		ratio := q.Div(dec(cur.Quantity))
		if err := releaseOrderSources(ctx, tx, oid, &cur.ID, &ratio); err != nil {
			return o, err
		}
	}
	if err := recomputeOrder(ctx, tx, oid); err != nil {
		return o, err
	}
	property := propertyOf(ctx)
	after, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "cancel_lines", EntityType: "procurement.purchase_order",
		EntityID: oid.String(), EntityLabel: o.Number, PropertyID: &property, Reason: in.Reason, Before: o, After: after})
}

// CloseOrder closes an order (remaining quantities will not be delivered).
func (m *Module) CloseOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID, reason string) (PurchaseOrder, error) {
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "received" && o.Status != "partially_received" && o.Status != "sent" && o.Status != "approved" {
		return o, errs.Conflict("order_not_closable", "only approved, sent or received orders can be closed")
	}
	if o.Status != "received" {
		if err := handle.Required("reason", reason); err != nil {
			return o, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_orders SET status = 'closed', closed_at = now(), close_reason = $2, updated_by = $3 WHERE id = $1`,
		oid, nz(reason), actor(ctx)); err != nil {
		return o, err
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "close", EntityType: "procurement.purchase_order", EntityID: oid.String(),
		EntityLabel: o.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": o.Status},
		After: map[string]any{"status": "closed", "outstandingValue": o.OutstandingValue}}); err != nil {
		return o, err
	}
	return GetOrder(ctx, tx, oid)
}

// ConfirmService records delivered services of a service order (the
// receipt of 2-way matching, FR-VIN-04).
func (m *Module) ConfirmService(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in PurchaseOrderConfirmServiceInput) (PurchaseOrder, error) {
	o, err := lockOrder(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.OrderType != "service" {
		return o, errs.Conflict("not_service_order", "goods are received with a goods receipt")
	}
	if o.Status != "approved" && o.Status != "sent" && o.Status != "partially_received" {
		return o, errs.Conflict("order_not_open", "only open approved service orders can be confirmed")
	}
	if len(in.Lines) == 0 {
		return o, errs.Validation("lines_required", "confirm at least one line", errs.Field("lines", "required", "confirm at least one line"))
	}
	for i, x := range in.Lines {
		f := fmt.Sprintf("lines[%d]", i)
		var cur *PurchaseOrderLine
		for j := range o.Lines {
			if o.Lines[j].ID == x.LineID {
				cur = &o.Lines[j]
			}
		}
		if cur == nil {
			return o, handle.Invalid(f+".lineId", "not_found", "not a line of this order")
		}
		q, err := positive(f+".quantity", x.Quantity)
		if err != nil {
			return o, err
		}
		if q.GreaterThan(dec(cur.OutstandingQuantity)) {
			return o, handle.Invalid(f+".quantity", "above_outstanding", "at most the outstanding quantity ("+cur.OutstandingQuantity+")")
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines SET service_confirmed_quantity = service_confirmed_quantity + $2::numeric
			WHERE id = $1`, cur.ID, q.String()); err != nil {
			return o, err
		}
	}
	if err := recomputeOrder(ctx, tx, oid); err != nil {
		return o, err
	}
	property := propertyOf(ctx)
	after, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "confirm_service", EntityType: "procurement.purchase_order",
		EntityID: oid.String(), EntityLabel: o.Number, PropertyID: &property, Reason: in.Notes, Before: o, After: after})
}

func (m *Module) registerOrders(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Purchase Orders", route.ScopeProperty
		reg.Add(rt)
	}
	const base = "/api/v1/procurement/purchase-orders"
	add(route.Route{Method: http.MethodGet, Path: base, Summary: "Purchase orders (outstanding=true: Outstanding PO)", Permission: "procurement.purchase_order.view",
		Response: PurchaseOrder{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[supplierId]"}, {Name: "filter[warehouseId]"},
			{Name: "filter[orderType]"}, {Name: "outstanding", Type: "boolean"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseOrder], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[PurchaseOrder](tx.Query(ctx, orderSelect+` WHERE o.property_id = $1
				AND ($2 = '' OR o.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR o.supplier_id::text = $3) AND ($4 = '' OR o.warehouse_id::text = $4)
				AND ($5 = '' OR o.order_type = $5) AND (NOT $6 OR o.status IN ('approved', 'sent', 'partially_received'))
				AND ($7 = '' OR o.number ILIKE '%' || $7 || '%' OR s.name ILIKE '%' || $7 || '%')
				ORDER BY o.created_at DESC LIMIT $8`, handle.Property(ctx), lp.Filters["status"], lp.Filters["supplierId"], lp.Filters["warehouseId"],
				lp.Filters["orderType"], r.URL.Query().Get("outstanding") == "true", lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: base, Summary: "Create Purchase Order (direct, from requisition lines or a selected quotation)",
		Permission: "procurement.purchase_order.create", Request: PurchaseOrderInput{}, Response: PurchaseOrder{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in PurchaseOrderInput) (PurchaseOrder, error) {
			return m.CreateOrder(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: base + ":from-requisitions", Summary: "Consolidate requisition lines into purchase orders (one per supplier)",
		Permission: "procurement.purchase_order.create", Request: PurchaseOrderFromRequisitionsInput{}, Response: PurchaseOrder{}, List: true, Status: http.StatusCreated, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in PurchaseOrderFromRequisitionsInput) (httpx.Page[PurchaseOrder], error) {
			return handle.Page(m.CreateOrdersFromRequisitions(ctx, tx, handle.Property(ctx), in))
		})})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Purchase order with lines, receipts and outstanding", Permission: "procurement.purchase_order.view",
		Response: PurchaseOrder{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PurchaseOrder, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return PurchaseOrder{}, err
			}
			return GetOrder(ctx, tx, oid)
		})})
	add(route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a draft purchase order (lines replaced when given)",
		Permission: "procurement.purchase_order.update", Request: PurchaseOrderInput{}, Response: PurchaseOrder{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PurchaseOrderInput) (PurchaseOrder, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return PurchaseOrder{}, err
			}
			return m.UpdateOrder(ctx, tx, oid, in)
		})})
	act := func(path, summary, perm string, fn func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error), req any) {
		add(route.Route{Method: http.MethodPost, Path: base + "/{id}" + path, Summary: summary, Permission: perm, Request: req, Response: PurchaseOrder{},
			Status: http.StatusOK, Handler: func(w http.ResponseWriter, r *http.Request) {
				oid, err := handle.ID(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				var out PurchaseOrder
				err = db.WithTx(r.Context(), func(tx pgx.Tx) error {
					var err error
					out, err = fn(r.Context(), tx, oid, r)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				httpx.JSON(w, http.StatusOK, out)
			}})
	}
	decode := func(r *http.Request, v any) error { return httpx.Decode(r, v) }
	act(":submit", "Submit Purchase Order for approval (matrix by amount)", "procurement.purchase_order.submit",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			if err := decode(r, &handle.Empty{}); err != nil {
				return PurchaseOrder{}, err
			}
			o, err := m.SubmitOrder(ctx, tx, oid)
			if err != nil {
				return o, err
			}
			return GetOrder(ctx, tx, oid)
		}, nil)
	for _, a := range []struct {
		path, summary string
		approve       bool
	}{{":approve", "Approve Purchase Order (current approval step)", true}, {":reject", "Reject Purchase Order (reason required)", false}} {
		a := a
		act(a.path, a.summary, "procurement.purchase_order.approve", func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in ProcurementReasonInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			o, err := GetOrder(ctx, tx, oid)
			if err != nil {
				return o, err
			}
			if o.Status != "pending_approval" {
				return o, errs.Conflict("order_not_pending", "the order is not waiting for approval")
			}
			if err := m.decide(ctx, tx, o.ApprovalRequestID, a.approve, in.Reason); err != nil {
				return o, err
			}
			return GetOrder(ctx, tx, oid)
		}, ProcurementReasonInput{})
	}
	act(":send", "Send Purchase Order to the supplier (e-mail with PDF link)", "procurement.purchase_order.send",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in PurchaseOrderSendInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.SendOrder(ctx, tx, oid, in)
		}, PurchaseOrderSendInput{})
	act(":revise", "Revise Purchase Order (new version; re-approval above the approved total)", "procurement.purchase_order.update",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in PurchaseOrderReviseInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.ReviseOrder(ctx, tx, oid, in)
		}, PurchaseOrderReviseInput{})
	act(":cancel", "Cancel Purchase Order (nothing received)", "procurement.purchase_order.cancel",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in ProcurementReasonInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.CancelOrder(ctx, tx, oid, in.Reason)
		}, ProcurementReasonInput{})
	act(":cancel-lines", "Cancel the outstanding quantity of lines not received", "procurement.purchase_order.cancel",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in PurchaseOrderCancelLinesInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.CancelOrderLines(ctx, tx, oid, in)
		}, PurchaseOrderCancelLinesInput{})
	act(":close", "Close Purchase Order", "procurement.purchase_order.close",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in ProcurementReasonInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.CloseOrder(ctx, tx, oid, in.Reason)
		}, ProcurementReasonInput{})
	act(":confirm-service", "Confirm delivered services of a service order (2-way matching)", "procurement.goods_receipt.create",
		func(ctx context.Context, tx pgx.Tx, oid uuid.UUID, r *http.Request) (PurchaseOrder, error) {
			var in PurchaseOrderConfirmServiceInput
			if err := decode(r, &in); err != nil {
				return PurchaseOrder{}, err
			}
			return m.ConfirmService(ctx, tx, oid, in)
		}, PurchaseOrderConfirmServiceInput{})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}/revisions", Summary: "Previous versions of the purchase order",
		Permission: "procurement.purchase_order.view", Response: PurchaseOrderRevision{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PurchaseOrderRevision], error) {
			oid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[PurchaseOrderRevision]{}, err
			}
			return handle.Page(handle.List[PurchaseOrderRevision](tx.Query(ctx, `SELECT version, reason, snapshot, created_at, created_by
				FROM procurement.purchase_order_revisions WHERE purchase_order_id = $1 ORDER BY version`, oid)))
		})})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}/pdf", Summary: "Purchase order PDF", Permission: "procurement.purchase_order.view",
		RawContent: "application/pdf", Handler: func(w http.ResponseWriter, r *http.Request) {
			oid, err := handle.ID(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			var b []byte
			err = db.WithReadTx(r.Context(), func(tx pgx.Tx) error {
				o, err := GetOrder(r.Context(), tx, oid)
				if err != nil {
					return err
				}
				b, err = OrderPDF(r.Context(), tx, o)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(b)
		}})
}
