package reporting

// Reports and KPI dashboards of Accounting & financial reports (P4 EP-16–23,
// FR-RPT-P4-03/04) on the acc_* reporting views: General Ledger Report,
// Trial Balance, Profit & Loss, Balance Sheet, Cash Flow, Accounts
// Receivable / Payable Aging Report, Bank Reconciliation Report, Deferred
// Revenue Report, Tax Report (PPN), Revenue by Business Line Report and
// Posting Exception Report, and the Financial Performance dashboard
// (Revenue by Business Line, AR, AP, Cash, Outstanding Payment, Deferred
// Revenue, Profit & Loss). The interactive statements with comparison and
// consolidation are served by /api/v1/accounting/reports/{kind}.

import "slices"

// plSign is +1 for income accounts (credit − debit) and −1 for costs.
const accSection = `CASE WHEN subtype IN ('other_income', 'other_expense') THEN 'Other Income & Expenses' WHEN account_type = 'revenue' THEN 'Revenue'
	WHEN account_type = 'cogs' THEN 'Cost of Sales' ELSE 'Operating Expenses' END`

var accountingReports = []*Report{
	sqlReport("accounting.general_ledger", "General Ledger Report", "accounting",
		"Journal lines of the period per account with the journal, source document, dimensions and partner (drill-down of the financial reports).",
		cols("date|Date|datetime", "journal|Journal", "journalType|Type", "account|Account", "accountName|Account Name", "description|Description",
			"debit|Debit|number", "credit|Credit|number", "businessLine|Business Line", "costCenter|Cost Center", "partner|Partner", "source|Source"),
		[]Param{{Key: "account", Label: "Account Code", Type: "string"}}, 31,
		`SELECT journal_date AS "date", journal_number AS "journal", journal_type AS "journalType", account_code AS "account", account_name AS "accountName",
		coalesce(description, journal_description) AS "description", trim_scale(debit)::text AS "debit", trim_scale(credit)::text AS "credit",
		business_line AS "businessLine", cost_center AS "costCenter", partner_name AS "partner", journal_source_type || ' ' || coalesce(source_ref, '') AS "source"
		FROM reporting.acc_ledger WHERE journal_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR account_code = $4)
		ORDER BY journal_date, journal_number, account_code`),
	sqlReport("accounting.trial_balance", "Trial Balance", "accounting",
		"Opening balance, debits, credits and closing balance per account for the period; debits equal credits.",
		cols("account|Account", "accountName|Account Name", "type|Type", "opening|Opening (Dr − Cr)|number", "debit|Debit|number", "credit|Credit|number",
			"closing|Closing (Dr − Cr)|number"), nil, 31,
		`SELECT account_code AS "account", min(account_name) AS "accountName", min(account_type) AS "type",
		trim_scale(coalesce(sum(amount) FILTER (WHERE journal_date < $1::date), 0))::text AS "opening",
		trim_scale(coalesce(sum(debit) FILTER (WHERE journal_date >= $1::date), 0))::text AS "debit",
		trim_scale(coalesce(sum(credit) FILTER (WHERE journal_date >= $1::date), 0))::text AS "credit", trim_scale(sum(amount))::text AS "closing"
		FROM reporting.acc_ledger WHERE journal_date <= $2::date AND $3::text <> '' GROUP BY account_code ORDER BY account_code`),
	sqlReport("accounting.profit_loss", "Profit & Loss", "accounting",
		"Revenue, cost of sales, operating expenses and other income per account for the period (income positive, costs positive; year-end closing excluded).",
		cols("section|Section", "account|Account", "accountName|Account Name", "amount|Amount|number"), nil, 31,
		`SELECT `+accSection+` AS "section", account_code AS "account", min(account_name) AS "accountName",
		trim_scale(sum(CASE WHEN account_type = 'revenue' THEN credit - debit ELSE debit - credit END))::text AS "amount"
		FROM reporting.acc_ledger WHERE account_type IN ('revenue', 'cogs', 'expense') AND journal_type <> 'closing'
		  AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1, 2 ORDER BY 2`),
	sqlReport("accounting.balance_sheet", "Balance Sheet", "accounting",
		"Assets, liabilities and equity per account at the end date, with the current earnings not yet closed to retained earnings.",
		cols("section|Section", "account|Account", "accountName|Account Name", "amount|Amount|number"), nil, 0,
		`SELECT CASE account_type WHEN 'asset' THEN 'Assets' WHEN 'liability' THEN 'Liabilities' ELSE 'Equity' END AS "section", account_code AS "account",
		min(account_name) AS "accountName", trim_scale(sum(CASE WHEN account_type = 'asset' THEN amount ELSE -amount END))::text AS "amount"
		FROM reporting.acc_ledger WHERE account_type IN ('asset', 'liability', 'equity') AND journal_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> ''
		GROUP BY account_type, account_code
		UNION ALL
		SELECT 'Equity', '', 'Current Earnings (not yet closed)', trim_scale(coalesce(-sum(amount), 0))::text FROM reporting.acc_ledger
		WHERE account_type IN ('revenue', 'cogs', 'expense') AND journal_date <= $2::date
		ORDER BY 1, 2`),
	sqlReport("accounting.cash_flow", "Cash Flow", "accounting",
		"Movement of the period per cash flow group (indirect method): net income, operating, investing and financing changes, and the change in cash.",
		cols("group|Group", "account|Account", "accountName|Account Name", "amount|Cash Effect|number"), nil, 31,
		`SELECT CASE WHEN account_type IN ('revenue', 'cogs', 'expense') THEN '1 Net Income' WHEN cash_flow = 'cash' THEN '5 Change in Cash'
		  WHEN cash_flow = 'investing' AND subtype <> 'accumulated_depreciation' THEN '3 Investing' WHEN cash_flow = 'financing' THEN '4 Financing'
		  ELSE '2 Operating' END AS "group", account_code AS "account", min(account_name) AS "accountName",
		trim_scale(CASE WHEN min(cash_flow) = 'cash' THEN sum(amount) ELSE -sum(amount) END)::text AS "amount"
		FROM reporting.acc_ledger WHERE journal_type NOT IN ('opening', 'closing') AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> ''
		GROUP BY 1, 2 HAVING sum(amount) <> 0 ORDER BY 1, 2`),
	sqlReport("accounting.ar_aging", "Accounts Receivable Aging Report", "accounting",
		"Open billing invoices at the end date by age since the invoice date (0–30, 31–60, 61–90, > 90 days); equals the AR control account.",
		cols("billTo|Bill To", "invoice|Invoice", "issueDate|Issue Date|datetime", "dueDate|Due Date|datetime", "d0_30|0–30|number", "d31_60|31–60|number",
			"d61_90|61–90|number", "d90|> 90|number", "total|Open|number"), nil, 0,
		`WITH open AS (SELECT i.bill_to_name, i.number, i.issue_date, i.due_date,
		  i.total - coalesce((SELECT sum(a.amount) FROM reporting.acc_allocations a WHERE a.invoice_id = i.id AND a.created_at < (($2::date + 1)::timestamp AT TIME ZONE $3)), 0)
		  - coalesce((SELECT sum(c.amount) FROM reporting.acc_credit_notes c WHERE c.invoice_id = i.id AND c.created_at < (($2::date + 1)::timestamp AT TIME ZONE $3)), 0)
		  - coalesce((SELECT sum(w.amount) FROM reporting.acc_write_offs w WHERE w.invoice_id = i.id AND w.status = 'approved' AND w.decided_at < (($2::date + 1)::timestamp AT TIME ZONE $3)), 0)
		  AS open_amount FROM reporting.acc_invoices i WHERE i.issue_date <= $2::date
		  AND (i.status NOT IN ('draft', 'void') OR (i.status = 'void' AND i.voided_at >= (($2::date + 1)::timestamp AT TIME ZONE $3))))
		SELECT bill_to_name AS "billTo", number AS "invoice", issue_date AS "issueDate", due_date AS "dueDate",
		trim_scale(CASE WHEN $2::date - issue_date <= 30 THEN open_amount ELSE 0 END)::text AS "d0_30",
		trim_scale(CASE WHEN $2::date - issue_date BETWEEN 31 AND 60 THEN open_amount ELSE 0 END)::text AS "d31_60",
		trim_scale(CASE WHEN $2::date - issue_date BETWEEN 61 AND 90 THEN open_amount ELSE 0 END)::text AS "d61_90",
		trim_scale(CASE WHEN $2::date - issue_date > 90 THEN open_amount ELSE 0 END)::text AS "d90", trim_scale(open_amount)::text AS "total"
		FROM open WHERE open_amount > 0 AND $1::date IS NOT NULL AND $3::text <> '' ORDER BY bill_to_name, issue_date`),
	sqlReport("accounting.ap_aging", "Accounts Payable Aging Report", "accounting",
		"Open payables per supplier by days past due at the end date.",
		cols("supplier|Supplier", "number|Document", "type|Type", "dueDate|Due Date|datetime", "current|Not Due|number", "d1_30|1–30|number",
			"d31_60|31–60|number", "d61_90|61–90|number", "d90|> 90|number", "total|Open|number"), nil, 0,
		`SELECT supplier_name AS "supplier", number, item_type AS "type", due_date AS "dueDate",
		trim_scale(CASE WHEN due_date >= $2::date THEN outstanding ELSE 0 END)::text AS "current",
		trim_scale(CASE WHEN $2::date - due_date BETWEEN 1 AND 30 THEN outstanding ELSE 0 END)::text AS "d1_30",
		trim_scale(CASE WHEN $2::date - due_date BETWEEN 31 AND 60 THEN outstanding ELSE 0 END)::text AS "d31_60",
		trim_scale(CASE WHEN $2::date - due_date BETWEEN 61 AND 90 THEN outstanding ELSE 0 END)::text AS "d61_90",
		trim_scale(CASE WHEN $2::date - due_date > 90 THEN outstanding ELSE 0 END)::text AS "d90", trim_scale(outstanding)::text AS "total"
		FROM reporting.acc_ap_items WHERE status IN ('open', 'partially_paid') AND invoice_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> ''
		ORDER BY supplier_name, due_date`),
	sqlReport("accounting.bank_reconciliation", "Bank Reconciliation Report", "accounting",
		"Bank statement lines of the period with their reconciliation status; unmatched lines are the exceptions.",
		cols("bankAccount|Bank Account", "date|Date|datetime", "description|Description", "reference|Reference", "amount|Amount|number", "status|Status"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"unmatched", "matched", "ignored"}}}, 31,
		`SELECT bank_account_name AS "bankAccount", tx_date AS "date", description, reference, trim_scale(amount)::text AS "amount", status
		FROM reporting.acc_bank_transactions WHERE tx_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR status = $4)
		ORDER BY bank_account_name, tx_date`),
	sqlReport("accounting.deferred_revenue", "Deferred Revenue Report", "accounting",
		"Deferrals, recognition and breakage of vouchers, prepaid, annual fees and packages in the period.",
		cols("liability|Liability", "entryType|Movement", "component|Component", "amount|Amount|number", "entries|Entries|number"), nil, 31,
		`SELECT liability_type AS "liability", entry_type AS "entryType", revenue_component AS "component", trim_scale(sum(amount))::text AS "amount",
		count(*)::int AS "entries" FROM reporting.acc_deferred_entries WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`),
	sqlReport("accounting.tax_ppn", "Tax Report (PPN)", "accounting",
		"Output and input tax invoices (e-Faktur) of the period: DPP, PPN, serial number and status.",
		cols("direction|Direction", "taxPeriod|Tax Period", "date|Date|datetime", "document|Document", "partner|Partner", "npwp|NPWP", "dpp|DPP|number",
			"ppn|PPN|number", "faktur|Faktur Number", "status|Status"),
		[]Param{{Key: "direction", Label: "Direction", Type: "enum", Enum: []string{"output", "input"}}}, 31,
		`SELECT direction, tax_period AS "taxPeriod", invoice_date AS "date", source_number AS "document", partner_name AS "partner", partner_npwp AS "npwp",
		trim_scale(dpp)::text AS "dpp", trim_scale(ppn)::text AS "ppn", faktur_number AS "faktur", status
		FROM reporting.acc_tax_invoices WHERE invoice_date BETWEEN $1::date AND $2::date AND status <> 'cancelled' AND $3::text <> '' AND ($4 = '' OR direction = $4)
		ORDER BY direction, invoice_date`),
	sqlReport("accounting.revenue_by_business_line", "Revenue by Business Line Report", "accounting",
		"Revenue of the period per business line and revenue account (credit − debit of the revenue accounts).",
		cols("businessLine|Business Line", "account|Account", "accountName|Account Name", "amount|Revenue|number"), nil, 31,
		`SELECT coalesce(nullif(business_line, ''), 'unassigned') AS "businessLine", account_code AS "account", min(account_name) AS "accountName",
		trim_scale(sum(credit - debit))::text AS "amount" FROM reporting.acc_ledger WHERE account_type = 'revenue' AND subtype <> 'other_income'
		AND journal_type <> 'closing' AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("accounting.posting_exception", "Posting Exception Report", "accounting",
		"Events that could not be posted completely (no rule, missing account, closed period …) and their resolution.",
		cols("date|Date|datetime", "eventType|Event", "reason|Reason", "message|Message", "amount|Amount|number", "status|Status", "attempts|Attempts|number",
			"resolvedAt|Resolved|datetime"),
		[]Param{{Key: "status", Label: "Status", Type: "enum", Enum: []string{"open", "resolved", "ignored"}}}, 31,
		`SELECT created_at AS "date", event_type AS "eventType", reason, message, trim_scale(amount)::text AS "amount", status, attempts,
		resolved_at AS "resolvedAt" FROM reporting.acc_posting_exceptions WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		AND ($4 = '' OR status = $4) ORDER BY created_at DESC`),
}

// financialKPIs are the figures of the Financial Performance dashboard
// (FR-RPT-P4-03).
var financialKPIs = []kpiDef{
	{key: "revenue", label: "Revenue by Business Line", unit: "idr", def: "Revenue of the period (credit − debit of the revenue accounts) per business line",
		sql: `SELECT trim_scale(coalesce(sum(credit - debit), 0))::text FROM reporting.acc_ledger WHERE account_type = 'revenue' AND subtype <> 'other_income'
		AND journal_type <> 'closing' AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		breakdown: `SELECT coalesce(nullif(business_line, ''), 'unassigned') AS label, trim_scale(sum(credit - debit))::text AS value FROM reporting.acc_ledger
		WHERE account_type = 'revenue' AND subtype <> 'other_income' AND journal_type <> 'closing' AND journal_date BETWEEN $1::date AND $2::date
		AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "net_income", label: "Profit & Loss", unit: "idr", def: "Net income of the period (revenue − cost of sales − expenses)",
		sql: `SELECT trim_scale(coalesce(-sum(amount), 0))::text FROM reporting.acc_ledger WHERE account_type IN ('revenue', 'cogs', 'expense')
		AND journal_type <> 'closing' AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
		breakdown: `SELECT CASE account_type WHEN 'revenue' THEN 'Revenue' WHEN 'cogs' THEN 'Cost of Sales' ELSE 'Expenses' END AS label,
		trim_scale(-sum(amount))::text AS value FROM reporting.acc_ledger WHERE account_type IN ('revenue', 'cogs', 'expense') AND journal_type <> 'closing'
		AND journal_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "accounts_receivable", label: "AR", unit: "idr", def: "Balance of the receivable accounts at the end date (invoiced, city ledger and guest ledger)",
		sql: `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM reporting.acc_ledger WHERE subtype = 'receivable' AND journal_date <= $2::date
		AND $1::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT account_name AS label, trim_scale(sum(amount))::text AS value FROM reporting.acc_ledger WHERE subtype = 'receivable'
		AND journal_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "outstanding_payment", label: "Outstanding Payment", unit: "idr", def: "Open invoices past their due date at the end date",
		sql: `SELECT trim_scale(coalesce(sum(i.total - i.paid_amount - i.credited_amount - i.written_off_amount), 0))::text FROM reporting.acc_invoices i
		WHERE i.status IN ('issued', 'partially_paid', 'overdue') AND i.due_date < $2::date AND $1::date IS NOT NULL AND $3::text <> ''`},
	{key: "accounts_payable", label: "AP", unit: "idr", def: "Open payables to suppliers",
		sql: `SELECT trim_scale(coalesce(sum(outstanding), 0))::text FROM reporting.acc_ap_items WHERE status IN ('open', 'partially_paid')
		AND invoice_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT supplier_name AS label, trim_scale(sum(outstanding))::text AS value FROM reporting.acc_ap_items WHERE status IN ('open', 'partially_paid')
		AND invoice_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 2 DESC LIMIT 10`},
	{key: "cash", label: "Cash", unit: "idr", def: "Cash, bank and clearing balances at the end date",
		sql: `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM reporting.acc_ledger WHERE cash_flow = 'cash' AND journal_date <= $2::date
		AND $1::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT account_name AS label, trim_scale(sum(amount))::text AS value FROM reporting.acc_ledger WHERE cash_flow = 'cash'
		AND journal_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "deferred_revenue", label: "Deferred Revenue", unit: "idr", def: "Deferred revenue, deposits and loyalty liabilities at the end date",
		sql: `SELECT trim_scale(coalesce(-sum(amount), 0))::text FROM reporting.acc_ledger WHERE subtype IN ('deferred', 'deposit') AND journal_date <= $2::date
		AND $1::date IS NOT NULL AND $3::text <> ''`,
		breakdown: `SELECT account_name AS label, trim_scale(-sum(amount))::text AS value FROM reporting.acc_ledger WHERE subtype IN ('deferred', 'deposit')
		AND journal_date <= $2::date AND $1::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
	{key: "posting_exceptions", label: "Posting Exceptions", unit: "count", def: "Open posting exceptions",
		sql: `SELECT count(*)::text FROM reporting.acc_posting_exceptions WHERE status = 'open' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
}

const financialPerformance = "financial-performance"

func init() {
	dashboards[financialPerformance] = dashDef{name: "Financial Performance", kpis: financialKPIs}
	if !slices.Contains(DashboardCodes, financialPerformance) {
		DashboardCodes = append(DashboardCodes, financialPerformance)
	}
}
