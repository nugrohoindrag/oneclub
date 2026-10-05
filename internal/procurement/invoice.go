package procurement

// PRD P4 EP-15 Vendor Invoice & 3-Way Matching: capture (supplier invoice
// no., Faktur Pajak, dates, lines linked to PO / GR lines, PPN, PPh
// withholding, due date by payment terms), 3-way matching PO + GR +
// invoice per line with the Procurement Policies tolerances (2-way PO +
// service confirmation for service orders), Matched → approval → Approved
// (procurement.vendor_invoice_approved, accounting creates the payable and
// closes GRNI); Mismatch → On Hold until corrected, covered by a debit
// note or approved as an override (FR-VIN-01..05). Payments come from
// accounting.vendor_payment_made (Partially Paid / Paid). Statuses (PRD P4
// §7.6): draft, matched, mismatch, on_hold, approved, partially_paid, paid.

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
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// VendorInvoice is a supplier invoice.
type VendorInvoice struct {
	ID                uuid.UUID           `json:"id" db:"id"`
	Number            string              `json:"number" db:"number"`
	SupplierID        uuid.UUID           `json:"supplierId" db:"supplier_id"`
	SupplierName      string              `json:"supplierName" db:"supplier_name"`
	SupplierInvoiceNo string              `json:"supplierInvoiceNo" db:"supplier_invoice_no"`
	TaxInvoiceNo      *string             `json:"taxInvoiceNo" db:"tax_invoice_no" doc:"Faktur Pajak masukan"`
	InvoiceDate       string              `json:"invoiceDate" db:"invoice_date"`
	ReceivedDate      *string             `json:"receivedDate" db:"received_date"`
	DueDate           string              `json:"dueDate" db:"due_date"`
	PaymentTermDays   int                 `json:"paymentTermDays" db:"payment_term_days"`
	Currency          string              `json:"currency" db:"currency"`
	Subtotal          string              `json:"subtotal" db:"subtotal"`
	TaxAmount         string              `json:"taxAmount" db:"tax_amount"`
	Total             string              `json:"total" db:"total" doc:"Subtotal + PPN"`
	WithholdingType   string              `json:"withholdingType" db:"withholding_type" enum:"none,pph23,pph4_2"`
	WithholdingAmount string              `json:"withholdingAmount" db:"withholding_amount"`
	DebitNoteTotal    string              `json:"debitNoteTotal" db:"debit_note_total"`
	PaidAmount        string              `json:"paidAmount" db:"paid_amount"`
	Outstanding       string              `json:"outstanding" db:"outstanding" doc:"Total − withholding − debit notes − paid"`
	Status            string              `json:"status" db:"status" enum:"draft,matched,mismatch,on_hold,approved,partially_paid,paid,cancelled"`
	MatchType         *string             `json:"matchType" db:"match_type" enum:"three_way,two_way"`
	MatchedAt         *time.Time          `json:"matchedAt" db:"matched_at"`
	MatchSummary      json.RawMessage     `json:"matchSummary" db:"match_summary"`
	HoldReason        *string             `json:"holdReason" db:"hold_reason"`
	HeldAt            *time.Time          `json:"heldAt" db:"held_at"`
	OverrideReason    *string             `json:"overrideReason" db:"override_reason"`
	ApprovalRequestID *uuid.UUID          `json:"approvalRequestId" db:"approval_request_id"`
	ApprovalKind      *string             `json:"approvalKind" db:"approval_kind" enum:"approval,override"`
	ApprovedAt        *time.Time          `json:"approvedAt" db:"approved_at"`
	PaidAt            *time.Time          `json:"paidAt" db:"paid_at"`
	DaysOverdue       int                 `json:"daysOverdue" db:"days_overdue"`
	Notes             *string             `json:"notes" db:"notes"`
	CancelledReason   *string             `json:"cancelledReason" db:"cancelled_reason"`
	PurchaseOrderIDs  []uuid.UUID         `json:"purchaseOrderIds" db:"purchase_order_ids"`
	CreatedAt         time.Time           `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time           `json:"updatedAt" db:"updated_at"`
	Lines             []VendorInvoiceLine `json:"lines,omitempty" db:"-"`
}

// VendorInvoiceLine is one invoiced line with its matching result.
type VendorInvoiceLine struct {
	ID                  uuid.UUID  `json:"id" db:"id"`
	LineNo              int        `json:"lineNo" db:"line_no"`
	PurchaseOrderLineID *uuid.UUID `json:"purchaseOrderLineId" db:"purchase_order_line_id"`
	GoodsReceiptLineID  *uuid.UUID `json:"goodsReceiptLineId" db:"goods_receipt_line_id"`
	ItemID              *uuid.UUID `json:"itemId" db:"item_id"`
	ItemCode            *string    `json:"itemCode" db:"item_code"`
	Description         string     `json:"description" db:"description"`
	Quantity            string     `json:"quantity" db:"quantity"`
	UnitPrice           string     `json:"unitPrice" db:"unit_price"`
	TaxPercent          string     `json:"taxPercent" db:"tax_percent"`
	LineSubtotal        string     `json:"lineSubtotal" db:"line_subtotal"`
	TaxAmount           string     `json:"taxAmount" db:"tax_amount"`
	LineTotal           string     `json:"lineTotal" db:"line_total"`
	AccountHint         string     `json:"accountHint" db:"account_hint" enum:"inventory,expense,asset"`
	MatchStatus         string     `json:"matchStatus" db:"match_status" enum:"pending,matched,quantity_mismatch,price_mismatch,tax_mismatch,not_received,not_on_order"`
	ExpectedQuantity    *string    `json:"expectedQuantity" db:"expected_quantity"`
	ExpectedUnitPrice   *string    `json:"expectedUnitPrice" db:"expected_unit_price"`
	MatchNote           *string    `json:"matchNote" db:"match_note"`
	// PRD P4 FR-VAL-03 (additive): landed cost line and its receipt.
	LandedCost           *string    `json:"landedCost" db:"landed_cost_basis" enum:"value,quantity"`
	LandedGoodsReceiptID *uuid.UUID `json:"landedGoodsReceiptId" db:"landed_goods_receipt_id"`
}

// VendorInvoiceLineInput is one invoiced line.
type VendorInvoiceLineInput struct {
	PurchaseOrderLineID *uuid.UUID `json:"purchaseOrderLineId,omitempty"`
	GoodsReceiptLineID  *uuid.UUID `json:"goodsReceiptLineId,omitempty"`
	ItemID              *uuid.UUID `json:"itemId,omitempty"`
	Description         string     `json:"description,omitempty"`
	Quantity            string     `json:"quantity"`
	UnitPrice           string     `json:"unitPrice"`
	TaxPercent          string     `json:"taxPercent,omitempty" doc:"Default: the PO line's"`
	AccountHint         string     `json:"accountHint,omitempty" enum:"inventory,expense,asset"`
	// PRD P4 FR-VAL-03 (additive): freight / duty / insurance allocated to the received stock.
	LandedCost           string     `json:"landedCost,omitempty" enum:"value,quantity" doc:"Landed cost line allocated to the received goods by value or base quantity (no order line, receipt line or item)"`
	LandedGoodsReceiptID *uuid.UUID `json:"landedGoodsReceiptId,omitempty" doc:"Goods receipt of the landed cost (default: the receipts of the invoice's other lines)"`
}

// VendorInvoiceInput records (or corrects) a vendor invoice.
type VendorInvoiceInput struct {
	SupplierID        *uuid.UUID               `json:"supplierId,omitempty" doc:"Default: supplier of the purchase order"`
	PurchaseOrderID   *uuid.UUID               `json:"purchaseOrderId,omitempty" doc:"Without lines: bill the received, not yet invoiced quantities"`
	SupplierInvoiceNo string                   `json:"supplierInvoiceNo"`
	TaxInvoiceNo      string                   `json:"taxInvoiceNo,omitempty" doc:"Faktur Pajak"`
	InvoiceDate       string                   `json:"invoiceDate,omitempty"`
	ReceivedDate      string                   `json:"receivedDate,omitempty"`
	DueDate           string                   `json:"dueDate,omitempty" doc:"Default: invoice date + payment term"`
	PaymentTermDays   *int                     `json:"paymentTermDays,omitempty"`
	WithholdingType   string                   `json:"withholdingType,omitempty" enum:"none,pph23,pph4_2"`
	WithholdingAmount string                   `json:"withholdingAmount,omitempty" doc:"Default: subtotal × the PPh rate of Procurement Configuration"`
	Notes             string                   `json:"notes,omitempty"`
	Lines             []VendorInvoiceLineInput `json:"lines,omitempty"`
	Match             bool                     `json:"match,omitempty" doc:"Run the 3-way matching right away"`
}

// VendorInvoiceMatchRun is one matching result (history).
type VendorInvoiceMatchRun struct {
	ID        uuid.UUID       `json:"id" db:"id"`
	Result    string          `json:"result" db:"result" enum:"matched,mismatch"`
	MatchType string          `json:"matchType" db:"match_type" enum:"three_way,two_way"`
	Details   json.RawMessage `json:"details" db:"details"`
	RunBy     *uuid.UUID      `json:"runBy" db:"run_by"`
	RunAt     time.Time       `json:"runAt" db:"run_at"`
}

// VendorInvoicePayment is a payment applied by accounting.
type VendorInvoicePayment struct {
	PaymentID     uuid.UUID `json:"paymentId" db:"payment_id"`
	PaymentNumber *string   `json:"paymentNumber" db:"payment_number"`
	PaidDate      *string   `json:"paidDate" db:"paid_date"`
	Amount        string    `json:"amount" db:"amount"`
	Currency      string    `json:"currency" db:"currency"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
}

const invoiceSelect = `SELECT v.id, v.number, v.supplier_id, s.name AS supplier_name, v.supplier_invoice_no, v.tax_invoice_no,
	to_char(v.invoice_date, 'YYYY-MM-DD') AS invoice_date, to_char(v.received_date, 'YYYY-MM-DD') AS received_date,
	to_char(v.due_date, 'YYYY-MM-DD') AS due_date, v.payment_term_days, v.currency, trim_scale(v.subtotal)::text AS subtotal,
	trim_scale(v.tax_amount)::text AS tax_amount, trim_scale(v.total)::text AS total, v.withholding_type,
	trim_scale(v.withholding_amount)::text AS withholding_amount, trim_scale(v.debit_note_total)::text AS debit_note_total,
	trim_scale(v.paid_amount)::text AS paid_amount,
	trim_scale(CASE WHEN v.status IN ('draft', 'cancelled') THEN 0 ELSE greatest(v.total - v.withholding_amount - v.debit_note_total - v.paid_amount, 0) END)::text
	  AS outstanding,
	v.status, v.match_type, v.matched_at, v.match_summary, v.hold_reason, v.held_at, v.override_reason, v.approval_request_id, v.approval_kind,
	v.approved_at, v.paid_at, CASE WHEN v.status IN ('approved', 'partially_paid') AND v.due_date < current_date THEN current_date - v.due_date ELSE 0 END
	  AS days_overdue, v.notes, v.cancelled_reason,
	coalesce((SELECT array_agg(DISTINCT pl.purchase_order_id) FROM procurement.vendor_invoice_lines l
	  JOIN procurement.purchase_order_lines pl ON pl.id = l.purchase_order_line_id WHERE l.vendor_invoice_id = v.id), '{}') AS purchase_order_ids,
	v.created_at, v.updated_at FROM procurement.vendor_invoices v JOIN procurement.suppliers s ON s.id = v.supplier_id`

const invoiceLineSelect = `SELECT l.id, l.line_no, l.purchase_order_line_id, l.goods_receipt_line_id, l.item_id, i.code AS item_code, l.description,
	trim_scale(l.quantity)::text AS quantity, trim_scale(l.unit_price)::text AS unit_price, trim_scale(l.tax_percent)::text AS tax_percent,
	trim_scale(l.line_subtotal)::text AS line_subtotal, trim_scale(l.tax_amount)::text AS tax_amount, trim_scale(l.line_total)::text AS line_total,
	l.account_hint, l.match_status, trim_scale(l.expected_quantity)::text AS expected_quantity, trim_scale(l.expected_unit_price)::text AS expected_unit_price,
	l.match_note, l.landed_cost_basis, l.landed_goods_receipt_id FROM procurement.vendor_invoice_lines l LEFT JOIN inventory.items i ON i.id = l.item_id`

// GetInvoice loads a vendor invoice with lines.
func GetInvoice(ctx context.Context, q dbtx.Querier, iid uuid.UUID) (VendorInvoice, error) {
	v, err := oneOf[VendorInvoice]("vendor invoice")(q.Query(ctx, invoiceSelect+` WHERE v.id = $1`, iid))
	if err != nil {
		return v, err
	}
	v.Lines, err = handle.List[VendorInvoiceLine](q.Query(ctx, invoiceLineSelect+` WHERE l.vendor_invoice_id = $1 ORDER BY l.line_no`, iid))
	return v, err
}

func lockInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (VendorInvoice, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM procurement.vendor_invoices WHERE id = $1 FOR UPDATE`, iid); err != nil {
		return VendorInvoice{}, err
	}
	return GetInvoice(ctx, tx, iid)
}

type poLineRef struct {
	ID, Order, Supplier uuid.UUID
	OrderType, Currency string
	Item                *uuid.UUID
	Description         string
	Price, Discount     decimal.Decimal
	Tax                 decimal.Decimal
	AccountHint         *string
}

func loadPOLine(ctx context.Context, q dbtx.Querier, lid uuid.UUID) (poLineRef, error) {
	var r poLineRef
	var price, disc, tax string
	err := q.QueryRow(ctx, `SELECT l.id, l.purchase_order_id, o.supplier_id, o.order_type, o.currency, l.item_id, l.description, l.unit_price::text,
		l.discount_percent::text, l.tax_percent::text, l.account_hint FROM procurement.purchase_order_lines l
		JOIN procurement.purchase_orders o ON o.id = l.purchase_order_id WHERE l.id = $1`, lid).
		Scan(&r.ID, &r.Order, &r.Supplier, &r.OrderType, &r.Currency, &r.Item, &r.Description, &price, &disc, &tax, &r.AccountHint)
	if dbtx.IsNoRows(err) {
		return r, handle.Invalid("purchaseOrderLineId", "not_found", "purchase order line not found")
	}
	r.Price, r.Discount, r.Tax = dec(price), dec(disc), dec(tax)
	return r, err
}

// invoiceLinesFromOrder bills the received, not yet invoiced quantities of
// a purchase order (service orders: confirmed quantities) at PO prices.
func invoiceLinesFromOrder(ctx context.Context, tx pgx.Tx, oid uuid.UUID) ([]VendorInvoiceLineInput, error) {
	o, err := GetOrder(ctx, tx, oid)
	if err != nil {
		return nil, err
	}
	var out []VendorInvoiceLineInput
	for _, l := range o.Lines {
		got := dec(l.ReceivedQuantity)
		if o.OrderType == "service" {
			got = dec(l.ServiceConfirmedQuantity)
		}
		var billed string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(il.quantity), 0)::text FROM procurement.vendor_invoice_lines il
			JOIN procurement.vendor_invoices v ON v.id = il.vendor_invoice_id WHERE il.purchase_order_line_id = $1
			AND v.status IN ('matched', 'approved', 'partially_paid', 'paid')`, l.ID).Scan(&billed); err != nil {
			return nil, err
		}
		q := got.Sub(dec(l.OpeningReceivedQuantity)).Sub(dec(billed))
		if !q.IsPositive() {
			continue
		}
		lid := l.ID
		out = append(out, VendorInvoiceLineInput{PurchaseOrderLineID: &lid, Quantity: q.String(),
			UnitPrice: netPrice(dec(l.UnitPrice), dec(l.DiscountPercent)).String(), TaxPercent: l.TaxPercent})
	}
	if len(out) == 0 {
		return nil, errs.Validation("nothing_to_invoice", "nothing received and not yet invoiced on this order",
			errs.Field("purchaseOrderId", "nothing_to_invoice", "nothing received to invoice"))
	}
	return out, nil
}

func (m *Module) writeInvoiceLines(ctx context.Context, tx pgx.Tx, property, iid, supplier uuid.UUID, cur string, in []VendorInvoiceLineInput) (decimal.Decimal, decimal.Decimal, error) {
	if len(in) == 0 {
		return decimal.Zero, decimal.Zero, errs.Validation("lines_required", "add the invoiced lines", errs.Field("lines", "required", "add the invoiced lines"))
	}
	var sub, tax decimal.Decimal
	for i, l := range in {
		f := fmt.Sprintf("lines[%d]", i)
		qty, err := positive(f+".quantity", l.Quantity)
		if err != nil {
			return sub, tax, err
		}
		if strings.TrimSpace(l.UnitPrice) == "" {
			return sub, tax, handle.Invalid(f+".unitPrice", "required", "enter the invoiced unit price")
		}
		price, err := nonNegative(f+".unitPrice", l.UnitPrice, decimal.Zero)
		if err != nil {
			return sub, tax, err
		}
		desc, item, hint, defTax := strings.TrimSpace(l.Description), l.ItemID, l.AccountHint, decimal.Zero
		poLine := l.PurchaseOrderLineID
		if l.LandedCost != "" { // FR-VAL-03: valued into the stock (clears GRNI with the revaluation)
			if err := checkLandedLine(ctx, tx, property, f, l); err != nil {
				return sub, tax, err
			}
			hint = "inventory"
		}
		if l.GoodsReceiptLineID != nil {
			var gpl *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT l.purchase_order_line_id FROM procurement.goods_receipt_lines l JOIN procurement.goods_receipts g
				ON g.id = l.goods_receipt_id WHERE l.id = $1 AND g.supplier_id = $2 AND g.status = 'posted'`, *l.GoodsReceiptLineID, supplier).Scan(&gpl); err != nil {
				if dbtx.IsNoRows(err) {
					return sub, tax, handle.Invalid(f+".goodsReceiptLineId", "not_found", "posted goods receipt line of this supplier not found")
				}
				return sub, tax, err
			}
			if poLine == nil {
				poLine = gpl
			}
		}
		if poLine != nil {
			pl, err := loadPOLine(ctx, tx, *poLine)
			if err != nil {
				return sub, tax, err
			}
			if desc == "" {
				desc = pl.Description
			}
			if item == nil {
				item = pl.Item
			}
			defTax = pl.Tax
			if hint == "" && pl.AccountHint != nil {
				hint = *pl.AccountHint
			}
		}
		if desc == "" {
			return sub, tax, handle.Invalid(f+".description", "required", "describe the line")
		}
		if hint == "" {
			hint = "expense"
			if item != nil {
				hint = "inventory"
			}
		}
		if hint != "inventory" && hint != "expense" && hint != "asset" {
			return sub, tax, handle.Invalid(f+".accountHint", "invalid", "inventory, expense or asset")
		}
		tp, err := percent(f+".taxPercent", l.TaxPercent, defTax)
		if err != nil {
			return sub, tax, err
		}
		ls, lt, total, _ := lineAmounts(qty, price, decimal.Zero, tp, cur)
		sub, tax = sub.Add(ls), tax.Add(lt)
		if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_invoice_lines (id, property_id, vendor_invoice_id, line_no, purchase_order_line_id,
			goods_receipt_line_id, item_id, description, quantity, unit_price, tax_percent, line_subtotal, tax_amount, line_total, account_hint,
			landed_cost_basis, landed_goods_receipt_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14::numeric,$15,$16,$17)`, id.New(), property, iid, i+1,
			poLine, l.GoodsReceiptLineID, item, desc, qty.String(), price.String(), tp.String(), ls.String(), lt.String(), total.String(), hint,
			nz(l.LandedCost), l.LandedGoodsReceiptID); err != nil {
			return sub, tax, err
		}
	}
	return sub, tax, nil
}

// withholding computes the PPh withheld on payment.
func withholding(cfg ProcurementConfiguration, typ, given string, sub decimal.Decimal, cur string) (decimal.Decimal, error) {
	if typ == "none" || typ == "" {
		return decimal.Zero, nil
	}
	if strings.TrimSpace(given) != "" {
		return nonNegative("withholdingAmount", given, decimal.Zero)
	}
	return money(sub.Mul(dec(cfg.WithholdingRates[typ])).Div(hundred), cur), nil
}

// RecordInvoice records a vendor invoice (FR-VIN-01).
func (m *Module) RecordInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, in VendorInvoiceInput) (VendorInvoice, error) {
	if err := handle.Required("supplierInvoiceNo", in.SupplierInvoiceNo); err != nil {
		return VendorInvoice{}, err
	}
	supplierID := in.SupplierID
	var order *PurchaseOrder
	if in.PurchaseOrderID != nil {
		o, err := GetOrder(ctx, tx, *in.PurchaseOrderID)
		if err != nil {
			return VendorInvoice{}, err
		}
		if supplierID != nil && *supplierID != o.SupplierID {
			return VendorInvoice{}, handle.Invalid("supplierId", "mismatch", "the purchase order is from another supplier")
		}
		supplierID, order = &o.SupplierID, &o
	}
	if supplierID == nil {
		return VendorInvoice{}, handle.Invalid("supplierId", "required", "select the supplier (or the purchase order)")
	}
	s, err := loadSupplier(ctx, tx, *supplierID)
	if err != nil {
		return VendorInvoice{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return VendorInvoice{}, err
	}
	lines := in.Lines
	if len(lines) == 0 && order != nil {
		if lines, err = invoiceLinesFromOrder(ctx, tx, order.ID); err != nil {
			return VendorInvoice{}, err
		}
	}
	cur, term := s.Currency, s.TermDays
	if order != nil {
		cur, term = order.Currency, order.PaymentTermDays
	} else if len(lines) > 0 && lines[0].PurchaseOrderLineID != nil {
		pl, err := loadPOLine(ctx, tx, *lines[0].PurchaseOrderLineID)
		if err != nil {
			return VendorInvoice{}, err
		}
		cur = pl.Currency
		_ = tx.QueryRow(ctx, `SELECT payment_term_days FROM procurement.purchase_orders WHERE id = $1`, pl.Order).Scan(&term)
	}
	if in.PaymentTermDays != nil {
		term = *in.PaymentTermDays
	}
	today := localToday(ctx, tx, property)
	idate, err := parseDate("invoiceDate", in.InvoiceDate)
	if err != nil {
		return VendorInvoice{}, err
	}
	if idate == nil {
		idate = &today
	}
	if err := m.openPeriod(ctx, tx, property, *idate, "invoiceDate"); err != nil {
		return VendorInvoice{}, err
	}
	rdate, err := parseDate("receivedDate", in.ReceivedDate)
	if err != nil {
		return VendorInvoice{}, err
	}
	if rdate == nil {
		rdate = &today
	}
	due, err := parseDate("dueDate", in.DueDate)
	if err != nil {
		return VendorInvoice{}, err
	}
	if due == nil {
		d := idate.AddDate(0, 0, term)
		due = &d
	}
	wtype := in.WithholdingType
	if wtype == "" {
		wtype = s.Withholding
	}
	if wtype != "none" && wtype != "pph23" && wtype != "pph4_2" {
		return VendorInvoice{}, handle.Invalid("withholdingType", "invalid", "none, pph23 or pph4_2")
	}
	num, err := nextNumber(ctx, tx, property, "VI")
	if err != nil {
		return VendorInvoice{}, err
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_invoices (id, property_id, number, supplier_id, supplier_invoice_no, tax_invoice_no, invoice_date,
		received_date, due_date, payment_term_days, currency, withholding_type, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`, iid, property, num, s.ID, strings.TrimSpace(in.SupplierInvoiceNo), nz(in.TaxInvoiceNo),
		idate, rdate, due, term, cur, wtype, nz(in.Notes), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return VendorInvoice{}, errs.Validation("duplicate_invoice", "this supplier invoice number is already recorded",
				errs.Field("supplierInvoiceNo", "taken", "already recorded for this supplier"))
		}
		return VendorInvoice{}, err
	}
	sub, tax, err := m.writeInvoiceLines(ctx, tx, property, iid, s.ID, cur, lines)
	if err != nil {
		return VendorInvoice{}, err
	}
	wh, err := withholding(cfg, wtype, in.WithholdingAmount, sub, cur)
	if err != nil {
		return VendorInvoice{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET subtotal = $2::numeric, tax_amount = $3::numeric, total = $4::numeric,
		withholding_amount = $5::numeric WHERE id = $1`, iid, sub.String(), tax.String(), sub.Add(tax).String(), wh.String()); err != nil {
		return VendorInvoice{}, err
	}
	out, err := GetInvoice(ctx, tx, iid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionCreate, EntityType: "procurement.vendor_invoice",
		EntityID: iid.String(), EntityLabel: num + " · " + in.SupplierInvoiceNo, PropertyID: &property, After: out}); err != nil {
		return out, err
	}
	if in.Match {
		return m.MatchInvoice(ctx, tx, iid)
	}
	return out, nil
}

// UpdateInvoice corrects an invoice not yet approved (back to Draft; match again).
func (m *Module) UpdateInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in VendorInvoiceInput) (VendorInvoice, error) {
	before, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return before, err
	}
	switch before.Status {
	case "draft", "matched", "mismatch", "on_hold":
	default:
		return before, errs.Conflict("invoice_not_editable", "an approved, paid or cancelled invoice cannot be corrected")
	}
	if before.ApprovalRequestID != nil {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_requests WHERE id = $1 AND status = 'pending')`, *before.ApprovalRequestID).
			Scan(&pending); err != nil {
			return before, err
		}
		if pending {
			return before, errs.Conflict("approval_pending", "the invoice is waiting for approval")
		}
	}
	property := propertyOf(ctx)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return before, err
	}
	idate, err := parseDate("invoiceDate", in.InvoiceDate)
	if err != nil {
		return before, err
	}
	due, err := parseDate("dueDate", in.DueDate)
	if err != nil {
		return before, err
	}
	if idate != nil {
		if err := m.openPeriod(ctx, tx, property, *idate, "invoiceDate"); err != nil {
			return before, err
		}
	}
	if idate != nil && due == nil {
		d := idate.AddDate(0, 0, before.PaymentTermDays)
		due = &d
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET supplier_invoice_no = coalesce($2, supplier_invoice_no),
		tax_invoice_no = coalesce($3, tax_invoice_no), invoice_date = coalesce($4, invoice_date), due_date = coalesce($5, due_date),
		notes = coalesce($6, notes), withholding_type = coalesce($7, withholding_type), status = 'draft', match_type = NULL, matched_at = NULL,
		hold_reason = NULL, held_at = NULL, approval_request_id = NULL, approval_kind = NULL, updated_by = $8 WHERE id = $1`,
		iid, nz(in.SupplierInvoiceNo), nz(in.TaxInvoiceNo), idate, due, nz(in.Notes), nz(in.WithholdingType), actor(ctx)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return before, errs.Validation("duplicate_invoice", "this supplier invoice number is already recorded",
				errs.Field("supplierInvoiceNo", "taken", "already recorded for this supplier"))
		}
		return before, err
	}
	sub, tax := dec(before.Subtotal), dec(before.TaxAmount)
	if len(in.Lines) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM procurement.vendor_invoice_lines WHERE vendor_invoice_id = $1`, iid); err != nil {
			return before, err
		}
		if sub, tax, err = m.writeInvoiceLines(ctx, tx, property, iid, before.SupplierID, before.Currency, in.Lines); err != nil {
			return before, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoice_lines SET match_status = 'pending', expected_quantity = NULL,
		expected_unit_price = NULL, match_note = NULL WHERE vendor_invoice_id = $1`, iid); err != nil {
		return before, err
	}
	wtype := before.WithholdingType
	if in.WithholdingType != "" {
		wtype = in.WithholdingType
	}
	wh, err := withholding(cfg, wtype, in.WithholdingAmount, sub, before.Currency)
	if err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET subtotal = $2::numeric, tax_amount = $3::numeric, total = $4::numeric,
		withholding_amount = $5::numeric WHERE id = $1`, iid, sub.String(), tax.String(), sub.Add(tax).String(), wh.String()); err != nil {
		return before, err
	}
	after, err := GetInvoice(ctx, tx, iid)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionUpdate, EntityType: "procurement.vendor_invoice",
		EntityID: iid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after}); err != nil {
		return after, err
	}
	if in.Match {
		return m.MatchInvoice(ctx, tx, iid)
	}
	return after, nil
}

// lineMatch is the matching result of one line.
type lineMatch struct {
	LineID        uuid.UUID `json:"lineId"`
	LineNo        int       `json:"lineNo"`
	Status        string    `json:"status"`
	Quantity      string    `json:"quantity"`
	ExpectedQty   *string   `json:"expectedQuantity,omitempty"`
	UnitPrice     string    `json:"unitPrice"`
	ExpectedPrice *string   `json:"expectedUnitPrice,omitempty"`
	Note          string    `json:"note,omitempty"`
	Variance      string    `json:"variance"`
}

// match runs the 3-way (2-way for services) matching of every line.
func (m *Module) match(ctx context.Context, tx pgx.Tx, v VendorInvoice, pol ProcurementPolicy) ([]lineMatch, string, decimal.Decimal, error) {
	out := make([]lineMatch, 0, len(v.Lines))
	goods := false
	variance := decimal.Zero
	for _, l := range v.Lines {
		lm := lineMatch{LineID: l.ID, LineNo: l.LineNo, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Status: "matched", Variance: "0"}
		qty, price := dec(l.Quantity), dec(l.UnitPrice)
		if l.LandedCost != nil { // FR-VAL-03
			st, note, err := matchLanded(ctx, tx, v, l)
			if err != nil {
				return nil, "", variance, err
			}
			lm.Status, lm.Note = st, note
			if st != "matched" {
				variance = variance.Add(dec(l.LineSubtotal))
				lm.Variance = l.LineSubtotal
			}
			out = append(out, lm)
			continue
		}
		if l.PurchaseOrderLineID == nil {
			lm.Status, lm.Note = "not_on_order", "the line is not on a purchase order"
			variance = variance.Add(dec(l.LineSubtotal))
			lm.Variance = l.LineSubtotal
			out = append(out, lm)
			continue
		}
		pl, err := loadPOLine(ctx, tx, *l.PurchaseOrderLineID)
		if err != nil {
			return nil, "", variance, err
		}
		if pl.Supplier != v.SupplierID || pl.Currency != v.Currency {
			lm.Status, lm.Note = "not_on_order", "the purchase order line belongs to another supplier or currency"
			variance = variance.Add(dec(l.LineSubtotal))
			lm.Variance = l.LineSubtotal
			out = append(out, lm)
			continue
		}
		expPrice := netPrice(pl.Price, pl.Discount)
		ep := expPrice.String()
		lm.ExpectedPrice = &ep
		var received, billed string
		if pl.OrderType == "service" {
			if err := tx.QueryRow(ctx, `SELECT service_confirmed_quantity::text FROM procurement.purchase_order_lines WHERE id = $1`, pl.ID).Scan(&received); err != nil {
				return nil, "", variance, err
			}
		} else {
			goods = true
			if l.GoodsReceiptLineID != nil {
				if err := tx.QueryRow(ctx, `SELECT accepted_quantity::text FROM procurement.goods_receipt_lines WHERE id = $1`, *l.GoodsReceiptLineID).
					Scan(&received); err != nil {
					return nil, "", variance, err
				}
			} else if err := tx.QueryRow(ctx, `SELECT coalesce(sum(l.accepted_quantity), 0)::text FROM procurement.goods_receipt_lines l
				JOIN procurement.goods_receipts g ON g.id = l.goods_receipt_id WHERE l.purchase_order_line_id = $1 AND g.status = 'posted'`, pl.ID).
				Scan(&received); err != nil {
				return nil, "", variance, err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(il.quantity), 0)::text FROM procurement.vendor_invoice_lines il
			JOIN procurement.vendor_invoices x ON x.id = il.vendor_invoice_id WHERE x.id <> $1 AND x.status IN ('matched', 'approved', 'partially_paid', 'paid')
			AND il.purchase_order_line_id = $2 AND ($3::uuid IS NULL OR il.goods_receipt_line_id = $3)`, v.ID, pl.ID, l.GoodsReceiptLineID).Scan(&billed); err != nil {
			return nil, "", variance, err
		}
		open := dec(received).Sub(dec(billed))
		eq := open.String()
		lm.ExpectedQty = &eq
		switch {
		case !dec(received).IsPositive():
			lm.Status, lm.Note = "not_received", "nothing received / confirmed on the order line"
			if pl.OrderType == "service" {
				lm.Note = "the service delivery is not confirmed"
			}
		case qty.GreaterThan(open.Mul(tolerance(pol.QuantityTolerancePercent))):
			lm.Status, lm.Note = "quantity_mismatch", fmt.Sprintf("invoiced %s, received and not yet invoiced %s", qty, open)
		case price.GreaterThan(expPrice.Mul(tolerance(pol.PriceTolerancePercent))):
			lm.Status, lm.Note = "price_mismatch", fmt.Sprintf("invoiced %s, ordered %s", price, expPrice)
		case !dec(l.TaxPercent).Equal(pl.Tax):
			lm.Status, lm.Note = "tax_mismatch", fmt.Sprintf("PPN %s%%, ordered %s%%", dec(l.TaxPercent), pl.Tax)
		}
		if lm.Status != "matched" {
			expected := money(decimal.Min(qty, decimal.Max(open, decimal.Zero)).Mul(expPrice), v.Currency)
			diff := dec(l.LineSubtotal).Sub(expected)
			if diff.IsNegative() {
				diff = decimal.Zero
			}
			variance = variance.Add(diff)
			lm.Variance = diff.String()
		}
		out = append(out, lm)
	}
	typ := "two_way"
	if goods {
		typ = "three_way"
	}
	return out, typ, variance, nil
}

// MatchInvoice performs the 3-way matching (FR-VIN-02/03).
func (m *Module) MatchInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (VendorInvoice, error) {
	v, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return v, err
	}
	switch v.Status {
	case "draft", "mismatch", "on_hold", "matched":
	default:
		return v, errs.Conflict("invoice_not_matchable", "an approved, paid or cancelled invoice is not matched again")
	}
	if v.ApprovalRequestID != nil && v.Status != "draft" {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_requests WHERE id = $1 AND status = 'pending')`, *v.ApprovalRequestID).
			Scan(&pending); err != nil {
			return v, err
		}
		if pending {
			return v, errs.Conflict("approval_pending", "the invoice is waiting for approval")
		}
	}
	property := propertyOf(ctx)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return v, err
	}
	results, typ, variance, err := m.match(ctx, tx, v, pol)
	if err != nil {
		return v, err
	}
	mismatches := 0
	for _, r := range results {
		if r.Status != "matched" {
			mismatches++
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoice_lines SET match_status = $2, expected_quantity = $3::numeric,
			expected_unit_price = $4::numeric, match_note = $5 WHERE id = $1`, r.LineID, r.Status, r.ExpectedQty, r.ExpectedPrice, nz(r.Note)); err != nil {
			return v, err
		}
	}
	result := "matched"
	if mismatches > 0 {
		result = "mismatch"
	}
	details, _ := json.Marshal(results)
	summary, _ := json.Marshal(map[string]any{"result": result, "matchType": typ, "lines": len(results), "mismatches": mismatches,
		"variance": variance.String(), "priceTolerancePercent": pol.PriceTolerancePercent, "quantityTolerancePercent": pol.QuantityTolerancePercent})
	if _, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_invoice_match_runs (id, property_id, vendor_invoice_id, result, match_type, details, run_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id.New(), property, iid, result, typ, details, actor(ctx)); err != nil {
		return v, err
	}
	status := result
	var hold *string
	if result == "mismatch" && pol.HoldOnMismatch {
		status = "on_hold"
		h := fmt.Sprintf("3-way matching: %d line(s) mismatched (variance %s)", mismatches, variance.String())
		hold = &h
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = $2, match_type = $3, matched_at = now(), match_summary = $4,
		hold_reason = $5, held_at = CASE WHEN $5::text IS NULL THEN NULL ELSE now() END, approval_request_id = NULL, approval_kind = NULL, updated_by = $6
		WHERE id = $1`, iid, status, typ, summary, hold, actor(ctx)); err != nil {
		return v, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "match", EntityType: "procurement.vendor_invoice", EntityID: iid.String(),
		EntityLabel: v.Number, PropertyID: &property, Before: map[string]any{"status": v.Status},
		After: map[string]any{"status": status, "matchType": typ, "mismatches": mismatches, "variance": variance.String()}}); err != nil {
		return v, err
	}
	if result == "matched" {
		if err := m.publish(ctx, tx, EventVendorInvoiceMatched, "procurement.vendor_invoice", iid, property, map[string]any{"vendorInvoiceId": iid,
			"number": v.Number, "supplierId": v.SupplierID, "matchType": typ, "total": v.Total, "currency": v.Currency}); err != nil {
			return v, err
		}
		if pol.SubmitMatchedInvoices {
			if err := m.submitInvoiceApproval(ctx, tx, iid, property, "approval", decimal.Zero); err != nil {
				return v, err
			}
		}
	} else if hold != nil {
		if err := m.notifyHolders(ctx, tx, property, "procurement.vendor_invoice.hold", "procurement.invoice_on_hold", map[string]any{"number": v.Number,
			"supplier": v.SupplierName, "currency": v.Currency, "total": v.Total, "reason": *hold}, "/procurement/vendor-invoices?open="+iid.String()); err != nil {
			return v, err
		}
	}
	return GetInvoice(ctx, tx, iid)
}

func (m *Module) submitInvoiceApproval(ctx context.Context, tx pgx.Tx, iid, property uuid.UUID, kind string, variance decimal.Decimal) error {
	v, err := GetInvoice(ctx, tx, iid)
	if err != nil {
		return err
	}
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET approval_kind = $2 WHERE id = $1`, iid, kind); err != nil {
		return err
	}
	dt, attrs := VendorInvoiceDocumentType, amountAttrs(pol, dec(v.Total))
	attrs["matchType"] = deref(v.MatchType)
	title := "Vendor Invoice " + v.Number + " – " + v.SupplierName
	if kind == "override" {
		fv, _ := variance.Float64()
		dt, attrs = InvoiceOverrideDocumentType, map[string]any{"amount": attrs["amount"], "variance": fv}
		title = "Mismatch override: " + title
	}
	areq, _, err := m.submitApproval(ctx, tx, dt, iid, property, v.Number, title, attrs)
	if err != nil {
		return err
	}
	// the decision may already have run (no workflow): keep its outcome
	_, err = tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET approval_request_id = $2 WHERE id = $1`,
		iid, areq)
	return err
}

// HoldInvoice puts an invoice on hold (dispute) with a reason.
func (m *Module) HoldInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, reason string) (VendorInvoice, error) {
	if err := handle.Required("reason", reason); err != nil {
		return VendorInvoice{}, err
	}
	v, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return v, err
	}
	if v.Status != "draft" && v.Status != "matched" && v.Status != "mismatch" {
		return v, errs.Conflict("invoice_not_holdable", "only draft, matched or mismatched invoices can be put on hold")
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'on_hold', hold_reason = $2, held_at = now(), updated_by = $3 WHERE id = $1`,
		iid, reason, actor(ctx)); err != nil {
		return v, err
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "hold", EntityType: "procurement.vendor_invoice", EntityID: iid.String(),
		EntityLabel: v.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": v.Status}, After: map[string]any{"status": "on_hold"}}); err != nil {
		return v, err
	}
	return GetInvoice(ctx, tx, iid)
}

// ReleaseHold releases an invoice on hold: matched again, it continues;
// still mismatched, it is approved as an override (approval, FR-VIN-03).
func (m *Module) ReleaseHold(ctx context.Context, tx pgx.Tx, iid uuid.UUID, reason string) (VendorInvoice, error) {
	if err := handle.Required("reason", reason); err != nil {
		return VendorInvoice{}, err
	}
	v, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return v, err
	}
	if v.Status != "on_hold" && v.Status != "mismatch" {
		return v, errs.Conflict("invoice_not_on_hold", "the invoice is not on hold")
	}
	property := propertyOf(ctx)
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return v, err
	}
	results, typ, variance, err := m.match(ctx, tx, v, pol)
	if err != nil {
		return v, err
	}
	matched := true
	for _, r := range results {
		matched = matched && r.Status == "matched"
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "release_hold", EntityType: "procurement.vendor_invoice", EntityID: iid.String(),
		EntityLabel: v.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": v.Status},
		Metadata: map[string]any{"matched": matched, "variance": variance.String()}}); err != nil {
		return v, err
	}
	if matched {
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'draft', hold_reason = NULL, held_at = NULL WHERE id = $1`, iid); err != nil {
			return v, err
		}
		return m.MatchInvoice(ctx, tx, iid)
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'on_hold', override_reason = $2, match_type = $3, updated_by = $4 WHERE id = $1`,
		iid, reason, typ, actor(ctx)); err != nil {
		return v, err
	}
	if err := m.submitInvoiceApproval(ctx, tx, iid, property, "override", variance); err != nil {
		return v, err
	}
	return GetInvoice(ctx, tx, iid)
}

// ApproveInvoice approves the current approval step of an invoice, or
// submits a matched invoice for approval.
func (m *Module) ApproveInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, reason string) (VendorInvoice, error) {
	v, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return v, err
	}
	property := propertyOf(ctx)
	if v.ApprovalRequestID != nil {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_requests WHERE id = $1 AND status = 'pending')`, *v.ApprovalRequestID).
			Scan(&pending); err != nil {
			return v, err
		}
		if pending {
			if err := m.decide(ctx, tx, v.ApprovalRequestID, true, reason); err != nil {
				return v, err
			}
			return GetInvoice(ctx, tx, iid)
		}
	}
	if v.Status != "matched" {
		return v, errs.Conflict("invoice_not_matched", "only a matched invoice can be approved; resolve the mismatch (correct, debit note or release hold)")
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "submit", EntityType: "procurement.vendor_invoice", EntityID: iid.String(),
		EntityLabel: v.Number, PropertyID: &property, Reason: reason}); err != nil {
		return v, err
	}
	if err := m.submitInvoiceApproval(ctx, tx, iid, property, "approval", decimal.Zero); err != nil {
		return v, err
	}
	return GetInvoice(ctx, tx, iid)
}

func (m *Module) invoiceDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	v, err := lockInvoice(ctx, tx, d.DocumentID)
	if err != nil {
		return err
	}
	if v.Status != "matched" && v.Status != "on_hold" && v.Status != "mismatch" {
		return nil
	}
	if d.Status != approval.StatusApproved {
		reason := "approval " + d.Status
		if d.Reason != "" {
			reason += ": " + d.Reason
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'on_hold', hold_reason = $2, held_at = now() WHERE id = $1`,
			v.ID, reason); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.vendor_invoice",
			EntityID: v.ID.String(), EntityLabel: v.Number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": v.Status},
			After: map[string]any{"status": "on_hold"}})
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'approved', approved_at = now(), approved_by = $2, hold_reason = NULL
		WHERE id = $1`, v.ID, nilIfZero(d.DecidedBy)); err != nil {
		return err
	}
	// Invoiced quantities on the order lines.
	if _, err := tx.Exec(ctx, `UPDATE procurement.purchase_order_lines pl SET invoiced_quantity = pl.invoiced_quantity + x.q
		FROM (SELECT purchase_order_line_id, sum(quantity) AS q FROM procurement.vendor_invoice_lines WHERE vendor_invoice_id = $1
		  AND purchase_order_line_id IS NOT NULL GROUP BY 1) x WHERE pl.id = x.purchase_order_line_id`, v.ID); err != nil {
		return err
	}
	// Debit notes of the same orders not yet tied to an invoice reduce this payable.
	var dnIDs []uuid.UUID
	rows, err := tx.Query(ctx, `UPDATE procurement.debit_notes d SET vendor_invoice_id = $1, status = 'applied' WHERE d.vendor_invoice_id IS NULL
		AND d.supplier_id = $2 AND d.purchase_order_id IN (SELECT pl.purchase_order_id FROM procurement.vendor_invoice_lines l
		  JOIN procurement.purchase_order_lines pl ON pl.id = l.purchase_order_line_id WHERE l.vendor_invoice_id = $1) RETURNING d.id`, v.ID, v.SupplierID)
	if err != nil {
		return err
	}
	if dnIDs, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices v SET debit_note_total = coalesce((SELECT sum(amount) FROM procurement.debit_notes
		WHERE vendor_invoice_id = v.id), 0) WHERE v.id = $1`, v.ID); err != nil {
		return err
	}
	var allDN []uuid.UUID
	rows, err = tx.Query(ctx, `SELECT id FROM procurement.debit_notes WHERE vendor_invoice_id = $1 ORDER BY created_at`, v.ID)
	if err != nil {
		return err
	}
	if allDN, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return err
	}
	if err := refreshInvoicePayment(ctx, tx, v.ID); err != nil {
		return err
	}
	after, err := GetInvoice(ctx, tx, v.ID)
	if err != nil {
		return err
	}
	// GRs behind the invoiced lines (the line's GR, else the order line's GRs).
	var grs []uuid.UUID
	rows, err = tx.Query(ctx, `SELECT DISTINCT g.id FROM procurement.vendor_invoice_lines l JOIN procurement.goods_receipt_lines gl
		ON gl.id = l.goods_receipt_line_id OR (l.goods_receipt_line_id IS NULL AND gl.purchase_order_line_id = l.purchase_order_line_id)
		JOIN procurement.goods_receipts g ON g.id = gl.goods_receipt_id AND g.status = 'posted' WHERE l.vendor_invoice_id = $1`, v.ID)
	if err != nil {
		return err
	}
	if grs, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return err
	}
	type evLine struct {
		ItemID      *uuid.UUID `json:"itemId,omitempty"`
		Description string     `json:"description"`
		Quantity    string     `json:"quantity"`
		UnitPrice   string     `json:"unitPrice"`
		Total       string     `json:"total" doc:"Net of PPN"`
		TaxAmount   string     `json:"taxAmount"`
		AccountHint string     `json:"accountHint"`
	}
	lines := make([]evLine, 0, len(after.Lines))
	for _, l := range after.Lines {
		lines = append(lines, evLine{ItemID: l.ItemID, Description: l.Description, Quantity: l.Quantity, UnitPrice: l.UnitPrice, Total: l.LineSubtotal,
			TaxAmount: l.TaxAmount, AccountHint: l.AccountHint})
	}
	if grs == nil {
		grs = []uuid.UUID{}
	}
	if allDN == nil {
		allDN = []uuid.UUID{}
	}
	if err := m.publish(ctx, tx, EventVendorInvoiceApproved, "procurement.vendor_invoice", v.ID, d.PropertyID, map[string]any{
		"vendorInvoiceId": v.ID, "number": v.Number, "supplierInvoiceNo": v.SupplierInvoiceNo, "supplierId": v.SupplierID, "invoiceDate": v.InvoiceDate,
		"dueDate": v.DueDate, "currency": v.Currency, "subtotal": v.Subtotal, "taxAmount": v.TaxAmount, "total": v.Total, "withholding": v.WithholdingAmount,
		"withholdingType": v.WithholdingType, "taxInvoiceNo": v.TaxInvoiceNo, "goodsReceiptIds": grs, "purchaseOrderIds": after.PurchaseOrderIDs,
		"debitNoteIds": allDN, "debitNoteTotal": after.DebitNoteTotal, "matchType": v.MatchType, "approvalKind": v.ApprovalKind, "lines": lines}); err != nil {
		return err
	}
	if err := m.publishPriceVariance(ctx, tx, d.PropertyID, after); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: audit.ActionStatusChange, EntityType: "procurement.vendor_invoice",
		EntityID: v.ID.String(), EntityLabel: v.Number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": v.Status},
		After: map[string]any{"status": after.Status, "debitNotesApplied": len(dnIDs)}})
}

// publishPriceVariance publishes procurement.invoice_price_variance (FR-VAL-06,
// contract proposed by inventory) once per goods receipt whose received
// base unit cost differs from the approved invoice price: inventory
// revalues the received quantity still in stock.
func (m *Module) publishPriceVariance(ctx context.Context, tx pgx.Tx, property uuid.UUID, v VendorInvoice) error {
	rows, err := tx.Query(ctx, `SELECT g.id, g.warehouse_id, gl.item_id, round(il.unit_price * gl.accepted_quantity / gl.base_quantity, 6)::text,
		gl.base_unit_cost::text FROM procurement.vendor_invoice_lines il
		JOIN procurement.goods_receipt_lines gl ON gl.id = il.goods_receipt_line_id
		  OR (il.goods_receipt_line_id IS NULL AND gl.purchase_order_line_id = il.purchase_order_line_id)
		JOIN procurement.goods_receipts g ON g.id = gl.goods_receipt_id AND g.status = 'posted'
		WHERE il.vendor_invoice_id = $1 AND il.account_hint = 'inventory' AND gl.item_id IS NOT NULL AND gl.base_quantity > 0 AND gl.accepted_quantity > 0
		ORDER BY g.number, gl.line_no`, v.ID)
	if err != nil {
		return err
	}
	type pv struct {
		gr, wh, item uuid.UUID
		invoiced     string
		received     string
	}
	var list []pv
	for rows.Next() {
		var x pv
		if err := rows.Scan(&x.gr, &x.wh, &x.item, &x.invoiced, &x.received); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// FR-VAL-03: landed cost per base unit on top of the invoiced (else received) cost
	landed, landedOrder, err := landedShares(ctx, tx, v)
	if err != nil {
		return err
	}
	for i, x := range list {
		if s, ok := landed[landedKey{x.gr, x.item}]; ok && !s.perUnit.IsZero() {
			list[i].invoiced = dec(x.invoiced).Add(s.perUnit).String()
			s.perUnit = decimal.Zero // applied
		}
	}
	for _, k := range landedOrder {
		if s := landed[k]; !s.perUnit.IsZero() {
			list = append(list, pv{gr: k.gr, wh: s.wh, item: k.item, invoiced: s.received.Add(s.perUnit).String(), received: s.received.String()})
		}
	}
	byGR := map[uuid.UUID][]map[string]any{}
	whOf := map[uuid.UUID]uuid.UUID{}
	var order []uuid.UUID
	seen := map[[2]uuid.UUID]bool{}
	for _, x := range list {
		if dec(x.invoiced).Equal(dec(x.received)) || seen[[2]uuid.UUID{x.gr, x.item}] {
			continue
		}
		seen[[2]uuid.UUID{x.gr, x.item}] = true
		if _, ok := byGR[x.gr]; !ok {
			order = append(order, x.gr)
			whOf[x.gr] = x.wh
		}
		byGR[x.gr] = append(byGR[x.gr], map[string]any{"itemId": x.item, "invoicedBaseUnitCost": dec(x.invoiced).String(),
			"receivedBaseUnitCost": dec(x.received).String()})
	}
	for _, gr := range order {
		if err := m.publish(ctx, tx, EventInvoicePriceVariance, "procurement.vendor_invoice", v.ID, property, map[string]any{"vendorInvoiceId": v.ID,
			"number": v.Number, "goodsReceiptId": gr, "warehouseId": whOf[gr], "currency": v.Currency, "lines": byGR[gr]}); err != nil {
			return err
		}
	}
	return nil
}

// refreshInvoicePayment derives Approved / Partially Paid / Paid.
func refreshInvoicePayment(ctx context.Context, tx pgx.Tx, iid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = CASE
		WHEN paid_amount > 0 AND paid_amount >= total - withholding_amount - debit_note_total THEN 'paid'
		WHEN paid_amount > 0 THEN 'partially_paid' ELSE 'approved' END,
		paid_at = CASE WHEN paid_amount > 0 AND paid_amount >= total - withholding_amount - debit_note_total THEN coalesce(paid_at, now()) ELSE NULL END
		WHERE id = $1 AND status IN ('approved', 'partially_paid', 'paid')`, iid)
	return err
}

// CancelInvoice cancels an invoice not yet approved.
func (m *Module) CancelInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, reason string) (VendorInvoice, error) {
	if err := handle.Required("reason", reason); err != nil {
		return VendorInvoice{}, err
	}
	v, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return v, err
	}
	switch v.Status {
	case "draft", "matched", "mismatch", "on_hold":
	default:
		return v, errs.Conflict("invoice_not_cancellable", "an approved, paid or cancelled invoice cannot be cancelled; issue a debit note")
	}
	if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET status = 'cancelled', cancelled_reason = $2, updated_by = $3 WHERE id = $1`,
		iid, reason, actor(ctx)); err != nil {
		return v, err
	}
	property := propertyOf(ctx)
	if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "cancel", EntityType: "procurement.vendor_invoice", EntityID: iid.String(),
		EntityLabel: v.Number, PropertyID: &property, Reason: reason, Before: map[string]any{"status": v.Status},
		After: map[string]any{"status": "cancelled"}}); err != nil {
		return v, err
	}
	return GetInvoice(ctx, tx, iid)
}

// PaymentPayload is accounting.vendor_payment_made.
type PaymentPayload struct {
	PaymentID   uuid.UUID `json:"paymentId"`
	Number      string    `json:"number"`
	SupplierID  uuid.UUID `json:"supplierId"`
	PaidDate    string    `json:"paidDate"`
	Currency    string    `json:"currency"`
	Allocations []struct {
		VendorInvoiceID uuid.UUID `json:"vendorInvoiceId"`
		Amount          string    `json:"amount"`
	} `json:"allocations"`
}

// OnVendorPaymentMade applies an accounting payment to the invoices
// (idempotent per payment and invoice): Partially Paid / Paid.
func (m *Module) OnVendorPaymentMade(ctx context.Context, tx pgx.Tx, property uuid.UUID, p PaymentPayload) error {
	paid, _ := parseDate("paidDate", p.PaidDate)
	for _, a := range p.Allocations {
		amount := dec(a.Amount)
		if !amount.IsPositive() {
			continue
		}
		var cur, status, number string
		err := tx.QueryRow(ctx, `SELECT currency, status, number FROM procurement.vendor_invoices WHERE id = $1 AND property_id = $2 FOR UPDATE`,
			a.VendorInvoiceID, property).Scan(&cur, &status, &number)
		if dbtx.IsNoRows(err) {
			continue // not a procurement invoice of this property
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO procurement.vendor_invoice_payments (id, property_id, vendor_invoice_id, payment_id, payment_number, paid_date,
			amount, currency) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8) ON CONFLICT (payment_id, vendor_invoice_id) DO NOTHING`, id.New(), property,
			a.VendorInvoiceID, p.PaymentID, nz(p.Number), paid, amount.String(), cur)
		if err != nil || tag.RowsAffected() == 0 {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE procurement.vendor_invoices SET paid_amount = paid_amount + $2::numeric WHERE id = $1`,
			a.VendorInvoiceID, amount.String()); err != nil {
			return err
		}
		if err := refreshInvoicePayment(ctx, tx, a.VendorInvoiceID); err != nil {
			return err
		}
		var after string
		if err := tx.QueryRow(ctx, `SELECT status FROM procurement.vendor_invoices WHERE id = $1`, a.VendorInvoiceID).Scan(&after); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "procurement", Action: "payment", Category: audit.CategorySystem,
			ActorName: "Accounting (accounting.vendor_payment_made)", EntityType: "procurement.vendor_invoice", EntityID: a.VendorInvoiceID.String(),
			EntityLabel: number, PropertyID: &property, Before: map[string]any{"status": status},
			After: map[string]any{"status": after, "paymentId": p.PaymentID, "amount": amount.String()}}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) registerInvoices(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "procurement", "Vendor Invoices", route.ScopeProperty
		reg.Add(rt)
	}
	const base = "/api/v1/procurement/vendor-invoices"
	add(route.Route{Method: http.MethodGet, Path: base, Summary: "Vendor invoices (overdue=true: open invoices past due)", Permission: "procurement.vendor_invoice.view",
		Response: VendorInvoice{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[supplierId]"},
			{Name: "overdue", Type: "boolean"}, {Name: "from"}, {Name: "to"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorInvoice], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[VendorInvoice](tx.Query(ctx, invoiceSelect+` WHERE v.property_id = $1
				AND ($2 = '' OR v.status = ANY(string_to_array($2, ','))) AND ($3 = '' OR v.supplier_id::text = $3)
				AND (NOT $4 OR (v.status IN ('approved', 'partially_paid') AND v.due_date < current_date))
				AND ($5 = '' OR v.invoice_date >= $5::date) AND ($6 = '' OR v.invoice_date <= $6::date)
				AND ($7 = '' OR v.number ILIKE '%' || $7 || '%' OR v.supplier_invoice_no ILIKE '%' || $7 || '%' OR s.name ILIKE '%' || $7 || '%')
				ORDER BY v.created_at DESC LIMIT $8`, handle.Property(ctx), lp.Filters["status"], lp.Filters["supplierId"], r.URL.Query().Get("overdue") == "true",
				r.URL.Query().Get("from"), r.URL.Query().Get("to"), lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: base, Summary: "Record Vendor Invoice (lines linked to PO / GR; match=true runs the 3-way matching)",
		Permission: "procurement.vendor_invoice.create", Request: VendorInvoiceInput{}, Response: VendorInvoice{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, _ *http.Request, in VendorInvoiceInput) (VendorInvoice, error) {
			return m.RecordInvoice(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Vendor invoice with lines and matching result", Permission: "procurement.vendor_invoice.view",
		Response: VendorInvoice{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (VendorInvoice, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return VendorInvoice{}, err
			}
			return GetInvoice(ctx, tx, iid)
		})})
	add(route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Correct a vendor invoice not yet approved (back to Draft)",
		Permission: "procurement.vendor_invoice.update", Request: VendorInvoiceInput{}, Response: VendorInvoice{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in VendorInvoiceInput) (VendorInvoice, error) {
			iid, err := handle.ID(r)
			if err != nil {
				return VendorInvoice{}, err
			}
			return m.UpdateInvoice(ctx, tx, iid, in)
		})})
	act := func(path, summary, perm string, fn func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in ProcurementReasonInput) (VendorInvoice, error)) {
		add(route.Route{Method: http.MethodPost, Path: base + "/{id}" + path, Summary: summary, Permission: perm, Request: ProcurementReasonInput{},
			Response: VendorInvoice{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ProcurementReasonInput) (VendorInvoice, error) {
				iid, err := handle.ID(r)
				if err != nil {
					return VendorInvoice{}, err
				}
				return fn(ctx, tx, iid, in)
			})})
	}
	act(":match", "Perform 3-Way Matching (PO + GR + invoice; 2-way for services)", "procurement.vendor_invoice.match",
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, _ ProcurementReasonInput) (VendorInvoice, error) {
			return m.MatchInvoice(ctx, tx, iid)
		})
	act(":hold", "Put the vendor invoice on hold (reason)", "procurement.vendor_invoice.hold",
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in ProcurementReasonInput) (VendorInvoice, error) {
			return m.HoldInvoice(ctx, tx, iid, in.Reason)
		})
	act(":release-hold", "Release hold: match again, or approve the mismatch as an override (reason, approval)", "procurement.vendor_invoice.hold",
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in ProcurementReasonInput) (VendorInvoice, error) {
			return m.ReleaseHold(ctx, tx, iid, in.Reason)
		})
	act(":approve", "Approve the vendor invoice (current approval step, or submit a matched invoice)", "procurement.vendor_invoice.approve",
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in ProcurementReasonInput) (VendorInvoice, error) {
			return m.ApproveInvoice(ctx, tx, iid, in.Reason)
		})
	act(":cancel", "Cancel a vendor invoice not yet approved", "procurement.vendor_invoice.cancel",
		func(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in ProcurementReasonInput) (VendorInvoice, error) {
			return m.CancelInvoice(ctx, tx, iid, in.Reason)
		})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}/match-runs", Summary: "Matching history of the vendor invoice",
		Permission: "procurement.vendor_invoice.view", Response: VendorInvoiceMatchRun{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorInvoiceMatchRun], error) {
			iid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[VendorInvoiceMatchRun]{}, err
			}
			return handle.Page(handle.List[VendorInvoiceMatchRun](tx.Query(ctx, `SELECT id, result, match_type, details, run_by, run_at FROM procurement.vendor_invoice_match_runs
				WHERE vendor_invoice_id = $1 ORDER BY run_at DESC`, iid)))
		})})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}/payments", Summary: "Payments applied by accounting", Permission: "procurement.vendor_invoice.view",
		Response: VendorInvoicePayment{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[VendorInvoicePayment], error) {
			iid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[VendorInvoicePayment]{}, err
			}
			return handle.Page(handle.List[VendorInvoicePayment](tx.Query(ctx, `SELECT payment_id, payment_number, to_char(paid_date, 'YYYY-MM-DD') AS paid_date,
				trim_scale(amount)::text AS amount, currency, created_at FROM procurement.vendor_invoice_payments WHERE vendor_invoice_id = $1 ORDER BY created_at`, iid)))
		})})
}
