package reporting

// Reports and KPI dashboards of promotions & packages (EP-10–11) on the area's reporting views:
// Promotion Performance Report, Package Sales Report and Package Revenue
// Allocation Report (FR-RPT-P3-05), and the Commercial Performance
// dashboard extended with Total Sales, Package Sales, Promotion Usage and
// Discount Cost next to the P2 Voucher Sold vs Redeemed and Average
// Transaction (FR-RPT-P3-03).

var commercialP3Reports = []*Report{
	sqlReport("commercial.promotion_performance", "Promotion Performance Report", "commercial",
		"Promotions used in the period: redemptions, discount cost by status, customers, budget and redemption limit used, per promotion.",
		cols("code|Promotion", "name|Name", "type|Type", "status|Status", "redemptions|Redemptions|number", "reversed|Reversed|number",
			"customers|Customers|number", "discount|Discount Cost|number", "posDiscount|POS|number", "bookingDiscount|Booking|number",
			"packageDiscount|Package|number", "budget|Budget|number", "budgetUsed|Budget Used|number", "maxRedemptions|Redemption Limit|number",
			"usedCount|Redemptions to Date|number"),
		[]Param{{Key: "promoType", Label: "Promotion Type", Type: "enum", Enum: []string{"percent_discount", "amount_discount", "happy_hour", "buy_n_get_x",
			"buy_n_price_x", "bundle", "member_discount", "promo_code", "period"}}}, 30,
		`SELECT p.code, p.name, p.promo_type AS "type", p.status,
		count(r.redemption_id) FILTER (WHERE r.status = 'redeemed')::int AS "redemptions",
		count(r.redemption_id) FILTER (WHERE r.status = 'reversed')::int AS "reversed",
		count(DISTINCT r.customer_id) FILTER (WHERE r.status = 'redeemed')::int AS "customers",
		trim_scale(coalesce(sum(r.discount_amount) FILTER (WHERE r.status = 'redeemed'), 0))::text AS "discount",
		trim_scale(coalesce(sum(r.discount_amount) FILTER (WHERE r.status = 'redeemed' AND r.source_type = 'pos_order'), 0))::text AS "posDiscount",
		trim_scale(coalesce(sum(r.discount_amount) FILTER (WHERE r.status = 'redeemed' AND r.source_type = 'pricing_snapshot'), 0))::text AS "bookingDiscount",
		trim_scale(coalesce(sum(r.discount_amount) FILTER (WHERE r.status = 'redeemed' AND r.source_type = 'package_booking'), 0))::text AS "packageDiscount",
		trim_scale(p.budget_amount)::text AS "budget", trim_scale(p.used_amount)::text AS "budgetUsed", p.max_redemptions AS "maxRedemptions",
		p.used_count AS "usedCount"
		FROM reporting.commercial_promotions p JOIN reporting.commercial_promotion_redemptions r ON r.promotion_id = p.promotion_id
		  AND (coalesce(r.redeemed_at, r.created_at) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		WHERE ($4 = '' OR p.promo_type = $4)
		GROUP BY p.promotion_id, p.code, p.name, p.promo_type, p.status, p.budget_amount, p.used_amount, p.max_redemptions, p.used_count
		ORDER BY sum(r.discount_amount) FILTER (WHERE r.status = 'redeemed') DESC NULLS LAST, p.code`),
	sqlReport("commercial.package_sales", "Package Sales Report", "commercial",
		"Package bookings made in the period per package: bookings, pax, list price, promotion discount, net, tax & service, total, cancellations and fees.",
		cols("code|Package", "name|Name", "type|Type", "bookings|Bookings|number", "pax|Pax|number", "listTotal|List Price|number",
			"discount|Discount|number", "net|Net|number", "serviceTax|Service & Tax|number", "total|Total|number", "cancelled|Cancelled|number",
			"cancellationFees|Cancellation Fees|number", "completed|Completed|number"),
		[]Param{{Key: "channel", Label: "Channel", Type: "enum", Enum: []string{"back_office", "website", "member_app", "ops", "quotation"}}}, 30,
		`SELECT package_code AS "code", min(package_name) AS "name", min(package_type) AS "type",
		count(*) FILTER (WHERE status IN ('confirmed', 'completed'))::int AS "bookings",
		coalesce(sum(pax) FILTER (WHERE status IN ('confirmed', 'completed')), 0)::int AS "pax",
		trim_scale(coalesce(sum(list_total) FILTER (WHERE status IN ('confirmed', 'completed')), 0))::text AS "listTotal",
		trim_scale(coalesce(sum(discount_total) FILTER (WHERE status IN ('confirmed', 'completed')), 0))::text AS "discount",
		trim_scale(coalesce(sum(net_total) FILTER (WHERE status IN ('confirmed', 'completed')), 0))::text AS "net",
		trim_scale(coalesce(sum(service_total + tax_total) FILTER (WHERE status IN ('confirmed', 'completed')), 0))::text AS "serviceTax",
		trim_scale(coalesce(sum(total) FILTER (WHERE status IN ('confirmed', 'completed')), 0))::text AS "total",
		count(*) FILTER (WHERE status IN ('cancelled', 'expired'))::int AS "cancelled",
		trim_scale(coalesce(sum(cancellation_fee), 0))::text AS "cancellationFees",
		count(*) FILTER (WHERE status = 'completed')::int AS "completed"
		FROM reporting.commercial_package_bookings WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR channel = $4)
		GROUP BY package_code ORDER BY sum(total) FILTER (WHERE status IN ('confirmed', 'completed')) DESC NULLS LAST, package_code`),
	sqlReport("commercial.package_allocation", "Package Revenue Allocation Report", "commercial",
		"Revenue allocated per revenue component of the packages confirmed in the period (liabilities apart), with the share already consumed.",
		cols("revenueComponent|Revenue Component", "businessLine|Business Line", "liability|Liability", "components|Components|number",
			"net|Net|number", "service|Service|number", "tax|Tax|number", "total|Allocated|number", "consumed|Consumed|number"), nil, 30,
		`SELECT revenue_component AS "revenueComponent", business_line AS "businessLine", CASE WHEN liability THEN 'yes' ELSE 'no' END AS "liability",
		count(*)::int AS "components", trim_scale(sum(allocated_net))::text AS "net", trim_scale(sum(allocated_service))::text AS "service",
		trim_scale(sum(allocated_tax))::text AS "tax", trim_scale(sum(allocated_total))::text AS "total", trim_scale(sum(consumed_total))::text AS "consumed"
		FROM reporting.commercial_package_allocations WHERE status <> 'cancelled' AND confirmed_at IS NOT NULL
		  AND (confirmed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY revenue_component, business_line, liability ORDER BY sum(allocated_total) DESC, revenue_component`),
}

// commercialP3KPIs extend the Commercial Performance dashboard (FR-RPT-P3-03).
var commercialP3KPIs = []kpiDef{
	{key: "package_sales", label: "Package Sales", unit: "idr", def: "Total of the package bookings confirmed in the period (tax & service included)",
		sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.commercial_package_bookings WHERE status IN ('confirmed', 'completed')
		AND (confirmed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT package_name AS label, trim_scale(sum(total))::text AS value FROM reporting.commercial_package_bookings
		WHERE status IN ('confirmed', 'completed') AND (confirmed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "promotion_usage", label: "Promotion Usage", unit: "count", def: "Promotions redeemed in the period (sales completed with a promotion)",
		sql: `SELECT count(*)::text FROM reporting.commercial_promotion_redemptions WHERE status = 'redeemed'
		AND (redeemed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT promotion_name AS label, count(*)::text AS value FROM reporting.commercial_promotion_redemptions WHERE status = 'redeemed'
		AND (redeemed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY count(*) DESC, 1`},
	{key: "discount_cost", label: "Discount Cost", unit: "idr", def: "Discount given by the promotions redeemed in the period",
		sql: `SELECT trim_scale(coalesce(sum(discount_amount), 0))::text FROM reporting.commercial_promotion_redemptions WHERE status = 'redeemed'
		AND (redeemed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT business_line AS label, trim_scale(sum(discount_amount))::text AS value FROM reporting.commercial_promotion_redemptions
		WHERE status = 'redeemed' AND (redeemed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
}

// totalSalesKPI is the revenue posted in the period on every business line
// (folio charges without liabilities).
var totalSalesKPI = kpiDef{key: "total_sales", label: "Total Sales", unit: "idr", def: "Charges posted in the period on every business line, liabilities excluded",
	sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.revenue_lines WHERE NOT liability
	AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
	breakdown: `SELECT business_line AS label, trim_scale(sum(total))::text AS value FROM reporting.revenue_lines WHERE NOT liability
	AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`}

const commercialPerformance = "commercial-performance"

func init() {
	d := dashboards[commercialPerformance]
	kpis := []kpiDef{totalSalesKPI}
	for _, k := range d.kpis {
		kpis = append(kpis, k)
		if k.key == "pos_sales" {
			kpis = append(kpis, commercialP3KPIs...)
		}
	}
	if len(kpis) == len(d.kpis)+1 {
		kpis = append(kpis, commercialP3KPIs...)
	}
	d.kpis = kpis
	dashboards[commercialPerformance] = d
}
