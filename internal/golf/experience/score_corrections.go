package experience

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

/*
 * Score correction requests (demo feedback 10 Oct 2026, item 36): after
 * Complete Round the scorecard is read-only for the player; a wrong score is
 * reported with the hole, the proposed strokes and a reason, and the
 * starter / marshal / handicap committee (golf.scorecard.correct) approves
 * or rejects it. Approval writes the hole with the score audit.
 */

// CorrectionRequest is a player's request to change a hole score.
type CorrectionRequest struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	ScorecardID    uuid.UUID  `json:"scorecardId" db:"scorecard_id"`
	PlayerName     string     `json:"playerName" db:"player_name"`
	PlayedOn       time.Time  `json:"playedOn" db:"played_on"`
	Seq            int        `json:"seq" db:"seq"`
	HoleNumber     int        `json:"holeNumber" db:"hole_number"`
	Par            int        `json:"par" db:"par"`
	CurrentStrokes *int       `json:"currentStrokes" db:"current_strokes"`
	Strokes        int        `json:"strokes" db:"strokes"`
	Reason         string     `json:"reason" db:"reason"`
	Status         string     `json:"status" db:"status" enum:"requested,approved,rejected"`
	RequestedName  *string    `json:"requestedName" db:"requested_name"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	DecidedAt      *time.Time `json:"decidedAt" db:"decided_at"`
	DecisionNote   *string    `json:"decisionNote" db:"decision_note"`
}

// CorrectionRequestInput asks for one hole to be changed.
type CorrectionRequestInput struct {
	Seq     int    `json:"seq" doc:"Hole sequence in the round (1-18)"`
	Strokes int    `json:"strokes"`
	Reason  string `json:"reason"`
}

// CorrectionDecision approves or rejects a request.
type CorrectionDecision struct {
	Note string `json:"note,omitempty"`
}

const correctionSelect = `SELECT q.id, q.scorecard_id, s.player_name, s.played_on, q.seq, h.hole_number, h.par, q.current_strokes, q.strokes, q.reason,
	q.status, q.requested_name, q.created_at, q.decided_at, q.decision_note
	FROM golf.score_correction_requests q JOIN golf.scorecards s ON s.id = q.scorecard_id
	JOIN golf.scorecard_holes h ON h.scorecard_id = q.scorecard_id AND h.seq = q.seq`

// RequestCorrection records a player's correction request on a locked card.
func (m *Module) RequestCorrection(ctx context.Context, tx pgx.Tx, sid uuid.UUID, who string, in CorrectionRequestInput) (CorrectionRequest, error) {
	sc, err := m.GetScorecard(ctx, tx, sid)
	if err != nil {
		return CorrectionRequest{}, err
	}
	if !sc.Locked {
		return CorrectionRequest{}, errs.Conflict("round_open", "the round is still in play: enter the score on the scorecard")
	}
	if in.Seq < 1 || in.Seq > sc.Holes {
		return CorrectionRequest{}, handle.Invalid("seq", "invalid_seq", "hole sequence out of range")
	}
	if in.Strokes < 1 || in.Strokes > 20 {
		return CorrectionRequest{}, handle.Invalid("strokes", "invalid_strokes", "strokes must be between 1 and 20")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if err := handle.Required("reason", in.Reason); err != nil {
		return CorrectionRequest{}, err
	}
	cur := sc.Scores[in.Seq-1].Strokes
	if cur != nil && *cur == in.Strokes {
		return CorrectionRequest{}, handle.Invalid("strokes", "unchanged", "that is already the score of this hole")
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.score_correction_requests WHERE scorecard_id = $1 AND seq = $2 AND status = 'requested')`,
		sid, in.Seq).Scan(&open); err != nil {
		return CorrectionRequest{}, err
	}
	if open {
		return CorrectionRequest{}, errs.Conflict("correction_pending", "a correction of this hole is already waiting for approval")
	}
	qid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.score_correction_requests (id, property_id, scorecard_id, seq, strokes, current_strokes, reason, requested_by, requested_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, qid, sc.PropertyID, sid, in.Seq, in.Strokes, cur, in.Reason, actorPtr(ctx), nullStr(who)); err != nil {
		return CorrectionRequest{}, err
	}
	out, err := m.correctionRequest(ctx, tx, qid)
	if err != nil {
		return out, err
	}
	if err := m.live(ctx, tx, "golf.pace", sc.PropertyID, "score_correction_requested", qid.String(), map[string]any{"player": sc.PlayerName, "seq": in.Seq}); err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.scorecard", sid, sc.PlayerName, "correction_request", sc.PropertyID, nil, out, in.Reason)
}

func (m *Module) correctionRequest(ctx context.Context, q dbtx.Querier, qid uuid.UUID) (CorrectionRequest, error) {
	rows, err := q.Query(ctx, correctionSelect+` WHERE q.id = $1`, qid)
	return handle.One[CorrectionRequest](rows, err, "correction request")
}

// DecideCorrection approves (applies the strokes) or rejects a request.
func (m *Module) DecideCorrection(ctx context.Context, tx pgx.Tx, property, qid uuid.UUID, approve bool, in CorrectionDecision) (CorrectionRequest, error) {
	var sid uuid.UUID
	var status string
	var p uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT scorecard_id, status, property_id FROM golf.score_correction_requests WHERE id = $1 FOR UPDATE`, qid).Scan(&sid, &status, &p); err != nil {
		if dbtx.IsNoRows(err) {
			return CorrectionRequest{}, errs.NotFound("correction request")
		}
		return CorrectionRequest{}, err
	}
	if p != property {
		return CorrectionRequest{}, errs.NotFound("correction request")
	}
	if status != "requested" {
		return CorrectionRequest{}, errs.Conflict("correction_decided", "the request is already "+status)
	}
	req, err := m.correctionRequest(ctx, tx, qid)
	if err != nil {
		return req, err
	}
	next := "rejected"
	if approve {
		next = "approved"
		sc, err := m.lockCard(ctx, tx, sid)
		if err != nil {
			return req, err
		}
		strokes := req.Strokes
		reason := "player request: " + req.Reason
		if _, err := m.applyCorrection(ctx, tx, sc, CorrectionInput{Entries: []ScoreEntry{{Seq: req.Seq, Strokes: &strokes}}, Reason: reason}, "player_request"); err != nil {
			return req, err
		}
	} else if strings.TrimSpace(in.Note) == "" {
		return req, handle.Invalid("note", "required", "tell the player why the correction is rejected")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.score_correction_requests SET status = $2, decided_at = now(), decided_by = $3, decision_note = $4 WHERE id = $1`,
		qid, next, actorPtr(ctx), nullStr(strings.TrimSpace(in.Note))); err != nil {
		return req, err
	}
	out, err := m.correctionRequest(ctx, tx, qid)
	if err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.scorecard", sid, req.PlayerName, "correction_"+next, property, req, out, in.Note)
}

func (m *Module) registerCorrectionRequests(reg *route.Registry, add func(tag string, rt route.Route)) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "golf", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/golf/scorecards/{id}/correction-requests", Summary: "My correction requests of a scorecard",
		Response: CorrectionRequest{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CorrectionRequest], error) {
			sid, _, err := m.myCard(ctx, tx, r)
			if err != nil {
				return httpx.Page[CorrectionRequest]{}, err
			}
			return handle.Page(handle.List[CorrectionRequest](tx.Query(ctx, correctionSelect+` WHERE q.scorecard_id = $1 ORDER BY q.created_at DESC`, sid)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/scorecards/{id}/correction-requests", Summary: "Ask for a score correction (round completed)",
		Request: CorrectionRequestInput{}, Response: CorrectionRequest{}, Status: http.StatusCreated,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CorrectionRequestInput) (CorrectionRequest, error) {
			sid, who, err := m.myCard(ctx, tx, r)
			if err != nil {
				return CorrectionRequest{}, err
			}
			return m.RequestCorrection(ctx, tx, sid, who.Name, in)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/golf/scorecards/{id}:share", Summary: "Share my scorecard: public PDF link (print, download, WhatsApp)",
		Response: ScorecardShare{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ScorecardShare, error) {
			sid, who, err := m.myCard(ctx, tx, r)
			if err != nil {
				return ScorecardShare{}, err
			}
			return m.ShareScorecard(ctx, tx, who.PropertyID, sid, requestOrigin(r), ScorecardShareInput{})
		})})
	add("Scoring", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/score-correction-requests", Summary: "Score correction requests from players",
		Permission: "golf.scorecard.correct", Response: CorrectionRequest{}, List: true, Query: []route.Param{{Name: "filter[status]", Enum: []string{"requested", "approved", "rejected"}}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CorrectionRequest], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[CorrectionRequest](tx.Query(ctx, correctionSelect+` WHERE q.property_id = $1 AND ($2 = '' OR q.status = $2)
				ORDER BY q.status = 'requested' DESC, q.created_at DESC LIMIT $3`, handle.Property(ctx), lp.Filters["status"], lp.Limit)))
		})})
	for _, d := range []struct {
		verb    string
		approve bool
	}{{"approve", true}, {"reject", false}} {
		add("Scoring", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/score-correction-requests/{id}:" + d.verb, Summary: "Score correction request: " + d.verb,
			Permission: "golf.scorecard.correct", Request: CorrectionDecision{}, Response: CorrectionRequest{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CorrectionDecision) (CorrectionRequest, error) {
				qid, err := handle.ID(r)
				if err != nil {
					return CorrectionRequest{}, err
				}
				return m.DecideCorrection(ctx, tx, handle.Property(ctx), qid, d.approve, in)
			})})
	}
}
