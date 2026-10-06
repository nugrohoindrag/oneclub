-- PRD P5 EP-03 Recruitment and EP-05 Performance Review (module hris).
--
-- Recruitment: job requisitions with approval and headcount (FR-RCT-01),
-- candidates and applications through Applied → Screening → Interview →
-- Offered → Hired / Rejected / Withdrawn (FR-RCT-02), interviews and
-- scorecards (FR-RCT-03), offers with approval and the onboarding checklist
-- of the hire (FR-RCT-04), website applications with consent (FR-RCT-05)
-- and the retention of applicants who were not hired (FR-RCT-06, §16 #10:
-- erased after the retention period).
--
-- Performance Review: templates per position with competencies and KPIs,
-- review cycles (annual / semester / probation), self assessment, manager
-- review and HR calibration with scores per item (FR-PRF-HR-01/02),
-- operational inputs (FR-PRF-HR-03) and the result as the basis of salary
-- increase, bonus and promotion (FR-PRF-HR-04). Completed reviews are
-- written to the employment history ('performance_review').
--
-- Candidate personal data, salary budgets, expected salaries and offer
-- salaries never reach the reporting role (FR-HR-03, UU PDP).

-- +goose Up

-- ── Recruitment ───────────────────────────────────────────────────────────

CREATE TABLE hris.job_requisitions (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  title                text NOT NULL,
  org_unit_id          uuid NOT NULL REFERENCES hris.org_units (id),
  position_id          uuid REFERENCES hris.positions (id),
  grade_id             uuid REFERENCES hris.grades (id),
  hiring_manager_id    uuid REFERENCES hris.employees (id),
  headcount            int NOT NULL CHECK (headcount BETWEEN 1 AND 500),
  hired_count          int NOT NULL DEFAULT 0 CHECK (hired_count >= 0),
  reason               text NOT NULL DEFAULT 'new_position' CHECK (reason IN ('new_position', 'replacement', 'seasonal', 'other')),
  replacement_for_id   uuid REFERENCES hris.employees (id),
  contract_type        text NOT NULL DEFAULT 'pkwt' CHECK (contract_type IN ('pkwt', 'pkwtt')),
  worker_category      text NOT NULL DEFAULT 'regular' CHECK (worker_category IN ('regular', 'daily', 'intern')),
  salary_min           numeric(18,2) CHECK (salary_min IS NULL OR salary_min >= 0),
  salary_max           numeric(18,2) CHECK (salary_max IS NULL OR salary_max >= 0),
  target_start_date    date,
  location             text,
  description          text,
  requirements         text,
  is_public            boolean NOT NULL DEFAULT false,
  publish_until        date,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'open', 'on_hold', 'filled', 'closed', 'rejected',
                         'cancelled')),
  approval_request_id  uuid,
  submitted_at         timestamptz,
  approved_at          timestamptz,
  filled_at            timestamptz,
  closed_at            timestamptz,
  close_reason         text,
  decision_note        text,
  requested_by         uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number),
  CHECK (salary_min IS NULL OR salary_max IS NULL OR salary_max >= salary_min)
);
SELECT platform.enable_property_rls('hris.job_requisitions');
SELECT platform.add_touch_trigger('hris.job_requisitions');
CREATE INDEX job_requisitions_status_idx ON hris.job_requisitions (property_id, status);

-- Candidates: the person, de-duplicated per property by e-mail / phone.
-- consent_at records the consent to process the application (UU PDP);
-- talent_pool_consent lets the club keep a rejected applicant for the
-- retention period of the HR Configuration; erased_at marks the erasure.
CREATE TABLE hris.candidates (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  full_name            text NOT NULL,
  email                text,
  phone                text,
  gender               text CHECK (gender IN ('male', 'female')),
  birth_date           date,
  city                 text,
  address              text,
  education            text CHECK (education IN ('sd', 'smp', 'sma', 'd1', 'd3', 's1', 's2', 's3', 'other')),
  current_employer     text,
  current_title        text,
  experience_years     numeric(4,1) CHECK (experience_years IS NULL OR experience_years >= 0),
  expected_salary      numeric(18,2) CHECK (expected_salary IS NULL OR expected_salary >= 0),
  source               text NOT NULL DEFAULT 'other' CHECK (source IN ('website', 'referral', 'walk_in', 'job_portal', 'agency', 'internal',
                         'social_media', 'other')),
  referred_by_id       uuid REFERENCES hris.employees (id),
  employee_id          uuid REFERENCES hris.employees (id),
  cv_file_id           uuid,
  profile_url          text,
  notes                text,
  consent_at           timestamptz,
  talent_pool_consent  boolean NOT NULL DEFAULT false,
  retention_until      date,
  erased_at            timestamptz,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'hired', 'erased')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  archived_at          timestamptz
);
SELECT platform.enable_property_rls('hris.candidates');
SELECT platform.add_touch_trigger('hris.candidates');
CREATE INDEX candidates_email_idx ON hris.candidates (property_id, lower(email)) WHERE erased_at IS NULL;
CREATE INDEX candidates_retention_idx ON hris.candidates (retention_until) WHERE erased_at IS NULL AND retention_until IS NOT NULL;

CREATE TABLE hris.applications (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  number            text NOT NULL,
  requisition_id    uuid NOT NULL REFERENCES hris.job_requisitions (id),
  candidate_id      uuid NOT NULL REFERENCES hris.candidates (id),
  source            text NOT NULL DEFAULT 'other',
  stage             text NOT NULL DEFAULT 'applied' CHECK (stage IN ('applied', 'screening', 'interview', 'offered', 'hired', 'rejected',
                      'withdrawn')),
  applied_at        timestamptz NOT NULL DEFAULT now(),
  stage_changed_at  timestamptz NOT NULL DEFAULT now(),
  cover_letter      text,
  screening_score   numeric(4,2),
  screening_note    text,
  rating            numeric(4,2),
  closed_at         timestamptz,
  closed_stage      text,
  close_reason      text,
  hired_at          timestamptz,
  employee_id       uuid REFERENCES hris.employees (id),
  contract_id       uuid REFERENCES hris.contracts (id),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, number),
  UNIQUE (requisition_id, candidate_id)
);
SELECT platform.enable_property_rls('hris.applications');
SELECT platform.add_touch_trigger('hris.applications');
CREATE INDEX applications_candidate_idx ON hris.applications (candidate_id);
CREATE INDEX applications_stage_idx ON hris.applications (property_id, stage);

-- Stage history of an application (funnel, time in stage, time to hire).
CREATE TABLE hris.application_stage_events (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  application_id  uuid NOT NULL REFERENCES hris.applications (id) ON DELETE CASCADE,
  from_stage      text,
  to_stage        text NOT NULL,
  note            text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid
);
SELECT platform.enable_property_rls('hris.application_stage_events');
CREATE INDEX application_stage_events_app_idx ON hris.application_stage_events (application_id, created_at);

CREATE TABLE hris.interviews (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  application_id    uuid NOT NULL REFERENCES hris.applications (id) ON DELETE CASCADE,
  round             int NOT NULL DEFAULT 1 CHECK (round BETWEEN 1 AND 20),
  interview_type    text NOT NULL DEFAULT 'onsite' CHECK (interview_type IN ('phone', 'video', 'onsite', 'panel', 'practical')),
  scheduled_at      timestamptz NOT NULL,
  duration_minutes  int NOT NULL DEFAULT 60 CHECK (duration_minutes BETWEEN 5 AND 480),
  location          text,
  interviewer_ids   uuid[] NOT NULL DEFAULT '{}',
  status            text NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled', 'completed', 'cancelled', 'no_show')),
  result            text CHECK (result IN ('pass', 'fail', 'hold')),
  score             numeric(5,2),
  notes             text,
  cancel_reason     text,
  completed_at      timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid
);
SELECT platform.enable_property_rls('hris.interviews');
SELECT platform.add_touch_trigger('hris.interviews');
CREATE INDEX interviews_application_idx ON hris.interviews (application_id);
CREATE INDEX interviews_schedule_idx ON hris.interviews (property_id, scheduled_at);

-- Scorecard of one interviewer: a score per criterion of the Recruitment
-- Configuration, the weighted overall score and a recommendation.
CREATE TABLE hris.interview_scorecards (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  interview_id         uuid NOT NULL REFERENCES hris.interviews (id) ON DELETE CASCADE,
  interviewer_id       uuid REFERENCES hris.employees (id),
  interviewer_user_id  uuid NOT NULL,
  scores               jsonb NOT NULL DEFAULT '[]'::jsonb,
  overall_score        numeric(5,2) NOT NULL,
  recommendation       text NOT NULL CHECK (recommendation IN ('strong_yes', 'yes', 'no', 'strong_no')),
  comments             text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (interview_id, interviewer_user_id)
);
SELECT platform.enable_property_rls('hris.interview_scorecards');
SELECT platform.add_touch_trigger('hris.interview_scorecards');

CREATE TABLE hris.job_offers (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  application_id       uuid NOT NULL REFERENCES hris.applications (id),
  org_unit_id          uuid REFERENCES hris.org_units (id),
  position_id          uuid REFERENCES hris.positions (id),
  grade_id             uuid REFERENCES hris.grades (id),
  job_title            text,
  contract_type        text NOT NULL CHECK (contract_type IN ('pkwt', 'pkwtt')),
  start_date           date NOT NULL,
  end_date             date,
  probation_months     int NOT NULL DEFAULT 0 CHECK (probation_months BETWEEN 0 AND 12),
  currency             char(3) NOT NULL DEFAULT 'IDR',
  base_salary          numeric(18,2) NOT NULL CHECK (base_salary >= 0),
  allowances           jsonb NOT NULL DEFAULT '[]'::jsonb,
  work_week_days       int NOT NULL DEFAULT 5 CHECK (work_week_days IN (5, 6)),
  expires_on           date,
  notes                text,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'submitted', 'approved', 'rejected', 'sent', 'accepted',
                         'declined', 'cancelled', 'expired')),
  approval_request_id  uuid,
  submitted_at         timestamptz,
  approved_at          timestamptz,
  sent_at              timestamptz,
  responded_at         timestamptz,
  response_note        text,
  decision_note        text,
  policy_version       int NOT NULL DEFAULT 0,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number),
  CHECK ((contract_type = 'pkwt' AND end_date IS NOT NULL AND probation_months = 0) OR (contract_type = 'pkwtt' AND end_date IS NULL)),
  CHECK (end_date IS NULL OR end_date >= start_date)
);
SELECT platform.enable_property_rls('hris.job_offers');
SELECT platform.add_touch_trigger('hris.job_offers');
CREATE UNIQUE INDEX job_offers_live_idx ON hris.job_offers (application_id) WHERE status IN ('draft', 'submitted', 'approved', 'sent', 'accepted');

-- Onboarding checklist of a hire (FR-RCT-04) from the Recruitment
-- Configuration.
CREATE TABLE hris.onboarding_items (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  employee_id     uuid NOT NULL REFERENCES hris.employees (id) ON DELETE CASCADE,
  application_id  uuid REFERENCES hris.applications (id),
  code            text NOT NULL,
  label           text NOT NULL,
  sort_order      int NOT NULL DEFAULT 0,
  due_date        date,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'not_applicable')),
  done_at         timestamptz,
  done_by         uuid,
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (employee_id, code)
);
SELECT platform.enable_property_rls('hris.onboarding_items');
SELECT platform.add_touch_trigger('hris.onboarding_items');

-- ── Performance Review ────────────────────────────────────────────────────

-- Review templates per position (position codes; empty = every position)
-- with competencies and KPIs (JSON arrays of {code, label, description,
-- weight[, target, unit]}).
CREATE TABLE hris.review_templates (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  code               text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name               text NOT NULL,
  review_type        text NOT NULL DEFAULT 'any' CHECK (review_type IN ('annual', 'semester', 'probation', 'any')),
  position_codes     text[] NOT NULL DEFAULT '{}',
  competencies       jsonb NOT NULL DEFAULT '[]'::jsonb,
  kpis               jsonb NOT NULL DEFAULT '[]'::jsonb,
  competency_weight  numeric(5,2) NOT NULL DEFAULT 50 CHECK (competency_weight BETWEEN 0 AND 100),
  description        text,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  archived_at        timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('hris.review_templates');
SELECT platform.add_touch_trigger('hris.review_templates');

CREATE TABLE hris.review_cycles (
  id                      uuid PRIMARY KEY,
  property_id             uuid NOT NULL REFERENCES platform.properties (id),
  code                    text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,29}$'),
  name                    text NOT NULL,
  cycle_type              text NOT NULL CHECK (cycle_type IN ('annual', 'semester', 'probation')),
  period_start            date NOT NULL,
  period_end              date NOT NULL,
  self_due                date,
  manager_due             date,
  calibration_due         date,
  org_unit_id             uuid REFERENCES hris.org_units (id),
  default_template_id     uuid REFERENCES hris.review_templates (id),
  status                  text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'in_progress', 'calibration', 'completed', 'cancelled')),
  launched_at             timestamptz,
  calibration_started_at  timestamptz,
  closed_at               timestamptz,
  policy_version          int NOT NULL DEFAULT 0,
  notes                   text,
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  updated_by              uuid,
  UNIQUE (property_id, code),
  CHECK (period_end >= period_start)
);
SELECT platform.enable_property_rls('hris.review_cycles');
SELECT platform.add_touch_trigger('hris.review_cycles');

CREATE TABLE hris.performance_reviews (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  cycle_id               uuid NOT NULL REFERENCES hris.review_cycles (id),
  employee_id            uuid NOT NULL REFERENCES hris.employees (id),
  reviewer_id            uuid REFERENCES hris.employees (id),
  template_id            uuid REFERENCES hris.review_templates (id),
  competency_weight      numeric(5,2) NOT NULL DEFAULT 50,
  status                 text NOT NULL DEFAULT 'self_assessment' CHECK (status IN ('self_assessment', 'manager_review', 'submitted', 'calibrated',
                           'completed', 'cancelled')),
  self_score             numeric(5,2),
  self_comment           text,
  self_submitted_at      timestamptz,
  manager_score          numeric(5,2),
  manager_comment        text,
  strengths              text,
  improvements           text,
  goals                  text,
  manager_submitted_at   timestamptz,
  manager_submitted_by   uuid,
  recommended_rating     text,
  final_score            numeric(5,2),
  final_rating           text,
  calibrated_at          timestamptz,
  calibrated_by          uuid,
  calibration_note       text,
  recommendation         text NOT NULL DEFAULT 'none' CHECK (recommendation IN ('none', 'salary_increase', 'bonus', 'promotion', 'confirm_employment',
                           'extend_probation', 'improvement_plan', 'terminate')),
  increase_percent       numeric(5,2),
  bonus_months           numeric(4,2),
  inputs                 jsonb NOT NULL DEFAULT '[]'::jsonb,
  employment_change_id   uuid REFERENCES hris.employment_history (id),
  history_id             uuid REFERENCES hris.employment_history (id),
  acknowledged_at        timestamptz,
  completed_at           timestamptz,
  policy_version         int NOT NULL DEFAULT 0,
  created_at             timestamptz NOT NULL DEFAULT now(),
  created_by             uuid,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             uuid,
  UNIQUE (cycle_id, employee_id)
);
SELECT platform.enable_property_rls('hris.performance_reviews');
SELECT platform.add_touch_trigger('hris.performance_reviews');
CREATE INDEX performance_reviews_employee_idx ON hris.performance_reviews (employee_id);
CREATE INDEX performance_reviews_reviewer_idx ON hris.performance_reviews (reviewer_id) WHERE status IN ('self_assessment', 'manager_review');

-- Scores per competency / KPI of a review (template snapshot).
CREATE TABLE hris.review_scores (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  review_id        uuid NOT NULL REFERENCES hris.performance_reviews (id) ON DELETE CASCADE,
  item_kind        text NOT NULL CHECK (item_kind IN ('competency', 'kpi')),
  code             text NOT NULL,
  label            text NOT NULL,
  description      text,
  weight           numeric(5,2) NOT NULL DEFAULT 1 CHECK (weight > 0),
  target           text,
  sort_order       int NOT NULL DEFAULT 0,
  actual           text,
  self_score       numeric(4,2),
  self_comment     text,
  manager_score    numeric(4,2),
  manager_comment  text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (review_id, item_kind, code)
);
SELECT platform.enable_property_rls('hris.review_scores');
SELECT platform.add_touch_trigger('hris.review_scores');

-- Completed reviews are part of the employment history (FR-PRF-HR-04).
ALTER TABLE hris.employment_history DROP CONSTRAINT employment_history_kind_check;
ALTER TABLE hris.employment_history ADD CONSTRAINT employment_history_kind_check CHECK (kind IN ('hire', 'transfer', 'rotation', 'promotion',
  'demotion', 'status_change', 'contract', 'termination', 'rehire', 'data_migration', 'performance_review'));

-- The report role reads no candidate personal data, salary budgets or offer
-- salaries. hris.harden_report_role() (every hris migration ends with it)
-- now also runs the hris.harden_report_role_<area>() functions of the later
-- hris areas, so a later grant_app('hris') + harden_report_role() keeps
-- these columns hidden.
-- +goose StatementBegin
CREATE FUNCTION hris.harden_report_role_talent() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
BEGIN
  IF rep IS NULL OR rep = '' THEN
    RETURN;
  END IF;
  EXECUTE format('REVOKE SELECT ON hris.job_requisitions, hris.candidates, hris.applications, hris.interviews, hris.interview_scorecards,
    hris.job_offers FROM %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, number, title, org_unit_id, position_id, grade_id, hiring_manager_id, headcount, hired_count, reason,
    contract_type, worker_category, target_start_date, location, is_public, publish_until, status, submitted_at, approved_at, filled_at, closed_at,
    created_at, updated_at) ON hris.job_requisitions TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, source, status, talent_pool_consent, retention_until, erased_at, created_at, archived_at)
    ON hris.candidates TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, number, requisition_id, candidate_id, source, stage, applied_at, stage_changed_at, rating, closed_at,
    closed_stage, hired_at, employee_id, created_at) ON hris.applications TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, application_id, round, interview_type, scheduled_at, status, result, score, completed_at)
    ON hris.interviews TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, interview_id, overall_score, recommendation, created_at) ON hris.interview_scorecards TO %I', rep);
  EXECUTE format('GRANT SELECT (id, property_id, number, application_id, org_unit_id, position_id, grade_id, contract_type, start_date, status,
    submitted_at, approved_at, sent_at, responded_at, created_at) ON hris.job_offers TO %I', rep);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.harden_report_role() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  rep text := current_setting('oneclub.report_role', true);
  f text;
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
  FOR f IN SELECT p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = 'hris' AND p.proname LIKE 'harden\_report\_role\_%' AND p.pronargs = 0 ORDER BY p.proname LOOP
    EXECUTE format('SELECT hris.%I()', f);
  END LOOP;
END $$;
-- +goose StatementEnd

SELECT platform.grant_app('hris');
SELECT hris.harden_report_role();

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION hris.harden_report_role() RETURNS void LANGUAGE plpgsql AS $$
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
DROP FUNCTION hris.harden_report_role_talent();
DELETE FROM hris.employment_history WHERE kind = 'performance_review';
ALTER TABLE hris.employment_history DROP CONSTRAINT employment_history_kind_check;
ALTER TABLE hris.employment_history ADD CONSTRAINT employment_history_kind_check CHECK (kind IN ('hire', 'transfer', 'rotation', 'promotion',
  'demotion', 'status_change', 'contract', 'termination', 'rehire', 'data_migration'));
DROP TABLE hris.review_scores, hris.performance_reviews, hris.review_cycles, hris.review_templates, hris.onboarding_items, hris.job_offers,
  hris.interview_scorecards, hris.interviews, hris.application_stage_events, hris.applications, hris.candidates, hris.job_requisitions;
