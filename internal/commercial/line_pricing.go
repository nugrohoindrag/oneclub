package commercial

// PRD P2 EP-02 Pricing Extension (multi-line, contract C4): sport club,
// classes, stay, meeting, driving range, vouchers and POS are priced by
// P1's versioned pricing rules extended with service type, item, unit &
// quantity, overtime, minimum, tax & service codes and revenue component.
// Day types are grouped in day type sets per line; resolution follows P1:
// the most specific rule wins, then the lower priority number, then the
// latest effective date. Golf keeps P1's Resolve by charge type.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// ServiceTypes are the priced services (FR-PRC-P2-01).
var ServiceTypes = []string{"golf", "sport_court", "facility_entry", "class_session", "class_registration", "class_package",
	"bungalow", "vip_suite", "meeting_room", "meeting_package", "equipment", "driving_range", "voucher_sale", "membership",
	"pos", "locker", "other"}

// Units of a priced service (FR-PRC-P2-05).
var Units = []string{"slot", "hour", "block", "night", "day_use_hour", "pax", "session", "package", "bucket", "ball", "entry",
	"item", "registration", "month", "year"}

func serviceTypeField(required bool) resource.Field {
	return resource.Field{Name: "serviceType", Column: "service_type", Label: "Service Type", Kind: resource.Enum, Enum: ServiceTypes,
		Required: required, Filter: true}
}

// DayTypeSets group day types per business line (FR-PRC-P2-03).
var DayTypeSets = &resource.Def{
	Key: "commercial.day_type_set", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/day-type-sets",
	Table: "commercial.day_type_sets", Name: "Day Type Set", Plural: "Day Type Sets", Tag: "Pricing", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{code20("Code"), resource.Name(), serviceTypeField(false), resource.Status("active", "inactive")},
}

// PackageRates are single-line packages (FR-PRC-P2-07).
var PackageRates = &resource.Def{
	Key: "commercial.package_rate", Module: "commercial", Perm: "commercial.pricing", Path: "/api/v1/commercial/package-rates",
	Table: "commercial.package_rates", Name: "Package Rate", Plural: "Package Rates", Tag: "Pricing", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{code20("Code"), resource.Name(), serviceTypeField(true),
		{Name: "durationMinutes", Column: "duration_minutes", Label: "Duration (minutes)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "coffeeBreaks", Column: "coffee_breaks", Label: "Coffee Breaks", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "minPax", Column: "min_pax", Label: "Minimum Pax", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "adults", Column: "adults", Label: "Adults", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "children", Column: "children", Label: "Children", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "maxChildAge", Column: "max_child_age", Label: "Maximum Child Age", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "inclusions", Column: "inclusions", Label: "Inclusions", Kind: resource.JSONList, Default: "[]"},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// PriceRequest asks for the price of a service of any line (C4).
type PriceRequest struct {
	ServiceType string     `json:"serviceType" enum:"golf,sport_court,facility_entry,class_session,class_registration,class_package,bungalow,vip_suite,meeting_room,meeting_package,equipment,driving_range,voucher_sale,membership,pos,locker,other"`
	ItemRef     string     `json:"itemRef,omitempty" doc:"Resource type code, resource id, product id, package code … (exact match beats generic rules)"`
	Segment     string     `json:"segment,omitempty" doc:"Default: any segment"`
	Start       time.Time  `json:"start" doc:"Service start; decides day type, time band and the rule version in force"`
	End         *time.Time `json:"end,omitempty" doc:"Service end for time-based units (slot, hour, block, night, day_use_hour)"`
	Quantity    int        `json:"quantity,omitempty" doc:"Pax, sessions, items …; default 1"`
	RatePlan    string     `json:"ratePlan,omitempty" doc:"Rate plan code (stay)"`
	Package     string     `json:"package,omitempty" doc:"Package rate code"`
	Channel     string     `json:"channel,omitempty"`
	Persist     bool       `json:"persist,omitempty" doc:"Store an immutable pricing snapshot"`
}

// LineComponent is one all-in component of a line price.
type LineComponent struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Amount           string `json:"amount"`
	RevenueComponent string `json:"revenueComponent,omitempty"`
	Liability        bool   `json:"liability,omitempty" doc:"Held for a partner (e.g. caddy fee), not club revenue"`
}

type lineComponentDef struct {
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	Amount           *string `json:"amount"`
	Percent          *string `json:"percent"`
	RevenueComponent string  `json:"revenueComponent"`
	Liability        bool    `json:"liability"`
}

// LinePrice is a resolved price of a service (optionally with its snapshot).
type LinePrice struct {
	SnapshotID       *uuid.UUID      `json:"snapshotId"`
	RuleID           uuid.UUID       `json:"ruleId"`
	RuleCode         string          `json:"ruleCode"`
	RuleVersion      int             `json:"ruleVersion"`
	RuleName         string          `json:"ruleName"`
	ServiceType      string          `json:"serviceType"`
	ItemRef          *string         `json:"itemRef"`
	Segment          string          `json:"segment" doc:"Empty: the rule applies to any segment"`
	DayType          *string         `json:"dayType"`
	TimeBand         *string         `json:"timeBand"`
	RatePlan         *string         `json:"ratePlan"`
	Package          *string         `json:"package"`
	Unit             string          `json:"unit"`
	Units            string          `json:"units"`
	UnitPrice        string          `json:"unitPrice"`
	Overtime         string          `json:"overtime" doc:"Overtime amount above the block (VIP Suite)"`
	Gross            string          `json:"gross" doc:"Units × unit price + overtime, in the rule pricing mode"`
	Tax              Breakdown       `json:"tax"`
	Components       []LineComponent `json:"components"`
	RevenueComponent string          `json:"revenueComponent"`
	Explanation      []string        `json:"explanation"`
}

// Net / Total / ServiceAmount / TaxAmount expose the breakdown as decimals for charges.
func (p LinePrice) Net() decimal.Decimal           { return dec(p.Tax.NetAmount) }
func (p LinePrice) Total() decimal.Decimal         { return dec(p.Tax.Total) }
func (p LinePrice) ServiceAmount() decimal.Decimal { return SumKind(p.Tax.Lines, "service") }
func (p LinePrice) TaxAmount() decimal.Decimal     { return SumKind(p.Tax.Lines, "tax") }

func SumKind(ls []Line, kind string) decimal.Decimal {
	t := decimal.Zero
	for _, l := range ls {
		if l.Kind == kind {
			t = t.Add(dec(l.Amount))
		}
	}
	return t
}

type ruleRow struct {
	ID               uuid.UUID       `db:"id"`
	Code             string          `db:"code"`
	Version          int             `db:"version"`
	Name             string          `db:"name"`
	ItemRef          *string         `db:"item_ref"`
	Segment          *string         `db:"segment"`
	DayTypeID        *uuid.UUID      `db:"day_type_id"`
	DayTypeCode      *string         `db:"day_type_code"`
	DayTypeSet       *uuid.UUID      `db:"day_type_set_id"`
	TimeBandID       *uuid.UUID      `db:"time_band_id"`
	TimeBandCode     *string         `db:"time_band_code"`
	BandStart        *string         `db:"band_start"`
	BandEnd          *string         `db:"band_end"`
	RatePlanCode     *string         `db:"rate_plan_code"`
	RatePlanMin      *int            `db:"rate_plan_min_nights"`
	PackageCode      *string         `db:"package_code"`
	PackageMinPax    *int            `db:"package_min_pax"`
	Channel          *string         `db:"channel"`
	Unit             string          `db:"unit"`
	UnitMinutes      *int            `db:"unit_minutes"`
	PackageQuantity  int             `db:"package_quantity"`
	MinQuantity      int             `db:"min_quantity"`
	MinPolicy        string          `db:"min_policy"`
	Price            string          `db:"price"`
	OvertimePrice    *string         `db:"overtime_price"`
	Currency         string          `db:"currency"`
	PricingMode      string          `db:"pricing_mode"`
	TaxCodes         []string        `db:"tax_codes"`
	RevenueComponent string          `db:"revenue_component"`
	Components       json.RawMessage `db:"components"`
	Priority         int             `db:"priority"`
	EffectiveFrom    time.Time       `db:"effective_from"`
}

// Pricer resolves prices of every line; it is stateless and safe to share.
type Pricer struct{}

// dayTypesOn returns, per day type set, the day type of a local date: a
// public holiday uses the set's holiday day type, otherwise the weekday
// mapping; within a set the lower priority number wins (P1 FR-PRC-01).
func dayTypesOn(ctx context.Context, q dbtx.Querier, property uuid.UUID, local time.Time) (map[uuid.UUID]uuid.UUID, error) {
	holiday, err := calendar.IsHoliday(ctx, q, property, local)
	if err != nil {
		return nil, err
	}
	wd := int(local.Weekday())
	if wd == 0 {
		wd = 7
	}
	rows, err := q.Query(ctx, `SELECT id, coalesce(day_type_set_id, '00000000-0000-0000-0000-000000000000'::uuid), weekdays, includes_holidays
		FROM commercial.day_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY priority, code`, property)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type dt struct {
		id, set  uuid.UUID
		days     []string
		holidays bool
	}
	var all []dt
	for rows.Next() {
		var x dt
		var days string
		if err := rows.Scan(&x.id, &x.set, &days, &x.holidays); err != nil {
			return nil, err
		}
		x.days = strings.Split(days, ",")
		all = append(all, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]uuid.UUID{}
	if holiday {
		for _, x := range all {
			if _, done := out[x.set]; !done && x.holidays {
				out[x.set] = x.id
			}
		}
	}
	for _, x := range all {
		if _, done := out[x.set]; !done && slices.Contains(x.days, fmt.Sprint(wd)) {
			out[x.set] = x.id
		}
	}
	return out, nil
}

// Resolve prices a request of any line except golf tee times.
func (Pricer) Resolve(ctx context.Context, q dbtx.Querier, property uuid.UUID, req PriceRequest) (LinePrice, error) {
	if !slices.Contains(ServiceTypes, req.ServiceType) {
		return LinePrice{}, handle.Invalid("serviceType", "invalid_service_type", "unknown service type "+req.ServiceType)
	}
	if req.Segment == "any" {
		req.Segment = ""
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}
	if req.Start.IsZero() {
		req.Start = time.Now()
	}
	loc := calendar.Location(ctx, q)
	local := req.Start.In(loc)
	day := local.Format("2006-01-02")
	dts, err := dayTypesOn(ctx, q, property, local)
	if err != nil {
		return LinePrice{}, err
	}
	rules, err := handle.List[ruleRow](q.Query(ctx, `SELECT r.id, r.code, r.version, r.name, r.item_ref, r.segment, r.day_type_id,
		dt.code AS day_type_code, coalesce(dt.day_type_set_id, '00000000-0000-0000-0000-000000000000'::uuid) AS day_type_set_id,
		r.time_band_id, tb.code AS time_band_code, tb.start_time AS band_start, tb.end_time AS band_end,
		rp.code AS rate_plan_code, rp.min_nights AS rate_plan_min_nights, pk.code AS package_code, pk.min_pax AS package_min_pax,
		r.channel, r.unit, r.unit_minutes, r.package_quantity, r.min_quantity, r.min_policy, r.price::text AS price, r.overtime_price::text AS overtime_price,
		coalesce(r.currency, rp.currency, (SELECT currency FROM platform.instance)) AS currency,
		coalesce(r.pricing_mode, rp.pricing_mode, 'nett') AS pricing_mode, r.tax_codes,
		coalesce(r.revenue_component, 'other') AS revenue_component, r.components, r.priority, r.effective_from
		FROM commercial.pricing_rules r
		LEFT JOIN commercial.day_types dt ON dt.id = r.day_type_id
		LEFT JOIN commercial.time_bands tb ON tb.id = r.time_band_id
		LEFT JOIN commercial.rate_plans rp ON rp.id = r.rate_plan_id
		LEFT JOIN commercial.package_rates pk ON pk.id = r.package_rate_id
		WHERE r.property_id = $1 AND r.service_type = $2 AND r.status = 'active'
		  AND r.effective_from <= $3::date AND (r.effective_to IS NULL OR r.effective_to >= $3::date)`, property, req.ServiceType, day))
	if err != nil {
		return LinePrice{}, err
	}
	nights := 0
	durMin := 0.0
	if req.End != nil {
		if !req.End.After(req.Start) {
			return LinePrice{}, handle.Invalid("end", "invalid_period", "end must be after start")
		}
		durMin = req.End.Sub(req.Start).Minutes()
		ls, le := local, req.End.In(loc)
		nights = int(time.Date(le.Year(), le.Month(), le.Day(), 0, 0, 0, 0, time.UTC).Sub(time.Date(ls.Year(), ls.Month(), ls.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	}
	clockStr := local.Format("15:04")
	type cand struct {
		r     ruleRow
		score int
	}
	var cands []cand
	for _, r := range rules {
		score := 0
		if r.ItemRef != nil {
			if *r.ItemRef != req.ItemRef {
				continue
			}
			score += 32
		}
		if r.Segment != nil {
			if *r.Segment != req.Segment {
				continue
			}
			score += 16
		}
		if r.RatePlanCode != nil {
			if *r.RatePlanCode != req.RatePlan {
				continue
			}
			n := nights
			if n == 0 {
				n = req.Quantity
			}
			if r.RatePlanMin != nil && n < *r.RatePlanMin {
				continue // e.g. Long Stay below its minimum nights
			}
			score += 8
		} else if req.RatePlan != "" && req.ServiceType == "bungalow" {
			score-- // a generic rule loses against the requested plan
		}
		if r.PackageCode != nil {
			if *r.PackageCode != req.Package {
				continue
			}
			score += 8
		} else if req.Package != "" {
			continue // a package request needs a package rule
		}
		if r.DayTypeID != nil {
			if r.DayTypeSet == nil || dts[*r.DayTypeSet] != *r.DayTypeID {
				continue
			}
			score += 4
		}
		if r.TimeBandID != nil {
			if r.BandStart == nil || clockStr < *r.BandStart || clockStr >= *r.BandEnd {
				continue
			}
			score += 2
		}
		if r.Channel != nil {
			if *r.Channel != req.Channel {
				continue
			}
			score++
		}
		cands = append(cands, cand{r, score})
	}
	if len(cands) == 0 {
		return LinePrice{}, errs.Validation("no_price", fmt.Sprintf("no pricing rule matches %s / %s / %s on %s",
			req.ServiceType, nonEmpty(req.ItemRef, "any item"), nonEmpty(req.Segment, "any segment"), local.Format("Mon 2006-01-02 15:04")))
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		if cands[i].r.Priority != cands[j].r.Priority {
			return cands[i].r.Priority < cands[j].r.Priority
		}
		if !cands[i].r.EffectiveFrom.Equal(cands[j].r.EffectiveFrom) {
			return cands[i].r.EffectiveFrom.After(cands[j].r.EffectiveFrom)
		}
		return cands[i].r.Version > cands[j].r.Version
	})
	r := cands[0].r
	price := dec(r.Price)
	expl := []string{fmt.Sprintf("rule %s v%d (%s)", r.Code, r.Version, r.Name)}
	units := decimal.NewFromInt(int64(req.Quantity))
	overtime := decimal.Zero
	unitMin := 60.0
	if r.UnitMinutes != nil {
		unitMin = float64(*r.UnitMinutes)
	}
	switch r.Unit {
	case "slot", "hour", "day_use_hour":
		if req.End != nil {
			units = decimal.NewFromFloat(math.Ceil(durMin / unitMin))
		}
	case "block":
		units = decimal.NewFromInt(1)
		if req.End != nil && durMin > unitMin {
			if r.OvertimePrice == nil {
				return LinePrice{}, errs.Validation("overtime_not_priced", "the booking is longer than the block and the rule has no overtime price")
			}
			ot := dec(*r.OvertimePrice)
			hours := math.Ceil((durMin - unitMin) / 60)
			overtime = ot.Mul(decimal.NewFromFloat(hours))
			expl = append(expl, fmt.Sprintf("overtime %v h × %s", hours, ot.StringFixed(0)))
		}
	case "night":
		if req.End != nil {
			if nights < 1 {
				nights = 1
			}
			units = decimal.NewFromInt(int64(nights))
		}
	}
	min := r.MinQuantity
	if r.PackageMinPax != nil && *r.PackageMinPax > min {
		min = *r.PackageMinPax
	}
	if min > 0 && units.LessThan(decimal.NewFromInt(int64(min))) {
		if r.MinPolicy == "reject" {
			return LinePrice{}, errs.Validation("below_minimum", fmt.Sprintf("minimum %d %s required by rule %s", min, r.Unit, r.Code))
		}
		expl = append(expl, fmt.Sprintf("charged the minimum of %d %s", min, r.Unit))
		units = decimal.NewFromInt(int64(min))
	}
	gross := units.Mul(price).Add(overtime)
	taxRules, err := RulesAt(ctx, q, property, req.Start)
	if err != nil {
		return LinePrice{}, err
	}
	if len(r.TaxCodes) > 0 {
		var sel []Rule
		for _, t := range taxRules {
			if slices.Contains(r.TaxCodes, t.Code) {
				sel = append(sel, t)
			}
		}
		taxRules = sel
	}
	b := CalculateMode(taxRules, gross, r.Currency, req.Start, r.PricingMode)
	comps := []LineComponent{}
	var defs []lineComponentDef
	_ = json.Unmarshal(r.Components, &defs)
	for _, c := range defs {
		amt := decimal.Zero
		switch {
		case c.Amount != nil:
			amt = dec(*c.Amount).Mul(units)
		case c.Percent != nil:
			amt = gross.Mul(dec(*c.Percent)).Div(hundred).Round(places(r.Currency))
		}
		comps = append(comps, LineComponent{Code: c.Code, Name: c.Name, Amount: amt.String(), RevenueComponent: c.RevenueComponent, Liability: c.Liability})
	}
	seg := ""
	if r.Segment != nil {
		seg = *r.Segment
	}
	return LinePrice{RuleID: r.ID, RuleCode: r.Code, RuleVersion: r.Version, RuleName: r.Name, ServiceType: req.ServiceType, ItemRef: r.ItemRef,
		Segment: seg, DayType: r.DayTypeCode, TimeBand: r.TimeBandCode, RatePlan: r.RatePlanCode, Package: r.PackageCode, Unit: r.Unit,
		Units: units.String(), UnitPrice: price.String(), Overtime: overtime.String(), Gross: gross.String(), Tax: b, Components: comps,
		RevenueComponent: r.RevenueComponent, Explanation: expl}, nil
}

var hundred = decimal.NewFromInt(100)

func nonEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Snapshot stores the immutable pricing snapshot of res (FR-PRC-P2-09) in
// P1's snapshot table.
func (Pricer) Snapshot(ctx context.Context, tx pgx.Tx, property uuid.UUID, req PriceRequest, res *LinePrice) (uuid.UUID, error) {
	sid := id.New()
	inputs, _ := json.Marshal(req)
	taxLines, _ := json.Marshal(res.Tax.Lines)
	comps, _ := json.Marshal(res.Components)
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_snapshots (id, property_id, charge_type, rule_id, rule_code, rule_version, rate_plan_code,
		segment, day_type_code, time_band_code, channel, play_at, currency, pricing_mode, list_price, quantity, net_amount, tax_amount, service_amount,
		total, components, tax_service, context, created_by, service_type, item_ref, unit, package_rate, gross_amount)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::numeric,$16::numeric,$17::numeric,$18::numeric,$19::numeric,$20::numeric,
		$21,$22,$23,$24,$3,$25,$26,$27,$28::numeric)`,
		sid, property, res.ServiceType, res.RuleID, res.RuleCode, res.RuleVersion, res.RatePlan, nullStr(res.Segment), res.DayType, res.TimeBand,
		nullStr(req.Channel), req.Start, res.Tax.Currency, res.Tax.PricingMode, res.UnitPrice, res.Units, res.Tax.NetAmount, res.TaxAmount().String(),
		res.ServiceAmount().String(), res.Tax.Total, comps, taxLines, inputs, actorPtr(ctx), res.ItemRef, res.Unit, res.Package, res.Gross); err != nil {
		return uuid.Nil, err
	}
	res.SnapshotID = &sid
	return sid, nil
}

// Price resolves and snapshots in one step (the common path for charges).
func (p Pricer) Price(ctx context.Context, tx pgx.Tx, property uuid.UUID, req PriceRequest) (LinePrice, error) {
	res, err := p.Resolve(ctx, tx, property, req)
	if err != nil {
		return res, err
	}
	_, err = p.Snapshot(ctx, tx, property, req, &res)
	return res, err
}

// ManualSnapshot stores a snapshot for a manually set price (override with
// reason, FR-POS-03 / P1 FR-PRC-09) or a product price from the POS menu.
func (Pricer) ManualSnapshot(ctx context.Context, tx pgx.Tx, property uuid.UUID, serviceType, itemRef, segment string, units decimal.Decimal,
	unitPrice decimal.Decimal, mode string, taxCodes []string, at time.Time, override map[string]any) (uuid.UUID, Breakdown, error) {
	taxRules, err := RulesAt(ctx, tx, property, at)
	if err != nil {
		return uuid.Nil, Breakdown{}, err
	}
	if len(taxCodes) > 0 {
		var sel []Rule
		for _, t := range taxRules {
			if slices.Contains(taxCodes, t.Code) {
				sel = append(sel, t)
			}
		}
		taxRules = sel
	}
	var cur string
	if err := tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur); err != nil {
		return uuid.Nil, Breakdown{}, err
	}
	gross := units.Mul(unitPrice)
	b := CalculateMode(taxRules, gross, cur, at, mode)
	sid := id.New()
	taxLines, _ := json.Marshal(b.Lines)
	var ov []byte
	if override != nil {
		ov, _ = json.Marshal(override)
	}
	if segment == "any" {
		segment = ""
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.pricing_snapshots (id, property_id, charge_type, service_type, item_ref, segment, unit, quantity,
		list_price, gross_amount, net_amount, service_amount, tax_amount, total, currency, pricing_mode, tax_service, override, play_at, created_by)
		VALUES ($1,$2,$3,$3,$4,$5,'item',$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13,$14,$15,$16,$17,$18)`,
		sid, property, serviceType, nullStr(itemRef), nullStr(segment), units.String(), unitPrice.String(), gross.String(), b.NetAmount,
		SumKind(b.Lines, "service").String(), SumKind(b.Lines, "tax").String(), b.Total, cur, mode, taxLines, ov, at, actorPtr(ctx)); err != nil {
		return uuid.Nil, b, err
	}
	return sid, b, nil
}

// PublicRate is one row of the public structured rate table (FR-WEB-P2-05).
type PublicRate struct {
	ServiceType string  `json:"serviceType" db:"service_type"`
	Name        string  `json:"name" db:"name"`
	ItemRef     *string `json:"itemRef" db:"item_ref"`
	Segment     *string `json:"segment" db:"segment"`
	DayType     *string `json:"dayType" db:"day_type"`
	TimeBand    *string `json:"timeBand" db:"time_band"`
	RatePlan    *string `json:"ratePlan" db:"rate_plan"`
	Package     *string `json:"package" db:"package"`
	Unit        string  `json:"unit" db:"unit"`
	Quantity    int     `json:"packageQuantity" db:"package_quantity"`
	Price       string  `json:"price" db:"price"`
	PricingMode string  `json:"pricingMode" db:"pricing_mode"`
	Currency    string  `json:"currency" db:"currency"`
}

// LineServiceTypes maps public website lines to service types.
var LineServiceTypes = map[string][]string{
	"sportclub": {"sport_court", "facility_entry", "class_session", "class_registration", "class_package", "locker"},
	"stay":      {"bungalow", "vip_suite"},
	"meeting":   {"meeting_room", "meeting_package", "equipment"},
	"range":     {"driving_range"},
	"vouchers":  {"voucher_sale"},
}

// PublicRates lists the current public rules of a line (members-only and
// corporate rates are not published).
func PublicRates(ctx context.Context, q dbtx.Querier, property uuid.UUID, types []string) ([]PublicRate, error) {
	out, err := handle.List[PublicRate](q.Query(ctx, `SELECT DISTINCT ON (r.code) r.service_type, r.name, r.item_ref, r.segment,
		dt.name AS day_type, tb.name AS time_band, rp.name AS rate_plan, pk.name AS package, r.unit, r.package_quantity,
		r.price::text AS price, coalesce(r.pricing_mode, rp.pricing_mode, 'nett') AS pricing_mode,
		coalesce(r.currency, rp.currency, (SELECT currency FROM platform.instance)) AS currency
		FROM commercial.pricing_rules r
		LEFT JOIN commercial.day_types dt ON dt.id = r.day_type_id
		LEFT JOIN commercial.time_bands tb ON tb.id = r.time_band_id
		LEFT JOIN commercial.rate_plans rp ON rp.id = r.rate_plan_id
		LEFT JOIN commercial.package_rates pk ON pk.id = r.package_rate_id
		WHERE r.property_id = $1 AND r.service_type = ANY($2) AND r.status = 'active' AND r.effective_from <= current_date
		AND (r.effective_to IS NULL OR r.effective_to >= current_date) AND coalesce(r.channel, 'website') = 'website'
		AND coalesce(r.segment, '') NOT IN ('member', 'corporate')
		ORDER BY r.code, r.version DESC`, property, types))
	for i := range out {
		out[i].Price = dec(out[i].Price).String()
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ServiceType != out[j].ServiceType {
			return out[i].ServiceType < out[j].ServiceType
		}
		return out[i].Name < out[j].Name
	})
	return out, err
}

// RatePlanInfo is a stay rate plan as other modules need it.
type RatePlanInfo struct {
	Code              string   `db:"code"`
	Name              string   `db:"name"`
	ServiceType       *string  `db:"service_type"`
	MinNights         int      `db:"min_nights"`
	IncludesBreakfast bool     `db:"includes_breakfast"`
	DayUse            bool     `db:"day_use"`
	FacilityAccess    []string `db:"facility_access"`
}

// RatePlan returns an active rate plan by code (nil when unknown).
func RatePlan(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (*RatePlanInfo, error) {
	rows, err := q.Query(ctx, `SELECT code, name, service_type, min_nights, includes_breakfast, day_use, facility_access FROM commercial.rate_plans
		WHERE property_id = $1 AND code = $2 AND status = 'active' AND archived_at IS NULL`, property, code)
	rp, err := handle.One[RatePlanInfo](rows, err, "rate plan")
	if errs.Is(err, errs.KindNotFound) {
		return nil, nil
	}
	return &rp, err
}

func actorPtr(ctx context.Context) *uuid.UUID {
	if u := handle.UserID(ctx); u != uuid.Nil {
		return &u
	}
	return nil
}
