-- Payment Methods master (FR-MD-03, Naming Convention §18). Processing is P1.
-- Availability per property/outlet is versioned by effective date
-- (FR-MD-08) so changes never alter existing transactions.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS billing;

CREATE TABLE billing.payment_methods (
  id           uuid PRIMARY KEY,
  code         text NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{0,19}$'),
  name         text NOT NULL,
  method_type  text NOT NULL CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account', 'qris', 'card',
                                                   'payment_gateway', 'member_account', 'voucher_prepaid')),
  description  text,
  sort_order   int NOT NULL DEFAULT 0,
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   uuid,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   uuid,
  archived_at  timestamptz
);
SELECT platform.add_touch_trigger('billing.payment_methods');

-- Immutable once effective: a change is a new row with a later effective_from.
CREATE TABLE billing.payment_method_settings (
  id                 uuid PRIMARY KEY,
  payment_method_id  uuid NOT NULL REFERENCES billing.payment_methods (id),
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  outlet_id          uuid,      -- NULL = whole property
  enabled            boolean NOT NULL,
  surcharge_percent  numeric(9,4) NOT NULL DEFAULT 0 CHECK (surcharge_percent >= 0 AND surcharge_percent <= 100),
  effective_from     timestamptz NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
CREATE UNIQUE INDEX payment_method_settings_uniq ON billing.payment_method_settings
  (payment_method_id, property_id, coalesce(outlet_id, '00000000-0000-0000-0000-000000000000'::uuid), effective_from);
SELECT platform.enable_property_rls('billing.payment_method_settings');

SELECT platform.grant_app('billing');

-- +goose Down
DROP SCHEMA billing CASCADE;
