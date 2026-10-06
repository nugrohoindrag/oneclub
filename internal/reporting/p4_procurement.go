package reporting

// Reports and KPI dashboard of Procurement (PRD P4 EP-10–15, EP-28
// FR-RPT-P4-02/04/05) on the procurement_* reporting views: Purchase
// Requisition, Purchase Order, Outstanding PO, Goods Receipt, Purchase
// Return, Vendor Invoice Matching (3-way match exceptions), Vendor
// Performance, Purchase by Supplier, Purchase by Item and Procurement
// Performance Reports, filtered by date, supplier, warehouse and status,
// and the Procurement Performance dashboard (PR, PO, Outstanding PO,
// Purchase Value, Vendor Performance, PR → PO lead time).

import "slices"

// procOrdered are the orders counted as purchases (approved and later).
const procOrdered = `status IN ('approved', 'sent', 'partially_received', 'received', 'closed')`

var supplierParam = Param{Key: "supplier", Label: "Supplier Code", Type: "string"}
var warehouseParam = Param{Key: "warehouse", Label: "Warehouse Code", Type: "string"}

var procurementReports = []*Report{
	sqlReport("procurement.purchase_requisition", "Purchase Requisition Report", "procurement",
		"Purchase requisitions of the period by source (manual, reorder, banquet, store requisition) with status, estimated value and days to the first PO.",
		cols("createdAt|Created|datetime", "number|Requisition", "source|Source", "status|Status", "title|Title", "warehouse|Warehouse",
			"costCenter|Cost Center", "neededBy|Needed By|datetime", "lines|Lines|number", "estimatedTotal|Estimated Value|number",
			"firstOrderedAt|First PO|datetime", "leadDays|PR → PO (days)|number"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "submitted", "approved", "rejected", "partially_ordered", "ordered", "cancelled"}},
			{Key: "source", Label: "Source", Type: "enum", Enum: []string{"manual", "reorder", "banquet", "store_requisition"}}, warehouseParam}, 30,
		`SELECT created_at AS "createdAt", number, source, status, title, warehouse_name AS "warehouse", cost_center AS "costCenter", needed_by AS "neededBy",
		lines, trim_scale(estimated_total)::text AS "estimatedTotal", first_ordered_at AS "firstOrderedAt",
		round(extract(epoch FROM first_ordered_at - created_at) / 86400, 1) AS "leadDays"
		FROM reporting.procurement_requisitions WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		AND ($4 = '' OR status = $4) AND ($5 = '' OR source = $5) AND ($6 = '' OR upper(warehouse_code) = upper($6)) ORDER BY created_at, number`),
	sqlReport("procurement.purchase_order", "Purchase Order Report", "procurement",
		"Purchase orders of the period with supplier, status, value (excl. and incl. PPN) and the value still outstanding.",
		cols("orderDate|Order Date|datetime", "number|Purchase Order", "version|Version|number", "type|Type", "supplier|Supplier", "warehouse|Warehouse",
			"status|Status", "expectedDate|Expected|datetime", "subtotal|Subtotal|number", "tax|PPN|number", "total|Total|number",
			"outstanding|Outstanding|number"),
		[]Param{supplierParam, warehouseParam, {Key: "status", Label: "Status", Type: "enum", Enum: []string{"draft", "pending_approval", "approved", "sent",
			"partially_received", "received", "closed", "cancelled"}}}, 30,
		`SELECT order_date AS "orderDate", number, version, order_type AS "type", supplier_name AS "supplier", warehouse_name AS "warehouse", status,
		expected_date AS "expectedDate", trim_scale(subtotal)::text AS "subtotal", trim_scale(tax_total)::text AS "tax", trim_scale(total)::text AS "total",
		trim_scale(round(outstanding_value, 2))::text AS "outstanding"
		FROM reporting.procurement_orders WHERE order_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		AND ($4 = '' OR upper(supplier_code) = upper($4)) AND ($5 = '' OR upper(warehouse_code) = upper($5)) AND ($6 = '' OR status = $6)
		ORDER BY order_date, number`),
	sqlReport("procurement.outstanding_po", "Outstanding PO Report", "procurement",
		"Open purchase order lines (approved, sent, partially received) ordered up to the To date with the quantity and value not yet received, per supplier.",
		cols("supplier|Supplier", "number|Purchase Order", "orderDate|Order Date|datetime", "expectedDate|Expected|datetime", "overdue|Overdue|boolean",
			"warehouse|Warehouse", "itemCode|Item Code", "item|Item", "ordered|Ordered|number", "received|Received|number", "outstanding|Outstanding|number",
			"uom|UOM", "unitPrice|Net Price|number", "outstandingValue|Outstanding Value|number"),
		[]Param{supplierParam, warehouseParam}, 0,
		`SELECT supplier_name AS "supplier", number, order_date AS "orderDate", expected_date AS "expectedDate",
		coalesce(expected_date < (now() AT TIME ZONE $3)::date, false) AS "overdue", warehouse_name AS "warehouse", item_code AS "itemCode",
		item_name AS "item", trim_scale(quantity - cancelled_quantity)::text AS "ordered", trim_scale(received_quantity - returned_quantity)::text AS "received",
		trim_scale(outstanding_quantity)::text AS "outstanding", uom, trim_scale(net_unit_price)::text AS "unitPrice",
		trim_scale(round(outstanding_quantity * net_unit_price, 2))::text AS "outstandingValue"
		FROM reporting.procurement_order_lines WHERE status IN ('approved', 'sent', 'partially_received') AND outstanding_quantity > 0
		AND order_date <= $2::date AND $1::date IS NOT NULL AND ($4 = '' OR upper(supplier_code) = upper($4))
		AND ($5 = '' OR upper(warehouse_code) = upper($5)) ORDER BY supplier_name, expected_date NULLS LAST, number, line_no`),
	sqlReport("procurement.goods_receipt", "Goods Receipt Report", "procurement",
		"Goods receipt lines of the period: delivered, accepted and rejected quantity with reason, cost, batch / expiry and on-time delivery.",
		cols("receivedDate|Received|datetime", "number|Goods Receipt", "status|Status", "poNumber|Purchase Order", "supplier|Supplier",
			"warehouse|Warehouse", "deliveryNote|Delivery Note", "itemCode|Item Code", "item|Item", "delivered|Delivered|number", "accepted|Accepted|number",
			"rejected|Rejected|number", "rejectionReason|Rejection Reason", "uom|UOM", "unitCost|Unit Cost|number", "totalCost|Total Cost|number",
			"batch|Batch", "expiryDate|Expiry|datetime", "onTime|On Time|boolean"),
		[]Param{supplierParam, warehouseParam}, 30,
		`SELECT received_date AS "receivedDate", number, status, po_number AS "poNumber", supplier_name AS "supplier", warehouse_name AS "warehouse",
		delivery_note_no AS "deliveryNote", item_code AS "itemCode", item_name AS "item", trim_scale(delivered_quantity)::text AS "delivered",
		trim_scale(accepted_quantity)::text AS "accepted", trim_scale(rejected_quantity)::text AS "rejected", rejection_reason AS "rejectionReason", uom,
		trim_scale(unit_cost)::text AS "unitCost", trim_scale(total_cost)::text AS "totalCost", batch_no AS "batch", expiry_date AS "expiryDate",
		on_time AS "onTime"
		FROM reporting.procurement_receipt_lines WHERE received_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		AND ($4 = '' OR upper(supplier_code) = upper($4)) AND ($5 = '' OR upper(warehouse_code) = upper($5)) ORDER BY received_date, number`),
	sqlReport("procurement.purchase_return", "Purchase Return Report", "procurement",
		"Goods returned to suppliers in the period with reason, cost, PPN and the debit note issued.",
		cols("returnDate|Return Date|datetime", "number|Purchase Return", "grNumber|Goods Receipt", "supplier|Supplier", "warehouse|Warehouse",
			"itemCode|Item Code", "item|Item", "quantity|Quantity|number", "uom|UOM", "unitCost|Unit Cost|number", "totalCost|Total Cost|number",
			"tax|PPN|number", "reason|Reason", "debitNote|Debit Note"),
		[]Param{supplierParam, warehouseParam}, 30,
		`SELECT return_date AS "returnDate", number, gr_number AS "grNumber", supplier_name AS "supplier", warehouse_name AS "warehouse",
		item_code AS "itemCode", item_name AS "item", trim_scale(quantity)::text AS "quantity", uom, trim_scale(unit_cost)::text AS "unitCost",
		trim_scale(total_cost)::text AS "totalCost", trim_scale(tax_amount)::text AS "tax", coalesce(line_reason, reason) AS "reason",
		debit_note_number AS "debitNote"
		FROM reporting.procurement_return_lines WHERE return_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		AND ($4 = '' OR upper(supplier_code) = upper($4)) AND ($5 = '' OR upper(warehouse_code) = upper($5)) ORDER BY return_date, number`),
	sqlReport("procurement.vendor_invoice_matching", "Vendor Invoice Matching Report", "procurement",
		"Vendor invoice lines of the period with the 3-way matching result (PO + GR + invoice) against the ordered price and received quantity; exceptions only on request.",
		cols("invoiceDate|Invoice Date|datetime", "number|Vendor Invoice", "supplierInvoiceNo|Supplier Invoice No.", "supplier|Supplier",
			"status|Invoice Status", "poNumber|Purchase Order", "itemCode|Item Code", "item|Item", "quantity|Invoiced Qty|number",
			"expectedQuantity|Received, not Invoiced|number", "unitPrice|Invoiced Price|number", "orderedPrice|Ordered Price|number",
			"lineSubtotal|Line Subtotal|number", "matchStatus|Match Status", "note|Note"),
		[]Param{supplierParam, {Key: "exceptions", Label: "Exceptions only (yes / no)", Type: "enum", Enum: []string{"yes", "no"}}}, 30,
		`SELECT invoice_date AS "invoiceDate", number, supplier_invoice_no AS "supplierInvoiceNo", supplier_name AS "supplier", status, po_number AS "poNumber",
		item_code AS "itemCode", item_name AS "item", trim_scale(quantity)::text AS "quantity", trim_scale(expected_quantity)::text AS "expectedQuantity",
		trim_scale(unit_price)::text AS "unitPrice", trim_scale(ordered_unit_price)::text AS "orderedPrice", trim_scale(line_subtotal)::text AS "lineSubtotal",
		match_status AS "matchStatus", match_note AS "note"
		FROM reporting.procurement_invoice_lines WHERE invoice_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND status <> 'cancelled'
		AND ($4 = '' OR upper(supplier_code) = upper($4)) AND ($5 <> 'yes' OR match_status NOT IN ('matched', 'pending'))
		ORDER BY invoice_date, number, line_no`),
	sqlReport("procurement.vendor_performance", "Vendor Performance Report", "procurement",
		"Per supplier for the period: orders and purchase value, receipts, on-time delivery, fill rate, quality (accepted ÷ delivered), return rate and invoice price variance.",
		cols("supplierCode|Supplier Code", "supplier|Supplier", "orders|Orders|number", "purchaseValue|Purchase Value|number", "receipts|Receipts|number",
			"onTimeRate|On Time|number", "fillRate|Fill Rate|number", "qualityRate|Quality|number", "returnRate|Return Rate|number",
			"priceVariancePercent|Price Variance %|number"),
		[]Param{supplierParam}, 90,
		`WITH o AS (SELECT supplier_id, count(*) AS orders, sum(subtotal) AS value FROM reporting.procurement_orders WHERE `+procOrdered+`
		  AND order_date BETWEEN $1::date AND $2::date GROUP BY 1),
		f AS (SELECT supplier_id, sum(received_quantity) AS received, sum(quantity - cancelled_quantity) AS ordered FROM reporting.procurement_order_lines
		  WHERE order_type = 'goods' AND `+procOrdered+` AND order_date BETWEEN $1::date AND $2::date GROUP BY 1),
		g AS (SELECT supplier_id, count(DISTINCT goods_receipt_id) AS receipts, count(DISTINCT goods_receipt_id) FILTER (WHERE on_time) AS on_time,
		  sum(delivered_quantity) AS delivered, sum(accepted_quantity) AS accepted, sum(returned_quantity) AS returned
		  FROM reporting.procurement_receipt_lines WHERE status = 'posted' AND received_date BETWEEN $1::date AND $2::date GROUP BY 1),
		p AS (SELECT supplier_id, avg((unit_price - ordered_unit_price) / nullif(ordered_unit_price, 0) * 100) AS pv FROM reporting.procurement_invoice_lines
		  WHERE status NOT IN ('draft', 'cancelled') AND ordered_unit_price IS NOT NULL AND invoice_date BETWEEN $1::date AND $2::date GROUP BY 1),
		s AS (SELECT supplier_id AS id, supplier_code AS code, supplier_name AS name FROM reporting.procurement_orders
		  UNION SELECT supplier_id, supplier_code, supplier_name FROM reporting.procurement_receipt_lines
		  UNION SELECT supplier_id, supplier_code, supplier_name FROM reporting.procurement_invoice_lines),
		k AS (SELECT supplier_id FROM o UNION SELECT supplier_id FROM g UNION SELECT supplier_id FROM p)
		SELECT s.code AS "supplierCode", s.name AS "supplier", coalesce(o.orders, 0) AS "orders", trim_scale(round(coalesce(o.value, 0), 2))::text AS "purchaseValue",
		coalesce(g.receipts, 0) AS "receipts", trim_scale(round(g.on_time::numeric / nullif(g.receipts, 0), 4))::text AS "onTimeRate",
		trim_scale(round(least(f.received / nullif(f.ordered, 0), 1), 4))::text AS "fillRate",
		trim_scale(round(g.accepted / nullif(g.delivered, 0), 4))::text AS "qualityRate", trim_scale(round(g.returned / nullif(g.accepted, 0), 4))::text AS "returnRate",
		trim_scale(round(p.pv, 2))::text AS "priceVariancePercent"
		FROM k JOIN s ON s.id = k.supplier_id LEFT JOIN o ON o.supplier_id = k.supplier_id LEFT JOIN f ON f.supplier_id = k.supplier_id
		LEFT JOIN g ON g.supplier_id = k.supplier_id LEFT JOIN p ON p.supplier_id = k.supplier_id
		WHERE $3::text <> '' AND ($4 = '' OR upper(s.code) = upper($4)) ORDER BY s.name`),
	sqlReport("procurement.purchase_by_supplier", "Purchase by Supplier Report", "procurement",
		"Ordered, received and invoiced value per supplier for the period (orders approved and later).",
		cols("supplierCode|Supplier Code", "supplier|Supplier", "orders|Orders|number", "ordered|Ordered Value|number", "received|Received Value|number",
			"outstanding|Outstanding|number", "invoiced|Invoiced Value|number"),
		[]Param{supplierParam}, 30,
		`WITH l AS (SELECT supplier_id, supplier_code, supplier_name, count(DISTINCT purchase_order_id) AS orders,
		  sum(round((quantity - cancelled_quantity) * net_unit_price, 2)) AS ordered, sum(round((received_quantity - returned_quantity) * net_unit_price, 2)) AS received,
		  sum(round(outstanding_quantity * net_unit_price, 2)) AS outstanding
		  FROM reporting.procurement_order_lines WHERE `+procOrdered+` AND order_date BETWEEN $1::date AND $2::date GROUP BY 1, 2, 3),
		i AS (SELECT supplier_id, sum(subtotal) AS invoiced FROM reporting.procurement_vendor_invoices WHERE status IN ('approved', 'partially_paid', 'paid')
		  AND invoice_date BETWEEN $1::date AND $2::date GROUP BY 1)
		SELECT l.supplier_code AS "supplierCode", l.supplier_name AS "supplier", l.orders, trim_scale(l.ordered)::text AS "ordered",
		trim_scale(l.received)::text AS "received", trim_scale(l.outstanding)::text AS "outstanding", trim_scale(coalesce(i.invoiced, 0))::text AS "invoiced"
		FROM l LEFT JOIN i ON i.supplier_id = l.supplier_id WHERE $3::text <> '' AND ($4 = '' OR upper(l.supplier_code) = upper($4))
		ORDER BY l.ordered DESC, l.supplier_name`),
	sqlReport("procurement.purchase_by_item", "Purchase by Item Report", "procurement",
		"Ordered quantity and value per item for the period with average, minimum and maximum net price and the suppliers it was bought from.",
		cols("itemCode|Item Code", "item|Item", "category|Category", "uom|UOM", "quantity|Ordered Qty|number", "value|Ordered Value|number",
			"avgPrice|Average Price|number", "minPrice|Min Price|number", "maxPrice|Max Price|number", "suppliers|Suppliers"),
		[]Param{supplierParam, warehouseParam}, 30,
		`SELECT item_code AS "itemCode", item_name AS "item", category, uom, trim_scale(sum(quantity - cancelled_quantity))::text AS "quantity",
		trim_scale(sum(round((quantity - cancelled_quantity) * net_unit_price, 2)))::text AS "value",
		trim_scale(round(sum((quantity - cancelled_quantity) * net_unit_price) / nullif(sum(quantity - cancelled_quantity), 0), 2))::text AS "avgPrice",
		trim_scale(min(net_unit_price))::text AS "minPrice", trim_scale(max(net_unit_price))::text AS "maxPrice",
		string_agg(DISTINCT supplier_name, ', ') AS "suppliers"
		FROM reporting.procurement_order_lines WHERE `+procOrdered+` AND order_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		AND ($4 = '' OR upper(supplier_code) = upper($4)) AND ($5 = '' OR upper(warehouse_code) = upper($5))
		GROUP BY item_code, item_name, category, uom ORDER BY sum(round((quantity - cancelled_quantity) * net_unit_price, 2)) DESC, item_name`),
	sqlReport("procurement.performance", "Procurement Performance Report", "procurement",
		"Per month of the period: requisitions, approved, purchase orders, purchase value, average PR → PO lead time, goods receipts on time and invoices on hold.",
		cols("month|Month", "requisitions|Requisitions|number", "approved|Approved|number", "orders|Purchase Orders|number", "purchaseValue|Purchase Value|number",
			"leadDays|PR → PO (days)|number", "receipts|Goods Receipts|number", "onTimeRate|On Time|number", "invoicesOnHold|Invoices on Hold|number"), nil, 180,
		`WITH m AS (SELECT to_char(d, 'YYYY-MM') AS month FROM generate_series(date_trunc('month', $1::date), $2::date, interval '1 month') d),
		r AS (SELECT to_char((created_at AT TIME ZONE $3)::date, 'YYYY-MM') AS month, count(*) AS n, count(*) FILTER (WHERE approved_at IS NOT NULL) AS approved,
		  avg(extract(epoch FROM first_ordered_at - created_at) / 86400) AS lead FROM reporting.procurement_requisitions
		  WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1),
		o AS (SELECT to_char(order_date, 'YYYY-MM') AS month, count(*) AS n, sum(subtotal) AS value FROM reporting.procurement_orders
		  WHERE `+procOrdered+` AND order_date BETWEEN $1::date AND $2::date GROUP BY 1),
		g AS (SELECT to_char(received_date, 'YYYY-MM') AS month, count(DISTINCT goods_receipt_id) AS n, count(DISTINCT goods_receipt_id) FILTER (WHERE on_time) AS ok
		  FROM reporting.procurement_receipt_lines WHERE status = 'posted' AND received_date BETWEEN $1::date AND $2::date GROUP BY 1),
		v AS (SELECT to_char(invoice_date, 'YYYY-MM') AS month, count(*) FILTER (WHERE status = 'on_hold') AS held FROM reporting.procurement_vendor_invoices
		  WHERE invoice_date BETWEEN $1::date AND $2::date GROUP BY 1)
		SELECT m.month, coalesce(r.n, 0) AS "requisitions", coalesce(r.approved, 0) AS "approved", coalesce(o.n, 0) AS "orders",
		trim_scale(round(coalesce(o.value, 0), 2))::text AS "purchaseValue", round(r.lead, 1) AS "leadDays", coalesce(g.n, 0) AS "receipts",
		trim_scale(round(g.ok::numeric / nullif(g.n, 0), 4))::text AS "onTimeRate", coalesce(v.held, 0) AS "invoicesOnHold"
		FROM m LEFT JOIN r ON r.month = m.month LEFT JOIN o ON o.month = m.month LEFT JOIN g ON g.month = m.month LEFT JOIN v ON v.month = m.month
		ORDER BY m.month`),
}

// procurementKPIs is the Procurement Performance dashboard (FR-RPT-P4-02).
var procurementKPIs = []kpiDef{
	{key: "purchase_requisitions", label: "Purchase Requisitions", unit: "count", def: "Purchase requisitions created in the period (breakdown by status)",
		sql: `SELECT count(*)::text FROM reporting.procurement_requisitions WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT status AS label, count(*)::text AS value FROM reporting.procurement_requisitions
		WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "purchase_orders", label: "Purchase Orders", unit: "count", def: "Purchase orders approved (and later) with an order date in the period",
		sql: `SELECT count(*)::text FROM reporting.procurement_orders WHERE ` + procOrdered + ` AND order_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		breakdown: `SELECT status AS label, count(*)::text AS value FROM reporting.procurement_orders WHERE ` + procOrdered + `
		AND order_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "outstanding_po", label: "Outstanding PO", unit: "idr", def: "Value not yet received of the open purchase orders (approved, sent, partially received)",
		sql: `SELECT trim_scale(round(coalesce(sum(outstanding_value), 0), 2))::text FROM reporting.procurement_orders
		WHERE status IN ('approved', 'sent', 'partially_received') AND order_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT supplier_name AS label, trim_scale(round(sum(outstanding_value), 0))::text AS value FROM reporting.procurement_orders
		WHERE status IN ('approved', 'sent', 'partially_received') AND order_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> ''
		GROUP BY 1 HAVING sum(outstanding_value) > 0 ORDER BY 1`},
	{key: "purchase_value", label: "Purchase Value", unit: "idr", def: "Value (excl. PPN) of the purchase orders of the period",
		sql: `SELECT trim_scale(round(coalesce(sum(subtotal), 0), 2))::text FROM reporting.procurement_orders WHERE ` + procOrdered + `
		AND order_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		breakdown: `SELECT supplier_name AS label, trim_scale(round(sum(subtotal), 0))::text AS value FROM reporting.procurement_orders WHERE ` + procOrdered + `
		AND order_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "vendor_on_time", label: "Vendor Performance (On Time)", unit: "ratio",
		def: "Goods receipts on or before the delivery date ÷ goods receipts of the period (breakdown per supplier; full scorecard in the Vendor Performance Report)",
		sql: `SELECT trim_scale(coalesce(round(count(DISTINCT goods_receipt_id) FILTER (WHERE on_time)::numeric / nullif(count(DISTINCT goods_receipt_id), 0), 4), 0))::text
		FROM reporting.procurement_receipt_lines WHERE status = 'posted' AND purchase_order_id IS NOT NULL AND received_date BETWEEN $1::date AND $2::date
		AND $3::text <> ''`,
		breakdown: `SELECT supplier_name AS label, trim_scale(round(count(DISTINCT goods_receipt_id) FILTER (WHERE on_time)::numeric
		/ nullif(count(DISTINCT goods_receipt_id), 0), 4))::text AS value FROM reporting.procurement_receipt_lines WHERE status = 'posted'
		AND purchase_order_id IS NOT NULL AND received_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "vendor_quality", label: "Vendor Quality", unit: "ratio", def: "Accepted ÷ delivered quantity at goods receipt in the period",
		sql: `SELECT trim_scale(coalesce(round(sum(accepted_quantity) / nullif(sum(delivered_quantity), 0), 4), 0))::text FROM reporting.procurement_receipt_lines
		WHERE status = 'posted' AND received_date BETWEEN $1::date AND $2::date AND $3::text <> ''`},
	{key: "pr_to_po_days", label: "PR → PO Lead Time", unit: "days", def: "Average days from the requisition to its first purchase order (requisitions of the period)",
		sql: `SELECT trim_scale(coalesce(round(avg(extract(epoch FROM first_ordered_at - created_at) / 86400)::numeric, 1), 0))::text
		FROM reporting.procurement_requisitions WHERE first_ordered_at IS NOT NULL AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	{key: "invoices_on_hold", label: "Vendor Invoices on Hold", unit: "count", def: "Vendor invoices currently On Hold (3-way matching exceptions) dated up to the To date",
		sql: `SELECT count(*)::text FROM reporting.procurement_vendor_invoices WHERE status = 'on_hold' AND invoice_date <= $2::date AND $1::date IS NOT NULL
		AND $3::text <> ''`},
}

const procurementPerformance = "procurement-performance"

func init() {
	dashboards[procurementPerformance] = dashDef{name: "Procurement Performance", kpis: procurementKPIs}
	if !slices.Contains(DashboardCodes, procurementPerformance) {
		DashboardCodes = append(DashboardCodes, procurementPerformance)
	}
}
