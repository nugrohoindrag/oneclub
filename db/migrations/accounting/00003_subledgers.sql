-- PRD P4 EP-18 Accounts Receivable, EP-19 Accounts Payable, EP-20 Cash &
-- Bank, EP-21 Revenue & Tax and EP-23 Accounting Transition: the AR ledger
-- derived from the billing invoices (contract K2; the invoice document stays
-- in billing), AP open items from vendor invoices, payment runs and vendor
-- payments, cash & bank accounts, bank statements and reconciliation, cash
-- deposits, counts, petty cash and transfers, tax codes and tax invoices
-- (e-Faktur / Coretax), service charge pools, revenue allocations (K3),
-- allowance runs, opening balances and account mappings.

-- +goose Up
-- ── EP-18 AR ledger (movements of the AR control account per invoice) ────
CREATE TABLE accounting.ar_entries (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  invoice_id            uuid,
  invoice_number        text,
  account_id            uuid,
  customer_id           uuid,
  corporate_account_id  uuid,
  bill_to_name          text,
  entry_type            text NOT NULL CHECK (entry_type IN ('invoice', 'payment', 'credit_note', 'write_off', 'void', 'opening')),
  entry_date            date NOT NULL,
  due_date              date,
  amount                numeric(19,4) NOT NULL,
  source_id             uuid NOT NULL,
  journal_id            uuid,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entry_type, source_id)
);
CREATE INDEX ar_entries_invoice ON accounting.ar_entries (invoice_id);
SELECT platform.enable_property_rls('accounting.ar_entries');

-- FR-AR-05 allowance for doubtful accounts (provision run with approval).
CREATE TABLE accounting.allowance_runs (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  as_of                date NOT NULL,
  buckets              jsonb NOT NULL DEFAULT '[]'::jsonb,
  required             numeric(19,4) NOT NULL DEFAULT 0,
  current_balance      numeric(19,4) NOT NULL DEFAULT 0,
  adjustment           numeric(19,4) NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'pending_approval' CHECK (status IN ('pending_approval', 'posted', 'rejected', 'no_change')),
  approval_request_id  uuid,
  journal_id           uuid,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.allowance_runs');
SELECT platform.add_touch_trigger('accounting.allowance_runs');

-- ── EP-19 AP open items ───────────────────────────────────────────────────
CREATE TABLE accounting.ap_items (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  supplier_id          uuid NOT NULL,
  supplier_name        text NOT NULL,
  item_type            text NOT NULL CHECK (item_type IN ('vendor_invoice', 'debit_note', 'consignment', 'vendor_bill', 'opening')),
  source_id            uuid NOT NULL,
  number               text NOT NULL,
  supplier_invoice_no  text,
  invoice_date         date NOT NULL,
  due_date             date NOT NULL,
  currency             char(3) NOT NULL,
  subtotal             numeric(19,4) NOT NULL DEFAULT 0,
  tax_amount           numeric(19,4) NOT NULL DEFAULT 0,
  withholding          numeric(19,4) NOT NULL DEFAULT 0,
  amount               numeric(19,4) NOT NULL,
  paid_amount          numeric(19,4) NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'partially_paid', 'paid', 'cancelled')),
  tax_invoice_no       text,
  related_item_id      uuid,
  journal_id           uuid,
  description          text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (item_type, source_id)
);
CREATE INDEX ap_items_supplier ON accounting.ap_items (property_id, supplier_id) WHERE status IN ('open', 'partially_paid');
SELECT platform.enable_property_rls('accounting.ap_items');
SELECT platform.add_touch_trigger('accounting.ap_items');

-- ── EP-20 Cash Accounts & Bank Accounts per property ──────────────────────
CREATE TABLE accounting.bank_accounts (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  code              text NOT NULL,
  name              text NOT NULL,
  kind              text NOT NULL DEFAULT 'bank' CHECK (kind IN ('bank', 'cash', 'petty_cash')),
  bank_name         text,
  account_no        text,
  account_holder    text,
  currency          char(3) NOT NULL DEFAULT 'IDR',
  gl_account_id     uuid NOT NULL REFERENCES accounting.accounts (id),
  statement_format  text NOT NULL DEFAULT 'generic_csv' CHECK (statement_format IN ('generic_csv', 'bca_csv', 'mandiri_csv', 'mt940')),
  float_amount      numeric(19,4),
  custodian         text,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  archived_at       timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        uuid,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('accounting.bank_accounts');
SELECT platform.add_touch_trigger('accounting.bank_accounts');

-- FR-AP-02 payment run: due invoices → approval → payment journal.
CREATE TABLE accounting.payment_runs (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  payment_date         date NOT NULL,
  bank_account_id      uuid NOT NULL REFERENCES accounting.bank_accounts (id),
  method               text NOT NULL DEFAULT 'transfer' CHECK (method IN ('transfer', 'cheque', 'cash')),
  currency             char(3) NOT NULL,
  total                numeric(19,4) NOT NULL DEFAULT 0,
  status               text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'pending_approval', 'approved', 'executed', 'rejected', 'cancelled')),
  approval_request_id  uuid,
  journal_id           uuid,
  notes                text,
  executed_at          timestamptz,
  executed_by          uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.payment_runs');
SELECT platform.add_touch_trigger('accounting.payment_runs');

CREATE TABLE accounting.payment_run_lines (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  run_id       uuid NOT NULL REFERENCES accounting.payment_runs (id),
  ap_item_id   uuid NOT NULL REFERENCES accounting.ap_items (id),
  supplier_id  uuid NOT NULL,
  amount       numeric(19,4) NOT NULL CHECK (amount <> 0),
  UNIQUE (run_id, ap_item_id)
);
SELECT platform.enable_property_rls('accounting.payment_run_lines');

CREATE TABLE accounting.vendor_payments (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  number           text NOT NULL,
  run_id           uuid REFERENCES accounting.payment_runs (id),
  supplier_id      uuid NOT NULL,
  supplier_name    text NOT NULL,
  paid_date        date NOT NULL,
  currency         char(3) NOT NULL,
  amount           numeric(19,4) NOT NULL,
  method           text NOT NULL,
  bank_account_id  uuid REFERENCES accounting.bank_accounts (id),
  reference        text,
  allocations      jsonb NOT NULL DEFAULT '[]'::jsonb,
  journal_id       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.vendor_payments');

-- ── FR-BNK-02 bank statements (mutasi) and transactions ───────────────────
CREATE TABLE accounting.bank_statements (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  number           text NOT NULL,
  bank_account_id  uuid NOT NULL REFERENCES accounting.bank_accounts (id),
  format           text NOT NULL,
  filename         text,
  period_from      date,
  period_to        date,
  opening_balance  numeric(19,4),
  closing_balance  numeric(19,4),
  line_count       int NOT NULL DEFAULT 0,
  duplicate_count  int NOT NULL DEFAULT 0,
  imported_at      timestamptz NOT NULL DEFAULT now(),
  imported_by      uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.bank_statements');

CREATE TABLE accounting.bank_transactions (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  bank_account_id  uuid NOT NULL REFERENCES accounting.bank_accounts (id),
  statement_id     uuid REFERENCES accounting.bank_statements (id),
  tx_date          date NOT NULL,
  description      text NOT NULL DEFAULT '',
  reference        text,
  amount           numeric(19,4) NOT NULL CHECK (amount <> 0),
  balance          numeric(19,4),
  fingerprint      text NOT NULL,
  status           text NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched', 'matched', 'ignored')),
  reconciliation_id uuid,
  matched_at       timestamptz,
  matched_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (bank_account_id, fingerprint)
);
CREATE INDEX bank_transactions_open ON accounting.bank_transactions (bank_account_id, tx_date) WHERE status = 'unmatched';
SELECT platform.enable_property_rls('accounting.bank_transactions');

-- FR-BNK-03 bank reconciliation and its matches.
CREATE TABLE accounting.bank_reconciliations (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  bank_account_id    uuid NOT NULL REFERENCES accounting.bank_accounts (id),
  statement_date     date NOT NULL,
  statement_balance  numeric(19,4) NOT NULL,
  book_balance       numeric(19,4),
  difference         numeric(19,4),
  status             text NOT NULL DEFAULT 'in_progress' CHECK (status IN ('in_progress', 'completed')),
  completed_at       timestamptz,
  completed_by       uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.bank_reconciliations');
SELECT platform.add_touch_trigger('accounting.bank_reconciliations');

CREATE TABLE accounting.bank_matches (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  reconciliation_id    uuid REFERENCES accounting.bank_reconciliations (id),
  bank_transaction_id  uuid NOT NULL REFERENCES accounting.bank_transactions (id),
  journal_line_id      uuid NOT NULL,
  journal_id           uuid NOT NULL,
  amount               numeric(19,4) NOT NULL,
  match_type           text NOT NULL CHECK (match_type IN ('auto', 'manual', 'journal', 'gateway')),
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (journal_line_id)
);
CREATE INDEX bank_matches_tx ON accounting.bank_matches (bank_transaction_id);
SELECT platform.enable_property_rls('accounting.bank_matches');

-- FR-BNK-04 cash deposits of cashier shifts, FR-BNK-05 petty cash, cash
-- counts and transfers between cash and bank accounts.
CREATE TABLE accounting.cash_transactions (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  number              text NOT NULL,
  kind                text NOT NULL CHECK (kind IN ('deposit', 'count', 'expense', 'replenishment', 'transfer')),
  tx_date             date NOT NULL,
  from_account_id     uuid REFERENCES accounting.bank_accounts (id),
  to_account_id       uuid REFERENCES accounting.bank_accounts (id),
  expected            numeric(19,4),
  amount              numeric(19,4) NOT NULL DEFAULT 0,
  variance            numeric(19,4) NOT NULL DEFAULT 0,
  fee                 numeric(19,4) NOT NULL DEFAULT 0,
  expense_account_id  uuid REFERENCES accounting.accounts (id),
  shift_refs          text[] NOT NULL DEFAULT '{}',
  reference           text,
  description         text,
  denominations       jsonb NOT NULL DEFAULT '{}'::jsonb,
  journal_id          uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.cash_transactions');

-- ── EP-21 Tax Configuration: tax codes mapped to the tax & service rules ──
CREATE TABLE accounting.tax_codes (
  id               uuid PRIMARY KEY,
  code             text NOT NULL UNIQUE,
  name             text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('output_vat', 'input_vat', 'local_tax', 'service_charge', 'withholding')),
  rate_percent     numeric(9,4) NOT NULL DEFAULT 0,
  account_id       uuid NOT NULL REFERENCES accounting.accounts (id),
  rule_codes       text[] NOT NULL DEFAULT '{}',
  transaction_code text,
  description      text,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid
);
SELECT platform.add_touch_trigger('accounting.tax_codes');

-- FR-REV-07 tax invoices (faktur pajak keluaran from invoices, masukan from
-- vendor invoices) and their exports for e-Faktur / Coretax.
CREATE TABLE accounting.tax_exports (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  number       text NOT NULL,
  format       text NOT NULL CHECK (format IN ('csv', 'xml')),
  tax_period   text NOT NULL,
  file_id      uuid,
  invoices     int NOT NULL DEFAULT 0,
  content      text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.tax_exports');

CREATE TABLE accounting.tax_invoices (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  direction         text NOT NULL CHECK (direction IN ('output', 'input')),
  source_type       text NOT NULL,
  source_id         uuid NOT NULL,
  source_number     text,
  partner_id        uuid,
  partner_name      text NOT NULL,
  partner_npwp      text,
  partner_address   text,
  invoice_date      date NOT NULL,
  tax_period        text NOT NULL,
  dpp               numeric(19,4) NOT NULL DEFAULT 0,
  ppn               numeric(19,4) NOT NULL DEFAULT 0,
  ppnbm             numeric(19,4) NOT NULL DEFAULT 0,
  transaction_code  text NOT NULL DEFAULT '01',
  faktur_number     text,
  status            text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'exported', 'uploaded', 'cancelled')),
  export_id         uuid REFERENCES accounting.tax_exports (id),
  replaces_id       uuid REFERENCES accounting.tax_invoices (id),
  lines             jsonb NOT NULL DEFAULT '[]'::jsonb,
  external_id       text,
  upload_error      text,
  uploaded_at       timestamptz,
  uploaded_by       uuid,
  cancelled_at      timestamptz,
  cancel_reason     text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tax_invoices_source ON accounting.tax_invoices (direction, source_type, source_id) WHERE status <> 'cancelled';
CREATE INDEX tax_invoices_period ON accounting.tax_invoices (property_id, tax_period);
SELECT platform.enable_property_rls('accounting.tax_invoices');
SELECT platform.add_touch_trigger('accounting.tax_invoices');

-- FR-REV-06 service charge pool per period with the department basis.
CREATE TABLE accounting.service_charge_pools (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  year             int NOT NULL,
  month            int NOT NULL CHECK (month BETWEEN 1 AND 12),
  collected        numeric(19,4) NOT NULL DEFAULT 0,
  reserve_percent  numeric(9,4) NOT NULL DEFAULT 0,
  reserve          numeric(19,4) NOT NULL DEFAULT 0,
  distributable    numeric(19,4) NOT NULL DEFAULT 0,
  basis            text NOT NULL,
  lines            jsonb NOT NULL DEFAULT '[]'::jsonb,
  status           text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved')),
  approved_at      timestamptz,
  approved_by      uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, year, month)
);
SELECT platform.enable_property_rls('accounting.service_charge_pools');
SELECT platform.add_touch_trigger('accounting.service_charge_pools');

-- K3 revenue allocation of packages and promotions as received.
CREATE TABLE accounting.revenue_allocations (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  source_type  text NOT NULL,
  source_id    uuid NOT NULL,
  reference    text,
  business_line text,
  total        numeric(19,4) NOT NULL DEFAULT 0,
  discount     numeric(19,4) NOT NULL DEFAULT 0,
  components   jsonb NOT NULL DEFAULT '[]'::jsonb,
  event_id     uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id, event_id)
);
SELECT platform.enable_property_rls('accounting.revenue_allocations');

-- FR-REV-01 revenue allocation rules: the net of a bundled charge without
-- a component snapshot is split into components by percent.
CREATE TABLE accounting.revenue_allocation_rules (
  id               uuid PRIMARY KEY,
  code             text NOT NULL,
  name             text NOT NULL,
  business_line    text,
  match_component  text NOT NULL,
  components       jsonb NOT NULL DEFAULT '[]'::jsonb,
  effective_from   date NOT NULL,
  effective_to     date,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  archived_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  updated_by       uuid,
  UNIQUE (code, effective_from)
);
SELECT platform.add_touch_trigger('accounting.revenue_allocation_rules');

-- ── EP-23 opening balances and account mappings ───────────────────────────
CREATE TABLE accounting.opening_balances (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  number         text NOT NULL,
  balance_date   date NOT NULL,
  description    text NOT NULL,
  status         text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'posted')),
  total_debit    numeric(19,4) NOT NULL DEFAULT 0,
  total_credit   numeric(19,4) NOT NULL DEFAULT 0,
  journal_id     uuid,
  posted_at      timestamptz,
  posted_by      uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('accounting.opening_balances');
SELECT platform.add_touch_trigger('accounting.opening_balances');

CREATE TABLE accounting.opening_balance_lines (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  batch_id       uuid NOT NULL REFERENCES accounting.opening_balances (id),
  line_no        int NOT NULL,
  account_id     uuid NOT NULL REFERENCES accounting.accounts (id),
  debit          numeric(19,4) NOT NULL DEFAULT 0 CHECK (debit >= 0),
  credit         numeric(19,4) NOT NULL DEFAULT 0 CHECK (credit >= 0),
  partner_type   text CHECK (partner_type IN ('supplier', 'ar_account', 'customer')),
  partner_id     uuid,
  partner_name   text,
  document_no    text,
  document_date  date,
  due_date       date,
  description    text,
  UNIQUE (batch_id, line_no)
);
SELECT platform.enable_property_rls('accounting.opening_balance_lines');

-- FR-TRS-05 / FR-MIG-P4-01 mapping of Excel Finance items to the CoA.
CREATE TABLE accounting.account_mappings (
  id             uuid PRIMARY KEY,
  source_system  text NOT NULL DEFAULT 'excel_finance',
  source_code    text NOT NULL,
  source_name    text,
  account_id     uuid NOT NULL REFERENCES accounting.accounts (id),
  notes          text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     uuid,
  UNIQUE (source_system, source_code)
);
SELECT platform.add_touch_trigger('accounting.account_mappings');

SELECT platform.grant_app('accounting');
SELECT accounting.protect_append_only();

-- +goose Down
DROP TABLE accounting.account_mappings, accounting.opening_balance_lines, accounting.opening_balances, accounting.revenue_allocation_rules,
  accounting.revenue_allocations,
  accounting.service_charge_pools, accounting.tax_invoices, accounting.tax_exports, accounting.tax_codes, accounting.cash_transactions,
  accounting.bank_matches, accounting.bank_reconciliations, accounting.bank_transactions, accounting.bank_statements, accounting.vendor_payments,
  accounting.payment_run_lines, accounting.payment_runs, accounting.bank_accounts, accounting.ap_items, accounting.allowance_runs, accounting.ar_entries;
