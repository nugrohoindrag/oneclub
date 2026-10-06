-- PRD P5 gap closure (non-payroll): device clock-in of partner caddies and
-- instructors (FR-ATT-08, FR-INT-P5-01) and the HR migration reconciliation
-- with HR Manager and Finance Manager sign-off (FR-MIG-P5-05).

-- +goose Up

-- Attendance profile of a partner (caddy or instructor, not an employee):
-- the user number on the biometric devices and the written consent. No
-- biometric template is ever stored (PRD P5 §6 #9). Device user numbers
-- share one namespace with the employees' (hris.attendance_profiles):
-- partners get numbers from 90001 upwards.
CREATE TABLE hris.partner_attendance_profiles (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  holder_kind            text NOT NULL CHECK (holder_kind IN ('caddy', 'instructor')),
  partner_id             uuid NOT NULL,
  partner_name           text NOT NULL,
  device_user_no         text NOT NULL,
  biometric_consent      boolean NOT NULL DEFAULT false,
  consent_signed_on      date,
  consent_withdrawn_at   timestamptz,
  enrolled_at            timestamptz,
  removal_requested_at   timestamptz,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, holder_kind, partner_id),
  UNIQUE (property_id, device_user_no)
);
SELECT platform.enable_property_rls('hris.partner_attendance_profiles');
SELECT platform.add_touch_trigger('hris.partner_attendance_profiles');

-- Clock events of partners read from the biometric devices (bridge agent
-- or the mock adapter). The device event id makes resubmissions
-- idempotent (FR-ATT-07); a caddy's first clock-in of the day marks the
-- caddy present in Caddy Master (golf, through the outbox event
-- hris.partner_attendance_recorded).
CREATE TABLE hris.partner_attendance_events (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  profile_id        uuid NOT NULL REFERENCES hris.partner_attendance_profiles (id),
  holder_kind       text NOT NULL CHECK (holder_kind IN ('caddy', 'instructor')),
  partner_id        uuid NOT NULL,
  work_date         date NOT NULL,
  direction         text NOT NULL CHECK (direction IN ('in', 'out')),
  occurred_at       timestamptz NOT NULL,
  method            text NOT NULL CHECK (method IN ('fingerprint', 'face_recognition')),
  device_id         uuid NOT NULL REFERENCES hris.attendance_devices (id),
  client_event_id   text NOT NULL,
  offline           boolean NOT NULL DEFAULT false,
  received_at       timestamptz NOT NULL DEFAULT now(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (profile_id, client_event_id)
);
SELECT platform.enable_property_rls('hris.partner_attendance_events');
SELECT platform.add_touch_trigger('hris.partner_attendance_events');
CREATE INDEX partner_attendance_events_day_idx ON hris.partner_attendance_events (property_id, work_date);

-- HR migration reconciliation (FR-MIG-P5-05): control totals of the club's
-- legacy HR system (headcount per org unit, leave balances per leave type
-- or employee at cutover) against OneClub, signed off by the HR Manager and
-- the Finance Manager. Payroll totals are added by the payroll area
-- through the metric registry (hris.RegisterReconciliationMetric).
CREATE TABLE hris.migration_reconciliations (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  number              text NOT NULL,
  cutover_date        date NOT NULL,
  legacy_system       text NOT NULL,
  notes               text,
  lines               jsonb NOT NULL DEFAULT '[]',
  checks              int NOT NULL DEFAULT 0,
  mismatches          int NOT NULL DEFAULT 0,
  status              text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'hr_signed', 'finance_signed', 'signed_off', 'cancelled')),
  calculated_at       timestamptz NOT NULL DEFAULT now(),
  hr_signed_by        uuid,
  hr_signed_name      text,
  hr_signed_at        timestamptz,
  hr_note             text,
  finance_signed_by   uuid,
  finance_signed_name text,
  finance_signed_at   timestamptz,
  finance_note        text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('hris.migration_reconciliations');
SELECT platform.add_touch_trigger('hris.migration_reconciliations');

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();

-- +goose Down
DROP TABLE hris.migration_reconciliations, hris.partner_attendance_events, hris.partner_attendance_profiles;
