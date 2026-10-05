package loyalty

// Redemption (FR-LOY-05, FR-LOY-10): points as a billing tender (POS, front
// desk, Member App) at the redemption value of the Loyalty Policies, and for
// rewards; manual adjustments through approval (FR-LOY-09); expiry with
// notice (FR-LOY-06) and the liability (FR-LOY-08).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

// ── tender ────────────────────────────────────────────────────────────────

// RedeemRequest is a points tender on a folio (billing.TenderRequest without
// the billing types: internal/app adapts it).
type RedeemRequest struct {
	PropertyID     uuid.UUID
	FolioID        uuid.UUID
	CustomerID     *uuid.UUID // the folio's customer
	Amount         decimal.Decimal
	Data           map[string]any // {accountId | customerId, points}
	IdempotencyKey string
}

// RedeemResult is what the tender covered.
type RedeemResult struct {
	Amount  decimal.Decimal
	Points  int64
	Account LoyaltyAccount
	EntryID uuid.UUID
	Ref     map[string]any
}

func dataUUID(d map[string]any, key string) (*uuid.UUID, error) {
	raw, ok := d[key]
	if !ok || raw == nil {
		return nil, nil
	}
	s, _ := raw.(string)
	u, err := uuid.Parse(s)
	if err != nil {
		return nil, handle.Invalid("tender."+key, "invalid_id", key+" must be an id")
	}
	return &u, nil
}

func dataInt(d map[string]any, key string) (int64, error) {
	switch v := d[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case string:
		if v == "" {
			return 0, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, handle.Invalid("tender."+key, "invalid_points", key+" must be a whole number")
		}
		return n, nil
	default:
		if s := fmt.Sprint(v); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err == nil {
				return n, nil
			}
		}
		return 0, handle.Invalid("tender."+key, "invalid_points", key+" must be a whole number")
	}
}

// accountFor finds the redeeming account: the given account or customer, else
// the folio's customer.
func accountFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, d map[string]any, folioCustomer *uuid.UUID) (uuid.UUID, error) {
	aid, err := dataUUID(d, "accountId")
	if err != nil {
		return uuid.Nil, err
	}
	if aid == nil {
		cust, err := dataUUID(d, "customerId")
		if err != nil {
			return uuid.Nil, err
		}
		if cust == nil {
			cust = folioCustomer
		}
		if cust == nil {
			return uuid.Nil, errs.Validation("loyalty_account_required", "choose the loyalty account (the folio has no customer)",
				errs.Field("tender.accountId", "required", "loyalty account or customer is required"))
		}
		var a uuid.UUID
		if err := q.QueryRow(ctx, `SELECT id FROM crm.loyalty_accounts WHERE customer_id = $1`, *cust).Scan(&a); err != nil {
			if dbtx.IsNoRows(err) {
				return uuid.Nil, errs.Conflict("no_loyalty_account", "the customer has no loyalty account")
			}
			return uuid.Nil, err
		}
		aid = &a
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE id = $1 AND property_id = $2)`, *aid, property).Scan(&ok); err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, errs.NotFound("loyalty account")
	}
	return *aid, nil
}

// checkRedeem enforces the account status, balance, minimum and daily limit.
func checkRedeem(ctx context.Context, q dbtx.Querier, a LoyaltyAccount, pol Policy, points int64) error {
	if !pol.Enabled {
		return errs.Unavailable("loyalty is disabled in the Loyalty Policies")
	}
	if a.Status != "active" {
		return errs.Conflict("loyalty_account_inactive", "loyalty account "+a.Number+" is "+a.Status)
	}
	if points < max(pol.MinRedeemPoints, 1) {
		return errs.Validation("below_minimum", fmt.Sprintf("at least %d points must be redeemed", max(pol.MinRedeemPoints, 1)),
			errs.Field("points", "too_small", "below the minimum redemption"))
	}
	if points > a.Balance {
		return errs.Conflict("insufficient_points", fmt.Sprintf("loyalty account %s has %d points; %d needed", a.Number, a.Balance, points))
	}
	if pol.MaxRedeemPointsPerDay > 0 {
		var today int64
		if err := q.QueryRow(ctx, `SELECT coalesce(-sum(points), 0) FROM crm.loyalty_ledger WHERE account_id = $1 AND kind = 'redeemed'
			AND occurred_at >= now() - interval '1 day'`, a.ID).Scan(&today); err != nil {
			return err
		}
		if today+points > pol.MaxRedeemPointsPerDay {
			return errs.Conflict("daily_limit", fmt.Sprintf("the daily redemption limit is %d points (%d already redeemed)", pol.MaxRedeemPointsPerDay, today))
		}
	}
	return nil
}

// Redeem pays (part of) a folio with points: amount ÷ redemption value,
// rounded down to whole points (the tender may cover less than asked).
// Retries with the same idempotency key return the first redemption.
func (m *Module) Redeem(ctx context.Context, tx pgx.Tx, r RedeemRequest) (RedeemResult, error) {
	aid, err := accountFor(ctx, tx, r.PropertyID, r.Data, r.CustomerID)
	if err != nil {
		return RedeemResult{}, err
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return RedeemResult{}, err
	}
	pol, _, err := LoadPolicy(ctx, tx, r.PropertyID)
	if err != nil {
		return RedeemResult{}, err
	}
	value := pol.Value()
	key := ""
	if r.IdempotencyKey != "" {
		key = "tender:" + r.IdempotencyKey
		var eid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_ledger WHERE account_id = $1 AND idempotency_key = $2`, a.ID, key).Scan(&eid); err == nil {
			e, err := entry(ctx, tx, eid)
			if err != nil {
				return RedeemResult{}, err
			}
			return redeemResult(a, e, value), nil
		} else if !dbtx.IsNoRows(err) {
			return RedeemResult{}, err
		}
	}
	points, err := dataInt(r.Data, "points")
	if err != nil {
		return RedeemResult{}, err
	}
	maxPoints := r.Amount.Div(value).Floor().IntPart()
	if points <= 0 || points > maxPoints {
		points = maxPoints
	}
	if points <= 0 {
		return RedeemResult{}, errs.Validation("amount_below_point_value", "the amount is below the value of one point ("+value.String()+")",
			errs.Field("amount", "too_small", "at least the value of one point"))
	}
	if err := checkRedeem(ctx, tx, a, pol, points); err != nil {
		return RedeemResult{}, err
	}
	amount := value.Mul(decimal.NewFromInt(points))
	var folioNo string
	if err := tx.QueryRow(ctx, `SELECT number FROM reporting.eng_folios WHERE folio_id = $1`, r.FolioID).Scan(&folioNo); err != nil && !dbtx.IsNoRows(err) {
		return RedeemResult{}, err
	}
	e, _, err := m.post(ctx, tx, &a, posting{Kind: KindRedeemed, Points: -points, SourceType: "billing.folio", SourceID: &r.FolioID, SourceRef: folioNo,
		Amount: &amount, Key: key, Description: "Points redeemed as payment on folio " + folioNo})
	if err != nil {
		return RedeemResult{}, err
	}
	return redeemResult(a, e, value), nil
}

func redeemResult(a LoyaltyAccount, e LoyaltyEntry, value decimal.Decimal) RedeemResult {
	points := -e.Points
	amount := value.Mul(decimal.NewFromInt(points))
	if e.Amount != nil {
		amount = dec(*e.Amount)
	}
	return RedeemResult{Amount: amount, Points: points, Account: a, EntryID: e.ID, Ref: map[string]any{"accountId": a.ID.String(),
		"accountNumber": a.Number, "customerId": a.CustomerID.String(), "points": points, "pointValue": value.String(), "balance": e.BalanceAfter,
		"entryId": e.ID.String()}}
}

// LoyaltyRedeemInput pays a folio with points (front desk, Member App).
type LoyaltyRedeemInput struct {
	FolioID uuid.UUID `json:"folioId"`
	Points  int64     `json:"points,omitempty" doc:"Default: as many as the folio balance needs"`
	Amount  string    `json:"amount,omitempty" doc:"Amount to pay with points (default: the folio balance)"`
}

// LoyaltyRedeemResult is the payment taken with points.
type LoyaltyRedeemResult struct {
	Payment LoyaltyFolioPayment `json:"payment"`
	Account LoyaltyAccount      `json:"account"`
}

// RedeemOnFolio takes a loyalty_points payment on an open folio through
// billing (which calls back Redeem in the same transaction).
func (m *Module) RedeemOnFolio(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in LoyaltyRedeemInput, key string, customer *uuid.UUID) (LoyaltyRedeemResult, error) {
	if m.PayFolio == nil {
		return LoyaltyRedeemResult{}, errs.Unavailable("the loyalty_points tender is not wired")
	}
	var status string
	var folioCustomer *uuid.UUID
	var charges, paid string
	err := tx.QueryRow(ctx, `SELECT status, customer_id, charges::text, paid::text FROM reporting.eng_folios WHERE folio_id = $1 AND property_id = $2`,
		in.FolioID, property).Scan(&status, &folioCustomer, &charges, &paid)
	if dbtx.IsNoRows(err) {
		return LoyaltyRedeemResult{}, errs.NotFound("folio")
	}
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	if customer != nil && (folioCustomer == nil || *folioCustomer != *customer) {
		return LoyaltyRedeemResult{}, errs.NotFound("folio")
	}
	if status != "open" {
		return LoyaltyRedeemResult{}, errs.Conflict("folio_closed", "the folio is "+status)
	}
	a, err := GetAccount(ctx, tx, aid)
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	if a.PropertyID != property {
		return LoyaltyRedeemResult{}, errs.NotFound("loyalty account")
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	balance := dec(charges).Sub(dec(paid))
	amount, err := handle.Decimal("amount", in.Amount, balance)
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	if in.Points > 0 {
		amount = decimal.Min(balance, pol.Value().Mul(decimal.NewFromInt(in.Points)))
	}
	if amount.GreaterThan(balance) {
		amount = balance
	}
	if !amount.IsPositive() {
		return LoyaltyRedeemResult{}, errs.Conflict("nothing_to_pay", "the folio has no balance to pay")
	}
	if key == "" {
		key = id.New().String()
	}
	pay, err := m.PayFolio(ctx, tx, FolioPayment{PropertyID: property, FolioID: in.FolioID, AccountID: aid, Amount: amount, Points: in.Points,
		IdempotencyKey: key})
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	after, err := GetAccount(ctx, tx, aid)
	if err != nil {
		return LoyaltyRedeemResult{}, err
	}
	return LoyaltyRedeemResult{Payment: pay, Account: after}, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "redeem_points",
		EntityType: "crm.loyalty_account", EntityID: aid.String(), EntityLabel: a.Number, PropertyID: &property,
		Before: map[string]any{"balance": a.Balance}, After: map[string]any{"balance": after.Balance, "payment": pay}})
}

// ── rewards ───────────────────────────────────────────────────────────────

// RewardRedemption is a reward exchanged for points.
type RewardRedemption struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	Number         string     `json:"number" db:"number"`
	AccountID      uuid.UUID  `json:"accountId" db:"account_id"`
	AccountNumber  string     `json:"accountNumber" db:"account_number"`
	CustomerID     uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName   string     `json:"customerName" db:"customer_name"`
	RewardID       uuid.UUID  `json:"rewardId" db:"reward_id"`
	RewardCode     string     `json:"rewardCode" db:"reward_code"`
	RewardName     string     `json:"rewardName" db:"reward_name"`
	RewardType     string     `json:"rewardType" db:"reward_type"`
	Quantity       int        `json:"quantity" db:"quantity"`
	Points         int64      `json:"points" db:"points"`
	Status         string     `json:"status" db:"status" enum:"pending,completed,cancelled"`
	FulfilmentCode *string    `json:"fulfilmentCode" db:"fulfilment_code" doc:"Code handed to the customer (voucher rewards)"`
	Channel        string     `json:"channel" db:"channel" enum:"staff,member_app"`
	Note           *string    `json:"note" db:"note"`
	CompletedAt    *time.Time `json:"completedAt" db:"completed_at"`
	CancelledAt    *time.Time `json:"cancelledAt" db:"cancelled_at"`
	CancelReason   *string    `json:"cancelReason" db:"cancel_reason"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

const redemptionSelect = `SELECT r.id, r.number, r.account_id, a.number AS account_number, a.customer_id, c.name AS customer_name, r.reward_id,
	w.code AS reward_code, w.name AS reward_name, w.reward_type, r.quantity, r.points, r.status, r.fulfilment_code, r.channel, r.note,
	r.completed_at, r.cancelled_at, r.cancel_reason, r.created_at
	FROM crm.loyalty_reward_redemptions r JOIN crm.loyalty_accounts a ON a.id = r.account_id JOIN crm.customers c ON c.id = a.customer_id
	JOIN crm.loyalty_rewards w ON w.id = r.reward_id`

// GetRedemption loads a reward redemption.
func GetRedemption(ctx context.Context, q dbtx.Querier, rid uuid.UUID) (RewardRedemption, error) {
	rows, err := q.Query(ctx, redemptionSelect+` WHERE r.id = $1`, rid)
	return handle.One[RewardRedemption](rows, err, "reward redemption")
}

// RedeemRewardInput exchanges points for a reward.
type RedeemRewardInput struct {
	RewardID uuid.UUID `json:"rewardId"`
	Quantity int       `json:"quantity,omitempty" doc:"Default 1"`
	Note     string    `json:"note,omitempty"`
}

// RedeemReward deducts the points of a reward (stock, validity and minimum
// tier checked) and records the redemption for fulfilment.
func (m *Module) RedeemReward(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in RedeemRewardInput, channel string) (RewardRedemption, error) {
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return RewardRedemption{}, err
	}
	if a.PropertyID != property {
		return RewardRedemption{}, errs.NotFound("loyalty account")
	}
	var w struct {
		Code, Name, Type, Status string
		VoucherType              *string
		Cost                     int64
		Stock                    *int
		MinTier                  *uuid.UUID
		From, To                 *time.Time
		Archived                 *time.Time
	}
	err = tx.QueryRow(ctx, `SELECT code, name, reward_type, status, points_cost, stock, min_tier_id, valid_from, valid_to, archived_at, voucher_type_ref
		FROM crm.loyalty_rewards WHERE id = $1 AND property_id = $2 FOR UPDATE`, in.RewardID, property).
		Scan(&w.Code, &w.Name, &w.Type, &w.Status, &w.Cost, &w.Stock, &w.MinTier, &w.From, &w.To, &w.Archived, &w.VoucherType)
	if dbtx.IsNoRows(err) {
		return RewardRedemption{}, errs.NotFound("reward")
	}
	if err != nil {
		return RewardRedemption{}, err
	}
	today := localToday(ctx, tx, property)
	if w.Status != "active" || w.Archived != nil || (w.From != nil && today.Before(*w.From)) || (w.To != nil && today.After(*w.To)) {
		return RewardRedemption{}, errs.Conflict("reward_unavailable", "reward "+w.Name+" is not available")
	}
	if w.Stock != nil && *w.Stock < in.Quantity {
		return RewardRedemption{}, errs.Conflict("out_of_stock", "reward "+w.Name+" is out of stock")
	}
	if w.MinTier != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT t.rank FROM crm.loyalty_tiers t WHERE t.id = $2), 0) >=
			(SELECT rank FROM crm.loyalty_tiers WHERE id = $1)`, *w.MinTier, a.TierID).Scan(&ok); err != nil {
			return RewardRedemption{}, err
		}
		if !ok {
			return RewardRedemption{}, errs.Conflict("tier_required", "reward "+w.Name+" needs a higher tier")
		}
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return RewardRedemption{}, err
	}
	points := w.Cost * int64(in.Quantity)
	if err := checkRedeem(ctx, tx, a, pol, points); err != nil {
		return RewardRedemption{}, err
	}
	if w.Stock != nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_rewards SET stock = stock - $2 WHERE id = $1`, in.RewardID, in.Quantity); err != nil {
			return RewardRedemption{}, err
		}
	}
	num, err := numbering.Next(ctx, tx, property, "RWD", today)
	if err != nil {
		return RewardRedemption{}, err
	}
	rid := id.New()
	var code *string
	if w.Type == "voucher" {
		c := randomCode("RW-", 5)
		code = &c
	}
	value := pol.Value().Mul(decimal.NewFromInt(points))
	e, _, err := m.post(ctx, tx, &a, posting{Kind: KindRedeemed, Points: -points, SourceType: "crm.reward_redemption", SourceID: &rid, SourceRef: num,
		Amount: &value, Key: "reward:" + rid.String(), Description: fmt.Sprintf("Reward %s × %d", w.Name, in.Quantity)})
	if err != nil {
		return RewardRedemption{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_reward_redemptions (id, property_id, number, account_id, reward_id, quantity, points,
		fulfilment_code, channel, ledger_id, note, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
		rid, property, num, a.ID, in.RewardID, in.Quantity, points, code, channel, e.ID, nullStr(in.Note), actor(ctx)); err != nil {
		return RewardRedemption{}, err
	}
	// A voucher reward with a Commercial voucher type is a real voucher of
	// the customer, handed over at once (FR-LOY-05).
	if w.Type == "voucher" && w.VoucherType != nil && strings.TrimSpace(*w.VoucherType) != "" && m.IssueVoucher != nil {
		codes, err := m.IssueVoucher(ctx, tx, RewardVoucher{PropertyID: property, TypeCode: *w.VoucherType, CustomerID: a.CustomerID,
			Quantity: in.Quantity, RedemptionID: rid, Number: num, RewardName: w.Name})
		if err != nil {
			return RewardRedemption{}, err
		}
		c := strings.Join(codes, ", ")
		code = &c
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_redemptions SET fulfilment_code = $2, status = 'completed', completed_at = now(),
			completed_by = $3 WHERE id = $1`, rid, c, actor(ctx)); err != nil {
			return RewardRedemption{}, err
		}
	}
	out, err := GetRedemption(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	data := map[string]any{"reward": w.Name, "points": points, "number": num, "code": ""}
	if code != nil {
		data["code"] = *code
	}
	if err := m.notifyCustomer(ctx, tx, property, a.CustomerID, "crm.loyalty_reward_redeemed", data); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "redeem_reward", EntityType: "crm.loyalty_reward_redemption",
		EntityID: rid.String(), EntityLabel: num, PropertyID: &property, After: out})
}

// RewardRedemptionAction completes or cancels a reward redemption.
type RewardRedemptionAction struct {
	Note           string `json:"note,omitempty"`
	FulfilmentCode string `json:"fulfilmentCode,omitempty" doc:"e.g. the Commercial voucher code issued for the reward"`
	Reason         string `json:"reason,omitempty" doc:"Required to cancel"`
}

// CompleteRedemption marks a reward as handed over.
func (m *Module) CompleteRedemption(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in RewardRedemptionAction) (RewardRedemption, error) {
	before, err := GetRedemption(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	tag, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_redemptions SET status = 'completed', completed_at = now(), completed_by = $3,
		fulfilment_code = coalesce($4, fulfilment_code), note = coalesce($5, note), updated_by = $3 WHERE id = $1 AND property_id = $2 AND status = 'pending'`,
		rid, property, actor(ctx), nullStr(in.FulfilmentCode), nullStr(in.Note))
	if err != nil {
		return before, err
	}
	if tag.RowsAffected() == 0 {
		return before, errs.Conflict("not_pending", "reward redemption "+before.Number+" is "+before.Status)
	}
	after, err := GetRedemption(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_reward_redemption",
		EntityID: rid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after})
}

// CancelRedemption cancels a pending reward and gives the points back.
func (m *Module) CancelRedemption(ctx context.Context, tx pgx.Tx, property, rid uuid.UUID, in RewardRedemptionAction) (RewardRedemption, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return RewardRedemption{}, err
	}
	before, err := GetRedemption(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	var ledger *uuid.UUID
	tag, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_redemptions SET status = 'cancelled', cancelled_at = now(), cancel_reason = $3, updated_by = $4
		WHERE id = $1 AND property_id = $2 AND status = 'pending'`, rid, property, in.Reason, actor(ctx))
	if err != nil {
		return before, err
	}
	if tag.RowsAffected() == 0 {
		return before, errs.Conflict("not_pending", "reward redemption "+before.Number+" is "+before.Status)
	}
	if err := tx.QueryRow(ctx, `SELECT ledger_id FROM crm.loyalty_reward_redemptions WHERE id = $1`, rid).Scan(&ledger); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_rewards SET stock = stock + $2 WHERE id = $1 AND stock IS NOT NULL`, before.RewardID, before.Quantity); err != nil {
		return before, err
	}
	if ledger != nil {
		if err := m.giveBack(ctx, tx, *ledger, decimal.Zero, "reward-cancel:"+rid.String(), "crm.reward_redemption", rid, before.Number); err != nil {
			return before, err
		}
		var back *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT id FROM crm.loyalty_ledger WHERE reverses_id = $1 ORDER BY occurred_at DESC LIMIT 1`, *ledger).Scan(&back)
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_reward_redemptions SET reversal_ledger_id = $2 WHERE id = $1`, rid, back); err != nil {
			return before, err
		}
	}
	after, err := GetRedemption(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_reward_redemption",
		EntityID: rid.String(), EntityLabel: after.Number, PropertyID: &property, Before: before, After: after, Reason: in.Reason})
}

// ── adjustments through approval (FR-LOY-09) ─────────────────────────────

// AdjustmentDocumentType is the approval document of manual adjustments.
var AdjustmentDocumentType = provision.DocumentType{Code: "loyalty_adjustment", Module: "crm", Name: "Loyalty Points Adjustment",
	Attributes: []provision.DocumentAttribute{{Key: "points", Label: "Points (signed)", Type: "number"}, {Key: "absPoints", Label: "Points (absolute)", Type: "number"}}}

// LoyaltyAdjustInput asks for a manual adjustment.
type LoyaltyAdjustInput struct {
	Points int64  `json:"points" doc:"Signed: positive adds, negative deducts"`
	Reason string `json:"reason"`
}

// LoyaltyAdjustment is a manual adjustment request.
type LoyaltyAdjustment struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	AccountID         uuid.UUID  `json:"accountId" db:"account_id"`
	AccountNumber     string     `json:"accountNumber" db:"account_number"`
	CustomerName      string     `json:"customerName" db:"customer_name"`
	Points            int64      `json:"points" db:"points"`
	Reason            string     `json:"reason" db:"reason"`
	SourceType        string     `json:"sourceType" db:"source_type" enum:"manual,ticket"`
	SourceID          *uuid.UUID `json:"sourceId" db:"source_id"`
	Status            string     `json:"status" db:"status" enum:"pending,approved,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	LedgerID          *uuid.UUID `json:"ledgerId" db:"ledger_id"`
	DecidedAt         *time.Time `json:"decidedAt" db:"decided_at"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const adjustmentSelect = `SELECT j.id, j.number, j.account_id, a.number AS account_number, c.name AS customer_name, j.points, j.reason, j.source_type,
	j.source_id, j.status, j.approval_request_id, j.ledger_id, j.decided_at, j.created_at FROM crm.loyalty_adjustments j
	JOIN crm.loyalty_accounts a ON a.id = j.account_id JOIN crm.customers c ON c.id = a.customer_id`

// GetAdjustment loads an adjustment.
func GetAdjustment(ctx context.Context, q dbtx.Querier, jid uuid.UUID) (LoyaltyAdjustment, error) {
	rows, err := q.Query(ctx, adjustmentSelect+` WHERE j.id = $1`, jid)
	return handle.One[LoyaltyAdjustment](rows, err, "adjustment")
}

// RequestAdjustment records an adjustment and submits it for approval; the
// points are posted by AdjustmentDecision once approved (immediately when
// no workflow is configured).
func (m *Module) RequestAdjustment(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in LoyaltyAdjustInput, sourceType string, sourceID *uuid.UUID) (LoyaltyAdjustment, error) {
	if in.Points == 0 {
		return LoyaltyAdjustment{}, handle.Invalid("points", "invalid_points", "points must not be zero")
	}
	if err := handle.Required("reason", in.Reason); err != nil {
		return LoyaltyAdjustment{}, err
	}
	a, err := GetAccount(ctx, tx, aid)
	if err != nil {
		return LoyaltyAdjustment{}, err
	}
	if a.PropertyID != property {
		return LoyaltyAdjustment{}, errs.NotFound("loyalty account")
	}
	if in.Points < 0 && -in.Points > a.Balance {
		return LoyaltyAdjustment{}, errs.Conflict("insufficient_points", fmt.Sprintf("loyalty account %s has %d points", a.Number, a.Balance))
	}
	if sourceType == "" {
		sourceType = "manual"
	}
	num, err := numbering.Next(ctx, tx, property, "LPA", localToday(ctx, tx, property))
	if err != nil {
		return LoyaltyAdjustment{}, err
	}
	jid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_adjustments (id, property_id, number, account_id, points, reason, source_type, source_id,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, jid, property, num, aid, in.Points, in.Reason, sourceType, sourceID, actor(ctx)); err != nil {
		return LoyaltyAdjustment{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.loyalty_adjustment", EntityID: jid.String(),
		EntityLabel: num, PropertyID: &property, Reason: in.Reason, After: map[string]any{"accountId": aid, "points": in.Points}}); err != nil {
		return LoyaltyAdjustment{}, err
	}
	if m.Approvals == nil {
		return LoyaltyAdjustment{}, errs.Unavailable("approvals are not wired")
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: AdjustmentDocumentType.Code, DocumentID: jid, DocumentRef: num,
		Title: fmt.Sprintf("Adjust %s by %+d points", a.Number, in.Points), PropertyID: property,
		Attributes: map[string]any{"points": in.Points, "absPoints": abs(in.Points)}})
	if err != nil {
		return LoyaltyAdjustment{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_adjustments SET approval_request_id = $2 WHERE id = $1`, jid, rid); err != nil {
		return LoyaltyAdjustment{}, err
	}
	return GetAdjustment(ctx, tx, jid)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// AdjustmentDecision applies the approval outcome of an adjustment.
func (m *Module) AdjustmentDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var aid uuid.UUID
	var points int64
	var num, reason, status string
	err := tx.QueryRow(ctx, `SELECT account_id, points, number, reason, status FROM crm.loyalty_adjustments WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&aid, &points, &num, &reason, &status)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil || status != "pending" {
		return err
	}
	newStatus := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
	if newStatus == "" {
		return nil
	}
	var ledger *uuid.UUID
	if newStatus == "approved" {
		a, err := lockAccount(ctx, tx, aid)
		if err != nil {
			return err
		}
		if points < 0 && -points > a.Balance {
			return errs.Conflict("insufficient_points", fmt.Sprintf("loyalty account %s has %d points", a.Number, a.Balance))
		}
		e, _, err := m.post(ctx, tx, &a, posting{Kind: KindAdjusted, Points: points, SourceType: "crm.adjustment", SourceID: &d.DocumentID,
			SourceRef: num, Key: "adjustment:" + d.DocumentID.String(), Description: "Adjustment " + num + ": " + reason})
		if err != nil {
			return err
		}
		ledger = &e.ID
		if err := m.notifyCustomer(ctx, tx, a.PropertyID, a.CustomerID, "crm.loyalty_points_adjusted", map[string]any{"points": points,
			"balance": a.Balance, "reason": reason}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_adjustments SET status = $2, ledger_id = $3, decided_at = now() WHERE id = $1`, d.DocumentID, newStatus, ledger); err != nil {
		return err
	}
	pid := d.PropertyID
	return audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_adjustment",
		EntityID: d.DocumentID.String(), EntityLabel: num, PropertyID: &pid, Reason: d.Reason,
		After: map[string]any{"status": newStatus, "points": points}})
}

// PostAdjustment posts an already approved adjustment (ticket compensation,
// opening balance) without a new approval request.
func (m *Module) PostAdjustment(ctx context.Context, tx pgx.Tx, aid uuid.UUID, points int64, sourceType string, sourceID *uuid.UUID, ref, key, description string,
	expires *time.Time) (LoyaltyEntry, error) {
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return LoyaltyEntry{}, err
	}
	if points < 0 && -points > a.Balance {
		return LoyaltyEntry{}, errs.Conflict("insufficient_points", fmt.Sprintf("loyalty account %s has %d points", a.Number, a.Balance))
	}
	e, _, err := m.post(ctx, tx, &a, posting{Kind: KindAdjusted, Points: points, SourceType: sourceType, SourceID: sourceID, SourceRef: ref, Key: key,
		ExpiresOn: expires, Description: description})
	return e, err
}

// ── expiry (FR-LOY-06) ────────────────────────────────────────────────────

// ExpiryResult reports a run of the expiry job.
type ExpiryResult struct {
	Expired  int `json:"expired" doc:"Lots expired"`
	Points   int `json:"points" doc:"Points expired"`
	Notified int `json:"notified" doc:"Accounts notified of points expiring soon"`
}

// ExpireDue expires the lots past their validity at a property and notifies
// the accounts whose points expire within the notice window.
func (m *Module) ExpireDue(ctx context.Context, tx pgx.Tx, property uuid.UUID) (ExpiryResult, error) {
	var res ExpiryResult
	today := localToday(ctx, tx, property)
	rows, err := tx.Query(ctx, `SELECT ledger_id, account_id FROM crm.loyalty_lots WHERE property_id = $1 AND remaining > 0 AND expires_on < $2::date
		ORDER BY expires_on, ledger_id`, property, today)
	if err != nil {
		return res, err
	}
	type lot struct {
		ID      uuid.UUID `db:"ledger_id"`
		Account uuid.UUID `db:"account_id"`
	}
	lots, err := pgx.CollectRows(rows, pgx.RowToStructByName[lot])
	if err != nil {
		return res, err
	}
	for _, l := range lots {
		a, err := lockAccount(ctx, tx, l.Account)
		if err != nil {
			return res, err
		}
		var remaining int64
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT remaining, expires_on FROM crm.loyalty_lots WHERE ledger_id = $1 FOR UPDATE`, l.ID).Scan(&remaining, &expires); err != nil {
			return res, err
		}
		if remaining <= 0 {
			continue
		}
		lid := l.ID
		if _, _, err := m.post(ctx, tx, &a, posting{Kind: KindExpired, Points: -remaining, SourceType: "expiry", SourceID: &lid,
			Key: "expiry:" + l.ID.String(), ConsumeLot: &lid, Description: "Points expired (valid until " + expires.Format("2006-01-02") + ")"}); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_lots SET expired_at = now() WHERE ledger_id = $1`, l.ID); err != nil {
			return res, err
		}
		res.Expired++
		res.Points += int(remaining)
	}
	pol, _, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return res, err
	}
	days := pol.ExpiryNoticeDays
	if days <= 0 {
		return res, nil
	}
	nrows, err := tx.Query(ctx, `SELECT l.account_id, a.customer_id, sum(l.remaining)::bigint AS points, min(l.expires_on) AS expires_on
		FROM crm.loyalty_lots l JOIN crm.loyalty_accounts a ON a.id = l.account_id
		WHERE l.property_id = $1 AND l.remaining > 0 AND l.notified_at IS NULL AND l.expires_on >= $2::date AND l.expires_on <= $2::date + $3::int
		AND a.status = 'active' GROUP BY l.account_id, a.customer_id`, property, today, days)
	if err != nil {
		return res, err
	}
	type notice struct {
		Account  uuid.UUID `db:"account_id"`
		Customer uuid.UUID `db:"customer_id"`
		Points   int64     `db:"points"`
		Expires  time.Time `db:"expires_on"`
	}
	notices, err := pgx.CollectRows(nrows, pgx.RowToStructByName[notice])
	if err != nil {
		return res, err
	}
	for _, n := range notices {
		if err := m.notifyCustomer(ctx, tx, property, n.Customer, "crm.loyalty_points_expiring", map[string]any{"points": n.Points,
			"date": n.Expires.Format("2006-01-02"), "days": days}); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_lots SET notified_at = now() WHERE account_id = $1 AND remaining > 0 AND notified_at IS NULL
			AND expires_on <= $2::date + $3::int`, n.Account, today, days); err != nil {
			return res, err
		}
		res.Notified++
	}
	return res, nil
}

// ── liability (FR-LOY-08) ─────────────────────────────────────────────────

// LoyaltyLiability is the points liability of a property.
type LoyaltyLiability struct {
	At              time.Time `json:"at"`
	Accounts        int       `json:"accounts" doc:"Accounts with a positive balance"`
	Points          int64     `json:"points" doc:"Outstanding points"`
	RedemptionValue string    `json:"redemptionValue"`
	Liability       string    `json:"liability" doc:"Points × redemption value"`
	Currency        string    `json:"currency"`
}

// LiabilityAt values the outstanding points (balance from the ledger at a
// time) at the redemption value in force.
func LiabilityAt(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (LoyaltyLiability, error) {
	out := LoyaltyLiability{At: at, Currency: currency(ctx, q)}
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE b > 0)::int, coalesce(sum(b) FILTER (WHERE b > 0), 0)::bigint FROM
		(SELECT sum(points) AS b FROM crm.loyalty_ledger WHERE property_id = $1 AND occurred_at <= $2 GROUP BY account_id) x`, property, at).
		Scan(&out.Accounts, &out.Points); err != nil {
		return out, err
	}
	pol, _, err := LoadPolicy(ctx, q, property)
	if err != nil {
		return out, err
	}
	v := pol.Value()
	out.RedemptionValue = v.String()
	out.Liability = v.Mul(decimal.NewFromInt(out.Points)).String()
	return out, nil
}

// Liability is the billing LiabilityProvider of the business day summary
// (registered by internal/app as "loyalty_points").
func (m *Module) Liability(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (map[string]decimal.Decimal, error) {
	s, err := LiabilityAt(ctx, q, property, at)
	if err != nil {
		return nil, err
	}
	return map[string]decimal.Decimal{"loyalty_points": dec(s.Liability)}, nil
}
