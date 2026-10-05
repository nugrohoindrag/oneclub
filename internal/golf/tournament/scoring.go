package tournament

// FR-TRN-07 Scoring on the P2 scorecard: the caddy tablet (tournament format
// on the Scorecard, PRD P3 §7.3), the member app and the Tournament Desk
// scoring desk (paper cards) enter strokes per hole through the golf
// experience (last-write-wins per hole with the Score Audit History, offline
// queue golf.tournament_score); cards are attested by the marker and
// validated (P2 finalization); validated cards change only by the P2
// correction workflow; DQ / WD / NR are tournament statuses.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/golf/experience"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	syncsvc "oneclub/internal/platform/sync"
)

// TournamentScoreHole is one hole of a scorecard in tournament format.
type TournamentScoreHole struct {
	Seq         int     `json:"seq"`
	HoleNumber  int     `json:"holeNumber"`
	SectionCode string  `json:"sectionCode"`
	Par         int     `json:"par"`
	StrokeIndex *int    `json:"strokeIndex"`
	Received    int     `json:"strokesReceived" doc:"Handicap strokes on this hole"`
	Strokes     *int    `json:"strokes"`
	Putts       *int    `json:"putts"`
	Net         *int    `json:"net" doc:"Strokes − strokes received"`
	Points      *int    `json:"points" doc:"Stableford points (net)"`
	Term        *string `json:"term"`
}

// TournamentScorecard is a player's card of a round in tournament format.
type TournamentScorecard struct {
	ScoreID         uuid.UUID             `json:"scoreId"`
	TournamentID    uuid.UUID             `json:"tournamentId"`
	RegistrationID  uuid.UUID             `json:"registrationId"`
	Number          string                `json:"number"`
	PlayerName      string                `json:"playerName"`
	RoundNo         int                   `json:"roundNo"`
	Format          string                `json:"format" enum:"stroke_play,stableford"`
	ScorecardID     uuid.UUID             `json:"scorecardId" doc:"P2 digital scorecard"`
	ScorecardStatus string                `json:"scorecardStatus" enum:"draft,submitted,finalized"`
	Status          string                `json:"status" enum:"not_started,in_progress,submitted,finalized,dq,wd,nr"`
	StatusReason    *string               `json:"statusReason"`
	TeeSetName      *string               `json:"teeSetName"`
	HandicapIndex   *string               `json:"handicapIndex"`
	CourseHandicap  *int                  `json:"courseHandicap"`
	PlayingHandicap *int                  `json:"playingHandicap"`
	Holes           []TournamentScoreHole `json:"holes"`
	Thru            int                   `json:"thru"`
	Out             *int                  `json:"out" doc:"Front nine strokes"`
	In              *int                  `json:"in" doc:"Back nine strokes"`
	Gross           *int                  `json:"gross"`
	Net             *int                  `json:"net"`
	Points          int                   `json:"points"`
	ToPar           int                   `json:"toPar"`
	NetToPar        int                   `json:"netToPar"`
	Flags           []string              `json:"flags" doc:"Validation flags of the card (P2)"`
	AttestedBy      *string               `json:"attestedBy"`
	ValidatedAt     *time.Time            `json:"validatedAt"`
	FlightNo        *int                  `json:"flightNo"`
	StartLabel      *string               `json:"startLabel"`
}

type scoreRow struct {
	ID              uuid.UUID  `db:"id"`
	PropertyID      uuid.UUID  `db:"property_id"`
	TournamentID    uuid.UUID  `db:"tournament_id"`
	RoundID         uuid.UUID  `db:"round_id"`
	RoundNo         int        `db:"round_no"`
	RoundStatus     string     `db:"round_status"`
	RegistrationID  uuid.UUID  `db:"registration_id"`
	Number          string     `db:"number"`
	PlayerName      string     `db:"player_name"`
	CustomerID      uuid.UUID  `db:"customer_id"`
	ScorecardID     uuid.UUID  `db:"scorecard_id"`
	TeeSetName      *string    `db:"tee_set_name"`
	HandicapIndex   *string    `db:"handicap_index"`
	CourseHandicap  *int       `db:"course_handicap"`
	PlayingHandicap *int       `db:"playing_handicap"`
	Status          string     `db:"status"`
	StatusReason    *string    `db:"status_reason"`
	AttestedBy      *string    `db:"attested_by"`
	ValidatedAt     *time.Time `db:"validated_at"`
	Format          string     `db:"format"`
	TournamentState string     `db:"tournament_status"`
}

const scoreSelect = `SELECT s.id, s.property_id, s.tournament_id, s.round_id, x.round_no, x.status AS round_status, s.registration_id, r.number, r.player_name,
	r.customer_id, s.scorecard_id, ts.name AS tee_set_name, trim_scale(s.handicap_index)::text AS handicap_index, s.course_handicap, s.playing_handicap,
	s.status, s.status_reason, s.attested_by, s.validated_at, t.format, t.status AS tournament_status
	FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id JOIN golf.tournament_registrations r ON r.id = s.registration_id
	JOIN golf.tournaments t ON t.id = s.tournament_id LEFT JOIN golf.tee_sets ts ON ts.id = s.tee_set_id`

func getScore(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (scoreRow, error) {
	rows, err := q.Query(ctx, scoreSelect+` WHERE s.id = $1`, sid)
	return handle.One[scoreRow](rows, err, "tournament score")
}

func scoreOf(ctx context.Context, q dbtx.Querier, tid, rid uuid.UUID, roundNo int) (scoreRow, error) {
	rows, err := q.Query(ctx, scoreSelect+` WHERE s.tournament_id = $1 AND s.registration_id = $2 AND x.round_no = $3`, tid, rid, roundNo)
	return handle.One[scoreRow](rows, err, "scorecard of this player and round")
}

// card renders a scorecard in tournament format.
func (m *Module) card(ctx context.Context, q dbtx.Querier, s scoreRow) (TournamentScorecard, error) {
	sc, err := m.Experience.GetScorecard(ctx, q, s.ScorecardID)
	if err != nil {
		return TournamentScorecard{}, err
	}
	c := calcCard(sc, s.PlayingHandicap)
	out := TournamentScorecard{ScoreID: s.ID, TournamentID: s.TournamentID, RegistrationID: s.RegistrationID, Number: s.Number, PlayerName: s.PlayerName,
		RoundNo: s.RoundNo, Format: s.Format, ScorecardID: s.ScorecardID, ScorecardStatus: sc.Status, Status: s.Status, StatusReason: s.StatusReason,
		TeeSetName: s.TeeSetName, HandicapIndex: s.HandicapIndex, CourseHandicap: s.CourseHandicap, PlayingHandicap: s.PlayingHandicap,
		Holes: []TournamentScoreHole{}, Thru: c.thru, Points: c.points, ToPar: c.toPar, NetToPar: c.netToPar, Flags: sc.Flags, AttestedBy: sc.AttestedBy,
		ValidatedAt: s.ValidatedAt}
	if out.Flags == nil {
		out.Flags = []string{}
	}
	for _, h := range c.holes {
		th := TournamentScoreHole{Seq: h.Seq, HoleNumber: h.Number, SectionCode: h.Section, Par: h.Par, StrokeIndex: h.SI, Received: h.Received,
			Strokes: h.Strokes, Putts: h.Putts, Term: h.Term}
		if h.Strokes != nil {
			th.Net = ptr(*h.Strokes - h.Received)
			th.Points = ptr(max(0, 2+h.Par+h.Received-*h.Strokes))
		}
		out.Holes = append(out.Holes, th)
	}
	if c.frontOK && c.n >= 18 {
		out.Out = ptr(c.front)
	}
	if c.backOK && c.n >= 18 {
		out.In = ptr(c.back)
	}
	if c.complete {
		out.Gross, out.Net = ptr(c.gross), ptr(c.gross-c.ph)
	}
	var fno int
	var hole int
	var group *string
	if err := q.QueryRow(ctx, `SELECT f.flight_no, f.start_hole, f.start_group FROM golf.tournament_flight_players fp JOIN golf.tournament_flights f ON f.id = fp.flight_id
		WHERE fp.round_id = $1 AND fp.registration_id = $2`, s.RoundID, s.RegistrationID).Scan(&fno, &hole, &group); err == nil {
		out.FlightNo = &fno
		label := startLabel(hole, group)
		if hole >= 1 && hole <= len(c.holes) {
			label = startLabel(c.holes[hole-1].Number, group)
		}
		out.StartLabel = &label
	} else if !dbtx.IsNoRows(err) {
		return out, err
	}
	return out, nil
}

// refreshScore stores the totals of a card on the tournament score
// (lists, reports) and follows the P2 card status.
func (m *Module) refreshScore(ctx context.Context, tx pgx.Tx, s scoreRow) (TournamentScorecard, error) {
	out, err := m.card(ctx, tx, s)
	if err != nil {
		return out, err
	}
	status := s.Status
	if status != "dq" && status != "wd" && status != "nr" {
		switch {
		case out.ScorecardStatus == "finalized":
			status = "finalized"
		case out.ScorecardStatus == "submitted":
			status = "submitted"
		case out.Thru > 0:
			status = "in_progress"
		default:
			status = "not_started"
		}
	}
	var gross, net *int
	if out.Gross != nil {
		gross, net = out.Gross, out.Net
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores SET status = $2, thru = $3, gross = $4, net = $5, points = $6, to_par = $7, attested_by = $8 WHERE id = $1`,
		s.ID, status, out.Thru, gross, net, out.Points, out.ToPar, out.AttestedBy); err != nil {
		return out, err
	}
	out.Status = status
	return out, nil
}

// ScoreSummary is a line of the scoring desk.
type TournamentScoreSummary struct {
	ScoreID         uuid.UUID  `json:"scoreId" db:"id"`
	RegistrationID  uuid.UUID  `json:"registrationId" db:"registration_id"`
	Number          string     `json:"number" db:"number"`
	PlayerName      string     `json:"playerName" db:"player_name"`
	RoundNo         int        `json:"roundNo" db:"round_no"`
	FlightNo        *int       `json:"flightNo" db:"flight_no"`
	ScorecardID     uuid.UUID  `json:"scorecardId" db:"scorecard_id"`
	Status          string     `json:"status" db:"status" enum:"not_started,in_progress,submitted,finalized,dq,wd,nr"`
	StatusReason    *string    `json:"statusReason" db:"status_reason"`
	Thru            int        `json:"thru" db:"thru"`
	Gross           *int       `json:"gross" db:"gross"`
	Net             *int       `json:"net" db:"net"`
	Points          *int       `json:"points" db:"points"`
	ToPar           *int       `json:"toPar" db:"to_par"`
	PlayingHandicap *int       `json:"playingHandicap" db:"playing_handicap"`
	AttestedBy      *string    `json:"attestedBy" db:"attested_by"`
	ValidatedAt     *time.Time `json:"validatedAt" db:"validated_at"`
}

// Scores lists the cards of a round (scoring desk).
func Scores(ctx context.Context, q dbtx.Querier, tid uuid.UUID, roundNo int, status string) ([]TournamentScoreSummary, error) {
	return handle.List[TournamentScoreSummary](q.Query(ctx, `SELECT s.id, s.registration_id, r.number, r.player_name, x.round_no, f.flight_no, s.scorecard_id, s.status,
		s.status_reason, s.thru, s.gross, s.net, s.points, s.to_par, s.playing_handicap, s.attested_by, s.validated_at
		FROM golf.tournament_scores s JOIN golf.tournament_rounds x ON x.id = s.round_id JOIN golf.tournament_registrations r ON r.id = s.registration_id
		LEFT JOIN golf.tournament_flight_players fp ON fp.round_id = s.round_id AND fp.registration_id = s.registration_id
		LEFT JOIN golf.tournament_flights f ON f.id = fp.flight_id
		WHERE s.tournament_id = $1 AND ($2 = 0 OR x.round_no = $2) AND ($3 = '' OR s.status = $3) ORDER BY x.round_no, f.flight_no NULLS LAST, fp.position, r.player_name`,
		tid, roundNo, status))
}

// ScoreInput enters strokes of a tournament round (desk, caddy, member).
type TournamentScoreInput struct {
	RegistrationID uuid.UUID               `json:"registrationId"`
	Round          int                     `json:"round,omitempty" doc:"Default: the current round"`
	Entries        []experience.ScoreEntry `json:"entries"`
	DeviceID       string                  `json:"deviceId,omitempty"`
	Source         string                  `json:"source,omitempty" enum:"staff,caddy,player" doc:"Default: staff (caddy for a caddy, player in the member app)"`
}

// scorer is who enters the score.
type scorer int

const (
	scorerDesk scorer = iota
	scorerCaddy
	scorerPlayer
)

// EnterScores records strokes per hole on the player's P2 scorecard and
// refreshes the live leaderboard (FR-TRN-07/08).
func (m *Module) EnterScores(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, in TournamentScoreInput, who scorer, self *uuid.UUID) (TournamentScorecard, error) {
	t, err := tournamentAt(ctx, tx, property, tid)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if t.Status != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("tournament_not_started", "scores are entered while the tournament is in progress")
	}
	roundNo := in.Round
	if roundNo == 0 {
		roundNo = t.CurrentRound
	}
	s, err := scoreOf(ctx, tx, tid, in.RegistrationID, roundNo)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if s.RoundStatus != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("round_not_started", fmt.Sprintf("round %d is %s", roundNo, s.RoundStatus))
	}
	switch s.Status {
	case "finalized":
		return TournamentScorecard{}, errs.Conflict("scorecard_finalized", "the card is validated; use Score Correction")
	case "dq", "wd", "nr":
		return TournamentScorecard{}, errs.Conflict("player_out", "the player is "+s.Status)
	}
	source := in.Source
	switch who {
	case scorerCaddy:
		if err := m.caddyMayScore(ctx, tx, property, s); err != nil {
			return TournamentScorecard{}, err
		}
		source = "caddy"
	case scorerPlayer:
		if self == nil || *self != s.CustomerID {
			return TournamentScorecard{}, errs.NotFound("scorecard of this player and round")
		}
		source = "player"
	default:
		if source == "" {
			source = "staff"
		}
	}
	if err := oneOf("source", source, "staff", "caddy", "player"); err != nil {
		return TournamentScorecard{}, err
	}
	if _, err := m.Experience.EnterScores(ctx, tx, s.ScorecardID, experience.ScoreInput{Entries: in.Entries, Source: source, DeviceID: in.DeviceID}); err != nil {
		return TournamentScorecard{}, err
	}
	out, err := m.refreshScore(ctx, tx, s)
	if err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "score", map[string]any{"round": roundNo, "registrationId": s.RegistrationID.String()})
}

// caddyMayScore: a caddy enters the scores of the flight he or she
// carries for (server wins when the assignment changed).
func (m *Module) caddyMayScore(ctx context.Context, q dbtx.Querier, property uuid.UUID, s scoreRow) error {
	if can(ctx, "golf.tournament_score.validate", property) {
		return nil
	}
	caddy, err := experience.TournamentCaddyOf(ctx, q, property, handle.UserID(ctx))
	if err != nil {
		return err
	}
	if caddy == nil {
		return errs.Forbidden("only the scoring desk or a caddy of the flight enters these scores")
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.tournament_flight_players fp JOIN golf.tournament_flight_players me
		ON me.flight_id = fp.flight_id WHERE fp.round_id = $1 AND fp.registration_id = $2 AND me.caddy_id = $3)`, s.RoundID, s.RegistrationID, *caddy).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.Forbidden("you are not a caddy of this player's flight")
	}
	return nil
}

// AttestInput attests a card (marker / fellow competitor).
type TournamentAttestInput struct {
	AttestedBy string `json:"attestedBy" doc:"Marker who signed the card"`
}

func (m *Module) lockScore(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID) (scoreRow, Tournament, error) {
	t, err := lockTournament(ctx, tx, property, tid)
	if err != nil {
		return scoreRow{}, t, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM golf.tournament_scores WHERE id = $1 FOR UPDATE`, sid); err != nil {
		return scoreRow{}, t, err
	}
	s, err := getScore(ctx, tx, sid)
	if err != nil {
		return s, t, err
	}
	if s.TournamentID != tid {
		return s, t, errs.NotFound("tournament score")
	}
	return s, t, nil
}

// Attest submits a complete card signed by the marker (P2 Submit).
func (m *Module) Attest(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID, in TournamentAttestInput) (TournamentScorecard, error) {
	if err := handle.Required("attestedBy", in.AttestedBy); err != nil {
		return TournamentScorecard{}, err
	}
	s, t, err := m.lockScore(ctx, tx, property, tid, sid)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if t.Status != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("tournament_not_started", "the tournament is not in progress")
	}
	if s.Status == "finalized" || s.Status == "dq" || s.Status == "wd" || s.Status == "nr" {
		return TournamentScorecard{}, errs.Conflict("invalid_status", "the card is "+s.Status)
	}
	if _, err := m.Experience.SubmitScorecard(ctx, tx, s.ScorecardID, experience.SubmitInput{AttestedBy: in.AttestedBy}); err != nil {
		return TournamentScorecard{}, err
	}
	out, err := m.refreshScore(ctx, tx, s)
	if err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament_score", sid, s.Number+" round "+itoa(s.RoundNo), "attest", property, map[string]any{"status": s.Status},
		map[string]any{"status": out.Status, "attestedBy": in.AttestedBy}, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "score", map[string]any{"round": s.RoundNo})
}

// Validate validates a card (P2 finalization: differential, handicap,
// Hole-in-One draft); the round completes with its last card.
func (m *Module) Validate(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID) (TournamentScorecard, error) {
	s, t, err := m.lockScore(ctx, tx, property, tid, sid)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if t.Status != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("tournament_not_started", "the tournament is not in progress")
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return TournamentScorecard{}, err
	}
	switch {
	case s.Status == "finalized" || s.Status == "dq" || s.Status == "wd" || s.Status == "nr":
		return TournamentScorecard{}, errs.Conflict("invalid_status", "the card is "+s.Status)
	case pol.RequireAttestation && s.Status != "submitted":
		return TournamentScorecard{}, errs.Conflict("not_attested", "the marker must attest the card first (Tournament Policies)")
	}
	if _, err := m.Experience.FinalizeScorecard(ctx, tx, s.ScorecardID); err != nil {
		return TournamentScorecard{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores SET validated_at = now(), validated_by = $2 WHERE id = $1`, sid, actor(ctx)); err != nil {
		return TournamentScorecard{}, err
	}
	s.ValidatedAt = ptr(now())
	out, err := m.refreshScore(ctx, tx, s)
	if err != nil {
		return out, err
	}
	if err := m.completeRound(ctx, tx, property, t, s.RoundID); err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament_score", sid, s.Number+" round "+itoa(s.RoundNo), "validate", property, map[string]any{"status": s.Status},
		map[string]any{"status": out.Status, "gross": out.Gross, "net": out.Net, "points": out.Points}, ""); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "score", map[string]any{"round": s.RoundNo})
}

// CorrectionInput corrects a validated card.
type TournamentCorrectionInput struct {
	Entries []experience.ScoreEntry `json:"entries"`
	Reason  string                  `json:"reason"`
}

// Correct changes a validated card by the P2 correction workflow (reason,
// Score Audit History) before the results are final.
func (m *Module) Correct(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID, in TournamentCorrectionInput) (TournamentScorecard, error) {
	s, t, err := m.lockScore(ctx, tx, property, tid, sid)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if t.Status != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("results_final", "the results are final; corrections are no longer possible")
	}
	if s.Status != "finalized" {
		return TournamentScorecard{}, errs.Conflict("not_validated", "only validated cards are corrected; enter the score instead")
	}
	if _, err := m.Experience.CorrectScorecard(ctx, tx, s.ScorecardID, experience.CorrectionInput{Entries: in.Entries, Reason: in.Reason}); err != nil {
		return TournamentScorecard{}, err
	}
	out, err := m.refreshScore(ctx, tx, s)
	if err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "score", map[string]any{"round": s.RoundNo})
}

// StatusInput disqualifies (DQ), withdraws (WD), records no return (NR) or
// reinstates a player in a round.
type TournamentStatusInput struct {
	Status string `json:"status" enum:"dq,wd,nr,reinstate"`
	Reason string `json:"reason"`
}

// SetStatus records DQ / WD / NR of a player's round (out of the results;
// DQ for the whole tournament).
func (m *Module) SetStatus(ctx context.Context, tx pgx.Tx, property, tid, sid uuid.UUID, in TournamentStatusInput) (TournamentScorecard, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return TournamentScorecard{}, err
	}
	if err := oneOf("status", in.Status, "dq", "wd", "nr", "reinstate"); err != nil {
		return TournamentScorecard{}, err
	}
	s, t, err := m.lockScore(ctx, tx, property, tid, sid)
	if err != nil {
		return TournamentScorecard{}, err
	}
	if t.Status != "in_progress" {
		return TournamentScorecard{}, errs.Conflict("tournament_not_started", "the tournament is not in progress")
	}
	before := s.Status
	if in.Status == "reinstate" {
		if before != "dq" && before != "wd" && before != "nr" {
			return TournamentScorecard{}, errs.Conflict("invalid_status", "the player is not out")
		}
		s.Status = "not_started" // derived again from the card
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores SET status = 'not_started', status_reason = NULL WHERE id = $1`, sid); err != nil {
			return TournamentScorecard{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE golf.tournament_scores SET status = $2, status_reason = $3 WHERE id = $1`, sid, in.Status, in.Reason); err != nil {
			return TournamentScorecard{}, err
		}
		s.Status = in.Status
	}
	out, err := m.refreshScore(ctx, tx, s)
	if err != nil {
		return out, err
	}
	if err := m.completeRound(ctx, tx, property, t, s.RoundID); err != nil {
		return out, err
	}
	if err := record(ctx, tx, "golf.tournament_score", sid, s.Number+" round "+itoa(s.RoundNo), "set_status", property, map[string]any{"status": before},
		map[string]any{"status": out.Status}, in.Reason); err != nil {
		return out, err
	}
	return out, live(ctx, tx, property, tid, "score", map[string]any{"round": s.RoundNo})
}

// completeRound closes a round once every card of its players is
// validated or out (DQ / WD / NR); flights complete with it.
func (m *Module) completeRound(ctx context.Context, tx pgx.Tx, property uuid.UUID, t Tournament, roundID uuid.UUID) error {
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.tournament_flight_players fp
		LEFT JOIN golf.tournament_scores s ON s.round_id = fp.round_id AND s.registration_id = fp.registration_id
		WHERE fp.round_id = $1 AND (s.id IS NULL OR s.status NOT IN ('finalized', 'dq', 'wd', 'nr'))`, roundID).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE golf.tournament_rounds SET status = 'completed', completed_at = now() WHERE id = $1 AND status = 'in_progress'`, roundID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tournament_flights SET status = 'completed', finished_at = now() WHERE round_id = $1 AND status <> 'completed'`, roundID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "round_completed", EntityType: "golf.tournament_round", EntityID: roundID.String(),
		EntityLabel: t.Code, PropertyID: &property, After: map[string]any{"status": "completed"}})
}

// ── caddy tablet ──────────────────────────────────────────────────────────

// CaddyFlight is a tournament flight on the caddy tablet with the cards in
// tournament format (cached for offline scoring).
type TournamentCaddyFlight struct {
	TournamentID   uuid.UUID             `json:"tournamentId"`
	TournamentName string                `json:"tournamentName"`
	Format         string                `json:"format" enum:"stroke_play,stableford"`
	RoundNo        int                   `json:"roundNo"`
	RoundStatus    string                `json:"roundStatus"`
	Flight         TournamentFlight      `json:"flight"`
	Scorecards     []TournamentScorecard `json:"scorecards"`
}

// MyTournamentFlights are the tournament flights of today the signed-in
// caddy carries for (Caddy Tablet → Scorecard in tournament format).
func (m *Module) MyTournamentFlights(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]TournamentCaddyFlight, error) {
	caddy, err := experience.TournamentCaddyOf(ctx, q, property, handle.UserID(ctx))
	if err != nil {
		return nil, err
	}
	out := []TournamentCaddyFlight{}
	if caddy == nil {
		return out, nil
	}
	type row struct {
		Tournament uuid.UUID `db:"tournament_id"`
		RoundNo    int       `db:"round_no"`
		Flight     uuid.UUID `db:"flight_id"`
	}
	loc := location(ctx, q, property)
	today := localDate(now(), loc).Format("2006-01-02")
	rows, err := handle.List[row](q.Query(ctx, `SELECT DISTINCT x.tournament_id, x.round_no, fp.flight_id FROM golf.tournament_flight_players fp
		JOIN golf.tournament_rounds x ON x.id = fp.round_id WHERE fp.caddy_id = $1 AND x.play_date = $2::date AND x.status IN ('published', 'in_progress')
		ORDER BY x.round_no`, *caddy, today))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		cf, err := m.flightCards(ctx, q, property, r.Tournament, r.Flight)
		if err != nil {
			return nil, err
		}
		out = append(out, cf)
	}
	return out, nil
}

// flightCards loads a flight with the cards of its players.
func (m *Module) flightCards(ctx context.Context, q dbtx.Querier, property, tid, fid uuid.UUID) (TournamentCaddyFlight, error) {
	t, err := tournamentAt(ctx, q, property, tid)
	if err != nil {
		return TournamentCaddyFlight{}, err
	}
	var roundNo int
	if err := q.QueryRow(ctx, `SELECT x.round_no FROM golf.tournament_flights f JOIN golf.tournament_rounds x ON x.id = f.round_id WHERE f.id = $1 AND f.tournament_id = $2`,
		fid, tid).Scan(&roundNo); err != nil {
		if dbtx.IsNoRows(err) {
			return TournamentCaddyFlight{}, errs.NotFound("flight")
		}
		return TournamentCaddyFlight{}, err
	}
	d, err := m.Draw(ctx, q, property, tid, roundNo)
	if err != nil {
		return TournamentCaddyFlight{}, err
	}
	out := TournamentCaddyFlight{TournamentID: tid, TournamentName: t.Name, Format: t.Format, RoundNo: roundNo, RoundStatus: d.Status, Scorecards: []TournamentScorecard{}}
	for _, f := range d.Flights {
		if f.ID != fid {
			continue
		}
		out.Flight = f
		for _, p := range f.Players {
			if p.ScoreID == nil {
				continue
			}
			s, err := getScore(ctx, q, *p.ScoreID)
			if err != nil {
				return out, err
			}
			c, err := m.card(ctx, q, s)
			if err != nil {
				return out, err
			}
			out.Scorecards = append(out.Scorecards, c)
		}
	}
	return out, nil
}

// FlightScorecards is the flight's cards for the scoring desk or one of its
// caddies.
func (m *Module) FlightScorecards(ctx context.Context, q dbtx.Querier, property, tid, fid uuid.UUID) (TournamentCaddyFlight, error) {
	cf, err := m.flightCards(ctx, q, property, tid, fid)
	if err != nil {
		return cf, err
	}
	if can(ctx, "golf.tournament_score.view", property) {
		return cf, nil
	}
	caddy, err := experience.TournamentCaddyOf(ctx, q, property, handle.UserID(ctx))
	if err != nil {
		return cf, err
	}
	for _, p := range cf.Flight.Players {
		if caddy != nil && p.CaddyID != nil && *p.CaddyID == *caddy {
			return cf, nil
		}
	}
	return TournamentCaddyFlight{}, errs.Forbidden("you are not a caddy of this flight")
}

// ── member ────────────────────────────────────────────────────────────────

// MyScorecards are the member's cards of a tournament.
func (m *Module) MyScorecards(ctx context.Context, q dbtx.Querier, tid uuid.UUID) ([]TournamentScorecard, error) {
	me, err := crm.Me(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, scoreSelect+` WHERE s.tournament_id = $1 AND r.customer_id = $2 ORDER BY x.round_no`, tid, me.ID)
	list, err := handle.List[scoreRow](rows, err)
	if err != nil {
		return nil, err
	}
	out := []TournamentScorecard{}
	for _, s := range list {
		c, err := m.card(ctx, q, s)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// ── offline sync (FR-OPS-P3-05) ───────────────────────────────────────────

// SyncScore is one queued tournament score of the caddy tablet or the
// scoring desk (last-write-wins per hole, idempotent per item).
type SyncScore struct {
	TournamentID   uuid.UUID               `json:"tournamentId"`
	RegistrationID uuid.UUID               `json:"registrationId"`
	Round          int                     `json:"round,omitempty"`
	Entries        []experience.ScoreEntry `json:"entries"`
	DeviceID       string                  `json:"deviceId,omitempty"`
}

// SyncScoreHandler replays queued tournament scores (action golf.tournament_score).
func (m *Module) SyncScoreHandler(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in SyncScore
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid tournament score payload")
	}
	property, err := tournamentProperty(ctx, tx, in.TournamentID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAt(ctx, "golf.tournament_score.enter", property); err != nil {
		return nil, err
	}
	who := scorerDesk
	if !can(ctx, "golf.tournament_score.validate", property) {
		who = scorerCaddy
	}
	out, err := m.EnterScores(ctx, tx, property, in.TournamentID, TournamentScoreInput{RegistrationID: in.RegistrationID, Round: in.Round, Entries: in.Entries,
		DeviceID: in.DeviceID}, who, nil)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindForbidden {
			return nil, &syncsvc.ConflictError{Message: "assignment changed on the server: " + e.Message}
		}
		return nil, err
	}
	return map[string]any{"scoreId": out.ScoreID, "thru": out.Thru, "gross": out.Gross, "points": out.Points, "status": out.Status}, nil
}

// SyncCheckIn is one queued check-in of the Tournament Desk.
type SyncCheckIn struct {
	TournamentID   uuid.UUID `json:"tournamentId"`
	RegistrationID uuid.UUID `json:"registrationId"`
}

// SyncCheckInHandler replays queued check-ins (action
// golf.tournament_check_in; a second check-in is a no-op).
func (m *Module) SyncCheckInHandler(ctx context.Context, tx pgx.Tx, payload json.RawMessage) (any, error) {
	var in SyncCheckIn
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, errs.Validation("invalid_payload", "invalid check-in payload")
	}
	property, err := tournamentProperty(ctx, tx, in.TournamentID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAt(ctx, "golf.tournament_registration.check_in", property); err != nil {
		return nil, err
	}
	d, err := m.CheckIn(ctx, tx, property, in.TournamentID, in.RegistrationID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"registrationId": d.ID, "status": d.Status, "checkedInAt": d.CheckedInAt}, nil
}

func tournamentProperty(ctx context.Context, q dbtx.Querier, tid uuid.UUID) (uuid.UUID, error) {
	var p uuid.UUID
	err := q.QueryRow(ctx, `SELECT property_id FROM golf.tournaments WHERE id = $1`, tid).Scan(&p)
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("tournament")
	}
	return p, err
}
