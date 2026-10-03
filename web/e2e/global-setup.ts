import { execFileSync } from 'node:child_process';

/**
 * Prepares browser-test accounts in the local dev instance: one user per
 * tested role, password = the demo password, MFA reset so the test can enrol
 * a fresh authenticator. Requires psql (superuser) — local/staging only.
 */
export default async function globalSetup() {
  if (process.env.E2E_SKIP_SETUP) return;
  const psql = process.env.PSQL ?? 'C:/Users/DELL/pgsql18/pgsql/bin/psql.exe';
  const db = process.env.E2E_DB ?? 'oneclub_mgcc';
  const sql = `
    INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at, locale)
    SELECT gen_random_uuid(), 'e2e.' || r.code || '@test.oneclub.id', r.name || ' (E2E)',
           (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id'), now(), 'en'
    FROM platform.roles r WHERE r.code IN ('platform_admin','super_admin','general_manager','golf_manager','starter_marshal','member','cashier')
    ON CONFLICT (email) DO NOTHING;
    INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
    SELECT gen_random_uuid(), u.id, r.id, CASE WHEN r.scope = 'property' THEN (SELECT id FROM platform.properties WHERE code = 'MAIN') END
    FROM platform.roles r JOIN platform.users u ON u.email = 'e2e.' || r.code || '@test.oneclub.id'
    ON CONFLICT DO NOTHING;
    UPDATE platform.users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_pending_secret_enc = NULL, locale = 'en',
      pin_hash = (SELECT pin_hash FROM platform.users WHERE email = 'starter@demo.oneclub.id'), failed_login_count = 0, locked_until = NULL
    WHERE email LIKE 'e2e.%@test.oneclub.id';
    UPDATE platform.feature_flags SET value = 'true' WHERE key = 'ui.theme_switch';`;
  execFileSync(psql, ['-h', 'localhost', '-U', 'postgres', '-d', db, '-v', 'ON_ERROR_STOP=1', '-q', '-c', sql], {
    env: { ...process.env, PGPASSWORD: process.env.PGPASSWORD ?? 'postgres' },
    stdio: 'inherit',
  });
}
