-- PRD P2 EP-04 Complete Membership Lifecycle on P1's membership foundation
-- (membership/00002): multi-program types (Sport Club, Corporate, Residence),
-- entitlements per business line (C5), annual fee & due date with grace and
-- automatic suspension, pause / resume, postpone, suspension, reactivation,
-- upgrade / downgrade, cancellation, card replacement and nominee changes.
-- Expand-only.

-- +goose Up
ALTER TABLE membership.programs ALTER COLUMN operational SET DEFAULT true;

-- Types of every program (FR-MBL-02): categories, annual fee & grace
-- (FR-MBL-04), entitlements (FR-MBL-05), upgrade rank (FR-MBL-06) and
-- policy fees (FR-MBL-08/12/13).
ALTER TABLE membership.types DROP CONSTRAINT types_category_check;
ALTER TABLE membership.types ADD CONSTRAINT types_category_check CHECK (category IN ('individual', 'family', 'corporate', 'couple', 'senior',
  'student', 'junior', 'residence', 'bulk_entrance', 'monthly', 'other'));
ALTER TABLE membership.types
  ADD COLUMN annual_fee            numeric(19,4) NOT NULL DEFAULT 0 CHECK (annual_fee >= 0),
  ADD COLUMN grace_days            int NOT NULL DEFAULT 30 CHECK (grace_days >= 0),
  ADD COLUMN rank                  int NOT NULL DEFAULT 0,
  ADD COLUMN entitlements          jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN card_replacement_fee  numeric(19,4) NOT NULL DEFAULT 0 CHECK (card_replacement_fee >= 0),
  ADD COLUMN reactivation_fee      numeric(19,4) NOT NULL DEFAULT 0 CHECK (reactivation_fee >= 0),
  ADD COLUMN nominee_change_fee    numeric(19,4) NOT NULL DEFAULT 0 CHECK (nominee_change_fee >= 0);

-- Memberships: Paused, Suspended and Cancelled (FR-MBL-14).
ALTER TABLE membership.memberships DROP CONSTRAINT memberships_status_check;
ALTER TABLE membership.memberships ADD CONSTRAINT memberships_status_check
  CHECK (status IN ('pending', 'active', 'expired', 'inactive', 'paused', 'suspended', 'cancelled'));
ALTER TABLE membership.memberships
  ADD COLUMN next_fee_due       date,
  ADD COLUMN paused_from        date,
  ADD COLUMN paused_until       date,
  ADD COLUMN suspension_kind    text CHECK (suspension_kind IS NULL OR suspension_kind IN ('arrears', 'discipline')),
  ADD COLUMN suspension_reason  text,
  ADD COLUMN suspended_at       timestamptz,
  ADD COLUMN cancelled_at       timestamptz,
  ADD COLUMN cancel_reason      text;
ALTER TABLE membership.members DROP CONSTRAINT members_status_check;
ALTER TABLE membership.members ADD CONSTRAINT members_status_check
  CHECK (status IN ('pending', 'active', 'inactive', 'suspended', 'expired', 'paused', 'cancelled'));

-- Card replacement blocks the old card (FR-MBL-12).
ALTER TABLE membership.cards DROP CONSTRAINT cards_status_check;
ALTER TABLE membership.cards ADD CONSTRAINT cards_status_check CHECK (status IN ('active', 'inactive', 'blocked', 'replaced'));
ALTER TABLE membership.cards
  ADD COLUMN blocked_at    timestamptz,
  ADD COLUMN block_reason  text,
  ADD COLUMN replaced_by   uuid REFERENCES membership.cards (id);

-- Annual Fee & Due Date and the Membership Fee History (FR-MBL-04).
CREATE TABLE membership.fees (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  membership_id    uuid NOT NULL REFERENCES membership.memberships (id),
  fee_type         text NOT NULL CHECK (fee_type IN ('annual', 'upgrade', 'reactivation', 'card_replacement', 'nominee_change')),
  period_start     date,
  period_end       date,
  due_date         date NOT NULL,
  amount           numeric(19,4) NOT NULL CHECK (amount >= 0),
  status           text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'due', 'paid', 'waived', 'postponed', 'cancelled')),
  folio_id         uuid REFERENCES billing.folios (id),
  postponed_from   date,
  paid_at          timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
CREATE INDEX fees_due ON membership.fees (status, due_date);
CREATE INDEX fees_membership ON membership.fees (membership_id, due_date);
SELECT platform.enable_property_rls('membership.fees');
SELECT platform.add_touch_trigger('membership.fees');

CREATE TABLE membership.fee_reminders (
  fee_id       uuid NOT NULL REFERENCES membership.fees (id),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  kind         text NOT NULL,          -- d30 | d7 | overdue
  sent_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (fee_id, kind)
);
SELECT platform.enable_property_rls('membership.fee_reminders');

-- Lifecycle requests that wait for approval (FR-MBL-06/07/08/11/13).
CREATE TABLE membership.requests (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  membership_id  uuid NOT NULL REFERENCES membership.memberships (id),
  request_type   text NOT NULL CHECK (request_type IN ('pause', 'postpone', 'reactivate', 'upgrade', 'downgrade', 'cancel', 'nominee_change')),
  payload        jsonb NOT NULL DEFAULT '{}'::jsonb,
  reason         text,
  status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'applied')),
  approval_id    uuid,
  channel        text NOT NULL DEFAULT 'back_office',
  decided_at     timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid
);
CREATE INDEX requests_membership ON membership.requests (membership_id, created_at);
SELECT platform.enable_property_rls('membership.requests');
SELECT platform.add_touch_trigger('membership.requests');

-- Online application from the Member Portal / Website (FR-MBL-15).
ALTER TABLE membership.applications DROP CONSTRAINT applications_channel_check;
ALTER TABLE membership.applications ADD CONSTRAINT applications_channel_check CHECK (channel IN ('back_office', 'member_portal', 'website'));

SELECT platform.grant_app('membership');

-- +goose Down
ALTER TABLE membership.applications DROP CONSTRAINT applications_channel_check;
ALTER TABLE membership.applications ADD CONSTRAINT applications_channel_check CHECK (channel IN ('back_office', 'member_portal'));
DROP TABLE membership.requests;
DROP TABLE membership.fee_reminders;
DROP TABLE membership.fees;
ALTER TABLE membership.cards DROP COLUMN replaced_by, DROP COLUMN block_reason, DROP COLUMN blocked_at;
ALTER TABLE membership.cards DROP CONSTRAINT cards_status_check;
ALTER TABLE membership.cards ADD CONSTRAINT cards_status_check CHECK (status IN ('active', 'inactive'));
ALTER TABLE membership.members DROP CONSTRAINT members_status_check;
ALTER TABLE membership.members ADD CONSTRAINT members_status_check CHECK (status IN ('pending', 'active', 'inactive', 'suspended', 'expired'));
ALTER TABLE membership.memberships DROP COLUMN cancel_reason, DROP COLUMN cancelled_at, DROP COLUMN suspended_at, DROP COLUMN suspension_reason,
  DROP COLUMN suspension_kind, DROP COLUMN paused_until, DROP COLUMN paused_from, DROP COLUMN next_fee_due;
ALTER TABLE membership.memberships DROP CONSTRAINT memberships_status_check;
ALTER TABLE membership.memberships ADD CONSTRAINT memberships_status_check CHECK (status IN ('pending', 'active', 'expired', 'inactive'));
ALTER TABLE membership.types DROP COLUMN nominee_change_fee, DROP COLUMN reactivation_fee, DROP COLUMN card_replacement_fee, DROP COLUMN entitlements,
  DROP COLUMN rank, DROP COLUMN grace_days, DROP COLUMN annual_fee;
ALTER TABLE membership.types DROP CONSTRAINT types_category_check;
ALTER TABLE membership.types ADD CONSTRAINT types_category_check CHECK (category IN ('individual', 'family', 'corporate'));
ALTER TABLE membership.programs ALTER COLUMN operational SET DEFAULT false;
