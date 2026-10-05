-- PRD P5 EP-18 tier benefit "F&B discount" at the POS (member tier class,
-- product owner request): the order keeps the loyalty tier of its customer
-- when it was opened (read through the hook wired by internal/app; commercial
-- does not import crm) and every F&B line its tier discount, shown as a
-- separate discount line ("Gold member 5%") after the promotions (Pricing
-- Policies "Member tier discount at the POS"). Expand-only.

-- +goose Up
ALTER TABLE commercial.orders
  ADD COLUMN tier_code              text,
  ADD COLUMN tier_name              text,
  ADD COLUMN tier_discount_percent  numeric(5,2) CHECK (tier_discount_percent IS NULL OR tier_discount_percent BETWEEN 0 AND 100),
  ADD COLUMN tier_discount_label    text;
ALTER TABLE commercial.order_lines ADD COLUMN tier_discount numeric(19,4) NOT NULL DEFAULT 0 CHECK (tier_discount >= 0);

-- +goose Down
ALTER TABLE commercial.order_lines DROP COLUMN tier_discount;
ALTER TABLE commercial.orders DROP COLUMN tier_discount_label, DROP COLUMN tier_discount_percent, DROP COLUMN tier_name, DROP COLUMN tier_code;
