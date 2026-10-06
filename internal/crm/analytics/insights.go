package analytics

// CRM & Sales Analytics (EP-20): NPS (trend per line / context / month,
// drivers from comments, retention by NPS group), complaint SLA, sales
// funnel / owner / line / loss reasons / forecast vs actual with the
// commission analytics, RFM summary and movement, CLV and cohort retention,
// customer behaviour and segment comparison (EP-17).

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

func rate(n, d int) string {
	if d == 0 {
		return "0"
	}
	return decimal.NewFromInt(int64(n)).Div(decimal.NewFromInt(int64(d))).Round(4).String()
}

func npsOf(prom, det, n int) string {
	if n == 0 {
		return "0"
	}
	return decimal.NewFromInt(int64((prom - det) * 100)).Div(decimal.NewFromInt(int64(n))).Round(1).String()
}

// Period is an analytics period.
type Period struct{ From, To time.Time }

// ParsePeriod reads from / to (YYYY-MM-DD; default the last 90 days).
func ParsePeriod(from, to string, today time.Time, days int) (Period, error) {
	p := Period{From: today.AddDate(0, 0, -days), To: today}
	var err error
	if from != "" {
		if p.From, err = time.Parse("2006-01-02", from); err != nil {
			return p, handle.Invalid("from", "invalid_date", "YYYY-MM-DD")
		}
	}
	if to != "" {
		if p.To, err = time.Parse("2006-01-02", to); err != nil {
			return p, handle.Invalid("to", "invalid_date", "YYYY-MM-DD")
		}
	}
	if p.To.Before(p.From) {
		return p, handle.Invalid("to", "invalid_period", "to must not be before from")
	}
	return p, nil
}

// ── NPS (FR-CRA-01) ───────────────────────────────────────────────────────

// NPSBucket is the NPS of one group.
type NPSBucket struct {
	Key        string `json:"key"`
	Responses  int    `json:"responses"`
	Promoters  int    `json:"promoters"`
	Passives   int    `json:"passives"`
	Detractors int    `json:"detractors"`
	NPS        string `json:"nps"`
}

// NPSTrendPoint is the NPS of a month and line.
type NPSTrendPoint struct {
	Month        string `json:"month"`
	BusinessLine string `json:"businessLine"`
	Responses    int    `json:"responses"`
	NPS          string `json:"nps"`
}

// NPSDriver is a driver found in the comments.
type NPSDriver struct {
	Driver     string `json:"driver"`
	Mentions   int    `json:"mentions"`
	Promoters  int    `json:"promoters"`
	Detractors int    `json:"detractors"`
	NPS        string `json:"nps" doc:"NPS of the answers mentioning the driver"`
}

// NPSRetention is the retention of the customers of an NPS group.
type NPSRetention struct {
	Group         string `json:"group" enum:"promoter,passive,detractor"`
	Customers     int    `json:"customers"`
	Retained      int    `json:"retained" doc:"Visited within the retention days after the answer"`
	RetentionRate string `json:"retentionRate"`
}

// NPSAnalytics is the NPS analytics of a period.
type NPSAnalytics struct {
	From          string          `json:"from"`
	To            string          `json:"to"`
	Overall       NPSBucket       `json:"overall"`
	ByLine        []NPSBucket     `json:"byLine"`
	ByContext     []NPSBucket     `json:"byContext" doc:"Survey context (outlet, venue, round …)"`
	Trend         []NPSTrendPoint `json:"trend"`
	Drivers       []NPSDriver     `json:"drivers"`
	Retention     []NPSRetention  `json:"retention"`
	RetentionDays int             `json:"retentionDays"`
}

type npsRow struct {
	Customer *uuid.UUID `db:"customer_id"`
	Score    int        `db:"score"`
	Comment  *string    `db:"comment"`
	Line     string     `db:"business_line"`
	Context  string     `db:"context"`
	Month    string     `db:"month"`
	Day      time.Time  `db:"day"`
}

func (b *NPSBucket) add(score int) {
	b.Responses++
	switch {
	case score >= 9:
		b.Promoters++
	case score >= 7:
		b.Passives++
	default:
		b.Detractors++
	}
	b.NPS = npsOf(b.Promoters, b.Detractors, b.Responses)
}

// NPS computes the NPS analytics.
func NPS(ctx context.Context, q dbtx.Querier, property uuid.UUID, p Period, line string) (NPSAnalytics, error) {
	pol, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return NPSAnalytics{}, err
	}
	tz := location(ctx, q, property).String()
	rows, err := handle.List[npsRow](q.Query(ctx, `SELECT n.customer_id, n.score, n.comment, n.business_line,
		coalesce(f.context_label, f.context_type, n.business_line) AS context, to_char((n.created_at AT TIME ZONE $4)::date, 'YYYY-MM') AS month,
		(n.created_at AT TIME ZONE $4)::date AS day
		FROM reporting.eng_nps n LEFT JOIN crm.feedback f ON f.id = n.response_id WHERE n.property_id = $1
		AND (n.created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date AND ($5 = '' OR n.business_line = $5) ORDER BY n.created_at`,
		property, p.From, p.To, tz, line))
	if err != nil {
		return NPSAnalytics{}, err
	}
	out := NPSAnalytics{From: p.From.Format("2006-01-02"), To: p.To.Format("2006-01-02"), Overall: NPSBucket{Key: "all", NPS: "0"}, ByLine: []NPSBucket{},
		ByContext: []NPSBucket{}, Trend: []NPSTrendPoint{}, Drivers: []NPSDriver{}, Retention: []NPSRetention{}, RetentionDays: pol.RetentionDays}
	byLine, byCtx := map[string]*NPSBucket{}, map[string]*NPSBucket{}
	trend := map[[2]string]*NPSBucket{}
	drivers := map[string]*NPSBucket{}
	latest := map[uuid.UUID]npsRow{}
	for _, r := range rows {
		out.Overall.add(r.Score)
		for _, m := range []struct {
			set map[string]*NPSBucket
			k   string
		}{{byLine, r.Line}, {byCtx, r.Context}} {
			b := m.set[m.k]
			if b == nil {
				b = &NPSBucket{Key: m.k}
				m.set[m.k] = b
			}
			b.add(r.Score)
		}
		tk := [2]string{r.Month, r.Line}
		if trend[tk] == nil {
			trend[tk] = &NPSBucket{}
		}
		trend[tk].add(r.Score)
		if r.Comment != nil {
			c := strings.ToLower(*r.Comment)
			for d, words := range pol.NPSDrivers {
				for _, w := range words {
					if w != "" && strings.Contains(c, strings.ToLower(w)) {
						if drivers[d] == nil {
							drivers[d] = &NPSBucket{Key: d}
						}
						drivers[d].add(r.Score)
						break
					}
				}
			}
		}
		if r.Customer != nil {
			latest[*r.Customer] = r
		}
	}
	for _, m := range []struct {
		set map[string]*NPSBucket
		out *[]NPSBucket
	}{{byLine, &out.ByLine}, {byCtx, &out.ByContext}} {
		for _, b := range m.set {
			*m.out = append(*m.out, *b)
		}
		sort.Slice(*m.out, func(i, j int) bool { return (*m.out)[i].Key < (*m.out)[j].Key })
	}
	for k, b := range trend {
		out.Trend = append(out.Trend, NPSTrendPoint{Month: k[0], BusinessLine: k[1], Responses: b.Responses, NPS: b.NPS})
	}
	sort.Slice(out.Trend, func(i, j int) bool {
		if out.Trend[i].Month != out.Trend[j].Month {
			return out.Trend[i].Month < out.Trend[j].Month
		}
		return out.Trend[i].BusinessLine < out.Trend[j].BusinessLine
	})
	for _, b := range drivers {
		out.Drivers = append(out.Drivers, NPSDriver{Driver: b.Key, Mentions: b.Responses, Promoters: b.Promoters, Detractors: b.Detractors, NPS: b.NPS})
	}
	sort.Slice(out.Drivers, func(i, j int) bool {
		if out.Drivers[i].Mentions != out.Drivers[j].Mentions {
			return out.Drivers[i].Mentions > out.Drivers[j].Mentions
		}
		return out.Drivers[i].Driver < out.Drivers[j].Driver
	})
	// Retention by NPS group: a visit within N days after the latest answer.
	groups := map[string]*NPSRetention{"promoter": {Group: "promoter"}, "passive": {Group: "passive"}, "detractor": {Group: "detractor"}}
	for c, r := range latest {
		g := "detractor"
		switch {
		case r.Score >= 9:
			g = "promoter"
		case r.Score >= 7:
			g = "passive"
		}
		var back bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.eng_folio_lines WHERE customer_id = $1 AND NOT liability
			AND business_date > $2::date AND business_date <= $2::date + $3::int)`, c, r.Day, pol.RetentionDays).Scan(&back); err != nil {
			return out, err
		}
		groups[g].Customers++
		if back {
			groups[g].Retained++
		}
	}
	for _, g := range []string{"promoter", "passive", "detractor"} {
		x := groups[g]
		x.RetentionRate = rate(x.Retained, x.Customers)
		out.Retention = append(out.Retention, *x)
	}
	return out, nil
}

// ── complaint SLA (FR-CRA-02) ─────────────────────────────────────────────

// SLAGroup is the SLA of one business line or priority.
type SLAGroup struct {
	Key                string `json:"key" db:"key"`
	Tickets            int    `json:"tickets" db:"tickets"`
	Resolved           int    `json:"resolved" db:"resolved"`
	WithinSLA          int    `json:"withinSla" db:"within_sla"`
	Compliance         string `json:"compliance" db:"compliance"`
	AvgResolutionHours string `json:"avgResolutionHours" db:"avg_hours"`
	Escalated          int    `json:"escalated" db:"escalated"`
}

// SLACategory is a recurring complaint category.
type SLACategory struct {
	Category        string `json:"category" db:"category"`
	Tickets         int    `json:"tickets" db:"tickets"`
	Customers       int    `json:"customers" db:"customers"`
	RepeatCustomers int    `json:"repeatCustomers" db:"repeat_customers" doc:"Customers with 2+ tickets of the category"`
}

// SLAAnalytics is the complaint SLA analytics of a period.
type SLAAnalytics struct {
	From                    string        `json:"from"`
	To                      string        `json:"to"`
	Tickets                 int           `json:"tickets"`
	Open                    int           `json:"open"`
	Resolved                int           `json:"resolved"`
	ResolutionCompliance    string        `json:"resolutionCompliance" doc:"Resolved within the SLA ÷ resolved"`
	FirstResponseCompliance string        `json:"firstResponseCompliance" doc:"First response within the SLA ÷ responded"`
	AvgResolutionHours      string        `json:"avgResolutionHours"`
	AvgFirstResponseHours   string        `json:"avgFirstResponseHours"`
	Escalated               int           `json:"escalated"`
	Reopened                int           `json:"reopened"`
	ByLine                  []SLAGroup    `json:"byLine"`
	ByPriority              []SLAGroup    `json:"byPriority"`
	RecurringCategories     []SLACategory `json:"recurringCategories"`
}

const slaGroupSQL = `SELECT %s AS key, count(*)::int AS tickets, count(*) FILTER (WHERE resolved_at IS NOT NULL)::int AS resolved,
	count(*) FILTER (WHERE resolved_at IS NOT NULL AND NOT resolution_breached)::int AS within_sla,
	trim_scale(round(coalesce(count(*) FILTER (WHERE resolved_at IS NOT NULL AND NOT resolution_breached)::numeric
	  / nullif(count(*) FILTER (WHERE resolved_at IS NOT NULL), 0), 0), 4))::text AS compliance,
	trim_scale(round(coalesce(avg(extract(epoch FROM resolved_at - created_at) / 3600) FILTER (WHERE resolved_at IS NOT NULL), 0)::numeric, 2))::text AS avg_hours,
	count(*) FILTER (WHERE escalation_level > 0)::int AS escalated
	FROM reporting.eng_tickets WHERE property_id = $1 AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date GROUP BY 1 ORDER BY 1`

// SLA computes the complaint SLA analytics.
func SLA(ctx context.Context, q dbtx.Querier, property uuid.UUID, p Period) (SLAAnalytics, error) {
	tz := location(ctx, q, property).String()
	out := SLAAnalytics{From: p.From.Format("2006-01-02"), To: p.To.Format("2006-01-02")}
	var avgRes, avgFirst, resComp, firstComp string
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE status IN ('open', 'in_progress', 'escalated'))::int,
		count(*) FILTER (WHERE resolved_at IS NOT NULL)::int,
		trim_scale(round(coalesce(count(*) FILTER (WHERE resolved_at IS NOT NULL AND NOT resolution_breached)::numeric
		  / nullif(count(*) FILTER (WHERE resolved_at IS NOT NULL), 0), 0), 4))::text,
		trim_scale(round(coalesce(count(*) FILTER (WHERE first_responded_at IS NOT NULL AND NOT first_response_breached)::numeric
		  / nullif(count(*) FILTER (WHERE first_responded_at IS NOT NULL), 0), 0), 4))::text,
		trim_scale(round(coalesce(avg(extract(epoch FROM resolved_at - created_at) / 3600) FILTER (WHERE resolved_at IS NOT NULL), 0)::numeric, 2))::text,
		trim_scale(round(coalesce(avg(extract(epoch FROM first_responded_at - created_at) / 3600) FILTER (WHERE first_responded_at IS NOT NULL), 0)::numeric, 2))::text,
		count(*) FILTER (WHERE escalation_level > 0)::int, coalesce(sum(reopened_count), 0)::int
		FROM reporting.eng_tickets WHERE property_id = $1 AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date`,
		property, p.From, p.To, tz).Scan(&out.Tickets, &out.Open, &out.Resolved, &resComp, &firstComp, &avgRes, &avgFirst, &out.Escalated, &out.Reopened); err != nil {
		return out, err
	}
	out.ResolutionCompliance, out.FirstResponseCompliance, out.AvgResolutionHours, out.AvgFirstResponseHours = resComp, firstComp, avgRes, avgFirst
	var err error
	if out.ByLine, err = handle.List[SLAGroup](q.Query(ctx, strings.Replace(slaGroupSQL, "%s", "business_line", 1), property, p.From, p.To, tz)); err != nil {
		return out, err
	}
	if out.ByPriority, err = handle.List[SLAGroup](q.Query(ctx, strings.Replace(slaGroupSQL, "%s", "priority", 1), property, p.From, p.To, tz)); err != nil {
		return out, err
	}
	out.RecurringCategories, err = handle.List[SLACategory](q.Query(ctx, `WITH t AS (SELECT coalesce(category_name, 'Uncategorised') AS category, customer_id
		  FROM reporting.eng_tickets WHERE property_id = $1 AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date),
		r AS (SELECT category, customer_id FROM t WHERE customer_id IS NOT NULL GROUP BY 1, 2 HAVING count(*) > 1)
		SELECT t.category, count(*)::int AS tickets, count(DISTINCT t.customer_id)::int AS customers,
		(SELECT count(*) FROM r WHERE r.category = t.category)::int AS repeat_customers FROM t GROUP BY t.category ORDER BY count(*) DESC, 1`,
		property, p.From, p.To, tz))
	return out, err
}

// ── sales & commission (FR-CRA-03/04) ─────────────────────────────────────

// SalesFunnelStage is one stage of a pipeline funnel.
type SalesFunnelStage struct {
	Pipeline         string `json:"pipeline" db:"pipeline"`
	Stage            string `json:"stage" db:"stage"`
	Order            int    `json:"order" db:"sort_order"`
	Reached          int    `json:"reached" db:"reached" doc:"Opportunities of the period that reached the stage"`
	ConversionToNext string `json:"conversionToNext" db:"-"`
}

// SalesGroupStats are the sales figures of an owner or line.
type SalesGroupStats struct {
	Key           string `json:"key" db:"key"`
	Opportunities int    `json:"opportunities" db:"opportunities"`
	Won           int    `json:"won" db:"won"`
	Lost          int    `json:"lost" db:"lost"`
	WinRate       string `json:"winRate" db:"win_rate"`
	WonValue      string `json:"wonValue" db:"won_value"`
	AvgCycleDays  string `json:"avgCycleDays" db:"avg_cycle"`
}

// SalesLossReason is a loss reason.
type SalesLossReason struct {
	Reason string `json:"reason" db:"reason"`
	Count  int    `json:"count" db:"n"`
	Value  string `json:"value" db:"value"`
}

// SalesForecastMonth is forecast vs actual of a month.
type SalesForecastMonth struct {
	Month    string `json:"month" db:"month"`
	Forecast string `json:"forecast" db:"forecast" doc:"Weighted value of the opportunities expected to close in the month"`
	Target   string `json:"target" db:"target"`
	Actual   string `json:"actual" db:"actual" doc:"Value won in the month"`
}

// CommissionGroup is the commission of an owner or scheme.
type CommissionGroup struct {
	Key        string `json:"key" db:"key"`
	Deals      int    `json:"deals" db:"deals"`
	Revenue    string `json:"revenue" db:"revenue" doc:"Commission basis (money received)"`
	Commission string `json:"commission" db:"commission"`
	Rate       string `json:"rate" db:"rate" doc:"Commission ÷ revenue"`
}

// SalesAnalytics is the sales performance of a period.
type SalesAnalytics struct {
	From               string               `json:"from"`
	To                 string               `json:"to"`
	Opportunities      int                  `json:"opportunities"`
	Won                int                  `json:"won"`
	Lost               int                  `json:"lost"`
	Open               int                  `json:"open"`
	WinRate            string               `json:"winRate"`
	WonValue           string               `json:"wonValue"`
	AvgCycleDays       string               `json:"avgCycleDays"`
	Funnel             []SalesFunnelStage   `json:"funnel"`
	ByOwner            []SalesGroupStats    `json:"byOwner"`
	ByLine             []SalesGroupStats    `json:"byLine"`
	LossReasons        []SalesLossReason    `json:"lossReasons"`
	Forecast           []SalesForecastMonth `json:"forecast"`
	CommissionTotal    string               `json:"commissionTotal"`
	CommissionRevenue  string               `json:"commissionRevenue"`
	CommissionRate     string               `json:"commissionRate"`
	CommissionByOwner  []CommissionGroup    `json:"commissionByOwner"`
	CommissionByScheme []CommissionGroup    `json:"commissionByScheme" doc:"Scheme effectiveness"`
}

const salesGroupSQL = `SELECT %s AS key, count(*)::int AS opportunities, count(*) FILTER (WHERE o.status = 'won')::int AS won,
	count(*) FILTER (WHERE o.status = 'lost')::int AS lost,
	trim_scale(round(coalesce(count(*) FILTER (WHERE o.status = 'won')::numeric / nullif(count(*) FILTER (WHERE o.status IN ('won', 'lost')), 0), 0), 4))::text AS win_rate,
	trim_scale(coalesce(sum(o.expected_value) FILTER (WHERE o.status = 'won'), 0))::text AS won_value,
	trim_scale(round(coalesce(avg(extract(epoch FROM o.won_at - o.created_at) / 86400) FILTER (WHERE o.status = 'won'), 0)::numeric, 1))::text AS avg_cycle
	FROM crm.sales_opportunities o LEFT JOIN platform.users u ON u.id = o.owner_user_id
	WHERE o.property_id = $1 AND (o.created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date GROUP BY 1 ORDER BY 1`

// Sales computes the sales performance and commission analytics.
func Sales(ctx context.Context, q dbtx.Querier, property uuid.UUID, p Period) (SalesAnalytics, error) {
	tz := location(ctx, q, property).String()
	out := SalesAnalytics{From: p.From.Format("2006-01-02"), To: p.To.Format("2006-01-02")}
	args := []any{property, p.From, p.To, tz}
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE status = 'won')::int, count(*) FILTER (WHERE status = 'lost')::int,
		count(*) FILTER (WHERE status = 'open')::int,
		trim_scale(round(coalesce(count(*) FILTER (WHERE status = 'won')::numeric / nullif(count(*) FILTER (WHERE status IN ('won', 'lost')), 0), 0), 4))::text,
		trim_scale(coalesce(sum(expected_value) FILTER (WHERE status = 'won'), 0))::text,
		trim_scale(round(coalesce(avg(extract(epoch FROM won_at - created_at) / 86400) FILTER (WHERE status = 'won'), 0)::numeric, 1))::text
		FROM crm.sales_opportunities WHERE property_id = $1 AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date`, args...).
		Scan(&out.Opportunities, &out.Won, &out.Lost, &out.Open, &out.WinRate, &out.WonValue, &out.AvgCycleDays); err != nil {
		return out, err
	}
	var err error
	if out.Funnel, err = handle.List[SalesFunnelStage](q.Query(ctx, `WITH o AS (SELECT o.id, o.pipeline_id, o.stage_id FROM crm.sales_opportunities o
		  WHERE o.property_id = $1 AND (o.created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date),
		reached AS (SELECT o.id, o.pipeline_id, max(st.sort_order) AS top FROM o JOIN crm.sales_pipeline_stages st ON st.pipeline_id = o.pipeline_id
		  AND st.kind <> 'lost' AND (st.id = o.stage_id OR EXISTS (SELECT 1 FROM crm.sales_stage_history h WHERE h.opportunity_id = o.id AND h.to_stage_id = st.id))
		  GROUP BY o.id, o.pipeline_id)
		SELECT p.name AS pipeline, st.name AS stage, st.sort_order, count(r.id)::int AS reached
		FROM crm.sales_pipelines p JOIN crm.sales_pipeline_stages st ON st.pipeline_id = p.id AND st.kind <> 'lost' AND st.archived_at IS NULL
		LEFT JOIN reached r ON r.pipeline_id = p.id AND r.top >= st.sort_order
		WHERE p.property_id = $1 AND EXISTS (SELECT 1 FROM o WHERE o.pipeline_id = p.id) GROUP BY p.name, st.name, st.sort_order ORDER BY p.name, st.sort_order`,
		args...)); err != nil {
		return out, err
	}
	for i := range out.Funnel {
		out.Funnel[i].ConversionToNext = "0"
		if i+1 < len(out.Funnel) && out.Funnel[i+1].Pipeline == out.Funnel[i].Pipeline {
			out.Funnel[i].ConversionToNext = rate(out.Funnel[i+1].Reached, out.Funnel[i].Reached)
		}
	}
	if out.ByOwner, err = handle.List[SalesGroupStats](q.Query(ctx, strings.Replace(salesGroupSQL, "%s", "coalesce(u.full_name, 'Unassigned')", 1), args...)); err != nil {
		return out, err
	}
	if out.ByLine, err = handle.List[SalesGroupStats](q.Query(ctx, strings.Replace(salesGroupSQL, "%s", "o.line", 1), args...)); err != nil {
		return out, err
	}
	if out.LossReasons, err = handle.List[SalesLossReason](q.Query(ctx, `SELECT coalesce(lost_reason, 'other') AS reason, count(*)::int AS n,
		trim_scale(coalesce(sum(expected_value), 0))::text AS value FROM crm.sales_opportunities WHERE property_id = $1 AND status = 'lost'
		AND (lost_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date GROUP BY 1 ORDER BY 2 DESC, 1`, args...)); err != nil {
		return out, err
	}
	if out.Forecast, err = handle.List[SalesForecastMonth](q.Query(ctx, `WITH m AS (SELECT generate_series(date_trunc('month', $2::date), date_trunc('month', $3::date),
		interval '1 month')::date AS month)
		SELECT to_char(m.month, 'YYYY-MM') AS month,
		trim_scale(coalesce((SELECT sum(expected_value * probability / 100) FROM crm.sales_opportunities o WHERE o.property_id = $1 AND o.status <> 'lost'
		  AND date_trunc('month', o.expected_close_date)::date = m.month), 0))::text AS forecast,
		trim_scale(coalesce((SELECT sum(target_revenue) FROM crm.sales_targets t WHERE t.property_id = $1 AND t.status = 'active'
		  AND date_trunc('month', t.period_start)::date = m.month AND t.period_type = 'month'), 0))::text AS target,
		trim_scale(coalesce((SELECT sum(expected_value) FROM crm.sales_opportunities o WHERE o.property_id = $1 AND o.status = 'won'
		  AND date_trunc('month', (o.won_at AT TIME ZONE $4)::date)::date = m.month), 0))::text AS actual
		FROM m ORDER BY m.month`, args...)); err != nil {
		return out, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount), 0))::text, trim_scale(coalesce(sum(basis_amount) FILTER (WHERE kind = 'earned'), 0))::text
		FROM crm.sales_commissions WHERE property_id = $1 AND recognized_on BETWEEN $2::date AND $3::date AND $4 <> ''`, args...).
		Scan(&out.CommissionTotal, &out.CommissionRevenue); err != nil {
		return out, err
	}
	out.CommissionRate = "0"
	if rev := dec(out.CommissionRevenue); rev.IsPositive() {
		out.CommissionRate = dec(out.CommissionTotal).Div(rev).Round(4).String()
	}
	commSQL := `SELECT %s AS key, count(DISTINCT c.quotation_id)::int AS deals, trim_scale(coalesce(sum(c.basis_amount) FILTER (WHERE c.kind = 'earned'), 0))::text AS revenue,
		trim_scale(coalesce(sum(c.amount), 0))::text AS commission,
		trim_scale(round(coalesce(sum(c.amount) / nullif(sum(c.basis_amount) FILTER (WHERE c.kind = 'earned'), 0), 0), 4))::text AS rate
		FROM crm.sales_commissions c JOIN platform.users u ON u.id = c.user_id LEFT JOIN crm.sales_commission_schemes s ON s.id = c.scheme_id
		WHERE c.property_id = $1 AND c.recognized_on BETWEEN $2::date AND $3::date AND $4 <> '' GROUP BY 1 ORDER BY 1`
	if out.CommissionByOwner, err = handle.List[CommissionGroup](q.Query(ctx, strings.Replace(commSQL, "%s", "u.full_name", 1), args...)); err != nil {
		return out, err
	}
	out.CommissionByScheme, err = handle.List[CommissionGroup](q.Query(ctx, strings.Replace(commSQL, "%s", "coalesce(s.name, 'No scheme')", 1), args...))
	return out, err
}

// ── RFM, CLV & cohorts (FR-SEG-02, FR-CRA-05) ─────────────────────────────

// RFMGroupStats are the figures of an RFM group.
type RFMGroupStats struct {
	Group          string `json:"group" db:"rfm_group"`
	Customers      int    `json:"customers" db:"customers"`
	Share          string `json:"share" db:"-"`
	Monetary       string `json:"monetary" db:"monetary"`
	AvgRecencyDays string `json:"avgRecencyDays" db:"avg_recency"`
	AvgFrequency   string `json:"avgFrequency" db:"avg_frequency"`
	AvgCLV         string `json:"avgClv" db:"avg_clv"`
}

// LineMix is the number of active customers of a business line.
type LineMix struct {
	BusinessLine string `json:"businessLine" db:"business_line"`
	Customers    int    `json:"customers" db:"customers"`
	Monetary     string `json:"monetary" db:"monetary"`
}

// RFMSummary is the latest RFM snapshot.
type RFMSummary struct {
	AsOf        *string         `json:"asOf"`
	RefreshedAt *time.Time      `json:"refreshedAt"`
	Customers   int             `json:"customers"`
	MultiLine   int             `json:"multiLine" doc:"Customers active in 2+ business lines (cross-business)"`
	Groups      []RFMGroupStats `json:"groups"`
	Lines       []LineMix       `json:"lines"`
}

func latestAsOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, at *time.Time) (*time.Time, error) {
	var d *time.Time
	err := q.QueryRow(ctx, `SELECT max(as_of) FROM crm.rfm_scores WHERE property_id = $1 AND ($2::date IS NULL OR as_of <= $2::date)`, property, at).Scan(&d)
	return d, err
}

// RFM summarises the latest snapshot.
func RFM(ctx context.Context, q dbtx.Querier, property uuid.UUID) (RFMSummary, error) {
	out := RFMSummary{Groups: []RFMGroupStats{}, Lines: []LineMix{}}
	asOf, err := latestAsOf(ctx, q, property, nil)
	if err != nil || asOf == nil {
		return out, err
	}
	s := asOf.Format("2006-01-02")
	out.AsOf = &s
	if err := q.QueryRow(ctx, `SELECT max(finished_at) FROM crm.analytics_runs WHERE property_id = $1 AND kind = 'rfm'`, property).Scan(&out.RefreshedAt); err != nil {
		return out, err
	}
	gs, err := handle.List[RFMGroupStats](q.Query(ctx, `SELECT rfm_group, count(*)::int AS customers, trim_scale(sum(monetary))::text AS monetary,
		trim_scale(round(avg(recency_days), 1))::text AS avg_recency, trim_scale(round(avg(frequency), 1))::text AS avg_frequency,
		trim_scale(round(avg(clv), 2))::text AS avg_clv FROM crm.rfm_scores WHERE property_id = $1 AND as_of = $2 GROUP BY rfm_group`, property, *asOf))
	if err != nil {
		return out, err
	}
	by := map[string]RFMGroupStats{}
	for _, g := range gs {
		out.Customers += g.Customers
		by[g.Group] = g
	}
	for _, name := range Groups {
		g, ok := by[name]
		if !ok {
			g = RFMGroupStats{Group: name, Monetary: "0", AvgRecencyDays: "0", AvgFrequency: "0", AvgCLV: "0"}
		}
		g.Share = rate(g.Customers, out.Customers)
		out.Groups = append(out.Groups, g)
	}
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM crm.rfm_scores WHERE property_id = $1 AND as_of = $2 AND cardinality(lines) >= 2`, property, *asOf).
		Scan(&out.MultiLine); err != nil {
		return out, err
	}
	out.Lines, err = handle.List[LineMix](q.Query(ctx, `SELECT business_line, count(*) FILTER (WHERE frequency > 0)::int AS customers,
		trim_scale(sum(monetary))::text AS monetary FROM crm.customer_line_behavior WHERE property_id = $1 GROUP BY business_line ORDER BY business_line`, property))
	return out, err
}

// RFMCustomer is the RFM score of a customer.
type RFMCustomer struct {
	CustomerID   uuid.UUID `json:"customerId" db:"customer_id"`
	CustomerCode string    `json:"customerCode" db:"code"`
	CustomerName string    `json:"customerName" db:"name"`
	AsOf         string    `json:"asOf" db:"as_of"`
	RScore       int       `json:"rScore" db:"r_score"`
	FScore       int       `json:"fScore" db:"f_score"`
	MScore       int       `json:"mScore" db:"m_score"`
	Group        string    `json:"group" db:"rfm_group"`
	RecencyDays  *int      `json:"recencyDays" db:"recency_days"`
	Frequency    int       `json:"frequency" db:"frequency"`
	Monetary     string    `json:"monetary" db:"monetary"`
	CLV          string    `json:"clv" db:"clv"`
	Lines        []string  `json:"lines" db:"lines"`
	VIPLevel     *string   `json:"vipLevel" db:"vip_level"`
}

const rfmCustomerSelect = `SELECT s.customer_id, c.code, c.name, to_char(s.as_of, 'YYYY-MM-DD') AS as_of, s.r_score, s.f_score, s.m_score, s.rfm_group,
	s.recency_days, s.frequency, trim_scale(s.monetary)::text AS monetary, trim_scale(s.clv)::text AS clv, s.lines,
	(SELECT v.level FROM crm.vip_customers v WHERE v.customer_id = s.customer_id AND v.status = 'active') AS vip_level
	FROM crm.rfm_scores s JOIN crm.customers c ON c.id = s.customer_id`

// RFMCustomers lists the scores of the latest snapshot (optionally of a group).
func RFMCustomers(ctx context.Context, q dbtx.Querier, property uuid.UUID, group, order string, limit int) ([]RFMCustomer, error) {
	asOf, err := latestAsOf(ctx, q, property, nil)
	if err != nil || asOf == nil {
		return []RFMCustomer{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	by := "s.monetary DESC, c.name"
	if order == "clv" {
		by = "s.clv DESC, c.name"
	}
	return handle.List[RFMCustomer](q.Query(ctx, rfmCustomerSelect+` WHERE s.property_id = $1 AND s.as_of = $2 AND ($3 = '' OR s.rfm_group = $3)
		ORDER BY `+by+` LIMIT $4`, property, *asOf, group, limit))
}

// RFMMove is the number of customers moving between two groups.
type RFMMove struct {
	FromGroup string `json:"fromGroup" db:"from_group" doc:"none: not scored in the first snapshot"`
	ToGroup   string `json:"toGroup" db:"to_group"`
	Customers int    `json:"customers" db:"customers"`
}

// RFMMovement compares two snapshots (FR-SEG-05).
type RFMMovement struct {
	From  *string   `json:"from"`
	To    *string   `json:"to"`
	Moves []RFMMove `json:"moves"`
}

// Movement compares the snapshots on or before from and to (default: the
// two latest snapshots).
func Movement(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to *time.Time) (RFMMovement, error) {
	out := RFMMovement{Moves: []RFMMove{}}
	t, err := latestAsOf(ctx, q, property, to)
	if err != nil || t == nil {
		return out, err
	}
	var f *time.Time
	if from != nil {
		f, err = latestAsOf(ctx, q, property, from)
	} else {
		err = q.QueryRow(ctx, `SELECT max(as_of) FROM crm.rfm_scores WHERE property_id = $1 AND as_of < $2`, property, *t).Scan(&f)
	}
	if err != nil {
		return out, err
	}
	ts := t.Format("2006-01-02")
	out.To = &ts
	if f != nil {
		fs := f.Format("2006-01-02")
		out.From = &fs
	}
	out.Moves, err = handle.List[RFMMove](q.Query(ctx, `SELECT coalesce(a.rfm_group, 'none') AS from_group, b.rfm_group AS to_group, count(*)::int AS customers
		FROM crm.rfm_scores b LEFT JOIN crm.rfm_scores a ON a.customer_id = b.customer_id AND a.as_of = $3::date
		WHERE b.property_id = $1 AND b.as_of = $2 GROUP BY 1, 2 ORDER BY 1, 2`, property, *t, f))
	return out, err
}

// CohortRetention is the retention of a first-visit cohort.
type CohortRetention struct {
	Cohort    string `json:"cohort" db:"cohort" doc:"Month of the first visit"`
	Customers int    `json:"customers" db:"customers"`
	Month1    int    `json:"month1" db:"m1"`
	Month3    int    `json:"month3" db:"m3"`
	Month6    int    `json:"month6" db:"m6"`
	Rate1     string `json:"rate1" db:"-"`
	Rate3     string `json:"rate3" db:"-"`
	Rate6     string `json:"rate6" db:"-"`
}

// CLVAnalytics is the customer lifetime value and cohort retention.
type CLVAnalytics struct {
	AsOf      *string           `json:"asOf"`
	Customers int               `json:"customers"`
	TotalCLV  string            `json:"totalClv"`
	AvgCLV    string            `json:"avgClv"`
	ByGroup   []RFMGroupStats   `json:"byGroup"`
	Top       []RFMCustomer     `json:"top"`
	Cohorts   []CohortRetention `json:"cohorts"`
}

// CLVCohorts computes the CLV of the latest snapshot and the retention of
// the first-visit cohorts of the last 12 months.
func CLVCohorts(ctx context.Context, q dbtx.Querier, property uuid.UUID) (CLVAnalytics, error) {
	out := CLVAnalytics{TotalCLV: "0", AvgCLV: "0", ByGroup: []RFMGroupStats{}, Top: []RFMCustomer{}, Cohorts: []CohortRetention{}}
	s, err := RFM(ctx, q, property)
	if err != nil {
		return out, err
	}
	out.AsOf, out.Customers, out.ByGroup = s.AsOf, s.Customers, s.Groups
	if s.AsOf != nil {
		var total string
		if err := q.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(clv), 0))::text FROM crm.rfm_scores WHERE property_id = $1 AND as_of = $2::date`,
			property, *s.AsOf).Scan(&total); err != nil {
			return out, err
		}
		out.TotalCLV = total
		if s.Customers > 0 {
			out.AvgCLV = dec(total).Div(decimal.NewFromInt(int64(s.Customers))).Round(2).String()
		}
		if out.Top, err = RFMCustomers(ctx, q, property, "", "clv", 10); err != nil {
			return out, err
		}
	}
	today := localToday(ctx, q, property)
	out.Cohorts, err = handle.List[CohortRetention](q.Query(ctx, `WITH f AS (SELECT customer_id, min(business_date) AS first_day FROM reporting.eng_folio_lines
		  WHERE property_id = $1 AND customer_id IS NOT NULL AND NOT liability GROUP BY customer_id),
		c AS (SELECT customer_id, date_trunc('month', first_day)::date AS cohort FROM f WHERE first_day >= date_trunc('month', $2::date - interval '11 months')),
		a AS (SELECT DISTINCT customer_id, date_trunc('month', business_date)::date AS m FROM reporting.eng_folio_lines
		  WHERE property_id = $1 AND customer_id IS NOT NULL AND NOT liability)
		SELECT to_char(c.cohort, 'YYYY-MM') AS cohort, count(DISTINCT c.customer_id)::int AS customers,
		count(DISTINCT a.customer_id) FILTER (WHERE a.m = (c.cohort + interval '1 month')::date)::int AS m1,
		count(DISTINCT a.customer_id) FILTER (WHERE a.m = (c.cohort + interval '3 months')::date)::int AS m3,
		count(DISTINCT a.customer_id) FILTER (WHERE a.m = (c.cohort + interval '6 months')::date)::int AS m6
		FROM c LEFT JOIN a ON a.customer_id = c.customer_id GROUP BY c.cohort ORDER BY c.cohort`, property, today))
	for i := range out.Cohorts {
		c := &out.Cohorts[i]
		c.Rate1, c.Rate3, c.Rate6 = rate(c.Month1, c.Customers), rate(c.Month3, c.Customers), rate(c.Month6, c.Customers)
	}
	return out, err
}

// ── customer behaviour & segment comparison (FR-SEG-01, FR-SEG-05) ────────

// LineBehavior is the behaviour of a customer in one business line.
type LineBehavior struct {
	BusinessLine string     `json:"businessLine" db:"business_line"`
	LastAt       *time.Time `json:"lastAt" db:"last_at"`
	RecencyDays  *int       `json:"recencyDays" db:"recency_days"`
	Frequency    int        `json:"frequency" db:"frequency"`
	Monetary     string     `json:"monetary" db:"monetary"`
}

// RFMHistoryPoint is the group of a customer in one snapshot.
type RFMHistoryPoint struct {
	AsOf  string `json:"asOf" db:"as_of"`
	Group string `json:"group" db:"rfm_group"`
}

// CustomerBehavior is the cross-business behaviour of a customer.
type CustomerBehavior struct {
	CustomerID uuid.UUID         `json:"customerId"`
	RFM        *RFMCustomer      `json:"rfm"`
	Lines      []LineBehavior    `json:"lines"`
	History    []RFMHistoryPoint `json:"history"`
	VIP        *VIPFlag          `json:"vip"`
}

// Behavior loads the behaviour of a customer.
func Behavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (CustomerBehavior, error) {
	out := CustomerBehavior{CustomerID: customer}
	var err error
	if out.Lines, err = handle.List[LineBehavior](q.Query(ctx, `SELECT business_line, last_at, recency_days, frequency, trim_scale(monetary)::text AS monetary
		FROM crm.customer_line_behavior WHERE customer_id = $1 AND property_id = $2 ORDER BY monetary DESC, business_line`, customer, property)); err != nil {
		return out, err
	}
	rs, err := handle.List[RFMCustomer](q.Query(ctx, rfmCustomerSelect+` WHERE s.customer_id = $1 AND s.property_id = $2 ORDER BY s.as_of DESC LIMIT 1`,
		customer, property))
	if err != nil {
		return out, err
	}
	if len(rs) == 1 {
		out.RFM = &rs[0]
	}
	if out.History, err = handle.List[RFMHistoryPoint](q.Query(ctx, `SELECT to_char(as_of, 'YYYY-MM-DD') AS as_of, rfm_group FROM crm.rfm_scores
		WHERE customer_id = $1 AND property_id = $2 ORDER BY as_of DESC LIMIT 12`, customer, property)); err != nil {
		return out, err
	}
	f, err := Flag(ctx, q, property, customer)
	if err != nil {
		return out, err
	}
	out.VIP = &f
	return out, nil
}

// SegmentComparison compares a segment with others (FR-SEG-05).
type SegmentComparison struct {
	SegmentID    uuid.UUID      `json:"segmentId"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Members      int            `json:"members"`
	Scored       int            `json:"scored" doc:"Members with an RFM score"`
	AvgMonetary  string         `json:"avgMonetary"`
	AvgFrequency string         `json:"avgFrequency"`
	AvgCLV       string         `json:"avgClv"`
	VIP          int            `json:"vip"`
	Groups       map[string]int `json:"groups" doc:"Members per RFM group"`
	Lines        map[string]int `json:"lines" doc:"Members active per business line"`
}

// CompareSegments compares segments of a property.
func CompareSegments(ctx context.Context, q dbtx.Querier, property uuid.UUID, ids []uuid.UUID) ([]SegmentComparison, error) {
	asOf, err := latestAsOf(ctx, q, property, nil)
	if err != nil {
		return nil, err
	}
	out := []SegmentComparison{}
	for _, sid := range ids {
		c := SegmentComparison{SegmentID: sid, Groups: map[string]int{}, Lines: map[string]int{}, AvgMonetary: "0", AvgFrequency: "0", AvgCLV: "0"}
		err := q.QueryRow(ctx, `SELECT code, name, (SELECT count(*) FROM crm.segment_members WHERE segment_id = s.id)::int FROM crm.segments s
			WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, sid, property).Scan(&c.Code, &c.Name, &c.Members)
		if dbtx.IsNoRows(err) {
			return nil, handle.Invalid("segmentIds", "not_found", "segment "+sid.String()+" not found")
		}
		if err != nil {
			return nil, err
		}
		if asOf != nil {
			if err := q.QueryRow(ctx, `SELECT count(*)::int, trim_scale(round(coalesce(avg(r.monetary), 0), 2))::text,
				trim_scale(round(coalesce(avg(r.frequency), 0), 1))::text, trim_scale(round(coalesce(avg(r.clv), 0), 2))::text
				FROM crm.segment_members m JOIN crm.rfm_scores r ON r.customer_id = m.customer_id AND r.as_of = $2 WHERE m.segment_id = $1`,
				sid, *asOf).Scan(&c.Scored, &c.AvgMonetary, &c.AvgFrequency, &c.AvgCLV); err != nil {
				return nil, err
			}
			rows, err := q.Query(ctx, `SELECT r.rfm_group, count(*)::int FROM crm.segment_members m JOIN crm.rfm_scores r ON r.customer_id = m.customer_id
				AND r.as_of = $2 WHERE m.segment_id = $1 GROUP BY 1`, sid, *asOf)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var g string
				var n int
				if err := rows.Scan(&g, &n); err != nil {
					rows.Close()
					return nil, err
				}
				c.Groups[g] = n
			}
			rows.Close()
		}
		rows, err := q.Query(ctx, `SELECT b.business_line, count(*)::int FROM crm.segment_members m JOIN crm.customer_line_behavior b
			ON b.customer_id = m.customer_id AND b.frequency > 0 WHERE m.segment_id = $1 GROUP BY 1`, sid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var l string
			var n int
			if err := rows.Scan(&l, &n); err != nil {
				rows.Close()
				return nil, err
			}
			c.Lines[l] = n
		}
		rows.Close()
		if err := q.QueryRow(ctx, `SELECT count(*)::int FROM crm.segment_members m JOIN crm.vip_customers v ON v.customer_id = m.customer_id
			AND v.status = 'active' WHERE m.segment_id = $1`, sid).Scan(&c.VIP); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
