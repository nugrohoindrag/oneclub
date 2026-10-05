package reporting

// Core HR reports of PRD P5 (EP-27 FR-RPT-P5-02, HR Reports of Naming
// Convention §23): Headcount Report, Turnover Report, Contract Expiry
// Report, Certification Expiry Report and Training Report on the hr_*
// reporting views (db/migrations/reporting/00018_p5_hr_core.sql). Salary
// and identity data never reach these views (FR-HR-03, FR-RPT-P5-04).

import "oneclub/internal/platform/catalog"

var hrCoreReports = []*Report{
	sqlReport("hris.headcount", "Headcount Report", "hris",
		"Employees per org unit on the To date by employment status and worker category, with joiners and leavers of the period.",
		cols("orgUnit|Org Unit", "active|Active|number", "probation|Probation|number", "contract|Contract|number", "permanent|Permanent|number",
			"daily|Daily Workers|number", "interns|Interns|number", "joined|Joined|number", "left|Left|number"),
		[]Param{{Key: "workerCategory", Label: "Worker Category", Type: "enum", Enum: []string{"regular", "daily", "intern"}}}, 30,
		`SELECT coalesce(org_unit_name, '(no unit)') AS "orgUnit",
		count(*) FILTER (WHERE a)::int AS "active",
		count(*) FILTER (WHERE a AND employment_status = 'probation')::int AS "probation",
		count(*) FILTER (WHERE a AND employment_status = 'contract')::int AS "contract",
		count(*) FILTER (WHERE a AND employment_status = 'permanent')::int AS "permanent",
		count(*) FILTER (WHERE a AND worker_category = 'daily')::int AS "daily",
		count(*) FILTER (WHERE a AND worker_category = 'intern')::int AS "interns",
		count(*) FILTER (WHERE join_date BETWEEN $1::date AND $2::date)::int AS "joined",
		count(*) FILTER (WHERE termination_date - 1 BETWEEN $1::date AND $2::date AND termination_status = 'completed')::int AS "left"
		FROM (SELECT *, (join_date IS NULL OR join_date <= $2::date) AND (termination_date IS NULL OR termination_date > $2::date)
		        AND (status = 'active' OR termination_date IS NOT NULL) AS a
		      FROM reporting.hr_employees WHERE archived_at IS NULL AND $3::text <> '' AND ($4 = '' OR worker_category = $4)) x
		GROUP BY org_unit_name ORDER BY org_unit_name NULLS LAST`),
	sqlReport("hris.turnover", "Turnover Report", "hris",
		"Leavers of the period per org unit by type, average headcount and turnover rate (leavers ÷ average headcount).",
		cols("orgUnit|Org Unit", "headcountStart|Headcount at Start|number", "headcountEnd|Headcount at End|number", "leavers|Leavers|number",
			"resigned|Resigned|number", "terminated|Terminated|number", "contractEnded|Contract Ended|number", "other|Retired / Deceased|number",
			"turnoverRate|Turnover Rate|number"), nil, 90,
		`WITH x AS (SELECT coalesce(org_unit_name, '(no unit)') AS unit, join_date, termination_date, termination_type, termination_status
		  FROM reporting.hr_employees WHERE archived_at IS NULL AND $3::text <> '')
		SELECT unit AS "orgUnit",
		count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $1::date) AND (termination_date IS NULL OR termination_date > $1::date))::int AS "headcountStart",
		count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $2::date) AND (termination_date IS NULL OR termination_date > $2::date))::int AS "headcountEnd",
		count(*) FILTER (WHERE l)::int AS "leavers",
		count(*) FILTER (WHERE l AND termination_type = 'resigned')::int AS "resigned",
		count(*) FILTER (WHERE l AND termination_type = 'terminated')::int AS "terminated",
		count(*) FILTER (WHERE l AND termination_type = 'contract_ended')::int AS "contractEnded",
		count(*) FILTER (WHERE l AND termination_type IN ('retired', 'deceased'))::int AS "other",
		trim_scale(round(coalesce(count(*) FILTER (WHERE l)::numeric / nullif((
		  count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $1::date) AND (termination_date IS NULL OR termination_date > $1::date)) +
		  count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $2::date) AND (termination_date IS NULL OR termination_date > $2::date)))::numeric / 2, 0), 0), 4))::text
		  AS "turnoverRate"
		FROM (SELECT *, termination_status = 'completed' AND termination_date - 1 BETWEEN $1::date AND $2::date AS l FROM x) y
		GROUP BY unit ORDER BY unit`),
	sqlReport("hris.contract_expiry", "Contract Expiry Report", "hris",
		"PKWT contracts ending between From and To + days ahead (default 60), with renewal status.",
		cols("number|Contract", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "position|Position", "contractType|Type",
			"sequence|Contract No.|number", "startDate|Start|datetime", "endDate|End|datetime", "daysLeft|Days Left|number", "status|Status"),
		[]Param{{Key: "daysAhead", Label: "Days Ahead", Type: "string"}}, 0,
		`SELECT number, employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", position_name AS "position",
		upper(contract_type) AS "contractType", sequence_no AS "sequence", start_date AS "startDate", coalesce(ended_on, end_date) AS "endDate",
		(coalesce(ended_on, end_date) - (now() AT TIME ZONE $3)::date) AS "daysLeft", status
		FROM reporting.hr_contracts WHERE contract_type = 'pkwt' AND status IN ('active', 'expiring', 'ended', 'renewed') AND employee_status = 'active'
		  AND coalesce(ended_on, end_date) BETWEEN $1::date AND $2::date + coalesce(nullif($4, '')::int, 60)
		ORDER BY coalesce(ended_on, end_date), full_name`),
	sqlReport("hris.certification_expiry", "Certification Expiry Report", "hris",
		"Certificates of employees, caddies and instructors expired or expiring between From and To + days ahead (default 60).",
		cols("type|Certification", "holder|Holder", "holderKind|Holder Type", "orgUnit|Org Unit", "certificateNo|Certificate No.", "issuedOn|Issued|datetime",
			"expiresOn|Expires|datetime", "daysLeft|Days Left|number", "mandatoryFor|Mandatory For", "status|Status"),
		[]Param{{Key: "daysAhead", Label: "Days Ahead", Type: "string"},
			{Key: "holderKind", Label: "Holder Type", Type: "enum", Enum: []string{"employee", "caddy", "instructor"}}}, 30,
		`SELECT type_name AS "type", holder_name AS "holder", holder_kind AS "holderKind", org_unit_name AS "orgUnit", certificate_no AS "certificateNo",
		issued_on AS "issuedOn", expires_on AS "expiresOn", (expires_on - (now() AT TIME ZONE $3)::date) AS "daysLeft",
		array_to_string(mandatory_for, ', ') AS "mandatoryFor", status
		FROM reporting.hr_certifications WHERE archived_at IS NULL AND status IN ('active', 'expired') AND expires_on IS NOT NULL
		  AND expires_on BETWEEN $1::date AND $2::date + coalesce(nullif($4, '')::int, 60) AND ($5 = '' OR holder_kind = $5)
		ORDER BY expires_on, holder_name`),
	sqlReport("hris.training", "Training Report", "hris",
		"Training sessions of the period with participants, attendance, results and cost.",
		cols("date|Date|datetime", "program|Program", "category|Category", "session|Session", "status|Status", "participants|Participants|number",
			"attended|Attended|number", "passed|Passed|number", "failed|Failed|number", "cost|Cost|number"),
		[]Param{{Key: "category", Label: "Category", Type: "enum", Enum: []string{"mandatory", "safety", "service", "technical", "leadership", "compliance", "other"}}}, 90,
		`SELECT (min(starts_at) AT TIME ZONE $3)::date AS "date", program_name AS "program", category, title AS "session", session_status AS "status",
		count(*)::int AS "participants", count(*) FILTER (WHERE attendance = 'attended')::int AS "attended",
		count(*) FILTER (WHERE result = 'passed')::int AS "passed", count(*) FILTER (WHERE result = 'failed')::int AS "failed",
		trim_scale(coalesce(max(cost_total), max(cost_per_participant) * count(*) FILTER (WHERE attendance = 'attended')))::text AS "cost"
		FROM reporting.hr_training WHERE (starts_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR category = $4)
		GROUP BY session_id, program_name, category, title, session_status ORDER BY min(starts_at), title`),
}

// HRCoreReports are the core HR reports (wired by internal/app).
func HRCoreReports() []*Report { return hrCoreReports }

// HRCoreContribution adds one permission per core HR report and grants it
// with the reporting views to the HR roles and the General Manager.
func HRCoreContribution() catalog.Contribution {
	var perms []catalog.Permission
	roles := map[string][]string{}
	for _, r := range hrCoreReports {
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		for _, role := range []string{"hr_admin", "hr_manager", "general_manager"} {
			roles[role] = append(roles[role], r.Permission, "reporting.report.view", "reporting.export.create")
		}
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}

// HRCoreKPI is an HR Performance KPI of core HR (FR-RPT-P5-01), in the
// shape of the BI area's HRKPI registry (RegisterHRKPI, P5 BI on staging):
// once both are merged, internal/app registers each definition there —
// the keys replace the BI defaults of the same name (headcount from the P0
// master today). SQL follows the dashboard convention: one row, one text
// column; $1 from, $2 to (inclusive), $3 time zone.
type HRCoreKPI struct {
	Key, Label, Unit, Kind, Direction, Definition, SQL, Breakdown, Report string
	Executive                                                             bool
}

// HRCoreKPIs are Headcount, Turnover and Certification Compliance on the
// hr_* reporting views.
var HRCoreKPIs = []HRCoreKPI{
	{Key: "headcount", Label: "Headcount", Unit: "count", Kind: "current", Executive: true, Report: "hris.headcount",
		Definition: "Active employees of the HRIS employee master (joined, not yet left)",
		SQL: `SELECT count(*)::text FROM reporting.hr_employees WHERE archived_at IS NULL AND status = 'active'
		AND (join_date IS NULL OR join_date <= $2::date) AND $1::date IS NOT NULL AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no unit)') AS label, count(*)::text AS value FROM reporting.hr_employees WHERE archived_at IS NULL
		AND status = 'active' AND (join_date IS NULL OR join_date <= $2::date) AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{Key: "turnover", Label: "Turnover", Unit: "ratio", Kind: "rate", Direction: "down", Report: "hris.turnover",
		Definition: "Employees who left in the period ÷ average headcount (start and end of the period)",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE termination_status = 'completed' AND termination_date - 1 BETWEEN $1::date AND $2::date)::numeric
		/ nullif((count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $1::date) AND (termination_date IS NULL OR termination_date > $1::date))
		+ count(*) FILTER (WHERE (join_date IS NULL OR join_date <= $2::date) AND (termination_date IS NULL OR termination_date > $2::date)))::numeric / 2, 0), 0), 4))::text
		FROM reporting.hr_employees WHERE archived_at IS NULL AND $3::text <> ''`},
	{Key: "certification_compliance", Label: "Certification Compliance", Unit: "ratio", Kind: "current", Report: "hris.certification_expiry",
		Definition: "Employees in certified positions holding every required valid certification ÷ employees in those positions",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE ok)::numeric / nullif(count(*), 0), 1), 4))::text FROM (
		SELECT employee_id, bool_and(valid) AS ok FROM reporting.hr_certification_requirements WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL
		AND $3::text <> '' GROUP BY employee_id) x`,
		Breakdown: `SELECT type_name AS label, count(*) FILTER (WHERE NOT valid)::text AS value FROM reporting.hr_certification_requirements
		WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
}
