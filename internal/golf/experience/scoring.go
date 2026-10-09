package experience

// Digital Scorecard, Playing History & Handicap (PRD P2 EP-08).

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// ScoreHole is one hole of a scorecard.
type ScoreHole struct {
	Seq         int        `json:"seq" db:"seq"`
	HoleID      uuid.UUID  `json:"holeId" db:"hole_id"`
	HoleNumber  int        `json:"holeNumber" db:"hole_number"`
	SectionCode string     `json:"sectionCode" db:"section_code"`
	Par         int        `json:"par" db:"par"`
	StrokeIndex *int       `json:"strokeIndex" db:"stroke_index"`
	Strokes     *int       `json:"strokes" db:"strokes"`
	Putts       *int       `json:"putts" db:"putts"`
	Penalties   *int       `json:"penalties" db:"penalties"`
	Term        *string    `json:"term" db:"term" enum:"albatross,eagle,birdie,par,bogey,double_bogey,triple_bogey,other,hole_in_one"`
	Source      *string    `json:"source" db:"source"`
	EnteredAt   *time.Time `json:"enteredAt" db:"entered_at"`
}

// Scorecard is a player's card for a round.
type Scorecard struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	PropertyID   uuid.UUID  `json:"propertyId" db:"property_id"`
	FlightID     *uuid.UUID `json:"flightId" db:"flight_id"`
	PlayerID     *uuid.UUID `json:"playerId" db:"booking_player_id" doc:"Booking player"`
	CustomerID   *uuid.UUID `json:"customerId" db:"customer_id"`
	PlayerName   string     `json:"playerName" db:"player_name"`
	RouteID      uuid.UUID  `json:"playingRouteId" db:"playing_route_id"`
	RouteName    string     `json:"playingRouteName" db:"route_name"`
	TeeSetID     *uuid.UUID `json:"teeSetId" db:"tee_set_id"`
	TeeSetName   *string    `json:"teeSetName" db:"tee_set_name"`
	TeeColor     *string    `json:"teeColor" db:"tee_color"`
	TeeCategory  *string    `json:"teeCategory" db:"tee_category" doc:"Player category of the tee"`
	PlayedOn     time.Time  `json:"playedOn" db:"played_on"`
	Holes        int        `json:"holes" db:"holes"`
	Par          int        `json:"par" db:"par"`
	CourseRating *string    `json:"courseRating" db:"course_rating"`
	SlopeRating  *int       `json:"slopeRating" db:"slope"`
	Gross        *int       `json:"gross" db:"gross"`
	Putts        *int       `json:"putts" db:"putts"`
	Differential *string    `json:"differential" db:"differential"`
	Status       string     `json:"status" db:"status" enum:"draft,submitted,finalized"`
	AttestedBy   *string    `json:"attestedBy" db:"attested_by"`
	Flags        []string   `json:"flags" db:"flags"`
	FinalizedAt  *time.Time `json:"finalizedAt" db:"finalized_at"`
	// Stage and Locked (additive, demo feedback 10 Oct 2026 #36): once the
	// round is completed the player and the tablet no longer edit the scores;
	// a wrong score goes through a correction request.
	Stage  string      `json:"stage" db:"stage" enum:"in_play,completed,submitted,finalized" doc:"in play → completed (round over) → submitted (attested) → finalized (handicap)"`
	Locked bool        `json:"locked" db:"locked" doc:"Scores change only by a correction (round completed or card finalized)"`
	Scores []ScoreHole `json:"scores" db:"-"`
}

const scorecardSelect = `SELECT s.id, s.property_id, s.flight_id, s.booking_player_id, s.customer_id, s.player_name, s.playing_route_id, r.name AS route_name, s.tee_set_id,
	t.name AS tee_set_name, coalesce(t.color, t.code) AS tee_color, t.player_category AS tee_category, s.played_on, s.holes, s.par, s.course_rating::text AS course_rating, s.slope, s.gross, s.putts,
	trim_scale(s.differential)::text AS differential, s.status, s.attested_by, s.flags, s.finalized_at,
	CASE WHEN s.status IN ('submitted', 'finalized') THEN s.status WHEN sf.status = 'completed' THEN 'completed' ELSE 'in_play' END AS stage,
	(s.status = 'finalized' OR coalesce(sf.status = 'completed', false)) AS locked
	FROM golf.scorecards s JOIN golf.playing_routes r ON r.id = s.playing_route_id LEFT JOIN golf.tee_sets t ON t.id = s.tee_set_id
	LEFT JOIN golf.flights sf ON sf.id = s.flight_id`

// GetScorecard loads a scorecard with its holes.
func (m *Module) GetScorecard(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Scorecard, error) {
	rows, err := q.Query(ctx, scorecardSelect+` WHERE s.id = $1`, sid)
	sc, err := handle.One[Scorecard](rows, err, "scorecard")
	if err != nil {
		return sc, err
	}
	sc.Scores, err = handle.List[ScoreHole](q.Query(ctx, `SELECT seq, hole_id, hole_number, section_code, par, stroke_index, strokes, putts, penalties, term,
		source, entered_at FROM golf.scorecard_holes WHERE scorecard_id = $1 ORDER BY seq`, sid))
	return sc, err
}

// openScorecard opens a player's digital scorecard at tee-off; the tee set
// follows the player's gender (or the first tee set of the course).
func (m *Module) openScorecard(ctx context.Context, tx pgx.Tx, r Round, p RoundPlayer, holes []RouteHole, at time.Time) error {
	if r.RouteID == nil || len(holes) == 0 {
		return nil
	}
	customer := p.CustomerID
	if customer == nil && p.GuestID != nil {
		// FR-SCR-10: the round of a guest is kept on a customer profile.
		name, email, phone, err := crm.Contact(ctx, tx, nil, p.GuestID)
		if err != nil {
			return err
		}
		if phone != "" || email != "" {
			c, _, err := crm.FindOrCreate(ctx, tx, r.PropertyID, crm.Identity{Name: name, Phone: phone, Email: email})
			if err != nil {
				return err
			}
			customer = &c.ID
		}
	}
	var tee *uuid.UUID
	var gender *string
	if customer != nil {
		if c, err := crm.GetCustomer(ctx, tx, *customer); err == nil && c.Gender != "" {
			gender = &c.Gender
		}
	}
	category := "general"
	if gender != nil && *gender == "female" {
		category = "women"
	}
	if err := tx.QueryRow(ctx, `SELECT t.id FROM golf.tee_sets t
		WHERE t.course_id = $1 AND t.status = 'active' AND t.archived_at IS NULL
		ORDER BY (t.id IS NOT DISTINCT FROM (SELECT tee_set_id FROM golf.booking_players WHERE id = $4)) DESC,
		  (t.player_category IS NOT DISTINCT FROM $3) DESC, (t.gender IS NOT DISTINCT FROM $2) DESC, (t.gender = 'any') DESC, t.sequence, t.code LIMIT 1`,
		r.CourseID, gender, category, p.ID).Scan(&tee); err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	par := 0
	for _, h := range holes {
		par += h.Par
	}
	sid := id.New()
	tag, err := tx.Exec(ctx, `INSERT INTO golf.scorecards (id, property_id, flight_id, booking_player_id, customer_id, player_name, playing_route_id, tee_set_id,
		played_on, holes, par, course_rating, slope, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::uuid,$9::date,$10,$11,(SELECT course_rating FROM golf.tee_sets WHERE id = $8::uuid),
		(SELECT slope FROM golf.tee_sets WHERE id = $8::uuid),$12)
		ON CONFLICT (booking_player_id) DO NOTHING`, sid, r.PropertyID, r.FlightID, p.ID, customer, p.Name, *r.RouteID, tee,
		at.In(location(ctx, tx, r.PropertyID)).Format("2006-01-02"), len(holes), par, actorPtr(ctx))
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	for _, h := range holes {
		if _, err := tx.Exec(ctx, `INSERT INTO golf.scorecard_holes (scorecard_id, property_id, seq, hole_id, hole_number, section_code, par, stroke_index)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, sid, r.PropertyID, h.Sequence, h.HoleID, h.Number, h.SectionCode, h.Par, h.StrokeIndex); err != nil {
			return err
		}
	}
	return nil
}

type ScoreEntry struct {
	Seq       int        `json:"seq"`
	Strokes   *int       `json:"strokes,omitempty"`
	Putts     *int       `json:"putts,omitempty"`
	Penalties *int       `json:"penalties,omitempty"`
	ClientAt  *time.Time `json:"clientAt,omitempty" doc:"Device time of the entry; the latest entry per hole wins (offline sync)"`
}

type ScoreInput struct {
	Entries  []ScoreEntry `json:"entries"`
	Source   string       `json:"source,omitempty" enum:"player,caddy,staff"`
	DeviceID string       `json:"deviceId,omitempty"`
}

// EnterScores records strokes per hole. Finalized cards reject normal edits
// (FR-SCR-04). Each hole is last-write-wins by device time with an audit row.
func (m *Module) EnterScores(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in ScoreInput) (Scorecard, error) {
	var status string
	var holes int
	if err := tx.QueryRow(ctx, `SELECT status, holes FROM golf.scorecards WHERE id = $1 FOR UPDATE`, sid).Scan(&status, &holes); err != nil {
		if dbtx.IsNoRows(err) {
			return Scorecard{}, errs.NotFound("scorecard")
		}
		return Scorecard{}, err
	}
	if status == "finalized" {
		return Scorecard{}, errs.Conflict("scorecard_finalized", "the scorecard is finalized; use Score Correction")
	}
	if err := roundOpen(ctx, tx, sid, in.Source); err != nil {
		return Scorecard{}, err
	}
	if len(in.Entries) == 0 {
		return Scorecard{}, handle.Invalid("entries", "required", "at least one hole score is required")
	}
	if in.Source == "" {
		in.Source = "staff"
	}
	var pid uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT property_id FROM golf.scorecards WHERE id = $1`, sid).Scan(&pid)
	for _, e := range in.Entries {
		if e.Seq < 1 || e.Seq > holes {
			return Scorecard{}, handle.Invalid("entries", "invalid_seq", "hole sequence out of range")
		}
		if e.Strokes != nil && (*e.Strokes < 1 || *e.Strokes > 20) {
			return Scorecard{}, handle.Invalid("entries", "invalid_strokes", "strokes must be between 1 and 20")
		}
		at := eventTime(e.ClientAt)
		var before struct {
			Strokes, Putts, Penalties *int
			EnteredAt                 *time.Time
		}
		if err := tx.QueryRow(ctx, `SELECT strokes, putts, penalties, entered_at FROM golf.scorecard_holes WHERE scorecard_id = $1 AND seq = $2`, sid, e.Seq).
			Scan(&before.Strokes, &before.Putts, &before.Penalties, &before.EnteredAt); err != nil {
			return Scorecard{}, err
		}
		if before.EnteredAt != nil && before.EnteredAt.After(at) {
			continue // an older offline write: the newer value stays
		}
		same := eqInt(before.Strokes, e.Strokes) && eqInt(before.Putts, e.Putts) && eqInt(before.Penalties, e.Penalties)
		if _, err := tx.Exec(ctx, `UPDATE golf.scorecard_holes SET strokes = coalesce($3, strokes), putts = coalesce($4, putts), penalties = coalesce($5, penalties),
			source = $6, entered_at = $7, entered_by = $8 WHERE scorecard_id = $1 AND seq = $2`, sid, e.Seq, e.Strokes, e.Putts, e.Penalties, in.Source, at, actorPtr(ctx)); err != nil {
			return Scorecard{}, err
		}
		if same {
			continue // replay of the same entry: no duplicate audit
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.score_audit (id, property_id, scorecard_id, seq, kind, before, after, source, device_id, client_at, created_by)
			VALUES ($1,$2,$3,$4,'entry',$5,$6,$7,$8,$9,$10)`, id.New(), pid, sid, e.Seq,
			map[string]any{"strokes": before.Strokes, "putts": before.Putts, "penalties": before.Penalties},
			map[string]any{"strokes": e.Strokes, "putts": e.Putts, "penalties": e.Penalties}, in.Source, nullStr(in.DeviceID), at, actorPtr(ctx)); err != nil {
			return Scorecard{}, err
		}
	}
	if err := m.recompute(ctx, tx, sid); err != nil {
		return Scorecard{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.scorecards SET status = 'draft' WHERE id = $1 AND status = 'submitted'`, sid); err != nil {
		return Scorecard{}, err
	}
	sc, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return sc, err
	}
	return sc, record(ctx, tx, "golf.scorecard", sid, sc.PlayerName, "score_entry", pid, nil, map[string]any{"entries": in.Entries, "source": in.Source}, "")
}

// caddyGrace is how long after Complete Round the caddy's tablet may still
// send the last holes (scores typed after tapping Complete, offline sync).
const caddyGrace = 30 * time.Minute

// roundOpen refuses score entries once the round is completed (demo feedback
// 10 Oct 2026 #36): the player, the front desk and the office ask for a
// correction instead; only the caddy's tablet gets a short grace period.
func roundOpen(ctx context.Context, q dbtx.Querier, sid uuid.UUID, source string) error {
	var status *string
	var finished *time.Time
	if err := q.QueryRow(ctx, `SELECT f.status, f.round_finish_at FROM golf.scorecards s LEFT JOIN golf.flights f ON f.id = s.flight_id WHERE s.id = $1`,
		sid).Scan(&status, &finished); err != nil {
		return err
	}
	if status == nil || *status != "completed" {
		return nil
	}
	if source == "caddy" && finished != nil && clock.Now().Before(finished.Add(caddyGrace)) {
		return nil
	}
	return errs.Conflict("round_completed", "the round is completed: the scores can only be changed by a correction request")
}

func eqInt(a, b *int) bool {
	if b == nil {
		return true // field not sent: unchanged
	}
	return a != nil && *a == *b
}

// term names a hole score relative to par (FR-SCR-06).
func term(strokes, par int) string {
	if strokes == 1 {
		return "hole_in_one"
	}
	switch strokes - par {
	case -3:
		return "albatross"
	case -2:
		return "eagle"
	case -1:
		return "birdie"
	case 0:
		return "par"
	case 1:
		return "bogey"
	case 2:
		return "double_bogey"
	case 3:
		return "triple_bogey"
	}
	if strokes-par < -3 {
		return "albatross"
	}
	return "other"
}

// recompute updates terms, gross, putts and validation flags.
func (m *Module) recompute(ctx context.Context, tx pgx.Tx, sid uuid.UUID) error {
	holes, err := handle.List[ScoreHole](tx.Query(ctx, `SELECT seq, hole_id, hole_number, section_code, par, stroke_index, strokes, putts, penalties
		FROM golf.scorecard_holes WHERE scorecard_id = $1 ORDER BY seq`, sid))
	if err != nil {
		return err
	}
	gross, putts, complete := 0, 0, true
	flags := []string{}
	for _, h := range holes {
		if h.Strokes == nil {
			complete = false
			if _, err := tx.Exec(ctx, `UPDATE golf.scorecard_holes SET term = NULL WHERE scorecard_id = $1 AND seq = $2`, sid, h.Seq); err != nil {
				return err
			}
			continue
		}
		s := *h.Strokes
		gross += s
		if h.Putts != nil {
			putts += *h.Putts
			if *h.Putts >= s {
				flags = append(flags, "hole "+itoa(h.Seq)+": putts not lower than strokes")
			}
		}
		if s >= h.Par+5 {
			flags = append(flags, "hole "+itoa(h.Seq)+": unusual score")
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.scorecard_holes SET term = $3 WHERE scorecard_id = $1 AND seq = $2`, sid, h.Seq, term(s, h.Par)); err != nil {
			return err
		}
	}
	var g, p *int
	if complete {
		g = &gross
		p = &putts
	}
	_, err = tx.Exec(ctx, `UPDATE golf.scorecards SET gross = $2, putts = $3, flags = $4 WHERE id = $1`, sid, g, p, flags)
	return err
}

func itoa(n int) string { return decimal.NewFromInt(int64(n)).String() }

type SubmitInput struct {
	AttestedBy string `json:"attestedBy,omitempty" doc:"Marker / fellow player confirming the card"`
}

// SubmitScorecard validates completeness (FR-SCR-03).
func (m *Module) SubmitScorecard(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in SubmitInput) (Scorecard, error) {
	sc, err := m.lockCard(ctx, tx, sid)
	if err != nil {
		return sc, err
	}
	if sc.Status == "finalized" {
		return sc, errs.Conflict("scorecard_finalized", "the scorecard is already finalized")
	}
	var missing []string
	for _, h := range sc.Scores {
		if h.Strokes == nil {
			missing = append(missing, itoa(h.Seq))
		}
	}
	if len(missing) > 0 {
		return sc, errs.Validation("incomplete", "scores are missing for holes "+join(missing))
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.scorecards SET status = 'submitted', attested_by = coalesce($2, attested_by) WHERE id = $1`, sid, nullStr(in.AttestedBy)); err != nil {
		return sc, err
	}
	after, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.scorecard", sid, sc.PlayerName, "submit", sc.PropertyID, sc, after, "")
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

func (m *Module) lockCard(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (Scorecard, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM golf.scorecards WHERE id = $1 FOR UPDATE`, sid); err != nil {
		return Scorecard{}, err
	}
	return m.GetScorecard(ctx, tx, sid)
}

// FinalizeScorecard makes the card immutable, computes the score
// differential and handicap, drafts a Hole-in-One and the automatic Hall of
// Fame entries (FR-SCR-04/06/08, FR-HOF-02).
func (m *Module) FinalizeScorecard(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (Scorecard, error) {
	sc, err := m.lockCard(ctx, tx, sid)
	if err != nil {
		return sc, err
	}
	if sc.Status == "finalized" {
		return sc, errs.Conflict("scorecard_finalized", "the scorecard is already finalized")
	}
	for _, h := range sc.Scores {
		if h.Strokes == nil {
			return sc, errs.Validation("incomplete", "every hole needs a score before finalization")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.scorecards SET status = 'finalized', finalized_at = now(), finalized_by = $2 WHERE id = $1`, sid, actorPtr(ctx)); err != nil {
		return sc, err
	}
	if err := m.afterFinal(ctx, tx, sid); err != nil {
		return sc, err
	}
	after, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	if err := m.publish(ctx, tx, "golf.scorecard_finalized", "golf.scorecard", sid, sc.PropertyID, map[string]any{"scorecardId": sid,
		"customerId": sc.CustomerID, "gross": after.Gross}); err != nil {
		return after, err
	}
	return after, record(ctx, tx, "golf.scorecard", sid, sc.PlayerName, "finalize", sc.PropertyID, sc, after, "")
}

// afterFinal computes the differential, handicap, HIO and Hall of Fame entries.
func (m *Module) afterFinal(ctx context.Context, tx pgx.Tx, sid uuid.UUID) error {
	sc, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return err
	}
	if sc.CustomerID != nil && sc.Holes == 18 && sc.CourseRating != nil && sc.SlopeRating != nil {
		idx := currentHandicap(ctx, tx, *sc.CustomerID)
		diff := differential(sc, idx)
		if _, err := tx.Exec(ctx, `UPDATE golf.scorecards SET differential = $2::numeric WHERE id = $1`, sid, diff.String()); err != nil {
			return err
		}
		if err := m.recomputeHandicap(ctx, tx, sc.PropertyID, *sc.CustomerID); err != nil {
			return err
		}
	}
	for _, h := range sc.Scores {
		if h.Strokes == nil {
			continue
		}
		switch term(*h.Strokes, h.Par) {
		case "hole_in_one":
			if err := m.draftHIO(ctx, tx, sc, h); err != nil {
				return err
			}
		case "albatross", "eagle":
			t := term(*h.Strokes, h.Par)
			title := map[string]string{"albatross": "Albatross", "eagle": "Eagle"}[t] + " · hole " + itoa(h.HoleNumber) + " (" + h.SectionCode + ")"
			if err := m.autoEntry(ctx, tx, sc, t, title, &h.HoleID, h.Strokes, "golf.scorecard", sid, false); err != nil {
				return err
			}
		}
	}
	// Course record per tee set (needs verification).
	pol, err := m.hofPolicy(ctx, tx, sc.PropertyID)
	if err != nil {
		return err
	}
	if sc.Gross != nil && sc.Holes >= pol.CourseRecordMinHoles {
		var best *int
		if err := tx.QueryRow(ctx, `SELECT min(gross) FROM golf.scorecards WHERE tee_set_id IS NOT DISTINCT FROM $1 AND playing_route_id = $2 AND status = 'finalized' AND id <> $3`,
			sc.TeeSetID, sc.RouteID, sid).Scan(&best); err != nil {
			return err
		}
		if best != nil && *sc.Gross < *best {
			if err := m.autoEntry(ctx, tx, sc, "course_record", "Course Record · "+deref(sc.TeeSetName)+" · "+sc.RouteName, nil, sc.Gross, "golf.scorecard", sid, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// differential = (113 / slope) × (adjusted gross − course rating); each hole
// is capped at net double bogey (WHS), or par + 5 without a handicap.
func differential(sc Scorecard, index *string) decimal.Decimal {
	cr, _ := decimal.NewFromString(*sc.CourseRating)
	slope := *sc.SlopeRating
	var courseHcp int
	hasIndex := index != nil
	if hasIndex {
		hi, _ := decimal.NewFromString(*index)
		v, _ := hi.Mul(decimal.NewFromInt(int64(slope))).Div(decimal.NewFromInt(113)).Add(cr).Sub(decimal.NewFromInt(int64(sc.Par))).Float64()
		courseHcp = int(math.Round(v))
	}
	adj := 0
	for _, h := range sc.Scores {
		s := *h.Strokes
		limit := h.Par + 5
		if hasIndex {
			received := 0
			if courseHcp > 0 {
				received = courseHcp / 18
				si := h.Seq
				if h.StrokeIndex != nil {
					si = *h.StrokeIndex
				}
				if si <= courseHcp%18 {
					received++
				}
			}
			limit = h.Par + 2 + received
		}
		adj += min(s, limit)
	}
	return decimal.NewFromInt(113).Div(decimal.NewFromInt(int64(slope))).Mul(decimal.NewFromInt(int64(adj)).Sub(cr)).Round(1)
}

// whsCount maps the number of differentials to (best n, adjustment).
func whsCount(n int) (int, float64) {
	switch {
	case n < 3:
		return 0, 0
	case n == 3:
		return 1, -2
	case n == 4:
		return 1, -1
	case n == 5:
		return 1, 0
	case n == 6:
		return 2, -1
	case n <= 8:
		return 2, 0
	case n <= 11:
		return 3, 0
	case n <= 14:
		return 4, 0
	case n <= 16:
		return 5, 0
	case n <= 18:
		return 6, 0
	case n == 19:
		return 7, 0
	}
	return 8, 0
}

// recomputeHandicap derives the local Handicap Index from the latest 20
// finalized 18-hole differentials (WHS method, proposed — OQ #6).
func (m *Module) recomputeHandicap(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT differential::float8 FROM golf.scorecards WHERE customer_id = $1 AND status = 'finalized' AND differential IS NOT NULL
		ORDER BY played_on DESC, finalized_at DESC LIMIT 20`, customer)
	if err != nil {
		return err
	}
	diffs, err := pgx.CollectRows(rows, pgx.RowTo[float64])
	if err != nil {
		return err
	}
	best, adj := whsCount(len(diffs))
	var index *string
	if best > 0 {
		sort.Float64s(diffs)
		sum := 0.0
		for _, d := range diffs[:best] {
			sum += d
		}
		v := math.Min(math.Floor((sum/float64(best)+adj)*10)/10, 54.0)
		s := decimal.NewFromFloat(v).StringFixed(1)
		index = &s
	}
	if index == nil {
		return nil
	}
	var last *string
	_ = tx.QueryRow(ctx, `SELECT trim_scale(handicap_index)::text FROM golf.handicap_indexes WHERE customer_id = $1 AND kind = 'whs' ORDER BY effective_at DESC LIMIT 1`,
		customer).Scan(&last)
	if last != nil && dec(*last).Equal(dec(*index)) {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO golf.handicap_indexes (id, property_id, customer_id, kind, handicap_index, rounds_counted, source, created_by)
		VALUES ($1,$2,$3,'whs',$4::numeric,$5,'scorecards',$6)`, id.New(), property, customer, *index, len(diffs), actorPtr(ctx))
	return err
}

type CorrectionInput struct {
	Entries []ScoreEntry `json:"entries"`
	Reason  string       `json:"reason"`
}

// CorrectScorecard changes a finalized card by an authorised role with a
// mandatory reason; before/after go to the Score Audit History (FR-SCR-05).
func (m *Module) CorrectScorecard(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CorrectionInput) (Scorecard, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return Scorecard{}, err
	}
	sc, err := m.lockCard(ctx, tx, sid)
	if err != nil {
		return sc, err
	}
	if !sc.Locked {
		return sc, errs.Conflict("not_finalized", "only scorecards of a completed round are corrected; edit the scores instead")
	}
	return m.applyCorrection(ctx, tx, sc, in, "staff")
}

// applyCorrection writes corrected holes with the before/after audit; a
// finalized card recomputes its differential and handicap.
func (m *Module) applyCorrection(ctx context.Context, tx pgx.Tx, sc Scorecard, in CorrectionInput, source string) (Scorecard, error) {
	sid := sc.ID
	if len(in.Entries) == 0 {
		return sc, handle.Invalid("entries", "required", "at least one hole is required")
	}
	for _, e := range in.Entries {
		if e.Seq < 1 || e.Seq > sc.Holes {
			return sc, handle.Invalid("entries", "invalid_seq", "hole sequence out of range")
		}
		if e.Strokes != nil && (*e.Strokes < 1 || *e.Strokes > 20) {
			return sc, handle.Invalid("entries", "invalid_strokes", "strokes must be between 1 and 20")
		}
		b := sc.Scores[e.Seq-1]
		if _, err := tx.Exec(ctx, `UPDATE golf.scorecard_holes SET strokes = coalesce($3, strokes), putts = coalesce($4, putts), penalties = coalesce($5, penalties),
			source = 'correction', entered_at = now(), entered_by = $6 WHERE scorecard_id = $1 AND seq = $2`, sid, e.Seq, e.Strokes, e.Putts, e.Penalties, actorPtr(ctx)); err != nil {
			return sc, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO golf.score_audit (id, property_id, scorecard_id, seq, kind, before, after, reason, source, created_by)
			VALUES ($1,$2,$3,$4,'correction',$5,$6,$7,$8,$9)`, id.New(), sc.PropertyID, sid, e.Seq,
			map[string]any{"strokes": b.Strokes, "putts": b.Putts, "penalties": b.Penalties},
			map[string]any{"strokes": e.Strokes, "putts": e.Putts, "penalties": e.Penalties}, in.Reason, source, actorPtr(ctx)); err != nil {
			return sc, err
		}
	}
	if err := m.recompute(ctx, tx, sid); err != nil {
		return sc, err
	}
	if sc.Status == "finalized" {
		if err := m.afterFinal(ctx, tx, sid); err != nil {
			return sc, err
		}
	}
	after, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "score_correction", EntityType: "golf.scorecard", EntityID: sid.String(),
		EntityLabel: sc.PlayerName, PropertyID: &sc.PropertyID, Before: sc, After: after, Reason: in.Reason})
}

// ScoreAudit is one Score Audit History row.
type ScoreAudit struct {
	ID        uuid.UUID      `json:"id" db:"id"`
	Seq       int            `json:"seq" db:"seq"`
	Kind      string         `json:"kind" db:"kind" enum:"entry,correction"`
	Before    map[string]any `json:"before" db:"before"`
	After     map[string]any `json:"after" db:"after"`
	Reason    *string        `json:"reason" db:"reason"`
	Source    *string        `json:"source" db:"source"`
	DeviceID  *string        `json:"deviceId" db:"device_id"`
	ClientAt  *time.Time     `json:"clientAt" db:"client_at"`
	CreatedAt time.Time      `json:"createdAt" db:"created_at"`
	CreatedBy *uuid.UUID     `json:"createdBy" db:"created_by"`
}

func (m *Module) ScoreAuditHistory(ctx context.Context, q dbtx.Querier, sid uuid.UUID) ([]ScoreAudit, error) {
	return handle.List[ScoreAudit](q.Query(ctx, `SELECT id, seq, kind, before, after, reason, source, device_id, client_at, created_at, created_by
		FROM golf.score_audit WHERE scorecard_id = $1 ORDER BY created_at, seq`, sid))
}

// ── history & statistics (FR-SCR-07) ──────────────────────────────────────

// RoundStats are the playing statistics of a customer.
type RoundStats struct {
	CustomerID      uuid.UUID   `json:"customerId"`
	Rounds          int         `json:"rounds"`
	AverageGross    *string     `json:"averageGross" doc:"18-hole finalized rounds"`
	BestGross       *int        `json:"bestGross"`
	AveragePar3     *string     `json:"averagePar3"`
	AveragePar4     *string     `json:"averagePar4"`
	AveragePar5     *string     `json:"averagePar5"`
	BirdiesOrBetter int         `json:"birdiesOrBetter"`
	AveragePutts    *string     `json:"averagePutts"`
	HandicapIndex   *string     `json:"handicapIndex" doc:"Local WHS index"`
	OfficialIndex   *string     `json:"officialHandicap" doc:"Federation handicap (input)"`
	History         []Scorecard `json:"history"`
}

func (m *Module) Stats(ctx context.Context, q dbtx.Querier, customer uuid.UUID, limit int) (RoundStats, error) {
	st := RoundStats{CustomerID: customer}
	if err := q.QueryRow(ctx, `SELECT count(*)::int,
		trim_scale(round(avg(gross) FILTER (WHERE holes = 18), 1))::text, min(gross) FILTER (WHERE holes = 18),
		trim_scale(round(avg(putts) FILTER (WHERE holes = 18), 1))::text
		FROM golf.scorecards WHERE customer_id = $1 AND status = 'finalized'`, customer).Scan(&st.Rounds, &st.AverageGross, &st.BestGross, &st.AveragePutts); err != nil {
		return st, err
	}
	if err := q.QueryRow(ctx, `SELECT trim_scale(round(avg(h.strokes) FILTER (WHERE h.par = 3), 2))::text, trim_scale(round(avg(h.strokes) FILTER (WHERE h.par = 4), 2))::text,
		trim_scale(round(avg(h.strokes) FILTER (WHERE h.par = 5), 2))::text, count(*) FILTER (WHERE h.strokes < h.par)::int
		FROM golf.scorecard_holes h JOIN golf.scorecards s ON s.id = h.scorecard_id WHERE s.customer_id = $1 AND s.status = 'finalized'`, customer).
		Scan(&st.AveragePar3, &st.AveragePar4, &st.AveragePar5, &st.BirdiesOrBetter); err != nil {
		return st, err
	}
	_ = q.QueryRow(ctx, `SELECT (SELECT trim_scale(handicap_index)::text FROM golf.handicap_indexes WHERE customer_id = $1 AND kind = 'whs' ORDER BY effective_at DESC LIMIT 1),
		(SELECT trim_scale(handicap_index)::text FROM golf.handicap_indexes WHERE customer_id = $1 AND kind = 'federation' ORDER BY effective_at DESC LIMIT 1)`, customer).
		Scan(&st.HandicapIndex, &st.OfficialIndex)
	var err error
	st.History, err = handle.List[Scorecard](q.Query(ctx, scorecardSelect+` WHERE s.customer_id = $1 ORDER BY s.played_on DESC, s.created_at DESC LIMIT $2`, customer, limit))
	return st, err
}

// HoleTime is the duration of one hole of a round.
type HoleTime struct {
	Seq        int        `json:"seq" db:"seq"`
	HoleNumber int        `json:"holeNumber" db:"number"`
	Section    string     `json:"sectionCode" db:"section_code"`
	StartedAt  time.Time  `json:"startedAt" db:"started_at"`
	FinishedAt *time.Time `json:"finishedAt" db:"finished_at"`
	Minutes    *string    `json:"minutes" db:"minutes"`
	Target     int        `json:"targetMinutes" db:"target_minutes"`
}

// RoundTimes returns Hole Progress / Hole Duration / Round Duration.
type RoundTimes struct {
	FlightID     uuid.UUID  `json:"flightId"`
	TeeOffAt     *time.Time `json:"teeOffAt"`
	FinishedAt   *time.Time `json:"roundFinishAt"`
	RoundMinutes *string    `json:"roundMinutes"`
	Holes        []HoleTime `json:"holes"`
}

func (m *Module) RoundTimes(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (RoundTimes, error) {
	rt := RoundTimes{FlightID: fid}
	if err := q.QueryRow(ctx, `SELECT tee_off_at, round_finish_at, trim_scale(round((extract(epoch FROM round_finish_at - tee_off_at) / 60)::numeric, 1))::text
		FROM golf.flights WHERE id = $1`, fid).Scan(&rt.TeeOffAt, &rt.FinishedAt, &rt.RoundMinutes); err != nil {
		if dbtx.IsNoRows(err) {
			return rt, errs.NotFound("flight")
		}
		return rt, err
	}
	var err error
	rt.Holes, err = handle.List[HoleTime](q.Query(ctx, `SELECT p.seq, h.number, s.code AS section_code, p.started_at, p.finished_at,
		trim_scale(round((extract(epoch FROM p.finished_at - p.started_at) / 60)::numeric, 1))::text AS minutes, coalesce(t.target_minutes, $2) AS target_minutes
		FROM golf.hole_progress p JOIN golf.holes h ON h.id = p.hole_id JOIN golf.course_sections s ON s.id = h.section_id
		LEFT JOIN golf.hole_pace_targets t ON t.hole_id = h.id
		WHERE p.flight_id = $1 ORDER BY p.seq`, fid, defaultHoleTarget))
	return rt, err
}

type OfficialHandicapInput struct {
	Index  string `json:"index"`
	Source string `json:"source,omitempty" doc:"e.g. PGI"`
}

// SetOfficialHandicap records the federation handicap (input, OQ #6).
func (m *Module) SetOfficialHandicap(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, in OfficialHandicapInput) error {
	v, err := handle.Decimal("index", in.Index, decimal.Zero)
	if err != nil {
		return err
	}
	if v.IsNegative() || v.GreaterThan(decimal.NewFromInt(54)) {
		return handle.Invalid("index", "invalid_index", "handicap index must be between 0 and 54")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.handicap_indexes (id, property_id, customer_id, kind, handicap_index, source, created_by)
		VALUES ($1,$2,$3,'federation',$4::numeric,$5,$6)`, id.New(), property, customer, v.String(), nullStr(in.Source), actorPtr(ctx)); err != nil {
		return err
	}
	return record(ctx, tx, "golf.handicap", customer, "official handicap", audit.ActionUpdate, property, nil, in, "")
}

// currentHandicap is the latest handicap index of P1's history (manual,
// import, WHS or federation).
func currentHandicap(ctx context.Context, q dbtx.Querier, customer uuid.UUID) *string {
	var idx *string
	_ = q.QueryRow(ctx, `SELECT trim_scale(handicap_index)::text FROM (SELECT handicap_index, effective_at FROM golf.handicaps WHERE customer_id = $1
		UNION ALL SELECT handicap_index, effective_at FROM golf.handicap_indexes WHERE customer_id = $1) h ORDER BY effective_at DESC LIMIT 1`, customer).Scan(&idx)
	return idx
}
