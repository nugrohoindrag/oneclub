-- PRD P5 EP-17 Advanced Segmentation & VIP, EP-18 Advanced Loyalty, EP-19
-- Campaign & Lifecycle Automation (journeys) and EP-20 CRM & Sales
-- Analytics on P3's engagement & loyalty (crm/00005, crm/00007).
-- Expand-only (Technical Doc §7.5): P3 rows keep their meaning. Other
-- domains (folios, payments, memberships, bookings) are read through
-- reporting views, never through foreign keys.

-- +goose Up
-- ── EP-18 tier benefits (FR-LOY-P5-02) and grace (FR-LOY-P5-01) ──────────
-- Benefits read by pricing / booking through the public interface
-- (loyalty.BenefitsOf, reporting.crm_tier_benefits): booking window +days,
-- F&B discount, VIP event access, priority service (PRD P5 §16 #14).
ALTER TABLE crm.loyalty_tiers
  ADD COLUMN booking_window_days   int NOT NULL DEFAULT 0 CHECK (booking_window_days BETWEEN 0 AND 60),
  ADD COLUMN fnb_discount_percent  numeric(5,2) NOT NULL DEFAULT 0 CHECK (fnb_discount_percent BETWEEN 0 AND 100),
  ADD COLUMN event_access          boolean NOT NULL DEFAULT false,
  ADD COLUMN priority_service      boolean NOT NULL DEFAULT false;

-- Grace before a downgrade: the account keeps its tier until grace_until and
-- then falls to the tier it qualifies for at that date.
ALTER TABLE crm.loyalty_accounts
  ADD COLUMN grace_until    date,
  ADD COLUMN grace_tier_id  uuid REFERENCES crm.loyalty_tiers (id),
  ADD COLUMN tier_since     date;
CREATE INDEX loyalty_accounts_grace ON crm.loyalty_accounts (grace_until) WHERE grace_until IS NOT NULL;

-- Reward cost (FR-LOY-P5-03 biaya; FR-LOY-P5-06 program cost).
ALTER TABLE crm.loyalty_rewards ADD COLUMN unit_cost numeric(19,4) NOT NULL DEFAULT 0 CHECK (unit_cost >= 0);

-- Tier Evaluation runs (annual, periodic upgrade check, grace review, manual).
CREATE TABLE crm.loyalty_tier_evaluations (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  number         text NOT NULL,
  kind           text NOT NULL CHECK (kind IN ('annual', 'periodic', 'grace_review')),
  evaluated_on   date NOT NULL,
  window_from    date NOT NULL,
  window_to      date NOT NULL,
  accounts       int NOT NULL DEFAULT 0,
  upgraded       int NOT NULL DEFAULT 0,
  downgraded     int NOT NULL DEFAULT 0,
  retained       int NOT NULL DEFAULT 0,
  grace_started  int NOT NULL DEFAULT 0,
  in_grace       int NOT NULL DEFAULT 0,
  policy_version int NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX loyalty_tier_evaluations_day ON crm.loyalty_tier_evaluations (property_id, evaluated_on DESC);
SELECT platform.enable_property_rls('crm.loyalty_tier_evaluations');

CREATE TABLE crm.loyalty_tier_evaluation_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  evaluation_id      uuid NOT NULL REFERENCES crm.loyalty_tier_evaluations (id),
  account_id         uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  from_tier_id       uuid REFERENCES crm.loyalty_tiers (id),
  qualified_tier_id  uuid REFERENCES crm.loyalty_tiers (id),
  to_tier_id         uuid REFERENCES crm.loyalty_tiers (id),
  outcome            text NOT NULL CHECK (outcome IN ('upgraded', 'retained', 'grace_started', 'in_grace', 'downgraded', 'locked')),
  spend_basis        numeric(19,4) NOT NULL DEFAULT 0,
  points_basis       bigint NOT NULL DEFAULT 0,
  grace_until        date,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX loyalty_tier_evaluation_lines_run ON crm.loyalty_tier_evaluation_lines (evaluation_id);
CREATE INDEX loyalty_tier_evaluation_lines_account ON crm.loyalty_tier_evaluation_lines (account_id, created_at DESC);
SELECT platform.enable_property_rls('crm.loyalty_tier_evaluation_lines');

-- Reward Eligibility rules (FR-LOY-P5-04): a reward with active rules is
-- available to customers matching at least one rule (tier, segment,
-- activity, period) within the limit per customer.
CREATE TABLE crm.loyalty_reward_rules (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL,
  name              text NOT NULL,
  reward_id         uuid REFERENCES crm.loyalty_rewards (id),
  min_tier_id       uuid REFERENCES crm.loyalty_tiers (id),
  segment_id        uuid REFERENCES crm.segments (id),
  min_visits        int CHECK (min_visits IS NULL OR min_visits >= 0),
  min_spend         numeric(19,4) CHECK (min_spend IS NULL OR min_spend >= 0),
  lookback_days     int NOT NULL DEFAULT 365 CHECK (lookback_days BETWEEN 1 AND 1095),
  valid_from        date,
  valid_to          date,
  max_per_customer  int CHECK (max_per_customer IS NULL OR max_per_customer > 0),
  limit_period      text NOT NULL DEFAULT 'year' CHECK (limit_period IN ('lifetime', 'year', 'month')),
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.loyalty_reward_rules');
SELECT platform.add_touch_trigger('crm.loyalty_reward_rules');

-- Rewards issued without points (Top Spender programme, journey, staff):
-- they count toward the loyalty programme cost and budget.
CREATE TABLE crm.loyalty_reward_issues (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  number           text NOT NULL,
  customer_id      uuid NOT NULL REFERENCES crm.customers (id),
  reward_id        uuid NOT NULL REFERENCES crm.loyalty_rewards (id),
  quantity         int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  source           text NOT NULL CHECK (source IN ('top_spender', 'journey', 'staff')),
  source_id        uuid,
  source_ref       text,
  period           text,
  unit_cost        numeric(19,4) NOT NULL DEFAULT 0,
  total_cost       numeric(19,4) NOT NULL DEFAULT 0,
  status           text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'fulfilled', 'cancelled')),
  fulfilment_code  text,
  voucher_codes    text[] NOT NULL DEFAULT '{}',
  note             text,
  idempotency_key  text,
  fulfilled_at     timestamptz,
  cancelled_at     timestamptz,
  cancel_reason    text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  UNIQUE (property_id, number),
  UNIQUE (property_id, idempotency_key)
);
CREATE INDEX loyalty_reward_issues_customer ON crm.loyalty_reward_issues (customer_id, created_at DESC);
SELECT platform.enable_property_rls('crm.loyalty_reward_issues');
SELECT platform.add_touch_trigger('crm.loyalty_reward_issues');

-- Top Spender driven engagement (FR-LOY-P5-05): reward and / or invitation
-- (static segment) for the top N of each period.
CREATE TABLE crm.top_spender_programs (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  code                   text NOT NULL,
  name                   text NOT NULL,
  period_type            text NOT NULL DEFAULT 'month' CHECK (period_type IN ('month', 'quarter', 'year')),
  top_n                  int NOT NULL DEFAULT 10 CHECK (top_n BETWEEN 1 AND 500),
  business_line          text,
  reward_id              uuid REFERENCES crm.loyalty_rewards (id),
  invitation_segment_id  uuid REFERENCES crm.segments (id),
  invitation_message     text,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  archived_at            timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.top_spender_programs');
SELECT platform.add_touch_trigger('crm.top_spender_programs');

CREATE TABLE crm.top_spender_program_runs (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  program_id      uuid NOT NULL REFERENCES crm.top_spender_programs (id),
  period          text NOT NULL,
  period_from     date NOT NULL,
  period_to       date NOT NULL,
  ranked          int NOT NULL DEFAULT 0,
  issued          int NOT NULL DEFAULT 0,
  invited         int NOT NULL DEFAULT 0,
  skipped_budget  int NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  UNIQUE (program_id, period)
);
SELECT platform.enable_property_rls('crm.top_spender_program_runs');

-- ── EP-17 analytics store (FR-SEG-01..05): computed on schedule ───────────
-- Cross-business behaviour: recency / frequency / monetary per line.
CREATE TABLE crm.customer_line_behavior (
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  customer_id    uuid NOT NULL REFERENCES crm.customers (id),
  business_line  text NOT NULL,
  last_at        timestamptz,
  recency_days   int,
  frequency      int NOT NULL DEFAULT 0,
  monetary       numeric(19,4) NOT NULL DEFAULT 0,
  computed_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_id, business_line)
);
CREATE INDEX customer_line_behavior_property ON crm.customer_line_behavior (property_id, business_line);
SELECT platform.enable_property_rls('crm.customer_line_behavior');

-- RFM score snapshots (one per customer and day): scores 1–5 and the group;
-- consecutive snapshots give the movement between groups.
CREATE TABLE crm.rfm_scores (
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  as_of         date NOT NULL,
  customer_id   uuid NOT NULL REFERENCES crm.customers (id),
  recency_days  int,
  frequency     int NOT NULL DEFAULT 0,
  monetary      numeric(19,4) NOT NULL DEFAULT 0,
  r_score       int NOT NULL CHECK (r_score BETWEEN 1 AND 5),
  f_score       int NOT NULL CHECK (f_score BETWEEN 1 AND 5),
  m_score       int NOT NULL CHECK (m_score BETWEEN 1 AND 5),
  rfm_group     text NOT NULL CHECK (rfm_group IN ('champions', 'loyal', 'potential', 'new', 'at_risk', 'lapsed')),
  lines         text[] NOT NULL DEFAULT '{}',
  clv           numeric(19,4) NOT NULL DEFAULT 0,
  computed_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_id, as_of)
);
CREATE INDEX rfm_scores_property ON crm.rfm_scores (property_id, as_of, rfm_group);
SELECT platform.enable_property_rls('crm.rfm_scores');

-- VIP segmentation (FR-SEG-03): from Top Spender, tier or staff; benefits
-- and handling shown at check-in / POS.
CREATE TABLE crm.vip_customers (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  customer_id    uuid NOT NULL REFERENCES crm.customers (id),
  level          text NOT NULL DEFAULT 'vip' CHECK (level IN ('vip', 'vvip')),
  source         text NOT NULL CHECK (source IN ('top_spender', 'tier', 'manual')),
  reason         text,
  benefits       text,
  handling_note  text,
  valid_until    date,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (customer_id)
);
CREATE INDEX vip_customers_property ON crm.vip_customers (property_id, status);
SELECT platform.enable_property_rls('crm.vip_customers');
SELECT platform.add_touch_trigger('crm.vip_customers');

-- Analytics refresh log (data freshness shown on the screens).
CREATE TABLE crm.analytics_runs (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  kind         text NOT NULL CHECK (kind IN ('rfm', 'vip')),
  as_of        date NOT NULL,
  rows         int NOT NULL DEFAULT 0,
  started_at   timestamptz NOT NULL,
  finished_at  timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
CREATE INDEX analytics_runs_latest ON crm.analytics_runs (property_id, kind, finished_at DESC);
SELECT platform.enable_property_rls('crm.analytics_runs');

-- ── EP-19 journeys (FR-JRN-01..07) ───────────────────────────────────────
CREATE TABLE crm.journeys (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  code                 text NOT NULL,
  name                 text NOT NULL,
  description          text,
  template             text NOT NULL DEFAULT 'custom' CHECK (template IN ('custom', 'renewal', 'birthday', 'welcome', 'win_back', 'post_event',
                         'abandoned_booking')),
  category             text NOT NULL DEFAULT 'marketing' CHECK (category IN ('marketing', 'transactional')),
  trigger_type         text NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('event', 'segment_entry', 'date', 'manual')),
  trigger_event        text,
  trigger_segment_id   uuid REFERENCES crm.segments (id),
  trigger_date         text CHECK (trigger_date IS NULL OR trigger_date IN ('birthday', 'membership_expiry', 'last_visit')),
  trigger_days         int NOT NULL DEFAULT 0 CHECK (trigger_days BETWEEN 0 AND 730),
  exit_events          text[] NOT NULL DEFAULT '{}',
  exit_segment_id      uuid REFERENCES crm.segments (id),
  goal_event           text,
  goal_days            int NOT NULL DEFAULT 7 CHECK (goal_days BETWEEN 1 AND 365),
  re_entry_days        int NOT NULL DEFAULT 0 CHECK (re_entry_days >= 0),
  control_percent      int NOT NULL DEFAULT 0 CHECK (control_percent BETWEEN 0 AND 50),
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending', 'active', 'paused', 'completed')),
  approval_request_id  uuid,
  activated_at         timestamptz,
  paused_at            timestamptz,
  completed_at         timestamptz,
  last_run_at          timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code)
);
CREATE INDEX journeys_trigger ON crm.journeys (trigger_event) WHERE status = 'active';
SELECT platform.enable_property_rls('crm.journeys');
SELECT platform.add_touch_trigger('crm.journeys');

-- Steps: message (A/B variant), wait (duration or until N days before the
-- anchor date), condition (branch), voucher, points, reward, sales task,
-- tag, exit. Steps run in position order unless a step names the next one.
CREATE TABLE crm.journey_steps (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  journey_id        uuid NOT NULL REFERENCES crm.journeys (id) ON DELETE CASCADE,
  key               text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,40}$'),
  position          int NOT NULL,
  step_type         text NOT NULL CHECK (step_type IN ('message', 'wait', 'condition', 'voucher', 'points', 'reward', 'sales_task', 'tag', 'exit')),
  name              text NOT NULL,
  channel           text CHECK (channel IS NULL OR channel IN ('email', 'whatsapp', 'in_app')),
  subject           text,
  body              text,
  subject_b         text,
  body_b            text,
  split_percent     int NOT NULL DEFAULT 0 CHECK (split_percent BETWEEN 0 AND 100),
  offer_title       text,
  promo_code        text,
  offer_valid_days  int CHECK (offer_valid_days IS NULL OR offer_valid_days > 0),
  wait_days         int CHECK (wait_days IS NULL OR wait_days >= 0),
  wait_hours        int CHECK (wait_hours IS NULL OR wait_hours >= 0),
  until_days_before int CHECK (until_days_before IS NULL OR until_days_before >= 0),
  condition_kind    text CHECK (condition_kind IS NULL OR condition_kind IN ('booked', 'paid', 'renewed', 'in_segment', 'tier', 'clicked', 'opted_in')),
  condition_value   text,
  on_true           text,
  on_false          text,
  next_key          text,
  points            bigint CHECK (points IS NULL OR points > 0),
  voucher_type_ref  text,
  reward_id         uuid REFERENCES crm.loyalty_rewards (id),
  task_subject      text,
  task_due_days     int CHECK (task_due_days IS NULL OR task_due_days >= 0),
  tag               text CHECK (tag IS NULL OR tag ~ '^[a-z][a-z0-9_]{1,40}$'),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (journey_id, key)
);
CREATE INDEX journey_steps_journey ON crm.journey_steps (journey_id, position);
SELECT platform.enable_property_rls('crm.journey_steps');
SELECT platform.add_touch_trigger('crm.journey_steps');

-- Enrollment of one customer per occurrence (e.g. membership:end date,
-- birthday year, event id); the control group receives no messages.
CREATE TABLE crm.journey_enrollments (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  journey_id     uuid NOT NULL REFERENCES crm.journeys (id),
  customer_id    uuid NOT NULL REFERENCES crm.customers (id),
  occurrence     text NOT NULL,
  cohort         text NOT NULL DEFAULT 'treatment' CHECK (cohort IN ('treatment', 'control')),
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'exited')),
  current_key    text,
  next_run_at    timestamptz,
  anchor_date    date,
  context        jsonb NOT NULL DEFAULT '{}'::jsonb,
  entered_at     timestamptz NOT NULL DEFAULT now(),
  completed_at   timestamptz,
  exited_at      timestamptz,
  exit_reason    text,
  converted_at   timestamptz,
  conversion_ref text,
  revenue        numeric(19,4) NOT NULL DEFAULT 0,
  created_by     uuid,
  UNIQUE (journey_id, customer_id, occurrence)
);
CREATE INDEX journey_enrollments_due ON crm.journey_enrollments (next_run_at) WHERE status = 'active';
CREATE INDEX journey_enrollments_customer ON crm.journey_enrollments (customer_id, status);
SELECT platform.enable_property_rls('crm.journey_enrollments');

-- Journey Event: every executed step (message outcome, wait, branch,
-- reward …) with the tracking of messages (read, click, conversion).
CREATE TABLE crm.journey_events (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  journey_id        uuid NOT NULL REFERENCES crm.journeys (id),
  enrollment_id     uuid NOT NULL REFERENCES crm.journey_enrollments (id),
  customer_id       uuid NOT NULL REFERENCES crm.customers (id),
  step_key          text NOT NULL,
  step_type         text NOT NULL,
  outcome           text NOT NULL CHECK (outcome IN ('sent', 'skipped_no_consent', 'skipped_suppressed', 'skipped_no_contact',
                      'skipped_frequency_cap', 'control', 'waiting', 'condition_true', 'condition_false', 'issued', 'skipped_budget', 'failed',
                      'created', 'tagged', 'exited')),
  category          text NOT NULL DEFAULT 'marketing',
  channel           text,
  variant           text CHECK (variant IS NULL OR variant IN ('A', 'B')),
  token             text UNIQUE,
  subject           text,
  body              text,
  offer_title       text,
  promo_code        text,
  offer_expires_on  date,
  details           jsonb NOT NULL DEFAULT '{}'::jsonb,
  read_at           timestamptz,
  clicked_at        timestamptz,
  click_count       int NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX journey_events_enrollment ON crm.journey_events (enrollment_id, created_at);
CREATE INDEX journey_events_sent ON crm.journey_events (customer_id, created_at) WHERE outcome = 'sent';
CREATE INDEX journey_events_journey ON crm.journey_events (journey_id, step_key);
SELECT platform.enable_property_rls('crm.journey_events');

SELECT platform.grant_app('crm');

-- +goose Down
DROP TABLE crm.journey_events, crm.journey_enrollments, crm.journey_steps, crm.journeys;
DROP TABLE crm.analytics_runs, crm.vip_customers, crm.rfm_scores, crm.customer_line_behavior;
DROP TABLE crm.top_spender_program_runs, crm.top_spender_programs, crm.loyalty_reward_issues, crm.loyalty_reward_rules;
DROP TABLE crm.loyalty_tier_evaluation_lines, crm.loyalty_tier_evaluations;
ALTER TABLE crm.loyalty_rewards DROP COLUMN unit_cost;
DROP INDEX crm.loyalty_accounts_grace;
ALTER TABLE crm.loyalty_accounts DROP COLUMN tier_since, DROP COLUMN grace_tier_id, DROP COLUMN grace_until;
ALTER TABLE crm.loyalty_tiers DROP COLUMN priority_service, DROP COLUMN event_access, DROP COLUMN fnb_discount_percent, DROP COLUMN booking_window_days;
