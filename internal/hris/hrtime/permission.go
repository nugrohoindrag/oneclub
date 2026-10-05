package hrtime

// Permission (izin) by hours (FR-LVE-02): late arrival, early leave and
// personal matters of the Leave Policy with their maximum hours, approved
// like leave; approved permission excuses the late arrival / early leave it
// covers, unpaid permission hours reach payroll (TimeSummaries).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// PermissionRequestView is a permission request.
type PermissionRequestView struct {
	ID             uuid.UUID          `json:"id" db:"id"`
	PropertyID     uuid.UUID          `json:"propertyId" db:"property_id"`
	Number         string             `json:"number" db:"number"`
	EmployeeID     uuid.UUID          `json:"employeeId" db:"employee_id"`
	EmployeeNo     string             `json:"employeeNo" db:"employee_no"`
	EmployeeName   string             `json:"employeeName" db:"employee_name"`
	PermissionType string             `json:"permissionType" db:"permission_type"`
	WorkDate       time.Time          `json:"workDate" db:"work_date"`
	StartsAt       time.Time          `json:"startsAt" db:"starts_at"`
	EndsAt         time.Time          `json:"endsAt" db:"ends_at"`
	Minutes        int                `json:"minutes" db:"minutes"`
	Paid           bool               `json:"paid" db:"paid"`
	Reason         string             `json:"reason" db:"reason"`
	Status         string             `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled"`
	DecisionNote   *string            `json:"decisionNote" db:"decision_note"`
	CreatedAt      time.Time          `json:"createdAt" db:"created_at"`
	Approvals      []TimeApprovalStep `json:"approvals" db:"-"`
}

const permissionSelect = `SELECT r.id, r.property_id, r.number, r.employee_id, e.employee_no, e.full_name AS employee_name, r.permission_type, r.work_date,
	r.starts_at, r.ends_at, r.minutes, r.paid, r.reason, r.status, r.decision_note, r.created_at
	FROM hris.permission_requests r JOIN hris.employees e ON e.id = r.employee_id`

// PermissionRequestInput requests permission for a time window.
type PermissionRequestInput struct {
	EmployeeID     *uuid.UUID `json:"employeeId,omitempty" doc:"HR only"`
	PermissionType string     `json:"permissionType"`
	WorkDate       string     `json:"workDate"`
	StartTime      string     `json:"startTime" doc:"HH:MM local"`
	EndTime        string     `json:"endTime" doc:"HH:MM local"`
	Reason         string     `json:"reason"`
}

func (m *Module) registerPermissions(reg *route.Registry) {
	tag := "HRIS Leave"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/permission-requests", Summary: "Permission (izin) requests",
		Permission: PermPermissionView, Response: PermissionRequestView{}, List: true,
		Query: []route.Param{{Name: "status", Enum: []string{"submitted", "approved", "rejected", "cancelled"}}}, Handler: listRead(m.DB, m.hrPermissionsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/permission-requests", Summary: "Request permission for an employee",
		Permission: PermPermissionCreate, Request: PermissionRequestInput{}, Response: PermissionRequestView{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createPermissionHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/permission-requests/{id}:approve", Summary: "Approve a permission request",
		Permission: PermPermissionApprove, Request: TimeDecision{}, Response: PermissionRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecidePermission(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/permission-requests/{id}:reject", Summary: "Reject a permission request",
		Permission: PermPermissionApprove, Request: TimeDecision{}, Response: PermissionRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecidePermission(false))})
	ess := "Employee Self Service"
	essAdd := func(rt route.Route) {
		rt.Permission = hris.PermissionESS
		add(reg, ess, rt)
	}
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/permission-requests", Summary: "My permission requests", Response: PermissionRequestView{},
		List: true, Handler: listRead(m.DB, m.essPermissionsHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/permission-requests", Summary: "Request permission (izin)",
		Request: PermissionRequestInput{}, Response: PermissionRequestView{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createPermissionHTTP(false))})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/permission-requests/{id}:cancel", Summary: "Withdraw my permission request",
		Request: TimeDecision{}, Response: PermissionRequestView{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essCancelPermissionHTTP)})
}

func (m *Module) loadPermission(ctx context.Context, q pgx.Tx, rid uuid.UUID) (PermissionRequestView, error) {
	p, err := getOne[PermissionRequestView]("permission request")(q.Query(ctx, permissionSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return p, err
	}
	p.Approvals, err = m.steps(ctx, q, KindPermission, rid)
	return p, err
}

func (m *Module) hrPermissionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PermissionRequestView, error) {
	list, err := handle.List[PermissionRequestView](tx.Query(ctx, permissionSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
		ORDER BY (r.status = 'submitted') DESC, r.work_date DESC LIMIT 500`, handle.Property(ctx), r.URL.Query().Get("status")))
	for i := range list {
		list[i].Approvals = []TimeApprovalStep{}
	}
	return list, err
}

func (m *Module) essPermissionsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]PermissionRequestView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[PermissionRequestView](tx.Query(ctx, permissionSelect+` WHERE r.employee_id = $1 ORDER BY r.work_date DESC LIMIT 100`, e.ID))
	for i := range list {
		if list[i].Approvals, err = m.steps(ctx, tx, KindPermission, list[i].ID); err != nil {
			return nil, err
		}
	}
	return list, err
}

func (m *Module) createPermissionHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req PermissionRequestInput) (PermissionRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req PermissionRequestInput) (PermissionRequestView, error) {
		var emp hris.Employee
		var err error
		if viaHR {
			if req.EmployeeID == nil {
				return PermissionRequestView{}, handle.Invalid("employeeId", "required", "choose the employee")
			}
			emp, err = employeeAt(ctx, tx, handle.Property(ctx), *req.EmployeeID)
		} else {
			if req.EmployeeID != nil {
				return PermissionRequestView{}, handle.Invalid("employeeId", "not_allowed", "you request your own permission")
			}
			emp, err = me(ctx, tx)
		}
		if err != nil {
			return PermissionRequestView{}, err
		}
		day, err := mustDate("workDate", req.WorkDate)
		if err != nil {
			return PermissionRequestView{}, err
		}
		if strings.TrimSpace(req.Reason) == "" {
			return PermissionRequestView{}, handle.Invalid("reason", "required", "give a reason")
		}
		lp, ref, err := hris.LoadLeavePolicy(ctx, tx, emp.PropertyID, clock.Now())
		if err != nil {
			return PermissionRequestView{}, err
		}
		pt, ok := lp.PermissionType(req.PermissionType)
		if !ok {
			return PermissionRequestView{}, handle.Invalid("permissionType", "invalid", "choose a permission type of the Leave Policy")
		}
		loc := location(ctx, tx, emp.PropertyID)
		sh, sm, err := parseClock("startTime", req.StartTime)
		if err != nil {
			return PermissionRequestView{}, err
		}
		eh, em, err := parseClock("endTime", req.EndTime)
		if err != nil {
			return PermissionRequestView{}, err
		}
		start, end := localAt(day, sh, sm, loc), localAt(day, eh, em, loc)
		if !end.After(start) {
			end = localAt(day.AddDate(0, 0, 1), eh, em, loc)
		}
		minutes := int(end.Sub(start) / time.Minute)
		if maxH := hris.Dec(pt.MaxHours); maxH.IsPositive() && float64(minutes) > maxH.InexactFloat64()*60 {
			return PermissionRequestView{}, handle.Invalid("endTime", "too_long", pt.Name+" is at most "+pt.MaxHours+" hours")
		}
		if !emp.EmployedOn(day) {
			return PermissionRequestView{}, handle.Invalid("workDate", "not_employed", "the employee is not employed on that day")
		}
		if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
			return PermissionRequestView{}, err
		}
		var overlap bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.permission_requests WHERE employee_id = $1 AND status IN ('submitted', 'approved')
			AND tstzrange(starts_at, ends_at) && tstzrange($2, $3))`, emp.ID, start, end).Scan(&overlap); err != nil {
			return PermissionRequestView{}, err
		}
		if overlap {
			return PermissionRequestView{}, errs.Conflict("permission_overlap", "the time overlaps another permission request")
		}
		number, err := yearlyNumber(ctx, tx, emp.PropertyID, "PM", today(ctx, tx, emp.PropertyID).Year())
		if err != nil {
			return PermissionRequestView{}, err
		}
		rid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO hris.permission_requests (id, property_id, number, employee_id, permission_type, work_date, starts_at, ends_at,
			minutes, paid, reason, policy_version, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)`, rid, emp.PropertyID, number,
			emp.ID, pt.Code, ymd(day), start.UTC(), end.UTC(), minutes, pt.Paid, strings.TrimSpace(req.Reason), ref.Version, actor(ctx)); err != nil {
			return PermissionRequestView{}, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.permission_request",
			EntityID: rid.String(), EntityLabel: number + " · " + emp.FullName, PropertyID: &emp.PropertyID,
			After: map[string]any{"permissionType": pt.Code, "workDate": ymd(day), "minutes": minutes, "viaHR": viaHR}}); err != nil {
			return PermissionRequestView{}, err
		}
		summary, attrs, err := m.describe(ctx, tx, KindPermission, rid)
		if err != nil {
			return PermissionRequestView{}, err
		}
		if err := m.startApproval(ctx, tx, KindPermission, rid, emp, lp.ApprovalLevels, summary, attrs); err != nil {
			return PermissionRequestView{}, err
		}
		return m.loadPermission(ctx, tx, rid)
	}
}

func (m *Module) essCancelPermissionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (PermissionRequestView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return PermissionRequestView{}, err
	}
	rid, err := handle.ID(r)
	if err != nil {
		return PermissionRequestView{}, err
	}
	h, err := loadHeader(ctx, tx, specs[KindPermission], rid, true)
	if err != nil || h.EmployeeID != e.ID {
		return PermissionRequestView{}, errs.NotFound("permission request")
	}
	if err := m.withdraw(ctx, tx, KindPermission, h, req.Note); err != nil {
		return PermissionRequestView{}, err
	}
	out, err := m.loadPermission(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.permission_request", EntityID: rid.String(),
		EntityLabel: h.Number, PropertyID: &h.PropertyID, Reason: req.Note, Before: map[string]any{"status": h.Status},
		After: map[string]any{"status": out.Status}})
}

func (m *Module) hrDecidePermission(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (PermissionRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (PermissionRequestView, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return PermissionRequestView{}, err
		}
		if err := m.decide(ctx, tx, KindPermission, rid, approve, req.Note, true); err != nil {
			return PermissionRequestView{}, err
		}
		out, err := m.loadPermission(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.permission_request", EntityID: rid.String(), EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &out.PropertyID,
			Reason: req.Note, After: map[string]any{"status": out.Status}})
	}
}

// applyPermission re-evaluates the day of approved permission.
func (m *Module) applyPermission(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	var eid uuid.UUID
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT employee_id, work_date FROM hris.permission_requests WHERE id = $1`, rid).Scan(&eid, &day); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return err
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
		return err
	}
	return m.reevaluate(ctx, tx, emp, day, day)
}
