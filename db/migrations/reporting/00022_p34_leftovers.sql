-- PRD P4 §9.2 Banquet Supply (EP-05 FR-VAL-02, EP-06 FR-CNS-03, EP-28): food
-- cost actual vs theoretical per banquet event, one row per event × item:
--  * theoretical: the ingredients of the issued BEO (menu recipes × pax,
--    additional F&B products) with the menu part scaled from the BEO pax to
--    the final pax (pax basis: final, else guaranteed, else expected),
--    valued at the item standard cost (FR-VAL-02: standard cost stays the
--    basis of the theoretical food cost);
--  * actual: the stock deducted for the event by banquet.event_completed (K6)
--    — the inventory movements with source banquet and the event as source,
--    reversals included — valued at the valuation cost (moving average /
--    FIFO), the same amount the ledger posts by rule DEF-INV-COGS-BQT.
-- security_invoker: Row Level Security of the source tables applies.

-- +goose Up
CREATE VIEW reporting.inventory_banquet_food_cost_lines WITH (security_invoker = true) AS
WITH beo AS (
  SELECT b.event_id, b.pax, b.requirements FROM banquet.beos b WHERE b.status = 'issued'
), theo AS (
  SELECT e.id AS event_id, (r->>'itemId')::uuid AS item_id,
    sum(CASE WHEN coalesce(r->>'source', 'menu') = 'menu' AND beo.pax > 0
      THEN (r->>'quantity')::numeric * coalesce(nullif(e.final_pax, 0), nullif(e.guaranteed_pax, 0), e.expected_pax) / beo.pax
      ELSE (r->>'quantity')::numeric END) AS quantity
  FROM banquet.events e JOIN beo ON beo.event_id = e.id CROSS JOIN LATERAL jsonb_array_elements(beo.requirements) r
  WHERE r ? 'itemId' AND nullif(r->>'quantity', '') IS NOT NULL
  GROUP BY 1, 2
), act AS (
  SELECT m.source_id AS event_id, l.item_id, -sum(l.quantity) AS quantity, -sum(l.total_cost) AS cost
  FROM inventory.stock_movement_lines l JOIN inventory.stock_movements m ON m.id = l.movement_id
  WHERE m.source_type = 'banquet' AND m.source_id IS NOT NULL AND NOT l.consignment
  GROUP BY 1, 2
), k AS (
  SELECT event_id, item_id FROM theo UNION SELECT event_id, item_id FROM act
)
SELECT e.property_id, e.id AS event_id, e.number AS event_number, i.id AS item_id, i.code AS item_code, i.name AS item_name, u.code AS uom,
       coalesce(t.quantity, 0) AS theoretical_quantity, i.standard_cost, coalesce(t.quantity, 0) * i.standard_cost AS theoretical_cost,
       coalesce(a.quantity, 0) AS actual_quantity, coalesce(a.cost, 0) AS actual_cost
FROM k
JOIN banquet.events e ON e.id = k.event_id
JOIN inventory.items i ON i.id = k.item_id
JOIN inventory.uoms u ON u.id = i.base_uom_id
LEFT JOIN theo t ON t.event_id = k.event_id AND t.item_id = k.item_id
LEFT JOIN act a ON a.event_id = k.event_id AND a.item_id = k.item_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.inventory_banquet_food_cost_lines;
