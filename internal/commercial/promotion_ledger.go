package commercial

// Promotion redemption ledger (FR-PRM-04/05/07, contract K3). A document
// (POS order, priced booking snapshot, package booking) holds Applied
// redemptions while it is open; limits and budgets are checked again under
// row locks when they are written, so a single-use code or the last unit of
// a budget can never be redeemed twice (PRD P3 §12 Konkurensi). Completing
// the document turns them Redeemed and publishes commercial.promotion_applied
// (discount by business line for P4); voids, refunds and cancellations
// reverse them.

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/outbox"
)

// EventPromotionApplied is published when a promotion is redeemed (K3).
const EventPromotionApplied = "commercial.promotion_applied"

// Publisher publishes domain events through the outbox (outbox.Bus).
type Publisher interface {
	Publish(ctx context.Context, tx pgx.Tx, eventType, aggregateType string, aggregateID, propertyID *uuid.UUID, payload any) (uuid.UUID, error)
}

// Redemption sources.
const (
	SourcePOSOrder       = "pos_order"
	SourceSnapshot       = "pricing_snapshot"
	SourcePackageBooking = "package_booking"
)

// RedemptionInput describes the document of a set of redemptions.
type RedemptionInput struct {
	Property     uuid.UUID
	Source       PromoSource
	SourceRef    string
	CustomerID   *uuid.UUID
	Channel      string
	BusinessLine string
	Currency     string
	Offline      bool
	NeedsReview  bool
}

// Redemption is one ledger row.
type Redemption struct {
	ID               uuid.UUID      `json:"id" db:"id"`
	PromotionID      uuid.UUID      `json:"promotionId" db:"promotion_id"`
	PromotionCode    string         `json:"promotionCode" db:"promotion_code"`
	PromotionVersion int            `json:"promotionVersion" db:"promotion_version"`
	PromoCode        *string        `json:"promoCode" db:"promo_code"`
	SourceType       string         `json:"sourceType" db:"source_type" enum:"pos_order,pricing_snapshot,package_booking"`
	SourceID         uuid.UUID      `json:"sourceId" db:"source_id"`
	SourceRef        *string        `json:"sourceRef" db:"source_ref"`
	FolioID          *uuid.UUID     `json:"folioId" db:"folio_id"`
	CustomerID       *uuid.UUID     `json:"customerId" db:"customer_id"`
	Channel          string         `json:"channel" db:"channel"`
	BusinessLine     string         `json:"businessLine" db:"business_line"`
	Discount         string         `json:"discount" db:"discount_amount"`
	Currency         string         `json:"currency" db:"currency"`
	Lines            []LineDiscount `json:"lines" db:"lines"`
	Offline          bool           `json:"offline" db:"offline"`
	NeedsReview      bool           `json:"needsReview" db:"needs_review"`
	Status           string         `json:"status" db:"status" enum:"applied,redeemed,reversed"`
	CreatedAt        string         `json:"createdAt" db:"created_at"`
}

const redemptionSelect = `SELECT id, promotion_id, promotion_code, promotion_version, promo_code, source_type, source_id, source_ref, folio_id, customer_id,
	channel, business_line, trim_scale(discount_amount)::text AS discount_amount, currency, lines, offline, needs_review, status,
	to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS created_at FROM commercial.promotion_redemptions`

// Redemptions lists the redemptions of a document.
func Redemptions(ctx context.Context, q dbtx.Querier, sourceType string, sourceID uuid.UUID) ([]Redemption, error) {
	return handle.List[Redemption](q.Query(ctx, redemptionSelect+` WHERE source_type = $1 AND source_id = $2 ORDER BY created_at, promotion_code`,
		sourceType, sourceID))
}

// ReplaceRedemptions replaces the Applied redemptions of a document with
// applied, re-checking the limits under row locks.
func ReplaceRedemptions(ctx context.Context, tx pgx.Tx, in RedemptionInput, applied []AppliedPromotion) error {
	// promotions touched now or before
	var prev []uuid.UUID
	rows, err := tx.Query(ctx, `SELECT DISTINCT promotion_id FROM commercial.promotion_redemptions WHERE source_type = $1 AND source_id = $2
		AND status = 'applied'`, in.Source.Type, in.Source.ID)
	if err != nil {
		return err
	}
	prev, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	ids := map[uuid.UUID]bool{}
	for _, p := range prev {
		ids[p] = true
	}
	var codeIDs []uuid.UUID
	for _, a := range applied {
		ids[a.PromotionID] = true
		if a.PromoCodeID != nil {
			codeIDs = append(codeIDs, *a.PromoCodeID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	all := make([]uuid.UUID, 0, len(ids))
	for k := range ids {
		all = append(all, k)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })
	if _, err := tx.Exec(ctx, `SELECT id FROM commercial.promotions WHERE id = ANY($1) ORDER BY id FOR UPDATE`, all); err != nil {
		return err
	}
	if len(codeIDs) > 0 {
		if _, err := tx.Exec(ctx, `SELECT id FROM commercial.promo_codes WHERE id = ANY($1) ORDER BY id FOR UPDATE`, codeIDs); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM commercial.promotion_redemptions WHERE source_type = $1 AND source_id = $2 AND status = 'applied'`,
		in.Source.Type, in.Source.ID); err != nil {
		return err
	}
	cur := in.Currency
	if cur == "" {
		cur = currencyOf(ctx, tx)
	}
	for _, a := range applied {
		d := dec(a.Discount)
		if !d.IsPositive() {
			continue
		}
		review := in.NeedsReview
		if err := checkLimits(ctx, tx, in, a, d); err != nil {
			// an offline sale already happened at the terminal: it is kept and
			// flagged for review instead of being rejected at sync (FR-PRM-09)
			if !in.Offline || !errs.Is(err, errs.KindConflict) {
				return err
			}
			review = true
		}
		lines, _ := json.Marshal(a.Lines)
		if _, err := tx.Exec(ctx, `INSERT INTO commercial.promotion_redemptions (id, property_id, promotion_id, promotion_version, promotion_code,
			promo_code_id, promo_code, source_type, source_id, source_ref, customer_id, channel, business_line, discount_amount, currency, lines, offline,
			needs_review, status, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17,$18,'applied',$19)`,
			id.New(), in.Property, a.PromotionID, a.Version, a.Code, a.PromoCodeID, nullStr(a.PromoCode), in.Source.Type, in.Source.ID, nullStr(in.SourceRef),
			in.CustomerID, nonEmpty(in.Channel, "back_office"), nonEmpty(in.BusinessLine, "other"), d.String(), cur, lines, in.Offline, review,
			actorPtr(ctx)); err != nil {
			return err
		}
	}
	return refreshUsage(ctx, tx, all)
}

// checkLimits enforces the redemption limit, budget, per customer limit and
// promo code limits with the other documents' redemptions.
func checkLimits(ctx context.Context, tx pgx.Tx, in RedemptionInput, a AppliedPromotion, d decimal.Decimal) error {
	var maxRed, maxCust *int
	var budget *string
	if err := tx.QueryRow(ctx, `SELECT max_redemptions, max_per_customer, budget_amount::text FROM commercial.promotions WHERE id = $1`, a.PromotionID).
		Scan(&maxRed, &maxCust, &budget); err != nil {
		return err
	}
	count := func(extra string, args ...any) (int, decimal.Decimal, error) {
		var n int
		var amt string
		base := []any{a.PromotionID, in.Source.Type, in.Source.ID}
		err := tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(discount_amount), 0)::text FROM commercial.promotion_redemptions
			WHERE promotion_id = $1 AND status IN ('applied', 'redeemed') AND NOT (source_type = $2 AND source_id = $3)`+extra, append(base, args...)...).
			Scan(&n, &amt)
		return n, dec(amt), err
	}
	n, used, err := count("")
	if err != nil {
		return err
	}
	if maxRed != nil && n >= *maxRed {
		return errs.Conflict("promotion_limit_reached", "promotion "+a.Code+" has reached its redemption limit")
	}
	if budget != nil && used.Add(d).GreaterThan(dec(*budget)) {
		return errs.Conflict("promotion_budget_exhausted", "promotion "+a.Code+" has no budget left for this discount")
	}
	if maxCust != nil && in.CustomerID != nil {
		n, _, err := count(` AND customer_id = $4`, *in.CustomerID)
		if err != nil {
			return err
		}
		if n >= *maxCust {
			return errs.Conflict("promotion_limit_reached", "the customer has used promotion "+a.Code+" the maximum number of times")
		}
	}
	if a.PromoCodeID != nil {
		var maxUses, maxPer *int
		var status string
		if err := tx.QueryRow(ctx, `SELECT max_uses, max_uses_per_customer, status FROM commercial.promo_codes WHERE id = $1`, *a.PromoCodeID).
			Scan(&maxUses, &maxPer, &status); err != nil {
			return err
		}
		if status != "active" {
			return errs.Conflict("promo_code_inactive", "promo code "+a.PromoCode+" is inactive")
		}
		if maxUses != nil {
			n, _, err := count(` AND promo_code_id = $4`, *a.PromoCodeID)
			if err != nil {
				return err
			}
			if n >= *maxUses {
				return errs.Conflict("promo_code_used_up", "promo code "+a.PromoCode+" has been used up")
			}
		}
		if maxPer != nil && in.CustomerID != nil {
			n, _, err := count(` AND promo_code_id = $4 AND customer_id = $5`, *a.PromoCodeID, *in.CustomerID)
			if err != nil {
				return err
			}
			if n >= *maxPer {
				return errs.Conflict("promo_code_used_up", "promo code "+a.PromoCode+" has been used the maximum number of times by this customer")
			}
		}
	}
	return nil
}

// refreshUsage recomputes the usage counters of promotions and their codes.
func refreshUsage(ctx context.Context, tx pgx.Tx, promotions []uuid.UUID) error {
	if len(promotions) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.promotions p SET
		used_count = (SELECT count(*) FROM commercial.promotion_redemptions r WHERE r.promotion_id = p.id AND r.status IN ('applied', 'redeemed')),
		used_amount = (SELECT coalesce(sum(discount_amount), 0) FROM commercial.promotion_redemptions r WHERE r.promotion_id = p.id
		  AND r.status IN ('applied', 'redeemed'))
		WHERE p.id = ANY($1)`, promotions); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE commercial.promo_codes c SET used_count = (SELECT count(*) FROM commercial.promotion_redemptions r
		WHERE r.promo_code_id = c.id AND r.status IN ('applied', 'redeemed')) WHERE c.promotion_id = ANY($1)`, promotions)
	return err
}

// PromotionAppliedPayload is commercial.promotion_applied (K3); fields
// after currency are additive.
type PromotionAppliedPayload struct {
	PromotionID      uuid.UUID      `json:"promotionId"`
	Code             string         `json:"code"`
	SourceType       string         `json:"sourceType" doc:"pos_order | folio | package_booking"`
	SourceID         uuid.UUID      `json:"sourceId"`
	CustomerID       *uuid.UUID     `json:"customerId"`
	BusinessLine     string         `json:"businessLine"`
	Discount         string         `json:"discount"`
	Currency         string         `json:"currency"`
	PromoCode        *string        `json:"promoCode,omitempty"`
	PromotionVersion int            `json:"promotionVersion"`
	RedemptionID     uuid.UUID      `json:"redemptionId"`
	Channel          string         `json:"channel"`
	Lines            []LineDiscount `json:"lines"`
}

// RedeemSource turns the Applied redemptions of a document Redeemed and
// publishes commercial.promotion_applied for each one; eventSource is the
// K3 source (pos_order, folio or package_booking).
func RedeemSource(ctx context.Context, tx pgx.Tx, pub Publisher, src PromoSource, eventSourceType string, eventSourceID uuid.UUID, folio *uuid.UUID) error {
	rows, err := tx.Query(ctx, redemptionSelect+` WHERE source_type = $1 AND source_id = $2 AND status = 'applied' ORDER BY promotion_code FOR UPDATE`,
		src.Type, src.ID)
	list, err := handle.List[Redemption](rows, err)
	if err != nil {
		return err
	}
	for _, r := range list {
		if _, err := tx.Exec(ctx, `UPDATE commercial.promotion_redemptions SET status = 'redeemed', redeemed_at = now(), folio_id = coalesce($2, folio_id)
			WHERE id = $1`, r.ID, folio); err != nil {
			return err
		}
		var pid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT property_id FROM commercial.promotion_redemptions WHERE id = $1`, r.ID).Scan(&pid); err != nil {
			return err
		}
		if pub == nil {
			continue
		}
		if _, err := pub.Publish(ctx, tx, EventPromotionApplied, "commercial.promotion", &r.PromotionID, &pid, PromotionAppliedPayload{
			PromotionID: r.PromotionID, Code: r.PromotionCode, SourceType: eventSourceType, SourceID: eventSourceID, CustomerID: r.CustomerID,
			BusinessLine: r.BusinessLine, Discount: r.Discount, Currency: r.Currency, PromoCode: r.PromoCode, PromotionVersion: r.PromotionVersion,
			RedemptionID: r.ID, Channel: r.Channel, Lines: r.Lines}); err != nil {
			return err
		}
	}
	return nil
}

// ReverseSource reverses the redemptions of a document (void, refund,
// cancellation, expiry) and frees their usage.
func ReverseSource(ctx context.Context, tx pgx.Tx, src PromoSource, reason string) error {
	rows, err := tx.Query(ctx, `UPDATE commercial.promotion_redemptions SET status = 'reversed', reversed_at = now(), reverse_reason = $3
		WHERE source_type = $1 AND source_id = $2 AND status IN ('applied', 'redeemed') RETURNING promotion_id`, src.Type, src.ID, reason)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(ids) == 0 {
		return err
	}
	return refreshUsage(ctx, tx, ids)
}

// OnFolioClosed redeems the booking prices with promotions (pricing
// snapshots) once their folio is closed: commercial.promotion_applied with
// sourceType folio; snapshots of voided lines are reversed
// (billing.folio_closed subscriber, wired by internal/app).
func OnFolioClosed(pub Publisher) outbox.Handler {
	return func(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
		var p struct {
			FolioID uuid.UUID `json:"folioId"`
		}
		if err := e.Decode(&p); err != nil {
			return err
		}
		f, err := billing.GetFolio(ctx, tx, p.FolioID)
		if err != nil {
			if errs.Is(err, errs.KindNotFound) {
				return nil
			}
			return err
		}
		live, voided := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
		for _, l := range f.Lines {
			if l.SnapshotID == nil {
				continue
			}
			if l.VoidedAt != nil {
				voided[*l.SnapshotID] = true
			} else {
				live[*l.SnapshotID] = true
			}
		}
		for sid := range live {
			if err := RedeemSource(ctx, tx, pub, PromoSource{Type: SourceSnapshot, ID: sid}, "folio", p.FolioID, &p.FolioID); err != nil {
				return err
			}
		}
		for sid := range voided {
			if live[sid] {
				continue
			}
			if err := ReverseSource(ctx, tx, PromoSource{Type: SourceSnapshot, ID: sid}, "charge voided"); err != nil {
				return err
			}
		}
		return nil
	}
}

// SnapshotPromotions is the JSON stored on a pricing snapshot.
func SnapshotPromotions(applied []AppliedPromotion) []byte {
	if len(applied) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(applied)
	return b
}

// recordSnapshot writes the Applied redemptions of a priced booking line.
func recordSnapshot(ctx context.Context, tx pgx.Tx, property, snapshot uuid.UUID, applied []AppliedPromotion, customer *uuid.UUID, channel, line,
	currency string) error {
	if len(applied) == 0 {
		return nil
	}
	return ReplaceRedemptions(ctx, tx, RedemptionInput{Property: property, Source: PromoSource{Type: SourceSnapshot, ID: snapshot},
		CustomerID: customer, Channel: promoChannel(channel), BusinessLine: line, Currency: currency}, applied)
}
