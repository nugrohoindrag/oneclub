package iam

// Time-bound role assignments (PRD P4 FR-ACC-09 / §16 #18): an assignment
// may carry an expiry; after it the role grants nothing (loadAssignments
// ignores it on every request, so access ends without a logout). The
// Auditor role must be assigned with one. Assigning a role again after its
// assignment expired renews that assignment for the new period.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/catalog"
)

// checkValidUntil validates the expiry of an assignment of role code.
func checkValidUntil(code, name string, until *time.Time) error {
	if until == nil {
		if catalog.IsTimeBound(code) {
			return errs.Validation("valid_until_required", name+" is assigned for a limited period", errs.Field("validUntil", "required",
				"set the end of the access period"))
		}
		return nil
	}
	if !until.After(clock.Now()) {
		return errs.Validation("valid_until_past", "the access period must end in the future", errs.Field("validUntil", "invalid", "must be in the future"))
	}
	return nil
}

// renewExpired extends an expired assignment of the same user, role and
// property to the new expiry; renewed is false when there is none (a new
// assignment is inserted, or a valid one is a duplicate).
func renewExpired(ctx context.Context, tx pgx.Tx, userID uuid.UUID, a AssignmentRequest) (uuid.UUID, bool, error) {
	var aid uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE platform.role_assignments SET valid_until = $4
		WHERE user_id = $1 AND role_id = $2 AND property_id IS NOT DISTINCT FROM $3 AND valid_until IS NOT NULL AND valid_until <= now()
		RETURNING id`, userID, a.RoleID, a.PropertyID, a.ValidUntil).Scan(&aid)
	if dbtx.IsNoRows(err) {
		return uuid.Nil, false, nil
	}
	return aid, err == nil, err
}
