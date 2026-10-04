package reporting

// P2 reports (PRD P2 FR-RPT-P2-06/07) on the P2 read models (reporting
// views, Technical Doc §4.2): date range filter (local dates of
// the instance timezone), property scope through RLS, CSV/XLSX export and
// one permission per report. Money is returned as text (exact decimals).

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/catalog"
)

var rangeParams = []Param{{Key: "from", Label: "From", Type: "date"}, {Key: "to", Label: "To", Type: "date"}}

func period(ctx context.Context, tx pgx.Tx, p map[string]string, defDays int) (string, string, string) {
	loc := calendar.Location(ctx, tx)
	now := time.Now().In(loc)
	from, to := now.AddDate(0, 0, -defDays).Format("2006-01-02"), now.Format("2006-01-02")
	if v := p["from"]; v != "" {
		if _, err := time.Parse("2006-01-02", v); err == nil {
			from = v
		}
	}
	if v := p["to"]; v != "" {
		if _, err := time.Parse("2006-01-02", v); err == nil {
			to = v
		}
	}
	return from, to, loc.String()
}

// sqlReport builds a report from one SQL statement. $1 = from date, $2 = to
// date (inclusive), $3 = timezone name, then the extra parameters in order.
func sqlReport(code, name, module, desc string, cols []Column, extra []Param, defDays int, sql string) *Report {
	return &Report{Code: code, Name: name, Module: module, Description: desc, Columns: cols, Params: append(append([]Param{}, rangeParams...), extra...),
		Permission: "reporting." + strings.ReplaceAll(code, ".", "_") + ".view",
		Query: func(ctx context.Context, tx pgx.Tx, p map[string]string, limit int) ([]map[string]any, error) {
			from, to, tz := period(ctx, tx, p, defDays)
			args := []any{from, to, tz}
			for _, e := range extra {
				args = append(args, p[e.Key])
			}
			rows, err := tx.Query(ctx, sql+limitClause(limit), args...)
			if err != nil {
				return nil, err
			}
			return collect(rows)
		}}
}

func cols(spec ...string) []Column {
	out := make([]Column, 0, len(spec))
	for _, s := range spec {
		parts := strings.SplitN(s, "|", 3)
		c := Column{Key: parts[0], Label: parts[1], Type: "string"}
		if len(parts) == 3 {
			c.Type = parts[2]
		}
		out = append(out, c)
	}
	return out
}

// P2Reports are the reports of PRD P2 FR-RPT-P2-06.
var P2Reports = []*Report{
	sqlReport("golf.round_history", "Round History Report", "golf", "Finished rounds per player with route, gross score, duration and caddies.",
		cols("date|Date|datetime", "bookingCode|Booking", "flightNo|Flight|number", "route|Playing Route", "player|Player", "playerType|Player Type",
			"gross|Gross|number", "scorecardStatus|Scorecard", "roundMinutes|Round (min)|number", "caddies|Caddies"),
		[]Param{{Key: "playerType", Label: "Player Type", Type: "enum", Enum: []string{"member", "guest_of_member", "reciprocal", "non_member"}}}, 30,
		`SELECT play_date AS "date", booking_code AS "bookingCode", flight_no AS "flightNo", route_name AS "route", player_name AS "player",
		player_type AS "playerType", gross, scorecard_status AS "scorecardStatus", round_minutes AS "roundMinutes", caddies
		FROM reporting.golf_rounds WHERE play_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR player_type = $4)
		ORDER BY tee_off_at, player_name`),
	sqlReport("golf.caddy_utilization", "Caddy Utilization Report", "golf", "Days present, rounds, rounds per day, duty and on-course hours per caddy.",
		cols("code|Caddy No.", "name|Caddy", "level|Level", "daysPresent|Days Present|number", "rounds|Rounds|number", "roundsPerDay|Rounds / Day|number",
			"dutyHours|Duty Hours|number", "onCourseHours|On-course Hours|number"), nil, 30,
		`WITH d AS (SELECT caddy_id, count(*) FILTER (WHERE status = 'present') AS days, sum(duty_hours) AS hours FROM reporting.golf_caddy_duty
		  WHERE work_date BETWEEN $1::date AND $2::date GROUP BY caddy_id),
		r AS (SELECT caddy_id, count(*) AS rounds, sum(extract(epoch FROM finished_at - started_at)) / 3600 AS hours FROM reporting.golf_caddy_assignments
		  WHERE status IN ('completed', 'replaced') AND play_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY caddy_id)
		SELECT c.code, c.name, c.level_name AS "level", coalesce(d.days, 0)::int AS "daysPresent", coalesce(r.rounds, 0)::int AS "rounds",
		trim_scale(round(coalesce(r.rounds, 0)::numeric / nullif(d.days, 0), 2))::text AS "roundsPerDay",
		trim_scale(round(coalesce(d.hours, 0)::numeric, 2))::text AS "dutyHours", trim_scale(round(coalesce(r.hours, 0)::numeric, 2))::text AS "onCourseHours"
		FROM reporting.golf_caddies c LEFT JOIN d ON d.caddy_id = c.caddy_id LEFT JOIN r ON r.caddy_id = c.caddy_id ORDER BY c.code`),
	sqlReport("golf.caddy_settlement", "Caddy Settlement Report", "golf", "Caddy fee settlements: fee, tips, deductions, total and payment status.",
		cols("number|Settlement", "caddy|Caddy", "periodStart|From|datetime", "periodEnd|To|datetime", "rounds|Rounds|number", "caddyFee|Caddy Fee|number",
			"tips|Tips|number", "deductions|Deductions|number", "total|Total|number", "status|Status"), nil, 31,
		`SELECT number, caddy_name AS "caddy", period_start AS "periodStart", period_end AS "periodEnd", rounds, trim_scale(caddy_fee)::text AS "caddyFee",
		trim_scale(tips)::text AS "tips", trim_scale(deductions)::text AS "deductions", trim_scale(total)::text AS "total", status
		FROM reporting.golf_caddy_settlements WHERE period_end >= $1::date AND period_start <= $2::date AND $3::text <> '' ORDER BY period_start, caddy_code`),
	sqlReport("golf.cart_maintenance", "Golf Cart Maintenance Report", "golf", "Maintenance records with category, cost and release.",
		cols("number|Maintenance", "golfCart|Golf Cart", "category|Category", "description|Description", "cost|Cost|number", "status|Status",
			"openedAt|Opened|datetime", "closedAt|Closed|datetime", "hoursSinceService|Hours since Service|number"), nil, 90,
		`SELECT number, golf_cart_code AS "golfCart", category, description, trim_scale(cost)::text AS "cost", status, opened_at AS "openedAt",
		closed_at AS "closedAt", trim_scale(hours_since_service)::text AS "hoursSinceService"
		FROM reporting.golf_cart_maintenance WHERE (opened_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY opened_at`),
	sqlReport("golf.hole_in_one", "Hole-in-One Report", "golf", "Hole-in-One records, verification and insurance claims.",
		cols("number|Record", "player|Player", "hole|Hole", "achievedOn|Date|datetime", "status|Status", "insured|Insured|boolean", "claimStatus|Claim",
			"claimPaid|Claim Paid|number"), nil, 365,
		`SELECT number, player_name AS "player", hole, achieved_on AS "achievedOn", status, insured, claim_status AS "claimStatus",
		trim_scale(claim_paid_amount)::text AS "claimPaid" FROM reporting.golf_hole_in_ones
		WHERE achieved_on BETWEEN $1::date AND $2::date AND $3::text <> '' ORDER BY achieved_on`),
	sqlReport("golf.driving_range_usage", "Driving Range Usage Report", "golf", "Per day: sessions, buckets, balls by source and revenue.",
		cols("date|Date|datetime", "sessions|Sessions|number", "buckets|Buckets|number", "balls|Balls|number", "prepaidBalls|Prepaid Balls|number",
			"revenue|Revenue|number"), nil, 30,
		`SELECT d::date AS "date",
		(SELECT count(*) FROM reporting.golf_range_sessions s WHERE (s.queued_at AT TIME ZONE $3)::date = d::date)::int AS "sessions",
		(SELECT count(*) FROM reporting.golf_range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "buckets",
		(SELECT coalesce(sum(balls), 0) FROM reporting.golf_range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "balls",
		(SELECT coalesce(sum(balls), 0) FROM reporting.golf_range_buckets b WHERE b.source = 'prepaid' AND (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "prepaidBalls",
		(SELECT trim_scale(coalesce(sum(amount), 0))::text FROM reporting.golf_range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date) AS "revenue"
		FROM generate_series($1::date, $2::date, interval '1 day') d ORDER BY 1`),
	sqlReport("golf.reciprocal_visits", "Reciprocal Visit Report", "golf", "Inbound and outbound reciprocal visits, charges and settlement status.",
		cols("number|Visit", "direction|Direction", "club|Club", "country|Country", "visitor|Visitor", "visitDate|Date|datetime", "verified|Verified|boolean",
			"settlement|Settlement", "charge|Charge|number"), []Param{{Key: "direction", Label: "Direction", Type: "enum", Enum: []string{"inbound", "outbound"}}}, 90,
		`SELECT number, direction, club_name AS "club", country, visitor_name AS "visitor", visit_date AS "visitDate", verified, settlement_status AS "settlement",
		trim_scale(charge)::text AS "charge" FROM reporting.golf_reciprocal_visits
		WHERE visit_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR direction = $4) ORDER BY visit_date`),
	sqlReport("sportclub.court_utilization", "Court Utilization Report", "sportclub", "Bookings and booked hours per court against opening hours.",
		cols("court|Court", "facility|Facility", "bookings|Bookings|number", "bookedHours|Booked Hours|number", "availableHours|Available Hours|number",
			"utilization|Utilization|number"), nil, 30,
		`WITH t AS (SELECT extract(epoch FROM close_time - open_time) / 3600 AS h FROM reporting.opening_hours WHERE resource_type = 'sport_court'),
		b AS (SELECT l.resource_id, count(DISTINCT l.reservation_id) AS n, sum(extract(epoch FROM upper(l.period) - lower(l.period)) / 3600) AS hours
		  FROM reporting.reservation_lines l WHERE l.status IN ('confirmed', 'checked_in', 'completed')
		  AND (lower(l.period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY l.resource_id)
		SELECT c.court_name AS "court", c.facility_name AS "facility", coalesce(b.n, 0)::int AS "bookings",
		trim_scale(round(coalesce(b.hours, 0)::numeric, 2))::text AS "bookedHours",
		trim_scale(round(((($2::date - $1::date) + 1) * (SELECT h FROM t))::numeric, 2))::text AS "availableHours",
		trim_scale(round((coalesce(b.hours, 0) / nullif((($2::date - $1::date) + 1) * (SELECT h FROM t), 0))::numeric, 4))::text AS "utilization"
		FROM reporting.sport_courts c LEFT JOIN b ON b.resource_id = c.resource_id ORDER BY c.facility_name, c.court_name`),
	sqlReport("sportclub.class_attendance", "Class Attendance Report", "sportclub", "Per class session: capacity, booked, present, absent and excused.",
		cols("start|Session|datetime", "program|Class", "instructor|Instructor", "capacity|Capacity|number", "booked|Booked|number", "present|Present|number",
			"absent|Absent|number", "excused|Excused|number", "status|Status"), nil, 30,
		`SELECT starts_at AS "start", program_name AS "program", instructor_name AS "instructor", capacity, booked::int, present::int, absent::int,
		excused::int, status FROM reporting.sport_class_sessions WHERE (starts_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY starts_at`),
	sqlReport("sportclub.instructor_fee", "Instructor Fee Report", "sportclub", "Instructor honorarium statements and payment status.",
		cols("feeNo|Statement", "instructor|Instructor", "periodStart|From|datetime", "periodEnd|To|datetime", "sessions|Sessions|number",
			"students|Students|number", "amount|Amount|number", "status|Status"), nil, 31,
		`SELECT fee_no AS "feeNo", instructor_name AS "instructor", period_start AS "periodStart", period_end AS "periodEnd", sessions, students,
		trim_scale(amount)::text AS "amount", status FROM reporting.sport_instructor_fees
		WHERE period_end >= $1::date AND period_start <= $2::date AND $3::text <> '' ORDER BY period_start, instructor_name`),
	sqlReport("stay.bungalow_occupancy", "Bungalow Occupancy Report", "stay", "Per night: bungalows available, occupied and occupancy rate.",
		cols("date|Night|datetime", "units|Units|number", "occupied|Occupied|number", "occupancy|Occupancy|number"), nil, 30,
		`WITH u AS (SELECT count(*) AS n FROM reporting.stay_bungalows WHERE status = 'active'),
		o AS (SELECT d::date AS night, (SELECT count(DISTINCT s.unit_id) FROM reporting.stays s WHERE s.kind = 'bungalow'
		  AND s.status NOT IN ('cancelled', 'no_show', 'requested') AND (s.start_at AT TIME ZONE $3)::date <= d::date AND (s.end_at AT TIME ZONE $3)::date > d::date) AS n
		  FROM generate_series($1::date, $2::date, interval '1 day') d)
		SELECT o.night AS "date", (SELECT n FROM u)::int AS "units", o.n::int AS "occupied",
		trim_scale(round(o.n::numeric / nullif((SELECT n FROM u), 0), 4))::text AS "occupancy" FROM o ORDER BY 1`),
	sqlReport("stay.meeting_room_booking", "Meeting Room Booking Report", "stay", "Meeting room bookings with package, layout and pax.",
		cols("stayNo|Booking", "room|Meeting Room", "start|Start|datetime", "end|End|datetime", "pax|Pax|number", "package|Package", "layout|Layout",
			"customer|Customer", "status|Status"), nil, 30,
		`SELECT stay_no AS "stayNo", coalesce(unit_name, '') AS "room", start_at AS "start", end_at AS "end", pax, package_code AS "package", layout,
		customer_name AS "customer", status FROM reporting.stays
		WHERE kind = 'meeting_room' AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY start_at`),
	sqlReport("commercial.pos_sales", "POS Sales Report", "commercial", "Per day and outlet: orders, gross, discount, net, service, tax and total.",
		cols("date|Date|datetime", "outlet|Outlet", "orders|Orders|number", "gross|Gross|number", "discount|Discount|number", "net|Net|number",
			"service|Service|number", "tax|Tax|number", "total|Total|number", "averageTransaction|Average Transaction|number"),
		[]Param{{Key: "outletId", Label: "Outlet", Type: "uuid"}}, 7,
		`SELECT (created_at AT TIME ZONE $3)::date AS "date", outlet_name AS "outlet", count(DISTINCT order_id)::int AS "orders",
		trim_scale(sum(gross))::text AS "gross", trim_scale(sum(discount_amount))::text AS "discount", trim_scale(sum(net_amount))::text AS "net",
		trim_scale(sum(service_amount))::text AS "service", trim_scale(sum(tax_amount))::text AS "tax", trim_scale(sum(total_amount))::text AS "total",
		trim_scale(round(sum(total_amount) / nullif(count(DISTINCT order_id), 0), 2))::text AS "averageTransaction"
		FROM reporting.pos_sales WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR outlet_id::text = $4)
		GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("commercial.shift", "Shift Report", "commercial", "POS shifts: opening cash, expected, counted and variance.",
		cols("shiftNo|Shift", "outlet|Outlet", "cashier|Cashier", "openedAt|Opened|datetime", "closedAt|Closed|datetime", "openingCash|Opening Cash|number",
			"expectedCash|Expected Cash|number", "countedCash|Counted Cash|number", "variance|Variance|number", "status|Status"), nil, 7,
		`SELECT shift_no AS "shiftNo", outlet_name AS "outlet", cashier_name AS "cashier", opened_at AS "openedAt", closed_at AS "closedAt",
		trim_scale(opening_cash)::text AS "openingCash", trim_scale(expected_cash)::text AS "expectedCash", trim_scale(counted_cash)::text AS "countedCash",
		trim_scale(variance)::text AS "variance", status FROM reporting.pos_shifts WHERE (opened_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY opened_at`),
	sqlReport("commercial.voucher_liability", "Voucher Liability Report", "commercial",
		"Outstanding voucher & prepaid liability per voucher type at the end of the To date (equals the deferred revenue sub-ledger).",
		cols("voucherType|Voucher Type", "liabilityType|Liability", "vouchers|Vouchers|number", "sold|Deferred|number", "recognized|Recognised|number",
			"breakage|Breakage|number", "liability|Liability|number"), nil, 0,
		`SELECT voucher_type AS "voucherType", liability_type AS "liabilityType", count(DISTINCT voucher_id)::int AS "vouchers",
		trim_scale(coalesce(sum(amount) FILTER (WHERE entry_type = 'deferral'), 0))::text AS "sold",
		trim_scale(-coalesce(sum(amount) FILTER (WHERE entry_type = 'recognition'), 0))::text AS "recognized",
		trim_scale(-coalesce(sum(amount) FILTER (WHERE entry_type = 'breakage'), 0))::text AS "breakage",
		trim_scale(sum(amount))::text AS "liability"
		FROM reporting.voucher_ledger WHERE liability_type IN ('voucher', 'prepaid')
		AND occurred_at < (($2::date + 1)::timestamp AT TIME ZONE $3) AND $1::date IS NOT NULL GROUP BY 1, 2 ORDER BY 1`),
	sqlReport("commercial.voucher_breakage", "Voucher Breakage Report", "commercial", "Expired voucher balances recognised as breakage in the period.",
		cols("voucherType|Voucher Type", "vouchers|Vouchers|number", "breakage|Breakage|number"), nil, 30,
		`SELECT voucher_type AS "voucherType", count(DISTINCT voucher_id)::int AS "vouchers", trim_scale(-sum(amount))::text AS "breakage"
		FROM reporting.voucher_ledger WHERE entry_type = 'breakage' AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY 1 ORDER BY 1`),
	sqlReport("commercial.food_cost", "Food Cost Report", "commercial", "Theoretical food cost per outlet and product (BOM).",
		cols("outlet|Outlet", "product|Product", "quantity|Quantity|number", "netSales|Net Sales|number", "cost|Theoretical Cost|number",
			"foodCostPercent|Food Cost %|number", "hasRecipe|Recipe|boolean"), []Param{{Key: "outletId", Label: "Outlet", Type: "uuid"}}, 30,
		`SELECT outlet_name AS "outlet", product_name AS "product", trim_scale(sum(quantity))::text AS "quantity", trim_scale(sum(net_amount))::text AS "netSales",
		trim_scale(sum(cost_amount))::text AS "cost", trim_scale(round(sum(cost_amount) * 100 / nullif(sum(net_amount), 0), 2))::text AS "foodCostPercent",
		bool_and(has_recipe) AS "hasRecipe" FROM reporting.food_cost
		WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR outlet_id::text = $4)
		GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("membership.lifecycle", "Membership Lifecycle Report", "membership",
		"Per program and type: principal memberships by status, new, expiring in 30 days and lifecycle events in the period.",
		cols("program|Program", "type|Membership Type", "active|Active|number", "paused|Paused|number", "suspended|Suspended|number", "expired|Expired|number",
			"cancelled|Cancelled|number", "new|New|number", "expiring30|Expiring (30 days)|number", "pauses|Pauses|number", "renewals|Renewals|number"), nil, 30,
		`SELECT m.program_name AS "program", m.type_name AS "type",
		count(*) FILTER (WHERE m.status = 'active')::int AS "active", count(*) FILTER (WHERE m.status = 'paused')::int AS "paused",
		count(*) FILTER (WHERE m.status = 'suspended')::int AS "suspended", count(*) FILTER (WHERE m.status = 'expired')::int AS "expired",
		count(*) FILTER (WHERE m.status = 'cancelled')::int AS "cancelled",
		count(*) FILTER (WHERE m.starts_on BETWEEN $1::date AND $2::date)::int AS "new",
		count(*) FILTER (WHERE m.status = 'active' AND m.ends_on BETWEEN current_date AND current_date + 30)::int AS "expiring30",
		(SELECT count(*) FROM reporting.membership_events e WHERE e.type_id = m.type_id AND e.event = 'paused'
		  AND (e.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::int AS "pauses",
		(SELECT count(*) FROM reporting.membership_events e WHERE e.type_id = m.type_id AND e.event = 'renewed'
		  AND (e.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::int AS "renewals"
		FROM reporting.membership_lifecycle m WHERE m.role = 'principal' GROUP BY m.program_name, m.type_id, m.type_name ORDER BY 1, 2`),
}

// P2Permissions are the report and dashboard permissions of P2 with roles.
func P2Permissions() ([]catalog.Permission, map[string][]string) {
	perms := []catalog.Permission{}
	byModule := map[string][]string{}
	var all []string
	for _, r := range P2Reports {
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		byModule[r.Module] = append(byModule[r.Module], r.Permission)
		all = append(all, r.Permission)
	}
	cat := func(lists ...[]string) []string {
		var out []string
		for _, l := range lists {
			out = append(out, l...)
		}
		return out
	}
	dash := []string{"reporting.dashboard.view", "reporting.report.view", "reporting.export.create"}
	return perms, map[string][]string{
		"property_admin":     cat(dash, all),
		"general_manager":    cat(dash, all),
		"club_manager":       cat(dash, byModule["golf"], byModule["sportclub"], byModule["membership"], byModule["commercial"]),
		"resort_manager":     cat(dash, byModule["stay"], byModule["commercial"]),
		"finance_manager":    cat(dash, all),
		"golf_manager":       cat(dash, byModule["golf"]),
		"sport_club_manager": cat(dash, byModule["sportclub"]),
		"membership_manager": cat(dash, byModule["membership"]),
		"outlet_manager":     cat(dash, byModule["commercial"]),
		"caddy_manager":      {"reporting.report.view", "reporting.golf_caddy_utilization.view", "reporting.golf_caddy_settlement.view"},
		"accountant": {"reporting.report.view", "reporting.export.create", "reporting.commercial_voucher_liability.view", "reporting.commercial_voucher_breakage.view",
			"reporting.commercial_shift.view", "reporting.golf_caddy_settlement.view", "reporting.sportclub_instructor_fee.view"},
	}
}
