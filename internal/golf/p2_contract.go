package golf

// Contract additions for PRD P2 (§5.4.2 C8): public functions the P2 golf
// experience (internal/golf/experience) needs to change P1 golf data without
// writing P1 tables itself. Additive only; existing P1 behaviour is not
// changed. Review: P1 developer.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
)

// StartCartAssignment sends an assigned golf cart out at once (In Use), as
// tee-off does for the flight's carts. Used for a replacement cart handed to
// a flight already in play (PRD P2 FR-CTL-05).
func (m *Module) StartCartAssignment(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID) error {
	var cart uuid.UUID
	var status string
	var day = clock.Now()
	if err := tx.QueryRow(ctx, `SELECT golf_cart_id, status, play_date FROM golf.golf_cart_assignments WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		aid, property).Scan(&cart, &status, &day); err != nil {
		return errs.NotFound("golf cart assignment")
	}
	if status != "assigned" {
		return errs.Conflict("assignment_not_assigned", "only an assigned golf cart can go out")
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_cart_assignments SET status = 'in_use', out_at = now() WHERE id = $1`, aid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.golf_carts SET readiness = 'in_use', readiness_changed_at = now() WHERE id = $1`, cart); err != nil {
		return err
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "golf_cart_out", EntityType: "golf.golf_cart_assignment", EntityID: aid.String(),
		PropertyID: &property})
}
