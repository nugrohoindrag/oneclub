-- PRD P4 Procurement read models (EP-28 FR-RPT-P4-02/04/05): purchase
-- requisitions with their first purchase order (PR → PO lead time),
-- purchase orders and order lines with received / outstanding quantities and
-- values, goods receipt lines with the delivery schedule (on time), purchase
-- return lines, vendor invoices and invoice lines with the 3-way matching
-- result. Views use security_invoker so RLS of the sources applies.

-- +goose Up
CREATE VIEW reporting.procurement_requisitions WITH (security_invoker = true) AS
SELECT r.id AS requisition_id, r.property_id, r.number, r.source, r.status, r.title, r.warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name,
       r.cost_center, r.category, r.needed_by, r.currency, r.estimated_total, r.source_ref, r.event_id, r.created_at, r.submitted_at, r.approved_at,
       (SELECT count(*) FROM procurement.purchase_requisition_lines l WHERE l.requisition_id = r.id AND l.status = 'open')::int AS lines,
       (SELECT min(o.created_at) FROM procurement.purchase_requisition_lines l
         JOIN procurement.purchase_order_line_sources x ON x.requisition_line_id = l.id
         JOIN procurement.purchase_order_lines pl ON pl.id = x.purchase_order_line_id
         JOIN procurement.purchase_orders o ON o.id = pl.purchase_order_id
         WHERE l.requisition_id = r.id AND o.status <> 'cancelled') AS first_ordered_at
FROM procurement.purchase_requisitions r
LEFT JOIN inventory.warehouses w ON w.id = r.warehouse_id;

CREATE VIEW reporting.procurement_orders WITH (security_invoker = true) AS
SELECT o.id AS purchase_order_id, o.property_id, o.number, o.version, o.order_type, o.source, o.status, o.order_date, o.expected_date,
       o.supplier_id, s.code AS supplier_code, s.name AS supplier_name, o.warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name,
       o.currency, o.subtotal, o.tax_total, o.total, o.approved_at, o.sent_at, o.closed_at, o.cancelled_at, o.created_at,
       coalesce((SELECT sum(round(l.unit_price * (1 - l.discount_percent / 100) * greatest(l.quantity - l.cancelled_quantity
         - CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity ELSE l.received_quantity - l.returned_quantity END, 0), 4))
         FROM procurement.purchase_order_lines l WHERE l.purchase_order_id = o.id), 0) AS outstanding_value
FROM procurement.purchase_orders o
JOIN procurement.suppliers s ON s.id = o.supplier_id
LEFT JOIN inventory.warehouses w ON w.id = o.warehouse_id;

CREATE VIEW reporting.procurement_order_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, o.property_id, o.id AS purchase_order_id, o.number, o.order_type, o.status, o.order_date, coalesce(l.expected_date, o.expected_date)
       AS expected_date, o.supplier_id, s.code AS supplier_code, s.name AS supplier_name, o.warehouse_id, w.code AS warehouse_code,
       w.name AS warehouse_name, o.currency, l.line_no, l.item_id, i.code AS item_code, coalesce(i.name, l.description) AS item_name,
       coalesce(c.name, i.category) AS category, u.code AS uom, l.quantity, l.cancelled_quantity,
       CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity ELSE l.received_quantity END AS received_quantity,
       l.returned_quantity, l.invoiced_quantity, round(l.unit_price * (1 - l.discount_percent / 100), 6) AS net_unit_price, l.line_subtotal,
       l.tax_amount, l.line_total,
       greatest(l.quantity - l.cancelled_quantity - CASE WHEN o.order_type = 'service' THEN l.service_confirmed_quantity
         ELSE l.received_quantity - l.returned_quantity END, 0) AS outstanding_quantity
FROM procurement.purchase_order_lines l
JOIN procurement.purchase_orders o ON o.id = l.purchase_order_id
JOIN procurement.suppliers s ON s.id = o.supplier_id
LEFT JOIN inventory.warehouses w ON w.id = o.warehouse_id
LEFT JOIN inventory.items i ON i.id = l.item_id
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN inventory.uoms u ON u.id = l.uom_id;

CREATE VIEW reporting.procurement_receipt_lines WITH (security_invoker = true) AS
SELECT gl.id AS line_id, g.property_id, g.id AS goods_receipt_id, g.number, g.status, g.received_date, g.delivery_note_no, g.approval_reason,
       g.purchase_order_id, o.number AS po_number, g.supplier_id, s.code AS supplier_code, s.name AS supplier_name, g.warehouse_id,
       w.code AS warehouse_code, w.name AS warehouse_name, g.currency, gl.item_id, i.code AS item_code, coalesce(i.name, gl.description) AS item_name,
       coalesce(c.name, i.category) AS category, u.code AS uom, gl.delivered_quantity, gl.accepted_quantity, gl.rejected_quantity, gl.rejection_reason,
       gl.returned_quantity, gl.unit_cost, gl.total_cost, gl.batch_no, gl.expiry_date, coalesce(pl.expected_date, o.expected_date) AS expected_date,
       g.received_date <= coalesce(pl.expected_date, o.expected_date, g.received_date) AS on_time
FROM procurement.goods_receipt_lines gl
JOIN procurement.goods_receipts g ON g.id = gl.goods_receipt_id
JOIN procurement.suppliers s ON s.id = g.supplier_id
LEFT JOIN procurement.purchase_orders o ON o.id = g.purchase_order_id
LEFT JOIN procurement.purchase_order_lines pl ON pl.id = gl.purchase_order_line_id
LEFT JOIN inventory.warehouses w ON w.id = g.warehouse_id
LEFT JOIN inventory.items i ON i.id = gl.item_id
LEFT JOIN inventory.item_categories c ON c.id = i.category_id
LEFT JOIN inventory.uoms u ON u.id = gl.uom_id;

CREATE VIEW reporting.procurement_return_lines WITH (security_invoker = true) AS
SELECT rl.id AS line_id, r.property_id, r.id AS purchase_return_id, r.number, r.return_date, r.reason, g.number AS gr_number, r.supplier_id,
       s.code AS supplier_code, s.name AS supplier_name, r.warehouse_id, w.code AS warehouse_code, w.name AS warehouse_name, r.currency,
       i.code AS item_code, coalesce(i.name, rl.description) AS item_name, u.code AS uom, rl.quantity, rl.unit_cost, rl.total_cost, rl.tax_amount,
       rl.batch_no, rl.reason AS line_reason, d.number AS debit_note_number
FROM procurement.purchase_return_lines rl
JOIN procurement.purchase_returns r ON r.id = rl.purchase_return_id
JOIN procurement.goods_receipts g ON g.id = r.goods_receipt_id
JOIN procurement.suppliers s ON s.id = r.supplier_id
LEFT JOIN procurement.debit_notes d ON d.purchase_return_id = r.id
LEFT JOIN inventory.warehouses w ON w.id = r.warehouse_id
LEFT JOIN inventory.items i ON i.id = rl.item_id
LEFT JOIN inventory.uoms u ON u.id = rl.uom_id;

CREATE VIEW reporting.procurement_vendor_invoices WITH (security_invoker = true) AS
SELECT v.id AS vendor_invoice_id, v.property_id, v.number, v.supplier_invoice_no, v.tax_invoice_no, v.invoice_date, v.due_date, v.status,
       v.match_type, v.matched_at, v.hold_reason, v.override_reason, v.supplier_id, s.code AS supplier_code, s.name AS supplier_name, v.currency,
       v.subtotal, v.tax_amount, v.total, v.withholding_amount, v.debit_note_total, v.paid_amount,
       CASE WHEN v.status IN ('draft', 'cancelled') THEN 0 ELSE greatest(v.total - v.withholding_amount - v.debit_note_total - v.paid_amount, 0) END
         AS outstanding,
       coalesce((v.match_summary->>'mismatches')::int, 0) AS mismatches, coalesce((v.match_summary->>'variance')::numeric, 0) AS variance,
       v.approved_at, v.created_at
FROM procurement.vendor_invoices v
JOIN procurement.suppliers s ON s.id = v.supplier_id;

CREATE VIEW reporting.procurement_invoice_lines WITH (security_invoker = true) AS
SELECT l.id AS line_id, v.property_id, v.id AS vendor_invoice_id, v.number, v.supplier_invoice_no, v.invoice_date, v.status, v.supplier_id,
       s.code AS supplier_code, s.name AS supplier_name, v.currency, l.line_no, i.code AS item_code, coalesce(i.name, l.description) AS item_name,
       o.number AS po_number, l.quantity, l.unit_price, l.line_subtotal, l.match_status, l.expected_quantity, l.expected_unit_price, l.match_note,
       round(pl.unit_price * (1 - pl.discount_percent / 100), 6) AS ordered_unit_price
FROM procurement.vendor_invoice_lines l
JOIN procurement.vendor_invoices v ON v.id = l.vendor_invoice_id
JOIN procurement.suppliers s ON s.id = v.supplier_id
LEFT JOIN procurement.purchase_order_lines pl ON pl.id = l.purchase_order_line_id
LEFT JOIN procurement.purchase_orders o ON o.id = pl.purchase_order_id
LEFT JOIN inventory.items i ON i.id = l.item_id;

SELECT platform.grant_app('reporting');

-- +goose Down
DROP VIEW reporting.procurement_invoice_lines, reporting.procurement_vendor_invoices, reporting.procurement_return_lines,
  reporting.procurement_receipt_lines, reporting.procurement_order_lines, reporting.procurement_orders, reporting.procurement_requisitions;
