-- PRD P2 EP-02 Pricing Extension (multi-line) on P1's Pricing Foundation
-- (commercial/00002): services of every business line are priced by the
-- same versioned rules — service type, item, unit & quantity, overtime,
-- minimum, tax & service codes and revenue component — with day type sets
-- per line, stay rate plans and single-line package rates. Golf keeps
-- resolving by charge type as in P1. Expand-only.

-- +goose Up
-- Day type sets group day types per business line (FR-PRC-P2-03); P1's
-- golf day types have no set.
CREATE TABLE commercial.day_type_sets (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  code          text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name          text NOT NULL,
  service_type  text,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.day_type_sets');
SELECT platform.add_touch_trigger('commercial.day_type_sets');
ALTER TABLE commercial.day_types ADD COLUMN day_type_set_id uuid REFERENCES commercial.day_type_sets (id);

-- Time bands per service (FR-PRC-P2-04); the session stays P1's (default other).
ALTER TABLE commercial.time_bands
  ADD COLUMN service_type text,
  ALTER COLUMN session SET DEFAULT 'other';

-- Rate plans of every line; stay plans (Room Only, Long Stay, with
-- breakfast, Day-use) carry minimum nights and inclusions (FR-PRC-P2-06).
ALTER TABLE commercial.rate_plans DROP CONSTRAINT rate_plans_business_line_check;
ALTER TABLE commercial.rate_plans ADD CONSTRAINT rate_plans_business_line_check
  CHECK (business_line IN ('golf', 'sportclub', 'stay', 'pos', 'membership', 'voucher', 'other'));
ALTER TABLE commercial.rate_plans
  ALTER COLUMN pricing_mode SET DEFAULT 'nett',
  ALTER COLUMN effective_from SET DEFAULT CURRENT_DATE,
  ADD COLUMN service_type        text,
  ADD COLUMN min_nights          int NOT NULL DEFAULT 1 CHECK (min_nights >= 0),
  ADD COLUMN includes_breakfast  boolean NOT NULL DEFAULT false,
  ADD COLUMN day_use             boolean NOT NULL DEFAULT false,
  ADD COLUMN facility_access     text[] NOT NULL DEFAULT '{}';   -- facility types included for staying guests (FR-SPT-09)

-- Single-line packages with a fixed price (FR-PRC-P2-07): Family Package,
-- Meeting Half/Full/One Day. Cross-line packages arrive in P3.
CREATE TABLE commercial.package_rates (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name              text NOT NULL,
  service_type      text NOT NULL,
  duration_minutes  int CHECK (duration_minutes IS NULL OR duration_minutes > 0),
  coffee_breaks     int NOT NULL DEFAULT 0,
  min_pax           int NOT NULL DEFAULT 0,
  adults            int NOT NULL DEFAULT 0,
  children          int NOT NULL DEFAULT 0,
  max_child_age     int,
  inclusions        jsonb NOT NULL DEFAULT '[]'::jsonb,
  description       text,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.package_rates');
SELECT platform.add_touch_trigger('commercial.package_rates');

-- Pricing rules of every line. Golf rules keep their rate plan and charge
-- type; other services match by service type (and item) and need no plan.
ALTER TABLE commercial.pricing_rules ALTER COLUMN rate_plan_id DROP NOT NULL;
ALTER TABLE commercial.pricing_rules DROP CONSTRAINT pricing_rules_charge_type_check;
ALTER TABLE commercial.pricing_rules ADD CONSTRAINT pricing_rules_charge_type_check
  CHECK (charge_type IN ('golf_round', 'caddy_fee', 'cart_fee', 'extra_cart', 'other'));
ALTER TABLE commercial.pricing_rules DROP CONSTRAINT pricing_rules_segment_check;
ALTER TABLE commercial.pricing_rules ADD CONSTRAINT pricing_rules_segment_check CHECK (segment IN ('member', 'guest', 'guest_of_member',
  'non_member', 'reciprocal', 'senior', 'ladies', 'junior', 'walk_in', 'student', 'child', 'residence', 'corporate', 'family', 'staying_guest'));
ALTER TABLE commercial.pricing_rules DROP CONSTRAINT pricing_rules_channel_check;
ALTER TABLE commercial.pricing_rules ADD CONSTRAINT pricing_rules_channel_check
  CHECK (channel IN ('member_app', 'website', 'back_office', 'walk_in', 'import', 'ops'));
ALTER TABLE commercial.pricing_rules
  ADD COLUMN service_type       text NOT NULL DEFAULT 'golf',
  ADD COLUMN item_ref           text,          -- resource type code, resource id, product id, package code … (NULL = any item)
  ADD COLUMN package_rate_id    uuid REFERENCES commercial.package_rates (id),
  ADD COLUMN unit               text NOT NULL DEFAULT 'pax' CHECK (unit IN ('slot', 'hour', 'block', 'night', 'day_use_hour', 'pax',
                                  'session', 'package', 'bucket', 'ball', 'entry', 'item', 'registration', 'month', 'year')),
  ADD COLUMN unit_minutes       int CHECK (unit_minutes IS NULL OR unit_minutes > 0),
  ADD COLUMN package_quantity   int NOT NULL DEFAULT 1 CHECK (package_quantity > 0),
  ADD COLUMN min_quantity       int NOT NULL DEFAULT 0 CHECK (min_quantity >= 0),
  ADD COLUMN min_policy         text NOT NULL DEFAULT 'reject' CHECK (min_policy IN ('reject', 'charge_minimum')),
  ADD COLUMN overtime_price     numeric(19,4) CHECK (overtime_price IS NULL OR overtime_price >= 0),
  ADD COLUMN currency           char(3),       -- NULL: the rate plan's / instance currency
  ADD COLUMN pricing_mode       text CHECK (pricing_mode IS NULL OR pricing_mode IN ('nett', 'plus_plus')),  -- NULL: the rate plan's
  ADD COLUMN tax_codes          text[] NOT NULL DEFAULT '{}',
  ADD COLUMN revenue_component  text;
CREATE INDEX pricing_rules_service ON commercial.pricing_rules (property_id, service_type, status);

-- Append-only guard of P2 commercial ledgers (voucher transactions, POS shift
-- closes) — corrections are new rows.
-- +goose StatementBegin
CREATE FUNCTION commercial.forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd

-- Snapshots of every line (FR-PRC-P2-09).
ALTER TABLE commercial.pricing_snapshots
  ADD COLUMN service_type     text,
  ADD COLUMN item_ref         text,
  ADD COLUMN unit             text,
  ADD COLUMN package_rate     text,
  ADD COLUMN gross_amount     numeric(19,4);

SELECT platform.grant_app('commercial');

-- +goose Down
ALTER TABLE commercial.pricing_snapshots DROP COLUMN gross_amount, DROP COLUMN package_rate, DROP COLUMN unit, DROP COLUMN item_ref,
  DROP COLUMN service_type;
DROP INDEX commercial.pricing_rules_service;
ALTER TABLE commercial.pricing_rules DROP COLUMN revenue_component, DROP COLUMN tax_codes, DROP COLUMN pricing_mode, DROP COLUMN currency,
  DROP COLUMN overtime_price, DROP COLUMN min_policy, DROP COLUMN min_quantity, DROP COLUMN package_quantity, DROP COLUMN unit_minutes,
  DROP COLUMN unit, DROP COLUMN package_rate_id, DROP COLUMN item_ref, DROP COLUMN service_type;
DROP TABLE commercial.package_rates;
ALTER TABLE commercial.rate_plans DROP COLUMN facility_access, DROP COLUMN day_use, DROP COLUMN includes_breakfast, DROP COLUMN min_nights,
  DROP COLUMN service_type;
ALTER TABLE commercial.time_bands DROP COLUMN service_type;
ALTER TABLE commercial.day_types DROP COLUMN day_type_set_id;
DROP TABLE commercial.day_type_sets;
DROP FUNCTION commercial.forbid_change();
