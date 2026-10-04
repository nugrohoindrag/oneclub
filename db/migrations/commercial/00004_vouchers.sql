-- Voucher & Prepaid (PRD P2 EP-19): value and quota vouchers, prepaid
-- balances (e.g. driving range balls), an append-only usage ledger with
-- idempotent redemption, and the link to the deferred revenue sub-ledger.

-- +goose Up
CREATE TABLE commercial.voucher_types (
  id                         uuid PRIMARY KEY,
  property_id                uuid NOT NULL REFERENCES platform.properties (id),
  code                       text NOT NULL,
  name                       text NOT NULL,
  kind                       text NOT NULL CHECK (kind IN ('value', 'quota', 'promo')),
  category                   text NOT NULL DEFAULT 'other' CHECK (category IN ('driving_range_balls', 'sport_entry', 'class_package',
                               'court_package', 'fnb', 'hole_in_one', 'gift', 'promo', 'other')),
  unit                       text NOT NULL DEFAULT 'rupiah' CHECK (unit IN ('rupiah', 'entry', 'session', 'ball', 'round', 'use')),
  face_value                 numeric(19,4) NOT NULL DEFAULT 0 CHECK (face_value >= 0),   -- value vouchers: amount; quota: number of units
  price                      numeric(19,4) NOT NULL DEFAULT 0 CHECK (price >= 0),        -- sale price
  member_price               numeric(19,4) CHECK (member_price IS NULL OR member_price >= 0),
  currency                   char(3) NOT NULL DEFAULT 'IDR',
  validity_days              int CHECK (validity_days IS NULL OR validity_days > 0),
  validity_months            int CHECK (validity_months IS NULL OR validity_months > 0),
  applicable_services        text[] NOT NULL DEFAULT '{}',
  applicable_resource_types  text[] NOT NULL DEFAULT '{}',
  applicable_items           text[] NOT NULL DEFAULT '{}',
  applicable_outlets         uuid[] NOT NULL DEFAULT '{}',
  transferable               boolean NOT NULL DEFAULT false,
  prepaid                    boolean NOT NULL DEFAULT false,      -- counts toward the customer's Prepaid Balance
  discount_percent           numeric(9,4) CHECK (discount_percent IS NULL OR (discount_percent > 0 AND discount_percent <= 100)),
  discount_amount            numeric(19,4) CHECK (discount_amount IS NULL OR discount_amount > 0),
  max_uses                   int CHECK (max_uses IS NULL OR max_uses > 0),
  revenue_component          text NOT NULL DEFAULT 'other',
  status                     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at                 timestamptz NOT NULL DEFAULT now(),
  created_by                 uuid,
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  updated_by                 uuid,
  archived_at                timestamptz,
  UNIQUE (property_id, code)
);
SELECT platform.enable_property_rls('commercial.voucher_types');
SELECT platform.add_touch_trigger('commercial.voucher_types');

CREATE TABLE commercial.vouchers (
  id                  uuid PRIMARY KEY,
  property_id         uuid NOT NULL REFERENCES platform.properties (id),
  code                text NOT NULL,
  voucher_type_id     uuid NOT NULL REFERENCES commercial.voucher_types (id),
  customer_id         uuid REFERENCES crm.customers (id),
  status              text NOT NULL CHECK (status IN ('pending', 'active', 'partially_redeemed', 'redeemed', 'expired', 'void')),
  original_quantity   numeric(19,4) NOT NULL CHECK (original_quantity >= 0),
  remaining_quantity  numeric(19,4) NOT NULL CHECK (remaining_quantity >= 0),
  unit_value          numeric(19,6) NOT NULL DEFAULT 0,     -- revenue recognised per unit used (price paid / quantity)
  price_paid          numeric(19,4) NOT NULL DEFAULT 0,
  uses_count          int NOT NULL DEFAULT 0,
  issued_via          text NOT NULL CHECK (issued_via IN ('sale', 'issue', 'auto', 'migration', 'transfer')),
  source_type         text,
  source_id           uuid,
  folio_id            uuid REFERENCES billing.folios (id),
  issued_at           timestamptz NOT NULL DEFAULT now(),
  expires_at          timestamptz,
  voided_at           timestamptz,
  void_reason         text,
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid,
  UNIQUE (property_id, code),
  CHECK (remaining_quantity <= original_quantity)
);
CREATE INDEX vouchers_customer ON commercial.vouchers (customer_id, status);
CREATE INDEX vouchers_expiry ON commercial.vouchers (expires_at) WHERE status IN ('active', 'partially_redeemed');
SELECT platform.enable_property_rls('commercial.vouchers');
SELECT platform.add_touch_trigger('commercial.vouchers');

-- Usage History. quantity is the signed change of the remaining quantity;
-- amount is the revenue recognised (redemption) or breakage (expiry).
CREATE TABLE commercial.voucher_ledger (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  voucher_id       uuid NOT NULL REFERENCES commercial.vouchers (id),
  entry_type       text NOT NULL CHECK (entry_type IN ('issue', 'sale', 'redemption', 'expiry', 'adjustment', 'transfer', 'void', 'extend', 'reversal')),
  quantity         numeric(19,4) NOT NULL DEFAULT 0,
  amount           numeric(19,4) NOT NULL DEFAULT 0,
  balance_after    numeric(19,4) NOT NULL,
  service_type     text,
  outlet_id        uuid,
  terminal         text,
  reference        text,
  source_type      text,
  source_id        uuid,
  reason           text,
  details          jsonb NOT NULL DEFAULT '{}'::jsonb,
  idempotency_key  text,
  actor_id         uuid,
  actor_name       text,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX voucher_ledger_idempotency ON commercial.voucher_ledger (voucher_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX voucher_ledger_voucher ON commercial.voucher_ledger (voucher_id, created_at);
CREATE INDEX voucher_ledger_time ON commercial.voucher_ledger (property_id, created_at);
SELECT platform.enable_property_rls('commercial.voucher_ledger');
CREATE TRIGGER voucher_ledger_append_only BEFORE UPDATE OR DELETE ON commercial.voucher_ledger
  FOR EACH ROW EXECUTE FUNCTION commercial.forbid_change();

-- Change requests that wait for approval (Extend Expiry, Adjust Balance).
CREATE TABLE commercial.voucher_requests (
  id            uuid PRIMARY KEY,
  property_id   uuid NOT NULL REFERENCES platform.properties (id),
  voucher_id    uuid NOT NULL REFERENCES commercial.vouchers (id),
  request_type  text NOT NULL CHECK (request_type IN ('extend', 'adjust')),
  new_expires_at timestamptz,
  quantity      numeric(19,4),
  reason        text NOT NULL,
  status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
  approval_id   uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,
  decided_at    timestamptz
);
SELECT platform.enable_property_rls('commercial.voucher_requests');

-- Expiry Notification H-30 / H-7 sent once per voucher (FR-VCH-05).
CREATE TABLE commercial.voucher_reminders (
  voucher_id   uuid NOT NULL REFERENCES commercial.vouchers (id),
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  days_before  int NOT NULL,
  sent_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (voucher_id, days_before)
);
SELECT platform.enable_property_rls('commercial.voucher_reminders');

SELECT platform.grant_app('commercial');

-- +goose Down
DROP TABLE commercial.voucher_reminders;
DROP TABLE commercial.voucher_requests;
DROP TABLE commercial.voucher_ledger;
DROP TABLE commercial.vouchers;
DROP TABLE commercial.voucher_types;
