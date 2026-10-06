package hrtime

// Timesheets (HRIS improvement phase C, spec §16): the employee records the
// actual working time per day and activity for a period (at most 31 days)
// in Employee Self Service and submits it; the supervisor approves it in
// ESS → Approvals (request kind "timesheet" of the approval chain), then
// the approval workflow of hris.timesheet (none = approved at once). HR
// sees the timesheets with the attendance hours of the same days. A
// timesheet returned or rejected goes back to the employee, who edits and
// resubmits it. Timesheets record time; they do not measure productivity.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Timesheet is a timesheet with its entries (detail).
type Timesheet struct {
	ID           uuid.UUID          `json:"id" db:"id"`
	PropertyID   uuid.UUID          `json:"propertyId" db:"property_id"`
	Number       string             `json:"number" db:"number"`
	EmployeeID   uuid.UUID          `json:"employeeId" db:"employee_id"`
	EmployeeNo   string             `json:"employeeNo" db:"employee_no"`
	EmployeeName string             `json:"employeeName" db:"employee_name"`
	OrgUnitName  *string            `json:"orgUnitName" db:"org_unit_name"`
	PeriodStart  time.Time          `json:"periodStart" db:"period_start"`
	PeriodEnd    time.Time          `json:"periodEnd" db:"period_end"`
	Status       string             `json:"status" db:"status" enum:"draft,submitted,approved,rejected,cancelled"`
	TotalHours   string             `json:"totalHours" db:"total_hours"`
	Notes        *string            `json:"notes" db:"notes"`
	SubmittedAt  *time.Time         `json:"submittedAt" db:"submitted_at"`
	DecidedAt    *time.Time         `json:"decidedAt" db:"decided_at"`
	DecisionNote *string            `json:"decisionNote" db:"decision_note"`
	CreatedAt    time.Time          `json:"createdAt" db:"created_at"`
	Entries      []TimesheetEntry   `json:"entries,omitempty" db:"-"`
	Days         []TimesheetDay     `json:"days,omitempty" db:"-" doc:"Timesheet hours against the attendance of each day (detail)"`
	Steps        []TimeApprovalStep `json:"steps,omitempty" db:"-"`
}

// TimesheetEntry is the time of one activity on one day.
type TimesheetEntry struct {
	LineNo    int       `json:"lineNo" db:"line_no"`
	WorkDate  time.Time `json:"workDate" db:"work_date"`
	Hours     string    `json:"hours" db:"hours"`
	Activity  string    `json:"activity" db:"activity"`
	Reference *string   `json:"reference" db:"reference"`
	Notes     *string   `json:"notes" db:"notes"`
}

// TimesheetDay compares a day of the timesheet with the attendance.
type TimesheetDay struct {
	Date            time.Time `json:"date"`
	Hours           string    `json:"hours"`
	AttendanceHours string    `json:"attendanceHours" doc:"Worked time recorded by attendance"`
}

// TimesheetEntryInput is one entry.
type TimesheetEntryInput struct {
	WorkDate  string `json:"workDate"`
	Hours     string `json:"hours"`
	Activity  string `json:"activity"`
	Reference string `json:"reference,omitempty" doc:"Event, project or work order"`
	Notes     string `json:"notes,omitempty"`
}

// TimesheetInput creates or edits a timesheet.
type TimesheetInput struct {
	PeriodStart string                `json:"periodStart,omitempty" doc:"Create only"`
	PeriodEnd   string                `json:"periodEnd,omitempty" doc:"Create only; at most 31 days"`
	Notes       *string               `json:"notes,omitempty"`
	Entries     []TimesheetEntryInput `json:"entries"`
	Submit      bool                  `json:"submit,omitempty" doc:"Submit for approval right away"`
}

const timesheetSelect = `SELECT t.id, t.property_id, t.number, t.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name,
	t.period_start, t.period_end, t.status, trim_scale(t.total_hours)::text AS total_hours, t.notes, t.submitted_at, t.decided_at, t.decision_note, t.created_at
	FROM hris.timesheets t JOIN hris.employees e ON e.id = t.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

var timesheetStatuses = []string{"draft", "submitted", "approved", "rejected", "cancelled"}

func (m *Module) registerTimesheets(reg *route.Registry) {
	tag := "HRIS Timesheets"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/timesheets", Summary: "Timesheets", Permission: PermTimesheetView,
		Response: Timesheet{}, List: true, Query: []route.Param{{Name: "status", Enum: timesheetStatuses}, {Name: "employeeId"},
			{Name: "from", Description: "Periods ending on or after"}, {Name: "to", Description: "Periods starting on or before"}},
		Handler: listRead(m.DB, m.timesheetsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/timesheets/{id}", Summary: "Timesheet with entries, attendance and approval steps",
		Permission: PermTimesheetView, Response: Timesheet{}, Handler: handle.Read(m.DB, m.getTimesheetHTTP(false))})
	for _, approve := range []bool{true, false} {
		verb := map[bool]string{true: "approve", false: "reject"}[approve]
		add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/timesheets/{id}:" + verb, Summary: strings.ToUpper(verb[:1]) + verb[1:] +
			" a timesheet (HR level, or on behalf of the manager)", Permission: PermTimesheetApprove, Request: TimeDecision{}, Response: Timesheet{},
			Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideTimesheet(approve))})
	}
	ess := "Employee Self Service"
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/timesheets", Summary: "My timesheets", Permission: hris.PermissionESS,
		Response: Timesheet{}, List: true, Handler: listRead(m.DB, m.myTimesheetsHTTP)})
	add(reg, ess, route.Route{Method: http.MethodGet, Path: "/api/v1/ess/timesheets/{id}", Summary: "My timesheet", Permission: hris.PermissionESS,
		Response: Timesheet{}, Handler: handle.Read(m.DB, m.getTimesheetHTTP(true))})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/timesheets", Summary: "Create my timesheet (draft, or submitted)",
		Permission: hris.PermissionESS, Request: TimesheetInput{}, Response: Timesheet{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createTimesheetHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPatch, Path: "/api/v1/ess/timesheets/{id}", Summary: "Edit my draft or rejected timesheet",
		Permission: hris.PermissionESS, Request: TimesheetInput{}, Response: Timesheet{}, Handler: handle.Write(m.DB, http.StatusOK, m.patchTimesheetHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/timesheets/{id}:submit", Summary: "Submit my timesheet for approval",
		Permission: hris.PermissionESS, Request: handle.Empty{}, Response: Timesheet{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.submitTimesheetHTTP)})
	add(reg, ess, route.Route{Method: http.MethodPost, Path: "/api/v1/ess/timesheets/{id}:withdraw", Summary: "Withdraw my submitted timesheet (back to draft)",
		Permission: hris.PermissionESS, Request: TimeDecision{}, Response: Timesheet{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.withdrawTimesheetHTTP)})
}

func (m *Module) timesheetsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]Timesheet, error) {
	q := r.URL.Query()
	emp := strings.TrimSpace(q.Get("employeeId"))
	if emp != "" {
		if _, err := uuid.Parse(emp); err != nil {
			return nil, handle.Invalid("employeeId", "invalid", "must be a uuid")
		}
	}
	from, to := strings.TrimSpace(q.Get("from")), strings.TrimSpace(q.Get("to"))
	for k, v := range map[string]string{"from": from, "to": to} {
		if v != "" {
			if _, err := mustDate(k, v); err != nil {
				return nil, err
			}
		}
	}
	return handle.List[Timesheet](tx.Query(ctx, timesheetSelect+` WHERE t.property_id = $1 AND ($2 = '' OR t.status = $2)
		AND ($3 = '' OR t.employee_id::text = $3) AND ($4 = '' OR t.period_end >= $4::date) AND ($5 = '' OR t.period_start <= $5::date)
		ORDER BY CASE t.status WHEN 'submitted' THEN 0 ELSE 1 END, t.period_start DESC, e.full_name LIMIT 500`,
		handle.Property(ctx), strings.TrimSpace(q.Get("status")), emp, from, to))
}

func (m *Module) myTimesheetsHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]Timesheet, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	return handle.List[Timesheet](tx.Query(ctx, timesheetSelect+` WHERE t.employee_id = $1 AND t.status <> 'cancelled' ORDER BY t.period_start DESC LIMIT 100`,
		e.ID))
}

// timesheet loads a timesheet with its entries, the attendance of its days
// and the approval steps.
func (m *Module) timesheet(ctx context.Context, tx pgx.Tx, tid uuid.UUID) (Timesheet, error) {
	rows, err := tx.Query(ctx, timesheetSelect+` WHERE t.id = $1`, tid)
	t, err := handle.One[Timesheet](rows, err, "timesheet")
	if err != nil {
		return t, err
	}
	if t.Entries, err = handle.List[TimesheetEntry](tx.Query(ctx, `SELECT line_no, work_date, trim_scale(hours)::text AS hours, activity, reference, notes
		FROM hris.timesheet_entries WHERE timesheet_id = $1 ORDER BY work_date, line_no`, tid)); err != nil {
		return t, err
	}
	drows, err := tx.Query(ctx, `SELECT g::date, coalesce((SELECT sum(hours) FROM hris.timesheet_entries WHERE timesheet_id = $1 AND work_date = g::date), 0),
		coalesce((SELECT worked_minutes FROM hris.attendance_days WHERE employee_id = $2 AND work_date = g::date), 0)
		FROM generate_series($3::date, $4::date, interval '1 day') g ORDER BY 1`, tid, t.EmployeeID, ymd(t.PeriodStart), ymd(t.PeriodEnd))
	if err != nil {
		return t, err
	}
	for drows.Next() {
		var d TimesheetDay
		var hours decimal.Decimal
		var minutes int
		if err := drows.Scan(&d.Date, &hours, &minutes); err != nil {
			drows.Close()
			return t, err
		}
		d.Hours, d.AttendanceHours = hours.String(), decimal.NewFromInt(int64(minutes)).Div(decimal.NewFromInt(60)).Round(2).String()
		t.Days = append(t.Days, d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return t, err
	}
	t.Steps, err = m.steps(ctx, tx, KindTimesheet, tid)
	return t, err
}

func (m *Module) getTimesheetHTTP(self bool) func(ctx context.Context, tx pgx.Tx, r *http.Request) (Timesheet, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request) (Timesheet, error) {
		tid, err := handle.ID(r)
		if err != nil {
			return Timesheet{}, err
		}
		t, err := m.timesheet(ctx, tx, tid)
		if err != nil {
			return t, err
		}
		if self {
			e, err := me(ctx, tx)
			if err != nil {
				return Timesheet{}, err
			}
			if t.EmployeeID != e.ID {
				return Timesheet{}, errs.NotFound("timesheet")
			}
		} else if t.PropertyID != handle.Property(ctx) {
			return Timesheet{}, errs.NotFound("timesheet")
		}
		return t, nil
	}
}

// writeEntries validates and replaces the entries; it returns the total.
func writeEntries(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID, start, end time.Time, in []TimesheetEntryInput) (decimal.Decimal, error) {
	total := decimal.Zero
	if len(in) > 500 {
		return total, handle.Invalid("entries", "too_many", "at most 500 entries")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.timesheet_entries WHERE timesheet_id = $1`, tid); err != nil {
		return total, err
	}
	perDay := map[string]decimal.Decimal{}
	for i, x := range in {
		field := "entries[" + strconv.Itoa(i) + "]"
		d, err := mustDate(field+".workDate", x.WorkDate)
		if err != nil {
			return total, err
		}
		if d.Before(start) || d.After(end) {
			return total, handle.Invalid(field+".workDate", "invalid", "within the period of the timesheet")
		}
		h, err := handle.Decimal(field+".hours", x.Hours, decimal.Zero)
		if err != nil {
			return total, err
		}
		if !h.IsPositive() || h.GreaterThan(decimal.NewFromInt(24)) {
			return total, handle.Invalid(field+".hours", "invalid", "more than 0 and up to 24 hours")
		}
		if perDay[ymd(d)] = perDay[ymd(d)].Add(h); perDay[ymd(d)].GreaterThan(decimal.NewFromInt(24)) {
			return total, handle.Invalid(field+".hours", "invalid", "more than 24 hours on "+ymd(d))
		}
		activity := strings.TrimSpace(x.Activity)
		if activity == "" {
			return total, handle.Invalid(field+".activity", "required", "is required")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.timesheet_entries (id, property_id, timesheet_id, line_no, work_date, hours, activity, reference, notes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, tid, i+1, ymd(d), h, activity, nullStr(x.Reference), nullStr(x.Notes)); err != nil {
			return total, err
		}
		total = total.Add(h)
	}
	_, err := tx.Exec(ctx, `UPDATE hris.timesheets SET total_hours = $2 WHERE id = $1`, tid, total)
	return total, err
}

func (m *Module) createTimesheetHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req TimesheetInput) (Timesheet, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return Timesheet{}, err
	}
	start, err := mustDate("periodStart", req.PeriodStart)
	if err != nil {
		return Timesheet{}, err
	}
	end, err := mustDate("periodEnd", req.PeriodEnd)
	if err != nil {
		return Timesheet{}, err
	}
	if end.Before(start) || end.After(start.AddDate(0, 0, 30)) {
		return Timesheet{}, handle.Invalid("periodEnd", "invalid", "on or after the start, at most 31 days")
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.timesheets WHERE employee_id = $1 AND status NOT IN ('cancelled', 'rejected')
		AND daterange(period_start, period_end, '[]') && daterange($2::date, $3::date, '[]'))`, e.ID, ymd(start), ymd(end)).Scan(&overlap); err != nil {
		return Timesheet{}, err
	}
	if overlap {
		return Timesheet{}, errs.Conflict("timesheet_overlap", "a timesheet already covers part of this period")
	}
	number, err := yearlyNumber(ctx, tx, e.PropertyID, "TS", start.Year())
	if err != nil {
		return Timesheet{}, err
	}
	tid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.timesheets (id, property_id, number, employee_id, period_start, period_end, notes, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, tid, e.PropertyID, number, e.ID, ymd(start), ymd(end), req.Notes, actor(ctx)); err != nil {
		return Timesheet{}, err
	}
	total, err := writeEntries(ctx, tx, e.PropertyID, tid, start, end, req.Entries)
	if err != nil {
		return Timesheet{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.timesheet", EntityID: tid.String(),
		EntityLabel: number + " · " + e.FullName, PropertyID: &e.PropertyID, After: map[string]any{"periodStart": ymd(start), "periodEnd": ymd(end),
			"totalHours": total.String(), "entries": len(req.Entries)}}); err != nil {
		return Timesheet{}, err
	}
	if req.Submit {
		if err := m.submitTimesheet(ctx, tx, tid, e); err != nil {
			return Timesheet{}, err
		}
	}
	return m.timesheet(ctx, tx, tid)
}

// ownTimesheet locks a timesheet of the signed-in employee.
func (m *Module) ownTimesheet(ctx context.Context, tx pgx.Tx, r *http.Request) (Timesheet, hris.Employee, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return Timesheet{}, e, err
	}
	tid, err := handle.ID(r)
	if err != nil {
		return Timesheet{}, e, err
	}
	var owner uuid.UUID
	err = tx.QueryRow(ctx, `SELECT employee_id FROM hris.timesheets WHERE id = $1 FOR UPDATE`, tid).Scan(&owner)
	if dbtx.IsNoRows(err) || (err == nil && owner != e.ID) {
		return Timesheet{}, e, errs.NotFound("timesheet")
	}
	if err != nil {
		return Timesheet{}, e, err
	}
	t, err := m.timesheet(ctx, tx, tid)
	return t, e, err
}

func (m *Module) patchTimesheetHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimesheetInput) (Timesheet, error) {
	t, e, err := m.ownTimesheet(ctx, tx, r)
	if err != nil {
		return t, err
	}
	if t.Status != "draft" && t.Status != hris.RequestRejected {
		return t, statusErr("the timesheet", t.Status)
	}
	if req.Notes != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.timesheets SET notes = $2 WHERE id = $1`, t.ID, nullStr(*req.Notes)); err != nil {
			return t, err
		}
	}
	total, err := writeEntries(ctx, tx, t.PropertyID, t.ID, t.PeriodStart, t.PeriodEnd, req.Entries)
	if err != nil {
		return t, err
	}
	// a rejected timesheet is corrected as a new draft
	if _, err := tx.Exec(ctx, `UPDATE hris.timesheets SET status = 'draft', updated_by = $2 WHERE id = $1`, t.ID, actor(ctx)); err != nil {
		return t, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.timesheet", EntityID: t.ID.String(),
		EntityLabel: t.Number + " · " + e.FullName, PropertyID: &t.PropertyID, Before: map[string]any{"status": t.Status, "totalHours": t.TotalHours},
		After: map[string]any{"status": "draft", "totalHours": total.String(), "entries": len(req.Entries)}}); err != nil {
		return t, err
	}
	if req.Submit {
		if err := m.submitTimesheet(ctx, tx, t.ID, e); err != nil {
			return t, err
		}
	}
	return m.timesheet(ctx, tx, t.ID)
}

// submitTimesheet submits a draft into the approval chain (supervisor).
func (m *Module) submitTimesheet(ctx context.Context, tx pgx.Tx, tid uuid.UUID, e hris.Employee) error {
	var total decimal.Decimal
	var status string
	if err := tx.QueryRow(ctx, `SELECT status, total_hours FROM hris.timesheets WHERE id = $1`, tid).Scan(&status, &total); err != nil {
		return err
	}
	if status != "draft" {
		return statusErr("the timesheet", status)
	}
	if !total.IsPositive() {
		return handle.Invalid("entries", "required", "record the time of at least one day")
	}
	// earlier rounds of the chain (withdrawn or rejected) are replaced
	if _, err := tx.Exec(ctx, `DELETE FROM hris.request_approvals WHERE request_kind = $1 AND request_id = $2`, KindTimesheet, tid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.timesheets SET status = 'submitted', submitted_at = now(), decided_at = NULL, decided_by = NULL,
		decision_note = NULL, approval_request_id = NULL, approval_step = NULL WHERE id = $1`, tid); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit", EntityType: "hris.timesheet", EntityID: tid.String(),
		EntityLabel: e.FullName, PropertyID: &e.PropertyID, Before: map[string]any{"status": "draft"}, After: map[string]any{"status": "submitted"}}); err != nil {
		return err
	}
	summary, attrs, err := m.describe(ctx, tx, KindTimesheet, tid)
	if err != nil {
		return err
	}
	return m.startApproval(ctx, tx, KindTimesheet, tid, e, []string{"supervisor"}, summary, attrs)
}

func (m *Module) submitTimesheetHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (Timesheet, error) {
	t, e, err := m.ownTimesheet(ctx, tx, r)
	if err != nil {
		return t, err
	}
	if t.Status == hris.RequestRejected { // resubmitted as is
		if _, err := tx.Exec(ctx, `UPDATE hris.timesheets SET status = 'draft' WHERE id = $1`, t.ID); err != nil {
			return t, err
		}
	}
	if err := m.submitTimesheet(ctx, tx, t.ID, e); err != nil {
		return t, err
	}
	return m.timesheet(ctx, tx, t.ID)
}

func (m *Module) withdrawTimesheetHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (Timesheet, error) {
	t, e, err := m.ownTimesheet(ctx, tx, r)
	if err != nil {
		return t, err
	}
	h, err := loadHeader(ctx, tx, specs[KindTimesheet], t.ID, true)
	if err != nil {
		return t, err
	}
	reason := strings.TrimSpace(req.Note)
	if reason == "" {
		reason = "Withdrawn to correct"
	}
	if err := m.withdraw(ctx, tx, KindTimesheet, h, reason); err != nil {
		return t, err
	}
	// withdrawn to correct: back to draft
	if _, err := tx.Exec(ctx, `UPDATE hris.timesheets SET status = 'draft' WHERE id = $1 AND status = 'cancelled'`, t.ID); err != nil {
		return t, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "withdraw", EntityType: "hris.timesheet", EntityID: t.ID.String(),
		EntityLabel: t.Number + " · " + e.FullName, PropertyID: &t.PropertyID, Reason: reason, Before: map[string]any{"status": t.Status},
		After: map[string]any{"status": "draft"}}); err != nil {
		return t, err
	}
	return m.timesheet(ctx, tx, t.ID)
}

func (m *Module) hrDecideTimesheet(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (Timesheet, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (Timesheet, error) {
		tid, err := handle.ID(r)
		if err != nil {
			return Timesheet{}, err
		}
		if err := m.decide(ctx, tx, KindTimesheet, tid, approve, req.Note, true); err != nil {
			return Timesheet{}, err
		}
		out, err := m.timesheet(ctx, tx, tid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.timesheet", EntityID: tid.String(), EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &out.PropertyID,
			Reason: req.Note, After: map[string]any{"status": out.Status}})
	}
}

func init() {
	hris.RegisterESSSection(hris.ESSSection{Key: "timesheets", Label: "Timesheet", LabelID: "Timesheet", Icon: "schedule", Path: "/ops/ess/timesheets",
		Order: 36})
}
