package loyalty

// Routes of CRM → Loyalty (PRD P3 §11: /crm/loyalty/accounts, earning
// rules, tiers, rewards, ledger, :adjust, :redeem) and the Member App
// Loyalty menu (EP-19: My Points, Tier, Rewards, Points History).

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Business lines of folio lines (billing.BusinessLines).
var businessLines = []string{"golf", "sportclub", "stay", "pos", "membership", "voucher", "banquet", "package", "other"}

var componentRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,40}$`)

// Tiers is the Loyalty Tier master (FR-LOY-07).
var Tiers = &resource.Def{
	Key: "crm.loyalty_tier", Module: "crm", Perm: "crm.loyalty_tier", Path: "/api/v1/crm/loyalty/tiers", Table: "crm.loyalty_tiers",
	Name: "Loyalty Tier", Plural: "Loyalty Tiers", Tag: "Loyalty", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "rank, code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "rank", Column: "rank", Label: "Rank", Kind: resource.Int, Default: int64(1), Min: resource.Min(0)},
		{Name: "minPoints", Column: "min_points", Label: "Minimum Points (period)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "minSpend", Column: "min_spend", Label: "Minimum Spend (period)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "multiplier", Column: "multiplier", Label: "Points Multiplier", Kind: resource.Decimal, Default: "1", Min: resource.Min(0)},
		{Name: "benefits", Column: "benefits", Label: "Benefits", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

// EarningRules are the Earning Rules (FR-LOY-02).
var EarningRules = &resource.Def{
	Key: "crm.loyalty_earning_rule", Module: "crm", Perm: "crm.loyalty_rule", Path: "/api/v1/crm/loyalty/earning-rules", Table: "crm.loyalty_earning_rules",
	Name: "Earning Rule", Plural: "Earning Rules", Tag: "Loyalty", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "priority, code, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "ruleType", Column: "rule_type", Label: "Rule Type", Kind: resource.Enum, Enum: []string{"spend", "activity"}, Default: "spend", Filter: true},
		{Name: "businessLine", Column: "business_line", Label: "Business Line", Kind: resource.Enum, Enum: businessLines, Filter: true},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.String, Max: 40},
		{Name: "outletId", Column: "outlet_id", Label: "Outlet", Kind: resource.UUID},
		{Name: "productId", Column: "product_id", Label: "Product", Kind: resource.UUID},
		{Name: "amountPerPoint", Column: "amount_per_point", Label: "Net Spend per Point (default: Loyalty Policies)", Kind: resource.Decimal, Min: resource.Min(0.0001)},
		{Name: "multiplier", Column: "multiplier", Label: "Multiplier (0 = no points)", Kind: resource.Decimal, Default: "1", Min: resource.Min(0)},
		{Name: "customerSegmentId", Column: "customer_segment_id", Label: "CRM Segment (spend rule: multiplier for its members, on top of the line's rule)",
			Kind: resource.UUID, Ref: &resource.Ref{Table: "crm.segments", SameProperty: true, Label: "segment"}},
		{Name: "activity", Column: "activity", Label: "Activity", Kind: resource.Enum, Enum: Activities},
		{Name: "points", Column: "points", Label: "Activity Points", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "priority", Column: "priority", Label: "Priority (lower first on a tie)", Kind: resource.Int, Default: int64(100)},
		resource.Status("active", "inactive")},
}

// Rewards is the Rewards catalogue (FR-LOY-05).
var Rewards = &resource.Def{
	Key: "crm.loyalty_reward", Module: "crm", Perm: "crm.loyalty_reward", Path: "/api/v1/crm/loyalty/rewards", Table: "crm.loyalty_rewards",
	Name: "Reward", Plural: "Rewards", Tag: "Loyalty", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "points_cost, code, id",
	SchemaName: "LoyaltyReward",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "rewardType", Column: "reward_type", Label: "Reward Type", Kind: resource.Enum, Enum: []string{"voucher", "merchandise", "service", "other"},
			Default: "merchandise", Filter: true},
		{Name: "pointsCost", Column: "points_cost", Label: "Points", Kind: resource.Int, Required: true, Min: resource.Min(1)},
		{Name: "stock", Column: "stock", Label: "Stock (empty = unlimited)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "voucherTypeRef", Column: "voucher_type_ref", Label: "Voucher Type (Commercial code)", Kind: resource.String, Max: 40},
		{Name: "minTierId", Column: "min_tier_id", Label: "Minimum Tier", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "crm.loyalty_tiers", SameProperty: true, Label: "tier"}},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		resource.Status("active", "inactive")},
}

func init() {
	EarningRules.Hooks.BeforeWrite = ruleBeforeWrite
	Rewards.Hooks.BeforeWrite = validPeriod
}

func merged(v, before map[string]any, k string) any {
	if x, ok := v[k]; ok {
		return x
	}
	if before != nil {
		return before[k]
	}
	return nil
}

// validPeriod checks validFrom ≤ validTo.
func validPeriod(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
	f, _ := merged(v, before, "validFrom").(string)
	t, _ := merged(v, before, "validTo").(string)
	if f != "" && t != "" && t < f {
		return handle.Invalid("validTo", "invalid_period", "valid to must not be before valid from")
	}
	return nil
}

// ruleBeforeWrite validates an earning rule: activity rules need an activity
// and points; outlets / products must exist (read through reporting views).
func ruleBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	if err := validPeriod(ctx, tx, v, before); err != nil {
		return err
	}
	if c, ok := v["revenueComponent"].(string); ok && c != "" && !componentRe.MatchString(c) {
		return handle.Invalid("revenueComponent", "invalid_component", "a revenue component code, e.g. fnb or green_fee")
	}
	if t, _ := merged(v, before, "ruleType").(string); t == "activity" {
		act, _ := merged(v, before, "activity").(string)
		var pts int64
		switch x := merged(v, before, "points").(type) {
		case int64:
			pts = x
		case int:
			pts = int64(x)
		}
		if act == "" || pts <= 0 {
			return errs.Validation("invalid_activity_rule", "an activity rule needs an activity and points",
				errs.Field("activity", "required", "choose the activity"), errs.Field("points", "too_small", "points above 0"))
		}
	}
	pid := handle.Property(ctx)
	for _, f := range []struct{ name, view, label string }{{"outletId", "reporting.eng_outlets", "outlet"}, {"productId", "reporting.eng_products", "product"}} {
		s, _ := v[f.name].(string)
		if s == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+f.view+` WHERE id = $1::uuid AND property_id = $2)`, s, pid).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid(f.name, "not_found", f.label+" not found in this property")
		}
	}
	return nil
}

// LoyaltyAccountStatusInput activates or deactivates an account (opt-out).
type LoyaltyAccountStatusInput struct {
	Status string `json:"status" enum:"active,inactive,suspended"`
	Reason string `json:"reason"`
}

// LoyaltySetTierInput sets a tier manually (e.g. a Top Spender note).
type LoyaltySetTierInput struct {
	TierID *uuid.UUID `json:"tierId" doc:"null removes the tier"`
	Lock   bool       `json:"lock,omitempty" doc:"Keep this tier at the periodic evaluation"`
	Reason string     `json:"reason"`
}

// LoyaltyAccountDetail is an account with its recent ledger.
type LoyaltyAccountDetail struct {
	LoyaltyAccount
	Ledger    []LoyaltyEntry    `json:"ledger"`
	TierNotes []LoyaltyTierNote `json:"tierNotes" doc:"Notes for the tier (e.g. from the Top Spender list)"`
}

// LoyaltyTierNote is a note for the tier of a customer (FR-TOP-05).
type LoyaltyTierNote struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Note              string     `json:"note" db:"note"`
	SuggestedTierID   *uuid.UUID `json:"suggestedTierId" db:"suggested_tier_id"`
	SuggestedTierName *string    `json:"suggestedTierName" db:"suggested_tier_name"`
	Source            string     `json:"source" db:"source"`
	CreatedByName     *string    `json:"createdByName" db:"created_by_name"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

// TierNotes lists the tier notes of a customer.
func TierNotes(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]LoyaltyTierNote, error) {
	return handle.List[LoyaltyTierNote](q.Query(ctx, `SELECT n.id, n.note, n.suggested_tier_id, t.name AS suggested_tier_name, n.source,
		u.full_name AS created_by_name, n.created_at FROM crm.loyalty_tier_notes n LEFT JOIN crm.loyalty_tiers t ON t.id = n.suggested_tier_id
		LEFT JOIN platform.users u ON u.id = n.created_by WHERE n.customer_id = $1 ORDER BY n.created_at DESC LIMIT 20`, customer))
}

// LoyaltyTierEvaluation reports a tier evaluation run.
type LoyaltyTierEvaluation struct {
	Changed int `json:"changed"`
}

func (m *Module) accountParam(ctx context.Context, q dbtx.Querier, r *http.Request) (LoyaltyAccount, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return LoyaltyAccount{}, err
	}
	a, err := GetAccount(ctx, q, aid)
	if err != nil {
		return a, err
	}
	if a.PropertyID != handle.Property(ctx) {
		return LoyaltyAccount{}, errs.NotFound("loyalty account")
	}
	return a, nil
}

// Register adds the loyalty routes (wired by internal/app).
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{Tiers, EarningRules, Rewards} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Loyalty"
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/accounts", Summary: "Loyalty accounts", Permission: "crm.loyalty_account.view",
		Response: LoyaltyAccount{}, List: true, Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[tierId]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyAccount], error) {
			lp := httpx.ParseList(r)
			items, err := handle.List[LoyaltyAccount](tx.Query(ctx, accountSelect+` WHERE a.property_id = $1 AND ($2 = '' OR a.status = $2)
				AND ($3 = '' OR a.tier_id::text = $3) AND ($4 = '' OR a.customer_id::text = $4)
				AND ($5 = '' OR a.number ILIKE '%' || $5 || '%' OR c.name ILIKE '%' || $5 || '%' OR c.code ILIKE '%' || $5 || '%')
				ORDER BY c.name, a.id LIMIT $6`, handle.Property(ctx), lp.Filters["status"], lp.Filters["tierId"], lp.Filters["customerId"], lp.Q, lp.Limit))
			if err != nil {
				return httpx.Page[LoyaltyAccount]{}, err
			}
			for i := range items {
				if err := fill(ctx, tx, &items[i]); err != nil {
					return httpx.Page[LoyaltyAccount]{}, err
				}
			}
			return handle.Page(items, nil)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts", Summary: "Enrol a customer in loyalty (opt-in)",
		Permission: "crm.loyalty_account.create", Request: LoyaltyEnrolInput{}, Response: LoyaltyAccount{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyEnrolInput) (LoyaltyAccount, error) {
			pid := handle.Property(ctx)
			a, created, err := m.Enrol(ctx, tx, pid, in.CustomerID, "staff", in.TierID)
			if err != nil {
				return a, err
			}
			action := audit.ActionCreate
			if !created {
				action = "re_enrol"
			}
			return a, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: "crm.loyalty_account", EntityID: a.ID.String(),
				EntityLabel: a.Number + " · " + a.CustomerName, PropertyID: &pid, After: a})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/accounts/{id}", Summary: "Loyalty account with recent points history",
		Permission: "crm.loyalty_account.view", Response: LoyaltyAccountDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyAccountDetail, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return LoyaltyAccountDetail{}, err
			}
			l, err := Ledger(ctx, tx, a.ID, "", 20)
			if err != nil {
				return LoyaltyAccountDetail{}, err
			}
			notes, err := TierNotes(ctx, tx, a.CustomerID)
			return LoyaltyAccountDetail{LoyaltyAccount: a, Ledger: l, TierNotes: notes}, err
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/accounts/{id}/ledger", Summary: "Points Ledger of an account",
		Permission: "crm.loyalty_account.view", Response: LoyaltyEntry{}, List: true, Query: []route.Param{{Name: "filter[kind]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyEntry], error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return httpx.Page[LoyaltyEntry]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(Ledger(ctx, tx, a.ID, lp.Filters["kind"], lp.Limit))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:adjust", Summary: "Adjust Points (approval, reason required)",
		Permission: "crm.loyalty_account.adjust", Request: LoyaltyAdjustInput{}, Response: LoyaltyAdjustment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyAdjustInput) (LoyaltyAdjustment, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return LoyaltyAdjustment{}, err
			}
			return m.RequestAdjustment(ctx, tx, handle.Property(ctx), a.ID, in, "manual", nil)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/adjustments", Summary: "Points adjustments and their approval status",
		Permission: "crm.loyalty_account.view", Response: LoyaltyAdjustment{}, List: true, Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[accountId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyAdjustment], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LoyaltyAdjustment](tx.Query(ctx, adjustmentSelect+` WHERE j.property_id = $1 AND ($2 = '' OR j.status = $2)
				AND ($3 = '' OR j.account_id::text = $3) ORDER BY j.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"],
				lp.Filters["accountId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:redeem", Summary: "Redeem Points as payment on a folio (front desk)",
		Permission: "crm.loyalty_account.redeem", Request: LoyaltyRedeemInput{}, Response: LoyaltyRedeemResult{}, Status: http.StatusOK, Idempotent: true,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyRedeemInput) (LoyaltyRedeemResult, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return LoyaltyRedeemResult{}, err
			}
			return m.RedeemOnFolio(ctx, tx, handle.Property(ctx), a.ID, in, r.Header.Get("Idempotency-Key"), nil)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:redeem-reward", Summary: "Exchange points for a reward",
		Permission: "crm.loyalty_account.redeem", Request: RedeemRewardInput{}, Response: RewardRedemption{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RedeemRewardInput) (RewardRedemption, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return RewardRedemption{}, err
			}
			return m.RedeemReward(ctx, tx, handle.Property(ctx), a.ID, in, "staff")
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:set-tier", Summary: "Set the tier manually (optionally locked)",
		Permission: "crm.loyalty_account.update", Request: LoyaltySetTierInput{}, Response: LoyaltyAccount{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltySetTierInput) (LoyaltyAccount, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return a, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return a, err
			}
			pid := handle.Property(ctx)
			if in.TierID != nil {
				var ok bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_tiers WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`,
					*in.TierID, pid).Scan(&ok); err != nil {
					return a, err
				}
				if !ok {
					return a, handle.Invalid("tierId", "not_found", "tier not found in this property")
				}
			}
			locked, err := lockAccount(ctx, tx, a.ID)
			if err != nil {
				return a, err
			}
			if err := noOverrideInForce(ctx, tx, a.ID); err != nil { // P5: revoke the classification override first
				return a, err
			}
			if (in.TierID == nil) != (locked.TierID == nil) || (in.TierID != nil && *in.TierID != *locked.TierID) {
				if err := m.changeTier(ctx, tx, &locked, in.TierID, "manual: "+in.Reason, nil, nil); err != nil {
					return a, err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_locked = $2, tier_source = CASE WHEN $2 THEN 'manual' ELSE tier_source END,
				updated_by = $3 WHERE id = $1`, a.ID, in.Lock, actor(ctx)); err != nil {
				return a, err
			}
			after, err := GetAccount(ctx, tx, a.ID)
			if err != nil {
				return after, err
			}
			return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "set_tier", EntityType: "crm.loyalty_account", EntityID: a.ID.String(),
				EntityLabel: a.Number, PropertyID: &pid, Reason: in.Reason, Before: map[string]any{"tierId": a.TierID, "tierLocked": a.TierLocked},
				After: map[string]any{"tierId": after.TierID, "tierLocked": after.TierLocked}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:set-status", Summary: "Activate, deactivate (opt-out) or suspend an account",
		Permission: "crm.loyalty_account.update", Request: LoyaltyAccountStatusInput{}, Response: LoyaltyAccount{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyAccountStatusInput) (LoyaltyAccount, error) {
			a, err := m.accountParam(ctx, tx, r)
			if err != nil {
				return a, err
			}
			if in.Status != "active" && in.Status != "inactive" && in.Status != "suspended" {
				return a, handle.Invalid("status", "invalid_status", "status must be active, inactive or suspended")
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return a, err
			}
			if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET status = $2, status_reason = $3, updated_by = $4 WHERE id = $1`,
				a.ID, in.Status, in.Reason, actor(ctx)); err != nil {
				return a, err
			}
			after, err := GetAccount(ctx, tx, a.ID)
			if err != nil {
				return after, err
			}
			pid := handle.Property(ctx)
			return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_account",
				EntityID: a.ID.String(), EntityLabel: a.Number, PropertyID: &pid, Reason: in.Reason, Before: map[string]any{"status": a.Status},
				After: map[string]any{"status": after.Status}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/tiers:evaluate", Summary: "Run the tier evaluation now",
		Permission: "crm.loyalty_tier.update", Response: LoyaltyTierEvaluation{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (LoyaltyTierEvaluation, error) {
			pid := handle.Property(ctx)
			n, err := m.EvaluateTiers(ctx, tx, pid)
			if err != nil {
				return LoyaltyTierEvaluation{}, err
			}
			out := LoyaltyTierEvaluation{Changed: n}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "evaluate_tiers", EntityType: "crm.loyalty_tier", EntityID: pid.String(),
				EntityLabel: "tier evaluation", PropertyID: &pid, After: out})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/redemptions", Summary: "Reward redemptions (fulfilment queue)",
		Permission: "crm.loyalty_redemption.view", Response: RewardRedemption{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[accountId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RewardRedemption], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[RewardRedemption](tx.Query(ctx, redemptionSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
				AND ($3 = '' OR r.account_id::text = $3) ORDER BY r.created_at DESC LIMIT $4`, handle.Property(ctx), lp.Filters["status"],
				lp.Filters["accountId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/redemptions/{id}:complete", Summary: "Mark a reward as handed over",
		Permission: "crm.loyalty_redemption.fulfil", Request: RewardRedemptionAction{}, Response: RewardRedemption{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RewardRedemptionAction) (RewardRedemption, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return RewardRedemption{}, err
			}
			return m.CompleteRedemption(ctx, tx, handle.Property(ctx), rid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/redemptions/{id}:cancel", Summary: "Cancel a pending reward (points given back)",
		Permission: "crm.loyalty_redemption.fulfil", Request: RewardRedemptionAction{}, Response: RewardRedemption{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RewardRedemptionAction) (RewardRedemption, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return RewardRedemption{}, err
			}
			return m.CancelRedemption(ctx, tx, handle.Property(ctx), rid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/liability", Summary: "Loyalty liability (outstanding points × redemption value)",
		Permission: "crm.loyalty_account.view", Response: LoyaltyLiability{}, Query: []route.Param{{Name: "at", Description: "RFC 3339; default now"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyLiability, error) {
			at, err := handle.QueryTime(r, "at", time.Now())
			if err != nil {
				return LoyaltyLiability{}, err
			}
			return LiabilityAt(ctx, tx, handle.Property(ctx), at)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/opening-balances:import", Summary: "Import loyalty opening balances (migration, CSV)",
		Permission: "crm.loyalty_account.import", Request: LoyaltyImportInput{}, Response: LoyaltyImportResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyImportInput) (LoyaltyImportResult, error) {
			return m.ImportOpeningBalances(ctx, tx, handle.Property(ctx), in)
		})})
	m.registerMember(reg)
}

// ── Member App: Loyalty (EP-19) ───────────────────────────────────────────

// MyLoyalty is the Loyalty home of the Member App (My Points, Tier).
type MyLoyalty struct {
	Enrolled        bool            `json:"enrolled"`
	Account         *LoyaltyAccount `json:"account"`
	NextTier        *MyLoyaltyTier  `json:"nextTier" doc:"The next tier and its thresholds"`
	PointsToNext    int64           `json:"pointsToNext" doc:"Points still needed for the next tier (evaluation window)"`
	AmountPerPoint  string          `json:"amountPerPoint" doc:"Net spend per point"`
	RedemptionValue string          `json:"redemptionValue"`
}

// MyLoyaltyTier is a tier as the member sees it.
type MyLoyaltyTier struct {
	ID         uuid.UUID `json:"id" db:"id"`
	Code       string    `json:"code" db:"code"`
	Name       string    `json:"name" db:"name"`
	Rank       int       `json:"rank" db:"rank"`
	MinPoints  int64     `json:"minPoints" db:"min_points"`
	MinSpend   string    `json:"minSpend" db:"min_spend"`
	Multiplier string    `json:"multiplier" db:"multiplier"`
	Benefits   *string   `json:"benefits" db:"benefits"`
	Current    bool      `json:"current" db:"-"`
}

// MyLoyaltyReward is a reward of the catalogue as the member sees it.
type MyLoyaltyReward struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	Code        string     `json:"code" db:"code"`
	Name        string     `json:"name" db:"name"`
	Description *string    `json:"description" db:"description"`
	RewardType  string     `json:"rewardType" db:"reward_type"`
	PointsCost  int64      `json:"pointsCost" db:"points_cost"`
	Stock       *int       `json:"stock" db:"stock"`
	MinTierID   *uuid.UUID `json:"minTierId" db:"min_tier_id"`
	MinTierName *string    `json:"minTierName" db:"min_tier_name"`
	MinTierRank *int       `json:"-" db:"min_tier_rank"`
	ValidTo     *string    `json:"validTo" db:"valid_to"`
	Affordable  bool       `json:"affordable" db:"-" doc:"The member has enough points and the tier"`
}

func (m *Module) myLoyalty(ctx context.Context, q dbtx.Querier, c crm.Customer) (MyLoyalty, error) {
	pol, _, err := LoadPolicy(ctx, q, c.PropertyID)
	if err != nil {
		return MyLoyalty{}, err
	}
	out := MyLoyalty{AmountPerPoint: pol.perPoint().String(), RedemptionValue: pol.Value().String()}
	a, err := AccountByCustomer(ctx, q, c.ID)
	if err != nil || a == nil {
		return out, err
	}
	out.Enrolled, out.Account = a.Status == "active", a
	rank := -1
	if a.TierRank != nil {
		rank = *a.TierRank
	}
	tiers, err := handle.List[MyLoyaltyTier](q.Query(ctx, `SELECT id, code, name, rank, min_points, trim_scale(min_spend)::text AS min_spend,
		trim_scale(multiplier)::text AS multiplier, benefits FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND rank > $2 ORDER BY rank, code LIMIT 1`, c.PropertyID, rank))
	if err != nil || len(tiers) == 0 {
		return out, err
	}
	out.NextTier = &tiers[0]
	months := pol.TierPeriodMonths
	if months <= 0 {
		months = 12
	}
	var pts int64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(points) FILTER (WHERE kind = 'earned' OR (kind = 'reversed' AND points < 0)), 0)
		FROM crm.loyalty_ledger WHERE account_id = $1 AND occurred_at >= $2`, a.ID, time.Now().AddDate(0, -months, 0)).Scan(&pts); err != nil {
		return out, err
	}
	out.PointsToNext = max(0, tiers[0].MinPoints-pts)
	return out, nil
}

func (m *Module) myAccount(ctx context.Context, q dbtx.Querier) (crm.Customer, LoyaltyAccount, error) {
	c, err := crm.Me(ctx, q)
	if err != nil {
		return c, LoyaltyAccount{}, err
	}
	a, err := AccountByCustomer(ctx, q, c.ID)
	if err != nil {
		return c, LoyaltyAccount{}, err
	}
	if a == nil {
		return c, LoyaltyAccount{}, errs.Conflict("not_enrolled", "join the loyalty programme first")
	}
	return c, *a, nil
}

func (m *Module) registerMember(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "crm", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty", Summary: "My Points and Tier", Response: MyLoyalty{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyLoyalty, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MyLoyalty{}, err
			}
			return m.myLoyalty(ctx, tx, c)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/loyalty:join", Summary: "Join the loyalty programme (opt-in)", Response: MyLoyalty{},
		Status: http.StatusOK, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (MyLoyalty, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MyLoyalty{}, err
			}
			a, _, err := m.Enrol(ctx, tx, c.PropertyID, c.ID, "member_app", nil)
			if err != nil {
				return MyLoyalty{}, err
			}
			out, err := m.myLoyalty(ctx, tx, c)
			if err != nil {
				return out, err
			}
			pid := c.PropertyID
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "opt_in", EntityType: "crm.loyalty_account", EntityID: a.ID.String(),
				EntityLabel: a.Number, PropertyID: &pid, After: map[string]any{"status": "active", "via": "member_app"}})
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/history", Summary: "Points History", Response: LoyaltyEntry{}, List: true,
		Query: []route.Param{{Name: "filter[kind]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyEntry], error) {
			_, a, err := m.myAccount(ctx, tx)
			if err != nil {
				return httpx.Page[LoyaltyEntry]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(Ledger(ctx, tx, a.ID, lp.Filters["kind"], lp.Limit))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/tiers", Summary: "Tiers and their benefits", Response: MyLoyaltyTier{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyLoyaltyTier], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MyLoyaltyTier]{}, err
			}
			ts, err := handle.List[MyLoyaltyTier](tx.Query(ctx, `SELECT id, code, name, rank, min_points, trim_scale(min_spend)::text AS min_spend,
				trim_scale(multiplier)::text AS multiplier, benefits FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active'
				AND archived_at IS NULL ORDER BY rank, code`, c.PropertyID))
			if err != nil {
				return httpx.Page[MyLoyaltyTier]{}, err
			}
			if a, err := AccountByCustomer(ctx, tx, c.ID); err == nil && a != nil && a.TierID != nil {
				for i := range ts {
					ts[i].Current = ts[i].ID == *a.TierID
				}
			}
			return handle.Page(ts, nil)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/rewards", Summary: "Rewards I can redeem", Response: MyLoyaltyReward{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyLoyaltyReward], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MyLoyaltyReward]{}, err
			}
			today := localToday(ctx, tx, c.PropertyID)
			rs, err := handle.List[MyLoyaltyReward](tx.Query(ctx, `SELECT w.id, w.code, w.name, w.description, w.reward_type, w.points_cost, w.stock, w.min_tier_id,
				t.name AS min_tier_name, t.rank AS min_tier_rank, to_char(w.valid_to, 'YYYY-MM-DD') AS valid_to
				FROM crm.loyalty_rewards w LEFT JOIN crm.loyalty_tiers t ON t.id = w.min_tier_id
				WHERE w.property_id = $1 AND w.status = 'active' AND w.archived_at IS NULL AND (w.valid_from IS NULL OR w.valid_from <= $2::date)
				AND (w.valid_to IS NULL OR w.valid_to >= $2::date) AND (w.stock IS NULL OR w.stock > 0) ORDER BY w.points_cost, w.code`, c.PropertyID, today))
			if err != nil {
				return httpx.Page[MyLoyaltyReward]{}, err
			}
			if a, err := AccountByCustomer(ctx, tx, c.ID); err == nil && a != nil {
				rank := -1
				if a.TierRank != nil {
					rank = *a.TierRank
				}
				for i := range rs {
					rs[i].Affordable = a.Status == "active" && a.Balance >= rs[i].PointsCost && (rs[i].MinTierRank == nil || rank >= *rs[i].MinTierRank)
				}
			}
			return handle.Page(rs, nil)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/loyalty/rewards/{id}:redeem", Summary: "Redeem a reward with my points",
		Request: MyRewardInput{}, Response: RewardRedemption{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyRewardInput) (RewardRedemption, error) {
			c, a, err := m.myAccount(ctx, tx)
			if err != nil {
				return RewardRedemption{}, err
			}
			rid, err := handle.ID(r)
			if err != nil {
				return RewardRedemption{}, err
			}
			return m.RedeemReward(ctx, tx, c.PropertyID, a.ID, RedeemRewardInput{RewardID: rid, Quantity: in.Quantity}, "member_app")
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/redemptions", Summary: "My reward redemptions", Response: RewardRedemption{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RewardRedemption], error) {
			_, a, err := m.myAccount(ctx, tx)
			if err != nil {
				return httpx.Page[RewardRedemption]{}, err
			}
			return handle.Page(handle.List[RewardRedemption](tx.Query(ctx, redemptionSelect+` WHERE r.account_id = $1 ORDER BY r.created_at DESC LIMIT 100`, a.ID)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/folios/{id}:pay-with-points", Summary: "Pay my folio with points",
		Request: MyPointsPaymentInput{}, Response: LoyaltyRedeemResult{}, Status: http.StatusOK, Idempotent: true,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyPointsPaymentInput) (LoyaltyRedeemResult, error) {
			c, a, err := m.myAccount(ctx, tx)
			if err != nil {
				return LoyaltyRedeemResult{}, err
			}
			fid, err := handle.ID(r)
			if err != nil {
				return LoyaltyRedeemResult{}, err
			}
			return m.RedeemOnFolio(ctx, tx, c.PropertyID, a.ID, LoyaltyRedeemInput{FolioID: fid, Points: in.Points, Amount: in.Amount},
				r.Header.Get("Idempotency-Key"), &c.ID)
		})})
}

// MyRewardInput redeems a reward from the Member App.
type MyRewardInput struct {
	Quantity int `json:"quantity,omitempty"`
}

// MyPointsPaymentInput pays a folio with points from the Member App.
type MyPointsPaymentInput struct {
	Points int64  `json:"points,omitempty"`
	Amount string `json:"amount,omitempty"`
}
