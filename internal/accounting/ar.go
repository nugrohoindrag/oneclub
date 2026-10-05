package accounting

// EP-18 Accounts Receivable: the AR ledger derived from the billing
// invoices (contract K2, the invoice document stays in billing): issue
// (Dr AR – Invoiced / Cr city ledger), payment allocations, credit notes
// (sales allowance, service and tax reversed proportionally), write-offs
// (against the allowance, the rest to bad debt expense) and voids, posted
// per document from the billing.invoice_* events and caught up by the
// business day close; receivables per payer, the AR Aging with the same
// figures as the operational ageing of billing (FR-AR-04) and the
// allowance for doubtful accounts through approval (FR-AR-05).

import (
	"context"
	"encoding/json"
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

// AllowanceDocumentType approves an allowance for doubtful accounts run.
var AllowanceDocumentType = provision.DocumentType{Code: "accounting_allowance", Module: "accounting", Name: "Allowance for Doubtful Accounts",
	Attributes: []provision.DocumentAttribute{{Key: "adjustment", Label: "Adjustment", Type: "number"}}}

// invoiceDoc is a billing invoice read through reporting.acc_invoices.
type invoiceDoc struct {
	ID                 uuid.UUID  `db:"id"`
	Number             *string    `db:"number"`
	Kind               string     `db:"kind"`
	AccountID          *uuid.UUID `db:"account_id"`
	CustomerID         *uuid.UUID `db:"customer_id"`
	CorporateAccountID *uuid.UUID `db:"corporate_account_id"`
	BillToName         string     `db:"bill_to_name"`
	BillToNpwp         *string    `db:"bill_to_npwp"`
	IssueDate          *time.Time `db:"issue_date"`
	DueDate            *time.Time `db:"due_date"`
	Status             string     `db:"status"`
	ServiceAmount      string     `db:"service_amount"`
	TaxAmount          string     `db:"tax_amount"`
	Total              string     `db:"total"`
	VoidedAt           *time.Time `db:"voided_at"`
}

func getInvoiceDoc(ctx context.Context, q dbtx.Querier, iid uuid.UUID) (invoiceDoc, bool, error) {
	inv, err := getOne[invoiceDoc]("invoice")(q.Query(ctx, `SELECT id, number, kind, account_id, customer_id, corporate_account_id, bill_to_name, bill_to_npwp,
		issue_date, due_date, status, service_amount::text, tax_amount::text, total::text, voided_at FROM reporting.acc_invoices WHERE id = $1`, iid))
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return inv, false, nil
		}
		return inv, false, err
	}
	return inv, true, nil
}

func invoiceIDOf(ev Ev) (uuid.UUID, error) {
	var p struct {
		InvoiceID uuid.UUID `json:"invoiceId"`
	}
	if err := ev.decode(&p); err != nil || p.InvoiceID == uuid.Nil {
		return uuid.Nil, badPayload("%s payload without invoiceId", ev.Type)
	}
	return p.InvoiceID, nil
}

// arOpen is the AR balance of an invoice in the AR ledger and whether the
// invoice is in it (issued after the cut-over or opened by the opening
// balances).
func arOpen(ctx context.Context, tx pgx.Tx, invoiceID uuid.UUID) (decimal.Decimal, bool, error) {
	var s *string
	var n int
	if err := tx.QueryRow(ctx, `SELECT sum(amount)::text, count(*) FILTER (WHERE entry_type IN ('invoice', 'opening')) FROM accounting.ar_entries
		WHERE invoice_id = $1`, invoiceID).Scan(&s, &n); err != nil {
		return decimal.Zero, false, err
	}
	return decp(s), n > 0, nil
}

func (m *Module) onInvoiceIssued(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	iid, err := invoiceIDOf(ev)
	if err != nil {
		return outcome{}, err
	}
	out, err := m.postInvoice(ctx, tx, ev.Property, iid, &ev.ID, ev.Type)
	if err != nil {
		return out, err
	}
	d := eventDate(ctx, tx, ev)
	j, missing, err := m.sweepAllocations(ctx, tx, ev.Property, sweepFilter{InvoiceID: &iid}, sweepHeader{Date: d, SourceType: ev.Type, SourceID: iid.String(),
		EventID: &ev.ID, Description: "Invoice payments"}, d)
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	return out, nil
}

// postInvoice posts the issue of an invoice (once) and drafts its output
// tax invoice.
func (m *Module) postInvoice(ctx context.Context, tx pgx.Tx, property, iid uuid.UUID, eventID *uuid.UUID, sourceType string) (outcome, error) {
	inv, ok, err := getInvoiceDoc(ctx, tx, iid)
	if err != nil || !ok {
		return outcome{Status: "no_posting", Note: "invoice not found"}, err
	}
	if inv.Status == "draft" || inv.IssueDate == nil {
		return outcome{Status: "no_posting", Note: "draft invoice"}, nil
	}
	var out outcome
	d := *inv.IssueDate
	total := dec(inv.Total)
	posted, err := isPosted(ctx, tx, "billing.invoice", iid)
	if err != nil {
		return out, err
	}
	if !posted && total.IsPositive() {
		num := deref(inv.Number)
		j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, SourceType: sourceType, SourceID: iid.String(),
			SourceRef: num, EventID: eventID, Description: "Invoice " + num + " · " + inv.BillToName},
			Sources: []sourceMark{{Type: "billing.invoice", ID: iid, BusinessDate: &d, Key1: inv.Kind, Key2: num, Amount: total,
				Items: []Item{{Source: "billing.invoice_issued", Attrs: map[string]string{"kind": inv.Kind}, Amount: total, Dims: arDims(inv.AccountID, inv.BillToName),
					Description: "Invoice " + num, SourceType: "billing.invoice", SourceID: iid.String(), FallbackDebit: "ar_control",
					FallbackCredit: "ar_unbilled"}}}}})
		if err != nil {
			return out, err
		}
		out.add(j, missing)
		if ok, err := isPosted(ctx, tx, "billing.invoice", iid); err != nil {
			return out, err
		} else if ok {
			if err := insertAREntry(ctx, tx, property, arEntry{InvoiceID: iid, InvoiceNumber: num, AccountID: inv.AccountID, CustomerID: inv.CustomerID,
				CorporateID: inv.CorporateAccountID, BillTo: inv.BillToName, Type: "invoice", Date: d, Due: inv.DueDate, Amount: total, SourceID: iid,
				Journal: journalID(j)}); err != nil {
				return out, err
			}
		}
	}
	if dec(inv.TaxAmount).IsPositive() && inv.Status != "void" && !d.Before(m.cutOver(ctx, tx, property)) {
		if err := m.createOutputTaxInvoice(ctx, tx, property, inv, d); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (m *Module) onInvoicePaid(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	iid, err := invoiceIDOf(ev)
	if err != nil {
		return outcome{}, err
	}
	d := eventDate(ctx, tx, ev)
	j, missing, err := m.sweepAllocations(ctx, tx, ev.Property, sweepFilter{InvoiceID: &iid}, sweepHeader{Date: d, SourceType: ev.Type, SourceID: iid.String(),
		EventID: &ev.ID, Description: "Invoice paid"}, d)
	var out outcome
	out.add(j, missing)
	return out, err
}

func (m *Module) onInvoiceVoided(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	iid, err := invoiceIDOf(ev)
	if err != nil {
		return outcome{}, err
	}
	return m.postVoid(ctx, tx, ev.Property, iid, &ev.ID, ev.Type)
}

// postVoid reverses what is left of a voided invoice on the AR control
// account and cancels its tax invoice.
func (m *Module) postVoid(ctx context.Context, tx pgx.Tx, property, iid uuid.UUID, eventID *uuid.UUID, sourceType string) (outcome, error) {
	inv, ok, err := getInvoiceDoc(ctx, tx, iid)
	if err != nil || !ok {
		return outcome{Status: "no_posting", Note: "invoice not found"}, err
	}
	if err := m.cancelTaxInvoice(ctx, tx, "billing.invoice", iid, "Invoice voided"); err != nil {
		return outcome{}, err
	}
	open, issued, err := arOpen(ctx, tx, iid)
	if err != nil || !issued {
		return outcome{Status: "no_posting", Note: "invoice not in the AR ledger"}, err
	}
	if posted, err := isPosted(ctx, tx, "billing.invoice_void", iid); err != nil || posted || !open.IsPositive() {
		return outcome{Status: "no_posting", Note: "nothing open to void"}, err
	}
	d := m.localDay(ctx, tx, property, inv.VoidedAt)
	num := deref(inv.Number)
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, SourceType: sourceType, SourceID: iid.String(), SourceRef: num,
		EventID: eventID, Description: "Invoice " + num + " voided"},
		Sources: []sourceMark{{Type: "billing.invoice_void", ID: iid, BusinessDate: &d, Amount: open, Items: []Item{{Source: "billing.invoice_voided",
			Attrs: map[string]string{"kind": inv.Kind}, Amount: open, Dims: arDims(inv.AccountID, inv.BillToName), Description: "Void " + num,
			SourceType: "billing.invoice", SourceID: iid.String(), FallbackDebit: "ar_unbilled", FallbackCredit: "ar_control"}}}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "billing.invoice_void", iid); ok {
		if err := insertAREntry(ctx, tx, property, arEntry{InvoiceID: iid, InvoiceNumber: num, AccountID: inv.AccountID, BillTo: inv.BillToName, Type: "void",
			Date: d, Amount: open.Neg(), SourceID: iid, Journal: journalID(j)}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (m *Module) localDay(ctx context.Context, q dbtx.Querier, property uuid.UUID, at *time.Time) time.Time {
	if at == nil {
		return localToday(ctx, q, property)
	}
	return localDate(ctx, q, property, *at)
}

func (m *Module) onCreditNote(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	var p struct {
		CreditNoteID uuid.UUID `json:"creditNoteId"`
	}
	if err := ev.decode(&p); err != nil || p.CreditNoteID == uuid.Nil {
		return outcome{}, badPayload("credit_note_issued payload")
	}
	return m.postCreditNote(ctx, tx, ev.Property, p.CreditNoteID, &ev.ID, ev.Type)
}

// postCreditNote reduces the invoiced receivable with a sales allowance
// and the service and tax of the invoice (proportional).
func (m *Module) postCreditNote(ctx context.Context, tx pgx.Tx, property, cnID uuid.UUID, eventID *uuid.UUID, sourceType string) (outcome, error) {
	var invoiceID uuid.UUID
	var number, amount, reason string
	var created time.Time
	if err := tx.QueryRow(ctx, `SELECT invoice_id, number, amount::text, coalesce(reason, ''), created_at FROM reporting.acc_credit_notes WHERE id = $1`, cnID).
		Scan(&invoiceID, &number, &amount, &reason, &created); err != nil {
		if dbtx.IsNoRows(err) {
			return outcome{Status: "no_posting", Note: "credit note not found"}, nil
		}
		return outcome{}, err
	}
	if posted, err := isPosted(ctx, tx, "billing.credit_note", cnID); err != nil || posted {
		return outcome{Status: "no_posting", Note: "already posted"}, err
	}
	if _, issued, err := arOpen(ctx, tx, invoiceID); err != nil || !issued {
		return outcome{Status: "no_posting", Note: "invoice not in the AR ledger"}, err
	}
	inv, _, err := getInvoiceDoc(ctx, tx, invoiceID)
	if err != nil {
		return outcome{}, err
	}
	cur := currency(ctx, tx)
	amt := dec(amount)
	tax, svc := decimal.Zero, decimal.Zero
	if tot := dec(inv.Total); tot.IsPositive() {
		tax = amt.Mul(dec(inv.TaxAmount)).Div(tot).Round(places(cur))
		svc = amt.Mul(dec(inv.ServiceAmount)).Div(tot).Round(places(cur))
	}
	net := amt.Sub(tax).Sub(svc)
	invNo := deref(inv.Number)
	dims := arDims(inv.AccountID, inv.BillToName)
	d := localDate(ctx, tx, property, created)
	var items []Item
	for _, x := range []struct {
		part string
		a    decimal.Decimal
	}{{"net", net}, {"service", svc}, {"tax", tax}} {
		if x.a.IsZero() {
			continue
		}
		items = append(items, Item{Source: "billing.credit_note_issued", Attrs: map[string]string{"part": x.part}, Amount: x.a, Dims: dims, DimSide: "credit",
			Description: "Credit note " + number + " (" + invNo + ")", SourceType: "billing.credit_note", SourceID: cnID.String(), FallbackCredit: "ar_control"})
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, SourceType: sourceType, SourceID: cnID.String(),
		SourceRef: number, EventID: eventID, Description: "Credit note " + number + " · " + reason},
		Sources: []sourceMark{{Type: "billing.credit_note", ID: cnID, BusinessDate: &d, Key1: invNo, Amount: amt, Items: items}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "billing.credit_note", cnID); ok {
		if err := insertAREntry(ctx, tx, property, arEntry{InvoiceID: invoiceID, InvoiceNumber: invNo, AccountID: inv.AccountID, BillTo: inv.BillToName,
			Type: "credit_note", Date: d, Amount: amt.Neg(), SourceID: cnID, Journal: journalID(j)}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (m *Module) onWrittenOff(ctx context.Context, tx pgx.Tx, ev Ev) (outcome, error) {
	iid, err := invoiceIDOf(ev)
	if err != nil {
		return outcome{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM reporting.acc_write_offs WHERE invoice_id = $1 AND status = 'approved'
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.write_off' AND s.source_id = reporting.acc_write_offs.id)
		ORDER BY decided_at`, iid)
	if err != nil {
		return outcome{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return outcome{}, err
	}
	var out outcome
	for _, wid := range ids {
		o, err := m.postWriteOff(ctx, tx, ev.Property, wid, &ev.ID, ev.Type)
		if err != nil {
			return out, err
		}
		out.Journals = append(out.Journals, o.Journals...)
		out.Missing = append(out.Missing, o.Missing...)
	}
	if len(out.Journals) > 0 {
		out.Status = "posted"
	}
	return out, nil
}

// postWriteOff writes an approved write-off off against the allowance for
// doubtful accounts, the rest to bad debt expense (FR-AR-05).
func (m *Module) postWriteOff(ctx context.Context, tx pgx.Tx, property, wid uuid.UUID, eventID *uuid.UUID, sourceType string) (outcome, error) {
	var iid uuid.UUID
	var amount string
	var decided *time.Time
	if err := tx.QueryRow(ctx, `SELECT invoice_id, amount::text, decided_at FROM reporting.acc_write_offs WHERE id = $1 AND status = 'approved'`, wid).
		Scan(&iid, &amount, &decided); err != nil {
		if dbtx.IsNoRows(err) {
			return outcome{Status: "no_posting"}, nil
		}
		return outcome{}, err
	}
	if posted, err := isPosted(ctx, tx, "billing.write_off", wid); err != nil || posted {
		return outcome{Status: "no_posting"}, err
	}
	if _, issued, err := arOpen(ctx, tx, iid); err != nil || !issued {
		return outcome{Status: "no_posting", Note: "invoice not in the AR ledger"}, err
	}
	inv, _, err := getInvoiceDoc(ctx, tx, iid)
	if err != nil {
		return outcome{}, err
	}
	amt := dec(amount)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return outcome{}, err
	}
	allowance, err := roleBalance(ctx, tx, property, cfg, "allowance_doubtful", nil)
	if err != nil {
		return outcome{}, err
	}
	covered := decimal.Min(amt, decimal.Max(allowance.Neg(), decimal.Zero)) // the allowance has a credit balance
	d := m.localDay(ctx, tx, property, decided)
	num := deref(inv.Number)
	dims := arDims(inv.AccountID, inv.BillToName)
	var items []Item
	for _, x := range []struct {
		cov string
		a   decimal.Decimal
	}{{"true", covered}, {"false", amt.Sub(covered)}} {
		if x.a.IsPositive() {
			items = append(items, Item{Source: "billing.invoice_written_off", Attrs: map[string]string{"coveredByAllowance": x.cov}, Amount: x.a, Dims: dims,
				DimSide: "credit", Description: "Write-off " + num, SourceType: "billing.write_off", SourceID: wid.String(), FallbackCredit: "ar_control"})
		}
	}
	var out outcome
	j, missing, err := m.postSources(ctx, tx, posting{Entry: Entry{Property: property, Date: d, SourceType: sourceType, SourceID: wid.String(), SourceRef: num,
		EventID: eventID, Description: "Invoice " + num + " written off"},
		Sources: []sourceMark{{Type: "billing.write_off", ID: wid, BusinessDate: &d, Key1: num, Amount: amt, Items: items}}})
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if ok, _ := isPosted(ctx, tx, "billing.write_off", wid); ok {
		if err := insertAREntry(ctx, tx, property, arEntry{InvoiceID: iid, InvoiceNumber: num, AccountID: inv.AccountID, BillTo: inv.BillToName,
			Type: "write_off", Date: d, Amount: amt.Neg(), SourceID: wid, Journal: journalID(j)}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// sweepAR catches up the AR documents of the property dated up to a day
// that their events did not post (FR-PST-04: no event is lost).
func (m *Module) sweepAR(ctx context.Context, tx pgx.Tx, property uuid.UUID, upTo time.Time, h sweepHeader) (outcome, error) {
	var out outcome
	cut := m.cutOver(ctx, tx, property)
	merge := func(o outcome) {
		out.Journals = append(out.Journals, o.Journals...)
		out.Missing = append(out.Missing, o.Missing...)
	}
	ids := func(sql string, args ...any) ([]uuid.UUID, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	}
	invs, err := ids(`SELECT i.id FROM reporting.acc_invoices i WHERE i.property_id = $1 AND i.issue_date BETWEEN $2 AND $3 AND i.status <> 'draft'
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.invoice' AND s.source_id = i.id) ORDER BY i.issue_date LIMIT 2000`,
		property, cut, dateOnly(upTo))
	if err != nil {
		return out, err
	}
	for _, iid := range invs {
		o, err := m.postInvoice(ctx, tx, property, iid, h.EventID, h.SourceType)
		if err != nil {
			return out, err
		}
		merge(o)
	}
	cns, err := ids(`SELECT c.id FROM reporting.acc_credit_notes c WHERE c.property_id = $1 AND c.created_at >= $2 AND c.created_at < $3::date + 1
		AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.credit_note' AND s.source_id = c.id) LIMIT 2000`,
		property, cut, dateOnly(upTo))
	if err != nil {
		return out, err
	}
	for _, cid := range cns {
		o, err := m.postCreditNote(ctx, tx, property, cid, h.EventID, h.SourceType)
		if err != nil {
			return out, err
		}
		merge(o)
	}
	wos, err := ids(`SELECT w.id FROM reporting.acc_write_offs w WHERE w.property_id = $1 AND w.status = 'approved' AND w.decided_at >= $2
		AND w.decided_at < $3::date + 1 AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.write_off' AND s.source_id = w.id)
		LIMIT 2000`, property, cut, dateOnly(upTo))
	if err != nil {
		return out, err
	}
	for _, wid := range wos {
		o, err := m.postWriteOff(ctx, tx, property, wid, h.EventID, h.SourceType)
		if err != nil {
			return out, err
		}
		merge(o)
	}
	voids, err := ids(`SELECT DISTINCT e.invoice_id FROM accounting.ar_entries e JOIN reporting.acc_invoices i ON i.id = e.invoice_id
		WHERE e.property_id = $1 AND i.status = 'void' AND NOT EXISTS (SELECT 1 FROM accounting.posted_sources s WHERE s.source_type = 'billing.invoice_void'
		AND s.source_id = i.id) LIMIT 2000`, property)
	if err != nil {
		return out, err
	}
	for _, iid := range voids {
		o, err := m.postVoid(ctx, tx, property, iid, h.EventID, h.SourceType)
		if err != nil {
			return out, err
		}
		merge(o)
	}
	j, missing, err := m.sweepAllocations(ctx, tx, property, sweepFilter{}, h, upTo)
	if err != nil {
		return out, err
	}
	out.add(j, missing)
	if len(out.Journals) > 0 {
		out.Status = "posted"
	}
	return out, nil
}

// ── receivables & AR aging ────────────────────────────────────────────────

// ARReceivable is the AR balance of one payer.
type ARReceivable struct {
	AccountID          *uuid.UUID `json:"accountId" db:"account_id"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	BillToName         string     `json:"billToName" db:"bill_to_name"`
	Invoiced           string     `json:"invoiced" db:"invoiced"`
	Paid               string     `json:"paid" db:"paid"`
	Credited           string     `json:"credited" db:"credited"`
	Balance            string     `json:"balance" db:"balance"`
	OpenInvoices       int        `json:"openInvoices" db:"open_invoices"`
	Overdue            string     `json:"overdue" db:"overdue"`
	CreditLimit        *string    `json:"creditLimit" db:"credit_limit"`
	AccountBalance     *string    `json:"accountBalance" db:"account_balance" doc:"Balance of the billing member/corporate account (city ledger incl. unbilled)"`
}

// ListReceivables lists the AR balance per payer from the AR ledger.
func ListReceivables(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]ARReceivable, error) {
	return handle.List[ARReceivable](q.Query(ctx, `WITH inv AS (SELECT e.invoice_id, min(e.account_id::text)::uuid AS account_id,
		  min(e.corporate_account_id::text)::uuid AS corporate_account_id, min(e.customer_id::text)::uuid AS customer_id, min(e.bill_to_name) AS bill_to_name,
		  sum(e.amount) FILTER (WHERE e.entry_type IN ('invoice', 'opening')) AS invoiced, -coalesce(sum(e.amount) FILTER (WHERE e.entry_type = 'payment'), 0) AS paid,
		  -coalesce(sum(e.amount) FILTER (WHERE e.entry_type IN ('credit_note', 'write_off', 'void')), 0) AS credited, sum(e.amount) AS balance,
		  min(e.due_date) AS due_date FROM accounting.ar_entries e WHERE e.property_id = $1 GROUP BY e.invoice_id)
		SELECT i.account_id, min(i.corporate_account_id::text)::uuid AS corporate_account_id, min(i.customer_id::text)::uuid AS customer_id,
		  coalesce(min(a.name), min(i.bill_to_name), '—') AS bill_to_name, trim_scale(coalesce(sum(i.invoiced), 0))::text AS invoiced,
		  trim_scale(sum(i.paid))::text AS paid, trim_scale(sum(i.credited))::text AS credited, trim_scale(sum(i.balance))::text AS balance,
		  count(*) FILTER (WHERE i.balance > 0)::int AS open_invoices,
		  trim_scale(coalesce(sum(i.balance) FILTER (WHERE i.balance > 0 AND i.due_date < current_date), 0))::text AS overdue,
		  trim_scale(min(a.credit_limit))::text AS credit_limit, trim_scale(min(a.balance))::text AS account_balance
		FROM inv i LEFT JOIN reporting.acc_ar_accounts a ON a.id = i.account_id GROUP BY i.account_id HAVING sum(i.balance) <> 0 OR sum(i.invoiced) <> 0
		ORDER BY 4 LIMIT 2000`, property))
}

// AREntry is one movement of the AR ledger.
type AREntry struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	InvoiceID     *uuid.UUID `json:"invoiceId" db:"invoice_id"`
	InvoiceNumber *string    `json:"invoiceNumber" db:"invoice_number"`
	AccountID     *uuid.UUID `json:"accountId" db:"account_id"`
	BillToName    *string    `json:"billToName" db:"bill_to_name"`
	EntryType     string     `json:"entryType" db:"entry_type" enum:"invoice,payment,credit_note,write_off,void,opening"`
	EntryDate     string     `json:"entryDate" db:"entry_date"`
	DueDate       *string    `json:"dueDate" db:"due_date"`
	Amount        string     `json:"amount" db:"amount"`
	JournalID     *uuid.UUID `json:"journalId" db:"journal_id"`
}

// ListAREntries lists the AR ledger of an invoice or a payer.
func ListAREntries(ctx context.Context, q dbtx.Querier, property uuid.UUID, invoice, account *uuid.UUID) ([]AREntry, error) {
	return handle.List[AREntry](q.Query(ctx, `SELECT id, invoice_id, invoice_number, account_id, bill_to_name, entry_type, to_char(entry_date, 'YYYY-MM-DD') AS entry_date,
		to_char(due_date, 'YYYY-MM-DD') AS due_date, trim_scale(amount)::text AS amount, journal_id FROM accounting.ar_entries WHERE property_id = $1
		AND ($2::uuid IS NULL OR invoice_id = $2) AND ($3::uuid IS NULL OR account_id = $3) ORDER BY entry_date, created_at LIMIT 2000`, property, invoice, account))
}

// ARAgingRow is the open receivable of one payer by age since the invoice
// date (0–30, 31–60, 61–90, > 90 days).
type ARAgingRow struct {
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

// ARAging is the AR Aging at a date.
type ARAging struct {
	AsOf       string       `json:"asOf"`
	Rows       []ARAgingRow `json:"rows"`
	Totals     ARAgingRow   `json:"totals"`
	ARControl  string       `json:"arControl" doc:"Balance of the AR control account at the date"`
	Difference string       `json:"difference" doc:"AR control − aging total (0 when reconciled, FR-AR-06)"`
}

// ARAgingAsOf ages the open billing invoices exactly like the operational
// ageing of billing (FR-AR-04) and compares the AR control account.
func ARAgingAsOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time) (ARAging, error) {
	day := ymd(asOf)
	rows, err := handle.List[ARAgingRow](q.Query(ctx, `WITH open AS (`+arOpenAsOf+`)
		SELECT account_id, corporate_account_id, customer_id, min(bill_to_name) AS bill_to_name, count(*)::int AS invoices,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date <= 30), 0))::text AS d0_30,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 31 AND 60), 0))::text AS d31_60,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 61 AND 90), 0))::text AS d61_90,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date > 90), 0))::text AS d90,
		  trim_scale(sum(open_amount))::text AS total,
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE due_date < $2::date), 0))::text AS overdue
		FROM open WHERE open_amount > 0 GROUP BY account_id, corporate_account_id, customer_id ORDER BY min(bill_to_name)`, property, day))
	if err != nil {
		return ARAging{}, err
	}
	t := ARAgingRow{BillToName: "Total"}
	s := make([]decimal.Decimal, 6)
	for _, r := range rows {
		t.Invoices += r.Invoices
		for i, v := range []string{r.Days0to30, r.Days31to60, r.Days61to90, r.Over90, r.Total, r.Overdue} {
			s[i] = s[i].Add(dec(v))
		}
	}
	t.Days0to30, t.Days31to60, t.Days61to90, t.Over90, t.Total, t.Overdue = s[0].String(), s[1].String(), s[2].String(), s[3].String(), s[4].String(), s[5].String()
	cfg, err := LoadConfiguration(ctx, q, property)
	if err != nil {
		return ARAging{}, err
	}
	ctl, err := roleBalance(ctx, q, property, cfg, "ar_control", &asOf)
	if err != nil {
		return ARAging{}, err
	}
	return ARAging{AsOf: day, Rows: rows, Totals: t, ARControl: ctl.String(), Difference: ctl.Sub(s[4]).String()}, nil
}

// ── allowance for doubtful accounts (FR-AR-05) ────────────────────────────

// ARAllowanceInput computes the allowance at a date.
type ARAllowanceInput struct {
	AsOf  string `json:"asOf"`
	Notes string `json:"notes,omitempty"`
}

// ARAllowanceBucket is the provision of one ageing bucket.
type ARAllowanceBucket struct {
	Bucket   string `json:"bucket"`
	Open     string `json:"open"`
	Rate     string `json:"rate"`
	Required string `json:"required"`
}

// ARAllowanceRun is an allowance computation.
type ARAllowanceRun struct {
	ID                uuid.UUID           `json:"id" db:"id"`
	Number            string              `json:"number" db:"number"`
	AsOf              string              `json:"asOf" db:"as_of"`
	Buckets           []ARAllowanceBucket `json:"buckets" db:"buckets"`
	Required          string              `json:"required" db:"required"`
	CurrentBalance    string              `json:"currentBalance" db:"current_balance"`
	Adjustment        string              `json:"adjustment" db:"adjustment"`
	Status            string              `json:"status" db:"status" enum:"pending_approval,posted,rejected,no_change"`
	ApprovalRequestID *uuid.UUID          `json:"approvalRequestId" db:"approval_request_id"`
	JournalID         *uuid.UUID          `json:"journalId" db:"journal_id"`
	Notes             *string             `json:"notes" db:"notes"`
	CreatedAt         time.Time           `json:"createdAt" db:"created_at"`
}

const allowanceSelect = `SELECT id, number, to_char(as_of, 'YYYY-MM-DD') AS as_of, buckets, trim_scale(required)::text AS required,
	trim_scale(current_balance)::text AS current_balance, trim_scale(adjustment)::text AS adjustment, status, approval_request_id, journal_id, notes, created_at
	FROM accounting.allowance_runs`

// ListAllowanceRuns lists the allowance runs of a property.
func ListAllowanceRuns(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]ARAllowanceRun, error) {
	return handle.List[ARAllowanceRun](q.Query(ctx, allowanceSelect+` WHERE property_id = $1 ORDER BY created_at DESC LIMIT 200`, property))
}

// CreateAllowanceRun computes the required allowance from the AR aging
// and the rates of the policy; the adjustment is posted after approval.
func (m *Module) CreateAllowanceRun(ctx context.Context, tx pgx.Tx, property uuid.UUID, in ARAllowanceInput) (ARAllowanceRun, error) {
	d, err := parseDate("asOf", in.AsOf)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	pol, err := loadAllowancePolicy(ctx, tx, property)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	ag, err := ARAgingAsOf(ctx, tx, property, d)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	cur := currency(ctx, tx)
	required := decimal.Zero
	var buckets []ARAllowanceBucket
	for _, b := range []struct{ name, open, rate string }{{"0-30", ag.Totals.Days0to30, pol.Rate0to30}, {"31-60", ag.Totals.Days31to60, pol.Rate31to60},
		{"61-90", ag.Totals.Days61to90, pol.Rate61to90}, {">90", ag.Totals.Over90, pol.RateOver90}} {
		req := dec(b.open).Mul(dec(b.rate)).Div(decimal.NewFromInt(100)).Round(places(cur))
		required = required.Add(req)
		buckets = append(buckets, ARAllowanceBucket{Bucket: b.name, Open: dec(b.open).String(), Rate: dec(b.rate).String(), Required: req.String()})
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	bal, err := roleBalance(ctx, tx, property, cfg, "allowance_doubtful", &d)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	current := bal.Neg()
	adj := required.Sub(current)
	rid := id.New()
	num, err := numbering.Next(ctx, tx, property, "ALW", d)
	if err != nil {
		return ARAllowanceRun{}, err
	}
	status := "pending_approval"
	if adj.IsZero() {
		status = "no_change"
	}
	raw, _ := json.Marshal(buckets)
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.allowance_runs (id, property_id, number, as_of, buckets, required, current_balance, adjustment, status, notes,
		created_by) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8::numeric,$9,$10,$11)`, rid, property, num, d, raw, required.String(), current.String(),
		adj.String(), status, nz(in.Notes), actorPtr(ctx)); err != nil {
		return ARAllowanceRun{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.allowance_run", EntityID: rid.String(),
		EntityLabel: num, PropertyID: &property, After: map[string]any{"asOf": ymd(d), "required": required.String(), "adjustment": adj.String()}}); err != nil {
		return ARAllowanceRun{}, err
	}
	if status == "pending_approval" {
		if !pol.RequireApproval || m.Approvals == nil {
			if err := m.postAllowance(ctx, tx, property, rid); err != nil {
				return ARAllowanceRun{}, err
			}
		} else {
			req, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: AllowanceDocumentType.Code, DocumentID: rid, DocumentRef: num,
				Title: "Allowance for doubtful accounts " + num + " · adjustment " + adj.String(), PropertyID: property,
				Attributes: map[string]any{"adjustment": adj.InexactFloat64()}})
			if err != nil {
				return ARAllowanceRun{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE accounting.allowance_runs SET approval_request_id = $2 WHERE id = $1`, rid, req); err != nil {
				return ARAllowanceRun{}, err
			}
		}
	}
	return getOne[ARAllowanceRun]("allowance run")(tx.Query(ctx, allowanceSelect+` WHERE id = $1`, rid))
}

func (m *Module) postAllowance(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) error {
	r, err := getOne[ARAllowanceRun]("allowance run")(tx.Query(ctx, allowanceSelect+` WHERE id = $1 AND status = 'pending_approval'`, rid))
	if err != nil {
		return err
	}
	adj := dec(r.Adjustment)
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return err
	}
	exp, err1 := accountIDByCode(ctx, tx, cfg.Accounts["bad_debt_expense"])
	alw, err2 := accountIDByCode(ctx, tx, cfg.Accounts["allowance_doubtful"])
	if err1 != nil || err2 != nil {
		return errs.Conflict("missing_account", "bad debt expense or allowance account not found")
	}
	lines := []Line{{AccountID: exp, Debit: adj, Description: "Allowance " + r.Number}, {AccountID: alw, Credit: adj, Description: "Allowance " + r.Number}}
	if adj.IsNegative() {
		lines = []Line{{AccountID: alw, Debit: adj.Neg(), Description: "Allowance release " + r.Number},
			{AccountID: exp, Credit: adj.Neg(), Description: "Allowance release " + r.Number}}
	}
	d, _ := time.Parse("2006-01-02", r.AsOf)
	if err := lockProperty(ctx, tx, property); err != nil {
		return err
	}
	j, err := m.post(ctx, tx, Entry{Property: property, Date: d, Type: "adjustment", SourceType: "accounting.allowance_run", SourceID: rid.String(),
		SourceRef: r.Number, Description: "Allowance for doubtful accounts " + r.Number, Lines: lines}, false)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE accounting.allowance_runs SET status = 'posted', journal_id = $2 WHERE id = $1`, rid, j.ID)
	return err
}

// AllowanceDecision applies the approval decision of an allowance run.
func (m *Module) AllowanceDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	switch d.Status {
	case approval.StatusApproved:
		return m.postAllowance(ctx, tx, d.PropertyID, d.DocumentID)
	case approval.StatusRejected, approval.StatusCancelled:
		_, err := tx.Exec(ctx, `UPDATE accounting.allowance_runs SET status = 'rejected' WHERE id = $1 AND status = 'pending_approval'`, d.DocumentID)
		return err
	}
	return nil
}
