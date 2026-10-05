-- PRD P5 core HR (EP-01 Organization & Employee, EP-02 Employment Contract &
-- Documents, EP-04 Training & Certification, EP-16 Employee Self Service).
--
-- Expand → migrate → contract (PRD P5 §5.4.1, Technical Doc §7.5): the
-- Department and Employee masters of P0 move to hris.org_units and
-- hris.employees with the same ids. For one release platform.departments and
-- platform.employees stay as synchronised copies (the P0 API facade and the
-- foreign keys of users, sport club instructors, CRM, inventory and
-- procurement keep working); triggers mirror every write in both
-- directions. The next release repoints the foreign keys to hris and drops
-- the P0 tables.

-- +goose Up

-- Gap-free document numbers per property, prefix and year (contracts).
CREATE TABLE hris.sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL,
  year         int NOT NULL,
  last_value   int NOT NULL,
  PRIMARY KEY (property_id, prefix, year)
);
SELECT platform.enable_property_rls('hris.sequences');

-- Grades G1–G7 (PRD P5 §16 #8), instance-wide.
CREATE TABLE hris.grades (
  id           uuid PRIMARY KEY,
  code         text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  level        int NOT NULL CHECK (level BETWEEN 1 AND 99),
  min_salary   numeric(18,2) CHECK (min_salary IS NULL OR min_salary >= 0),
  max_salary   numeric(18,2) CHECK (max_salary IS NULL OR max_salary >= 0),
  description  text,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  CHECK (min_salary IS NULL OR max_salary IS NULL OR max_salary >= min_salary)
);
SELECT platform.add_touch_trigger('hris.grades');

-- Certification types (FR-TRC-01, PRD P5 §16 #9), instance-wide.
-- mandatory_for lists the workforce roles that must hold a valid
-- certificate of the type (FR-TRC-03).
CREATE TABLE hris.certification_types (
  id               uuid PRIMARY KEY,
  code             text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name             text NOT NULL,
  issuer           text,
  validity_months  int CHECK (validity_months IS NULL OR validity_months > 0),
  mandatory_for    text[] NOT NULL DEFAULT '{}',
  reminder_days    int[] NOT NULL DEFAULT '{60,30}',
  description      text,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz
);
SELECT platform.add_touch_trigger('hris.certification_types');

-- Organization units (FR-HR-01): the P0 departments with hierarchy, unit
-- type, cost center (P4 dimension) and head.
CREATE TABLE hris.org_units (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  parent_id         uuid REFERENCES hris.org_units (id),
  code              text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name              text NOT NULL,
  unit_type         text NOT NULL DEFAULT 'department' CHECK (unit_type IN ('division', 'department', 'section', 'outlet', 'team')),
  cost_center       text,
  head_employee_id  uuid,
  description       text,
  sort_order        int NOT NULL DEFAULT 0,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  archived_at       timestamptz,
  UNIQUE (property_id, code),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
SELECT platform.enable_property_rls('hris.org_units');
SELECT platform.add_touch_trigger('hris.org_units');
CREATE INDEX org_units_parent_idx ON hris.org_units (parent_id);

-- Positions (jabatan) with grade, reporting line and the certifications the
-- position requires (FR-HR-01, FR-TRC-03).
CREATE TABLE hris.positions (
  id                        uuid PRIMARY KEY,
  property_id               uuid NOT NULL REFERENCES platform.properties (id),
  code                      text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name                      text NOT NULL,
  org_unit_id               uuid NOT NULL REFERENCES hris.org_units (id),
  grade_id                  uuid REFERENCES hris.grades (id),
  reports_to_position_id    uuid REFERENCES hris.positions (id),
  is_head                   boolean NOT NULL DEFAULT false,
  workforce_role            text CHECK (workforce_role IS NULL OR workforce_role IN ('lifeguard', 'caddy', 'instructor', 'food_handler',
                              'engineering', 'course_maintenance', 'security', 'sport_staff', 'starter', 'other')),
  required_certifications   text[] NOT NULL DEFAULT '{}',
  headcount                 int CHECK (headcount IS NULL OR headcount >= 0),
  description               text,
  status                    text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at                timestamptz NOT NULL DEFAULT now(),
  created_by                uuid,
  updated_at                timestamptz NOT NULL DEFAULT now(),
  updated_by                uuid,
  archived_at               timestamptz,
  UNIQUE (property_id, code),
  CHECK (reports_to_position_id IS NULL OR reports_to_position_id <> id)
);
SELECT platform.enable_property_rls('hris.positions');
SELECT platform.add_touch_trigger('hris.positions');
CREATE INDEX positions_unit_idx ON hris.positions (org_unit_id);

-- Employee profile (FR-HR-02), the P0 employee master moved to hris.
-- Sensitive personal data (NIK, NPWP, BPJS numbers, health) is masked by
-- permission in the API and never logged (FR-HR-03).
CREATE TABLE hris.employees (
  id                       uuid PRIMARY KEY,
  property_id              uuid NOT NULL REFERENCES platform.properties (id),
  employee_no              text NOT NULL,
  full_name                text NOT NULL,
  preferred_name           text,
  gender                   text CHECK (gender IS NULL OR gender IN ('male', 'female')),
  birth_date               date,
  birth_place              text,
  religion                 text,
  marital_status           text CHECK (marital_status IS NULL OR marital_status IN ('single', 'married', 'divorced', 'widowed')),
  nationality              text,
  nik                      text,
  npwp                     text,
  ptkp_status              text CHECK (ptkp_status IS NULL OR ptkp_status IN ('TK/0', 'TK/1', 'TK/2', 'TK/3', 'K/0', 'K/1', 'K/2', 'K/3')),
  bpjs_kesehatan_no        text,
  bpjs_ketenagakerjaan_no  text,
  blood_type               text,
  health_notes             text,
  email                    text,
  personal_email           text,
  phone                    text,
  address                  text,
  city                     text,
  postal_code              text,
  org_unit_id              uuid REFERENCES hris.org_units (id),
  position_id              uuid REFERENCES hris.positions (id),
  grade_id                 uuid REFERENCES hris.grades (id),
  supervisor_id            uuid REFERENCES hris.employees (id),
  job_title                text,
  cost_center              text,
  employment_status        text NOT NULL DEFAULT 'permanent'
                             CHECK (employment_status IN ('probation', 'contract', 'permanent', 'resigned', 'terminated')),
  worker_category          text NOT NULL DEFAULT 'regular' CHECK (worker_category IN ('regular', 'daily', 'intern')),
  join_date                date,
  probation_end_date       date,
  permanent_date           date,
  termination_date         date,
  termination_type         text CHECK (termination_type IS NULL OR termination_type IN ('resigned', 'terminated', 'contract_ended', 'retired', 'deceased')),
  termination_reason       text,
  termination_status       text CHECK (termination_status IS NULL OR termination_status IN ('scheduled', 'completed')),
  photo_file_id            uuid REFERENCES platform.files (id),
  legacy_ref               text,
  status                   text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid,
  updated_at               timestamptz NOT NULL DEFAULT now(),
  updated_by               uuid,
  archived_at              timestamptz,
  UNIQUE (property_id, employee_no),
  CHECK (supervisor_id IS NULL OR supervisor_id <> id)
);
SELECT platform.enable_property_rls('hris.employees');
SELECT platform.add_touch_trigger('hris.employees');
CREATE INDEX employees_unit_idx ON hris.employees (org_unit_id);
CREATE INDEX employees_supervisor_idx ON hris.employees (supervisor_id);
CREATE UNIQUE INDEX employees_nik_uniq ON hris.employees (property_id, nik)
  WHERE nik IS NOT NULL AND archived_at IS NULL AND employment_status NOT IN ('resigned', 'terminated');

ALTER TABLE hris.org_units ADD CONSTRAINT org_units_head_fk FOREIGN KEY (head_employee_id) REFERENCES hris.employees (id);

CREATE TABLE hris.employee_bank_accounts (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  employee_id   uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  bank_code     text,
  bank_name     text NOT NULL,
  account_no    text NOT NULL,
  account_name  text NOT NULL,
  branch        text,
  is_primary    boolean NOT NULL DEFAULT true,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz
);
SELECT platform.enable_property_rls('hris.employee_bank_accounts');
SELECT platform.add_touch_trigger('hris.employee_bank_accounts');
CREATE UNIQUE INDEX bank_accounts_primary_uniq ON hris.employee_bank_accounts (employee_id)
  WHERE is_primary AND status = 'active' AND archived_at IS NULL;

CREATE TABLE hris.employee_emergency_contacts (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  employee_id   uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  name          text NOT NULL,
  relationship  text NOT NULL DEFAULT 'other' CHECK (relationship IN ('spouse', 'parent', 'child', 'sibling', 'relative', 'friend', 'other')),
  phone         text NOT NULL,
  address       text,
  is_primary    boolean NOT NULL DEFAULT false,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz
);
SELECT platform.enable_property_rls('hris.employee_emergency_contacts');
SELECT platform.add_touch_trigger('hris.employee_emergency_contacts');

-- Employment history: hire, transfer, rotation, promotion, status changes
-- and termination with effective date (FR-HR-05). Future-dated changes stay
-- Scheduled until the daily job applies them.
CREATE TABLE hris.employment_history (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  employee_id         uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  kind                text NOT NULL CHECK (kind IN ('hire', 'transfer', 'rotation', 'promotion', 'demotion', 'status_change', 'contract',
                        'termination', 'rehire', 'data_migration')),
  effective_date      date NOT NULL,
  from_org_unit_id    uuid REFERENCES hris.org_units (id),
  to_org_unit_id      uuid REFERENCES hris.org_units (id),
  from_position_id    uuid REFERENCES hris.positions (id),
  to_position_id      uuid REFERENCES hris.positions (id),
  from_grade_id       uuid REFERENCES hris.grades (id),
  to_grade_id         uuid REFERENCES hris.grades (id),
  from_supervisor_id  uuid REFERENCES hris.employees (id),
  to_supervisor_id    uuid REFERENCES hris.employees (id),
  from_status         text,
  to_status           text,
  reference           text,
  reason              text,
  status              text NOT NULL DEFAULT 'applied' CHECK (status IN ('scheduled', 'applied', 'cancelled')),
  applied_at          timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid
);
SELECT platform.enable_property_rls('hris.employment_history');
SELECT platform.add_touch_trigger('hris.employment_history');
CREATE INDEX employment_history_employee_idx ON hris.employment_history (employee_id, effective_date);
CREATE INDEX employment_history_scheduled_idx ON hris.employment_history (effective_date) WHERE status = 'scheduled';

-- Employment contracts PKWT / PKWTT (FR-CTR-01–03). Renewal and conversion
-- to permanent create a new contract that supersedes the previous one.
CREATE TABLE hris.contracts (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  employee_id           uuid NOT NULL REFERENCES hris.employees (id),
  contract_type         text NOT NULL CHECK (contract_type IN ('pkwt', 'pkwtt')),
  sequence_no           int NOT NULL DEFAULT 1 CHECK (sequence_no >= 1),
  previous_contract_id  uuid REFERENCES hris.contracts (id),
  start_date            date NOT NULL,
  end_date              date,
  probation_months      int NOT NULL DEFAULT 0 CHECK (probation_months BETWEEN 0 AND 12),
  probation_end_date    date,
  org_unit_id           uuid REFERENCES hris.org_units (id),
  position_id           uuid REFERENCES hris.positions (id),
  grade_id              uuid REFERENCES hris.grades (id),
  job_title             text,
  currency              char(3) NOT NULL DEFAULT 'IDR',
  base_salary           numeric(18,2) NOT NULL DEFAULT 0 CHECK (base_salary >= 0),
  allowances            jsonb NOT NULL DEFAULT '[]'::jsonb,
  work_week_days        int NOT NULL DEFAULT 5 CHECK (work_week_days IN (5, 6)),
  status                text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'expiring', 'renewed', 'ended', 'cancelled')),
  activated_at          timestamptz,
  ended_on              date,
  end_reason            text,
  superseded_by_id      uuid REFERENCES hris.contracts (id),
  signed_file_id        uuid REFERENCES platform.files (id),
  notes                 text,
  reminders_sent        int[] NOT NULL DEFAULT '{}',
  policy_version        int NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK ((contract_type = 'pkwt' AND end_date IS NOT NULL) OR (contract_type = 'pkwtt' AND end_date IS NULL)),
  CHECK (end_date IS NULL OR end_date >= start_date),
  CHECK (contract_type = 'pkwtt' OR probation_months = 0)
);
SELECT platform.enable_property_rls('hris.contracts');
SELECT platform.add_touch_trigger('hris.contracts');
CREATE INDEX contracts_employee_idx ON hris.contracts (employee_id, start_date);
CREATE UNIQUE INDEX contracts_one_active ON hris.contracts (employee_id) WHERE status IN ('active', 'expiring');

-- Employee documents (FR-CTR-04) with validity and restricted access;
-- warning letters (SP1–SP3) carry their level (service charge eligibility,
-- PRD P5 §16 #5).
CREATE TABLE hris.employee_documents (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  employee_id      uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  document_type    text NOT NULL CHECK (document_type IN ('ktp', 'kk', 'npwp', 'passport', 'ijazah', 'cv', 'bpjs_kesehatan', 'bpjs_ketenagakerjaan',
                     'certificate', 'contract', 'warning_letter', 'medical', 'reference_letter', 'photo', 'other')),
  title            text NOT NULL,
  document_no      text,
  issued_on        date,
  expires_on       date,
  warning_level    int CHECK (warning_level IS NULL OR warning_level BETWEEN 1 AND 3),
  file_id          uuid REFERENCES platform.files (id),
  confidential     boolean NOT NULL DEFAULT false,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'superseded')),
  reminders_sent   int[] NOT NULL DEFAULT '{}',
  notes            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  CHECK ((document_type = 'warning_letter') = (warning_level IS NOT NULL)),
  CHECK (expires_on IS NULL OR issued_on IS NULL OR expires_on >= issued_on)
);
SELECT platform.enable_property_rls('hris.employee_documents');
SELECT platform.add_touch_trigger('hris.employee_documents');
CREATE INDEX employee_documents_employee_idx ON hris.employee_documents (employee_id);
CREATE INDEX employee_documents_expiry_idx ON hris.employee_documents (expires_on) WHERE archived_at IS NULL;

-- Certifications of employees and of partner caddies and instructors
-- (FR-TRC-02). Partners are referenced by their golf / sport club id.
CREATE TABLE hris.certifications (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  certification_type_id   uuid NOT NULL REFERENCES hris.certification_types (id),
  holder_kind             text NOT NULL CHECK (holder_kind IN ('employee', 'caddy', 'instructor')),
  employee_id             uuid REFERENCES hris.employees (id),
  partner_id              uuid,
  holder_name             text NOT NULL,
  certificate_no          text,
  issuer                  text,
  issued_on               date,
  expires_on              date,
  file_id                 uuid REFERENCES platform.files (id),
  status                  text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'expired', 'renewed', 'revoked')),
  renewed_by_id           uuid REFERENCES hris.certifications (id),
  revoked_reason          text,
  reminders_sent          int[] NOT NULL DEFAULT '{}',
  expired_notified_at     timestamptz,
  notes                   text,
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              uuid,
  archived_at             timestamptz,
  CHECK ((holder_kind = 'employee' AND employee_id IS NOT NULL) OR (holder_kind <> 'employee' AND partner_id IS NOT NULL)),
  CHECK (expires_on IS NULL OR issued_on IS NULL OR expires_on >= issued_on)
);
SELECT platform.enable_property_rls('hris.certifications');
SELECT platform.add_touch_trigger('hris.certifications');
CREATE INDEX certifications_employee_idx ON hris.certifications (employee_id, certification_type_id);
CREATE INDEX certifications_partner_idx ON hris.certifications (holder_kind, partner_id, certification_type_id);
CREATE INDEX certifications_expiry_idx ON hris.certifications (expires_on) WHERE status = 'active';

-- Training (FR-TRC-04/05): programs, sessions, participants with
-- attendance, result and cost.
CREATE TABLE hris.training_programs (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  code                   text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name                   text NOT NULL,
  category               text NOT NULL DEFAULT 'technical' CHECK (category IN ('mandatory', 'safety', 'service', 'technical', 'leadership', 'compliance', 'other')),
  provider               text,
  duration_hours         numeric(6,2) CHECK (duration_hours IS NULL OR duration_hours > 0),
  cost_per_participant   numeric(18,2) NOT NULL DEFAULT 0 CHECK (cost_per_participant >= 0),
  certification_type_id  uuid REFERENCES hris.certification_types (id),
  required_positions     text[] NOT NULL DEFAULT '{}',
  refresher_months       int CHECK (refresher_months IS NULL OR refresher_months > 0),
  description            text,
  status                 text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  archived_at            timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.training_programs');
SELECT platform.add_touch_trigger('hris.training_programs');

CREATE TABLE hris.training_sessions (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  program_id    uuid NOT NULL REFERENCES hris.training_programs (id),
  title         text NOT NULL,
  starts_at     timestamptz NOT NULL,
  ends_at       timestamptz NOT NULL,
  location      text,
  trainer       text,
  capacity      int CHECK (capacity IS NULL OR capacity > 0),
  cost_total    numeric(18,2) CHECK (cost_total IS NULL OR cost_total >= 0),
  status        text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'completed', 'cancelled')),
  completed_at  timestamptz,
  notes         text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  updated_by    uuid,
  archived_at   timestamptz,
  CHECK (ends_at > starts_at)
);
SELECT platform.enable_property_rls('hris.training_sessions');
SELECT platform.add_touch_trigger('hris.training_sessions');
CREATE INDEX training_sessions_program_idx ON hris.training_sessions (program_id, starts_at);

CREATE TABLE hris.training_participants (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  session_id        uuid NOT NULL REFERENCES hris.training_sessions (id) ON DELETE CASCADE,
  employee_id       uuid NOT NULL REFERENCES hris.employees (id),
  attendance        text NOT NULL DEFAULT 'registered' CHECK (attendance IN ('registered', 'attended', 'absent', 'excused')),
  result            text CHECK (result IS NULL OR result IN ('passed', 'failed')),
  score             numeric(6,2),
  certification_id  uuid REFERENCES hris.certifications (id),
  notes             text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (session_id, employee_id)
);
SELECT platform.enable_property_rls('hris.training_participants');
SELECT platform.add_touch_trigger('hris.training_participants');
CREATE INDEX training_participants_employee_idx ON hris.training_participants (employee_id);

-- Offboarding checklist (FR-HR-06) from the HR Configuration.
CREATE TABLE hris.offboarding_items (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  employee_id  uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  code         text NOT NULL,
  label        text NOT NULL,
  sort_order   int NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'not_applicable')),
  done_at      timestamptz,
  done_by      uuid,
  notes        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (employee_id, code)
);
SELECT platform.enable_property_rls('hris.offboarding_items');
SELECT platform.add_touch_trigger('hris.offboarding_items');

-- Personal data updates from Employee Self Service, verified by HR
-- (FR-ESS-06).
CREATE TABLE hris.profile_change_requests (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  employee_id    uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  changes        jsonb NOT NULL,
  status         text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted', 'approved', 'rejected', 'cancelled')),
  submitted_by   uuid,
  reviewed_by    uuid,
  reviewed_at    timestamptz,
  review_note    text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('hris.profile_change_requests');
SELECT platform.add_touch_trigger('hris.profile_change_requests');
CREATE INDEX profile_change_requests_employee_idx ON hris.profile_change_requests (employee_id, created_at);

-- HR letter templates (FR-CTR-05): employment agreement, employment
-- certificate, warning letter … rendered from employee data.
CREATE TABLE hris.letter_templates (
  id           uuid PRIMARY KEY,
  code         text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name         text NOT NULL,
  letter_type  text NOT NULL DEFAULT 'other' CHECK (letter_type IN ('employment_agreement', 'employment_certificate', 'warning_letter',
                 'offer_letter', 'other')),
  language     text NOT NULL DEFAULT 'id' CHECK (language IN ('id', 'en')),
  title        text NOT NULL,
  body         text NOT NULL,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz
);
SELECT platform.add_touch_trigger('hris.letter_templates');

-- ── migrate: P0 departments and employees → hris (same ids) ────────────────
INSERT INTO hris.org_units (id, property_id, code, name, status, created_at, created_by, updated_at, updated_by, archived_at)
SELECT id, property_id, code, name, status, created_at, created_by, updated_at, updated_by, archived_at FROM platform.departments;
UPDATE hris.org_units h SET parent_id = d.parent_id FROM platform.departments d WHERE d.id = h.id AND d.parent_id IS NOT NULL;

INSERT INTO hris.employees (id, property_id, employee_no, full_name, org_unit_id, job_title, email, phone, join_date, employment_status, status,
  created_at, created_by, updated_at, updated_by, archived_at)
SELECT id, property_id, employee_no, full_name, department_id, job_title, email, phone, join_date,
       CASE WHEN status = 'active' THEN 'permanent' ELSE 'resigned' END, status, created_at, created_by, updated_at, updated_by, archived_at
FROM platform.employees;

INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, to_org_unit_id, to_status, reason, status, applied_at)
SELECT gen_random_uuid(), property_id, id, 'data_migration', coalesce(join_date, created_at::date), department_id,
       CASE WHEN status = 'active' THEN 'permanent' ELSE 'resigned' END, 'Migrated from the P0 employee master', 'applied', now()
FROM platform.employees;

-- ── synchronised P0 copies (one release) ──────────────────────────────────
-- A write on either side is mirrored to the other; pg_trigger_depth() stops
-- the echo of the mirrored write.
-- +goose StatementBegin
CREATE FUNCTION hris.org_unit_to_platform() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF pg_trigger_depth() > 1 THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'DELETE' THEN
    DELETE FROM platform.departments WHERE id = OLD.id;
    RETURN NULL;
  END IF;
  INSERT INTO platform.departments (id, property_id, parent_id, code, name, status, created_by, updated_by, archived_at)
  VALUES (NEW.id, NEW.property_id, NEW.parent_id, NEW.code, NEW.name, NEW.status, NEW.created_by, NEW.updated_by, NEW.archived_at)
  ON CONFLICT (id) DO UPDATE SET parent_id = EXCLUDED.parent_id, code = EXCLUDED.code, name = EXCLUDED.name, status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;

CREATE FUNCTION hris.org_unit_from_platform() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF pg_trigger_depth() > 1 THEN
    RETURN NULL;
  END IF;
  IF TG_OP = 'DELETE' THEN
    DELETE FROM hris.org_units WHERE id = OLD.id;
    RETURN NULL;
  END IF;
  INSERT INTO hris.org_units (id, property_id, parent_id, code, name, status, created_by, updated_by, archived_at)
  VALUES (NEW.id, NEW.property_id, NEW.parent_id, NEW.code, NEW.name, NEW.status, NEW.created_by, NEW.updated_by, NEW.archived_at)
  ON CONFLICT (id) DO UPDATE SET parent_id = EXCLUDED.parent_id, code = EXCLUDED.code, name = EXCLUDED.name, status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;

CREATE FUNCTION hris.employee_to_platform() RETURNS trigger LANGUAGE plpgsql AS $$
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
    NEW.status, NEW.created_by, NEW.updated_by, NEW.archived_at)
  ON CONFLICT (id) DO UPDATE SET employee_no = EXCLUDED.employee_no, full_name = EXCLUDED.full_name, department_id = EXCLUDED.department_id,
    job_title = EXCLUDED.job_title, email = EXCLUDED.email, phone = EXCLUDED.phone, join_date = EXCLUDED.join_date, status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;

-- An employee created or edited through the P0 facade keeps its HR data;
-- new P0 employees start Permanent (existing staff).
CREATE FUNCTION hris.employee_from_platform() RETURNS trigger LANGUAGE plpgsql AS $$
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
    job_title = EXCLUDED.job_title, email = EXCLUDED.email, phone = EXCLUDED.phone, join_date = EXCLUDED.join_date, status = EXCLUDED.status,
    updated_by = EXCLUDED.updated_by, archived_at = EXCLUDED.archived_at;
  RETURN NULL;
END $$;

-- The reporting role (read replica) never reads identity numbers, bank
-- accounts, salaries, health data or pending personal data changes
-- (FR-HR-03): table-wide SELECT from platform.grant_app is replaced with
-- column grants. Every later hris migration ends with this call.
CREATE FUNCTION hris.harden_report_role() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON hris.employees, hris.employee_bank_accounts, hris.contracts, hris.profile_change_requests,
    hris.employee_documents FROM %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, employee_no, full_name, preferred_name, gender, org_unit_id, position_id, grade_id, supervisor_id,
    job_title, cost_center, employment_status, worker_category, join_date, probation_end_date, permanent_date, termination_date, termination_type,
    termination_status, status, created_at, updated_at, archived_at) ON hris.employees TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, number, employee_id, contract_type, sequence_no, previous_contract_id, start_date, end_date,
    probation_months, probation_end_date, org_unit_id, position_id, grade_id, job_title, status, activated_at, ended_on, end_reason, created_at,
    updated_at) ON hris.contracts TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, employee_id, document_type, title, issued_on, expires_on, warning_level, confidential, status,
    created_at, updated_at, archived_at) ON hris.employee_documents TO %I', rep);
END $$;
-- +goose StatementEnd

CREATE TRIGGER sync_platform AFTER INSERT OR UPDATE OR DELETE ON hris.org_units FOR EACH ROW EXECUTE FUNCTION hris.org_unit_to_platform();
CREATE TRIGGER sync_hris AFTER INSERT OR UPDATE OR DELETE ON platform.departments FOR EACH ROW EXECUTE FUNCTION hris.org_unit_from_platform();
CREATE TRIGGER sync_platform AFTER INSERT OR UPDATE OR DELETE ON hris.employees FOR EACH ROW EXECUTE FUNCTION hris.employee_to_platform();
CREATE TRIGGER sync_hris AFTER INSERT OR UPDATE OR DELETE ON platform.employees FOR EACH ROW EXECUTE FUNCTION hris.employee_from_platform();

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();

-- +goose Down
DROP TRIGGER IF EXISTS sync_hris ON platform.employees;
DROP TRIGGER IF EXISTS sync_hris ON platform.departments;
DROP TABLE hris.letter_templates, hris.profile_change_requests, hris.offboarding_items, hris.training_participants, hris.training_sessions,
  hris.training_programs, hris.certifications, hris.employee_documents, hris.contracts, hris.employment_history, hris.employee_emergency_contacts,
  hris.employee_bank_accounts;
ALTER TABLE hris.org_units DROP CONSTRAINT org_units_head_fk;
DROP TABLE hris.employees, hris.positions, hris.org_units, hris.certification_types, hris.grades, hris.sequences;
DROP FUNCTION hris.harden_report_role(), hris.employee_from_platform(), hris.employee_to_platform(), hris.org_unit_from_platform(),
  hris.org_unit_to_platform();
