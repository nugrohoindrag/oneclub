package golf

// Golf course blocks of banquet events (PRD P3 FR-EVT-03): a Definite event
// that uses the golf course asks for a block (banquet.golf_block_requested);
// golf closes the tee times of the window with an ordinary course block
// (reason private event, source = the event's golf block) — the same effect
// as Block Course Availability: public tee times are blocked, bookings
// already made are listed for the golf staff and the tee sheet screens
// refresh. banquet.golf_block_released lifts it. Decoded by name.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/outbox"
)

// Banquet events consumed (docs/p3-p4-contracts.md).
const (
	EventGolfBlockRequested = "banquet.golf_block_requested"
	EventGolfBlockReleased  = "banquet.golf_block_released"
	// BlockSourceBanquet is the source type of course blocks of events.
	BlockSourceBanquet = "banquet.event_golf_block"
)

// OnEventGolfBlock blocks or re-opens the tee times of an event's golf
// block (idempotent per golf block).
func (m *Module) OnEventGolfBlock(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	var p struct {
		GolfBlockID    uuid.UUID  `json:"golfBlockId"`
		EventNumber    string     `json:"eventNumber"`
		Title          string     `json:"title"`
		CourseID       uuid.UUID  `json:"courseId"`
		PlayingRouteID *uuid.UUID `json:"playingRouteId"`
		StartsAt       time.Time  `json:"startsAt"`
		EndsAt         time.Time  `json:"endsAt"`
		Notes          *string    `json:"notes"`
	}
	if err := ev.Decode(&p); err != nil || ev.PropertyID == nil || p.GolfBlockID == uuid.Nil {
		return nil //nolint:nilerr // foreign payload
	}
	property := *ev.PropertyID
	ctx = withProperty(dbtx.System(ctx), property)
	var active *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM golf.course_blocks WHERE source_type = $1 AND source_id = $2 AND status = 'active'`, BlockSourceBanquet,
		p.GolfBlockID).Scan(&active)
	if err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	switch ev.Type {
	case EventGolfBlockRequested:
		if active != nil {
			return nil
		}
		notes := strings.TrimSpace("Event " + p.EventNumber + " · " + p.Title)
		if p.Notes != nil && *p.Notes != "" {
			notes += " — " + *p.Notes
		}
		src := p.GolfBlockID
		_, err := m.courseBlock(ctx, tx, property, TournamentBlockInput{CourseID: p.CourseID, PlayingRouteID: p.PlayingRouteID, StartsAt: p.StartsAt,
			EndsAt: p.EndsAt, Notes: notes}, "private_event", BlockSourceBanquet, &src)
		return err
	case EventGolfBlockReleased:
		if active == nil {
			return nil
		}
		return m.liftBlock(ctx, tx, property, *active, "private_event")
	}
	return nil
}
