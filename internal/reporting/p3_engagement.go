package reporting

// Reports and KPI dashboards of CRM engagement & loyalty (EP-05–09) on the area's reporting views:
// Campaign Performance, Complaint, NPS, Top Spender, Loyalty Points and
// Loyalty Liability Reports (FR-RPT-P3-05) and the engagement KPIs of the
// CRM Performance dashboard (FR-RPT-P3-02): Active Customers, Member
// Activity, Campaign Performance, Loyalty, Top Spender, NPS, Complaint SLA.

import "slices"

var businessLineEnum = []string{"golf", "sportclub", "stay", "pos", "membership", "banquet", "other"}

var engagementReports = []*Report{
	sqlReport("crm.campaign_performance", "Campaign Performance Report", "crm",
		"Campaigns scheduled or sent in the period: recipients, consent skips, delivered, read, clicked, converted and unsubscribed.",
		cols("code|Campaign", "name|Name", "channel|Channel", "status|Status", "sentAt|Sent|datetime", "recipients|Recipients|number",
			"skipped|Skipped (consent / suppression / cap)|number", "sent|Sent|number", "delivered|Delivered|number", "read|Read|number",
			"clicked|Clicked|number", "converted|Converted|number", "unsubscribed|Unsubscribed|number", "clickRate|Click Rate|number",
			"conversionRate|Conversion Rate|number"),
		[]Param{{Key: "channel", Label: "Channel", Type: "enum", Enum: []string{"email", "whatsapp", "in_app"}}}, 30,
		`SELECT c.code, c.name, c.channel, c.status, c.sent_at AS "sentAt", count(r.recipient_id)::int AS "recipients",
		count(r.recipient_id) FILTER (WHERE r.status LIKE 'skipped%')::int AS "skipped", count(r.recipient_id) FILTER (WHERE r.status = 'sent')::int AS "sent",
		count(r.recipient_id) FILTER (WHERE r.status = 'sent' AND (r.delivered_at IS NOT NULL OR r.delivery_status IN ('delivered', 'read')))::int AS "delivered",
		count(r.recipient_id) FILTER (WHERE r.read_at IS NOT NULL OR r.delivery_status = 'read')::int AS "read",
		count(r.recipient_id) FILTER (WHERE r.clicked_at IS NOT NULL)::int AS "clicked", count(r.recipient_id) FILTER (WHERE r.converted_at IS NOT NULL)::int AS "converted",
		count(r.recipient_id) FILTER (WHERE r.unsubscribed_at IS NOT NULL)::int AS "unsubscribed",
		trim_scale(round(coalesce(count(r.recipient_id) FILTER (WHERE r.clicked_at IS NOT NULL)::numeric / nullif(count(r.recipient_id) FILTER (WHERE r.status = 'sent'), 0), 0), 4))::text AS "clickRate",
		trim_scale(round(coalesce(count(r.recipient_id) FILTER (WHERE r.converted_at IS NOT NULL)::numeric / nullif(count(r.recipient_id) FILTER (WHERE r.status = 'sent'), 0), 0), 4))::text AS "conversionRate"
		FROM reporting.eng_campaigns c LEFT JOIN reporting.eng_campaign_recipients r ON r.campaign_id = c.campaign_id
		WHERE (coalesce(c.sent_at, c.scheduled_at, c.created_at) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR c.channel = $4)
		GROUP BY c.campaign_id, c.code, c.name, c.channel, c.status, c.sent_at, c.created_at ORDER BY coalesce(c.sent_at, c.created_at), c.code`),
	sqlReport("crm.complaint", "Complaint Report", "crm",
		"Complaint tickets opened in the period: category, line, priority, channel, status, escalation and whether the first-response and resolution SLA were met.",
		cols("number|Ticket", "customer|Customer", "category|Category", "businessLine|Business Line", "priority|Priority", "channel|Channel",
			"subject|Subject", "status|Status", "escalationLevel|Escalation Level|number", "createdAt|Opened|datetime", "firstRespondedAt|First Response|datetime",
			"resolvedAt|Resolved|datetime", "resolutionHours|Resolution (h)|number", "firstResponseBreached|First Response SLA Breached",
			"resolutionBreached|Resolution SLA Breached", "reopened|Reopened|number"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"open", "in_progress", "escalated", "resolved", "closed"}},
			{Key: "businessLine", Label: "Business Line", Type: "enum", Enum: businessLineEnum},
			{Key: "priority", Label: "Priority", Type: "enum", Enum: []string{"low", "medium", "high", "urgent"}}}, 30,
		`SELECT number, customer_name AS "customer", category_name AS "category", business_line AS "businessLine", priority, channel, subject, status,
		escalation_level AS "escalationLevel", created_at AS "createdAt", first_responded_at AS "firstRespondedAt", resolved_at AS "resolvedAt",
		trim_scale(round((extract(epoch FROM resolved_at - created_at) / 3600)::numeric, 2))::text AS "resolutionHours",
		CASE WHEN first_response_breached OR (first_responded_at IS NULL AND first_response_due_at < now()) THEN 'yes' ELSE 'no' END AS "firstResponseBreached",
		CASE WHEN resolution_breached OR (resolved_at IS NULL AND resolution_due_at < now()) THEN 'yes' ELSE 'no' END AS "resolutionBreached",
		reopened_count AS "reopened"
		FROM reporting.eng_tickets WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		AND ($4 = '' OR status = $4) AND ($5 = '' OR business_line = $5) AND ($6 = '' OR priority = $6) ORDER BY created_at, number`),
	sqlReport("crm.nps", "NPS Report", "crm",
		"Net Promoter Score per business line for the period from survey and relationship answers: promoters (9–10), passives (7–8), detractors (0–6).",
		cols("businessLine|Business Line", "responses|Responses|number", "promoters|Promoters|number", "passives|Passives|number",
			"detractors|Detractors|number", "nps|NPS|number"), nil, 90,
		`SELECT coalesce(business_line, 'all') AS "businessLine", count(*)::int AS "responses", count(*) FILTER (WHERE score >= 9)::int AS "promoters",
		count(*) FILTER (WHERE score BETWEEN 7 AND 8)::int AS "passives", count(*) FILTER (WHERE score <= 6)::int AS "detractors",
		trim_scale(round((count(*) FILTER (WHERE score >= 9) - count(*) FILTER (WHERE score <= 6)) * 100.0 / nullif(count(*), 0), 1))::text AS "nps"
		FROM reporting.eng_nps WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY ROLLUP (business_line) ORDER BY business_line NULLS LAST`),
	sqlReport("crm.top_spender", "Top Spender Report", "crm",
		"Customers ranked by spend in the period from folios: net charges − refunds − credit notes (− points / voucher payments per the Top Spender policy), per business line.",
		cols("rank|Rank|number", "code|Customer Code", "customer|Customer", "member|Member", "charges|Net Charges|number", "refunds|Refunds|number",
			"credits|Credit Notes|number", "excluded|Excluded Payments|number", "spend|Spend|number", "golf|Golf|number", "fnb|F&B|number",
			"sport|Sport|number", "bungalow|Bungalow|number", "banquet|Banquet|number", "visits|Visit Days|number"),
		[]Param{{Key: "memberType", Label: "Member Type", Type: "enum", Enum: []string{"member", "non_member", "corporate"}}}, 30,
		`WITH s AS (SELECT customer_id, kind, business_line, amount, (occurred_at AT TIME ZONE $3)::date AS day FROM reporting.eng_spend
		  WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date),
		a AS (SELECT customer_id, sum(amount) FILTER (WHERE kind = 'charge') AS charges, -coalesce(sum(amount) FILTER (WHERE kind = 'refund'), 0) AS refunds,
		  -coalesce(sum(amount) FILTER (WHERE kind = 'credit_note'), 0) AS credits, -coalesce(sum(amount) FILTER (WHERE kind = 'excluded'), 0) AS excluded,
		  sum(amount) AS spend, coalesce(sum(amount) FILTER (WHERE kind = 'charge' AND business_line = 'golf'), 0) AS golf,
		  coalesce(sum(amount) FILTER (WHERE kind = 'charge' AND business_line = 'pos'), 0) AS fnb,
		  coalesce(sum(amount) FILTER (WHERE kind = 'charge' AND business_line = 'sportclub'), 0) AS sport,
		  coalesce(sum(amount) FILTER (WHERE kind = 'charge' AND business_line = 'stay'), 0) AS bungalow,
		  coalesce(sum(amount) FILTER (WHERE kind = 'charge' AND business_line = 'banquet'), 0) AS banquet,
		  count(DISTINCT day) FILTER (WHERE kind = 'charge') AS visits
		  FROM s GROUP BY customer_id HAVING count(*) FILTER (WHERE kind = 'charge') > 0)
		SELECT (rank() OVER (ORDER BY a.spend DESC, c.name, c.customer_id))::int AS "rank", c.code, c.name AS "customer",
		CASE WHEN c.member THEN 'yes' ELSE 'no' END AS "member", trim_scale(a.charges)::text AS "charges", trim_scale(a.refunds)::text AS "refunds",
		trim_scale(a.credits)::text AS "credits", trim_scale(a.excluded)::text AS "excluded", trim_scale(a.spend)::text AS "spend",
		trim_scale(a.golf)::text AS "golf", trim_scale(a.fnb)::text AS "fnb", trim_scale(a.sport)::text AS "sport", trim_scale(a.bungalow)::text AS "bungalow",
		trim_scale(a.banquet)::text AS "banquet", a.visits::int AS "visits"
		FROM a JOIN reporting.eng_customers c ON c.customer_id = a.customer_id
		WHERE a.spend > 0 AND ($4 = '' OR ($4 = 'member' AND c.member) OR ($4 = 'non_member' AND NOT c.member) OR ($4 = 'corporate' AND c.customer_type = 'corporate'))
		ORDER BY a.spend DESC, c.name, c.customer_id`),
	sqlReport("crm.loyalty_points", "Loyalty Points Report", "crm",
		"Points ledger entries of the period: earned, redeemed, expired, adjusted and reversed points per account with the balance after each entry.",
		cols("occurredAt|Date|datetime", "account|Account", "customer|Customer", "tier|Tier", "kind|Type", "points|Points|number", "balanceAfter|Balance|number",
			"source|Source", "reference|Reference", "amount|Amount|number", "description|Description"),
		[]Param{{Key: "kind", Label: "Type", Type: "enum", Enum: []string{"earned", "redeemed", "expired", "adjusted", "reversed"}}}, 30,
		`SELECT occurred_at AS "occurredAt", account_number AS "account", customer_name AS "customer", tier_name AS "tier", kind, points,
		balance_after AS "balanceAfter", source_type AS "source", source_ref AS "reference", trim_scale(amount)::text AS "amount", description
		FROM reporting.eng_loyalty_ledger WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR kind = $4)
		ORDER BY occurred_at, entry_id`),
	sqlReport("crm.loyalty_liability", "Loyalty Liability Report", "crm",
		"Outstanding points per account at the end of the To date valued at the redemption value of the Loyalty Policies (points × value = liability).",
		cols("account|Account", "customer|Customer", "tier|Tier", "points|Points|number", "redemptionValue|Point Value|number", "liability|Liability|number"), nil, 0,
		`SELECT a.number AS "account", a.customer_name AS "customer", a.tier_name AS "tier", b.points::bigint AS "points",
		trim_scale(v.redemption_value)::text AS "redemptionValue", trim_scale(b.points * v.redemption_value)::text AS "liability"
		FROM (SELECT account_id, property_id, sum(points) AS points FROM reporting.eng_loyalty_ledger
		      WHERE occurred_at < (($2::date + 1)::timestamp AT TIME ZONE $3) AND $1::date IS NOT NULL GROUP BY account_id, property_id) b
		JOIN reporting.eng_loyalty_accounts a ON a.account_id = b.account_id JOIN reporting.eng_loyalty_value v ON v.property_id = b.property_id
		WHERE b.points > 0 ORDER BY b.points DESC, a.number`),
}

// engagementKPIs are the engagement figures of the CRM Performance dashboard.
var engagementKPIs = []kpiDef{
	{key: "active_customers", label: "Active Customers", unit: "count", def: "Customers with at least one charge in the period",
		sql: `SELECT count(DISTINCT customer_id)::text FROM reporting.eng_folio_lines WHERE customer_id IS NOT NULL
		AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT business_line AS label, count(DISTINCT customer_id)::text AS value FROM reporting.eng_folio_lines WHERE customer_id IS NOT NULL
		AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "member_activity", label: "Member Activity", unit: "ratio", def: "Active members with a charge in the period ÷ active members",
		sql: `SELECT trim_scale(round(coalesce((SELECT count(DISTINCT l.customer_id) FROM reporting.eng_folio_lines l JOIN reporting.eng_customers c
		  ON c.customer_id = l.customer_id AND c.member WHERE (l.posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
		  nullif((SELECT count(*) FROM reporting.eng_customers WHERE member), 0), 0), 4))::text`},
	{key: "campaign_performance", label: "Campaign Performance", unit: "ratio", def: "Campaign messages of the period that converted ÷ messages sent",
		sql: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE converted_at IS NOT NULL)::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.eng_campaign_recipients WHERE status = 'sent' AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT x.label, x.value FROM (SELECT 1 AS o, 'sent' AS label, count(*)::text AS value FROM reporting.eng_campaign_recipients
		WHERE status = 'sent' AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		UNION ALL SELECT 2, 'clicked', count(*) FILTER (WHERE clicked_at IS NOT NULL)::text FROM reporting.eng_campaign_recipients
		WHERE status = 'sent' AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		UNION ALL SELECT 3, 'converted', count(*) FILTER (WHERE converted_at IS NOT NULL)::text FROM reporting.eng_campaign_recipients
		WHERE status = 'sent' AND (sent_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date) x ORDER BY x.o`},
	{key: "loyalty_points_earned", label: "Loyalty", unit: "count", def: "Points earned in the period (per ledger type in the breakdown)",
		sql: `SELECT coalesce(sum(points) FILTER (WHERE kind = 'earned'), 0)::text FROM reporting.eng_loyalty_ledger
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT kind AS label, sum(points)::text AS value FROM reporting.eng_loyalty_ledger
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "loyalty_liability", label: "Loyalty Liability", unit: "idr", def: "Outstanding points at the end of the To date × redemption value",
		sql: `SELECT trim_scale(coalesce(sum(b.points * v.redemption_value) FILTER (WHERE b.points > 0), 0))::text FROM
		(SELECT account_id, property_id, sum(points) AS points FROM reporting.eng_loyalty_ledger WHERE occurred_at < (($2::date + 1)::timestamp AT TIME ZONE $3)
		 AND $1::date IS NOT NULL GROUP BY account_id, property_id) b JOIN reporting.eng_loyalty_value v ON v.property_id = b.property_id`},
	{key: "top_spender", label: "Top Spender", unit: "idr", def: "Spend of the 10 highest-spending customers of the period (top 5 in the breakdown)",
		sql: `SELECT trim_scale(coalesce(sum(spend), 0))::text FROM (SELECT customer_id, sum(amount) AS spend FROM reporting.eng_spend
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 HAVING sum(amount) > 0 ORDER BY 2 DESC LIMIT 10) t`,
		breakdown: `SELECT c.name AS label, trim_scale(t.spend)::text AS value FROM (SELECT customer_id, sum(amount) AS spend FROM reporting.eng_spend
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 HAVING sum(amount) > 0 ORDER BY 2 DESC LIMIT 5) t
		JOIN reporting.eng_customers c ON c.customer_id = t.customer_id ORDER BY t.spend DESC, c.name`},
	{key: "nps", label: "NPS", unit: "count", def: "% promoters − % detractors of the answers of the period (−100 … 100)",
		sql: `SELECT trim_scale(round(coalesce((count(*) FILTER (WHERE score >= 9) - count(*) FILTER (WHERE score <= 6)) * 100.0 / nullif(count(*), 0), 0), 1))::text
		FROM reporting.eng_nps WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT business_line AS label, trim_scale(round((count(*) FILTER (WHERE score >= 9) - count(*) FILTER (WHERE score <= 6)) * 100.0 / count(*), 1))::text
		AS value FROM reporting.eng_nps WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "complaint_sla", label: "Complaint SLA", unit: "ratio", def: "Tickets resolved in the period within the resolution SLA ÷ tickets resolved",
		sql: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE NOT resolution_breached)::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.eng_tickets WHERE resolved_at IS NOT NULL AND (resolved_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT priority AS label, count(*)::text AS value FROM reporting.eng_tickets WHERE status IN ('open', 'in_progress', 'escalated')
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
}

// The engagement KPIs follow the CRM Sales KPIs on CRM Performance.
func init() {
	d := dashboards[crmPerformance]
	d.name = "CRM Performance"
	d.kpis = append(append([]kpiDef{}, d.kpis...), engagementKPIs...)
	dashboards[crmPerformance] = d
	if !slices.Contains(DashboardCodes, crmPerformance) {
		DashboardCodes = append(DashboardCodes, crmPerformance)
	}
}
