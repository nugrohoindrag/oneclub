-- PRD P3 read models of CRM engagement & loyalty (EP-05..09, EP-23). CRM sits
-- below billing and commercial (Technical Doc §4.2), so loyalty earning, Top
-- Spender, RFM segmentation and Corporate 360 read folios, payments, refunds,
-- invoices and POS outlets through these views; reports and the CRM
-- Performance KPIs read the CRM views below. security_invoker keeps Row
-- Level Security of the source tables.

-- +goose Up
-- ── cross-domain reads (billing, commercial) ─────────────────────────────
-- Charged folio lines with the customer, the POS outlet and product.
CREATE VIEW reporting.eng_folio_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.folio_id, f.customer_id, f.corporate_account_id, f.source_type, f.status AS folio_status,
       CASE WHEN f.source_type IN ('membership_fee', 'membership_renewal', 'membership') THEN 'membership' ELSE l.business_line END AS business_line,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.liability, l.net_amount, l.service_amount, l.tax_amount, l.total,
       f.currency, l.posted_at, l.business_date, l.reference_type, l.reference_id,
       CASE WHEN f.source_type = 'pos_order' THEN (SELECT o.outlet_id FROM commercial.orders o WHERE o.id = f.source_id) END AS outlet_id,
       CASE WHEN l.reference_type = 'commercial.order_line' THEN (SELECT ol.product_id FROM commercial.order_lines ol WHERE ol.id = l.reference_id) END AS product_id
FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
WHERE l.voided_at IS NULL;

-- Folios with their charges and net payments (points tender, Member App).
CREATE VIEW reporting.eng_folios WITH (security_invoker = true) AS
SELECT f.id AS folio_id, f.property_id, f.number, f.customer_id, f.corporate_account_id, f.status, f.business_line, f.source_type, f.currency,
       coalesce((SELECT sum(l.total) FROM billing.folio_lines l WHERE l.folio_id = f.id AND l.voided_at IS NULL), 0) AS charges,
       coalesce((SELECT sum(p.amount - p.refunded_amount) FROM billing.payments p WHERE p.folio_id = f.id AND p.status IN ('completed', 'refunded')), 0) AS paid,
       f.created_at
FROM billing.folios f;

CREATE VIEW reporting.eng_payments WITH (security_invoker = true) AS
SELECT p.id AS payment_id, p.property_id, p.number, p.folio_id, f.customer_id, f.number AS folio_number, p.method_type, p.purpose, p.amount,
       p.refunded_amount, p.currency, p.status, p.tender_ref, p.paid_at, p.business_date
FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id;

CREATE VIEW reporting.eng_refunds WITH (security_invoker = true) AS
SELECT r.id AS refund_id, r.property_id, r.number, r.payment_id, coalesce(r.folio_id, p.folio_id) AS folio_id, f.customer_id, p.method_type,
       r.amount, r.status, r.processed_at
FROM billing.refunds r JOIN billing.payments p ON p.id = r.payment_id LEFT JOIN billing.folios f ON f.id = coalesce(r.folio_id, p.folio_id);

CREATE VIEW reporting.eng_invoices WITH (security_invoker = true) AS
SELECT i.id AS invoice_id, i.property_id, i.number, i.kind, i.customer_id, i.corporate_account_id, i.bill_to_name, i.issue_date, i.due_date,
       i.currency, i.total, i.paid_amount, i.credited_amount, i.written_off_amount,
       i.total - i.paid_amount - i.credited_amount - i.written_off_amount AS outstanding, i.status
FROM billing.invoices i;

CREATE VIEW reporting.eng_credit_notes WITH (security_invoker = true) AS
SELECT c.id AS credit_note_id, c.property_id, c.number, c.invoice_id, i.customer_id, i.corporate_account_id, c.amount, c.created_at
FROM billing.credit_notes c JOIN billing.invoices i ON i.id = c.invoice_id;

CREATE VIEW reporting.eng_outlets WITH (security_invoker = true) AS
SELECT id, property_id, code, name, status FROM commercial.outlets WHERE archived_at IS NULL;

CREATE VIEW reporting.eng_products WITH (security_invoker = true) AS
SELECT id, property_id, code, name, status FROM commercial.products WHERE archived_at IS NULL;

-- ── CRM engagement & loyalty read models (reports, KPIs) ─────────────────
-- Redemption value of a point in force per property (Loyalty Policies
-- crm.loyalty; 100 = the code default, keep in sync with internal/crm/loyalty).
CREATE VIEW reporting.eng_loyalty_value WITH (security_invoker = true) AS
SELECT p.id AS property_id,
       coalesce((SELECT nullif(r.value->>'redemptionValue', '')::numeric FROM platform.rules r WHERE r.kind = 'club_policy' AND r.code = 'crm.loyalty'
                 AND r.status = 'active' AND r.effective_from <= now() AND (r.property_id IS NULL OR r.property_id = p.id)
                 ORDER BY (r.property_id IS NOT NULL) DESC, r.effective_from DESC, r.version DESC LIMIT 1), 100) AS redemption_value
FROM platform.properties p;

CREATE VIEW reporting.eng_loyalty_accounts WITH (security_invoker = true) AS
SELECT a.id AS account_id, a.property_id, a.number, a.customer_id, c.code AS customer_code, c.name AS customer_name, t.code AS tier_code,
       t.name AS tier_name, a.status, a.balance, a.lifetime_points, a.enrolled_via, a.opted_in_at
FROM crm.loyalty_accounts a JOIN crm.customers c ON c.id = a.customer_id LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id;

CREATE VIEW reporting.eng_loyalty_ledger WITH (security_invoker = true) AS
SELECT l.id AS entry_id, l.property_id, l.account_id, a.number AS account_number, a.customer_id, c.name AS customer_name, t.name AS tier_name,
       l.kind, l.points, l.balance_after, l.source_type, l.source_ref, l.amount, l.expires_on, l.description, l.occurred_at
FROM crm.loyalty_ledger l JOIN crm.loyalty_accounts a ON a.id = l.account_id JOIN crm.customers c ON c.id = a.customer_id
LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id;

CREATE VIEW reporting.eng_reward_redemptions WITH (security_invoker = true) AS
SELECT r.id AS redemption_id, r.property_id, r.number, a.number AS account_number, c.name AS customer_name, w.code AS reward_code,
       w.name AS reward_name, w.reward_type, r.quantity, r.points, r.status, r.channel, r.created_at, r.completed_at
FROM crm.loyalty_reward_redemptions r JOIN crm.loyalty_accounts a ON a.id = r.account_id JOIN crm.customers c ON c.id = a.customer_id
JOIN crm.loyalty_rewards w ON w.id = r.reward_id;

CREATE VIEW reporting.eng_campaigns WITH (security_invoker = true) AS
SELECT id AS campaign_id, property_id, code, name, channel, status, segment_id, promo_code, promo_mode, scheduled_at, sent_at, recipient_count,
       sent_count, skipped_count, created_at
FROM crm.campaigns WHERE archived_at IS NULL;

CREATE VIEW reporting.eng_campaign_recipients WITH (security_invoker = true) AS
SELECT r.id AS recipient_id, r.property_id, r.campaign_id, r.customer_id, r.channel, r.status, r.sent_at, r.clicked_at, r.click_count,
       r.unsubscribed_at, r.converted_at,
       d.delivery_status, d.delivered_at, coalesce(d.read_at, n.read_at) AS read_at
FROM crm.campaign_recipients r
LEFT JOIN LATERAL (SELECT x.delivery_status, x.delivered_at, x.read_at FROM platform.notification_deliveries x
                   WHERE x.payload->>'trackingToken' = r.token ORDER BY x.created_at DESC LIMIT 1) d ON true
LEFT JOIN LATERAL (SELECT y.read_at FROM platform.notifications y WHERE r.channel = 'in_app' AND y.link = '/c/' || r.token
                   ORDER BY y.created_at DESC LIMIT 1) n ON true;

CREATE VIEW reporting.eng_tickets WITH (security_invoker = true) AS
SELECT t.id AS ticket_id, t.property_id, t.number, t.customer_id, coalesce(c.name, t.contact_name) AS customer_name, k.name AS category_name,
       t.business_line, t.priority, t.channel, t.subject, t.status, t.escalation_level, t.first_response_due_at, t.resolution_due_at,
       t.first_responded_at, t.resolved_at, t.closed_at, t.first_response_breached, t.resolution_breached, t.reopened_count, t.created_at
FROM crm.tickets t LEFT JOIN crm.customers c ON c.id = t.customer_id LEFT JOIN crm.ticket_categories k ON k.id = t.category_id;

-- Survey NPS (P2 feedback) and relationship NPS (P3) in one stream; the
-- business line follows the feedback context.
CREATE VIEW reporting.eng_nps WITH (security_invoker = true) AS
SELECT f.id AS response_id, f.property_id, f.customer_id, f.nps AS score, f.comment,
       CASE f.context_type WHEN 'round' THEN 'golf' WHEN 'stay' THEN 'stay' WHEN 'meeting' THEN 'stay' WHEN 'class' THEN 'sportclub'
         WHEN 'sport' THEN 'sportclub' WHEN 'fnb' THEN 'pos' ELSE 'other' END AS business_line, 'survey' AS source, f.created_at
FROM crm.feedback f WHERE f.nps IS NOT NULL
UNION ALL
SELECT n.id, n.property_id, n.customer_id, n.score, n.comment, n.business_line, 'relationship', n.created_at FROM crm.nps_responses n;

-- Top Spender (FR-TOP-01/07): spend events per customer — net charges of
-- folios (not liabilities), completed refunds and credit notes (negative)
-- and, per the Top Spender policy (crm.top_spender), payments with points
-- or Voucher & Prepaid (negative, bought or earned before).
CREATE VIEW reporting.eng_top_spender_policy WITH (security_invoker = true) AS
SELECT p.id AS property_id,
       coalesce((x.value->>'excludePointsPaid')::boolean, false) AS exclude_points,
       coalesce((x.value->>'excludeVoucherPaid')::boolean, false) AS exclude_voucher
FROM platform.properties p
LEFT JOIN LATERAL (SELECT r.value FROM platform.rules r WHERE r.kind = 'club_policy' AND r.code = 'crm.top_spender' AND r.status = 'active'
                   AND r.effective_from <= now() AND (r.property_id IS NULL OR r.property_id = p.id)
                   ORDER BY (r.property_id IS NOT NULL) DESC, r.effective_from DESC, r.version DESC LIMIT 1) x ON true;

CREATE VIEW reporting.eng_spend WITH (security_invoker = true) AS
SELECT l.property_id, l.customer_id, 'charge'::text AS kind, l.business_line, l.outlet_id, l.posted_at AS occurred_at, l.net_amount AS amount
FROM reporting.eng_folio_lines l WHERE l.customer_id IS NOT NULL AND NOT l.liability
UNION ALL
SELECT r.property_id, r.customer_id, 'refund', f.business_line, NULL::uuid, r.processed_at, -r.amount
FROM reporting.eng_refunds r LEFT JOIN reporting.eng_folios f ON f.folio_id = r.folio_id
WHERE r.customer_id IS NOT NULL AND r.status = 'completed'
UNION ALL
SELECT c.property_id, c.customer_id, 'credit_note', NULL::text, NULL::uuid, c.created_at, -c.amount
FROM reporting.eng_credit_notes c WHERE c.customer_id IS NOT NULL
UNION ALL
SELECT p.property_id, p.customer_id, 'excluded', NULL::text, NULL::uuid, p.paid_at, -(p.amount - p.refunded_amount)
FROM reporting.eng_payments p JOIN reporting.eng_top_spender_policy x ON x.property_id = p.property_id
WHERE p.customer_id IS NOT NULL AND p.status IN ('completed', 'refunded')
  AND ((p.method_type = 'loyalty_points' AND x.exclude_points) OR (p.method_type = 'voucher_prepaid' AND x.exclude_voucher));

CREATE VIEW reporting.eng_customers WITH (security_invoker = true) AS
SELECT c.id AS customer_id, c.property_id, c.code, c.name, c.customer_type, c.status, c.created_at,
       EXISTS (SELECT 1 FROM membership.members m JOIN membership.memberships ms ON ms.member_id = m.id
               WHERE m.customer_id = c.id AND ms.status = 'active') AS member
FROM crm.customers c WHERE c.status <> 'merged';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.eng_customers, reporting.eng_spend, reporting.eng_top_spender_policy;
DROP VIEW reporting.eng_nps, reporting.eng_tickets, reporting.eng_campaign_recipients, reporting.eng_campaigns, reporting.eng_reward_redemptions,
  reporting.eng_loyalty_ledger, reporting.eng_loyalty_accounts, reporting.eng_loyalty_value, reporting.eng_products, reporting.eng_outlets,
  reporting.eng_credit_notes, reporting.eng_invoices, reporting.eng_refunds, reporting.eng_payments, reporting.eng_folios, reporting.eng_folio_lines;
