package hrtime

// Attendance corrections (FR-ATT-04): the employee (ESS) or HR asks to set
// the clock-in and / or clock-out of a day with a reason; after approval
// (Attendance Policy correctionRequiresApproval) the events of the day are
// replaced by correction events and the day is re-evaluated (flag
// Corrected). Requests reach back at most correctionWindowDays.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// AttendanceCorrection is a correction request.
type AttendanceCorrection struct {
	ID           uuid.UUID          `json:"id" db:"id"`
	PropertyID   uuid.UUID          `json:"propertyId" db:"property_id"`
	Number       string             `json:"number" db:"number"`
	EmployeeID   uuid.UUID          `json:"employeeId" db:"employee_id"`
	EmployeeNo   string             `json:"employeeNo" db:"employee_no"`
	EmployeeName string             `json:"employeeName" db:"employee_name"`
	WorkDate     time.Time          `json:"workDate" db:"work_date"`
	ClockIn      *time.Time         `json:"clockIn" db:"clock_in"`
	ClockOut     *time.Time         `json:"clockOut" db:"clock_out"`
	Reason       string             `json:"reason" db:"reason"`
	AttachmentID *uuid.UUID         `json:"attachmentFileId" db:"attachment_file_id"`
	Status       string             `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled"`
	DecisionNote *string            `json:"decisionNote" db:"decision_note"`
	CreatedAt    time.Time          `json:"createdAt" db:"created_at"`
	Approvals    []TimeApprovalStep `json:"approvals" db:"-"`
}

const correctionSelect = `SELECT c.id, c.property_id, c.number, c.employee_id, e.employee_no, e.full_name AS employee_name, c.work_date, c.clock_in, c.clock_out,
	c.reason, c.attachment_file_id, c.status, c.decision_note, c.created_at
	FROM hris.attendance_corrections c JOIN hris.employees e ON e.id = c.employee_id`

// AttendanceCorrectionRequest asks to correct a day (local times HH:MM; a clock-out
// before the clock-in is on the next day).
type AttendanceCorrectionRequest struct {
	EmployeeID       *uuid.UUID `json:"employeeId,omitempty" doc:"HR only; ESS corrects my own attendance"`
	WorkDate         string     `json:"workDate"`
	ClockIn          string     `json:"clockIn,omitempty" doc:"HH:MM local"`
	ClockOut         string     `json:"clockOut,omitempty" doc:"HH:MM local"`
	Reason           string     `json:"reason"`
	AttachmentFileID *uuid.UUID `json:"attachmentFileId,omitempty"`
}

func (m *Module) registerCorrections(reg *route.Registry) {
	tag := "HRIS Attendance"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/attendance-corrections", Summary: "Attendance corrections",
		Permission: PermCorrectionView, Response: AttendanceCorrection{}, List: true,
		Query: []route.Param{{Name: "status", Enum: []string{"submitted", "approved", "rejected", "cancelled"}}}, Handler: listRead(m.DB, m.hrCorrectionsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-corrections", Summary: "Request an attendance correction for an employee",
		Permission: PermCorrectionCreate, Request: AttendanceCorrectionRequest{}, Response: AttendanceCorrection{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createCorrectionHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-corrections/{id}:approve", Summary: "Approve an attendance correction",
		Permission: PermCorrectionApprove, Request: TimeDecision{}, Response: AttendanceCorrection{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideCorrection(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/attendance-corrections/{id}:reject", Summary: "Reject an attendance correction",
		Permission: PermCorrectionApprove, Request: TimeDecision{}, Response: AttendanceCorrection{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideCorrection(false))})
	ess := "Employee Self Service"
	essAdd := func(rt route.Route) {
		rt.Permission = hris.PermissionESS
		add(reg, ess, rt)
	}
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/attendance-corrections", Summary: "My attendance corrections",
		Response: AttendanceCorrection{}, List: true, Handler: listRead(m.DB, m.essCorrectionsHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/attendance-corrections", Summary: "Ask to correct my attendance",
		Request: AttendanceCorrectionRequest{}, Response: AttendanceCorrection{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createCorrectionHTTP(false))})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/attendance-corrections/{id}:cancel", Summary: "Withdraw my attendance correction",
		Request: TimeDecision{}, Response: AttendanceCorrection{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essCancelCorrectionHTTP)})
}

func (m *Module) loadCorrection(ctx context.Context, q pgx.Tx, cid uuid.UUID) (AttendanceCorrection, error) {
	c, err := getOne[AttendanceCorrection]("attendance correction")(q.Query(ctx, correctionSelect+` WHERE c.id = $1`, cid))
	if err != nil {
		return c, err
	}
	c.Approvals, err = m.steps(ctx, q, KindCorrection, cid)
	return c, err
}

func (m *Module) hrCorrectionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]AttendanceCorrection, error) {
	list, err := handle.List[AttendanceCorrection](tx.Query(ctx, correctionSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.status = $2)
		ORDER BY (c.status = 'submitted') DESC, c.created_at DESC LIMIT 500`, handle.Property(ctx), r.URL.Query().Get("status")))
	for i := range list {
		list[i].Approvals = []TimeApprovalStep{}
	}
	return list, err
}

func (m *Module) essCorrectionsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]AttendanceCorrection, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[AttendanceCorrection](tx.Query(ctx, correctionSelect+` WHERE c.employee_id = $1 ORDER BY c.created_at DESC LIMIT 100`, e.ID))
	for i := range list {
		if list[i].Approvals, err = m.steps(ctx, tx, KindCorrection, list[i].ID); err != nil {
			return nil, err
		}
	}
	return list, err
}

func (m *Module) createCorrectionHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceCorrectionRequest) (AttendanceCorrection, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req AttendanceCorrectionRequest) (AttendanceCorrection, error) {
		var emp hris.Employee
		var err error
		if viaHR {
			if req.EmployeeID == nil {
				return AttendanceCorrection{}, handle.Invalid("employeeId", "required", "choose the employee")
			}
			emp, err = employeeAt(ctx, tx, handle.Property(ctx), *req.EmployeeID)
		} else {
			if req.EmployeeID != nil {
				return AttendanceCorrection{}, handle.Invalid("employeeId", "not_allowed", "you correct your own attendance")
			}
			emp, err = me(ctx, tx)
		}
		if err != nil {
			return AttendanceCorrection{}, err
		}
		day, err := mustDate("workDate", req.WorkDate)
		if err != nil {
			return AttendanceCorrection{}, err
		}
		if strings.TrimSpace(req.Reason) == "" {
			return AttendanceCorrection{}, handle.Invalid("reason", "required", "explain the correction")
		}
		tday := today(ctx, tx, emp.PropertyID)
		if day.After(tday) {
			return AttendanceCorrection{}, handle.Invalid("workDate", "invalid", "a correction is for a past day or today")
		}
		ap, ref, err := hris.LoadAttendancePolicy(ctx, tx, emp.PropertyID, clock.Now())
		if err != nil {
			return AttendanceCorrection{}, err
		}
		_ = ref
		if !viaHR && ap.CorrectionWindowDays > 0 && tday.Sub(day) > time.Duration(ap.CorrectionWindowDays)*24*time.Hour {
			return AttendanceCorrection{}, handle.Invalid("workDate", "too_old", "corrections reach back "+itoa(ap.CorrectionWindowDays)+" days; ask HR")
		}
		if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
			return AttendanceCorrection{}, err
		}
		if strings.TrimSpace(req.ClockIn) == "" && strings.TrimSpace(req.ClockOut) == "" {
			return AttendanceCorrection{}, handle.Invalid("clockIn", "required", "give the clock-in and / or clock-out time")
		}
		loc := location(ctx, tx, emp.PropertyID)
		var in, out *time.Time
		if strings.TrimSpace(req.ClockIn) != "" {
			h, mm, err := parseClock("clockIn", req.ClockIn)
			if err != nil {
				return AttendanceCorrection{}, err
			}
			t := localAt(day, h, mm, loc).UTC()
			in = &t
		}
		if strings.TrimSpace(req.ClockOut) != "" {
			h, mm, err := parseClock("clockOut", req.ClockOut)
			if err != nil {
				return AttendanceCorrection{}, err
			}
			t := localAt(day, h, mm, loc).UTC()
			if in != nil && !t.After(*in) {
				t = localAt(day.AddDate(0, 0, 1), h, mm, loc).UTC()
			}
			out = &t
		}
		if (in != nil && in.After(clock.Now())) || (out != nil && out.After(clock.Now())) {
			return AttendanceCorrection{}, handle.Invalid("clockOut", "in_future", "the corrected time is in the future")
		}
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.attendance_corrections WHERE employee_id = $1 AND work_date = $2::date
			AND status = 'submitted')`, emp.ID, ymd(day)).Scan(&open); err != nil {
			return AttendanceCorrection{}, err
		}
		if open {
			return AttendanceCorrection{}, errs.Conflict("correction_pending", "a correction of this day waits for approval")
		}
		number, err := yearlyNumber(ctx, tx, emp.PropertyID, "AC", tday.Year())
		if err != nil {
			return AttendanceCorrection{}, err
		}
		cid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO hris.attendance_corrections (id, property_id, number, employee_id, work_date, clock_in, clock_out, reason,
			attachment_file_id, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, cid, emp.PropertyID, number, emp.ID, ymd(day), in, out,
			strings.TrimSpace(req.Reason), req.AttachmentFileID, actor(ctx)); err != nil {
			if dbtx.IsForeignKeyViolation(err) {
				return AttendanceCorrection{}, handle.Invalid("attachmentFileId", "not_found", "upload the attachment first")
			}
			return AttendanceCorrection{}, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.attendance_correction",
			EntityID: cid.String(), EntityLabel: number, PropertyID: &emp.PropertyID, Reason: req.Reason,
			After: map[string]any{"employee": emp.EmployeeNo, "workDate": ymd(day), "clockIn": in, "clockOut": out}}); err != nil {
			return AttendanceCorrection{}, err
		}
		if ap.CorrectionRequiresApproval {
			summary, attrs, err := m.describe(ctx, tx, KindCorrection, cid)
			if err != nil {
				return AttendanceCorrection{}, err
			}
			if err := m.startApproval(ctx, tx, KindCorrection, cid, emp, []string{"supervisor"}, summary, attrs); err != nil {
				return AttendanceCorrection{}, err
			}
		} else if err := m.finalize(ctx, tx, KindCorrection, cid, hris.RequestApproved, actor(ctx), "no approval required"); err != nil {
			return AttendanceCorrection{}, err
		}
		return m.loadCorrection(ctx, tx, cid)
	}
}

func (m *Module) essCancelCorrectionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (AttendanceCorrection, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return AttendanceCorrection{}, err
	}
	cid, err := handle.ID(r)
	if err != nil {
		return AttendanceCorrection{}, err
	}
	h, err := loadHeader(ctx, tx, specs[KindCorrection], cid, true)
	if err != nil || h.EmployeeID != e.ID {
		return AttendanceCorrection{}, errs.NotFound("attendance correction")
	}
	if err := m.withdraw(ctx, tx, KindCorrection, h, req.Note); err != nil {
		return AttendanceCorrection{}, err
	}
	out, err := m.loadCorrection(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.attendance_correction", EntityID: cid.String(),
		EntityLabel: h.Number, PropertyID: &h.PropertyID, Reason: req.Note, Before: map[string]any{"status": h.Status},
		After: map[string]any{"status": out.Status}})
}

func (m *Module) hrDecideCorrection(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (AttendanceCorrection, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (AttendanceCorrection, error) {
		cid, err := handle.ID(r)
		if err != nil {
			return AttendanceCorrection{}, err
		}
		if err := m.decide(ctx, tx, KindCorrection, cid, approve, req.Note, true); err != nil {
			return AttendanceCorrection{}, err
		}
		out, err := m.loadCorrection(ctx, tx, cid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.attendance_correction", EntityID: cid.String(), EntityLabel: out.Number, PropertyID: &out.PropertyID, Reason: req.Note,
			After: map[string]any{"status": out.Status}})
	}
}

// applyCorrection replaces the clock events of the day.
func (m *Module) applyCorrection(ctx context.Context, tx pgx.Tx, cid uuid.UUID) error {
	c, err := m.loadCorrection(ctx, tx, cid)
	if err != nil {
		return err
	}
	if err := checkLocked(ctx, tx, c.PropertyID, c.WorkDate); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, c.EmployeeID)
	if err != nil {
		return err
	}
	day := c.WorkDate
	for _, x := range []struct {
		dir string
		at  *time.Time
	}{{"in", c.ClockIn}, {"out", c.ClockOut}} {
		if x.at == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.attendance_events SET voided_at = now() WHERE employee_id = $1 AND work_date = $2::date AND direction = $3
			AND voided_at IS NULL`, emp.ID, ymd(day), x.dir); err != nil {
			return err
		}
		if _, err := m.recordEvent(ctx, tx, clockInput{Emp: emp, Direction: x.dir, At: *x.at, Method: hris.MethodCorrection, Source: "correction",
			CorrectionID: &c.ID, Note: c.Number + ": " + c.Reason, WorkDate: &day, Trusted: true}); err != nil {
			return err
		}
	}
	tday := today(ctx, tx, c.PropertyID)
	_, err = m.recomputeDay(ctx, tx, emp, day, day.Before(tday), nil)
	return err
}
