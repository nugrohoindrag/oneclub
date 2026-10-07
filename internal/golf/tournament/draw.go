package tournament

// FR-TRN-05 Flighting (automatic by handicap / division / random /
// standings, manual moves, sponsor guests paired), FR-TRN-06 Tee Assignment:
// sequential tee times (one or two tees) or Shotgun Start — every flight
// starts on its own hole at the same time, A/B groups when there are more
// flights than holes (B groups on par 5s and par 4s first) — with the start
// sheet (print), publication to the players, the shotgun at the Starter
// and the tee-off of flights.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/golf"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/platform/handle"
)

// FlightPlayer is a player of a flight.
type TournamentFlightPlayer struct {
	FlightID        uuid.UUID  `json:"-" db:"flight_id"`
	RegistrationID  uuid.UUID  `json:"registrationId" db:"registration_id"`
	Number          string     `json:"number" db:"number"`
	PlayerName      string     `json:"playerName" db:"player_name"`
	Position        int        `json:"position" db:"position"`
	HandicapIndex   *string    `json:"handicapIndex" db:"handicap_index"`
	PlayingHandicap *int       `json:"playingHandicap" db:"playing_handicap"`
	DivisionName    *string    `json:"divisionName" db:"division_name"`
	PlayerType      string     `json:"playerType" db:"player_type"`
	Status          string     `json:"status" db:"status" doc:"Registration status"`
	CaddyID         *uuid.UUID `json:"caddyId" db:"caddy_id"`
	CaddyCode       *string    `json:"caddyCode" db:"caddy_code"`
	CaddyName       *string    `json:"caddyName" db:"caddy_name"`
	ScoreID         *uuid.UUID `json:"scoreId" db:"score_id"`
	ScoreStatus     *string    `json:"scoreStatus" db:"score_status"`
}

// TournamentFlight is a flight of a round with its start.
type TournamentFlight struct {
	ID         uuid.UUID                `json:"id" db:"id"`
	RoundNo    int                      `json:"roundNo" db:"round_no"`
	FlightNo   int                      `json:"flightNo" db:"flight_no"`
	StartHole  int                      `json:"startHole" db:"start_hole" doc:"Sequence of the start hole on the playing route"`
	StartGroup *string                  `json:"startGroup" db:"start_group" enum:"A,B"`
	StartLabel string                   `json:"startLabel" db:"-" doc:"Hole number and group, e.g. 7A"`
	StartAt    time.Time                `json:"startAt" db:"start_at"`
	LocalTime  string                   `json:"localTime" db:"-"`
	Status     string                   `json:"status" db:"status" enum:"scheduled,in_play,completed"`
	TeeOffAt   *time.Time               `json:"teeOffAt" db:"tee_off_at"`
	Players    []TournamentFlightPlayer `json:"players" db:"-"`
}

// TournamentDraw is the draw of a round.
type TournamentDraw struct {
	RoundNo   int                `json:"roundNo"`
	Status    string             `json:"status" enum:"scheduled,drawn,published,in_progress,completed"`
	Method    *string            `json:"method"`
	StartType string             `json:"startType" enum:"shotgun,tee_times"`
	Flights   []TournamentFlight `json:"flights"`
	Excluded  []string           `json:"excluded" doc:"Players not drawn: missed the cut, withdrawn or disqualified"`
}

func roundByNo(ctx context.Context, q dbtx.Querier, tid uuid.UUID, no int) (TournamentRound, error) {
	rows, err := q.Query(ctx, roundSelect+` WHERE x.tournament_id = $1 AND x.round_no = $2`, tid, no)
	return handle.One[TournamentRound](rows, err, "round")
}

// routeOf is the playing route of a round.
func routeOf(t Tournament, r TournamentRound) uuid.UUID {
	if r.PlayingRouteID != nil {
		return *r.PlayingRouteID
	}
	return t.PlayingRouteID
}

// flights loads the flights of a round with their players.
func flights(ctx context.Context, q dbtx.Querier, property, roundID uuid.UUID, holes []golf.RouteHole) ([]TournamentFlight, error) {
	fs, err := handle.List[TournamentFlight](q.Query(ctx, `SELECT f.id, x.round_no, f.flight_no, f.start_hole, f.start_group, f.start_at, f.status, f.tee_off_at
		FROM golf.tournament_flights f JOIN golf.tournament_rounds x ON x.id = f.round_id WHERE f.round_id = $1
		ORDER BY f.start_at, f.start_hole, f.start_group NULLS FIRST, f.flight_no`, roundID))
	if err != nil {
		return nil, err
	}
	ps, err := handle.List[TournamentFlightPlayer](q.Query(ctx, `SELECT fp.flight_id, fp.registration_id, r.number, r.player_name, fp.position,
		trim_scale(coalesce(s.handicap_index, r.handicap_index))::text AS handicap_index, s.playing_handicap, d.name AS division_name, r.player_type, r.status,
		fp.caddy_id, c.code AS caddy_code, c.name AS caddy_name, s.id AS score_id, s.status AS score_status
		FROM golf.tournament_flight_players fp JOIN golf.tournament_registrations r ON r.id = fp.registration_id
		LEFT JOIN golf.tournament_divisions d ON d.id = r.division_id LEFT JOIN golf.caddies c ON c.id = fp.caddy_id
		LEFT JOIN golf.tournament_scores s ON s.round_id = fp.round_id AND s.registration_id = fp.registration_id
		WHERE fp.round_id = $1 ORDER BY fp.flight_id, fp.position`, roundID))
	if err != nil {
		return nil, err
	}
	loc := location(ctx, q, property)
	numberAt := map[int]int{}
	for _, h := range holes {
		numberAt[h.Sequence] = h.Number
	}
	byFlight := map[uuid.UUID][]TournamentFlightPlayer{}
	for _, p := range ps {
		byFlight[p.FlightID] = append(byFlight[p.FlightID], p)
	}
	for i := range fs {
		n := numberAt[fs[i].StartHole]
		if n == 0 {
			n = fs[i].StartHole
		}
		fs[i].StartLabel = startLabel(n, fs[i].StartGroup)
		fs[i].LocalTime = fs[i].StartAt.In(loc).Format("15:04")
		fs[i].Players = byFlight[fs[i].ID]
		if fs[i].Players == nil {
			fs[i].Players = []TournamentFlightPlayer{}
		}
	}
	return fs, nil
}

// Draw returns the draw of a round.
func (m *Module) Draw(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, roundNo int) (TournamentDraw, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentDraw{}, err
	}
	if roundNo == 0 {
		roundNo = t.CurrentRound
	}
	r, err := roundByNo(ctx, q, tid, roundNo)
	if err != nil {
		return TournamentDraw{}, err
	}
	rh, err := golf.LoadRouteHoles(ctx, q, routeOf(t, r))
	if err != nil {
		return TournamentDraw{}, err
	}
	fs, err := flights(ctx, q, property, r.ID, rh.Holes)
	return TournamentDraw{RoundNo: r.RoundNo, Status: r.Status, Method: r.DrawMethod, StartType: t.StartType, Flights: fs, Excluded: []string{}}, err
}

// DrawInput generates the draw of a round.
type TournamentDrawInput struct {
	Round            int    `json:"round,omitempty" doc:"Default: the current round"`
	Method           string `json:"method,omitempty" enum:"handicap,division,random,standings" doc:"Default: handicap (round 1), standings (later rounds)"`
	PlayersPerFlight int    `json:"playersPerFlight,omitempty" doc:"Default: the tournament's"`
	KeepPairings     *bool  `json:"keepPairings,omitempty" doc:"Keep pairing groups (sponsor guests, friends) together; default true"`
}

type drawPlayer struct {
	id       uuid.UUID
	name     string
	hcp      *decimal.Decimal
	division int
	group    string
	rank     int
	at       time.Time
}

// GenerateDraw makes the flights of a round and assigns the starts
// (FR-TRN-05/06). An unpublished or published (not started) draw is
// replaced.
func (m *Module) GenerateDraw(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentDrawInput) (TournamentDraw, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDraw{}, err
	}
	if t.Status != "open" && t.Status != "closed" && t.Status != "in_progress" {
		return TournamentDraw{}, errs.Conflict("invalid_status", "the draw is made for an open, closed or running tournament, not "+t.Status)
	}
	roundNo := in.Round
	if roundNo == 0 {
		roundNo = t.CurrentRound
		if cur, err := roundByNo(ctx, tx, tid, roundNo); err == nil && (cur.Status == "in_progress" || cur.Status == "completed") {
			roundNo++
		}
	}
	r, err := roundByNo(ctx, tx, tid, roundNo)
	if err != nil {
		return TournamentDraw{}, err
	}
	if r.Status == "in_progress" || r.Status == "completed" {
		return TournamentDraw{}, errs.Conflict("round_started", fmt.Sprintf("round %d has started; move players instead", roundNo))
	}
	if roundNo > 1 {
		prev, err := roundByNo(ctx, tx, tid, roundNo-1)
		if err != nil {
			return TournamentDraw{}, err
		}
		if prev.Status != "completed" {
			return TournamentDraw{}, errs.Conflict("previous_round_open", fmt.Sprintf("round %d is not completed yet", roundNo-1))
		}
	}
	method := in.Method
	if method == "" {
		method = "handicap"
		if roundNo > 1 {
			method = "standings"
		}
	}
	if err := oneOf("method", method, "handicap", "division", "random", "standings"); err != nil {
		return TournamentDraw{}, err
	}
	ppf := in.PlayersPerFlight
	if ppf == 0 {
		ppf = t.PlayersPerFlight
	}
	if ppf < 1 || ppf > 5 {
		return TournamentDraw{}, handle.Invalid("playersPerFlight", "invalid", "1–5 players per flight")
	}
	keep := in.KeepPairings == nil || *in.KeepPairings
	excluded := []string{}
	ranks := map[uuid.UUID]int{}
	if roundNo > 1 {
		lb, err := m.compute(ctx, tx, property, t, computeOptions{})
		if err != nil {
			return TournamentDraw{}, err
		}
		primary := primaryCategory(t)
		for _, b := range lb.Boards {
			if b.Category != primary || b.DivisionID != nil {
				continue
			}
			for i, e := range b.Entries {
				if e.RegistrationID != nil {
					ranks[*e.RegistrationID] = i + 1
				}
			}
			// the cut after cutAfterRound: top N and ties (FR P3 §5.2 multi-round)
			if t.CutAfterRound != nil && t.CutTop != nil && roundNo == *t.CutAfterRound+1 {
				for _, e := range b.Entries {
					if e.RegistrationID == nil || e.Status == "dq" || e.Status == "wd" || e.Status == "nr" {
						continue
					}
					made := e.Position != nil && *e.Position <= *t.CutTop
					if _, err := tx.Exec(ctx, `UPDATE golf.tournament_registrations SET made_cut = $2 WHERE id = $1`, *e.RegistrationID, made); err != nil {
						return TournamentDraw{}, err
					}
				}
			}
		}
	}
	// players: registered / checked in, made the cut, not out of the tournament
	rows, err := tx.Query(ctx, `SELECT r.id, r.player_name, r.handicap_index::text, coalesce(d.sequence, 999), coalesce(r.pairing_group, ''), r.registered_at,
		r.made_cut, EXISTS (SELECT 1 FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id
		  WHERE s.registration_id = r.id AND x.round_no < $2 AND s.status IN ('dq', 'wd', 'nr'))
		FROM golf.tournament_registrations r LEFT JOIN golf.tournament_divisions d ON d.id = r.division_id
		WHERE r.tournament_id = $1 AND r.status IN ('registered', 'checked_in') ORDER BY r.registered_at`, tid, roundNo)
	if err != nil {
		return TournamentDraw{}, err
	}
	var players []drawPlayer
	for rows.Next() {
		var p drawPlayer
		var hcp *string
		var made *bool
		var out bool
		if err := rows.Scan(&p.id, &p.name, &hcp, &p.division, &p.group, &p.at, &made, &out); err != nil {
			rows.Close()
			return TournamentDraw{}, err
		}
		if out || (made != nil && !*made) || (roundNo > 1 && ranks[p.id] == 0) {
			excluded = append(excluded, p.name)
			continue
		}
		if hcp != nil {
			d := dec(*hcp)
			p.hcp = &d
		}
		p.rank = ranks[p.id]
		players = append(players, p)
	}
	rows.Close()
	if len(players) == 0 {
		return TournamentDraw{}, errs.Conflict("no_players", "there are no registered players to draw")
	}
	hcpLess := func(a, b drawPlayer) bool {
		switch {
		case a.hcp == nil && b.hcp == nil:
			return a.at.Before(b.at)
		case a.hcp == nil:
			return false
		case b.hcp == nil:
			return true
		case !a.hcp.Equal(*b.hcp):
			return a.hcp.LessThan(*b.hcp)
		}
		return a.at.Before(b.at)
	}
	switch method {
	case "handicap":
		sort.SliceStable(players, func(i, j int) bool { return hcpLess(players[i], players[j]) })
	case "division":
		sort.SliceStable(players, func(i, j int) bool {
			if players[i].division != players[j].division {
				return players[i].division < players[j].division
			}
			return hcpLess(players[i], players[j])
		})
	case "random":
		rand.Shuffle(len(players), func(i, j int) { players[i], players[j] = players[j], players[i] }) //nolint:gosec // draw order, not a secret
	case "standings":
		// leaders go out last and together
		sort.SliceStable(players, func(i, j int) bool { return players[i].rank > players[j].rank })
	}
	groups := buildFlights(players, ppf, keep)
	rh, err := golf.LoadRouteHoles(ctx, tx, routeOf(t, r))
	if err != nil {
		return TournamentDraw{}, err
	}
	loc := location(ctx, tx, property)
	starts, err := assignStarts(t, r, rh.Holes, len(groups), loc)
	if err != nil {
		return TournamentDraw{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_flight_players WHERE round_id = $1`, r.ID); err != nil {
		return TournamentDraw{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_flights WHERE round_id = $1`, r.ID); err != nil {
		return TournamentDraw{}, err
	}
	for i, g := range groups {
		fid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_flights (id, property_id, tournament_id, round_id, flight_no, start_hole, start_group, start_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, fid, property, tid, r.ID, i+1, starts[i].hole, starts[i].group, starts[i].at); err != nil {
			return TournamentDraw{}, err
		}
		for k, p := range g {
			if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_flight_players (round_id, registration_id, flight_id, property_id, position)
				VALUES ($1,$2,$3,$4,$5)`, r.ID, p.id, fid, property, k+1); err != nil {
				return TournamentDraw{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET status = CASE WHEN status = 'published' THEN 'published' ELSE 'drawn' END, draw_method = $2,
		drawn_at = now() WHERE id = $1`, r.ID, method); err != nil {
		return TournamentDraw{}, err
	}
	if r.Status == "published" {
		// a re-draw of a published round keeps the cards in step
		if err := m.openCards(ctx, tx, property, t, r); err != nil {
			return TournamentDraw{}, err
		}
	}
	d, err := m.Draw(ctx, tx, property, tid, roundNo)
	if err != nil {
		return d, err
	}
	d.Excluded = excluded
	if err := record(ctx, tx, "golf.tournament_round", r.ID, fmt.Sprintf("%s round %d", t.Code, roundNo), "draw", property, nil,
		map[string]any{"method": method, "flights": len(groups), "players": len(players), "excluded": len(excluded), "startType": t.StartType}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "draw", map[string]any{"round": roundNo})
}

// buildFlights splits the ordered players into balanced flights; pairing
// groups stay together where the sizes allow.
func buildFlights(players []drawPlayer, size int, keepPairings bool) [][]drawPlayer {
	n := len(players)
	nf := (n + size - 1) / size
	caps := make([]int, nf)
	for i := range caps {
		caps[i] = n / nf
		if i < n%nf {
			caps[i]++
		}
	}
	// units in order: a pairing group sits where its first player is
	var units [][]drawPlayer
	index := map[string]int{}
	for _, p := range players {
		if keepPairings && p.group != "" {
			if i, ok := index[p.group]; ok {
				units[i] = append(units[i], p)
				continue
			}
			index[p.group] = len(units)
		}
		units = append(units, []drawPlayer{p})
	}
	out := make([][]drawPlayer, nf)
	cur := 0
	for _, u := range units {
		placed := false
		for j := cur; j < nf; j++ {
			if caps[j]-len(out[j]) >= len(u) {
				out[j] = append(out[j], u...)
				placed = true
				break
			}
		}
		if !placed {
			for _, p := range u {
				for j := cur; j < nf; j++ {
					if len(out[j]) < caps[j] {
						out[j] = append(out[j], p)
						break
					}
				}
			}
		}
		for cur < nf && len(out[cur]) >= caps[cur] {
			cur++
		}
	}
	return out
}

type start struct {
	hole  int
	group *string
	at    time.Time
}

// assignStarts computes the start of each flight: a shotgun puts one flight
// per hole (A/B groups above the number of holes, B groups on par 5s and
// par 4s first); tee times start every interval from tee 1 (or 1 and 10).
func assignStarts(t Tournament, r TournamentRound, holes []golf.RouteHole, n int, loc *time.Location) ([]start, error) {
	day, _ := time.Parse("2006-01-02", r.PlayDate)
	at := atLocal(day, r.StartTime, loc)
	h := len(holes)
	if h == 0 {
		return nil, errs.Conflict("route_without_holes", "the playing route has no active holes")
	}
	out := make([]start, n)
	if t.StartType == "shotgun" {
		if n > 2*h {
			return nil, errs.Conflict("too_many_flights", fmt.Sprintf("a shotgun takes at most %d flights on %d holes (A/B groups)", 2*h, h))
		}
		a, b := "A", "B"
		for i := 0; i < n && i < h; i++ {
			out[i] = start{hole: holes[i].Sequence, at: at}
			if n > h {
				out[i].group = &a
			}
		}
		if n > h {
			pref := append([]golf.RouteHole(nil), holes...)
			sort.SliceStable(pref, func(i, j int) bool {
				if pref[i].Par != pref[j].Par {
					return pref[i].Par > pref[j].Par
				}
				return pref[i].Sequence < pref[j].Sequence
			})
			for i := h; i < n; i++ {
				out[i] = start{hole: pref[i-h].Sequence, group: &b, at: at}
			}
		}
		return out, nil
	}
	second := 0
	if r.StartTees == "1,10" {
		for _, x := range holes {
			if x.Sequence == h/2+1 {
				second = x.Sequence
			}
		}
		if second == 0 || h < 18 {
			return nil, errs.Conflict("two_tee_start", "a two-tee start needs an 18-hole route")
		}
	}
	for i := 0; i < n; i++ {
		slot, hole := i, 1
		if second > 0 {
			slot = i / 2
			if i%2 == 1 {
				hole = second
			}
		}
		out[i] = start{hole: hole, at: at.Add(time.Duration(slot*r.TeeIntervalMinutes) * time.Minute)}
	}
	return out, nil
}

// MoveInput moves a player to another flight (manual flighting).
type TournamentMoveInput struct {
	Round          int        `json:"round,omitempty" doc:"Default: the current round"`
	RegistrationID uuid.UUID  `json:"registrationId"`
	ToFlightID     *uuid.UUID `json:"toFlightId,omitempty" doc:"Empty: a new flight"`
	Position       int        `json:"position,omitempty" doc:"Default: last"`
}

// MovePlayer moves (or adds) a player to a flight before the round starts.
func (m *Module) MovePlayer(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentMoveInput) (TournamentDraw, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDraw{}, err
	}
	roundNo := in.Round
	if roundNo == 0 {
		roundNo = t.CurrentRound
	}
	r, err := roundByNo(ctx, tx, tid, roundNo)
	if err != nil {
		return TournamentDraw{}, err
	}
	if r.Status == "in_progress" || r.Status == "completed" {
		return TournamentDraw{}, errs.Conflict("round_started", "the round has started")
	}
	reg, err := getRegistration(ctx, tx, in.RegistrationID)
	if err != nil || reg.TournamentID != tid {
		return TournamentDraw{}, handle.Invalid("registrationId", "not_found", "participant of this tournament")
	}
	if reg.Status != "registered" && reg.Status != "checked_in" {
		return TournamentDraw{}, handle.Invalid("registrationId", "invalid", reg.PlayerName+" is "+reg.Status)
	}
	var from *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT flight_id FROM golf.tournament_flight_players WHERE round_id = $1 AND registration_id = $2`, r.ID, reg.ID).Scan(&from); err != nil &&
		!dbtx.IsNoRows(err) {
		return TournamentDraw{}, err
	}
	var to uuid.UUID
	if in.ToFlightID != nil {
		var count int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM golf.tournament_flight_players p WHERE p.flight_id = f.id AND p.registration_id <> $3)
			FROM golf.tournament_flights f WHERE f.id = $1 AND f.round_id = $2`, *in.ToFlightID, r.ID, reg.ID).Scan(&count); err != nil {
			if dbtx.IsNoRows(err) {
				return TournamentDraw{}, handle.Invalid("toFlightId", "not_found", "flight of this round")
			}
			return TournamentDraw{}, err
		}
		if count >= t.PlayersPerFlight {
			return TournamentDraw{}, errs.Conflict("flight_full", fmt.Sprintf("the flight already has %d players", count))
		}
		to = *in.ToFlightID
	} else {
		rh, err := golf.LoadRouteHoles(ctx, tx, routeOf(t, r))
		if err != nil {
			return TournamentDraw{}, err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.tournament_flights WHERE round_id = $1`, r.ID).Scan(&n); err != nil {
			return TournamentDraw{}, err
		}
		starts, err := assignStarts(t, r, rh.Holes, n+1, location(ctx, tx, property))
		if err != nil {
			return TournamentDraw{}, err
		}
		s := starts[n]
		var no int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(flight_no), 0) + 1 FROM golf.tournament_flights WHERE round_id = $1`, r.ID).Scan(&no); err != nil {
			return TournamentDraw{}, err
		}
		to = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_flights (id, property_id, tournament_id, round_id, flight_no, start_hole, start_group, start_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, to, property, tid, r.ID, no, s.hole, s.group, s.at); err != nil {
			return TournamentDraw{}, err
		}
	}
	var caddy *uuid.UUID
	if from != nil {
		if err := tx.QueryRow(ctx, `DELETE FROM golf.tournament_flight_players WHERE round_id = $1 AND registration_id = $2 RETURNING caddy_id`, r.ID, reg.ID).
			Scan(&caddy); err != nil {
			return TournamentDraw{}, err
		}
	}
	pos := in.Position
	if pos <= 0 {
		pos = 99
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_flight_players (round_id, registration_id, flight_id, property_id, position, caddy_id)
		VALUES ($1,$2,$3,$4,$5,$6)`, r.ID, reg.ID, to, property, pos, caddy); err != nil {
		return TournamentDraw{}, err
	}
	for _, f := range []*uuid.UUID{from, &to} {
		if f == nil {
			continue
		}
		if err := renumberFlight(ctx, tx, *f, reg.ID, pos); err != nil {
			return TournamentDraw{}, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM golf.tournament_flights f WHERE f.round_id = $1 AND NOT EXISTS (SELECT 1 FROM golf.tournament_flight_players p
		WHERE p.flight_id = f.id)`, r.ID); err != nil {
		return TournamentDraw{}, err
	}
	if r.Status == "scheduled" {
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET status = 'drawn', draw_method = 'manual', drawn_at = now() WHERE id = $1`, r.ID); err != nil {
			return TournamentDraw{}, err
		}
	}
	if r.Status == "published" {
		if err := m.openCards(ctx, tx, property, t, r); err != nil {
			return TournamentDraw{}, err
		}
	}
	d, err := m.Draw(ctx, tx, property, tid, roundNo)
	if err != nil {
		return d, err
	}
	if err := record(ctx, tx, "golf.tournament_round", r.ID, fmt.Sprintf("%s round %d", t.Code, roundNo), "move_player", property,
		map[string]any{"registrationId": reg.ID, "fromFlightId": from}, map[string]any{"toFlightId": to, "position": pos}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "draw", map[string]any{"round": roundNo})
}

// renumberFlight re-numbers positions 1..n (the moved player at pos).
func renumberFlight(ctx context.Context, tx pgx.Tx, flight, moved uuid.UUID, pos int) error {
	_, err := tx.Exec(ctx, `UPDATE golf.tournament_flight_players p SET position = x.n FROM (
		SELECT registration_id, row_number() OVER (ORDER BY position, (registration_id <> $2)) AS n FROM golf.tournament_flight_players WHERE flight_id = $1) x
		WHERE p.flight_id = $1 AND p.registration_id = x.registration_id`, flight, moved)
	_ = pos
	return err
}

// CaddyInput assigns a caddy to a player of a flight.
type TournamentCaddyInput struct {
	RegistrationID uuid.UUID  `json:"registrationId"`
	CaddyID        *uuid.UUID `json:"caddyId,omitempty" doc:"Empty: no caddy"`
}

// FlightPatch changes the start or the caddies of a flight.
type TournamentFlightPatch struct {
	StartHole  *int                   `json:"startHole,omitempty" doc:"Sequence of the start hole on the playing route"`
	StartGroup *string                `json:"startGroup,omitempty" enum:"A,B" doc:"Empty string: no group"`
	StartAt    *time.Time             `json:"startAt,omitempty"`
	Caddies    []TournamentCaddyInput `json:"caddies,omitempty"`
}

// UpdateFlight changes the start (before the round) and the caddies.
func (m *Module) UpdateFlight(ctx context.Context, tx pgx.Tx, property, tid, fid uuid.UUID, in TournamentFlightPatch) (TournamentFlight, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentFlight{}, err
	}
	var roundID uuid.UUID
	var roundStatus string
	var roundNo int
	var hole int
	var group *string
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT f.round_id, x.status, x.round_no, f.start_hole, f.start_group, f.start_at FROM golf.tournament_flights f
		JOIN golf.tournament_rounds x ON x.id = f.round_id WHERE f.id = $1 AND f.tournament_id = $2 FOR UPDATE OF f`, fid, tid).
		Scan(&roundID, &roundStatus, &roundNo, &hole, &group, &at); err != nil {
		if dbtx.IsNoRows(err) {
			return TournamentFlight{}, errs.NotFound("flight")
		}
		return TournamentFlight{}, err
	}
	r, err := roundByNo(ctx, tx, tid, roundNo)
	if err != nil {
		return TournamentFlight{}, err
	}
	rh, err := golf.LoadRouteHoles(ctx, tx, routeOf(t, r))
	if err != nil {
		return TournamentFlight{}, err
	}
	before := map[string]any{"startHole": hole, "startGroup": group, "startAt": at}
	if in.StartHole != nil || in.StartGroup != nil || in.StartAt != nil {
		if roundStatus == "in_progress" || roundStatus == "completed" {
			return TournamentFlight{}, errs.Conflict("round_started", "the start cannot change once the round started")
		}
		if in.StartHole != nil {
			if *in.StartHole < 1 || *in.StartHole > len(rh.Holes) {
				return TournamentFlight{}, handle.Invalid("startHole", "invalid", fmt.Sprintf("1–%d", len(rh.Holes)))
			}
			hole = *in.StartHole
		}
		if in.StartGroup != nil {
			group = nullStr(*in.StartGroup)
			if group != nil && *group != "A" && *group != "B" {
				return TournamentFlight{}, handle.Invalid("startGroup", "invalid", "A, B or empty")
			}
		}
		if in.StartAt != nil {
			at = *in.StartAt
		}
		var clash bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_flights WHERE round_id = $1 AND id <> $2 AND start_hole = $3
			AND start_group IS NOT DISTINCT FROM $4 AND start_at = $5)`, roundID, fid, hole, group, at).Scan(&clash); err != nil {
			return TournamentFlight{}, err
		}
		if clash {
			return TournamentFlight{}, errs.Conflict("start_taken", "another flight has this start")
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_flights SET start_hole = $2, start_group = $3, start_at = $4 WHERE id = $1`, fid, hole, group, at); err != nil {
			return TournamentFlight{}, err
		}
	}
	for _, c := range in.Caddies {
		if c.CaddyID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.caddies WHERE id = $1 AND property_id = $2 AND status = 'active' AND archived_at IS NULL)`,
				*c.CaddyID, property).Scan(&ok); err != nil {
				return TournamentFlight{}, err
			}
			if !ok {
				return TournamentFlight{}, handle.Invalid("caddies", "not_found", "active caddy of this property")
			}
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_flight_players WHERE round_id = $1 AND caddy_id = $2 AND flight_id <> $3)`,
				roundID, *c.CaddyID, fid).Scan(&busy); err != nil {
				return TournamentFlight{}, err
			}
			if busy {
				return TournamentFlight{}, errs.Conflict("caddy_busy", "the caddy is assigned to another flight of this round")
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE golf.tournament_flight_players SET caddy_id = $3 WHERE flight_id = $1 AND registration_id = $2`, fid, c.RegistrationID, c.CaddyID)
		if err != nil {
			return TournamentFlight{}, err
		}
		if tag.RowsAffected() == 0 {
			return TournamentFlight{}, handle.Invalid("caddies", "not_found", "player of this flight")
		}
	}
	fs, err := flights(ctx, tx, property, roundID, rh.Holes)
	if err != nil {
		return TournamentFlight{}, err
	}
	var out TournamentFlight
	for _, f := range fs {
		if f.ID == fid {
			out = f
		}
	}
	if err := record(ctx, tx, "golf.tournament_flight", fid, fmt.Sprintf("%s round %d flight %d", t.Code, roundNo, out.FlightNo), "update", property, before, out, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "draw", map[string]any{"round": roundNo})
}

// PublishDrawInput publishes the draw of a round.
type TournamentPublishDrawInput struct {
	Round  int   `json:"round,omitempty" doc:"Default: the drawn round"`
	Notify *bool `json:"notify,omitempty" doc:"Send the start to the players (default true)"`
}

// PublishDraw publishes the start sheet: scorecards open for every player,
// the tee sheet block follows the real starts and players are notified.
func (m *Module) PublishDraw(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentPublishDrawInput) (TournamentDraw, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDraw{}, err
	}
	roundNo := in.Round
	if roundNo == 0 {
		if err := tx.QueryRow(ctx, `SELECT coalesce(min(round_no), 0) FROM golf.tournament_rounds WHERE tournament_id = $1 AND status IN ('drawn', 'published')`, tid).
			Scan(&roundNo); err != nil {
			return TournamentDraw{}, err
		}
		if roundNo == 0 {
			return TournamentDraw{}, errs.Conflict("not_drawn", "make the draw first")
		}
	}
	r, err := roundByNo(ctx, tx, tid, roundNo)
	if err != nil {
		return TournamentDraw{}, err
	}
	if r.Status != "drawn" && r.Status != "published" {
		return TournamentDraw{}, errs.Conflict("not_drawn", fmt.Sprintf("round %d is %s", roundNo, r.Status))
	}
	if err := m.openCards(ctx, tx, property, t, r); err != nil {
		return TournamentDraw{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET status = 'published', published_at = now() WHERE id = $1`, r.ID); err != nil {
		return TournamentDraw{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET current_round = $2 WHERE id = $1`, tid, roundNo); err != nil {
		return TournamentDraw{}, err
	}
	// the tee sheet block follows the real starts (FR-TRN-02)
	if m.Golf != nil {
		pol, _, err := LoadPolicy(ctx, tx, property)
		if err != nil {
			return TournamentDraw{}, err
		}
		if r.CourseBlockID != nil {
			if err := m.Golf.TournamentUnblock(ctx, tx, property, *r.CourseBlockID); err != nil {
				return TournamentDraw{}, err
			}
		}
		if r, err = roundByNo(ctx, tx, tid, roundNo); err != nil {
			return TournamentDraw{}, err
		}
		if err := m.blockRound(ctx, tx, property, t, r, pol, location(ctx, tx, property)); err != nil {
			return TournamentDraw{}, err
		}
	}
	d, err := m.Draw(ctx, tx, property, tid, roundNo)
	if err != nil {
		return d, err
	}
	if in.Notify == nil || *in.Notify {
		for _, f := range d.Flights {
			for _, p := range f.Players {
				var email *string
				var cust uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT customer_id, email FROM golf.tournament_registrations WHERE id = $1`, p.RegistrationID).Scan(&cust, &email); err != nil {
					return d, err
				}
				user, err := customerUser(ctx, tx, cust)
				if err != nil {
					return d, err
				}
				if err := m.notifyCustomer(ctx, tx, property, deref(email), p.PlayerName, user, "golf.tournament_draw_published", map[string]any{"name": p.PlayerName,
					"tournament": t.Name, "round": roundNo, "flight": f.FlightNo, "start": "hole " + f.StartLabel, "time": f.LocalTime}); err != nil {
					return d, err
				}
			}
		}
	}
	if err := record(ctx, tx, "golf.tournament_round", r.ID, fmt.Sprintf("%s round %d", t.Code, roundNo), "publish_draw", property, nil,
		map[string]any{"flights": len(d.Flights)}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "draw", map[string]any{"round": roundNo})
}

// openCards opens the P2 scorecard of every player of the round (through
// the golf experience) with the handicap of the tournament: course
// handicap from the tee set, playing handicap after the allowance.
func (m *Module) openCards(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, r TournamentRound) error {
	if m.Experience == nil {
		return errs.Unavailable("scorecards are not available")
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	route := routeOf(t, r)
	rh, err := golf.LoadRouteHoles(ctx, tx, route)
	if err != nil {
		return err
	}
	day, _ := time.Parse("2006-01-02", r.PlayDate)
	type player struct {
		reg      uuid.UUID
		customer uuid.UUID
		name     string
		gender   *string
		hcp      *string
		teeSet   *uuid.UUID
		score    *uuid.UUID
		status   *string
	}
	rows, err := tx.Query(ctx, `SELECT r.id, r.customer_id, r.player_name, r.gender, trim_scale(r.handicap_index)::text, d.tee_set_id, s.id, s.status
		FROM golf.tournament_flight_players fp JOIN golf.tournament_registrations r ON r.id = fp.registration_id
		LEFT JOIN golf.tournament_divisions d ON d.id = r.division_id
		LEFT JOIN golf.tournament_scores s ON s.round_id = fp.round_id AND s.registration_id = r.id WHERE fp.round_id = $1`, r.ID)
	if err != nil {
		return err
	}
	var list []player
	for rows.Next() {
		var p player
		if err := rows.Scan(&p.reg, &p.customer, &p.name, &p.gender, &p.hcp, &p.teeSet, &p.score, &p.status); err != nil {
			rows.Close()
			return err
		}
		list = append(list, p)
	}
	rows.Close()
	allowance := dec(pol.HandicapAllowancePercent)
	if t.HandicapAllowance != nil {
		allowance = dec(*t.HandicapAllowance)
	}
	for _, p := range list {
		if p.score != nil {
			if deref(p.status) == "wd" {
				if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores SET status = 'not_started', status_reason = NULL WHERE id = $1`, *p.score); err != nil {
					return err
				}
			}
			continue
		}
		tee := p.teeSet
		if tee == nil {
			if err := tx.QueryRow(ctx, `SELECT id FROM golf.tee_sets WHERE course_id = $1 AND status = 'active' AND archived_at IS NULL
				ORDER BY (gender IS NOT DISTINCT FROM $2) DESC, (gender = 'any') DESC, sequence, code LIMIT 1`, t.CourseID, p.gender).Scan(&tee); err != nil &&
				!dbtx.IsNoRows(err) {
				return err
			}
		}
		var cr *string
		var slope *int
		if tee != nil {
			if err := tx.QueryRow(ctx, `SELECT course_rating::text, slope FROM golf.tee_sets WHERE id = $1`, *tee).Scan(&cr, &slope); err != nil {
				return err
			}
		}
		maxHcp := dec(pol.MaxHandicap)
		if deref(p.gender) == "female" {
			maxHcp = dec(pol.MaxHandicapLadies)
		}
		if t.MaxHandicap != nil {
			maxHcp = dec(*t.MaxHandicap)
		}
		course, playing := handicaps(p.hcp, maxHcp, cr, slope, rh.Par, len(rh.Holes), allowance)
		cid := p.customer
		card, err := m.Experience.TournamentOpenScorecard(ctx, tx, property, experience.TournamentCardInput{CustomerID: &cid, PlayerName: p.name, RouteID: route,
			TeeSetID: tee, PlayedOn: day})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.tournament_scores (id, property_id, tournament_id, round_id, registration_id, scorecard_id, tee_set_id,
			handicap_index, course_handicap, playing_handicap) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10)`, id.New(), property, t.ID, r.ID, p.reg, card, tee,
			p.hcp, course, playing); err != nil {
			return err
		}
	}
	return nil
}

// handicaps computes the course handicap (WHS: index × slope / 113 +
// (course rating − par), capped index, half for 9 holes) and the playing
// handicap (course handicap × allowance).
func handicaps(index *string, maxHcp decimal.Decimal, cr *string, slope *int, par, holes int, allowance decimal.Decimal) (*int, *int) {
	if index == nil {
		return nil, nil
	}
	hi := dec(*index)
	if maxHcp.IsPositive() && hi.GreaterThan(maxHcp) {
		hi = maxHcp
	}
	c := experience.CourseHandicap(hi, cr, slope, par, holes)
	ph := int(decimal.NewFromInt(int64(c)).Mul(allowance).Div(hundred).Round(0).IntPart())
	return &c, &ph
}

// Start starts the current round (FR-TRN-06 shotgun: every flight tees off
// at once) and the tournament.
func (m *Module) Start(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (TournamentDetail, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return TournamentDetail{}, err
	}
	if t.Status != "open" && t.Status != "closed" && t.Status != "in_progress" {
		return TournamentDetail{}, errs.Conflict("invalid_status", "a "+t.Status+" tournament cannot start")
	}
	r, err := roundByNo(ctx, tx, tid, t.CurrentRound)
	if err != nil {
		return TournamentDetail{}, err
	}
	if r.Status != "published" {
		return TournamentDetail{}, errs.Conflict("draw_not_published", fmt.Sprintf("publish the draw of round %d first (round is %s)", r.RoundNo, r.Status))
	}
	at := now()
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET status = 'in_progress', started_at = $2 WHERE id = $1`, r.ID, at); err != nil {
		return TournamentDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournaments SET status = 'in_progress', registration_closes_at = least(coalesce(registration_closes_at, $2), $2),
		updated_by = $3 WHERE id = $1`, tid, at, actor(ctx)); err != nil {
		return TournamentDetail{}, err
	}
	shotgun := t.StartType == "shotgun"
	if shotgun {
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_flights SET status = 'in_play', tee_off_at = $2 WHERE round_id = $1 AND status = 'scheduled'`, r.ID, at); err != nil {
			return TournamentDetail{}, err
		}
	}
	if err := m.publish(ctx, tx, EventStarted, "golf.tournament", tid, property, map[string]any{"tournamentId": tid, "code": t.Code, "round": r.RoundNo,
		"startType": t.StartType, "startedAt": at}); err != nil {
		return TournamentDetail{}, err
	}
	d, err := m.Detail(ctx, tx, property, tid)
	if err != nil {
		return d, err
	}
	action := "start"
	if shotgun {
		action = "shotgun_start"
	}
	if err := record(ctx, tx, "golf.tournament", tid, d.Code, action, property, map[string]any{"status": t.Status, "round": r.RoundNo},
		map[string]any{"status": "in_progress", "round": r.RoundNo, "startedAt": at}, ""); err != nil {
		return d, err
	}
	return d, live(ctx, tx, property, tid, "started", map[string]any{"round": r.RoundNo})
}

// TeeOff sends a flight out (tee times start, or a late shotgun group).
func (m *Module) TeeOff(ctx context.Context, tx pgx.Tx, property, tid, fid uuid.UUID) (TournamentFlight, error) {
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentFlight{}, err
	}
	var roundNo int
	var status, roundStatus string
	if err := tx.QueryRow(ctx, `SELECT x.round_no, f.status, x.status FROM golf.tournament_flights f JOIN golf.tournament_rounds x ON x.id = f.round_id
		WHERE f.id = $1 AND f.tournament_id = $2 FOR UPDATE OF f`, fid, tid).Scan(&roundNo, &status, &roundStatus); err != nil {
		if dbtx.IsNoRows(err) {
			return TournamentFlight{}, errs.NotFound("flight")
		}
		return TournamentFlight{}, err
	}
	if roundStatus != "in_progress" {
		return TournamentFlight{}, errs.Conflict("round_not_started", "start the round first")
	}
	if status != "scheduled" {
		return TournamentFlight{}, errs.Conflict("already_teed_off", "the flight is "+status)
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_flights SET status = 'in_play', tee_off_at = now() WHERE id = $1`, fid); err != nil {
		return TournamentFlight{}, err
	}
	d, err := m.Draw(ctx, tx, property, tid, roundNo)
	if err != nil {
		return TournamentFlight{}, err
	}
	var out TournamentFlight
	for _, f := range d.Flights {
		if f.ID == fid {
			out = f
		}
	}
	if err := record(ctx, tx, "golf.tournament_flight", fid, fmt.Sprintf("%s round %d flight %d", t.Code, roundNo, out.FlightNo), "tee_off", property,
		map[string]any{"status": status}, map[string]any{"status": "in_play"}, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "tee_off", map[string]any{"round": roundNo})
}

// ── start sheet ───────────────────────────────────────────────────────────

// SponsorLogo is a sponsor shown on the start sheet / leaderboard.
type TournamentSponsorLogo struct {
	Name       string     `json:"name"`
	Level      string     `json:"level"`
	LogoURL    *string    `json:"logoUrl"`
	LogoFileID *uuid.UUID `json:"logoFileId"`
	Holes      []int32    `json:"holes"`
}

// StartSheet is the printable start sheet of a round.
type TournamentStartSheet struct {
	TournamentID uuid.UUID               `json:"tournamentId"`
	Code         string                  `json:"code"`
	Name         string                  `json:"name"`
	Format       string                  `json:"format"`
	StartType    string                  `json:"startType" enum:"shotgun,tee_times"`
	RoundNo      int                     `json:"roundNo"`
	PlayDate     string                  `json:"playDate"`
	StartTime    string                  `json:"startTime"`
	CourseName   string                  `json:"courseName"`
	RouteName    string                  `json:"playingRouteName"`
	Status       string                  `json:"status" doc:"Round status"`
	Players      int                     `json:"players"`
	Flights      []TournamentFlight      `json:"flights"`
	Sponsors     []TournamentSponsorLogo `json:"sponsors"`
}

func sponsorLogos(ss []TournamentSponsor, onLeaderboard bool) []TournamentSponsorLogo {
	out := []TournamentSponsorLogo{}
	for _, s := range ss {
		if s.Status != "active" || (onLeaderboard && !s.ShowOnLeaderboard) || (!onLeaderboard && !s.ShowOnStartSheet) {
			continue
		}
		out = append(out, TournamentSponsorLogo{Name: s.Name, Level: s.SponsorLevel, LogoURL: s.LogoURL, LogoFileID: s.LogoFileID, Holes: s.Holes})
	}
	return out
}

// GetStartSheet builds the start sheet of a round (the current by default).
func (m *Module) GetStartSheet(ctx context.Context, q dbtx.Querier, property, tid uuid.UUID, roundNo int) (TournamentStartSheet, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentStartSheet{}, err
	}
	d, err := m.Draw(ctx, q, property, tid, roundNo)
	if err != nil {
		return TournamentStartSheet{}, err
	}
	r, err := roundByNo(ctx, q, tid, d.RoundNo)
	if err != nil {
		return TournamentStartSheet{}, err
	}
	rh, err := golf.LoadRouteHoles(ctx, q, routeOf(t, r))
	if err != nil {
		return TournamentStartSheet{}, err
	}
	ss, err := listSponsors(ctx, q, tid)
	if err != nil {
		return TournamentStartSheet{}, err
	}
	out := TournamentStartSheet{TournamentID: tid, Code: t.Code, Name: t.Name, Format: t.Format, StartType: t.StartType, RoundNo: d.RoundNo, PlayDate: r.PlayDate,
		StartTime: r.StartTime, CourseName: t.CourseName, RouteName: rh.Name, Status: r.Status, Flights: d.Flights, Sponsors: sponsorLogos(ss, false)}
	for _, f := range d.Flights {
		out.Players += len(f.Players)
	}
	return out, nil
}

// StartSheetPDF renders the start sheet for printing (FR-TRN-06).
func StartSheetPDF(s TournamentStartSheet) []byte {
	d := pdf.New()
	d.Row(15, true, s.Name)
	kind := "Shotgun start"
	if s.StartType == "tee_times" {
		kind = "Tee times"
	}
	d.Row(10, false, fmt.Sprintf("Round %d · %s · %s %s · %s (%s)", s.RoundNo, s.PlayDate, kind, s.StartTime, s.CourseName, s.RouteName))
	if len(s.Sponsors) > 0 {
		var names []string
		for _, sp := range s.Sponsors {
			names = append(names, sp.Name)
		}
		d.Row(9, false, "Sponsors: "+strings.Join(names, ", "))
	}
	d.Space(6)
	d.Rule(d.Y + 8)
	xs := []float64{pdf.Margin, pdf.Margin + 50, pdf.Margin + 95, pdf.Margin + 300, pdf.Margin + 350, pdf.Margin + 430}
	d.Columns(9, true, xs, []string{"Start", "Time", "Player", "HCP", "Division", "Caddy"})
	for _, f := range s.Flights {
		for i, p := range f.Players {
			label, at := "", ""
			if i == 0 {
				label, at = fmt.Sprintf("%s (F%d)", f.StartLabel, f.FlightNo), f.LocalTime
			}
			ph := "-"
			if p.PlayingHandicap != nil {
				ph = itoa(*p.PlayingHandicap)
			} else if p.HandicapIndex != nil {
				ph = *p.HandicapIndex
			}
			d.Columns(9, false, xs, []string{label, at, p.PlayerName, ph, deref(p.DivisionName), deref(p.CaddyCode)})
		}
		d.Space(3)
	}
	d.Space(6)
	d.Row(8, false, fmt.Sprintf("%d players · %d flights", s.Players, len(s.Flights)))
	return d.Bytes()
}

var _ = slices.Contains[[]string]
