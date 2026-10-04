-- PRD P3 EP-17 Unified Folio & Billing Expansion and EP-18 Cashier Shift,
-- End-of-Day & Night Audit on P1's Payment & Folio Foundation (00002) and
-- P2's billing extension (00003): customer folios that merge the folios of
-- every business line, payment schedules (DP, installments, final payment),
-- invoices with gap-free numbers, payment allocations, credit notes and
-- write-offs, corporate credit overrides, billing-level cashier shifts and
-- the business day closed by the night audit (contracts K2 and K4 with P4).
-- Expand-only (Technical Doc §7.5): P1/P2 rows keep their meaning.

-- +goose Up
-- P3 folio sources: banquet / event, cross-line packages, tournaments and
-- the folios created when a bill is split to another payer.
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership', 'banquet_event', 'package_booking', 'tournament', 'split'));

-- Loyalty points are a tender (PRD P3 FR-LOY-05), registered by CRM.
ALTER TABLE billing.payments DROP CONSTRAINT payments_method_type_check;
ALTER TABLE billing.payments ADD CONSTRAINT payments_method_type_check CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account',
  'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid', 'folio_transfer', 'loyalty_points'));
ALTER TABLE billing.payment_methods DROP CONSTRAINT payment_methods_method_type_check;
ALTER TABLE billing.payment_methods ADD CONSTRAINT payment_methods_method_type_check CHECK (method_type IN ('cash', 'bank_transfer',
  'virtual_account', 'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid', 'loyalty_points'));

-- ── FR-BIL-P3-01 Customer Folio ───────────────────────────────────────────
-- One open customer folio per customer or corporate account and property.
-- Folios of reservations (P1–P2) stay where they are and are linked to it
-- ("merged"); the customer folio is the sum of its linked folios.
CREATE TABLE billing.customer_folios (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  customer_id           uuid REFERENCES crm.customers (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  holder_name           text NOT NULL,
  currency              char(3) NOT NULL DEFAULT 'IDR',
  status                text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  version               int NOT NULL DEFAULT 1,
  closed_at             timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number),
  CHECK ((customer_id IS NULL) <> (corporate_account_id IS NULL))
);
CREATE UNIQUE INDEX customer_folios_customer_open ON billing.customer_folios (property_id, customer_id)
  WHERE status = 'open' AND customer_id IS NOT NULL;
CREATE UNIQUE INDEX customer_folios_corporate_open ON billing.customer_folios (property_id, corporate_account_id)
  WHERE status = 'open' AND corporate_account_id IS NOT NULL;
SELECT platform.enable_property_rls('billing.customer_folios');
SELECT platform.add_touch_trigger('billing.customer_folios');

ALTER TABLE billing.folios
  ADD COLUMN customer_folio_id     uuid REFERENCES billing.customer_folios (id),
  ADD COLUMN corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  ADD COLUMN split_from_folio_id   uuid REFERENCES billing.folios (id);
CREATE INDEX folios_customer_folio ON billing.folios (customer_folio_id) WHERE customer_folio_id IS NOT NULL;

-- Month of the last corporate statement sent (statement day job).
ALTER TABLE billing.customer_accounts ADD COLUMN last_statement_month text;

-- ── FR-EOD-03 Business Day ────────────────────────────────────────────────
-- The club's business date advances with the night audit only. A reopened
-- day (status open again) receives adjustments; normal transactions go to
-- the day after the last closed day, or the local date before any audit.
CREATE TABLE billing.business_days (
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  business_date  date NOT NULL,
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  closed_at      timestamptz,
  closed_by      uuid,
  run_id         uuid,
  reopened_at    timestamptz,
  reopened_by    uuid,
  reopen_reason  text,
  reopen_request_id uuid,
  summary        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (property_id, business_date)
);
SELECT platform.enable_property_rls('billing.business_days');
SELECT platform.add_touch_trigger('billing.business_days');

-- +goose StatementBegin
CREATE FUNCTION billing.current_business_date(p uuid) RETURNS date
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT coalesce(
    (SELECT max(business_date) + 1 FROM billing.business_days WHERE property_id = p AND status = 'closed'),
    (now() AT TIME ZONE coalesce((SELECT timezone FROM platform.properties WHERE id = p), (SELECT timezone FROM platform.instance), 'Asia/Jakarta'))::date)
$$;

-- Calendar date of a property (invoice ageing and due dates).
CREATE FUNCTION billing.local_date(p uuid) RETURNS date
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  SELECT (now() AT TIME ZONE coalesce((SELECT timezone FROM platform.properties WHERE id = p), (SELECT timezone FROM platform.instance), 'Asia/Jakarta'))::date
$$;

CREATE FUNCTION billing.set_business_date() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.business_date IS NULL THEN
    NEW.business_date := billing.current_business_date(NEW.property_id);
  END IF;
  RETURN NEW;
END $$;

-- A payment belongs to the business day it was completed on (online
-- payments settle later than they are created).
CREATE FUNCTION billing.set_payment_business_date() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.business_date IS NULL THEN
      NEW.business_date := billing.current_business_date(NEW.property_id);
    END IF;
  ELSIF OLD.status = 'pending' AND NEW.status IN ('completed', 'refunded') THEN
    NEW.business_date := billing.current_business_date(NEW.property_id);
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd

ALTER TABLE billing.folio_lines ADD COLUMN business_date date, ADD COLUMN invoice_id uuid;
ALTER TABLE billing.payments ADD COLUMN business_date date;
ALTER TABLE billing.refunds ADD COLUMN business_date date;
ALTER TABLE billing.account_entries ADD COLUMN invoice_id uuid;
UPDATE billing.folio_lines l SET business_date = (l.posted_at AT TIME ZONE coalesce(p.timezone, i.timezone))::date
  FROM platform.properties p, platform.instance i WHERE p.id = l.property_id AND l.business_date IS NULL;
UPDATE billing.payments x SET business_date = (coalesce(x.paid_at, x.created_at) AT TIME ZONE coalesce(p.timezone, i.timezone))::date
  FROM platform.properties p, platform.instance i WHERE p.id = x.property_id AND x.business_date IS NULL;
UPDATE billing.refunds x SET business_date = (coalesce(x.processed_at, x.created_at) AT TIME ZONE coalesce(p.timezone, i.timezone))::date
  FROM platform.properties p, platform.instance i WHERE p.id = x.property_id AND x.business_date IS NULL;
CREATE TRIGGER folio_lines_business_date BEFORE INSERT ON billing.folio_lines FOR EACH ROW EXECUTE FUNCTION billing.set_business_date();
CREATE TRIGGER refunds_business_date BEFORE INSERT ON billing.refunds FOR EACH ROW EXECUTE FUNCTION billing.set_business_date();
CREATE TRIGGER payments_business_date BEFORE INSERT OR UPDATE OF status ON billing.payments
  FOR EACH ROW EXECUTE FUNCTION billing.set_payment_business_date();
CREATE INDEX folio_lines_business_date ON billing.folio_lines (property_id, business_date);
CREATE INDEX payments_business_date ON billing.payments (property_id, business_date) WHERE status IN ('completed', 'refunded');
CREATE INDEX folio_lines_invoice ON billing.folio_lines (invoice_id) WHERE invoice_id IS NOT NULL;

-- ── FR-EOD-01 Cashier Shift (front desk, sport reception, banquet …) ──────
CREATE TABLE billing.cashier_shifts (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  station         text NOT NULL CHECK (station IN ('front_desk', 'sport_reception', 'banquet', 'golf', 'stay_desk', 'other')),
  cashier_id      uuid NOT NULL,
  cashier_name    text NOT NULL,
  business_date   date NOT NULL,
  currency        char(3) NOT NULL DEFAULT 'IDR',
  opening_float   numeric(19,4) NOT NULL DEFAULT 0 CHECK (opening_float >= 0),
  opened_at       timestamptz NOT NULL DEFAULT now(),
  closed_at       timestamptz,
  counted_cash    numeric(19,4),
  expected_cash   numeric(19,4),
  variance        numeric(19,4),
  close_note      text,
  totals          jsonb NOT NULL DEFAULT '[]'::jsonb,   -- payments per method frozen at close
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX cashier_shifts_open ON billing.cashier_shifts (property_id, cashier_id) WHERE status = 'open';
SELECT platform.enable_property_rls('billing.cashier_shifts');
SELECT platform.add_touch_trigger('billing.cashier_shifts');

CREATE TABLE billing.cash_movements (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  shift_id     uuid NOT NULL REFERENCES billing.cashier_shifts (id),
  kind         text NOT NULL CHECK (kind IN ('cash_in', 'cash_out')),
  amount       numeric(19,4) NOT NULL CHECK (amount > 0),
  reason       text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid
);
CREATE INDEX cash_movements_shift ON billing.cash_movements (shift_id);
SELECT platform.enable_property_rls('billing.cash_movements');

-- Venue payments taken by a cashier with an open billing shift belong to
-- that shift (POS payments keep their POS shift).
ALTER TABLE billing.payments ADD COLUMN cashier_shift_id uuid REFERENCES billing.cashier_shifts (id);
ALTER TABLE billing.refunds ADD COLUMN cashier_shift_id uuid REFERENCES billing.cashier_shifts (id);
CREATE INDEX payments_cashier_shift ON billing.payments (cashier_shift_id) WHERE cashier_shift_id IS NOT NULL;
-- +goose StatementBegin
CREATE FUNCTION billing.set_cashier_shift() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.shift_id IS NOT NULL THEN
    NEW.cashier_shift_id := NULL;   -- POS payments belong to their POS shift
  ELSIF TG_OP = 'INSERT' AND NEW.cashier_shift_id IS NULL AND NEW.received_by IS NOT NULL AND NEW.channel = 'venue' THEN
    SELECT id INTO NEW.cashier_shift_id FROM billing.cashier_shifts
      WHERE property_id = NEW.property_id AND cashier_id = NEW.received_by AND status = 'open';
  END IF;
  RETURN NEW;
END $$;

CREATE FUNCTION billing.set_refund_shift() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.cashier_shift_id IS NULL AND NEW.status = 'completed' AND NEW.processed_by IS NOT NULL THEN
    SELECT id INTO NEW.cashier_shift_id FROM billing.cashier_shifts
      WHERE property_id = NEW.property_id AND cashier_id = NEW.processed_by AND status = 'open';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER payments_cashier_shift BEFORE INSERT OR UPDATE OF shift_id ON billing.payments FOR EACH ROW EXECUTE FUNCTION billing.set_cashier_shift();
CREATE TRIGGER refunds_cashier_shift BEFORE INSERT OR UPDATE OF status ON billing.refunds FOR EACH ROW EXECUTE FUNCTION billing.set_refund_shift();

-- ── FR-EOD-03 Night Audit runs ────────────────────────────────────────────
CREATE TABLE billing.night_audit_runs (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  business_date  date NOT NULL,
  mode           text NOT NULL CHECK (mode IN ('manual', 'auto')),
  status         text NOT NULL CHECK (status IN ('completed', 'blocked')),
  checks         jsonb NOT NULL DEFAULT '[]'::jsonb,
  exceptions     int NOT NULL DEFAULT 0,
  warnings       int NOT NULL DEFAULT 0,
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  run_by         uuid
);
CREATE INDEX night_audit_runs_day ON billing.night_audit_runs (property_id, business_date, started_at DESC);
SELECT platform.enable_property_rls('billing.night_audit_runs');

-- ── FR-BIL-P3-03 / FR-BIL-P3-07 Payment Schedule ──────────────────────────
CREATE TABLE billing.payment_schedules (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text NOT NULL,
  title                 text NOT NULL,
  folio_id              uuid REFERENCES billing.folios (id),
  customer_id           uuid REFERENCES crm.customers (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  source_type           text NOT NULL CHECK (source_type IN ('banquet_event', 'package_booking', 'membership', 'quotation', 'tournament', 'other')),
  source_id             uuid,
  source_ref            text,
  total_amount          numeric(19,4) NOT NULL CHECK (total_amount > 0),
  currency              char(3) NOT NULL DEFAULT 'IDR',
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'cancelled')),
  cancelled_at          timestamptz,
  cancel_reason         text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
-- FR-QUO-07: converting the same quotation twice never creates a second schedule.
CREATE UNIQUE INDEX payment_schedules_source ON billing.payment_schedules (source_type, source_id)
  WHERE source_id IS NOT NULL AND status <> 'cancelled';
SELECT platform.enable_property_rls('billing.payment_schedules');
SELECT platform.add_touch_trigger('billing.payment_schedules');

CREATE TABLE billing.payment_schedule_lines (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  schedule_id       uuid NOT NULL REFERENCES billing.payment_schedules (id),
  seq               int NOT NULL,
  label             text NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('down_payment', 'installment', 'final')),
  due_date          date NOT NULL,
  amount            numeric(19,4) NOT NULL CHECK (amount > 0),
  paid_amount       numeric(19,4) NOT NULL DEFAULT 0 CHECK (paid_amount >= 0),
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'partially_paid', 'paid', 'overdue', 'cancelled')),
  invoice_id        uuid,
  paid_at           timestamptz,
  reminders         text[] NOT NULL DEFAULT '{}',     -- reminder offsets already sent (e.g. D-14, D-8, overdue)
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (schedule_id, seq)
);
CREATE INDEX payment_schedule_lines_due ON billing.payment_schedule_lines (property_id, due_date) WHERE status IN ('pending', 'partially_paid', 'overdue');
SELECT platform.enable_property_rls('billing.payment_schedule_lines');
SELECT platform.add_touch_trigger('billing.payment_schedule_lines');

-- ── FR-BIL-P3-04..06 Invoice, Credit Note, Payment Allocation ─────────────
-- Numbers are gap-free per property and year (INV-2026-00001) and assigned
-- at issue; drafts carry no number. An invoice is the AR source document of
-- P4 (contract K2): it is never deleted, only voided.
CREATE TABLE billing.invoices (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  number                text,
  kind                  text NOT NULL DEFAULT 'standard' CHECK (kind IN ('standard', 'deposit', 'final')),
  customer_id           uuid REFERENCES crm.customers (id),
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),
  account_id            uuid REFERENCES billing.customer_accounts (id),
  customer_folio_id     uuid REFERENCES billing.customer_folios (id),
  folio_id              uuid REFERENCES billing.folios (id),
  schedule_line_id      uuid REFERENCES billing.payment_schedule_lines (id),
  bill_to_name          text NOT NULL,
  bill_to_address       text,
  bill_to_npwp          text,
  bill_to_email         text,
  bill_to_phone         text,
  issue_date            date,
  due_date              date,
  terms_days            int NOT NULL DEFAULT 0 CHECK (terms_days >= 0),
  currency              char(3) NOT NULL DEFAULT 'IDR',
  subtotal              numeric(19,4) NOT NULL DEFAULT 0,
  service_amount        numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount            numeric(19,4) NOT NULL DEFAULT 0,
  total                 numeric(19,4) NOT NULL DEFAULT 0,
  paid_amount           numeric(19,4) NOT NULL DEFAULT 0,
  credited_amount       numeric(19,4) NOT NULL DEFAULT 0,
  written_off_amount    numeric(19,4) NOT NULL DEFAULT 0,
  status                text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'issued', 'partially_paid', 'paid', 'overdue', 'void')),
  public_token          text UNIQUE,
  reminders             text[] NOT NULL DEFAULT '{}',   -- reminder tags already sent (D-7, overdue …)
  notes                 text,
  issued_at             timestamptz,
  issued_by             uuid,
  sent_at               timestamptz,
  paid_at               timestamptz,
  voided_at             timestamptz,
  void_reason           text,
  version               int NOT NULL DEFAULT 1,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX invoices_open ON billing.invoices (property_id, issue_date) WHERE status IN ('issued', 'partially_paid', 'overdue');
CREATE INDEX invoices_account ON billing.invoices (account_id) WHERE account_id IS NOT NULL;
CREATE INDEX invoices_corporate ON billing.invoices (corporate_account_id) WHERE corporate_account_id IS NOT NULL;
CREATE INDEX invoices_customer ON billing.invoices (customer_id) WHERE customer_id IS NOT NULL;
SELECT platform.enable_property_rls('billing.invoices');
SELECT platform.add_touch_trigger('billing.invoices');

ALTER TABLE billing.payment_schedule_lines ADD CONSTRAINT payment_schedule_lines_invoice_fk FOREIGN KEY (invoice_id) REFERENCES billing.invoices (id);

CREATE TABLE billing.invoice_lines (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  invoice_id         uuid NOT NULL REFERENCES billing.invoices (id),
  seq                int NOT NULL,
  folio_line_id      uuid REFERENCES billing.folio_lines (id),
  account_entry_id   uuid REFERENCES billing.account_entries (id),
  description        text NOT NULL,
  quantity           numeric(19,4) NOT NULL DEFAULT 1,
  unit_price         numeric(19,4) NOT NULL DEFAULT 0,
  net_amount         numeric(19,4) NOT NULL DEFAULT 0,
  service_amount     numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount         numeric(19,4) NOT NULL DEFAULT 0,
  total              numeric(19,4) NOT NULL,
  business_line      text,
  revenue_component  text,
  UNIQUE (invoice_id, seq)
);
SELECT platform.enable_property_rls('billing.invoice_lines');

CREATE TABLE billing.credit_notes (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  number        text NOT NULL,
  invoice_id    uuid NOT NULL REFERENCES billing.invoices (id),
  amount        numeric(19,4) NOT NULL CHECK (amount > 0),
  currency      char(3) NOT NULL,
  reason        text NOT NULL,
  status        text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('billing.credit_notes');

-- FR-BIL-P3-06: one payment may settle many invoices or schedule lines.
CREATE TABLE billing.payment_allocations (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  payment_id        uuid NOT NULL REFERENCES billing.payments (id),
  invoice_id        uuid REFERENCES billing.invoices (id),
  schedule_line_id  uuid REFERENCES billing.payment_schedule_lines (id),
  amount            numeric(19,4) NOT NULL CHECK (amount > 0),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  CHECK (invoice_id IS NOT NULL OR schedule_line_id IS NOT NULL)
);
CREATE INDEX payment_allocations_payment ON billing.payment_allocations (payment_id);
CREATE INDEX payment_allocations_invoice ON billing.payment_allocations (invoice_id) WHERE invoice_id IS NOT NULL;
CREATE INDEX payment_allocations_line ON billing.payment_allocations (schedule_line_id) WHERE schedule_line_id IS NOT NULL;
SELECT platform.enable_property_rls('billing.payment_allocations');

-- FR-BIL-P3-08 write-off through approval.
CREATE TABLE billing.write_offs (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  invoice_id           uuid NOT NULL REFERENCES billing.invoices (id),
  amount               numeric(19,4) NOT NULL CHECK (amount > 0),
  reason               text NOT NULL,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  decided_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('billing.write_offs');
SELECT platform.add_touch_trigger('billing.write_offs');

-- FR-BIL-P3-05: a corporate account over its credit limit charges more only
-- with an approved, time-limited credit override.
CREATE TABLE billing.credit_overrides (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  account_id           uuid NOT NULL REFERENCES billing.customer_accounts (id),
  amount               numeric(19,4) NOT NULL CHECK (amount > 0),
  reason               text NOT NULL,
  expires_at           timestamptz NOT NULL,
  status               text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  decided_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX credit_overrides_account ON billing.credit_overrides (account_id) WHERE status = 'approved';
SELECT platform.enable_property_rls('billing.credit_overrides');
SELECT platform.add_touch_trigger('billing.credit_overrides');

SELECT platform.grant_app('billing');

-- +goose Down
DROP TABLE billing.credit_overrides, billing.write_offs, billing.payment_allocations, billing.credit_notes, billing.invoice_lines;
ALTER TABLE billing.payment_schedule_lines DROP CONSTRAINT payment_schedule_lines_invoice_fk;
DROP TABLE billing.invoices, billing.payment_schedule_lines, billing.payment_schedules, billing.night_audit_runs;
DROP TRIGGER refunds_cashier_shift ON billing.refunds;
DROP TRIGGER payments_cashier_shift ON billing.payments;
DROP FUNCTION billing.set_refund_shift(), billing.set_cashier_shift();
ALTER TABLE billing.refunds DROP COLUMN cashier_shift_id;
ALTER TABLE billing.payments DROP COLUMN cashier_shift_id;
DROP TABLE billing.cash_movements, billing.cashier_shifts;
DROP TRIGGER payments_business_date ON billing.payments;
DROP TRIGGER refunds_business_date ON billing.refunds;
DROP TRIGGER folio_lines_business_date ON billing.folio_lines;
ALTER TABLE billing.account_entries DROP COLUMN invoice_id;
ALTER TABLE billing.refunds DROP COLUMN business_date;
ALTER TABLE billing.payments DROP COLUMN business_date;
ALTER TABLE billing.folio_lines DROP COLUMN invoice_id, DROP COLUMN business_date;
DROP FUNCTION billing.set_payment_business_date(), billing.set_business_date(), billing.local_date(uuid), billing.current_business_date(uuid);
DROP TABLE billing.business_days;
ALTER TABLE billing.customer_accounts DROP COLUMN last_statement_month;
ALTER TABLE billing.folios DROP COLUMN split_from_folio_id, DROP COLUMN corporate_account_id, DROP COLUMN customer_folio_id;
DROP TABLE billing.customer_folios;
ALTER TABLE billing.payment_methods DROP CONSTRAINT payment_methods_method_type_check;
ALTER TABLE billing.payment_methods ADD CONSTRAINT payment_methods_method_type_check CHECK (method_type IN ('cash', 'bank_transfer',
  'virtual_account', 'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid'));
ALTER TABLE billing.payments DROP CONSTRAINT payments_method_type_check;
ALTER TABLE billing.payments ADD CONSTRAINT payments_method_type_check CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account',
  'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid', 'folio_transfer'));
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership'));
