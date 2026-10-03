-- The read-only reporting role (used against the read replica) must never
-- read secrets: password/PIN hashes, MFA secrets, session and API tokens,
-- integration credentials or idempotency payloads. Table-wide SELECT granted
-- by platform.grant_app is replaced with column-level grants on users.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RAISE NOTICE 'oneclub.report_role not set; skipping';
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON platform.users, platform.sessions, platform.api_keys, platform.password_reset_tokens,
    platform.integrations, platform.idempotency_keys, platform.devices, platform.bridge_agents FROM %I', rep);
  EXECUTE format('GRANT SELECT (id, email, full_name, phone, employee_id, status, mfa_enabled, last_login_at, locale, created_at, updated_at)
    ON platform.users TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, name, device_type, outlet_id, status, last_seen_at, created_at) ON platform.devices TO %I', rep);
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
