-- HRIS improvement phase B (docs/HRIS_Product_Requirements_UI_Backend_Audit.md
-- §39.4), expand-only: employee lifecycle statuses (draft, suspended),
-- loan / cash advance requests with approval, payment by Finance and
-- repayments outside payroll, the Finance & Accounting posting status of a
-- payroll run, and workforce plans per department.

-- +goose Up

-- §34 Employee: Draft (prepared, not yet hired: no hire history, payroll or
-- attendance until activated) and Suspended (skorsing, a period with a
-- reason). On Leave is derived from the approved leave of the day.
ALTER TABLE hris.employees DROP CONSTRAINT employees_status_check;
ALTER TABLE hris.employees ADD CONSTRAINT employees_status_check CHECK (status IN ('draft', 'active', 'inactive'));
ALTER TABLE hris.employees ADD COLUMN suspended_from date, ADD COLUMN suspended_until date, ADD COLUMN suspension_reason text,
  ADD CONSTRAINT employees_suspension_check CHECK (suspended_until IS NULL OR (suspended_from IS NOT NULL AND suspended_until >= suspended_from));

-- The P0 employee facade knows active / inactive only: a draft is inactive
-- there and stays draft when the facade writes it back.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.employee_to_platform() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF pg_trigger_depth() > 1 THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'DELETE' THEN
    DELETE FROM platform.employees WHERE id = OLD.id;
    RETURN NULL;
  END IF;
  INSERT INTO platform.employees (id, property_id, employee_no, full_name, department_id, job_title, email, phone, join_date, status,
    created_by, updated_by, archived_at)
  VALUES (NEW.id, NEW.property_id, NEW.employee_no, NEW.full_name, NEW.org_unit_id, NEW.job_title, NEW.email, NEW.phone, NEW.join_date,
    CASE WHEN NEW.status = 'draft' THEN 'inactive' ELSE NEW.status END, NEW.created_by, NEW.updated_by, NEW.archived_at)
  ON CONFLICT (id) DO UPDATE SET employee_no = EXCLUDED.employee_no, full_name = EXCLUDED.full_name, department_id = EXCLUDED.department_id,
    job_title = EXCLUDED.job_title, email = EXCLUDED.email, phone = EXCLUDED.phone, join_date = EXCLUDED.join_date, status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION hris.employee_from_platform() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF pg_trigger_depth() > 1 THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'DELETE' THEN
    DELETE FROM hris.employees WHERE id = OLD.id;
    RETURN NULL;
  END IF;
  INSERT INTO hris.employees (id, property_id, employee_no, full_name, org_unit_id, job_title, email, phone, join_date, status,
    employment_status, created_by, updated_by, archived_at)
  VALUES (NEW.id, NEW.property_id, NEW.employee_no, NEW.full_name, NEW.department_id, NEW.job_title, NEW.email, NEW.phone, NEW.join_date,
    NEW.status, 'permanent', NEW.created_by, NEW.updated_by, NEW.archived_at)
  ON CONFLICT (id) DO UPDATE SET employee_no = EXCLUDED.employee_no, full_name = EXCLUDED.full_name, org_unit_id = EXCLUDED.org_unit_id,
    job_title = EXCLUDED.job_title, email = EXCLUDED.email, phone = EXCLUDED.phone, join_date = EXCLUDED.join_date,
    status = CASE WHEN hris.employees.status = 'draft' AND EXCLUDED.status = 'inactive' THEN 'draft' ELSE EXCLUDED.status END,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;
-- +goose StatementEnd

-- §24 Loans and cash advances: requested by HR or by the employee (ESS),
-- approved through the approval engine, paid by Finance (active from then:
-- payroll deducts the installments) and repaid by payroll or outside it.
ALTER TABLE hris.employee_loans DROP CONSTRAINT employee_loans_status_check;
ALTER TABLE hris.employee_loans ADD CONSTRAINT employee_loans_status_check
  CHECK (status IN ('submitted', 'approved', 'rejected', 'active', 'settled', 'cancelled'));
ALTER TABLE hris.employee_loans
  ADD COLUMN number               text,
  ADD COLUMN purpose              text,
  ADD COLUMN request_source       text NOT NULL DEFAULT 'hr' CHECK (request_source IN ('hr', 'ess')),
  ADD COLUMN approval_request_id  uuid,
  ADD COLUMN decided_at           timestamptz,
  ADD COLUMN decision_note        text,
  ADD COLUMN disbursed_on         date,
  ADD COLUMN disbursement_method  text CHECK (disbursement_method IS NULL OR disbursement_method IN ('cash', 'bank_transfer')),
  ADD COLUMN disbursement_ref     text,
  ADD COLUMN disbursed_by         uuid;
CREATE UNIQUE INDEX employee_loans_number_uniq ON hris.employee_loans (property_id, number) WHERE number IS NOT NULL;

CREATE TABLE hris.loan_repayments (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  loan_id       uuid NOT NULL REFERENCES hris.employee_loans (id),
  paid_on       date NOT NULL,
  amount        numeric(18,2) NOT NULL CHECK (amount > 0),
  method        text NOT NULL CHECK (method IN ('cash', 'bank_transfer')),
  reference     text,
  notes         text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);
SELECT platform.enable_property_rls('hris.loan_repayments');
CREATE INDEX loan_repayments_loan_idx ON hris.loan_repayments (loan_id, paid_on);

-- §19 Payroll → Finance: the outcome of the posting events in Accounting
-- (accounting.journal_posted / accounting.posting_exception), so the run
-- shows Posted to Finance or Posting Failed without reading accounting.
ALTER TABLE hris.payroll_runs
  ADD COLUMN posting_event_id   uuid,
  ADD COLUMN payment_event_id   uuid,
  ADD COLUMN finance_status     text NOT NULL DEFAULT 'not_posted' CHECK (finance_status IN ('not_posted', 'pending', 'posted', 'failed')),
  ADD COLUMN finance_message    text,
  ADD COLUMN finance_journals   text[] NOT NULL DEFAULT '{}',
  ADD COLUMN finance_failed_event text,
  ADD COLUMN finance_failed_journal uuid,
  ADD COLUMN finance_updated_at timestamptz;
UPDATE hris.payroll_runs SET finance_status = 'pending' WHERE status IN ('posted', 'paid');

-- §8 Workforce planning: the headcount a department needs per position and
-- shift for a period and scenario (weekend, holiday, tournament, event,
-- season), compared with the available and scheduled employees.
CREATE TABLE hris.workforce_plans (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  number         text NOT NULL,
  name           text NOT NULL,
  org_unit_id    uuid NOT NULL REFERENCES hris.org_units (id),
  period_start   date NOT NULL,
  period_end     date NOT NULL,
  scenario       text NOT NULL DEFAULT 'normal'
                   CHECK (scenario IN ('normal', 'weekend', 'holiday', 'peak_season', 'low_season', 'tournament', 'event')),
  status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (property_id, number),
  CHECK (period_end >= period_start AND period_end <= period_start + 92)
);
SELECT platform.enable_property_rls('hris.workforce_plans');
SELECT platform.add_touch_trigger('hris.workforce_plans');

CREATE TABLE hris.workforce_plan_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  plan_id            uuid NOT NULL REFERENCES hris.workforce_plans (id) ON DELETE CASCADE,
  line_no            int NOT NULL,
  position_id        uuid REFERENCES hris.positions (id),
  shift_template_id  uuid REFERENCES hris.shift_templates (id),
  work_date          date,
  required           int NOT NULL CHECK (required BETWEEN 0 AND 500),
  notes              text,
  UNIQUE (plan_id, line_no)
);
SELECT platform.enable_property_rls('hris.workforce_plan_lines');

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();

-- +goose Down
-- (the facade functions keep their draft mapping: harmless without drafts)
DROP TABLE hris.workforce_plan_lines, hris.workforce_plans, hris.loan_repayments;
UPDATE hris.employee_loans SET status = 'cancelled' WHERE status IN ('submitted', 'approved', 'rejected');
UPDATE hris.employees SET status = 'inactive' WHERE status = 'draft';
ALTER TABLE hris.payroll_runs DROP COLUMN posting_event_id, DROP COLUMN payment_event_id, DROP COLUMN finance_status, DROP COLUMN finance_message,
  DROP COLUMN finance_journals, DROP COLUMN finance_failed_event, DROP COLUMN finance_failed_journal, DROP COLUMN finance_updated_at;
DROP INDEX hris.employee_loans_number_uniq;
ALTER TABLE hris.employee_loans DROP COLUMN number, DROP COLUMN purpose, DROP COLUMN request_source, DROP COLUMN approval_request_id,
  DROP COLUMN decided_at, DROP COLUMN decision_note, DROP COLUMN disbursed_on, DROP COLUMN disbursement_method, DROP COLUMN disbursement_ref,
  DROP COLUMN disbursed_by;
ALTER TABLE hris.employee_loans DROP CONSTRAINT employee_loans_status_check;
ALTER TABLE hris.employee_loans ADD CONSTRAINT employee_loans_status_check CHECK (status IN ('active', 'settled', 'cancelled'));
ALTER TABLE hris.employees DROP CONSTRAINT employees_suspension_check, DROP COLUMN suspended_from, DROP COLUMN suspended_until,
  DROP COLUMN suspension_reason;
ALTER TABLE hris.employees DROP CONSTRAINT employees_status_check;
ALTER TABLE hris.employees ADD CONSTRAINT employees_status_check CHECK (status IN ('active', 'inactive'));
