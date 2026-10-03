// Package commercial is the Commercial module (POS + BOM, Pricing &
// Promotion, Voucher & Prepaid). P0 owns Tax & Service rules with effective
// dates (FR-MD-04, FR-MD-08) and the Product / Outlet foundation entities.
package commercial

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

// TaxServiceRules: values are configured by the club, never hard-coded.
var TaxServiceRules = &resource.Def{
	Key: "commercial.tax_service", Module: "commercial", Perm: "commercial.tax_service", Path: "/api/v1/commercial/tax-service-rules",
	Table: "commercial.tax_service_rules", Name: "Tax & Service Rule", Plural: "Tax & Service Rules", Tag: "Tax & Service",
	PropertyScoped: true, CodeField: "", NoDelete: true, OrderBy: "sequence, code, effective_from DESC", SchemaName: "TaxServiceRule",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true, CreateOnly: true,
			Pattern: regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`), PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true, Filter: true},
		{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 80, Search: true},
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: []string{"tax", "service"}, Required: true, CreateOnly: true, Filter: true},
		{Name: "ratePercent", Column: "rate_percent", Label: "Rate (%)", Kind: resource.Decimal, Required: true, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "basis", Column: "basis", Label: "Calculation Basis", Kind: resource.Enum, Enum: []string{"net_amount", "net_plus_service"}, Required: true},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Pricing Mode", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Required: true, Filter: true},
		{Name: "sequence", Column: "sequence", Label: "Sequence", Kind: resource.Int, Default: int64(1)},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Timestamp, Required: true, CreateOnly: true},
		resource.Status("active", "inactive"),
	},
}

func init() {
	TaxServiceRules.Hooks = resource.Hooks{BeforeWrite: taxBeforeWrite}
}

// taxBeforeWrite enforces FR-MD-08: a version that is already effective is
// immutable (except deactivation); new versions cannot be back-dated once a
// version of the same code exists.
func taxBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	now := clock.Now()
	if before != nil {
		eff, _ := before["effectiveFrom"].(time.Time)
		if !eff.After(now) {
			for k := range v {
				if k != "status" {
					return errs.Conflict("rule_already_effective",
						"this version is already effective; add a new version with a later effective date instead")
				}
			}
		}
		return nil
	}
	pid, _ := reqctx.Property(ctx)
	eff, _ := v["effectiveFrom"].(time.Time)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.tax_service_rules WHERE property_id = $1 AND code = $2)`,
		pid, v["code"]).Scan(&exists); err != nil {
		return err
	}
	if exists && eff.Before(now.Add(-time.Minute)) {
		return errs.Validation("effective_date_past", "a new version cannot take effect in the past",
			errs.Field("effectiveFrom", "past", "existing transactions must not change; choose now or a future date"))
	}
	return nil
}

// Products and Outlets are foundation entities (PRD §5.2).
var Products = &resource.Def{
	Key: "commercial.product", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/products", Table: "commercial.products",
	Name: "Product", Plural: "Products", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "category", Column: "category", Label: "Category", Kind: resource.String, Max: 80, Filter: true, Search: true},
		{Name: "unit", Column: "unit", Label: "Unit", Kind: resource.String, Max: 20},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var Outlets = &resource.Def{
	Key: "commercial.outlet", Module: "commercial", Perm: "commercial.outlet", Path: "/api/v1/commercial/outlets", Table: "commercial.outlets",
	Name: "Outlet", Plural: "Outlets", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "outletType", Column: "outlet_type", Label: "Outlet Type", Kind: resource.String, Max: 40, Filter: true},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// ── calculation ───────────────────────────────────────────────────────────

// Rule is one rule version in force.
type Rule struct {
	ID            uuid.UUID
	Code, Name    string
	Kind          string
	Rate          decimal.Decimal
	Basis         string
	PricingMode   string
	Sequence      int
	EffectiveFrom time.Time
}

// RulesAt returns, per code, the active version with the latest
// effective_from not after at.
func RulesAt(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) ([]Rule, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (code) id, code, name, kind, rate_percent::text, basis, pricing_mode, sequence, effective_from, status
		FROM commercial.tax_service_rules WHERE property_id = $1 AND effective_from <= $2 ORDER BY code, effective_from DESC`, property, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var r Rule
		var rate, status string
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Kind, &rate, &r.Basis, &r.PricingMode, &r.Sequence, &r.EffectiveFrom, &status); err != nil {
			return nil, err
		}
		if status != "active" {
			continue
		}
		r.Rate, _ = decimal.NewFromString(rate)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "service" // service before tax
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, rows.Err()
}

// Line is one computed tax or service component.
type Line struct {
	RuleID        uuid.UUID `json:"ruleId"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind" enum:"tax,service"`
	RatePercent   string    `json:"ratePercent"`
	Basis         string    `json:"basis"`
	Amount        string    `json:"amount"`
	EffectiveFrom time.Time `json:"effectiveFrom"`
}

// Breakdown is the result of a calculation.
type Breakdown struct {
	At          time.Time `json:"at"`
	Currency    string    `json:"currency"`
	PricingMode string    `json:"pricingMode" enum:"nett,plus_plus"`
	InputAmount string    `json:"inputAmount"`
	NetAmount   string    `json:"netAmount"`
	Lines       []Line    `json:"lines"`
	Total       string    `json:"total"`
}

func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" {
		return 0
	}
	return 2
}

// Calculate computes service and tax for amount at a time. In plus_plus
// mode amount is the net price; in nett mode amount already includes tax and
// service and the net is derived. Rounding residue is absorbed by the net
// so that net + lines == total exactly.
func Calculate(rules []Rule, amount decimal.Decimal, currency string, at time.Time) Breakdown {
	mode := "plus_plus"
	if len(rules) > 0 {
		mode = rules[0].PricingMode
	}
	return CalculateMode(rules, amount, currency, at, mode)
}

// CalculateMode is Calculate with an explicit Nett / ++ mode (a rate plan
// decides whether its prices include tax & service, FR-PRC-06).
func CalculateMode(rules []Rule, amount decimal.Decimal, currency string, at time.Time, mode string) Breakdown {
	if mode != "nett" {
		mode = "plus_plus"
	}
	hundred := decimal.NewFromInt(100)
	// factor(net=1): total per unit of net
	compute := func(net decimal.Decimal, round bool) ([]decimal.Decimal, decimal.Decimal) {
		amts := make([]decimal.Decimal, len(rules))
		service := decimal.Zero
		total := net
		for i, r := range rules {
			base := net
			if r.Basis == "net_plus_service" {
				base = net.Add(service)
			}
			a := base.Mul(r.Rate).Div(hundred)
			if round {
				a = a.Round(places(currency))
			}
			if r.Kind == "service" {
				service = service.Add(a)
			}
			amts[i] = a
			total = total.Add(a)
		}
		return amts, total
	}
	net := amount
	if mode == "nett" {
		_, k := compute(decimal.NewFromInt(1), false)
		net = amount.Div(k).Round(places(currency))
	}
	amts, total := compute(net, true)
	if mode == "nett" {
		// absorb rounding so the guest pays exactly the nett price
		net = net.Add(amount.Sub(total))
		total = amount
	}
	b := Breakdown{At: at, Currency: currency, PricingMode: mode, InputAmount: amount.String(), NetAmount: net.StringFixed(places(currency)),
		Total: total.StringFixed(places(currency)), Lines: []Line{}}
	for i, r := range rules {
		b.Lines = append(b.Lines, Line{RuleID: r.ID, Code: r.Code, Name: r.Name, Kind: r.Kind, RatePercent: r.Rate.String(), Basis: r.Basis,
			Amount: amts[i].StringFixed(places(currency)), EffectiveFrom: r.EffectiveFrom})
	}
	return b
}

type CalculateRequest struct {
	Amount string     `json:"amount"`
	At     *time.Time `json:"at,omitempty" doc:"Calculation time; default now"`
}

// Module serves the calculation endpoint.
type Module struct{ DB *dbtx.DB }

func (m *Module) calculate(w http.ResponseWriter, r *http.Request) {
	var req CalculateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	amt, err := decimal.NewFromString(req.Amount)
	if err != nil || amt.IsNegative() {
		httpx.WriteError(w, r, errs.Validation("invalid_amount", "invalid amount", errs.Field("amount", "invalid", "non-negative decimal")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	at := clock.Now()
	if req.At != nil {
		at = req.At.UTC()
	}
	var b Breakdown
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur); err != nil {
			return err
		}
		rules, err := RulesAt(ctx, tx, pid, at)
		if err != nil {
			return err
		}
		b = Calculate(rules, amt, cur, at)
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

// Register adds commercial routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	eng.Register(reg, TaxServiceRules)
	eng.Register(reg, Products)
	eng.Register(reg, Outlets)
	for _, d := range []*resource.Def{DayTypes, TimeBands, RatePlans, PricingRules} {
		eng.Register(reg, d)
	}
	m.registerPricing(reg)
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/tax-service-rules:calculate", Module: "commercial", Tag: "Tax & Service",
		Summary: "Calculate tax and service for an amount at a point in time", Permission: "commercial.tax_service.view",
		Scope: route.ScopeProperty, Request: CalculateRequest{}, Response: Breakdown{}, Status: http.StatusOK,
		NoAudit: "read-only calculation", Handler: m.calculate})
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	pricingAll := append(resource.AllActions(DayTypes), "commercial.price_override.apply", "commercial.price_override.approve_any")
	return catalog.Contribution{
		Permissions: append(append(append(append(catalog.P("commercial", "tax_service", "view", "create", "update", "export"),
			resource.Permissions(Products)...), resource.Permissions(Outlets)...), resource.Permissions(DayTypes)...),
			catalog.P("commercial", "price_override", "apply", "approve_any")...),
		RolePermissions: map[string][]string{
			"property_admin":    append(append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update", "commercial.tax_service.export"}, resource.AllActions(Products, Outlets)...), pricingAll...),
			"finance_manager":   append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update"}, pricingAll...),
			"accountant":        {"commercial.tax_service.view", "commercial.pricing.view", "commercial.pricing.export"},
			"general_manager":   {"commercial.pricing.view", "commercial.price_override.apply"},
			"club_manager":      {"commercial.pricing.view", "commercial.price_override.apply"},
			"golf_manager":      {"commercial.pricing.view", "commercial.pricing.create", "commercial.pricing.update", "commercial.pricing.export", "commercial.price_override.apply"},
			"golf_admin":        {"commercial.pricing.view"},
			"reservation_staff": {"commercial.pricing.view"},
			"front_desk":        {"commercial.pricing.view"},
			"membership_admin":  {"commercial.pricing.view"},
			"outlet_manager":    {"commercial.product.view", "commercial.outlet.view"},
			"cashier":           {"commercial.outlet.view"},
			"pos_staff":         {"commercial.outlet.view"},
			"kitchen_staff":     {"commercial.outlet.view"},
		},
	}
}
