-- Customer and Guest foundation entities (FR-MD-05, PRD §5.2): identity,
-- unique code per property, status. Full profiles arrive in P1.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS crm;

CREATE TABLE crm.customers (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  customer_type  text NOT NULL DEFAULT 'individual' CHECK (customer_type IN ('individual', 'corporate')),
  email          text,
  phone          text,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.customers');
SELECT platform.add_touch_trigger('crm.customers');

CREATE TABLE crm.guests (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  email        text,
  phone        text,
  id_number    text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.guests');
SELECT platform.add_touch_trigger('crm.guests');

SELECT platform.grant_app('crm');

-- +goose Down
DROP SCHEMA crm CASCADE;
