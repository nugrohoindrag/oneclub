package reporting

// Reports and KPI dashboards of Banquet & Event (PRD P3 EP-12–15,
// FR-RPT-P3-01/05, FR-EVT-09): Event Report (pax planned vs actual, revenue
// per component, payments, outstanding), Banquet Revenue Report, BEO Report
// and the Banquet Performance dashboard. The dashboard's Banquet Revenue and
// the Banquet Revenue Report read the same view with the same period rule
// (EP-23 AC).

import "slices"

// banquetRevenueDay is the business date of a banquet revenue line.
const banquetRevenueDay = `coalesce(business_date, (posted_at AT TIME ZONE $3)::date)`

// banquetLines are the quotation lines of the banquet funnel.
const banquetLines = `('wedding', 'banquet', 'mice', 'event')`

var banquetReports = []*Report{
	sqlReport("banquet.event", "Event Report", "banquet",
		"Events starting in the period: pax planned vs actual, revenue per component, payments received and outstanding.",
		cols("number|Event", "title|Title", "eventType|Type", "status|Status", "start|Start|datetime", "venues|Venues", "customer|Customer", "sales|Sales",
			"expectedPax|Expected Pax|number", "guaranteedPax|Guaranteed Pax|number", "finalPax|Actual Pax|number", "package|Package|number",
			"fnb|F&B|number", "venue|Venue|number", "other|Other|number", "revenue|Revenue|number", "paid|Paid|number", "outstanding|Outstanding|number"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"inquiry", "tentative", "definite", "completed", "cancelled"}},
			{Key: "category", Label: "Category", Type: "enum", Enum: []string{"wedding", "banquet", "mice", "social", "sport", "tournament", "other"}}}, 30,
		`SELECT e.number, e.title, e.event_type AS "eventType", e.status, e.start_at AS "start", e.venues, e.customer_name AS "customer",
		e.sales_owner_name AS "sales", e.expected_pax AS "expectedPax", e.guaranteed_pax AS "guaranteedPax", e.final_pax AS "finalPax",
		trim_scale(coalesce(sum(r.total) FILTER (WHERE r.revenue_component = 'banquet_package'), 0))::text AS "package",
		trim_scale(coalesce(sum(r.total) FILTER (WHERE r.revenue_component = 'banquet_fnb'), 0))::text AS "fnb",
		trim_scale(coalesce(sum(r.total) FILTER (WHERE r.revenue_component IN ('venue_rental', 'outdoor_venue')), 0))::text AS "venue",
		trim_scale(coalesce(sum(r.total) FILTER (WHERE r.revenue_component NOT IN ('banquet_package', 'banquet_fnb', 'venue_rental', 'outdoor_venue')), 0))::text
		  AS "other",
		trim_scale(coalesce(sum(r.total), 0))::text AS "revenue",
		trim_scale(coalesce((SELECT sum(t.paid_amount) FROM reporting.banquet_payment_terms t WHERE t.event_id = e.event_id), 0))::text AS "paid",
		trim_scale(greatest(coalesce(sum(r.total), 0) - coalesce((SELECT sum(t.paid_amount) FROM reporting.banquet_payment_terms t WHERE t.event_id = e.event_id), 0),
		  0))::text AS "outstanding"
		FROM reporting.banquet_events e LEFT JOIN reporting.banquet_revenue r ON r.event_id = e.event_id
		WHERE (e.start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR e.status = $4) AND ($5 = '' OR e.category = $5)
		GROUP BY e.event_id, e.number, e.title, e.event_type, e.status, e.start_at, e.venues, e.customer_name, e.sales_owner_name, e.expected_pax,
		  e.guaranteed_pax, e.final_pax
		ORDER BY e.start_at, e.number`),
	sqlReport("banquet.revenue", "Banquet Revenue Report", "banquet",
		"Banquet revenue posted in the period per event and revenue component (net, service, tax, total); equals Banquet Revenue on Banquet Performance.",
		cols("number|Event", "title|Title", "component|Revenue Component", "net|Net|number", "service|Service|number", "tax|Tax|number", "total|Total|number"),
		nil, 30,
		`SELECT coalesce(e.number, '—') AS "number", coalesce(e.title, '') AS "title", r.revenue_component AS "component",
		trim_scale(sum(r.net))::text AS "net", trim_scale(sum(r.service))::text AS "service", trim_scale(sum(r.tax))::text AS "tax",
		trim_scale(sum(r.total))::text AS "total"
		FROM reporting.banquet_revenue r LEFT JOIN reporting.banquet_events e ON e.event_id = r.event_id
		WHERE `+banquetRevenueDay+` BETWEEN $1::date AND $2::date
		GROUP BY e.number, e.title, r.revenue_component ORDER BY e.number NULLS LAST, r.revenue_component`),
	sqlReport("banquet.beo", "BEO Report", "banquet",
		"Banquet Event Orders of events in the period: version, status, changes against the previous version and departments still to confirm.",
		cols("number|BEO", "version|Version|number", "status|Status", "event|Event", "title|Title", "eventDate|Event Date|datetime", "pax|Pax|number",
			"issuedAt|Issued|datetime", "changes|Changes|number", "requirements|Ingredients|number", "acknowledged|Confirmed|number",
			"departments|Departments|number", "pending|Pending Departments"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "issued", "superseded"}}}, 30,
		`SELECT number, version, status, event_number AS "event", event_title AS "title", event_date AS "eventDate", pax, issued_at AS "issuedAt",
		changes, requirements, acknowledged, departments, pending_departments AS "pending"
		FROM reporting.banquet_beos WHERE event_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR status = $4)
		ORDER BY event_date, number, version`),
}

// banquetKPIs are the figures of Banquet Performance (FR-RPT-P3-01).
var banquetKPIs = []kpiDef{
	{key: "inquiries", label: "Inquiry", unit: "count", def: "Leads of the wedding, banquet, MICE and event lines created in the period",
		sql: `SELECT count(*)::text FROM reporting.sales_leads WHERE line IN ` + banquetLines + ` AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT line AS label, count(*)::text AS value FROM reporting.sales_leads WHERE line IN ` + banquetLines + `
		AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "quotations", label: "Quotation", unit: "count", def: "Quotations of the banquet lines sent in the period (one per quotation number)",
		sql: `SELECT count(DISTINCT number)::text FROM reporting.sales_quotations WHERE line IN ` + banquetLines + `
		AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "deals", label: "Deal", unit: "count", def: "Quotations of the banquet lines accepted in the period",
		sql: `SELECT count(*)::text FROM reporting.sales_quotations WHERE line IN ` + banquetLines + ` AND status = 'accepted'
		AND (accepted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "inquiry_to_quotation", label: "Inquiry → Quotation", unit: "ratio", def: "Quotations ÷ inquiries of the period",
		sql: `SELECT trim_scale(round(coalesce((SELECT count(DISTINCT number) FROM reporting.sales_quotations WHERE line IN ` + banquetLines + `
		AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric / nullif((SELECT count(*) FROM reporting.sales_leads WHERE line IN ` + banquetLines + `
		AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date), 0), 0), 4))::text`},
	{key: "quotation_to_deal", label: "Quotation → Deal", unit: "ratio", def: "Deals ÷ quotations of the period",
		sql: `SELECT trim_scale(round(coalesce((SELECT count(*) FROM reporting.sales_quotations WHERE line IN ` + banquetLines + ` AND status = 'accepted'
		AND (accepted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric / nullif((SELECT count(DISTINCT number) FROM reporting.sales_quotations
		WHERE line IN ` + banquetLines + ` AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date), 0), 0), 4))::text`},
	{key: "event_count", label: "Event Count", unit: "count", def: "Tentative, Definite and Completed events starting in the period",
		sql: `SELECT count(*)::text FROM reporting.banquet_events WHERE status IN ('tentative', 'definite', 'completed')
		AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT category AS label, count(*)::text AS value FROM reporting.banquet_events WHERE status IN ('tentative', 'definite', 'completed')
		AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "pax", label: "Pax", unit: "count", def: "Pax of those events (final, else guaranteed, else expected)",
		sql: `SELECT coalesce(sum(pax), 0)::text FROM reporting.banquet_events WHERE status IN ('tentative', 'definite', 'completed')
		AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "banquet_revenue", label: "Banquet Revenue", unit: "idr", def: "Banquet revenue posted in the period (= Banquet Revenue Report)",
		sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.banquet_revenue WHERE ` + banquetRevenueDay + ` BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT revenue_component AS label, trim_scale(sum(total))::text AS value FROM reporting.banquet_revenue
		WHERE ` + banquetRevenueDay + ` BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "outstanding_dp", label: "Outstanding DP", unit: "idr", def: "Unpaid down payments of live events (now)",
		sql: `SELECT trim_scale(coalesce(sum(outstanding), 0))::text FROM reporting.banquet_payment_terms WHERE kind = 'down_payment'
		AND status NOT IN ('paid', 'cancelled') AND event_status IN ('inquiry', 'tentative', 'definite')
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
	{key: "outstanding_settlement", label: "Outstanding Settlement", unit: "idr", def: "Unpaid terms and final payments of live and completed events (now)",
		sql: `SELECT trim_scale(coalesce(sum(outstanding), 0))::text FROM reporting.banquet_payment_terms WHERE kind <> 'down_payment'
		AND status NOT IN ('paid', 'cancelled') AND event_status IN ('inquiry', 'tentative', 'definite', 'completed')
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
}

// banquetPerformance is the Banquet Performance dashboard (PRD P3 §7.1).
const banquetPerformance = "banquet-performance"

func init() {
	dashboards[banquetPerformance] = dashDef{name: "Banquet Performance", kpis: banquetKPIs}
	if !slices.Contains(DashboardCodes, banquetPerformance) {
		DashboardCodes = append(DashboardCodes, banquetPerformance)
	}
}
