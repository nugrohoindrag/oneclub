-- PRD P4 EP-16 General Accounting and EP-17 Posting Otomatis: chart of
-- accounts (instance level, used per property), books per property with
-- the cut-over date, financial periods (Open → Soft Closed → Closed),
-- append-only journals (Technical Doc §7.4: no UPDATE / DELETE, enforced by
-- trigger and grant; corrections are reversal journals), balanced debit =
-- credit per journal enforced at commit, posting rules, the posting
-- exception queue and the processed events of the event subscribers.

-- +goose Up
-- ── FR-ACC-01 Chart of Accounts ───────────────────────────────────────────
-- One chart for the legal entity (MAIN + MDR are one legal entity, PRD P4
-- §16 #6): consolidated reports add the same accounts across properties.
-- properties restricts an account to some properties (empty = all).
CREATE TABLE accounting.accounts (
  id               uuid PRIMARY KEY,
  code             text NOT NULL UNIQUE,
  name             text NOT NULL,
  name_id          text,
  account_type     text NOT NULL CHECK (account_type IN ('asset', 'liability', 'equity', 'revenue', 'expense', 'cogs')),
  subtype          text NOT NULL DEFAULT 'other',
  parent_id        uuid REFERENCES accounting.accounts (id),
  is_posting       boolean NOT NULL DEFAULT true,
  normal_balance   text NOT NULL CHECK (normal_balance IN ('debit', 'credit')),
  cash_flow        text NOT NULL DEFAULT 'operating' CHECK (cash_flow IN ('cash', 'operating', 'investing', 'financing', 'non_cash')),
  properties       uuid[] NOT NULL DEFAULT '{}',
  business_line    text,
  cost_center      text,
  description      text,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  archived_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
CREATE INDEX accounts_parent ON accounting.accounts (parent_id);
SELECT platform.add_touch_trigger('accounting.accounts');

-- ── EP-23 book of a property: template, cut-over (go-live) date ──────────
CREATE TABLE accounting.books (
  property_id        uuid PRIMARY KEY REFERENCES platform.properties (id),
  template           text NOT NULL,
  status             text NOT NULL DEFAULT 'live' CHECK (status IN ('setup', 'live')),
  cut_over_date      date NOT NULL,
  currency           char(3) NOT NULL DEFAULT 'IDR',
  export_stopped_at  timestamptz,
  export_stopped_by  uuid,
  signoff_note       text,
  loaded_at          timestamptz NOT NULL DEFAULT now(),
  loaded_by          uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
SELECT platform.enable_property_rls('accounting.books');
SELECT platform.add_touch_trigger('accounting.books');

-- ── FR-ACC-06 Financial Period ────────────────────────────────────────────
CREATE TABLE accounting.periods (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  year               int NOT NULL,
  month              int NOT NULL CHECK (month BETWEEN 1 AND 12),
  start_date         date NOT NULL,
  end_date           date NOT NULL,
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'soft_closed', 'closed')),
  checklist          jsonb NOT NULL DEFAULT '[]'::jsonb,
  soft_closed_at     timestamptz,
  soft_closed_by     uuid,
  closed_at          timestamptz,
  closed_by          uuid,
  reopened_at        timestamptz,
  reopened_by        uuid,
  reopen_reason      text,
  reopen_request_id  uuid,
  reopen_status      text CHECK (reopen_status IN ('pending', 'approved', 'rejected')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, year, month)
);
SELECT platform.enable_property_rls('accounting.periods');
SELECT platform.add_touch_trigger('accounting.periods');

-- FR-ACC-07 year-end closing to retained earnings.
CREATE TABLE accounting.fiscal_years (
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  year               int NOT NULL,
  status             text NOT NULL DEFAULT 'closed' CHECK (status IN ('open', 'closed')),
  net_income         numeric(19,4) NOT NULL DEFAULT 0,
  closing_journal_id uuid,
  closed_at          timestamptz NOT NULL DEFAULT now(),
  closed_by          uuid,
  PRIMARY KEY (property_id, year)
);
SELECT platform.enable_property_rls('accounting.fiscal_years');

-- ── FR-ACC-02..04 Journal (append-only) ───────────────────────────────────
CREATE TABLE accounting.journals (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  journal_date         date NOT NULL,
  period_id            uuid NOT NULL REFERENCES accounting.periods (id),
  journal_type         text NOT NULL CHECK (journal_type IN ('automatic', 'manual', 'adjustment', 'recurring', 'reversal', 'opening', 'closing')),
  source_type          text NOT NULL,
  source_id            text,
  source_ref           text,
  event_id             uuid,
  description          text NOT NULL,
  currency             char(3) NOT NULL,
  total                numeric(19,4) NOT NULL CHECK (total > 0),
  reverses_journal_id  uuid REFERENCES accounting.journals (id),
  requested_date       date,
  request_id           uuid,
  posted_at            timestamptz NOT NULL DEFAULT now(),
  posted_by            uuid,
  approved_by          uuid,
  UNIQUE (property_id, number),
  UNIQUE (id, property_id)
);
-- a journal is reversed at most once
CREATE UNIQUE INDEX journals_reversal ON accounting.journals (reverses_journal_id) WHERE reverses_journal_id IS NOT NULL;
CREATE INDEX journals_date ON accounting.journals (property_id, journal_date);
CREATE INDEX journals_source ON accounting.journals (source_type, source_id);
CREATE INDEX journals_event ON accounting.journals (event_id) WHERE event_id IS NOT NULL;
SELECT platform.enable_property_rls('accounting.journals');

CREATE TABLE accounting.journal_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL,
  journal_id         uuid NOT NULL,
  line_no            int NOT NULL,
  journal_date       date NOT NULL,
  account_id         uuid NOT NULL REFERENCES accounting.accounts (id),
  debit              numeric(19,4) NOT NULL DEFAULT 0 CHECK (debit >= 0),
  credit             numeric(19,4) NOT NULL DEFAULT 0 CHECK (credit >= 0),
  description        text,
  business_line      text,
  revenue_component  text,
  component          text,
  cost_center        text,
  department_id      uuid,
  partner_type       text,
  partner_id         uuid,
  partner_name       text,
  rule_id            uuid,
  source_type        text,
  source_id          text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  CHECK ((debit = 0) <> (credit = 0)),
  UNIQUE (journal_id, line_no),
  FOREIGN KEY (journal_id, property_id) REFERENCES accounting.journals (id, property_id)
);
CREATE INDEX journal_lines_account ON accounting.journal_lines (account_id, property_id, journal_date);
CREATE INDEX journal_lines_partner ON accounting.journal_lines (partner_id) WHERE partner_id IS NOT NULL;
SELECT platform.enable_property_rls('accounting.journal_lines');

-- +goose StatementBegin
-- Append-only (Technical Doc §7.4): journals are never changed or removed.
CREATE FUNCTION accounting.forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only: post a reversal journal instead', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;

-- FR-ACC-04: every journal balances (debit = credit = total) with at least
-- two lines, checked when the transaction commits.
CREATE FUNCTION accounting.check_journal_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  jid uuid;
  d numeric; c numeric; n int; t numeric;
BEGIN
  IF TG_TABLE_NAME = 'journals' THEN
    jid := NEW.id;
  ELSE
    jid := NEW.journal_id;
  END IF;
  SELECT coalesce(sum(debit), 0), coalesce(sum(credit), 0), count(*) INTO d, c, n FROM accounting.journal_lines WHERE journal_id = jid;
  SELECT total INTO t FROM accounting.journals WHERE id = jid;
  IF t IS NULL THEN
    RAISE EXCEPTION 'journal line without journal %', jid USING ERRCODE = 'check_violation';
  END IF;
  IF n < 2 OR d <> c OR d <> t THEN
    RAISE EXCEPTION 'journal % is not balanced (debit %, credit %, total %, lines %)', jid, d, c, t, n USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END $$;

-- Re-grants after platform.grant_app('accounting'): the application role
-- may only INSERT and SELECT the append-only tables.
CREATE FUNCTION accounting.protect_append_only() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  app_role text := current_setting('oneclub.app_role', true);
BEGIN
  IF app_role IS NOT NULL AND app_role <> '' THEN
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON accounting.journals, accounting.journal_lines FROM %I', app_role);
  END IF;
END $$;
-- +goose StatementEnd

CREATE TRIGGER journals_append_only BEFORE UPDATE OR DELETE ON accounting.journals FOR EACH ROW EXECUTE FUNCTION accounting.forbid_change();
CREATE TRIGGER journal_lines_append_only BEFORE UPDATE OR DELETE ON accounting.journal_lines FOR EACH ROW EXECUTE FUNCTION accounting.forbid_change();
CREATE CONSTRAINT TRIGGER journals_balanced AFTER INSERT ON accounting.journals DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION accounting.check_journal_balance();
CREATE CONSTRAINT TRIGGER journal_lines_balanced AFTER INSERT ON accounting.journal_lines DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION accounting.check_journal_balance();

-- ── FR-ACC-02 manual journals (request → approval → posted journal) ──────
CREATE TABLE accounting.manual_journals (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  journal_date         date NOT NULL,
  journal_type         text NOT NULL DEFAULT 'manual' CHECK (journal_type IN ('manual', 'adjustment', 'recurring')),
  description          text NOT NULL,
  currency             char(3) NOT NULL,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  lines                jsonb NOT NULL DEFAULT '[]'::jsonb,
  auto_reverse         boolean NOT NULL DEFAULT false,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'posted', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  journal_id           uuid,
  reversal_journal_id  uuid,
  recurring_id         uuid,
  decided_at           timestamptz,
  decision_reason      text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.manual_journals');
SELECT platform.add_touch_trigger('accounting.manual_journals');

-- FR-ACC-08 recurring journals (monthly accruals) with automatic reversal.
CREATE TABLE accounting.recurring_journals (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  code           text NOT NULL,
  name           text NOT NULL,
  description    text,
  lines          jsonb NOT NULL DEFAULT '[]'::jsonb,
  frequency      text NOT NULL DEFAULT 'monthly' CHECK (frequency IN ('monthly', 'quarterly', 'yearly')),
  day_of_month   int NOT NULL DEFAULT 28 CHECK (day_of_month BETWEEN 1 AND 31),
  next_run_date  date NOT NULL,
  end_date       date,
  auto_reverse   boolean NOT NULL DEFAULT false,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  last_run_at    timestamptz,
  runs           int NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('accounting.recurring_journals');
SELECT platform.add_touch_trigger('accounting.recurring_journals');

-- ── FR-PST-01/06 Posting Rules (versioned by effective date) ──────────────
-- event / source + conditions on its attributes → debit and credit account
-- (a concrete account or a default-account role of the Accounting
-- Configuration). Instance level; a property condition narrows a rule.
CREATE TABLE accounting.posting_rules (
  id                 uuid PRIMARY KEY,
  code               text NOT NULL,
  name               text NOT NULL,
  source             text NOT NULL,
  conditions         jsonb NOT NULL DEFAULT '{}'::jsonb,
  debit_account_id   uuid REFERENCES accounting.accounts (id),
  debit_role         text,
  credit_account_id  uuid REFERENCES accounting.accounts (id),
  credit_role        text,
  priority           int NOT NULL DEFAULT 100,
  effective_from     date NOT NULL DEFAULT DATE '2000-01-01',
  effective_to       date,
  description        text,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  UNIQUE (code, effective_from),
  CHECK (debit_account_id IS NOT NULL OR debit_role IS NOT NULL),
  CHECK (credit_account_id IS NOT NULL OR credit_role IS NOT NULL)
);
CREATE INDEX posting_rules_source ON accounting.posting_rules (source) WHERE status = 'active';
SELECT platform.add_touch_trigger('accounting.posting_rules');

-- FR-PST-05 processed events: one row per consumed event (idempotency on
-- top of the outbox marker) with the payload for replay after a fix.
CREATE TABLE accounting.processed_events (
  event_id       uuid PRIMARY KEY,
  property_id    uuid REFERENCES platform.properties (id),
  event_type     text NOT NULL,
  occurred_at    timestamptz,
  payload        jsonb NOT NULL DEFAULT '{}'::jsonb,
  status         text NOT NULL CHECK (status IN ('posted', 'no_posting', 'exception', 'skipped')),
  journal_ids    uuid[] NOT NULL DEFAULT '{}',
  note           text,
  attempts       int NOT NULL DEFAULT 1,
  processed_at   timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX processed_events_type ON accounting.processed_events (property_id, event_type, processed_at DESC);
SELECT platform.enable_property_rls('accounting.processed_events');
SELECT platform.add_touch_trigger('accounting.processed_events');

-- FR-PST-04 posting exceptions (no matching rule, missing account, …).
CREATE TABLE accounting.posting_exceptions (
  id                     uuid PRIMARY KEY,
  property_id            uuid NOT NULL REFERENCES platform.properties (id),
  event_id               uuid,
  event_type             text NOT NULL,
  source_type            text,
  source_id              text,
  reason                 text NOT NULL CHECK (reason IN ('missing_rule', 'missing_account', 'closed_period', 'invalid_payload', 'no_book', 'unbalanced',
                           'processing_error')),
  message                text NOT NULL,
  details                jsonb NOT NULL DEFAULT '{}'::jsonb,
  amount                 numeric(19,4) NOT NULL DEFAULT 0,
  journal_id             uuid,
  status                 text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'ignored')),
  attempts               int NOT NULL DEFAULT 0,
  resolved_at            timestamptz,
  resolved_by            uuid,
  resolution_note        text,
  resolution_journal_ids uuid[] NOT NULL DEFAULT '{}',
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX posting_exceptions_open ON accounting.posting_exceptions (property_id, created_at) WHERE status = 'open';
SELECT platform.enable_property_rls('accounting.posting_exceptions');
SELECT platform.add_touch_trigger('accounting.posting_exceptions');

-- Source documents already posted (folio lines, payments, refunds, deposit
-- applications, deferred revenue entries, allocations …): one journal per
-- source, whatever event triggered the posting (daily summary or per
-- transaction, FR-PST-03/05). part numbers cumulative postings (deposit
-- applications) and void reversals.
CREATE TABLE accounting.posted_sources (
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  source_type    text NOT NULL,
  source_id      uuid NOT NULL,
  part           int NOT NULL DEFAULT 0,
  journal_id     uuid,
  business_date  date,
  key1           text,
  key2           text,
  amount         numeric(19,4) NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_type, source_id, part)
);
CREATE INDEX posted_sources_journal ON accounting.posted_sources (journal_id);
CREATE INDEX posted_sources_day ON accounting.posted_sources (property_id, source_type, business_date);
SELECT platform.enable_property_rls('accounting.posted_sources');

-- K4: the Daily Revenue Report of every closed business day as received.
CREATE TABLE accounting.business_days (
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  business_date  date NOT NULL,
  summary        jsonb NOT NULL DEFAULT '{}'::jsonb,
  journal_ids    uuid[] NOT NULL DEFAULT '{}',
  received_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, business_date)
);
SELECT platform.enable_property_rls('accounting.business_days');

SELECT platform.grant_app('accounting');
SELECT accounting.protect_append_only();

-- +goose Down
DROP TABLE accounting.business_days, accounting.posted_sources, accounting.posting_exceptions, accounting.processed_events,
  accounting.posting_rules, accounting.recurring_journals, accounting.manual_journals;
DROP TABLE accounting.journal_lines, accounting.journals;
DROP FUNCTION accounting.protect_append_only(), accounting.check_journal_balance(), accounting.forbid_change();
DROP TABLE accounting.fiscal_years, accounting.periods, accounting.books, accounting.accounts;
