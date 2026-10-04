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

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/realtime"
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
		// POS (PRD P2 EP-20)
		{Name: "productType", Column: "product_type", Label: "Product Type", Kind: resource.Enum, Enum: []string{"food", "beverage", "retail", "service", "package"}, Default: "food", Filter: true},
		{Name: "price", Column: "price", Label: "Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "memberPrice", Column: "member_price", Label: "Member Price", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Max: 60, Search: true},
		{Name: "kitchenStation", Column: "kitchen_station", Label: "Kitchen Station", Kind: resource.String, Max: 40, Filter: true},
		{Name: "outletIds", Column: "outlet_ids", Label: "Outlets (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "comboItems", Column: "combo_items", Label: "Combo / Package Items", Kind: resource.JSONList, Default: "[]"},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component (default: outlet)", Kind: resource.Enum, Enum: billing.RevenueComponents},
		{Name: "voucherTypeId", Column: "voucher_type_id", Label: "Sells Voucher Type", Kind: resource.UUID, Ref: &resource.Ref{Table: "commercial.voucher_types", SameProperty: true, Label: "voucher type"}},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var Outlets = &resource.Def{
	Key: "commercial.outlet", Module: "commercial", Perm: "commercial.outlet", Path: "/api/v1/commercial/outlets", Table: "commercial.outlets",
	Name: "Outlet", Plural: "Outlets", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "outletType", Column: "outlet_type", Label: "Outlet Type", Kind: resource.String, Max: 40, Filter: true},
		// POS (PRD P2 FR-POS-01)
		{Name: "taxCodes", Column: "tax_codes", Label: "Tax & Service Codes (empty = all)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Pricing Mode", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "nett"},
		{Name: "openingTime", Column: "opening_time", Label: "Opening Time", Kind: resource.Time},
		{Name: "closingTime", Column: "closing_time", Label: "Closing Time", Kind: resource.Time},
		{Name: "kdsStations", Column: "kds_stations", Label: "Kitchen Stations", Kind: resource.StringList, Default: []string{}},
		{Name: "printerDevice", Column: "printer_device", Label: "Receipt Printer (bridge device)", Kind: resource.String, Max: 80},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.Enum, Enum: billing.RevenueComponents, Default: "fnb"},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// ProductVariants, ModifierGroups, Modifiers and Menus (FR-POS-02).
var ProductVariants = &resource.Def{
	Key: "commercial.product_variant", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/product-variants",
	Table: "commercial.product_variants", Name: "Variant", Plural: "Variants", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "productId", Column: "product_id", Label: "Product", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "priceDelta", Column: "price_delta", Label: "Price Difference", Kind: resource.Decimal, Default: "0"},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive")},
}

var ModifierGroups = &resource.Def{
	Key: "commercial.modifier_group", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/modifier-groups",
	Table: "commercial.modifier_groups", Name: "Modifier Group", Plural: "Modifier Groups", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "minSelect", Column: "min_select", Label: "Minimum", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "maxSelect", Column: "max_select", Label: "Maximum", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "productIds", Column: "product_ids", Label: "Products", Kind: resource.StringList, Default: []string{}},
		resource.Status("active", "inactive")},
}

var Modifiers = &resource.Def{
	Key: "commercial.modifier", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/modifiers", Table: "commercial.modifiers",
	Name: "Modifier", Plural: "Modifiers", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "groupId", Column: "group_id", Label: "Modifier Group", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.modifier_groups", SameProperty: true, Label: "modifier group"}},
		{Name: "priceDelta", Column: "price_delta", Label: "Price Difference", Kind: resource.Decimal, Default: "0"},
		resource.Status("active", "inactive")},
}

var Menus = &resource.Def{
	Key: "commercial.menu", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/menus", Table: "commercial.menus",
	Name: "Menu", Plural: "Menus", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "outletId", Column: "outlet_id", Label: "Outlet", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "availableFrom", Column: "available_from", Label: "Available From", Kind: resource.Time},
		{Name: "availableTo", Column: "available_to", Label: "Available To", Kind: resource.Time},
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays", Kind: resource.IntList, Enum: []string{"1", "2", "3", "4", "5", "6", "7"}, Default: []int64{1, 2, 3, 4, 5, 6, 7}},
		{Name: "productIds", Column: "product_ids", Label: "Products", Kind: resource.StringList, Default: []string{}},
		{Name: "channels", Column: "channels", Label: "Channels", Kind: resource.StringList, Enum: []string{"pos", "member_app", "caddy_tablet", "website"}, Default: []string{"pos"}},
		resource.Status("active", "inactive")},
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

// Module is the Commercial module: tax & service, pricing, voucher &
// prepaid, POS and F&B.
type Module struct {
	DB        *dbtx.DB
	Billing   *billing.Service
	Events    *outbox.Bus
	Approvals *approval.Engine
	Notify    notify.Sender
	Realtime  *realtime.Hub
}

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
	m.registerMe(reg)
	eng.Register(reg, TaxServiceRules)
	eng.Register(reg, Products)
	eng.Register(reg, Outlets)
	for _, d := range []*resource.Def{DayTypeSets, DayTypes, TimeBands, RatePlans, PackageRates, PricingRules} {
		eng.Register(reg, d)
	}
	m.registerPricing(reg)
	m.registerVouchers(reg, eng)
	m.registerPOS(reg, eng)
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/tax-service-rules:calculate", Module: "commercial", Tag: "Tax & Service",
		Summary: "Calculate tax and service for an amount at a point in time", Permission: "commercial.tax_service.view",
		Scope: route.ScopeProperty, Request: CalculateRequest{}, Response: Breakdown{}, Status: http.StatusOK,
		NoAudit: "read-only calculation", Handler: m.calculate})
}

// Contribution returns catalogue entries (P1 pricing foundation + P2
// vouchers, POS & F&B).
func Contribution() catalog.Contribution { return catalog.Merge(p1Contribution(), p2Contribution()) }

func p1Contribution() catalog.Contribution {
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

func p2Contribution() catalog.Contribution {
	perms := append(append(catalog.P("commercial", "tax_service", "view", "create", "update", "export"),
		resource.Permissions(Products)...), resource.Permissions(Outlets)...)
	perms = append(perms, resource.Permissions(PricingRules)...)
	perms = append(perms, resource.Permissions(VoucherTypes)...)
	perms = append(perms, catalog.P("commercial", "order", "view", "create", "pay", "void", "refund")...)
	perms = append(perms, catalog.P("commercial", "pos", "discount", "discount_override")...)
	perms = append(perms, catalog.P("commercial", "shift", "view", "manage")...)
	perms = append(perms, catalog.P("commercial", "kitchen", "view", "update")...)
	perms = append(perms, catalog.P("commercial", "voucher", "view", "sell", "issue", "redeem", "transfer", "extend", "adjust", "void")...)
	voucherFront := []string{"commercial.voucher_type.view", "commercial.voucher.view", "commercial.voucher.sell", "commercial.voucher.redeem"}
	voucherAll := append(append(resource.AllActions(VoucherTypes), voucherFront...), "commercial.voucher.issue", "commercial.voucher.transfer",
		"commercial.voucher.extend", "commercial.voucher.adjust", "commercial.voucher.void")
	pricingAll := resource.AllActions(PricingRules)
	pricingView := []string{"commercial.pricing.view"}
	rp := map[string][]string{
		"property_admin":  append(append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update", "commercial.tax_service.export"}, resource.AllActions(Products, Outlets)...), pricingAll...),
		"finance_manager": append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update"}, pricingAll...),
		"accountant":      append([]string{"commercial.tax_service.view"}, pricingView...),
		"outlet_manager":  append([]string{"commercial.product.view", "commercial.outlet.view"}, pricingView...),
		"cashier":         append([]string{"commercial.outlet.view"}, pricingView...),
		"pos_staff":       append([]string{"commercial.outlet.view"}, pricingView...),
		"kitchen_staff":   {"commercial.outlet.view"},
	}
	for _, role := range []string{"general_manager", "club_manager", "resort_manager", "golf_manager", "golf_admin", "sport_club_manager",
		"sport_club_receptionist", "reservation_staff", "front_desk", "membership_admin", "membership_manager", "driving_range_staff", "banquet_sales"} {
		rp[role] = append(rp[role], pricingView...)
	}
	for _, role := range []string{"property_admin", "finance_manager", "outlet_manager", "sport_club_manager"} {
		rp[role] = append(rp[role], voucherAll...)
	}
	for _, role := range []string{"cashier", "pos_staff", "sport_club_receptionist", "front_desk", "driving_range_staff", "reservation_staff",
		"membership_admin", "golf_admin"} {
		rp[role] = append(rp[role], voucherFront...)
	}
	for _, role := range []string{"accountant", "general_manager", "club_manager", "marketing_staff", "crm_admin"} {
		rp[role] = append(rp[role], "commercial.voucher_type.view", "commercial.voucher.view")
	}
	posAll := []string{"commercial.order.view", "commercial.order.create", "commercial.order.pay", "commercial.order.void", "commercial.order.refund",
		"commercial.pos.discount", "commercial.pos.discount_override", "commercial.shift.view", "commercial.shift.manage", "commercial.kitchen.view", "commercial.kitchen.update"}
	posCashier := []string{"commercial.order.view", "commercial.order.create", "commercial.order.pay", "commercial.order.void", "commercial.pos.discount",
		"commercial.shift.view", "commercial.shift.manage", "commercial.kitchen.view", "commercial.product.view"}
	for _, role := range []string{"property_admin", "outlet_manager"} {
		rp[role] = append(rp[role], posAll...)
		rp[role] = append(rp[role], resource.AllActions(Products)...)
	}
	for _, role := range []string{"cashier", "pos_staff", "driving_range_staff", "sport_club_receptionist"} {
		rp[role] = append(rp[role], posCashier...)
	}
	rp["kitchen_staff"] = append(rp["kitchen_staff"], "commercial.kitchen.view", "commercial.kitchen.update", "commercial.order.view")
	for _, role := range []string{"caddy", "front_desk", "banquet_manager", "reservation_staff"} {
		rp[role] = append(rp[role], "commercial.order.view", "commercial.order.create", "commercial.product.view", "commercial.outlet.view")
	}
	for _, role := range []string{"general_manager", "finance_manager", "accountant", "club_manager"} {
		rp[role] = append(rp[role], "commercial.order.view", "commercial.shift.view")
	}
	rp["marketing_staff"] = append(rp["marketing_staff"], "commercial.voucher.issue")
	rp["crm_admin"] = append(rp["crm_admin"], "commercial.voucher.issue")
	return catalog.Contribution{Permissions: perms, RolePermissions: rp}
}
