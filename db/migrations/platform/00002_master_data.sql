-- Venues, departments, employees (EP-02, EP-04), files, business rules /
-- club policies framework, idempotency keys, transactional outbox, imports.

-- +goose Up
CREATE TABLE platform.venues (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  venue_type   text NOT NULL DEFAULT 'golf' CHECK (venue_type IN ('golf', 'sport', 'stay', 'banquet', 'dining', 'other')),
  description  text,
  -- 'pending' while waiting for the Venue Activation approval (PRD §8)
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'inactive', 'rejected')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('platform.venues');
SELECT platform.add_touch_trigger('platform.venues');

CREATE TABLE platform.departments (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  parent_id    uuid REFERENCES platform.departments (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
SELECT platform.enable_property_rls('platform.departments');
SELECT platform.add_touch_trigger('platform.departments');

CREATE TABLE platform.employees (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  employee_no    text NOT NULL,
  full_name      text NOT NULL,
  department_id  uuid REFERENCES platform.departments (id),
  job_title      text,
  email          text,
  phone          text,
  join_date      date,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, employee_no)
);
SELECT platform.enable_property_rls('platform.employees');
SELECT platform.add_touch_trigger('platform.employees');

ALTER TABLE platform.users
  ADD CONSTRAINT users_employee_fk FOREIGN KEY (employee_id) REFERENCES platform.employees (id);
CREATE UNIQUE INDEX users_employee_uniq ON platform.users (employee_id) WHERE employee_id IS NOT NULL;

-- Stored files (branding images, exports). Bytes live in object storage.
CREATE TABLE platform.files (
  id            uuid PRIMARY KEY,
  storage_key   text NOT NULL UNIQUE,
  filename      text NOT NULL,
  content_type  text NOT NULL,
  size_bytes    bigint NOT NULL,
  purpose       text NOT NULL CHECK (purpose IN ('branding', 'export', 'import', 'attachment')),
  public        boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);

-- Business Rules & Club Policies framework: versioned values with
-- effective dates; domain-specific rules arrive in P1+ (PRD §6.1).
CREATE TABLE platform.rules (
  id              uuid PRIMARY KEY,
  kind            text NOT NULL CHECK (kind IN ('business_rule', 'club_policy')),
  category        text NOT NULL,         -- e.g. "Golf Policies", "Cancellation Policies"
  code            text NOT NULL CHECK (code ~ '^[a-z][a-z0-9_.]{1,80}$'),
  name            text NOT NULL,
  description     text,
  property_id     uuid REFERENCES platform.properties (id), -- NULL = all properties
  version         int NOT NULL,
  effective_from  timestamptz NOT NULL,
  value           jsonb NOT NULL,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('draft', 'active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
CREATE UNIQUE INDEX rules_version_uniq ON platform.rules
  (kind, code, coalesce(property_id, '00000000-0000-0000-0000-000000000000'::uuid), version);
SELECT platform.add_touch_trigger('platform.rules');

-- FR-JOB-06 Idempotency-Key store.
CREATE TABLE platform.idempotency_keys (
  actor_id       text NOT NULL,
  key            text NOT NULL,
  method         text NOT NULL,
  path           text NOT NULL,
  request_hash   text NOT NULL,
  status         text NOT NULL DEFAULT 'in_progress' CHECK (status IN ('in_progress', 'completed')),
  response_code  int,
  response_body  bytea,
  response_type  text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  completed_at   timestamptz,
  PRIMARY KEY (actor_id, key)
);
CREATE INDEX idempotency_keys_created ON platform.idempotency_keys (created_at);

-- FR-JOB-02 transactional outbox (Technical Doc §4.3).
CREATE TABLE platform.outbox (
  id              uuid PRIMARY KEY,
  event_type      text NOT NULL,
  aggregate_type  text NOT NULL,
  aggregate_id    uuid,
  property_id     uuid,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at     timestamptz NOT NULL DEFAULT now(),
  dispatched_at   timestamptz,
  attempts        int NOT NULL DEFAULT 0,
  last_error      text
);
CREATE INDEX outbox_pending ON platform.outbox (occurred_at) WHERE dispatched_at IS NULL;

-- Subscriber dedupe: at-least-once delivery, idempotent subscribers.
CREATE TABLE platform.outbox_processed (
  subscriber    text NOT NULL,
  event_id      uuid NOT NULL,
  processed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (subscriber, event_id)
);

-- FR-MD-06 Master Data Import jobs.
CREATE TABLE platform.imports (
  id            uuid PRIMARY KEY,
  entity        text NOT NULL,
  property_id   uuid REFERENCES platform.properties (id),
  filename      text NOT NULL,
  mode          text NOT NULL DEFAULT 'commit' CHECK (mode IN ('preview', 'commit')),
  status        text NOT NULL CHECK (status IN ('completed', 'failed')),
  total_rows    int NOT NULL DEFAULT 0,
  inserted_rows int NOT NULL DEFAULT 0,
  updated_rows  int NOT NULL DEFAULT 0,
  failed_rows   int NOT NULL DEFAULT 0,
  errors        jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);

SELECT platform.grant_app('platform');

-- +goose Down
DROP TABLE platform.imports, platform.outbox_processed, platform.outbox, platform.idempotency_keys,
  platform.rules, platform.files;
ALTER TABLE platform.users DROP CONSTRAINT users_employee_fk;
DROP TABLE platform.employees, platform.departments, platform.venues;
