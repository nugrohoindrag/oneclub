package loyalty

// PRD P5 EP-18 Advanced Loyalty — tiers (FR-LOY-P5-01/02): the tier
// programme evaluates the qualifying spend / points of the evaluation window
// once a year (default 1 January, 12-month window), upgrades at any time,
// and lowers a tier only after a grace period (default 3 months, PRD P5 §16
// #14). Tier benefits (points multiplier, booking window +days, F&B
// discount, VIP event access, priority service) are read by pricing and
// booking through BenefitsOf / reporting.crm_tier_benefits (internal/app
// wires the golf booking window).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// P5 loyalty events (PRD P5 §11, docs/p5-contracts.md).
const (
	EventTierEvaluated = "crm.tier_evaluated"
	EventRewardIssued  = "crm.reward_issued"
)

// Tier evaluation kinds.
const (
	EvalAnnual      = "annual"
	EvalPeriodic    = "periodic"
	EvalGraceReview = "grace_review"
)

// Tier evaluation outcomes.
const (
	OutcomeUpgraded     = "upgraded"
	OutcomeRetained     = "retained"
	OutcomeGraceStarted = "grace_started"
	OutcomeInGrace      = "in_grace"
	OutcomeDowngraded   = "downgraded"
	OutcomeLocked       = "locked"
)

// TierProgramPolicy is the "Loyalty tier programme" club policy (Loyalty
// Policies, PRD P5 §16 #14).
type TierProgramPolicy struct {
	Enabled         bool `json:"enabled" doc:"P5 tier programme: annual evaluation with grace; off = the P3 periodic evaluation"`
	PeriodMonths    int  `json:"periodMonths" doc:"Qualifying window of spend / points (months before the evaluation date)"`
	EvaluationMonth int  `json:"evaluationMonth" doc:"Month (1–12) of the annual evaluation"`
	EvaluationDay   int  `json:"evaluationDay" doc:"Day of the month of the annual evaluation"`
	GraceMonths     int  `json:"graceMonths" doc:"A member below the threshold keeps the tier this long before the downgrade (0 = immediately)"`
	NotifyGrace     bool `json:"notifyGrace" doc:"Tell the member when the grace period starts"`
}

// DefaultTierProgram: yearly on 1 January over 12 months, 3 months grace.
var DefaultTierProgram = TierProgramPolicy{Enabled: true, PeriodMonths: 12, EvaluationMonth: 1, EvaluationDay: 1, GraceMonths: 3, NotifyGrace: true}

// TierProgramPolicyCode is the club policy code.
const TierProgramPolicyCode = "crm.loyalty_tier_program"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: TierProgramPolicyCode, Category: "Loyalty Policies", Name: "Loyalty tier programme",
		Description: "Annual tier evaluation date and window, grace period before a downgrade, grace notice", Default: DefaultTierProgram})
	Tiers.Fields = append(Tiers.Fields,
		resource.Field{Name: "bookingWindowDays", Column: "booking_window_days", Label: "Booking Window + Days", Kind: resource.Int, Default: int64(0),
			Min: resource.Min(0), MaxN: resource.Max(60)},
		resource.Field{Name: "fnbDiscountPercent", Column: "fnb_discount_percent", Label: "F&B Discount (%)", Kind: resource.Decimal, Default: "0",
			Min: resource.Min(0), MaxN: resource.Max(100)},
		resource.Field{Name: "eventAccess", Column: "event_access", Label: "VIP Event Access", Kind: resource.Bool, Default: false},
		resource.Field{Name: "priorityService", Column: "priority_service", Label: "Priority Service", Kind: resource.Bool, Default: false})
	Rewards.Fields = append(Rewards.Fields,
		resource.Field{Name: "unitCost", Column: "unit_cost", Label: "Cost per Reward (programme cost)", Kind: resource.Decimal, Default: "0",
			Min: resource.Min(0)})
}

// LoadTierProgram returns the tier programme in force.
func LoadTierProgram(ctx context.Context, q dbtx.Querier, property uuid.UUID) (TierProgramPolicy, rules.PolicyRef, error) {
	p, ref, err := rules.PolicyAt(ctx, q, TierProgramPolicyCode, property, DefaultTierProgram)
	if p.PeriodMonths <= 0 {
		p.PeriodMonths = 12
	}
	if p.EvaluationMonth < 1 || p.EvaluationMonth > 12 {
		p.EvaluationMonth = 1
	}
	if p.EvaluationDay < 1 || p.EvaluationDay > 28 {
		p.EvaluationDay = 1
	}
	if p.GraceMonths < 0 {
		p.GraceMonths = 0
	}
	return p, ref, err
}

// AnnualDue reports whether day is the annual evaluation date.
func (p TierProgramPolicy) AnnualDue(day time.Time) bool {
	return int(day.Month()) == p.EvaluationMonth && day.Day() == p.EvaluationDay
}

// ── engine (pure) ─────────────────────────────────────────────────────────

// TierThreshold is a tier as the engine sees it: the thresholds in force
// (the effective version; see p5_tier_master.go) and the evaluation
// settings of the tier.
type TierThreshold struct {
	ID        uuid.UUID
	Rank      int
	MinPoints int64
	MinSpend  decimal.Decimal
	// QualifyMode "any": the spend OR the points threshold is enough;
	// "all" (default): every threshold above zero must be met.
	QualifyMode string
	// MembershipTypes: the customer must hold an active membership of one
	// of these types (empty: any customer).
	MembershipTypes []string
	PeriodMonths    int  // qualifying window; 0 = the programme window
	GraceMonths     *int // grace before leaving this tier; nil = the programme grace
	NoDowngrade     bool // members of this tier are never downgraded automatically
}

// TierBasis is what an account brings to a tier: points and spend of the
// tier's window and the membership types held.
type TierBasis struct {
	Points          int64
	Spend           decimal.Decimal
	MembershipTypes []string
}

// Qualifies reports whether the basis meets the tier.
func (t TierThreshold) Qualifies(b TierBasis) bool {
	if len(t.MembershipTypes) > 0 && !slices.ContainsFunc(b.MembershipTypes, func(x string) bool { return slices.Contains(t.MembershipTypes, x) }) {
		return false
	}
	pts := b.Points >= t.MinPoints
	spend := b.Spend.GreaterThanOrEqual(t.MinSpend)
	if t.QualifyMode == QualifyAny {
		if t.MinPoints <= 0 && !t.MinSpend.IsPositive() {
			return true
		}
		return (t.MinPoints > 0 && pts) || (t.MinSpend.IsPositive() && spend)
	}
	return pts && spend
}

// Qualify modes of a tier.
const (
	QualifyAll = "all"
	QualifyAny = "any"
)

// QualifyTier returns the highest-ranked tier whose thresholds the points
// and spend of the window meet (nil when none).
func QualifyTier(tiers []TierThreshold, points int64, spend decimal.Decimal) *TierThreshold {
	return QualifyTierBy(tiers, func(TierThreshold) TierBasis { return TierBasis{Points: points, Spend: spend} })
}

// QualifyTierBy returns the highest-ranked tier the basis of each tier
// (its own window) meets (nil when none).
func QualifyTierBy(tiers []TierThreshold, basis func(TierThreshold) TierBasis) *TierThreshold {
	var best *TierThreshold
	for i := range tiers {
		t := tiers[i]
		if (best == nil || t.Rank > best.Rank) && t.Qualifies(basis(t)) {
			best = &tiers[i]
		}
	}
	return best
}

// TierDecision is the outcome of evaluating one account.
type TierDecision struct {
	Outcome    string
	To         *uuid.UUID // tier after the evaluation
	GraceUntil *time.Time // set while in grace
}

// DecideTier applies the programme to one account: upgrade at once, retain,
// start / keep the grace (annual) or downgrade when the grace has ended.
// The periodic run and the grace review never start a grace period.
func DecideTier(kind string, current *TierThreshold, qualified *TierThreshold, graceUntil *time.Time, locked bool, today time.Time, graceMonths int) TierDecision {
	cur := -1
	var curID *uuid.UUID
	if current != nil {
		cur, curID = current.Rank, &current.ID
	}
	q := -1
	var qID *uuid.UUID
	if qualified != nil {
		q, qID = qualified.Rank, &qualified.ID
	}
	switch {
	case locked:
		return TierDecision{Outcome: OutcomeLocked, To: curID, GraceUntil: graceUntil}
	case q > cur:
		return TierDecision{Outcome: OutcomeUpgraded, To: qID}
	case q == cur:
		return TierDecision{Outcome: OutcomeRetained, To: curID}
	}
	// below the current tier
	if graceUntil != nil {
		if !today.Before(*graceUntil) {
			return TierDecision{Outcome: OutcomeDowngraded, To: qID}
		}
		return TierDecision{Outcome: OutcomeInGrace, To: curID, GraceUntil: graceUntil}
	}
	if kind != EvalAnnual {
		return TierDecision{Outcome: OutcomeRetained, To: curID}
	}
	if graceMonths <= 0 {
		return TierDecision{Outcome: OutcomeDowngraded, To: qID}
	}
	g := today.AddDate(0, graceMonths, 0)
	return TierDecision{Outcome: OutcomeGraceStarted, To: curID, GraceUntil: &g}
}

// ── runs ──────────────────────────────────────────────────────────────────

// LoyaltyTierEvaluationRun is one tier evaluation run.
type LoyaltyTierEvaluationRun struct {
	ID            uuid.UUID `json:"id" db:"id"`
	Number        string    `json:"number" db:"number"`
	Kind          string    `json:"kind" db:"kind" enum:"annual,periodic,grace_review"`
	EvaluatedOn   string    `json:"evaluatedOn" db:"evaluated_on"`
	WindowFrom    string    `json:"windowFrom" db:"window_from"`
	WindowTo      string    `json:"windowTo" db:"window_to"`
	Accounts      int       `json:"accounts" db:"accounts"`
	Upgraded      int       `json:"upgraded" db:"upgraded"`
	Downgraded    int       `json:"downgraded" db:"downgraded"`
	Retained      int       `json:"retained" db:"retained"`
	GraceStarted  int       `json:"graceStarted" db:"grace_started"`
	InGrace       int       `json:"inGrace" db:"in_grace"`
	PolicyVersion int       `json:"policyVersion" db:"policy_version"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
	// Tier threshold versions put in force by this run.
	AppliedVersions []AppliedTierVersion `json:"appliedVersions" db:"applied_versions"`
}

// LoyaltyTierEvaluationLine is the result for one account.
type LoyaltyTierEvaluationLine struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	AccountID         uuid.UUID  `json:"accountId" db:"account_id"`
	AccountNumber     string     `json:"accountNumber" db:"account_number"`
	CustomerID        uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName      string     `json:"customerName" db:"customer_name"`
	FromTierName      *string    `json:"fromTierName" db:"from_tier_name"`
	QualifiedTierName *string    `json:"qualifiedTierName" db:"qualified_tier_name"`
	ToTierID          *uuid.UUID `json:"toTierId" db:"to_tier_id"`
	ToTierName        *string    `json:"toTierName" db:"to_tier_name"`
	Outcome           string     `json:"outcome" db:"outcome" enum:"upgraded,retained,grace_started,in_grace,downgraded,locked"`
	SpendBasis        string     `json:"spendBasis" db:"spend_basis"`
	PointsBasis       int64      `json:"pointsBasis" db:"points_basis"`
	GraceUntil        *string    `json:"graceUntil" db:"grace_until"`
}

// LoyaltyTierEvaluationDetail is a run with its lines.
type LoyaltyTierEvaluationDetail struct {
	LoyaltyTierEvaluationRun
	Lines []LoyaltyTierEvaluationLine `json:"lines"`
}

// LoyaltyTierEvaluationInput runs an evaluation now.
type LoyaltyTierEvaluationInput struct {
	Kind       string      `json:"kind" enum:"annual,periodic,grace_review" doc:"annual: full evaluation with grace; periodic: upgrades and ended grace; grace_review: ended grace only"`
	AccountIDs []uuid.UUID `json:"accountIds,omitempty" doc:"Evaluate only these accounts (default: every active account)"`
	// Re-evaluate now (tier master): put the pending threshold versions in
	// force first (a full annual / periodic run always does).
	ApplyPendingVersions bool `json:"applyPendingVersions,omitempty" doc:"Put the pending tier thresholds in force before evaluating"`
}

const evalRunSelect = `SELECT id, number, kind, to_char(evaluated_on, 'YYYY-MM-DD') AS evaluated_on, to_char(window_from, 'YYYY-MM-DD') AS window_from,
	to_char(window_to, 'YYYY-MM-DD') AS window_to, accounts, upgraded, downgraded, retained, grace_started, in_grace, policy_version, created_at,
	applied_versions FROM crm.loyalty_tier_evaluations`

// TierEvaluation loads a run with its lines.
func TierEvaluation(ctx context.Context, q dbtx.Querier, property, rid uuid.UUID) (LoyaltyTierEvaluationDetail, error) {
	rows, err := q.Query(ctx, evalRunSelect+` WHERE id = $1 AND property_id = $2`, rid, property)
	run, err := handle.One[LoyaltyTierEvaluationRun](rows, err, "tier evaluation")
	if err != nil {
		return LoyaltyTierEvaluationDetail{}, err
	}
	lines, err := handle.List[LoyaltyTierEvaluationLine](q.Query(ctx, `SELECT l.id, l.account_id, a.number AS account_number, a.customer_id,
		c.name AS customer_name, ft.name AS from_tier_name, qt.name AS qualified_tier_name, l.to_tier_id, tt.name AS to_tier_name, l.outcome,
		trim_scale(l.spend_basis)::text AS spend_basis, l.points_basis, to_char(l.grace_until, 'YYYY-MM-DD') AS grace_until
		FROM crm.loyalty_tier_evaluation_lines l JOIN crm.loyalty_accounts a ON a.id = l.account_id JOIN crm.customers c ON c.id = a.customer_id
		LEFT JOIN crm.loyalty_tiers ft ON ft.id = l.from_tier_id LEFT JOIN crm.loyalty_tiers qt ON qt.id = l.qualified_tier_id
		LEFT JOIN crm.loyalty_tiers tt ON tt.id = l.to_tier_id WHERE l.evaluation_id = $1 ORDER BY c.name, l.id`, rid))
	return LoyaltyTierEvaluationDetail{LoyaltyTierEvaluationRun: run, Lines: lines}, err
}

// tierThresholds loads the active tiers of a property with the thresholds
// in force (pending = the thresholds waiting for the next evaluation, for
// the preview of "re-evaluate now").
func tierThresholds(ctx context.Context, q dbtx.Querier, property uuid.UUID, pending bool) (map[uuid.UUID]TierThreshold, []TierThreshold, error) {
	cols := `eff_min_points, eff_min_spend::text, eff_qualify_mode, eff_membership_type_ids, eff_period_months`
	if pending {
		cols = `min_points, min_spend::text, qualify_mode, membership_type_ids, period_months`
	}
	rows, err := q.Query(ctx, `SELECT id, rank, `+cols+`, grace_months, downgrade_allowed FROM crm.loyalty_tiers WHERE property_id = $1
		AND status = 'active' AND archived_at IS NULL`, property)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := map[uuid.UUID]TierThreshold{}
	var list []TierThreshold
	for rows.Next() {
		var t TierThreshold
		var spend string
		var period *int
		var down bool
		if err := rows.Scan(&t.ID, &t.Rank, &t.MinPoints, &spend, &t.QualifyMode, &t.MembershipTypes, &period, &t.GraceMonths, &down); err != nil {
			return nil, nil, err
		}
		t.MinSpend, t.NoDowngrade = dec(spend), !down
		if period != nil {
			t.PeriodMonths = *period
		}
		byID[t.ID] = t
		list = append(list, t)
	}
	return byID, list, rows.Err()
}

// tierBasis is the qualifying points and eligible spend of an account in
// the window [from, to] of the property's calendar dates.
func tierBasis(ctx context.Context, q dbtx.Querier, property, account uuid.UUID, from, to time.Time) (int64, decimal.Decimal, error) {
	var pts int64
	var spend string
	err := q.QueryRow(ctx, `SELECT coalesce(sum(points) FILTER (WHERE kind = 'earned' OR (kind = 'reversed' AND points < 0)), 0),
		coalesce(sum(amount) FILTER (WHERE kind = 'earned'), 0)::text FROM crm.loyalty_ledger WHERE account_id = $1
		AND occurred_at >= $2 AND occurred_at < $3`, account, localStart(ctx, q, property, from),
		localStart(ctx, q, property, to.AddDate(0, 0, 1))).Scan(&pts, &spend)
	return pts, dec(spend), err
}

// customerMembershipTypes are the membership types a customer holds
// actively (membership read model).
func customerMembershipTypes(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT type_id::text FROM reporting.membership_lifecycle WHERE customer_id = $1 AND status = 'active'`, customer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// accountQualification evaluates the tiers for one account: the basis of
// every tier window (cached per window length) and the qualified tier.
// The returned basis is the one of the programme window.
func accountQualification(ctx context.Context, q dbtx.Querier, property, account, customer uuid.UUID, tiers []TierThreshold, defaultMonths int,
	today time.Time) (*TierThreshold, TierBasis, error) {
	var types []string
	if slices.ContainsFunc(tiers, func(t TierThreshold) bool { return len(t.MembershipTypes) > 0 }) {
		var err error
		if types, err = customerMembershipTypes(ctx, q, customer); err != nil {
			return nil, TierBasis{}, err
		}
	}
	if defaultMonths <= 0 {
		defaultMonths = 12
	}
	cache := map[int]TierBasis{}
	get := func(months int) (TierBasis, error) {
		if months <= 0 {
			months = defaultMonths
		}
		if b, ok := cache[months]; ok {
			return b, nil
		}
		pts, spend, err := tierBasis(ctx, q, property, account, today.AddDate(0, -months, 0), today)
		b := TierBasis{Points: pts, Spend: spend, MembershipTypes: types}
		cache[months] = b
		return b, err
	}
	base, err := get(defaultMonths)
	if err != nil {
		return nil, base, err
	}
	for _, t := range tiers {
		if _, err := get(t.PeriodMonths); err != nil {
			return nil, base, err
		}
	}
	qual := QualifyTierBy(tiers, func(t TierThreshold) TierBasis {
		m := t.PeriodMonths
		if m <= 0 {
			m = defaultMonths
		}
		return cache[m]
	})
	return qual, base, nil
}

// TierProgramOn reports whether the P5 tier programme runs the tier
// evaluations of a property.
func TierProgramOn(ctx context.Context, q dbtx.Querier, property uuid.UUID) (bool, error) {
	p, _, err := LoadTierProgram(ctx, q, property)
	return p.Enabled, err
}

// tierPlanItem is the decision for one account (preview or run).
type tierPlanItem struct {
	Account LoyaltyAccount
	Basis   TierBasis
	Qual    *TierThreshold
	D       TierDecision
}

// planTierEvaluation decides the tier of the active accounts of a property
// (or the given ones) without writing anything; lock locks the accounts
// for the run, pending uses the thresholds waiting for the next evaluation.
func (m *Module) planTierEvaluation(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, accountIDs []uuid.UUID, lock, pending bool) (
	[]tierPlanItem, TierProgramPolicy, rules.PolicyRef, error) {
	pol, ref, err := LoadTierProgram(ctx, tx, property)
	if err != nil {
		return nil, pol, ref, err
	}
	today := localToday(ctx, tx, property)
	byID, tiers, err := tierThresholds(ctx, tx, property, pending)
	if err != nil {
		return nil, pol, ref, err
	}
	sql := `SELECT id FROM crm.loyalty_accounts WHERE property_id = $1 AND status = 'active' AND (cardinality($2::uuid[]) = 0 OR id = ANY($2::uuid[]))`
	if kind == EvalGraceReview {
		sql += ` AND grace_until IS NOT NULL AND grace_until <= $3::date`
	} else {
		sql += ` AND $3::date IS NOT NULL`
	}
	ids := accountIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	rows, err := tx.Query(ctx, sql+` ORDER BY id`, property, ids, today)
	if err != nil {
		return nil, pol, ref, err
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, pol, ref, err
	}
	out := make([]tierPlanItem, 0, len(accounts))
	for _, aid := range accounts {
		var a LoyaltyAccount
		if lock {
			a, err = lockAccount(ctx, tx, aid)
		} else {
			rows, qerr := tx.Query(ctx, accountSelect+` WHERE a.id = $1`, aid)
			a, err = handle.One[LoyaltyAccount](rows, qerr, "loyalty account")
		}
		if err != nil {
			return nil, pol, ref, err
		}
		var grace *time.Time
		if err := tx.QueryRow(ctx, `SELECT grace_until FROM crm.loyalty_accounts WHERE id = $1`, aid).Scan(&grace); err != nil {
			return nil, pol, ref, err
		}
		var cur *TierThreshold
		if a.TierID != nil {
			if t, ok := byID[*a.TierID]; ok {
				cur = &t
			} else if a.TierRank != nil {
				cur = &TierThreshold{ID: *a.TierID, Rank: *a.TierRank} // inactive / archived tier keeps its rank
			}
		}
		qual, basis, err := accountQualification(ctx, tx, property, aid, a.CustomerID, tiers, pol.PeriodMonths, today)
		if err != nil {
			return nil, pol, ref, err
		}
		decideQual, graceMonths := qual, pol.GraceMonths
		if cur != nil {
			if cur.GraceMonths != nil {
				graceMonths = *cur.GraceMonths
			}
			if cur.NoDowngrade && (qual == nil || qual.Rank < cur.Rank) {
				decideQual = cur // "downgrade allowed" off: the member keeps the tier
			}
		}
		d := DecideTier(kind, cur, decideQual, grace, a.TierLocked, today, graceMonths)
		out = append(out, tierPlanItem{Account: a, Basis: basis, Qual: qual, D: d})
	}
	return out, pol, ref, nil
}

// RunTierEvaluation evaluates the active accounts of a property (or the
// given ones) and records the run with one line per account. Upgrades and
// downgrades go through changeTier (history, crm.tier_changed, member
// notification); every line publishes crm.tier_evaluated. A full annual or
// periodic run (or applyPendingVersions) first puts the pending threshold
// versions in force.
func (m *Module) RunTierEvaluation(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LoyaltyTierEvaluationInput) (LoyaltyTierEvaluationRun, error) {
	if !slices.Contains([]string{EvalAnnual, EvalPeriodic, EvalGraceReview}, in.Kind) {
		return LoyaltyTierEvaluationRun{}, handle.Invalid("kind", "invalid_kind", "kind must be annual, periodic or grace_review")
	}
	today := localToday(ctx, tx, property)
	rid := id.New()
	applied := []AppliedTierVersion{}
	if in.ApplyPendingVersions || (in.Kind != EvalGraceReview && len(in.AccountIDs) == 0) {
		var err error
		if applied, err = applyPendingVersions(ctx, tx, property, today); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
	}
	plan, pol, ref, err := m.planTierEvaluation(ctx, tx, property, in.Kind, in.AccountIDs, true, false)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	if len(in.AccountIDs) > 0 && len(plan) == 0 && in.Kind != EvalGraceReview {
		return LoyaltyTierEvaluationRun{}, handle.Invalid("accountIds", "not_found", "no active loyalty account of this property")
	}
	from := today.AddDate(0, -pol.PeriodMonths, 0)
	num, err := numbering.Next(ctx, tx, property, "TEV", today)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	rawApplied, _ := json.Marshal(applied)
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_evaluations (id, property_id, number, kind, evaluated_on, window_from, window_to, policy_version,
		created_by, applied_versions) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, rid, property, num, in.Kind, today, from, today, ref.Version, actor(ctx),
		rawApplied); err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	if len(applied) > 0 {
		vids := make([]uuid.UUID, 0, len(applied))
		for _, v := range applied {
			vids = append(vids, v.VersionID)
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_versions SET evaluation_id = $2 WHERE id = ANY($1)`, vids, rid); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
	}
	counts := map[string]int{}
	for _, it := range plan {
		a, d, pts, spend := it.Account, it.D, it.Basis.Points, it.Basis.Spend
		fromTier := it.Account.TierID
		if err := m.applyTierDecision(ctx, tx, &a, d, in.Kind, pts, spend, pol); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		counts[d.Outcome]++
		var qid *uuid.UUID
		if it.Qual != nil {
			qid = &it.Qual.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_evaluation_lines (id, property_id, evaluation_id, account_id, from_tier_id, qualified_tier_id,
			to_tier_id, outcome, spend_basis, points_basis, grace_until) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11)`, id.New(), property, rid, a.ID,
			fromTier, qid, d.To, d.Outcome, spend.String(), pts, d.GraceUntil); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		if m.Events != nil {
			pid := property
			if _, err := m.Events.Publish(ctx, tx, EventTierEvaluated, "crm.loyalty_account", &a.ID, &pid, map[string]any{"evaluationId": rid,
				"evaluationNumber": num, "kind": in.Kind, "accountId": a.ID, "customerId": a.CustomerID, "fromTierId": fromTier, "qualifiedTierId": qid,
				"toTierId": d.To, "outcome": d.Outcome, "spendBasis": spend.String(), "pointsBasis": pts, "graceUntil": dateStr(d.GraceUntil),
				"windowFrom": from.Format("2006-01-02"), "windowTo": today.Format("2006-01-02")}); err != nil {
				return LoyaltyTierEvaluationRun{}, err
			}
		}
	}
	retained := counts[OutcomeRetained] + counts[OutcomeLocked]
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_evaluations SET accounts = $2, upgraded = $3, downgraded = $4, retained = $5, grace_started = $6,
		in_grace = $7 WHERE id = $1`, rid, len(plan), counts[OutcomeUpgraded], counts[OutcomeDowngraded], retained, counts[OutcomeGraceStarted],
		counts[OutcomeInGrace]); err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	d, err := TierEvaluation(ctx, tx, property, rid)
	return d.LoyaltyTierEvaluationRun, err
}

func dateStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

// applyTierDecision stores the outcome of one account.
func (m *Module) applyTierDecision(ctx context.Context, tx pgx.Tx, a *LoyaltyAccount, d TierDecision, kind string, pts int64, spend decimal.Decimal, pol TierProgramPolicy) error {
	switch d.Outcome {
	case OutcomeUpgraded, OutcomeDowngraded:
		reason := map[string]string{EvalAnnual: "annual evaluation", EvalPeriodic: "periodic evaluation", EvalGraceReview: "grace period ended"}[kind]
		if d.Outcome == OutcomeDowngraded && kind != EvalGraceReview {
			reason += " (grace period ended)"
		}
		if err := m.changeTier(ctx, tx, a, d.To, reason, &pts, &spend); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET grace_until = NULL, grace_tier_id = NULL, tier_since = $2, tier_evaluated_at = now()
			WHERE id = $1`, a.ID, localToday(ctx, tx, a.PropertyID))
		return err
	case OutcomeGraceStarted:
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET grace_until = $2, tier_source = 'auto', tier_evaluated_at = now() WHERE id = $1`, a.ID, d.GraceUntil); err != nil {
			return err
		}
		if pol.NotifyGrace && a.TierName != nil {
			return m.notifyCustomer(ctx, tx, a.PropertyID, a.CustomerID, "crm.loyalty_tier_grace", map[string]any{"tier": *a.TierName,
				"date": d.GraceUntil.Format("2006-01-02")})
		}
		return nil
	case OutcomeRetained:
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET grace_until = NULL, grace_tier_id = NULL, tier_source = 'auto', tier_evaluated_at = now() WHERE id = $1`, a.ID)
		return err
	case OutcomeInGrace:
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_source = 'auto', tier_evaluated_at = now() WHERE id = $1`, a.ID)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_evaluated_at = now() WHERE id = $1`, a.ID)
	return err
}

// RunTierProgramDaily runs the annual evaluation on its date and the grace
// review every day for every property with the programme on.
func (m *Module) RunTierProgramDaily(ctx context.Context) error {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, m.DB)
	if err != nil {
		return err
	}
	for _, p := range props {
		if err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			pol, _, err := LoadTierProgram(ctx, tx, p)
			if err != nil || !pol.Enabled {
				return err
			}
			if pol.AnnualDue(localToday(ctx, tx, p)) {
				_, err = m.RunTierEvaluation(ctx, tx, p, LoyaltyTierEvaluationInput{Kind: EvalAnnual})
				return err
			}
			var due bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE property_id = $1 AND status = 'active'
				AND grace_until IS NOT NULL AND grace_until <= $2::date)`, p, localToday(ctx, tx, p)).Scan(&due); err != nil || !due {
				return err
			}
			_, err = m.RunTierEvaluation(ctx, tx, p, LoyaltyTierEvaluationInput{Kind: EvalGraceReview})
			return err
		}); err != nil {
			return fmt.Errorf("tier programme for %s: %w", p, err)
		}
	}
	return nil
}

// ── benefits (FR-LOY-P5-02) ───────────────────────────────────────────────

// LoyaltyTierBenefits are the tier benefits of a customer as pricing and
// booking read them.
type LoyaltyTierBenefits struct {
	CustomerID         uuid.UUID  `json:"customerId"`
	Enrolled           bool       `json:"enrolled"`
	AccountID          *uuid.UUID `json:"accountId"`
	TierID             *uuid.UUID `json:"tierId"`
	TierCode           *string    `json:"tierCode"`
	TierName           *string    `json:"tierName"`
	PointsMultiplier   string     `json:"pointsMultiplier"`
	BookingWindowDays  int        `json:"bookingWindowDays" doc:"Days added to the booking window"`
	FnbDiscountPercent string     `json:"fnbDiscountPercent"`
	EventAccess        bool       `json:"eventAccess" doc:"Invitation to VIP events"`
	PriorityService    bool       `json:"priorityService"`
	GraceUntil         *string    `json:"graceUntil" doc:"The tier is kept until this date (grace period)"`
	// Tier class badge and classification source (tier master, P5).
	TierRank      *int    `json:"tierRank"`
	TierColor     *string `json:"tierColor" doc:"Badge colour #RRGGBB"`
	TierIcon      *string `json:"tierIcon" doc:"Badge icon (Material Symbols name)"`
	TierSource    string  `json:"tierSource" enum:"auto,manual" doc:"auto: tier evaluation; manual: Set Tier or a classification override"`
	OverrideUntil *string `json:"overrideUntil" doc:"End of the manual classification override in force (null: none or open-ended)"`
	Overridden    bool    `json:"overridden" doc:"A manual classification override is in force"`
}

// BenefitsOf returns the tier benefits of a customer (none without an
// active account or tier).
func BenefitsOf(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (LoyaltyTierBenefits, error) {
	out := LoyaltyTierBenefits{CustomerID: customer, PointsMultiplier: "1", FnbDiscountPercent: "0", TierSource: "auto"}
	var aid uuid.UUID
	var tier *uuid.UUID
	var code, name *string
	var mult, disc *string
	var window *int
	var event, prio *bool
	var grace, until *time.Time
	var override *uuid.UUID
	err := q.QueryRow(ctx, `SELECT a.id, a.tier_id, t.code, t.name, trim_scale(t.multiplier)::text, t.booking_window_days, trim_scale(t.fnb_discount_percent)::text,
		t.event_access, t.priority_service, a.grace_until, t.rank, t.color, t.icon, a.tier_source, a.tier_override_id, o.valid_until
		FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id
		LEFT JOIN crm.loyalty_tier_overrides o ON o.id = a.tier_override_id AND o.status = 'active'
		WHERE a.customer_id = $1 AND a.status = 'active'`, customer).Scan(&aid, &tier, &code, &name, &mult, &window, &disc, &event, &prio, &grace,
		&out.TierRank, &out.TierColor, &out.TierIcon, &out.TierSource, &override, &until)
	if dbtx.IsNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enrolled, out.AccountID, out.TierID, out.TierCode, out.TierName, out.GraceUntil = true, &aid, tier, code, name, dateStr(grace)
	out.Overridden, out.OverrideUntil = override != nil, dateStr(until)
	if tier == nil {
		return out, nil
	}
	if mult != nil {
		out.PointsMultiplier = *mult
	}
	if disc != nil {
		out.FnbDiscountPercent = *disc
	}
	if window != nil {
		out.BookingWindowDays = *window
	}
	out.EventAccess = event != nil && *event
	out.PriorityService = prio != nil && *prio
	return out, nil
}

// BookingWindowBonus is the extra booking window days of a customer's tier
// (0 without an active account); the golf booking reads it through the hook
// wired by internal/app.
func BookingWindowBonus(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (int, error) {
	b, err := BenefitsOf(ctx, q, customer)
	return b.BookingWindowDays, err
}

// MaxBookingWindowBonus is the largest booking window bonus of the active
// tiers of a property (tee sheet generation horizon).
func MaxBookingWindowBonus(ctx context.Context, q dbtx.Querier, property uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT coalesce(max(booking_window_days), 0) FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active'
		AND archived_at IS NULL`, property).Scan(&n)
	return n, err
}

// requireAccount checks an account of the property.
func requireAccount(ctx context.Context, q dbtx.Querier, property, aid uuid.UUID) (LoyaltyAccount, error) {
	a, err := GetAccount(ctx, q, aid)
	if err != nil {
		return a, err
	}
	if a.PropertyID != property {
		return LoyaltyAccount{}, errs.NotFound("loyalty account")
	}
	return a, nil
}
