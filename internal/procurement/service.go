package procurement

// PRD P4 EP-10..EP-15 Procurement: the procure-to-pay module. This file
// holds the module wiring (services, approval document types, events,
// permissions, notification templates) and shared helpers; each document
// lives in its own file (requisition, rfq, order, receipt, invoice,
// performance).
//
// Dependencies (Technical Doc §4.2): procurement reads Inventory through its
// root package (items, UOM conversion — same back-office layer); everything
// else reaches procurement through domain events (inventory.reorder_needed,
// banquet.beo_issued / beo_revised, accounting.vendor_payment_made), and
// procurement publishes procurement.goods_received / purchase_returned /
// vendor_invoice_approved / debit_note_issued for inventory and accounting
// (docs/p3-p4-contracts.md).

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Events published by procurement (docs/p3-p4-contracts.md) and consumed.
const (
	EventGoodsReceived         = "procurement.goods_received"
	EventPurchaseReturned      = "procurement.purchase_returned"
	EventVendorInvoiceApproved = "procurement.vendor_invoice_approved"
	EventDebitNoteIssued       = "procurement.debit_note_issued"
	EventRequisitionApproved   = "procurement.requisition_approved"
	EventPurchaseOrderApproved = "procurement.po_approved"
	EventVendorInvoiceMatched  = "procurement.vendor_invoice_matched"
	EventInvoicePriceVariance  = "procurement.invoice_price_variance"

	EventReorderNeeded     = "inventory.reorder_needed"
	EventBEOIssued         = "banquet.beo_issued"
	EventBEORevised        = "banquet.beo_revised"
	EventVendorPaymentMade = "accounting.vendor_payment_made"
)

// Approval document types (approval engine, FR-PR-03, FR-PO-02, FR-RFQ-03,
// FR-GR-02/03, FR-VIN-03, FR-SUP-04). Without a configured workflow a
// request is approved immediately.
var (
	amountAttr = provision.DocumentAttribute{Key: "amount", Label: "Amount", Type: "number"}
	levelAttr  = provision.DocumentAttribute{Key: "approvalLevel", Label: "Approval level (Procurement Policies tier)", Type: "number"}

	RequisitionDocumentType = provision.DocumentType{Code: "purchase_requisition", Module: "procurement", Name: "Purchase Requisition",
		Attributes: []provision.DocumentAttribute{amountAttr, levelAttr, {Key: "source", Label: "Source", Type: "string"},
			{Key: "costCenter", Label: "Cost center", Type: "string"}, {Key: "category", Label: "Category", Type: "string"},
			{Key: "departmentId", Label: "Department", Type: "uuid"}}}
	PurchaseOrderDocumentType = provision.DocumentType{Code: "purchase_order", Module: "procurement", Name: "Purchase Order",
		Attributes: []provision.DocumentAttribute{amountAttr, levelAttr, {Key: "orderType", Label: "Order type", Type: "string"},
			{Key: "supplierId", Label: "Supplier", Type: "uuid"}, {Key: "rfqSkipped", Label: "RFQ skipped (1 = yes)", Type: "number"},
			{Key: "revision", Label: "Revision (version > 1)", Type: "number"}}}
	QuotationSelectionDocumentType = provision.DocumentType{Code: "vendor_quotation_selection", Module: "procurement", Name: "Vendor Quotation Selection",
		Attributes: []provision.DocumentAttribute{amountAttr, {Key: "priceDifference", Label: "Above the cheapest quotation", Type: "number"},
			{Key: "quotations", Label: "Quotations compared", Type: "number"}}}
	GoodsReceiptDocumentType = provision.DocumentType{Code: "goods_receipt_exception", Module: "procurement", Name: "Goods Receipt Exception",
		Attributes: []provision.DocumentAttribute{amountAttr, {Key: "reason", Label: "Reason (over_receipt, without_po)", Type: "string"},
			{Key: "excessPercent", Label: "Over-receipt (%)", Type: "number"}}}
	VendorInvoiceDocumentType = provision.DocumentType{Code: "vendor_invoice", Module: "procurement", Name: "Vendor Invoice",
		Attributes: []provision.DocumentAttribute{amountAttr, levelAttr, {Key: "matchType", Label: "Match type", Type: "string"}}}
	InvoiceOverrideDocumentType = provision.DocumentType{Code: "vendor_invoice_override", Module: "procurement", Name: "Vendor Invoice Mismatch Override",
		Attributes: []provision.DocumentAttribute{amountAttr, {Key: "variance", Label: "Variance amount", Type: "number"}}}
	SupplierStatusDocumentType = provision.DocumentType{Code: "supplier_status_change", Module: "procurement", Name: "Supplier Block / Unblock",
		Attributes: []provision.DocumentAttribute{{Key: "action", Label: "Action (block, unblock)", Type: "string"}}}
)

// DocumentTypes are the approval document types of procurement.
var DocumentTypes = []provision.DocumentType{RequisitionDocumentType, PurchaseOrderDocumentType, QuotationSelectionDocumentType,
	GoodsReceiptDocumentType, VendorInvoiceDocumentType, InvoiceOverrideDocumentType, SupplierStatusDocumentType}

// Publisher publishes domain events in the business transaction (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Module is the Procurement module.
type Module struct {
	DB         *dbtx.DB
	Events     Publisher
	Approvals  *approval.Engine
	Notify     notify.Sender
	PublicURL  func() string // API / Staff App base URL (supplier PDF links)
	WebsiteURL func() string // website base URL (supplier RFQ response page)
	period     atomic.Pointer[PeriodStatusFunc]
}

// Decisions maps the document types to their decision hooks (wired by
// internal/app with the approval engine).
func (m *Module) Decisions() map[string]approval.DecisionHook {
	return map[string]approval.DecisionHook{
		RequisitionDocumentType.Code:        m.requisitionDecision,
		PurchaseOrderDocumentType.Code:      m.orderDecision,
		QuotationSelectionDocumentType.Code: m.selectionDecision,
		GoodsReceiptDocumentType.Code:       m.receiptDecision,
		VendorInvoiceDocumentType.Code:      m.invoiceDecision,
		InvoiceOverrideDocumentType.Code:    m.invoiceDecision,
		SupplierStatusDocumentType.Code:     m.supplierStatusDecision,
	}
}

// ── permissions & role templates ─────────────────────────────────────────

func perms(object string, actions ...string) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, "procurement."+object+"."+a)
	}
	return out
}

func unionOf(groups ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range groups {
		for _, s := range g {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// P4Contribution returns the catalogue entries of PRD P4 procurement
// (P0 keeps procurement.supplier.* in Contribution).
func P4Contribution() catalog.Contribution {
	var ps []catalog.Permission
	ps = append(ps, resource.Permissions(SupplierItems, SupplierBankAccounts)...)
	ps = append(ps, catalog.P("procurement", "supplier", "block")...)
	ps = append(ps, catalog.Permission{Code: "procurement.supplier_bank_account.view_sensitive", Description: "View full supplier bank account numbers and NPWP"})
	ps = append(ps, catalog.P("procurement", "vendor_performance", "view", "compute")...)
	ps = append(ps, catalog.P("procurement", "requisition", "view", "create", "update", "submit", "approve", "cancel")...)
	ps = append(ps, catalog.P("procurement", "rfq", "view", "create", "update", "send", "cancel")...)
	ps = append(ps, catalog.P("procurement", "vendor_quotation", "view", "create", "update", "select")...)
	ps = append(ps, catalog.P("procurement", "purchase_order", "view", "create", "update", "submit", "approve", "send", "cancel", "close")...)
	ps = append(ps, catalog.P("procurement", "goods_receipt", "view", "create")...)
	ps = append(ps, catalog.P("procurement", "purchase_return", "view", "create")...)
	ps = append(ps, catalog.P("procurement", "debit_note", "view", "create")...)
	ps = append(ps, catalog.P("procurement", "vendor_invoice", "view", "create", "update", "match", "hold", "approve", "cancel")...)
	ps = append(ps, catalog.P("procurement", "migration", "import")...)
	all := make([]string, 0, len(ps))
	for _, p := range ps {
		all = append(all, p.Code)
	}
	view := unionOf(perms("supplier_item", "view"), perms("vendor_performance", "view"), perms("requisition", "view"), perms("rfq", "view"),
		perms("vendor_quotation", "view"), perms("purchase_order", "view"), perms("goods_receipt", "view"), perms("purchase_return", "view"),
		perms("debit_note", "view"), perms("vendor_invoice", "view"))
	staff := unionOf(view, perms("supplier_item", "create", "update", "export", "import"), perms("supplier_bank_account", "view"),
		perms("requisition", "create", "update", "submit", "cancel"), perms("rfq", "create", "update", "send", "cancel"),
		perms("vendor_quotation", "create", "update", "select"), perms("purchase_order", "create", "update", "submit", "send", "cancel"),
		perms("goods_receipt", "create"), perms("purchase_return", "create"), perms("vendor_invoice", "create", "update", "match"))
	finance := unionOf(view, perms("supplier_bank_account", "view", "create", "update", "delete", "export", "view_sensitive"),
		perms("requisition", "approve"), perms("purchase_order", "approve"), perms("debit_note", "create"),
		perms("vendor_invoice", "create", "update", "match", "hold", "approve", "cancel"))
	requester := unionOf(perms("requisition", "view", "create", "update", "submit", "cancel"), perms("purchase_order", "view"),
		perms("goods_receipt", "view"), perms("supplier_item", "view"))
	return catalog.Contribution{
		Permissions: ps,
		RolePermissions: map[string][]string{
			"property_admin":      all,
			"procurement_manager": unionOf(all, []string{"procurement.supplier.view", "procurement.supplier.create", "procurement.supplier.update"}),
			"procurement_staff":   staff,
			"approver":            unionOf(view, perms("requisition", "approve"), perms("purchase_order", "approve"), perms("vendor_invoice", "approve"), []string{"procurement.module.access", "procurement.supplier.view"}),
			"general_manager":     unionOf(view, perms("requisition", "approve"), perms("purchase_order", "approve"), perms("vendor_invoice", "approve"), perms("supplier", "block"), []string{"procurement.supplier.view"}),
			"finance_manager":     unionOf(finance, perms("supplier", "view", "block"), perms("vendor_performance", "compute")),
			"accountant": unionOf(view, perms("supplier_bank_account", "view", "view_sensitive"), perms("debit_note", "create"),
				perms("vendor_invoice", "create", "update", "match", "hold"), []string{"procurement.supplier.view", "procurement.module.access"}),
			"inventory_manager": unionOf(requester, perms("goods_receipt", "create"), perms("purchase_return", "view", "create"),
				perms("vendor_performance", "view"), []string{"procurement.supplier.view"}),
			"warehouse_staff": unionOf(perms("goods_receipt", "view", "create"), perms("purchase_order", "view"), perms("purchase_return", "view", "create"),
				[]string{"procurement.supplier.view"}),
			"outlet_manager":  requester,
			"banquet_manager": perms("requisition", "view"),
		},
	}
}

// Templates are the notification templates of procurement (ID / EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"procurement.rfq": {
			"en": {"Request for Quotation {{.number}} – {{.club}}", "Dear {{.supplier}},\n\n{{.club}} invites you to quote for {{.title}} ({{.lines}} item(s)) " +
				"before {{.dueAt}}.\nView the request and submit your quotation: {{.link}}\n\nThank you."},
			"id": {"Permintaan Penawaran {{.number}} – {{.club}}", "Yth. {{.supplier}},\n\n{{.club}} mengundang Anda mengajukan penawaran untuk {{.title}} " +
				"({{.lines}} item) sebelum {{.dueAt}}.\nLihat permintaan dan kirim penawaran: {{.link}}\n\nTerima kasih."},
		},
		"procurement.purchase_order": {
			"en": {"Purchase Order {{.number}} – {{.club}}", "Dear {{.supplier}},\n\nPlease find purchase order {{.number}} (version {{.version}}) of " +
				"{{.currency}} {{.total}}, delivery by {{.expectedDate}}.\nPDF: {{.link}}\n\nPlease quote the PO number on the delivery note and invoice."},
			"id": {"Pesanan Pembelian {{.number}} – {{.club}}", "Yth. {{.supplier}},\n\nBerikut pesanan pembelian {{.number}} (versi {{.version}}) senilai " +
				"{{.currency}} {{.total}}, dikirim paling lambat {{.expectedDate}}.\nPDF: {{.link}}\n\nCantumkan nomor PO pada surat jalan dan faktur."},
		},
		"procurement.requisition_review": {
			"en": {"Purchase requisition {{.number}} to review", "{{.reason}}: purchase requisition {{.number}} ({{.lines}} line(s)) is waiting in Draft."},
			"id": {"Purchase requisition {{.number}} perlu ditinjau", "{{.reason}}: purchase requisition {{.number}} ({{.lines}} baris) menunggu dalam status Draft."},
		},
		"procurement.invoice_on_hold": {
			"en": {"Vendor invoice {{.number}} on hold", "Vendor invoice {{.number}} of {{.supplier}} ({{.currency}} {{.total}}) is On Hold: {{.reason}}."},
			"id": {"Vendor invoice {{.number}} ditahan", "Vendor invoice {{.number}} dari {{.supplier}} ({{.currency}} {{.total}}) berstatus On Hold: {{.reason}}."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// ── helpers ───────────────────────────────────────────────────────────────

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(strings.TrimSpace(s))
	return d
}

func decp(s *string) decimal.Decimal {
	if s == nil {
		return decimal.Zero
	}
	return dec(*s)
}

var hundred = decimal.NewFromInt(100)

// places is the number of decimals of a currency (IDR has none).
func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" || cur == "" {
		return 0
	}
	return 2
}

func money(d decimal.Decimal, cur string) decimal.Decimal { return d.Round(places(cur)) }

func nz(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func propertyOf(ctx context.Context) uuid.UUID {
	pid, _ := reqctx.Property(ctx)
	return pid
}

// localNow is the property's local time; localToday its calendar date.
func localNow(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	return clock.Now().In(loc)
}

func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	n := localNow(ctx, q, property)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// nextNumber issues PREFIX-YYYYMMDD-NNNN per property and local day.
func nextNumber(ctx context.Context, tx pgx.Tx, property uuid.UUID, prefix string) (string, error) {
	return numbering.Next(ctx, tx, property, prefix, localNow(ctx, tx, property))
}

func parseDate(field, v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, errs.Validation("invalid_date", field+" must be a date (YYYY-MM-DD)", errs.Field(field, "invalid_date", "must be YYYY-MM-DD"))
	}
	return &t, nil
}

// positive parses a required positive decimal field.
func positive(field, v string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(v))
	if err != nil || !d.IsPositive() {
		return d, errs.Validation("invalid_quantity", field+" must be a positive number", errs.Field(field, "invalid", "must be a positive number"))
	}
	return d, nil
}

// nonNegative parses an optional non-negative decimal field ("" = def).
func nonNegative(field, v string, def decimal.Decimal) (decimal.Decimal, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	d, err := decimal.NewFromString(strings.TrimSpace(v))
	if err != nil || d.IsNegative() {
		return d, errs.Validation("invalid_amount", field+" must be zero or more", errs.Field(field, "invalid", "must be zero or more"))
	}
	return d, nil
}

func percent(field, v string, def decimal.Decimal) (decimal.Decimal, error) {
	d, err := nonNegative(field, v, def)
	if err == nil && d.GreaterThan(hundred) {
		return d, errs.Validation("invalid_percent", field+" must be at most 100", errs.Field(field, "invalid", "must be between 0 and 100"))
	}
	return d, err
}

// lineAmounts returns subtotal (after discount), tax and total in the
// currency's precision.
func lineAmounts(qty, price, discountPct, taxPct decimal.Decimal, cur string) (sub, tax, total, gross decimal.Decimal) {
	gross = money(qty.Mul(price), cur)
	sub = money(qty.Mul(price).Mul(decimal.NewFromInt(1).Sub(discountPct.Div(hundred))), cur)
	tax = money(sub.Mul(taxPct).Div(hundred), cur)
	return sub, tax, sub.Add(tax), gross
}

// netPrice is the unit price after the line discount.
func netPrice(price, discountPct decimal.Decimal) decimal.Decimal {
	return price.Mul(decimal.NewFromInt(1).Sub(discountPct.Div(hundred))).Round(6)
}

// supplierInfo is the supplier data procurement documents use.
type supplierInfo struct {
	ID          uuid.UUID
	Code, Name  string
	Email       *string
	NPWP        *string
	Address     *string
	Status      string
	PKP         bool
	Withholding string
	TermDays    int
	Currency    string
	LeadDays    int
	Contract    bool
}

func loadSupplier(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (supplierInfo, error) {
	var s supplierInfo
	err := q.QueryRow(ctx, `SELECT id, code, name, email, npwp, address, status, pkp, withholding_type, payment_term_days, currency, lead_time_days,
		contract_supplier FROM procurement.suppliers WHERE id = $1 AND archived_at IS NULL`, sid).
		Scan(&s.ID, &s.Code, &s.Name, &s.Email, &s.NPWP, &s.Address, &s.Status, &s.PKP, &s.Withholding, &s.TermDays, &s.Currency, &s.LeadDays, &s.Contract)
	if dbtx.IsNoRows(err) {
		return s, errs.Validation("invalid_supplier", "supplier not found", errs.Field("supplierId", "not_found", "supplier not found in this property"))
	}
	return s, err
}

// usableSupplier rejects inactive and blocked suppliers for new documents.
func usableSupplier(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (supplierInfo, error) {
	s, err := loadSupplier(ctx, q, sid)
	if err != nil {
		return s, err
	}
	if s.Status != "active" {
		return s, errs.Validation("supplier_not_active", "supplier "+s.Code+" is "+s.Status,
			errs.Field("supplierId", s.Status, "the supplier is "+s.Status))
	}
	return s, nil
}

// orderEmail is the address RFQs / POs go to: the contacts receiving
// orders, else the supplier e-mail.
func orderEmail(ctx context.Context, q dbtx.Querier, sid uuid.UUID, fallback *string) string {
	var e *string
	_ = q.QueryRow(ctx, `SELECT email FROM procurement.supplier_contacts WHERE supplier_id = $1 AND receives_orders AND coalesce(email, '') <> ''
		ORDER BY is_primary DESC, created_at LIMIT 1`, sid).Scan(&e)
	if e != nil && *e != "" {
		return *e
	}
	return deref(fallback)
}

func clubName(ctx context.Context, q dbtx.Querier) string {
	var club string
	_ = q.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&club)
	return club
}

// notifyHolders notifies the users holding a permission (in-app + e-mail).
func (m *Module) notifyHolders(ctx context.Context, tx pgx.Tx, property uuid.UUID, permission, event string, data map[string]any, link string) error {
	if m.Notify == nil {
		return nil
	}
	users, err := notify.Holders(ctx, tx, property, permission)
	if err != nil || len(users) == 0 {
		return err
	}
	return m.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "procurement", UserIDs: users, PropertyID: &property, Data: data,
		Link: link})
}

// PeriodStatusFunc returns the accounting period status of a date (K10:
// open, soft_closed or closed); accounting.PeriodStatus has this shape.
type PeriodStatusFunc func(ctx context.Context, q dbtx.Querier, property uuid.UUID, date time.Time) (string, error)

// SetPeriodGuard registers the period status source (internal/app wires
// Accounting; nil = every period is open).
func (m *Module) SetPeriodGuard(f PeriodStatusFunc) {
	if f == nil {
		m.period.Store(nil)
		return
	}
	m.period.Store(&f)
}

// openPeriod refuses a document dated in a Closed accounting period (K10).
func (m *Module) openPeriod(ctx context.Context, q dbtx.Querier, property uuid.UUID, d time.Time, field string) error {
	f := m.period.Load()
	if f == nil || *f == nil {
		return nil
	}
	st, err := (*f)(ctx, q, property, d)
	if err != nil {
		return err
	}
	if st == "closed" {
		return errs.Validation("period_closed", "the accounting period of "+d.Format("2006-01-02")+" is closed",
			errs.Field(field, "period_closed", "date in a closed accounting period"))
	}
	return nil
}

// publish writes a domain event in the business transaction.
func (m *Module) publish(ctx context.Context, tx pgx.Tx, event, aggregate string, aggID, property uuid.UUID, payload any) error {
	if m.Events == nil {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, event, aggregate, &aggID, &property, payload)
	return err
}

// submitApproval submits a document to the approval engine (auto-approved
// without a workflow; the decision hook runs in this transaction).
func (m *Module) submitApproval(ctx context.Context, tx pgx.Tx, dt provision.DocumentType, docID, property uuid.UUID, ref, title string,
	attrs map[string]any) (uuid.UUID, string, error) {
	if m.Approvals == nil {
		return uuid.Nil, "", fmt.Errorf("procurement: approval engine not wired")
	}
	return m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: dt.Code, DocumentID: docID, DocumentRef: ref, Title: title,
		PropertyID: property, Attributes: attrs})
}

// decide approves or rejects the pending approval request of a document
// (convenience actions on the document; eligibility is the engine's).
func (m *Module) decide(ctx context.Context, tx pgx.Tx, requestID *uuid.UUID, approve bool, reason string) error {
	if requestID == nil {
		return errs.Conflict("no_pending_approval", "this document has no pending approval request")
	}
	return m.Approvals.Decide(ctx, tx, *requestID, approve, reason)
}

// amountAttrs are the common approval attributes of an amount.
func amountAttrs(pol ProcurementPolicy, amount decimal.Decimal) map[string]any {
	lvl, _ := pol.ApprovalLevel(amount)
	f, _ := amount.Float64()
	return map[string]any{"amount": f, "approvalLevel": lvl}
}

// oneOf adapts handle.One to a query result: oneOf[T]("what")(tx.Query(…)).
func oneOf[T any](what string) func(pgx.Rows, error) (T, error) {
	return func(rows pgx.Rows, err error) (T, error) { return handle.One[T](rows, err, what) }
}
