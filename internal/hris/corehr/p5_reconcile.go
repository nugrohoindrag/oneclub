package corehr

// HR Migration Reconciliation (PRD P5 EP-28 FR-MIG-P5-05, Exit Criteria #1):
// the control totals of the legacy HR system (headcount per org unit,
// leave balances per leave type or employee at the cutover date; payroll
// totals are added by the payroll area as reconciliation metrics) against
// OneClub, with the sign-off of the HR Manager and of the Finance Manager —
// two different people. A mismatch can only be signed with an explanation
// ("selisih terjelaskan"). Used by the HRIS Migration screen, the API and
// `oneclub import hris --reconcile`.

import (
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Permissions of the migration reconciliation.
const (
	PermReconView        = "hris.migration_reconciliation.view"
	PermReconCreate      = "hris.migration_reconciliation.create"
	PermReconSignHR      = "hris.migration_reconciliation.sign_hr"
	PermReconSignFinance = "hris.migration_reconciliation.sign_finance"
)

// MigrationReconciliation is a reconciliation run with its sign-offs.
type MigrationReconciliation struct {
	ID                uuid.UUID                 `json:"id" db:"id"`
	Number            string                    `json:"number" db:"number"`
	CutoverDate       time.Time                 `json:"cutoverDate" db:"cutover_date"`
	LegacySystem      string                    `json:"legacySystem" db:"legacy_system"`
	Notes             *string                   `json:"notes" db:"notes"`
	Lines             []hris.ReconciliationLine `json:"lines" db:"lines"`
	Checks            int                       `json:"checks" db:"checks"`
	Mismatches        int                       `json:"mismatches" db:"mismatches"`
	Status            string                    `json:"status" db:"status" enum:"draft,hr_signed,finance_signed,signed_off,cancelled"`
	CalculatedAt      time.Time                 `json:"calculatedAt" db:"calculated_at"`
	HRSignedName      *string                   `json:"hrSignedName" db:"hr_signed_name"`
	HRSignedAt        *time.Time                `json:"hrSignedAt" db:"hr_signed_at"`
	HRNote            *string                   `json:"hrNote" db:"hr_note"`
	FinanceSignedName *string                   `json:"financeSignedName" db:"finance_signed_name"`
	FinanceSignedAt   *time.Time                `json:"financeSignedAt" db:"finance_signed_at"`
	FinanceNote       *string                   `json:"financeNote" db:"finance_note"`
	CreatedAt         time.Time                 `json:"createdAt" db:"created_at"`
}

const reconSelect = `SELECT id, number, cutover_date, legacy_system, notes, lines, checks, mismatches, status, calculated_at, hr_signed_name, hr_signed_at,
	hr_note, finance_signed_name, finance_signed_at, finance_note, created_at FROM hris.migration_reconciliations`

// MigrationReconciliationRequest starts a reconciliation from the legacy control totals.
type MigrationReconciliationRequest struct {
	CutoverDate  string                     `json:"cutoverDate"`
	LegacySystem string                     `json:"legacySystem" doc:"e.g. the club's HR Excel / previous HRIS"`
	Notes        string                     `json:"notes,omitempty"`
	Lines        []hris.ReconciliationInput `json:"lines" doc:"Control totals: metric (headcount, leave_balance, …), key (TOTAL, org unit code, leave type, EMPLOYEENO:TYPE) and the legacy value"`
}

// MigrationReconciliationSignOff signs a reconciliation.
type MigrationReconciliationSignOff struct {
	Role string `json:"role" enum:"hr,finance" doc:"hr = HR Manager, finance = Finance Manager"`
	Note string `json:"note,omitempty" doc:"Required when the reconciliation has mismatches (explanation of the differences)"`
}

// ReconciliationMetricInfo describes a metric for the import screen.
type ReconciliationMetricInfo struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	KeyHelp string `json:"keyHelp"`
}

func (m *Module) registerReconciliation(reg *route.Registry) {
	tag := "HRIS Migration"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/migration-reconciliation-metrics", Summary: "Metrics of the HR migration reconciliation",
		Permission: PermReconView, Response: ReconciliationMetricInfo{}, List: true, Handler: listRead(m.DB, m.reconMetricsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/migration-reconciliations", Summary: "HR migration reconciliations (cutover sign-off)",
		Permission: PermReconView, Response: MigrationReconciliation{}, List: true, Handler: listRead(m.DB, m.listReconHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/migration-reconciliations",
		Summary: "Reconcile the legacy HR control totals with OneClub", Permission: PermReconCreate, Request: MigrationReconciliationRequest{},
		Response: MigrationReconciliation{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createReconHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/migration-reconciliations/{id}", Summary: "HR migration reconciliation",
		Permission: PermReconView, Response: MigrationReconciliation{}, Handler: handle.Read(m.DB, m.getReconHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/migration-reconciliations/{id}/csv", Summary: "HR migration reconciliation as CSV",
		Permission: PermReconView, RawContent: "text/csv", Handler: m.reconCSVHTTP})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/migration-reconciliations/{id}:recalculate",
		Summary: "Recalculate the OneClub values (before any sign-off)", Permission: PermReconCreate, Request: handle.Empty{}, Response: MigrationReconciliation{},
		Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.recalculateReconHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/migration-reconciliations/{id}:sign-off",
		Summary: "Sign the reconciliation as HR Manager or Finance Manager", Request: MigrationReconciliationSignOff{}, Response: MigrationReconciliation{},
		Permission: PermReconView, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.signReconHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/migration-reconciliations/{id}:cancel",
		Summary: "Cancel a reconciliation that is not signed off", Permission: PermReconCreate, Request: handle.Empty{}, Response: MigrationReconciliation{},
		Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.cancelReconHTTP)})
}

func (m *Module) reconMetricsHTTP(_ context.Context, _ pgx.Tx, _ *http.Request) ([]ReconciliationMetricInfo, error) {
	out := []ReconciliationMetricInfo{}
	for _, x := range hris.ReconciliationMetrics() {
		out = append(out, ReconciliationMetricInfo{Code: x.Code, Label: x.Label, KeyHelp: x.KeyHelp})
	}
	return out, nil
}

func (m *Module) listReconHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request) ([]MigrationReconciliation, error) {
	return handle.List[MigrationReconciliation](tx.Query(ctx, reconSelect+` WHERE property_id = $1 ORDER BY created_at DESC LIMIT 200`, handle.Property(ctx)))
}

func (m *Module) loadRecon(ctx context.Context, q pgx.Tx, property, rid uuid.UUID) (MigrationReconciliation, error) {
	return getOne[MigrationReconciliation]("reconciliation")(q.Query(ctx, reconSelect+` WHERE id = $1 AND property_id = $2`, rid, property))
}

func (m *Module) getReconHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (MigrationReconciliation, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	return m.loadRecon(ctx, tx, handle.Property(ctx), rid)
}

// CreateReconciliation reconciles the legacy control totals (API and CLI).
func (m *Module) CreateReconciliation(ctx context.Context, tx pgx.Tx, property uuid.UUID, req MigrationReconciliationRequest) (MigrationReconciliation, error) {
	cutover, err := mustDate("cutoverDate", req.CutoverDate)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	if strings.TrimSpace(req.LegacySystem) == "" {
		return MigrationReconciliation{}, handle.Invalid("legacySystem", "required", "name the legacy system")
	}
	if len(req.Lines) == 0 || len(req.Lines) > 5000 {
		return MigrationReconciliation{}, handle.Invalid("lines", "required", "1 to 5000 control totals")
	}
	for i, l := range req.Lines {
		if _, ok := hris.ReconciliationMetricOf(strings.TrimSpace(l.Metric)); !ok {
			var codes []string
			for _, x := range hris.ReconciliationMetrics() {
				codes = append(codes, x.Code)
			}
			return MigrationReconciliation{}, handle.Invalid("lines["+itoa(i)+"].metric", "invalid", "one of: "+strings.Join(codes, ", "))
		}
		if _, err := decimal.NewFromString(strings.TrimSpace(l.Legacy)); err != nil {
			return MigrationReconciliation{}, handle.Invalid("lines["+itoa(i)+"].legacy", "invalid", "a number")
		}
		req.Lines[i].Metric = strings.TrimSpace(l.Metric)
	}
	lines, err := hris.Reconcile(ctx, tx, property, cutover, req.Lines)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	number, err := yearlyNumber(ctx, tx, property, "HRREC", cutover.Year())
	if err != nil {
		return MigrationReconciliation{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.migration_reconciliations (id, property_id, number, cutover_date, legacy_system, notes, lines, checks, mismatches,
		created_by, updated_by) VALUES ($1,$2,$3,$4::date,$5,$6,$7,$8,$9,$10,$10)`, rid, property, number, ymd(cutover), strings.TrimSpace(req.LegacySystem),
		nullStr(req.Notes), lines, len(lines), mismatches(lines), actor(ctx)); err != nil {
		return MigrationReconciliation{}, err
	}
	rec, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return rec, err
	}
	return rec, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.migration_reconciliation",
		EntityID: rid.String(), EntityLabel: number, PropertyID: &property,
		After: map[string]any{"cutoverDate": ymd(cutover), "legacySystem": req.LegacySystem, "checks": rec.Checks, "mismatches": rec.Mismatches}})
}

func mismatches(lines []hris.ReconciliationLine) int {
	n := 0
	for _, l := range lines {
		if !l.Match {
			n++
		}
	}
	return n
}

func (m *Module) createReconHTTP(ctx context.Context, tx pgx.Tx, _ *http.Request, req MigrationReconciliationRequest) (MigrationReconciliation, error) {
	return m.CreateReconciliation(ctx, tx, handle.Property(ctx), req)
}

func (m *Module) recalculateReconHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (MigrationReconciliation, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	rec, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return rec, err
	}
	if rec.Status != "draft" {
		return rec, errs.Conflict("invalid_status", "a reconciliation can be recalculated before the first sign-off only; cancel it and start a new one")
	}
	var in []hris.ReconciliationInput
	for _, l := range rec.Lines {
		if l.Legacy != nil {
			in = append(in, hris.ReconciliationInput{Metric: l.Metric, Key: l.Key, Legacy: *l.Legacy})
		}
	}
	lines, err := hris.Reconcile(ctx, tx, property, rec.CutoverDate, in)
	if err != nil {
		return rec, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.migration_reconciliations SET lines = $2, checks = $3, mismatches = $4, calculated_at = now(), updated_by = $5
		WHERE id = $1`, rid, lines, len(lines), mismatches(lines), actor(ctx)); err != nil {
		return rec, err
	}
	after, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "recalculate", EntityType: "hris.migration_reconciliation",
		EntityID: rid.String(), EntityLabel: rec.Number, PropertyID: &property, Before: map[string]any{"mismatches": rec.Mismatches},
		After: map[string]any{"mismatches": after.Mismatches}})
}

func (m *Module) signReconHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req MigrationReconciliationSignOff) (MigrationReconciliation, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	perm := map[string]string{"hr": PermReconSignHR, "finance": PermReconSignFinance}[req.Role]
	if perm == "" {
		return MigrationReconciliation{}, enumErr("role", []string{"hr", "finance"})
	}
	if !can(ctx, perm, property) {
		return MigrationReconciliation{}, errs.Forbidden("only the " + map[string]string{"hr": "HR Manager", "finance": "Finance Manager"}[req.Role] + " signs this part")
	}
	rec, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return rec, err
	}
	if rec.Status == "signed_off" || rec.Status == "cancelled" {
		return rec, errs.Conflict("invalid_status", "the reconciliation is "+strings.ReplaceAll(rec.Status, "_", " "))
	}
	if (req.Role == "hr" && rec.HRSignedAt != nil) || (req.Role == "finance" && rec.FinanceSignedAt != nil) {
		return rec, errs.Conflict("already_signed", "this part is already signed")
	}
	note := strings.TrimSpace(req.Note)
	if rec.Mismatches > 0 && note == "" {
		return rec, handle.Invalid("note", "required", "explain the differences before signing a reconciliation with mismatches")
	}
	user := handle.UserID(ctx)
	var hrBy, finBy *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT hr_signed_by, finance_signed_by FROM hris.migration_reconciliations WHERE id = $1`, rid).Scan(&hrBy, &finBy); err != nil {
		return rec, err
	}
	if (hrBy != nil && *hrBy == user) || (finBy != nil && *finBy == user) {
		return rec, errs.Conflict("four_eyes", "HR and Finance sign-offs are given by two different people")
	}
	name := ""
	if p := authz.From(ctx); p != nil {
		name = p.Name
	}
	var sql string
	if req.Role == "hr" {
		sql = `UPDATE hris.migration_reconciliations SET hr_signed_by = $2, hr_signed_name = $3, hr_signed_at = now(), hr_note = $4,
			status = CASE WHEN finance_signed_at IS NULL THEN 'hr_signed' ELSE 'signed_off' END, updated_by = $2 WHERE id = $1`
	} else {
		sql = `UPDATE hris.migration_reconciliations SET finance_signed_by = $2, finance_signed_name = $3, finance_signed_at = now(), finance_note = $4,
			status = CASE WHEN hr_signed_at IS NULL THEN 'finance_signed' ELSE 'signed_off' END, updated_by = $2 WHERE id = $1`
	}
	if _, err := tx.Exec(ctx, sql, rid, user, name, nullStr(note)); err != nil {
		return rec, err
	}
	after, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "sign_off", EntityType: "hris.migration_reconciliation", EntityID: rid.String(),
		EntityLabel: rec.Number, PropertyID: &property, Before: map[string]any{"status": rec.Status},
		After: map[string]any{"status": after.Status, "role": req.Role, "note": note}})
}

func (m *Module) cancelReconHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (MigrationReconciliation, error) {
	property := handle.Property(ctx)
	rid, err := handle.ID(r)
	if err != nil {
		return MigrationReconciliation{}, err
	}
	rec, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return rec, err
	}
	if rec.Status == "signed_off" || rec.Status == "cancelled" {
		return rec, errs.Conflict("invalid_status", "the reconciliation is "+strings.ReplaceAll(rec.Status, "_", " "))
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.migration_reconciliations SET status = 'cancelled', updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
		return rec, err
	}
	after, err := m.loadRecon(ctx, tx, property, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.migration_reconciliation", EntityID: rid.String(),
		EntityLabel: rec.Number, PropertyID: &property, Before: map[string]any{"status": rec.Status}, After: map[string]any{"status": "cancelled"}})
}

// ReconciliationCSV renders the lines with the sign-offs.
func ReconciliationCSV(rec MigrationReconciliation) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"metric", "key", "legacy", "oneclub", "difference", "result"})
	for _, l := range rec.Lines {
		legacy, res := "", "MATCH"
		if l.Legacy != nil {
			legacy = *l.Legacy
		}
		if !l.Match {
			res = "MISMATCH"
		}
		_ = w.Write([]string{l.Metric, l.Key, legacy, l.OneClub, l.Difference, res})
	}
	sign := func(role string, name *string, at *time.Time, note *string) []string {
		row := []string{"sign-off", role, "", "", "", "pending"}
		if at != nil {
			row[2], row[5] = deref(name), at.Format(time.RFC3339)
			row[3] = deref(note)
		}
		return row
	}
	_ = w.Write(sign("HR Manager", rec.HRSignedName, rec.HRSignedAt, rec.HRNote))
	_ = w.Write(sign("Finance Manager", rec.FinanceSignedName, rec.FinanceSignedAt, rec.FinanceNote))
	w.Flush()
	return b.String()
}

func (m *Module) reconCSVHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var rec MigrationReconciliation
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		rec, err = m.loadRecon(ctx, tx, handle.Property(ctx), rid)
		return err
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+rec.Number+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(ReconciliationCSV(rec)))
}
