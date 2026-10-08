package corehr

// Employee lifecycle (EP-01): profile view, employment history, transfer /
// rotation / promotion with effective date (FR-HR-05), onboarding account
// (FR-HR-04), offboarding with checklist and the H7 termination event
// (FR-HR-06), and the org chart (FR-HR-07).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/inventory"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

func authzPrincipal(ctx context.Context) *authz.Principal { return authz.From(ctx) }

// EmploymentChange is one employment history entry.
type EmploymentChange struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	EmployeeID     uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo     string     `json:"employeeNo" db:"employee_no"`
	EmployeeName   string     `json:"employeeName" db:"employee_name"`
	Kind           string     `json:"kind" db:"kind" enum:"hire,transfer,rotation,promotion,demotion,status_change,contract,termination,rehire,data_migration"`
	EffectiveDate  time.Time  `json:"effectiveDate" db:"effective_date"`
	FromOrgUnit    *string    `json:"fromOrgUnit" db:"from_org_unit"`
	ToOrgUnit      *string    `json:"toOrgUnit" db:"to_org_unit"`
	FromPosition   *string    `json:"fromPosition" db:"from_position"`
	ToPosition     *string    `json:"toPosition" db:"to_position"`
	FromGrade      *string    `json:"fromGrade" db:"from_grade"`
	ToGrade        *string    `json:"toGrade" db:"to_grade"`
	FromSupervisor *string    `json:"fromSupervisor" db:"from_supervisor"`
	ToSupervisor   *string    `json:"toSupervisor" db:"to_supervisor"`
	ToOrgUnitID    *uuid.UUID `json:"toOrgUnitId" db:"to_org_unit_id"`
	ToPositionID   *uuid.UUID `json:"toPositionId" db:"to_position_id"`
	ToGradeID      *uuid.UUID `json:"toGradeId" db:"to_grade_id"`
	ToSupervisorID *uuid.UUID `json:"toSupervisorId" db:"to_supervisor_id"`
	FromStatus     *string    `json:"fromStatus" db:"from_status"`
	ToStatus       *string    `json:"toStatus" db:"to_status"`
	Reference      *string    `json:"reference" db:"reference"`
	Reason         *string    `json:"reason" db:"reason"`
	Status         string     `json:"status" db:"status" enum:"scheduled,applied,cancelled"`
	AppliedAt      *time.Time `json:"appliedAt" db:"applied_at"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
	CreatedByName  *string    `json:"createdByName" db:"created_by_name"`
}

const changeSelect = `SELECT h.id, h.employee_id, e.employee_no, e.full_name AS employee_name, h.kind, h.effective_date,
	fo.name AS from_org_unit, tou.name AS to_org_unit, fp.name AS from_position, tp.name AS to_position, fg.code AS from_grade, tg.code AS to_grade,
	fs.full_name AS from_supervisor, ts.full_name AS to_supervisor, h.to_org_unit_id, h.to_position_id, h.to_grade_id, h.to_supervisor_id,
	h.from_status, h.to_status, h.reference, h.reason, h.status, h.applied_at, h.created_at, cu.full_name AS created_by_name
	FROM hris.employment_history h JOIN hris.employees e ON e.id = h.employee_id
	LEFT JOIN hris.org_units fo ON fo.id = h.from_org_unit_id LEFT JOIN hris.org_units tou ON tou.id = h.to_org_unit_id
	LEFT JOIN hris.positions fp ON fp.id = h.from_position_id LEFT JOIN hris.positions tp ON tp.id = h.to_position_id
	LEFT JOIN hris.grades fg ON fg.id = h.from_grade_id LEFT JOIN hris.grades tg ON tg.id = h.to_grade_id
	LEFT JOIN hris.employees fs ON fs.id = h.from_supervisor_id LEFT JOIN hris.employees ts ON ts.id = h.to_supervisor_id
	LEFT JOIN platform.users cu ON cu.id = h.created_by`

// EmployeeAccount is the login linked to an employee.
type EmployeeAccount struct {
	UserID      uuid.UUID  `json:"userId" db:"id"`
	Email       string     `json:"email" db:"email"`
	Status      string     `json:"status" db:"status"`
	Roles       []string   `json:"roles" db:"roles"`
	LastLoginAt *time.Time `json:"lastLoginAt" db:"last_login_at"`
}

// EmployeeDetail is the employee profile screen: the master data (masked
// by permission), placement names, account, contract in force, scheduled
// changes and counters.
type EmployeeDetail struct {
	Employee          map[string]any          `json:"employee"`
	OrgUnitName       *string                 `json:"orgUnitName"`
	PositionName      *string                 `json:"positionName"`
	GradeCode         *string                 `json:"gradeCode"`
	SupervisorName    *string                 `json:"supervisorName"`
	Account           *EmployeeAccount        `json:"account"`
	Contract          *ContractSummary        `json:"contract"`
	Scheduled         []EmploymentChange      `json:"scheduled"`
	Documents         int                     `json:"documents"`
	ExpiringDocuments int                     `json:"expiringDocuments"`
	Certifications    int                     `json:"certifications"`
	CertificationGaps []hris.CertificationGap `json:"certificationGaps"`
	Offboarding       *OffboardingStatus      `json:"offboarding"`
	ServiceMonths     int                     `json:"serviceMonths"`
	WorkStatus        string                  `json:"workStatus" enum:"active,draft,on_leave,suspended,leaving,terminated,inactive" doc:"Work status today (HRIS phase B)"`
	WorkStatusNote    *WorkStatus             `json:"workStatusNote" doc:"Since / until / note of a status other than active"`
}

// OffboardingStatus counts the checklist.
type OffboardingStatus struct {
	Total int `json:"total"`
	Done  int `json:"done"`
}

// ContractSummary is the contract in force on the profile.
type ContractSummary struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	Number       string     `json:"number" db:"number"`
	ContractType string     `json:"contractType" db:"contract_type"`
	StartDate    time.Time  `json:"startDate" db:"start_date"`
	EndDate      *time.Time `json:"endDate" db:"end_date"`
	Status       string     `json:"status" db:"status"`
	SequenceNo   int        `json:"sequenceNo" db:"sequence_no"`
}

// EmploymentChangeRequest transfers, rotates, promotes or demotes an
// employee from an effective date.
type EmploymentChangeRequest struct {
	Kind          string     `json:"kind,omitempty" enum:"transfer,rotation,promotion,demotion" doc:"Default: transfer (:transfer) or promotion (:promote)"`
	EffectiveDate string     `json:"effectiveDate" doc:"YYYY-MM-DD; a future date is scheduled and applied that day"`
	OrgUnitID     *uuid.UUID `json:"orgUnitId,omitempty"`
	PositionID    *uuid.UUID `json:"positionId,omitempty" doc:"The org unit follows the position"`
	GradeID       *uuid.UUID `json:"gradeId,omitempty" doc:"Default: the grade of the new position"`
	SupervisorID  *uuid.UUID `json:"supervisorId,omitempty"`
	Reason        string     `json:"reason"`
}

// TerminationRequest ends the employment (FR-HR-06).
type TerminationRequest struct {
	TerminationType string `json:"terminationType" enum:"resigned,terminated,contract_ended,retired,deceased"`
	EffectiveDate   string `json:"effectiveDate" doc:"First day the employee no longer works and cannot log in (YYYY-MM-DD)"`
	Reason          string `json:"reason"`
}

// ReasonRequest carries a reason.
type ReasonRequest struct {
	Reason string `json:"reason"`
}

// AccountCreateRequest creates or links the employee's login (FR-HR-04).
type AccountCreateRequest struct {
	Email     string     `json:"email,omitempty" doc:"Default: the work e-mail; an invitation is sent to set the password"`
	UserID    *uuid.UUID `json:"userId,omitempty" doc:"Link an existing user instead"`
	RoleCodes []string   `json:"roleCodes,omitempty" doc:"Default: the self-service role of the HR Configuration"`
	Locale    *string    `json:"locale,omitempty" enum:"en,id"`
}

// OffboardingItem is one checklist entry.
type OffboardingItem struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	EmployeeID uuid.UUID  `json:"employeeId" db:"employee_id"`
	Code       string     `json:"code" db:"code"`
	Label      string     `json:"label" db:"label"`
	Status     string     `json:"status" db:"status" enum:"pending,done,not_applicable"`
	DoneAt     *time.Time `json:"doneAt" db:"done_at"`
	DoneByName *string    `json:"doneByName" db:"done_by_name"`
	Notes      *string    `json:"notes" db:"notes"`
	// FR-HR-06: the assets still in the employee's custody (item return_assets)
	Assets []OffboardingAsset `json:"assets,omitempty" db:"-" doc:"Item return_assets: the assets of the Inventory asset register still in the employee's custody"`
}

// OffboardingReturnAssets is the checklist item of the assets to return.
const OffboardingReturnAssets = "return_assets"

// OffboardingAsset is an asset the leaver still holds (custodian on the
// asset register).
type OffboardingAsset struct {
	ID       uuid.UUID `json:"id"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	Category string    `json:"category"`
	Status   string    `json:"status"`
}

// custodyOf lists the assets in the custody of an employee.
func custodyOf(ctx context.Context, tx pgx.Tx, eid uuid.UUID) ([]OffboardingAsset, error) {
	list, err := inventory.AssetsInCustody(ctx, tx, eid)
	if err != nil {
		return nil, err
	}
	out := make([]OffboardingAsset, 0, len(list))
	for _, a := range list {
		out = append(out, OffboardingAsset{ID: a.ID, Code: a.Code, Name: a.Name, Category: a.Category, Status: a.Status})
	}
	return out, nil
}

// OffboardingUpdate ticks a checklist entry.
type OffboardingUpdate struct {
	Status string  `json:"status" enum:"pending,done,not_applicable"`
	Notes  *string `json:"notes,omitempty"`
}

// OrgChartNode is one unit of the org chart.
type OrgChartNode struct {
	ID         uuid.UUID        `json:"id"`
	Code       string           `json:"code"`
	Name       string           `json:"name"`
	UnitType   string           `json:"unitType"`
	CostCenter *string          `json:"costCenter"`
	HeadID     *uuid.UUID       `json:"headId"`
	HeadName   *string          `json:"headName"`
	HeadTitle  *string          `json:"headTitle"`
	Headcount  int              `json:"headcount" doc:"Active employees of the unit (without sub-units)"`
	Budgeted   int              `json:"budgeted" doc:"Budgeted headcount of the unit's positions"`
	Total      int              `json:"total" doc:"Active employees including sub-units"`
	Employees  []OrgChartPerson `json:"employees,omitempty"`
	Children   []*OrgChartNode  `json:"children"`
}

// OrgChartPerson is an employee on the org chart.
type OrgChartPerson struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	FullName     string     `json:"fullName" db:"full_name"`
	JobTitle     *string    `json:"jobTitle" db:"job_title"`
	OrgUnitID    *uuid.UUID `json:"orgUnitId" db:"org_unit_id"`
	SupervisorID *uuid.UUID `json:"supervisorId" db:"supervisor_id"`
}

func (m *Module) registerEmployees(reg *route.Registry) {
	tag := "HRIS Employees"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/profile", Summary: "Employee profile",
		Permission: "hris.employee.view", Response: EmployeeDetail{}, Handler: handle.Read(m.DB, m.profileHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/history", Summary: "Employment history of an employee",
		Permission: "hris.employee.view", Response: EmploymentChange{}, List: true, Handler: listRead(m.DB, m.historyHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employment-changes", Summary: "Employment changes (transfers, promotions, terminations)",
		Permission: "hris.employee.view", Response: EmploymentChange{}, List: true,
		Query: []route.Param{{Name: "status", Enum: []string{"scheduled", "applied", "cancelled"}}, {Name: "kind"}, {Name: "from", Description: "YYYY-MM-DD"},
			{Name: "to", Description: "YYYY-MM-DD"}},
		Handler: listRead(m.DB, m.changesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:transfer", Summary: "Transfer or rotate an employee (effective date)",
		Permission: "hris.employee.transfer", Request: EmploymentChangeRequest{}, Response: EmploymentChange{},
		Handler: handle.Write(m.DB, http.StatusCreated, m.changeHTTP("transfer"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:promote", Summary: "Promote or demote an employee (effective date)",
		Permission: "hris.employee.transfer", Request: EmploymentChangeRequest{}, Response: EmploymentChange{},
		Handler: handle.Write(m.DB, http.StatusCreated, m.changeHTTP("promotion"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employment-changes/{id}:cancel", Summary: "Cancel a scheduled employment change",
		Permission: "hris.employee.transfer", Request: ReasonRequest{}, Response: EmploymentChange{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelChangeHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:terminate", Summary: "Resign / terminate an employee (offboarding)",
		Permission: "hris.employee.terminate", Request: TerminationRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.terminateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:cancel-termination", Summary: "Withdraw a scheduled resignation / termination",
		Permission: "hris.employee.terminate", Request: ReasonRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelTerminationHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/employees/{id}:create-account", Summary: "Create or link the employee's login (onboarding)",
		Permission: "hris.employee.manage_account", Request: AccountCreateRequest{}, Response: EmployeeDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.createAccountHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/offboarding", Summary: "Offboarding checklist",
		Permission: "hris.offboarding.view", Response: OffboardingItem{}, List: true, Handler: listRead(m.DB, m.offboardingHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/offboarding-items/{id}", Summary: "Tick an offboarding checklist item",
		Permission: "hris.offboarding.manage", Request: OffboardingUpdate{}, Response: OffboardingItem{}, Handler: handle.Write(m.DB, http.StatusOK, m.offboardingItemHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/certification-check", Summary: "Mandatory certifications of an employee on a day",
		Permission: "hris.certification.view", Response: hris.CertificationCheck{},
		Query:   []route.Param{{Name: "date", Description: "YYYY-MM-DD (default today)"}, {Name: "role", Enum: hris.WorkforceRoles}},
		Handler: handle.Read(m.DB, m.certificationCheckHTTP)})
	add(reg, "HRIS Organization", route.Route{Method: http.MethodGet, Path: "/api/v1/hris/org-chart", Summary: "Org chart of the property",
		Permission: "hris.org_unit.view", Response: OrgChartNode{}, List: true,
		Query:   []route.Param{{Name: "employees", Type: "boolean", Description: "Include the employees of each unit"}},
		Handler: listRead(m.DB, m.orgChartHTTP)})
}

// employeeRow loads the resource row of an employee (masked by permission).
func (m *Module) employeeRow(ctx context.Context, tx pgx.Tx, eid uuid.UUID) (map[string]any, error) {
	d, _ := m.Engine.Def(KeyEmployee)
	row, err := m.Engine.Get(ctx, tx, d, eid, false)
	if err != nil {
		return nil, err
	}
	maskEmployee(ctx, row)
	return row, nil
}

// Profile builds the employee profile.
func (m *Module) Profile(ctx context.Context, tx pgx.Tx, eid uuid.UUID) (EmployeeDetail, error) {
	row, err := m.employeeRow(ctx, tx, eid)
	if err != nil {
		return EmployeeDetail{}, err
	}
	e, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return EmployeeDetail{}, err
	}
	out := EmployeeDetail{Employee: row, OrgUnitName: e.OrgUnitName, PositionName: e.PositionName, GradeCode: e.GradeCode,
		Scheduled: []EmploymentChange{}, CertificationGaps: []hris.CertificationGap{}}
	if e.SupervisorID != nil {
		var n string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM hris.employees WHERE id = $1`, *e.SupervisorID).Scan(&n); err == nil {
			out.SupervisorName = &n
		}
	}
	accs, err := handle.List[EmployeeAccount](tx.Query(ctx, `SELECT u.id, u.email::text AS email, u.status, u.last_login_at,
		coalesce((SELECT array_agg(r.code ORDER BY r.code) FROM platform.role_assignments ra JOIN platform.roles r ON r.id = ra.role_id
		  WHERE ra.user_id = u.id), '{}') AS roles FROM platform.users u WHERE u.employee_id = $1`, eid))
	if err != nil {
		return out, err
	}
	if len(accs) > 0 {
		out.Account = &accs[0]
	}
	cs, err := handle.List[ContractSummary](tx.Query(ctx, `SELECT id, number, contract_type, start_date, end_date, status, sequence_no FROM hris.contracts
		WHERE employee_id = $1 AND status IN ('active', 'expiring', 'draft') ORDER BY (status = 'draft'), start_date DESC LIMIT 1`, eid))
	if err != nil {
		return out, err
	}
	if len(cs) > 0 {
		out.Contract = &cs[0]
	}
	if out.Scheduled, err = handle.List[EmploymentChange](tx.Query(ctx, changeSelect+` WHERE h.employee_id = $1 AND h.status = 'scheduled'
		ORDER BY h.effective_date`, eid)); err != nil {
		return out, err
	}
	day := today(ctx, tx, e.PropertyID)
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE true), count(*) FILTER (WHERE expires_on IS NOT NULL AND expires_on <= $2::date + 30)
		FROM hris.employee_documents WHERE employee_id = $1 AND archived_at IS NULL AND status = 'active'`, eid, ymd(day)).
		Scan(&out.Documents, &out.ExpiringDocuments); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hris.certifications WHERE employee_id = $1 AND archived_at IS NULL AND status IN ('active', 'expired')`,
		eid).Scan(&out.Certifications); err != nil {
		return out, err
	}
	if e.Status == "active" {
		chk, err := hris.CheckEmployee(ctx, tx, eid, "", day)
		if err != nil {
			return out, err
		}
		out.CertificationGaps = chk.Gaps
	}
	var total, done int
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status <> 'pending') FROM hris.offboarding_items WHERE employee_id = $1`, eid).
		Scan(&total, &done); err != nil {
		return out, err
	}
	if total > 0 {
		out.Offboarding = &OffboardingStatus{Total: total, Done: done}
	}
	if e.JoinDate != nil {
		end := day
		if e.TerminationDate != nil && e.TerminationDate.Before(end) {
			end = *e.TerminationDate
		}
		out.ServiceMonths = hris.ServiceMonths(*e.JoinDate, end)
	}
	out.WorkStatus = "active"
	ws, err := WorkStatuses(ctx, tx, e.PropertyID, day)
	if err != nil {
		return out, err
	}
	for i := range ws {
		if ws[i].EmployeeID == eid {
			out.WorkStatus, out.WorkStatusNote = ws[i].WorkStatus, &ws[i]
		}
	}
	return out, nil
}

func (m *Module) profileHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (EmployeeDetail, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

// inProperty checks that the employee belongs to the active property.
func (m *Module) inProperty(ctx context.Context, q pgx.Tx, eid uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE id = $1 AND property_id = $2)`, eid, handle.Property(ctx)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errs.NotFound("employee")
	}
	return nil
}

func (m *Module) historyHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]EmploymentChange, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return nil, err
	}
	return handle.List[EmploymentChange](tx.Query(ctx, changeSelect+` WHERE h.employee_id = $1 ORDER BY h.effective_date DESC, h.created_at DESC`, eid))
}

func (m *Module) changesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]EmploymentChange, error) {
	q := r.URL.Query()
	from, err := handle.QueryDate(r, "from", time.Time{})
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return nil, err
	}
	return handle.List[EmploymentChange](tx.Query(ctx, changeSelect+` WHERE h.property_id = $1 AND ($2 = '' OR h.status = $2) AND ($3 = '' OR h.kind = $3)
		AND h.effective_date BETWEEN $4::date AND $5::date ORDER BY h.effective_date DESC, h.created_at DESC LIMIT 500`,
		handle.Property(ctx), q.Get("status"), q.Get("kind"), ymd(from), ymd(to)))
}

func (m *Module) loadChange(ctx context.Context, tx pgx.Tx, hid uuid.UUID) (EmploymentChange, error) {
	return getOne[EmploymentChange]("employment change")(tx.Query(ctx, changeSelect+` WHERE h.id = $1`, hid))
}

// changeHTTP records a transfer / rotation / promotion / demotion.
func (m *Module) changeHTTP(defaultKind string) func(ctx context.Context, tx pgx.Tx, r *http.Request, req EmploymentChangeRequest) (EmploymentChange, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req EmploymentChangeRequest) (EmploymentChange, error) {
		eid, err := handle.ID(r)
		if err != nil {
			return EmploymentChange{}, err
		}
		kind := req.Kind
		if kind == "" {
			kind = defaultKind
		}
		allowed := []string{"transfer", "rotation"}
		if defaultKind == "promotion" {
			allowed = []string{"promotion", "demotion"}
		}
		if !oneOf(allowed, kind) {
			return EmploymentChange{}, enumErr("kind", allowed)
		}
		hid, err := m.ChangeEmployment(ctx, tx, eid, kind, req)
		if err != nil {
			return EmploymentChange{}, err
		}
		return m.loadChange(ctx, tx, hid)
	}
}

// ChangeEmployment records an employment change and applies it when its
// effective date has come (FR-HR-05).
func (m *Module) ChangeEmployment(ctx context.Context, tx pgx.Tx, eid uuid.UUID, kind string, req EmploymentChangeRequest) (uuid.UUID, error) {
	if strings.TrimSpace(req.Reason) == "" {
		return uuid.Nil, handle.Invalid("reason", "required", "a reason is required")
	}
	eff, err := mustDate("effectiveDate", req.EffectiveDate)
	if err != nil {
		return uuid.Nil, err
	}
	var cur struct {
		property                     uuid.UUID
		unit, pos, grade, supervisor *uuid.UUID
		status, empStatus            string
		termination                  *string
	}
	if err := tx.QueryRow(ctx, `SELECT property_id, org_unit_id, position_id, grade_id, supervisor_id, status, employment_status, termination_status
		FROM hris.employees WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`, eid, handle.Property(ctx)).
		Scan(&cur.property, &cur.unit, &cur.pos, &cur.grade, &cur.supervisor, &cur.status, &cur.empStatus, &cur.termination); err != nil {
		return uuid.Nil, errs.NotFound("employee")
	}
	if cur.status != "active" || cur.empStatus == hris.StatusResigned || cur.empStatus == hris.StatusTerminated {
		return uuid.Nil, errs.Conflict("employee_inactive", "the employee no longer works here")
	}
	unit, pos, grade, sup := req.OrgUnitID, req.PositionID, req.GradeID, req.SupervisorID
	if pos != nil {
		var pUnit uuid.UUID
		var pGrade *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT org_unit_id, grade_id FROM hris.positions WHERE id = $1 AND property_id = $2 AND archived_at IS NULL AND status = 'active'`,
			*pos, cur.property).Scan(&pUnit, &pGrade); err != nil {
			return uuid.Nil, handle.Invalid("positionId", "not_found", "position not found")
		}
		if unit != nil && *unit != pUnit {
			return uuid.Nil, handle.Invalid("positionId", "invalid", "the position belongs to another org unit")
		}
		unit = &pUnit
		if grade == nil {
			grade = pGrade
		}
	}
	if unit != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.org_units WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`, *unit,
			cur.property).Scan(&ok); err != nil || !ok {
			return uuid.Nil, handle.Invalid("orgUnitId", "not_found", "org unit not found")
		}
	}
	if grade != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.grades WHERE id = $1 AND archived_at IS NULL)`, *grade).Scan(&ok); err != nil || !ok {
			return uuid.Nil, handle.Invalid("gradeId", "not_found", "grade not found")
		}
	}
	if sup != nil {
		if *sup == eid {
			return uuid.Nil, handle.Invalid("supervisorId", "invalid", "an employee cannot supervise themself")
		}
		lead, err := hris.IsManagerOf(ctx, tx, eid, *sup)
		if err != nil {
			return uuid.Nil, err
		}
		if lead {
			return uuid.Nil, handle.Invalid("supervisorId", "cycle", "the supervisor reports to this employee")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE id = $1 AND property_id = $2 AND status = 'active')`, *sup,
			cur.property).Scan(&ok); err != nil || !ok {
			return uuid.Nil, handle.Invalid("supervisorId", "not_found", "supervisor not found")
		}
	}
	same := func(a, b *uuid.UUID) bool { return a == nil || (b != nil && *a == *b) }
	if same(unit, cur.unit) && same(pos, cur.pos) && same(grade, cur.grade) && same(sup, cur.supervisor) {
		return uuid.Nil, handle.Invalid("positionId", "no_change", "choose a new org unit, position, grade or supervisor")
	}
	if (kind == "promotion" || kind == "demotion") && grade == nil && pos == nil {
		return uuid.Nil, handle.Invalid("gradeId", "required", "a promotion or demotion changes the position or the grade")
	}
	if cur.termination != nil && *cur.termination == "scheduled" {
		var leaves time.Time
		if err := tx.QueryRow(ctx, `SELECT termination_date FROM hris.employees WHERE id = $1`, eid).Scan(&leaves); err == nil && !eff.Before(leaves) {
			return uuid.Nil, handle.Invalid("effectiveDate", "invalid", "the employee leaves before this date")
		}
	}
	hid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_org_unit_id, to_org_unit_id,
		from_position_id, to_position_id, from_grade_id, to_grade_id, from_supervisor_id, to_supervisor_id, from_status, to_status, reason, status,
		created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,$15,'scheduled',$16,$16)`,
		hid, cur.property, eid, kind, ymd(eff), cur.unit, coalesceID(unit, cur.unit), cur.pos, coalesceID(pos, cur.pos), cur.grade,
		coalesceID(grade, cur.grade), cur.supervisor, coalesceID(sup, cur.supervisor), cur.empStatus, req.Reason, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	if !eff.After(today(ctx, tx, cur.property)) {
		if err := m.applyChange(ctx, tx, hid); err != nil {
			return uuid.Nil, err
		}
	}
	after, err := m.loadChange(ctx, tx, hid)
	if err != nil {
		return uuid.Nil, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: kind, EntityType: KeyEmployee, EntityID: eid.String(),
		EntityLabel: after.EmployeeNo + " · " + after.EmployeeName, PropertyID: &cur.property, Reason: req.Reason, After: after}); err != nil {
		return uuid.Nil, err
	}
	var user *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1 AND status = 'active'`, eid).Scan(&user)
	if user != nil {
		summary := strings.Join(nonEmpty(after.ToPosition, after.ToOrgUnit, after.ToGrade), " · ")
		if err := m.notifyUsers(ctx, tx, cur.property, []uuid.UUID{*user}, "hris.employment_changed", "/ops/ess/profile",
			map[string]any{"kind": kind, "effectiveDate": ymd(eff), "summary": summary}); err != nil {
			return uuid.Nil, err
		}
	}
	return hid, nil
}

func nonEmpty(ss ...*string) []string {
	var out []string
	for _, s := range ss {
		if s != nil && *s != "" {
			out = append(out, *s)
		}
	}
	return out
}

func coalesceID(a, b *uuid.UUID) *uuid.UUID {
	if a != nil {
		return a
	}
	return b
}

// applyChange applies a scheduled change to the employee.
func (m *Module) applyChange(ctx context.Context, tx pgx.Tx, hid uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE hris.employees e SET org_unit_id = coalesce(h.to_org_unit_id, e.org_unit_id),
		position_id = coalesce(h.to_position_id, e.position_id), grade_id = coalesce(h.to_grade_id, e.grade_id),
		supervisor_id = CASE WHEN h.to_supervisor_id IS NOT NULL THEN h.to_supervisor_id ELSE e.supervisor_id END,
		job_title = CASE WHEN h.to_position_id IS DISTINCT FROM h.from_position_id AND h.to_position_id IS NOT NULL
		  THEN (SELECT name FROM hris.positions WHERE id = h.to_position_id) ELSE e.job_title END,
		updated_by = h.created_by
		FROM hris.employment_history h WHERE h.id = $1 AND h.status = 'scheduled' AND e.id = h.employee_id`, hid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE hris.employment_history SET status = 'applied', applied_at = now() WHERE id = $1`, hid)
	return err
}

func (m *Module) cancelChangeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReasonRequest) (EmploymentChange, error) {
	hid, err := handle.ID(r)
	if err != nil {
		return EmploymentChange{}, err
	}
	before, err := m.loadChange(ctx, tx, hid)
	if err != nil {
		return EmploymentChange{}, err
	}
	if before.Status != "scheduled" || before.Kind == "termination" {
		return EmploymentChange{}, errs.Conflict("change_not_scheduled", "only a scheduled transfer or promotion can be cancelled")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return EmploymentChange{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employment_history SET status = 'cancelled', reason = reason || ' — cancelled: ' || $2, updated_by = $3
		WHERE id = $1 AND property_id = $4`, hid, req.Reason, actor(ctx), handle.Property(ctx)); err != nil {
		return EmploymentChange{}, err
	}
	after, err := m.loadChange(ctx, tx, hid)
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: "hris.employment_change",
		EntityID: hid.String(), EntityLabel: before.EmployeeNo + " · " + before.Kind, PropertyID: &pid, Reason: req.Reason,
		Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status}})
}

// ── termination & offboarding (FR-HR-06, H7) ─────────────────────────────

var terminationTypes = []string{"resigned", "terminated", "contract_ended", "retired", "deceased"}

func (m *Module) terminateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req TerminationRequest) (EmployeeDetail, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if err := m.Terminate(ctx, tx, eid, req); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

// Terminate records a resignation / termination; it takes effect at once
// when the effective date has come, otherwise on that date (daily job).
func (m *Module) Terminate(ctx context.Context, tx pgx.Tx, eid uuid.UUID, req TerminationRequest) error {
	if !oneOf(terminationTypes, req.TerminationType) {
		return enumErr("terminationType", terminationTypes)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return handle.Invalid("reason", "required", "a reason is required")
	}
	eff, err := mustDate("effectiveDate", req.EffectiveDate)
	if err != nil {
		return err
	}
	var property uuid.UUID
	var no, name, status, empStatus string
	var termination *string
	if err := tx.QueryRow(ctx, `SELECT property_id, employee_no, full_name, status, employment_status, termination_status FROM hris.employees
		WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`, eid, handle.Property(ctx)).
		Scan(&property, &no, &name, &status, &empStatus, &termination); err != nil {
		return errs.NotFound("employee")
	}
	if status != "active" || empStatus == hris.StatusResigned || empStatus == hris.StatusTerminated {
		return errs.Conflict("employee_inactive", "the employee no longer works here")
	}
	if termination != nil && *termination == "scheduled" {
		return errs.Conflict("termination_scheduled", "a resignation / termination is already scheduled; withdraw it first")
	}
	var join *time.Time
	_ = tx.QueryRow(ctx, `SELECT join_date FROM hris.employees WHERE id = $1`, eid).Scan(&join)
	if join != nil && !eff.After(*join) {
		return handle.Invalid("effectiveDate", "invalid", "must be after the join date")
	}
	to := hris.StatusTerminated
	if req.TerminationType == "resigned" {
		to = hris.StatusResigned
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET termination_date = $2::date, termination_type = $3, termination_reason = $4,
		termination_status = 'scheduled', updated_by = $5 WHERE id = $1`, eid, ymd(eff), req.TerminationType, req.Reason, actor(ctx)); err != nil {
		return err
	}
	hid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_org_unit_id, from_position_id,
		from_grade_id, from_supervisor_id, from_status, to_status, reason, status, created_by, updated_by)
		SELECT $1, property_id, id, 'termination', $2::date, org_unit_id, position_id, grade_id, supervisor_id, employment_status, $3, $4, 'scheduled', $5, $5
		FROM hris.employees WHERE id = $6`, hid, ymd(eff), to, req.TerminationType+": "+req.Reason, actor(ctx), eid); err != nil {
		return err
	}
	if err := m.createChecklist(ctx, tx, property, eid, eff); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "terminate", EntityType: KeyEmployee, EntityID: eid.String(),
		EntityLabel: no + " · " + name, PropertyID: &property, Reason: req.Reason,
		After: map[string]any{"terminationType": req.TerminationType, "effectiveDate": ymd(eff), "employmentStatus": to}}); err != nil {
		return err
	}
	if !eff.After(today(ctx, tx, property)) {
		return m.completeTermination(ctx, tx, eid)
	}
	return nil
}

// createChecklist creates the offboarding checklist of the HR Configuration.
func (m *Module) createChecklist(ctx context.Context, tx pgx.Tx, property, eid uuid.UUID, at time.Time) error {
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	for i, it := range cfg.OffboardingChecklist {
		if _, err := tx.Exec(ctx, `INSERT INTO hris.offboarding_items (id, property_id, employee_id, code, label, sort_order) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (employee_id, code) DO UPDATE SET status = 'pending', done_at = NULL, done_by = NULL`,
			id.New(), property, eid, it.Code, it.Label, i); err != nil {
			return err
		}
	}
	return nil
}

// completeTermination makes a due termination effective: the employee
// becomes Resigned / Terminated and inactive, the contract ends, direct
// reports move to the leaver's supervisor, the unit loses its head and
// hris.employee_terminated (H7) asks IAM to deactivate the login.
func (m *Module) completeTermination(ctx context.Context, tx pgx.Tx, eid uuid.UUID) error {
	var e struct {
		property                uuid.UUID
		no, name, ttype, reason string
		eff                     time.Time
		supervisor              *uuid.UUID
	}
	if err := tx.QueryRow(ctx, `SELECT property_id, employee_no, full_name, termination_type, coalesce(termination_reason, ''), termination_date, supervisor_id
		FROM hris.employees WHERE id = $1 AND termination_status = 'scheduled' FOR UPDATE`, eid).
		Scan(&e.property, &e.no, &e.name, &e.ttype, &e.reason, &e.eff, &e.supervisor); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	to := hris.StatusTerminated
	if e.ttype == "resigned" {
		to = hris.StatusResigned
	}
	sup, err := hris.Supervisor(ctx, tx, eid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET employment_status = $2, status = 'inactive', termination_status = 'completed' WHERE id = $1`,
		eid, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'ended', ended_on = $2::date - 1, end_reason = coalesce(end_reason, $3)
		WHERE employee_id = $1 AND status IN ('active', 'expiring')`, eid, ymd(e.eff), e.ttype); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'cancelled' WHERE employee_id = $1 AND status = 'draft'`, eid); err != nil {
		return err
	}
	var supID *uuid.UUID
	if sup != nil {
		supID = &sup.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET supervisor_id = $2 WHERE supervisor_id = $1`, eid, supID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.org_units SET head_employee_id = NULL WHERE head_employee_id = $1`, eid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employment_history SET status = 'applied', applied_at = now() WHERE employee_id = $1 AND kind = 'termination'
		AND status = 'scheduled'`, eid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employment_history SET status = 'cancelled', reason = coalesce(reason, '') || ' — employee left'
		WHERE employee_id = $1 AND status = 'scheduled'`, eid); err != nil {
		return err
	}
	var user *uuid.UUID
	_ = tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1`, eid).Scan(&user)
	payload := hris.EmployeeTerminated{EmployeeID: eid, EmployeeNo: e.no, FullName: e.name, PropertyID: e.property, UserID: user,
		EffectiveDate: ymd(e.eff), TerminationType: e.ttype, EmploymentStatus: to, Reason: e.reason, SupervisorID: supID}
	supName := "-"
	if sup != nil {
		payload.SupervisorUserID = sup.UserID
		supName = sup.FullName
	}
	if _, err := m.Events.Publish(ctx, tx, hris.EventEmployeeTerminated, "hris.employee", &eid, &e.property, payload); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.offboarding_items SET status = 'done', done_at = now(), notes = 'Login deactivated (H7)'
		WHERE employee_id = $1 AND code = 'revoke_access' AND status = 'pending'`, eid); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: KeyEmployee, EntityID: eid.String(),
		EntityLabel: e.no + " · " + e.name, PropertyID: &e.property, Reason: e.reason,
		Before: map[string]any{"status": "active"}, After: map[string]any{"status": "inactive", "employmentStatus": to}}); err != nil {
		return err
	}
	users := hrUsers(ctx, tx, e.property, "hris.offboarding.manage")
	if sup != nil && sup.UserID != nil {
		users = append(users, *sup.UserID)
	}
	return m.notifyUsers(ctx, tx, e.property, users, "hris.employee_terminated", "/hris/employees/"+eid.String(),
		map[string]any{"employeeName": e.name, "employeeNo": e.no, "effectiveDate": ymd(e.eff), "terminationType": e.ttype, "supervisorName": supName})
}

func (m *Module) cancelTerminationHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReasonRequest) (EmployeeDetail, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return EmployeeDetail{}, handle.Invalid("reason", "required", "a reason is required")
	}
	var property uuid.UUID
	var no, name string
	var termination *string
	if err := tx.QueryRow(ctx, `SELECT property_id, employee_no, full_name, termination_status FROM hris.employees WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		eid, handle.Property(ctx)).Scan(&property, &no, &name, &termination); err != nil {
		return EmployeeDetail{}, errs.NotFound("employee")
	}
	if termination == nil || *termination != "scheduled" {
		return EmployeeDetail{}, errs.Conflict("termination_not_scheduled", "no scheduled resignation / termination to withdraw")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET termination_date = NULL, termination_type = NULL, termination_reason = NULL,
		termination_status = NULL, updated_by = $2 WHERE id = $1`, eid, actor(ctx)); err != nil {
		return EmployeeDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employment_history SET status = 'cancelled', reason = coalesce(reason, '') || ' — withdrawn: ' || $2
		WHERE employee_id = $1 AND kind = 'termination' AND status = 'scheduled'`, eid, req.Reason); err != nil {
		return EmployeeDetail{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hris.offboarding_items WHERE employee_id = $1`, eid); err != nil {
		return EmployeeDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel_termination", EntityType: KeyEmployee, EntityID: eid.String(),
		EntityLabel: no + " · " + name, PropertyID: &property, Reason: req.Reason}); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

func (m *Module) offboardingHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]OffboardingItem, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return nil, err
	}
	items, err := handle.List[OffboardingItem](tx.Query(ctx, offboardingSelect+` WHERE i.employee_id = $1 ORDER BY i.sort_order, i.code`, eid))
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Code == OffboardingReturnAssets {
			if items[i].Assets, err = custodyOf(ctx, tx, eid); err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}

const offboardingSelect = `SELECT i.id, i.employee_id, i.code, i.label, i.status, i.done_at, u.full_name AS done_by_name, i.notes
	FROM hris.offboarding_items i LEFT JOIN platform.users u ON u.id = i.done_by`

func (m *Module) offboardingItemHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OffboardingUpdate) (OffboardingItem, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return OffboardingItem{}, err
	}
	if !oneOf([]string{"pending", "done", "not_applicable"}, req.Status) {
		return OffboardingItem{}, enumErr("status", []string{"pending", "done", "not_applicable"})
	}
	before, err := getOne[OffboardingItem]("offboarding item")(tx.Query(ctx, offboardingSelect+` WHERE i.id = $1 AND i.property_id = $2 FOR UPDATE OF i`, iid,
		handle.Property(ctx)))
	if err != nil {
		return before, err
	}
	if before.Code == OffboardingReturnAssets && req.Status == "done" {
		held, err := custodyOf(ctx, tx, before.EmployeeID)
		if err != nil {
			return before, err
		}
		if len(held) > 0 {
			codes := make([]string, len(held))
			for i, a := range held {
				codes[i] = a.Code
			}
			return before, errs.Conflict("assets_in_custody", "the employee still holds "+strings.Join(codes, ", ")+
				": record the return on the asset register (clear the custodian) first")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.offboarding_items SET status = $2, notes = coalesce($3, notes),
		done_at = CASE WHEN $2 = 'pending' THEN NULL ELSE now() END, done_by = CASE WHEN $2 = 'pending' THEN NULL ELSE $4::uuid END WHERE id = $1`,
		iid, req.Status, req.Notes, actor(ctx)); err != nil {
		return before, err
	}
	after, err := getOne[OffboardingItem]("offboarding item")(tx.Query(ctx, offboardingSelect+` WHERE i.id = $1`, iid))
	if err != nil {
		return after, err
	}
	pid := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.offboarding_item",
		EntityID: iid.String(), EntityLabel: after.Label, PropertyID: &pid, Before: before, After: after})
}

// ── onboarding account (FR-HR-04) ────────────────────────────────────────

func (m *Module) createAccountHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req AccountCreateRequest) (EmployeeDetail, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if m.Accounts == nil {
		return EmployeeDetail{}, errs.Unavailable("account provisioning is not wired")
	}
	e, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return EmployeeDetail{}, err
	}
	if e.PropertyID != handle.Property(ctx) {
		return EmployeeDetail{}, errs.NotFound("employee")
	}
	if !e.EmployedOn(today(ctx, tx, e.PropertyID)) && (e.JoinDate == nil || !e.JoinDate.After(today(ctx, tx, e.PropertyID))) {
		return EmployeeDetail{}, errs.Conflict("employee_inactive", "the employee no longer works here")
	}
	if e.UserID != nil {
		return EmployeeDetail{}, errs.Conflict("account_exists", "the employee already has a login")
	}
	roles := req.RoleCodes
	if len(roles) == 0 {
		cfg, _, err := hris.LoadHRConfiguration(ctx, tx, e.PropertyID, clock.Now())
		if err != nil {
			return EmployeeDetail{}, err
		}
		roles = []string{cfg.SelfServiceRole}
	}
	email := strings.TrimSpace(req.Email)
	if email == "" && e.Email != nil {
		email = *e.Email
	}
	if req.UserID == nil && email == "" {
		return EmployeeDetail{}, handle.Invalid("email", "required", "an e-mail is required (the employee has no work e-mail)")
	}
	uid, err := m.Accounts.ProvisionEmployeeUser(ctx, tx, AccountRequest{EmployeeID: eid, PropertyID: e.PropertyID, FullName: e.FullName, Email: email,
		Phone: e.Phone, Locale: req.Locale, RoleCodes: roles, ExistingUserID: req.UserID})
	if err != nil {
		return EmployeeDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "create_account", EntityType: KeyEmployee, EntityID: eid.String(),
		EntityLabel: e.EmployeeNo + " · " + e.FullName, PropertyID: &e.PropertyID,
		After: map[string]any{"userId": uid, "roles": roles, "linkedExisting": req.UserID != nil}}); err != nil {
		return EmployeeDetail{}, err
	}
	return m.Profile(ctx, tx, eid)
}

func (m *Module) certificationCheckHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (hris.CertificationCheck, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return hris.CertificationCheck{}, err
	}
	if err := m.inProperty(ctx, tx, eid); err != nil {
		return hris.CertificationCheck{}, err
	}
	day, err := handle.QueryDate(r, "date", today(ctx, tx, handle.Property(ctx)))
	if err != nil {
		return hris.CertificationCheck{}, err
	}
	role := r.URL.Query().Get("role")
	if role != "" && !oneOf(hris.WorkforceRoles, role) {
		return hris.CertificationCheck{}, errs.BadRequest("invalid_role", "unknown workforce role")
	}
	return hris.CheckEmployee(ctx, tx, eid, role, day)
}

// ── org chart (FR-HR-07) ──────────────────────────────────────────────────

func (m *Module) orgChartHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]*OrgChartNode, error) {
	return m.OrgChart(ctx, tx, handle.Property(ctx), r.URL.Query().Get("employees") == "true")
}

// OrgChart builds the unit tree of a property with heads and headcounts.
func (m *Module) OrgChart(ctx context.Context, tx pgx.Tx, property uuid.UUID, withPeople bool) ([]*OrgChartNode, error) {
	day := ymd(today(ctx, tx, property))
	rows, err := tx.Query(ctx, `SELECT u.id, u.code, u.name, u.unit_type, u.cost_center, u.parent_id, u.head_employee_id, h.full_name,
		coalesce(h.job_title, hp.name),
		(SELECT count(*) FROM hris.employees e WHERE e.org_unit_id = u.id AND e.status = 'active' AND e.archived_at IS NULL
		   AND (e.join_date IS NULL OR e.join_date <= $2::date))::int,
		coalesce((SELECT sum(p.headcount) FROM hris.positions p WHERE p.org_unit_id = u.id AND p.archived_at IS NULL AND p.status = 'active'), 0)::int
		FROM hris.org_units u LEFT JOIN hris.employees h ON h.id = u.head_employee_id LEFT JOIN hris.positions hp ON hp.id = h.position_id
		WHERE u.property_id = $1 AND u.archived_at IS NULL AND u.status = 'active' ORDER BY u.sort_order, u.name`, property, day)
	if err != nil {
		return nil, err
	}
	nodes := map[uuid.UUID]*OrgChartNode{}
	parents := map[uuid.UUID]*uuid.UUID{}
	var order []uuid.UUID
	for rows.Next() {
		n := &OrgChartNode{Children: []*OrgChartNode{}}
		var parent *uuid.UUID
		if err := rows.Scan(&n.ID, &n.Code, &n.Name, &n.UnitType, &n.CostCenter, &parent, &n.HeadID, &n.HeadName, &n.HeadTitle, &n.Headcount,
			&n.Budgeted); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[n.ID], parents[n.ID] = n, parent
		order = append(order, n.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if withPeople {
		people, err := handle.List[OrgChartPerson](tx.Query(ctx, `SELECT id, full_name, job_title, org_unit_id, supervisor_id FROM hris.employees
			WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND (join_date IS NULL OR join_date <= $2::date)
			ORDER BY full_name`, property, day))
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			if p.OrgUnitID != nil && nodes[*p.OrgUnitID] != nil {
				nodes[*p.OrgUnitID].Employees = append(nodes[*p.OrgUnitID].Employees, p)
			}
		}
	}
	roots := []*OrgChartNode{}
	for _, id := range order {
		n := nodes[id]
		if p := parents[id]; p != nil && nodes[*p] != nil {
			nodes[*p].Children = append(nodes[*p].Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	var total func(n *OrgChartNode) int
	total = func(n *OrgChartNode) int {
		t := n.Headcount
		for _, c := range n.Children {
			t += total(c)
		}
		n.Total = t
		return t
	}
	for _, r := range roots {
		total(r)
	}
	return roots, nil
}
