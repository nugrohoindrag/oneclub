package reporting

// PRD P5 EP-21 Management Dashboard & BI and the BI part of EP-27 (HR KPI &
// reports) and EP-29 (BI performance):
//
//   - analytics store (FR-BI-01): schema analytics, materialised per property,
//     KPI and period by jobs from the KPI definitions of the domain
//     dashboards (p5_bi_store.go);
//   - Executive Overview across domains with targets, MoM / YoY and per
//     property comparison (FR-BI-02/03/08, p5_bi_exec.go);
//   - KPI target plans per year and month, approved through the approval
//     engine (FR-BI-03, PRD P5 §16 #13, p5_bi_targets.go);
//   - drill-down from a KPI to its dimensions and the source transactions
//     (FR-BI-04, p5_bi_exec.go);
//   - scheduled reports by e-mail / WhatsApp per role (FR-BI-05,
//     p5_bi_schedule.go) and the self-service report builder (FR-BI-06,
//     p5_bi_datasets.go);
//   - KPI definitions in one catalogue (FR-BI-07) and the registry through
//     which the HR areas add their KPIs, reports and datasets (EP-27,
//     p5_bi_registry.go).
//
// Every read runs on the read replica with the query budget of the BI
// Policies (statement timeout, row limits — EP-29).

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/rules"
)

// BIPolicyCode is the BI Policies code in Settings → Club Policies.
const BIPolicyCode = "reporting.bi"

// BIPolicy configures the analytics store, the target indicators, the query
// budget and the scheduled reports (PRD P5 §6 #18: configurable, versioned).
type BIPolicy struct {
	// RefreshWindowDays are the days the incremental refresh recomputes
	// (today and the days before, for late postings).
	RefreshWindowDays int `json:"refreshWindowDays"`
	// RefreshIntervalMinutes documents the incremental refresh cadence
	// (the job runs every 10 minutes; data latency ≤ 15 minutes).
	RefreshIntervalMinutes int `json:"refreshIntervalMinutes"`
	// BackfillDays are the days the nightly refresh recomputes (2 years
	// keep YoY comparisons available).
	BackfillDays int `json:"backfillDays"`
	// StaleAfterMinutes flags the Executive Overview as stale.
	StaleAfterMinutes int `json:"staleAfterMinutes"`
	// OnTrackPercent / WatchPercent are the achievement thresholds of the
	// target indicator (on track ≥ OnTrack, watch ≥ Watch, else off track).
	OnTrackPercent string `json:"onTrackPercent"`
	WatchPercent   string `json:"watchPercent"`
	// QueryTimeoutSeconds is the statement budget of dashboard, drill-down
	// and dataset queries on the read replica.
	QueryTimeoutSeconds int `json:"queryTimeoutSeconds"`
	// MaxDrilldownRows / MaxDatasetRows bound the rows returned.
	MaxDrilldownRows int `json:"maxDrilldownRows"`
	MaxDatasetRows   int `json:"maxDatasetRows"`
	// MaxScheduledRecipients bounds the recipients of one scheduled report.
	MaxScheduledRecipients int `json:"maxScheduledRecipients"`
}

// DefaultBIPolicy is the code default of the BI Policies.
func DefaultBIPolicy() BIPolicy {
	return BIPolicy{RefreshWindowDays: 3, RefreshIntervalMinutes: 10, BackfillDays: 400, StaleAfterMinutes: 15, OnTrackPercent: "100",
		WatchPercent: "90", QueryTimeoutSeconds: 15, MaxDrilldownRows: 1000, MaxDatasetRows: 5000, MaxScheduledRecipients: 50}
}

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: BIPolicyCode, Category: "BI Policies", Name: "Analytics store, targets, query budget & scheduled reports",
		Description: "Refresh window and backfill of the analytics store, staleness of the Executive Overview, target indicator thresholds, " +
			"query budget (timeout and row limits) of dashboards, drill-down and datasets, and the recipient limit of scheduled reports",
		Default: DefaultBIPolicy()})
}

// LoadBIPolicy returns the BI Policies in force at the property.
func LoadBIPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) BIPolicy {
	p, _, err := rules.PolicyAt(ctx, q, BIPolicyCode, property, DefaultBIPolicy())
	if err != nil {
		return DefaultBIPolicy()
	}
	d := DefaultBIPolicy()
	if p.RefreshWindowDays <= 0 {
		p.RefreshWindowDays = d.RefreshWindowDays
	}
	if p.BackfillDays <= 0 {
		p.BackfillDays = d.BackfillDays
	}
	if p.StaleAfterMinutes <= 0 {
		p.StaleAfterMinutes = d.StaleAfterMinutes
	}
	if p.QueryTimeoutSeconds <= 0 {
		p.QueryTimeoutSeconds = d.QueryTimeoutSeconds
	}
	if p.MaxDrilldownRows <= 0 {
		p.MaxDrilldownRows = d.MaxDrilldownRows
	}
	if p.MaxDatasetRows <= 0 {
		p.MaxDatasetRows = d.MaxDatasetRows
	}
	if p.MaxScheduledRecipients <= 0 {
		p.MaxScheduledRecipients = d.MaxScheduledRecipients
	}
	if p.OnTrackPercent == "" {
		p.OnTrackPercent = d.OnTrackPercent
	}
	if p.WatchPercent == "" {
		p.WatchPercent = d.WatchPercent
	}
	return p
}

// queryBudget applies the statement timeout of the BI Policies to the
// transaction (EP-29 query budget).
func queryBudget(ctx context.Context, tx pgx.Tx, seconds int) error {
	_, err := tx.Exec(ctx, `SELECT set_config('statement_timeout', $1, true)`, strconv.Itoa(seconds*1000))
	return err
}

// budgetError maps a cancelled statement to a clear problem.
func budgetError(err error) error {
	var pe *pgconn.PgError
	if err != nil && errors.As(err, &pe) && pe.Code == "57014" {
		return errs.BadRequest("query_budget_exceeded", "the query exceeded the BI query budget; narrow the period or the filters")
	}
	return err
}

// Permissions of the BI area.
const (
	PermAnalyticsRefresh      = "reporting.analytics.refresh"
	PermKPITargetView         = "reporting.kpi_target.view"
	PermKPITargetManage       = "reporting.kpi_target.manage"
	PermScheduledReportView   = "reporting.scheduled_report.view"
	PermScheduledReportManage = "reporting.scheduled_report.manage"
	PermDatasetView           = "reporting.dataset.view"
	PermSavedReportManage     = "reporting.saved_report.manage"
	PermDatasetRevenue        = "reporting.dataset_revenue.view"
	PermDatasetKPI            = "reporting.dataset_kpi.view"
	PermHRPerformance         = "reporting.hr_performance.view"
)

// KPITargetDocumentType is the approval document of a KPI target plan
// (PRD P5 §16 #13: approved by the Board / Owner).
var KPITargetDocumentType = provision.DocumentType{Code: "reporting.kpi_target_plan", Module: "reporting", Name: "KPI Target Plan",
	Attributes: []provision.DocumentAttribute{{Key: "year", Label: "Year", Type: "number"}, {Key: "version", Label: "Version", Type: "number"}}}

// P5Contribution adds the BI permissions, the permissions of the P5 reports
// (BI and the reports the HR areas register, EP-27) and their role grants.
func P5Contribution() catalog.Contribution {
	perms := []catalog.Permission{
		{Code: PermAnalyticsRefresh, Description: "Refresh the analytics store"},
		{Code: PermKPITargetView, Description: "View KPI targets"},
		{Code: PermKPITargetManage, Description: "Create and submit KPI target plans"},
		{Code: PermScheduledReportView, Description: "View scheduled reports"},
		{Code: PermScheduledReportManage, Description: "Manage scheduled reports"},
		{Code: PermDatasetView, Description: "Use the report builder"},
		{Code: PermSavedReportManage, Description: "Save report builder reports"},
		{Code: PermDatasetRevenue, Description: "Revenue dataset"},
		{Code: PermDatasetKPI, Description: "Executive KPI datasets"},
		{Code: PermHRPerformance, Description: "Open HR Performance"},
	}
	mgmt := []string{PermKPITargetView, PermScheduledReportView, PermScheduledReportManage, PermDatasetView, PermSavedReportManage, PermDatasetKPI}
	finance := append(append([]string{}, mgmt...), PermKPITargetManage, PermAnalyticsRefresh, PermDatasetRevenue)
	roles := map[string][]string{
		"property_admin":  append(append([]string{}, finance...), PermHRPerformance),
		"general_manager": append(append([]string{}, finance...), PermHRPerformance),
		"finance_manager": finance,
		"club_manager":    append(append([]string{}, mgmt...), PermDatasetRevenue),
		"resort_manager":  append(append([]string{}, mgmt...), PermDatasetRevenue),
		"accountant":      {PermKPITargetView, PermDatasetView, PermSavedReportManage, PermDatasetRevenue, PermDatasetKPI},
		"hr_manager":      {PermHRPerformance, PermScheduledReportView, PermScheduledReportManage, PermDatasetView, PermSavedReportManage},
	}
	seen := map[string]bool{}
	for _, p := range perms {
		seen[p.Code] = true
	}
	for _, r := range P5Reports() {
		if !seen[r.Permission] {
			seen[r.Permission] = true
			perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		}
		for _, role := range p5ReportRoles[r.Code] {
			roles[role] = append(roles[role], r.Permission, "reporting.report.view", "reporting.export.create")
		}
	}
	for _, d := range datasets() {
		if d.Permission != "" && !seen[d.Permission] && !isExistingReportPermission(d.Permission) {
			seen[d.Permission] = true
			perms = append(perms, catalog.Permission{Code: d.Permission, Description: d.Name + " dataset"})
		}
	}
	for _, k := range hrKPIs() {
		if k.Permission != "" && !seen[k.Permission] && k.ContributePermission {
			seen[k.Permission] = true
			perms = append(perms, catalog.Permission{Code: k.Permission, Description: k.Label})
		}
	}
	for role, codes := range roles {
		roles[role] = dedupe(codes)
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}

// isExistingReportPermission reports whether a dataset reuses the
// permission of a P0–P4 report (contributed by those modules).
func isExistingReportPermission(perm string) bool {
	for _, r := range append(append(append(append([]*Report{UserAccessReport, VenueDirectoryReport}, P1Reports...), P2Reports...), P3Reports()...),
		P4Reports()...) {
		if r.Permission == perm {
			return true
		}
	}
	for _, p := range p1PermissionCodes() {
		if p == perm {
			return true
		}
	}
	return false
}

func p1PermissionCodes() []string {
	perms, _ := P1Permissions()
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Code)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ModuleChecker tells whether a module is enabled (FR-INS-04).
type ModuleChecker interface {
	ModuleEnabled(ctx context.Context, module string) (bool, error)
}

// BI is the Management Dashboard & BI service of P5.
type BI struct {
	S         *Service
	Approvals *approval.Engine
	Modules   ModuleChecker
	// LoadAuthz loads a user's principal (scheduled report recipients).
	LoadAuthz func(ctx context.Context, userID uuid.UUID) (*authz.Principal, error)
	// Now is the clock (tests); nil = time.Now.
	Now func() time.Time
}

func (b *BI) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *BI) moduleEnabled(ctx context.Context, module string) bool {
	if b.Modules == nil || module == "" {
		return true
	}
	ok, err := b.Modules.ModuleEnabled(ctx, module)
	return err != nil || ok
}

// Register adds the BI routes (PRD P5 §11 BI).
func (b *BI) Register(reg *route.Registry) {
	add := func(rt route.Route) { rt.Module = "reporting"; rt.Tag = "Management & BI"; reg.Add(rt) }
	b.registerExecutive(add)
	b.registerTargets(add)
	b.registerSchedules(add)
	b.registerDatasets(add)
}
