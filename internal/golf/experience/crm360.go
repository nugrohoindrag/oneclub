package experience

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// GolfActivity is the golf section of the Customer 360 (PRD P2 FR-CRM-01).
type GolfActivity struct {
	Rounds          int              `json:"rounds"`
	LastRound       *time.Time       `json:"lastRound"`
	HandicapIndex   *string          `json:"handicapIndex"`
	OfficialIndex   *string          `json:"officialHandicap"`
	UpcomingFlights []UpcomingFlight `json:"upcomingFlights"`
	FavoriteCaddies []string         `json:"favoriteCaddies"`
	RecentRounds    []Scorecard      `json:"recentRounds"`
}

type UpcomingFlight struct {
	FlightID    uuid.UUID `json:"flightId" db:"id"`
	BookingCode *string   `json:"bookingCode" db:"booking_code"`
	TeeTime     time.Time `json:"teeTime" db:"tee_time"`
	Course      string    `json:"course" db:"course_name"`
	Status      string    `json:"status" db:"status"`
}

// playedRounds are P1 booking players of the customer whose flight finished.
const playedRounds = `FROM golf.booking_players p JOIN golf.flights f ON f.id = p.flight_id
	WHERE p.customer_id = $1 AND p.status = 'checked_in' AND f.status = 'completed'`

// CustomerSection is the golf part of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := GolfActivity{UpcomingFlights: []UpcomingFlight{}, FavoriteCaddies: []string{}}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT p.flight_id)::int, max(f.round_finish_at) `+playedRounds, customer).Scan(&a.Rounds, &a.LastRound); err != nil {
		return a, err
	}
	_ = q.QueryRow(ctx, `SELECT (SELECT trim_scale(handicap_index)::text FROM golf.handicaps WHERE customer_id = $1 AND source <> 'federation' ORDER BY effective_at DESC LIMIT 1),
		(SELECT trim_scale(handicap_index)::text FROM golf.handicaps WHERE customer_id = $1 AND source = 'federation' ORDER BY effective_at DESC LIMIT 1)`, customer).
		Scan(&a.HandicapIndex, &a.OfficialIndex)
	var err error
	if a.UpcomingFlights, err = handle.List[UpcomingFlight](q.Query(ctx, `SELECT DISTINCT f.id, b.code AS booking_code, tt.start_at AS tee_time, c.name AS course_name, f.status
		FROM golf.booking_players p JOIN golf.flights f ON f.id = p.flight_id JOIN golf.tee_times tt ON tt.id = f.tee_time_id JOIN golf.courses c ON c.id = f.course_id
		LEFT JOIN golf.bookings b ON b.id = f.booking_id
		WHERE p.customer_id = $1 AND p.status IN ('booked', 'checked_in') AND tt.start_at >= now() AND f.status NOT IN ('completed', 'cancelled')
		ORDER BY tt.start_at LIMIT 5`, customer)); err != nil {
		return a, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(array_agg('#' || c.code || ' ' || c.name ORDER BY c.code), '{}') FROM golf.caddy_favorites f
		JOIN golf.caddies c ON c.id = f.caddy_id WHERE f.customer_id = $1`, customer).Scan(&a.FavoriteCaddies); err != nil {
		return a, err
	}
	a.RecentRounds, err = handle.List[Scorecard](q.Query(ctx, scorecardSelect+` WHERE s.customer_id = $1 AND s.status = 'finalized'
		ORDER BY s.played_on DESC LIMIT 5`, customer))
	return a, err
}

var weekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// CustomerBehavior derives golf habits: rounds, favourite day & tee time,
// most frequent caddy (FR-PRF-02).
func (m *Module) CustomerBehavior(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (crm.Behavior, error) {
	b := crm.Behavior{Facts: map[string]any{}, Highlights: []string{}}
	var rounds int
	var dow, hour *int
	loc := location(ctx, q, property).String()
	if err := q.QueryRow(ctx, `SELECT count(*)::int,
		mode() WITHIN GROUP (ORDER BY extract(dow FROM f.tee_off_at AT TIME ZONE $2)::int),
		mode() WITHIN GROUP (ORDER BY extract(hour FROM f.tee_off_at AT TIME ZONE $2)::int)
		`+playedRounds+` AND f.tee_off_at > now() - interval '12 months'`, customer, loc).Scan(&rounds, &dow, &hour); err != nil {
		return b, err
	}
	b.Facts["roundsLast12Months"] = rounds
	if dow != nil && hour != nil && rounds > 0 {
		b.Facts["favoriteDay"] = weekdays[*dow]
		b.Facts["favoriteTeeHour"] = *hour
		b.Highlights = append(b.Highlights, "Usually plays "+weekdays[*dow]+" around "+itoa(*hour)+":00")
	}
	var caddy *string
	_ = q.QueryRow(ctx, `SELECT '#' || c.code || ' ' || c.name FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
		JOIN golf.booking_players p ON p.id = ANY(a.player_ids)
		WHERE p.customer_id = $1 AND a.status IN ('completed', 'replaced') GROUP BY c.code, c.name ORDER BY count(*) DESC LIMIT 1`, customer).Scan(&caddy)
	if caddy != nil {
		b.Facts["mostFrequentCaddy"] = *caddy
		b.Highlights = append(b.Highlights, "Most rounds with caddy "+*caddy)
	}
	return b, nil
}
