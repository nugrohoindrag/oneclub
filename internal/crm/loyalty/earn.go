package loyalty

// Earning (FR-LOY-02, FR-LOY-04): points follow billing.payment_settled —
// never the order — once per payment; refunds reverse them; finished golf
// rounds give activity points. Folio lines are read through the reporting
// views (crm sits below billing). Event handlers skip silently whatever does
// not concern loyalty: they run for every payment of the club.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/outbox"
)

// SettledPayment is the part of billing.payment_settled loyalty uses.
type SettledPayment struct {
	PaymentID  uuid.UUID  `json:"paymentId"`
	Number     string     `json:"number"`
	FolioID    *uuid.UUID `json:"folioId"`
	Amount     string     `json:"amount"`
	MethodType string     `json:"methodType"`
	Purpose    string     `json:"purpose"`
}

// OnPaymentSettled earns points for a settled payment (outbox subscriber).
func (m *Module) OnPaymentSettled(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p SettledPayment
	if err := e.Decode(&p); err != nil {
		return nil // not a payment payload: nothing to earn
	}
	_, err := m.EarnFromPayment(ctx, tx, p)
	return err
}

type earnLine struct {
	BusinessLine     string     `db:"business_line"`
	RevenueComponent string     `db:"revenue_component"`
	Liability        bool       `db:"liability"`
	Net              string     `db:"net_amount"`
	Total            string     `db:"total"`
	OutletID         *uuid.UUID `db:"outlet_id"`
	ProductID        *uuid.UUID `db:"product_id"`
}

type earnRule struct {
	ID               uuid.UUID  `db:"id"`
	Code             string     `db:"code"`
	BusinessLine     *string    `db:"business_line"`
	RevenueComponent *string    `db:"revenue_component"`
	OutletID         *uuid.UUID `db:"outlet_id"`
	ProductID        *uuid.UUID `db:"product_id"`
	AmountPerPoint   *string    `db:"amount_per_point"`
	Multiplier       string     `db:"multiplier"`
	Activity         *string    `db:"activity"`
	Points           int64      `db:"points"`
	Priority         int        `db:"priority"`
	SegmentID        *uuid.UUID `db:"customer_segment_id"`
}

func (r earnRule) matches(l earnLine) (int, bool) {
	score := 0
	if r.BusinessLine != nil {
		if *r.BusinessLine != l.BusinessLine {
			return 0, false
		}
		score++
	}
	if r.RevenueComponent != nil {
		if *r.RevenueComponent != l.RevenueComponent {
			return 0, false
		}
		score += 2
	}
	if r.OutletID != nil {
		if l.OutletID == nil || *r.OutletID != *l.OutletID {
			return 0, false
		}
		score += 4
	}
	if r.ProductID != nil {
		if l.ProductID == nil || *r.ProductID != *l.ProductID {
			return 0, false
		}
		score += 8
	}
	return score, true
}

func activeRules(ctx context.Context, q dbtx.Querier, property uuid.UUID, ruleType string, day time.Time) ([]earnRule, error) {
	rows, err := q.Query(ctx, `SELECT id, code, business_line, revenue_component, outlet_id, product_id, trim_scale(amount_per_point)::text AS amount_per_point,
		trim_scale(multiplier)::text AS multiplier, activity, points, priority, customer_segment_id FROM crm.loyalty_earning_rules
		WHERE property_id = $1 AND rule_type = $2 AND status = 'active' AND archived_at IS NULL AND (rule_type = 'spend' OR customer_segment_id IS NULL)
		AND (valid_from IS NULL OR valid_from <= $3::date) AND (valid_to IS NULL OR valid_to >= $3::date) ORDER BY priority, code`, property, ruleType, day)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[earnRule])
}

// EarnResult reports an earning.
type EarnResult struct {
	Points   int64
	Eligible decimal.Decimal
	Entry    *LoyaltyEntry
}

// EarnFromPayment computes and posts the points of one settled payment:
// eligible net spend of the folio (business line / component / outlet /
// product rules) × the payment's share of the folio, ÷ amount per point,
// × rule, CRM segment and tier multipliers. Idempotent per payment.
func (m *Module) EarnFromPayment(ctx context.Context, tx pgx.Tx, p SettledPayment) (EarnResult, error) {
	var res EarnResult
	if p.FolioID == nil || (p.Purpose != "" && p.Purpose != "settlement") {
		return res, nil
	}
	switch p.MethodType {
	case "loyalty_points", "folio_transfer":
		return res, nil
	}
	var property uuid.UUID
	var customer *uuid.UUID
	var status, amountRaw string
	err := tx.QueryRow(ctx, `SELECT property_id, customer_id, status, amount::text FROM reporting.eng_payments WHERE payment_id = $1`, p.PaymentID).
		Scan(&property, &customer, &status, &amountRaw)
	if dbtx.IsNoRows(err) || (err == nil && (customer == nil || status == "pending" || status == "cancelled")) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return res, err
	}
	if !pol.Enabled || (p.MethodType == "voucher_prepaid" && !pol.EarnOnVoucherPayments) {
		return res, nil
	}
	var aid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1 AND status = 'active'`, *customer).Scan(&aid)
	if dbtx.IsNoRows(err) {
		if !pol.AutoEnroll {
			return res, nil
		}
		var registered bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.customers WHERE id = $1 AND status = 'active' AND erased_at IS NULL
			AND (email IS NOT NULL OR phone IS NOT NULL) AND NOT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE customer_id = $1))`, *customer).Scan(&registered); err != nil {
			return res, err
		}
		if !registered {
			return res, nil
		}
		a, _, err := m.Enrol(ctx, tx, property, *customer, "auto", nil)
		if err != nil {
			return res, err
		}
		aid = a.ID
	} else if err != nil {
		return res, err
	}
	lrows, err := tx.Query(ctx, `SELECT business_line, revenue_component, liability, net_amount::text, total::text, outlet_id, product_id
		FROM reporting.eng_folio_lines WHERE folio_id = $1`, *p.FolioID)
	if err != nil {
		return res, err
	}
	lines, err := pgx.CollectRows(lrows, pgx.RowToStructByNameLax[earnLine])
	if err != nil {
		return res, err
	}
	folioTotal := decimal.Zero
	for _, l := range lines {
		folioTotal = folioTotal.Add(dec(l.Total))
	}
	paid := dec(amountRaw)
	if !folioTotal.IsPositive() || !paid.IsPositive() {
		return res, nil
	}
	share := decimal.Min(decimal.NewFromInt(1), paid.Div(folioTotal))
	today := localToday(ctx, tx, property)
	rules, err := activeRules(ctx, tx, property, "spend", today)
	if err != nil {
		return res, err
	}
	// spend rules with a CRM segment are segment multipliers (FR-LOY-02):
	// the highest one matching the line applies on top of the line's rule
	// for the customers in the segment
	segs := map[uuid.UUID]bool{}
	var base, segRules []earnRule
	for _, r := range rules {
		if r.SegmentID != nil {
			segRules = append(segRules, r)
		} else {
			base = append(base, r)
		}
	}
	if len(segRules) > 0 {
		srows, err := tx.Query(ctx, `SELECT segment_id FROM crm.segment_members WHERE customer_id = $1`, *customer)
		if err != nil {
			return res, err
		}
		ids, err := pgx.CollectRows(srows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return res, err
		}
		for _, s := range ids {
			segs[s] = true
		}
	}
	rules = base
	per := pol.perPoint()
	raw := decimal.Zero
	for _, l := range lines {
		if l.Liability {
			continue
		}
		net := dec(l.Net).Mul(share)
		var best *earnRule
		bestScore := -1
		for i := range rules {
			if s, ok := rules[i].matches(l); ok && s > bestScore {
				best, bestScore = &rules[i], s
			}
		}
		rate, mult := per, decimal.NewFromInt(1)
		if best != nil {
			if best.AmountPerPoint != nil {
				rate = dec(*best.AmountPerPoint)
			}
			mult = dec(best.Multiplier)
		} else if !slices.Contains(pol.EligibleLines, l.BusinessLine) || slices.Contains(pol.ExcludedComponents, l.RevenueComponent) {
			continue
		}
		if !rate.IsPositive() || !mult.IsPositive() {
			continue
		}
		var segMult *decimal.Decimal
		for _, r := range segRules {
			if _, ok := r.matches(l); ok && segs[*r.SegmentID] {
				if x := dec(r.Multiplier); segMult == nil || x.GreaterThan(*segMult) {
					segMult = &x
				}
			}
		}
		if segMult != nil {
			mult = mult.Mul(*segMult)
		}
		if !mult.IsPositive() {
			continue
		}
		res.Eligible = res.Eligible.Add(net)
		raw = raw.Add(net.Div(rate).Mul(mult))
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return res, err
	}
	tierMult := dec(a.TierMultiplier)
	if !tierMult.IsPositive() {
		tierMult = decimal.NewFromInt(1)
	}
	points := raw.Mul(tierMult).Floor().IntPart()
	if points <= 0 {
		return res, nil
	}
	eligible := res.Eligible.Round(2)
	e, created, err := m.post(ctx, tx, &a, posting{Kind: KindEarned, Points: points, SourceType: "billing.payment", SourceID: &p.PaymentID,
		SourceRef: p.Number, Amount: &eligible, Key: "earn:" + p.PaymentID.String(), Description: "Points earned on payment " + p.Number})
	if err != nil {
		return res, err
	}
	res.Points, res.Entry = e.Points, &e
	if !created {
		return res, nil
	}
	if pol.NotifyEarned {
		if err := m.notifyCustomer(ctx, tx, property, a.CustomerID, "crm.loyalty_points_earned", map[string]any{"points": points,
			"balance": a.Balance, "reference": p.Number}, "in_app"); err != nil {
			return res, err
		}
	}
	// Tier upgrade right after earning (FR-LOY-07; downgrades only in the periodic evaluation).
	_, err = m.evaluateTier(ctx, tx, &a, pol, false, "points earned")
	return res, err
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// ── refunds (FR-LOY-04: a refund reverses the points) ────────────────────

// RefundPayload is the payload of billing.refund_processed.
type RefundPayload struct {
	RefundID  uuid.UUID `json:"refundId"`
	Number    string    `json:"number"`
	PaymentID uuid.UUID `json:"paymentId"`
	Amount    string    `json:"amount"`
}

// OnRefundProcessed reverses the points earned on a refunded payment, or
// gives back the points of a refunded loyalty_points payment.
func (m *Module) OnRefundProcessed(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p RefundPayload
	if err := e.Decode(&p); err != nil {
		return nil // not a refund payload
	}
	return m.ReverseRefund(ctx, tx, p)
}

// ReverseRefund applies one processed refund to the ledger.
func (m *Module) ReverseRefund(ctx context.Context, tx pgx.Tx, p RefundPayload) error {
	var method, amountRaw, refundedRaw string
	var rawRef []byte
	err := tx.QueryRow(ctx, `SELECT method_type, amount::text, refunded_amount::text, tender_ref FROM reporting.eng_payments WHERE payment_id = $1`,
		p.PaymentID).Scan(&method, &amountRaw, &refundedRaw, &rawRef)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	refund := dec(p.Amount)
	if !refund.IsPositive() {
		return nil
	}
	if method == "loyalty_points" {
		var ref map[string]any
		_ = json.Unmarshal(rawRef, &ref)
		raw, _ := ref["entryId"].(string)
		eid, err := uuid.Parse(raw)
		if err != nil {
			return nil // payment without a loyalty ledger reference
		}
		return m.giveBack(ctx, tx, eid, refund, "refund:"+p.RefundID.String(), "billing.refund", p.RefundID, p.Number)
	}
	var eid, aid uuid.UUID
	var points int64
	err = tx.QueryRow(ctx, `SELECT id, account_id, points FROM crm.loyalty_ledger WHERE source_type = 'billing.payment' AND source_id = $1 AND kind = 'earned'`,
		p.PaymentID).Scan(&eid, &aid, &points)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return err
	}
	var reversed int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(-sum(points), 0) FROM crm.loyalty_ledger WHERE reverses_id = $1`, eid).Scan(&reversed); err != nil {
		return err
	}
	open := points - reversed
	if open <= 0 {
		return nil
	}
	amount := dec(amountRaw)
	n := open
	if amount.IsPositive() && dec(refundedRaw).LessThan(amount) {
		n = min(open, decimal.NewFromInt(points).Mul(refund).Div(amount).Round(0).IntPart())
	}
	if n <= 0 {
		return nil
	}
	_, _, err = m.post(ctx, tx, &a, posting{Kind: KindReversed, Points: -n, SourceType: "billing.refund", SourceID: &p.RefundID, SourceRef: p.Number,
		Key: "refund:" + p.RefundID.String(), ReversesID: &eid, ConsumeLot: &eid, Description: "Points reversed: refund " + p.Number})
	return err
}

// giveBack re-credits redeemed points (refund of a points payment, cancelled
// reward) proportionally to the value given back.
func (m *Module) giveBack(ctx context.Context, tx pgx.Tx, redeemedID uuid.UUID, value decimal.Decimal, key, sourceType string, sourceID uuid.UUID, ref string) error {
	var aid uuid.UUID
	var points int64
	var amountRaw *string
	err := tx.QueryRow(ctx, `SELECT account_id, points, amount::text FROM crm.loyalty_ledger WHERE id = $1 AND kind = 'redeemed'`, redeemedID).
		Scan(&aid, &points, &amountRaw)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return err
	}
	var back int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(points), 0) FROM crm.loyalty_ledger WHERE reverses_id = $1`, redeemedID).Scan(&back); err != nil {
		return err
	}
	open := -points - back
	if open <= 0 {
		return nil
	}
	n := open
	if amountRaw != nil && dec(*amountRaw).IsPositive() && value.IsPositive() {
		n = min(open, decimal.NewFromInt(-points).Mul(value).Div(dec(*amountRaw)).Round(0).IntPart())
	}
	if n <= 0 {
		return nil
	}
	_, _, err = m.post(ctx, tx, &a, posting{Kind: KindReversed, Points: n, SourceType: sourceType, SourceID: &sourceID, SourceRef: ref, Key: key,
		ReversesID: &redeemedID, Description: "Points given back: " + ref})
	return err
}

// ── activity points (FR-LOY-02: finished round) ───────────────────────────

// OnRoundFinished gives the activity points of the round_finished rules to
// the players of a finished flight who hold an account.
func (m *Module) OnRoundFinished(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		FlightID uuid.UUID `json:"flightId"`
	}
	if err := e.Decode(&p); err != nil || p.FlightID == uuid.Nil || e.PropertyID == nil {
		return nil // not a flight payload
	}
	property := *e.PropertyID
	rules, err := activeRules(ctx, tx, property, "activity", localToday(ctx, tx, property))
	if err != nil {
		return err
	}
	var points int64
	for _, r := range rules {
		if r.Activity != nil && *r.Activity == "round_finished" {
			points += r.Points
		}
	}
	if points <= 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.id FROM reporting.golf_rounds g JOIN crm.loyalty_accounts a ON a.customer_id = g.customer_id
		WHERE g.flight_id = $1 AND a.status = 'active'`, p.FlightID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, aid := range ids {
		a, err := lockAccount(ctx, tx, aid)
		if err != nil {
			return err
		}
		if _, _, err := m.post(ctx, tx, &a, posting{Kind: KindEarned, Points: points, SourceType: "golf.flight", SourceID: &p.FlightID,
			Key: "activity:round_finished:" + p.FlightID.String(), Description: "Activity points: round finished"}); err != nil {
			return err
		}
	}
	return nil
}

// ── tiers (FR-LOY-07) ─────────────────────────────────────────────────────

// evaluateTier moves an account to the highest tier whose thresholds in
// force (effective version, qualify mode, membership types, window of the
// tier) are met by the points earned and the eligible spend.
func (m *Module) evaluateTier(ctx context.Context, tx pgx.Tx, a *LoyaltyAccount, pol Policy, downgrade bool, reason string) (bool, error) {
	if a.TierLocked {
		return false, nil
	}
	months := pol.TierPeriodMonths
	if months <= 0 {
		months = 12
	}
	_, tiers, err := tierThresholds(ctx, tx, a.PropertyID, false)
	if err != nil || len(tiers) == 0 {
		return false, err
	}
	target, basis, err := accountQualification(ctx, tx, a.PropertyID, a.ID, a.CustomerID, tiers, months, localToday(ctx, tx, a.PropertyID))
	if err != nil {
		return false, err
	}
	if target == nil || (a.TierID != nil && *a.TierID == target.ID) {
		return false, nil
	}
	if a.TierRank != nil && target.Rank < *a.TierRank && !downgrade {
		return false, nil
	}
	pts, spend := basis.Points, basis.Spend
	return true, m.changeTier(ctx, tx, a, &target.ID, reason, &pts, &spend)
}

// changeTier moves an account to a tier (history, crm.tier_changed, member
// notification); a reason starting with "manual" is a manual classification.
func (m *Module) changeTier(ctx context.Context, tx pgx.Tx, a *LoyaltyAccount, to *uuid.UUID, reason string, pts *int64, spend *decimal.Decimal) error {
	src := TierSourceAuto
	if strings.HasPrefix(reason, "manual") {
		src = TierSourceManual
	}
	return m.changeTierFrom(ctx, tx, a, to, reason, pts, spend, src, nil)
}

// Tier sources of an account.
const (
	TierSourceAuto   = "auto"
	TierSourceManual = "manual"
)

func (m *Module) changeTierFrom(ctx context.Context, tx pgx.Tx, a *LoyaltyAccount, to *uuid.UUID, reason string, pts *int64, spend *decimal.Decimal,
	src string, override *uuid.UUID) error {
	from := a.TierID
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_id = $2, tier_source = $3, tier_evaluated_at = now() WHERE id = $1`, a.ID, to, src); err != nil {
		return err
	}
	var spendStr *string
	if spend != nil {
		s := spend.String()
		spendStr = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_history (id, property_id, account_id, from_tier_id, to_tier_id, reason, points_basis,
		spend_basis, created_by, source, override_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11)`, id.New(), a.PropertyID, a.ID, from, to, reason,
		pts, spendStr, actor(ctx), src, override); err != nil {
		return err
	}
	var toName string
	if to != nil {
		_ = tx.QueryRow(ctx, `SELECT name FROM crm.loyalty_tiers WHERE id = $1`, *to).Scan(&toName)
	}
	pid := a.PropertyID
	if m.Events != nil {
		if _, err := m.Events.Publish(ctx, tx, EventTierChanged, "crm.loyalty_account", &a.ID, &pid, map[string]any{"accountId": a.ID,
			"customerId": a.CustomerID, "fromTierId": from, "toTierId": to, "tier": toName, "reason": reason, "source": src, "overrideId": override}); err != nil {
			return err
		}
	}
	a.TierID = to
	if toName != "" {
		if err := m.notifyCustomer(ctx, tx, pid, a.CustomerID, "crm.loyalty_tier_changed", map[string]any{"tier": toName}); err != nil {
			return err
		}
	}
	return nil
}

// EvaluateTiers runs the periodic tier evaluation of a property (with
// downgrades when the Loyalty Policies allow them).
func (m *Module) EvaluateTiers(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	// PRD P5 tier programme: the periodic run upgrades and ends grace periods
	// (downgrades only after the grace of the annual evaluation).
	if on, err := TierProgramOn(ctx, tx, property); err != nil || on {
		run, err := m.RunTierEvaluation(ctx, tx, property, LoyaltyTierEvaluationInput{Kind: EvalPeriodic})
		return run.Upgraded + run.Downgraded, err
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM crm.loyalty_accounts WHERE property_id = $1 AND status = 'active' AND NOT tier_locked ORDER BY id`, property)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, aid := range ids {
		a, err := lockAccount(ctx, tx, aid)
		if err != nil {
			return changed, err
		}
		ok, err := m.evaluateTier(ctx, tx, &a, pol, pol.TierDowngrade, "periodic evaluation")
		if err != nil {
			return changed, err
		}
		if ok {
			changed++
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_evaluated_at = now() WHERE id = $1`, aid); err != nil {
			return changed, err
		}
	}
	return changed, nil
}
