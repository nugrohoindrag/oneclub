package commercial

// Promotion actions and channels (FR-PRM-04/06/08/10): activation with the
// Promotion Activation approval, approve, deactivate, simulation, cart
// evaluation, usage, bulk promo code generation, staff and public
// (rate-limited) promo code checks, the website promotion list (K5) and the
// member app Offers.

import (
	"context"
	"crypto/rand"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// PromotionState is a promotion after an action.
type PromotionState struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Code              string     `json:"code" db:"code"`
	Name              string     `json:"name" db:"name"`
	Status            string     `json:"status" db:"status" enum:"draft,pending,active,inactive,rejected,expired"`
	Version           int        `json:"version" db:"version"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	ActivatedAt       *time.Time `json:"activatedAt" db:"activated_at"`
	UsedCount         int        `json:"usedCount" db:"used_count"`
	UsedAmount        string     `json:"usedAmount" db:"used_amount"`
}

func promotionState(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (PromotionState, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, status, version, approval_request_id, activated_at, used_count, trim_scale(used_amount)::text AS used_amount
		FROM commercial.promotions WHERE id = $1 AND archived_at IS NULL`, pid)
	return handle.One[PromotionState](rows, err, "promotion")
}

// PromotionReasonInput carries an optional reason.
type PromotionReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

// ActivatePromotion activates a promotion; when the Promotion Policies
// require approval it waits as Pending for the Promotion Activation
// approval (auto-approved without a workflow, FR-PRM-10).
func (m *P3Module) ActivatePromotion(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (PromotionState, error) {
	var property uuid.UUID
	var status, code, name, ptype string
	var pct, amt, budget *string
	if err := tx.QueryRow(ctx, `SELECT property_id, status, code, name, promo_type, discount_percent::text, discount_amount::text, budget_amount::text
		FROM commercial.promotions WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, pid).Scan(&property, &status, &code, &name, &ptype, &pct, &amt, &budget); err != nil {
		if dbtx.IsNoRows(err) {
			return PromotionState{}, errs.NotFound("promotion")
		}
		return PromotionState{}, err
	}
	switch status {
	case PromoActive:
		return PromotionState{}, errs.Conflict("promotion_active", "promotion "+code+" is already Active")
	case PromoPending:
		return PromotionState{}, errs.Conflict("promotion_pending", "promotion "+code+" is waiting for approval")
	}
	pol, ref, err := LoadPromotionPolicy(ctx, tx, property)
	if err != nil {
		return PromotionState{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.promotions SET status = 'pending', approval_request_id = NULL, updated_by = $2 WHERE id = $1`,
		pid, actorPtr(ctx)); err != nil {
		return PromotionState{}, err
	}
	if !pol.RequireApproval {
		if err := setPromotionActive(ctx, tx, pid, handle.UserID(ctx)); err != nil {
			return PromotionState{}, err
		}
	} else {
		attrs := map[string]any{"promoType": ptype}
		for k, v := range map[string]*string{"discountPercent": pct, "discountAmount": amt, "budget": budget} {
			if v != nil {
				f, _ := dec(*v).Float64()
				attrs[k] = f
			}
		}
		rid, st, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: PromotionActivationType.Code, DocumentID: pid, DocumentRef: code,
			Title: "Activate promotion " + code + " · " + name, PropertyID: property, Attributes: attrs})
		if err != nil {
			return PromotionState{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE commercial.promotions SET approval_request_id = $2 WHERE id = $1`, pid, rid); err != nil {
			return PromotionState{}, err
		}
		_ = st
	}
	out, err := promotionState(ctx, tx, pid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "activate", EntityType: "commercial.promotion", EntityID: pid.String(),
		EntityLabel: code + " · " + name, PropertyID: &property, Before: map[string]any{"status": status},
		After: map[string]any{"status": out.Status, "version": out.Version, "approvalRequestId": out.ApprovalRequestID}, Metadata: map[string]any{"policy": ref}})
}

func setPromotionActive(ctx context.Context, tx pgx.Tx, pid, by uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE commercial.promotions SET status = 'active', activated_at = now(), activated_by = $2 WHERE id = $1 AND status = 'pending'`,
		pid, id.Ptr(by))
	return err
}

// PromotionDecision applies the Promotion Activation approval decision.
func (m *P3Module) PromotionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, code string
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, code, property_id FROM commercial.promotions WHERE id = $1 FOR UPDATE`, d.DocumentID).Scan(&status, &code, &property); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != PromoPending {
		return nil // deactivated meanwhile
	}
	to := map[string]string{approval.StatusApproved: PromoActive, approval.StatusRejected: PromoRejected, approval.StatusCancelled: PromoDraft}[d.Status]
	if to == "" {
		return nil
	}
	if to == PromoActive {
		if err := setPromotionActive(ctx, tx, d.DocumentID, d.DecidedBy); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE commercial.promotions SET status = $2 WHERE id = $1`, d.DocumentID, to); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionStatusChange, EntityType: "commercial.promotion",
		EntityID: d.DocumentID.String(), EntityLabel: code, PropertyID: &property, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": to, "approvalRequestId": d.RequestID}})
}

// ApprovePromotion approves the pending Promotion Activation of a promotion
// (the approver of the current workflow step).
func (m *P3Module) ApprovePromotion(ctx context.Context, tx pgx.Tx, pid uuid.UUID, reason string) (PromotionState, error) {
	st, err := promotionState(ctx, tx, pid)
	if err != nil {
		return st, err
	}
	if st.Status != PromoPending || st.ApprovalRequestID == nil {
		return st, errs.Conflict("promotion_not_pending", "promotion "+st.Code+" is not waiting for approval")
	}
	if err := m.Approvals.Decide(ctx, tx, *st.ApprovalRequestID, true, reason); err != nil {
		return st, err
	}
	return promotionState(ctx, tx, pid)
}

// DeactivatePromotion stops a promotion (Active or Pending).
func (m *P3Module) DeactivatePromotion(ctx context.Context, tx pgx.Tx, pid uuid.UUID, reason string) (PromotionState, error) {
	var property uuid.UUID
	var status, code string
	if err := tx.QueryRow(ctx, `SELECT property_id, status, code FROM commercial.promotions WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, pid).
		Scan(&property, &status, &code); err != nil {
		if dbtx.IsNoRows(err) {
			return PromotionState{}, errs.NotFound("promotion")
		}
		return PromotionState{}, err
	}
	if status != PromoActive && status != PromoPending {
		return PromotionState{}, errs.Conflict("promotion_not_active", "promotion "+code+" is "+status)
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.promotions SET status = 'inactive', updated_by = $2 WHERE id = $1`, pid, actorPtr(ctx)); err != nil {
		return PromotionState{}, err
	}
	out, err := promotionState(ctx, tx, pid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "deactivate", EntityType: "commercial.promotion", EntityID: pid.String(),
		EntityLabel: code, PropertyID: &property, Reason: reason, Before: map[string]any{"status": status}, After: map[string]any{"status": PromoInactive}})
}

// ── evaluation & simulation (FR-PRM-06, FR-PRM-08) ─────────────────────────

// EvaluateLine is a cart line to price.
type EvaluateLine struct {
	Key         string     `json:"key,omitempty"`
	ProductID   *uuid.UUID `json:"productId,omitempty"`
	Category    string     `json:"category,omitempty"`
	ProductType string     `json:"productType,omitempty"`
	ServiceType string     `json:"serviceType,omitempty"`
	ItemRef     string     `json:"itemRef,omitempty"`
	Quantity    string     `json:"quantity,omitempty" doc:"Default 1"`
	UnitPrice   string     `json:"unitPrice"`
}

// EvaluateInput prices a cart with the promotions of a channel.
type EvaluateInput struct {
	Label        string         `json:"label,omitempty"`
	At           *time.Time     `json:"at,omitempty" doc:"Sale / service time (default now)"`
	Channel      string         `json:"channel,omitempty" enum:"pos,member_app,website,back_office,ops"`
	BusinessLine string         `json:"businessLine,omitempty"`
	OutletID     *uuid.UUID     `json:"outletId,omitempty"`
	CustomerID   *uuid.UUID     `json:"customerId,omitempty"`
	Segment      string         `json:"segment,omitempty"`
	PromoCodes   []string       `json:"promoCodes,omitempty"`
	Exclude      []uuid.UUID    `json:"exclude,omitempty" doc:"Promotions removed from this sale"`
	Lines        []EvaluateLine `json:"lines"`
}

// Evaluation is the priced cart.
type Evaluation struct {
	Label string `json:"label,omitempty"`
	Total string `json:"total" doc:"Subtotal − discount"`
	PromoResult
}

func (in EvaluateInput) context(property uuid.UUID) (PromoContext, error) {
	pc := PromoContext{Property: property, Channel: nonEmpty(in.Channel, "back_office"), BusinessLine: in.BusinessLine, OutletID: in.OutletID,
		CustomerID: in.CustomerID, Segment: in.Segment, Codes: in.PromoCodes, Exclude: in.Exclude}
	if in.At != nil {
		pc.At = in.At.UTC()
	}
	if len(in.Lines) == 0 {
		return pc, handle.Invalid("lines", "required", "at least one line")
	}
	for i, l := range in.Lines {
		q, err := handle.Decimal("lines.quantity", l.Quantity, decimal.NewFromInt(1))
		if err != nil {
			return pc, err
		}
		p, err := handle.Decimal("lines.unitPrice", l.UnitPrice, decimal.Zero)
		if err != nil {
			return pc, err
		}
		key := l.Key
		if key == "" {
			key = strings.TrimSpace(strconvI(i + 1))
		}
		pc.Lines = append(pc.Lines, PromoLine{Key: key, ProductID: l.ProductID, Category: l.Category, ProductType: l.ProductType,
			ServiceType: l.ServiceType, ItemRef: l.ItemRef, Quantity: q, UnitPrice: p})
	}
	return pc, nil
}

func strconvI(n int) string {
	return decimal.NewFromInt(int64(n)).String()
}

func evaluationOf(label string, r PromoResult) Evaluation {
	return Evaluation{Label: label, Total: dec(r.Subtotal).Sub(dec(r.Discount)).String(), PromoResult: r}
}

// SimulateInput tries a promotion (any status) on scenarios before it is
// activated (FR-PRM-08).
type SimulateInput struct {
	Scenarios []EvaluateInput `json:"scenarios"`
}

// Simulation is the result per scenario.
type Simulation struct {
	PromotionID uuid.UUID    `json:"promotionId"`
	Scenarios   []Evaluation `json:"scenarios"`
}

// ── promo codes (FR-PRM-04) ───────────────────────────────────────────────

// PromoCodeGenerateInput creates promo codes in bulk.
type PromoCodeGenerateInput struct {
	PromotionID        uuid.UUID   `json:"promotionId"`
	Count              int         `json:"count,omitempty" doc:"Number of codes (ignored with customerIds)"`
	CustomerIDs        []uuid.UUID `json:"customerIds,omitempty" doc:"One personal code per customer (campaign)"`
	Prefix             string      `json:"prefix,omitempty"`
	Length             int         `json:"length,omitempty" doc:"Random part length (6–16, default 8)"`
	MaxUses            *int        `json:"maxUses,omitempty"`
	MaxUsesPerCustomer *int        `json:"maxUsesPerCustomer,omitempty"`
	ExpiresAt          *time.Time  `json:"expiresAt,omitempty"`
	CampaignRef        string      `json:"campaignRef,omitempty"`
}

// GeneratedCodes reports a generation batch.
type GeneratedCodes struct {
	BatchID uuid.UUID          `json:"batchId"`
	Count   int                `json:"count"`
	Codes   []GeneratedCodeRow `json:"codes"`
}

// GeneratedCodeRow is one generated code.
type GeneratedCodeRow struct {
	ID         uuid.UUID  `json:"id"`
	Code       string     `json:"code"`
	CustomerID *uuid.UUID `json:"customerId"`
}

const promoAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

func randomCode(prefix string, n int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; i < n; i++ {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(promoAlphabet))))
		b.WriteByte(promoAlphabet[x.Int64()])
	}
	return b.String()
}

// GenerateCodes creates unique promo codes for a promotion.
func (m *P3Module) GenerateCodes(ctx context.Context, tx pgx.Tx, property uuid.UUID, in PromoCodeGenerateInput) (GeneratedCodes, error) {
	var code string
	if err := tx.QueryRow(ctx, `SELECT code FROM commercial.promotions WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, in.PromotionID, property).
		Scan(&code); err != nil {
		if dbtx.IsNoRows(err) {
			return GeneratedCodes{}, handle.Invalid("promotionId", "not_found", "promotion not found")
		}
		return GeneratedCodes{}, err
	}
	n := in.Count
	if len(in.CustomerIDs) > 0 {
		n = len(in.CustomerIDs)
	}
	if n < 1 || n > 10000 {
		return GeneratedCodes{}, handle.Invalid("count", "invalid", "1 to 10,000 codes")
	}
	length := in.Length
	if length == 0 {
		length = 8
	}
	if length < 6 || length > 16 {
		return GeneratedCodes{}, handle.Invalid("length", "invalid", "6 to 16 characters")
	}
	prefix := strings.ToUpper(strings.TrimSpace(in.Prefix))
	if prefix != "" && !promoCodeRe.MatchString(prefix+"XXX") {
		return GeneratedCodes{}, handle.Invalid("prefix", "invalid", "A–Z, 0–9, - or _")
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(clock.Now()) {
		return GeneratedCodes{}, handle.Invalid("expiresAt", "past", "choose a future time")
	}
	batch := id.New()
	out := GeneratedCodes{BatchID: batch, Codes: []GeneratedCodeRow{}}
	for i := 0; i < n; i++ {
		var cust *uuid.UUID
		if len(in.CustomerIDs) > 0 {
			c := in.CustomerIDs[i]
			cust = &c
		}
		var row GeneratedCodeRow
		for attempt := 0; ; attempt++ {
			c := randomCode(prefix, length)
			sp, err := tx.Begin(ctx)
			if err != nil {
				return out, err
			}
			cid := id.New()
			_, err = sp.Exec(ctx, `INSERT INTO commercial.promo_codes (id, property_id, promotion_id, code, customer_id, campaign_ref, batch_id, max_uses,
				max_uses_per_customer, expires_at, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
				cid, property, in.PromotionID, c, cust, nullStr(in.CampaignRef), batch, in.MaxUses, in.MaxUsesPerCustomer, in.ExpiresAt, actorPtr(ctx))
			if err != nil {
				_ = sp.Rollback(ctx)
				if ok, _ := dbtx.IsUniqueViolation(err); ok && attempt < 5 {
					continue
				}
				if dbtx.IsForeignKeyViolation(err) {
					return out, handle.Invalid("customerIds", "not_found", "customer not found")
				}
				return out, err
			}
			if err := sp.Commit(ctx); err != nil {
				return out, err
			}
			row = GeneratedCodeRow{ID: cid, Code: c, CustomerID: cust}
			break
		}
		if len(out.Codes) < 500 {
			out.Codes = append(out.Codes, row)
		}
		out.Count++
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "generate_codes", EntityType: "commercial.promotion",
		EntityID: in.PromotionID.String(), EntityLabel: code, PropertyID: &property,
		After: map[string]any{"batchId": batch, "count": out.Count, "personal": len(in.CustomerIDs) > 0, "campaignRef": in.CampaignRef}})
}

// CodeCheckInput checks a promo code.
type CodeCheckInput struct {
	Code         string     `json:"code"`
	CustomerID   *uuid.UUID `json:"customerId,omitempty"`
	Amount       string     `json:"amount,omitempty" doc:"Purchase amount (minimum purchase)"`
	Channel      string     `json:"channel,omitempty" enum:"pos,member_app,website,back_office,ops"`
	BusinessLine string     `json:"businessLine,omitempty"`
}

// CodeCheck is the result of a promo code check.
type CodeCheck struct {
	Code      string         `json:"code"`
	Valid     bool           `json:"valid"`
	Reason    string         `json:"reason,omitempty"`
	Promotion *PromotionView `json:"promotion"`
	UsesLeft  *int           `json:"usesLeft"`
	ExpiresAt *time.Time     `json:"expiresAt"`
}

// CheckCode tells whether a promo code can be used (channel, customer,
// expiry, uses left, minimum purchase); it never redeems.
func CheckCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, in CodeCheckInput) (CodeCheck, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	out := CodeCheck{Code: code}
	if code == "" {
		return out, handle.Invalid("code", "required", "enter the promo code")
	}
	rows, err := q.Query(ctx, `SELECT id, promotion_id, code, customer_id, max_uses, max_uses_per_customer, expires_at, status FROM commercial.promo_codes
		WHERE property_id = $1 AND code = $2`, property, code)
	c, err := handle.One[codeRow](rows, err, "promo code")
	if errs.Is(err, errs.KindNotFound) {
		out.Reason = "unknown promo code"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.ExpiresAt = c.ExpiresAt
	now := clock.Now()
	loc := calendar.Location(ctx, q)
	ps, err := loadPromotions(ctx, q, property, now.In(loc).Format("2006-01-02"), []uuid.UUID{c.PromotionID})
	if err != nil {
		return out, err
	}
	if len(ps) == 0 {
		out.Reason = "the promotion of this code is not available"
		return out, nil
	}
	p := ps[0]
	v := viewOf(p)
	out.Promotion = &v
	e := &evaluation{ctx: ctx, q: q, pc: PromoContext{Property: property, At: now, Channel: nonEmpty(in.Channel, "website"), BusinessLine: in.BusinessLine,
		CustomerID: in.CustomerID}, places: places(currencyOf(ctx, q))}
	e.facts.local = now.In(loc)
	e.facts.day = e.facts.local.Format("2006-01-02")
	e.facts.weekday = isoWeekday(e.facts.local)
	switch {
	case p.Status != PromoActive:
		out.Reason = "the promotion of this code is not active"
	case p.ValidFrom != nil && e.facts.day < *p.ValidFrom, p.ValidTo != nil && e.facts.day > *p.ValidTo:
		out.Reason = "the promotion is outside its validity period"
	case len(p.Channels) > 0 && !slices.Contains(p.Channels, e.pc.Channel):
		out.Reason = "not valid in this channel"
	case in.BusinessLine != "" && len(p.BusinessLines) > 0 && !slices.Contains(p.BusinessLines, in.BusinessLine):
		out.Reason = "not valid for this purchase"
	}
	if out.Reason == "" {
		if r, err := e.codeProblem(c); err != nil {
			return out, err
		} else if r != "" {
			out.Reason = r
		}
	}
	if out.Reason == "" && p.MinPurchase != nil && in.Amount != "" {
		if a, err := decimal.NewFromString(in.Amount); err == nil && a.LessThan(dec(*p.MinPurchase)) {
			out.Reason = "minimum purchase " + dec(*p.MinPurchase).String()
		}
	}
	if c.MaxUses != nil {
		n, _, err := e.usage(c.PromotionID, &c.ID, nil)
		if err != nil {
			return out, err
		}
		left := max(*c.MaxUses-n, 0)
		out.UsesLeft = &left
	}
	out.Valid = out.Reason == ""
	return out, nil
}

// codeChecks limits public promo code checks per client and property to
// the Promotion Policies "code checks per minute" (FR-PRM-04, FR-WEB-P3-07).
var codeChecks = &checkLimiter{hits: map[string]*checkWindow{}}

type checkWindow struct {
	start time.Time
	n     int
}

type checkLimiter struct {
	mu   sync.Mutex
	hits map[string]*checkWindow
}

// allow records one hit for key and reports whether it is within n per minute.
func (l *checkLimiter) allow(key string, n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w := l.hits[key]
	if w == nil || now.Sub(w.start) > time.Minute {
		if len(l.hits) > 10000 {
			l.hits = map[string]*checkWindow{}
		}
		l.hits[key] = &checkWindow{start: now, n: 1}
		return n > 0
	}
	w.n++
	return w.n <= n
}

// PublicCodeCheckInput is the website checkout promo code check.
type PublicCodeCheckInput struct {
	PropertyID   uuid.UUID `json:"propertyId"`
	Code         string    `json:"code"`
	Amount       string    `json:"amount,omitempty"`
	BusinessLine string    `json:"businessLine,omitempty"`
}

// ── member app Offers (FR-APP-P3-02) ──────────────────────────────────────

// PersonalCode is a promo code personal to the signed-in member.
type PersonalCode struct {
	Code          string     `json:"code" db:"code"`
	PromotionCode string     `json:"promotionCode" db:"promotion_code"`
	PromotionName string     `json:"promotionName" db:"promotion_name"`
	ExpiresAt     *time.Time `json:"expiresAt" db:"expires_at"`
	MaxUses       *int       `json:"maxUses" db:"max_uses"`
	UsedCount     int        `json:"usedCount" db:"used_count"`
}

// Offers are the promotions, personal codes and packages for a member.
type Offers struct {
	Promotions []PromotionView `json:"promotions"`
	PromoCodes []PersonalCode  `json:"promoCodes"`
	Packages   []PackageOffer  `json:"packages"`
}

// MemberOffers returns the offers a customer is eligible for.
func MemberOffers(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (Offers, error) {
	out := Offers{Promotions: []PromotionView{}, PromoCodes: []PersonalCode{}, Packages: []PackageOffer{}}
	loc := calendar.Location(ctx, q)
	now := clock.Now().In(loc)
	codes, err := handle.List[PersonalCode](q.Query(ctx, `SELECT c.code, p.code AS promotion_code, p.name AS promotion_name, c.expires_at, c.max_uses, c.used_count
		FROM commercial.promo_codes c JOIN commercial.promotions p ON p.id = c.promotion_id
		WHERE c.property_id = $1 AND c.customer_id = $2 AND c.status = 'active' AND p.status = 'active' AND p.archived_at IS NULL
		AND (c.expires_at IS NULL OR c.expires_at > now()) AND (c.max_uses IS NULL OR c.used_count < c.max_uses) ORDER BY c.expires_at NULLS LAST, c.code`,
		property, customer))
	if err != nil {
		return out, err
	}
	out.PromoCodes = codes
	personal := map[string]bool{}
	for _, c := range codes {
		personal[c.PromotionCode] = true
	}
	segment, err := customerSegment(ctx, q, property, &customer)
	if err != nil {
		return out, err
	}
	ps, err := ActivePromotions(ctx, q, property, now, "member_app", "", false)
	if err != nil {
		return out, err
	}
	e := &evaluation{ctx: ctx, q: q, pc: PromoContext{Property: property, CustomerID: &customer, Segment: segment}}
	for _, p := range ps {
		if p.RequiresCode && !personal[p.Code] {
			continue
		}
		if !p.Public && !personal[p.Code] && len(p.Segments) == 0 && len(p.MembershipTypes) == 0 && len(p.CustomerSegmentIDs) == 0 {
			continue
		}
		if len(p.Segments) > 0 && !slices.Contains(p.Segments, segment) {
			continue
		}
		if len(p.MembershipTypes) > 0 || len(p.CustomerSegmentIDs) > 0 {
			if err := e.customerFacts(); err != nil {
				return out, err
			}
			if len(p.MembershipTypes) > 0 && !overlapsAny(e.memTypes, p.MembershipTypes) {
				continue
			}
			if len(p.CustomerSegmentIDs) > 0 && !overlapsAny(e.crmSegs, p.CustomerSegmentIDs) {
				continue
			}
		}
		out.Promotions = append(out.Promotions, viewOf(p))
	}
	pk, err := PackageOffers(ctx, q, property, "member_app", segment)
	if err != nil {
		return out, err
	}
	out.Packages = pk
	return out, nil
}

// ── routes ────────────────────────────────────────────────────────────────

func (m *P3Module) registerPromotions(reg routeAdder, eng *resource.Engine) {
	db := m.DB
	add := func(rt routeSpec) { reg.add(rt, "Promotions") }
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promotions/{id}:activate", summary: "Activate Promotion (Promotion Activation approval when required)",
		perm: "commercial.promotion.activate", req: handle.Empty{}, res: PromotionState{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PromotionState, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PromotionState{}, err
			}
			return m.ActivatePromotion(ctx, tx, pid)
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promotions/{id}:approve", summary: "Approve the pending activation of a promotion",
		perm: "commercial.promotion.approve", req: PromotionReasonInput{}, res: PromotionState{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PromotionReasonInput) (PromotionState, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PromotionState{}, err
			}
			return m.ApprovePromotion(ctx, tx, pid, in.Reason)
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promotions/{id}:deactivate", summary: "Deactivate Promotion",
		perm: "commercial.promotion.activate", req: PromotionReasonInput{}, res: PromotionState{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PromotionReasonInput) (PromotionState, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PromotionState{}, err
			}
			return m.DeactivatePromotion(ctx, tx, pid, in.Reason)
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promotions/{id}:simulate", summary: "Simulate a promotion on example sales before it is activated",
		perm: "commercial.promotion.view", req: SimulateInput{}, res: Simulation{}, status: http.StatusOK, noAudit: "read-only simulation",
		h: readPost(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, in SimulateInput) (Simulation, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return Simulation{}, err
			}
			if _, err := promotionState(ctx, tx, pid); err != nil {
				return Simulation{}, err
			}
			if len(in.Scenarios) == 0 || len(in.Scenarios) > 50 {
				return Simulation{}, handle.Invalid("scenarios", "required", "1 to 50 scenarios")
			}
			out := Simulation{PromotionID: pid, Scenarios: []Evaluation{}}
			for _, sc := range in.Scenarios {
				pc, err := sc.context(handle.Property(ctx))
				if err != nil {
					return out, err
				}
				pc.Only = []uuid.UUID{pid}
				res, err := EvaluatePromotions(ctx, tx, pc)
				if err != nil {
					return out, err
				}
				out.Scenarios = append(out.Scenarios, evaluationOf(sc.Label, res))
			}
			return out, nil
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promotions:evaluate", summary: "Price a cart with the Active promotions (booking, quotation, website checkout)",
		perm: "commercial.promotion.view", req: EvaluateInput{}, res: Evaluation{}, status: http.StatusOK, noAudit: "read-only evaluation",
		h: readPost(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, in EvaluateInput) (Evaluation, error) {
			pc, err := in.context(handle.Property(ctx))
			if err != nil {
				return Evaluation{}, err
			}
			res, err := EvaluatePromotions(ctx, tx, pc)
			return evaluationOf(in.Label, res), err
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/promotions/{id}/redemptions", summary: "Promotion usage (redemption ledger)",
		perm: "commercial.promotion.view", res: Redemption{}, list: true,
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Redemption], error) {
			pid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[Redemption]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Redemption](tx.Query(ctx, redemptionSelect+` WHERE promotion_id = $1 AND ($2 = '' OR status = $2)
				ORDER BY created_at DESC LIMIT $3`, pid, lp.Filters["status"], lp.Limit)))
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promo-codes:generate", summary: "Generate unique promo codes (bulk or one per customer)",
		perm: "commercial.promo_code.create", req: PromoCodeGenerateInput{}, res: GeneratedCodes{}, status: http.StatusCreated,
		h: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PromoCodeGenerateInput) (GeneratedCodes, error) {
			return m.GenerateCodes(ctx, tx, handle.Property(ctx), in)
		})})
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/promo-codes:check", summary: "Check a promo code (never redeems)",
		perm: "commercial.promo_code.view", req: CodeCheckInput{}, res: CodeCheck{}, status: http.StatusOK, noAudit: "read-only check",
		h: readPost(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CodeCheckInput) (CodeCheck, error) {
			return CheckCode(ctx, tx, handle.Property(ctx), in)
		})})
	// Public (website K5) and member app
	reg.public(routeSpec{method: http.MethodGet, path: "/api/v1/public/promotions", summary: "Active promotions for the website (K5)", res: PromotionView{}, list: true,
		query: []queryParam{{name: "propertyId", required: true}, {name: "businessLine"}},
		h: m.publicRead(func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (any, error) {
			loc := calendar.Location(ctx, tx)
			ps, err := ActivePromotions(ctx, tx, pid, clock.Now().In(loc), "website", r.URL.Query().Get("businessLine"), true)
			if err != nil {
				return nil, err
			}
			out := []PromotionView{}
			for _, p := range ps {
				// codes and member-only promotions are offered personally (member app Offers)
				if p.RequiresCode || len(p.Segments) > 0 || len(p.MembershipTypes) > 0 || len(p.CustomerSegmentIDs) > 0 {
					continue
				}
				out = append(out, viewOf(p))
			}
			return httpx.Page[PromotionView]{Items: out}, nil
		})})
	reg.public(routeSpec{method: http.MethodPost, path: "/api/v1/public/promo-codes:check", summary: "Check a promo code at the website checkout (rate-limited)",
		req: PublicCodeCheckInput{}, res: CodeCheck{}, status: http.StatusOK, noAudit: "read-only check",
		h: func(w http.ResponseWriter, r *http.Request) {
			var in PublicCodeCheckInput
			if err := httpx.Decode(r, &in); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if in.PropertyID == uuid.Nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), in.PropertyID)
			var out CodeCheck
			limited := false
			err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
				pol, _, err := LoadPromotionPolicy(ctx, tx, in.PropertyID)
				if err != nil {
					return err
				}
				if !codeChecks.allow(httpx.ClientIP(r)+" "+in.PropertyID.String(), pol.CodeCheckPerMinute) {
					limited = true
					return nil
				}
				out, err = CheckCode(ctx, tx, in.PropertyID, CodeCheckInput{Code: in.Code, Amount: in.Amount, Channel: "website", BusinessLine: in.BusinessLine})
				return err
			})
			if err == nil && limited {
				err = errs.RateLimited()
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if !out.Valid {
				out.UsesLeft = nil
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	reg.member(routeSpec{method: http.MethodGet, path: "/api/v1/member/offers", summary: "Offers: promotions, personal promo codes and packages for me",
		res: Offers{}, h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Offers, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return Offers{}, err
			}
			return MemberOffers(ctx, tx, c.PropertyID, c.ID)
		})})
	_ = eng
}
