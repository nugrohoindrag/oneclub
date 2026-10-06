package accounting

// EP-20 Cash & Bank: cash and bank accounts per property (FR-BNK-01, master
// data in defs.go), bank statement import from CSV (generic, BCA, Mandiri)
// and MT940 with duplicate detection (FR-BNK-02 / FR-INT-P4-03), bank
// reconciliation with auto-match against the book lines of the account
// (gateway settlements, shift cash deposits, AP payments, receipts),
// manual matches, differences resolved by a journal and completion
// (FR-BNK-03), and cash documents: shift cash deposits with the variance
// (FR-BNK-04), petty cash expenses and replenishments (FR-BNK-05),
// transfers and cash counts.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"sort"
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

// BankAccountInfo is a cash or bank account with its GL account.
type BankAccountInfo struct {
	ID              uuid.UUID `db:"id"`
	Code            string    `db:"code"`
	Name            string    `db:"name"`
	Kind            string    `db:"kind"`
	GLAccountID     uuid.UUID `db:"gl_account_id"`
	GLCode          string    `db:"gl_code"`
	Currency        string    `db:"currency"`
	StatementFormat string    `db:"statement_format"`
	Status          string    `db:"status"`
}

func bankAccountOf(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) (BankAccountInfo, error) {
	b, err := getOne[BankAccountInfo]("bank account")(q.Query(ctx, `SELECT b.id, b.code, b.name, b.kind, b.gl_account_id, a.code AS gl_code, b.currency,
		b.statement_format, b.status FROM accounting.bank_accounts b JOIN accounting.accounts a ON a.id = b.gl_account_id
		WHERE b.id = $1 AND b.property_id = $2 AND b.archived_at IS NULL`, bid, property))
	if err != nil {
		return b, err
	}
	if b.Status != "active" {
		return b, errs.Conflict("inactive", "the cash / bank account is inactive")
	}
	return b, nil
}

// ── statement import (FR-BNK-02) ──────────────────────────────────────────

// BankStatementLine is a parsed bank statement line.
type BankStatementLine struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Reference   string `json:"reference,omitempty"`
	Amount      string `json:"amount" doc:"Positive = credit to the account (money in), negative = debit (money out)"`
	Balance     string `json:"balance,omitempty"`
}

// Statement is a parsed statement.
type Statement struct {
	Format         string              `json:"format"`
	From           string              `json:"from,omitempty"`
	To             string              `json:"to,omitempty"`
	OpeningBalance string              `json:"openingBalance,omitempty"`
	ClosingBalance string              `json:"closingBalance,omitempty"`
	Lines          []BankStatementLine `json:"lines"`
}

// BankStatementImportInput imports a bank statement file (mutasi).
type BankStatementImportInput struct {
	BankAccountID uuid.UUID `json:"bankAccountId"`
	Format        string    `json:"format,omitempty" enum:"generic_csv,bca_csv,mandiri_csv,mt940" doc:"Default: the format of the bank account"`
	Filename      string    `json:"filename,omitempty"`
	Content       string    `json:"content" doc:"The statement file as text"`
	Preview       bool      `json:"preview,omitempty" doc:"Parse only, nothing is stored"`
}

// BankStatementImportResult reports an import.
type BankStatementImportResult struct {
	StatementID    *uuid.UUID          `json:"statementId"`
	Number         string              `json:"number,omitempty"`
	Format         string              `json:"format"`
	Lines          int                 `json:"lines"`
	Imported       int                 `json:"imported"`
	Duplicates     int                 `json:"duplicates"`
	From           string              `json:"from,omitempty"`
	To             string              `json:"to,omitempty"`
	OpeningBalance string              `json:"openingBalance,omitempty"`
	ClosingBalance string              `json:"closingBalance,omitempty"`
	Preview        []BankStatementLine `json:"preview,omitempty"`
}

var dateLayouts = []string{"2006-01-02", "02/01/2006", "02-01-2006", "02/01/06", "2/1/2006", "02.01.2006", "20060102", "02 Jan 2006", "02-Jan-2006"}

func parseStmtDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(strings.Trim(s, "'\""))
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseAmount reads "1,000,000.00", "1.000.000,00", "-25000", "1,500.00 CR".
func parseAmount(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(strings.Trim(s, "'\""))
	neg := false
	up := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(up, "DB") || strings.HasSuffix(up, "DR") || strings.HasSuffix(up, " D"):
		neg = true
		s = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(up, "CR") || strings.HasSuffix(up, " C"):
		s = strings.TrimSpace(s[:len(s)-2])
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg, s = true, s[1:len(s)-1]
	}
	s = strings.NewReplacer("Rp", "", "IDR", "", " ", "").Replace(s)
	if s == "" || s == "-" {
		return decimal.Zero, false
	}
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case lastDot >= 0 && lastComma >= 0:
		if lastComma > lastDot { // 1.000.000,00
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else { // 1,000,000.00
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		if len(s)-lastComma-1 == 2 && strings.Count(s, ",") == 1 {
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastDot >= 0:
		if strings.Count(s, ".") > 1 || len(s)-lastDot-1 == 3 {
			s = strings.ReplaceAll(s, ".", "")
		}
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, false
	}
	if neg {
		d = d.Neg()
	}
	return d, true
}

func headerIndex(header []string, names ...string) int {
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.Trim(h, "\ufeff\"'")))
		for _, n := range names {
			if h == n || strings.HasPrefix(h, n) {
				return i
			}
		}
	}
	return -1
}

// parseCSVStatement reads generic, BCA and Mandiri CSV statements: a
// header row names the columns; amounts are one signed column, a column
// with a CR/DB suffix (BCA) or separate debit and credit columns (Mandiri).
func parseCSVStatement(content, format string) (Statement, error) {
	st := Statement{Format: format}
	sep := ','
	first := strings.SplitN(content, "\n", 2)[0]
	if strings.Count(first, ";") > strings.Count(first, ",") {
		sep = ';'
	}
	r := csv.NewReader(strings.NewReader(content))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = sep, -1, true
	var header []string
	var idx struct{ date, desc, ref, amount, debit, credit, balance, dc int }
	line := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, errs.Validation("invalid_file", "the statement is not a valid CSV file: "+err.Error())
		}
		line++
		if header == nil {
			d := headerIndex(rec, "date", "tanggal", "tgl", "transaction date", "posting date", "tx_date")
			if d < 0 {
				continue // bank header lines before the column names
			}
			header = rec
			idx.date = d
			idx.desc = headerIndex(rec, "description", "keterangan", "remark", "uraian", "transaction description", "narrative")
			idx.ref = headerIndex(rec, "reference", "ref", "no. ref", "nomor referensi", "reference no")
			idx.amount = headerIndex(rec, "amount", "jumlah", "nominal", "mutasi")
			idx.debit = headerIndex(rec, "debit", "debet", "db")
			idx.credit = headerIndex(rec, "credit", "kredit", "cr")
			idx.balance = headerIndex(rec, "balance", "saldo")
			idx.dc = headerIndex(rec, "d/c", "db/cr", "type", "jenis")
			if idx.amount < 0 && (idx.debit < 0 || idx.credit < 0) {
				return st, errs.Validation("invalid_file", "the statement needs an amount column or debit and credit columns")
			}
			continue
		}
		get := func(i int) string {
			if i < 0 || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		raw := get(idx.date)
		if raw == "" {
			continue
		}
		t, ok := parseStmtDate(raw)
		if !ok {
			low := strings.ToLower(raw)
			if strings.Contains(low, "saldo") || strings.Contains(low, "total") || strings.Contains(low, "balance") {
				continue // footer rows
			}
			return st, errs.Validation("invalid_file", fmt.Sprintf("line %d: unreadable date %q", line, raw))
		}
		var amt decimal.Decimal
		if idx.amount >= 0 {
			a, ok := parseAmount(get(idx.amount))
			if !ok {
				return st, errs.Validation("invalid_file", fmt.Sprintf("line %d: unreadable amount %q", line, get(idx.amount)))
			}
			amt = a
			if dc := strings.ToUpper(get(idx.dc)); (dc == "D" || dc == "DB" || dc == "DR" || dc == "DEBIT") && amt.IsPositive() {
				amt = amt.Neg()
			}
		} else {
			db, _ := parseAmount(get(idx.debit))
			cr, _ := parseAmount(get(idx.credit))
			amt = cr.Abs().Sub(db.Abs())
		}
		if amt.IsZero() {
			continue
		}
		sl := BankStatementLine{Date: ymd(t), Description: get(idx.desc), Reference: get(idx.ref), Amount: amt.String()}
		if b, ok := parseAmount(get(idx.balance)); ok {
			sl.Balance = b.String()
		}
		st.Lines = append(st.Lines, sl)
	}
	if header == nil {
		return st, errs.Validation("invalid_file", "no header row with a date column found")
	}
	return st, nil
}

var mt940Line = regexp.MustCompile(`^(\d{6})(\d{4})?([CD]|RC|RD)[A-Z]?([0-9]+,[0-9]{0,2})(N[A-Z0-9]{3})?([^/]*)(//.*)?$`)

// parseMT940 reads a SWIFT MT940 customer statement (:60F: opening,
// :61: statement lines with :86: descriptions, :62F: closing).
func parseMT940(content string) (Statement, error) {
	st := Statement{Format: "mt940"}
	sc := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(content, "\r", "")))
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var cur *BankStatementLine
	var tag string
	bal := func(v string) string {
		// C/D YYMMDD CUR amount
		if len(v) < 11 {
			return ""
		}
		a, ok := parseAmount(strings.Replace(v[10:], ",", ".", 1))
		if !ok {
			return ""
		}
		if v[0] == 'D' {
			a = a.Neg()
		}
		return a.String()
	}
	flush := func() {
		if cur != nil {
			st.Lines = append(st.Lines, *cur)
			cur = nil
		}
	}
	for sc.Scan() {
		ln := sc.Text()
		if strings.HasPrefix(ln, ":") {
			end := strings.Index(ln[1:], ":")
			if end < 0 {
				continue
			}
			tag = ln[1 : end+1]
			val := ln[end+2:]
			switch tag {
			case "60F", "60M":
				if st.OpeningBalance == "" {
					st.OpeningBalance = bal(val)
				}
			case "62F", "62M":
				flush()
				st.ClosingBalance = bal(val)
			case "61":
				flush()
				m := mt940Line.FindStringSubmatch(val)
				if m == nil {
					return st, errs.Validation("invalid_file", "unreadable MT940 :61: line "+val)
				}
				t, err := time.Parse("060102", m[1])
				if err != nil {
					return st, errs.Validation("invalid_file", "unreadable MT940 date "+m[1])
				}
				a, ok := parseAmount(strings.Replace(m[4], ",", ".", 1))
				if !ok {
					return st, errs.Validation("invalid_file", "unreadable MT940 amount "+m[4])
				}
				if strings.Contains(m[3], "D") {
					a = a.Neg()
				}
				ref := strings.TrimSpace(m[6])
				if ref == "NONREF" {
					ref = ""
				}
				cur = &BankStatementLine{Date: ymd(t), Amount: a.String(), Reference: ref}
			case "86":
				if cur != nil {
					cur.Description = strings.TrimSpace(val)
				}
			}
			continue
		}
		if tag == "86" && cur != nil && ln != "-" {
			cur.Description = strings.TrimSpace(cur.Description + " " + strings.TrimSpace(ln))
		}
	}
	flush()
	if len(st.Lines) == 0 && st.OpeningBalance == "" {
		return st, errs.Validation("invalid_file", "no MT940 statement lines found")
	}
	return st, nil
}

// ParseStatement parses a statement file in a format.
func ParseStatement(format, content string) (Statement, error) {
	if strings.TrimSpace(content) == "" {
		return Statement{}, handle.Invalid("content", "required", "the statement file is empty")
	}
	var st Statement
	var err error
	switch format {
	case "mt940":
		st, err = parseMT940(content)
	case "generic_csv", "bca_csv", "mandiri_csv":
		st, err = parseCSVStatement(content, format)
	default:
		return st, handle.Invalid("format", "invalid", "generic_csv, bca_csv, mandiri_csv or mt940")
	}
	if err != nil {
		return st, err
	}
	for _, l := range st.Lines {
		if st.From == "" || l.Date < st.From {
			st.From = l.Date
		}
		if l.Date > st.To {
			st.To = l.Date
		}
	}
	return st, nil
}

// ImportStatement stores the new lines of a statement as bank
// transactions; lines already imported (same fingerprint) are skipped.
func (m *Module) ImportStatement(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BankStatementImportInput) (BankStatementImportResult, error) {
	b, err := bankAccountOf(ctx, tx, property, in.BankAccountID)
	if err != nil {
		return BankStatementImportResult{}, err
	}
	format := in.Format
	if format == "" {
		format = b.StatementFormat
	}
	st, err := ParseStatement(format, in.Content)
	if err != nil {
		return BankStatementImportResult{}, err
	}
	res := BankStatementImportResult{Format: format, Lines: len(st.Lines), From: st.From, To: st.To, OpeningBalance: st.OpeningBalance,
		ClosingBalance: st.ClosingBalance}
	if in.Preview {
		res.Preview = st.Lines
		if len(res.Preview) > 200 {
			res.Preview = res.Preview[:200]
		}
		return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "preview", EntityType: "accounting.bank_statement", EntityID: b.ID.String(),
			EntityLabel: b.Name + " · statement preview", PropertyID: &property, After: map[string]any{"format": format, "lines": res.Lines, "from": res.From, "to": res.To}})
	}
	sid := id.New()
	today := localToday(ctx, tx, property)
	num, err := numbering.Next(ctx, tx, property, "BST", today)
	if err != nil {
		return res, err
	}
	var from, to any
	if t, ok := parseStmtDate(st.From); ok {
		from = t
	}
	if t, ok := parseStmtDate(st.To); ok {
		to = t
	}
	var ob, cb any
	if st.OpeningBalance != "" {
		ob = st.OpeningBalance
	}
	if st.ClosingBalance != "" {
		cb = st.ClosingBalance
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.bank_statements (id, property_id, number, bank_account_id, format, filename, period_from, period_to,
		opening_balance, closing_balance, line_count, imported_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11,$12)`,
		sid, property, num, b.ID, format, nz(in.Filename), from, to, ob, cb, len(st.Lines), actorPtr(ctx)); err != nil {
		return res, err
	}
	occ := map[string]int{}
	for _, l := range st.Lines {
		base := strings.Join([]string{b.ID.String(), l.Date, dec(l.Amount).String(), strings.ToUpper(l.Reference), strings.ToUpper(strings.Join(strings.Fields(l.Description), " "))}, "|")
		occ[base]++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", base, occ[base])))
		t, _ := time.Parse("2006-01-02", l.Date)
		var balance any
		if l.Balance != "" {
			balance = l.Balance
		}
		tag, err := tx.Exec(ctx, `INSERT INTO accounting.bank_transactions (id, property_id, bank_account_id, statement_id, tx_date, description, reference, amount,
			balance, fingerprint) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10) ON CONFLICT (bank_account_id, fingerprint) DO NOTHING`,
			id.New(), property, b.ID, sid, t, l.Description, nz(l.Reference), l.Amount, balance, hex.EncodeToString(sum[:]))
		if err != nil {
			return res, err
		}
		if tag.RowsAffected() == 1 {
			res.Imported++
		} else {
			res.Duplicates++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.bank_statements SET duplicate_count = $2 WHERE id = $1`, sid, res.Duplicates); err != nil {
		return res, err
	}
	res.StatementID, res.Number = &sid, num
	return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionImport, EntityType: "accounting.bank_statement", EntityID: sid.String(),
		EntityLabel: num + " · " + b.Name, PropertyID: &property, After: res})
}

// BankTransaction is an imported statement line.
type BankTransaction struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	BankAccountID    uuid.UUID  `json:"bankAccountId" db:"bank_account_id"`
	BankAccountName  string     `json:"bankAccountName" db:"bank_account_name"`
	StatementID      *uuid.UUID `json:"statementId" db:"statement_id"`
	TxDate           string     `json:"txDate" db:"tx_date"`
	Description      string     `json:"description" db:"description"`
	Reference        *string    `json:"reference" db:"reference"`
	Amount           string     `json:"amount" db:"amount"`
	Balance          *string    `json:"balance" db:"balance"`
	Status           string     `json:"status" db:"status" enum:"unmatched,matched,ignored"`
	ReconciliationID *uuid.UUID `json:"reconciliationId" db:"reconciliation_id"`
	MatchedAt        *time.Time `json:"matchedAt" db:"matched_at"`
	Matches          []byte     `json:"-" db:"matches"`
	MatchedJournals  []string   `json:"matchedJournals" db:"matched_journals"`
}

const bankTxSelect = `SELECT t.id, t.bank_account_id, b.name AS bank_account_name, t.statement_id, to_char(t.tx_date, 'YYYY-MM-DD') AS tx_date, t.description,
	t.reference, trim_scale(t.amount)::text AS amount, trim_scale(t.balance)::text AS balance, t.status, t.reconciliation_id, t.matched_at,
	coalesce((SELECT array_agg(DISTINCT j.number) FROM accounting.bank_matches mt JOIN accounting.journals j ON j.id = mt.journal_id
	  WHERE mt.bank_transaction_id = t.id), '{}') AS matched_journals
	FROM accounting.bank_transactions t JOIN accounting.bank_accounts b ON b.id = t.bank_account_id`

// ListBankTransactions lists the bank transactions of a property.
func ListBankTransactions(ctx context.Context, q dbtx.Querier, property uuid.UUID, bank *uuid.UUID, status, from, to string, limit int) ([]BankTransaction, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	return handle.List[BankTransaction](q.Query(ctx, bankTxSelect+` WHERE t.property_id = $1 AND ($2::uuid IS NULL OR t.bank_account_id = $2)
		AND ($3 = '' OR t.status = $3) AND ($4 = '' OR t.tx_date >= $4::date) AND ($5 = '' OR t.tx_date <= $5::date)
		ORDER BY t.tx_date, t.created_at LIMIT $6`, property, bank, status, from, to, limit))
}

// ── bank reconciliation (FR-BNK-03) ───────────────────────────────────────

// BankBookLine is a ledger line on the GL account of a bank account.
type BankBookLine struct {
	LineID        uuid.UUID `json:"lineId" db:"line_id"`
	JournalID     uuid.UUID `json:"journalId" db:"journal_id"`
	JournalNumber string    `json:"journalNumber" db:"journal_number"`
	JournalDate   string    `json:"journalDate" db:"journal_date"`
	SourceType    string    `json:"sourceType" db:"source_type"`
	SourceRef     *string   `json:"sourceRef" db:"source_ref"`
	Description   string    `json:"description" db:"description"`
	Amount        string    `json:"amount" db:"amount" doc:"Debit − credit (money in > 0)"`
	PartnerName   *string   `json:"partnerName" db:"partner_name"`
}

// BankReconciliation is a bank reconciliation with its open items.
type BankReconciliation struct {
	ID               uuid.UUID         `json:"id" db:"id"`
	Number           string            `json:"number" db:"number"`
	BankAccountID    uuid.UUID         `json:"bankAccountId" db:"bank_account_id"`
	BankAccountName  string            `json:"bankAccountName" db:"bank_account_name"`
	StatementDate    string            `json:"statementDate" db:"statement_date"`
	StatementBalance string            `json:"statementBalance" db:"statement_balance"`
	Status           string            `json:"status" db:"status" enum:"in_progress,completed"`
	CompletedAt      *time.Time        `json:"completedAt" db:"completed_at"`
	CreatedAt        time.Time         `json:"createdAt" db:"created_at"`
	BookBalance      string            `json:"bookBalance" db:"-" doc:"GL balance of the account on the statement date"`
	OutstandingBook  string            `json:"outstandingBook" db:"-" doc:"Book lines not yet on the statement (deposits in transit, unpresented payments)"`
	UnmatchedBank    string            `json:"unmatchedBank" db:"-" doc:"Statement lines not yet in the books"`
	Difference       string            `json:"difference" db:"-" doc:"Statement − (book − outstanding book + unmatched bank); 0 to complete"`
	MatchedCount     int               `json:"matchedCount" db:"-"`
	UnmatchedTxns    []BankTransaction `json:"unmatchedTransactions" db:"-" doc:"Exceptions: statement lines without a book match"`
	OutstandingLines []BankBookLine    `json:"outstandingLines" db:"-"`
	MatchedTxns      []BankTransaction `json:"matchedTransactions" db:"-"`
}

const recSelect = `SELECT r.id, r.number, r.bank_account_id, b.name AS bank_account_name, to_char(r.statement_date, 'YYYY-MM-DD') AS statement_date,
	trim_scale(r.statement_balance)::text AS statement_balance, r.status, r.completed_at, r.created_at
	FROM accounting.bank_reconciliations r JOIN accounting.bank_accounts b ON b.id = r.bank_account_id`

func bookLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, gl uuid.UUID, upTo time.Time, onlyOpen bool) ([]BankBookLine, error) {
	return handle.List[BankBookLine](q.Query(ctx, `SELECT l.id AS line_id, l.journal_id, j.number AS journal_number, to_char(l.journal_date, 'YYYY-MM-DD') AS journal_date,
		j.source_type, j.source_ref, coalesce(l.description, j.description) AS description, trim_scale(l.debit - l.credit)::text AS amount, l.partner_name
		FROM accounting.journal_lines l JOIN accounting.journals j ON j.id = l.journal_id
		WHERE l.property_id = $1 AND l.account_id = $2 AND l.journal_date <= $3 AND j.journal_type <> 'opening'
		AND (NOT $4 OR NOT EXISTS (SELECT 1 FROM accounting.bank_matches mt WHERE mt.journal_line_id = l.id))
		ORDER BY l.journal_date, j.number, l.line_no LIMIT 5000`, property, gl, dateOnly(upTo), onlyOpen))
}

// GetReconciliation loads a reconciliation with its figures.
func GetReconciliation(ctx context.Context, q dbtx.Querier, property, rid uuid.UUID) (BankReconciliation, error) {
	r, err := getOne[BankReconciliation]("bank reconciliation")(q.Query(ctx, recSelect+` WHERE r.id = $1 AND r.property_id = $2`, rid, property))
	if err != nil {
		return r, err
	}
	var gl uuid.UUID
	if err := q.QueryRow(ctx, `SELECT gl_account_id FROM accounting.bank_accounts WHERE id = $1`, r.BankAccountID).Scan(&gl); err != nil {
		return r, err
	}
	sd, _ := time.Parse("2006-01-02", r.StatementDate)
	book, err := accountBalance(ctx, q, &property, gl, &sd)
	if err != nil {
		return r, err
	}
	if r.OutstandingLines, err = bookLines(ctx, q, property, gl, sd, true); err != nil {
		return r, err
	}
	outstanding := decimal.Zero
	for _, l := range r.OutstandingLines {
		outstanding = outstanding.Add(dec(l.Amount))
	}
	bank := r.BankAccountID
	all, err := ListBankTransactions(ctx, q, property, &bank, "", "", r.StatementDate, 2000)
	if err != nil {
		return r, err
	}
	unmatched := decimal.Zero
	r.UnmatchedTxns, r.MatchedTxns = []BankTransaction{}, []BankTransaction{}
	for _, t := range all {
		switch t.Status {
		case "unmatched":
			unmatched = unmatched.Add(dec(t.Amount))
			r.UnmatchedTxns = append(r.UnmatchedTxns, t)
		case "matched":
			if t.ReconciliationID != nil && *t.ReconciliationID == r.ID {
				r.MatchedTxns = append(r.MatchedTxns, t)
			}
		}
	}
	r.MatchedCount = len(r.MatchedTxns)
	r.BookBalance, r.OutstandingBook, r.UnmatchedBank = book.String(), outstanding.String(), unmatched.String()
	r.Difference = dec(r.StatementBalance).Sub(book.Sub(outstanding).Add(unmatched)).String()
	return r, nil
}

// ListReconciliations lists the reconciliations of a property.
func ListReconciliations(ctx context.Context, q dbtx.Querier, property uuid.UUID, bank *uuid.UUID) ([]BankReconciliation, error) {
	return handle.List[BankReconciliation](q.Query(ctx, recSelect+` WHERE r.property_id = $1 AND ($2::uuid IS NULL OR r.bank_account_id = $2)
		ORDER BY r.statement_date DESC, r.created_at DESC LIMIT 200`, property, bank))
}

// BankReconciliationInput starts a reconciliation of a statement.
type BankReconciliationInput struct {
	BankAccountID    uuid.UUID `json:"bankAccountId"`
	StatementDate    string    `json:"statementDate"`
	StatementBalance string    `json:"statementBalance" doc:"Closing balance of the bank statement on that date"`
}

// CreateReconciliation starts a bank reconciliation.
func (m *Module) CreateReconciliation(ctx context.Context, tx pgx.Tx, property uuid.UUID, in BankReconciliationInput) (BankReconciliation, error) {
	b, err := bankAccountOf(ctx, tx, property, in.BankAccountID)
	if err != nil {
		return BankReconciliation{}, err
	}
	d, err := parseDate("statementDate", in.StatementDate)
	if err != nil {
		return BankReconciliation{}, err
	}
	bal, err := handle.Decimal("statementBalance", in.StatementBalance, decimal.Zero)
	if err != nil {
		return BankReconciliation{}, err
	}
	if strings.TrimSpace(in.StatementBalance) == "" {
		return BankReconciliation{}, handle.Invalid("statementBalance", "required", "enter the statement closing balance")
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.bank_reconciliations WHERE bank_account_id = $1 AND status = 'in_progress')`,
		b.ID).Scan(&open); err != nil {
		return BankReconciliation{}, err
	}
	if open {
		return BankReconciliation{}, errs.Conflict("reconciliation_open", "complete the reconciliation in progress of this account first")
	}
	num, err := numbering.Next(ctx, tx, property, "BREC", d)
	if err != nil {
		return BankReconciliation{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.bank_reconciliations (id, property_id, number, bank_account_id, statement_date, statement_balance, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::numeric,$7)`, rid, property, num, b.ID, d, bal.String(), actorPtr(ctx)); err != nil {
		return BankReconciliation{}, err
	}
	r, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	return r, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.bank_reconciliation",
		EntityID: rid.String(), EntityLabel: num + " · " + b.Name, PropertyID: &property, After: map[string]any{"statementDate": r.StatementDate,
			"statementBalance": r.StatementBalance, "bookBalance": r.BookBalance}})
}

func lockReconciliation(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (BankReconciliation, uuid.UUID, error) {
	var st string
	var gl uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT r.status, b.gl_account_id FROM accounting.bank_reconciliations r JOIN accounting.bank_accounts b ON b.id = r.bank_account_id
		WHERE r.id = $1 AND r.property_id = $2 FOR UPDATE OF r`, rid, property).Scan(&st, &gl); err != nil {
		if dbtx.IsNoRows(err) {
			return BankReconciliation{}, gl, errs.NotFound("bank reconciliation")
		}
		return BankReconciliation{}, gl, err
	}
	if st != "in_progress" {
		return BankReconciliation{}, gl, errs.Conflict("reconciliation_completed", "the reconciliation is completed")
	}
	r, err := GetReconciliation(ctx, tx, property, rid)
	return r, gl, err
}

func (m *Module) matchLines(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, t BankTransaction, lines []BankBookLine, kind string) error {
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO accounting.bank_matches (id, property_id, reconciliation_id, bank_transaction_id, journal_line_id, journal_id, amount,
			match_type, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9)`, id.New(), property, rid, t.ID, l.LineID, l.JournalID, l.Amount, kind,
			actorPtr(ctx)); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return errs.Conflict("already_matched", "journal line "+l.JournalNumber+" is already matched")
			}
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE accounting.bank_transactions SET status = 'matched', reconciliation_id = $2, matched_at = now(), matched_by = $3 WHERE id = $1`,
		t.ID, rid, actorPtr(ctx))
	return err
}

// BankAutoMatchResult reports an auto-match.
type BankAutoMatchResult struct {
	Matched        int                `json:"matched"`
	Unmatched      int                `json:"unmatched" doc:"Statement lines left as exceptions"`
	Reconciliation BankReconciliation `json:"reconciliation"`
}

// AutoMatch matches the statement lines up to the statement date with the
// open book lines: one-to-one on the same amount (same date first, then
// within the configured days, a reference in the description wins), then a
// statement line equal to the sum of the open book lines of one source and
// date (e.g. the deposits of several shifts).
func (m *Module) AutoMatch(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (BankAutoMatchResult, error) {
	r, gl, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return BankAutoMatchResult{}, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return BankAutoMatchResult{}, err
	}
	sd, _ := time.Parse("2006-01-02", r.StatementDate)
	open, err := bookLines(ctx, tx, property, gl, sd.AddDate(0, 0, cfg.AutoMatchDays), true)
	if err != nil {
		return BankAutoMatchResult{}, err
	}
	used := map[uuid.UUID]bool{}
	matched := 0
	days := func(a, b string) int {
		x, _ := time.Parse("2006-01-02", a)
		y, _ := time.Parse("2006-01-02", b)
		d := int(x.Sub(y).Hours() / 24)
		if d < 0 {
			d = -d
		}
		return d
	}
	refHit := func(t BankTransaction, l BankBookLine) bool {
		text := strings.ToUpper(t.Description + " " + deref(t.Reference))
		for _, s := range []string{deref(l.SourceRef), l.JournalNumber} {
			if s != "" && strings.Contains(text, strings.ToUpper(s)) {
				return true
			}
		}
		return false
	}
	var rest []BankTransaction
	for _, t := range r.UnmatchedTxns {
		amt := dec(t.Amount)
		best, bestScore := -1, 1<<30
		for i, l := range open {
			if used[l.LineID] || !dec(l.Amount).Equal(amt) {
				continue
			}
			dd := days(t.TxDate, l.JournalDate)
			if dd > cfg.AutoMatchDays {
				continue
			}
			score := dd * 10
			if refHit(t, l) {
				score -= 5
			}
			if score < bestScore {
				best, bestScore = i, score
			}
		}
		if best < 0 {
			rest = append(rest, t)
			continue
		}
		used[open[best].LineID] = true
		if err := m.matchLines(ctx, tx, property, rid, t, []BankBookLine{open[best]}, "auto"); err != nil {
			return BankAutoMatchResult{}, err
		}
		matched++
	}
	// aggregate: one statement line = the open lines of one source type on one date
	var left []BankTransaction
	for _, t := range rest {
		groups := map[string][]BankBookLine{}
		var keys []string
		for _, l := range open {
			if used[l.LineID] || l.JournalDate != t.TxDate || dec(l.Amount).Sign() != dec(t.Amount).Sign() {
				continue
			}
			k := l.SourceType
			if _, ok := groups[k]; !ok {
				keys = append(keys, k)
			}
			groups[k] = append(groups[k], l)
		}
		sort.Strings(keys)
		done := false
		for _, k := range keys {
			g := groups[k]
			if len(g) < 2 {
				continue
			}
			sum := decimal.Zero
			for _, l := range g {
				sum = sum.Add(dec(l.Amount))
			}
			if sum.Equal(dec(t.Amount)) {
				for _, l := range g {
					used[l.LineID] = true
				}
				if err := m.matchLines(ctx, tx, property, rid, t, g, "auto"); err != nil {
					return BankAutoMatchResult{}, err
				}
				matched++
				done = true
				break
			}
		}
		if !done {
			left = append(left, t)
		}
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return BankAutoMatchResult{}, err
	}
	res := BankAutoMatchResult{Matched: matched, Unmatched: len(left), Reconciliation: after}
	return res, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "auto_match", EntityType: "accounting.bank_reconciliation", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, After: map[string]any{"matched": matched, "unmatched": len(left), "difference": after.Difference}})
}

// BankMatchInput matches a statement line with book lines manually.
type BankMatchInput struct {
	BankTransactionID uuid.UUID   `json:"bankTransactionId"`
	JournalLineIDs    []uuid.UUID `json:"journalLineIds"`
}

func bankTxOf(ctx context.Context, q dbtx.Querier, property, bankAccount, txID uuid.UUID) (BankTransaction, error) {
	t, err := getOne[BankTransaction]("bank transaction")(q.Query(ctx, bankTxSelect+` WHERE t.id = $1 AND t.property_id = $2`, txID, property))
	if err != nil {
		return t, err
	}
	if t.BankAccountID != bankAccount {
		return t, handle.Invalid("bankTransactionId", "other_account", "the statement line belongs to another bank account")
	}
	return t, nil
}

// Match matches a statement line with book lines of the same total.
func (m *Module) Match(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in BankMatchInput) (BankReconciliation, error) {
	r, gl, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	t, err := bankTxOf(ctx, tx, property, r.BankAccountID, in.BankTransactionID)
	if err != nil {
		return r, err
	}
	if t.Status != "unmatched" {
		return r, errs.Conflict("not_unmatched", "the statement line is "+t.Status)
	}
	if len(in.JournalLineIDs) == 0 {
		return r, handle.Invalid("journalLineIds", "required", "choose the book lines")
	}
	sd, _ := time.Parse("2006-01-02", r.StatementDate)
	open, err := bookLines(ctx, tx, property, gl, sd.AddDate(0, 1, 0), true)
	if err != nil {
		return r, err
	}
	byID := map[uuid.UUID]BankBookLine{}
	for _, l := range open {
		byID[l.LineID] = l
	}
	var lines []BankBookLine
	sum := decimal.Zero
	for _, lid := range in.JournalLineIDs {
		l, ok := byID[lid]
		if !ok {
			return r, handle.Invalid("journalLineIds", "not_open", "a book line is not an open line of this bank account")
		}
		lines = append(lines, l)
		sum = sum.Add(dec(l.Amount))
	}
	if !sum.Equal(dec(t.Amount)) {
		return r, handle.Invalid("journalLineIds", "amount_mismatch", "the book lines total "+sum.String()+", the statement line "+t.Amount)
	}
	if err := m.matchLines(ctx, tx, property, rid, t, lines, "manual"); err != nil {
		return r, err
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "match", EntityType: "accounting.bank_transaction", EntityID: t.ID.String(),
		EntityLabel: t.TxDate + " " + t.Amount, PropertyID: &property, After: map[string]any{"reconciliationId": rid, "journalLineIds": in.JournalLineIDs}})
}

// BankUnmatchInput releases a matched statement line.
type BankUnmatchInput struct {
	BankTransactionID uuid.UUID `json:"bankTransactionId"`
}

// Unmatch releases a statement line matched in this reconciliation.
func (m *Module) Unmatch(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in BankUnmatchInput) (BankReconciliation, error) {
	r, _, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	t, err := bankTxOf(ctx, tx, property, r.BankAccountID, in.BankTransactionID)
	if err != nil {
		return r, err
	}
	if t.Status == "unmatched" || (t.ReconciliationID != nil && *t.ReconciliationID != rid) {
		return r, errs.Conflict("not_matched_here", "the statement line is not matched in this reconciliation")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM accounting.bank_matches WHERE bank_transaction_id = $1`, t.ID); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.bank_transactions SET status = 'unmatched', reconciliation_id = NULL, matched_at = NULL, matched_by = NULL
		WHERE id = $1`, t.ID); err != nil {
		return r, err
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "unmatch", EntityType: "accounting.bank_transaction", EntityID: t.ID.String(),
		EntityLabel: t.TxDate + " " + t.Amount, PropertyID: &property, Before: map[string]any{"status": t.Status}, After: map[string]any{"status": "unmatched"}})
}

// BankResolveLineInput settles a statement line without a book entry (bank
// charges, interest, direct debits) with a journal against an account.
type BankResolveLineInput struct {
	BankTransactionID uuid.UUID `json:"bankTransactionId"`
	AccountID         uuid.UUID `json:"accountId" doc:"Counter account, e.g. bank charges or interest income"`
	Description       string    `json:"description,omitempty"`
}

// ResolveLine posts the difference journal of a statement line and
// matches it (FR-BNK-03: differences are settled with a journal).
func (m *Module) ResolveLine(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in BankResolveLineInput) (BankReconciliation, error) {
	r, gl, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	t, err := bankTxOf(ctx, tx, property, r.BankAccountID, in.BankTransactionID)
	if err != nil {
		return r, err
	}
	if t.Status != "unmatched" {
		return r, errs.Conflict("not_unmatched", "the statement line is "+t.Status)
	}
	if in.AccountID == uuid.Nil {
		return r, handle.Invalid("accountId", "required", "choose the counter account")
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = t.Description
	}
	if desc == "" {
		desc = "Bank statement line " + t.TxDate
	}
	amt := dec(t.Amount)
	d, _ := time.Parse("2006-01-02", t.TxDate)
	counter := in.AccountID
	lines := []Line{{AccountID: gl, Debit: decimal.Max(amt, decimal.Zero), Credit: decimal.Max(amt.Neg(), decimal.Zero), Description: desc,
		SourceType: "accounting.bank_transaction", SourceID: t.ID.String()},
		{AccountID: counter, Debit: decimal.Max(amt.Neg(), decimal.Zero), Credit: decimal.Max(amt, decimal.Zero), Description: desc,
			SourceType: "accounting.bank_transaction", SourceID: t.ID.String()}}
	if err := lockProperty(ctx, tx, property); err != nil {
		return r, err
	}
	j, err := m.post(ctx, tx, Entry{Property: property, Date: d, Type: "adjustment", SourceType: "accounting.bank_transaction", SourceID: t.ID.String(),
		SourceRef: r.Number, Description: "Bank reconciliation " + r.Number + ": " + desc, Lines: lines}, false)
	if err != nil {
		return r, err
	}
	var lid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM accounting.journal_lines WHERE journal_id = $1 AND account_id = $2 LIMIT 1`, j.ID, gl).Scan(&lid); err != nil {
		return r, err
	}
	if err := m.matchLines(ctx, tx, property, rid, t, []BankBookLine{{LineID: lid, JournalID: j.ID, JournalNumber: j.Number, Amount: amt.String()}}, "journal"); err != nil {
		return r, err
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "resolve", EntityType: "accounting.bank_transaction", EntityID: t.ID.String(),
		EntityLabel: t.TxDate + " " + t.Amount, PropertyID: &property, After: map[string]any{"journalId": j.ID, "journalNumber": j.Number}})
}

// BankIgnoreLineInput ignores a statement line (e.g. a duplicate of the bank).
type BankIgnoreLineInput struct {
	BankTransactionID uuid.UUID `json:"bankTransactionId"`
	Reason            string    `json:"reason"`
}

// IgnoreLine marks a statement line as ignored.
func (m *Module) IgnoreLine(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in BankIgnoreLineInput) (BankReconciliation, error) {
	r, _, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	t, err := bankTxOf(ctx, tx, property, r.BankAccountID, in.BankTransactionID)
	if err != nil {
		return r, err
	}
	if t.Status != "unmatched" {
		return r, errs.Conflict("not_unmatched", "the statement line is "+t.Status)
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.bank_transactions SET status = 'ignored', reconciliation_id = $2, matched_at = now(), matched_by = $3 WHERE id = $1`,
		t.ID, rid, actorPtr(ctx)); err != nil {
		return r, err
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "ignore", EntityType: "accounting.bank_transaction", EntityID: t.ID.String(),
		EntityLabel: t.TxDate + " " + t.Amount, PropertyID: &property, Reason: in.Reason, Before: map[string]any{"status": "unmatched"},
		After: map[string]any{"status": "ignored"}})
}

// CompleteReconciliation closes a reconciliation without difference and
// without unmatched statement lines up to the statement date.
func (m *Module) CompleteReconciliation(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID) (BankReconciliation, error) {
	r, _, err := lockReconciliation(ctx, tx, property, rid)
	if err != nil {
		return r, err
	}
	if len(r.UnmatchedTxns) > 0 {
		return r, errs.Conflict("unmatched_lines", fmt.Sprintf("%d statement lines are not matched; match, resolve or ignore them", len(r.UnmatchedTxns)))
	}
	if !dec(r.Difference).IsZero() {
		return r, errs.Conflict("difference", "the reconciliation has a difference of "+r.Difference)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounting.bank_reconciliations SET status = 'completed', book_balance = $2::numeric, difference = 0, completed_at = now(),
		completed_by = $3 WHERE id = $1`, rid, r.BookBalance, actorPtr(ctx)); err != nil {
		return r, err
	}
	after, err := GetReconciliation(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: "complete", EntityType: "accounting.bank_reconciliation", EntityID: rid.String(),
		EntityLabel: r.Number, PropertyID: &property, Before: map[string]any{"status": "in_progress"},
		After: map[string]any{"status": "completed", "bookBalance": r.BookBalance, "statementBalance": r.StatementBalance}})
}

// ── cash documents (FR-BNK-04, FR-BNK-05) ─────────────────────────────────

// AccountingCashShift is a closed cashier or POS shift with its counted cash.
type AccountingCashShift struct {
	Source       string     `json:"source" db:"source" enum:"cashier_shift,pos_shift"`
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	Station      string     `json:"station" db:"station"`
	BusinessDate *string    `json:"businessDate" db:"business_date"`
	OpeningFloat string     `json:"openingFloat" db:"opening_float"`
	CountedCash  *string    `json:"countedCash" db:"counted_cash"`
	ExpectedCash *string    `json:"expectedCash" db:"expected_cash"`
	Variance     *string    `json:"variance" db:"variance"`
	ToDeposit    string     `json:"toDeposit" db:"to_deposit" doc:"Counted cash − opening float"`
	ClosedAt     *time.Time `json:"closedAt" db:"closed_at"`
	Deposited    bool       `json:"deposited" db:"deposited"`
}

// ListCashShifts lists the closed shifts of a date range for deposits.
func ListCashShifts(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) ([]AccountingCashShift, error) {
	return handle.List[AccountingCashShift](q.Query(ctx, `SELECT * FROM (
		SELECT 'cashier_shift' AS source, s.id, s.number, s.station, to_char(s.business_date, 'YYYY-MM-DD') AS business_date,
		  trim_scale(s.opening_float)::text AS opening_float, trim_scale(s.counted_cash)::text AS counted_cash, trim_scale(s.expected_cash)::text AS expected_cash,
		  trim_scale(s.variance)::text AS variance, trim_scale(coalesce(s.counted_cash, 0) - s.opening_float)::text AS to_deposit, s.closed_at
		FROM reporting.acc_cashier_shifts s WHERE s.property_id = $1 AND s.status = 'closed' AND s.business_date BETWEEN $2 AND $3
		UNION ALL
		SELECT 'pos_shift', s.id, s.shift_no, s.outlet_name, to_char(s.closed_date, 'YYYY-MM-DD'), trim_scale(s.opening_cash)::text,
		  trim_scale(s.counted_cash)::text, trim_scale(s.expected_cash)::text, trim_scale(s.variance)::text,
		  trim_scale(coalesce(s.counted_cash, 0) - s.opening_cash)::text, s.closed_at
		FROM reporting.acc_pos_shifts s WHERE s.property_id = $1 AND s.status = 'closed' AND s.closed_date BETWEEN $2 AND $3) x
		CROSS JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM accounting.cash_transactions c WHERE c.property_id = $1 AND c.kind = 'deposit' AND x.number = ANY (c.shift_refs)) AS deposited) d
		ORDER BY business_date, number`, property, dateOnly(from), dateOnly(to)))
}

// CashTransactionInput is a cash document.
type CashTransactionInput struct {
	Kind             string     `json:"kind" enum:"deposit,expense,replenishment,transfer,count"`
	TxDate           string     `json:"txDate"`
	FromAccountID    *uuid.UUID `json:"fromAccountId,omitempty" doc:"Cash account the money leaves (deposit, expense, transfer, count), bank of a replenishment"`
	ToAccountID      *uuid.UUID `json:"toAccountId,omitempty" doc:"Bank of a deposit, petty cash of a replenishment, target of a transfer"`
	Amount           string     `json:"amount" doc:"Deposited / spent / transferred amount, counted cash of a count"`
	Expected         string     `json:"expected,omitempty" doc:"Deposit: cash that should be deposited (default: the shifts' counted cash − float)"`
	Fee              string     `json:"fee,omitempty" doc:"Bank fee of a deposit or transfer"`
	ExpenseAccountID *uuid.UUID `json:"expenseAccountId,omitempty"`
	CostCenter       string     `json:"costCenter,omitempty"`
	ShiftRefs        []string   `json:"shiftRefs,omitempty" doc:"Numbers of the cashier / POS shifts deposited"`
	Reference        string     `json:"reference,omitempty"`
	Description      string     `json:"description,omitempty"`
}

// CashTransaction is a posted cash document.
type CashTransaction struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	Number           string     `json:"number" db:"number"`
	Kind             string     `json:"kind" db:"kind"`
	TxDate           string     `json:"txDate" db:"tx_date"`
	FromAccountID    *uuid.UUID `json:"fromAccountId" db:"from_account_id"`
	FromAccountName  *string    `json:"fromAccountName" db:"from_name"`
	ToAccountID      *uuid.UUID `json:"toAccountId" db:"to_account_id"`
	ToAccountName    *string    `json:"toAccountName" db:"to_name"`
	Expected         *string    `json:"expected" db:"expected"`
	Amount           string     `json:"amount" db:"amount"`
	Variance         string     `json:"variance" db:"variance"`
	Fee              string     `json:"fee" db:"fee"`
	ExpenseAccountID *uuid.UUID `json:"expenseAccountId" db:"expense_account_id"`
	ShiftRefs        []string   `json:"shiftRefs" db:"shift_refs"`
	Reference        *string    `json:"reference" db:"reference"`
	Description      *string    `json:"description" db:"description"`
	JournalID        *uuid.UUID `json:"journalId" db:"journal_id"`
	JournalNumber    *string    `json:"journalNumber" db:"journal_number"`
	CreatedAt        time.Time  `json:"createdAt" db:"created_at"`
}

const cashSelect = `SELECT c.id, c.number, c.kind, to_char(c.tx_date, 'YYYY-MM-DD') AS tx_date, c.from_account_id, fa.name AS from_name, c.to_account_id,
	ta.name AS to_name, trim_scale(c.expected)::text AS expected, trim_scale(c.amount)::text AS amount, trim_scale(c.variance)::text AS variance,
	trim_scale(c.fee)::text AS fee, c.expense_account_id, c.shift_refs, c.reference, c.description, c.journal_id,
	(SELECT j.number FROM accounting.journals j WHERE j.id = c.journal_id) AS journal_number, c.created_at
	FROM accounting.cash_transactions c LEFT JOIN accounting.bank_accounts fa ON fa.id = c.from_account_id
	LEFT JOIN accounting.bank_accounts ta ON ta.id = c.to_account_id`

// ListCashTransactions lists the cash documents of a property.
func ListCashTransactions(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind string) ([]CashTransaction, error) {
	return handle.List[CashTransaction](q.Query(ctx, cashSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.kind = $2) ORDER BY c.tx_date DESC, c.number DESC LIMIT 500`,
		property, kind))
}

// CreateCashTransaction posts a cash document.
func (m *Module) CreateCashTransaction(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CashTransactionInput) (CashTransaction, error) {
	d, err := parseDate("txDate", in.TxDate)
	if err != nil {
		return CashTransaction{}, err
	}
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return CashTransaction{}, err
	}
	fee, err := handle.Decimal("fee", in.Fee, decimal.Zero)
	if err != nil {
		return CashTransaction{}, err
	}
	if amt.IsNegative() || fee.IsNegative() || (amt.IsZero() && in.Kind != "count") {
		return CashTransaction{}, handle.Invalid("amount", "invalid", "a positive amount")
	}
	acct := func(field string, bid *uuid.UUID) (BankAccountInfo, error) {
		if bid == nil {
			return BankAccountInfo{}, handle.Invalid(field, "required", "choose the cash / bank account")
		}
		b, err := bankAccountOf(ctx, tx, property, *bid)
		if err != nil {
			if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
				return b, handle.Invalid(field, "not_found", "cash / bank account not found")
			}
		}
		return b, err
	}
	cfg, err := LoadConfiguration(ctx, tx, property)
	if err != nil {
		return CashTransaction{}, err
	}
	res := &resolver{tx: tx, ctx: ctx, property: property, cfg: cfg, byCode: map[string]acctInfo{}, byID: map[uuid.UUID]acctInfo{}, held: map[int]bool{}}
	roleAcct := func(role string) (uuid.UUID, error) {
		a, err := res.role(role)
		if err != nil {
			return uuid.Nil, err
		}
		if a.Err != "" {
			return uuid.Nil, errs.Conflict("missing_account", a.Err)
		}
		return a.ID, nil
	}
	desc := strings.TrimSpace(in.Description)
	var lines []Line
	add := func(account uuid.UUID, debit, credit decimal.Decimal, cc string) {
		if debit.IsZero() && credit.IsZero() {
			return
		}
		lines = append(lines, Line{AccountID: account, Debit: debit, Credit: credit, Description: desc, Dims: Dims{CostCenter: cc}})
	}
	expected := amt
	variance := decimal.Zero
	var from, to *uuid.UUID
	switch in.Kind {
	case "deposit":
		cash, err := acct("fromAccountId", in.FromAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		bank, err := acct("toAccountId", in.ToAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		if cash.Kind == "bank" || bank.Kind != "bank" {
			return CashTransaction{}, handle.Invalid("toAccountId", "invalid", "a deposit moves cash into a bank account")
		}
		switch {
		case strings.TrimSpace(in.Expected) != "":
			if expected, err = handle.Decimal("expected", in.Expected, amt); err != nil {
				return CashTransaction{}, err
			}
		case len(in.ShiftRefs) > 0:
			shifts, err := ListCashShifts(ctx, tx, property, d.AddDate(0, 0, -62), d)
			if err != nil {
				return CashTransaction{}, err
			}
			expected = decimal.Zero
			for _, ref := range in.ShiftRefs {
				found := false
				for _, s := range shifts {
					if s.Number == ref {
						if s.Deposited {
							return CashTransaction{}, handle.Invalid("shiftRefs", "deposited", "shift "+ref+" is already deposited")
						}
						expected, found = expected.Add(dec(s.ToDeposit)), true
					}
				}
				if !found {
					return CashTransaction{}, handle.Invalid("shiftRefs", "not_found", "closed shift "+ref+" not found")
				}
			}
		}
		variance = amt.Sub(expected)
		if desc == "" {
			desc = "Cash deposit " + cash.Name + " → " + bank.Name
		}
		over, err := roleAcct("cash_over_short")
		if err != nil {
			return CashTransaction{}, err
		}
		add(bank.GLAccountID, amt.Sub(fee), decimal.Zero, "")
		if fee.IsPositive() {
			charges, err := roleAcct("bank_charges")
			if err != nil {
				return CashTransaction{}, err
			}
			add(charges, fee, decimal.Zero, "")
		}
		add(cash.GLAccountID, decimal.Zero, expected, "")
		if variance.IsNegative() {
			add(over, variance.Neg(), decimal.Zero, "")
		} else {
			add(over, decimal.Zero, variance, "")
		}
		from, to = &cash.ID, &bank.ID
	case "expense":
		cash, err := acct("fromAccountId", in.FromAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		if in.ExpenseAccountID == nil {
			return CashTransaction{}, handle.Invalid("expenseAccountId", "required", "choose the expense account")
		}
		ea, err := res.accountByID(*in.ExpenseAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		if ea.Err != "" {
			return CashTransaction{}, handle.Invalid("expenseAccountId", "invalid_account", ea.Err)
		}
		if err := handle.Required("description", desc); err != nil {
			return CashTransaction{}, err
		}
		add(*in.ExpenseAccountID, amt, decimal.Zero, in.CostCenter)
		add(cash.GLAccountID, decimal.Zero, amt, "")
		from = &cash.ID
	case "replenishment", "transfer":
		src, err := acct("fromAccountId", in.FromAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		dst, err := acct("toAccountId", in.ToAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		if src.ID == dst.ID {
			return CashTransaction{}, handle.Invalid("toAccountId", "same_account", "choose two different accounts")
		}
		if in.Kind == "replenishment" && dst.Kind != "petty_cash" {
			return CashTransaction{}, handle.Invalid("toAccountId", "invalid", "a replenishment tops up a petty cash account")
		}
		if desc == "" {
			desc = strings.ToUpper(in.Kind[:1]) + in.Kind[1:] + " " + src.Name + " → " + dst.Name
		}
		add(dst.GLAccountID, amt, decimal.Zero, "")
		if fee.IsPositive() {
			charges, err := roleAcct("bank_charges")
			if err != nil {
				return CashTransaction{}, err
			}
			add(charges, fee, decimal.Zero, "")
		}
		add(src.GLAccountID, decimal.Zero, amt.Add(fee), "")
		from, to = &src.ID, &dst.ID
	case "count":
		cash, err := acct("fromAccountId", in.FromAccountID)
		if err != nil {
			return CashTransaction{}, err
		}
		if cash.Kind == "bank" {
			return CashTransaction{}, handle.Invalid("fromAccountId", "invalid", "count a cash or petty cash account")
		}
		book, err := accountBalance(ctx, tx, &property, cash.GLAccountID, &d)
		if err != nil {
			return CashTransaction{}, err
		}
		expected = book
		variance = amt.Sub(book)
		if desc == "" {
			desc = "Cash count " + cash.Name
		}
		over, err := roleAcct("cash_over_short")
		if err != nil {
			return CashTransaction{}, err
		}
		if variance.IsNegative() {
			add(over, variance.Neg(), decimal.Zero, "")
			add(cash.GLAccountID, decimal.Zero, variance.Neg(), "")
		} else {
			add(cash.GLAccountID, variance, decimal.Zero, "")
			add(over, decimal.Zero, variance, "")
		}
		from = &cash.ID
	default:
		return CashTransaction{}, handle.Invalid("kind", "invalid", "deposit, expense, replenishment, transfer or count")
	}
	cid := id.New()
	num, err := numbering.Next(ctx, tx, property, "CASH", d)
	if err != nil {
		return CashTransaction{}, err
	}
	var jid *uuid.UUID
	if len(lines) >= 2 {
		if err := lockProperty(ctx, tx, property); err != nil {
			return CashTransaction{}, err
		}
		for i := range lines {
			lines[i].SourceType, lines[i].SourceID = "accounting.cash_transaction", cid.String()
		}
		j, err := m.post(ctx, tx, Entry{Property: property, Date: d, Type: "automatic", SourceType: "accounting.cash_transaction", SourceID: cid.String(),
			SourceRef: num, Description: desc, Lines: lines, Merge: true}, false)
		if err != nil {
			return CashTransaction{}, err
		}
		jid = &j.ID
	}
	refs := in.ShiftRefs
	if refs == nil {
		refs = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO accounting.cash_transactions (id, property_id, number, kind, tx_date, from_account_id, to_account_id, expected, amount,
		variance, fee, expense_account_id, shift_refs, reference, description, journal_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12,$13,$14,$15,$16,$17)`,
		cid, property, num, in.Kind, d, from, to, expected.String(), amt.String(), variance.String(), fee.String(), in.ExpenseAccountID, refs,
		nz(in.Reference), nz(desc), jid, actorPtr(ctx)); err != nil {
		return CashTransaction{}, err
	}
	c, err := getOne[CashTransaction]("cash transaction")(tx.Query(ctx, cashSelect+` WHERE c.id = $1`, cid))
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: "accounting", Action: audit.ActionCreate, EntityType: "accounting.cash_transaction", EntityID: cid.String(),
		EntityLabel: num + " · " + in.Kind, PropertyID: &property, After: c})
}
