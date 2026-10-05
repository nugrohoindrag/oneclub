package reporting

// Reports of PRD P5 advanced CRM & loyalty (FR-RPT-P5-03, FR-LOY-P5-06,
// FR-SEG-02/03) on the area's read models (reporting/00019): Journey
// Performance, Loyalty Tier, Tier Evaluation, Reward Redemption, Loyalty
// Programme Cost, NPS Analytics, Complaint SLA, Sales Performance, RFM
// Segment and VIP Customer Reports; and the P5 figures of the CRM
// Performance dashboard (journey conversion, loyalty cost ratio, VIPs).

var crmP5Reports = []*Report{
	sqlReport("crm.journey_performance", "Journey Performance Report", "crm",
		"Per journey and step of the period: executed, messages sent, skipped (consent, suppression, contact, frequency cap), read, clicked, converted enrollments and attributed revenue.",
		cols("journey|Journey", "journeyName|Journey Name", "step|Step", "stepType|Step Type", "executed|Executed|number", "sent|Sent|number",
			"skipped|Skipped|number", "control|Control Group|number", "read|Read|number", "clicked|Clicked|number", "converted|Converted|number",
			"revenue|Attributed Revenue|number", "conversionRate|Conversion Rate|number"),
		nil, 30,
		`SELECT journey_code AS "journey", journey_name AS "journeyName", coalesce(step_name, step_key) AS "step", step_type AS "stepType",
		count(*)::int AS "executed", count(*) FILTER (WHERE outcome = 'sent')::int AS "sent",
		count(*) FILTER (WHERE outcome LIKE 'skipped%')::int AS "skipped", count(*) FILTER (WHERE outcome = 'control')::int AS "control",
		count(*) FILTER (WHERE outcome = 'sent' AND read_at IS NOT NULL)::int AS "read", count(*) FILTER (WHERE outcome = 'sent' AND clicked_at IS NOT NULL)::int AS "clicked",
		count(DISTINCT enrollment_id) FILTER (WHERE outcome = 'sent' AND converted_at >= sent_at)::int AS "converted",
		trim_scale(coalesce(sum(revenue) FILTER (WHERE outcome = 'sent' AND converted_at >= sent_at), 0))::text AS "revenue",
		trim_scale(round(coalesce(count(DISTINCT enrollment_id) FILTER (WHERE outcome = 'sent' AND converted_at >= sent_at)::numeric
		  / nullif(count(*) FILTER (WHERE outcome = 'sent'), 0), 0), 4))::text AS "conversionRate"
		FROM reporting.crm_journey_events WHERE (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY journey_code, journey_name, step_key, step_name, step_type ORDER BY journey_code, min(sent_at), step_key`),
	sqlReport("crm.loyalty_tier", "Loyalty Tier Report", "crm",
		"Active loyalty accounts per tier at the end of the To date with the accounts in grace, and the upgrades / downgrades of the tier evaluations of the period.",
		cols("tier|Tier", "rank|Rank|number", "accounts|Accounts|number", "inGrace|In Grace|number", "upgradedTo|Upgraded To|number",
			"downgradedTo|Downgraded To|number", "graceStarted|Grace Started|number", "points|Points Balance|number"), nil, 365,
		`SELECT coalesce(a.tier_name, 'No tier') AS "tier", coalesce(a.tier_rank, -1) AS "rank", count(*)::int AS "accounts",
		count(*) FILTER (WHERE a.grace_until IS NOT NULL)::int AS "inGrace",
		(SELECT count(*) FROM reporting.crm_tier_evaluations e WHERE e.to_tier IS NOT DISTINCT FROM a.tier_name AND e.outcome = 'upgraded'
		  AND e.evaluated_on BETWEEN $1::date AND $2::date)::int AS "upgradedTo",
		(SELECT count(*) FROM reporting.crm_tier_evaluations e WHERE e.to_tier IS NOT DISTINCT FROM a.tier_name AND e.outcome = 'downgraded'
		  AND e.evaluated_on BETWEEN $1::date AND $2::date)::int AS "downgradedTo",
		(SELECT count(*) FROM reporting.crm_tier_evaluations e WHERE e.from_tier IS NOT DISTINCT FROM a.tier_name AND e.outcome = 'grace_started'
		  AND e.evaluated_on BETWEEN $1::date AND $2::date)::int AS "graceStarted",
		coalesce(sum(a.balance), 0)::bigint AS "points"
		FROM reporting.crm_tier_accounts a WHERE $3::text <> '' GROUP BY a.tier_name, a.tier_rank ORDER BY coalesce(a.tier_rank, -1), 1`),
	sqlReport("crm.tier_evaluation", "Tier Evaluation Report", "crm",
		"Results of the tier evaluations of the period per account: qualifying spend and points, tier before, qualified and after, grace end.",
		cols("evaluatedOn|Evaluated On", "evaluation|Evaluation", "kind|Kind", "account|Account", "customer|Customer", "fromTier|From Tier",
			"qualifiedTier|Qualified Tier", "toTier|To Tier", "outcome|Outcome", "spend|Qualifying Spend|number", "points|Qualifying Points|number",
			"graceUntil|Grace Until"),
		[]Param{{Key: "outcome", Label: "Outcome", Type: "enum", Enum: []string{"upgraded", "retained", "grace_started", "in_grace", "downgraded", "locked"}}}, 365,
		`SELECT to_char(evaluated_on, 'YYYY-MM-DD') AS "evaluatedOn", evaluation_number AS "evaluation", kind, account_number AS "account",
		customer_name AS "customer", from_tier AS "fromTier", qualified_tier AS "qualifiedTier", to_tier AS "toTier", outcome,
		trim_scale(spend_basis)::text AS "spend", points_basis AS "points", to_char(grace_until, 'YYYY-MM-DD') AS "graceUntil"
		FROM reporting.crm_tier_evaluations WHERE evaluated_on BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR outcome = $4)
		ORDER BY evaluated_on, evaluation_number, customer_name`),
	sqlReport("crm.reward_redemption", "Reward Redemption Report", "crm",
		"Rewards redeemed with points and rewards issued without points (Top Spender programme, journeys, staff) in the period, with their cost.",
		cols("date|Date|datetime", "number|Number", "customer|Customer", "reward|Reward", "rewardType|Reward Type", "source|Source", "quantity|Quantity|number",
			"points|Points|number", "cost|Cost|number", "status|Status"),
		[]Param{{Key: "source", Label: "Source", Type: "enum", Enum: []string{"points", "top_spender", "journey", "staff"}}}, 30,
		`SELECT * FROM (SELECT r.created_at AS "date", r.number, r.customer_name AS "customer", r.reward_name AS "reward", r.reward_type AS "rewardType",
		  'points' AS "source", r.quantity, r.points, trim_scale(coalesce(k.cost, 0))::text AS "cost", r.status
		  FROM reporting.eng_reward_redemptions r LEFT JOIN reporting.crm_reward_redemption_costs k ON k.redemption_id = r.redemption_id
		  WHERE (r.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		UNION ALL SELECT i.created_at, i.number, i.customer_name, i.reward_name, i.reward_type, i.source, i.quantity, 0, trim_scale(i.total_cost)::text, i.status
		  FROM reporting.crm_loyalty_issues i WHERE (i.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date) x
		WHERE ($4 = '' OR "source" = $4) ORDER BY "date", number`),
	sqlReport("crm.loyalty_cost", "Loyalty Programme Cost Report", "crm",
		"Per month: net revenue of the loyalty lines (golf, F&B, sport club, bungalow), points issued and their value, cost of rewards issued and redeemed, total programme cost and its share of revenue (budget 2%).",
		cols("month|Month", "netRevenue|Net Revenue (loyalty lines)|number", "pointsIssued|Points Issued|number", "pointsCost|Points Cost|number",
			"rewardCost|Rewards Issued Cost|number", "redeemedCost|Rewards Redeemed Cost|number", "totalCost|Programme Cost|number",
			"costRatio|Cost ÷ Revenue|number"), nil, 365,
		`WITH m AS (SELECT generate_series(date_trunc('month', $1::date), date_trunc('month', $2::date), interval '1 month')::date AS month)
		SELECT to_char(m.month, 'YYYY-MM') AS "month", trim_scale(r.rev)::text AS "netRevenue", p.points::bigint AS "pointsIssued",
		trim_scale(p.cost)::text AS "pointsCost", trim_scale(i.cost)::text AS "rewardCost", trim_scale(d.cost)::text AS "redeemedCost",
		trim_scale(p.cost + i.cost)::text AS "totalCost",
		trim_scale(round(coalesce((p.cost + i.cost) / nullif(r.rev, 0), 0), 4))::text AS "costRatio"
		FROM m
		CROSS JOIN LATERAL (SELECT coalesce(sum(net_amount), 0) AS rev FROM reporting.eng_folio_lines WHERE NOT liability
		  AND business_line IN ('golf', 'pos', 'sportclub', 'stay') AND business_date >= m.month AND business_date < (m.month + interval '1 month')::date) r
		CROSS JOIN LATERAL (SELECT coalesce(sum(l.points), 0) AS points, coalesce(sum(l.points * v.redemption_value), 0) AS cost
		  FROM reporting.eng_loyalty_ledger l JOIN reporting.eng_loyalty_value v ON v.property_id = l.property_id
		  WHERE (l.kind = 'earned' OR (l.kind = 'adjusted' AND l.points > 0) OR (l.kind = 'reversed' AND l.points < 0))
		  AND (l.occurred_at AT TIME ZONE $3)::date >= m.month AND (l.occurred_at AT TIME ZONE $3)::date < (m.month + interval '1 month')::date) p
		CROSS JOIN LATERAL (SELECT coalesce(sum(total_cost), 0) AS cost FROM reporting.crm_loyalty_issues WHERE status <> 'cancelled'
		  AND (created_at AT TIME ZONE $3)::date >= m.month AND (created_at AT TIME ZONE $3)::date < (m.month + interval '1 month')::date) i
		CROSS JOIN LATERAL (SELECT coalesce(sum(cost), 0) AS cost FROM reporting.crm_reward_redemption_costs WHERE status <> 'cancelled'
		  AND (created_at AT TIME ZONE $3)::date >= m.month AND (created_at AT TIME ZONE $3)::date < (m.month + interval '1 month')::date) d
		ORDER BY m.month`),
	sqlReport("crm.nps_analytics", "NPS Analytics Report", "crm",
		"NPS per month and business line of the period (survey and relationship answers) with promoters, passives and detractors and the answers with a comment.",
		cols("month|Month", "businessLine|Business Line", "responses|Responses|number", "promoters|Promoters|number", "passives|Passives|number",
			"detractors|Detractors|number", "nps|NPS|number", "withComment|With Comment|number"),
		[]Param{{Key: "businessLine", Label: "Business Line", Type: "enum", Enum: businessLineEnum}}, 180,
		`SELECT to_char((created_at AT TIME ZONE $3)::date, 'YYYY-MM') AS "month", business_line AS "businessLine", count(*)::int AS "responses",
		count(*) FILTER (WHERE score >= 9)::int AS "promoters", count(*) FILTER (WHERE score BETWEEN 7 AND 8)::int AS "passives",
		count(*) FILTER (WHERE score <= 6)::int AS "detractors",
		trim_scale(round((count(*) FILTER (WHERE score >= 9) - count(*) FILTER (WHERE score <= 6)) * 100.0 / nullif(count(*), 0), 1))::text AS "nps",
		count(*) FILTER (WHERE coalesce(comment, '') <> '')::int AS "withComment"
		FROM reporting.eng_nps WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR business_line = $4)
		GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("crm.complaint_sla", "Complaint SLA Report", "crm",
		"Complaint tickets of the period per business line and priority: resolved, within the resolution SLA, compliance, average resolution hours, first-response compliance and escalations.",
		cols("businessLine|Business Line", "priority|Priority", "tickets|Tickets|number", "resolved|Resolved|number", "withinSla|Within SLA|number",
			"compliance|Resolution Compliance|number", "firstResponseCompliance|First Response Compliance|number",
			"avgResolutionHours|Avg Resolution (h)|number", "escalated|Escalated|number", "reopened|Reopened|number"), nil, 90,
		`SELECT business_line AS "businessLine", priority, count(*)::int AS "tickets", count(*) FILTER (WHERE resolved_at IS NOT NULL)::int AS "resolved",
		count(*) FILTER (WHERE resolved_at IS NOT NULL AND NOT resolution_breached)::int AS "withinSla",
		trim_scale(round(coalesce(count(*) FILTER (WHERE resolved_at IS NOT NULL AND NOT resolution_breached)::numeric
		  / nullif(count(*) FILTER (WHERE resolved_at IS NOT NULL), 0), 0), 4))::text AS "compliance",
		trim_scale(round(coalesce(count(*) FILTER (WHERE first_responded_at IS NOT NULL AND NOT first_response_breached)::numeric
		  / nullif(count(*) FILTER (WHERE first_responded_at IS NOT NULL), 0), 0), 4))::text AS "firstResponseCompliance",
		trim_scale(round(coalesce(avg(extract(epoch FROM resolved_at - created_at) / 3600) FILTER (WHERE resolved_at IS NOT NULL), 0)::numeric, 2))::text AS "avgResolutionHours",
		count(*) FILTER (WHERE escalation_level > 0)::int AS "escalated", coalesce(sum(reopened_count), 0)::int AS "reopened"
		FROM reporting.eng_tickets WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("crm.sales_performance", "Sales Performance Report", "crm",
		"Opportunities created in the period per sales person and line: won, lost, win rate, won value, average sales cycle and the commission recognised in the period.",
		cols("owner|Sales", "line|Line", "opportunities|Opportunities|number", "won|Won|number", "lost|Lost|number", "winRate|Win Rate|number",
			"wonValue|Won Value|number", "avgCycleDays|Avg Cycle (days)|number", "commission|Commission|number"), nil, 180,
		`SELECT coalesce(o.owner_name, 'Unassigned') AS "owner", o.line, count(*)::int AS "opportunities", count(*) FILTER (WHERE o.status = 'won')::int AS "won",
		count(*) FILTER (WHERE o.status = 'lost')::int AS "lost",
		trim_scale(round(coalesce(count(*) FILTER (WHERE o.status = 'won')::numeric / nullif(count(*) FILTER (WHERE o.status IN ('won', 'lost')), 0), 0), 4))::text AS "winRate",
		trim_scale(coalesce(sum(o.expected_value) FILTER (WHERE o.status = 'won'), 0))::text AS "wonValue",
		trim_scale(round(coalesce(avg(extract(epoch FROM o.won_at - o.created_at) / 86400) FILTER (WHERE o.status = 'won'), 0)::numeric, 1))::text AS "avgCycleDays",
		trim_scale(coalesce((SELECT sum(c.amount) FROM reporting.crm_sales_owner_commissions c WHERE c.user_id = o.owner_user_id
		  AND c.recognized_on BETWEEN $1::date AND $2::date), 0))::text AS "commission"
		FROM reporting.sales_opportunities o WHERE (o.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY o.owner_user_id, o.owner_name, o.line ORDER BY 1, 2`),
	sqlReport("crm.rfm_segment", "RFM Segment Report", "crm",
		"Customers per RFM group of the latest snapshot on or before the To date: share, spend, average recency, frequency and lifetime value, cross-business customers.",
		cols("group|RFM Group", "customers|Customers|number", "monetary|Spend|number", "avgRecencyDays|Avg Recency (days)|number",
			"avgFrequency|Avg Visit Days|number", "avgClv|Avg Lifetime Value|number", "multiLine|Cross-business|number", "asOf|As Of"), nil, 0,
		`SELECT rfm_group AS "group", count(*)::int AS "customers", trim_scale(sum(monetary))::text AS "monetary",
		trim_scale(round(avg(recency_days), 1))::text AS "avgRecencyDays", trim_scale(round(avg(frequency), 1))::text AS "avgFrequency",
		trim_scale(round(avg(clv), 2))::text AS "avgClv", count(*) FILTER (WHERE cardinality(lines) >= 2)::int AS "multiLine",
		to_char(as_of, 'YYYY-MM-DD') AS "asOf"
		FROM reporting.crm_rfm_scores s WHERE as_of = (SELECT max(as_of) FROM reporting.crm_rfm_scores x WHERE x.property_id = s.property_id AND x.as_of <= $2::date)
		AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY rfm_group, as_of ORDER BY 2 DESC, 1`),
	sqlReport("crm.vip_customers", "VIP Customer Report", "crm",
		"VIP customers with level, source (Top Spender, tier, manual), reason, benefits and handling note.",
		cols("customerCode|Customer Code", "customer|Customer", "level|Level", "source|Source", "reason|Reason", "benefits|Benefits",
			"handlingNote|Handling", "validUntil|Valid Until", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"active", "inactive"}}}, 0,
		`SELECT customer_code AS "customerCode", customer_name AS "customer", level, source, reason, benefits, handling_note AS "handlingNote",
		to_char(valid_until, 'YYYY-MM-DD') AS "validUntil", status FROM reporting.crm_vip WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		AND ($4 = '' OR status = $4) ORDER BY level DESC, customer_name`),
}

// P5CRMReports are the reports of the P5 CRM area (registered in the BI registry by init).
func P5CRMReports() []*Report { return crmP5Reports }

// crmP5KPIs are the P5 figures of the CRM Performance dashboard.
var crmP5KPIs = []kpiDef{
	{key: "journey_conversion", label: "Journey Conversion", unit: "ratio", def: "Journey enrollments of the period that converted (goal event) ÷ enrollments",
		sql: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE converted_at IS NOT NULL)::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.crm_journey_enrollments WHERE (entered_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT journey_code AS label, count(*) FILTER (WHERE converted_at IS NOT NULL)::text AS value FROM reporting.crm_journey_enrollments
		WHERE (entered_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "loyalty_cost_ratio", label: "Loyalty Cost Ratio", unit: "ratio", def: "Loyalty programme cost (points issued × value + rewards issued) ÷ net revenue of the loyalty lines (budget 2%)",
		sql: `SELECT trim_scale(round(coalesce(((SELECT coalesce(sum(l.points * v.redemption_value), 0) FROM reporting.eng_loyalty_ledger l
		  JOIN reporting.eng_loyalty_value v ON v.property_id = l.property_id WHERE (l.kind = 'earned' OR (l.kind = 'adjusted' AND l.points > 0)
		  OR (l.kind = 'reversed' AND l.points < 0)) AND (l.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)
		  + (SELECT coalesce(sum(total_cost), 0) FROM reporting.crm_loyalty_issues WHERE status <> 'cancelled' AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date))
		  / nullif((SELECT sum(net_amount) FROM reporting.eng_folio_lines WHERE NOT liability AND business_line IN ('golf', 'pos', 'sportclub', 'stay')
		  AND business_date BETWEEN $1::date AND $2::date), 0), 0), 4))::text`},
	{key: "vip_customers", label: "VIP Customers", unit: "count", def: "Active VIP customers (per level in the breakdown)",
		sql: `SELECT count(*)::text FROM reporting.crm_vip WHERE status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT level AS label, count(*)::text AS value FROM reporting.crm_vip WHERE status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL
		AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
}

func init() {
	// FR-RPT-P5-03/04: one permission per report (unchanged codes), granted
	// with the report view and export to the CRM report roles.
	for _, r := range crmP5Reports {
		RegisterP5Report(r, reportRoles[r.Module]...)
	}
	d := dashboards[crmPerformance]
	d.kpis = append(append([]kpiDef{}, d.kpis...), crmP5KPIs...)
	dashboards[crmPerformance] = d
}
