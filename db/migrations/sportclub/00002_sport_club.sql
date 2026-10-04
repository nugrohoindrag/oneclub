-- Sport Club & Facility (PRD P2 EP-14) and Classes & Training (EP-15).
-- Courts, entries and class seats are booked through the Reservation Engine.

-- +goose Up
ALTER TABLE sportclub.facilities
  ADD COLUMN usage_mode     text NOT NULL DEFAULT 'entry' CHECK (usage_mode IN ('slot_booking', 'entry', 'class')),
  ADD COLUMN min_age        int CHECK (min_age IS NULL OR min_age >= 0),
  ADD COLUMN max_age        int CHECK (max_age IS NULL OR max_age >= 0),
  ADD COLUMN opening_hours  jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {"weekday": ["06:00","21:00"], "weekend": [...], "holiday": [...]}
  ADD COLUMN price_item     text,
  ADD COLUMN resource_id    uuid REFERENCES reservation.resources (id);

CREATE TABLE sportclub.courts (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  facility_id  uuid NOT NULL REFERENCES sportclub.facilities (id),
  code         text NOT NULL,
  name         text NOT NULL,
  surface      text,
  indoor       boolean NOT NULL DEFAULT false,
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
SELECT platform.enable_property_rls('sportclub.courts');
SELECT platform.add_touch_trigger('sportclub.courts');

-- Entry Ticket (FR-SPT-04).
CREATE TABLE sportclub.entries (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  ticket_no            text NOT NULL,
  facility_id          uuid NOT NULL REFERENCES sportclub.facilities (id),
  entry_type           text NOT NULL CHECK (entry_type IN ('walk_in_guest', 'guest_of_member', 'child', 'family_package', 'member', 'voucher', 'staying_guest')),
  customer_id          uuid REFERENCES crm.customers (id),
  host_customer_id     uuid REFERENCES crm.customers (id),
  guest_name           text,
  adults               int NOT NULL DEFAULT 1 CHECK (adults >= 0),
  children             int NOT NULL DEFAULT 0 CHECK (children >= 0),
  visit_date           date NOT NULL,
  amount               numeric(19,4) NOT NULL DEFAULT 0,
  pricing_snapshot_id  uuid,
  folio_id             uuid REFERENCES billing.folios (id),
  voucher_id           uuid,
  reservation_id       uuid REFERENCES reservation.reservations (id),
  qr_token             text NOT NULL UNIQUE,
  status               text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'used', 'cancelled', 'expired')),
  used_at              timestamptz,
  channel              text NOT NULL DEFAULT 'ops',
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, ticket_no)
);
CREATE INDEX entries_day ON sportclub.entries (property_id, visit_date);
SELECT platform.enable_property_rls('sportclub.entries');
SELECT platform.add_touch_trigger('sportclub.entries');

-- Facility Access & Access History (FR-SPT-05).
CREATE TABLE sportclub.access_events (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  facility_id      uuid NOT NULL REFERENCES sportclub.facilities (id),
  credential_type  text NOT NULL CHECK (credential_type IN ('member_card', 'ticket', 'booking', 'stay', 'unknown')),
  code_hint        text,
  customer_id      uuid REFERENCES crm.customers (id),
  entry_id         uuid REFERENCES sportclub.entries (id),
  reservation_id   uuid REFERENCES reservation.reservations (id),
  direction        text NOT NULL DEFAULT 'in' CHECK (direction IN ('in', 'out')),
  result           text NOT NULL CHECK (result IN ('granted', 'denied')),
  reason           text,
  terminal         text,
  offline          boolean NOT NULL DEFAULT false,
  occurred_at      timestamptz NOT NULL DEFAULT now(),
  created_by       uuid
);
CREATE INDEX access_events_day ON sportclub.access_events (property_id, facility_id, occurred_at);
CREATE INDEX access_events_customer ON sportclub.access_events (customer_id, occurred_at);
SELECT platform.enable_property_rls('sportclub.access_events');

-- Lockers (FR-SPT-07).
CREATE TABLE sportclub.lockers (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  area         text NOT NULL DEFAULT 'unisex' CHECK (area IN ('male', 'female', 'unisex')),
  facility_id  uuid REFERENCES sportclub.facilities (id),
  size         text,
  status       text NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'occupied', 'maintenance', 'out_of_service')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('sportclub.lockers');
SELECT platform.add_touch_trigger('sportclub.lockers');

CREATE TABLE sportclub.locker_assignments (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  locker_id        uuid NOT NULL REFERENCES sportclub.lockers (id),
  customer_id      uuid REFERENCES crm.customers (id),
  guest_name       text,
  assignment_type  text NOT NULL DEFAULT 'daily' CHECK (assignment_type IN ('daily', 'rental')),
  entry_id         uuid REFERENCES sportclub.entries (id),
  start_at         timestamptz NOT NULL DEFAULT now(),
  end_at           timestamptz,
  returned_at      timestamptz,
  fee              numeric(19,4) NOT NULL DEFAULT 0,
  folio_id         uuid REFERENCES billing.folios (id),
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'returned', 'cancelled')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid
);
CREATE UNIQUE INDEX locker_assignments_active ON sportclub.locker_assignments (locker_id) WHERE status = 'active';
SELECT platform.enable_property_rls('sportclub.locker_assignments');

-- Classes & Training (EP-15).
CREATE TABLE sportclub.instructors (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  partnership     text NOT NULL DEFAULT 'partner' CHECK (partnership IN ('employee', 'partner')),
  employee_id     uuid REFERENCES platform.employees (id),
  user_id         uuid REFERENCES platform.users (id),
  phone           text,
  email           text,
  disciplines     text[] NOT NULL DEFAULT '{}',
  certifications  jsonb NOT NULL DEFAULT '[]'::jsonb,
  fee_scheme      text NOT NULL DEFAULT 'per_session' CHECK (fee_scheme IN ('per_session', 'per_student')),
  fee_rate        numeric(19,4) NOT NULL DEFAULT 0 CHECK (fee_rate >= 0),
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('sportclub.instructors');
SELECT platform.add_touch_trigger('sportclub.instructors');

CREATE TABLE sportclub.class_programs (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL,
  name              text NOT NULL,
  discipline        text NOT NULL CHECK (discipline IN ('swimming', 'tennis', 'aikido', 'aerobic', 'gym', 'badminton', 'squash', 'other')),
  level             text,
  min_age           int,
  max_age           int,
  capacity          int NOT NULL CHECK (capacity > 0),
  duration_minutes  int NOT NULL DEFAULT 60 CHECK (duration_minutes > 0),
  facility_id       uuid REFERENCES sportclub.facilities (id),
  resource_id       uuid REFERENCES reservation.resources (id),   -- class_session capacity resource
  registration_valid_months int NOT NULL DEFAULT 12,
  description       text,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('sportclub.class_programs');
SELECT platform.add_touch_trigger('sportclub.class_programs');

CREATE TABLE sportclub.class_schedules (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  program_id     uuid NOT NULL REFERENCES sportclub.class_programs (id),
  instructor_id  uuid NOT NULL REFERENCES sportclub.instructors (id),
  facility_id    uuid REFERENCES sportclub.facilities (id),
  weekdays       int[] NOT NULL CHECK (weekdays <@ ARRAY[1,2,3,4,5,6,7] AND cardinality(weekdays) > 0),
  start_time     time NOT NULL,
  start_date     date NOT NULL,
  end_date       date NOT NULL,
  capacity       int CHECK (capacity IS NULL OR capacity > 0),
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  CHECK (end_date >= start_date)
);
SELECT platform.enable_property_rls('sportclub.class_schedules');
SELECT platform.add_touch_trigger('sportclub.class_schedules');

-- Class sessions: instructor and facility conflicts are rejected by the
-- database (FR-CLS-03).
CREATE TABLE sportclub.class_sessions (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  schedule_id    uuid REFERENCES sportclub.class_schedules (id),
  program_id     uuid NOT NULL REFERENCES sportclub.class_programs (id),
  instructor_id  uuid NOT NULL REFERENCES sportclub.instructors (id),
  facility_id    uuid REFERENCES sportclub.facilities (id),
  period         tstzrange NOT NULL CHECK (NOT isempty(period)),
  capacity       int NOT NULL CHECK (capacity > 0),
  status         text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'completed', 'cancelled')),
  cancel_reason  text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  CONSTRAINT class_sessions_instructor_free EXCLUDE USING gist (instructor_id WITH =, period WITH &&) WHERE (status <> 'cancelled'),
  CONSTRAINT class_sessions_facility_free EXCLUDE USING gist (facility_id WITH =, period WITH &&) WHERE (status <> 'cancelled' AND facility_id IS NOT NULL)
);
CREATE INDEX class_sessions_time ON sportclub.class_sessions (property_id, lower(period));
SELECT platform.enable_property_rls('sportclub.class_sessions');
SELECT platform.add_touch_trigger('sportclub.class_sessions');

-- Class Registration (registration fee once per program and period).
CREATE TABLE sportclub.enrollments (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  program_id          uuid NOT NULL REFERENCES sportclub.class_programs (id),
  customer_id         uuid NOT NULL REFERENCES crm.customers (id),
  segment             text NOT NULL DEFAULT 'guest',
  valid_until         date NOT NULL,
  registration_fee    numeric(19,4) NOT NULL DEFAULT 0,
  folio_id            uuid REFERENCES billing.folios (id),
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'cancelled')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid
);
CREATE UNIQUE INDEX enrollments_active ON sportclub.enrollments (program_id, customer_id) WHERE status = 'active';
SELECT platform.enable_property_rls('sportclub.enrollments');
SELECT platform.add_touch_trigger('sportclub.enrollments');

-- Session roster: seat in a session (capacity allocation) and attendance.
CREATE TABLE sportclub.session_bookings (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  session_id      uuid NOT NULL REFERENCES sportclub.class_sessions (id),
  enrollment_id   uuid NOT NULL REFERENCES sportclub.enrollments (id),
  customer_id     uuid NOT NULL REFERENCES crm.customers (id),
  reservation_id  uuid REFERENCES reservation.reservations (id),
  voucher_id      uuid,
  status          text NOT NULL DEFAULT 'booked' CHECK (status IN ('booked', 'present', 'absent', 'excused', 'cancelled')),
  quota_used      boolean NOT NULL DEFAULT false,
  marked_by       uuid,
  marked_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid
);
CREATE UNIQUE INDEX session_bookings_once ON sportclub.session_bookings (session_id, customer_id) WHERE status <> 'cancelled';
SELECT platform.enable_property_rls('sportclub.session_bookings');

-- Instructor Fee / honorarium (FR-CLS-08).
CREATE TABLE sportclub.instructor_fees (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  fee_no         text NOT NULL,
  instructor_id  uuid NOT NULL REFERENCES sportclub.instructors (id),
  period_start   date NOT NULL,
  period_end     date NOT NULL,
  sessions       int NOT NULL DEFAULT 0,
  students       int NOT NULL DEFAULT 0,
  rate           numeric(19,4) NOT NULL DEFAULT 0,
  amount         numeric(19,4) NOT NULL DEFAULT 0,
  lines          jsonb NOT NULL DEFAULT '[]'::jsonb,
  status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'paid')),
  approval_id    uuid,
  payout_id      uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (property_id, fee_no),
  CHECK (period_end >= period_start)
);
SELECT platform.enable_property_rls('sportclub.instructor_fees');
SELECT platform.add_touch_trigger('sportclub.instructor_fees');

SELECT platform.grant_app('sportclub');

-- +goose Down
DROP TABLE sportclub.instructor_fees;
DROP TABLE sportclub.session_bookings;
DROP TABLE sportclub.enrollments;
DROP TABLE sportclub.class_sessions;
DROP TABLE sportclub.class_schedules;
DROP TABLE sportclub.class_programs;
DROP TABLE sportclub.instructors;
DROP TABLE sportclub.locker_assignments;
DROP TABLE sportclub.lockers;
DROP TABLE sportclub.access_events;
DROP TABLE sportclub.entries;
DROP TABLE sportclub.courts;
ALTER TABLE sportclub.facilities DROP COLUMN resource_id, DROP COLUMN price_item, DROP COLUMN opening_hours, DROP COLUMN max_age,
  DROP COLUMN min_age, DROP COLUMN usage_mode;
