package reporting

// Executive Overview across domains with targets, MoM / YoY and per
// property comparison (FR-BI-02/03/08), drill-down from a KPI to its
// dimensions and source transactions (FR-BI-04), HR Performance (EP-27) and
// the KPI definition catalogue (FR-BI-07). Everything reads the analytics
// store (or, for HR Performance, the reporting views) on the read replica
// within the query budget of the BI Policies.

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// ExecutiveKPI is one KPI of the Executive Overview.
type ExecutiveKPI struct {
	Key            string      `json:"key"`
	Label          string      `json:"label"`
	Unit           string      `json:"unit" enum:"count,idr,ratio,hours,balls,points,days"`
	Kind           string      `json:"kind" enum:"flow,rate,stock,current"`
	Direction      string      `json:"direction" enum:"up,down" doc:"up = higher is better"`
	Definition     string      `json:"definition"`
	Status         string      `json:"status" enum:"available,coming_soon,not_refreshed"`
	Value          *string     `json:"value"`
	Target         *string     `json:"target" doc:"Target of the period (approved KPI target plan)"`
	TargetToDate   *string     `json:"targetToDate" doc:"Target pro-rated to the days elapsed for flow KPIs of the running month"`
	Achievement    *string     `json:"achievement" doc:"Value ÷ target to date (target ÷ value when lower is better)"`
	Variance       *string     `json:"variance" doc:"Value − target to date"`
	Indicator      string      `json:"indicator" enum:"on_track,watch,off_track,no_target"`
	Previous       *string     `json:"previous" doc:"Previous month (or previous year)"`
	PreviousChange *string     `json:"previousChange" doc:"MoM (or YoY for the year) change ratio"`
	LastYear       *string     `json:"lastYear" doc:"Same month last year"`
	LastYearChange *string     `json:"lastYearChange" doc:"YoY change ratio"`
	YTD            *string     `json:"ytd" doc:"Year to date (flow KPIs)"`
	YTDTarget      *string     `json:"ytdTarget"`
	Breakdown      []Breakdown `json:"breakdown,omitempty"`
	Dashboard      string      `json:"dashboard" doc:"Source dashboard code"`
	SourceKPI      string      `json:"sourceKpi" doc:"KPI key in the source dashboard"`
	Report         string      `json:"report,omitempty" doc:"Source report code"`
	DrillBy        []string    `json:"drillBy" doc:"Dimensions of the drill-down"`
	RefreshedAt    *time.Time  `json:"refreshedAt"`
}

// ExecutiveDomain groups the KPIs of one domain.
type ExecutiveDomain struct {
	Code          string         `json:"code"`
	Label         string         `json:"label"`
	Module        string         `json:"module"`
	DashboardPath string         `json:"dashboardPath"`
	KPIs          []ExecutiveKPI `json:"kpis"`
}

// ExecutiveTargetPlanRef is the target plan in force.
type ExecutiveTargetPlanRef struct {
	ID      uuid.UUID `json:"id"`
	Year    int       `json:"year"`
	Version int       `json:"version"`
	Title   string    `json:"title"`
}

// ExecutiveOverview is the cross-domain Executive Overview (FR-BI-02).
type ExecutiveOverview struct {
	PropertyID  uuid.UUID               `json:"propertyId"`
	Period      string                  `json:"period" enum:"month,year"`
	Month       string                  `json:"month" doc:"YYYY-MM"`
	From        string                  `json:"from"`
	To          string                  `json:"to"`
	DataAsOf    *time.Time              `json:"dataAsOf" doc:"Oldest refresh of the values shown (analytics store)"`
	Stale       bool                    `json:"stale"`
	TargetPlan  *ExecutiveTargetPlanRef `json:"targetPlan"`
	Domains     []ExecutiveDomain       `json:"domains"`
	GeneratedAt time.Time               `json:"generatedAt"`
}

type monthKey struct {
	key   string
	month string // YYYY-MM-01
}

type storedValue struct {
	value       decimal.Decimal
	refreshedAt time.Time
}

func ds(d decimal.Decimal) *string {
	s := d.String()
	return &s
}

func ratio(a, b decimal.Decimal) *string {
	if b.IsZero() {
		return nil
	}
	return ds(a.Sub(b).Div(b.Abs()).Round(4))
}

// indicatorFor evaluates a value against its target (BI Policies).
func indicatorFor(value, target decimal.Decimal, direction string, pol BIPolicy) (string, *string) {
	onTrack, _ := decimal.NewFromString(pol.OnTrackPercent)
	watch, _ := decimal.NewFromString(pol.WatchPercent)
	hundred := decimal.NewFromInt(100)
	var ach decimal.Decimal
	switch {
	case direction == "down":
		if value.IsZero() || value.IsNegative() {
			ach = decimal.NewFromInt(1)
			if target.IsNegative() {
				ach = decimal.Zero
			}
		} else {
			ach = target.Div(value)
		}
	case target.IsZero():
		if value.IsPositive() || value.IsZero() {
			ach = decimal.NewFromInt(1)
		}
	default:
		ach = value.Div(target)
		if target.IsNegative() {
			// a planned loss: beating it means a smaller loss
			ach = decimal.NewFromInt(2).Sub(ach)
		}
	}
	ach = ach.Round(4)
	pct := ach.Mul(hundred)
	switch {
	case pct.GreaterThanOrEqual(onTrack):
		return "on_track", ds(ach)
	case pct.GreaterThanOrEqual(watch):
		return "watch", ds(ach)
	default:
		return "off_track", ds(ach)
	}
}

// periodRange resolves ?period=month|year&month=YYYY-MM.
func periodRange(r *http.Request, today time.Time) (period string, m, from, to time.Time, err error) {
	period = r.URL.Query().Get("period")
	if period == "" {
		period = "month"
	}
	if period != "month" && period != "year" {
		return period, m, from, to, handle.Invalid("period", "invalid", "period must be month or year")
	}
	m = monthStart(today)
	if v := r.URL.Query().Get("month"); v != "" {
		t, perr := time.Parse("2006-01", v)
		if perr != nil {
			return period, m, from, to, handle.Invalid("month", "invalid", "month must be YYYY-MM")
		}
		if t.After(m) {
			return period, m, from, to, handle.Invalid("month", "future", "month must not be in the future")
		}
		m = t
	}
	if period == "year" {
		from = time.Date(m.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		to = minDate(time.Date(m.Year(), 12, 31, 0, 0, 0, 0, time.UTC), today)
		if m.Year() == today.Year() {
			m = monthStart(today)
		} else {
			m = time.Date(m.Year(), 12, 1, 0, 0, 0, 0, time.UTC)
		}
		return period, m, from, to, nil
	}
	return period, m, m, minDate(monthEnd(m), today), nil
}

func (b *BI) visible(ctx context.Context, k execKPI, property uuid.UUID) bool {
	if k.Permission != "" && !authz.From(ctx).Can(k.Permission, &property) {
		return false
	}
	return k.Module == "" || b.moduleEnabled(ctx, k.Module)
}

// loadTargets returns the approved targets of a year (kpi → month → value).
func loadTargets(ctx context.Context, q pgx.Tx, property uuid.UUID, year int) (map[string]map[int]decimal.Decimal, *ExecutiveTargetPlanRef, error) {
	out := map[string]map[int]decimal.Decimal{}
	var ref ExecutiveTargetPlanRef
	err := q.QueryRow(ctx, `SELECT id, year, version, title FROM reporting.kpi_target_plans WHERE property_id = $1 AND year = $2 AND status = 'approved'`,
		property, year).Scan(&ref.ID, &ref.Year, &ref.Version, &ref.Title)
	if err != nil {
		if err == pgx.ErrNoRows {
			return out, nil, nil
		}
		return nil, nil, err
	}
	rows, err := q.Query(ctx, `SELECT kpi_key, month, target::text FROM reporting.kpi_targets WHERE plan_id = $1`, ref.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		var mo int
		if err := rows.Scan(&k, &mo, &v); err != nil {
			return nil, nil, err
		}
		if out[k] == nil {
			out[k] = map[int]decimal.Decimal{}
		}
		out[k][mo], _ = decimal.NewFromString(v)
	}
	return out, &ref, rows.Err()
}

// loadValues reads the month and year values of the analytics store.
func loadValues(ctx context.Context, q pgx.Tx, property uuid.UUID, fromMonth, toMonth time.Time) (map[monthKey]storedValue, map[string]storedValue, error) {
	months := map[monthKey]storedValue{}
	years := map[string]storedValue{}
	rows, err := q.Query(ctx, `SELECT kpi_key, grain, period_start::text, value::text, refreshed_at FROM analytics.kpi_values
		WHERE property_id = $1 AND ((grain = 'month' AND period_start BETWEEN $2::date AND $3::date)
		  OR (grain = 'year' AND period_start BETWEEN date_trunc('year', $2::date)::date AND $3::date))`,
		property, fromMonth.Format(dateFmt), toMonth.Format(dateFmt))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, g, start, v string
		var at time.Time
		if err := rows.Scan(&k, &g, &start, &v, &at); err != nil {
			return nil, nil, err
		}
		d, _ := decimal.NewFromString(v)
		if g == "month" {
			months[monthKey{k, start}] = storedValue{d, at}
		} else {
			years[k+"|"+start[:4]] = storedValue{d, at}
		}
	}
	return months, years, rows.Err()
}

func (b *BI) executive(ctx context.Context, tx pgx.Tx, r *http.Request) (ExecutiveOverview, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	if err := queryBudget(ctx, tx, pol.QueryTimeoutSeconds); err != nil {
		return ExecutiveOverview{}, err
	}
	loc := calendar.Location(ctx, tx)
	today := day(b.now().In(loc))
	period, m, from, to, err := periodRange(r, today)
	if err != nil {
		return ExecutiveOverview{}, err
	}
	out := ExecutiveOverview{PropertyID: property, Period: period, Month: m.Format("2006-01"), From: from.Format(dateFmt), To: to.Format(dateFmt),
		Domains: []ExecutiveDomain{}, GeneratedAt: time.Now().UTC()}
	months, years, err := loadValues(ctx, tx, property, time.Date(m.Year()-1, 1, 1, 0, 0, 0, 0, time.UTC), m)
	if err != nil {
		return out, budgetError(err)
	}
	targets, plan, err := loadTargets(ctx, tx, property, m.Year())
	if err != nil {
		return out, err
	}
	out.TargetPlan = plan
	monthStr := func(t time.Time) string { return t.Format(dateFmt) }
	daysIn := decimal.NewFromInt(int64(monthEnd(m).Day()))
	elapsed := decimal.NewFromInt(int64(to.Day()))
	running := m.Year() == today.Year() && m.Month() == today.Month()
	var oldest *time.Time
	byDomain := map[string][]ExecutiveKPI{}
	for _, k := range executiveKPIs() {
		if !b.visible(ctx, k, property) {
			continue
		}
		e := ExecutiveKPI{Key: k.Key, Label: k.Label, Unit: k.Unit, Kind: k.Kind, Direction: k.Direction, Definition: k.Definition,
			Status: "available", Indicator: "no_target", Dashboard: k.Dashboard, SourceKPI: k.Source, Report: k.Report, DrillBy: []string{"day"}}
		if k.Revenue != "" {
			e.DrillBy = append(e.DrillBy, "weekday", "daypart", "revenue_component", "business_line", "outlet", "segment", "lines")
		}
		if k.Placeholder {
			e.Status, e.DrillBy = "coming_soon", []string{}
			byDomain[k.Domain] = append(byDomain[k.Domain], e)
			continue
		}
		tg := targets[k.Key]
		monthTarget := func(mo int) (decimal.Decimal, bool) {
			v, ok := tg[mo]
			return v, ok
		}
		var cur storedValue
		var ok bool
		if period == "month" {
			cur, ok = months[monthKey{k.Key, monthStr(m)}]
			if p, okp := months[monthKey{k.Key, monthStr(m.AddDate(0, -1, 0))}]; okp {
				e.Previous = ds(p.value)
				if ok {
					e.PreviousChange = ratio(cur.value, p.value)
				}
			}
			if p, okp := months[monthKey{k.Key, monthStr(m.AddDate(-1, 0, 0))}]; okp {
				e.LastYear = ds(p.value)
				if ok {
					e.LastYearChange = ratio(cur.value, p.value)
				}
			}
			if t, okt := monthTarget(int(m.Month())); okt {
				e.Target = ds(t)
				ttd := t
				if k.Kind == KindFlow && running && to.Before(monthEnd(m)) {
					ttd = t.Mul(elapsed).Div(daysIn).Round(2)
				}
				e.TargetToDate = ds(ttd)
			}
			if k.Kind == KindFlow {
				ytd, ytdT, any, anyT := decimal.Zero, decimal.Zero, false, false
				for mo := 1; mo <= int(m.Month()); mo++ {
					mm := time.Date(m.Year(), time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
					if v, okv := months[monthKey{k.Key, monthStr(mm)}]; okv {
						ytd, any = ytd.Add(v.value), true
					}
					if t, okt := monthTarget(mo); okt {
						if mo == int(m.Month()) && e.TargetToDate != nil {
							t, _ = decimal.NewFromString(*e.TargetToDate)
						}
						ytdT, anyT = ytdT.Add(t), true
					}
				}
				if any {
					e.YTD = ds(ytd)
				}
				if anyT {
					e.YTDTarget = ds(ytdT)
				}
			}
		} else {
			y := strconv.Itoa(m.Year())
			cur, ok = years[k.Key+"|"+y]
			if p, okp := years[k.Key+"|"+strconv.Itoa(m.Year()-1)]; okp {
				e.Previous, e.LastYear = ds(p.value), ds(p.value)
				if ok {
					e.PreviousChange = ratio(cur.value, p.value)
					e.LastYearChange = e.PreviousChange
				}
			}
			switch k.Kind {
			case KindFlow:
				full, ttd, anyT := decimal.Zero, decimal.Zero, false
				for mo := 1; mo <= 12; mo++ {
					t, okt := monthTarget(mo)
					if !okt {
						continue
					}
					anyT = true
					full = full.Add(t)
					switch {
					case mo < int(m.Month()):
						ttd = ttd.Add(t)
					case mo == int(m.Month()):
						if running {
							ttd = ttd.Add(t.Mul(elapsed).Div(daysIn).Round(2))
						} else {
							ttd = ttd.Add(t)
						}
					}
				}
				if anyT {
					e.Target, e.TargetToDate = ds(full), ds(ttd)
				}
				if ok {
					e.YTD = ds(cur.value)
					if anyT {
						e.YTDTarget = ds(ttd)
					}
				}
			case KindRate:
				sum, n := decimal.Zero, 0
				for mo := 1; mo <= int(m.Month()); mo++ {
					if t, okt := monthTarget(mo); okt {
						sum, n = sum.Add(t), n+1
					}
				}
				if n > 0 {
					avg := sum.Div(decimal.NewFromInt(int64(n))).Round(4)
					e.Target, e.TargetToDate = ds(avg), ds(avg)
				}
			default:
				if t, okt := monthTarget(int(m.Month())); okt {
					e.Target, e.TargetToDate = ds(t), ds(t)
				}
			}
		}
		if !ok {
			e.Status = "not_refreshed"
		} else {
			e.Value = ds(cur.value)
			at := cur.refreshedAt
			e.RefreshedAt = &at
			if oldest == nil || at.Before(*oldest) {
				oldest = &at
			}
			if e.TargetToDate != nil {
				t, _ := decimal.NewFromString(*e.TargetToDate)
				e.Indicator, e.Achievement = indicatorFor(cur.value, t, k.Direction, pol)
				e.Variance = ds(cur.value.Sub(t))
			}
		}
		if k.Revenue != "" && ok {
			bd, err := revenueBreakdown(ctx, tx, property, k.Revenue, from, to)
			if err != nil {
				return out, budgetError(err)
			}
			e.Breakdown = bd
		}
		byDomain[k.Domain] = append(byDomain[k.Domain], e)
	}
	for _, d := range ExecutiveDomains {
		if d.Module != "" && !b.moduleEnabled(ctx, d.Module) {
			continue
		}
		if len(byDomain[d.Code]) == 0 {
			continue
		}
		out.Domains = append(out.Domains, ExecutiveDomain{Code: d.Code, Label: d.Label, Module: d.Module, DashboardPath: d.Path, KPIs: byDomain[d.Code]})
	}
	out.DataAsOf = oldest
	if running || period == "year" && m.Year() == today.Year() {
		out.Stale = oldest == nil || b.now().Sub(*oldest) > time.Duration(pol.StaleAfterMinutes)*time.Minute
	}
	return out, nil
}

// revenueBreakdown splits a revenue KPI by business line (all lines) or by
// revenue component (one line) from the analytics store.
func revenueBreakdown(ctx context.Context, tx pgx.Tx, property uuid.UUID, line string, from, to time.Time) ([]Breakdown, error) {
	dim := "revenue_component"
	if line == "*" {
		dim = "business_line"
	}
	rows, err := tx.Query(ctx, `SELECT `+dim+` AS label, trim_scale(sum(amount))::text AS value FROM analytics.revenue_daily
		WHERE property_id = $1 AND day BETWEEN $2::date AND $3::date AND ($4 = '*' OR business_line = $4) GROUP BY 1 ORDER BY sum(amount) DESC, 1`,
		property, from.Format(dateFmt), to.Format(dateFmt), line)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Breakdown])
}

// ── per property comparison (FR-BI-08) ───────────────────────────────────

// ExecutivePropertyRef is a property of the comparison.
type ExecutivePropertyRef struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Code string    `json:"code" db:"code"`
	Name string    `json:"name" db:"name"`
}

// ExecutivePropertyValue is a KPI value at one property.
type ExecutivePropertyValue struct {
	PropertyID uuid.UUID `json:"propertyId"`
	Value      *string   `json:"value"`
	Target     *string   `json:"target"`
}

// ExecutivePropertyKPI is one KPI across properties.
type ExecutivePropertyKPI struct {
	Key    string                   `json:"key"`
	Label  string                   `json:"label"`
	Domain string                   `json:"domain"`
	Unit   string                   `json:"unit"`
	Values []ExecutivePropertyValue `json:"values"`
}

// ExecutivePropertyComparison compares the month values of the properties
// the user manages.
type ExecutivePropertyComparison struct {
	Month      string                 `json:"month"`
	Properties []ExecutivePropertyRef `json:"properties"`
	KPIs       []ExecutivePropertyKPI `json:"kpis"`
}

func (b *BI) byProperty(ctx context.Context, tx pgx.Tx, r *http.Request) (ExecutivePropertyComparison, error) {
	loc := calendar.Location(ctx, tx)
	today := day(b.now().In(loc))
	m := monthStart(today)
	if v := r.URL.Query().Get("month"); v != "" {
		t, err := time.Parse("2006-01", v)
		if err != nil {
			return ExecutivePropertyComparison{}, handle.Invalid("month", "invalid", "month must be YYYY-MM")
		}
		m = t
	}
	out := ExecutivePropertyComparison{Month: m.Format("2006-01"), Properties: []ExecutivePropertyRef{}, KPIs: []ExecutivePropertyKPI{}}
	props, err := handle.List[ExecutivePropertyRef](tx.Query(ctx, `SELECT id, code, name FROM platform.properties WHERE status = 'active'
		AND archived_at IS NULL AND platform.rls_allowed(id) ORDER BY code`))
	if err != nil {
		return out, err
	}
	out.Properties = props
	type pk struct {
		prop uuid.UUID
		key  string
	}
	vals := map[pk]string{}
	tgts := map[pk]string{}
	rows, err := tx.Query(ctx, `SELECT property_id, kpi_key, value::text FROM analytics.kpi_values WHERE grain = 'month' AND period_start = $1::date`,
		m.Format(dateFmt))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p uuid.UUID
		var k, v string
		if err := rows.Scan(&p, &k, &v); err != nil {
			rows.Close()
			return out, err
		}
		vals[pk{p, k}] = v
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT t.property_id, t.kpi_key, t.target::text FROM reporting.kpi_targets t JOIN reporting.kpi_target_plans p ON p.id = t.plan_id
		WHERE p.status = 'approved' AND p.year = $1 AND t.month = $2`, m.Year(), int(m.Month()))
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var p uuid.UUID
		var k, v string
		if err := rows.Scan(&p, &k, &v); err != nil {
			rows.Close()
			return out, err
		}
		tgts[pk{p, k}] = v
	}
	rows.Close()
	for _, k := range executiveKPIs() {
		if k.Placeholder || k.Module != "" && !b.moduleEnabled(ctx, k.Module) {
			continue
		}
		pk2 := ExecutivePropertyKPI{Key: k.Key, Label: k.Label, Domain: k.Domain, Unit: k.Unit, Values: []ExecutivePropertyValue{}}
		for _, p := range props {
			if k.Permission != "" && !authz.From(ctx).Can(k.Permission, &p.ID) {
				continue
			}
			v := ExecutivePropertyValue{PropertyID: p.ID}
			if x, ok := vals[pk{p.ID, k.Key}]; ok {
				v.Value = &x
			}
			if x, ok := tgts[pk{p.ID, k.Key}]; ok {
				v.Target = &x
			}
			pk2.Values = append(pk2.Values, v)
		}
		out.KPIs = append(out.KPIs, pk2)
	}
	return out, nil
}

// ── trend (MoM / YoY chart) ───────────────────────────────────────────────

// ExecutiveTrendPoint is one month of a KPI.
type ExecutiveTrendPoint struct {
	Month    string  `json:"month"`
	Value    *string `json:"value"`
	Target   *string `json:"target"`
	LastYear *string `json:"lastYear"`
}

// ExecutiveTrend is the monthly series of a KPI with its targets.
type ExecutiveTrend struct {
	Key    string                `json:"key"`
	Label  string                `json:"label"`
	Unit   string                `json:"unit"`
	Points []ExecutiveTrendPoint `json:"points"`
}

func (b *BI) trend(ctx context.Context, tx pgx.Tx, r *http.Request) (ExecutiveTrend, error) {
	property := handle.Property(ctx)
	k, ok := executiveKPI(r.URL.Query().Get("kpi"))
	if !ok || !b.visible(ctx, k, property) {
		return ExecutiveTrend{}, errs.NotFound("kpi")
	}
	n := 13
	if v, err := strconv.Atoi(r.URL.Query().Get("months")); err == nil && v > 0 && v <= 36 {
		n = v
	}
	today := day(b.now().In(calendar.Location(ctx, tx)))
	last := monthStart(today)
	first := last.AddDate(0, -(n - 1), 0)
	months, _, err := loadValues(ctx, tx, property, first.AddDate(-1, 0, 0), last)
	if err != nil {
		return ExecutiveTrend{}, err
	}
	out := ExecutiveTrend{Key: k.Key, Label: k.Label, Unit: k.Unit, Points: []ExecutiveTrendPoint{}}
	targets := map[int]map[string]map[int]decimal.Decimal{}
	for mm := first; !mm.After(last); mm = mm.AddDate(0, 1, 0) {
		if _, ok := targets[mm.Year()]; !ok {
			t, _, err := loadTargets(ctx, tx, property, mm.Year())
			if err != nil {
				return out, err
			}
			targets[mm.Year()] = t
		}
		p := ExecutiveTrendPoint{Month: mm.Format("2006-01")}
		if v, ok := months[monthKey{k.Key, mm.Format(dateFmt)}]; ok {
			p.Value = ds(v.value)
		}
		if v, ok := months[monthKey{k.Key, mm.AddDate(-1, 0, 0).Format(dateFmt)}]; ok {
			p.LastYear = ds(v.value)
		}
		if t, ok := targets[mm.Year()][k.Key][int(mm.Month())]; ok {
			p.Target = ds(t)
		}
		out.Points = append(out.Points, p)
	}
	return out, nil
}

// ── drill-down (FR-BI-04) ─────────────────────────────────────────────────

// ExecutiveDrillRow is one member of the drilled dimension.
type ExecutiveDrillRow struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value string  `json:"value"`
	Share *string `json:"share"`
	Lines *int    `json:"lines,omitempty"`
}

// ExecutiveDrillLine is a source transaction (folio line).
type ExecutiveDrillLine struct {
	LineID           uuid.UUID `json:"lineId" db:"line_id"`
	PostedAt         time.Time `json:"postedAt" db:"posted_at"`
	Day              string    `json:"day" db:"day"`
	FolioID          uuid.UUID `json:"folioId" db:"folio_id"`
	FolioNumber      string    `json:"folioNumber" db:"folio_number"`
	HolderName       string    `json:"holderName" db:"holder_name"`
	Description      string    `json:"description" db:"description"`
	BusinessLine     string    `json:"businessLine" db:"business_line"`
	RevenueComponent string    `json:"revenueComponent" db:"revenue_component"`
	Outlet           string    `json:"outlet" db:"outlet"`
	Segment          string    `json:"segment" db:"segment"`
	Daypart          string    `json:"daypart" db:"daypart"`
	Source           string    `json:"source" db:"source"`
	Quantity         string    `json:"quantity" db:"quantity"`
	Total            string    `json:"total" db:"total"`
}

// ExecutiveLink leads to the domain dashboard or the source report.
type ExecutiveLink struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ExecutiveDrilldown is one step of the drill-down of a KPI.
type ExecutiveDrilldown struct {
	KPI        string               `json:"kpi"`
	Label      string               `json:"label"`
	Unit       string               `json:"unit"`
	Kind       string               `json:"kind"`
	From       string               `json:"from"`
	To         string               `json:"to"`
	By         string               `json:"by" enum:"day,weekday,daypart,business_line,revenue_component,outlet,segment,lines"`
	Filters    map[string]string    `json:"filters"`
	Total      *string              `json:"total" doc:"KPI value of the period and filters"`
	Rows       []ExecutiveDrillRow  `json:"rows"`
	Lines      []ExecutiveDrillLine `json:"lines"`
	Truncated  bool                 `json:"truncated"`
	Dimensions []string             `json:"dimensions" doc:"Dimensions available for the next step"`
	Links      []ExecutiveLink      `json:"links"`
}

// drillDim is a drill dimension of the revenue facts: its expression over
// analytics.revenue_daily and over the source lines ($5 = time zone).
type drillDim struct{ fact, line string }

// daypartExpr buckets the posting time of a line (morning < 12:00 ≤
// afternoon < 17:00 ≤ evening), the "weekday PM" of the §9.5 review.
const daypartExpr = `CASE WHEN extract(hour FROM posted_at AT TIME ZONE $5) < 12 THEN 'morning'
	WHEN extract(hour FROM posted_at AT TIME ZONE $5) < 17 THEN 'afternoon' ELSE 'evening' END`

var drillDims = map[string]drillDim{
	"day":               {"day::text", "((posted_at AT TIME ZONE $5)::date)::text"},
	"weekday":           {"extract(isodow FROM day)::int::text", "extract(isodow FROM (posted_at AT TIME ZONE $5)::date)::int::text"},
	"daypart":           {"daypart", daypartExpr},
	"business_line":     {"business_line", "business_line"},
	"revenue_component": {"revenue_component", "revenue_component"},
	"outlet":            {"outlet", "outlet"},
	"segment":           {"segment", "segment"},
}

var revenueDims = []string{"day", "weekday", "daypart", "business_line", "revenue_component", "outlet", "segment"}

var weekdays = []string{"", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func validDrillFilter(d, v string) bool {
	switch d {
	case "day":
		_, err := time.Parse(dateFmt, v)
		return err == nil
	case "weekday":
		n, err := strconv.Atoi(v)
		return err == nil && n >= 1 && n <= 7
	case "daypart":
		return v == "morning" || v == "afternoon" || v == "evening"
	}
	return true
}

func (b *BI) drilldown(ctx context.Context, tx pgx.Tx, r *http.Request) (ExecutiveDrilldown, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	if err := queryBudget(ctx, tx, pol.QueryTimeoutSeconds); err != nil {
		return ExecutiveDrilldown{}, err
	}
	q := r.URL.Query()
	k, ok := executiveKPI(q.Get("kpi"))
	if !ok || k.Placeholder || !b.visible(ctx, k, property) {
		return ExecutiveDrilldown{}, errs.NotFound("kpi")
	}
	today := day(b.now().In(calendar.Location(ctx, tx)))
	from, to := monthStart(today), today
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(dateFmt, v)
		if err != nil {
			return ExecutiveDrilldown{}, handle.Invalid("from", "invalid", "from must be YYYY-MM-DD")
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(dateFmt, v)
		if err != nil {
			return ExecutiveDrilldown{}, handle.Invalid("to", "invalid", "to must be YYYY-MM-DD")
		}
		to = t
	}
	if to.Before(from) {
		return ExecutiveDrilldown{}, handle.Invalid("to", "invalid", "to must be on or after from")
	}
	by := q.Get("by")
	if by == "" {
		by = "day"
	}
	out := ExecutiveDrilldown{KPI: k.Key, Label: k.Label, Unit: k.Unit, Kind: k.Kind, From: from.Format(dateFmt), To: to.Format(dateFmt), By: by,
		Filters: map[string]string{}, Rows: []ExecutiveDrillRow{}, Lines: []ExecutiveDrillLine{}, Dimensions: []string{}, Links: []ExecutiveLink{}}
	for _, d := range revenueDims {
		if v := q.Get("filter[" + d + "]"); v != "" {
			if k.Revenue == "" && d != "day" {
				return out, handle.Invalid("filter["+d+"]", "invalid", k.Label+" drills by day only")
			}
			if !validDrillFilter(d, v) {
				return out, handle.Invalid("filter["+d+"]", "invalid", "invalid "+d+" (day YYYY-MM-DD, weekday 1–7, daypart morning / afternoon / evening)")
			}
			out.Filters[d] = v
		}
	}
	dash := ""
	for _, d := range ExecutiveDomains {
		if d.Code == k.Domain {
			dash = d.Path
		}
	}
	rng := "?from=" + out.From + "&to=" + out.To
	if dash != "" {
		out.Links = append(out.Links, ExecutiveLink{Label: "Open the domain dashboard", Path: dash + rng})
	}
	if k.Report != "" {
		out.Links = append(out.Links, ExecutiveLink{Label: "Open the source report", Path: "/reports/" + k.Report + rng})
	}
	if k.Revenue == "" {
		if by != "day" {
			return out, handle.Invalid("by", "invalid", k.Label+" drills by day only; open the source report for the transactions")
		}
		return out, b.drillStored(ctx, tx, property, k, from, to, &out)
	}
	if by != "lines" && !slices.Contains(revenueDims, by) {
		return out, handle.Invalid("by", "invalid", "by must be one of "+strings.Join(append(revenueDims, "lines"), ", "))
	}
	// revenue facts: the KPI line and the filters
	where := []string{"property_id = $1", "day BETWEEN $2::date AND $3::date", "($4 = '*' OR business_line = $4)"}
	args := []any{property, out.From, out.To, k.Revenue}
	for _, d := range revenueDims {
		if v, ok := out.Filters[d]; ok {
			args = append(args, v)
			where = append(where, drillDims[d].fact+" = $"+strconv.Itoa(len(args)))
		}
	}
	var total *string
	if err := tx.QueryRow(ctx, `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM analytics.revenue_daily WHERE `+strings.Join(where, " AND "), args...).
		Scan(&total); err != nil {
		return out, budgetError(err)
	}
	out.Total = total
	tot, _ := decimal.NewFromString(*total)
	for _, d := range revenueDims {
		if _, ok := out.Filters[d]; !ok && d != by {
			out.Dimensions = append(out.Dimensions, d)
		}
	}
	out.Dimensions = append(out.Dimensions, "lines")
	if by == "lines" {
		lw := []string{"NOT liability", "($4 = '*' OR business_line = $4)",
			"posted_at >= ($2::date::timestamp AT TIME ZONE $5)", "posted_at < (($3::date + 1)::timestamp AT TIME ZONE $5)"}
		largs := []any{property, out.From, out.To, k.Revenue, calendar.Location(ctx, tx).String()}
		lw = append(lw, "property_id = $1")
		for _, d := range revenueDims {
			if v, ok := out.Filters[d]; ok {
				largs = append(largs, v)
				lw = append(lw, "("+drillDims[d].line+") = $"+strconv.Itoa(len(largs)))
			}
		}
		lines, err := handle.List[ExecutiveDrillLine](tx.Query(ctx, `SELECT line_id, posted_at, ((posted_at AT TIME ZONE $5)::date)::text AS day, folio_id, folio_number,
			holder_name, description, business_line, revenue_component, outlet, segment, `+daypartExpr+` AS daypart, source_type || coalesce(' ' || source_ref, '') AS source,
			trim_scale(quantity)::text AS quantity, trim_scale(total)::text AS total FROM reporting.bi_revenue_lines WHERE `+strings.Join(lw, " AND ")+
			` ORDER BY posted_at, folio_number LIMIT `+strconv.Itoa(pol.MaxDrilldownRows+1), largs...))
		if err != nil {
			return out, budgetError(err)
		}
		if len(lines) > pol.MaxDrilldownRows {
			lines, out.Truncated = lines[:pol.MaxDrilldownRows], true
		}
		out.Lines = lines
		return out, nil
	}
	col := drillDims[by].fact
	rows, err := tx.Query(ctx, `SELECT `+col+` AS k, trim_scale(sum(amount))::text, sum(lines)::int FROM analytics.revenue_daily WHERE `+strings.Join(where, " AND ")+
		` GROUP BY 1 ORDER BY 1`, args...)
	if err != nil {
		return out, budgetError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, v string
		var n int
		if err := rows.Scan(&key, &v, &n); err != nil {
			return out, err
		}
		row := ExecutiveDrillRow{Key: key, Label: drillLabel(by, key), Value: v, Lines: &n}
		if d, err := decimal.NewFromString(v); err == nil && !tot.IsZero() {
			row.Share = ds(d.Div(tot).Round(4))
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

func drillLabel(dim, s string) string {
	if dim == "weekday" {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 7 {
			return weekdays[n]
		}
	}
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "_", " ")
}

// drillStored drills a non-revenue KPI by day from the analytics store; the
// total is the KPI value of the period (the month / year value when the
// period is one, the sum of the days for flow KPIs).
func (b *BI) drillStored(ctx context.Context, tx pgx.Tx, property uuid.UUID, k execKPI, from, to time.Time, out *ExecutiveDrilldown) error {
	rows, err := tx.Query(ctx, `SELECT period_start::text, trim_scale(value)::text FROM analytics.kpi_values WHERE property_id = $1 AND kpi_key = $2
		AND grain = 'day' AND period_start BETWEEN $3::date AND $4::date ORDER BY period_start`, property, k.Key, out.From, out.To)
	if err != nil {
		return budgetError(err)
	}
	sum := decimal.Zero
	for rows.Next() {
		var d, v string
		if err := rows.Scan(&d, &v); err != nil {
			rows.Close()
			return err
		}
		x, _ := decimal.NewFromString(v)
		sum = sum.Add(x)
		out.Rows = append(out.Rows, ExecutiveDrillRow{Key: d, Label: d, Value: v})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var total *string
	err = tx.QueryRow(ctx, `SELECT trim_scale(value)::text FROM analytics.kpi_values WHERE property_id = $1 AND kpi_key = $2 AND grain = 'month'
		AND period_start = $3::date AND period_end = $4::date`, property, k.Key, out.From, out.To).Scan(&total)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if total == nil && k.Kind == KindFlow {
		total = ds(sum)
	}
	out.Total = total
	if k.Kind == KindFlow && total != nil {
		tot, _ := decimal.NewFromString(*total)
		for i := range out.Rows {
			if v, err := decimal.NewFromString(out.Rows[i].Value); err == nil && !tot.IsZero() {
				out.Rows[i].Share = ds(v.Div(tot).Round(4))
			}
		}
	}
	return nil
}

// ── HR Performance (EP-27, FR-RPT-P5-01) ──────────────────────────────────

// HRPerformanceKPI is one KPI of HR Performance.
type HRPerformanceKPI struct {
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Unit       string      `json:"unit" enum:"count,idr,ratio,hours,balls,points,days"`
	Kind       string      `json:"kind" enum:"flow,rate,stock,current"`
	Direction  string      `json:"direction" enum:"up,down"`
	Definition string      `json:"definition"`
	Status     string      `json:"status" enum:"available,coming_soon"`
	Value      *string     `json:"value"`
	Breakdown  []Breakdown `json:"breakdown,omitempty"`
	Report     string      `json:"report,omitempty"`
}

// HRPerformanceDashboard is the HR Performance dashboard (NC §23).
type HRPerformanceDashboard struct {
	Code        string             `json:"code"`
	Name        string             `json:"name"`
	From        string             `json:"from"`
	To          string             `json:"to"`
	KPIs        []HRPerformanceKPI `json:"kpis"`
	GeneratedAt time.Time          `json:"generatedAt"`
}

func (b *BI) hrPerformance(ctx context.Context, tx pgx.Tx, r *http.Request) (HRPerformanceDashboard, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	if err := queryBudget(ctx, tx, pol.QueryTimeoutSeconds); err != nil {
		return HRPerformanceDashboard{}, err
	}
	from, to, tz := period(ctx, tx, map[string]string{"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to")}, 30)
	out := HRPerformanceDashboard{Code: "hr-performance", Name: "HR Performance", From: from, To: to, KPIs: []HRPerformanceKPI{}, GeneratedAt: time.Now().UTC()}
	p := authz.From(ctx)
	for _, h := range hrKPIs() {
		if h.Permission != "" && !p.Can(h.Permission, &property) {
			continue
		}
		v := HRPerformanceKPI{Key: h.Key, Label: h.Label, Unit: h.Unit, Kind: h.Kind, Direction: h.Direction, Definition: h.Definition, Status: "available",
			Report: h.Report}
		if h.SQL == "" || h.Module != "" && !b.moduleEnabled(ctx, h.Module) {
			v.Status = "coming_soon"
			out.KPIs = append(out.KPIs, v)
			continue
		}
		res, err := kpi(ctx, tx, kpiDef{key: h.Key, label: h.Label, unit: h.Unit, def: h.Definition, sql: h.SQL, breakdown: h.Breakdown}, from, to, tz)
		if err != nil {
			return out, budgetError(err)
		}
		v.Value, v.Breakdown = &res.Value, res.Breakdown
		out.KPIs = append(out.KPIs, v)
	}
	return out, nil
}

// ── KPI definitions (FR-BI-07) ────────────────────────────────────────────

// KPIDefinition documents one KPI: label (NC §23), unit, kind and the
// dashboard that owns its single definition.
type KPIDefinition struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Dashboard     string `json:"dashboard"`
	DashboardName string `json:"dashboardName"`
	Domain        string `json:"domain,omitempty"`
	Unit          string `json:"unit"`
	Kind          string `json:"kind,omitempty"`
	Direction     string `json:"direction,omitempty"`
	Definition    string `json:"definition"`
	Executive     bool   `json:"executive"`
	ExecutiveKey  string `json:"executiveKey,omitempty"`
	Report        string `json:"report,omitempty"`
	Status        string `json:"status" enum:"available,coming_soon"`
}

func (b *BI) definitions(w http.ResponseWriter, r *http.Request) {
	exec := map[string]execKPI{}
	for _, k := range executiveKPIs() {
		exec[k.Dashboard+"/"+k.Source] = k
	}
	out := []KPIDefinition{}
	codes := append([]string{}, DashboardCodes...)
	for c := range dashboards {
		if !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	for _, c := range codes {
		d := dashboards[c]
		for _, k := range d.kpis {
			def := KPIDefinition{Key: k.key, Label: k.label, Dashboard: c, DashboardName: d.name, Unit: k.unit, Definition: k.def, Status: "available"}
			if e, ok := exec[c+"/"+k.key]; ok {
				def.Executive, def.ExecutiveKey, def.Domain, def.Kind, def.Direction, def.Report = true, e.Key, e.Domain, e.Kind, e.Direction, e.Report
			}
			if def.Definition == "" {
				def.Definition = k.label + " of the period"
			}
			out = append(out, def)
		}
	}
	for _, h := range hrKPIs() {
		def := KPIDefinition{Key: h.Key, Label: h.Label, Dashboard: "hr-performance", DashboardName: "HR Performance", Domain: "hr", Unit: h.Unit, Kind: h.Kind,
			Direction: h.Direction, Definition: h.Definition, Executive: h.Executive, Report: h.Report, Status: "available"}
		if h.Executive {
			def.ExecutiveKey = h.Key
		}
		if h.SQL == "" {
			def.Status = "coming_soon"
		}
		out = append(out, def)
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[KPIDefinition]{Items: out})
}

func (b *BI) reportRead(fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, ok := reqctx.Property(ctx); !ok {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "select a property"))
			return
		}
		var res any
		err := b.S.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
			var err error
			res, err = fn(ctx, tx, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

// replica wraps a typed read use case on the read replica.
func replica[T any](b *BI, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (T, error)) http.HandlerFunc {
	return b.reportRead(func(ctx context.Context, tx pgx.Tx, r *http.Request) (any, error) { return fn(ctx, tx, r) })
}

// replicaGlobal is replica for instance-level routes (no active property).
func replicaGlobal[T any](b *BI, fn func(ctx context.Context, tx pgx.Tx, r *http.Request) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var res T
		err := b.S.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
			var err error
			res, err = fn(ctx, tx, r)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, res)
	}
}

func (b *BI) registerExecutive(add func(route.Route)) {
	b.registerStore(add)
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/executive", Scope: route.ScopeProperty, Permission: catalog.ManagementView,
		Summary: "Executive Overview across domains with targets, MoM / YoY and year to date (analytics store)", Response: ExecutiveOverview{},
		Query:   []route.Param{{Name: "period", Enum: []string{"month", "year"}}, {Name: "month", Description: "YYYY-MM (default: this month)"}},
		Handler: replica(b, b.executive)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/executive/properties", Permission: catalog.ManagementView,
		Summary: "Executive KPIs of the month per property the user manages", Response: ExecutivePropertyComparison{},
		Query: []route.Param{{Name: "month", Description: "YYYY-MM"}}, Handler: replicaGlobal(b, b.byProperty)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/executive/trend", Scope: route.ScopeProperty, Permission: catalog.ManagementView,
		Summary: "Monthly series of an executive KPI with targets and last year", Response: ExecutiveTrend{},
		Query: []route.Param{{Name: "kpi", Required: true}, {Name: "months", Type: "integer"}}, Handler: replica(b, b.trend)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/drilldown", Scope: route.ScopeProperty, Permission: catalog.ManagementView,
		Summary:  "Drill-down of an executive KPI by day and dimension down to the source transactions",
		Response: ExecutiveDrilldown{}, Query: []route.Param{{Name: "kpi", Required: true}, {Name: "from"}, {Name: "to"},
			{Name: "by", Enum: append(append([]string{}, revenueDims...), "lines")}, {Name: "filter[...]", Description: "day, weekday (1 = Monday), daypart, business_line, revenue_component, outlet, segment"}},
		Handler: replica(b, b.drilldown)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/hr-performance", Scope: route.ScopeProperty, Permission: PermHRPerformance,
		Summary: "HR Performance dashboard (registered HR KPIs)", Response: HRPerformanceDashboard{}, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: replica(b, b.hrPerformance)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/kpi-definitions", Permission: catalog.ManagementView,
		Summary: "KPI definitions (one definition per KPI)", Response: KPIDefinition{}, List: true, Handler: b.definitions})
}

// MonthTargets returns the approved targets of a month by KPI key (empty
// without an approved plan for the year); the Finance Dashboard compares
// them with the ledger (wired by internal/app).
func MonthTargets(ctx context.Context, q dbtx.Querier, property uuid.UUID, year, month int) (map[string]decimal.Decimal, error) {
	rows, err := q.Query(ctx, `SELECT t.kpi_key, t.target::text FROM reporting.kpi_targets t
		JOIN reporting.kpi_target_plans p ON p.id = t.plan_id
		WHERE p.property_id = $1 AND p.year = $2 AND p.status = 'approved' AND t.month = $3`, property, year, month)
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
		out[k], _ = decimal.NewFromString(v)
	}
	return out, rows.Err()
}
