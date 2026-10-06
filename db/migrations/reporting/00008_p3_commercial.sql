-- PRD P3 Pricing & Promotion Engine and Package Management read models
-- (EP-10/11, EP-23 FR-RPT-P3-03/05): Promotion Performance Report, Package
-- Sales Report, Package Revenue Allocation Report and the promotion and
-- package KPIs of the Commercial Performance dashboard.
-- Views use security_invoker so Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.commercial_promotions WITH (security_invoker = true) AS
SELECT p.id AS promotion_id, p.property_id, p.code, p.name, p.promo_type, p.status, p.priority, p.stackable, p.requires_code, p.channels,
       p.business_lines, p.valid_from, p.valid_to, p.budget_amount, p.max_redemptions, p.used_count, p.used_amount, p.version, p.activated_at
FROM commercial.promotions p WHERE p.archived_at IS NULL;

-- One row per application of a promotion (Applied, Redeemed or Reversed).
CREATE VIEW reporting.commercial_promotion_redemptions WITH (security_invoker = true) AS
SELECT r.id AS redemption_id, r.property_id, r.promotion_id, r.promotion_code, p.name AS promotion_name, p.promo_type, r.promotion_version,
       r.promo_code, r.source_type, r.source_id, r.source_ref, r.customer_id, r.channel, r.business_line, r.discount_amount, r.currency, r.offline,
       r.needs_review, r.status, r.created_at, r.redeemed_at, r.reversed_at
FROM commercial.promotion_redemptions r JOIN commercial.promotions p ON p.id = r.promotion_id;

CREATE VIEW reporting.commercial_package_bookings WITH (security_invoker = true) AS
SELECT b.id AS booking_id, b.property_id, b.number, b.package_id, b.package_code, p.name AS package_name, p.package_type, b.package_version,
       b.channel, b.status, b.start_date, b.pax, b.nights, b.currency, b.list_total, b.discount_total, b.net_total, b.service_total, b.tax_total,
       b.total, b.cancellation_fee, coalesce(c.name, b.guest_name) AS customer_name, b.created_at, b.confirmed_at, b.completed_at, b.cancelled_at
FROM commercial.package_bookings b JOIN commercial.packages p ON p.id = b.package_id LEFT JOIN crm.customers c ON c.id = b.customer_id;

-- Revenue allocation per component of the package bookings (K3) with what
-- was consumed (FR-PKG-05/06).
CREATE VIEW reporting.commercial_package_allocations WITH (security_invoker = true) AS
SELECT c.id AS booking_component_id, c.property_id, b.id AS booking_id, b.number, b.package_code, b.status AS booking_status, b.confirmed_at,
       c.name AS component_name, c.component_type, c.revenue_component, c.business_line, c.liability, c.quantity, c.consumed_quantity, c.service_date,
       c.allocated_net, c.allocated_service, c.allocated_tax, c.allocated_total, c.status,
       coalesce((SELECT sum(pc.total) FROM commercial.package_consumptions pc WHERE pc.booking_component_id = c.id), 0) AS consumed_total
FROM commercial.package_booking_components c JOIN commercial.package_bookings b ON b.id = c.booking_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.commercial_package_allocations, reporting.commercial_package_bookings, reporting.commercial_promotion_redemptions,
  reporting.commercial_promotions;
