-- PRD P5 payroll read models (EP-27 FR-RPT-P5-01/02/04, FR-PPY-04, EP-29):
-- Payroll Summary, Payroll Cost (department, component, employer cost,
-- headcount cost), Overtime Cost, PPh 21, BPJS, the annual recap per
-- employee and the Parallel Run Report; the Payroll Cost KPIs of HR
-- Performance. Approved, posted and paid runs only; no identity numbers or
-- bank accounts (the report role cannot read them). The reports are granted
-- to payroll roles only. Views use security_invoker so RLS of the sources
-- applies.

-- +goose Up
CREATE VIEW reporting.hr_payroll_runs WITH (security_invoker = true) AS
SELECT r.id AS run_id, r.property_id, r.number, r.name, r.run_type, r.period_code, r.period_start, r.period_end, r.payment_date, r.status, r.headcount,
       r.gross, r.taxable_gross, r.bpjs_employee, r.bpjs_employer, r.pph21, r.other_deductions, r.net, r.employer_cost, r.posted_at, r.paid_on
FROM hris.payroll_runs r
WHERE r.status IN ('approved', 'posted', 'paid');

CREATE VIEW reporting.hr_payroll_slips WITH (security_invoker = true) AS
SELECT s.id AS slip_id, s.property_id, s.run_id, r.number AS run_number, r.run_type, r.status AS run_status, r.period_code, r.period_start, r.period_end,
       r.payment_date, s.employee_id, s.employee_no, s.full_name, s.org_unit_id, s.org_unit_code, s.org_unit_name, s.cost_center, s.grade_code,
       s.position_name, s.ptkp_status, s.ter_category, s.tax_method, s.ter_rate, s.gross, s.taxable_gross, s.deductible, s.bpjs_employee, s.bpjs_employer,
       s.pph21, s.pph21_final, s.other_deductions, s.net, s.employer_cost, s.overtime_hours
FROM hris.payroll_slips s
JOIN hris.payroll_runs r ON r.id = s.run_id
WHERE r.status IN ('approved', 'posted', 'paid');

CREATE VIEW reporting.hr_payroll_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.run_id, l.slip_id, r.number AS run_number, r.run_type, r.status AS run_status, r.period_code, r.period_end,
       l.employee_id, s.employee_no, s.full_name, s.org_unit_name, s.cost_center, l.code, l.name, l.kind, l.category, l.programme, l.pre_tax, l.quantity,
       l.amount
FROM hris.payroll_lines l
JOIN hris.payroll_slips s ON s.id = l.slip_id
JOIN hris.payroll_runs r ON r.id = l.run_id
WHERE r.status IN ('approved', 'posted', 'paid');

CREATE VIEW reporting.hr_payroll_legacy WITH (security_invoker = true) AS
SELECT l.id AS legacy_line_id, l.property_id, l.period_code, l.employee_id, e.employee_no, e.full_name, l.component_code, l.amount, l.batch_ref
FROM hris.legacy_payroll_lines l
JOIN hris.employees e ON e.id = l.employee_id;

-- Parallel run (EP-28 FR-MIG-P5-06): the regular run of a period (from
-- Calculated on) against the legacy payroll per employee and component,
-- with the totals GROSS, BPJS_EE, PPH21 and NET.
CREATE VIEW reporting.hr_payroll_parallel WITH (security_invoker = true) AS
WITH one AS (
  SELECT r.property_id, r.period_code, s.employee_id, l.code AS component_code, sum(l.amount) AS amount
  FROM hris.payroll_lines l JOIN hris.payroll_slips s ON s.id = l.slip_id JOIN hris.payroll_runs r ON r.id = l.run_id
  WHERE r.run_type = 'regular' AND r.status IN ('calculated', 'submitted', 'approved', 'posted', 'paid') AND l.kind IN ('earning', 'deduction')
  GROUP BY 1, 2, 3, 4
  UNION ALL
  SELECT r.property_id, r.period_code, s.employee_id, x.code, x.amount
  FROM hris.payroll_slips s JOIN hris.payroll_runs r ON r.id = s.run_id,
    LATERAL (VALUES ('GROSS', s.gross), ('BPJS_EE', s.bpjs_employee), ('PPH21', s.pph21 + s.pph21_final), ('NET', s.net)) x(code, amount)
  WHERE r.run_type = 'regular' AND r.status IN ('calculated', 'submitted', 'approved', 'posted', 'paid')),
legacy AS (
  SELECT property_id, period_code, employee_id, component_code, sum(amount) AS amount FROM hris.legacy_payroll_lines GROUP BY 1, 2, 3, 4)
SELECT coalesce(o.property_id, g.property_id) AS property_id, coalesce(o.period_code, g.period_code) AS period_code,
       coalesce(o.employee_id, g.employee_id) AS employee_id, e.employee_no, e.full_name, coalesce(o.component_code, g.component_code) AS component_code,
       coalesce(o.amount, 0) AS oneclub_amount, coalesce(g.amount, 0) AS legacy_amount, coalesce(o.amount, 0) - coalesce(g.amount, 0) AS difference,
       (o.employee_id IS NULL) AS missing_in_oneclub, (g.employee_id IS NULL) AS missing_in_legacy
FROM one o
FULL JOIN legacy g ON g.property_id = o.property_id AND g.period_code = o.period_code AND g.employee_id = o.employee_id
  AND g.component_code = o.component_code
JOIN hris.employees e ON e.id = coalesce(o.employee_id, g.employee_id)
WHERE EXISTS (SELECT 1 FROM hris.legacy_payroll_lines x WHERE x.period_code = coalesce(o.period_code, g.period_code)
  AND x.employee_id = coalesce(o.employee_id, g.employee_id));

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.hr_payroll_parallel, reporting.hr_payroll_legacy, reporting.hr_payroll_lines, reporting.hr_payroll_slips, reporting.hr_payroll_runs;
