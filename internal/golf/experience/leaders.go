package experience

// Score boards from the scorecards (demo feedback 9 Oct 2026), ranked on
// the score, never on the play time:
//   - Weekly Hole Leader / Monthly Hole Record: per hole the lowest strokes
//     of the running week (Monday–Sunday) or month, with every player who
//     shares it;
//   - Top Player of the week / month: the lowest 18-hole gross of each
//     player in the period;
//   - the best score per hole within one booking (booking detail).
// Shown on the Management dashboard and in the Member App.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// HoleLeaderPlayer is a player holding the best score of a hole.
type HoleLeaderPlayer struct {
	Name        string     `json:"name" db:"player_name"`
	CustomerID  *uuid.UUID `json:"customerId" db:"customer_id"`
	PlayedOn    time.Time  `json:"playedOn" db:"played_on"`
	ScorecardID uuid.UUID  `json:"scorecardId" db:"scorecard_id"`
}

// HoleLeader is the best score of one hole in a period.
type HoleLeader struct {
	HoleID      uuid.UUID          `json:"holeId"`
	HoleNumber  int                `json:"holeNumber"`
	SectionCode string             `json:"sectionCode"`
	CourseName  string             `json:"courseName"`
	Par         int                `json:"par"`
	Strokes     int                `json:"strokes" doc:"Lowest strokes on the hole"`
	ToPar       int                `json:"toPar" doc:"Strokes − par (−2 eagle, −1 birdie …)"`
	Players     []HoleLeaderPlayer `json:"players" doc:"Everyone sharing the best score, earliest first"`
}

// TopPlayer is a player of the period ranked by the best 18-hole gross.
type TopPlayer struct {
	Rank       int        `json:"rank" db:"rank"`
	Name       string     `json:"name" db:"player_name"`
	CustomerID *uuid.UUID `json:"customerId" db:"customer_id"`
	Gross      int        `json:"gross" db:"gross"`
	ToPar      int        `json:"toPar" db:"to_par"`
	PlayedOn   time.Time  `json:"playedOn" db:"played_on"`
	Rounds     int        `json:"rounds" db:"rounds" doc:"18-hole rounds in the period"`
}

// ScoreBoard is the score board of a period.
type ScoreBoard struct {
	Period     string       `json:"period" enum:"week,month"`
	From       string       `json:"from"`
	To         string       `json:"to"`
	HoleLeader []HoleLeader `json:"holeLeaders"`
	TopPlayers []TopPlayer  `json:"topPlayers"`
}

// periodOf is the running week (Monday–Sunday) or month of a day.
func periodOf(period string, day time.Time) (time.Time, time.Time, error) {
	switch period {
	case "", "week":
		wd := (int(day.Weekday()) + 6) % 7 // Monday = 0
		from := day.AddDate(0, 0, -wd)
		return from, from.AddDate(0, 0, 6), nil
	case "month":
		from := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 1, -1), nil
	}
	return time.Time{}, time.Time{}, handle.Invalid("period", "invalid", "week or month")
}

// Leaders builds the hole leaders and top players of a period.
func (m *Module) Leaders(ctx context.Context, q dbtx.Querier, property uuid.UUID, period string, day time.Time, course *uuid.UUID) (ScoreBoard, error) {
	from, to, err := periodOf(period, day)
	if err != nil {
		return ScoreBoard{}, err
	}
	if period == "" {
		period = "week"
	}
	out := ScoreBoard{Period: period, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), HoleLeader: []HoleLeader{}, TopPlayers: []TopPlayer{}}
	rows, err := q.Query(ctx, `WITH s AS (
		  SELECT h.hole_id, h.hole_number, h.section_code, h.par, h.strokes, sc.player_name, sc.customer_id, sc.played_on, sc.id AS scorecard_id, c.name AS course_name,
		    rank() OVER (PARTITION BY h.hole_id ORDER BY h.strokes) AS rk
		  FROM golf.scorecard_holes h JOIN golf.scorecards sc ON sc.id = h.scorecard_id JOIN golf.holes ho ON ho.id = h.hole_id JOIN golf.courses c ON c.id = ho.course_id
		  WHERE sc.property_id = $1 AND sc.played_on BETWEEN $2::date AND $3::date AND h.strokes IS NOT NULL AND ($4::uuid IS NULL OR ho.course_id = $4))
		SELECT hole_id, hole_number, section_code, course_name, par, strokes, player_name, customer_id, played_on, scorecard_id FROM s WHERE rk = 1
		ORDER BY course_name, section_code, hole_number, played_on, player_name`, property, out.From, out.To, course)
	if err != nil {
		return out, err
	}
	idx := map[uuid.UUID]int{}
	for rows.Next() {
		var h HoleLeader
		var p HoleLeaderPlayer
		if err := rows.Scan(&h.HoleID, &h.HoleNumber, &h.SectionCode, &h.CourseName, &h.Par, &h.Strokes, &p.Name, &p.CustomerID, &p.PlayedOn, &p.ScorecardID); err != nil {
			rows.Close()
			return out, err
		}
		i, ok := idx[h.HoleID]
		if !ok {
			h.ToPar, h.Players = h.Strokes-h.Par, []HoleLeaderPlayer{}
			out.HoleLeader = append(out.HoleLeader, h)
			i = len(out.HoleLeader) - 1
			idx[h.HoleID] = i
		}
		if len(out.HoleLeader[i].Players) < 5 {
			out.HoleLeader[i].Players = append(out.HoleLeader[i].Players, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.TopPlayers, err = handle.List[TopPlayer](q.Query(ctx, `WITH r AS (
		  SELECT coalesce(sc.customer_id::text, lower(sc.player_name)) AS who, sc.player_name, sc.customer_id, sc.gross, sc.gross - sc.par AS to_par, sc.played_on,
		    row_number() OVER (PARTITION BY coalesce(sc.customer_id::text, lower(sc.player_name)) ORDER BY sc.gross, sc.played_on) AS n,
		    count(*) OVER (PARTITION BY coalesce(sc.customer_id::text, lower(sc.player_name))) AS rounds
		  FROM golf.scorecards sc
		  WHERE sc.property_id = $1 AND sc.played_on BETWEEN $2::date AND $3::date AND sc.holes = 18 AND sc.gross IS NOT NULL
		    AND NOT EXISTS (SELECT 1 FROM golf.scorecard_holes h WHERE h.scorecard_id = sc.id AND h.strokes IS NULL)
		    AND ($4::uuid IS NULL OR EXISTS (SELECT 1 FROM golf.playing_routes pr WHERE pr.id = sc.playing_route_id AND pr.course_id = $4)))
		SELECT rank() OVER (ORDER BY gross, played_on)::int AS rank, player_name, customer_id, gross, to_par, played_on, rounds::int AS rounds
		FROM r WHERE n = 1 ORDER BY gross, played_on LIMIT 10`, property, out.From, out.To, course))
	return out, err
}

// HoleBest is the best score of a hole within one booking.
type HoleBest struct {
	Seq         int      `json:"seq" db:"seq"`
	HoleNumber  int      `json:"holeNumber" db:"hole_number"`
	SectionCode string   `json:"sectionCode" db:"section_code"`
	Par         int      `json:"par" db:"par"`
	Strokes     int      `json:"strokes" db:"strokes"`
	Players     []string `json:"players" db:"players"`
}

// BookingHoleBests lists the best score per hole among the players of a
// booking (booking detail).
func (m *Module) BookingHoleBests(ctx context.Context, q dbtx.Querier, booking uuid.UUID) ([]HoleBest, error) {
	return handle.List[HoleBest](q.Query(ctx, `WITH s AS (
		  SELECT h.seq, h.hole_number, h.section_code, h.par, h.strokes, sc.player_name, rank() OVER (PARTITION BY h.seq ORDER BY h.strokes) AS rk
		  FROM golf.scorecard_holes h JOIN golf.scorecards sc ON sc.id = h.scorecard_id JOIN golf.booking_players bp ON bp.id = sc.booking_player_id
		  WHERE bp.booking_id = $1 AND h.strokes IS NOT NULL)
		SELECT seq, max(hole_number) AS hole_number, max(section_code) AS section_code, max(par) AS par, min(strokes) AS strokes,
		  array_agg(player_name ORDER BY player_name) AS players
		FROM s WHERE rk = 1 GROUP BY seq ORDER BY seq`, booking))
}

func leaderParams(ctx context.Context, q dbtx.Querier, r *http.Request) (string, time.Time, *uuid.UUID, error) {
	day := localNow(ctx, q)
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	if v := r.URL.Query().Get("date"); v != "" {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			return "", day, nil, handle.Invalid("date", "invalid", "YYYY-MM-DD")
		}
		day = d
	}
	course, err := handle.QueryUUID(r, "courseId")
	return r.URL.Query().Get("period"), day, course, err
}

func (m *Module) registerLeaders(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	params := []route.Param{{Name: "period", Enum: []string{"week", "month"}}, {Name: "date"}, {Name: "courseId"}}
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/leaderboard", Summary: "Weekly Hole Leader / Monthly Hole Record and Top Players (by score)",
		Permission: "golf.scorecard.view", Response: ScoreBoard{}, Query: params,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ScoreBoard, error) {
			period, day, course, err := leaderParams(ctx, tx, r)
			if err != nil {
				return ScoreBoard{}, err
			}
			return m.Leaders(ctx, tx, handle.Property(ctx), period, day, course)
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/bookings/{id}/hole-bests", Summary: "Best score per hole among the players of a booking",
		Permission: "golf.booking.view", Response: HoleBest{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[HoleBest], error) {
			bid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[HoleBest]{}, err
			}
			out, err := m.BookingHoleBests(ctx, tx, bid)
			return httpx.Page[HoleBest]{Items: out}, err
		})})
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/leaderboard", Summary: "Weekly Hole Leader / Monthly Hole Record and Top Players",
		Response: ScoreBoard{}, Query: params,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (ScoreBoard, error) {
			period, day, course, err := leaderParams(ctx, tx, r)
			if err != nil {
				return ScoreBoard{}, err
			}
			return m.Leaders(ctx, tx, handle.Property(ctx), period, day, course)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/bookings/{id}/hole-bests", Summary: "Best score per hole in my booking",
		Response: HoleBest{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[HoleBest], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[HoleBest]{}, err
			}
			bid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[HoleBest]{}, err
			}
			var mine bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.bookings b WHERE b.id = $1 AND (b.customer_id = $2
				OR EXISTS (SELECT 1 FROM golf.booking_players bp WHERE bp.booking_id = b.id AND bp.customer_id = $2)))`, bid, c.ID).Scan(&mine); err != nil {
				return httpx.Page[HoleBest]{}, err
			}
			if !mine {
				return httpx.Page[HoleBest]{}, errs.NotFound("booking")
			}
			out, err := m.BookingHoleBests(ctx, tx, bid)
			return httpx.Page[HoleBest]{Items: out}, err
		})})
}
