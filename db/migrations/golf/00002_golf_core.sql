-- P1 Golf Core MVP (PRD §9): course structure (EP-02), tee sheet (EP-03),
-- booking / flight / player (EP-04, EP-05), caddy (EP-09), golf cart
-- (EP-10), check-in, bag drop, locker and starter (EP-11). Tee time seats
-- are locked in reservation.allocations (EXCLUDE, FR-BKG-02).

-- +goose Up
-- ── EP-02 Course structure ─────────────────────────────────────────────────
ALTER TABLE golf.courses
  ADD COLUMN length_meters  int CHECK (length_meters IS NULL OR length_meters > 0),
  ADD COLUMN par            int,
  ADD COLUMN description    text,
  ADD COLUMN guide          text;      -- Course Guide text for the website

CREATE TABLE golf.course_sections (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  course_id    uuid NOT NULL REFERENCES golf.courses (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  sequence     int NOT NULL DEFAULT 1,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (course_id, code)
);
SELECT platform.enable_property_rls('golf.course_sections');
SELECT platform.add_touch_trigger('golf.course_sections');

CREATE TABLE golf.tee_sets (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  course_id      uuid NOT NULL REFERENCES golf.courses (id),
  code           text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name           text NOT NULL,
  color          text,
  course_rating  numeric(4,1) CHECK (course_rating IS NULL OR (course_rating > 0 AND course_rating < 100)),
  slope          int CHECK (slope IS NULL OR slope BETWEEN 55 AND 155),
  gender         text CHECK (gender IS NULL OR gender IN ('male', 'female', 'any')),
  sequence       int NOT NULL DEFAULT 1,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (course_id, code)
);
SELECT platform.enable_property_rls('golf.tee_sets');
SELECT platform.add_touch_trigger('golf.tee_sets');

CREATE TABLE golf.holes (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  course_id     uuid NOT NULL REFERENCES golf.courses (id),
  section_id    uuid NOT NULL REFERENCES golf.course_sections (id),
  code          text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),   -- e.g. H01
  number        int NOT NULL CHECK (number BETWEEN 1 AND 36),
  par           int NOT NULL CHECK (par BETWEEN 3 AND 6),
  stroke_index  int CHECK (stroke_index IS NULL OR stroke_index BETWEEN 1 AND 36),
  distances     jsonb NOT NULL DEFAULT '{}'::jsonb,   -- {"<tee set code>": meters}
  description   text,                                  -- Hole-by-Hole text
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (course_id, code),
  UNIQUE (course_id, number)
);
SELECT platform.enable_property_rls('golf.holes');
SELECT platform.add_touch_trigger('golf.holes');

-- A playing route is an ordered combination of sections; its holes are
-- derived, so Front Nine / Back Nine / Championship 18 need no hole re-entry.
CREATE TABLE golf.playing_routes (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  course_id    uuid NOT NULL REFERENCES golf.courses (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  section_codes text NOT NULL CHECK (section_codes ~ '^[A-Z0-9_-]+(,[A-Z0-9_-]+)*$'),  -- ordered, e.g. FRONT,BACK
  hole_count   int NOT NULL DEFAULT 0,
  is_default   boolean NOT NULL DEFAULT false,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (course_id, code)
);
SELECT platform.enable_property_rls('golf.playing_routes');
SELECT platform.add_touch_trigger('golf.playing_routes');
ALTER TABLE commercial.pricing_rules
  ADD CONSTRAINT pricing_rules_route_fk FOREIGN KEY (playing_route_id) REFERENCES golf.playing_routes (id);

-- FR-CRS-06 course assets (map, panorama, hazards, POI, green points).
CREATE TABLE golf.course_assets (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  course_id    uuid NOT NULL REFERENCES golf.courses (id),
  hole_id      uuid REFERENCES golf.holes (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  asset_type   text NOT NULL CHECK (asset_type IN ('course_map', 'panorama', 'hazard', 'point_of_interest',
                 'green_front', 'green_center', 'green_back', 'distance_marker')),
  name         text NOT NULL,
  file_id      uuid REFERENCES platform.files (id),
  geometry     jsonb NOT NULL DEFAULT '{}'::jsonb,   -- GeoJSON Point / Polygon
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (course_id, code)
);
SELECT platform.enable_property_rls('golf.course_assets');
SELECT platform.add_touch_trigger('golf.course_assets');

-- FR-CRS-05 course availability blocking.
CREATE TABLE golf.course_blocks (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  course_id         uuid NOT NULL REFERENCES golf.courses (id),
  playing_route_id  uuid REFERENCES golf.playing_routes (id),
  starts_at         timestamptz NOT NULL,
  ends_at           timestamptz NOT NULL,
  reason            text NOT NULL CHECK (reason IN ('maintenance', 'tournament', 'private_event', 'weather_closure', 'management_hold')),
  notes             text,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  CHECK (ends_at > starts_at)
);
CREATE INDEX course_blocks_period ON golf.course_blocks USING gist (course_id, tstzrange(starts_at, ends_at)) WHERE status = 'active';
SELECT platform.enable_property_rls('golf.course_blocks');
SELECT platform.add_touch_trigger('golf.course_blocks');

-- FR-CRS-07 handicap index history (latest row is current).
CREATE TABLE golf.handicaps (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  customer_id     uuid NOT NULL REFERENCES crm.customers (id),
  handicap_index  numeric(4,1) NOT NULL CHECK (handicap_index BETWEEN -10 AND 54),
  source          text NOT NULL CHECK (source IN ('manual', 'import')),
  effective_at    timestamptz NOT NULL DEFAULT now(),
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid
);
CREATE INDEX handicaps_customer ON golf.handicaps (customer_id, effective_at DESC);
SELECT platform.enable_property_rls('golf.handicaps');

-- ── EP-03 Tee sheet ────────────────────────────────────────────────────────
-- Templates are versioned by effective date (FR-TEE-11).
CREATE TABLE golf.tee_sheet_templates (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  course_id          uuid NOT NULL REFERENCES golf.courses (id),
  code               text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  name               text NOT NULL,
  day_type_code      text NOT NULL,     -- commercial.day_types.code (WEEKDAY, WEEKEND …)
  session            text NOT NULL CHECK (session IN ('morning', 'afternoon', 'night')),
  start_time         text NOT NULL CHECK (start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  end_time           text NOT NULL CHECK (end_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  interval_minutes   int NOT NULL CHECK (interval_minutes BETWEEN 4 AND 30),
  start_tees         text NOT NULL DEFAULT '1' CHECK (start_tees IN ('1', '1,10')),
  flights_per_slot   int NOT NULL DEFAULT 1 CHECK (flights_per_slot BETWEEN 1 AND 4),
  min_players        int NOT NULL DEFAULT 1 CHECK (min_players BETWEEN 1 AND 6),
  max_players        int NOT NULL DEFAULT 4 CHECK (max_players BETWEEN 1 AND 6),
  playing_route_id   uuid REFERENCES golf.playing_routes (id),
  peak               boolean NOT NULL DEFAULT false,
  member_only        boolean NOT NULL DEFAULT false,
  lighting           boolean NOT NULL DEFAULT false,   -- Night Golf lights required
  effective_from     date NOT NULL,
  effective_to       date,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (course_id, code),
  CHECK (end_time >= start_time),
  CHECK (max_players >= min_players),
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
SELECT platform.enable_property_rls('golf.tee_sheet_templates');
SELECT platform.add_touch_trigger('golf.tee_sheet_templates');

-- Generated slots. A slot with a booking is never changed by regeneration.
CREATE TABLE golf.tee_times (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  course_id          uuid NOT NULL REFERENCES golf.courses (id),
  play_date          date NOT NULL,
  start_at           timestamptz NOT NULL,
  start_tee          int NOT NULL CHECK (start_tee IN (1, 10)),
  session            text NOT NULL CHECK (session IN ('morning', 'afternoon', 'night')),
  day_type_code      text NOT NULL,
  template_id        uuid REFERENCES golf.tee_sheet_templates (id),
  interval_minutes   int NOT NULL,
  flights            int NOT NULL,
  min_players        int NOT NULL,
  max_players        int NOT NULL,
  capacity           int NOT NULL,          -- flights × max players (seats)
  playing_route_id   uuid REFERENCES golf.playing_routes (id),
  peak               boolean NOT NULL DEFAULT false,
  member_only        boolean NOT NULL DEFAULT false,
  lighting           boolean NOT NULL DEFAULT false,
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'blocked', 'closed')),  -- closed = removed by regeneration
  block_id           uuid REFERENCES golf.course_blocks (id),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (course_id, start_at, start_tee)
);
CREATE INDEX tee_times_day ON golf.tee_times (property_id, play_date, course_id, start_at);
SELECT platform.enable_property_rls('golf.tee_times');
SELECT platform.add_touch_trigger('golf.tee_times');

-- ── EP-04 / EP-05 Booking, flight, player ─────────────────────────────────
CREATE TABLE golf.bookings (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  code                  text NOT NULL,          -- booking code (QR / check-in)
  booking_type          text NOT NULL CHECK (booking_type IN ('member', 'guest', 'non_member', 'group', 'corporate', 'walk_in')),
  channel               text NOT NULL CHECK (channel IN ('member_app', 'website', 'back_office', 'walk_in', 'import')),
  status                text NOT NULL CHECK (status IN ('draft', 'pending', 'confirmed', 'checked_in', 'completed', 'cancelled', 'no_show')),
  parent_booking_id     uuid REFERENCES golf.bookings (id),     -- group booking children
  course_id             uuid NOT NULL REFERENCES golf.courses (id),
  tee_time_id           uuid NOT NULL REFERENCES golf.tee_times (id),
  play_date             date NOT NULL,
  start_at              timestamptz NOT NULL,
  playing_route_id      uuid REFERENCES golf.playing_routes (id),
  player_count          int NOT NULL CHECK (player_count BETWEEN 1 AND 24),
  customer_id           uuid REFERENCES crm.customers (id),     -- person responsible (penanggung jawab)
  guest_id              uuid REFERENCES crm.guests (id),
  member_id             uuid REFERENCES membership.members (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  contact_name          text NOT NULL,
  contact_phone         text,
  contact_email         text,
  hold_expires_at       timestamptz,
  payment_mode          text CHECK (payment_mode IN ('prepaid', 'deposit', 'pay_at_venue', 'member_charge')),
  payment_due_at        timestamptz,
  deposit_amount        numeric(19,4),
  folio_id              uuid REFERENCES billing.folios (id),
  policy_versions       jsonb NOT NULL DEFAULT '{}'::jsonb,   -- FR-POL-09
  qr_token              text NOT NULL,
  manage_token_hash     text,                                 -- website manage link (FR-WEB-06)
  reschedule_count      int NOT NULL DEFAULT 0,
  cart_request          int,                                  -- requested golf carts
  caddy_request         text,                                 -- requested caddy (preference)
  notes                 text,
  confirmed_at          timestamptz,
  checked_in_at         timestamptz,
  completed_at          timestamptz,
  cancelled_at          timestamptz,
  cancel_reason         text,
  no_show_at            timestamptz,
  legacy_ref            text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, code)
);
CREATE UNIQUE INDEX bookings_qr ON golf.bookings (qr_token);
CREATE INDEX bookings_day ON golf.bookings (property_id, play_date, status);
CREATE INDEX bookings_customer ON golf.bookings (customer_id);
CREATE INDEX bookings_hold ON golf.bookings (hold_expires_at) WHERE status = 'draft';
CREATE INDEX bookings_due ON golf.bookings (payment_due_at) WHERE status = 'pending';
CREATE UNIQUE INDEX bookings_legacy ON golf.bookings (property_id, legacy_ref) WHERE legacy_ref IS NOT NULL;
SELECT platform.enable_property_rls('golf.bookings');
SELECT platform.add_touch_trigger('golf.bookings');

CREATE TABLE golf.flights (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  tee_time_id      uuid NOT NULL REFERENCES golf.tee_times (id),
  booking_id       uuid REFERENCES golf.bookings (id),
  course_id        uuid NOT NULL REFERENCES golf.courses (id),
  play_date        date NOT NULL,
  flight_no        int NOT NULL,
  status           text NOT NULL CHECK (status IN ('confirmed', 'checked_in', 'ready', 'on_hold', 'in_play', 'completed', 'cancelled')),
  ready_at         timestamptz,
  tee_off_at       timestamptz,   -- Actual Tee-Off = Round Start
  round_finish_at  timestamptz,
  holes_played     int,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tee_time_id, flight_no)
);
CREATE INDEX flights_day ON golf.flights (property_id, play_date, status);
SELECT platform.enable_property_rls('golf.flights');
SELECT platform.add_touch_trigger('golf.flights');

CREATE TABLE golf.booking_players (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  booking_id           uuid NOT NULL REFERENCES golf.bookings (id),
  flight_id            uuid NOT NULL REFERENCES golf.flights (id),
  seq                  int NOT NULL,
  player_type          text NOT NULL CHECK (player_type IN ('member', 'guest_of_member', 'reciprocal', 'non_member')),
  segment              text NOT NULL,            -- pricing segment used (member, guest, senior …)
  customer_id          uuid REFERENCES crm.customers (id),
  guest_id             uuid REFERENCES crm.guests (id),
  member_id            uuid REFERENCES membership.members (id),
  membership_id        uuid REFERENCES membership.memberships (id),
  host_player_id       uuid REFERENCES golf.booking_players (id),   -- guest of member → member
  name                 text NOT NULL,
  phone                text,
  tba                  boolean NOT NULL DEFAULT false,              -- FR-FLT-09
  reciprocal_club      text,
  reciprocal_letter_file_id uuid REFERENCES platform.files (id),
  reciprocal_verified  boolean NOT NULL DEFAULT false,
  eligibility          jsonb NOT NULL DEFAULT '{}'::jsonb,   -- FR-FLT-04
  entitlement          jsonb NOT NULL DEFAULT '{}'::jsonb,   -- FR-FLT-06
  allocation_id        uuid,                                  -- reservation.allocations
  pricing_snapshot_id  uuid REFERENCES commercial.pricing_snapshots (id),
  folio_line_id        uuid REFERENCES billing.folio_lines (id),
  price_total          numeric(19,4),
  handicap_index       numeric(4,1),
  status               text NOT NULL CHECK (status IN ('booked', 'checked_in', 'no_show', 'cancelled', 'removed')),
  checked_in_at        timestamptz,
  check_in_method      text CHECK (check_in_method IS NULL OR check_in_method IN ('member_card', 'booking_qr', 'booking_code', 'name', 'offline')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid
);
CREATE INDEX booking_players_booking ON golf.booking_players (booking_id);
CREATE INDEX booking_players_flight ON golf.booking_players (flight_id);
CREATE INDEX booking_players_customer ON golf.booking_players (customer_id);
SELECT platform.enable_property_rls('golf.booking_players');
SELECT platform.add_touch_trigger('golf.booking_players');

-- FR-BKG-12 booking history & modification.
CREATE TABLE golf.booking_history (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  booking_id   uuid NOT NULL REFERENCES golf.bookings (id),
  event        text NOT NULL,
  from_value   jsonb,
  to_value     jsonb,
  reason       text,
  actor_id     uuid,
  actor_name   text,
  occurred_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX booking_history_booking ON golf.booking_history (booking_id, occurred_at);
SELECT platform.enable_property_rls('golf.booking_history');

-- FR-BKG-11 rain checks.
CREATE TABLE golf.rain_checks (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  booking_id           uuid NOT NULL REFERENCES golf.bookings (id),
  booking_player_id    uuid NOT NULL REFERENCES golf.booking_players (id),
  customer_id          uuid REFERENCES crm.customers (id),
  holes_played         int NOT NULL CHECK (holes_played >= 0),
  holes_total          int NOT NULL,
  credit_percent       numeric(9,4) NOT NULL,
  credit_amount        numeric(19,4) NOT NULL,
  currency             char(3) NOT NULL,
  expires_on           date NOT NULL,
  status               text NOT NULL CHECK (status IN ('issued', 'redeemed', 'expired', 'cancelled')),
  redeemed_booking_id  uuid REFERENCES golf.bookings (id),
  redeemed_at          timestamptz,
  policy_version       int,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (property_id, number),
  UNIQUE (booking_player_id)
);
SELECT platform.enable_property_rls('golf.rain_checks');

-- ── EP-09 Caddy ────────────────────────────────────────────────────────────
CREATE TABLE golf.caddies (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  code                text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),   -- caddy number
  name                text NOT NULL,
  gender              text CHECK (gender IS NULL OR gender IN ('male', 'female')),
  phone               text,
  photo_file_id       uuid REFERENCES platform.files (id),
  partnership_status  text NOT NULL DEFAULT 'partner' CHECK (partnership_status IN ('partner', 'trainee', 'employee')),
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  legacy_ref          text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  archived_at         timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.caddies');
SELECT platform.add_touch_trigger('golf.caddies');

-- FR-CAD-02/03 daily attendance and queue / rotation.
CREATE TABLE golf.caddy_attendance (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  caddy_id     uuid NOT NULL REFERENCES golf.caddies (id),
  work_date    date NOT NULL,
  status       text NOT NULL CHECK (status IN ('present', 'absent', 'leave')),
  queue_no     numeric(12,4),
  arrived_at   timestamptz,
  notes        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  UNIQUE (caddy_id, work_date)
);
SELECT platform.enable_property_rls('golf.caddy_attendance');
SELECT platform.add_touch_trigger('golf.caddy_attendance');

CREATE TABLE golf.caddy_assignments (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  caddy_id           uuid NOT NULL REFERENCES golf.caddies (id),
  flight_id          uuid NOT NULL REFERENCES golf.flights (id),
  player_ids         uuid[] NOT NULL,
  play_date          date NOT NULL,
  period             tstzrange NOT NULL,
  status             text NOT NULL CHECK (status IN ('assigned', 'in_play', 'completed', 'cancelled', 'replaced')),
  fee_amount         numeric(19,4) NOT NULL DEFAULT 0,      -- caddy fee held for the caddy (liability)
  currency           char(3) NOT NULL DEFAULT 'IDR',
  requested          boolean NOT NULL DEFAULT false,
  replaced_by_id     uuid REFERENCES golf.caddy_assignments (id),
  replace_reason     text,
  assigned_at        timestamptz NOT NULL DEFAULT now(),
  assigned_by        uuid,
  started_at         timestamptz,
  finished_at        timestamptz,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT caddy_assignments_no_overlap EXCLUDE USING gist (caddy_id WITH =, period WITH &&)
    WHERE (status IN ('assigned', 'in_play'))
);
CREATE INDEX caddy_assignments_flight ON golf.caddy_assignments (flight_id);
CREATE INDEX caddy_assignments_caddy ON golf.caddy_assignments (caddy_id, play_date DESC);
SELECT platform.enable_property_rls('golf.caddy_assignments');
SELECT platform.add_touch_trigger('golf.caddy_assignments');

-- FR-CAD-08 tips (cash / non-cash) per caddy for the P2 settlement.
CREATE TABLE golf.caddy_tips (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  caddy_id           uuid NOT NULL REFERENCES golf.caddies (id),
  assignment_id      uuid REFERENCES golf.caddy_assignments (id),
  booking_player_id  uuid REFERENCES golf.booking_players (id),
  amount             numeric(19,4) NOT NULL CHECK (amount > 0),
  currency           char(3) NOT NULL,
  method             text NOT NULL CHECK (method IN ('cash', 'non_cash')),
  folio_line_id      uuid REFERENCES billing.folio_lines (id),
  tip_date           date NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
CREATE INDEX caddy_tips_caddy ON golf.caddy_tips (caddy_id, tip_date);
SELECT platform.enable_property_rls('golf.caddy_tips');

-- ── EP-10 Golf cart ────────────────────────────────────────────────────────
CREATE TABLE golf.golf_carts (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  code                  text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),   -- cart number
  name                  text NOT NULL,
  cart_type             text NOT NULL DEFAULT 'electric' CHECK (cart_type IN ('electric', 'gasoline', 'other')),
  capacity              int NOT NULL DEFAULT 2 CHECK (capacity BETWEEN 1 AND 6),
  readiness             text NOT NULL DEFAULT 'ready' CHECK (readiness IN ('ready', 'not_ready', 'in_use', 'charging', 'maintenance', 'out_of_service')),
  readiness_reason      text,
  readiness_changed_at  timestamptz NOT NULL DEFAULT now(),
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  legacy_ref            text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  archived_at           timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.golf_carts');
SELECT platform.add_touch_trigger('golf.golf_carts');

CREATE TABLE golf.golf_cart_assignments (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  golf_cart_id   uuid NOT NULL REFERENCES golf.golf_carts (id),
  flight_id      uuid NOT NULL REFERENCES golf.flights (id),
  player_ids     uuid[] NOT NULL DEFAULT '{}',
  play_date      date NOT NULL,
  period         tstzrange NOT NULL,
  status         text NOT NULL CHECK (status IN ('assigned', 'in_use', 'returned', 'cancelled')),
  extra          boolean NOT NULL DEFAULT false,      -- beyond the buggy sharing rule (surcharge)
  fee_amount     numeric(19,4) NOT NULL DEFAULT 0,
  surcharge_line_id uuid REFERENCES billing.folio_lines (id),
  assigned_at    timestamptz NOT NULL DEFAULT now(),
  assigned_by    uuid,
  out_at         timestamptz,
  returned_at    timestamptz,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT golf_cart_assignments_no_overlap EXCLUDE USING gist (golf_cart_id WITH =, period WITH &&)
    WHERE (status IN ('assigned', 'in_use'))
);
CREATE INDEX golf_cart_assignments_flight ON golf.golf_cart_assignments (flight_id);
SELECT platform.enable_property_rls('golf.golf_cart_assignments');
SELECT platform.add_touch_trigger('golf.golf_cart_assignments');

-- Readiness changes (Maintenance / Out of Service require a reason, FR-CRT-08).
CREATE TABLE golf.golf_cart_events (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  golf_cart_id  uuid NOT NULL REFERENCES golf.golf_carts (id),
  from_state    text,
  to_state      text NOT NULL,
  reason        text,
  occurred_at   timestamptz NOT NULL DEFAULT now(),
  actor_id      uuid
);
SELECT platform.enable_property_rls('golf.golf_cart_events');

-- ── EP-11 Check-in, bag drop, locker, starter ─────────────────────────────
CREATE TABLE golf.lockers (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),
  name         text NOT NULL,
  area         text NOT NULL CHECK (area IN ('male', 'female')),
  zone         text,
  locker_status text NOT NULL DEFAULT 'available' CHECK (locker_status IN ('available', 'occupied', 'maintenance')),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  legacy_ref   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.lockers');
SELECT platform.add_touch_trigger('golf.lockers');

CREATE TABLE golf.locker_assignments (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  locker_id          uuid NOT NULL REFERENCES golf.lockers (id),
  customer_id        uuid REFERENCES crm.customers (id),
  booking_player_id  uuid REFERENCES golf.booking_players (id),
  holder_name        text NOT NULL,
  assignment_type    text NOT NULL CHECK (assignment_type IN ('daily', 'periodic')),
  period             tstzrange NOT NULL,
  status             text NOT NULL CHECK (status IN ('active', 'released')),
  fee_amount         numeric(19,4),
  folio_line_id      uuid REFERENCES billing.folio_lines (id),
  released_at        timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT locker_assignments_no_overlap EXCLUDE USING gist (locker_id WITH =, period WITH &&) WHERE (status = 'active')
);
SELECT platform.enable_property_rls('golf.locker_assignments');
SELECT platform.add_touch_trigger('golf.locker_assignments');

CREATE TABLE golf.bag_drops (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  booking_player_id  uuid NOT NULL REFERENCES golf.booking_players (id),
  flight_id          uuid NOT NULL REFERENCES golf.flights (id),
  play_date          date NOT NULL,
  tag_number         text NOT NULL,
  bag_count          int NOT NULL DEFAULT 1 CHECK (bag_count BETWEEN 1 AND 6),
  dropped_at         timestamptz NOT NULL DEFAULT now(),
  collected_at       timestamptz,
  status             text NOT NULL CHECK (status IN ('dropped', 'loaded', 'collected')),
  notes              text,
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX bag_drops_tag ON golf.bag_drops (property_id, play_date, tag_number);
SELECT platform.enable_property_rls('golf.bag_drops');
SELECT platform.add_touch_trigger('golf.bag_drops');

-- FR-CHK-04 long-term bag storage per customer.
CREATE TABLE golf.bag_storage (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  customer_id   uuid NOT NULL REFERENCES crm.customers (id),
  rack_number   text NOT NULL,
  starts_on     date NOT NULL,
  ends_on       date,
  fee_amount    numeric(19,4),
  folio_id      uuid REFERENCES billing.folios (id),
  status        text NOT NULL CHECK (status IN ('active', 'ended')),
  notes         text,
  legacy_ref    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid
);
CREATE UNIQUE INDEX bag_storage_rack ON golf.bag_storage (property_id, rack_number) WHERE status = 'active';
SELECT platform.enable_property_rls('golf.bag_storage');
SELECT platform.add_touch_trigger('golf.bag_storage');

-- FR-CHK-07/08 starter queue: FIFO by scheduled tee time and Ready Time;
-- a held flight keeps its position value (Preserved Queue Position).
CREATE TABLE golf.starter_queue (
  flight_id      uuid PRIMARY KEY REFERENCES golf.flights (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  course_id      uuid NOT NULL REFERENCES golf.courses (id),
  play_date      date NOT NULL,
  scheduled_at   timestamptz NOT NULL,
  ready_at       timestamptz NOT NULL,
  position       numeric(24,6) NOT NULL,
  status         text NOT NULL CHECK (status IN ('waiting', 'on_hold', 'dispatched', 'removed')),
  hold_reason    text,
  held_at        timestamptz,
  released_at    timestamptz,
  skips          int NOT NULL DEFAULT 0,
  called_at      timestamptz,
  dispatched_at  timestamptz,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX starter_queue_day ON golf.starter_queue (property_id, course_id, play_date, status, position);
SELECT platform.enable_property_rls('golf.starter_queue');
SELECT platform.add_touch_trigger('golf.starter_queue');

-- FR-CHK-11 marshal & course status, weather status, night lights.
CREATE TABLE golf.course_status (
  course_id      uuid PRIMARY KEY REFERENCES golf.courses (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  course_state   text NOT NULL DEFAULT 'open' CHECK (course_state IN ('open', 'closed')),
  weather        text NOT NULL DEFAULT 'normal' CHECK (weather IN ('normal', 'rain', 'lightning_warning', 'rain_stop', 'heat_warning')),
  lighting       text NOT NULL DEFAULT 'off' CHECK (lighting IN ('on', 'off')),
  closed_holes   text NOT NULL DEFAULT '',          -- e.g. 7,12
  notes          text,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
SELECT platform.enable_property_rls('golf.course_status');

CREATE TABLE golf.course_status_log (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  course_id    uuid NOT NULL REFERENCES golf.courses (id),
  before       jsonb,
  after        jsonb NOT NULL,
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  actor_id     uuid
);
SELECT platform.enable_property_rls('golf.course_status_log');

CREATE TABLE golf.sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL,
  day          date NOT NULL,
  last_value   int NOT NULL,
  PRIMARY KEY (property_id, prefix, day)
);
SELECT platform.enable_property_rls('golf.sequences');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.sequences, golf.course_status_log, golf.course_status, golf.starter_queue, golf.bag_storage, golf.bag_drops,
  golf.locker_assignments, golf.lockers, golf.golf_cart_events, golf.golf_cart_assignments, golf.golf_carts, golf.caddy_tips,
  golf.caddy_assignments, golf.caddy_attendance, golf.caddies, golf.rain_checks, golf.booking_history, golf.booking_players,
  golf.flights, golf.bookings, golf.tee_times, golf.tee_sheet_templates, golf.handicaps, golf.course_blocks, golf.course_assets;
ALTER TABLE commercial.pricing_rules DROP CONSTRAINT pricing_rules_route_fk;
DROP TABLE golf.playing_routes, golf.holes, golf.tee_sets, golf.course_sections;
ALTER TABLE golf.courses DROP COLUMN length_meters, DROP COLUMN par, DROP COLUMN description, DROP COLUMN guide;
