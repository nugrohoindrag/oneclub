package accounting

// EP-19 Accounts Payable: AP open items from matched vendor invoices
// (procurement.vendor_invoice_approved), debit notes, consignment sales,
// service bills entered in accounting (vendor jasa, FR-AP-01) and opening
// balances; payment runs (due items → approval → execution with the payment
// journal and accounting.vendor_payment_made, FR-AP-02); AP aging per
// supplier (FR-AP-03); withholding tax on service bills (FR-AP-05); the bulk
// payment file of a run (FR-AP-06).

import (
	"context"
	"encoding/csv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

// PaymentRunDocumentType approves a payment run (FR-AP-02).
var PaymentRunDocumentType = provision.DocumentType{Code: "accounting_payment_run", Module: "accounting", Name: "Payment Run",
	Attributes: []provision.DocumentAttribute{{Key: "total", Label: "Total", Type: "number"}, {Key: "suppliers", Label: "Suppliers", Type: "number"}}}

// apItem is an AP open item to record.
type apItem struct {
	SupplierID        uuid.UUID
	SupplierName      string
	Type              string // vendor_invoice | debit_note | consignment | vendor_bill | opening
	SourceID          uuid.UUID
	Number            string
	SupplierInvoiceNo string
	InvoiceDate       time.Time
	DueDate           time.Time
	Subtotal          decimal.Decimal
	Tax               decimal.Decimal
	Withholding       decimal.Decimal
	Amount            decimal.Decimal
	TaxInvoiceNo      string
	Related           *uuid.UUID
	Journal           *uuid.UUID
	Description       string
}

// upsertAPItem records an AP open item once per source document.
func upsertAPItem(ctx context.Context, tx pgx.Tx, property uuid.UUID, it apItem) error {
	cur := currency(ctx, tx)
	_, err := tx.Exec(ctx, `INSERT INTO accounting.ap_items (id, property_id, supplier_id, supplier_name, item_type, source_id, number, supplier_invoice_no,
		invoice_date, due_date, currency, subtotal, tax_amount, withholding, amount, tax_invoice_no, related_item_id, journal_id, description)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::numeric,$13::numeric,$14::numeric,$15::numeric,$16,$17,$18,$19)
		ON CONFLICT (item_type, source_id) DO UPDATE SET journal_id = EXCLUDED.journal_id, amount = EXCLUDED.amount, subtotal = EXCLUDED.subtotal,
		tax_amount = EXCLUDED.tax_amount, withholding = EXCLUDED.withholding`,
		id.New(), property, it.SupplierID, it.SupplierName, it.Type, it.SourceID, it.Number, nz(it.SupplierInvoiceNo), dateOnly(it.InvoiceDate),
		dateOnly(it.DueDate), cur, it.Subtotal.String(), it.Tax.String(), it.Withholding.String(), it.Amount.String(), nz(it.TaxInvoiceNo), it.Related,
		it.Journal, nz(it.Description))
	return err
}

// APItem is an AP open item (Payables).
type APItem struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	SupplierID        uuid.UUID  `json:"supplierId" db:"supplier_id"`
	SupplierName      string     `json:"supplierName" db:"supplier_name"`
	ItemType          string     `json:"itemType" db:"item_type" enum:"vendor_invoice,debit_note,consignment,vendor_bill,opening"`
	SourceID          uuid.UUID  `json:"sourceId" db:"source_id"`
	Number            string     `json:"number" db:"number"`
	SupplierInvoiceNo *string    `json:"supplierInvoiceNo" db:"supplier_invoice_no"`
	InvoiceDate       string     `json:"invoiceDate" db:"invoice_date"`
	DueDate           string     `json:"dueDate" db:"due_date"`
	Currency          string     `json:"currency" db:"currency"`
	Subtotal          string     `json:"subtotal" db:"subtotal"`
	TaxAmount         string     `json:"taxAmount" db:"tax_amount"`
	Withholding       string     `json:"withholding" db:"withholding"`
	Amount            string     `json:"amount" db:"amount"`
	PaidAmount        string     `json:"paidAmount" db:"paid_amount"`
	Outstanding       string     `json:"outstanding" db:"outstanding"`
	Status            string     `json:"status" db:"status" enum:"open,partially_paid,paid,cancelled"`
	TaxInvoiceNo      *string    `json:"taxInvoiceNo" db:"tax_invoice_no"`
	RelatedItemID     *uuid.UUID `json:"relatedItemId" db:"related_item_id"`
	JournalID         *uuid.UUID `json:"journalId" db:"journal_id"`
	Description       *string    `json:"description" db:"description"`
	DaysOverdue       int        `json:"daysOverdue" db:"days_overdue"`
}

const apSelect = `SELECT i.id, i.supplier_id, i.supplier_name, i.item_type, i.source_id, i.number, i.supplier_invoice_no,
	to_char(i.invoice_date, 'YYYY-MM-DD') AS invoice_date, to_char(i.due_date, 'YYYY-MM-DD') AS due_date, i.currency, trim_scale(i.subtotal)::text AS subtotal,
	trim_scale(i.tax_amount)::text AS tax_amount, trim_scale(i.withholding)::text AS withholding, trim_scale(i.amount)::text AS amount,
	trim_scale(i.paid_amount)::text AS paid_amount, trim_scale(i.amount - i.paid_amount)::text AS outstanding, i.status, i.tax_invoice_no, i.related_item_id,
	i.journal_id, i.description, greatest(0, billing.local_date(i.property_id) - i.due_date)::int AS days_overdue FROM accounting.ap_items i`

// ListAPItems lists the payables of a property.
func ListAPItems(ctx context.Context, q dbtx.Querier, property uuid.UUID, supplier *uuid.UUID, status, dueBy string, limit int) ([]APItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var due any
	if dueBy != "" {
		t, err := parseDate("dueBy", dueBy)
		if err != nil {
			return nil, err
		}
		due = t
	}
	open := status == "unpaid"
	if open {
		status = ""
	}
	return handle.List[APItem](q.Query(ctx, apSelect+` WHERE i.property_id = $1 AND ($2::uuid IS NULL OR i.supplier_id = $2)
		AND ($3 = '' OR i.status = $3) AND (NOT $4 OR i.status IN ('open', 'partially_paid')) AND ($5::date IS NULL OR i.due_date <= $5)
		ORDER BY i.due_date, i.number LIMIT $6`, property, supplier, status, open, due, limit))
}

// ── AP aging (FR-AP-03) ───────────────────────────────────────────────────

// APAgingRow is the open payable of one supplier by days past due.
type APAgingRow struct {
	SupplierID   *uuid.UUID `json:"supplierId" db:"supplier_id"`
	SupplierName string     `json:"supplierName" db:"supplier_name"`
	Items        int        `json:"items" db:"items"`
	Current      string     `json:"current" db:"current" doc:"Not yet due"`
	Days1to30    string     `json:"days1to30" db:"d1_30"`
	Days31to60   string     `json:"days31to60" db:"d31_60"`
	Days61to90   string     `json:"days61to90" db:"d61_90"`
	Over90       string     `json:"over90" db:"d90"`
	Total        string     `json:"total" db:"total"`
}

// APAging is the AP aging as of a date.
type APAging struct {
	AsOf   string       `json:"asOf"`
	Rows   []APAgingRow `json:"rows"`
	Totals APAgingRow   `json:"totals"`
}

const apOpenAsOf = `SELECT i.supplier_id, i.supplier_name, i.due_date, i.amount - coalesce((SELECT sum((a->>'amount')::numeric)
	FROM accounting.vendor_payments p CROSS JOIN LATERAL jsonb_array_elements(p.allocations) a
	WHERE (a->>'apItemId')::uuid = i.id AND p.paid_date <= $2::date), 0) AS open_amount
	FROM accounting.ap_items i WHERE ($1::uuid IS NULL OR i.property_id = $1) AND i.invoice_date <= $2::date AND i.status <> 'cancelled'`

// APAgingAsOf ages the open payables by days past due (property nil =
// every property in scope).
func APAgingAsOf(ctx context.Context, q dbtx.Querier, property *uuid.UUID, asOf time.Time) (APAging, error) {
	day := ymd(asOf)
	var prop any
	if property != nil {
		prop = *property
	}
	rows, err := handle.List[APAgingRow](q.Query(ctx, `WITH open AS (`+apOpenAsOf+`)
		SELECT supplier_id, min(supplier_name) AS supplier_name, count(*)::int AS items,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE due_date >= $2::date), 0))::text AS current,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - due_date BETWEEN 1 AND 30), 0))::text AS d1_30,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - due_date BETWEEN 31 AND 60), 0))::text AS d31_60,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - due_date BETWEEN 61 AND 90), 0))::text AS d61_90,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - due_date > 90), 0))::text AS d90,
		  trim_scale(sum(open_amount))::text AS total
		FROM open WHERE open_amount <> 0 GROUP BY supplier_id ORDER BY min(supplier_name)`, prop, day))
	if err != nil {
		return APAging{}, err
	}
	t := APAgingRow{SupplierName: "Total"}
	sums := make([]decimal.Decimal, 6)
	for _, r := range rows {
		t.Items += r.Items
		for i, v := range []string{r.Current, r.Days1to30, r.Days31to60, r.Days61to90, r.Over90, r.Total} {
			sums[i] = sums[i].Add(dec(v))
		}
	}
	t.Current, t.Days1to30, t.Days31to60, t.Days61to90, t.Over90, t.Total = sums[0].String(), sums[1].String(), sums[2].String(), sums[3].String(),
		sums[4].String(), sums[5].String()
	return APAging{AsOf: day, Rows: rows, Totals: t}, nil
}

// ── service bills entered in accounting (FR-AP-01, FR-AP-05) ─────────────

// VendorBillLine is one expense line of a service bill.
type VendorBillLine struct {
	AccountID    uuid.UUID `json:"accountId"`
	Description  string    `json:"description"`
	Amount       string    `json:"amount"`
	CostCenter   string    `json:"costCenter,omitempty"`
	BusinessLine string    `json:"businessLine,omitempty"`
}

// VendorBillInput records a vendor bill for services (no purchase order).
type VendorBillInput struct {
	SupplierID         uuid.UUID        `json:"supplierId"`
	SupplierInvoiceNo  string           `json:"supplierInvoiceNo"`
	InvoiceDate        string           `json:"invoiceDate"`
	DueDate            string           `json:"dueDate,omitempty"`
	TaxAmount          string           `json:"taxAmount,omitempty" doc:"PPN input on the bill"`
	TaxInvoiceNo       string           `json:"taxInvoiceNo,omitempty"`
	WithholdingTaxCode string           `json:"withholdingTaxCode,omitempty" doc:"PPH23 or PPH42: withheld from the payable (FR-AP-05)"`
	Lines              []VendorBillLine `json:"lines"`
}

// CreateVendorBill posts a service bill to AP: Dr expense lines, Dr VAT
// input, Cr AP and Cr withholding tax payable.
func (m *Module) CreateVendorBill(ctx context.Context, tx pgx.Tx, property uuid.UUID, in VendorBillInput) (APItem, error) {
	if in.SupplierID == uuid.Nil {
		return APItem{}, handle.Invalid("supplierId", "required", "choose the supplier")
	}
	if err := handle.Required("supplierInvoiceNo", in.SupplierInvoiceNo); err != nil {
		return APItem{}, err
	}
	d, err := parseDate("invoiceDate", in.InvoiceDate)
	if err != nil {
		return APItem{}, err
	}
	due := d.AddDate(0, 0, 30)
	if in.DueDate != "" {
		if due, err = parseDate("dueDate", in.DueDate); err != nil {
			return APItem{}, err
		}
	}
	if len(in.Lines) == 0 {
		return APItem{}, handle.Invalid("lines", "required", "add at least one line")
	}
	name, npwp, found, err := supplierLookup(ctx, tx, property, in.SupplierID)
	if err != nil {
		return APItem{}, err
	}
	if !found {
		return APItem{}, handle.Invalid("supplierId", "not_found", "supplier not found")
	}
	tax, err := handle.Decimal("taxAmount", in.TaxAmount, decimal.Zero)
	if err != nil {
		return APItem{}, err
	}
	if tax.IsNegative() {
		return APItem{}, handle.Invalid("taxAmount", "invalid", "zero or more")
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return APItem{}, err
	}
	r := &resolver{tx: tx, ctx: ctx, property: property, cfg: cfg, byCode: map[string]acctInfo{}, byID: map[uuid.UUID]acctInfo{}, held: map[int]bool{}}
	clr, err := r.role("posting_clearing")
	if err != nil {
		return APItem{}, err
	}
	if clr.Err != "" {
		return APItem{}, errs.Conflict("missing_account", clr.Err)
	}
	src := id.New()
	sup := in.SupplierID
	dims := Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: name}
	subtotal := decimal.Zero
	var items []Item
	for i, l := range in.Lines {
		amt, err := handle.Decimal("lines.amount", l.Amount, decimal.Zero)
		if err != nil {
			return APItem{}, err
		}
		if !amt.IsPositive() {
			return APItem{}, handle.Invalid("lines", "invalid", "line amounts must be positive")
		}
		a, err := r.accountByID(l.AccountID)
		if err != nil {
			return APItem{}, err
		}
		if a.Err != "" {
			return APItem{}, handle.Invalid("lines", "invalid_account", a.Err)
		}
		_ = i
		subtotal = subtotal.Add(amt)
		acct := l.AccountID
		ld := dims
		ld.CostCenter, ld.BusinessLine = l.CostCenter, l.BusinessLine
		items = append(items, Item{Source: "accounting.vendor_bill_line", Amount: amt, Description: l.Description, Dims: ld, DimSide: "debit",
			DebitAccount: &acct, CreditAccount: &clr.ID, SourceType: "accounting.vendor_bill", SourceID: src.String()})
	}
	wht := decimal.Zero
	if code := strings.ToUpper(strings.TrimSpace(in.WithholdingTaxCode)); code != "" {
		var rate string
		if err := tx.QueryRow(ctx, `SELECT rate_percent::text FROM accounting.tax_codes WHERE code = $1 AND kind = 'withholding' AND status = 'active'`, code).
			Scan(&rate); err != nil {
			if dbtx.IsNoRows(err) {
				return APItem{}, handle.Invalid("withholdingTaxCode", "not_found", "unknown withholding tax code")
			}
			return APItem{}, err
		}
		wht = subtotal.Mul(dec(rate)).Div(decimal.NewFromInt(100)).Round(places(currency(ctx, tx)))
	}
	total := subtotal.Add(tax)
	payable := total.Sub(wht)
	if tax.IsPositive() {
		items = append(items, Item{Source: "procurement.vendor_invoice_tax", Attrs: map[string]string{"origin": "vendor_bill"}, Amount: tax,
			Description: "PPN " + in.SupplierInvoiceNo, SourceType: "accounting.vendor_bill", SourceID: src.String(), FallbackDebit: "ppn_input",
			FallbackCredit: "posting_clearing"})
	}
	if wht.IsPositive() {
		items = append(items, Item{Source: "procurement.vendor_invoice_withholding", Attrs: map[string]string{"origin": "vendor_bill"}, Amount: wht,
			Description: "PPh " + in.SupplierInvoiceNo, Dims: dims, DimSide: "credit", SourceType: "accounting.vendor_bill", SourceID: src.String(),
			FallbackDebit: "posting_clearing", FallbackCredit: "pph_payable"})
	}
	items = append(items, Item{Source: "procurement.vendor_invoice_payable", Attrs: map[string]string{"origin": "vendor_bill"}, Amount: payable,
		Description: "AP " + in.SupplierInvoiceNo, Dims: dims, DimSide: "credit", SourceType: "accounting.vendor_bill", SourceID: src.String(),
		FallbackDebit: "posting_clearing", FallbackCredit: "ap_control"})
	num, err := numbering.Next(ctx, tx, property, "VB", d)
	if err != nil {
		return APItem{}, err
	}
	if err := lockProperty(ctx, tx, property); err != nil {
		return APItem{}, err
	}
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, Type: "automatic", SourceType: "accounting.vendor_bill",
		SourceID: src.String(), SourceRef: num, Description: "Vendor bill " + num + " (" + in.SupplierInvoiceNo + ") · " + name},
		Sources: []sourceMark{{Type: "accounting.vendor_bill", ID: src, BusinessDate: &d, Key1: in.SupplierInvoiceNo, Amount: payable, Items: items}}})
	if err != nil {
		return APItem{}, err
	}
	if len(missing) > 0 {
		return APItem{}, errs.Conflict("missing_rule", "the bill cannot be posted: "+missing[0].Detail)
	}
	if err := upsertAPItem(ctx, tx, property, apItem{SupplierID: sup, SupplierName: name, Type: "vendor_bill", SourceID: src, Number: num,
		SupplierInvoiceNo: in.SupplierInvoiceNo, InvoiceDate: d, DueDate: due, Subtotal: subtotal, Tax: tax, Withholding: wht, Amount: payable,
		TaxInvoiceNo: in.TaxInvoiceNo, Journal: journalID(j), Description: in.Lines[0].Description}); err != nil {
		return APItem{}, err
	}
	if tax.IsPositive() {
		if err := m.createInputTaxInvoice(ctx, tx, property, "accounting.vendor_bill", src, num, sup, name, npwp, d, subtotal, tax, in.TaxInvoiceNo); err != nil {
			return APItem{}, err
		}
	}
	it, err := getOne[APItem]("payable")(tx.Query(ctx, apSelect+` WHERE i.item_type = 'vendor_bill' AND i.source_id = $1`, src))
	if err != nil {
		return it, err
	}
	return it, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.vendor_bill", EntityID: it.ID.String(),
		EntityLabel: num + " · " + name, PropertyID: &property, After: it})
}

// supplierLookup returns the name and NPWP of a supplier of the property.
func supplierLookup(ctx context.Context, q dbtx.Querier, property, sid uuid.UUID) (string, string, bool, error) {
	var name string
	var npwp *string
	err := q.QueryRow(ctx, `SELECT name, npwp FROM reporting.acc_suppliers WHERE id = $1 AND property_id = $2`, sid, property).Scan(&name, &npwp)
	if dbtx.IsNoRows(err) {
		return "", "", false, nil
	}
	return name, deref(npwp), err == nil, err
}

// ── payment runs (FR-AP-02) ───────────────────────────────────────────────

// APPaymentRunLine is one payable paid by a run.
type APPaymentRunLine struct {
	ID           uuid.UUID `json:"id" db:"id"`
	APItemID     uuid.UUID `json:"apItemId" db:"ap_item_id"`
	SupplierID   uuid.UUID `json:"supplierId" db:"supplier_id"`
	SupplierName string    `json:"supplierName" db:"supplier_name"`
	Number       string    `json:"number" db:"number"`
	ItemType     string    `json:"itemType" db:"item_type"`
	DueDate      string    `json:"dueDate" db:"due_date"`
	Amount       string    `json:"amount" db:"amount"`
}

// APPaymentRun is a payment run with its lines.
type APPaymentRun struct {
	ID                uuid.UUID          `json:"id" db:"id"`
	Number            string             `json:"number" db:"number"`
	PaymentDate       string             `json:"paymentDate" db:"payment_date"`
	BankAccountID     uuid.UUID          `json:"bankAccountId" db:"bank_account_id"`
	BankAccountName   string             `json:"bankAccountName" db:"bank_account_name"`
	Method            string             `json:"method" db:"method" enum:"transfer,cheque,cash"`
	Currency          string             `json:"currency" db:"currency"`
	Total             string             `json:"total" db:"total"`
	Status            string             `json:"status" db:"status" enum:"draft,pending_approval,approved,executed,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID         `json:"approvalRequestId" db:"approval_request_id"`
	JournalID         *uuid.UUID         `json:"journalId" db:"journal_id"`
	Notes             *string            `json:"notes" db:"notes"`
	ExecutedAt        *time.Time         `json:"executedAt" db:"executed_at"`
	CreatedAt         time.Time          `json:"createdAt" db:"created_at"`
	Lines             []APPaymentRunLine `json:"lines" db:"-"`
}

const runSelect = `SELECT r.id, r.number, to_char(r.payment_date, 'YYYY-MM-DD') AS payment_date, r.bank_account_id, b.name AS bank_account_name, r.method,
	r.currency, trim_scale(r.total)::text AS total, r.status, r.approval_request_id, r.journal_id, r.notes, r.executed_at, r.created_at
	FROM accounting.payment_runs r JOIN accounting.bank_accounts b ON b.id = r.bank_account_id`

// GetPaymentRun loads a run with its lines.
func GetPaymentRun(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (APPaymentRun, error) {
	r, err := getOne[APPaymentRun]("payment run")(q.Query(ctx, runSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return r, err
	}
	r.Lines, err = handle.List[APPaymentRunLine](q.Query(ctx, `SELECT l.id, l.ap_item_id, l.supplier_id, i.supplier_name, i.number, i.item_type,
		to_char(i.due_date, 'YYYY-MM-DD') AS due_date, trim_scale(l.amount)::text AS amount FROM accounting.payment_run_lines l
		JOIN accounting.ap_items i ON i.id = l.ap_item_id WHERE l.run_id = $1 ORDER BY i.supplier_name, i.due_date, i.number`, rid))
	return r, err
}

// ListPaymentRuns lists the payment runs of a property.
func ListPaymentRuns(ctx context.Context, q dbtx.Querier, property uuid.UUID, status string) ([]APPaymentRun, error) {
	return handle.List[APPaymentRun](q.Query(ctx, runSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2) ORDER BY r.created_at DESC LIMIT 200`,
		property, status))
}

// APPaymentRunItem selects one payable for a run.
type APPaymentRunItem struct {
	APItemID uuid.UUID `json:"apItemId"`
	Amount   string    `json:"amount,omitempty" doc:"Default: the outstanding amount (partial payment when lower)"`
}

// APPaymentRunInput creates a payment run: chosen payables, or every open
// payable due by a date (optionally of one supplier).
type APPaymentRunInput struct {
	PaymentDate   string             `json:"paymentDate"`
	BankAccountID uuid.UUID          `json:"bankAccountId"`
	Method        string             `json:"method,omitempty" enum:"transfer,cheque,cash"`
	Items         []APPaymentRunItem `json:"items,omitempty"`
	DueBy         string             `json:"dueBy,omitempty"`
	SupplierID    *uuid.UUID         `json:"supplierId,omitempty"`
	Notes         string             `json:"notes,omitempty"`
}

// CreatePaymentRun drafts a payment run.
func (m *Module) CreatePaymentRun(ctx context.Context, tx pgx.Tx, property uuid.UUID, in APPaymentRunInput) (APPaymentRun, error) {
	d, err := parseDate("paymentDate", in.PaymentDate)
	if err != nil {
		return APPaymentRun{}, err
	}
	method := in.Method
	if method == "" {
		method = "transfer"
	}
	if method != "transfer" && method != "cheque" && method != "cash" {
		return APPaymentRun{}, handle.Invalid("method", "invalid", "transfer, cheque or cash")
	}
	if _, err := bankAccountOf(ctx, tx, property, in.BankAccountID); err != nil {
		return APPaymentRun{}, err
	}
	items := in.Items
	if len(items) == 0 {
		if in.DueBy == "" {
			return APPaymentRun{}, handle.Invalid("items", "required", "choose the payables or a due date")
		}
		open, err := ListAPItems(ctx, tx, property, in.SupplierID, "unpaid", in.DueBy, 1000)
		if err != nil {
			return APPaymentRun{}, err
		}
		for _, it := range open {
			items = append(items, APPaymentRunItem{APItemID: it.ID})
		}
		// debit notes of the same suppliers offset the run
		sups := map[uuid.UUID]bool{}
		for _, it := range open {
			sups[it.SupplierID] = true
		}
		dns, err := ListAPItems(ctx, tx, property, in.SupplierID, "unpaid", "", 1000)
		if err != nil {
			return APPaymentRun{}, err
		}
		for _, it := range dns {
			if it.ItemType == "debit_note" && sups[it.SupplierID] && !dec(it.Outstanding).IsZero() {
				dup := false
				for _, x := range items {
					dup = dup || x.APItemID == it.ID
				}
				if !dup {
					items = append(items, APPaymentRunItem{APItemID: it.ID})
				}
			}
		}
		if len(items) == 0 {
			return APPaymentRun{}, errs.Conflict("nothing_due", "no open payable is due by "+in.DueBy)
		}
	}
	rid := id.New()
	num, err := numbering.Next(ctx, tx, property, "PRUN", d)
	if err != nil {
		return APPaymentRun{}, err
	}
	cur := currency(ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.payment_runs (id, property_id, number, payment_date, bank_account_id, method, currency, notes, created_by,
		updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, rid, property, num, d, in.BankAccountID, method, cur, nz(in.Notes), actorPtr(ctx)); err != nil {
		return APPaymentRun{}, err
	}
	total := decimal.Zero
	perSupplier := map[uuid.UUID]decimal.Decimal{}
	seen := map[uuid.UUID]bool{}
	for _, x := range items {
		if seen[x.APItemID] {
			return APPaymentRun{}, handle.Invalid("items", "duplicate", "a payable is listed twice")
		}
		seen[x.APItemID] = true
		it, err := getOne[APItem]("payable")(tx.Query(ctx, apSelect+` WHERE i.id = $1 AND i.property_id = $2 FOR UPDATE OF i`, x.APItemID, property))
		if err != nil {
			return APPaymentRun{}, err
		}
		if it.Status != "open" && it.Status != "partially_paid" {
			return APPaymentRun{}, errs.Conflict("not_open", "payable "+it.Number+" is "+it.Status)
		}
		var inRun bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.payment_run_lines l JOIN accounting.payment_runs r ON r.id = l.run_id
			WHERE l.ap_item_id = $1 AND r.status IN ('draft', 'pending_approval', 'approved'))`, it.ID).Scan(&inRun); err != nil {
			return APPaymentRun{}, err
		}
		if inRun {
			return APPaymentRun{}, errs.Conflict("in_run", "payable "+it.Number+" is already in an open payment run")
		}
		out := dec(it.Outstanding)
		amt, err := handle.Decimal("items.amount", x.Amount, out)
		if err != nil {
			return APPaymentRun{}, err
		}
		if amt.IsZero() || (out.IsPositive() && (amt.IsNegative() || amt.GreaterThan(out))) || (out.IsNegative() && (amt.IsPositive() || amt.LessThan(out))) {
			return APPaymentRun{}, handle.Invalid("items.amount", "invalid", "between 0 and the outstanding amount of "+it.Number)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.payment_run_lines (id, property_id, run_id, ap_item_id, supplier_id, amount) VALUES ($1,$2,$3,$4,$5,$6::numeric)`,
			id.New(), property, rid, it.ID, it.SupplierID, amt.String()); err != nil {
			return APPaymentRun{}, err
		}
		total = total.Add(amt)
		perSupplier[it.SupplierID] = perSupplier[it.SupplierID].Add(amt)
	}
	for _, v := range perSupplier {
		if !v.IsPositive() {
			return APPaymentRun{}, errs.Conflict("negative_payment", "the debit notes of a supplier exceed its payables in this run")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET total = $2::numeric WHERE id = $1`, rid, total.String()); err != nil {
		return APPaymentRun{}, err
	}
	r, err := GetPaymentRun(ctx, tx, rid)
	if err != nil {
		return r, err
	}
	return r, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.payment_run", EntityID: rid.String(),
		EntityLabel: num, PropertyID: &property, After: r})
}

func lockRun(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (APPaymentRun, error) {
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM accounting.payment_runs WHERE id = $1 AND property_id = $2 FOR UPDATE`, rid, property).Scan(&st); err != nil {
		if dbtx.IsNoRows(err) {
			return APPaymentRun{}, errs.NotFound("payment run")
		}
		return APPaymentRun{}, err
	}
	return GetPaymentRun(ctx, tx, rid)
}

// SubmitPaymentRun sends a draft run for approval (approved at once
// without a workflow).
func (m *Module) SubmitPaymentRun(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (APPaymentRun, error) {
	r, err := lockRun(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "draft" && r.Status != "rejected" {
		return r, errs.Conflict("not_draft", "only a draft run can be submitted")
	}
	sups := map[uuid.UUID]bool{}
	for _, l := range r.Lines {
		sups[l.SupplierID] = true
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET status = 'pending_approval', updated_by = $2 WHERE id = $1`, rid, actorPtr(ctx)); err != nil {
		return r, err
	}
	if m.Approvals == nil {
		return r, errs.Conflict("no_approval", "approvals are not available")
	}
	req, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: PaymentRunDocumentType.Code, DocumentID: rid, DocumentRef: r.Number,
		Title: "Payment run " + r.Number + " · " + r.Currency + " " + r.Total, PropertyID: property,
		Attributes: map[string]any{"total": dec(r.Total).InexactFloat64(), "suppliers": len(sups)}})
	if err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET approval_request_id = $2 WHERE id = $1`, rid, req); err != nil {
		return r, err
	}
	after, err := GetPaymentRun(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "submit", EntityType: "accounting.payment_run", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, Before: map[string]any{"status": r.Status}, After: map[string]any{"status": after.Status}})
}

// PaymentRunDecision applies the approval decision of a run.
func (m *Module) PaymentRunDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "draft"}[d.Status]
	if st == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET status = $2 WHERE id = $1 AND status = 'pending_approval'`, d.DocumentID, st)
	return err
}

// CancelPaymentRun cancels a run that is not executed.
func (m *Module) CancelPaymentRun(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in ExceptionResolveInput) (APPaymentRun, error) {
	r, err := lockRun(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	if r.Status == "executed" || r.Status == "cancelled" {
		return r, errs.Conflict("not_cancellable", "the run is "+r.Status)
	}
	if r.Status == "pending_approval" && r.ApprovalRequestID != nil && m.Approvals != nil {
		if err := m.Approvals.Cancel(ctx, tx, *r.ApprovalRequestID, "payment run cancelled"); err != nil {
			return r, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET status = 'cancelled', updated_by = $2 WHERE id = $1`, rid, actorPtr(ctx)); err != nil {
		return r, err
	}
	after, err := GetPaymentRun(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "cancel", EntityType: "accounting.payment_run", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": r.Status}, After: map[string]any{"status": "cancelled"}})
}

// ExecutePaymentRun pays an approved run: one vendor payment per
// supplier, the payment journal (Dr AP / Cr bank), the AP items settled and
// accounting.vendor_payment_made for procurement.
func (m *Module) ExecutePaymentRun(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (APPaymentRun, error) {
	r, err := lockRun(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	if r.Status != "approved" {
		return r, errs.Conflict("not_approved", "only an approved run can be executed")
	}
	bank, err := bankAccountOf(ctx, tx, property, r.BankAccountID)
	if err != nil {
		return r, err
	}
	d, _ := time.Parse("2006-01-02", r.PaymentDate)
	if err := lockProperty(ctx, tx, property); err != nil {
		return r, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return r, err
	}
	res := &resolver{tx: tx, ctx: ctx, property: property, cfg: cfg, byCode: map[string]acctInfo{}, byID: map[uuid.UUID]acctInfo{}, held: map[int]bool{}}
	clr, err := res.role("posting_clearing")
	if err != nil {
		return r, err
	}
	if clr.Err != "" {
		return r, errs.Conflict("missing_account", clr.Err)
	}
	type pay struct {
		id     uuid.UUID
		number string
		name   string
		amount decimal.Decimal
		allocs []map[string]any
	}
	bySupplier := map[uuid.UUID]*pay{}
	var order []uuid.UUID
	for _, l := range r.Lines {
		var outstanding, status string
		var itemType string
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT (amount - paid_amount)::text, status, item_type, source_id FROM accounting.ap_items WHERE id = $1 FOR UPDATE`,
			l.APItemID).Scan(&outstanding, &status, &itemType, &source); err != nil {
			return r, err
		}
		amt := dec(l.Amount)
		out := dec(outstanding)
		if status == "paid" || status == "cancelled" || (out.IsPositive() && amt.GreaterThan(out)) || (out.IsNegative() && amt.LessThan(out)) {
			return r, errs.Conflict("payable_changed", "payable "+l.Number+" changed since the run was approved; cancel and recreate the run")
		}
		p := bySupplier[l.SupplierID]
		if p == nil {
			p = &pay{id: id.New(), name: l.SupplierName}
			bySupplier[l.SupplierID] = p
			order = append(order, l.SupplierID)
		}
		p.amount = p.amount.Add(amt)
		a := map[string]any{"apItemId": l.APItemID, "itemType": itemType, "number": l.Number, "amount": amt.String()}
		if itemType == "vendor_invoice" {
			a["vendorInvoiceId"] = source
		}
		p.allocs = append(p.allocs, a)
		paid := "partially_paid"
		if amt.Equal(out) {
			paid = "paid"
		}
		if _, err := tx.Exec(ctx, `UPDATE accounting.ap_items SET paid_amount = paid_amount + $2::numeric, status = $3 WHERE id = $1`, l.APItemID,
			amt.String(), paid); err != nil {
			return r, err
		}
	}
	var items []Item
	for _, sid := range order {
		p := bySupplier[sid]
		if p.number, err = numbering.Next(ctx, tx, property, "VPY", d); err != nil {
			return r, err
		}
		sup := sid
		dims := Dims{PartnerType: "supplier", PartnerID: &sup, PartnerName: p.name}
		desc := "Payment " + p.number + " · " + p.name
		items = append(items,
			Item{Source: "accounting.vendor_payment", Attrs: map[string]string{"method": r.Method}, Amount: p.amount, Description: desc, Dims: dims,
				SourceType: "accounting.vendor_payment", SourceID: p.id.String(), FallbackDebit: "ap_control", FallbackCredit: "posting_clearing"},
			Item{Source: "accounting.bank_payment", Amount: p.amount, Description: desc, Dims: dims, DebitAccount: &clr.ID, CreditAccount: &bank.GLAccountID,
				SourceType: "accounting.vendor_payment", SourceID: p.id.String()})
	}
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, Type: "automatic", SourceType: "accounting.payment_run",
		SourceID: rid.String(), SourceRef: r.Number, Description: "Payment run " + r.Number + " · " + bank.Name},
		Sources: []sourceMark{{Type: "accounting.payment_run", ID: rid, BusinessDate: &d, Key1: r.Method, Amount: dec(r.Total), Items: items}}})
	if err != nil {
		return r, err
	}
	if len(missing) > 0 || j == nil {
		return r, errs.Conflict("missing_rule", "the payment cannot be posted; check the posting rules of accounting.vendor_payment")
	}
	for _, sid := range order {
		p := bySupplier[sid]
		raw := p.allocs
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.vendor_payments (id, property_id, number, run_id, supplier_id, supplier_name, paid_date, currency, amount,
			method, bank_account_id, allocations, journal_id, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13,$14)`,
			p.id, property, p.number, rid, sid, p.name, d, r.Currency, p.amount.String(), r.Method, r.BankAccountID, raw, j.ID, actorPtr(ctx)); err != nil {
			return r, err
		}
		var evAllocs []map[string]any
		for _, a := range p.allocs {
			if a["vendorInvoiceId"] != nil {
				evAllocs = append(evAllocs, map[string]any{"vendorInvoiceId": a["vendorInvoiceId"], "amount": a["amount"]})
			}
		}
		if evAllocs == nil {
			evAllocs = []map[string]any{}
		}
		if m.Events != nil {
			pid := p.id
			if _, err := m.Events.Publish(ctx, tx, EventVendorPaymentMade, "accounting.vendor_payment", &pid, &property, map[string]any{"paymentId": p.id,
				"number": p.number, "supplierId": sid, "paidDate": ymd(d), "currency": r.Currency, "amount": p.amount.String(), "method": r.Method,
				"allocations": evAllocs}); err != nil {
				return r, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.payment_runs SET status = 'executed', journal_id = $2, executed_at = now(), executed_by = $3, updated_by = $3
		WHERE id = $1`, rid, j.ID, actorPtr(ctx)); err != nil {
		return r, err
	}
	if m.Events != nil {
		if _, err := m.Events.Publish(ctx, tx, EventPaymentRunExecuted, "accounting.payment_run", &rid, &property, map[string]any{"runId": rid,
			"number": r.Number, "paymentDate": r.PaymentDate, "total": r.Total, "currency": r.Currency, "payments": len(order), "journalId": j.ID}); err != nil {
			return r, err
		}
	}
	after, err := GetPaymentRun(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "execute", EntityType: "accounting.payment_run", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, Before: map[string]any{"status": r.Status},
		After: map[string]any{"status": after.Status, "journalId": j.ID, "journalNumber": j.Number, "payments": len(order)}})
}

// APVendorPayment is a payment to a supplier.
type APVendorPayment struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	RunID        *uuid.UUID `json:"runId" db:"run_id"`
	SupplierID   uuid.UUID  `json:"supplierId" db:"supplier_id"`
	SupplierName string     `json:"supplierName" db:"supplier_name"`
	PaidDate     string     `json:"paidDate" db:"paid_date"`
	Currency     string     `json:"currency" db:"currency"`
	Amount       string     `json:"amount" db:"amount"`
	Method       string     `json:"method" db:"method"`
	JournalID    *uuid.UUID `json:"journalId" db:"journal_id"`
	Allocations  []byte     `json:"-" db:"allocations"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

// ListVendorPayments lists the vendor payments of a property.
func ListVendorPayments(ctx context.Context, q dbtx.Querier, property uuid.UUID, supplier *uuid.UUID) ([]APVendorPayment, error) {
	return handle.List[APVendorPayment](q.Query(ctx, `SELECT id, number, run_id, supplier_id, supplier_name, to_char(paid_date, 'YYYY-MM-DD') AS paid_date, currency,
		trim_scale(amount)::text AS amount, method, journal_id, allocations, created_at FROM accounting.vendor_payments WHERE property_id = $1
		AND ($2::uuid IS NULL OR supplier_id = $2) ORDER BY paid_date DESC, number DESC LIMIT 500`, property, supplier))
}

// PaymentRunFile is the bulk transfer file of a run (FR-AP-06, generic
// CSV accepted by the club's bank templates).
func PaymentRunFile(ctx context.Context, q dbtx.Querier, property, rid uuid.UUID) (string, error) {
	r, err := GetPaymentRun(ctx, q, rid)
	if err != nil {
		return "", err
	}
	var owner uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM accounting.payment_runs WHERE id = $1`, rid).Scan(&owner); err != nil || owner != property {
		return "", errs.NotFound("payment run")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"payment_date", "supplier_code", "supplier_name", "bank_name", "account_no", "account_name", "amount", "currency", "reference"})
	per := map[uuid.UUID]decimal.Decimal{}
	var order []uuid.UUID
	names := map[uuid.UUID]string{}
	for _, l := range r.Lines {
		if _, ok := per[l.SupplierID]; !ok {
			order = append(order, l.SupplierID)
		}
		per[l.SupplierID] = per[l.SupplierID].Add(dec(l.Amount))
		names[l.SupplierID] = l.SupplierName
	}
	for _, sid := range order {
		var code, bankName, acct, acctName string
		_ = q.QueryRow(ctx, `SELECT code, coalesce(attributes->>'bankName', ''), coalesce(attributes->>'bankAccountNo', ''),
			coalesce(attributes->>'bankAccountName', name) FROM reporting.acc_suppliers WHERE id = $1`, sid).Scan(&code, &bankName, &acct, &acctName)
		_ = w.Write([]string{r.PaymentDate, code, names[sid], bankName, acct, acctName, per[sid].String(), r.Currency, r.Number})
	}
	w.Flush()
	return b.String(), w.Error()
}
