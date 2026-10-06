package experience

// Public interface of the P2 digital scorecard, handicap and Hall of Fame for
// the P3 tournament (golf/tournament, PRD P3 §5.4.1: "Memakai scorecard P2
// lewat interface publik; tidak menulis tabel scoring"). A tournament round
// player gets an ordinary P2 scorecard that is not tied to a P1 flight; its
// holes are entered, attested, finalized and corrected through EnterScores,
// SubmitScorecard, FinalizeScorecard and CorrectScorecard, so the P2 rules
// (last-write-wins per hole, Score Audit History, correction workflow, WHS
// handicap, Hole-in-One draft) apply unchanged. Additive only.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
)

// TournamentCardInput opens the scorecard of a tournament player round.
type TournamentCardInput struct {
	CustomerID *uuid.UUID
	PlayerName string
	RouteID    uuid.UUID
	TeeSetID   *uuid.UUID
	PlayedOn   time.Time
}

// TournamentOpenScorecard opens a P2 digital scorecard for a tournament
// round player (no P1 flight): the holes of the playing route with par and
// stroke index, course rating and slope of the tee set.
func (m *Module) TournamentOpenScorecard(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentCardInput) (uuid.UUID, error) {
	rh, err := golf.LoadRouteHoles(ctx, tx, in.RouteID)
	if err != nil {
		return uuid.Nil, err
	}
	if len(rh.Holes) == 0 {
		return uuid.Nil, errs.Conflict("route_without_holes", "the playing route has no active holes")
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.scorecards (id, property_id, customer_id, player_name, playing_route_id, tee_set_id, played_on, holes, par,
		course_rating, slope, created_by)
		VALUES ($1,$2,$3,$4,$5,$6::uuid,$7::date,$8,$9,(SELECT course_rating FROM golf.tee_sets WHERE id = $6::uuid),
		(SELECT slope FROM golf.tee_sets WHERE id = $6::uuid),$10)`, sid, property, in.CustomerID, in.PlayerName, in.RouteID, in.TeeSetID,
		in.PlayedOn.Format("2006-01-02"), len(rh.Holes), rh.Par, actorPtr(ctx)); err != nil {
		return uuid.Nil, err
	}
	for _, h := range rh.Holes {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.scorecard_holes (scorecard_id, property_id, seq, hole_id, hole_number, section_code, par, stroke_index)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, sid, property, h.Sequence, h.HoleID, h.Number, h.SectionCode, h.Par, h.StrokeIndex); err != nil {
			return uuid.Nil, err
		}
	}
	return sid, record(ctx, tx, "golf.scorecard", sid, in.PlayerName, "open_tournament_card", property, nil,
		map[string]any{"playingRouteId": in.RouteID, "teeSetId": in.TeeSetID, "playedOn": in.PlayedOn.Format("2006-01-02"), "holes": len(rh.Holes)}, "")
}

// TournamentScorecards loads scorecards with their holes in two queries
// (leaderboards of a whole field).
func (m *Module) TournamentScorecards(ctx context.Context, q dbtx.Querier, ids []uuid.UUID) (map[uuid.UUID]Scorecard, error) {
	out := map[uuid.UUID]Scorecard{}
	if len(ids) == 0 {
		return out, nil
	}
	cards, err := handle.List[Scorecard](q.Query(ctx, scorecardSelect+` WHERE s.id = ANY($1)`, ids))
	if err != nil {
		return nil, err
	}
	for _, c := range cards {
		c.Scores = []ScoreHole{}
		out[c.ID] = c
	}
	type row struct {
		ScorecardID uuid.UUID `db:"scorecard_id"`
		ScoreHole
	}
	rows, err := handle.List[row](q.Query(ctx, `SELECT scorecard_id, seq, hole_id, hole_number, section_code, par, stroke_index, strokes, putts, penalties, term,
		source, entered_at FROM golf.scorecard_holes WHERE scorecard_id = ANY($1) ORDER BY scorecard_id, seq`, ids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		c, ok := out[r.ScorecardID]
		if !ok {
			continue
		}
		c.Scores = append(c.Scores, r.ScoreHole)
		out[r.ScorecardID] = c
	}
	return out, nil
}

// TournamentHandicap returns the handicap index used at registration and
// its source: the federation index (e.g. PGI, recorded as the official
// handicap) or the local index (WHS from finalised cards, or P1's manual /
// imported history) — federationFirst follows the Tournament Policies
// (PRD P3 §16 #12: local OneClub handicap, PGI when the player has one).
func TournamentHandicap(ctx context.Context, q dbtx.Querier, customer uuid.UUID, federationFirst bool) (*string, string) {
	var fed, local *string
	var localSource string
	_ = q.QueryRow(ctx, `SELECT trim_scale(handicap_index)::text FROM golf.handicap_indexes WHERE customer_id = $1 AND kind = 'federation'
		ORDER BY effective_at DESC LIMIT 1`, customer).Scan(&fed)
	_ = q.QueryRow(ctx, `SELECT trim_scale(handicap_index)::text, src FROM (
		SELECT handicap_index, effective_at, 'manual' AS src FROM golf.handicaps WHERE customer_id = $1
		UNION ALL SELECT handicap_index, effective_at, 'whs' FROM golf.handicap_indexes WHERE customer_id = $1 AND kind = 'whs') h
		ORDER BY effective_at DESC LIMIT 1`, customer).Scan(&local, &localSource)
	switch {
	case fed != nil && (federationFirst || local == nil):
		return fed, "federation"
	case local != nil:
		return local, localSource
	}
	return nil, "none"
}

// TournamentHallOfFameInput is an automatic champion entry of a tournament.
type TournamentHallOfFameInput struct {
	Category   string // tournament_champion | club_champion
	Title      string
	Year       int
	Division   *string // men | ladies | senior | junior | open
	CustomerID *uuid.UUID
	PlayerName string
	Score      *int
	AchievedOn time.Time
	SourceID   uuid.UUID // the tournament result (one entry per result)
	Consent    bool      // the player agreed to public display at registration
}

// TournamentHallOfFameEntry creates the Hall of Fame entry of a tournament
// champion (PRD P3 FR-TRN-11, continuing P2 EP-11): public display follows
// the P2 consent and the Hall of Fame Policies (auto-publish). Idempotent per
// result.
func (m *Module) TournamentHallOfFameEntry(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentHallOfFameInput) (uuid.UUID, error) {
	if in.Category != "tournament_champion" && in.Category != "club_champion" {
		return uuid.Nil, handle.Invalid("category", "invalid", "tournament_champion or club_champion")
	}
	pol, err := m.hofPolicy(ctx, tx, property)
	if err != nil {
		return uuid.Nil, err
	}
	consent := "pending"
	if in.Consent {
		consent = "granted"
	}
	eid := id.New()
	if err := tx.QueryRow(ctx, `INSERT INTO golf.hall_of_fame (id, property_id, category, title, year, division, customer_id, player_name, score, achieved_on,
		source_type, source_id, consent, consent_at, published, published_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'golf.tournament_result',$11,$12, CASE WHEN $12 = 'granted' THEN now() END,$13, CASE WHEN $13 THEN now() END,$14)
		ON CONFLICT (source_type, source_id, category) WHERE source_id IS NOT NULL DO UPDATE SET score = EXCLUDED.score
		RETURNING id`, eid, property, in.Category, in.Title, in.Year, in.Division, in.CustomerID, in.PlayerName, in.Score, in.AchievedOn.Format("2006-01-02"),
		in.SourceID, consent, pol.AutoPublish, actorPtr(ctx)).Scan(&eid); err != nil {
		return uuid.Nil, err
	}
	return eid, record(ctx, tx, "golf.hall_of_fame", eid, in.Title, "tournament_champion", property, nil,
		map[string]any{"category": in.Category, "player": in.PlayerName, "score": in.Score, "consent": consent, "published": pol.AutoPublish}, "")
}

// TournamentCaddyOf returns the caddy linked to a Caddy Tablet user (nil
// when the user is not a caddy).
func TournamentCaddyOf(ctx context.Context, q dbtx.Querier, property, user uuid.UUID) (*uuid.UUID, error) {
	var cid uuid.UUID
	err := q.QueryRow(ctx, `SELECT c.id FROM golf.caddies c JOIN golf.caddy_profiles p ON p.caddy_id = c.id
		WHERE c.property_id = $1 AND p.user_id = $2 AND c.archived_at IS NULL`, property, user).Scan(&cid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cid, nil
}
