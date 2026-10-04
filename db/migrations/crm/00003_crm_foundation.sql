-- CRM Foundation (PRD P2 EP-23 Customer Preferences & Personalization,
-- EP-24 Customer 360, Interaction History, Segmentation, Feedback, Campaign).

-- +goose Up
-- Profile attributes used by P2 eligibility and personalisation that the P1
-- profile (crm/00002) does not carry. Marketing consent stays P1's
-- marketing_opt_in; profiling consent is separate (FR-CRM-01, UU PDP).
ALTER TABLE crm.customers
  ADD COLUMN resident_ref         text,
  ADD COLUMN student              boolean NOT NULL DEFAULT false,
  ADD COLUMN student_valid_until  date,
  ADD COLUMN marital_status       text CHECK (marital_status IS NULL OR marital_status IN ('single', 'married')),
  ADD COLUMN locale               text CHECK (locale IS NULL OR locale IN ('en', 'id')),
  ADD COLUMN consent_profiling    boolean NOT NULL DEFAULT false,
  ADD COLUMN consent_updated_at   timestamptz;

-- Structured preferences (FR-PRF-01) extend P1's customer_preferences
-- (FR-CUS-06): P2 categories, the source that recorded them (staff, caddy
-- tablet, member app) and a reference (caddy, product). Diet / allergy are
-- health data and flagged sensitive (FR-PRF-05).
ALTER TABLE crm.customer_preferences DROP CONSTRAINT customer_preferences_category_check;
ALTER TABLE crm.customer_preferences ADD CONSTRAINT customer_preferences_category_check CHECK (category IN ('golf', 'caddy',
  'golf_cart', 'dining', 'communication', 'other', 'favorite_caddy', 'tee_time', 'diet', 'allergy', 'food', 'beverage', 'facility', 'note'));
ALTER TABLE crm.customer_preferences
  ADD COLUMN ref_type   text,
  ADD COLUMN ref_id     uuid,
  ADD COLUMN sensitive  boolean NOT NULL DEFAULT false,
  ADD COLUMN source     text NOT NULL DEFAULT 'staff' CHECK (source IN ('staff', 'caddy', 'member', 'system'));
CREATE INDEX customer_preferences_active ON crm.customer_preferences (customer_id) WHERE status = 'active';

-- Interaction History (FR-CRM-02).
CREATE TABLE crm.interactions (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  channel      text NOT NULL CHECK (channel IN ('phone', 'whatsapp', 'email', 'visit', 'in_app', 'other')),
  direction    text NOT NULL DEFAULT 'outbound' CHECK (direction IN ('inbound', 'outbound')),
  subject      text NOT NULL,
  body         text,
  source       text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'notification', 'campaign', 'feedback')),
  ref_type     text,
  ref_id       uuid,
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
CREATE INDEX interactions_customer ON crm.interactions (customer_id, occurred_at DESC);
SELECT platform.enable_property_rls('crm.interactions');

-- Customer Segmentation (FR-CRM-03).
CREATE TABLE crm.segments (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  code          text NOT NULL,
  name          text NOT NULL,
  rules         jsonb NOT NULL DEFAULT '{}'::jsonb,
  member_count  int NOT NULL DEFAULT 0,
  computed_at   timestamptz,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.segments');
SELECT platform.add_touch_trigger('crm.segments');

CREATE TABLE crm.segment_members (
  segment_id   uuid NOT NULL REFERENCES crm.segments (id) ON DELETE CASCADE,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  PRIMARY KEY (segment_id, customer_id)
);
SELECT platform.enable_property_rls('crm.segment_members');

-- Feedback survey (FR-CRM-04): a request carries a public token link.
CREATE TABLE crm.feedback_requests (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  customer_id   uuid REFERENCES crm.customers (id),
  context_type  text NOT NULL CHECK (context_type IN ('round', 'stay', 'class', 'fnb', 'sport', 'meeting', 'other')),
  context_id    uuid,
  context_label text,
  subject_type  text,                      -- e.g. caddy (rated together with the visit)
  subject_id    uuid,
  token         text NOT NULL UNIQUE,
  channel       text NOT NULL DEFAULT 'email',
  status        text NOT NULL DEFAULT 'sent' CHECK (status IN ('sent', 'answered', 'expired')),
  sent_at       timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  created_by    uuid,
  UNIQUE (context_type, context_id, customer_id)
);
SELECT platform.enable_property_rls('crm.feedback_requests');

CREATE TABLE crm.feedback (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  request_id    uuid REFERENCES crm.feedback_requests (id),
  customer_id   uuid REFERENCES crm.customers (id),
  context_type  text NOT NULL,
  context_id    uuid,
  context_label text,
  rating        int NOT NULL CHECK (rating BETWEEN 1 AND 5),
  nps           int CHECK (nps BETWEEN 0 AND 10),
  comment       text,
  channel       text NOT NULL DEFAULT 'link',
  low_score     boolean NOT NULL DEFAULT false,
  follow_up     text NOT NULL DEFAULT 'none' CHECK (follow_up IN ('none', 'open', 'closed')),
  follow_up_note text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (request_id)
);
CREATE INDEX feedback_context ON crm.feedback (property_id, context_type, created_at);
SELECT platform.enable_property_rls('crm.feedback');

-- Campaign foundation (FR-CRM-05).
CREATE TABLE crm.campaigns (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  segment_id     uuid NOT NULL REFERENCES crm.segments (id),
  template_event text NOT NULL,
  channel        text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'whatsapp', 'in_app')),
  data           jsonb NOT NULL DEFAULT '{}'::jsonb,
  status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'sent', 'cancelled')),
  sent_at        timestamptz,
  sent_count     int NOT NULL DEFAULT 0,
  skipped_count  int NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.campaigns');
SELECT platform.add_touch_trigger('crm.campaigns');

CREATE TABLE crm.campaign_deliveries (
  campaign_id  uuid NOT NULL REFERENCES crm.campaigns (id),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  customer_id  uuid NOT NULL REFERENCES crm.customers (id),
  status       text NOT NULL CHECK (status IN ('queued', 'skipped_no_consent', 'skipped_no_contact')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (campaign_id, customer_id)
);
SELECT platform.enable_property_rls('crm.campaign_deliveries');

SELECT platform.grant_app('crm');

-- +goose Down
DROP TABLE crm.campaign_deliveries, crm.campaigns, crm.feedback, crm.feedback_requests, crm.segment_members, crm.segments, crm.interactions;
DROP INDEX crm.customer_preferences_active;
ALTER TABLE crm.customer_preferences DROP COLUMN source, DROP COLUMN sensitive, DROP COLUMN ref_id, DROP COLUMN ref_type;
ALTER TABLE crm.customer_preferences DROP CONSTRAINT customer_preferences_category_check;
ALTER TABLE crm.customer_preferences ADD CONSTRAINT customer_preferences_category_check CHECK (category IN ('golf', 'caddy', 'golf_cart', 'dining', 'communication', 'other'));
ALTER TABLE crm.customers DROP COLUMN consent_updated_at, DROP COLUMN consent_profiling, DROP COLUMN locale, DROP COLUMN marital_status,
  DROP COLUMN student_valid_until, DROP COLUMN student, DROP COLUMN resident_ref;
