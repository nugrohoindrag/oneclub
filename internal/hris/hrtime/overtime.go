package hrtime

// Overtime (EP-08 FR-OVT-01–04): requests before the work or afterwards
// with a reason (within afterTheFactDays), daily / weekly limits of the
// Overtime Policy, approval (only approved overtime is paid), payable hours
// from the actual attendance vs the schedule, multiplier tiers on the PP
// 35/2021 basis (hourly wage = 1/173 of the monthly wage; workday 1.5× the
// first hour and 2× the next; rest days / holidays per table) and the
// exceptions of overtime worked without approval.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// OvertimeRequestView is an overtime request.
type OvertimeRequestView struct {
	ID              uuid.UUID           `json:"id" db:"id"`
	PropertyID      uuid.UUID           `json:"propertyId" db:"property_id"`
	Number          string              `json:"number" db:"number"`
	EmployeeID      uuid.UUID           `json:"employeeId" db:"employee_id"`
	EmployeeNo      string              `json:"employeeNo" db:"employee_no"`
	EmployeeName    string              `json:"employeeName" db:"employee_name"`
	OrgUnitName     *string             `json:"orgUnitName" db:"org_unit_name"`
	WorkDate        time.Time           `json:"workDate" db:"work_date"`
	StartsAt        time.Time           `json:"startsAt" db:"starts_at"`
	EndsAt          time.Time           `json:"endsAt" db:"ends_at"`
	Hours           string              `json:"hours" db:"hours"`
	Timing          string              `json:"timing" db:"timing" enum:"before,after"`
	Reason          string              `json:"reason" db:"reason"`
	DayKind         string              `json:"dayKind" db:"day_kind" enum:"workday,rest_day,shortest_day"`
	WorkWeekDays    int                 `json:"workWeekDays" db:"work_week_days"`
	ActualHours     *string             `json:"actualHours" db:"actual_hours" doc:"Overtime clocked on the day"`
	PayableHours    string              `json:"payableHours" db:"payable_hours"`
	MultipliedHours string              `json:"multipliedHours" db:"multiplied_hours"`
	Tiers           []hris.OvertimeTier `json:"tiers" db:"tiers"`
	EstimatedPay    *string             `json:"estimatedPay" db:"-" doc:"Hourly wage (1/173) × multiplied hours; for the employee and salary viewers"`
	Status          string              `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled"`
	DecisionNote    *string             `json:"decisionNote" db:"decision_note"`
	CreatedAt       time.Time           `json:"createdAt" db:"created_at"`
	Approvals       []TimeApprovalStep  `json:"approvals" db:"-"`
}

const overtimeSelect = `SELECT r.id, r.property_id, r.number, r.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, r.work_date,
	r.starts_at, r.ends_at, trim_scale(r.hours)::text AS hours, r.timing, r.reason, r.day_kind, r.work_week_days,
	trim_scale(r.actual_hours)::text AS actual_hours, trim_scale(r.payable_hours)::text AS payable_hours,
	trim_scale(r.multiplied_hours)::text AS multiplied_hours, r.tiers, r.status, r.decision_note, r.created_at
	FROM hris.overtime_requests r JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id`

// OvertimeRequestInput requests overtime.
type OvertimeRequestInput struct {
	EmployeeID *uuid.UUID `json:"employeeId,omitempty" doc:"HR only"`
	WorkDate   string     `json:"workDate"`
	StartTime  string     `json:"startTime" doc:"HH:MM local; an end before the start is on the next day"`
	EndTime    string     `json:"endTime" doc:"HH:MM local"`
	Reason     string     `json:"reason"`
}

// OvertimeException is overtime worked without (enough) approval (FR-OVT-04).
type OvertimeException struct {
	EmployeeID      uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo      string     `json:"employeeNo" db:"employee_no"`
	EmployeeName    string     `json:"employeeName" db:"employee_name"`
	OrgUnitName     *string    `json:"orgUnitName" db:"org_unit_name"`
	WorkDate        time.Time  `json:"workDate" db:"work_date"`
	ScheduledEnd    *time.Time `json:"scheduledEnd" db:"scheduled_end"`
	LastOut         *time.Time `json:"lastOut" db:"last_out"`
	OvertimeMinutes int        `json:"overtimeMinutes" db:"overtime_minutes"`
	ApprovedHours   string     `json:"approvedHours" db:"approved_hours"`
}

const exceptionSQL = `SELECT d.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, d.work_date, d.scheduled_end, d.last_out,
	d.overtime_minutes, trim_scale(coalesce((SELECT sum(hours) FROM hris.overtime_requests o WHERE o.employee_id = d.employee_id AND o.work_date = d.work_date
	  AND o.status = 'approved'), 0))::text AS approved_hours
	FROM hris.attendance_days d JOIN hris.employees e ON e.id = d.employee_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
	WHERE 'unapproved_overtime' = ANY (d.flags)`

func (m *Module) registerOvertime(reg *route.Registry) {
	tag := "HRIS Overtime"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/overtime-requests", Summary: "Overtime requests", Permission: PermOvertimeView,
		Response: OvertimeRequestView{}, List: true, Query: []route.Param{{Name: "status", Enum: []string{"submitted", "approved", "rejected", "cancelled"}},
			{Name: "from"}, {Name: "to"}, {Name: "employeeId"}}, Handler: listRead(m.DB, m.hrOvertimeHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/overtime-requests", Summary: "Request overtime for an employee",
		Permission: PermOvertimeCreate, Request: OvertimeRequestInput{}, Response: OvertimeRequestView{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createOvertimeHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/overtime-requests/{id}:approve", Summary: "Approve an overtime request",
		Permission: PermOvertimeApprove, Request: TimeDecision{}, Response: OvertimeRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideOvertime(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/overtime-requests/{id}:reject", Summary: "Reject an overtime request",
		Permission: PermOvertimeApprove, Request: TimeDecision{}, Response: OvertimeRequestView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrDecideOvertime(false))})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/overtime-exceptions", Summary: "Overtime worked without approval",
		Permission: PermOvertimeView, Response: OvertimeException{}, List: true, Query: []route.Param{{Name: "from"}, {Name: "to"}},
		Handler: listRead(m.DB, m.exceptionsHTTP)})
	ess := "Employee Self Service"
	essAdd := func(rt route.Route) {
		rt.Permission = hris.PermissionESS
		add(reg, ess, rt)
	}
	essAdd(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/overtime-requests", Summary: "My overtime", Response: OvertimeRequestView{}, List: true,
		Handler: listRead(m.DB, m.essOvertimeHTTP)})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/overtime-requests", Summary: "Request overtime", Request: OvertimeRequestInput{},
		Response: OvertimeRequestView{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createOvertimeHTTP(false))})
	essAdd(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/overtime-requests/{id}:cancel", Summary: "Withdraw my overtime request",
		Request: TimeDecision{}, Response: OvertimeRequestView{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essCancelOvertimeHTTP)})
}

// estimate fills the estimated pay of a request for the employee or a
// salary viewer.
func estimate(ctx context.Context, q pgx.Tx, r *OvertimeRequestView, allowed bool) error {
	if !allowed {
		return nil
	}
	c, err := hris.ContractAt(ctx, q, r.EmployeeID, r.WorkDate)
	if err != nil || c == nil {
		return err
	}
	p, _, err := hris.LoadOvertimePolicy(ctx, q, r.PropertyID, hris.PolicyTime(r.WorkDate))
	if err != nil {
		return err
	}
	mult := hris.Dec(r.MultipliedHours)
	if r.Status != hris.RequestApproved || mult.IsZero() {
		mult = hris.MultipliedOf(p.Tiers(hris.Dec(r.Hours), r.DayKind, r.WorkWeekDays))
	}
	pay := p.HourlyWage(c.FixedWage()).Mul(mult).Round(0).String()
	r.EstimatedPay = &pay
	return nil
}

func (m *Module) loadOvertime(ctx context.Context, q pgx.Tx, rid uuid.UUID) (OvertimeRequestView, error) {
	o, err := getOne[OvertimeRequestView]("overtime request")(q.Query(ctx, overtimeSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return o, err
	}
	if o.Tiers == nil {
		o.Tiers = []hris.OvertimeTier{}
	}
	o.Approvals, err = m.steps(ctx, q, KindOvertime, rid)
	if err != nil {
		return o, err
	}
	mine := meOptional(ctx, q)
	return o, estimate(ctx, q, &o, (mine != nil && mine.ID == o.EmployeeID) || can(ctx, PermSalary, o.PropertyID))
}

func (m *Module) hrOvertimeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]OvertimeRequestView, error) {
	property := handle.Property(ctx)
	from, err := handle.QueryDate(r, "from", today(ctx, tx, property).AddDate(0, -2, 0))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", today(ctx, tx, property).AddDate(0, 1, 0))
	if err != nil {
		return nil, err
	}
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	list, err := handle.List[OvertimeRequestView](tx.Query(ctx, overtimeSelect+` WHERE r.property_id = $1 AND r.work_date BETWEEN $2::date AND $3::date
		AND ($4 = '' OR r.status = $4) AND ($5::uuid IS NULL OR r.employee_id = $5) ORDER BY (r.status = 'submitted') DESC, r.work_date DESC LIMIT 1000`,
		property, ymd(from), ymd(to), r.URL.Query().Get("status"), emp))
	if err != nil {
		return nil, err
	}
	salary := can(ctx, PermSalary, property)
	for i := range list {
		list[i].Approvals = []TimeApprovalStep{}
		if list[i].Tiers == nil {
			list[i].Tiers = []hris.OvertimeTier{}
		}
		if err := estimate(ctx, tx, &list[i], salary); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (m *Module) essOvertimeHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]OvertimeRequestView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[OvertimeRequestView](tx.Query(ctx, overtimeSelect+` WHERE r.employee_id = $1 ORDER BY r.work_date DESC LIMIT 100`, e.ID))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Tiers == nil {
			list[i].Tiers = []hris.OvertimeTier{}
		}
		if list[i].Approvals, err = m.steps(ctx, tx, KindOvertime, list[i].ID); err != nil {
			return nil, err
		}
		if err := estimate(ctx, tx, &list[i], true); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// dayKindFor is the overtime day kind of an employee's date.
func dayKindFor(ctx context.Context, tx pgx.Tx, emp hris.Employee, day time.Time) (string, int, error) {
	ww, err := workWeekOf(ctx, tx, emp, day)
	if err != nil {
		return "", 0, err
	}
	a, err := loadAssignment(ctx, tx, emp.ID, day)
	if err != nil {
		return "", 0, err
	}
	hol, _, err := holidayOn(ctx, tx, emp.PropertyID, day)
	if err != nil {
		return "", 0, err
	}
	rest := (a != nil && a.Kind == "off") || (a == nil && !hris.IsWorkingWeekday(day.Weekday(), ww))
	return hris.DayKindOf(hol != "", rest, ww, day.Weekday()), ww, nil
}

func (m *Module) createOvertimeHTTP(viaHR bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req OvertimeRequestInput) (OvertimeRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req OvertimeRequestInput) (OvertimeRequestView, error) {
		var emp hris.Employee
		var err error
		if viaHR {
			if req.EmployeeID == nil {
				return OvertimeRequestView{}, handle.Invalid("employeeId", "required", "choose the employee")
			}
			emp, err = employeeAt(ctx, tx, handle.Property(ctx), *req.EmployeeID)
		} else {
			if req.EmployeeID != nil {
				return OvertimeRequestView{}, handle.Invalid("employeeId", "not_allowed", "you request your own overtime")
			}
			emp, err = me(ctx, tx)
		}
		if err != nil {
			return OvertimeRequestView{}, err
		}
		rid, err := m.createOvertime(ctx, tx, emp, req, viaHR)
		if err != nil {
			return OvertimeRequestView{}, err
		}
		return m.loadOvertime(ctx, tx, rid)
	}
}

func (m *Module) createOvertime(ctx context.Context, tx pgx.Tx, emp hris.Employee, req OvertimeRequestInput, viaHR bool) (uuid.UUID, error) {
	day, err := mustDate("workDate", req.WorkDate)
	if err != nil {
		return uuid.Nil, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return uuid.Nil, handle.Invalid("reason", "required", "give the reason of the overtime")
	}
	loc := location(ctx, tx, emp.PropertyID)
	sh, sm, err := parseClock("startTime", req.StartTime)
	if err != nil {
		return uuid.Nil, err
	}
	eh, em, err := parseClock("endTime", req.EndTime)
	if err != nil {
		return uuid.Nil, err
	}
	start, end := localAt(day, sh, sm, loc), localAt(day, eh, em, loc)
	if !end.After(start) {
		end = localAt(day.AddDate(0, 0, 1), eh, em, loc)
	}
	hours := decimal.NewFromInt(int64(end.Sub(start) / time.Minute)).Div(decimal.NewFromInt(60)).Round(2)
	p, ref, err := hris.LoadOvertimePolicy(ctx, tx, emp.PropertyID, hris.PolicyTime(day))
	if err != nil {
		return uuid.Nil, err
	}
	now := clock.Now()
	tday := today(ctx, tx, emp.PropertyID)
	timing := "before"
	if !start.After(now) {
		timing = "after"
		if !p.AllowAfterTheFact {
			return uuid.Nil, handle.Invalid("workDate", "before_only", "overtime is requested before the work (Overtime Policy)")
		}
		if p.AfterTheFactDays >= 0 && tday.Sub(day) > time.Duration(p.AfterTheFactDays)*24*time.Hour {
			return uuid.Nil, handle.Invalid("workDate", "too_late", fmt.Sprintf("overtime is requested at most %d days afterwards", p.AfterTheFactDays))
		}
	}
	if !emp.EmployedOn(day) {
		return uuid.Nil, handle.Invalid("workDate", "not_employed", "the employee is not employed on that day")
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
		return uuid.Nil, err
	}
	var dayTotal, weekTotal decimal.Decimal
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(hours) FILTER (WHERE work_date = $2::date), 0),
		coalesce(sum(hours) FILTER (WHERE date_trunc('week', work_date) = date_trunc('week', $2::date)), 0),
		coalesce(bool_or(tstzrange(starts_at, ends_at) && tstzrange($3, $4)), false)
		FROM hris.overtime_requests WHERE employee_id = $1 AND status IN ('submitted', 'approved')
		AND work_date BETWEEN $2::date - 7 AND $2::date + 7`, emp.ID, ymd(day), start, end).Scan(&dayTotal, &weekTotal, &overlap); err != nil {
		return uuid.Nil, err
	}
	if overlap {
		return uuid.Nil, errs.Conflict("overtime_overlap", "the time overlaps another overtime request")
	}
	if lim := hris.Dec(p.MaxHoursPerDay); lim.IsPositive() && dayTotal.Add(hours).GreaterThan(lim) {
		return uuid.Nil, handle.Invalid("endTime", "over_daily_limit", fmt.Sprintf("overtime is limited to %s hours a day (%s already requested)", p.MaxHoursPerDay,
			dayTotal.String()))
	}
	if lim := hris.Dec(p.MaxHoursPerWeek); lim.IsPositive() && weekTotal.Add(hours).GreaterThan(lim) {
		return uuid.Nil, handle.Invalid("endTime", "over_weekly_limit", fmt.Sprintf("overtime is limited to %s hours a week (%s already requested)",
			p.MaxHoursPerWeek, weekTotal.String()))
	}
	kind, ww, err := dayKindFor(ctx, tx, emp, day)
	if err != nil {
		return uuid.Nil, err
	}
	number, err := yearlyNumber(ctx, tx, emp.PropertyID, "OT", tday.Year())
	if err != nil {
		return uuid.Nil, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.overtime_requests (id, property_id, number, employee_id, work_date, starts_at, ends_at, hours, timing, reason,
		day_kind, work_week_days, policy_version, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`, rid, emp.PropertyID,
		number, emp.ID, ymd(day), start.UTC(), end.UTC(), hours, timing, strings.TrimSpace(req.Reason), kind, ww, ref.Version, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.overtime_request", EntityID: rid.String(),
		EntityLabel: number + " · " + emp.FullName, PropertyID: &emp.PropertyID, Reason: req.Reason,
		After: map[string]any{"workDate": ymd(day), "hours": hours.String(), "timing": timing, "dayKind": kind, "viaHR": viaHR}}); err != nil {
		return uuid.Nil, err
	}
	if !p.RequireApproval {
		return rid, m.finalize(ctx, tx, KindOvertime, rid, hris.RequestApproved, actor(ctx), "no approval required (Overtime Policy)")
	}
	summary, attrs, err := m.describe(ctx, tx, KindOvertime, rid)
	if err != nil {
		return uuid.Nil, err
	}
	return rid, m.startApproval(ctx, tx, KindOvertime, rid, emp, []string{"supervisor"}, summary, attrs)
}

func (m *Module) essCancelOvertimeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (OvertimeRequestView, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return OvertimeRequestView{}, err
	}
	rid, err := handle.ID(r)
	if err != nil {
		return OvertimeRequestView{}, err
	}
	h, err := loadHeader(ctx, tx, specs[KindOvertime], rid, true)
	if err != nil || h.EmployeeID != e.ID {
		return OvertimeRequestView{}, errs.NotFound("overtime request")
	}
	if err := m.withdraw(ctx, tx, KindOvertime, h, req.Note); err != nil {
		return OvertimeRequestView{}, err
	}
	out, err := m.loadOvertime(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.overtime_request", EntityID: rid.String(),
		EntityLabel: h.Number, PropertyID: &h.PropertyID, Reason: req.Note, Before: map[string]any{"status": h.Status},
		After: map[string]any{"status": out.Status}})
}

func (m *Module) hrDecideOvertime(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (OvertimeRequestView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req TimeDecision) (OvertimeRequestView, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return OvertimeRequestView{}, err
		}
		if err := m.decide(ctx, tx, KindOvertime, rid, approve, req.Note, true); err != nil {
			return OvertimeRequestView{}, err
		}
		out, err := m.loadOvertime(ctx, tx, rid)
		if err != nil {
			return out, err
		}
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.overtime_request", EntityID: rid.String(), EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &out.PropertyID,
			Reason: req.Note, After: map[string]any{"status": out.Status}})
	}
}

// applyOvertime computes the payable hours of approved overtime from the
// attendance of the day (0 until worked) and publishes
// hris.overtime_approved.
func (m *Module) applyOvertime(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	var eid uuid.UUID
	var day time.Time
	if err := tx.QueryRow(ctx, `SELECT employee_id, work_date FROM hris.overtime_requests WHERE id = $1`, rid).Scan(&eid, &day); err != nil {
		return err
	}
	emp, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return err
	}
	if err := checkLocked(ctx, tx, emp.PropertyID, day); err != nil {
		return err
	}
	tday := today(ctx, tx, emp.PropertyID)
	if !day.After(tday) {
		if _, err := m.recomputeDay(ctx, tx, emp, day, day.Before(tday), nil); err != nil {
			return err
		}
	} else {
		kind, ww, err := dayKindFor(ctx, tx, emp, day)
		if err != nil {
			return err
		}
		if _, err := m.updateDayOvertime(ctx, tx, emp, day, 0, kind, ww, newPolicies(emp.PropertyID)); err != nil {
			return err
		}
	}
	o, err := getOne[OvertimeRequestView]("overtime request")(tx.Query(ctx, overtimeSelect+` WHERE r.id = $1`, rid))
	if err != nil {
		return err
	}
	if o.Tiers == nil {
		o.Tiers = []hris.OvertimeTier{}
	}
	var version int
	if err := tx.QueryRow(ctx, `SELECT policy_version FROM hris.overtime_requests WHERE id = $1`, rid).Scan(&version); err != nil {
		return err
	}
	_, err = m.Events.Publish(ctx, tx, hris.EventOvertimeApproved, "hris.overtime_request", &rid, &emp.PropertyID, hris.OvertimeApproved{
		OvertimeRequestID: rid, Number: o.Number, PropertyID: emp.PropertyID, EmployeeID: emp.ID, EmployeeNo: emp.EmployeeNo, WorkDate: ymd(day),
		Hours: o.Hours, PayableHours: o.PayableHours, DayKind: o.DayKind, WorkWeekDays: o.WorkWeekDays, Tiers: o.Tiers, MultipliedHours: o.MultipliedHours,
		PolicyVersion: version})
	return err
}

func (m *Module) exceptionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]OvertimeException, error) {
	property := handle.Property(ctx)
	to, err := handle.QueryDate(r, "to", today(ctx, tx, property))
	if err != nil {
		return nil, err
	}
	from, err := handle.QueryDate(r, "from", to.AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	return handle.List[OvertimeException](tx.Query(ctx, exceptionSQL+` AND d.property_id = $1 AND d.work_date BETWEEN $2::date AND $3::date
		ORDER BY d.work_date DESC, e.full_name LIMIT 1000`, property, ymd(from), ymd(to)))
}
