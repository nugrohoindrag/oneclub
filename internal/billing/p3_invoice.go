package billing

// PRD P3 EP-17 FR-BIL-P3-04..06, 08, 10: invoices, payment allocations,
// credit notes, write-offs, operational ageing and the corporate statement.
//
// Three invoice sources share one document (PRD P3 §6 #6: one invoice
// system; P4 builds the AR ledger from it, contract K2):
//   - folio invoice (customer folio or one folio): the uninvoiced charges
//     less what was already received; at issue held deposits are applied and
//     the rest is transferred to the payer's AR account (city ledger:
//     corporate account for companies, customer account for individuals);
//   - account invoice: uninvoiced charges already on an AR account
//     (corporate city ledger or member signing bill);
//   - schedule invoice: one line of a payment schedule (DP, installment),
//     paid as a deposit on the schedule's folio.
// Payments settle invoices through allocations; numbers are gap-free per
// property and year and assigned at issue.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/provision"
)

// Invoice events (contract K2 with P4 Accounts Receivable).
const (
	EventInvoiceIssued  = "billing.invoice_issued"
	EventInvoicePaid    = "billing.invoice_paid"
	EventInvoiceOverdue = "billing.invoice_overdue"
	EventInvoiceVoided  = "billing.invoice_voided"
	EventCreditNote     = "billing.credit_note_issued"
	EventWrittenOff     = "billing.invoice_written_off"
)

// WriteOffDocumentType approves an invoice write-off (FR-BIL-P3-08).
var WriteOffDocumentType = provision.DocumentType{Code: "invoice_write_off", Module: "billing", Name: "Invoice Write-off",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}

// Invoice is the invoice header.
type Invoice struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	Number             *string    `json:"number" db:"number" doc:"Assigned at issue (gap-free per property and year)"`
	Kind               string     `json:"kind" db:"kind" enum:"standard,deposit,final"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	AccountID          *uuid.UUID `json:"accountId" db:"account_id" doc:"AR account (city ledger) the invoice is receivable on"`
	CustomerFolioID    *uuid.UUID `json:"customerFolioId" db:"customer_folio_id"`
	FolioID            *uuid.UUID `json:"folioId" db:"folio_id"`
	ScheduleLineID     *uuid.UUID `json:"scheduleLineId" db:"schedule_line_id"`
	BillToName         string     `json:"billToName" db:"bill_to_name"`
	BillToAddress      *string    `json:"billToAddress" db:"bill_to_address"`
	BillToNPWP         *string    `json:"billToNpwp" db:"bill_to_npwp"`
	BillToEmail        *string    `json:"billToEmail" db:"bill_to_email"`
	BillToPhone        *string    `json:"billToPhone" db:"bill_to_phone"`
	IssueDate          *string    `json:"issueDate" db:"issue_date"`
	DueDate            *string    `json:"dueDate" db:"due_date"`
	TermsDays          int        `json:"termsDays" db:"terms_days"`
	Currency           string     `json:"currency" db:"currency"`
	Subtotal           string     `json:"subtotal" db:"subtotal"`
	ServiceAmount      string     `json:"serviceAmount" db:"service_amount"`
	TaxAmount          string     `json:"taxAmount" db:"tax_amount"`
	Total              string     `json:"total" db:"total"`
	PaidAmount         string     `json:"paidAmount" db:"paid_amount"`
	CreditedAmount     string     `json:"creditedAmount" db:"credited_amount"`
	WrittenOffAmount   string     `json:"writtenOffAmount" db:"written_off_amount"`
	Outstanding        string     `json:"outstanding" db:"outstanding"`
	Status             string     `json:"status" db:"status" enum:"draft,issued,partially_paid,paid,overdue,void"`
	DaysOverdue        int        `json:"daysOverdue" db:"days_overdue"`
	Notes              *string    `json:"notes" db:"notes"`
	IssuedAt           *time.Time `json:"issuedAt" db:"issued_at"`
	SentAt             *time.Time `json:"sentAt" db:"sent_at"`
	PaidAt             *time.Time `json:"paidAt" db:"paid_at"`
	VoidedAt           *time.Time `json:"voidedAt" db:"voided_at"`
	VoidReason         *string    `json:"voidReason" db:"void_reason"`
	Version            int        `json:"version" db:"version"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
}

// InvoiceLine is one invoice line.
type InvoiceLine struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	Seq              int        `json:"seq" db:"seq"`
	FolioLineID      *uuid.UUID `json:"folioLineId" db:"folio_line_id"`
	AccountEntryID   *uuid.UUID `json:"accountEntryId" db:"account_entry_id"`
	Description      string     `json:"description" db:"description"`
	Quantity         string     `json:"quantity" db:"quantity"`
	UnitPrice        string     `json:"unitPrice" db:"unit_price"`
	NetAmount        string     `json:"netAmount" db:"net_amount"`
	ServiceAmount    string     `json:"serviceAmount" db:"service_amount"`
	TaxAmount        string     `json:"taxAmount" db:"tax_amount"`
	Total            string     `json:"total" db:"total"`
	BusinessLine     *string    `json:"businessLine" db:"business_line"`
	RevenueComponent *string    `json:"revenueComponent" db:"revenue_component"`
}

// Allocation applies a payment to an invoice or a schedule line.
type Allocation struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	PaymentID      uuid.UUID  `json:"paymentId" db:"payment_id"`
	PaymentNumber  string     `json:"paymentNumber" db:"payment_number"`
	InvoiceID      *uuid.UUID `json:"invoiceId" db:"invoice_id"`
	ScheduleLineID *uuid.UUID `json:"scheduleLineId" db:"schedule_line_id"`
	Amount         string     `json:"amount" db:"amount"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

// CreditNote reduces an invoice.
type CreditNote struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Number    string    `json:"number" db:"number"`
	InvoiceID uuid.UUID `json:"invoiceId" db:"invoice_id"`
	Amount    string    `json:"amount" db:"amount"`
	Currency  string    `json:"currency" db:"currency"`
	Reason    string    `json:"reason" db:"reason"`
	Status    string    `json:"status" db:"status" enum:"issued"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// WriteOff is a write-off request.
type WriteOff struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	InvoiceID         uuid.UUID  `json:"invoiceId" db:"invoice_id"`
	Amount            string     `json:"amount" db:"amount"`
	Reason            string     `json:"reason" db:"reason"`
	Status            string     `json:"status" db:"status" enum:"pending,approved,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

// InvoiceDetail is an invoice with lines, allocations, credits and write-offs.
type InvoiceDetail struct {
	Invoice
	Lines       []InvoiceLine `json:"lines"`
	Allocations []Allocation  `json:"allocations"`
	CreditNotes []CreditNote  `json:"creditNotes"`
	WriteOffs   []WriteOff    `json:"writeOffs"`
	PayLink     *string       `json:"payLink" doc:"Public payment link (issued invoices)"`
}

const invoiceSelect = `SELECT i.id, i.number, i.kind, i.customer_id, i.corporate_account_id, i.account_id, i.customer_folio_id, i.folio_id,
	i.schedule_line_id, i.bill_to_name, i.bill_to_address, i.bill_to_npwp, i.bill_to_email, i.bill_to_phone,
	to_char(i.issue_date, 'YYYY-MM-DD') AS issue_date, to_char(i.due_date, 'YYYY-MM-DD') AS due_date, i.terms_days, i.currency,
	trim_scale(i.subtotal)::text AS subtotal, trim_scale(i.service_amount)::text AS service_amount, trim_scale(i.tax_amount)::text AS tax_amount,
	trim_scale(i.total)::text AS total, trim_scale(i.paid_amount)::text AS paid_amount, trim_scale(i.credited_amount)::text AS credited_amount,
	trim_scale(i.written_off_amount)::text AS written_off_amount,
	trim_scale(CASE WHEN i.status IN ('draft', 'void') THEN 0 ELSE i.total - i.paid_amount - i.credited_amount - i.written_off_amount END)::text AS outstanding,
	i.status, CASE WHEN i.status IN ('issued', 'partially_paid', 'overdue') AND i.due_date < billing.local_date(i.property_id)
	  THEN billing.local_date(i.property_id) - i.due_date ELSE 0 END AS days_overdue,
	i.notes, i.issued_at, i.sent_at, i.paid_at, i.voided_at, i.void_reason, i.version, i.created_at
	FROM billing.invoices i`

func listInvoices(ctx context.Context, q dbtx.Querier, where string, args ...any) ([]Invoice, error) {
	return handle.List[Invoice](q.Query(ctx, invoiceSelect+` WHERE `+where+` ORDER BY i.created_at`, args...))
}

// GetInvoice loads an invoice with its lines and settlement.
func GetInvoice(ctx context.Context, q dbtx.Querier, iid uuid.UUID, publicBase string) (InvoiceDetail, error) {
	inv, err := oneOf[Invoice]("invoice")(q.Query(ctx, invoiceSelect+` WHERE i.id = $1`, iid))
	if err != nil {
		return InvoiceDetail{}, err
	}
	d := InvoiceDetail{Invoice: inv}
	if d.Lines, err = handle.List[InvoiceLine](q.Query(ctx, `SELECT id, seq, folio_line_id, account_entry_id, description, trim_scale(quantity)::text AS quantity,
		trim_scale(unit_price)::text AS unit_price, trim_scale(net_amount)::text AS net_amount, trim_scale(service_amount)::text AS service_amount,
		trim_scale(tax_amount)::text AS tax_amount, trim_scale(total)::text AS total, business_line, revenue_component
		FROM billing.invoice_lines WHERE invoice_id = $1 ORDER BY seq`, iid)); err != nil {
		return d, err
	}
	if d.Allocations, err = handle.List[Allocation](q.Query(ctx, `SELECT a.id, a.payment_id, p.number AS payment_number, a.invoice_id, a.schedule_line_id,
		trim_scale(a.amount)::text AS amount, a.created_at FROM billing.payment_allocations a JOIN billing.payments p ON p.id = a.payment_id
		WHERE a.invoice_id = $1 ORDER BY a.created_at`, iid)); err != nil {
		return d, err
	}
	if d.CreditNotes, err = handle.List[CreditNote](q.Query(ctx, `SELECT id, number, invoice_id, trim_scale(amount)::text AS amount, currency, reason, status,
		created_at FROM billing.credit_notes WHERE invoice_id = $1 ORDER BY created_at`, iid)); err != nil {
		return d, err
	}
	if d.WriteOffs, err = handle.List[WriteOff](q.Query(ctx, `SELECT id, number, invoice_id, trim_scale(amount)::text AS amount, reason, status,
		approval_request_id, created_at FROM billing.write_offs WHERE invoice_id = $1 ORDER BY created_at`, iid)); err != nil {
		return d, err
	}
	if inv.Status != "draft" && inv.Status != "void" {
		var token *string
		if err := q.QueryRow(ctx, `SELECT public_token FROM billing.invoices WHERE id = $1`, iid).Scan(&token); err == nil && token != nil && publicBase != "" {
			l := strings.TrimRight(publicBase, "/") + "/invoice/" + *token
			d.PayLink = &l
		}
	}
	return d, nil
}

// BillTo overrides the bill-to party of an invoice.
type BillTo struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
	NPWP    string `json:"npwp,omitempty"`
	Email   string `json:"email,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

// InvoiceInput generates an invoice (draft unless Issue).
type InvoiceInput struct {
	FolioID            *uuid.UUID  `json:"folioId,omitempty" doc:"Folio invoice: uninvoiced charges of one folio"`
	CustomerFolioID    *uuid.UUID  `json:"customerFolioId,omitempty" doc:"Folio invoice: uninvoiced charges of every folio of the customer folio"`
	LineIDs            []uuid.UUID `json:"lineIds,omitempty" doc:"Folio invoice: only these charge lines"`
	AccountID          *uuid.UUID  `json:"accountId,omitempty" doc:"Account invoice: uninvoiced charges on the AR account"`
	CorporateAccountID *uuid.UUID  `json:"corporateAccountId,omitempty" doc:"Account invoice of the corporate city ledger, or the payer of a folio invoice"`
	ScheduleLineID     *uuid.UUID  `json:"scheduleLineId,omitempty" doc:"Schedule invoice: one payment schedule line (DP, installment)"`
	From               string      `json:"from,omitempty" doc:"Account invoice period (YYYY-MM-DD)"`
	To                 string      `json:"to,omitempty"`
	Kind               string      `json:"kind,omitempty" enum:"standard,final" doc:"Folio invoices only; final = the closing invoice of an event"`
	TermsDays          *int        `json:"termsDays,omitempty" doc:"Default: Credit Policies term for companies, 0 for individuals"`
	BillTo             *BillTo     `json:"billTo,omitempty"`
	Notes              string      `json:"notes,omitempty"`
	Issue              bool        `json:"issue,omitempty" doc:"Issue immediately"`
}

type draftLine struct {
	folioLine, entry     *uuid.UUID
	desc                 string
	qty, unit            decimal.Decimal
	net, svc, tax, total decimal.Decimal
	line, component      *string
	folioOf              *uuid.UUID
	depositOf            *uuid.UUID // a "less: received" line of a folio
	isReceipt            bool
}

// CreateInvoice builds a draft invoice from a folio, a customer folio, an
// AR account or a payment schedule line.
func (h *HTTP) CreateInvoice(ctx context.Context, tx pgx.Tx, property uuid.UUID, in InvoiceInput) (InvoiceDetail, error) {
	s := h.Svc
	cur, err := org.Currency(ctx, tx)
	if err != nil {
		return InvoiceDetail{}, err
	}
	var (
		lines        []draftLine
		kind         = "standard"
		customerID   *uuid.UUID
		corporateID  = in.CorporateAccountID
		accountID    *uuid.UUID
		customerFol  *uuid.UUID
		folioID      *uuid.UUID
		scheduleLine *uuid.UUID
		billTo       BillTo
	)
	sources := 0
	for _, x := range []bool{in.FolioID != nil, in.CustomerFolioID != nil, in.AccountID != nil, in.ScheduleLineID != nil} {
		if x {
			sources++
		}
	}
	if sources == 0 && in.CorporateAccountID != nil {
		a, err := s.EnsureCorporateAccount(ctx, tx, property, *in.CorporateAccountID)
		if err != nil {
			return InvoiceDetail{}, err
		}
		in.AccountID, sources = &a.ID, 1
	}
	if sources != 1 {
		return InvoiceDetail{}, errs.Validation("source_required", "choose one source: a folio, a customer folio, an account or a schedule line",
			errs.Field("folioId", "required", "one source"))
	}
	switch {
	case in.ScheduleLineID != nil:
		var l struct {
			ScheduleID uuid.UUID
			Label      string
			Amount     string
			Status     string
			Invoice    *uuid.UUID
			Title      string
			Folio      *uuid.UUID
			Customer   *uuid.UUID
			Corporate  *uuid.UUID
			LineStatus string
		}
		if err := tx.QueryRow(ctx, `SELECT l.schedule_id, l.label, (l.amount - l.paid_amount)::text, s.status, l.invoice_id, s.title, s.folio_id,
			s.customer_id, s.corporate_account_id, l.status FROM billing.payment_schedule_lines l JOIN billing.payment_schedules s ON s.id = l.schedule_id
			WHERE l.id = $1 AND l.property_id = $2 FOR UPDATE OF l`, *in.ScheduleLineID, property).
			Scan(&l.ScheduleID, &l.Label, &l.Amount, &l.Status, &l.Invoice, &l.Title, &l.Folio, &l.Customer, &l.Corporate, &l.LineStatus); err != nil {
			if dbtx.IsNoRows(err) {
				return InvoiceDetail{}, errs.NotFound("payment schedule line")
			}
			return InvoiceDetail{}, err
		}
		if l.Invoice != nil {
			var st string
			_ = tx.QueryRow(ctx, `SELECT status FROM billing.invoices WHERE id = $1`, *l.Invoice).Scan(&st)
			if st != "void" {
				return GetInvoice(ctx, tx, *l.Invoice, h.publicBase()) // idempotent: one invoice per schedule line
			}
		}
		if l.Status != "active" || l.LineStatus == "paid" || l.LineStatus == "cancelled" {
			return InvoiceDetail{}, errs.Conflict("nothing_due", "the schedule line is paid or cancelled")
		}
		kind, scheduleLine, folioID, customerID, corporateID = "deposit", in.ScheduleLineID, l.Folio, l.Customer, l.Corporate
		amt := dec(l.Amount)
		lines = append(lines, draftLine{desc: l.Label + " · " + l.Title, qty: decimal.NewFromInt(1), unit: amt, net: amt, total: amt,
			component: ptr("deposit")})
	case in.AccountID != nil:
		a, err := GetAccount(ctx, tx, *in.AccountID)
		if err != nil {
			return InvoiceDetail{}, err
		}
		accountID, customerID = &a.ID, &a.CustomerID
		_ = tx.QueryRow(ctx, `SELECT corporate_account_id FROM billing.customer_accounts WHERE id = $1`, a.ID).Scan(&corporateID)
		// the period is in local dates of the property
		loc, err := org.Location(ctx, tx, property)
		if err != nil || loc == nil {
			loc = time.UTC
		}
		fromDate, toDate := time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		if in.From != "" {
			if fromDate, err = time.ParseInLocation("2006-01-02", in.From, loc); err != nil {
				return InvoiceDetail{}, handle.Invalid("from", "invalid", "YYYY-MM-DD")
			}
		}
		if in.To != "" {
			if toDate, err = time.ParseInLocation("2006-01-02", in.To, loc); err != nil {
				return InvoiceDetail{}, handle.Invalid("to", "invalid", "YYYY-MM-DD")
			}
			toDate = toDate.AddDate(0, 0, 1)
		}
		rows, err := tx.Query(ctx, `SELECT e.id, e.description, e.amount::text, e.folio_id, e.business_line FROM billing.account_entries e
			WHERE e.account_id = $1 AND e.entry_type = 'charge' AND e.invoice_id IS NULL AND e.amount > 0 AND e.occurred_at >= $2 AND e.occurred_at < $3
			ORDER BY e.occurred_at FOR UPDATE`, a.ID, fromDate, toDate)
		if err != nil {
			return InvoiceDetail{}, err
		}
		type entry struct {
			id     uuid.UUID
			desc   string
			amount decimal.Decimal
			folio  *uuid.UUID
			line   *string
		}
		var entries []entry
		for rows.Next() {
			var e entry
			var amt string
			if err := rows.Scan(&e.id, &e.desc, &amt, &e.folio, &e.line); err != nil {
				rows.Close()
				return InvoiceDetail{}, err
			}
			e.amount = dec(amt)
			entries = append(entries, e)
		}
		rows.Close()
		for _, e := range entries {
			eid := e.id
			// expand into the folio's charge lines when they make up the entry (tax detail for e-Faktur)
			if e.folio != nil {
				fl, err := folioDraftLines(ctx, tx, *e.folio, nil, true)
				if err != nil {
					return InvoiceDetail{}, err
				}
				sum := decimal.Zero
				for _, l := range fl {
					sum = sum.Add(l.total)
				}
				if len(fl) > 0 && sum.Equal(e.amount) {
					for i := range fl {
						fl[i].entry = &eid
					}
					lines = append(lines, fl...)
					continue
				}
			}
			lines = append(lines, draftLine{entry: &eid, desc: e.desc, qty: decimal.NewFromInt(1), unit: e.amount, net: e.amount, total: e.amount, line: e.line})
		}
	default: // folio invoice
		var folios []uuid.UUID
		if in.CustomerFolioID != nil {
			cf, err := GetCustomerFolio(ctx, tx, *in.CustomerFolioID)
			if err != nil {
				return InvoiceDetail{}, err
			}
			customerFol, customerID = &cf.ID, cf.CustomerID
			if cf.CorporateAccountID != nil {
				corporateID = cf.CorporateAccountID
			}
			for _, f := range cf.Folios {
				if f.Status != "cancelled" {
					folios = append(folios, f.ID)
				}
			}
		} else {
			f, err := GetFolio(ctx, tx, *in.FolioID)
			if err != nil {
				return InvoiceDetail{}, err
			}
			folioID, customerID, folios = &f.ID, f.CustomerID, []uuid.UUID{f.ID}
			var corp *uuid.UUID
			_ = tx.QueryRow(ctx, `SELECT corporate_account_id FROM billing.folios WHERE id = $1`, f.ID).Scan(&corp)
			if corporateID == nil {
				corporateID = corp
			}
			billTo.Name = f.HolderName
		}
		for _, fid := range folios {
			fl, err := folioDraftLines(ctx, tx, fid, in.LineIDs, true)
			if err != nil {
				return InvoiceDetail{}, err
			}
			lines = append(lines, fl...)
			if len(in.LineIDs) == 0 {
				// less what was already received on the folio (deposits, payments)
				rl, err := receiptLines(ctx, tx, fid)
				if err != nil {
					return InvoiceDetail{}, err
				}
				lines = append(lines, rl...)
			}
		}
		if in.Kind == "final" {
			kind = "final"
		}
	}
	charged := false
	for _, l := range lines {
		if !l.isReceipt && !l.total.IsZero() {
			charged = true
		}
	}
	if !charged {
		return InvoiceDetail{}, errs.Conflict("nothing_to_invoice", "there are no uninvoiced charges for this source")
	}
	// bill-to party
	switch {
	case corporateID != nil:
		var name string
		var npwp, email, phone, address *string
		if err := tx.QueryRow(ctx, `SELECT name, npwp, email, phone, address FROM crm.corporate_accounts WHERE id = $1`, *corporateID).
			Scan(&name, &npwp, &email, &phone, &address); err == nil {
			billTo = BillTo{Name: name, NPWP: deref(npwp), Email: deref(email), Phone: deref(phone), Address: deref(address)}
		}
	case customerID != nil:
		var name string
		var email, phone *string
		if err := tx.QueryRow(ctx, `SELECT name, email, phone FROM crm.customers WHERE id = $1`, *customerID).Scan(&name, &email, &phone); err == nil {
			billTo = BillTo{Name: name, Email: deref(email), Phone: deref(phone)}
		}
	}
	if b := in.BillTo; b != nil {
		for dst, src := range map[*string]string{&billTo.Name: b.Name, &billTo.Address: b.Address, &billTo.NPWP: b.NPWP, &billTo.Email: b.Email, &billTo.Phone: b.Phone} {
			if strings.TrimSpace(src) != "" {
				*dst = src
			}
		}
	}
	if billTo.Name == "" {
		billTo.Name = "Customer"
	}
	terms := 0
	if corporateID != nil {
		pol, _, err := LoadCreditPolicy(ctx, tx, property)
		if err != nil {
			return InvoiceDetail{}, err
		}
		terms = pol.PaymentTermDays
	}
	if in.TermsDays != nil {
		terms = *in.TermsDays
	}
	if terms < 0 {
		return InvoiceDetail{}, handle.Invalid("termsDays", "invalid", "zero or more days")
	}
	iid := id.New()
	sub, svc, tax, tot := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for _, l := range lines {
		sub, svc, tax, tot = sub.Add(l.net), svc.Add(l.svc), tax.Add(l.tax), tot.Add(l.total)
	}
	if !tot.IsPositive() {
		return InvoiceDetail{}, errs.Conflict("nothing_due", "nothing is due: what was received already covers the charges")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.invoices (id, property_id, kind, customer_id, corporate_account_id, account_id, customer_folio_id, folio_id,
		schedule_line_id, bill_to_name, bill_to_address, bill_to_npwp, bill_to_email, bill_to_phone, terms_days, currency, subtotal, service_amount,
		tax_amount, total, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::numeric,$18::numeric,$19::numeric,$20::numeric,$21,$22,$22)`,
		iid, property, kind, customerID, corporateID, accountID, customerFol, folioID, scheduleLine, billTo.Name, nullStr(billTo.Address),
		nullStr(billTo.NPWP), nullStr(billTo.Email), nullStr(billTo.Phone), terms, cur, sub.String(), svc.String(), tax.String(), tot.String(),
		nullStr(in.Notes), id.Ptr(actor(ctx))); err != nil {
		return InvoiceDetail{}, err
	}
	for i, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO billing.invoice_lines (id, property_id, invoice_id, seq, folio_line_id, account_entry_id, description, quantity,
			unit_price, net_amount, service_amount, tax_amount, total, business_line, revenue_component)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13::numeric,$14,$15)`,
			id.New(), property, iid, i+1, l.folioLine, l.entry, l.desc, l.qty.String(), l.unit.String(), l.net.String(), l.svc.String(), l.tax.String(),
			l.total.String(), l.line, l.component); err != nil {
			return InvoiceDetail{}, err
		}
		if l.folioLine != nil {
			if _, err := tx.Exec(ctx, `UPDATE billing.folio_lines SET invoice_id = $2 WHERE id = $1`, *l.folioLine, iid); err != nil {
				return InvoiceDetail{}, err
			}
		}
		if l.entry != nil {
			if _, err := tx.Exec(ctx, `UPDATE billing.account_entries SET invoice_id = $2 WHERE id = $1`, *l.entry, iid); err != nil {
				return InvoiceDetail{}, err
			}
		}
	}
	if scheduleLine != nil {
		if _, err := tx.Exec(ctx, `UPDATE billing.payment_schedule_lines SET invoice_id = $2 WHERE id = $1`, *scheduleLine, iid); err != nil {
			return InvoiceDetail{}, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.invoice", EntityID: iid.String(),
		EntityLabel: "Draft invoice · " + billTo.Name, PropertyID: &property, After: map[string]any{"kind": kind, "total": tot.String(), "lines": len(lines)}}); err != nil {
		return InvoiceDetail{}, err
	}
	if in.Issue {
		return h.IssueInvoice(ctx, tx, iid)
	}
	return GetInvoice(ctx, tx, iid, h.publicBase())
}

func ptr(s string) *string { return &s }

// folioDraftLines returns the uninvoiced, not voided charges of a folio.
func folioDraftLines(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, only []uuid.UUID, lock bool) ([]draftLine, error) {
	q := `SELECT l.id, l.description, l.quantity::text, l.unit_price::text, l.net_amount::text, l.service_amount::text, l.tax_amount::text, l.total::text,
		l.business_line, coalesce(l.revenue_component, l.charge_type) FROM billing.folio_lines l
		WHERE l.folio_id = $1 AND l.voided_at IS NULL AND l.invoice_id IS NULL AND (cardinality($2::uuid[]) = 0 OR l.id = ANY($2)) ORDER BY l.posted_at, l.id`
	if lock {
		q += ` FOR UPDATE`
	}
	if only == nil {
		only = []uuid.UUID{}
	}
	rows, err := tx.Query(ctx, q, folioID, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []draftLine
	for rows.Next() {
		var l draftLine
		var lid uuid.UUID
		var qty, unit, net, svc, tax, total string
		var line, comp string
		if err := rows.Scan(&lid, &l.desc, &qty, &unit, &net, &svc, &tax, &total, &line, &comp); err != nil {
			return nil, err
		}
		l.folioLine = &lid
		l.qty, l.unit, l.net, l.svc, l.tax, l.total = dec(qty), dec(unit), dec(net), dec(svc), dec(tax), dec(total)
		l.line, l.component = &line, &comp
		fid := folioID
		l.folioOf = &fid
		out = append(out, l)
	}
	return out, rows.Err()
}

// receiptLines are the "less: received" lines of a folio invoice: held and
// applied deposits and settlement payments (net of refunds). AR transfers
// (member account / folio transfer) are not receipts.
func receiptLines(ctx context.Context, tx pgx.Tx, folioID uuid.UUID) ([]draftLine, error) {
	var out []draftLine
	rows, err := tx.Query(ctx, `SELECT d.number, (CASE WHEN d.status = 'held' THEN d.amount ELSE d.applied_amount END)::text FROM billing.deposits d
		WHERE d.folio_id = $1 AND d.status IN ('held', 'applied') ORDER BY d.created_at`, folioID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var num, amt string
		if err := rows.Scan(&num, &amt); err != nil {
			rows.Close()
			return nil, err
		}
		a := dec(amt).Neg()
		fid := folioID
		out = append(out, draftLine{desc: "Less: deposit received " + num, qty: decimal.NewFromInt(1), unit: a, net: a, total: a,
			component: ptr("deposit"), depositOf: &fid, isReceipt: true})
	}
	rows.Close()
	var paid string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(amount - refunded_amount), 0)::text FROM billing.payments WHERE folio_id = $1
		AND status IN ('completed', 'refunded') AND purpose = 'settlement' AND method_type NOT IN ('member_account', 'folio_transfer')`, folioID).Scan(&paid); err != nil {
		return nil, err
	}
	if p := dec(paid); p.IsPositive() {
		a := p.Neg()
		out = append(out, draftLine{desc: "Less: payments received", qty: decimal.NewFromInt(1), unit: a, net: a, total: a, component: ptr("payment"),
			isReceipt: true})
	}
	return out, nil
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// IssueInvoice numbers and issues a draft (FR-BIL-P3-04). A folio invoice
// applies the held deposits and moves the rest to the payer's AR account.
func (h *HTTP) IssueInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (InvoiceDetail, error) {
	s := h.Svc
	inv, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return InvoiceDetail{}, err
	}
	if inv.Status != "draft" {
		return InvoiceDetail{}, errs.Conflict("not_draft", "only draft invoices can be issued")
	}
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM billing.invoices WHERE id = $1`, iid).Scan(&property); err != nil {
		return InvoiceDetail{}, err
	}
	day, err := CurrentBusinessDate(ctx, tx, property)
	if err != nil {
		return InvoiceDetail{}, err
	}
	num, err := yearlyNumber(ctx, tx, property, "INV", day.Year())
	if err != nil {
		return InvoiceDetail{}, err
	}
	due := day.AddDate(0, 0, inv.TermsDays)
	accountID := inv.AccountID
	if inv.ScheduleLineID == nil && inv.AccountID == nil {
		// folio invoice: apply held deposits, then transfer the rest per folio to the AR account
		var acc Account
		if inv.CorporateAccountID != nil {
			if acc, err = s.EnsureCorporateAccount(ctx, tx, property, *inv.CorporateAccountID); err != nil {
				return InvoiceDetail{}, err
			}
		} else if inv.CustomerID != nil {
			if acc, err = s.EnsureAccount(ctx, tx, property, *inv.CustomerID, "customer", nil); err != nil {
				return InvoiceDetail{}, err
			}
		} else {
			return InvoiceDetail{}, errs.Conflict("payer_required", "the folio has no customer or company to invoice; split it to a payer first")
		}
		accountID = &acc.ID
		rows, err := tx.Query(ctx, `SELECT DISTINCT l.folio_id FROM billing.invoice_lines il JOIN billing.folio_lines l ON l.id = il.folio_line_id
			WHERE il.invoice_id = $1`, iid)
		if err != nil {
			return InvoiceDetail{}, err
		}
		folios, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return InvoiceDetail{}, err
		}
		for _, fid := range folios {
			if err := s.applyDeposits(ctx, tx, fid); err != nil {
				return InvoiceDetail{}, err
			}
			var invoiced string
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(il.total), 0)::text FROM billing.invoice_lines il JOIN billing.folio_lines l ON l.id = il.folio_line_id
				WHERE il.invoice_id = $1 AND l.folio_id = $2`, iid, fid).Scan(&invoiced); err != nil {
				return InvoiceDetail{}, err
			}
			sum, err := FolioSummary(ctx, tx, fid)
			if err != nil {
				return InvoiceDetail{}, err
			}
			amt := decimal.Min(dec(invoiced), dec(sum.Balance))
			if !amt.IsPositive() {
				continue
			}
			p, err := s.TakeTender(ctx, tx, TenderPaymentInput{PaymentInput: PaymentInput{FolioID: &fid, AccountID: accountID, MethodType: "member_account",
				Amount: amt, Description: "Invoice " + num, SkipCreditCheck: inv.CorporateAccountID == nil}})
			if err != nil {
				return InvoiceDetail{}, err
			}
			if err := TagPayment(ctx, tx, p.ID, "invoiceTransfer", iid.String()); err != nil {
				return InvoiceDetail{}, err
			}
			// the AR entry of the transfer belongs to this invoice
			if _, err := tx.Exec(ctx, `UPDATE billing.account_entries SET invoice_id = $2 WHERE payment_id = $1`, p.ID, iid); err != nil {
				return InvoiceDetail{}, err
			}
		}
		// receipts already netted on the invoice count as paid on the AR side
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET number = $2, status = 'issued', issue_date = $3, due_date = $4, account_id = $5,
		public_token = $6, issued_at = now(), issued_by = $7, version = version + 1, updated_by = $7 WHERE id = $1`,
		iid, num, day, due, accountID, newToken(), id.Ptr(actor(ctx))); err != nil {
		return InvoiceDetail{}, err
	}
	d, err := GetInvoice(ctx, tx, iid, h.publicBase())
	if err != nil {
		return d, err
	}
	if _, err := s.Events.Publish(ctx, tx, EventInvoiceIssued, "billing.invoice", &iid, &property, invoicePayload(d)); err != nil {
		return d, err
	}
	return d, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "issue", EntityType: "billing.invoice", EntityID: iid.String(),
		EntityLabel: num + " · " + d.BillToName, PropertyID: &property, Before: inv, After: d.Invoice})
}

// invoicePayload is the event payload of invoice events (contract K2).
func invoicePayload(d InvoiceDetail) map[string]any {
	lines := make([]map[string]any, 0, len(d.Lines))
	for _, l := range d.Lines {
		lines = append(lines, map[string]any{"businessLine": l.BusinessLine, "revenueComponent": l.RevenueComponent, "net": l.NetAmount,
			"service": l.ServiceAmount, "tax": l.TaxAmount, "total": l.Total, "folioLineId": l.FolioLineID})
	}
	return map[string]any{"invoiceId": d.ID, "number": d.Number, "kind": d.Kind, "accountId": d.AccountID, "customerId": d.CustomerID,
		"corporateAccountId": d.CorporateAccountID, "billToName": d.BillToName, "billToNpwp": d.BillToNPWP, "issueDate": d.IssueDate,
		"dueDate": d.DueDate, "currency": d.Currency, "subtotal": d.Subtotal, "serviceAmount": d.ServiceAmount, "taxAmount": d.TaxAmount,
		"total": d.Total, "paidAmount": d.PaidAmount, "outstanding": d.Outstanding, "status": d.Status, "lines": lines}
}

func lockInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (Invoice, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM billing.invoices WHERE id = $1 FOR UPDATE`, iid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Invoice{}, errs.NotFound("invoice")
		}
		return Invoice{}, err
	}
	return oneOf[Invoice]("invoice")(tx.Query(ctx, invoiceSelect+` WHERE i.id = $1`, iid))
}

// refreshInvoice recomputes paid / status after a settlement change and
// publishes billing.invoice_paid once fully settled.
func (s *Service) refreshInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID) error {
	var property uuid.UUID
	var status string
	var total, credited, writtenOff, paid string
	if err := tx.QueryRow(ctx, `SELECT i.property_id, i.status, i.total::text, i.credited_amount::text, i.written_off_amount::text,
		coalesce((SELECT sum(amount) FROM billing.payment_allocations a WHERE a.invoice_id = i.id), 0)::text
		FROM billing.invoices i WHERE i.id = $1`, iid).Scan(&property, &status, &total, &credited, &writtenOff, &paid); err != nil {
		return err
	}
	if status == "draft" || status == "void" {
		return nil
	}
	outstanding := dec(total).Sub(dec(paid)).Sub(dec(credited)).Sub(dec(writtenOff))
	next := status
	switch {
	case !outstanding.IsPositive():
		next = "paid"
	case dec(paid).Add(dec(credited)).Add(dec(writtenOff)).IsPositive() && status != "overdue":
		next = "partially_paid"
	case status == "paid":
		next = "issued"
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET paid_amount = $2::numeric, status = $3,
		paid_at = CASE WHEN $3 = 'paid' THEN coalesce(paid_at, now()) ELSE NULL END, version = version + 1 WHERE id = $1`, iid, dec(paid).String(), next); err != nil {
		return err
	}
	if next == "paid" && status != "paid" {
		d, err := GetInvoice(ctx, tx, iid, "")
		if err != nil {
			return err
		}
		if _, err := s.Events.Publish(ctx, tx, EventInvoicePaid, "billing.invoice", &iid, &property, invoicePayload(d)); err != nil {
			return err
		}
	}
	return nil
}

// allocate applies part of a payment to an invoice and / or a schedule line.
func (s *Service) allocate(ctx context.Context, tx pgx.Tx, property, paymentID uuid.UUID, invoiceID, lineID *uuid.UUID, amount decimal.Decimal) error {
	if !amount.IsPositive() {
		return handle.Invalid("amount", "invalid", "positive amount")
	}
	var pamt, refunded, used, status string
	if err := tx.QueryRow(ctx, `SELECT p.amount::text, p.refunded_amount::text, p.status,
		coalesce((SELECT sum(amount) FROM billing.payment_allocations a WHERE a.payment_id = p.id), 0)::text
		FROM billing.payments p WHERE p.id = $1 FOR UPDATE`, paymentID).Scan(&pamt, &refunded, &status, &used); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("payment")
		}
		return err
	}
	if status != "completed" && status != "refunded" {
		return errs.Conflict("payment_not_completed", "only completed payments can be allocated")
	}
	if amount.GreaterThan(dec(pamt).Sub(dec(refunded)).Sub(dec(used))) {
		return errs.Validation("over_allocation", "the payment has no unallocated amount left", errs.Field("amount", "too_large", "at most the unallocated amount"))
	}
	if invoiceID != nil {
		var total, paid, credited, wo, st string
		if err := tx.QueryRow(ctx, `SELECT total::text, status, credited_amount::text, written_off_amount::text,
			coalesce((SELECT sum(amount) FROM billing.payment_allocations a WHERE a.invoice_id = i.id), 0)::text
			FROM billing.invoices i WHERE i.id = $1 FOR UPDATE`, *invoiceID).Scan(&total, &st, &credited, &wo, &paid); err != nil {
			if dbtx.IsNoRows(err) {
				return errs.NotFound("invoice")
			}
			return err
		}
		if st == "draft" || st == "void" {
			return errs.Conflict("invoice_not_open", "the invoice is not issued")
		}
		if amount.GreaterThan(dec(total).Sub(dec(paid)).Sub(dec(credited)).Sub(dec(wo))) {
			return errs.Validation("over_allocation", "the amount exceeds what is outstanding on the invoice",
				errs.Field("amount", "too_large", "at most the outstanding amount"))
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billing.payment_allocations (id, property_id, payment_id, invoice_id, schedule_line_id, amount, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7)`, id.New(), property, paymentID, invoiceID, lineID, amount.String(), id.Ptr(actor(ctx))); err != nil {
		return err
	}
	if lineID != nil {
		if err := s.refreshScheduleLine(ctx, tx, *lineID); err != nil {
			return err
		}
	}
	if invoiceID != nil {
		return s.refreshInvoice(ctx, tx, *invoiceID)
	}
	return nil
}

// PayInvoiceInput takes a payment for an invoice.
type PayInvoiceInput struct {
	MethodType string `json:"methodType" enum:"cash,bank_transfer,virtual_account,qris,card,payment_gateway"`
	Amount     string `json:"amount,omitempty" doc:"Default: the outstanding amount"`
	Reference  string `json:"reference,omitempty"`
	Online     bool   `json:"online,omitempty" doc:"Create a gateway payment (payment link / QRIS) settled by the webhook"`
	PayerName  string `json:"payerName,omitempty"`
}

// PayInvoice takes a payment and allocates it: AR invoices settle the AR
// account, schedule invoices become a deposit on the schedule's folio.
func (h *HTTP) PayInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, in PayInvoiceInput) (Payment, error) {
	s := h.Svc
	inv, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return Payment{}, err
	}
	if inv.Status == "draft" || inv.Status == "void" || inv.Status == "paid" {
		return Payment{}, errs.Conflict("invoice_not_open", "the invoice is not open for payment")
	}
	amt, err := handle.Decimal("amount", in.Amount, dec(inv.Outstanding))
	if err != nil {
		return Payment{}, err
	}
	if !amt.IsPositive() || amt.GreaterThan(dec(inv.Outstanding)) {
		return Payment{}, handle.Invalid("amount", "invalid", "between 0 and the outstanding amount")
	}
	method := in.MethodType
	if method == "" {
		method = "qris"
	}
	pin := PaymentInput{MethodType: method, Amount: amt, Reference: in.Reference, PayerName: in.PayerName, Description: deref(inv.Number)}
	if in.Online {
		pin.Channel = "online"
	}
	var lineID *uuid.UUID
	switch {
	case inv.ScheduleLineID != nil:
		var folio *uuid.UUID
		var source string
		if err := tx.QueryRow(ctx, `SELECT s.folio_id, s.source_type FROM billing.payment_schedule_lines l JOIN billing.payment_schedules s ON s.id = l.schedule_id
			WHERE l.id = $1`, *inv.ScheduleLineID).Scan(&folio, &source); err != nil {
			return Payment{}, err
		}
		if folio == nil {
			return Payment{}, errs.Conflict("no_folio", "the payment schedule has no folio")
		}
		pin.FolioID, pin.Purpose, lineID = folio, schedulePurpose(source), inv.ScheduleLineID
	case inv.AccountID != nil:
		pin.AccountID, pin.Purpose = inv.AccountID, "account_settlement"
	default:
		return Payment{}, errs.Conflict("no_receivable", "the invoice has no receivable account")
	}
	p, err := s.TakePayment(ctx, tx, pin)
	if err != nil {
		return p, err
	}
	if err := TagPayment(ctx, tx, p.ID, "invoiceId", iid.String()); err != nil {
		return p, err
	}
	if p.Status == "completed" {
		var property uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM billing.invoices WHERE id = $1`, iid).Scan(&property); err != nil {
			return p, err
		}
		if err := s.allocate(ctx, tx, property, p.ID, &iid, lineID, amt); err != nil {
			return p, err
		}
	}
	return GetPayment(ctx, tx, p.ID)
}

func schedulePurpose(source string) string {
	switch source {
	case "membership", "other":
		return "settlement"
	}
	return "deposit"
}

// OnPaymentSettled allocates gateway payments of invoice payment links once
// the webhook settles them (idempotent).
func (s *Service) OnPaymentSettled(ctx context.Context, tx pgx.Tx, paymentID uuid.UUID) error {
	var property uuid.UUID
	var ref map[string]any
	var amount, refunded string
	if err := tx.QueryRow(ctx, `SELECT property_id, tender_ref, amount::text, refunded_amount::text FROM billing.payments WHERE id = $1`, paymentID).
		Scan(&property, &ref, &amount, &refunded); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	raw, _ := ref["invoiceId"].(string)
	iid, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM billing.payment_allocations WHERE payment_id = $1)`, paymentID).Scan(&done); err != nil || done {
		return err
	}
	var line *uuid.UUID
	var outstanding string
	if err := tx.QueryRow(ctx, `SELECT schedule_line_id, (total - paid_amount - credited_amount - written_off_amount)::text FROM billing.invoices WHERE id = $1`,
		iid).Scan(&line, &outstanding); err != nil {
		return err
	}
	amt := decimal.Min(dec(amount).Sub(dec(refunded)), dec(outstanding))
	if !amt.IsPositive() {
		return nil
	}
	return s.allocate(ctx, tx, property, paymentID, &iid, line, amt)
}

// AllocationInput allocates an existing payment (e.g. a bank transfer
// received on the AR account) to invoices.
type AllocationInput struct {
	PaymentID   uuid.UUID `json:"paymentId"`
	Allocations []struct {
		InvoiceID uuid.UUID `json:"invoiceId"`
		Amount    string    `json:"amount"`
	} `json:"allocations"`
}

// AllocatePayment allocates one payment to many invoices (FR-BIL-P3-06).
func (s *Service) AllocatePayment(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AllocationInput) ([]Allocation, error) {
	if len(in.Allocations) == 0 {
		return nil, handle.Invalid("allocations", "required", "at least one invoice")
	}
	for i, a := range in.Allocations {
		amt, err := handle.Decimal(fmt.Sprintf("allocations[%d].amount", i), a.Amount, decimal.Zero)
		if err != nil {
			return nil, err
		}
		iid := a.InvoiceID
		if err := s.allocate(ctx, tx, property, in.PaymentID, &iid, nil, amt); err != nil {
			return nil, err
		}
	}
	out, err := handle.List[Allocation](tx.Query(ctx, `SELECT a.id, a.payment_id, p.number AS payment_number, a.invoice_id, a.schedule_line_id,
		trim_scale(a.amount)::text AS amount, a.created_at FROM billing.payment_allocations a JOIN billing.payments p ON p.id = a.payment_id
		WHERE a.payment_id = $1 ORDER BY a.created_at`, in.PaymentID))
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "allocate", EntityType: "billing.payment", EntityID: in.PaymentID.String(),
		PropertyID: &property, After: out})
}

// CreditNoteInput reduces an issued invoice.
type CreditNoteInput struct {
	InvoiceID uuid.UUID `json:"invoiceId"`
	Amount    string    `json:"amount"`
	Reason    string    `json:"reason"`
}

// IssueCreditNote reduces an invoice; on an AR account the receivable is
// reduced too (FR-BIL-P3-04).
func (s *Service) IssueCreditNote(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CreditNoteInput) (CreditNote, error) {
	inv, err := lockInvoice(ctx, tx, in.InvoiceID)
	if err != nil {
		return CreditNote{}, err
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return CreditNote{}, err
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return CreditNote{}, err
	}
	if inv.Status == "draft" || inv.Status == "void" {
		return CreditNote{}, errs.Conflict("invoice_not_open", "only issued invoices can be credited")
	}
	if !amt.IsPositive() || amt.GreaterThan(dec(inv.Outstanding)) {
		return CreditNote{}, handle.Invalid("amount", "invalid", "between 0 and the outstanding amount")
	}
	num, err := number(ctx, tx, property, "CN")
	if err != nil {
		return CreditNote{}, err
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.credit_notes (id, property_id, number, invoice_id, amount, currency, reason, created_by)
		VALUES ($1,$2,$3,$4,$5::numeric,$6,$7,$8)`, cid, property, num, inv.ID, amt.String(), inv.Currency, in.Reason, id.Ptr(actor(ctx))); err != nil {
		return CreditNote{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET credited_amount = credited_amount + $2::numeric WHERE id = $1`, inv.ID, amt.String()); err != nil {
		return CreditNote{}, err
	}
	if inv.AccountID != nil {
		if err := PostEntry(ctx, tx, property, *inv.AccountID, "adjustment", amt.Neg(), inv.Currency, "Credit note "+num, nil, nil); err != nil {
			return CreditNote{}, err
		}
	}
	if err := s.refreshInvoice(ctx, tx, inv.ID); err != nil {
		return CreditNote{}, err
	}
	cn, err := oneOf[CreditNote]("credit note")(tx.Query(ctx, `SELECT id, number, invoice_id, trim_scale(amount)::text AS amount, currency, reason, status, created_at
		FROM billing.credit_notes WHERE id = $1`, cid))
	if err != nil {
		return cn, err
	}
	if _, err := s.Events.Publish(ctx, tx, EventCreditNote, "billing.credit_note", &cid, &property, map[string]any{"creditNoteId": cid, "number": num,
		"invoiceId": inv.ID, "invoiceNumber": inv.Number, "accountId": inv.AccountID, "amount": amt.String(), "currency": inv.Currency, "reason": in.Reason}); err != nil {
		return cn, err
	}
	return cn, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.credit_note", EntityID: cid.String(),
		EntityLabel: num, PropertyID: &property, Reason: in.Reason, After: cn})
}

// VoidInvoice voids an invoice without payments: the charges become
// invoiceable again and an AR transfer goes back to the folio.
func (s *Service) VoidInvoice(ctx context.Context, tx pgx.Tx, iid uuid.UUID, reason string) (Invoice, error) {
	inv, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return Invoice{}, err
	}
	if err := handle.Required("reason", reason); err != nil {
		return Invoice{}, err
	}
	if inv.Status == "void" {
		return inv, nil
	}
	if dec(inv.PaidAmount).IsPositive() || dec(inv.CreditedAmount).IsPositive() || dec(inv.WrittenOffAmount).IsPositive() {
		return Invoice{}, errs.Conflict("invoice_settled", "the invoice has payments, credit notes or write-offs; credit it instead of voiding")
	}
	var property uuid.UUID
	var issued *time.Time
	if err := tx.QueryRow(ctx, `SELECT property_id, issue_date FROM billing.invoices WHERE id = $1`, iid).Scan(&property, &issued); err != nil {
		return Invoice{}, err
	}
	if issued != nil {
		if err := ensureOpenPeriod(ctx, tx, property, *issued, "issue a credit note instead of voiding"); err != nil {
			return Invoice{}, err
		}
	}
	// reverse the AR transfers made at issue
	rows, err := tx.Query(ctx, `SELECT id, (amount - refunded_amount)::text FROM billing.payments WHERE tender_ref->>'invoiceTransfer' = $1
		AND status = 'completed'`, iid.String())
	if err != nil {
		return Invoice{}, err
	}
	type tr struct {
		id  uuid.UUID
		amt string
	}
	var trs []tr
	for rows.Next() {
		var t tr
		if err := rows.Scan(&t.id, &t.amt); err != nil {
			rows.Close()
			return Invoice{}, err
		}
		trs = append(trs, t)
	}
	rows.Close()
	for _, t := range trs {
		if _, err := s.RequestRefund(ctx, tx, t.id, dec(t.amt), "member_account", "Invoice void: "+reason, nil); err != nil {
			return Invoice{}, err
		}
	}
	// account charges billed by this invoice become invoiceable again; the AR
	// transfers made at issue stay linked (their refund offsets them)
	for _, q := range []string{`UPDATE billing.folio_lines SET invoice_id = NULL WHERE invoice_id = $1`,
		`UPDATE billing.account_entries e SET invoice_id = NULL WHERE e.invoice_id = $1 AND NOT EXISTS (SELECT 1 FROM billing.payments p
		  WHERE p.id = e.payment_id AND p.tender_ref->>'invoiceTransfer' = $1::text)`,
		`UPDATE billing.payment_schedule_lines SET invoice_id = NULL WHERE invoice_id = $1`} {
		if _, err := tx.Exec(ctx, q, iid); err != nil {
			return Invoice{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET status = 'void', voided_at = now(), void_reason = $2, version = version + 1, updated_by = $3
		WHERE id = $1`, iid, reason, id.Ptr(actor(ctx))); err != nil {
		return Invoice{}, err
	}
	out, err := oneOf[Invoice]("invoice")(tx.Query(ctx, invoiceSelect+` WHERE i.id = $1`, iid))
	if err != nil {
		return out, err
	}
	if inv.Number != nil {
		if _, err := s.Events.Publish(ctx, tx, EventInvoiceVoided, "billing.invoice", &iid, &property, map[string]any{"invoiceId": iid,
			"number": inv.Number, "accountId": inv.AccountID, "total": inv.Total, "reason": reason}); err != nil {
			return out, err
		}
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionVoid, EntityType: "billing.invoice", EntityID: iid.String(),
		EntityLabel: deref(inv.Number), PropertyID: &property, Reason: reason, Before: inv, After: out})
}

// WriteOffInput requests a write-off of (part of) an invoice.
type WriteOffInput struct {
	Amount string `json:"amount,omitempty" doc:"Default: the outstanding amount"`
	Reason string `json:"reason"`
}

// RequestWriteOff submits a write-off for approval (FR-BIL-P3-08).
func (h *HTTP) RequestWriteOff(ctx context.Context, tx pgx.Tx, property, iid uuid.UUID, in WriteOffInput) (WriteOff, error) {
	inv, err := lockInvoice(ctx, tx, iid)
	if err != nil {
		return WriteOff{}, err
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return WriteOff{}, err
	}
	if inv.Status == "draft" || inv.Status == "void" || inv.Status == "paid" {
		return WriteOff{}, errs.Conflict("invoice_not_open", "only open invoices can be written off")
	}
	amt, err := handle.Decimal("amount", in.Amount, dec(inv.Outstanding))
	if err != nil {
		return WriteOff{}, err
	}
	if !amt.IsPositive() || amt.GreaterThan(dec(inv.Outstanding)) {
		return WriteOff{}, handle.Invalid("amount", "invalid", "between 0 and the outstanding amount")
	}
	num, err := number(ctx, tx, property, "WO")
	if err != nil {
		return WriteOff{}, err
	}
	wid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO billing.write_offs (id, property_id, number, invoice_id, amount, reason, created_by)
		VALUES ($1,$2,$3,$4,$5::numeric,$6,$7)`, wid, property, num, iid, amt.String(), in.Reason, id.Ptr(actor(ctx))); err != nil {
		return WriteOff{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionCreate, EntityType: "billing.write_off", EntityID: wid.String(),
		EntityLabel: num, PropertyID: &property, Reason: in.Reason, After: map[string]any{"invoice": inv.Number, "amount": amt.String()}}); err != nil {
		return WriteOff{}, err
	}
	f, _ := amt.Float64()
	rid, _, err := h.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: WriteOffDocumentType.Code, DocumentID: wid, DocumentRef: num,
		Title: "Write-off " + deref(inv.Number) + " · " + amt.String(), PropertyID: property, Attributes: map[string]any{"amount": f}})
	if err != nil {
		return WriteOff{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.write_offs SET approval_request_id = $2 WHERE id = $1`, wid, rid); err != nil {
		return WriteOff{}, err
	}
	return oneOf[WriteOff]("write-off")(tx.Query(ctx, `SELECT id, number, invoice_id, trim_scale(amount)::text AS amount, reason, status, approval_request_id, created_at
		FROM billing.write_offs WHERE id = $1`, wid))
}

// WriteOffDecision posts an approved write-off.
func (h *HTTP) WriteOffDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	s := h.Svc
	var status, amount, cur string
	var iid uuid.UUID
	var acct *uuid.UUID
	var num *string
	if err := tx.QueryRow(ctx, `SELECT w.status, w.amount::text, w.invoice_id, i.account_id, i.currency, i.number FROM billing.write_offs w
		JOIN billing.invoices i ON i.id = w.invoice_id WHERE w.id = $1 FOR UPDATE OF w`, d.DocumentID).Scan(&status, &amount, &iid, &acct, &cur, &num); err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	st := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if st == "" {
		return nil
	}
	if st == "approved" {
		inv, err := lockInvoice(ctx, tx, iid)
		if err != nil {
			return err
		}
		amt := decimal.Min(dec(amount), dec(inv.Outstanding))
		if amt.IsPositive() {
			if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET written_off_amount = written_off_amount + $2::numeric WHERE id = $1`, iid, amt.String()); err != nil {
				return err
			}
			if acct != nil {
				if err := PostEntry(ctx, tx, d.PropertyID, *acct, "adjustment", amt.Neg(), cur, "Write-off "+deref(num), nil, nil); err != nil {
					return err
				}
			}
			if err := s.refreshInvoice(ctx, tx, iid); err != nil {
				return err
			}
			if _, err := s.Events.Publish(ctx, tx, EventWrittenOff, "billing.invoice", &iid, &d.PropertyID, map[string]any{"invoiceId": iid,
				"number": num, "accountId": acct, "amount": amt.String(), "currency": cur, "reason": d.Reason}); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE billing.write_offs SET status = $2, decided_at = now() WHERE id = $1`, d.DocumentID, st); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: audit.ActionStatusChange, EntityType: "billing.write_off",
		EntityID: d.DocumentID.String(), PropertyID: &d.PropertyID, Reason: d.Reason, After: map[string]any{"status": st}})
}

// ── ageing & statement (FR-BIL-P3-05) ─────────────────────────────────────

// AgingRow is the open receivable of one payer by age of invoice.
type AgingRow struct {
	AccountID          *uuid.UUID `json:"accountId" db:"account_id"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	BillToName         string     `json:"billToName" db:"bill_to_name"`
	Invoices           int        `json:"invoices" db:"invoices"`
	Days0to30          string     `json:"days0to30" db:"d0_30"`
	Days31to60         string     `json:"days31to60" db:"d31_60"`
	Days61to90         string     `json:"days61to90" db:"d61_90"`
	Over90             string     `json:"over90" db:"d90"`
	Total              string     `json:"total" db:"total"`
	Overdue            string     `json:"overdue" db:"overdue"`
}

// Aging is the operational receivable ageing (0–30, 31–60, 61–90, > 90
// days since the invoice date) of open invoices as of a date; P4's AR Aging
// must show the same figures (PRD P4 FR-AR-04).
type Aging struct {
	AsOf   string     `json:"asOf"`
	Rows   []AgingRow `json:"rows"`
	Totals AgingRow   `json:"totals"`
}

// AgingAsOf computes the ageing as of a date (settlements after the date
// are ignored).
func AgingAsOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time, account *uuid.UUID) (Aging, error) {
	day := asOf.Format("2006-01-02")
	rows, err := handle.List[AgingRow](q.Query(ctx, `WITH open AS (
		SELECT i.account_id, i.corporate_account_id, i.customer_id, i.bill_to_name, i.issue_date, i.due_date,
		  i.total - coalesce((SELECT sum(a.amount) FROM billing.payment_allocations a WHERE a.invoice_id = i.id AND a.created_at < ($2::date + 1)), 0)
		    - coalesce((SELECT sum(c.amount) FROM billing.credit_notes c WHERE c.invoice_id = i.id AND c.created_at < ($2::date + 1)), 0)
		    - coalesce((SELECT sum(w.amount) FROM billing.write_offs w WHERE w.invoice_id = i.id AND w.status = 'approved' AND w.decided_at < ($2::date + 1)), 0)
		    AS open_amount
		FROM billing.invoices i WHERE i.property_id = $1 AND i.issue_date <= $2::date
		  AND (i.status NOT IN ('draft', 'void') OR (i.status = 'void' AND i.voided_at >= ($2::date + 1)))
		  AND ($3::uuid IS NULL OR i.account_id = $3))
		SELECT account_id, corporate_account_id, customer_id, min(bill_to_name) AS bill_to_name, count(*)::int AS invoices,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date <= 30), 0))::text AS d0_30,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 31 AND 60), 0))::text AS d31_60,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 61 AND 90), 0))::text AS d61_90,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date > 90), 0))::text AS d90,
		  trim_scale(sum(open_amount))::text AS total,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE due_date < $2::date), 0))::text AS overdue
		FROM open WHERE open_amount > 0 GROUP BY account_id, corporate_account_id, customer_id ORDER BY min(bill_to_name)`, property, day, account))
	if err != nil {
		return Aging{}, err
	}
	t := AgingRow{BillToName: "Total"}
	sum := map[*string]decimal.Decimal{}
	for _, r := range rows {
		t.Invoices += r.Invoices
		for dst, v := range map[*string]string{&t.Days0to30: r.Days0to30, &t.Days31to60: r.Days31to60, &t.Days61to90: r.Days61to90, &t.Over90: r.Over90,
			&t.Total: r.Total, &t.Overdue: r.Overdue} {
			sum[dst] = sum[dst].Add(dec(v))
		}
	}
	for _, dst := range []*string{&t.Days0to30, &t.Days31to60, &t.Days61to90, &t.Over90, &t.Total, &t.Overdue} {
		*dst = sum[dst].String()
	}
	return Aging{AsOf: day, Rows: rows, Totals: t}, nil
}

// StatementEntry is one movement on an AR account.
type StatementEntry struct {
	OccurredAt  time.Time  `json:"occurredAt" db:"occurred_at"`
	EntryType   string     `json:"entryType" db:"entry_type"`
	Description string     `json:"description" db:"description"`
	Amount      string     `json:"amount" db:"amount"`
	InvoiceID   *uuid.UUID `json:"invoiceId" db:"invoice_id"`
}

// AccountStatement is the statement of a corporate (or other AR) account.
type AccountStatement struct {
	Account        Account          `json:"account"`
	BillToName     string           `json:"billToName"`
	From           string           `json:"from"`
	To             string           `json:"to"`
	OpeningBalance string           `json:"openingBalance"`
	Charges        string           `json:"charges"`
	Payments       string           `json:"payments"`
	ClosingBalance string           `json:"closingBalance"`
	Entries        []StatementEntry `json:"entries"`
	OpenInvoices   []Invoice        `json:"openInvoices"`
	Aging          Aging            `json:"aging"`
}

// AccountStatementOf builds the statement of an AR account for a period.
func AccountStatementOf(ctx context.Context, q dbtx.Querier, property, accountID uuid.UUID, from, to time.Time) (AccountStatement, error) {
	a, err := GetAccount(ctx, q, accountID)
	if err != nil {
		return AccountStatement{}, err
	}
	st := AccountStatement{Account: a, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), BillToName: a.Number}
	_ = q.QueryRow(ctx, `SELECT coalesce(ca.name, c.name) FROM billing.customer_accounts x JOIN crm.customers c ON c.id = x.customer_id
		LEFT JOIN crm.corporate_accounts ca ON ca.id = x.corporate_account_id WHERE x.id = $1`, accountID).Scan(&st.BillToName)
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	var opening, charges, payments string
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(amount) FILTER (WHERE occurred_at < $2), 0)::text,
		coalesce(sum(amount) FILTER (WHERE occurred_at >= $2 AND occurred_at < $3 AND amount > 0), 0)::text,
		coalesce(-sum(amount) FILTER (WHERE occurred_at >= $2 AND occurred_at < $3 AND amount < 0), 0)::text
		FROM billing.account_entries WHERE account_id = $1`, accountID, start, end).Scan(&opening, &charges, &payments); err != nil {
		return st, err
	}
	p := places(a.Currency)
	st.OpeningBalance, st.Charges, st.Payments = dec(opening).StringFixed(p), dec(charges).StringFixed(p), dec(payments).StringFixed(p)
	st.ClosingBalance = dec(opening).Add(dec(charges)).Sub(dec(payments)).StringFixed(p)
	if st.Entries, err = handle.List[StatementEntry](q.Query(ctx, `SELECT occurred_at, entry_type, description, trim_scale(amount)::text AS amount, invoice_id
		FROM billing.account_entries WHERE account_id = $1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY occurred_at`, accountID, start, end)); err != nil {
		return st, err
	}
	if st.OpenInvoices, err = listInvoices(ctx, q, `i.account_id = $1 AND i.status IN ('issued', 'partially_paid', 'overdue')`, accountID); err != nil {
		return st, err
	}
	st.Aging, err = AgingAsOf(ctx, q, property, to, &accountID)
	return st, err
}

// InvoicePDF renders an invoice (ID/EN labels).
func InvoicePDF(ctx context.Context, q dbtx.Querier, d InvoiceDetail) ([]byte, error) {
	var club string
	_ = q.QueryRow(ctx, `SELECT coalesce(branding->>'appName', name) FROM platform.instance`).Scan(&club)
	doc := pdf.New()
	doc.Row(18, true, club)
	title := "Invoice / Faktur Tagihan"
	if d.Kind == "deposit" {
		title = "Down Payment Invoice / Tagihan Uang Muka"
	}
	doc.Row(12, false, title)
	doc.Space(8)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "Invoice No.", deref(d.Number))
	doc.Row(10, false, "Issue Date / Tanggal", deref(d.IssueDate))
	doc.Row(10, false, "Due Date / Jatuh Tempo", deref(d.DueDate))
	doc.Row(10, false, "Bill To / Kepada", d.BillToName)
	if d.BillToNPWP != nil {
		doc.Row(10, false, "NPWP", *d.BillToNPWP)
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	for _, l := range d.Lines {
		doc.Row(9, false, l.Description, d.Currency+" "+formatAmount(dec(l.Total), d.Currency))
	}
	doc.Space(6)
	doc.Rule(doc.Y + 10)
	doc.Row(10, false, "Subtotal", formatAmount(dec(d.Subtotal), d.Currency))
	doc.Row(10, false, "Service", formatAmount(dec(d.ServiceAmount), d.Currency))
	doc.Row(10, false, "Tax / Pajak", formatAmount(dec(d.TaxAmount), d.Currency))
	doc.Row(14, true, "Total", d.Currency+" "+formatAmount(dec(d.Total), d.Currency))
	doc.Row(10, false, "Paid / Dibayar", formatAmount(dec(d.PaidAmount), d.Currency))
	doc.Row(12, true, "Outstanding / Sisa", d.Currency+" "+formatAmount(dec(d.Outstanding), d.Currency))
	return doc.Bytes(), nil
}

// ── reminders & overdue (job) ─────────────────────────────────────────────

// InvoiceReminders marks overdue invoices (billing.invoice_overdue, once)
// and sends reminders before the due date and while overdue.
func (h *HTTP) InvoiceReminders(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	s := h.Svc
	pol, _, err := LoadCreditPolicy(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	today := localToday(ctx, tx, property, clock.Now())
	rows, err := tx.Query(ctx, `SELECT id FROM billing.invoices WHERE property_id = $1 AND status IN ('issued', 'partially_paid', 'overdue')`, property)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, iid := range ids {
		inv, err := lockInvoice(ctx, tx, iid)
		if err != nil {
			return n, err
		}
		if inv.DueDate == nil {
			continue
		}
		due, _ := time.Parse("2006-01-02", *inv.DueDate)
		days := int(due.Sub(today).Hours() / 24)
		var tag string
		switch {
		case days < 0 && inv.Status != "overdue":
			if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET status = 'overdue' WHERE id = $1`, iid); err != nil {
				return n, err
			}
			if _, err := s.Events.Publish(ctx, tx, EventInvoiceOverdue, "billing.invoice", &iid, &property, map[string]any{"invoiceId": iid,
				"number": inv.Number, "accountId": inv.AccountID, "outstanding": inv.Outstanding, "dueDate": inv.DueDate}); err != nil {
				return n, err
			}
			tag = "overdue"
		case days < 0 && pol.OverdueReminderEveryDays > 0 && (-days)%pol.OverdueReminderEveryDays == 0:
			tag = fmt.Sprintf("overdue+%d", -days)
		case days >= 0:
			for _, d := range pol.ReminderDaysBefore {
				if d == days {
					tag = fmt.Sprintf("D-%d", d)
				}
			}
		}
		if tag == "" {
			continue
		}
		var sent bool
		if err := tx.QueryRow(ctx, `SELECT $2 = ANY(reminders) FROM billing.invoices WHERE id = $1`, iid, tag).Scan(&sent); err != nil || sent {
			if err != nil {
				return n, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE billing.invoices SET reminders = array_append(reminders, $2) WHERE id = $1`, iid, tag); err != nil {
			return n, err
		}
		if err := h.sendInvoice(ctx, tx, property, inv, "billing.invoice_reminder", map[string]any{"days": days, "tag": tag}); err != nil {
			return n, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "billing", Action: "reminder", Category: audit.CategorySystem, EntityType: "billing.invoice",
			EntityID: iid.String(), EntityLabel: deref(inv.Number), PropertyID: &property, Metadata: map[string]any{"tag": tag}}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
