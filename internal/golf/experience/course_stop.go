package experience

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/golf"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

/*
 * Rain stop / lightning warning from the Course Monitor (demo feedback
 * 10 Oct 2026 #29): the Marshal stops play on the whole course from the
 * tablet or phone carried on the course. Every flight in play pauses at
 * once (the pace freezes), every caddy tablet gets the message, the tee-off
 * is held by the course status, and Resume play restarts the flights the
 * stop paused. Each stop is kept per course for the rain check and the
 * management report.
 */

// CourseStop is one rain stop or lightning warning of a course.
type CourseStop struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	CourseID  uuid.UUID  `json:"courseId" db:"course_id"`
	Kind      string     `json:"kind" db:"kind" enum:"rain_stop,lightning_warning"`
	Reason    *string    `json:"reason" db:"reason"`
	StartedAt time.Time  `json:"startedAt" db:"started_at"`
	EndedAt   *time.Time `json:"endedAt" db:"ended_at"`
	Flights   int        `json:"flights" db:"flights" doc:"Flights in play paused by the stop"`
	Minutes   int        `json:"minutes" db:"minutes"`
	StartedBy *string    `json:"startedBy" db:"started_by_name"`
	EndedBy   *string    `json:"endedBy" db:"ended_by_name"`
}

// CourseStopInput starts a stop.
type CourseStopInput struct {
	CourseID uuid.UUID `json:"courseId"`
	Kind     string    `json:"kind" enum:"rain_stop,lightning_warning"`
	Reason   string    `json:"reason,omitempty"`
}

const stopSelect = `SELECT s.id, s.course_id, s.kind, s.reason, s.started_at, s.ended_at, s.flights,
	(extract(epoch FROM coalesce(s.ended_at, now()) - s.started_at) / 60)::int AS minutes,
	(SELECT full_name FROM platform.users u WHERE u.id = s.started_by) AS started_by_name,
	(SELECT full_name FROM platform.users u WHERE u.id = s.ended_by) AS ended_by_name
	FROM golf.course_stops s`

// StartCourseStop stops play on the course.
func (m *Module) StartCourseStop(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CourseStopInput) (CourseStop, error) {
	if in.Kind != "rain_stop" && in.Kind != "lightning_warning" {
		return CourseStop{}, handle.Invalid("kind", "invalid", "rain_stop or lightning_warning")
	}
	var open *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM golf.course_stops WHERE course_id = $1 AND ended_at IS NULL`, in.CourseID).Scan(&open); err != nil && !dbtx.IsNoRows(err) {
		return CourseStop{}, err
	}
	if open != nil {
		return CourseStop{}, errs.Conflict("course_stopped", "play is already stopped on this course: resume play first")
	}
	reason := strings.TrimSpace(in.Reason)
	notes := map[string]string{"rain_stop": "Rain stop", "lightning_warning": "Lightning warning"}[in.Kind]
	if reason != "" {
		notes += ": " + reason
	}
	if _, err := m.Golf.UpdateCourseStatus(ctx, tx, property, in.CourseID, golf.CourseStatusRequest{Weather: in.Kind, Notes: &notes}); err != nil {
		return CourseStop{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.course_stops (id, property_id, course_id, kind, reason, started_by) VALUES ($1,$2,$3,$4,$5,$6)`,
		sid, property, in.CourseID, in.Kind, nullStr(reason), actorPtr(ctx)); err != nil {
		return CourseStop{}, err
	}
	flights, err := collectIDs(tx.Query(ctx, `SELECT id FROM golf.flights WHERE course_id = $1 AND property_id = $2 AND status = 'in_play'`, in.CourseID, property))
	if err != nil {
		return CourseStop{}, err
	}
	kind := "rain"
	if in.Kind == "lightning_warning" {
		kind = "lightning"
	}
	n := 0
	for _, fid := range flights {
		p, err := m.Golf.PauseRound(ctx, tx, fid, golf.PauseInput{Reason: kind, StopID: &sid})
		if err != nil {
			return CourseStop{}, err
		}
		if p.PausedAt != nil {
			n++
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.course_stops SET flights = $2 WHERE id = $1`, sid, n); err != nil {
		return CourseStop{}, err
	}
	if n > 0 {
		body := notes + " — stop play and go to the nearest shelter. Wait for the Marshal's Resume play."
		if _, err := m.SendCourseMessage(ctx, tx, property, CourseMessageInput{CourseID: in.CourseID, Body: body, Desk: "marshal"}); err != nil && !isNoFlight(err) {
			return CourseStop{}, err
		}
	}
	out, err := m.courseStop(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	if err := m.live(ctx, tx, "golf.pace", property, "course_stop", sid.String(), map[string]any{"kind": in.Kind, "flights": n}); err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.course_stop", sid, notes, "start", property, nil, out, reason)
}

func isNoFlight(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Code == "no_flight_in_play"
}

// EndCourseStop resumes play: the flights the stop paused restart together.
func (m *Module) EndCourseStop(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (CourseStop, error) {
	before, err := m.courseStop(ctx, tx, sid)
	if err != nil {
		return before, err
	}
	if before.EndedAt != nil {
		return before, nil
	}
	at := clock.Now()
	if _, err := tx.Exec(ctx, `UPDATE golf.course_stops SET ended_at = $2, ended_by = $3 WHERE id = $1`, sid, at, actorPtr(ctx)); err != nil {
		return before, err
	}
	flights, err := collectIDs(tx.Query(ctx, `SELECT DISTINCT p.flight_id FROM golf.flight_pauses p JOIN golf.flights f ON f.id = p.flight_id
		WHERE p.course_stop_id = $1 AND p.ended_at IS NULL AND f.paused_at IS NOT NULL`, sid))
	if err != nil {
		return before, err
	}
	for _, fid := range flights {
		if _, err := m.Golf.ResumeRound(ctx, tx, fid, golf.PauseInput{At: &at}); err != nil {
			return before, err
		}
	}
	notes := "Play resumed"
	if _, err := m.Golf.UpdateCourseStatus(ctx, tx, property, before.CourseID, golf.CourseStatusRequest{Weather: "normal", Notes: &notes}); err != nil {
		return before, err
	}
	if len(flights) > 0 {
		if _, err := m.SendCourseMessage(ctx, tx, property, CourseMessageInput{CourseID: before.CourseID, Body: "Play resumed — continue your round.", Desk: "marshal"}); err != nil && !isNoFlight(err) {
			return before, err
		}
	}
	out, err := m.courseStop(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	if err := m.live(ctx, tx, "golf.pace", property, "course_resumed", sid.String(), map[string]any{"flights": len(flights)}); err != nil {
		return out, err
	}
	return out, record(ctx, tx, "golf.course_stop", sid, "Play resumed", "end", property, before, out, "")
}

func (m *Module) courseStop(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (CourseStop, error) {
	rows, err := q.Query(ctx, stopSelect+` WHERE s.id = $1`, sid)
	return handle.One[CourseStop](rows, err, "course stop")
}

func (m *Module) registerCourseStops(add func(tag string, rt route.Route)) {
	db := m.DB
	add("Pace of Play", route.Route{Method: http.MethodGet, Path: "/api/v1/golf/course-stops", Summary: "Rain stops and lightning warnings (open first, then the day's history)",
		Permission: "golf.pace.view", Response: CourseStop{}, List: true, Query: []route.Param{{Name: "courseId"}, {Name: "date", Description: "Club day (default today)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[CourseStop], error) {
			p := handle.Property(ctx)
			loc := location(ctx, tx, p)
			d, err := handle.QueryDate(r, "date", localDay(clock.Now(), loc))
			if err != nil {
				return httpx.Page[CourseStop]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			return handle.Page(handle.List[CourseStop](tx.Query(ctx, stopSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.course_id::text = $2)
				AND (s.ended_at IS NULL OR (s.started_at >= $3 AND s.started_at < $3::timestamptz + interval '1 day'))
				ORDER BY s.ended_at IS NULL DESC, s.started_at DESC`, p, r.URL.Query().Get("courseId"), from)))
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-stops", Summary: "Rain stop / lightning warning: pause every flight on the course",
		Permission: "golf.course_status.update", Request: CourseStopInput{}, Response: CourseStop{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CourseStopInput) (CourseStop, error) {
			return m.StartCourseStop(ctx, tx, handle.Property(ctx), in)
		})})
	add("Pace of Play", route.Route{Method: http.MethodPost, Path: "/api/v1/golf/course-stops/{id}:resume", Summary: "Resume play after a rain stop",
		Permission: "golf.course_status.update", Response: CourseStop{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (CourseStop, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return CourseStop{}, err
			}
			return m.EndCourseStop(ctx, tx, handle.Property(ctx), sid)
		})})
}
