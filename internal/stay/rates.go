package stay

// Accommodation rates (requirements §11, §12, §13, §31). A bungalow stay is
// priced night by night: the Best Available Rate (BAR) of a night is the
// price of the room type in the season covering it (the season with the
// lowest priority number wins when seasons overlap), else the type's base
// rate — the weekend rate on the weekend nights of Stay Policies. A rate
// plan takes the BAR, a percentage or amount from it, or its own fixed
// prices; a stay package replaces the room price with the package price
// (room + inclusions). One promotion — the best one the stay is eligible
// for, or the one of the promo code given — discounts the room, then tax and
// service follow the rate plan's pricing mode. Properties that keep their
// bungalow prices in Commercial pricing rules (P2) are priced there as
// before: the accommodation rates apply when the room type has a base rate
// (or a season / fixed price) or an accommodation rate plan is asked for.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/calendar"
)

// NightPrice is the room price of one night.
type NightPrice struct {
	Date    string  `json:"date"`
	BAR     string  `json:"bar"`
	Price   string  `json:"price"`
	Weekend bool    `json:"weekend"`
	Holiday bool    `json:"holiday"`
	Season  *string `json:"season"`
}

// RoomQuote is the price of a bungalow stay under an accommodation rate
// plan or stay package.
type RoomQuote struct {
	TypeID            uuid.UUID         `json:"typeId"`
	TypeName          string            `json:"typeName"`
	RatePlan          string            `json:"ratePlan"`
	RatePlanName      string            `json:"ratePlanName"`
	PackageID         *uuid.UUID        `json:"packageId"`
	PackageCode       *string           `json:"packageCode"`
	Nights            []NightPrice      `json:"nights"`
	Gross             string            `json:"gross" doc:"Room price of the nights before the promotion"`
	Discount          string            `json:"discount"`
	PromotionID       *uuid.UUID        `json:"promotionId"`
	PromotionCode     *string           `json:"promotionCode"`
	PromotionName     *string           `json:"promotionName"`
	PricingMode       string            `json:"pricingMode"`
	Net               string            `json:"net"`
	Service           string            `json:"service"`
	Tax               string            `json:"tax"`
	Total             string            `json:"total"`
	TaxLines          []commercial.Line `json:"taxLines"`
	IncludesBreakfast bool              `json:"includesBreakfast"`
	DepositPercent    *string           `json:"depositPercent"`
	PaymentPolicy     string            `json:"paymentPolicy"`
	FreeCancelHours   int               `json:"freeCancelHours"`
	CancelFeePercent  string            `json:"cancelFeePercent"`
	NoShowFeePercent  string            `json:"noShowFeePercent"`
	NonRefundable     bool              `json:"nonRefundable"`
	Inclusions        []AddonLine       `json:"inclusions" doc:"Package inclusions"`
	FacilityAccess    []string          `json:"facilityAccess" doc:"Facility types the staying guest enters free"`
}

// roomRequest asks the price of a stay.
type roomRequest struct {
	TypeID      uuid.UUID
	RatePlan    string // accommodation rate plan code ("" = default plan)
	PackageCode string
	Arrival     time.Time // local dates
	Departure   time.Time
	Adults      int
	Children    int
	Segment     string
	CorporateID *uuid.UUID
	Source      string
	PromoCode   string
	BookedAt    time.Time
	Explicit    bool // the rate plan was asked for: a missing price is an error, not the Commercial fallback
}

type ratePlanRow struct {
	ID                uuid.UUID  `db:"id"`
	Code              string     `db:"code"`
	Name              string     `db:"name"`
	PlanType          string     `db:"plan_type"`
	Derivation        string     `db:"derivation"`
	AdjustValue       string     `db:"adjust_value"`
	PricingMode       string     `db:"pricing_mode"`
	IncludesBreakfast bool       `db:"includes_breakfast"`
	MinNights         int        `db:"min_nights"`
	MaxNights         *int       `db:"max_nights"`
	WeekendOnly       bool       `db:"weekend_only"`
	FreeCancelHours   int        `db:"free_cancel_hours"`
	CancelFeePercent  string     `db:"cancel_fee_percent"`
	NoShowFeePercent  string     `db:"no_show_fee_percent"`
	NonRefundable     bool       `db:"non_refundable"`
	DepositPercent    *string    `db:"deposit_percent"`
	PaymentPolicy     string     `db:"payment_policy"`
	Eligibility       string     `db:"eligibility"`
	BookingSources    []string   `db:"booking_sources"`
	FacilityAccess    []string   `db:"facility_access"`
	ValidFrom         *time.Time `db:"valid_from"`
	ValidTo           *time.Time `db:"valid_to"`
}

const ratePlanCols = `id, code, name, plan_type, derivation, trim_scale(adjust_value)::text AS adjust_value, pricing_mode, includes_breakfast, min_nights,
	max_nights, weekend_only, free_cancel_hours, trim_scale(cancel_fee_percent)::text AS cancel_fee_percent,
	trim_scale(no_show_fee_percent)::text AS no_show_fee_percent, non_refundable, trim_scale(deposit_percent)::text AS deposit_percent, payment_policy,
	eligibility, booking_sources, facility_access, valid_from, valid_to`

// ratePlan returns an active accommodation rate plan by code, or the
// default plan for "" (nil when none).
func ratePlan(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*ratePlanRow, error) {
	sql := `SELECT ` + ratePlanCols + ` FROM stay.rate_plans WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND `
	if code == "" {
		sql += `($2 = '' AND is_default) ORDER BY sort_order LIMIT 1`
	} else {
		sql += `upper(code) = upper($2)`
	}
	rows, err := q.Query(ctx, sql, property, code)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[ratePlanRow])
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

type typeRates struct {
	Name        string
	BaseRate    *decimal.Decimal
	WeekendRate *decimal.Decimal
	MaxAdults   int
	MaxChildren int
}

func loadTypeRates(ctx context.Context, q dbtx.Querier, typeID uuid.UUID) (typeRates, error) {
	var t typeRates
	var base, weekend *string
	err := q.QueryRow(ctx, `SELECT name, base_rate::text, weekend_rate::text, max_adults, max_children FROM stay.bungalow_types WHERE id = $1`, typeID).
		Scan(&t.Name, &base, &weekend, &t.MaxAdults, &t.MaxChildren)
	if dbtx.IsNoRows(err) {
		return t, errs.NotFound("room type")
	}
	t.BaseRate, t.WeekendRate = decPtr(base), decPtr(weekend)
	return t, err
}

func decPtr(s *string) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d, err := decimal.NewFromString(*s)
	if err != nil {
		return nil
	}
	return &d
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func decOf(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// weekendNights parses Stay Policies weekendNights ("5,6").
func weekendNights(s string) []time.Weekday {
	var out []time.Weekday
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n >= 1 && n <= 7 {
			out = append(out, time.Weekday(n%7))
		}
	}
	return out
}

type seasonRow struct {
	Code          string
	Start, End    time.Time
	Priority      int
	Weekday       *decimal.Decimal
	Weekend       *decimal.Decimal
	AdjustPercent *decimal.Decimal
}

func currencyPlaces(ctx context.Context, q dbtx.Querier) (string, int32) {
	var cur string
	if err := q.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur); err != nil || cur == "" {
		cur = "IDR"
	}
	if cur == "IDR" || cur == "JPY" {
		return cur, 0
	}
	return cur, 2
}

// quoteRoom prices a bungalow stay with the accommodation rates; nil when
// the stay is priced by Commercial pricing rules (no accommodation rate
// applies and none was asked for).
func (m *Module) quoteRoom(ctx context.Context, tx pgx.Tx, property uuid.UUID, req roomRequest) (*RoomQuote, error) {
	loc := calendar.Location(ctx, tx)
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	nights := int(req.Departure.Sub(req.Arrival).Hours()/24 + 0.5)
	if nights < 1 {
		return nil, errs.Validation("invalid_period", "the departure must be after the arrival")
	}
	t, err := loadTypeRates(ctx, tx, req.TypeID)
	if err != nil {
		return nil, err
	}
	if req.Adults > t.MaxAdults || req.Children > t.MaxChildren {
		return nil, errs.Validation("over_capacity", fmt.Sprintf("%s takes at most %d adults and %d children", t.Name, t.MaxAdults, t.MaxChildren),
			errs.Field("adults", "over_capacity", fmt.Sprintf("maximum %d adults", t.MaxAdults)))
	}
	cur, places := currencyPlaces(ctx, tx)
	q := &RoomQuote{TypeID: req.TypeID, TypeName: t.Name, Nights: []NightPrice{}, Inclusions: []AddonLine{}, Discount: "0"}
	var plan *ratePlanRow
	var pkg *packageRow
	if req.PackageCode != "" {
		if pkg, err = stayPackage(ctx, tx, property, req.PackageCode); err != nil {
			return nil, err
		}
		if err := pkg.eligible(req, nights, loc); err != nil {
			return nil, err
		}
		q.PackageID, q.PackageCode, q.IncludesBreakfast = &pkg.ID, &pkg.Code, pkg.IncludesBreakfast
		// the package room follows the default plan's policies
		if plan, err = ratePlan(ctx, tx, property, ""); err != nil {
			return nil, err
		}
	} else {
		if plan, err = ratePlan(ctx, tx, property, req.RatePlan); err != nil {
			return nil, err
		}
		if plan == nil {
			if req.Explicit {
				return nil, errs.Validation("unknown_rate_plan", "rate plan "+req.RatePlan+" is not an active accommodation rate plan")
			}
			if t.BaseRate == nil {
				return nil, nil // Commercial pricing (P2)
			}
		}
	}
	if plan != nil {
		q.RatePlan, q.RatePlanName, q.PricingMode = plan.Code, plan.Name, plan.PricingMode
		q.IncludesBreakfast = q.IncludesBreakfast || plan.IncludesBreakfast
		q.DepositPercent, q.PaymentPolicy, q.FreeCancelHours = plan.DepositPercent, plan.PaymentPolicy, plan.FreeCancelHours
		q.CancelFeePercent, q.NoShowFeePercent, q.NonRefundable = plan.CancelFeePercent, plan.NoShowFeePercent, plan.NonRefundable
		q.FacilityAccess = plan.FacilityAccess
		if pkg == nil {
			if err := plan.eligible(req, nights, weekendNights(pol.WeekendNights), loc); err != nil {
				return nil, err
			}
		}
	} else {
		q.RatePlan, q.RatePlanName, q.PricingMode, q.PaymentPolicy = "BAR", "Best Available Rate", "nett", "deposit"
		q.FreeCancelHours, q.CancelFeePercent, q.NoShowFeePercent = 24, "50", "100"
	}
	if pkg != nil {
		q.RatePlan, q.RatePlanName = pkg.Code, pkg.Name
	}
	// seasons and holidays of the stay
	rows, err := tx.Query(ctx, `SELECT s.code, s.start_date, s.end_date, s.priority, sp.weekday_price::text, sp.weekend_price::text, s.adjust_percent::text
		FROM stay.seasons s LEFT JOIN stay.season_prices sp ON sp.season_id = s.id AND sp.bungalow_type_id = $2
		WHERE s.property_id = $1 AND s.status = 'active' AND s.archived_at IS NULL AND s.start_date < $4::date AND s.end_date >= $3::date
		ORDER BY s.priority, s.start_date DESC`, property, req.TypeID, req.Arrival.Format(time.DateOnly), req.Departure.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	var seasons []seasonRow
	for rows.Next() {
		var s seasonRow
		var wd, we, adj *string
		if err := rows.Scan(&s.Code, &s.Start, &s.End, &s.Priority, &wd, &we, &adj); err != nil {
			rows.Close()
			return nil, err
		}
		s.Weekday, s.Weekend, s.AdjustPercent = decPtr(wd), decPtr(we), decPtr(adj)
		seasons = append(seasons, s)
	}
	rows.Close()
	holidays, err := holidaysBetween(ctx, tx, property, req.Arrival, req.Departure)
	if err != nil {
		return nil, err
	}
	var fixed *[2]*decimal.Decimal
	if plan != nil && plan.Derivation == "fixed" && pkg == nil {
		var wd string
		var we *string
		err := tx.QueryRow(ctx, `SELECT weekday_price::text, weekend_price::text FROM stay.rate_plan_prices WHERE rate_plan_id = $1 AND bungalow_type_id = $2`,
			plan.ID, req.TypeID).Scan(&wd, &we)
		if err != nil && !dbtx.IsNoRows(err) {
			return nil, err
		}
		if err == nil {
			fixed = &[2]*decimal.Decimal{decPtr(&wd), decPtr(we)}
		}
	}
	wkend := weekendNights(pol.WeekendNights)
	gross := decimal.Zero
	for i := 0; i < nights; i++ {
		d := time.Date(req.Arrival.Year(), req.Arrival.Month(), req.Arrival.Day()+i, 0, 0, 0, 0, loc)
		day := d.Format(time.DateOnly)
		n := NightPrice{Date: day, Weekend: slices.Contains(wkend, d.Weekday()), Holiday: holidays[day]}
		// BAR: season price, season uplift of the base rate, base / weekend rate
		var bar *decimal.Decimal
		base := t.BaseRate
		if n.Weekend && t.WeekendRate != nil {
			base = t.WeekendRate
		}
		for _, s := range seasons {
			if day < s.Start.Format(time.DateOnly) || day > s.End.Format(time.DateOnly) {
				continue
			}
			code := s.Code
			switch {
			case s.Weekday != nil:
				p := *s.Weekday
				if n.Weekend && s.Weekend != nil {
					p = *s.Weekend
				}
				bar, n.Season = &p, &code
			case s.AdjustPercent != nil && base != nil:
				p := base.Mul(decimal.NewFromInt(100).Add(*s.AdjustPercent)).Div(decimal.NewFromInt(100)).Round(places)
				bar, n.Season = &p, &code
			}
			if bar != nil {
				break
			}
		}
		if bar == nil {
			bar = base
		}
		var price *decimal.Decimal
		switch {
		case pkg != nil && pkg.PriceMode == "fixed":
			p := decOf(deref(pkg.Price))
			price = &p
		case pkg != nil:
			price = bar
		case plan != nil && plan.Derivation == "fixed" && fixed != nil:
			p := *fixed[0]
			if n.Weekend && fixed[1] != nil {
				p = *fixed[1]
			}
			price = &p
		case bar == nil:
		case plan == nil || plan.Derivation == "bar" || plan.Derivation == "fixed":
			price = bar
		case plan.Derivation == "percent":
			p := bar.Mul(decimal.NewFromInt(100).Add(decOf(plan.AdjustValue))).Div(decimal.NewFromInt(100)).Round(places)
			price = &p
		case plan.Derivation == "amount":
			p := bar.Add(decOf(plan.AdjustValue))
			price = &p
		}
		if price == nil {
			if !req.Explicit && pkg == nil && plan != nil && t.BaseRate == nil {
				return nil, nil // the property prices bungalows in Commercial pricing (P2)
			}
			return nil, errs.Validation("no_price", fmt.Sprintf("%s has no rate for %s: set its base rate, a season price or the rate plan price", t.Name, day))
		}
		if price.IsNegative() {
			z := decimal.Zero
			price = &z
		}
		if bar != nil {
			n.BAR = bar.StringFixed(places)
		} else {
			n.BAR = price.StringFixed(places)
		}
		n.Price = price.StringFixed(places)
		gross = gross.Add(*price)
		q.Nights = append(q.Nights, n)
	}
	q.Gross = gross.StringFixed(places)
	// promotion
	if pkg == nil {
		promo, disc, err := m.bestPromotion(ctx, tx, property, req, q, loc)
		if err != nil {
			return nil, err
		}
		if promo != nil {
			disc = decimal.Min(disc, gross).Round(places)
			q.PromotionID, q.PromotionCode, q.PromotionName, q.Discount = &promo.ID, &promo.Code, &promo.Name, disc.StringFixed(places)
		}
	}
	// tax and service
	start := clockOn(req.Arrival, pol.CheckInTime, loc)
	rules, err := commercial.RulesAt(ctx, tx, property, start)
	if err != nil {
		return nil, err
	}
	b := commercial.CalculateMode(rules, gross.Sub(decOf(q.Discount)), cur, start, q.PricingMode)
	q.Net, q.Total, q.TaxLines = b.NetAmount, b.Total, b.Lines
	q.Service, q.Tax = commercial.SumKind(b.Lines, "service").String(), commercial.SumKind(b.Lines, "tax").String()
	if q.TaxLines == nil {
		q.TaxLines = []commercial.Line{}
	}
	// package inclusions
	if pkg != nil {
		lines, err := m.priceInclusions(ctx, tx, property, pkg, nights, req.Adults+req.Children, start)
		if err != nil {
			return nil, err
		}
		q.Inclusions = lines
	}
	return q, nil
}

func (p *ratePlanRow) eligible(req roomRequest, nights int, weekend []time.Weekday, loc *time.Location) error {
	if nights < p.MinNights {
		return errs.Validation("min_nights", fmt.Sprintf("%s requires at least %d nights", p.Name, p.MinNights))
	}
	if p.MaxNights != nil && nights > *p.MaxNights {
		return errs.Validation("max_nights", fmt.Sprintf("%s allows at most %d nights", p.Name, *p.MaxNights))
	}
	day := req.Arrival.Format(time.DateOnly)
	if (p.ValidFrom != nil && day < p.ValidFrom.Format(time.DateOnly)) || (p.ValidTo != nil && day > p.ValidTo.Format(time.DateOnly)) {
		return errs.Validation("rate_not_valid", p.Name+" is not valid for the arrival date")
	}
	if p.WeekendOnly {
		for i := 0; i < nights; i++ {
			d := time.Date(req.Arrival.Year(), req.Arrival.Month(), req.Arrival.Day()+i, 0, 0, 0, 0, loc)
			if !slices.Contains(weekend, d.Weekday()) {
				return errs.Validation("weekend_only", p.Name+" is for weekend nights only")
			}
		}
	}
	switch p.Eligibility {
	case "member":
		if !isMemberSegment(req.Segment) {
			return errs.Validation("members_only", p.Name+" is for members only")
		}
	case "corporate":
		if req.CorporateID == nil {
			return errs.Validation("corporate_only", p.Name+" is for corporate bookings only")
		}
	}
	if len(p.BookingSources) > 0 && req.Source != "" && !slices.Contains(p.BookingSources, req.Source) {
		return errs.Validation("source_not_allowed", p.Name+" cannot be booked from "+strings.ReplaceAll(req.Source, "_", " "))
	}
	return nil
}

func isMemberSegment(s string) bool {
	return s != "" && s != "guest" && s != "non_member" && s != "walk_in" && s != "corporate" && s != "staying_guest"
}

// holidaysBetween returns the public holidays (platform calendar) of the stay.
func holidaysBetween(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD') FROM platform.calendar_days WHERE (property_id IS NULL OR property_id = $1)
		AND kind = 'public_holiday' AND status = 'active' AND day >= $2::date AND day < $3::date`, property, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := map[string]bool{}
	for _, d := range days {
		out[d] = true
	}
	return out, err
}

// ── promotions (§31) ──────────────────────────────────────────────────────

type promoRow struct {
	ID                  uuid.UUID  `db:"id"`
	Code                string     `db:"code"`
	Name                string     `db:"name"`
	PromoType           string     `db:"promo_type"`
	DiscountPercent     *string    `db:"discount_percent"`
	DiscountAmount      *string    `db:"discount_amount"`
	StayNights          *int       `db:"stay_nights"`
	PayNights           *int       `db:"pay_nights"`
	MinDaysBefore       *int       `db:"min_days_before"`
	MaxDaysBefore       *int       `db:"max_days_before"`
	MinNights           *int       `db:"min_nights"`
	MaxNights           *int       `db:"max_nights"`
	BungalowTypeIDs     []string   `db:"bungalow_type_ids"`
	RatePlanCodes       []string   `db:"rate_plan_codes"`
	BookingSources      []string   `db:"booking_sources"`
	CorporateAccountIDs []string   `db:"corporate_account_ids"`
	BookFrom            *time.Time `db:"book_from"`
	BookTo              *time.Time `db:"book_to"`
	StayFrom            *time.Time `db:"stay_from"`
	StayTo              *time.Time `db:"stay_to"`
	PromoCode           *string    `db:"promo_code"`
	UsageLimit          *int       `db:"usage_limit"`
	UsedCount           int        `db:"used_count"`
	Priority            int        `db:"priority"`
}

// bestPromotion returns the promotion of the promo code (an error when it
// does not apply) or the best automatic promotion, with its discount.
func (m *Module) bestPromotion(ctx context.Context, q dbtx.Querier, property uuid.UUID, req roomRequest, quote *RoomQuote, loc *time.Location) (*promoRow, decimal.Decimal, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, promo_type, trim_scale(discount_percent)::text AS discount_percent,
		trim_scale(discount_amount)::text AS discount_amount, stay_nights, pay_nights, min_days_before, max_days_before, min_nights, max_nights,
		bungalow_type_ids, rate_plan_codes, booking_sources, corporate_account_ids, book_from, book_to, stay_from, stay_to, promo_code, usage_limit,
		used_count, priority FROM stay.promotions WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND (($2 = '' AND promo_code IS NULL) OR ($2 <> '' AND upper(promo_code) = upper($2))) ORDER BY priority, code`, property, req.PromoCode)
	if err != nil {
		return nil, decimal.Zero, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[promoRow])
	if err != nil {
		return nil, decimal.Zero, err
	}
	if req.PromoCode != "" && len(list) == 0 {
		return nil, decimal.Zero, errs.Validation("invalid_promo_code", "promo code "+req.PromoCode+" is not valid")
	}
	var best *promoRow
	bestDisc := decimal.Zero
	var lastReason string
	for i := range list {
		p := &list[i]
		disc, why := p.discount(req, quote, loc)
		if why != "" {
			lastReason = why
			continue
		}
		if disc.GreaterThan(bestDisc) {
			best, bestDisc = p, disc
		}
	}
	if req.PromoCode != "" && best == nil {
		return nil, decimal.Zero, errs.Validation("promo_not_applicable", "promo code "+req.PromoCode+" does not apply: "+lastReason)
	}
	return best, bestDisc, nil
}

// discount computes the discount of a promotion on a stay, or why it does
// not apply.
func (p *promoRow) discount(req roomRequest, quote *RoomQuote, loc *time.Location) (decimal.Decimal, string) {
	nights := len(quote.Nights)
	booked := req.BookedAt.In(loc)
	bookDay := booked.Format(time.DateOnly)
	switch {
	case p.UsageLimit != nil && p.UsedCount >= *p.UsageLimit:
		return decimal.Zero, "the usage limit is reached"
	case p.BookFrom != nil && bookDay < p.BookFrom.Format(time.DateOnly), p.BookTo != nil && bookDay > p.BookTo.Format(time.DateOnly):
		return decimal.Zero, "outside the booking period"
	case p.MinNights != nil && nights < *p.MinNights:
		return decimal.Zero, fmt.Sprintf("needs at least %d nights", *p.MinNights)
	case p.MaxNights != nil && nights > *p.MaxNights:
		return decimal.Zero, fmt.Sprintf("allows at most %d nights", *p.MaxNights)
	case len(p.BungalowTypeIDs) > 0 && !slices.Contains(p.BungalowTypeIDs, req.TypeID.String()):
		return decimal.Zero, "not for this room type"
	case len(p.RatePlanCodes) > 0 && !slices.ContainsFunc(p.RatePlanCodes, func(c string) bool { return strings.EqualFold(c, quote.RatePlan) }):
		return decimal.Zero, "not for this rate plan"
	case len(p.BookingSources) > 0 && !slices.Contains(p.BookingSources, req.Source):
		return decimal.Zero, "not for this booking source"
	}
	daysBefore := int(time.Date(req.Arrival.Year(), req.Arrival.Month(), req.Arrival.Day(), 0, 0, 0, 0, time.UTC).
		Sub(time.Date(booked.Year(), booked.Month(), booked.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	// the nights in the stay window of the promotion
	var eligible []decimal.Decimal
	for _, n := range quote.Nights {
		if (p.StayFrom != nil && n.Date < p.StayFrom.Format(time.DateOnly)) || (p.StayTo != nil && n.Date > p.StayTo.Format(time.DateOnly)) {
			continue
		}
		switch p.PromoType {
		case "weekend":
			if !n.Weekend {
				continue
			}
		case "holiday":
			if !n.Holiday {
				continue
			}
		}
		eligible = append(eligible, decOf(n.Price))
	}
	if len(eligible) == 0 {
		return decimal.Zero, "no night of the stay is eligible"
	}
	sum := decimal.Zero
	for _, e := range eligible {
		sum = sum.Add(e)
	}
	pct := func(base decimal.Decimal) decimal.Decimal {
		if p.DiscountPercent != nil {
			return base.Mul(decOf(*p.DiscountPercent)).Div(decimal.NewFromInt(100))
		}
		return decOf(deref(p.DiscountAmount))
	}
	switch p.PromoType {
	case "early_booking":
		if p.MinDaysBefore != nil && daysBefore < *p.MinDaysBefore {
			return decimal.Zero, fmt.Sprintf("book at least %d days before arrival", *p.MinDaysBefore)
		}
	case "last_minute":
		if p.MaxDaysBefore != nil && daysBefore > *p.MaxDaysBefore {
			return decimal.Zero, fmt.Sprintf("only within %d days of arrival", *p.MaxDaysBefore)
		}
	case "long_stay":
		if p.MinNights == nil && nights < 3 {
			return decimal.Zero, "a long stay needs at least 3 nights"
		}
	case "corporate":
		if req.CorporateID == nil || (len(p.CorporateAccountIDs) > 0 && !slices.Contains(p.CorporateAccountIDs, req.CorporateID.String())) {
			return decimal.Zero, "for corporate bookings of the listed accounts"
		}
	case "weekend", "holiday":
		if p.DiscountPercent == nil {
			return decOf(deref(p.DiscountAmount)).Mul(decimal.NewFromInt(int64(len(eligible)))), ""
		}
	case "stay_pay":
		if p.StayNights == nil || p.PayNights == nil || *p.PayNights >= *p.StayNights {
			return decimal.Zero, "invalid Stay X Pay Y"
		}
		x, y := *p.StayNights, *p.PayNights
		free := (len(eligible) / x) * (x - y)
		if free == 0 {
			return decimal.Zero, fmt.Sprintf("stay at least %d nights", x)
		}
		slices.SortFunc(eligible, func(a, b decimal.Decimal) int { return a.Cmp(b) })
		d := decimal.Zero
		for i := 0; i < free; i++ {
			d = d.Add(eligible[i])
		}
		return d, ""
	}
	return pct(sum), ""
}

// ── stay packages and add-ons (§13) ───────────────────────────────────────

type packageRow struct {
	ID                uuid.UUID   `db:"id"`
	Code              string      `db:"code"`
	Name              string      `db:"name"`
	PriceMode         string      `db:"price_mode"`
	Price             *string     `db:"price"`
	InclusionDiscount *string     `db:"inclusion_discount"`
	Inclusions        []inclusion `db:"inclusions"`
	BungalowTypeIDs   []string    `db:"bungalow_type_ids"`
	MinNights         int         `db:"min_nights"`
	MaxNights         *int        `db:"max_nights"`
	IncludesBreakfast bool        `db:"includes_breakfast"`
	ValidFrom         *time.Time  `db:"valid_from"`
	ValidTo           *time.Time  `db:"valid_to"`
}

type inclusion struct {
	AddonID  uuid.UUID `json:"addonId"`
	Quantity int       `json:"quantity"`
}

func stayPackage(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*packageRow, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, price_mode, trim_scale(price)::text AS price, trim_scale(inclusion_discount)::text AS inclusion_discount,
		inclusions, bungalow_type_ids, min_nights, max_nights, includes_breakfast, valid_from, valid_to FROM stay.packages
		WHERE property_id = $1 AND upper(code) = upper($2) AND status = 'active' AND archived_at IS NULL`, property, code)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[packageRow])
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errs.Validation("unknown_package", "stay package "+code+" is not available")
	}
	return &list[0], nil
}

func (p *packageRow) eligible(req roomRequest, nights int, _ *time.Location) error {
	switch {
	case len(p.BungalowTypeIDs) > 0 && !slices.Contains(p.BungalowTypeIDs, req.TypeID.String()):
		return errs.Validation("package_type", p.Name+" is not offered for this room type")
	case nights < p.MinNights:
		return errs.Validation("min_nights", fmt.Sprintf("%s requires at least %d nights", p.Name, p.MinNights))
	case p.MaxNights != nil && nights > *p.MaxNights:
		return errs.Validation("max_nights", fmt.Sprintf("%s allows at most %d nights", p.Name, *p.MaxNights))
	}
	day := req.Arrival.Format(time.DateOnly)
	if (p.ValidFrom != nil && day < p.ValidFrom.Format(time.DateOnly)) || (p.ValidTo != nil && day > p.ValidTo.Format(time.DateOnly)) {
		return errs.Validation("package_not_valid", p.Name+" is not valid for the arrival date")
	}
	return nil
}

// AddonLine is a priced add-on of a stay.
type AddonLine struct {
	AddonID   uuid.UUID `json:"addonId"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	Quantity  int       `json:"quantity"`
	Units     int       `json:"units" doc:"Nights / persons the unit price is multiplied by"`
	UnitPrice string    `json:"unitPrice"`
	Gross     string    `json:"gross"`
	Discount  string    `json:"discount"`
	Net       string    `json:"net"`
	Service   string    `json:"service"`
	Tax       string    `json:"tax"`
	Total     string    `json:"total"`
	Included  bool      `json:"included" doc:"Included in the package price"`
}

type addonRow struct {
	ID            uuid.UUID `db:"id"`
	Code          string    `db:"code"`
	Name          string    `db:"name"`
	Category      string    `db:"category"`
	Price         string    `db:"price"`
	Unit          string    `db:"unit"`
	PricingMode   string    `db:"pricing_mode"`
	Taxable       bool      `db:"taxable"`
	ServiceCharge bool      `db:"service_charge"`
	Availability  string    `db:"availability"`
	MaxQuantity   *int      `db:"max_quantity"`
	Status        string    `db:"status"`
}

func loadAddon(ctx context.Context, q dbtx.Querier, property, aid uuid.UUID) (addonRow, error) {
	rows, err := q.Query(ctx, `SELECT id, code, name, category, trim_scale(price)::text AS price, unit, pricing_mode, taxable, service_charge, availability,
		max_quantity, status FROM stay.addons WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, aid, property)
	if err != nil {
		return addonRow{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByName[addonRow])
	if err != nil {
		return addonRow{}, err
	}
	if len(list) == 0 {
		return addonRow{}, errs.Validation("unknown_addon", "add-on not found")
	}
	return list[0], nil
}

// priceAddon prices an add-on for a stay of n nights and persons guests.
func priceAddon(ctx context.Context, q dbtx.Querier, property uuid.UUID, a addonRow, qty, nights, persons int, discountPct decimal.Decimal, at time.Time) (AddonLine, error) {
	if qty <= 0 {
		qty = 1
	}
	if a.MaxQuantity != nil && qty > *a.MaxQuantity {
		return AddonLine{}, errs.Validation("over_max_quantity", fmt.Sprintf("%s: at most %d", a.Name, *a.MaxQuantity))
	}
	units := 1
	switch a.Unit {
	case "per_night":
		units = max(nights, 1)
	case "per_person":
		units = max(persons, 1)
	case "per_person_night":
		units = max(nights, 1) * max(persons, 1)
	}
	cur, places := currencyPlaces(ctx, q)
	price := decOf(a.Price)
	gross := price.Mul(decimal.NewFromInt(int64(qty * units)))
	disc := gross.Mul(discountPct).Div(decimal.NewFromInt(100)).Round(places)
	rules, err := commercial.RulesAt(ctx, q, property, at)
	if err != nil {
		return AddonLine{}, err
	}
	var sel []commercial.Rule
	for _, r := range rules {
		if (r.Kind == "tax" && a.Taxable) || (r.Kind == "service" && a.ServiceCharge) {
			sel = append(sel, r)
		}
	}
	b := commercial.CalculateMode(sel, gross.Sub(disc), cur, at, a.PricingMode)
	return AddonLine{AddonID: a.ID, Code: a.Code, Name: a.Name, Category: a.Category, Quantity: qty, Units: units, UnitPrice: price.StringFixed(places),
		Gross: gross.StringFixed(places), Discount: disc.StringFixed(places), Net: b.NetAmount, Service: commercial.SumKind(b.Lines, "service").String(),
		Tax: commercial.SumKind(b.Lines, "tax").String(), Total: b.Total}, nil
}

// priceInclusions prices the inclusions of a package: included at no
// charge (fixed package) or charged with the package discount (room_plus).
func (m *Module) priceInclusions(ctx context.Context, q dbtx.Querier, property uuid.UUID, p *packageRow, nights, persons int, at time.Time) ([]AddonLine, error) {
	out := []AddonLine{}
	disc := decimal.Zero
	if p.InclusionDiscount != nil {
		disc = decOf(*p.InclusionDiscount)
	}
	for _, inc := range p.Inclusions {
		a, err := loadAddon(ctx, q, property, inc.AddonID)
		if err != nil {
			return nil, err
		}
		l, err := priceAddon(ctx, q, property, a, max(inc.Quantity, 1), nights, persons, disc, at)
		if err != nil {
			return nil, err
		}
		if p.PriceMode == "fixed" {
			l.Included, l.Discount, l.Net, l.Service, l.Tax, l.Total = true, l.Gross, "0", "0", "0", "0"
		}
		out = append(out, l)
	}
	return out, nil
}
