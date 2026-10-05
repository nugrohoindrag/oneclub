package commercial

// PRD P3 EP-10 on the P1–P2 pricing (additive): FR-PRC-P3-01 completes the
// rate rules with Corporate Rate (a contract rate per corporate account),
// Holiday Rate (public holidays of the Day Calendar) and Peak / Off-Peak
// Rate for every line (the Pricing Policies peak seasons and hours; golf
// keeps the tee time's peak flag), and the promotion hook applies the
// promotion engine to golf, booking and POS prices so every price snapshot
// records its promotions (FR-PRM-06/07).

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/resource"
)

func init() {
	PricingRules.Fields = insertBefore(PricingRules.Fields, "price", []resource.Field{
		{Name: "corporateAccountId", Column: "corporate_account_id", Label: "Corporate Account (Corporate Rate; empty = any)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.corporate_accounts", SameProperty: true, Label: "corporate account"}},
		{Name: "holiday", Column: "holiday", Label: "Public Holiday (Holiday Rate; empty = any)", Kind: resource.Bool},
	})
	for i := range PricingRules.Fields {
		if PricingRules.Fields[i].Name == "peak" {
			PricingRules.Fields[i].Label = "Peak (Peak / Off-Peak Rate; empty = any)"
		}
	}
}

// ruleHoliday tells whether a local date is a public holiday.
func ruleHoliday(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (bool, error) {
	return calendar.IsHoliday(ctx, q, property, day)
}

// ruleFacts lazily answers the P3 rule dimensions of a line price.
type ruleFacts struct {
	property             uuid.UUID
	local                time.Time
	holiday, peak, ready bool
}

func (f *ruleFacts) load(ctx context.Context, q dbtx.Querier) error {
	if f.ready {
		return nil
	}
	f.ready = true
	h, err := calendar.IsHoliday(ctx, q, f.property, f.local)
	if err != nil {
		return err
	}
	pol, _, err := LoadPricingPolicy(ctx, q, f.property)
	if err != nil {
		return err
	}
	f.holiday, f.peak = h, IsPeak(pol, f.local, h)
	return nil
}

// match checks the Corporate / Holiday / Peak dimensions of a line rule and
// returns its extra specificity (a contract rate beats every generic rule).
func (f *ruleFacts) match(ctx context.Context, q dbtx.Querier, r ruleRow, corporate *uuid.UUID) (bool, int, error) {
	score := 0
	if r.CorporateID != nil {
		if corporate == nil || *corporate != *r.CorporateID {
			return false, 0, nil
		}
		score += 64
	}
	if r.Holiday != nil || r.Peak != nil {
		if err := f.load(ctx, q); err != nil {
			return false, 0, err
		}
		if r.Holiday != nil {
			if *r.Holiday != f.holiday {
				return false, 0, nil
			}
			score += 4
		}
		if r.Peak != nil {
			if *r.Peak != f.peak {
				return false, 0, nil
			}
			score += 2
		}
	}
	return true, score, nil
}

// applyLinePromotions applies the promotion engine to a line price: the
// discount reduces the gross and tax & service are computed again.
func applyLinePromotions(ctx context.Context, q dbtx.Querier, property uuid.UUID, req PriceRequest, out *LinePrice, units, gross decimal.Decimal,
	taxRules []Rule, facts *ruleFacts) error {
	if !units.IsPositive() || !gross.IsPositive() {
		return nil
	}
	pc := PromoContext{Property: property, At: req.Start, Channel: promoChannel(req.Channel), BusinessLine: lineOfService(req.ServiceType),
		CustomerID: req.CustomerID, Segment: req.Segment, Codes: req.PromoCodes, Currency: out.Tax.Currency,
		Lines: []PromoLine{{Key: "1", ServiceType: req.ServiceType, ItemRef: nonEmpty(req.ItemRef, deref(out.ItemRef)), Quantity: units,
			UnitPrice: gross.Div(units)}}}
	if facts != nil && facts.ready {
		pc.Peak = &facts.peak
	}
	res, err := EvaluatePromotions(ctx, q, pc)
	if err != nil {
		return err
	}
	d := res.TotalDiscount()
	if !d.IsPositive() {
		return nil
	}
	if d.GreaterThan(gross) {
		d = gross
	}
	if len(out.Tax.Lines) > 0 {
		// keep the rule's tax & service selection
		var sel []Rule
		for _, t := range taxRules {
			for _, l := range out.Tax.Lines {
				if l.RuleID == t.ID {
					sel = append(sel, t)
					break
				}
			}
		}
		taxRules = sel
	} else {
		taxRules = nil
	}
	out.Tax = CalculateMode(taxRules, gross.Sub(d), out.Tax.Currency, req.Start, out.Tax.PricingMode)
	out.Discount = d.StringFixed(places(out.Tax.Currency))
	out.Promotions = res.Applied
	out.Explanation = append(out.Explanation, "promotions "+promoCodesOf(res.Applied)+" −"+out.Discount)
	return nil
}

func promoCodesOf(as []AppliedPromotion) string {
	s := ""
	for i, a := range as {
		if i > 0 {
			s += ", "
		}
		s += a.Code
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyGolfPromotions applies the promotion engine to a golf price (P1
// Resolve): the discounted unit price is finished again so the all-in
// components keep adding up (the remainder component absorbs the discount).
func applyGolfPromotions(ctx context.Context, q dbtx.Querier, pq PriceQuery, res *PriceResult, unit decimal.Decimal, comps []Component, taxRules []Rule) error {
	qty := decimal.NewFromInt(int64(res.Quantity))
	gross := unit.Mul(qty)
	if !gross.IsPositive() {
		return nil
	}
	pc := PromoContext{Property: pq.Property, At: pq.PlayAt, Channel: promoChannel(pq.Channel), BusinessLine: "golf", CustomerID: pq.CustomerID,
		Segment: res.Segment, Codes: pq.PromoCodes, Peak: pq.Peak, Currency: res.Currency,
		Lines: []PromoLine{{Key: "1", ServiceType: "golf", ItemRef: res.ChargeType, Quantity: qty, UnitPrice: unit}}}
	if res.DayTypeCode != "" {
		pc.DayTypeCodes = []string{res.DayTypeCode}
	}
	pr, err := EvaluatePromotions(ctx, q, pc)
	if err != nil {
		return err
	}
	d := pr.TotalDiscount()
	if !d.IsPositive() {
		return nil
	}
	if d.GreaterThan(gross) {
		d = gross
	}
	list := res.UnitPrice
	var cs []Component
	for _, c := range comps {
		cc := c
		cc.Amount = ""
		cs = append(cs, cc)
	}
	finish(res, gross.Sub(d).Div(qty), cs, taxRules, pq.PlayAt)
	res.ListUnitPrice = list
	res.Discount = d.StringFixed(places(res.Currency))
	res.Promotions = pr.Applied
	return nil
}

// LineSnapshotInput is a manually priced line (POS) with its promotions.
type LineSnapshotInput struct {
	ServiceType string
	ItemRef     string
	Segment     string
	Units       decimal.Decimal
	UnitPrice   decimal.Decimal // after discounts
	ListPrice   decimal.Decimal // before discounts (0 = UnitPrice)
	Mode        string
	TaxCodes    []string
	At          time.Time
	Override    map[string]any
	Promotions  []AppliedPromotion
	Channel     string
}

// LineSnapshot stores the snapshot of a manually priced line (POS) with the
// promotions applied to it; ManualSnapshot of P2 without promotions.
func (Pricer) LineSnapshot(ctx context.Context, tx pgx.Tx, property uuid.UUID, in LineSnapshotInput) (uuid.UUID, Breakdown, error) {
	taxRules, err := RulesAt(ctx, tx, property, in.At)
	if err != nil {
		return uuid.Nil, Breakdown{}, err
	}
	if len(in.TaxCodes) > 0 {
		var sel []Rule
		for _, t := range taxRules {
			if slices.Contains(in.TaxCodes, t.Code) {
				sel = append(sel, t)
			}
		}
		taxRules = sel
	}
	cur := currencyOf(ctx, tx)
	gross := in.Units.Mul(in.UnitPrice)
	b := CalculateMode(taxRules, gross, cur, in.At, in.Mode)
	list := in.ListPrice
	if list.IsZero() {
		list = in.UnitPrice
	}
	sid := id.New()
	taxLines, _ := json.Marshal(b.Lines)
	var ov []byte
	if in.Override != nil {
		ov, _ = json.Marshal(in.Override)
	}
	seg := in.Segment
	if seg == "any" {
		seg = ""
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_snapshots (id, property_id, charge_type, service_type, item_ref, segment, unit, quantity,
		list_price, gross_amount, net_amount, service_amount, tax_amount, total, currency, pricing_mode, tax_service, override, play_at, created_by,
		channel, promotions)
		VALUES ($1,$2,$3,$3,$4,$5,'item',$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13,$14,$15,$16,$17,$18,$19,$20)`,
		sid, property, in.ServiceType, nullStr(in.ItemRef), nullStr(seg), in.Units.String(), list.String(), in.Units.Mul(list).String(), b.NetAmount,
		SumKind(b.Lines, "service").String(), SumKind(b.Lines, "tax").String(), b.Total, cur, in.Mode, taxLines, ov, in.At, actorPtr(ctx),
		nullStr(in.Channel), SnapshotPromotions(in.Promotions)); err != nil {
		return uuid.Nil, b, err
	}
	return sid, b, nil
}
