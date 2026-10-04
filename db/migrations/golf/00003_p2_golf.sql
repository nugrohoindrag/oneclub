-- PRD P2 Complete Golf Experience on P1's Golf Core (golf/00002): advanced
-- caddy lifecycle (EP-05), caddy tablet & round tracking (EP-06, EP-09),
-- golf cart inspections & maintenance (EP-07), digital scorecard & WHS
-- handicap (EP-08), Hole-in-One (EP-10), Hall of Fame (EP-11), Driving Range
-- (EP-12) and Reciprocal Club (EP-13). Expand-only: P1 rows keep their
-- meaning; rounds, players and assignments are P1's flights, booking
-- players, caddy and golf cart assignments.

-- +goose Up
-- ── EP-05 caddy levels, attendance, tablet login ──────────────────────────
CREATE TABLE golf.caddy_levels (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  code          text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),
  name          text NOT NULL,
  rank          int NOT NULL DEFAULT 1,
  fee_amount    numeric(19,4) NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
  min_rounds    int NOT NULL DEFAULT 0,
  min_rating    numeric(3,2) NOT NULL DEFAULT 0,
  max_incidents int,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.caddy_levels');
SELECT platform.add_touch_trigger('golf.caddy_levels');

ALTER TABLE golf.caddies
  ADD COLUMN level_id   uuid REFERENCES golf.caddy_levels (id),
  ADD COLUMN user_id    uuid REFERENCES platform.users (id),   -- Caddy Tablet login
  ADD COLUMN joined_on  date;
CREATE UNIQUE INDEX caddies_user ON golf.caddies (user_id) WHERE user_id IS NOT NULL;

CREATE TABLE golf.caddy_level_history (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  caddy_id             uuid NOT NULL REFERENCES golf.caddies (id),
  from_level_id        uuid REFERENCES golf.caddy_levels (id),
  to_level_id          uuid NOT NULL REFERENCES golf.caddy_levels (id),
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
  reason               text NOT NULL,
  indicators           jsonb NOT NULL DEFAULT '{}'::jsonb,
  approval_request_id  uuid,
  effective_at         timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid
);
SELECT platform.enable_property_rls('golf.caddy_level_history');

-- Clock-out and shift on P1's daily attendance (FR-CDL-03).
ALTER TABLE golf.caddy_attendance
  ADD COLUMN shift        text NOT NULL DEFAULT 'full_day' CHECK (shift IN ('morning', 'afternoon', 'full_day')),
  ADD COLUMN departed_at  timestamptz;

-- The caddy accepts the assignment on the tablet (FR-TAB-01).
ALTER TABLE golf.caddy_assignments
  ADD COLUMN accepted_at  timestamptz,
  ADD COLUMN device_id    text;

-- ── EP-07 golf cart inspections, maintenance, service hours, GPS ─────────
ALTER TABLE golf.golf_carts DROP CONSTRAINT golf_carts_readiness_check;
ALTER TABLE golf.golf_carts ADD CONSTRAINT golf_carts_readiness_check
  CHECK (readiness IN ('ready', 'not_ready', 'in_use', 'charging', 'maintenance', 'out_of_service', 'under_inspection'));
ALTER TABLE golf.golf_carts
  ADD COLUMN service_threshold_hours  numeric(10,2),
  ADD COLUMN hours_since_service      numeric(10,2) NOT NULL DEFAULT 0,
  ADD COLUMN service_alerted_at       timestamptz,
  ADD COLUMN battery_percent          int CHECK (battery_percent IS NULL OR battery_percent BETWEEN 0 AND 100),
  ADD COLUMN last_lat                 double precision,
  ADD COLUMN last_lng                 double precision,
  ADD COLUMN position_at              timestamptz,
  ADD COLUMN gps_device_id            text;

CREATE TABLE golf.cart_checklists (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),
  name             text NOT NULL,
  cart_type        text NOT NULL DEFAULT 'electric' CHECK (cart_type IN ('electric', 'gasoline', 'other')),
  inspection_kind  text NOT NULL CHECK (inspection_kind IN ('pre_op', 'post_op', 'release')),
  items            text[] NOT NULL,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.cart_checklists');
SELECT platform.add_touch_trigger('golf.cart_checklists');

CREATE TABLE golf.cart_inspections (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  golf_cart_id     uuid NOT NULL REFERENCES golf.golf_carts (id),
  inspection_kind  text NOT NULL CHECK (inspection_kind IN ('pre_op', 'post_op', 'release')),
  checklist_id     uuid REFERENCES golf.cart_checklists (id),
  results          jsonb NOT NULL,          -- [{item, pass, note}]
  photos           text[] NOT NULL DEFAULT '{}',
  passed           boolean NOT NULL,
  hour_meter       numeric(10,2),
  battery_percent  int,
  readiness_after  text NOT NULL,
  maintenance_id   uuid,
  inspected_at     timestamptz NOT NULL DEFAULT now(),
  inspected_by     uuid
);
CREATE INDEX cart_inspections_cart ON golf.cart_inspections (golf_cart_id, inspected_at DESC);
SELECT platform.enable_property_rls('golf.cart_inspections');

CREATE TABLE golf.cart_maintenance (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  golf_cart_id           uuid NOT NULL REFERENCES golf.golf_carts (id),
  number                 text NOT NULL,
  category               text NOT NULL DEFAULT 'repair' CHECK (category IN ('repair', 'scheduled_service', 'battery', 'tyre', 'body', 'other')),
  description            text NOT NULL,
  cost                   numeric(19,4) NOT NULL DEFAULT 0,
  status                 text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  opened_at              timestamptz NOT NULL DEFAULT now(),
  opened_by              uuid,
  closed_at              timestamptz,
  closed_by              uuid,
  release_inspection_id  uuid REFERENCES golf.cart_inspections (id),
  notes                  text,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('golf.cart_maintenance');

-- ── EP-09 pace of play: hole targets and progress ─────────────────────────
ALTER TABLE golf.holes ADD COLUMN target_minutes int NOT NULL DEFAULT 15 CHECK (target_minutes BETWEEN 5 AND 40);
ALTER TABLE golf.playing_routes ADD COLUMN tolerance_minutes int NOT NULL DEFAULT 10 CHECK (tolerance_minutes >= 0);
ALTER TABLE golf.flights
  ADD COLUMN current_seq     int NOT NULL DEFAULT 0,
  ADD COLUMN last_hole_at    timestamptz,
  ADD COLUMN pace_status     text NOT NULL DEFAULT 'on_pace' CHECK (pace_status IN ('on_pace', 'slow', 'fast')),
  ADD COLUMN behind_minutes  int NOT NULL DEFAULT 0,
  ADD COLUMN tablet_device   text;

CREATE TABLE golf.hole_progress (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  flight_id    uuid NOT NULL REFERENCES golf.flights (id),
  seq          int NOT NULL,
  hole_id      uuid NOT NULL REFERENCES golf.holes (id),
  started_at   timestamptz NOT NULL,
  finished_at  timestamptz,
  device_id    text,
  source       text NOT NULL DEFAULT 'tablet' CHECK (source IN ('tablet', 'staff', 'gps')),
  UNIQUE (flight_id, seq)
);
SELECT platform.enable_property_rls('golf.hole_progress');

-- ── incidents, ratings, favourites, settlement (EP-05 / EP-07) ────────────
CREATE TABLE golf.incidents (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  subject_type         text NOT NULL CHECK (subject_type IN ('caddy', 'golf_cart')),
  caddy_id             uuid REFERENCES golf.caddies (id),
  golf_cart_id         uuid REFERENCES golf.golf_carts (id),
  flight_id            uuid REFERENCES golf.flights (id),
  booking_player_id    uuid REFERENCES golf.booking_players (id),
  customer_id          uuid REFERENCES crm.customers (id),
  category             text NOT NULL,
  severity             text NOT NULL DEFAULT 'low' CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  description          text NOT NULL,
  action_taken         text,
  attachments          text[] NOT NULL DEFAULT '{}',
  damage_amount        numeric(19,4),
  damage_status        text CHECK (damage_status IN ('pending', 'approved', 'rejected', 'charged')),
  approval_request_id  uuid,
  folio_line_id        uuid,
  status               text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  occurred_at          timestamptz NOT NULL DEFAULT now(),
  reported_by          uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('golf.incidents');
SELECT platform.add_touch_trigger('golf.incidents');

CREATE TABLE golf.caddy_ratings (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  assignment_id  uuid NOT NULL REFERENCES golf.caddy_assignments (id),
  caddy_id       uuid NOT NULL REFERENCES golf.caddies (id),
  customer_id    uuid REFERENCES crm.customers (id),
  rating         int NOT NULL CHECK (rating BETWEEN 1 AND 5),
  comment        text,
  channel        text NOT NULL DEFAULT 'member_portal',
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (assignment_id, customer_id)
);
SELECT platform.enable_property_rls('golf.caddy_ratings');

CREATE TABLE golf.caddy_favorites (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  caddy_id     uuid NOT NULL REFERENCES golf.caddies (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (caddy_id, customer_id)
);
SELECT platform.enable_property_rls('golf.caddy_favorites');

CREATE TABLE golf.caddy_settlements (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  caddy_id             uuid NOT NULL REFERENCES golf.caddies (id),
  period_start         date NOT NULL,
  period_end           date NOT NULL,
  rounds               int NOT NULL DEFAULT 0,
  caddy_fee            numeric(19,4) NOT NULL DEFAULT 0,
  tips                 numeric(19,4) NOT NULL DEFAULT 0,
  deductions           numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  lines                jsonb NOT NULL DEFAULT '[]'::jsonb,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'paid')),
  approval_request_id  uuid,
  payout_id            uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX caddy_settlements_period ON golf.caddy_settlements (caddy_id, period_start, period_end) WHERE status <> 'rejected';
SELECT platform.enable_property_rls('golf.caddy_settlements');
SELECT platform.add_touch_trigger('golf.caddy_settlements');
-- tips and caddy fee lines settled once
ALTER TABLE golf.caddy_tips ADD COLUMN settlement_id uuid REFERENCES golf.caddy_settlements (id);
ALTER TABLE golf.caddy_assignments ADD COLUMN settlement_id uuid REFERENCES golf.caddy_settlements (id);

-- ── EP-08 digital scorecard & handicap ────────────────────────────────────
CREATE TABLE golf.scorecards (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  flight_id          uuid REFERENCES golf.flights (id),
  booking_player_id  uuid REFERENCES golf.booking_players (id),
  customer_id        uuid REFERENCES crm.customers (id),
  player_name        text NOT NULL,
  playing_route_id   uuid NOT NULL REFERENCES golf.playing_routes (id),
  tee_set_id         uuid REFERENCES golf.tee_sets (id),
  played_on          date NOT NULL,
  holes              int NOT NULL,
  par                int NOT NULL,
  course_rating      numeric(4,1),
  slope              int,
  gross              int,
  putts              int,
  differential       numeric(5,1),
  status             text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'finalized')),
  attested_by        text,
  flags              text[] NOT NULL DEFAULT '{}',
  finalized_at       timestamptz,
  finalized_by       uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  UNIQUE (booking_player_id)
);
CREATE INDEX scorecards_customer ON golf.scorecards (customer_id, played_on);
SELECT platform.enable_property_rls('golf.scorecards');
SELECT platform.add_touch_trigger('golf.scorecards');

CREATE TABLE golf.scorecard_holes (
  scorecard_id  uuid NOT NULL REFERENCES golf.scorecards (id),
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  seq           int NOT NULL,
  hole_id       uuid NOT NULL REFERENCES golf.holes (id),
  hole_number   int NOT NULL,
  section_code  text NOT NULL,
  par           int NOT NULL,
  stroke_index  int,
  strokes       int CHECK (strokes IS NULL OR strokes BETWEEN 1 AND 20),
  putts         int CHECK (putts IS NULL OR putts BETWEEN 0 AND 10),
  penalties     int CHECK (penalties IS NULL OR penalties BETWEEN 0 AND 10),
  term          text,
  source        text,
  entered_at    timestamptz,
  entered_by    uuid,
  PRIMARY KEY (scorecard_id, seq)
);
SELECT platform.enable_property_rls('golf.scorecard_holes');

CREATE TABLE golf.score_audit (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  scorecard_id  uuid NOT NULL REFERENCES golf.scorecards (id),
  seq           int NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('entry', 'correction')),
  before        jsonb,
  after         jsonb NOT NULL,
  reason        text,
  source        text,
  device_id     text,
  client_at     timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);
CREATE INDEX score_audit_card ON golf.score_audit (scorecard_id, created_at);
SELECT platform.enable_property_rls('golf.score_audit');

-- P1 handicap history: the WHS index computed from finalised cards and the
-- official (federation) index join manual / imported entries.
ALTER TABLE golf.handicaps DROP CONSTRAINT handicaps_source_check;
ALTER TABLE golf.handicaps ADD CONSTRAINT handicaps_source_check CHECK (source IN ('manual', 'import', 'whs', 'federation'));
ALTER TABLE golf.handicaps ADD COLUMN rounds_counted int;

-- ── EP-10 Hole-in-One, EP-11 Hall of Fame ─────────────────────────────────
CREATE TABLE golf.hio_records (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  scorecard_id         uuid REFERENCES golf.scorecards (id),
  flight_id            uuid REFERENCES golf.flights (id),
  booking_player_id    uuid REFERENCES golf.booking_players (id),
  customer_id          uuid REFERENCES crm.customers (id),
  player_name          text NOT NULL,
  hole_id              uuid NOT NULL REFERENCES golf.holes (id),
  tee_set_id           uuid REFERENCES golf.tee_sets (id),
  caddy_id             uuid REFERENCES golf.caddies (id),
  achieved_on          date NOT NULL,
  witnesses            jsonb NOT NULL DEFAULT '[]'::jsonb,
  attachments          text[] NOT NULL DEFAULT '{}',
  insured              boolean NOT NULL DEFAULT false,
  policy_ref           text,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_verification', 'verified', 'claimed', 'completed', 'rejected')),
  approval_request_id  uuid,
  verified_at          timestamptz,
  claim_submitted_on   date,
  claim_provider_ref   text,
  claim_documents      text[] NOT NULL DEFAULT '{}',
  claim_status         text CHECK (claim_status IN ('submitted', 'in_review', 'paid', 'rejected')),
  claim_paid_amount    numeric(19,4),
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX hio_records_scorecard_hole ON golf.hio_records (scorecard_id, hole_id) WHERE scorecard_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.hio_records');
SELECT platform.add_touch_trigger('golf.hio_records');

CREATE TABLE golf.hall_of_fame (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  category            text NOT NULL CHECK (category IN ('hole_in_one', 'club_champion', 'course_record', 'albatross', 'eagle', 'tournament_champion', 'club_history')),
  title               text NOT NULL,
  year                int,
  division            text CHECK (division IN ('men', 'ladies', 'senior', 'junior', 'open')),
  customer_id         uuid REFERENCES crm.customers (id),
  player_name         text,
  tee_set_id          uuid REFERENCES golf.tee_sets (id),
  hole_id             uuid REFERENCES golf.holes (id),
  score               int,
  achieved_on         date,
  description         text,
  photo_url           text,
  source_type         text NOT NULL DEFAULT 'manual',
  source_id           uuid,
  needs_verification  boolean NOT NULL DEFAULT false,
  consent             text NOT NULL DEFAULT 'pending' CHECK (consent IN ('pending', 'granted', 'withdrawn', 'not_required')),
  consent_at          timestamptz,
  published           boolean NOT NULL DEFAULT false,
  published_at        timestamptz,
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  archived_at         timestamptz
);
CREATE UNIQUE INDEX hall_of_fame_source ON golf.hall_of_fame (source_type, source_id, category) WHERE source_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.hall_of_fame');
SELECT platform.add_touch_trigger('golf.hall_of_fame');

-- ── EP-12 Driving Range ───────────────────────────────────────────────────
CREATE TABLE golf.range_bays (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),
  name         text NOT NULL,
  area         text NOT NULL DEFAULT 'outdoor' CHECK (area IN ('indoor', 'outdoor')),
  bay_type     text NOT NULL DEFAULT 'standard',
  tier         int NOT NULL DEFAULT 1,
  readiness    text NOT NULL DEFAULT 'available' CHECK (readiness IN ('available', 'occupied', 'maintenance')),
  resource_id  uuid,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.range_bays');
SELECT platform.add_touch_trigger('golf.range_bays');

CREATE TABLE golf.range_sessions (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  bay_id          uuid REFERENCES golf.range_bays (id),
  area            text NOT NULL DEFAULT 'outdoor',
  customer_id     uuid REFERENCES crm.customers (id),
  guest_name      text,
  status          text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting', 'active', 'finished', 'cancelled')),
  queued_at       timestamptz NOT NULL DEFAULT now(),
  started_at      timestamptz,
  ended_at        timestamptz,
  reservation_id  uuid,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('golf.range_sessions');
SELECT platform.add_touch_trigger('golf.range_sessions');

CREATE TABLE golf.range_buckets (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  session_id       uuid REFERENCES golf.range_sessions (id),
  customer_id      uuid REFERENCES crm.customers (id),
  balls            int NOT NULL CHECK (balls > 0),
  source           text NOT NULL CHECK (source IN ('sale', 'prepaid', 'complimentary')),
  order_id         uuid,
  voucher_id       uuid,
  amount           numeric(19,4) NOT NULL DEFAULT 0,
  reason           text,
  dispenser_code   text NOT NULL,
  dispense_mode    text NOT NULL DEFAULT 'manual' CHECK (dispense_mode IN ('bridge', 'manual')),
  dispensed_at     timestamptz,
  idempotency_key  text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid
);
CREATE UNIQUE INDEX range_buckets_idem ON golf.range_buckets (property_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
SELECT platform.enable_property_rls('golf.range_buckets');

-- ── EP-13 Reciprocal Club (on P1's reciprocal player fields) ──────────────
CREATE TABLE golf.reciprocal_clubs (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,19}$'),
  name             text NOT NULL,
  country          text NOT NULL,
  city             text,
  contact_name     text,
  contact_email    text,
  contact_phone    text,
  agreement_from   date,
  agreement_to     date,
  visit_quota      int,
  quota_period     text NOT NULL DEFAULT 'year' CHECK (quota_period IN ('month', 'year')),
  settlement_mode  text NOT NULL DEFAULT 'none' CHECK (settlement_mode IN ('none', 'periodic')),
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.reciprocal_clubs');
SELECT platform.add_touch_trigger('golf.reciprocal_clubs');

CREATE TABLE golf.introduction_letters (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  club_id              uuid NOT NULL REFERENCES golf.reciprocal_clubs (id),
  customer_id          uuid NOT NULL REFERENCES crm.customers (id),
  play_from            date NOT NULL,
  play_to              date NOT NULL,
  players              int NOT NULL DEFAULT 1,
  notes                text,
  status               text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'approved', 'rejected', 'issued', 'cancelled')),
  approval_request_id  uuid,
  file_id              uuid,
  issued_at            timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('golf.introduction_letters');

CREATE TABLE golf.reciprocal_visits (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  direction          text NOT NULL CHECK (direction IN ('inbound', 'outbound')),
  club_id            uuid NOT NULL REFERENCES golf.reciprocal_clubs (id),
  customer_id        uuid REFERENCES crm.customers (id),
  visitor_name       text NOT NULL,
  home_card_no       text,
  card_valid_until   date,
  letter_ref         text,
  letter_id          uuid REFERENCES golf.introduction_letters (id),
  documents          text[] NOT NULL DEFAULT '{}',
  visit_date         date NOT NULL,
  booking_player_id  uuid REFERENCES golf.booking_players (id),
  charge_amount      numeric(19,4) NOT NULL DEFAULT 0,
  verified           boolean NOT NULL DEFAULT false,
  verified_by        uuid,
  settlement_status  text NOT NULL DEFAULT 'not_applicable' CHECK (settlement_status IN ('not_applicable', 'open', 'invoiced', 'settled')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('golf.reciprocal_visits');
SELECT platform.add_touch_trigger('golf.reciprocal_visits');
ALTER TABLE golf.booking_players ADD COLUMN reciprocal_visit_id uuid REFERENCES golf.reciprocal_visits (id);

SELECT platform.grant_app('golf');

-- +goose Down
ALTER TABLE golf.booking_players DROP COLUMN reciprocal_visit_id;
DROP TABLE golf.reciprocal_visits, golf.introduction_letters, golf.reciprocal_clubs, golf.range_buckets, golf.range_sessions, golf.range_bays,
  golf.hall_of_fame, golf.hio_records, golf.score_audit, golf.scorecard_holes, golf.scorecards;
ALTER TABLE golf.handicaps DROP COLUMN rounds_counted;
ALTER TABLE golf.handicaps DROP CONSTRAINT handicaps_source_check;
ALTER TABLE golf.handicaps ADD CONSTRAINT handicaps_source_check CHECK (source IN ('manual', 'import'));
ALTER TABLE golf.caddy_assignments DROP COLUMN settlement_id;
ALTER TABLE golf.caddy_tips DROP COLUMN settlement_id;
DROP TABLE golf.caddy_settlements, golf.caddy_favorites, golf.caddy_ratings, golf.incidents, golf.hole_progress;
ALTER TABLE golf.flights DROP COLUMN tablet_device, DROP COLUMN behind_minutes, DROP COLUMN pace_status, DROP COLUMN last_hole_at, DROP COLUMN current_seq;
ALTER TABLE golf.playing_routes DROP COLUMN tolerance_minutes;
ALTER TABLE golf.holes DROP COLUMN target_minutes;
DROP TABLE golf.cart_maintenance, golf.cart_inspections, golf.cart_checklists;
ALTER TABLE golf.golf_carts DROP COLUMN gps_device_id, DROP COLUMN position_at, DROP COLUMN last_lng, DROP COLUMN last_lat, DROP COLUMN battery_percent,
  DROP COLUMN service_alerted_at, DROP COLUMN hours_since_service, DROP COLUMN service_threshold_hours;
ALTER TABLE golf.golf_carts DROP CONSTRAINT golf_carts_readiness_check;
ALTER TABLE golf.golf_carts ADD CONSTRAINT golf_carts_readiness_check
  CHECK (readiness IN ('ready', 'not_ready', 'in_use', 'charging', 'maintenance', 'out_of_service'));
ALTER TABLE golf.caddy_assignments DROP COLUMN device_id, DROP COLUMN accepted_at;
ALTER TABLE golf.caddy_attendance DROP COLUMN departed_at, DROP COLUMN shift;
DROP TABLE golf.caddy_level_history;
DROP INDEX golf.caddies_user;
ALTER TABLE golf.caddies DROP COLUMN joined_on, DROP COLUMN user_id, DROP COLUMN level_id;
DROP TABLE golf.caddy_levels;
