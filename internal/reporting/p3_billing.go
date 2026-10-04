package reporting

// Reports of billing (PRD P3 EP-17/18, FR-RPT-P3-05): Invoice Report,
// Accounts Receivable Aging Report, Payment Schedule Report, Daily Revenue
// Report, Night Audit Report and Cashier Shift Report on the billing_*
// reporting views. The ageing follows the operational ageing of billing
// (buckets by days since the issue date, settlements dated up to the as-of
// date), so the report equals the open invoice balances (EP-23 AC).

var billingP3Reports = []*Report{
	sqlReport("billing.invoice", "Invoice Report", "billing", "Invoices issued in the period with payments, credit notes, write-offs and the outstanding amount.",
		cols("number|Invoice", "kind|Kind", "billTo|Bill To", "issueDate|Issue Date|datetime", "dueDate|Due Date|datetime", "total|Total|number",
			"paid|Paid|number", "credited|Credited|number", "writtenOff|Written Off|number", "outstanding|Outstanding|number", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"issued", "partially_paid", "paid", "overdue", "void"}}}, 30,
		`SELECT number, kind, bill_to_name AS "billTo", issue_date AS "issueDate", due_date AS "dueDate", trim_scale(total)::text AS "total",
		trim_scale(paid_amount)::text AS "paid", trim_scale(credited_amount)::text AS "credited", trim_scale(written_off_amount)::text AS "writtenOff",
		trim_scale(CASE WHEN status = 'void' THEN 0 ELSE outstanding END)::text AS "outstanding", status
		FROM reporting.billing_invoices WHERE issue_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR status = $4)
		ORDER BY issue_date, number`),
	sqlReport("billing.ar_aging", "Accounts Receivable Aging Report", "billing",
		"Open invoice balances per bill-to as of the To date, aged by days since issue (0–30, 31–60, 61–90, > 90) with the overdue part.",
		cols("billTo|Bill To", "invoices|Invoices|number", "d0to30|0–30|number", "d31to60|31–60|number", "d61to90|61–90|number", "over90|> 90|number",
			"total|Total|number", "overdue|Overdue|number"), nil, 0,
		`WITH open AS (
		  SELECT i.account_id, i.corporate_account_id, i.customer_id, i.bill_to_name, i.issue_date, i.due_date,
		    i.total - coalesce((SELECT sum(m.amount) FROM reporting.billing_invoice_movements m WHERE m.invoice_id = i.invoice_id
		      AND m.occurred_at < ($2::date + 1)), 0) AS open_amount
		  FROM reporting.billing_invoices i WHERE i.issue_date <= $2::date AND $1::text <> '' AND $3::text <> ''
		    AND (i.status NOT IN ('draft', 'void') OR (i.status = 'void' AND i.voided_at >= ($2::date + 1))))
		SELECT min(bill_to_name) AS "billTo", count(*)::int AS "invoices",
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date <= 30), 0))::text AS "d0to30",
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 31 AND 60), 0))::text AS "d31to60",
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date BETWEEN 61 AND 90), 0))::text AS "d61to90",
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE $2::date - issue_date > 90), 0))::text AS "over90",
		  trim_scale(sum(open_amount))::text AS "total",
		  trim_scale(coalesce(sum(open_amount) FILTER (WHERE due_date < $2::date), 0))::text AS "overdue"
		FROM open WHERE open_amount > 0 GROUP BY account_id, corporate_account_id, customer_id ORDER BY min(bill_to_name)`),
	sqlReport("billing.payment_schedule", "Payment Schedule Report", "billing",
		"Down payments, installments and final payments due in the period with what is paid and outstanding.",
		cols("schedule|Schedule", "title|Title", "customer|Customer", "source|Source", "line|Due Item", "kind|Kind", "dueDate|Due Date|datetime",
			"amount|Amount|number", "paid|Paid|number", "outstanding|Outstanding|number", "status|Status", "invoice|Invoice"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"pending", "partially_paid", "paid", "overdue", "cancelled"}}}, 60,
		`SELECT schedule_number AS "schedule", title, customer_name AS "customer", source_type AS "source", label AS "line", kind, due_date AS "dueDate",
		trim_scale(amount)::text AS "amount", trim_scale(paid_amount)::text AS "paid", trim_scale(outstanding)::text AS "outstanding", status,
		invoice_number AS "invoice" FROM reporting.billing_payment_schedules
		WHERE due_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR status = $4) ORDER BY due_date, schedule_number, seq`),
	sqlReport("billing.daily_revenue", "Daily Revenue Report", "billing",
		"Charges per business day, business line and revenue component (net, service, tax, total); liabilities flagged.",
		cols("businessDate|Business Date|datetime", "businessLine|Business Line", "component|Revenue Component", "liability|Liability|boolean",
			"net|Net|number", "service|Service|number", "tax|Tax|number", "total|Total|number"), nil, 7,
		`SELECT business_date AS "businessDate", business_line AS "businessLine", revenue_component AS "component", liability,
		trim_scale(sum(net_amount))::text AS "net", trim_scale(sum(service_amount))::text AS "service", trim_scale(sum(tax_amount))::text AS "tax",
		trim_scale(sum(total))::text AS "total" FROM reporting.billing_daily_revenue WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3`),
	sqlReport("billing.night_audit", "Night Audit Report", "billing",
		"Night audit runs per business day: mode, result, exceptions and warnings, with the frozen day totals.",
		cols("businessDate|Business Date|datetime", "mode|Mode", "status|Result", "exceptions|Exceptions|number", "warnings|Warnings|number",
			"startedAt|Started|datetime", "runBy|Run By", "dayStatus|Business Day", "charges|Charges|number", "payments|Payments|number"), nil, 30,
		`SELECT r.business_date AS "businessDate", r.mode, r.status, r.exceptions, r.warnings, r.started_at AS "startedAt", r.run_by_name AS "runBy",
		coalesce(d.status, 'open') AS "dayStatus", d.charges, d.payment_total AS "payments"
		FROM reporting.billing_night_audit_runs r LEFT JOIN reporting.billing_business_days d ON d.property_id = r.property_id AND d.business_date = r.business_date
		WHERE r.business_date BETWEEN $1::date AND $2::date AND $3::text <> '' ORDER BY r.started_at`),
	sqlReport("billing.cashier_shift", "Cashier Shift Report", "billing",
		"Cashier shifts per business day: opening float, payments taken, expected and counted cash and the variance.",
		cols("number|Shift", "station|Station", "cashier|Cashier", "businessDate|Business Date|datetime", "openedAt|Opened|datetime",
			"closedAt|Closed|datetime", "openingFloat|Opening Float|number", "payments|Payments|number", "expectedCash|Expected Cash|number",
			"countedCash|Counted Cash|number", "variance|Variance|number", "status|Status"), nil, 7,
		`SELECT number, station, cashier_name AS "cashier", business_date AS "businessDate", opened_at AS "openedAt", closed_at AS "closedAt",
		trim_scale(opening_float)::text AS "openingFloat", trim_scale(payments)::text AS "payments", trim_scale(expected_cash)::text AS "expectedCash",
		trim_scale(counted_cash)::text AS "countedCash", trim_scale(variance)::text AS "variance", status
		FROM reporting.billing_cashier_shifts WHERE business_date BETWEEN $1::date AND $2::date AND $3::text <> '' ORDER BY opened_at`),
}
