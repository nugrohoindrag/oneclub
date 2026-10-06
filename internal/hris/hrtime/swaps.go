package hrtime

// Shift swaps between employees with approval (FR-SCH-06): the employee
// asks a colleague in ESS (My Schedule) to take a shift — optionally giving
// one of the colleague's shifts back; the colleague accepts; the manager
// (or HR) approves; the assignments change and the employees are notified.

import (
	"context"
	"fmt"
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

// ShiftSwap is a shift swap request.
type ShiftSwap struct {
	ID                      uuid.UUID          `json:"id" db:"id"`
	PropertyID              uuid.UUID          `json:"propertyId" db:"property_id"`
	Number                  string             `json:"number" db:"number"`
	RequesterID             uuid.UUID          `json:"requesterId" db:"requester_employee_id"`
	RequesterName           string             `json:"requesterName" db:"requester_name"`
	RequesterAssignmentID   uuid.UUID          `json:"requesterAssignmentId" db:"requester_assignment_id"`
	RequesterDate           time.Time          `json:"requesterDate" db:"requester_date"`
	RequesterShift          *string            `json:"requesterShift" db:"requester_shift"`
	CounterpartID           uuid.UUID          `json:"counterpartId" db:"counterpart_employee_id"`
	CounterpartName         string             `json:"counterpartName" db:"counterpart_name"`
	CounterpartAssignmentID *uuid.UUID         `json:"counterpartAssignmentId" db:"counterpart_assignment_id"`
	CounterpartDate         *time.Time         `json:"counterpartDate" db:"counterpart_date"`
	CounterpartShift        *string            `json:"counterpartShift" db:"counterpart_shift"`
	Reason                  string             `json:"reason" db:"reason"`
	Status                  string             `json:"status" db:"status" enum:"requested,submitted,approved,rejected,declined,cancelled"`
	DecisionNote            *string            `json:"decisionNote" db:"decision_note"`
	CreatedAt               time.Time          `json:"createdAt" db:"created_at"`
	Approvals               []TimeApprovalStep `json:"approvals" db:"-"`
}

const swapSelect = `SELECT s.id, s.property_id, s.number, s.requester_employee_id, r.full_name AS requester_name, s.requester_assignment_id,
	ra.work_date AS requester_date, rt.name || ' ' || to_char(rt.start_time, 'HH24:MI') || '–' || to_char(rt.end_time, 'HH24:MI') AS requester_shift,
	s.counterpart_employee_id, c.full_name AS counterpart_name, s.counterpart_assignment_id, ca.work_date AS counterpart_date,
	CASE WHEN ca.id IS NULL THEN NULL WHEN ca.kind = 'off' THEN 'Day off' ELSE ct.name || ' ' || to_char(ct.start_time, 'HH24:MI') || '–' || to_char(ct.end_time, 'HH24:MI') END
	  AS counterpart_shift,
	s.reason, s.status, s.decision_note, s.created_at
	FROM hris.shift_swaps s JOIN hris.employees r ON r.id = s.requester_employee_id JOIN hris.employees c ON c.id = s.counterpart_employee_id
	JOIN hris.shift_assignments ra ON ra.id = s.requester_assignment_id LEFT JOIN hris.shift_templates rt ON rt.id = ra.shift_template_id
	LEFT JOIN hris.shift_assignments ca ON ca.id = s.counterpart_assignment_id LEFT JOIN hris.shift_templates ct ON ct.id = ca.shift_template_id`

// ShiftSwapRequest asks a colleague to take a shift.
type ShiftSwapRequest struct {
	AssignmentID            uuid.UUID  `json:"assignmentId" doc:"My shift"`
	CounterpartEmployeeID   uuid.UUID  `json:"counterpartEmployeeId"`
	CounterpartAssignmentID *uuid.UUID `json:"counterpartAssignmentId,omitempty" doc:"The colleague's shift I take in return (empty = the colleague covers my shift)"`
	Reason                  string     `json:"reason"`
}

func (m *Module) registerSwaps(reg *route.Registry) {
	tag := "HRIS Schedules"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/shift-swaps", Summary: "Shift swaps", Permission: PermSwapView,
		Response: ShiftSwap{}, List: true, Query: []route.Param{{Name: "status", Enum: []string{"requested", "submitted", "approved", "rejected", "declined",
			"cancelled"}}}, Handler: listRead(m.DB, m.hrSwapsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/shift-swaps/{id}:approve", Summary: "Approve a shift swap (HR)",
		Permission: PermSwapApprove, Request: TimeDecision{}, Response: ShiftSwap{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideSwap(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/shift-swaps/{id}:reject", Summary: "Reject a shift swap (HR)",
		Permission: PermSwapApprove, Request: TimeDecision{}, Response: ShiftSwap{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideSwap(false))})

	ess := "Employee Self Service"
	essAdd := func(rt route.Route) {
		rt.Permission = hris.PermissionESS
		add(reg, ess, rt)
	}
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/shift-swaps", Summary: "My shift swaps (asked by me or to me)", Response: ShiftSwap{}, List: true,
		Handler: listRead(m.DB, m.essSwapsHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/shift-swaps", Summary: "Ask a colleague to swap a shift", Request: ShiftSwapRequest{},
		Response: ShiftSwap{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.essRequestSwapHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/shift-swaps/{id}:accept", Summary: "Accept a swap a colleague asked (goes to approval)",
		Request: handle.Empty{}, Response: ShiftSwap{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essAnswerSwap(true))})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/shift-swaps/{id}:decline", Summary: "Decline a swap a colleague asked",
		Request: TimeDecision{}, Response: ShiftSwap{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essAnswerSwapDecline)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/shift-swaps/{id}:cancel", Summary: "Withdraw my swap request", Request: TimeDecision{},
		Response: ShiftSwap{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essCancelSwapHTTP)})
}

func (m *Module) loadSwap(ctx context.Context, q pgx.Tx, sid uuid.UUID) (ShiftSwap, error) {
	s, err := getOne[ShiftSwap]("shift swap")(q.Query(ctx, swapSelect+` WHERE s.id = $1`, sid))
	if err != nil {
		return s, err
	}
	s.Approvals, err = m.steps(ctx, q, KindSwap, sid)
	return s, err
}

func (m *Module) hrSwapsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ShiftSwap, error) {
	list, err := handle.List[ShiftSwap](tx.Query(ctx, swapSelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.status = $2)
		ORDER BY (s.status IN ('requested', 'submitted')) DESC, s.created_at DESC LIMIT 500`, handle.Property(ctx), r.URL.Query().Get("status")))
	for i := range list {
		list[i].Approvals = []TimeApprovalStep{}
	}
	return list, err
}

func (m *Module) essSwapsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]ShiftSwap, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[ShiftSwap](tx.Query(ctx, swapSelect+` WHERE s.requester_employee_id = $1 OR s.counterpart_employee_id = $1
		ORDER BY s.created_at DESC LIMIT 100`, e.ID))
	for i := range list {
		if list[i].Approvals, err = m.steps(ctx, tx, KindSwap, list[i].ID); err != nil {
			return nil, err
		}
	}
	return list, err
}

// swapAssignment is an assignment row for swap checks.
type swapAssignment struct {
	ID, EmployeeID, ScheduleID uuid.UUID
	Day                        time.Time
	Kind, Status, SchedStatus  string
	Role                       *string
	StartsAt, EndsAt           *time.Time
}

func loadSwapAssignment(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (swapAssignment, error) {
	var a swapAssignment
	err := q.QueryRow(ctx, `SELECT a.id, a.employee_id, a.schedule_id, a.work_date, a.kind, a.status, s.status, a.workforce_role, a.starts_at, a.ends_at
		FROM hris.shift_assignments a JOIN hris.schedules s ON s.id = a.schedule_id WHERE a.id = $1 FOR UPDATE OF a`, aid).
		Scan(&a.ID, &a.EmployeeID, &a.ScheduleID, &a.Day, &a.Kind, &a.Status, &a.SchedStatus, &a.Role, &a.StartsAt, &a.EndsAt)
	if dbtx.IsNoRows(err) {
		return a, errs.NotFound("assignment")
	}
	return a, err
}

// checkSwap validates that both employees can take the other's day.
func (m *Module) checkSwap(ctx context.Context, tx pgx.Tx, mine swapAssignment, other *swapAssignment, requester, counterpart hris.Employee) error {
	tday := today(ctx, tx, requester.PropertyID)
	if mine.Kind != "shift" || mine.Status != "scheduled" || mine.SchedStatus != "published" {
		return errs.Conflict("not_swappable", "only a scheduled shift of a published schedule can be swapped")
	}
	if mine.Day.Before(tday) {
		return errs.Conflict("not_swappable", "the shift is in the past")
	}
	takes := func(emp hris.Employee, a swapAssignment, giving *swapAssignment) error {
		if !emp.EmployedOn(a.Day) {
			return errs.Conflict("not_employed", emp.FullName+" is not employed on "+ymd(a.Day))
		}
		var clash bool
		var gid *uuid.UUID
		if giving != nil {
			gid = &giving.ID
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_assignments WHERE employee_id = $1 AND work_date = $2::date AND status <> 'cancelled'
			AND kind = 'shift' AND ($3::uuid IS NULL OR id <> $3))`, emp.ID, ymd(a.Day), gid).Scan(&clash); err != nil {
			return err
		}
		if clash {
			return errs.Conflict("swap_clash", emp.FullName+" already works on "+ymd(a.Day))
		}
		if a.Kind == "shift" {
			role := ""
			if a.Role != nil {
				role = *a.Role
			}
			chk, err := hris.CheckEmployee(ctx, tx, emp.ID, role, a.Day)
			if err != nil {
				return err
			}
			if e := chk.Err(emp.FullName); e != nil {
				return e
			}
			var onLeave bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.leave_requests WHERE employee_id = $1 AND status = 'approved'
				AND $2::date BETWEEN start_date AND end_date)`, emp.ID, ymd(a.Day)).Scan(&onLeave); err != nil {
				return err
			}
			if onLeave {
				return errs.Conflict("on_leave", emp.FullName+" is on leave on "+ymd(a.Day))
			}
		}
		return nil
	}
	var back *swapAssignment
	if other != nil && other.Day.Equal(mine.Day) {
		back = other
	}
	if err := takes(counterpart, mine, back); err != nil {
		return err
	}
	if other != nil {
		if other.EmployeeID != counterpart.ID || other.Status != "scheduled" || other.SchedStatus != "published" {
			return errs.Conflict("not_swappable", "the colleague's shift is not a scheduled shift of a published schedule")
		}
		if other.Day.Before(tday) {
			return errs.Conflict("not_swappable", "the colleague's shift is in the past")
		}
		mineBack := (*swapAssignment)(nil)
		if other.Day.Equal(mine.Day) {
			mineBack = &mine
		}
		if err := takes(requester, *other, mineBack); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) essRequestSwapHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ShiftSwapRequest) (ShiftSwap, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return ShiftSwap{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return ShiftSwap{}, handle.Invalid("reason", "required", "tell your colleague and manager why")
	}
	mine, err := loadSwapAssignment(ctx, tx, req.AssignmentID)
	if err != nil || mine.EmployeeID != e.ID {
		return ShiftSwap{}, handle.Invalid("assignmentId", "not_found", "choose one of your shifts")
	}
	if req.CounterpartEmployeeID == e.ID {
		return ShiftSwap{}, handle.Invalid("counterpartEmployeeId", "invalid", "choose a colleague")
	}
	other, err := employeeAt(ctx, tx, e.PropertyID, req.CounterpartEmployeeID)
	if err != nil {
		return ShiftSwap{}, handle.Invalid("counterpartEmployeeId", "not_found", "colleague not found")
	}
	var theirs *swapAssignment
	if req.CounterpartAssignmentID != nil {
		a, err := loadSwapAssignment(ctx, tx, *req.CounterpartAssignmentID)
		if err != nil {
			return ShiftSwap{}, handle.Invalid("counterpartAssignmentId", "not_found", "shift of the colleague not found")
		}
		theirs = &a
	}
	if err := m.checkSwap(ctx, tx, mine, theirs, e, other); err != nil {
		return ShiftSwap{}, err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_swaps WHERE requester_assignment_id = $1 AND status IN ('requested', 'submitted'))`,
		mine.ID).Scan(&open); err != nil {
		return ShiftSwap{}, err
	}
	if open {
		return ShiftSwap{}, errs.Conflict("swap_pending", "a swap of this shift is already open")
	}
	number, err := yearlyNumber(ctx, tx, e.PropertyID, "SW", today(ctx, tx, e.PropertyID).Year())
	if err != nil {
		return ShiftSwap{}, err
	}
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.shift_swaps (id, property_id, number, requester_employee_id, requester_assignment_id, counterpart_employee_id,
		counterpart_assignment_id, reason, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, sid, e.PropertyID, number, e.ID, mine.ID, other.ID,
		req.CounterpartAssignmentID, strings.TrimSpace(req.Reason), actor(ctx)); err != nil {
		return ShiftSwap{}, err
	}
	out, err := m.loadSwap(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.shift_swap", EntityID: sid.String(),
		EntityLabel: number, PropertyID: &e.PropertyID, After: map[string]any{"requester": e.EmployeeNo, "counterpart": other.EmployeeNo,
			"date": ymd(mine.Day)}}); err != nil {
		return out, err
	}
	if u := userOf(ctx, tx, other.ID); u != nil {
		giveBack := ""
		if out.CounterpartDate != nil {
			giveBack = fmt.Sprintf(" and give your shift of %s", ymd(*out.CounterpartDate))
		}
		shift := ""
		if out.RequesterShift != nil {
			shift = *out.RequesterShift
		}
		if err := m.notifyUsers(ctx, tx, e.PropertyID, []uuid.UUID{*u}, NotifySwapRequested, "/ops/ess/schedule", map[string]any{"requesterName": e.FullName,
			"date": ymd(mine.Day), "shift": shift, "giveBack": giveBack}, "in_app", "email", "whatsapp"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// essAnswerSwap: the colleague accepts → the swap goes to approval.
func (m *Module) essAnswerSwap(accept bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ShiftSwap, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ShiftSwap, error) {
		return m.answerSwap(ctx, tx, r, accept, "")
	}
}

func (m *Module) essAnswerSwapDecline(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (ShiftSwap, error) {
	return m.answerSwap(ctx, tx, r, false, req.Note)
}

func (m *Module) answerSwap(ctx context.Context, tx pgx.Tx, r *http.Request, accept bool, note string) (ShiftSwap, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return ShiftSwap{}, err
	}
	sid, err := handle.ID(r)
	if err != nil {
		return ShiftSwap{}, err
	}
	h, err := loadHeader(ctx, tx, specs[KindSwap], sid, true)
	if err != nil {
		return ShiftSwap{}, err
	}
	s, err := m.loadSwap(ctx, tx, sid)
	if err != nil {
		return s, err
	}
	if s.CounterpartID != e.ID {
		return s, errs.NotFound("shift swap")
	}
	if h.Status != "requested" {
		return s, statusErr("the swap", h.Status)
	}
	if !accept {
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_swaps SET status = 'declined', counterpart_responded_at = now(), decision_note = $2 WHERE id = $1`,
			sid, nullStr(note)); err != nil {
			return s, err
		}
	} else {
		requester, err := hris.EmployeeByID(ctx, tx, s.RequesterID)
		if err != nil {
			return s, err
		}
		mine, err := loadSwapAssignment(ctx, tx, s.RequesterAssignmentID)
		if err != nil {
			return s, err
		}
		var theirs *swapAssignment
		if s.CounterpartAssignmentID != nil {
			a, err := loadSwapAssignment(ctx, tx, *s.CounterpartAssignmentID)
			if err != nil {
				return s, err
			}
			theirs = &a
		}
		if err := m.checkSwap(ctx, tx, mine, theirs, requester, e); err != nil {
			return s, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_swaps SET status = 'submitted', counterpart_responded_at = now() WHERE id = $1`, sid); err != nil {
			return s, err
		}
		summary, attrs, err := m.describe(ctx, tx, KindSwap, sid)
		if err != nil {
			return s, err
		}
		if err := m.startApproval(ctx, tx, KindSwap, sid, requester, []string{"supervisor"}, summary, attrs); err != nil {
			return s, err
		}
	}
	out, err := m.loadSwap(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "accept", false: "decline"}[accept],
		EntityType: "hris.shift_swap", EntityID: sid.String(), EntityLabel: s.Number, PropertyID: &s.PropertyID, Reason: note,
		Before: map[string]any{"status": h.Status}, After: map[string]any{"status": out.Status}})
}

func (m *Module) essCancelSwapHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (ShiftSwap, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return ShiftSwap{}, err
	}
	sid, err := handle.ID(r)
	if err != nil {
		return ShiftSwap{}, err
	}
	h, err := loadHeader(ctx, tx, specs[KindSwap], sid, true)
	if err != nil {
		return ShiftSwap{}, err
	}
	if h.EmployeeID != e.ID {
		return ShiftSwap{}, errs.NotFound("shift swap")
	}
	if h.Status == "requested" {
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_swaps SET status = 'cancelled', decision_note = $2 WHERE id = $1`, sid, nullStr(req.Note)); err != nil {
			return ShiftSwap{}, err
		}
	} else if err := m.withdraw(ctx, tx, KindSwap, h, req.Note); err != nil {
		return ShiftSwap{}, err
	}
	out, err := m.loadSwap(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.shift_swap", EntityID: sid.String(),
		EntityLabel: h.Number, PropertyID: &h.PropertyID, Reason: req.Note, Before: map[string]any{"status": h.Status},
		After: map[string]any{"status": out.Status}})
}

func (m *Module) hrDecideSwap(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (ShiftSwap, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (ShiftSwap, error) {
		sid, err := handle.ID(r)
		if err != nil {
			return ShiftSwap{}, err
		}
		if err := m.decide(ctx, tx, KindSwap, sid, approve, req.Note, true); err != nil {
			return ShiftSwap{}, err
		}
		out, err := m.loadSwap(ctx, tx, sid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.shift_swap", EntityID: sid.String(), EntityLabel: out.Number, PropertyID: &out.PropertyID, Reason: req.Note,
			After: map[string]any{"status": out.Status}})
	}
}

// applySwap moves the assignments of an approved swap.
func (m *Module) applySwap(ctx context.Context, tx pgx.Tx, sid uuid.UUID) error {
	s, err := m.loadSwap(ctx, tx, sid)
	if err != nil {
		return err
	}
	requester, err := hris.EmployeeByID(ctx, tx, s.RequesterID)
	if err != nil {
		return err
	}
	counterpart, err := hris.EmployeeByID(ctx, tx, s.CounterpartID)
	if err != nil {
		return err
	}
	mine, err := loadSwapAssignment(ctx, tx, s.RequesterAssignmentID)
	if err != nil {
		return err
	}
	var theirs *swapAssignment
	if s.CounterpartAssignmentID != nil {
		a, err := loadSwapAssignment(ctx, tx, *s.CounterpartAssignmentID)
		if err != nil {
			return err
		}
		theirs = &a
	}
	if err := m.checkSwap(ctx, tx, mine, theirs, requester, counterpart); err != nil {
		return err
	}
	if theirs != nil && theirs.Day.Equal(mine.Day) {
		// same day: exchange the shifts of the two rows
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments a SET kind = b.kind, shift_template_id = b.shift_template_id, starts_at = b.starts_at,
			ends_at = b.ends_at, break_minutes = b.break_minutes, work_minutes = b.work_minutes, workforce_role = b.workforce_role,
			swapped_from_id = CASE WHEN a.id = $1 THEN $4::uuid ELSE $3::uuid END, changed_after_publish = true
			FROM (SELECT id, kind, shift_template_id, starts_at, ends_at, break_minutes, work_minutes, workforce_role FROM hris.shift_assignments
			      WHERE id IN ($1, $2)) b WHERE a.id IN ($1, $2) AND b.id <> a.id`, mine.ID, theirs.ID, requester.ID, counterpart.ID); err != nil {
			return err
		}
	} else {
		// the counterpart's day off on my date (and mine on theirs) gives way
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'cancelled' WHERE employee_id = $1 AND work_date = $2::date AND kind = 'off'
			AND status <> 'cancelled'`, counterpart.ID, ymd(mine.Day)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET employee_id = $2, swapped_from_id = $3, changed_after_publish = true WHERE id = $1`,
			mine.ID, counterpart.ID, requester.ID); err != nil {
			return err
		}
		if theirs != nil {
			if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET status = 'cancelled' WHERE employee_id = $1 AND work_date = $2::date AND kind = 'off'
				AND status <> 'cancelled'`, requester.ID, ymd(theirs.Day)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.shift_assignments SET employee_id = $2, swapped_from_id = $3, changed_after_publish = true WHERE id = $1`,
				theirs.ID, requester.ID, counterpart.ID); err != nil {
				return err
			}
		}
	}
	changed := map[uuid.UUID]map[uuid.UUID][]string{} // schedule → employee → dates
	addChange := func(sched, emp uuid.UUID, d time.Time) {
		if changed[sched] == nil {
			changed[sched] = map[uuid.UUID][]string{}
		}
		changed[sched][emp] = append(changed[sched][emp], ymd(d))
	}
	addChange(mine.ScheduleID, requester.ID, mine.Day)
	addChange(mine.ScheduleID, counterpart.ID, mine.Day)
	if theirs != nil {
		addChange(theirs.ScheduleID, requester.ID, theirs.Day)
		addChange(theirs.ScheduleID, counterpart.ID, theirs.Day)
	}
	for sched, emps := range changed {
		sc, err := m.loadSchedule(ctx, tx, sched, false)
		if err != nil {
			return err
		}
		if err := m.publishChanges(ctx, tx, sc, emps); err != nil {
			return err
		}
	}
	return nil
}
