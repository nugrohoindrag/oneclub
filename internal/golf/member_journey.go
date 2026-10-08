package golf

// Member journey of the Member App (Book → Arrive → Play → Pay → History):
// the fee estimate of a tee time per segment (Green Fee, Caddy, Golf Cart
// components), the caddies a member can prefer, the member's guests from
// earlier bookings, the caddy preference of each player (No Caddy /
// Request Caddy / Preferred Caddy) and the state of a booking per player
// (caddy requested vs assigned, check-in, scorecard).

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
)

// MemberQuote is the fee estimate of a tee time for the booking summary.
type MemberQuote struct {
	TeeTimeID uuid.UUID                         `json:"teeTimeId"`
	PlayDate  string                            `json:"playDate"`
	LocalTime string                            `json:"localTime"`
	Remaining int                               `json:"remaining"`
	Segments  map[string]commercial.PriceResult `json:"segments" doc:"member (or guest without Member Rate) and guest_of_member, with components"`
	Caddy     CaddyPolicy                       `json:"caddy"`
	Cart      CartPolicy                        `json:"cart"`
}

// MemberCaddy is a caddy a member can prefer.
type MemberCaddy struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Gender   *string   `json:"gender"`
	Level    *string   `json:"level"`
	Rating   *string   `json:"rating" doc:"Average of the players' ratings (1–5)"`
	Ratings  int       `json:"ratings"`
	Rounds   int       `json:"rounds" doc:"Completed rounds"`
	Favorite bool      `json:"favorite"`
	OnDuty   bool      `json:"onDuty" doc:"Present on the date (attendance); unknown for later days"`
}

// MyGuest is a guest the member brought before.
type MyGuest struct {
	Name       string  `json:"name"`
	Phone      *string `json:"phone"`
	Rounds     int     `json:"rounds"`
	LastPlayed string  `json:"lastPlayed"`
}

// CaddyRequestInput is the caddy preference of one player.
type CaddyRequestInput struct {
	PlayerID   uuid.UUID  `json:"playerId"`
	Preference string     `json:"preference" enum:"none,any,preferred"`
	CaddyID    *uuid.UUID `json:"caddyId,omitempty" doc:"Preferred caddy"`
}

// CaddyRequestsInput sets the caddy preferences of a booking's players.
type CaddyRequestsInput struct {
	Players []CaddyRequestInput `json:"players"`
}

// PlayerCaddy is the caddy assigned to a player.
type PlayerCaddy struct {
	AssignmentID uuid.UUID `json:"assignmentId"`
	CaddyID      uuid.UUID `json:"caddyId"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	Status       string    `json:"status" enum:"assigned,in_play,completed"`
	Accepted     bool      `json:"accepted"`
	Rated        bool      `json:"rated" doc:"I rated this caddy"`
}

// PlayerScore is the scorecard of a player.
type PlayerScore struct {
	ID     uuid.UUID `json:"id"`
	Gross  *int      `json:"gross"`
	Holes  int       `json:"holes"`
	Status string    `json:"status" enum:"draft,submitted,finalized"`
}

// PlayerJourney is the journey state of one player of a booking.
type PlayerJourney struct {
	PlayerID           uuid.UUID    `json:"playerId"`
	Preference         *string      `json:"caddyPreference" enum:"none,any,preferred"`
	PreferredCaddyID   *uuid.UUID   `json:"preferredCaddyId"`
	PreferredCaddyName *string      `json:"preferredCaddyName"`
	Caddy              *PlayerCaddy `json:"caddy" doc:"Set once the club assigned a caddy (a request is not an assignment)"`
	Scorecard          *PlayerScore `json:"scorecard"`
}

// BookingJourney is the per-player state of a member's booking.
type BookingJourney struct {
	BookingID uuid.UUID       `json:"bookingId"`
	Caddy     CaddyPolicy     `json:"caddy"`
	Players   []PlayerJourney `json:"players"`
}

func (m *Module) registerMemberJourney(reg *route.Registry) {
	mem := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Permission = "golf", "Member Portal", catalog.ShellMemberPortal
		reg.Add(rt)
	}
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/quote", Summary: "Fee estimate of a tee time (Green Fee, Caddy, Golf Cart)",
		Response: MemberQuote{}, Query: []route.Param{{Name: "teeTimeId", Required: true}}, Handler: m.memberQuote})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/caddies", Summary: "Caddies I can prefer (rating, rounds, favourite)",
		Response: MemberCaddy{}, List: true, Query: []route.Param{{Name: "date", Description: "YYYY-MM-DD: on duty that day"}}, Handler: m.memberCaddies})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/guests", Summary: "My Guests (from my earlier bookings)",
		Response: MyGuest{}, List: true, Handler: m.memberGuests})
	mem(route.Route{Method: http.MethodPut, Path: "/api/v1/member/bookings/{id}/caddy-requests", Summary: "Caddy preference per player (No Caddy, Request, Preferred)",
		Request: CaddyRequestsInput{}, Response: BookingJourney{}, Handler: m.memberCaddyRequests})
	mem(route.Route{Method: http.MethodGet, Path: "/api/v1/member/bookings/{id}/journey", Summary: "My booking per player: caddy, check-in, scorecard",
		Response: BookingJourney{}, Handler: m.memberJourney})
}

func (m *Module) memberQuote(w http.ResponseWriter, r *http.Request) {
	tid, err := uuid.Parse(r.URL.Query().Get("teeTimeId"))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("invalid_tee_time", "teeTimeId must be a UUID"))
		return
	}
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		loc := location(ctx, tx, mc.property)
		slots, err := loadSlots(ctx, tx, loc, "t.property_id = $1 AND t.id = $2", mc.property, tid)
		if err != nil {
			return nil, err
		}
		if len(slots) == 0 {
			return nil, errs.NotFound("tee time")
		}
		s := slots[0]
		day, err := time.ParseInLocation("2006-01-02", s.PlayDate, loc)
		if err != nil {
			return nil, err
		}
		pol, err := LoadPolicies(ctx, tx, mc.property, clock.Now())
		if err != nil {
			return nil, err
		}
		self := "member"
		if !mc.standing.Privileges.MemberRate {
			self = "guest"
		}
		out := MemberQuote{TeeTimeID: s.ID, PlayDate: s.PlayDate, LocalTime: s.LocalTime, Remaining: s.Remaining, Segments: map[string]commercial.PriceResult{},
			Caddy: pol.Caddy, Cart: pol.Cart}
		// the segments as a booking prices its players (guest of member falls back to guest)
		for seg, chain := range map[string][]string{self: {self}, "guest_of_member": {"guest_of_member", "guest"}} {
			peak := s.Peak
			res, err := commercial.Resolve(ctx, tx, commercial.PriceQuery{Property: mc.property, Segments: chain, PlayAt: s.StartAt, PlayDate: day,
				LocalTime: s.LocalTime, Session: s.Session, DayTypeCode: s.DayTypeCode, PlayingRouteID: s.PlayingRouteID, Channel: "member_app", Peak: &peak})
			if err == nil {
				out.Segments[seg] = res
			}
		}
		return out, nil
	})
}

func (m *Module) memberCaddies(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		day := localDay(clock.Now(), location(ctx, tx, mc.property))
		if v := r.URL.Query().Get("date"); v != "" {
			d, err := parseDate(v)
			if err != nil {
				return nil, err
			}
			day = d
		}
		rows, err := tx.Query(ctx, `SELECT c.id, c.code, c.name, c.gender, lv.name,
			(SELECT trim_scale(round(avg(cr.rating), 1))::text FROM golf.caddy_ratings cr WHERE cr.caddy_id = c.id),
			(SELECT count(*) FROM golf.caddy_ratings cr WHERE cr.caddy_id = c.id)::int,
			(SELECT count(*) FROM golf.caddy_assignments a WHERE a.caddy_id = c.id AND a.status = 'completed')::int,
			EXISTS (SELECT 1 FROM golf.caddy_favorites f WHERE f.caddy_id = c.id AND f.customer_id = $2),
			EXISTS (SELECT 1 FROM golf.caddy_attendance at WHERE at.caddy_id = c.id AND at.work_date = $3::date AND at.status = 'present')
			FROM golf.caddies c LEFT JOIN golf.caddy_profiles p ON p.caddy_id = c.id LEFT JOIN golf.caddy_levels lv ON lv.id = p.level_id
			WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL`, mc.property, mc.customerID, day.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []MemberCaddy{}
		for rows.Next() {
			var c MemberCaddy
			if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.Gender, &c.Level, &c.Rating, &c.Ratings, &c.Rounds, &c.Favorite, &c.OnDuty); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// favourites first, then the best rated, then the most experienced
		rating := func(c MemberCaddy) float64 {
			if c.Rating == nil {
				return 0
			}
			f, _ := strconv.ParseFloat(*c.Rating, 64)
			return f
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.Favorite != b.Favorite {
				return a.Favorite
			}
			if rating(a) != rating(b) {
				return rating(a) > rating(b)
			}
			if a.Rounds != b.Rounds {
				return a.Rounds > b.Rounds
			}
			return a.Code < b.Code
		})
		return httpx.Page[MemberCaddy]{Items: out}, nil
	})
}

func (m *Module) memberGuests(w http.ResponseWriter, r *http.Request) {
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		rows, err := tx.Query(ctx, `SELECT min(bp.name), max(bp.phone), count(*)::int, max(b.play_date)::text
			FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id
			WHERE b.member_id = $1 AND bp.player_type = 'guest_of_member' AND NOT bp.tba AND bp.status NOT IN ('removed', 'cancelled')
			AND b.status NOT IN ('draft', 'cancelled') AND trim(bp.name) <> ''
			GROUP BY lower(trim(bp.name)) ORDER BY max(b.play_date) DESC LIMIT 50`, mc.memberID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []MyGuest{}
		for rows.Next() {
			var g MyGuest
			if err := rows.Scan(&g.Name, &g.Phone, &g.Rounds, &g.LastPlayed); err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return httpx.Page[MyGuest]{Items: out}, rows.Err()
	})
}

func (m *Module) memberCaddyRequests(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	req, ok := decode[CaddyRequestsInput](w, r)
	if !ok {
		return
	}
	m.memberWrite(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM golf.bookings WHERE id = $1 FOR UPDATE`, bid).Scan(&status); err != nil {
			return nil, errs.NotFound("booking")
		}
		if status != "draft" && status != "pending" && status != "confirmed" {
			return nil, errs.Conflict("booking_closed", "caddy preferences can be changed until check-in")
		}
		pol, err := LoadPolicies(ctx, tx, mc.property, clock.Now())
		if err != nil {
			return nil, err
		}
		for i, p := range req.Players {
			field := fmt.Sprintf("players[%d]", i)
			switch p.Preference {
			case "none":
				if pol.Caddy.Mandatory {
					return nil, errs.Validation("caddy_mandatory", "a caddy is mandatory (Caddy Policy)", errs.Field(field+".preference", "invalid", "caddy mandatory"))
				}
			case "any":
			case "preferred":
				if !pol.Caddy.AllowRequest {
					return nil, errs.Validation("caddy_request_not_allowed", "the club assigns the caddies (Caddy Policy)",
						errs.Field(field+".preference", "invalid", "no preferred caddy"))
				}
				if p.CaddyID == nil {
					return nil, errs.Validation("caddy_required", "choose the preferred caddy", errs.Field(field+".caddyId", "required", "caddy"))
				}
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddies WHERE id = $1 AND property_id = $2 AND status = 'active' AND archived_at IS NULL)`,
					*p.CaddyID, mc.property).Scan(&ok); err != nil || !ok {
					return nil, errs.Validation("caddy_not_found", "caddy not found", errs.Field(field+".caddyId", "not_found", "caddy"))
				}
			default:
				return nil, errs.Validation("invalid_preference", "preference is none, any or preferred", errs.Field(field+".preference", "invalid", "none, any or preferred"))
			}
			caddy := p.CaddyID
			if p.Preference != "preferred" {
				caddy = nil
			}
			tag, err := tx.Exec(ctx, `INSERT INTO golf.player_caddy_requests (booking_player_id, property_id, booking_id, preference, caddy_id, created_by, updated_by)
				SELECT bp.id, $2, bp.booking_id, $4, $5, $6, $6 FROM golf.booking_players bp WHERE bp.id = $1 AND bp.booking_id = $3 AND bp.status <> 'removed'
				ON CONFLICT (booking_player_id) DO UPDATE SET preference = EXCLUDED.preference, caddy_id = EXCLUDED.caddy_id, updated_by = EXCLUDED.updated_by`,
				p.PlayerID, mc.property, bid, p.Preference, caddy, id.Ptr(actor(ctx)))
			if err != nil {
				return nil, err
			}
			if tag.RowsAffected() == 0 {
				return nil, errs.Validation("invalid_player", "player not in this booking", errs.Field(field+".playerId", "invalid", "player of this booking"))
			}
		}
		// the booking's free-text caddy request names the first preferred caddy (Tee Sheet, Caddy Master)
		if _, err := tx.Exec(ctx, `UPDATE golf.bookings b SET caddy_request = coalesce((SELECT c.code || ' ' || c.name FROM golf.player_caddy_requests r
			JOIN golf.caddies c ON c.id = r.caddy_id JOIN golf.booking_players bp ON bp.id = r.booking_player_id WHERE r.booking_id = b.id ORDER BY bp.seq LIMIT 1),
			CASE WHEN EXISTS (SELECT 1 FROM golf.player_caddy_requests r WHERE r.booking_id = b.id AND r.preference = 'none') THEN 'see player caddy requests' END)
			WHERE b.id = $1`, bid); err != nil {
			return nil, err
		}
		out, err := m.bookingJourney(ctx, tx, mc, bid, pol.Caddy)
		if err != nil {
			return nil, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "caddy_request", EntityType: "golf.booking", EntityID: bid.String(),
			PropertyID: &mc.property, After: req})
	})
}

func (m *Module) memberJourney(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.memberRead(w, r, func(ctx context.Context, tx pgx.Tx, mc memberCtx) (any, error) {
		if err := m.memberOwnsBooking(ctx, tx, mc, bid); err != nil {
			return nil, err
		}
		pol, err := LoadPolicies(ctx, tx, mc.property, clock.Now())
		if err != nil {
			return nil, err
		}
		return m.bookingJourney(ctx, tx, mc, bid, pol.Caddy)
	})
}

func (m *Module) bookingJourney(ctx context.Context, tx pgx.Tx, mc memberCtx, bid uuid.UUID, pol CaddyPolicy) (BookingJourney, error) {
	out := BookingJourney{BookingID: bid, Caddy: pol, Players: []PlayerJourney{}}
	rows, err := tx.Query(ctx, `SELECT bp.id, r.preference, r.caddy_id, pc.code || ' ' || pc.name,
		a.id, a.caddy_id, c.code, c.name, a.status, acc.assignment_id IS NOT NULL,
		EXISTS (SELECT 1 FROM golf.caddy_ratings cr WHERE cr.assignment_id = a.id AND cr.customer_id = $2),
		s.id, s.gross, s.holes, s.status
		FROM golf.booking_players bp
		LEFT JOIN golf.player_caddy_requests r ON r.booking_player_id = bp.id
		LEFT JOIN golf.caddies pc ON pc.id = r.caddy_id
		LEFT JOIN LATERAL (SELECT x.id, x.caddy_id, x.status FROM golf.caddy_assignments x WHERE bp.id = ANY (x.player_ids)
		  AND x.status IN ('assigned', 'in_play', 'completed') ORDER BY x.assigned_at DESC LIMIT 1) a ON true
		LEFT JOIN golf.caddies c ON c.id = a.caddy_id
		LEFT JOIN golf.caddy_assignment_acceptances acc ON acc.assignment_id = a.id
		LEFT JOIN golf.scorecards s ON s.booking_player_id = bp.id
		WHERE bp.booking_id = $1 AND bp.status <> 'removed' ORDER BY bp.seq`, bid, mc.customerID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PlayerJourney
		var aid, cid, sid *uuid.UUID
		var code, name, astatus, sstatus *string
		var accepted, rated bool
		var gross, holes *int
		if err := rows.Scan(&p.PlayerID, &p.Preference, &p.PreferredCaddyID, &p.PreferredCaddyName, &aid, &cid, &code, &name, &astatus, &accepted, &rated,
			&sid, &gross, &holes, &sstatus); err != nil {
			return out, err
		}
		if aid != nil {
			p.Caddy = &PlayerCaddy{AssignmentID: *aid, CaddyID: *cid, Code: *code, Name: *name, Status: *astatus, Accepted: accepted, Rated: rated}
		}
		if sid != nil {
			p.Scorecard = &PlayerScore{ID: *sid, Gross: gross, Holes: *holes, Status: *sstatus}
		}
		out.Players = append(out.Players, p)
	}
	return out, rows.Err()
}

// playerCaddyRequests returns the caddy preferences of a flight's players.
func playerCaddyRequests(ctx context.Context, tx pgx.Tx, players []uuid.UUID) (map[uuid.UUID]string, map[uuid.UUID]uuid.UUID, error) {
	pref, caddy := map[uuid.UUID]string{}, map[uuid.UUID]uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT booking_player_id, preference, caddy_id FROM golf.player_caddy_requests WHERE booking_player_id = ANY ($1)`, players)
	if err != nil {
		return pref, caddy, err
	}
	defer rows.Close()
	for rows.Next() {
		var p uuid.UUID
		var s string
		var c *uuid.UUID
		if err := rows.Scan(&p, &s, &c); err != nil {
			return pref, caddy, err
		}
		pref[p] = s
		if c != nil {
			caddy[p] = *c
		}
	}
	return pref, caddy, rows.Err()
}
