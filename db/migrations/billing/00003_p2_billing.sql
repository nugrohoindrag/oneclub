-- PRD P2 EP-03 Billing Extension on P1's Payment & Folio Foundation
-- (billing/00002): charges from every business line (C1), member charge
-- across lines and offline POS (C2), the tender extension point for
-- Voucher & Prepaid and folio transfer (C3), the deferred revenue
-- sub-ledger (FR-BIL-P2-06) and partner payouts (caddy fee settlement,
-- instructor fees). Expand-only: P1 rows keep their meaning.

-- +goose Up
-- Folios: any business line or reservation of the Reservation Engine.
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership'));
ALTER TABLE billing.folios
  ADD COLUMN business_line   text NOT NULL DEFAULT 'golf',
  ADD COLUMN reservation_id  uuid,               -- reservation.reservations (Reservation Engine)
  ADD COLUMN corporate_name  text;
CREATE INDEX folios_reservation ON billing.folios (reservation_id) WHERE reservation_id IS NOT NULL;

-- Charge lines: the business line and revenue component drive revenue
-- reports and the accounting export; the beneficiary marks money held for
-- a partner (caddy, instructor).
ALTER TABLE billing.folio_lines
  ADD COLUMN business_line      text NOT NULL DEFAULT 'golf',
  ADD COLUMN revenue_component  text,
  ADD COLUMN tax_lines          jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN beneficiary_type   text,   -- caddy | instructor
  ADD COLUMN beneficiary_id     uuid;
-- Expand-only (Tech Doc §7.5): P1's AddCharge does not set the component, so
-- the column stays nullable; readers fall back to charge_type.
UPDATE billing.folio_lines SET revenue_component = charge_type WHERE revenue_component IS NULL;
CREATE INDEX folio_lines_beneficiary ON billing.folio_lines (beneficiary_type, beneficiary_id, posted_at) WHERE beneficiary_id IS NOT NULL;
CREATE INDEX folio_lines_component ON billing.folio_lines (property_id, revenue_component, posted_at);

-- Payments: tenders (voucher / prepaid, folio transfer), POS outlet & shift,
-- offline recording and client idempotency.
ALTER TABLE billing.payments DROP CONSTRAINT payments_method_type_check;
ALTER TABLE billing.payments ADD CONSTRAINT payments_method_type_check CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account',
  'qris', 'card', 'payment_gateway', 'member_account', 'voucher_prepaid', 'folio_transfer'));
ALTER TABLE billing.payments
  ADD COLUMN tender_ref       jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN outlet_id        uuid,
  ADD COLUMN shift_id         uuid,
  ADD COLUMN offline          boolean NOT NULL DEFAULT false,
  ADD COLUMN needs_review     boolean NOT NULL DEFAULT false,   -- offline member charge over the cached limit
  ADD COLUMN idempotency_key  text;
CREATE UNIQUE INDEX payments_idempotency ON billing.payments (property_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX payments_shift ON billing.payments (shift_id) WHERE shift_id IS NOT NULL;

-- Member account: suspension reason (membership suspended), the business
-- line of each entry for the combined Member Statement.
ALTER TABLE billing.customer_accounts ADD COLUMN status_reason text;
ALTER TABLE billing.account_entries
  ADD COLUMN business_line  text,
  ADD COLUMN source_type    text,
  ADD COLUMN source_id      uuid,
  ADD COLUMN offline        boolean NOT NULL DEFAULT false;

-- Deferred revenue sub-ledger (FR-BIL-P2-06): liability per voucher /
-- prepaid / annual fee. Signed amounts: deferral (+) raises the liability,
-- recognition / breakage (−) release it. Liability = sum(amount).
CREATE TABLE billing.deferred_revenue_entries (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  liability_type     text NOT NULL CHECK (liability_type IN ('voucher', 'prepaid', 'annual_fee', 'package')),
  ref_type           text NOT NULL,
  ref_id             uuid NOT NULL,
  entry_type         text NOT NULL CHECK (entry_type IN ('deferral', 'recognition', 'breakage', 'adjustment', 'reversal')),
  amount             numeric(19,4) NOT NULL,
  revenue_component  text NOT NULL,
  description        text NOT NULL,
  idempotency_key    text,
  occurred_at        timestamptz NOT NULL DEFAULT now(),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid
);
CREATE UNIQUE INDEX deferred_revenue_idempotency ON billing.deferred_revenue_entries (property_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX deferred_revenue_ref ON billing.deferred_revenue_entries (ref_type, ref_id);
CREATE INDEX deferred_revenue_time ON billing.deferred_revenue_entries (property_id, occurred_at);
SELECT platform.enable_property_rls('billing.deferred_revenue_entries');

-- Payouts to partners (caddy fee settlement, instructor fees): what the
-- club paid out against held liabilities or as expense (EP-05, EP-15).
CREATE TABLE billing.payouts (
  id                 uuid PRIMARY KEY,
  property_id        uuid NOT NULL REFERENCES platform.properties (id),
  number             text NOT NULL,
  payout_type        text NOT NULL CHECK (payout_type IN ('caddy_fee_settlement', 'instructor_fee')),
  beneficiary_type   text NOT NULL,
  beneficiary_id     uuid NOT NULL,
  beneficiary_name   text NOT NULL,
  source_type        text NOT NULL,
  source_id          uuid NOT NULL,
  amount             numeric(19,4) NOT NULL CHECK (amount >= 0),
  currency           char(3) NOT NULL,
  method_type        text NOT NULL CHECK (method_type IN ('cash', 'bank_transfer')),
  reference          text,
  paid_at            timestamptz NOT NULL DEFAULT now(),
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid,
  UNIQUE (property_id, number),
  UNIQUE (source_type, source_id)
);
SELECT platform.enable_property_rls('billing.payouts');

-- The deferred revenue ledger is append-only: corrections are new entries.
-- +goose StatementBegin
CREATE FUNCTION billing.forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd
CREATE TRIGGER deferred_revenue_append_only BEFORE UPDATE OR DELETE ON billing.deferred_revenue_entries
  FOR EACH ROW EXECUTE FUNCTION billing.forbid_change();

SELECT platform.grant_app('billing');

-- +goose Down
DROP TABLE billing.payouts;
DROP TABLE billing.deferred_revenue_entries;
DROP FUNCTION billing.forbid_change();
ALTER TABLE billing.account_entries DROP COLUMN offline, DROP COLUMN source_id, DROP COLUMN source_type, DROP COLUMN business_line;
ALTER TABLE billing.customer_accounts DROP COLUMN status_reason;
DROP INDEX billing.payments_shift;
DROP INDEX billing.payments_idempotency;
ALTER TABLE billing.payments DROP COLUMN idempotency_key, DROP COLUMN needs_review, DROP COLUMN offline, DROP COLUMN shift_id, DROP COLUMN outlet_id,
  DROP COLUMN tender_ref;
ALTER TABLE billing.payments DROP CONSTRAINT payments_method_type_check;
ALTER TABLE billing.payments ADD CONSTRAINT payments_method_type_check CHECK (method_type IN ('cash', 'bank_transfer', 'virtual_account', 'qris',
  'card', 'payment_gateway', 'member_account', 'voucher_prepaid'));
DROP INDEX billing.folio_lines_component;
DROP INDEX billing.folio_lines_beneficiary;
ALTER TABLE billing.folio_lines DROP COLUMN beneficiary_id, DROP COLUMN beneficiary_type, DROP COLUMN tax_lines, DROP COLUMN revenue_component,
  DROP COLUMN business_line;
DROP INDEX billing.folios_reservation;
ALTER TABLE billing.folios DROP COLUMN corporate_name, DROP COLUMN reservation_id, DROP COLUMN business_line;
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other'));
