package resource

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// ImportRequest is the JSON form of an import (multipart with a "file" part
// is also accepted).
type ImportRequest struct {
	Entity   string `json:"entity" doc:"Resource key, e.g. crm.customer"`
	Mode     string `json:"mode" enum:"preview,commit" doc:"preview validates without saving"`
	Filename string `json:"filename,omitempty"`
	CSV      string `json:"csv" doc:"CSV content with a header row (JSON field names or labels)"`
}

type RowError struct {
	Row     int    `json:"row" doc:"1-based line number in the file (header = 1)"`
	Field   string `json:"field,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ImportResult struct {
	ID           *uuid.UUID `json:"id"`
	Entity       string     `json:"entity"`
	Mode         string     `json:"mode" enum:"preview,commit"`
	Filename     string     `json:"filename"`
	Status       string     `json:"status" enum:"completed,failed"`
	TotalRows    int        `json:"totalRows"`
	InsertedRows int        `json:"insertedRows"`
	UpdatedRows  int        `json:"updatedRows"`
	FailedRows   int        `json:"failedRows"`
	Errors       []RowError `json:"errors"`
	CreatedAt    time.Time  `json:"createdAt"`
}

type ImportEntity struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	Plural         string   `json:"plural"`
	Module         string   `json:"module"`
	PropertyScoped bool     `json:"propertyScoped"`
	CodeField      string   `json:"codeField"`
	Columns        []string `json:"columns"`
	Required       []string `json:"required"`
}

// utf8BOM is stripped from the first header cell (Excel CSV exports).
const utf8BOM = string(rune(0xFEFF))

const (
	maxImportBytes = 10 << 20
	maxImportRows  = 50000
)

// headerMap maps CSV header cells to field JSON names.
func (d *Def) headerMap(header []string) (map[int]string, []RowError) {
	out := map[int]string{}
	var bad []RowError
	for i, h := range header {
		h = strings.TrimSpace(strings.TrimPrefix(h, utf8BOM))
		if h == "" || h == "id" || h == "propertyId" || h == "createdAt" || h == "updatedAt" || h == "archivedAt" {
			continue
		}
		matched := ""
		for _, f := range d.Fields {
			if f.ReadOnly {
				continue
			}
			if strings.EqualFold(h, f.Name) || strings.EqualFold(h, f.Label) || strings.EqualFold(h, f.Column) {
				matched = f.Name
			}
		}
		if matched == "" {
			bad = append(bad, RowError{Row: 1, Field: h, Code: "unknown_column", Message: "unknown column " + h})
			continue
		}
		out[i] = matched
	}
	return out, bad
}

// Import runs an import of r into d within tx (FR-MD-06): each row is
// validated and upserted by its code inside a savepoint, so invalid rows are
// reported without blocking valid ones, and re-running the same file is
// idempotent (existing codes are updated, not duplicated).
func (e *Engine) Import(ctx context.Context, tx pgx.Tx, d *Def, r io.Reader) (ImportResult, error) {
	res := ImportResult{Entity: d.Key, Errors: []RowError{}}
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return res, errs.Validation("csv_invalid", "the file is empty or not a CSV")
	}
	cols, bad := d.headerMap(header)
	if len(bad) > 0 {
		res.Errors = append(res.Errors, bad...)
		res.Status = "failed"
		return res, nil
	}
	code, hasCode := d.field(d.CodeField)
	if !hasCode {
		return res, errs.Validation("import_not_supported", d.Plural+" cannot be imported")
	}
	codeIdx := -1
	for i, n := range cols {
		if n == d.CodeField {
			codeIdx = i
		}
	}
	if codeIdx < 0 {
		res.Errors = append(res.Errors, RowError{Row: 1, Field: d.CodeField, Code: "missing_column", Message: "column " + d.CodeField + " is required"})
		res.Status = "failed"
		return res, nil
	}
	pid, hasProp := reqctx.Property(ctx)
	line := 1
	for {
		rec, err := cr.Read()
		line++
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			res.TotalRows++
			res.FailedRows++
			res.Errors = append(res.Errors, RowError{Row: line, Code: "csv_invalid", Message: err.Error()})
			continue
		}
		empty := true
		for _, c := range rec {
			if strings.TrimSpace(c) != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		res.TotalRows++
		if res.TotalRows > maxImportRows {
			return res, errs.Validation("too_many_rows", "imports are limited to 50,000 rows per file")
		}
		input := map[string]any{}
		for i, name := range cols {
			if i < len(rec) && strings.TrimSpace(rec[i]) != "" {
				input[name] = rec[i]
			}
		}
		codeVal, fe := coerce(code, input[d.CodeField])
		if fe != nil || codeVal == nil {
			res.FailedRows++
			res.Errors = append(res.Errors, RowError{Row: line, Field: d.CodeField, Code: "required", Message: code.Label + " is required"})
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return res, err
		}
		var existing *uuid.UUID
		sql := "SELECT id FROM " + d.Table + " WHERE " + code.Column + " = $1"
		args := []any{codeVal}
		if d.PropertyScoped && hasProp {
			sql += " AND property_id = $2"
			args = append(args, pid)
		}
		var eid uuid.UUID
		if err := sp.QueryRow(ctx, sql, args...).Scan(&eid); err == nil {
			existing = &eid
		} else if !dbtx.IsNoRows(err) {
			_ = sp.Rollback(ctx)
			return res, err
		}
		if existing != nil {
			delete(input, d.CodeField)
			_, err = e.UpdateRow(ctx, sp, d, *existing, input)
		} else {
			_, err = e.CreateRow(ctx, sp, d, input)
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			res.FailedRows++
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				if len(de.Fields) == 0 {
					res.Errors = append(res.Errors, RowError{Row: line, Code: de.Code, Message: de.Message})
				}
				for _, f := range de.Fields {
					res.Errors = append(res.Errors, RowError{Row: line, Field: f.Field, Code: f.Code, Message: f.Message})
				}
				continue
			}
			return res, err
		}
		if err := sp.Commit(ctx); err != nil {
			return res, err
		}
		if existing != nil {
			res.UpdatedRows++
		} else {
			res.InsertedRows++
		}
	}
	res.Status = "completed"
	if res.TotalRows > 0 && res.FailedRows == res.TotalRows {
		res.Status = "failed"
	}
	return res, nil
}

var errPreviewRollback = errors.New("preview rollback")

func (e *Engine) handleImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req ImportRequest
	var body io.Reader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes+64<<10) // file + multipart envelope
		if err := r.ParseMultipartForm(maxImportBytes); err != nil {   //nolint:gosec // G120: body capped by MaxBytesReader above
			httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "invalid upload"))
			return
		}
		req.Entity, req.Mode = r.FormValue("entity"), r.FormValue("mode")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose a CSV file")))
			return
		}
		defer f.Close()
		req.Filename = hdr.Filename
		body = io.LimitReader(f, maxImportBytes)
	} else {
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		body = strings.NewReader(req.CSV)
	}
	if req.Mode == "" {
		req.Mode = "commit"
	}
	if req.Mode != "preview" && req.Mode != "commit" {
		httpx.WriteError(w, r, errs.Validation("invalid_mode", "mode must be preview or commit", errs.Field("mode", "invalid", "preview or commit")))
		return
	}
	if req.Filename == "" {
		req.Filename = req.Entity + ".csv"
	}
	d, ok := e.Def(req.Entity)
	if !ok || d.CodeField == "" {
		httpx.WriteError(w, r, errs.Validation("invalid_entity", "unknown entity", errs.Field("entity", "invalid", "unknown or non-importable entity")))
		return
	}
	p := authz.From(ctx)
	var pidPtr *uuid.UUID
	if d.PropertyScoped {
		pid, ok := reqctx.Property(ctx)
		if !ok {
			httpx.WriteError(w, r, errs.BadRequest("property_required", "select a property (X-Property-Id header)"))
			return
		}
		pidPtr = &pid
		ctx = dbtx.WithScope(ctx, dbtx.Scope{UserID: p.UserID, PropertyIDs: []uuid.UUID{pid}})
	}
	if !p.Can(d.Perm+".import", pidPtr) {
		httpx.WriteError(w, r, errs.Forbidden("missing permission "+d.Perm+".import"))
		return
	}
	var res ImportResult
	err := e.DB.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		res, err = e.Import(ctx, tx, d, body)
		if err != nil {
			return err
		}
		res.Mode, res.Filename, res.CreatedAt = req.Mode, req.Filename, time.Now().UTC()
		if req.Mode == "preview" {
			return errPreviewRollback
		}
		iid := id.New()
		res.ID = &iid
		errsJSON, _ := json.Marshal(res.Errors)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.imports (id, entity, property_id, filename, mode, status, total_rows, inserted_rows,
			updated_rows, failed_rows, errors, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			iid, d.Key, pidPtr, req.Filename, req.Mode, res.Status, res.TotalRows, res.InsertedRows, res.UpdatedRows, res.FailedRows,
			errsJSON, id.Ptr(p.UserID)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: moduleOf(d.Perm), Action: audit.ActionImport, EntityType: "platform.import",
			EntityID: iid.String(), EntityLabel: d.Plural + " · " + req.Filename, PropertyID: pidPtr,
			After: map[string]any{"total": res.TotalRows, "inserted": res.InsertedRows, "updated": res.UpdatedRows, "failed": res.FailedRows}})
	})
	if err != nil && !errors.Is(err, errPreviewRollback) {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusCreated
	if req.Mode == "preview" {
		status = http.StatusOK
	}
	httpx.JSON(w, status, res)
}

func (e *Engine) listImports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []ImportResult{}
	err := e.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, entity, mode, filename, status, total_rows, inserted_rows, updated_rows, failed_rows, errors, created_at
			FROM platform.imports ORDER BY created_at DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x ImportResult
			var iid uuid.UUID
			if err := rows.Scan(&iid, &x.Entity, &x.Mode, &x.Filename, &x.Status, &x.TotalRows, &x.InsertedRows, &x.UpdatedRows,
				&x.FailedRows, &x.Errors, &x.CreatedAt); err != nil {
				return err
			}
			x.ID = &iid
			out = append(out, x)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[ImportResult]{Items: out})
}

func (e *Engine) importEntities(w http.ResponseWriter, r *http.Request) {
	out := []ImportEntity{}
	for _, d := range e.Defs() {
		if d.CodeField == "" {
			continue
		}
		ie := ImportEntity{Key: d.Key, Name: d.Name, Plural: d.Plural, Module: d.Module, PropertyScoped: d.PropertyScoped, CodeField: d.CodeField,
			Columns: []string{}, Required: []string{}}
		for _, f := range d.Fields {
			if f.ReadOnly {
				continue
			}
			ie.Columns = append(ie.Columns, f.Name)
			if f.Required && f.Default == nil {
				ie.Required = append(ie.Required, f.Name)
			}
		}
		out = append(out, ie)
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[ImportEntity]{Items: out})
}

// RegisterImports adds Settings → System Settings → Master Data Import.
func (e *Engine) RegisterImports(reg *route.Registry) {
	const tag = "Master Data Import"
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/imports/entities", Module: "platform", Tag: tag,
		Summary: "Importable entities and their columns", Permission: "platform.import.view", Response: ImportEntity{}, List: true, Handler: e.importEntities})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/imports", Module: "platform", Tag: tag,
		Summary: "Import history", Permission: "platform.import.view", Response: ImportResult{}, List: true, Handler: e.listImports})
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/platform/imports", Module: "platform", Tag: tag,
		Summary:     "Import CSV (preview or commit; idempotent by code)",
		Description: "Accepts JSON (csv field) or multipart/form-data (file, entity, mode). Property-scoped entities require X-Property-Id.",
		Permission:  "platform.import.create", Request: ImportRequest{}, Response: ImportResult{}, Handler: e.handleImport})
}
