package reporting

// Reports and KPIs of CRM Sales (PRD P3 EP-01–04, FR-RPT-P3-02/05): Lead
// Source Report, Sales Pipeline Report, Quotation Report and Sales
// Commission Report on the sales_* reporting views, and the sales part of
// the CRM Performance dashboard (leads per source, conversion, first
// response time, pipeline value, deals won). CRM engagement adds its KPIs
// to the same dashboard.

import "slices"

var salesReports = []*Report{
	sqlReport("crm.lead_source", "Lead Source Report", "crm",
		"Leads created in the period per source: qualified, unqualified and converted leads, conversion rate and first-response SLA.",
		cols("source|Source", "leads|Leads|number", "open|Open|number", "qualified|Qualified|number", "unqualified|Unqualified|number",
			"converted|Converted|number", "conversion|Conversion|number", "avgResponseHours|Avg First Response (h)|number",
			"slaMet|First Response within SLA|number"),
		[]Param{{Key: "line", Label: "Business Line", Type: "enum", Enum: []string{"wedding", "banquet", "mice", "event", "tournament", "stay", "golf",
			"package", "membership", "other"}}}, 30,
		`SELECT source, count(*)::int AS "leads", count(*) FILTER (WHERE status IN ('new', 'contacted'))::int AS "open",
		count(*) FILTER (WHERE qualified_at IS NOT NULL)::int AS "qualified", count(*) FILTER (WHERE status = 'unqualified')::int AS "unqualified",
		count(*) FILTER (WHERE status = 'converted')::int AS "converted",
		trim_scale(round(count(*) FILTER (WHERE status = 'converted')::numeric / nullif(count(*), 0), 4))::text AS "conversion",
		trim_scale(round(avg(extract(epoch FROM first_responded_at - created_at) / 3600) FILTER (WHERE first_responded_at IS NOT NULL)::numeric, 2))::text
		  AS "avgResponseHours",
		count(*) FILTER (WHERE first_responded_at IS NOT NULL AND first_responded_at <= first_response_due_at)::int AS "slaMet"
		FROM reporting.sales_leads WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR line = $4)
		GROUP BY source ORDER BY count(*) DESC, source`),
	sqlReport("crm.sales_pipeline", "Sales Pipeline Report", "crm",
		"Opportunities open now or closed in the period: stage, probability, expected and weighted value, expected close date and owner.",
		cols("number|Opportunity", "title|Title", "customer|Customer", "line|Business Line", "pipeline|Pipeline", "stage|Stage",
			"probability|Probability %|number", "expectedValue|Expected Value|number", "weightedValue|Weighted Value|number",
			"expectedCloseDate|Expected Close|datetime", "owner|Sales", "status|Status", "lostReason|Lost Reason"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"open", "won", "lost"}},
			{Key: "line", Label: "Business Line", Type: "enum", Enum: []string{"wedding", "banquet", "mice", "event", "tournament", "stay", "golf",
				"package", "membership", "other"}}}, 90,
		`SELECT number, title, customer_name AS "customer", line, pipeline_name AS "pipeline", stage_name AS "stage", trim_scale(probability)::text AS "probability",
		trim_scale(expected_value)::text AS "expectedValue", trim_scale(round(weighted_value, 2))::text AS "weightedValue",
		expected_close_date AS "expectedCloseDate", owner_name AS "owner", status, lost_reason AS "lostReason"
		FROM reporting.sales_opportunities
		WHERE (status = 'open' OR (coalesce(won_at, lost_at) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)
		  AND ($4 = '' OR status = $4) AND ($5 = '' OR line = $5)
		ORDER BY pipeline_name, stage_order, expected_close_date NULLS LAST, number`),
	sqlReport("crm.quotation", "Quotation Report", "crm",
		"Quotations created in the period (active versions): amounts, discount, validity, decision and the money received for accepted deals.",
		cols("number|Quotation", "version|Version|number", "title|Title", "customer|Customer", "line|Business Line", "owner|Sales",
			"subtotal|Subtotal|number", "discount|Discount|number", "total|Total|number", "validUntil|Valid Until|datetime", "status|Status",
			"acceptedAt|Accepted|datetime", "paid|Paid|number"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "pending_approval", "sent", "accepted", "rejected", "expired"}}}, 30,
		`SELECT number, version, title, customer_name AS "customer", line, owner_name AS "owner", trim_scale(subtotal)::text AS "subtotal",
		trim_scale(discount)::text AS "discount", trim_scale(total)::text AS "total", valid_until AS "validUntil", status, accepted_at AS "acceptedAt",
		trim_scale(paid_amount)::text AS "paid"
		FROM reporting.sales_quotations WHERE status <> 'revised' AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		  AND ($4 = '' OR status = $4) ORDER BY created_at, number`),
	sqlReport("crm.sales_commission", "Sales Commission Report", "crm",
		"Commission recognised in the period per sales: earned on paid deals, clawed back, adjusted, and the statement it belongs to.",
		cols("sales|Sales", "period|Period", "earned|Earned|number", "clawback|Clawback|number", "adjustments|Adjustments|number", "total|Total|number",
			"deals|Deals|number", "statement|Statement", "statementStatus|Statement Status"), nil, 31,
		`SELECT user_name AS "sales", period, trim_scale(coalesce(sum(amount) FILTER (WHERE kind = 'earned'), 0))::text AS "earned",
		trim_scale(coalesce(sum(amount) FILTER (WHERE kind = 'clawback'), 0))::text AS "clawback",
		trim_scale(coalesce(sum(amount) FILTER (WHERE kind = 'adjustment'), 0))::text AS "adjustments", trim_scale(sum(amount))::text AS "total",
		count(DISTINCT quotation_number) FILTER (WHERE kind = 'earned')::int AS "deals", min(statement_number) AS "statement",
		min(statement_status) AS "statementStatus"
		FROM reporting.sales_commissions WHERE recognized_on BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY user_id, user_name, period ORDER BY period, user_name`),
}

// salesKPIs are the sales figures of the CRM Performance dashboard.
var salesKPIs = []kpiDef{
	{key: "leads", label: "Leads", unit: "count", sql: `SELECT count(*)::text FROM reporting.sales_leads
		WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT source AS label, count(*)::text AS value FROM reporting.sales_leads
		WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY count(*) DESC, 1`},
	{key: "lead_conversion", label: "Conversion", unit: "ratio", def: "Leads of the period converted to a customer ÷ leads of the period",
		sql: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status = 'converted')::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.sales_leads WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "first_response_time", label: "First Response Time", unit: "hours", def: "Average hours from lead creation to the first response",
		sql: `SELECT trim_scale(round(coalesce(avg(extract(epoch FROM first_responded_at - created_at) / 3600), 0)::numeric, 2))::text
		FROM reporting.sales_leads WHERE first_responded_at IS NOT NULL AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "pipeline_value", label: "Pipeline Value", unit: "idr", def: "Expected value of the open opportunities (weighted value per line in the breakdown)",
		sql: `SELECT trim_scale(coalesce(sum(expected_value), 0))::text FROM reporting.sales_opportunities WHERE status = 'open'
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT line AS label, trim_scale(round(sum(weighted_value), 0))::text AS value FROM reporting.sales_opportunities WHERE status = 'open'
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "deals_won", label: "Deals Won", unit: "idr", def: "Total of the quotations accepted in the period",
		sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.sales_quotations WHERE status = 'accepted'
		AND (accepted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT line AS label, trim_scale(sum(total))::text AS value FROM reporting.sales_quotations WHERE status = 'accepted'
		AND (accepted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
}

// crmPerformance is the CRM Performance dashboard (label proposed in PRD
// P3 §7.6); CRM Sales KPIs come first.
const crmPerformance = "crm-performance"

func init() {
	d := dashboards[crmPerformance]
	d.name = "CRM Performance"
	d.kpis = append(append([]kpiDef{}, salesKPIs...), d.kpis...)
	dashboards[crmPerformance] = d
	if !slices.Contains(DashboardCodes, crmPerformance) {
		DashboardCodes = append(DashboardCodes, crmPerformance)
	}
}
