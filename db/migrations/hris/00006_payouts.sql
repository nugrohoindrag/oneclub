-- PRD P5 payouts & distributions: EP-11 Service Charge Distribution, EP-12
-- Sales Commission & Bonus Payout, EP-13 caddy and EP-14 partner instructor
-- payout runs (non-employee workforce, §16 #4).
--
-- hris never writes golf / sportclub / crm tables: approved caddy
-- settlements (H1), instructor fees (H2) and commission statements (H3)
-- reach hris through their outbox events and are kept in event-maintained
-- tables here. Service charge lines, commission and bonus lines are payroll
-- inputs (hris.RegisterPayrollInputSource): the payroll engine pays them and
-- marks them consumed. Dates are local business dates of the property.

-- +goose Up

-- Partner payout profile (FR-CDY-01, FR-INS-HR-01): partnership status,
-- tax id, bank account and BPJS BPU enrolment of a golf caddy or a partner
-- instructor of the sport club (partner_id is their id there).
CREATE TABLE hris.partner_profiles (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  partner_kind         text NOT NULL CHECK (partner_kind IN ('caddy', 'instructor')),
  partner_id           uuid NOT NULL,
  partner_code         text,
  partner_name         text NOT NULL,
  partnership_status   text NOT NULL DEFAULT 'active' CHECK (partnership_status IN ('active', 'suspended', 'ended')),
  nik                  text,
  npwp                 text,
  bank_code            text,
  bank_name            text,
  bank_account_no      text,
  bank_account_name    text,
  payment_method       text NOT NULL DEFAULT 'bank_transfer' CHECK (payment_method IN ('bank_transfer', 'cash')),
  bpu_enrolled         boolean NOT NULL DEFAULT false,
  bpu_no               text,
  bpu_declared_income  numeric(19,4) CHECK (bpu_declared_income IS NULL OR bpu_declared_income >= 0),
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz
);
SELECT platform.enable_property_rls('hris.partner_profiles');
SELECT platform.add_touch_trigger('hris.partner_profiles');
CREATE UNIQUE INDEX partner_profiles_partner_uniq ON hris.partner_profiles (property_id, partner_kind, partner_id) WHERE archived_at IS NULL;

-- Approved caddy settlements and instructor fees (contracts H1, H2), kept
-- from golf.caddy_settlement_approved / sportclub.instructor_fee_approved.
-- channel payout: paid by a payout run; payroll: an employee instructor's
-- fee, paid by payroll (payroll input source instructor_fee).
CREATE TABLE hris.payout_sources (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  kind              text NOT NULL CHECK (kind IN ('caddy', 'instructor')),
  source_type       text NOT NULL CHECK (source_type IN ('golf.caddy_settlement', 'sportclub.instructor_fee')),
  source_id         uuid NOT NULL,
  number            text NOT NULL,
  partner_id        uuid NOT NULL,
  partner_name      text NOT NULL,
  channel           text NOT NULL DEFAULT 'payout' CHECK (channel IN ('payout', 'payroll')),
  employee_id       uuid REFERENCES hris.employees (id),
  period_start      date NOT NULL,
  period_end        date NOT NULL,
  units             int NOT NULL DEFAULT 0,
  fee               numeric(19,4) NOT NULL DEFAULT 0,
  tips              numeric(19,4) NOT NULL DEFAULT 0,
  deductions        numeric(19,4) NOT NULL DEFAULT 0,
  gross             numeric(19,4) NOT NULL DEFAULT 0,
  total             numeric(19,4) NOT NULL DEFAULT 0,
  status            text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'claimed', 'paid', 'cancelled')),
  payout_run_id     uuid,
  payout_line_id    uuid,
  payroll_run_id    uuid,
  consumed_at       timestamptz,
  event_id          uuid,
  received_at       timestamptz NOT NULL DEFAULT now(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id),
  CHECK (period_end >= period_start),
  CHECK (channel = 'payout' OR employee_id IS NOT NULL)
);
SELECT platform.enable_property_rls('hris.payout_sources');
SELECT platform.add_touch_trigger('hris.payout_sources');
CREATE INDEX payout_sources_open_idx ON hris.payout_sources (property_id, kind, status, period_end);
CREATE INDEX payout_sources_partner_idx ON hris.payout_sources (partner_id);

-- Payout runs (EP-13 caddy, semi-monthly; EP-14 partner instructors,
-- monthly): Draft → Calculated → (approval) → Approved (posted to the
-- ledger: hris.payout_posted) → Paid (hris.payout_paid). The run keeps the
-- policy versions it used (FR-POL-P5-07).
CREATE TABLE hris.payout_runs (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  kind                 text NOT NULL CHECK (kind IN ('caddy', 'instructor')),
  period_start         date NOT NULL,
  period_end           date NOT NULL,
  pay_date             date NOT NULL,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'calculated', 'pending_approval', 'approved', 'paid', 'cancelled')),
  policy_versions      jsonb NOT NULL DEFAULT '[]'::jsonb,
  tax_note             text,
  partners             int NOT NULL DEFAULT 0,
  held_partners        int NOT NULL DEFAULT 0,
  gross                numeric(19,4) NOT NULL DEFAULT 0,
  source_deductions    numeric(19,4) NOT NULL DEFAULT 0,
  pph21                numeric(19,4) NOT NULL DEFAULT 0,
  bpu                  numeric(19,4) NOT NULL DEFAULT 0,
  other_deductions     numeric(19,4) NOT NULL DEFAULT 0,
  net                  numeric(19,4) NOT NULL DEFAULT 0,
  bank_amount          numeric(19,4) NOT NULL DEFAULT 0,
  cash_amount          numeric(19,4) NOT NULL DEFAULT 0,
  approval_request_id  uuid,
  calculated_at        timestamptz,
  calculated_by        uuid,
  approved_at          timestamptz,
  posted_at            timestamptz,
  paid_at              timestamptz,
  paid_on              date,
  paid_by              uuid,
  paid_reference       text,
  bank_file_at         timestamptz,
  cancelled_at         timestamptz,
  cancel_reason        text,
  decision_note        text,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number),
  CHECK (period_end >= period_start)
);
SELECT platform.enable_property_rls('hris.payout_runs');
SELECT platform.add_touch_trigger('hris.payout_runs');
CREATE INDEX payout_runs_period_idx ON hris.payout_runs (property_id, kind, period_end DESC);

-- One partner of a run: gross (settlements / fees), PPh 21 non-employee,
-- BPJS BPU, deductions, net, and a snapshot of the payment details used by
-- the bank file (sensitive: masked without hris.partner_profile.view_sensitive).
CREATE TABLE hris.payout_lines (
  id                  uuid PRIMARY KEY,
  run_id              uuid NOT NULL REFERENCES hris.payout_runs (id) ON DELETE CASCADE,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  partner_id          uuid NOT NULL,
  partner_code        text,
  partner_name        text NOT NULL,
  profile_id          uuid REFERENCES hris.partner_profiles (id),
  payment_method      text NOT NULL DEFAULT 'bank_transfer' CHECK (payment_method IN ('bank_transfer', 'cash')),
  bank_code           text,
  bank_name           text,
  bank_account_no     text,
  bank_account_name   text,
  has_tax_id          boolean NOT NULL DEFAULT false,
  bpu_enrolled        boolean NOT NULL DEFAULT false,
  monthly_due         boolean NOT NULL DEFAULT true,
  units               int NOT NULL DEFAULT 0,
  fee                 numeric(19,4) NOT NULL DEFAULT 0,
  tips                numeric(19,4) NOT NULL DEFAULT 0,
  gross               numeric(19,4) NOT NULL DEFAULT 0,
  source_deductions   numeric(19,4) NOT NULL DEFAULT 0,
  ytd_tax_base        numeric(19,4) NOT NULL DEFAULT 0,
  tax_base            numeric(19,4) NOT NULL DEFAULT 0,
  pph21_rate          numeric(9,4) NOT NULL DEFAULT 0,
  pph21               numeric(19,4) NOT NULL DEFAULT 0,
  bpu_jkk             numeric(19,4) NOT NULL DEFAULT 0,
  bpu_jkm             numeric(19,4) NOT NULL DEFAULT 0,
  other_deductions    numeric(19,4) NOT NULL DEFAULT 0,
  deductions          jsonb NOT NULL DEFAULT '[]'::jsonb,
  net                 numeric(19,4) NOT NULL DEFAULT 0,
  sources             jsonb NOT NULL DEFAULT '[]'::jsonb,
  status              text NOT NULL DEFAULT 'payable' CHECK (status IN ('payable', 'paid')),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, partner_id)
);
SELECT platform.enable_property_rls('hris.payout_lines');
SELECT platform.add_touch_trigger('hris.payout_lines');
CREATE INDEX payout_lines_partner_idx ON hris.payout_lines (partner_id, run_id);

-- Service charge distribution of a month's pool (EP-11, §16 #5) and its
-- lines per employee (every employee of the period, eligible or not, with
-- the reason). Approved lines are payroll inputs (SERVICE_CHARGE).
CREATE TABLE hris.service_charge_distributions (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  number                 text NOT NULL,
  year                   int NOT NULL CHECK (year BETWEEN 2000 AND 2100),
  month                  int NOT NULL CHECK (month BETWEEN 1 AND 12),
  period_start           date NOT NULL,
  period_end             date NOT NULL,
  pay_period             text NOT NULL CHECK (pay_period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  pool_id                uuid,
  pool_status            text,
  collected              numeric(19,4) NOT NULL DEFAULT 0,
  reserve_percent        numeric(9,4) NOT NULL DEFAULT 0,
  distributed_percent    numeric(9,4) NOT NULL DEFAULT 0,
  reserve                numeric(19,4) NOT NULL DEFAULT 0,
  distributable          numeric(19,4) NOT NULL DEFAULT 0,
  distributed            numeric(19,4) NOT NULL DEFAULT 0,
  undistributed          numeric(19,4) NOT NULL DEFAULT 0,
  rounding_difference    numeric(19,4) NOT NULL DEFAULT 0,
  rounding_account_code  text,
  method                 text NOT NULL DEFAULT 'equal',
  attendance_factor      boolean NOT NULL DEFAULT true,
  redistribute           boolean NOT NULL DEFAULT true,
  eligible               int NOT NULL DEFAULT 0,
  excluded               int NOT NULL DEFAULT 0,
  policy_version         int NOT NULL DEFAULT 0,
  status                 text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'simulated', 'pending_approval', 'approved', 'paid', 'cancelled')),
  approval_request_id    uuid,
  simulated_at           timestamptz,
  simulated_by           uuid,
  approved_at            timestamptz,
  posted_at              timestamptz,
  paid_at                timestamptz,
  cancelled_at           timestamptz,
  cancel_reason          text,
  decision_note          text,
  notes                  text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('hris.service_charge_distributions');
SELECT platform.add_touch_trigger('hris.service_charge_distributions');
CREATE UNIQUE INDEX service_charge_distributions_month_uniq ON hris.service_charge_distributions (property_id, year, month) WHERE status <> 'cancelled';

CREATE TABLE hris.service_charge_lines (
  id                  uuid PRIMARY KEY,
  distribution_id     uuid NOT NULL REFERENCES hris.service_charge_distributions (id) ON DELETE CASCADE,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  employee_id         uuid NOT NULL REFERENCES hris.employees (id),
  employee_no         text NOT NULL,
  full_name           text NOT NULL,
  org_unit_code       text,
  org_unit_name       text,
  grade_code          text,
  employment_status   text NOT NULL,
  worker_category     text NOT NULL,
  warning_level       int NOT NULL DEFAULT 0,
  scheduled_days      int NOT NULL DEFAULT 0,
  present_days        numeric(9,2) NOT NULL DEFAULT 0,
  unpaid_leave_days   numeric(9,2) NOT NULL DEFAULT 0,
  eligible            boolean NOT NULL DEFAULT false,
  exclusion_reason    text,
  share_group         text,
  points              numeric(9,4) NOT NULL DEFAULT 0,
  attendance_factor   numeric(9,4) NOT NULL DEFAULT 0,
  base_share          numeric(19,4) NOT NULL DEFAULT 0,
  attendance_amount   numeric(19,4) NOT NULL DEFAULT 0,
  redistributed       numeric(19,4) NOT NULL DEFAULT 0,
  amount              numeric(19,4) NOT NULL DEFAULT 0,
  payroll_run_id      uuid,
  consumed_at         timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (distribution_id, employee_id)
);
SELECT platform.enable_property_rls('hris.service_charge_lines');
SELECT platform.add_touch_trigger('hris.service_charge_lines');
CREATE INDEX service_charge_lines_employee_idx ON hris.service_charge_lines (employee_id);

-- Approved commission statements of P3 (contract H3,
-- crm.commission_approved) to pay with payroll: earning COMMISSION and the
-- clawback deduction (FR-CMS-HR-01/02); the statement turns Paid when the
-- payroll run that pays it is posted (FR-CMS-HR-03).
CREATE TABLE hris.commission_payouts (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  statement_id     uuid NOT NULL UNIQUE,
  number           text NOT NULL,
  user_id          uuid,
  user_name        text,
  employee_id      uuid REFERENCES hris.employees (id),
  period           text NOT NULL,
  earned           numeric(19,4) NOT NULL DEFAULT 0,
  clawback         numeric(19,4) NOT NULL DEFAULT 0,
  adjustments      numeric(19,4) NOT NULL DEFAULT 0,
  total            numeric(19,4) NOT NULL DEFAULT 0,
  earning          numeric(19,4) NOT NULL DEFAULT 0,
  deduction        numeric(19,4) NOT NULL DEFAULT 0,
  currency         text NOT NULL DEFAULT 'IDR',
  irregular        boolean NOT NULL DEFAULT true,
  status           text NOT NULL DEFAULT 'ready' CHECK (status IN ('unmatched', 'ready', 'paid', 'cancelled')),
  payroll_run_id   uuid,
  consumed_at      timestamptz,
  event_id         uuid,
  received_at      timestamptz NOT NULL DEFAULT now(),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.commission_payouts');
SELECT platform.add_touch_trigger('hris.commission_payouts');
CREATE INDEX commission_payouts_employee_idx ON hris.commission_payouts (employee_id, status);

-- Bonus programmes (EP-12, FR-PAY-05): manual or review-based bonus lines
-- (BONUS, irregular income) approved through the approval engine.
CREATE TABLE hris.bonus_programmes (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  name                 text NOT NULL,
  bonus_type           text NOT NULL DEFAULT 'performance' CHECK (bonus_type IN ('performance', 'annual', 'incentive', 'other')),
  pay_period           text NOT NULL CHECK (pay_period ~ '^\d{4}-(0[1-9]|1[0-2])$'),
  review_cycle_id      uuid,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'paid', 'cancelled')),
  employees            int NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  approval_request_id  uuid,
  approved_at          timestamptz,
  decision_note        text,
  cancelled_at         timestamptz,
  cancel_reason        text,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('hris.bonus_programmes');
SELECT platform.add_touch_trigger('hris.bonus_programmes');

CREATE TABLE hris.bonus_lines (
  id               uuid PRIMARY KEY,
  programme_id     uuid NOT NULL REFERENCES hris.bonus_programmes (id) ON DELETE CASCADE,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  employee_id      uuid NOT NULL REFERENCES hris.employees (id),
  employee_no      text NOT NULL,
  full_name        text NOT NULL,
  amount           numeric(19,4) NOT NULL CHECK (amount > 0),
  basis            text,
  note             text,
  payroll_run_id   uuid,
  consumed_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (programme_id, employee_id)
);
SELECT platform.enable_property_rls('hris.bonus_lines');
SELECT platform.add_touch_trigger('hris.bonus_lines');
CREATE INDEX bonus_lines_employee_idx ON hris.bonus_lines (employee_id);

-- Report role hardening of the payouts area (UU PDP): partner tax ids and
-- bank accounts, and the bank details printed in the bank file, are not
-- readable by the report role. Called by hris.harden_report_role().
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.harden_report_role_payouts() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON hris.partner_profiles, hris.payout_lines FROM %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, partner_kind, partner_id, partner_code, partner_name, partnership_status, payment_method,
    bpu_enrolled, created_at, updated_at, archived_at) ON hris.partner_profiles TO %I', rep);
  EXECUTE format('GRANT SELECT (id, run_id, property_id, partner_id, partner_code, partner_name, profile_id, payment_method, has_tax_id,
    bpu_enrolled, monthly_due, units, fee, tips, gross, source_deductions, ytd_tax_base, tax_base, pph21_rate, pph21, bpu_jkk, bpu_jkm,
    other_deductions, deductions, net, sources, status, created_at, updated_at) ON hris.payout_lines TO %I', rep);
END $$;
-- +goose StatementEnd

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();
SELECT hris.harden_report_role_payouts();

-- +goose Down
DROP TABLE hris.bonus_lines, hris.bonus_programmes, hris.commission_payouts, hris.service_charge_lines, hris.service_charge_distributions,
  hris.payout_lines, hris.payout_runs, hris.payout_sources, hris.partner_profiles;
DROP FUNCTION hris.harden_report_role_payouts();
