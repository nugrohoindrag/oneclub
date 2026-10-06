-- PRD P5 EP-17–20 read models of advanced CRM & loyalty (FR-RPT-P5-03):
--  * crm_tier_benefits: the loyalty tier benefits of a customer (booking
--    window + days, F&B discount, event access) — the public read model for
--    pricing / booking (FR-LOY-P5-02);
--  * crm_banquet_event_customers: the customer of a banquet event (journey
--    triggers on banquet.event_completed);
--  * crm_journey_events: executed journey steps with delivery / read /
--    click and the conversion of the enrollment (FR-JRN-07);
--  * crm_loyalty_issues, crm_tier_evaluations, crm_rfm_scores, crm_vip:
--    reward issues, tier evaluation lines, RFM snapshots and VIPs.
-- security_invoker: Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.crm_tier_benefits WITH (security_invoker = true) AS
SELECT a.customer_id, a.property_id, a.id AS account_id, t.id AS tier_id, t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank,
       t.multiplier, t.booking_window_days, t.fnb_discount_percent, t.event_access, t.priority_service, a.grace_until
FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id
WHERE a.status = 'active';

CREATE VIEW reporting.crm_banquet_event_customers WITH (security_invoker = true) AS
SELECT e.id AS event_id, e.property_id, e.customer_id, e.number FROM banquet.events e;

CREATE VIEW reporting.crm_journey_events WITH (security_invoker = true) AS
SELECT ev.id AS event_id, ev.property_id, ev.journey_id, j.code AS journey_code, j.name AS journey_name, j.category, ev.enrollment_id, ev.customer_id,
       ev.step_key, s.name AS step_name, ev.step_type, ev.outcome, ev.channel, ev.variant, ev.created_at AS sent_at, ev.clicked_at,
       en.cohort, en.converted_at, en.revenue, en.status AS enrollment_status,
       d.delivery_status, d.delivered_at, coalesce(ev.read_at, d.read_at, n.read_at) AS read_at
FROM crm.journey_events ev
JOIN crm.journeys j ON j.id = ev.journey_id
JOIN crm.journey_enrollments en ON en.id = ev.enrollment_id
LEFT JOIN crm.journey_steps s ON s.journey_id = ev.journey_id AND s.key = ev.step_key
LEFT JOIN LATERAL (SELECT x.delivery_status, x.delivered_at, x.read_at FROM platform.notification_deliveries x
                   WHERE ev.token IS NOT NULL AND x.payload->>'trackingToken' = ev.token ORDER BY x.created_at DESC LIMIT 1) d ON true
LEFT JOIN LATERAL (SELECT y.read_at FROM platform.notifications y WHERE ev.channel = 'in_app' AND ev.token IS NOT NULL AND y.link = '/loyalty/offers/' || ev.token
                   ORDER BY y.created_at DESC LIMIT 1) n ON true;

CREATE VIEW reporting.crm_journey_enrollments WITH (security_invoker = true) AS
SELECT en.id AS enrollment_id, en.property_id, en.journey_id, j.code AS journey_code, j.name AS journey_name, j.status AS journey_status,
       en.customer_id, en.cohort, en.status, en.entered_at, en.completed_at, en.exited_at, en.exit_reason, en.converted_at, en.revenue
FROM crm.journey_enrollments en JOIN crm.journeys j ON j.id = en.journey_id;

CREATE VIEW reporting.crm_loyalty_issues WITH (security_invoker = true) AS
SELECT i.id AS issue_id, i.property_id, i.number, i.customer_id, c.name AS customer_name, w.code AS reward_code, w.name AS reward_name, w.reward_type,
       i.quantity, i.source, i.source_ref, i.period, i.unit_cost, i.total_cost, i.status, i.created_at
FROM crm.loyalty_reward_issues i JOIN crm.customers c ON c.id = i.customer_id JOIN crm.loyalty_rewards w ON w.id = i.reward_id;

CREATE VIEW reporting.crm_reward_redemption_costs WITH (security_invoker = true) AS
SELECT r.id AS redemption_id, r.property_id, r.quantity * w.unit_cost AS cost, r.status, r.created_at
FROM crm.loyalty_reward_redemptions r JOIN crm.loyalty_rewards w ON w.id = r.reward_id;

CREATE VIEW reporting.crm_tier_evaluations WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, e.number AS evaluation_number, e.kind, e.evaluated_on, a.number AS account_number, a.customer_id,
       c.name AS customer_name, ft.name AS from_tier, qt.name AS qualified_tier, tt.name AS to_tier, l.outcome, l.spend_basis, l.points_basis, l.grace_until
FROM crm.loyalty_tier_evaluation_lines l
JOIN crm.loyalty_tier_evaluations e ON e.id = l.evaluation_id
JOIN crm.loyalty_accounts a ON a.id = l.account_id JOIN crm.customers c ON c.id = a.customer_id
LEFT JOIN crm.loyalty_tiers ft ON ft.id = l.from_tier_id LEFT JOIN crm.loyalty_tiers qt ON qt.id = l.qualified_tier_id
LEFT JOIN crm.loyalty_tiers tt ON tt.id = l.to_tier_id;

CREATE VIEW reporting.crm_tier_accounts WITH (security_invoker = true) AS
SELECT a.id AS account_id, a.property_id, a.customer_id, t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank, a.grace_until, a.tier_since,
       a.balance, a.lifetime_points
FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id WHERE a.status = 'active';

CREATE VIEW reporting.crm_rfm_scores WITH (security_invoker = true) AS
SELECT s.property_id, s.as_of, s.customer_id, c.code AS customer_code, c.name AS customer_name, s.recency_days, s.frequency, s.monetary,
       s.r_score, s.f_score, s.m_score, s.rfm_group, s.lines, s.clv
FROM crm.rfm_scores s JOIN crm.customers c ON c.id = s.customer_id;

CREATE VIEW reporting.crm_vip WITH (security_invoker = true) AS
SELECT v.id AS vip_id, v.property_id, v.customer_id, c.code AS customer_code, c.name AS customer_name, v.level, v.source, v.reason, v.benefits,
       v.handling_note, v.valid_until, v.status, v.created_at, v.updated_at
FROM crm.vip_customers v JOIN crm.customers c ON c.id = v.customer_id;

-- Commission of a sales person per period (FR-CRA-04).
CREATE VIEW reporting.crm_sales_owner_commissions WITH (security_invoker = true) AS
SELECT c.property_id, c.user_id, u.full_name AS user_name, c.kind, c.basis_amount, c.amount, c.recognized_on, c.scheme_id, s.name AS scheme_name
FROM crm.sales_commissions c JOIN platform.users u ON u.id = c.user_id LEFT JOIN crm.sales_commission_schemes s ON s.id = c.scheme_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.crm_sales_owner_commissions, reporting.crm_vip, reporting.crm_rfm_scores, reporting.crm_tier_accounts, reporting.crm_tier_evaluations,
  reporting.crm_reward_redemption_costs, reporting.crm_loyalty_issues, reporting.crm_journey_enrollments, reporting.crm_journey_events,
  reporting.crm_banquet_event_customers, reporting.crm_tier_benefits;
