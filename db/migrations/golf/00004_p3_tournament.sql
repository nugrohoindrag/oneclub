-- PRD P3 EP-16 Tournament Management (golf/tournament, PRD P3 §5.4.1):
-- tournament setup (single or multi-round, stroke play / stableford, gross /
-- net, divisions), Tournament Packages & Tournament Fees billed through the
-- billing folio, sponsors (invoiced to corporate accounts) and prizes,
-- registrations with waitlist, flighting and tee assignment (sequential tee
-- times or shotgun start with A/B groups), per-round scores on the P2
-- digital scorecard (golf.scorecards is never written by tournament code;
-- tournament_scores keeps the tournament status and a snapshot for the
-- leaderboard), frozen results and awards, the read-only archive of imported
-- tournament history (FR-MIG-P3-03) and corporate tournaments / sponsorships
-- converted from accepted quotations (FR-QUO-06/07). Expand-only and
-- P3-owned: P1 and P2 tables are not altered (the course is blocked through
-- P1's golf.course_blocks). The banquet event is a plain id plus a snapshot
-- synchronised from banquet.event_* (no cross-line FK, PRD P3 §6 #17).

-- +goose Up
CREATE TABLE golf.tournaments (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  code                    text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  name                    text NOT NULL,
  description             text,
  tournament_type         text NOT NULL DEFAULT 'club' CHECK (tournament_type IN ('club', 'club_championship', 'corporate', 'invitational', 'sponsor', 'charity')),
  course_id               uuid NOT NULL REFERENCES golf.courses (id),
  playing_route_id        uuid NOT NULL REFERENCES golf.playing_routes (id),
  start_date              date NOT NULL,
  end_date                date NOT NULL,
  format                  text NOT NULL CHECK (format IN ('stroke_play', 'stableford')),
  scoring_basis           text NOT NULL DEFAULT 'gross_and_net' CHECK (scoring_basis IN ('gross', 'net', 'gross_and_net')),
  handicap_allowance      numeric(5,2) CHECK (handicap_allowance IS NULL OR handicap_allowance BETWEEN 0 AND 100),
  max_handicap            numeric(4,1) CHECK (max_handicap IS NULL OR max_handicap BETWEEN 0 AND 54),
  eligibility             text NOT NULL DEFAULT 'members_and_guests' CHECK (eligibility IN ('members', 'members_and_guests', 'invitation', 'open')),
  field_size              int NOT NULL CHECK (field_size BETWEEN 1 AND 288),
  waitlist_enabled        boolean NOT NULL DEFAULT true,
  players_per_flight      int NOT NULL DEFAULT 4 CHECK (players_per_flight BETWEEN 1 AND 5),
  start_type              text NOT NULL DEFAULT 'shotgun' CHECK (start_type IN ('shotgun', 'tee_times')),
  registration_opens_at   timestamptz,
  registration_closes_at  timestamptz,
  tie_break               text CHECK (tie_break IS NULL OR tie_break IN ('countback', 'shared')),
  cut_after_round         int CHECK (cut_after_round IS NULL OR cut_after_round >= 1),
  cut_top                 int CHECK (cut_top IS NULL OR cut_top >= 1),
  public                  boolean NOT NULL DEFAULT false,      -- website listing & registration
  leaderboard_public      boolean NOT NULL DEFAULT true,       -- website leaderboard (consented names)
  event_id                uuid,                                -- banquet event (venue, catering): plain id, no cross-line FK
  event_number            text,                                -- snapshot of banquet.event_* (FR-TRN-13)
  event_title             text,
  event_status            text,
  event_synced_at         timestamptz,
  customer_id             uuid REFERENCES crm.customers (id),  -- host / contact of a corporate tournament
  corporate_account_id    uuid REFERENCES crm.corporate_accounts (id),
  quotation_id            uuid,                                -- crm.quotation_accepted (idempotent conversion)
  quotation_number        text,
  folio_id                uuid REFERENCES billing.folios (id),            -- corporate tournament: deposits & final billing
  schedule_id             uuid REFERENCES billing.payment_schedules (id), -- corporate tournament: payment terms of the quotation
  quotation_total         numeric(19,4),
  quotation_service       numeric(19,4),
  quotation_tax           numeric(19,4),
  event_billed_at         timestamptz,
  source                  text NOT NULL DEFAULT 'oneclub' CHECK (source IN ('oneclub', 'import')),  -- import = read-only archive
  legacy_ref              text,
  currency                char(3) NOT NULL DEFAULT 'IDR',
  status                  text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed', 'in_progress', 'completed', 'cancelled')),
  current_round           int NOT NULL DEFAULT 1,
  policy_versions         jsonb NOT NULL DEFAULT '{}'::jsonb,
  finalized_at            timestamptz,
  finalized_by            uuid,
  cancelled_at            timestamptz,
  cancel_reason           text,
  notes                   text,
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              uuid,
  UNIQUE (property_id, code),
  CHECK (end_date >= start_date)
);
CREATE UNIQUE INDEX tournaments_quotation ON golf.tournaments (quotation_id) WHERE quotation_id IS NOT NULL;
CREATE UNIQUE INDEX tournaments_legacy ON golf.tournaments (property_id, legacy_ref) WHERE legacy_ref IS NOT NULL;
CREATE INDEX tournaments_dates ON golf.tournaments (property_id, start_date, status);
CREATE INDEX tournaments_event ON golf.tournaments (event_id) WHERE event_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.tournaments');
SELECT platform.add_touch_trigger('golf.tournaments');

-- One row per round (multi-day / multi-round); the course block keeps the
-- public tee times of the round closed (FR-TRN-02).
CREATE TABLE golf.tournament_rounds (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id         uuid NOT NULL REFERENCES golf.tournaments (id),
  round_no              int NOT NULL CHECK (round_no BETWEEN 1 AND 8),
  play_date             date NOT NULL,
  start_time            text NOT NULL CHECK (start_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),  -- shotgun time / first tee time (local)
  tee_interval_minutes  int NOT NULL DEFAULT 10 CHECK (tee_interval_minutes BETWEEN 4 AND 30),
  start_tees            text NOT NULL DEFAULT '1' CHECK (start_tees IN ('1', '1,10')),
  playing_route_id      uuid REFERENCES golf.playing_routes (id),
  course_block_id       uuid REFERENCES golf.course_blocks (id),
  status                text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'drawn', 'published', 'in_progress', 'completed')),
  draw_method           text,
  drawn_at              timestamptz,
  published_at          timestamptz,
  started_at            timestamptz,
  completed_at          timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tournament_id, round_no)
);
SELECT platform.enable_property_rls('golf.tournament_rounds');
SELECT platform.add_touch_trigger('golf.tournament_rounds');

-- Divisions (handicap flights A/B/C, ladies, senior, guests …): results and
-- prizes per division; the Hall of Fame division maps a champion entry.
CREATE TABLE golf.tournament_divisions (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id          uuid NOT NULL REFERENCES golf.tournaments (id),
  code                   text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                   text NOT NULL,
  sequence               int NOT NULL DEFAULT 1,
  gender                 text NOT NULL DEFAULT 'any' CHECK (gender IN ('male', 'female', 'any')),
  player_type            text NOT NULL DEFAULT 'any' CHECK (player_type IN ('member', 'guest', 'any')),
  handicap_min           numeric(4,1),
  handicap_max           numeric(4,1),
  age_min                int CHECK (age_min IS NULL OR age_min BETWEEN 0 AND 120),
  tee_set_id             uuid REFERENCES golf.tee_sets (id),
  hall_of_fame_division  text CHECK (hall_of_fame_division IS NULL OR hall_of_fame_division IN ('men', 'ladies', 'senior', 'junior', 'open')),
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (tournament_id, code),
  CHECK (handicap_min IS NULL OR handicap_max IS NULL OR handicap_max >= handicap_min)
);
SELECT platform.enable_property_rls('golf.tournament_divisions');
SELECT platform.add_touch_trigger('golf.tournament_divisions');

-- Tournament Packages (e.g. Player Package: green fee, caddy, golf cart,
-- dinner, goodie bag) and Tournament Fees (fee components; a fee without a
-- package applies to every registration of the player type).
CREATE TABLE golf.tournament_packages (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  code           text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name           text NOT NULL,
  description    text,
  player_type    text NOT NULL DEFAULT 'any' CHECK (player_type IN ('member', 'guest', 'any')),
  is_default     boolean NOT NULL DEFAULT false,
  sequence       int NOT NULL DEFAULT 1,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (tournament_id, code)
);
SELECT platform.enable_property_rls('golf.tournament_packages');
SELECT platform.add_touch_trigger('golf.tournament_packages');

CREATE TABLE golf.tournament_fees (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  package_id     uuid REFERENCES golf.tournament_packages (id),
  component      text NOT NULL CHECK (component IN ('entry_fee', 'green_fee', 'caddy_fee', 'cart_fee', 'dinner', 'goodie_bag', 'insurance', 'other')),
  name           text NOT NULL,
  player_type    text NOT NULL DEFAULT 'any' CHECK (player_type IN ('member', 'guest', 'any')),
  amount         numeric(19,4) NOT NULL CHECK (amount >= 0),       -- nett (tax & service inclusive)
  currency       char(3) NOT NULL DEFAULT 'IDR',
  tax_codes      text[] NOT NULL DEFAULT '{}',                    -- Tax & Service rules included in the nett amount
  liability      boolean NOT NULL DEFAULT false,                  -- held for a partner (caddy fee), not club revenue
  sequence       int NOT NULL DEFAULT 1,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
CREATE INDEX tournament_fees_tournament ON golf.tournament_fees (tournament_id, package_id);
SELECT platform.enable_property_rls('golf.tournament_fees');
SELECT platform.add_touch_trigger('golf.tournament_fees');

-- Sponsors: package, logo on leaderboard / start sheet, hole sponsorship,
-- sponsorship billed to the corporate account (billing folio + invoice, or
-- the payment schedule of the accepted sponsorship quotation).
CREATE TABLE golf.tournament_sponsors (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id         uuid NOT NULL REFERENCES golf.tournaments (id),
  name                  text NOT NULL,
  sponsor_level         text NOT NULL DEFAULT 'supporting' CHECK (sponsor_level IN ('title', 'platinum', 'gold', 'silver', 'bronze', 'hole', 'supporting', 'in_kind')),
  package_name          text,
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  customer_id           uuid REFERENCES crm.customers (id),
  contact_name          text,
  contact_email         text,
  contact_phone         text,
  amount                numeric(19,4) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  currency              char(3) NOT NULL DEFAULT 'IDR',
  tax_codes             text[] NOT NULL DEFAULT '{}',
  logo_file_id          uuid REFERENCES platform.files (id),
  logo_url              text,
  holes                 int[] NOT NULL DEFAULT '{}',
  show_on_leaderboard   boolean NOT NULL DEFAULT true,
  show_on_start_sheet   boolean NOT NULL DEFAULT true,
  sequence              int NOT NULL DEFAULT 1,
  folio_id              uuid REFERENCES billing.folios (id),
  invoice_id            uuid REFERENCES billing.invoices (id),
  invoiced_at           timestamptz,
  quotation_id          uuid,                                            -- sponsorship sold through CRM Sales
  quotation_number      text,
  schedule_id           uuid REFERENCES billing.payment_schedules (id),
  billed_at             timestamptz,                                     -- sponsorship charge posted (event billing)
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cancelled')),
  notes                 text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid
);
CREATE INDEX tournament_sponsors_tournament ON golf.tournament_sponsors (tournament_id);
CREATE UNIQUE INDEX tournament_sponsors_quotation ON golf.tournament_sponsors (quotation_id) WHERE quotation_id IS NOT NULL;
CREATE INDEX tournament_sponsors_folio ON golf.tournament_sponsors (folio_id) WHERE folio_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.tournament_sponsors');
SELECT platform.add_touch_trigger('golf.tournament_sponsors');

-- Participants (FR-TRN-03/04): Registered, Waitlisted, Withdrawn,
-- Checked-in; handicap snapshot at registration; fees on the folio.
CREATE TABLE golf.tournament_registrations (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id        uuid NOT NULL REFERENCES golf.tournaments (id),
  number               text NOT NULL,
  customer_id          uuid NOT NULL REFERENCES crm.customers (id),
  member_id            uuid REFERENCES membership.members (id),
  player_name          text NOT NULL,
  gender               text CHECK (gender IS NULL OR gender IN ('male', 'female')),
  email                text,
  phone                text,
  player_type          text NOT NULL CHECK (player_type IN ('member', 'guest')),
  channel              text NOT NULL CHECK (channel IN ('back_office', 'member_app', 'website', 'quotation', 'import')),
  division_id          uuid REFERENCES golf.tournament_divisions (id),
  package_id           uuid REFERENCES golf.tournament_packages (id),
  handicap_index       numeric(4,1) CHECK (handicap_index IS NULL OR handicap_index BETWEEN -10 AND 54),
  handicap_source      text NOT NULL DEFAULT 'none' CHECK (handicap_source IN ('whs', 'federation', 'manual', 'official', 'declared', 'none')),
  shirt_size           text,
  preferences          text,
  pairing_group        text,
  sponsor_id           uuid REFERENCES golf.tournament_sponsors (id),
  status               text NOT NULL CHECK (status IN ('registered', 'waitlisted', 'withdrawn', 'checked_in')),
  waitlist_position    int,
  public_consent       boolean NOT NULL DEFAULT false,
  fee_total            numeric(19,4) NOT NULL DEFAULT 0,
  currency             char(3) NOT NULL DEFAULT 'IDR',
  folio_id             uuid REFERENCES billing.folios (id),
  payment_status       text NOT NULL DEFAULT 'not_required' CHECK (payment_status IN ('not_required', 'pending', 'paid', 'waived', 'refunded', 'partially_refunded', 'cancelled')),
  payment_due_at       timestamptz,
  fee_waiver_status    text CHECK (fee_waiver_status IS NULL OR fee_waiver_status IN ('pending', 'approved', 'rejected', 'cancelled')),
  fee_waiver_reason    text,
  approval_request_id  uuid,
  refund_amount        numeric(19,4) NOT NULL DEFAULT 0,
  made_cut             boolean,
  registered_at        timestamptz NOT NULL DEFAULT now(),
  promoted_at          timestamptz,
  withdrawn_at         timestamptz,
  withdraw_reason      text,
  checked_in_at        timestamptz,
  checked_in_by        uuid,
  manage_token_hash    text,
  policy_versions      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
-- one active registration per player and tournament; no overbooking is
-- enforced under the tournament row lock (PRD P3 §12 Konkurensi)
CREATE UNIQUE INDEX tournament_registrations_player ON golf.tournament_registrations (tournament_id, customer_id) WHERE status <> 'withdrawn';
CREATE INDEX tournament_registrations_tournament ON golf.tournament_registrations (tournament_id, status);
CREATE INDEX tournament_registrations_customer ON golf.tournament_registrations (customer_id);
CREATE INDEX tournament_registrations_folio ON golf.tournament_registrations (folio_id) WHERE folio_id IS NOT NULL;
CREATE UNIQUE INDEX tournament_registrations_token ON golf.tournament_registrations (manage_token_hash) WHERE manage_token_hash IS NOT NULL;
SELECT platform.enable_property_rls('golf.tournament_registrations');
SELECT platform.add_touch_trigger('golf.tournament_registrations');

-- Prizes per position / award; recipient and hand-over (serah terima).
CREATE TABLE golf.tournament_prizes (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id    uuid NOT NULL REFERENCES golf.tournaments (id),
  category         text NOT NULL CHECK (category IN ('gross', 'net', 'stableford', 'nearest_to_pin', 'longest_drive', 'hole_in_one', 'lucky_draw', 'other')),
  division_id      uuid REFERENCES golf.tournament_divisions (id),
  position         int CHECK (position IS NULL OR position BETWEEN 1 AND 50),
  hole_number      int CHECK (hole_number IS NULL OR hole_number BETWEEN 1 AND 36),
  name             text NOT NULL,
  description      text,
  value            numeric(19,4) NOT NULL DEFAULT 0 CHECK (value >= 0),
  currency         char(3) NOT NULL DEFAULT 'IDR',
  sponsor_id       uuid REFERENCES golf.tournament_sponsors (id),
  registration_id  uuid REFERENCES golf.tournament_registrations (id),
  result_text      text,
  status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'awarded', 'handed_over', 'cancelled')),
  awarded_at       timestamptz,
  awarded_by       uuid,
  handed_over_at   timestamptz,
  handed_over_by   uuid,
  handed_over_to   text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
CREATE INDEX tournament_prizes_tournament ON golf.tournament_prizes (tournament_id);
SELECT platform.enable_property_rls('golf.tournament_prizes');
SELECT platform.add_touch_trigger('golf.tournament_prizes');

-- Flights of a round (Flighting) and the start assignment of each player
-- (Tee Assignment: start hole + A/B group for a shotgun, or a tee time).
CREATE TABLE golf.tournament_flights (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  round_id       uuid NOT NULL REFERENCES golf.tournament_rounds (id),
  flight_no      int NOT NULL,
  start_hole     int NOT NULL CHECK (start_hole BETWEEN 1 AND 36),   -- sequence on the playing route
  start_group    text CHECK (start_group IS NULL OR start_group IN ('A', 'B')),
  start_at       timestamptz NOT NULL,
  status         text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'in_play', 'completed')),
  tee_off_at     timestamptz,
  finished_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (round_id, flight_no)
);
CREATE INDEX tournament_flights_round ON golf.tournament_flights (round_id, start_at);
SELECT platform.enable_property_rls('golf.tournament_flights');
SELECT platform.add_touch_trigger('golf.tournament_flights');

CREATE TABLE golf.tournament_flight_players (
  round_id         uuid NOT NULL REFERENCES golf.tournament_rounds (id),
  registration_id  uuid NOT NULL REFERENCES golf.tournament_registrations (id),
  flight_id        uuid NOT NULL REFERENCES golf.tournament_flights (id),
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  position         int NOT NULL CHECK (position BETWEEN 1 AND 99),
  caddy_id         uuid REFERENCES golf.caddies (id),
  PRIMARY KEY (round_id, registration_id)
);
CREATE INDEX tournament_flight_players_flight ON golf.tournament_flight_players (flight_id, position);
CREATE INDEX tournament_flight_players_caddy ON golf.tournament_flight_players (caddy_id) WHERE caddy_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.tournament_flight_players');

-- Per player and round: the P2 scorecard and the tournament status of the
-- card (DQ / WD / NR handling); totals are a snapshot of the scorecard for
-- lists and reports, the leaderboard computes from the card holes.
CREATE TABLE golf.tournament_scores (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id     uuid NOT NULL REFERENCES golf.tournaments (id),
  round_id          uuid NOT NULL REFERENCES golf.tournament_rounds (id),
  registration_id   uuid NOT NULL REFERENCES golf.tournament_registrations (id),
  scorecard_id      uuid NOT NULL REFERENCES golf.scorecards (id),
  tee_set_id        uuid REFERENCES golf.tee_sets (id),
  handicap_index    numeric(4,1),
  course_handicap   int,
  playing_handicap  int,
  status            text NOT NULL DEFAULT 'not_started' CHECK (status IN ('not_started', 'in_progress', 'submitted', 'finalized', 'dq', 'wd', 'nr')),
  status_reason     text,
  thru              int NOT NULL DEFAULT 0,
  gross             int,
  net               int,
  points            int,
  to_par            int,
  attested_by       text,
  validated_at      timestamptz,
  validated_by      uuid,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (round_id, registration_id),
  UNIQUE (scorecard_id)
);
CREATE INDEX tournament_scores_tournament ON golf.tournament_scores (tournament_id, round_id);
SELECT platform.enable_property_rls('golf.tournament_scores');
SELECT platform.add_touch_trigger('golf.tournament_scores');

-- Results frozen at finalization (FR-TRN-11): position per category and
-- division (division NULL = overall), countback detail. Imported history
-- (FR-MIG-P3-03) keeps the player and division as text, without a
-- registration, keyed by the legacy key of the row (stable ids, so the Hall
-- of Fame champion entries stay idempotent on re-import).
CREATE TABLE golf.tournament_results (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id    uuid NOT NULL REFERENCES golf.tournaments (id),
  registration_id  uuid REFERENCES golf.tournament_registrations (id),
  customer_id      uuid REFERENCES crm.customers (id),
  player_name      text NOT NULL,
  division_id      uuid REFERENCES golf.tournament_divisions (id),
  division_label   text,
  category         text NOT NULL CHECK (category IN ('gross', 'net', 'stableford')),
  position         int,
  position_label   text NOT NULL,
  tied             boolean NOT NULL DEFAULT false,
  score            int,
  to_par           int,
  tie_break        text,
  rounds           jsonb NOT NULL DEFAULT '[]'::jsonb,
  legacy_key       text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CHECK (registration_id IS NOT NULL OR legacy_key IS NOT NULL)
);
CREATE UNIQUE INDEX tournament_results_registration ON golf.tournament_results
  (tournament_id, category, coalesce(division_id, '00000000-0000-0000-0000-000000000000'::uuid), registration_id) WHERE registration_id IS NOT NULL;
CREATE UNIQUE INDEX tournament_results_legacy ON golf.tournament_results (tournament_id, legacy_key) WHERE legacy_key IS NOT NULL;
CREATE INDEX tournament_results_tournament ON golf.tournament_results (tournament_id, category, division_id, position);
SELECT platform.enable_property_rls('golf.tournament_results');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.tournament_results, golf.tournament_scores, golf.tournament_flight_players, golf.tournament_flights, golf.tournament_prizes,
  golf.tournament_registrations, golf.tournament_sponsors, golf.tournament_fees, golf.tournament_packages, golf.tournament_divisions,
  golf.tournament_rounds, golf.tournaments;
