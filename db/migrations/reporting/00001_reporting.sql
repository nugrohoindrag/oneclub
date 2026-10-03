-- Reporting foundation (EP-10): report registry, asynchronous exports and
-- read models. Report queries run on the read replica (FR-REP-01).
-- Views use security_invoker so Row Level Security of the source tables
-- applies to the caller.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS reporting;

-- FR-REP-02 report registry (synchronised from code).
CREATE TABLE reporting.report_definitions (
  code         text PRIMARY KEY,
  name         text NOT NULL,         -- "[Business Domain] [Report Type]" (Naming Convention §32)
  module       text NOT NULL,
  permission   text NOT NULL,
  description  text NOT NULL DEFAULT '',
  parameters   jsonb NOT NULL DEFAULT '[]'::jsonb,
  columns      jsonb NOT NULL DEFAULT '[]'::jsonb
);

-- FR-REP-03 asynchronous export.
CREATE TABLE reporting.exports (
  id            uuid PRIMARY KEY,
  report_code   text NOT NULL,
  format        text NOT NULL CHECK (format IN ('csv', 'xlsx')),
  parameters    jsonb NOT NULL DEFAULT '{}'::jsonb,
  property_id   uuid,
  status        text NOT NULL CHECK (status IN ('pending', 'completed', 'failed')),
  file_id       uuid REFERENCES platform.files (id),
  row_count     int,
  error         text,
  requested_by  uuid NOT NULL REFERENCES platform.users (id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  completed_at  timestamptz
);

-- FR-REP-05 User Access Report read model.
CREATE VIEW reporting.user_access WITH (security_invoker = true) AS
SELECT u.id            AS user_id,
       u.full_name,
       u.email::text   AS email,
       u.status        AS user_status,
       u.mfa_enabled,
       u.last_login_at,
       r.code          AS role_code,
       r.name          AS role_name,
       ra.property_id,
       coalesce(p.name, 'All Properties') AS property_name
FROM platform.users u
JOIN platform.role_assignments ra ON ra.user_id = u.id
JOIN platform.roles r ON r.id = ra.role_id
LEFT JOIN platform.properties p ON p.id = ra.property_id;

-- Vertical slice read model (PRD §8): venues with their activation status.
CREATE VIEW reporting.venue_directory WITH (security_invoker = true) AS
SELECT v.id AS venue_id, v.code, v.name, v.venue_type, v.status,
       v.property_id, p.name AS property_name, v.created_at, v.updated_at
FROM platform.venues v
JOIN platform.properties p ON p.id = v.property_id
WHERE v.archived_at IS NULL;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP SCHEMA reporting CASCADE;
