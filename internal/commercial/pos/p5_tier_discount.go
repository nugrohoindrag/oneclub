package pos

// PRD P5 EP-18 tier benefit "F&B discount" at the POS (FR-LOY-P5-02; member
// tier class, product owner request). When an order is opened for a member
// customer, the F&B discount of the customer's loyalty tier is stored on the
// order (read through the hook wired by internal/app: commercial must not
// import crm) and every F&B line gets it as a separate discount after its
// manual discount and promotions, labelled e.g. "Gold member 5%". The
// Pricing Policies "Member tier discount at the POS" decide the F&B product
// types and the stacking with promotions: not stacked (default, PRD P3 §16
// #8) the line gets the better of the promotions and the tier discount;
// stacked, the tier discount applies to the amount after the promotions
// within the stacking ceiling of the Promotion Policies. Offline terminals
// cache the benefit with the member and send their total for the check.

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/rules"
)

// TierBenefit is the loyalty tier of a customer as the POS reads it.
type TierBenefit struct {
	Code               string
	Name               string
	FnbDiscountPercent decimal.Decimal
}

var tierBenefit func(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (*TierBenefit, error)

// SetTierBenefit wires the tier benefit lookup (internal/app; nil = none).
func SetTierBenefit(f func(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (*TierBenefit, error)) {
	tierBenefit = f
}

// TierDiscountPolicy is "Member tier discount at the POS" (Pricing Policies).
type TierDiscountPolicy struct {
	Enabled             bool     `json:"enabled" doc:"Apply the F&B discount of the member's loyalty tier at the POS"`
	ProductTypes        []string `json:"productTypes" doc:"Product types that are F&B items"`
	StackWithPromotions bool     `json:"stackWithPromotions" doc:"true: on top of the promotions within the Promotion Policies stacking ceiling; false: the better of the promotions and the tier discount per line"`
	Label               string   `json:"label" doc:"Label of the discount line; {tier} and {percent} are replaced"`
}

// DefaultTierDiscountPolicy: F&B (food, beverage), no stacking with
// promotions (best price, PRD P3 §16 #8), "Gold member 5%".
var DefaultTierDiscountPolicy = TierDiscountPolicy{Enabled: true, ProductTypes: []string{"food", "beverage"}, Label: "{tier} member {percent}%"}

// TierDiscountPolicyCode is the club policy code.
const TierDiscountPolicyCode = "pos.tier_discount"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: TierDiscountPolicyCode, Category: "Pricing Policies", Name: "Member tier discount at the POS",
		Description: "F&B discount of the loyalty tier: F&B product types, stacking with promotions and the label of the discount line",
		Default:     DefaultTierDiscountPolicy})
}

// LoadTierDiscountPolicy returns the policy in force.
func LoadTierDiscountPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (TierDiscountPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, TierDiscountPolicyCode, property, DefaultTierDiscountPolicy)
	if len(p.ProductTypes) == 0 {
		p.ProductTypes = DefaultTierDiscountPolicy.ProductTypes
	}
	if strings.TrimSpace(p.Label) == "" {
		p.Label = DefaultTierDiscountPolicy.Label
	}
	return p, err
}

var hundred = decimal.NewFromInt(100)

// TierLineDiscount is the tier discount of a line: base is the line amount
// after its manual discount, promo the promotion discount of the line, pct
// the tier F&B discount, ceilingPct the Promotion Policies stacking ceiling
// (percent of the line; 0 or ≥ 100 = none), rounded to the currency.
func TierLineDiscount(base, promo, pct, ceilingPct decimal.Decimal, stack bool, places int32) decimal.Decimal {
	if !pct.IsPositive() || !base.IsPositive() {
		return decimal.Zero
	}
	if !stack {
		// the better of the promotions and the tier discount
		return decimal.Max(base.Mul(pct).Div(hundred).Round(places).Sub(promo), decimal.Zero)
	}
	rest := base.Sub(promo)
	if !rest.IsPositive() {
		return decimal.Zero
	}
	d := rest.Mul(pct).Div(hundred).Round(places)
	if ceilingPct.IsPositive() && ceilingPct.LessThan(hundred) {
		d = decimal.Min(d, decimal.Max(base.Mul(ceilingPct).Div(hundred).Round(places).Sub(promo), decimal.Zero))
	}
	return d
}

// TierDiscountLabel is the label of the discount line, e.g. "Gold member 5%".
func TierDiscountLabel(label, tier string, pct decimal.Decimal) string {
	return strings.NewReplacer("{tier}", tier, "{percent}", pct.String()).Replace(label)
}

// snapshotTier stores the tier benefit of the order's customer when the
// order is opened.
func snapshotTier(ctx context.Context, tx pgx.Tx, oid, property uuid.UUID, customer *uuid.UUID) error {
	if customer == nil || tierBenefit == nil {
		return nil
	}
	pol, err := LoadTierDiscountPolicy(ctx, tx, property)
	if err != nil || !pol.Enabled {
		return err
	}
	b, err := tierBenefit(ctx, tx, *customer)
	if err != nil || b == nil || !b.FnbDiscountPercent.IsPositive() {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE commercial.orders SET tier_code = $2, tier_name = $3, tier_discount_percent = $4::numeric, tier_discount_label = $5
		WHERE id = $1`, oid, b.Code, b.Name, b.FnbDiscountPercent.String(), TierDiscountLabel(pol.Label, b.Name, b.FnbDiscountPercent))
	return err
}

// orderTier is the tier discount context of an order being priced.
type orderTier struct {
	pct     decimal.Decimal
	types   []string
	stack   bool
	ceiling decimal.Decimal
	places  int32
	label   string
}

func loadOrderTier(ctx context.Context, q dbtx.Querier, o promoOrder) (*orderTier, error) {
	if o.TierPercent == nil {
		return nil, nil
	}
	pct, _ := decimal.NewFromString(*o.TierPercent)
	if !pct.IsPositive() {
		return nil, nil
	}
	pol, err := LoadTierDiscountPolicy(ctx, q, o.PropertyID)
	if err != nil {
		return nil, err
	}
	promo, _, err := commercial.LoadPromotionPolicy(ctx, q, o.PropertyID)
	if err != nil {
		return nil, err
	}
	ceiling, _ := decimal.NewFromString(promo.MaxStackedPercent)
	var cur string
	_ = q.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	places := int32(2)
	if cur == "" || cur == "IDR" || cur == "JPY" {
		places = 0
	}
	label := ""
	if o.TierLabel != nil {
		label = *o.TierLabel
	}
	return &orderTier{pct: pct, types: pol.ProductTypes, stack: pol.StackWithPromotions, ceiling: ceiling, places: places, label: label}, nil
}

// discount is the tier discount of one line.
func (t *orderTier) discount(productType string, base, promo decimal.Decimal) decimal.Decimal {
	if t == nil || !slices.Contains(t.types, productType) {
		return decimal.Zero
	}
	return TierLineDiscount(base, promo, t.pct, t.ceiling, t.stack, t.places)
}

// POSTierDiscount is the tier F&B discount of a customer as the POS applies
// it; offline terminals cache it with the member.
type POSTierDiscount struct {
	CustomerID          uuid.UUID `json:"customerId"`
	Enabled             bool      `json:"enabled" doc:"The customer gets a tier discount at the POS"`
	TierCode            *string   `json:"tierCode"`
	TierName            *string   `json:"tierName"`
	Percent             string    `json:"percent"`
	Label               string    `json:"label" doc:"Label of the discount line, e.g. Gold member 5%"`
	ProductTypes        []string  `json:"productTypes" doc:"F&B product types the discount applies to"`
	StackWithPromotions bool      `json:"stackWithPromotions"`
	MaxStackedPercent   string    `json:"maxStackedPercent" doc:"Promotion Policies stacking ceiling (percent of the line)"`
	Currency            string    `json:"currency"`
}

func (m *Module) registerTierDiscount(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/pos/tier-discount", Module: "commercial", Tag: "POS", Scope: route.ScopeProperty,
		Summary: "Loyalty tier F&B discount of a customer (POS customer picker; cached offline)", Permission: "commercial.order.view",
		Response: POSTierDiscount{}, Query: []route.Param{{Name: "customerId", Required: true}},
		Handler: handle.Read(m.DB, func(ctx context.Context, tx pgx.Tx, r *http.Request) (POSTierDiscount, error) {
			cid, err := uuid.Parse(r.URL.Query().Get("customerId"))
			if err != nil {
				return POSTierDiscount{}, handle.Invalid("customerId", "required", "customer id")
			}
			property := handle.Property(ctx)
			pol, err := LoadTierDiscountPolicy(ctx, tx, property)
			if err != nil {
				return POSTierDiscount{}, err
			}
			promo, _, err := commercial.LoadPromotionPolicy(ctx, tx, property)
			if err != nil {
				return POSTierDiscount{}, err
			}
			out := POSTierDiscount{CustomerID: cid, Percent: "0", ProductTypes: pol.ProductTypes, StackWithPromotions: pol.StackWithPromotions,
				MaxStackedPercent: promo.MaxStackedPercent}
			_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&out.Currency)
			if !pol.Enabled || tierBenefit == nil {
				return out, nil
			}
			b, err := tierBenefit(ctx, tx, cid)
			if err != nil || b == nil {
				return out, err
			}
			out.TierCode, out.TierName = &b.Code, &b.Name
			if b.FnbDiscountPercent.IsPositive() {
				out.Enabled, out.Percent, out.Label = true, b.FnbDiscountPercent.String(), TierDiscountLabel(pol.Label, b.Name, b.FnbDiscountPercent)
			}
			return out, nil
		})})
}
