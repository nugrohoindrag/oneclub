-- Reporting views on the club's calendar. They compared the UTC date
-- (current_date, created_at::date — the database session runs in UTC) while
-- the club's date (property / instance timezone, Asia/Jakarta = UTC+7) is
-- already the next day from 17:00 to 24:00 UTC:
--  * golf_rain_checks: a rain check expiring today still read as issued
--    until 07:00 WIB the day after, and one issued after midnight WIB was
--    dated the day before (Rain Check Report).
--  * hr_certification_requirements: a certificate that expired yesterday
--    still counted as valid until 07:00 WIB (Certification Compliance).
-- billing.local_date(property) is the property's calendar date (as for the
-- invoice ageing). security_invoker: Row Level Security of the sources
-- applies.

-- +goose Up
CREATE OR REPLACE VIEW reporting.golf_rain_checks WITH (security_invoker = true) AS
SELECT r.id AS rain_check_id, r.property_id, r.number,
       (r.created_at AT TIME ZONE coalesce((SELECT nullif(p.timezone, '') FROM platform.properties p WHERE p.id = r.property_id),
         (SELECT timezone FROM platform.instance)))::date AS issued_on,
       b.code AS booking_code, bp.name AS player_name,
       r.holes_played, r.holes_total, r.credit_percent, r.credit_amount, r.expires_on,
       CASE WHEN r.status = 'issued' AND r.expires_on < billing.local_date(r.property_id) THEN 'expired' ELSE r.status END AS status, r.redeemed_at
FROM golf.rain_checks r JOIN golf.bookings b ON b.id = r.booking_id JOIN golf.booking_players bp ON bp.id = r.booking_player_id;

CREATE OR REPLACE VIEW reporting.hr_certification_requirements WITH (security_invoker = true) AS
SELECT e.id AS employee_id, e.property_id, e.employee_no, e.full_name, ou.name AS org_unit_name, p.name AS position_name, t.code AS type_code,
       t.name AS type_name,
       EXISTS (SELECT 1 FROM hris.certifications c WHERE c.employee_id = e.id AND c.certification_type_id = t.id AND c.archived_at IS NULL
         AND c.status <> 'revoked' AND (c.issued_on IS NULL OR c.issued_on <= billing.local_date(e.property_id))
         AND (c.expires_on IS NULL OR c.expires_on >= billing.local_date(e.property_id))) AS valid
FROM hris.employees e
JOIN hris.positions p ON p.id = e.position_id
JOIN hris.certification_types t ON t.archived_at IS NULL AND t.status = 'active'
  AND ((p.workforce_role IS NOT NULL AND p.workforce_role = ANY (t.mandatory_for)) OR t.code = ANY (p.required_certifications))
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
WHERE e.status = 'active' AND e.archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
CREATE OR REPLACE VIEW reporting.hr_certification_requirements WITH (security_invoker = true) AS
SELECT e.id AS employee_id, e.property_id, e.employee_no, e.full_name, ou.name AS org_unit_name, p.name AS position_name, t.code AS type_code,
       t.name AS type_name,
       EXISTS (SELECT 1 FROM hris.certifications c WHERE c.employee_id = e.id AND c.certification_type_id = t.id AND c.archived_at IS NULL
         AND c.status <> 'revoked' AND (c.issued_on IS NULL OR c.issued_on <= current_date)
         AND (c.expires_on IS NULL OR c.expires_on >= current_date)) AS valid
FROM hris.employees e
JOIN hris.positions p ON p.id = e.position_id
JOIN hris.certification_types t ON t.archived_at IS NULL AND t.status = 'active'
  AND ((p.workforce_role IS NOT NULL AND p.workforce_role = ANY (t.mandatory_for)) OR t.code = ANY (p.required_certifications))
LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
WHERE e.status = 'active' AND e.archived_at IS NULL;

CREATE OR REPLACE VIEW reporting.golf_rain_checks WITH (security_invoker = true) AS
SELECT r.id AS rain_check_id, r.property_id, r.number, r.created_at::date AS issued_on, b.code AS booking_code, bp.name AS player_name,
       r.holes_played, r.holes_total, r.credit_percent, r.credit_amount, r.expires_on,
       CASE WHEN r.status = 'issued' AND r.expires_on < current_date THEN 'expired' ELSE r.status END AS status, r.redeemed_at
FROM golf.rain_checks r JOIN golf.bookings b ON b.id = r.booking_id JOIN golf.booking_players bp ON bp.id = r.booking_player_id;
