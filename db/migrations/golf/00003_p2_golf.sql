-- PRD P2 Complete Golf Experience on P1's Golf Core (golf/00002): advanced
-- caddy lifecycle (EP-05), caddy tablet & round tracking (EP-06, EP-09),
-- golf cart inspections & maintenance (EP-07), digital scorecard & WHS
-- handicap (EP-08), Hole-in-One (EP-10), Hall of Fame (EP-11), Driving Range
-- (EP-12) and Reciprocal Club (EP-13). Expand-only and P2-owned: P1 tables
-- are not altered (Tech Doc §12.3, PRD P2 §5.4.1); P2 data about P1 rows
-- (caddy profile, golf cart service, round progress, pace targets) lives in
-- P2 tables keyed by the P1 id.

-- +goose Up
-- ── EP-05 caddy levels, profile, shifts, acceptance ───────────────────────
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

-- Caddy profile of P2: level, tablet login, joined date.
CREATE TABLE golf.caddy_profiles (
  caddy_id     uuid PRIMARY KEY REFERENCES golf.caddies (id),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  level_id     uuid REFERENCES golf.caddy_levels (id),
  user_id      uuid REFERENCES platform.users (id),   -- Caddy Tablet login
  joined_on    date,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid
);
CREATE UNIQUE INDEX caddy_profiles_user ON golf.caddy_profiles (user_id) WHERE user_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.caddy_profiles');
SELECT platform.add_touch_trigger('golf.caddy_profiles');

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

-- Clock-in / clock-out and shift next to P1's daily attendance (FR-CDL-03).
CREATE TABLE golf.caddy_shifts (
  caddy_id        uuid NOT NULL REFERENCES golf.caddies (id),
  work_date       date NOT NULL,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  shift           text NOT NULL DEFAULT 'full_day' CHECK (shift IN ('morning', 'afternoon', 'full_day')),
  clocked_in_at   timestamptz NOT NULL,
  clocked_out_at  timestamptz,
  created_by      uuid,
  PRIMARY KEY (caddy_id, work_date)
);
SELECT platform.enable_property_rls('golf.caddy_shifts');

-- The caddy accepts the assignment on the tablet (FR-TAB-01).
CREATE TABLE golf.caddy_assignment_acceptances (
  assignment_id  uuid PRIMARY KEY REFERENCES golf.caddy_assignments (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  accepted_at    timestamptz NOT NULL DEFAULT now(),
  device_id      text,
  accepted_by    uuid
);
SELECT platform.enable_property_rls('golf.caddy_assignment_acceptances');

-- ── EP-07 golf cart service, inspections, maintenance, GPS ───────────────
-- Service hours, battery and GPS of a P1 golf cart (FR-CTL-04, FR-PLX-05).
CREATE TABLE golf.cart_profiles (
  golf_cart_id             uuid PRIMARY KEY REFERENCES golf.golf_carts (id),
  property_id              uuid NOT NULL REFERENCES platform.properties (id),
  service_threshold_hours  numeric(10,2),
  last_service_at          timestamptz,             -- hours since service = P1 usage after this
  service_alerted_at       timestamptz,
  battery_percent          int CHECK (battery_percent IS NULL OR battery_percent BETWEEN 0 AND 100),
  last_lat                 double precision,
  last_lng                 double precision,
  position_at              timestamptz,
  gps_device_id            text,
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now(),
  updated_by               uuid
);
SELECT platform.enable_property_rls('golf.cart_profiles');
SELECT platform.add_touch_trigger('golf.cart_profiles');

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

-- ── EP-09 pace of play: targets, round and hole progress ──────────────────
-- Pace targets per hole and tolerance per playing route (defaults 15 / 10).
CREATE TABLE golf.hole_pace_targets (
  hole_id         uuid PRIMARY KEY REFERENCES golf.holes (id),
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  target_minutes  int NOT NULL CHECK (target_minutes BETWEEN 5 AND 40),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
SELECT platform.enable_property_rls('golf.hole_pace_targets');

CREATE TABLE golf.route_pace_tolerances (
  playing_route_id   uuid PRIMARY KEY REFERENCES golf.playing_routes (id),
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  tolerance_minutes  int NOT NULL CHECK (tolerance_minutes >= 0),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
SELECT platform.enable_property_rls('golf.route_pace_tolerances');

-- Round progress of a P1 flight (current hole, pace, tablet).
CREATE TABLE golf.round_progress (
  flight_id       uuid PRIMARY KEY REFERENCES golf.flights (id),
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  current_seq     int NOT NULL DEFAULT 0,
  last_hole_at    timestamptz,
  pace_status     text NOT NULL DEFAULT 'on_pace' CHECK (pace_status IN ('on_pace', 'slow', 'fast')),
  behind_minutes  int NOT NULL DEFAULT 0,
  tablet_device   text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('golf.round_progress');
SELECT platform.add_touch_trigger('golf.round_progress');

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

-- Caddy fee share of an assignment when a caddy was replaced (Caddy
-- Policies split; P1 holds the full fee on each assignment).
CREATE TABLE golf.caddy_fee_shares (
  assignment_id  uuid PRIMARY KEY REFERENCES golf.caddy_assignments (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  holes          int NOT NULL,
  share_amount   numeric(19,4) NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('golf.caddy_fee_shares');

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

-- Caddy fees (P1 assignments) and non-cash tips (P1 caddy tips) settled once.
CREATE TABLE golf.caddy_settlement_items (
  item_type      text NOT NULL CHECK (item_type IN ('caddy_fee', 'caddy_tip')),
  item_id        uuid NOT NULL,
  settlement_id  uuid NOT NULL REFERENCES golf.caddy_settlements (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  amount         numeric(19,4) NOT NULL,
  PRIMARY KEY (item_type, item_id)
);
SELECT platform.enable_property_rls('golf.caddy_settlement_items');

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

-- WHS Handicap Index computed from finalised cards and the official
-- (federation) index; P1's handicap history (golf.handicaps) is unchanged.
CREATE TABLE golf.handicap_indexes (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  customer_id     uuid NOT NULL REFERENCES crm.customers (id),
  kind            text NOT NULL CHECK (kind IN ('whs', 'federation')),
  handicap_index  numeric(4,1) NOT NULL CHECK (handicap_index BETWEEN -10 AND 54),
  rounds_counted  int,
  source          text,
  effective_at    timestamptz NOT NULL DEFAULT now(),
  created_by      uuid
);
CREATE INDEX handicap_indexes_customer ON golf.handicap_indexes (customer_id, kind, effective_at DESC);
SELECT platform.enable_property_rls('golf.handicap_indexes');

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

-- ── EP-13 Reciprocal Club (next to P1's reciprocal player fields) ─────────
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
  customer_name        text NOT NULL,
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
CREATE UNIQUE INDEX reciprocal_visits_player ON golf.reciprocal_visits (booking_player_id) WHERE booking_player_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.reciprocal_visits');
SELECT platform.add_touch_trigger('golf.reciprocal_visits');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.reciprocal_visits, golf.introduction_letters, golf.reciprocal_clubs, golf.range_buckets, golf.range_sessions, golf.range_bays,
  golf.hall_of_fame, golf.hio_records, golf.handicap_indexes, golf.score_audit, golf.scorecard_holes, golf.scorecards, golf.caddy_settlement_items,
  golf.caddy_settlements, golf.caddy_fee_shares, golf.caddy_favorites, golf.caddy_ratings, golf.incidents, golf.hole_progress, golf.round_progress,
  golf.route_pace_tolerances, golf.hole_pace_targets, golf.cart_maintenance, golf.cart_inspections, golf.cart_checklists, golf.cart_profiles,
  golf.caddy_assignment_acceptances, golf.caddy_shifts, golf.caddy_level_history, golf.caddy_profiles, golf.caddy_levels;
