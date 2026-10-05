package reporting

// Reports and KPI dashboard of Inventory (PRD P4 EP-01–09, EP-28
// FR-RPT-P4-01/04) on the inventory_* reporting views: Stock Balance, Stock
// Movement, Stock Valuation, Stock Opname Variance, Waste, Expiry,
// Theoretical vs Actual Consumption, Food Cost (actual), Low Stock, Slow
// Moving, Asset Register, Maintenance Schedule and Asset Depreciation
// Reports, and the Inventory Performance dashboard. Inventory value is the
// sum of the valued movement lines up to the date (consignment stock has
// no value), the same basis as the inventory journal (EP-28 AC).

import "slices"

// valueAsOf is the stock value of the movement lines up to $2.
const invValueAsOf = `SELECT trim_scale(round(coalesce(sum(total_cost), 0), 2))::text FROM reporting.inventory_movement_lines
	WHERE business_date <= $2::date AND NOT consignment AND $1::date IS NOT NULL AND $3::text <> ''`

// invActual is the actual usage per outlet warehouse × item in the period:
// opening + received − transferred out − closing, valued at movement cost.
const invActual = `a AS (SELECT m.warehouse_id, m.outlet_id, m.warehouse_code, m.item_id, m.item_code, m.item_name, m.uom,
	  coalesce(sum(m.quantity) FILTER (WHERE m.business_date < $1::date), 0) AS opening,
	  coalesce(sum(m.quantity) FILTER (WHERE m.business_date BETWEEN $1::date AND $2::date AND m.movement_type IN ('receipt', 'transfer_in', 'production_in')), 0) AS received,
	  coalesce(-sum(m.quantity) FILTER (WHERE m.business_date BETWEEN $1::date AND $2::date AND m.movement_type IN ('transfer_out', 'return_out')), 0) AS transferred_out,
	  coalesce(sum(m.quantity), 0) AS closing,
	  coalesce(-sum(m.total_cost) FILTER (WHERE m.business_date BETWEEN $1::date AND $2::date
	    AND m.movement_type NOT IN ('receipt', 'transfer_in', 'production_in', 'transfer_out', 'return_out')), 0) AS actual_cost
	  FROM reporting.inventory_movement_lines m WHERE m.outlet_id IS NOT NULL AND m.business_date <= $2::date AND NOT m.consignment
	  GROUP BY 1, 2, 3, 4, 5, 6, 7)`

var inventoryReports = []*Report{
	sqlReport("inventory.stock_balance", "Stock Balance Report", "inventory",
		"Stock on hand per warehouse, item and batch with value, average cost and the reorder point / par level.",
		cols("warehouse|Warehouse", "itemCode|Item Code", "item|Item", "category|Category", "batch|Batch", "expiryDate|Expiry|datetime",
			"quantity|Quantity|number", "uom|UOM", "unitCost|Unit Cost|number", "value|Value|number", "reorderPoint|Reorder Point|number",
			"parLevel|Par Level|number", "belowReorder|Below Reorder|boolean"),
		[]Param{{Key: "warehouse", Label: "Warehouse Code", Type: "string"}, {Key: "category", Label: "Category", Type: "string"}}, 0,
		`SELECT warehouse_name AS "warehouse", item_code AS "itemCode", item_name AS "item", category, batch_no AS "batch", expiry_date AS "expiryDate",
		trim_scale(quantity)::text AS "quantity", uom, trim_scale(CASE WHEN quantity > 0 THEN round(value / quantity, 4) ELSE 0 END)::text AS "unitCost",
		trim_scale(round(value, 2))::text AS "value", trim_scale(reorder_point)::text AS "reorderPoint", trim_scale(par_level)::text AS "parLevel",
		coalesce(quantity < coalesce(reorder_point, nullif(min_stock, 0)), false) AS "belowReorder"
		FROM reporting.inventory_stock_balances WHERE quantity <> 0 AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		AND ($4 = '' OR upper(warehouse_code) = upper($4)) AND ($5 = '' OR category ILIKE $5) ORDER BY warehouse_code, item_code, expiry_date NULLS LAST`),
	sqlReport("inventory.stock_movement", "Stock Movement Report", "inventory",
		"Movement lines of the period: type, source, quantity (signed, stock UOM), unit and total cost and the balance after the line.",
		cols("businessDate|Date|datetime", "number|Movement", "type|Type", "source|Source", "warehouse|Warehouse", "itemCode|Item Code", "item|Item",
			"batch|Batch", "quantity|Quantity|number", "uom|UOM", "unitCost|Unit Cost|number", "totalCost|Total Cost|number", "balanceAfter|Balance After|number",
			"reason|Reason", "flagged|Flagged|boolean"),
		[]Param{{Key: "warehouse", Label: "Warehouse Code", Type: "string"}, {Key: "movementType", Label: "Movement Type", Type: "enum",
			Enum: []string{"receipt", "issue", "transfer_out", "transfer_in", "adjustment", "opname", "waste", "production_in", "production_out", "consumption", "return_out"}}}, 7,
		`SELECT business_date AS "businessDate", number, movement_type AS "type", source_type AS "source", warehouse_name AS "warehouse", item_code AS "itemCode",
		item_name AS "item", batch_no AS "batch", trim_scale(quantity)::text AS "quantity", uom, trim_scale(unit_cost)::text AS "unitCost",
		trim_scale(total_cost)::text AS "totalCost", trim_scale(balance_after)::text AS "balanceAfter", reason, flagged
		FROM reporting.inventory_movement_lines WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		AND ($4 = '' OR upper(warehouse_code) = upper($4)) AND ($5 = '' OR movement_type = $5) ORDER BY business_date, seq`),
	sqlReport("inventory.stock_valuation", "Stock Valuation Report", "inventory",
		"Stock value per warehouse and item as of the To date (moving average / FIFO), equal to the inventory account of the general ledger.",
		cols("warehouse|Warehouse", "category|Category", "itemCode|Item Code", "item|Item", "quantity|Quantity|number", "uom|UOM",
			"unitCost|Unit Cost|number", "value|Value|number"),
		[]Param{{Key: "warehouse", Label: "Warehouse Code", Type: "string"}}, 0,
		`SELECT warehouse_name AS "warehouse", category, item_code AS "itemCode", item_name AS "item", trim_scale(sum(quantity))::text AS "quantity", uom,
		trim_scale(CASE WHEN sum(quantity) > 0 THEN round(sum(total_cost) / sum(quantity), 4) END)::text AS "unitCost",
		trim_scale(round(sum(total_cost), 2))::text AS "value"
		FROM reporting.inventory_movement_lines WHERE business_date <= $2::date AND NOT consignment AND $1::date IS NOT NULL AND $3::text <> ''
		AND ($4 = '' OR upper(warehouse_code) = upper($4))
		GROUP BY warehouse_code, warehouse_name, category, item_code, item_name, uom HAVING sum(quantity) <> 0 OR sum(total_cost) <> 0
		ORDER BY warehouse_code, item_code`),
	sqlReport("inventory.opname_variance", "Stock Opname Variance Report", "inventory",
		"Counted stock opname lines of the period: system, expected (cut-off), counted, variance quantity and value, tolerance and recounts.",
		cols("number|Stock Opname", "warehouse|Warehouse", "status|Status", "countedAt|Counted|datetime", "itemCode|Item Code", "item|Item", "batch|Batch",
			"system|System|number", "expected|Expected|number", "counted|Counted|number", "variance|Variance|number", "uom|UOM", "unitCost|Unit Cost|number",
			"varianceValue|Variance Value|number", "overTolerance|Above Tolerance|boolean", "recounts|Recounts|number"), nil, 31,
		`SELECT number, warehouse_name AS "warehouse", status, counted_at AS "countedAt", item_code AS "itemCode", item_name AS "item", batch_no AS "batch",
		trim_scale(system_quantity)::text AS "system", trim_scale(expected_quantity)::text AS "expected", trim_scale(counted_quantity)::text AS "counted",
		trim_scale(variance_quantity)::text AS "variance", uom, trim_scale(unit_cost)::text AS "unitCost", trim_scale(variance_value)::text AS "varianceValue",
		over_tolerance AS "overTolerance", recounts
		FROM reporting.inventory_opname_lines WHERE counted_at IS NOT NULL AND (counted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		AND status <> 'cancelled' ORDER BY counted_at, number, item_code`),
	sqlReport("inventory.waste", "Waste Report", "inventory", "Waste and spoilage of the period per reason, warehouse and item at valuation cost.",
		cols("businessDate|Date|datetime", "number|Waste", "reason|Reason", "warehouse|Warehouse", "category|Category", "itemCode|Item Code", "item|Item",
			"batch|Batch", "quantity|Quantity|number", "uom|UOM", "cost|Cost|number"),
		[]Param{{Key: "reason", Label: "Reason", Type: "enum", Enum: []string{"expired", "damaged", "spoiled", "wrong_preparation", "overproduction", "breakage", "other"}}}, 30,
		`SELECT business_date AS "businessDate", number, reason, warehouse_name AS "warehouse", category, item_code AS "itemCode", item_name AS "item",
		batch_no AS "batch", trim_scale(quantity)::text AS "quantity", uom, trim_scale(round(cost, 2))::text AS "cost"
		FROM reporting.inventory_waste WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR reason = $4)
		ORDER BY business_date, number, item_code`),
	sqlReport("inventory.expiry", "Expiry Report", "inventory",
		"Batches in stock that are expired or expire within the given days (default 30), first expiring first.",
		cols("expiryDate|Expiry|datetime", "daysLeft|Days Left|number", "warehouse|Warehouse", "itemCode|Item Code", "item|Item", "batch|Batch",
			"quantity|Quantity|number", "uom|UOM", "value|Value|number"),
		[]Param{{Key: "days", Label: "Within Days", Type: "string"}}, 0,
		`SELECT expiry_date AS "expiryDate", (expiry_date - (now() AT TIME ZONE $3)::date)::int AS "daysLeft", warehouse_name AS "warehouse",
		item_code AS "itemCode", item_name AS "item", batch_no AS "batch", trim_scale(quantity)::text AS "quantity", uom, trim_scale(round(value, 2))::text AS "value"
		FROM reporting.inventory_stock_balances WHERE quantity > 0 AND expiry_date IS NOT NULL AND $1::date IS NOT NULL AND $2::date IS NOT NULL
		AND expiry_date <= (now() AT TIME ZONE $3)::date + coalesce(nullif($4, '')::int, 30) ORDER BY expiry_date, item_code`),
	sqlReport("inventory.consumption_variance", "Theoretical vs Actual Consumption Report", "inventory",
		"Per outlet warehouse and item: recipe (theoretical) consumption of the sales against actual usage (opening + received − transferred out − closing).",
		cols("warehouse|Warehouse", "itemCode|Item Code", "item|Item", "uom|UOM", "opening|Opening|number", "received|Received|number",
			"transferredOut|Transferred Out|number", "closing|Closing|number", "actual|Actual|number", "theoretical|Theoretical|number",
			"variance|Variance|number", "actualCost|Actual Cost|number", "varianceValue|Variance Value|number"), nil, 30,
		`WITH `+invActual+`,
		t AS (SELECT w.warehouse_id, c.item_id, sum(c.quantity) AS qty FROM reporting.inventory_theoretical_consumption c
		  JOIN reporting.inventory_outlet_warehouses w ON w.outlet_id = c.outlet_id
		  WHERE (c.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1, 2)
		SELECT a.warehouse_code AS "warehouse", a.item_code AS "itemCode", a.item_name AS "item", a.uom, trim_scale(a.opening)::text AS "opening",
		trim_scale(a.received)::text AS "received", trim_scale(a.transferred_out)::text AS "transferredOut", trim_scale(a.closing)::text AS "closing",
		trim_scale(a.opening + a.received - a.transferred_out - a.closing)::text AS "actual", trim_scale(round(coalesce(t.qty, 0), 6))::text AS "theoretical",
		trim_scale(round(a.opening + a.received - a.transferred_out - a.closing - coalesce(t.qty, 0), 6))::text AS "variance",
		trim_scale(round(a.actual_cost, 2))::text AS "actualCost",
		trim_scale(round((a.opening + a.received - a.transferred_out - a.closing - coalesce(t.qty, 0)) *
		  CASE WHEN a.opening + a.received - a.transferred_out - a.closing <> 0 THEN a.actual_cost / (a.opening + a.received - a.transferred_out - a.closing) ELSE 0 END, 2))::text
		  AS "varianceValue"
		FROM a LEFT JOIN t ON t.warehouse_id = a.warehouse_id AND t.item_id = a.item_id
		WHERE a.opening <> 0 OR a.received <> 0 OR a.closing <> 0 OR t.qty IS NOT NULL ORDER BY a.warehouse_code, a.item_code`),
	sqlReport("inventory.food_cost", "Food Cost Report", "inventory",
		"Actual food cost per outlet: net sales, actual usage at valuation cost, theoretical (recipe) cost and both food cost percentages.",
		cols("outlet|Outlet", "netSales|Net Sales|number", "actualCost|Actual Cost|number", "actualPercent|Actual Food Cost %|number",
			"theoreticalCost|Theoretical Cost|number", "theoreticalPercent|Theoretical Food Cost %|number", "variance|Variance|number"), nil, 30,
		`WITH `+invActual+`,
		ac AS (SELECT outlet_id, sum(actual_cost) AS cost FROM a GROUP BY 1),
		s AS (SELECT outlet_id, sum(net_amount) AS sales, sum(cost_amount) AS cost FROM reporting.inventory_sales
		  WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1),
		k AS (SELECT outlet_id FROM ac UNION SELECT outlet_id FROM s WHERE outlet_id IN (SELECT outlet_id FROM reporting.inventory_outlet_warehouses))
		SELECT (SELECT min(outlet_name) FROM reporting.inventory_outlet_warehouses o WHERE o.outlet_id = k.outlet_id) AS "outlet",
		trim_scale(round(coalesce(s.sales, 0), 2))::text AS "netSales", trim_scale(round(coalesce(ac.cost, 0), 2))::text AS "actualCost",
		trim_scale(round(coalesce(ac.cost, 0) / nullif(s.sales, 0) * 100, 2))::text AS "actualPercent",
		trim_scale(round(coalesce(s.cost, 0), 2))::text AS "theoreticalCost", trim_scale(round(coalesce(s.cost, 0) / nullif(s.sales, 0) * 100, 2))::text AS "theoreticalPercent",
		trim_scale(round(coalesce(ac.cost, 0) - coalesce(s.cost, 0), 2))::text AS "variance"
		FROM k LEFT JOIN ac ON ac.outlet_id = k.outlet_id LEFT JOIN s ON s.outlet_id = k.outlet_id ORDER BY 1`),
	sqlReport("inventory.low_stock", "Low Stock Report", "inventory",
		"Items below their reorder point or minimum stock per warehouse, with the par level to replenish to.",
		cols("warehouse|Warehouse", "itemCode|Item Code", "item|Item", "onHand|On Hand|number", "uom|UOM", "reorderPoint|Reorder Point|number",
			"minStock|Minimum Stock|number", "parLevel|Par Level|number", "shortage|To Par|number"), nil, 0,
		`SELECT max(warehouse_name) AS "warehouse", item_code AS "itemCode", max(item_name) AS "item", trim_scale(sum(quantity))::text AS "onHand", max(uom) AS uom,
		trim_scale(max(reorder_point))::text AS "reorderPoint", trim_scale(max(min_stock))::text AS "minStock", trim_scale(max(par_level))::text AS "parLevel",
		trim_scale(greatest(coalesce(max(par_level), max(reorder_point), 0) - sum(quantity), 0))::text AS "shortage"
		FROM reporting.inventory_stock_balances WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		GROUP BY warehouse_id, warehouse_code, item_id, item_code
		HAVING sum(quantity) < coalesce(max(reorder_point), nullif(max(min_stock), 0)) ORDER BY warehouse_code, item_code`),
	sqlReport("inventory.slow_moving", "Slow Moving Report", "inventory",
		"Items in stock without an outbound movement for the given days (default 90).",
		cols("warehouse|Warehouse", "itemCode|Item Code", "item|Item", "quantity|Quantity|number", "uom|UOM", "value|Value|number",
			"lastOutbound|Last Outbound|datetime", "daysIdle|Days Idle|number"),
		[]Param{{Key: "days", Label: "Idle Days", Type: "string"}}, 0,
		`WITH s AS (SELECT warehouse_id, warehouse_name, warehouse_code, item_id, item_code, item_name, uom, sum(quantity) AS quantity, sum(value) AS value
		  FROM reporting.inventory_stock_balances GROUP BY 1, 2, 3, 4, 5, 6, 7 HAVING sum(quantity) > 0),
		m AS (SELECT warehouse_id, item_id, max(business_date) FILTER (WHERE quantity < 0 AND movement_type <> 'opname') AS last_out, min(business_date) AS first_in
		  FROM reporting.inventory_movement_lines GROUP BY 1, 2)
		SELECT s.warehouse_name AS "warehouse", s.item_code AS "itemCode", s.item_name AS "item", trim_scale(s.quantity)::text AS "quantity", s.uom,
		trim_scale(round(s.value, 2))::text AS "value", m.last_out AS "lastOutbound", ((now() AT TIME ZONE $3)::date - coalesce(m.last_out, m.first_in))::int AS "daysIdle"
		FROM s JOIN m ON m.warehouse_id = s.warehouse_id AND m.item_id = s.item_id WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL
		AND coalesce(m.last_out, m.first_in) <= (now() AT TIME ZONE $3)::date - coalesce(nullif($4, '')::int, 90) ORDER BY "daysIdle" DESC, s.item_code`),
	sqlReport("inventory.asset_register", "Asset Register Report", "inventory",
		"Assets & equipment with location, acquisition, accumulated depreciation, book value, warranty and status.",
		cols("code|Asset Code", "name|Asset", "category|Category", "assetClass|Class", "serialNo|Serial No.", "location|Location",
			"acquisitionDate|Acquired|datetime", "acquisitionCost|Acquisition Cost|number", "accumulated|Accumulated Depreciation|number",
			"bookValue|Book Value|number", "method|Method", "usefulLife|Useful Life (months)|number", "supplier|Supplier", "warrantyUntil|Warranty Until|datetime",
			"usageHours|Usage Hours|number", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"active", "maintenance", "out_of_service", "disposed"}}}, 0,
		`SELECT code, name, category, asset_class AS "assetClass", serial_no AS "serialNo", location, acquisition_date AS "acquisitionDate",
		trim_scale(acquisition_cost)::text AS "acquisitionCost", trim_scale(accumulated_depreciation)::text AS "accumulated",
		trim_scale(book_value)::text AS "bookValue", depreciation_method AS "method", useful_life_months AS "usefulLife", supplier_name AS "supplier",
		warranty_until AS "warrantyUntil", trim_scale(usage_hours)::text AS "usageHours", status
		FROM reporting.inventory_assets WHERE archived_at IS NULL AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''
		AND ($4 = '' OR status = $4) ORDER BY code`),
	sqlReport("inventory.maintenance_schedule", "Maintenance Schedule Report", "inventory",
		"Active maintenance schedules per asset: interval, last service, next due date or usage hours since the last service, and whether it is due.",
		cols("asset|Asset", "assetName|Asset Name", "schedule|Schedule", "basedOn|Based On", "lastDone|Last Done|datetime", "nextDue|Next Due|datetime",
			"hoursSince|Hours Since Service|number", "intervalHours|Interval (hours)|number", "due|Due|boolean", "openWorkOrders|Open Work Orders|number"), nil, 0,
		`SELECT asset_code AS "asset", asset_name AS "assetName", schedule_name AS "schedule", trigger_type AS "basedOn", last_done_on AS "lastDone",
		next_due_on AS "nextDue", trim_scale(hours_since)::text AS "hoursSince", trim_scale(interval_hours)::text AS "intervalHours",
		CASE WHEN trigger_type = 'time' THEN next_due_on IS NOT NULL AND next_due_on <= $2::date + reminder_days_before
		  ELSE interval_hours IS NOT NULL AND hours_since >= interval_hours END AS "due", open_work_orders AS "openWorkOrders"
		FROM reporting.inventory_maintenance_schedules WHERE status = 'active' AND asset_status <> 'disposed' AND $1::date IS NOT NULL AND $3::text <> ''
		ORDER BY next_due_on NULLS LAST, asset_code`),
	sqlReport("inventory.depreciation", "Asset Depreciation Report", "inventory",
		"Monthly depreciation per asset in the period with accumulated depreciation and book value after the run.",
		cols("period|Period", "number|Run", "asset|Asset Code", "assetName|Asset", "category|Category", "amount|Depreciation|number",
			"accumulated|Accumulated|number", "bookValue|Book Value|number"), nil, 365,
		`SELECT period, number, asset_code AS "asset", asset_name AS "assetName", category, trim_scale(amount)::text AS "amount",
		trim_scale(accumulated_after)::text AS "accumulated", trim_scale(book_value_after)::text AS "bookValue"
		FROM reporting.inventory_depreciation WHERE period_end BETWEEN $1::date AND ($2::date + 31) AND $3::text <> '' ORDER BY period, asset_code`),
}

// inventoryKPIs is the Inventory Performance dashboard (FR-RPT-P4-01).
var inventoryKPIs = []kpiDef{
	{key: "inventory_value", label: "Inventory Value", unit: "idr", def: "Stock value as of the To date (= Stock Valuation Report = GL inventory account)",
		sql: invValueAsOf,
		breakdown: `SELECT warehouse_code AS label, trim_scale(round(sum(total_cost), 0))::text AS value FROM reporting.inventory_movement_lines
		WHERE business_date <= $2::date AND NOT consignment AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 HAVING sum(total_cost) <> 0 ORDER BY 1`},
	{key: "stock_balance", label: "Stock Balance", unit: "count", def: "Items with stock on hand (warehouse × item)",
		sql: `SELECT count(*)::text FROM (SELECT warehouse_id, item_id FROM reporting.inventory_stock_balances GROUP BY 1, 2 HAVING sum(quantity) > 0) x
		WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
	{key: "low_stock", label: "Low Stock", unit: "count", def: "Warehouse × item below the reorder point or minimum stock",
		sql: `SELECT count(*)::text FROM (SELECT warehouse_id, item_id FROM reporting.inventory_stock_balances GROUP BY 1, 2
		HAVING sum(quantity) < coalesce(max(reorder_point), nullif(max(min_stock), 0))) x WHERE $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
	{key: "stock_movement", label: "Stock Movement", unit: "count", def: "Stock movements posted in the period",
		sql: `SELECT count(DISTINCT movement_id)::text FROM reporting.inventory_movement_lines WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		breakdown: `SELECT movement_type AS label, count(DISTINCT movement_id)::text AS value FROM reporting.inventory_movement_lines
		WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "stock_opname", label: "Stock Opname", unit: "idr", def: "Variance value of the stock opnames posted in the period",
		sql: `SELECT trim_scale(round(coalesce(sum(variance_value), 0), 2))::text FROM reporting.inventory_opname_lines WHERE status = 'posted'
		AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
		breakdown: `SELECT warehouse_name AS label, trim_scale(round(sum(variance_value), 0))::text AS value FROM reporting.inventory_opname_lines
		WHERE status = 'posted' AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	{key: "consumption_variance", label: "Theoretical vs Actual Variance", unit: "idr",
		def: "Actual usage cost of the outlet warehouses − theoretical (recipe) cost of the sales",
		sql: `WITH ` + invActual + `,
		s AS (SELECT coalesce(sum(cost_amount), 0) AS cost FROM reporting.inventory_sales WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		  AND outlet_id IN (SELECT outlet_id FROM reporting.inventory_outlet_warehouses))
		SELECT trim_scale(round(coalesce((SELECT sum(actual_cost) FROM a), 0) - (SELECT cost FROM s), 2))::text`},
	{key: "food_cost_actual", label: "Food Cost % (actual)", unit: "ratio", def: "Actual usage cost of the outlet warehouses ÷ net sales of those outlets",
		sql: `WITH ` + invActual + `,
		s AS (SELECT coalesce(sum(net_amount), 0) AS sales FROM reporting.inventory_sales WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		  AND outlet_id IN (SELECT outlet_id FROM reporting.inventory_outlet_warehouses))
		SELECT trim_scale(round(coalesce((SELECT sum(actual_cost) FROM a) / nullif((SELECT sales FROM s), 0), 0), 4))::text`},
}

const inventoryPerformance = "inventory-performance"

func init() {
	dashboards[inventoryPerformance] = dashDef{name: "Inventory Performance", kpis: inventoryKPIs}
	if !slices.Contains(DashboardCodes, inventoryPerformance) {
		DashboardCodes = append(DashboardCodes, inventoryPerformance)
	}
}
