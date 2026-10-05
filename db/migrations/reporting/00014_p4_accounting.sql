-- PRD P4 EP-16..EP-23 / EP-28 read models of Accounting. Two groups:
--  1. acc_* views over the billing, commercial, golf and procurement source
--     documents that accounting posts from (Technical Doc §4.2: accounting
--     reads other domains only through reporting views, never their tables);
--  2. acc_* views over the accounting schema for the financial reports and
--     the Financial Performance dashboard.
-- security_invoker: Row Level Security of the source tables applies.

-- +goose Up
-- ── 1. source documents ───────────────────────────────────────────────────
CREATE VIEW reporting.acc_folio_lines WITH (security_invoker = true) AS
SELECT l.id, l.property_id, l.folio_id, f.number AS folio_number, l.business_date, l.posted_at, l.voided_at, l.business_line,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.charge_type, l.liability, l.net_amount, l.service_amount, l.tax_amount,
       l.total, l.currency, l.components, l.tax_lines, l.reference_type, l.reference_id, l.beneficiary_type, l.beneficiary_id, l.description,
       f.source_type AS folio_source_type, f.customer_id, f.corporate_account_id, l.invoice_id
FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id;

CREATE VIEW reporting.acc_payments WITH (security_invoker = true) AS
SELECT p.id, p.property_id, p.number, p.folio_id, p.account_id, p.method_type, p.purpose, p.channel, p.amount, p.refunded_amount, p.currency, p.status,
       p.business_date, p.paid_at, p.created_at, p.tender_ref, p.integration_code, p.external_id, p.reference, p.cashier_shift_id, p.shift_id,
       f.business_line AS folio_business_line, f.source_type AS folio_source_type, f.customer_id
FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id;

CREATE VIEW reporting.acc_refunds WITH (security_invoker = true) AS
SELECT r.id, r.property_id, r.number, r.payment_id, r.folio_id, r.amount, r.currency, r.destination, r.status, r.business_date, r.processed_at,
       p.method_type, p.purpose, p.account_id, p.number AS payment_number, f.business_line AS folio_business_line
FROM billing.refunds r JOIN billing.payments p ON p.id = r.payment_id LEFT JOIN billing.folios f ON f.id = r.folio_id;

CREATE VIEW reporting.acc_deposits WITH (security_invoker = true) AS
SELECT d.id, d.property_id, d.number, d.folio_id, d.payment_id, d.amount, d.applied_amount, d.status, d.applied_at, d.created_at, d.currency
FROM billing.deposits d;

CREATE VIEW reporting.acc_payouts WITH (security_invoker = true) AS
SELECT o.id, o.property_id, o.number, o.payout_type, o.beneficiary_type, o.beneficiary_id, o.beneficiary_name, o.source_type, o.source_id, o.amount,
       o.currency, o.method_type, o.paid_at, s.caddy_fee AS settlement_fee, s.tips AS settlement_tips, s.deductions AS settlement_deductions
FROM billing.payouts o LEFT JOIN golf.caddy_settlements s ON o.source_type = 'golf.caddy_settlement' AND s.id = o.source_id;

-- Deferred revenue sub-ledger; via_tender marks recognitions of a value
-- voucher used as a folio tender (the folio charge already carries revenue).
CREATE VIEW reporting.acc_deferred_entries WITH (security_invoker = true) AS
SELECT e.id, e.property_id, e.liability_type, e.ref_type, e.ref_id, e.entry_type, e.amount, e.revenue_component, e.description, e.occurred_at,
       (e.entry_type = 'recognition' AND e.ref_type = 'commercial.voucher' AND EXISTS (
          SELECT 1 FROM commercial.voucher_ledger vl WHERE vl.voucher_id = e.ref_id AND vl.entry_type = 'redemption' AND vl.source_type = 'billing.folio'
            AND vl.amount = -e.amount AND vl.created_at BETWEEN e.occurred_at - interval '10 minutes' AND e.occurred_at + interval '10 minutes')) AS via_tender
FROM billing.deferred_revenue_entries e;

CREATE VIEW reporting.acc_invoices WITH (security_invoker = true) AS
SELECT i.id, i.property_id, i.number, i.kind, i.account_id, i.customer_id, i.corporate_account_id, i.folio_id, i.schedule_line_id, i.bill_to_name,
       i.bill_to_npwp, i.bill_to_address, i.issue_date, i.due_date, i.terms_days, i.currency, i.subtotal, i.service_amount, i.tax_amount, i.total,
       i.paid_amount, i.credited_amount, i.written_off_amount, i.status, i.issued_at, i.voided_at
FROM billing.invoices i;

CREATE VIEW reporting.acc_invoice_lines WITH (security_invoker = true) AS
SELECT il.id, il.property_id, il.invoice_id, il.seq, il.description, il.quantity, il.unit_price, il.net_amount, il.service_amount, il.tax_amount, il.total,
       il.business_line, il.revenue_component, coalesce(fl.tax_lines, '[]'::jsonb) AS tax_lines
FROM billing.invoice_lines il LEFT JOIN billing.folio_lines fl ON fl.id = il.folio_line_id;

CREATE VIEW reporting.acc_allocations WITH (security_invoker = true) AS
SELECT a.id, a.property_id, a.payment_id, a.invoice_id, a.schedule_line_id, a.amount, a.created_at, p.number AS payment_number,
       p.business_date AS payment_business_date
FROM billing.payment_allocations a JOIN billing.payments p ON p.id = a.payment_id;

CREATE VIEW reporting.acc_credit_notes WITH (security_invoker = true) AS
SELECT c.id, c.property_id, c.number, c.invoice_id, c.amount, c.currency, c.reason, c.created_at FROM billing.credit_notes c;

CREATE VIEW reporting.acc_write_offs WITH (security_invoker = true) AS
SELECT w.id, w.property_id, w.number, w.invoice_id, w.amount, w.status, w.decided_at FROM billing.write_offs w;

CREATE VIEW reporting.acc_ar_accounts WITH (security_invoker = true) AS
SELECT a.id, a.property_id, a.number, a.account_type, a.customer_id, a.corporate_account_id, coalesce(ca.name, c.name) AS name, a.credit_limit, a.status,
       coalesce((SELECT sum(e.amount) FROM billing.account_entries e WHERE e.account_id = a.id), 0) AS balance
FROM billing.customer_accounts a JOIN crm.customers c ON c.id = a.customer_id LEFT JOIN crm.corporate_accounts ca ON ca.id = a.corporate_account_id;

CREATE VIEW reporting.acc_gateway_settlements WITH (security_invoker = true) AS
SELECT r.id, r.property_id, r.business_date, r.integration_code, r.status, r.settlement_count, r.settlement_total, r.oneclub_total, r.created_at
FROM billing.reconciliations r;

CREATE VIEW reporting.acc_cashier_shifts WITH (security_invoker = true) AS
SELECT s.id, s.property_id, s.number, s.station, s.cashier_name, s.business_date, s.opening_float, s.counted_cash, s.expected_cash, s.variance, s.status,
       s.closed_at
FROM billing.cashier_shifts s;

-- POS shifts (P2) with the local date of the close.
CREATE VIEW reporting.acc_pos_shifts WITH (security_invoker = true) AS
SELECT s.id, s.property_id, s.shift_no, s.outlet_id, o.name AS outlet_name, s.opening_cash, s.counted_cash, s.expected_cash, s.variance, s.status, s.closed_at,
       (s.closed_at AT TIME ZONE coalesce((SELECT nullif(p.timezone, '') FROM platform.properties p WHERE p.id = s.property_id),
         (SELECT timezone FROM platform.instance)))::date AS closed_date
FROM commercial.pos_shifts s JOIN commercial.outlets o ON o.id = s.outlet_id;

CREATE VIEW reporting.acc_billing_days WITH (security_invoker = true) AS
SELECT d.property_id, d.business_date, d.status, d.closed_at, d.summary FROM billing.business_days d;

-- Accounting Export equivalent per business date and component (P1–P3
-- export rules: all-in components, caddy fee / tip held as liability, tax,
-- service) for the parallel run (PRD P4 §6 #15, FR-PST-07, FR-TRS-03).
CREATE VIEW reporting.acc_export_components WITH (security_invoker = true) AS
SELECT l.property_id, l.business_date, x.code AS component, bool_or(x.liability) AS liability, sum(x.amount) AS amount
FROM billing.folio_lines l
CROSS JOIN LATERAL (
  SELECT c->>'code' AS code, (c->>'amount')::numeric AS amount, coalesce((c->>'liability')::boolean, false) AS liability
  FROM jsonb_array_elements(CASE WHEN jsonb_typeof(l.components) = 'array' THEN l.components ELSE '[]'::jsonb END) c
  UNION ALL
  SELECT l.charge_type, l.net_amount, l.liability OR l.charge_type = 'caddy_tip'
  WHERE jsonb_array_length(CASE WHEN jsonb_typeof(l.components) = 'array' THEN l.components ELSE '[]'::jsonb END) = 0
  UNION ALL SELECT 'tax', l.tax_amount, false
  UNION ALL SELECT 'service', l.service_amount, false) x
WHERE l.voided_at IS NULL
GROUP BY 1, 2, 3;

CREATE VIEW reporting.acc_suppliers WITH (security_invoker = true) AS
SELECT s.id, s.property_id, s.code, s.name, s.npwp, s.address, s.email, s.status, s.attributes FROM procurement.suppliers s;

-- ── 2. accounting read models ─────────────────────────────────────────────
CREATE VIEW reporting.acc_ledger WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.journal_id, j.number AS journal_number, l.journal_date, j.journal_type, j.source_type AS journal_source_type,
       j.source_id AS journal_source_id, j.source_ref, j.description AS journal_description, l.account_id, a.code AS account_code, a.name AS account_name,
       a.account_type, a.subtype, a.cash_flow, a.normal_balance, l.debit, l.credit, l.debit - l.credit AS amount, l.description, l.business_line,
       l.revenue_component, l.component, l.cost_center, l.partner_type, l.partner_id, l.partner_name, l.source_type, l.source_id
FROM accounting.journal_lines l JOIN accounting.journals j ON j.id = l.journal_id JOIN accounting.accounts a ON a.id = l.account_id;

CREATE VIEW reporting.acc_accounts WITH (security_invoker = true) AS
SELECT a.id AS account_id, a.code, a.name, a.account_type, a.subtype, a.cash_flow, a.normal_balance, a.is_posting, a.status, p.code AS parent_code
FROM accounting.accounts a LEFT JOIN accounting.accounts p ON p.id = a.parent_id;

CREATE VIEW reporting.acc_ap_items WITH (security_invoker = true) AS
SELECT i.id, i.property_id, i.supplier_id, i.supplier_name, i.item_type, i.number, i.supplier_invoice_no, i.invoice_date, i.due_date, i.currency,
       i.amount, i.paid_amount, i.amount - i.paid_amount AS outstanding, i.status
FROM accounting.ap_items i;

CREATE VIEW reporting.acc_bank_transactions WITH (security_invoker = true) AS
SELECT t.id, t.property_id, t.bank_account_id, b.code AS bank_account_code, b.name AS bank_account_name, t.tx_date, t.description, t.reference,
       t.amount, t.balance, t.status, t.matched_at
FROM accounting.bank_transactions t JOIN accounting.bank_accounts b ON b.id = t.bank_account_id;

CREATE VIEW reporting.acc_tax_invoices WITH (security_invoker = true) AS
SELECT t.id, t.property_id, t.direction, t.source_type, t.source_number, t.partner_name, t.partner_npwp, t.invoice_date, t.tax_period, t.dpp, t.ppn,
       t.faktur_number, t.status
FROM accounting.tax_invoices t;

CREATE VIEW reporting.acc_posting_exceptions WITH (security_invoker = true) AS
SELECT e.id, e.property_id, e.event_type, e.source_type, e.source_id, e.reason, e.message, e.amount, e.status, e.attempts, e.created_at, e.resolved_at
FROM accounting.posting_exceptions e;

CREATE VIEW reporting.acc_posted_sources WITH (security_invoker = true) AS
SELECT s.property_id, s.source_type, s.source_id, s.part, s.journal_id, s.business_date, s.key1, s.key2, s.amount, s.created_at
FROM accounting.posted_sources s;

-- Daily Revenue rows of the closed business days as received (K4).
CREATE VIEW reporting.acc_daily_revenue WITH (security_invoker = true) AS
SELECT d.property_id, d.business_date, r->>'businessLine' AS business_line, r->>'revenueComponent' AS revenue_component,
       coalesce((r->>'liability')::boolean, false) AS liability, (r->>'net')::numeric AS net, (r->>'service')::numeric AS service,
       (r->>'tax')::numeric AS tax, (r->>'total')::numeric AS total
FROM accounting.business_days d CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.summary->'revenue', '[]'::jsonb)) r;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.acc_daily_revenue, reporting.acc_posted_sources, reporting.acc_posting_exceptions, reporting.acc_tax_invoices,
  reporting.acc_bank_transactions, reporting.acc_ap_items, reporting.acc_accounts, reporting.acc_ledger, reporting.acc_suppliers,
  reporting.acc_export_components, reporting.acc_billing_days, reporting.acc_pos_shifts, reporting.acc_cashier_shifts, reporting.acc_gateway_settlements,
  reporting.acc_ar_accounts, reporting.acc_write_offs, reporting.acc_credit_notes, reporting.acc_allocations, reporting.acc_invoice_lines,
  reporting.acc_invoices, reporting.acc_deferred_entries, reporting.acc_payouts, reporting.acc_deposits, reporting.acc_refunds,
  reporting.acc_payments, reporting.acc_folio_lines;
