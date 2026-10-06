package accounting

// EP-23 Accounting Transition & Opening Balances and EP-29 migration wave
// 4: the book of a property (chart of accounts template, cut-over date,
// sign-off that stops the Accounting Export, FR-TRS-04), opening balance
// batches per property (manual, CSV import with the Excel Finance account
// mapping, or pulled from the operational sub-ledgers: open invoices,
// deferred revenue, deposits, caddy fees), posted by the Finance Manager as
// one opening journal with the AR / AP open items (FR-TRS-01/02,
// FR-MIG-P4-02/04), and the reconciliation of the batch with the trial
// balance and the sub-ledgers (FR-MIG-P4-06).

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
)

// COATemplateInput opens the book of a property.
type COATemplateInput struct {
	CutOverDate string `json:"cutOverDate" doc:"First day posted by OneClub (go-live, normally the first day of a month)"`
}

// SetupBook loads the chart of accounts template and opens the book.
func (m *Module) SetupBook(ctx context.Context, tx pgx.Tx, property uuid.UUID, in COATemplateInput) (COALoadResult, error) {
	d, err := parseDate("cutOverDate", in.CutOverDate)
	if err != nil {
		return COALoadResult{}, err
	}
	if _, err := GetBook(ctx, tx, property); err == nil {
		res, err := LoadTemplate(ctx, tx, property, d)
		if err != nil {
			return res, err
		}
		if res.Book.CutOverDate != ymd(d) {
			return res, errs.Conflict("book_exists", "the book is already open since "+res.Book.CutOverDate)
		}
		return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "load_template", EntityType: "accounting.book", EntityID: property.String(),
			EntityLabel: TemplateCode, PropertyID: &property, After: res})
	}
	res, err := LoadTemplate(ctx, tx, property, d)
	if err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.books SET status = 'setup' WHERE property_id = $1`, property); err != nil {
		return res, err
	}
	if res.Book, err = GetBook(ctx, tx, property); err != nil {
		return res, err
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "load_template", EntityType: "accounting.book", EntityID: property.String(),
		EntityLabel: TemplateCode, PropertyID: &property, After: res})
}

// AccountingSignOffInput signs the transition off (FR-TRS-03/04).
type AccountingSignOffInput struct {
	Note string `json:"note"`
}

// SignOff marks the book live after the reconciliation month: the
// Accounting Export of the property stops (old exports stay downloadable).
func (m *Module) SignOff(ctx context.Context, tx pgx.Tx, property uuid.UUID, in AccountingSignOffInput) (AccountingBook, error) {
	b, err := GetBook(ctx, tx, property)
	if err != nil {
		return b, err
	}
	if b.Status == "live" && b.ExportStoppedAt != nil {
		return b, errs.Conflict("signed_off", "the transition is already signed off")
	}
	if err := handle.Required("note", in.Note); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.books SET status = 'live', export_stopped_at = now(), export_stopped_by = $2, signoff_note = $3
		WHERE property_id = $1`, property, actorPtr(ctx), in.Note); err != nil {
		return b, err
	}
	after, err := GetBook(ctx, tx, property)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "sign_off", EntityType: "accounting.book", EntityID: property.String(),
		EntityLabel: "Accounting cut-over", PropertyID: &property, Reason: in.Note, Before: b, After: after})
}

// ExportStopped reports whether the Accounting Export of a property is
// stopped (the book is live, FR-TRS-04).
func ExportStopped(ctx context.Context, q dbtx.Querier, property uuid.UUID) (bool, error) {
	var stopped bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.books WHERE property_id = $1 AND export_stopped_at IS NOT NULL)`, property).Scan(&stopped)
	return stopped, err
}

// ── opening balance batches ───────────────────────────────────────────────

// OpeningLineInput is one opening balance line.
type OpeningLineInput struct {
	AccountID    *uuid.UUID `json:"accountId,omitempty"`
	AccountCode  string     `json:"accountCode,omitempty" doc:"Account code, or an Excel Finance code of the account mapping"`
	Debit        string     `json:"debit,omitempty"`
	Credit       string     `json:"credit,omitempty"`
	PartnerType  string     `json:"partnerType,omitempty" enum:"supplier,ar_account,customer"`
	PartnerID    *uuid.UUID `json:"partnerId,omitempty"`
	PartnerName  string     `json:"partnerName,omitempty"`
	DocumentNo   string     `json:"documentNo,omitempty" doc:"Open item number (invoice / vendor invoice)"`
	DocumentDate string     `json:"documentDate,omitempty"`
	DueDate      string     `json:"dueDate,omitempty"`
	Description  string     `json:"description,omitempty"`
}

// OpeningBalanceInput drafts an opening balance batch.
type OpeningBalanceInput struct {
	BalanceDate       string             `json:"balanceDate,omitempty" doc:"Default: the day before the cut-over date"`
	Description       string             `json:"description"`
	Lines             []OpeningLineInput `json:"lines,omitempty"`
	IncludeSubledgers bool               `json:"includeSubledgers,omitempty" doc:"Add the operational sub-ledgers at the balance date: open invoices (AR), deferred revenue, deposits held, caddy fees held"`
}

// OpeningLine is a stored opening balance line.
type OpeningLine struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	LineNo       int        `json:"lineNo" db:"line_no"`
	AccountID    uuid.UUID  `json:"accountId" db:"account_id"`
	AccountCode  string     `json:"accountCode" db:"account_code"`
	AccountName  string     `json:"accountName" db:"account_name"`
	Debit        string     `json:"debit" db:"debit"`
	Credit       string     `json:"credit" db:"credit"`
	PartnerType  *string    `json:"partnerType" db:"partner_type"`
	PartnerID    *uuid.UUID `json:"partnerId" db:"partner_id"`
	PartnerName  *string    `json:"partnerName" db:"partner_name"`
	DocumentNo   *string    `json:"documentNo" db:"document_no"`
	DocumentDate *string    `json:"documentDate" db:"document_date"`
	DueDate      *string    `json:"dueDate" db:"due_date"`
	Description  *string    `json:"description" db:"description"`
}

// OpeningBalance is an opening balance batch.
type OpeningBalance struct {
	ID          uuid.UUID     `json:"id" db:"id"`
	Number      string        `json:"number" db:"number"`
	BalanceDate string        `json:"balanceDate" db:"balance_date"`
	Description string        `json:"description" db:"description"`
	Status      string        `json:"status" db:"status" enum:"draft,posted"`
	TotalDebit  string        `json:"totalDebit" db:"total_debit"`
	TotalCredit string        `json:"totalCredit" db:"total_credit"`
	JournalID   *uuid.UUID    `json:"journalId" db:"journal_id"`
	PostedAt    *time.Time    `json:"postedAt" db:"posted_at"`
	CreatedAt   time.Time     `json:"createdAt" db:"created_at"`
	Lines       []OpeningLine `json:"lines" db:"-"`
}

const obSelect = `SELECT id, number, to_char(balance_date, 'YYYY-MM-DD') AS balance_date, description, status, trim_scale(total_debit)::text AS total_debit,
	trim_scale(total_credit)::text AS total_credit, journal_id, posted_at, created_at FROM accounting.opening_balances`

// GetOpeningBalance loads a batch with its lines.
func GetOpeningBalance(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) (OpeningBalance, error) {
	b, err := getOne[OpeningBalance]("opening balance")(q.Query(ctx, obSelect+` WHERE id = $1 AND property_id = $2`, bid, property))
	if err != nil {
		return b, err
	}
	b.Lines, err = handle.List[OpeningLine](q.Query(ctx, `SELECT l.id, l.line_no, l.account_id, a.code AS account_code, a.name AS account_name,
		trim_scale(l.debit)::text AS debit, trim_scale(l.credit)::text AS credit, l.partner_type, l.partner_id, l.partner_name, l.document_no,
		to_char(l.document_date, 'YYYY-MM-DD') AS document_date, to_char(l.due_date, 'YYYY-MM-DD') AS due_date, l.description
		FROM accounting.opening_balance_lines l JOIN accounting.accounts a ON a.id = l.account_id WHERE l.batch_id = $1 ORDER BY l.line_no`, bid))
	return b, err
}

// ListOpeningBalances lists the batches of a property.
func ListOpeningBalances(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]OpeningBalance, error) {
	return handle.List[OpeningBalance](q.Query(ctx, obSelect+` WHERE property_id = $1 ORDER BY created_at DESC`, property))
}

type resolvedLine struct {
	account            uuid.UUID
	debit, credit      decimal.Decimal
	partnerType        string
	partnerID          *uuid.UUID
	partnerName, docNo string
	docDate, dueDate   *time.Time
	description        string
}

// resolveOpeningLine resolves the account (id, code or mapped Excel
// code) and the partner of a line.
func resolveOpeningLine(ctx context.Context, q dbtx.Querier, property uuid.UUID, i int, l OpeningLineInput) (resolvedLine, error) {
	field := fmt.Sprintf("lines[%d]", i)
	var r resolvedLine
	switch {
	case l.AccountID != nil:
		r.account = *l.AccountID
	case strings.TrimSpace(l.AccountCode) != "":
		code := strings.TrimSpace(l.AccountCode)
		err := q.QueryRow(ctx, `SELECT id FROM accounting.accounts WHERE code = $1`, code).Scan(&r.account)
		if dbtx.IsNoRows(err) {
			err = q.QueryRow(ctx, `SELECT account_id FROM accounting.account_mappings WHERE source_code = $1 ORDER BY source_system LIMIT 1`, code).Scan(&r.account)
			if dbtx.IsNoRows(err) {
				return r, handle.Invalid(field+".accountCode", "not_found", "account or mapping "+code+" not found")
			}
		}
		if err != nil {
			return r, err
		}
	default:
		return r, handle.Invalid(field+".accountId", "required", "choose the account")
	}
	var status, code string
	var posting bool
	var props []uuid.UUID
	err := q.QueryRow(ctx, `SELECT code, status, is_posting, properties FROM accounting.accounts WHERE id = $1`, r.account).Scan(&code, &status, &posting, &props)
	if dbtx.IsNoRows(err) {
		return r, handle.Invalid(field+".accountId", "not_found", "account not found")
	}
	if err != nil {
		return r, err
	}
	if a := checkAccount(acctInfo{}, false, status, posting, props, property, code); a.Err != "" {
		return r, handle.Invalid(field+".accountId", "invalid_account", a.Err)
	}
	if r.debit, err = handle.Decimal(field+".debit", l.Debit, decimal.Zero); err != nil {
		return r, err
	}
	if r.credit, err = handle.Decimal(field+".credit", l.Credit, decimal.Zero); err != nil {
		return r, err
	}
	if r.debit.IsNegative() || r.credit.IsNegative() || (r.debit.IsPositive() && r.credit.IsPositive()) || (r.debit.IsZero() && r.credit.IsZero()) {
		return r, handle.Invalid(field, "invalid_line", "a line is either a debit or a credit of a positive amount")
	}
	r.partnerType, r.partnerID, r.partnerName = l.PartnerType, l.PartnerID, l.PartnerName
	switch r.partnerType {
	case "":
	case "supplier":
		if r.partnerID == nil {
			return r, handle.Invalid(field+".partnerId", "required", "choose the supplier")
		}
		name, _, ok, err := supplierLookup(ctx, q, property, *r.partnerID)
		if err != nil {
			return r, err
		}
		if !ok {
			return r, handle.Invalid(field+".partnerId", "not_found", "supplier not found")
		}
		if r.partnerName == "" {
			r.partnerName = name
		}
	case "ar_account", "customer":
		if r.partnerName == "" && r.partnerID != nil {
			_ = q.QueryRow(ctx, `SELECT name FROM reporting.acc_ar_accounts WHERE id = $1`, *r.partnerID).Scan(&r.partnerName)
		}
	default:
		return r, handle.Invalid(field+".partnerType", "invalid", "supplier, ar_account or customer")
	}
	r.docNo, r.description = strings.TrimSpace(l.DocumentNo), l.Description
	if l.DocumentDate != "" {
		t, err := parseDate(field+".documentDate", l.DocumentDate)
		if err != nil {
			return r, err
		}
		r.docDate = &t
	}
	if l.DueDate != "" {
		t, err := parseDate(field+".dueDate", l.DueDate)
		if err != nil {
			return r, err
		}
		r.dueDate = &t
	}
	return r, nil
}

// subledgerLines are the opening lines pulled from the operational
// sub-ledgers at the end of the balance date.
func subledgerLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, cfg AccountingConfiguration, at time.Time) ([]resolvedLine, error) {
	code := func(role string) (uuid.UUID, error) {
		aid, err := accountIDByCode(ctx, q, cfg.Accounts[role])
		if err != nil {
			return aid, errs.Conflict("missing_account", "default account of "+role+" not found")
		}
		return aid, nil
	}
	ar, err := code("ar_control")
	if err != nil {
		return nil, err
	}
	equity, err := code("opening_balance_equity")
	if err != nil {
		return nil, err
	}
	var out []resolvedLine
	eqLine := func(amt decimal.Decimal, desc string) {
		if amt.IsPositive() {
			out = append(out, resolvedLine{account: equity, credit: amt, description: desc})
		} else if amt.IsNegative() {
			out = append(out, resolvedLine{account: equity, debit: amt.Neg(), description: desc})
		}
	}
	type openInv struct {
		ID        uuid.UUID  `db:"id"`
		Number    string     `db:"number"`
		AccountID *uuid.UUID `db:"account_id"`
		BillTo    string     `db:"bill_to_name"`
		Issue     time.Time  `db:"issue_date"`
		Due       *time.Time `db:"due_date"`
		Open      string     `db:"open_amount"`
	}
	invs, err := handle.List[openInv](q.Query(ctx, `WITH open AS (`+arOpenAsOf+`) SELECT id, number, account_id, bill_to_name, issue_date, due_date,
		open_amount::text AS open_amount FROM open WHERE open_amount > 0 ORDER BY issue_date, number`, property, ymd(at)))
	if err != nil {
		return nil, err
	}
	for _, i := range invs {
		amt := dec(i.Open)
		issue, due := i.Issue, i.Due
		out = append(out, resolvedLine{account: ar, debit: amt, partnerType: "ar_account", partnerID: i.AccountID, partnerName: i.BillTo, docNo: i.Number,
			docDate: &issue, dueDate: due, description: "Open invoice " + i.Number})
		eqLine(amt, "Open invoice "+i.Number)
	}
	end := dateOnly(at).AddDate(0, 0, 1)
	for lt, role := range liabilityRoles {
		var s *string
		if err := q.QueryRow(ctx, `SELECT sum(amount)::text FROM reporting.acc_deferred_entries WHERE property_id = $1 AND liability_type = $2 AND occurred_at < $3`,
			property, lt, end).Scan(&s); err != nil {
			return nil, err
		}
		if amt := decp(s); !amt.IsZero() {
			aid, err := code(role)
			if err != nil {
				return nil, err
			}
			l := resolvedLine{account: aid, description: "Deferred revenue " + lt}
			if amt.IsPositive() {
				l.credit = amt
			} else {
				l.debit = amt.Neg()
			}
			out = append(out, l)
			eqLine(amt.Neg(), "Deferred revenue "+lt)
		}
	}
	var held *string
	if err := q.QueryRow(ctx, `SELECT sum(amount - applied_amount)::text FROM reporting.acc_deposits WHERE property_id = $1 AND status = 'held' AND created_at < $2`,
		property, end).Scan(&held); err != nil {
		return nil, err
	}
	if amt := decp(held); amt.IsPositive() {
		aid, err := code("customer_deposits")
		if err != nil {
			return nil, err
		}
		out = append(out, resolvedLine{account: aid, credit: amt, description: "Customer deposits held"})
		eqLine(amt.Neg(), "Customer deposits held")
	}
	caddy, err := caddySubledger(ctx, q, property, at)
	if err != nil {
		return nil, err
	}
	if caddy.IsPositive() {
		aid, err := code("caddy_fee_payable")
		if err != nil {
			return nil, err
		}
		out = append(out, resolvedLine{account: aid, credit: caddy, description: "Caddy fees held"})
		eqLine(caddy.Neg(), "Caddy fees held")
	}
	return out, nil
}

// arOpenAsOf lists the billing invoices with their open amount at a date
// ($1 property, $2 date): the same figures as the operational ageing of
// billing (PRD P4 FR-AR-04). Settlements count up to the end of the date at
// the property (k.end_at, the next local midnight), not midnight UTC.
const arOpenAsOf = `SELECT i.id, i.number, i.account_id, i.corporate_account_id, i.customer_id, i.bill_to_name, i.issue_date, i.due_date,
	i.total - coalesce((SELECT sum(a.amount) FROM reporting.acc_allocations a WHERE a.invoice_id = i.id AND a.created_at < k.end_at), 0)
	  - coalesce((SELECT sum(c.amount) FROM reporting.acc_credit_notes c WHERE c.invoice_id = i.id AND c.created_at < k.end_at), 0)
	  - coalesce((SELECT sum(w.amount) FROM reporting.acc_write_offs w WHERE w.invoice_id = i.id AND w.status = 'approved' AND w.decided_at < k.end_at), 0)
	  AS open_amount
	FROM reporting.acc_invoices i CROSS JOIN (SELECT ($2::date + 1)::timestamp AT TIME ZONE coalesce(
	  (SELECT nullif(timezone, '') FROM platform.properties WHERE id = $1), (SELECT timezone FROM platform.instance)) AS end_at) k
	WHERE i.property_id = $1 AND i.issue_date <= $2::date
	  AND (i.status NOT IN ('draft', 'void') OR (i.status = 'void' AND i.voided_at >= k.end_at))`

// caddySubledger is the caddy fee held for caddies at the end of a date:
// caddy fee and tip components charged minus the settlements paid.
func caddySubledger(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (decimal.Decimal, error) {
	var held, paid *string
	if err := q.QueryRow(ctx, `SELECT sum(x.amount)::text FROM reporting.acc_folio_lines l CROSS JOIN LATERAL (
		  SELECT (c->>'amount')::numeric AS amount FROM jsonb_array_elements(CASE WHEN jsonb_typeof(l.components) = 'array' THEN l.components ELSE '[]'::jsonb END) c
		  WHERE c->>'code' IN ('caddy_fee', 'caddy_tip')
		  UNION ALL SELECT l.net_amount WHERE l.charge_type IN ('caddy_fee', 'caddy_tip')
		    AND jsonb_array_length(CASE WHEN jsonb_typeof(l.components) = 'array' THEN l.components ELSE '[]'::jsonb END) = 0) x
		WHERE l.property_id = $1 AND l.voided_at IS NULL AND l.business_date <= $2 AND l.reference_type IS DISTINCT FROM 'billing.folio_transfer'`,
		property, dateOnly(at)).Scan(&held); err != nil {
		return decimal.Zero, err
	}
	if err := q.QueryRow(ctx, `SELECT sum(amount + coalesce(settlement_deductions, 0))::text FROM reporting.acc_payouts WHERE property_id = $1
		AND payout_type = 'caddy_fee_settlement' AND paid_at < $2`, property, dateOnly(at).AddDate(0, 0, 1)).Scan(&paid); err != nil {
		return decimal.Zero, err
	}
	return decp(held).Sub(decp(paid)), nil
}

func (m *Module) insertOpeningLines(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, start int, lines []resolvedLine) error {
	for i, l := range lines {
		var pt *string
		if l.partnerType != "" {
			pt = &l.partnerType
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.opening_balance_lines (id, property_id, batch_id, line_no, account_id, debit, credit, partner_type, partner_id,
			partner_name, document_no, document_date, due_date, description) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7::numeric,$8,$9,$10,$11,$12,$13,$14)`,
			id.New(), property, bid, start+i+1, l.account, l.debit.String(), l.credit.String(), pt, l.partnerID, nz(l.partnerName), nz(l.docNo), l.docDate,
			l.dueDate, nz(l.description)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE accounting.opening_balances SET total_debit = (SELECT coalesce(sum(debit), 0) FROM accounting.opening_balance_lines WHERE batch_id = $1),
		total_credit = (SELECT coalesce(sum(credit), 0) FROM accounting.opening_balance_lines WHERE batch_id = $1) WHERE id = $1`, bid)
	return err
}

// CreateOpeningBalance drafts an opening balance batch.
func (m *Module) CreateOpeningBalance(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpeningBalanceInput) (OpeningBalance, error) {
	b, err := GetBook(ctx, tx, property)
	if err != nil {
		return OpeningBalance{}, err
	}
	cut, _ := time.Parse("2006-01-02", b.CutOverDate)
	d := cut.AddDate(0, 0, -1)
	if in.BalanceDate != "" {
		if d, err = parseDate("balanceDate", in.BalanceDate); err != nil {
			return OpeningBalance{}, err
		}
	}
	if !d.Before(cut) {
		return OpeningBalance{}, handle.Invalid("balanceDate", "after_cut_over", "opening balances are dated before the cut-over date "+b.CutOverDate)
	}
	if err := handle.Required("description", in.Description); err != nil {
		return OpeningBalance{}, err
	}
	var lines []resolvedLine
	for i, l := range in.Lines {
		r, err := resolveOpeningLine(ctx, tx, property, i, l)
		if err != nil {
			return OpeningBalance{}, err
		}
		lines = append(lines, r)
	}
	if in.IncludeSubledgers {
		cfg, err := LoadConfiguration(ctx, tx, property)
		if err != nil {
			return OpeningBalance{}, err
		}
		sl, err := subledgerLines(ctx, tx, property, cfg, d)
		if err != nil {
			return OpeningBalance{}, err
		}
		lines = append(lines, sl...)
	}
	if len(lines) == 0 {
		return OpeningBalance{}, handle.Invalid("lines", "required", "add lines or include the sub-ledgers")
	}
	bid := id.New()
	num, err := numbering.Next(ctx, tx, property, "OB", d)
	if err != nil {
		return OpeningBalance{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.opening_balances (id, property_id, number, balance_date, description, created_by) VALUES ($1,$2,$3,$4,$5,$6)`,
		bid, property, num, d, in.Description, actorPtr(ctx)); err != nil {
		return OpeningBalance{}, err
	}
	if err := m.insertOpeningLines(ctx, tx, property, bid, 0, lines); err != nil {
		return OpeningBalance{}, err
	}
	ob, err := GetOpeningBalance(ctx, tx, property, bid)
	if err != nil {
		return ob, err
	}
	return ob, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.opening_balance", EntityID: bid.String(),
		EntityLabel: num + " · " + in.Description, PropertyID: &property, After: map[string]any{"balanceDate": ob.BalanceDate, "lines": len(ob.Lines),
			"totalDebit": ob.TotalDebit, "totalCredit": ob.TotalCredit}})
}

// OpeningImportInput imports opening balances from a CSV (Excel Finance):
// account (code or mapped Excel code), debit, credit, partner_type,
// partner (supplier code or AR account number), document_no,
// document_date, due_date, description.
type OpeningImportInput struct {
	Content     string `json:"content"`
	Description string `json:"description"`
	BalanceDate string `json:"balanceDate,omitempty"`
	Preview     bool   `json:"preview,omitempty" doc:"Validate only"`
}

// OpeningImportIssue is a problem of an import row.
type OpeningImportIssue struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// OpeningImportResult reports an import.
type OpeningImportResult struct {
	Rows        int                  `json:"rows"`
	TotalDebit  string               `json:"totalDebit"`
	TotalCredit string               `json:"totalCredit"`
	Balanced    bool                 `json:"balanced"`
	Issues      []OpeningImportIssue `json:"issues"`
	Batch       *OpeningBalance      `json:"batch,omitempty"`
}

// ImportOpeningBalances validates (preview) or drafts a batch from a CSV.
func (m *Module) ImportOpeningBalances(ctx context.Context, tx pgx.Tx, property uuid.UUID, in OpeningImportInput) (OpeningImportResult, error) {
	res := OpeningImportResult{Issues: []OpeningImportIssue{}}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(in.Content, "\ufeff")))
	r.FieldsPerRecord, r.TrimLeadingSpace = -1, true
	header, err := r.Read()
	if err != nil {
		return res, handle.Invalid("content", "invalid_file", "the file needs a header row")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["account"]; !ok {
		return res, handle.Invalid("content", "invalid_file", "columns: account, debit, credit, partner_type, partner, document_no, document_date, due_date, description")
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var inputs []OpeningLineInput
	row := 1
	dr, cr := decimal.Zero, decimal.Zero
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		row++
		if err != nil {
			res.Issues = append(res.Issues, OpeningImportIssue{Row: row, Message: err.Error()})
			continue
		}
		l := OpeningLineInput{AccountCode: get(rec, "account"), PartnerType: get(rec, "partner_type"), DocumentNo: get(rec, "document_no"),
			DocumentDate: get(rec, "document_date"), DueDate: get(rec, "due_date"), Description: get(rec, "description")}
		if d, ok := parseAmount(get(rec, "debit")); ok {
			l.Debit = d.String()
		}
		if c, ok := parseAmount(get(rec, "credit")); ok {
			l.Credit = c.String()
		}
		if p := get(rec, "partner"); p != "" {
			var pid uuid.UUID
			var name string
			var err error
			switch l.PartnerType {
			case "supplier":
				err = tx.QueryRow(ctx, `SELECT id, name FROM reporting.acc_suppliers WHERE property_id = $1 AND code = $2`, property, p).Scan(&pid, &name)
			case "ar_account", "customer":
				l.PartnerType = "ar_account"
				err = tx.QueryRow(ctx, `SELECT id, name FROM reporting.acc_ar_accounts WHERE property_id = $1 AND number = $2`, property, p).Scan(&pid, &name)
			default:
				err = fmt.Errorf("partner_type must be supplier or ar_account")
			}
			if err != nil {
				msg := "partner " + p + " not found"
				if !dbtx.IsNoRows(err) {
					msg = err.Error()
				}
				res.Issues = append(res.Issues, OpeningImportIssue{Row: row, Message: msg})
				continue
			}
			l.PartnerID, l.PartnerName = &pid, name
		}
		rl, err := resolveOpeningLine(ctx, tx, property, len(inputs), l)
		if err != nil {
			msg := err.Error()
			if e, ok := errs.As(err); ok {
				msg = e.Message
				for _, f := range e.Fields {
					msg += " (" + f.Message + ")"
				}
			}
			res.Issues = append(res.Issues, OpeningImportIssue{Row: row, Message: msg})
			continue
		}
		dr, cr = dr.Add(rl.debit), cr.Add(rl.credit)
		inputs = append(inputs, l)
	}
	res.Rows, res.TotalDebit, res.TotalCredit, res.Balanced = len(inputs), dr.String(), cr.String(), dr.Equal(cr)
	if in.Preview {
		return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "preview", EntityType: "accounting.opening_balance", EntityID: property.String(),
			EntityLabel: "Opening balance import preview · " + in.Description, PropertyID: &property,
			After: map[string]any{"rows": res.Rows, "balanced": res.Balanced, "issues": len(res.Issues)}})
	}
	if len(res.Issues) > 0 {
		return res, errs.Validation("import_issues", fmt.Sprintf("%d rows have issues; fix them (preview shows the details)", len(res.Issues)))
	}
	ob, err := m.CreateOpeningBalance(ctx, tx, property, OpeningBalanceInput{BalanceDate: in.BalanceDate, Description: in.Description, Lines: inputs})
	if err != nil {
		return res, err
	}
	res.Batch = &ob
	return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionImport, EntityType: "accounting.opening_balance", EntityID: ob.ID.String(),
		EntityLabel: ob.Number, PropertyID: &property, After: map[string]any{"rows": res.Rows, "totalDebit": res.TotalDebit, "totalCredit": res.TotalCredit}})
}

// DeleteOpeningBalance removes a draft batch.
func (m *Module) DeleteOpeningBalance(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID) error {
	ob, err := GetOpeningBalance(ctx, tx, property, bid)
	if err != nil {
		return err
	}
	if ob.Status != "draft" {
		return errs.Conflict("posted", "a posted batch is corrected with a journal")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM accounting.opening_balance_lines WHERE batch_id = $1`, bid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM accounting.opening_balances WHERE id = $1`, bid); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionDelete, EntityType: "accounting.opening_balance", EntityID: bid.String(),
		EntityLabel: ob.Number, PropertyID: &property, Before: map[string]any{"lines": len(ob.Lines), "totalDebit": ob.TotalDebit}})
}

// PostOpeningBalance posts a balanced batch as the opening journal and
// opens its AR and AP items (Finance Manager sign-off, FR-TRS-01).
func (m *Module) PostOpeningBalance(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID) (OpeningBalance, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounting.opening_balances WHERE id = $1 FOR UPDATE`, bid); err != nil {
		return OpeningBalance{}, err
	}
	ob, err := GetOpeningBalance(ctx, tx, property, bid)
	if err != nil {
		return ob, err
	}
	if ob.Status != "draft" {
		return ob, errs.Conflict("posted", "the batch is already posted")
	}
	if !dec(ob.TotalDebit).Equal(dec(ob.TotalCredit)) {
		return ob, errs.Conflict("unbalanced", "the opening balances are not balanced: debit "+ob.TotalDebit+" ≠ credit "+ob.TotalCredit)
	}
	d, _ := time.Parse("2006-01-02", ob.BalanceDate)
	var lines []Line
	for _, l := range ob.Lines {
		lines = append(lines, Line{AccountID: l.AccountID, Debit: dec(l.Debit), Credit: dec(l.Credit), Description: deref(l.Description),
			Dims: Dims{PartnerType: deref(l.PartnerType), PartnerID: l.PartnerID, PartnerName: deref(l.PartnerName)}, SourceType: "accounting.opening_balance",
			SourceID: l.ID.String()})
	}
	if err := lockProperty(ctx, tx, property); err != nil {
		return ob, err
	}
	j, err := m.post(ctx, tx, Entry{Property: property, Date: d, Type: "opening", SourceType: "accounting.opening_balance", SourceID: bid.String(),
		SourceRef: ob.Number, Description: "Opening balances " + ob.Number + " · " + ob.Description, Lines: lines}, false)
	if err != nil {
		return ob, err
	}
	for _, l := range ob.Lines {
		amt := dec(l.Debit).Sub(dec(l.Credit))
		pt := deref(l.PartnerType)
		switch {
		case (pt == "ar_account" || pt == "customer") && deref(l.DocumentNo) != "":
			var inv uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM reporting.acc_invoices WHERE property_id = $1 AND number = $2`, property, *l.DocumentNo).Scan(&inv)
			if dbtx.IsNoRows(err) {
				inv = l.ID // legacy invoice outside billing
			} else if err != nil {
				return ob, err
			}
			docDate := d
			if l.DocumentDate != nil {
				docDate, _ = time.Parse("2006-01-02", *l.DocumentDate)
			}
			var due *time.Time
			if l.DueDate != nil {
				t, _ := time.Parse("2006-01-02", *l.DueDate)
				due = &t
			}
			if err := insertAREntry(ctx, tx, property, arEntry{InvoiceID: inv, InvoiceNumber: *l.DocumentNo, AccountID: arAccountOf(pt, l.PartnerID),
				BillTo: deref(l.PartnerName), Type: "opening", Date: docDate, Due: due, Amount: amt, SourceID: l.ID, Journal: &j.ID}); err != nil {
				return ob, err
			}
		case pt == "supplier" && l.PartnerID != nil:
			docDate, due := d, d
			if l.DocumentDate != nil {
				docDate, _ = time.Parse("2006-01-02", *l.DocumentDate)
			}
			if l.DueDate != nil {
				due, _ = time.Parse("2006-01-02", *l.DueDate)
			}
			num := deref(l.DocumentNo)
			if num == "" {
				num = ob.Number + "-" + fmt.Sprint(l.LineNo)
			}
			if err := upsertAPItem(ctx, tx, property, apItem{SupplierID: *l.PartnerID, SupplierName: deref(l.PartnerName), Type: "opening", SourceID: l.ID,
				Number: num, SupplierInvoiceNo: deref(l.DocumentNo), InvoiceDate: docDate, DueDate: due, Subtotal: amt.Neg(), Amount: amt.Neg(), Journal: &j.ID,
				Description: "Opening balance " + ob.Number}); err != nil {
				return ob, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.opening_balances SET status = 'posted', journal_id = $2, posted_at = now(), posted_by = $3 WHERE id = $1`,
		bid, j.ID, actorPtr(ctx)); err != nil {
		return ob, err
	}
	after, err := GetOpeningBalance(ctx, tx, property, bid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "post", EntityType: "accounting.opening_balance", EntityID: bid.String(),
		EntityLabel: ob.Number, PropertyID: &property, Before: map[string]any{"status": "draft"},
		After: map[string]any{"status": "posted", "journalId": j.ID, "journalNumber": j.Number, "total": ob.TotalDebit}})
}

func arAccountOf(pt string, id *uuid.UUID) *uuid.UUID {
	if pt == "ar_account" {
		return id
	}
	return nil
}

// ControlReconCheck is one reconciliation check: a GL control account against
// its sub-ledger or source.
type ControlReconCheck struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	GL         string `json:"gl" doc:"Balance in the general ledger (normal side positive)"`
	Subledger  string `json:"subledger"`
	Difference string `json:"difference"`
	OK         bool   `json:"ok"`
}

func check(code, label string, gl, sub decimal.Decimal) ControlReconCheck {
	diff := gl.Sub(sub)
	return ControlReconCheck{Code: code, Label: label, GL: gl.String(), Subledger: sub.String(), Difference: diff.String(), OK: diff.IsZero()}
}

// OpeningReconciliation compares a posted batch with the trial balance at
// its date and the AR / AP open items (FR-MIG-P4-06).
type OpeningReconciliation struct {
	BatchID     uuid.UUID           `json:"batchId"`
	BalanceDate string              `json:"balanceDate"`
	Checks      []ControlReconCheck `json:"checks"`
	Accounts    []ControlReconCheck `json:"accounts" doc:"Batch amount per account vs the trial balance at the balance date"`
	OK          bool                `json:"ok"`
}

// ReconcileOpening reconciles an opening balance batch.
func ReconcileOpening(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) (OpeningReconciliation, error) {
	ob, err := GetOpeningBalance(ctx, q, property, bid)
	if err != nil {
		return OpeningReconciliation{}, err
	}
	out := OpeningReconciliation{BatchID: bid, BalanceDate: ob.BalanceDate, Checks: []ControlReconCheck{}, Accounts: []ControlReconCheck{}, OK: true}
	d, _ := time.Parse("2006-01-02", ob.BalanceDate)
	per := map[uuid.UUID]decimal.Decimal{}
	names := map[uuid.UUID]string{}
	var order []uuid.UUID
	arBatch, apBatch := decimal.Zero, decimal.Zero
	for _, l := range ob.Lines {
		if _, ok := per[l.AccountID]; !ok {
			order = append(order, l.AccountID)
		}
		amt := dec(l.Debit).Sub(dec(l.Credit))
		per[l.AccountID] = per[l.AccountID].Add(amt)
		names[l.AccountID] = l.AccountCode + " " + l.AccountName
		switch deref(l.PartnerType) {
		case "ar_account", "customer":
			arBatch = arBatch.Add(amt)
		case "supplier":
			apBatch = apBatch.Sub(amt)
		}
	}
	for _, aid := range order {
		gl, err := accountBalance(ctx, q, &property, aid, &d)
		if err != nil {
			return out, err
		}
		c := check(aid.String(), names[aid], gl, per[aid])
		out.Accounts = append(out.Accounts, c)
		out.OK = out.OK && c.OK
	}
	var arOpen *string
	if err := q.QueryRow(ctx, `WITH open AS (`+arOpenAsOf+`) SELECT sum(open_amount)::text FROM open WHERE open_amount > 0`, property, ymd(d)).Scan(&arOpen); err != nil {
		return out, err
	}
	var arLedger, apLedger *string
	if err := q.QueryRow(ctx, `SELECT sum(amount)::text FROM accounting.ar_entries WHERE property_id = $1 AND entry_type = 'opening'`, property).Scan(&arLedger); err != nil {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT sum(amount)::text FROM accounting.ap_items WHERE property_id = $1 AND item_type = 'opening'`, property).Scan(&apLedger); err != nil {
		return out, err
	}
	for _, c := range []ControlReconCheck{
		check("ar_open_items", "AR open items = billing open invoices at the balance date", arBatch, decp(arOpen)),
		check("ar_ledger", "AR opening items in the AR ledger", decp(arLedger), arBatch),
		check("ap_ledger", "AP opening items in the AP ledger", decp(apLedger), apBatch),
		check("balanced", "Debit = credit", dec(ob.TotalDebit), dec(ob.TotalCredit)),
	} {
		out.Checks = append(out.Checks, c)
		out.OK = out.OK && c.OK
	}
	return out, nil
}
