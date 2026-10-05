package accounting

// P4 gap fixes, Finance UX (read endpoints only; posting is untouched):
//   - FR-FIN-04 drill-down: the source documents of a journal and of every
//     general ledger line resolved to their document (folio, invoice, PO,
//     goods receipt, vendor invoice, stock movement, journal …) with number
//     and the related document, so the screens link documents instead of
//     showing raw ids;
//   - §7.1 / §7.3 (FR-REV-07): the e-Faktur number and status of billing
//     invoices for the Back Office Invoices screen and the Member App;
//   - FR-TRS-03: the parallel-run comparison of the OneClub Trial Balance
//     with the Excel Finance trial balance (per mapped account).
// Documents of other modules are read from their reporting views
// (Technical Doc §4.2 #3: no import of lower-layer modules).

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// AccountingSourceDocument is a source document of a journal (drill-down).
type AccountingSourceDocument struct {
	SourceType   string `json:"sourceType"`
	SourceID     string `json:"sourceId"`
	DocumentType string `json:"documentType" enum:"folio,invoice,credit_note,write_off,payment,refund,deposit,payout,cashier_shift,purchase_order,goods_receipt,purchase_return,vendor_invoice,stock_movement,journal,manual_journal,recurring_journal,bank_transaction,cash_transaction,vendor_bill,vendor_payment,payment_run,opening_balance,other"`
	// DocumentID opens the document screen (absent when unknown).
	DocumentID    *uuid.UUID `json:"documentId"`
	Number        *string    `json:"number"`
	RelatedType   *string    `json:"relatedType,omitempty" doc:"A related document (e.g. the PO of a goods receipt, the invoice of a credit note)"`
	RelatedID     *uuid.UUID `json:"relatedId,omitempty"`
	RelatedNumber *string    `json:"relatedNumber,omitempty"`
}

// AccountingLedgerLineDocuments are the source documents of a general
// ledger line: the line's own source, else the journal's.
type AccountingLedgerLineDocuments struct {
	LineID        uuid.UUID                  `json:"lineId"`
	JournalID     uuid.UUID                  `json:"journalId"`
	Documents     []AccountingSourceDocument `json:"documents"`
	MoreDocuments int                        `json:"moreDocuments" doc:"Further source documents of the journal not listed"`
}

// srcRef is a (source type, source id) pair.
type srcRef struct{ Type, ID string }

// docSource resolves one source type to its documents: the query returns
// key, document id, number, related type, related id, related number for
// the keys in $1 (uuid[]).
type docSource struct {
	DocType string
	SQL     string
}

var docSources = map[string]docSource{
	"billing.folio_line": {"folio", `SELECT l.id, l.folio_id, l.folio_number, CASE WHEN l.invoice_id IS NOT NULL THEN 'invoice' END, l.invoice_id,
		(SELECT i.number FROM reporting.acc_invoices i WHERE i.id = l.invoice_id) FROM reporting.acc_folio_lines l WHERE l.id = ANY($1)`},
	"billing.invoice": {"invoice", `SELECT i.id, i.id, i.number, CASE WHEN i.folio_id IS NOT NULL THEN 'folio' END, i.folio_id, NULL::text
		FROM reporting.acc_invoices i WHERE i.id = ANY($1)`},
	"billing.credit_note": {"credit_note", `SELECT c.id, c.id, c.number, 'invoice', c.invoice_id, i.number
		FROM reporting.acc_credit_notes c LEFT JOIN reporting.acc_invoices i ON i.id = c.invoice_id WHERE c.id = ANY($1)`},
	"billing.write_off": {"write_off", `SELECT w.id, w.id, w.number, 'invoice', w.invoice_id, i.number
		FROM reporting.acc_write_offs w LEFT JOIN reporting.acc_invoices i ON i.id = w.invoice_id WHERE w.id = ANY($1)`},
	"billing.allocation": {"payment", `SELECT a.id, a.payment_id, a.payment_number, CASE WHEN a.invoice_id IS NOT NULL THEN 'invoice' END, a.invoice_id, i.number
		FROM reporting.acc_allocations a LEFT JOIN reporting.acc_invoices i ON i.id = a.invoice_id WHERE a.id = ANY($1)`},
	"billing.payment": {"payment", `SELECT p.id, p.id, p.number, CASE WHEN p.folio_id IS NOT NULL THEN 'folio' END, p.folio_id, NULL::text
		FROM reporting.acc_payments p WHERE p.id = ANY($1)`},
	"billing.refund": {"refund", `SELECT r.id, r.id, r.number, 'payment', r.payment_id, r.payment_number FROM reporting.acc_refunds r WHERE r.id = ANY($1)`},
	"billing.deposit": {"deposit", `SELECT d.id, d.id, d.number, CASE WHEN d.folio_id IS NOT NULL THEN 'folio' END, d.folio_id, NULL::text
		FROM reporting.acc_deposits d WHERE d.id = ANY($1)`},
	"billing.payout":         {"payout", `SELECT o.id, o.id, o.number, NULL::text, NULL::uuid, NULL::text FROM reporting.acc_payouts o WHERE o.id = ANY($1)`},
	"billing.shift_variance": {"cashier_shift", `SELECT s.id, s.id, s.number, NULL::text, NULL::uuid, NULL::text FROM reporting.acc_cashier_shifts s WHERE s.id = ANY($1)`},
	"procurement.purchase_order": {"purchase_order", `SELECT o.purchase_order_id, o.purchase_order_id, o.number, NULL::text, NULL::uuid, NULL::text
		FROM reporting.procurement_orders o WHERE o.purchase_order_id = ANY($1)`},
	"procurement.goods_receipt": {"goods_receipt", `SELECT DISTINCT ON (g.goods_receipt_id) g.goods_receipt_id, g.goods_receipt_id, g.number,
		CASE WHEN g.purchase_order_id IS NOT NULL THEN 'purchase_order' END, g.purchase_order_id, g.po_number
		FROM reporting.procurement_receipt_lines g WHERE g.goods_receipt_id = ANY($1) ORDER BY g.goods_receipt_id`},
	"procurement.purchase_return": {"purchase_return", `SELECT DISTINCT ON (r.purchase_return_id) r.purchase_return_id, r.purchase_return_id, r.number,
		NULL::text, NULL::uuid, NULL::text FROM reporting.procurement_return_lines r WHERE r.purchase_return_id = ANY($1) ORDER BY r.purchase_return_id`},
	"procurement.vendor_invoice": {"vendor_invoice", `SELECT v.vendor_invoice_id, v.vendor_invoice_id, v.number, NULL::text, NULL::uuid, v.supplier_invoice_no
		FROM reporting.procurement_vendor_invoices v WHERE v.vendor_invoice_id = ANY($1)`},
	"inventory.movement": {"stock_movement", `SELECT DISTINCT ON (m.movement_id) m.movement_id, m.movement_id, m.number, NULL::text, NULL::uuid, NULL::text
		FROM reporting.inventory_movement_lines m WHERE m.movement_id = ANY($1) ORDER BY m.movement_id`},
	"accounting.journal": {"journal", `SELECT j.id, j.id, j.number, NULL::text, NULL::uuid, NULL::text FROM accounting.journals j WHERE j.id = ANY($1)`},
	"accounting.manual_journal": {"manual_journal", `SELECT m.id, m.id, m.number, NULL::text, NULL::uuid, NULL::text
		FROM accounting.manual_journals m WHERE m.id = ANY($1)`},
	"accounting.recurring_journal": {"recurring_journal", `SELECT r.id, r.id, r.code, NULL::text, NULL::uuid, NULL::text
		FROM accounting.recurring_journals r WHERE r.id = ANY($1)`},
	"accounting.bank_transaction": {"bank_transaction", `SELECT t.id, t.id, coalesce(t.reference, t.description), NULL::text, NULL::uuid, NULL::text
		FROM accounting.bank_transactions t WHERE t.id = ANY($1)`},
	"accounting.cash_transaction": {"cash_transaction", `SELECT c.id, c.id, c.number, NULL::text, NULL::uuid, NULL::text
		FROM accounting.cash_transactions c WHERE c.id = ANY($1)`},
	"accounting.vendor_bill": {"vendor_bill", `SELECT k.key, a.id, a.number, NULL::text, NULL::uuid, a.supplier_invoice_no
		FROM unnest($1::uuid[]) k(key) JOIN accounting.ap_items a ON a.id = k.key OR (a.source_id = k.key AND a.item_type = 'vendor_bill')`},
	"accounting.vendor_payment": {"vendor_payment", `SELECT p.id, p.id, p.number, NULL::text, NULL::uuid, NULL::text
		FROM accounting.vendor_payments p WHERE p.id = ANY($1)`},
	"accounting.payment_run": {"payment_run", `SELECT r.id, r.id, r.number, NULL::text, NULL::uuid, NULL::text FROM accounting.payment_runs r WHERE r.id = ANY($1)`},
	"accounting.opening_balance": {"opening_balance", `SELECT o.id, o.id, o.number, NULL::text, NULL::uuid, NULL::text
		FROM accounting.opening_balances o WHERE o.id = ANY($1)`},
}

// docSourceOf finds the resolver of a source type: the exact type, else
// the longest document type an event type starts with
// ("procurement.goods_receipt_posted" → "procurement.goods_receipt").
func docSourceOf(typ string) (string, bool) {
	if _, ok := docSources[typ]; ok {
		return typ, true
	}
	if typ == "billing.deposit_application" {
		return "billing.deposit", true
	}
	best := ""
	for k := range docSources {
		if strings.HasPrefix(typ, k+"_") && len(k) > len(best) {
			best = k
		}
	}
	return best, best != ""
}

// resolveDocuments resolves source references to their documents.
func resolveDocuments(ctx context.Context, q dbtx.Querier, refs []srcRef) (map[srcRef]AccountingSourceDocument, error) {
	out := make(map[srcRef]AccountingSourceDocument, len(refs))
	byType := map[string][]uuid.UUID{}
	for _, r := range refs {
		out[r] = AccountingSourceDocument{SourceType: r.Type, SourceID: r.ID, DocumentType: "other"}
		if k, ok := docSourceOf(r.Type); ok {
			if u, err := uuid.Parse(r.ID); err == nil {
				byType[k] = append(byType[k], u)
			}
		}
	}
	types := make([]string, 0, len(byType))
	for k := range byType {
		types = append(types, k)
	}
	sort.Strings(types)
	found := map[string]map[uuid.UUID]AccountingSourceDocument{}
	for _, k := range types {
		src := docSources[k]
		rows, err := q.Query(ctx, src.SQL, byType[k])
		if err != nil {
			return nil, err
		}
		m := map[uuid.UUID]AccountingSourceDocument{}
		for rows.Next() {
			var key uuid.UUID
			d := AccountingSourceDocument{DocumentType: src.DocType}
			if err := rows.Scan(&key, &d.DocumentID, &d.Number, &d.RelatedType, &d.RelatedID, &d.RelatedNumber); err != nil {
				rows.Close()
				return nil, err
			}
			m[key] = d
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		found[k] = m
	}
	for r := range out {
		k, ok := docSourceOf(r.Type)
		if !ok {
			continue
		}
		u, err := uuid.Parse(r.ID)
		if err != nil {
			continue
		}
		if d, ok := found[k][u]; ok {
			d.SourceType, d.SourceID = r.Type, r.ID
			out[r] = d
		}
	}
	return out, nil
}

// uniqueDocs keeps one entry per resolved document, in the order of refs.
func uniqueDocs(refs []srcRef, docs map[srcRef]AccountingSourceDocument) []AccountingSourceDocument {
	out := []AccountingSourceDocument{}
	seen := map[string]bool{}
	for _, r := range refs {
		d := docs[r]
		key := d.DocumentType + ":" + r.Type + ":" + r.ID
		if d.DocumentID != nil {
			key = d.DocumentType + ":" + d.DocumentID.String()
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	return out
}

// JournalDocuments returns the source documents of a journal: its lines'
// sources, the posted source documents, the journal's own source and the
// journal it reverses.
func JournalDocuments(ctx context.Context, q dbtx.Querier, property, jid uuid.UUID) ([]AccountingSourceDocument, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM accounting.journals WHERE id = $1 AND property_id = $2`, jid, property).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errs.NotFound("journal")
	}
	rows, err := q.Query(ctx, `SELECT t, i FROM (
		SELECT 1 AS o, l.line_no AS n, l.source_type AS t, l.source_id AS i FROM accounting.journal_lines l
		  WHERE l.journal_id = $1 AND l.source_type IS NOT NULL AND l.source_id IS NOT NULL
		UNION ALL SELECT 2, 0, s.source_type, s.source_id::text FROM accounting.posted_sources s WHERE s.journal_id = $1
		UNION ALL SELECT 3, 0, j.source_type, j.source_id FROM accounting.journals j WHERE j.id = $1 AND j.source_id IS NOT NULL
		UNION ALL SELECT 4, 0, 'accounting.journal', j.reverses_journal_id::text FROM accounting.journals j WHERE j.id = $1 AND j.reverses_journal_id IS NOT NULL
		) x ORDER BY o, n LIMIT 500`, jid)
	if err != nil {
		return nil, err
	}
	var refs []srcRef
	seen := map[srcRef]bool{}
	for rows.Next() {
		var r srcRef
		if err := rows.Scan(&r.Type, &r.ID); err != nil {
			rows.Close()
			return nil, err
		}
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	docs, err := resolveDocuments(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	// a source resolved to a document hides the unresolved event header
	all := uniqueDocs(refs, docs)
	known := false
	for _, d := range all {
		known = known || d.DocumentID != nil
	}
	if !known {
		return all, nil
	}
	out := []AccountingSourceDocument{}
	for _, d := range all {
		if d.DocumentID != nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// LedgerDocuments returns the source documents of the general ledger lines
// of an account (same filter as GeneralLedgerOf).
func LedgerDocuments(ctx context.Context, q dbtx.Querier, accountID uuid.UUID, f LedgerFilter) ([]AccountingLedgerLineDocuments, error) {
	var prop any
	if f.Property != nil {
		prop = *f.Property
	}
	type lineSrc struct {
		Line, Journal        uuid.UUID
		LineType, LineID     *string
		HeaderType, HeaderID *string
		Reverses             *uuid.UUID
	}
	rows, err := q.Query(ctx, `SELECT l.id, l.journal_id, l.source_type, l.source_id, j.source_type, j.source_id, j.reverses_journal_id
		FROM accounting.journal_lines l JOIN accounting.journals j ON j.id = l.journal_id
		WHERE l.account_id = $1 AND ($2::uuid IS NULL OR l.property_id = $2) AND l.journal_date BETWEEN $3 AND $4
		AND ($5 = '' OR l.business_line = $5) AND ($6 = '' OR l.cost_center = $6) AND ($7::uuid IS NULL OR l.department_id = $7)
		ORDER BY l.journal_date, j.posted_at, j.number, l.line_no LIMIT 20000`, accountID, prop, dateOnly(f.From), dateOnly(f.To), f.BusinessLine, f.CostCenter,
		f.DepartmentID)
	if err != nil {
		return nil, err
	}
	var lines []lineSrc
	for rows.Next() {
		var x lineSrc
		if err := rows.Scan(&x.Line, &x.Journal, &x.LineType, &x.LineID, &x.HeaderType, &x.HeaderID, &x.Reverses); err != nil {
			rows.Close()
			return nil, err
		}
		lines = append(lines, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// journal-level sources (first three per journal) for lines without their own
	var journals []uuid.UUID
	seenJ := map[uuid.UUID]bool{}
	for _, x := range lines {
		if (x.LineType == nil || x.LineID == nil) && !seenJ[x.Journal] {
			seenJ[x.Journal] = true
			journals = append(journals, x.Journal)
		}
	}
	posted := map[uuid.UUID][]srcRef{}
	more := map[uuid.UUID]int{}
	if len(journals) > 0 {
		rows, err := q.Query(ctx, `SELECT journal_id, source_type, source_id::text, total FROM (
			SELECT s.journal_id, s.source_type, s.source_id, count(*) OVER (PARTITION BY s.journal_id) AS total,
			  row_number() OVER (PARTITION BY s.journal_id ORDER BY s.source_type, s.source_id) AS rn
			FROM accounting.posted_sources s WHERE s.journal_id = ANY($1)) x WHERE rn <= 3`, journals)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var j uuid.UUID
			var r srcRef
			var total int
			if err := rows.Scan(&j, &r.Type, &r.ID, &total); err != nil {
				rows.Close()
				return nil, err
			}
			posted[j] = append(posted[j], r)
			more[j] = total - len(posted[j])
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	refsOf := make([][]srcRef, len(lines))
	var all []srcRef
	for i, x := range lines {
		switch {
		case x.LineType != nil && x.LineID != nil:
			refsOf[i] = []srcRef{{*x.LineType, *x.LineID}}
		case len(posted[x.Journal]) > 0:
			refsOf[i] = posted[x.Journal]
		case x.Reverses != nil:
			refsOf[i] = []srcRef{{"accounting.journal", x.Reverses.String()}}
		case x.HeaderType != nil && x.HeaderID != nil:
			refsOf[i] = []srcRef{{*x.HeaderType, *x.HeaderID}}
		}
		all = append(all, refsOf[i]...)
	}
	docs, err := resolveDocuments(ctx, q, all)
	if err != nil {
		return nil, err
	}
	out := make([]AccountingLedgerLineDocuments, len(lines))
	for i, x := range lines {
		out[i] = AccountingLedgerLineDocuments{LineID: x.Line, JournalID: x.Journal, Documents: uniqueDocs(refsOf[i], docs)}
		if x.LineType == nil || x.LineID == nil {
			out[i].MoreDocuments = more[x.Journal]
		}
	}
	return out, nil
}

// ── e-Faktur on invoices (§7.1 / §7.3) ────────────────────────────────────

// InvoiceEFakturStatus is the e-Faktur (output tax invoice) of an invoice.
type InvoiceEFakturStatus struct {
	InvoiceID     uuid.UUID  `json:"invoiceId" db:"invoice_id"`
	InvoiceNumber *string    `json:"invoiceNumber" db:"invoice_number"`
	TaxInvoiceID  uuid.UUID  `json:"taxInvoiceId" db:"tax_invoice_id"`
	FakturNumber  *string    `json:"fakturNumber" db:"faktur_number" doc:"Nomor seri faktur pajak (after upload to Coretax)"`
	Status        string     `json:"status" db:"status" enum:"draft,exported,uploaded,cancelled"`
	TaxPeriod     string     `json:"taxPeriod" db:"tax_period"`
	UploadedAt    *time.Time `json:"uploadedAt" db:"uploaded_at"`
}

const efakturSelect = `SELECT e.invoice_id, e.invoice_number, e.tax_invoice_id, e.faktur_number, e.status, e.tax_period, e.uploaded_at
	FROM reporting.billing_invoice_efaktur e`

// ── Excel Finance trial balance comparison (FR-TRS-03) ────────────────────

// ExcelTrialBalanceInput is the Excel Finance trial balance of a period.
type ExcelTrialBalanceInput struct {
	From       string     `json:"from,omitempty" doc:"Default: first day of the month of to"`
	To         string     `json:"to"`
	PropertyID *uuid.UUID `json:"propertyId,omitempty" doc:"Empty: consolidated over the properties in scope"`
	Content    string     `json:"content" doc:"CSV with a header: account,debit,credit (or account,balance); account = Excel Finance item (Account Mappings) or OneClub account code"`
}

// ExcelTrialBalanceRow is the comparison of one OneClub account.
type ExcelTrialBalanceRow struct {
	AccountID  *uuid.UUID `json:"accountId"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	ExcelItems []string   `json:"excelItems"`
	Excel      string     `json:"excel" doc:"Excel Finance closing balance (debit − credit)"`
	OneClub    string     `json:"oneClub" doc:"OneClub closing balance (debit − credit)"`
	Difference string     `json:"difference" doc:"OneClub − Excel"`
	Matched    bool       `json:"matched"`
}

// ExcelTrialBalanceUnmapped is an Excel item without an account.
type ExcelTrialBalanceUnmapped struct {
	Row     int    `json:"row"`
	Account string `json:"account"`
	Balance string `json:"balance"`
}

// ExcelTrialBalanceComparison is the parallel-run comparison report.
type ExcelTrialBalanceComparison struct {
	From            string                      `json:"from"`
	To              string                      `json:"to"`
	Consolidated    bool                        `json:"consolidated"`
	Rows            []ExcelTrialBalanceRow      `json:"rows"`
	Unmapped        []ExcelTrialBalanceUnmapped `json:"unmapped"`
	Differences     int                         `json:"differences"`
	TotalDifference string                      `json:"totalDifference" doc:"Σ |difference|"`
	Matched         bool                        `json:"matched" doc:"Every account agrees and no Excel item is unmapped"`
}

// CompareExcelTrialBalance compares the OneClub trial balance of a period
// with the Excel Finance one (FR-TRS-03 1-month parallel run).
func CompareExcelTrialBalance(ctx context.Context, q dbtx.Querier, in ExcelTrialBalanceInput) (ExcelTrialBalanceComparison, error) {
	var res ExcelTrialBalanceComparison
	to, err := parseDate("to", in.To)
	if err != nil {
		return res, err
	}
	from := monthStart(to)
	if in.From != "" {
		if from, err = parseDate("from", in.From); err != nil {
			return res, err
		}
	}
	if from.After(to) {
		return res, handle.Invalid("from", "invalid", "from must not be after to")
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.Content, "\ufeff")))
	r.FieldsPerRecord, r.TrimLeadingSpace = -1, true
	header, err := r.Read()
	if err != nil {
		return res, handle.Invalid("content", "invalid_file", "the file needs a header row: account,debit,credit or account,balance")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	_, hasBal := col["balance"]
	_, hasDr := col["debit"]
	if _, ok := col["account"]; !ok || (!hasBal && !hasDr) {
		return res, handle.Invalid("content", "invalid_file", "columns: account, debit, credit (or account, balance)")
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	amount := func(s string) decimal.Decimal {
		if v, ok := parseAmount(s); ok {
			return v
		}
		return decimal.Zero
	}
	type item struct {
		row     int
		account string
		balance decimal.Decimal
	}
	var items []item
	for n := 2; ; n++ {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, handle.Invalid("content", "invalid_file", "row "+strconv.Itoa(n)+": "+err.Error())
		}
		acct := get(rec, "account")
		if acct == "" {
			continue
		}
		bal := amount(get(rec, "balance"))
		if !hasBal {
			bal = amount(get(rec, "debit")).Sub(amount(get(rec, "credit")))
		}
		items = append(items, item{n, acct, bal})
	}
	if len(items) == 0 {
		return res, handle.Invalid("content", "empty", "the file has no rows")
	}
	// account per Excel item: Account Mappings first, else the OneClub code
	codes := make([]string, len(items))
	for i, it := range items {
		codes[i] = it.account
	}
	rows, err := q.Query(ctx, `SELECT k.code, coalesce(m.account_id, a.id) FROM unnest($1::text[]) k(code)
		LEFT JOIN accounting.account_mappings m ON m.source_system = 'excel_finance' AND m.source_code = k.code
		LEFT JOIN accounting.accounts a ON a.code = k.code`, codes)
	if err != nil {
		return res, err
	}
	acctOf := map[string]*uuid.UUID{}
	for rows.Next() {
		var code string
		var aid *uuid.UUID
		if err := rows.Scan(&code, &aid); err != nil {
			rows.Close()
			return res, err
		}
		acctOf[code] = aid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	tb, err := TrialBalanceOf(ctx, q, in.PropertyID, from, to)
	if err != nil {
		return res, err
	}
	res = ExcelTrialBalanceComparison{From: ymd(from), To: ymd(to), Consolidated: in.PropertyID == nil, Rows: []ExcelTrialBalanceRow{},
		Unmapped: []ExcelTrialBalanceUnmapped{}}
	byAcct := map[uuid.UUID]*ExcelTrialBalanceRow{}
	var order []uuid.UUID
	for _, t := range tb.Rows {
		row := &ExcelTrialBalanceRow{AccountID: &t.AccountID, Code: t.Code, Name: t.Name, ExcelItems: []string{}, Excel: "0", OneClub: dec(t.Closing).String()}
		byAcct[t.AccountID] = row
		order = append(order, t.AccountID)
	}
	for _, it := range items {
		aid := acctOf[it.account]
		if aid == nil {
			res.Unmapped = append(res.Unmapped, ExcelTrialBalanceUnmapped{Row: it.row, Account: it.account, Balance: it.balance.String()})
			continue
		}
		row, ok := byAcct[*aid]
		if !ok {
			a := *aid
			row = &ExcelTrialBalanceRow{AccountID: &a, ExcelItems: []string{}, Excel: "0", OneClub: "0"}
			if err := q.QueryRow(ctx, `SELECT code, name FROM accounting.accounts WHERE id = $1`, a).Scan(&row.Code, &row.Name); err != nil {
				return res, err
			}
			byAcct[a] = row
			order = append(order, a)
		}
		row.ExcelItems = append(row.ExcelItems, it.account)
		row.Excel = dec(row.Excel).Add(it.balance).String()
	}
	total := decimal.Zero
	for _, aid := range order {
		row := byAcct[aid]
		diff := dec(row.OneClub).Sub(dec(row.Excel))
		row.Difference, row.Matched = diff.String(), diff.IsZero()
		if dec(row.OneClub).IsZero() && dec(row.Excel).IsZero() && len(row.ExcelItems) == 0 {
			continue
		}
		if !row.Matched {
			res.Differences++
			total = total.Add(diff.Abs())
		}
		res.Rows = append(res.Rows, *row)
	}
	sort.SliceStable(res.Rows, func(i, j int) bool { return res.Rows[i].Code < res.Rows[j].Code })
	res.TotalDifference = total.String()
	res.Matched = res.Differences == 0 && len(res.Unmapped) == 0
	return res, nil
}

// ── routes ────────────────────────────────────────────────────────────────

// RegisterP4FixFinance adds the read endpoints of the Finance UX fixes.
func (m *Module) RegisterP4FixFinance(reg *route.Registry) {
	db := m.DB
	m.propertyRoute(reg, "General Ledger", route.Route{Method: http.MethodGet, Path: base + "/journals/{id}/documents",
		Summary: "Source documents of a journal with their numbers (drill-down, FR-FIN-04)", Permission: "accounting.journal.view",
		Response: AccountingSourceDocument{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingSourceDocument], error) {
			jid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[AccountingSourceDocument]{}, err
			}
			return handle.Page(JournalDocuments(ctx, tx, handle.Property(ctx), jid))
		})})
	m.globalRoute(reg, "General Ledger", route.Route{Method: http.MethodGet, Path: base + "/general-ledger/documents",
		Summary:    "Source documents of the general ledger lines of an account (same filters as the General Ledger, FR-FIN-04)",
		Permission: "accounting.ledger.view", Response: AccountingLedgerLineDocuments{}, List: true,
		Query: []route.Param{{Name: "accountId", Required: true}, {Name: "from"}, {Name: "to"}, {Name: "propertyId"}, {Name: "businessLine"},
			{Name: "costCenter"}, {Name: "departmentId"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountingLedgerLineDocuments], error) {
			var zero httpx.Page[AccountingLedgerLineDocuments]
			aid, err := handle.QueryUUID(r, "accountId")
			if err != nil {
				return zero, err
			}
			if aid == nil {
				return zero, errs.BadRequest("account_required", "accountId is required")
			}
			from, to, err := dateRange(ctx, tx, r)
			if err != nil {
				return zero, err
			}
			prop, err := propertyFilter(r)
			if err != nil {
				return zero, err
			}
			dep, err := handle.QueryUUID(r, "departmentId")
			if err != nil {
				return zero, err
			}
			q := r.URL.Query()
			return handle.Page(LedgerDocuments(ctx, tx, *aid, LedgerFilter{Property: prop, From: from, To: to, BusinessLine: q.Get("businessLine"),
				CostCenter: q.Get("costCenter"), DepartmentID: dep}))
		})})
	m.propertyRoute(reg, "Revenue & Tax", route.Route{Method: http.MethodGet, Path: base + "/invoice-efaktur",
		Summary:    "e-Faktur number and status of the billing invoices of the property (Invoices screen, PRD P4 §7.1)",
		Permission: "billing.invoice.view", Response: InvoiceEFakturStatus{}, List: true,
		Query: []route.Param{{Name: "filter[invoiceId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[InvoiceEFakturStatus], error) {
			lp := httpx.ParseList(r)
			var inv *uuid.UUID
			if v := lp.Filters["invoiceId"]; v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					return httpx.Page[InvoiceEFakturStatus]{}, handle.Invalid("filter[invoiceId]", "invalid", "not a uuid")
				}
				inv = &u
			}
			return handle.Page(handle.List[InvoiceEFakturStatus](tx.Query(ctx, efakturSelect+` WHERE e.property_id = $1 AND ($2::uuid IS NULL OR e.invoice_id = $2)
				ORDER BY e.created_at DESC LIMIT 500`, handle.Property(ctx), inv)))
		})})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/member/invoice-efaktur", Module: "accounting", Tag: "Member Portal",
		Summary: "e-Faktur of my invoices (Member App → Invoices, PRD P4 §7.3)", Permission: catalog.ShellMemberPortal,
		Response: InvoiceEFakturStatus{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[InvoiceEFakturStatus], error) {
			return handle.Page(handle.List[InvoiceEFakturStatus](tx.Query(ctx, efakturSelect+` WHERE e.customer_user_id = $1 AND e.corporate_account_id IS NULL
				AND e.invoice_status <> 'draft' ORDER BY e.created_at DESC LIMIT 500`, handle.UserID(ctx))))
		})})
	m.globalRoute(reg, "Closing", route.Route{Method: http.MethodPost, Path: base + "/reconciliations:excel-trial-balance",
		Summary:    "Compare the trial balance with the Excel Finance trial balance of the parallel-run month (FR-TRS-03)",
		Permission: "accounting.ledger.view", Request: ExcelTrialBalanceInput{}, Response: ExcelTrialBalanceComparison{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ExcelTrialBalanceInput) (ExcelTrialBalanceComparison, error) {
			res, err := CompareExcelTrialBalance(ctx, tx, in)
			if err != nil {
				return res, err
			}
			return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "compare", EntityType: "accounting.trial_balance",
				EntityID: res.From + ".." + res.To, EntityLabel: "Excel Finance TB comparison " + res.From + " – " + res.To, PropertyID: in.PropertyID,
				After: map[string]any{"differences": res.Differences, "unmapped": len(res.Unmapped), "totalDifference": res.TotalDifference, "matched": res.Matched}})
		})})
}

// SourceDocumentTypes lists the source types resolved to documents.
func SourceDocumentTypes() []string {
	out := make([]string, 0, len(docSources))
	for k := range docSources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResolveSourceDocuments resolves (source type, source id) pairs to their
// documents, in the order given.
func ResolveSourceDocuments(ctx context.Context, q dbtx.Querier, refs [][2]string) ([]AccountingSourceDocument, error) {
	rs := make([]srcRef, len(refs))
	for i, r := range refs {
		rs[i] = srcRef{r[0], r[1]}
	}
	docs, err := resolveDocuments(ctx, q, rs)
	if err != nil {
		return nil, err
	}
	out := make([]AccountingSourceDocument, len(rs))
	for i, r := range rs {
		out[i] = docs[r]
	}
	return out, nil
}
