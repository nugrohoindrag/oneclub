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
	Unit       string      `json:"unit" enum:"count,idr,ratio,hours,balls,points,days"`
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

// Dashboards read the reporting views only: $1 from date, $2 to date
// (inclusive), $3 timezone.
var dashboards = map[string]dashDef{
	"golf-performance": {name: "Golf Performance", kpis: []kpiDef{
		{key: "golf_rounds", label: "Golf Rounds", unit: "count", sql: `SELECT count(*)::text FROM reporting.golf_rounds
			WHERE play_date BETWEEN $1::date AND $2::date AND $3::text <> ''`},
		{key: "tee_time_utilization", label: "Tee Time Utilization", unit: "ratio", def: "Players booked ÷ player capacity of the open tee times",
			sql: `SELECT trim_scale(round(coalesce((SELECT count(*) FROM reporting.golf_players WHERE play_date BETWEEN $1::date AND $2::date)::numeric
			  / nullif((SELECT sum(capacity) FROM reporting.golf_tee_times WHERE status = 'open' AND play_date BETWEEN $1::date AND $2::date), 0), 0), 4))::text
			  WHERE $3::text <> ''`},
		{key: "revpatt", label: "Revenue per Available Tee Time", unit: "idr", def: "Golf revenue ÷ open tee times",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(total) FROM reporting.revenue_lines WHERE business_line = 'golf' AND NOT liability
			  AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)
			  / nullif((SELECT count(*) FROM reporting.golf_tee_times WHERE status = 'open' AND play_date BETWEEN $1::date AND $2::date), 0), 0), 2))::text`},
		{key: "member_vs_guest", label: "Member vs Guest", unit: "count", sql: `SELECT count(*) FILTER (WHERE player_type = 'member')::text FROM reporting.golf_rounds
			WHERE play_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
			breakdown: `SELECT player_type AS label, count(*)::text AS value FROM reporting.golf_rounds
			WHERE play_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
		{key: "rounds_per_member", label: "Rounds per Member", unit: "ratio", def: "Member rounds ÷ active golf members",
			sql: `SELECT trim_scale(round(coalesce((SELECT count(*) FROM reporting.golf_rounds WHERE player_type = 'member'
			  AND play_date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(DISTINCT customer_id) FROM reporting.membership_lifecycle WHERE program_kind = 'golf' AND status = 'active'), 0), 0), 2))::text
			  WHERE $3::text <> ''`},
		{key: "caddy_utilization", label: "Caddy Utilization", unit: "ratio", def: "On-course hours ÷ duty hours",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(extract(epoch FROM finished_at - started_at)) / 3600 FROM reporting.golf_caddy_assignments
			  WHERE finished_at IS NOT NULL AND started_at IS NOT NULL AND play_date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT sum(duty_hours) FROM reporting.golf_caddy_duty WHERE work_date BETWEEN $1::date AND $2::date)::numeric, 0), 0), 4))::text
			  WHERE $3::text <> ''`},
		{key: "golf_cart_utilization", label: "Golf Cart Utilization", unit: "ratio", def: "Out hours ÷ (active carts × tee sheet hours)",
			sql: `WITH h AS (SELECT coalesce(extract(epoch FROM max(start_at) - min(start_at)) / 3600, 0) + 5 AS hours, count(DISTINCT play_date) AS days
			  FROM reporting.golf_tee_times WHERE play_date BETWEEN $1::date AND $2::date)
			SELECT trim_scale(round(coalesce((SELECT sum(minutes_out) / 60 FROM reporting.golf_cart_usage WHERE play_date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(*) FROM reporting.golf_carts WHERE status = 'active') * (SELECT days FROM h) * (SELECT hours FROM h)
			  / greatest((SELECT days FROM h), 1), 0)::numeric, 0), 4))::text WHERE $3::text <> ''`},
		{key: "driving_range_usage", label: "Driving Range Usage", unit: "balls", sql: `SELECT coalesce(sum(balls), 0)::text FROM reporting.golf_range_buckets
			WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "golf_revenue", label: "Golf Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.revenue_lines
			WHERE business_line = 'golf' AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT revenue_component AS label, trim_scale(sum(total))::text AS value FROM reporting.revenue_lines WHERE business_line = 'golf'
			AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
	}},
	"sport-club-performance": {name: "Sport Club Performance", kpis: []kpiDef{
		{key: "court_utilization", label: "Court Utilization", unit: "ratio", def: "Booked court hours ÷ court opening hours",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(extract(epoch FROM upper(period) - lower(period))) / 3600 FROM reporting.reservation_lines
			  WHERE resource_type = 'sport_court' AND status IN ('confirmed', 'checked_in', 'completed') AND (lower(period) AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date)::numeric /
			  nullif((SELECT count(*) FROM reporting.sport_courts WHERE status = 'active') * (($2::date - $1::date) + 1) *
			  (SELECT extract(epoch FROM close_time - open_time) / 3600 FROM reporting.opening_hours WHERE resource_type = 'sport_court'), 0)::numeric, 0), 4))::text`},
		{key: "entries", label: "Entries", unit: "count", sql: `SELECT coalesce(sum(adults + children), 0)::text FROM reporting.sport_entries WHERE status <> 'cancelled'
			AND visit_date BETWEEN $1::date AND $2::date AND $3::text <> ''`,
			breakdown: `SELECT entry_type AS label, coalesce(sum(adults + children), 0)::text AS value FROM reporting.sport_entries WHERE status <> 'cancelled'
			AND visit_date BETWEEN $1::date AND $2::date AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
		{key: "class_enrollment", label: "Class Enrollment", unit: "count", sql: `SELECT count(*)::text FROM reporting.sport_enrollments WHERE status = 'active'
			AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`},
		{key: "class_attendance", label: "Class Attendance", unit: "ratio", def: "Present ÷ marked bookings",
			sql: `SELECT trim_scale(round(coalesce(sum(present)::numeric / nullif(sum(present + absent + excused), 0), 0), 4))::text
			FROM reporting.sport_class_sessions WHERE (starts_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "sport_revenue", label: "Sport Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.revenue_lines
			WHERE business_line = 'sportclub' AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"membership-performance": {name: "Membership Performance", kpis: []kpiDef{
		{key: "active_members", label: "Active Members", unit: "count", sql: `SELECT count(*)::text FROM reporting.membership_lifecycle WHERE status = 'active'
			AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> ''`,
			breakdown: `SELECT program_name AS label, count(*)::text AS value FROM reporting.membership_lifecycle
			WHERE status = 'active' AND $1::date IS NOT NULL AND $2::date IS NOT NULL AND $3::text <> '' GROUP BY 1 ORDER BY 1`},
		{key: "new_members", label: "New Members", unit: "count", sql: `SELECT count(*)::text FROM reporting.membership_lifecycle
			WHERE starts_on BETWEEN $1::date AND $2::date AND $3::text <> ''`},
		{key: "expiring_membership", label: "Expiring Membership", unit: "count", def: "Active memberships ending within 30 days after the To date",
			sql: `SELECT count(*)::text FROM reporting.membership_lifecycle WHERE status = 'active' AND ends_on BETWEEN $2::date AND $2::date + 30
			AND $1::date IS NOT NULL AND $3::text <> ''`},
		{key: "renewal", label: "Renewal", unit: "count", sql: `SELECT count(*)::text FROM reporting.membership_events WHERE event = 'renewed'
			AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "member_activity", label: "Member Activity", unit: "count", def: "Members with at least one charged visit",
			sql: `SELECT count(DISTINCT r.customer_id)::text FROM reporting.revenue_lines r
			WHERE r.customer_id IN (SELECT customer_id FROM reporting.membership_lifecycle WHERE status = 'active')
			AND (r.posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "member_revenue", label: "Member Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.revenue_lines
			WHERE business_line = 'membership' AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"booking-performance": {name: "Booking Performance", kpis: []kpiDef{
		{key: "bookings", label: "Bookings", unit: "count", sql: `SELECT count(*)::text FROM reporting.reservations WHERE kind = 'booking' AND status <> 'expired'
			AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT business_line AS label, count(*)::text AS value FROM reporting.reservations WHERE kind = 'booking' AND status <> 'expired'
			AND (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
		{key: "bungalow_occupancy", label: "Bungalow Occupancy", unit: "ratio", def: "Bungalow nights sold ÷ nights available",
			sql: `SELECT trim_scale(round(coalesce((SELECT sum(greatest(0, least((s.end_at AT TIME ZONE $3)::date, $2::date + 1) - greatest((s.start_at AT TIME ZONE $3)::date, $1::date)))
			  FROM reporting.stays s WHERE s.kind = 'bungalow' AND s.status NOT IN ('cancelled', 'no_show', 'requested'))::numeric /
			  nullif((SELECT count(*) FROM reporting.stay_bungalows WHERE status = 'active') * (($2::date - $1::date) + 1), 0), 0), 4))::text`},
		{key: "meeting_room_booking", label: "Meeting Room Booking", unit: "count", sql: `SELECT count(*)::text FROM reporting.stays WHERE kind = 'meeting_room'
			AND status NOT IN ('cancelled', 'requested') AND (start_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "cancellation", label: "Cancellation", unit: "count", sql: `SELECT count(*)::text FROM reporting.reservations WHERE status = 'cancelled'
			AND (cancelled_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "no_show", label: "No-show", unit: "count", sql: `SELECT count(*)::text FROM reporting.reservations WHERE status = 'no_show'
			AND (updated_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "booking_revenue", label: "Booking Revenue", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total), 0))::text FROM reporting.revenue_lines
			WHERE reservation_id IS NOT NULL AND NOT liability AND (posted_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
	}},
	"commercial-performance": {name: "Commercial Performance", kpis: []kpiDef{
		{key: "pos_sales", label: "POS Sales", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(total_amount), 0))::text FROM reporting.pos_sales
			WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`,
			breakdown: `SELECT outlet_name AS label, trim_scale(coalesce(sum(total_amount), 0))::text AS value FROM reporting.pos_sales
			WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date GROUP BY 1 ORDER BY 1`},
		{key: "average_transaction", label: "Average Transaction", unit: "idr", sql: `SELECT trim_scale(round(coalesce(sum(total_amount) / nullif(count(DISTINCT order_id), 0), 0), 2))::text
			FROM reporting.pos_sales WHERE (created_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "voucher_sold", label: "Voucher Sold", unit: "idr", sql: `SELECT trim_scale(coalesce(sum(amount), 0))::text FROM reporting.voucher_ledger
			WHERE entry_type = 'deferral' AND liability_type IN ('voucher', 'prepaid') AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "voucher_redeemed", label: "Voucher Redeemed", unit: "idr", sql: `SELECT trim_scale(-coalesce(sum(amount), 0))::text FROM reporting.voucher_ledger
			WHERE entry_type = 'recognition' AND liability_type IN ('voucher', 'prepaid') AND (occurred_at AT TIME ZONE $3)::date BETWEEN $1::date AND $2::date`},
		{key: "food_cost_percent", label: "Food Cost %", unit: "ratio", def: "Theoretical cost (BOM) ÷ net sales of products with a recipe",
			sql: `SELECT trim_scale(round(coalesce(sum(cost_amount) / nullif(sum(net_amount) FILTER (WHERE has_recipe), 0), 0), 4))::text FROM reporting.food_cost
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
