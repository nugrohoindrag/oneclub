-- PRD P5 EP-23 Advanced Tournament (golf/tournament, PRD P5 §5.4.1,
-- lanjutan P3; additive only, P3 tables are extended, never rewritten):
--   * Team Formats (FR-TRN-P5-01, §16 #11): Scramble and Four-ball (Best
--     Ball) first, Foursomes and Texas Scramble by configuration, with the
--     handicap allowance per player; a tournament keeps a snapshot of its
--     format; teams, their members and the frozen team results.
--   * Advanced registration (FR-TRN-P5-06): categories member / guest /
--     sponsor invitation with a quota each, early-bird fee per fee line.
--   * Tournament Series & Order of Merit (FR-TRN-P5-02): points tables,
--     series with events (weight, final), points per player and event, the
--     frozen standings of completed or imported seasons.
--   * Federation (FR-TRN-P5-05, §16 #11 PGI): result reports sent / exported.

-- +goose Up
CREATE TABLE golf.team_formats (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name             text NOT NULL,
  format_type      text NOT NULL CHECK (format_type IN ('scramble', 'four_ball', 'foursomes', 'texas_scramble')),
  team_size        int NOT NULL DEFAULT 4 CHECK (team_size BETWEEN 2 AND 4),
  scores_per_hole  int NOT NULL DEFAULT 1 CHECK (scores_per_hole BETWEEN 1 AND 4),   -- best ball: best N scores of a hole count
  allowances       int[] NOT NULL DEFAULT '{}',                                      -- % of course handicap, lowest handicap first
  min_drives       int CHECK (min_drives IS NULL OR min_drives BETWEEN 0 AND 18),    -- Texas Scramble: drives each player must use
  description      text,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.team_formats');
SELECT platform.add_touch_trigger('golf.team_formats');

CREATE TABLE golf.tournament_team_settings (
  tournament_id    uuid PRIMARY KEY REFERENCES golf.tournaments (id),
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  team_format_id   uuid NOT NULL REFERENCES golf.team_formats (id),
  format_snapshot  jsonb NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
SELECT platform.enable_property_rls('golf.tournament_team_settings');
SELECT platform.add_touch_trigger('golf.tournament_team_settings');

CREATE TABLE golf.tournament_teams (
  id                        uuid PRIMARY KEY,
  property_id               uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id             uuid NOT NULL REFERENCES golf.tournaments (id),
  code                      text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                      text NOT NULL,
  captain_registration_id   uuid REFERENCES golf.tournament_registrations (id),
  created_at                timestamptz NOT NULL DEFAULT now(),
  created_by                uuid,
  updated_at                timestamptz NOT NULL DEFAULT now(),
  updated_by                uuid,
  UNIQUE (tournament_id, code)
);
SELECT platform.enable_property_rls('golf.tournament_teams');
SELECT platform.add_touch_trigger('golf.tournament_teams');

CREATE TABLE golf.tournament_team_members (
  team_id          uuid NOT NULL REFERENCES golf.tournament_teams (id) ON DELETE CASCADE,
  registration_id  uuid NOT NULL REFERENCES golf.tournament_registrations (id),
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  position         int NOT NULL DEFAULT 1,
  PRIMARY KEY (team_id, registration_id),
  UNIQUE (registration_id)
);
SELECT platform.enable_property_rls('golf.tournament_team_members');

CREATE TABLE golf.tournament_team_results (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id   uuid NOT NULL REFERENCES golf.tournaments (id),
  team_id         uuid NOT NULL REFERENCES golf.tournament_teams (id),
  team_name       text NOT NULL,
  members         jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{registrationId, customerId, playerName, consent}]
  category        text NOT NULL CHECK (category IN ('gross', 'net', 'stableford')),
  position        int,
  position_label  text NOT NULL,
  tied            boolean NOT NULL DEFAULT false,
  score           int,
  to_par          int,
  team_handicap   int,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tournament_id, team_id, category)
);
SELECT platform.enable_property_rls('golf.tournament_team_results');

-- Advanced registration: categories with quotas, early-bird fees.
CREATE TABLE golf.tournament_registration_categories (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  category       text NOT NULL CHECK (category IN ('member', 'guest', 'sponsor_invitation')),
  quota          int CHECK (quota IS NULL OR quota >= 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (tournament_id, category)
);
SELECT platform.enable_property_rls('golf.tournament_registration_categories');
SELECT platform.add_touch_trigger('golf.tournament_registration_categories');

ALTER TABLE golf.tournament_fees
  ADD COLUMN early_bird_amount numeric(19,4) CHECK (early_bird_amount IS NULL OR early_bird_amount >= 0),
  ADD COLUMN early_bird_until  date,
  ADD COLUMN category          text CHECK (category IS NULL OR category IN ('member', 'guest', 'sponsor_invitation'));
ALTER TABLE golf.tournament_registrations
  ADD COLUMN category   text CHECK (category IS NULL OR category IN ('member', 'guest', 'sponsor_invitation')),
  ADD COLUMN early_bird boolean NOT NULL DEFAULT false;

-- A waitlisted player is promoted only while the quota of their category
-- has room (used by the waitlist promotion).
CREATE FUNCTION golf.tournament_category_full(registration uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS (
    SELECT 1 FROM golf.tournament_registrations r
    JOIN golf.tournament_registration_categories c ON c.tournament_id = r.tournament_id AND c.category = r.category
    WHERE r.id = registration AND c.quota IS NOT NULL
      AND (SELECT count(*) FROM golf.tournament_registrations x WHERE x.tournament_id = r.tournament_id AND x.category = r.category
           AND x.status IN ('registered', 'checked_in')) >= c.quota)
$$;

-- Tournament Series & Order of Merit.
CREATE TABLE golf.series_points_tables (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  code                  text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                  text NOT NULL,
  points                int[] NOT NULL DEFAULT '{}',          -- points of positions 1, 2, 3 …
  participation_points  int NOT NULL DEFAULT 0 CHECK (participation_points >= 0),
  tie_rule              text NOT NULL DEFAULT 'split' CHECK (tie_rule IN ('split', 'full')),
  description           text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('golf.series_points_tables');
SELECT platform.add_touch_trigger('golf.series_points_tables');

CREATE TABLE golf.tournament_series (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  code                   text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,39}$'),
  name                   text NOT NULL,
  season                 int NOT NULL CHECK (season BETWEEN 1990 AND 2200),
  description            text,
  category               text NOT NULL DEFAULT 'primary' CHECK (category IN ('primary', 'gross', 'net', 'stableford')),
  points_table_id        uuid REFERENCES golf.series_points_tables (id),
  points_snapshot        jsonb,                                  -- points table frozen at activation (versioned)
  best_of                int CHECK (best_of IS NULL OR best_of >= 1),
  min_events             int NOT NULL DEFAULT 0 CHECK (min_events >= 0),
  public                 boolean NOT NULL DEFAULT false,         -- website leaderboard (names with consent)
  status                 text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'completed', 'cancelled')),
  source                 text NOT NULL DEFAULT 'oneclub' CHECK (source IN ('oneclub', 'import')),
  legacy_ref             text,
  champion_customer_id   uuid REFERENCES crm.customers (id),
  champion_name          text,
  activated_at           timestamptz,
  completed_at           timestamptz,
  cancelled_at           timestamptz,
  cancel_reason          text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, code)
);
CREATE INDEX tournament_series_season ON golf.tournament_series (property_id, season);
SELECT platform.enable_property_rls('golf.tournament_series');
SELECT platform.add_touch_trigger('golf.tournament_series');

CREATE TABLE golf.tournament_series_events (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  series_id      uuid NOT NULL REFERENCES golf.tournament_series (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  sequence       int NOT NULL DEFAULT 1,
  weight         numeric(5,2) NOT NULL DEFAULT 1 CHECK (weight > 0 AND weight <= 10),
  is_final       boolean NOT NULL DEFAULT false,
  status         text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'counted')),
  counted_at     timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (series_id, tournament_id)
);
CREATE INDEX tournament_series_events_tournament ON golf.tournament_series_events (tournament_id);
SELECT platform.enable_property_rls('golf.tournament_series_events');
SELECT platform.add_touch_trigger('golf.tournament_series_events');

CREATE TABLE golf.tournament_series_points (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  series_id        uuid NOT NULL REFERENCES golf.tournament_series (id),
  series_event_id  uuid NOT NULL REFERENCES golf.tournament_series_events (id) ON DELETE CASCADE,
  tournament_id    uuid NOT NULL REFERENCES golf.tournaments (id),
  player_key       text NOT NULL,                      -- customer id, else n:<lower(name)> (imported history)
  customer_id      uuid REFERENCES crm.customers (id),
  player_name      text NOT NULL,
  team_name        text,
  position         int,
  position_label   text NOT NULL,
  tied             boolean NOT NULL DEFAULT false,
  base_points      numeric(9,2) NOT NULL DEFAULT 0,
  weight           numeric(5,2) NOT NULL DEFAULT 1,
  points           numeric(9,2) NOT NULL DEFAULT 0,
  public_consent   boolean NOT NULL DEFAULT false,
  event_date       date NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (series_event_id, player_key)
);
CREATE INDEX tournament_series_points_series ON golf.tournament_series_points (series_id, player_key);
CREATE INDEX tournament_series_points_customer ON golf.tournament_series_points (customer_id) WHERE customer_id IS NOT NULL;
SELECT platform.enable_property_rls('golf.tournament_series_points');

-- Final standings of completed seasons and of imported (historical) seasons.
CREATE TABLE golf.tournament_series_standings (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  series_id       uuid NOT NULL REFERENCES golf.tournament_series (id),
  player_key      text NOT NULL,
  customer_id     uuid REFERENCES crm.customers (id),
  player_name     text NOT NULL,
  rank            int,
  position_label  text NOT NULL,
  points          numeric(9,2) NOT NULL DEFAULT 0,
  events          int NOT NULL DEFAULT 0,
  wins            int NOT NULL DEFAULT 0,
  top3            int NOT NULL DEFAULT 0,
  best_position   int,
  public_consent  boolean NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (series_id, player_key)
);
SELECT platform.enable_property_rls('golf.tournament_series_standings');

CREATE TABLE golf.tournament_federation_reports (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  tournament_id  uuid NOT NULL REFERENCES golf.tournaments (id),
  federation     text NOT NULL DEFAULT 'PGI',
  method         text NOT NULL CHECK (method IN ('manual_upload', 'api')),
  status         text NOT NULL CHECK (status IN ('exported', 'submitted', 'failed')),
  reference      text,
  rows           int NOT NULL DEFAULT 0,
  payload        jsonb NOT NULL DEFAULT '[]'::jsonb,
  submitted_at   timestamptz NOT NULL DEFAULT now(),
  submitted_by   uuid
);
CREATE INDEX tournament_federation_reports_tournament ON golf.tournament_federation_reports (tournament_id, submitted_at);
SELECT platform.enable_property_rls('golf.tournament_federation_reports');

SELECT platform.grant_app('golf');

-- +goose Down
DROP TABLE golf.tournament_federation_reports, golf.tournament_series_standings, golf.tournament_series_points, golf.tournament_series_events,
  golf.tournament_series, golf.series_points_tables;
DROP FUNCTION golf.tournament_category_full(uuid);
ALTER TABLE golf.tournament_registrations DROP COLUMN early_bird, DROP COLUMN category;
ALTER TABLE golf.tournament_fees DROP COLUMN category, DROP COLUMN early_bird_until, DROP COLUMN early_bird_amount;
DROP TABLE golf.tournament_registration_categories, golf.tournament_team_results, golf.tournament_team_members, golf.tournament_teams,
  golf.tournament_team_settings, golf.team_formats;
