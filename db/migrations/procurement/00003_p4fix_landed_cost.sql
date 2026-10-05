-- PRD P4 FR-VAL-03 (Should) landed cost: a vendor invoice line for freight /
-- duty / insurance allocated to the received goods by value or quantity
-- (the receipt given, else the receipts of the invoice's other lines);
-- inventory revalues the stock through procurement.invoice_price_variance.

-- +goose Up
ALTER TABLE procurement.vendor_invoice_lines
  ADD COLUMN landed_cost_basis text CHECK (landed_cost_basis IS NULL OR landed_cost_basis IN ('value', 'quantity')),
  ADD COLUMN landed_goods_receipt_id uuid REFERENCES procurement.goods_receipts (id);

SELECT platform.grant_app('procurement');

-- +goose Down
ALTER TABLE procurement.vendor_invoice_lines DROP COLUMN landed_goods_receipt_id, DROP COLUMN landed_cost_basis;
