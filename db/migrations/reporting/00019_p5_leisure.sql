-- PRD P5 EP-22/23 read models (FR-RPT-P5-03: Package Profitability Report,
-- Tournament Series Report) and the sources of the Package Profitability
-- API and of the Order of Merit (one definition each, PRD P5 FR-BI-07).
-- Views use security_invoker so Row Level Security of the source tables applies.

-- +goose Up
-- One line per revenue component (allocated net; liabilities as pass
-- through) and per cost line of the package bookings that took place
-- (confirmed, completed, or cancelled with consumed components / a fee).
CREATE VIEW reporting.commercial_package_profit_lines WITH (security_invoker = true) AS
SELECT x.*, row_number() OVER (PARTITION BY x.booking_id ORDER BY x.line_kind DESC, x.booking_component_id) = 1 AS first_line
FROM (
  SELECT c.property_id, b.id AS booking_id, b.number, b.package_id, b.package_code, p.name AS package_name, p.package_type, b.start_date,
         b.status AS booking_status, b.pax, c.id AS booking_component_id, c.name AS component_name, c.component_type,
         CASE WHEN c.liability THEN 'pass_through' ELSE 'revenue' END AS line_kind, NULL::text AS cost_type, NULL::text AS cost_basis,
         c.allocated_net AS amount
  FROM commercial.package_booking_components c JOIN commercial.package_bookings b ON b.id = c.booking_id JOIN commercial.packages p ON p.id = b.package_id
  WHERE b.status IN ('confirmed', 'completed', 'cancelled') AND (c.status <> 'cancelled' OR c.consumed_quantity > 0)
  UNION ALL
  SELECT b.property_id, b.id, b.number, b.package_id, b.package_code, p.name, p.package_type, b.start_date, b.status, b.pax, NULL::uuid,
         'Cancellation fee', 'other', 'revenue', NULL::text, NULL::text, b.cancellation_fee
  FROM commercial.package_bookings b JOIN commercial.packages p ON p.id = b.package_id
  WHERE b.status = 'cancelled' AND b.cancellation_fee > 0
  UNION ALL
  SELECT l.property_id, b.id, b.number, b.package_id, b.package_code, p.name, p.package_type, b.start_date, b.status, b.pax, l.booking_component_id,
         c.name, c.component_type, 'cost', l.cost_type, l.basis, l.amount
  FROM commercial.package_cost_lines l JOIN commercial.package_bookings b ON b.id = l.booking_id JOIN commercial.packages p ON p.id = b.package_id
  LEFT JOIN commercial.package_booking_components c ON c.id = l.booking_component_id
  WHERE b.status IN ('confirmed', 'completed', 'cancelled')
) x;

-- Order of Merit: the points of every player per series (best N events),
-- ranked by points, wins and best finish; players below the minimum number
-- of events are listed without a rank. Completed and imported seasons keep
-- their frozen standings.
CREATE VIEW reporting.golf_series_standings WITH (security_invoker = true) AS
WITH pts AS (
  SELECT p.*, row_number() OVER (PARTITION BY p.series_id, p.player_key ORDER BY p.points DESC, p.event_date) AS rn,
         row_number() OVER (PARTITION BY p.series_id, p.player_key ORDER BY p.event_date DESC, p.created_at DESC) AS latest
  FROM golf.tournament_series_points p
), live AS (
  SELECT s.id AS series_id, s.property_id, x.player_key, max(x.player_name) FILTER (WHERE x.latest = 1) AS player_name,
         (array_agg(x.customer_id) FILTER (WHERE x.customer_id IS NOT NULL))[1] AS customer_id,
         count(*)::int AS events, count(*) FILTER (WHERE x.position = 1)::int AS wins, count(*) FILTER (WHERE x.position <= 3)::int AS top3,
         min(x.position) AS best_position, sum(x.points) FILTER (WHERE s.best_of IS NULL OR x.rn <= s.best_of) AS points,
         bool_or(x.public_consent) FILTER (WHERE x.latest = 1) AS public_consent, count(*) >= s.min_events AS qualified
  FROM golf.tournament_series s JOIN pts x ON x.series_id = s.id
  WHERE s.status IN ('draft', 'active')
  GROUP BY s.id, s.property_id, s.min_events, x.player_key
), ranked AS (
  SELECT l.*, CASE WHEN l.qualified THEN rank() OVER (PARTITION BY l.series_id, l.qualified ORDER BY l.points DESC, l.wins DESC, l.best_position NULLS LAST)
         END::int AS rank
  FROM live l
)
SELECT r.series_id, r.property_id, s.code AS series_code, s.name AS series_name, s.season, s.status AS series_status, r.player_key, r.customer_id,
       r.player_name, r.rank,
       CASE WHEN r.rank IS NULL THEN '-' WHEN count(*) OVER (PARTITION BY r.series_id, r.rank) > 1 THEN 'T' || r.rank ELSE r.rank::text END AS position_label,
       coalesce(r.points, 0)::numeric(9,2) AS points, r.events, r.wins, r.top3, r.best_position, coalesce(r.public_consent, false) AS public_consent,
       false AS final
FROM ranked r JOIN golf.tournament_series s ON s.id = r.series_id
UNION ALL
SELECT f.series_id, f.property_id, s.code, s.name, s.season, s.status, f.player_key, f.customer_id, f.player_name, f.rank, f.position_label, f.points,
       f.events, f.wins, f.top3, f.best_position, f.public_consent, true
FROM golf.tournament_series_standings f JOIN golf.tournament_series s ON s.id = f.series_id
WHERE s.status = 'completed';

-- Tournament history: results of completed and imported tournaments with
-- the player key of the Order of Merit (customer, else the name).
CREATE VIEW reporting.golf_tournament_history WITH (security_invoker = true) AS
SELECT x.id AS result_id, x.property_id, x.tournament_id, t.code, t.name AS tournament_name, t.tournament_type, t.format, t.start_date, t.end_date,
       t.source, x.category, x.division_id, coalesce(d.name, x.division_label) AS division, x.position, x.position_label, x.tied, x.score, x.to_par,
       x.customer_id, x.player_name, coalesce(x.customer_id::text, 'n:' || lower(x.player_name)) AS player_key
FROM golf.tournament_results x JOIN golf.tournaments t ON t.id = x.tournament_id LEFT JOIN golf.tournament_divisions d ON d.id = x.division_id
WHERE t.status = 'completed';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.golf_tournament_history, reporting.golf_series_standings, reporting.commercial_package_profit_lines;
