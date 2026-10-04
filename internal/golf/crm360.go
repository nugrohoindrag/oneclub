package golf

import (
	"context"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/handle"
)

// GolfActivity is the golf section of the Customer 360 (FR-CRM-01).
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
	FlightID uuid.UUID `json:"flightId" db:"id"`
	FlightNo string    `json:"flightNo" db:"flight_no"`
	TeeTime  time.Time `json:"teeTime" db:"tee_time"`
	Route    string    `json:"route" db:"route_name"`
	Status   string    `json:"status" db:"status"`
}

// CustomerSection is the golf part of the Customer 360.
func (m *Module) CustomerSection(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (any, error) {
	a := GolfActivity{UpcomingFlights: []UpcomingFlight{}, FavoriteCaddies: []string{}}
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT p.flight_id)::int, max(f.tee_time) FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id
		WHERE p.customer_id = $1 AND p.status = 'finished'`, customer).Scan(&a.Rounds, &a.LastRound); err != nil {
		return a, err
	}
	_ = q.QueryRow(ctx, `SELECT local_index::text, official_index::text FROM golf.handicaps WHERE customer_id = $1`, customer).Scan(&a.HandicapIndex, &a.OfficialIndex)
	var err error
	if a.UpcomingFlights, err = handle.List[UpcomingFlight](q.Query(ctx, `SELECT f.id, f.flight_no, f.tee_time, r.name AS route_name, f.status FROM golf.flights f
		JOIN golf.playing_routes r ON r.id = f.route_id JOIN golf.flight_players p ON p.flight_id = f.id
		WHERE p.customer_id = $1 AND f.tee_time >= now() AND f.status IN ('booked', 'checked_in') ORDER BY f.tee_time LIMIT 5`, customer)); err != nil {
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
	loc := localNow(ctx, q).Location().String()
	if err := q.QueryRow(ctx, `SELECT count(*)::int,
		mode() WITHIN GROUP (ORDER BY extract(dow FROM f.tee_time AT TIME ZONE $2)::int),
		mode() WITHIN GROUP (ORDER BY extract(hour FROM f.tee_time AT TIME ZONE $2)::int)
		FROM golf.flight_players p JOIN golf.flights f ON f.id = p.flight_id
		WHERE p.customer_id = $1 AND p.status = 'finished' AND f.tee_time > now() - interval '12 months'`, customer, loc).Scan(&rounds, &dow, &hour); err != nil {
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
		JOIN golf.flight_players p ON p.flight_id = a.flight_id AND (a.player_id IS NULL OR a.player_id = p.id)
		WHERE p.customer_id = $1 AND a.status IN ('completed', 'replaced') GROUP BY c.code, c.name ORDER BY count(*) DESC LIMIT 1`, customer).Scan(&caddy)
	if caddy != nil {
		b.Facts["mostFrequentCaddy"] = *caddy
		b.Highlights = append(b.Highlights, "Most rounds with caddy "+*caddy)
	}
	return b, nil
}
