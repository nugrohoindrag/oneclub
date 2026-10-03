-- Member foundation entity (FR-MD-05). Membership lifecycle arrives in P1.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS membership;

CREATE TABLE membership.members (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL,        -- member number
  name             text NOT NULL,
  email            text,
  phone            text,
  membership_type  text,
  user_id          uuid REFERENCES platform.users (id),   -- Member Portal login
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'suspended', 'expired')),
  attributes       jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('membership.members');
SELECT platform.add_touch_trigger('membership.members');

SELECT platform.grant_app('membership');

-- +goose Down
DROP SCHEMA membership CASCADE;
