-- Offline sync queue receipts (Technical Doc §6.4, PRD FR-SH-05): every
-- item an offline client queues carries a client-generated UUIDv7; the
-- server processes it once and returns the stored result on retries.

-- +goose Up
CREATE TABLE platform.sync_items (
  id           uuid PRIMARY KEY,            -- client-generated UUIDv7
  user_id      uuid NOT NULL REFERENCES platform.users (id),
  device_id    uuid REFERENCES platform.devices (id),
  property_id  uuid REFERENCES platform.properties (id),
  action       text NOT NULL,
  payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
  status       text NOT NULL CHECK (status IN ('accepted', 'rejected', 'conflict')),
  result       jsonb NOT NULL DEFAULT '{}'::jsonb,
  client_time  timestamptz,
  received_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sync_items_user ON platform.sync_items (user_id, received_at DESC);
ALTER TABLE platform.sync_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY property_isolation ON platform.sync_items
  USING (property_id IS NULL OR platform.rls_allowed(property_id))
  WITH CHECK (property_id IS NULL OR platform.rls_allowed(property_id));

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.sync_items;
