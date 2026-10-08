package crm

// Member 360: the relationship block of Customer 360 — engagement per
// business line and value (analytics store), upcoming tee times and the
// guests the customer brings (reporting read models), interactions and the
// open follow-ups of the relationship team.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
)

// OverviewLine is the customer's behaviour in one business line.
type OverviewLine struct {
	Line      string     `json:"line"`
	Frequency int64      `json:"frequency" doc:"Visit days"`
	Monetary  float64    `json:"monetary"`
	LastAt    *time.Time `json:"lastAt"`
}

// OverviewBooking is an upcoming tee time.
type OverviewBooking struct {
	BookingID  uuid.UUID  `json:"bookingId"`
	Code       string     `json:"code"`
	PlayDate   time.Time  `json:"playDate"`
	StartAt    *time.Time `json:"startAt"`
	CourseName string     `json:"courseName"`
	Players    int64      `json:"players"`
	Status     string     `json:"status"`
}

// OverviewTask is an open follow-up on the customer.
type OverviewTask struct {
	ID      uuid.UUID  `json:"id"`
	Type    string     `json:"type"`
	Subject string     `json:"subject"`
	DueAt   *time.Time `json:"dueAt"`
}

// OverviewRelationship is the relationship block of Customer 360.
type OverviewRelationship struct {
	Lines            []OverviewLine    `json:"lines"`
	RFMGroup         *string           `json:"rfmGroup"`
	CLV              *float64          `json:"clv"`
	Spend            *float64          `json:"spend" doc:"RFM monetary"`
	LastActivity     *time.Time        `json:"lastActivity"`
	UpcomingBookings []OverviewBooking `json:"upcomingBookings"`
	Guests           int64             `json:"guests" doc:"Guests brought on the customer's tee times"`
	Interactions     int64             `json:"interactions"`
	LastInteraction  *time.Time        `json:"lastInteraction"`
	Tasks            []OverviewTask    `json:"tasks"`
}

func loadRelationship(ctx context.Context, tx pgx.Tx, cid, pid uuid.UUID) (OverviewRelationship, error) {
	o := OverviewRelationship{Lines: []OverviewLine{}, UpcomingBookings: []OverviewBooking{}, Tasks: []OverviewTask{}}
	rows, err := tx.Query(ctx, `SELECT business_line, frequency::bigint, monetary::float8, last_at FROM crm.customer_line_behavior
		WHERE property_id = $1 AND customer_id = $2 ORDER BY monetary DESC`, pid, cid)
	if err != nil {
		return o, err
	}
	if o.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OverviewLine, error) {
		var l OverviewLine
		return l, row.Scan(&l.Line, &l.Frequency, &l.Monetary, &l.LastAt)
	}); err != nil {
		return o, err
	}
	for _, l := range o.Lines {
		if l.LastAt != nil && (o.LastActivity == nil || l.LastAt.After(*o.LastActivity)) {
			o.LastActivity = l.LastAt
		}
	}
	if err := tx.QueryRow(ctx, `SELECT rfm_group, clv::float8, monetary::float8 FROM crm.rfm_scores WHERE property_id = $1 AND customer_id = $2
		ORDER BY as_of DESC LIMIT 1`, pid, cid).Scan(&o.RFMGroup, &o.CLV, &o.Spend); err != nil && !dbtx.IsNoRows(err) {
		return o, err
	}
	rows, err = tx.Query(ctx, `WITH tz AS (SELECT coalesce((SELECT timezone FROM platform.properties WHERE id = $1 AND timezone <> ''),
	(SELECT timezone FROM platform.instance)) AS z)
		SELECT booking_id, code, play_date, start_at, coalesce(course_name, ''), coalesce(player_count, 0)::bigint, status
		FROM reporting.golf_bookings WHERE property_id = $1 AND customer_id = $2 AND play_date >= (now() AT TIME ZONE (SELECT z FROM tz))::date AND status NOT IN ('cancelled', 'no_show')
		ORDER BY play_date, start_at LIMIT 5`, pid, cid)
	if err != nil {
		return o, err
	}
	if o.UpcomingBookings, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OverviewBooking, error) {
		var b OverviewBooking
		return b, row.Scan(&b.BookingID, &b.Code, &b.PlayDate, &b.StartAt, &b.CourseName, &b.Players, &b.Status)
	}); err != nil {
		return o, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM reporting.golf_players p JOIN reporting.golf_bookings b ON b.booking_id = p.booking_id
		WHERE p.property_id = $1 AND b.customer_id = $2 AND p.player_type = 'guest_of_member' AND p.booking_status <> 'cancelled'`, pid, cid).Scan(&o.Guests); err != nil {
		return o, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*), max(occurred_at) FROM crm.interactions WHERE property_id = $1 AND customer_id = $2`, pid, cid).
		Scan(&o.Interactions, &o.LastInteraction); err != nil {
		return o, err
	}
	rows, err = tx.Query(ctx, `SELECT id, activity_type, coalesce(subject, ''), due_at FROM crm.sales_activities
		WHERE property_id = $1 AND customer_id = $2 AND status = 'open' ORDER BY due_at NULLS LAST LIMIT 5`, pid, cid)
	if err != nil {
		return o, err
	}
	o.Tasks, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OverviewTask, error) {
		var t OverviewTask
		return t, row.Scan(&t.ID, &t.Type, &t.Subject, &t.DueAt)
	})
	if o.Tasks == nil {
		o.Tasks = []OverviewTask{}
	}
	return o, err
}
