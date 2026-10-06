package reporting

// Payout reports of PRD P5 (EP-27 FR-RPT-P5-02, HR Reports of Naming
// Convention §23): Service Charge Distribution Report, Caddy Payout Report,
// Instructor Payout Report and Commission Payout Report on the hr_* views
// of reporting/00028_p5_payouts.sql, for HR, Finance and the General
// Manager (FR-RPT-P5-04). Bank accounts and tax ids are not reported.

func init() {
	roles := []string{"hr_manager", "finance_manager", "general_manager"}
	RegisterP5Report(sqlReport("hris.service_charge_distribution", "Service Charge Distribution Report", "hris",
		"Service charge per employee of the distributions whose pool month is in the period: eligibility, attendance factor, base share, "+
			"redistribution and the amount paid with payroll.",
		cols("period|Pool Month|datetime", "number|Distribution", "status|Status", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit",
			"grade|Grade", "eligible|Eligible", "reason|Not Eligible Because", "factor|Attendance Factor|number", "baseShare|Base Share|money",
			"attendanceAmount|After Attendance|money", "redistributed|Redistributed|money", "amount|Amount|money", "paid|Paid with Payroll"),
		[]Param{{Key: "eligible", Label: "Eligible", Type: "enum", Enum: []string{"yes", "no"}}}, 92,
		`SELECT period AS "period", number, status, employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", grade_code AS "grade",
		CASE WHEN eligible THEN 'yes' ELSE 'no' END AS "eligible", exclusion_reason AS "reason", trim_scale(attendance_factor)::text AS "factor",
		trim_scale(base_share)::text AS "baseShare", trim_scale(attendance_amount)::text AS "attendanceAmount",
		trim_scale(redistributed)::text AS "redistributed", trim_scale(amount)::text AS "amount", CASE WHEN consumed_at IS NULL THEN 'no' ELSE 'yes' END AS "paid"
		FROM reporting.hr_service_charge_lines WHERE period BETWEEN date_trunc('month', $1::date)::date AND $2::date AND $3::text <> ''
		  AND ($4 = '' OR eligible = ($4 = 'yes'))
		ORDER BY period DESC, eligible DESC, org_unit_name NULLS LAST, full_name`), roles...)
	for _, k := range []struct{ kind, code, name, unit string }{
		{"caddy", "hris.caddy_payout", "Caddy Payout Report", "Rounds"},
		{"instructor", "hris.instructor_payout", "Instructor Payout Report", "Sessions"},
	} {
		RegisterP5Report(sqlReport(k.code, k.name, "hris",
			"Partner payout lines of the runs whose period ends in the period: gross, settlement deductions, PPh 21 non-employee, BPJS BPU, "+
				"deductions and net, with the run status.",
			cols("periodEnd|Period End|datetime", "number|Payout Run", "status|Status", "partnerCode|Code", "partner|Partner", "units|"+k.unit+"|number",
				"gross|Gross|money", "settlementDeductions|Settlement Deductions|money", "pph21|PPh 21|money", "bpu|BPJS BPU|money",
				"otherDeductions|Deductions|money", "net|Net|money", "method|Payment"),
			[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"calculated", "pending_approval", "approved", "paid"}}}, 31,
			`SELECT period_end AS "periodEnd", number, run_status AS "status", partner_code AS "partnerCode", partner_name AS "partner", units,
			trim_scale(gross)::text AS "gross", trim_scale(source_deductions)::text AS "settlementDeductions", trim_scale(pph21)::text AS "pph21",
			trim_scale(bpu)::text AS "bpu", trim_scale(other_deductions)::text AS "otherDeductions", trim_scale(net)::text AS "net",
			payment_method AS "method"
			FROM reporting.hr_payout_lines WHERE kind = '`+k.kind+`' AND period_end BETWEEN $1::date AND $2::date AND $3::text <> ''
			  AND ($4 = '' OR run_status = $4) ORDER BY period_end DESC, number, partner_name`), roles...)
	}
	RegisterP5Report(sqlReport("hris.commission_payout", "Commission Payout Report", "hris",
		"Approved commission statements paid with payroll: earned, clawback, adjustments, the payroll earning and deduction, and whether it is paid.",
		cols("period|Commission Month", "number|Statement", "employeeNo|Employee No.", "employee|Employee", "earned|Earned|money",
			"clawback|Clawback|money", "adjustments|Adjustments|money", "earning|Payroll Earning|money", "deduction|Payroll Deduction|money",
			"total|Net|money", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"unmatched", "ready", "paid"}}}, 92,
		`SELECT period, number, employee_no AS "employeeNo", coalesce(full_name, user_name) AS "employee", trim_scale(earned)::text AS "earned",
		trim_scale(clawback)::text AS "clawback", trim_scale(adjustments)::text AS "adjustments", trim_scale(earning)::text AS "earning",
		trim_scale(deduction)::text AS "deduction", trim_scale(total)::text AS "total", status
		FROM reporting.hr_commission_payouts WHERE period_start BETWEEN date_trunc('month', $1::date)::date AND $2::date AND $3::text <> ''
		  AND ($4 = '' OR status = $4) ORDER BY period DESC, number`), roles...)
}
