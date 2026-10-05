// Package banquet is Banquet & Event (PRD P3 EP-12 Event Management, EP-13
// Banquet, MICE & Wedding, EP-14 Banquet Event Order, EP-15 Banquet Venue &
// Event Resource). The event is the parent entity; a banquet is an event
// with the banquet sales flow and the BEO (PRD P3 §6 #16). Venues are
// Reservation Engine resources, money lives on the event folio in Billing
// (business line banquet) and P4 receives the procurement requirement (K1)
// and the consumption (K6) through domain events.
package banquet

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
	"oneclub/internal/reservation"
)

// Domain events (docs/p3-p4-contracts.md).
const (
	EventConfirmed = "banquet.event_confirmed"
	EventCancelled = "banquet.event_cancelled"
	EventCompleted = "banquet.event_completed" // K6
	EventBEOIssued = "banquet.beo_issued"      // K1
	EventBEORevise = "banquet.beo_revised"     // K1
)

// Events consumed (decoded by name: banquet never imports crm/sales or the
// package engine).
const (
	QuotationSent     = "crm.quotation_sent"
	QuotationAccepted = "crm.quotation_accepted"
	QuotationRejected = "crm.quotation_rejected"
	QuotationExpired  = "crm.quotation_expired"
	PackageBooked     = "commercial.package_booked"
	ScheduleDue       = billing.EventScheduleDue
)

// Event statuses (PRD P3 §7.6: Tentative, Definite; Naming Convention §31).
const (
	StatusInquiry   = "inquiry"
	StatusTentative = "tentative"
	StatusDefinite  = "definite"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
)

// QuotationLines are the business lines of crm.quotation_* events that
// become banquet events (the others are converted by their own modules).
var QuotationLines = []string{"wedding", "banquet", "mice", "event"}

// VoucherIssuer issues the F&B vouchers of a package (FR-BQT-06); wired by
// internal/app to the voucher engine (commercial/voucher). It returns the
// voucher codes.
type VoucherIssuer func(ctx context.Context, tx pgx.Tx, property uuid.UUID, typeCode string, quantity int, customerID uuid.UUID, eventID uuid.UUID,
	note string) ([]string, error)

// Module is the Banquet & Event module.
type Module struct {
	DB        *dbtx.DB
	Res       *reservation.Engine
	Billing   *billing.Service
	Invoices  *billing.HTTP // invoices of corporate events and the final bill
	Events    *outbox.Bus
	Approvals *approval.Engine
	Notify    notify.Sender
	// Vouchers issues the voucher inclusions of packages; nil = vouchers
	// stay pending.
	Vouchers VoucherIssuer
	// WebsiteURL and StaffURL build the links of notifications.
	WebsiteURL func() string
	StaffURL   func() string
}

const tag = "Banquet & Event"

// Categories of event types: they group the Back Office menus Events,
// Banquet, MICE and Weddings (Naming Convention §11).
var Categories = []string{"wedding", "banquet", "mice", "social", "sport", "tournament", "other"}

// Layouts are the setup styles with a capacity per venue (FR-VEN-01).
var Layouts = []string{"round_table", "classroom", "u_shape", "theater", "boardroom", "standing", "banquet", "cocktail"}

// Departments receive the BEO and own checklist tasks (FR-BEO-03).
var Departments = []string{"banquet", "sales", "kitchen", "fnb_service", "venue", "engineering", "front_desk", "golf", "finance", "security",
	"housekeeping", "other"}

// RevenueComponents of banquet charges (billing.RevenueComponents).
var RevenueComponents = []string{"banquet_package", "banquet_fnb", "venue_rental", "corkage", "outdoor_venue", "electricity", "event_fee",
	"damage_charge", "other"}

// ChargeKinds of extra charges.
var ChargeKinds = []string{"corkage", "outdoor_venue", "electricity", "overtime", "decoration", "av_equipment", "vendor_fee", "additional_fnb",
	"venue_rental", "damage", "other"}

func modeField(name, column, label string) resource.Field {
	return resource.Field{Name: name, Column: column, Label: label, Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "plus_plus"}
}

func taxCodesField() resource.Field {
	return resource.Field{Name: "taxCodes", Column: "tax_codes", Label: "Tax & Service Codes (empty = all in force)", Kind: resource.StringList,
		Default: []string{}, Upper: true}
}

// EventTypes are the event types (Product Overview §27).
var EventTypes = &resource.Def{
	Key: "banquet.event_type", Module: "banquet", Perm: "banquet.event_type", Path: "/api/v1/banquet/event-types", Table: "banquet.event_types",
	Name: "Event Type", Plural: "Event Types", SchemaName: "BanquetEventType", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Enum: Categories, Default: "other", Filter: true},
		{Name: "banquetFlow", Column: "banquet_flow", Label: "Banquet flow (quotation, BEO)", Kind: resource.Bool, Default: true},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// Venues are banquet venues; each is a Reservation Engine resource of type
// banquet_venue (FR-VEN-01/02). A venue with bookings is never deleted:
// it is set Inactive (its bookable resource follows).
var Venues = &resource.Def{
	Key: "banquet.venue", Module: "banquet", Perm: "banquet.venue", Path: "/api/v1/banquet/venues", Table: "banquet.venues",
	Name: "Venue", Plural: "Venues", SchemaName: "BanquetVenue", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	NoDelete: true,
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "venueType", Column: "venue_type", Label: "Venue Type", Kind: resource.Enum,
			Enum: []string{"ballroom", "function_room", "outdoor", "meeting_room", "vip_suite", "other"}, Default: "function_room", Filter: true},
		{Name: "parentVenueId", Column: "parent_venue_id", Label: "Part of (combined venue)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "banquet.venues", SameProperty: true, Label: "venue"}},
		{Name: "platformVenueId", Column: "platform_venue_id", Label: "Site", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "platform.venues", SameProperty: true, Label: "venue"}},
		{Name: "sizeSqm", Column: "size_sqm", Label: "Size (m²)", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "maxCapacity", Column: "max_capacity", Label: "Maximum Capacity (pax)", Kind: resource.Int, Min: resource.Min(1)},
		{Name: "minPax", Column: "min_pax", Label: "Minimum Pax", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "addonPrice", Column: "addon_price", Label: "Venue Add-on Price (outdoor)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "rentalPrice", Column: "rental_price", Label: "Venue Rental Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "electricityWatt", Column: "electricity_watt", Label: "Electricity Included (watt)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "facilities", Column: "facilities", Label: "Facilities", Kind: resource.StringList, Default: []string{}},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "public", Column: "public", Label: "Show on the website", Kind: resource.Bool, Default: true},
		{Name: "resourceId", Column: "resource_id", Label: "Bookable Resource", Kind: resource.UUID, ReadOnly: true},
		resource.Status("active", "inactive")},
}

// VenueLayouts are the capacities per setup style.
var VenueLayouts = &resource.Def{
	Key: "banquet.venue_layout", Module: "banquet", Perm: "banquet.venue", Path: "/api/v1/banquet/venue-layouts", Table: "banquet.venue_layouts",
	Name: "Venue Layout", Plural: "Venue Layouts", SchemaName: "BanquetVenueLayout", Tag: tag, PropertyScoped: true, OrderBy: "venue_id, capacity",
	Fields: []resource.Field{
		{Name: "venueId", Column: "venue_id", Label: "Venue", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "banquet.venues", SameProperty: true, Label: "venue"}},
		{Name: "layout", Column: "layout", Label: "Layout", Kind: resource.Enum, Enum: Layouts, Required: true, CreateOnly: true, Filter: true},
		{Name: "capacity", Column: "capacity", Label: "Capacity (pax)", Kind: resource.Int, Required: true, Min: resource.Min(1)}},
}

// Packages are banquet packages (FR-BQT-01, FR-BQT-06, FR-BQT-13).
var Packages = &resource.Def{
	Key: "banquet.package", Module: "banquet", Perm: "banquet.package", Path: "/api/v1/banquet/packages", Table: "banquet.packages",
	Name: "Banquet Package", Plural: "Banquet Packages", SchemaName: "BanquetPackage", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum,
			Enum: []string{"wedding", "banquet", "mice", "birthday", "social", "corporate", "other"}, Default: "other", Filter: true},
		{Name: "pricingMethod", Column: "pricing_method", Label: "Pricing Method", Kind: resource.Enum, Enum: []string{"per_pax", "per_pax_per_day", "fixed"},
			Default: "per_pax"},
		{Name: "price", Column: "price", Label: "Price (per pax, per pax per day or package)", Kind: resource.Decimal, Required: true, Min: resource.Min(0)},
		modeField("pricingMode", "pricing_mode", "Price is Nett or ++"),
		taxCodesField(),
		{Name: "minPax", Column: "min_pax", Label: "Minimum Pax (charged at least)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "includedPax", Column: "included_pax", Label: "Pax included in the package price", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "extraPaxPrice", Column: "extra_pax_price", Label: "Additional Pax Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		modeField("extraPaxMode", "extra_pax_mode", "Additional Pax Price is Nett or ++"),
		{Name: "durationHours", Column: "duration_hours", Label: "Venue Hours", Kind: resource.Int, Default: int64(4), Min: resource.Min(1)},
		{Name: "electricityWatt", Column: "electricity_watt", Label: "Electricity Included (watt)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "menuId", Column: "menu_id", Label: "Menu", Kind: resource.UUID, Ref: &resource.Ref{Table: "banquet.menus", SameProperty: true, Label: "menu"}},
		{Name: "inclusions", Column: "inclusions", Label: "Inclusions (bungalow night, golf cart, F&B voucher, food tasting …)", Kind: resource.JSONList,
			Default: "[]"},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.Enum, Enum: RevenueComponents,
			Default: "banquet_package"},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "public", Column: "public", Label: "Show on the website", Kind: resource.Bool, Default: true},
		resource.Status("active", "inactive")},
}

// Menus with category quotas and items (FR-BQT-02).
var Menus = &resource.Def{
	Key: "banquet.menu", Module: "banquet", Perm: "banquet.menu", Path: "/api/v1/banquet/menus", Table: "banquet.menus",
	Name: "Banquet Menu", Plural: "Banquet Menus", SchemaName: "BanquetMenu", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "menuType", Column: "menu_type", Label: "Menu Type", Kind: resource.Enum,
			Enum: []string{"buffet", "set_menu", "cocktail", "coffee_break", "food_stall", "kids", "beverage", "other"}, Default: "buffet", Filter: true},
		{Name: "pricePerPax", Column: "price_per_pax", Label: "Price per Pax (when added to a package)", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		modeField("pricingMode", "pricing_mode", "Price is Nett or ++"),
		taxCodesField(),
		{Name: "outletId", Column: "outlet_id", Label: "Kitchen / Outlet", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		{Name: "public", Column: "public", Label: "Show on the website", Kind: resource.Bool, Default: true},
		resource.Status("active", "inactive")},
}

// MenuCategories are the choice quotas of a menu (2 appetizers, rice …).
var MenuCategories = &resource.Def{
	Key: "banquet.menu_category", Module: "banquet", Perm: "banquet.menu", Path: "/api/v1/banquet/menu-categories", Table: "banquet.menu_categories",
	Name: "Menu Category", Plural: "Menu Categories", SchemaName: "BanquetMenuCategory", Tag: tag, PropertyScoped: true,
	OrderBy: "menu_id, sort_order, name",
	Fields: []resource.Field{
		{Name: "menuId", Column: "menu_id", Label: "Menu", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "banquet.menus", SameProperty: true, Label: "menu"}},
		{Name: "name", Column: "name", Label: "Category", Kind: resource.String, Required: true, Max: 80, Search: true},
		{Name: "quota", Column: "quota", Label: "Choices included", Kind: resource.Int, Default: int64(1), Min: resource.Min(0)},
		{Name: "extraChoicePrice", Column: "extra_choice_price", Label: "Price per pax of an extra choice (empty = not allowed)", Kind: resource.Decimal,
			Min: resource.Min(0)},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)}},
}

// MenuItems link a dish to the product and recipe (BOM per pax, FR-BQT-11).
var MenuItems = &resource.Def{
	Key: "banquet.menu_item", Module: "banquet", Perm: "banquet.menu", Path: "/api/v1/banquet/menu-items", Table: "banquet.menu_items",
	Name: "Menu Item", Plural: "Menu Items", SchemaName: "BanquetMenuItem", Tag: tag, PropertyScoped: true, Archive: true, OrderBy: "menu_id, name, id",
	Fields: []resource.Field{
		{Name: "menuId", Column: "menu_id", Label: "Menu", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "banquet.menus", SameProperty: true, Label: "menu"}},
		{Name: "categoryId", Column: "category_id", Label: "Category", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "banquet.menu_categories", SameProperty: true, Label: "menu category"}},
		resource.Name(),
		{Name: "course", Column: "course", Label: "Course", Kind: resource.String, Max: 40},
		{Name: "station", Column: "station", Label: "Station (buffet, food stall, kitchen)", Kind: resource.String, Max: 40},
		{Name: "productId", Column: "product_id", Label: "Product", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "recipeId", Column: "recipe_id", Label: "Recipe (BOM)", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "inventory.recipes", SameProperty: true, Label: "recipe"}},
		{Name: "portionPerPax", Column: "portion_per_pax", Label: "Portions per Pax", Kind: resource.Decimal, Default: "1", Min: resource.Min(0)},
		{Name: "serveOffsetMinutes", Column: "serve_offset_minutes", Label: "Served (minutes after start)", Kind: resource.Int, Default: int64(0)},
		{Name: "allergens", Column: "allergens", Label: "Allergens", Kind: resource.StringList, Default: []string{}},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// ChargeTypes are the extra charges (corkage, outdoor add-on, electricity …).
var ChargeTypes = &resource.Def{
	Key: "banquet.charge_type", Module: "banquet", Perm: "banquet.charge_type", Path: "/api/v1/banquet/charge-types", Table: "banquet.charge_types",
	Name: "Extra Charge", Plural: "Extra Charges", SchemaName: "BanquetChargeType", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "kind", Column: "kind", Label: "Kind", Kind: resource.Enum, Enum: ChargeKinds, Default: "other", Filter: true},
		{Name: "unit", Column: "unit", Label: "Unit", Kind: resource.Enum, Enum: []string{"item", "bottle", "hour", "pax", "kw", "package", "day"}, Default: "item"},
		{Name: "unitPrice", Column: "unit_price", Label: "Unit Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		modeField("pricingMode", "pricing_mode", "Price is Nett or ++"),
		taxCodesField(),
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component (default by kind)", Kind: resource.Enum, Enum: RevenueComponents},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

// Vendors are outside vendors (FR-EVT-06).
var Vendors = &resource.Def{
	Key: "banquet.vendor", Module: "banquet", Perm: "banquet.vendor", Path: "/api/v1/banquet/vendors", Table: "banquet.vendors",
	Name: "Event Vendor", Plural: "Event Vendors", SchemaName: "BanquetVendor", Tag: tag, PropertyScoped: true, Archive: true, CodeField: "code",
	OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "vendorType", Column: "vendor_type", Label: "Vendor Type", Kind: resource.Enum, Enum: []string{"decoration", "mc", "band", "photographer",
			"videographer", "florist", "wedding_organizer", "catering", "av", "makeup", "other"}, Default: "other", Filter: true},
		{Name: "contactName", Column: "contact_name", Label: "Contact", Kind: resource.String, Max: 120},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email},
		{Name: "partner", Column: "partner", Label: "Partner vendor (outside food allowed)", Kind: resource.Bool, Default: false},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

// ChecklistTemplates are event checklist templates per event type (FR-EVT-07).
var ChecklistTemplates = &resource.Def{
	Key: "banquet.checklist_template", Module: "banquet", Perm: "banquet.checklist_template", Path: "/api/v1/banquet/checklist-templates",
	Table: "banquet.checklist_templates", Name: "Checklist Template", Plural: "Checklist Templates", SchemaName: "BanquetChecklistTemplate", Tag: tag,
	PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "eventTypeId", Column: "event_type_id", Label: "Event Type (applied automatically)", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "banquet.event_types", SameProperty: true, Label: "event type"}},
		resource.Status("active", "inactive")},
}

// ChecklistTemplateItems are the tasks of a template.
var ChecklistTemplateItems = &resource.Def{
	Key: "banquet.checklist_template_item", Module: "banquet", Perm: "banquet.checklist_template", Path: "/api/v1/banquet/checklist-template-items",
	Table: "banquet.checklist_template_items", Name: "Checklist Template Task", Plural: "Checklist Template Tasks", SchemaName: "BanquetChecklistTemplateItem",
	Tag: tag, PropertyScoped: true, OrderBy: "template_id, sort_order, days_before DESC",
	Fields: []resource.Field{
		{Name: "templateId", Column: "template_id", Label: "Template", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
			Ref: &resource.Ref{Table: "banquet.checklist_templates", SameProperty: true, Label: "checklist template"}},
		{Name: "task", Column: "task", Label: "Task", Kind: resource.String, Required: true, Max: 200, Search: true},
		{Name: "department", Column: "department", Label: "Department", Kind: resource.Enum, Enum: Departments, Default: "banquet"},
		{Name: "daysBefore", Column: "days_before", Label: "Due (days before the event)", Kind: resource.Int, Default: int64(7)},
		{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)}},
}

// Defs are the master data resources of the module.
func Defs() []*resource.Def {
	return []*resource.Def{EventTypes, Venues, VenueLayouts, Packages, Menus, MenuCategories, MenuItems, ChargeTypes, Vendors, ChecklistTemplates,
		ChecklistTemplateItems}
}

// DefiniteOverrideType approves an event as Definite before the down
// payment is paid ("manual with permission", FR-VEN-03).
var DefiniteOverrideType = provision.DocumentType{Code: "banquet_definite_override", Module: "banquet", Name: "Definite without Deposit",
	Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Contract Total", Type: "number"}}}

// DocumentTypes are the approval document types of the module.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{DefiniteOverrideType}
}

// Contribution returns the catalogue entries: permissions and role templates
// (Product Overview §44: Banquet Manager, Banquet Sales, Event Manager,
// Event Staff; Kitchen Staff for Banquet Production).
func Contribution() catalog.Contribution {
	defs := []*resource.Def{EventTypes, Venues, Packages, Menus, ChargeTypes, Vendors, ChecklistTemplates}
	perms := resource.Permissions(defs...)
	perms = append(perms, catalog.P("banquet", "event", "view", "create", "update", "hold", "confirm", "cancel", "complete", "operate", "import")...)
	perms = append(perms, catalog.P("banquet", "billing", "view", "manage")...)
	perms = append(perms, catalog.P("banquet", "beo", "view", "manage", "issue", "acknowledge")...)
	perms = append(perms, catalog.P("banquet", "production", "view", "update")...)
	perms = append(perms, catalog.P("banquet", "checklist", "view", "update")...)
	perms = append(perms, catalog.P("banquet", "participant", "view", "manage", "check_in")...)
	all := make([]string, 0, len(perms))
	for _, p := range perms {
		all = append(all, p.Code)
	}
	masterView := []string{"banquet.event_type.view", "banquet.venue.view", "banquet.package.view", "banquet.menu.view", "banquet.charge_type.view",
		"banquet.vendor.view", "banquet.checklist_template.view"}
	views := append(append([]string{}, masterView...), "banquet.event.view", "banquet.billing.view", "banquet.beo.view", "banquet.production.view",
		"banquet.checklist.view", "banquet.participant.view")
	with := func(base []string, extra ...string) []string { return append(append([]string{}, base...), extra...) }
	sales := with(masterView, "banquet.event.view", "banquet.event.create", "banquet.event.update", "banquet.event.hold", "banquet.event.confirm",
		"banquet.event.cancel", "banquet.billing.view", "banquet.billing.manage", "banquet.beo.view", "banquet.beo.manage", "banquet.checklist.view",
		"banquet.checklist.update", "banquet.participant.view", "banquet.participant.manage")
	eventManager := with(views, "banquet.event.create", "banquet.event.update", "banquet.event.hold", "banquet.event.complete", "banquet.event.operate",
		"banquet.beo.manage", "banquet.beo.issue", "banquet.beo.acknowledge", "banquet.production.update", "banquet.checklist.update",
		"banquet.participant.manage", "banquet.participant.check_in")
	eventManager = append(eventManager, resource.AllActions(Vendors, ChecklistTemplates)...)
	staff := []string{"banquet.event.view", "banquet.event.operate", "banquet.beo.view", "banquet.beo.acknowledge", "banquet.production.view",
		"banquet.checklist.view", "banquet.checklist.update", "banquet.participant.view", "banquet.participant.manage", "banquet.participant.check_in",
		"banquet.venue.view"}
	kitchen := []string{"banquet.production.view", "banquet.production.update", "banquet.beo.view", "banquet.beo.acknowledge", "banquet.menu.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":  all,
			"resort_manager":  all,
			"banquet_manager": all,
			"general_manager": views,
			"banquet_sales":   sales,
			"sales_executive": with(masterView, "banquet.event.view", "banquet.event.create", "banquet.event.update", "banquet.event.hold",
				"banquet.billing.view", "banquet.beo.view", "banquet.participant.view"),
			"event_manager":     eventManager,
			"event_staff":       staff,
			"kitchen_staff":     kitchen,
			"outlet_manager":    with(kitchen, "banquet.event.view"),
			"front_desk":        {"banquet.event.view", "banquet.participant.view", "banquet.participant.check_in", "banquet.beo.view", "banquet.beo.acknowledge"},
			"finance_manager":   with(masterView, "banquet.event.view", "banquet.event.import", "banquet.billing.view", "banquet.billing.manage", "banquet.beo.view"),
			"accountant":        {"banquet.event.view", "banquet.billing.view"},
			"reservation_staff": {"banquet.event.view", "banquet.venue.view"},
		},
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func uuidPtr(v any) *uuid.UUID {
	if u, err := uuid.Parse(str(v)); err == nil {
		return &u
	}
	return nil
}

func intPtr(v any) *int {
	if n, ok := v.(int64); ok {
		x := int(n)
		return &x
	}
	return nil
}

func nzs(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(strings.TrimSpace(s))
	return d
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func actorName(ctx context.Context) string {
	if p := authz.From(ctx); p != nil && p.Name != "" {
		return p.Name
	}
	return "system"
}

func activeOr(s string) string {
	if s == "active" {
		return "active"
	}
	return "inactive"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// places are the decimal places of a currency.
func places(cur string) int32 {
	if cur == "IDR" || cur == "JPY" || cur == "" {
		return 0
	}
	return 2
}

// ticketCode is a random registration code (QR content).
func ticketCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 10)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return "EV" + string(b)
}

// venueHook keeps the Reservation Engine resource of a venue in sync
// (resource type banquet_venue, exclusive allocation with setup buffers) and
// keeps combined venues one level deep.
func (m *Module) venueHook() resource.Hooks {
	sync := func(ctx context.Context, tx pgx.Tx, row map[string]any) error {
		pid, _ := reqctx.Property(ctx)
		capacity := intPtr(row["maxCapacity"])
		if capacity == nil {
			var c *int
			if err := tx.QueryRow(ctx, `SELECT max(capacity) FROM banquet.venue_layouts WHERE venue_id = $1::uuid`, row["id"]).Scan(&c); err != nil {
				return err
			}
			capacity = c
		}
		rid, err := m.Res.EnsureResource(ctx, tx, uuidPtr(row["resourceId"]), reservation.ResourceRequest{PropertyID: pid, Code: "BQV-" + str(row["code"]),
			Name: str(row["name"]), ResourceType: "banquet_venue", Capacity: capacity, Status: activeOr(str(row["status"])),
			Attributes: map[string]any{"venueId": str(row["id"]), "venueType": str(row["venueType"])}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE banquet.venues SET resource_id = $2 WHERE id = $1::uuid`, row["id"], rid)
		return err
	}
	check := func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
		parent, ok := v["parentVenueId"]
		if !ok || parent == nil {
			return nil
		}
		var grand *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT parent_venue_id FROM banquet.venues WHERE id = $1::uuid`, parent).Scan(&grand); err != nil {
			return err
		}
		if grand != nil {
			return handle.Invalid("parentVenueId", "nested", "a part of a venue cannot have parts of its own")
		}
		if before != nil {
			if str(before["id"]) == str(parent) {
				return handle.Invalid("parentVenueId", "self", "a venue cannot be a part of itself")
			}
			var children int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM banquet.venues WHERE parent_venue_id = $1::uuid`, before["id"]).Scan(&children); err != nil {
				return err
			}
			if children > 0 {
				return handle.Invalid("parentVenueId", "has_parts", "a venue with parts cannot be a part of another venue")
			}
		}
		return nil
	}
	return resource.Hooks{
		BeforeWrite: check,
		AfterCreate: func(ctx context.Context, tx pgx.Tx, row map[string]any) error { return sync(ctx, tx, row) },
		AfterUpdate: func(ctx context.Context, tx pgx.Tx, _, after map[string]any) error { return sync(ctx, tx, after) },
	}
}

// menuItemCheck keeps an item's category within its menu.
func menuItemCheck(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	cat, ok := v["categoryId"]
	if !ok || cat == nil {
		return nil
	}
	menu := v["menuId"]
	if menu == nil && before != nil {
		menu = before["menuId"]
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM banquet.menu_categories WHERE id = $1::uuid AND menu_id = $2::uuid)`, cat, menu).Scan(&same); err != nil {
		return err
	}
	if !same {
		return handle.Invalid("categoryId", "other_menu", "the category belongs to another menu")
	}
	return nil
}

// packageCheck validates the inclusions of a package (FR-BQT-06).
func packageCheck(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	raw, ok := v["inclusions"]
	if !ok || raw == nil {
		return nil
	}
	list, err := parseInclusions(raw)
	if err != nil {
		return handle.Invalid("inclusions", "invalid", err.Error())
	}
	_ = list
	return nil
}

func (m *Module) hooks() {
	Venues.Hooks = m.venueHook()
	MenuItems.Hooks = resource.Hooks{BeforeWrite: menuItemCheck}
	Packages.Hooks = resource.Hooks{BeforeWrite: packageCheck}
	ChargeTypes.Hooks = resource.Hooks{BeforeWrite: func(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
		if before == nil && v["revenueComponent"] == nil {
			v["revenueComponent"] = componentOfKind(str(v["kind"]))
		}
		return nil
	}}
}

// componentOfKind is the default revenue component of an extra charge.
func componentOfKind(kind string) string {
	switch kind {
	case "corkage":
		return "corkage"
	case "outdoor_venue":
		return "outdoor_venue"
	case "electricity":
		return "electricity"
	case "additional_fnb":
		return "banquet_fnb"
	case "venue_rental", "overtime":
		return "venue_rental"
	case "damage":
		return "damage_charge"
	case "decoration", "av_equipment", "vendor_fee":
		return "event_fee"
	}
	return "other"
}
