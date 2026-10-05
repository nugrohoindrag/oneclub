package reporting

// Payroll reports of PRD P5 (EP-27 FR-RPT-P5-02/04, EP-15 FR-PPY-04, EP-29):
// Payroll Summary, Payroll Cost (department × component), Headcount Cost,
// Overtime Cost, PPh 21, BPJS, the annual recap per employee and the
// Parallel Run Report, and the HR Performance KPIs Payroll Cost, Payroll
// Cost per Head and Overtime Cost (FR-RPT-P5-01), on the hr_payroll_* views
// of reporting/00027_p5_payroll.sql. Salary reports and KPIs are granted to
// payroll roles only (permission hris.payroll_run.view on the KPIs).

// payrollReportRoles may run the salary reports (FR-RPT-P5-04).
var payrollReportRoles = []string{"hr_manager", "hr_admin", "finance_manager", "general_manager"}

// PayrollKPIPermission restricts the payroll KPIs to payroll roles.
const PayrollKPIPermission = "hris.payroll_run.view"

func init() {
	r := payrollReportRoles
	RegisterP5Report(sqlReport("hris.payroll_summary", "Payroll Summary Report", "hris",
		"Payroll runs whose period ends in the range: employees, gross, BPJS, PPh 21, deductions, net pay and employer cost.",
		cols("number|Run", "name|Name", "runType|Type", "period|Period", "paymentDate|Payment Date|datetime", "status|Status", "headcount|Employees|number",
			"gross|Gross|number", "bpjsEmployee|BPJS (employee)|number", "pph21|PPh 21|number", "otherDeductions|Other Deductions|number", "net|Net Pay|number",
			"bpjsEmployer|BPJS (employer)|number", "employerCost|Employer Cost|number"),
		[]Param{{Key: "runType", Label: "Run Type", Type: "enum", Enum: []string{"regular", "thr", "bonus", "adjustment", "final_settlement"}}}, 92,
		`SELECT number, name, run_type AS "runType", period_code AS "period", payment_date AS "paymentDate", status, headcount, gross::text AS gross,
		bpjs_employee::text AS "bpjsEmployee", pph21::text AS pph21, other_deductions::text AS "otherDeductions", net::text AS net,
		bpjs_employer::text AS "bpjsEmployer", employer_cost::text AS "employerCost"
		FROM reporting.hr_payroll_runs WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR run_type = $4)
		ORDER BY period_code DESC, number DESC`), r...)
	RegisterP5Report(sqlReport("hris.payroll_cost", "Payroll Cost Report", "hris",
		"Payroll cost per department and component (gross pay, pre-tax deductions as negative cost, BPJS employer contributions).",
		cols("orgUnit|Department", "costCenter|Cost Center", "component|Component", "name|Name", "category|Category", "employees|Employees|number",
			"amount|Amount|number"), nil, 92,
		`SELECT coalesce(org_unit_name, '(no department)') AS "orgUnit", coalesce(cost_center, '') AS "costCenter", code AS "component", min(name) AS name,
		category, count(DISTINCT employee_id)::int AS employees,
		sum(CASE WHEN kind = 'deduction' THEN -amount ELSE amount END)::text AS amount
		FROM reporting.hr_payroll_lines WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
		  AND (kind IN ('earning', 'bpjs_employer') OR (kind = 'deduction' AND pre_tax))
		GROUP BY 1, 2, 3, 5 ORDER BY 1, 3`), r...)
	RegisterP5Report(sqlReport("hris.payroll_headcount_cost", "Headcount Cost Report", "hris",
		"Employees paid, gross, BPJS employer contributions, employer cost and cost per head per department.",
		cols("orgUnit|Department", "headcount|Employees|number", "gross|Gross|number", "bpjsEmployer|BPJS (employer)|number",
			"employerCost|Employer Cost|number", "costPerHead|Cost per Head|number"), nil, 31,
		`SELECT coalesce(org_unit_name, '(no department)') AS "orgUnit", count(DISTINCT employee_id)::int AS headcount, sum(gross)::text AS gross,
		sum(bpjs_employer)::text AS "bpjsEmployer", sum(employer_cost)::text AS "employerCost",
		round(sum(employer_cost) / nullif(count(DISTINCT employee_id), 0), 0)::text AS "costPerHead"
		FROM reporting.hr_payroll_slips WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`), r...)
	RegisterP5Report(sqlReport("hris.overtime_cost", "Overtime Cost Report", "hris",
		"Overtime paid per employee: multiplied hours (PP 35/2021 tiers) and amount (1/173 of the monthly wage per hour).",
		cols("period|Period", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Department", "multipliedHours|Multiplied Hours|number",
			"amount|Amount|number"), nil, 92,
		`SELECT period_code AS "period", employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit",
		trim_scale(sum(quantity))::text AS "multipliedHours", sum(amount)::text AS amount
		FROM reporting.hr_payroll_lines WHERE code = 'OVERTIME' AND kind = 'earning' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2, 3, 4 ORDER BY 1 DESC, 3`), r...)
	RegisterP5Report(sqlReport("hris.withholding_tax", "PPh 21 Report", "hris",
		"PPh 21 per employee and period: PTKP, TER category and rate or annual calculation, taxable gross, tax withheld and final tax on severance.",
		cols("period|Period", "employeeNo|Employee No.", "employee|Employee", "ptkp|PTKP", "terCategory|TER", "method|Method", "terRate|TER Rate (%)|number",
			"taxableGross|Taxable Gross|number", "pph21|PPh 21|number", "pph21Final|PPh 21 Final|number"), nil, 31,
		`SELECT period_code AS "period", employee_no AS "employeeNo", full_name AS "employee", coalesce(min(ptkp_status), 'TK/0') AS ptkp,
		min(ter_category) AS "terCategory", max(tax_method) AS method, trim_scale(max(ter_rate))::text AS "terRate", sum(taxable_gross)::text AS "taxableGross",
		sum(pph21)::text AS pph21, sum(pph21_final)::text AS "pph21Final"
		FROM reporting.hr_payroll_slips WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2, 3 ORDER BY 1 DESC, 3`), r...)
	RegisterP5Report(sqlReport("hris.bpjs", "BPJS Report", "hris",
		"BPJS Kesehatan and Ketenagakerjaan (JHT, JP, JKK, JKM) contributions per employee and period, employer and employee shares.",
		cols("period|Period", "employeeNo|Employee No.", "employee|Employee", "kesehatanEmployer|Kesehatan (employer)|number",
			"kesehatanEmployee|Kesehatan (employee)|number", "jhtEmployer|JHT (employer)|number", "jhtEmployee|JHT (employee)|number",
			"jpEmployer|JP (employer)|number", "jpEmployee|JP (employee)|number", "jkk|JKK|number", "jkm|JKM|number", "total|Total|number"), nil, 31,
		`SELECT period_code AS "period", employee_no AS "employeeNo", full_name AS "employee",
		coalesce(sum(amount) FILTER (WHERE code = 'BPJS_KESEHATAN_ER'), 0)::text AS "kesehatanEmployer",
		coalesce(sum(amount) FILTER (WHERE code = 'BPJS_KESEHATAN_EE'), 0)::text AS "kesehatanEmployee",
		coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JHT_ER'), 0)::text AS "jhtEmployer", coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JHT_EE'), 0)::text AS "jhtEmployee",
		coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JP_ER'), 0)::text AS "jpEmployer", coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JP_EE'), 0)::text AS "jpEmployee",
		coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JKK_ER'), 0)::text AS jkk, coalesce(sum(amount) FILTER (WHERE code = 'BPJS_JKM_ER'), 0)::text AS jkm,
		sum(amount)::text AS total
		FROM reporting.hr_payroll_lines WHERE kind IN ('bpjs_employee', 'bpjs_employer') AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2, 3 ORDER BY 1 DESC, 3`), r...)
	RegisterP5Report(sqlReport("hris.payroll_annual", "Annual Payroll Recap Report", "hris",
		"Recap per employee of the payroll runs in the range (FR-PPY-04): periods paid, gross, BPJS, PPh 21, net pay and employer cost.",
		cols("employeeNo|Employee No.", "employee|Employee", "orgUnit|Department", "periods|Periods|number", "gross|Gross|number",
			"bpjsEmployee|BPJS (employee)|number", "pph21|PPh 21|number", "net|Net Pay|number", "employerCost|Employer Cost|number"), nil, 365,
		`SELECT employee_no AS "employeeNo", full_name AS "employee", min(org_unit_name) AS "orgUnit", count(DISTINCT period_code)::int AS periods,
		sum(gross)::text AS gross, sum(bpjs_employee)::text AS "bpjsEmployee", sum(pph21 + pph21_final)::text AS pph21, sum(net)::text AS net,
		sum(employer_cost)::text AS "employerCost"
		FROM reporting.hr_payroll_slips WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1, 2 ORDER BY 2`), r...)
	RegisterP5Report(sqlReport("hris.payroll_parallel_run", "Parallel Run Report", "hris",
		"Parallel run of a period (EP-28): the OneClub regular run against the imported legacy payroll per employee and component, with differences.",
		cols("employeeNo|Employee No.", "employee|Employee", "component|Component", "oneClub|OneClub|number", "legacy|Legacy|number",
			"difference|Difference|number", "note|Note"),
		[]Param{{Key: "periodCode", Label: "Period (YYYY-MM)", Type: "string"}, {Key: "show", Label: "Show", Type: "enum", Enum: []string{"differences", "all"}}},
		31,
		`SELECT employee_no AS "employeeNo", full_name AS "employee", component_code AS "component", oneclub_amount::text AS "oneClub",
		legacy_amount::text AS legacy, difference::text AS difference,
		CASE WHEN missing_in_oneclub THEN 'missing in OneClub' WHEN missing_in_legacy THEN 'missing in legacy' ELSE '' END AS note
		FROM reporting.hr_payroll_parallel WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		  AND period_code = coalesce(nullif($4, ''), to_char($2::date, 'YYYY-MM')) AND ($5 = 'all' OR difference <> 0)
		ORDER BY full_name, component_code`), r...)

	RegisterHRKPI(HRKPI{Key: "payroll_cost", Label: "Payroll Cost", Unit: "idr", Kind: KindFlow, Direction: "down", Module: "hris", Executive: true,
		Order: 40, Permission: PayrollKPIPermission, Report: "hris.payroll_cost",
		Definition: "Gross pay + BPJS employer contributions of the approved payroll runs whose period ends in the range",
		SQL:        `SELECT coalesce(sum(employer_cost), 0)::text FROM reporting.hr_payroll_runs WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no department)') AS label, sum(employer_cost)::text AS value FROM reporting.hr_payroll_slips
		WHERE period_end BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "payroll_cost_per_head", Label: "Payroll Cost per Head", Unit: "idr", Kind: KindRate, Direction: "down", Module: "hris",
		Order: 41, Permission: PayrollKPIPermission, Report: "hris.payroll_headcount_cost",
		Definition: "Employer cost of the regular payroll runs ÷ employees paid",
		SQL: `SELECT coalesce(round(sum(employer_cost) / nullif(count(DISTINCT employee_id), 0), 0), 0)::text FROM reporting.hr_payroll_slips
		WHERE run_type = 'regular' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no department)') AS label, round(sum(employer_cost) / nullif(count(DISTINCT employee_id), 0), 0)::text AS value
		FROM reporting.hr_payroll_slips WHERE run_type = 'regular' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "overtime_cost", Label: "Overtime Cost", Unit: "idr", Kind: KindFlow, Direction: "down", Module: "hris", Order: 42,
		Permission: PayrollKPIPermission, Report: "hris.overtime_cost", Definition: "Overtime paid by the approved payroll runs",
		SQL: `SELECT coalesce(sum(amount), 0)::text FROM reporting.hr_payroll_lines WHERE code = 'OVERTIME' AND kind = 'earning'
		AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no department)') AS label, sum(amount)::text AS value FROM reporting.hr_payroll_lines
		WHERE code = 'OVERTIME' AND kind = 'earning' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
}
