// Package reporting is Management & BI (EP-10): a report registry with
// per-report permission, queries routed to the read replica (FR-REP-01),
// asynchronous CSV/XLSX export with a notification when ready (FR-REP-03),
// and the Management Dashboard Executive Overview (FR-REP-04).
package reporting

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/xuri/excelize/v2"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/config"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/storage"
)

// Column describes a report column.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type" enum:"string,number,datetime,boolean"`
}

// Param describes a report parameter.
type Param struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Type  string   `json:"type" enum:"string,date,uuid,enum"`
	Enum  []string `json:"enum,omitempty"`
}

// Report is a registered report (FR-REP-02).
type Report struct {
	Code        string
	Name        string // "[Business Domain] [Report Type]" (Naming Convention §32)
	Module      string
	Permission  string
	Description string
	Columns     []Column
	Params      []Param
	// Query returns rows as maps keyed by column key. limit <= 0 = no limit.
	Query func(ctx context.Context, tx pgx.Tx, params map[string]string, limit int) ([]map[string]any, error)
}

// ReportInfo is the API view of a report.
type ReportInfo struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Module      string   `json:"module"`
	Description string   `json:"description"`
	Columns     []Column `json:"columns"`
	Params      []Param  `json:"params"`
}

// Result is a synchronous report run.
type Result struct {
	Report      ReportInfo       `json:"report"`
	Rows        []map[string]any `json:"rows"`
	Truncated   bool             `json:"truncated"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Source      string           `json:"source" enum:"replica,primary"`
}

// Export is an asynchronous export request.
type Export struct {
	ID          uuid.UUID  `json:"id"`
	ReportCode  string     `json:"reportCode"`
	Format      string     `json:"format" enum:"csv,xlsx"`
	Status      string     `json:"status" enum:"pending,completed,failed"`
	RowCount    *int       `json:"rowCount"`
	FileURL     *string    `json:"fileUrl"`
	Error       *string    `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt"`
}

type ExportRequest struct {
	ReportCode string            `json:"reportCode"`
	Format     string            `json:"format" enum:"csv,xlsx"`
	Params     map[string]string `json:"params,omitempty"`
}

// Service is the reporting module.
type Service struct {
	DB     *dbtx.DB
	Jobs   *jobs.Client
	Files  *storage.Files
	Notify notify.Sender
	Cfg    *config.Config
	// Location is the instance time zone (the golf day boundary).
	Location func() *time.Location
	reports  []*Report
}

// Add registers a report.
func (s *Service) Add(r *Report) { s.reports = append(s.reports, r) }

func (s *Service) find(code string) (*Report, bool) {
	for _, r := range s.reports {
		if r.Code == code {
			return r, true
		}
	}
	return nil, false
}

// Seeds returns registry rows for catalogue sync.
func (s *Service) Seeds() []provision.ReportDef {
	var out []provision.ReportDef
	for _, r := range s.reports {
		var cols, params []map[string]any
		for _, c := range r.Columns {
			cols = append(cols, map[string]any{"key": c.Key, "label": c.Label, "type": c.Type})
		}
		for _, p := range r.Params {
			params = append(params, map[string]any{"key": p.Key, "label": p.Label, "type": p.Type})
		}
		out = append(out, provision.ReportDef{Code: r.Code, Name: r.Name, Module: r.Module, Permission: r.Permission,
			Description: r.Description, Columns: cols, Parameters: params})
	}
	return out
}

func info(r *Report) ReportInfo {
	params := r.Params
	if params == nil {
		params = []Param{}
	}
	return ReportInfo{Code: r.Code, Name: r.Name, Module: r.Module, Description: r.Description, Columns: r.Columns, Params: params}
}

// scoped narrows the RLS scope to where the report permission is held.
func scoped(ctx context.Context, r *Report) (context.Context, error) {
	p := authz.From(ctx)
	if !p.Can(r.Permission, nil) {
		return ctx, errs.Forbidden("missing permission " + r.Permission)
	}
	all, ids := p.PropertiesFor(r.Permission)
	return dbtx.WithScope(ctx, dbtx.Scope{AllProperties: all, PropertyIDs: ids, UserID: p.UserID}), nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p := authz.From(r.Context())
	out := []ReportInfo{}
	for _, rep := range s.reports {
		if p.Can(rep.Permission, nil) {
			out = append(out, info(rep))
		}
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[ReportInfo]{Items: out})
}

func params(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.URL.Query() {
		if strings.HasPrefix(k, "params[") && strings.HasSuffix(k, "]") && len(v) > 0 {
			out[k[7:len(k)-1]] = v[0]
		}
	}
	return out
}

// run executes a report synchronously on the read replica (FR-REP-01).
func (s *Service) run(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.find(chi.URLParam(r, "code"))
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("report"))
		return
	}
	ctx, err := scoped(r.Context(), rep)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	const limit = 1000
	var rows []map[string]any
	err = s.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
		var err error
		rows, err = rep.Query(ctx, tx, params(r), limit+1)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res := Result{Report: info(rep), Rows: rows, GeneratedAt: time.Now().UTC(), Source: "replica"}
	if s.DB.Replica == s.DB.Primary {
		res.Source = "primary"
	}
	if res.Rows == nil {
		res.Rows = []map[string]any{}
	}
	if len(res.Rows) > limit {
		res.Rows, res.Truncated = res.Rows[:limit], true
	}
	httpx.JSON(w, http.StatusOK, res)
}

const exportSelect = `SELECT e.id, e.report_code, e.format, e.status, e.row_count, e.file_id, e.error, e.created_at, e.completed_at FROM reporting.exports e`

func scanExport(row pgx.Row) (Export, error) {
	var e Export
	var fid *uuid.UUID
	err := row.Scan(&e.ID, &e.ReportCode, &e.Format, &e.Status, &e.RowCount, &fid, &e.Error, &e.CreatedAt, &e.CompletedAt)
	if fid != nil {
		u := storage.URLFor(*fid)
		e.FileURL = &u
	}
	return e, err
}

func (s *Service) createExport(w http.ResponseWriter, r *http.Request) {
	var req ExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.Format == "" {
		req.Format = "csv"
	}
	if req.Format != "csv" && req.Format != "xlsx" {
		httpx.WriteError(w, r, errs.Validation("invalid_format", "format must be csv or xlsx", errs.Field("format", "invalid", "csv or xlsx")))
		return
	}
	rep, ok := s.find(req.ReportCode)
	if !ok {
		httpx.WriteError(w, r, errs.Validation("invalid_report", "unknown report", errs.Field("reportCode", "invalid", "unknown report")))
		return
	}
	ctx, err := scoped(r.Context(), rep)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p := authz.From(ctx)
	var out Export
	err = s.DB.WithTx(ctx, func(tx pgx.Tx) error {
		eid := id.New()
		if req.Params == nil {
			req.Params = map[string]string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO reporting.exports (id, report_code, format, parameters, status, requested_by)
			VALUES ($1,$2,$3,$4,'pending',$5)`, eid, rep.Code, req.Format, req.Params, p.UserID); err != nil {
			return err
		}
		if _, err := s.Jobs.Insert(ctx, tx, ExportArgs{ExportID: eid}, nil); err != nil {
			return err
		}
		var err error
		if out, err = scanExport(tx.QueryRow(ctx, exportSelect+" WHERE e.id = $1", eid)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionExport, EntityType: "reporting.export",
			EntityID: eid.String(), EntityLabel: rep.Name, After: map[string]any{"format": req.Format, "params": req.Params}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

func (s *Service) listExports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := []Export{}
	err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, exportSelect+" WHERE e.requested_by = $1 ORDER BY e.created_at DESC LIMIT 100", authz.From(ctx).UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanExport(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[Export]{Items: out})
}

// ── async export job ──────────────────────────────────────────────────────

type ExportArgs struct {
	ExportID uuid.UUID `json:"exportId"`
}

func (ExportArgs) Kind() string { return "report_export" }

func (ExportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueDefault, MaxAttempts: 3}
}

// ExportWorker produces the file under the requester's permissions.
type ExportWorker struct {
	river.WorkerDefaults[ExportArgs]
	Svc       *Service
	LoadAuthz func(ctx context.Context, userID uuid.UUID) (*authz.Principal, error)
}

func (wk *ExportWorker) Timeout(*river.Job[ExportArgs]) time.Duration { return 10 * time.Minute }

func (wk *ExportWorker) Work(ctx context.Context, job *river.Job[ExportArgs]) error {
	s := wk.Svc
	sys := dbtx.System(ctx)
	var code, format string
	var prm map[string]string
	var user uuid.UUID
	var status string
	if err := s.DB.Primary.QueryRow(sys, `SELECT report_code, format, parameters, requested_by, status FROM reporting.exports WHERE id = $1`,
		job.Args.ExportID).Scan(&code, &format, &prm, &user, &status); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	rep, ok := s.find(code)
	fail := func(msg string) error {
		_, err := s.DB.Primary.Exec(sys, `UPDATE reporting.exports SET status = 'failed', error = $2, completed_at = now() WHERE id = $1`, job.Args.ExportID, msg)
		if err != nil {
			return err
		}
		return s.DB.WithTx(sys, func(tx pgx.Tx) error {
			return s.Notify.Send(sys, tx, notify.Message{Event: "report.export_failed", Category: "report", UserIDs: []uuid.UUID{user},
				Data: map[string]any{"reportName": code, "error": msg}})
		})
	}
	if !ok {
		return fail("report no longer exists")
	}
	p, err := wk.LoadAuthz(ctx, user)
	if err != nil {
		return err
	}
	uctx, err := scoped(authz.WithPrincipal(ctx, p), rep)
	if err != nil {
		return fail("you no longer have access to this report")
	}
	var rows []map[string]any
	if err := s.DB.WithReportTx(uctx, func(tx pgx.Tx) error {
		var err error
		rows, err = rep.Query(uctx, tx, prm, 100000)
		return err
	}); err != nil {
		return err
	}
	var buf bytes.Buffer
	ctype := "text/csv"
	switch format {
	case "xlsx":
		ctype = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		f := excelize.NewFile()
		for i, c := range rep.Columns {
			ref, _ := excelize.CoordinatesToCellName(i+1, 1)
			_ = f.SetCellValue("Sheet1", ref, c.Label)
		}
		for ri, row := range rows {
			for ci, c := range rep.Columns {
				ref, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
				_ = f.SetCellValue("Sheet1", ref, cellValue(row[c.Key]))
			}
		}
		if err := f.Write(&buf); err != nil {
			return err
		}
		_ = f.Close()
	default:
		cw := csv.NewWriter(&buf)
		hdr := make([]string, len(rep.Columns))
		for i, c := range rep.Columns {
			hdr[i] = c.Label
		}
		_ = cw.Write(hdr)
		for _, row := range rows {
			rec := make([]string, len(rep.Columns))
			for i, c := range rep.Columns {
				rec[i] = fmt.Sprint(cellValue(row[c.Key]))
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
	}
	name := strings.ReplaceAll(strings.ToLower(rep.Name), " ", "-") + "-" + time.Now().UTC().Format("20060102-150405") + "." + format
	pctx := authz.WithPrincipal(sys, p)
	return s.DB.WithTx(sys, func(tx pgx.Tx) error {
		f, err := s.Files.Save(pctx, tx, name, ctype, "export", false, bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(sys, `UPDATE reporting.exports SET status = 'completed', file_id = $2, row_count = $3, completed_at = now() WHERE id = $1`,
			job.Args.ExportID, f.ID, len(rows)); err != nil {
			return err
		}
		return s.Notify.Send(sys, tx, notify.Message{Event: "report.export_ready", Category: "report", UserIDs: []uuid.UUID{user},
			Link: s.Cfg.PublicBaseURL + "/reports/exports",
			Data: map[string]any{"reportName": rep.Name, "format": strings.ToUpper(format), "rowCount": len(rows), "link": s.Cfg.PublicBaseURL + "/reports/exports"}})
	})
}

func cellValue(v any) any {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case *time.Time:
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	return v
}

// ── Executive Overview (FR-REP-04) ────────────────────────────────────────

type Widget struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Module string `json:"module"`
	Status string `json:"status" enum:"available,coming_soon"`
	Value  *int64 `json:"value"`
	Phase  string `json:"phase"`
}

type Dashboard struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	GeneratedAt time.Time `json:"generatedAt"`
	Widgets     []Widget  `json:"widgets"`
}

// executiveOverview returns platform counts and the live golf KPIs
// (Naming Convention §23).
func (s *Service) executiveOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := Dashboard{Code: "executive_overview", Name: "Executive Overview", GeneratedAt: time.Now().UTC(), Widgets: []Widget{}}
	var users, props, venues, pending int64
	day := time.Now()
	if s.Location != nil {
		day = day.In(s.Location())
	}
	var golf []Widget
	err := s.DB.WithReportTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT (SELECT count(DISTINCT user_id) FROM reporting.user_access WHERE user_status = 'active'),
			(SELECT count(*) FROM platform.properties WHERE status = 'active' AND archived_at IS NULL),
			(SELECT count(*) FROM reporting.venue_directory WHERE status = 'active'),
			(SELECT count(*) FROM reporting.venue_directory WHERE status = 'pending')`).Scan(&users, &props, &venues, &pending)
		if err != nil {
			return err
		}
		live, err := GolfTodayValues(ctx, tx, nil, day, nil)
		if err != nil {
			return err
		}
		exec, err := GolfExecutive(ctx, tx, day)
		if err != nil {
			return err
		}
		for _, wd := range live {
			switch wd.Key {
			case "todays_bookings", "todays_players", "players_on_course":
				golf = append(golf, wd)
			}
		}
		golf = append(golf, exec...)
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d.Widgets = append(d.Widgets,
		Widget{Key: "active_users", Label: "Active Users", Module: "platform", Status: "available", Value: &users, Phase: "P0"},
		Widget{Key: "active_properties", Label: "Active Properties", Module: "platform", Status: "available", Value: &props, Phase: "P0"},
		Widget{Key: "active_venues", Label: "Active Venues", Module: "platform", Status: "available", Value: &venues, Phase: "P0"},
		Widget{Key: "venues_pending_activation", Label: "Venues Pending Activation", Module: "platform", Status: "available", Value: &pending, Phase: "P0"},
	)
	d.Widgets = append(d.Widgets, golf...)
	httpx.JSON(w, http.StatusOK, d)
}

// Register adds reporting routes (PRD §10 Reporting).
func (s *Service) Register(reg *route.Registry) {
	const tag = "Reports"
	add := func(rt route.Route) { rt.Module = "reporting"; rt.Tag = tag; reg.Add(rt) }
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/reports", Summary: "Reports I can run",
		Permission: "reporting.report.view", Response: ReportInfo{}, List: true, Handler: s.list})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/reports/{code}", Summary: "Run a report (read replica, first 1,000 rows)",
		Permission: "reporting.report.view", Response: Result{}, Query: []route.Param{{Name: "params[...]", Description: "Report parameters"}}, Handler: s.run})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/reporting/exports", Summary: "Export a report asynchronously (notified when ready)",
		Permission: "reporting.export.create", Request: ExportRequest{}, Response: Export{}, Status: http.StatusAccepted, Idempotent: true, Handler: s.createExport})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/exports", Summary: "My exports",
		Permission: "reporting.export.create", Response: Export{}, List: true, Handler: s.listExports})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/reporting/dashboards/executive-overview", Summary: "Executive Overview",
		Permission: catalog.ManagementView, Response: Dashboard{}, Handler: s.executiveOverview})
	s.registerP1(reg)
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	perms := []catalog.Permission{
		{Code: "reporting.dashboard.view", Description: "Open the Management Dashboard"},
		{Code: "reporting.report.view", Description: "Open reports"},
		{Code: "reporting.export.create", Description: "Export reports"},
		{Code: "reporting.user_access.view", Description: "User Access Report"},
		{Code: "reporting.venue_directory.view", Description: "Venue Directory Report"},
	}
	mgmt := []string{"reporting.venue_directory.view"}
	roles := map[string][]string{
		"property_admin":  {"reporting.user_access.view", "reporting.venue_directory.view"},
		"general_manager": mgmt, "club_manager": mgmt, "resort_manager": mgmt,
		"finance_manager": {"reporting.user_access.view", "reporting.venue_directory.view"},
		"golf_manager":    mgmt,
	}
	p1, p1Roles := P1Permissions()
	perms = append(perms, p1...)
	for role, codes := range p1Roles {
		roles[role] = append(roles[role], codes...)
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}
