package analytics

// CRM Dashboard Overview: the customer / member relationship at a glance —
// who the members are, how active and engaged they are, what the
// relationship is worth and who needs a follow-up. Golf operations and
// revenue stay on their own dashboards. Cross-domain data comes from the
// reporting read models (Technical Doc §4.2); scores from the RFM store.

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/platform/handle"
)

// Segment thresholds of the dashboard (overridable per request).
const (
	DefaultActiveDays     = 90  // Active: an activity within the last 90 days
	DefaultOccasionalDays = 180 // Occasional: the last activity 90–180 days ago; Inactive: older or none
	DefaultExpiringDays   = 30  // Expiring: the membership ends within 30 days
)

// DashCount is one bar or slice of a dashboard breakdown.
type DashCount struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

// DashGrowth is one month of the member growth.
type DashGrowth struct {
	Month     string `json:"month" doc:"YYYY-MM"`
	Members   int64  `json:"members" doc:"Members at the end of the month"`
	New       int64  `json:"new"`
	Renewed   int64  `json:"renewed"`
	Expired   int64  `json:"expired"`
	Cancelled int64  `json:"cancelled"`
	Net       int64  `json:"net" doc:"New − expired − cancelled"`
}

// DashKPIs are the headline figures.
type DashKPIs struct {
	TotalMembers    int64    `json:"totalMembers" doc:"Active members"`
	NewMembers      int64    `json:"newMembers" doc:"Joined in the period"`
	NewMembersPrev  int64    `json:"newMembersPrev" doc:"Joined in the previous period of the same length"`
	ActiveMembers   int64    `json:"activeMembers" doc:"Active members with an activity in the period"`
	InactiveMembers int64    `json:"inactiveMembers" doc:"Active members without an activity in the period"`
	Expiring        int64    `json:"expiring" doc:"Active memberships ending within the expiring threshold"`
	NonMembers      int64    `json:"nonMembers" doc:"Non-member customers who transacted in the period"`
	Retention       *float64 `json:"retention" doc:"Renewed ÷ (renewed + expired + cancelled) over the last 12 months"`
	AvgCLV          float64  `json:"avgClv" doc:"Average lifetime value of active members (RFM store)"`
}

// DashMember is a member in a dashboard list (high value, at risk).
type DashMember struct {
	CustomerID  uuid.UUID  `json:"customerId"`
	Code        string     `json:"code"`
	Name        string     `json:"name"`
	TypeName    string     `json:"typeName"`
	Visits      int64      `json:"visits" doc:"Visit days (RFM frequency)"`
	Value       float64    `json:"value" doc:"Spend (RFM monetary)"`
	CLV         float64    `json:"clv"`
	LastVisit   *time.Time `json:"lastVisit"`
	RecencyDays *int64     `json:"recencyDays"`
	EndsOn      *time.Time `json:"endsOn"`
	Reason      string     `json:"reason,omitempty" enum:"no_visit,declining,expiring"`
	Action      string     `json:"action,omitempty"`
}

// DashReferrer is a member who brings guests.
type DashReferrer struct {
	CustomerID uuid.UUID `json:"customerId"`
	Name       string    `json:"name"`
	Guests     int64     `json:"guests"`
}

// DashFollowUp is an open follow-up of the current user.
type DashFollowUp struct {
	ID           uuid.UUID  `json:"id"`
	Type         string     `json:"type"`
	Subject      string     `json:"subject"`
	CustomerID   *uuid.UUID `json:"customerId"`
	CustomerName *string    `json:"customerName"`
	DueAt        *time.Time `json:"dueAt"`
}

// CRMDashboard is the CRM Dashboard Overview.
type CRMDashboard struct {
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Today      time.Time `json:"today"`
	Thresholds struct {
		ActiveDays     int `json:"activeDays"`
		OccasionalDays int `json:"occasionalDays"`
		ExpiringDays   int `json:"expiringDays"`
	} `json:"thresholds"`
	Types      []string     `json:"types" doc:"Membership types (filter options)"`
	KPIs       DashKPIs     `json:"kpis"`
	Growth     []DashGrowth `json:"growth" doc:"Last 12 months up to the period end"`
	Segments   []DashCount  `json:"segments" doc:"Active members: expiring, active, occasional, inactive"`
	Activity   []DashCount  `json:"activity" doc:"Member activities in the period per kind"`
	Engagement []DashCount  `json:"engagement" doc:"Active members per RFM group (no_score when not scored yet)"`
	ByType     []DashCount  `json:"byType"`
	ByStatus   []DashCount  `json:"byStatus"`
	Renewal    struct {
		Due     int64 `json:"due"`
		Renewed int64 `json:"renewed"`
		Pending int64 `json:"pending"`
		Expired int64 `json:"expired"`
	} `json:"renewal" doc:"This month"`
	Expiry struct {
		D30 int64 `json:"d30"`
		D60 int64 `json:"d60"`
		D90 int64 `json:"d90"`
	} `json:"expiry"`
	Acquisition struct {
		NewCustomers int64       `json:"newCustomers"`
		Sources      []DashCount `json:"sources" doc:"Lead source of the new customers; direct when they came without a lead"`
	} `json:"acquisition"`
	Guests struct {
		Visits       int64          `json:"visits"`
		Unique       int64          `json:"unique"`
		Repeat       int64          `json:"repeat"`
		Converted    int64          `json:"converted" doc:"Members joined in the period who played as a member's guest before"`
		TopReferrers []DashReferrer `json:"topReferrers"`
	} `json:"guests"`
	HighValue []DashMember `json:"highValue"`
	AtRisk    struct {
		Total     int64        `json:"total"`
		NoVisit   int64        `json:"noVisit"`
		Declining int64        `json:"declining"`
		Expiring  int64        `json:"expiring"`
		Members   []DashMember `json:"members"`
	} `json:"atRisk"`
	Upcoming struct {
		Expiring    int64 `json:"expiring" doc:"Next 30 days"`
		Birthdays   int64 `json:"birthdays" doc:"Members, next 30 days"`
		VIPArrivals int64 `json:"vipArrivals" doc:"Tee times of VIPs, next 7 days"`
		Events      int64 `json:"events" doc:"Event registrations of members, next 30 days"`
		FollowUps   int64 `json:"followUps" doc:"Open follow-ups due in the next 7 days"`
	} `json:"upcoming"`
	FollowUps struct {
		DueToday int64          `json:"dueToday"`
		Overdue  int64          `json:"overdue"`
		Upcoming int64          `json:"upcoming"`
		Items    []DashFollowUp `json:"items"`
	} `json:"followUps" doc:"Mine"`
}

// DashboardFilter selects the period, the membership type and the thresholds.
type DashboardFilter struct {
	From, To                                 time.Time
	Type                                     string
	ActiveDays, OccasionalDays, ExpiringDays int
}

func dashboardFilter(ctx context.Context, tx pgx.Tx, r *http.Request) (DashboardFilter, error) {
	v := r.URL.Query()
	today := localToday(ctx, tx, handle.Property(ctx))
	f := DashboardFilter{Type: v.Get("type"), ActiveDays: DefaultActiveDays, OccasionalDays: DefaultOccasionalDays, ExpiringDays: DefaultExpiringDays}
	switch v.Get("period") {
	case "quarter":
		m := (int(today.Month())-1)/3*3 + 1
		f.From = time.Date(today.Year(), time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		f.To = f.From.AddDate(0, 3, -1)
	case "year":
		f.From = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		f.To = time.Date(today.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	case "", "month":
		f.From = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		f.To = f.From.AddDate(0, 1, -1)
	default:
		return f, handle.Invalid("period", "invalid_period", "period must be month, quarter or year")
	}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"activeDays", &f.ActiveDays}, {"occasionalDays", &f.OccasionalDays}, {"expiringDays", &f.ExpiringDays}} {
		if s := v.Get(p.name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 730 {
				return f, handle.Invalid(p.name, "invalid_days", "days between 1 and 730")
			}
			*p.dst = n
		}
	}
	if f.OccasionalDays <= f.ActiveDays {
		return f, handle.Invalid("occasionalDays", "invalid_days", "occasionalDays must be above activeDays")
	}
	return f, nil
}

// dashCTE: $1 property, $2 membership type (” = all). mem is the current
// membership of each member customer; last is the last activity (spend or
// round) of every customer, as a local date of the club (tz).
const dashCTE = `WITH tz AS (SELECT coalesce((SELECT timezone FROM platform.properties WHERE id = $1 AND timezone <> ''),
	(SELECT timezone FROM platform.instance)) AS z),
mem AS (
	SELECT DISTINCT ON (customer_id) customer_id, type_name, status, joined_on, ends_on
	FROM reporting.memberships WHERE property_id = $1 AND customer_id IS NOT NULL AND ($2 = '' OR type_name = $2)
	ORDER BY customer_id, (status = 'active') DESC, ends_on DESC NULLS LAST),
act AS (
	SELECT customer_id, (occurred_at AT TIME ZONE (SELECT z FROM tz))::date AS day FROM reporting.eng_spend WHERE property_id = $1 AND customer_id IS NOT NULL
	UNION ALL
	SELECT customer_id, play_date FROM reporting.golf_rounds WHERE property_id = $1 AND customer_id IS NOT NULL AND tee_off_at IS NOT NULL),
last AS (SELECT customer_id, max(day) AS last_day FROM act GROUP BY 1),
rfm AS (
	SELECT s.* FROM crm.rfm_scores s
	WHERE s.property_id = $1 AND s.as_of = (SELECT max(as_of) FROM crm.rfm_scores WHERE property_id = $1)) `

// Columns, joins and the at-risk reason of the member lists ($3 today, $4
// active days, $5 expiring days).
const dashCols = `m.customer_id, c.code, c.name, m.type_name, coalesce(r.frequency, 0)::bigint, coalesce(r.monetary, 0)::float8, coalesce(r.clv, 0)::float8,
		l.last_day, CASE WHEN l.last_day IS NULL THEN NULL ELSE ($3::date - l.last_day)::bigint END, m.ends_on`

const dashJoins = ` FROM mem m JOIN crm.customers c ON c.id = m.customer_id LEFT JOIN rfm r ON r.customer_id = m.customer_id
		LEFT JOIN last l ON l.customer_id = m.customer_id WHERE m.status = 'active'`

const dashRisk = `CASE WHEN m.ends_on <= $3::date + $5::int THEN 'expiring'
		WHEN l.last_day IS NULL OR l.last_day < $3::date - $4::int THEN 'no_visit'
		WHEN r.rfm_group IN ('at_risk', 'lapsed') THEN 'declining' END`

// Dashboard builds the CRM Dashboard Overview.
func Dashboard(ctx context.Context, tx pgx.Tx, property uuid.UUID, f DashboardFilter) (CRMDashboard, error) {
	var d CRMDashboard
	today := localToday(ctx, tx, property)
	d.From, d.To, d.Today = f.From, f.To, today
	d.Thresholds.ActiveDays, d.Thresholds.OccasionalDays, d.Thresholds.ExpiringDays = f.ActiveDays, f.OccasionalDays, f.ExpiringDays
	end := f.To.AddDate(0, 0, 1) // exclusive
	span := int(end.Sub(f.From).Hours() / 24)
	prevFrom := f.From.AddDate(0, 0, -span)

	one := func(dst *int64, q string, args ...any) error {
		return tx.QueryRow(ctx, dashCTE+q, append([]any{property, f.Type}, args...)...).Scan(dst)
	}
	counts := func(q string, args ...any) ([]DashCount, error) {
		rows, err := tx.Query(ctx, dashCTE+q, append([]any{property, f.Type}, args...)...)
		if err != nil {
			return nil, err
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DashCount, error) {
			var c DashCount
			return c, row.Scan(&c.Key, &c.Count)
		})
		if out == nil {
			out = []DashCount{}
		}
		return out, err
	}

	rows, err := tx.Query(ctx, `SELECT DISTINCT type_name FROM reporting.memberships WHERE property_id = $1 ORDER BY 1`, property)
	if err != nil {
		return d, err
	}
	if d.Types, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return d, err
	}

	// KPIs
	if err := tx.QueryRow(ctx, dashCTE+`SELECT
		count(*) FILTER (WHERE m.status = 'active'),
		count(*) FILTER (WHERE m.joined_on >= $3::date AND m.joined_on < $4::date),
		count(*) FILTER (WHERE m.joined_on >= $5::date AND m.joined_on < $3::date),
		count(*) FILTER (WHERE m.status = 'active' AND EXISTS (SELECT 1 FROM act a WHERE a.customer_id = m.customer_id AND a.day >= $3::date AND a.day < $4::date)),
		count(*) FILTER (WHERE m.status = 'active' AND m.ends_on BETWEEN $6::date AND $6::date + $7::int),
		coalesce((SELECT avg(r.clv) FROM rfm r JOIN mem x ON x.customer_id = r.customer_id AND x.status = 'active'), 0)::float8
		FROM mem m`, property, f.Type, f.From, end, prevFrom, today, f.ExpiringDays).
		Scan(&d.KPIs.TotalMembers, &d.KPIs.NewMembers, &d.KPIs.NewMembersPrev, &d.KPIs.ActiveMembers, &d.KPIs.Expiring, &d.KPIs.AvgCLV); err != nil {
		return d, err
	}
	d.KPIs.InactiveMembers = d.KPIs.TotalMembers - d.KPIs.ActiveMembers
	if err := one(&d.KPIs.NonMembers, `SELECT count(DISTINCT s.customer_id) FROM reporting.eng_spend s
		WHERE s.property_id = $1 AND s.customer_id IS NOT NULL AND (s.occurred_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (s.occurred_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date
		  AND NOT EXISTS (SELECT 1 FROM mem m WHERE m.customer_id = s.customer_id AND m.status = 'active')`, f.From, end); err != nil {
		return d, err
	}
	var renewed, lost int64
	if err := tx.QueryRow(ctx, dashCTE+`SELECT count(*) FILTER (WHERE event = 'renewed'), count(*) FILTER (WHERE event IN ('expired', 'cancelled'))
		FROM reporting.membership_events WHERE property_id = $1 AND (occurred_at AT TIME ZONE (SELECT z FROM tz))::date > $3::date - 365 AND (occurred_at AT TIME ZONE (SELECT z FROM tz))::date <= $3::date`,
		property, f.Type, today).Scan(&renewed, &lost); err != nil {
		return d, err
	}
	if renewed+lost > 0 {
		r := float64(renewed) / float64(renewed+lost)
		d.KPIs.Retention = &r
	}

	// Member growth: the 12 months up to the period end (or today, whichever is earlier).
	anchor := f.To
	if today.Before(anchor) {
		anchor = today
	}
	rows, err = tx.Query(ctx, dashCTE+`, months AS (
		SELECT m::date AS m0, (m + interval '1 month')::date AS m1 FROM generate_series(date_trunc('month', $3::date) - interval '11 months', date_trunc('month', $3::date), interval '1 month') m),
	ev AS (SELECT e.event, (e.occurred_at AT TIME ZONE (SELECT z FROM tz))::date AS day FROM reporting.membership_events e
		JOIN reporting.memberships x ON x.membership_id = e.membership_id WHERE e.property_id = $1 AND ($2 = '' OR x.type_name = $2))
	SELECT to_char(mo.m0, 'YYYY-MM'),
		(SELECT count(*) FROM mem WHERE joined_on < mo.m1 AND (status = 'active' OR ends_on >= mo.m1)),
		(SELECT count(*) FROM mem WHERE joined_on >= mo.m0 AND joined_on < mo.m1),
		(SELECT count(*) FROM ev WHERE event = 'renewed' AND day >= mo.m0 AND day < mo.m1),
		(SELECT count(*) FROM mem WHERE status = 'expired' AND ends_on >= mo.m0 AND ends_on < mo.m1),
		(SELECT count(*) FROM ev WHERE event = 'cancelled' AND day >= mo.m0 AND day < mo.m1)
	FROM months mo ORDER BY mo.m0`, property, f.Type, anchor)
	if err != nil {
		return d, err
	}
	if d.Growth, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DashGrowth, error) {
		var g DashGrowth
		err := row.Scan(&g.Month, &g.Members, &g.New, &g.Renewed, &g.Expired, &g.Cancelled)
		g.Net = g.New - g.Expired - g.Cancelled
		return g, err
	}); err != nil {
		return d, err
	}

	// Segmentation (the expiring members first, then by recency).
	if d.Segments, err = counts(`SELECT CASE WHEN m.ends_on <= $3::date + $6::int THEN 'expiring'
			WHEN l.last_day >= $3::date - $4::int THEN 'active' WHEN l.last_day >= $3::date - $5::int THEN 'occasional' ELSE 'inactive' END AS k, count(*)
		FROM mem m LEFT JOIN last l ON l.customer_id = m.customer_id WHERE m.status = 'active' GROUP BY 1`,
		today, f.ActiveDays, f.OccasionalDays, f.ExpiringDays); err != nil {
		return d, err
	}

	// What the members use the club for, in the period.
	if d.Activity, err = counts(`
		SELECT 'golf_round', count(*) FROM reporting.golf_rounds r JOIN mem m ON m.customer_id = r.customer_id
			WHERE r.property_id = $1 AND r.tee_off_at IS NOT NULL AND r.play_date >= $3::date AND r.play_date < $4::date
		UNION ALL SELECT 'tee_time_booking', count(*) FROM reporting.golf_bookings b JOIN mem m ON m.customer_id = b.customer_id
			WHERE b.property_id = $1 AND (b.created_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (b.created_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date AND b.status <> 'cancelled'
		UNION ALL SELECT 'event', count(*) FROM reporting.crm_banquet_event_customers c JOIN reporting.banquet_events e ON e.event_id = c.event_id
			JOIN mem m ON m.customer_id = c.customer_id WHERE c.property_id = $1 AND (e.start_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (e.start_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date AND e.status <> 'cancelled'
		UNION ALL SELECT k, count(DISTINCT (s.customer_id, (s.occurred_at AT TIME ZONE (SELECT z FROM tz))::date)) FROM (VALUES ('stay', 'bungalow_stay'), ('pos', 'fnb'), ('sportclub', 'sport')) v(line, k)
			JOIN reporting.eng_spend s ON s.business_line = v.line AND s.kind = 'charge' JOIN mem m ON m.customer_id = s.customer_id
			WHERE s.property_id = $1 AND (s.occurred_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (s.occurred_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date GROUP BY k`, f.From, end); err != nil {
		return d, err
	}

	if d.Engagement, err = counts(`SELECT coalesce(r.rfm_group, 'no_score'), count(*) FROM mem m LEFT JOIN rfm r ON r.customer_id = m.customer_id
		WHERE m.status = 'active' GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return d, err
	}
	if d.ByType, err = counts(`SELECT type_name, count(*) FROM mem WHERE status = 'active' GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return d, err
	}
	if d.ByStatus, err = counts(`SELECT CASE WHEN status = 'active' AND ends_on <= $3::date + $4::int THEN 'expiring' ELSE status END, count(*)
		FROM reporting.memberships WHERE property_id = $1 AND ($2 = '' OR type_name = $2) GROUP BY 1 ORDER BY 2 DESC`, today, f.ExpiringDays); err != nil {
		return d, err
	}

	// Renewal this month and the expiry windows.
	m0 := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	if err := tx.QueryRow(ctx, dashCTE+`SELECT
		(SELECT count(*) FROM reporting.memberships x WHERE x.property_id = $1 AND ($2 = '' OR x.type_name = $2) AND x.ends_on >= $3::date AND x.ends_on < $4::date),
		(SELECT count(*) FROM reporting.membership_events e JOIN reporting.memberships x ON x.membership_id = e.membership_id
			WHERE e.property_id = $1 AND ($2 = '' OR x.type_name = $2) AND e.event = 'renewed' AND (e.occurred_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (e.occurred_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date),
		(SELECT count(*) FROM mem WHERE status = 'active' AND ends_on >= $5::date AND ends_on < $4::date),
		(SELECT count(*) FROM mem WHERE status = 'expired' AND ends_on >= $3::date AND ends_on < $4::date),
		(SELECT count(*) FROM mem WHERE status = 'active' AND ends_on BETWEEN $5::date AND $5::date + 30),
		(SELECT count(*) FROM mem WHERE status = 'active' AND ends_on BETWEEN $5::date AND $5::date + 60),
		(SELECT count(*) FROM mem WHERE status = 'active' AND ends_on BETWEEN $5::date AND $5::date + 90)`,
		property, f.Type, m0, m0.AddDate(0, 1, 0), today).
		Scan(&d.Renewal.Due, &d.Renewal.Renewed, &d.Renewal.Pending, &d.Renewal.Expired, &d.Expiry.D30, &d.Expiry.D60, &d.Expiry.D90); err != nil {
		return d, err
	}

	// Acquisition: customers created in the period, by the source of their lead.
	if err := one(&d.Acquisition.NewCustomers, `SELECT count(*) FROM crm.customers c
		WHERE c.property_id = $1 AND (c.created_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (c.created_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date AND c.merged_into_id IS NULL`, f.From, end); err != nil {
		return d, err
	}
	if d.Acquisition.Sources, err = counts(`SELECT coalesce(l.source, 'direct'), count(*) FROM crm.customers c
		LEFT JOIN LATERAL (SELECT source FROM reporting.sales_leads s WHERE s.customer_id = c.id ORDER BY s.created_at LIMIT 1) l ON true
		WHERE c.property_id = $1 AND (c.created_at AT TIME ZONE (SELECT z FROM tz))::date >= $3::date AND (c.created_at AT TIME ZONE (SELECT z FROM tz))::date < $4::date AND c.merged_into_id IS NULL GROUP BY 1 ORDER BY 2 DESC`, f.From, end); err != nil {
		return d, err
	}

	// Members' guests in the period.
	const guests = `, g AS (
		SELECT p.customer_id, coalesce(p.customer_id::text, lower(p.player_name)) AS who, p.play_date, b.customer_id AS host
		FROM reporting.golf_players p JOIN reporting.golf_bookings b ON b.booking_id = p.booking_id
		WHERE p.property_id = $1 AND p.player_type = 'guest_of_member' AND p.booking_status <> 'cancelled' AND p.play_date >= $3::date AND p.play_date < $4::date) `
	if err := tx.QueryRow(ctx, dashCTE+guests+`SELECT count(*), count(DISTINCT who),
		(SELECT count(*) FROM (SELECT who FROM g GROUP BY who HAVING count(*) > 1) x),
		(SELECT count(*) FROM mem m WHERE m.joined_on >= $3::date AND m.joined_on < $4::date AND EXISTS (
			SELECT 1 FROM reporting.golf_players p WHERE p.property_id = $1 AND p.customer_id = m.customer_id AND p.player_type = 'guest_of_member' AND p.play_date <= m.joined_on))
		FROM g`, property, f.Type, f.From, end).Scan(&d.Guests.Visits, &d.Guests.Unique, &d.Guests.Repeat, &d.Guests.Converted); err != nil {
		return d, err
	}
	rows, err = tx.Query(ctx, dashCTE+guests+`SELECT g.host, c.name, count(*) FROM g JOIN mem m ON m.customer_id = g.host JOIN crm.customers c ON c.id = g.host
		GROUP BY 1, 2 ORDER BY 3 DESC, 2 LIMIT 5`, property, f.Type, f.From, end)
	if err != nil {
		return d, err
	}
	if d.Guests.TopReferrers, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DashReferrer, error) {
		var x DashReferrer
		return x, row.Scan(&x.CustomerID, &x.Name, &x.Guests)
	}); err != nil {
		return d, err
	}

	// High value and at-risk members.
	member := func(q string, args ...any) ([]DashMember, error) {
		return dashMembers(ctx, tx, q, append([]any{property, f.Type}, args...)...)
	}
	if d.HighValue, err = member(`SELECT `+dashCols+`, ''`+dashJoins+` ORDER BY coalesce(r.clv, 0) DESC, c.name LIMIT 8`, today); err != nil {
		return d, err
	}
	if err := tx.QueryRow(ctx, dashCTE+`SELECT count(*) FILTER (WHERE k IS NOT NULL), count(*) FILTER (WHERE k = 'no_visit'),
		count(*) FILTER (WHERE k = 'declining'), count(*) FILTER (WHERE k = 'expiring')
		FROM (SELECT `+dashRisk+` AS k`+dashJoins+`) x`, property, f.Type, today, f.ActiveDays, f.ExpiringDays).
		Scan(&d.AtRisk.Total, &d.AtRisk.NoVisit, &d.AtRisk.Declining, &d.AtRisk.Expiring); err != nil {
		return d, err
	}
	if d.AtRisk.Members, err = member(`SELECT `+dashCols+`, `+dashRisk+dashJoins+` AND (`+dashRisk+`) IS NOT NULL
		ORDER BY coalesce(r.clv, 0) DESC, c.name LIMIT 8`, today, f.ActiveDays, f.ExpiringDays); err != nil {
		return d, err
	}

	// Upcoming (birthdays by month-day, across the year end).
	md := func(t time.Time) string { return t.Format("0102") }
	if err := tx.QueryRow(ctx, dashCTE+`SELECT
		(SELECT count(*) FROM mem WHERE status = 'active' AND ends_on BETWEEN $3::date AND $3::date + 30),
		(SELECT count(*) FROM mem m JOIN reporting.customer_directory c ON c.id = m.customer_id WHERE m.status = 'active' AND c.birth_date IS NOT NULL
			AND CASE WHEN $4::text <= $5::text THEN to_char(c.birth_date, 'MMDD') BETWEEN $4::text AND $5::text ELSE to_char(c.birth_date, 'MMDD') >= $4::text OR to_char(c.birth_date, 'MMDD') <= $5::text END),
		(SELECT count(*) FROM reporting.golf_bookings b JOIN crm.vip_customers v ON v.customer_id = b.customer_id AND v.status = 'active'
			WHERE b.property_id = $1 AND b.play_date BETWEEN $3::date AND $3::date + 7 AND b.status NOT IN ('cancelled', 'no_show')),
		(SELECT count(*) FROM reporting.crm_banquet_event_customers c JOIN reporting.banquet_events e ON e.event_id = c.event_id JOIN mem m ON m.customer_id = c.customer_id
			WHERE c.property_id = $1 AND (e.start_at AT TIME ZONE (SELECT z FROM tz))::date BETWEEN $3::date AND $3::date + 30 AND e.status <> 'cancelled'),
		(SELECT count(*) FROM crm.sales_activities a WHERE a.property_id = $1 AND a.status = 'open' AND (a.due_at AT TIME ZONE (SELECT z FROM tz))::date BETWEEN $3::date AND $3::date + 7)`,
		property, f.Type, today, md(today), md(today.AddDate(0, 0, 30))).
		Scan(&d.Upcoming.Expiring, &d.Upcoming.Birthdays, &d.Upcoming.VIPArrivals, &d.Upcoming.Events, &d.Upcoming.FollowUps); err != nil {
		return d, err
	}

	// My follow-ups.
	me := uuid.Nil
	if p := authz.From(ctx); p != nil {
		me = p.UserID
	}
	if err := tx.QueryRow(ctx, `WITH tz AS (SELECT coalesce((SELECT timezone FROM platform.properties WHERE id = $1 AND timezone <> ''),
	(SELECT timezone FROM platform.instance)) AS z), a AS (SELECT (due_at AT TIME ZONE (SELECT z FROM tz))::date AS day FROM crm.sales_activities
		WHERE property_id = $1 AND assigned_to = $2 AND status = 'open' AND due_at IS NOT NULL)
		SELECT count(*) FILTER (WHERE day = $3::date), count(*) FILTER (WHERE day < $3::date), count(*) FILTER (WHERE day > $3::date) FROM a`,
		property, me, today).Scan(&d.FollowUps.DueToday, &d.FollowUps.Overdue, &d.FollowUps.Upcoming); err != nil {
		return d, err
	}
	rows, err = tx.Query(ctx, `SELECT a.id, a.activity_type, coalesce(a.subject, ''), a.customer_id, c.name, a.due_at FROM crm.sales_activities a
		LEFT JOIN crm.customers c ON c.id = a.customer_id
		WHERE a.property_id = $1 AND a.assigned_to = $2 AND a.status = 'open' ORDER BY a.due_at NULLS LAST LIMIT 6`, property, me)
	if err != nil {
		return d, err
	}
	if d.FollowUps.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (DashFollowUp, error) {
		var x DashFollowUp
		return x, row.Scan(&x.ID, &x.Type, &x.Subject, &x.CustomerID, &x.CustomerName, &x.DueAt)
	}); err != nil {
		return d, err
	}
	if d.FollowUps.Items == nil {
		d.FollowUps.Items = []DashFollowUp{}
	}
	if d.Guests.TopReferrers == nil {
		d.Guests.TopReferrers = []DashReferrer{}
	}
	return d, nil
}

// riskActions are the recommended actions per at-risk reason.
var riskActions = map[string]string{"expiring": "Renewal call", "no_visit": "Win-back offer", "declining": "Retention call"}

func dashMembers(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]DashMember, error) {
	rows, err := tx.Query(ctx, dashCTE+q, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DashMember, error) {
		var x DashMember
		err := row.Scan(&x.CustomerID, &x.Code, &x.Name, &x.TypeName, &x.Visits, &x.Value, &x.CLV, &x.LastVisit, &x.RecencyDays, &x.EndsOn, &x.Reason)
		x.Action = riskActions[x.Reason]
		return x, err
	})
	if out == nil {
		out = []DashMember{}
	}
	return out, err
}

// DashboardMembers lists the members behind a dashboard widget: expiring
// within `days`, at risk, or by value (high_value).
func DashboardMembers(ctx context.Context, tx pgx.Tx, property uuid.UUID, f DashboardFilter, list string, days, limit int) ([]DashMember, error) {
	today := localToday(ctx, tx, property)
	switch list {
	case "expiring":
		return dashMembers(ctx, tx, `SELECT `+dashCols+`, 'expiring'`+dashJoins+` AND m.ends_on BETWEEN $3::date AND $3::date + $4::int
			ORDER BY m.ends_on, c.name LIMIT $5`, property, f.Type, today, days, limit)
	case "at_risk":
		return dashMembers(ctx, tx, `SELECT `+dashCols+`, `+dashRisk+dashJoins+` AND (`+dashRisk+`) IS NOT NULL
			ORDER BY coalesce(r.clv, 0) DESC, c.name LIMIT $6`, property, f.Type, today, f.ActiveDays, f.ExpiringDays, limit)
	case "high_value":
		return dashMembers(ctx, tx, `SELECT `+dashCols+`, ''`+dashJoins+` ORDER BY coalesce(r.clv, 0) DESC, c.name LIMIT $4`, property, f.Type, today, limit)
	}
	return nil, handle.Invalid("list", "invalid_list", "list must be expiring, at_risk or high_value")
}
