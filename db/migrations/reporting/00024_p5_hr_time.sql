-- PRD P5 time & attendance read models (EP-27 FR-RPT-P5-01/02: Attendance,
-- Late & Absence, Overtime, Leave Balance and Shift Coverage reports; the
-- Attendance and Overtime KPIs of HR Performance). No GPS positions, PINs or
-- pay amounts. Views use security_invoker so RLS of the sources applies.

-- +goose Up
CREATE VIEW reporting.hr_attendance_days WITH (security_invoker = true) AS
SELECT d.id AS attendance_day_id, d.property_id, d.employee_id, e.employee_no, e.full_name, e.org_unit_id, ou.code AS org_unit_code,
       ou.name AS org_unit_name, p.name AS position_name, d.work_date, t.code AS shift_code, t.name AS shift_name, d.scheduled_start, d.scheduled_end,
       d.scheduled_minutes, d.first_in, d.last_out, d.worked_minutes, d.late_minutes, d.early_leave_minutes, d.overtime_minutes, d.status, d.day_kind,
       d.holiday_name, d.leave_type, d.leave_paid, d.leave_fraction, d.permission_minutes, d.unpaid_permission_minutes, d.flags, d.finalized, d.locked
FROM hris.attendance_days d
JOIN hris.employees e ON e.id = d.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
LEFT JOIN hris.positions p ON p.id = e.position_id
LEFT JOIN hris.shift_templates t ON t.id = d.shift_template_id;

CREATE VIEW reporting.hr_overtime WITH (security_invoker = true) AS
SELECT r.id AS overtime_request_id, r.property_id, r.number, r.employee_id, e.employee_no, e.full_name, ou.name AS org_unit_name, r.work_date,
       r.hours, r.timing, r.day_kind, r.actual_hours, r.payable_hours, r.multiplied_hours, r.tiers, r.status, r.created_at
FROM hris.overtime_requests r
JOIN hris.employees e ON e.id = r.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id;

CREATE VIEW reporting.hr_leave_balances WITH (security_invoker = true) AS
SELECT b.id AS balance_id, b.property_id, b.employee_id, e.employee_no, e.full_name, ou.name AS org_unit_name, e.status AS employee_status, b.leave_type,
       b.year, b.entitled, b.carried_over, b.carry_over_expires_on, b.carried_expired, b.adjusted, b.used,
       coalesce((SELECT sum(r.days) FROM hris.leave_requests r WHERE r.employee_id = b.employee_id AND r.leave_type = b.leave_type AND r.status = 'submitted'
         AND extract(year FROM r.start_date)::int = b.year), 0) AS pending,
       b.entitled + b.carried_over - b.carried_expired + b.adjusted - b.used AS balance
FROM hris.leave_balances b
JOIN hris.employees e ON e.id = b.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id;

CREATE VIEW reporting.hr_leave_requests WITH (security_invoker = true) AS
SELECT r.id AS leave_request_id, r.property_id, r.number, r.employee_id, e.employee_no, e.full_name, ou.name AS org_unit_name, r.leave_type, r.start_date,
       r.end_date, r.days, r.paid, r.status, r.created_at
FROM hris.leave_requests r
JOIN hris.employees e ON e.id = r.employee_id
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id;

-- Published shifts and the staffing requirements (Shift Coverage).
CREATE VIEW reporting.hr_shift_assignments WITH (security_invoker = true) AS
SELECT a.id AS assignment_id, a.property_id, s.id AS schedule_id, s.org_unit_id, ou.name AS org_unit_name, a.employee_id, e.position_id, a.work_date, a.kind,
       a.shift_template_id, t.code AS shift_code, t.name AS shift_name, a.work_minutes, a.status
FROM hris.shift_assignments a
JOIN hris.schedules s ON s.id = a.schedule_id AND s.status = 'published'
JOIN hris.org_units ou ON ou.id = s.org_unit_id
JOIN hris.employees e ON e.id = a.employee_id
LEFT JOIN hris.shift_templates t ON t.id = a.shift_template_id
WHERE a.status <> 'cancelled';

CREATE VIEW reporting.hr_staffing_requirements WITH (security_invoker = true) AS
SELECT r.id AS requirement_id, r.property_id, r.org_unit_id, ou.name AS org_unit_name, r.position_id, p.name AS position_name, r.shift_template_id,
       t.code AS shift_code, r.weekdays, r.min_staff
FROM hris.staffing_requirements r
JOIN hris.org_units ou ON ou.id = r.org_unit_id
LEFT JOIN hris.positions p ON p.id = r.position_id
LEFT JOIN hris.shift_templates t ON t.id = r.shift_template_id
WHERE r.status = 'active' AND r.archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.hr_staffing_requirements, reporting.hr_shift_assignments, reporting.hr_leave_requests, reporting.hr_leave_balances,
  reporting.hr_overtime, reporting.hr_attendance_days;
