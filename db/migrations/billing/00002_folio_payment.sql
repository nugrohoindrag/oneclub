-- EP-08 Payment & Folio Foundation: customer accounts (member account /
-- signing bill ledger), golf folios with charge lines from pricing
-- snapshots, payments (online gateway, pay at venue, member charge),
-- deposits, refunds, daily gateway reconciliation, member statements and
-- the daily accounting export (FR-INT-P1-04). Money is numeric(19,4) +
-- currency (Technical Doc §7.2). Financial rows are never deleted: they are
-- voided, cancelled or reversed.

-- +goose Up
-- FR-PAY-01 / FR-MEM-11 Customer Account. A member account is a running
-- ledger: charges increase the balance owed, payments decrease it.
CREATE TABLE billing.customer_accounts (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  customer_id     uuid NOT NULL REFERENCES crm.customers (id),
  account_type    text NOT NULL CHECK (account_type IN ('member', 'corporate', 'customer')),
  member_id       uuid,               -- membership.members (FK added by membership)
  corporate_account_id uuid REFERENCES crm.corporate_accounts (id),
  credit_limit    numeric(19,4),      -- NULL = Member Policy default
  currency        char(3) NOT NULL DEFAULT 'IDR',
  status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'suspended')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX customer_accounts_customer ON billing.customer_accounts (property_id, customer_id, account_type);
SELECT platform.enable_property_rls('billing.customer_accounts');
SELECT platform.add_touch_trigger('billing.customer_accounts');

CREATE TABLE billing.account_entries (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  account_id    uuid NOT NULL REFERENCES billing.customer_accounts (id),
  entry_type    text NOT NULL CHECK (entry_type IN ('charge', 'payment', 'refund', 'adjustment', 'opening_balance')),
  amount        numeric(19,4) NOT NULL,       -- + increases the amount owed, − reduces it
  currency      char(3) NOT NULL,
  description   text NOT NULL,
  folio_id      uuid,
  payment_id    uuid,
  occurred_at   timestamptz NOT NULL DEFAULT now(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid
);
CREATE INDEX account_entries_account ON billing.account_entries (account_id, occurred_at);
SELECT platform.enable_property_rls('billing.account_entries');

-- FR-PAY-01 Folio per booking (or per customer for walk-in / membership).
CREATE TABLE billing.folios (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  customer_id     uuid REFERENCES crm.customers (id),
  guest_id        uuid REFERENCES crm.guests (id),
  holder_name     text NOT NULL,
  source_type     text NOT NULL CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee', 'membership_renewal', 'bag_storage', 'other')),
  source_id       uuid,
  source_ref      text,
  status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'cancelled')),
  currency        char(3) NOT NULL DEFAULT 'IDR',
  version         int NOT NULL DEFAULT 1,       -- ETag / If-Match
  closed_at       timestamptz,
  closed_by       uuid,
  reopened_at     timestamptz,
  reopened_by     uuid,
  reopen_reason   text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, number)
);
CREATE INDEX folios_source ON billing.folios (source_type, source_id);
CREATE INDEX folios_customer ON billing.folios (customer_id);
SELECT platform.enable_property_rls('billing.folios');
SELECT platform.add_touch_trigger('billing.folios');

CREATE TABLE billing.folio_lines (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  folio_id             uuid NOT NULL REFERENCES billing.folios (id),
  charge_type          text NOT NULL CHECK (charge_type IN ('golf_round', 'caddy_fee', 'cart_fee', 'extra_cart', 'caddy_tip',
                         'cancellation_fee', 'no_show_fee', 'membership_fee', 'renewal_fee', 'bag_storage', 'locker',
                         'rain_check_credit', 'discount', 'other')),
  description          text NOT NULL,
  quantity             numeric(19,4) NOT NULL DEFAULT 1,
  unit_price           numeric(19,4) NOT NULL,
  net_amount           numeric(19,4) NOT NULL,
  tax_amount           numeric(19,4) NOT NULL DEFAULT 0,
  service_amount       numeric(19,4) NOT NULL DEFAULT 0,
  total                numeric(19,4) NOT NULL,
  currency             char(3) NOT NULL,
  components           jsonb NOT NULL DEFAULT '[]'::jsonb,   -- all-in breakdown (green fee, caddy fee titipan, buggy, HIO …)
  pricing_snapshot_id  uuid,                                  -- FK added by commercial
  reference_type       text,                                  -- golf_player, caddy, golf_cart …
  reference_id         uuid,
  liability            boolean NOT NULL DEFAULT false,        -- caddy fee / tip held for caddies, not club revenue
  posted_at            timestamptz NOT NULL DEFAULT now(),
  posted_by            uuid,
  voided_at            timestamptz,
  voided_by            uuid,
  void_reason          text,
  created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX folio_lines_folio ON billing.folio_lines (folio_id);
CREATE INDEX folio_lines_reference ON billing.folio_lines (reference_type, reference_id);
SELECT platform.enable_property_rls('billing.folio_lines');

-- FR-PAY-03..06 payments. A pending online payment carries the gateway
-- checkout; the webhook settles it exactly once (external_id is unique).
CREATE TABLE billing.payments (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  folio_id           uuid REFERENCES billing.folios (id),
  account_id         uuid REFERENCES billing.customer_accounts (id),  -- member charge or account settlement
  payment_method_id  uuid REFERENCES billing.payment_methods (id),
  method_type        text NOT NULL CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account', 'qris', 'card',
                       'payment_gateway', 'member_account', 'voucher_prepaid')),
  channel            text NOT NULL CHECK (channel IN ('online', 'venue', 'member_account')),
  purpose            text NOT NULL DEFAULT 'settlement' CHECK (purpose IN ('settlement', 'deposit', 'account_settlement')),
  amount             numeric(19,4) NOT NULL CHECK (amount > 0),
  refunded_amount    numeric(19,4) NOT NULL DEFAULT 0,
  currency           char(3) NOT NULL,
  status             text NOT NULL CHECK (status IN ('pending', 'completed', 'cancelled', 'refunded')),
  integration_code   text,
  external_id        text,
  checkout_url       text,
  qr_string          text,
  va_number          text,
  reference          text,                -- EDC / transfer reference
  payer_name         text,
  expires_at         timestamptz,
  paid_at            timestamptz,
  received_by        uuid,
  cancelled_at       timestamptz,
  cancel_reason      text,
  receipt_sent_at    timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         uuid,
  UNIQUE (property_id, number)
);
CREATE UNIQUE INDEX payments_external ON billing.payments (integration_code, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX payments_folio ON billing.payments (folio_id);
CREATE INDEX payments_paid ON billing.payments (property_id, paid_at) WHERE status IN ('completed', 'refunded');
SELECT platform.enable_property_rls('billing.payments');
SELECT platform.add_touch_trigger('billing.payments');

-- FR-PAY-08 deposits are kept apart and applied to the folio at settlement.
CREATE TABLE billing.deposits (
  id              uuid PRIMARY KEY,
  property_id     uuid NOT NULL REFERENCES platform.properties (id),
  number          text NOT NULL,
  folio_id        uuid REFERENCES billing.folios (id),
  customer_id     uuid REFERENCES crm.customers (id),
  payment_id      uuid NOT NULL REFERENCES billing.payments (id),
  amount          numeric(19,4) NOT NULL CHECK (amount > 0),
  applied_amount  numeric(19,4) NOT NULL DEFAULT 0,
  currency        char(3) NOT NULL,
  status          text NOT NULL CHECK (status IN ('held', 'applied', 'refunded')),
  applied_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('billing.deposits');
SELECT platform.add_touch_trigger('billing.deposits');

-- FR-PAY-07 refunds (to the original method or to the member account);
-- above the Refund Policy limit they wait for an approval.
CREATE TABLE billing.refunds (
  id                   uuid PRIMARY KEY,
  property_id          uuid NOT NULL REFERENCES platform.properties (id),
  number               text NOT NULL,
  payment_id           uuid NOT NULL REFERENCES billing.payments (id),
  folio_id             uuid REFERENCES billing.folios (id),
  amount               numeric(19,4) NOT NULL CHECK (amount > 0),
  currency             char(3) NOT NULL,
  destination          text NOT NULL CHECK (destination IN ('original_method', 'member_account')),
  status               text NOT NULL CHECK (status IN ('pending', 'completed', 'rejected', 'cancelled')),
  reason               text NOT NULL,
  approval_request_id  uuid,
  external_id          text,
  processed_at         timestamptz,
  processed_by         uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid,
  UNIQUE (property_id, number)
);
SELECT platform.enable_property_rls('billing.refunds');
SELECT platform.add_touch_trigger('billing.refunds');

-- FR-PAY-10 daily reconciliation: gateway transactions vs the vendor
-- settlement report; differences are exceptions.
CREATE TABLE billing.reconciliations (
  id                uuid PRIMARY KEY,
  property_id       uuid NOT NULL REFERENCES platform.properties (id),
  business_date     date NOT NULL,
  integration_code  text NOT NULL,
  status            text NOT NULL CHECK (status IN ('matched', 'exceptions')),
  oneclub_count     int NOT NULL,
  oneclub_total     numeric(19,4) NOT NULL,
  settlement_count  int NOT NULL,
  settlement_total  numeric(19,4) NOT NULL,
  exception_count   int NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        uuid
);
CREATE INDEX reconciliations_date ON billing.reconciliations (property_id, business_date DESC);
SELECT platform.enable_property_rls('billing.reconciliations');

CREATE TABLE billing.reconciliation_items (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  reconciliation_id  uuid NOT NULL REFERENCES billing.reconciliations (id),
  external_id        text NOT NULL,
  payment_id         uuid REFERENCES billing.payments (id),
  oneclub_amount     numeric(19,4),
  settlement_amount  numeric(19,4),
  result             text NOT NULL CHECK (result IN ('matched', 'missing_in_settlement', 'missing_in_oneclub', 'amount_mismatch')),
  resolved_at        timestamptz,
  resolved_by        uuid,
  resolution_note    text
);
SELECT platform.enable_property_rls('billing.reconciliation_items');

-- FR-MEM-12 monthly member statements (stored in billing, PRD §9).
CREATE TABLE billing.member_statements (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  account_id       uuid NOT NULL REFERENCES billing.customer_accounts (id),
  period_start     date NOT NULL,
  period_end       date NOT NULL,
  opening_balance  numeric(19,4) NOT NULL,
  total_charges    numeric(19,4) NOT NULL,
  total_payments   numeric(19,4) NOT NULL,
  closing_balance  numeric(19,4) NOT NULL,
  currency         char(3) NOT NULL,
  lines            jsonb NOT NULL DEFAULT '[]'::jsonb,
  file_id          uuid REFERENCES platform.files (id),
  sent_at          timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (account_id, period_start)
);
SELECT platform.enable_property_rls('billing.member_statements');

-- FR-INT-P1-04 daily accounting export.
CREATE TABLE billing.accounting_exports (
  id             uuid PRIMARY KEY,
  property_id    uuid NOT NULL REFERENCES platform.properties (id),
  business_date  date NOT NULL,
  file_id        uuid REFERENCES platform.files (id),
  totals         jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     uuid
);
CREATE INDEX accounting_exports_date ON billing.accounting_exports (property_id, business_date DESC);
SELECT platform.enable_property_rls('billing.accounting_exports');

-- Per-property document numbers (FOL-260401-0001 …).
CREATE TABLE billing.sequences (
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  prefix       text NOT NULL,
  day          date NOT NULL,
  last_value   int NOT NULL,
  PRIMARY KEY (property_id, prefix, day)
);
SELECT platform.enable_property_rls('billing.sequences');

SELECT platform.grant_app('billing');

-- +goose Down
DROP TABLE billing.sequences, billing.accounting_exports, billing.member_statements, billing.reconciliation_items,
  billing.reconciliations, billing.refunds, billing.deposits, billing.payments, billing.folio_lines, billing.folios,
  billing.account_entries, billing.customer_accounts;
