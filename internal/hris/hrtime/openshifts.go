package hrtime

// Open shifts (HRIS improvement phase C, spec §13): a scheduler posts an
// unassigned shift of a schedule — date, shift, optional position and the
// number of people needed. Employees of the unit see the open shifts of
// published schedules in Employee Self Service and claim them; the
// scheduler approves a claim, which becomes the employee's assignment
// through the normal roster rules (period, lock, employment,
// certifications, leave, overlap), or rejects it. A shift whose slots are
// all taken is Filled and its remaining claims are declined.

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
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// OpenShiftClaim is an employee's claim of an open shift.
type OpenShiftClaim struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	EmployeeID   uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo   string     `json:"employeeNo" db:"employee_no"`
	EmployeeName string     `json:"employeeName" db:"employee_name"`
	Status       string     `json:"status" db:"status" enum:"requested,approved,rejected,withdrawn"`
	Note         *string    `json:"note" db:"note"`
	DecisionNote *string    `json:"decisionNote" db:"decision_note"`
	DecidedAt    *time.Time `json:"decidedAt" db:"decided_at"`
	CreatedAt    time.Time  `json:"createdAt" db:"created_at"`
}

// OpenShift is an unassigned shift of a schedule.
type OpenShift struct {
	ID           uuid.UUID        `json:"id" db:"id"`
	ScheduleID   uuid.UUID        `json:"scheduleId" db:"schedule_id"`
	ScheduleName string           `json:"scheduleName" db:"schedule_name"`
	OrgUnitID    uuid.UUID        `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName  string           `json:"orgUnitName" db:"org_unit_name"`
	WorkDate     time.Time        `json:"workDate" db:"work_date"`
	ShiftID      uuid.UUID        `json:"shiftTemplateId" db:"shift_template_id"`
	ShiftName    string           `json:"shiftName" db:"shift_name"`
	StartTime    string           `json:"startTime" db:"start_time"`
	EndTime      string           `json:"endTime" db:"end_time"`
	PositionID   *uuid.UUID       `json:"positionId" db:"position_id"`
	PositionName *string          `json:"positionName" db:"position_name"`
	Slots        int              `json:"slots" db:"slots"`
	Filled       int              `json:"filled" db:"filled"`
	Status       string           `json:"status" db:"status" enum:"open,filled,cancelled"`
	Notes        *string          `json:"notes" db:"notes"`
	MyClaim      *string          `json:"myClaim" db:"-" doc:"ESS: status of my claim"`
	Claims       []OpenShiftClaim `json:"claims" db:"-"`
}

// OpenShiftInput posts an open shift.
type OpenShiftInput struct {
	WorkDate        string     `json:"workDate"`
	ShiftTemplateID uuid.UUID  `json:"shiftTemplateId"`
	PositionID      *uuid.UUID `json:"positionId,omitempty" doc:"Only employees of this position may claim"`
	Slots           int        `json:"slots"`
	Notes           string     `json:"notes,omitempty"`
}

// OpenShiftNote carries a note (claim, decision, cancel).
type OpenShiftNote struct {
	Note string `json:"note,omitempty"`
}

const openShiftSelect = `SELECT o.id, o.schedule_id, s.name AS schedule_name, s.org_unit_id, ou.name AS org_unit_name, o.work_date, o.shift_template_id,
	t.name AS shift_name, to_char(t.start_time, 'HH24:MI') AS start_time, to_char(t.end_time, 'HH24:MI') AS end_time, o.position_id,
	p.name AS position_name, o.slots, o.filled, o.status, o.notes
	FROM hris.open_shifts o JOIN hris.schedules s ON s.id = o.schedule_id JOIN hris.org_units ou ON ou.id = s.org_unit_id
	JOIN hris.shift_templates t ON t.id = o.shift_template_id LEFT JOIN hris.positions p ON p.id = o.position_id`

func (m *Module) registerOpenShifts(reg *route.Registry) {
	tag := "HRIS Schedules"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/open-shifts", Summary: "Open shifts with their claims", Permission: PermScheduleView,
		Response: OpenShift{}, List: true, Query: []route.Param{{Name: "scheduleId"}, {Name: "status", Enum: []string{"open", "filled", "cancelled"}}},
		Handler: listRead(m.DB, m.openShiftsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/schedules/{id}/open-shifts", Summary: "Post an open shift on a schedule",
		Permission: PermScheduleUpdate, Request: OpenShiftInput{}, Response: OpenShift{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createOpenShiftHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/open-shifts/{id}:cancel", Summary: "Withdraw an open shift (pending claims declined)",
		Permission: PermScheduleUpdate, Request: OpenShiftNote{}, Response: OpenShift{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelOpenShiftHTTP)})
	for _, approve := range []bool{true, false} {
		verb := map[bool]string{true: "approve", false: "reject"}[approve]
		add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/open-shift-claims/{id}:" + verb,
			Summary:    map[bool]string{true: "Approve a claim: the employee is assigned the shift", false: "Reject a claim"}[approve],
			Permission: PermScheduleUpdate, Request: OpenShiftNote{}, Response: OpenShift{}, Status: http.StatusOK,
			Handler: handle.Write(m.DB, http.StatusOK, m.decideClaimHTTP(approve))})
	}
	ess := "Employee Self Service"
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/open-shifts", Summary: "Open shifts of my unit I can claim",
		Permission: hris.PermissionESS, Response: OpenShift{}, List: true, Handler: listRead(m.DB, m.myOpenShiftsHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/open-shifts/{id}:claim", Summary: "Claim an open shift",
		Permission: hris.PermissionESS, Request: OpenShiftNote{}, Response: OpenShift{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.claimHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/open-shift-claims/{id}:withdraw", Summary: "Withdraw my claim",
		Permission: hris.PermissionESS, Request: OpenShiftNote{}, Response: OpenShift{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.withdrawClaimHTTP)})
}

func (m *Module) withClaims(ctx context.Context, tx pgx.Tx, list []OpenShift) ([]OpenShift, error) {
	for i := range list {
		cs, err := handle.List[OpenShiftClaim](tx.Query(ctx, `SELECT c.id, c.employee_id, e.employee_no, e.full_name AS employee_name, c.status, c.note,
			c.decision_note, c.decided_at, c.created_at FROM hris.open_shift_claims c JOIN hris.employees e ON e.id = c.employee_id
			WHERE c.open_shift_id = $1 ORDER BY c.created_at`, list[i].ID))
		if err != nil {
			return nil, err
		}
		list[i].Claims = cs
	}
	return list, nil
}

func (m *Module) openShiftsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]OpenShift, error) {
	sid := strings.TrimSpace(r.URL.Query().Get("scheduleId"))
	if sid != "" {
		if _, err := uuid.Parse(sid); err != nil {
			return nil, handle.Invalid("scheduleId", "invalid", "must be a uuid")
		}
	}
	list, err := handle.List[OpenShift](tx.Query(ctx, openShiftSelect+` WHERE o.property_id = $1 AND ($2 = '' OR o.schedule_id::text = $2)
		AND ($3 = '' OR o.status = $3) ORDER BY o.work_date, t.start_time LIMIT 500`, handle.Property(ctx), sid, strings.TrimSpace(r.URL.Query().Get("status"))))
	if err != nil {
		return nil, err
	}
	return m.withClaims(ctx, tx, list)
}

func (m *Module) openShift(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (OpenShift, error) {
	rows, err := tx.Query(ctx, openShiftSelect+` WHERE o.id = $1`, oid)
	o, err := handle.One[OpenShift](rows, err, "open shift")
	if err != nil {
		return o, err
	}
	list, err := m.withClaims(ctx, tx, []OpenShift{o})
	if err != nil {
		return o, err
	}
	return list[0], nil
}

func (m *Module) createOpenShiftHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftInput) (OpenShift, error) {
	s, err := m.editable(ctx, tx, r)
	if err != nil {
		return OpenShift{}, err
	}
	day, err := mustDate("workDate", req.WorkDate)
	if err != nil {
		return OpenShift{}, err
	}
	if day.Before(s.PeriodStart) || day.After(s.PeriodEnd) {
		return OpenShift{}, handle.Invalid("workDate", "invalid", "within the schedule period")
	}
	if day.Before(today(ctx, tx, s.PropertyID)) {
		return OpenShift{}, handle.Invalid("workDate", "invalid", "today or later")
	}
	t, err := loadTemplate(ctx, tx, s.PropertyID, req.ShiftTemplateID)
	if err != nil {
		return OpenShift{}, err
	}
	if t == nil || t.Status != "active" {
		return OpenShift{}, handle.Invalid("shiftTemplateId", "not_found", "shift template not found or inactive")
	}
	if req.PositionID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.positions WHERE id = $1 AND property_id = $2)`, *req.PositionID, s.PropertyID).
			Scan(&ok); err != nil || !ok {
			return OpenShift{}, handle.Invalid("positionId", "not_found", "position not found")
		}
	}
	if req.Slots < 1 || req.Slots > 50 {
		return OpenShift{}, handle.Invalid("slots", "invalid", "1 to 50 people")
	}
	oid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.open_shifts (id, property_id, schedule_id, work_date, shift_template_id, position_id, slots, notes, created_by,
		updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, oid, s.PropertyID, s.ID, ymd(day), t.ID, req.PositionID, req.Slots, nullStr(req.Notes),
		actor(ctx)); err != nil {
		return OpenShift{}, err
	}
	out, err := m.openShift(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.open_shift", EntityID: oid.String(),
		EntityLabel: s.Name + " · " + ymd(day) + " " + t.Name, PropertyID: &s.PropertyID, After: map[string]any{"workDate": ymd(day), "shift": t.Code,
			"slots": req.Slots}}); err != nil {
		return out, err
	}
	if s.Status != "published" {
		return out, nil
	}
	return out, m.notifyOpenShift(ctx, tx, s, out)
}

// notifyOpenShift tells the employees of the unit about a new open shift.
func (m *Module) notifyOpenShift(ctx context.Context, tx pgx.Tx, s ShiftSchedule, o OpenShift) error {
	rows, err := tx.Query(ctx, `SELECT u.id FROM hris.employees e JOIN platform.users u ON u.employee_id = e.id AND u.status = 'active'
		WHERE e.org_unit_id = $1 AND e.status = 'active' AND ($2::uuid IS NULL OR e.position_id = $2)`, s.OrgUnitID, o.PositionID)
	if err != nil {
		return err
	}
	var users []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return err
		}
		users = append(users, u)
	}
	rows.Close()
	return m.notifyUsers(ctx, tx, s.PropertyID, users, NotifyOpenShift, "/ops/ess/open-shifts", map[string]any{"date": ymd(o.WorkDate), "shift": o.ShiftName,
		"time": o.StartTime + "–" + o.EndTime, "unit": o.OrgUnitName, "slots": o.Slots})
}

// lockOpenShift locks an open shift of a schedule the user may manage.
func (m *Module) lockOpenShift(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (OpenShift, ShiftSchedule, error) {
	var sid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT schedule_id FROM hris.open_shifts WHERE id = $1 AND property_id = $2 FOR UPDATE`, oid, handle.Property(ctx)).Scan(&sid)
	if dbtx.IsNoRows(err) {
		return OpenShift{}, ShiftSchedule{}, errs.NotFound("open shift")
	}
	if err != nil {
		return OpenShift{}, ShiftSchedule{}, err
	}
	s, err := m.loadSchedule(ctx, tx, sid, true)
	if err != nil {
		return OpenShift{}, s, err
	}
	ok, err := canManageUnit(ctx, tx, s.PropertyID, s.OrgUnitID)
	if err != nil {
		return OpenShift{}, s, err
	}
	if !ok {
		return OpenShift{}, s, errs.Forbidden("you schedule only the units you head")
	}
	o, err := m.openShift(ctx, tx, oid)
	return o, s, err
}

func (m *Module) cancelOpenShiftHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftNote) (OpenShift, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return OpenShift{}, err
	}
	o, s, err := m.lockOpenShift(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if o.Status != "open" {
		return o, statusErr("the open shift", o.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.open_shifts SET status = 'cancelled', updated_by = $2 WHERE id = $1`, oid, actor(ctx)); err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.open_shift_claims SET status = 'rejected', decided_by = $2, decided_at = now(), decision_note = 'Open shift withdrawn'
		WHERE open_shift_id = $1 AND status = 'requested'`, oid, actor(ctx)); err != nil {
		return o, err
	}
	out, err := m.openShift(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.open_shift", EntityID: oid.String(),
		EntityLabel: s.Name + " · " + ymd(o.WorkDate) + " " + o.ShiftName, PropertyID: &s.PropertyID, Reason: req.Note,
		Before: map[string]any{"status": o.Status}, After: map[string]any{"status": "cancelled"}})
}

func (m *Module) decideClaimHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftNote) (OpenShift, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftNote) (OpenShift, error) {
		cid, err := handle.ID(r)
		if err != nil {
			return OpenShift{}, err
		}
		var oid, emp uuid.UUID
		var status string
		err = tx.QueryRow(ctx, `SELECT open_shift_id, employee_id, status FROM hris.open_shift_claims WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid,
			handle.Property(ctx)).Scan(&oid, &emp, &status)
		if dbtx.IsNoRows(err) {
			return OpenShift{}, errs.NotFound("claim")
		}
		if err != nil {
			return OpenShift{}, err
		}
		o, s, err := m.lockOpenShift(ctx, tx, oid)
		if err != nil {
			return o, err
		}
		if status != "requested" {
			return o, statusErr("the claim", status)
		}
		if !approve && strings.TrimSpace(req.Note) == "" {
			return o, handle.Invalid("note", "required", "a reason is required to reject")
		}
		if approve {
			if o.Status != "open" {
				return o, statusErr("the open shift", o.Status)
			}
			mu := newMutation()
			tday := today(ctx, tx, s.PropertyID)
			sh := o.ShiftID
			if err := m.setAssignment(ctx, tx, s, ShiftAssignmentInput{EmployeeID: emp, WorkDate: ymd(o.WorkDate), ShiftTemplateID: &sh,
				Notes: "Open shift"}, mu, false, map[uuid.UUID]*templateRow{}, location(ctx, tx, s.PropertyID), tday); err != nil {
				return o, err
			}
			if _, err := m.afterMutation(ctx, tx, s, mu, "assign", map[string]any{"openShiftId": oid.String()}); err != nil {
				return o, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.open_shifts SET filled = filled + 1, status = CASE WHEN filled + 1 >= slots THEN 'filled' ELSE status END
				WHERE id = $1`, oid); err != nil {
				return o, err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.open_shift_claims SET status = 'rejected', decided_by = $2, decided_at = now(),
				decision_note = 'All slots taken' WHERE open_shift_id = $1 AND status = 'requested' AND id <> $3
				AND (SELECT status FROM hris.open_shifts WHERE id = $1) = 'filled'`, oid, actor(ctx), cid); err != nil {
				return o, err
			}
		}
		next := map[bool]string{true: "approved", false: "rejected"}[approve]
		if _, err := tx.Exec(ctx, `UPDATE hris.open_shift_claims SET status = $2, decided_by = $3, decided_at = now(), decision_note = $4 WHERE id = $1`, cid, next,
			actor(ctx), nullStr(req.Note)); err != nil {
			return o, err
		}
		out, err := m.openShift(ctx, tx, oid)
		if err != nil {
			return out, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve_claim", false: "reject_claim"}[approve],
			EntityType: "hris.open_shift", EntityID: oid.String(), EntityLabel: s.Name + " · " + ymd(o.WorkDate) + " " + o.ShiftName, PropertyID: &s.PropertyID,
			Reason: req.Note, After: map[string]any{"claimId": cid.String(), "employeeId": emp.String(), "status": next}}); err != nil {
			return out, err
		}
		if u := userOf(ctx, tx, emp); u != nil {
			return out, m.notifyUsers(ctx, tx, s.PropertyID, []uuid.UUID{*u}, NotifyOpenShiftDecided, "/ops/ess/open-shifts", map[string]any{
				"date": ymd(o.WorkDate), "shift": o.ShiftName, "time": o.StartTime + "–" + o.EndTime, "decision": next, "note": req.Note})
		}
		return out, nil
	}
}

func (m *Module) myOpenShiftsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]OpenShift, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	if e.OrgUnitID == nil {
		return []OpenShift{}, nil
	}
	list, err := handle.List[OpenShift](tx.Query(ctx, openShiftSelect+` WHERE s.org_unit_id = $1 AND s.status = 'published' AND o.status = 'open'
		AND o.work_date >= $2::date AND (o.position_id IS NULL OR o.position_id = $3) ORDER BY o.work_date, t.start_time LIMIT 200`,
		*e.OrgUnitID, ymd(today(ctx, tx, e.PropertyID)), e.PositionID))
	if err != nil {
		return nil, err
	}
	for i := range list {
		var st string
		err := tx.QueryRow(ctx, `SELECT status FROM hris.open_shift_claims WHERE open_shift_id = $1 AND employee_id = $2 ORDER BY created_at DESC LIMIT 1`,
			list[i].ID, e.ID).Scan(&st)
		if err == nil {
			list[i].MyClaim = &st
		} else if !dbtx.IsNoRows(err) {
			return nil, err
		}
		list[i].Claims = []OpenShiftClaim{} // other employees' claims are not shown in ESS
	}
	return list, nil
}

func (m *Module) claimHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftNote) (OpenShift, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return OpenShift{}, err
	}
	oid, err := handle.ID(r)
	if err != nil {
		return OpenShift{}, err
	}
	o, err := m.openShift(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	var schedStatus string
	if err := tx.QueryRow(ctx, `SELECT s.status FROM hris.schedules s WHERE s.id = $1`, o.ScheduleID).Scan(&schedStatus); err != nil {
		return o, err
	}
	switch {
	case e.OrgUnitID == nil || *e.OrgUnitID != o.OrgUnitID || schedStatus != "published":
		return OpenShift{}, errs.NotFound("open shift")
	case o.Status != "open":
		return o, statusErr("the open shift", o.Status)
	case o.PositionID != nil && (e.PositionID == nil || *e.PositionID != *o.PositionID):
		return o, handle.Invalid("position", "not_eligible", "this open shift is for "+deref(o.PositionName, "another position"))
	case o.WorkDate.Before(today(ctx, tx, e.PropertyID)):
		return o, errs.Conflict("open_shift_past", "the open shift is in the past")
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_assignments WHERE employee_id = $1 AND work_date = $2 AND status <> 'cancelled'
		AND kind = 'shift')`, e.ID, ymd(o.WorkDate)).Scan(&busy); err != nil {
		return o, err
	}
	if busy {
		return o, errs.Conflict("already_scheduled", "you already work a shift on "+ymd(o.WorkDate))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.open_shift_claims (id, property_id, open_shift_id, employee_id, note) VALUES ($1,$2,$3,$4,$5)`, id.New(),
		e.PropertyID, oid, e.ID, nullStr(req.Note)); err != nil {
		if ok, _ := dbtx.IsUniqueViolation(err); ok {
			return o, errs.Conflict("already_claimed", "you already claimed this open shift")
		}
		return o, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "claim", EntityType: "hris.open_shift", EntityID: oid.String(),
		EntityLabel: o.ScheduleName + " · " + ymd(o.WorkDate) + " " + o.ShiftName, PropertyID: &e.PropertyID, Reason: req.Note,
		After: map[string]any{"employeeId": e.ID.String()}}); err != nil {
		return o, err
	}
	users := holders(ctx, tx, e.PropertyID, PermScheduleUpdate)
	var head *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT head_employee_id FROM hris.org_units WHERE id = $1`, o.OrgUnitID).Scan(&head)
	if head != nil {
		if u := userOf(ctx, tx, *head); u != nil {
			users = append(users, *u)
		}
	}
	if err := m.notifyUsers(ctx, tx, e.PropertyID, users, NotifyOpenShiftClaimed, "/hris/schedules/"+o.ScheduleID.String(), map[string]any{
		"employeeName": e.FullName, "date": ymd(o.WorkDate), "shift": o.ShiftName, "unit": o.OrgUnitName}); err != nil {
		return o, err
	}
	out, err := m.openShift(ctx, tx, oid)
	st := "requested"
	out.MyClaim, out.Claims = &st, []OpenShiftClaim{}
	return out, err
}

func (m *Module) withdrawClaimHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OpenShiftNote) (OpenShift, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return OpenShift{}, err
	}
	cid, err := handle.ID(r)
	if err != nil {
		return OpenShift{}, err
	}
	var oid, emp uuid.UUID
	var status string
	err = tx.QueryRow(ctx, `SELECT open_shift_id, employee_id, status FROM hris.open_shift_claims WHERE id = $1 FOR UPDATE`, cid).Scan(&oid, &emp, &status)
	if dbtx.IsNoRows(err) || (err == nil && emp != e.ID) {
		return OpenShift{}, errs.NotFound("claim")
	}
	if err != nil {
		return OpenShift{}, err
	}
	if status != "requested" {
		return OpenShift{}, statusErr("the claim", status)
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.open_shift_claims SET status = 'withdrawn', decided_at = now(), decision_note = $2 WHERE id = $1`, cid,
		nullStr(req.Note)); err != nil {
		return OpenShift{}, err
	}
	out, err := m.openShift(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	st := "withdrawn"
	out.MyClaim, out.Claims = &st, []OpenShiftClaim{}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "withdraw_claim", EntityType: "hris.open_shift", EntityID: oid.String(),
		EntityLabel: out.ScheduleName + " · " + ymd(out.WorkDate), PropertyID: &e.PropertyID, Reason: req.Note,
		After: map[string]any{"claimId": cid.String(), "status": "withdrawn"}})
}

func init() {
	hris.RegisterESSSection(hris.ESSSection{Key: "open-shifts", Label: "Open Shifts", LabelID: "Shift Terbuka", Icon: "event_available",
		Path: "/ops/ess/open-shifts", Order: 31})
}
