package resource

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xuri/excelize/v2"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
)

// Engine serves resource definitions.
type Engine struct {
	DB   *dbtx.DB
	defs map[string]*Def
}

func NewEngine(db *dbtx.DB) *Engine { return &Engine{DB: db, defs: map[string]*Def{}} }

// Def returns a registered definition by key.
func (e *Engine) Def(key string) (*Def, bool) {
	d, ok := e.defs[key]
	return d, ok
}

// Defs returns all definitions sorted by key.
func (e *Engine) Defs() []*Def {
	out := make([]*Def, 0, len(e.defs))
	for _, d := range e.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func moduleOf(perm string) string { return strings.SplitN(perm, ".", 2)[0] }

// Register adds the routes for d.
func (e *Engine) Register(reg *route.Registry, d *Def) {
	if d.SchemaName == "" {
		d.SchemaName = strings.ReplaceAll(d.Name, " ", "")
	}
	e.defs[d.Key] = d
	scope := route.ScopeGlobal
	if d.PropertyScoped {
		scope = route.ScopeProperty
	}
	add := func(rt route.Route) {
		rt.Module = d.Module
		rt.Tag = d.Tag
		rt.Scope = scope
		reg.Add(rt)
	}
	var filters []route.Param
	filters = append(filters, route.Param{Name: "q", Description: "Search"}, route.Param{Name: "includeArchived", Type: "boolean"},
		route.Param{Name: "sort", Description: "Field name, prefix with - for descending"})
	for _, f := range d.Fields {
		if f.Filter {
			filters = append(filters, route.Param{Name: "filter[" + f.Name + "]"})
		}
	}
	out := d.Schema(false)
	in := d.Schema(true)
	add(route.Route{Method: http.MethodGet, Path: d.Path, Summary: "List " + d.Plural, Permission: d.Perm + ".view",
		ResponseSchema: out, SchemaName: d.SchemaName, List: true, Query: filters, Handler: e.list(d)})
	add(route.Route{Method: http.MethodGet, Path: d.Path + ":export", Summary: "Export " + d.Plural + " (CSV/XLSX)",
		Permission: d.Perm + ".export", RawContent: "text/csv",
		Query:   append([]route.Param{{Name: "format", Enum: []string{"csv", "xlsx"}}}, filters...),
		Handler: e.export(d)})
	add(route.Route{Method: http.MethodGet, Path: d.Path + "/{id}", Summary: "View " + d.Name, Permission: d.Perm + ".view",
		ResponseSchema: out, SchemaName: d.SchemaName, Handler: e.get(d)})
	add(route.Route{Method: http.MethodPost, Path: d.Path, Summary: "Add " + d.Name, Permission: d.Perm + ".create",
		RequestSchema: in, ResponseSchema: out, SchemaName: d.SchemaName, Idempotent: true, Handler: e.create(d)})
	patch := *in
	patch.Required = nil
	add(route.Route{Method: http.MethodPatch, Path: d.Path + "/{id}", Summary: "Edit " + d.Name, Permission: d.Perm + ".update",
		RequestSchema: &patch, ResponseSchema: out, SchemaName: d.SchemaName, Handler: e.update(d)})
	if !d.NoDelete {
		add(route.Route{Method: http.MethodDelete, Path: d.Path + "/{id}", Summary: "Delete " + d.Name + " (only when unused; otherwise set Inactive)",
			Permission: d.Perm + ".delete", Handler: e.remove(d)})
	}
}

// ── read ──────────────────────────────────────────────────────────────────

type listQuery struct {
	where []string
	args  []any
}

func (q *listQuery) add(cond string, v any) {
	q.args = append(q.args, v)
	q.where = append(q.where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(q.args))))
}

func (e *Engine) buildList(ctx context.Context, d *Def, lp httpx.ListParams, includeArchived bool) (*listQuery, string, error) {
	q := &listQuery{where: []string{"true"}}
	if d.PropertyScoped {
		if pid, ok := reqctx.Property(ctx); ok {
			q.add("property_id = ?", pid)
		}
	}
	if d.Archive && !includeArchived {
		q.where = append(q.where, "archived_at IS NULL")
	}
	for name, v := range lp.Filters {
		f, ok := d.field(name)
		if !ok || (!f.Filter && name != "status") {
			return nil, "", errs.BadRequest("invalid_filter", "unsupported filter "+name)
		}
		vals := strings.Split(v, ",")
		q.add(f.Column+"::text = ANY(?)", vals)
	}
	if lp.Q != "" {
		var ors []string
		for _, f := range d.Fields {
			if f.Search {
				ors = append(ors, f.Column+"::text ILIKE ?")
			}
		}
		if len(ors) > 0 {
			q.args = append(q.args, "%"+lp.Q+"%")
			n := "$" + strconv.Itoa(len(q.args))
			q.where = append(q.where, "("+strings.ReplaceAll(strings.Join(ors, " OR "), "?", n)+")")
		}
	}
	order := d.OrderBy
	if order == "" {
		order = "created_at DESC, id DESC"
	}
	if lp.Sort != "" {
		name := strings.TrimPrefix(lp.Sort, "-")
		dir := "ASC"
		if strings.HasPrefix(lp.Sort, "-") {
			dir = "DESC"
		}
		col := map[string]string{"createdAt": "created_at", "updatedAt": "updated_at"}[name]
		if f, ok := d.field(name); ok {
			col = f.Column
		}
		if col == "" {
			return nil, "", errs.BadRequest("invalid_sort", "unsupported sort "+name)
		}
		order = col + " " + dir + ", id " + dir
	}
	return q, order, nil
}

func (e *Engine) list(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		lp := httpx.ParseList(r)
		offset := 0
		if lp.Cursor != "" {
			c, err := httpx.DecodeCursor(lp.Cursor)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			offset, _ = strconv.Atoi(c)
		}
		q, order, err := e.buildList(ctx, d, lp, r.URL.Query().Get("includeArchived") == "true")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var rows []map[string]any
		err = e.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
			sql := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %d OFFSET %d",
				d.selectList(), d.Table, strings.Join(q.where, " AND "), order, lp.PageSize+1, offset)
			rs, err := tx.Query(ctx, sql, q.args...)
			if err != nil {
				return err
			}
			rows, err = d.scanRows(rs)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d.afterRead(ctx, rows)
		page := httpx.Page[map[string]any]{Items: rows}
		if page.Items == nil {
			page.Items = []map[string]any{}
		}
		if len(rows) > lp.PageSize {
			page.Items = rows[:lp.PageSize]
			page.NextCursor = httpx.EncodeCursor(strconv.Itoa(offset + lp.PageSize))
		}
		httpx.JSON(w, http.StatusOK, page)
	}
}

// Get loads one row by id within tx (RLS applies).
func (e *Engine) Get(ctx context.Context, tx pgx.Tx, d *Def, rid uuid.UUID, forUpdate bool) (map[string]any, error) {
	sql := fmt.Sprintf("SELECT %s FROM %s WHERE id = $1", d.selectList(), d.Table)
	args := []any{rid}
	if d.PropertyScoped {
		if pid, ok := reqctx.Property(ctx); ok {
			sql += " AND property_id = $2"
			args = append(args, pid)
		}
	}
	if forUpdate {
		sql += " FOR UPDATE"
	}
	rs, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	rows, err := d.scanRows(rs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errs.NotFound(strings.ToLower(d.Name))
	}
	return rows[0], nil
}

func (e *Engine) get(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var row map[string]any
		err = e.DB.WithReadTx(r.Context(), func(tx pgx.Tx) error {
			row, err = e.Get(r.Context(), tx, d, rid, false)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d.afterRead(r.Context(), []map[string]any{row})
		httpx.JSON(w, http.StatusOK, row)
	}
}

func (d *Def) afterRead(ctx context.Context, rows []map[string]any) {
	if d.Hooks.AfterRead == nil {
		return
	}
	for _, row := range rows {
		d.Hooks.AfterRead(ctx, row)
	}
}

// ── write ─────────────────────────────────────────────────────────────────

// prepare validates input against the definition. creating=true enforces
// required fields and defaults.
func (e *Engine) prepare(ctx context.Context, tx pgx.Tx, d *Def, input map[string]any, creating bool) (map[string]any, error) {
	vals := map[string]any{}
	var fields []errs.FieldError
	for k := range input {
		f, ok := d.field(k)
		if !ok || f.ReadOnly {
			if k == "id" || k == "propertyId" || k == "createdAt" || k == "updatedAt" || k == "archivedAt" {
				continue
			}
			fields = append(fields, errs.Field(k, "unknown_field", "field is not allowed"))
			continue
		}
		if !creating && f.CreateOnly {
			fields = append(fields, errs.Field(k, "immutable", "cannot be changed after creation"))
			continue
		}
		v, fe := coerce(f, input[k])
		if fe != nil {
			fields = append(fields, *fe)
			continue
		}
		vals[k] = v
	}
	if creating {
		for i := range d.Fields {
			f := &d.Fields[i]
			if f.ReadOnly {
				continue
			}
			if _, ok := vals[f.Name]; !ok || vals[f.Name] == nil {
				if f.Default != nil {
					vals[f.Name] = f.Default
				} else if f.Required {
					fields = append(fields, errs.Field(f.Name, "required", f.Label+" is required"))
				}
			}
		}
	} else {
		for k, v := range vals {
			if f, _ := d.field(k); f.Required && v == nil {
				fields = append(fields, errs.Field(k, "required", f.Label+" is required"))
			}
		}
	}
	if len(fields) > 0 {
		return nil, errs.Validation("invalid_"+strings.ReplaceAll(strings.ToLower(d.Name), " ", "_"), "invalid "+strings.ToLower(d.Name), fields...)
	}
	// references
	pid, hasProp := reqctx.Property(ctx)
	for k, v := range vals {
		f, _ := d.field(k)
		if f.Ref == nil || v == nil {
			continue
		}
		sql := "SELECT EXISTS (SELECT 1 FROM " + f.Ref.Table + " WHERE id = $1::uuid"
		args := []any{v}
		if f.Ref.SameProperty && hasProp {
			sql += " AND property_id = $2"
			args = append(args, pid)
		}
		var ok bool
		if err := tx.QueryRow(ctx, sql+")", args...).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			label := f.Ref.Label
			if label == "" {
				label = f.Label
			}
			fields = append(fields, errs.Field(k, "not_found", label+" not found in this property"))
		}
	}
	if len(fields) > 0 {
		return nil, errs.Validation("invalid_reference", "invalid reference", fields...)
	}
	return vals, nil
}

func (e *Engine) mapWriteErr(d *Def, err error) error {
	if ok, constraint := dbtx.IsUniqueViolation(err); ok {
		for _, f := range d.Fields {
			if strings.Contains(constraint, f.Column) && f.Name != "propertyId" {
				return errs.Validation("duplicate", f.Label+" already exists", errs.Field(f.Name, "taken", f.Label+" already exists"))
			}
		}
		return errs.Conflict("duplicate", "a "+strings.ToLower(d.Name)+" with the same key already exists")
	}
	if ok, c := dbtx.IsCheckViolation(err); ok {
		return errs.Validation("check_failed", "value not allowed ("+c+")")
	}
	if dbtx.IsForeignKeyViolation(err) {
		return errs.Validation("invalid_reference", "a referenced record does not exist")
	}
	if dbtx.IsInsufficientPrivilege(err) {
		return errs.Forbidden("not allowed for this property")
	}
	return err
}

// CreateRow inserts a row and records audit (used by HTTP and import).
func (e *Engine) CreateRow(ctx context.Context, tx pgx.Tx, d *Def, input map[string]any) (map[string]any, error) {
	vals, err := e.prepare(ctx, tx, d, input, true)
	if err != nil {
		return nil, err
	}
	if d.Hooks.BeforeWrite != nil {
		if err := d.Hooks.BeforeWrite(ctx, tx, vals, nil); err != nil {
			return nil, err
		}
	}
	rid := id.New()
	cols := []string{"id"}
	ph := []string{"$1"}
	args := []any{rid}
	if d.PropertyScoped {
		pid, ok := reqctx.Property(ctx)
		if !ok {
			return nil, errs.BadRequest("property_required", "select a property")
		}
		args = append(args, pid)
		cols = append(cols, "property_id")
		ph = append(ph, "$"+strconv.Itoa(len(args)))
	}
	uid := id.Ptr(authzUser(ctx))
	args = append(args, uid)
	cols = append(cols, "created_by", "updated_by")
	n := "$" + strconv.Itoa(len(args))
	ph = append(ph, n, n)
	for _, f := range d.Fields {
		v, ok := vals[f.Name]
		if !ok {
			continue
		}
		args = append(args, v)
		cols = append(cols, f.Column)
		ph = append(ph, "$"+strconv.Itoa(len(args))+cast(&f))
	}
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", d.Table, strings.Join(cols, ", "), strings.Join(ph, ", "))
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return nil, e.mapWriteErr(d, err)
	}
	row, err := e.Get(ctx, tx, d, rid, false)
	if err != nil {
		return nil, err
	}
	if d.Hooks.AfterCreate != nil {
		if err := d.Hooks.AfterCreate(ctx, tx, row); err != nil {
			return nil, err
		}
		if row, err = e.Get(ctx, tx, d, rid, false); err != nil {
			return nil, err
		}
	}
	return row, audit.Record(ctx, tx, audit.Entry{Module: moduleOf(d.Perm), Action: audit.ActionCreate, EntityType: d.Key,
		EntityID: rid.String(), EntityLabel: d.entityLabel(row), PropertyID: propOf(row), After: row})
}

// UpdateRow updates a row and records audit (status changes are audited as
// status_change).
func (e *Engine) UpdateRow(ctx context.Context, tx pgx.Tx, d *Def, rid uuid.UUID, input map[string]any) (map[string]any, error) {
	before, err := e.Get(ctx, tx, d, rid, true)
	if err != nil {
		return nil, err
	}
	vals, err := e.prepare(ctx, tx, d, input, false)
	if err != nil {
		return nil, err
	}
	if d.Hooks.BeforeWrite != nil {
		if err := d.Hooks.BeforeWrite(ctx, tx, vals, before); err != nil {
			return nil, err
		}
	}
	if len(vals) == 0 {
		return before, nil
	}
	args := []any{rid, id.Ptr(authzUser(ctx))}
	sets := []string{"updated_by = $2"}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, _ := d.field(k)
		args = append(args, vals[k])
		sets = append(sets, fmt.Sprintf("%s = $%d%s", f.Column, len(args), cast(f)))
	}
	sql := fmt.Sprintf("UPDATE %s SET %s WHERE id = $1", d.Table, strings.Join(sets, ", "))
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return nil, e.mapWriteErr(d, err)
	}
	after, err := e.Get(ctx, tx, d, rid, false)
	if err != nil {
		return nil, err
	}
	if d.Hooks.AfterUpdate != nil {
		if err := d.Hooks.AfterUpdate(ctx, tx, before, after); err != nil {
			return nil, err
		}
		if after, err = e.Get(ctx, tx, d, rid, false); err != nil {
			return nil, err
		}
	}
	action := audit.ActionUpdate
	if changed := audit.Diff(before, after); len(changed) <= 2 && before["status"] != after["status"] {
		action = audit.ActionStatusChange
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: moduleOf(d.Perm), Action: action, EntityType: d.Key,
		EntityID: rid.String(), EntityLabel: d.entityLabel(after), PropertyID: propOf(after), Before: before, After: after,
		Metadata: map[string]any{"changed": audit.Diff(before, after)}})
}

func propOf(row map[string]any) *uuid.UUID {
	if s, ok := row["propertyId"].(string); ok {
		if u, err := uuid.Parse(s); err == nil {
			return &u
		}
	}
	return nil
}

func authzUser(ctx context.Context) uuid.UUID {
	if p := authz.From(ctx); p != nil {
		return p.UserID
	}
	return uuid.Nil
}

func (e *Engine) create(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		if err := httpx.Decode(r, &input); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var row map[string]any
		err := e.DB.WithTx(r.Context(), func(tx pgx.Tx) error {
			var err error
			row, err = e.CreateRow(r.Context(), tx, d, input)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d.afterRead(r.Context(), []map[string]any{row})
		httpx.JSON(w, http.StatusCreated, row)
	}
}

func (e *Engine) update(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var input map[string]any
		if err := httpx.Decode(r, &input); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var row map[string]any
		err = e.DB.WithTx(r.Context(), func(tx pgx.Tx) error {
			row, err = e.UpdateRow(r.Context(), tx, d, rid, input)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d.afterRead(r.Context(), []map[string]any{row})
		httpx.JSON(w, http.StatusOK, row)
	}
}

// remove deletes a row only when nothing references it (FR-ORG-05). The
// delete is first attempted inside a savepoint; a foreign key violation
// means the entity is in use and must be set Inactive instead. Archivable
// entities are then soft-deleted (archived_at) to keep history.
func (e *Engine) remove(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid, err := httpx.PathUUID(r, "id")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := r.Context()
		err = e.DB.WithTx(ctx, func(tx pgx.Tx) error {
			before, err := e.Get(ctx, tx, d, rid, true)
			if err != nil {
				return err
			}
			if before["archivedAt"] != nil {
				return errs.NotFound(strings.ToLower(d.Name))
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err := sp.Exec(ctx, "DELETE FROM "+d.Table+" WHERE id = $1", rid); err != nil {
				_ = sp.Rollback(ctx)
				if dbtx.IsForeignKeyViolation(err) {
					return errs.Conflict("in_use", d.Name+" is already used and cannot be deleted; set it Inactive instead")
				}
				return err
			}
			action := audit.ActionDelete
			if d.Archive {
				if err := sp.Rollback(ctx); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "UPDATE "+d.Table+" SET archived_at = now(), updated_by = $2 WHERE id = $1", rid, id.Ptr(authzUser(ctx))); err != nil {
					return err
				}
				action = audit.ActionArchive
			} else if err := sp.Commit(ctx); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Entry{Module: moduleOf(d.Perm), Action: action, EntityType: d.Key,
				EntityID: rid.String(), EntityLabel: d.entityLabel(before), PropertyID: propOf(before), Before: before})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.NoContent(w)
	}
}

// ── export (FR-MD-07) ─────────────────────────────────────────────────────

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	default:
		return fmt.Sprint(t)
	}
}

// ExportColumns returns the export header (JSON names).
func (d *Def) ExportColumns() []string {
	cols := []string{"id"}
	if d.PropertyScoped {
		cols = append(cols, "propertyId")
	}
	for _, f := range d.Fields {
		cols = append(cols, f.Name)
	}
	return append(cols, "createdAt", "updatedAt")
}

func (e *Engine) export(d *Def) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "csv"
		}
		if format != "csv" && format != "xlsx" {
			httpx.WriteError(w, r, errs.BadRequest("invalid_format", "format must be csv or xlsx"))
			return
		}
		lp := httpx.ParseList(r)
		q, order, err := e.buildList(ctx, d, lp, r.URL.Query().Get("includeArchived") == "true")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var rows []map[string]any
		err = e.DB.WithTx(ctx, func(tx pgx.Tx) error {
			rs, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT 100000",
				d.selectList(), d.Table, strings.Join(q.where, " AND "), order), q.args...)
			if err != nil {
				return err
			}
			if rows, err = d.scanRows(rs); err != nil {
				return err
			}
			d.afterRead(ctx, rows)
			// The export action itself is audited (FR-AUD-05 spirit for master data).
			return audit.Record(ctx, tx, audit.Entry{Module: moduleOf(d.Perm), Action: audit.ActionExport, EntityType: d.Key,
				EntityLabel: d.Plural, Metadata: map[string]any{"format": format, "rows": len(rows), "filters": lp.Filters, "q": lp.Q}})
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		cols := d.ExportColumns()
		name := strings.ReplaceAll(strings.ToLower(d.Plural), " ", "-") + "-" + time.Now().UTC().Format("20060102-150405")
		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
			cw := csv.NewWriter(w)
			_ = cw.Write(cols)
			for _, row := range rows {
				rec := make([]string, len(cols))
				for i, c := range cols {
					rec[i] = cell(row[c])
				}
				_ = cw.Write(rec)
			}
			cw.Flush()
			return
		}
		f := excelize.NewFile()
		defer f.Close()
		sheet := "Sheet1"
		for i, c := range cols {
			ref, _ := excelize.CoordinatesToCellName(i+1, 1)
			_ = f.SetCellValue(sheet, ref, c)
		}
		for ri, row := range rows {
			for ci, c := range cols {
				ref, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
				_ = f.SetCellValue(sheet, ref, cell(row[c]))
			}
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.xlsx"`)
		_ = f.Write(w)
	}
}
