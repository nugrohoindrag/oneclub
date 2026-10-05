package reporting

// Reports of PRD P5 EP-22/23 (FR-RPT-P5-03): Package Profitability Report
// and Tournament Series Report, on the reporting views of the area
// (reporting.commercial_package_profit_lines, reporting.golf_series_standings),
// and the Package Margin KPI of Commercial Performance. Registered by
// internal/app (p5_leisure.go).

import "oneclub/internal/platform/catalog"

var leisureReports = []*Report{
	sqlReport("commercial.package_profitability", "Package Profitability Report", "commercial",
		"Packages taking place in the period (start date): bookings, pax, allocated revenue, pass-through, COGS (BOM, P4), caddy fee, room cost, commission, other costs and margin.",
		cols("code|Package", "name|Name", "type|Type", "bookings|Bookings|number", "pax|Pax|number", "revenue|Revenue|number", "passThrough|Pass-through|number",
			"cogs|COGS|number", "caddyFee|Caddy Fee|number", "roomCost|Room Cost|number", "commission|Commission|number", "otherCost|Other Costs|number",
			"totalCost|Total Cost|number", "margin|Margin|number", "marginPercent|Margin %|number"),
		[]Param{{Key: "packageType", Label: "Package Type", Type: "enum", Enum: []string{"golf_day", "golf_lunch", "stay_golf", "corporate", "wedding", "family",
			"other"}}}, 30,
		`SELECT package_code AS "code", min(package_name) AS "name", min(package_type) AS "type", count(DISTINCT booking_id)::int AS "bookings",
		coalesce(sum(pax) FILTER (WHERE first_line), 0)::int AS "pax",
		trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0))::text AS "revenue",
		trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'pass_through'), 0))::text AS "passThrough",
		trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'cogs'), 0))::text AS "cogs",
		trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'caddy_fee'), 0))::text AS "caddyFee",
		trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'room_cost'), 0))::text AS "roomCost",
		trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type = 'commission'), 0))::text AS "commission",
		trim_scale(coalesce(sum(amount) FILTER (WHERE cost_type IN ('labour', 'other')), 0))::text AS "otherCost",
		trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text AS "totalCost",
		trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0) - coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text AS "margin",
		CASE WHEN coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0) > 0 THEN trim_scale(round((coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0)
		  - coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0)) * 100 / sum(amount) FILTER (WHERE line_kind = 'revenue'), 2))::text END AS "marginPercent"
		FROM reporting.commercial_package_profit_lines WHERE start_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR package_type = $4)
		GROUP BY package_code ORDER BY sum(amount) FILTER (WHERE line_kind = 'revenue') DESC NULLS LAST, package_code`),
	sqlReport("golf.tournament_series", "Tournament Series Report", "golf",
		"Order of Merit standings of the seasons in the period: rank, points (best N events), events played, wins and top 3 per player and series.",
		cols("series|Series", "season|Season|number", "status|Status", "rank|Rank", "player|Player", "points|Points|number", "events|Events|number",
			"wins|Wins|number", "top3|Top 3|number", "final|Final"),
		[]Param{{Key: "series", Label: "Series Code", Type: "string"}}, 365,
		`SELECT series_name AS "series", season, series_status AS "status", position_label AS "rank", player_name AS "player",
		trim_scale(points)::text AS "points", events, wins, top3, CASE WHEN final THEN 'yes' ELSE 'no' END AS "final"
		FROM reporting.golf_series_standings
		WHERE season BETWEEN extract(year FROM $1::date) AND extract(year FROM $2::date) AND $3::text <> '' AND ($4 = '' OR series_code = upper($4))
		ORDER BY season DESC, series_name, rank NULLS LAST, points DESC, player_name`),
}

// LeisureReports are the reports of PRD P5 EP-22/23.
func LeisureReports() []*Report { return leisureReports }

// LeisureContribution adds the report permissions and grants them with the
// dashboards to the owning roles (the Tournament Series Report also to the
// golf roles, the Package Profitability Report to finance).
func LeisureContribution() catalog.Contribution {
	var perms []catalog.Permission
	roles := map[string][]string{}
	for _, r := range leisureReports {
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		for _, role := range reportRoles[r.Module] {
			roles[role] = append(roles[role], r.Permission, "reporting.report.view", "reporting.dashboard.view", "reporting.export.create")
		}
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}

// packageMarginKPI is the margin of the packages taking place in the period.
var packageMarginKPI = kpiDef{key: "package_margin", label: "Package Margin", unit: "idr",
	def: "Allocated revenue minus COGS and direct costs of the packages taking place in the period (Package Profitability)",
	sql: `SELECT trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0) - coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text
	FROM reporting.commercial_package_profit_lines WHERE start_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
	breakdown: `SELECT package_name AS label, trim_scale(coalesce(sum(amount) FILTER (WHERE line_kind = 'revenue'), 0)
	- coalesce(sum(amount) FILTER (WHERE line_kind = 'cost'), 0))::text AS value FROM reporting.commercial_package_profit_lines
	WHERE start_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`}

func init() {
	d := dashboards[commercialPerformance]
	d.kpis = append(append([]kpiDef{}, d.kpis...), packageMarginKPI)
	dashboards[commercialPerformance] = d
}
