-- Supplier foundation entity (FR-MD-05).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS procurement;

CREATE TABLE procurement.suppliers (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  npwp         text,
  email        text,
  phone        text,
  address      text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('procurement.suppliers');
SELECT platform.add_touch_trigger('procurement.suppliers');

SELECT platform.grant_app('procurement');

-- +goose Down
DROP SCHEMA procurement CASCADE;
