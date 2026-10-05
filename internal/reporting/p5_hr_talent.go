package reporting

// Recruitment and performance review reports and KPIs of PRD P5 (EP-27,
// FR-RPT-P5-01/02) on the hr_requisitions, hr_applications and
// hr_performance_reviews views (db/migrations/reporting/00023): Recruitment
// Funnel Report, Time to Hire Report, Performance Review Report and
// Performance Rating Distribution Report; HR Performance KPIs Time to Hire,
// Open Positions, Review Completion and Review Score. No candidate personal
// data or salaries reach these views (FR-HR-03).

// hrTalentRoles may run the recruitment and performance reports.
var hrTalentRoles = []string{"hr_manager", "hr_admin", "general_manager", "property_admin"}

// Recruitment & performance reports.
var (
	RecruitmentFunnelReport = sqlReport("hris.recruitment_funnel", "Recruitment Funnel Report", "hris",
		"Applications received in the period per requisition and how far they went: screening, interview, offer, hire, rejected and withdrawn, "+
			"with the conversion from application to hire.",
		cols("requisition|Requisition", "title|Position", "orgUnit|Org Unit", "status|Requisition Status", "applications|Applications|number",
			"screening|Screened|number", "interview|Interviewed|number", "offered|Offered|number", "hired|Hired|number", "rejected|Rejected|number",
			"withdrawn|Withdrawn|number", "conversion|Application → Hire|number"),
		[]Param{{Key: "source", Label: "Source", Type: "enum", Enum: []string{"website", "referral", "walk_in", "job_portal", "agency", "internal",
			"social_media", "other"}}}, 90,
		`SELECT a.requisition_number AS "requisition", a.requisition_title AS "title", a.org_unit_name AS "orgUnit", r.status,
		count(*)::int AS "applications", count(*) FILTER (WHERE a.reached_screening)::int AS "screening",
		count(*) FILTER (WHERE a.reached_interview)::int AS "interview", count(*) FILTER (WHERE a.reached_offer)::int AS "offered",
		count(*) FILTER (WHERE a.stage = 'hired')::int AS "hired", count(*) FILTER (WHERE a.stage = 'rejected')::int AS "rejected",
		count(*) FILTER (WHERE a.stage = 'withdrawn')::int AS "withdrawn",
		trim_scale(round(count(*) FILTER (WHERE a.stage = 'hired')::numeric / nullif(count(*), 0), 4))::text AS "conversion"
		FROM reporting.hr_applications a JOIN reporting.hr_requisitions r ON r.requisition_id = a.requisition_id
		WHERE (a.applied_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR a.source = $4)
		GROUP BY a.requisition_id, a.requisition_number, a.requisition_title, a.org_unit_name, r.status ORDER BY a.requisition_number`)

	TimeToHireReport = sqlReport("hris.time_to_hire", "Time to Hire Report", "hris",
		"Hires of the period: days from application to hire (time to hire) and from the requisition approval to the hire (time to fill), by source.",
		cols("application|Application", "requisition|Requisition", "title|Position", "orgUnit|Org Unit", "source|Source",
			"appliedOn|Applied|datetime", "hiredOn|Hired|datetime", "timeToHire|Time to Hire (days)|number", "timeToFill|Time to Fill (days)|number"),
		nil, 180,
		`SELECT number AS "application", requisition_number AS "requisition", requisition_title AS "title", org_unit_name AS "orgUnit", source,
		(applied_at AT TIME ZONE $3)::date AS "appliedOn", (hired_at AT TIME ZONE $3)::date AS "hiredOn",
		((hired_at AT TIME ZONE $3)::date - (applied_at AT TIME ZONE $3)::date) AS "timeToHire",
		((hired_at AT TIME ZONE $3)::date - (requisition_approved_at AT TIME ZONE $3)::date) AS "timeToFill"
		FROM reporting.hr_applications WHERE stage = 'hired' AND (hired_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		ORDER BY hired_at, number`)

	PerformanceReviewReport = sqlReport("hris.performance_review", "Performance Review Report", "hris",
		"Reviews of the cycles ending in the period: status, self, manager and final score, rating, recommendation and promotion.",
		cols("cycle|Cycle", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "position|Position", "status|Status",
			"selfScore|Self Score|number", "managerScore|Manager Score|number", "finalScore|Final Score|number", "rating|Rating",
			"recommendation|Recommendation", "promoted|Promoted", "acknowledged|Acknowledged"),
		[]Param{{Key: "cycleType", Label: "Cycle Type", Type: "enum", Enum: []string{"annual", "semester", "probation"}}}, 365,
		`SELECT cycle_name AS "cycle", employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", position_name AS "position", status,
		trim_scale(self_score)::text AS "selfScore", trim_scale(manager_score)::text AS "managerScore", trim_scale(final_score)::text AS "finalScore",
		coalesce(final_rating, recommended_rating) AS "rating", recommendation, CASE WHEN promoted THEN 'yes' ELSE 'no' END AS "promoted",
		CASE WHEN acknowledged_at IS NOT NULL THEN 'yes' ELSE 'no' END AS "acknowledged"
		FROM reporting.hr_performance_reviews WHERE status <> 'cancelled' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
		  AND ($4 = '' OR cycle_type = $4)
		ORDER BY period_end DESC, cycle_name, org_unit_name NULLS LAST, full_name`)

	PerformanceDistributionReport = sqlReport("hris.performance_distribution", "Performance Rating Distribution Report", "hris",
		"Completion and rating distribution of the review cycles ending in the period per org unit (final rating, else the recommended one).",
		cols("cycle|Cycle", "orgUnit|Org Unit", "rating|Rating", "reviews|Reviews|number", "share|Share of Org Unit|number",
			"completion|Completion of Org Unit|number", "averageScore|Average Final Score|number"),
		nil, 365,
		`WITH r AS (SELECT * FROM reporting.hr_performance_reviews WHERE status <> 'cancelled' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''),
		u AS (SELECT cycle_id, coalesce(org_unit_name, '(no unit)') AS unit, count(*) AS total,
		  count(*) FILTER (WHERE status IN ('submitted', 'calibrated', 'completed')) AS done FROM r GROUP BY 1, 2)
		SELECT r.cycle_name AS "cycle", coalesce(r.org_unit_name, '(no unit)') AS "orgUnit", coalesce(r.final_rating, r.recommended_rating, '(not rated)') AS "rating",
		count(*)::int AS "reviews", trim_scale(round(count(*)::numeric / max(u.total), 4))::text AS "share",
		trim_scale(round(max(u.done)::numeric / max(u.total), 4))::text AS "completion", trim_scale(round(avg(r.final_score), 2))::text AS "averageScore"
		FROM r JOIN u ON u.cycle_id = r.cycle_id AND u.unit = coalesce(r.org_unit_name, '(no unit)')
		GROUP BY r.cycle_id, r.cycle_name, 2, 3 ORDER BY r.cycle_name, 2, min(r.final_score) DESC NULLS LAST, 3`)
)

// HRTalentReports are the recruitment and performance reports.
func HRTalentReports() []*Report {
	return []*Report{RecruitmentFunnelReport, TimeToHireReport, PerformanceReviewReport, PerformanceDistributionReport}
}

func init() {
	for _, r := range HRTalentReports() {
		RegisterP5Report(r, hrTalentRoles...)
	}
	RegisterHRKPI(HRKPI{Key: "time_to_hire", Label: "Time to Hire", Unit: "days", Kind: KindRate, Direction: "down", Module: "hris", Order: 90,
		Report: "hris.time_to_hire", Definition: "Average days from application to hire of the hires of the period",
		SQL: `SELECT trim_scale(round(coalesce(avg((hired_at AT TIME ZONE $3)::date - (applied_at AT TIME ZONE $3)::date), 0), 1))::text
		FROM reporting.hr_applications WHERE stage = 'hired' AND (hired_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		Breakdown: `SELECT source AS label, trim_scale(round(avg((hired_at AT TIME ZONE $3)::date - (applied_at AT TIME ZONE $3)::date), 1))::text AS value
		FROM reporting.hr_applications WHERE stage = 'hired' AND (hired_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "open_positions", Label: "Open Positions", Unit: "count", Kind: KindCurrent, Module: "hris", Order: 95,
		Report: "hris.recruitment_funnel", Definition: "Positions still to fill on the open job requisitions (headcount − hired)",
		SQL: `SELECT coalesce(sum(headcount - hired_count), 0)::text FROM reporting.hr_requisitions WHERE status IN ('open', 'on_hold')
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
		Breakdown: `SELECT org_unit_name AS label, sum(headcount - hired_count)::text AS value FROM reporting.hr_requisitions WHERE status IN ('open', 'on_hold')
		AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "review_completion", Label: "Review Completion", Unit: "ratio", Kind: KindRate, Module: "hris", Order: 100,
		Report:     "hris.performance_distribution",
		Definition: "Performance reviews submitted, calibrated or completed ÷ reviews of the cycles running or ending in the period",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status IN ('submitted', 'calibrated', 'completed'))::numeric / nullif(count(*), 0), 0), 4))::text
		FROM reporting.hr_performance_reviews WHERE status <> 'cancelled' AND cycle_status <> 'draft' AND period_start <= $2::date AND period_end >= $1::date
		AND $3::text <> ''`,
		Breakdown: `SELECT status AS label, count(*)::text AS value FROM reporting.hr_performance_reviews WHERE status <> 'cancelled' AND cycle_status <> 'draft'
		AND period_start <= $2::date AND period_end >= $1::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "review_score", Label: "Review Score", Unit: "points", Kind: KindRate, Module: "hris", Order: 105,
		Report: "hris.performance_review", Definition: "Average final score of the reviews completed in the period; breakdown: rating distribution",
		SQL: `SELECT trim_scale(round(coalesce(avg(final_score), 0), 2))::text FROM reporting.hr_performance_reviews WHERE status = 'completed'
		AND (completed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		Breakdown: `SELECT final_rating AS label, count(*)::text AS value FROM reporting.hr_performance_reviews WHERE status = 'completed'
		AND (completed_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`})
}
