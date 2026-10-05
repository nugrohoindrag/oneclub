package approval

// Reassignment of a leaver's approvals (PRD P5 EP-01 acceptance criterion,
// contract H7): when an employee leaves, the pending and waiting steps of
// approval requests that name the user as approver move to the supervisor.
// Role-based steps need nothing: inactive users are no approvers.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/audit"
)

// ReassignUser moves the open user steps of from to to and notifies the
// new approver of the steps waiting for a decision. It returns the number
// of steps moved; nothing moves without a target user.
func (e *Engine) ReassignUser(ctx context.Context, tx pgx.Tx, from uuid.UUID, to *uuid.UUID, reason string) (int, error) {
	if to == nil || *to == from {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `UPDATE platform.approval_request_steps s SET approver_user_id = $2
		FROM platform.approval_requests r WHERE r.id = s.request_id AND r.status = 'pending' AND s.approver_type = 'user' AND s.approver_user_id = $1
		  AND s.status IN ('pending', 'waiting')
		RETURNING s.request_id, s.step_no, s.status, s.property_id, r.title`, from, *to)
	if err != nil {
		return 0, err
	}
	type moved struct {
		rid      uuid.UUID
		step     int
		status   string
		property uuid.UUID
		title    string
	}
	var ms []moved
	for rows.Next() {
		var m moved
		if err := rows.Scan(&m.rid, &m.step, &m.status, &m.property, &m.title); err != nil {
			rows.Close()
			return 0, err
		}
		ms = append(ms, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, m := range ms {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approver_reassigned", EntityType: "platform.approval_request",
			EntityID: m.rid.String(), EntityLabel: m.title, PropertyID: &m.property, Reason: reason,
			Before: map[string]any{"approverUserId": from, "stepNo": m.step}, After: map[string]any{"approverUserId": *to, "stepNo": m.step},
			ActorName: "HRIS offboarding"}); err != nil {
			return 0, err
		}
		if m.status == "pending" {
			if err := e.notifyApprovers(ctx, tx, m.rid, m.step, "approval.pending"); err != nil {
				return 0, err
			}
		}
	}
	return len(ms), nil
}
