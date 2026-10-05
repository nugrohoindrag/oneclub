package golf

// Contract additions for PRD P3 EP-16 (golf/tournament, FR-TRN-02 and the
// shotgun start deferred by PRD P1 FR-TEE-04): a tournament round closes the
// course on P1's tee sheet with an ordinary course block (reason
// tournament), so public tee times of the window are blocked, bookings
// already made are listed for the golf staff and the tee sheet screens
// refresh. Additive only; P1 behaviour is unchanged.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notification"
	"oneclub/internal/platform/notify"
)

// TournamentBlockInput closes the course (or one playing route) for a
// tournament round.
type TournamentBlockInput struct {
	CourseID       uuid.UUID
	PlayingRouteID *uuid.UUID
	StartsAt       time.Time
	EndsAt         time.Time
	Notes          string // e.g. "Monthly Medal · round 1 (shotgun 07:00)"
}

// TournamentBlock creates a course block with reason tournament (same
// effect as Block Course Availability in the Back Office).
func (m *Module) TournamentBlock(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TournamentBlockInput) (Block, error) {
	if !in.EndsAt.After(in.StartsAt) {
		return Block{}, errs.Validation("invalid_period", "end must be after start", errs.Field("endsAt", "invalid", "after start"))
	}
	var courseName string
	if err := tx.QueryRow(ctx, `SELECT name FROM golf.courses WHERE id = $1 AND property_id = $2`, in.CourseID, property).Scan(&courseName); err != nil {
		if dbtx.IsNoRows(err) {
			return Block{}, errs.Validation("invalid_course", "course not found", errs.Field("courseId", "not_found", "course not found"))
		}
		return Block{}, err
	}
	bid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO golf.course_blocks (id, property_id, course_id, playing_route_id, starts_at, ends_at, reason, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,'tournament',$7,$8,$8)`, bid, property, in.CourseID, in.PlayingRouteID, in.StartsAt, in.EndsAt, nullStr(in.Notes),
		id.Ptr(actor(ctx))); err != nil {
		return Block{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET status = 'blocked', block_id = $1 WHERE course_id = $2 AND start_at >= $3 AND start_at < $4
		AND status = 'open' AND ($5::uuid IS NULL OR playing_route_id IS NULL OR playing_route_id = $5)`,
		bid, in.CourseID, in.StartsAt, in.EndsAt, in.PlayingRouteID); err != nil {
		return Block{}, err
	}
	out, err := loadBlock(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	loc := location(ctx, tx, property)
	if len(out.AffectedBookings) > 0 && m.Notify != nil {
		users, err := notification.UserIDsWithPermission(ctx, tx, "golf.booking.update", &property)
		if err != nil {
			return out, err
		}
		link := ""
		if m.Cfg != nil {
			link = m.Cfg.PublicBaseURL + "/golf/bookings"
		}
		if err := m.Notify.Send(ctx, tx, notify.Message{Event: "golf.bookings_affected", Category: "general", UserIDs: users, PropertyID: &property,
			Link: link, Data: map[string]any{"count": len(out.AffectedBookings), "reason": "tournament", "course": courseName,
				"period": in.StartsAt.In(loc).Format("02 Jan 15:04") + " – " + in.EndsAt.In(loc).Format("15:04"), "link": link}}); err != nil {
			return out, err
		}
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "blocked", in.CourseID, localDay(in.StartsAt, loc), nil); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionCreate, EntityType: "golf.course_block", EntityID: bid.String(),
		EntityLabel: courseName + " tournament", PropertyID: &property, After: map[string]any{"startsAt": in.StartsAt, "endsAt": in.EndsAt,
			"reason": "tournament", "notes": strings.TrimSpace(in.Notes), "blockedSlots": out.BlockedSlots, "affectedBookings": len(out.AffectedBookings)}})
}

// TournamentUnblock cancels a tournament course block (round moved or
// tournament cancelled); the slots re-open unless another block covers them.
func (m *Module) TournamentUnblock(ctx context.Context, tx pgx.Tx, property, blockID uuid.UUID) error {
	var course uuid.UUID
	var starts time.Time
	err := tx.QueryRow(ctx, `UPDATE golf.course_blocks SET status = 'cancelled', updated_by = $3 WHERE id = $1 AND property_id = $2 AND status = 'active'
		RETURNING course_id, starts_at`, blockID, property, id.Ptr(actor(ctx))).Scan(&course, &starts)
	if dbtx.IsNoRows(err) {
		return nil // already released
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tee_times t SET block_id = (SELECT cb.id FROM golf.course_blocks cb WHERE cb.course_id = t.course_id
		AND cb.status = 'active' AND cb.starts_at <= t.start_at AND cb.ends_at > t.start_at LIMIT 1) WHERE t.block_id = $1`, blockID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.tee_times SET status = CASE WHEN block_id IS NULL THEN 'open' ELSE 'blocked' END
		WHERE course_id = $1 AND status = 'blocked' AND (block_id IS NULL OR block_id <> $2)`, course, blockID); err != nil {
		return err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "unblocked", course, localDay(starts, location(ctx, tx, property)), nil); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: audit.ActionStatusChange, EntityType: "golf.course_block", EntityID: blockID.String(),
		PropertyID: &property, After: map[string]any{"status": "cancelled", "reason": "tournament"}})
}
