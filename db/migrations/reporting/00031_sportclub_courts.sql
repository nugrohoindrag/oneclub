-- Sport Club court booking read models (docs/requirement-booking-sportclub-
-- mgcc.md §6.5, §7): one row per booked court line for the booking report
-- and its export (FR-83), the online payments of the Sport Club for the
-- settlement report (FR-84, FR-95), the cost center of folio lines for the
-- profit center drill-down per sport (FR-91, FR-104) and the Sport Club
-- incidents next to the golf and banquet ones (FR-107).

-- +goose Up
CREATE OR REPLACE VIEW reporting.acc_folio_lines WITH (security_invoker = true) AS
SELECT l.id, l.property_id, l.folio_id, f.number AS folio_number, l.business_date, l.posted_at, l.voided_at, l.business_line,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.charge_type, l.liability, l.net_amount, l.service_amount, l.tax_amount,
       l.total, l.currency, l.components, l.tax_lines, l.reference_type, l.reference_id, l.beneficiary_type, l.beneficiary_id, l.description,
       f.source_type AS folio_source_type, f.customer_id, f.corporate_account_id, l.invoice_id, l.cost_center
FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id;

-- One row per court line. channel: website, member_app, walk_in, phone,
-- recurring (FR-53); pay_status from the folio (FR-124): a package pays the
-- whole booking.
CREATE VIEW reporting.sport_court_lines WITH (security_invoker = true) AS
WITH fo AS (
  SELECT f.id AS folio_id,
         coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = f.id AND x.voided_at IS NULL), 0) AS charges,
         coalesce((SELECT sum(amount - refunded_amount) FROM billing.payments p WHERE p.folio_id = f.id AND p.status IN ('completed', 'refunded')
           AND p.purpose = 'settlement'), 0) AS paid,
         coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = f.id AND x.voided_at IS NULL AND x.revenue_component = 'gateway_fee'), 0) AS service_fee
  FROM billing.folios f
)
SELECT l.id AS line_id, l.property_id, r.id AS reservation_id, r.code, l.line_no, c.id AS court_id, c.name AS court_name, fa.id AS facility_id,
       fa.code AS facility_code, fa.name AS facility_name, lower(l.period) AS start_at, upper(l.period) AS end_at,
       extract(epoch FROM upper(l.period) - lower(l.period)) / 3600 AS hours, l.status AS line_status, r.status AS reservation_status,
       CASE WHEN r.recurring_group_id IS NOT NULL THEN 'recurring' WHEN r.attributes ->> 'via' = 'phone' THEN 'phone'
            WHEN r.channel IN ('walk_in', 'ops', 'back_office') THEN 'walk_in' ELSE r.channel END AS channel,
       r.customer_id, coalesce(cu.name, r.guest_name) AS customer_name, coalesce(cu.phone, r.guest_phone) AS phone,
       coalesce(l.amount, 0) AS amount, r.attributes ->> 'packageCode' AS package_code, r.attributes ->> 'promoCode' AS promo_code,
       r.folio_id, coalesce(fo.charges, 0) AS folio_charges, coalesce(fo.paid, 0) AS folio_paid, coalesce(fo.service_fee, 0) AS service_fee,
       CASE WHEN r.attributes ? 'packageCode' AND coalesce(fo.charges, 0) <= coalesce(fo.paid, 0) THEN 'paid'
            WHEN fo.folio_id IS NULL OR fo.charges = 0 THEN CASE WHEN r.attributes ? 'packageCode' THEN 'paid' ELSE 'unpaid' END
            WHEN fo.paid > fo.charges THEN 'overpaid' WHEN fo.paid = fo.charges THEN 'paid' WHEN fo.paid > 0 THEN 'partially_paid' ELSE 'unpaid' END AS pay_status,
       coalesce((r.attributes ->> 'void')::boolean, false) AS void, r.recurring_group_id, r.created_at
FROM reservation.reservation_lines l
JOIN reservation.reservations r ON r.id = l.reservation_id
JOIN sportclub.courts c ON c.resource_id = l.resource_id
JOIN sportclub.facilities fa ON fa.id = c.facility_id
LEFT JOIN crm.customers cu ON cu.id = r.customer_id
LEFT JOIN fo ON fo.folio_id = r.folio_id
WHERE r.kind = 'booking' AND r.business_line = 'sportclub';

-- Online payments of the Sport Club folios (mock gateway until the real one,
-- FR-84): rent and service fee per payment.
CREATE VIEW reporting.sport_settlements WITH (security_invoker = true) AS
SELECT p.id AS payment_id, p.property_id, p.number, p.method_type, p.channel, p.status, p.amount, p.refunded_amount, p.paid_at, p.created_at,
       p.integration_code, p.external_id, r.code AS booking_code, r.id AS reservation_id,
       coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = p.folio_id AND x.voided_at IS NULL AND x.revenue_component = 'gateway_fee'), 0) AS service_fee,
       coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = p.folio_id AND x.voided_at IS NULL
         AND coalesce(x.revenue_component, '') <> 'gateway_fee'), 0) AS rent
FROM billing.payments p
JOIN billing.folios f ON f.id = p.folio_id
JOIN reservation.reservations r ON r.folio_id = f.id AND r.business_line = 'sportclub' AND r.kind = 'booking'
WHERE p.channel = 'online';

CREATE OR REPLACE VIEW reporting.incidents WITH (security_invoker = true) AS
SELECT i.id, i.property_id, i.subject_type AS source, i.number, i.category, i.severity, i.status, i.description, i.action_taken,
       coalesce(c.code || ' · ' || c.name, g.code || ' · ' || g.name) AS subject, i.damage_amount, i.damage_status, i.occurred_at, i.created_at
FROM golf.incidents i
LEFT JOIN golf.caddies c ON c.id = i.caddy_id
LEFT JOIN golf.golf_carts g ON g.id = i.golf_cart_id
UNION ALL
SELECT b.id, b.property_id, 'banquet', e.number, 'event', b.severity, 'logged', b.note, NULL, e.number || ' · ' || e.title, NULL, NULL,
       b.created_at, b.created_at
FROM banquet.event_incidents b
JOIN banquet.events e ON e.id = b.event_id
UNION ALL
SELECT s.id, s.property_id, 'sportclub', s.number, s.category, s.severity, s.status, s.description, s.action_taken,
       coalesce(c.name, f.name, 'Sport Club'), s.damage_amount, NULL, s.occurred_at, s.created_at
FROM sportclub.incidents s
LEFT JOIN sportclub.courts c ON c.id = s.court_id
LEFT JOIN sportclub.facilities f ON f.id = s.facility_id;

SELECT platform.grant_app('reporting');

-- +goose Down
CREATE OR REPLACE VIEW reporting.incidents WITH (security_invoker = true) AS
SELECT i.id, i.property_id, i.subject_type AS source, i.number, i.category, i.severity, i.status, i.description, i.action_taken,
       coalesce(c.code || ' · ' || c.name, g.code || ' · ' || g.name) AS subject, i.damage_amount, i.damage_status, i.occurred_at, i.created_at
FROM golf.incidents i
LEFT JOIN golf.caddies c ON c.id = i.caddy_id
LEFT JOIN golf.golf_carts g ON g.id = i.golf_cart_id
UNION ALL
SELECT b.id, b.property_id, 'banquet', e.number, 'event', b.severity, 'logged', b.note, NULL, e.number || ' · ' || e.title, NULL, NULL,
       b.created_at, b.created_at
FROM banquet.event_incidents b
JOIN banquet.events e ON e.id = b.event_id;
DROP VIEW reporting.sport_settlements, reporting.sport_court_lines;
-- reporting.acc_folio_lines keeps its cost_center column (other views depend on it)
