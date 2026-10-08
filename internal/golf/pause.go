package golf

// Rain during the round (demo feedback 9 Oct 2026): the caddy reports rain
// on the tablet (or the starter / front desk does) and the play time of the
// flight stops; when play resumes the paused time is left out of the actual
// play time. A rain stop before half of the holes gives a full rain check,
// so the players can reschedule (Weather Policy fullCreditBeforeHalf).

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
	"oneclub/internal/platform/audit"
)

// PauseInput pauses or resumes a flight.
type PauseInput struct {
	Reason string     `json:"reason,omitempty" enum:"rain,lightning,other" doc:"Default rain"`
	At     *time.Time `json:"at,omitempty" doc:"When it happened on the tablet (offline replay)"`
}

// FlightPause is the pause state of a flight.
type FlightPause struct {
	FlightID      uuid.UUID  `json:"flightId"`
	Status        string     `json:"status"`
	PausedAt      *time.Time `json:"pausedAt"`
	PauseReason   *string    `json:"pauseReason"`
	PausedSeconds int        `json:"pausedSeconds"`
}

func flightPause(ctx context.Context, q dbtx.Querier, fid uuid.UUID) (FlightPause, uuid.UUID, uuid.UUID, time.Time, error) {
	p := FlightPause{FlightID: fid}
	var property, course uuid.UUID
	var day time.Time
	err := q.QueryRow(ctx, `SELECT status, paused_at, pause_reason, paused_seconds, property_id, course_id, play_date FROM golf.flights WHERE id = $1`, fid).
		Scan(&p.Status, &p.PausedAt, &p.PauseReason, &p.PausedSeconds, &property, &course, &day)
	if dbtx.IsNoRows(err) {
		return p, property, course, day, errs.NotFound("flight")
	}
	return p, property, course, day, err
}

// PauseRound stops the play time of a flight in play (rain).
func (m *Module) PauseRound(ctx context.Context, tx pgx.Tx, fid uuid.UUID, in PauseInput) (FlightPause, error) {
	before, property, course, day, err := flightPause(ctx, tx, fid)
	if err != nil {
		return before, err
	}
	if before.Status != "in_play" {
		return before, errs.Conflict("not_in_play", "only a flight on the course can pause")
	}
	if before.PausedAt != nil {
		return before, nil // already paused (offline replay)
	}
	reason := in.Reason
	if reason == "" {
		reason = "rain"
	}
	at := clock.Now()
	if in.At != nil && in.At.Before(at) {
		at = *in.At
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET paused_at = $2, pause_reason = $3 WHERE id = $1`, fid, at, reason); err != nil {
		return before, err
	}
	after, _, _, _, err := flightPause(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "round_paused", course, day, map[string]any{"flightId": fid.String(), "reason": reason}); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "pause", EntityType: "golf.flight", EntityID: fid.String(), EntityLabel: "Flight paused: " + reason,
		PropertyID: &property, Reason: reason, Before: before, After: after})
}

// ResumeRound restarts the play time; the paused time is not play time.
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
	secs := int(at.Sub(*before.PausedAt).Seconds())
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET paused_at = NULL, paused_seconds = paused_seconds + $2 WHERE id = $1`, fid, max(secs, 0)); err != nil {
		return before, err
	}
	after, _, _, _, err := flightPause(ctx, tx, fid)
	if err != nil {
		return after, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "round_resumed", course, day, map[string]any{"flightId": fid.String()}); err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "resume", EntityType: "golf.flight", EntityID: fid.String(), EntityLabel: "Flight resumed",
		PropertyID: &property, Before: before, After: after})
}

// closePause ends an open pause when the round finishes (rain stop).
func closePause(ctx context.Context, tx pgx.Tx, fid uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE golf.flights SET paused_seconds = paused_seconds + greatest(0, extract(epoch FROM now() - paused_at)::int), paused_at = NULL
		WHERE id = $1 AND paused_at IS NOT NULL`, fid)
	return err
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
