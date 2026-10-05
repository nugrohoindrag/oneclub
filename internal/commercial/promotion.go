package commercial

// PRD P3 EP-10 Pricing & Promotion Engine: promotions are master data with
// a benefit (percent / amount discount, Buy N Get X, Buy N Price X, bundle
// price), a scope (channel, business line, outlet, product / category,
// service type, item, segment, membership type, CRM segment, weekday, time
// window, day kind, day type, validity, minimum purchase), stacking
// (non-stackable by default, priority, stack groups) and limits (budget,
// redemptions, per customer, promo codes). A promotion becomes Active only
// through activation — with approval when the Promotion Policies require it
// (FR-PRM-10); an Active promotion cannot be edited, it is deactivated and
// activated again as a new version.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// Promotion types (FR-PRM-01).
var PromoTypes = []string{"percent_discount", "amount_discount", "happy_hour", "buy_n_get_x", "buy_n_price_x", "bundle", "member_discount",
	"promo_code", "period"}

// PromoChannels are the sales channels of a promotion scope (FR-PRM-02).
var PromoChannels = []string{"pos", "member_app", "website", "back_office", "ops"}

// DayKinds are the day conditions of a promotion: weekday / weekend from
// the calendar, public holidays from the Day Calendar and peak / off-peak
// from the Pricing Policies (or the tee time's peak flag).
var DayKinds = []string{"weekday", "weekend", "holiday", "peak", "off_peak"}

// PromoServiceTypes are the pricing service types plus cross-line packages.
var PromoServiceTypes = append(append([]string{}, ServiceTypes...), "package")

// Promotion statuses (Naming Convention §31).
const (
	PromoDraft    = "draft"
	PromoPending  = "pending"
	PromoActive   = "active"
	PromoInactive = "inactive"
	PromoRejected = "rejected"
	PromoExpired  = "expired"
)

var promoCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,39}$`)

// Promotions master (FR-PRM-01..05).
var Promotions = &resource.Def{
	Key: "commercial.promotion", Module: "commercial", Perm: "commercial.promotion", Path: "/api/v1/commercial/promotions",
	Table: "commercial.promotions", Name: "Promotion", Plural: "Promotions", Tag: "Promotions", PropertyScoped: true, Archive: true, SchemaName: "CommercialPromotion",
	CodeField: "code", OrderBy: "priority, code, id",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true,
			Pattern: code40Re, PatternMsg: "1–40 characters: A–Z, 0–9, - or _", Search: true, Filter: true},
		resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "terms", Column: "terms", Label: "Terms & Conditions", Kind: resource.Text, Max: 4000},
		{Name: "promoType", Column: "promo_type", Label: "Promotion Type", Kind: resource.Enum, Enum: PromoTypes, Required: true, Filter: true},
		{Name: "discountPercent", Column: "discount_percent", Label: "Discount (%)", Kind: resource.Decimal, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "discountAmount", Column: "discount_amount", Label: "Discount Amount", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "perUnit", Column: "per_unit", Label: "Amount per Unit", Kind: resource.Bool, Default: false},
		{Name: "maxDiscount", Column: "max_discount", Label: "Maximum Discount per Transaction", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "buyQuantity", Column: "buy_quantity", Label: "Buy Quantity (N)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "getQuantity", Column: "get_quantity", Label: "Get Quantity (X)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "bundlePrice", Column: "bundle_price", Label: "Bundle / Group Price", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "bundleItems", Column: "bundle_items", Label: "Bundle Items", Kind: resource.JSONList, Default: "[]"},
		{Name: "channels", Column: "channels", Label: "Channels (empty = all)", Kind: resource.StringList, Enum: PromoChannels, Default: []string{}},
		{Name: "businessLines", Column: "business_lines", Label: "Business Lines (empty = all)", Kind: resource.StringList, Enum: billing.BusinessLines, Default: []string{}},
		{Name: "serviceTypes", Column: "service_types", Label: "Service Types (empty = all)", Kind: resource.StringList, Enum: PromoServiceTypes, Default: []string{}},
		{Name: "outletIds", Column: "outlet_ids", Label: "Outlets (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "productIds", Column: "product_ids", Label: "Products (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "categories", Column: "categories", Label: "Product Categories / Types (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "itemRefs", Column: "item_refs", Label: "Items (resource type, resource, package codes; empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "segments", Column: "segments", Label: "Customer Segments (pricing; empty = everyone)", Kind: resource.StringList, Default: []string{}},
		{Name: "membershipTypes", Column: "membership_types", Label: "Membership Types (codes)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "customerSegmentIds", Column: "customer_segment_ids", Label: "CRM Segments", Kind: resource.StringList, Default: []string{}},
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays (empty = every day)", Kind: resource.IntList, Enum: []string{"1", "2", "3", "4", "5", "6", "7"}, Default: []int64{}},
		{Name: "timeWindows", Column: "time_windows", Label: "Time Windows (happy hour)", Kind: resource.JSONList, Default: "[]"},
		{Name: "dayKinds", Column: "day_kinds", Label: "Day Kinds (weekday, weekend, holiday, peak, off-peak)", Kind: resource.StringList, Enum: DayKinds, Default: []string{}},
		{Name: "dayTypeCodes", Column: "day_type_codes", Label: "Day Types (codes)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "minPurchase", Column: "min_purchase", Label: "Minimum Purchase", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "minQuantity", Column: "min_quantity", Label: "Minimum Quantity", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "requiresCode", Column: "requires_code", Label: "Requires Promo Code", Kind: resource.Bool, Default: false},
		{Name: "public", Column: "public", Label: "Show on Website & Member App", Kind: resource.Bool, Default: true},
		{Name: "stackable", Column: "stackable", Label: "Stackable", Kind: resource.Bool, Default: false},
		{Name: "stackGroup", Column: "stack_group", Label: "Stack Group", Kind: resource.String, Max: 40, Upper: true},
		{Name: "priority", Column: "priority", Label: "Priority (lower first)", Kind: resource.Int, Default: int64(100), Min: resource.Min(0)},
		{Name: "budgetAmount", Column: "budget_amount", Label: "Budget (maximum total discount)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "maxRedemptions", Column: "max_redemptions", Label: "Maximum Redemptions", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "maxPerCustomer", Column: "max_per_customer", Label: "Maximum per Customer", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "usedCount", Column: "used_count", Label: "Redemptions", Kind: resource.Int, ReadOnly: true},
		{Name: "usedAmount", Column: "used_amount", Label: "Discount Given", Kind: resource.Decimal, ReadOnly: true},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{PromoDraft, PromoPending, PromoActive, PromoInactive, PromoRejected,
			PromoExpired}, ReadOnly: true, Filter: true},
		{Name: "version", Column: "version", Label: "Version", Kind: resource.Int, ReadOnly: true},
		{Name: "approvalRequestId", Column: "approval_request_id", Label: "Approval Request", Kind: resource.UUID, ReadOnly: true},
		{Name: "activatedAt", Column: "activated_at", Label: "Activated At", Kind: resource.Timestamp, ReadOnly: true},
	},
}

// PromoCodes master (FR-PRM-04): general codes or unique codes per
// recipient (generated in bulk for campaigns).
var PromoCodes = &resource.Def{
	Key: "commercial.promo_code", Module: "commercial", Perm: "commercial.promo_code", Path: "/api/v1/commercial/promo-codes",
	Table: "commercial.promo_codes", Name: "Promo Code", Plural: "Promo Codes", Tag: "Promotions", PropertyScoped: true, NoDelete: false,
	CodeField: "code", OrderBy: "created_at DESC, code",
	Fields: []resource.Field{
		{Name: "promotionId", Column: "promotion_id", Label: "Promotion", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.promotions", SameProperty: true, Label: "promotion"}},
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true,
			Pattern: promoCodeRe, PatternMsg: "3–40 characters: A–Z, 0–9, - or _", Search: true},
		{Name: "customerId", Column: "customer_id", Label: "Customer (personal code)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "campaignRef", Column: "campaign_ref", Label: "Campaign", Kind: resource.String, Max: 80, Filter: true},
		{Name: "batchId", Column: "batch_id", Label: "Generation Batch", Kind: resource.UUID, ReadOnly: true, Filter: true},
		{Name: "maxUses", Column: "max_uses", Label: "Maximum Uses", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "maxUsesPerCustomer", Column: "max_uses_per_customer", Label: "Maximum Uses per Customer", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "usedCount", Column: "used_count", Label: "Used", Kind: resource.Int, ReadOnly: true},
		{Name: "expiresAt", Column: "expires_at", Label: "Expires At", Kind: resource.Timestamp},
		resource.Status("active", "inactive"),
	},
}

// PromotionActivationType is the approval of new or changed promotions
// before they become Active (FR-PRM-10); workflow steps can use the
// discount, budget and type attributes.
var PromotionActivationType = provision.DocumentType{Code: "promotion_activation", Module: "commercial", Name: "Promotion Activation",
	Attributes: []provision.DocumentAttribute{{Key: "discountPercent", Label: "Discount (%)", Type: "number"},
		{Key: "discountAmount", Label: "Discount Amount", Type: "number"}, {Key: "budget", Label: "Budget", Type: "number"},
		{Key: "promoType", Label: "Promotion Type", Type: "string"}}}

// ── Club Policies: Promotion Policies & Pricing Policies (FR-POL-P3-02) ────

// PromotionPolicy is "Promotion Policies" (PRD P3 §7.6 proposed label).
type PromotionPolicy struct {
	RequireApproval    bool   `json:"requireApproval" doc:"New or changed promotions need the Promotion Activation approval before they become Active"`
	Selection          string `json:"selection" doc:"priority: lower priority number first, the best discount breaks ties; best_price: the best discount first"`
	MaxStackedPercent  string `json:"maxStackedPercent" doc:"Ceiling of all promotions on one line, percent of the line"`
	OfflineTolerance   string `json:"offlineTolerance" doc:"Accepted difference between an offline terminal total and the server total"`
	CodeCheckPerMinute int    `json:"codeCheckPerMinute" doc:"Public promo code checks per client per minute"`
	OfflineCacheHours  int    `json:"offlineCacheHours" doc:"Validity of the POS offline promotion cache"`
}

// DefaultPromotionPolicy follows PRD P3 §16 #8: no stacking by default and
// the best price for the customer (best_price selection).
var DefaultPromotionPolicy = PromotionPolicy{RequireApproval: true, Selection: "best_price", MaxStackedPercent: "100", OfflineTolerance: "0",
	CodeCheckPerMinute: 10, OfflineCacheHours: 24}

// PeakPeriod is a peak season (dates inclusive).
type PeakPeriod struct {
	Name string `json:"name"`
	From string `json:"from" doc:"YYYY-MM-DD"`
	To   string `json:"to" doc:"YYYY-MM-DD"`
}

// TimeWindow is a weekday time window ("16:00"–"19:00"; days ISO 1..7,
// empty = every day). An end before the start runs past midnight.
type TimeWindow struct {
	Days  []int  `json:"days,omitempty"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// PricingPolicy is "Pricing Policies" (Naming Convention §25): peak seasons
// and hours for Peak / Off-Peak Rates and promotions (FR-PRC-P3-01) and the
// manual discount limit per role (PRD P3 §16 #5).
type PricingPolicy struct {
	PeakPeriods          []PeakPeriod      `json:"peakPeriods" doc:"Peak seasons"`
	PeakHours            []TimeWindow      `json:"peakHours" doc:"Peak hours per weekday"`
	HolidaysArePeak      bool              `json:"holidaysArePeak" doc:"Public holidays count as peak"`
	ManualDiscountLimits map[string]string `json:"manualDiscountLimits" doc:"Manual discount limit (percent) per role code (default: the tiers of PRD P3 §16 #5); {} = POS Policies for every role"`
}

// DefaultManualDiscountLimits are the manual discount tiers of PRD P3 §16 #5,
// the same as the quotation tiers of the Sales Policies
// (crm/sales.DefaultRoleDiscountLimits, which commercial may not import):
// sales / cashier / POS / marketing staff 5%, CRM / membership / golf
// admins 10%, managers 20%, the General Manager 100%. Above the limit the
// POS asks for a supervisor (commercial.pos.discount_override). Roles not
// listed keep the POS Policies limit. A fresh map is returned on every call.
func DefaultManualDiscountLimits() map[string]string {
	return map[string]string{"sales_executive": "5", "banquet_sales": "5", "cashier": "5", "pos_staff": "5", "marketing_staff": "5",
		"crm_admin": "10", "membership_admin": "10", "golf_admin": "10",
		"banquet_manager": "20", "event_manager": "20", "outlet_manager": "20", "membership_manager": "20", "golf_manager": "20",
		"sport_club_manager": "20", "club_manager": "20", "resort_manager": "20", "general_manager": "100"}
}

// DefaultPricingPolicy: no peak periods; manual discounts per role of PRD P3 §16 #5.
var DefaultPricingPolicy = PricingPolicy{PeakPeriods: []PeakPeriod{}, PeakHours: []TimeWindow{}, ManualDiscountLimits: DefaultManualDiscountLimits()}

// Policy codes.
const (
	PolicyPromotion = "commercial.promotion"
	PolicyPricing   = "commercial.pricing"
)

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyPromotion, Category: "Promotion Policies", Name: "Promotion approval, stacking & offline",
		Description: "Approval before activation, selection between promotions, stacking ceiling, offline tolerance and promo code check limit",
		Default:     DefaultPromotionPolicy})
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyPricing, Category: "Pricing Policies", Name: "Peak periods & manual discount limits",
		Description: "Peak seasons and hours (Peak / Off-Peak Rate), holidays as peak and the manual discount limit per role", Default: DefaultPricingPolicy})
	if !slices.Contains(rules.PolicyCategories, "Promotion Policies") {
		rules.PolicyCategories = append(rules.PolicyCategories, "Promotion Policies")
	}
	Promotions.Hooks = resource.Hooks{BeforeWrite: promotionBeforeWrite}
	PromoCodes.Hooks = resource.Hooks{BeforeWrite: promoCodeBeforeWrite}
}

// LoadPromotionPolicy returns the Promotion Policies in force.
func LoadPromotionPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (PromotionPolicy, rules.PolicyRef, error) {
	p, ref, err := rules.PolicyAt(ctx, q, PolicyPromotion, property, DefaultPromotionPolicy)
	if p.Selection != "best_price" {
		p.Selection = "priority"
	}
	return p, ref, err
}

// LoadPricingPolicy returns the Pricing Policies in force.
func LoadPricingPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (PricingPolicy, rules.PolicyRef, error) {
	// decoded into a fresh value: a configured policy must never merge into
	// the shared default map; a version without manualDiscountLimits keeps
	// the default tiers, an explicit {} means the POS Policies limit
	def := DefaultPricingPolicy
	def.ManualDiscountLimits = nil
	pol, ref, err := rules.PolicyAt(ctx, q, PolicyPricing, property, def)
	if pol.ManualDiscountLimits == nil {
		pol.ManualDiscountLimits = DefaultManualDiscountLimits()
	}
	return pol, ref, err
}

// ManualDiscountLimit is the manual discount limit (percent) of a user with
// the given roles: the highest limit of the roles configured in the Pricing
// Policies, def when none of the roles is configured.
func ManualDiscountLimit(ctx context.Context, q dbtx.Querier, property uuid.UUID, roles []string, def decimal.Decimal) (decimal.Decimal, error) {
	pol, _, err := LoadPricingPolicy(ctx, q, property)
	if err != nil || len(pol.ManualDiscountLimits) == 0 {
		return def, err
	}
	found := false
	limit := decimal.Zero
	for _, r := range roles {
		if v, ok := pol.ManualDiscountLimits[r]; ok {
			d, err := decimal.NewFromString(v)
			if err != nil {
				continue
			}
			if !found || d.GreaterThan(limit) {
				limit, found = d, true
			}
		}
	}
	if !found {
		return def, nil
	}
	return limit, nil
}

// ── validation ────────────────────────────────────────────────────────────

// BundleItem is one product of a bundle promotion.
type BundleItem struct {
	ProductID uuid.UUID `json:"productId"`
	Quantity  int       `json:"quantity"`
}

func merged(v, before map[string]any) map[string]any {
	m := map[string]any{}
	for k, x := range before {
		m[k] = x
	}
	for k, x := range v {
		m[k] = x
	}
	return m
}

func decOf(v any) (decimal.Decimal, bool) {
	if v == nil {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(str(v))
	if err != nil {
		return decimal.Zero, false
	}
	return d, true
}

func intOf(v any) (int, bool) {
	switch t := v.(type) {
	case int64:
		return int(t), true
	case int32:
		return int(t), true
	case int:
		return t, true
	case float64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	}
	if v == nil {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscan(str(v), &n); err != nil {
		return 0, false
	}
	return n, true
}

func listOf(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, str(x))
		}
		return out
	}
	var out []string
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

func jsonOf(v any, into any) error {
	var b []byte
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		b = []byte(t)
	case []byte:
		b = t
	default:
		var err error
		if b, err = json.Marshal(t); err != nil {
			return err
		}
	}
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return json.Unmarshal(b, into)
}

var hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidTimeWindows checks time windows (happy hour, peak hours).
func ValidTimeWindows(field string, ws []TimeWindow) error {
	for i, w := range ws {
		f := fmt.Sprintf("%s[%d]", field, i)
		if !hhmmRe.MatchString(w.Start) || !hhmmRe.MatchString(w.End) || w.Start == w.End {
			return errs.Validation("invalid_time_window", "invalid time window", errs.Field(f, "invalid", "start and end as HH:MM, end ≠ start"))
		}
		for _, d := range w.Days {
			if d < 1 || d > 7 {
				return errs.Validation("invalid_time_window", "invalid weekday", errs.Field(f, "invalid", "days are ISO weekdays 1–7"))
			}
		}
	}
	return nil
}

// promotionBeforeWrite validates the benefit of the promotion type and the
// scope references; an Active or Pending promotion cannot be edited, and
// every edit raises the version recorded in snapshots and redemptions.
func promotionBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	pid, _ := reqctx.Property(ctx)
	if before != nil {
		switch str(before["status"]) {
		case PromoActive, PromoPending:
			return errs.Conflict("promotion_active", "an Active or Pending promotion cannot be edited; deactivate it first (a new version is activated again)")
		}
		ver, _ := intOf(before["version"])
		if str(before["status"]) != PromoDraft || before["activatedAt"] != nil {
			v["version"] = int64(ver + 1)
		}
	}
	m := merged(v, before)
	pct, hasPct := decOf(m["discountPercent"])
	amt, hasAmt := decOf(m["discountAmount"])
	need := func(field, msg string) error {
		return errs.Validation("invalid_promotion", msg, errs.Field(field, "required", msg))
	}
	switch t := str(m["promoType"]); t {
	case "percent_discount":
		if !hasPct || !pct.IsPositive() {
			return need("discountPercent", "a percentage discount needs the discount percent")
		}
	case "amount_discount":
		if !hasAmt || !amt.IsPositive() {
			return need("discountAmount", "an amount discount needs the discount amount")
		}
	case "happy_hour", "member_discount", "promo_code", "period":
		if (!hasPct || !pct.IsPositive()) && (!hasAmt || !amt.IsPositive()) {
			return need("discountPercent", "set the discount percent or the discount amount")
		}
		if hasPct && hasAmt && pct.IsPositive() && amt.IsPositive() {
			return errs.Validation("invalid_promotion", "choose either a percent or an amount discount", errs.Field("discountAmount", "invalid", "percent or amount, not both"))
		}
		switch t {
		case "happy_hour":
			var ws []TimeWindow
			if err := jsonOf(m["timeWindows"], &ws); err != nil || len(ws) == 0 {
				return need("timeWindows", "a happy hour needs at least one time window")
			}
		case "member_discount":
			if len(listOf(m["segments"])) == 0 && len(listOf(m["membershipTypes"])) == 0 && len(listOf(m["customerSegmentIds"])) == 0 {
				return need("segments", "a member discount needs a segment, membership type or CRM segment")
			}
		case "promo_code":
			v["requiresCode"], m["requiresCode"] = true, true
		case "period":
			if str(m["validFrom"]) == "" || str(m["validTo"]) == "" {
				return need("validTo", "a period promotion needs valid from and valid to")
			}
		}
	case "buy_n_get_x":
		if n, ok := intOf(m["buyQuantity"]); !ok || n < 1 {
			return need("buyQuantity", "Buy N Get X needs the buy quantity")
		}
		if x, ok := intOf(m["getQuantity"]); !ok || x < 1 {
			return need("getQuantity", "Buy N Get X needs the get quantity")
		}
	case "buy_n_price_x":
		if n, ok := intOf(m["buyQuantity"]); !ok || n < 1 {
			return need("buyQuantity", "Buy N Price X needs the buy quantity")
		}
		if _, ok := decOf(m["bundlePrice"]); !ok {
			return need("bundlePrice", "Buy N Price X needs the group price")
		}
	case "bundle":
		var items []BundleItem
		if err := jsonOf(m["bundleItems"], &items); err != nil {
			return errs.Validation("invalid_promotion", "invalid bundle items", errs.Field("bundleItems", "invalid", "list of {productId, quantity}"))
		}
		if len(items) < 2 {
			return need("bundleItems", "a bundle combines at least two products")
		}
		if _, ok := decOf(m["bundlePrice"]); !ok {
			return need("bundlePrice", "a bundle needs the bundle price")
		}
		for i, it := range items {
			if it.Quantity < 1 {
				return errs.Validation("invalid_promotion", "invalid bundle quantity", errs.Field(fmt.Sprintf("bundleItems[%d].quantity", i), "invalid", "1 or more"))
			}
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.products WHERE id = $1 AND property_id = $2)`, it.ProductID, pid).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errs.Validation("invalid_promotion", "bundle product not found", errs.Field(fmt.Sprintf("bundleItems[%d].productId", i), "not_found", "product not found"))
			}
		}
	}
	var ws []TimeWindow
	if err := jsonOf(m["timeWindows"], &ws); err != nil {
		return errs.Validation("invalid_promotion", "invalid time windows", errs.Field("timeWindows", "invalid", "list of {days, start, end}"))
	}
	if err := ValidTimeWindows("timeWindows", ws); err != nil {
		return err
	}
	if f, t := str(m["validFrom"]), str(m["validTo"]); f != "" && t != "" && t < f {
		return errs.Validation("invalid_period", "valid to must not be before valid from", errs.Field("validTo", "invalid", "on or after valid from"))
	}
	for _, k := range []string{"outletIds", "productIds", "customerSegmentIds"} {
		for _, x := range listOf(v[k]) {
			if _, err := uuid.Parse(x); err != nil {
				return errs.Validation("invalid_promotion", "invalid reference", errs.Field(k, "invalid", "list of ids"))
			}
		}
	}
	return nil
}

// promoCodeBeforeWrite normalises codes; codes of another promotion with
// the same text are rejected by the unique constraint.
func promoCodeBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if c, ok := v["code"].(string); ok {
		v["code"] = strings.ToUpper(strings.TrimSpace(c))
	}
	if t, ok := v["expiresAt"].(time.Time); ok && t.Before(time.Now().Add(-time.Minute)) {
		return errs.Validation("invalid_expiry", "the code would already be expired", errs.Field("expiresAt", "past", "choose a future time"))
	}
	return nil
}
