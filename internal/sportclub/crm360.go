package sportclub

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// SportActivity is the Sport Club section of the Customer 360.
type SportActivity struct {
	EntriesLast90Days int             `json:"entriesLast90Days"`
	LastVisit         *time.Time      `json:"lastVisit"`
	Enrollments       []EnrolledClass `json:"enrollments"`
	ClassAttendance   int             `json:"classesAttended"`
	// court bookings (docs/requirement-booking-sportclub-mgcc.md FR-86)
	CourtBookings   int             `json:"courtBookings"`
	CourtBookings90 int             `json:"courtBookingsLast90Days"`
	CourtHours      string          `json:"courtHours"`
	CourtSpend      string          `json:"courtSpend" doc:"Court rent and extras, with tax"`
	CourtNoShows    int             `json:"courtNoShows"`
	FavoriteSport   *string         `json:"favoriteSport"`
	FavoriteHour    *string         `json:"favoriteHour"`
	LastCourtPlay   *time.Time      `json:"lastCourtPlay"`
	UpcomingCourts  int             `json:"upcomingCourtBookings"`
	Packages        []ActivePackage `json:"packages" doc:"Active court and class packages with the quota left"`
}

type EnrolledClass struct {
	Program    string     `json:"program" db:"program"`
	Status     string     `json:"status" db:"status"`
	ValidUntil *time.Time `json:"validUntil" db:"valid_until"`
}

// CustomerSection is the Sport Club part of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := SportActivity{}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE visit_date > billing.local_date(property_id) - 90)::int, max(used_at) FROM sportclub.entries
		WHERE (customer_id = $1 OR host_customer_id = $1) AND status <> 'cancelled'`, customer).Scan(&a.EntriesLast90Days, &a.LastVisit); err != nil {
		return a, err
	}
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM sportclub.session_bookings WHERE customer_id = $1 AND status = 'present'`, customer).Scan(&a.ClassAttendance); err != nil {
		return a, err
	}
	var err error
	if a.Enrollments, err = handle.List[EnrolledClass](q.Query(ctx, `SELECT p.name AS program, e.status, e.valid_until FROM sportclub.enrollments e
		JOIN sportclub.class_programs p ON p.id = e.program_id WHERE e.customer_id = $1 ORDER BY e.created_at DESC`, customer)); err != nil {
		return a, err
	}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT r.id)::int,
		count(DISTINCT r.id) FILTER (WHERE lower(l.period) > now() - interval '90 days')::int,
		trim_scale(round(coalesce(sum(extract(epoch FROM upper(l.period) - lower(l.period)) / 3600) FILTER (WHERE l.status <> 'no_show'), 0)::numeric, 1))::text,
		count(*) FILTER (WHERE l.status = 'no_show')::int,
		max(lower(l.period)) FILTER (WHERE l.status IN ('checked_in', 'completed')),
		count(DISTINCT r.id) FILTER (WHERE lower(l.period) > now() AND l.status IN ('held', 'confirmed'))::int,
		(SELECT f.name FROM reservation.reservation_lines l2 JOIN reservation.reservations r2 ON r2.id = l2.reservation_id
		   JOIN sportclub.courts c ON c.resource_id = l2.resource_id JOIN sportclub.facilities f ON f.id = c.facility_id
		   WHERE r2.customer_id = $1 AND r2.source_type = 'sportclub.court_booking' AND r2.status NOT IN ('cancelled', 'expired') GROUP BY f.name ORDER BY count(*) DESC LIMIT 1),
		(SELECT to_char(lower(l2.period) AT TIME ZONE (SELECT timezone FROM platform.instance), 'HH24:00') FROM reservation.reservation_lines l2
		   JOIN reservation.reservations r2 ON r2.id = l2.reservation_id WHERE r2.customer_id = $1 AND r2.source_type = 'sportclub.court_booking'
		   AND r2.status NOT IN ('cancelled', 'expired') GROUP BY 1 ORDER BY count(*) DESC LIMIT 1),
		trim_scale(coalesce((SELECT sum(x.total) FROM billing.folio_lines x JOIN reservation.reservations r3 ON r3.folio_id = x.folio_id
		   WHERE r3.customer_id = $1 AND r3.source_type = 'sportclub.court_booking' AND x.voided_at IS NULL AND coalesce(x.revenue_component, '') <> 'gateway_fee'), 0))::text
		FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		WHERE r.customer_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.status NOT IN ('cancelled', 'expired')`, customer).
		Scan(&a.CourtBookings, &a.CourtBookings90, &a.CourtHours, &a.CourtNoShows, &a.LastCourtPlay, &a.UpcomingCourts, &a.FavoriteSport, &a.FavoriteHour,
			&a.CourtSpend); err != nil {
		return a, err
	}
	a.Packages, err = m.activePackages(ctx, q, property, &customer, "")
	return a, err
}

// CustomerBehavior derives sport habits: visits and favourite facility.
func (m *Module) CustomerBehavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (crm.Behavior, error) {
	b := crm.Behavior{Facts: map[string]any{}, Highlights: []string{}}
	var visits int
	var fav *string
	if err := q.QueryRow(ctx, `SELECT count(*)::int, (SELECT f.name FROM sportclub.entries e2 JOIN sportclub.facilities f ON f.id = e2.facility_id
		WHERE e2.customer_id = $1 AND e2.status = 'used' GROUP BY f.name ORDER BY count(*) DESC LIMIT 1)
		FROM sportclub.entries WHERE customer_id = $1 AND status = 'used' AND visit_date > billing.local_date(property_id) - 90`, customer).Scan(&visits, &fav); err != nil {
		return b, err
	}
	b.Facts["visitsLast90Days"] = visits
	if fav != nil {
		b.Facts["favoriteFacility"] = *fav
		b.Highlights = append(b.Highlights, "Regular at "+*fav)
	}
	var courts int
	var sport *string
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT r.id)::int, (SELECT f.name FROM reservation.reservation_lines l2 JOIN reservation.reservations r2 ON r2.id = l2.reservation_id
		JOIN sportclub.courts c ON c.resource_id = l2.resource_id JOIN sportclub.facilities f ON f.id = c.facility_id
		WHERE r2.customer_id = $1 AND r2.source_type = 'sportclub.court_booking' GROUP BY f.name ORDER BY count(*) DESC LIMIT 1)
		FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		WHERE r.customer_id = $1 AND r.source_type = 'sportclub.court_booking' AND r.status NOT IN ('cancelled', 'expired')
		AND lower(l.period) > now() - interval '90 days'`, customer).Scan(&courts, &sport); err != nil {
		return b, err
	}
	b.Facts["courtBookingsLast90Days"] = courts
	if sport != nil && courts > 0 {
		b.Facts["favoriteSport"] = *sport
		b.Highlights = append(b.Highlights, fmt.Sprintf("Plays %s (%d court bookings in 90 days)", *sport, courts))
	}
	return b, nil
}

// SegmentFact is the court activity of a customer for CRM segments.
type SegmentFact struct {
	CourtBookings int
	Sports        []string
	LastCourtPlay *time.Time
}

// SegmentFacts returns the court bookings since a date, the sports played
// and the last court played per customer (CRM segmentation, FR-87).
func SegmentFacts(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]SegmentFact, error) {
	type row struct {
		CustomerID uuid.UUID  `db:"customer_id"`
		Bookings   int        `db:"bookings"`
		Sports     []string   `db:"sports"`
		Last       *time.Time `db:"last_play"`
	}
	list, err := handle.List[row](q.Query(ctx, `SELECT r.customer_id, count(DISTINCT r.id) FILTER (WHERE lower(l.period) >= $2)::int AS bookings,
		array_agg(DISTINCT f.code) AS sports, max(lower(l.period)) FILTER (WHERE l.status IN ('checked_in', 'completed', 'confirmed') AND lower(l.period) < now()) AS last_play
		FROM reservation.reservations r JOIN reservation.reservation_lines l ON l.reservation_id = r.id
		JOIN sportclub.courts c ON c.resource_id = l.resource_id JOIN sportclub.facilities f ON f.id = c.facility_id
		WHERE r.property_id = $1 AND r.customer_id IS NOT NULL AND r.source_type = 'sportclub.court_booking' AND r.status NOT IN ('cancelled', 'expired')
		GROUP BY r.customer_id`, property, since))
	out := map[uuid.UUID]SegmentFact{}
	for _, x := range list {
		out[x.CustomerID] = SegmentFact{CourtBookings: x.Bookings, Sports: x.Sports, LastCourtPlay: x.Last}
	}
	return out, err
}
