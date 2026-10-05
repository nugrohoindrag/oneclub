package hrtime

// Approval of the time requests (leave, permission, overtime, shift swap,
// attendance correction; FR-LVE-02 "approval berjenjang", FR-OVT-01,
// FR-SCH-06, FR-ATT-04): the manager levels of the policy run in HRIS —
// supervisor (the employee's supervisor), department head (head of the
// employee's org unit or the nearest parent unit), HR (holders of the
// approve permission) — and are decided in ESS → Approvals (department
// heads, hris.team.approve, for their team) or in the Back Office (HR, the
// approve permission). A level without an approver with a login is skipped.
// Afterwards the approval engine applies the workflow configured for the
// document type (none = approved at once); its decision hook finalises the
// request. Each request kind applies its effect when approved.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/provision"
)

// Request kinds.
const (
	KindLeave      = "leave"
	KindPermission = "permission"
	KindOvertime   = "overtime"
	KindSwap       = "shift_swap"
	KindCorrection = "attendance_correction"
)

// RequestKinds lists the request kinds.
var RequestKinds = []string{KindLeave, KindPermission, KindOvertime, KindSwap, KindCorrection}

type kindSpec struct {
	kind, table, label, employeeCol, approvePerm, hrPath string
	doc                                                  provision.DocumentType
}

var specs = map[string]kindSpec{
	KindLeave: {kind: KindLeave, table: "hris.leave_requests", label: "leave request", employeeCol: "employee_id", approvePerm: PermLeaveApprove,
		hrPath: "/hris/leave", doc: LeaveDocumentType},
	KindPermission: {kind: KindPermission, table: "hris.permission_requests", label: "permission request", employeeCol: "employee_id",
		approvePerm: PermPermissionApprove, hrPath: "/hris/leave?tab=permission", doc: PermissionDocumentType},
	KindOvertime: {kind: KindOvertime, table: "hris.overtime_requests", label: "overtime request", employeeCol: "employee_id",
		approvePerm: PermOvertimeApprove, hrPath: "/hris/overtime", doc: OvertimeDocumentType},
	KindSwap: {kind: KindSwap, table: "hris.shift_swaps", label: "shift swap", employeeCol: "requester_employee_id", approvePerm: PermSwapApprove,
		hrPath: "/hris/schedules?tab=swaps", doc: SwapDocumentType},
	KindCorrection: {kind: KindCorrection, table: "hris.attendance_corrections", label: "attendance correction", employeeCol: "employee_id",
		approvePerm: PermCorrectionApprove, hrPath: "/hris/attendance?tab=corrections", doc: CorrectionDocumentType},
}

func spec(kind string) (kindSpec, error) {
	s, ok := specs[kind]
	if !ok {
		return s, errs.NotFound("request kind")
	}
	return s, nil
}

// header is the common part of a request.
type header struct {
	ID, PropertyID, EmployeeID uuid.UUID
	Number, Status             string
	ApprovalRequestID          *uuid.UUID
}

func loadHeader(ctx context.Context, tx pgx.Tx, s kindSpec, id uuid.UUID, lock bool) (header, error) {
	var h header
	q := `SELECT id, property_id, ` + s.employeeCol + `, number, status, approval_request_id FROM ` + s.table + ` WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, q, id).Scan(&h.ID, &h.PropertyID, &h.EmployeeID, &h.Number, &h.Status, &h.ApprovalRequestID)
	if dbtx.IsNoRows(err) {
		return h, errs.NotFound(s.label)
	}
	return h, err
}

// TimeApprovalStep is one step of a request's approval trail.
type TimeApprovalStep struct {
	StepNo       int     `json:"stepNo" db:"step_no"`
	Level        string  `json:"level" db:"level" enum:"supervisor,department_head,hr,workflow"`
	ApproverID   *string `json:"approverId" db:"approver_employee_id"`
	ApproverName *string `json:"approverName" db:"approver_name"`
	Status       string  `json:"status" db:"status" enum:"waiting,pending,approved,rejected,skipped,cancelled"`
	DecidedBy    *string `json:"decidedBy" db:"decided_by_name"`
	DecidedAt    *string `json:"decidedAt" db:"decided_at"`
	Note         *string `json:"note" db:"note"`
}

func (m *Module) steps(ctx context.Context, q pgx.Tx, kind string, id uuid.UUID) ([]TimeApprovalStep, error) {
	return handle.List[TimeApprovalStep](q.Query(ctx, `SELECT a.step_no, a.level, a.approver_employee_id::text, e.full_name AS approver_name, a.status,
		u.full_name AS decided_by_name, to_char(a.decided_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS decided_at, a.note
		FROM hris.request_approvals a LEFT JOIN hris.employees e ON e.id = a.approver_employee_id LEFT JOIN platform.users u ON u.id = a.decided_by
		WHERE a.request_kind = $1 AND a.request_id = $2 ORDER BY a.step_no`, kind, id))
}

// orgHead is the head of the employee's org unit or the nearest parent unit
// with an active head other than the employee.
func orgHead(ctx context.Context, q pgx.Tx, emp hris.Employee) (*uuid.UUID, error) {
	if emp.OrgUnitID == nil {
		return nil, nil
	}
	var head *uuid.UUID
	err := q.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, head_employee_id, 0 AS depth FROM hris.org_units WHERE id = $1
		  UNION ALL SELECT p.id, p.parent_id, p.head_employee_id, up.depth + 1 FROM hris.org_units p JOIN up ON p.id = up.parent_id WHERE up.depth < 20)
		SELECT up.head_employee_id FROM up JOIN hris.employees h ON h.id = up.head_employee_id AND h.status = 'active'
		WHERE up.head_employee_id <> $2 ORDER BY up.depth LIMIT 1`, *emp.OrgUnitID, emp.ID).Scan(&head)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	return head, err
}

type plannedStep struct {
	level    string
	approver *uuid.UUID
}

// startApproval plans the manager levels of a new request and starts the
// first one (or the workflow when no level applies). summary describes the
// request in notifications; attrs are the workflow condition attributes.
func (m *Module) startApproval(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, emp hris.Employee, levels []string, summary string,
	attrs map[string]any) error {
	s, err := spec(kind)
	if err != nil {
		return err
	}
	used := map[uuid.UUID]bool{emp.ID: true}
	var plan []plannedStep
	for _, lvl := range levels {
		switch lvl {
		case "supervisor":
			sup, err := hris.Supervisor(ctx, tx, emp.ID)
			if err != nil {
				return err
			}
			if sup != nil && !used[sup.ID] && userOf(ctx, tx, sup.ID) != nil {
				plan = append(plan, plannedStep{level: lvl, approver: &sup.ID})
				used[sup.ID] = true
			}
		case "department_head":
			head, err := orgHead(ctx, tx, emp)
			if err != nil {
				return err
			}
			if head != nil && !used[*head] && userOf(ctx, tx, *head) != nil {
				plan = append(plan, plannedStep{level: lvl, approver: head})
				used[*head] = true
			}
		case "hr":
			plan = append(plan, plannedStep{level: lvl})
		}
	}
	for i, p := range plan {
		st := "waiting"
		if i == 0 {
			st = "pending"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.request_approvals (id, property_id, request_kind, request_id, step_no, level, approver_employee_id, status)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)`, emp.PropertyID, kind, id, i+1, p.level, p.approver, st); err != nil {
			return err
		}
	}
	if len(plan) == 0 {
		return m.submitWorkflow(ctx, tx, s, id, summary, attrs)
	}
	if _, err := tx.Exec(ctx, `UPDATE `+s.table+` SET approval_step = 1 WHERE id = $1`, id); err != nil {
		return err
	}
	return m.notifyApprovers(ctx, tx, s, id, plan[0], emp, summary)
}

func (m *Module) notifyApprovers(ctx context.Context, tx pgx.Tx, s kindSpec, id uuid.UUID, p plannedStep, emp hris.Employee, summary string) error {
	var users []uuid.UUID
	link := "/ops/ess/approvals"
	if p.approver != nil {
		if u := userOf(ctx, tx, *p.approver); u != nil {
			users = append(users, *u)
		}
	} else {
		users = holders(ctx, tx, emp.PropertyID, s.approvePerm)
		link = s.hrPath
	}
	var number string
	_ = tx.QueryRow(ctx, `SELECT number FROM `+s.table+` WHERE id = $1`, id).Scan(&number)
	return m.notifyUsers(ctx, tx, emp.PropertyID, users, NotifyApprovalNeeded, link, map[string]any{"kind": s.label, "number": number,
		"employeeName": emp.FullName, "employeeNo": emp.EmployeeNo, "summary": summary})
}

// submitWorkflow hands the request to the approval engine (configured
// workflow of the document type; none = approved at once, the hook runs
// inside Submit).
func (m *Module) submitWorkflow(ctx context.Context, tx pgx.Tx, s kindSpec, id uuid.UUID, summary string, attrs map[string]any) error {
	h, err := loadHeader(ctx, tx, s, id, false)
	if err != nil {
		return err
	}
	var stepNo int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(step_no), 0) + 1 FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2`,
		s.kind, id).Scan(&stepNo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.request_approvals (id, property_id, request_kind, request_id, step_no, level, status)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'workflow', 'pending')`, h.PropertyID, s.kind, id, stepNo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+s.table+` SET approval_step = $2 WHERE id = $1`, id, stepNo); err != nil {
		return err
	}
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["employeeId"] = h.EmployeeID.String()
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: s.doc.Code, DocumentID: id, DocumentRef: h.Number,
		Title: s.doc.Name + " " + h.Number + " · " + summary, PropertyID: h.PropertyID, Attributes: attrs})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE `+s.table+` SET approval_request_id = $2 WHERE id = $1`, id, rid)
	return err
}

// TimeDecision is an approve / reject of the current manager level.
type TimeDecision struct {
	Note string `json:"note,omitempty" doc:"Required to reject"`
}

// decide approves or rejects the current manager level of a request: HR
// (viaHR: the approve permission of the kind) or a manager of the employee
// in ESS (hris.team.approve).
func (m *Module) decide(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, approve bool, note string, viaHR bool) error {
	s, err := spec(kind)
	if err != nil {
		return err
	}
	h, err := loadHeader(ctx, tx, s, id, true)
	if err != nil {
		return err
	}
	if viaHR && h.PropertyID != handle.Property(ctx) {
		return errs.NotFound(s.label)
	}
	if h.Status != hris.RequestSubmitted {
		return statusErr("the "+s.label, h.Status)
	}
	if !approve && strings.TrimSpace(note) == "" {
		return handle.Invalid("note", "required", "a reason is required to reject")
	}
	var stepNo int
	var level string
	var approver *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT step_no, level, approver_employee_id FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2
		AND status = 'pending' ORDER BY step_no LIMIT 1 FOR UPDATE`, kind, id).Scan(&stepNo, &level, &approver)
	if dbtx.IsNoRows(err) {
		return errs.Conflict("not_pending", "the "+s.label+" has no pending approval step")
	}
	if err != nil {
		return err
	}
	if level == "workflow" {
		return errs.Conflict("in_workflow", "the "+s.label+" is waiting in the approval workflow (Approvals inbox)")
	}
	mine := meOptional(ctx, tx)
	if mine != nil && mine.ID == h.EmployeeID {
		return errs.Forbidden("you cannot approve or reject your own request")
	}
	switch {
	case viaHR:
		if !can(ctx, s.approvePerm, h.PropertyID) {
			return errs.Forbidden("you may not approve " + s.label + "s")
		}
	case level == "hr":
		if !can(ctx, s.approvePerm, h.PropertyID) {
			return errs.Forbidden("this step is approved by HR")
		}
	default:
		if mine == nil || !can(ctx, hris.PermissionTeamApprove, h.PropertyID) {
			return errs.Forbidden("only a manager of the employee may approve this step")
		}
		ok := approver != nil && *approver == mine.ID
		if !ok {
			if ok, err = hris.IsManagerOf(ctx, tx, mine.ID, h.EmployeeID); err != nil {
				return err
			}
		}
		if !ok {
			return errs.Forbidden("only a manager of the employee may approve this step")
		}
	}
	st := map[bool]string{true: "approved", false: "rejected"}[approve]
	if _, err := tx.Exec(ctx, `UPDATE hris.request_approvals SET status = $4, decided_by = $3, decided_at = now(), note = $5
		WHERE request_kind = $1 AND request_id = $2 AND step_no = $6`, kind, id, actor(ctx), st, nullStr(note), stepNo); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, h.EmployeeID)
	if err != nil {
		return err
	}
	summary, attrs, err := m.describe(ctx, tx, kind, id)
	if err != nil {
		return err
	}
	if !approve {
		return m.finalize(ctx, tx, kind, id, hris.RequestRejected, actor(ctx), note)
	}
	var next *int
	var nextLevel string
	var nextApprover *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT step_no, level, approver_employee_id FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2
		AND status = 'waiting' ORDER BY step_no LIMIT 1`, kind, id).Scan(&next, &nextLevel, &nextApprover)
	if err != nil && !dbtx.IsNoRows(err) {
		return err
	}
	if next != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.request_approvals SET status = 'pending' WHERE request_kind = $1 AND request_id = $2 AND step_no = $3`,
			kind, id, *next); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE `+s.table+` SET approval_step = $2 WHERE id = $1`, id, *next); err != nil {
			return err
		}
		return m.notifyApprovers(ctx, tx, s, id, plannedStep{level: nextLevel, approver: nextApprover}, emp, summary)
	}
	return m.submitWorkflow(ctx, tx, s, id, summary, attrs)
}

// decisionHook finalises a request decided by the approval engine.
func (m *Module) decisionHook(kind string) func(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	return func(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
		s, err := spec(kind)
		if err != nil {
			return err
		}
		h, err := loadHeader(ctx, tx, s, d.DocumentID, true)
		if err != nil {
			if errs.Is(err, errs.KindNotFound) {
				return nil
			}
			return err
		}
		if h.Status != hris.RequestSubmitted {
			return nil
		}
		var by *uuid.UUID
		if d.DecidedBy != uuid.Nil {
			by = &d.DecidedBy
		}
		status := map[string]string{approval.StatusApproved: hris.RequestApproved, approval.StatusRejected: hris.RequestRejected,
			approval.StatusCancelled: hris.RequestCancelled}[d.Status]
		if status == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.request_approvals SET status = $3, decided_by = $4, decided_at = now(), note = $5
			WHERE request_kind = $1 AND request_id = $2 AND level = 'workflow' AND status = 'pending'`, kind, d.DocumentID,
			map[string]string{hris.RequestApproved: "approved", hris.RequestRejected: "rejected", hris.RequestCancelled: "cancelled"}[status], by,
			nullStr(d.Reason)); err != nil {
			return err
		}
		return m.finalize(ctx, tx, kind, d.DocumentID, status, by, d.Reason)
	}
}

// finalize closes a request and applies its effect.
func (m *Module) finalize(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, status string, by *uuid.UUID, note string) error {
	s, err := spec(kind)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+s.table+` SET status = $2, decided_by = $3, decided_at = now(), decision_note = $4, approval_step = NULL
		WHERE id = $1`, id, status, by, nullStr(note)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.request_approvals SET status = 'cancelled' WHERE request_kind = $1 AND request_id = $2
		AND status IN ('waiting', 'pending')`, kind, id); err != nil {
		return err
	}
	if status == hris.RequestApproved {
		var err error
		switch kind {
		case KindLeave:
			err = m.applyLeave(ctx, tx, id)
		case KindPermission:
			err = m.applyPermission(ctx, tx, id)
		case KindOvertime:
			err = m.applyOvertime(ctx, tx, id)
		case KindSwap:
			err = m.applySwap(ctx, tx, id)
		case KindCorrection:
			err = m.applyCorrection(ctx, tx, id)
		}
		if err != nil {
			return err
		}
	}
	h, err := loadHeader(ctx, tx, s, id, false)
	if err != nil {
		return err
	}
	summary, _, err := m.describe(ctx, tx, kind, id)
	if err != nil {
		return err
	}
	if u := userOf(ctx, tx, h.EmployeeID); u != nil && (by == nil || *u != *by) {
		link := map[string]string{KindLeave: "/ops/ess/leave", KindPermission: "/ops/ess/leave", KindOvertime: "/ops/ess/overtime",
			KindSwap: "/ops/ess/schedule", KindCorrection: "/ops/ess/attendance"}[kind]
		return m.notifyUsers(ctx, tx, h.PropertyID, []uuid.UUID{*u}, NotifyRequestDecided, link, map[string]any{"kind": s.label, "number": h.Number,
			"summary": summary, "status": status, "note": note}, "in_app", "email", "whatsapp")
	}
	return nil
}

// withdraw cancels a submitted request (the employee or HR): pending in the
// approval engine, the requester of the engine request withdraws it there.
func (m *Module) withdraw(ctx context.Context, tx pgx.Tx, kind string, h header, reason string) error {
	if h.Status != hris.RequestSubmitted && (kind != KindSwap || h.Status != "requested") {
		return statusErr("the "+specs[kind].label, h.Status)
	}
	var workflow bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2 AND level = 'workflow'
		AND status = 'pending')`, kind, h.ID).Scan(&workflow); err != nil {
		return err
	}
	if workflow && h.ApprovalRequestID != nil {
		if err := m.Approvals.Cancel(ctx, tx, *h.ApprovalRequestID, reason); err != nil {
			if errs.Is(err, errs.KindForbidden) {
				return errs.Conflict("in_workflow", "the request waits in the approval workflow; ask the approver to reject it")
			}
			return err
		}
		return nil // the decision hook finalised it
	}
	return m.finalize(ctx, tx, kind, h.ID, hris.RequestCancelled, actor(ctx), reason)
}

// describe returns the notification summary and the workflow attributes.
func (m *Module) describe(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID) (string, map[string]any, error) {
	var summary string
	attrs := map[string]any{}
	var orgUnit *string
	var err error
	switch kind {
	case KindLeave:
		var lt, from, to, days string
		var paid bool
		err = tx.QueryRow(ctx, `SELECT r.leave_type, to_char(r.start_date, 'YYYY-MM-DD'), to_char(r.end_date, 'YYYY-MM-DD'), trim_scale(r.days)::text, r.paid,
			ou.code FROM hris.leave_requests r JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
			WHERE r.id = $1`, id).Scan(&lt, &from, &to, &days, &paid, &orgUnit)
		summary = fmt.Sprintf("%s %s – %s (%s days)", lt, from, to, days)
		attrs["leaveType"], attrs["days"], attrs["paid"] = lt, hris.Dec(days).InexactFloat64(), fmt.Sprint(paid)
	case KindPermission:
		var pt, day string
		var minutes int
		err = tx.QueryRow(ctx, `SELECT r.permission_type, to_char(r.work_date, 'YYYY-MM-DD'), r.minutes, ou.code FROM hris.permission_requests r
			JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id WHERE r.id = $1`, id).
			Scan(&pt, &day, &minutes, &orgUnit)
		summary = fmt.Sprintf("%s on %s (%d minutes)", pt, day, minutes)
		attrs["permissionType"], attrs["hours"] = pt, float64(minutes)/60
	case KindOvertime:
		var day, hours, dayKind, timing string
		err = tx.QueryRow(ctx, `SELECT to_char(r.work_date, 'YYYY-MM-DD'), trim_scale(r.hours)::text, r.day_kind, r.timing, ou.code FROM hris.overtime_requests r
			JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id WHERE r.id = $1`, id).
			Scan(&day, &hours, &dayKind, &timing, &orgUnit)
		summary = fmt.Sprintf("%s hours on %s (%s)", hours, day, strings.ReplaceAll(dayKind, "_", " "))
		attrs["hours"], attrs["dayKind"], attrs["timing"] = hris.Dec(hours).InexactFloat64(), dayKind, timing
	case KindSwap:
		var day, other string
		err = tx.QueryRow(ctx, `SELECT to_char(a.work_date, 'YYYY-MM-DD'), c.full_name, ou.code FROM hris.shift_swaps s
			JOIN hris.shift_assignments a ON a.id = s.requester_assignment_id JOIN hris.employees c ON c.id = s.counterpart_employee_id
			JOIN hris.employees e ON e.id = s.requester_employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id WHERE s.id = $1`, id).
			Scan(&day, &other, &orgUnit)
		summary = fmt.Sprintf("shift of %s with %s", day, other)
	case KindCorrection:
		var day time.Time
		var property uuid.UUID
		err = tx.QueryRow(ctx, `SELECT r.work_date, r.property_id, ou.code FROM hris.attendance_corrections r
			JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id WHERE r.id = $1`, id).
			Scan(&day, &property, &orgUnit)
		summary = "attendance of " + ymd(day)
		attrs["daysBack"] = int(today(ctx, tx, property).Sub(day).Hours() / 24)
	}
	if orgUnit != nil {
		attrs["orgUnit"] = *orgUnit
	}
	return summary, attrs, err
}

// RegisterDecisions registers the document types with their decision hooks.
func (m *Module) RegisterDecisions(register func(provision.DocumentType, approval.DecisionHook)) {
	for _, k := range RequestKinds {
		register(specs[k].doc, m.decisionHook(k))
	}
}
