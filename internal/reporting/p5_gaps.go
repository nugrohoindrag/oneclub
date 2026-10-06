package reporting

// HR datasets of the self-service report builder (PRD P5 FR-BI-06 with
// EP-27): attendance days, overtime requests and leave requests on the hr_*
// read models. Each dataset reuses the permission of its HR report
// (contributed by the BI registry), so a user who may run the Attendance
// Report may also slice attendance in the builder. No pay amounts: salary
// data stays with payroll (FR-RPT-P5-04).

func init() {
	org := DatasetField{Key: "org_unit", Label: "Org Unit", Type: "string", Expr: "coalesce(org_unit_name, '(no unit)')"}
	RegisterDataset(Dataset{Code: "hr_attendance", Name: "Attendance Days", Module: "hris", Permission: "reporting.hris_attendance.view",
		Description: "Attendance days per day, org unit, shift, status and day kind (scheduled, present, late, absent, worked and overtime hours)",
		Source:      "reporting.hr_attendance_days", DateExpr: "work_date",
		Dimensions: []DatasetField{{Key: "day", Label: "Day", Type: "date", Expr: "work_date"},
			{Key: "month", Label: "Month", Type: "string", Expr: "to_char(work_date, 'YYYY-MM')"}, org,
			{Key: "shift", Label: "Shift", Type: "string", Expr: "coalesce(shift_name, '')"},
			{Key: "status", Label: "Status", Type: "string", Expr: "status"}, {Key: "day_kind", Label: "Day Kind", Type: "string", Expr: "coalesce(day_kind, '')"}},
		Metrics: []DatasetField{{Key: "days", Label: "Days", Type: "number", Expr: "count(*)"},
			{Key: "scheduled", Label: "Scheduled Days", Type: "number", Expr: "count(*) FILTER (WHERE scheduled_start IS NOT NULL)"},
			{Key: "present", Label: "Present", Type: "number", Expr: "count(*) FILTER (WHERE status IN ('present', 'late', 'early_leave'))"},
			{Key: "late", Label: "Late", Type: "number", Expr: "count(*) FILTER (WHERE status = 'late')"},
			{Key: "absent", Label: "Absent", Type: "number", Expr: "count(*) FILTER (WHERE status = 'absent')"},
			{Key: "worked_hours", Label: "Worked Hours", Type: "number", Expr: "trim_scale(round(sum(worked_minutes) / 60.0, 2))"},
			{Key: "overtime_hours", Label: "Overtime Clocked (h)", Type: "number", Expr: "trim_scale(round(sum(overtime_minutes) / 60.0, 2))"},
			{Key: "late_minutes", Label: "Late Minutes", Type: "number", Expr: "sum(late_minutes)"}}})
	RegisterDataset(Dataset{Code: "hr_overtime", Name: "Overtime Requests", Module: "hris", Permission: "reporting.hris_overtime.view",
		Description: "Overtime requests per work day, org unit, day kind and status (requested, payable and multiplied hours)",
		Source:      "reporting.hr_overtime", DateExpr: "work_date",
		Dimensions: []DatasetField{{Key: "day", Label: "Day", Type: "date", Expr: "work_date"},
			{Key: "month", Label: "Month", Type: "string", Expr: "to_char(work_date, 'YYYY-MM')"}, org,
			{Key: "day_kind", Label: "Day Kind", Type: "string", Expr: "coalesce(day_kind, '')"}, {Key: "status", Label: "Status", Type: "string", Expr: "status"}},
		Metrics: []DatasetField{{Key: "requests", Label: "Requests", Type: "number", Expr: "count(*)"},
			{Key: "hours", Label: "Requested Hours", Type: "number", Expr: "trim_scale(sum(hours))"},
			{Key: "payable_hours", Label: "Payable Hours", Type: "number", Expr: "trim_scale(coalesce(sum(payable_hours), 0))"},
			{Key: "multiplied_hours", Label: "Multiplied Hours", Type: "number", Expr: "trim_scale(coalesce(sum(multiplied_hours), 0))"}}})
	RegisterDataset(Dataset{Code: "hr_leave", Name: "Leave Requests", Module: "hris", Permission: "reporting.hris_leave_balance.view",
		Description: "Leave requests per start date, org unit, leave type, paid / unpaid and status (requests and days)",
		Source:      "reporting.hr_leave_requests", DateExpr: "start_date",
		Dimensions: []DatasetField{{Key: "day", Label: "Start Date", Type: "date", Expr: "start_date"},
			{Key: "month", Label: "Month", Type: "string", Expr: "to_char(start_date, 'YYYY-MM')"}, org,
			{Key: "leave_type", Label: "Leave Type", Type: "string", Expr: "leave_type"},
			{Key: "paid", Label: "Paid", Type: "string", Expr: "CASE WHEN paid THEN 'paid' ELSE 'unpaid' END"},
			{Key: "status", Label: "Status", Type: "string", Expr: "status"}},
		Metrics: []DatasetField{{Key: "requests", Label: "Requests", Type: "number", Expr: "count(*)"},
			{Key: "days", Label: "Days", Type: "number", Expr: "trim_scale(sum(days))"}}})
}
