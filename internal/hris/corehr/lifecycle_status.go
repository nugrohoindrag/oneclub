package corehr

// Employee lifecycle statuses (HRIS improvement phase B, spec §34 Employee:
// Draft, Active, On Leave, Suspended, Terminated). Draft and the suspension
// are stored; On Leave comes from the approved leave of the day and
// Terminated / Leaving from the offboarding. GET employee-work-status lists
// the employees whose work status is not plain Active, for the Employees
// workspace (filters and status pills) and the dashboard.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Permission of the suspension.
const permSuspend = "hris.employee.suspend"

// WorkStatus is the work status of an employee today.
type WorkStatus struct {
	EmployeeID uuid.UUID  `json:"employeeId" db:"employee_id"`
	WorkStatus string     `json:"workStatus" db:"work_status" enum:"draft,on_leave,suspended,leaving,terminated,inactive"`
	Since      *time.Time `json:"since" db:"since"`
	Until      *time.Time `json:"until" db:"until"`
	Note       *string    `json:"note" db:"note" doc:"Leave type, suspension reason or leaving type"`
}

// EmployeeActivateRequest hires a draft employee.
type EmployeeActivateRequest struct {
	JoinDate string `json:"joinDate,omitempty" doc:"Default: the join date of the draft"`
}

// EmployeeSuspendRequest suspends an employee (skorsing).
type EmployeeSuspendRequest struct {
	From   string `json:"from" doc:"First day (YYYY-MM-DD)"`
	Until  string `json:"until,omitempty" doc:"Last day; empty = until reinstated"`
	Reason string `json:"reason"`
}

func (m *Module) registerLifecycleStatus(reg *route.Registry) {
	tag := "HRIS Employees"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employee-work-status",
		Summary: "Employees not plainly active today: draft, on leave, suspended, leaving, terminated", Permission: "hris.employee.view",
		Response: WorkStatus{}, List: true, Query: []route.Param{{Name: "workStatus", Enum: []string{"draft", "on_leave", "suspended", "leaving", "terminated", "inactive"}}},
		Handler: listRead(m.DB, m.workStatusHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:activate", Summary: "Hire a draft employee (employment history, onboarding)",
		Permission: "hris.employee.create", Request: EmployeeActivateRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.activateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:suspend", Summary: "Suspend an employee for a period (reason required)",
		Permission: permSuspend, Request: EmployeeSuspendRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.suspendHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:reinstate", Summary: "End the suspension of an employee",
		Permission: permSuspend, Request: ReasonRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reinstateHTTP)})
}

func (m *Module) workStatusHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]WorkStatus, error) {
	property := handle.Property(ctx)
	list, err := WorkStatuses(ctx, tx, property, today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	if f := strings.TrimSpace(r.URL.Query().Get("workStatus")); f != "" {
		kept := list[:0]
		for _, w := range list {
			if w.WorkStatus == f {
				kept = append(kept, w)
			}
		}
		list = kept
	}
	return list, nil
}

// WorkStatuses lists the employees of the property whose work status on day
// is not plain Active (one row per employee, the strongest status first).
func WorkStatuses(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]WorkStatus, error) {
	return handle.List[WorkStatus](q.Query(ctx, `SELECT DISTINCT ON (employee_id) employee_id, work_status, since, until, note FROM (
		SELECT e.id AS employee_id, 'draft' AS work_status, e.join_date AS since, NULL::date AS until, NULL::text AS note, 1 AS rank
		  FROM hris.employees e WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.status = 'draft'
		UNION ALL
		SELECT e.id, CASE WHEN e.termination_date <= $2::date OR e.status = 'inactive' THEN 'terminated' ELSE 'leaving' END, e.termination_date, NULL,
		  e.termination_type, 2
		  FROM hris.employees e WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.termination_date IS NOT NULL AND e.status <> 'draft'
		UNION ALL
		SELECT e.id, 'inactive', NULL, NULL, NULL, 3
		  FROM hris.employees e WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.status = 'inactive' AND e.termination_date IS NULL
		UNION ALL
		SELECT e.id, 'suspended', e.suspended_from, e.suspended_until, e.suspension_reason, 4
		  FROM hris.employees e WHERE e.property_id = $1 AND e.archived_at IS NULL AND e.status = 'active' AND e.suspended_from <= $2::date
		  AND (e.suspended_until IS NULL OR e.suspended_until >= $2::date)
		UNION ALL
		SELECT l.employee_id, 'on_leave', l.start_date, l.end_date, l.leave_type, 5
		  FROM hris.leave_requests l JOIN hris.employees e ON e.id = l.employee_id AND e.status = 'active'
		  WHERE l.property_id = $1 AND l.status = 'approved' AND $2::date BETWEEN l.start_date AND l.end_date
		) x ORDER BY employee_id, rank`, property, ymd(day)))
}

// lockEmployee reads the status of an employee of the property.
func lockEmployee(ctx context.Context, tx pgx.Tx, eid, property uuid.UUID) (status, name string, from, until *time.Time, err error) {
	err = tx.QueryRow(ctx, `SELECT status, full_name, suspended_from, suspended_until FROM hris.employees WHERE id = $1 AND property_id = $2
		AND archived_at IS NULL FOR UPDATE`, eid, property).Scan(&status, &name, &from, &until)
	if dbtx.IsNoRows(err) {
		err = errs.NotFound("employee")
	}
	return
}

func (m *Module) activateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req EmployeeActivateRequest) (EmployeeDetail, error) {
	property := handle.Property(ctx)
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	status, name, _, _, err := lockEmployee(ctx, tx, eid, property)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if status != "draft" {
		return EmployeeDetail{}, errs.Conflict("employee_not_draft", "only a draft employee can be activated")
	}
	join, err := parseDate("joinDate", req.JoinDate)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if join != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.employees SET join_date = $2 WHERE id = $1`, eid, ymd(*join)); err != nil {
			return EmployeeDetail{}, err
		}
	}
	var joinDate *time.Time
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET status = 'active', updated_by = $2 WHERE id = $1`, eid, actor(ctx)); err != nil {
		return EmployeeDetail{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT join_date FROM hris.employees WHERE id = $1`, eid).Scan(&joinDate); err != nil {
		return EmployeeDetail{}, err
	}
	j := ""
	if joinDate != nil {
		j = ymd(*joinDate)
	}
	if err := m.hire(ctx, tx, eid, property, j); err != nil {
		return EmployeeDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "activate", EntityType: "hris.employee", EntityID: eid.String(), EntityLabel: name,
		PropertyID: &property, Before: map[string]any{"status": "draft"}, After: map[string]any{"status": "active", "joinDate": j}}); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

func (m *Module) suspendHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req EmployeeSuspendRequest) (EmployeeDetail, error) {
	property := handle.Property(ctx)
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	status, name, _, _, err := lockEmployee(ctx, tx, eid, property)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if status != "active" {
		return EmployeeDetail{}, errs.Conflict("employee_not_active", "only an active employee can be suspended")
	}
	from, err := parseDate("from", req.From)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if from == nil {
		return EmployeeDetail{}, handle.Invalid("from", "required", "is required")
	}
	until, err := parseDate("until", req.Until)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if until != nil && until.Before(*from) {
		return EmployeeDetail{}, handle.Invalid("until", "invalid", "cannot be before the first day")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return EmployeeDetail{}, handle.Invalid("reason", "required", "is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET suspended_from = $2, suspended_until = $3, suspension_reason = $4, updated_by = $5 WHERE id = $1`,
		eid, ymd(*from), datePtr(until), reason, actor(ctx)); err != nil {
		return EmployeeDetail{}, err
	}
	if err := m.statusHistory(ctx, tx, eid, *from, "suspended", reason); err != nil {
		return EmployeeDetail{}, err
	}
	after := map[string]any{"suspendedFrom": ymd(*from), "suspendedUntil": datePtr(until)}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "suspend", EntityType: "hris.employee", EntityID: eid.String(), EntityLabel: name,
		PropertyID: &property, Reason: reason, After: after}); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

func (m *Module) reinstateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReasonRequest) (EmployeeDetail, error) {
	property := handle.Property(ctx)
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	_, name, from, until, err := lockEmployee(ctx, tx, eid, property)
	if err != nil {
		return EmployeeDetail{}, err
	}
	day := today(ctx, tx, property)
	if from == nil || (until != nil && until.Before(day)) {
		return EmployeeDetail{}, errs.Conflict("employee_not_suspended", "the employee is not suspended")
	}
	// a suspension that has not started is withdrawn; a running one ends yesterday
	if from.Before(day) {
		end := day.AddDate(0, 0, -1)
		_, err = tx.Exec(ctx, `UPDATE hris.employees SET suspended_until = $2, updated_by = $3 WHERE id = $1`, eid, ymd(end), actor(ctx))
	} else {
		_, err = tx.Exec(ctx, `UPDATE hris.employees SET suspended_from = NULL, suspended_until = NULL, suspension_reason = NULL, updated_by = $2
			WHERE id = $1`, eid, actor(ctx))
	}
	if err != nil {
		return EmployeeDetail{}, err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Reinstated"
	}
	if err := m.statusHistory(ctx, tx, eid, day, "reinstated", reason); err != nil {
		return EmployeeDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "reinstate", EntityType: "hris.employee", EntityID: eid.String(),
		EntityLabel: name, PropertyID: &property, Reason: reason, Before: map[string]any{"suspendedFrom": ymd(*from), "suspendedUntil": datePtr(until)}}); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

// statusHistory records a status change in the employment history.
func (m *Module) statusHistory(ctx context.Context, tx pgx.Tx, eid uuid.UUID, on time.Time, to, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reason, status,
		applied_at, created_by, updated_by)
		SELECT gen_random_uuid(), property_id, id, 'status_change', $2::date, employment_status, $3, $4, 'applied', now(), $5, $5 FROM hris.employees
		WHERE id = $1`, eid, ymd(on), to, reason, actor(ctx))
	return err
}
