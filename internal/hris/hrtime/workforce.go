package hrtime

// Workforce planning per department (HRIS improvement phase B, spec §8):
// a plan states the headcount a department needs per position and shift for
// a period and a scenario (weekend, holiday, peak or low season, tournament,
// event) — for every day of the period or for given dates — prefilled from
// the staffing requirements when wanted. The gap review compares each day
// and line with the employees available (active, employed, not on leave or
// suspended) and those scheduled in the shift schedules; staff are assigned
// in the department's shift schedule. The result exports as CSV.

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Permissions of workforce planning.
const (
	PermPlanView   = "hris.workforce_plan.view"
	PermPlanManage = "hris.workforce_plan.manage"
)

// WorkforceScenarios are the planning scenarios.
var WorkforceScenarios = []string{"normal", "weekend", "holiday", "peak_season", "low_season", "tournament", "event"}

// WorkforcePlanLine is one requirement of a plan.
type WorkforcePlanLine struct {
	LineNo          int        `json:"lineNo" db:"line_no"`
	PositionID      *uuid.UUID `json:"positionId" db:"position_id"`
	PositionName    *string    `json:"positionName" db:"position_name"`
	ShiftTemplateID *uuid.UUID `json:"shiftTemplateId" db:"shift_template_id"`
	ShiftName       *string    `json:"shiftName" db:"shift_name"`
	WorkDate        *time.Time `json:"workDate" db:"work_date" doc:"Empty: every day of the period"`
	Required        int        `json:"required" db:"required"`
	Notes           *string    `json:"notes" db:"notes"`
}

// WorkforcePlan is a workforce plan of a department.
type WorkforcePlan struct {
	ID          uuid.UUID           `json:"id" db:"id"`
	Number      string              `json:"number" db:"number"`
	Name        string              `json:"name" db:"name"`
	OrgUnitID   uuid.UUID           `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName string              `json:"orgUnitName" db:"org_unit_name"`
	PeriodStart time.Time           `json:"periodStart" db:"period_start"`
	PeriodEnd   time.Time           `json:"periodEnd" db:"period_end"`
	Scenario    string              `json:"scenario" db:"scenario" enum:"normal,weekend,holiday,peak_season,low_season,tournament,event"`
	Status      string              `json:"status" db:"status" enum:"draft,active,archived"`
	Notes       *string             `json:"notes" db:"notes"`
	LineCount   int                 `json:"lineCount" db:"line_count"`
	UpdatedAt   time.Time           `json:"updatedAt" db:"updated_at"`
	Lines       []WorkforcePlanLine `json:"lines,omitempty" db:"-"`
}

// WorkforcePlanLineInput is a requirement of a plan.
type WorkforcePlanLineInput struct {
	PositionID      *uuid.UUID `json:"positionId,omitempty"`
	ShiftTemplateID *uuid.UUID `json:"shiftTemplateId,omitempty"`
	WorkDate        string     `json:"workDate,omitempty" doc:"YYYY-MM-DD within the period; empty = every day"`
	Required        int        `json:"required"`
	Notes           string     `json:"notes,omitempty"`
}

// WorkforcePlanInput creates or edits a plan.
type WorkforcePlanInput struct {
	Name             string                    `json:"name,omitempty"`
	OrgUnitID        *uuid.UUID                `json:"orgUnitId,omitempty" doc:"Create only"`
	PeriodStart      string                    `json:"periodStart,omitempty"`
	PeriodEnd        string                    `json:"periodEnd,omitempty" doc:"At most 93 days"`
	Scenario         string                    `json:"scenario,omitempty" enum:"normal,weekend,holiday,peak_season,low_season,tournament,event"`
	Notes            *string                   `json:"notes,omitempty"`
	Lines            *[]WorkforcePlanLineInput `json:"lines,omitempty" doc:"Replaces the lines"`
	FromRequirements bool                      `json:"fromRequirements,omitempty" doc:"Create: copy the staffing requirements of the department when no lines are given"`
}

// WorkforceGapRow is one day of one plan line.
type WorkforceGapRow struct {
	Date         time.Time `json:"date"`
	LineNo       int       `json:"lineNo"`
	PositionName *string   `json:"positionName"`
	ShiftName    *string   `json:"shiftName"`
	Required     int       `json:"required"`
	Available    int       `json:"available" doc:"Active employees of the department (and position) not on leave or suspended"`
	Scheduled    int       `json:"scheduled" doc:"Assigned in the shift schedules"`
	Gap          int       `json:"gap" doc:"Required − scheduled (≥ 0): still to assign"`
	Shortage     int       `json:"shortage" doc:"Required − available (≥ 0): to hire, borrow or cover with overtime"`
}

// WorkforceGap is the gap review of a plan.
type WorkforceGap struct {
	Plan      WorkforcePlan     `json:"plan"`
	Required  int               `json:"required" doc:"Sum over days and lines"`
	Scheduled int               `json:"scheduled"`
	Gap       int               `json:"gap"`
	Shortage  int               `json:"shortage"`
	Rows      []WorkforceGapRow `json:"rows"`
}

const planSelect = `SELECT p.id, p.number, p.name, p.org_unit_id, ou.name AS org_unit_name, p.period_start, p.period_end, p.scenario, p.status, p.notes,
	(SELECT count(*) FROM hris.workforce_plan_lines l WHERE l.plan_id = p.id)::int AS line_count, p.updated_at
	FROM hris.workforce_plans p JOIN hris.org_units ou ON ou.id = p.org_unit_id`

func (m *Module) registerWorkforce(reg *route.Registry) {
	tag := "HRIS Workforce Planning"
	base := "/api/v1/hris/workforce-plans"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Workforce plans", Permission: PermPlanView, Response: WorkforcePlan{}, List: true,
		Query:   []route.Param{{Name: "orgUnitId"}, {Name: "status", Enum: []string{"draft", "active", "archived"}}, {Name: "date", Description: "Plans covering the day"}},
		Handler: listRead(m.DB, m.plansHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Create a workforce plan of a department (draft)", Permission: PermPlanManage,
		Request: WorkforcePlanInput{}, Response: WorkforcePlan{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createPlanHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Workforce plan with its lines", Permission: PermPlanView,
		Response: WorkforcePlan{}, Handler: handle.Read(m.DB, m.getPlanHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Adjust a workforce plan (header, requirements)",
		Permission: PermPlanManage, Request: WorkforcePlanInput{}, Response: WorkforcePlan{}, Handler: handle.Write(m.DB, http.StatusOK, m.patchPlanHTTP)})
	for _, a := range []struct{ path, summary, from, to string }{
		{":activate", "Activate a draft plan (shown on the HR Dashboard and the schedules)", "draft", "active"},
		{":archive", "Archive a plan", "", "archived"},
	} {
		add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}" + a.path, Summary: a.summary, Permission: PermPlanManage,
			Request: handle.Empty{}, Response: WorkforcePlan{}, Status: http.StatusOK,
			Handler: handle.Write(m.DB, http.StatusOK, m.planStatusHTTP(a.from, a.to))})
	}
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/gap", Summary: "Gap review: required, available and scheduled per day and line",
		Permission: PermPlanView, Response: WorkforceGap{}, Handler: handle.Read(m.DB, m.planGapHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/export", Summary: "Planning result as CSV", Permission: PermPlanView,
		RawContent: "text/csv", Handler: m.planExportHTTP})
}

func (m *Module) plansHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]WorkforcePlan, error) {
	q := r.URL.Query()
	unit, day := strings.TrimSpace(q.Get("orgUnitId")), strings.TrimSpace(q.Get("date"))
	if unit != "" {
		if _, err := uuid.Parse(unit); err != nil {
			return nil, handle.Invalid("orgUnitId", "invalid", "must be a uuid")
		}
	}
	if day != "" {
		if _, err := mustDate("date", day); err != nil {
			return nil, err
		}
	}
	return handle.List[WorkforcePlan](tx.Query(ctx, planSelect+` WHERE p.property_id = $1 AND ($2 = '' OR p.org_unit_id::text = $2)
		AND ($3 = '' OR p.status = $3) AND ($4 = '' OR $4::date BETWEEN p.period_start AND p.period_end)
		ORDER BY p.period_start DESC, p.number DESC LIMIT 300`, handle.Property(ctx), unit, strings.TrimSpace(q.Get("status")), day))
}

func (m *Module) plan(ctx context.Context, q dbtx.Querier, pid, property uuid.UUID) (WorkforcePlan, error) {
	rows, err := q.Query(ctx, planSelect+` WHERE p.id = $1 AND p.property_id = $2`, pid, property)
	p, err := handle.One[WorkforcePlan](rows, err, "workforce plan")
	if err != nil {
		return p, err
	}
	p.Lines, err = handle.List[WorkforcePlanLine](q.Query(ctx, `SELECT l.line_no, l.position_id, ps.name AS position_name, l.shift_template_id,
		t.name AS shift_name, l.work_date, l.required, l.notes FROM hris.workforce_plan_lines l LEFT JOIN hris.positions ps ON ps.id = l.position_id
		LEFT JOIN hris.shift_templates t ON t.id = l.shift_template_id WHERE l.plan_id = $1 ORDER BY l.line_no`, pid))
	return p, err
}

func (m *Module) getPlanHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (WorkforcePlan, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return WorkforcePlan{}, err
	}
	return m.plan(ctx, tx, pid, handle.Property(ctx))
}

// planPeriod validates the period of a plan.
func planPeriod(from, to string) (time.Time, time.Time, error) {
	start, err := mustDate("periodStart", from)
	if err != nil {
		return start, start, err
	}
	end, err := mustDate("periodEnd", to)
	if err != nil {
		return start, end, err
	}
	if end.Before(start) || end.After(start.AddDate(0, 0, 92)) {
		return start, end, handle.Invalid("periodEnd", "invalid", "on or after the start, at most 93 days")
	}
	return start, end, nil
}

func (m *Module) createPlanHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req WorkforcePlanInput) (WorkforcePlan, error) {
	property := handle.Property(ctx)
	if req.OrgUnitID == nil {
		return WorkforcePlan{}, handle.Invalid("orgUnitId", "required", "is required")
	}
	var unitName string
	if err := tx.QueryRow(ctx, `SELECT name FROM hris.org_units WHERE id = $1 AND property_id = $2`, *req.OrgUnitID, property).Scan(&unitName); err != nil {
		return WorkforcePlan{}, handle.Invalid("orgUnitId", "not_found", "department not found")
	}
	start, end, err := planPeriod(req.PeriodStart, req.PeriodEnd)
	if err != nil {
		return WorkforcePlan{}, err
	}
	scenario := req.Scenario
	if scenario == "" {
		scenario = "normal"
	}
	if !slices.Contains(WorkforceScenarios, scenario) {
		return WorkforcePlan{}, enumErr("scenario", WorkforceScenarios)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = unitName + " " + start.Format("Jan 2006")
	}
	number, err := yearlyNumber(ctx, tx, property, "WFP", start.Year())
	if err != nil {
		return WorkforcePlan{}, err
	}
	pid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.workforce_plans (id, property_id, number, name, org_unit_id, period_start, period_end, scenario, notes,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, pid, property, number, name, *req.OrgUnitID, ymd(start), ymd(end), scenario,
		req.Notes, actor(ctx)); err != nil {
		return WorkforcePlan{}, err
	}
	lines := []WorkforcePlanLineInput{}
	if req.Lines != nil {
		lines = *req.Lines
	}
	if len(lines) == 0 && req.FromRequirements {
		if lines, err = requirementLines(ctx, tx, property, *req.OrgUnitID, start, end); err != nil {
			return WorkforcePlan{}, err
		}
	}
	if err := writePlanLines(ctx, tx, property, pid, start, end, lines); err != nil {
		return WorkforcePlan{}, err
	}
	out, err := m.plan(ctx, tx, pid, property)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.workforce_plan", EntityID: pid.String(),
		EntityLabel: number + " · " + name, PropertyID: &property, After: map[string]any{"orgUnit": unitName, "periodStart": ymd(start), "periodEnd": ymd(end),
			"scenario": scenario, "lines": len(out.Lines)}})
}

// requirementLines copies the active staffing requirements of a unit: one
// undated line for a requirement of every weekday, else one line per
// matching day of the period.
func requirementLines(ctx context.Context, tx pgx.Tx, property, unit uuid.UUID, start, end time.Time) ([]WorkforcePlanLineInput, error) {
	rows, err := tx.Query(ctx, `SELECT position_id, shift_template_id, weekdays, min_staff FROM hris.staffing_requirements
		WHERE property_id = $1 AND org_unit_id = $2 AND status = 'active' AND archived_at IS NULL ORDER BY created_at, id`, property, unit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkforcePlanLineInput
	for rows.Next() {
		var pos, shift *uuid.UUID
		var days []int32
		var n int
		if err := rows.Scan(&pos, &shift, &days, &n); err != nil {
			return nil, err
		}
		if len(days) == 7 {
			out = append(out, WorkforcePlanLineInput{PositionID: pos, ShiftTemplateID: shift, Required: n, Notes: "Staffing requirement"})
			continue
		}
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			wd := int32(d.Weekday())
			if wd == 0 {
				wd = 7
			}
			for _, x := range days {
				if x == wd {
					out = append(out, WorkforcePlanLineInput{PositionID: pos, ShiftTemplateID: shift, WorkDate: ymd(d), Required: n, Notes: "Staffing requirement"})
				}
			}
		}
	}
	return out, rows.Err()
}

// writePlanLines replaces the lines of a plan.
func writePlanLines(ctx context.Context, tx pgx.Tx, property, pid uuid.UUID, start, end time.Time, lines []WorkforcePlanLineInput) error {
	if len(lines) > 1000 {
		return handle.Invalid("lines", "too_many", "at most 1000 lines")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.workforce_plan_lines WHERE plan_id = $1`, pid); err != nil {
		return err
	}
	for i, l := range lines {
		field := "lines[" + strconv.Itoa(i) + "]"
		if l.Required < 0 || l.Required > 500 {
			return handle.Invalid(field+".required", "invalid", "0 to 500")
		}
		var day *string
		if strings.TrimSpace(l.WorkDate) != "" {
			d, err := mustDate(field+".workDate", l.WorkDate)
			if err != nil {
				return err
			}
			if d.Before(start) || d.After(end) {
				return handle.Invalid(field+".workDate", "invalid", "within the period of the plan")
			}
			s := ymd(d)
			day = &s
		}
		if l.PositionID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.positions WHERE id = $1 AND property_id = $2)`, *l.PositionID, property).Scan(&ok); err != nil || !ok {
				return handle.Invalid(field+".positionId", "not_found", "position not found")
			}
		}
		if l.ShiftTemplateID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.shift_templates WHERE id = $1 AND property_id = $2)`, *l.ShiftTemplateID, property).
				Scan(&ok); err != nil || !ok {
				return handle.Invalid(field+".shiftTemplateId", "not_found", "shift not found")
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.workforce_plan_lines (id, property_id, plan_id, line_no, position_id, shift_template_id, work_date, required,
			notes) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id.New(), property, pid, i+1, l.PositionID, l.ShiftTemplateID, day, l.Required,
			nullStr(l.Notes)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) patchPlanHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req WorkforcePlanInput) (WorkforcePlan, error) {
	property := handle.Property(ctx)
	pid, err := handle.ID(r)
	if err != nil {
		return WorkforcePlan{}, err
	}
	before, err := m.plan(ctx, tx, pid, property)
	if err != nil {
		return before, err
	}
	if before.Status == "archived" {
		return before, errs.Conflict("plan_archived", "an archived plan cannot be changed")
	}
	if req.OrgUnitID != nil && *req.OrgUnitID != before.OrgUnitID {
		return before, handle.Invalid("orgUnitId", "invalid", "the department of a plan cannot change")
	}
	start, end := before.PeriodStart, before.PeriodEnd
	if req.PeriodStart != "" || req.PeriodEnd != "" {
		if start, end, err = planPeriod(orStr(req.PeriodStart, ymd(start)), orStr(req.PeriodEnd, ymd(end))); err != nil {
			return before, err
		}
	}
	scenario := orStr(req.Scenario, before.Scenario)
	if !slices.Contains(WorkforceScenarios, scenario) {
		return before, enumErr("scenario", WorkforceScenarios)
	}
	notes := before.Notes
	if req.Notes != nil {
		notes = nullStr(*req.Notes)
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.workforce_plans SET name = $2, period_start = $3, period_end = $4, scenario = $5, notes = $6, updated_by = $7
		WHERE id = $1`, pid, orStr(strings.TrimSpace(req.Name), before.Name), ymd(start), ymd(end), scenario, notes, actor(ctx)); err != nil {
		return before, err
	}
	if req.Lines != nil {
		if err := writePlanLines(ctx, tx, property, pid, start, end, *req.Lines); err != nil {
			return before, err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM hris.workforce_plan_lines WHERE plan_id = $1 AND work_date IS NOT NULL AND work_date NOT BETWEEN $2 AND $3`,
		pid, ymd(start), ymd(end)); err != nil {
		return before, err
	}
	out, err := m.plan(ctx, tx, pid, property)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.workforce_plan", EntityID: pid.String(),
		EntityLabel: out.Number + " · " + out.Name, PropertyID: &property, Before: planAudit(before), After: planAudit(out)})
}

func planAudit(p WorkforcePlan) map[string]any {
	req := 0
	for _, l := range p.Lines {
		req += l.Required
	}
	return map[string]any{"name": p.Name, "periodStart": ymd(p.PeriodStart), "periodEnd": ymd(p.PeriodEnd), "scenario": p.Scenario, "lines": len(p.Lines),
		"requiredPerDay": req}
}

func (m *Module) planStatusHTTP(from, to string) func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (WorkforcePlan, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (WorkforcePlan, error) {
		property := handle.Property(ctx)
		pid, err := handle.ID(r)
		if err != nil {
			return WorkforcePlan{}, err
		}
		before, err := m.plan(ctx, tx, pid, property)
		if err != nil {
			return before, err
		}
		if (from != "" && before.Status != from) || before.Status == to {
			return before, errs.Conflict("plan_status", "the plan is "+before.Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.workforce_plans SET status = $2, updated_by = $3 WHERE id = $1`, pid, to, actor(ctx)); err != nil {
			return before, err
		}
		out, err := m.plan(ctx, tx, pid, property)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[string]string{"active": "activate", "archived": "archive"}[to], EntityType: "hris.workforce_plan",
			EntityID: pid.String(), EntityLabel: out.Number + " · " + out.Name, PropertyID: &property, Before: map[string]any{"status": before.Status},
			After: map[string]any{"status": to}})
	}
}

func (m *Module) planGapHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (WorkforceGap, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return WorkforceGap{}, err
	}
	return m.planGap(ctx, tx, pid, handle.Property(ctx))
}

// planGap compares every day and line of a plan with the available and
// scheduled employees of the department.
func (m *Module) planGap(ctx context.Context, q dbtx.Querier, pid, property uuid.UUID) (WorkforceGap, error) {
	p, err := m.plan(ctx, q, pid, property)
	if err != nil {
		return WorkforceGap{}, err
	}
	out := WorkforceGap{Plan: p, Rows: []WorkforceGapRow{}}
	rows, err := q.Query(ctx, `SELECT g::date, l.line_no, ps.name, t.name, l.required,
		(SELECT count(*) FROM hris.employees e WHERE e.org_unit_id = $2 AND e.status = 'active' AND e.archived_at IS NULL
		   AND (l.position_id IS NULL OR e.position_id = l.position_id) AND (e.join_date IS NULL OR e.join_date <= g::date)
		   AND (e.termination_date IS NULL OR e.termination_date > g::date)
		   AND NOT (e.suspended_from IS NOT NULL AND e.suspended_from <= g::date AND (e.suspended_until IS NULL OR e.suspended_until >= g::date))
		   AND NOT EXISTS (SELECT 1 FROM hris.leave_requests lr WHERE lr.employee_id = e.id AND lr.status = 'approved'
		     AND g::date BETWEEN lr.start_date AND lr.end_date))::int,
		(SELECT count(*) FROM hris.shift_assignments a JOIN hris.schedules sc ON sc.id = a.schedule_id AND sc.status <> 'cancelled'
		   JOIN hris.employees e ON e.id = a.employee_id
		 WHERE a.work_date = g::date AND a.kind = 'shift' AND a.status = 'scheduled' AND sc.org_unit_id = $2
		   AND (l.shift_template_id IS NULL OR a.shift_template_id = l.shift_template_id) AND (l.position_id IS NULL OR e.position_id = l.position_id))::int
		FROM hris.workforce_plan_lines l LEFT JOIN hris.positions ps ON ps.id = l.position_id LEFT JOIN hris.shift_templates t ON t.id = l.shift_template_id,
		generate_series($3::date, $4::date, interval '1 day') g
		WHERE l.plan_id = $1 AND (l.work_date IS NULL OR l.work_date = g::date)
		ORDER BY 1, 2`, pid, p.OrgUnitID, ymd(p.PeriodStart), ymd(p.PeriodEnd))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var x WorkforceGapRow
		if err := rows.Scan(&x.Date, &x.LineNo, &x.PositionName, &x.ShiftName, &x.Required, &x.Available, &x.Scheduled); err != nil {
			return out, err
		}
		x.Gap, x.Shortage = max(x.Required-x.Scheduled, 0), max(x.Required-x.Available, 0)
		out.Required += x.Required
		out.Scheduled += min(x.Scheduled, x.Required)
		out.Gap += x.Gap
		out.Shortage += x.Shortage
		out.Rows = append(out.Rows, x)
	}
	return out, rows.Err()
}

func (m *Module) planExportHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var g WorkforceGap
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		g, err = m.planGap(ctx, tx, pid, handle.Property(ctx))
		return err
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"Plan", "Department", "Scenario", "Date", "Line", "Position", "Shift", "Required", "Available", "Scheduled", "Gap", "Shortage"})
	for _, x := range g.Rows {
		_ = cw.Write([]string{g.Plan.Number, g.Plan.OrgUnitName, g.Plan.Scenario, ymd(x.Date), strconv.Itoa(x.LineNo), deref(x.PositionName, "Any"),
			deref(x.ShiftName, "Any"), strconv.Itoa(x.Required), strconv.Itoa(x.Available), strconv.Itoa(x.Scheduled), strconv.Itoa(x.Gap),
			strconv.Itoa(x.Shortage)})
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+g.Plan.Number+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func deref(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
