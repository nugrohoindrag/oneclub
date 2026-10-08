package approval

// Request Revision (HRIS improvement phase B, docs/HRIS_Product_Requirements
// _UI_Backend_Audit.md §27): an approver of the current step returns a
// pending request to the requester instead of approving or rejecting it.
// Naming Convention §31 has no separate status, so the request goes back to
// Draft with the reason; its steps are dropped (the audit trail keeps them)
// and rebuilt from the workflow when the requester resubmits (SubmitDraft)
// or the requester cancels it. A module that lets the requester edit the
// document meanwhile registers a revision hook.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
)

// StatusRevision is the Decision.Status passed to revision hooks (never
// stored on the request, which goes back to draft).
const StatusRevision = "revision"

// RegisterRevisionHook runs hook when a request of the document type is
// returned for revision (inside the same transaction).
func (e *Engine) RegisterRevisionHook(code string, hook DecisionHook) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revisions == nil {
		e.revisions = map[string]DecisionHook{}
	}
	e.revisions[code] = hook
}

// RequestRevision returns a pending request to its requester (reason
// required) by an approver of the current step.
func (e *Engine) RequestRevision(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) error {
	p := authz.From(ctx)
	ri, err := loadInfo(ctx, tx, rid, true)
	if err != nil {
		return err
	}
	if ri.Status != StatusPending || ri.CurrentStep == nil {
		return errs.Conflict("approval_not_pending", "this request is no longer pending")
	}
	if ri.RequestedBy == p.UserID {
		return errs.Forbidden("you cannot request a revision of your own request")
	}
	ok, onBehalf, err := eligible(ctx, tx, rid, *ri.CurrentStep, p.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return errs.Forbidden("you are not an approver for the current step")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errs.Validation("reason_required", "explain what must be revised", errs.Field("reason", "required", "reason is required"))
	}
	steps, err := stepSnapshot(ctx, tx, rid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.approval_request_steps WHERE request_id = $1`, rid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_requests SET status = 'draft', workflow_id = NULL, current_step_no = NULL, decided_at = NULL,
		decision_reason = $2 WHERE id = $1`, rid, reason); err != nil {
		return err
	}
	md := map[string]any{"stepNo": *ri.CurrentStep, "steps": steps}
	if onBehalf != nil {
		md["onBehalfOf"] = onBehalf.String()
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approval_revision_requested", EntityType: "platform.approval_request",
		EntityID: rid.String(), EntityLabel: ri.Title, PropertyID: &ri.PropertyID, Reason: reason, Metadata: md,
		Before: map[string]any{"status": StatusPending, "currentStep": *ri.CurrentStep}, After: map[string]any{"status": StatusDraft}}); err != nil {
		return err
	}
	e.mu.RLock()
	hook := e.revisions[ri.DocumentType]
	e.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, tx, Decision{RequestID: rid, DocumentType: ri.DocumentType, DocumentID: ri.DocumentID, PropertyID: ri.PropertyID,
			Status: StatusRevision, Reason: reason, DecidedBy: p.UserID}); err != nil {
			return err
		}
	}
	if e.Events != nil {
		if _, err := e.Events.Publish(ctx, tx, "approval.revision_requested", "platform.approval_request", &rid, &ri.PropertyID, map[string]any{
			"requestId": rid, "documentType": ri.DocumentType, "documentId": ri.DocumentID, "reason": reason,
		}); err != nil {
			return err
		}
	}
	var by string
	_ = tx.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, p.UserID).Scan(&by)
	return e.Notify.Send(ctx, tx, notify.Message{Event: "approval.revision_requested", Category: "approval", UserIDs: []uuid.UUID{ri.RequestedBy},
		Link: e.link(rid), PropertyID: &ri.PropertyID,
		Data: map[string]any{"title": ri.Title, "documentType": e.docName(ri.DocumentType), "documentRef": ri.DocumentRef, "deciderName": by,
			"reason": reason}})
}

// stepSnapshot lists the steps of a request for the audit trail.
func stepSnapshot(ctx context.Context, tx pgx.Tx, rid uuid.UUID) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT step_no, name, status, decided_by, decided_at, reason FROM platform.approval_request_steps
		WHERE request_id = $1 ORDER BY step_no`, rid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var no int
		var name, status string
		var by *uuid.UUID
		var at *time.Time
		var reason *string
		if err := rows.Scan(&no, &name, &status, &by, &at, &reason); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"stepNo": no, "name": name, "status": status, "decidedBy": by, "decidedAt": at, "reason": reason})
	}
	return out, rows.Err()
}

// RequestEvent is one entry of the approval history (audit trail of the
// request: submitted, step decisions, revisions, resubmissions, cancel).
type RequestEvent struct {
	At        time.Time `json:"at"`
	Action    string    `json:"action" doc:"create | approval_approved | approval_rejected | approval_revision_requested | approval_submitted | approval_cancelled | approver_reassigned"`
	ActorName *string   `json:"actorName"`
	StepNo    *int      `json:"stepNo"`
	Reason    *string   `json:"reason"`
}

// history reads the audit trail of a request, oldest first.
func history(ctx context.Context, tx pgx.Tx, rid uuid.UUID) ([]RequestEvent, error) {
	rows, err := tx.Query(ctx, `SELECT occurred_at, action, actor_name, (metadata->>'stepNo')::int, reason FROM audit.audit_log
		WHERE entity_type = 'platform.approval_request' AND entity_id = $1 ORDER BY occurred_at, id`, rid.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RequestEvent{}
	for rows.Next() {
		var x RequestEvent
		if err := rows.Scan(&x.At, &x.Action, &x.ActorName, &x.StepNo, &x.Reason); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
