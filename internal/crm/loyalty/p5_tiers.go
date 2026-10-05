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

// TierThreshold is a tier as the engine sees it.
type TierThreshold struct {
	ID        uuid.UUID
	Rank      int
	MinPoints int64
	MinSpend  decimal.Decimal
}

// QualifyTier returns the highest-ranked tier whose thresholds the points
// and spend of the window meet (nil when none).
func QualifyTier(tiers []TierThreshold, points int64, spend decimal.Decimal) *TierThreshold {
	var best *TierThreshold
	for i := range tiers {
		t := tiers[i]
		if points >= t.MinPoints && spend.GreaterThanOrEqual(t.MinSpend) && (best == nil || t.Rank > best.Rank) {
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
}

const evalRunSelect = `SELECT id, number, kind, to_char(evaluated_on, 'YYYY-MM-DD') AS evaluated_on, to_char(window_from, 'YYYY-MM-DD') AS window_from,
	to_char(window_to, 'YYYY-MM-DD') AS window_to, accounts, upgraded, downgraded, retained, grace_started, in_grace, policy_version, created_at
	FROM crm.loyalty_tier_evaluations`

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

func tierThresholds(ctx context.Context, q dbtx.Querier, property uuid.UUID) (map[uuid.UUID]TierThreshold, []TierThreshold, error) {
	rows, err := q.Query(ctx, `SELECT id, rank, min_points, min_spend::text FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active'
		AND archived_at IS NULL`, property)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := map[uuid.UUID]TierThreshold{}
	var list []TierThreshold
	for rows.Next() {
		var t TierThreshold
		var spend string
		if err := rows.Scan(&t.ID, &t.Rank, &t.MinPoints, &spend); err != nil {
			return nil, nil, err
		}
		t.MinSpend = dec(spend)
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

// TierProgramOn reports whether the P5 tier programme runs the tier
// evaluations of a property.
func TierProgramOn(ctx context.Context, q dbtx.Querier, property uuid.UUID) (bool, error) {
	p, _, err := LoadTierProgram(ctx, q, property)
	return p.Enabled, err
}

// RunTierEvaluation evaluates the active accounts of a property (or the
// given ones) and records the run with one line per account. Upgrades and
// downgrades go through changeTier (history, crm.tier_changed, member
// notification); every line publishes crm.tier_evaluated.
func (m *Module) RunTierEvaluation(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LoyaltyTierEvaluationInput) (LoyaltyTierEvaluationRun, error) {
	if !slices.Contains([]string{EvalAnnual, EvalPeriodic, EvalGraceReview}, in.Kind) {
		return LoyaltyTierEvaluationRun{}, handle.Invalid("kind", "invalid_kind", "kind must be annual, periodic or grace_review")
	}
	pol, ref, err := LoadTierProgram(ctx, tx, property)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	today := localToday(ctx, tx, property)
	from := today.AddDate(0, -pol.PeriodMonths, 0)
	byID, tiers, err := tierThresholds(ctx, tx, property)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	sql := `SELECT id FROM crm.loyalty_accounts WHERE property_id = $1 AND status = 'active' AND (cardinality($2::uuid[]) = 0 OR id = ANY($2::uuid[]))`
	if in.Kind == EvalGraceReview {
		sql += ` AND grace_until IS NOT NULL AND grace_until <= $3::date`
	} else {
		sql += ` AND $3::date IS NOT NULL`
	}
	ids := in.AccountIDs
	if ids == nil {
		ids = []uuid.UUID{}
	}
	rows, err := tx.Query(ctx, sql+` ORDER BY id`, property, ids, today)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	if len(in.AccountIDs) > 0 && len(accounts) == 0 && in.Kind != EvalGraceReview {
		return LoyaltyTierEvaluationRun{}, handle.Invalid("accountIds", "not_found", "no active loyalty account of this property")
	}
	num, err := numbering.Next(ctx, tx, property, "TEV", today)
	if err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_evaluations (id, property_id, number, kind, evaluated_on, window_from, window_to, policy_version,
		created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, rid, property, num, in.Kind, today, from, today, ref.Version, actor(ctx)); err != nil {
		return LoyaltyTierEvaluationRun{}, err
	}
	counts := map[string]int{}
	for _, aid := range accounts {
		a, err := lockAccount(ctx, tx, aid)
		if err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		var grace *time.Time
		if err := tx.QueryRow(ctx, `SELECT grace_until FROM crm.loyalty_accounts WHERE id = $1`, aid).Scan(&grace); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		pts, spend, err := tierBasis(ctx, tx, property, aid, from, today)
		if err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		var cur *TierThreshold
		if a.TierID != nil {
			if t, ok := byID[*a.TierID]; ok {
				cur = &t
			} else if a.TierRank != nil {
				cur = &TierThreshold{ID: *a.TierID, Rank: *a.TierRank} // inactive / archived tier keeps its rank
			}
		}
		qual := QualifyTier(tiers, pts, spend)
		d := DecideTier(in.Kind, cur, qual, grace, a.TierLocked, today, pol.GraceMonths)
		if err := m.applyTierDecision(ctx, tx, &a, d, in.Kind, pts, spend, pol); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		counts[d.Outcome]++
		var qid *uuid.UUID
		if qual != nil {
			qid = &qual.ID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_evaluation_lines (id, property_id, evaluation_id, account_id, from_tier_id, qualified_tier_id,
			to_tier_id, outcome, spend_basis, points_basis, grace_until) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11)`, id.New(), property, rid, aid,
			a.TierID, qid, d.To, d.Outcome, spend.String(), pts, d.GraceUntil); err != nil {
			return LoyaltyTierEvaluationRun{}, err
		}
		if m.Events != nil {
			pid := property
			if _, err := m.Events.Publish(ctx, tx, EventTierEvaluated, "crm.loyalty_account", &a.ID, &pid, map[string]any{"evaluationId": rid,
				"evaluationNumber": num, "kind": in.Kind, "accountId": a.ID, "customerId": a.CustomerID, "fromTierId": a.TierID, "qualifiedTierId": qid,
				"toTierId": d.To, "outcome": d.Outcome, "spendBasis": spend.String(), "pointsBasis": pts, "graceUntil": dateStr(d.GraceUntil),
				"windowFrom": from.Format("2006-01-02"), "windowTo": today.Format("2006-01-02")}); err != nil {
				return LoyaltyTierEvaluationRun{}, err
			}
		}
	}
	retained := counts[OutcomeRetained] + counts[OutcomeLocked]
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_evaluations SET accounts = $2, upgraded = $3, downgraded = $4, retained = $5, grace_started = $6,
		in_grace = $7 WHERE id = $1`, rid, len(accounts), counts[OutcomeUpgraded], counts[OutcomeDowngraded], retained, counts[OutcomeGraceStarted],
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
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET grace_until = $2, tier_evaluated_at = now() WHERE id = $1`, a.ID, d.GraceUntil); err != nil {
			return err
		}
		if pol.NotifyGrace && a.TierName != nil {
			return m.notifyCustomer(ctx, tx, a.PropertyID, a.CustomerID, "crm.loyalty_tier_grace", map[string]any{"tier": *a.TierName,
				"date": d.GraceUntil.Format("2006-01-02")})
		}
		return nil
	case OutcomeRetained:
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET grace_until = NULL, grace_tier_id = NULL, tier_evaluated_at = now() WHERE id = $1`, a.ID)
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
}

// BenefitsOf returns the tier benefits of a customer (none without an
// active account or tier).
func BenefitsOf(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (LoyaltyTierBenefits, error) {
	out := LoyaltyTierBenefits{CustomerID: customer, PointsMultiplier: "1", FnbDiscountPercent: "0"}
	var aid uuid.UUID
	var tier *uuid.UUID
	var code, name *string
	var mult, disc *string
	var window *int
	var event, prio *bool
	var grace *time.Time
	err := q.QueryRow(ctx, `SELECT a.id, a.tier_id, t.code, t.name, trim_scale(t.multiplier)::text, t.booking_window_days, trim_scale(t.fnb_discount_percent)::text,
		t.event_access, t.priority_service, a.grace_until FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id
		WHERE a.customer_id = $1 AND a.status = 'active'`, customer).Scan(&aid, &tier, &code, &name, &mult, &window, &disc, &event, &prio, &grace)
	if dbtx.IsNoRows(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Enrolled, out.AccountID, out.TierID, out.TierCode, out.TierName, out.GraceUntil = true, &aid, tier, code, name, dateStr(grace)
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
