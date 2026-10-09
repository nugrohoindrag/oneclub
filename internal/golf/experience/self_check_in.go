package experience

// Self check-in (demo feedback 9 Oct 2026): a member checks in from the
// Member App once at the club — the phone's GPS within the course area of
// the course map — or a golfer scans the booking QR at the kiosk. The
// booking then waits at the front desk, which still picks the caddy and
// the golf cart; the desk can check players in by hand as before.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/golf"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// Self check-in limits.
const (
	// SelfCheckInMarginMeters is added around the course map: the clubhouse
	// and the car park are often just outside the course.
	SelfCheckInMarginMeters = 300
	// SelfCheckInAccuracyMeters is the worst GPS accuracy accepted.
	SelfCheckInAccuracyMeters = 300
)

// SelfCheckInInput is the member's position on arrival.
type SelfCheckInInput struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Accuracy  float64 `json:"accuracy,omitempty" doc:"GPS accuracy in meters"`
}

// SelfCheckInResult reports a self check-in.
type SelfCheckInResult struct {
	BookingID uuid.UUID   `json:"bookingId"`
	Code      string      `json:"code"`
	CheckedIn []uuid.UUID `json:"checkedIn"`
	Distance  int         `json:"distanceMeters" doc:"From the middle of the course"`
}

// SelfCheckIn checks the member's players of a booking in when the phone is
// at the club on the day of play.
func (m *Module) SelfCheckIn(ctx context.Context, tx pgx.Tx, property, bid, customer uuid.UUID, in SelfCheckInInput) (SelfCheckInResult, error) {
	var p uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.bookings WHERE id = $1`, bid).Scan(&p); err != nil || p != property {
		return SelfCheckInResult{}, errs.NotFound("booking")
	}
	b, err := golf.GetBooking(ctx, tx, bid)
	if err != nil {
		return SelfCheckInResult{}, err
	}
	var mine []uuid.UUID
	waiting := false
	for _, pl := range b.Players {
		if pl.CustomerID != nil && *pl.CustomerID == customer {
			mine = append(mine, pl.ID)
			waiting = waiting || pl.Status == "booked"
		}
	}
	if len(mine) == 0 {
		return SelfCheckInResult{}, errs.NotFound("booking")
	}
	if !waiting {
		return SelfCheckInResult{}, errs.Conflict("already_checked_in", "you are already checked in")
	}
	if today := localDay(clock.Now(), location(ctx, tx, property)).Format("2006-01-02"); b.PlayDate != today {
		return SelfCheckInResult{}, errs.Conflict("not_today", "self check-in opens on the day of play")
	}
	if in.Latitude == 0 && in.Longitude == 0 {
		return SelfCheckInResult{}, handle.Invalid("latitude", "required", "turn on the location (GPS) to check in")
	}
	if in.Accuracy > SelfCheckInAccuracyMeters {
		return SelfCheckInResult{}, handle.Invalid("accuracy", "inaccurate", fmt.Sprintf("the GPS position is too rough (±%.0f m); try again outside", in.Accuracy))
	}
	cm, err := loadCourseMap(ctx, tx, b.CourseID)
	if err != nil {
		return SelfCheckInResult{}, err
	}
	if !cm.ok {
		return SelfCheckInResult{}, errs.Conflict("self_check_in_unavailable", "self check-in is not available at this course; please check in at the front desk")
	}
	lat, lng := (cm.north+cm.south)/2, (cm.east+cm.west)/2
	radius := haversine(lat, lng, cm.north, cm.east) + SelfCheckInMarginMeters
	dist := haversine(lat, lng, in.Latitude, in.Longitude)
	if dist > radius {
		return SelfCheckInResult{}, errs.Conflict("not_at_club", fmt.Sprintf("you are %.1f km from the club; check in when you arrive", float64(dist)/1000))
	}
	res, err := m.Golf.CheckIn(ctx, tx, property, golf.CheckInRequest{BookingID: &bid, PlayerIDs: mine}, "self_app")
	if err != nil {
		return SelfCheckInResult{}, err
	}
	return SelfCheckInResult{BookingID: bid, Code: b.Code, CheckedIn: res.CheckedIn, Distance: dist}, nil
}

// SelfCheckInWaiting is a booking whose players checked in by themselves
// and still wait for the desk's caddy or golf cart.
type SelfCheckInWaiting struct {
	BookingID    uuid.UUID `json:"bookingId"`
	Code         string    `json:"code"`
	LocalTime    string    `json:"localTime"`
	ContactName  string    `json:"contactName"`
	Players      []string  `json:"players" doc:"Checked in by themselves"`
	Method       string    `json:"method" enum:"self_app,kiosk"`
	CheckedInAt  time.Time `json:"checkedInAt"`
	WaitingCaddy int       `json:"waitingCaddy" doc:"Checked-in players without a caddy"`
	WaitingCart  bool      `json:"waitingCart" doc:"A flight without a golf cart"`
}

// SelfCheckIns lists today's self check-ins waiting at the desk (first come
// first served).
func (m *Module) SelfCheckIns(ctx context.Context, q dbtx.Querier, property uuid.UUID, day string) ([]SelfCheckInWaiting, error) {
	type row struct {
		BookingID   uuid.UUID `db:"booking_id"`
		Players     []string  `db:"players"`
		Method      string    `db:"method"`
		CheckedInAt time.Time `db:"checked_in_at"`
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT p.booking_id, array_agg(p.name ORDER BY p.seq) AS players, min(p.check_in_method) AS method,
		min(p.checked_in_at) AS checked_in_at
		FROM golf.booking_players p JOIN golf.bookings b ON b.id = p.booking_id
		WHERE b.property_id = $1 AND b.play_date = $2::date AND p.status = 'checked_in' AND p.check_in_method IN ('self_app', 'kiosk')
		GROUP BY p.booking_id ORDER BY min(p.checked_in_at)`, property, day))
	if err != nil {
		return nil, err
	}
	out := []SelfCheckInWaiting{}
	for _, r := range rows {
		b, err := golf.GetBooking(ctx, q, r.BookingID)
		if err != nil {
			return nil, err
		}
		w := SelfCheckInWaiting{BookingID: b.ID, Code: b.Code, LocalTime: b.LocalTime, ContactName: b.ContactName, Players: r.Players, Method: r.Method,
			CheckedInAt: r.CheckedInAt}
		if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM golf.caddy_assignments a WHERE p.id = ANY(a.player_ids)
				AND a.status IN ('assigned', 'in_play', 'completed')))::int,
			EXISTS (SELECT 1 FROM golf.flights f WHERE f.booking_id = $1 AND f.status NOT IN ('cancelled', 'in_play', 'completed')
				AND NOT EXISTS (SELECT 1 FROM golf.golf_cart_assignments c WHERE c.flight_id = f.id AND c.status IN ('assigned', 'in_use')))
			FROM golf.booking_players p WHERE p.booking_id = $1 AND p.status = 'checked_in'`, b.ID).Scan(&w.WaitingCaddy, &w.WaitingCart); err != nil {
			return nil, err
		}
		if w.WaitingCaddy > 0 || w.WaitingCart {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *Module) registerSelfCheckIn(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	crm.MeRoute(reg, "golf", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/bookings/{id}:self-check-in",
		Summary: "Self check-in on arrival (GPS within the club's course area)", Request: SelfCheckInInput{}, Response: SelfCheckInResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SelfCheckInInput) (SelfCheckInResult, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return SelfCheckInResult{}, err
			}
			bid, err := handle.ID(r)
			if err != nil {
				return SelfCheckInResult{}, err
			}
			return m.SelfCheckIn(ctx, tx, handle.Property(ctx), bid, c.ID, in)
		})})
	add("Check-in", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/self-check-ins", Summary: "Self check-ins (Member App, kiosk) waiting for the desk's caddy and golf cart",
		Permission: "golf.check_in.perform", Response: SelfCheckInWaiting{}, List: true, Query: []route.Param{{Name: "date"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SelfCheckInWaiting], error) {
			day := r.URL.Query().Get("date")
			if day == "" {
				day = localDay(clock.Now(), location(ctx, tx, handle.Property(ctx))).Format("2006-01-02")
			}
			return handle.Page(m.SelfCheckIns(ctx, tx, handle.Property(ctx), day))
		})})
}
