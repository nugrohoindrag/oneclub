-- Platform services: notification (EP-05), approval (EP-06),
-- integration layer (EP-08).

-- +goose Up
-- ── EP-05 Notification ─────────────────────────────────────────────────────
CREATE TABLE platform.notification_templates (
  id          uuid PRIMARY KEY,
  event_code  text NOT NULL,
  channel     text NOT NULL CHECK (channel IN ('in_app', 'email', 'whatsapp')),
  locale      text NOT NULL CHECK (locale IN ('en', 'id')),
  subject     text NOT NULL DEFAULT '',
  body        text NOT NULL,
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid,
  UNIQUE (event_code, channel, locale)
);
SELECT platform.add_touch_trigger('platform.notification_templates');

-- In-app notification center (FR-NOT-05).
CREATE TABLE platform.notifications (
  id          uuid PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES platform.users (id) ON DELETE CASCADE,
  event_code  text NOT NULL,
  category    text NOT NULL,
  title       text NOT NULL,
  body        text NOT NULL,
  link        text,
  read_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_user ON platform.notifications (user_id, created_at DESC);
CREATE INDEX notifications_unread ON platform.notifications (user_id) WHERE read_at IS NULL;

-- Delivery history (FR-NOT-04).
CREATE TABLE platform.notification_deliveries (
  id           uuid PRIMARY KEY,
  user_id      uuid REFERENCES platform.users (id) ON DELETE SET NULL,
  event_code   text NOT NULL,
  category     text NOT NULL,
  channel      text NOT NULL CHECK (channel IN ('in_app', 'email', 'whatsapp')),
  locale       text NOT NULL,
  recipient    text NOT NULL,
  subject      text NOT NULL DEFAULT '',
  body         text NOT NULL,
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
  attempts     int NOT NULL DEFAULT 0,
  last_error   text,
  job_id       bigint,
  created_at   timestamptz NOT NULL DEFAULT now(),
  sent_at      timestamptz,
  failed_at    timestamptz
);
CREATE INDEX notification_deliveries_created ON platform.notification_deliveries (created_at DESC);

-- FR-NOT-06 opt-out per non-mandatory category.
CREATE TABLE platform.notification_preferences (
  user_id   uuid NOT NULL REFERENCES platform.users (id) ON DELETE CASCADE,
  category  text NOT NULL,
  channel   text NOT NULL CHECK (channel IN ('in_app', 'email', 'whatsapp')),
  enabled   boolean NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, category, channel)
);

-- ── EP-06 Approval ─────────────────────────────────────────────────────────
-- Document types are registered by modules through the public interface
-- (FR-APR-09) and synchronised at startup.
CREATE TABLE platform.approval_document_types (
  code        text PRIMARY KEY,
  module      text NOT NULL,
  name        text NOT NULL,
  attributes  jsonb NOT NULL DEFAULT '[]'::jsonb  -- [{key,label,type}]
);

CREATE TABLE platform.approval_workflows (
  id             uuid PRIMARY KEY,
  document_type  text NOT NULL REFERENCES platform.approval_document_types (code),
  name           text NOT NULL,
  property_id    uuid REFERENCES platform.properties (id),  -- NULL = all properties
  priority       int NOT NULL DEFAULT 100,                  -- lower wins
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
SELECT platform.add_touch_trigger('platform.approval_workflows');

CREATE TABLE platform.approval_workflow_steps (
  id                uuid PRIMARY KEY,
  workflow_id       uuid NOT NULL REFERENCES platform.approval_workflows (id) ON DELETE CASCADE,
  step_no           int NOT NULL CHECK (step_no > 0),
  name              text NOT NULL,
  approver_type     text NOT NULL CHECK (approver_type IN ('role', 'user')),
  approver_role_id  uuid REFERENCES platform.roles (id),
  approver_user_id  uuid REFERENCES platform.users (id),
  conditions        jsonb NOT NULL DEFAULT '[]'::jsonb,  -- [{attribute, operator, value}] (FR-APR-02)
  sla_hours         int CHECK (sla_hours IS NULL OR sla_hours > 0),  -- FR-APR-08
  UNIQUE (workflow_id, step_no),
  CHECK ((approver_type = 'role' AND approver_role_id IS NOT NULL) OR (approver_type = 'user' AND approver_user_id IS NOT NULL))
);

-- Status set follows Naming Convention §31 (FR-APR-04).
CREATE TABLE platform.approval_requests (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  document_type   text NOT NULL REFERENCES platform.approval_document_types (code),
  document_id     uuid NOT NULL,
  document_ref    text NOT NULL,
  title           text NOT NULL,
  attributes      jsonb NOT NULL DEFAULT '{}'::jsonb,
  workflow_id     uuid REFERENCES platform.approval_workflows (id),
  status          text NOT NULL CHECK (status IN ('draft', 'pending', 'approved', 'rejected', 'cancelled')),
  current_step_no int,
  requested_by    uuid NOT NULL REFERENCES platform.users (id),
  decided_at      timestamptz,
  decision_reason text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_requests_document ON platform.approval_requests (document_type, document_id);
SELECT platform.enable_property_rls('platform.approval_requests');
SELECT platform.add_touch_trigger('platform.approval_requests');

CREATE TABLE platform.approval_request_steps (
  id               uuid PRIMARY KEY,
  request_id       uuid NOT NULL REFERENCES platform.approval_requests (id) ON DELETE CASCADE,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  step_no          int NOT NULL,
  name             text NOT NULL,
  approver_type    text NOT NULL CHECK (approver_type IN ('role', 'user')),
  approver_role_id uuid REFERENCES platform.roles (id),
  approver_user_id uuid REFERENCES platform.users (id),
  status           text NOT NULL CHECK (status IN ('waiting', 'pending', 'approved', 'rejected', 'skipped', 'cancelled')),
  decided_by       uuid REFERENCES platform.users (id),
  decided_on_behalf_of uuid REFERENCES platform.users (id),  -- delegation (FR-APR-07)
  decided_at       timestamptz,
  reason           text,
  due_at           timestamptz,
  reminded_at      timestamptz,
  UNIQUE (request_id, step_no)
);
SELECT platform.enable_property_rls('platform.approval_request_steps');

-- FR-APR-07 approver delegation.
CREATE TABLE platform.approval_delegations (
  id                 uuid PRIMARY KEY,
  delegator_user_id  uuid NOT NULL REFERENCES platform.users (id),
  delegate_user_id   uuid NOT NULL REFERENCES platform.users (id),
  starts_at          timestamptz NOT NULL,
  ends_at            timestamptz NOT NULL,
  reason             text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  revoked_at         timestamptz,
  CHECK (ends_at > starts_at),
  CHECK (delegator_user_id <> delegate_user_id)
);

-- ── EP-08 Integration Layer ────────────────────────────────────────────────
CREATE TABLE platform.integrations (
  id                   uuid PRIMARY KEY,
  code                 text NOT NULL UNIQUE CHECK (code ~ '^[a-z][a-z0-9-]{1,40}$'),
  adapter              text NOT NULL,           -- vendor implementation key
  capability           text NOT NULL CHECK (capability IN ('payment', 'messaging', 'email', 'tax_invoice', 'resident_data', 'hardware')),
  name                 text NOT NULL,
  enabled              boolean NOT NULL DEFAULT false,
  mode                 text NOT NULL DEFAULT 'sandbox' CHECK (mode IN ('sandbox', 'production')),
  credentials_enc      bytea,                   -- AES-GCM (FR-INT-02)
  webhook_secret_enc   bytea,
  settings             jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_tested_at       timestamptz,
  last_test_ok         boolean,
  last_test_message    text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid
);
SELECT platform.add_touch_trigger('platform.integrations');

CREATE TABLE platform.integration_logs (
  id              uuid PRIMARY KEY,
  integration_id  uuid REFERENCES platform.integrations (id) ON DELETE SET NULL,
  integration_code text NOT NULL,
  direction       text NOT NULL CHECK (direction IN ('outbound', 'inbound')),
  operation       text NOT NULL,
  method          text,
  url             text,
  request         jsonb,   -- masked (FR-INT-04)
  response        jsonb,   -- masked
  status_code     int,
  success         boolean NOT NULL,
  duration_ms     int NOT NULL DEFAULT 0,
  error           text,
  correlation_id  text,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX integration_logs_created ON platform.integration_logs (created_at DESC);
CREATE INDEX integration_logs_integration ON platform.integration_logs (integration_code, created_at DESC);

-- FR-INT-03 inbound webhooks with idempotent processing.
CREATE TABLE platform.webhook_events (
  id               uuid PRIMARY KEY,
  integration_id   uuid NOT NULL REFERENCES platform.integrations (id),
  external_id      text NOT NULL,
  event_type       text NOT NULL,
  payload          jsonb NOT NULL,
  status           text NOT NULL CHECK (status IN ('received', 'processed', 'failed')),
  attempts         int NOT NULL DEFAULT 0,
  last_error       text,
  received_at      timestamptz NOT NULL DEFAULT now(),
  processed_at     timestamptz,
  UNIQUE (integration_id, external_id)
);

-- FR-INT-07 local bridge agents (locker, turnstile, ball dispenser).
CREATE TABLE platform.bridge_agents (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  name               text NOT NULL,
  token_hash         text NOT NULL UNIQUE,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  agent_version      text,
  hardware           jsonb NOT NULL DEFAULT '[]'::jsonb,
  last_heartbeat_at  timestamptz,
  last_ip            text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid
);
SELECT platform.enable_property_rls('platform.bridge_agents');
SELECT platform.add_touch_trigger('platform.bridge_agents');

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.bridge_agents, platform.webhook_events, platform.integration_logs, platform.integrations,
  platform.approval_delegations, platform.approval_request_steps, platform.approval_requests,
  platform.approval_workflow_steps, platform.approval_workflows, platform.approval_document_types,
  platform.notification_preferences, platform.notification_deliveries, platform.notifications,
  platform.notification_templates;
