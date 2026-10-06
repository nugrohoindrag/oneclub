-- PRD P3 CRM Sales read models (EP-01–04, EP-23 FR-RPT-P3-02/05).
-- Operational: where billing money belongs (commission recognition reads the
-- payment schedules, invoices and payments of deals without importing
-- billing, Technical Doc §4.2 #3), venue resources of the opportunity
-- availability check (FR-PIPE-06) and the membership applications converted
-- from quotations (FR-QUO-06). Reports: Lead Source, Sales Pipeline,
-- Quotation and Sales Commission Reports and the CRM Performance KPIs.
-- Views use security_invoker so Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.sales_schedules WITH (security_invoker = true) AS
SELECT s.id AS schedule_id, s.property_id, s.source_type, s.source_id, s.customer_id, s.corporate_account_id, s.status, s.total_amount
FROM billing.payment_schedules s;

-- One row per invoice: the payment schedule the invoice bills (if any).
CREATE VIEW reporting.sales_invoice_sources WITH (security_invoker = true) AS
SELECT i.id AS invoice_id, i.property_id, i.customer_id, i.corporate_account_id, s.id AS schedule_id, s.source_type AS schedule_source_type,
       s.source_id AS schedule_source_id, s.customer_id AS schedule_customer_id, s.corporate_account_id AS schedule_corporate_account_id
FROM billing.invoices i
LEFT JOIN LATERAL (SELECT l.schedule_id FROM billing.payment_schedule_lines l WHERE l.id = i.schedule_line_id OR l.invoice_id = i.id
                   ORDER BY (l.id = i.schedule_line_id) DESC LIMIT 1) sl ON true
LEFT JOIN billing.payment_schedules s ON s.id = sl.schedule_id;

-- One row per allocation of a payment (invoice or schedule line); a gateway
-- payment of a schedule line not allocated yet counts by its tag.
CREATE VIEW reporting.sales_payment_sources WITH (security_invoker = true) AS
WITH src AS (
  SELECT p.id AS payment_id, p.property_id, p.folio_id, a.invoice_id, a.schedule_line_id, a.amount AS allocated_amount
  FROM billing.payments p JOIN billing.payment_allocations a ON a.payment_id = p.id
  UNION ALL
  SELECT p.id, p.property_id, p.folio_id, NULL::uuid, (p.tender_ref ->> 'scheduleLineId')::uuid, p.amount - p.refunded_amount
  FROM billing.payments p
  WHERE p.tender_ref ->> 'scheduleLineId' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
    AND NOT EXISTS (SELECT 1 FROM billing.payment_allocations a WHERE a.payment_id = p.id))
SELECT src.payment_id, src.property_id, src.invoice_id, l.schedule_id, s.source_type AS schedule_source_type, s.source_id AS schedule_source_id,
       s.customer_id AS schedule_customer_id, s.corporate_account_id AS schedule_corporate_account_id, f.customer_id AS folio_customer_id,
       src.allocated_amount
FROM src
LEFT JOIN billing.payment_schedule_lines l ON l.id = src.schedule_line_id
LEFT JOIN billing.payment_schedules s ON s.id = l.schedule_id
LEFT JOIN billing.folios f ON f.id = src.folio_id;

CREATE VIEW reporting.sales_venues WITH (security_invoker = true) AS
SELECT r.id AS resource_id, r.property_id, r.code, r.name, r.resource_type, r.capacity, r.status
FROM reservation.resources r WHERE r.archived_at IS NULL;

CREATE VIEW reporting.sales_membership_applications WITH (security_invoker = true) AS
SELECT a.id AS application_id, a.property_id, a.number, a.status, a.customer_id, a.quotation_id, a.quotation_number
FROM membership.applications a WHERE a.quotation_id IS NOT NULL;

-- ── reports & KPIs ────────────────────────────────────────────────────────
CREATE VIEW reporting.sales_leads WITH (security_invoker = true) AS
SELECT l.id AS lead_id, l.property_id, l.number, l.name, l.company_name, l.source, l.channel, l.line, l.status, l.owner_user_id,
       u.full_name AS owner_name, l.created_at, l.first_response_due_at, l.first_responded_at, l.qualified_at, l.unqualified_at,
       l.unqualified_reason, l.converted_at, l.budget, l.customer_id, l.opportunity_id
FROM crm.sales_leads l LEFT JOIN platform.users u ON u.id = l.owner_user_id
WHERE l.anonymized_at IS NULL;

CREATE VIEW reporting.sales_opportunities WITH (security_invoker = true) AS
SELECT o.id AS opportunity_id, o.property_id, o.number, o.title, o.line, o.status, p.name AS pipeline_name, st.name AS stage_name,
       st.sort_order AS stage_order, o.probability, o.expected_value, o.expected_value * o.probability / 100 AS weighted_value, o.currency,
       o.expected_close_date, o.event_date, o.owner_user_id, u.full_name AS owner_name, coalesce(ca.name, c.name) AS customer_name,
       o.created_at, o.won_at, o.lost_at, o.lost_reason
FROM crm.sales_opportunities o
JOIN crm.sales_pipelines p ON p.id = o.pipeline_id
JOIN crm.sales_pipeline_stages st ON st.id = o.stage_id
LEFT JOIN platform.users u ON u.id = o.owner_user_id
LEFT JOIN crm.customers c ON c.id = o.customer_id
LEFT JOIN crm.corporate_accounts ca ON ca.id = o.corporate_account_id;

CREATE VIEW reporting.sales_quotations WITH (security_invoker = true) AS
SELECT q.id AS quotation_id, q.property_id, q.number, q.version, q.title, q.line, q.status, q.approval_status, coalesce(ca.name, c.name) AS customer_name,
       q.owner_user_id, u.full_name AS owner_name, q.currency, q.subtotal, q.discount, q.discount_percent, q.net_amount, q.service_amount, q.tax_amount,
       q.total, q.valid_until, q.sent_at, q.accepted_at, q.accepted_via, q.rejected_at, q.expired_at, q.paid_amount, q.paid_at, q.created_at
FROM crm.sales_quotations q
LEFT JOIN platform.users u ON u.id = q.owner_user_id
LEFT JOIN crm.customers c ON c.id = q.customer_id
LEFT JOIN crm.corporate_accounts ca ON ca.id = q.corporate_account_id;

CREATE VIEW reporting.sales_commissions WITH (security_invoker = true) AS
SELECT c.id AS commission_id, c.property_id, c.user_id, u.full_name AS user_name, c.kind, c.line, c.basis_amount, c.rate_percent, c.amount, c.currency,
       c.recognized_on, c.period, q.number AS quotation_number, s.number AS statement_number, s.status AS statement_status, c.reason
FROM crm.sales_commissions c
JOIN platform.users u ON u.id = c.user_id
LEFT JOIN crm.sales_quotations q ON q.id = c.quotation_id
LEFT JOIN crm.sales_commission_statements s ON s.id = c.statement_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.sales_commissions, reporting.sales_quotations, reporting.sales_opportunities, reporting.sales_leads,
  reporting.sales_membership_applications, reporting.sales_venues, reporting.sales_payment_sources, reporting.sales_invoice_sources,
  reporting.sales_schedules;
