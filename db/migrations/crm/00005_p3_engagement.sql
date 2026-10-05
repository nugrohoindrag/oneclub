-- PRD P3 EP-05 Advanced Customer 360 & Segmentation, EP-06 Customer
-- Engagement (Campaign & Reminder), EP-07 Feedback, NPS & Complaint Ticket,
-- EP-08 Top Spender and EP-09 Loyalty Foundation on P2's CRM Foundation
-- (crm/00003). Expand-only (Technical Doc §7.5): P2 rows keep their meaning.
-- Cross-domain data (folios, payments, memberships, rounds) is read through
-- reporting views (reporting/00007), never through foreign keys.

-- +goose Up
-- ── EP-09 Loyalty ─────────────────────────────────────────────────────────
-- Tier foundation (FR-LOY-07): threshold on points earned / eligible spend in
-- the evaluation period of the Loyalty Policies; the multiplier is the basic
-- benefit.
CREATE TABLE crm.loyalty_tiers (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  rank         int NOT NULL DEFAULT 1 CHECK (rank >= 0),
  min_points   bigint NOT NULL DEFAULT 0 CHECK (min_points >= 0),
  min_spend    numeric(19,4) NOT NULL DEFAULT 0 CHECK (min_spend >= 0),
  multiplier   numeric(9,4) NOT NULL DEFAULT 1 CHECK (multiplier >= 0),
  benefits     text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.loyalty_tiers');
SELECT platform.add_touch_trigger('crm.loyalty_tiers');

-- Earning Rules (FR-LOY-02): per business line / revenue component / outlet /
-- product a points rate or multiplier (0 excludes), or activity points.
CREATE TABLE crm.loyalty_earning_rules (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL,
  name               text NOT NULL,
  rule_type          text NOT NULL DEFAULT 'spend' CHECK (rule_type IN ('spend', 'activity')),
  business_line      text,
  revenue_component  text,
  outlet_id          uuid,          -- commercial outlet (validated through reporting.eng_outlets)
  product_id         uuid,          -- commercial product
  amount_per_point   numeric(19,4) CHECK (amount_per_point IS NULL OR amount_per_point > 0),
  multiplier         numeric(9,4) NOT NULL DEFAULT 1 CHECK (multiplier >= 0),
  activity           text CHECK (activity IS NULL OR activity IN ('round_finished', 'referral', 'event_attended')),
  points             bigint NOT NULL DEFAULT 0 CHECK (points >= 0),
  valid_from         date,
  valid_to           date,
  priority           int NOT NULL DEFAULT 100,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.loyalty_earning_rules');
SELECT platform.add_touch_trigger('crm.loyalty_earning_rules');

-- Loyalty Account per customer with opt-in (FR-LOY-01). The balance is the
-- running sum of the ledger, kept on the row that is locked by every posting.
CREATE TABLE crm.loyalty_accounts (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  customer_id        uuid NOT NULL REFERENCES crm.customers (id),
  tier_id            uuid REFERENCES crm.loyalty_tiers (id),
  tier_locked        boolean NOT NULL DEFAULT false,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'suspended')),
  balance            bigint NOT NULL DEFAULT 0,
  lifetime_points    bigint NOT NULL DEFAULT 0,
  enrolled_via       text NOT NULL DEFAULT 'staff' CHECK (enrolled_via IN ('staff', 'member_app', 'auto', 'migration')),
  opted_in_at        timestamptz NOT NULL DEFAULT now(),
  tier_evaluated_at  timestamptz,
  status_reason      text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  UNIQUE (property_id, number),
  UNIQUE (customer_id)
);
SELECT platform.enable_property_rls('crm.loyalty_accounts');
SELECT platform.add_touch_trigger('crm.loyalty_accounts');

-- Points Ledger (FR-LOY-03): append-only; corrections are new entries.
CREATE TABLE crm.loyalty_ledger (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  account_id       uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  kind             text NOT NULL CHECK (kind IN ('earned', 'redeemed', 'expired', 'adjusted', 'reversed')),
  points           bigint NOT NULL CHECK (points <> 0),
  balance_after    bigint NOT NULL,
  source_type      text NOT NULL,     -- billing.payment, billing.folio, crm.reward_redemption, crm.adjustment, golf.flight, migration, expiry …
  source_id        uuid,
  source_ref       text,
  amount           numeric(19,4),     -- eligible spend (earned) or tender value (redeemed)
  currency         char(3),
  idempotency_key  text,
  reverses_id      uuid REFERENCES crm.loyalty_ledger (id),
  expires_on       date,              -- positive entries: end of validity
  description      text NOT NULL,
  occurred_at      timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  UNIQUE (account_id, idempotency_key)
);
CREATE INDEX loyalty_ledger_account ON crm.loyalty_ledger (account_id, occurred_at DESC);
CREATE INDEX loyalty_ledger_source ON crm.loyalty_ledger (source_type, source_id);
CREATE INDEX loyalty_ledger_property ON crm.loyalty_ledger (property_id, occurred_at);
SELECT platform.enable_property_rls('crm.loyalty_ledger');
-- +goose StatementBegin
CREATE FUNCTION crm.forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd
CREATE TRIGGER loyalty_ledger_append_only BEFORE UPDATE OR DELETE ON crm.loyalty_ledger
  FOR EACH ROW EXECUTE FUNCTION crm.forbid_change();

-- Validity lots of positive entries (12 months rolling, FR-LOY-06):
-- redemptions consume the lots that expire first.
CREATE TABLE crm.loyalty_lots (
  ledger_id    uuid PRIMARY KEY REFERENCES crm.loyalty_ledger (id),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  account_id   uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  points       bigint NOT NULL CHECK (points > 0),
  remaining    bigint NOT NULL CHECK (remaining >= 0),
  expires_on   date NOT NULL,
  notified_at  timestamptz,
  expired_at   timestamptz
);
CREATE INDEX loyalty_lots_open ON crm.loyalty_lots (account_id, expires_on) WHERE remaining > 0;
CREATE INDEX loyalty_lots_due ON crm.loyalty_lots (expires_on) WHERE remaining > 0;
SELECT platform.enable_property_rls('crm.loyalty_lots');

-- Rewards catalogue (FR-LOY-05): fulfilment is a record (and a code text for
-- voucher rewards; the voucher itself is issued in Commercial).
CREATE TABLE crm.loyalty_rewards (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL,
  name              text NOT NULL,
  description       text,
  reward_type       text NOT NULL DEFAULT 'merchandise' CHECK (reward_type IN ('voucher', 'merchandise', 'service', 'other')),
  points_cost       bigint NOT NULL CHECK (points_cost > 0),
  stock             int CHECK (stock IS NULL OR stock >= 0),
  voucher_type_ref  text,
  min_tier_id       uuid REFERENCES crm.loyalty_tiers (id),
  valid_from        date,
  valid_to          date,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.loyalty_rewards');
SELECT platform.add_touch_trigger('crm.loyalty_rewards');

CREATE TABLE crm.loyalty_reward_redemptions (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  number              text NOT NULL,
  account_id          uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  reward_id           uuid NOT NULL REFERENCES crm.loyalty_rewards (id),
  quantity            int NOT NULL DEFAULT 1 CHECK (quantity > 0),
  points              bigint NOT NULL CHECK (points > 0),
  status              text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'cancelled')),
  fulfilment_code     text,
  channel             text NOT NULL DEFAULT 'staff' CHECK (channel IN ('staff', 'member_app')),
  ledger_id           uuid REFERENCES crm.loyalty_ledger (id),
  reversal_ledger_id  uuid REFERENCES crm.loyalty_ledger (id),
  note                text,
  completed_at        timestamptz,
  completed_by        uuid,
  cancelled_at        timestamptz,
  cancel_reason       text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX loyalty_reward_redemptions_account ON crm.loyalty_reward_redemptions (account_id, created_at DESC);
SELECT platform.enable_property_rls('crm.loyalty_reward_redemptions');
SELECT platform.add_touch_trigger('crm.loyalty_reward_redemptions');

-- Adjust Points (FR-LOY-09): applied only once the approval is granted.
CREATE TABLE crm.loyalty_adjustments (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  account_id           uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  points               bigint NOT NULL CHECK (points <> 0),
  reason               text NOT NULL,
  source_type          text NOT NULL DEFAULT 'manual' CHECK (source_type IN ('manual', 'ticket')),
  source_id            uuid,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  ledger_id            uuid REFERENCES crm.loyalty_ledger (id),
  decided_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('crm.loyalty_adjustments');
SELECT platform.add_touch_trigger('crm.loyalty_adjustments');

CREATE TABLE crm.loyalty_tier_history (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  account_id    uuid NOT NULL REFERENCES crm.loyalty_accounts (id),
  from_tier_id  uuid REFERENCES crm.loyalty_tiers (id),
  to_tier_id    uuid REFERENCES crm.loyalty_tiers (id),
  reason        text NOT NULL,
  points_basis  bigint,
  spend_basis   numeric(19,4),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);
CREATE INDEX loyalty_tier_history_account ON crm.loyalty_tier_history (account_id, created_at DESC);
SELECT platform.enable_property_rls('crm.loyalty_tier_history');

-- Notes for the loyalty tier of a customer (FR-TOP-05: from the Top Spender
-- list, reviewed at the next tier evaluation or applied with Set Tier).
CREATE TABLE crm.loyalty_tier_notes (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  customer_id        uuid NOT NULL REFERENCES crm.customers (id),
  suggested_tier_id  uuid REFERENCES crm.loyalty_tiers (id),
  note               text NOT NULL,
  source             text NOT NULL DEFAULT 'top_spender' CHECK (source IN ('top_spender', 'staff')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
CREATE INDEX loyalty_tier_notes_customer ON crm.loyalty_tier_notes (customer_id, created_at DESC);
SELECT platform.enable_property_rls('crm.loyalty_tier_notes');

-- ── EP-06 Communication Preferences, suppression, campaigns, reminders ────
-- Opt-in per channel (FR-CMP-02, UU PDP). Without a row, e-mail and in-app
-- follow P1's marketing opt-in; WhatsApp needs an explicit opt-in.
CREATE TABLE crm.communication_preferences (
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  channel      text NOT NULL CHECK (channel IN ('email', 'whatsapp', 'in_app')),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  opted_in     boolean NOT NULL,
  source       text NOT NULL CHECK (source IN ('staff', 'member', 'unsubscribe', 'import', 'website')),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  PRIMARY KEY (customer_id, channel)
);
SELECT platform.enable_property_rls('crm.communication_preferences');

-- Consent history (evidence of every opt-in / opt-out).
CREATE TABLE crm.consent_events (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  channel      text NOT NULL,
  opted_in     boolean NOT NULL,
  source       text NOT NULL,
  ref_type     text,
  ref_id       uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
CREATE INDEX consent_events_customer ON crm.consent_events (customer_id, created_at DESC);
SELECT platform.enable_property_rls('crm.consent_events');

-- Suppression list (bounces, complaints, legal requests).
CREATE TABLE crm.suppressions (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  channel      text NOT NULL CHECK (channel IN ('email', 'whatsapp')),
  address      text NOT NULL,
  customer_id  uuid REFERENCES crm.customers (id),
  reason       text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  UNIQUE (property_id, channel, address)
);
SELECT platform.enable_property_rls('crm.suppressions');
SELECT platform.add_touch_trigger('crm.suppressions');

-- P3 campaigns (FR-CMP-01..08): own message, schedule, promo code (shared or
-- unique per recipient), tracking link, approval above a recipient count.
ALTER TABLE crm.campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE crm.campaigns ADD CONSTRAINT campaigns_status_check CHECK (status IN ('draft', 'pending', 'scheduled', 'sent', 'cancelled'));
ALTER TABLE crm.campaigns
  ADD COLUMN subject              text,
  ADD COLUMN body                 text,
  ADD COLUMN promo_code           text,
  ADD COLUMN promo_mode           text NOT NULL DEFAULT 'none' CHECK (promo_mode IN ('none', 'shared', 'unique')),
  ADD COLUMN target_url           text,
  ADD COLUMN voucher_type_ref     text,
  ADD COLUMN scheduled_at         timestamptz,
  ADD COLUMN approval_request_id  uuid,
  ADD COLUMN audience_built_at    timestamptz,
  ADD COLUMN recipient_count      int NOT NULL DEFAULT 0,
  ADD COLUMN cancel_reason        text;
CREATE INDEX campaigns_due ON crm.campaigns (scheduled_at) WHERE status = 'scheduled';

CREATE TABLE crm.campaign_recipients (
  id               uuid PRIMARY KEY,
  campaign_id      uuid NOT NULL REFERENCES crm.campaigns (id),
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  customer_id      uuid NOT NULL REFERENCES crm.customers (id),
  channel          text NOT NULL CHECK (channel IN ('email', 'whatsapp', 'in_app')),
  address          text,
  token            text NOT NULL UNIQUE,
  promo_code       text,
  status           text NOT NULL CHECK (status IN ('queued', 'sent', 'failed', 'skipped_no_consent', 'skipped_suppressed',
                     'skipped_frequency_cap', 'skipped_no_contact')),
  sent_at          timestamptz,
  clicked_at       timestamptz,
  click_count      int NOT NULL DEFAULT 0,
  unsubscribed_at  timestamptz,
  converted_at     timestamptz,
  conversion_ref   text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (campaign_id, customer_id)
);
CREATE INDEX campaign_recipients_customer ON crm.campaign_recipients (customer_id, sent_at DESC);
CREATE INDEX campaign_recipients_queue ON crm.campaign_recipients (campaign_id) WHERE status = 'queued';
CREATE INDEX campaign_recipients_promo ON crm.campaign_recipients (property_id, promo_code) WHERE promo_code IS NOT NULL;
SELECT platform.enable_property_rls('crm.campaign_recipients');

-- Renewal / Birthday / Follow-up reminders (FR-CMP-06) and their send log
-- (one message per rule, customer and occurrence).
CREATE TABLE crm.reminder_rules (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('birthday', 'renewal', 'follow_up')),
  days_offset     int NOT NULL DEFAULT 0 CHECK (days_offset >= 0 AND days_offset <= 365),
  channel         text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'whatsapp', 'in_app')),
  template_event  text NOT NULL DEFAULT 'crm.reminder_message',
  subject         text,
  body            text,
  promo_code      text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.reminder_rules');
SELECT platform.add_touch_trigger('crm.reminder_rules');

CREATE TABLE crm.reminder_log (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  rule_id      uuid NOT NULL REFERENCES crm.reminder_rules (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  occurrence   text NOT NULL,
  channel      text NOT NULL,
  status       text NOT NULL CHECK (status IN ('sent', 'skipped_no_consent', 'skipped_suppressed', 'skipped_no_contact')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_id, customer_id, occurrence)
);
CREATE INDEX reminder_log_created ON crm.reminder_log (property_id, created_at DESC);
SELECT platform.enable_property_rls('crm.reminder_log');

-- ── EP-05 automatic interactions (FR-C360-05): tickets, reminders, quotations,
-- events and loyalty join P2's sources.
ALTER TABLE crm.interactions DROP CONSTRAINT interactions_source_check;
ALTER TABLE crm.interactions ADD CONSTRAINT interactions_source_check
  CHECK (source IN ('manual', 'notification', 'campaign', 'feedback', 'ticket', 'reminder', 'quotation', 'event', 'loyalty'));

-- ── EP-05 segments: dynamic (refreshed daily) or static (snapshot) ───────
ALTER TABLE crm.segments
  ADD COLUMN segment_type  text NOT NULL DEFAULT 'dynamic' CHECK (segment_type IN ('dynamic', 'static')),
  ADD COLUMN refreshed_at  timestamptz;

-- Customer tags contributed by other modules through events (FR-C360-02
-- configurable segments: tournament participant, wedding, event guest …).
CREATE TABLE crm.customer_tags (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  tag          text NOT NULL CHECK (tag ~ '^[a-z][a-z0-9_]{1,40}$'),
  source_type  text NOT NULL,
  source_id    uuid,
  tagged_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (customer_id, tag)
);
CREATE INDEX customer_tags_tag ON crm.customer_tags (property_id, tag, tagged_at);
SELECT platform.enable_property_rls('crm.customer_tags');

-- ── EP-07 Complaint Ticket, SLA, escalation, compensation, NPS ───────────
CREATE TABLE crm.ticket_categories (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  code                    text NOT NULL,
  name                    text NOT NULL,
  business_line           text,
  default_priority        text NOT NULL DEFAULT 'medium' CHECK (default_priority IN ('low', 'medium', 'high', 'urgent')),
  department_id           uuid REFERENCES platform.departments (id),
  first_response_minutes  int CHECK (first_response_minutes IS NULL OR first_response_minutes > 0),
  resolution_minutes      int CHECK (resolution_minutes IS NULL OR resolution_minutes > 0),
  status                  text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              uuid,
  archived_at             timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.ticket_categories');
SELECT platform.add_touch_trigger('crm.ticket_categories');

CREATE TABLE crm.tickets (
  id                       uuid PRIMARY KEY,
  property_id              uuid NOT NULL REFERENCES platform.properties (id),
  number                   text NOT NULL,
  customer_id              uuid REFERENCES crm.customers (id),
  contact_name             text,
  contact_email            text,
  contact_phone            text,
  category_id              uuid REFERENCES crm.ticket_categories (id),
  business_line            text NOT NULL DEFAULT 'other',
  priority                 text NOT NULL DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
  channel                  text NOT NULL DEFAULT 'staff' CHECK (channel IN ('staff', 'member_app', 'website', 'feedback', 'whatsapp', 'email', 'phone')),
  subject                  text NOT NULL,
  description              text NOT NULL,
  attachment_file_ids      uuid[] NOT NULL DEFAULT '{}',
  status                   text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'escalated', 'resolved', 'closed')),
  feedback_id              uuid REFERENCES crm.feedback (id),
  department_id            uuid REFERENCES platform.departments (id),
  assigned_to              uuid REFERENCES platform.users (id),
  escalation_level         int NOT NULL DEFAULT 0 CHECK (escalation_level BETWEEN 0 AND 2),
  escalated_at             timestamptz,
  first_response_due_at    timestamptz NOT NULL,
  resolution_due_at        timestamptz NOT NULL,
  first_responded_at       timestamptz,
  resolved_at              timestamptz,
  resolution               text,
  closed_at                timestamptz,
  reopened_count           int NOT NULL DEFAULT 0,
  first_response_breached  boolean NOT NULL DEFAULT false,
  resolution_breached      boolean NOT NULL DEFAULT false,
  sla_policy_version       int NOT NULL DEFAULT 0,
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid,
  updated_at               timestamptz NOT NULL DEFAULT now(),
  updated_by               uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX tickets_feedback ON crm.tickets (feedback_id) WHERE feedback_id IS NOT NULL;
CREATE INDEX tickets_open ON crm.tickets (property_id, status, resolution_due_at) WHERE status IN ('open', 'in_progress', 'escalated');
CREATE INDEX tickets_customer ON crm.tickets (customer_id, created_at DESC);
SELECT platform.enable_property_rls('crm.tickets');
SELECT platform.add_touch_trigger('crm.tickets');

-- Ticket history: comments (customer-visible or internal), status changes,
-- assignments, escalations and compensation.
CREATE TABLE crm.ticket_events (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  ticket_id    uuid NOT NULL REFERENCES crm.tickets (id),
  kind         text NOT NULL CHECK (kind IN ('created', 'comment', 'customer_reply', 'status', 'assigned', 'escalated', 'compensation', 'reopened')),
  body         text,
  internal     boolean NOT NULL DEFAULT false,
  from_status  text,
  to_status    text,
  details      jsonb NOT NULL DEFAULT '{}'::jsonb,
  actor_type   text NOT NULL DEFAULT 'staff' CHECK (actor_type IN ('staff', 'customer', 'system')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
CREATE INDEX ticket_events_ticket ON crm.ticket_events (ticket_id, created_at);
SELECT platform.enable_property_rls('crm.ticket_events');

-- Compensation from a ticket (FR-TKT-05) through approval.
CREATE TABLE crm.ticket_compensations (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  ticket_id            uuid NOT NULL REFERENCES crm.tickets (id),
  comp_type            text NOT NULL CHECK (comp_type IN ('points', 'voucher', 'refund', 'other')),
  points               bigint CHECK (points IS NULL OR points > 0),
  amount               numeric(19,4) CHECK (amount IS NULL OR amount > 0),
  description          text NOT NULL,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  ledger_id            uuid REFERENCES crm.loyalty_ledger (id),
  decided_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('crm.ticket_compensations');
SELECT platform.add_touch_trigger('crm.ticket_compensations');

-- Relationship NPS answers (Member App, staff) next to P2's survey NPS.
CREATE TABLE crm.nps_responses (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  customer_id    uuid REFERENCES crm.customers (id),
  score          int NOT NULL CHECK (score BETWEEN 0 AND 10),
  comment        text,
  business_line  text NOT NULL DEFAULT 'other',
  channel        text NOT NULL DEFAULT 'member_app' CHECK (channel IN ('member_app', 'staff', 'link')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
CREATE INDEX nps_responses_created ON crm.nps_responses (property_id, created_at);
SELECT platform.enable_property_rls('crm.nps_responses');

-- ── EP-08 Top Spender monthly snapshot (rank history) ─────────────────────
CREATE TABLE crm.top_spender_snapshots (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  period       text NOT NULL CHECK (period ~ '^[0-9]{4}-[0-9]{2}$'),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  rank         int NOT NULL,
  spend        numeric(19,4) NOT NULL,
  visits       int NOT NULL DEFAULT 0,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, period, customer_id)
);
SELECT platform.enable_property_rls('crm.top_spender_snapshots');

SELECT platform.grant_app('crm');

-- +goose Down
DELETE FROM crm.interactions WHERE source IN ('ticket', 'reminder', 'quotation', 'event', 'loyalty');
ALTER TABLE crm.interactions DROP CONSTRAINT interactions_source_check;
ALTER TABLE crm.interactions ADD CONSTRAINT interactions_source_check CHECK (source IN ('manual', 'notification', 'campaign', 'feedback'));
DROP TABLE crm.top_spender_snapshots, crm.nps_responses, crm.ticket_compensations, crm.ticket_events, crm.tickets, crm.ticket_categories;
ALTER TABLE crm.segments DROP COLUMN refreshed_at, DROP COLUMN segment_type;
DROP TABLE crm.customer_tags;
DROP TABLE crm.reminder_log, crm.reminder_rules, crm.campaign_recipients;
DROP INDEX crm.campaigns_due;
ALTER TABLE crm.campaigns DROP COLUMN cancel_reason, DROP COLUMN recipient_count, DROP COLUMN audience_built_at, DROP COLUMN approval_request_id,
  DROP COLUMN scheduled_at, DROP COLUMN voucher_type_ref, DROP COLUMN target_url, DROP COLUMN promo_mode, DROP COLUMN promo_code, DROP COLUMN body, DROP COLUMN subject;
ALTER TABLE crm.campaigns DROP CONSTRAINT campaigns_status_check;
ALTER TABLE crm.campaigns ADD CONSTRAINT campaigns_status_check CHECK (status IN ('draft', 'sent', 'cancelled'));
DROP TABLE crm.suppressions, crm.consent_events, crm.communication_preferences;
DROP TABLE crm.loyalty_tier_notes;
DROP TABLE crm.loyalty_tier_history, crm.loyalty_adjustments, crm.loyalty_reward_redemptions, crm.loyalty_rewards, crm.loyalty_lots;
DROP TRIGGER loyalty_ledger_append_only ON crm.loyalty_ledger;
DROP TABLE crm.loyalty_ledger;
DROP FUNCTION crm.forbid_change();
DROP TABLE crm.loyalty_accounts, crm.loyalty_earning_rules, crm.loyalty_tiers;
