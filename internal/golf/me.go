package golf

// Member Portal golf of PRD P2 (EP-25 FR-APP-P2-02/08) next to P1's
// /api/v1/member/golf routes: my scorecards (own score entry), round
// history, statistics & handicap, Hall of Fame with my consent, caddy rating
// & favourite, introduction letters for reciprocal clubs.

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// MyHallOfFame is the Hall of Fame screen: public entries plus my own
// entries with their consent state.
type MyHallOfFame struct {
	Public []PublicEntry `json:"public"`
	Mine   []MyEntry     `json:"mine"`
}

type MyEntry struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Category  string    `json:"category" db:"category"`
	Title     string    `json:"title" db:"title"`
	Consent   string    `json:"consent" db:"consent"`
	Published bool      `json:"published" db:"published"`
}

type MyRatingInput struct {
	Rating  int    `json:"rating"`
	Comment string `json:"comment,omitempty"`
}

type MyFavoriteInput struct {
	Favorite bool `json:"favorite"`
}

type MyLetterInput struct {
	ClubID   uuid.UUID `json:"clubId"`
	PlayFrom string    `json:"playFrom"`
	PlayTo   string    `json:"playTo"`
	Players  int       `json:"players,omitempty"`
	Notes    string    `json:"notes,omitempty"`
}

// myCard checks the scorecard is the member's own (score privacy, FR-SCR-09).
func (m *Module) myCard(ctx context.Context, tx pgx.Tx, r *http.Request) (uuid.UUID, crm.Customer, error) {
	me, err := crm.Me(ctx, tx)
	if err != nil {
		return uuid.Nil, me, err
	}
	sid, err := handle.ID(r)
	if err != nil {
		return sid, me, err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.scorecards WHERE id = $1 AND customer_id = $2)`, sid, me.ID).Scan(&ok); err != nil {
		return sid, me, err
	}
	if !ok {
		return sid, me, errs.NotFound("scorecard")
	}
	return sid, me, nil
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/stats", Summary: "Round History, Score History, Statistics & Handicap",
		Response: RoundStats{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (RoundStats, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return RoundStats{}, err
			}
			return m.Stats(ctx, tx, p.ID, httpx.ParseList(r).Limit)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/scorecards/{id}", Summary: "My scorecard", Response: Scorecard{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Scorecard, error) {
			sid, _, err := m.myCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.GetScorecard(ctx, tx, sid)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/scorecards/{id}/scores", Summary: "Enter my own scores", Request: ScoreInput{},
		Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ScoreInput) (Scorecard, error) {
			sid, _, err := m.myCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			in.Source = "player"
			return m.EnterScores(ctx, tx, sid, in)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/scorecards/{id}:submit", Summary: "Submit my scorecard for finalization",
		Request: SubmitInput{}, Response: Scorecard{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SubmitInput) (Scorecard, error) {
			sid, _, err := m.myCard(ctx, tx, r)
			if err != nil {
				return Scorecard{}, err
			}
			return m.SubmitScorecard(ctx, tx, sid, in)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/hall-of-fame", Summary: "Hall of Fame and my entries (consent)", Response: MyHallOfFame{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyHallOfFame, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return MyHallOfFame{}, err
			}
			out := MyHallOfFame{}
			if out.Public, err = m.PublicEntries(ctx, tx, p.PropertyID, ""); err != nil {
				return out, err
			}
			out.Mine, err = handle.List[MyEntry](tx.Query(ctx, `SELECT id, category, title, consent, published FROM golf.hall_of_fame
				WHERE customer_id = $1 AND archived_at IS NULL ORDER BY achieved_on DESC NULLS LAST`, p.ID))
			return out, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/hall-of-fame/{id}:consent", Summary: "Give or withdraw my Hall of Fame consent",
		Request: ConsentInput{}, Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsentInput) (handle.Empty, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return handle.Empty{}, err
			}
			eid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.SetEntryConsent(ctx, tx, p.PropertyID, eid, in.Consent, &p.ID)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/caddy-assignments/{id}:rate", Summary: "Rate my caddy after the round",
		Request: MyRatingInput{}, Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyRatingInput) (handle.Empty, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return handle.Empty{}, err
			}
			aid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.RateCaddy(ctx, tx, p.PropertyID, aid, &p.ID, in.Rating, in.Comment, "member_portal")
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/caddies/{id}:favorite", Summary: "Mark / unmark my favourite caddy",
		Request: MyFavoriteInput{}, Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyFavoriteInput) (handle.Empty, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return handle.Empty{}, err
			}
			cid, err := handle.ID(r)
			if err != nil {
				return handle.Empty{}, err
			}
			return handle.Empty{}, m.Favorite(ctx, tx, p.PropertyID, cid, p.ID, in.Favorite)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/introduction-letters", Summary: "Request an introduction letter to a reciprocal club",
		Request: MyLetterInput{}, Response: Letter{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyLetterInput) (Letter, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return Letter{}, err
			}
			return m.RequestLetter(ctx, tx, p.PropertyID, LetterInput{ClubID: in.ClubID, CustomerID: p.ID, PlayFrom: in.PlayFrom, PlayTo: in.PlayTo,
				Players: in.Players, Notes: in.Notes})
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/introduction-letters", Summary: "My introduction letters", Response: Letter{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Letter], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Letter]{}, err
			}
			return handle.Page(handle.List[Letter](tx.Query(ctx, letterSelect+` WHERE l.customer_id = $1 ORDER BY l.created_at DESC`, p.ID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/holes/{id}/distances", Summary: "GPS distance on the course map", Response: Distance{}, List: true,
		Query: []route.Param{{Name: "lat"}, {Name: "lng"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Distance], error) {
			hid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[Distance]{}, err
			}
			var lat, lng float64
			if lat, err = strconv.ParseFloat(r.URL.Query().Get("lat"), 64); err != nil {
				return httpx.Page[Distance]{}, errs.BadRequest("invalid_position", "lat and lng are required")
			}
			if lng, err = strconv.ParseFloat(r.URL.Query().Get("lng"), 64); err != nil {
				return httpx.Page[Distance]{}, errs.BadRequest("invalid_position", "lat and lng are required")
			}
			return handle.Page(m.HoleDistances(ctx, tx, hid, lat, lng))
		})})
}
