-- PRD P2 read models (EP-27 reports & KPI dashboards) next to P1's. Views
-- use security_invoker so Row Level Security of the source tables applies;
-- P2 reports and dashboards query only these views on the read replica
-- (Technical Doc §4.2).

-- +goose Up
-- ── golf ──────────────────────────────────────────────────────────────────
-- Finished rounds per player (Round History, Golf Performance).
CREATE VIEW reporting.golf_rounds WITH (security_invoker = true) AS
SELECT bp.id AS player_id, bp.property_id, f.id AS flight_id, f.play_date, f.tee_off_at, f.round_finish_at, b.code AS booking_code, f.flight_no,
       r.name AS route_name, bp.name AS player_name, bp.player_type, bp.customer_id, s.gross, s.status AS scorecard_status,
       round(extract(epoch FROM f.round_finish_at - f.tee_off_at) / 60)::int AS round_minutes,
       (SELECT string_agg(cd.code, ', ' ORDER BY a.assigned_at) FROM golf.caddy_assignments a JOIN golf.caddies cd ON cd.id = a.caddy_id
         WHERE a.flight_id = f.id AND bp.id = ANY (a.player_ids) AND a.status IN ('completed', 'replaced')) AS caddies
FROM golf.booking_players bp
JOIN golf.flights f ON f.id = bp.flight_id
JOIN golf.tee_times t ON t.id = f.tee_time_id
LEFT JOIN golf.bookings b ON b.id = bp.booking_id
LEFT JOIN golf.playing_routes r ON r.id = coalesce(b.playing_route_id, t.playing_route_id)
LEFT JOIN golf.scorecards s ON s.booking_player_id = bp.id
WHERE f.status = 'completed' AND bp.status = 'checked_in';

CREATE VIEW reporting.golf_caddies WITH (security_invoker = true) AS
SELECT c.id AS caddy_id, c.property_id, c.code, c.name, l.name AS level_name, c.status
FROM golf.caddies c LEFT JOIN golf.caddy_levels l ON l.id = c.level_id WHERE c.archived_at IS NULL;

-- Attendance days with duty hours (arrival → departure).
CREATE VIEW reporting.golf_caddy_duty WITH (security_invoker = true) AS
SELECT a.caddy_id, a.property_id, a.work_date, a.status, a.shift, a.arrived_at, a.departed_at,
       CASE WHEN a.status = 'present' AND a.arrived_at IS NOT NULL
            THEN extract(epoch FROM coalesce(a.departed_at, least(now(), a.arrived_at + interval '12 hours')) - a.arrived_at) / 3600 END AS duty_hours
FROM golf.caddy_attendance a;

CREATE VIEW reporting.golf_caddy_settlements WITH (security_invoker = true) AS
SELECT s.id AS settlement_id, s.property_id, s.number, c.code AS caddy_code, c.name AS caddy_name, s.period_start, s.period_end, s.rounds, s.caddy_fee,
       s.tips, s.deductions, s.total, s.status
FROM golf.caddy_settlements s JOIN golf.caddies c ON c.id = s.caddy_id;

CREATE VIEW reporting.golf_cart_maintenance WITH (security_invoker = true) AS
SELECT m.id AS maintenance_id, m.property_id, m.number, g.code AS golf_cart_code, m.category, m.description, m.cost, m.status, m.opened_at, m.closed_at,
       g.hours_since_service
FROM golf.cart_maintenance m JOIN golf.golf_carts g ON g.id = m.golf_cart_id;

CREATE VIEW reporting.golf_hole_in_ones WITH (security_invoker = true) AS
SELECT r.id AS hio_id, r.property_id, r.number, r.player_name, s.code || '-' || h.number AS hole, r.achieved_on, r.status, r.insured, r.claim_status,
       r.claim_paid_amount
FROM golf.hio_records r JOIN golf.holes h ON h.id = r.hole_id JOIN golf.course_sections s ON s.id = h.section_id;

CREATE VIEW reporting.golf_range_sessions WITH (security_invoker = true) AS
SELECT id AS session_id, property_id, area, status, queued_at, started_at, ended_at FROM golf.range_sessions;

CREATE VIEW reporting.golf_range_buckets WITH (security_invoker = true) AS
SELECT id AS bucket_id, property_id, session_id, balls, source, amount, created_at FROM golf.range_buckets;

CREATE VIEW reporting.golf_reciprocal_visits WITH (security_invoker = true) AS
SELECT v.id AS visit_id, v.property_id, v.number, v.direction, c.name AS club_name, c.country, v.visitor_name, v.visit_date, v.verified, v.settlement_status,
       coalesce((SELECT sum(p.price_total) FROM golf.booking_players p WHERE p.reciprocal_visit_id = v.id AND p.status IN ('booked', 'checked_in')),
                v.charge_amount) AS charge
FROM golf.reciprocal_visits v JOIN golf.reciprocal_clubs c ON c.id = v.club_id;

-- ── billing ───────────────────────────────────────────────────────────────
-- Club revenue lines per business line and revenue component (membership
-- fee folios of P1 count as membership).
CREATE VIEW reporting.revenue_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.folio_id, l.posted_at,
       CASE WHEN f.source_type IN ('membership_fee', 'membership_renewal', 'membership') THEN 'membership' ELSE l.business_line END AS business_line,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.liability, l.total, f.customer_id, f.reservation_id, f.source_type
FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
WHERE l.voided_at IS NULL;

CREATE VIEW reporting.voucher_ledger WITH (security_invoker = true) AS
SELECT e.id AS entry_id, e.property_id, e.liability_type, e.entry_type, e.amount, e.occurred_at, v.id AS voucher_id, t.name AS voucher_type
FROM billing.deferred_revenue_entries e JOIN commercial.vouchers v ON v.id = e.ref_id JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
WHERE e.ref_type = 'commercial.voucher';

-- ── membership ────────────────────────────────────────────────────────────
CREATE VIEW reporting.membership_lifecycle WITH (security_invoker = true) AS
SELECT ms.id AS membership_id, ms.property_id, p.name AS program_name, p.program_kind, t.id AS type_id, t.name AS type_name, ms.role, ms.status,
       ms.starts_on, ms.ends_on, m.customer_id
FROM membership.memberships ms JOIN membership.members m ON m.id = ms.member_id JOIN membership.types t ON t.id = ms.type_id
JOIN membership.programs p ON p.id = t.program_id;

CREATE VIEW reporting.membership_events WITH (security_invoker = true) AS
SELECT h.id AS event_id, h.property_id, h.membership_id, ms.type_id, h.event, h.occurred_at
FROM membership.history h JOIN membership.memberships ms ON ms.id = h.membership_id;

-- ── reservation, sport club, stay ─────────────────────────────────────────
CREATE VIEW reporting.reservations WITH (security_invoker = true) AS
SELECT id AS reservation_id, property_id, code, kind, business_line, status, channel, created_at, cancelled_at, updated_at FROM reservation.reservations;

CREATE VIEW reporting.reservation_lines WITH (security_invoker = true) AS
SELECT id AS line_id, property_id, reservation_id, resource_id, resource_type, period, status FROM reservation.reservation_lines;

CREATE VIEW reporting.opening_hours WITH (security_invoker = true) AS
SELECT code AS resource_type, open_time, close_time, slot_minutes FROM reservation.resource_types WHERE archived_at IS NULL;

CREATE VIEW reporting.sport_courts WITH (security_invoker = true) AS
SELECT c.id AS court_id, c.property_id, c.name AS court_name, f.name AS facility_name, c.resource_id, c.status
FROM sportclub.courts c JOIN sportclub.facilities f ON f.id = c.facility_id WHERE c.archived_at IS NULL;

CREATE VIEW reporting.sport_entries WITH (security_invoker = true) AS
SELECT id AS entry_id, property_id, entry_type, adults, children, visit_date, status FROM sportclub.entries;

CREATE VIEW reporting.sport_enrollments WITH (security_invoker = true) AS
SELECT id AS enrollment_id, property_id, program_id, status FROM sportclub.enrollments;

CREATE VIEW reporting.sport_class_sessions WITH (security_invoker = true) AS
SELECT s.id AS session_id, s.property_id, lower(s.period) AS starts_at, p.name AS program_name, i.name AS instructor_name, s.capacity, s.status,
       (SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status <> 'cancelled') AS booked,
       (SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'present') AS present,
       (SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'absent') AS absent,
       (SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'excused') AS excused
FROM sportclub.class_sessions s JOIN sportclub.class_programs p ON p.id = s.program_id JOIN sportclub.instructors i ON i.id = s.instructor_id;

CREATE VIEW reporting.sport_instructor_fees WITH (security_invoker = true) AS
SELECT f.id AS fee_id, f.property_id, f.fee_no, i.name AS instructor_name, f.period_start, f.period_end, f.sessions, f.students, f.amount, f.status
FROM sportclub.instructor_fees f JOIN sportclub.instructors i ON i.id = f.instructor_id;

CREATE VIEW reporting.stay_bungalows WITH (security_invoker = true) AS
SELECT id AS bungalow_id, property_id, code, name, status FROM stay.bungalows WHERE archived_at IS NULL;

CREATE VIEW reporting.stays WITH (security_invoker = true) AS
SELECT s.id AS stay_id, s.property_id, s.stay_no, s.kind, s.unit_id, coalesce(mr.name, b.name) AS unit_name, s.start_at, s.end_at, s.pax, s.package_code,
       s.layout, coalesce(c.name, s.guest_name, s.corporate_name) AS customer_name, s.status
FROM stay.stays s LEFT JOIN stay.meeting_rooms mr ON mr.id = s.unit_id LEFT JOIN stay.bungalows b ON b.id = s.unit_id
LEFT JOIN crm.customers c ON c.id = s.customer_id;

-- ── commercial & inventory ────────────────────────────────────────────────
CREATE VIEW reporting.pos_sales WITH (security_invoker = true) AS
SELECT l.id AS line_id, o.property_id, o.id AS order_id, o.created_at, o.outlet_id, ou.name AS outlet_name, l.unit_price * l.quantity AS gross,
       l.discount_amount, l.net_amount, l.service_amount, l.tax_amount, l.total_amount
FROM commercial.orders o JOIN commercial.outlets ou ON ou.id = o.outlet_id JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active'
WHERE o.status IN ('paid', 'charged');

CREATE VIEW reporting.pos_shifts WITH (security_invoker = true) AS
SELECT s.id AS shift_id, s.property_id, s.shift_no, o.name AS outlet_name, u.full_name AS cashier_name, s.opened_at, s.closed_at, s.opening_cash,
       s.expected_cash, s.counted_cash, s.variance, s.status
FROM commercial.pos_shifts s JOIN commercial.outlets o ON o.id = s.outlet_id JOIN platform.users u ON u.id = s.cashier_id;

CREATE VIEW reporting.food_cost WITH (security_invoker = true) AS
SELECT s.source_id, s.property_id, s.outlet_id, coalesce(o.name, '-') AS outlet_name, p.name AS product_name, s.quantity, s.net_amount, s.cost_amount,
       s.has_recipe, s.occurred_at
FROM inventory.consumption_sales s JOIN commercial.products p ON p.id = s.product_id LEFT JOIN commercial.outlets o ON o.id = s.outlet_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.food_cost, reporting.pos_shifts, reporting.pos_sales, reporting.stays, reporting.stay_bungalows, reporting.sport_instructor_fees,
  reporting.sport_class_sessions, reporting.sport_enrollments, reporting.sport_entries, reporting.sport_courts, reporting.opening_hours,
  reporting.reservation_lines, reporting.reservations, reporting.membership_events, reporting.membership_lifecycle, reporting.voucher_ledger,
  reporting.revenue_lines, reporting.golf_reciprocal_visits, reporting.golf_range_buckets, reporting.golf_range_sessions, reporting.golf_hole_in_ones,
  reporting.golf_cart_maintenance, reporting.golf_caddy_settlements, reporting.golf_caddy_duty, reporting.golf_caddies, reporting.golf_rounds;
