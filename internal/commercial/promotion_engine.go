package commercial

// Promotion engine (FR-PRM-01..05, FR-PRM-09): one deterministic
// evaluation used by the POS (online and offline sync), booking prices
// (Pricer.Resolve, golf Resolve), packages, the website and the member app,
// so a promotion yields the same price in every channel.
//
//  1. candidates: Active promotions of the property valid on the local date
//     (or the promotions being simulated), filtered by channel, business
//     line, outlet, segment, membership type, CRM segment, weekday, time
//     window, day kind, day type, promo code and usage limits;
//  2. each candidate's discount on its eligible lines is computed alone and
//     candidates are ordered by the Promotion Policies selection — priority
//     (lower number first, the best discount breaks ties) or best_price;
//  3. candidates are applied in that order on the remaining line amounts: a
//     non-stackable promotion takes lines no other promotion touched and
//     blocks them; stackable promotions combine on a line when their stack
//     groups match (an empty group stacks with any stackable promotion);
//  4. discounts are rounded to the currency, allocated per line (the last
//     line absorbs the rounding) and capped by the line amount, the budget
//     left and the maximum discount.

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/membership"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// PromoLine is one priced line of a sale.
type PromoLine struct {
	Key         string          `json:"key" doc:"Caller's line reference (order line id, index)"`
	ProductID   *uuid.UUID      `json:"productId,omitempty"`
	Category    string          `json:"category,omitempty"`
	ProductType string          `json:"productType,omitempty"`
	ServiceType string          `json:"serviceType,omitempty"`
	ItemRef     string          `json:"itemRef,omitempty"`
	Quantity    decimal.Decimal `json:"quantity"`
	UnitPrice   decimal.Decimal `json:"unitPrice" doc:"Unit price before promotions, in the pricing mode of the line"`
}

// PromoSource identifies the document a promotion is evaluated for; its own
// earlier redemptions do not count against the usage limits.
type PromoSource struct {
	Type string
	ID   uuid.UUID
}

// PromoContext describes a sale being priced.
type PromoContext struct {
	Property     uuid.UUID
	At           time.Time // when the service / sale happens (client time of an offline sale)
	Channel      string    // pos | member_app | website | back_office | ops
	BusinessLine string    // golf | sportclub | stay | pos | banquet | package …
	OutletID     *uuid.UUID
	CustomerID   *uuid.UUID
	Segment      string   // pricing segment of the sale
	Codes        []string // promo codes entered
	Exclude      []uuid.UUID
	Peak         *bool    // known peak flag (golf tee time); else the Pricing Policies decide
	DayTypeCodes []string // known day type (golf); else computed from the day types
	Lines        []PromoLine
	Only         []uuid.UUID // simulation: evaluate only these promotions, any status
	Source       *PromoSource
	Currency     string
}

// LineDiscount is the discount of one line.
type LineDiscount struct {
	Key      string `json:"key"`
	Discount string `json:"discount"`
}

// AppliedPromotion is one promotion applied to a sale.
type AppliedPromotion struct {
	PromotionID uuid.UUID      `json:"promotionId"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	PromoType   string         `json:"promoType"`
	Version     int            `json:"version"`
	PromoCodeID *uuid.UUID     `json:"promoCodeId,omitempty"`
	PromoCode   string         `json:"promoCode,omitempty"`
	Stackable   bool           `json:"stackable"`
	Priority    int            `json:"priority"`
	Discount    string         `json:"discount"`
	Lines       []LineDiscount `json:"lines"`
}

// RejectedPromotion explains why an entered code or a promotion did not apply.
type RejectedPromotion struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// PromoResult is the outcome of an evaluation.
type PromoResult struct {
	Currency    string              `json:"currency"`
	Subtotal    string              `json:"subtotal"`
	Discount    string              `json:"discount"`
	Applied     []AppliedPromotion  `json:"applied"`
	Lines       []LineDiscount      `json:"lines"`
	Rejected    []RejectedPromotion `json:"rejected"`
	Selection   string              `json:"selection" enum:"priority,best_price"`
	EvaluatedAt time.Time           `json:"evaluatedAt"`
}

// DiscountOf returns the total discount of a line key.
func (r PromoResult) DiscountOf(key string) decimal.Decimal {
	for _, l := range r.Lines {
		if l.Key == key {
			return dec(l.Discount)
		}
	}
	return decimal.Zero
}

// AppliedTo returns the promotions applied to a line with their discount.
func (r PromoResult) AppliedTo(key string) []AppliedPromotion {
	var out []AppliedPromotion
	for _, a := range r.Applied {
		for _, l := range a.Lines {
			if l.Key == key && dec(l.Discount).IsPositive() {
				x := a
				x.Discount = l.Discount
				x.Lines = nil
				out = append(out, x)
			}
		}
	}
	return out
}

// TotalDiscount is the discount of the whole sale.
func (r PromoResult) TotalDiscount() decimal.Decimal { return dec(r.Discount) }

// PromotionRule is a promotion as the engine reads it (also the POS offline
// promotion cache, FR-PRM-09).
type PromotionRule struct {
	ID                 uuid.UUID    `db:"id" json:"id"`
	Code               string       `db:"code" json:"code"`
	Name               string       `db:"name" json:"name"`
	Description        *string      `db:"description" json:"description"`
	PromoType          string       `db:"promo_type" json:"promoType"`
	DiscountPercent    *string      `db:"discount_percent" json:"discountPercent"`
	DiscountAmount     *string      `db:"discount_amount" json:"discountAmount"`
	PerUnit            bool         `db:"per_unit" json:"perUnit"`
	MaxDiscount        *string      `db:"max_discount" json:"maxDiscount"`
	BuyQuantity        *int         `db:"buy_quantity" json:"buyQuantity"`
	GetQuantity        *int         `db:"get_quantity" json:"getQuantity"`
	BundlePrice        *string      `db:"bundle_price" json:"bundlePrice"`
	BundleItems        []BundleItem `db:"bundle_items" json:"bundleItems"`
	Channels           []string     `db:"channels" json:"channels"`
	BusinessLines      []string     `db:"business_lines" json:"businessLines"`
	ServiceTypes       []string     `db:"service_types" json:"serviceTypes"`
	OutletIDs          []string     `db:"outlet_ids" json:"outletIds"`
	ProductIDs         []string     `db:"product_ids" json:"productIds"`
	Categories         []string     `db:"categories" json:"categories"`
	ItemRefs           []string     `db:"item_refs" json:"itemRefs"`
	Segments           []string     `db:"segments" json:"segments"`
	MembershipTypes    []string     `db:"membership_types" json:"membershipTypes"`
	CustomerSegmentIDs []string     `db:"customer_segment_ids" json:"customerSegmentIds"`
	Weekdays           []int32      `db:"weekdays" json:"weekdays"`
	TimeWindows        []TimeWindow `db:"time_windows" json:"timeWindows"`
	DayKinds           []string     `db:"day_kinds" json:"dayKinds"`
	DayTypeCodes       []string     `db:"day_type_codes" json:"dayTypeCodes"`
	ValidFrom          *string      `db:"valid_from" json:"validFrom"`
	ValidTo            *string      `db:"valid_to" json:"validTo"`
	MinPurchase        *string      `db:"min_purchase" json:"minPurchase"`
	MinQuantity        *int         `db:"min_quantity" json:"minQuantity"`
	RequiresCode       bool         `db:"requires_code" json:"requiresCode"`
	Public             bool         `db:"public" json:"public"`
	Stackable          bool         `db:"stackable" json:"stackable"`
	StackGroup         *string      `db:"stack_group" json:"stackGroup"`
	Priority           int          `db:"priority" json:"priority"`
	BudgetAmount       *string      `db:"budget_amount" json:"budgetAmount"`
	MaxRedemptions     *int         `db:"max_redemptions" json:"maxRedemptions"`
	MaxPerCustomer     *int         `db:"max_per_customer" json:"maxPerCustomer"`
	Status             string       `db:"status" json:"status"`
	Version            int          `db:"version" json:"version"`
}

const promoSelect = `SELECT id, code, name, description, promo_type, discount_percent::text AS discount_percent, discount_amount::text AS discount_amount,
	per_unit, max_discount::text AS max_discount, buy_quantity, get_quantity, bundle_price::text AS bundle_price, bundle_items, channels, business_lines,
	service_types, outlet_ids, product_ids, categories, item_refs, segments, membership_types, customer_segment_ids, weekdays, time_windows, day_kinds,
	day_type_codes, to_char(valid_from, 'YYYY-MM-DD') AS valid_from, to_char(valid_to, 'YYYY-MM-DD') AS valid_to, min_purchase::text AS min_purchase,
	min_quantity, requires_code, public, stackable, stack_group, priority, budget_amount::text AS budget_amount, max_redemptions, max_per_customer,
	status, version FROM commercial.promotions`

// loadPromotions returns the candidate promotions valid on a local date.
func loadPromotions(ctx context.Context, q dbtx.Querier, property uuid.UUID, day string, only []uuid.UUID) ([]PromotionRule, error) {
	if len(only) > 0 {
		return handle.List[PromotionRule](q.Query(ctx, promoSelect+` WHERE property_id = $1 AND id = ANY($2) AND archived_at IS NULL
			ORDER BY priority, code`, property, only))
	}
	return handle.List[PromotionRule](q.Query(ctx, promoSelect+` WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND (valid_from IS NULL OR valid_from <= $2::date) AND (valid_to IS NULL OR valid_to >= $2::date) ORDER BY priority, code`, property, day))
}

// codeRow is an entered promo code.
type codeRow struct {
	ID                 uuid.UUID  `db:"id"`
	PromotionID        uuid.UUID  `db:"promotion_id"`
	Code               string     `db:"code"`
	CustomerID         *uuid.UUID `db:"customer_id"`
	MaxUses            *int       `db:"max_uses"`
	MaxUsesPerCustomer *int       `db:"max_uses_per_customer"`
	ExpiresAt          *time.Time `db:"expires_at"`
	Status             string     `db:"status"`
}

// dayFacts are the calendar facts of the sale time, computed once.
type dayFacts struct {
	local    time.Time
	day      string
	weekday  int
	hhmm     string
	holiday  bool
	peak     bool
	dayTypes []string
	computed bool
}

// lineState is a line during evaluation.
type lineState struct {
	line      PromoLine
	gross     decimal.Decimal // unit price × quantity
	remaining decimal.Decimal
	applied   []*PromotionRule
}

type evaluation struct {
	ctx      context.Context
	q        dbtx.Querier
	pc       PromoContext
	places   int32
	facts    dayFacts
	memTypes []string
	crmSegs  []string
	custDone bool
}

func normCodes(codes []string) []string {
	var out []string
	for _, c := range codes {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

func currencyOf(ctx context.Context, q dbtx.Querier) string {
	var cur string
	if err := q.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur); err != nil || cur == "" {
		return "IDR"
	}
	return cur
}

// EvaluatePromotions evaluates the promotions of a sale (read-only).
func EvaluatePromotions(ctx context.Context, q dbtx.Querier, pc PromoContext) (PromoResult, error) {
	if pc.At.IsZero() {
		pc.At = clock.Now()
	}
	if pc.Currency == "" {
		pc.Currency = currencyOf(ctx, q)
	}
	pc.Codes = normCodes(pc.Codes)
	pol, _, err := LoadPromotionPolicy(ctx, q, pc.Property)
	if err != nil {
		return PromoResult{}, err
	}
	e := &evaluation{ctx: ctx, q: q, pc: pc, places: places(pc.Currency)}
	loc := calendar.Location(ctx, q)
	e.facts.local = pc.At.In(loc)
	e.facts.day = e.facts.local.Format("2006-01-02")
	e.facts.hhmm = e.facts.local.Format("15:04")
	e.facts.weekday = isoWeekday(e.facts.local)
	out := PromoResult{Currency: pc.Currency, Applied: []AppliedPromotion{}, Lines: []LineDiscount{}, Rejected: []RejectedPromotion{},
		Selection: pol.Selection, EvaluatedAt: pc.At}
	states := make([]*lineState, 0, len(pc.Lines))
	subtotal := decimal.Zero
	for _, l := range pc.Lines {
		g := l.UnitPrice.Mul(l.Quantity).Round(e.places)
		if g.IsNegative() {
			g = decimal.Zero
		}
		states = append(states, &lineState{line: l, gross: g, remaining: g})
		subtotal = subtotal.Add(g)
	}
	out.Subtotal = subtotal.StringFixed(e.places)
	cands, err := loadPromotions(ctx, q, pc.Property, e.facts.day, pc.Only)
	if err != nil {
		return out, err
	}
	candidate := map[uuid.UUID]bool{}
	for _, c := range cands {
		candidate[c.ID] = true
	}
	// promo codes entered
	codes := map[uuid.UUID]codeRow{}
	if len(pc.Codes) > 0 {
		rows, err := handle.List[codeRow](q.Query(ctx, `SELECT id, promotion_id, code, customer_id, max_uses, max_uses_per_customer, expires_at, status
			FROM commercial.promo_codes WHERE property_id = $1 AND code = ANY($2)`, pc.Property, pc.Codes))
		if err != nil {
			return out, err
		}
		found := map[string]bool{}
		for _, c := range rows {
			found[c.Code] = true
			if !candidate[c.PromotionID] {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: c.Code, Reason: "the promotion of this code is not active"})
				continue
			}
			if reason, err := e.codeProblem(c); err != nil {
				return out, err
			} else if reason != "" {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: c.Code, Reason: reason})
				continue
			}
			if _, dup := codes[c.PromotionID]; !dup {
				codes[c.PromotionID] = c
			}
		}
		for _, c := range pc.Codes {
			if !found[c] {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: c, Reason: "unknown promo code"})
			}
		}
	}
	type scored struct {
		p        *PromotionRule
		discount decimal.Decimal
		code     *codeRow
	}
	var ranked []scored
	for i := range cands {
		p := &cands[i]
		var code *codeRow
		if c, ok := codes[p.ID]; ok {
			code = &c
		}
		ok, reason, err := e.eligible(p, code, subtotal)
		if err != nil {
			return out, err
		}
		if !ok {
			if code != nil {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: code.Code, Reason: reason})
			}
			continue
		}
		d, _ := e.benefit(p, e.usable(p, states, true))
		if !d.IsPositive() {
			if code != nil {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: code.Code, Reason: "no eligible item in this sale"})
			}
			continue
		}
		ranked = append(ranked, scored{p, d, code})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if pol.Selection == "best_price" {
			if !a.discount.Equal(b.discount) {
				return a.discount.GreaterThan(b.discount)
			}
			if a.p.Priority != b.p.Priority {
				return a.p.Priority < b.p.Priority
			}
		} else {
			if a.p.Priority != b.p.Priority {
				return a.p.Priority < b.p.Priority
			}
			if !a.discount.Equal(b.discount) {
				return a.discount.GreaterThan(b.discount)
			}
		}
		return a.p.Code < b.p.Code
	})
	maxStack := dec(pol.MaxStackedPercent)
	total := decimal.Zero
	for _, r := range ranked {
		usable := e.usable(r.p, states, false)
		d, per := e.benefit(r.p, usable)
		// budget left
		if r.p.BudgetAmount != nil {
			_, used, err := e.usage(r.p.ID, nil, nil)
			if err != nil {
				return out, err
			}
			left := dec(*r.p.BudgetAmount).Sub(used)
			if !left.IsPositive() {
				if r.code != nil {
					out.Rejected = append(out.Rejected, RejectedPromotion{Code: r.code.Code, Reason: "the promotion budget is used up"})
				}
				continue
			}
			if d.GreaterThan(left) {
				per = capAllocation(per, usable, left, e.places)
				d = left
			}
		}
		// stacking ceiling per line
		if maxStack.IsPositive() && maxStack.LessThan(hundred) {
			for i, s := range usable {
				ceiling := s.gross.Mul(maxStack).Div(hundred).Round(e.places)
				taken := s.gross.Sub(s.remaining)
				if taken.Add(per[i]).GreaterThan(ceiling) {
					per[i] = decimal.Max(ceiling.Sub(taken), decimal.Zero)
				}
			}
			d = sumDec(per)
		}
		if !d.IsPositive() {
			continue
		}
		ap := AppliedPromotion{PromotionID: r.p.ID, Code: r.p.Code, Name: r.p.Name, PromoType: r.p.PromoType, Version: r.p.Version,
			Stackable: r.p.Stackable, Priority: r.p.Priority, Discount: d.StringFixed(e.places), Lines: []LineDiscount{}}
		if r.code != nil {
			ap.PromoCodeID, ap.PromoCode = &r.code.ID, r.code.Code
		}
		for i, s := range usable {
			if !per[i].IsPositive() {
				continue
			}
			s.remaining = s.remaining.Sub(per[i])
			s.applied = append(s.applied, r.p)
			ap.Lines = append(ap.Lines, LineDiscount{Key: s.line.Key, Discount: per[i].StringFixed(e.places)})
		}
		total = total.Add(d)
		out.Applied = append(out.Applied, ap)
	}
	for _, s := range states {
		out.Lines = append(out.Lines, LineDiscount{Key: s.line.Key, Discount: s.gross.Sub(s.remaining).StringFixed(e.places)})
	}
	out.Discount = total.StringFixed(e.places)
	if len(pc.Codes) > 0 {
		used := map[string]bool{}
		for _, a := range out.Applied {
			used[a.PromoCode] = true
		}
		rejected := map[string]bool{}
		for _, r := range out.Rejected {
			rejected[r.Code] = true
		}
		for _, c := range pc.Codes {
			if !used[c] && !rejected[c] {
				out.Rejected = append(out.Rejected, RejectedPromotion{Code: c, Reason: "another promotion gives a better price or the conditions are not met"})
			}
		}
	}
	return out, nil
}

func sumDec(ds []decimal.Decimal) decimal.Decimal {
	t := decimal.Zero
	for _, d := range ds {
		t = t.Add(d)
	}
	return t
}

func isoWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// codeProblem returns why a promo code cannot be used ("" = usable).
func (e *evaluation) codeProblem(c codeRow) (string, error) {
	if c.Status != "active" {
		return "the promo code is inactive", nil
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(e.pc.At) {
		return "the promo code has expired", nil
	}
	if c.CustomerID != nil && (e.pc.CustomerID == nil || *e.pc.CustomerID != *c.CustomerID) {
		return "the promo code is personal to another customer", nil
	}
	if c.MaxUses != nil || c.MaxUsesPerCustomer != nil {
		n, _, err := e.usage(c.PromotionID, &c.ID, nil)
		if err != nil {
			return "", err
		}
		if c.MaxUses != nil && n >= *c.MaxUses {
			return "the promo code has been used up", nil
		}
		if c.MaxUsesPerCustomer != nil && e.pc.CustomerID != nil {
			n, _, err := e.usage(c.PromotionID, &c.ID, e.pc.CustomerID)
			if err != nil {
				return "", err
			}
			if n >= *c.MaxUsesPerCustomer {
				return "the promo code has been used the maximum number of times by this customer", nil
			}
		}
	}
	return "", nil
}

// usage counts the applied and redeemed uses of a promotion (or one of its
// codes, or one customer), excluding the document being priced.
func (e *evaluation) usage(promotion uuid.UUID, code *uuid.UUID, customer *uuid.UUID) (int, decimal.Decimal, error) {
	var srcType string
	srcID := uuid.Nil
	if e.pc.Source != nil {
		srcType, srcID = e.pc.Source.Type, e.pc.Source.ID
	}
	var n int
	var amt string
	err := e.q.QueryRow(e.ctx, `SELECT count(*), coalesce(sum(discount_amount), 0)::text FROM commercial.promotion_redemptions
		WHERE promotion_id = $1 AND status IN ('applied', 'redeemed') AND ($2::uuid IS NULL OR promo_code_id = $2) AND ($3::uuid IS NULL OR customer_id = $3)
		AND NOT (source_type = $4 AND source_id = $5)`, promotion, code, customer, srcType, srcID).Scan(&n, &amt)
	return n, dec(amt), err
}

// customerFacts loads the membership types and CRM segments of the customer once.
func (e *evaluation) customerFacts() error {
	if e.custDone || e.pc.CustomerID == nil {
		e.custDone = true
		return nil
	}
	e.custDone = true
	act, err := membership.ActiveFor(e.ctx, e.q, e.pc.Property, *e.pc.CustomerID)
	if err != nil {
		return err
	}
	for _, a := range act {
		e.memTypes = append(e.memTypes, strings.ToUpper(a.TypeCode))
	}
	rows, err := e.q.Query(e.ctx, `SELECT segment_id::text FROM crm.segment_members WHERE customer_id = $1`, *e.pc.CustomerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		e.crmSegs = append(e.crmSegs, s)
	}
	return rows.Err()
}

// dayFacts computes holiday, peak and day types once.
func (e *evaluation) loadDayFacts() error {
	if e.facts.computed {
		return nil
	}
	e.facts.computed = true
	h, err := calendar.IsHoliday(e.ctx, e.q, e.pc.Property, e.facts.local)
	if err != nil {
		return err
	}
	e.facts.holiday = h
	if e.pc.Peak != nil {
		e.facts.peak = *e.pc.Peak
	} else {
		pol, _, err := LoadPricingPolicy(e.ctx, e.q, e.pc.Property)
		if err != nil {
			return err
		}
		e.facts.peak = IsPeak(pol, e.facts.local, h)
	}
	if len(e.pc.DayTypeCodes) > 0 {
		e.facts.dayTypes = e.pc.DayTypeCodes
		return nil
	}
	if code, err := DayTypeFor(e.ctx, e.q, e.pc.Property, e.facts.local); err == nil {
		e.facts.dayTypes = append(e.facts.dayTypes, code)
	}
	dts, err := dayTypesOn(e.ctx, e.q, e.pc.Property, e.facts.local)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for _, id := range dts {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		rows, err := e.q.Query(e.ctx, `SELECT code FROM commercial.line_day_types WHERE id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				return err
			}
			e.facts.dayTypes = append(e.facts.dayTypes, c)
		}
		return rows.Err()
	}
	return nil
}

// IsPeak tells whether a local time falls in a peak period or peak hour of
// the Pricing Policies (public holidays count when configured).
func IsPeak(pol PricingPolicy, local time.Time, holiday bool) bool {
	if holiday && pol.HolidaysArePeak {
		return true
	}
	day := local.Format("2006-01-02")
	for _, p := range pol.PeakPeriods {
		if p.From != "" && p.To != "" && day >= p.From && day <= p.To {
			return true
		}
	}
	return InTimeWindows(pol.PeakHours, local)
}

// InTimeWindows tells whether a local time falls in one of the windows.
func InTimeWindows(ws []TimeWindow, local time.Time) bool {
	hm := local.Format("15:04")
	wd := isoWeekday(local)
	prev := wd - 1
	if prev == 0 {
		prev = 7
	}
	for _, w := range ws {
		if w.Start < w.End {
			if (len(w.Days) == 0 || slices.Contains(w.Days, wd)) && hm >= w.Start && hm < w.End {
				return true
			}
			continue
		}
		// past midnight: the evening part on the day, the morning part on the next day
		if (len(w.Days) == 0 || slices.Contains(w.Days, wd)) && hm >= w.Start {
			return true
		}
		if (len(w.Days) == 0 || slices.Contains(w.Days, prev)) && hm < w.End {
			return true
		}
	}
	return false
}

func overlapsAny(have, want []string) bool {
	for _, w := range want {
		if slices.Contains(have, w) {
			return true
		}
	}
	return false
}

// eligible checks the sale-level conditions of a promotion.
func (e *evaluation) eligible(p *PromotionRule, code *codeRow, subtotal decimal.Decimal) (bool, string, error) {
	pc := e.pc
	if slices.Contains(pc.Exclude, p.ID) {
		return false, "removed from this sale", nil
	}
	if len(pc.Only) > 0 {
		// simulation ignores status but keeps the validity period
		if (p.ValidFrom != nil && e.facts.day < *p.ValidFrom) || (p.ValidTo != nil && e.facts.day > *p.ValidTo) {
			return false, "outside the validity period", nil
		}
	}
	if p.RequiresCode && code == nil {
		return false, "requires a promo code", nil
	}
	if len(p.Channels) > 0 && !slices.Contains(p.Channels, pc.Channel) {
		return false, "not valid in this channel", nil
	}
	if len(p.BusinessLines) > 0 && !slices.Contains(p.BusinessLines, pc.BusinessLine) {
		return false, "not valid for this business line", nil
	}
	if len(p.OutletIDs) > 0 && (pc.OutletID == nil || !slices.Contains(p.OutletIDs, pc.OutletID.String())) {
		return false, "not valid at this outlet", nil
	}
	if len(p.Segments) > 0 && !slices.Contains(p.Segments, pc.Segment) {
		return false, "not valid for this customer segment", nil
	}
	if len(p.MembershipTypes) > 0 || len(p.CustomerSegmentIDs) > 0 {
		if err := e.customerFacts(); err != nil {
			return false, "", err
		}
		if len(p.MembershipTypes) > 0 && !overlapsAny(e.memTypes, p.MembershipTypes) {
			return false, "for members of another membership type", nil
		}
		if len(p.CustomerSegmentIDs) > 0 && !overlapsAny(e.crmSegs, p.CustomerSegmentIDs) {
			return false, "for another customer segment", nil
		}
	}
	if len(p.Weekdays) > 0 && !slices.Contains(p.Weekdays, int32(e.facts.weekday)) {
		return false, "not valid on this day", nil
	}
	if len(p.TimeWindows) > 0 && !InTimeWindows(p.TimeWindows, e.facts.local) {
		return false, "outside the promotion hours", nil
	}
	if len(p.DayKinds) > 0 || len(p.DayTypeCodes) > 0 {
		if err := e.loadDayFacts(); err != nil {
			return false, "", err
		}
		if len(p.DayKinds) > 0 {
			var kinds []string
			if e.facts.holiday {
				kinds = append(kinds, "holiday")
			} else if e.facts.weekday <= 5 {
				kinds = append(kinds, "weekday")
			}
			if e.facts.weekday >= 6 {
				kinds = append(kinds, "weekend")
			}
			if e.facts.peak {
				kinds = append(kinds, "peak")
			} else {
				kinds = append(kinds, "off_peak")
			}
			if !overlapsAny(kinds, p.DayKinds) {
				return false, "not valid on this kind of day", nil
			}
		}
		if len(p.DayTypeCodes) > 0 && !overlapsAny(e.facts.dayTypes, p.DayTypeCodes) {
			return false, "not valid on this day type", nil
		}
	}
	if p.MinPurchase != nil && subtotal.LessThan(dec(*p.MinPurchase)) {
		return false, "below the minimum purchase of " + dec(*p.MinPurchase).StringFixed(e.places), nil
	}
	if p.MaxRedemptions != nil || p.MaxPerCustomer != nil {
		if p.MaxRedemptions != nil {
			n, _, err := e.usage(p.ID, nil, nil)
			if err != nil {
				return false, "", err
			}
			if n >= *p.MaxRedemptions {
				return false, "the promotion has reached its redemption limit", nil
			}
		}
		if p.MaxPerCustomer != nil && pc.CustomerID != nil {
			n, _, err := e.usage(p.ID, nil, pc.CustomerID)
			if err != nil {
				return false, "", err
			}
			if n >= *p.MaxPerCustomer {
				return false, "the customer has used this promotion the maximum number of times", nil
			}
		}
	}
	return true, "", nil
}

// lineEligible checks the item conditions of a line.
func lineEligible(p *PromotionRule, l PromoLine) bool {
	if !l.Quantity.IsPositive() || l.UnitPrice.IsNegative() {
		return false
	}
	if len(p.ServiceTypes) > 0 && !slices.Contains(p.ServiceTypes, l.ServiceType) {
		return false
	}
	if len(p.ItemRefs) > 0 && !slices.Contains(p.ItemRefs, l.ItemRef) {
		return false
	}
	if len(p.ProductIDs) > 0 && (l.ProductID == nil || !slices.Contains(p.ProductIDs, l.ProductID.String())) {
		return false
	}
	if len(p.Categories) > 0 && !slices.Contains(p.Categories, l.Category) && !slices.Contains(p.Categories, l.ProductType) {
		return false
	}
	if p.PromoType == "bundle" {
		ok := false
		for _, it := range p.BundleItems {
			ok = ok || (l.ProductID != nil && *l.ProductID == it.ProductID)
		}
		return ok
	}
	return true
}

func groupOf(p *PromotionRule) string {
	if p.StackGroup == nil {
		return ""
	}
	return *p.StackGroup
}

// usable returns the lines a promotion may still take: fresh evaluates
// against untouched lines (ranking), otherwise the stacking rules apply.
func (e *evaluation) usable(p *PromotionRule, states []*lineState, fresh bool) []*lineState {
	var out []*lineState
	for _, s := range states {
		if !lineEligible(p, s.line) {
			continue
		}
		if fresh {
			out = append(out, &lineState{line: s.line, gross: s.gross, remaining: s.gross})
			continue
		}
		if !s.remaining.IsPositive() {
			continue
		}
		ok := true
		for _, x := range s.applied {
			if !x.Stackable || !p.Stackable {
				ok = false
				break
			}
			if g1, g2 := groupOf(p), groupOf(x); g1 != "" && g2 != "" && g1 != g2 {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// unit is one item unit during Buy N / bundle evaluation.
type unit struct {
	line  int
	value decimal.Decimal
}

func unitsOf(lines []*lineState) []unit {
	var out []unit
	for i, s := range lines {
		n := int(s.line.Quantity.IntPart())
		if n <= 0 {
			continue
		}
		v := s.remaining.Div(decimal.NewFromInt(int64(n)))
		for k := 0; k < n; k++ {
			out = append(out, unit{line: i, value: v})
		}
	}
	return out
}

// benefit computes the discount of a promotion on lines and its allocation.
func (e *evaluation) benefit(p *PromotionRule, lines []*lineState) (decimal.Decimal, []decimal.Decimal) {
	per := make([]decimal.Decimal, len(lines))
	for i := range per {
		per[i] = decimal.Zero
	}
	if len(lines) == 0 {
		return decimal.Zero, per
	}
	totalUnits := decimal.Zero
	eligibleAmt := decimal.Zero
	for _, s := range lines {
		totalUnits = totalUnits.Add(s.line.Quantity)
		eligibleAmt = eligibleAmt.Add(s.remaining)
	}
	if p.MinQuantity != nil && totalUnits.LessThan(decimal.NewFromInt(int64(*p.MinQuantity))) {
		return decimal.Zero, per
	}
	pct := decimal.Zero
	if p.DiscountPercent != nil {
		pct = dec(*p.DiscountPercent)
	}
	amt := decimal.Zero
	if p.DiscountAmount != nil {
		amt = dec(*p.DiscountAmount)
	}
	switch p.PromoType {
	case "buy_n_get_x":
		n, x := derefInt(p.BuyQuantity), derefInt(p.GetQuantity)
		if n < 1 || x < 1 {
			return decimal.Zero, per
		}
		free := hundred
		if pct.IsPositive() {
			free = pct
		}
		units := unitsOf(lines)
		sort.SliceStable(units, func(i, j int) bool { return units[i].value.GreaterThan(units[j].value) })
		g := n + x
		raw := make([]decimal.Decimal, len(lines))
		for i := range raw {
			raw[i] = decimal.Zero
		}
		for k := 0; (k+1)*g <= len(units); k++ {
			for _, u := range units[k*g+n : (k+1)*g] {
				raw[u.line] = raw[u.line].Add(u.value.Mul(free).Div(hundred))
			}
		}
		per = roundPer(raw, lines, e.places)
	case "buy_n_price_x":
		n := derefInt(p.BuyQuantity)
		if n < 1 || p.BundlePrice == nil {
			return decimal.Zero, per
		}
		price := dec(*p.BundlePrice)
		units := unitsOf(lines)
		sort.SliceStable(units, func(i, j int) bool { return units[i].value.GreaterThan(units[j].value) })
		raw := make([]decimal.Decimal, len(lines))
		for i := range raw {
			raw[i] = decimal.Zero
		}
		for k := 0; (k+1)*n <= len(units); k++ {
			group := units[k*n : (k+1)*n]
			list := decimal.Zero
			for _, u := range group {
				list = list.Add(u.value)
			}
			d := list.Sub(price)
			if !d.IsPositive() || !list.IsPositive() {
				continue
			}
			for _, u := range group {
				raw[u.line] = raw[u.line].Add(d.Mul(u.value).Div(list))
			}
		}
		per = roundPer(raw, lines, e.places)
	case "bundle":
		if p.BundlePrice == nil || len(p.BundleItems) == 0 {
			return decimal.Zero, per
		}
		price := dec(*p.BundlePrice)
		// units available per product in line order
		avail := map[uuid.UUID][]unit{}
		for i, s := range lines {
			if s.line.ProductID == nil {
				continue
			}
			n := int(s.line.Quantity.IntPart())
			v := decimal.Zero
			if n > 0 {
				v = s.remaining.Div(decimal.NewFromInt(int64(n)))
			}
			for k := 0; k < n; k++ {
				avail[*s.line.ProductID] = append(avail[*s.line.ProductID], unit{line: i, value: v})
			}
		}
		bundles := -1
		for _, it := range p.BundleItems {
			k := len(avail[it.ProductID]) / max(it.Quantity, 1)
			if bundles < 0 || k < bundles {
				bundles = k
			}
		}
		raw := make([]decimal.Decimal, len(lines))
		for i := range raw {
			raw[i] = decimal.Zero
		}
		used := map[uuid.UUID]int{}
		for b := 0; b < bundles; b++ {
			var group []unit
			for _, it := range p.BundleItems {
				us := avail[it.ProductID]
				group = append(group, us[used[it.ProductID]:used[it.ProductID]+it.Quantity]...)
				used[it.ProductID] += it.Quantity
			}
			list := decimal.Zero
			for _, u := range group {
				list = list.Add(u.value)
			}
			d := list.Sub(price)
			if !d.IsPositive() || !list.IsPositive() {
				continue
			}
			for _, u := range group {
				raw[u.line] = raw[u.line].Add(d.Mul(u.value).Div(list))
			}
		}
		per = roundPer(raw, lines, e.places)
	default: // percent / amount based types
		switch {
		case pct.IsPositive():
			for i, s := range lines {
				per[i] = s.remaining.Mul(pct).Div(hundred).Round(e.places)
			}
		case amt.IsPositive() && p.PerUnit:
			for i, s := range lines {
				per[i] = decimal.Min(amt.Mul(s.line.Quantity).Round(e.places), s.remaining)
			}
		case amt.IsPositive():
			per = capAllocation(per, lines, decimal.Min(amt, eligibleAmt), e.places)
		}
	}
	total := sumDec(per)
	if p.MaxDiscount != nil && total.GreaterThan(dec(*p.MaxDiscount)) {
		per = capAllocation(per, lines, dec(*p.MaxDiscount), e.places)
		total = sumDec(per)
	}
	return total, per
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// roundPer rounds raw per-line discounts, keeping the rounded total and
// never exceeding a line's remaining amount.
func roundPer(raw []decimal.Decimal, lines []*lineState, places int32) []decimal.Decimal {
	total := sumDec(raw).Round(places)
	return allocate(total, raw, lines, places)
}

// capAllocation spreads amount over lines pro rata to their remaining
// amounts (weights per when they are given), the last line absorbing the
// rounding.
func capAllocation(per []decimal.Decimal, lines []*lineState, amount decimal.Decimal, places int32) []decimal.Decimal {
	weights := make([]decimal.Decimal, len(lines))
	useGiven := sumDec(per).IsPositive()
	for i, s := range lines {
		if useGiven {
			weights[i] = per[i]
		} else {
			weights[i] = s.remaining
		}
	}
	return allocate(amount, weights, lines, places)
}

// allocate splits amount pro rata to weights with the remainder on the
// last positive line, capped by each line's remaining amount.
func allocate(amount decimal.Decimal, weights []decimal.Decimal, lines []*lineState, places int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(lines))
	for i := range out {
		out[i] = decimal.Zero
	}
	wsum := sumDec(weights)
	if !amount.IsPositive() || !wsum.IsPositive() {
		return out
	}
	last := -1
	for i := range weights {
		if weights[i].IsPositive() {
			last = i
		}
	}
	given := decimal.Zero
	for i := range weights {
		if !weights[i].IsPositive() {
			continue
		}
		var a decimal.Decimal
		if i == last {
			a = amount.Sub(given)
		} else {
			a = amount.Mul(weights[i]).Div(wsum).Round(places)
		}
		if a.GreaterThan(lines[i].remaining) {
			a = lines[i].remaining
		}
		if a.IsNegative() {
			a = decimal.Zero
		}
		out[i] = a
		given = given.Add(a)
	}
	return out
}

// PromotionView is the summary of a promotion for channels (website,
// member app, POS cache).
type PromotionView struct {
	ID              uuid.UUID    `json:"id"`
	Code            string       `json:"code"`
	Name            string       `json:"name"`
	Description     *string      `json:"description"`
	PromoType       string       `json:"promoType"`
	DiscountPercent *string      `json:"discountPercent"`
	DiscountAmount  *string      `json:"discountAmount"`
	BuyQuantity     *int         `json:"buyQuantity"`
	GetQuantity     *int         `json:"getQuantity"`
	BundlePrice     *string      `json:"bundlePrice"`
	ValidFrom       *string      `json:"validFrom"`
	ValidTo         *string      `json:"validTo"`
	Weekdays        []int32      `json:"weekdays"`
	TimeWindows     []TimeWindow `json:"timeWindows"`
	Channels        []string     `json:"channels"`
	BusinessLines   []string     `json:"businessLines"`
	MinPurchase     *string      `json:"minPurchase"`
	RequiresCode    bool         `json:"requiresCode"`
}

func viewOf(p PromotionRule) PromotionView {
	return PromotionView{ID: p.ID, Code: p.Code, Name: p.Name, Description: p.Description, PromoType: p.PromoType, DiscountPercent: trimDec(p.DiscountPercent),
		DiscountAmount: trimDec(p.DiscountAmount), BuyQuantity: p.BuyQuantity, GetQuantity: p.GetQuantity, BundlePrice: trimDec(p.BundlePrice),
		ValidFrom: p.ValidFrom, ValidTo: p.ValidTo, Weekdays: nonNilInts(p.Weekdays), TimeWindows: nonNilWindows(p.TimeWindows), Channels: nonNil(p.Channels),
		BusinessLines: nonNil(p.BusinessLines), MinPurchase: trimDec(p.MinPurchase), RequiresCode: p.RequiresCode}
}

func trimDec(s *string) *string {
	if s == nil {
		return nil
	}
	v := dec(*s).String()
	return &v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilInts(s []int32) []int32 {
	if s == nil {
		return []int32{}
	}
	return s
}

func nonNilWindows(s []TimeWindow) []TimeWindow {
	if s == nil {
		return []TimeWindow{}
	}
	return s
}

// ActivePromotions lists the Active promotions valid on a local date that
// match a channel (and, when given, a business line) — website, member
// app and the POS offline cache.
func ActivePromotions(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, channel, line string, publicOnly bool) ([]PromotionRule, error) {
	all, err := loadPromotions(ctx, q, property, day.Format("2006-01-02"), nil)
	if err != nil {
		return nil, err
	}
	var out []PromotionRule
	for _, p := range all {
		if publicOnly && !p.Public {
			continue
		}
		if channel != "" && len(p.Channels) > 0 && !slices.Contains(p.Channels, channel) {
			continue
		}
		if line != "" && len(p.BusinessLines) > 0 && !slices.Contains(p.BusinessLines, line) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// promoChannel maps an order source or booking channel to a promotion channel.
func promoChannel(source string) string {
	switch source {
	case "member_app":
		return "member_app"
	case "website":
		return "website"
	case "back_office", "walk_in", "import", "quotation", "":
		return "back_office"
	case "ops", "caddy_tablet", "driving_range":
		return "ops"
	}
	return "pos"
}

// PromoChannelOf exposes the channel mapping to sub-packages (POS).
func PromoChannelOf(source string) string { return promoChannel(source) }

// lineOfService maps a pricing service type to its business line.
func lineOfService(serviceType string) string {
	switch serviceType {
	case "golf", "driving_range":
		return "golf"
	case "sport_court", "facility_entry", "class_session", "class_registration", "class_package", "locker":
		return "sportclub"
	case "bungalow", "vip_suite", "meeting_room", "meeting_package", "equipment":
		return "stay"
	case "pos":
		return "pos"
	case "membership":
		return "membership"
	case "voucher_sale":
		return "voucher"
	case "package":
		return "package"
	}
	return "other"
}
