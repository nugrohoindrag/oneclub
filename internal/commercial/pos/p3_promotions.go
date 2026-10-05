package pos

// PRD P3 promotions at the POS (FR-PRM-06, FR-PRM-09, FR-OPS-P3-03): the
// promotion engine prices the open lines of an order after every change
// (items added, voided, discounted, promo code entered, promotion removed);
// the cashier sees the promotions per line, enters promo codes and — with
// commercial.pos.promotion_override — removes a promotion from the sale.
// The order holds Applied redemptions until it is paid (Redeemed, K3) or
// voided / refunded (Reversed). Offline terminals price with the promotion
// cache of their shift and the client time of the sale; at sync the server
// evaluates the same rules at that time and flags a different total for
// review.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// promoOrder is the order header the promotion engine needs.
type promoOrder struct {
	ID              uuid.UUID  `db:"id"`
	PropertyID      uuid.UUID  `db:"property_id"`
	OrderNo         string     `db:"order_no"`
	OutletID        uuid.UUID  `db:"outlet_id"`
	CustomerID      *uuid.UUID `db:"customer_id"`
	Source          string     `db:"source"`
	MemberPricing   bool       `db:"member_pricing"`
	Offline         bool       `db:"offline"`
	ClientCreatedAt *time.Time `db:"client_created_at"`
	PromoCodes      []string   `db:"promo_codes"`
	Exclusions      []string   `db:"promotion_exclusions"`
	Status          string     `db:"status"`
}

// promoLine is an order line as the promotion engine prices it.
type promoLine struct {
	ID                uuid.UUID  `db:"id"`
	ProductID         uuid.UUID  `db:"product_id"`
	Code              string     `db:"code"`
	Category          *string    `db:"category"`
	ProductType       string     `db:"product_type"`
	Quantity          string     `db:"quantity"`
	UnitPrice         string     `db:"unit_price"`
	Discount          string     `db:"discount_amount"`
	DiscountReason    *string    `db:"discount_reason"`
	PromotionDiscount string     `db:"promotion_discount"`
	ChargedFolioID    *uuid.UUID `db:"charged_folio_id"`
}

// promotionContext is the evaluation context of an order (also used for the
// offline sync check).
func promotionContext(o promoOrder, ou outlet, lines []promoLine) commercial.PromoContext {
	at := clock.Now()
	if o.Offline && o.ClientCreatedAt != nil {
		at = *o.ClientCreatedAt // the client time of an offline sale (FR-PRM-09)
	}
	seg := "non_member"
	if o.MemberPricing {
		seg = "member"
	}
	pc := commercial.PromoContext{Property: o.PropertyID, At: at, Channel: commercial.PromoChannelOf(o.Source), BusinessLine: "pos", OutletID: &ou.ID,
		CustomerID: o.CustomerID, Segment: seg, Codes: o.PromoCodes, Source: &commercial.PromoSource{Type: commercial.SourcePOSOrder, ID: o.ID}}
	for _, x := range o.Exclusions {
		if pid, err := uuid.Parse(x); err == nil {
			pc.Exclude = append(pc.Exclude, pid)
		}
	}
	for _, l := range lines {
		qty, _ := decimal.NewFromString(l.Quantity)
		unit, _ := decimal.NewFromString(l.UnitPrice)
		manual, _ := decimal.NewFromString(l.Discount)
		if !qty.IsPositive() {
			continue
		}
		pid := l.ProductID
		cat := ""
		if l.Category != nil {
			cat = *l.Category
		}
		pc.Lines = append(pc.Lines, commercial.PromoLine{Key: l.ID.String(), ProductID: &pid, Category: cat, ProductType: l.ProductType,
			ServiceType: "pos", ItemRef: l.Code, Quantity: qty, UnitPrice: unit.Mul(qty).Sub(manual).Div(qty)})
	}
	return pc
}

func loadPromoOrder(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (promoOrder, []promoLine, error) {
	rows, err := q.Query(ctx, `SELECT id, property_id, order_no, outlet_id, customer_id, source, member_pricing, offline, client_created_at, promo_codes,
		promotion_exclusions, status FROM commercial.orders WHERE id = $1`, oid)
	o, err := handle.One[promoOrder](rows, err, "order")
	if err != nil {
		return o, nil, err
	}
	lines, err := handle.List[promoLine](q.Query(ctx, `SELECT l.id, l.product_id, p.code, p.category, p.product_type, l.quantity::text AS quantity,
		l.unit_price::text AS unit_price, l.discount_amount::text AS discount_amount, l.discount_reason, l.promotion_discount::text AS promotion_discount,
		l.charged_folio_id FROM commercial.order_lines l JOIN commercial.products p ON p.id = l.product_id
		WHERE l.order_id = $1 AND l.status = 'active' AND l.charged_folio_id IS NULL ORDER BY l.line_no`, oid))
	return o, lines, err
}

// applyPromotions prices the open lines of an order with the promotion
// engine and replaces the order's Applied redemptions; lines without a
// promotion before and after keep their P2 price untouched.
func (m *Module) applyPromotions(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (commercial.PromoResult, error) {
	o, lines, err := loadPromoOrder(ctx, tx, oid)
	if err != nil || o.Status != "open" {
		return commercial.PromoResult{}, err
	}
	ou, err := loadOutlet(ctx, tx, o.OutletID)
	if err != nil {
		return commercial.PromoResult{}, err
	}
	res, err := commercial.EvaluatePromotions(ctx, tx, promotionContext(o, ou, lines))
	if err != nil {
		return res, err
	}
	for _, l := range lines {
		d := res.DiscountOf(l.ID.String())
		before, _ := decimal.NewFromString(l.PromotionDiscount)
		if !d.IsPositive() && !before.IsPositive() {
			continue
		}
		applied := res.AppliedTo(l.ID.String())
		pr, err := loadProduct(ctx, tx, l.ProductID)
		if err != nil {
			return res, err
		}
		qty, _ := decimal.NewFromString(l.Quantity)
		unit, _ := decimal.NewFromString(l.UnitPrice)
		manual, _ := decimal.NewFromString(l.Discount)
		net, svc, tax, total, snap, err := m.pricePromotionLine(ctx, tx, o, ou, pr, unit, qty, manual, d, applied, l.DiscountReason)
		if err != nil {
			return res, err
		}
		var first *uuid.UUID
		if len(applied) > 0 {
			first = &applied[0].PromotionID
		}
		raw, _ := json.Marshal(nonNilApplied(applied))
		if _, err := tx.Exec(ctx, `UPDATE commercial.order_lines SET promotion_discount = $2::numeric, promotions = $3, promotion_id = $4, net_amount = $5::numeric,
			service_amount = $6::numeric, tax_amount = $7::numeric, total_amount = $8::numeric, pricing_snapshot_id = $9 WHERE id = $1`,
			l.ID, d.String(), raw, first, net.String(), svc.String(), tax.String(), total.String(), snap); err != nil {
			return res, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET promotion_at = $2 WHERE id = $1`, oid, res.EvaluatedAt); err != nil {
		return res, err
	}
	err = commercial.ReplaceRedemptions(ctx, tx, commercial.RedemptionInput{Property: o.PropertyID, Source: commercial.PromoSource{Type: commercial.SourcePOSOrder,
		ID: oid}, SourceRef: o.OrderNo, CustomerID: o.CustomerID, Channel: commercial.PromoChannelOf(o.Source), BusinessLine: "pos", Offline: o.Offline},
		res.Applied)
	return res, err
}

func nonNilApplied(a []commercial.AppliedPromotion) []commercial.AppliedPromotion {
	if a == nil {
		return []commercial.AppliedPromotion{}
	}
	return a
}

// pricePromotionLine prices a line after its manual discount and promotion
// discount, the snapshot recording the promotions (FR-PRM-07).
func (m *Module) pricePromotionLine(ctx context.Context, tx pgx.Tx, o promoOrder, ou outlet, pr product, unit, qty, manual, promo decimal.Decimal,
	applied []commercial.AppliedPromotion, reason *string) (net, svc, tax, total decimal.Decimal, snap *uuid.UUID, err error) {
	gross := unit.Mul(qty).Sub(manual).Sub(promo)
	if gross.IsNegative() {
		gross = decimal.Zero
	}
	seg := "any"
	if o.MemberPricing {
		seg = "member"
	}
	var override map[string]any
	if manual.IsPositive() {
		override = map[string]any{"discount": manual.String()}
		if reason != nil {
			override["reason"] = *reason
		}
	}
	sid, b, err := commercial.Pricer{}.LineSnapshot(ctx, tx, o.PropertyID, commercial.LineSnapshotInput{ServiceType: "pos", ItemRef: pr.Code, Segment: seg,
		Units: qty, UnitPrice: gross.Div(qty), ListPrice: unit, Mode: ou.PricingMode, TaxCodes: ou.TaxCodes, At: clock.Now(), Override: override,
		Promotions: applied, Channel: o.Source})
	if err != nil {
		return net, svc, tax, total, nil, err
	}
	net, _ = decimal.NewFromString(b.NetAmount)
	total, _ = decimal.NewFromString(b.Total)
	return net, commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax"), total, &sid, nil
}

// roleCodes are the role codes of the signed-in user at a property.
func roleCodes(ctx context.Context, property uuid.UUID) []string {
	p := authz.From(ctx)
	if p == nil {
		return nil
	}
	var out []string
	for _, a := range p.Assignments {
		if a.PropertyID == nil || *a.PropertyID == property {
			out = append(out, a.RoleCode)
		}
	}
	return out
}

// setOrderPromo stores the promo codes and the offline client total of a
// new order.
func setOrderPromo(ctx context.Context, tx pgx.Tx, oid uuid.UUID, in OrderInput) error {
	codes := []string{}
	for _, c := range in.PromoCodes {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" && !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	var client *string
	if strings.TrimSpace(in.ClientTotal) != "" {
		v, err := handle.Decimal("clientTotal", in.ClientTotal, decimal.Zero)
		if err != nil {
			return err
		}
		s := v.String()
		client = &s
	}
	excl := []string{}
	for _, x := range in.PromotionExclusions {
		excl = append(excl, x.String())
	}
	if len(excl) > 0 {
		if err := authz.RequireAt(ctx, "commercial.pos.promotion_override", handle.Property(ctx)); err != nil {
			return errs.Forbidden("removing a promotion needs commercial.pos.promotion_override")
		}
	}
	if len(codes) == 0 && client == nil && len(excl) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE commercial.orders SET promo_codes = $2, client_total = $3::numeric, promotion_exclusions = $4 WHERE id = $1`,
		oid, codes, client, excl)
	return err
}

// checkOfflineTotal compares the total an offline terminal computed with
// its cached promotions to the server total (FR-PRM-09): a difference above
// the Promotion Policies tolerance flags the order and its redemptions for
// review.
func (m *Module) checkOfflineTotal(ctx context.Context, tx pgx.Tx, oid uuid.UUID, property uuid.UUID) error {
	var client *string
	var server string
	var offline bool
	if err := tx.QueryRow(ctx, `SELECT client_total::text, offline, coalesce((SELECT sum(total_amount) FROM commercial.order_lines l WHERE l.order_id = o.id
		AND l.status = 'active'), 0)::text FROM commercial.orders o WHERE id = $1`, oid).Scan(&client, &offline, &server); err != nil {
		return err
	}
	if !offline || client == nil {
		return nil
	}
	pol, _, err := commercial.LoadPromotionPolicy(ctx, tx, property)
	if err != nil {
		return err
	}
	c, _ := decimal.NewFromString(*client)
	s, _ := decimal.NewFromString(server)
	tol, _ := decimal.NewFromString(pol.OfflineTolerance)
	if c.Sub(s).Abs().LessThanOrEqual(tol) {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET promotion_mismatch = true, needs_review = true WHERE id = $1`, oid); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE commercial.promotion_redemptions SET needs_review = true WHERE source_type = $1 AND source_id = $2`,
		commercial.SourcePOSOrder, oid)
	return err
}

// orderPromotions loads the redemptions of an order for its view.
func orderPromotions(ctx context.Context, q dbtx.Querier, o *Order) error {
	r, err := commercial.Redemptions(ctx, q, commercial.SourcePOSOrder, o.ID)
	if err != nil {
		return err
	}
	o.Promotions = r
	if o.PromoCodes == nil {
		o.PromoCodes = []string{}
	}
	if o.PromotionExclusions == nil {
		o.PromotionExclusions = []string{}
	}
	for i := range o.Lines {
		if o.Lines[i].Promotions == nil {
			o.Lines[i].Promotions = []commercial.AppliedPromotion{}
		}
	}
	return nil
}

// ── routes (FR-OPS-P3-03) ─────────────────────────────────────────────────

// ApplyPromotionInput enters a promo code or restores a removed promotion.
type ApplyPromotionInput struct {
	PromoCode   string     `json:"promoCode,omitempty" doc:"Promo code entered by the cashier"`
	PromotionID *uuid.UUID `json:"promotionId,omitempty" doc:"Restore a promotion removed from this sale"`
}

// RemovePromotionInput removes a promotion from a sale (permission
// commercial.pos.promotion_override).
type RemovePromotionInput struct {
	PromotionID uuid.UUID `json:"promotionId"`
	Reason      string    `json:"reason"`
}

// OrderPromotionResult is an order after its promotions were evaluated, with
// the codes or promotions that did not apply.
type OrderPromotionResult struct {
	Order    Order                          `json:"order"`
	Rejected []commercial.RejectedPromotion `json:"rejected"`
}

// POSPromotionCache is the promotion cache of an offline POS terminal for a
// shift (FR-PRM-09): the Active POS promotions of the outlet valid today,
// the general promo codes and the Promotion Policies the terminal applies.
type POSPromotionCache struct {
	OutletID          uuid.UUID                  `json:"outletId"`
	GeneratedAt       time.Time                  `json:"generatedAt"`
	ValidUntil        time.Time                  `json:"validUntil"`
	Timezone          string                     `json:"timezone"`
	Currency          string                     `json:"currency"`
	Selection         string                     `json:"selection" enum:"priority,best_price"`
	MaxStackedPercent string                     `json:"maxStackedPercent"`
	OfflineTolerance  string                     `json:"offlineTolerance"`
	Promotions        []commercial.PromotionRule `json:"promotions"`
	PromoCodes        []POSCachedCode            `json:"promoCodes"`
}

// POSCachedCode is a general promo code an offline terminal may accept.
type POSCachedCode struct {
	Code        string     `json:"code" db:"code"`
	PromotionID uuid.UUID  `json:"promotionId" db:"promotion_id"`
	ExpiresAt   *time.Time `json:"expiresAt" db:"expires_at"`
}

func (m *Module) promotionResult(ctx context.Context, tx pgx.Tx, oid uuid.UUID, res commercial.PromoResult) (OrderPromotionResult, error) {
	o, err := m.Order(ctx, tx, oid)
	if err != nil {
		return OrderPromotionResult{}, err
	}
	rej := res.Rejected
	if rej == nil {
		rej = []commercial.RejectedPromotion{}
	}
	return OrderPromotionResult{Order: o, Rejected: rej}, nil
}

func (m *Module) registerPromotions(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "commercial", "POS", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:apply-promotion", Summary: "Apply Promotion: promo code or a removed promotion again",
		Permission: "commercial.pos.promotion", Request: ApplyPromotionInput{}, Response: OrderPromotionResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ApplyPromotionInput) (OrderPromotionResult, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			o, err := m.lockOrder(ctx, tx, oid)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			if o.Status != "open" {
				return OrderPromotionResult{}, errs.Conflict("order_closed", "order "+o.OrderNo+" is "+o.Status)
			}
			code := strings.ToUpper(strings.TrimSpace(in.PromoCode))
			if code == "" && in.PromotionID == nil {
				return OrderPromotionResult{}, handle.Invalid("promoCode", "required", "enter a promo code or choose a removed promotion")
			}
			if code != "" && !slices.Contains(o.PromoCodes, code) {
				if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET promo_codes = array_append(promo_codes, $2) WHERE id = $1`, oid, code); err != nil {
					return OrderPromotionResult{}, err
				}
			}
			if in.PromotionID != nil {
				if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET promotion_exclusions = array_remove(promotion_exclusions, $2) WHERE id = $1`,
					oid, in.PromotionID.String()); err != nil {
					return OrderPromotionResult{}, err
				}
			}
			res, err := m.applyPromotions(ctx, tx, oid)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			out, err := m.promotionResult(ctx, tx, oid, res)
			if err != nil {
				return out, err
			}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "apply_promotion", EntityType: "commercial.order", EntityID: oid.String(),
				EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, After: map[string]any{"promoCode": code, "promotionId": in.PromotionID,
					"discount": res.Discount, "applied": len(res.Applied), "rejected": out.Rejected}})
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/orders/{id}:remove-promotion", Summary: "Remove a promotion from the sale (supervisor permission)",
		Permission: "commercial.pos.promotion_override", Request: RemovePromotionInput{}, Response: OrderPromotionResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RemovePromotionInput) (OrderPromotionResult, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			if strings.TrimSpace(in.Reason) == "" {
				return OrderPromotionResult{}, handle.Invalid("reason", "required", "a reason is required to remove a promotion")
			}
			o, err := m.lockOrder(ctx, tx, oid)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			if o.Status != "open" {
				return OrderPromotionResult{}, errs.Conflict("order_closed", "order "+o.OrderNo+" is "+o.Status)
			}
			if !slices.Contains(o.PromotionExclusions, in.PromotionID.String()) {
				if _, err := tx.Exec(ctx, `UPDATE commercial.orders SET promotion_exclusions = array_append(promotion_exclusions, $2) WHERE id = $1`,
					oid, in.PromotionID.String()); err != nil {
					return OrderPromotionResult{}, err
				}
			}
			res, err := m.applyPromotions(ctx, tx, oid)
			if err != nil {
				return OrderPromotionResult{}, err
			}
			out, err := m.promotionResult(ctx, tx, oid, res)
			if err != nil {
				return out, err
			}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "remove_promotion", EntityType: "commercial.order", EntityID: oid.String(),
				EntityLabel: o.OrderNo, PropertyID: &o.PropertyID, Reason: in.Reason, After: map[string]any{"promotionId": in.PromotionID,
					"discount": res.Discount}})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/commercial/pos/promotions", Summary: "Promotion cache of an offline POS terminal (per shift)",
		Permission: "commercial.order.view", Response: POSPromotionCache{}, Query: []route.Param{{Name: "outletId", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (POSPromotionCache, error) {
			outletID, err := uuid.Parse(r.URL.Query().Get("outletId"))
			if err != nil {
				return POSPromotionCache{}, handle.Invalid("outletId", "required", "outlet id")
			}
			ou, err := loadOutlet(ctx, tx, outletID)
			if err != nil {
				return POSPromotionCache{}, err
			}
			property := handle.Property(ctx)
			pol, _, err := commercial.LoadPromotionPolicy(ctx, tx, property)
			if err != nil {
				return POSPromotionCache{}, err
			}
			loc := calendar.Location(ctx, tx)
			now := clock.Now()
			all, err := commercial.ActivePromotions(ctx, tx, property, now.In(loc), "pos", "pos", false)
			if err != nil {
				return POSPromotionCache{}, err
			}
			out := POSPromotionCache{OutletID: ou.ID, GeneratedAt: now, ValidUntil: now.Add(time.Duration(max(pol.OfflineCacheHours, 1)) * time.Hour),
				Timezone: loc.String(), Selection: pol.Selection, MaxStackedPercent: pol.MaxStackedPercent, OfflineTolerance: pol.OfflineTolerance,
				Promotions: []commercial.PromotionRule{}}
			_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&out.Currency)
			var ids []uuid.UUID
			for _, p := range all {
				if len(p.OutletIDs) > 0 && !slices.Contains(p.OutletIDs, ou.ID.String()) {
					continue
				}
				out.Promotions = append(out.Promotions, p)
				ids = append(ids, p.ID)
			}
			out.PromoCodes, err = handle.List[POSCachedCode](tx.Query(ctx, `SELECT code, promotion_id, expires_at FROM commercial.promo_codes
				WHERE property_id = $1 AND promotion_id = ANY($2) AND status = 'active' AND customer_id IS NULL AND (expires_at IS NULL OR expires_at > now())
				AND (max_uses IS NULL OR used_count < max_uses) ORDER BY code LIMIT 5000`, property, ids))
			return out, err
		})})
}
