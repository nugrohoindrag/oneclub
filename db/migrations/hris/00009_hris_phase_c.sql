-- HRIS improvement phase C (docs/HRIS_Product_Requirements_UI_Backend_Audit.md
-- §39.5): reimbursement claims, benefit plans and enrollments (employee
-- contributions deducted by payroll), timesheets and open shifts of the
-- roster. New tables only.

-- +goose Up

-- §24 Reimbursement: categories with an optional GL expense account, a
-- ceiling per claim and whether a receipt is required.
CREATE TABLE hris.reimbursement_categories (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  code                  text NOT NULL,
  name                  text NOT NULL,
  expense_account_code  text,
  max_amount            numeric(18,2) CHECK (max_amount IS NULL OR max_amount > 0),
  receipt_required      boolean NOT NULL DEFAULT true,
  description           text,
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  archived_at           timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.reimbursement_categories');
SELECT platform.add_touch_trigger('hris.reimbursement_categories');

-- Claim: submitted → approved / rejected (approval engine) → sent to
-- Finance → paid (hris.reimbursement_paid: Dr expense / Cr cash or bank).
CREATE TABLE hris.reimbursements (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  employee_id          uuid NOT NULL REFERENCES hris.employees (id),
  category_id          uuid NOT NULL REFERENCES hris.reimbursement_categories (id),
  expense_date         date NOT NULL,
  amount               numeric(18,2) NOT NULL CHECK (amount > 0),
  description          text NOT NULL,
  file_id              uuid REFERENCES platform.files (id),
  request_source       text NOT NULL DEFAULT 'ess' CHECK (request_source IN ('hr', 'ess')),
  status               text NOT NULL DEFAULT 'submitted'
                         CHECK (status IN ('submitted', 'approved', 'rejected', 'sent_to_finance', 'paid', 'cancelled')),
  approval_request_id  uuid,
  decided_at           timestamptz,
  decision_note        text,
  sent_at              timestamptz,
  sent_by              uuid,
  paid_on              date,
  payment_method       text CHECK (payment_method IS NULL OR payment_method IN ('cash', 'bank_transfer')),
  payment_ref          text,
  paid_by              uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('hris.reimbursements');
SELECT platform.add_touch_trigger('hris.reimbursements');
CREATE INDEX reimbursements_employee_idx ON hris.reimbursements (employee_id, expense_date);
CREATE INDEX reimbursements_status_idx ON hris.reimbursements (property_id, status);

-- §24 Benefits: plans with eligibility and contributions; enrollments with
-- an effective period; the employee contribution of each payroll period is
-- a payroll input (deduction) consumed by the posted run.
CREATE TABLE hris.benefit_plans (
  id                       uuid PRIMARY KEY,
  property_id              uuid NOT NULL REFERENCES platform.properties (id),
  code                     text NOT NULL,
  name                     text NOT NULL,
  benefit_type             text NOT NULL DEFAULT 'health_insurance'
                             CHECK (benefit_type IN ('health_insurance', 'life_insurance', 'pension', 'allowance', 'facility', 'other')),
  provider                 text,
  employer_contribution    numeric(18,2) NOT NULL DEFAULT 0 CHECK (employer_contribution >= 0),
  employee_contribution    numeric(18,2) NOT NULL DEFAULT 0 CHECK (employee_contribution >= 0),
  eligible_statuses        text[] NOT NULL DEFAULT '{}',
  eligible_categories      text[] NOT NULL DEFAULT '{}',
  min_service_months       int NOT NULL DEFAULT 0 CHECK (min_service_months BETWEEN 0 AND 600),
  description              text,
  status                   text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid,
  updated_at               timestamptz NOT NULL DEFAULT now(),
  updated_by               uuid,
  archived_at              timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.benefit_plans');
SELECT platform.add_touch_trigger('hris.benefit_plans');

CREATE TABLE hris.benefit_enrollments (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  employee_id            uuid NOT NULL REFERENCES hris.employees (id),
  plan_id                uuid NOT NULL REFERENCES hris.benefit_plans (id),
  effective_from         date NOT NULL,
  effective_to           date,
  employer_contribution  numeric(18,2) NOT NULL DEFAULT 0 CHECK (employer_contribution >= 0),
  employee_contribution  numeric(18,2) NOT NULL DEFAULT 0 CHECK (employee_contribution >= 0),
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended', 'cancelled')),
  notes                  text,
  end_reason             text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  CHECK (effective_to IS NULL OR effective_to >= effective_from)
);
SELECT platform.enable_property_rls('hris.benefit_enrollments');
SELECT platform.add_touch_trigger('hris.benefit_enrollments');
CREATE UNIQUE INDEX benefit_enrollments_open_uniq ON hris.benefit_enrollments (employee_id, plan_id) WHERE status = 'active';

CREATE TABLE hris.benefit_deductions (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  enrollment_id  uuid NOT NULL REFERENCES hris.benefit_enrollments (id),
  period_start   date NOT NULL,
  amount         numeric(18,2) NOT NULL CHECK (amount > 0),
  run_id         uuid REFERENCES hris.payroll_runs (id),
  consumed_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (enrollment_id, period_start)
);
SELECT platform.enable_property_rls('hris.benefit_deductions');

-- §16 Timesheets: actual working time per day and activity for a period,
-- submitted by the employee and approved like the other time requests (the
-- supervisor in ESS → Approvals, then the approval workflow).
CREATE TABLE hris.timesheets (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  employee_id          uuid NOT NULL REFERENCES hris.employees (id),
  period_start         date NOT NULL,
  period_end           date NOT NULL,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'approved', 'rejected', 'cancelled')),
  total_hours          numeric(7,2) NOT NULL DEFAULT 0,
  notes                text,
  approval_request_id  uuid,
  approval_step        int,
  submitted_at         timestamptz,
  decided_by           uuid,
  decided_at           timestamptz,
  decision_note        text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number),
  CHECK (period_end >= period_start AND period_end <= period_start + 30)
);
SELECT platform.enable_property_rls('hris.timesheets');
SELECT platform.add_touch_trigger('hris.timesheets');
CREATE INDEX timesheets_employee_idx ON hris.timesheets (employee_id, period_start);

CREATE TABLE hris.timesheet_entries (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  timesheet_id  uuid NOT NULL REFERENCES hris.timesheets (id) ON DELETE CASCADE,
  line_no       int NOT NULL,
  work_date     date NOT NULL,
  hours         numeric(5,2) NOT NULL CHECK (hours > 0 AND hours <= 24),
  activity      text NOT NULL,
  reference     text,
  notes         text,
  UNIQUE (timesheet_id, line_no)
);
SELECT platform.enable_property_rls('hris.timesheet_entries');
ALTER TABLE hris.request_approvals DROP CONSTRAINT request_approvals_request_kind_check;
ALTER TABLE hris.request_approvals ADD CONSTRAINT request_approvals_request_kind_check
  CHECK (request_kind IN ('leave', 'permission', 'overtime', 'shift_swap', 'attendance_correction', 'timesheet'));

-- §13 Open shift: an unassigned shift of a schedule that employees of the
-- unit claim in ESS; the approved claim becomes the assignment.
CREATE TABLE hris.open_shifts (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  schedule_id        uuid NOT NULL REFERENCES hris.schedules (id) ON DELETE CASCADE,
  work_date          date NOT NULL,
  shift_template_id  uuid NOT NULL REFERENCES hris.shift_templates (id),
  position_id        uuid REFERENCES hris.positions (id),
  slots              int NOT NULL CHECK (slots BETWEEN 1 AND 50),
  filled             int NOT NULL DEFAULT 0 CHECK (filled >= 0),
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'filled', 'cancelled')),
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  CHECK (filled <= slots)
);
SELECT platform.enable_property_rls('hris.open_shifts');
SELECT platform.add_touch_trigger('hris.open_shifts');
CREATE INDEX open_shifts_schedule_idx ON hris.open_shifts (schedule_id, work_date);

CREATE TABLE hris.open_shift_claims (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  open_shift_id  uuid NOT NULL REFERENCES hris.open_shifts (id) ON DELETE CASCADE,
  employee_id    uuid NOT NULL REFERENCES hris.employees (id),
  status         text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'approved', 'rejected', 'withdrawn')),
  note           text,
  decided_by     uuid,
  decided_at     timestamptz,
  decision_note  text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.open_shift_claims');
CREATE UNIQUE INDEX open_shift_claims_open_uniq ON hris.open_shift_claims (open_shift_id, employee_id) WHERE status IN ('requested', 'approved');

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();

-- +goose Down
DELETE FROM hris.request_approvals WHERE request_kind = 'timesheet';
ALTER TABLE hris.request_approvals DROP CONSTRAINT request_approvals_request_kind_check;
ALTER TABLE hris.request_approvals ADD CONSTRAINT request_approvals_request_kind_check
  CHECK (request_kind IN ('leave', 'permission', 'overtime', 'shift_swap', 'attendance_correction'));
DROP TABLE hris.open_shift_claims, hris.open_shifts, hris.timesheet_entries, hris.timesheets,
  hris.benefit_deductions, hris.benefit_enrollments, hris.benefit_plans, hris.reimbursements, hris.reimbursement_categories;
