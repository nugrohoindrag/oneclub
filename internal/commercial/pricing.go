package commercial

// EP-07 Pricing Foundation. Rates are rules, never code: a Rate Plan groups
// Pricing Rules that match a player segment, day type, time band, playing
// route, channel and peak flag with an effective date. Resolution is
// deterministic (priority, then specificity, then the latest effective
// date) and every charge stores an immutable Pricing Snapshot.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/resource"
)

// Segments are the golf player segments of P1 (FR-PRC-03).
var Segments = []string{"member", "guest", "guest_of_member", "non_member", "reciprocal", "senior", "ladies", "junior"}

// ChargeTypes priced by rules.
var ChargeTypes = []string{"golf_round", "caddy_fee", "cart_fee", "extra_cart"}

// Channels of booking (FR-BKG-13).
var Channels = []string{"member_app", "website", "back_office", "walk_in", "import"}

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	code40Re = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
	timeRe   = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	wdRe     = regexp.MustCompile(`^([1-7](,[1-7])*)?$`)
)

func code20(label string) resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: label, Kind: resource.String, Required: true, Max: 20, Upper: true,
		Pattern: codeRe, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true}
}

var DayTypes = &resource.Def{
	Key: "commercial.day_type", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/day-types", Table: "commercial.day_types",
	Name: "Day Type", Plural: "Day Types", Tag: "Pricing", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "priority, code, id",
	Fields: []resource.Field{code20("Code"), resource.Name(),
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays (1=Mon … 7=Sun)", Kind: resource.String, Max: 20, Default: "", Pattern: wdRe, PatternMsg: "comma separated 1–7, e.g. 1,2,3,4,5"},
		{Name: "includesHolidays", Column: "includes_holidays", Label: "Public holidays use this day type", Kind: resource.Bool, Default: false},
		{Name: "priority", Column: "priority", Label: "Priority", Kind: resource.Int, Default: int64(100)},
		resource.Status("active", "inactive")},
}

var TimeBands = &resource.Def{
	Key: "commercial.time_band", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/time-bands", Table: "commercial.time_bands",
	Name: "Time Band", Plural: "Time Bands", Tag: "Pricing", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "start_time, code, id",
	Fields: []resource.Field{code20("Code"), resource.Name(),
		{Name: "session", Column: "session", Label: "Session", Kind: resource.Enum, Enum: []string{"morning", "afternoon", "night", "other"}, Required: true, Filter: true},
		{Name: "startTime", Column: "start_time", Label: "Start (HH:MM)", Kind: resource.String, Required: true, Pattern: timeRe, PatternMsg: "HH:MM"},
		{Name: "endTime", Column: "end_time", Label: "End (HH:MM)", Kind: resource.String, Required: true, Pattern: timeRe, PatternMsg: "HH:MM"},
		resource.Status("active", "inactive")},
}

var RatePlans = &resource.Def{
	Key: "commercial.rate_plan", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/rate-plans", Table: "commercial.rate_plans",
	Name: "Rate Plan", Plural: "Rate Plans", Tag: "Pricing", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "effective_from DESC, code, id",
	Fields: []resource.Field{code20("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		{Name: "businessLine", Column: "business_line", Label: "Business Line", Kind: resource.Enum, Enum: []string{"golf"}, Default: "golf", Filter: true},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Nett / ++", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Required: true, Filter: true},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Upper: true, Default: "IDR"},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Required: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		resource.Status("active", "inactive")},
}

var PricingRules = &resource.Def{
	Key: "commercial.pricing_rule", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/pricing-rules", Table: "commercial.pricing_rules",
	Name: "Pricing Rule", Plural: "Pricing Rules", Tag: "Pricing", PropertyScoped: true, NoDelete: true, OrderBy: "charge_type, priority, code, version DESC",
	Fields: []resource.Field{
		{Name: "ratePlanId", Column: "rate_plan_id", Label: "Rate Plan", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.rate_plans", SameProperty: true, Label: "rate plan"}},
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true,
			Pattern: code40Re, PatternMsg: "1–40 characters: A–Z, 0–9, - or _", Search: true, Filter: true},
		{Name: "version", Column: "version", Label: "Version", Kind: resource.Int, ReadOnly: true},
		resource.Name(),
		{Name: "chargeType", Column: "charge_type", Label: "Charge Type", Kind: resource.Enum, Enum: ChargeTypes, Default: "golf_round", Filter: true},
		{Name: "segment", Column: "segment", Label: "Player Segment (empty = any)", Kind: resource.Enum, Enum: Segments, Filter: true},
		{Name: "dayTypeId", Column: "day_type_id", Label: "Day Type (empty = any)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.day_types", SameProperty: true, Label: "day type"}},
		{Name: "timeBandId", Column: "time_band_id", Label: "Time Band (empty = any)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.time_bands", SameProperty: true, Label: "time band"}},
		{Name: "playingRouteId", Column: "playing_route_id", Label: "Playing Route (empty = any)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "golf.playing_routes", SameProperty: true, Label: "playing route"}},
		{Name: "channel", Column: "channel", Label: "Channel (empty = any)", Kind: resource.Enum, Enum: Channels, Filter: true},
		{Name: "peak", Column: "peak", Label: "Peak (empty = any)", Kind: resource.Bool},
		{Name: "price", Column: "price", Label: "Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "components", Column: "components", Label: "Price Components (all-in breakdown)", Kind: resource.JSON},
		{Name: "priority", Column: "priority", Label: "Priority (lower wins)", Kind: resource.Int, Default: int64(100)},
		{Name: "effectiveFrom", Column: "effective_from", Label: "Effective From", Kind: resource.Date, Required: true},
		{Name: "effectiveTo", Column: "effective_to", Label: "Effective To", Kind: resource.Date},
		resource.Status("active", "inactive"),
	},
}

func init() {
	PricingRules.Hooks = resource.Hooks{BeforeWrite: ruleBeforeWrite}
	TimeBands.Hooks = resource.Hooks{BeforeWrite: func(_ context.Context, _ pgx.Tx, v map[string]any, before map[string]any) error {
		st, en := str(v["startTime"]), str(v["endTime"])
		if before != nil {
			if st == "" {
				st = str(before["startTime"])
			}
			if en == "" {
				en = str(before["endTime"])
			}
		}
		if st != "" && en != "" && en <= st {
			return errs.Validation("invalid_time_band", "end must be after start", errs.Field("endTime", "invalid", "end must be after start"))
		}
		return nil
	}}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// Component is one part of an all-in price (FR-PRC-05).
type Component struct {
	Code      string `json:"code"`                // green_fee, caddy_fee, buggy_fee, hio, water, …
	Name      string `json:"name"`                // label on receipts and reports
	Type      string `json:"type"`                // amount | percent | remainder
	Value     string `json:"value,omitempty"`     // decimal; amount per unit or percent of the net
	Liability bool   `json:"liability,omitempty"` // held for a third party (caddy fee), not club revenue
	Amount    string `json:"amount,omitempty"`    // computed (snapshot)
}

// ParseComponents validates a components JSON value.
func ParseComponents(raw any) ([]Component, error) {
	if raw == nil {
		return nil, nil
	}
	var b []byte
	switch t := raw.(type) {
	case string:
		b = []byte(t)
	case []byte:
		b = t
	default:
		var err error
		if b, err = json.Marshal(t); err != nil {
			return nil, err
		}
	}
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var cs []Component
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, errs.Validation("invalid_components", "components must be a list", errs.Field("components", "invalid", "list of {code, name, type, value}"))
	}
	remainders := 0
	seen := map[string]bool{}
	pct := decimal.Zero
	for i, c := range cs {
		f := fmt.Sprintf("components[%d]", i)
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`).MatchString(c.Code) || seen[c.Code] {
			return nil, errs.Validation("invalid_components", "invalid component code", errs.Field(f, "invalid", "lower-case unique code, e.g. green_fee"))
		}
		seen[c.Code] = true
		if strings.TrimSpace(c.Name) == "" {
			return nil, errs.Validation("invalid_components", "component name required", errs.Field(f, "required", "name is required"))
		}
		switch c.Type {
		case "remainder":
			remainders++
		case "amount", "percent":
			d, err := decimal.NewFromString(c.Value)
			if err != nil || d.IsNegative() {
				return nil, errs.Validation("invalid_components", "invalid component value", errs.Field(f, "invalid", "non-negative decimal"))
			}
			if c.Type == "percent" {
				pct = pct.Add(d)
			}
		default:
			return nil, errs.Validation("invalid_components", "invalid component type", errs.Field(f, "invalid", "amount, percent or remainder"))
		}
	}
	if remainders > 1 {
		return nil, errs.Validation("invalid_components", "only one remainder component is allowed", errs.Field("components", "invalid", "one remainder at most"))
	}
	if pct.GreaterThan(decimal.NewFromInt(100)) {
		return nil, errs.Validation("invalid_components", "percent components exceed 100%", errs.Field("components", "invalid", "total percent ≤ 100"))
	}
	return cs, nil
}

// ruleBeforeWrite implements versioning (an effective version is immutable;
// the same code creates the next version) and conflict detection on save
// (FR-PRC-04).
func ruleBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	pid, _ := reqctx.Property(ctx)
	today := clock.Now().In(calendar.Location(ctx, tx)).Format("2006-01-02") // the club's date, not UTC
	if comps, ok := v["components"]; ok {
		if _, err := ParseComponents(comps); err != nil {
			return err
		}
	}
	if before != nil {
		if eff := str(before["effectiveFrom"]); eff <= today {
			for k := range v {
				if k != "status" && k != "effectiveTo" {
					return errs.Conflict("rule_already_effective", "this rule version is already effective; add a new version (same code) with a later effective date instead")
				}
			}
		}
	} else {
		var maxV *int
		if err := tx.QueryRow(ctx, `SELECT max(version) FROM commercial.pricing_rules WHERE property_id = $1 AND code = $2`, pid, v["code"]).Scan(&maxV); err != nil {
			return err
		}
		v["version"] = int64(1)
		if maxV != nil {
			if str(v["effectiveFrom"]) <= today {
				return errs.Validation("effective_date_past", "a new version must take effect after today",
					errs.Field("effectiveFrom", "past", "existing bookings keep their snapshot; choose a future date"))
			}
			v["version"] = int64(*maxV + 1)
			// The previous versions end the day before the new one starts.
			if _, err := tx.Exec(ctx, `UPDATE commercial.pricing_rules SET effective_to = ($3::date - 1)
				WHERE property_id = $1 AND code = $2 AND (effective_to IS NULL OR effective_to >= $3::date) AND effective_from < $3::date`,
				pid, v["code"], v["effectiveFrom"]); err != nil {
				return err
			}
		}
	}
	// merged view of the rule after the write
	m := map[string]any{}
	for k, x := range before {
		m[k] = x
	}
	for k, x := range v {
		m[k] = x
	}
	if str(m["status"]) == "inactive" {
		return nil
	}
	if to := str(m["effectiveTo"]); to != "" && to < str(m["effectiveFrom"]) {
		return errs.Validation("invalid_period", "effective to must not be before effective from", errs.Field("effectiveTo", "invalid", "on or after effective from"))
	}
	nullable := func(k string) any {
		x := m[k]
		if x == nil || str(x) == "" {
			return nil
		}
		return str(x)
	}
	var conflict string
	err := tx.QueryRow(ctx, `SELECT code || ' v' || version FROM commercial.pricing_rules
		WHERE property_id = $1 AND status = 'active' AND code <> $2 AND charge_type = $3
		  AND segment IS NOT DISTINCT FROM $4 AND day_type_id IS NOT DISTINCT FROM $5::uuid AND time_band_id IS NOT DISTINCT FROM $6::uuid
		  AND playing_route_id IS NOT DISTINCT FROM $7::uuid AND channel IS NOT DISTINCT FROM $8 AND peak IS NOT DISTINCT FROM $9::bool
		  AND priority = $10
		  AND daterange(effective_from, coalesce(effective_to, 'infinity'::date), '[]') && daterange($11::date, coalesce($12::date, 'infinity'::date), '[]')
		  AND corporate_account_id IS NOT DISTINCT FROM $13::uuid AND holiday IS NOT DISTINCT FROM $14::bool
		LIMIT 1`,
		pid, m["code"], str(m["chargeType"]), nullable("segment"), nullable("dayTypeId"), nullable("timeBandId"), nullable("playingRouteId"),
		nullable("channel"), nullable("peak"), m["priority"], str(m["effectiveFrom"]), nullable("effectiveTo"), nullable("corporateAccountId"),
		nullable("holiday")).Scan(&conflict)
	if err == nil {
		return errs.Conflict("rule_conflict", "rule "+conflict+" matches the same segment, day type, time band, route, channel and peak with the same priority in an overlapping period; change the priority or the dimensions")
	}
	if !dbtx.IsNoRows(err) {
		return err
	}
	return nil
}

// ── resolution ────────────────────────────────────────────────────────────

// PriceQuery describes what is being priced.
type PriceQuery struct {
	Property       uuid.UUID
	ChargeType     string     // default golf_round
	Segments       []string   // eligible segments; the cheapest resolving one wins
	PlayAt         time.Time  // tee time (UTC)
	PlayDate       time.Time  // local business date
	LocalTime      string     // HH:MM local
	Session        string     // morning | afternoon | night
	DayTypeCode    string     // optional; computed from the calendar when empty
	PlayingRouteID *uuid.UUID // optional
	Channel        string
	Peak           *bool
	Quantity       int
	// PRD P3 (additive): contract rate, holiday rate and promotions.
	CorporateAccountID *uuid.UUID // Corporate Rate (contract rate) of a corporate account
	CustomerID         *uuid.UUID // promotion eligibility (membership type, CRM segment, limits)
	PromoCodes         []string   // promo codes entered
	NoPromotions       bool       // list price only (e.g. standalone component prices)
}

// PriceResult is a resolved price (not yet snapshotted).
type PriceResult struct {
	RuleID       uuid.UUID     `json:"ruleId"`
	RuleCode     string        `json:"ruleCode"`
	RuleVersion  int           `json:"ruleVersion"`
	RuleName     string        `json:"ruleName"`
	RatePlanID   uuid.UUID     `json:"ratePlanId"`
	RatePlanCode string        `json:"ratePlanCode"`
	ChargeType   string        `json:"chargeType"`
	Segment      string        `json:"segment"`
	DayTypeCode  string        `json:"dayTypeCode"`
	TimeBandCode string        `json:"timeBandCode"`
	PricingMode  string        `json:"pricingMode" enum:"nett,plus_plus"`
	Currency     string        `json:"currency"`
	UnitPrice    string        `json:"unitPrice"`
	Quantity     int           `json:"quantity"`
	NetAmount    string        `json:"netAmount"`
	TaxAmount    string        `json:"taxAmount"`
	ServiceAmt   string        `json:"serviceAmount"`
	Total        string        `json:"total"`
	Components   []Component   `json:"components"`
	TaxService   []Line        `json:"taxService"`
	Candidates   []SegmentCost `json:"candidates" doc:"Price per eligible segment (lowest wins)"`
	// PRD P3 (additive): promotions applied to the price (FR-PRM-06/07).
	ListUnitPrice string             `json:"listUnitPrice,omitempty" doc:"Unit price before promotions (set when a promotion applies)"`
	Discount      string             `json:"discount" doc:"Promotion discount"`
	Promotions    []AppliedPromotion `json:"promotions"`
}

// SegmentCost is the resolved unit price for one candidate segment.
type SegmentCost struct {
	Segment  string `json:"segment"`
	Price    string `json:"price"`
	RuleCode string `json:"ruleCode"`
}

// ErrNoRate means no pricing rule matches.
var ErrNoRate = errs.Conflict("no_rate", "no pricing rule matches this tee time and player segment; check Pricing Rules")

// DayTypeFor resolves the day type code of a local date: an explicit
// calendar override, then a holiday day type for public holidays, then the
// weekday mapping (FR-TEE-02, FR-PRC-01).
func DayTypeFor(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (string, error) {
	kind, override, _, found, err := org.CalendarEntry(ctx, q, property, day)
	if err != nil {
		return "", err
	}
	if found && override != "" {
		return override, nil
	}
	var code string
	if found && kind == "public_holiday" {
		err := q.QueryRow(ctx, `SELECT code FROM commercial.day_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
			AND includes_holidays ORDER BY priority, code LIMIT 1`, property).Scan(&code)
		if err == nil {
			return code, nil
		}
		if !dbtx.IsNoRows(err) {
			return "", err
		}
	}
	wd := int(day.Weekday())
	if wd == 0 {
		wd = 7
	}
	err = q.QueryRow(ctx, `SELECT code FROM commercial.day_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND $2 = ANY (string_to_array(weekdays, ',')) ORDER BY priority, code LIMIT 1`, property, fmt.Sprint(wd)).Scan(&code)
	if dbtx.IsNoRows(err) {
		return "", errs.Conflict("no_day_type", "no day type covers "+day.Format("Monday 2006-01-02")+"; configure Day Types")
	}
	return code, err
}

type candidate struct {
	ID          uuid.UUID
	Code        string
	Version     int
	Name        string
	PlanID      uuid.UUID
	PlanCode    string
	Mode        string
	Currency    string
	Segment     *string
	Specificity int
	Priority    int
	From        time.Time
	Price       decimal.Decimal
	Components  []Component
	TaxCodes    []string
}

// Resolve finds the price for q.
func Resolve(ctx context.Context, q dbtx.Querier, pq PriceQuery) (PriceResult, error) {
	if pq.ChargeType == "" {
		pq.ChargeType = "golf_round"
	}
	if pq.Quantity <= 0 {
		pq.Quantity = 1
	}
	if len(pq.Segments) == 0 {
		return PriceResult{}, errs.Validation("segment_required", "a player segment is required")
	}
	var err error
	if pq.DayTypeCode == "" {
		if pq.DayTypeCode, err = DayTypeFor(ctx, q, pq.Property, pq.PlayDate); err != nil {
			return PriceResult{}, err
		}
	}
	var dayTypeID *uuid.UUID
	{
		var x uuid.UUID
		err := q.QueryRow(ctx, `SELECT id FROM commercial.day_types WHERE property_id = $1 AND code = $2`, pq.Property, pq.DayTypeCode).Scan(&x)
		if err == nil {
			dayTypeID = &x
		} else if !dbtx.IsNoRows(err) {
			return PriceResult{}, err
		}
	}
	var timeBandID *uuid.UUID
	timeBandCode := ""
	{
		var x uuid.UUID
		// golf sessions only: bands of session "other" price other lines (e.g. the Sport Club courts)
		err := q.QueryRow(ctx, `SELECT id, code FROM commercial.time_bands WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
			AND session <> 'other' AND (session = $2 OR ($3 <> '' AND start_time <= $3 AND end_time >= $3))
			ORDER BY (session = $2) DESC, (start_time <= $3 AND end_time >= $3) DESC, start_time LIMIT 1`,
			pq.Property, pq.Session, pq.LocalTime).Scan(&x, &timeBandCode)
		if err == nil {
			timeBandID = &x
		} else if !dbtx.IsNoRows(err) {
			return PriceResult{}, err
		}
	}
	day := pq.PlayDate.Format("2006-01-02")
	holiday, err := ruleHoliday(ctx, q, pq.Property, pq.PlayDate)
	if err != nil {
		return PriceResult{}, err
	}
	rows, err := q.Query(ctx, `SELECT r.id, r.code, r.version, r.name, p.id, p.code, p.pricing_mode, p.currency, r.segment,
		(CASE WHEN r.segment IS NULL THEN 0 ELSE 1 END + CASE WHEN r.day_type_id IS NULL THEN 0 ELSE 1 END +
		 CASE WHEN r.time_band_id IS NULL THEN 0 ELSE 1 END + CASE WHEN r.playing_route_id IS NULL THEN 0 ELSE 1 END +
		 CASE WHEN r.channel IS NULL THEN 0 ELSE 1 END + CASE WHEN r.peak IS NULL THEN 0 ELSE 1 END +
		 CASE WHEN r.holiday IS NULL THEN 0 ELSE 1 END + CASE WHEN r.corporate_account_id IS NULL THEN 0 ELSE 2 END),
		r.priority, r.effective_from, r.price::text, r.components, r.tax_codes
		FROM commercial.pricing_rules r JOIN commercial.rate_plans p ON p.id = r.rate_plan_id
		WHERE r.property_id = $1 AND r.charge_type = $2 AND r.status = 'active' AND p.status = 'active' AND p.archived_at IS NULL
		  AND r.effective_from <= $3::date AND (r.effective_to IS NULL OR r.effective_to >= $3::date)
		  AND p.effective_from <= $3::date AND (p.effective_to IS NULL OR p.effective_to >= $3::date)
		  AND (r.segment IS NULL OR r.segment = ANY($4))
		  AND (r.day_type_id IS NULL OR r.day_type_id = $5::uuid)
		  AND (r.time_band_id IS NULL OR r.time_band_id = $6::uuid)
		  AND (r.playing_route_id IS NULL OR r.playing_route_id = $7::uuid)
		  AND (r.channel IS NULL OR r.channel = $8)
		  AND (r.peak IS NULL OR r.peak = $9::bool)
		  AND (r.holiday IS NULL OR r.holiday = $10::bool)
		  AND (r.corporate_account_id IS NULL OR r.corporate_account_id = $11::uuid)`,
		pq.Property, pq.ChargeType, day, pq.Segments, dayTypeID, timeBandID, pq.PlayingRouteID, pq.Channel, pq.Peak, holiday, pq.CorporateAccountID)
	if err != nil {
		return PriceResult{}, err
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		var price string
		var comps []byte
		if err := rows.Scan(&c.ID, &c.Code, &c.Version, &c.Name, &c.PlanID, &c.PlanCode, &c.Mode, &c.Currency, &c.Segment, &c.Specificity,
			&c.Priority, &c.From, &price, &comps, &c.TaxCodes); err != nil {
			rows.Close()
			return PriceResult{}, err
		}
		c.Price, _ = decimal.NewFromString(price)
		_ = json.Unmarshal(comps, &c.Components)
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PriceResult{}, err
	}
	// deterministic order (FR-PRC-04)
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Specificity != b.Specificity {
			return a.Specificity > b.Specificity
		}
		if !a.From.Equal(b.From) {
			return a.From.After(b.From)
		}
		if a.Version != b.Version {
			return a.Version > b.Version
		}
		return a.ID.String() < b.ID.String()
	})
	var best *candidate
	bestSeg := ""
	var costs []SegmentCost
	for _, seg := range pq.Segments {
		for i := range cands {
			c := &cands[i]
			if c.Segment != nil && *c.Segment != seg {
				continue
			}
			costs = append(costs, SegmentCost{Segment: seg, Price: c.Price.String(), RuleCode: c.Code})
			if best == nil || c.Price.LessThan(best.Price) {
				best, bestSeg = c, seg
			}
			break
		}
	}
	if best == nil {
		return PriceResult{}, ErrNoRate
	}
	res := PriceResult{RuleID: best.ID, RuleCode: best.Code, RuleVersion: best.Version, RuleName: best.Name, RatePlanID: best.PlanID,
		RatePlanCode: best.PlanCode, ChargeType: pq.ChargeType, Segment: bestSeg, DayTypeCode: pq.DayTypeCode, TimeBandCode: timeBandCode,
		PricingMode: best.Mode, Currency: best.Currency, Quantity: pq.Quantity, Candidates: costs}
	taxRules, err := RulesAt(ctx, q, pq.Property, pq.PlayAt)
	if err != nil {
		return PriceResult{}, err
	}
	// only the Tax & Service codes of the rule (empty = all): a property's
	// banquet service or POS PB1 must not split a golf all-in price
	taxRules = WithCodes(taxRules, best.TaxCodes)
	finish(&res, best.Price, best.Components, taxRules, pq.PlayAt)
	res.Discount, res.Promotions = "0", []AppliedPromotion{}
	if !pq.NoPromotions {
		if err := applyGolfPromotions(ctx, q, pq, &res, best.Price, best.Components, taxRules); err != nil {
			return PriceResult{}, err
		}
	}
	return res, nil
}

// finish computes amounts, tax & service and the component breakdown.
func finish(res *PriceResult, unit decimal.Decimal, comps []Component, taxRules []Rule, at time.Time) {
	qty := decimal.NewFromInt(int64(res.Quantity))
	gross := unit.Mul(qty)
	b := CalculateMode(taxRules, gross, res.Currency, at, res.PricingMode)
	res.UnitPrice = unit.StringFixed(places(res.Currency))
	res.NetAmount = b.NetAmount
	res.Total = b.Total
	res.TaxService = b.Lines
	tax, svc := decimal.Zero, decimal.Zero
	for _, l := range b.Lines {
		a, _ := decimal.NewFromString(l.Amount)
		if l.Kind == "tax" {
			tax = tax.Add(a)
		} else {
			svc = svc.Add(a)
		}
	}
	res.TaxAmount = tax.StringFixed(places(res.Currency))
	res.ServiceAmt = svc.StringFixed(places(res.Currency))
	net, _ := decimal.NewFromString(b.NetAmount)
	res.Components = Breakdown2Components(comps, net, qty, res.Currency, res.ChargeType)
}

// Breakdown2Components splits a net amount into components; the remainder
// component (or the first one) absorbs rounding so the parts add up exactly.
func Breakdown2Components(comps []Component, net, qty decimal.Decimal, currency, chargeType string) []Component {
	if len(comps) == 0 {
		name := map[string]string{"golf_round": "Green Fee", "caddy_fee": "Caddy Fee", "cart_fee": "Golf Cart Fee", "extra_cart": "Golf Cart Surcharge"}[chargeType]
		code := map[string]string{"golf_round": "green_fee", "caddy_fee": "caddy_fee", "cart_fee": "buggy_fee", "extra_cart": "buggy_surcharge"}[chargeType]
		if code == "" {
			code, name = chargeType, chargeType
		}
		return []Component{{Code: code, Name: name, Type: "remainder", Liability: chargeType == "caddy_fee", Amount: net.StringFixed(places(currency))}}
	}
	out := make([]Component, len(comps))
	used := decimal.Zero
	rem := -1
	for i, c := range comps {
		out[i] = c
		v, _ := decimal.NewFromString(c.Value)
		var a decimal.Decimal
		switch c.Type {
		case "amount":
			a = v.Mul(qty)
		case "percent":
			a = net.Mul(v).Div(decimal.NewFromInt(100))
		default:
			rem = i
			continue
		}
		a = a.Round(places(currency))
		out[i].Amount = a.StringFixed(places(currency))
		used = used.Add(a)
	}
	if rem < 0 {
		rem = 0
		first, _ := decimal.NewFromString(out[0].Amount)
		used = used.Sub(first)
	}
	out[rem].Amount = net.Sub(used).StringFixed(places(currency))
	return out
}

// Override is a manual price or discount (FR-PRC-09).
type Override struct {
	UnitPrice  decimal.Decimal `json:"unitPrice"`
	Reason     string          `json:"reason"`
	ApprovedBy *uuid.UUID      `json:"approvedBy,omitempty"`
	ApprovalID *uuid.UUID      `json:"approvalRequestId,omitempty"`
	ListPrice  string          `json:"listPrice"`
}

// ApplyOverride re-computes res with a manual unit price.
func ApplyOverride(ctx context.Context, q dbtx.Querier, property uuid.UUID, res PriceResult, unit decimal.Decimal, at time.Time) (PriceResult, error) {
	taxRules, err := RulesAt(ctx, q, property, at)
	if err != nil {
		return res, err
	}
	var codes []string
	if err := q.QueryRow(ctx, `SELECT tax_codes FROM commercial.pricing_rules WHERE id = $1`, res.RuleID).Scan(&codes); err != nil && !dbtx.IsNoRows(err) {
		return res, err
	}
	taxRules = WithCodes(taxRules, codes)
	var comps []Component
	for _, c := range res.Components {
		cc := c
		cc.Amount = ""
		if cc.Type == "" {
			cc.Type = "remainder"
		}
		comps = append(comps, cc)
	}
	// Fixed-amount components keep their amount; the remainder absorbs the discount.
	finish(&res, unit, comps, taxRules, at)
	// a manual price replaces promotions (PRD P3 FR-PRM-06)
	res.ListUnitPrice, res.Discount, res.Promotions = "", "0", []AppliedPromotion{}
	return res, nil
}

// SnapshotInput carries context stored with a snapshot.
type SnapshotInput struct {
	Query    PriceQuery
	Result   PriceResult
	Override *Override
	Context  map[string]any
}

// Snapshot stores an immutable pricing snapshot (FR-PRC-07) and returns its id.
func Snapshot(ctx context.Context, tx pgx.Tx, in SnapshotInput) (uuid.UUID, error) {
	sid := id.New()
	r := in.Result
	comps, _ := json.Marshal(r.Components)
	ts, _ := json.Marshal(r.TaxService)
	var ov []byte
	if in.Override != nil {
		ov, _ = json.Marshal(in.Override)
	}
	cx := in.Context
	if cx == nil {
		cx = map[string]any{}
	}
	cx["candidates"] = r.Candidates
	cxRaw, _ := json.Marshal(cx)
	var uid *uuid.UUID
	if p := authz.From(ctx); p != nil {
		uid = id.Ptr(p.UserID)
	}
	var ruleID, planID *uuid.UUID
	if r.RuleID != uuid.Nil {
		ruleID, planID = &r.RuleID, &r.RatePlanID
	}
	_, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_snapshots (id, property_id, charge_type, rule_id, rule_code, rule_version, rate_plan_id,
		rate_plan_code, segment, day_type_code, time_band_code, playing_route_id, channel, peak, play_at, currency, pricing_mode, list_price, quantity,
		net_amount, tax_amount, service_amount, total, components, tax_service, override, context, created_by, promotions)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::numeric,$19,$20::numeric,$21::numeric,$22::numeric,$23::numeric,$24,$25,$26,$27,$28,$29)`,
		sid, in.Query.Property, r.ChargeType, ruleID, nullStr(r.RuleCode), nullInt(r.RuleVersion), planID, nullStr(r.RatePlanCode), r.Segment,
		nullStr(r.DayTypeCode), nullStr(r.TimeBandCode), in.Query.PlayingRouteID, nullStr(in.Query.Channel), in.Query.Peak, in.Query.PlayAt,
		r.Currency, r.PricingMode, r.UnitPrice, r.Quantity, r.NetAmount, r.TaxAmount, r.ServiceAmt, r.Total, comps, ts, ov, cxRaw, uid,
		SnapshotPromotions(r.Promotions))
	if err != nil || len(r.Promotions) == 0 {
		return sid, err
	}
	// PRD P3 FR-PRM-07: the redemption of the promotions of this price
	return sid, recordSnapshot(ctx, tx, in.Query.Property, sid, r.Promotions, in.Query.CustomerID, in.Query.Channel, "golf", r.Currency)
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// SnapshotView is the API representation of a snapshot.
type SnapshotView struct {
	ID             uuid.UUID       `json:"id"`
	ChargeType     string          `json:"chargeType"`
	RuleID         *uuid.UUID      `json:"ruleId"`
	RuleCode       *string         `json:"ruleCode"`
	RuleVersion    *int            `json:"ruleVersion"`
	RatePlanCode   *string         `json:"ratePlanCode"`
	Segment        *string         `json:"segment"`
	DayTypeCode    *string         `json:"dayTypeCode"`
	TimeBandCode   *string         `json:"timeBandCode"`
	PlayingRouteID *uuid.UUID      `json:"playingRouteId"`
	Channel        *string         `json:"channel"`
	Peak           *bool           `json:"peak"`
	PlayAt         time.Time       `json:"playAt"`
	Currency       string          `json:"currency"`
	PricingMode    string          `json:"pricingMode"`
	ListPrice      string          `json:"listPrice"`
	Quantity       string          `json:"quantity"`
	NetAmount      string          `json:"netAmount"`
	TaxAmount      string          `json:"taxAmount"`
	ServiceAmount  string          `json:"serviceAmount"`
	Total          string          `json:"total"`
	Components     json.RawMessage `json:"components"`
	TaxService     json.RawMessage `json:"taxService"`
	Override       json.RawMessage `json:"override"`
	Context        json.RawMessage `json:"context"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// GetSnapshot loads a snapshot.
func GetSnapshot(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (SnapshotView, error) {
	var s SnapshotView
	var ov []byte
	err := q.QueryRow(ctx, `SELECT id, charge_type, rule_id, rule_code, rule_version, rate_plan_code, segment, day_type_code, time_band_code,
		playing_route_id, channel, peak, play_at, currency, pricing_mode, list_price::text, quantity::text, net_amount::text, tax_amount::text,
		service_amount::text, total::text, components, tax_service, override, context, created_at FROM commercial.pricing_snapshots WHERE id = $1`, sid).
		Scan(&s.ID, &s.ChargeType, &s.RuleID, &s.RuleCode, &s.RuleVersion, &s.RatePlanCode, &s.Segment, &s.DayTypeCode, &s.TimeBandCode,
			&s.PlayingRouteID, &s.Channel, &s.Peak, &s.PlayAt, &s.Currency, &s.PricingMode, &s.ListPrice, &s.Quantity, &s.NetAmount, &s.TaxAmount,
			&s.ServiceAmount, &s.Total, &s.Components, &s.TaxService, &ov, &s.Context, &s.CreatedAt)
	if dbtx.IsNoRows(err) {
		return s, errs.NotFound("pricing snapshot")
	}
	if ov == nil {
		ov = []byte("null")
	}
	s.Override = ov
	return s, err
}

// RateRow is a line of the structured rate table.
type RateRow struct {
	Segment  string
	DayType  string
	TimeBand string
	Price    string
	Currency string
	Mode     string
}

// RateTable lists the rules in effect on a date (website tariff table,
// FR-WEB-03) — the latest version of each code.
func RateTable(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, chargeType string) ([]RateRow, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (r.code) coalesce(r.segment, 'any'), coalesce(d.name, 'Every day'), coalesce(tb.name, 'All day'),
		r.price::text, p.currency, p.pricing_mode, coalesce(d.priority, 999), coalesce(tb.start_time, '00:00')
		FROM commercial.pricing_rules r JOIN commercial.rate_plans p ON p.id = r.rate_plan_id
		LEFT JOIN commercial.day_types d ON d.id = r.day_type_id LEFT JOIN commercial.time_bands tb ON tb.id = r.time_band_id
		WHERE r.property_id = $1 AND r.charge_type = $2 AND r.status = 'active' AND p.status = 'active'
		  AND r.effective_from <= $3::date AND (r.effective_to IS NULL OR r.effective_to >= $3::date)
		  AND p.effective_from <= $3::date AND (p.effective_to IS NULL OR p.effective_to >= $3::date)
		ORDER BY r.code, r.version DESC`, property, chargeType, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type sortable struct {
		RateRow
		prio  int
		start string
	}
	var list []sortable
	for rows.Next() {
		var s sortable
		if err := rows.Scan(&s.Segment, &s.DayType, &s.TimeBand, &s.Price, &s.Currency, &s.Mode, &s.prio, &s.start); err != nil {
			return nil, err
		}
		s.Price = dec(s.Price).StringFixed(places(s.Currency))
		list = append(list, s)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Segment != list[j].Segment {
			return list[i].Segment < list[j].Segment
		}
		if list[i].prio != list[j].prio {
			return list[i].prio < list[j].prio
		}
		return list[i].start < list[j].start
	})
	out := make([]RateRow, len(list))
	for i := range list {
		out[i] = list[i].RateRow
	}
	return out, rows.Err()
}

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}
