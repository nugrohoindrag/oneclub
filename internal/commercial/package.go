package commercial

// PRD P3 EP-11 Package Management: cross-line packages (Golf Day, Golf &
// Lunch, Stay & Golf, Corporate, Wedding, Family) with components —
// Reservation Engine resources, golf tee times, vouchers, F&B products with
// their recipe (BOM link), services — pricing per package / pax / night,
// nett or ++, add-ons, availability (sale period, valid days, daily quota,
// segments, channels) and a revenue allocation method (standalone selling
// price, fixed amounts or percentages). Publishing freezes a version that
// bookings keep (FR-PKG-09).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Package enums.
var (
	PackageTypes       = []string{"golf_day", "golf_lunch", "stay_golf", "corporate", "wedding", "family", "other"}
	PackagePricing     = []string{"fixed", "per_pax", "per_night", "per_pax_per_night"}
	PackageChannels    = []string{"back_office", "website", "member_app", "ops", "quotation"}
	ComponentTypes     = []string{"reservation", "tee_time", "voucher", "fnb", "service", "banquet", "other"}
	AllocationMethods  = []string{"standalone", "fixed", "percent"}
	packageStatuses    = []string{"draft", "active", "inactive"}
	packageCodeLabelRe = code40Re
)

// Packages master (FR-PKG-01..03).
var Packages = &resource.Def{
	Key: "commercial.package", Module: "commercial", Perm: "commercial.package", Path: "/api/v1/commercial/packages", Table: "commercial.packages",
	Name: "Package", Plural: "Packages", Tag: "Packages", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	SchemaName: "CommercialPackage",
	Fields: []resource.Field{
		{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 40, Upper: true, CreateOnly: true,
			Pattern: packageCodeLabelRe, PatternMsg: "1–40 characters: A–Z, 0–9, - or _", Search: true, Filter: true},
		resource.Name(),
		{Name: "packageType", Column: "package_type", Label: "Package Type", Kind: resource.Enum, Enum: PackageTypes, Default: "other", Filter: true},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 4000},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Package Pricing", Kind: resource.Enum, Enum: PackagePricing, Default: "fixed"},
		{Name: "price", Column: "price", Label: "Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "minPax", Column: "min_pax", Label: "Minimum Pax", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "maxPax", Column: "max_pax", Label: "Maximum Pax", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "nights", Column: "nights", Label: "Nights", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "startTime", Column: "start_time", Label: "Start Time", Kind: resource.Time, Default: "08:00"},
		{Name: "taxMode", Column: "tax_mode", Label: "Nett / ++", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "nett"},
		{Name: "taxCodes", Column: "tax_codes", Label: "Tax & Service Codes (empty = all)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "sellFrom", Column: "sell_from", Label: "Sell From", Kind: resource.Date},
		{Name: "sellTo", Column: "sell_to", Label: "Sell To", Kind: resource.Date},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From (service dates)", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To (service dates)", Kind: resource.Date},
		{Name: "validDays", Column: "valid_days", Label: "Start Days (empty = every day)", Kind: resource.IntList, Enum: []string{"1", "2", "3", "4", "5", "6", "7"}, Default: []int64{}},
		{Name: "dailyQuota", Column: "daily_quota", Label: "Daily Quota", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "segments", Column: "segments", Label: "Segments allowed (empty = everyone)", Kind: resource.StringList, Default: []string{}},
		{Name: "channels", Column: "channels", Label: "Channels (empty = all)", Kind: resource.StringList, Enum: PackageChannels, Default: []string{}},
		{Name: "allocationMethod", Column: "allocation_method", Label: "Revenue Allocation", Kind: resource.Enum, Enum: AllocationMethods, Default: "standalone"},
		{Name: "cancellationHours", Column: "cancellation_hours", Label: "Free Cancellation (hours before start)", Kind: resource.Int, Default: int64(48), Min: resource.Min(0)},
		{Name: "cancellationFeePercent", Column: "cancellation_fee_percent", Label: "Late Cancellation Fee (%)", Kind: resource.Decimal, Default: "0",
			Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "componentValidityDays", Column: "component_validity_days", Label: "Unused Components Expire After (days)", Kind: resource.Int, Default: int64(0),
			Min: resource.Min(0)},
		{Name: "public", Column: "public", Label: "Show on Website & Member App", Kind: resource.Bool, Default: true},
		{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: packageStatuses, Default: "draft", Filter: true},
		{Name: "version", Column: "version", Label: "Published Version", Kind: resource.Int, ReadOnly: true},
		{Name: "publishedAt", Column: "published_at", Label: "Published At", Kind: resource.Timestamp, ReadOnly: true},
	},
}

// PackageComponents of a package (FR-PKG-01, FR-PKG-05, FR-PKG-07).
var PackageComponents = &resource.Def{
	Key: "commercial.package_component", Module: "commercial", Perm: "commercial.package", Path: "/api/v1/commercial/package-components",
	Table: "commercial.package_components", Name: "Package Component", Plural: "Package Components", Tag: "Packages", PropertyScoped: true,
	OrderBy: "package_id, seq, id",
	Fields: []resource.Field{
		{Name: "packageId", Column: "package_id", Label: "Package", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "commercial.packages", SameProperty: true, Label: "package"}},
		{Name: "seq", Column: "seq", Label: "Sequence", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "componentType", Column: "component_type", Label: "Component Type", Kind: resource.Enum, Enum: ComponentTypes, Required: true, Filter: true},
		resource.Name(),
		{Name: "resourceTypeCode", Column: "resource_type_code", Label: "Resource Type (Reservation Engine)", Kind: resource.String, Max: 40},
		{Name: "resourceId", Column: "resource_id", Label: "Specific Resource", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "reservation.resources", SameProperty: true, Label: "resource"}},
		{Name: "refCode", Column: "ref_code", Label: "Reference (course / voucher type / product code)", Kind: resource.String, Max: 40, Upper: true},
		{Name: "productId", Column: "product_id", Label: "Product", Kind: resource.UUID, Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "recipeId", Column: "recipe_id", Label: "Recipe (BOM)", Kind: resource.UUID, Ref: &resource.Ref{Table: "inventory.recipes", SameProperty: true, Label: "recipe"}},
		{Name: "quantity", Column: "quantity", Label: "Quantity", Kind: resource.Decimal, Default: "1", Min: resource.Min(0)},
		{Name: "perPax", Column: "per_pax", Label: "Per Pax", Kind: resource.Bool, Default: false},
		{Name: "perNight", Column: "per_night", Label: "Per Night", Kind: resource.Bool, Default: false},
		{Name: "dayOffset", Column: "day_offset", Label: "Day (0 = first day)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "startTime", Column: "start_time", Label: "Start Time (empty = package start)", Kind: resource.Time},
		{Name: "durationMinutes", Column: "duration_minutes", Label: "Duration (minutes) / Tee Time Search Window", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "nights", Column: "nights", Label: "Nights (empty = package nights)", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "standalonePrice", Column: "standalone_price", Label: "Standalone Selling Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "allocationValue", Column: "allocation_value", Label: "Allocation (fixed amount or percent)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.Enum, Enum: billing.RevenueComponents, Default: "package"},
		{Name: "businessLine", Column: "business_line", Label: "Business Line", Kind: resource.Enum, Enum: billing.BusinessLines, Default: "package"},
		{Name: "liability", Column: "liability", Label: "Held for a Third Party (liability)", Kind: resource.Bool, Default: false},
		{Name: "optional", Column: "optional", Label: "Add-on (optional)", Kind: resource.Bool, Default: false},
		{Name: "addonPrice", Column: "addon_price", Label: "Add-on Price", Kind: resource.Decimal, Min: resource.Min(0)},
	},
}

func init() {
	Packages.Hooks = resource.Hooks{BeforeWrite: packageBeforeWrite}
	PackageComponents.Hooks = resource.Hooks{BeforeWrite: componentBeforeWrite}
}

func packageBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, before map[string]any) error {
	if st, ok := v["status"]; ok && str(st) == "active" && (before == nil || str(before["status"]) != "active") {
		return errs.Validation("publish_required", "a package becomes Active when it is published", errs.Field("status", "invalid", "use Publish"))
	}
	m := merged(v, before)
	if mx, ok := intOf(m["maxPax"]); ok {
		if mn, _ := intOf(m["minPax"]); mx < mn {
			return errs.Validation("invalid_pax", "maximum pax below the minimum", errs.Field("maxPax", "invalid", "≥ minimum pax"))
		}
	}
	for _, pr := range [][2]string{{"sellFrom", "sellTo"}, {"validFrom", "validTo"}} {
		if f, t := str(m[pr[0]]), str(m[pr[1]]); f != "" && t != "" && t < f {
			return errs.Validation("invalid_period", "the end is before the start", errs.Field(pr[1], "invalid", "on or after "+pr[0]))
		}
	}
	return nil
}

func componentBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	m := merged(v, before)
	q, _ := decOf(m["quantity"])
	if !q.IsPositive() {
		return errs.Validation("invalid_quantity", "quantity must be positive", errs.Field("quantity", "invalid", "> 0"))
	}
	switch str(m["componentType"]) {
	case "reservation":
		if str(m["resourceTypeCode"]) == "" && str(m["resourceId"]) == "" {
			return errs.Validation("resource_required", "a reservation component needs a resource type or a resource",
				errs.Field("resourceTypeCode", "required", "resource type code or a specific resource"))
		}
	case "voucher":
		code := str(m["refCode"])
		if code == "" {
			return errs.Validation("voucher_type_required", "a voucher component needs the voucher type code", errs.Field("refCode", "required", "voucher type code"))
		}
		var ok bool
		pkg := str(m["packageId"])
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.voucher_types t JOIN commercial.packages p ON p.property_id = t.property_id
			WHERE p.id = $1::uuid AND t.code = $2)`, pkg, code).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errs.Validation("voucher_type_not_found", "voucher type not found", errs.Field("refCode", "not_found", "voucher type code"))
		}
	}
	if b, ok := m["optional"].(bool); ok && b {
		if _, ok := decOf(m["addonPrice"]); !ok {
			return errs.Validation("addon_price_required", "an add-on needs its price", errs.Field("addonPrice", "required", "add-on price"))
		}
	}
	return nil
}

// ── published versions ────────────────────────────────────────────────────

// ComponentSpec is a component in a published version.
type ComponentSpec struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	Seq              int        `json:"seq" db:"seq"`
	ComponentType    string     `json:"componentType" db:"component_type"`
	Name             string     `json:"name" db:"name"`
	ResourceTypeCode *string    `json:"resourceTypeCode" db:"resource_type_code"`
	ResourceID       *uuid.UUID `json:"resourceId" db:"resource_id"`
	RefCode          *string    `json:"refCode" db:"ref_code"`
	ProductID        *uuid.UUID `json:"productId" db:"product_id"`
	RecipeID         *uuid.UUID `json:"recipeId" db:"recipe_id"`
	Quantity         string     `json:"quantity" db:"quantity"`
	PerPax           bool       `json:"perPax" db:"per_pax"`
	PerNight         bool       `json:"perNight" db:"per_night"`
	DayOffset        int        `json:"dayOffset" db:"day_offset"`
	StartTime        *string    `json:"startTime" db:"start_time"`
	DurationMinutes  *int       `json:"durationMinutes" db:"duration_minutes"`
	Nights           *int       `json:"nights" db:"nights"`
	StandalonePrice  string     `json:"standalonePrice" db:"standalone_price"`
	AllocationValue  *string    `json:"allocationValue" db:"allocation_value"`
	RevenueComponent string     `json:"revenueComponent" db:"revenue_component"`
	BusinessLine     string     `json:"businessLine" db:"business_line"`
	Liability        bool       `json:"liability" db:"liability"`
	Optional         bool       `json:"optional" db:"optional"`
	AddonPrice       *string    `json:"addonPrice" db:"addon_price"`
}

// PackageSpec is a published package version.
type PackageSpec struct {
	ID                     uuid.UUID       `json:"id" db:"id"`
	Code                   string          `json:"code" db:"code"`
	Name                   string          `json:"name" db:"name"`
	PackageType            string          `json:"packageType" db:"package_type"`
	Description            *string         `json:"description" db:"description"`
	PricingMode            string          `json:"pricingMode" db:"pricing_mode"`
	Price                  string          `json:"price" db:"price"`
	MinPax                 int             `json:"minPax" db:"min_pax"`
	MaxPax                 *int            `json:"maxPax" db:"max_pax"`
	Nights                 int             `json:"nights" db:"nights"`
	StartTime              string          `json:"startTime" db:"start_time"`
	TaxMode                string          `json:"taxMode" db:"tax_mode"`
	TaxCodes               []string        `json:"taxCodes" db:"tax_codes"`
	SellFrom               *string         `json:"sellFrom" db:"sell_from"`
	SellTo                 *string         `json:"sellTo" db:"sell_to"`
	ValidFrom              *string         `json:"validFrom" db:"valid_from"`
	ValidTo                *string         `json:"validTo" db:"valid_to"`
	ValidDays              []int32         `json:"validDays" db:"valid_days"`
	DailyQuota             *int            `json:"dailyQuota" db:"daily_quota"`
	Segments               []string        `json:"segments" db:"segments"`
	Channels               []string        `json:"channels" db:"channels"`
	AllocationMethod       string          `json:"allocationMethod" db:"allocation_method"`
	CancellationHours      int             `json:"cancellationHours" db:"cancellation_hours"`
	CancellationFeePercent string          `json:"cancellationFeePercent" db:"cancellation_fee_percent"`
	ComponentValidityDays  int             `json:"componentValidityDays" db:"component_validity_days"`
	Public                 bool            `json:"public" db:"public"`
	Status                 string          `json:"status" db:"status"`
	Version                int             `json:"version" db:"version"`
	Components             []ComponentSpec `json:"components" db:"-"`
}

const packageSpecSelect = `SELECT id, code, name, package_type, description, pricing_mode, trim_scale(price)::text AS price, min_pax, max_pax, nights,
	to_char(start_time, 'HH24:MI') AS start_time, tax_mode, tax_codes, to_char(sell_from, 'YYYY-MM-DD') AS sell_from, to_char(sell_to, 'YYYY-MM-DD') AS sell_to,
	to_char(valid_from, 'YYYY-MM-DD') AS valid_from, to_char(valid_to, 'YYYY-MM-DD') AS valid_to, valid_days, daily_quota, segments, channels,
	allocation_method, cancellation_hours, trim_scale(cancellation_fee_percent)::text AS cancellation_fee_percent, component_validity_days, public, status,
	version FROM commercial.packages`

const componentSpecSelect = `SELECT id, seq, component_type, name, resource_type_code, resource_id, ref_code, product_id, recipe_id,
	trim_scale(quantity)::text AS quantity, per_pax, per_night, day_offset, to_char(start_time, 'HH24:MI') AS start_time, duration_minutes, nights,
	trim_scale(standalone_price)::text AS standalone_price, trim_scale(allocation_value)::text AS allocation_value, revenue_component, business_line,
	liability, optional, trim_scale(addon_price)::text AS addon_price FROM commercial.package_components`

// draftSpec loads the current (editable) definition of a package.
func draftSpec(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (PackageSpec, error) {
	rows, err := q.Query(ctx, packageSpecSelect+` WHERE id = $1 AND archived_at IS NULL`, pid)
	p, err := handle.One[PackageSpec](rows, err, "package")
	if err != nil {
		return p, err
	}
	p.Components, err = handle.List[ComponentSpec](q.Query(ctx, componentSpecSelect+` WHERE package_id = $1 ORDER BY seq, created_at`, pid))
	return p, err
}

// PublishedSpec returns the latest published version of a package.
func PublishedSpec(ctx context.Context, q dbtx.Querier, pid uuid.UUID) (PackageSpec, error) {
	var raw []byte
	var status string
	err := q.QueryRow(ctx, `SELECT v.snapshot, p.status FROM commercial.package_versions v JOIN commercial.packages p ON p.id = v.package_id
		WHERE v.package_id = $1 AND p.archived_at IS NULL ORDER BY v.version DESC LIMIT 1`, pid).Scan(&raw, &status)
	if dbtx.IsNoRows(err) {
		return PackageSpec{}, errs.Conflict("package_not_published", "the package has not been published")
	}
	if err != nil {
		return PackageSpec{}, err
	}
	var s PackageSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	s.Status = status
	return s, nil
}

// VersionSpec returns a specific published version (bookings keep theirs).
func VersionSpec(ctx context.Context, q dbtx.Querier, pid uuid.UUID, version int) (PackageSpec, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT snapshot FROM commercial.package_versions WHERE package_id = $1 AND version = $2`, pid, version).Scan(&raw); err != nil {
		if dbtx.IsNoRows(err) {
			return PackageSpec{}, errs.NotFound("package version")
		}
		return PackageSpec{}, err
	}
	var s PackageSpec
	return s, json.Unmarshal(raw, &s)
}

// validateSpec checks a package before it is published.
func validateSpec(p PackageSpec) error {
	price := dec(p.Price)
	base := 0
	fixed, pct := decimal.Zero, decimal.Zero
	nilValues := 0
	for _, c := range p.Components {
		if c.Optional {
			continue
		}
		base++
		if c.AllocationValue == nil {
			nilValues++
		} else if p.AllocationMethod == "fixed" {
			fixed = fixed.Add(dec(*c.AllocationValue))
		} else {
			pct = pct.Add(dec(*c.AllocationValue))
		}
	}
	if base == 0 {
		return errs.Validation("components_required", "add at least one included component before publishing")
	}
	switch p.AllocationMethod {
	case "fixed":
		if fixed.GreaterThan(price) || (nilValues == 0 && !fixed.Equal(price)) {
			return errs.Validation("allocation_mismatch", fmt.Sprintf("fixed allocations add up to %s; the package price is %s", fixed.String(), price.String()))
		}
	case "percent":
		if pct.GreaterThan(hundred) || (nilValues == 0 && !pct.Equal(hundred)) {
			return errs.Validation("allocation_mismatch", fmt.Sprintf("percent allocations add up to %s%%, not 100%%", pct.String()))
		}
	}
	for _, c := range p.Components {
		if (c.ComponentType == "reservation") && c.ResourceTypeCode == nil && c.ResourceID == nil {
			return errs.Validation("resource_required", "component "+c.Name+" needs a resource type or a resource")
		}
		if c.ComponentType == "voucher" && c.RefCode == nil {
			return errs.Validation("voucher_type_required", "component "+c.Name+" needs the voucher type")
		}
	}
	return nil
}

// Publish freezes the current definition as the next version and makes the
// package Active (FR-PKG-09).
func (m *P3Module) Publish(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (PackageSpec, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM commercial.packages WHERE id = $1 FOR UPDATE`, pid); err != nil {
		return PackageSpec{}, err
	}
	p, err := draftSpec(ctx, tx, pid)
	if err != nil {
		return p, err
	}
	if err := validateSpec(p); err != nil {
		return p, err
	}
	before := p.Status
	p.Version++
	p.Status = "active"
	raw, _ := json.Marshal(p)
	var property uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE commercial.packages SET version = $2, status = 'active', published_at = now(), published_by = $3, updated_by = $3
		WHERE id = $1 RETURNING property_id`, pid, p.Version, actorPtr(ctx)).Scan(&property); err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commercial.package_versions (package_id, property_id, version, snapshot, published_by) VALUES ($1,$2,$3,$4,$5)`,
		pid, property, p.Version, raw, actorPtr(ctx)); err != nil {
		return p, err
	}
	return p, audit.Record(ctx, tx, audit.Entry{Module: "commercial", Action: "publish", EntityType: "commercial.package", EntityID: pid.String(),
		EntityLabel: p.Code + " · " + p.Name, PropertyID: &property, Before: map[string]any{"status": before, "version": p.Version - 1},
		After: map[string]any{"status": "active", "version": p.Version, "components": len(p.Components)}})
}

// PackageVersion is one published version (Package History).
type PackageVersion struct {
	Version     int             `json:"version" db:"version"`
	PublishedAt time.Time       `json:"publishedAt" db:"published_at"`
	PublishedBy *string         `json:"publishedBy" db:"published_by"`
	Snapshot    json.RawMessage `json:"snapshot" db:"snapshot"`
}

// Versions lists the published versions of a package.
func Versions(ctx context.Context, q dbtx.Querier, pid uuid.UUID) ([]PackageVersion, error) {
	return handle.List[PackageVersion](q.Query(ctx, `SELECT v.version, v.published_at, u.full_name AS published_by, v.snapshot
		FROM commercial.package_versions v LEFT JOIN platform.users u ON u.id = v.published_by WHERE v.package_id = $1 ORDER BY v.version DESC`, pid))
}

// packageByCode finds a package id by code.
func packageByCode(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) (uuid.UUID, error) {
	var pid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM commercial.packages WHERE property_id = $1 AND code = upper($2) AND archived_at IS NULL`, property, code).Scan(&pid)
	if dbtx.IsNoRows(err) {
		return pid, errs.NotFound("package")
	}
	return pid, err
}

// ── sale rules (FR-PKG-03) ────────────────────────────────────────────────

// saleProblem returns why a package cannot be sold for a start date ("" = ok).
func saleProblem(p PackageSpec, today, start time.Time, pax int, channel, segment string) string {
	if p.Status != "active" {
		return "the package is not on sale"
	}
	d, t := start.Format("2006-01-02"), today.Format("2006-01-02")
	if p.SellFrom != nil && t < *p.SellFrom {
		return "the package is on sale from " + *p.SellFrom
	}
	if p.SellTo != nil && t > *p.SellTo {
		return "the package was on sale until " + *p.SellTo
	}
	if d < t {
		return "the start date is in the past"
	}
	if p.ValidFrom != nil && d < *p.ValidFrom {
		return "the package is valid from " + *p.ValidFrom
	}
	if p.ValidTo != nil && d > *p.ValidTo {
		return "the package is valid until " + *p.ValidTo
	}
	if len(p.ValidDays) > 0 && !slices.Contains(p.ValidDays, int32(isoWeekday(start))) {
		return "the package does not start on " + start.Weekday().String()
	}
	if pax < p.MinPax {
		return fmt.Sprintf("minimum %d pax", p.MinPax)
	}
	if p.MaxPax != nil && pax > *p.MaxPax {
		return fmt.Sprintf("maximum %d pax", *p.MaxPax)
	}
	if channel != "" && len(p.Channels) > 0 && !slices.Contains(p.Channels, channel) {
		return "the package is not sold in this channel"
	}
	if len(p.Segments) > 0 && !slices.Contains(p.Segments, segment) {
		return "the package is not available for this customer segment"
	}
	return ""
}

// quotaLeft returns the bookings left on a start date (nil = no quota).
func quotaLeft(ctx context.Context, q dbtx.Querier, p PackageSpec, start time.Time, exclude uuid.UUID) (*int, error) {
	if p.DailyQuota == nil {
		return nil, nil
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM commercial.package_bookings WHERE package_id = $1 AND start_date = $2::date
		AND status IN ('pending', 'confirmed', 'completed') AND id <> $3`, p.ID, start.Format("2006-01-02"), exclude).Scan(&n); err != nil {
		return nil, err
	}
	left := *p.DailyQuota - n
	return &left, nil
}
