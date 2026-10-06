package corehr

// Migration wave 5, organization and documents (PRD P5 EP-28 FR-MIG-P5-01):
// grades, org units (hierarchy, cost center, head) and positions (grade,
// reporting line, workforce role, required certifications) by code, and
// employee documents (KTP, NPWP, BPJS, diplomas, warning letters …) with
// their scanned files. Loads are repeatable: grades, org units and
// positions upsert by code through the resource engine (same validation and
// hooks as the Back Office); documents skip rows already on file (same
// type and number, or same type, title and issue date). References inside
// one file (parent unit, reports-to position) are resolved after every row
// of the file exists; an org unit head is linked once the employee exists
// (re-run the org units file after the employees).

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// listValue normalises a list cell ("A|B", "A;B" or "A,B") for the
// resource engine (comma separated).
func listValue(s string) string {
	return strings.NewReplacer("|", ",", ";", ",").Replace(s)
}

func (m *Module) importOrganization(ctx context.Context, tx pgx.Tx, property uuid.UUID, entity string, rows []map[string]string, rep *ImportReport) error {
	key := map[string]string{ImportGrades: KeyGrade, ImportOrgUnits: KeyOrgUnit, ImportPositions: KeyPosition}[entity]
	def, ok := m.Engine.Def(key)
	if !ok {
		return errs.Unavailable("resource " + key + " not registered")
	}
	table := def.Table
	var scope *uuid.UUID
	if def.PropertyScoped {
		scope = &property
	}
	plain := map[string][]string{
		ImportGrades:    {"name", "level", "minSalary", "maxSalary", "description", "status"},
		ImportOrgUnits:  {"name", "unitType", "costCenter", "description", "sortOrder", "status"},
		ImportPositions: {"name", "isHead", "workforceRole", "headcount", "description", "status"},
	}[entity]
	line := 1
	if err := eachRow(ctx, tx, rows, "code", rep, func(row map[string]string) (string, error) {
		line++
		code := strings.ToUpper(strings.TrimSpace(row["code"]))
		if code == "" || row["name"] == "" {
			return "", handle.Invalid("code", "required", "code and name are required")
		}
		input := map[string]any{}
		for _, f := range plain {
			if v := row[f]; v != "" {
				input[f] = v
			}
		}
		switch entity {
		case ImportOrgUnits:
			if no := row["headEmployeeNo"]; no != "" {
				e, err := hris.EmployeeByNo(ctx, tx, property, no)
				if err != nil {
					return "", err
				}
				if e != nil {
					input["headEmployeeId"] = e.ID.String()
				} else {
					rep.Issues = append(rep.Issues, ImportIssue{Row: line, Key: code, Message: "head " + no + " not found yet: re-run the org units after the employees"})
				}
			}
		case ImportPositions:
			unit, err := lookupID(ctx, tx, "orgUnitCode", "hris.org_units", &property, row["orgUnitCode"])
			if err != nil {
				return "", err
			}
			if unit != nil {
				input["orgUnitId"] = unit.String()
			}
			grade, err := lookupID(ctx, tx, "gradeCode", "hris.grades", nil, row["gradeCode"])
			if err != nil {
				return "", err
			}
			if grade != nil {
				input["gradeId"] = grade.String()
			}
			if v := row["requiredCertifications"]; v != "" {
				input["requiredCertifications"] = listValue(v)
			}
		}
		existing, err := lookupID(ctx, tx, "code", table, scope, code)
		if err != nil {
			existing = nil // not found: insert
		}
		if existing != nil {
			if _, err := m.Engine.UpdateRow(ctx, tx, def, *existing, input); err != nil {
				return "", err
			}
			return "updated", nil
		}
		input["code"] = code
		if _, err := m.Engine.CreateRow(ctx, tx, def, input); err != nil {
			return "", err
		}
		return "inserted", nil
	}); err != nil {
		return err
	}
	// references inside the file, once every row exists
	ref := map[string][3]string{ImportOrgUnits: {"parentCode", "parentId", "hris.org_units"}, ImportPositions: {"reportsToCode", "reportsToPositionId", "hris.positions"}}
	r, ok := ref[entity]
	if !ok {
		return nil
	}
	for i, row := range rows {
		if row[r[0]] == "" || row["code"] == "" {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		err = func() error {
			self, err := lookupID(ctx, sp, "code", table, &property, row["code"])
			if err != nil {
				return err
			}
			target, err := lookupID(ctx, sp, r[0], r[2], &property, row[r[0]])
			if err != nil {
				return err
			}
			_, err = m.Engine.UpdateRow(ctx, sp, def, *self, map[string]any{r[1]: target.String()})
			return err
		}()
		if err != nil {
			_ = sp.Rollback(ctx)
			msg := err.Error()
			if e, ok := errs.As(err); ok {
				msg = e.Message
				for _, f := range e.Fields {
					msg += "; " + f.Field + ": " + f.Message
				}
			}
			rep.Issues = append(rep.Issues, ImportIssue{Row: i + 2, Key: row["code"], Message: r[0] + ": " + msg})
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) importDocuments(ctx context.Context, tx pgx.Tx, property uuid.UUID, rows []map[string]string, rep *ImportReport) error {
	return eachRow(ctx, tx, rows, "employeeNo", rep, func(row map[string]string) (string, error) {
		e, err := hris.EmployeeByNo(ctx, tx, property, row["employeeNo"])
		if err != nil {
			return "", err
		}
		if e == nil {
			return "", handle.Invalid("employeeNo", "not_found", "unknown employee "+row["employeeNo"])
		}
		dtype := strings.ToLower(strings.TrimSpace(row["documentType"]))
		req := DocumentRequest{DocumentType: dtype, Title: row["title"], DocumentNo: row["documentNo"], IssuedOn: row["issuedOn"], ExpiresOn: row["expiresOn"],
			Notes: row["notes"]}
		if v := row["warningLevel"]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return "", handle.Invalid("warningLevel", "invalid", "1, 2 or 3")
			}
			req.WarningLevel = &n
		}
		if v := row["confidential"]; v != "" {
			b, err := strconv.ParseBool(strings.ToLower(v))
			if err != nil {
				return "", handle.Invalid("confidential", "invalid", "true or false")
			}
			req.Confidential = &b
		}
		if v := strings.TrimSpace(row["file"]); v != "" {
			fid, err := uuid.Parse(v)
			if err != nil {
				return "", handle.Invalid("file", "invalid", "the id of an uploaded file (POST /api/v1/hris/document-files; the CLI uploads --documents-dir files)")
			}
			req.FileID = &fid
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employee_documents WHERE employee_id = $1 AND document_type = $2 AND archived_at IS NULL
			AND ((nullif($3, '') IS NOT NULL AND document_no = $3)
			  OR (nullif($3, '') IS NULL AND issued_on IS NOT DISTINCT FROM nullif($4, '')::date AND ($5 = '' OR title = $5))))`,
			e.ID, dtype, strings.TrimSpace(row["documentNo"]), row["issuedOn"], strings.TrimSpace(row["title"])).Scan(&dup); err != nil {
			return "", err
		}
		if dup {
			return "skipped", nil
		}
		if _, err := m.AddDocument(ctx, tx, e.ID, req); err != nil {
			return "", err
		}
		return "inserted", nil
	})
}

// UploadImportFiles stores the scanned files referenced by the "file"
// column of a documents CSV (paths relative to dir) and returns the CSV with
// the file ids, ready for ImportCSV (CLI: oneclub import hris --documents F
// --documents-dir D). Cells that already hold a file id are kept. Nothing is
// stored on a dry run: the paths are only checked.
func (m *Module) UploadImportFiles(ctx context.Context, property uuid.UUID, src io.Reader, dir string, dryRun bool) (string, error) {
	rd := csv.NewReader(src)
	rd.FieldsPerRecord = -1
	records, err := rd.ReadAll()
	if err != nil {
		return "", handle.Invalid("csv", "invalid", "malformed CSV: "+err.Error())
	}
	col := -1
	if len(records) > 0 {
		for i, h := range records[0] {
			if strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")) == "file" {
				col = i
			}
		}
	}
	ctx = reqctx.WithProperty(ctx, property)
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		stored := 0
		for _, rec := range records[1:] {
			if col < 0 || col >= len(rec) {
				continue
			}
			v := strings.TrimSpace(rec[col])
			if v == "" {
				continue
			}
			if _, err := uuid.Parse(v); err == nil {
				continue
			}
			path := filepath.Join(dir, filepath.Clean("/"+v))
			data, err := os.ReadFile(path) //nolint:gosec // operator-supplied migration directory, path cleaned to stay inside it
			if err != nil {
				return handle.Invalid("file", "not_found", "cannot read "+v+": "+err.Error())
			}
			if len(data) == 0 || len(data) > documentMaxBytes {
				return handle.Invalid("file", "size", v+": empty or larger than 10 MB")
			}
			ctype := http.DetectContentType(data)
			if i := strings.IndexByte(ctype, ';'); i >= 0 {
				ctype = ctype[:i]
			}
			if !documentFileTypes[ctype] {
				return handle.Invalid("file", "type", v+": JPEG, PNG, WebP or PDF only")
			}
			if dryRun {
				rec[col] = "" // checked; the rest of the row is validated by the dry run
				continue
			}
			f, err := m.Files.Save(ctx, tx, filepath.Base(v), ctype, "attachment", false, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return err
			}
			rec[col] = f.ID.String()
			stored++
		}
		if stored == 0 {
			return nil
		}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "platform.file",
			EntityLabel: "HR migration document files", PropertyID: &property, After: map[string]any{"files": stored}})
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.WriteAll(records); err != nil {
		return "", err
	}
	return b.String(), nil
}
