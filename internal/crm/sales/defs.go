package sales

// Master data of CRM Sales on the generic resource engine: pipelines and
// their stages (FR-PIPE-01), sales teams and members (round-robin
// assignment per line, FR-LEAD-05), sales targets (FR-COM-01) and
// commission schemes (FR-COM-03).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

func linesField(label string) resource.Field {
	return resource.Field{Name: "lines", Column: "lines", Label: label, Kind: resource.StringList, Enum: Lines, Default: []string{}}
}

// Pipelines are the sales pipelines per business line.
var Pipelines = &resource.Def{
	Key: "crm.sales_pipeline", Module: "crm", Perm: "crm.pipeline", Path: "/api/v1/crm/pipelines", Table: "crm.sales_pipelines",
	Name: "Sales Pipeline", Plural: "Sales Pipelines", Tag: "Sales Pipeline", PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "sort_order, name, id", SchemaName: "SalesPipeline",
	Fields: []resource.Field{resource.Code("Pipeline Code"), resource.Name(), linesField("Business Lines (default pipeline of)"),
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(100)},
		resource.Status("active", "inactive")},
}

// PipelineStages are the stages of a pipeline with their probability.
var PipelineStages = &resource.Def{
	Key: "crm.sales_pipeline_stage", Module: "crm", Perm: "crm.pipeline", Path: "/api/v1/crm/pipeline-stages", Table: "crm.sales_pipeline_stages",
	Name: "Pipeline Stage", Plural: "Pipeline Stages", Tag: "Sales Pipeline", PropertyScoped: true, Archive: true,
	OrderBy: "pipeline_id, sort_order, id", SchemaName: "SalesPipelineStage",
	Fields: []resource.Field{
		{Name: "pipelineId", Column: "pipeline_id", Label: "Pipeline", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "crm.sales_pipelines", SameProperty: true, Label: "pipeline"}},
		resource.Code("Stage Code"), resource.Name(),
		{Name: "probability", Column: "probability", Label: "Probability (%)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(100)},
		{Name: "kind", Column: "kind", Label: "Stage Kind", Kind: resource.Enum, Enum: []string{"open", "won", "lost"}, Default: "open", Filter: true},
		resource.Status("active", "inactive")},
}

// Teams are the sales teams; a team handles its business lines.
var Teams = &resource.Def{
	Key: "crm.sales_team", Module: "crm", Perm: "crm.sales_team", Path: "/api/v1/crm/sales-teams", Table: "crm.sales_teams",
	Name: "Sales Team", Plural: "Sales Teams", Tag: "Sales Teams", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	SchemaName: "SalesTeam",
	Fields: []resource.Field{resource.Code("Team Code"), resource.Name(), linesField("Business Lines (empty: all)"),
		{Name: "managerUserId", Column: "manager_user_id", Label: "Sales Manager", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		resource.Status("active", "inactive")},
}

// TeamMembers are the sales executives of a team (round-robin pool).
var TeamMembers = &resource.Def{
	Key: "crm.sales_team_member", Module: "crm", Perm: "crm.sales_team", Path: "/api/v1/crm/sales-team-members", Table: "crm.sales_team_members",
	Name: "Sales Team Member", Plural: "Sales Team Members", Tag: "Sales Teams", PropertyScoped: true, OrderBy: "team_id, created_at, id",
	SchemaName: "SalesTeamMember",
	Fields: []resource.Field{
		{Name: "teamId", Column: "team_id", Label: "Sales Team", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "crm.sales_teams", SameProperty: true, Label: "sales team"}},
		{Name: "userId", Column: "user_id", Label: "Sales Executive", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		linesField("Business Lines (empty: the team's)"),
		{Name: "lastAssignedAt", Column: "last_assigned_at", Label: "Last Lead Assigned", Kind: resource.Timestamp, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// Targets are the sales targets per sales, team, line and period.
var Targets = &resource.Def{
	Key: "crm.sales_target", Module: "crm", Perm: "crm.sales_target", Path: "/api/v1/crm/sales-targets", Table: "crm.sales_targets",
	Name: "Sales Target", Plural: "Sales Targets", Tag: "Sales Targets", PropertyScoped: true, Archive: true,
	OrderBy: "period_start DESC, id", SchemaName: "SalesTarget",
	Fields: []resource.Field{
		{Name: "userId", Column: "user_id", Label: "Sales Executive (empty: team or club)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		{Name: "teamId", Column: "team_id", Label: "Sales Team", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.sales_teams", SameProperty: true, Label: "sales team"}},
		{Name: "line", Column: "line", Label: "Business Line (empty: all)", Kind: resource.Enum, Enum: Lines, Filter: true},
		{Name: "periodType", Column: "period_type", Label: "Period", Kind: resource.Enum, Enum: []string{"month", "quarter", "year", "custom"},
			Default: "month", Filter: true},
		{Name: "periodStart", Column: "period_start", Label: "Period Start", Kind: resource.Date, Required: true, Filter: true},
		{Name: "periodEnd", Column: "period_end", Label: "Period End", Kind: resource.Date, Required: true},
		{Name: "targetRevenue", Column: "target_revenue", Label: "Target Revenue", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "targetDeals", Column: "target_deals", Label: "Target Deals", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Default: "IDR", Upper: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// CommissionSchemes are the commission plans (percent per line, tiers by
// achievement, flat per deal), effective by date.
var CommissionSchemes = &resource.Def{
	Key: "crm.commission_scheme", Module: "crm", Perm: "crm.commission_scheme", Path: "/api/v1/crm/commission-schemes",
	Table: "crm.sales_commission_schemes", Name: "Commission Scheme", Plural: "Commission Schemes", Tag: "Sales Commission",
	PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "effective_from DESC, code, id", SchemaName: "CommissionScheme",
	Fields: []resource.Field{resource.Code("Scheme Code"), resource.Name(),
		{Name: "schemeType", Column: "scheme_type", Label: "Scheme Type", Kind: resource.Enum, Enum: []string{"percent", "tiered", "flat"},
			Default: "percent", Filter: true},
		{Name: "basis", Column: "basis", Label: "Basis", Kind: resource.Enum, Enum: []string{"net", "total"}, Default: "net"},
		{Name: "rates", Column: "rates", Label: "Rates per Business Line ([{line, percent, flatAmount}])", Kind: resource.JSONList, Default: "[]"},
		{Name: "tiers", Column: "tiers", Label: "Tiers ([{minAchievementPercent, percent}])", Kind: resource.JSONList, Default: "[]"},
		{Name: "userId", Column: "user_id", Label: "Sales Executive (empty: all)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		{Name: "teamId", Column: "team_id", Label: "Sales Team (empty: all)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.sales_teams", SameProperty: true, Label: "sales team"}},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Required: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		{Name: "clawbackDays", Column: "clawback_days", Label: "Clawback Window (days)", Kind: resource.Int, Default: int64(90), Min: resource.Min(0)},
		resource.Status("active", "inactive")},
}

// Defs are the resource definitions of CRM Sales.
func Defs() []*resource.Def {
	return []*resource.Def{Pipelines, PipelineStages, Teams, TeamMembers, Targets, CommissionSchemes}
}

// SchemeRate is one rate of a commission scheme.
type SchemeRate struct {
	Line       string `json:"line"`
	Percent    string `json:"percent,omitempty"`
	FlatAmount string `json:"flatAmount,omitempty"`
}

// SchemeTier is one achievement tier of a tiered scheme.
type SchemeTier struct {
	MinAchievementPercent string `json:"minAchievementPercent"`
	Percent               string `json:"percent"`
}

func init() {
	Pipelines.Hooks = resource.Hooks{BeforeWrite: linesBeforeWrite}
	Teams.Hooks = resource.Hooks{BeforeWrite: linesBeforeWrite}
	TeamMembers.Hooks = resource.Hooks{BeforeWrite: linesBeforeWrite}
	PipelineStages.Hooks = resource.Hooks{BeforeWrite: stageBeforeWrite}
	CommissionSchemes.Hooks = resource.Hooks{BeforeWrite: schemeBeforeWrite}
}

func linesBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if ls, ok := v["lines"].([]string); ok {
		for _, l := range ls {
			if !oneOf(Lines, l) {
				return enumErr("lines", Lines)
			}
		}
	}
	return nil
}

// stageBeforeWrite keeps one won and one lost stage per pipeline.
func stageBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	kind, _ := v["kind"].(string)
	if kind != "won" && kind != "lost" {
		return nil
	}
	pipeline := v["pipelineId"]
	self := ""
	if before != nil {
		pipeline, self = before["pipelineId"], str(before["id"])
		if before["kind"] == kind {
			return nil
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.sales_pipeline_stages WHERE pipeline_id = $1::uuid AND kind = $2
		AND archived_at IS NULL AND id::text <> $3)`, str(pipeline), kind, self).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return handle.Invalid("kind", "duplicate_"+kind+"_stage", "the pipeline already has a "+kind+" stage")
	}
	return nil
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

// schemeBeforeWrite validates the rates and tiers of a commission scheme.
func schemeBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if raw, ok := v["rates"].(string); ok {
		var rates []SchemeRate
		if err := json.Unmarshal([]byte(raw), &rates); err != nil {
			return handle.Invalid("rates", "invalid", "a list of {line, percent, flatAmount}")
		}
		for _, r := range rates {
			if r.Line != "" && r.Line != "*" && !oneOf(Lines, r.Line) {
				return enumErr("rates.line", Lines)
			}
			if r.Percent != "" {
				if p, err := handle.Decimal("rates.percent", r.Percent, dec("0")); err != nil || p.IsNegative() || p.GreaterThan(hundred) {
					return handle.Invalid("rates.percent", "invalid", "percent between 0 and 100")
				}
			}
			if r.FlatAmount != "" {
				if a, err := handle.Decimal("rates.flatAmount", r.FlatAmount, dec("0")); err != nil || a.IsNegative() {
					return handle.Invalid("rates.flatAmount", "invalid", "a positive amount")
				}
			}
		}
	}
	if raw, ok := v["tiers"].(string); ok {
		var tiers []SchemeTier
		if err := json.Unmarshal([]byte(raw), &tiers); err != nil {
			return handle.Invalid("tiers", "invalid", "a list of {minAchievementPercent, percent}")
		}
		for _, t := range tiers {
			if _, err := handle.Decimal("tiers.minAchievementPercent", t.MinAchievementPercent, dec("0")); err != nil {
				return err
			}
			if p, err := handle.Decimal("tiers.percent", t.Percent, dec("0")); err != nil || p.IsNegative() || p.GreaterThan(hundred) {
				return handle.Invalid("tiers.percent", "invalid", "percent between 0 and 100")
			}
		}
	}
	return nil
}

var _ = errs.NotFound
