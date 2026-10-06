package reporting

// Reports and KPI dashboards of tournaments (EP-16): the Tournament Report
// (FR-TRN-12: participants, results, revenue from fees, packages and
// sponsors, prize cost) and the tournament KPIs of Golf Performance
// (FR-RPT-P3-04: number of tournaments, participants, tournament and
// sponsor revenue) on the golf_tournament* reporting views.

var tournamentReports = []*Report{
	sqlReport("golf.tournament", "Tournament Report", "golf",
		"Tournaments played in the period: field, champions, revenue from tournament fees, packages and sponsors, corporate contract value and prize cost.",
		cols("code|Code", "name|Tournament", "type|Type", "startDate|Start|datetime", "endDate|End|datetime", "status|Status", "format|Format",
			"participants|Participants|number", "checkedIn|Checked-in|number", "withdrawn|Withdrawn|number", "waitlisted|Waitlisted|number",
			"members|Members|number", "guests|Guests|number", "champion|Champion", "feeRevenue|Tournament Fees|number", "packageRevenue|Packages|number",
			"withdrawalFees|Withdrawal Fees|number", "sponsorRevenue|Sponsorship|number", "corporateValue|Corporate Contract|number",
			"totalRevenue|Total Revenue|number", "prizeCost|Prize Cost|number", "netResult|Net (Revenue − Prizes)|number", "source|Source"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "open", "closed", "in_progress", "completed", "cancelled"}},
			{Key: "type", Label: "Type", Type: "enum", Enum: []string{"club", "club_championship", "corporate", "invitational", "sponsor", "charity"}}}, 90,
		`WITH rev AS (
		  SELECT tournament_id, sum(total) FILTER (WHERE kind = 'fee' AND NOT liability) AS fee, sum(total) FILTER (WHERE kind = 'package' AND NOT liability) AS pkg,
		    sum(total) FILTER (WHERE kind = 'withdrawal') AS wd, sum(total) FILTER (WHERE kind = 'sponsorship') AS sponsor,
		    sum(total) FILTER (WHERE kind = 'corporate' AND NOT liability) AS corporate, sum(total) FILTER (WHERE NOT liability) AS total
		  FROM reporting.golf_tournament_revenue GROUP BY tournament_id),
		prize AS (SELECT tournament_id, sum(value) FILTER (WHERE status <> 'cancelled') AS cost FROM reporting.golf_tournament_prizes GROUP BY tournament_id),
		champ AS (SELECT tournament_id, string_agg(player_name || ' (' || category || coalesce(' ' || score::text, '') || ')', ', ' ORDER BY category) AS names
		  FROM reporting.golf_tournament_champions GROUP BY tournament_id)
		SELECT t.code, t.name, t.tournament_type AS "type", t.start_date AS "startDate", t.end_date AS "endDate", t.status, t.format,
		t.participants, t.checked_in AS "checkedIn", t.withdrawn, t.waitlisted, t.members, t.guests, c.names AS "champion",
		trim_scale(coalesce(r.fee, 0))::text AS "feeRevenue", trim_scale(coalesce(r.pkg, 0))::text AS "packageRevenue",
		trim_scale(coalesce(r.wd, 0))::text AS "withdrawalFees", trim_scale(coalesce(r.sponsor, 0))::text AS "sponsorRevenue",
		trim_scale(coalesce(t.quotation_total, r.corporate, 0))::text AS "corporateValue",
		trim_scale(coalesce(r.total, 0) - coalesce(r.corporate, 0) + coalesce(t.quotation_total, r.corporate, 0))::text AS "totalRevenue",
		trim_scale(coalesce(p.cost, 0))::text AS "prizeCost",
		trim_scale(coalesce(r.total, 0) - coalesce(r.corporate, 0) + coalesce(t.quotation_total, r.corporate, 0) - coalesce(p.cost, 0))::text AS "netResult",
		t.source
		FROM reporting.golf_tournaments t LEFT JOIN rev r ON r.tournament_id = t.tournament_id LEFT JOIN prize p ON p.tournament_id = t.tournament_id
		LEFT JOIN champ c ON c.tournament_id = t.tournament_id
		WHERE t.start_date <= $2::date AND t.end_date >= $1::date AND $3::text <> '' AND ($4 = '' OR t.status = $4) AND ($5 = '' OR t.tournament_type = $5)
		ORDER BY t.start_date, t.code`),
}

// tournamentKPIs are the tournament figures of Golf Performance.
var tournamentKPIs = []kpiDef{
	{key: "tournaments", label: "Tournaments", unit: "count", def: "Tournaments played in the period (not cancelled)",
		sql: `SELECT count(*)::text FROM reporting.golf_tournaments WHERE status <> 'cancelled' AND start_date <= $2::date AND end_date >= $1::date
		AND $3::text <> ''`,
		breakdown: `SELECT tournament_type AS label, count(*)::text AS value FROM reporting.golf_tournaments WHERE status <> 'cancelled'
		AND start_date <= $2::date AND end_date >= $1::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "tournament_participants", label: "Tournament Participants", unit: "count", def: "Registered and checked-in players of the period's tournaments",
		sql: `SELECT coalesce(sum(participants), 0)::text FROM reporting.golf_tournaments WHERE status <> 'cancelled' AND start_date <= $2::date
		AND end_date >= $1::date AND $3::text <> ''`,
		breakdown: `SELECT 'member' AS label, coalesce(sum(members), 0)::text AS value FROM reporting.golf_tournaments WHERE status <> 'cancelled'
		AND start_date <= $2::date AND end_date >= $1::date AND $3::text <> ''
		UNION ALL SELECT 'guest', coalesce(sum(guests), 0)::text FROM reporting.golf_tournaments WHERE status <> 'cancelled'
		AND start_date <= $2::date AND end_date >= $1::date AND $3::text <> ''`},
	{key: "tournament_revenue", label: "Tournament Revenue", unit: "idr", def: "Tournament fees, packages and withdrawal fees posted in the period (club revenue)",
		sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.golf_tournament_revenue WHERE kind IN ('fee', 'package', 'withdrawal', 'corporate')
		AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT kind AS label, trim_scale(sum(total))::text AS value FROM reporting.golf_tournament_revenue WHERE kind IN ('fee', 'package', 'withdrawal', 'corporate')
		AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "sponsor_revenue", label: "Sponsor Revenue", unit: "idr", def: "Sponsorships billed in the period",
		sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.golf_tournament_revenue WHERE kind = 'sponsorship'
		AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
}

func init() {
	d := dashboards["golf-performance"]
	d.kpis = append(append([]kpiDef{}, d.kpis...), tournamentKPIs...)
	dashboards["golf-performance"] = d
}
