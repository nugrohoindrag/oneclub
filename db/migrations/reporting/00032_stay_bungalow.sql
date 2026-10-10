-- Bungalow incidents on Management › Incidents next to the golf, banquet and
-- Sport Club ones (docs/requirement-booking-hotel-mgcc.md FR-H86).

-- +goose Up
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
LEFT JOIN sportclub.facilities f ON f.id = s.facility_id
UNION ALL
SELECT x.id, x.property_id, 'stay', x.number, x.category, x.severity, x.status, x.description, x.action_taken,
       coalesce(b.code || ' · ' || b.name, 'Bungalow'), x.damage_amount, NULL, x.occurred_at, x.created_at
FROM stay.incidents x
LEFT JOIN stay.bungalows b ON b.id = x.bungalow_id;

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
JOIN banquet.events e ON e.id = b.event_id
UNION ALL
SELECT s.id, s.property_id, 'sportclub', s.number, s.category, s.severity, s.status, s.description, s.action_taken,
       coalesce(c.name, f.name, 'Sport Club'), s.damage_amount, NULL, s.occurred_at, s.created_at
FROM sportclub.incidents s
LEFT JOIN sportclub.courts c ON c.id = s.court_id
LEFT JOIN sportclub.facilities f ON f.id = s.facility_id;
