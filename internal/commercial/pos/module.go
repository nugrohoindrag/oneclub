// Package pos is POS & F&B of PRD P2 (EP-20/21), the P2-owned
// commercial/pos sub-package (PRD P2 §5.4.1): outlets, menu, orders,
// kitchen display, split bills, shifts and cashier, member charge and
// voucher tenders through billing (contracts C1–C3). P1's commercial
// resources get the P2 product and outlet fields additively.
package pos

import (
	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/commercial/voucher"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/notify"
	"oneclub/internal/platform/outbox"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/platform/resource"
	"oneclub/internal/platform/rules"
)

// Module is the POS service.
type Module struct {
	DB        *dbtx.DB
	Billing   *billing.Service
	Events    *outbox.Bus
	Approvals *approval.Engine
	Notify    notify.Sender
	Realtime  *realtime.Hub
	Vouchers  *voucher.Module // voucher sale and redemption at the POS
}

// P2 POS fields on P1's Products and Outlets (expand columns of
// commercial/00005).
var (
	productFields = []resource.Field{
		{Name: "productType", Column: "product_type", Label: "Product Type", Kind: resource.Enum, Enum: []string{"food", "beverage", "retail", "service", "package"}, Default: "food", Filter: true},
		{Name: "price", Column: "price", Label: "Price", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "memberPrice", Column: "member_price", Label: "Member Price", Kind: resource.Decimal, Min: resource.Min(0)},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Max: 60, Search: true},
		{Name: "kitchenStation", Column: "kitchen_station", Label: "Kitchen Station", Kind: resource.String, Max: 40, Filter: true},
		{Name: "outletIds", Column: "outlet_ids", Label: "Outlets (empty = all)", Kind: resource.StringList, Default: []string{}},
		{Name: "comboItems", Column: "combo_items", Label: "Combo / Package Items", Kind: resource.JSONList, Default: "[]"},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component (default: outlet)", Kind: resource.Enum, Enum: billing.RevenueComponents},
		{Name: "voucherTypeId", Column: "voucher_type_id", Label: "Sells Voucher Type", Kind: resource.UUID, Ref: &resource.Ref{Table: "commercial.voucher_types", SameProperty: true, Label: "voucher type"}},
	}
	outletFields = []resource.Field{
		{Name: "taxCodes", Column: "tax_codes", Label: "Tax & Service Codes (empty = all)", Kind: resource.StringList, Upper: true, Default: []string{}},
		{Name: "pricingMode", Column: "pricing_mode", Label: "Pricing Mode", Kind: resource.Enum, Enum: []string{"nett", "plus_plus"}, Default: "nett"},
		{Name: "openingTime", Column: "opening_time", Label: "Opening Time", Kind: resource.Time},
		{Name: "closingTime", Column: "closing_time", Label: "Closing Time", Kind: resource.Time},
		{Name: "kdsStations", Column: "kds_stations", Label: "Kitchen Stations", Kind: resource.StringList, Default: []string{}},
		{Name: "printerDevice", Column: "printer_device", Label: "Receipt Printer (bridge device)", Kind: resource.String, Max: 80},
		{Name: "revenueComponent", Column: "revenue_component", Label: "Revenue Component", Kind: resource.Enum, Enum: billing.RevenueComponents, Default: "fnb"},
	}
)

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: "pos.policy", Category: "POS Policies", Name: "Discount, offline member charge & pre-order",
		Description: "Discount limit without supervisor, member charge while offline, pre-order lead time, default kitchen station", Default: defaultPOSPolicy})
	commercial.Products.Fields = insertBeforeStatus(commercial.Products.Fields, productFields)
	commercial.Outlets.Fields = insertBeforeStatus(commercial.Outlets.Fields, outletFields)
}

// insertBeforeStatus keeps status and attributes last.
func insertBeforeStatus(fields, extra []resource.Field) []resource.Field {
	for i, f := range fields {
		if f.Name == "status" {
			out := append(append(append([]resource.Field{}, fields[:i]...), extra...), fields[i:]...)
			return out
		}
	}
	return append(fields, extra...)
}

// Register adds the POS routes (wired by internal/app).
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	for _, d := range []*resource.Def{ProductVariants, ModifierGroups, Modifiers, Menus} {
		eng.Register(reg, d)
	}
	m.registerMe(reg)
	m.registerPOS(reg, eng)
}

// ProductVariants, ModifierGroups, Modifiers and Menus (FR-POS-02).
var ProductVariants = &resource.Def{
	Key: "commercial.product_variant", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/product-variants",
	Table: "commercial.product_variants", Name: "Variant", Plural: "Variants", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "productId", Column: "product_id", Label: "Product", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.products", SameProperty: true, Label: "product"}},
		{Name: "priceDelta", Column: "price_delta", Label: "Price Difference", Kind: resource.Decimal, Default: "0"},
		{Name: "barcode", Column: "barcode", Label: "Barcode", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive")},
}

var ModifierGroups = &resource.Def{
	Key: "commercial.modifier_group", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/modifier-groups",
	Table: "commercial.modifier_groups", Name: "Modifier Group", Plural: "Modifier Groups", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "minSelect", Column: "min_select", Label: "Minimum", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "maxSelect", Column: "max_select", Label: "Maximum", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "productIds", Column: "product_ids", Label: "Products", Kind: resource.StringList, Default: []string{}},
		resource.Status("active", "inactive")},
}

var Modifiers = &resource.Def{
	Key: "commercial.modifier", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/modifiers", Table: "commercial.modifiers",
	Name: "Modifier", Plural: "Modifiers", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "groupId", Column: "group_id", Label: "Modifier Group", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.modifier_groups", SameProperty: true, Label: "modifier group"}},
		{Name: "priceDelta", Column: "price_delta", Label: "Price Difference", Kind: resource.Decimal, Default: "0"},
		resource.Status("active", "inactive")},
}

var Menus = &resource.Def{
	Key: "commercial.menu", Module: "commercial", Perm: "commercial.product", Path: "/api/v1/commercial/menus", Table: "commercial.menus",
	Name: "Menu", Plural: "Menus", Tag: "POS", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "outletId", Column: "outlet_id", Label: "Outlet", Kind: resource.UUID, Required: true, Filter: true, Ref: &resource.Ref{Table: "commercial.outlets", SameProperty: true, Label: "outlet"}},
		{Name: "availableFrom", Column: "available_from", Label: "Available From", Kind: resource.Time},
		{Name: "availableTo", Column: "available_to", Label: "Available To", Kind: resource.Time},
		{Name: "weekdays", Column: "weekdays", Label: "Weekdays", Kind: resource.IntList, Enum: []string{"1", "2", "3", "4", "5", "6", "7"}, Default: []int64{1, 2, 3, 4, 5, 6, 7}},
		{Name: "productIds", Column: "product_ids", Label: "Products", Kind: resource.StringList, Default: []string{}},
		{Name: "channels", Column: "channels", Label: "Channels", Kind: resource.StringList, Enum: []string{"pos", "member_app", "caddy_tablet", "website"}, Default: []string{"pos"}},
		resource.Status("active", "inactive")},
}

// Contribution is the P2 commercial catalogue: POS, vouchers and the P2
// roles of the pricing and foundation resources.
func Contribution() catalog.Contribution {
	perms := append(append(catalog.P("commercial", "tax_service", "view", "create", "update", "export"),
		resource.Permissions(commercial.Products)...), resource.Permissions(commercial.Outlets)...)
	perms = append(perms, resource.Permissions(commercial.PricingRules)...)
	perms = append(perms, resource.Permissions(voucher.VoucherTypes)...)
	perms = append(perms, catalog.P("commercial", "order", "view", "create", "pay", "void", "refund")...)
	perms = append(perms, catalog.P("commercial", "pos", "discount", "discount_override")...)
	perms = append(perms, catalog.P("commercial", "shift", "view", "manage")...)
	perms = append(perms, catalog.P("commercial", "kitchen", "view", "update")...)
	perms = append(perms, catalog.P("commercial", "voucher", "view", "sell", "issue", "redeem", "transfer", "extend", "adjust", "void")...)
	voucherFront := []string{"commercial.voucher_type.view", "commercial.voucher.view", "commercial.voucher.sell", "commercial.voucher.redeem"}
	voucherAll := append(append(resource.AllActions(voucher.VoucherTypes), voucherFront...), "commercial.voucher.issue", "commercial.voucher.transfer",
		"commercial.voucher.extend", "commercial.voucher.adjust", "commercial.voucher.void")
	pricingAll := resource.AllActions(commercial.PricingRules)
	pricingView := []string{"commercial.pricing.view"}
	rp := map[string][]string{
		"property_admin":  append(append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update", "commercial.tax_service.export"}, resource.AllActions(commercial.Products, commercial.Outlets)...), pricingAll...),
		"finance_manager": append([]string{"commercial.tax_service.view", "commercial.tax_service.create", "commercial.tax_service.update"}, pricingAll...),
		"accountant":      append([]string{"commercial.tax_service.view"}, pricingView...),
		"outlet_manager":  append([]string{"commercial.product.view", "commercial.outlet.view"}, pricingView...),
		"cashier":         append([]string{"commercial.outlet.view"}, pricingView...),
		"pos_staff":       append([]string{"commercial.outlet.view"}, pricingView...),
		"kitchen_staff":   {"commercial.outlet.view"},
	}
	for _, role := range []string{"general_manager", "club_manager", "resort_manager", "golf_manager", "golf_admin", "sport_club_manager",
		"sport_club_receptionist", "reservation_staff", "front_desk", "membership_admin", "membership_manager", "driving_range_staff", "banquet_sales"} {
		rp[role] = append(rp[role], pricingView...)
	}
	for _, role := range []string{"property_admin", "finance_manager", "outlet_manager", "sport_club_manager"} {
		rp[role] = append(rp[role], voucherAll...)
	}
	for _, role := range []string{"cashier", "pos_staff", "sport_club_receptionist", "front_desk", "driving_range_staff", "reservation_staff",
		"membership_admin", "golf_admin"} {
		rp[role] = append(rp[role], voucherFront...)
	}
	for _, role := range []string{"accountant", "general_manager", "club_manager", "marketing_staff", "crm_admin"} {
		rp[role] = append(rp[role], "commercial.voucher_type.view", "commercial.voucher.view")
	}
	posAll := []string{"commercial.order.view", "commercial.order.create", "commercial.order.pay", "commercial.order.void", "commercial.order.refund",
		"commercial.pos.discount", "commercial.pos.discount_override", "commercial.shift.view", "commercial.shift.manage", "commercial.kitchen.view", "commercial.kitchen.update"}
	posCashier := []string{"commercial.order.view", "commercial.order.create", "commercial.order.pay", "commercial.order.void", "commercial.pos.discount",
		"commercial.shift.view", "commercial.shift.manage", "commercial.kitchen.view", "commercial.product.view"}
	for _, role := range []string{"property_admin", "outlet_manager"} {
		rp[role] = append(rp[role], posAll...)
		rp[role] = append(rp[role], resource.AllActions(commercial.Products)...)
	}
	for _, role := range []string{"cashier", "pos_staff", "driving_range_staff", "sport_club_receptionist"} {
		rp[role] = append(rp[role], posCashier...)
	}
	rp["kitchen_staff"] = append(rp["kitchen_staff"], "commercial.kitchen.view", "commercial.kitchen.update", "commercial.order.view")
	for _, role := range []string{"caddy", "front_desk", "banquet_manager", "reservation_staff"} {
		rp[role] = append(rp[role], "commercial.order.view", "commercial.order.create", "commercial.product.view", "commercial.outlet.view")
	}
	for _, role := range []string{"general_manager", "finance_manager", "accountant", "club_manager"} {
		rp[role] = append(rp[role], "commercial.order.view", "commercial.shift.view")
	}
	rp["marketing_staff"] = append(rp["marketing_staff"], "commercial.voucher.issue")
	rp["crm_admin"] = append(rp["crm_admin"], "commercial.voucher.issue")
	return catalog.Contribution{Permissions: perms, RolePermissions: rp}
}

// Types of the public commercial interface (internal/commercial/sales_api.go).
type (
	OrderLine  = commercial.OrderLine
	Bill       = commercial.Bill
	Order      = commercial.Order
	LineInput  = commercial.LineInput
	OrderInput = commercial.OrderInput
)

var _ commercial.POS = (*Module)(nil)
