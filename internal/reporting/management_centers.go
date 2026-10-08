package reporting

// Management Dashboard › Profit Centers and Incidents.
//
// Profit Centers: revenue, cost of sales, direct expenses and contribution
// per profit center (golf, membership, F&B, pro shop, sport club, stay,
// banquet, packages) of a month or year to date from the general ledger
// (reporting.acc_profit_center_lines), against the same days of the
// previous period and over six months. What belongs to no center (salaries,
// utilities, depreciation, other income and expenses) is the shared
// overhead, so contribution − overhead = net income of the P&L.
//
// Incidents: the operational incidents of the modules in one place
// (reporting.incidents: caddy, golf cart, banquet event) — totals, open and
// high-severity counts, damage, split by source / severity / category,
// six-month trend, what is still open and the incidents of the period.

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

func init() {
	p3p4Routes = append(p3p4Routes, registerManagementCenters)
	RegisterP5Report(sqlReport("accounting.profit_center", "Profit Center Report", "accounting",
		"Revenue, cost of sales, direct expenses and contribution per profit center and account for the period; shared overhead below the centers.",
		cols("profitCenter|Profit Center", "section|Section", "account|Account", "accountName|Account Name", "amount|Amount|number"), nil, 31,
		`SELECT profit_center AS "profitCenter", section, account_code AS "account", min(account_name) AS "accountName",
		trim_scale(CASE WHEN section IN ('revenue', 'other') THEN sum(profit) ELSE -sum(profit) END)::text AS "amount"
		FROM reporting.acc_profit_center_lines WHERE journal_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2, 3 HAVING sum(profit) <> 0 ORDER BY 1, 2, 3`),
		"property_admin", "general_manager", "club_manager", "resort_manager", "finance_manager", "accountant")
	RegisterP5Report(sqlReport("reporting.incidents", "Incident Report", "reporting",
		"Caddy, golf cart and banquet event incidents of the period with severity, status, action taken and damage.",
		cols("occurredAt|Occurred|datetime", "source|Source", "number|Number", "subject|Subject", "category|Category", "severity|Severity", "status|Status",
			"description|Description", "actionTaken|Action Taken", "damage|Damage|number", "damageStatus|Damage Status"),
		[]Param{{Key: "source", Label: "Source", Type: "enum", Enum: incidentSources}}, 31,
		`SELECT occurred_at AS "occurredAt", source, number, subject, category, severity, status, description, action_taken AS "actionTaken",
		trim_scale(damage_amount)::text AS "damage", damage_status AS "damageStatus" FROM reporting.incidents
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR source = $4) ORDER BY occurred_at DESC`),
		"property_admin", "general_manager", "club_manager", "resort_manager", "golf_manager", "banquet_manager")
}

var incidentSources = []string{"caddy", "golf_cart", "banquet"}

// profitCenterOrder lists the profit centers in display order (shared last).
var profitCenterOrder = []struct{ code, label string }{
	{"golf", "Golf"}, {"membership", "Membership"}, {"fnb", "Food & Beverage"}, {"pro_shop", "Pro Shop"}, {"sportclub", "Sport Club"},
	{"stay", "Stay"}, {"banquet", "Banquet & Events"}, {"package", "Packages"}, {"shared", "Shared & Unallocated"},
}

func registerManagementCenters(s *Service, reg *route.Registry) {
	add := func(rt route.Route) {
		rt.Module = "reporting"
		rt.Tag = "Management & BI"
		rt.Scope = route.ScopeProperty
		reg.Add(rt)
	}
	q := []route.Param{{Name: "period", Enum: []string{"month", "year"}}, {Name: "month", Description: "YYYY-MM (default: this month)"}}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/profit-centers", Permission: catalog.ManagementView, Query: q,
		Summary:  "Profit centers: revenue, cost of sales, direct expenses and contribution per center with the previous period and six months",
		Response: ProfitCenters{}, Handler: managementRead(s, profitCenters)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/incidents", Permission: catalog.ManagementView, Query: q,
		Summary:  "Incident dashboard: caddy, golf cart and banquet incidents by source, severity, category and month; open incidents",
		Response: IncidentDashboard{}, Handler: managementRead(s, incidentDashboard)})
}

// managementRead runs a read use case of the active property on the replica
// within the query budget of the BI Policies.
func managementRead[T any](s *Service, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, rg managementRange) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := reqctx.Property(ctx); !ok {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "select a property"))
			return
		}
		var res T
		err := s.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
			if err := queryBudget(ctx, tx, LoadBIPolicy(ctx, tx, handle.Property(ctx)).QueryTimeoutSeconds); err != nil {
				return err
			}
			loc := calendar.Location(ctx, tx)
			today := day(time.Now().In(loc))
			period, m, from, to, err := periodRange(r, today)
			if err != nil {
				return err
			}
			pf, pt := previousRange(period, from, to)
			res, err = fn(ctx, tx, r, managementRange{Period: period, Month: m, From: from, To: to, PrevFrom: pf, PrevTo: pt, TZ: loc.String()})
			return budgetError(err)
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

type managementRange struct {
	Period                            string
	Month, From, To, PrevFrom, PrevTo time.Time
	TZ                                string
}

// previousRange is the comparison period: the same days of the previous
// month (month to date against month to date) or of the previous year.
func previousRange(period string, from, to time.Time) (time.Time, time.Time) {
	if period == "year" {
		return from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0)
	}
	pf := from.AddDate(0, -1, 0)
	return pf, minDate(time.Date(pf.Year(), pf.Month(), to.Day(), 0, 0, 0, 0, time.UTC), monthEnd(pf))
}

// ── Profit Centers ────────────────────────────────────────────────────────

// ProfitCenter is the result of one profit center over the period.
type ProfitCenter struct {
	Code                 string              `json:"code"`
	Label                string              `json:"label"`
	Shared               bool                `json:"shared" doc:"Overhead and other income / expenses that belong to no center"`
	Revenue              string              `json:"revenue"`
	CostOfSales          string              `json:"costOfSales"`
	DirectExpense        string              `json:"directExpense"`
	Contribution         string              `json:"contribution" doc:"Revenue − cost of sales − direct expenses (for shared: its net)"`
	Margin               *string             `json:"margin" doc:"Contribution ÷ revenue"`
	RevenueShare         *string             `json:"revenueShare" doc:"Share of the revenue of all centers"`
	PreviousRevenue      string              `json:"previousRevenue"`
	PreviousContribution string              `json:"previousContribution"`
	RevenueChange        *string             `json:"revenueChange" doc:"Change ratio against the previous period"`
	ContributionChange   *string             `json:"contributionChange"`
	Trend                []ProfitCenterMonth `json:"trend" doc:"Six months up to the period's month"`
}

// ProfitCenterMonth is one month of a profit center.
type ProfitCenterMonth struct {
	Month        string `json:"month" doc:"YYYY-MM"`
	Revenue      string `json:"revenue"`
	Contribution string `json:"contribution"`
}

// ProfitCenterAccount is the amount of one account in a profit center (revenue and other income positive, costs positive).
type ProfitCenterAccount struct {
	ProfitCenter string `json:"profitCenter"`
	Section      string `json:"section" enum:"revenue,cogs,expense,other"`
	Account      string `json:"account"`
	AccountName  string `json:"accountName"`
	Amount       string `json:"amount"`
}

// ProfitCenters is the Profit Centers dashboard.
type ProfitCenters struct {
	Period               string                `json:"period" enum:"month,year"`
	Month                string                `json:"month"`
	From                 string                `json:"from"`
	To                   string                `json:"to"`
	CompareFrom          string                `json:"compareFrom"`
	CompareTo            string                `json:"compareTo"`
	Centers              []ProfitCenter        `json:"centers" doc:"Profit centers with activity, the shared overhead last"`
	Revenue              string                `json:"revenue" doc:"Revenue of the centers"`
	Contribution         string                `json:"contribution" doc:"Contribution of the centers"`
	Margin               *string               `json:"margin"`
	SharedNet            string                `json:"sharedNet" doc:"Net of the shared lines (negative = overhead)"`
	NetIncome            string                `json:"netIncome" doc:"Contribution + shared net = net income of the P&L"`
	PreviousContribution string                `json:"previousContribution"`
	PreviousNetIncome    string                `json:"previousNetIncome"`
	Accounts             []ProfitCenterAccount `json:"accounts"`
	GeneratedAt          time.Time             `json:"generatedAt"`
}

type pcSums struct{ revenue, cogs, expense, other decimal.Decimal }

func (p pcSums) contribution() decimal.Decimal {
	return p.revenue.Sub(p.cogs).Sub(p.expense).Add(p.other)
}

// pcTotals sums profit per center and section between two dates (costs positive).
func pcTotals(ctx context.Context, tx pgx.Tx, from, to time.Time) (map[string]pcSums, error) {
	rows, err := tx.Query(ctx, `SELECT profit_center, section, sum(profit)::text FROM reporting.acc_profit_center_lines
		WHERE journal_date BETWEEN $1 AND $2 GROUP BY 1, 2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]pcSums{}
	for rows.Next() {
		var c, sec, v string
		if err := rows.Scan(&c, &sec, &v); err != nil {
			return nil, err
		}
		d, _ := decimal.NewFromString(v)
		s := out[c]
		switch sec {
		case "revenue":
			s.revenue = s.revenue.Add(d)
		case "cogs":
			s.cogs = s.cogs.Sub(d)
		case "expense":
			s.expense = s.expense.Sub(d)
		default:
			s.other = s.other.Add(d)
		}
		out[c] = s
	}
	return out, rows.Err()
}

func share(a, b decimal.Decimal) *string {
	if b.IsZero() {
		return nil
	}
	return ds(a.Div(b).Round(4))
}

func profitCenters(ctx context.Context, tx pgx.Tx, _ *http.Request, rg managementRange) (ProfitCenters, error) {
	out := ProfitCenters{Period: rg.Period, Month: rg.Month.Format("2006-01"), From: rg.From.Format(dateFmt), To: rg.To.Format(dateFmt),
		CompareFrom: rg.PrevFrom.Format(dateFmt), CompareTo: rg.PrevTo.Format(dateFmt), Centers: []ProfitCenter{}, Accounts: []ProfitCenterAccount{},
		GeneratedAt: time.Now().UTC()}
	cur, err := pcTotals(ctx, tx, rg.From, rg.To)
	if err != nil {
		return out, err
	}
	prev, err := pcTotals(ctx, tx, rg.PrevFrom, rg.PrevTo)
	if err != nil {
		return out, err
	}
	// Six months up to the month of the period (the running month to date).
	trendFrom := monthStart(rg.Month).AddDate(0, -5, 0)
	rows, err := tx.Query(ctx, `SELECT to_char(journal_date, 'YYYY-MM'), profit_center, sum(profit) FILTER (WHERE section = 'revenue')::text,
		sum(profit)::text FROM reporting.acc_profit_center_lines WHERE journal_date BETWEEN $1 AND $2 GROUP BY 1, 2`, trendFrom, minDate(monthEnd(rg.Month), rg.To))
	if err != nil {
		return out, err
	}
	type tv struct{ rev, net decimal.Decimal }
	trend := map[string]map[string]tv{}
	for rows.Next() {
		var m, c string
		var rev, net *string
		if err := rows.Scan(&m, &c, &rev, &net); err != nil {
			rows.Close()
			return out, err
		}
		v := tv{}
		if rev != nil {
			v.rev, _ = decimal.NewFromString(*rev)
		}
		if net != nil {
			v.net, _ = decimal.NewFromString(*net)
		}
		if trend[c] == nil {
			trend[c] = map[string]tv{}
		}
		trend[c][m] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	var revenue, contribution, prevContribution, sharedNet, prevSharedNet decimal.Decimal
	for _, c := range profitCenterOrder {
		if c.code == "shared" {
			continue
		}
		revenue = revenue.Add(cur[c.code].revenue)
	}
	for _, c := range profitCenterOrder {
		s, p := cur[c.code], prev[c.code]
		if s == (pcSums{}) && p == (pcSums{}) && trend[c.code] == nil {
			continue
		}
		pc := ProfitCenter{Code: c.code, Label: c.label, Shared: c.code == "shared", Revenue: s.revenue.String(), CostOfSales: s.cogs.String(),
			DirectExpense: s.expense.String(), Contribution: s.contribution().String(), Margin: share(s.contribution(), s.revenue),
			PreviousRevenue: p.revenue.String(), PreviousContribution: p.contribution().String(), RevenueChange: ratio(s.revenue, p.revenue),
			ContributionChange: ratio(s.contribution(), p.contribution()), Trend: []ProfitCenterMonth{}}
		if pc.Shared {
			pc.Margin, pc.RevenueChange = nil, nil
			sharedNet, prevSharedNet = s.contribution(), p.contribution()
		} else {
			pc.RevenueShare = share(s.revenue, revenue)
			contribution, prevContribution = contribution.Add(s.contribution()), prevContribution.Add(p.contribution())
		}
		for m := trendFrom; !m.After(rg.Month); m = m.AddDate(0, 1, 0) {
			v := trend[c.code][m.Format("2006-01")]
			pc.Trend = append(pc.Trend, ProfitCenterMonth{Month: m.Format("2006-01"), Revenue: v.rev.String(), Contribution: v.net.String()})
		}
		out.Centers = append(out.Centers, pc)
	}
	out.Revenue, out.Contribution, out.Margin = revenue.String(), contribution.String(), share(contribution, revenue)
	out.SharedNet, out.NetIncome = sharedNet.String(), contribution.Add(sharedNet).String()
	out.PreviousContribution, out.PreviousNetIncome = prevContribution.String(), prevContribution.Add(prevSharedNet).String()

	accts, err := tx.Query(ctx, `SELECT profit_center, section, account_code, min(account_name),
		trim_scale(CASE WHEN section IN ('revenue', 'other') THEN sum(profit) ELSE -sum(profit) END)::text
		FROM reporting.acc_profit_center_lines WHERE journal_date BETWEEN $1 AND $2 GROUP BY 1, 2, 3 HAVING sum(profit) <> 0 ORDER BY 1, 2, 3`, rg.From, rg.To)
	if err != nil {
		return out, err
	}
	defer accts.Close()
	for accts.Next() {
		var a ProfitCenterAccount
		if err := accts.Scan(&a.ProfitCenter, &a.Section, &a.Account, &a.AccountName, &a.Amount); err != nil {
			return out, err
		}
		out.Accounts = append(out.Accounts, a)
	}
	return out, accts.Err()
}

// ── Incidents ─────────────────────────────────────────────────────────────

// IncidentItem is one incident of the dashboard.
type IncidentItem struct {
	ID           string    `json:"id" db:"id"`
	Source       string    `json:"source" db:"source" enum:"caddy,golf_cart,banquet"`
	Number       string    `json:"number" db:"number"`
	Subject      *string   `json:"subject" db:"subject"`
	Category     string    `json:"category" db:"category"`
	Severity     string    `json:"severity" db:"severity" enum:"low,medium,high,critical"`
	Status       string    `json:"status" db:"status" enum:"open,closed,logged"`
	Description  string    `json:"description" db:"description"`
	ActionTaken  *string   `json:"actionTaken" db:"action_taken"`
	DamageAmount *string   `json:"damageAmount" db:"damage_amount"`
	DamageStatus *string   `json:"damageStatus" db:"damage_status"`
	OccurredAt   time.Time `json:"occurredAt" db:"occurred_at"`
}

// IncidentMonth is the count of one month per source.
type IncidentMonth struct {
	Month    string `json:"month" doc:"YYYY-MM"`
	Caddy    int    `json:"caddy"`
	GolfCart int    `json:"golfCart"`
	Banquet  int    `json:"banquet"`
}

// IncidentDashboard is the Incidents dashboard.
type IncidentDashboard struct {
	Period        string          `json:"period" enum:"month,year"`
	Month         string          `json:"month"`
	From          string          `json:"from"`
	To            string          `json:"to"`
	CompareFrom   string          `json:"compareFrom"`
	CompareTo     string          `json:"compareTo"`
	Total         int             `json:"total" doc:"Incidents of the period"`
	PreviousTotal int             `json:"previousTotal"`
	TotalChange   *string         `json:"totalChange"`
	HighSeverity  int             `json:"highSeverity" doc:"High or critical incidents of the period"`
	Open          int             `json:"open" doc:"Open incidents now (any date)"`
	OpenOver7Days int             `json:"openOver7Days" doc:"Open for more than 7 days"`
	Damage        string          `json:"damage" doc:"Damage recorded in the period (golf cart)"`
	DamageCharged string          `json:"damageCharged" doc:"Damage charged to a folio"`
	BySource      []Breakdown     `json:"bySource"`
	BySeverity    []Breakdown     `json:"bySeverity"`
	ByCategory    []Breakdown     `json:"byCategory"`
	ByStatus      []Breakdown     `json:"byStatus"`
	Trend         []IncidentMonth `json:"trend" doc:"Six months up to the period's month"`
	OpenItems     []IncidentItem  `json:"openItems" doc:"Open incidents, oldest first (up to 20)"`
	Items         []IncidentItem  `json:"items" doc:"Incidents of the period, latest first (up to 100)"`
	GeneratedAt   time.Time       `json:"generatedAt"`
}

const incidentItemSelect = `SELECT id::text, source, number, subject, category, severity, status, description, action_taken,
	trim_scale(damage_amount)::text AS damage_amount, damage_status, occurred_at FROM reporting.incidents`

// inPeriod filters reporting.incidents on the club date of occurred_at: $1 from, $2 to, $3 time zone.
const inPeriod = `(occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`

func incidentDashboard(ctx context.Context, tx pgx.Tx, _ *http.Request, rg managementRange) (IncidentDashboard, error) {
	out := IncidentDashboard{Period: rg.Period, Month: rg.Month.Format("2006-01"), From: rg.From.Format(dateFmt), To: rg.To.Format(dateFmt),
		CompareFrom: rg.PrevFrom.Format(dateFmt), CompareTo: rg.PrevTo.Format(dateFmt), GeneratedAt: time.Now().UTC()}
	from, to, tz := rg.From.Format(dateFmt), rg.To.Format(dateFmt), rg.TZ
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE `+inPeriod+`)::int,
		count(*) FILTER (WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $4::date AND $5::date)::int,
		count(*) FILTER (WHERE `+inPeriod+` AND severity IN ('high', 'critical'))::int,
		count(*) FILTER (WHERE status = 'open')::int,
		count(*) FILTER (WHERE status = 'open' AND occurred_at < now() - interval '7 days')::int,
		trim_scale(coalesce(sum(damage_amount) FILTER (WHERE `+inPeriod+`), 0))::text,
		trim_scale(coalesce(sum(damage_amount) FILTER (WHERE `+inPeriod+` AND damage_status = 'charged'), 0))::text
		FROM reporting.incidents`, from, to, tz, rg.PrevFrom.Format(dateFmt), rg.PrevTo.Format(dateFmt)).
		Scan(&out.Total, &out.PreviousTotal, &out.HighSeverity, &out.Open, &out.OpenOver7Days, &out.Damage, &out.DamageCharged)
	if err != nil {
		return out, err
	}
	out.TotalChange = ratio(decimal.NewFromInt(int64(out.Total)), decimal.NewFromInt(int64(out.PreviousTotal)))
	by := func(col string) ([]Breakdown, error) {
		return handle.List[Breakdown](tx.Query(ctx, `SELECT `+col+` AS label, count(*)::text AS value FROM reporting.incidents WHERE `+inPeriod+`
			GROUP BY 1 ORDER BY count(*) DESC, 1`, from, to, tz))
	}
	if out.BySource, err = by("source"); err != nil {
		return out, err
	}
	if out.BySeverity, err = by("severity"); err != nil {
		return out, err
	}
	// Severity from the most to the least serious.
	rank := map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3}
	slices.SortStableFunc(out.BySeverity, func(a, b Breakdown) int { return rank[a.Label] - rank[b.Label] })
	if out.ByCategory, err = by("category"); err != nil {
		return out, err
	}
	if out.ByStatus, err = by("status"); err != nil {
		return out, err
	}

	trendFrom := monthStart(rg.Month).AddDate(0, -5, 0)
	rows, err := tx.Query(ctx, `SELECT to_char(occurred_at AT TIME ZONE $3, 'YYYY-MM'), source, count(*)::int FROM reporting.incidents
		WHERE `+inPeriod+` GROUP BY 1, 2`, trendFrom.Format(dateFmt), minDate(monthEnd(rg.Month), rg.To).Format(dateFmt), tz)
	if err != nil {
		return out, err
	}
	counts := map[string]map[string]int{}
	for rows.Next() {
		var m, src string
		var n int
		if err := rows.Scan(&m, &src, &n); err != nil {
			rows.Close()
			return out, err
		}
		if counts[m] == nil {
			counts[m] = map[string]int{}
		}
		counts[m][src] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for m := trendFrom; !m.After(rg.Month); m = m.AddDate(0, 1, 0) {
		c := counts[m.Format("2006-01")]
		out.Trend = append(out.Trend, IncidentMonth{Month: m.Format("2006-01"), Caddy: c["caddy"], GolfCart: c["golf_cart"], Banquet: c["banquet"]})
	}

	if out.OpenItems, err = handle.List[IncidentItem](tx.Query(ctx, incidentItemSelect+` WHERE status = 'open' ORDER BY occurred_at LIMIT 20`)); err != nil {
		return out, err
	}
	out.Items, err = handle.List[IncidentItem](tx.Query(ctx, incidentItemSelect+` WHERE `+inPeriod+` ORDER BY occurred_at DESC LIMIT 100`, from, to, tz))
	return out, err
}
