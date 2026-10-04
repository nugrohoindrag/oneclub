-- EP-01 Customer & Customer 360 (Basic): full customer profile, Customer vs
-- Guest (upgrade without losing history), duplicate detection and merge,
-- family relationships, corporate accounts with nominees, preferences and
-- personal data requests (UU PDP).

-- +goose Up
ALTER TABLE crm.customers
  ADD COLUMN gender            text CHECK (gender IS NULL OR gender IN ('male', 'female')),
  ADD COLUMN birth_date        date,
  ADD COLUMN address           text,
  ADD COLUMN city              text,
  ADD COLUMN id_number         text,          -- NIK / passport; masked in the UI (FR-CUS-01)
  ADD COLUMN photo_file_id     uuid REFERENCES platform.files (id),
  ADD COLUMN notes             text,
  ADD COLUMN resident          boolean NOT NULL DEFAULT false,  -- Modernland resident (eligibility)
  ADD COLUMN user_id           uuid REFERENCES platform.users (id),  -- Member Portal / guest login
  ADD COLUMN consent_at        timestamptz,   -- UU PDP consent (FR-CUS-09)
  ADD COLUMN consent_channel   text,
  ADD COLUMN marketing_opt_in  boolean NOT NULL DEFAULT false,
  ADD COLUMN duplicate_acknowledged boolean NOT NULL DEFAULT false,
  ADD COLUMN merged_into_id    uuid REFERENCES crm.customers (id),
  ADD COLUMN erased_at         timestamptz,
  ADD COLUMN legacy_ref        text;          -- Rhapsody id (migration)
ALTER TABLE crm.customers DROP CONSTRAINT customers_status_check;
ALTER TABLE crm.customers ADD CONSTRAINT customers_status_check CHECK (status IN ('active', 'inactive', 'merged'));
CREATE INDEX customers_phone ON crm.customers (property_id, phone) WHERE phone IS NOT NULL;
CREATE INDEX customers_email ON crm.customers (property_id, lower(email)) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX customers_user ON crm.customers (user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX customers_legacy ON crm.customers (property_id, legacy_ref) WHERE legacy_ref IS NOT NULL;

-- A Guest is a minimal identity (name + phone). Upgrading links it to the
-- new Customer; history stays attached through customer_id (FR-CUS-02).
ALTER TABLE crm.guests
  ADD COLUMN customer_id     uuid REFERENCES crm.customers (id),
  ADD COLUMN upgraded_at     timestamptz,
  ADD COLUMN consent_at      timestamptz,
  ADD COLUMN erased_at       timestamptz;
CREATE INDEX guests_phone ON crm.guests (property_id, phone) WHERE phone IS NOT NULL;

-- FR-CUS-07 family relationships (spouse, child …), stored once per pair
-- direction as entered.
CREATE TABLE crm.customer_relationships (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  customer_id          uuid NOT NULL REFERENCES crm.customers (id),
  related_customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  relationship         text NOT NULL CHECK (relationship IN ('spouse', 'child', 'parent', 'sibling', 'other')),
  notes                text,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (customer_id, related_customer_id, relationship),
  CHECK (customer_id <> related_customer_id)
);
SELECT platform.enable_property_rls('crm.customer_relationships');
SELECT platform.add_touch_trigger('crm.customer_relationships');

-- FR-CUS-08 corporate account foundation.
CREATE TABLE crm.corporate_accounts (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  code          text NOT NULL,
  name          text NOT NULL,
  npwp          text,
  contact_name  text,
  phone         text,
  email         text,
  address       text,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes    jsonb NOT NULL DEFAULT '{}'::jsonb,
  legacy_ref    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.corporate_accounts');
SELECT platform.add_touch_trigger('crm.corporate_accounts');

CREATE TABLE crm.corporate_nominees (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  corporate_account_id  uuid NOT NULL REFERENCES crm.corporate_accounts (id),
  customer_id           uuid NOT NULL REFERENCES crm.customers (id),
  title                 text,
  starts_on             date,
  ends_on               date,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (corporate_account_id, customer_id)
);
SELECT platform.enable_property_rls('crm.corporate_nominees');
SELECT platform.add_touch_trigger('crm.corporate_nominees');

-- FR-CUS-06 preferences: structured attributes (favourite caddy, buggy need)
-- plus free notes.
CREATE TABLE crm.customer_preferences (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  category     text NOT NULL CHECK (category IN ('golf', 'caddy', 'golf_cart', 'dining', 'communication', 'other')),
  pref_key     text NOT NULL,
  pref_value   text,
  notes        text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  UNIQUE (customer_id, category, pref_key)
);
SELECT platform.enable_property_rls('crm.customer_preferences');
SELECT platform.add_touch_trigger('crm.customer_preferences');

-- FR-CUS-03 merge log.
CREATE TABLE crm.customer_merges (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  source_id     uuid NOT NULL REFERENCES crm.customers (id),
  target_id     uuid NOT NULL REFERENCES crm.customers (id),
  reason        text NOT NULL,
  merged_at     timestamptz NOT NULL DEFAULT now(),
  merged_by     uuid
);
SELECT platform.enable_property_rls('crm.customer_merges');

-- FR-CUS-09 data subject requests (export / erase).
CREATE TABLE crm.data_requests (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  customer_id   uuid NOT NULL REFERENCES crm.customers (id),
  request_type  text NOT NULL CHECK (request_type IN ('export', 'erase')),
  status        text NOT NULL CHECK (status IN ('pending', 'completed', 'rejected')),
  reason        text,
  file_id       uuid REFERENCES platform.files (id),
  requested_at  timestamptz NOT NULL DEFAULT now(),
  requested_by  uuid,
  completed_at  timestamptz
);
SELECT platform.enable_property_rls('crm.data_requests');

SELECT platform.grant_app('crm');

-- +goose Down
DROP TABLE crm.data_requests, crm.customer_merges, crm.customer_preferences, crm.corporate_nominees,
  crm.corporate_accounts, crm.customer_relationships;
ALTER TABLE crm.guests DROP COLUMN customer_id, DROP COLUMN upgraded_at, DROP COLUMN consent_at, DROP COLUMN erased_at;
ALTER TABLE crm.customers DROP CONSTRAINT customers_status_check;
ALTER TABLE crm.customers ADD CONSTRAINT customers_status_check CHECK (status IN ('active', 'inactive'));
ALTER TABLE crm.customers DROP COLUMN gender, DROP COLUMN birth_date, DROP COLUMN address, DROP COLUMN city, DROP COLUMN id_number,
  DROP COLUMN photo_file_id, DROP COLUMN notes, DROP COLUMN resident, DROP COLUMN user_id, DROP COLUMN consent_at,
  DROP COLUMN consent_channel, DROP COLUMN marketing_opt_in, DROP COLUMN duplicate_acknowledged, DROP COLUMN merged_into_id,
  DROP COLUMN erased_at, DROP COLUMN legacy_ref;
