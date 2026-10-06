package commercial

// PRD P5 EP-22 Advanced Package Management (lanjutan P3 EP-11, PRD P5 §6 #4):
// the master data of multi-business packages — Package Component Rules
// (choice groups, sequence & time gap, service window), Package Capacity
// (allotment, blackout, time block per package or component), package cost
// rules for the Package Profitability, and payment templates per package
// type — with the Package Policies (large-quota inventory check, cost
// basis). Rates, limits and formulas are configuration (PRD P5 §6 #18):
// capacity and cost rules carry effective dates; policies are versioned.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// P5 package enums.
var (
	CapacityTypes  = []string{"blackout", "allotment", "time_block"}
	CapacityBasis  = []string{"bookings", "pax", "units"}
	CostTypes      = []string{"other", "caddy_fee", "room_cost", "commission", "labour", "cogs"}
	CostBasis      = []string{"per_unit", "per_booking", "per_pax", "percent_of_revenue"}
	activeInactive = []string{"active", "inactive"}
	code20Re       = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
)

// PackageComponentRules (FR-PKG-P5-01): conditional components of a
// multi-business package.
var PackageComponentRules = &resource.Def{
	Key: "commercial.package_component_rule", Module: "commercial", Perm: "commercial.package_component_rule",
	Path: "/api/v1/commercial/package-component-rules", Table: "commercial.package_component_rules", Name: "Package Component Rule",
	Plural: "Package Component Rules", Tag: "Packages", PropertyScoped: true, OrderBy: "component_id, created_at", SchemaName: "PackageComponentRule",
	Fields: []resource.Field{
		{Name: "componentId", Column: "component_id", Label: "Component", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.package_components", SameProperty: true, Label: "package component"}},
		{Name: "choicePick", Column: "choice_pick", Label: "Components to Pick in the Group", Kind: resource.Int, Default: int64(1), Min: resource.Min(1),
			MaxN: resource.Max(20)},
		{Name: "choiceGroup", Column: "choice_group", Label: "Choice Group (customer picks among the components of the group)", Kind: resource.String, Max: 20,
			Upper: true, Pattern: code20Re, PatternMsg: "1–20 characters: A–Z, 0–9, - or _"},
		{Name: "afterComponentId", Column: "after_component_id", Label: "Starts After Component", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "commercial.package_components", SameProperty: true, Label: "package component"}},
		{Name: "minGapMinutes", Column: "min_gap_minutes", Label: "Minimum Gap (minutes)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0),
			MaxN: resource.Max(10080)},
		{Name: "maxGapMinutes", Column: "max_gap_minutes", Label: "Maximum Gap (minutes)", Kind: resource.Int, Min: resource.Min(0), MaxN: resource.Max(10080)},
		{Name: "windowStart", Column: "window_start", Label: "Served From", Kind: resource.Time},
		{Name: "windowEnd", Column: "window_end", Label: "Served Until", Kind: resource.Time},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: activeInactive, Default: "active", Filter: true},
	},
}

// PackageCapacities (FR-PKG-P5-02): allotments, blackouts and time blocks of
// a package (start date) or of one of its components (service date).
var PackageCapacities = &resource.Def{
	Key: "commercial.package_capacity", Module: "commercial", Perm: "commercial.package_capacity", Path: "/api/v1/commercial/package-capacities",
	Table: "commercial.package_capacities", Name: "Package Capacity", Plural: "Package Capacity", Tag: "Packages", PropertyScoped: true,
	OrderBy: "package_id, date_from, created_at", SchemaName: "PackageCapacityRule",
	Fields: []resource.Field{
		{Name: "packageId", Column: "package_id", Label: "Package", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.packages", SameProperty: true, Label: "package"}},
		{Name: "componentId", Column: "component_id", Label: "Component (empty = the package)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.package_components", SameProperty: true, Label: "package component"}},
		{Name: "capacityType", Column: "capacity_type", Label: "Capacity Type", Kind: resource.Enum, Enum: CapacityTypes, Required: true, Filter: true},
		{Name: "dateFrom", Column: "date_from", Label: "From", Kind: resource.Date, Required: true},
		{Name: "dateTo", Column: "date_to", Label: "Until (empty = open)", Kind: resource.Date},
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays (empty = every day)", Kind: resource.IntList, Enum: []string{"1", "2", "3", "4", "5", "6", "7"},
			Default: []int64{}},
		{Name: "quota", Column: "quota", Label: "Quota (allotment / per time block)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "basis", Column: "basis", Label: "Counted In", Kind: resource.Enum, Enum: CapacityBasis, Default: "bookings"},
		{Name: "blockMinutes", Column: "block_minutes", Label: "Time Block (minutes)", Kind: resource.Int, Min: resource.Min(5), MaxN: resource.Max(1440)},
		{Name: "windowStart", Column: "window_start", Label: "Window From", Kind: resource.Time},
		{Name: "windowEnd", Column: "window_end", Label: "Window Until", Kind: resource.Time},
		{Name: "reason", Column: "reason", Label: "Reason", Kind: resource.Text, Max: 500},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: activeInactive, Default: "active", Filter: true},
	},
}

// PackageCostRules (FR-PKG-P5-04): direct costs of a package or component
// (caddy fee, room cost, commission …) next to the BOM cost of P4.
var PackageCostRules = &resource.Def{
	Key: "commercial.package_cost_rule", Module: "commercial", Perm: "commercial.package_cost_rule", Path: "/api/v1/commercial/package-cost-rules",
	Table: "commercial.package_cost_rules", Name: "Package Cost Rule", Plural: "Package Cost Rules", Tag: "Packages", PropertyScoped: true,
	OrderBy: "package_id, created_at", SchemaName: "PackageCostRule",
	Fields: []resource.Field{
		{Name: "packageId", Column: "package_id", Label: "Package", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.packages", SameProperty: true, Label: "package"}},
		{Name: "componentId", Column: "component_id", Label: "Component (empty = per booking)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.package_components", SameProperty: true, Label: "package component"}},
		{Name: "costType", Column: "cost_type", Label: "Cost Type", Kind: resource.Enum, Enum: CostTypes, Required: true, Filter: true},
		{Name: "basis", Column: "basis", Label: "Basis", Kind: resource.Enum, Enum: CostBasis, Default: "per_unit"},
		{Name: "amount", Column: "amount", Label: "Amount (or percent of revenue)", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From (service date)", Kind: resource.Date},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: activeInactive, Default: "active", Filter: true},
	},
}

// PaymentTemplateLine is one due amount of a payment template.
type PaymentTemplateLine struct {
	Label   string `json:"label"`
	Kind    string `json:"kind,omitempty" enum:"down_payment,installment,final"`
	Percent string `json:"percent,omitempty" doc:"Of the booking total; the last line takes the rest"`
	Days    int    `json:"days"`
	From    string `json:"from" enum:"booking,before_start" doc:"Due N days after the booking date or N days before the start date"`
}

// DefaultPaymentTemplateLines is a down payment now and the rest H-7.
var DefaultPaymentTemplateLines = []map[string]any{{"label": "Down Payment", "kind": "down_payment", "percent": "30", "days": 0, "from": "booking"},
	{"label": "Final Payment", "kind": "final", "days": 7, "from": "before_start"}}

// PackagePaymentTemplates (FR-PKG-P5-05): payment schedule per package type.
var PackagePaymentTemplates = &resource.Def{
	Key: "commercial.package_payment_template", Module: "commercial", Perm: "commercial.package_payment_template",
	Path: "/api/v1/commercial/package-payment-templates", Table: "commercial.package_payment_templates", Name: "Package Payment Template",
	Plural: "Package Payment Templates", Tag: "Packages", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "package_type, min_amount, code",
	SchemaName: "PackagePaymentTemplate",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true, Pattern: code40Re,
			PatternMsg: "1–40 characters: A–Z, 0–9, - or _", Search: true},
		resource.Name(),
		{Name: "packageType", Column: "package_type", Label: "Package Type", Kind: resource.Enum, Enum: PackageTypes, Required: true, Filter: true},
		{Name: "minAmount", Column: "min_amount", Label: "Applies From Package Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "lines", Column: "lines", Label: "Due Amounts (label, kind, percent, days, from booking | before_start)", Kind: resource.JSONList,
			Default: DefaultPaymentTemplateLines},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: activeInactive, Default: "active", Filter: true},
	},
}

func init() {
	PackageComponentRules.Hooks = resource.Hooks{BeforeWrite: componentRuleBeforeWrite}
	PackageCapacities.Hooks = resource.Hooks{BeforeWrite: capacityBeforeWrite}
	PackageCostRules.Hooks = resource.Hooks{BeforeWrite: costRuleBeforeWrite}
	PackagePaymentTemplates.Hooks = resource.Hooks{BeforeWrite: paymentTemplateBeforeWrite}
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyPackage, Category: "Pricing Policies", Name: "Package capacity, inventory check & profitability",
		Description: "Large-quota packages: inventory requirement (BOM) shown or enforced; cost basis of the Package Profitability; default time block",
		Default:     DefaultPackagePolicy})
	bookingRules = applyBookingRules
}

// PolicyPackage is the club policy of advanced packages.
const PolicyPackage = "commercial.package_advanced"

// PackagePolicy is the Package Policies document (PRD P5 EP-22).
type PackagePolicy struct {
	LargeQuotaPax       int    `json:"largeQuotaPax" doc:"Allotments or bookings of at least this many pax show the inventory requirement (BOM) before they are sold"`
	InventoryCheck      string `json:"inventoryCheck" doc:"warn | block: a booking of at least largeQuotaPax is refused when an ingredient is short"`
	CostBasis           string `json:"costBasis" doc:"average (stock value ÷ quantity, else standard cost) | standard (item standard cost)"`
	DefaultBlockMinutes int    `json:"defaultBlockMinutes" doc:"Time block length when a time block capacity has none"`
}

// DefaultPackagePolicy applies until the club configures it.
var DefaultPackagePolicy = PackagePolicy{LargeQuotaPax: 50, InventoryCheck: "warn", CostBasis: "average", DefaultBlockMinutes: 60}

// LoadPackagePolicy returns the policy in force.
func LoadPackagePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (PackagePolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyPackage, property, DefaultPackagePolicy)
	if p.InventoryCheck == "" {
		p.InventoryCheck = DefaultPackagePolicy.InventoryCheck
	}
	if p.CostBasis == "" {
		p.CostBasis = DefaultPackagePolicy.CostBasis
	}
	if p.DefaultBlockMinutes <= 0 {
		p.DefaultBlockMinutes = DefaultPackagePolicy.DefaultBlockMinutes
	}
	if p.LargeQuotaPax <= 0 {
		p.LargeQuotaPax = DefaultPackagePolicy.LargeQuotaPax
	}
	return p, err
}

func samePackage(ctx context.Context, tx pgx.Tx, component, other any) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.package_components a JOIN commercial.package_components b ON b.package_id = a.package_id
		WHERE a.id = $1::uuid AND b.id = $2::uuid)`, str(component), str(other)).Scan(&ok)
	return ok, err
}

func componentOfPackage(ctx context.Context, tx pgx.Tx, pkg, component any) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.package_components WHERE id = $1::uuid AND package_id = $2::uuid)`,
		str(component), str(pkg)).Scan(&ok)
	return ok, err
}

func componentRuleBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	m := merged(v, before)
	if after := str(m["afterComponentId"]); after != "" {
		if after == str(m["componentId"]) {
			return errs.Validation("invalid_sequence", "a component cannot follow itself", errs.Field("afterComponentId", "invalid", "another component"))
		}
		ok, err := samePackage(ctx, tx, m["componentId"], after)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Validation("invalid_sequence", "the previous component must be of the same package",
				errs.Field("afterComponentId", "invalid", "component of the same package"))
		}
	}
	if mx, ok := intOf(m["maxGapMinutes"]); ok {
		if mn, _ := intOf(m["minGapMinutes"]); mx < mn {
			return errs.Validation("invalid_gap", "the maximum gap is below the minimum", errs.Field("maxGapMinutes", "invalid", "≥ minimum gap"))
		}
	}
	if s, e := str(m["windowStart"]), str(m["windowEnd"]); s != "" && e != "" && e <= s {
		return errs.Validation("invalid_window", "the window ends before it starts", errs.Field("windowEnd", "invalid", "after the window start"))
	}
	return nil
}

func capacityBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	m := merged(v, before)
	if c := str(m["componentId"]); c != "" {
		ok, err := componentOfPackage(ctx, tx, m["packageId"], c)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Validation("component_not_in_package", "the component is not part of the package", errs.Field("componentId", "invalid", "component of the package"))
		}
	}
	if f, t := str(m["dateFrom"]), str(m["dateTo"]); f != "" && t != "" && t < f {
		return errs.Validation("invalid_period", "the end is before the start", errs.Field("dateTo", "invalid", "on or after the start"))
	}
	if s, e := str(m["windowStart"]), str(m["windowEnd"]); s != "" && e != "" && e <= s {
		return errs.Validation("invalid_window", "the window ends before it starts", errs.Field("windowEnd", "invalid", "after the window start"))
	}
	switch str(m["capacityType"]) {
	case "allotment":
		if _, ok := intOf(m["quota"]); !ok {
			return errs.Validation("quota_required", "an allotment needs its quota", errs.Field("quota", "required", "quota"))
		}
	case "time_block":
		if _, ok := intOf(m["quota"]); !ok {
			return errs.Validation("quota_required", "a time block needs its capacity per block", errs.Field("quota", "required", "capacity per block"))
		}
		if str(m["componentId"]) == "" {
			return errs.Validation("component_required", "time blocks apply to a component", errs.Field("componentId", "required", "component"))
		}
	}
	return nil
}

func costRuleBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	m := merged(v, before)
	if c := str(m["componentId"]); c != "" {
		ok, err := componentOfPackage(ctx, tx, m["packageId"], c)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Validation("component_not_in_package", "the component is not part of the package", errs.Field("componentId", "invalid", "component of the package"))
		}
	}
	if str(m["basis"]) == "percent_of_revenue" {
		if a, ok := decOf(m["amount"]); ok && a.GreaterThan(hundred) {
			return errs.Validation("invalid_percent", "a percent of revenue is at most 100", errs.Field("amount", "invalid", "0–100"))
		}
	}
	if f, t := str(m["effectiveFrom"]), str(m["effectiveTo"]); f != "" && t != "" && t < f {
		return errs.Validation("invalid_period", "the end is before the start", errs.Field("effectiveTo", "invalid", "on or after the start"))
	}
	return nil
}

func paymentTemplateBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, before map[string]any) error {
	m := merged(v, before)
	var raw []byte
	switch x := m["lines"].(type) {
	case string:
		raw = []byte(x)
	case []byte:
		raw = x
	case json.RawMessage:
		raw = x
	default:
		var err error
		if raw, err = json.Marshal(x); err != nil {
			return err
		}
	}
	var lines []PaymentTemplateLine
	if err := json.Unmarshal(raw, &lines); err != nil {
		return errs.Validation("invalid_lines", "due amounts: "+err.Error(), errs.Field("lines", "invalid", "label, kind, percent, days, from"))
	}
	if len(lines) == 0 {
		return errs.Validation("lines_required", "a template needs at least one due amount", errs.Field("lines", "required", "due amounts"))
	}
	sum := dec("0")
	for i, l := range lines {
		f := fmt.Sprintf("lines[%d]", i)
		if strings.TrimSpace(l.Label) == "" {
			return errs.Validation("invalid_lines", "every due amount needs a label", errs.Field(f+".label", "required", "label"))
		}
		if l.From != "booking" && l.From != "before_start" {
			return errs.Validation("invalid_lines", "from is booking or before_start", errs.Field(f+".from", "invalid", "booking | before_start"))
		}
		if l.Kind != "" && !slices.Contains([]string{"down_payment", "installment", "final"}, l.Kind) {
			return errs.Validation("invalid_lines", "kind is down_payment, installment or final", errs.Field(f+".kind", "invalid", "kind"))
		}
		if l.Days < 0 {
			return errs.Validation("invalid_lines", "days cannot be negative", errs.Field(f+".days", "invalid", "≥ 0"))
		}
		if i < len(lines)-1 {
			p, ok := decOf(l.Percent)
			if !ok || !p.IsPositive() {
				return errs.Validation("invalid_lines", "every due amount but the last needs its percent", errs.Field(f+".percent", "required", "percent"))
			}
			sum = sum.Add(p)
		}
	}
	if !sum.LessThan(hundred) && len(lines) > 1 {
		return errs.Validation("invalid_lines", "the percents before the last due amount must stay below 100%", errs.Field("lines", "invalid", "< 100%"))
	}
	return nil
}

// P5Contribution is the catalogue of advanced packages: permissions of the
// P5 master data, capacity and profitability, granted to the role
// templates (Product Overview §44, PRD P5 §4 Commercial persona).
func P5Contribution() catalog.Contribution {
	defs := []*resource.Def{PackageComponentRules, PackageCapacities, PackageCostRules, PackagePaymentTemplates}
	perms := resource.Permissions(defs...)
	perms = append(perms, catalog.P("commercial", "package_profitability", "view", "recalculate")...)
	all := append(resource.AllActions(defs...), "commercial.package_profitability.view", "commercial.package_profitability.recalculate")
	setup := append(resource.AllActions(PackageComponentRules, PackageCapacities, PackagePaymentTemplates), "commercial.package_cost_rule.view")
	capView := []string{"commercial.package_capacity.view", "commercial.package_component_rule.view"}
	finance := append(resource.AllActions(PackageCostRules), "commercial.package_profitability.view", "commercial.package_profitability.recalculate",
		"commercial.package_capacity.view", "commercial.package_payment_template.view")
	rp := map[string][]string{
		"property_admin":    all,
		"club_manager":      all,
		"resort_manager":    all,
		"general_manager":   append(append([]string{}, capView...), "commercial.package_profitability.view", "commercial.package_cost_rule.view"),
		"finance_manager":   finance,
		"accountant":        {"commercial.package_profitability.view", "commercial.package_cost_rule.view"},
		"outlet_manager":    append(append([]string{}, setup...), "commercial.package_profitability.view"),
		"reservation_staff": capView,
		"front_desk":        capView,
		"banquet_sales":     capView,
		"banquet_manager":   capView,
		"sales_executive":   capView,
		"golf_manager":      capView,
		"marketing_staff":   capView,
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: rp}
}
