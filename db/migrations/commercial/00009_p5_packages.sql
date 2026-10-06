-- PRD P5 EP-22 Advanced Package Management (commercial/package, PRD P5
-- §5.4.1, lanjutan P3; additive only):
--   * Package Component Rule (FR-PKG-P5-01): choice groups (pick N of the
--     alternatives), sequence & time gap after another component, the time
--     window a component is served in.
--   * Package Capacity (FR-PKG-P5-02): allotments (bookings / pax / units
--     per start or service date), blackouts and time blocks (capacity per
--     block of minutes) per package or per component.
--   * Package cost rules and cost lines (FR-PKG-P5-04 Package
--     Profitability): COGS of the BOM (P4 inventory cost), caddy fee, room
--     cost, commission and other direct costs per booking component; COGS
--     is estimated at booking and replaced by the actual BOM cost when the
--     component is consumed.
--   * Package payment templates per package type (FR-PKG-P5-05).
-- Rates, limits and formulas are configuration with effective dates
-- (PRD P5 §6 #18).

-- +goose Up
CREATE TABLE commercial.package_component_rules (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  component_id        uuid NOT NULL REFERENCES commercial.package_components (id),
  choice_group        text CHECK (choice_group IS NULL OR choice_group ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  choice_pick         int NOT NULL DEFAULT 1 CHECK (choice_pick BETWEEN 1 AND 20),
  after_component_id  uuid REFERENCES commercial.package_components (id),
  min_gap_minutes     int NOT NULL DEFAULT 0 CHECK (min_gap_minutes BETWEEN 0 AND 10080),
  max_gap_minutes     int CHECK (max_gap_minutes IS NULL OR max_gap_minutes BETWEEN 0 AND 10080),
  window_start        time,
  window_end          time,
  notes               text,
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  CHECK (after_component_id IS NULL OR after_component_id <> component_id),
  CHECK (max_gap_minutes IS NULL OR max_gap_minutes >= min_gap_minutes),
  CHECK (window_start IS NULL OR window_end IS NULL OR window_end > window_start)
);
CREATE INDEX package_component_rules_component ON commercial.package_component_rules (component_id, status);
SELECT platform.enable_property_rls('commercial.package_component_rules');
SELECT platform.add_touch_trigger('commercial.package_component_rules');

CREATE TABLE commercial.package_capacities (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  package_id      uuid NOT NULL REFERENCES commercial.packages (id),
  component_id    uuid REFERENCES commercial.package_components (id),   -- NULL = the package (start date)
  capacity_type   text NOT NULL CHECK (capacity_type IN ('blackout', 'allotment', 'time_block')),
  date_from       date NOT NULL,
  date_to         date,                                                  -- NULL = open ended
  weekdays        int[] NOT NULL DEFAULT '{}',                           -- ISO weekdays (empty = every day)
  quota           int CHECK (quota IS NULL OR quota >= 0),
  basis           text NOT NULL DEFAULT 'bookings' CHECK (basis IN ('bookings', 'pax', 'units')),
  block_minutes   int CHECK (block_minutes IS NULL OR block_minutes BETWEEN 5 AND 1440),
  window_start    time,
  window_end      time,
  reason          text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  CHECK (date_to IS NULL OR date_to >= date_from),
  CHECK (window_start IS NULL OR window_end IS NULL OR window_end > window_start)
);
CREATE INDEX package_capacities_package ON commercial.package_capacities (package_id, status, date_from);
SELECT platform.enable_property_rls('commercial.package_capacities');
SELECT platform.add_touch_trigger('commercial.package_capacities');

CREATE TABLE commercial.package_cost_rules (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  package_id      uuid NOT NULL REFERENCES commercial.packages (id),
  component_id    uuid REFERENCES commercial.package_components (id),   -- NULL = per booking of the package
  cost_type       text NOT NULL CHECK (cost_type IN ('caddy_fee', 'room_cost', 'commission', 'labour', 'cogs', 'other')),
  basis           text NOT NULL DEFAULT 'per_unit' CHECK (basis IN ('per_unit', 'per_booking', 'per_pax', 'percent_of_revenue')),
  amount          numeric(19,4) NOT NULL CHECK (amount >= 0),
  effective_from  date,
  effective_to    date,
  description     text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  CHECK (effective_to IS NULL OR effective_from IS NULL OR effective_to >= effective_from)
);
CREATE INDEX package_cost_rules_package ON commercial.package_cost_rules (package_id, status);
SELECT platform.enable_property_rls('commercial.package_cost_rules');
SELECT platform.add_touch_trigger('commercial.package_cost_rules');

-- Cost of each package booking (component or booking level) by cost type;
-- refreshed from the booking, consumption and cancellation events.
CREATE TABLE commercial.package_cost_lines (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  booking_id            uuid NOT NULL REFERENCES commercial.package_bookings (id),
  booking_component_id  uuid REFERENCES commercial.package_booking_components (id),
  cost_type             text NOT NULL CHECK (cost_type IN ('cogs', 'caddy_fee', 'room_cost', 'commission', 'labour', 'other')),
  basis                 text NOT NULL CHECK (basis IN ('estimate', 'actual', 'rule')),
  amount                numeric(19,4) NOT NULL,
  rule_id               uuid,
  detail                jsonb NOT NULL DEFAULT '{}'::jsonb,
  computed_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX package_cost_lines_booking ON commercial.package_cost_lines (booking_id);
SELECT platform.enable_property_rls('commercial.package_cost_lines');

CREATE TABLE commercial.package_payment_templates (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  package_type    text NOT NULL CHECK (package_type IN ('golf_day', 'golf_lunch', 'stay_golf', 'corporate', 'wedding', 'family', 'other')),
  min_amount      numeric(19,4) NOT NULL DEFAULT 0 CHECK (min_amount >= 0),
  lines           jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{label, kind, percent, days, from: booking|before_start}]
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.package_payment_templates');
SELECT platform.add_touch_trigger('commercial.package_payment_templates');

SELECT platform.grant_app('commercial');

-- +goose Down
DROP TABLE commercial.package_payment_templates, commercial.package_cost_lines, commercial.package_cost_rules, commercial.package_capacities,
  commercial.package_component_rules;
