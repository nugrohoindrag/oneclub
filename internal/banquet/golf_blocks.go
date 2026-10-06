package banquet

// Golf for events (PRD P3 FR-EVT-03): an event may use the golf course (a
// corporate outing, a golf day before the gala dinner). The event records
// the golf block it needs; once the event is Definite the block is
// requested from golf (banquet.golf_block_requested), which closes the tee
// times of the window with an ordinary course block (reason private event)
// so they can no longer be sold; cancelling the event or the block releases
// it (banquet.golf_block_released). Golf decodes the events by name; banquet
// keeps a snapshot of the course and never imports golf.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Golf block events published (docs/p3-p4-contracts.md).
const (
	EventGolfBlockRequested = "banquet.golf_block_requested"
	EventGolfBlockReleased  = "banquet.golf_block_released"
)

// EventGolfBlock is a golf course block used by an event.
type EventGolfBlock struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	EventID        uuid.UUID  `json:"eventId" db:"event_id"`
	CourseID       uuid.UUID  `json:"courseId" db:"course_id"`
	CourseName     string     `json:"courseName" db:"course_name"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId" db:"playing_route_id"`
	Start          time.Time  `json:"start" db:"start_at"`
	End            time.Time  `json:"end" db:"end_at"`
	Notes          *string    `json:"notes" db:"notes"`
	Status         string     `json:"status" db:"status" enum:"pending,requested,released" doc:"Pending until the event is Definite; requested = tee times blocked by golf"`
	RequestedAt    *time.Time `json:"requestedAt" db:"requested_at"`
	ReleasedAt     *time.Time `json:"releasedAt" db:"released_at"`
}

// EventGolfBlockInput asks for a golf course block for an event.
type EventGolfBlockInput struct {
	CourseID       uuid.UUID  `json:"courseId"`
	PlayingRouteID *uuid.UUID `json:"playingRouteId,omitempty" doc:"Only this playing route (default: the whole course)"`
	Start          *time.Time `json:"start,omitempty" doc:"Default: the event start"`
	End            *time.Time `json:"end,omitempty" doc:"Default: the event end"`
	Notes          string     `json:"notes,omitempty"`
}

const golfBlockSelect = `SELECT id, event_id, course_id, course_name, playing_route_id, start_at, end_at, notes, status, requested_at, released_at
	FROM banquet.event_golf_blocks`

func (m *Module) golfBlocks(ctx context.Context, q dbtx.Querier, eid uuid.UUID) ([]EventGolfBlock, error) {
	return handle.List[EventGolfBlock](q.Query(ctx, golfBlockSelect+` WHERE event_id = $1 ORDER BY start_at, id`, eid))
}

func (m *Module) golfBlock(ctx context.Context, q dbtx.Querier, gid uuid.UUID) (EventGolfBlock, error) {
	rows, err := q.Query(ctx, golfBlockSelect+` WHERE id = $1`, gid)
	return handle.One[EventGolfBlock](rows, err, "golf block")
}

// AddGolfBlock records the golf block of an event; it is requested from
// golf at once when the event is Definite, else when it becomes Definite.
func (m *Module) AddGolfBlock(ctx context.Context, tx pgx.Tx, eid uuid.UUID, in EventGolfBlockInput) (EventGolfBlock, error) {
	e, err := m.lock(ctx, tx, eid)
	if err != nil {
		return EventGolfBlock{}, err
	}
	if !e.open() {
		return EventGolfBlock{}, errs.Conflict("event_closed", "a "+e.Status+" event cannot block the golf course")
	}
	var course string
	if err := tx.QueryRow(ctx, `SELECT name FROM golf.courses WHERE id = $1 AND property_id = $2`, in.CourseID, e.PropertyID).Scan(&course); err != nil {
		if dbtx.IsNoRows(err) {
			return EventGolfBlock{}, handle.Invalid("courseId", "not_found", "golf course not found")
		}
		return EventGolfBlock{}, err
	}
	if in.PlayingRouteID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM golf.playing_routes WHERE id = $1 AND course_id = $2)`, *in.PlayingRouteID, in.CourseID).
			Scan(&ok); err != nil {
			return EventGolfBlock{}, err
		}
		if !ok {
			return EventGolfBlock{}, handle.Invalid("playingRouteId", "not_found", "playing route of the course not found")
		}
	}
	start, end := e.Start, e.End
	if in.Start != nil {
		start = *in.Start
	}
	if in.End != nil {
		end = *in.End
	}
	if !end.After(start) {
		return EventGolfBlock{}, handle.Invalid("end", "invalid_period", "end must be after start")
	}
	gid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO banquet.event_golf_blocks (id, property_id, event_id, course_id, course_name, playing_route_id, start_at, end_at,
		notes, status, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$10)`, gid, e.PropertyID, eid, in.CourseID, course,
		in.PlayingRouteID, start, end, nzs(strings.TrimSpace(in.Notes)), actor(ctx)); err != nil {
		return EventGolfBlock{}, err
	}
	if err := m.touch(ctx, tx, eid); err != nil {
		return EventGolfBlock{}, err
	}
	if e.Status == StatusDefinite {
		if err := m.requestGolfBlocks(ctx, tx, e); err != nil {
			return EventGolfBlock{}, err
		}
	}
	out, err := m.golfBlock(ctx, tx, gid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionCreate, EntityType: "banquet.event_golf_block",
		EntityID: gid.String(), EntityLabel: e.Number + " · " + course, PropertyID: &e.PropertyID, After: out})
}

// ReleaseGolfBlock releases a golf block of an event (the tee times open
// again unless another block covers them).
func (m *Module) ReleaseGolfBlock(ctx context.Context, tx pgx.Tx, gid uuid.UUID, reason string) (EventGolfBlock, error) {
	before, err := m.golfBlock(ctx, tx, gid)
	if err != nil {
		return before, err
	}
	if before.Status == "released" {
		return before, errs.Conflict("already_released", "the golf block is already released")
	}
	e, err := m.lock(ctx, tx, before.EventID)
	if err != nil {
		return before, err
	}
	if err := m.releaseGolfBlock(ctx, tx, e, before, "released", reason); err != nil {
		return before, err
	}
	out, err := m.golfBlock(ctx, tx, gid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "banquet", Action: audit.ActionStatusChange, EntityType: "banquet.event_golf_block",
		EntityID: gid.String(), EntityLabel: e.Number + " · " + before.CourseName, PropertyID: &e.PropertyID, Reason: reason, Before: before, After: out})
}

// golfBlockPayload is the payload of banquet.golf_block_requested / released.
func golfBlockPayload(e BanquetEvent, b EventGolfBlock, reason string) map[string]any {
	return map[string]any{"golfBlockId": b.ID, "eventId": e.ID, "eventNumber": e.Number, "title": e.Title, "courseId": b.CourseID,
		"playingRouteId": b.PlayingRouteID, "startsAt": b.Start, "endsAt": b.End, "notes": b.Notes, "reason": reason}
}

// requestGolfBlocks asks golf for the pending blocks of a Definite event.
func (m *Module) requestGolfBlocks(ctx context.Context, tx pgx.Tx, e BanquetEvent) error {
	rows, err := tx.Query(ctx, golfBlockSelect+` WHERE event_id = $1 AND status = 'pending' ORDER BY start_at FOR UPDATE`, e.ID)
	bs, err := handle.List[EventGolfBlock](rows, err)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if _, err := tx.Exec(ctx, `UPDATE banquet.event_golf_blocks SET status = 'requested', requested_at = now(), released_at = NULL WHERE id = $1`,
			b.ID); err != nil {
			return err
		}
		if _, err := m.Events.Publish(ctx, tx, EventGolfBlockRequested, "banquet.event_golf_block", &b.ID, &e.PropertyID,
			golfBlockPayload(e, b, "")); err != nil {
			return err
		}
	}
	return nil
}

// releaseGolfBlock releases one block: a requested block is lifted in golf.
// status is released (event cancelled, block removed) or pending (the event
// is no longer Definite: requested again when it is).
func (m *Module) releaseGolfBlock(ctx context.Context, tx pgx.Tx, e BanquetEvent, b EventGolfBlock, status, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE banquet.event_golf_blocks SET status = $2, released_at = CASE WHEN $2 = 'released' THEN now() END,
		updated_by = $3 WHERE id = $1`, b.ID, status, actor(ctx)); err != nil {
		return err
	}
	if b.Status != "requested" {
		return nil
	}
	_, err := m.Events.Publish(ctx, tx, EventGolfBlockReleased, "banquet.event_golf_block", &b.ID, &e.PropertyID, golfBlockPayload(e, b, reason))
	return err
}

// releaseGolfBlocks releases every live block of an event.
func (m *Module) releaseGolfBlocks(ctx context.Context, tx pgx.Tx, e BanquetEvent, status, reason string) error {
	bs, err := m.golfBlocks(ctx, tx, e.ID)
	if err != nil {
		return err
	}
	for _, b := range bs {
		if b.Status == "released" || (status == "pending" && b.Status == "pending") {
			continue
		}
		if err := m.releaseGolfBlock(ctx, tx, e, b, status, reason); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) registerGolfBlocks(reg *route.Registry) {
	db := m.DB
	m.add(reg, route.Route{Method: http.MethodGet, Path: "/api/v1/banquet/events/{id}/golf-blocks", Summary: "Golf course blocks of the event (FR-EVT-03)",
		Permission: "banquet.event.view", Response: EventGolfBlock{}, List: true,
		Handler: read(db, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request) (httpx.Page[EventGolfBlock], error) {
			return handle.Page(m.golfBlocks(ctx, tx, eid))
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/events/{id}/golf-blocks",
		Summary: "Block the golf course for the event (tee times closed once the event is Definite)", Permission: "banquet.event.hold",
		Request: EventGolfBlockInput{}, Response: EventGolfBlock{},
		Handler: write(db, http.StatusCreated, "events", "event", func(ctx context.Context, tx pgx.Tx, eid uuid.UUID, r *http.Request, in EventGolfBlockInput) (EventGolfBlock, error) {
			return m.AddGolfBlock(ctx, tx, eid, in)
		})})
	m.add(reg, route.Route{Method: http.MethodPost, Path: "/api/v1/banquet/event-golf-blocks/{id}:release", Summary: "Release a golf block of an event",
		Permission: "banquet.event.hold", Request: BanquetReasonInput{}, Response: EventGolfBlock{},
		Handler: write(db, http.StatusOK, "event_golf_blocks", "golf block", func(ctx context.Context, tx pgx.Tx, gid uuid.UUID, r *http.Request, in BanquetReasonInput) (EventGolfBlock, error) {
			return m.ReleaseGolfBlock(ctx, tx, gid, in.Reason)
		})})
}
