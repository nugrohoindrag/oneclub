package payroll

// Salary structures (PRD P5 FR-PAY-01): component lines per grade, position
// or employee, effective-dated and versioned. A structure is created as a
// draft, activated by a second person (FR-PPY-05) and revised into a new
// version with a later effective date; old versions stay for audit and
// retro calculations. The pay of an employee on a date merges the grade,
// position and employee structures in force (most specific wins) with the
// contract (base salary and fixed allowances of the contract win).

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
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

// SalaryStructureLine is one component of a salary structure.
type SalaryStructureLine struct {
	ComponentCode string `json:"componentCode"`
	Amount        string `json:"amount" doc:"Monthly amount; rate per present day; or % of basic (per the component calculation or Method)"`
	Method        string `json:"method,omitempty" enum:"fixed,per_present_day,percent_of_basic" doc:"Overrides the component calculation"`
	Note          string `json:"note,omitempty"`
}

// SalaryStructure is a version of a salary structure.
type SalaryStructure struct {
	ID            uuid.UUID             `json:"id" db:"id"`
	Code          string                `json:"code" db:"code"`
	Version       int                   `json:"version" db:"version"`
	Name          string                `json:"name" db:"name"`
	Scope         string                `json:"scope" db:"scope" enum:"grade,position,employee"`
	GradeID       *uuid.UUID            `json:"gradeId" db:"grade_id"`
	GradeCode     *string               `json:"gradeCode" db:"grade_code"`
	PositionID    *uuid.UUID            `json:"positionId" db:"position_id"`
	PositionName  *string               `json:"positionName" db:"position_name"`
	EmployeeID    *uuid.UUID            `json:"employeeId" db:"employee_id"`
	EmployeeName  *string               `json:"employeeName" db:"employee_name"`
	EffectiveFrom time.Time             `json:"effectiveFrom" db:"effective_from"`
	Status        string                `json:"status" db:"status" enum:"draft,active,inactive"`
	InForce       bool                  `json:"inForce" db:"in_force" doc:"The version of its target applied today"`
	Lines         []SalaryStructureLine `json:"lines" db:"lines"`
	PreviousID    *uuid.UUID            `json:"previousId" db:"previous_id"`
	ActivatedBy   *uuid.UUID            `json:"activatedBy" db:"activated_by"`
	ActivatedAt   *time.Time            `json:"activatedAt" db:"activated_at"`
	Notes         *string               `json:"notes" db:"notes"`
	CreatedBy     *uuid.UUID            `json:"createdBy" db:"created_by"`
	UpdatedBy     *uuid.UUID            `json:"updatedBy" db:"updated_by"`
	CreatedAt     time.Time             `json:"createdAt" db:"created_at"`
	UpdatedAt     time.Time             `json:"updatedAt" db:"updated_at"`
}

// SalaryStructureInput creates or edits a draft structure.
type SalaryStructureInput struct {
	Code          string                `json:"code,omitempty"`
	Name          string                `json:"name,omitempty"`
	Scope         string                `json:"scope,omitempty" enum:"grade,position,employee"`
	GradeID       *uuid.UUID            `json:"gradeId,omitempty"`
	PositionID    *uuid.UUID            `json:"positionId,omitempty"`
	EmployeeID    *uuid.UUID            `json:"employeeId,omitempty"`
	EffectiveFrom string                `json:"effectiveFrom,omitempty"`
	Lines         []SalaryStructureLine `json:"lines,omitempty"`
	Notes         *string               `json:"notes,omitempty"`
}

// SalaryStructureRevision creates the next version.
type SalaryStructureRevision struct {
	EffectiveFrom string `json:"effectiveFrom"`
	Note          string `json:"note,omitempty"`
}

// SalaryStructureDecision is the note of an activation / deactivation.
type SalaryStructureDecision struct {
	Note string `json:"note,omitempty"`
}

// ResolvedPayLine is a component of the resolved pay of an employee.
type ResolvedPayLine struct {
	ComponentCode string `json:"componentCode"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Category      string `json:"category"`
	Method        string `json:"method"`
	Amount        string `json:"amount"`
	Fixed         bool   `json:"fixed"`
	Source        string `json:"source" enum:"contract,employee,position,grade,component"`
	StructureID   string `json:"structureId,omitempty"`
}

// ResolvedPay is the pay of an employee on a date (FR-PAY-01).
type ResolvedPay struct {
	EmployeeID     uuid.UUID         `json:"employeeId"`
	Date           string            `json:"date"`
	ContractNumber string            `json:"contractNumber"`
	FixedWage      string            `json:"fixedWage" doc:"Monthly fixed wage: basis of BPJS, overtime (1/173) and THR"`
	Lines          []ResolvedPayLine `json:"lines"`
	Structures     []string          `json:"structures" doc:"Structure versions applied (code v<version>)"`
}

const structureSelect = `SELECT s.id, s.code, s.version, s.name, s.scope, s.grade_id, g.code AS grade_code, s.position_id, p.name AS position_name,
	s.employee_id, e.full_name AS employee_name, s.effective_from, s.status, s.lines, s.previous_id, s.activated_by, s.activated_at, s.notes,
	s.created_by, s.updated_by, s.created_at, s.updated_at,
	(s.status = 'active' AND s.id = (SELECT x.id FROM hris.salary_structures x WHERE x.property_id = s.property_id AND x.scope = s.scope
	  AND x.grade_id IS NOT DISTINCT FROM s.grade_id AND x.position_id IS NOT DISTINCT FROM s.position_id AND x.employee_id IS NOT DISTINCT FROM s.employee_id
	  AND x.status = 'active' AND x.effective_from <= $1::date ORDER BY x.effective_from DESC, x.version DESC LIMIT 1)) AS in_force
	FROM hris.salary_structures s
	LEFT JOIN hris.grades g ON g.id = s.grade_id
	LEFT JOIN hris.positions p ON p.id = s.position_id
	LEFT JOIN hris.employees e ON e.id = s.employee_id`

func (m *Module) registerStructures(reg *route.Registry) {
	tag := "HRIS Payroll"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/salary-structures", Summary: "Salary structures (all versions)",
		Permission: PermStructView, Response: SalaryStructure{}, List: true,
		Query: []route.Param{{Name: "scope", Enum: []string{"grade", "position", "employee"}}, {Name: "status", Enum: []string{"draft", "active", "inactive"}},
			{Name: "employeeId"}, {Name: "gradeId"}, {Name: "positionId"}, {Name: "code"}}, Handler: listRead(m.DB, m.structuresHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/salary-structures", Summary: "Create a draft salary structure",
		Permission: PermStructManage, Request: SalaryStructureInput{}, Response: SalaryStructure{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createStructureHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/salary-structures/resolved", Summary: "Resolved pay of an employee on a date",
		Permission: PermStructView, Response: ResolvedPay{}, Query: []route.Param{{Name: "employeeId", Required: true}, {Name: "date"}},
		Handler: handle.Read(m.DB, m.resolvedHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/salary-structures/{id}", Summary: "Salary structure version",
		Permission: PermStructView, Response: SalaryStructure{}, Handler: handle.Read(m.DB, m.getStructureHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/salary-structures/{id}", Summary: "Edit a draft salary structure",
		Permission: PermStructManage, Request: SalaryStructureInput{}, Response: SalaryStructure{}, Handler: handle.Write(m.DB, http.StatusOK, m.patchStructureHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/salary-structures/{id}:activate",
		Summary: "Activate a draft salary structure (by another person than its last editor)", Permission: PermStructApprove,
		Request: SalaryStructureDecision{}, Response: SalaryStructure{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.activateStructureHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/salary-structures/{id}:revise",
		Summary: "Create the next version of a salary structure (draft) with a new effective date", Permission: PermStructManage,
		Request: SalaryStructureRevision{}, Response: SalaryStructure{}, Status: http.StatusCreated, Handler: handle.Write(m.DB, http.StatusCreated, m.reviseStructureHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/salary-structures/{id}:deactivate", Summary: "Deactivate a salary structure version",
		Permission: PermStructApprove, Request: SalaryStructureDecision{}, Response: SalaryStructure{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.deactivateStructureHTTP)})
}

func (m *Module) structuresHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]SalaryStructure, error) {
	property := handle.Property(ctx)
	var ids [3]*uuid.UUID
	for i, n := range []string{"employeeId", "gradeId", "positionId"} {
		v, err := uuidParam(r, n)
		if err != nil {
			return nil, err
		}
		ids[i] = v
	}
	return handle.List[SalaryStructure](tx.Query(ctx, structureSelect+` WHERE s.property_id = $2 AND ($3 = '' OR s.scope = $3) AND ($4 = '' OR s.status = $4)
		AND ($5::uuid IS NULL OR s.employee_id = $5) AND ($6::uuid IS NULL OR s.grade_id = $6) AND ($7::uuid IS NULL OR s.position_id = $7)
		AND ($8 = '' OR s.code = upper($8))
		ORDER BY s.scope, s.code, s.version DESC LIMIT 500`, ymd(today(ctx, tx, property)), property, filterParam(r, "scope"), filterParam(r, "status"),
		ids[0], ids[1], ids[2], filterParam(r, "code")))
}

func (m *Module) structure(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (SalaryStructure, error) {
	rows, err := tx.Query(ctx, structureSelect+` WHERE s.id = $2`, ymd(today(ctx, tx, handle.Property(ctx))), sid)
	return handle.One[SalaryStructure](rows, err, "salary structure")
}

func (m *Module) getStructureHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (SalaryStructure, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return SalaryStructure{}, err
	}
	return m.structure(ctx, tx, sid)
}

// validateLines checks the component lines against the catalogue.
func validateLines(cat map[string]component, lines []SalaryStructureLine) ([]SalaryStructureLine, error) {
	seen := map[string]bool{}
	out := make([]SalaryStructureLine, 0, len(lines))
	for i, l := range lines {
		l.ComponentCode = strings.ToUpper(strings.TrimSpace(l.ComponentCode))
		field := "lines[" + itoa(i) + "]"
		c, ok := cat[l.ComponentCode]
		if !ok {
			return nil, handle.Invalid(field+".componentCode", "unknown_component", "unknown pay component "+l.ComponentCode)
		}
		if seen[l.ComponentCode] {
			return nil, handle.Invalid(field+".componentCode", "duplicate", "component "+l.ComponentCode+" appears twice")
		}
		seen[l.ComponentCode] = true
		if l.Method != "" && !oneOf([]string{"fixed", "per_present_day", "percent_of_basic"}, l.Method) {
			return nil, enumErr(field+".method", []string{"fixed", "per_present_day", "percent_of_basic"})
		}
		a, err := handle.Decimal(field+".amount", l.Amount, dec("-1"))
		if err != nil {
			return nil, err
		}
		if a.IsNegative() {
			return nil, handle.Invalid(field+".amount", "invalid", "an amount of 0 or more")
		}
		if c.Category == "severance" || c.Category == "thr" {
			return nil, handle.Invalid(field+".componentCode", "invalid", c.Code+" is calculated by the payroll run")
		}
		l.Amount = a.String()
		out = append(out, l)
	}
	return out, nil
}

func (m *Module) checkTarget(ctx context.Context, tx pgx.Tx, property uuid.UUID, scope string, grade, position, employee *uuid.UUID) error {
	var ok bool
	var err error
	switch scope {
	case "grade":
		if grade == nil || position != nil || employee != nil {
			return handle.Invalid("gradeId", "required", "a grade structure names the grade only")
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.grades WHERE id = $1)`, grade).Scan(&ok)
	case "position":
		if position == nil || grade != nil || employee != nil {
			return handle.Invalid("positionId", "required", "a position structure names the position only")
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.positions WHERE id = $1 AND property_id = $2)`, position, property).Scan(&ok)
	case "employee":
		if employee == nil || grade != nil || position != nil {
			return handle.Invalid("employeeId", "required", "an employee structure names the employee only")
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE id = $1 AND property_id = $2)`, employee, property).Scan(&ok)
	default:
		return enumErr("scope", []string{"grade", "position", "employee"})
	}
	if err != nil {
		return err
	}
	if !ok {
		return handle.Invalid(scope+"Id", "not_found", scope+" not found")
	}
	return nil
}

// structureAudit is the audit snapshot (component codes, never amounts).
func structureAudit(s SalaryStructure) map[string]any {
	codes := make([]string, 0, len(s.Lines))
	for _, l := range s.Lines {
		codes = append(codes, l.ComponentCode)
	}
	return map[string]any{"code": s.Code, "version": s.Version, "scope": s.Scope, "effectiveFrom": ymd(s.EffectiveFrom), "status": s.Status,
		"components": codes}
}

func (m *Module) createStructureHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req SalaryStructureInput) (SalaryStructure, error) {
	property := handle.Property(ctx)
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !componentCodeRe.MatchString(code) {
		return SalaryStructure{}, handle.Invalid("code", "invalid", "1–30 characters: A–Z, 0–9, - or _")
	}
	if strings.TrimSpace(req.Name) == "" {
		return SalaryStructure{}, handle.Invalid("name", "required", "is required")
	}
	if err := m.checkTarget(ctx, tx, property, req.Scope, req.GradeID, req.PositionID, req.EmployeeID); err != nil {
		return SalaryStructure{}, err
	}
	eff, err := mustDate("effectiveFrom", req.EffectiveFrom)
	if err != nil {
		return SalaryStructure{}, err
	}
	cat, err := loadComponents(ctx, tx, property, eff)
	if err != nil {
		return SalaryStructure{}, err
	}
	lines, err := validateLines(cat, req.Lines)
	if err != nil {
		return SalaryStructure{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.salary_structures WHERE property_id = $1 AND code = $2)`, property, code).Scan(&exists); err != nil {
		return SalaryStructure{}, err
	}
	if exists {
		return SalaryStructure{}, errs.Conflict("code_taken", "a structure with this code exists; revise it to create a new version")
	}
	raw, _ := json.Marshal(lines)
	sid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.salary_structures (id, property_id, code, version, name, scope, grade_id, position_id, employee_id,
		effective_from, lines, notes, created_by, updated_by) VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
		sid, property, code, strings.TrimSpace(req.Name), req.Scope, req.GradeID, req.PositionID, req.EmployeeID, ymd(eff), raw, req.Notes, actor(ctx)); err != nil {
		return SalaryStructure{}, err
	}
	out, err := m.structure(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.salary_structure", EntityID: sid.String(),
		EntityLabel: code + " v1", PropertyID: &property, After: structureAudit(out)})
}

func lockStructure(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (string, *uuid.UUID, error) {
	var status string
	var editor *uuid.UUID
	err := tx.QueryRow(ctx, `SELECT status, coalesce(updated_by, created_by) FROM hris.salary_structures WHERE id = $1 FOR UPDATE`, sid).Scan(&status, &editor)
	if dbtx.IsNoRows(err) {
		return "", nil, errs.NotFound("salary structure")
	}
	return status, editor, err
}

func (m *Module) patchStructureHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req SalaryStructureInput) (SalaryStructure, error) {
	property := handle.Property(ctx)
	sid, err := handle.ID(r)
	if err != nil {
		return SalaryStructure{}, err
	}
	status, _, err := lockStructure(ctx, tx, sid)
	if err != nil {
		return SalaryStructure{}, err
	}
	if status != "draft" {
		return SalaryStructure{}, errs.Conflict("structure_not_draft", "only a draft can be edited; revise the structure to change it")
	}
	if req.Code != "" || req.Scope != "" || req.GradeID != nil || req.PositionID != nil || req.EmployeeID != nil {
		return SalaryStructure{}, handle.Invalid("code", "read_only", "code, scope and target cannot change")
	}
	before, err := m.structure(ctx, tx, sid)
	if err != nil {
		return before, err
	}
	name, eff, lines := before.Name, before.EffectiveFrom, before.Lines
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
	}
	if req.EffectiveFrom != "" {
		if eff, err = mustDate("effectiveFrom", req.EffectiveFrom); err != nil {
			return before, err
		}
	}
	if req.Lines != nil {
		cat, err := loadComponents(ctx, tx, property, eff)
		if err != nil {
			return before, err
		}
		if lines, err = validateLines(cat, req.Lines); err != nil {
			return before, err
		}
	}
	raw, _ := json.Marshal(lines)
	if _, err := tx.Exec(ctx, `UPDATE hris.salary_structures SET name = $2, effective_from = $3, lines = $4, notes = coalesce($5, notes), updated_by = $6
		WHERE id = $1`, sid, name, ymd(eff), raw, req.Notes, actor(ctx)); err != nil {
		return before, err
	}
	out, err := m.structure(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.salary_structure", EntityID: sid.String(),
		EntityLabel: out.Code + " v" + itoa(out.Version), PropertyID: &property, Before: structureAudit(before), After: structureAudit(out)})
}

func (m *Module) activateStructureHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req SalaryStructureDecision) (SalaryStructure, error) {
	property := handle.Property(ctx)
	sid, err := handle.ID(r)
	if err != nil {
		return SalaryStructure{}, err
	}
	status, editor, err := lockStructure(ctx, tx, sid)
	if err != nil {
		return SalaryStructure{}, err
	}
	if status != "draft" {
		return SalaryStructure{}, errs.Conflict("structure_not_draft", "only a draft can be activated")
	}
	if me := actor(ctx); me != nil && editor != nil && *me == *editor {
		return SalaryStructure{}, errs.Forbidden("a second person activates a salary structure (FR-PPY-05): you edited it last")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.salary_structures SET status = 'active', activated_by = $2, activated_at = now() WHERE id = $1`, sid,
		actor(ctx)); err != nil {
		return SalaryStructure{}, err
	}
	out, err := m.structure(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "activate", EntityType: "hris.salary_structure", EntityID: sid.String(),
		EntityLabel: out.Code + " v" + itoa(out.Version), PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status},
		After: structureAudit(out)})
}

func (m *Module) reviseStructureHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req SalaryStructureRevision) (SalaryStructure, error) {
	property := handle.Property(ctx)
	sid, err := handle.ID(r)
	if err != nil {
		return SalaryStructure{}, err
	}
	if _, _, err := lockStructure(ctx, tx, sid); err != nil {
		return SalaryStructure{}, err
	}
	cur, err := m.structure(ctx, tx, sid)
	if err != nil {
		return cur, err
	}
	eff, err := mustDate("effectiveFrom", req.EffectiveFrom)
	if err != nil {
		return cur, err
	}
	var latest int
	var latestEff time.Time
	if err := tx.QueryRow(ctx, `SELECT max(version), max(effective_from) FROM hris.salary_structures WHERE property_id = $1 AND code = $2`, property, cur.Code).
		Scan(&latest, &latestEff); err != nil {
		return cur, err
	}
	if !eff.After(latestEff) {
		return cur, handle.Invalid("effectiveFrom", "invalid", "a revision takes effect after the latest version ("+ymd(latestEff)+")")
	}
	raw, _ := json.Marshal(cur.Lines)
	nid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.salary_structures (id, property_id, code, version, name, scope, grade_id, position_id, employee_id,
		effective_from, lines, previous_id, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		nid, property, cur.Code, latest+1, cur.Name, cur.Scope, cur.GradeID, cur.PositionID, cur.EmployeeID, ymd(eff), raw, sid, nullStr(req.Note),
		actor(ctx)); err != nil {
		return cur, err
	}
	out, err := m.structure(ctx, tx, nid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "revise", EntityType: "hris.salary_structure", EntityID: nid.String(),
		EntityLabel: out.Code + " v" + itoa(out.Version), PropertyID: &property, Reason: req.Note, After: structureAudit(out),
		Metadata: map[string]any{"previousId": sid}})
}

func (m *Module) deactivateStructureHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req SalaryStructureDecision) (SalaryStructure, error) {
	property := handle.Property(ctx)
	sid, err := handle.ID(r)
	if err != nil {
		return SalaryStructure{}, err
	}
	status, _, err := lockStructure(ctx, tx, sid)
	if err != nil {
		return SalaryStructure{}, err
	}
	if status == "inactive" {
		return SalaryStructure{}, errs.Conflict("structure_inactive", "the structure is already inactive")
	}
	if strings.TrimSpace(req.Note) == "" {
		return SalaryStructure{}, handle.Invalid("note", "required", "explain why the structure is withdrawn")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.salary_structures SET status = 'inactive', updated_by = $2 WHERE id = $1`, sid, actor(ctx)); err != nil {
		return SalaryStructure{}, err
	}
	out, err := m.structure(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "deactivate", EntityType: "hris.salary_structure", EntityID: sid.String(),
		EntityLabel: out.Code + " v" + itoa(out.Version), PropertyID: &property, Reason: req.Note, Before: map[string]any{"status": status},
		After: map[string]any{"status": "inactive"}})
}

// ── resolution ───────────────────────────────────────────────────────────

// payLine is a resolved component before the engine runs.
type payLine struct {
	comp   component
	method string
	amount string
	source string
	ref    string
}

// resolvePay merges the structures in force on day with the contract.
func resolvePay(ctx context.Context, q dbtx.Querier, cat map[string]component, e hris.Employee, c *hris.Contract, day time.Time) ([]payLine, []string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (s.scope) s.id, s.code, s.version, s.scope, s.lines FROM hris.salary_structures s
		WHERE s.property_id = $1 AND s.status = 'active' AND s.effective_from <= $2::date
		  AND ((s.scope = 'grade' AND s.grade_id = $3) OR (s.scope = 'position' AND s.position_id = $4) OR (s.scope = 'employee' AND s.employee_id = $5))
		ORDER BY s.scope, s.effective_from DESC, s.version DESC`, e.PropertyID, ymd(day), e.GradeID, e.PositionID, e.ID)
	if err != nil {
		return nil, nil, err
	}
	type st struct {
		id      uuid.UUID
		code    string
		version int
		scope   string
		lines   []SalaryStructureLine
	}
	byScope := map[string]st{}
	for rows.Next() {
		var s st
		if err := rows.Scan(&s.id, &s.code, &s.version, &s.scope, &s.lines); err != nil {
			rows.Close()
			return nil, nil, err
		}
		byScope[s.scope] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	merged := map[string]payLine{}
	var applied []string
	for _, scope := range []string{"grade", "position", "employee"} {
		s, ok := byScope[scope]
		if !ok {
			continue
		}
		applied = append(applied, s.code+" v"+itoa(s.version))
		for _, l := range s.lines {
			comp := componentOr(cat, l.ComponentCode, "", "")
			method := l.Method
			if method == "" {
				method = comp.Method
			}
			merged[comp.Code] = payLine{comp: comp, method: method, amount: l.Amount, source: scope, ref: s.id.String()}
		}
	}
	if c != nil {
		if dec(c.BaseSalary).IsPositive() {
			comp := componentOr(cat, "BASIC", "Basic Salary", "earning")
			merged["BASIC"] = payLine{comp: comp, method: "fixed", amount: c.BaseSalary, source: "contract", ref: c.ID.String()}
		}
		for _, a := range c.Allowances {
			code := strings.ToUpper(strings.TrimSpace(a.Code))
			if code == "" {
				continue
			}
			comp, ok := cat[code]
			if !ok {
				comp = component{Code: code, Name: a.Name, Kind: "earning", Category: hris.CatAllowance, Taxable: true, Sort: 25}
			}
			// a contract allowance is a fixed monthly allowance (Contract.FixedWage:
			// BPJS, overtime and THR basis), prorated for joiners and leavers
			comp.Kind, comp.Method, comp.Fixed, comp.Prorate = "earning", "fixed", true, true
			if comp.Name == "" {
				comp.Name = code
			}
			merged[code] = payLine{comp: comp, method: "fixed", amount: a.Amount, source: "contract", ref: c.ID.String()}
		}
	}
	out := make([]payLine, 0, len(merged))
	for _, l := range merged {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].comp.Sort != out[j].comp.Sort {
			return out[i].comp.Sort < out[j].comp.Sort
		}
		return out[i].comp.Code < out[j].comp.Code
	})
	return out, applied, nil
}

// fixedWage is the monthly fixed wage of resolved lines (fixed earnings;
// percent-of-basic lines on the basic salary).
func fixedWage(lines []payLine) (basic, wage string) {
	b := dec("0")
	for _, l := range lines {
		if l.comp.Code == "BASIC" {
			b = dec(l.amount)
		}
	}
	w := dec("0")
	for _, l := range lines {
		if l.comp.Kind != "earning" || !l.comp.Fixed || l.method == "per_present_day" {
			continue
		}
		a := dec(l.amount)
		if l.method == "percent_of_basic" {
			a = b.Mul(a).Div(dec("100"))
		}
		w = w.Add(a)
	}
	return b.String(), w.String()
}

func (m *Module) resolvedHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ResolvedPay, error) {
	property := handle.Property(ctx)
	eid, err := handle.QueryUUID(r, "employeeId")
	if err != nil {
		return ResolvedPay{}, err
	}
	if eid == nil {
		return ResolvedPay{}, handle.Invalid("employeeId", "required", "is required")
	}
	day, err := handle.QueryDate(r, "date", today(ctx, tx, property))
	if err != nil {
		return ResolvedPay{}, err
	}
	e, err := hris.EmployeeByID(ctx, tx, *eid)
	if err != nil {
		return ResolvedPay{}, err
	}
	if e.PropertyID != property {
		return ResolvedPay{}, errs.NotFound("employee")
	}
	c, err := hris.ContractAt(ctx, tx, e.ID, day)
	if err != nil {
		return ResolvedPay{}, err
	}
	cat, err := loadComponents(ctx, tx, property, day)
	if err != nil {
		return ResolvedPay{}, err
	}
	lines, applied, err := resolvePay(ctx, tx, cat, e, c, day)
	if err != nil {
		return ResolvedPay{}, err
	}
	out := ResolvedPay{EmployeeID: e.ID, Date: ymd(day), Lines: []ResolvedPayLine{}, Structures: applied}
	if out.Structures == nil {
		out.Structures = []string{}
	}
	if c != nil {
		out.ContractNumber = c.Number
	}
	_, out.FixedWage = fixedWage(lines)
	for _, l := range lines {
		out.Lines = append(out.Lines, ResolvedPayLine{ComponentCode: l.comp.Code, Name: l.comp.Name, Kind: l.comp.Kind, Category: l.comp.Category,
			Method: l.method, Amount: l.amount, Fixed: l.comp.Fixed, Source: l.source, StructureID: l.ref})
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
