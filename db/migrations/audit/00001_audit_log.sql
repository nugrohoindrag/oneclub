-- EP-07 Audit Log: append-only (FR-AUD-03), partitioned per month with a
-- minimum retention of 5 years (FR-AUD-07, Technical Doc §14).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.audit_log (
  id             uuid NOT NULL,
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  actor_type     text NOT NULL CHECK (actor_type IN ('user', 'api_key', 'device', 'system', 'anonymous')),
  actor_id       uuid,
  actor_name     text,
  actor_roles    text[] NOT NULL DEFAULT '{}',
  property_id    uuid,
  module         text NOT NULL,
  action         text NOT NULL,      -- create | update | status_change | delete | void | login_succeeded | ...
  category       text NOT NULL DEFAULT 'data' CHECK (category IN ('data', 'security', 'system')),
  entity_type    text NOT NULL,
  entity_id      text,
  entity_label   text,
  before         jsonb,
  after          jsonb,
  reason         text,
  ip             text,
  user_agent     text,
  device_id      uuid,
  session_id     uuid,
  request_id     text,
  metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX audit_log_occurred ON audit.audit_log (occurred_at DESC);
CREATE INDEX audit_log_entity ON audit.audit_log (entity_type, entity_id);
CREATE INDEX audit_log_actor ON audit.audit_log (actor_id, occurred_at DESC);
CREATE INDEX audit_log_module ON audit.audit_log (module, occurred_at DESC);

CREATE TABLE audit.audit_log_default PARTITION OF audit.audit_log DEFAULT;

-- +goose StatementBegin
-- Creates monthly partitions from the current month forward. SECURITY DEFINER
-- so the application role (which has no DDL rights) can run it from the
-- periodic maintenance job.
CREATE FUNCTION audit.ensure_partitions(months_ahead int DEFAULT 3) RETURNS int
LANGUAGE plpgsql SECURITY DEFINER SET search_path = audit, pg_temp AS $$
DECLARE
  m date := date_trunc('month', now())::date;
  last date := (date_trunc('month', now()) + make_interval(months => months_ahead))::date;
  created int := 0;
  part text;
BEGIN
  WHILE m <= last LOOP
    part := 'audit_log_' || to_char(m, 'YYYYMM');
    IF to_regclass('audit.' || part) IS NULL THEN
      EXECUTE format('CREATE TABLE audit.%I PARTITION OF audit.audit_log FOR VALUES FROM (%L) TO (%L)',
                     part, m, (m + interval '1 month')::date);
      created := created + 1;
    END IF;
    m := (m + interval '1 month')::date;
  END LOOP;
  RETURN created;
END $$;

-- Append-only guard: rejects UPDATE/DELETE/TRUNCATE for every role,
-- including the table owner, unless the guard is explicitly disabled by a
-- superuser for a documented retention operation.
CREATE FUNCTION audit.reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit.audit_log is append-only (% rejected)', TG_OP
    USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd

SELECT audit.ensure_partitions(3);

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit.audit_log
  FOR EACH ROW EXECUTE FUNCTION audit.reject_mutation();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit.audit_log
  FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_mutation();

-- Readers see only properties in their scope; instance-level entries
-- (property_id NULL) are visible to anyone allowed to read audit logs.
ALTER TABLE audit.audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY audit_read ON audit.audit_log FOR SELECT
  USING (property_id IS NULL OR platform.rls_allowed(property_id));
CREATE POLICY audit_insert ON audit.audit_log FOR INSERT WITH CHECK (true);

SELECT platform.grant_app('audit', true);

-- +goose Down
DROP SCHEMA audit CASCADE;
