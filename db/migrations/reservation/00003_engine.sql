-- Reservation Engine (PRD P2 EP-01): one engine for courts, entries, class
-- sessions, bungalows, VIP suites, meeting rooms, equipment and driving range
-- bays. Expand-only: the exclusive allocation behaviour of P0 (EXCLUDE on
-- reservation.allocations, contract C9 used by P1 tee times) is unchanged.

-- +goose Up
CREATE TABLE reservation.resource_types (
  id                     uuid PRIMARY KEY,
  code                   text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9_]{1,39}$'),
  name                   text NOT NULL,
  business_line          text NOT NULL DEFAULT 'other' CHECK (business_line IN ('golf', 'sportclub', 'stay', 'banquet', 'other')),
  reservation_model      text NOT NULL CHECK (reservation_model IN ('time_slot', 'entry', 'class_session', 'night', 'day_use', 'block', 'package', 'quantity', 'bay', 'tee_time')),
  allocation_mode        text NOT NULL CHECK (allocation_mode IN ('exclusive', 'capacity')),
  slot_minutes           int NOT NULL DEFAULT 60 CHECK (slot_minutes > 0),
  buffer_before_minutes  int NOT NULL DEFAULT 0 CHECK (buffer_before_minutes >= 0),
  buffer_after_minutes   int NOT NULL DEFAULT 0 CHECK (buffer_after_minutes >= 0),
  hold_minutes           int NOT NULL DEFAULT 15 CHECK (hold_minutes > 0),
  open_time              time NOT NULL DEFAULT '07:00',
  close_time             time NOT NULL DEFAULT '21:00',
  check_in_time          time,   -- night model: check-in / check-out times
  check_out_time         time,
  service_type           text,   -- pricing service type
  sort_order             int NOT NULL DEFAULT 0,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  archived_at            timestamptz
);
SELECT platform.add_touch_trigger('reservation.resource_types');

INSERT INTO reservation.resource_types (id, code, name, business_line, reservation_model, allocation_mode, slot_minutes,
  buffer_before_minutes, buffer_after_minutes, hold_minutes, open_time, close_time, check_in_time, check_out_time, service_type, sort_order, status) VALUES
  (gen_random_uuid(), 'tee_time',          'Tee Time',          'golf',      'tee_time',      'exclusive', 8,    0,  0, 15, '05:30', '20:00', NULL, NULL, 'golf', 10, 'active'),
  (gen_random_uuid(), 'sport_court',       'Sport Court',       'sportclub', 'time_slot',     'exclusive', 60,   0,  0, 15, '07:00', '21:00', NULL, NULL, 'sport_court', 20, 'active'),
  (gen_random_uuid(), 'facility_entry',    'Facility Entry',    'sportclub', 'entry',         'capacity',  1440, 0,  0, 15, '06:00', '21:00', NULL, NULL, 'facility_entry', 30, 'active'),
  (gen_random_uuid(), 'class_session',     'Class Session',     'sportclub', 'class_session', 'capacity',  60,   0,  0, 15, '06:00', '21:00', NULL, NULL, 'class_session', 40, 'active'),
  (gen_random_uuid(), 'class_facility',    'Class Facility',    'sportclub', 'time_slot',     'exclusive', 30,   0,  0, 15, '06:00', '21:00', NULL, NULL, NULL, 45, 'active'),
  (gen_random_uuid(), 'bungalow',          'Bungalow',          'stay',      'night',         'exclusive', 1440, 0, 60, 30, '00:00', '23:59', '14:00', '12:00', 'bungalow', 50, 'active'),
  (gen_random_uuid(), 'vip_suite',         'VIP Suite',         'stay',      'block',         'exclusive', 60,   0, 30, 30, '07:00', '23:59', NULL, NULL, 'vip_suite', 60, 'active'),
  (gen_random_uuid(), 'meeting_room',      'Meeting Room',      'stay',      'package',       'exclusive', 60,  60, 30, 60, '06:00', '23:59', NULL, NULL, 'meeting_room', 70, 'active'),
  (gen_random_uuid(), 'equipment',         'Equipment',         'stay',      'quantity',      'capacity',  60,   0,  0, 60, '06:00', '23:59', NULL, NULL, 'equipment', 80, 'active'),
  (gen_random_uuid(), 'driving_range_bay', 'Driving Range Bay', 'golf',      'bay',           'exclusive', 60,   0,  0, 15, '06:00', '22:00', NULL, NULL, 'driving_range', 90, 'active'),
  (gen_random_uuid(), 'banquet_venue',     'Banquet Venue',     'banquet',   'block',         'exclusive', 60,  60, 60, 60, '06:00', '23:59', NULL, NULL, NULL, 100, 'inactive'),
  (gen_random_uuid(), 'event',             'Event',             'banquet',   'block',         'exclusive', 60,   0,  0, 60, '06:00', '23:59', NULL, NULL, NULL, 110, 'inactive'),
  (gen_random_uuid(), 'other',             'Other',             'other',     'time_slot',     'exclusive', 60,   0,  0, 15, '07:00', '21:00', NULL, NULL, NULL, 999, 'active');

-- Capacity mode (FR-RSV-02): a slot holds `capacity` places; `booked` is
-- maintained by the engine in the same transaction and can never exceed the
-- capacity (CHECK + row lock of the UPDATE). Slots of one resource never
-- overlap.
CREATE TABLE reservation.capacity_slots (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  resource_id  uuid NOT NULL REFERENCES reservation.resources (id),
  period       tstzrange NOT NULL CHECK (NOT isempty(period)),
  capacity     int NOT NULL CHECK (capacity >= 0),
  booked       int NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  source_type  text,
  source_id    uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT capacity_slots_booked CHECK (booked >= 0 AND booked <= capacity),
  CONSTRAINT capacity_slots_no_overlap EXCLUDE USING gist (resource_id WITH =, period WITH &&)
);
CREATE INDEX capacity_slots_resource ON reservation.capacity_slots (resource_id, lower(period));
SELECT platform.enable_property_rls('reservation.capacity_slots');
SELECT platform.add_touch_trigger('reservation.capacity_slots');

CREATE TABLE reservation.reservations (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  code                text NOT NULL,
  kind                text NOT NULL DEFAULT 'booking' CHECK (kind IN ('booking', 'block', 'class')),
  business_line       text NOT NULL DEFAULT 'other',
  status              text NOT NULL CHECK (status IN ('draft', 'pending', 'confirmed', 'checked_in', 'completed', 'cancelled', 'no_show', 'expired')),
  customer_id         uuid REFERENCES crm.customers (id),
  guest_name          text,
  guest_phone         text,
  guest_email         text,
  corporate_name      text,
  channel             text NOT NULL DEFAULT 'back_office' CHECK (channel IN ('member_app', 'website', 'back_office', 'walk_in', 'import', 'ops')),
  source_type         text,
  source_id           uuid,
  recurring_group_id  uuid,
  hold_expires_at     timestamptz,
  deposit_required    numeric(19,4) NOT NULL DEFAULT 0,
  deposit_due_at      timestamptz,
  folio_id            uuid REFERENCES billing.folios (id),
  policy_refs         jsonb NOT NULL DEFAULT '[]'::jsonb,
  notes               text,
  attributes          jsonb NOT NULL DEFAULT '{}'::jsonb,
  confirmed_at        timestamptz,
  checked_in_at       timestamptz,
  completed_at        timestamptz,
  cancelled_at        timestamptz,
  cancel_reason       text,
  cancellation_fee    numeric(19,4),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (property_id, code),
  CHECK (status <> 'draft' OR hold_expires_at IS NOT NULL)
);
CREATE INDEX reservations_customer ON reservation.reservations (customer_id, created_at DESC);
CREATE INDEX reservations_status ON reservation.reservations (property_id, status);
CREATE INDEX reservations_hold ON reservation.reservations (hold_expires_at) WHERE status = 'draft';
CREATE INDEX reservations_source ON reservation.reservations (source_type, source_id);
SELECT platform.enable_property_rls('reservation.reservations');
SELECT platform.add_touch_trigger('reservation.reservations');

CREATE TABLE reservation.reservation_lines (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  reservation_id       uuid NOT NULL REFERENCES reservation.reservations (id),
  line_no              int NOT NULL,
  resource_id          uuid NOT NULL REFERENCES reservation.resources (id),
  resource_type        text NOT NULL,
  allocation_mode      text NOT NULL CHECK (allocation_mode IN ('exclusive', 'capacity')),
  period               tstzrange NOT NULL CHECK (NOT isempty(period)),
  quantity             int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  allocation_id        uuid REFERENCES reservation.allocations (id),
  status               text NOT NULL CHECK (status IN ('held', 'confirmed', 'checked_in', 'completed', 'released', 'cancelled')),
  description          text,
  pricing_snapshot_id  uuid,
  amount               numeric(19,4),
  attributes           jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (reservation_id, line_no)
);
CREATE INDEX reservation_lines_resource ON reservation.reservation_lines (resource_id, lower(period));
SELECT platform.enable_property_rls('reservation.reservation_lines');
SELECT platform.add_touch_trigger('reservation.reservation_lines');

CREATE TABLE reservation.capacity_allocations (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  slot_id         uuid NOT NULL REFERENCES reservation.capacity_slots (id),
  resource_id     uuid NOT NULL REFERENCES reservation.resources (id),
  reservation_id  uuid NOT NULL REFERENCES reservation.reservations (id),
  line_id         uuid NOT NULL REFERENCES reservation.reservation_lines (id),
  quantity        int NOT NULL CHECK (quantity > 0),
  status          text NOT NULL CHECK (status IN ('held', 'confirmed', 'released', 'cancelled')),
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CHECK (status <> 'held' OR expires_at IS NOT NULL)
);
CREATE INDEX capacity_allocations_line ON reservation.capacity_allocations (line_id);
CREATE INDEX capacity_allocations_expiring ON reservation.capacity_allocations (expires_at) WHERE status = 'held';
SELECT platform.enable_property_rls('reservation.capacity_allocations');
SELECT platform.add_touch_trigger('reservation.capacity_allocations');

-- Booking History & Modification (FR-RSV-09).
CREATE TABLE reservation.reservation_history (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  reservation_id  uuid NOT NULL REFERENCES reservation.reservations (id),
  action          text NOT NULL,
  from_status     text,
  to_status       text,
  channel         text NOT NULL,
  reason          text,
  actor_id        uuid,
  actor_name      text,
  details         jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reservation_history_reservation ON reservation.reservation_history (reservation_id, created_at);
SELECT platform.enable_property_rls('reservation.reservation_history');

SELECT platform.grant_app('reservation');

-- +goose Down
DROP TABLE reservation.reservation_history;
DROP TABLE reservation.capacity_allocations;
DROP TABLE reservation.reservation_lines;
DROP TABLE reservation.reservations;
DROP TABLE reservation.capacity_slots;
DROP TABLE reservation.resource_types;
