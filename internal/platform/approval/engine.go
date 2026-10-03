// Package approval is the approval engine (EP-06). Modules use it through
// its public interface only (FR-APR-09): they register a document type with
// condition attributes, call Submit inside their transaction and receive the
// outcome through a decision hook (also published as an outbox event).
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
)

// Request statuses follow Naming Convention §31 (FR-APR-04).
const (
	StatusDraft     = "draft"
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

// Decision is passed to a document type's hook when a request is finalised.
type Decision struct {
	RequestID    uuid.UUID
	DocumentType string
	DocumentID   uuid.UUID
	PropertyID   uuid.UUID
	Status       string // approved | rejected | cancelled
	Reason       string
	DecidedBy    uuid.UUID
}

// DecisionHook runs inside the deciding transaction.
type DecisionHook func(ctx context.Context, tx pgx.Tx, d Decision) error

// Publisher publishes domain events (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Engine is the approval engine.
type Engine struct {
	DB     *dbtx.DB
	Notify notify.Sender
	Events Publisher
	Cfg    *config.Config

	mu    sync.RWMutex
	types map[string]provision.DocumentType
	hooks map[string]DecisionHook
}

func New(db *dbtx.DB, n notify.Sender, ev Publisher, cfg *config.Config) *Engine {
	return &Engine{DB: db, Notify: n, Events: ev, Cfg: cfg, types: map[string]provision.DocumentType{}, hooks: map[string]DecisionHook{}}
}

// RegisterDocumentType registers a document type and its decision hook.
func (e *Engine) RegisterDocumentType(dt provision.DocumentType, hook DecisionHook) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.types[dt.Code] = dt
	if hook != nil {
		e.hooks[dt.Code] = hook
	}
}

// DocumentTypes returns registered types (for catalogue sync).
func (e *Engine) DocumentTypes() []provision.DocumentType {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]provision.DocumentType, 0, len(e.types))
	for _, t := range e.types {
		out = append(out, t)
	}
	return out
}

func (e *Engine) docType(code string) (provision.DocumentType, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.types[code]
	return t, ok
}

// SubmitRequest is the public submit contract.
type SubmitRequest struct {
	DocumentType string
	DocumentID   uuid.UUID
	DocumentRef  string
	Title        string
	PropertyID   uuid.UUID
	Attributes   map[string]any
	Draft        bool
}

// Condition is one workflow step condition (FR-APR-02).
type Condition struct {
	Attribute string `json:"attribute"`
	Operator  string `json:"operator" enum:"eq,neq,gt,gte,lt,lte,in"`
	Value     any    `json:"value"`
}

var operators = map[string]bool{"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true, "in": true}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// Match evaluates one condition against document attributes.
func (c Condition) Match(attrs map[string]any) bool {
	v, ok := attrs[c.Attribute]
	if !ok {
		return false
	}
	switch c.Operator {
	case "eq", "neq":
		eq := fmt.Sprint(v) == fmt.Sprint(c.Value)
		if a, ok1 := toFloat(v); ok1 {
			if b, ok2 := toFloat(c.Value); ok2 {
				eq = a == b
			}
		}
		return eq == (c.Operator == "eq")
	case "in":
		list, _ := c.Value.([]any)
		for _, x := range list {
			if fmt.Sprint(x) == fmt.Sprint(v) {
				return true
			}
		}
		return false
	default:
		a, ok1 := toFloat(v)
		b, ok2 := toFloat(c.Value)
		if !ok1 || !ok2 {
			return false
		}
		switch c.Operator {
		case "gt":
			return a > b
		case "gte":
			return a >= b
		case "lt":
			return a < b
		case "lte":
			return a <= b
		}
	}
	return false
}

type stepDef struct {
	StepNo     int
	Name       string
	Type       string
	RoleID     *uuid.UUID
	UserID     *uuid.UUID
	Conditions []Condition
	SLAHours   *int
}

// Submit creates an approval request inside the caller's transaction. The
// workflow is the active one for the document type with the most specific
// property match and lowest priority. Steps whose conditions do not match
// the document attributes are skipped. Without any applicable step the
// request is approved immediately and the decision hook runs.
func (e *Engine) Submit(ctx context.Context, tx pgx.Tx, s SubmitRequest) (uuid.UUID, string, error) {
	if _, ok := e.docType(s.DocumentType); !ok {
		return uuid.Nil, "", fmt.Errorf("approval: unknown document type %s", s.DocumentType)
	}
	p := authz.From(ctx)
	if p == nil || p.UserID == uuid.Nil {
		return uuid.Nil, "", errs.Unauthorized("a user must submit approval requests")
	}
	attrs := map[string]any{}
	for k, v := range s.Attributes {
		attrs[k] = v
	}
	attrs["propertyId"] = s.PropertyID.String()
	rawAttrs, _ := json.Marshal(attrs)
	rid := id.New()
	status := StatusPending
	if s.Draft {
		status = StatusDraft
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_requests (id, property_id, document_type, document_id, document_ref, title,
		attributes, status, requested_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		rid, s.PropertyID, s.DocumentType, s.DocumentID, s.DocumentRef, s.Title, rawAttrs, status, p.UserID); err != nil {
		return uuid.Nil, "", err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.approval_request",
		EntityID: rid.String(), EntityLabel: s.Title, PropertyID: &s.PropertyID,
		After: map[string]any{"documentType": s.DocumentType, "documentRef": s.DocumentRef, "status": status, "attributes": attrs}}); err != nil {
		return uuid.Nil, "", err
	}
	if s.Draft {
		return rid, StatusDraft, nil
	}
	st, err := e.start(ctx, tx, rid)
	return rid, st, err
}

// start selects the workflow and creates request steps.
func (e *Engine) start(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (string, error) {
	var docType string
	var propID uuid.UUID
	var attrs map[string]any
	if err := tx.QueryRow(ctx, `SELECT document_type, property_id, attributes FROM platform.approval_requests WHERE id = $1`, rid).
		Scan(&docType, &propID, &attrs); err != nil {
		return "", err
	}
	var wfID *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM platform.approval_workflows WHERE document_type = $1 AND status = 'active'
		AND (property_id IS NULL OR property_id = $2) ORDER BY (property_id IS NOT NULL) DESC, priority, created_at LIMIT 1`, docType, propID).Scan(&wfID)
	if err != nil && !dbtx.IsNoRows(err) {
		return "", err
	}
	var steps []stepDef
	if wfID != nil {
		rows, err := tx.Query(ctx, `SELECT step_no, name, approver_type, approver_role_id, approver_user_id, conditions, sla_hours
			FROM platform.approval_workflow_steps WHERE workflow_id = $1 ORDER BY step_no`, *wfID)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var sd stepDef
			var raw []byte
			if err := rows.Scan(&sd.StepNo, &sd.Name, &sd.Type, &sd.RoleID, &sd.UserID, &raw, &sd.SLAHours); err != nil {
				rows.Close()
				return "", err
			}
			_ = json.Unmarshal(raw, &sd.Conditions)
			steps = append(steps, sd)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
	}
	first := -1
	for i, sd := range steps {
		applicable := true
		for _, c := range sd.Conditions {
			if !c.Match(attrs) {
				applicable = false
				break
			}
		}
		st := "skipped"
		var due *time.Time
		if applicable {
			if first < 0 {
				first = i
				st = "pending"
				if sd.SLAHours != nil {
					t := clock.Now().Add(time.Duration(*sd.SLAHours) * time.Hour)
					due = &t
				}
			} else {
				st = "waiting"
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_request_steps (id, request_id, property_id, step_no, name, approver_type,
			approver_role_id, approver_user_id, status, due_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			id.New(), rid, propID, sd.StepNo, sd.Name, sd.Type, sd.RoleID, sd.UserID, st, due); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_requests SET workflow_id = $2, status = 'pending' WHERE id = $1`, rid, wfID); err != nil {
		return "", err
	}
	if first < 0 {
		reason := "no applicable approval step"
		if wfID == nil {
			reason = "no approval workflow configured"
		}
		if err := e.finalize(ctx, tx, rid, StatusApproved, reason, uuid.Nil); err != nil {
			return "", err
		}
		return StatusApproved, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_requests SET current_step_no = $2 WHERE id = $1`, rid, steps[first].StepNo); err != nil {
		return "", err
	}
	return StatusPending, e.notifyApprovers(ctx, tx, rid, steps[first].StepNo, "approval.pending")
}

// approversFor returns users who may act on a step: the configured user or
// role holders at the request's property, plus their active delegates.
func approversFor(ctx context.Context, q dbtx.Querier, rid uuid.UUID, stepNo int) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		WITH step AS (
		  SELECT s.approver_type, s.approver_role_id, s.approver_user_id, r.property_id, r.requested_by
		  FROM platform.approval_request_steps s JOIN platform.approval_requests r ON r.id = s.request_id
		  WHERE s.request_id = $1 AND s.step_no = $2),
		principals AS (
		  SELECT approver_user_id AS uid FROM step WHERE approver_type = 'user'
		  UNION
		  SELECT ra.user_id FROM step JOIN platform.role_assignments ra ON ra.role_id = step.approver_role_id
		    AND (ra.property_id IS NULL OR ra.property_id = step.property_id) WHERE step.approver_type = 'role')
		SELECT DISTINCT u.id FROM (
		  SELECT uid FROM principals
		  UNION
		  SELECT d.delegate_user_id FROM platform.approval_delegations d JOIN principals p ON p.uid = d.delegator_user_id
		  WHERE d.revoked_at IS NULL AND now() BETWEEN d.starts_at AND d.ends_at) x
		JOIN platform.users u ON u.id = x.uid AND u.status = 'active'
		WHERE u.id <> (SELECT requested_by FROM step)`, rid, stepNo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// eligible reports whether user may decide the current step; onBehalfOf is
// the delegator when acting as a delegate (FR-APR-07).
func eligible(ctx context.Context, q dbtx.Querier, rid uuid.UUID, stepNo int, user uuid.UUID) (bool, *uuid.UUID, error) {
	var direct bool
	var onBehalf *uuid.UUID
	err := q.QueryRow(ctx, `
		WITH step AS (
		  SELECT s.approver_type, s.approver_role_id, s.approver_user_id, r.property_id
		  FROM platform.approval_request_steps s JOIN platform.approval_requests r ON r.id = s.request_id
		  WHERE s.request_id = $1 AND s.step_no = $2),
		holders AS (
		  SELECT approver_user_id AS uid FROM step WHERE approver_type = 'user'
		  UNION
		  SELECT ra.user_id FROM step JOIN platform.role_assignments ra ON ra.role_id = step.approver_role_id
		    AND (ra.property_id IS NULL OR ra.property_id = step.property_id) WHERE step.approver_type = 'role')
		SELECT EXISTS (SELECT 1 FROM holders WHERE uid = $3),
		  (SELECT d.delegator_user_id FROM platform.approval_delegations d JOIN holders h ON h.uid = d.delegator_user_id
		   WHERE d.delegate_user_id = $3 AND d.revoked_at IS NULL AND now() BETWEEN d.starts_at AND d.ends_at LIMIT 1)`,
		rid, stepNo, user).Scan(&direct, &onBehalf)
	if err != nil {
		return false, nil, err
	}
	if direct {
		return true, nil, nil
	}
	return onBehalf != nil, onBehalf, nil
}

func (e *Engine) link(rid uuid.UUID) string {
	return e.Cfg.PublicBaseURL + "/approvals/" + rid.String()
}

type reqInfo struct {
	Title, DocumentType, DocumentRef string
	RequestedBy                      uuid.UUID
	RequesterName                    string
	PropertyID                       uuid.UUID
	DocumentID                       uuid.UUID
	Status                           string
	CurrentStep                      *int
}

func loadInfo(ctx context.Context, q dbtx.Querier, rid uuid.UUID, lock bool) (reqInfo, error) {
	var ri reqInfo
	sql := `SELECT r.title, r.document_type, r.document_ref, r.requested_by, u.full_name, r.property_id, r.document_id, r.status, r.current_step_no
		FROM platform.approval_requests r JOIN platform.users u ON u.id = r.requested_by WHERE r.id = $1`
	if lock {
		sql += " FOR UPDATE OF r"
	}
	err := q.QueryRow(ctx, sql, rid).Scan(&ri.Title, &ri.DocumentType, &ri.DocumentRef, &ri.RequestedBy, &ri.RequesterName,
		&ri.PropertyID, &ri.DocumentID, &ri.Status, &ri.CurrentStep)
	if dbtx.IsNoRows(err) {
		return ri, errs.NotFound("approval request")
	}
	return ri, err
}

func (e *Engine) docName(code string) string {
	if t, ok := e.docType(code); ok {
		return t.Name
	}
	return code
}

func (e *Engine) notifyApprovers(ctx context.Context, tx pgx.Tx, rid uuid.UUID, stepNo int, event string) error {
	ri, err := loadInfo(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	var stepName string
	_ = tx.QueryRow(ctx, `SELECT name FROM platform.approval_request_steps WHERE request_id = $1 AND step_no = $2`, rid, stepNo).Scan(&stepName)
	users, err := approversFor(ctx, tx, rid, stepNo)
	if err != nil || len(users) == 0 {
		return err
	}
	return e.Notify.Send(ctx, tx, notify.Message{Event: event, Category: "approval", UserIDs: users, Link: e.link(rid), PropertyID: &ri.PropertyID,
		Data: map[string]any{"title": ri.Title, "documentType": e.docName(ri.DocumentType), "documentRef": ri.DocumentRef,
			"requesterName": ri.RequesterName, "stepName": stepName, "since": clock.Now().Format("2006-01-02 15:04")}})
}

// finalize sets the final status, runs the document hook, notifies the
// requester (FR-APR-05) and publishes approval.decided.
func (e *Engine) finalize(ctx context.Context, tx pgx.Tx, rid uuid.UUID, status, reason string, by uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_requests SET status = $2, decided_at = now(), decision_reason = $3, current_step_no = NULL WHERE id = $1`,
		rid, status, nullable(reason)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_request_steps SET status = 'cancelled' WHERE request_id = $1 AND status IN ('waiting', 'pending')`, rid); err != nil {
		return err
	}
	ri, err := loadInfo(ctx, tx, rid, false)
	if err != nil {
		return err
	}
	d := Decision{RequestID: rid, DocumentType: ri.DocumentType, DocumentID: ri.DocumentID, PropertyID: ri.PropertyID, Status: status, Reason: reason, DecidedBy: by}
	e.mu.RLock()
	hook := e.hooks[ri.DocumentType]
	e.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, tx, d); err != nil {
			return err
		}
	}
	if e.Events != nil {
		if _, err := e.Events.Publish(ctx, tx, "approval.decided", "platform.approval_request", &rid, &ri.PropertyID, map[string]any{
			"requestId": rid, "documentType": ri.DocumentType, "documentId": ri.DocumentID, "status": status, "reason": reason,
		}); err != nil {
			return err
		}
	}
	if status == StatusCancelled {
		return nil
	}
	deciderName := "system"
	if by != uuid.Nil {
		_ = tx.QueryRow(ctx, `SELECT full_name FROM platform.users WHERE id = $1`, by).Scan(&deciderName)
	}
	return e.Notify.Send(ctx, tx, notify.Message{Event: "approval.decided", Category: "approval", UserIDs: []uuid.UUID{ri.RequestedBy},
		Link: e.link(rid), PropertyID: &ri.PropertyID,
		Data: map[string]any{"title": ri.Title, "documentType": e.docName(ri.DocumentType), "documentRef": ri.DocumentRef,
			"decision": statusLabel(status), "deciderName": deciderName, "reason": reason}})
}

func statusLabel(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Decide applies Approve or Reject by the current principal.
func (e *Engine) Decide(ctx context.Context, tx pgx.Tx, rid uuid.UUID, approve bool, reason string) error {
	p := authz.From(ctx)
	ri, err := loadInfo(ctx, tx, rid, true)
	if err != nil {
		return err
	}
	if ri.Status != StatusPending || ri.CurrentStep == nil {
		return errs.Conflict("approval_not_pending", "this request is no longer pending")
	}
	if ri.RequestedBy == p.UserID {
		return errs.Forbidden("you cannot approve or reject your own request")
	}
	ok, onBehalf, err := eligible(ctx, tx, rid, *ri.CurrentStep, p.UserID)
	if err != nil {
		return err
	}
	if !ok {
		return errs.Forbidden("you are not an approver for the current step")
	}
	if !approve && strings.TrimSpace(reason) == "" {
		return errs.Validation("reason_required", "a reason is required to reject", errs.Field("reason", "required", "reason is required"))
	}
	stepStatus := "approved"
	if !approve {
		stepStatus = "rejected"
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_request_steps SET status = $3, decided_by = $4, decided_on_behalf_of = $5,
		decided_at = now(), reason = $6 WHERE request_id = $1 AND step_no = $2`,
		rid, *ri.CurrentStep, stepStatus, p.UserID, onBehalf, nullable(reason)); err != nil {
		return err
	}
	md := map[string]any{"stepNo": *ri.CurrentStep}
	if onBehalf != nil {
		md["onBehalfOf"] = onBehalf.String()
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approval_" + stepStatus, EntityType: "platform.approval_request",
		EntityID: rid.String(), EntityLabel: ri.Title, PropertyID: &ri.PropertyID, Reason: reason, Metadata: md,
		Before: map[string]any{"status": ri.Status, "currentStep": *ri.CurrentStep}}); err != nil {
		return err
	}
	if !approve {
		return e.finalize(ctx, tx, rid, StatusRejected, reason, p.UserID)
	}
	var next *int
	err = tx.QueryRow(ctx, `SELECT min(step_no) FROM platform.approval_request_steps WHERE request_id = $1 AND status = 'waiting'`, rid).Scan(&next)
	if err != nil {
		return err
	}
	if next == nil {
		return e.finalize(ctx, tx, rid, StatusApproved, reason, p.UserID)
	}
	var sla *int
	_ = tx.QueryRow(ctx, `SELECT ws.sla_hours FROM platform.approval_requests r JOIN platform.approval_workflow_steps ws
		ON ws.workflow_id = r.workflow_id AND ws.step_no = $2 WHERE r.id = $1`, rid, *next).Scan(&sla)
	var due *time.Time
	if sla != nil {
		t := clock.Now().Add(time.Duration(*sla) * time.Hour)
		due = &t
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_request_steps SET status = 'pending', due_at = $3 WHERE request_id = $1 AND step_no = $2`, rid, *next, due); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.approval_requests SET current_step_no = $2 WHERE id = $1`, rid, *next); err != nil {
		return err
	}
	return e.notifyApprovers(ctx, tx, rid, *next, "approval.pending")
}

// Cancel lets the requester withdraw a draft or pending request (FR-APR-03).
func (e *Engine) Cancel(ctx context.Context, tx pgx.Tx, rid uuid.UUID, reason string) error {
	p := authz.From(ctx)
	ri, err := loadInfo(ctx, tx, rid, true)
	if err != nil {
		return err
	}
	if ri.RequestedBy != p.UserID {
		return errs.Forbidden("only the requester can cancel this request")
	}
	if ri.Status != StatusPending && ri.Status != StatusDraft {
		return errs.Conflict("approval_not_pending", "only draft or pending requests can be cancelled")
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approval_cancelled", EntityType: "platform.approval_request",
		EntityID: rid.String(), EntityLabel: ri.Title, PropertyID: &ri.PropertyID, Reason: reason,
		Before: map[string]any{"status": ri.Status}, After: map[string]any{"status": StatusCancelled}}); err != nil {
		return err
	}
	return e.finalize(ctx, tx, rid, StatusCancelled, reason, p.UserID)
}

// SubmitDraft moves a draft to pending.
func (e *Engine) SubmitDraft(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (string, error) {
	p := authz.From(ctx)
	ri, err := loadInfo(ctx, tx, rid, true)
	if err != nil {
		return "", err
	}
	if ri.RequestedBy != p.UserID {
		return "", errs.Forbidden("only the requester can submit this request")
	}
	if ri.Status != StatusDraft {
		return "", errs.Conflict("approval_not_draft", "only draft requests can be submitted")
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "approval_submitted", EntityType: "platform.approval_request",
		EntityID: rid.String(), EntityLabel: ri.Title, PropertyID: &ri.PropertyID,
		Before: map[string]any{"status": StatusDraft}, After: map[string]any{"status": StatusPending}}); err != nil {
		return "", err
	}
	return e.start(ctx, tx, rid)
}

// SendReminders notifies approvers of overdue steps (FR-APR-08); returns
// the number of steps reminded. Steps are reminded at most once per day.
func (e *Engine) SendReminders(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	n := 0
	err := e.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.request_id, s.step_no FROM platform.approval_request_steps s
			JOIN platform.approval_requests r ON r.id = s.request_id AND r.status = 'pending'
			WHERE s.status = 'pending' AND s.due_at < now() AND (s.reminded_at IS NULL OR s.reminded_at < now() - interval '24 hours')
			FOR UPDATE OF s SKIP LOCKED`)
		if err != nil {
			return err
		}
		type key struct {
			rid uuid.UUID
			no  int
		}
		var due []key
		for rows.Next() {
			var k key
			if err := rows.Scan(&k.rid, &k.no); err != nil {
				rows.Close()
				return err
			}
			due = append(due, k)
		}
		rows.Close()
		for _, k := range due {
			if err := e.notifyApprovers(ctx, tx, k.rid, k.no, "approval.reminder"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.approval_request_steps SET reminded_at = now() WHERE request_id = $1 AND step_no = $2`, k.rid, k.no); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
