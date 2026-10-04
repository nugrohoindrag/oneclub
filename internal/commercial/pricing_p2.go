package commercial

// Contract C4 of PRD P2 (§5.4.2): pricing of every business line on P1's
// pricing tables. P1's resources gain the P2 dimensions additively (this
// file runs its init after pricing.go), golf rules keep P1's validation, and
// other lines resolve through pricing:resolve-line next to P1's
// pricing:resolve. Review: P1 developer.

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/resource"
)

// P2 segments and channels of the other lines (FR-PRC-P2-02); an empty
// segment matches any.
var (
	p2Segments = []string{"walk_in", "student", "child", "residence", "corporate", "family", "staying_guest"}
	p2Channels = []string{"ops"}
)

func init() {
	extendEnum(PricingRules, "segment", p2Segments...)
	extendEnum(PricingRules, "channel", p2Channels...)
	extendEnum(PricingRules, "chargeType", "other")
	extendEnum(RatePlans, "businessLine", "sportclub", "stay", "pos", "membership", "voucher", "other")
	relax(PricingRules, "ratePlanId") // golf rules still require it (ruleP2BeforeWrite)
	relax(RatePlans, "effectiveFrom") // default today
	relax(RatePlans, "pricingMode")   // default nett
	relax(TimeBands, "session")       // default other
	setDefault(RatePlans, "pricingMode", "nett")
	setDefault(TimeBands, "session", "other")
	DayTypes.Fields = insertBeforeStatus(DayTypes.Fields, []resource.Field{
		{Name: "dayTypeSetId", Column: "day_type_set_id", Label: "Day Type Set (empty = golf)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "commercial.day_type_sets", SameProperty: true, Label: "day type set"}}})
	TimeBands.Fields = insertBeforeStatus(TimeBands.Fields, []resource.Field{serviceTypeField(false)})
	RatePlans.Fields = insertBeforeStatus(RatePlans.Fields, []resource.Field{serviceTypeField(false),
		{Name: "minNights", Column: "min_nights", Label: "Minimum Nights", Kind: resource.Int, Default: int64(1), Min: resource.Min(0)},
		{Name: "includesBreakfast", Column: "includes_breakfast", Label: "Includes Breakfast", Kind: resource.Bool, Default: false},
		{Name: "dayUse", Column: "day_use", Label: "Day-use", Kind: resource.Bool, Default: false},
		{Name: "facilityAccess", Column: "facility_access", Label: "Facility Access (facility types)", Kind: resource.StringList, Default: []string{}}})
	PricingRules.Fields = insertBefore(PricingRules.Fields, "price", []resource.Field{
		{Name: "serviceType", Column: "service_type", Label: "Service Type", Kind: resource.Enum, Enum: ServiceTypes, Default: "golf", CreateOnly: true, Filter: true},
		{Name: "itemRef", Column: "item_ref", Label: "Item (resource type, resource, product, package …; empty = any)", Kind: resource.String, Max: 80, Filter: true},
		{Name: "packageRateId", Column: "package_rate_id", Label: "Package Rate", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "commercial.package_rates", SameProperty: true, Label: "package rate"}},
		{Name: "unit", Column: "unit", Label: "Unit", Kind: resource.Enum, Enum: Units, Default: "pax"},
		{Name: "unitMinutes", Column: "unit_minutes", Label: "Unit Length (minutes)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "packageQuantity", Column: "package_quantity", Label: "Package Quantity (4x/8x/5x)", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "minQuantity", Column: "min_quantity", Label: "Minimum Quantity (pax / nights)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "minPolicy", Column: "min_policy", Label: "Below Minimum", Kind: resource.Enum, Enum: []string{"reject", "charge_minimum"}, Default: "reject"},
		{Name: "overtimePrice", Column: "overtime_price", Label: "Overtime Price per Hour", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Nett / ++ (empty = rate plan)", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}},
		{Name: "taxCodes", Column: "tax_codes", Label: "Tax & Service Codes (empty = all)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.Enum, Enum: billing.RevenueComponents}})
	p1Rule := PricingRules.Hooks.BeforeWrite
	PricingRules.Hooks.BeforeWrite = func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
		return ruleP2BeforeWrite(ctx, tx, v, before, p1Rule)
	}
	p1Plan := RatePlans.Hooks.BeforeWrite
	RatePlans.Hooks.BeforeWrite = func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
		if before == nil && str(v["effectiveFrom"]) == "" {
			v["effectiveFrom"] = clock.Now().Format("2006-01-02")
		}
		if p1Plan != nil {
			return p1Plan(ctx, tx, v, before)
		}
		return nil
	}
}

func extendEnum(d *resource.Def, field string, values ...string) {
	for i := range d.Fields {
		if d.Fields[i].Name == field {
			for _, v := range values {
				if !slices.Contains(d.Fields[i].Enum, v) {
					d.Fields[i].Enum = append(d.Fields[i].Enum, v)
				}
			}
		}
	}
}

func relax(d *resource.Def, field string) {
	for i := range d.Fields {
		if d.Fields[i].Name == field {
			d.Fields[i].Required = false
		}
	}
}

func setDefault(d *resource.Def, field string, v any) {
	for i := range d.Fields {
		if d.Fields[i].Name == field && d.Fields[i].Default == nil {
			d.Fields[i].Default = v
		}
	}
}

func insertBefore(fields []resource.Field, name string, extra []resource.Field) []resource.Field {
	for i, f := range fields {
		if f.Name == name {
			return append(append(append([]resource.Field{}, fields[:i]...), extra...), fields[i:]...)
		}
	}
	return append(fields, extra...)
}

func insertBeforeStatus(fields, extra []resource.Field) []resource.Field {
	return insertBefore(fields, "status", extra)
}

// ruleP2BeforeWrite keeps P1's validation for golf rules; rules of other
// lines post as charge type "other", need no rate plan and conflict only
// with rules of the same service, item, package, rate plan and unit.
func ruleP2BeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any, p1 func(context.Context, pgx.Tx, map[string]any, map[string]any) error) error {
	m := map[string]any{}
	for k, x := range before {
		m[k] = x
	}
	for k, x := range v {
		m[k] = x
	}
	st := str(m["serviceType"])
	if st == "" || st == "golf" {
		if str(m["ratePlanId"]) == "" {
			return errs.Validation("rate_plan_required", "golf rules belong to a rate plan", errs.Field("ratePlanId", "required", "choose the rate plan"))
		}
		if p1 != nil {
			return p1(ctx, tx, v, before)
		}
		return nil
	}
	if before == nil {
		v["chargeType"], m["chargeType"] = "other", "other"
	}
	if str(m["effectiveFrom"]) == "" {
		v["effectiveFrom"], m["effectiveFrom"] = clock.Now().Format("2006-01-02"), clock.Now().Format("2006-01-02")
	}
	if str(m["status"]) == "inactive" {
		return nil
	}
	pid, _ := reqctx.Property(ctx)
	nullable := func(k string) any {
		if s := str(m[k]); s != "" {
			return s
		}
		return nil
	}
	var conflict string
	err := tx.QueryRow(ctx, `SELECT code || ' v' || version FROM commercial.pricing_rules
		WHERE property_id = $1 AND status = 'active' AND code <> $2 AND service_type = $3 AND item_ref IS NOT DISTINCT FROM $4
		  AND package_rate_id IS NOT DISTINCT FROM $5::uuid AND rate_plan_id IS NOT DISTINCT FROM $6::uuid AND unit = $7
		  AND segment IS NOT DISTINCT FROM $8 AND day_type_id IS NOT DISTINCT FROM $9::uuid AND time_band_id IS NOT DISTINCT FROM $10::uuid
		  AND channel IS NOT DISTINCT FROM $11 AND priority = $12
		  AND daterange(effective_from, coalesce(effective_to, 'infinity'::date), '[]') && daterange($13::date, coalesce($14::date, 'infinity'::date), '[]')
		LIMIT 1`, pid, m["code"], st, nullable("itemRef"), nullable("packageRateId"), nullable("ratePlanId"), nonEmpty(str(m["unit"]), "pax"),
		nullable("segment"), nullable("dayTypeId"), nullable("timeBandId"), nullable("channel"), m["priority"], str(m["effectiveFrom"]),
		nullable("effectiveTo")).Scan(&conflict)
	if err == nil {
		return errs.Conflict("rule_conflict", "rule "+conflict+" prices the same service, item, segment, day type, time band and channel with the same priority in an overlapping period")
	}
	if !dbtx.IsNoRows(err) {
		return err
	}
	return nil
}

// LineResolveRequest prices a service of a non-golf line (PRD P2 EP-02).
type LineResolveRequest struct {
	ServiceType string     `json:"serviceType" enum:"sport_court,facility_entry,class_session,class_registration,class_package,bungalow,vip_suite,meeting_room,meeting_package,equipment,driving_range,voucher_sale,membership,pos,locker,other"`
	ItemRef     string     `json:"itemRef,omitempty"`
	Segment     string     `json:"segment,omitempty" doc:"Customer segment (default any)"`
	Start       time.Time  `json:"start" doc:"Service start"`
	End         *time.Time `json:"end,omitempty" doc:"Service end (time-based units)"`
	Quantity    int        `json:"quantity,omitempty"`
	RatePlan    string     `json:"ratePlan,omitempty"`
	Package     string     `json:"package,omitempty"`
	Channel     string     `json:"channel,omitempty"`
	Persist     bool       `json:"persist,omitempty" doc:"Store an immutable pricing snapshot"`
}

// RegisterP2 adds the P2 pricing resources and routes (wired by
// internal/app).
func (m *Module) RegisterP2(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{DayTypeSets, PackageRates} {
		eng.Register(reg, d)
	}
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/commercial/pricing:resolve-line", Module: "commercial", Tag: "Pricing",
		Summary: "Resolve the price of a non-golf service (units, overtime, tax breakdown; optional snapshot)", Permission: "commercial.pricing.view",
		Scope: route.ScopeProperty, Request: LineResolveRequest{}, Response: LinePrice{}, Status: http.StatusOK,
		NoAudit: "read-only calculation; a persisted snapshot is audited", Handler: m.resolveLine})
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/public/rates/{line}", Module: "commercial", Tag: "Public",
		Summary: "Structured public rate table per line (sportclub, stay, meeting, range, vouchers)", Auth: route.AuthPublic,
		Response: PublicRate{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}}, Handler: m.publicRates})
}

// resolveLine prices a service of another line; with persist the snapshot
// is stored and audited.
func (m *Module) resolveLine(w http.ResponseWriter, r *http.Request) {
	var req LineResolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.ServiceType == "" || req.ServiceType == "golf" {
		httpx.WriteError(w, r, errs.Validation("invalid_service", "golf prices resolve through pricing:resolve", errs.Field("serviceType", "invalid", "a non-golf service")))
		return
	}
	ctx := r.Context()
	pid, _ := reqctx.Property(ctx)
	pr := PriceRequest{ServiceType: req.ServiceType, ItemRef: req.ItemRef, Segment: req.Segment, Start: req.Start, End: req.End, Quantity: req.Quantity,
		RatePlan: req.RatePlan, Package: req.Package, Channel: req.Channel, Persist: req.Persist}
	if pr.Start.IsZero() {
		pr.Start = clock.Now()
	}
	var out LinePrice
	fn := func(tx pgx.Tx) error {
		var err error
		if !req.Persist {
			out, err = (Pricer{}).Resolve(ctx, tx, pid, pr)
			return err
		}
		if out, err = (Pricer{}).Price(ctx, tx, pid, pr); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: audit.ActionCreate, EntityType: "commercial.pricing_snapshot",
			EntityID: out.SnapshotID.String(), EntityLabel: out.RuleCode, PropertyID: &pid, After: out})
	}
	var err error
	if req.Persist {
		err = m.DB.WithTx(ctx, fn)
	} else {
		err = m.DB.WithReadTx(ctx, fn)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// publicRates is the structured rate table of a line for the website
// (FR-WEB-P2-05); golf rates are P1's /api/v1/public/golf/rates.
func (m *Module) publicRates(w http.ResponseWriter, r *http.Request) {
	types, ok := LineServiceTypes[chi.URLParam(r, "line")]
	if !ok {
		httpx.WriteError(w, r, errs.NotFound("rate table"))
		return
	}
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := dbtx.WithScope(r.Context(), dbtx.Scope{PropertyIDs: []uuid.UUID{pid}})
	var out []PublicRate
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		out, err = PublicRates(ctx, tx, pid, types)
		return err
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, httpx.Page[PublicRate]{Items: out})
}
