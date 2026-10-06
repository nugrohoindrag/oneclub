package accounting

// FR-ACC-05 General Ledger & Trial Balance and EP-22 Financial Reports:
// Profit & Loss, Balance Sheet, Cash Flow (indirect method) and Revenue by
// Business Line per property or consolidated over every property in scope
// (MAIN + MDR, one legal entity: no eliminations, FR-FIN-03), with the
// previous period / last year comparison (FR-FIN-02). Every row carries
// its account for the drill-down to the general ledger, the journals and
// their source documents (FR-FIN-04). Year-end closing journals are left
// out of the P&L so the closed year still reports its result; the Balance
// Sheet shows the result not yet closed as current earnings, so it always
// balances and its earnings change equals the P&L net income.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// GLLedgerLine is one line of the general ledger.
type GLLedgerLine struct {
	LineID        uuid.UUID  `json:"lineId" db:"line_id"`
	JournalID     uuid.UUID  `json:"journalId" db:"journal_id"`
	JournalNumber string     `json:"journalNumber" db:"journal_number"`
	JournalDate   string     `json:"journalDate" db:"journal_date"`
	JournalType   string     `json:"journalType" db:"journal_type"`
	SourceType    string     `json:"sourceType" db:"source_type"`
	SourceRef     *string    `json:"sourceRef" db:"source_ref"`
	Description   string     `json:"description" db:"description"`
	Debit         string     `json:"debit" db:"debit"`
	Credit        string     `json:"credit" db:"credit"`
	Balance       string     `json:"balance" db:"-" doc:"Running balance (debit − credit)"`
	BusinessLine  *string    `json:"businessLine" db:"business_line"`
	CostCenter    *string    `json:"costCenter" db:"cost_center"`
	PartnerName   *string    `json:"partnerName" db:"partner_name"`
	PropertyID    uuid.UUID  `json:"propertyId" db:"property_id"`
	DepartmentID  *uuid.UUID `json:"departmentId" db:"department_id"`
}

// GeneralLedger is the ledger of one account for a date range.
type GeneralLedger struct {
	AccountID      uuid.UUID      `json:"accountId"`
	AccountCode    string         `json:"accountCode"`
	AccountName    string         `json:"accountName"`
	NormalBalance  string         `json:"normalBalance"`
	From           string         `json:"from"`
	To             string         `json:"to"`
	OpeningBalance string         `json:"openingBalance"`
	Debit          string         `json:"debit"`
	Credit         string         `json:"credit"`
	ClosingBalance string         `json:"closingBalance"`
	Lines          []GLLedgerLine `json:"lines"`
}

// LedgerFilter selects ledger lines.
type LedgerFilter struct {
	Property     *uuid.UUID
	From, To     time.Time
	BusinessLine string
	CostCenter   string
	DepartmentID *uuid.UUID
}

// GeneralLedgerOf returns the ledger of an account (FR-ACC-05).
func GeneralLedgerOf(ctx context.Context, q dbtx.Querier, accountID uuid.UUID, f LedgerFilter) (GeneralLedger, error) {
	g := GeneralLedger{AccountID: accountID, From: ymd(f.From), To: ymd(f.To), Lines: []GLLedgerLine{}}
	if err := q.QueryRow(ctx, `SELECT code, name, normal_balance FROM accounting.accounts WHERE id = $1`, accountID).Scan(&g.AccountCode, &g.AccountName,
		&g.NormalBalance); err != nil {
		if dbtx.IsNoRows(err) {
			return g, errs.NotFound("account")
		}
		return g, err
	}
	var prop any
	if f.Property != nil {
		prop = *f.Property
	}
	var opening *string
	if err := q.QueryRow(ctx, `SELECT sum(debit - credit)::text FROM accounting.journal_lines WHERE account_id = $1 AND ($2::uuid IS NULL OR property_id = $2)
		AND journal_date < $3 AND ($4 = '' OR business_line = $4) AND ($5 = '' OR cost_center = $5) AND ($6::uuid IS NULL OR department_id = $6)`,
		accountID, prop, dateOnly(f.From), f.BusinessLine, f.CostCenter, f.DepartmentID).Scan(&opening); err != nil {
		return g, err
	}
	lines, err := handle.List[GLLedgerLine](q.Query(ctx, `SELECT l.id AS line_id, l.journal_id, j.number AS journal_number, to_char(l.journal_date, 'YYYY-MM-DD') AS journal_date,
		j.journal_type, j.source_type, j.source_ref, coalesce(l.description, j.description) AS description, trim_scale(l.debit)::text AS debit,
		trim_scale(l.credit)::text AS credit, l.business_line, l.cost_center, l.partner_name, l.property_id, l.department_id
		FROM accounting.journal_lines l JOIN accounting.journals j ON j.id = l.journal_id
		WHERE l.account_id = $1 AND ($2::uuid IS NULL OR l.property_id = $2) AND l.journal_date BETWEEN $3 AND $4
		AND ($5 = '' OR l.business_line = $5) AND ($6 = '' OR l.cost_center = $6) AND ($7::uuid IS NULL OR l.department_id = $7)
		ORDER BY l.journal_date, j.posted_at, j.number, l.line_no LIMIT 20000`, accountID, prop, dateOnly(f.From), dateOnly(f.To), f.BusinessLine, f.CostCenter,
		f.DepartmentID))
	if err != nil {
		return g, err
	}
	bal := decp(opening)
	g.OpeningBalance = bal.String()
	dr, cr := decimal.Zero, decimal.Zero
	for i := range lines {
		d, c := dec(lines[i].Debit), dec(lines[i].Credit)
		dr, cr = dr.Add(d), cr.Add(c)
		bal = bal.Add(d).Sub(c)
		lines[i].Balance = bal.String()
	}
	g.Lines, g.Debit, g.Credit, g.ClosingBalance = lines, dr.String(), cr.String(), bal.String()
	return g, nil
}

// TrialBalanceRow is one account of the trial balance.
type TrialBalanceRow struct {
	AccountID     uuid.UUID `json:"accountId" db:"account_id"`
	Code          string    `json:"code" db:"code"`
	Name          string    `json:"name" db:"name"`
	AccountType   string    `json:"accountType" db:"account_type"`
	NormalBalance string    `json:"normalBalance" db:"normal_balance"`
	Opening       string    `json:"opening" db:"opening" doc:"Debit − credit before the period"`
	Debit         string    `json:"debit" db:"debit"`
	Credit        string    `json:"credit" db:"credit"`
	Closing       string    `json:"closing" db:"closing"`
	ClosingDebit  string    `json:"closingDebit" db:"closing_debit"`
	ClosingCredit string    `json:"closingCredit" db:"closing_credit"`
}

// TrialBalance is the trial balance of a period.
type TrialBalance struct {
	From         string            `json:"from"`
	To           string            `json:"to"`
	Consolidated bool              `json:"consolidated"`
	Rows         []TrialBalanceRow `json:"rows"`
	TotalDebit   string            `json:"totalDebit" doc:"Σ closing debit balances"`
	TotalCredit  string            `json:"totalCredit" doc:"Σ closing credit balances"`
	PeriodDebit  string            `json:"periodDebit"`
	PeriodCredit string            `json:"periodCredit"`
	Balanced     bool              `json:"balanced"`
}

// TrialBalanceOf computes the trial balance (FR-ACC-05).
func TrialBalanceOf(ctx context.Context, q dbtx.Querier, property *uuid.UUID, from, to time.Time) (TrialBalance, error) {
	tb := TrialBalance{From: ymd(from), To: ymd(to), Consolidated: property == nil}
	var prop any
	if property != nil {
		prop = *property
	}
	rows, err := handle.List[TrialBalanceRow](q.Query(ctx, `WITH m AS (SELECT l.account_id,
		  coalesce(sum(l.debit - l.credit) FILTER (WHERE l.journal_date < $2), 0) AS opening,
		  coalesce(sum(l.debit) FILTER (WHERE l.journal_date >= $2), 0) AS debit, coalesce(sum(l.credit) FILTER (WHERE l.journal_date >= $2), 0) AS credit
		FROM accounting.journal_lines l WHERE ($1::uuid IS NULL OR l.property_id = $1) AND l.journal_date <= $3 GROUP BY l.account_id)
		SELECT a.id AS account_id, a.code, a.name, a.account_type, a.normal_balance, trim_scale(m.opening)::text AS opening, trim_scale(m.debit)::text AS debit,
		  trim_scale(m.credit)::text AS credit, trim_scale(m.opening + m.debit - m.credit)::text AS closing,
		  trim_scale(greatest(m.opening + m.debit - m.credit, 0))::text AS closing_debit, trim_scale(greatest(-(m.opening + m.debit - m.credit), 0))::text AS closing_credit
		FROM m JOIN accounting.accounts a ON a.id = m.account_id WHERE m.opening <> 0 OR m.debit <> 0 OR m.credit <> 0 ORDER BY a.code`,
		prop, dateOnly(from), dateOnly(to)))
	if err != nil {
		return tb, err
	}
	td, tc, pd, pc := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	for _, r := range rows {
		td, tc, pd, pc = td.Add(dec(r.ClosingDebit)), tc.Add(dec(r.ClosingCredit)), pd.Add(dec(r.Debit)), pc.Add(dec(r.Credit))
	}
	tb.Rows, tb.TotalDebit, tb.TotalCredit, tb.PeriodDebit, tb.PeriodCredit = rows, td.String(), tc.String(), pd.String(), pc.String()
	tb.Balanced = td.Equal(tc) && pd.Equal(pc)
	return tb, nil
}

// ── statements ────────────────────────────────────────────────────────────

// FinancialStatementRow is a line of a financial statement.
type FinancialStatementRow struct {
	Section   string     `json:"section"`
	AccountID *uuid.UUID `json:"accountId,omitempty" doc:"Drill-down to the general ledger"`
	Code      string     `json:"code,omitempty"`
	Name      string     `json:"name"`
	Amount    string     `json:"amount"`
	Compare   *string    `json:"compare,omitempty" doc:"Amount of the comparison period"`
	Total     bool       `json:"total,omitempty"`
}

// FinancialStatement is a P&L, Balance Sheet, Cash Flow or Revenue by
// Business Line report.
type FinancialStatement struct {
	Kind         string                  `json:"kind" enum:"profit-loss,balance-sheet,cash-flow,revenue-by-business-line"`
	From         string                  `json:"from,omitempty"`
	To           string                  `json:"to"`
	CompareFrom  string                  `json:"compareFrom,omitempty"`
	CompareTo    string                  `json:"compareTo,omitempty"`
	Consolidated bool                    `json:"consolidated"`
	Rows         []FinancialStatementRow `json:"rows"`
	Totals       map[string]string       `json:"totals"`
	Balanced     *bool                   `json:"balanced,omitempty" doc:"Balance Sheet: assets = liabilities + equity"`
}

type acctAmount struct {
	AccountID uuid.UUID `db:"account_id"`
	Code      string    `db:"code"`
	Name      string    `db:"name"`
	Type      string    `db:"account_type"`
	Subtype   string    `db:"subtype"`
	CashFlow  string    `db:"cash_flow"`
	Amount    string    `db:"amount"`
}

// movements returns debit − credit per account between two dates
// (inclusive; from zero = since the beginning) excluding journal types.
func movements(ctx context.Context, q dbtx.Querier, property *uuid.UUID, from, to time.Time, exclude []string) (map[uuid.UUID]acctAmount, error) {
	var prop, f any
	if property != nil {
		prop = *property
	}
	if !from.IsZero() {
		f = dateOnly(from)
	}
	if exclude == nil {
		exclude = []string{}
	}
	rows, err := handle.List[acctAmount](q.Query(ctx, `SELECT a.id AS account_id, a.code, a.name, a.account_type, a.subtype, a.cash_flow,
		sum(l.debit - l.credit)::text AS amount FROM accounting.journal_lines l JOIN accounting.journals j ON j.id = l.journal_id
		JOIN accounting.accounts a ON a.id = l.account_id WHERE ($1::uuid IS NULL OR l.property_id = $1) AND ($2::date IS NULL OR l.journal_date >= $2)
		AND l.journal_date <= $3 AND NOT (j.journal_type = ANY ($4)) GROUP BY a.id ORDER BY a.code`, prop, f, dateOnly(to), exclude))
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]acctAmount, len(rows))
	for _, r := range rows {
		out[r.AccountID] = r
	}
	return out, nil
}

type accountRef struct {
	ID       uuid.UUID `db:"id"`
	Code     string    `db:"code"`
	Name     string    `db:"name"`
	Type     string    `db:"account_type"`
	Subtype  string    `db:"subtype"`
	CashFlow string    `db:"cash_flow"`
	Posting  bool      `db:"is_posting"`
}

func allAccounts(ctx context.Context, q dbtx.Querier) ([]accountRef, error) {
	return handle.List[accountRef](q.Query(ctx, `SELECT id, code, name, account_type, subtype, cash_flow, is_posting FROM accounting.accounts
		WHERE is_posting ORDER BY code`))
}

// compareRange returns the comparison range of a period.
func compareRange(from, to time.Time, mode string) (time.Time, time.Time, bool) {
	switch mode {
	case "previous_period":
		if from.Day() == 1 && to.Equal(monthEnd(to)) {
			months := (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month()) + 1
			f := from.AddDate(0, -months, 0)
			return f, from.AddDate(0, 0, -1), true
		}
		days := int(to.Sub(from).Hours()/24) + 1
		return from.AddDate(0, 0, -days), from.AddDate(0, 0, -1), true
	case "last_year":
		return from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0), true
	}
	return time.Time{}, time.Time{}, false
}

var plSections = []struct{ key, label string }{{"revenue", "Revenue"}, {"cogs", "Cost of Sales"}, {"expense", "Operating Expenses"},
	{"other", "Other Income & Expenses"}}

func plSection(a accountRef) string {
	switch {
	case a.Subtype == "other_income" || a.Subtype == "other_expense":
		return "other"
	case a.Type == "revenue":
		return "revenue"
	case a.Type == "cogs":
		return "cogs"
	case a.Type == "expense":
		return "expense"
	}
	return ""
}

// ProfitLoss is the P&L of a period: revenue and other income as credit
// − debit, costs as debit − credit (FR-FIN-01).
func ProfitLoss(ctx context.Context, q dbtx.Querier, property *uuid.UUID, from, to time.Time, compare string) (FinancialStatement, error) {
	fs := FinancialStatement{Kind: "profit-loss", From: ymd(from), To: ymd(to), Consolidated: property == nil, Rows: []FinancialStatementRow{}, Totals: map[string]string{}}
	accts, err := allAccounts(ctx, q)
	if err != nil {
		return fs, err
	}
	cur, err := movements(ctx, q, property, from, to, []string{"closing"})
	if err != nil {
		return fs, err
	}
	var prev map[uuid.UUID]acctAmount
	cf, ct, hasCmp := compareRange(from, to, compare)
	if hasCmp {
		fs.CompareFrom, fs.CompareTo = ymd(cf), ymd(ct)
		if prev, err = movements(ctx, q, property, cf, ct, []string{"closing"}); err != nil {
			return fs, err
		}
	}
	tot, ptot := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for _, s := range plSections {
		var rows []FinancialStatementRow
		for _, a := range accts {
			if plSection(a) != s.key {
				continue
			}
			v, pv := dec(cur[a.ID].Amount).Neg(), dec(prev[a.ID].Amount).Neg() // credit positive
			if s.key == "cogs" || s.key == "expense" {
				v, pv = v.Neg(), pv.Neg()
			}
			if v.IsZero() && pv.IsZero() {
				continue
			}
			tot[s.key], ptot[s.key] = tot[s.key].Add(v), ptot[s.key].Add(pv)
			aid := a.ID
			r := FinancialStatementRow{Section: s.key, AccountID: &aid, Code: a.Code, Name: a.Name, Amount: v.String()}
			if hasCmp {
				p := pv.String()
				r.Compare = &p
			}
			rows = append(rows, r)
		}
		fs.Rows = append(fs.Rows, rows...)
		fs.Rows = append(fs.Rows, totalRow(s.key, "Total "+s.label, tot[s.key], ptot[s.key], hasCmp))
		if s.key == "cogs" {
			gp, pgp := tot["revenue"].Sub(tot["cogs"]), ptot["revenue"].Sub(ptot["cogs"])
			fs.Rows = append(fs.Rows, totalRow("gross_profit", "Gross Profit", gp, pgp, hasCmp))
			fs.Totals["grossProfit"] = gp.String()
		}
	}
	net := tot["revenue"].Sub(tot["cogs"]).Sub(tot["expense"]).Add(tot["other"])
	pnet := ptot["revenue"].Sub(ptot["cogs"]).Sub(ptot["expense"]).Add(ptot["other"])
	fs.Rows = append(fs.Rows, totalRow("net_income", "Net Income", net, pnet, hasCmp))
	fs.Totals["revenue"], fs.Totals["cogs"], fs.Totals["expense"], fs.Totals["other"] = tot["revenue"].String(), tot["cogs"].String(),
		tot["expense"].String(), tot["other"].String()
	fs.Totals["netIncome"] = net.String()
	if hasCmp {
		fs.Totals["compareNetIncome"] = pnet.String()
	}
	return fs, nil
}

func totalRow(section, name string, v, pv decimal.Decimal, cmp bool) FinancialStatementRow {
	r := FinancialStatementRow{Section: section, Name: name, Amount: v.String(), Total: true}
	if cmp {
		p := pv.String()
		r.Compare = &p
	}
	return r
}

// BalanceSheet is the balance sheet at a date: assets = liabilities +
// equity + earnings not yet closed to retained earnings.
func BalanceSheet(ctx context.Context, q dbtx.Querier, property *uuid.UUID, asOf time.Time, compare string) (FinancialStatement, error) {
	fs := FinancialStatement{Kind: "balance-sheet", To: ymd(asOf), Consolidated: property == nil, Rows: []FinancialStatementRow{}, Totals: map[string]string{}}
	accts, err := allAccounts(ctx, q)
	if err != nil {
		return fs, err
	}
	cur, err := movements(ctx, q, property, time.Time{}, asOf, nil)
	if err != nil {
		return fs, err
	}
	var prev map[uuid.UUID]acctAmount
	hasCmp := false
	var cmpDate time.Time
	switch compare {
	case "previous_period":
		cmpDate, hasCmp = monthStart(asOf).AddDate(0, 0, -1), true
	case "last_year":
		cmpDate, hasCmp = asOf.AddDate(-1, 0, 0), true
	}
	if hasCmp {
		fs.CompareTo = ymd(cmpDate)
		if prev, err = movements(ctx, q, property, time.Time{}, cmpDate, nil); err != nil {
			return fs, err
		}
	}
	tot, ptot := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	earn, pearn := decimal.Zero, decimal.Zero
	for _, a := range accts {
		v, pv := dec(cur[a.ID].Amount), dec(prev[a.ID].Amount)
		switch a.Type {
		case "revenue", "expense", "cogs":
			earn, pearn = earn.Sub(v), pearn.Sub(pv)
		}
	}
	for _, s := range []struct{ key, label string }{{"asset", "Assets"}, {"liability", "Liabilities"}, {"equity", "Equity"}} {
		for _, a := range accts {
			if a.Type != s.key {
				continue
			}
			v, pv := dec(cur[a.ID].Amount), dec(prev[a.ID].Amount)
			if s.key != "asset" {
				v, pv = v.Neg(), pv.Neg()
			}
			if v.IsZero() && pv.IsZero() {
				continue
			}
			tot[s.key], ptot[s.key] = tot[s.key].Add(v), ptot[s.key].Add(pv)
			aid := a.ID
			r := FinancialStatementRow{Section: s.key, AccountID: &aid, Code: a.Code, Name: a.Name, Amount: v.String()}
			if hasCmp {
				p := pv.String()
				r.Compare = &p
			}
			fs.Rows = append(fs.Rows, r)
		}
		if s.key == "equity" {
			r := FinancialStatementRow{Section: "equity", Name: "Current Earnings (not yet closed)", Amount: earn.String()}
			if hasCmp {
				p := pearn.String()
				r.Compare = &p
			}
			fs.Rows = append(fs.Rows, r)
			tot["equity"], ptot["equity"] = tot["equity"].Add(earn), ptot["equity"].Add(pearn)
		}
		fs.Rows = append(fs.Rows, totalRow(s.key, "Total "+s.label, tot[s.key], ptot[s.key], hasCmp))
	}
	le, ple := tot["liability"].Add(tot["equity"]), ptot["liability"].Add(ptot["equity"])
	fs.Rows = append(fs.Rows, totalRow("liabilities_equity", "Total Liabilities & Equity", le, ple, hasCmp))
	ok := tot["asset"].Equal(le) && ptot["asset"].Equal(ple)
	fs.Balanced = &ok
	fs.Totals["assets"], fs.Totals["liabilities"], fs.Totals["equity"], fs.Totals["currentEarnings"] = tot["asset"].String(), tot["liability"].String(),
		tot["equity"].String(), earn.String()
	fs.Totals["liabilitiesAndEquity"] = le.String()
	return fs, nil
}

// CashFlow is the cash flow statement of a period (indirect method):
// net income, adjusted by depreciation and the change of the operating
// balance sheet accounts, investing and financing flows, against the
// change of the cash and bank accounts. Opening and year-end closing
// journals are not cash flows.
func CashFlow(ctx context.Context, q dbtx.Querier, property *uuid.UUID, from, to time.Time, compare string) (FinancialStatement, error) {
	fs := FinancialStatement{Kind: "cash-flow", From: ymd(from), To: ymd(to), Consolidated: property == nil, Rows: []FinancialStatementRow{}, Totals: map[string]string{}}
	accts, err := allAccounts(ctx, q)
	if err != nil {
		return fs, err
	}
	compute := func(f, t time.Time) (map[string]decimal.Decimal, map[uuid.UUID]decimal.Decimal, error) {
		mv, err := movements(ctx, q, property, f, t, []string{"closing", "opening"})
		if err != nil {
			return nil, nil, err
		}
		tot := map[string]decimal.Decimal{}
		per := map[uuid.UUID]decimal.Decimal{}
		for _, a := range accts {
			v := dec(mv[a.ID].Amount)
			if v.IsZero() {
				continue
			}
			switch {
			case a.Type == "revenue" || a.Type == "expense" || a.Type == "cogs":
				tot["net_income"] = tot["net_income"].Sub(v)
			case a.CashFlow == "cash":
				tot["cash_change"] = tot["cash_change"].Add(v)
			case a.Subtype == "accumulated_depreciation":
				tot["depreciation"] = tot["depreciation"].Sub(v)
				per[a.ID] = v.Neg()
			case a.CashFlow == "investing":
				tot["investing"] = tot["investing"].Sub(v)
				per[a.ID] = v.Neg()
			case a.CashFlow == "financing":
				tot["financing"] = tot["financing"].Sub(v)
				per[a.ID] = v.Neg()
			default:
				tot["working_capital"] = tot["working_capital"].Sub(v)
				per[a.ID] = v.Neg()
			}
		}
		tot["operating"] = tot["net_income"].Add(tot["depreciation"]).Add(tot["working_capital"])
		tot["net_change"] = tot["operating"].Add(tot["investing"]).Add(tot["financing"])
		return tot, per, nil
	}
	tot, per, err := compute(from, to)
	if err != nil {
		return fs, err
	}
	var ptot map[string]decimal.Decimal
	pper := map[uuid.UUID]decimal.Decimal{}
	cf, ct, hasCmp := compareRange(from, to, compare)
	if hasCmp {
		fs.CompareFrom, fs.CompareTo = ymd(cf), ymd(ct)
		if ptot, pper, err = compute(cf, ct); err != nil {
			return fs, err
		}
	}
	section := func(key, label string, filter func(a accountRef) bool) {
		for _, a := range accts {
			if !filter(a) {
				continue
			}
			v, pv := per[a.ID], pper[a.ID]
			if v.IsZero() && pv.IsZero() {
				continue
			}
			aid := a.ID
			r := FinancialStatementRow{Section: key, AccountID: &aid, Code: a.Code, Name: "Change in " + a.Name, Amount: v.String()}
			if hasCmp {
				p := pv.String()
				r.Compare = &p
			}
			fs.Rows = append(fs.Rows, r)
		}
	}
	isPL := func(a accountRef) bool { return a.Type == "revenue" || a.Type == "expense" || a.Type == "cogs" }
	fs.Rows = append(fs.Rows, totalRow("operating", "Net Income", tot["net_income"], ptot["net_income"], hasCmp))
	section("operating", "Depreciation", func(a accountRef) bool { return a.Subtype == "accumulated_depreciation" })
	section("operating", "Working capital", func(a accountRef) bool {
		return !isPL(a) && a.CashFlow != "cash" && a.Subtype != "accumulated_depreciation" && a.CashFlow != "investing" && a.CashFlow != "financing"
	})
	fs.Rows = append(fs.Rows, totalRow("operating", "Net Cash from Operating Activities", tot["operating"], ptot["operating"], hasCmp))
	section("investing", "Investing", func(a accountRef) bool {
		return !isPL(a) && a.CashFlow == "investing" && a.Subtype != "accumulated_depreciation"
	})
	fs.Rows = append(fs.Rows, totalRow("investing", "Net Cash from Investing Activities", tot["investing"], ptot["investing"], hasCmp))
	section("financing", "Financing", func(a accountRef) bool { return !isPL(a) && a.CashFlow == "financing" })
	fs.Rows = append(fs.Rows, totalRow("financing", "Net Cash from Financing Activities", tot["financing"], ptot["financing"], hasCmp))
	fs.Rows = append(fs.Rows, totalRow("net_change", "Net Change in Cash", tot["net_change"], ptot["net_change"], hasCmp))
	var opening decimal.Decimal
	for _, a := range accts {
		if a.CashFlow != "cash" {
			continue
		}
		b, err := accountBalance(ctx, q, property, a.ID, ptrTime(from.AddDate(0, 0, -1)))
		if err != nil {
			return fs, err
		}
		opening = opening.Add(b)
	}
	fs.Rows = append(fs.Rows, totalRow("cash", "Cash at Beginning of Period", opening, decimal.Zero, false))
	fs.Rows = append(fs.Rows, totalRow("cash", "Cash at End of Period", opening.Add(tot["cash_change"]), decimal.Zero, false))
	for k, v := range tot {
		fs.Totals[k] = v.String()
	}
	ok := tot["net_change"].Equal(tot["cash_change"])
	fs.Balanced = &ok
	return fs, nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// RevenueByBusinessLine is the revenue of a period per business line
// (dimension of the revenue lines; credit − debit), FR-FIN-01.
func RevenueByBusinessLine(ctx context.Context, q dbtx.Querier, property *uuid.UUID, from, to time.Time, compare string) (FinancialStatement, error) {
	fs := FinancialStatement{Kind: "revenue-by-business-line", From: ymd(from), To: ymd(to), Consolidated: property == nil, Rows: []FinancialStatementRow{},
		Totals: map[string]string{}}
	var prop any
	if property != nil {
		prop = *property
	}
	query := func(f, t time.Time) (map[string]decimal.Decimal, error) {
		rows, err := q.Query(ctx, `SELECT coalesce(nullif(l.business_line, ''), 'unassigned'), sum(l.credit - l.debit)::text
			FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id JOIN accounting.journals j ON j.id = l.journal_id
			WHERE a.account_type = 'revenue' AND a.subtype <> 'other_income' AND ($1::uuid IS NULL OR l.property_id = $1) AND l.journal_date BETWEEN $2 AND $3
			AND j.journal_type <> 'closing' GROUP BY 1`, prop, dateOnly(f), dateOnly(t))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]decimal.Decimal{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = dec(v)
		}
		return out, rows.Err()
	}
	cur, err := query(from, to)
	if err != nil {
		return fs, err
	}
	prev := map[string]decimal.Decimal{}
	cf, ct, hasCmp := compareRange(from, to, compare)
	if hasCmp {
		fs.CompareFrom, fs.CompareTo = ymd(cf), ymd(ct)
		if prev, err = query(cf, ct); err != nil {
			return fs, err
		}
	}
	keys := map[string]bool{}
	for k := range cur {
		keys[k] = true
	}
	for k := range prev {
		keys[k] = true
	}
	order := []string{"golf", "sportclub", "stay", "pos", "banquet", "membership", "package", "voucher", "other", "unassigned"}
	for k := range keys {
		found := false
		for _, o := range order {
			found = found || o == k
		}
		if !found {
			order = append(order, k)
		}
	}
	total, ptotal := decimal.Zero, decimal.Zero
	for _, k := range order {
		if !keys[k] {
			continue
		}
		v, pv := cur[k], prev[k]
		total, ptotal = total.Add(v), ptotal.Add(pv)
		r := FinancialStatementRow{Section: "revenue", Code: k, Name: k, Amount: v.String()}
		if hasCmp {
			p := pv.String()
			r.Compare = &p
		}
		fs.Rows = append(fs.Rows, r)
		fs.Totals[k] = v.String()
	}
	fs.Rows = append(fs.Rows, totalRow("revenue", "Total Revenue", total, ptotal, hasCmp))
	fs.Totals["total"] = total.String()
	return fs, nil
}
