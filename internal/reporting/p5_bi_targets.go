package reporting

// KPI targets (FR-BI-03, PRD P5 §16 #13): the Finance Manager and the GM
// enter the annual budget split per month into a KPI target plan of the
// year; the plan is approved through the approval engine (Board / Owner
// workflow; approved at once when no workflow is configured). Approved
// plans are immutable: a revision copies the plan into a new draft version,
// and its approval supersedes the previous version (versioned targets).

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// KPITargetLine is the target of one KPI in one month.
type KPITargetLine struct {
	KPIKey string `json:"kpiKey" db:"kpi_key"`
	Month  int    `json:"month" db:"month" minimum:"1" maximum:"12"`
	Target string `json:"target" db:"target" doc:"Decimal; ratios 0–1"`
}

// KPITargetAnnual enters an annual budget that is split per month: flow
// KPIs evenly (rounding in December), other KPIs with the same value.
type KPITargetAnnual struct {
	KPIKey string `json:"kpiKey"`
	Amount string `json:"amount"`
	Split  string `json:"split,omitempty" enum:"even,same" doc:"Default: even for flow KPIs, same otherwise"`
}

// KPITargetPlan is a versioned target plan of a year.
type KPITargetPlan struct {
	ID                uuid.UUID       `json:"id" db:"id"`
	PropertyID        uuid.UUID       `json:"propertyId" db:"property_id"`
	Year              int             `json:"year" db:"year"`
	Version           int             `json:"version" db:"version"`
	Title             string          `json:"title" db:"title"`
	Notes             *string         `json:"notes" db:"notes"`
	Status            string          `json:"status" db:"status" enum:"draft,pending_approval,approved,rejected,superseded"`
	ApprovalRequestID *uuid.UUID      `json:"approvalRequestId" db:"approval_request_id"`
	SubmittedAt       *time.Time      `json:"submittedAt" db:"submitted_at"`
	DecidedAt         *time.Time      `json:"decidedAt" db:"decided_at"`
	DecisionReason    *string         `json:"decisionReason" db:"decision_reason"`
	TargetCount       int             `json:"targetCount" db:"target_count"`
	CreatedAt         time.Time       `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time       `json:"updatedAt" db:"updated_at"`
	Targets           []KPITargetLine `json:"targets,omitempty" db:"-"`
}

// KPITargetPlanRequest creates a plan.
type KPITargetPlanRequest struct {
	Year           int               `json:"year"`
	Title          string            `json:"title"`
	Notes          *string           `json:"notes,omitempty"`
	Targets        []KPITargetLine   `json:"targets,omitempty"`
	Annual         []KPITargetAnnual `json:"annual,omitempty"`
	CopyFromPlanID *uuid.UUID        `json:"copyFromPlanId,omitempty" doc:"Start from the targets of another plan"`
}

// KPITargetPlanUpdate changes a draft or rejected plan; targets / annual
// replace the targets of the KPIs they name.
type KPITargetPlanUpdate struct {
	Title   *string           `json:"title,omitempty"`
	Notes   *string           `json:"notes,omitempty"`
	Targets []KPITargetLine   `json:"targets,omitempty"`
	Annual  []KPITargetAnnual `json:"annual,omitempty"`
	Remove  []string          `json:"remove,omitempty" doc:"KPI keys whose targets are removed"`
}

const planSelect = `SELECT p.id, p.property_id, p.year, p.version, p.title, p.notes, p.status, p.approval_request_id, p.submitted_at, p.decided_at,
	p.decision_reason, (SELECT count(*) FROM reporting.kpi_targets t WHERE t.plan_id = p.id)::int AS target_count, p.created_at, p.updated_at
	FROM reporting.kpi_target_plans p`

func loadPlan(ctx context.Context, q dbtx.Querier, pid uuid.UUID, lock bool) (KPITargetPlan, error) {
	sql := planSelect + ` WHERE p.id = $1`
	if lock {
		sql += ` FOR UPDATE OF p`
	}
	rows, err := q.Query(ctx, sql, pid)
	p, err := handle.One[KPITargetPlan](rows, err, "KPI target plan")
	if err != nil {
		return p, err
	}
	p.Targets, err = handle.List[KPITargetLine](q.Query(ctx, `SELECT kpi_key, month, trim_scale(target)::text AS target FROM reporting.kpi_targets
		WHERE plan_id = $1 ORDER BY kpi_key, month`, pid))
	return p, err
}

// expandTargets validates the lines and the annual budgets.
func expandTargets(lines []KPITargetLine, annual []KPITargetAnnual) ([]KPITargetLine, error) {
	out := []KPITargetLine{}
	seen := map[string]bool{}
	valid := func(key, field string) (execKPI, error) {
		k, ok := executiveKPI(key)
		if !ok || k.Placeholder {
			return k, handle.Invalid(field, "unknown_kpi", "unknown KPI "+key+" (see the KPI definitions)")
		}
		return k, nil
	}
	for i, l := range lines {
		f := "targets[" + strconv.Itoa(i) + "]"
		if _, err := valid(l.KPIKey, f+".kpiKey"); err != nil {
			return nil, err
		}
		if l.Month < 1 || l.Month > 12 {
			return nil, handle.Invalid(f+".month", "invalid", "month must be 1–12")
		}
		if _, err := decimal.NewFromString(l.Target); err != nil {
			return nil, handle.Invalid(f+".target", "invalid", "target must be a decimal")
		}
		k := l.KPIKey + "|" + strconv.Itoa(l.Month)
		if seen[k] {
			return nil, handle.Invalid(f, "duplicate", "one target per KPI and month")
		}
		seen[k] = true
		out = append(out, l)
	}
	for i, a := range annual {
		f := "annual[" + strconv.Itoa(i) + "]"
		k, err := valid(a.KPIKey, f+".kpiKey")
		if err != nil {
			return nil, err
		}
		amt, err := decimal.NewFromString(a.Amount)
		if err != nil {
			return nil, handle.Invalid(f+".amount", "invalid", "amount must be a decimal")
		}
		split := a.Split
		if split == "" {
			split = "same"
			if k.Kind == KindFlow {
				split = "even"
			}
		}
		if split != "even" && split != "same" {
			return nil, handle.Invalid(f+".split", "invalid", "split must be even or same")
		}
		monthly := amt
		if split == "even" {
			monthly = amt.Div(decimal.NewFromInt(12)).RoundDown(2)
		}
		acc := decimal.Zero
		for mo := 1; mo <= 12; mo++ {
			key := a.KPIKey + "|" + strconv.Itoa(mo)
			if seen[key] {
				return nil, handle.Invalid(f, "duplicate", "one target per KPI and month")
			}
			seen[key] = true
			v := monthly
			if split == "even" && mo == 12 {
				v = amt.Sub(acc)
			}
			acc = acc.Add(v)
			out = append(out, KPITargetLine{KPIKey: a.KPIKey, Month: mo, Target: v.String()})
		}
	}
	return out, nil
}

func writeTargets(ctx context.Context, tx pgx.Tx, plan, property uuid.UUID, lines []KPITargetLine) error {
	if len(lines) == 0 {
		return nil
	}
	keys, months, vals := make([]string, len(lines)), make([]int32, len(lines)), make([]string, len(lines))
	for i, l := range lines {
		keys[i], months[i], vals[i] = l.KPIKey, int32(l.Month), l.Target
	}
	_, err := tx.Exec(ctx, `INSERT INTO reporting.kpi_targets (plan_id, property_id, kpi_key, month, target)
		SELECT $1, $2, k, m, v::numeric FROM unnest($3::text[], $4::int[], $5::text[]) AS x(k, m, v)
		ON CONFLICT (plan_id, kpi_key, month) DO UPDATE SET target = EXCLUDED.target`, plan, property, keys, months, vals)
	return err
}

func (b *BI) createPlan(ctx context.Context, tx pgx.Tx, r *http.Request, req KPITargetPlanRequest) (KPITargetPlan, error) {
	property := handle.Property(ctx)
	if req.Year < 2000 || req.Year > 2100 {
		return KPITargetPlan{}, handle.Invalid("year", "invalid", "enter the budget year")
	}
	if req.Title == "" {
		req.Title = fmt.Sprintf("Budget %d", req.Year)
	}
	lines, err := expandTargets(req.Targets, req.Annual)
	if err != nil {
		return KPITargetPlan{}, err
	}
	pid := id.New()
	var version int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) + 1 FROM reporting.kpi_target_plans WHERE property_id = $1 AND year = $2`,
		property, req.Year).Scan(&version); err != nil {
		return KPITargetPlan{}, err
	}
	uid := handle.UserID(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO reporting.kpi_target_plans (id, property_id, year, version, title, notes, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,'draft',$7,$7)`, pid, property, req.Year, version, req.Title, req.Notes, uid); err != nil {
		return KPITargetPlan{}, err
	}
	if req.CopyFromPlanID != nil {
		src, err := loadPlan(ctx, tx, *req.CopyFromPlanID, false)
		if err != nil {
			return KPITargetPlan{}, handle.Invalid("copyFromPlanId", "not_found", "plan not found")
		}
		if err := writeTargets(ctx, tx, pid, property, src.Targets); err != nil {
			return KPITargetPlan{}, err
		}
	}
	if err := writeTargets(ctx, tx, pid, property, lines); err != nil {
		return KPITargetPlan{}, err
	}
	out, err := loadPlan(ctx, tx, pid, false)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionCreate, EntityType: "reporting.kpi_target_plan",
		EntityID: pid.String(), EntityLabel: planLabel(out), PropertyID: &property,
		After: map[string]any{"year": out.Year, "version": out.Version, "title": out.Title, "targets": len(out.Targets)}})
}

func planLabel(p KPITargetPlan) string {
	return fmt.Sprintf("KPI Target Plan %d v%d", p.Year, p.Version)
}

func editable(p KPITargetPlan) error {
	if p.Status != "draft" && p.Status != "rejected" {
		return errs.Conflict("plan_not_editable", "only draft or rejected plans can change; revise the plan to change approved targets")
	}
	return nil
}

func (b *BI) updatePlan(ctx context.Context, tx pgx.Tx, r *http.Request, req KPITargetPlanUpdate) (KPITargetPlan, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return KPITargetPlan{}, err
	}
	before, err := loadPlan(ctx, tx, pid, true)
	if err != nil {
		return before, err
	}
	if err := editable(before); err != nil {
		return before, err
	}
	lines, err := expandTargets(req.Targets, req.Annual)
	if err != nil {
		return before, err
	}
	if req.Title != nil && *req.Title == "" {
		return before, handle.Invalid("title", "required", "title is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE reporting.kpi_target_plans SET title = coalesce($2, title), notes = CASE WHEN $3::boolean THEN $4 ELSE notes END,
		status = 'draft', updated_by = $5 WHERE id = $1`, pid, req.Title, req.Notes != nil, req.Notes, handle.UserID(ctx)); err != nil {
		return before, err
	}
	if len(req.Remove) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM reporting.kpi_targets WHERE plan_id = $1 AND kpi_key = ANY($2)`, pid, req.Remove); err != nil {
			return before, err
		}
	}
	if err := writeTargets(ctx, tx, pid, before.PropertyID, lines); err != nil {
		return before, err
	}
	out, err := loadPlan(ctx, tx, pid, false)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionUpdate, EntityType: "reporting.kpi_target_plan",
		EntityID: pid.String(), EntityLabel: planLabel(out), PropertyID: &out.PropertyID, Before: before, After: out})
}

func (b *BI) deletePlan(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (struct{}, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return struct{}{}, err
	}
	p, err := loadPlan(ctx, tx, pid, true)
	if err != nil {
		return struct{}{}, err
	}
	if err := editable(p); err != nil {
		return struct{}{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM reporting.kpi_target_plans WHERE id = $1`, pid); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionDelete, EntityType: "reporting.kpi_target_plan",
		EntityID: pid.String(), EntityLabel: planLabel(p), PropertyID: &p.PropertyID, Before: p})
}

// submitPlan sends a plan for approval (FR-BI-03, §16 #13).
func (b *BI) submitPlan(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (KPITargetPlan, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return KPITargetPlan{}, err
	}
	p, err := loadPlan(ctx, tx, pid, true)
	if err != nil {
		return p, err
	}
	if err := editable(p); err != nil {
		return p, err
	}
	if len(p.Targets) == 0 {
		return p, errs.Validation("plan_empty", "enter at least one target before submitting")
	}
	uid := handle.UserID(ctx)
	if _, err := tx.Exec(ctx, `UPDATE reporting.kpi_target_plans SET status = 'pending_approval', submitted_at = now(), submitted_by = $2, decided_at = NULL,
		decided_by = NULL, decision_reason = NULL, updated_by = $2 WHERE id = $1`, pid, uid); err != nil {
		return p, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionStatusChange, EntityType: "reporting.kpi_target_plan",
		EntityID: pid.String(), EntityLabel: planLabel(p), PropertyID: &p.PropertyID, Before: map[string]any{"status": p.Status},
		After: map[string]any{"status": "pending_approval"}}); err != nil {
		return p, err
	}
	rid, _, err := b.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: KPITargetDocumentType.Code, DocumentID: pid,
		DocumentRef: fmt.Sprintf("KPI-%d-v%d", p.Year, p.Version), Title: fmt.Sprintf("%s — %s", planLabel(p), p.Title), PropertyID: p.PropertyID,
		Attributes: map[string]any{"year": p.Year, "version": p.Version}})
	if err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reporting.kpi_target_plans SET approval_request_id = $2 WHERE id = $1`, pid, rid); err != nil {
		return p, err
	}
	return loadPlan(ctx, tx, pid, false)
}

// revisePlan copies a plan into a new draft version (targets of the next
// months updated during the year, §9.5).
func (b *BI) revisePlan(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (KPITargetPlan, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return KPITargetPlan{}, err
	}
	p, err := loadPlan(ctx, tx, pid, false)
	if err != nil {
		return p, err
	}
	if p.Status != "approved" && p.Status != "superseded" {
		return p, errs.Conflict("plan_not_approved", "only approved plans are revised; edit the draft instead")
	}
	return b.createPlan(ctx, tx, r, KPITargetPlanRequest{Year: p.Year, Title: p.Title, Notes: p.Notes, CopyFromPlanID: &p.ID})
}

// KPITargetDecision applies the approval decision to the plan; approving
// supersedes the plan in force of the same year.
func (b *BI) KPITargetDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status string
	var year, version int
	if err := tx.QueryRow(ctx, `SELECT status, year, version FROM reporting.kpi_target_plans WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&status, &year, &version); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != "pending_approval" {
		return nil
	}
	next := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "draft"}[d.Status]
	if next == "" {
		return nil
	}
	if next == "approved" {
		if _, err := tx.Exec(ctx, `UPDATE reporting.kpi_target_plans SET status = 'superseded' WHERE property_id = $1 AND year = $2 AND status = 'approved'`,
			d.PropertyID, year); err != nil {
			return err
		}
	}
	var by *uuid.UUID
	if d.DecidedBy != uuid.Nil {
		by = &d.DecidedBy
	}
	if _, err := tx.Exec(ctx, `UPDATE reporting.kpi_target_plans SET status = $2, decided_at = now(), decided_by = $3, decision_reason = nullif($4, '')
		WHERE id = $1`, d.DocumentID, next, by, d.Reason); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "reporting", Action: audit.ActionStatusChange, EntityType: "reporting.kpi_target_plan",
		EntityID: d.DocumentID.String(), EntityLabel: fmt.Sprintf("KPI Target Plan %d v%d", year, version), PropertyID: &d.PropertyID, Reason: d.Reason,
		Before: map[string]any{"status": status}, After: map[string]any{"status": next}})
}

func (b *BI) listPlans(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[KPITargetPlan], error) {
	out := httpx.Page[KPITargetPlan]{Items: []KPITargetPlan{}}
	where, args := ` WHERE p.property_id = $1`, []any{handle.Property(ctx)}
	if v := r.URL.Query().Get("year"); v != "" {
		y, err := strconv.Atoi(v)
		if err != nil {
			return out, handle.Invalid("year", "invalid", "year must be a number")
		}
		where += ` AND p.year = $2`
		args = append(args, y)
	}
	var err error
	out.Items, err = handle.List[KPITargetPlan](tx.Query(ctx, planSelect+where+` ORDER BY p.year DESC, p.version DESC`, args...))
	return out, err
}

func (b *BI) getPlan(ctx context.Context, tx pgx.Tx, r *http.Request) (KPITargetPlan, error) {
	pid, err := handle.ID(r)
	if err != nil {
		return KPITargetPlan{}, err
	}
	return loadPlan(ctx, tx, pid, false)
}

func (b *BI) registerTargets(add func(route.Route)) {
	db := b.S.DB
	const base = "/api/v1/reporting/kpi-targets"
	add(route.Route{Method: http.MethodGet, Path: base, Scope: route.ScopeProperty, Permission: PermKPITargetView, Summary: "KPI target plans",
		Response: KPITargetPlan{}, List: true, Query: []route.Param{{Name: "year", Type: "integer"}}, Handler: handle.Read(db, b.listPlans)})
	add(route.Route{Method: http.MethodPost, Path: base, Scope: route.ScopeProperty, Permission: PermKPITargetManage,
		Summary: "Create a KPI target plan (targets per KPI and month, or an annual budget split per month)", Request: KPITargetPlanRequest{},
		Response: KPITargetPlan{}, Handler: handle.Write(db, http.StatusCreated, b.createPlan)})
	add(route.Route{Method: http.MethodGet, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermKPITargetView, Summary: "KPI target plan with its targets",
		Response: KPITargetPlan{}, Handler: handle.Read(db, b.getPlan)})
	add(route.Route{Method: http.MethodPatch, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermKPITargetManage,
		Summary: "Change a draft or rejected KPI target plan", Request: KPITargetPlanUpdate{}, Response: KPITargetPlan{},
		Handler: handle.Write(db, http.StatusOK, b.updatePlan)})
	add(route.Route{Method: http.MethodDelete, Path: base + "/{id}", Scope: route.ScopeProperty, Permission: PermKPITargetManage,
		Summary: "Delete a draft or rejected KPI target plan", Handler: handle.Write(db, http.StatusNoContent, b.deletePlan)})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:submit", Scope: route.ScopeProperty, Permission: PermKPITargetManage,
		Summary: "Submit a KPI target plan for approval (approved at once without a workflow)", Response: KPITargetPlan{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, b.submitPlan)})
	add(route.Route{Method: http.MethodPost, Path: base + "/{id}:revise", Scope: route.ScopeProperty, Permission: PermKPITargetManage,
		Summary: "Revise an approved plan into a new draft version", Response: KPITargetPlan{}, Handler: handle.Write(db, http.StatusCreated, b.revisePlan)})
}

// SeedTargets writes annual budgets split per month into a plan (demo seed).
func SeedTargets(ctx context.Context, tx pgx.Tx, plan, property uuid.UUID, annual []KPITargetAnnual) error {
	lines, err := expandTargets(nil, annual)
	if err != nil {
		return err
	}
	return writeTargets(ctx, tx, plan, property, lines)
}
