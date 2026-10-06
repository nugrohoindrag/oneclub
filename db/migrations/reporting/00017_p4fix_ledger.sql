-- PRD P4 FR-INV-01/02, FR-VAL-05 read models of accounting over inventory
-- (Technical Doc §4.2: accounting reads other domains only through reporting
-- views):
--  * acc_inventory_item_accounts: the ledger accounts of an item from its
--    category, inherited from the nearest parent category that sets them
--    (categories are hierarchical with default accounts, FR-INV-02); the
--    posting rules apply where a category sets no account;
--  * acc_inventory_valuation: the valued stock movement lines with the
--    inventory account of the item, the basis of the Stock Valuation = GL
--    inventory reconciliation (consignment lines carry no value).
-- security_invoker: Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.acc_inventory_item_accounts WITH (security_invoker = true) AS
WITH RECURSIVE chain AS (
  SELECT c.id AS category_id, c.id AS ancestor_id, c.parent_id, 0 AS depth FROM inventory.item_categories c
  UNION ALL
  SELECT ch.category_id, p.id, p.parent_id, ch.depth + 1 FROM chain ch JOIN inventory.item_categories p ON p.id = ch.parent_id WHERE ch.depth < 16
), acc AS (
  SELECT ch.category_id,
    (array_agg(nullif(a.inventory_account, '') ORDER BY ch.depth) FILTER (WHERE nullif(a.inventory_account, '') IS NOT NULL))[1] AS inventory_account,
    (array_agg(nullif(a.cogs_account, '') ORDER BY ch.depth) FILTER (WHERE nullif(a.cogs_account, '') IS NOT NULL))[1] AS cogs_account,
    (array_agg(nullif(a.expense_account, '') ORDER BY ch.depth) FILTER (WHERE nullif(a.expense_account, '') IS NOT NULL))[1] AS expense_account,
    (array_agg(nullif(a.waste_account, '') ORDER BY ch.depth) FILTER (WHERE nullif(a.waste_account, '') IS NOT NULL))[1] AS waste_account,
    (array_agg(nullif(a.variance_account, '') ORDER BY ch.depth) FILTER (WHERE nullif(a.variance_account, '') IS NOT NULL))[1] AS variance_account
  FROM chain ch JOIN inventory.item_categories a ON a.id = ch.ancestor_id GROUP BY ch.category_id
)
SELECT i.id AS item_id, i.property_id, i.code AS item_code, i.category_id, c.code AS category_code, acc.inventory_account, acc.cogs_account,
       acc.expense_account, acc.waste_account, acc.variance_account
FROM inventory.items i
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN acc ON acc.category_id = i.category_id;

CREATE VIEW reporting.acc_inventory_valuation WITH (security_invoker = true) AS
SELECT l.id AS line_id, m.property_id, m.id AS movement_id, m.number, m.movement_type, m.source_type, m.business_date, m.warehouse_id,
       w.code AS warehouse_code, l.item_id, l.quantity, l.total_cost, l.consignment, ia.inventory_account
FROM inventory.stock_movement_lines l
JOIN inventory.stock_movements m ON m.id = l.movement_id
JOIN inventory.warehouses w ON w.id = m.warehouse_id
LEFT JOIN reporting.acc_inventory_item_accounts ia ON ia.item_id = l.item_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.acc_inventory_valuation;
DROP VIEW reporting.acc_inventory_item_accounts;
