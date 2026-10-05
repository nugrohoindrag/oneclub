-- PRD P3 EP-16 Tournament Management read models (FR-TRN-12 Tournament
-- Report, FR-RPT-P3-04 Golf Performance + tournament KPIs): tournaments
-- with their field, the tournament money posted on billing folios of source
-- "tournament" (registration fees and packages, sponsorships, corporate
-- tournaments), prizes and the champions of the frozen results.
-- Views use security_invoker so Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.golf_tournaments WITH (security_invoker = true) AS
SELECT t.id AS tournament_id, t.property_id, t.code, t.name, t.tournament_type, t.format, t.scoring_basis, t.start_date, t.end_date, t.status,
       t.source, t.field_size, t.start_type, t.corporate_account_id, t.quotation_number, t.quotation_total, t.finalized_at,
       count(r.id) FILTER (WHERE r.status IN ('registered', 'checked_in'))::int AS participants,
       count(r.id) FILTER (WHERE r.status = 'checked_in')::int AS checked_in,
       count(r.id) FILTER (WHERE r.status = 'waitlisted')::int AS waitlisted,
       count(r.id) FILTER (WHERE r.status = 'withdrawn')::int AS withdrawn,
       count(r.id) FILTER (WHERE r.status IN ('registered', 'checked_in') AND r.player_type = 'member')::int AS members,
       count(r.id) FILTER (WHERE r.status IN ('registered', 'checked_in') AND r.player_type = 'guest')::int AS guests
FROM golf.tournaments t LEFT JOIN golf.tournament_registrations r ON r.tournament_id = t.id
GROUP BY t.id;

-- Tournament money on billing folios: kind fee (Tournament Fee, entry),
-- package (package components: green fee, caddy, golf cart, dinner, goodie
-- bag …), withdrawal (fee kept on withdrawal), sponsorship, corporate.
CREATE VIEW reporting.golf_tournament_revenue WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, coalesce(r.tournament_id, s.tournament_id, t.id) AS tournament_id, l.posted_at,
       CASE WHEN s.id IS NOT NULL THEN 'sponsorship'
            WHEN t.id IS NOT NULL THEN 'corporate'
            WHEN l.revenue_component = 'tournament_fee' THEN 'fee'
            WHEN l.revenue_component = 'cancellation_fee' THEN 'withdrawal'
            ELSE 'package' END AS kind,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.liability, l.net_amount AS net, l.service_amount AS service,
       l.tax_amount AS tax, l.total
FROM billing.folio_lines l
JOIN billing.folios f ON f.id = l.folio_id AND f.source_type = 'tournament'
LEFT JOIN golf.tournament_registrations r ON r.id = f.source_id
LEFT JOIN golf.tournament_sponsors s ON s.id = f.source_id
LEFT JOIN golf.tournaments t ON t.id = f.source_id
WHERE l.voided_at IS NULL AND coalesce(r.tournament_id, s.tournament_id, t.id) IS NOT NULL;

CREATE VIEW reporting.golf_tournament_sponsors WITH (security_invoker = true) AS
SELECT s.id AS sponsor_id, s.property_id, s.tournament_id, s.name, s.sponsor_level, s.amount, s.status, s.invoice_id, s.invoiced_at
FROM golf.tournament_sponsors s;

CREATE VIEW reporting.golf_tournament_prizes WITH (security_invoker = true) AS
SELECT p.id AS prize_id, p.property_id, p.tournament_id, p.category, p.name, p.value, p.status, p.awarded_at, p.handed_over_at
FROM golf.tournament_prizes p;

-- Winners (position 1) of the overall boards, imported history included.
CREATE VIEW reporting.golf_tournament_champions WITH (security_invoker = true) AS
SELECT x.id AS result_id, x.property_id, x.tournament_id, x.category, x.player_name, x.score, x.customer_id
FROM golf.tournament_results x WHERE x.position = 1 AND x.division_id IS NULL AND x.division_label IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.golf_tournament_champions, reporting.golf_tournament_prizes, reporting.golf_tournament_sponsors,
  reporting.golf_tournament_revenue, reporting.golf_tournaments;
