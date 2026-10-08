-- Billing workspace (Revenue & Billing → Billing): the billing work on a
-- folio before it is invoiced. A billing record keeps what Finance prepared
-- (who is billed, payment term, references, owner, ready / approved); the
-- exceptions stay derived from the folio, its charges and the bill-to party,
-- so they disappear once the data is fixed. Adjustments are correcting lines
-- on the folio (the original line is never changed) with the previous and the
-- new value; billing events are the history of the record (notes, prepared,
-- marked ready, approved, exception resolved, invoice generated …).
-- Expand-only (Technical Doc §7.5).

-- +goose Up
CREATE TABLE billing.billing_records (
  id                    uuid PRIMARY KEY,
  property_id           uuid NOT NULL REFERENCES platform.properties (id),
  folio_id              uuid NOT NULL UNIQUE REFERENCES billing.folios (id),
  customer_id           uuid,                                   -- bill to: another customer than the folio's
  corporate_account_id  uuid REFERENCES crm.corporate_accounts (id),  -- bill to: the company
  bill_to_name          text,
  bill_to_address       text,
  bill_to_npwp          text,
  bill_to_email         text,
  bill_to_phone         text,
  terms_days            integer CHECK (terms_days >= 0),        -- null: Credit Policies default
  customer_po           text,
  contract_ref          text,
  billing_ref           text,
  notes                 text,                                   -- printed on the invoice
  internal_notes        text,
  attachment_file_ids   uuid[] NOT NULL DEFAULT '{}',           -- platform.files (private)
  owner_id              uuid,
  status                text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'cancelled')),
  ready_at              timestamptz,
  ready_by              uuid,
  approved_amount       numeric(19,4),                          -- approval holds while the amount to invoice is unchanged
  approved_at           timestamptz,
  approved_by           uuid,
  resolved              jsonb NOT NULL DEFAULT '{}'::jsonb,     -- exception code → {reason, by, byName, at}
  version               integer NOT NULL DEFAULT 1,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid,
  updated_at            timestamptz NOT NULL DEFAULT now(),
  updated_by            uuid
);
CREATE INDEX billing_records_owner ON billing.billing_records (property_id, owner_id) WHERE owner_id IS NOT NULL;
SELECT platform.enable_property_rls('billing.billing_records');
SELECT platform.add_touch_trigger('billing.billing_records');

CREATE TABLE billing.billing_adjustments (
  id               uuid PRIMARY KEY,
  property_id      uuid NOT NULL REFERENCES platform.properties (id),
  folio_id         uuid NOT NULL REFERENCES billing.folios (id),
  folio_line_id    uuid NOT NULL REFERENCES billing.folio_lines (id),     -- the line adjusted
  kind             text NOT NULL CHECK (kind IN ('quantity', 'price', 'discount', 'service_charge', 'tax', 'revenue_allocation')),
  previous_value   text NOT NULL,
  new_value        text NOT NULL,
  amount           numeric(19,4) NOT NULL,                                -- change of the amount to invoice
  posted_line_ids  uuid[] NOT NULL DEFAULT '{}',                          -- the correcting lines
  reason           text NOT NULL,
  reference        text,
  approved_at      timestamptz,
  approved_by      uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  created_by       uuid
);
CREATE INDEX billing_adjustments_folio ON billing.billing_adjustments (folio_id, created_at);
SELECT platform.enable_property_rls('billing.billing_adjustments');

CREATE TABLE billing.billing_events (
  id           uuid PRIMARY KEY,
  property_id  uuid NOT NULL REFERENCES platform.properties (id),
  folio_id     uuid NOT NULL REFERENCES billing.folios (id),
  kind         text NOT NULL,          -- note, prepared, marked_ready, approved, adjusted, exception_resolved, owner_assigned, split, invoice_generated …
  summary      text NOT NULL,
  detail       jsonb NOT NULL DEFAULT '{}'::jsonb,
  actor_id     uuid,
  actor_name   text,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX billing_events_folio ON billing.billing_events (folio_id, created_at);
SELECT platform.enable_property_rls('billing.billing_events');

SELECT platform.grant_app('billing');

-- +goose Down
DROP TABLE IF EXISTS billing.billing_events;
DROP TABLE IF EXISTS billing.billing_adjustments;
DROP TABLE IF EXISTS billing.billing_records;
