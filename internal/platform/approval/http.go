package approval

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/provision"
)

// ── DTOs ──────────────────────────────────────────────────────────────────

type WorkflowStep struct {
	StepNo         int         `json:"stepNo"`
	Name           string      `json:"name"`
	ApproverType   string      `json:"approverType" enum:"role,user"`
	ApproverRoleID *uuid.UUID  `json:"approverRoleId"`
	ApproverUserID *uuid.UUID  `json:"approverUserId"`
	Conditions     []Condition `json:"conditions"`
	SLAHours       *int        `json:"slaHours"`
}

type Workflow struct {
	ID           uuid.UUID      `json:"id"`
	DocumentType string         `json:"documentType"`
	Name         string         `json:"name"`
	PropertyID   *uuid.UUID     `json:"propertyId"`
	Priority     int            `json:"priority"`
	Status       string         `json:"status" enum:"active,inactive"`
	Steps        []WorkflowStep `json:"steps"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

type WorkflowRequest struct {
	DocumentType string         `json:"documentType"`
	Name         string         `json:"name"`
	PropertyID   *uuid.UUID     `json:"propertyId,omitempty"`
	Priority     *int           `json:"priority,omitempty"`
	Status       *string        `json:"status,omitempty" enum:"active,inactive"`
	Steps        []WorkflowStep `json:"steps"`
}

type WorkflowUpdate struct {
	Name       *string         `json:"name,omitempty"`
	PropertyID *uuid.UUID      `json:"propertyId,omitempty"`
	Priority   *int            `json:"priority,omitempty"`
	Status     *string         `json:"status,omitempty" enum:"active,inactive"`
	Steps      *[]WorkflowStep `json:"steps,omitempty"`
}

type DocumentTypeView struct {
	Code       string                        `json:"code"`
	Module     string                        `json:"module"`
	Name       string                        `json:"name"`
	Attributes []provision.DocumentAttribute `json:"attributes"`
}

type RequestStep struct {
	StepNo           int        `json:"stepNo"`
	Name             string     `json:"name"`
	ApproverType     string     `json:"approverType"`
	ApproverRoleName *string    `json:"approverRoleName"`
	ApproverUserName *string    `json:"approverUserName"`
	Status           string     `json:"status" enum:"waiting,pending,approved,rejected,skipped,cancelled"`
	DecidedByName    *string    `json:"decidedByName"`
	OnBehalfOfName   *string    `json:"onBehalfOfName"`
	DecidedAt        *time.Time `json:"decidedAt"`
	Reason           *string    `json:"reason"`
	DueAt            *time.Time `json:"dueAt"`
}

type Request struct {
	ID             uuid.UUID      `json:"id"`
	PropertyID     uuid.UUID      `json:"propertyId"`
	DocumentType   string         `json:"documentType"`
	DocumentName   string         `json:"documentName"`
	DocumentID     uuid.UUID      `json:"documentId"`
	DocumentRef    string         `json:"documentRef"`
	Title          string         `json:"title"`
	Attributes     map[string]any `json:"attributes"`
	Status         string         `json:"status" enum:"draft,pending,approved,rejected,cancelled"`
	CurrentStepNo  *int           `json:"currentStepNo"`
	RequestedBy    uuid.UUID      `json:"requestedBy"`
	RequesterName  string         `json:"requesterName"`
	DecidedAt      *time.Time     `json:"decidedAt"`
	DecisionReason *string        `json:"decisionReason"`
	CreatedAt      time.Time      `json:"createdAt"`
	CanDecide      bool           `json:"canDecide"`
	CanCancel      bool           `json:"canCancel"`
	Steps          []RequestStep  `json:"steps,omitempty"`
}

type DecisionRequest struct {
	Reason string `json:"reason,omitempty"`
}

type TestApprovalRequest struct {
	Title  string `json:"title"`
	Amount string `json:"amount" doc:"Decimal amount used by workflow conditions"`
	Draft  bool   `json:"draft,omitempty"`
}

type Delegation struct {
	ID              uuid.UUID  `json:"id"`
	DelegatorUserID uuid.UUID  `json:"delegatorUserId"`
	DelegatorName   string     `json:"delegatorName"`
	DelegateUserID  uuid.UUID  `json:"delegateUserId"`
	DelegateName    string     `json:"delegateName"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          time.Time  `json:"endsAt"`
	Reason          *string    `json:"reason"`
	RevokedAt       *time.Time `json:"revokedAt"`
	Active          bool       `json:"active"`
}

type DelegationRequest struct {
	DelegatorUserID *uuid.UUID `json:"delegatorUserId,omitempty" doc:"Defaults to the current user; others require platform.approval.delegate"`
	DelegateUserID  uuid.UUID  `json:"delegateUserId"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          time.Time  `json:"endsAt"`
	Reason          string     `json:"reason,omitempty"`
}

// HTTP exposes the approval endpoints.
type HTTP struct{ E *Engine }

// ── workflows (FR-APR-01, FR-APR-02) ──────────────────────────────────────

func (h *HTTP) loadWorkflow(r *http.Request, tx pgx.Tx, wid uuid.UUID) (Workflow, error) {
	ctx := r.Context()
	var w Workflow
	err := tx.QueryRow(ctx, `SELECT id, document_type, name, property_id, priority, status, updated_at FROM platform.approval_workflows WHERE id = $1`, wid).
		Scan(&w.ID, &w.DocumentType, &w.Name, &w.PropertyID, &w.Priority, &w.Status, &w.UpdatedAt)
	if dbtx.IsNoRows(err) {
		return w, errs.NotFound("approval workflow")
	}
	if err != nil {
		return w, err
	}
	rows, err := tx.Query(ctx, `SELECT step_no, name, approver_type, approver_role_id, approver_user_id, conditions, sla_hours
		FROM platform.approval_workflow_steps WHERE workflow_id = $1 ORDER BY step_no`, wid)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	w.Steps = []WorkflowStep{}
	for rows.Next() {
		var s WorkflowStep
		var raw []byte
		if err := rows.Scan(&s.StepNo, &s.Name, &s.ApproverType, &s.ApproverRoleID, &s.ApproverUserID, &raw, &s.SLAHours); err != nil {
			return w, err
		}
		s.Conditions = []Condition{}
		_ = json.Unmarshal(raw, &s.Conditions)
		w.Steps = append(w.Steps, s)
	}
	return w, rows.Err()
}

func (h *HTTP) validateSteps(docType string, steps []WorkflowStep) error {
	dt, ok := h.E.docType(docType)
	if !ok {
		return errs.Validation("invalid_document_type", "unknown document type", errs.Field("documentType", "invalid", "unknown document type"))
	}
	attrs := map[string]bool{"propertyId": true}
	for _, a := range dt.Attributes {
		attrs[a.Key] = true
	}
	var fields []errs.FieldError
	if len(steps) == 0 {
		fields = append(fields, errs.Field("steps", "required", "add at least one step"))
	}
	seen := map[int]bool{}
	for i, s := range steps {
		p := "steps[" + itoa(i) + "]"
		if s.StepNo <= 0 || seen[s.StepNo] {
			fields = append(fields, errs.Field(p+".stepNo", "invalid", "step numbers must be unique and positive"))
		}
		seen[s.StepNo] = true
		if strings.TrimSpace(s.Name) == "" {
			fields = append(fields, errs.Field(p+".name", "required", "step name is required"))
		}
		switch s.ApproverType {
		case "role":
			if s.ApproverRoleID == nil {
				fields = append(fields, errs.Field(p+".approverRoleId", "required", "select a role"))
			}
		case "user":
			if s.ApproverUserID == nil {
				fields = append(fields, errs.Field(p+".approverUserId", "required", "select a user"))
			}
		default:
			fields = append(fields, errs.Field(p+".approverType", "invalid", "role or user"))
		}
		if s.SLAHours != nil && *s.SLAHours <= 0 {
			fields = append(fields, errs.Field(p+".slaHours", "invalid", "must be positive"))
		}
		for j, c := range s.Conditions {
			cp := p + ".conditions[" + itoa(j) + "]"
			if !attrs[c.Attribute] {
				fields = append(fields, errs.Field(cp+".attribute", "invalid", "unknown attribute "+c.Attribute))
			}
			if !operators[c.Operator] {
				fields = append(fields, errs.Field(cp+".operator", "invalid", "eq, neq, gt, gte, lt, lte or in"))
			}
		}
	}
	if len(fields) > 0 {
		return errs.Validation("invalid_workflow", "invalid approval workflow", fields...)
	}
	return nil
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func writeSteps(r *http.Request, tx pgx.Tx, wid uuid.UUID, steps []WorkflowStep) error {
	ctx := r.Context()
	if _, err := tx.Exec(ctx, `DELETE FROM platform.approval_workflow_steps WHERE workflow_id = $1`, wid); err != nil {
		return err
	}
	for _, s := range steps {
		conds := s.Conditions
		if conds == nil {
			conds = []Condition{}
		}
		raw, _ := json.Marshal(conds)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflow_steps (id, workflow_id, step_no, name, approver_type, approver_role_id,
			approver_user_id, conditions, sla_hours) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id.New(), wid, s.StepNo, s.Name, s.ApproverType, s.ApproverRoleID, s.ApproverUserID, raw, s.SLAHours); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_approver", "approver role or user not found")
			}
			return err
		}
	}
	return nil
}

func (h *HTTP) listWorkflows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Workflow{}
	err := h.E.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		args := []any{}
		where := "true"
		if v := httpx.ParseList(r).Filters["documentType"]; v != "" {
			args = append(args, v)
			where = "document_type = $1"
		}
		rows, err := tx.Query(ctx, `SELECT id FROM platform.approval_workflows WHERE `+where+` ORDER BY document_type, priority, name`, args...)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var x uuid.UUID
			_ = rows.Scan(&x)
			ids = append(ids, x)
		}
		rows.Close()
		for _, x := range ids {
			wf, err := h.loadWorkflow(r, tx, x)
			if err != nil {
				return err
			}
			out = append(out, wf)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Workflow]{Items: out})
}

func (h *HTTP) getWorkflow(w http.ResponseWriter, r *http.Request) {
	wid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out Workflow
	err = h.E.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
		out, err = h.loadWorkflow(r, tx, wid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) createWorkflow(w http.ResponseWriter, r *http.Request) {
	var req WorkflowRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_workflow", "name is required", errs.Field("name", "required", "name is required")))
		return
	}
	if err := h.validateSteps(req.DocumentType, req.Steps); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	prio := 100
	if req.Priority != nil {
		prio = *req.Priority
	}
	status := "active"
	if req.Status != nil {
		status = *req.Status
	}
	ctx := r.Context()
	var out Workflow
	err := h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		wid := id.New()
		uid := id.Ptr(authz.From(ctx).UserID)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_workflows (id, document_type, name, property_id, priority, status, created_by, updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, wid, req.DocumentType, req.Name, req.PropertyID, prio, status, uid); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_property", "property not found", errs.Field("propertyId", "invalid", "property not found"))
			}
			return err
		}
		if err := writeSteps(r, tx, wid, req.Steps); err != nil {
			return err
		}
		var err error
		if out, err = h.loadWorkflow(r, tx, wid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.approval_workflow",
			EntityID: wid.String(), EntityLabel: req.Name, PropertyID: req.PropertyID, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	wid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req WorkflowUpdate
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "inactive" {
		httpx.WriteError(w, r, errs.Validation("invalid_status", "invalid status", errs.Field("status", "invalid", "active or inactive")))
		return
	}
	ctx := r.Context()
	var out Workflow
	err = h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := h.loadWorkflow(r, tx, wid)
		if err != nil {
			return err
		}
		if req.Steps != nil {
			if err := h.validateSteps(before.DocumentType, *req.Steps); err != nil {
				return err
			}
			if err := writeSteps(r, tx, wid, *req.Steps); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.approval_workflows SET name = coalesce($2, name), property_id = coalesce($3, property_id),
			priority = coalesce($4, priority), status = coalesce($5, status), updated_by = $6 WHERE id = $1`,
			wid, req.Name, req.PropertyID, req.Priority, req.Status, id.Ptr(authz.From(ctx).UserID)); err != nil {
			return err
		}
		if out, err = h.loadWorkflow(r, tx, wid); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionUpdate, EntityType: "platform.approval_workflow",
			EntityID: wid.String(), EntityLabel: out.Name, Before: before, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) documentTypes(w http.ResponseWriter, r *http.Request) {
	out := []DocumentTypeView{}
	for _, t := range h.E.DocumentTypes() {
		attrs := t.Attributes
		if attrs == nil {
			attrs = []provision.DocumentAttribute{}
		}
		out = append(out, DocumentTypeView{Code: t.Code, Module: t.Module, Name: t.Name, Attributes: attrs})
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[DocumentTypeView]{Items: out})
}

// ── requests ──────────────────────────────────────────────────────────────

const requestSelect = `SELECT r.id, r.property_id, r.document_type, r.document_id, r.document_ref, r.title, r.attributes, r.status,
	r.current_step_no, r.requested_by, u.full_name, r.decided_at, r.decision_reason, r.created_at
	FROM platform.approval_requests r JOIN platform.users u ON u.id = r.requested_by`

func (h *HTTP) scanRequest(row pgx.Row) (Request, error) {
	var x Request
	err := row.Scan(&x.ID, &x.PropertyID, &x.DocumentType, &x.DocumentID, &x.DocumentRef, &x.Title, &x.Attributes, &x.Status,
		&x.CurrentStepNo, &x.RequestedBy, &x.RequesterName, &x.DecidedAt, &x.DecisionReason, &x.CreatedAt)
	x.DocumentName = h.E.docName(x.DocumentType)
	return x, err
}

// list serves the Approvals inbox (FR-APR-06): box=inbox (waiting for me),
// box=mine (my requests) or box=all (platform.approval.view_all).
func (h *HTTP) list(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	lp := httpx.ParseList(r)
	box := r.URL.Query().Get("box")
	if box == "" {
		box = "inbox"
	}
	where := []string{}
	args := []any{p.UserID}
	switch box {
	case "inbox":
		where = append(where, `r.status = 'pending' AND r.requested_by <> $1 AND EXISTS (
			SELECT 1 FROM platform.approval_request_steps s WHERE s.request_id = r.id AND s.step_no = r.current_step_no AND (
			  (s.approver_type = 'user' AND (s.approver_user_id = $1 OR s.approver_user_id IN (
			     SELECT delegator_user_id FROM platform.approval_delegations WHERE delegate_user_id = $1 AND revoked_at IS NULL AND now() BETWEEN starts_at AND ends_at)))
			  OR (s.approver_type = 'role' AND EXISTS (
			     SELECT 1 FROM platform.role_assignments ra WHERE ra.role_id = s.approver_role_id
			     AND (ra.property_id IS NULL OR ra.property_id = r.property_id)
			     AND (ra.user_id = $1 OR ra.user_id IN (
			       SELECT delegator_user_id FROM platform.approval_delegations WHERE delegate_user_id = $1 AND revoked_at IS NULL AND now() BETWEEN starts_at AND ends_at)))))
			)`)
	case "mine":
		where = append(where, "r.requested_by = $1")
	case "all":
		if !p.Can("platform.approval.view_all", nil) {
			httpx.WriteError(w, r, errs.Forbidden("missing permission platform.approval.view_all"))
			return
		}
		where = append(where, "$1::uuid IS NOT NULL")
	default:
		httpx.WriteError(w, r, errs.BadRequest("invalid_box", "box must be inbox, mine or all"))
		return
	}
	if st := lp.Filters["status"]; st != "" {
		args = append(args, strings.Split(st, ","))
		where = append(where, "r.status = ANY($"+itoa(len(args))+")")
	}
	if dt := lp.Filters["documentType"]; dt != "" {
		args = append(args, dt)
		where = append(where, "r.document_type = $"+itoa(len(args)))
	}
	if lp.Q != "" {
		args = append(args, "%"+lp.Q+"%")
		where = append(where, "(r.title ILIKE $"+itoa(len(args))+" OR r.document_ref ILIKE $"+itoa(len(args))+")")
	}
	cursor, err := httpx.DecodeCursor(lp.Cursor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if cursor != "" {
		args = append(args, cursor)
		where = append(where, "r.id < $"+itoa(len(args))+"::uuid")
	}
	args = append(args, lp.Limit+1)
	// Inbox/mine eligibility is explicit in SQL; "all" stays within RLS scope.
	qctx := ctx
	if box != "all" {
		qctx = dbtx.WithScope(ctx, dbtx.Scope{AllProperties: true, UserID: p.UserID})
	}
	var out []Request
	err = h.E.DB.WithReadTx(qctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, requestSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY r.id DESC LIMIT $"+itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := h.scanRequest(rows)
			if err != nil {
				return err
			}
			x.CanCancel = x.RequestedBy == p.UserID && (x.Status == StatusPending || x.Status == StatusDraft)
			x.CanDecide = box == "inbox"
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.BuildPage(out, lp.Limit, func(x Request) string { return x.ID.String() }))
}

func (h *HTTP) loadDetail(r *http.Request, tx pgx.Tx, rid uuid.UUID) (Request, error) {
	ctx := r.Context()
	p := authz.From(ctx)
	x, err := h.scanRequest(tx.QueryRow(ctx, requestSelect+" WHERE r.id = $1", rid))
	if dbtx.IsNoRows(err) {
		return x, errs.NotFound("approval request")
	}
	if err != nil {
		return x, err
	}
	rows, err := tx.Query(ctx, `SELECT s.step_no, s.name, s.approver_type, ro.name, au.full_name, s.status, du.full_name, bu.full_name,
		s.decided_at, s.reason, s.due_at
		FROM platform.approval_request_steps s
		LEFT JOIN platform.roles ro ON ro.id = s.approver_role_id
		LEFT JOIN platform.users au ON au.id = s.approver_user_id
		LEFT JOIN platform.users du ON du.id = s.decided_by
		LEFT JOIN platform.users bu ON bu.id = s.decided_on_behalf_of
		WHERE s.request_id = $1 ORDER BY s.step_no`, rid)
	if err != nil {
		return x, err
	}
	x.Steps = []RequestStep{}
	for rows.Next() {
		var s RequestStep
		if err := rows.Scan(&s.StepNo, &s.Name, &s.ApproverType, &s.ApproverRoleName, &s.ApproverUserName, &s.Status, &s.DecidedByName,
			&s.OnBehalfOfName, &s.DecidedAt, &s.Reason, &s.DueAt); err != nil {
			rows.Close()
			return x, err
		}
		x.Steps = append(x.Steps, s)
	}
	rows.Close()
	if x.Status == StatusPending && x.CurrentStepNo != nil && x.RequestedBy != p.UserID {
		ok, _, err := eligible(ctx, tx, rid, *x.CurrentStepNo, p.UserID)
		if err != nil {
			return x, err
		}
		x.CanDecide = ok
	}
	x.CanCancel = x.RequestedBy == p.UserID && (x.Status == StatusPending || x.Status == StatusDraft)
	// Visible to the requester, any approver of any step, or view_all.
	if x.RequestedBy != p.UserID && !x.CanDecide && !p.Can("platform.approval.view_all", &x.PropertyID) {
		var involved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.approval_request_steps WHERE request_id = $1 AND
			(decided_by = $2 OR approver_user_id = $2))`, rid, p.UserID).Scan(&involved); err != nil {
			return x, err
		}
		if !involved {
			return x, errs.NotFound("approval request")
		}
	}
	return x, nil
}

func (h *HTTP) get(w http.ResponseWriter, r *http.Request) {
	rid, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true, UserID: authz.From(r.Context()).UserID})
	var out Request
	err = h.E.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		out, err = h.loadDetail(r, tx, rid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *HTTP) action(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var req DecisionRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		// Eligibility is checked by the engine; the transaction may touch
		// any property the request belongs to.
		ctx := dbtx.WithScope(r.Context(), dbtx.Scope{AllProperties: true, UserID: authz.From(r.Context()).UserID})
		var out Request
		err = h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			switch kind {
			case "approve":
				err = h.E.Decide(ctx, tx, rid, true, req.Reason)
			case "reject":
				err = h.E.Decide(ctx, tx, rid, false, req.Reason)
			case "cancel":
				err = h.E.Cancel(ctx, tx, rid, req.Reason)
			case "submit":
				_, err = h.E.SubmitDraft(ctx, tx, rid)
			}
			if err != nil {
				return err
			}
			out, err = h.loadDetail(r.WithContext(ctx), tx, rid)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

// testRequest creates a "Test Approval" document (EP-06 AC).
func (h *HTTP) testRequest(w http.ResponseWriter, r *http.Request) {
	var req TestApprovalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_request", "title is required", errs.Field("title", "required", "title is required")))
		return
	}
	if _, ok := toFloat(req.Amount); !ok {
		httpx.WriteError(w, r, errs.Validation("invalid_request", "amount must be a number", errs.Field("amount", "invalid", "must be a number")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	var out Request
	err := h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		docID := id.New()
		rid, _, err := h.E.Submit(ctx, tx, SubmitRequest{DocumentType: "test_approval", DocumentID: docID,
			DocumentRef: "TEST-" + clock.Now().Format("20060102-150405"), Title: req.Title, PropertyID: pid,
			Attributes: map[string]any{"amount": req.Amount}, Draft: req.Draft})
		if err != nil {
			return err
		}
		out, err = h.loadDetail(r, tx, rid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// ── delegations (FR-APR-07) ───────────────────────────────────────────────

func (h *HTTP) listDelegations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := authz.From(ctx)
	all := r.URL.Query().Get("all") == "true" && p.Can("platform.approval.delegate", nil)
	out := []Delegation{}
	err := h.E.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id, d.delegator_user_id, a.full_name, d.delegate_user_id, b.full_name, d.starts_at, d.ends_at,
			d.reason, d.revoked_at, (d.revoked_at IS NULL AND now() BETWEEN d.starts_at AND d.ends_at)
			FROM platform.approval_delegations d JOIN platform.users a ON a.id = d.delegator_user_id JOIN platform.users b ON b.id = d.delegate_user_id
			WHERE $2 OR d.delegator_user_id = $1 OR d.delegate_user_id = $1 ORDER BY d.starts_at DESC LIMIT 200`, p.UserID, all)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Delegation
			if err := rows.Scan(&d.ID, &d.DelegatorUserID, &d.DelegatorName, &d.DelegateUserID, &d.DelegateName, &d.StartsAt, &d.EndsAt,
				&d.Reason, &d.RevokedAt, &d.Active); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Delegation]{Items: out})
}

func (h *HTTP) createDelegation(w http.ResponseWriter, r *http.Request) {
	var req DelegationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	delegator := p.UserID
	if req.DelegatorUserID != nil && *req.DelegatorUserID != p.UserID {
		if !p.Can("platform.approval.delegate", nil) {
			httpx.WriteError(w, r, errs.Forbidden("missing permission platform.approval.delegate"))
			return
		}
		delegator = *req.DelegatorUserID
	}
	if !req.EndsAt.After(req.StartsAt) || delegator == req.DelegateUserID {
		httpx.WriteError(w, r, errs.Validation("invalid_delegation", "invalid delegation",
			errs.Field("endsAt", "invalid", "must be after the start and the delegate must differ from the delegator")))
		return
	}
	var out Delegation
	err := h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		did := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO platform.approval_delegations (id, delegator_user_id, delegate_user_id, starts_at, ends_at, reason, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, did, delegator, req.DelegateUserID, req.StartsAt, req.EndsAt, nullable(req.Reason), p.UserID); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return errs.Validation("invalid_user", "user not found", errs.Field("delegateUserId", "invalid", "user not found"))
			}
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT d.id, d.delegator_user_id, a.full_name, d.delegate_user_id, b.full_name, d.starts_at, d.ends_at, d.reason,
			d.revoked_at, (d.revoked_at IS NULL AND now() BETWEEN d.starts_at AND d.ends_at)
			FROM platform.approval_delegations d JOIN platform.users a ON a.id = d.delegator_user_id JOIN platform.users b ON b.id = d.delegate_user_id
			WHERE d.id = $1`, did).Scan(&out.ID, &out.DelegatorUserID, &out.DelegatorName, &out.DelegateUserID, &out.DelegateName,
			&out.StartsAt, &out.EndsAt, &out.Reason, &out.RevokedAt, &out.Active); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: audit.ActionCreate, EntityType: "platform.approval_delegation",
			EntityID: did.String(), EntityLabel: out.DelegatorName + " → " + out.DelegateName, After: out})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *HTTP) revokeDelegation(w http.ResponseWriter, r *http.Request) {
	did, err := httpx.PathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	p := authz.From(ctx)
	err = h.E.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var delegator uuid.UUID
		err := tx.QueryRow(ctx, `SELECT delegator_user_id FROM platform.approval_delegations WHERE id = $1 AND revoked_at IS NULL FOR UPDATE`, did).Scan(&delegator)
		if dbtx.IsNoRows(err) {
			return errs.NotFound("delegation")
		}
		if err != nil {
			return err
		}
		if delegator != p.UserID && !p.Can("platform.approval.delegate", nil) {
			return errs.Forbidden("only the delegator can revoke this delegation")
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.approval_delegations SET revoked_at = now() WHERE id = $1`, did); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "platform", Action: "revoke", EntityType: "platform.approval_delegation", EntityID: did.String()})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// Register adds the approval routes (PRD §10 Approval).
func (h *HTTP) Register(reg *route.Registry) {
	const tag = "Approvals"
	add := func(rt route.Route) { rt.Module = "platform"; rt.Tag = tag; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approval-document-types", Summary: "Document types and condition attributes",
		Permission: "platform.approval_workflow.view", Response: DocumentTypeView{}, List: true, Handler: h.documentTypes})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approval-workflows", Summary: "List approval workflows",
		Permission: "platform.approval_workflow.view", Response: Workflow{}, List: true, Query: []route.Param{{Name: "filter[documentType]"}}, Handler: h.listWorkflows})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approval-workflows", Summary: "Add approval workflow",
		Permission: "platform.approval_workflow.manage", Request: WorkflowRequest{}, Response: Workflow{}, Handler: h.createWorkflow})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approval-workflows/{id}", Summary: "View approval workflow",
		Permission: "platform.approval_workflow.view", Response: Workflow{}, Handler: h.getWorkflow})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/platform/approval-workflows/{id}", Summary: "Edit approval workflow",
		Permission: "platform.approval_workflow.manage", Request: WorkflowUpdate{}, Response: Workflow{}, Handler: h.updateWorkflow})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approvals", Summary: "Approvals inbox / my requests / all",
		Response: Request{}, List: true, Query: []route.Param{{Name: "box", Enum: []string{"inbox", "mine", "all"}}, {Name: "q"},
			{Name: "filter[status]"}, {Name: "filter[documentType]"}}, Handler: h.list})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approvals/{id}", Summary: "View approval request", Response: Request{}, Handler: h.get})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals/{id}:approve", Summary: "Approve",
		Request: DecisionRequest{}, Response: Request{}, Status: http.StatusOK, Handler: h.action("approve")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals/{id}:reject", Summary: "Reject (reason required)",
		Request: DecisionRequest{}, Response: Request{}, Status: http.StatusOK, Handler: h.action("reject")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals/{id}:cancel", Summary: "Cancel (requester, while pending)",
		Request: DecisionRequest{}, Response: Request{}, Status: http.StatusOK, Handler: h.action("cancel")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals/{id}:submit", Summary: "Submit a draft",
		Request: DecisionRequest{}, Response: Request{}, Status: http.StatusOK, Handler: h.action("submit")})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approvals:test", Summary: "Create a Test Approval document",
		Permission: "platform.approval.request_test", Scope: route.ScopeProperty, Request: TestApprovalRequest{}, Response: Request{},
		Idempotent: true, Handler: h.testRequest})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/approval-delegations", Summary: "My delegations (all=true with approval.delegate)",
		Response: Delegation{}, List: true, Query: []route.Param{{Name: "all", Type: "boolean"}}, Handler: h.listDelegations})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approval-delegations", Summary: "Delegate my approvals for a period",
		Request: DelegationRequest{}, Response: Delegation{}, Handler: h.createDelegation})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/approval-delegations/{id}:revoke", Summary: "Revoke delegation",
		Handler: h.revokeDelegation})
}

// TestDocumentType is the reference document type (EP-06 AC "Test Approval").
var TestDocumentType = provision.DocumentType{Code: "test_approval", Module: "platform", Name: "Test Approval",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}}}
