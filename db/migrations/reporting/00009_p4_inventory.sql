-- PRD P4 Inventory read models (EP-28 FR-RPT-P4-01/04, K9): stock balance
-- per warehouse × item × batch, the movement ledger at line level, stock
-- opname variances, waste, theoretical consumption and sales of the P2
-- ledger per outlet, the outlet ↔ warehouse mapping, the asset register with
-- maintenance schedules, depreciation, and the stock availability read model
-- K9 that POS / packages read to mark products as out of stock without
-- calling inventory. Views use security_invoker so RLS of the sources applies.

-- +goose Up
CREATE VIEW reporting.inventory_stock_balances WITH (security_invoker = true) AS
SELECT b.property_id, b.warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name, w.location_type, w.outlet_id, b.item_id, i.code AS item_code,
       i.name AS item_name, coalesce(c.name, i.category) AS category, i.category_id, u.code AS uom, b.batch_id, bt.batch_no, bt.expiry_date,
       b.quantity, b.value, i.consignment, b.last_movement_at, rp.reorder_point, ps.par_level, ps.min_stock
FROM inventory.stock_balances b
JOIN inventory.warehouses w ON w.id = b.warehouse_id
JOIN inventory.items i ON i.id = b.item_id
JOIN inventory.uoms u ON u.id = i.base_uom_id
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN inventory.batches bt ON bt.id = b.batch_id
LEFT JOIN inventory.reorder_points rp ON rp.warehouse_id = b.warehouse_id AND rp.item_id = b.item_id
LEFT JOIN inventory.par_stocks ps ON ps.warehouse_id = b.warehouse_id AND ps.item_id = b.item_id;

CREATE VIEW reporting.inventory_movement_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, m.property_id, m.id AS movement_id, m.number, m.movement_type, m.business_date, m.posted_at, m.warehouse_id,
       w.code AS warehouse_code, w.name AS warehouse_name, w.outlet_id, m.source_type, m.source_id, m.reason, m.cost_center, m.flagged,
       m.reversal_of IS NOT NULL AS reversal, l.item_id, i.code AS item_code, i.name AS item_name, coalesce(c.name, i.category) AS category,
       u.code AS uom, bt.batch_no, l.quantity, l.unit_cost, l.total_cost, l.consignment, l.balance_after, l.value_after, l.seq
FROM inventory.stock_movement_lines l
JOIN inventory.stock_movements m ON m.id = l.movement_id
JOIN inventory.warehouses w ON w.id = m.warehouse_id
JOIN inventory.items i ON i.id = l.item_id
JOIN inventory.uoms u ON u.id = i.base_uom_id
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN inventory.batches bt ON bt.id = l.batch_id;

CREATE VIEW reporting.inventory_opname_lines WITH (security_invoker = true) AS
SELECT ol.id AS line_id, o.property_id, o.id AS opname_id, o.number, o.status, o.snapshot_at, o.counted_at, o.posted_at, w.code AS warehouse_code,
       w.name AS warehouse_name, i.code AS item_code, i.name AS item_name, u.code AS uom, bt.batch_no, ol.system_quantity, ol.expected_quantity,
       ol.counted_quantity, ol.variance_quantity, ol.unit_cost, ol.variance_value, ol.tolerance_percent, ol.over_tolerance, ol.recounts
FROM inventory.stock_opname_lines ol
JOIN inventory.stock_opnames o ON o.id = ol.opname_id
JOIN inventory.warehouses w ON w.id = o.warehouse_id
JOIN inventory.items i ON i.id = ol.item_id
JOIN inventory.uoms u ON u.id = i.base_uom_id
LEFT JOIN inventory.batches bt ON bt.id = ol.batch_id;

CREATE VIEW reporting.inventory_waste WITH (security_invoker = true) AS
SELECT l.id AS line_id, x.property_id, x.id AS waste_id, x.number, x.business_date, x.reason, w.code AS warehouse_code, w.name AS warehouse_name,
       i.code AS item_code, i.name AS item_name, coalesce(c.name, i.category) AS category, u.code AS uom, bt.batch_no, -l.quantity AS quantity,
       l.unit_cost, -l.total_cost AS cost
FROM inventory.waste_records x
JOIN inventory.stock_movement_lines l ON l.movement_id = x.movement_id
JOIN inventory.warehouses w ON w.id = x.warehouse_id
JOIN inventory.items i ON i.id = l.item_id
JOIN inventory.uoms u ON u.id = i.base_uom_id
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN inventory.batches bt ON bt.id = l.batch_id;

CREATE VIEW reporting.inventory_outlet_warehouses WITH (security_invoker = true) AS
SELECT w.property_id, w.id AS warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name, w.outlet_id, o.name AS outlet_name
FROM inventory.warehouses w JOIN commercial.outlets o ON o.id = w.outlet_id
WHERE w.archived_at IS NULL;

CREATE VIEW reporting.inventory_theoretical_consumption WITH (security_invoker = true) AS
SELECT c.property_id, c.outlet_id, c.item_id, i.code AS item_code, i.name AS item_name, u.code AS uom, c.quantity, c.cost_amount, c.occurred_at
FROM inventory.consumption_ledger c JOIN inventory.items i ON i.id = c.item_id JOIN inventory.uoms u ON u.id = i.base_uom_id;

CREATE VIEW reporting.inventory_sales WITH (security_invoker = true) AS
SELECT s.property_id, s.outlet_id, s.product_id, s.quantity, s.net_amount, s.cost_amount, s.has_recipe, s.occurred_at
FROM inventory.consumption_sales s;

CREATE VIEW reporting.inventory_assets WITH (security_invoker = true) AS
SELECT a.id AS asset_id, a.property_id, a.code, a.name, c.name AS category, c.asset_class, a.serial_no, a.brand, a.model, w.name AS location,
       a.cost_center, a.acquisition_date, a.acquisition_cost, a.residual_value, a.accumulated_depreciation, a.book_value,
       coalesce(a.depreciation_method, c.depreciation_method) AS depreciation_method, coalesce(a.useful_life_months, c.useful_life_months) AS useful_life_months,
       a.supplier_id, s.name AS supplier_name, a.warranty_until, a.rentable, a.rental_status, a.usage_hours, a.status, a.disposed_on, a.archived_at
FROM inventory.assets a
JOIN inventory.asset_categories c ON c.id = a.category_id
LEFT JOIN inventory.warehouses w ON w.id = a.warehouse_id
LEFT JOIN procurement.suppliers s ON s.id = a.supplier_id;

CREATE VIEW reporting.inventory_maintenance_schedules WITH (security_invoker = true) AS
SELECT s.id AS schedule_id, s.property_id, a.code AS asset_code, a.name AS asset_name, c.asset_class, s.name AS schedule_name, s.trigger_type,
       s.interval_days, s.interval_hours, s.last_done_on, s.last_done_hours, s.next_due_on, s.reminder_days_before, a.usage_hours,
       a.usage_hours - s.last_done_hours AS hours_since, s.status, a.status AS asset_status,
       (SELECT max(r.completed_at) FROM inventory.maintenance_records r WHERE r.schedule_id = s.id AND r.status = 'completed') AS last_completed_at,
       (SELECT count(*) FROM inventory.maintenance_records r WHERE r.schedule_id = s.id AND r.status IN ('open', 'in_progress')) AS open_work_orders
FROM inventory.maintenance_schedules s
JOIN inventory.assets a ON a.id = s.asset_id
JOIN inventory.asset_categories c ON c.id = a.category_id;

CREATE VIEW reporting.inventory_depreciation WITH (security_invoker = true) AS
SELECT d.id AS line_id, d.property_id, r.number, r.period, r.period_end, a.code AS asset_code, a.name AS asset_name, c.name AS category,
       d.amount, d.accumulated_after, d.book_value_after
FROM inventory.depreciation_lines d
JOIN inventory.depreciation_runs r ON r.id = d.run_id
JOIN inventory.assets a ON a.id = d.asset_id
JOIN inventory.asset_categories c ON c.id = a.category_id;

-- K9 stock availability: on hand, available (on hand − approved quantities
-- not yet issued from the warehouse) and below reorder per warehouse × item;
-- product_id is the retail product sold 1:1 (FR-INV-05).
CREATE VIEW reporting.stock_availability WITH (security_invoker = true) AS
WITH s AS (SELECT warehouse_id, item_id, sum(quantity) AS on_hand FROM inventory.stock_balances GROUP BY 1, 2),
r AS (SELECT q.source_warehouse_id AS warehouse_id, l.item_id, sum(l.base_quantity - l.issued_quantity - l.cancelled_quantity) AS reserved
      FROM inventory.requisition_lines l JOIN inventory.requisitions q ON q.id = l.requisition_id
      WHERE q.status IN ('approved', 'partially_issued') GROUP BY 1, 2)
SELECT w.property_id, s.warehouse_id, s.item_id, i.product_id, w.outlet_id, s.on_hand, s.on_hand - coalesce(r.reserved, 0) AS available,
       coalesce(s.on_hand < coalesce(rp.reorder_point, nullif(ps.min_stock, 0)), false) AS below_reorder
FROM s
JOIN inventory.warehouses w ON w.id = s.warehouse_id
JOIN inventory.items i ON i.id = s.item_id
LEFT JOIN r ON r.warehouse_id = s.warehouse_id AND r.item_id = s.item_id
LEFT JOIN inventory.reorder_points rp ON rp.warehouse_id = s.warehouse_id AND rp.item_id = s.item_id
LEFT JOIN inventory.par_stocks ps ON ps.warehouse_id = s.warehouse_id AND ps.item_id = s.item_id
WHERE w.location_type <> 'transit';

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.stock_availability, reporting.inventory_depreciation, reporting.inventory_maintenance_schedules, reporting.inventory_assets,
  reporting.inventory_sales, reporting.inventory_theoretical_consumption, reporting.inventory_outlet_warehouses, reporting.inventory_waste,
  reporting.inventory_opname_lines, reporting.inventory_movement_lines, reporting.inventory_stock_balances;
