package stay

// Accommodation & Bungalow Management (docs/oneclub-accommodation-bungalow-
// management-requirements.md): the bungalows run like a resort. This file
// holds the master data of the accommodation — add-ons and stay packages
// (§13), rate plans with their prices, seasons and season prices (§11, §12),
// promotions (§31), corporate terms (§29) and preventive maintenance
// schedules (§22) — and the permissions of the accommodation roles (§34).
// Operations live in rates.go, rooms.go, housekeeping.go, maintenance.go,
// requests.go, waitlist.go, frontoffice.go, guests.go and dashboard.go.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// Housekeeping statuses of a bungalow (§10 operational status; Maintenance,
// Out of Order and Blocked come from the room blocks).
var HKStatuses = []string{"dirty", "cleaning", "cleaned", "inspected", "ready"}

// Booking sources (§28).
var BookingSources = []string{"website", "member_app", "guest_app", "front_desk", "phone", "walk_in", "corporate"}

var maintenanceCategories = []string{"ac", "plumbing", "electrical", "water_heater", "furniture", "appliance", "structure", "pest", "other"}

const tagAccommodation = "Accommodation"

var Addons = &resource.Def{
	Key: "stay.addon", SchemaName: "AccommodationAddon", Module: "stay", Perm: "stay.addon", Path: "/api/v1/stay/addons", Table: "stay.addons",
	Name: "Add-on", Plural: "Add-ons", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "category, name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Required: true, Filter: true, Default: "other",
			Enum: []string{"breakfast", "extra_bed", "extra_pillow", "extra_towel", "bbq", "dinner", "lunch", "transportation", "laundry", "other"}},
		{Name: "price", Column: "price", Label: "Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "unit", Column: "unit", Label: "Unit", Kind: resource.Enum, Default: "per_item",
			Enum: []string{"per_stay", "per_night", "per_item", "per_person", "per_person_night"}},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Price includes tax & service", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "plus_plus"},
		{Name: "taxable", Column: "taxable", Label: "Tax", Kind: resource.Bool, Default: true},
		{Name: "serviceCharge", Column: "service_charge", Label: "Service Charge", Kind: resource.Bool, Default: true},
		{Name: "availability", Column: "availability", Label: "Availability", Kind: resource.Enum, Enum: []string{"booking", "in_stay", "both"}, Default: "both", Filter: true},
		{Name: "maxQuantity", Column: "max_quantity", Label: "Maximum Quantity", Kind: resource.Int, Min: resource.Min(1)},
		resource.Status("active", "inactive")},
}

var StayPackages = &resource.Def{
	Key: "stay.package", SchemaName: "AccommodationPackage", Module: "stay", Perm: "stay.package", Path: "/api/v1/stay/packages", Table: "stay.packages",
	Name: "Stay Package", Plural: "Stay Packages", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "priceMode", Column: "price_mode", Label: "Pricing", Kind: resource.Enum, Enum: []string{"fixed", "room_plus"}, Default: "fixed"},
		{Name: "price", Column: "price", Label: "Package Price per Night (fixed)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "inclusionDiscount", Column: "inclusion_discount", Label: "Discount on Inclusions % (room + inclusions)", Kind: resource.Decimal, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "inclusions", Column: "inclusions", Label: "Inclusions [{addonId, quantity}]", Kind: resource.JSONList, Default: "[]"},
		{Name: "bungalowTypeIds", Column: "bungalow_type_ids", Label: "Room Types (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "minNights", Column: "min_nights", Label: "Minimum Nights", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "maxNights", Column: "max_nights", Label: "Maximum Nights", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "includesBreakfast", Column: "includes_breakfast", Label: "Includes Breakfast", Kind: resource.Bool, Default: false},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "photo", Column: "photo", Label: "Photo", Kind: resource.Image, Max: 500},
		resource.Status("active", "inactive")},
	Hooks: resource.Hooks{BeforeWrite: func(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
		mode := str(v["priceMode"])
		if mode == "" && before != nil {
			mode = str(before["priceMode"])
		}
		price, set := v["price"]
		if !set && before != nil {
			price = before["price"]
		}
		if (mode == "" || mode == "fixed") && (price == nil || str(price) == "") {
			return handle.Invalid("price", "required", "a fixed package needs its price per night")
		}
		return nil
	}},
}

var RatePlans = &resource.Def{
	Key: "stay.rate_plan", SchemaName: "AccommodationRatePlan", Module: "stay", Perm: "stay.rate_plan", Path: "/api/v1/stay/rate-plans", Table: "stay.rate_plans",
	Name: "Rate Plan", Plural: "Rate Plans", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "sort_order, name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "planType", Column: "plan_type", Label: "Rate Type", Kind: resource.Enum, Default: "standard", Filter: true,
			Enum: []string{"standard", "weekend", "peak_season", "holiday", "member", "corporate", "promotional", "other"}},
		{Name: "derivation", Column: "derivation", Label: "Price", Kind: resource.Enum, Enum: []string{"bar", "percent", "amount", "fixed"}, Default: "bar"},
		{Name: "adjustValue", Column: "adjust_value", Label: "Adjustment (% or amount from the BAR, negative = discount)", Kind: resource.Decimal, Default: "0"},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Price includes tax & service", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "nett"},
		{Name: "includesBreakfast", Column: "includes_breakfast", Label: "Includes Breakfast", Kind: resource.Bool, Default: false},
		{Name: "minNights", Column: "min_nights", Label: "Minimum Stay (nights)", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "maxNights", Column: "max_nights", Label: "Maximum Stay (nights)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "weekendOnly", Column: "weekend_only", Label: "Weekend Nights Only", Kind: resource.Bool, Default: false},
		{Name: "freeCancelHours", Column: "free_cancel_hours", Label: "Free Cancellation (hours before arrival)", Kind: resource.Int, Default: int64(24), Min: resource.Min(0)},
		{Name: "cancelFeePercent", Column: "cancel_fee_percent", Label: "Cancellation Fee %", Kind: resource.Decimal, Default: "50", Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "noShowFeePercent", Column: "no_show_fee_percent", Label: "No-show Charge % (of the first night)", Kind: resource.Decimal, Default: "100", Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "nonRefundable", Column: "non_refundable", Label: "Non-refundable", Kind: resource.Bool, Default: false},
		{Name: "depositPercent", Column: "deposit_percent", Label: "Deposit % (default: Stay Policies)", Kind: resource.Decimal, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "paymentPolicy", Column: "payment_policy", Label: "Payment Policy", Kind: resource.Enum, Enum: []string{"deposit", "full_prepayment", "pay_at_hotel"}, Default: "deposit"},
		{Name: "eligibility", Column: "eligibility", Label: "Eligible Guests", Kind: resource.Enum, Enum: []string{"everyone", "member", "corporate"}, Default: "everyone"},
		{Name: "bookingSources", Column: "booking_sources", Label: "Booking Sources (empty = all)", Kind: resource.StringList, Enum: BookingSources, Default: []string{}},
		{Name: "facilityAccess", Column: "facility_access", Label: "Facility Access for staying guests (facility types, * = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "validFrom", Column: "valid_from", Label: "Valid From", Kind: resource.Date},
		{Name: "validTo", Column: "valid_to", Label: "Valid To", Kind: resource.Date},
		{Name: "isDefault", Column: "is_default", Label: "Default Rate Plan", Kind: resource.Bool, Default: false},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
		resource.Status("active", "inactive")},
	Hooks: resource.Hooks{
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error { return oneDefault(ctx, tx, row) },
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error { return oneDefault(ctx, tx, after) },
	},
}

// oneDefault keeps a single default rate plan per property.
func oneDefault(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	if b, _ := row["isDefault"].(bool); !b {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE stay.rate_plans SET is_default = false WHERE property_id = (SELECT property_id FROM stay.rate_plans WHERE id = $1)
		AND id <> $1 AND is_default`, row["id"])
	return err
}

var RatePlanPrices = &resource.Def{
	Key: "stay.rate_plan_price", SchemaName: "AccommodationRatePlanPrice", Module: "stay", Perm: "stay.rate_plan", Path: "/api/v1/stay/rate-plan-prices", Table: "stay.rate_plan_prices",
	Name: "Rate Plan Price", Plural: "Rate Plan Prices", Tag: tagAccommodation, PropertyScoped: true, OrderBy: "rate_plan_id, bungalow_type_id",
	Fields: []resource.Field{
		{Name: "ratePlanId", Column: "rate_plan_id", Label: "Rate Plan", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.rate_plans", SameProperty: true, Label: "rate plan"}},
		{Name: "bungalowTypeId", Column: "bungalow_type_id", Label: "Room Type", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.bungalow_types", SameProperty: true, Label: "room type"}},
		{Name: "weekdayPrice", Column: "weekday_price", Label: "Weekday Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "weekendPrice", Column: "weekend_price", Label: "Weekend Price (default: weekday)", Kind: resource.Decimal, Min: resource.Min(0)}},
}

var Seasons = &resource.Def{
	Key: "stay.season", SchemaName: "AccommodationSeason", Module: "stay", Perm: "stay.rate_plan", Path: "/api/v1/stay/seasons", Table: "stay.seasons",
	Name: "Season", Plural: "Seasons", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "start_date, priority, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "seasonType", Column: "season_type", Label: "Season Type", Kind: resource.Enum, Enum: []string{"normal", "low", "peak", "holiday", "event"}, Default: "normal", Filter: true},
		{Name: "startDate", Column: "start_date", Label: "From", Kind: resource.Date, Required: true},
		{Name: "endDate", Column: "end_date", Label: "To", Kind: resource.Date, Required: true},
		{Name: "priority", Column: "priority", Label: "Priority (lower wins)", Kind: resource.Int, Default: int64(100)},
		{Name: "adjustPercent", Column: "adjust_percent", Label: "Base Rate Adjustment % (types without a season price)", Kind: resource.Decimal},
		resource.Status("active", "inactive")},
}

var SeasonPrices = &resource.Def{
	Key: "stay.season_price", SchemaName: "AccommodationSeasonPrice", Module: "stay", Perm: "stay.rate_plan", Path: "/api/v1/stay/season-prices", Table: "stay.season_prices",
	Name: "Season Price", Plural: "Season Prices", Tag: tagAccommodation, PropertyScoped: true, OrderBy: "season_id, bungalow_type_id",
	Fields: []resource.Field{
		{Name: "seasonId", Column: "season_id", Label: "Season", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.seasons", SameProperty: true, Label: "season"}},
		{Name: "bungalowTypeId", Column: "bungalow_type_id", Label: "Room Type", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "stay.bungalow_types", SameProperty: true, Label: "room type"}},
		{Name: "weekdayPrice", Column: "weekday_price", Label: "Weekday Price", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		{Name: "weekendPrice", Column: "weekend_price", Label: "Weekend Price (default: weekday)", Kind: resource.Decimal, Min: resource.Min(0)}},
}

var Promotions = &resource.Def{
	Key: "stay.promotion", SchemaName: "AccommodationPromotion", Module: "stay", Perm: "stay.promotion", Path: "/api/v1/stay/promotions", Table: "stay.promotions",
	Name: "Promotion", Plural: "Promotions", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "priority, name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "promoType", Column: "promo_type", Label: "Promotion Type", Kind: resource.Enum, Required: true, Filter: true,
			Enum: []string{"percent", "fixed", "stay_pay", "early_booking", "last_minute", "long_stay", "weekend", "holiday", "corporate"}},
		{Name: "discountPercent", Column: "discount_percent", Label: "Discount %", Kind: resource.Decimal, Min: resource.Min(0), MaxN: resource.Max(100)},
		{Name: "discountAmount", Column: "discount_amount", Label: "Discount Amount (per stay; weekend / holiday: per night)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "stayNights", Column: "stay_nights", Label: "Stay X Nights", Kind: resource.Int, Min: resource.Min(2)},
		{Name: "payNights", Column: "pay_nights", Label: "Pay Y Nights", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "minDaysBefore", Column: "min_days_before", Label: "Early Booking: at least N days before arrival", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "maxDaysBefore", Column: "max_days_before", Label: "Last Minute: at most N days before arrival", Kind: resource.Int, Min: resource.Min(0)},
		{Name: "minNights", Column: "min_nights", Label: "Minimum Nights", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "maxNights", Column: "max_nights", Label: "Maximum Nights", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "bungalowTypeIds", Column: "bungalow_type_ids", Label: "Room Types (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "ratePlanCodes", Column: "rate_plan_codes", Label: "Rate Plans (empty = all)", Kind: resource.StringList, Default: []string{}, Upper: true},
		{Name: "bookingSources", Column: "booking_sources", Label: "Booking Sources (empty = all)", Kind: resource.StringList, Enum: BookingSources, Default: []string{}},
		{Name: "corporateAccountIds", Column: "corporate_account_ids", Label: "Corporate Accounts (empty = every corporate)", Kind: resource.StringList, Default: []string{}},
		{Name: "bookFrom", Column: "book_from", Label: "Booking From", Kind: resource.Date},
		{Name: "bookTo", Column: "book_to", Label: "Booking To", Kind: resource.Date},
		{Name: "stayFrom", Column: "stay_from", Label: "Stay From", Kind: resource.Date},
		{Name: "stayTo", Column: "stay_to", Label: "Stay To", Kind: resource.Date},
		{Name: "promoCode", Column: "promo_code", Label: "Promo Code (empty = applied automatically)", Kind: resource.String, Max: 40, Upper: true},
		{Name: "usageLimit", Column: "usage_limit", Label: "Usage Limit", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "usedCount", Column: "used_count", Label: "Used", Kind: resource.Int, ReadOnly: true},
		{Name: "priority", Column: "priority", Label: "Priority", Kind: resource.Int, Default: int64(100)},
		resource.Status("active", "inactive")},
	Hooks: resource.Hooks{BeforeWrite: func(_ context.Context, _ pgx.Tx, v, before map[string]any) error {
		get := func(k string) any {
			if x, ok := v[k]; ok {
				return x
			}
			if before != nil {
				return before[k]
			}
			return nil
		}
		empty := func(k string) bool { return get(k) == nil || str(get(k)) == "" }
		switch str(get("promoType")) {
		case "stay_pay":
			if empty("stayNights") || empty("payNights") {
				return handle.Invalid("stayNights", "required", "Stay X Pay Y needs both night counts")
			}
		default:
			if empty("discountPercent") && empty("discountAmount") {
				return handle.Invalid("discountPercent", "required", "a discount percentage or amount is required")
			}
		}
		return nil
	}},
}

var CorporateTerms = &resource.Def{
	Key: "stay.corporate_term", SchemaName: "AccommodationCorporateTerms", Module: "stay", Perm: "stay.corporate_term", Path: "/api/v1/stay/corporate-terms", Table: "stay.corporate_terms",
	Name: "Corporate Accommodation Terms", Plural: "Corporate Accommodation Terms", Tag: tagAccommodation, PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "corporateAccountId", Column: "corporate_account_id", Label: "Corporate Account", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "crm.corporate_accounts", SameProperty: true, Label: "corporate account"}},
		{Name: "ratePlanCode", Column: "rate_plan_code", Label: "Corporate Rate Plan", Kind: resource.String, Max: 40, Upper: true},
		{Name: "billingArrangement", Column: "billing_arrangement", Label: "Billing Arrangement", Kind: resource.Enum, Default: "company_pays_room",
			Enum: []string{"guest_pays", "company_pays_room", "company_pays_all"}},
		{Name: "maxRoomsPerNight", Column: "max_rooms_per_night", Label: "Booking Limit (rooms per night)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "monthlyRoomNights", Column: "monthly_room_nights", Label: "Booking Limit (room nights per month)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "paymentTermsDays", Column: "payment_terms_days", Label: "Payment Terms (days)", Kind: resource.Int, Default: int64(30), Min: resource.Min(0)},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

var PreventiveSchedules = &resource.Def{
	Key: "stay.preventive_schedule", SchemaName: "AccommodationPreventiveSchedule", Module: "stay", Perm: "stay.preventive_schedule", Path: "/api/v1/stay/preventive-schedules", Table: "stay.preventive_schedules",
	Name: "Preventive Maintenance", Plural: "Preventive Maintenance", Tag: tagAccommodation, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "next_due, name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Enum: maintenanceCategories, Default: "other", Filter: true},
		{Name: "bungalowId", Column: "bungalow_id", Label: "Bungalow (empty = every bungalow)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "stay.bungalows", SameProperty: true, Label: "bungalow"}},
		{Name: "intervalDays", Column: "interval_days", Label: "Every (days)", Kind: resource.Int, Required: true, Min: resource.Min(1)},
		{Name: "nextDue", Column: "next_due", Label: "Next Due", Kind: resource.Date, Required: true},
		{Name: "closesUnit", Column: "closes_unit", Label: "Bungalow Out of Order while worked", Kind: resource.Bool, Default: false},
		{Name: "durationHours", Column: "duration_hours", Label: "Duration (hours)", Kind: resource.Int, Default: int64(2), Min: resource.Min(1)},
		{Name: "assignedTo", Column: "assigned_to", Label: "Technician / Team", Kind: resource.String, Max: 120},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000},
		{Name: "lastGenerated", Column: "last_generated", Label: "Last Generated", Kind: resource.Date, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// accommodationDefs carry their own permission prefix; accommodationShared
// reuse one (rate plan prices, seasons and season prices: stay.rate_plan).
var (
	accommodationDefs   = []*resource.Def{Addons, StayPackages, RatePlans, Promotions, CorporateTerms, PreventiveSchedules}
	accommodationShared = []*resource.Def{RatePlanPrices, Seasons, SeasonPrices}
)

func accommodationPermissions() []catalog.Permission {
	var out []catalog.Permission
	for _, p := range [][]catalog.Permission{
		catalog.P("stay", "stay", "reschedule", "charge"),
		catalog.P("stay", "room_status", "view", "update"),
		catalog.P("stay", "room_block", "view", "manage"),
		catalog.P("stay", "housekeeping", "view", "work", "manage", "inspect"),
		catalog.P("stay", "work_order", "view", "create", "work", "manage"),
		catalog.P("stay", "guest_request", "view", "create", "work"),
		catalog.P("stay", "waitlist", "view", "manage"),
		catalog.P("stay", "guest", "view", "update"),
		catalog.P("stay", "dashboard", "view"),
	} {
		out = append(out, p...)
	}
	return out
}

// Role permissions of the accommodation (§34). The manager gets every
// accommodation permission; the other roles what their work needs.
var (
	accommodationManager = []string{"stay.stay.reschedule", "stay.stay.charge", "stay.room_status.view", "stay.room_status.update",
		"stay.room_block.view", "stay.room_block.manage", "stay.housekeeping.view", "stay.housekeeping.work", "stay.housekeeping.manage",
		"stay.housekeeping.inspect", "stay.work_order.view", "stay.work_order.create", "stay.work_order.work", "stay.work_order.manage",
		"stay.guest_request.view", "stay.guest_request.create", "stay.guest_request.work", "stay.waitlist.view", "stay.waitlist.manage",
		"stay.guest.view", "stay.guest.update", "stay.dashboard.view"}
	accommodationFrontOffice = []string{"stay.stay.reschedule", "stay.stay.charge", "stay.room_status.view", "stay.room_status.update",
		"stay.room_block.view", "stay.housekeeping.view", "stay.work_order.view", "stay.work_order.create", "stay.guest_request.view",
		"stay.guest_request.create", "stay.guest_request.work", "stay.waitlist.view", "stay.waitlist.manage", "stay.guest.view", "stay.guest.update",
		"stay.dashboard.view", "stay.addon.view", "stay.package.view", "stay.rate_plan.view", "stay.promotion.view", "stay.corporate_term.view"}
	accommodationReservations = []string{"stay.stay.reschedule", "stay.room_status.view", "stay.room_block.view", "stay.waitlist.view",
		"stay.waitlist.manage", "stay.guest.view", "stay.guest.update", "stay.guest_request.view", "stay.guest_request.create",
		"stay.addon.view", "stay.package.view", "stay.rate_plan.view", "stay.promotion.view", "stay.corporate_term.view", "stay.dashboard.view"}
	accommodationHousekeeping = []string{"stay.bungalow.view", "stay.bungalow_type.view", "stay.room_status.view", "stay.room_status.update",
		"stay.housekeeping.view", "stay.housekeeping.work", "stay.housekeeping.inspect", "stay.guest_request.view", "stay.guest_request.work",
		"stay.work_order.view", "stay.work_order.create"}
	accommodationMaintenance = []string{"stay.bungalow.view", "stay.bungalow_type.view", "stay.room_status.view", "stay.work_order.view",
		"stay.work_order.create", "stay.work_order.work", "stay.guest_request.view", "stay.guest_request.work", "stay.preventive_schedule.view"}
	accommodationViewer = []string{"stay.dashboard.view", "stay.room_status.view", "stay.housekeeping.view", "stay.work_order.view",
		"stay.guest_request.view", "stay.waitlist.view", "stay.guest.view", "stay.bungalow_type.view"}
)
