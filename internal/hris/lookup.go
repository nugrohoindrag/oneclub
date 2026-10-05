package hris

// Read API of the employee master and the organization for the other P5
// areas (scheduling, attendance, leave, payroll, service charge, BI). All
// functions take the caller's querier (normally its transaction), so row
// level security of the request applies; background jobs pass a system
// context. Sensitive personal data (NIK, NPWP, bank accounts, health) is
// not part of these structs: payroll reads it through PayrollProfile.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
)

// Employee is the employee master as other areas see it.
type Employee struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	PropertyID       uuid.UUID  `json:"propertyId" db:"property_id"`
	EmployeeNo       string     `json:"employeeNo" db:"employee_no"`
	FullName         string     `json:"fullName" db:"full_name"`
	Gender           *string    `json:"gender" db:"gender"`
	Email            *string    `json:"email" db:"email"`
	Phone            *string    `json:"phone" db:"phone"`
	OrgUnitID        *uuid.UUID `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitCode      *string    `json:"orgUnitCode" db:"org_unit_code"`
	OrgUnitName      *string    `json:"orgUnitName" db:"org_unit_name"`
	PositionID       *uuid.UUID `json:"positionId" db:"position_id"`
	PositionCode     *string    `json:"positionCode" db:"position_code"`
	PositionName     *string    `json:"positionName" db:"position_name"`
	WorkforceRole    *string    `json:"workforceRole" db:"workforce_role"`
	GradeID          *uuid.UUID `json:"gradeId" db:"grade_id"`
	GradeCode        *string    `json:"gradeCode" db:"grade_code"`
	GradeLevel       *int       `json:"gradeLevel" db:"grade_level"`
	SupervisorID     *uuid.UUID `json:"supervisorId" db:"supervisor_id"`
	UserID           *uuid.UUID `json:"userId" db:"user_id"`
	JobTitle         *string    `json:"jobTitle" db:"job_title"`
	CostCenter       *string    `json:"costCenter" db:"cost_center" doc:"Employee override, else the org unit cost center"`
	EmploymentStatus string     `json:"employmentStatus" db:"employment_status"`
	WorkerCategory   string     `json:"workerCategory" db:"worker_category"`
	JoinDate         *time.Time `json:"joinDate" db:"join_date"`
	ProbationEndDate *time.Time `json:"probationEndDate" db:"probation_end_date"`
	TerminationDate  *time.Time `json:"terminationDate" db:"termination_date"`
	Status           string     `json:"status" db:"status"`
}

// EmployeeSelect is the SELECT behind Employee (alias e).
const EmployeeSelect = `SELECT e.id, e.property_id, e.employee_no, e.full_name, e.gender, e.email, e.phone, e.org_unit_id, ou.code AS org_unit_code,
	ou.name AS org_unit_name, e.position_id, p.code AS position_code, p.name AS position_name, p.workforce_role, e.grade_id, g.code AS grade_code,
	g.level AS grade_level, e.supervisor_id, u.id AS user_id, coalesce(e.job_title, p.name) AS job_title, coalesce(e.cost_center, ou.cost_center) AS cost_center,
	e.employment_status, e.worker_category, e.join_date, e.probation_end_date, e.termination_date, e.status
	FROM hris.employees e
	LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
	LEFT JOIN hris.positions p ON p.id = e.position_id
	LEFT JOIN hris.grades g ON g.id = e.grade_id
	LEFT JOIN platform.users u ON u.employee_id = e.id`

func collectEmployees(rows pgx.Rows, err error) ([]Employee, error) {
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[Employee])
	if out == nil {
		out = []Employee{}
	}
	return out, err
}

func oneEmployee(rows pgx.Rows, err error) (*Employee, error) {
	list, err := collectEmployees(rows, err)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ErrNotEmployee is returned when a user is not linked to an employee.
var ErrNotEmployee = errs.NotFound("employee profile")

// EmployeeByID returns an employee (not found → errs.NotFound).
func EmployeeByID(ctx context.Context, q dbtx.Querier, id uuid.UUID) (Employee, error) {
	e, err := oneEmployee(q.Query(ctx, EmployeeSelect+` WHERE e.id = $1`, id))
	if err != nil {
		return Employee{}, err
	}
	if e == nil {
		return Employee{}, errs.NotFound("employee")
	}
	return *e, nil
}

// EmployeeByUser returns the employee linked to a user (nil when the user
// has no employee profile).
func EmployeeByUser(ctx context.Context, q dbtx.Querier, userID uuid.UUID) (*Employee, error) {
	return oneEmployee(q.Query(ctx, EmployeeSelect+` WHERE u.id = $1 AND e.archived_at IS NULL`, userID))
}

// EmployeeByNo returns the employee with a number at a property (nil when
// none).
func EmployeeByNo(ctx context.Context, q dbtx.Querier, property uuid.UUID, no string) (*Employee, error) {
	return oneEmployee(q.Query(ctx, EmployeeSelect+` WHERE e.property_id = $1 AND upper(e.employee_no) = upper($2) AND e.archived_at IS NULL`, property, no))
}

// EmployeeFilter selects employees.
type EmployeeFilter struct {
	PropertyID uuid.UUID
	// OrgUnitID includes the employees of the unit and of its sub-units.
	OrgUnitID *uuid.UUID
	// ActiveOn keeps the employees employed on that date (joined, not yet
	// terminated, not archived); nil = every status.
	ActiveOn *time.Time
	// Statuses limits the employment statuses (empty = all).
	Statuses []string
	IDs      []uuid.UUID
}

// Employees lists employees ordered by name.
func Employees(ctx context.Context, q dbtx.Querier, f EmployeeFilter) ([]Employee, error) {
	var day *string
	if f.ActiveOn != nil {
		s := f.ActiveOn.Format("2006-01-02")
		day = &s
	}
	return collectEmployees(q.Query(ctx, EmployeeSelect+` WHERE e.property_id = $1 AND e.archived_at IS NULL
		AND ($2::uuid IS NULL OR e.org_unit_id IN (WITH RECURSIVE d AS (SELECT id FROM hris.org_units WHERE id = $2
		  UNION ALL SELECT c.id FROM hris.org_units c JOIN d ON c.parent_id = d.id) SELECT id FROM d))
		AND ($3::date IS NULL OR ((e.join_date IS NULL OR e.join_date <= $3::date) AND (e.termination_date IS NULL OR e.termination_date > $3::date)
		  AND e.status = 'active'))
		AND (cardinality($4::text[]) = 0 OR e.employment_status = ANY ($4))
		AND (cardinality($5::uuid[]) = 0 OR e.id = ANY ($5))
		ORDER BY e.full_name, e.id`, f.PropertyID, f.OrgUnitID, day, nonNil(f.Statuses), nonNilIDs(f.IDs)))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilIDs(s []uuid.UUID) []uuid.UUID {
	if s == nil {
		return []uuid.UUID{}
	}
	return s
}

// EmployedOn reports whether the employee works on a date.
func (e Employee) EmployedOn(day time.Time) bool {
	d := day.Format("2006-01-02")
	if e.Status != "active" {
		return false
	}
	if e.JoinDate != nil && e.JoinDate.Format("2006-01-02") > d {
		return false
	}
	return e.TerminationDate == nil || e.TerminationDate.Format("2006-01-02") > d
}

// Supervisor returns the direct supervisor of an employee: the supervisor
// of the profile, else the head of the employee's org unit (or of the
// nearest parent unit with a head), skipping the employee themself. nil when
// there is none.
func Supervisor(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID) (*Employee, error) {
	var sup *uuid.UUID
	err := q.QueryRow(ctx, `WITH RECURSIVE me AS (SELECT id, supervisor_id, org_unit_id FROM hris.employees WHERE id = $1),
		up AS (SELECT u.id, u.parent_id, u.head_employee_id, 0 AS depth FROM hris.org_units u JOIN me ON u.id = me.org_unit_id
		  UNION ALL SELECT p.id, p.parent_id, p.head_employee_id, up.depth + 1 FROM hris.org_units p JOIN up ON p.id = up.parent_id WHERE up.depth < 20)
		SELECT coalesce((SELECT supervisor_id FROM me),
		  (SELECT up.head_employee_id FROM up JOIN hris.employees h ON h.id = up.head_employee_id AND h.status = 'active'
		   WHERE up.head_employee_id <> $1 ORDER BY up.depth LIMIT 1))`, employeeID).Scan(&sup)
	if err != nil {
		if dbtx.IsNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	if sup == nil {
		return nil, nil
	}
	s, err := EmployeeByID(ctx, q, *sup)
	if err != nil {
		if errs.Is(err, errs.KindNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// teamSQL lists the employees a manager leads: direct and indirect reports
// through supervisor lines, plus the members of the org units (and
// sub-units) the manager heads.
const teamSQL = `WITH RECURSIVE reports AS (
	  SELECT id, 1 AS depth FROM hris.employees WHERE supervisor_id = $1
	  UNION ALL SELECT e.id, r.depth + 1 FROM hris.employees e JOIN reports r ON e.supervisor_id = r.id WHERE r.depth < 20),
	units AS (
	  SELECT id FROM hris.org_units WHERE head_employee_id = $1
	  UNION ALL SELECT c.id FROM hris.org_units c JOIN units ON c.parent_id = units.id)
	SELECT id FROM reports UNION SELECT e.id FROM hris.employees e JOIN units ON e.org_unit_id = units.id WHERE e.id <> $1`

// Team returns the employees a manager leads (active profiles only).
func Team(ctx context.Context, q dbtx.Querier, managerID uuid.UUID) ([]Employee, error) {
	return collectEmployees(q.Query(ctx, EmployeeSelect+` WHERE e.id IN (`+teamSQL+`) AND e.archived_at IS NULL AND e.status = 'active'
		ORDER BY e.full_name, e.id`, managerID))
}

// IsManagerOf reports whether managerID leads employeeID (supervisor line or
// head of the employee's org unit or a parent unit).
func IsManagerOf(ctx context.Context, q dbtx.Querier, managerID, employeeID uuid.UUID) (bool, error) {
	if managerID == employeeID {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT $2::uuid IN (`+teamSQL+`)`, managerID, employeeID).Scan(&ok)
	return ok, err
}

// OrgUnit is an organization unit (department).
type OrgUnit struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	PropertyID     uuid.UUID  `json:"propertyId" db:"property_id"`
	Code           string     `json:"code" db:"code"`
	Name           string     `json:"name" db:"name"`
	ParentID       *uuid.UUID `json:"parentId" db:"parent_id"`
	UnitType       string     `json:"unitType" db:"unit_type"`
	CostCenter     *string    `json:"costCenter" db:"cost_center"`
	HeadEmployeeID *uuid.UUID `json:"headEmployeeId" db:"head_employee_id"`
	Status         string     `json:"status" db:"status"`
}

const orgUnitSelect = `SELECT id, property_id, code, name, parent_id, unit_type, cost_center, head_employee_id, status FROM hris.org_units`

// OrgUnits lists the org units of a property (archived excluded).
func OrgUnits(ctx context.Context, q dbtx.Querier, property uuid.UUID) ([]OrgUnit, error) {
	rows, err := q.Query(ctx, orgUnitSelect+` WHERE property_id = $1 AND archived_at IS NULL ORDER BY sort_order, name, id`, property)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[OrgUnit])
	if out == nil {
		out = []OrgUnit{}
	}
	return out, err
}

// OrgUnitByCode returns an org unit by code at a property (nil when none).
func OrgUnitByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*OrgUnit, error) {
	rows, err := q.Query(ctx, orgUnitSelect+` WHERE property_id = $1 AND code = upper($2) AND archived_at IS NULL`, property, code)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[OrgUnit])
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// Grade is a grade (G1–G7).
type Grade struct {
	ID    uuid.UUID `json:"id" db:"id"`
	Code  string    `json:"code" db:"code"`
	Name  string    `json:"name" db:"name"`
	Level int       `json:"level" db:"level"`
}

// Grades lists the active grades by level.
func Grades(ctx context.Context, q dbtx.Querier) ([]Grade, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, level FROM hris.grades WHERE archived_at IS NULL AND status = 'active' ORDER BY level, code`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[Grade])
	if out == nil {
		out = []Grade{}
	}
	return out, err
}

// Allowance is a fixed allowance of a contract.
type Allowance struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// Contract is the employment contract in force (payroll basis, FR-PAY-01).
type Contract struct {
	ID               uuid.UUID   `json:"id" db:"id"`
	Number           string      `json:"number" db:"number"`
	EmployeeID       uuid.UUID   `json:"employeeId" db:"employee_id"`
	ContractType     string      `json:"contractType" db:"contract_type"`
	StartDate        time.Time   `json:"startDate" db:"start_date"`
	EndDate          *time.Time  `json:"endDate" db:"end_date"`
	ProbationEndDate *time.Time  `json:"probationEndDate" db:"probation_end_date"`
	Currency         string      `json:"currency" db:"currency"`
	BaseSalary       string      `json:"baseSalary" db:"base_salary"`
	Allowances       []Allowance `json:"allowances" db:"allowances"`
	WorkWeekDays     int         `json:"workWeekDays" db:"work_week_days"`
	Status           string      `json:"status" db:"status"`
}

// FixedWage is base salary plus the fixed allowances (THR, BPJS and
// overtime wage basis).
func (c Contract) FixedWage() decimal.Decimal {
	w := Dec(c.BaseSalary)
	for _, a := range c.Allowances {
		w = w.Add(Dec(a.Amount))
	}
	return w
}

// ContractAt returns the contract of an employee in force on a date (active,
// expiring or since superseded / ended after that date); nil when none.
func ContractAt(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) (*Contract, error) {
	rows, err := q.Query(ctx, `SELECT id, number, employee_id, contract_type, start_date, end_date, probation_end_date, currency,
		trim_scale(base_salary)::text AS base_salary, allowances, work_week_days, status FROM hris.contracts
		WHERE employee_id = $1 AND status IN ('active', 'expiring', 'renewed', 'ended') AND start_date <= $2::date
		  AND coalesce(ended_on, end_date, 'infinity'::date) >= $2::date
		ORDER BY start_date DESC, sequence_no DESC LIMIT 1`, employeeID, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[Contract])
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// WarningLevel returns the highest warning letter level (1–3) of an
// employee valid on a date; 0 when none (service charge eligibility).
func WarningLevel(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID, day time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT coalesce(max(warning_level), 0) FROM hris.employee_documents
		WHERE employee_id = $1 AND document_type = 'warning_letter' AND archived_at IS NULL AND status = 'active'
		  AND (issued_on IS NULL OR issued_on <= $2::date) AND (expires_on IS NULL OR expires_on >= $2::date)`, employeeID, day.Format("2006-01-02")).Scan(&n)
	return n, err
}

// BankAccount is the primary salary account of an employee.
type BankAccount struct {
	BankCode    *string `json:"bankCode" db:"bank_code"`
	BankName    string  `json:"bankName" db:"bank_name"`
	AccountNo   string  `json:"accountNo" db:"account_no"`
	AccountName string  `json:"accountName" db:"account_name"`
}

// PayrollProfile is the sensitive part of an employee payroll needs (PPh 21,
// BPJS, bank file). Only payroll use cases call it; never log it.
type PayrollProfile struct {
	EmployeeID            uuid.UUID    `json:"employeeId"`
	NIK                   *string      `json:"nik"`
	NPWP                  *string      `json:"npwp"`
	PTKPStatus            *string      `json:"ptkpStatus"`
	BPJSKesehatanNo       *string      `json:"bpjsKesehatanNo"`
	BPJSKetenagakerjaanNo *string      `json:"bpjsKetenagakerjaanNo"`
	Bank                  *BankAccount `json:"bank"`
}

// PayrollProfileOf returns the payroll profile of an employee.
func PayrollProfileOf(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID) (PayrollProfile, error) {
	p := PayrollProfile{EmployeeID: employeeID}
	err := q.QueryRow(ctx, `SELECT nik, npwp, ptkp_status, bpjs_kesehatan_no, bpjs_ketenagakerjaan_no FROM hris.employees WHERE id = $1`, employeeID).
		Scan(&p.NIK, &p.NPWP, &p.PTKPStatus, &p.BPJSKesehatanNo, &p.BPJSKetenagakerjaanNo)
	if dbtx.IsNoRows(err) {
		return p, errs.NotFound("employee")
	}
	if err != nil {
		return p, err
	}
	rows, err := q.Query(ctx, `SELECT bank_code, bank_name, account_no, account_name FROM hris.employee_bank_accounts
		WHERE employee_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY is_primary DESC, created_at LIMIT 1`, employeeID)
	if err != nil {
		return p, err
	}
	banks, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[BankAccount])
	if err != nil {
		return p, err
	}
	if len(banks) > 0 {
		p.Bank = &banks[0]
	}
	return p, nil
}
