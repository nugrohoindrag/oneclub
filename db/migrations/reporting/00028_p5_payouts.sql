-- PRD P5 EP-27 (FR-RPT-P5-02): read models of the payouts area for the
-- Service Charge Distribution Report, Caddy Payout Report, Instructor
-- Payout Report and Commission Payout Report. No bank account or tax id.

-- +goose Up

CREATE VIEW reporting.hr_service_charge_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, d.property_id, d.id AS distribution_id, d.number, make_date(d.year, d.month, 1) AS period, d.pay_period, d.status,
       l.employee_id, l.employee_no, l.full_name, l.org_unit_code, l.org_unit_name, l.grade_code, l.employment_status, l.worker_category,
       l.eligible, l.exclusion_reason, l.scheduled_days, l.present_days, l.attendance_factor, l.base_share, l.attendance_amount, l.redistributed,
       l.amount, l.consumed_at
FROM hris.service_charge_lines l
JOIN hris.service_charge_distributions d ON d.id = l.distribution_id
WHERE d.status <> 'cancelled';

CREATE VIEW reporting.hr_payout_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, r.property_id, r.id AS run_id, r.number, r.kind, r.period_start, r.period_end, r.pay_date, r.status AS run_status, r.paid_on,
       l.partner_id, l.partner_code, l.partner_name, l.payment_method, l.has_tax_id, l.bpu_enrolled, l.units, l.fee, l.tips, l.gross,
       l.source_deductions, l.tax_base, l.pph21_rate, l.pph21, l.bpu_jkk + l.bpu_jkm AS bpu, l.other_deductions, l.net
FROM hris.payout_lines l
JOIN hris.payout_runs r ON r.id = l.run_id
WHERE r.status <> 'cancelled';

CREATE VIEW reporting.hr_commission_payouts WITH (security_invoker = true) AS
SELECT c.id AS commission_payout_id, c.property_id, c.number, c.period, to_date(c.period || '-01', 'YYYY-MM-DD') AS period_start, c.user_name,
       c.employee_id, e.employee_no, e.full_name, c.earned, c.clawback, c.adjustments, c.total, c.earning, c.deduction, c.status, c.consumed_at,
       c.received_at
FROM hris.commission_payouts c
LEFT JOIN hris.employees e ON e.id = c.employee_id
WHERE c.status <> 'cancelled';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.hr_commission_payouts, reporting.hr_payout_lines, reporting.hr_service_charge_lines;
