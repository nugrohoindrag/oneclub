-- Driving Range booking (demo feedback 9 Oct 2026; PRD P2 FR-RNG-02 "booking
-- opsional lewat EP-01"): a bay and a time held in the Reservation Engine,
-- or only an announced visit (balls bought at the counter). The time is
-- flexible: at check-in the guest gets the booked bay when free, otherwise
-- another bay or the queue (expand only).

-- +goose Up
CREATE TABLE golf.range_bookings (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  number           text NOT NULL,
  play_date        date NOT NULL,
  start_at         timestamptz NOT NULL,
  end_at           timestamptz NOT NULL CHECK (end_at > start_at),
  area             text NOT NULL DEFAULT 'outdoor' CHECK (area IN ('indoor', 'outdoor')),
  bay_id           uuid REFERENCES golf.range_bays (id),
  reservation_id   uuid,
  bay_released_at  timestamptz,
  players          int NOT NULL DEFAULT 1 CHECK (players BETWEEN 1 AND 8),
  customer_id      uuid REFERENCES crm.customers (id),
  guest_name       text NOT NULL,
  guest_phone      text,
  guest_email      text,
  channel          text NOT NULL CHECK (channel IN ('member_app', 'website', 'back_office', 'walk_in')),
  status           text NOT NULL DEFAULT 'booked' CHECK (status IN ('booked', 'checked_in', 'cancelled', 'no_show')),
  session_id       uuid REFERENCES golf.range_sessions (id),
  notes            text,
  checked_in_at    timestamptz,
  cancelled_at     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
CREATE INDEX range_bookings_day_idx ON golf.range_bookings (property_id, play_date);
CREATE INDEX range_bookings_customer_idx ON golf.range_bookings (customer_id) WHERE customer_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.range_bookings');
SELECT platform.add_touch_trigger('golf.range_bookings');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.range_bookings;
