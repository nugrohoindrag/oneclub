package reporting

// Banquet food cost per event (PRD P4 §9.2, EP-05 FR-VAL-02, EP-28): actual
// cost of the stock deducted for a banquet event (K6, valuation cost, the
// amount of the banquet cost of sales journal DEF-INV-COGS-BQT) against the
// theoretical cost of its issued BEO for the final pax (recipe × standard
// cost), per event and per event × ingredient, on the
// inventory_banquet_food_cost_lines view. Also opened from the banquet event
// detail screen (Food Cost tab).

// banquetFoodCostEvent filters the events starting in the period and,
// optionally, one event number ($4).
const banquetFoodCostEvent = `(e.start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR upper(e.number) = upper($4))`

var banquetFoodCostReports = []*Report{
	sqlReport("inventory.banquet_food_cost", "Banquet Food Cost Report", "inventory",
		"Per banquet event starting in the period: F&B revenue (package + banquet F&B), theoretical food cost of the issued BEO for the final pax at standard "+
			"cost, actual food cost of the stock deducted at valuation cost (= banquet cost of sales journal), variance and both food cost percentages.",
		cols("eventDate|Event Date|datetime", "number|Event", "title|Title", "eventType|Type", "status|Status", "pax|Pax|number", "fnbRevenue|F&B Revenue|number",
			"theoreticalCost|Theoretical Cost|number", "actualCost|Actual Cost|number", "variance|Variance|number",
			"theoreticalPercent|Theoretical Food Cost %|number", "actualPercent|Actual Food Cost %|number", "actualPerPax|Actual Cost / Pax|number"),
		[]Param{{Key: "event", Label: "Event Number", Type: "string"}}, 30,
		`WITH l AS (SELECT event_id, sum(theoretical_cost) AS theo, sum(actual_cost) AS act FROM reporting.inventory_banquet_food_cost_lines GROUP BY 1),
		r AS (SELECT event_id, sum(net) AS revenue FROM reporting.banquet_revenue WHERE revenue_component IN ('banquet_package', 'banquet_fnb') GROUP BY 1)
		SELECT (e.start_at AT TIME ZONE $3)::date AS "eventDate", e.number, e.title, e.event_type AS "eventType", e.status, e.pax,
		trim_scale(round(coalesce(r.revenue, 0), 2))::text AS "fnbRevenue", trim_scale(round(l.theo, 2))::text AS "theoreticalCost",
		trim_scale(round(l.act, 2))::text AS "actualCost", trim_scale(round(l.act - l.theo, 2))::text AS "variance",
		trim_scale(round(l.theo / nullif(r.revenue, 0) * 100, 2))::text AS "theoreticalPercent",
		trim_scale(round(l.act / nullif(r.revenue, 0) * 100, 2))::text AS "actualPercent",
		trim_scale(round(l.act / nullif(e.pax, 0), 2))::text AS "actualPerPax"
		FROM reporting.banquet_events e JOIN l ON l.event_id = e.event_id LEFT JOIN r ON r.event_id = e.event_id
		WHERE `+banquetFoodCostEvent+` ORDER BY e.start_at, e.number`),
	sqlReport("inventory.banquet_food_cost_lines", "Banquet Food Cost Detail Report", "inventory",
		"Per banquet event and ingredient: theoretical quantity of the issued BEO for the final pax at standard cost against the actual quantity "+
			"deducted at valuation cost, with the quantity and cost variance.",
		cols("number|Event", "title|Title", "itemCode|Item Code", "item|Item", "uom|UOM", "theoreticalQuantity|Theoretical Qty|number",
			"standardCost|Standard Cost|number", "theoreticalCost|Theoretical Cost|number", "actualQuantity|Actual Qty|number",
			"actualUnitCost|Actual Unit Cost|number", "actualCost|Actual Cost|number", "quantityVariance|Qty Variance|number", "variance|Cost Variance|number"),
		[]Param{{Key: "event", Label: "Event Number", Type: "string"}}, 30,
		`SELECT e.number, e.title, l.item_code AS "itemCode", l.item_name AS "item", l.uom,
		trim_scale(round(l.theoretical_quantity, 6))::text AS "theoreticalQuantity", trim_scale(l.standard_cost)::text AS "standardCost",
		trim_scale(round(l.theoretical_cost, 2))::text AS "theoreticalCost", trim_scale(round(l.actual_quantity, 6))::text AS "actualQuantity",
		trim_scale(round(l.actual_cost / nullif(l.actual_quantity, 0), 4))::text AS "actualUnitCost", trim_scale(round(l.actual_cost, 2))::text AS "actualCost",
		trim_scale(round(l.actual_quantity - l.theoretical_quantity, 6))::text AS "quantityVariance",
		trim_scale(round(l.actual_cost - l.theoretical_cost, 2))::text AS "variance"
		FROM reporting.banquet_events e JOIN reporting.inventory_banquet_food_cost_lines l ON l.event_id = e.event_id
		WHERE `+banquetFoodCostEvent+` ORDER BY e.start_at, e.number, l.item_code`),
}
