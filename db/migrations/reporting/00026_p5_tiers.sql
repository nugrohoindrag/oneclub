-- Member tier classes & classification (PRD P5 EP-18, product owner
-- request) read models:
--  * crm_membership_types: membership types for the tier qualification rule;
--  * crm_member_tiers: memberships with the tier class of their member
--    (Membership → Members column and filter by tier);
--  * crm_tier_members: active loyalty accounts with tier class and source
--    (Members by Tier Report);
--  * crm_tier_movements: tier evaluation runs and manual overrides
--    (movement up / down per evaluation);
--  * crm_tier_benefits: + badge colour, icon and classification source.
-- security_invoker: Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.crm_membership_types WITH (security_invoker = true) AS
SELECT t.id, t.property_id, t.code, t.name, t.status FROM membership.types t;

CREATE VIEW reporting.crm_member_tiers WITH (security_invoker = true) AS
SELECT ms.id AS membership_id, ms.property_id, m.id AS member_id, m.code AS member_no, m.name AS member_name, m.customer_id, ms.type_id, t.name AS type_name,
       ms.role, ms.status, ms.starts_on, ms.ends_on, a.id AS account_id, lt.id AS tier_id, lt.code AS tier_code, lt.name AS tier_name, lt.rank AS tier_rank,
       lt.color AS tier_color, lt.icon AS tier_icon, a.tier_source, o.valid_until AS override_until
FROM membership.memberships ms
JOIN membership.members m ON m.id = ms.member_id
JOIN membership.types t ON t.id = ms.type_id
LEFT JOIN crm.loyalty_accounts a ON a.customer_id = m.customer_id AND a.status = 'active'
LEFT JOIN crm.loyalty_tiers lt ON lt.id = a.tier_id
LEFT JOIN crm.loyalty_tier_overrides o ON o.id = a.tier_override_id AND o.status = 'active';

CREATE VIEW reporting.crm_tier_members WITH (security_invoker = true) AS
SELECT a.id AS account_id, a.property_id, a.customer_id, a.tier_id, t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank, t.color AS tier_color,
       a.tier_source, a.tier_override_id IS NOT NULL AS overridden, a.grace_until, a.tier_since
FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id WHERE a.status = 'active';

CREATE VIEW reporting.crm_tier_movements WITH (security_invoker = true) AS
SELECT e.id AS movement_id, e.property_id, 'evaluation' AS movement, e.number, e.kind, e.evaluated_on AS moved_on, e.accounts, e.upgraded, e.downgraded,
       e.retained, e.grace_started, e.in_grace, jsonb_array_length(e.applied_versions) AS versions_applied, e.created_at
FROM crm.loyalty_tier_evaluations e
UNION ALL
SELECT o.id, o.property_id, 'override', o.number, o.status, (o.applied_at AT TIME ZONE coalesce((SELECT timezone FROM platform.instance), 'Asia/Jakarta'))::date,
       1, CASE WHEN coalesce(t.rank, 0) > coalesce(pt.rank, 0) THEN 1 ELSE 0 END, CASE WHEN coalesce(t.rank, 0) < coalesce(pt.rank, 0) THEN 1 ELSE 0 END,
       CASE WHEN coalesce(t.rank, 0) = coalesce(pt.rank, 0) THEN 1 ELSE 0 END, 0, 0, 0, o.created_at
FROM crm.loyalty_tier_overrides o JOIN crm.loyalty_tiers t ON t.id = o.tier_id LEFT JOIN crm.loyalty_tiers pt ON pt.id = o.previous_tier_id
WHERE o.applied_at IS NOT NULL;

CREATE OR REPLACE VIEW reporting.crm_tier_benefits WITH (security_invoker = true) AS
SELECT a.customer_id, a.property_id, a.id AS account_id, t.id AS tier_id, t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank,
       t.multiplier, t.booking_window_days, t.fnb_discount_percent, t.event_access, t.priority_service, a.grace_until,
       t.color AS tier_color, t.icon AS tier_icon, a.tier_source
FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id
WHERE a.status = 'active';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.crm_tier_benefits;
CREATE VIEW reporting.crm_tier_benefits WITH (security_invoker = true) AS
SELECT a.customer_id, a.property_id, a.id AS account_id, t.id AS tier_id, t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank,
       t.multiplier, t.booking_window_days, t.fnb_discount_percent, t.event_access, t.priority_service, a.grace_until
FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id
WHERE a.status = 'active';
DROP VIEW reporting.crm_tier_movements, reporting.crm_tier_members, reporting.crm_member_tiers, reporting.crm_membership_types;
