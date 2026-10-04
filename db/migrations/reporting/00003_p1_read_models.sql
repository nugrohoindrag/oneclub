-- P1 read models (EP-16, EP-01 Customer 360). Views use security_invoker so
-- Row Level Security of the source tables applies to the caller. Reports and
-- dashboards query these views on the read replica (FR-REP-01); other
-- modules read cross-domain data only through them (Technical Doc §4.2).

-- +goose Up
-- Players of active bookings (Daily Tee Sheet Report, Today's Players).
CREATE VIEW reporting.golf_players WITH (security_invoker = true) AS
SELECT bp.id AS player_id, bp.property_id, b.id AS booking_id, b.code AS booking_code, b.booking_type, b.channel, b.status AS booking_status,
       f.play_date, t.start_at, t.start_tee, t.session, c.id AS course_id, c.name AS course_name, f.id AS flight_id, f.flight_no,
       f.status AS flight_status, f.ready_at, f.tee_off_at, f.round_finish_at, bp.seq, bp.name AS player_name, bp.player_type, bp.segment,
       bp.status AS player_status, bp.checked_in_at, bp.customer_id, bp.member_id, bp.price_total,
       (SELECT cd.code || ' ' || cd.name FROM golf.caddy_assignments ca JOIN golf.caddies cd ON cd.id = ca.caddy_id
         WHERE ca.flight_id = f.id AND bp.id = ANY (ca.player_ids) AND ca.status IN ('assigned', 'in_play', 'completed')
         ORDER BY ca.assigned_at DESC LIMIT 1) AS caddy
FROM golf.booking_players bp
JOIN golf.bookings b ON b.id = bp.booking_id
JOIN golf.flights f ON f.id = bp.flight_id
JOIN golf.tee_times t ON t.id = f.tee_time_id
JOIN golf.courses c ON c.id = f.course_id
WHERE b.status IN ('pending', 'confirmed', 'checked_in', 'completed') AND bp.status IN ('booked', 'checked_in');

CREATE VIEW reporting.golf_bookings WITH (security_invoker = true) AS
SELECT b.id AS booking_id, b.property_id, b.code, b.booking_type, b.channel, b.status, b.play_date, b.start_at, b.course_id, c.name AS course_name,
       b.player_count, b.contact_name, b.customer_id, b.payment_mode, b.created_at, b.cancelled_at, b.cancel_reason, b.no_show_at, b.folio_id,
       coalesce((SELECT sum(l.total) FROM billing.folio_lines l WHERE l.folio_id = b.folio_id AND l.voided_at IS NULL), 0) AS charges,
       coalesce((SELECT sum(l.total) FROM billing.folio_lines l WHERE l.folio_id = b.folio_id AND l.voided_at IS NULL
                 AND l.charge_type IN ('cancellation_fee', 'no_show_fee')), 0) AS fees
FROM golf.bookings b JOIN golf.courses c ON c.id = b.course_id
WHERE b.status <> 'draft';

CREATE VIEW reporting.golf_caddy_assignments WITH (security_invoker = true) AS
SELECT a.id AS assignment_id, a.property_id, a.play_date, cd.id AS caddy_id, cd.code AS caddy_code, cd.name AS caddy_name, a.status, lower(a.period) AS window_start,
       a.fee_amount, a.started_at, a.finished_at, b.code AS booking_code, cardinality(a.player_ids) AS players,
       coalesce((SELECT sum(t.amount) FROM golf.caddy_tips t WHERE t.assignment_id = a.id), 0) AS tips
FROM golf.caddy_assignments a JOIN golf.caddies cd ON cd.id = a.caddy_id
JOIN golf.flights f ON f.id = a.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id;

CREATE VIEW reporting.golf_cart_usage WITH (security_invoker = true) AS
SELECT a.id AS assignment_id, a.property_id, a.play_date, gc.id AS golf_cart_id, gc.code AS golf_cart_code, gc.cart_type, a.status, a.extra,
       a.fee_amount, a.out_at, a.returned_at, b.code AS booking_code,
       CASE WHEN a.out_at IS NOT NULL AND a.returned_at IS NOT NULL THEN extract(epoch FROM a.returned_at - a.out_at) / 60 END AS minutes_out
FROM golf.golf_cart_assignments a JOIN golf.golf_carts gc ON gc.id = a.golf_cart_id
JOIN golf.flights f ON f.id = a.flight_id LEFT JOIN golf.bookings b ON b.id = f.booking_id;

CREATE VIEW reporting.golf_rain_checks WITH (security_invoker = true) AS
SELECT r.id AS rain_check_id, r.property_id, r.number, r.created_at::date AS issued_on, b.code AS booking_code, bp.name AS player_name,
       r.holes_played, r.holes_total, r.credit_percent, r.credit_amount, r.expires_on,
       CASE WHEN r.status = 'issued' AND r.expires_on < current_date THEN 'expired' ELSE r.status END AS status, r.redeemed_at
FROM golf.rain_checks r JOIN golf.bookings b ON b.id = r.booking_id JOIN golf.booking_players bp ON bp.id = r.booking_player_id;

-- Revenue per all-in component and segment (Golf Revenue Report, FR-RPT-04).
CREATE VIEW reporting.folio_components WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.folio_id, l.posted_at, l.charge_type, l.liability AS line_liability, s.segment,
       coalesce(comp->>'code', l.charge_type) AS component_code, coalesce(comp->>'name', l.description) AS component_name,
       coalesce((comp->>'liability')::boolean, l.liability) AS liability,
       coalesce((comp->>'amount')::numeric, l.net_amount) AS amount, l.tax_amount, l.service_amount, l.total, l.currency,
       f.source_type
FROM billing.folio_lines l
JOIN billing.folios f ON f.id = l.folio_id
LEFT JOIN commercial.pricing_snapshots s ON s.id = l.pricing_snapshot_id
LEFT JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(l.components) = 'array' AND jsonb_array_length(l.components) > 0
     THEN l.components ELSE '[null]'::jsonb END) comp ON true
WHERE l.voided_at IS NULL;

CREATE VIEW reporting.payments WITH (security_invoker = true) AS
SELECT p.id AS payment_id, p.property_id, p.number, p.method_type, p.channel, p.purpose, p.status, p.amount, p.refunded_amount, p.currency,
       p.paid_at, p.created_at, f.number AS folio_number, f.source_type, f.customer_id, u.full_name AS received_by, p.integration_code, p.external_id
FROM billing.payments p LEFT JOIN billing.folios f ON f.id = p.folio_id LEFT JOIN platform.users u ON u.id = p.received_by;

CREATE VIEW reporting.refunds WITH (security_invoker = true) AS
SELECT r.id AS refund_id, r.property_id, r.number, r.amount, r.currency, r.destination, r.status, r.reason, r.created_at, r.processed_at,
       p.number AS payment_number, p.method_type
FROM billing.refunds r JOIN billing.payments p ON p.id = r.payment_id;

CREATE VIEW reporting.member_accounts WITH (security_invoker = true) AS
SELECT a.id AS account_id, a.property_id, a.number, a.customer_id, c.name AS holder_name, a.account_type, a.member_id, a.credit_limit, a.status,
       coalesce((SELECT sum(e.amount) FROM billing.account_entries e WHERE e.account_id = a.id), 0) AS balance,
       (SELECT max(e.occurred_at) FROM billing.account_entries e WHERE e.account_id = a.id) AS last_activity
FROM billing.customer_accounts a JOIN crm.customers c ON c.id = a.customer_id;

CREATE VIEW reporting.memberships WITH (security_invoker = true) AS
SELECT ms.id AS membership_id, ms.property_id, m.id AS member_id, m.code AS member_no, m.name AS member_name, m.customer_id, t.name AS type_name,
       t.category, ms.role, ms.status, ms.starts_on, ms.ends_on, ms.activated_at, ms.created_at, m.joined_on
FROM membership.memberships ms JOIN membership.members m ON m.id = ms.member_id JOIN membership.types t ON t.id = ms.type_id;

-- Customer History (FR-CUS-04): bookings / rounds, payments and member charges.
CREATE VIEW reporting.customer_history WITH (security_invoker = true) AS
SELECT b.property_id, coalesce(bp.customer_id, b.customer_id) AS customer_id, 'round' AS kind, b.start_at AS occurred_at,
       b.code AS reference, c.name || ' · ' || bp.player_type AS description, bp.price_total AS amount, bp.status
FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id JOIN golf.courses c ON c.id = b.course_id
WHERE b.status <> 'draft' AND coalesce(bp.customer_id, b.customer_id) IS NOT NULL
UNION ALL
SELECT b.property_id, g.customer_id, 'round', b.start_at, b.code, c.name || ' · ' || bp.player_type, bp.price_total, bp.status
FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id JOIN golf.courses c ON c.id = b.course_id JOIN crm.guests g ON g.id = bp.guest_id
WHERE b.status <> 'draft' AND g.customer_id IS NOT NULL AND bp.customer_id IS NULL
UNION ALL
SELECT p.property_id, f.customer_id, 'payment', coalesce(p.paid_at, p.created_at), p.number, p.method_type || ' · ' || coalesce(f.source_ref, f.number), p.amount, p.status
FROM billing.payments p JOIN billing.folios f ON f.id = p.folio_id WHERE f.customer_id IS NOT NULL
UNION ALL
SELECT e.property_id, a.customer_id, 'member_charge', e.occurred_at, a.number, e.description, e.amount, e.entry_type
FROM billing.account_entries e JOIN billing.customer_accounts a ON a.id = e.account_id WHERE e.entry_type = 'charge';

-- Handicap Index in force.
CREATE VIEW reporting.handicaps WITH (security_invoker = true) AS
SELECT DISTINCT ON (customer_id) customer_id, property_id, handicap_index, effective_at
FROM golf.handicaps ORDER BY customer_id, effective_at DESC;

-- Live golf operations (Executive Overview golf widgets, FR-RPT-01).
CREATE VIEW reporting.golf_starter_queue WITH (security_invoker = true) AS
SELECT flight_id, property_id, course_id, play_date, status, ready_at, dispatched_at FROM golf.starter_queue;

CREATE VIEW reporting.golf_caddy_attendance WITH (security_invoker = true) AS
SELECT a.caddy_id, a.property_id, a.work_date, a.status,
       EXISTS (SELECT 1 FROM golf.caddy_assignments x WHERE x.caddy_id = a.caddy_id AND x.play_date = a.work_date
               AND x.status IN ('assigned', 'in_play')) AS engaged
FROM golf.caddy_attendance a;

CREATE VIEW reporting.golf_tee_times WITH (security_invoker = true) AS
SELECT id AS tee_time_id, property_id, course_id, play_date, start_at, start_tee, session, capacity, status FROM golf.tee_times WHERE status <> 'closed';

CREATE VIEW reporting.golf_carts WITH (security_invoker = true) AS
SELECT id AS golf_cart_id, property_id, code, cart_type, readiness, status FROM golf.golf_carts WHERE archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- The reporting role never reads one-time login codes.
-- +goose StatementBegin
DO $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NOT NULL AND rep <> '' THEN
    EXECUTE format('REVOKE SELECT ON platform.login_codes FROM %I', rep);
    EXECUTE format('REVOKE SELECT ON golf.bookings FROM %I', rep);
    EXECUTE format('GRANT SELECT (id, property_id, code, booking_type, channel, status, parent_booking_id, course_id, tee_time_id, play_date, start_at,
      playing_route_id, player_count, customer_id, guest_id, member_id, corporate_account_id, contact_name, contact_phone, contact_email, hold_expires_at,
      payment_mode, payment_due_at, deposit_amount, folio_id, policy_versions, reschedule_count, cart_request, caddy_request, notes, confirmed_at,
      checked_in_at, completed_at, cancelled_at, cancel_reason, no_show_at, legacy_ref, created_at, created_by, updated_at, updated_by)
      ON golf.bookings TO %I', rep);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP VIEW reporting.golf_carts, reporting.golf_tee_times, reporting.golf_caddy_attendance, reporting.golf_starter_queue, reporting.handicaps, reporting.customer_history, reporting.memberships, reporting.member_accounts, reporting.refunds, reporting.payments,
  reporting.folio_components, reporting.golf_rain_checks, reporting.golf_cart_usage, reporting.golf_caddy_assignments, reporting.golf_bookings,
  reporting.golf_players;
