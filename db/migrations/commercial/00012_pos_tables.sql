-- POS Table View (PRD P2 FR-POS-01 dine-in, product owner design 7 Oct
-- 2026): the dining tables of an outlet placed on its floor plan, table
-- reservations, the tables an order seats and the bill presented to the
-- table, and a photo per product for the POS menu. Expand-only.

-- +goose Up
-- Floor plan: pos_x / pos_y are the centre of the table in percent of the
-- plan (0–100), so the plan scales with the screen.
CREATE TABLE commercial.dining_tables (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  outlet_id    uuid NOT NULL REFERENCES commercial.outlets (id),
  code         text NOT NULL,
  area         text,
  seats        int NOT NULL DEFAULT 4 CHECK (seats BETWEEN 1 AND 30),
  shape        text NOT NULL DEFAULT 'square' CHECK (shape IN ('square', 'round', 'rect', 'seat')),
  pos_x        numeric(5,2) NOT NULL DEFAULT 50 CHECK (pos_x BETWEEN 0 AND 100),
  pos_y        numeric(5,2) NOT NULL DEFAULT 50 CHECK (pos_y BETWEEN 0 AND 100),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (outlet_id, code)
);
SELECT platform.enable_property_rls('commercial.dining_tables');
SELECT platform.add_touch_trigger('commercial.dining_tables');

CREATE TABLE commercial.table_reservations (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  outlet_id         uuid NOT NULL REFERENCES commercial.outlets (id),
  table_ids         text[] NOT NULL DEFAULT '{}',
  customer_id       uuid REFERENCES crm.customers (id),
  guest_name        text NOT NULL,
  phone             text,
  guest_count       int NOT NULL DEFAULT 2 CHECK (guest_count BETWEEN 1 AND 200),
  reserved_for      timestamptz NOT NULL,
  duration_minutes  int NOT NULL DEFAULT 90 CHECK (duration_minutes BETWEEN 15 AND 720),
  status            text NOT NULL DEFAULT 'booked' CHECK (status IN ('booked', 'seated', 'cancelled', 'no_show')),
  order_id          uuid REFERENCES commercial.orders (id),
  notes             text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid
);
CREATE INDEX table_reservations_outlet ON commercial.table_reservations (outlet_id, reserved_for) WHERE status = 'booked';
SELECT platform.enable_property_rls('commercial.table_reservations');
SELECT platform.add_touch_trigger('commercial.table_reservations');

ALTER TABLE commercial.orders
  ADD COLUMN table_ids             uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN table_reservation_id  uuid REFERENCES commercial.table_reservations (id),
  ADD COLUMN billed_at             timestamptz;
CREATE INDEX orders_open_tables ON commercial.orders USING gin (table_ids) WHERE status = 'open';

ALTER TABLE commercial.products ADD COLUMN image_url text;

SELECT platform.grant_app('commercial');

-- +goose Down
ALTER TABLE commercial.products DROP COLUMN image_url;
DROP INDEX commercial.orders_open_tables;
ALTER TABLE commercial.orders DROP COLUMN billed_at, DROP COLUMN table_reservation_id, DROP COLUMN table_ids;
DROP TABLE commercial.table_reservations;
DROP TABLE commercial.dining_tables;
