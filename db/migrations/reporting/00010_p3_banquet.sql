-- PRD P3 Banquet & Event read models (EP-12–15, EP-23 FR-RPT-P3-01/05):
-- Event Report, Banquet Revenue Report, BEO Report and the Banquet
-- Performance dashboard. Banquet revenue is the folio lines of the event
-- and registration folios (source banquet_event) so the dashboard and the
-- report read the same rows (EP-23 AC). Views use security_invoker so Row
-- Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.banquet_events WITH (security_invoker = true) AS
SELECT e.id AS event_id, e.property_id, e.number, e.title, t.name AS event_type, e.category, e.status, e.source,
       coalesce(ca.name, c.name, e.contact_name) AS customer_name, u.full_name AS sales_owner_name, e.sales_owner_id, e.start_at, e.end_at,
       e.expected_pax, e.guaranteed_pax, e.final_pax, coalesce(e.final_pax, e.guaranteed_pax, e.expected_pax) AS pax, e.contract_total, e.currency,
       e.folio_id, e.schedule_id, e.quotation_number, e.legacy_ref, e.definite_at, e.completed_at, e.cancelled_at, e.cancellation_fee, e.created_at,
       coalesce((SELECT string_agg(DISTINCT v.name, ', ') FROM banquet.event_venues h JOIN banquet.venues v ON v.id = h.venue_id
                 WHERE h.event_id = e.id AND h.status IN ('tentative', 'definite', 'completed')), '') AS venues
FROM banquet.events e
JOIN banquet.event_types t ON t.id = e.event_type_id
LEFT JOIN crm.customers c ON c.id = e.customer_id
LEFT JOIN crm.corporate_accounts ca ON ca.id = e.corporate_account_id
LEFT JOIN platform.users u ON u.id = e.sales_owner_id;

-- One row per revenue line of a banquet folio (event folio or the folio of
-- a paid registration).
CREATE VIEW reporting.banquet_revenue WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, coalesce(e.id, p.event_id) AS event_id, l.folio_id, l.posted_at, l.business_date,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.description, l.net_amount AS net, l.service_amount AS service,
       l.tax_amount AS tax, l.total
FROM billing.folio_lines l
JOIN billing.folios f ON f.id = l.folio_id AND f.source_type = 'banquet_event'
LEFT JOIN banquet.events e ON e.folio_id = l.folio_id
LEFT JOIN banquet.participants p ON p.folio_id = l.folio_id
WHERE l.voided_at IS NULL AND NOT l.liability;

-- Payment schedule lines of the events (Outstanding DP / Settlement).
CREATE VIEW reporting.banquet_payment_terms WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, e.id AS event_id, e.status AS event_status, l.kind, l.label, l.due_date, l.amount, l.paid_amount,
       greatest(l.amount - l.paid_amount, 0) AS outstanding, l.status
FROM billing.payment_schedule_lines l
JOIN billing.payment_schedules s ON s.id = l.schedule_id AND s.source_type = 'banquet_event'
JOIN banquet.events e ON e.id = s.source_id;

CREATE VIEW reporting.banquet_beos WITH (security_invoker = true) AS
SELECT b.id AS beo_id, b.property_id, b.number, b.version, b.status, b.event_date, b.pax, e.id AS event_id, e.number AS event_number,
       e.title AS event_title, e.status AS event_status, b.issued_at, b.superseded_at, b.locked_at, b.revision_reason,
       jsonb_array_length(b.changes) AS changes, jsonb_array_length(b.requirements) AS requirements,
       (SELECT count(*) FROM banquet.beo_departments d WHERE d.beo_id = b.id) AS departments,
       (SELECT count(*) FROM banquet.beo_departments d WHERE d.beo_id = b.id AND d.acknowledged_at IS NOT NULL) AS acknowledged,
       coalesce((SELECT string_agg(d.department, ', ' ORDER BY d.department) FROM banquet.beo_departments d
                 WHERE d.beo_id = b.id AND d.acknowledged_at IS NULL), '') AS pending_departments
FROM banquet.beos b JOIN banquet.events e ON e.id = b.event_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.banquet_beos;
DROP VIEW reporting.banquet_payment_terms;
DROP VIEW reporting.banquet_revenue;
DROP VIEW reporting.banquet_events;
