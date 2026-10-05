-- PRD P3 gap fixes (expand-only):
--  * FR-QUO-01: quotation lines may quote a banquet menu (priced per pax
--    from Banquet → Menus) besides banquet packages, packages and rates;
--    the price source records the catalogue (Commercial package, banquet
--    package / menu).
--  * FR-LOY-02: an earning rule may target a CRM segment; a spend rule with
--    a segment is a segment multiplier stacked on the line's points (like
--    the tier multiplier) for customers in that segment.

-- +goose Up
ALTER TABLE crm.sales_quotation_lines DROP CONSTRAINT sales_quotation_lines_item_type_check;
ALTER TABLE crm.sales_quotation_lines ADD CONSTRAINT sales_quotation_lines_item_type_check
  CHECK (item_type IN ('banquet_package', 'banquet_menu', 'venue', 'product', 'service', 'package', 'other'));
ALTER TABLE crm.sales_quotation_lines DROP CONSTRAINT sales_quotation_lines_price_source_check;
ALTER TABLE crm.sales_quotation_lines ADD CONSTRAINT sales_quotation_lines_price_source_check
  CHECK (price_source IN ('manual', 'pricing_rule', 'product', 'package', 'banquet_package', 'banquet_menu'));

ALTER TABLE crm.loyalty_earning_rules ADD COLUMN customer_segment_id uuid REFERENCES crm.segments (id);
CREATE INDEX loyalty_earning_rules_segment ON crm.loyalty_earning_rules (customer_segment_id) WHERE customer_segment_id IS NOT NULL;

SELECT platform.grant_app('crm');

-- +goose Down
DROP INDEX crm.loyalty_earning_rules_segment;
ALTER TABLE crm.loyalty_earning_rules DROP COLUMN customer_segment_id;
ALTER TABLE crm.sales_quotation_lines DROP CONSTRAINT sales_quotation_lines_price_source_check;
ALTER TABLE crm.sales_quotation_lines ADD CONSTRAINT sales_quotation_lines_price_source_check
  CHECK (price_source IN ('manual', 'pricing_rule', 'product'));
ALTER TABLE crm.sales_quotation_lines DROP CONSTRAINT sales_quotation_lines_item_type_check;
ALTER TABLE crm.sales_quotation_lines ADD CONSTRAINT sales_quotation_lines_item_type_check
  CHECK (item_type IN ('banquet_package', 'venue', 'product', 'service', 'package', 'other'));
