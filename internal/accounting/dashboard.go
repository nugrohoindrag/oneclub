package accounting

// Finance Dashboard of the Back Office (Accountant, Finance Manager, GM): one
// read of the period's financial summary, cash & bank with the cash flow
// trend and the bank reconciliation status, receivables and payables by
// due date, expenses by account and budget vs actual. Every figure comes from
// the general ledger, the AR / AP open items and the bank statements; the
// budget comes from the approved KPI target plan through Module.Targets
// (wired by internal/app, so Accounting does not import Reporting).

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PermDashboardView opens the Finance Dashboard.
const PermDashboardView = "accounting.dashboard.view"

// TargetSource returns the approved monthly targets of a property by KPI key
// (gl_revenue, net_income, cash, accounts_receivable, accounts_payable).
type TargetSource func(ctx context.Context, q dbtx.Querier, property uuid.UUID, year, month int) (map[string]decimal.Decimal, error)

// FinanceSummary is the headline of the period; the changes compare with the
// previous period of the same length (ratios, nil without a base).
type FinanceSummary struct {
	Revenue         string  `json:"revenue" doc:"Revenue and other income of the period"`
	Expenses        string  `json:"expenses" doc:"Cost of sales, operating and other expenses of the period"`
	NetProfit       string  `json:"netProfit" doc:"Revenue − expenses (= net income of the P&L)"`
	CashBank        string  `json:"cashBank" doc:"Cash and bank balances at the end of the period"`
	Receivables     string  `json:"receivables" doc:"Open billing invoices at the end of the period"`
	Payables        string  `json:"payables" doc:"Open supplier items at the end of the period"`
	RevenueChange   *string `json:"revenueChange,omitempty"`
	ExpensesChange  *string `json:"expensesChange,omitempty"`
	NetProfitChange *string `json:"netProfitChange,omitempty"`
	CompareFrom     string  `json:"compareFrom"`
	CompareTo       string  `json:"compareTo"`
}

// CashBalance is the GL balance of a cash or bank account.
type CashBalance struct {
	AccountID uuid.UUID `json:"accountId" db:"account_id"`
	Code      string    `json:"code" db:"code"`
	Name      string    `json:"name" db:"name"`
	Kind      string    `json:"kind" db:"kind" enum:"cash,bank"`
	Balance   string    `json:"balance" db:"balance"`
}

// CashFlowMonth is the cash in and out of a month (per journal, so transfers
// between cash and bank accounts net out).
type CashFlowMonth struct {
	Month   string `json:"month" db:"month" doc:"YYYY-MM"`
	CashIn  string `json:"cashIn" db:"cash_in"`
	CashOut string `json:"cashOut" db:"cash_out"`
	Net     string `json:"net" db:"net"`
}

// BankReconciliationStatus is the reconciliation state of a bank account.
type BankReconciliationStatus struct {
	BankAccountID     uuid.UUID `json:"bankAccountId"`
	Name              string    `json:"name"`
	BookBalance       string    `json:"bookBalance"`
	LastStatementDate *string   `json:"lastStatementDate" doc:"Statement date of the latest reconciliation"`
	LastStatus        *string   `json:"lastStatus" enum:"in_progress,completed"`
	UnmatchedLines    int       `json:"unmatchedLines" doc:"Imported statement lines not matched yet"`
	Status            string    `json:"status" enum:"reconciled,in_progress,attention,never" doc:"reconciled: latest completed within 35 days and nothing unmatched"`
}

// AgingBucket is an amount of an ageing bucket.
type AgingBucket struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

// OpenItem is an open receivable or payable.
type OpenItem struct {
	ID      uuid.UUID `json:"id"`
	Number  string    `json:"number"`
	Party   string    `json:"party" doc:"Bill-to or supplier"`
	DueDate string    `json:"dueDate"`
	Amount  string    `json:"amount" doc:"Open amount"`
	Status  string    `json:"status" enum:"overdue,due_today,due_soon,current"`
}

// ExpenseShare is an expense account (or "Other") with its share of the
// period's expenses.
type ExpenseShare struct {
	AccountID *uuid.UUID `json:"accountId,omitempty"`
	Name      string     `json:"name"`
	Amount    string     `json:"amount"`
	Share     string     `json:"share" doc:"Ratio of the total expenses"`
}

// BudgetLine compares an actual with its approved target of the month.
type BudgetLine struct {
	Key       string  `json:"key"`
	Label     string  `json:"label"`
	Budget    string  `json:"budget"`
	Actual    string  `json:"actual"`
	Variance  *string `json:"variance,omitempty" doc:"(actual − budget) ÷ budget"`
	Prorated  bool    `json:"prorated" doc:"Flow line of a month in progress: the budget is the month's target × days elapsed ÷ days of the month"`
	Favorable bool    `json:"favorable" doc:"The variance goes the right way for this line"`
}

// FinanceDashboard is the Finance Dashboard of a period.
type FinanceDashboard struct {
	From             string                     `json:"from"`
	To               string                     `json:"to"`
	BookOpen         bool                       `json:"bookOpen" doc:"The property has an accounting book (Accounting → Setup)"`
	Summary          FinanceSummary             `json:"summary"`
	Cash             []CashBalance              `json:"cash"`
	CashFlow         []CashFlowMonth            `json:"cashFlow" doc:"Last six months up to the period"`
	Banks            []BankReconciliationStatus `json:"banks"`
	ReceivableAging  []AgingBucket              `json:"receivableAging" doc:"By days past due"`
	TopReceivables   []OpenItem                 `json:"topReceivables"`
	PayableDue       []AgingBucket              `json:"payableDue" doc:"Overdue, due today, this week, this month, later"`
	UpcomingPayables []OpenItem                 `json:"upcomingPayables"`
	Expenses         []ExpenseShare             `json:"expenses"`
	Budget           []BudgetLine               `json:"budget" doc:"Empty without an approved KPI target plan for the year"`
}

// RegisterDashboard adds the Finance Dashboard route.
func (m *Module) RegisterDashboard(reg *route.Registry) {
	m.propertyRoute(reg, "Finance Dashboard", route.Route{Method: http.MethodGet, Path: base + "/dashboard",
		Summary:    "Finance Dashboard of a month: summary, cash & bank, receivables, payables, expenses, budget vs actual",
		Permission: PermDashboardView, Response: FinanceDashboard{}, Query: []route.Param{{Name: "month", Description: "YYYY-MM (default: this month, to date)"}},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FinanceDashboard, error) {
			p := handle.Property(ctx)
			today := localToday(ctx, tx, p)
			from := monthStart(today)
			if v := strings.TrimSpace(r.URL.Query().Get("month")); v != "" {
				t, err := time.Parse("2006-01", v)
				if err != nil {
					return FinanceDashboard{}, errs.Validation("invalid_month", "month must be YYYY-MM", errs.Field("month", "invalid_month", "YYYY-MM"))
				}
				from = t
			}
			to := monthEnd(from)
			if to.After(today) {
				to = today
			}
			return m.Dashboard(ctx, tx, p, from, to, today)
		})})
}

// Dashboard builds the Finance Dashboard of property between from and to
// (today drives the due-date buckets).
func (m *Module) Dashboard(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to, today time.Time) (FinanceDashboard, error) {
	d := FinanceDashboard{From: ymd(from), To: ymd(to), Cash: []CashBalance{}, CashFlow: []CashFlowMonth{}, Banks: []BankReconciliationStatus{},
		TopReceivables: []OpenItem{}, UpcomingPayables: []OpenItem{}, Expenses: []ExpenseShare{}, Budget: []BudgetLine{}}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounting.books WHERE property_id = $1)`, property).Scan(&d.BookOpen); err != nil {
		return d, err
	}
	pl, err := ProfitLoss(ctx, q, &property, from, to, "previous_period")
	if err != nil {
		return d, err
	}
	rev, exp, prev, pexp := plSplit(pl)
	d.Summary = FinanceSummary{Revenue: rev.String(), Expenses: exp.String(), NetProfit: rev.Sub(exp).String(), CompareFrom: pl.CompareFrom, CompareTo: pl.CompareTo,
		RevenueChange: change(rev, prev), ExpensesChange: change(exp, pexp), NetProfitChange: change(rev.Sub(exp), prev.Sub(pexp))}
	d.Expenses = expenseShares(pl, exp)

	cash, total, err := cashBalances(ctx, q, property, to)
	if err != nil {
		return d, err
	}
	d.Cash, d.Summary.CashBank = cash, total.String()
	if d.CashFlow, err = cashFlowTrend(ctx, q, property, to); err != nil {
		return d, err
	}
	if d.Banks, err = bankStatuses(ctx, q, property, to, today); err != nil {
		return d, err
	}
	var ar decimal.Decimal
	if d.ReceivableAging, d.TopReceivables, ar, err = receivables(ctx, q, property, to, today); err != nil {
		return d, err
	}
	d.Summary.Receivables = ar.String()
	var ap decimal.Decimal
	if d.PayableDue, d.UpcomingPayables, ap, err = payables(ctx, q, property, to, today); err != nil {
		return d, err
	}
	d.Summary.Payables = ap.String()
	if m.Targets != nil {
		t, err := m.Targets(ctx, q, property, from.Year(), int(from.Month()))
		if err != nil {
			return d, err
		}
		elapsed := decimal.NewFromInt(int64(to.Day())).Div(decimal.NewFromInt(int64(monthEnd(from).Day())))
		d.Budget = budgetLines(t, elapsed, map[string]decimal.Decimal{"gl_revenue": rev, "net_income": rev.Sub(exp), "cash": total,
			"accounts_receivable": ar, "accounts_payable": ap})
	}
	return d, nil
}

// plSplit returns revenue (with other income) and expenses (cost of sales,
// operating and other expenses) of the period and of the comparison period.
func plSplit(pl FinancialStatement) (rev, exp, prev, pexp decimal.Decimal) {
	for _, r := range pl.Rows {
		if r.Total {
			continue
		}
		v, pv := dec(r.Amount), decimal.Zero
		if r.Compare != nil {
			pv = dec(*r.Compare)
		}
		switch r.Section {
		case "revenue":
			rev, prev = rev.Add(v), prev.Add(pv)
		case "cogs", "expense":
			exp, pexp = exp.Add(v), pexp.Add(pv)
		case "other": // credit positive: other income > 0, other expense < 0
			if v.IsPositive() {
				rev = rev.Add(v)
			} else {
				exp = exp.Sub(v)
			}
			if pv.IsPositive() {
				prev = prev.Add(pv)
			} else {
				pexp = pexp.Sub(pv)
			}
		}
	}
	return
}

// change is (cur − prev) ÷ |prev|, nil without a base.
func change(cur, prev decimal.Decimal) *string {
	if prev.IsZero() {
		return nil
	}
	s := cur.Sub(prev).Div(prev.Abs()).Round(4).String()
	return &s
}

// expenseShares are the five largest expense accounts of the period and the
// rest as "Other".
func expenseShares(pl FinancialStatement, total decimal.Decimal) []ExpenseShare {
	var rows []FinancialStatementRow
	for _, r := range pl.Rows {
		if !r.Total && (r.Section == "cogs" || r.Section == "expense" || (r.Section == "other" && dec(r.Amount).IsNegative())) {
			if r.Section == "other" {
				r.Amount = dec(r.Amount).Neg().String()
			}
			if dec(r.Amount).IsPositive() {
				rows = append(rows, r)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return dec(rows[i].Amount).GreaterThan(dec(rows[j].Amount)) })
	share := func(v decimal.Decimal) string {
		if total.IsZero() {
			return "0"
		}
		return v.Div(total).Round(4).String()
	}
	out := []ExpenseShare{}
	var other decimal.Decimal
	for i, r := range rows {
		if i < 5 {
			out = append(out, ExpenseShare{AccountID: r.AccountID, Name: r.Name, Amount: r.Amount, Share: share(dec(r.Amount))})
			continue
		}
		other = other.Add(dec(r.Amount))
	}
	if other.IsPositive() {
		out = append(out, ExpenseShare{Name: "Other", Amount: other.String(), Share: share(other)})
	}
	return out
}

// cashBalances are the GL balances of the cash and bank accounts at a date.
func cashBalances(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time) ([]CashBalance, decimal.Decimal, error) {
	rows, err := handle.List[CashBalance](q.Query(ctx, `SELECT a.id AS account_id, a.code, coalesce(b.name, a.name) AS name, a.subtype AS kind,
		trim_scale(coalesce(sum(l.debit - l.credit), 0))::text AS balance
		FROM accounting.accounts a
		LEFT JOIN accounting.journal_lines l ON l.account_id = a.id AND l.property_id = $1 AND l.journal_date <= $2
		LEFT JOIN LATERAL (SELECT name FROM accounting.bank_accounts WHERE gl_account_id = a.id AND property_id = $1 ORDER BY created_at LIMIT 1) b ON true
		WHERE a.is_posting AND a.subtype IN ('cash', 'bank') GROUP BY a.id, b.name ORDER BY a.code`, property, dateOnly(asOf)))
	if err != nil {
		return nil, decimal.Zero, err
	}
	var total decimal.Decimal
	for _, r := range rows {
		total = total.Add(dec(r.Balance))
	}
	return rows, total, nil
}

// cashFlowTrend is the cash in / out of the six months ending with asOf's
// month; per journal the cash and bank lines are netted first, and the
// opening balances and year-end closing are not cash flows.
func cashFlowTrend(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf time.Time) ([]CashFlowMonth, error) {
	first := monthStart(asOf).AddDate(0, -5, 0)
	rows, err := handle.List[CashFlowMonth](q.Query(ctx, `WITH m AS (SELECT generate_series($2::date, $3::date, interval '1 month')::date AS m),
		j AS (SELECT date_trunc('month', l.journal_date)::date AS m, l.journal_id, sum(l.debit - l.credit) AS net
		  FROM accounting.journal_lines l JOIN accounting.accounts a ON a.id = l.account_id JOIN accounting.journals jr ON jr.id = l.journal_id
		  WHERE l.property_id = $1 AND a.subtype IN ('cash', 'bank') AND l.journal_date BETWEEN $2 AND $4
		    AND jr.journal_type NOT IN ('opening', 'closing') GROUP BY 1, 2)
		SELECT to_char(m.m, 'YYYY-MM') AS month,
		  trim_scale(coalesce(sum(j.net) FILTER (WHERE j.net > 0), 0))::text AS cash_in,
		  trim_scale(coalesce(-sum(j.net) FILTER (WHERE j.net < 0), 0))::text AS cash_out,
		  trim_scale(coalesce(sum(j.net), 0))::text AS net
		FROM m LEFT JOIN j ON j.m = m.m GROUP BY m.m ORDER BY m.m`, property, first, monthStart(asOf), dateOnly(asOf)))
	if rows == nil {
		rows = []CashFlowMonth{}
	}
	return rows, err
}

// bankStatuses is the reconciliation status of each active bank account.
func bankStatuses(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf, today time.Time) ([]BankReconciliationStatus, error) {
	type row struct {
		ID        uuid.UUID `db:"id"`
		Name      string    `db:"name"`
		Book      string    `db:"book"`
		Statement *string   `db:"statement_date"`
		Status    *string   `db:"status"`
		Unmatched int       `db:"unmatched"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT b.id, b.name,
		  trim_scale(coalesce((SELECT sum(l.debit - l.credit) FROM accounting.journal_lines l WHERE l.account_id = b.gl_account_id
		    AND l.property_id = $1 AND l.journal_date <= $2), 0))::text AS book,
		  r.statement_date::text AS statement_date, r.status,
		  (SELECT count(*) FROM accounting.bank_transactions t WHERE t.bank_account_id = b.id AND t.status NOT IN ('matched', 'ignored'))::int AS unmatched
		FROM accounting.bank_accounts b
		LEFT JOIN LATERAL (SELECT statement_date, status FROM accounting.bank_reconciliations WHERE bank_account_id = b.id
		  ORDER BY statement_date DESC, created_at DESC LIMIT 1) r ON true
		WHERE b.property_id = $1 AND b.kind = 'bank' AND b.status = 'active' AND b.archived_at IS NULL ORDER BY b.code`, property, dateOnly(asOf)))
	if err != nil {
		return nil, err
	}
	out := make([]BankReconciliationStatus, 0, len(rows))
	for _, r := range rows {
		s := BankReconciliationStatus{BankAccountID: r.ID, Name: r.Name, BookBalance: dec(r.Book).String(), LastStatementDate: r.Statement,
			LastStatus: r.Status, UnmatchedLines: r.Unmatched}
		switch {
		case r.Statement == nil:
			s.Status = "never"
		case *r.Status == "in_progress":
			s.Status = "in_progress"
		default:
			d, _ := time.Parse("2006-01-02", *r.Statement)
			s.Status = "reconciled"
			if r.Unmatched > 0 || today.Sub(d) > 35*24*time.Hour {
				s.Status = "attention"
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// dueStatus classifies an open item by its due date.
func dueStatus(due, today time.Time) string {
	switch {
	case due.Before(today):
		return "overdue"
	case due.Equal(today):
		return "due_today"
	case !due.After(today.AddDate(0, 0, 7)):
		return "due_soon"
	}
	return "current"
}

// receivables ages the open billing invoices by days past due and lists the
// largest ones.
func receivables(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf, today time.Time) ([]AgingBucket, []OpenItem, decimal.Decimal, error) {
	rows, err := handle.List[partyItem](q.Query(ctx, arOpenItems+` ORDER BY open_amount DESC, due_date`, property, ymd(asOf)))
	if err != nil {
		return nil, nil, decimal.Zero, err
	}
	keys := []AgingBucket{{Key: "current", Label: "Current"}, {Key: "d1_30", Label: "1–30 days"}, {Key: "d31_60", Label: "31–60 days"},
		{Key: "d61_90", Label: "61–90 days"}, {Key: "d90", Label: "> 90 days"}}
	sums := make([]decimal.Decimal, len(keys))
	var total decimal.Decimal
	top := []OpenItem{}
	for _, r := range rows {
		v := dec(r.Amount)
		total = total.Add(v)
		late := int(asOf.Sub(r.Due).Hours() / 24)
		i := 0
		switch {
		case late > 90:
			i = 4
		case late > 60:
			i = 3
		case late > 30:
			i = 2
		case late > 0:
			i = 1
		}
		sums[i] = sums[i].Add(v)
		if len(top) < 8 {
			top = append(top, OpenItem{ID: r.ID, Number: r.Number, Party: r.PartyName, DueDate: ymd(r.Due), Amount: r.Amount, Status: dueStatus(r.Due, today)})
		}
	}
	for i := range keys {
		keys[i].Amount = sums[i].String()
	}
	return keys, top, total, nil
}

// payables groups the open supplier items by due date and lists the next
// ones to pay.
func payables(ctx context.Context, q dbtx.Querier, property uuid.UUID, asOf, today time.Time) ([]AgingBucket, []OpenItem, decimal.Decimal, error) {
	rows, err := handle.List[partyItem](q.Query(ctx, apOpenItems+` ORDER BY due_date, open_amount DESC`, property, ymd(asOf)))
	if err != nil {
		return nil, nil, decimal.Zero, err
	}
	keys := []AgingBucket{{Key: "overdue", Label: "Overdue"}, {Key: "today", Label: "Due today"}, {Key: "week", Label: "Due this week"},
		{Key: "month", Label: "Due this month"}, {Key: "later", Label: "Later"}}
	sums := make([]decimal.Decimal, len(keys))
	var total decimal.Decimal
	next := []OpenItem{}
	week, month := today.AddDate(0, 0, 7), monthEnd(today)
	for _, r := range rows {
		v := dec(r.Amount)
		total = total.Add(v)
		i := 4
		switch {
		case r.Due.Before(today):
			i = 0
		case r.Due.Equal(today):
			i = 1
		case !r.Due.After(week):
			i = 2
		case !r.Due.After(month):
			i = 3
		}
		sums[i] = sums[i].Add(v)
		if !r.Due.Before(today) && len(next) < 8 {
			next = append(next, OpenItem{ID: r.ID, Number: r.Number, Party: r.PartyName, DueDate: ymd(r.Due), Amount: r.Amount, Status: dueStatus(r.Due, today)})
		}
	}
	for i := range keys {
		keys[i].Amount = sums[i].String()
	}
	return keys, next, total, nil
}

// budgetLines compares the actuals with the targets of the month (only the
// KPIs with a target); flow targets are prorated by the elapsed share of the
// month, balances are compared as they are.
func budgetLines(targets map[string]decimal.Decimal, elapsed decimal.Decimal, actual map[string]decimal.Decimal) []BudgetLine {
	defs := []struct {
		key, label string
		up, flow   bool // higher is better; flow of the period (else a balance)
	}{{"gl_revenue", "Revenue", true, true}, {"net_income", "Net Profit", true, true}, {"cash", "Cash & Bank", true, false},
		{"accounts_receivable", "Accounts Receivable", false, false}, {"accounts_payable", "Accounts Payable", false, false}}
	out := []BudgetLine{}
	for _, d := range defs {
		t, ok := targets[d.key]
		if !ok {
			continue
		}
		a := actual[d.key]
		prorate := d.flow && elapsed.LessThan(decimal.NewFromInt(1))
		if prorate {
			t = t.Mul(elapsed).Round(0)
		}
		l := BudgetLine{Key: d.key, Label: d.label, Budget: t.String(), Actual: a.String(), Variance: change(a, t), Prorated: prorate}
		l.Favorable = a.GreaterThanOrEqual(t) == d.up || a.Equal(t)
		out = append(out, l)
	}
	return out
}
