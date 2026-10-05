package loyalty

// PRD P5 EP-18 Advanced Loyalty — rewards: Reward Eligibility rules
// (FR-LOY-P5-04: tier, segment, activity, period, limit per customer),
// rewards issued without points (Top Spender programme, journey, staff;
// FR-LOY-P5-05) and the loyalty programme cost against the budget of 2% of
// the net revenue of the participating lines (FR-LOY-P5-06, PRD P5 §16 #14).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// RewardRules is the Reward Eligibility master (FR-LOY-P5-04).
var RewardRules = &resource.Def{
	Key: "crm.loyalty_reward_rule", Module: "crm", Perm: "crm.loyalty_reward_rule", Path: "/api/v1/crm/loyalty/reward-rules",
	Table: "crm.loyalty_reward_rules", Name: "Reward Eligibility Rule", Plural: "Reward Eligibility", Tag: "Loyalty", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "code, id", SchemaName: "LoyaltyRewardRule",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "rewardId", Column: "reward_id", Label: "Reward (empty = every reward)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.loyalty_rewards", SameProperty: true, Label: "reward"}},
		{Name: "minTierId", Column: "min_tier_id", Label: "Minimum Tier", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "crm.loyalty_tiers", SameProperty: true, Label: "tier"}},
		{Name: "segmentId", Column: "segment_id", Label: "CRM Segment", Kind: resource.UUID, Ref: &resource.Ref{Table: "crm.segments", SameProperty: true, Label: "segment"}},
		{Name: "minVisits", Column: "min_visits", Label: "Minimum Visit Days (lookback)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "minSpend", Column: "min_spend", Label: "Minimum Spend (lookback)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "lookbackDays", Column: "lookback_days", Label: "Activity Lookback (days)", Kind: resource.Int, Default: int64(365), Min: resource.Min(1),
			MaxN: resource.Max(1095)},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "maxPerCustomer", Column: "max_per_customer", Label: "Limit per Customer", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "limitPeriod", Column: "limit_period", Label: "Limit Period", Kind: resource.Enum, Enum: []string{"lifetime", "year", "month"}, Default: "year"},
		resource.Status("active", "inactive")},
}

func init() {
	RewardRules.Hooks.BeforeWrite = validPeriod
	rules.RegisterPolicy(rules.PolicyDef{Code: BudgetPolicyCode, Category: "Loyalty Policies", Name: "Loyalty programme budget",
		Description: "Maximum loyalty programme cost (points issued × redemption value + rewards issued) as a percentage of the net revenue of the participating lines",
		Default:     DefaultBudgetPolicy})
}

// ── eligibility engine (pure) ─────────────────────────────────────────────

// RewardRuleCheck is what one rule is evaluated against.
type RewardRuleCheck struct {
	Today          time.Time
	ValidFrom      *time.Time
	ValidTo        *time.Time
	TierRank       int  // -1 without a tier
	MinTierRank    *int // nil: no tier condition
	InSegment      *bool
	Visits         int
	MinVisits      *int
	Spend          decimal.Decimal
	MinSpend       *decimal.Decimal
	Used           int // rewards of this kind received in the limit period
	MaxPerCustomer *int
	Quantity       int
}

// Reasons a reward rule is not met.
const (
	ReasonNotValid     = "not_valid_today"
	ReasonTier         = "tier_required"
	ReasonSegment      = "segment_required"
	ReasonVisits       = "activity_required"
	ReasonSpend        = "spend_required"
	ReasonLimitReached = "limit_reached"
)

// EvaluateRewardRule checks one rule; reason is empty when it is met.
func EvaluateRewardRule(c RewardRuleCheck) string {
	switch {
	case (c.ValidFrom != nil && c.Today.Before(*c.ValidFrom)) || (c.ValidTo != nil && c.Today.After(*c.ValidTo)):
		return ReasonNotValid
	case c.MinTierRank != nil && c.TierRank < *c.MinTierRank:
		return ReasonTier
	case c.InSegment != nil && !*c.InSegment:
		return ReasonSegment
	case c.MinVisits != nil && c.Visits < *c.MinVisits:
		return ReasonVisits
	case c.MinSpend != nil && c.Spend.LessThan(*c.MinSpend):
		return ReasonSpend
	case c.MaxPerCustomer != nil && c.Used+max(c.Quantity, 1) > *c.MaxPerCustomer:
		return ReasonLimitReached
	}
	return ""
}

var reasonText = map[string]string{ReasonNotValid: "outside the validity of its eligibility rule", ReasonTier: "needs a higher loyalty tier",
	ReasonSegment: "is reserved for a customer segment", ReasonVisits: "needs more visits", ReasonSpend: "needs more spend",
	ReasonLimitReached: "has reached its limit per customer"}

type rewardRule struct {
	ID          uuid.UUID  `db:"id"`
	Code        string     `db:"code"`
	Name        string     `db:"name"`
	RewardID    *uuid.UUID `db:"reward_id"`
	MinTierRank *int       `db:"min_tier_rank"`
	SegmentID   *uuid.UUID `db:"segment_id"`
	MinVisits   *int       `db:"min_visits"`
	MinSpend    *string    `db:"min_spend"`
	Lookback    int        `db:"lookback_days"`
	ValidFrom   *time.Time `db:"valid_from"`
	ValidTo     *time.Time `db:"valid_to"`
	Max         *int       `db:"max_per_customer"`
	LimitPeriod string     `db:"limit_period"`
}

func activeRewardRules(ctx context.Context, q dbtx.Querier, property uuid.UUID, reward *uuid.UUID) ([]rewardRule, error) {
	return handle.List[rewardRule](q.Query(ctx, `SELECT r.id, r.code, r.name, r.reward_id, t.rank AS min_tier_rank, r.segment_id, r.min_visits,
		r.min_spend::text AS min_spend, r.lookback_days, r.valid_from, r.valid_to, r.max_per_customer, r.limit_period
		FROM crm.loyalty_reward_rules r LEFT JOIN crm.loyalty_tiers t ON t.id = r.min_tier_id
		WHERE r.property_id = $1 AND r.status = 'active' AND r.archived_at IS NULL AND ($2::uuid IS NULL OR r.reward_id IS NULL OR r.reward_id = $2)
		ORDER BY r.code, r.id`, property, reward))
}

func limitStart(period string, today time.Time) time.Time {
	switch period {
	case "month":
		return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	case "year":
		return time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

// ruleCheck gathers the facts of a customer for one rule.
func ruleCheck(ctx context.Context, q dbtx.Querier, a LoyaltyAccount, r rewardRule, reward uuid.UUID, qty int, today time.Time) (RewardRuleCheck, error) {
	c := RewardRuleCheck{Today: today, ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, TierRank: -1, MinTierRank: r.MinTierRank, MinVisits: r.MinVisits,
		MaxPerCustomer: r.Max, Quantity: qty}
	if a.TierRank != nil {
		c.TierRank = *a.TierRank
	}
	if r.SegmentID != nil {
		var in bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.segment_members WHERE segment_id = $1 AND customer_id = $2)`, *r.SegmentID,
			a.CustomerID).Scan(&in); err != nil {
			return c, err
		}
		c.InSegment = &in
	}
	if r.MinSpend != nil {
		d := dec(*r.MinSpend)
		c.MinSpend = &d
	}
	if r.MinVisits != nil || r.MinSpend != nil {
		since := today.AddDate(0, 0, -r.Lookback)
		var spend string
		if err := q.QueryRow(ctx, `SELECT count(DISTINCT business_date)::int, coalesce(sum(net_amount) FILTER (WHERE NOT liability), 0)::text
			FROM reporting.eng_folio_lines WHERE customer_id = $1 AND business_date >= $2::date`, a.CustomerID, since).Scan(&c.Visits, &spend); err != nil {
			return c, err
		}
		c.Spend = dec(spend)
	}
	if r.Max != nil {
		from := localStart(ctx, q, a.PropertyID, limitStart(r.LimitPeriod, today))
		if err := q.QueryRow(ctx, `SELECT coalesce((SELECT sum(quantity) FROM crm.loyalty_reward_redemptions WHERE account_id = $1 AND reward_id = $2
			AND status <> 'cancelled' AND created_at >= $3), 0)::int + coalesce((SELECT sum(quantity) FROM crm.loyalty_reward_issues
			WHERE customer_id = $4 AND reward_id = $2 AND status <> 'cancelled' AND created_at >= $3), 0)::int`, a.ID, reward, from,
			a.CustomerID).Scan(&c.Used); err != nil {
			return c, err
		}
	}
	return c, nil
}

// RewardEligibility is the eligibility of an account for a reward: ok when
// the reward has no active rule or one rule is met.
func RewardEligibility(ctx context.Context, q dbtx.Querier, a LoyaltyAccount, reward uuid.UUID, qty int) (bool, string, error) {
	rs, err := activeRewardRules(ctx, q, a.PropertyID, &reward)
	if err != nil || len(rs) == 0 {
		return err == nil, "", err
	}
	today := localToday(ctx, q, a.PropertyID)
	first := ""
	for _, r := range rs {
		c, err := ruleCheck(ctx, q, a, r, reward, qty, today)
		if err != nil {
			return false, "", err
		}
		reason := EvaluateRewardRule(c)
		if reason == "" {
			return true, "", nil
		}
		if first == "" || reason == ReasonLimitReached {
			first = reason
		}
	}
	return false, first, nil
}

// CheckRewardEligibility rejects a redemption the eligibility rules do not
// allow (called by RedeemReward).
func CheckRewardEligibility(ctx context.Context, q dbtx.Querier, a LoyaltyAccount, reward uuid.UUID, qty int) error {
	ok, reason, err := RewardEligibility(ctx, q, a, reward, max(qty, 1))
	if err != nil || ok {
		return err
	}
	if reason == ReasonLimitReached {
		return errs.Conflict("reward_limit_reached", "this reward "+reasonText[reason])
	}
	return errs.Conflict("not_eligible", "this reward "+reasonText[reason])
}

// LoyaltyEligibleReward is a reward of the catalogue with the eligibility
// of one account.
type LoyaltyEligibleReward struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	Description *string   `json:"description" db:"description"`
	RewardType  string    `json:"rewardType" db:"reward_type"`
	PointsCost  int64     `json:"pointsCost" db:"points_cost"`
	Stock       *int      `json:"stock" db:"stock"`
	ValidTo     *string   `json:"validTo" db:"valid_to"`
	Eligible    bool      `json:"eligible" db:"-" doc:"The eligibility rules allow this reward"`
	Reason      *string   `json:"reason" db:"-" enum:"not_valid_today,tier_required,segment_required,activity_required,spend_required,limit_reached"`
	Affordable  bool      `json:"affordable" db:"-" doc:"Eligible and enough points"`
}

// EligibleRewards lists the available rewards with the eligibility of an account.
func EligibleRewards(ctx context.Context, q dbtx.Querier, a LoyaltyAccount) ([]LoyaltyEligibleReward, error) {
	today := localToday(ctx, q, a.PropertyID)
	rs, err := handle.List[LoyaltyEligibleReward](q.Query(ctx, `SELECT w.id, w.code, w.name, w.description, w.reward_type, w.points_cost, w.stock,
		to_char(w.valid_to, 'YYYY-MM-DD') AS valid_to FROM crm.loyalty_rewards w LEFT JOIN crm.loyalty_tiers t ON t.id = w.min_tier_id
		WHERE w.property_id = $1 AND w.status = 'active' AND w.archived_at IS NULL AND (w.valid_from IS NULL OR w.valid_from <= $2::date)
		AND (w.valid_to IS NULL OR w.valid_to >= $2::date) AND (w.stock IS NULL OR w.stock > 0)
		AND (w.min_tier_id IS NULL OR coalesce((SELECT rank FROM crm.loyalty_tiers WHERE id = $3::uuid), -1) >= t.rank)
		ORDER BY w.points_cost, w.code`, a.PropertyID, today, a.TierID))
	if err != nil {
		return nil, err
	}
	for i := range rs {
		ok, reason, err := RewardEligibility(ctx, q, a, rs[i].ID, 1)
		if err != nil {
			return nil, err
		}
		rs[i].Eligible = ok && a.Status == "active"
		if reason != "" {
			r := reason
			rs[i].Reason = &r
		}
		rs[i].Affordable = rs[i].Eligible && a.Balance >= rs[i].PointsCost
	}
	return rs, nil
}

// ── budget (FR-LOY-P5-06, PRD P5 §16 #14) ─────────────────────────────────

// BudgetPolicy is the "Loyalty programme budget" club policy.
type BudgetPolicy struct {
	BudgetPercent     string   `json:"budgetPercent" doc:"Maximum programme cost as % of the net revenue of the participating lines"`
	Lines             []string `json:"lines" doc:"Participating business lines"`
	EnforceAutomatic  bool     `json:"enforceAutomatic" doc:"Automatic rewards (Top Spender programme, journeys) are skipped above the budget"`
	IncludePointsCost bool     `json:"includePointsCost" doc:"Points issued (× redemption value) count toward the cost"`
}

// DefaultBudgetPolicy: 2% of the net revenue of golf, F&B, sport club and bungalow.
var DefaultBudgetPolicy = BudgetPolicy{BudgetPercent: "2", Lines: []string{"golf", "pos", "sportclub", "stay"}, EnforceAutomatic: true,
	IncludePointsCost: true}

// BudgetPolicyCode is the club policy code.
const BudgetPolicyCode = "crm.loyalty_budget"

// LoyaltyBudget is the programme cost of a month against its budget.
type LoyaltyBudget struct {
	Period        string   `json:"period" doc:"YYYY-MM"`
	From          string   `json:"from"`
	To            string   `json:"to"`
	Lines         []string `json:"lines"`
	NetRevenue    string   `json:"netRevenue" doc:"Net revenue of the participating lines"`
	BudgetPercent string   `json:"budgetPercent"`
	Cap           string   `json:"cap" doc:"Net revenue × budget %"`
	PointsIssued  int64    `json:"pointsIssued" doc:"Points earned and bonus points (net of reversals)"`
	PointValue    string   `json:"pointValue"`
	PointsCost    string   `json:"pointsCost"`
	RewardCost    string   `json:"rewardCost" doc:"Rewards issued without points (cost)"`
	RedeemedCost  string   `json:"redeemedCost" doc:"Cost of rewards redeemed with points (information: already paid with points)"`
	TotalCost     string   `json:"totalCost"`
	Remaining     string   `json:"remaining"`
	Utilisation   string   `json:"utilisation" doc:"Total cost ÷ cap"`
	WithinBudget  bool     `json:"withinBudget"`
}

// MonthRange parses YYYY-MM (default: the month of today).
func MonthRange(period string, today time.Time) (string, time.Time, time.Time, error) {
	if strings.TrimSpace(period) == "" {
		period = today.Format("2006-01")
	}
	from, err := time.Parse("2006-01", period)
	if err != nil {
		return "", time.Time{}, time.Time{}, handle.Invalid("period", "invalid_period", "period must be YYYY-MM")
	}
	return period, from, from.AddDate(0, 1, -1), nil
}

// Budget computes the programme cost of a month.
func Budget(ctx context.Context, q dbtx.Querier, property uuid.UUID, period string) (LoyaltyBudget, error) {
	pol, _, err := rules.PolicyAt(ctx, q, BudgetPolicyCode, property, DefaultBudgetPolicy)
	if err != nil {
		return LoyaltyBudget{}, err
	}
	period, from, to, err := MonthRange(period, localToday(ctx, q, property))
	if err != nil {
		return LoyaltyBudget{}, err
	}
	lp, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return LoyaltyBudget{}, err
	}
	lines := pol.Lines
	if len(lines) == 0 {
		lines = lp.EligibleLines
	}
	if lines == nil {
		lines = []string{}
	}
	pct := dec(pol.BudgetPercent)
	var revenue, rewardCost, redeemedCost string
	var points int64
	// The month of the property's calendar: from local midnight to local midnight.
	start, end := localStart(ctx, q, property, from), localStart(ctx, q, property, to.AddDate(0, 0, 1))
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(net_amount), 0)::text FROM reporting.eng_folio_lines WHERE property_id = $1 AND NOT liability
		AND business_line = ANY($2::text[]) AND business_date BETWEEN $3::date AND $4::date`, property, lines, from, to).Scan(&revenue); err != nil {
		return LoyaltyBudget{}, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(points) FILTER (WHERE kind = 'earned' OR (kind = 'reversed' AND reverses_id IN
		  (SELECT id FROM crm.loyalty_ledger x WHERE x.kind = 'earned')) OR (kind = 'adjusted' AND points > 0 AND source_type <> 'crm.customer_merge')), 0)
		FROM crm.loyalty_ledger WHERE property_id = $1 AND occurred_at >= $2 AND occurred_at < $3`, property, start, end).Scan(&points); err != nil {
		return LoyaltyBudget{}, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(total_cost), 0)::text FROM crm.loyalty_reward_issues WHERE property_id = $1 AND status <> 'cancelled'
		AND created_at >= $2 AND created_at < $3`, property, start, end).Scan(&rewardCost); err != nil {
		return LoyaltyBudget{}, err
	}
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(r.quantity * w.unit_cost), 0)::text FROM crm.loyalty_reward_redemptions r JOIN crm.loyalty_rewards w
		ON w.id = r.reward_id WHERE r.property_id = $1 AND r.status <> 'cancelled' AND r.created_at >= $2 AND r.created_at < $3`,
		property, start, end).Scan(&redeemedCost); err != nil {
		return LoyaltyBudget{}, err
	}
	value := lp.Value()
	pointsCost := decimal.Zero
	if pol.IncludePointsCost {
		pointsCost = value.Mul(decimal.NewFromInt(points))
	}
	rev := dec(revenue)
	limit := rev.Mul(pct).Div(decimal.NewFromInt(100)).Round(2)
	total := pointsCost.Add(dec(rewardCost))
	out := LoyaltyBudget{Period: period, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"), Lines: lines, NetRevenue: rev.String(),
		BudgetPercent: pct.String(), Cap: limit.String(), PointsIssued: points, PointValue: value.String(), PointsCost: pointsCost.String(),
		RewardCost: dec(rewardCost).String(), RedeemedCost: dec(redeemedCost).String(), TotalCost: total.String(), Remaining: limit.Sub(total).String(),
		Utilisation: "0", WithinBudget: total.LessThanOrEqual(limit)}
	if limit.IsPositive() {
		out.Utilisation = total.Div(limit).Round(4).String()
	}
	return out, nil
}

// WithinBudget reports whether an extra cost keeps the month within the
// budget (always true when the policy does not enforce automatic rewards).
func WithinBudget(ctx context.Context, q dbtx.Querier, property uuid.UUID, extra decimal.Decimal) (bool, error) {
	pol, _, err := rules.PolicyAt(ctx, q, BudgetPolicyCode, property, DefaultBudgetPolicy)
	if err != nil || !pol.EnforceAutomatic {
		return true, err
	}
	b, err := Budget(ctx, q, property, "")
	if err != nil {
		return false, err
	}
	return dec(b.TotalCost).Add(extra).LessThanOrEqual(dec(b.Cap)), nil
}

// ── rewards issued without points ─────────────────────────────────────────

// LoyaltyRewardIssue is a reward given to a customer without points.
type LoyaltyRewardIssue struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	Number         string     `json:"number" db:"number"`
	CustomerID     uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName   string     `json:"customerName" db:"customer_name"`
	RewardID       uuid.UUID  `json:"rewardId" db:"reward_id"`
	RewardCode     string     `json:"rewardCode" db:"reward_code"`
	RewardName     string     `json:"rewardName" db:"reward_name"`
	RewardType     string     `json:"rewardType" db:"reward_type"`
	Quantity       int        `json:"quantity" db:"quantity"`
	Source         string     `json:"source" db:"source" enum:"top_spender,journey,staff"`
	SourceRef      *string    `json:"sourceRef" db:"source_ref"`
	Period         *string    `json:"period" db:"period"`
	UnitCost       string     `json:"unitCost" db:"unit_cost"`
	TotalCost      string     `json:"totalCost" db:"total_cost"`
	Status         string     `json:"status" db:"status" enum:"issued,fulfilled,cancelled"`
	FulfilmentCode *string    `json:"fulfilmentCode" db:"fulfilment_code"`
	VoucherCodes   []string   `json:"voucherCodes" db:"voucher_codes"`
	Note           *string    `json:"note" db:"note"`
	FulfilledAt    *time.Time `json:"fulfilledAt" db:"fulfilled_at"`
	CancelledAt    *time.Time `json:"cancelledAt" db:"cancelled_at"`
	CancelReason   *string    `json:"cancelReason" db:"cancel_reason"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

const issueSelect = `SELECT i.id, i.number, i.customer_id, c.name AS customer_name, i.reward_id, w.code AS reward_code, w.name AS reward_name,
	w.reward_type, i.quantity, i.source, i.source_ref, i.period, trim_scale(i.unit_cost)::text AS unit_cost, trim_scale(i.total_cost)::text AS total_cost,
	i.status, i.fulfilment_code, i.voucher_codes, i.note, i.fulfilled_at, i.cancelled_at, i.cancel_reason, i.created_at
	FROM crm.loyalty_reward_issues i JOIN crm.customers c ON c.id = i.customer_id JOIN crm.loyalty_rewards w ON w.id = i.reward_id`

// GetRewardIssue loads an issued reward of a property.
func GetRewardIssue(ctx context.Context, q dbtx.Querier, property, iid uuid.UUID) (LoyaltyRewardIssue, error) {
	rows, err := q.Query(ctx, issueSelect+` WHERE i.id = $1 AND i.property_id = $2`, iid, property)
	return handle.One[LoyaltyRewardIssue](rows, err, "issued reward")
}

// RewardIssues lists issued rewards (optionally of a customer).
func RewardIssues(ctx context.Context, q dbtx.Querier, property uuid.UUID, customer *uuid.UUID, status, source string, limit int) ([]LoyaltyRewardIssue, error) {
	if limit <= 0 {
		limit = 100
	}
	return handle.List[LoyaltyRewardIssue](q.Query(ctx, issueSelect+` WHERE i.property_id = $1 AND ($2::uuid IS NULL OR i.customer_id = $2)
		AND ($3 = '' OR i.status = $3) AND ($4 = '' OR i.source = $4) ORDER BY i.created_at DESC, i.id LIMIT $5`, property, customer, status, source, limit))
}

// IssueRequest gives a reward without points.
type IssueRequest struct {
	PropertyID    uuid.UUID
	CustomerID    uuid.UUID
	RewardID      uuid.UUID
	Quantity      int
	Source        string // top_spender | journey | staff
	SourceID      *uuid.UUID
	SourceRef     string
	Period        string
	Note          string
	Key           string // idempotency key (per property)
	EnforceBudget bool
}

// Issue outcomes.
const (
	IssueCreated       = "issued"
	IssueExisting      = "existing"
	IssueSkippedBudget = "skipped_budget"
)

// IssueReward issues a reward (stock, Commercial voucher, member notice,
// crm.reward_issued). The idempotency key returns the first issue; above
// the budget an automatic reward is skipped.
func (m *Module) IssueReward(ctx context.Context, tx pgx.Tx, r IssueRequest) (LoyaltyRewardIssue, string, error) {
	if r.Quantity <= 0 {
		r.Quantity = 1
	}
	if r.Key != "" {
		var existing uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_reward_issues WHERE property_id = $1 AND idempotency_key = $2`, r.PropertyID, r.Key).Scan(&existing)
		if err == nil {
			i, err := GetRewardIssue(ctx, tx, r.PropertyID, existing)
			return i, IssueExisting, err
		}
		if !dbtx.IsNoRows(err) {
			return LoyaltyRewardIssue{}, "", err
		}
	}
	var w struct {
		Code, Name, Type, Status string
		VoucherType              *string
		Stock                    *int
		UnitCost                 string
		From, To                 *time.Time
		Archived                 *time.Time
	}
	err := tx.QueryRow(ctx, `SELECT code, name, reward_type, status, stock, unit_cost::text, valid_from, valid_to, archived_at, voucher_type_ref
		FROM crm.loyalty_rewards WHERE id = $1 AND property_id = $2 FOR UPDATE`, r.RewardID, r.PropertyID).
		Scan(&w.Code, &w.Name, &w.Type, &w.Status, &w.Stock, &w.UnitCost, &w.From, &w.To, &w.Archived, &w.VoucherType)
	if dbtx.IsNoRows(err) {
		return LoyaltyRewardIssue{}, "", errs.NotFound("reward")
	}
	if err != nil {
		return LoyaltyRewardIssue{}, "", err
	}
	today := localToday(ctx, tx, r.PropertyID)
	if w.Status != "active" || w.Archived != nil || (w.From != nil && today.Before(*w.From)) || (w.To != nil && today.After(*w.To)) {
		return LoyaltyRewardIssue{}, "", errs.Conflict("reward_unavailable", "reward "+w.Name+" is not available")
	}
	if w.Stock != nil && *w.Stock < r.Quantity {
		return LoyaltyRewardIssue{}, "", errs.Conflict("out_of_stock", "reward "+w.Name+" is out of stock")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND property_id = $2 AND status = 'active' AND erased_at IS NULL)`,
		r.CustomerID, r.PropertyID).Scan(&ok); err != nil {
		return LoyaltyRewardIssue{}, "", err
	}
	if !ok {
		return LoyaltyRewardIssue{}, "", handle.Invalid("customerId", "not_found", "active customer not found in this property")
	}
	unit := dec(w.UnitCost)
	total := unit.Mul(decimal.NewFromInt(int64(r.Quantity)))
	if r.EnforceBudget {
		within, err := WithinBudget(ctx, tx, r.PropertyID, total)
		if err != nil {
			return LoyaltyRewardIssue{}, "", err
		}
		if !within {
			return LoyaltyRewardIssue{}, IssueSkippedBudget, nil
		}
	}
	if w.Stock != nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_rewards SET stock = stock - $2 WHERE id = $1`, r.RewardID, r.Quantity); err != nil {
			return LoyaltyRewardIssue{}, "", err
		}
	}
	num, err := numbering.Next(ctx, tx, r.PropertyID, "RWI", today)
	if err != nil {
		return LoyaltyRewardIssue{}, "", err
	}
	iid := id.New()
	var code *string
	var vouchers []string
	if w.Type == "voucher" {
		if w.VoucherType != nil && *w.VoucherType != "" && m.IssueVoucher != nil {
			vouchers, err = m.IssueVoucher(ctx, tx, RewardVoucher{PropertyID: r.PropertyID, TypeCode: *w.VoucherType, CustomerID: r.CustomerID,
				Quantity: r.Quantity, RedemptionID: iid, Number: num, RewardName: w.Name})
			if err != nil {
				return LoyaltyRewardIssue{}, "", err
			}
		}
		c := randomCode("RI-", 5)
		if len(vouchers) > 0 {
			c = vouchers[0]
		}
		code = &c
	}
	if vouchers == nil {
		vouchers = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_reward_issues (id, property_id, number, customer_id, reward_id, quantity, source, source_id, source_ref,
		period, unit_cost, total_cost, fulfilment_code, voucher_codes, note, idempotency_key, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::numeric,$12::numeric,$13,$14,$15,$16,$17,$17)`, iid, r.PropertyID, num, r.CustomerID, r.RewardID,
		r.Quantity, r.Source, r.SourceID, nullStr(r.SourceRef), nullStr(r.Period), unit.String(), total.String(), code, vouchers, nullStr(r.Note),
		nullStr(r.Key), actor(ctx)); err != nil {
		return LoyaltyRewardIssue{}, "", err
	}
	if m.Events != nil {
		pid := r.PropertyID
		if _, err := m.Events.Publish(ctx, tx, EventRewardIssued, "crm.loyalty_reward_issue", &iid, &pid, map[string]any{"issueId": iid, "number": num,
			"customerId": r.CustomerID, "rewardId": r.RewardID, "rewardCode": w.Code, "rewardType": w.Type, "quantity": r.Quantity, "source": r.Source,
			"sourceId": r.SourceID, "sourceRef": nullStr(r.SourceRef), "period": nullStr(r.Period), "unitCost": unit.String(), "totalCost": total.String(),
			"voucherCodes": vouchers}); err != nil {
			return LoyaltyRewardIssue{}, "", err
		}
	}
	data := map[string]any{"reward": w.Name, "number": num}
	if code != nil {
		data["code"] = *code
	}
	if err := m.notifyCustomer(ctx, tx, r.PropertyID, r.CustomerID, "crm.loyalty_reward_issued", data); err != nil {
		return LoyaltyRewardIssue{}, "", err
	}
	i, err := GetRewardIssue(ctx, tx, r.PropertyID, iid)
	return i, IssueCreated, err
}

// LoyaltyRewardIssueInput issues a reward from the back office.
type LoyaltyRewardIssueInput struct {
	CustomerID uuid.UUID `json:"customerId"`
	RewardID   uuid.UUID `json:"rewardId"`
	Quantity   int       `json:"quantity,omitempty"`
	Note       string    `json:"note" doc:"Reason (required)"`
}

// LoyaltyRewardIssueAction completes or cancels an issued reward.
type LoyaltyRewardIssueAction struct {
	Note   string `json:"note,omitempty"`
	Reason string `json:"reason,omitempty" doc:"Cancel: required"`
}

// SetIssueStatus marks an issued reward as handed over or cancels it (stock back).
func SetIssueStatus(ctx context.Context, tx pgx.Tx, property, iid uuid.UUID, to string, in LoyaltyRewardIssueAction) (LoyaltyRewardIssue, LoyaltyRewardIssue, error) {
	before, err := GetRewardIssue(ctx, tx, property, iid)
	if err != nil {
		return before, before, err
	}
	if before.Status != "issued" {
		return before, before, errs.Conflict("issue_closed", fmt.Sprintf("reward %s is %s", before.Number, before.Status))
	}
	switch to {
	case "fulfilled":
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_issues SET status = 'fulfilled', fulfilled_at = now(), note = coalesce($2, note), updated_by = $3
			WHERE id = $1`, iid, nullStr(in.Note), actor(ctx)); err != nil {
			return before, before, err
		}
	case "cancelled":
		if err := handle.Required("reason", in.Reason); err != nil {
			return before, before, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_issues SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3
			WHERE id = $1`, iid, in.Reason, actor(ctx)); err != nil {
			return before, before, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_rewards SET stock = stock + $2 WHERE id = $1 AND stock IS NOT NULL`, before.RewardID, before.Quantity); err != nil {
			return before, before, err
		}
	}
	after, err := GetRewardIssue(ctx, tx, property, iid)
	return before, after, err
}

// AwardPoints posts bonus points (journey incentive) to the active account
// of a customer, once per key; nil when the customer has no active account.
func (m *Module) AwardPoints(ctx context.Context, tx pgx.Tx, customer uuid.UUID, points int64, sourceType string, sourceID uuid.UUID, key, desc string) (*LoyaltyEntry, error) {
	var aid uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1 AND status = 'active'`, customer).Scan(&aid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return nil, err
	}
	sid := sourceID
	e, _, err := m.post(ctx, tx, &a, posting{Kind: KindEarned, Points: points, SourceType: sourceType, SourceID: &sid, Key: key, Description: desc})
	if err != nil {
		return nil, err
	}
	return &e, nil
}
