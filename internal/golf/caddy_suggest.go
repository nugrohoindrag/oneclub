package golf

// Caddy choice at the front desk (demo feedback 9 Oct 2026): after a player
// checks in, the desk asks who their usual caddy is or recommends one. One
// caddy serves one player (Caddy Policy playersPerCaddy). For every player
// the desk sees the caddy assigned, the caddy the player asked for, their
// favourites, the caddies who carried for them most often, and a
// recommendation: the first of those who is free today, else the next
// caddy of the queue.

import (
	"context"
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
)

// CaddyOption is a caddy the desk can pick for a player.
type CaddyOption struct {
	CaddyID   uuid.UUID `json:"caddyId"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Rounds    int       `json:"rounds" doc:"Rounds with this player (usual caddies)"`
	Available bool      `json:"available" doc:"Present and free today"`
	Reason    string    `json:"reason,omitempty" enum:"requested,favourite,usual,queue"`
}

// PlayerCaddySuggestion is the caddy choice of one player.
type PlayerCaddySuggestion struct {
	PlayerID     uuid.UUID     `json:"playerId"`
	FlightID     uuid.UUID     `json:"flightId"`
	Name         string        `json:"name"`
	CheckedIn    bool          `json:"checkedIn" doc:"The caddy is assigned once the player has checked in"`
	AssignmentID *uuid.UUID    `json:"assignmentId"`
	Current      *CaddyOption  `json:"current"`
	Requested    *CaddyOption  `json:"requested"`
	Favourites   []CaddyOption `json:"favourites"`
	Usual        []CaddyOption `json:"usual"`
	Recommended  *CaddyOption  `json:"recommended"`
}

// CaddySuggestions lists the caddy choice of every player of a booking.
func (m *Module) CaddySuggestions(ctx context.Context, q dbtx.Querier, property, bid uuid.UUID) ([]PlayerCaddySuggestion, error) {
	var p uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM golf.bookings WHERE id = $1`, bid).Scan(&p); err != nil || p != property {
		return nil, errs.NotFound("booking")
	}
	b, err := GetBooking(ctx, q, bid)
	if err != nil {
		return nil, err
	}
	loc := location(ctx, q, property)
	day, err := parseDate(b.PlayDate)
	if err != nil {
		return nil, err
	}
	board, err := CaddyBoard(ctx, q, property, day, loc)
	if err != nil {
		return nil, err
	}
	free := map[uuid.UUID]bool{}
	var queue []CaddyBoardEntry
	for _, c := range board {
		if c.Status == "available" {
			free[c.CaddyID] = true
			queue = append(queue, c)
		}
	}
	// the next caddy of the rotation first
	sort.SliceStable(queue, func(i, j int) bool {
		a, b := queue[i].QueueNo, queue[j].QueueNo
		return a != nil && (b == nil || *a < *b)
	})
	type caddy struct {
		ID     uuid.UUID `db:"id"`
		Code   string    `db:"code"`
		Name   string    `db:"name"`
		Rounds int       `db:"rounds"`
	}
	option := func(c caddy, reason string) CaddyOption {
		return CaddyOption{CaddyID: c.ID, Code: c.Code, Name: c.Name, Rounds: c.Rounds, Available: free[c.ID], Reason: reason}
	}
	collect := func(sql string, args ...any) ([]caddy, error) {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByNameLax[caddy])
	}
	taken := map[uuid.UUID]bool{} // recommended to an earlier player of the booking
	out := []PlayerCaddySuggestion{}
	for _, p := range b.Players {
		if p.Status == "removed" || p.Status == "cancelled" {
			continue
		}
		s := PlayerCaddySuggestion{PlayerID: p.ID, FlightID: p.FlightID, Name: p.Name, CheckedIn: p.Status == "checked_in",
			Favourites: []CaddyOption{}, Usual: []CaddyOption{}}
		cur, err := collect(`SELECT c.id, c.code, c.name, 0 AS rounds FROM golf.caddy_assignments a JOIN golf.caddies c ON c.id = a.caddy_id
			WHERE $1 = ANY(a.player_ids) AND a.status IN ('assigned', 'in_play', 'completed') LIMIT 1`, p.ID)
		if err != nil {
			return nil, err
		}
		if len(cur) > 0 {
			o := option(cur[0], "")
			s.Current = &o
			var aid uuid.UUID
			if err := q.QueryRow(ctx, `SELECT id FROM golf.caddy_assignments WHERE $1 = ANY(player_ids) AND status IN ('assigned', 'in_play', 'completed') LIMIT 1`,
				p.ID).Scan(&aid); err == nil {
				s.AssignmentID = &aid
			}
		}
		req, err := collect(`SELECT c.id, c.code, c.name, 0 AS rounds FROM golf.player_caddy_requests r JOIN golf.caddies c ON c.id = r.caddy_id
			WHERE r.booking_player_id = $1 AND r.preference = 'preferred'`, p.ID)
		if err != nil {
			return nil, err
		}
		if len(req) > 0 {
			o := option(req[0], "requested")
			s.Requested = &o
		}
		if p.CustomerID != nil {
			fav, err := collect(`SELECT c.id, c.code, c.name, 0 AS rounds FROM golf.caddy_favorites f JOIN golf.caddies c ON c.id = f.caddy_id
				WHERE f.customer_id = $1 AND c.status = 'active' ORDER BY f.created_at DESC LIMIT 3`, *p.CustomerID)
			if err != nil {
				return nil, err
			}
			for _, c := range fav {
				s.Favourites = append(s.Favourites, option(c, "favourite"))
			}
			usual, err := collect(`SELECT c.id, c.code, c.name, count(*)::int AS rounds FROM golf.caddy_assignments a
				JOIN golf.booking_players bp ON bp.id = ANY(a.player_ids) JOIN golf.caddies c ON c.id = a.caddy_id
				WHERE bp.customer_id = $1 AND bp.id <> $2 AND a.status IN ('in_play', 'completed') AND c.status = 'active'
				GROUP BY c.id, c.code, c.name ORDER BY count(*) DESC, max(a.assigned_at) DESC LIMIT 3`, *p.CustomerID, p.ID)
			if err != nil {
				return nil, err
			}
			for _, c := range usual {
				s.Usual = append(s.Usual, option(c, "usual"))
			}
		}
		if s.Current == nil {
			var cands []CaddyOption
			if s.Requested != nil {
				cands = append(cands, *s.Requested)
			}
			cands = append(append(cands, s.Favourites...), s.Usual...)
			for _, c := range cands {
				if c.Available && !taken[c.CaddyID] {
					o := c
					s.Recommended = &o
					break
				}
			}
			if s.Recommended == nil {
				for _, c := range queue {
					if !taken[c.CaddyID] {
						s.Recommended = &CaddyOption{CaddyID: c.CaddyID, Code: c.Code, Name: c.Name, Available: true, Reason: "queue"}
						break
					}
				}
			}
			if s.Recommended != nil {
				taken[s.Recommended.CaddyID] = true
			}
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *Module) caddySuggestionsHTTP(w http.ResponseWriter, r *http.Request) {
	bid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m.read(w, r, func(ctx context.Context, tx pgx.Tx) (any, error) {
		out, err := m.CaddySuggestions(ctx, tx, prop(ctx), bid)
		return httpx.Page[PlayerCaddySuggestion]{Items: out}, err
	})
}
