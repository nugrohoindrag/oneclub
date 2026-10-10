-- Court booking of the Sport Club MGCC (docs/requirement-booking-sportclub-
-- mgcc.md): the sports (facilities) and courts carry their website content,
-- order and online booking switch; recurring bookings of communities and
-- companies; court readiness and problem reports of the court staff; Sport
-- Club incidents; the 15-minute reminders already sent; the membership
-- prospects found among frequent players. Expand-only.

-- +goose Up
ALTER TABLE sportclub.facilities
  ADD COLUMN sort_order     int NOT NULL DEFAULT 0,
  ADD COLUMN online_booking boolean NOT NULL DEFAULT true,
  -- {"nameEn", "slug", "icon", "photos": [], "description": {"id", "en"}, "rules": {"id": [], "en": []}, "amenities": [], "faq": [{"q", "a"}]}
  ADD COLUMN content        jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- {"minHours", "maxHours", "eveningFrom"}: overrides of the Court Booking policy
  ADD COLUMN booking_rules  jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE sportclub.courts
  ADD COLUMN sort_order     int NOT NULL DEFAULT 0,
  ADD COLUMN online_booking boolean NOT NULL DEFAULT true,
  ADD COLUMN photo_url      text;

-- Booking Rutin (FR-64, FR-75, FR-128): one reservation per meeting, linked
-- by group_id (reservation.reservations.recurring_group_id).
CREATE TABLE sportclub.recurring_bookings (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  group_id        uuid NOT NULL UNIQUE,
  court_id        uuid NOT NULL REFERENCES sportclub.courts (id),
  customer_id     uuid REFERENCES crm.customers (id),
  holder_name     text NOT NULL,
  phone           text,
  corporate_name  text,
  weekdays        int[] NOT NULL CHECK (weekdays <@ ARRAY[1,2,3,4,5,6,7] AND cardinality(weekdays) > 0),
  start_time      time NOT NULL,
  hours           int NOT NULL CHECK (hours BETWEEN 1 AND 6),
  start_date      date NOT NULL,
  end_date        date NOT NULL,
  payment_mode    text NOT NULL DEFAULT 'pay_per_visit' CHECK (payment_mode IN ('prepaid', 'package', 'pay_per_visit', 'invoice')),
  package_code    text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'stopped', 'ended')),
  paused_from     date,
  paused_until    date,
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, code),
  CHECK (end_date >= start_date)
);
SELECT platform.enable_property_rls('sportclub.recurring_bookings');
SELECT platform.add_touch_trigger('sportclub.recurring_bookings');

-- Court staff (FR-119): court ready before a booking, or a problem that
-- closes the court for a while (temporary block) and warns the front desk.
CREATE TABLE sportclub.court_reports (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  court_id        uuid NOT NULL REFERENCES sportclub.courts (id),
  kind            text NOT NULL CHECK (kind IN ('ready', 'issue')),
  note            text,
  reservation_id  uuid REFERENCES reservation.reservations (id),
  block_id        uuid REFERENCES reservation.reservations (id),
  until           timestamptz,
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  resolved_at     timestamptz,
  resolved_by     uuid
);
CREATE INDEX court_reports_court ON sportclub.court_reports (court_id, created_at DESC);
SELECT platform.enable_property_rls('sportclub.court_reports');

-- Incidents of the Sport Club (FR-107): injuries, damage, complaints.
CREATE TABLE sportclub.incidents (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  category        text NOT NULL CHECK (category IN ('injury', 'damage', 'complaint', 'lost_item', 'other')),
  severity        text NOT NULL DEFAULT 'medium' CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  facility_id     uuid REFERENCES sportclub.facilities (id),
  court_id        uuid REFERENCES sportclub.courts (id),
  reservation_id  uuid REFERENCES reservation.reservations (id),
  customer_id     uuid REFERENCES crm.customers (id),
  person_name     text,
  description     text NOT NULL,
  action_taken    text,
  damage_amount   numeric(19,4),
  occurred_at     timestamptz NOT NULL DEFAULT now(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  closed_at       timestamptz,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('sportclub.incidents');
SELECT platform.add_touch_trigger('sportclub.incidents');

-- 15-minute reminders sent per booking line (FR-59): sent once.
CREATE TABLE sportclub.booking_reminders (
  line_id      uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  sent_at      timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('sportclub.booking_reminders');

-- Membership prospects (FR-89): non-members who play often, flagged once
-- and handed to Sales as a lead.
CREATE TABLE sportclub.membership_prospects (
  customer_id   uuid PRIMARY KEY REFERENCES crm.customers (id),
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  bookings      int NOT NULL,
  flagged_at    timestamptz NOT NULL DEFAULT now(),
  lead_ref      text
);
SELECT platform.enable_property_rls('sportclub.membership_prospects');

SELECT platform.grant_app('sportclub');

-- +goose Down
DROP TABLE sportclub.membership_prospects;
DROP TABLE sportclub.booking_reminders;
DROP TABLE sportclub.incidents;
DROP TABLE sportclub.court_reports;
DROP TABLE sportclub.recurring_bookings;
ALTER TABLE sportclub.courts DROP COLUMN photo_url, DROP COLUMN online_booking, DROP COLUMN sort_order;
ALTER TABLE sportclub.facilities DROP COLUMN booking_rules, DROP COLUMN content, DROP COLUMN online_booking, DROP COLUMN sort_order;
