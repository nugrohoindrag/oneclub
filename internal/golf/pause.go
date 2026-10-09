package golf

// Rain during the round (demo feedback 9 Oct 2026): the caddy reports rain
// on the tablet (or the starter / front desk does) and the play time of the
// flight stops; when play resumes the paused time is left out of the actual
// play time. A rain stop before half of the holes gives a full rain check,
// so the players can reschedule (Weather Policy fullCreditBeforeHalf).
//
// Breaks (demo feedback 10 Oct 2026 #31): a halfway break, the turn or a tee
// house stop is recorded the same way but keeps counting as play time; the
// pace allows the break allowance of the Golf Policy. Every pause is kept in
// golf.flight_pauses (history, pace report) and reaches the Marshal's
// Course Monitor at once (#28).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/realtime"
)

// BreakKinds are the pauses that keep counting as play time.
var BreakKinds = map[string]string{"halfway": "Halfway break", "turn": "The turn", "tee_house": "Tee house stop", "break_other": "Break"}

// IsBreak reports whether a pause reason is a break (not a weather stop).
func IsBreak(reason string) bool {
	_, ok := BreakKinds[reason]
	return ok
}

// PauseInput pauses or resumes a flight.
type PauseInput struct {
	Reason string     `json:"reason,omitempty" enum:"rain,lightning,other,halfway,turn,tee_house,break_other" doc:"Default rain; halfway, turn, tee_house and break_other are breaks (play time keeps counting)"`
	At     *time.Time `json:"at,omitempty" doc:"When it happened on the tablet (offline replay)"`
	Source string     `json:"source,omitempty" enum:"tablet,starter,marshal" doc:"Default tablet"`
	// set by a course rain stop
	StopID *uuid.UUID `json:"-"`
}

// FlightPause is the pause state of a flight.
type FlightPause struct {
	FlightID      uuid.UUID  `json:"flightId"`
	Status        string     `json:"status"`
	PausedAt      *time.Time `json:"pausedAt"`
	PauseReason   *string    `json:"pauseReason"`
	PausedSeconds int        `json:"pausedSeconds"`
	BreakSeconds  int        `json:"breakSeconds" doc:"Time on breaks (counted in the play time)"`
}

func flightPause(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (FlightPause, uuid.UUID, uuid.UUID, time.Time, error) {
	p := FlightPause{FlightID: fid}
	var property, course uuid.UUID
	var day time.Time
	err := q.QueryRow(ctx, `SELECT status, paused_at, pause_reason, paused_seconds, break_seconds, property_id, course_id, play_date FROM golf.flights WHERE id = $1`, fid).
		Scan(&p.Status, &p.PausedAt, &p.PauseReason, &p.PausedSeconds, &p.BreakSeconds, &property, &course, &day)
	if dbtx.IsNoRows(err) {
		return p, property, course, day, errs.NotFound("flight")
	}
	return p, property, course, day, err
}

// PauseRound stops the play time of a flight in play (rain), or starts a
// break.
func (m *Module) PauseRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in PauseInput) (FlightPause, error) {
	before, property, course, day, err := flightPause(ctx, tx, fid)
	if err != nil {
		return before, err
	}
	if before.Status != "in_play" {
		return before, errs.Conflict("not_in_play", "only a flight on the course can pause")
	}
	reason := in.Reason
	if reason == "" {
		reason = "rain"
	}
	switch reason {
	case "rain", "lightning", "other":
	default:
		if !IsBreak(reason) {
			return before, errs.Validation("invalid_reason", "unknown pause reason", errs.Field("reason", "invalid", "rain, lightning, other or a break"))
		}
	}
	if before.PausedAt != nil {
		// a rain stop overrides a break; otherwise already paused (offline replay)
		if before.PauseReason == nil || !IsBreak(*before.PauseReason) || IsBreak(reason) {
			return before, nil
		}
		if err := closePause(ctx, tx, fid); err != nil {
			return before, err
		}
	}
	at := clock.Now()
	if in.At != nil && in.At.Before(at) {
		at = *in.At
	}
	source := in.Source
	if in.StopID != nil {
		source = "course_stop"
	}
	if source == "" {
		source = "tablet"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET paused_at = $2, pause_reason = $3 WHERE id = $1`, fid, at, reason); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.flight_pauses (id, property_id, flight_id, kind, source, course_stop_id, hole_seq, started_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,(SELECT current_seq FROM golf.round_progress WHERE flight_id = $3),$7,$8)`,
		id.New(), property, fid, reason, source, in.StopID, at, id.Ptr(actor(ctx))); err != nil {
		return before, err
	}
	after, _, _, _, err := flightPause(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	ev := map[string]any{"flightId": fid.String(), "reason": reason, "break": IsBreak(reason), "since": at}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "round_paused", course, day, ev); err != nil {
		return after, err
	}
	// the Marshal's Course Monitor listens to golf.pace (#28)
	if err := realtime.Publish(ctx, tx, "golf.pace", "round_paused", &property, ev); err != nil {
		return after, err
	}
	label := "Flight paused: " + reason
	if IsBreak(reason) {
		label = "Flight on break: " + BreakKinds[reason]
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "pause", EntityType: "golf.flight", EntityID: fid.String(), EntityLabel: label,
		PropertyID: &property, Reason: reason, Before: before, After: after})
}

// ResumeRound restarts the play time; the paused time is not play time (a
// break's time is).
func (m *Module) ResumeRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in PauseInput) (FlightPause, error) {
	before, property, course, day, err := flightPause(ctx, tx, fid)
	if err != nil {
		return before, err
	}
	if before.PausedAt == nil {
		return before, nil
	}
	at := clock.Now()
	if in.At != nil && in.At.Before(at) && in.At.After(*before.PausedAt) {
		at = *in.At
	}
	if err := endPause(ctx, tx, fid, before, at); err != nil {
		return before, err
	}
	after, _, _, _, err := flightPause(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	ev := map[string]any{"flightId": fid.String()}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "round_resumed", course, day, ev); err != nil {
		return after, err
	}
	if err := realtime.Publish(ctx, tx, "golf.pace", "round_resumed", &property, ev); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "resume", EntityType: "golf.flight", EntityID: fid.String(), EntityLabel: "Flight resumed",
		PropertyID: &property, Before: before, After: after})
}

// endPause closes the open pause at time at: a weather pause adds to the
// paused time, a break to the break time.
func endPause(ctx context.Context, tx pgx.Tx, fid uuid.UUID, p FlightPause, at time.Time) error {
	secs := max(int(at.Sub(*p.PausedAt).Seconds()), 0)
	col := "paused_seconds"
	if p.PauseReason != nil && IsBreak(*p.PauseReason) {
		col = "break_seconds"
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET paused_at = NULL, `+col+` = `+col+` + $2 WHERE id = $1`, fid, secs); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE golf.flight_pauses SET ended_at = $2, seconds = greatest(0, extract(epoch FROM $2::timestamptz - started_at)::int)
		WHERE flight_id = $1 AND ended_at IS NULL`, fid, at)
	return err
}

// closePause ends an open pause when the round finishes (rain stop).
func closePause(ctx context.Context, tx pgx.Tx, fid uuid.UUID) error {
	p, _, _, _, err := flightPause(ctx, tx, fid)
	if err != nil || p.PausedAt == nil {
		return err
	}
	return endPause(ctx, tx, fid, p, clock.Now())
}

func (m *Module) pauseHTTP(resume bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		in, ok := decode[PauseInput](w, r)
		if !ok {
			return
		}
		if in.Source == "" {
			in.Source = "starter"
		}
		m.write(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
			var p uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT property_id FROM golf.flights WHERE id = $1`, fid).Scan(&p); err != nil || p != prop(ctx) {
				return nil, errs.NotFound("flight")
			}
			if resume {
				return m.ResumeRound(ctx, tx, fid, in)
			}
			return m.PauseRound(ctx, tx, fid, in)
		})
	}
}
