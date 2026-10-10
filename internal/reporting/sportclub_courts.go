package reporting

// Sport Club court reports with CSV / XLSX / PDF export (docs/requirement-
// booking-sportclub-mgcc.md FR-83, FR-84, FR-106): the booking report per
// court line and the settlement of the online payments, per transaction and
// per day and method. They open from Sport Club › Laporan & Ekspor, the
// Management dashboard and Finance (one permission per report).

func init() {
	P2Reports = append(P2Reports,
		sqlReport("sportclub.court_bookings", "Court Booking Report", "sportclub",
			"Court bookings per line: sport, court, time, customer, channel, status, payment status and amount (before the service fee).",
			cols("date|Date|datetime", "code|Booking", "sport|Sport", "court|Court", "start|Start|datetime", "end|End|datetime", "hours|Hours|number",
				"customer|Customer", "phone|Phone", "channel|Channel", "status|Status", "payStatus|Payment", "amount|Amount|number", "package|Package",
				"promo|Promo Code"),
			[]Param{{Key: "facilityId", Label: "Sport", Type: "uuid"}, {Key: "courtId", Label: "Court", Type: "uuid"},
				{Key: "channel", Label: "Channel", Type: "enum", Enum: []string{"website", "member_app", "walk_in", "phone", "recurring"}},
				{Key: "status", Label: "Status", Type: "enum", Enum: []string{"held", "confirmed", "checked_in", "completed", "no_show", "released", "cancelled"}},
				{Key: "payStatus", Label: "Payment", Type: "enum", Enum: []string{"unpaid", "partially_paid", "paid", "overpaid"}}}, 30,
			`SELECT (start_at AT TIME ZONE $3)::date AS "date", code, facility_name AS "sport", court_name AS "court", start_at AS "start", end_at AS "end",
			trim_scale(round(hours::numeric, 2))::text AS "hours", customer_name AS "customer", phone, channel,
			CASE WHEN void THEN 'void' ELSE line_status END AS "status", pay_status AS "payStatus", trim_scale(amount)::text AS "amount",
			package_code AS "package", promo_code AS "promo"
			FROM reporting.sport_court_lines WHERE (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
			AND ($4 = '' OR facility_id::text = $4) AND ($5 = '' OR court_id::text = $5) AND ($6 = '' OR channel = $6)
			AND ($7 = '' OR line_status = $7) AND ($8 = '' OR pay_status = $8) ORDER BY start_at, court_name`),
		sqlReport("sportclub.settlement", "Sport Club Online Settlement Report", "sportclub",
			"Online payments of the court bookings (mock gateway until the real one): rent, service fee, total received and settlement status.",
			cols("paidAt|Paid|datetime", "createdAt|Created|datetime", "number|Payment", "booking|Booking", "method|Method", "rent|Rent|number",
				"serviceFee|Service Fee|number", "amount|Total Received|number", "refunded|Refunded|number", "status|Settlement", "gateway|Gateway"),
			[]Param{{Key: "method", Label: "Method", Type: "enum", Enum: []string{"qris", "virtual_account", "card", "payment_gateway"}}}, 30,
			`SELECT paid_at AS "paidAt", created_at AS "createdAt", number, booking_code AS "booking", method_type AS "method",
			trim_scale(rent)::text AS "rent", trim_scale(service_fee)::text AS "serviceFee", trim_scale(amount)::text AS "amount",
			trim_scale(refunded_amount)::text AS "refunded",
			CASE status WHEN 'completed' THEN 'settled' WHEN 'pending' THEN 'pending' ELSE status END AS "status", integration_code AS "gateway"
			FROM reporting.sport_settlements WHERE (coalesce(paid_at, created_at) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
			AND ($4 = '' OR method_type = $4) ORDER BY coalesce(paid_at, created_at)`),
		sqlReport("sportclub.settlement_daily", "Sport Club Settlement per Day", "sportclub",
			"Online payments settled per day and method: count, amount received and service fees.",
			cols("date|Date|datetime", "method|Method", "payments|Payments|number", "amount|Amount|number", "serviceFee|Service Fee|number"), nil, 30,
			`SELECT (paid_at AT TIME ZONE $3)::date AS "date", method_type AS "method", count(*)::int AS "payments",
			trim_scale(sum(amount - refunded_amount))::text AS "amount", trim_scale(sum(service_fee))::text AS "serviceFee"
			FROM reporting.sport_settlements WHERE status IN ('completed', 'refunded') AND (paid_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
			GROUP BY 1, 2 ORDER BY 1, 2`),
	)
}
