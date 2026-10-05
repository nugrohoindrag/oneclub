-- PRD P4 gap fixes (inventory): refunded POS sales return their consumption
-- to stock (FR-CNS-02, source sale_refund) and P2 golf cart usage feeds the
-- usage-based maintenance schedules of the linked assets (FR-AST-02, usage
-- type golf_cart, one usage per cart assignment).

-- +goose Up
ALTER TABLE inventory.stock_movements DROP CONSTRAINT stock_movements_source_type_check;
ALTER TABLE inventory.stock_movements ADD CONSTRAINT stock_movements_source_type_check CHECK (source_type IN ('goods_receipt', 'requisition', 'transfer',
  'adjustment', 'opname', 'waste', 'production', 'sale', 'package', 'banquet', 'golf_round', 'purchase_return', 'consignment', 'revaluation', 'sale_refund'));

ALTER TABLE inventory.asset_usages DROP CONSTRAINT asset_usages_usage_type_check;
ALTER TABLE inventory.asset_usages ADD CONSTRAINT asset_usages_usage_type_check CHECK (usage_type IN ('rental', 'operation', 'golf_cart'));
CREATE UNIQUE INDEX asset_usages_golf_cart ON inventory.asset_usages (asset_id, reference) WHERE usage_type = 'golf_cart';

SELECT platform.grant_app('inventory');

-- +goose Down
DROP INDEX inventory.asset_usages_golf_cart;
DELETE FROM inventory.asset_usages WHERE usage_type = 'golf_cart';
ALTER TABLE inventory.asset_usages DROP CONSTRAINT asset_usages_usage_type_check;
ALTER TABLE inventory.asset_usages ADD CONSTRAINT asset_usages_usage_type_check CHECK (usage_type IN ('rental', 'operation'));
ALTER TABLE inventory.stock_movements DROP CONSTRAINT stock_movements_source_type_check;
ALTER TABLE inventory.stock_movements ADD CONSTRAINT stock_movements_source_type_check CHECK (source_type IN ('goods_receipt', 'requisition', 'transfer',
  'adjustment', 'opname', 'waste', 'production', 'sale', 'package', 'banquet', 'golf_round', 'purchase_return', 'consignment', 'revaluation')) NOT VALID; -- the ledger is append-only
