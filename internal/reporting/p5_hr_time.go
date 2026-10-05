package reporting

// Time & attendance reports of PRD P5 (EP-27 FR-RPT-P5-02, HR Reports of
// Naming Convention §23): Attendance Report, Late & Absence Report,
// Overtime Report, Leave Balance Report and Shift Coverage Report, and the
// HR Performance KPIs Attendance, Overtime, Late Arrivals and Absenteeism
// (FR-RPT-P5-01) on the hr_* views of reporting/00022_p5_hr_time.sql. No pay
// amounts: overtime is reported in hours (salary data stays with payroll).

func init() {
	hr := []string{"hr_admin", "hr_manager", "general_manager"}
	withFinance := append([]string{"finance_manager"}, hr...)
	RegisterP5Report(sqlReport("hris.attendance", "Attendance Report", "hris",
		"Attendance per employee in the period: scheduled days, present, late, early leave, absent, leave, worked and overtime hours.",
		cols("employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "scheduled|Scheduled Days|number", "present|Present|number",
			"late|Late|number", "earlyLeave|Early Leave|number", "absent|Absent|number", "onLeave|On Leave|number", "workedHours|Worked Hours|number",
			"overtimeHours|Overtime Clocked (h)|number", "attendanceRate|Attendance Rate|number"),
		[]Param{{Key: "orgUnit", Label: "Org Unit (code)", Type: "string"}}, 30,
		`SELECT employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit",
		count(*) FILTER (WHERE scheduled_start IS NOT NULL)::int AS "scheduled",
		count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::int AS "present",
		count(*) FILTER (WHERE status = 'late')::int AS "late", count(*) FILTER (WHERE early_leave_minutes > 0)::int AS "earlyLeave",
		count(*) FILTER (WHERE status = 'absent')::int AS "absent", trim_scale(coalesce(sum(leave_fraction), 0))::text AS "onLeave",
		trim_scale(round(sum(worked_minutes) / 60.0, 2))::text AS "workedHours", trim_scale(round(sum(overtime_minutes) / 60.0, 2))::text AS "overtimeHours",
		trim_scale(round(coalesce(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::numeric
		  / nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave', 'absent')), 0), 0), 4))::text AS "attendanceRate"
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR org_unit_code = upper($4))
		GROUP BY employee_no, full_name, org_unit_name ORDER BY org_unit_name NULLS LAST, full_name`), withFinance...)
	RegisterP5Report(sqlReport("hris.late_absence", "Late & Absence Report", "hris",
		"Late arrivals, early leave and absences day by day, with missing clock-outs.",
		cols("date|Date|datetime", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "shift|Shift", "status|Status",
			"lateMinutes|Late (min)|number", "earlyLeaveMinutes|Early Leave (min)|number", "flags|Flags"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"late", "early_leave", "absent"}}}, 30,
		`SELECT work_date AS "date", employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", shift_name AS "shift", status,
		late_minutes AS "lateMinutes", early_leave_minutes AS "earlyLeaveMinutes", array_to_string(flags, ', ') AS "flags"
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		  AND (status IN ('late', 'early_leave', 'absent') OR early_leave_minutes > 0 OR 'missing_out' = ANY (flags))
		  AND ($4 = '' OR status = $4) ORDER BY work_date DESC, full_name`), hr...)
	RegisterP5Report(sqlReport("hris.overtime", "Overtime Report", "hris",
		"Overtime requests of the period with approved, clocked and payable hours and the multiplied hours (PP 35/2021 tiers).",
		cols("date|Date|datetime", "number|Request", "employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "dayKind|Day", "timing|Requested",
			"hours|Hours|number", "actualHours|Clocked|number", "payableHours|Payable|number", "multipliedHours|Multiplied|number", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"submitted", "approved", "rejected", "cancelled"}}}, 30,
		`SELECT work_date AS "date", number, employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", day_kind AS "dayKind", timing,
		trim_scale(hours)::text AS "hours", trim_scale(actual_hours)::text AS "actualHours", trim_scale(payable_hours)::text AS "payableHours",
		trim_scale(multiplied_hours)::text AS "multipliedHours", status
		FROM reporting.hr_overtime WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR status = $4)
		ORDER BY work_date DESC, full_name`), withFinance...)
	RegisterP5Report(sqlReport("hris.leave_balance", "Leave Balance Report", "hris",
		"Leave balances of the year of the To date: entitlement, carry-over (lapsed part), adjustments, used, pending and the balance.",
		cols("employeeNo|Employee No.", "employee|Employee", "orgUnit|Org Unit", "leaveType|Leave Type", "year|Year|number", "entitled|Entitled|number",
			"carriedOver|Carried Over|number", "carriedExpired|Carry-over Lapsed|number", "adjusted|Adjusted|number", "used|Used|number",
			"pending|Pending|number", "balance|Balance|number"), nil, 0,
		`SELECT employee_no AS "employeeNo", full_name AS "employee", org_unit_name AS "orgUnit", leave_type AS "leaveType", year,
		trim_scale(entitled)::text AS "entitled", trim_scale(carried_over)::text AS "carriedOver", trim_scale(carried_expired)::text AS "carriedExpired",
		trim_scale(adjusted)::text AS "adjusted", trim_scale(used)::text AS "used", trim_scale(pending)::text AS "pending", trim_scale(balance)::text AS "balance"
		FROM reporting.hr_leave_balances WHERE year = extract(year FROM $2::date)::int AND employee_status = 'active' AND $1::date IS NOT NULL AND $3::text <> ''
		ORDER BY org_unit_name NULLS LAST, full_name, leave_type`), hr...)
	RegisterP5Report(sqlReport("hris.shift_coverage", "Shift Coverage Report", "hris",
		"Minimum staff of the staffing requirements against the published shifts per day (FR-SCH-03).",
		cols("date|Date|datetime", "orgUnit|Org Unit", "position|Position", "shift|Shift", "required|Required|number", "assigned|Assigned|number",
			"short|Short|number"), nil, 7,
		`SELECT g::date AS "date", r.org_unit_name AS "orgUnit", coalesce(r.position_name, 'Any') AS "position", coalesce(r.shift_code, 'Any') AS "shift",
		r.min_staff AS "required",
		(SELECT count(*) FROM reporting.hr_shift_assignments a WHERE a.org_unit_id = r.org_unit_id AND a.work_date = g::date AND a.kind = 'shift'
		  AND a.status = 'scheduled' AND (r.shift_template_id IS NULL OR a.shift_template_id = r.shift_template_id)
		  AND (r.position_id IS NULL OR a.position_id = r.position_id))::int AS "assigned",
		greatest(r.min_staff - (SELECT count(*) FROM reporting.hr_shift_assignments a WHERE a.org_unit_id = r.org_unit_id AND a.work_date = g::date
		  AND a.kind = 'shift' AND a.status = 'scheduled' AND (r.shift_template_id IS NULL OR a.shift_template_id = r.shift_template_id)
		  AND (r.position_id IS NULL OR a.position_id = r.position_id)), 0)::int AS "short"
		FROM reporting.hr_staffing_requirements r, generate_series($1::date, $2::date, interval '1 day') g
		WHERE extract(isodow FROM g)::int = ANY (r.weekdays) AND $3::text <> ''
		ORDER BY 1, 2, 3, 4`), hr...)

	RegisterHRKPI(HRKPI{Key: "attendance_rate", Label: "Attendance", Unit: "ratio", Kind: KindRate, Module: "hris", Executive: true, Order: 20,
		Report: "hris.attendance", Definition: "Days present (incl. late / early leave) ÷ scheduled working days not on leave",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))::numeric
		/ nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave', 'absent')), 0), 0), 4))::text
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no unit)') AS label, trim_scale(round(coalesce(count(*) FILTER (WHERE status IN ('present', 'late',
		'early_leave'))::numeric / nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave', 'absent')), 0), 0), 4))::text AS value
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "overtime_hours", Label: "Overtime", Unit: "hours", Kind: KindFlow, Direction: "down", Module: "hris", Executive: true,
		Order: 30, Report: "hris.overtime", Definition: "Approved overtime hours payable in the period (capped by attendance)",
		SQL: `SELECT trim_scale(coalesce(sum(payable_hours), 0))::text FROM reporting.hr_overtime WHERE status = 'approved'
		AND work_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		Breakdown: `SELECT coalesce(org_unit_name, '(no unit)') AS label, trim_scale(coalesce(sum(payable_hours), 0))::text AS value FROM reporting.hr_overtime
		WHERE status = 'approved' AND work_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`})
	RegisterHRKPI(HRKPI{Key: "late_rate", Label: "Late Arrivals", Unit: "ratio", Kind: KindRate, Direction: "down", Module: "hris", Order: 25,
		Report: "hris.late_absence", Definition: "Days clocked in late ÷ days clocked in",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status = 'late')::numeric
		/ nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave')), 0), 0), 4))::text
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> ''`})
	RegisterHRKPI(HRKPI{Key: "absenteeism", Label: "Absenteeism", Unit: "ratio", Kind: KindRate, Direction: "down", Module: "hris", Order: 26,
		Report: "hris.late_absence", Definition: "Absent days ÷ scheduled working days not on leave",
		SQL: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE status = 'absent')::numeric
		/ nullif(count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave', 'absent')), 0), 0), 4))::text
		FROM reporting.hr_attendance_days WHERE work_date BETWEEN $1::date AND $2::date AND $3::text <> ''`})
}
