-- PRD P3 CRM Sales (crm/sales): EP-01 Lead Management, EP-02 Sales Pipeline &
-- Opportunity, EP-03 Quotation & Conversion, EP-04 Sales Target & Commission.
-- Business lines follow the crm.quotation_accepted contract
-- (docs/p3-p4-contracts.md); statuses follow PRD P3 §7.6 (snake_case).

-- +goose Up
-- ── sales teams (FR-LEAD-05 round-robin per line) ─────────────────────────
CREATE TABLE crm.sales_teams (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  code             text NOT NULL,
  name             text NOT NULL,
  lines            text[] NOT NULL DEFAULT '{}',   -- business lines handled (empty: every line)
  manager_user_id  uuid REFERENCES platform.users (id),
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  archived_at      timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.sales_teams');
SELECT platform.add_touch_trigger('crm.sales_teams');

CREATE TABLE crm.sales_team_members (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  team_id           uuid NOT NULL REFERENCES crm.sales_teams (id),
  user_id           uuid NOT NULL REFERENCES platform.users (id),
  lines             text[] NOT NULL DEFAULT '{}',  -- empty: the lines of the team
  last_assigned_at  timestamptz,                   -- round-robin pointer
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (team_id, user_id)
);
SELECT platform.enable_property_rls('crm.sales_team_members');
SELECT platform.add_touch_trigger('crm.sales_team_members');

-- ── pipelines & stages (FR-PIPE-01, PRD P3 §16.1) ────────────────────────
CREATE TABLE crm.sales_pipelines (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  code         text NOT NULL,
  name         text NOT NULL,
  lines        text[] NOT NULL DEFAULT '{}',   -- default pipeline of these business lines (empty: any line)
  sort_order   int NOT NULL DEFAULT 100,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.sales_pipelines');
SELECT platform.add_touch_trigger('crm.sales_pipelines');

CREATE TABLE crm.sales_pipeline_stages (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  pipeline_id  uuid NOT NULL REFERENCES crm.sales_pipelines (id),
  code         text NOT NULL,
  name         text NOT NULL,
  probability  numeric(5,2) NOT NULL DEFAULT 0 CHECK (probability BETWEEN 0 AND 100),
  sort_order   int NOT NULL DEFAULT 100,
  kind         text NOT NULL DEFAULT 'open' CHECK (kind IN ('open', 'won', 'lost')),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz,
  UNIQUE (pipeline_id, code)
);
CREATE INDEX sales_pipeline_stages_pipeline ON crm.sales_pipeline_stages (pipeline_id, sort_order);
SELECT platform.enable_property_rls('crm.sales_pipeline_stages');
SELECT platform.add_touch_trigger('crm.sales_pipeline_stages');

-- ── leads (FR-LEAD-01..10) ────────────────────────────────────────────────
CREATE TABLE crm.sales_leads (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  name                  text NOT NULL,
  company_name          text,
  phone                 text,
  email                 text,
  source                text NOT NULL CHECK (source IN ('whatsapp', 'instagram', 'facebook', 'tiktok', 'website_form', 'walk_in',
                          'member_referral', 'email', 'phone', 'event', 'import', 'other')),
  channel               text,                    -- business number, department address, campaign / page
  line                  text NOT NULL DEFAULT 'other' CHECK (line IN ('wedding', 'banquet', 'mice', 'event', 'tournament', 'stay', 'golf',
                          'package', 'membership', 'other')),
  event_type            text CHECK (event_type IS NULL OR event_type IN ('wedding', 'meeting', 'conference', 'gathering', 'birthday', 'tournament', 'other')),
  event_date            date,
  pax                   int CHECK (pax IS NULL OR pax > 0),
  budget                numeric(19,4) CHECK (budget IS NULL OR budget >= 0),
  currency              char(3) NOT NULL DEFAULT 'IDR',
  notes                 text,
  message               text,                    -- first inbound message (website, WhatsApp, e-mail)
  status                text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'contacted', 'qualified', 'unqualified', 'converted')),
  owner_user_id         uuid REFERENCES platform.users (id),
  assigned_at           timestamptz,
  assignment_method     text CHECK (assignment_method IS NULL OR assignment_method IN ('manual', 'round_robin', 'fixed', 'transfer', 'import')),
  customer_id           uuid REFERENCES crm.customers (id),          -- matched (FR-LEAD-03) or converted customer
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  referrer_customer_id  uuid REFERENCES crm.customers (id),          -- Member Referral (FR-LEAD-04)
  marketing_consent     boolean NOT NULL DEFAULT false,                -- FR-LEAD-09 (UU PDP)
  consent_at            timestamptz,
  consent_source        text,
  first_response_due_at timestamptz,                                   -- FR-LEAD-06
  first_responded_at    timestamptz,
  sla_reminded_at       timestamptz,
  sla_escalated_at      timestamptz,
  qualified_at          timestamptz,
  unqualified_at        timestamptz,
  unqualified_reason    text CHECK (unqualified_reason IS NULL OR unqualified_reason IN ('not_interested', 'budget', 'date_unavailable',
                          'duplicate', 'spam', 'no_response', 'competitor', 'other')),
  unqualified_note      text,
  converted_at          timestamptz,
  opportunity_id        uuid,                                          -- FK added below
  last_activity_at      timestamptz,
  external_ref          text,                                          -- import reference (idempotent re-import, EP-25)
  source_ref            text,                                          -- e-mail message id (idempotent capture)
  anonymized_at         timestamptz,                                   -- retention (UU PDP, PRD P3 §16 #19)
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX sales_leads_external_ref ON crm.sales_leads (property_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE UNIQUE INDEX sales_leads_source_ref ON crm.sales_leads (property_id, source, source_ref) WHERE source_ref IS NOT NULL;
CREATE INDEX sales_leads_open_phone ON crm.sales_leads (property_id, phone) WHERE status IN ('new', 'contacted', 'qualified');
CREATE INDEX sales_leads_open_email ON crm.sales_leads (property_id, lower(email)) WHERE status IN ('new', 'contacted', 'qualified');
CREATE INDEX sales_leads_sla ON crm.sales_leads (first_response_due_at) WHERE first_responded_at IS NULL;
CREATE INDEX sales_leads_owner ON crm.sales_leads (property_id, owner_user_id, status);
SELECT platform.enable_property_rls('crm.sales_leads');
SELECT platform.add_touch_trigger('crm.sales_leads');

-- Lead Assignment history (FR-LEAD-05, FR-LEAD-10: transfer keeps history).
CREATE TABLE crm.sales_lead_assignments (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  lead_id       uuid NOT NULL REFERENCES crm.sales_leads (id),
  from_user_id  uuid REFERENCES platform.users (id),
  to_user_id    uuid REFERENCES platform.users (id),
  method        text NOT NULL CHECK (method IN ('manual', 'round_robin', 'fixed', 'transfer', 'import')),
  reason        text,
  assigned_by   uuid,
  assigned_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_lead_assignments_lead ON crm.sales_lead_assignments (lead_id, assigned_at);
SELECT platform.enable_property_rls('crm.sales_lead_assignments');

-- ── opportunities (FR-PIPE-02..07) ────────────────────────────────────────
CREATE TABLE crm.sales_opportunities (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  title                 text NOT NULL,
  pipeline_id           uuid NOT NULL REFERENCES crm.sales_pipelines (id),
  stage_id              uuid NOT NULL REFERENCES crm.sales_pipeline_stages (id),
  line                  text NOT NULL DEFAULT 'other' CHECK (line IN ('wedding', 'banquet', 'mice', 'event', 'tournament', 'stay', 'golf',
                          'package', 'membership', 'other')),
  lead_id               uuid REFERENCES crm.sales_leads (id),
  customer_id           uuid REFERENCES crm.customers (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  owner_user_id         uuid REFERENCES platform.users (id),
  expected_value        numeric(19,4) NOT NULL DEFAULT 0 CHECK (expected_value >= 0),
  currency              char(3) NOT NULL DEFAULT 'IDR',
  probability           numeric(5,2) NOT NULL DEFAULT 0 CHECK (probability BETWEEN 0 AND 100),
  expected_close_date   date,
  event_type            text CHECK (event_type IS NULL OR event_type IN ('wedding', 'meeting', 'conference', 'gathering', 'birthday', 'tournament', 'other')),
  event_date            date,
  end_date              date,
  pax                   int CHECK (pax IS NULL OR pax > 0),
  venue_resource_id     uuid,                    -- reservation resource (EP-15); plain reference across modules
  package_ref           text,
  status                text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'won', 'lost')),
  stage_changed_at      timestamptz NOT NULL DEFAULT now(),
  won_at                timestamptz,
  won_quotation_id      uuid,                    -- FK added below
  lost_at               timestamptz,
  lost_reason           text CHECK (lost_reason IS NULL OR lost_reason IN ('price', 'date_unavailable', 'competitor', 'cancelled', 'budget',
                          'no_response', 'other')),
  lost_note             text,
  notes                 text,
  external_ref          text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX sales_opportunities_board ON crm.sales_opportunities (pipeline_id, stage_id) WHERE status = 'open';
CREATE INDEX sales_opportunities_customer ON crm.sales_opportunities (customer_id);
CREATE UNIQUE INDEX sales_opportunities_external_ref ON crm.sales_opportunities (property_id, external_ref) WHERE external_ref IS NOT NULL;
SELECT platform.enable_property_rls('crm.sales_opportunities');
SELECT platform.add_touch_trigger('crm.sales_opportunities');
ALTER TABLE crm.sales_leads ADD CONSTRAINT sales_leads_opportunity_fk FOREIGN KEY (opportunity_id) REFERENCES crm.sales_opportunities (id);

-- Stage moves are recorded (FR-PIPE-03).
CREATE TABLE crm.sales_stage_history (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  opportunity_id  uuid NOT NULL REFERENCES crm.sales_opportunities (id),
  from_stage_id   uuid REFERENCES crm.sales_pipeline_stages (id),
  to_stage_id     uuid NOT NULL REFERENCES crm.sales_pipeline_stages (id),
  probability     numeric(5,2) NOT NULL,
  note            text,
  changed_by      uuid,
  changed_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_stage_history_opportunity ON crm.sales_stage_history (opportunity_id, changed_at);
SELECT platform.enable_property_rls('crm.sales_stage_history');

-- ── activities & follow-ups (FR-LEAD-08, FR-PIPE-05) ──────────────────────
CREATE TABLE crm.sales_activities (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  lead_id         uuid REFERENCES crm.sales_leads (id),
  opportunity_id  uuid REFERENCES crm.sales_opportunities (id),
  customer_id     uuid REFERENCES crm.customers (id),
  activity_type   text NOT NULL CHECK (activity_type IN ('call', 'whatsapp', 'email', 'meeting', 'site_visit', 'food_tasting', 'note', 'task')),
  direction       text NOT NULL DEFAULT 'outbound' CHECK (direction IN ('inbound', 'outbound', 'internal')),
  subject         text NOT NULL,
  notes           text,
  due_at          timestamptz,                   -- a follow-up is open until completed
  assigned_to     uuid REFERENCES platform.users (id),
  status          text NOT NULL DEFAULT 'completed' CHECK (status IN ('open', 'completed', 'cancelled')),
  occurred_at     timestamptz,
  completed_at    timestamptz,
  outcome         text,
  cancel_reason   text,
  reminded_at     timestamptz,
  source          text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'whatsapp', 'email', 'website', 'system')),
  external_ref    text,                          -- WhatsApp message id / e-mail message id
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  CHECK (lead_id IS NOT NULL OR opportunity_id IS NOT NULL OR customer_id IS NOT NULL)
);
CREATE INDEX sales_activities_due ON crm.sales_activities (property_id, due_at) WHERE status = 'open';
CREATE INDEX sales_activities_lead ON crm.sales_activities (lead_id, created_at) WHERE lead_id IS NOT NULL;
CREATE INDEX sales_activities_opportunity ON crm.sales_activities (opportunity_id, created_at) WHERE opportunity_id IS NOT NULL;
CREATE UNIQUE INDEX sales_activities_external_ref ON crm.sales_activities (property_id, source, external_ref) WHERE external_ref IS NOT NULL;
SELECT platform.enable_property_rls('crm.sales_activities');
SELECT platform.add_touch_trigger('crm.sales_activities');

-- ── quotations (FR-QUO-01..08) ────────────────────────────────────────────
-- Yearly gap-free counters (QUO-2026-00012).
CREATE TABLE crm.sales_sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL,
  year         int NOT NULL,
  last_value   int NOT NULL,
  PRIMARY KEY (property_id, prefix, year)
);
SELECT platform.enable_property_rls('crm.sales_sequences');

-- One row per version: the number is shared, revising keeps the old row
-- (status revised) and only one version is active (FR-QUO-03).
CREATE TABLE crm.sales_quotations (
  id                         uuid PRIMARY KEY,
  property_id                uuid NOT NULL REFERENCES platform.properties (id),
  number                     text NOT NULL,
  version                    int NOT NULL DEFAULT 1 CHECK (version > 0),
  opportunity_id             uuid REFERENCES crm.sales_opportunities (id),
  lead_id                    uuid REFERENCES crm.sales_leads (id),
  customer_id                uuid REFERENCES crm.customers (id),
  corporate_account_id       uuid REFERENCES crm.corporate_accounts (id),
  owner_user_id              uuid REFERENCES platform.users (id),       -- sales credited with the deal (commission)
  line                       text NOT NULL DEFAULT 'other' CHECK (line IN ('wedding', 'banquet', 'mice', 'event', 'tournament', 'stay', 'golf',
                               'package', 'membership', 'other')),
  event_type                 text CHECK (event_type IS NULL OR event_type IN ('wedding', 'meeting', 'conference', 'gathering', 'birthday', 'tournament', 'other')),
  event_date                 date,
  end_date                   date,
  pax                        int CHECK (pax IS NULL OR pax > 0),
  venue_resource_id          uuid,
  package_ref                text,
  title                      text NOT NULL,
  currency                   char(3) NOT NULL DEFAULT 'IDR',
  pricing_mode               text NOT NULL DEFAULT 'plus_plus' CHECK (pricing_mode IN ('nett', 'plus_plus')),
  tax_codes                  text[] NOT NULL DEFAULT '{}',
  header_discount            numeric(19,4) NOT NULL DEFAULT 0 CHECK (header_discount >= 0),
  header_discount_percent    numeric(7,4),
  subtotal                   numeric(19,4) NOT NULL DEFAULT 0,
  discount                   numeric(19,4) NOT NULL DEFAULT 0,
  discount_percent           numeric(7,4) NOT NULL DEFAULT 0,
  net_amount                 numeric(19,4) NOT NULL DEFAULT 0,
  service_amount             numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount                 numeric(19,4) NOT NULL DEFAULT 0,
  total                      numeric(19,4) NOT NULL DEFAULT 0,
  valid_until                date NOT NULL,
  option_date                date,                                       -- tentative venue hold option date (FR-QUO-04)
  status                     text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'sent', 'accepted', 'rejected',
                               'expired', 'revised')),
  approval_status            text NOT NULL DEFAULT 'not_required' CHECK (approval_status IN ('not_required', 'required', 'pending',
                               'approved', 'rejected')),
  approval_request_id        uuid,
  approved_discount_percent  numeric(7,4),
  payment_terms              jsonb NOT NULL DEFAULT '[]'::jsonb,
  terms                      text,
  notes                      text,
  policy_version             int NOT NULL DEFAULT 0,
  public_token               text UNIQUE,
  token_expires_at           timestamptz,
  sent_at                    timestamptz,
  sent_via                   text[] NOT NULL DEFAULT '{}',
  accepted_at                timestamptz,
  accepted_via               text CHECK (accepted_via IS NULL OR accepted_via IN ('staff', 'public_link')),
  accepted_by_name           text,
  rejected_at                timestamptz,
  reject_reason              text,
  expired_at                 timestamptz,
  revised_at                 timestamptz,
  revised_from_id            uuid REFERENCES crm.sales_quotations (id),
  paid_amount                numeric(19,4) NOT NULL DEFAULT 0,          -- money attributed to the deal (commission recognition)
  paid_at                    timestamptz,                               -- deal paid in full (PRD P3 §16 #4)
  created_at                 timestamptz NOT NULL DEFAULT now(),
  created_by                 uuid,
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  updated_by                 uuid,
  UNIQUE (property_id, number, version)
);
CREATE UNIQUE INDEX sales_quotations_active_version ON crm.sales_quotations (property_id, number) WHERE status <> 'revised';
CREATE INDEX sales_quotations_opportunity ON crm.sales_quotations (opportunity_id);
CREATE INDEX sales_quotations_customer ON crm.sales_quotations (customer_id, status);
CREATE INDEX sales_quotations_expiry ON crm.sales_quotations (valid_until) WHERE status IN ('sent', 'draft', 'pending_approval');
SELECT platform.enable_property_rls('crm.sales_quotations');
SELECT platform.add_touch_trigger('crm.sales_quotations');
ALTER TABLE crm.sales_opportunities ADD CONSTRAINT sales_opportunities_won_quotation_fk FOREIGN KEY (won_quotation_id) REFERENCES crm.sales_quotations (id);

CREATE TABLE crm.sales_quotation_lines (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  quotation_id           uuid NOT NULL REFERENCES crm.sales_quotations (id) ON DELETE CASCADE,
  line_no                int NOT NULL,
  item_type              text NOT NULL CHECK (item_type IN ('banquet_package', 'venue', 'product', 'service', 'package', 'other')),
  item_ref               text,
  service_type           text,
  description            text NOT NULL,
  quantity               numeric(19,4) NOT NULL CHECK (quantity > 0),
  unit_price             numeric(19,4) NOT NULL CHECK (unit_price >= 0),
  discount               numeric(19,4) NOT NULL DEFAULT 0 CHECK (discount >= 0),   -- line discount
  header_discount_share  numeric(19,4) NOT NULL DEFAULT 0,                         -- share of the quotation discount
  total                  numeric(19,4) NOT NULL,                                   -- quantity × unit price − discounts (contract)
  net_amount             numeric(19,4) NOT NULL DEFAULT 0,
  service_amount         numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount             numeric(19,4) NOT NULL DEFAULT 0,
  gross_total            numeric(19,4) NOT NULL DEFAULT 0,                         -- incl. tax & service
  price_source           text NOT NULL DEFAULT 'manual' CHECK (price_source IN ('manual', 'pricing_rule', 'product')),
  pricing                jsonb NOT NULL DEFAULT '{}'::jsonb,                       -- price snapshot (rule, tax lines)
  UNIQUE (quotation_id, line_no)
);
SELECT platform.enable_property_rls('crm.sales_quotation_lines');

-- Quotation Acceptance (FR-QUO-05): one decision per version — the public
-- link is single-use; name, IP and time are kept.
CREATE TABLE crm.sales_quotation_decisions (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  quotation_id    uuid NOT NULL REFERENCES crm.sales_quotations (id),
  decision        text NOT NULL CHECK (decision IN ('accepted', 'rejected')),
  via             text NOT NULL CHECK (via IN ('staff', 'public_link')),
  name            text,
  ip              text,
  user_agent      text,
  terms_accepted  boolean NOT NULL DEFAULT false,
  note            text,
  decided_by      uuid,
  decided_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (quotation_id)
);
SELECT platform.enable_property_rls('crm.sales_quotation_decisions');

-- ── targets & commission (FR-COM-01..05) ──────────────────────────────────
CREATE TABLE crm.sales_targets (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  user_id         uuid REFERENCES platform.users (id),
  team_id         uuid REFERENCES crm.sales_teams (id),
  line            text CHECK (line IS NULL OR line IN ('wedding', 'banquet', 'mice', 'event', 'tournament', 'stay', 'golf', 'package',
                    'membership', 'other')),
  period_type     text NOT NULL DEFAULT 'month' CHECK (period_type IN ('month', 'quarter', 'year', 'custom')),
  period_start    date NOT NULL,
  period_end      date NOT NULL,
  target_revenue  numeric(19,4) NOT NULL DEFAULT 0 CHECK (target_revenue >= 0),
  target_deals    int NOT NULL DEFAULT 0 CHECK (target_deals >= 0),
  currency        char(3) NOT NULL DEFAULT 'IDR',
  notes           text,
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  CHECK (period_end >= period_start)
);
CREATE INDEX sales_targets_period ON crm.sales_targets (property_id, period_start, period_end);
SELECT platform.enable_property_rls('crm.sales_targets');
SELECT platform.add_touch_trigger('crm.sales_targets');

CREATE TABLE crm.sales_commission_schemes (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  code            text NOT NULL,
  name            text NOT NULL,
  scheme_type     text NOT NULL DEFAULT 'percent' CHECK (scheme_type IN ('percent', 'tiered', 'flat')),
  basis           text NOT NULL DEFAULT 'net' CHECK (basis IN ('net', 'total')),
  rates           jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{line, percent, flatAmount}]
  tiers           jsonb NOT NULL DEFAULT '[]'::jsonb,   -- [{minAchievementPercent, percent}]
  user_id         uuid REFERENCES platform.users (id),  -- empty: every sales of the property / team
  team_id         uuid REFERENCES crm.sales_teams (id),
  effective_from  date NOT NULL,
  effective_to    date,
  clawback_days   int NOT NULL DEFAULT 90 CHECK (clawback_days >= 0),
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  archived_at     timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('crm.sales_commission_schemes');
SELECT platform.add_touch_trigger('crm.sales_commission_schemes');

-- Money received for a deal (accepted quotation), attributed from billing
-- events (billing.invoice_paid / payment_settled / refund_processed /
-- invoice_voided) — crm never imports billing (Technical Doc §4.2 #3).
CREATE TABLE crm.sales_deal_payments (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  quotation_id  uuid NOT NULL REFERENCES crm.sales_quotations (id),
  source_type   text NOT NULL CHECK (source_type IN ('invoice', 'payment', 'refund', 'manual')),
  source_id     uuid NOT NULL,
  schedule_id   uuid,                                -- billing payment schedule the money belongs to
  amount        numeric(19,4) NOT NULL,              -- negative for refunds
  occurred_at   timestamptz NOT NULL DEFAULT now(),
  reversed_at   timestamptz,
  reverse_reason text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id)
);
CREATE INDEX sales_deal_payments_quotation ON crm.sales_deal_payments (quotation_id);
CREATE INDEX sales_deal_payments_schedule ON crm.sales_deal_payments (schedule_id) WHERE schedule_id IS NOT NULL;
SELECT platform.enable_property_rls('crm.sales_deal_payments');

CREATE TABLE crm.sales_commission_statements (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  user_id              uuid NOT NULL REFERENCES platform.users (id),
  period               text NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
  period_start         date NOT NULL,
  period_end           date NOT NULL,
  earned               numeric(19,4) NOT NULL DEFAULT 0,
  clawback             numeric(19,4) NOT NULL DEFAULT 0,
  adjustments          numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  currency             char(3) NOT NULL DEFAULT 'IDR',
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'rejected', 'paid')),
  approval_request_id  uuid,
  submitted_at         timestamptz,
  approved_at          timestamptz,
  rejected_reason      text,
  paid_at              timestamptz,
  paid_reference       text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number),
  UNIQUE (property_id, user_id, period)
);
SELECT platform.enable_property_rls('crm.sales_commission_statements');
SELECT platform.add_touch_trigger('crm.sales_commission_statements');

CREATE TABLE crm.sales_commissions (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  user_id         uuid NOT NULL REFERENCES platform.users (id),
  quotation_id    uuid REFERENCES crm.sales_quotations (id),
  opportunity_id  uuid REFERENCES crm.sales_opportunities (id),
  scheme_id       uuid REFERENCES crm.sales_commission_schemes (id),
  line            text,
  kind            text NOT NULL CHECK (kind IN ('earned', 'clawback', 'adjustment')),
  basis_amount    numeric(19,4) NOT NULL DEFAULT 0,
  rate_percent    numeric(7,4),
  amount          numeric(19,4) NOT NULL,
  currency        char(3) NOT NULL DEFAULT 'IDR',
  recognized_on   date NOT NULL,
  period          text NOT NULL CHECK (period ~ '^\d{4}-\d{2}$'),
  statement_id    uuid REFERENCES crm.sales_commission_statements (id),
  source_type     text NOT NULL DEFAULT 'deal' CHECK (source_type IN ('deal', 'refund', 'invoice_void', 'manual')),
  source_id       uuid,
  reason          text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid
);
CREATE UNIQUE INDEX sales_commissions_once ON crm.sales_commissions (quotation_id, kind, source_type, source_id)
  WHERE quotation_id IS NOT NULL AND source_id IS NOT NULL;
CREATE INDEX sales_commissions_open ON crm.sales_commissions (property_id, user_id, period) WHERE statement_id IS NULL;
SELECT platform.enable_property_rls('crm.sales_commissions');

SELECT platform.grant_app('crm');

-- +goose Down
DROP TABLE crm.sales_commissions, crm.sales_commission_statements, crm.sales_deal_payments, crm.sales_commission_schemes, crm.sales_targets,
  crm.sales_quotation_decisions, crm.sales_quotation_lines;
ALTER TABLE crm.sales_opportunities DROP CONSTRAINT sales_opportunities_won_quotation_fk;
DROP TABLE crm.sales_quotations, crm.sales_sequences, crm.sales_activities, crm.sales_stage_history;
ALTER TABLE crm.sales_leads DROP CONSTRAINT sales_leads_opportunity_fk;
DROP TABLE crm.sales_opportunities, crm.sales_lead_assignments, crm.sales_leads, crm.sales_pipeline_stages, crm.sales_pipelines,
  crm.sales_team_members, crm.sales_teams;
