-- EP-07 Pricing Foundation: Day Types and Time Bands (FR-PRC-01), Rate
-- Plans and Pricing Rules with effective dates (FR-PRC-02/03), all-in price
-- components (FR-PRC-05), Nett / ++ (FR-PRC-06) and immutable Pricing
-- Snapshots (FR-PRC-07, Technical Doc §7.4).

-- +goose Up
CREATE TABLE commercial.day_types (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  code                 text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                 text NOT NULL,
  weekdays             text NOT NULL DEFAULT '' CHECK (weekdays ~ '^([1-7](,[1-7])*)?$'),  -- ISO 1 (Mon) … 7 (Sun), e.g. 1,2,3,4,5
  includes_holidays    boolean NOT NULL DEFAULT false,  -- public holidays use this day type
  priority             int NOT NULL DEFAULT 100,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.day_types');
SELECT platform.add_touch_trigger('commercial.day_types');

CREATE TABLE commercial.time_bands (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  session      text NOT NULL CHECK (session IN ('morning', 'afternoon', 'night', 'other')),
  start_time   text NOT NULL CHECK (start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  end_time     text NOT NULL CHECK (end_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code),
  CHECK (end_time > start_time)
);
SELECT platform.enable_property_rls('commercial.time_bands');
SELECT platform.add_touch_trigger('commercial.time_bands');

CREATE TABLE commercial.rate_plans (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name            text NOT NULL,
  description     text,
  business_line   text NOT NULL DEFAULT 'golf' CHECK (business_line IN ('golf')),
  pricing_mode    text NOT NULL CHECK (pricing_mode IN ('nett', 'plus_plus')),
  currency        char(3) NOT NULL DEFAULT 'IDR',
  effective_from  date NOT NULL,
  effective_to    date,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
SELECT platform.enable_property_rls('commercial.rate_plans');
SELECT platform.add_touch_trigger('commercial.rate_plans');

-- A pricing rule version is immutable once it is in effect; a change is a
-- new version (same code, version + 1) with a later effective date.
CREATE TABLE commercial.pricing_rules (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  rate_plan_id      uuid NOT NULL REFERENCES commercial.rate_plans (id),
  code              text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  version           int NOT NULL DEFAULT 1,
  name              text NOT NULL,
  charge_type       text NOT NULL DEFAULT 'golf_round' CHECK (charge_type IN ('golf_round', 'caddy_fee', 'cart_fee', 'extra_cart')),
  segment           text CHECK (segment IN ('member', 'guest', 'guest_of_member', 'non_member', 'reciprocal', 'senior', 'ladies', 'junior')),
  day_type_id       uuid REFERENCES commercial.day_types (id),
  time_band_id      uuid REFERENCES commercial.time_bands (id),
  playing_route_id  uuid,   -- golf.playing_routes (FK added by golf)
  channel           text CHECK (channel IN ('member_app', 'website', 'back_office', 'walk_in', 'import')),
  peak              boolean,
  price             numeric(19,4) NOT NULL CHECK (price >= 0),
  components        jsonb NOT NULL DEFAULT '[]'::jsonb,
  priority          int NOT NULL DEFAULT 100,
  effective_from    date NOT NULL,
  effective_to      date,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, code, version),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
CREATE INDEX pricing_rules_lookup ON commercial.pricing_rules (property_id, charge_type, status, effective_from);
SELECT platform.enable_property_rls('commercial.pricing_rules');
SELECT platform.add_touch_trigger('commercial.pricing_rules');

-- FR-PRC-07 immutable snapshot of every price used by a charge.
CREATE TABLE commercial.pricing_snapshots (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  charge_type       text NOT NULL,
  rule_id           uuid REFERENCES commercial.pricing_rules (id),
  rule_code         text,
  rule_version      int,
  rate_plan_id      uuid REFERENCES commercial.rate_plans (id),
  rate_plan_code    text,
  segment           text,
  day_type_code     text,
  time_band_code    text,
  playing_route_id  uuid,
  channel           text,
  peak              boolean,
  play_at           timestamptz NOT NULL,
  currency          char(3) NOT NULL,
  pricing_mode      text NOT NULL,
  list_price        numeric(19,4) NOT NULL,
  quantity          numeric(19,4) NOT NULL DEFAULT 1,
  net_amount        numeric(19,4) NOT NULL,
  tax_amount        numeric(19,4) NOT NULL,
  service_amount    numeric(19,4) NOT NULL,
  total             numeric(19,4) NOT NULL,
  components        jsonb NOT NULL DEFAULT '[]'::jsonb,
  tax_service       jsonb NOT NULL DEFAULT '[]'::jsonb,
  override          jsonb,              -- manual price / discount with reason and approver (FR-PRC-09)
  context           jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid
);
CREATE INDEX pricing_snapshots_rule ON commercial.pricing_snapshots (rule_id);
SELECT platform.enable_property_rls('commercial.pricing_snapshots');

-- +goose StatementBegin
CREATE FUNCTION commercial.snapshot_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'pricing snapshots are immutable' USING ERRCODE = '42501';
END $$;
-- +goose StatementEnd
CREATE TRIGGER pricing_snapshots_immutable BEFORE UPDATE OR DELETE ON commercial.pricing_snapshots
  FOR EACH ROW EXECUTE FUNCTION commercial.snapshot_immutable();

ALTER TABLE billing.folio_lines
  ADD CONSTRAINT folio_lines_snapshot_fk FOREIGN KEY (pricing_snapshot_id) REFERENCES commercial.pricing_snapshots (id);

SELECT platform.grant_app('commercial');

-- +goose Down
ALTER TABLE billing.folio_lines DROP CONSTRAINT folio_lines_snapshot_fk;
DROP TABLE commercial.pricing_snapshots, commercial.pricing_rules, commercial.rate_plans, commercial.time_bands, commercial.day_types;
DROP FUNCTION commercial.snapshot_immutable();
