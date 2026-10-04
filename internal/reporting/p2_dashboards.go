package reporting

// P2 KPI dashboards (PRD P2 FR-RPT-P2-01..05, Naming Convention §23):
// Golf, Sport Club, Membership, Booking and Commercial Performance for a
// date range, read from the reporting replica.

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
)

// KPI is one dashboard figure.
type KPI struct {
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Value      string      `json:"value" doc:"Decimal as text (counts, money, ratios 0–1)"`
	Unit       string      `json:"unit" enum:"count,idr,ratio,hours,balls"`
	Definition string      `json:"definition,omitempty"`
	Breakdown  []Breakdown `json:"breakdown,omitempty"`
}

type Breakdown struct {
	Label string `json:"label" db:"label"`
	Value string `json:"value" db:"value"`
}

// PerformanceDashboard is a P2 KPI dashboard.
type PerformanceDashboard struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	KPIs        []KPI     `json:"kpis"`
	GeneratedAt time.Time `json:"generatedAt"`
}

type kpiDef struct {
	key, label, unit, def, sql string
	breakdown                  string // optional: SELECT label, value
}

type dashDef struct {
	name string
	kpis []kpiDef
}

// Dashboards: $1 from date, $2 to date (inclusive), $3 timezone.
var dashboards = map[string]dashDef{
	"golf-performance": {name: "Golf Performance", kpis: []kpiDef{
		{key: "golf_rounds", label: "Golf Rounds", unit: "count", sql: `SELECT count(*)::text FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id
			WHERE p.status = 'finished' AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "tee_time_utilization", label: "Tee Time Utilization", unit: "ratio", def: "Players booked ÷ player capacity of the available tee times",
			sql: `WITH t AS (SELECT (extract(epoch FROM close_time - open_time) / 60 / slot_minutes)::int AS slots FROM reservation.resource_types WHERE code = 'tee_time'),
			r AS (SELECT count(*) AS routes FROM golf.playing_routes WHERE status = 'active' AND archived_at IS NULL)
			SELECT trim_scale(round(coalesce((SELECT count(*) FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id WHERE p.status NOT IN ('cancelled')
			  AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric
			  / nullif((($2::date - $1::date) + 1) * (SELECT slots FROM t) * (SELECT routes FROM r) * 4, 0), 0), 4))::text`},
		{key: "revpatt", label: "Revenue per Available Tee Time", unit: "idr", def: "Golf revenue ÷ tee times available",
			sql: `WITH t AS (SELECT (extract(epoch FROM close_time - open_time) / 60 / slot_minutes)::int AS slots FROM reservation.resource_types WHERE code = 'tee_time'),
			r AS (SELECT count(*) AS routes FROM golf.playing_routes WHERE status = 'active' AND archived_at IS NULL)
			SELECT trim_scale(round(coalesce((SELECT sum(total_amount) FROM billing.folio_lines WHERE business_line = 'golf' AND status = 'posted' AND NOT liability
			  AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date) / nullif((($2::date - $1::date) + 1) * (SELECT slots FROM t) * (SELECT routes FROM r), 0), 0), 2))::text`},
		{key: "member_vs_guest", label: "Member vs Guest", unit: "count", sql: `SELECT count(*) FILTER (WHERE p.player_type = 'member')::text FROM golf.flight_players p
			JOIN golf.flights f ON f.id = p.flight_id WHERE p.status = 'finished' AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT p.player_type AS label, count(*)::text AS value FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id
			WHERE p.status = 'finished' AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
		{key: "rounds_per_member", label: "Rounds per Member", unit: "ratio", def: "Member rounds ÷ active golf members",
			sql: `SELECT trim_scale(round(coalesce((SELECT count(*) FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id WHERE p.status = 'finished'
			  AND p.player_type = 'member' AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(DISTINCT mm.customer_id) FROM membership.membership_members mm JOIN membership.memberships m ON m.id = mm.membership_id
			  JOIN membership.programs pr ON pr.id = m.program_id WHERE pr.program_kind = 'golf' AND m.status = 'active' AND mm.status = 'active'), 0), 0), 2))::text`},
		{key: "caddy_utilization", label: "Caddy Utilization", unit: "ratio", def: "On-course hours ÷ duty hours",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(extract(epoch FROM a.ended_at - a.started_at)) FROM golf.caddy_assignments a JOIN golf.flights f ON f.id = a.flight_id
			  WHERE a.ended_at IS NOT NULL AND a.started_at IS NOT NULL AND (f.tee_time AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT sum(extract(epoch FROM coalesce(clock_out, now()) - clock_in)) FROM golf.caddy_attendance WHERE work_date BETWEEN $1::date AND $2::date)::numeric, 0), 0), 4))::text`},
		{key: "golf_cart_utilization", label: "Golf Cart Utilization", unit: "ratio", def: "In-use hours ÷ (active carts × tee sheet hours)",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(extract(epoch FROM coalesce(a.returned_at, now()) - a.out_at)) / 3600 FROM golf.cart_assignments a
			  WHERE a.out_at IS NOT NULL AND (a.out_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(*) FROM golf.golf_carts WHERE status = 'active' AND archived_at IS NULL) * (($2::date - $1::date) + 1) *
			  (SELECT extract(epoch FROM close_time - open_time) / 3600 FROM reservation.resource_types WHERE code = 'tee_time'), 0)::numeric, 0), 4))::text`},
		{key: "driving_range_usage", label: "Driving Range Usage", unit: "balls", sql: `SELECT coalesce(sum(balls), 0)::text FROM golf.range_buckets
			WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "golf_revenue", label: "Golf Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total_amount), 0))::text FROM billing.folio_lines
			WHERE business_line = 'golf' AND status = 'posted' AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT revenue_component AS label, trim_scale(sum(total_amount))::text AS value FROM billing.folio_lines WHERE business_line = 'golf'
			AND status = 'posted' AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	}},
	"sport-club-performance": {name: "Sport Club Performance", kpis: []kpiDef{
		{key: "court_utilization", label: "Court Utilization", unit: "ratio", def: "Booked court hours ÷ court opening hours",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(extract(epoch FROM upper(l.period) - lower(l.period))) / 3600 FROM reservation.reservation_lines l
			  WHERE l.resource_type = 'sport_court' AND l.status IN ('confirmed', 'checked_in', 'completed') AND (lower(l.period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(*) FROM sportclub.courts WHERE status = 'active' AND archived_at IS NULL) * (($2::date - $1::date) + 1) *
			  (SELECT extract(epoch FROM close_time - open_time) / 3600 FROM reservation.resource_types WHERE code = 'sport_court'), 0)::numeric, 0), 4))::text`},
		{key: "entries", label: "Entries", unit: "count", sql: `SELECT coalesce(sum(adults + children), 0)::text FROM sportclub.entries WHERE status <> 'cancelled'
			AND visit_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
			breakdown: `SELECT entry_type AS label, coalesce(sum(adults + children), 0)::text AS value FROM sportclub.entries WHERE status <> 'cancelled'
			AND visit_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
		{key: "class_enrollment", label: "Class Enrollment", unit: "count", sql: `SELECT count(*)::text FROM sportclub.enrollments WHERE status = 'active'
			AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
		{key: "class_attendance", label: "Class Attendance", unit: "ratio", def: "Present ÷ marked bookings",
			sql: `SELECT trim_scale(round(coalesce(count(*) FILTER (WHERE b.status = 'present')::numeric / nullif(count(*) FILTER (WHERE b.status IN ('present', 'absent', 'excused')), 0), 0), 4))::text
			FROM sportclub.session_bookings b JOIN sportclub.class_sessions s ON s.id = b.session_id WHERE (lower(s.period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "sport_revenue", label: "Sport Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total_amount), 0))::text FROM billing.folio_lines
			WHERE business_line = 'sportclub' AND status = 'posted' AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"membership-performance": {name: "Membership Performance", kpis: []kpiDef{
		{key: "active_members", label: "Active Members", unit: "count", sql: `SELECT count(*)::text FROM membership.memberships WHERE status = 'active'
			AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
			breakdown: `SELECT p.name AS label, count(*)::text AS value FROM membership.memberships m JOIN membership.programs p ON p.id = m.program_id
			WHERE m.status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
		{key: "new_members", label: "New Members", unit: "count", sql: `SELECT count(*)::text FROM membership.memberships WHERE start_date BETWEEN $1::date AND $2::date AND $3::text <> ''`},
		{key: "expiring_membership", label: "Expiring Membership", unit: "count", def: "Active memberships ending within 30 days after the To date",
			sql: `SELECT count(*)::text FROM membership.memberships WHERE status = 'active' AND end_date BETWEEN $2::date AND $2::date + 30 AND $1::date IS NOT NULL AND $3::text <> ''`},
		{key: "renewal", label: "Renewal", unit: "count", sql: `SELECT count(*)::text FROM membership.membership_events WHERE event_type LIKE 'renew%'
			AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "member_activity", label: "Member Activity", unit: "count", def: "Members with at least one charged visit",
			sql: `SELECT count(DISTINCT f.customer_id)::text FROM billing.folios f JOIN membership.membership_members mm ON mm.customer_id = f.customer_id AND mm.status = 'active'
			WHERE (f.opened_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "member_revenue", label: "Member Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total_amount), 0))::text FROM billing.folio_lines
			WHERE business_line = 'membership' AND status = 'posted' AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"booking-performance": {name: "Booking Performance", kpis: []kpiDef{
		{key: "bookings", label: "Bookings", unit: "count", sql: `SELECT count(*)::text FROM reservation.reservations WHERE kind = 'booking' AND status <> 'expired'
			AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT business_line AS label, count(*)::text AS value FROM reservation.reservations WHERE kind = 'booking' AND status <> 'expired'
			AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
		{key: "bungalow_occupancy", label: "Bungalow Occupancy", unit: "ratio", def: "Bungalow nights sold ÷ nights available",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(greatest(0, least((s.end_at AT TIME ZONE $3)::date, $2::date + 1) - greatest((s.start_at AT TIME ZONE $3)::date, $1::date)))
			  FROM stay.stays s WHERE s.kind = 'bungalow' AND s.status NOT IN ('cancelled', 'no_show', 'requested'))::numeric /
			  nullif((SELECT count(*) FROM stay.bungalows WHERE status = 'active' AND archived_at IS NULL) * (($2::date - $1::date) + 1), 0), 0), 4))::text`},
		{key: "meeting_room_booking", label: "Meeting Room Booking", unit: "count", sql: `SELECT count(*)::text FROM stay.stays WHERE kind = 'meeting_room'
			AND status NOT IN ('cancelled', 'requested') AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "cancellation", label: "Cancellation", unit: "count", sql: `SELECT count(*)::text FROM reservation.reservations WHERE status = 'cancelled'
			AND (cancelled_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "no_show", label: "No-show", unit: "count", sql: `SELECT count(*)::text FROM reservation.reservations WHERE status = 'no_show'
			AND (updated_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "booking_revenue", label: "Booking Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(l.total_amount), 0))::text FROM billing.folio_lines l
			JOIN billing.folios f ON f.id = l.folio_id WHERE f.reservation_id IS NOT NULL AND l.status = 'posted' AND NOT l.liability
			AND (l.posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"commercial-performance": {name: "Commercial Performance", kpis: []kpiDef{
		{key: "pos_sales", label: "POS Sales", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(l.total_amount), 0))::text FROM commercial.orders o
			JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' WHERE o.status IN ('paid', 'charged') AND (o.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT ou.name AS label, trim_scale(coalesce(sum(l.total_amount), 0))::text AS value FROM commercial.orders o JOIN commercial.outlets ou ON ou.id = o.outlet_id
			JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' WHERE o.status IN ('paid', 'charged')
			AND (o.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
		{key: "average_transaction", label: "Average Transaction", unit: "idr", sql: `SELECT trim_scale(round(coalesce(sum(l.total_amount) / nullif(count(DISTINCT o.id), 0), 0), 2))::text
			FROM commercial.orders o JOIN commercial.order_lines l ON l.order_id = o.id AND l.status = 'active' WHERE o.status IN ('paid', 'charged')
			AND (o.created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "voucher_sold", label: "Voucher Sold", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM billing.deferred_revenue_entries
			WHERE entry_type = 'deferral' AND liability_type IN ('voucher', 'prepaid') AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "voucher_redeemed", label: "Voucher Redeemed", unit: "idr", sql: `SELECT trim_scale(-coalesce(sum(amount), 0))::text FROM billing.deferred_revenue_entries
			WHERE entry_type = 'recognition' AND liability_type IN ('voucher', 'prepaid') AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "food_cost_percent", label: "Food Cost %", unit: "ratio", def: "Theoretical cost (BOM) ÷ net sales of products with a recipe",
			sql: `SELECT trim_scale(round(coalesce(sum(cost_amount) / nullif(sum(net_amount) FILTER (WHERE has_recipe), 0), 0), 4))::text FROM inventory.consumption_sales
			WHERE (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
}

// DashboardCodes lists the P2 dashboards.
var DashboardCodes = []string{"golf-performance", "sport-club-performance", "membership-performance", "booking-performance", "commercial-performance"}

func (s *Service) performance(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	def, ok := dashboards[code]
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("dashboard"))
		return
	}
	ctx := r.Context()
	if _, ok := reqctx.Property(ctx); !ok {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "select a property"))
		return
	}
	q := map[string]string{"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to")}
	out := PerformanceDashboard{Code: code, Name: def.name, KPIs: []KPI{}, GeneratedAt: time.Now().UTC()}
	err := s.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
		from, to, tz := period(ctx, tx, q, 30)
		out.From, out.To = from, to
		for _, k := range def.kpis {
			v, err := kpi(ctx, tx, k, from, to, tz)
			if err != nil {
				return err
			}
			out.KPIs = append(out.KPIs, v)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func kpi(ctx context.Context, tx pgx.Tx, k kpiDef, from, to, tz string) (KPI, error) {
	out := KPI{Key: k.key, Label: k.label, Unit: k.unit, Definition: k.def}
	var v *string
	if err := tx.QueryRow(ctx, k.sql, from, to, tz).Scan(&v); err != nil {
		return out, err
	}
	out.Value = "0"
	if v != nil {
		out.Value = *v
	}
	if k.breakdown != "" {
		rows, err := tx.Query(ctx, k.breakdown, from, to, tz)
		if err != nil {
			return out, err
		}
		bd, err := pgx.CollectRows(rows, pgx.RowToStructByName[Breakdown])
		if err != nil {
			return out, err
		}
		out.Breakdown = bd
	}
	return out, nil
}

func (s *Service) registerPerformance(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/dashboards/{code}", Module: "reporting", Tag: "Reports", Scope: route.ScopeProperty,
		Summary:    "KPI dashboard: golf-performance, sport-club-performance, membership-performance, booking-performance, commercial-performance",
		Permission: "reporting.dashboard.view", Response: PerformanceDashboard{}, Query: []route.Param{{Name: "from"}, {Name: "to"}}, Handler: s.performance})
}
