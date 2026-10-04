package reporting

// P2 reports (PRD P2 FR-RPT-P2-06/07): date range filter (local dates of
// the instance timezone), property scope through RLS, CSV/XLSX export and
// one permission per report. Money is returned as text (exact decimals).

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/calendar"
)

var rangeParams = []Param{{Key: "from", Label: "From", Type: "date"}, {Key: "to", Label: "To", Type: "date"}}

func dateRange(ctx context.Context, tx pgx.Tx, p map[string]string, defDays int) (string, string, string) {
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
			from, to, tz := dateRange(ctx, tx, p, defDays)
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
		cols("date|Date|datetime", "flightNo|Flight", "route|Playing Route", "player|Player", "playerType|Player Type", "gross|Gross|number",
			"scorecardStatus|Scorecard", "roundMinutes|Round (min)|number", "caddies|Caddies"), []Param{{Key: "playerType", Label: "Player Type", Type: "enum",
			Enum: []string{"member", "guest_of_member", "visitor", "reciprocal"}}}, 30,
		`SELECT (f.tee_time AT TIME ZONE $3)::date AS "date", f.flight_no AS "flightNo", r.name AS "route", p.name AS "player", p.player_type AS "playerType",
		s.gross AS "gross", s.status AS "scorecardStatus", round(extract(epoch FROM f.finished_at - f.teed_off_at) / 60)::int AS "roundMinutes",
		(SELECT string_agg(c.code, ', ' ORDER BY a.from_seq) FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
		  WHERE a.flight_id = f.id AND (a.player_id IS NULL OR a.player_id = p.id) AND a.status IN ('completed', 'replaced')) AS "caddies"
		FROM golf.flights f JOIN golf.playing_routes r ON r.id = f.route_id JOIN golf.flight_players p ON p.flight_id = f.id
		LEFT JOIN golf.scorecards s ON s.player_id = p.id
		WHERE f.status = 'finished' AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR p.player_type = $4)
		ORDER BY f.tee_time, p.name`),
	sqlReport("golf.caddy_utilization", "Caddy Utilization Report", "golf", "Days present, rounds, rounds per day, duty and on-course hours per caddy.",
		cols("code|Caddy No.", "name|Caddy", "level|Level", "daysPresent|Days Present|number", "rounds|Rounds|number", "roundsPerDay|Rounds / Day|number",
			"dutyHours|Duty Hours|number", "onCourseHours|On-course Hours|number"), nil, 30,
		`SELECT c.code, c.name, l.name AS "level",
		(SELECT count(*) FROM golf.caddy_attendance a WHERE a.caddy_id = c.id AND a.work_date BETWEEN $1::date AND $2::date)::int AS "daysPresent",
		(SELECT count(*) FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id WHERE a.caddy_id = c.id AND a.status IN ('completed', 'replaced')
		  AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::int AS "rounds",
		trim_scale(round((SELECT count(*) FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id WHERE a.caddy_id = c.id
		  AND a.status IN ('completed', 'replaced') AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
		  nullif((SELECT count(*) FROM golf.caddy_attendance a WHERE a.caddy_id = c.id AND a.work_date BETWEEN $1::date AND $2::date), 0), 2))::text AS "roundsPerDay",
		trim_scale(round(coalesce((SELECT sum(extract(epoch FROM coalesce(a.clock_out, a.clock_in) - a.clock_in)) / 3600 FROM golf.caddy_attendance a
		  WHERE a.caddy_id = c.id AND a.work_date BETWEEN $1::date AND $2::date), 0)::numeric, 2))::text AS "dutyHours",
		trim_scale(round(coalesce((SELECT sum(extract(epoch FROM a.ended_at - a.started_at)) / 3600 FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id
		  WHERE a.caddy_id = c.id AND a.started_at IS NOT NULL AND a.ended_at IS NOT NULL AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date), 0)::numeric, 2))::text AS "onCourseHours"
		FROM golf.caddies c LEFT JOIN golf.caddy_levels l ON l.id = c.level_id WHERE c.archived_at IS NULL ORDER BY c.code`),
	sqlReport("golf.caddy_settlement", "Caddy Settlement Report", "golf", "Caddy fee settlements: fee, tips, deductions, total and payment status.",
		cols("settlementNo|Settlement", "caddy|Caddy", "periodStart|From|datetime", "periodEnd|To|datetime", "rounds|Rounds|number", "caddyFee|Caddy Fee|number",
			"tips|Tips|number", "deductions|Deductions|number", "total|Total|number", "status|Status"), nil, 31,
		`SELECT s.settlement_no AS "settlementNo", c.name AS "caddy", s.period_start AS "periodStart", s.period_end AS "periodEnd", s.rounds,
		trim_scale(s.caddy_fee)::text AS "caddyFee", trim_scale(s.tips)::text AS "tips", trim_scale(s.deductions)::text AS "deductions",
		trim_scale(s.total)::text AS "total", s.status FROM golf.caddy_settlements s JOIN golf.caddies c ON c.id = s.caddy_id
		WHERE s.period_end >= $1::date AND s.period_start <= $2::date AND $3::text <> '' ORDER BY s.period_start, c.code`),
	sqlReport("golf.cart_maintenance", "Golf Cart Maintenance Report", "golf", "Maintenance records with category, cost and release.",
		cols("maintenanceNo|Maintenance", "cart|Golf Cart", "category|Category", "description|Description", "cost|Cost|number", "status|Status",
			"openedAt|Opened|datetime", "closedAt|Closed|datetime", "usageHours|Usage Hours|number"), nil, 90,
		`SELECT m.maintenance_no AS "maintenanceNo", g.code AS "cart", m.category, m.description, trim_scale(m.cost)::text AS "cost", m.status,
		m.opened_at AS "openedAt", m.closed_at AS "closedAt", trim_scale(g.usage_hours)::text AS "usageHours"
		FROM golf.cart_maintenance m JOIN golf.golf_carts g ON g.id = m.cart_id WHERE (m.opened_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		ORDER BY m.opened_at`),
	sqlReport("golf.hole_in_one", "Hole-in-One Report", "golf", "Hole-in-One records, verification and insurance claims.",
		cols("hioNo|Record", "player|Player", "hole|Hole", "achievedOn|Date|datetime", "status|Status", "insured|Insured|boolean", "claimStatus|Claim",
			"claimPaid|Claim Paid|number"), nil, 365,
		`SELECT r.hio_no AS "hioNo", r.player_name AS "player", s.code || '-' || h.number AS "hole", r.achieved_on AS "achievedOn", r.status, r.insured,
		r.claim_status AS "claimStatus", trim_scale(r.claim_paid_amount)::text AS "claimPaid"
		FROM golf.hio_records r JOIN golf.holes h ON h.id = r.hole_id JOIN golf.course_sections s ON s.id = h.section_id
		WHERE r.achieved_on BETWEEN $1::date AND $2::date AND $3::text <> '' ORDER BY r.achieved_on`),
	sqlReport("golf.driving_range_usage", "Driving Range Usage Report", "golf", "Per day: sessions, buckets, balls by source and revenue.",
		cols("date|Date|datetime", "sessions|Sessions|number", "buckets|Buckets|number", "balls|Balls|number", "prepaidBalls|Prepaid Balls|number",
			"revenue|Revenue|number"), nil, 30,
		`SELECT d::date AS "date",
		(SELECT count(*) FROM golf.range_sessions s WHERE (s.queued_at AT TIME ZONE $3)::date = d::date)::int AS "sessions",
		(SELECT count(*) FROM golf.range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "buckets",
		(SELECT coalesce(sum(balls), 0) FROM golf.range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "balls",
		(SELECT coalesce(sum(balls), 0) FROM golf.range_buckets b WHERE b.source = 'prepaid' AND (b.created_at AT TIME ZONE $3)::date = d::date)::int AS "prepaidBalls",
		(SELECT trim_scale(coalesce(sum(amount), 0))::text FROM golf.range_buckets b WHERE (b.created_at AT TIME ZONE $3)::date = d::date) AS "revenue"
		FROM generate_series($1::date, $2::date, interval '1 day') d ORDER BY 1`),
	sqlReport("golf.reciprocal_visits", "Reciprocal Visit Report", "golf", "Inbound and outbound reciprocal visits, charges and settlement status.",
		cols("visitNo|Visit", "direction|Direction", "club|Club", "country|Country", "visitor|Visitor", "visitDate|Date|datetime", "verified|Verified|boolean",
			"settlement|Settlement", "charge|Charge|number"), []Param{{Key: "direction", Label: "Direction", Type: "enum", Enum: []string{"inbound", "outbound"}}}, 90,
		`SELECT v.visit_no AS "visitNo", v.direction, c.name AS "club", c.country, v.visitor_name AS "visitor", v.visit_date AS "visitDate", v.verified,
		v.settlement_status AS "settlement", trim_scale(coalesce((SELECT sum(l.total_amount) FROM golf.flight_players p JOIN billing.folio_lines l
		  ON l.folio_id = p.folio_id AND l.status = 'posted' WHERE p.reciprocal_visit_id = v.id), v.charge_amount))::text AS "charge"
		FROM golf.reciprocal_visits v JOIN golf.reciprocal_clubs c ON c.id = v.club_id
		WHERE v.visit_date BETWEEN $1::date AND $2::date AND $3::text <> '' AND ($4 = '' OR v.direction = $4) ORDER BY v.visit_date`),
	sqlReport("sportclub.court_utilization", "Court Utilization Report", "sportclub", "Bookings and booked hours per court against opening hours.",
		cols("court|Court", "facility|Facility", "bookings|Bookings|number", "bookedHours|Booked Hours|number", "availableHours|Available Hours|number",
			"utilization|Utilization|number"), nil, 30,
		`WITH t AS (SELECT extract(epoch FROM close_time - open_time) / 3600 AS h FROM reservation.resource_types WHERE code = 'sport_court'),
		b AS (SELECT l.resource_id, count(DISTINCT l.reservation_id) AS n, sum(extract(epoch FROM upper(l.period) - lower(l.period)) / 3600) AS hours
		  FROM reservation.reservation_lines l WHERE l.status IN ('confirmed', 'checked_in', 'completed')
		  AND (lower(l.period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY l.resource_id)
		SELECT c.name AS "court", f.name AS "facility", coalesce(b.n, 0)::int AS "bookings", trim_scale(round(coalesce(b.hours, 0)::numeric, 2))::text AS "bookedHours",
		trim_scale(round(((($2::date - $1::date) + 1) * (SELECT h FROM t))::numeric, 2))::text AS "availableHours",
		trim_scale(round((coalesce(b.hours, 0) / nullif((($2::date - $1::date) + 1) * (SELECT h FROM t), 0))::numeric, 4))::text AS "utilization"
		FROM sportclub.courts c JOIN sportclub.facilities f ON f.id = c.facility_id LEFT JOIN b ON b.resource_id = c.resource_id
		WHERE c.archived_at IS NULL ORDER BY f.name, c.name`),
	sqlReport("sportclub.class_attendance", "Class Attendance Report", "sportclub", "Per class session: capacity, booked, present, absent and excused.",
		cols("start|Session|datetime", "program|Class", "instructor|Instructor", "capacity|Capacity|number", "booked|Booked|number", "present|Present|number",
			"absent|Absent|number", "excused|Excused|number", "status|Status"), nil, 30,
		`SELECT lower(s.period) AS "start", p.name AS "program", i.name AS "instructor", s.capacity,
		(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status <> 'cancelled')::int AS "booked",
		(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'present')::int AS "present",
		(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'absent')::int AS "absent",
		(SELECT count(*) FROM sportclub.session_bookings b WHERE b.session_id = s.id AND b.status = 'excused')::int AS "excused", s.status
		FROM sportclub.class_sessions s JOIN sportclub.class_programs p ON p.id = s.program_id JOIN sportclub.instructors i ON i.id = s.instructor_id
		WHERE (lower(s.period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY lower(s.period)`),
	sqlReport("sportclub.instructor_fee", "Instructor Fee Report", "sportclub", "Instructor honorarium statements and payment status.",
		cols("feeNo|Statement", "instructor|Instructor", "periodStart|From|datetime", "periodEnd|To|datetime", "sessions|Sessions|number",
			"students|Students|number", "amount|Amount|number", "status|Status"), nil, 31,
		`SELECT f.fee_no AS "feeNo", i.name AS "instructor", f.period_start AS "periodStart", f.period_end AS "periodEnd", f.sessions, f.students,
		trim_scale(f.amount)::text AS "amount", f.status FROM sportclub.instructor_fees f JOIN sportclub.instructors i ON i.id = f.instructor_id
		WHERE f.period_end >= $1::date AND f.period_start <= $2::date AND $3::text <> '' ORDER BY f.period_start, i.name`),
	sqlReport("stay.bungalow_occupancy", "Bungalow Occupancy Report", "stay", "Per night: bungalows available, occupied and occupancy rate.",
		cols("date|Night|datetime", "units|Units|number", "occupied|Occupied|number", "occupancy|Occupancy|number"), nil, 30,
		`WITH u AS (SELECT count(*) AS n FROM stay.bungalows WHERE status = 'active' AND archived_at IS NULL)
		SELECT d::date AS "date", (SELECT n FROM u)::int AS "units",
		(SELECT count(DISTINCT s.unit_id) FROM stay.stays s WHERE s.kind = 'bungalow' AND s.status NOT IN ('cancelled', 'no_show', 'requested')
		  AND (s.start_at AT TIME ZONE $3)::date <= d::date AND (s.end_at AT TIME ZONE $3)::date > d::date)::int AS "occupied",
		trim_scale(round((SELECT count(DISTINCT s.unit_id) FROM stay.stays s WHERE s.kind = 'bungalow' AND s.status NOT IN ('cancelled', 'no_show', 'requested')
		  AND (s.start_at AT TIME ZONE $3)::date <= d::date AND (s.end_at AT TIME ZONE $3)::date > d::date)::numeric / nullif((SELECT n FROM u), 0), 4))::text AS "occupancy"
		FROM generate_series($1::date, $2::date, interval '1 day') d ORDER BY 1`),
	sqlReport("stay.meeting_room_booking", "Meeting Room Booking Report", "stay", "Meeting room bookings with package, layout and pax.",
		cols("stayNo|Booking", "room|Meeting Room", "start|Start|datetime", "end|End|datetime", "pax|Pax|number", "package|Package", "layout|Layout",
			"customer|Customer", "status|Status"), nil, 30,
		`SELECT s.stay_no AS "stayNo", coalesce(mr.name, '') AS "room", s.start_at AS "start", s.end_at AS "end", s.pax, s.package_code AS "package",
		s.layout, coalesce(c.name, s.guest_name, s.corporate_name) AS "customer", s.status
		FROM stay.stays s LEFT JOIN stay.meeting_rooms mr ON mr.id = s.unit_id LEFT JOIN crm.customers c ON c.id = s.customer_id
		WHERE s.kind = 'meeting_room' AND (s.start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY s.start_at`),
	sqlReport("commercial.pos_sales", "POS Sales Report", "commercial", "Per day and outlet: orders, gross, discount, net, service, tax and total.",
		cols("date|Date|datetime", "outlet|Outlet", "orders|Orders|number", "gross|Gross|number", "discount|Discount|number", "net|Net|number",
			"service|Service|number", "tax|Tax|number", "total|Total|number", "averageTransaction|Average Transaction|number"),
		[]Param{{Key: "outletId", Label: "Outlet", Type: "uuid"}}, 7,
		`SELECT (o.created_at AT TIME ZONE $3)::date AS "date", ou.name AS "outlet", count(DISTINCT o.id)::int AS "orders",
		trim_scale(sum(l.unit_price * l.quantity))::text AS "gross", trim_scale(sum(l.discount_amount))::text AS "discount",
		trim_scale(sum(l.net_amount))::text AS "net", trim_scale(sum(l.service_amount))::text AS "service", trim_scale(sum(l.tax_amount))::text AS "tax",
		trim_scale(sum(l.total_amount))::text AS "total", trim_scale(round(sum(l.total_amount) / nullif(count(DISTINCT o.id), 0), 2))::text AS "averageTransaction"
		FROM commercial.orders o JOIN commercial.outlets ou ON ou.id = o.outlet_id JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active'
		WHERE o.status IN ('paid', 'charged') AND (o.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR o.outlet_id::text = $4)
		GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("commercial.shift", "Shift Report", "commercial", "POS shifts: opening cash, expected, counted and variance.",
		cols("shiftNo|Shift", "outlet|Outlet", "cashier|Cashier", "openedAt|Opened|datetime", "closedAt|Closed|datetime", "openingCash|Opening Cash|number",
			"expectedCash|Expected Cash|number", "countedCash|Counted Cash|number", "variance|Variance|number", "status|Status"), nil, 7,
		`SELECT s.shift_no AS "shiftNo", o.name AS "outlet", u.full_name AS "cashier", s.opened_at AS "openedAt", s.closed_at AS "closedAt",
		trim_scale(s.opening_cash)::text AS "openingCash", trim_scale(s.expected_cash)::text AS "expectedCash", trim_scale(s.counted_cash)::text AS "countedCash",
		trim_scale(s.variance)::text AS "variance", s.status FROM commercial.pos_shifts s JOIN commercial.outlets o ON o.id = s.outlet_id
		JOIN platform.users u ON u.id = s.cashier_id WHERE (s.opened_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date ORDER BY s.opened_at`),
	sqlReport("commercial.voucher_liability", "Voucher Liability Report", "commercial",
		"Outstanding voucher & prepaid liability per voucher type at the end of the To date (equals the deferred revenue sub-ledger).",
		cols("voucherType|Voucher Type", "liabilityType|Liability", "vouchers|Vouchers|number", "sold|Deferred|number", "recognized|Recognised|number",
			"breakage|Breakage|number", "liability|Liability|number"), nil, 0,
		`SELECT t.name AS "voucherType", e.liability_type AS "liabilityType", count(DISTINCT v.id)::int AS "vouchers",
		trim_scale(coalesce(sum(e.amount) FILTER (WHERE e.entry_type = 'deferral'), 0))::text AS "sold",
		trim_scale(-coalesce(sum(e.amount) FILTER (WHERE e.entry_type = 'recognition'), 0))::text AS "recognized",
		trim_scale(-coalesce(sum(e.amount) FILTER (WHERE e.entry_type = 'breakage'), 0))::text AS "breakage",
		trim_scale(sum(e.amount))::text AS "liability"
		FROM billing.deferred_revenue_entries e JOIN commercial.vouchers v ON v.id = e.ref_id JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE e.ref_type = 'commercial.voucher' AND e.liability_type IN ('voucher', 'prepaid')
		AND e.occurred_at < (($2::date + 1)::timestamp AT TIME ZONE $3) AND $1::date IS NOT NULL
		GROUP BY 1, 2 ORDER BY 1`),
	sqlReport("commercial.voucher_breakage", "Voucher Breakage Report", "commercial", "Expired voucher balances recognised as breakage in the period.",
		cols("voucherType|Voucher Type", "vouchers|Vouchers|number", "breakage|Breakage|number"), nil, 30,
		`SELECT t.name AS "voucherType", count(DISTINCT v.id)::int AS "vouchers", trim_scale(-sum(e.amount))::text AS "breakage"
		FROM billing.deferred_revenue_entries e JOIN commercial.vouchers v ON v.id = e.ref_id JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE e.ref_type = 'commercial.voucher' AND e.entry_type = 'breakage' AND (e.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date
		GROUP BY 1 ORDER BY 1`),
	sqlReport("commercial.food_cost", "Food Cost Report", "commercial", "Theoretical food cost per outlet and product (BOM).",
		cols("outlet|Outlet", "product|Product", "quantity|Quantity|number", "netSales|Net Sales|number", "cost|Theoretical Cost|number",
			"foodCostPercent|Food Cost %|number", "hasRecipe|Recipe|boolean"), []Param{{Key: "outletId", Label: "Outlet", Type: "uuid"}}, 30,
		`SELECT coalesce(o.name, '-') AS "outlet", p.name AS "product", trim_scale(sum(s.quantity))::text AS "quantity", trim_scale(sum(s.net_amount))::text AS "netSales",
		trim_scale(sum(s.cost_amount))::text AS "cost", trim_scale(round(sum(s.cost_amount) * 100 / nullif(sum(s.net_amount), 0), 2))::text AS "foodCostPercent",
		bool_and(s.has_recipe) AS "hasRecipe"
		FROM inventory.consumption_sales s JOIN commercial.products p ON p.id = s.product_id LEFT JOIN commercial.outlets o ON o.id = s.outlet_id
		WHERE (s.occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date AND ($4 = '' OR s.outlet_id::text = $4)
		GROUP BY 1, 2 ORDER BY 1, 2`),
	sqlReport("membership.lifecycle", "Membership Lifecycle Report", "membership",
		"Per program and type: memberships by status, new, expiring in 30 days and lifecycle events in the period.",
		cols("program|Program", "type|Membership Type", "active|Active|number", "paused|Paused|number", "suspended|Suspended|number", "expired|Expired|number",
			"cancelled|Cancelled|number", "new|New|number", "expiring30|Expiring (30 days)|number", "pauses|Pauses|number", "renewals|Renewals|number"), nil, 30,
		`SELECT p.name AS "program", t.name AS "type",
		count(*) FILTER (WHERE m.status = 'active')::int AS "active", count(*) FILTER (WHERE m.status = 'paused')::int AS "paused",
		count(*) FILTER (WHERE m.status = 'suspended')::int AS "suspended", count(*) FILTER (WHERE m.status = 'expired')::int AS "expired",
		count(*) FILTER (WHERE m.status = 'cancelled')::int AS "cancelled",
		count(*) FILTER (WHERE m.start_date BETWEEN $1::date AND $2::date)::int AS "new",
		count(*) FILTER (WHERE m.status = 'active' AND m.end_date BETWEEN current_date AND current_date + 30)::int AS "expiring30",
		(SELECT count(*) FROM membership.membership_events e JOIN membership.memberships m2 ON m2.id = e.membership_id WHERE m2.type_id = t.id
		  AND e.event_type = 'paused' AND (e.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::int AS "pauses",
		(SELECT count(*) FROM membership.membership_events e JOIN membership.memberships m2 ON m2.id = e.membership_id WHERE m2.type_id = t.id
		  AND e.event_type LIKE 'renew%' AND (e.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::int AS "renewals"
		FROM membership.memberships m JOIN membership.types t ON t.id = m.type_id JOIN membership.programs p ON p.id = m.program_id
		GROUP BY p.name, t.id, t.name ORDER BY 1, 2`),
}
