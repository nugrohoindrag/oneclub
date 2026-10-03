-- P1 platform additions: calendar of public holidays and special dates
-- (FR-TEE-02, PRD §9 "Day Calendar" — platform, used across modules),
-- CAPTCHA capability for public booking (FR-WEB-07), WhatsApp delivery
-- status from the BSP (FR-INT-P1-02), one-time login codes for the Member
-- Portal (FR-APP-01) and personal data requests (FR-CUS-09).

-- +goose Up
CREATE TABLE platform.calendar_days (
  id             uuid PRIMARY KEY,
  property_id    uuid REFERENCES platform.properties (id),   -- NULL = every property
  day            date NOT NULL,
  name           text NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('public_holiday', 'special')),
  day_type_code  text,                                      -- explicit day type override (commercial.day_types.code)
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
CREATE UNIQUE INDEX calendar_days_uniq ON platform.calendar_days
  (day, coalesce(property_id, '00000000-0000-0000-0000-000000000000'::uuid));
SELECT platform.add_touch_trigger('platform.calendar_days');
ALTER TABLE platform.calendar_days ENABLE ROW LEVEL SECURITY;
CREATE POLICY property_isolation ON platform.calendar_days
  USING (property_id IS NULL OR platform.rls_allowed(property_id))
  WITH CHECK (property_id IS NULL OR platform.rls_allowed(property_id));

ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check
  CHECK (capability IN ('payment', 'messaging', 'email', 'tax_invoice', 'resident_data', 'hardware', 'captcha'));

-- Template parameters and the provider message id let the BSP report the
-- delivery status (sent → delivered → read / failed) back to the history.
ALTER TABLE platform.notification_deliveries
  ADD COLUMN payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN external_id   text,
  ADD COLUMN delivery_status text,
  ADD COLUMN delivered_at  timestamptz,
  ADD COLUMN read_at       timestamptz;
CREATE INDEX notification_deliveries_external ON platform.notification_deliveries (external_id) WHERE external_id IS NOT NULL;

-- One-time login codes (e-mail / WhatsApp OTP) for the Member Portal.
CREATE TABLE platform.login_codes (
  id          uuid PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES platform.users (id) ON DELETE CASCADE,
  channel     text NOT NULL CHECK (channel IN ('email', 'whatsapp')),
  code_hash   text NOT NULL,
  attempts    int NOT NULL DEFAULT 0,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_codes_user ON platform.login_codes (user_id, created_at DESC);

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.login_codes;
ALTER TABLE platform.notification_deliveries DROP COLUMN payload, DROP COLUMN external_id, DROP COLUMN delivery_status,
  DROP COLUMN delivered_at, DROP COLUMN read_at;
ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check
  CHECK (capability IN ('payment', 'messaging', 'email', 'tax_invoice', 'resident_data', 'hardware'));
DROP TABLE platform.calendar_days;
