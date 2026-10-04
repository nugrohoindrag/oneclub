-- POS (PRD P2 EP-20) and F&B Experience (EP-21): outlets, products with
-- variants and modifiers, menus, orders with split bills, kitchen tickets
-- (KDS), cashier shifts and cash movements. Offline terminals create orders
-- with client-generated UUIDv7 ids, so a resubmitted order is never duplicated.

-- +goose Up
ALTER TABLE commercial.outlets
  ADD COLUMN tax_codes        text[] NOT NULL DEFAULT '{}',
  ADD COLUMN pricing_mode     text NOT NULL DEFAULT 'nett' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  ADD COLUMN opening_time     time,
  ADD COLUMN closing_time     time,
  ADD COLUMN kds_stations     text[] NOT NULL DEFAULT '{}',
  ADD COLUMN printer_device   text,
  ADD COLUMN revenue_component text NOT NULL DEFAULT 'fnb';

ALTER TABLE commercial.products
  ADD COLUMN product_type      text NOT NULL DEFAULT 'food' CHECK (product_type IN ('food', 'beverage', 'retail', 'service', 'package')),
  ADD COLUMN price             numeric(19,4) NOT NULL DEFAULT 0 CHECK (price >= 0),
  ADD COLUMN member_price      numeric(19,4) CHECK (member_price IS NULL OR member_price >= 0),
  ADD COLUMN barcode           text,
  ADD COLUMN kitchen_station   text,
  ADD COLUMN outlet_ids        text[] NOT NULL DEFAULT '{}',
  ADD COLUMN combo_items       jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{productId, quantity}] (package menu)
  ADD COLUMN revenue_component text,
  ADD COLUMN voucher_type_id   uuid REFERENCES commercial.voucher_types (id);   -- selling a voucher / ball bucket package at the POS
CREATE INDEX products_barcode ON commercial.products (property_id, barcode) WHERE barcode IS NOT NULL;

CREATE TABLE commercial.product_variants (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  product_id   uuid NOT NULL REFERENCES commercial.products (id),
  code         text NOT NULL,
  name         text NOT NULL,
  price_delta  numeric(19,4) NOT NULL DEFAULT 0,
  barcode      text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.product_variants');
SELECT platform.add_touch_trigger('commercial.product_variants');

CREATE TABLE commercial.modifier_groups (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  min_select   int NOT NULL DEFAULT 0 CHECK (min_select >= 0),
  max_select   int NOT NULL DEFAULT 1 CHECK (max_select >= 1),
  product_ids  text[] NOT NULL DEFAULT '{}',
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code),
  CHECK (max_select >= min_select)
);
SELECT platform.enable_property_rls('commercial.modifier_groups');
SELECT platform.add_touch_trigger('commercial.modifier_groups');

CREATE TABLE commercial.modifiers (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  group_id     uuid NOT NULL REFERENCES commercial.modifier_groups (id),
  code         text NOT NULL,
  name         text NOT NULL,
  price_delta  numeric(19,4) NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.modifiers');
SELECT platform.add_touch_trigger('commercial.modifiers');

-- Menus per outlet and hours (FR-POS-02).
CREATE TABLE commercial.menus (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  outlet_id      uuid NOT NULL REFERENCES commercial.outlets (id),
  code           text NOT NULL,
  name           text NOT NULL,
  available_from time,
  available_to   time,
  weekdays       int[] NOT NULL DEFAULT '{1,2,3,4,5,6,7}',
  product_ids    text[] NOT NULL DEFAULT '{}',
  channels       text[] NOT NULL DEFAULT '{pos}',
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.menus');
SELECT platform.add_touch_trigger('commercial.menus');

CREATE TABLE commercial.pos_shifts (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  shift_no       text NOT NULL,
  outlet_id      uuid NOT NULL REFERENCES commercial.outlets (id),
  cashier_id     uuid NOT NULL REFERENCES platform.users (id),
  device_id      uuid REFERENCES platform.devices (id),
  opening_cash   numeric(19,4) NOT NULL DEFAULT 0,
  counted_cash   numeric(19,4),
  expected_cash  numeric(19,4),
  variance       numeric(19,4),
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  opened_at      timestamptz NOT NULL DEFAULT now(),
  closed_at      timestamptz,
  z_report       jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, shift_no)
);
CREATE UNIQUE INDEX pos_shifts_one_open ON commercial.pos_shifts (outlet_id, cashier_id) WHERE status = 'open';
SELECT platform.enable_property_rls('commercial.pos_shifts');

CREATE TABLE commercial.cash_movements (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  shift_id     uuid NOT NULL REFERENCES commercial.pos_shifts (id),
  kind         text NOT NULL CHECK (kind IN ('cash_in', 'cash_out')),
  amount       numeric(19,4) NOT NULL CHECK (amount > 0),
  reason       text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
SELECT platform.enable_property_rls('commercial.cash_movements');

CREATE TABLE commercial.orders (
  id                  uuid PRIMARY KEY,                 -- client UUIDv7 for offline terminals
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  order_no            text NOT NULL,
  outlet_id           uuid NOT NULL REFERENCES commercial.outlets (id),
  shift_id            uuid REFERENCES commercial.pos_shifts (id),
  order_type          text NOT NULL DEFAULT 'dine_in' CHECK (order_type IN ('dine_in', 'takeaway', 'on_course', 'delivery', 'catering', 'pre_order', 'retail')),
  source              text NOT NULL DEFAULT 'pos' CHECK (source IN ('pos', 'member_app', 'caddy_tablet', 'vip_suite', 'meeting_catering', 'website', 'driving_range')),
  table_no            text,
  guest_count         int,
  customer_id         uuid REFERENCES crm.customers (id),
  member_pricing      boolean NOT NULL DEFAULT false,
  serving_destination text NOT NULL DEFAULT 'table' CHECK (serving_destination IN ('table', 'pickup', 'hole', 'halfway_house', 'vip_suite', 'meeting_room', 'bungalow')),
  destination_ref     text,
  scheduled_for       timestamptz,
  status              text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'paid', 'charged', 'voided', 'refunded')),
  service_status      text NOT NULL DEFAULT 'new' CHECK (service_status IN ('new', 'sent', 'preparing', 'ready', 'out_for_delivery', 'served')),
  charge_folio_id     uuid REFERENCES billing.folios (id),   -- charge to stay / reservation / member folio instead of paying here
  offline             boolean NOT NULL DEFAULT false,
  needs_review        boolean NOT NULL DEFAULT false,
  device_id           uuid,
  notes               text,
  void_reason         text,
  client_created_at   timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (property_id, order_no)
);
CREATE INDEX orders_outlet ON commercial.orders (outlet_id, created_at DESC);
CREATE INDEX orders_customer ON commercial.orders (customer_id, created_at DESC);
SELECT platform.enable_property_rls('commercial.orders');
SELECT platform.add_touch_trigger('commercial.orders');

CREATE TABLE commercial.order_bills (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  order_id     uuid NOT NULL REFERENCES commercial.orders (id),
  bill_no      int NOT NULL,
  label        text,
  customer_id  uuid REFERENCES crm.customers (id),
  share        numeric(9,6),                 -- equal split: fraction of the order
  folio_id     uuid REFERENCES billing.folios (id),
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'paid', 'voided')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (order_id, bill_no)
);
SELECT platform.enable_property_rls('commercial.order_bills');

CREATE TABLE commercial.order_lines (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  order_id             uuid NOT NULL REFERENCES commercial.orders (id),
  line_no              int NOT NULL,
  product_id           uuid NOT NULL REFERENCES commercial.products (id),
  variant_id           uuid REFERENCES commercial.product_variants (id),
  name                 text NOT NULL,
  quantity             numeric(19,4) NOT NULL CHECK (quantity > 0),
  unit_price           numeric(19,4) NOT NULL,
  modifiers            jsonb NOT NULL DEFAULT '[]'::jsonb,
  discount_amount      numeric(19,4) NOT NULL DEFAULT 0,
  discount_reason      text,
  net_amount           numeric(19,4) NOT NULL,
  service_amount       numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount           numeric(19,4) NOT NULL DEFAULT 0,
  total_amount         numeric(19,4) NOT NULL,
  pricing_snapshot_id  uuid,
  kitchen_station      text,
  bill_id              uuid REFERENCES commercial.order_bills (id),
  seat                 text,
  notes                text,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'voided')),
  void_reason          text,
  sent_at              timestamptz,
  charged_folio_id     uuid REFERENCES billing.folios (id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (order_id, line_no)
);
SELECT platform.enable_property_rls('commercial.order_lines');

-- Kitchen Display tickets per station (FR-POS-05, FR-FNB-04).
CREATE TABLE commercial.kitchen_tickets (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  order_id            uuid NOT NULL REFERENCES commercial.orders (id),
  station             text NOT NULL,
  line_ids            uuid[] NOT NULL,
  status              text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'preparing', 'ready', 'out_for_delivery', 'served', 'cancelled')),
  due_at              timestamptz,
  received_at         timestamptz NOT NULL DEFAULT now(),
  preparing_at        timestamptz,
  ready_at            timestamptz,
  out_at              timestamptz,
  served_at           timestamptz,
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kitchen_tickets_station ON commercial.kitchen_tickets (property_id, station, status);
SELECT platform.enable_property_rls('commercial.kitchen_tickets');
SELECT platform.add_touch_trigger('commercial.kitchen_tickets');

-- Refund / void requests waiting for approval (FR-POS-09).
CREATE TABLE commercial.order_requests (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  order_id     uuid NOT NULL REFERENCES commercial.orders (id),
  request_type text NOT NULL CHECK (request_type IN ('refund', 'void', 'discount')),
  payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
  reason       text NOT NULL,
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'applied')),
  approval_id  uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
SELECT platform.enable_property_rls('commercial.order_requests');

SELECT platform.grant_app('commercial');

-- +goose Down
DROP TABLE commercial.order_requests;
DROP TABLE commercial.kitchen_tickets;
DROP TABLE commercial.order_lines;
DROP TABLE commercial.order_bills;
DROP TABLE commercial.orders;
DROP TABLE commercial.cash_movements;
DROP TABLE commercial.pos_shifts;
DROP TABLE commercial.menus;
DROP TABLE commercial.modifiers;
DROP TABLE commercial.modifier_groups;
DROP TABLE commercial.product_variants;
ALTER TABLE commercial.products DROP COLUMN voucher_type_id, DROP COLUMN revenue_component, DROP COLUMN combo_items, DROP COLUMN outlet_ids,
  DROP COLUMN kitchen_station, DROP COLUMN barcode, DROP COLUMN member_price, DROP COLUMN price, DROP COLUMN product_type;
ALTER TABLE commercial.outlets DROP COLUMN revenue_component, DROP COLUMN printer_device, DROP COLUMN kds_stations, DROP COLUMN closing_time,
  DROP COLUMN opening_time, DROP COLUMN pricing_mode, DROP COLUMN tax_codes;
