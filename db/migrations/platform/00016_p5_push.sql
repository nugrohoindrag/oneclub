-- PRD P5 FR-INT-P5-05 / FR-ESS-07: push notifications of the installed
-- Staff App (Employee Self Service) and Member App. Browsers subscribe with
-- the Push API; the notification service gets the channel "push" and the
-- integration layer the capability "push" (Web Push with VAPID, or the
-- mock adapter). A subscription belongs to a user; the endpoint is the
-- browser's push service URL (no personal data in the push itself beyond
-- the rendered notification).

-- +goose Up
CREATE TABLE platform.push_subscriptions (
  id               uuid PRIMARY KEY,
  user_id          uuid NOT NULL REFERENCES platform.users (id) ON DELETE CASCADE,
  endpoint         text NOT NULL UNIQUE CHECK (endpoint ~ '^https://'),
  p256dh           text NOT NULL,
  auth             text NOT NULL,
  surface          text NOT NULL CHECK (surface IN ('staff', 'member')),
  user_agent       text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  last_success_at  timestamptz,
  failure_count    int NOT NULL DEFAULT 0,
  revoked_at       timestamptz
);
SELECT platform.add_touch_trigger('platform.push_subscriptions');
CREATE INDEX push_subscriptions_user ON platform.push_subscriptions (user_id) WHERE revoked_at IS NULL;

ALTER TABLE platform.notification_templates DROP CONSTRAINT notification_templates_channel_check;
ALTER TABLE platform.notification_templates ADD CONSTRAINT notification_templates_channel_check
  CHECK (channel IN ('in_app', 'email', 'whatsapp', 'push'));
ALTER TABLE platform.notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
  CHECK (channel IN ('in_app', 'email', 'whatsapp', 'push'));
ALTER TABLE platform.notification_preferences DROP CONSTRAINT notification_preferences_channel_check;
ALTER TABLE platform.notification_preferences ADD CONSTRAINT notification_preferences_channel_check
  CHECK (channel IN ('in_app', 'email', 'whatsapp', 'push'));

-- +goose StatementBegin
DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO def FROM pg_constraint c
  WHERE c.conname = 'integrations_capability_check' AND c.conrelid = 'platform.integrations'::regclass;
  IF def IS NULL OR position('''push''' IN def) > 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || regexp_replace(def, '\]\)', ', ''push''::text])');
END $$;
-- +goose StatementEnd

SELECT platform.grant_app('platform');

-- +goose Down
DELETE FROM platform.notification_templates WHERE channel = 'push';
DELETE FROM platform.notification_deliveries WHERE channel = 'push';
DELETE FROM platform.notification_preferences WHERE channel = 'push';
ALTER TABLE platform.notification_templates DROP CONSTRAINT notification_templates_channel_check;
ALTER TABLE platform.notification_templates ADD CONSTRAINT notification_templates_channel_check CHECK (channel IN ('in_app', 'email', 'whatsapp'));
ALTER TABLE platform.notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('in_app', 'email', 'whatsapp'));
ALTER TABLE platform.notification_preferences DROP CONSTRAINT notification_preferences_channel_check;
ALTER TABLE platform.notification_preferences ADD CONSTRAINT notification_preferences_channel_check CHECK (channel IN ('in_app', 'email', 'whatsapp'));
-- +goose StatementBegin
DO $$
DECLARE
  def text;
BEGIN
  DELETE FROM platform.integrations WHERE capability = 'push';
  SELECT pg_get_constraintdef(c.oid) INTO def FROM pg_constraint c
  WHERE c.conname = 'integrations_capability_check' AND c.conrelid = 'platform.integrations'::regclass;
  IF def IS NULL OR position('''push''' IN def) = 0 THEN
    RETURN;
  END IF;
  ALTER TABLE platform.integrations DROP CONSTRAINT integrations_capability_check;
  EXECUTE 'ALTER TABLE platform.integrations ADD CONSTRAINT integrations_capability_check '
    || replace(def, ', ''push''::text', '');
END $$;
-- +goose StatementEnd
DROP TABLE platform.push_subscriptions;
