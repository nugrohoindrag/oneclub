package corehr

// Migration wave 5 (EP-28 FR-MIG-P5-01/04): employees (with organization
// placement, identity, BPJS and bank data), employment contracts and
// certifications of employees, caddies and instructors from the club's HR
// system / Excel as CSV. Every row runs in its own savepoint, so a bad row
// is reported and skipped; loads are repeatable (employees upsert by
// employee number, contracts and certifications skip duplicates). Used by
// POST /api/v1/hris/imports and `oneclub import hris`.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Import entities.
const (
	ImportEmployees      = "employees"
	ImportContracts      = "contracts"
	ImportCertifications = "certifications"
)

// ImportEntities lists the entities in load order.
var ImportEntities = []string{ImportEmployees, ImportContracts, ImportCertifications}

// ImportColumns documents the CSV header of each entity.
var ImportColumns = map[string][]string{
	ImportEmployees: {"employeeNo", "fullName", "orgUnitCode", "positionCode", "gradeCode", "supervisorNo", "employmentStatus", "workerCategory", "joinDate",
		"gender", "birthDate", "birthPlace", "religion", "maritalStatus", "nik", "npwp", "ptkpStatus", "bpjsKesehatanNo", "bpjsKetenagakerjaanNo", "email",
		"personalEmail", "phone", "address", "city", "postalCode", "bankName", "bankCode", "accountNo", "accountName", "legacyRef"},
	ImportContracts: {"employeeNo", "contractType", "startDate", "endDate", "probationMonths", "positionCode", "gradeCode", "baseSalary", "allowances",
		"workWeekDays", "status", "previousNumber", "notes"},
	ImportCertifications: {"holderKind", "employeeNo", "partnerCode", "typeCode", "certificateNo", "issuer", "issuedOn", "expiresOn", "notes"},
}

// HRImportRequest is a CSV upload.
type HRImportRequest struct {
	Entity string `json:"entity" enum:"employees,contracts,certifications"`
	CSV    string `json:"csv" doc:"CSV with a header row (column names of the entity, see the HRIS import screen)"`
	DryRun bool   `json:"dryRun,omitempty" doc:"Validate only; nothing is saved"`
}

// ImportIssue is a rejected row.
type ImportIssue struct {
	Row     int    `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

// ImportReport summarises a load.
type ImportReport struct {
	Entity   string        `json:"entity"`
	DryRun   bool          `json:"dryRun"`
	Rows     int           `json:"rows"`
	Inserted int           `json:"inserted"`
	Updated  int           `json:"updated"`
	Skipped  int           `json:"skipped"`
	Failed   int           `json:"failed"`
	Issues   []ImportIssue `json:"issues"`
}

func (m *Module) registerImports(reg *route.Registry) {
	add(reg, "HRIS Migration", route.Route{Method: http.MethodPost, Path: "/api/v1/hris/imports",
		Summary: "Import employees, contracts or certifications from CSV (migration wave 5)", Permission: "hris.import.create",
		Request: HRImportRequest{}, Response: ImportReport{}, Status: http.StatusOK, Handler: m.importHTTP})
}

func (m *Module) importHTTP(w http.ResponseWriter, r *http.Request) {
	var req HRImportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rep, err := m.ImportCSV(r.Context(), handle.Property(r.Context()), req.Entity, strings.NewReader(req.CSV), req.DryRun)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}

var errDryRun = errors.New("dry run")

// ImportCSV loads one entity from CSV at a property. ctx carries the
// caller (request or system) and must select the property.
func (m *Module) ImportCSV(ctx context.Context, property uuid.UUID, entity string, src io.Reader, dryRun bool) (ImportReport, error) {
	rep := ImportReport{Entity: entity, DryRun: dryRun, Issues: []ImportIssue{}}
	cols, ok := ImportColumns[entity]
	if !ok {
		return rep, handle.Invalid("entity", "invalid", "one of: "+strings.Join(ImportEntities, ", "))
	}
	rd := csv.NewReader(src)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	records, err := rd.ReadAll()
	if err != nil {
		return rep, handle.Invalid("csv", "invalid", "malformed CSV: "+err.Error())
	}
	if len(records) < 2 {
		return rep, handle.Invalid("csv", "empty", "a header row and at least one data row are required")
	}
	header := map[string]int{}
	for i, h := range records[0] {
		header[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	var unknown []string
	for h := range header {
		if !oneOf(cols, h) {
			unknown = append(unknown, h)
		}
	}
	if len(unknown) > 0 {
		return rep, handle.Invalid("csv", "unknown_column", "unknown column(s): "+strings.Join(unknown, ", ")+"; expected "+strings.Join(cols, ", "))
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := map[string]string{}
		empty := true
		for h, i := range header {
			if i < len(rec) {
				row[h] = strings.TrimSpace(rec[i])
				empty = empty && row[h] == ""
			}
		}
		if !empty {
			rows = append(rows, row)
		}
	}
	rep.Rows = len(rows)
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		switch entity {
		case ImportEmployees:
			err = m.importEmployees(ctx, tx, property, rows, &rep)
		case ImportContracts:
			err = m.importContracts(ctx, tx, property, rows, &rep)
		case ImportCertifications:
			err = m.importCertifications(ctx, tx, property, rows, &rep)
		}
		if err != nil {
			return err
		}
		if dryRun {
			return errDryRun
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionImport, EntityType: "hris." + entity, EntityLabel: "HR migration " + entity,
			PropertyID: &property, After: map[string]any{"rows": rep.Rows, "inserted": rep.Inserted, "updated": rep.Updated, "skipped": rep.Skipped,
				"failed": rep.Failed}})
	})
	if errors.Is(err, errDryRun) {
		err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "import_validate", EntityType: "hris." + entity,
				EntityLabel: "HR migration " + entity + " (dry run)", PropertyID: &property,
				After: map[string]any{"rows": rep.Rows, "valid": rep.Inserted + rep.Updated + rep.Skipped, "failed": rep.Failed}})
		})
	}
	return rep, err
}

// eachRow runs fn per row in a savepoint and records failures.
func eachRow(ctx context.Context, tx pgx.Tx, rows []map[string]string, key string, rep *ImportReport, fn func(row map[string]string) (string, error)) error {
	for i, row := range rows {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		result, err := fn(row)
		if err != nil {
			_ = sp.Rollback(ctx)
			msg := err.Error()
			if e, ok := errs.As(err); ok {
				msg = e.Message
				for _, f := range e.Fields {
					msg += "; " + f.Field + ": " + f.Message
				}
			}
			rep.Failed++
			rep.Issues = append(rep.Issues, ImportIssue{Row: i + 2, Key: row[key], Message: msg})
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		switch result {
		case "inserted":
			rep.Inserted++
		case "updated":
			rep.Updated++
		default:
			rep.Skipped++
		}
	}
	return nil
}

func lookupID(ctx context.Context, tx pgx.Tx, field, table string, property *uuid.UUID, code string) (*uuid.UUID, error) {
	if code == "" {
		return nil, nil
	}
	var out uuid.UUID
	var err error
	if property != nil {
		err = tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE property_id = $1 AND upper(code) = upper($2) AND archived_at IS NULL`, *property, code).Scan(&out)
	} else {
		err = tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE upper(code) = upper($1) AND archived_at IS NULL`, code).Scan(&out)
	}
	if err != nil {
		return nil, handle.Invalid(field, "not_found", "unknown "+field+" "+code)
	}
	return &out, nil
}

func (m *Module) importEmployees(ctx context.Context, tx pgx.Tx, property uuid.UUID, rows []map[string]string, rep *ImportReport) error {
	def, _ := m.Engine.Def(KeyEmployee)
	plain := []string{"fullName", "gender", "birthDate", "birthPlace", "religion", "maritalStatus", "nik", "npwp", "ptkpStatus", "bpjsKesehatanNo",
		"bpjsKetenagakerjaanNo", "email", "personalEmail", "phone", "address", "city", "postalCode", "legacyRef", "workerCategory", "joinDate"}
	if err := eachRow(ctx, tx, rows, "employeeNo", rep, func(row map[string]string) (string, error) {
		if row["employeeNo"] == "" || row["fullName"] == "" {
			return "", handle.Invalid("employeeNo", "required", "employeeNo and fullName are required")
		}
		input := map[string]any{}
		for _, f := range plain {
			if v := row[f]; v != "" {
				input[f] = v
			}
		}
		existing, err := hris.EmployeeByNo(ctx, tx, property, row["employeeNo"])
		if err != nil {
			return "", err
		}
		result := "updated"
		var eid uuid.UUID
		if existing == nil {
			input["employeeNo"] = row["employeeNo"]
			for f, c := range map[string][2]string{"orgUnitId": {"orgUnitCode", "hris.org_units"}, "positionId": {"positionCode", "hris.positions"}} {
				v, err := lookupID(ctx, tx, c[0], c[1], &property, row[c[0]])
				if err != nil {
					return "", err
				}
				if v != nil {
					input[f] = v.String()
				}
			}
			g, err := lookupID(ctx, tx, "gradeCode", "hris.grades", nil, row["gradeCode"])
			if err != nil {
				return "", err
			}
			if g != nil {
				input["gradeId"] = g.String()
			}
			if s := row["employmentStatus"]; s != "" {
				input["employmentStatus"] = s
			} else {
				input["employmentStatus"] = hris.StatusPermanent
			}
			created, err := m.Engine.CreateRow(ctx, tx, def, input)
			if err != nil {
				return "", err
			}
			eid, _ = uuid.Parse(created["id"].(string))
			result = "inserted"
		} else {
			eid = existing.ID
			if _, err := m.Engine.UpdateRow(ctx, tx, def, eid, input); err != nil {
				return "", err
			}
		}
		if row["accountNo"] != "" {
			var has bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employee_bank_accounts WHERE employee_id = $1 AND account_no = $2 AND archived_at IS NULL)`,
				eid, row["accountNo"]).Scan(&has); err != nil {
				return "", err
			}
			if !has {
				if row["bankName"] == "" {
					return "", handle.Invalid("bankName", "required", "bankName is required with accountNo")
				}
				name := row["accountName"]
				if name == "" {
					name = row["fullName"]
				}
				if _, err := tx.Exec(ctx, `UPDATE hris.employee_bank_accounts SET is_primary = false WHERE employee_id = $1`, eid); err != nil {
					return "", err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_bank_accounts (id, property_id, employee_id, bank_code, bank_name, account_no, account_name,
					is_primary, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,true,$8,$8)`, id.New(), property, eid, nullStr(row["bankCode"]),
					row["bankName"], row["accountNo"], name, actor(ctx)); err != nil {
					return "", err
				}
			}
		}
		return result, nil
	}); err != nil {
		return err
	}
	// supervisors once every employee of the file exists
	for i, row := range rows {
		if row["supervisorNo"] == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.employees e SET supervisor_id = s.id FROM hris.employees s
			WHERE e.property_id = $1 AND e.employee_no = upper($2) AND s.property_id = $1 AND s.employee_no = upper($3) AND s.id <> e.id`,
			property, row["employeeNo"], row["supervisorNo"]); err != nil {
			rep.Issues = append(rep.Issues, ImportIssue{Row: i + 2, Key: row["employeeNo"], Message: "supervisor: " + err.Error()})
		}
	}
	return nil
}

// parseAllowances reads "CODE:Name:amount;…" or a JSON array.
func parseAllowances(s string) ([]hris.Allowance, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		var out []hris.Allowance
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, handle.Invalid("allowances", "invalid", "invalid JSON")
		}
		return validAllowances(out)
	}
	var out []hris.Allowance
	for _, part := range strings.Split(s, ";") {
		f := strings.Split(strings.TrimSpace(part), ":")
		if len(f) != 3 {
			return nil, handle.Invalid("allowances", "invalid", "use CODE:Name:amount separated by ;")
		}
		out = append(out, hris.Allowance{Code: f[0], Name: f[1], Amount: f[2]})
	}
	return validAllowances(out)
}

func (m *Module) importContracts(ctx context.Context, tx pgx.Tx, property uuid.UUID, rows []map[string]string, rep *ImportReport) error {
	return eachRow(ctx, tx, rows, "employeeNo", rep, func(row map[string]string) (string, error) {
		e, err := hris.EmployeeByNo(ctx, tx, property, row["employeeNo"])
		if err != nil {
			return "", err
		}
		if e == nil {
			return "", handle.Invalid("employeeNo", "not_found", "unknown employee "+row["employeeNo"])
		}
		start, err := mustDate("startDate", row["startDate"])
		if err != nil {
			return "", err
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.contracts WHERE employee_id = $1 AND start_date = $2::date AND status <> 'cancelled')`,
			e.ID, ymd(start)).Scan(&dup); err != nil {
			return "", err
		}
		if dup {
			return "skipped", nil
		}
		end, err := parseDate("endDate", row["endDate"])
		if err != nil {
			return "", err
		}
		base, err := salary("baseSalary", row["baseSalary"])
		if err != nil {
			return "", err
		}
		al, err := parseAllowances(row["allowances"])
		if err != nil {
			return "", err
		}
		if al == nil {
			al = []hris.Allowance{}
		}
		probation, _ := strconv.Atoi(row["probationMonths"])
		week, _ := strconv.Atoi(row["workWeekDays"])
		d := contractDraft{employee: *e, ctype: strings.ToLower(row["contractType"]), start: start, end: end, probation: probation, unit: e.OrgUnitID,
			pos: e.PositionID, grade: e.GradeID, jobTitle: e.JobTitle, base: base, allowances: al, weekDays: week, notes: nullStr(row["notes"]), sequence: 1}
		if !oneOf([]string{"pkwt", "pkwtt"}, d.ctype) {
			return "", enumErr("contractType", []string{"pkwt", "pkwtt"})
		}
		if d.pos, err = lookupIDOr(ctx, tx, "positionCode", "hris.positions", &property, row["positionCode"], e.PositionID); err != nil {
			return "", err
		}
		if row["positionCode"] != "" {
			d.unit = nil
		}
		if d.grade, err = lookupIDOr(ctx, tx, "gradeCode", "hris.grades", nil, row["gradeCode"], e.GradeID); err != nil {
			return "", err
		}
		if prev := row["previousNumber"]; prev != "" {
			var pid uuid.UUID
			var seq int
			if err := tx.QueryRow(ctx, `SELECT id, sequence_no FROM hris.contracts WHERE property_id = $1 AND number = $2 AND employee_id = $3`, property, prev,
				e.ID).Scan(&pid, &seq); err != nil {
				return "", handle.Invalid("previousNumber", "not_found", "unknown previous contract "+prev)
			}
			d.previous, d.sequence = &pid, seq+1
		}
		cid, err := m.insertContract(ctx, tx, d)
		if err != nil {
			return "", err
		}
		switch strings.ToLower(row["status"]) {
		case "", "active":
			if d.previous != nil {
				if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'renewed', superseded_by_id = $2, ended_on = coalesce(ended_on, $3::date - 1)
					WHERE id = $1 AND status IN ('active', 'expiring', 'ended')`, *d.previous, cid, ymd(start)); err != nil {
					return "", err
				}
			}
			if err := m.activate(ctx, tx, cid); err != nil {
				return "", err
			}
		case "ended":
			if _, err := tx.Exec(ctx, `UPDATE hris.contracts SET status = 'ended', activated_at = now(), ended_on = end_date, end_reason = 'migrated'
				WHERE id = $1`, cid); err != nil {
				return "", err
			}
		case "draft":
		default:
			return "", enumErr("status", []string{"active", "ended", "draft"})
		}
		return "inserted", nil
	})
}

func lookupIDOr(ctx context.Context, tx pgx.Tx, field, table string, property *uuid.UUID, code string, def *uuid.UUID) (*uuid.UUID, error) {
	if code == "" {
		return def, nil
	}
	return lookupID(ctx, tx, field, table, property, code)
}

func (m *Module) importCertifications(ctx context.Context, tx pgx.Tx, property uuid.UUID, rows []map[string]string, rep *ImportReport) error {
	def, _ := m.Engine.Def(KeyCertification)
	return eachRow(ctx, tx, rows, "typeCode", rep, func(row map[string]string) (string, error) {
		kind := strings.ToLower(row["holderKind"])
		if kind == "" {
			kind = hris.HolderEmployee
		}
		t, err := lookupID(ctx, tx, "typeCode", "hris.certification_types", nil, row["typeCode"])
		if err != nil {
			return "", err
		}
		if t == nil {
			return "", handle.Invalid("typeCode", "required", "typeCode is required")
		}
		input := map[string]any{"certificationTypeId": t.String(), "holderKind": kind}
		var holder uuid.UUID
		switch kind {
		case hris.HolderEmployee:
			e, err := hris.EmployeeByNo(ctx, tx, property, row["employeeNo"])
			if err != nil {
				return "", err
			}
			if e == nil {
				return "", handle.Invalid("employeeNo", "not_found", "unknown employee "+row["employeeNo"])
			}
			holder = e.ID
			input["employeeId"] = e.ID.String()
		case hris.HolderCaddy, hris.HolderInstructor:
			if m.Partners == nil {
				return "", errs.Unavailable("partner directory not wired")
			}
			pid, _, err := m.Partners.PartnerByCode(ctx, tx, property, kind, row["partnerCode"])
			if err != nil {
				return "", err
			}
			if pid == nil {
				return "", handle.Invalid("partnerCode", "not_found", fmt.Sprintf("unknown %s %s", kind, row["partnerCode"]))
			}
			holder = *pid
			input["partnerId"] = pid.String()
		default:
			return "", enumErr("holderKind", []string{"employee", "caddy", "instructor"})
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.certifications WHERE certification_type_id = $1 AND holder_kind = $2
			AND coalesce(employee_id, partner_id) = $3 AND archived_at IS NULL
			AND (($4 <> '' AND certificate_no = $4) OR ($4 = '' AND issued_on IS NOT DISTINCT FROM nullif($5, '')::date)))`,
			*t, kind, holder, row["certificateNo"], row["issuedOn"]).Scan(&dup); err != nil {
			return "", err
		}
		if dup {
			return "skipped", nil
		}
		for _, f := range []string{"certificateNo", "issuer", "issuedOn", "expiresOn", "notes"} {
			if v := row[f]; v != "" {
				input[f] = v
			}
		}
		if _, err := m.Engine.CreateRow(ctx, tx, def, input); err != nil {
			return "", err
		}
		return "inserted", nil
	})
}

var _ = decimal.Zero
