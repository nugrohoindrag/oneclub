-- Invoice Management (Revenue & Billing): manual invoices for the exceptions
-- of automatic billing (adjustment, special billing, non-system transaction,
-- one-off charge) post their lines to a folio of their own, so tax, revenue
-- posting and the AR transfer at issue stay those of folio invoices; the
-- customer references printed on the invoice (PO, contract, billing
-- reference), internal notes that are never printed, line unit & discount
-- and the supporting documents (PO, contract, confirmations) of an invoice.
-- Expand-only (Technical Doc §7.5).

-- +goose Up
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership', 'banquet_event', 'package_booking', 'tournament', 'split', 'quotation', 'manual_invoice'));

ALTER TABLE billing.invoices
  ADD COLUMN customer_po          text,
  ADD COLUMN contract_ref         text,
  ADD COLUMN billing_ref          text,
  ADD COLUMN internal_notes       text,                          -- never on the customer invoice
  ADD COLUMN attachment_file_ids  uuid[] NOT NULL DEFAULT '{}';  -- platform.files (private)

ALTER TABLE billing.invoice_lines
  ADD COLUMN unit             text,
  ADD COLUMN discount_amount  numeric(19,4) NOT NULL DEFAULT 0 CHECK (discount_amount >= 0);

-- +goose Down
ALTER TABLE billing.invoice_lines DROP COLUMN discount_amount, DROP COLUMN unit;
ALTER TABLE billing.invoices DROP COLUMN attachment_file_ids, DROP COLUMN internal_notes, DROP COLUMN billing_ref, DROP COLUMN contract_ref,
  DROP COLUMN customer_po;
ALTER TABLE billing.folios DROP CONSTRAINT folios_source_type_check;
ALTER TABLE billing.folios ADD CONSTRAINT folios_source_type_check CHECK (source_type IN ('golf_booking', 'walk_in', 'membership_fee',
  'membership_renewal', 'bag_storage', 'other', 'reservation', 'stay', 'pos_order', 'voucher_sale', 'sport_entry', 'class_enrollment',
  'locker', 'golf_round', 'membership', 'banquet_event', 'package_booking', 'tournament', 'split', 'quotation'));
