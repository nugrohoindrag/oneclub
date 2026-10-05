package reporting

// Reports of the member tier classes & classification (PRD P5 EP-18,
// product owner request; read models reporting/00023): Members by Tier
// (members, manual classifications, grace, spend and movement up / down per
// tier) and Tier Movement (each evaluation run and manual override with the
// members moved up / down).

import "oneclub/internal/platform/catalog"

var tierReports = []*Report{
	sqlReport("crm.members_by_tier", "Members by Tier Report", "crm",
		"Active loyalty members per tier class at the end of the period with the manual classifications and the members in grace, their spend of the period, and the members moved up into / down into the tier by the evaluations of the period.",
		cols("tier|Tier", "tierCode|Tier Code", "rank|Rank|number", "color|Badge", "members|Members|number", "manual|Manual Classification|number",
			"inGrace|In Grace|number", "spend|Spend (period)|number", "avgSpend|Avg Spend per Member|number", "movedUp|Moved Up Into|number",
			"movedDown|Moved Down Into|number"),
		[]Param{{Key: "tierCode", Label: "Tier Code", Type: "string"}}, 365,
		`WITH s AS (SELECT account_id, sum(amount) FILTER (WHERE kind = 'earned') AS spend FROM reporting.eng_loyalty_ledger
		  WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY account_id)
		SELECT coalesce(m.tier_name, 'No tier') AS "tier", m.tier_code AS "tierCode", coalesce(m.tier_rank, 0) AS "rank", m.tier_color AS "color",
		count(*)::int AS "members", count(*) FILTER (WHERE m.tier_source = 'manual')::int AS "manual",
		count(*) FILTER (WHERE m.grace_until IS NOT NULL)::int AS "inGrace", trim_scale(coalesce(sum(s.spend), 0))::text AS "spend",
		trim_scale(round(coalesce(avg(coalesce(s.spend, 0)), 0), 2))::text AS "avgSpend",
		(SELECT count(*) FROM reporting.crm_tier_evaluations e WHERE e.to_tier IS NOT DISTINCT FROM m.tier_name AND e.outcome = 'upgraded'
		  AND e.evaluated_on BETWEEN $1::date AND $2::date)::int AS "movedUp",
		(SELECT count(*) FROM reporting.crm_tier_evaluations e WHERE e.to_tier IS NOT DISTINCT FROM m.tier_name AND e.outcome = 'downgraded'
		  AND e.evaluated_on BETWEEN $1::date AND $2::date)::int AS "movedDown"
		FROM reporting.crm_tier_members m LEFT JOIN s ON s.account_id = m.account_id
		WHERE $3::text <> '' AND ($4 = '' OR upper(m.tier_code) = upper($4))
		GROUP BY m.tier_name, m.tier_code, m.tier_rank, m.tier_color ORDER BY coalesce(m.tier_rank, 0) DESC, 1`),
	sqlReport("crm.tier_movement", "Tier Movement Report", "crm",
		"Each tier evaluation run and manual classification of the period: members evaluated, moved up, moved down, retained, grace started, in grace and the threshold versions put in force.",
		cols("date|Date", "movement|Movement", "number|Number", "kind|Kind", "accounts|Members|number", "upgraded|Moved Up|number",
			"downgraded|Moved Down|number", "retained|Retained|number", "graceStarted|Grace Started|number", "inGrace|In Grace|number",
			"versionsApplied|Threshold Versions Applied|number"),
		[]Param{{Key: "movement", Label: "Movement", Type: "enum", Enum: []string{"evaluation", "override"}}}, 365,
		`SELECT to_char(moved_on, 'YYYY-MM-DD') AS "date", movement, number, kind, accounts, upgraded, downgraded, retained, grace_started AS "graceStarted",
		in_grace AS "inGrace", versions_applied AS "versionsApplied"
		FROM reporting.crm_tier_movements WHERE moved_on BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR movement = $4)
		ORDER BY moved_on, created_at, number`),
}

// P5TierReports are the reports of the member tier classes (registered by internal/app).
func P5TierReports() []*Report { return tierReports }

// P5TierContribution adds one permission per tier report and grants it to
// the CRM report roles.
func P5TierContribution() catalog.Contribution {
	var perms []catalog.Permission
	roles := map[string][]string{}
	for _, r := range tierReports {
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		for _, role := range reportRoles[r.Module] {
			roles[role] = append(roles[role], r.Permission)
		}
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}
