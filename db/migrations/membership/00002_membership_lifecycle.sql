-- EP-06 Basic Membership: programs, types and packages (FR-MEM-01/02),
-- applications with eligibility check and approval (FR-MEM-03/04), fee and
-- activation (FR-MEM-05/06), membership status (FR-MEM-07), cards with the
-- digital card QR (FR-MEM-08), family members and corporate nominees
-- (FR-MEM-09), renewals (FR-MEM-13) and history (FR-MEM-14).

-- +goose Up
CREATE TABLE membership.programs (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  program_kind text NOT NULL CHECK (program_kind IN ('golf', 'sport_club', 'corporate', 'residence')),
  operational  boolean NOT NULL DEFAULT false,   -- P1 operates the Golf program only
  description  text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('membership.programs');
SELECT platform.add_touch_trigger('membership.programs');

-- Golf privileges and eligibility live on the type (FR-MEM-02/10).
CREATE TABLE membership.types (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  program_id           uuid NOT NULL REFERENCES membership.programs (id),
  code                 text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name                 text NOT NULL,
  category             text NOT NULL CHECK (category IN ('individual', 'family', 'corporate')),
  member_rate          boolean NOT NULL DEFAULT true,     -- plays at Member Rate
  golf_access          boolean NOT NULL DEFAULT true,
  max_guests           int NOT NULL DEFAULT 3 CHECK (max_guests >= 0),
  booking_window_days  int NOT NULL DEFAULT 14 CHECK (booking_window_days >= 0),
  max_family_members   int NOT NULL DEFAULT 0 CHECK (max_family_members >= 0),
  max_nominees         int NOT NULL DEFAULT 0 CHECK (max_nominees >= 0),
  eligibility          jsonb NOT NULL DEFAULT '{}'::jsonb,  -- {minAge,maxAge,gender,residentOnly,studentOnly,maxChildAge,maxChildren,requireSpouse}
  description          text,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('membership.types');
SELECT platform.add_touch_trigger('membership.types');

CREATE TABLE membership.packages (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  type_id        uuid NOT NULL REFERENCES membership.types (id),
  code           text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name           text NOT NULL,
  period_unit    text NOT NULL CHECK (period_unit IN ('year', 'month')),
  period_count   int NOT NULL DEFAULT 1 CHECK (period_count > 0),
  joining_fee    numeric(19,4) NOT NULL DEFAULT 0 CHECK (joining_fee >= 0),
  period_fee     numeric(19,4) NOT NULL DEFAULT 0 CHECK (period_fee >= 0),
  currency       char(3) NOT NULL DEFAULT 'IDR',
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  archived_at    timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('membership.packages');
SELECT platform.add_touch_trigger('membership.packages');

-- Members become full entities linked to their customer profile.
ALTER TABLE membership.members
  ADD COLUMN customer_id  uuid REFERENCES crm.customers (id),
  ADD COLUMN joined_on    date,
  ADD COLUMN legacy_ref   text;
ALTER TABLE membership.members DROP CONSTRAINT members_status_check;
ALTER TABLE membership.members ADD CONSTRAINT members_status_check
  CHECK (status IN ('pending', 'active', 'inactive', 'suspended', 'expired'));
CREATE UNIQUE INDEX members_customer ON membership.members (property_id, customer_id) WHERE customer_id IS NOT NULL;

CREATE TABLE membership.applications (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  channel               text NOT NULL DEFAULT 'back_office' CHECK (channel IN ('back_office', 'member_portal')),
  customer_id           uuid NOT NULL REFERENCES crm.customers (id),
  type_id               uuid NOT NULL REFERENCES membership.types (id),
  package_id            uuid NOT NULL REFERENCES membership.packages (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  dependents            jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{customerId, relationship, student}]
  documents             jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{fileId, kind, verified}]
  eligibility           jsonb,                                 -- last eligibility check result
  status                text NOT NULL CHECK (status IN ('draft', 'pending', 'approved', 'rejected', 'cancelled', 'completed')),
  approval_request_id   uuid,
  fee_folio_id          uuid REFERENCES billing.folios (id),
  membership_id         uuid,
  notes                 text,
  submitted_at          timestamptz,
  decided_at            timestamptz,
  decision_reason       text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('membership.applications');
SELECT platform.add_touch_trigger('membership.applications');

-- One row per membership period holder; family members and corporate
-- nominees point to the principal membership (FR-MEM-09).
CREATE TABLE membership.memberships (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  member_id              uuid NOT NULL REFERENCES membership.members (id),
  type_id                uuid NOT NULL REFERENCES membership.types (id),
  package_id             uuid REFERENCES membership.packages (id),
  principal_id           uuid REFERENCES membership.memberships (id),
  role                   text NOT NULL DEFAULT 'principal' CHECK (role IN ('principal', 'family', 'nominee')),
  relationship           text,
  corporate_account_id   uuid REFERENCES crm.corporate_accounts (id),
  starts_on              date NOT NULL,
  ends_on                date,
  status                 text NOT NULL CHECK (status IN ('pending', 'active', 'expired', 'inactive')),
  application_id         uuid REFERENCES membership.applications (id),
  activated_at           timestamptz,
  legacy_ref             text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  CHECK (ends_on IS NULL OR ends_on >= starts_on)
);
CREATE INDEX memberships_member ON membership.memberships (member_id, status);
CREATE INDEX memberships_ends ON membership.memberships (property_id, ends_on) WHERE status = 'active';
SELECT platform.enable_property_rls('membership.memberships');
SELECT platform.add_touch_trigger('membership.memberships');
ALTER TABLE membership.applications ADD CONSTRAINT applications_membership_fk FOREIGN KEY (membership_id) REFERENCES membership.memberships (id);

CREATE TABLE membership.cards (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  member_id      uuid NOT NULL REFERENCES membership.members (id),
  membership_id  uuid REFERENCES membership.memberships (id),
  card_number    text NOT NULL,
  legacy_number  text,                -- card number from Rhapsody
  card_type      text NOT NULL CHECK (card_type IN ('physical', 'digital')),
  qr_token       text NOT NULL,       -- opaque token encoded in the QR (check-in)
  issued_at      timestamptz NOT NULL DEFAULT now(),
  valid_until    date,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (property_id, card_number)
);
CREATE UNIQUE INDEX cards_qr ON membership.cards (qr_token);
CREATE INDEX cards_legacy ON membership.cards (property_id, legacy_number) WHERE legacy_number IS NOT NULL;
SELECT platform.enable_property_rls('membership.cards');
SELECT platform.add_touch_trigger('membership.cards');

CREATE TABLE membership.renewals (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  membership_id   uuid NOT NULL REFERENCES membership.memberships (id),
  package_id      uuid NOT NULL REFERENCES membership.packages (id),
  from_ends_on    date,
  new_ends_on     date NOT NULL,
  folio_id        uuid REFERENCES billing.folios (id),
  status          text NOT NULL CHECK (status IN ('pending', 'completed', 'cancelled')),
  completed_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid
);
SELECT platform.enable_property_rls('membership.renewals');
SELECT platform.add_touch_trigger('membership.renewals');

-- FR-MEM-14 history of status, period, card and fee events.
CREATE TABLE membership.history (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  member_id      uuid NOT NULL REFERENCES membership.members (id),
  membership_id  uuid REFERENCES membership.memberships (id),
  event          text NOT NULL,
  from_status    text,
  to_status      text,
  details        jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  actor_id       uuid
);
CREATE INDEX history_member ON membership.history (member_id, occurred_at DESC);
SELECT platform.enable_property_rls('membership.history');

-- Renewal reminders sent (H-30 / H-7), one per membership and offset.
CREATE TABLE membership.reminders (
  membership_id  uuid NOT NULL REFERENCES membership.memberships (id),
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  days_before    int NOT NULL,
  ends_on        date NOT NULL,
  sent_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (membership_id, days_before, ends_on)
);
SELECT platform.enable_property_rls('membership.reminders');

CREATE TABLE membership.sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL,
  last_value   int NOT NULL,
  PRIMARY KEY (property_id, prefix)
);
SELECT platform.enable_property_rls('membership.sequences');

ALTER TABLE billing.customer_accounts
  ADD CONSTRAINT customer_accounts_member_fk FOREIGN KEY (member_id) REFERENCES membership.members (id);

SELECT platform.grant_app('membership');

-- +goose Down
ALTER TABLE billing.customer_accounts DROP CONSTRAINT customer_accounts_member_fk;
DROP TABLE membership.sequences, membership.reminders, membership.history, membership.renewals, membership.cards;
ALTER TABLE membership.applications DROP CONSTRAINT applications_membership_fk;
DROP TABLE membership.memberships, membership.applications, membership.packages, membership.types, membership.programs;
ALTER TABLE membership.members DROP CONSTRAINT members_status_check;
ALTER TABLE membership.members ADD CONSTRAINT members_status_check CHECK (status IN ('active', 'inactive', 'suspended', 'expired'));
ALTER TABLE membership.members DROP COLUMN customer_id, DROP COLUMN joined_on, DROP COLUMN legacy_ref;
