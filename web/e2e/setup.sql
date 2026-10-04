-- Browser-test accounts (run by e2e/global-setup.ts locally/CI and by
-- deploy/scripts/e2e-setup.sh on Staging): one user per tested role,
-- password = the demo password, MFA reset so each run enrols a fresh
-- authenticator. Never run against Production.
INSERT INTO platform.users (id, email, full_name, password_hash, password_changed_at, locale)
SELECT gen_random_uuid(), 'e2e.' || r.code || '@test.oneclub.id', r.name || ' (E2E)',
       (SELECT password_hash FROM platform.users WHERE email = 'gm@demo.oneclub.id'), now(), 'en'
FROM platform.roles r WHERE r.code IN ('platform_admin','super_admin','general_manager','golf_manager','starter_marshal','member','cashier','caddy','caddy_manager','sport_club_receptionist','kitchen_staff')
ON CONFLICT (email) DO NOTHING;

INSERT INTO platform.role_assignments (id, user_id, role_id, property_id)
SELECT gen_random_uuid(), u.id, r.id, CASE WHEN r.scope = 'property' THEN (SELECT id FROM platform.properties WHERE code = 'MAIN') END
FROM platform.roles r JOIN platform.users u ON u.email = 'e2e.' || r.code || '@test.oneclub.id'
ON CONFLICT DO NOTHING;

UPDATE platform.users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_pending_secret_enc = NULL, locale = 'en',
  pin_hash = (SELECT pin_hash FROM platform.users WHERE email = 'starter@demo.oneclub.id'), failed_login_count = 0, locked_until = NULL
WHERE email LIKE 'e2e.%@test.oneclub.id';

UPDATE platform.feature_flags SET value = 'true' WHERE key = 'ui.theme_switch';
