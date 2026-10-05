-- PRD P5 payroll (EP-09 Payroll Engine, EP-10 PPh 21 & BPJS, EP-15 Payroll
-- Accounting, Payment & Payslip, the payroll parts of EP-24/26/27/28/29).
--
-- Mode A (§16 #1): the full Indonesian payroll runs in OneClub. Salary
-- structures per grade / position / employee are versioned with an
-- effective date; statutory rates (PTKP, PPh 21 TER, Article 17, severance
-- tax, BPJS rates and wage caps) are versioned rate sets; every payroll
-- run stores the policy versions and the rate set it used (FR-POL-P5-07).
-- A run goes Draft → Calculated → (Submitted) → Approved → Posted → Paid;
-- an approved run is immutable, corrections go through adjustment runs.
-- Amounts are numeric(18,2) in the property currency; dates are local
-- business dates.

-- +goose Up

-- Statutory rate sets (FR-TAX-HR-01/03, /hris/statutory-rates), national
-- and therefore instance-wide. The set in force on the last day of a
-- payroll period applies; activation needs a second person (FR-PPY-05) and
-- the tax consultant's verification is recorded (FR-TAX-HR-05).
CREATE TABLE hris.statutory_rate_sets (
  id                   uuid PRIMARY KEY,
  code                 text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name                 text NOT NULL,
  effective_from       date NOT NULL,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'inactive')),
  rates                jsonb NOT NULL,
  regulation           text,
  verification_status  text NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified', 'verified')),
  verified_by          uuid,
  verified_at          timestamptz,
  verification_note    text,
  activated_by         uuid,
  activated_at         timestamptz,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid
);
SELECT platform.add_touch_trigger('hris.statutory_rate_sets');
CREATE UNIQUE INDEX statutory_rate_sets_active_uniq ON hris.statutory_rate_sets (effective_from) WHERE status = 'active';

-- Pay components (FR-PAY-01): earnings and deductions with their tax and
-- wage-basis treatment. Loaded from the Payroll Configuration components
-- ("Load defaults") and extended by the club.
CREATE TABLE hris.pay_components (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name            text NOT NULL,
  name_id         text,
  kind            text NOT NULL CHECK (kind IN ('earning', 'deduction')),
  category        text NOT NULL DEFAULT 'allowance' CHECK (category IN ('basic', 'allowance', 'overtime', 'service_charge', 'commission', 'bonus', 'thr',
                    'severance', 'leave_encashment', 'absence', 'loan', 'deduction', 'other')),
  taxable         boolean NOT NULL DEFAULT true,
  irregular       boolean NOT NULL DEFAULT false,
  fixed           boolean NOT NULL DEFAULT false,
  pre_tax         boolean NOT NULL DEFAULT false,
  prorate         boolean NOT NULL DEFAULT false,
  calc_method     text NOT NULL DEFAULT 'fixed' CHECK (calc_method IN ('fixed', 'per_present_day', 'percent_of_basic')),
  default_amount  numeric(18,2) CHECK (default_amount IS NULL OR default_amount >= 0),
  sort_order      int NOT NULL DEFAULT 100,
  description     text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code),
  CHECK (kind = 'deduction' OR NOT pre_tax),
  CHECK (kind = 'earning' OR NOT fixed)
);
SELECT platform.enable_property_rls('hris.pay_components');
SELECT platform.add_touch_trigger('hris.pay_components');

-- Salary structures (FR-PAY-01): component lines per grade, position or
-- employee, effective-dated; a revision is a new version of the same code
-- (versions stay for audit and retro calculations).
CREATE TABLE hris.salary_structures (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  version         int NOT NULL DEFAULT 1 CHECK (version >= 1),
  name            text NOT NULL,
  scope           text NOT NULL CHECK (scope IN ('grade', 'position', 'employee')),
  grade_id        uuid REFERENCES hris.grades (id),
  position_id     uuid REFERENCES hris.positions (id),
  employee_id     uuid REFERENCES hris.employees (id),
  effective_from  date NOT NULL,
  status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'inactive')),
  lines           jsonb NOT NULL DEFAULT '[]'::jsonb,
  previous_id     uuid REFERENCES hris.salary_structures (id),
  activated_by    uuid,
  activated_at    timestamptz,
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, code, version),
  CHECK ((scope = 'grade' AND grade_id IS NOT NULL AND position_id IS NULL AND employee_id IS NULL)
      OR (scope = 'position' AND position_id IS NOT NULL AND grade_id IS NULL AND employee_id IS NULL)
      OR (scope = 'employee' AND employee_id IS NOT NULL AND grade_id IS NULL AND position_id IS NULL))
);
SELECT platform.enable_property_rls('hris.salary_structures');
SELECT platform.add_touch_trigger('hris.salary_structures');
CREATE INDEX salary_structures_target_idx ON hris.salary_structures (property_id, scope, grade_id, position_id, employee_id, effective_from);

-- Payroll runs (FR-PAY-02/06): one regular run per property and period.
CREATE TABLE hris.payroll_runs (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  number                 text NOT NULL,
  name                   text NOT NULL,
  run_type               text NOT NULL CHECK (run_type IN ('regular', 'thr', 'bonus', 'adjustment', 'final_settlement')),
  period_code            text NOT NULL CHECK (period_code ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  period_start           date NOT NULL,
  period_end             date NOT NULL,
  payment_date           date NOT NULL,
  corrects_period        text CHECK (corrects_period IS NULL OR corrects_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  thr_date               date,
  religions              text[] NOT NULL DEFAULT '{}',
  org_unit_id            uuid REFERENCES hris.org_units (id),
  employee_ids           uuid[] NOT NULL DEFAULT '{}',
  status                 text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'calculated', 'submitted', 'approved', 'posted', 'paid', 'cancelled')),
  calculation_count      int NOT NULL DEFAULT 0,
  headcount              int NOT NULL DEFAULT 0,
  gross                  numeric(18,2) NOT NULL DEFAULT 0,
  taxable_gross          numeric(18,2) NOT NULL DEFAULT 0,
  bpjs_employee          numeric(18,2) NOT NULL DEFAULT 0,
  bpjs_employer          numeric(18,2) NOT NULL DEFAULT 0,
  pph21                  numeric(18,2) NOT NULL DEFAULT 0,
  other_deductions       numeric(18,2) NOT NULL DEFAULT 0,
  net                    numeric(18,2) NOT NULL DEFAULT 0,
  employer_cost          numeric(18,2) NOT NULL DEFAULT 0,
  warnings               int NOT NULL DEFAULT 0,
  currency               char(3) NOT NULL DEFAULT 'IDR',
  policy_refs            jsonb NOT NULL DEFAULT '[]'::jsonb,
  statutory_rate_set_id  uuid REFERENCES hris.statutory_rate_sets (id),
  time_lock_id           uuid REFERENCES hris.time_locks (id),
  approval_request_id    uuid,
  calculated_at          timestamptz,
  calculated_by          uuid,
  submitted_at           timestamptz,
  submitted_by           uuid,
  approved_at            timestamptz,
  approved_by            uuid,
  decision_note          text,
  posted_at              timestamptz,
  posted_by              uuid,
  paid_on                date,
  paid_at                timestamptz,
  paid_by                uuid,
  payment_reference      text,
  bank_account_code      text,
  bank_file_count        int NOT NULL DEFAULT 0,
  bank_file_at           timestamptz,
  cancelled_at           timestamptz,
  cancelled_by           uuid,
  cancel_reason          text,
  parallel_signoffs      jsonb NOT NULL DEFAULT '[]'::jsonb,
  notes                  text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (property_id, number),
  CHECK (period_end >= period_start)
);
SELECT platform.enable_property_rls('hris.payroll_runs');
SELECT platform.add_touch_trigger('hris.payroll_runs');
CREATE UNIQUE INDEX payroll_runs_regular_uniq ON hris.payroll_runs (property_id, period_code) WHERE run_type = 'regular' AND status <> 'cancelled';
CREATE INDEX payroll_runs_period_idx ON hris.payroll_runs (property_id, period_code, status);

-- Payslips: one per employee and run, with the employee snapshot of the
-- calculation (identity numbers and the bank account for the bank file and
-- the tax exports — never readable by the report role).
CREATE TABLE hris.payroll_slips (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  run_id             uuid NOT NULL REFERENCES hris.payroll_runs (id) ON DELETE CASCADE,
  employee_id        uuid NOT NULL REFERENCES hris.employees (id),
  employee_no        text NOT NULL,
  full_name          text NOT NULL,
  org_unit_id        uuid REFERENCES hris.org_units (id),
  org_unit_code      text,
  org_unit_name      text,
  position_name      text,
  grade_code         text,
  job_title          text,
  cost_center        text,
  employment_status  text,
  join_date          date,
  termination_date   date,
  ptkp_status        text,
  ter_category       text,
  tax_method         text NOT NULL DEFAULT 'none' CHECK (tax_method IN ('ter', 'annual', 'none')),
  ter_rate           numeric(6,3) NOT NULL DEFAULT 0,
  npwp               text,
  nik                text,
  bank_code          text,
  bank_name          text,
  account_no         text,
  account_name       text,
  proration_factor   numeric(10,6) NOT NULL DEFAULT 1,
  fixed_wage         numeric(18,2) NOT NULL DEFAULT 0,
  daily_wage         numeric(18,2) NOT NULL DEFAULT 0,
  scheduled_days     int NOT NULL DEFAULT 0,
  present_days       int NOT NULL DEFAULT 0,
  absent_days        int NOT NULL DEFAULT 0,
  unpaid_leave_days  numeric(6,2) NOT NULL DEFAULT 0,
  overtime_hours     numeric(8,2) NOT NULL DEFAULT 0,
  gross              numeric(18,2) NOT NULL DEFAULT 0,
  taxable_gross      numeric(18,2) NOT NULL DEFAULT 0,
  deductible         numeric(18,2) NOT NULL DEFAULT 0,
  severance          numeric(18,2) NOT NULL DEFAULT 0,
  bpjs_employee      numeric(18,2) NOT NULL DEFAULT 0,
  bpjs_employer      numeric(18,2) NOT NULL DEFAULT 0,
  pph21              numeric(18,2) NOT NULL DEFAULT 0,
  pph21_final        numeric(18,2) NOT NULL DEFAULT 0,
  other_deductions   numeric(18,2) NOT NULL DEFAULT 0,
  net                numeric(18,2) NOT NULL DEFAULT 0,
  employer_cost      numeric(18,2) NOT NULL DEFAULT 0,
  annual             jsonb,
  messages           text[] NOT NULL DEFAULT '{}',
  status             text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'warning')),
  published_at       timestamptz,
  viewed_at          timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, employee_id)
);
SELECT platform.enable_property_rls('hris.payroll_slips');
SELECT platform.add_touch_trigger('hris.payroll_slips');
CREATE INDEX payroll_slips_employee_idx ON hris.payroll_slips (employee_id, run_id);

CREATE TABLE hris.payroll_lines (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  run_id        uuid NOT NULL REFERENCES hris.payroll_runs (id) ON DELETE CASCADE,
  slip_id       uuid NOT NULL REFERENCES hris.payroll_slips (id) ON DELETE CASCADE,
  employee_id   uuid NOT NULL REFERENCES hris.employees (id),
  line_no       int NOT NULL,
  code          text NOT NULL,
  name          text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('earning', 'deduction', 'bpjs_employee', 'bpjs_employer', 'tax')),
  category      text NOT NULL,
  programme     text,
  taxable       boolean NOT NULL DEFAULT false,
  irregular     boolean NOT NULL DEFAULT false,
  pre_tax       boolean NOT NULL DEFAULT false,
  quantity      numeric(18,4) NOT NULL DEFAULT 1,
  rate          numeric(18,4) NOT NULL DEFAULT 0,
  amount        numeric(18,2) NOT NULL,
  source        text NOT NULL,
  input_source  text,
  input_kind    text CHECK (input_kind IS NULL OR input_kind IN ('earning', 'deduction', 'non_taxable')),
  source_type   text,
  source_id     text,
  description   text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.payroll_lines');
CREATE INDEX payroll_lines_slip_idx ON hris.payroll_lines (slip_id, line_no);
CREATE INDEX payroll_lines_run_idx ON hris.payroll_lines (run_id, code);

-- Payroll adjustments (FR-PAY-05 bonus from the review result, corrections,
-- cash advances): approved through the approval engine, paid by the first
-- posted run of their period and run type.
CREATE TABLE hris.payroll_adjustments (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  employee_id          uuid NOT NULL REFERENCES hris.employees (id),
  component_code       text NOT NULL,
  component_name       text NOT NULL,
  kind                 text NOT NULL CHECK (kind IN ('earning', 'deduction')),
  category             text NOT NULL,
  taxable              boolean NOT NULL DEFAULT true,
  irregular            boolean NOT NULL DEFAULT false,
  pre_tax              boolean NOT NULL DEFAULT false,
  amount               numeric(18,2) NOT NULL CHECK (amount <> 0),
  period_code          text NOT NULL CHECK (period_code ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  target_run_type      text NOT NULL DEFAULT 'regular' CHECK (target_run_type IN ('regular', 'bonus', 'adjustment', 'final_settlement')),
  reason               text NOT NULL,
  source               text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'performance_review', 'import')),
  review_id            uuid,
  status               text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled', 'paid')),
  approval_request_id  uuid,
  decided_at           timestamptz,
  decision_note        text,
  run_id               uuid REFERENCES hris.payroll_runs (id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('hris.payroll_adjustments');
SELECT platform.add_touch_trigger('hris.payroll_adjustments');
CREATE INDEX payroll_adjustments_period_idx ON hris.payroll_adjustments (property_id, period_code, status);
CREATE UNIQUE INDEX payroll_adjustments_review_uniq ON hris.payroll_adjustments (review_id, component_code)
  WHERE review_id IS NOT NULL AND status NOT IN ('rejected', 'cancelled');

-- Employee loans and cash advances (FR-PAY-02 potongan pinjaman, kasbon):
-- an installment is deducted by every regular run from the start period
-- until repaid; repaid grows when a run is posted.
CREATE TABLE hris.employee_loans (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  employee_id   uuid NOT NULL REFERENCES hris.employees (id),
  loan_type     text NOT NULL DEFAULT 'loan' CHECK (loan_type IN ('loan', 'cash_advance')),
  principal     numeric(18,2) NOT NULL CHECK (principal > 0),
  installment   numeric(18,2) NOT NULL CHECK (installment > 0),
  start_period  text NOT NULL CHECK (start_period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  repaid        numeric(18,2) NOT NULL DEFAULT 0 CHECK (repaid >= 0),
  reference     text,
  notes         text,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'settled', 'cancelled')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  CHECK (repaid <= principal)
);
SELECT platform.enable_property_rls('hris.employee_loans');
SELECT platform.add_touch_trigger('hris.employee_loans');

-- Opening year-to-date payroll per employee and tax year (EP-28
-- FR-MIG-P5-03): taxable gross, deductible JHT / JP and PPh 21 withheld by
-- the legacy payroll before the cut-over, for the annual calculation and
-- the 1721-A1.
CREATE TABLE hris.payroll_ytd (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  employee_id    uuid NOT NULL REFERENCES hris.employees (id),
  tax_year       int NOT NULL CHECK (tax_year BETWEEN 2000 AND 2100),
  through_month  int NOT NULL CHECK (through_month BETWEEN 1 AND 12),
  gross          numeric(18,2) NOT NULL DEFAULT 0,
  deductible     numeric(18,2) NOT NULL DEFAULT 0,
  pph21          numeric(18,2) NOT NULL DEFAULT 0,
  months_worked  int NOT NULL DEFAULT 0 CHECK (months_worked BETWEEN 0 AND 12),
  legacy_ref     text,
  imported_at    timestamptz NOT NULL DEFAULT now(),
  imported_by    uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (employee_id, tax_year)
);
SELECT platform.enable_property_rls('hris.payroll_ytd');
SELECT platform.add_touch_trigger('hris.payroll_ytd');

-- Legacy payroll of a parallel-run period (EP-28 FR-MIG-P5-05/06, EP-29):
-- the club's current calculation per employee and component, compared with
-- the OneClub run of the same period.
CREATE TABLE hris.legacy_payroll_lines (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  period_code     text NOT NULL CHECK (period_code ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  employee_id     uuid NOT NULL REFERENCES hris.employees (id),
  component_code  text NOT NULL,
  amount          numeric(18,2) NOT NULL,
  batch_ref       text,
  imported_at     timestamptz NOT NULL DEFAULT now(),
  imported_by     uuid,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, period_code, employee_id, component_code)
);
SELECT platform.enable_property_rls('hris.legacy_payroll_lines');
SELECT platform.add_touch_trigger('hris.legacy_payroll_lines');

-- Initial statutory rates, in force from 1 January 2024 (PP 58/2023 TER,
-- PMK 168/2023, UU HPP Article 17, PMK 101/2016 PTKP, PP 68/2009 severance,
-- BPJS rates and caps of the PRD P5 EP-10 acceptance criteria). To be
-- verified by the tax consultant before go-live (FR-TAX-HR-05): the set is
-- marked unverified until then.
INSERT INTO hris.statutory_rate_sets (id, code, name, effective_from, status, rates, regulation, verification_status, notes)
VALUES ('0192a000-0000-7000-8000-00000000a021', 'ID-2024', 'Indonesia 2024 (TER)', '2024-01-01', 'active', '{"ptkp":{"K/0":"58500000","K/1":"63000000","K/2":"67500000","K/3":"72000000","TK/0":"54000000","TK/1":"58500000","TK/2":"63000000","TK/3":"67500000"},"terCategories":{"K/0":"A","K/1":"B","K/2":"B","K/3":"C","TK/0":"A","TK/1":"A","TK/2":"B","TK/3":"B"},"terRates":{"A":[{"upTo":"5400000","rate":"0"},{"upTo":"5650000","rate":"0.25"},{"upTo":"5950000","rate":"0.5"},{"upTo":"6300000","rate":"0.75"},{"upTo":"6750000","rate":"1"},{"upTo":"7500000","rate":"1.25"},{"upTo":"8550000","rate":"1.5"},{"upTo":"9650000","rate":"1.75"},{"upTo":"10050000","rate":"2"},{"upTo":"10350000","rate":"2.25"},{"upTo":"10700000","rate":"2.5"},{"upTo":"11050000","rate":"3"},{"upTo":"11600000","rate":"3.5"},{"upTo":"12500000","rate":"4"},{"upTo":"13750000","rate":"5"},{"upTo":"15100000","rate":"6"},{"upTo":"16950000","rate":"7"},{"upTo":"19750000","rate":"8"},{"upTo":"24150000","rate":"9"},{"upTo":"26450000","rate":"10"},{"upTo":"28000000","rate":"11"},{"upTo":"30050000","rate":"12"},{"upTo":"32400000","rate":"13"},{"upTo":"35400000","rate":"14"},{"upTo":"39100000","rate":"15"},{"upTo":"43850000","rate":"16"},{"upTo":"47800000","rate":"17"},{"upTo":"51400000","rate":"18"},{"upTo":"56300000","rate":"19"},{"upTo":"62200000","rate":"20"},{"upTo":"68600000","rate":"21"},{"upTo":"77500000","rate":"22"},{"upTo":"89000000","rate":"23"},{"upTo":"103000000","rate":"24"},{"upTo":"125000000","rate":"25"},{"upTo":"157000000","rate":"26"},{"upTo":"206000000","rate":"27"},{"upTo":"337000000","rate":"28"},{"upTo":"454000000","rate":"29"},{"upTo":"550000000","rate":"30"},{"upTo":"695000000","rate":"31"},{"upTo":"910000000","rate":"32"},{"upTo":"1400000000","rate":"33"},{"upTo":"","rate":"34"}],"B":[{"upTo":"6200000","rate":"0"},{"upTo":"6500000","rate":"0.25"},{"upTo":"6850000","rate":"0.5"},{"upTo":"7300000","rate":"0.75"},{"upTo":"9200000","rate":"1"},{"upTo":"10750000","rate":"1.5"},{"upTo":"11250000","rate":"2"},{"upTo":"11600000","rate":"2.5"},{"upTo":"12600000","rate":"3"},{"upTo":"13600000","rate":"4"},{"upTo":"14950000","rate":"5"},{"upTo":"16400000","rate":"6"},{"upTo":"18450000","rate":"7"},{"upTo":"21850000","rate":"8"},{"upTo":"26000000","rate":"9"},{"upTo":"27700000","rate":"10"},{"upTo":"29350000","rate":"11"},{"upTo":"31450000","rate":"12"},{"upTo":"33950000","rate":"13"},{"upTo":"37100000","rate":"14"},{"upTo":"41100000","rate":"15"},{"upTo":"45800000","rate":"16"},{"upTo":"49500000","rate":"17"},{"upTo":"53800000","rate":"18"},{"upTo":"58500000","rate":"19"},{"upTo":"64000000","rate":"20"},{"upTo":"71000000","rate":"21"},{"upTo":"80000000","rate":"22"},{"upTo":"93000000","rate":"23"},{"upTo":"109000000","rate":"24"},{"upTo":"129000000","rate":"25"},{"upTo":"163000000","rate":"26"},{"upTo":"211000000","rate":"27"},{"upTo":"374000000","rate":"28"},{"upTo":"459000000","rate":"29"},{"upTo":"555000000","rate":"30"},{"upTo":"704000000","rate":"31"},{"upTo":"957000000","rate":"32"},{"upTo":"1405000000","rate":"33"},{"upTo":"","rate":"34"}],"C":[{"upTo":"6600000","rate":"0"},{"upTo":"6950000","rate":"0.25"},{"upTo":"7350000","rate":"0.5"},{"upTo":"7800000","rate":"0.75"},{"upTo":"8850000","rate":"1"},{"upTo":"9800000","rate":"1.25"},{"upTo":"10950000","rate":"1.5"},{"upTo":"11200000","rate":"1.75"},{"upTo":"12050000","rate":"2"},{"upTo":"12950000","rate":"3"},{"upTo":"14150000","rate":"4"},{"upTo":"15550000","rate":"5"},{"upTo":"17050000","rate":"6"},{"upTo":"19500000","rate":"7"},{"upTo":"22700000","rate":"8"},{"upTo":"26600000","rate":"9"},{"upTo":"28100000","rate":"10"},{"upTo":"30100000","rate":"11"},{"upTo":"32600000","rate":"12"},{"upTo":"35400000","rate":"13"},{"upTo":"38900000","rate":"14"},{"upTo":"43000000","rate":"15"},{"upTo":"47400000","rate":"16"},{"upTo":"51200000","rate":"17"},{"upTo":"55800000","rate":"18"},{"upTo":"60400000","rate":"19"},{"upTo":"66700000","rate":"20"},{"upTo":"74500000","rate":"21"},{"upTo":"83200000","rate":"22"},{"upTo":"95600000","rate":"23"},{"upTo":"110000000","rate":"24"},{"upTo":"134000000","rate":"25"},{"upTo":"169000000","rate":"26"},{"upTo":"221000000","rate":"27"},{"upTo":"390000000","rate":"28"},{"upTo":"463000000","rate":"29"},{"upTo":"561000000","rate":"30"},{"upTo":"709000000","rate":"31"},{"upTo":"965000000","rate":"32"},{"upTo":"1419000000","rate":"33"},{"upTo":"","rate":"34"}]},"progressiveRates":[{"upTo":"60000000","rate":"5"},{"upTo":"250000000","rate":"15"},{"upTo":"500000000","rate":"25"},{"upTo":"5000000000","rate":"30"},{"upTo":"","rate":"35"}],"occupationalCostPercent":"5","occupationalCostMaxAnnual":"6000000","nonNpwpSurchargePercent":"20","severanceTaxBrackets":[{"upTo":"50000000","rate":"0"},{"upTo":"100000000","rate":"5"},{"upTo":"500000000","rate":"15"},{"upTo":"","rate":"25"}],"taxableEmployerContributions":["kesehatan","jkk","jkm"],"deductibleEmployeeContributions":["jht","jp"],"bpjs":{"kesehatan":{"employerPercent":"4","employeePercent":"1","wageCap":"12000000"},"jht":{"employerPercent":"3.7","employeePercent":"2","wageCap":""},"jp":{"employerPercent":"2","employeePercent":"1","wageCap":"10547400"},"jkk":{"employerPercent":"0.24","employeePercent":"0","wageCap":""},"jkm":{"employerPercent":"0.3","employeePercent":"0","wageCap":""},"partner":{"jkkPercent":"1","jkmAmount":"6800"}}}'::jsonb,
  'PP 58/2023, PMK 168/2023, UU HPP (Article 17), PMK 101/2016 (PTKP), PP 68/2009 (severance), BPJS Kesehatan & Ketenagakerjaan', 'unverified',
  'Initial values — to be verified by the tax consultant before go-live (FR-TAX-HR-05).');

-- The report role reads payroll amounts (payroll reports are granted to
-- payroll roles only, FR-RPT-P5-04) but never the identity numbers and bank
-- accounts of the payslip snapshots. hris.harden_report_role() runs every
-- per-area function.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.harden_report_role_payroll() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON hris.payroll_slips FROM %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, run_id, employee_id, employee_no, full_name, org_unit_id, org_unit_code, org_unit_name, position_name,
    grade_code, job_title, cost_center, employment_status, join_date, termination_date, ptkp_status, ter_category, tax_method, ter_rate, proration_factor,
    fixed_wage, daily_wage, scheduled_days, present_days, absent_days, unpaid_leave_days, overtime_hours, gross, taxable_gross, deductible, severance,
    bpjs_employee, bpjs_employer, pph21, pph21_final, other_deductions, net, employer_cost, status, published_at, created_at, updated_at)
    ON hris.payroll_slips TO %I', rep);
END $$;
-- +goose StatementEnd

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();
SELECT hris.harden_report_role_payroll();

-- +goose Down
DROP TABLE hris.legacy_payroll_lines, hris.payroll_ytd, hris.employee_loans, hris.payroll_adjustments, hris.payroll_lines, hris.payroll_slips,
  hris.payroll_runs, hris.salary_structures, hris.pay_components, hris.statutory_rate_sets;
DROP FUNCTION hris.harden_report_role_payroll();
