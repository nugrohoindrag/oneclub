package reporting

// Self-service report builder (FR-BI-06, Should): curated datasets over the
// analytics store and the reporting read models with whitelisted
// dimensions, metrics and filters; each dataset has its own permission.
// Users pick a dataset, dimensions, metrics and filters, run it on the read
// replica within the query budget and may save the definition (private or
// shared with the property).

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// DatasetField is a dimension or metric of a dataset. Expr is SQL over the
// dataset source ({tz} = the club time zone); metrics aggregate.
type DatasetField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type" enum:"string,date,number"`
	Expr  string `json:"-"`
}

// Dataset is a self-service dataset.
type Dataset struct {
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Permission  string         `json:"-"`
	Module      string         `json:"module,omitempty"`
	Source      string         `json:"-"` // FROM clause (RLS narrows to the property)
	DateExpr    string         `json:"-"` // date of the period filter
	Dimensions  []DatasetField `json:"dimensions"`
	Metrics     []DatasetField `json:"metrics"`
}

// DatasetResult is a dataset query result.
type DatasetResult struct {
	Dataset    string           `json:"dataset"`
	From       string           `json:"from"`
	To         string           `json:"to"`
	Dimensions []string         `json:"dimensions"`
	Metrics    []string         `json:"metrics"`
	Columns    []Column         `json:"columns"`
	Rows       []map[string]any `json:"rows"`
	Truncated  bool             `json:"truncated"`
}

// DatasetDefinition is a saved report builder query.
type DatasetDefinition struct {
	Dimensions []string          `json:"dimensions"`
	Metrics    []string          `json:"metrics"`
	Filters    map[string]string `json:"filters,omitempty"`
	Period     string            `json:"period,omitempty" enum:"month_to_date,previous_month,year_to_date,last_30_days" doc:"Relative period; empty = from / to"`
	From       string            `json:"from,omitempty"`
	To         string            `json:"to,omitempty"`
}

// SavedReport is a saved report builder definition.
type SavedReport struct {
	ID         uuid.UUID         `json:"id" db:"id"`
	Name       string            `json:"name" db:"name"`
	Dataset    string            `json:"dataset" db:"dataset"`
	Definition DatasetDefinition `json:"definition" db:"definition"`
	Shared     bool              `json:"shared" db:"shared"`
	Mine       bool              `json:"mine" db:"mine"`
	CreatedAt  time.Time         `json:"createdAt" db:"created_at"`
	UpdatedAt  time.Time         `json:"updatedAt" db:"updated_at"`
}

// SavedReportRequest saves a definition.
type SavedReportRequest struct {
	Name       string            `json:"name"`
	Dataset    string            `json:"dataset"`
	Definition DatasetDefinition `json:"definition"`
	Shared     bool              `json:"shared,omitempty"`
}

// SavedReportUpdate changes a saved report.
type SavedReportUpdate struct {
	Name       *string            `json:"name,omitempty"`
	Definition *DatasetDefinition `json:"definition,omitempty"`
	Shared     *bool              `json:"shared,omitempty"`
}

func init() {
	day := DatasetField{Key: "day", Label: "Day", Type: "date", Expr: "day"}
	month := DatasetField{Key: "month", Label: "Month", Type: "string", Expr: "to_char(day, 'YYYY-MM')"}
	RegisterDataset(Dataset{Code: "revenue", Name: "Revenue", Module: "billing", Permission: PermDatasetRevenue,
		Description: "Revenue (charges without liabilities) per day, weekday, daypart, business line, revenue component, outlet and segment (analytics store)",
		Source:      "analytics.revenue_daily", DateExpr: "day",
		Dimensions: []DatasetField{day, month, {Key: "weekday", Label: "Weekday (1 = Monday)", Type: "string", Expr: "extract(isodow FROM day)::int::text"},
			{Key: "daypart", Label: "Daypart", Type: "string", Expr: "daypart"},
			{Key: "business_line", Label: "Business Line", Type: "string", Expr: "business_line"},
			{Key: "revenue_component", Label: "Revenue Component", Type: "string", Expr: "revenue_component"},
			{Key: "outlet", Label: "Outlet", Type: "string", Expr: "outlet"}, {Key: "segment", Label: "Segment", Type: "string", Expr: "segment"}},
		Metrics: []DatasetField{{Key: "amount", Label: "Revenue", Type: "number", Expr: "trim_scale(sum(amount))"},
			{Key: "lines", Label: "Charge Lines", Type: "number", Expr: "sum(lines)"}}})
	RegisterDataset(Dataset{Code: "kpi_daily", Name: "Executive KPIs (daily)", Permission: PermDatasetKPI,
		Description: "Daily values of the executive KPIs (analytics store); totals are meaningful for flow KPIs",
		Source:      "analytics.kpi_values WHERE grain = 'day'", DateExpr: "period_start",
		Dimensions: []DatasetField{{Key: "day", Label: "Day", Type: "date", Expr: "period_start"}, {Key: "month", Label: "Month", Type: "string",
			Expr: "to_char(period_start, 'YYYY-MM')"}, {Key: "kpi", Label: "KPI", Type: "string", Expr: "kpi_key"}},
		Metrics: []DatasetField{{Key: "total", Label: "Total", Type: "number", Expr: "trim_scale(sum(value))"},
			{Key: "average", Label: "Average", Type: "number", Expr: "trim_scale(round(avg(value), 4))"},
			{Key: "minimum", Label: "Minimum", Type: "number", Expr: "trim_scale(min(value))"},
			{Key: "maximum", Label: "Maximum", Type: "number", Expr: "trim_scale(max(value))"}}})
	RegisterDataset(Dataset{Code: "kpi_targets", Name: "KPI Target vs Actual (monthly)", Permission: PermDatasetKPI,
		Description: "Monthly actual of the executive KPIs against the approved target plan",
		Source: `(SELECT v.property_id, v.kpi_key, v.period_start, v.value, t.target FROM analytics.kpi_values v
			LEFT JOIN reporting.kpi_target_plans p ON p.property_id = v.property_id AND p.year = extract(year FROM v.period_start)::int AND p.status = 'approved'
			LEFT JOIN reporting.kpi_targets t ON t.plan_id = p.id AND t.kpi_key = v.kpi_key AND t.month = extract(month FROM v.period_start)::int
			WHERE v.grain = 'month') x`, DateExpr: "period_start",
		Dimensions: []DatasetField{{Key: "month", Label: "Month", Type: "string", Expr: "to_char(period_start, 'YYYY-MM')"},
			{Key: "kpi", Label: "KPI", Type: "string", Expr: "kpi_key"}},
		Metrics: []DatasetField{{Key: "actual", Label: "Actual", Type: "number", Expr: "trim_scale(sum(value))"},
			{Key: "target", Label: "Target", Type: "number", Expr: "trim_scale(sum(target))"},
			{Key: "achievement", Label: "Achievement", Type: "number", Expr: "trim_scale(round(sum(value) / nullif(sum(target), 0), 4))"}}})
	RegisterDataset(Dataset{Code: "golf_rounds", Name: "Golf Rounds", Module: "golf", Permission: "reporting.golf_round_history.view",
		Description: "Rounds played per day, player type and playing route",
		Source:      "reporting.golf_rounds", DateExpr: "play_date",
		Dimensions: []DatasetField{{Key: "day", Label: "Day", Type: "date", Expr: "play_date"}, {Key: "month", Label: "Month", Type: "string",
			Expr: "to_char(play_date, 'YYYY-MM')"}, {Key: "player_type", Label: "Player Type", Type: "string", Expr: "player_type"},
			{Key: "route", Label: "Playing Route", Type: "string", Expr: "coalesce(route_name, '')"}},
		Metrics: []DatasetField{{Key: "rounds", Label: "Rounds", Type: "number", Expr: "count(*)"},
			{Key: "average_gross", Label: "Average Gross", Type: "number", Expr: "trim_scale(round(avg(gross), 1))"},
			{Key: "average_minutes", Label: "Average Round (min)", Type: "number", Expr: "trim_scale(round(avg(round_minutes), 0))"}}})
	RegisterDataset(Dataset{Code: "pos_sales", Name: "POS Sales", Module: "commercial", Permission: "reporting.commercial_pos_sales.view",
		Description: "POS sales per day and outlet",
		Source:      "reporting.pos_sales", DateExpr: "(created_at AT TIME ZONE {tz})::date",
		Dimensions: []DatasetField{{Key: "day", Label: "Day", Type: "date", Expr: "(created_at AT TIME ZONE {tz})::date"},
			{Key: "month", Label: "Month", Type: "string", Expr: "to_char((created_at AT TIME ZONE {tz})::date, 'YYYY-MM')"},
			{Key: "outlet", Label: "Outlet", Type: "string", Expr: "outlet_name"}},
		Metrics: []DatasetField{{Key: "sales", Label: "Sales", Type: "number", Expr: "trim_scale(sum(total_amount))"},
			{Key: "net", Label: "Net", Type: "number", Expr: "trim_scale(sum(net_amount))"},
			{Key: "orders", Label: "Orders", Type: "number", Expr: "count(DISTINCT order_id)"}}})
}

func fieldByKey(fs []DatasetField, key string) (DatasetField, bool) {
	for _, f := range fs {
		if f.Key == key {
			return f, true
		}
	}
	return DatasetField{}, false
}

func (b *BI) datasetAllowed(ctx context.Context, d Dataset, property *uuid.UUID) bool {
	if d.Module != "" && !b.moduleEnabled(ctx, d.Module) {
		return false
	}
	return d.Permission == "" || authz.From(ctx).Can(d.Permission, property)
}

func (b *BI) listDatasets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Dataset{}
	pid, _ := handlePropertyPtr(ctx)
	for _, d := range datasets() {
		if b.datasetAllowed(ctx, d, pid) {
			out = append(out, d)
		}
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Dataset]{Items: out})
}

func handlePropertyPtr(ctx context.Context) (*uuid.UUID, bool) {
	p := handle.Property(ctx)
	if p == uuid.Nil {
		return nil, false
	}
	return &p, true
}

// resolveDefinition turns a relative period into dates.
func resolveDefinition(def DatasetDefinition, today time.Time) (time.Time, time.Time, error) {
	from, to := monthStart(today), today
	switch def.Period {
	case "previous_month":
		from = monthStart(today).AddDate(0, -1, 0)
		to = monthEnd(from)
	case "year_to_date":
		from = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	case "last_30_days":
		from = today.AddDate(0, 0, -29)
	case "", "month_to_date":
	default:
		return from, to, handle.Invalid("period", "invalid", "period must be month_to_date, previous_month, year_to_date or last_30_days")
	}
	if def.Period == "" {
		if def.From != "" {
			t, err := time.Parse(dateFmt, def.From)
			if err != nil {
				return from, to, handle.Invalid("from", "invalid", "from must be YYYY-MM-DD")
			}
			from = t
		}
		if def.To != "" {
			t, err := time.Parse(dateFmt, def.To)
			if err != nil {
				return from, to, handle.Invalid("to", "invalid", "to must be YYYY-MM-DD")
			}
			to = t
		}
	}
	if to.Before(from) {
		return from, to, handle.Invalid("to", "invalid", "to must be on or after from")
	}
	return from, to, nil
}

// validateDefinition checks dimensions, metrics and filters.
func validateDefinition(d Dataset, def DatasetDefinition) error {
	if len(def.Metrics) == 0 {
		return handle.Invalid("metrics", "required", "choose at least one metric")
	}
	if len(def.Dimensions) > 4 {
		return handle.Invalid("dimensions", "invalid", "choose at most 4 dimensions")
	}
	for _, k := range def.Dimensions {
		if _, ok := fieldByKey(d.Dimensions, k); !ok {
			return handle.Invalid("dimensions", "invalid", "unknown dimension "+k)
		}
	}
	for _, k := range def.Metrics {
		if _, ok := fieldByKey(d.Metrics, k); !ok {
			return handle.Invalid("metrics", "invalid", "unknown metric "+k)
		}
	}
	for k := range def.Filters {
		if _, ok := fieldByKey(d.Dimensions, k); !ok {
			return handle.Invalid("filters", "invalid", "unknown filter "+k)
		}
	}
	return nil
}

// runDataset runs a definition on tx (read replica) within the budget.
func (b *BI) runDataset(ctx context.Context, tx pgx.Tx, d Dataset, def DatasetDefinition) (DatasetResult, error) {
	property := handle.Property(ctx)
	pol := LoadBIPolicy(ctx, tx, property)
	out := DatasetResult{Dataset: d.Code, Dimensions: def.Dimensions, Metrics: def.Metrics, Columns: []Column{}, Rows: []map[string]any{}}
	if err := validateDefinition(d, def); err != nil {
		return out, err
	}
	loc := calendar.Location(ctx, tx)
	from, to, err := resolveDefinition(def, day(b.now().In(loc)))
	if err != nil {
		return out, err
	}
	out.From, out.To = from.Format(dateFmt), to.Format(dateFmt)
	if err := queryBudget(ctx, tx, pol.QueryTimeoutSeconds); err != nil {
		return out, err
	}
	tz := "$3"
	sub := func(s string) string { return strings.ReplaceAll(s, "{tz}", tz) }
	args := []any{out.From, out.To, loc.String()}
	var sel, group []string
	for i, k := range def.Dimensions {
		f, _ := fieldByKey(d.Dimensions, k)
		sel = append(sel, sub(f.Expr)+" AS d"+strconv.Itoa(i))
		group = append(group, strconv.Itoa(i+1))
		typ := "string"
		if f.Type == "date" {
			typ = "datetime"
		}
		out.Columns = append(out.Columns, Column{Key: k, Label: f.Label, Type: typ})
	}
	for i, k := range def.Metrics {
		f, _ := fieldByKey(d.Metrics, k)
		sel = append(sel, "("+sub(f.Expr)+")::text AS m"+strconv.Itoa(i))
		out.Columns = append(out.Columns, Column{Key: k, Label: f.Label, Type: "number"})
	}
	where := []string{sub(d.DateExpr) + " BETWEEN $1::date AND $2::date", "$3::text <> ''"}
	keys := make([]string, 0, len(def.Filters))
	for k := range def.Filters {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		f, _ := fieldByKey(d.Dimensions, k)
		args = append(args, def.Filters[k])
		where = append(where, "("+sub(f.Expr)+")::text = $"+strconv.Itoa(len(args)))
	}
	source := d.Source
	if strings.Contains(strings.ToUpper(source), " WHERE ") && !strings.HasPrefix(strings.TrimSpace(source), "(") {
		parts := strings.SplitN(source, " WHERE ", 2)
		source = parts[0]
		where = append(where, parts[1])
	}
	sql := "SELECT " + strings.Join(sel, ", ") + " FROM " + source + " WHERE " + strings.Join(where, " AND ")
	if len(group) > 0 {
		sql += " GROUP BY " + strings.Join(group, ", ") + " ORDER BY " + strings.Join(group, ", ")
	}
	sql += " LIMIT " + strconv.Itoa(pol.MaxDatasetRows+1)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return out, budgetError(err)
	}
	defer rows.Close()
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return out, budgetError(err)
		}
		m := map[string]any{}
		for i, k := range def.Dimensions {
			v := vals[i]
			if t, ok := v.(time.Time); ok {
				v = t.Format(dateFmt)
			}
			m[k] = v
		}
		for i, k := range def.Metrics {
			m[k] = vals[len(def.Dimensions)+i]
		}
		out.Rows = append(out.Rows, m)
	}
	if err := rows.Err(); err != nil {
		return out, budgetError(err)
	}
	if len(out.Rows) > pol.MaxDatasetRows {
		out.Rows, out.Truncated = out.Rows[:pol.MaxDatasetRows], true
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (b *BI) dataset(ctx context.Context, r *http.Request) (Dataset, error) {
	d, ok := datasetByCode(chi.URLParam(r, "code"))
	if !ok {
		return d, errs.NotFound("dataset")
	}
	if d.Module != "" && !b.moduleEnabled(ctx, d.Module) {
		return d, errs.NotFound("dataset")
	}
	property := handle.Property(ctx)
	if d.Permission != "" && !authz.From(ctx).Can(d.Permission, &property) {
		return d, errs.Forbidden("missing permission " + d.Permission)
	}
	return d, nil
}

func (b *BI) queryDataset(ctx context.Context, tx pgx.Tx, r *http.Request) (DatasetResult, error) {
	d, err := b.dataset(ctx, r)
	if err != nil {
		return DatasetResult{}, err
	}
	q := r.URL.Query()
	def := DatasetDefinition{Dimensions: splitList(q.Get("dimensions")), Metrics: splitList(q.Get("metrics")), Filters: map[string]string{},
		Period: q.Get("period"), From: q.Get("from"), To: q.Get("to")}
	for k, v := range q {
		if strings.HasPrefix(k, "filter[") && strings.HasSuffix(k, "]") && len(v) > 0 {
			def.Filters[k[7:len(k)-1]] = v[0]
		}
	}
	return b.runDataset(ctx, tx, d, def)
}

// ── saved reports ─────────────────────────────────────────────────────────

const savedSelect = `SELECT id, name, dataset, definition, shared, created_by = $2 AS mine, created_at, updated_at FROM reporting.saved_reports`

func (b *BI) listSaved(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SavedReport], error) {
	uid := handle.UserID(ctx)
	list, err := handle.List[SavedReport](tx.Query(ctx, savedSelect+` WHERE property_id = $1 AND (shared OR created_by = $2) ORDER BY name`,
		handle.Property(ctx), uid))
	if err != nil {
		return httpx.Page[SavedReport]{}, err
	}
	property := handle.Property(ctx)
	out := []SavedReport{}
	for _, s := range list {
		if d, ok := datasetByCode(s.Dataset); ok && b.datasetAllowed(ctx, d, &property) {
			out = append(out, s)
		}
	}
	return httpx.Page[SavedReport]{Items: out}, nil
}

func (b *BI) loadSaved(ctx context.Context, tx pgx.Tx, sid uuid.UUID, mine bool) (SavedReport, error) {
	uid := handle.UserID(ctx)
	where := ` WHERE id = $1 AND (shared OR created_by = $2)`
	if mine {
		where = ` WHERE id = $1 AND created_by = $2`
	}
	rows, err := tx.Query(ctx, savedSelect+where, sid, uid)
	return handle.One[SavedReport](rows, err, "saved report")
}

func (b *BI) createSaved(ctx context.Context, tx pgx.Tx, r *http.Request, req SavedReportRequest) (SavedReport, error) {
	property := handle.Property(ctx)
	d, ok := datasetByCode(req.Dataset)
	if !ok || !b.datasetAllowed(ctx, d, &property) {
		return SavedReport{}, handle.Invalid("dataset", "invalid", "unknown dataset")
	}
	if strings.TrimSpace(req.Name) == "" {
		return SavedReport{}, handle.Invalid("name", "required", "name is required")
	}
	if err := validateDefinition(d, req.Definition); err != nil {
		return SavedReport{}, err
	}
	if _, _, err := resolveDefinition(req.Definition, day(b.now())); err != nil {
		return SavedReport{}, err
	}
	sid := id.New()
	def, _ := json.Marshal(req.Definition)
	uid := handle.UserID(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO reporting.saved_reports (id, property_id, name, dataset, definition, shared, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, sid, property, req.Name, req.Dataset, def, req.Shared, uid); err != nil {
		return SavedReport{}, err
	}
	out, err := b.loadSaved(ctx, tx, sid, true)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionCreate, EntityType: "reporting.saved_report",
		EntityID: sid.String(), EntityLabel: out.Name, PropertyID: &property, After: out})
}

func (b *BI) updateSaved(ctx context.Context, tx pgx.Tx, r *http.Request, req SavedReportUpdate) (SavedReport, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return SavedReport{}, err
	}
	before, err := b.loadSaved(ctx, tx, sid, true)
	if err != nil {
		return before, err
	}
	name, def, shared := before.Name, before.Definition, before.Shared
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			return before, handle.Invalid("name", "required", "name is required")
		}
		name = *req.Name
	}
	if req.Definition != nil {
		d, _ := datasetByCode(before.Dataset)
		if err := validateDefinition(d, *req.Definition); err != nil {
			return before, err
		}
		def = *req.Definition
	}
	if req.Shared != nil {
		shared = *req.Shared
	}
	raw, _ := json.Marshal(def)
	if _, err := tx.Exec(ctx, `UPDATE reporting.saved_reports SET name = $2, definition = $3, shared = $4, updated_by = $5 WHERE id = $1`,
		sid, name, raw, shared, handle.UserID(ctx)); err != nil {
		return before, err
	}
	out, err := b.loadSaved(ctx, tx, sid, true)
	if err != nil {
		return out, err
	}
	property := handle.Property(ctx)
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionUpdate, EntityType: "reporting.saved_report",
		EntityID: sid.String(), EntityLabel: out.Name, PropertyID: &property, Before: before, After: out})
}

func (b *BI) deleteSaved(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (struct{}, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return struct{}{}, err
	}
	before, err := b.loadSaved(ctx, tx, sid, true)
	if err != nil {
		return struct{}{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM reporting.saved_reports WHERE id = $1`, sid); err != nil {
		return struct{}{}, err
	}
	property := handle.Property(ctx)
	return struct{}{}, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionDelete, EntityType: "reporting.saved_report",
		EntityID: sid.String(), EntityLabel: before.Name, PropertyID: &property, Before: before})
}

// runSaved runs a saved report.
func (b *BI) runSaved(ctx context.Context, tx pgx.Tx, r *http.Request) (DatasetResult, error) {
	sid, err := handle.ID(r)
	if err != nil {
		return DatasetResult{}, err
	}
	s, err := b.loadSaved(ctx, tx, sid, false)
	if err != nil {
		return DatasetResult{}, err
	}
	property := handle.Property(ctx)
	d, ok := datasetByCode(s.Dataset)
	if !ok || !b.datasetAllowed(ctx, d, &property) {
		return DatasetResult{}, errs.NotFound("saved report")
	}
	return b.runDataset(ctx, tx, d, s.Definition)
}

func (b *BI) registerDatasets(add func(route.Route)) {
	db := b.S.DB
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/datasets", Scope: route.ScopeProperty, Permission: PermDatasetView,
		Summary: "Datasets of the report builder I may use", Response: Dataset{}, List: true, Handler: b.listDatasets})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/datasets/{code}/query", Scope: route.ScopeProperty, Permission: PermDatasetView,
		Summary: "Run a report builder query (dimensions, metrics, filters; read replica, query budget)", Response: DatasetResult{},
		Query: []route.Param{{Name: "dimensions", Description: "Comma-separated"}, {Name: "metrics", Required: true, Description: "Comma-separated"},
			{Name: "period", Enum: []string{"month_to_date", "previous_month", "year_to_date", "last_30_days"}}, {Name: "from"}, {Name: "to"},
			{Name: "filter[...]", Description: "Dimension = value"}},
		Handler: replica(b, b.queryDataset)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/saved-reports", Scope: route.ScopeProperty, Permission: PermDatasetView,
		Summary: "Saved report builder reports (mine and shared)", Response: SavedReport{}, List: true, Handler: handle.Read(db, b.listSaved)})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reporting/saved-reports", Scope: route.ScopeProperty, Permission: PermSavedReportManage,
		Summary: "Save a report builder report", Request: SavedReportRequest{}, Response: SavedReport{}, Handler: handle.Write(db, http.StatusCreated, b.createSaved)})
	add(route.Route{Method: http.MethodPatch, Path: "/api/v1/reporting/saved-reports/{id}", Scope: route.ScopeProperty, Permission: PermSavedReportManage,
		Summary: "Change my saved report", Request: SavedReportUpdate{}, Response: SavedReport{}, Handler: handle.Write(db, http.StatusOK, b.updateSaved)})
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/reporting/saved-reports/{id}", Scope: route.ScopeProperty, Permission: PermSavedReportManage,
		Summary: "Delete my saved report", Handler: handle.Write(db, http.StatusNoContent, b.deleteSaved)})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/saved-reports/{id}/run", Scope: route.ScopeProperty, Permission: PermDatasetView,
		Summary: "Run a saved report", Response: DatasetResult{}, Handler: replica(b, b.runSaved)})
}
