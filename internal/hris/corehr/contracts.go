package corehr

// Employment contracts PKWT / PKWTT (EP-02 FR-CTR-01–03): draft, activate,
// renew (PKWT), make permanent (PKWT → PKWTT), end; PKWT limits and the
// probation rule of the HR Configuration (PRD P5 §16 #3). Salaries are only
// shown to hris.contract.view_salary (FR-HR-03).

import (
	"context"
	"encoding/json"
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

// ContractView is an employment contract.
type ContractView struct {
	ID                 uuid.UUID        `json:"id" db:"id"`
	PropertyID         uuid.UUID        `json:"propertyId" db:"property_id"`
	Number             string           `json:"number" db:"number"`
	EmployeeID         uuid.UUID        `json:"employeeId" db:"employee_id"`
	EmployeeNo         string           `json:"employeeNo" db:"employee_no"`
	EmployeeName       string           `json:"employeeName" db:"employee_name"`
	ContractType       string           `json:"contractType" db:"contract_type" enum:"pkwt,pkwtt"`
	SequenceNo         int              `json:"sequenceNo" db:"sequence_no"`
	PreviousContractID *uuid.UUID       `json:"previousContractId" db:"previous_contract_id"`
	StartDate          time.Time        `json:"startDate" db:"start_date"`
	EndDate            *time.Time       `json:"endDate" db:"end_date"`
	ProbationMonths    int              `json:"probationMonths" db:"probation_months"`
	ProbationEndDate   *time.Time       `json:"probationEndDate" db:"probation_end_date"`
	OrgUnitID          *uuid.UUID       `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName        *string          `json:"orgUnitName" db:"org_unit_name"`
	PositionID         *uuid.UUID       `json:"positionId" db:"position_id"`
	PositionName       *string          `json:"positionName" db:"position_name"`
	GradeID            *uuid.UUID       `json:"gradeId" db:"grade_id"`
	GradeCode          *string          `json:"gradeCode" db:"grade_code"`
	JobTitle           *string          `json:"jobTitle" db:"job_title"`
	Currency           string           `json:"currency" db:"currency"`
	BaseSalary         *string          `json:"baseSalary" db:"base_salary" doc:"null without hris.contract.view_salary"`
	Allowances         []hris.Allowance `json:"allowances" db:"allowances" doc:"empty without hris.contract.view_salary"`
	WorkWeekDays       int              `json:"workWeekDays" db:"work_week_days"`
	Status             string           `json:"status" db:"status" enum:"draft,active,expiring,renewed,ended,cancelled"`
	ActivatedAt        *time.Time       `json:"activatedAt" db:"activated_at"`
	EndedOn            *time.Time       `json:"endedOn" db:"ended_on"`
	EndReason          *string          `json:"endReason" db:"end_reason"`
	SupersededByID     *uuid.UUID       `json:"supersededById" db:"superseded_by_id"`
	SignedFileID       *uuid.UUID       `json:"signedFileId" db:"signed_file_id"`
	Notes              *string          `json:"notes" db:"notes"`
	DaysRemaining      *int             `json:"daysRemaining" db:"days_remaining"`
	PolicyVersion      int              `json:"policyVersion" db:"policy_version" doc:"HR Configuration version the contract was activated under"`
	CreatedAt          time.Time        `json:"createdAt" db:"created_at"`
	UpdatedAt          time.Time        `json:"updatedAt" db:"updated_at"`
}

const contractSelect = `SELECT c.id, c.property_id, c.number, c.employee_id, e.employee_no, e.full_name AS employee_name, c.contract_type, c.sequence_no,
	c.previous_contract_id, c.start_date, c.end_date, c.probation_months, c.probation_end_date, c.org_unit_id, ou.name AS org_unit_name, c.position_id,
	p.name AS position_name, c.grade_id, g.code AS grade_code, c.job_title, c.currency, trim_scale(c.base_salary)::text AS base_salary, c.allowances,
	c.work_week_days, c.status, c.activated_at, c.ended_on, c.end_reason, c.superseded_by_id, c.signed_file_id, c.notes,
	CASE WHEN c.end_date IS NOT NULL AND c.status IN ('active', 'expiring') THEN (c.end_date - $1::date) END AS days_remaining, c.policy_version,
	c.created_at, c.updated_at
	FROM hris.contracts c JOIN hris.employees e ON e.id = c.employee_id LEFT JOIN hris.org_units ou ON ou.id = c.org_unit_id
	LEFT JOIN hris.positions p ON p.id = c.position_id LEFT JOIN hris.grades g ON g.id = c.grade_id`

// ContractRequest creates a contract (draft unless activate).
type ContractRequest struct {
	EmployeeID      uuid.UUID        `json:"employeeId"`
	ContractType    string           `json:"contractType" enum:"pkwt,pkwtt"`
	StartDate       string           `json:"startDate"`
	EndDate         string           `json:"endDate,omitempty" doc:"Required for PKWT"`
	ProbationMonths int              `json:"probationMonths,omitempty" doc:"PKWTT only, at most the HR Configuration maximum"`
	OrgUnitID       *uuid.UUID       `json:"orgUnitId,omitempty" doc:"Default: the employee's"`
	PositionID      *uuid.UUID       `json:"positionId,omitempty" doc:"Default: the employee's"`
	GradeID         *uuid.UUID       `json:"gradeId,omitempty" doc:"Default: the employee's"`
	JobTitle        string           `json:"jobTitle,omitempty"`
	BaseSalary      string           `json:"baseSalary"`
	Allowances      []hris.Allowance `json:"allowances,omitempty"`
	WorkWeekDays    int              `json:"workWeekDays,omitempty" enum:"5,6"`
	SignedFileID    *uuid.UUID       `json:"signedFileId,omitempty"`
	Notes           string           `json:"notes,omitempty"`
	Activate        bool             `json:"activate,omitempty" doc:"Activate at once"`
}

// ContractUpdate edits a draft contract.
type ContractUpdate struct {
	StartDate       *string          `json:"startDate,omitempty"`
	EndDate         *string          `json:"endDate,omitempty"`
	ProbationMonths *int             `json:"probationMonths,omitempty"`
	PositionID      *uuid.UUID       `json:"positionId,omitempty"`
	GradeID         *uuid.UUID       `json:"gradeId,omitempty"`
	JobTitle        *string          `json:"jobTitle,omitempty"`
	BaseSalary      *string          `json:"baseSalary,omitempty"`
	Allowances      []hris.Allowance `json:"allowances,omitempty"`
	WorkWeekDays    *int             `json:"workWeekDays,omitempty"`
	SignedFileID    *uuid.UUID       `json:"signedFileId,omitempty" doc:"Also accepted on active contracts (signed scan)"`
	Notes           *string          `json:"notes,omitempty"`
}

// ContractRenewRequest renews a PKWT or converts it to PKWTT.
type ContractRenewRequest struct {
	StartDate  string           `json:"startDate,omitempty" doc:"Default: the day after the current contract ends"`
	EndDate    string           `json:"endDate,omitempty" doc:"New PKWT end date (renew)"`
	BaseSalary string           `json:"baseSalary,omitempty" doc:"Default: unchanged"`
	Allowances []hris.Allowance `json:"allowances,omitempty" doc:"Default: unchanged"`
	PositionID *uuid.UUID       `json:"positionId,omitempty"`
	GradeID    *uuid.UUID       `json:"gradeId,omitempty"`
	Notes      string           `json:"notes,omitempty"`
}

// EndContractRequest ends a contract early.
type EndContractRequest struct {
	EndDate string `json:"endDate" doc:"Last day of the contract"`
	Reason  string `json:"reason"`
}

func (m *Module) registerContracts(reg *route.Registry) {
	tag := "HRIS Contracts"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/contracts", Summary: "Employment contracts", Permission: "hris.contract.view",
		Response: ContractView{}, List: true, Query: []route.Param{{Name: "employeeId"}, {Name: "status"}, {Name: "contractType", Enum: []string{"pkwt", "pkwtt"}},
			{Name: "expiringWithin", Type: "integer", Description: "PKWT ending within N days"}},
		Handler: listRead(m.DB, m.listContractsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/contracts/{id}", Summary: "Employment contract", Permission: "hris.contract.view",
		Response: ContractView{}, Handler: handle.Read(m.DB, m.getContractHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts", Summary: "Create an employment contract (PKWT / PKWTT)",
		Permission: "hris.contract.create", Request: ContractRequest{}, Response: ContractView{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createContractHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/contracts/{id}", Summary: "Edit a draft contract",
		Permission: "hris.contract.update", Request: ContractUpdate{}, Response: ContractView{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateContractHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts/{id}:activate", Summary: "Activate a draft contract",
		Permission: "hris.contract.activate", Request: handle.Empty{}, Response: ContractView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.activateContractHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts/{id}:renew", Summary: "Renew a PKWT (new contract)",
		Permission: "hris.contract.renew", Request: ContractRenewRequest{}, Response: ContractView{}, Handler: handle.Write(m.DB, http.StatusCreated, m.renewHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts/{id}:make-permanent", Summary: "Make the employee permanent (PKWT → PKWTT)",
		Permission: "hris.contract.renew", Request: ContractRenewRequest{}, Response: ContractView{}, Handler: handle.Write(m.DB, http.StatusCreated, m.renewHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts/{id}:end", Summary: "End a contract early", Permission: "hris.contract.end",
		Request: EndContractRequest{}, Response: ContractView{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.endContractHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/contracts/{id}:cancel", Summary: "Cancel a draft contract",
		Permission: "hris.contract.update", Request: ReasonRequest{}, Response: ContractView{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelContractHTTP)})
}

// maskContract hides salaries without hris.contract.view_salary.
func maskContract(ctx context.Context, c *ContractView) {
	if c.Allowances == nil {
		c.Allowances = []hris.Allowance{}
	}
	if !can(ctx, "hris.contract.view_salary", c.PropertyID) {
		c.BaseSalary, c.Allowances = nil, []hris.Allowance{}
	}
}

func (m *Module) loadContract(ctx context.Context, tx pgx.Tx, cid uuid.UUID, lock bool) (ContractView, error) {
	sql := contractSelect + ` WHERE c.id = $2 AND c.property_id = $3`
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	property := handle.Property(ctx)
	c, err := getOne[ContractView]("contract")(tx.Query(ctx, sql, ymd(today(ctx, tx, property)), cid, property))
	if err != nil {
		return c, err
	}
	if c.Allowances == nil {
		c.Allowances = []hris.Allowance{}
	}
	return c, nil
}

func (m *Module) viewContract(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (ContractView, error) {
	c, err := m.loadContract(ctx, tx, cid, false)
	if err == nil {
		maskContract(ctx, &c)
	}
	return c, err
}

func (m *Module) listContractsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ContractView, error) {
	q := r.URL.Query()
	emp, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return nil, err
	}
	property := handle.Property(ctx)
	within := handle.QueryInt(r, "expiringWithin", -1)
	list, err := handle.List[ContractView](tx.Query(ctx, contractSelect+` WHERE c.property_id = $2 AND ($3::uuid IS NULL OR c.employee_id = $3)
		AND ($4 = '' OR c.status = ANY (string_to_array($4, ','))) AND ($5 = '' OR c.contract_type = $5)
		AND ($6::int < 0 OR (c.end_date IS NOT NULL AND c.status IN ('active', 'expiring') AND c.end_date <= $1::date + $6::int))
		ORDER BY CASE WHEN $6::int >= 0 THEN c.end_date END, e.full_name, c.sequence_no DESC LIMIT 1000`,
		ymd(today(ctx, tx, property)), property, emp, q.Get("status"), q.Get("contractType"), within))
	for i := range list {
		maskContract(ctx, &list[i])
	}
	return list, err
}

func (m *Module) getContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ContractView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ContractView{}, err
	}
	return m.viewContract(ctx, tx, cid)
}

func validAllowances(list []hris.Allowance) ([]hris.Allowance, error) {
	out := []hris.Allowance{}
	for i, a := range list {
		a.Code = strings.ToUpper(strings.TrimSpace(a.Code))
		a.Name = strings.TrimSpace(a.Name)
		if a.Code == "" || a.Name == "" {
			return nil, handle.Invalid("allowances", "invalid", "allowance "+itoa(i+1)+": code and name are required")
		}
		d, err := decimal.NewFromString(strings.TrimSpace(a.Amount))
		if err != nil || d.IsNegative() {
			return nil, handle.Invalid("allowances", "invalid", "allowance "+a.Code+": amount must be a non-negative number")
		}
		a.Amount = d.String()
		out = append(out, a)
	}
	return out, nil
}

func salary(field, v string) (decimal.Decimal, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(v))
	if err != nil || d.IsNegative() {
		return decimal.Zero, handle.Invalid(field, "invalid", "must be a non-negative amount")
	}
	return d, nil
}

// contractDraft are the validated values of a contract.
type contractDraft struct {
	employee         hris.Employee
	ctype            string
	start            time.Time
	end              *time.Time
	probation        int
	unit, pos, grade *uuid.UUID
	jobTitle         *string
	base             decimal.Decimal
	allowances       []hris.Allowance
	weekDays         int
	signed           *uuid.UUID
	notes            *string
	previous         *uuid.UUID
	sequence         int
}

func (m *Module) createContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ContractRequest) (ContractView, error) {
	property := handle.Property(ctx)
	e, err := hris.EmployeeByID(ctx, tx, req.EmployeeID)
	if err != nil || e.PropertyID != property {
		return ContractView{}, handle.Invalid("employeeId", "not_found", "employee not found")
	}
	if e.Status != "active" {
		return ContractView{}, errs.Conflict("employee_inactive", "the employee no longer works here")
	}
	if !oneOf([]string{"pkwt", "pkwtt"}, req.ContractType) {
		return ContractView{}, enumErr("contractType", []string{"pkwt", "pkwtt"})
	}
	start, err := mustDate("startDate", req.StartDate)
	if err != nil {
		return ContractView{}, err
	}
	end, err := parseDate("endDate", req.EndDate)
	if err != nil {
		return ContractView{}, err
	}
	base, err := salary("baseSalary", req.BaseSalary)
	if err != nil {
		return ContractView{}, err
	}
	al, err := validAllowances(req.Allowances)
	if err != nil {
		return ContractView{}, err
	}
	d := contractDraft{employee: e, ctype: req.ContractType, start: start, end: end, probation: req.ProbationMonths, unit: coalesceID(req.OrgUnitID, e.OrgUnitID),
		pos: coalesceID(req.PositionID, e.PositionID), grade: coalesceID(req.GradeID, e.GradeID), jobTitle: nullStr(req.JobTitle), base: base,
		allowances: al, weekDays: req.WorkWeekDays, signed: req.SignedFileID, notes: nullStr(req.Notes), sequence: 1}
	if d.jobTitle == nil {
		d.jobTitle = e.JobTitle
	}
	var prev *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM hris.contracts WHERE employee_id = $1 AND status IN ('active', 'expiring')
		ORDER BY start_date DESC LIMIT 1`, e.ID).Scan(&prev); err == nil && prev != nil {
		return ContractView{}, errs.Conflict("contract_exists", "the employee already has a contract: renew it or make the employee permanent")
	}
	cid, err := m.insertContract(ctx, tx, d)
	if err != nil {
		return ContractView{}, err
	}
	if req.Activate {
		if err := m.activate(ctx, tx, cid); err != nil {
			return ContractView{}, err
		}
	}
	c, err := m.viewContract(ctx, tx, cid)
	if err != nil {
		return c, err
	}
	return c, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.contract", EntityID: cid.String(),
		EntityLabel: c.Number + " · " + c.EmployeeName, PropertyID: &property, After: contractAudit(c)})
}

// contractAudit is the audit snapshot of a contract: amounts are never
// written to the audit log, only that they changed.
func contractAudit(c ContractView) map[string]any {
	return map[string]any{"number": c.Number, "employeeId": c.EmployeeID, "contractType": c.ContractType, "startDate": ymd(c.StartDate),
		"endDate": datePtr(c.EndDate), "probationMonths": c.ProbationMonths, "positionId": c.PositionID, "gradeId": c.GradeID, "status": c.Status,
		"sequenceNo": c.SequenceNo, "salaryRecorded": true}
}

func datePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ymd(*t)
}

// validateContract applies the HR Configuration rules (FR-CTR-03).
func (m *Module) validateContract(ctx context.Context, tx pgx.Tx, d *contractDraft) (int, error) {
	cfg, ref, err := hris.LoadHRConfiguration(ctx, tx, d.employee.PropertyID, clock.Now())
	if err != nil {
		return 0, err
	}
	if d.weekDays == 0 {
		d.weekDays = 5
	}
	if d.weekDays != 5 && d.weekDays != 6 {
		return 0, handle.Invalid("workWeekDays", "invalid", "5 or 6")
	}
	switch d.ctype {
	case "pkwt":
		if d.end == nil {
			return 0, handle.Invalid("endDate", "required", "a PKWT has an end date")
		}
		if d.end.Before(d.start) {
			return 0, handle.Invalid("endDate", "invalid", "must be on or after the start date")
		}
		if d.probation > 0 {
			return 0, handle.Invalid("probationMonths", "invalid", "probation is not allowed on a PKWT (PP 35/2021)")
		}
		// the PKWT chain of the employee: the first PKWT start and the renewals
		first := d.start
		renewals := 0
		if d.previous != nil {
			if err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
				SELECT id, previous_contract_id, start_date, contract_type FROM hris.contracts WHERE id = $1
				UNION ALL SELECT c.id, c.previous_contract_id, c.start_date, c.contract_type FROM hris.contracts c JOIN chain ON c.id = chain.previous_contract_id)
				SELECT min(start_date), count(*) FROM chain WHERE contract_type = 'pkwt'`, *d.previous).Scan(&first, &renewals); err != nil {
				return 0, err
			}
		}
		if limit := first.AddDate(0, cfg.PKWTMaxMonths, 0); cfg.PKWTMaxMonths > 0 && !d.end.Before(limit) {
			return 0, handle.Invalid("endDate", "pkwt_too_long",
				"PKWT including renewals may last at most "+itoa(cfg.PKWTMaxMonths)+" months (until "+ymd(limit.AddDate(0, 0, -1))+"); make the employee permanent")
		}
		if cfg.PKWTMaxRenewals > 0 && renewals > cfg.PKWTMaxRenewals {
			return 0, errs.Conflict("pkwt_renewals", "the PKWT was already renewed "+itoa(cfg.PKWTMaxRenewals)+" times")
		}
	case "pkwtt":
		if d.end != nil {
			return 0, handle.Invalid("endDate", "invalid", "a PKWTT has no end date")
		}
		if d.probation < 0 || d.probation > cfg.ProbationMaxMonths {
			return 0, handle.Invalid("probationMonths", "invalid", "probation is at most "+itoa(cfg.ProbationMaxMonths)+" months")
		}
		if d.probation > 0 && d.previous != nil {
			return 0, handle.Invalid("probationMonths", "invalid", "no probation after a PKWT with the company")
		}
	}
	if d.pos != nil {
		var unit uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT org_unit_id FROM hris.positions WHERE id = $1 AND property_id = $2`, *d.pos, d.employee.PropertyID).Scan(&unit); err != nil {
			return 0, handle.Invalid("positionId", "not_found", "position not found")
		}
		if d.unit == nil {
			d.unit = &unit
		}
	}
	return ref.Version, nil
}

func (m *Module) insertContract(ctx context.Context, tx pgx.Tx, d contractDraft) (uuid.UUID, error) {
	version, err := m.validateContract(ctx, tx, &d)
	if err != nil {
		return uuid.Nil, err
	}
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, d.employee.PropertyID, clock.Now())
	if err != nil {
		return uuid.Nil, err
	}
	no, err := yearlyNumber(ctx, tx, d.employee.PropertyID, strings.ToUpper(cfg.ContractNumberPrefix), d.start.Year())
	if err != nil {
		return uuid.Nil, err
	}
	var probEnd *string
	if d.probation > 0 {
		s := ymd(d.start.AddDate(0, d.probation, -1))
		probEnd = &s
	}
	raw, _ := json.Marshal(d.allowances)
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	cid := id.New()
	var end *string
	if d.end != nil {
		s := ymd(*d.end)
		end = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.contracts (id, property_id, number, employee_id, contract_type, sequence_no, previous_contract_id, start_date,
		end_date, probation_months, probation_end_date, org_unit_id, position_id, grade_id, job_title, currency, base_salary, allowances, work_week_days,
		signed_file_id, notes, policy_version, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::date,$9::date,$10,$11::date,$12,$13,$14,$15,$16,$17::numeric,$18,$19,$20,$21,$22,'draft',$23,$23)`,
		cid, d.employee.PropertyID, no, d.employee.ID, d.ctype, d.sequence, d.previous, ymd(d.start), end, d.probation, probEnd, d.unit, d.pos, d.grade,
		d.jobTitle, cur, d.base.String(), raw, d.weekDays, d.signed, d.notes, version, actor(ctx)); err != nil {
		return uuid.Nil, err
	}
	return cid, nil
}

func (m *Module) updateContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ContractUpdate) (ContractView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ContractView{}, err
	}
	before, err := m.loadContract(ctx, tx, cid, true)
	if err != nil {
		return before, err
	}
	onlyFile := req.StartDate == nil && req.EndDate == nil && req.ProbationMonths == nil && req.PositionID == nil && req.GradeID == nil &&
		req.JobTitle == nil && req.BaseSalary == nil && req.Allowances == nil && req.WorkWeekDays == nil
	if before.Status != "draft" && (!onlyFile || (before.Status != "active" && before.Status != "expiring")) {
		return before, errs.Conflict("contract_not_draft", "only a draft contract can be edited; renew it or end it instead")
	}
	e, err := hris.EmployeeByID(ctx, tx, before.EmployeeID)
	if err != nil {
		return before, err
	}
	d := contractDraft{employee: e, ctype: before.ContractType, start: before.StartDate, end: before.EndDate, probation: before.ProbationMonths,
		unit: before.OrgUnitID, pos: before.PositionID, grade: before.GradeID, jobTitle: before.JobTitle, allowances: before.Allowances,
		weekDays: before.WorkWeekDays, previous: before.PreviousContractID}
	if before.BaseSalary != nil {
		d.base, _ = decimal.NewFromString(*before.BaseSalary)
	}
	if req.StartDate != nil {
		if d.start, err = mustDate("startDate", *req.StartDate); err != nil {
			return before, err
		}
	}
	if req.EndDate != nil {
		if d.end, err = parseDate("endDate", *req.EndDate); err != nil {
			return before, err
		}
	}
	if req.ProbationMonths != nil {
		d.probation = *req.ProbationMonths
	}
	if req.PositionID != nil {
		d.pos, d.unit = req.PositionID, nil
	}
	if req.GradeID != nil {
		d.grade = req.GradeID
	}
	if req.JobTitle != nil {
		d.jobTitle = nullStr(*req.JobTitle)
	}
	if req.BaseSalary != nil {
		if d.base, err = salary("baseSalary", *req.BaseSalary); err != nil {
			return before, err
		}
	}
	if req.Allowances != nil {
		if d.allowances, err = validAllowances(req.Allowances); err != nil {
			return before, err
		}
	}
	if req.WorkWeekDays != nil {
		d.weekDays = *req.WorkWeekDays
	}
	if before.Status == "draft" {
		version, err := m.validateContract(ctx, tx, &d)
		if err != nil {
			return before, err
		}
		var probEnd, end *string
		if d.probation > 0 {
			s := ymd(d.start.AddDate(0, d.probation, -1))
			probEnd = &s
		}
		if d.end != nil {
			s := ymd(*d.end)
			end = &s
		}
		raw, _ := json.Marshal(d.allowances)
		if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET start_date = $2::date, end_date = $3::date, probation_months = $4, probation_end_date = $5::date,
			org_unit_id = $6, position_id = $7, grade_id = $8, job_title = $9, base_salary = $10::numeric, allowances = $11, work_week_days = $12,
			policy_version = $13, updated_by = $14 WHERE id = $1`, cid, ymd(d.start), end, d.probation, probEnd, d.unit, d.pos, d.grade, d.jobTitle,
			d.base.String(), raw, d.weekDays, version, actor(ctx)); err != nil {
			return before, err
		}
	}
	if req.SignedFileID != nil || req.Notes != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET signed_file_id = coalesce($2, signed_file_id), notes = coalesce($3, notes), updated_by = $4
			WHERE id = $1`, cid, req.SignedFileID, req.Notes, actor(ctx)); err != nil {
			if strings.Contains(err.Error(), "foreign key") {
				return before, handle.Invalid("signedFileId", "not_found", "file not found")
			}
			return before, err
		}
	}
	after, err := m.viewContract(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	property := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.contract", EntityID: cid.String(),
		EntityLabel: after.Number + " · " + after.EmployeeName, PropertyID: &property, Before: contractAudit(before), After: contractAudit(after),
		Metadata: map[string]any{"salaryChanged": req.BaseSalary != nil || req.Allowances != nil}})
}

func (m *Module) activateContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ContractView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ContractView{}, err
	}
	before, err := m.loadContract(ctx, tx, cid, true)
	if err != nil {
		return before, err
	}
	if err := m.activate(ctx, tx, cid); err != nil {
		return before, err
	}
	after, err := m.viewContract(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	property := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: "hris.contract", EntityID: cid.String(),
		EntityLabel: after.Number + " · " + after.EmployeeName, PropertyID: &property, Before: map[string]any{"status": before.Status},
		After: map[string]any{"status": after.Status}})
}

// activate turns a draft into the contract in force and aligns the
// employee's status (PKWT → Contract, PKWTT in probation → Probation, else
// Permanent).
func (m *Module) activate(ctx context.Context, tx pgx.Tx, cid uuid.UUID) error {
	var eid uuid.UUID
	var status string
	if err := tx.QueryRow(ctx, `SELECT employee_id, status FROM hris.contracts WHERE id = $1 FOR UPDATE`, cid).Scan(&eid, &status); err != nil {
		return errs.NotFound("contract")
	}
	if status != "draft" {
		return errs.Conflict("contract_not_draft", "only a draft contract can be activated")
	}
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.contracts WHERE employee_id = $1 AND id <> $2 AND status IN ('active', 'expiring'))`,
		eid, cid).Scan(&other); err != nil {
		return err
	}
	if other {
		return errs.Conflict("contract_active", "the employee has a contract in force: renew it or make the employee permanent")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'active', activated_at = now(), updated_by = $2 WHERE id = $1`, cid, actor(ctx)); err != nil {
		return err
	}
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id FROM hris.employees WHERE id = $1`, eid).Scan(&property); err != nil {
		return err
	}
	return m.syncEmploymentStatus(ctx, tx, eid, today(ctx, tx, property))
}

// syncEmploymentStatus derives the employment status from the contract in
// force on a day and records the change in the history.
func (m *Module) syncEmploymentStatus(ctx context.Context, tx pgx.Tx, eid uuid.UUID, day time.Time) error {
	var cur string
	var property uuid.UUID
	var permanentDate *time.Time
	if err := tx.QueryRow(ctx, `SELECT employment_status, property_id, permanent_date FROM hris.employees WHERE id = $1 AND status = 'active' FOR UPDATE`, eid).
		Scan(&cur, &property, &permanentDate); err != nil {
		return nil //nolint:nilerr // inactive or archived: nothing to align
	}
	if cur == hris.StatusResigned || cur == hris.StatusTerminated {
		return nil
	}
	c, err := hris.ContractAt(ctx, tx, eid, day)
	if err != nil || c == nil {
		return err
	}
	want := hris.StatusPermanent
	var probEnd *string
	switch {
	case c.ContractType == "pkwt":
		want = hris.StatusContract
	case c.ProbationEndDate != nil && !c.ProbationEndDate.Before(day):
		want = hris.StatusProbation
		s := ymd(*c.ProbationEndDate)
		probEnd = &s
	}
	if c.ProbationEndDate != nil {
		s := ymd(*c.ProbationEndDate)
		probEnd = &s
	}
	var perm *string
	if want == hris.StatusPermanent && permanentDate == nil {
		start := c.StartDate
		if c.ProbationEndDate != nil {
			start = c.ProbationEndDate.AddDate(0, 0, 1)
		}
		s := ymd(start)
		perm = &s
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET employment_status = $2, probation_end_date = coalesce($3::date, probation_end_date),
		permanent_date = coalesce(permanent_date, $4::date) WHERE id = $1`, eid, want, probEnd, perm); err != nil {
		return err
	}
	if want == cur {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reference, reason,
		status, applied_at, created_by, updated_by) VALUES ($1,$2,$3,'status_change',$4::date,$5,$6,$7,$8,'applied',now(),$9,$9)`,
		id.New(), property, eid, ymd(day), cur, want, c.Number, "Contract "+c.Number+" ("+strings.ToUpper(c.ContractType)+")", actor(ctx))
	return err
}

func (m *Module) renewHTTP(permanent bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req ContractRenewRequest) (ContractView, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req ContractRenewRequest) (ContractView, error) {
		cid, err := handle.ID(r)
		if err != nil {
			return ContractView{}, err
		}
		prev, err := m.loadContract(ctx, tx, cid, true)
		if err != nil {
			return prev, err
		}
		if prev.ContractType != "pkwt" {
			return prev, errs.Conflict("not_pkwt", "only a PKWT is renewed or converted; a PKWTT has no end")
		}
		if prev.Status != "active" && prev.Status != "expiring" {
			return prev, errs.Conflict("contract_not_active", "only the contract in force can be renewed")
		}
		e, err := hris.EmployeeByID(ctx, tx, prev.EmployeeID)
		if err != nil {
			return prev, err
		}
		if e.Status != "active" || (e.TerminationDate != nil) {
			return prev, errs.Conflict("employee_leaving", "the employee leaves the company")
		}
		start := prev.EndDate.AddDate(0, 0, 1)
		if req.StartDate != "" {
			if start, err = mustDate("startDate", req.StartDate); err != nil {
				return prev, err
			}
			if start.Before(prev.StartDate) || start.After(prev.EndDate.AddDate(0, 0, 1)) {
				return prev, handle.Invalid("startDate", "invalid", "the new contract starts after the current one, at the latest the day after it ends")
			}
		}
		d := contractDraft{employee: e, start: start, unit: prev.OrgUnitID, pos: coalesceID(req.PositionID, prev.PositionID),
			grade: coalesceID(req.GradeID, prev.GradeID), jobTitle: prev.JobTitle, allowances: prev.Allowances, weekDays: prev.WorkWeekDays,
			notes: nullStr(req.Notes), previous: &prev.ID, sequence: prev.SequenceNo + 1}
		if req.PositionID != nil {
			d.unit = nil
		}
		if prev.BaseSalary != nil {
			d.base, _ = decimal.NewFromString(*prev.BaseSalary)
		}
		if req.BaseSalary != "" {
			if d.base, err = salary("baseSalary", req.BaseSalary); err != nil {
				return prev, err
			}
		}
		if req.Allowances != nil {
			if d.allowances, err = validAllowances(req.Allowances); err != nil {
				return prev, err
			}
		}
		if permanent {
			d.ctype = "pkwtt"
		} else {
			d.ctype = "pkwt"
			end, err := mustDate("endDate", req.EndDate)
			if err != nil {
				return prev, err
			}
			d.end = &end
		}
		nid, err := m.insertContract(ctx, tx, d)
		if err != nil {
			return prev, err
		}
		// the current contract stays in force until the new one starts
		endedOn := start.AddDate(0, 0, -1)
		if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'renewed', superseded_by_id = $2, ended_on = least(coalesce(end_date, $3::date), $3::date),
			updated_by = $4 WHERE id = $1`, prev.ID, nid, ymd(endedOn), actor(ctx)); err != nil {
			return prev, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'active', activated_at = now() WHERE id = $1`, nid); err != nil {
			return prev, err
		}
		day := today(ctx, tx, e.PropertyID)
		if !start.After(day) {
			if err := m.syncEmploymentStatus(ctx, tx, e.ID, day); err != nil {
				return prev, err
			}
		}
		kind := "renew"
		if permanent {
			kind = "make_permanent"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reference,
			reason, status, applied_at, created_by, updated_by)
			SELECT $1, property_id, id, 'contract', $2::date, employment_status, $3, $4, $5, 'applied', now(), $6, $6 FROM hris.employees WHERE id = $7`,
			id.New(), ymd(start), map[bool]string{true: hris.StatusPermanent, false: hris.StatusContract}[permanent], prev.Number, kind, actor(ctx), e.ID); err != nil {
			return prev, err
		}
		out, err := m.viewContract(ctx, tx, nid)
		if err != nil {
			return out, err
		}
		property := handle.Property(ctx)
		return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: kind, EntityType: "hris.contract", EntityID: nid.String(),
			EntityLabel: out.Number + " · " + out.EmployeeName, PropertyID: &property, Before: map[string]any{"previous": prev.Number},
			After: contractAudit(out)})
	}
}

func (m *Module) endContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req EndContractRequest) (ContractView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ContractView{}, err
	}
	before, err := m.loadContract(ctx, tx, cid, true)
	if err != nil {
		return before, err
	}
	if before.Status != "active" && before.Status != "expiring" {
		return before, errs.Conflict("contract_not_active", "only the contract in force can be ended")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return before, handle.Invalid("reason", "required", "a reason is required")
	}
	end, err := mustDate("endDate", req.EndDate)
	if err != nil {
		return before, err
	}
	if end.Before(before.StartDate) || (before.EndDate != nil && end.After(*before.EndDate)) {
		return before, handle.Invalid("endDate", "invalid", "must be within the contract period")
	}
	property := handle.Property(ctx)
	status := before.Status
	if end.Before(today(ctx, tx, property)) {
		status = "ended"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET ended_on = $2::date, end_reason = $3, status = $4, updated_by = $5 WHERE id = $1`,
		cid, ymd(end), req.Reason, status, actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.viewContract(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "end_contract", EntityType: "hris.contract", EntityID: cid.String(),
		EntityLabel: after.Number + " · " + after.EmployeeName, PropertyID: &property, Reason: req.Reason,
		Before: map[string]any{"status": before.Status, "endedOn": datePtr(before.EndedOn)}, After: map[string]any{"status": after.Status, "endedOn": ymd(end)}})
}

func (m *Module) cancelContractHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReasonRequest) (ContractView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ContractView{}, err
	}
	before, err := m.loadContract(ctx, tx, cid, true)
	if err != nil {
		return before, err
	}
	if before.Status != "draft" {
		return before, errs.Conflict("contract_not_draft", "only a draft contract can be cancelled")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'cancelled', end_reason = $2, updated_by = $3 WHERE id = $1`, cid, nullStr(req.Reason),
		actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.viewContract(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	property := handle.Property(ctx)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: "hris.contract", EntityID: cid.String(),
		EntityLabel: after.Number + " · " + after.EmployeeName, PropertyID: &property, Reason: req.Reason,
		Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status}})
}
