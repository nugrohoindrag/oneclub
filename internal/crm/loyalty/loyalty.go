// Package loyalty is the Loyalty Foundation of PRD P3 (EP-09), the P3-owned
// crm/loyalty sub-package (PRD P3 §5.4.1): loyalty accounts with opt-in,
// earning rules per business line / outlet / product, an append-only points
// ledger (earned, redeemed, expired, adjusted, reversed), redemption as a
// billing tender and for rewards, 12-month rolling expiry, the tier
// foundation and the points liability.
//
// CRM sits below billing (Technical Doc §4.2): earning follows the
// billing.payment_settled event and reads folios through reporting views;
// billing calls Redeem through the tender hook wired by internal/app.
package loyalty

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
)

// EventPointsChanged is published for every ledger entry (contract
// docs/p3-p4-contracts.md).
const (
	EventPointsChanged = "crm.loyalty_points_changed"
	EventTierChanged   = "crm.tier_changed"
)

// Ledger kinds (PRD P3 §7.6: Earned, Redeemed + NC Expired; Adjusted, Reversed).
const (
	KindEarned   = "earned"
	KindRedeemed = "redeemed"
	KindExpired  = "expired"
	KindAdjusted = "adjusted"
	KindReversed = "reversed"
)

// Module is the loyalty service.
type Module struct {
	DB        *dbtx.DB
	Events    crm.Publisher
	Approvals *approval.Engine
	Notify    notify.Sender
	// PayFolio takes a loyalty_points tender payment on a folio through
	// billing (wired by internal/app; billing calls back Redeem).
	PayFolio func(ctx context.Context, tx pgx.Tx, in FolioPayment) (LoyaltyFolioPayment, error)
	// IssueVoucher issues the Commercial vouchers of a voucher reward
	// (FR-LOY-05; wired by internal/app, crm cannot import commercial) and
	// returns their codes; idempotent per redemption.
	IssueVoucher func(ctx context.Context, tx pgx.Tx, in RewardVoucher) ([]string, error)
}

// RewardVoucher asks Commercial for the vouchers of a reward redemption.
type RewardVoucher struct {
	PropertyID   uuid.UUID
	TypeCode     string
	CustomerID   uuid.UUID
	Quantity     int
	RedemptionID uuid.UUID
	Number       string
	RewardName   string
}

// FolioPayment is a points payment on a folio (front desk, Member App).
type FolioPayment struct {
	PropertyID     uuid.UUID
	FolioID        uuid.UUID
	AccountID      uuid.UUID
	Amount         decimal.Decimal
	Points         int64
	IdempotencyKey string
}

// LoyaltyFolioPayment is the billing payment taken with points.
type LoyaltyFolioPayment struct {
	PaymentID uuid.UUID      `json:"paymentId"`
	Number    string         `json:"number"`
	Amount    string         `json:"amount"`
	Status    string         `json:"status"`
	TenderRef map[string]any `json:"tenderRef"`
}

// ── Loyalty Policies (FR-POL-P3-04) ───────────────────────────────────────

// Policy is the Loyalty Policies club policy.
type Policy struct {
	Enabled               bool     `json:"enabled"`
	AutoEnroll            bool     `json:"autoEnroll" doc:"Open an account at the first settled payment of a registered customer (default: opt-in only)"`
	AmountPerPoint        string   `json:"amountPerPoint" doc:"Net spend (before tax & service) per point"`
	RedemptionValue       string   `json:"redemptionValue" doc:"Value of one point when redeemed"`
	ExpiryMonths          int      `json:"expiryMonths" doc:"Validity of earned points (rolling)"`
	ExpiryNoticeDays      int      `json:"expiryNoticeDays" doc:"Notify the customer this many days before points expire"`
	EligibleLines         []string `json:"eligibleLines" doc:"Business lines that earn without a specific earning rule"`
	ExcludedComponents    []string `json:"excludedComponents" doc:"Revenue components that never earn (caddy fee, deposits, membership fee …)"`
	EarnOnVoucherPayments bool     `json:"earnOnVoucherPayments" doc:"Payments with Voucher & Prepaid also earn"`
	MinRedeemPoints       int64    `json:"minRedeemPoints"`
	MaxRedeemPointsPerDay int64    `json:"maxRedeemPointsPerDay" doc:"0 = no daily limit (FR-LOY-10)"`
	TierPeriodMonths      int      `json:"tierPeriodMonths" doc:"Evaluation window of tier thresholds"`
	TierDowngrade         bool     `json:"tierDowngrade" doc:"The periodic evaluation may lower a tier"`
	NotifyEarned          bool     `json:"notifyEarned" doc:"In-app message to portal users when points are earned"`
}

// DefaultPolicy follows PRD P3 §16 #7: 1 point per Rp10.000 net, 1 point =
// Rp100, 12 months; golf (not caddy fee), F&B, sport club and bungalow earn;
// membership fee, banquet / wedding and tournament fees do not.
var DefaultPolicy = Policy{Enabled: true, AmountPerPoint: "10000", RedemptionValue: "100", ExpiryMonths: 12, ExpiryNoticeDays: 30,
	EligibleLines: []string{"golf", "pos", "sportclub", "stay"},
	ExcludedComponents: []string{"caddy_fee", "caddy_tip", "caddy_fee_settlement", "instructor_fee", "hio_insurance", "deposit", "voucher_deferred",
		"breakage", "membership_fee", "membership_annual_fee", "card_replacement_fee", "reactivation_fee", "nominee_fee", "tournament_fee",
		"cancellation_fee", "damage_charge", "loyalty_redemption"},
	MinRedeemPoints: 1, TierPeriodMonths: 12, TierDowngrade: true, NotifyEarned: true}

// PolicyCode is the club policy code.
const PolicyCode = "crm.loyalty"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Loyalty Policies", Name: "Loyalty earning, redemption, expiry & tiers",
		Description: "Points per net spend, eligible lines and excluded components, redemption value and limits, expiry and notice, tier evaluation",
		Default:     DefaultPolicy})
	if !slices.Contains(rules.PolicyCategories, "Loyalty Policies") {
		rules.PolicyCategories = append(rules.PolicyCategories, "Loyalty Policies") // PRD P3 §7.6 proposed label
	}
}

// LoadPolicy returns the Loyalty Policies in force at a property.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (Policy, rules.PolicyRef, error) {
	return rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
}

func (p Policy) perPoint() decimal.Decimal {
	d, err := decimal.NewFromString(p.AmountPerPoint)
	if err != nil || !d.IsPositive() {
		return decimal.NewFromInt(10000)
	}
	return d
}

// Value is the redemption value of one point.
func (p Policy) Value() decimal.Decimal {
	d, err := decimal.NewFromString(p.RedemptionValue)
	if err != nil || !d.IsPositive() {
		return decimal.NewFromInt(100)
	}
	return d
}

func (p Policy) expiryMonths() int {
	if p.ExpiryMonths <= 0 {
		return 12
	}
	return p.ExpiryMonths
}

// ── helpers ───────────────────────────────────────────────────────────────

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func nullStr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// localToday is the calendar date of the property (UTC midnight).
func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	n := clock.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func randomCode(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + strings.ToUpper(hex.EncodeToString(b))
}

func currency(ctx context.Context, q dbtx.Querier) string {
	c, err := org.Currency(ctx, q)
	if err != nil || c == "" {
		return "IDR"
	}
	return c
}

// ── accounts ──────────────────────────────────────────────────────────────

// LoyaltyAccount is a loyalty account with its customer and tier.
type LoyaltyAccount struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	PropertyID      uuid.UUID  `json:"propertyId" db:"property_id"`
	Number          string     `json:"number" db:"number"`
	CustomerID      uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerCode    string     `json:"customerCode" db:"customer_code"`
	CustomerName    string     `json:"customerName" db:"customer_name"`
	TierID          *uuid.UUID `json:"tierId" db:"tier_id"`
	TierCode        *string    `json:"tierCode" db:"tier_code"`
	TierName        *string    `json:"tierName" db:"tier_name"`
	TierRank        *int       `json:"-" db:"tier_rank"`
	TierMultiplier  string     `json:"tierMultiplier" db:"tier_multiplier"`
	TierLocked      bool       `json:"tierLocked" db:"tier_locked"`
	Status          string     `json:"status" db:"status" enum:"active,inactive,suspended"`
	Balance         int64      `json:"balance" db:"balance"`
	LifetimePoints  int64      `json:"lifetimePoints" db:"lifetime_points"`
	EnrolledVia     string     `json:"enrolledVia" db:"enrolled_via" enum:"staff,member_app,auto,migration"`
	OptedInAt       time.Time  `json:"optedInAt" db:"opted_in_at"`
	TierEvaluatedAt *time.Time `json:"tierEvaluatedAt" db:"tier_evaluated_at"`
	StatusReason    *string    `json:"statusReason" db:"status_reason"`
	CreatedAt       time.Time  `json:"createdAt" db:"created_at"`
	// Computed with the Loyalty Policies in force.
	RedemptionValue string `json:"redemptionValue" db:"-" doc:"Value of one point"`
	BalanceValue    string `json:"balanceValue" db:"-" doc:"Balance × redemption value"`
	ExpiringPoints  int64  `json:"expiringPoints" db:"-" doc:"Points expiring within the notice window"`
	NextExpiry      string `json:"nextExpiry,omitempty" db:"-" doc:"Date the next points expire"`
}

const accountSelect = `SELECT a.id, a.property_id, a.number, a.customer_id, c.code AS customer_code, c.name AS customer_name, a.tier_id,
	t.code AS tier_code, t.name AS tier_name, t.rank AS tier_rank, trim_scale(coalesce(t.multiplier, 1))::text AS tier_multiplier, a.tier_locked,
	a.status, a.balance, a.lifetime_points, a.enrolled_via, a.opted_in_at, a.tier_evaluated_at, a.status_reason, a.created_at
	FROM crm.loyalty_accounts a JOIN crm.customers c ON c.id = a.customer_id LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id`

// GetAccount loads an account (with its computed values).
func GetAccount(ctx context.Context, q dbtx.Querier, aid uuid.UUID) (LoyaltyAccount, error) {
	rows, err := q.Query(ctx, accountSelect+` WHERE a.id = $1`, aid)
	a, err := handle.One[LoyaltyAccount](rows, err, "loyalty account")
	if err != nil {
		return a, err
	}
	return a, fill(ctx, q, &a)
}

// AccountByCustomer returns the account of a customer (nil when none).
func AccountByCustomer(ctx context.Context, q dbtx.Querier, customer uuid.UUID) (*LoyaltyAccount, error) {
	var aid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, customer).Scan(&aid)
	if dbtx.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := GetAccount(ctx, q, aid)
	return &a, err
}

func fill(ctx context.Context, q dbtx.Querier, a *LoyaltyAccount) error {
	pol, _, err := LoadPolicy(ctx, q, a.PropertyID)
	if err != nil {
		return err
	}
	v := pol.Value()
	a.RedemptionValue = v.String()
	a.BalanceValue = v.Mul(decimal.NewFromInt(max(a.Balance, 0))).String()
	days := pol.ExpiryNoticeDays
	if days <= 0 {
		days = 30
	}
	today := localToday(ctx, q, a.PropertyID)
	var next *time.Time
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(remaining) FILTER (WHERE expires_on <= $2::date + $3::int), 0), min(expires_on)
		FROM crm.loyalty_lots WHERE account_id = $1 AND remaining > 0`, a.ID, today, days).Scan(&a.ExpiringPoints, &next); err != nil {
		return err
	}
	if next != nil {
		a.NextExpiry = next.Format("2006-01-02")
	}
	return nil
}

// lockAccount locks the account row for a posting.
func lockAccount(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (LoyaltyAccount, error) {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT true FROM crm.loyalty_accounts WHERE id = $1 FOR UPDATE`, aid).Scan(&ok); err != nil {
		if dbtx.IsNoRows(err) {
			return LoyaltyAccount{}, errs.NotFound("loyalty account")
		}
		return LoyaltyAccount{}, err
	}
	rows, err := tx.Query(ctx, accountSelect+` WHERE a.id = $1`, aid)
	return handle.One[LoyaltyAccount](rows, err, "loyalty account")
}

// LoyaltyEnrolInput opens a loyalty account (opt-in).
type LoyaltyEnrolInput struct {
	CustomerID uuid.UUID  `json:"customerId"`
	TierID     *uuid.UUID `json:"tierId,omitempty" doc:"Default: the entry tier (lowest rank without thresholds)"`
}

// Enrol opens the account of a customer, or returns the existing one
// (reactivated when inactive). created reports a new account.
func (m *Module) Enrol(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, via string, tier *uuid.UUID) (LoyaltyAccount, bool, error) {
	c, err := crm.GetCustomer(ctx, tx, customer)
	if err != nil {
		return LoyaltyAccount{}, false, err
	}
	if c.PropertyID != property {
		return LoyaltyAccount{}, false, errs.NotFound("customer")
	}
	customer = c.ID // a merged profile resolves to its target
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, customer).Scan(&existing)
	if err == nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET status = 'active', status_reason = NULL, opted_in_at = CASE WHEN status = 'active'
			THEN opted_in_at ELSE now() END, updated_by = $2 WHERE id = $1`, existing, actor(ctx)); err != nil {
			return LoyaltyAccount{}, false, err
		}
		a, err := GetAccount(ctx, tx, existing)
		return a, false, err
	}
	if !dbtx.IsNoRows(err) {
		return LoyaltyAccount{}, false, err
	}
	if tier != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_tiers WHERE id = $1 AND property_id = $2 AND status = 'active'
			AND archived_at IS NULL)`, *tier, property).Scan(&ok); err != nil {
			return LoyaltyAccount{}, false, err
		}
		if !ok {
			return LoyaltyAccount{}, false, handle.Invalid("tierId", "not_found", "tier not found in this property")
		}
	} else {
		var t uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
			AND min_points = 0 AND min_spend = 0 ORDER BY rank, code LIMIT 1`, property).Scan(&t)
		if err == nil {
			tier = &t
		} else if !dbtx.IsNoRows(err) {
			return LoyaltyAccount{}, false, err
		}
	}
	num, err := numbering.Next(ctx, tx, property, "LOY", localToday(ctx, tx, property))
	if err != nil {
		return LoyaltyAccount{}, false, err
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_accounts (id, property_id, number, customer_id, tier_id, enrolled_via, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, aid, property, num, customer, tier, via, actor(ctx)); err != nil {
		return LoyaltyAccount{}, false, err
	}
	a, err := GetAccount(ctx, tx, aid)
	return a, true, err
}

// ── ledger ────────────────────────────────────────────────────────────────

// LoyaltyEntry is one points ledger entry.
type LoyaltyEntry struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	AccountID    uuid.UUID  `json:"accountId" db:"account_id"`
	Kind         string     `json:"kind" db:"kind" enum:"earned,redeemed,expired,adjusted,reversed"`
	Points       int64      `json:"points" db:"points" doc:"Signed: positive adds to the balance"`
	BalanceAfter int64      `json:"balanceAfter" db:"balance_after"`
	SourceType   string     `json:"sourceType" db:"source_type"`
	SourceID     *uuid.UUID `json:"sourceId" db:"source_id"`
	SourceRef    *string    `json:"sourceRef" db:"source_ref"`
	Amount       *string    `json:"amount" db:"amount" doc:"Eligible spend (earned) or tender value (redeemed)"`
	ExpiresOn    *string    `json:"expiresOn" db:"expires_on"`
	ReversesID   *uuid.UUID `json:"reversesId" db:"reverses_id"`
	Description  string     `json:"description" db:"description"`
	OccurredAt   time.Time  `json:"occurredAt" db:"occurred_at"`
}

const entrySelect = `SELECT id, account_id, kind, points, balance_after, source_type, source_id, source_ref, trim_scale(amount)::text AS amount,
	to_char(expires_on, 'YYYY-MM-DD') AS expires_on, reverses_id, description, occurred_at FROM crm.loyalty_ledger`

func entry(ctx context.Context, q dbtx.Querier, eid uuid.UUID) (LoyaltyEntry, error) {
	rows, err := q.Query(ctx, entrySelect+` WHERE id = $1`, eid)
	return handle.One[LoyaltyEntry](rows, err, "ledger entry")
}

// Ledger lists the entries of an account, newest first.
func Ledger(ctx context.Context, q dbtx.Querier, aid uuid.UUID, kind string, limit int) ([]LoyaltyEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	return handle.List[LoyaltyEntry](q.Query(ctx, entrySelect+` WHERE account_id = $1 AND ($2 = '' OR kind = $2) ORDER BY occurred_at DESC, id DESC LIMIT $3`,
		aid, kind, limit))
}

// posting is one ledger write.
type posting struct {
	Kind        string
	Points      int64
	SourceType  string
	SourceID    *uuid.UUID
	SourceRef   string
	Amount      *decimal.Decimal
	Key         string
	ReversesID  *uuid.UUID
	ExpiresOn   *time.Time // positive entries; default: today + policy expiry
	ConsumeLot  *uuid.UUID // negative entries: consume this lot first (expiry, reversal of an earning)
	Description string
}

// post writes a ledger entry on a locked account, keeps the validity lots
// and the balance, and publishes crm.loyalty_points_changed. An entry with
// the same idempotency key is returned unchanged (created = false).
func (m *Module) post(ctx context.Context, tx pgx.Tx, a *LoyaltyAccount, p posting) (LoyaltyEntry, bool, error) {
	if p.Key != "" {
		var eid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_ledger WHERE account_id = $1 AND idempotency_key = $2`, a.ID, p.Key).Scan(&eid)
		if err == nil {
			e, err := entry(ctx, tx, eid)
			return e, false, err
		}
		if !dbtx.IsNoRows(err) {
			return LoyaltyEntry{}, false, err
		}
	}
	if p.Points == 0 {
		return LoyaltyEntry{}, false, errs.Validation("invalid_points", "points must not be zero")
	}
	balance := a.Balance + p.Points
	var expires *time.Time
	if p.Points > 0 {
		if p.ExpiresOn != nil {
			expires = p.ExpiresOn
		} else {
			pol, _, err := LoadPolicy(ctx, tx, a.PropertyID)
			if err != nil {
				return LoyaltyEntry{}, false, err
			}
			e := localToday(ctx, tx, a.PropertyID).AddDate(0, pol.expiryMonths(), 0)
			expires = &e
		}
	}
	var amount *string
	var cur *string
	if p.Amount != nil {
		s := p.Amount.String()
		amount = &s
		c := currency(ctx, tx)
		cur = &c
	}
	eid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_ledger (id, property_id, account_id, kind, points, balance_after, source_type, source_id,
		source_ref, amount, currency, idempotency_key, reverses_id, expires_on, description, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,$12,$13,$14,$15,$16)`, eid, a.PropertyID, a.ID, p.Kind, p.Points, balance, p.SourceType,
		p.SourceID, nullStr(p.SourceRef), amount, cur, nullStr(p.Key), p.ReversesID, expires, p.Description, actor(ctx)); err != nil {
		return LoyaltyEntry{}, false, err
	}
	if p.Points > 0 {
		// Σ remaining of the lots stays = max(balance, 0): a debt (negative
		// balance after a reversal) is repaid first.
		remaining := min(p.Points, max(balance, 0))
		if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_lots (ledger_id, property_id, account_id, points, remaining, expires_on)
			VALUES ($1,$2,$3,$4,$5,$6)`, eid, a.PropertyID, a.ID, p.Points, remaining, expires); err != nil {
			return LoyaltyEntry{}, false, err
		}
	} else if err := consumeLots(ctx, tx, a.ID, -p.Points, p.ConsumeLot); err != nil {
		return LoyaltyEntry{}, false, err
	}
	lifetime := int64(0)
	switch {
	case p.Kind == KindEarned:
		lifetime = p.Points
	case p.Kind == KindReversed && p.Points < 0:
		lifetime = p.Points // an earning reversed
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET balance = $2, lifetime_points = lifetime_points + $3 WHERE id = $1`,
		a.ID, balance, lifetime); err != nil {
		return LoyaltyEntry{}, false, err
	}
	a.Balance = balance
	a.LifetimePoints += lifetime
	if m.Events != nil {
		pid := a.PropertyID
		pol, _, err := LoadPolicy(ctx, tx, a.PropertyID)
		if err != nil {
			return LoyaltyEntry{}, false, err
		}
		if _, err := m.Events.Publish(ctx, tx, EventPointsChanged, "crm.loyalty_account", &a.ID, &pid, map[string]any{"accountId": a.ID,
			"customerId": a.CustomerID, "kind": p.Kind, "points": p.Points, "balance": balance, "sourceType": p.SourceType, "sourceId": p.SourceID,
			"entryId": eid, "value": pol.Value().Mul(decimal.NewFromInt(p.Points)).String(), "currency": currency(ctx, tx)}); err != nil {
			return LoyaltyEntry{}, false, err
		}
	}
	e, err := entry(ctx, tx, eid)
	return e, true, err
}

// consumeLots takes points from the open lots, the given lot first and then
// the lots that expire first. Points beyond the open lots leave a debt.
func consumeLots(ctx context.Context, tx pgx.Tx, account uuid.UUID, points int64, first *uuid.UUID) error {
	type lot struct {
		id        uuid.UUID
		remaining int64
	}
	rows, err := tx.Query(ctx, `SELECT ledger_id, remaining FROM crm.loyalty_lots WHERE account_id = $1 AND remaining > 0
		ORDER BY (ledger_id = $2) DESC, expires_on, ledger_id FOR UPDATE`, account, first)
	if err != nil {
		return err
	}
	var lots []lot
	for rows.Next() {
		var l lot
		if err := rows.Scan(&l.id, &l.remaining); err != nil {
			rows.Close()
			return err
		}
		lots = append(lots, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range lots {
		if points <= 0 {
			break
		}
		take := min(points, l.remaining)
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_lots SET remaining = remaining - $2 WHERE ledger_id = $1`, l.id, take); err != nil {
			return err
		}
		points -= take
	}
	return nil
}

// notifyCustomer sends a loyalty message to the customer (portal user in-app
// and e-mail, or e-mail); unreachable customers are skipped.
func (m *Module) notifyCustomer(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, event string, data map[string]any, channels ...string) error {
	if m.Notify == nil {
		return nil
	}
	if data == nil {
		data = map[string]any{}
	}
	msg := notify.Message{Event: event, Category: "loyalty", PropertyID: &property, Data: data, Link: "/loyalty"}
	ok, err := crm.Recipient(ctx, tx, customer, &msg)
	if err != nil || !ok {
		return err
	}
	if len(channels) > 0 {
		if len(msg.UserIDs) == 0 {
			return nil // in-app only: the customer has no portal account
		}
		msg.Channels = channels
	}
	if c, err := crm.GetCustomer(ctx, tx, customer); err == nil {
		msg.Data["name"] = c.Name
	}
	return m.Notify.Send(ctx, tx, msg)
}
