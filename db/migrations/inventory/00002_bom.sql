-- BOM / Recipe foundation (PRD P2 EP-22): items with UOM conversions
-- (purchase → stock → usage), recipes with sub-recipes, yield and waste,
-- modifier impact, theoretical consumption per sale and theoretical food
-- cost. Stock deduction, actual consumption and production follow in P4.

-- +goose Up
CREATE TABLE inventory.uoms (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  kind         text NOT NULL DEFAULT 'count' CHECK (kind IN ('mass', 'volume', 'count', 'portion', 'length')),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('inventory.uoms');
SELECT platform.add_touch_trigger('inventory.uoms');

CREATE TABLE inventory.items (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL,
  name             text NOT NULL,
  category         text,
  item_type        text NOT NULL DEFAULT 'raw' CHECK (item_type IN ('raw', 'semi_finished', 'packaging', 'consumable')),
  base_uom_id      uuid NOT NULL REFERENCES inventory.uoms (id),
  purchase_uom_id  uuid REFERENCES inventory.uoms (id),
  standard_cost    numeric(19,6) NOT NULL DEFAULT 0 CHECK (standard_cost >= 0),   -- per base UOM
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('inventory.items');
SELECT platform.add_touch_trigger('inventory.items');

-- 1 from = factor × to (e.g. 1 kg = 1000 g; item-specific: 1 karton = 24 pcs).
CREATE TABLE inventory.uom_conversions (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  item_id      uuid REFERENCES inventory.items (id),
  from_uom_id  uuid NOT NULL REFERENCES inventory.uoms (id),
  to_uom_id    uuid NOT NULL REFERENCES inventory.uoms (id),
  factor       numeric(19,6) NOT NULL CHECK (factor > 0),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  CHECK (from_uom_id <> to_uom_id)
);
CREATE UNIQUE INDEX uom_conversions_uniq ON inventory.uom_conversions
  (property_id, coalesce(item_id, '00000000-0000-0000-0000-000000000000'::uuid), from_uom_id, to_uom_id);
SELECT platform.enable_property_rls('inventory.uom_conversions');
SELECT platform.add_touch_trigger('inventory.uom_conversions');

CREATE TABLE inventory.recipes (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  recipe_type     text NOT NULL DEFAULT 'menu' CHECK (recipe_type IN ('menu', 'sub_recipe', 'service')),
  product_id      uuid REFERENCES commercial.products (id),
  output_item_id  uuid REFERENCES inventory.items (id),     -- semi-finished item produced by a sub-recipe
  service_ref     text,                                      -- service BOM (golf rate, meeting package)
  yield_quantity  numeric(19,6) NOT NULL DEFAULT 1 CHECK (yield_quantity > 0),
  yield_uom_id    uuid REFERENCES inventory.uoms (id),
  waste_percent   numeric(9,4) NOT NULL DEFAULT 0 CHECK (waste_percent >= 0 AND waste_percent < 100),
  notes           text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
CREATE UNIQUE INDEX recipes_product ON inventory.recipes (product_id) WHERE product_id IS NOT NULL AND status = 'active' AND archived_at IS NULL;
SELECT platform.enable_property_rls('inventory.recipes');
SELECT platform.add_touch_trigger('inventory.recipes');

CREATE TABLE inventory.recipe_lines (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  recipe_id      uuid NOT NULL REFERENCES inventory.recipes (id),
  item_id        uuid REFERENCES inventory.items (id),
  sub_recipe_id  uuid REFERENCES inventory.recipes (id),
  quantity       numeric(19,6) NOT NULL CHECK (quantity > 0),
  uom_id         uuid NOT NULL REFERENCES inventory.uoms (id),
  waste_percent  numeric(9,4) NOT NULL DEFAULT 0 CHECK (waste_percent >= 0 AND waste_percent < 100),
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  CHECK ((item_id IS NULL) <> (sub_recipe_id IS NULL)),
  CHECK (sub_recipe_id IS NULL OR sub_recipe_id <> recipe_id)
);
SELECT platform.enable_property_rls('inventory.recipe_lines');
SELECT platform.add_touch_trigger('inventory.recipe_lines');

-- Modifier impact on the recipe (FR-BOM-03): add / remove / replace.
CREATE TABLE inventory.modifier_impacts (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  modifier_id      uuid NOT NULL REFERENCES commercial.modifiers (id),
  action           text NOT NULL CHECK (action IN ('add', 'remove', 'replace')),
  item_id          uuid NOT NULL REFERENCES inventory.items (id),
  replace_item_id  uuid REFERENCES inventory.items (id),
  quantity         numeric(19,6) NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  uom_id           uuid REFERENCES inventory.uoms (id),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  CHECK (action <> 'replace' OR replace_item_id IS NOT NULL)
);
SELECT platform.enable_property_rls('inventory.modifier_impacts');
SELECT platform.add_touch_trigger('inventory.modifier_impacts');

-- Theoretical consumption ledger (FR-BOM-04), ready for P4 stock.
CREATE TABLE inventory.consumption_ledger (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  source_type    text NOT NULL,
  source_id      uuid NOT NULL,
  order_id       uuid,
  outlet_id      uuid,
  product_id     uuid,
  item_id        uuid NOT NULL REFERENCES inventory.items (id),
  quantity       numeric(19,6) NOT NULL,        -- in the item base UOM
  unit_cost      numeric(19,6) NOT NULL,
  cost_amount    numeric(19,4) NOT NULL,
  occurred_at    timestamptz NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id, item_id)
);
CREATE INDEX consumption_ledger_time ON inventory.consumption_ledger (property_id, occurred_at);
SELECT platform.enable_property_rls('inventory.consumption_ledger');

CREATE TABLE inventory.consumption_sales (
  source_id     uuid PRIMARY KEY,               -- order line
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  order_id      uuid,
  outlet_id     uuid,
  product_id    uuid NOT NULL,
  quantity      numeric(19,4) NOT NULL,
  net_amount    numeric(19,4) NOT NULL,
  cost_amount   numeric(19,4) NOT NULL,
  has_recipe    boolean NOT NULL,
  occurred_at   timestamptz NOT NULL
);
CREATE INDEX consumption_sales_time ON inventory.consumption_sales (property_id, occurred_at);
SELECT platform.enable_property_rls('inventory.consumption_sales');

SELECT platform.grant_app('inventory');

-- +goose Down
DROP TABLE inventory.consumption_sales;
DROP TABLE inventory.consumption_ledger;
DROP TABLE inventory.modifier_impacts;
DROP TABLE inventory.recipe_lines;
DROP TABLE inventory.recipes;
DROP TABLE inventory.uom_conversions;
DROP TABLE inventory.items;
DROP TABLE inventory.uoms;
