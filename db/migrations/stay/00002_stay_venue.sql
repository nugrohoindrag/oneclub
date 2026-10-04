-- Stay & Venue (PRD P2 EP-16 Bungalow, EP-17 VIP Suite, EP-18 Meeting Room).
-- Booking inventory, not a PMS (roadmap §73): a stay is a reservation in
-- the Reservation Engine plus the line-specific details kept here.

-- +goose Up
CREATE TABLE stay.bungalow_types (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  max_adults      int NOT NULL DEFAULT 2 CHECK (max_adults > 0),
  max_children    int NOT NULL DEFAULT 1 CHECK (max_children >= 0),
  bedrooms        int NOT NULL DEFAULT 1 CHECK (bedrooms > 0),
  facilities      text[] NOT NULL DEFAULT '{}',
  description     text,
  price_item      text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.bungalow_types');
SELECT platform.add_touch_trigger('stay.bungalow_types');

CREATE TABLE stay.bungalows (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  type_id      uuid NOT NULL REFERENCES stay.bungalow_types (id),
  view         text CHECK (view IN ('golf', 'lake', 'pool', 'garden', 'other')),
  venue_id     uuid REFERENCES platform.venues (id),
  readiness    text NOT NULL DEFAULT 'ready' CHECK (readiness IN ('ready', 'not_ready')),
  resource_id  uuid REFERENCES reservation.resources (id),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.bungalows');
SELECT platform.add_touch_trigger('stay.bungalows');

CREATE TABLE stay.vip_suites (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  facilities   text[] NOT NULL DEFAULT '{}',
  block_hours  int NOT NULL DEFAULT 8 CHECK (block_hours > 0),
  price_item   text,
  resource_id  uuid REFERENCES reservation.resources (id),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.vip_suites');
SELECT platform.add_touch_trigger('stay.vip_suites');

CREATE TABLE stay.meeting_rooms (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  size_sqm     numeric(9,2),
  facilities   text[] NOT NULL DEFAULT '{}',
  price_item   text,
  resource_id  uuid REFERENCES reservation.resources (id),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.meeting_rooms');
SELECT platform.add_touch_trigger('stay.meeting_rooms');

-- Room Capacity per layout (FR-MTG-01).
CREATE TABLE stay.room_layouts (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  meeting_room_id  uuid NOT NULL REFERENCES stay.meeting_rooms (id),
  layout           text NOT NULL CHECK (layout IN ('round_table', 'classroom', 'u_shape', 'theater', 'boardroom', 'cocktail')),
  capacity         int NOT NULL CHECK (capacity > 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  UNIQUE (meeting_room_id, layout)
);
SELECT platform.enable_property_rls('stay.room_layouts');
SELECT platform.add_touch_trigger('stay.room_layouts');

CREATE TABLE stay.equipment (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  quantity     int NOT NULL CHECK (quantity > 0),
  price_item   text,
  resource_id  uuid REFERENCES reservation.resources (id),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('stay.equipment');
SELECT platform.add_touch_trigger('stay.equipment');

CREATE TABLE stay.stays (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  stay_no           text NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('bungalow', 'vip_suite', 'meeting_room')),
  reservation_id    uuid NOT NULL REFERENCES reservation.reservations (id),
  customer_id       uuid REFERENCES crm.customers (id),
  guest_name        text,
  guest_phone       text,
  guest_email       text,
  corporate_name    text,
  unit_id           uuid NOT NULL,             -- bungalow, VIP suite or meeting room
  unit_type_id      uuid,                      -- bungalow type
  unit_assigned     boolean NOT NULL DEFAULT true,
  start_at          timestamptz NOT NULL,
  end_at            timestamptz NOT NULL,
  actual_end_at     timestamptz,
  adults            int NOT NULL DEFAULT 1,
  children          int NOT NULL DEFAULT 0,
  pax               int,
  layout            text,
  rate_plan         text,
  package_code      text,
  special_requests  text,
  event_schedule    jsonb NOT NULL DEFAULT '[]'::jsonb,
  catering          jsonb NOT NULL DEFAULT '[]'::jsonb,
  id_type           text,
  id_number_masked  text,
  status            text NOT NULL DEFAULT 'reserved' CHECK (status IN ('requested', 'reserved', 'checked_in', 'checked_out', 'cancelled', 'no_show')),
  checked_in_at     timestamptz,
  checked_out_at    timestamptz,
  folio_id          uuid REFERENCES billing.folios (id),
  channel           text NOT NULL DEFAULT 'back_office',
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, stay_no),
  CHECK (end_at > start_at)
);
CREATE INDEX stays_time ON stay.stays (property_id, start_at);
CREATE INDEX stays_reservation ON stay.stays (reservation_id);
SELECT platform.enable_property_rls('stay.stays');
SELECT platform.add_touch_trigger('stay.stays');

SELECT platform.grant_app('stay');

-- +goose Down
DROP TABLE stay.stays;
DROP TABLE stay.equipment;
DROP TABLE stay.room_layouts;
DROP TABLE stay.meeting_rooms;
DROP TABLE stay.vip_suites;
DROP TABLE stay.bungalows;
DROP TABLE stay.bungalow_types;
