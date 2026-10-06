-- PRD P5 EP-21 Management Dashboard & BI and EP-27 HR KPI framework.
--
-- * Analytics store (FR-BI-01, Tech Doc §7.6): schema `analytics`, separate
--   from the transactional schemas and from the reporting read models. Jobs
--   materialise every executive KPI per property, KPI and period (day, month,
--   year) by running the KPI definition of its source dashboard (one
--   definition per KPI, FR-BI-07), and the revenue facts per day and
--   dimension (business line, revenue component, outlet, segment) for the
--   drill-down (FR-BI-04). The schema is the extraction boundary when the
--   analytics store moves to its own database.
-- * KPI targets (FR-BI-03, PRD P5 §16 #13): annual budget per month,
--   versioned target plans approved through the approval engine.
-- * Scheduled reports (FR-BI-05) and saved self-service reports (FR-BI-06).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS analytics;

CREATE TABLE analytics.kpi_values (
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  kpi_key       text NOT NULL,
  grain         text NOT NULL CHECK (grain IN ('day', 'month', 'year')),
  period_start  date NOT NULL,
  period_end    date NOT NULL,          -- last day covered (today for the running month / year)
  value         numeric NOT NULL,
  refreshed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, kpi_key, grain, period_start),
  CHECK (period_end >= period_start)
);
CREATE INDEX kpi_values_period ON analytics.kpi_values (property_id, grain, period_start);
SELECT platform.enable_property_rls('analytics.kpi_values');

CREATE TABLE analytics.revenue_daily (
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  day                date NOT NULL,
  daypart            text NOT NULL CHECK (daypart IN ('morning', 'afternoon', 'evening')),  -- posting time (club time zone)
  business_line      text NOT NULL,
  revenue_component  text NOT NULL,
  outlet             text NOT NULL DEFAULT '',
  segment            text NOT NULL DEFAULT '',
  amount             numeric(19,4) NOT NULL,
  lines              int NOT NULL,
  refreshed_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, day, daypart, business_line, revenue_component, outlet, segment)
);
SELECT platform.enable_property_rls('analytics.revenue_daily');

CREATE TABLE analytics.refresh_runs (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  kind          text NOT NULL CHECK (kind IN ('incremental', 'backfill', 'manual', 'seed')),
  window_from   date NOT NULL,
  window_to     date NOT NULL,
  status        text NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
  kpis          int NOT NULL DEFAULT 0,
  values_written int NOT NULL DEFAULT 0,
  facts_written int NOT NULL DEFAULT 0,
  duration_ms   int,
  error         text,
  requested_by  uuid,
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz
);
CREATE INDEX refresh_runs_recent ON analytics.refresh_runs (property_id, started_at DESC);
SELECT platform.enable_property_rls('analytics.refresh_runs');

SELECT platform.grant_app('analytics');

-- ── KPI targets (FR-BI-03) ─────────────────────────────────────────────────
CREATE TABLE reporting.kpi_target_plans (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  year                 int NOT NULL CHECK (year BETWEEN 2000 AND 2100),
  version              int NOT NULL,
  title                text NOT NULL,
  notes                text,
  status               text NOT NULL DEFAULT 'draft'
                       CHECK (status IN ('draft', 'pending_approval', 'approved', 'rejected', 'superseded')),
  approval_request_id  uuid,
  submitted_at         timestamptz,
  submitted_by         uuid,
  decided_at           timestamptz,
  decided_by           uuid,
  decision_reason      text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, year, version)
);
-- One plan in force per property and year.
CREATE UNIQUE INDEX kpi_target_plans_approved ON reporting.kpi_target_plans (property_id, year) WHERE status = 'approved';
SELECT platform.enable_property_rls('reporting.kpi_target_plans');
SELECT platform.add_touch_trigger('reporting.kpi_target_plans');

CREATE TABLE reporting.kpi_targets (
  plan_id      uuid NOT NULL REFERENCES reporting.kpi_target_plans (id) ON DELETE CASCADE,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  kpi_key      text NOT NULL,
  month        int NOT NULL CHECK (month BETWEEN 1 AND 12),
  target       numeric NOT NULL,
  PRIMARY KEY (plan_id, kpi_key, month)
);
SELECT platform.enable_property_rls('reporting.kpi_targets');

-- ── Scheduled reports (FR-BI-05) ──────────────────────────────────────────
CREATE TABLE reporting.scheduled_reports (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  name                text NOT NULL,
  report_code         text NOT NULL,
  format              text NOT NULL CHECK (format IN ('csv', 'xlsx', 'pdf')),
  parameters          jsonb NOT NULL DEFAULT '{}'::jsonb,
  period              text NOT NULL CHECK (period IN ('previous_day', 'previous_week', 'previous_month', 'month_to_date', 'year_to_date')),
  frequency           text NOT NULL CHECK (frequency IN ('daily', 'weekly', 'monthly')),
  weekday             int CHECK (weekday BETWEEN 0 AND 6),        -- weekly: 0 = Sunday
  month_day           int CHECK (month_day BETWEEN 1 AND 28),     -- monthly
  send_time           time NOT NULL DEFAULT '07:00',
  channels            text[] NOT NULL DEFAULT '{email}',
  recipient_roles     text[] NOT NULL DEFAULT '{}',
  recipient_user_ids  uuid[] NOT NULL DEFAULT '{}',
  status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
  next_run_at         timestamptz,
  last_run_at         timestamptz,
  last_status         text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  archived_at         timestamptz,
  CHECK (frequency <> 'weekly' OR weekday IS NOT NULL),
  CHECK (frequency <> 'monthly' OR month_day IS NOT NULL)
);
CREATE INDEX scheduled_reports_due ON reporting.scheduled_reports (next_run_at) WHERE status = 'active' AND archived_at IS NULL;
SELECT platform.enable_property_rls('reporting.scheduled_reports');
SELECT platform.add_touch_trigger('reporting.scheduled_reports');

CREATE TABLE reporting.scheduled_report_runs (
  id            uuid PRIMARY KEY,
  schedule_id   uuid NOT NULL REFERENCES reporting.scheduled_reports (id) ON DELETE CASCADE,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  trigger       text NOT NULL CHECK (trigger IN ('schedule', 'manual')),
  period_from   date NOT NULL,
  period_to     date NOT NULL,
  status        text NOT NULL CHECK (status IN ('completed', 'partial', 'failed', 'skipped')),
  recipients    int NOT NULL DEFAULT 0,
  delivered     int NOT NULL DEFAULT 0,
  skipped       int NOT NULL DEFAULT 0,
  row_count     int,
  error         text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);
CREATE INDEX scheduled_report_runs_schedule ON reporting.scheduled_report_runs (schedule_id, created_at DESC);
SELECT platform.enable_property_rls('reporting.scheduled_report_runs');

-- Exports delivered by a schedule run (one per recipient, generated under
-- the recipient's own report permission).
ALTER TABLE reporting.exports ADD COLUMN schedule_run_id uuid REFERENCES reporting.scheduled_report_runs (id) ON DELETE SET NULL;

-- ── Self-service report builder (FR-BI-06) ────────────────────────────────
CREATE TABLE reporting.saved_reports (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  name         text NOT NULL,
  dataset      text NOT NULL,
  definition   jsonb NOT NULL,          -- dimensions, metrics, filters, from / to
  shared       boolean NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid
);
SELECT platform.enable_property_rls('reporting.saved_reports');
SELECT platform.add_touch_trigger('reporting.saved_reports');

-- ── Read models of the drill-down and the HR Performance defaults ─────────
-- Charge lines down to the source transaction (folio line) with the drill
-- dimensions: business line, revenue component, outlet and segment.
CREATE VIEW reporting.bi_revenue_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, l.property_id, l.posted_at, l.folio_id, f.number AS folio_number, f.holder_name, l.description,
       CASE WHEN f.source_type IN ('membership_fee', 'membership_renewal', 'membership') THEN 'membership' ELSE l.business_line END AS business_line,
       coalesce(l.revenue_component, l.charge_type) AS revenue_component, l.liability, l.quantity, l.net_amount, l.service_amount, l.tax_amount, l.total,
       f.source_type, f.source_ref, f.customer_id,
       coalesce((SELECT o.name FROM commercial.orders co JOIN commercial.outlets o ON o.id = co.outlet_id
                 WHERE f.source_type = 'pos_order' AND co.id = f.source_id), '') AS outlet,
       coalesce(s.segment, CASE WHEN f.customer_id IS NULL THEN 'walk_in' ELSE 'customer' END) AS segment
FROM billing.folio_lines l JOIN billing.folios f ON f.id = l.folio_id
LEFT JOIN commercial.pricing_snapshots s ON s.id = l.pricing_snapshot_id
WHERE l.voided_at IS NULL;

-- Employees of the P0 master (Headcount until the HRIS registers its own
-- KPI, PRD P5 EP-27).
CREATE VIEW reporting.bi_employees WITH (security_invoker = true) AS
SELECT e.id AS employee_id, e.property_id, e.employee_no, e.full_name, d.name AS department_name, e.job_title, e.join_date, e.status
FROM platform.employees e LEFT JOIN platform.departments d ON d.id = e.department_id
WHERE e.archived_at IS NULL;

-- Caddy ratings (HR Performance: Caddy Rating, roadmap §60).
CREATE VIEW reporting.bi_caddy_ratings WITH (security_invoker = true) AS
SELECT r.id AS rating_id, r.property_id, r.caddy_id, c.code AS caddy_code, c.name AS caddy_name, r.rating, r.channel, r.created_at
FROM golf.caddy_ratings r JOIN golf.caddies c ON c.id = r.caddy_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.bi_caddy_ratings, reporting.bi_employees, reporting.bi_revenue_lines;
DROP TABLE reporting.saved_reports;
ALTER TABLE reporting.exports DROP COLUMN schedule_run_id;
DROP TABLE reporting.scheduled_report_runs, reporting.scheduled_reports;
DROP TABLE reporting.kpi_targets, reporting.kpi_target_plans;
DROP SCHEMA analytics CASCADE;
