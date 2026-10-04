-- PRD P3 billing read models (EP-17/18 reports, EP-23 FR-RPT-P3-05):
-- invoices and their settlement movements (Invoice Report, Accounts
-- Receivable Aging Report — same rules as the operational ageing of
-- billing.AgingAsOf), payment schedules, business-dated revenue (Daily
-- Revenue Report), night audit runs and cashier shifts. Views use
-- security_invoker so Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.billing_invoices WITH (security_invoker = true) AS
SELECT i.id AS invoice_id, i.property_id, i.number, i.kind, i.status, i.bill_to_name, i.customer_id, i.corporate_account_id, i.account_id,
       i.issue_date, i.due_date, i.currency, i.subtotal, i.service_amount, i.tax_amount, i.total, i.paid_amount, i.credited_amount,
       i.written_off_amount, i.total - i.paid_amount - i.credited_amount - i.written_off_amount AS outstanding, i.issued_at, i.voided_at, i.created_at
FROM billing.invoices i;

-- Settlement movements of invoices with their dates (as-of ageing).
CREATE VIEW reporting.billing_invoice_movements WITH (security_invoker = true) AS
SELECT a.invoice_id, a.property_id, 'payment'::text AS kind, a.amount, a.created_at AS occurred_at
FROM billing.payment_allocations a WHERE a.invoice_id IS NOT NULL
UNION ALL
SELECT c.invoice_id, c.property_id, 'credit_note', c.amount, c.created_at FROM billing.credit_notes c
UNION ALL
SELECT w.invoice_id, w.property_id, 'write_off', w.amount, w.decided_at FROM billing.write_offs w WHERE w.status = 'approved';

CREATE VIEW reporting.billing_payment_schedules WITH (security_invoker = true) AS
SELECT l.id AS line_id, s.property_id, s.id AS schedule_id, s.number AS schedule_number, s.title, s.source_type, s.source_ref, s.status AS schedule_status,
       coalesce(c.name, ca.name) AS customer_name, l.seq, l.label, l.kind, l.due_date, l.amount, l.paid_amount, l.amount - l.paid_amount AS outstanding,
       l.status, l.paid_at, inv.number AS invoice_number, s.currency
FROM billing.payment_schedule_lines l
JOIN billing.payment_schedules s ON s.id = l.schedule_id
LEFT JOIN crm.customers c ON c.id = s.customer_id
LEFT JOIN crm.corporate_accounts ca ON ca.id = s.corporate_account_id
LEFT JOIN billing.invoices inv ON inv.id = l.invoice_id;

-- Charges by business date (transactions after the night audit belong to
-- the next business day, FR-EOD-05).
CREATE VIEW reporting.billing_daily_revenue WITH (security_invoker = true) AS
SELECT l.property_id, l.business_date, l.business_line, coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.liability,
       l.net_amount, l.service_amount, l.tax_amount, l.total
FROM billing.folio_lines l WHERE l.voided_at IS NULL AND l.business_date IS NOT NULL;

CREATE VIEW reporting.billing_payments_by_day WITH (security_invoker = true) AS
SELECT p.property_id, p.business_date, p.method_type, p.purpose, p.amount, p.refunded_amount, p.cashier_shift_id, p.shift_id
FROM billing.payments p WHERE p.status IN ('completed', 'refunded') AND p.business_date IS NOT NULL;

CREATE VIEW reporting.billing_business_days WITH (security_invoker = true) AS
SELECT d.property_id, d.business_date, d.status, d.closed_at, d.reopened_at, d.reopen_reason,
       (d.summary ->> 'charges') AS charges, (d.summary ->> 'paymentTotal') AS payment_total, (d.summary ->> 'refunds') AS refunds,
       (d.summary ->> 'shiftTotal') AS shift_total
FROM billing.business_days d;

CREATE VIEW reporting.billing_night_audit_runs WITH (security_invoker = true) AS
SELECT r.id AS run_id, r.property_id, r.business_date, r.mode, r.status, r.exceptions, r.warnings, r.started_at, r.finished_at,
       u.full_name AS run_by_name, r.checks
FROM billing.night_audit_runs r LEFT JOIN platform.users u ON u.id = r.run_by;

CREATE VIEW reporting.billing_cashier_shifts WITH (security_invoker = true) AS
SELECT s.id AS shift_id, s.property_id, s.number, s.station, s.cashier_name, s.business_date, s.opened_at, s.closed_at, s.opening_float,
       s.expected_cash, s.counted_cash, s.variance, s.status, s.currency,
       (SELECT coalesce(sum(p.amount - p.refunded_amount), 0) FROM billing.payments p WHERE p.cashier_shift_id = s.id
          AND p.status IN ('completed', 'refunded')) AS payments
FROM billing.cashier_shifts s;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.billing_cashier_shifts, reporting.billing_night_audit_runs, reporting.billing_business_days, reporting.billing_payments_by_day,
  reporting.billing_daily_revenue, reporting.billing_payment_schedules, reporting.billing_invoice_movements, reporting.billing_invoices;
