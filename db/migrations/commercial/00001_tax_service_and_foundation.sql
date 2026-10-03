-- Tax & Service rules with effective dates (FR-MD-04, FR-MD-08) and the
-- Product / Outlet foundation entities (FR-MD-05).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS commercial;

-- Rates are configured by the club, never hard-coded. A rule version that is
-- already effective is immutable; changes create a new version.
CREATE TABLE commercial.tax_service_rules (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('tax', 'service')),
  rate_percent    numeric(9,4) NOT NULL CHECK (rate_percent >= 0 AND rate_percent <= 100),
  -- base: what the percentage applies to
  basis           text NOT NULL CHECK (basis IN ('net_amount', 'net_plus_service')),
  -- pricing mode: nett = price already includes it; plus_plus = added on top
  pricing_mode    text NOT NULL CHECK (pricing_mode IN ('nett', 'plus_plus')),
  sequence        int NOT NULL DEFAULT 1,
  effective_from  timestamptz NOT NULL,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, code, effective_from)
);
SELECT platform.enable_property_rls('commercial.tax_service_rules');
SELECT platform.add_touch_trigger('commercial.tax_service_rules');

CREATE TABLE commercial.outlets (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  outlet_type  text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.outlets');
SELECT platform.add_touch_trigger('commercial.outlets');

CREATE TABLE commercial.products (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  category     text,
  unit         text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  attributes   jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.products');
SELECT platform.add_touch_trigger('commercial.products');

ALTER TABLE billing.payment_method_settings
  ADD CONSTRAINT payment_method_settings_outlet_fk FOREIGN KEY (outlet_id) REFERENCES commercial.outlets (id);
ALTER TABLE platform.devices
  ADD CONSTRAINT devices_outlet_fk FOREIGN KEY (outlet_id) REFERENCES commercial.outlets (id);

SELECT platform.grant_app('commercial');

-- +goose Down
ALTER TABLE platform.devices DROP CONSTRAINT devices_outlet_fk;
ALTER TABLE billing.payment_method_settings DROP CONSTRAINT payment_method_settings_outlet_fk;
DROP SCHEMA commercial CASCADE;
