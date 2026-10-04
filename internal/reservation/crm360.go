package reservation

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// BookingSummary is one booking in the Customer 360.
type BookingSummary struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Code         string     `json:"code" db:"code"`
	BusinessLine string     `json:"businessLine" db:"business_line"`
	Status       string     `json:"status" db:"status"`
	Start        *time.Time `json:"start" db:"start_at"`
	Resource     *string    `json:"resource" db:"resource"`
	Channel      string     `json:"channel" db:"channel"`
}

// Bookings is the bookings section of the Customer 360: upcoming and recent
// bookings of every business line (sport, bungalow, VIP suite, meeting …).
type Bookings struct {
	Upcoming []BookingSummary `json:"upcoming"`
	Recent   []BookingSummary `json:"recent"`
	ByLine   map[string]int   `json:"countByBusinessLine"`
}

const bookingSummary = `SELECT r.id, r.code, r.business_line, r.status,
	(SELECT min(lower(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) AS start_at,
	(SELECT s.name FROM reservation.reservation_lines l JOIN reservation.resources s ON s.id = l.resource_id WHERE l.reservation_id = r.id
	  ORDER BY lower(l.period) LIMIT 1) AS resource, r.channel FROM reservation.reservations r`

// CustomerSection lists the customer's bookings across business lines.
func (e *Engine) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	out := Bookings{ByLine: map[string]int{}}
	var err error
	if out.Upcoming, err = handle.List[BookingSummary](q.Query(ctx, `SELECT * FROM (`+bookingSummary+` WHERE r.customer_id = $1 AND r.kind = 'booking'
		AND r.status IN ('pending', 'confirmed', 'checked_in')) x WHERE x.start_at >= now() - interval '1 day' ORDER BY x.start_at LIMIT 10`, customer)); err != nil {
		return out, err
	}
	if out.Recent, err = handle.List[BookingSummary](q.Query(ctx, bookingSummary+` WHERE r.customer_id = $1 AND r.kind = 'booking'
		ORDER BY r.created_at DESC LIMIT 10`, customer)); err != nil {
		return out, err
	}
	rows, err := q.Query(ctx, `SELECT business_line, count(*)::int FROM reservation.reservations WHERE customer_id = $1 AND kind = 'booking' GROUP BY 1`, customer)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return out, err
		}
		out.ByLine[k] = n
	}
	return out, rows.Err()
}

// CustomerBehavior derives booking behaviour: lead time, cancellation and
// no-show (FR-PRF-02).
func (e *Engine) CustomerBehavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (crm.Behavior, error) {
	b := crm.Behavior{Facts: map[string]any{}, Highlights: []string{}}
	var total, cancelled, noShow int
	var lead *float64
	if err := q.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE status = 'cancelled')::int, count(*) FILTER (WHERE status = 'no_show')::int,
		avg(extract(epoch FROM (SELECT min(lower(l.period)) FROM reservation.reservation_lines l WHERE l.reservation_id = r.id) - r.created_at) / 86400)
		FROM reservation.reservations r WHERE r.customer_id = $1 AND r.kind = 'booking' AND r.status <> 'expired'`, customer).Scan(&total, &cancelled, &noShow, &lead); err != nil {
		return b, err
	}
	b.Facts["bookings"] = total
	b.Facts["cancellations"] = cancelled
	b.Facts["noShows"] = noShow
	if lead != nil {
		b.Facts["averageLeadDays"] = int(*lead + 0.5)
	}
	if total >= 3 && cancelled*100/total >= 30 {
		b.Highlights = append(b.Highlights, "Cancels often")
	}
	if noShow > 0 {
		b.Highlights = append(b.Highlights, "Has no-shows on record")
	}
	return b, nil
}
