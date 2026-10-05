package reporting

// BI reports of PRD P5 (EP-21): the KPI Target vs Actual Report (monthly
// actual of every executive KPI from the analytics store against the
// approved target plan) — the report management receives on a schedule
// (§9.5) — and the Analytics Refresh Report (freshness of the store).

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

var biReportRoles = []string{"property_admin", "general_manager", "finance_manager", "club_manager", "resort_manager", "accountant"}

// KPITargetReport is the KPI Target vs Actual Report.
var KPITargetReport = &Report{
	Code: "reporting.kpi_target_vs_actual", Name: "KPI Target vs Actual Report", Module: "reporting",
	Permission:  "reporting.kpi_target_report.view",
	Description: "Monthly actual of the executive KPIs (analytics store) against the approved KPI target plan, with achievement and indicator.",
	Columns: cols("month|Month", "domain|Domain", "kpi|KPI", "unit|Unit", "actual|Actual|number", "target|Target|number",
		"achievement|Achievement|number", "indicator|Indicator", "plan|Target Plan"),
	Params: append(append([]Param{}, rangeParams...), Param{Key: "domain", Label: "Domain", Type: "enum",
		Enum: []string{"golf", "sportclub", "membership", "booking", "banquet", "commercial", "inventory", "procurement", "finance", "crm", "hr"}}),
	Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
		from, to, tz := period(ctx, tx, p, 180)
		rows, err := tx.Query(ctx, `SELECT v.property_id, to_char(v.period_start, 'YYYY-MM') AS month, v.kpi_key, v.value::text, t.target::text,
			CASE WHEN pl.id IS NULL THEN '' ELSE pl.year || ' v' || pl.version END AS plan
			FROM analytics.kpi_values v
			LEFT JOIN reporting.kpi_target_plans pl ON pl.property_id = v.property_id AND pl.year = extract(year FROM v.period_start)::int AND pl.status = 'approved'
			LEFT JOIN reporting.kpi_targets t ON t.plan_id = pl.id AND t.kpi_key = v.kpi_key AND t.month = extract(month FROM v.period_start)::int
			WHERE v.grain = 'month' AND v.period_start BETWEEN date_trunc('month', $1::date)::date AND $2::date AND $3::text <> ''
			ORDER BY v.period_start, v.kpi_key`, from, to, tz)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		catalogue := map[string]execKPI{}
		order := map[string]int{}
		for i, k := range executiveKPIs() {
			catalogue[k.Key] = k
			order[k.Key] = i
		}
		pol := DefaultBIPolicy()
		var out []map[string]any
		for rows.Next() {
			var prop any
			var month, key, value, plan string
			var target *string
			if err := rows.Scan(&prop, &month, &key, &value, &target, &plan); err != nil {
				return nil, err
			}
			k, ok := catalogue[key]
			if !ok || (p["domain"] != "" && k.Domain != p["domain"]) {
				continue
			}
			v, _ := decimal.NewFromString(value)
			row := map[string]any{"month": month, "domain": k.Domain, "kpi": k.Label, "unit": k.Unit, "actual": v.String(), "target": nil,
				"achievement": nil, "indicator": "no_target", "plan": plan}
			if target != nil {
				t, _ := decimal.NewFromString(*target)
				row["target"] = t.String()
				ind, ach := indicatorFor(v, t, k.Direction, pol)
				row["indicator"] = ind
				if ach != nil {
					row["achievement"] = *ach
				}
			}
			out = append(out, row)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		return out, rows.Err()
	},
}

// AnalyticsRefreshReport lists the refresh runs of the analytics store.
var AnalyticsRefreshReport = sqlReport("reporting.analytics_refresh", "Analytics Refresh Report", "reporting",
	"Refresh runs of the analytics store: window, KPIs, values and facts written, duration and failed KPIs (data latency, FR-BI-01).",
	cols("startedAt|Started|datetime", "kind|Kind", "windowFrom|From|datetime", "windowTo|To|datetime", "status|Status", "kpis|KPIs|number",
		"values|Values|number", "facts|Revenue Facts|number", "durationMs|Duration (ms)|number", "error|Failed KPIs"), nil, 7,
	`SELECT started_at AS "startedAt", kind, window_from AS "windowFrom", window_to AS "windowTo", status, kpis, values_written AS "values",
	facts_written AS "facts", duration_ms AS "durationMs", error FROM analytics.refresh_runs
	WHERE (started_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY started_at DESC`)

func init() {
	RegisterP5Report(KPITargetReport, biReportRoles...)
	RegisterP5Report(AnalyticsRefreshReport, "property_admin", "general_manager", "finance_manager")
}
