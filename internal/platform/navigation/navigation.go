// Package navigation builds each shell's menu from Enabled Modules and the
// user's permissions (FR-SH-02). Labels follow the Naming Convention (§5
// Admin Dashboard, §23 Reports, §24 Settings, §27 Member Portal, Platform
// Administration). The UI only renders what the server returns; the backend
// still authorises every request.
package navigation

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
)

// Item is one menu entry.
type Item struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Path       string `json:"path"`
	Icon       string `json:"icon,omitempty" doc:"Material Symbols Rounded name"`
	Module     string `json:"module,omitempty"`
	Permission string `json:"-"`
	ComingSoon bool   `json:"comingSoon,omitempty" doc:"Module is enabled but its features arrive in a later phase"`
	Phase      string `json:"phase,omitempty"`
	Children   []Item `json:"children,omitempty"`
}

// Menu is a shell's navigation.
type Menu struct {
	Shell string `json:"shell" enum:"backoffice,management,member,ops,platform-admin"`
	Items []Item `json:"items"`
}

func live(module, label, icon, path string) Item {
	return Item{Key: module, Label: label, Path: path, Icon: icon, Module: module, Permission: catalog.ModuleAccess(module)}
}

func m(module, label, icon, path, phase string) Item {
	return Item{Key: module, Label: label, Path: path, Icon: icon, Module: module, Permission: catalog.ModuleAccess(module), ComingSoon: true, Phase: phase}
}

// mod is an operational module: it opens its first page and lists its
// menus (PRD P1 §6.1).
func mod(module, label, icon, path string, children ...Item) Item {
	return Item{Key: module, Label: label, Path: path, Icon: icon, Module: module, Permission: catalog.ModuleAccess(module), Children: children}
}

func s(key, label, path, perm string, children ...Item) Item {
	return Item{Key: key, Label: label, Path: path, Permission: perm, Children: children}
}

// Trees are the static menu definitions per shell. Paths are Staff App paths:
// Back Office at the root, the other staff shells under their area prefix
// (management /management, ops /ops, caddy /tablet, platform-admin /platform).
var Trees = map[string][]Item{
	"backoffice": {
		{Key: "dashboard", Label: "Dashboard", Path: "/", Icon: "space_dashboard"},
		{Key: "approvals", Label: "Approvals", Path: "/approvals", Icon: "approval"},
		mod("golf", "Golf", "golf_course", "/golf/tee-sheet",
			s("tee-sheet", "Tee Sheet", "/golf/tee-sheet", "golf.tee_sheet.view"),
			s("golf-bookings", "Bookings", "/golf/bookings", "golf.booking.view"),
			s("flights", "Flights", "/golf/flights", "golf.flight.view"),
			s("players", "Players", "/golf/players", "golf.booking.view"),
			s("caddies", "Caddies", "/golf/caddies", "golf.caddy.view"),
			s("golf-carts", "Golf Carts", "/golf/golf-carts", "golf.golf_cart.view"),
			s("course", "Course", "/golf/course", "golf.course.view"),
			s("check-in", "Check-in", "/golf/check-in", "golf.check_in.perform"),
			s("starter", "Starter", "/golf/starter", "golf.starter.view"),
			s("rain-checks", "Rain Checks", "/golf/rain-checks", "golf.rain_check.view"),
			s("golf-operations", "Round Operations", "/golf/operations", "golf.flight.view"),
			s("golf-master", "Golf Master Data", "/golf/master", "golf.course.view"),
			s("golf-settings", "Golf Settings", "/golf/settings", "golf.tee_sheet.view"),
		),
		live("sportclub", "Sport Club", "sports_tennis", "/sport-club"),
		mod("membership", "Membership", "card_membership", "/membership/members",
			s("members", "Members", "/membership/members", "membership.member.view"),
			s("membership-programs", "Membership Programs", "/membership/programs", "membership.program.view"),
			s("membership-types", "Membership Types", "/membership/types", "membership.program.view"),
			s("membership-packages", "Membership Packages", "/membership/packages", "membership.program.view"),
			s("applications", "Applications", "/membership/applications", "membership.application.view"),
			s("membership-lifecycle", "Lifecycle Requests", "/membership/lifecycle", "membership.membership.view"),
			s("membership-approvals", "Approvals", "/approvals", ""),
			s("membership-fees", "Membership Fees", "/billing/folios", "billing.folio.view"),
			s("membership-cards", "Membership Cards", "/membership/cards", "membership.card.view"),
			s("renewals", "Renewals", "/membership/renewals", "membership.membership.view"),
			s("membership-history", "Membership History", "/membership/history", "membership.membership.view"),
			s("membership-settings", "Membership Settings", "/settings/club-policies", "platform.club_policy.view"),
		),
		mod("reservation", "Booking", "event_available", "/booking/all",
			s("all-bookings", "All Bookings", "/booking/all", "golf.booking.view"),
			s("all-lines", "All Business Lines", "/booking/all-lines", "reservation.reservation.view"),
			s("availability", "Availability", "/booking/availability", "golf.tee_sheet.view"),
			s("booking-golf", "Golf", "/golf/bookings", "golf.booking.view"),
			s("booking-calendar", "Booking Calendar", "/golf/tee-sheet", "golf.tee_sheet.view"),
			s("booking-history", "Booking History", "/booking/history", "golf.booking.view"),
			s("cancellations", "Cancellations", "/booking/cancellations", "golf.booking.view"),
			s("booking-refunds", "Refunds", "/billing/refunds", "billing.refund.view"),
			s("booking-settings", "Booking Settings", "/golf/settings", "golf.tee_sheet.view"),
		),
		live("stay", "Stay & Venue", "hotel", "/stay-venue"),
		mod("banquet", "Banquet & Event", "celebration", "/banquet-event/events",
			s("banquet-events", "Events", "/banquet-event/events", "banquet.event.view"),
			s("banquet-banquet", "Banquet", "/banquet-event/events?category=banquet,social", "banquet.event.view"),
			s("banquet-mice", "MICE", "/banquet-event/events?category=mice", "banquet.event.view"),
			s("banquet-weddings", "Weddings", "/banquet-event/events?category=wedding", "banquet.event.view"),
			s("banquet-packages", "Packages", "/banquet-event/packages", "banquet.package.view"),
			s("banquet-venues", "Venues", "/banquet-event/venues", "banquet.venue.view"),
			s("banquet-beo", "BEO", "/banquet-event/beo", "banquet.beo.view"),
			s("banquet-schedule", "Event Schedule", "/banquet-event/schedule", "banquet.event.view"),
			s("banquet-checklist", "Event Checklist", "/banquet-event/checklist", "banquet.checklist.view"),
			s("banquet-billing", "Event Billing", "/banquet-event/billing", "banquet.billing.view"),
			s("banquet-master", "Banquet Master Data", "/banquet-event/master", "banquet.menu.view"),
			s("banquet-import", "Event Import", "/banquet-event/import", "banquet.event.import"),
			s("banquet-reports", "Event Reports", "/reports?module=banquet", "reporting.report.view"),
		),
		mod("crm", "CRM", "groups", "/crm/customers",
			s("customers", "Customers", "/crm/customers", "crm.customer.view"),
			s("customer-360", "Customer 360", "/crm/customer-360", "crm.customer_overview.view"),
			s("leads", "Leads", "/crm/leads", "crm.lead.view"),
			s("opportunities", "Opportunities", "/crm/opportunities", "crm.opportunity.view"),
			s("sales-pipeline", "Sales Pipeline", "/crm/pipeline", "crm.opportunity.view",
				s("sales-pipeline-board", "Sales Pipeline", "/crm/pipeline", "crm.opportunity.view"),
				s("sales-commission", "Sales Targets & Commission", "/crm/sales-commission", "crm.sales_target.view"),
				s("sales-settings", "Sales Settings", "/crm/sales-settings", "crm.pipeline.view"),
			),
			s("quotations", "Quotations", "/crm/quotations", "crm.quotation.view"),
			s("corporate-accounts", "Corporate Accounts", "/crm/corporate-accounts", "crm.corporate_account.view"),
			s("crm-engagement", "Feedback & Campaigns", "/crm/engagement", "crm.feedback.view"),
			s("follow-ups", "Follow-ups", "/crm/follow-ups", "crm.follow_up.view"),
			s("crm-reports", "CRM Reports", "/reports?module=crm", "reporting.report.view"),
		),
		mod("commercial", "Commercial", "storefront", "/commercial/pricing/rate-plans",
			s("pricing", "Pricing", "/commercial/pricing/rate-plans", "commercial.pricing.view",
				s("rate-plans", "Rate Plans", "/commercial/pricing/rate-plans", "commercial.pricing.view"),
				s("pricing-rules", "Pricing Rules", "/commercial/pricing/rules", "commercial.pricing.view"),
				s("effective-dates", "Effective Dates", "/commercial/pricing/effective-dates", "commercial.pricing.view"),
				s("commercial-tax-service", "Tax & Service", "/settings/tax-service", "commercial.tax_service.view"),
				s("discounts", "Discounts", "/commercial/pricing/discounts", "commercial.promotion.view"),
				s("promo-codes", "Promo Codes", "/commercial/pricing/promo-codes", "commercial.promo_code.view"),
			),
			s("promotions", "Promotions", "/commercial/promotions", "commercial.promotion.view"),
			s("packages", "Packages", "/commercial/packages", "commercial.package.view",
				s("package-list", "Packages", "/commercial/packages", "commercial.package.view"),
				s("package-bookings", "Package Bookings", "/commercial/package-bookings", "commercial.package_booking.view"),
			),
			s("vouchers", "Voucher & Prepaid", "/commercial/operations", "commercial.voucher.view"),
			s("commercial-master", "Outlets & Products", "/commercial/master", "commercial.outlet.view"),
		),
		mod("billing", "Billing & Payment", "payments", "/billing/folios",
			s("customer-accounts", "Customer Accounts", "/billing/customer-accounts", "billing.customer_account.view"),
			s("folios", "Folios", "/billing/folios", "billing.folio.view",
				s("all-folios", "Folios", "/billing/folios", "billing.folio.view"),
				s("customer-folios", "Customer Folios", "/billing/customer-folios", "billing.customer_folio.view"),
			),
			s("invoices", "Invoices", "/billing/invoices", "billing.invoice.view"),
			s("payments", "Payments", "/billing/payments", "billing.payment.view"),
			s("deposits", "Deposits", "/billing/deposits", "billing.payment.view"),
			s("refunds", "Refunds", "/billing/refunds", "billing.refund.view"),
			s("member-charges", "Member Charges", "/billing/member-charges", "billing.customer_account.view"),
			s("payment-schedules", "Payment Schedule", "/billing/payment-schedules", "billing.payment_schedule.view"),
			s("corporate-billing", "Corporate Billing", "/billing/corporate-billing", "billing.invoice.view"),
			s("payment-reconciliation", "Payment Reconciliation", "/billing/reconciliation", "billing.reconciliation.view"),
			s("cashier", "Cashier", "/billing/cashier", "billing.cashier_shift.view"),
			s("night-audit", "Night Audit", "/billing/night-audit", "billing.night_audit.view"),
			s("billing-reports", "Billing Reports", "/reports?module=billing-reports", "reporting.report.view"),
		),
		mod("inventory", "Inventory", "inventory_2", "/inventory/stock-balance",
			s("inventory-items", "Items", "/inventory/m/inventory.item", "inventory.item.view"),
			s("inventory-categories", "Categories", "/inventory/m/inventory.item_category", "inventory.item_category.view"),
			s("inventory-uom", "UOM", "/inventory/m/inventory.uom", "inventory.uom.view"),
			s("warehouses", "Warehouses", "/inventory/m/inventory.warehouse", "inventory.warehouse.view"),
			s("stock-locations", "Stock Locations", "/inventory/m/inventory.stock_location", "inventory.stock_location.view"),
			s("stock-balance", "Stock Balance", "/inventory/stock-balance", "inventory.stock_balance.view"),
			s("stock-movement", "Stock Movement", "/inventory/stock-movement", "inventory.stock_movement.view"),
			s("store-requisition", "Store Requisition", "/inventory/requisitions", "inventory.requisition.view"),
			s("stock-transfer", "Stock Transfer", "/inventory/transfers", "inventory.transfer.view"),
			s("stock-adjustment", "Stock Adjustment", "/inventory/adjustments", "inventory.adjustment.view"),
			s("stock-opname", "Stock Opname", "/inventory/stock-opname", "inventory.stock_opname.view"),
			s("stock-valuation", "Stock Valuation", "/inventory/valuation", "inventory.valuation.view"),
			s("par-stock", "Par Stock", "/inventory/m/inventory.par_stock", "inventory.par_stock.view"),
			s("reorder-point", "Reorder Point", "/inventory/replenishment", "inventory.reorder_point.view"),
			s("bom-recipes", "BOM & Recipes", "/inventory/m/inventory.recipe", "inventory.recipe.view"),
			s("production", "Production", "/inventory/production", "inventory.production.view"),
			s("waste", "Waste", "/inventory/waste", "inventory.waste.view"),
			s("consignment", "Consignment", "/inventory/consignment", "inventory.consignment.view"),
			s("assets-equipment", "Assets & Equipment", "/inventory/assets", "inventory.asset.view"),
			s("inventory-exceptions", "Posting Exceptions", "/inventory/posting-exceptions", "inventory.posting_exception.view"),
			s("inventory-configuration", "Inventory Configuration", "/settings/club-policies", "platform.club_policy.view"),
			s("inventory-reports", "Inventory Reports", "/inventory/reports", "reporting.report.view"),
		),
		m("procurement", "Procurement", "shopping_cart", "/procurement", "P4"),
		m("accounting", "Accounting", "account_balance", "/accounting", "P4"),
		m("hris", "HRIS", "badge", "/hris", "P5"),
		// Landing Page & CMS (PRD P4 EP-24, Naming Convention §26 CMS menu).
		mod("cms", "CMS", "web", "/cms/pages",
			s("cms-pages", "Pages", "/cms/pages", "cms.page.view"),
			s("cms-banners", "Banners", "/cms/banners", "cms.banner.view"),
			s("cms-images", "Images", "/cms/images", "cms.media.view"),
			s("cms-packages", "Packages", "/cms/data/packages", "cms.page.view"),
			s("cms-promotions", "Promotions", "/cms/data/promotions", "cms.page.view"),
			s("cms-pricing", "Pricing", "/cms/data/rates", "cms.page.view"),
			s("cms-events", "Events", "/cms/data/events", "cms.page.view"),
			s("cms-news", "News", "/cms/news", "cms.article.view"),
			s("cms-gallery", "Gallery", "/cms/gallery", "cms.gallery.view"),
			s("cms-course-guide", "Course Guide", "/cms/course-guide", "cms.course_guide.view"),
			s("cms-contact", "Contact Information", "/cms/contact", "cms.contact.view"),
			s("cms-languages", "Languages", "/cms/languages", "cms.language.view"),
			s("cms-site", "Navigation & Redirects", "/cms/navigation", "cms.menu.view"),
			s("cms-publishing", "Publishing", "/cms/publishing", "cms.page.view"),
			s("cms-reports", "CMS Reports", "/reports?module=cms", "reporting.report.view"),
		),
		{Key: "reports", Label: "Reports", Path: "/reports", Icon: "monitoring", Module: "reporting", Permission: "reporting.report.view", Children: []Item{
			s("reports-list", "Reports", "/reports", "reporting.report.view"),
			s("golf-reports", "Golf Reports", "/reports?module=golf", "reporting.report.view"),
			s("booking-reports", "Booking Reports", "/reports?module=booking", "reporting.report.view"),
			s("membership-reports", "Membership Reports", "/reports?module=membership", "reporting.report.view"),
			s("operational-reports", "Operational Reports", "/reports?module=billing", "reporting.report.view"),
			s("reports-exports", "Exports", "/reports/exports", "reporting.export.create"),
			s("management-dashboard", "Management Dashboard", "/management", catalog.ManagementView),
			s("kpi-dashboards", "KPI Dashboards", "/dashboards/golf-performance", catalog.ManagementView),
		}},
		{Key: "settings", Label: "Settings", Path: "/settings", Icon: "settings", Module: "platform", Children: []Item{
			s("organization", "Organization", "/settings/organization", "platform.organization.view",
				s("properties", "Properties", "/settings/organization/properties", "platform.property.view"),
				s("departments", "Departments", "/settings/organization/departments", "platform.department.view"),
				s("employees", "Employees", "/settings/organization/employees", "platform.employee.view"),
			),
			s("customer-instance", "Customer Instance", "/settings/customer-instance", "platform.instance.view"),
			s("venues", "Venues", "/settings/venues", "platform.venue.view"),
			{Key: "courses", Label: "Courses", Path: "/golf/course", Module: "golf", Permission: "golf.course.view"},
			s("users", "Users", "/settings/users", "platform.user.view"),
			s("roles", "Roles & Permissions", "/settings/roles", "platform.role.view"),
			s("features", "Features", "/settings/features", "platform.module.view"),
			s("feature-configuration", "Feature Configuration", "/settings/feature-configuration", "platform.feature_flag.view"),
			s("business-rules", "Business Rules", "/settings/business-rules", "platform.business_rule.view"),
			{Key: "pricing-rules-settings", Label: "Pricing Rules", Path: "/commercial/pricing/rules", Module: "commercial", Permission: "commercial.pricing.view"},
			s("club-policies", "Club Policies", "/settings/club-policies", "platform.club_policy.view"),
			s("notifications", "Notifications", "/settings/notifications", "platform.notification_template.view"),
			s("integrations", "Integrations", "/settings/integrations", "platform.integration.view"),
			{Key: "payment-methods", Label: "Payment Methods", Path: "/settings/payment-methods", Module: "billing", Permission: "billing.payment_method.view"},
			{Key: "tax-service", Label: "Tax & Service", Path: "/settings/tax-service", Module: "commercial", Permission: "commercial.tax_service.view"},
			s("approval-workflows", "Approval Workflows", "/settings/approval-workflows", "platform.approval_workflow.view"),
			{Key: "audit-logs", Label: "Audit Logs", Path: "/settings/audit-logs", Module: "audit", Permission: "audit.log.view"},
			s("localization", "Localization", "/settings/localization", "platform.localization.update"),
			s("branding", "Branding", "/settings/branding", "platform.branding.update"),
			s("system-settings", "System Settings", "/settings/system", "platform.system_settings.view",
				s("background-jobs", "Background Jobs", "/settings/system/background-jobs", "platform.job.view"),
				s("devices", "Devices", "/settings/system/devices", "platform.device.view"),
				s("master-data-import", "Master Data Import", "/settings/system/master-data-import", "platform.import.view"),
				s("api-keys", "API Keys", "/settings/system/api-keys", "platform.api_key.view"),
				s("bridge-agents", "Bridge Agents", "/settings/system/bridge-agents", "platform.bridge_agent.view"),
				s("integration-logs", "Integration Logs", "/settings/system/integration-logs", "platform.integration_log.view"),
				s("notification-history", "Notification History", "/settings/system/notification-history", "platform.notification_delivery.view"),
			),
		}},
	},
	"management": {
		{Key: "executive-overview", Label: "Executive Overview", Path: "/management", Icon: "insights", Module: "reporting", Permission: catalog.ManagementView},
		{Key: "golf-performance", Label: "Golf Performance", Path: "/management/golf", Module: "golf", Permission: catalog.ManagementView},
		{Key: "sport-club-performance", Label: "Sport Club Performance", Path: "/management/sport-club-performance", Module: "sportclub", Permission: catalog.ManagementView},
		{Key: "membership-performance", Label: "Membership Performance", Path: "/management/membership", Module: "membership", Permission: catalog.ManagementView},
		{Key: "booking-performance", Label: "Booking Performance", Path: "/management/booking", Module: "reservation", Permission: catalog.ManagementView},
		{Key: "commercial-performance", Label: "Commercial Performance", Path: "/management/commercial-performance", Module: "commercial", Permission: catalog.ManagementView},
		{Key: "crm-performance", Label: "CRM Performance", Path: "/management/crm-performance", Module: "crm", Permission: catalog.ManagementView},
		{Key: "inventory-performance", Label: "Inventory Performance", Path: "/management/inventory-performance", Module: "inventory", Permission: catalog.ManagementView},
		{Key: "banquet-performance", Label: "Banquet Performance", Path: "/management/banquet-performance", Module: "banquet", Permission: catalog.ManagementView},
		{Key: "financial-performance", Label: "Financial Performance", Path: "/management/financial", Module: "accounting", Permission: catalog.ManagementView, ComingSoon: true, Phase: "P4"},
	},
	"member": {
		{Key: "home", Label: "Home", Path: "/", Icon: "home", Permission: catalog.ShellMemberPortal},
		{Key: "offers", Label: "Offers", Path: "/offers", Icon: "local_offer", Module: "commercial", Permission: catalog.ShellMemberPortal},
		{Key: "golf", Label: "Golf", Path: "/golf", Icon: "golf_course", Module: "golf", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("book-golf", "Book Golf", "/golf", catalog.ShellMemberPortal),
			s("tee-time", "Tee Time", "/golf/tee-time", catalog.ShellMemberPortal),
			s("my-flights", "My Flights", "/golf/my-flights", catalog.ShellMemberPortal),
			s("my-caddy", "My Caddy", "/golf/my-caddy", catalog.ShellMemberPortal),
			s("my-golf-cart", "My Golf Cart", "/golf/my-golf-cart", catalog.ShellMemberPortal),
			s("my-scores", "Scores & Handicap", "/golf/scores", catalog.ShellMemberPortal),
		}},
		{Key: "sport-club", Label: "Sport Club", Path: "/sport-club", Icon: "sports_tennis", Module: "sportclub", Permission: catalog.ShellMemberPortal},
		{Key: "stay-venue", Label: "Stay & Venue", Path: "/stay", Icon: "hotel", Module: "stay", Permission: catalog.ShellMemberPortal},
		{Key: "packages", Label: "Packages", Path: "/packages", Icon: "card_travel", Module: "commercial", Permission: catalog.ShellMemberPortal},
		{Key: "events", Label: "Events", Path: "/events", Icon: "celebration", Module: "banquet", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("upcoming-events", "Upcoming Events", "/events", catalog.ShellMemberPortal),
			s("my-events", "My Events", "/events/my-events", catalog.ShellMemberPortal),
		}},
		{Key: "bookings", Label: "Bookings", Path: "/bookings", Icon: "event_available", Module: "reservation", Permission: catalog.ShellMemberPortal},
		{Key: "membership", Label: "Membership", Path: "/membership", Icon: "card_membership", Module: "membership", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("my-membership", "My Membership", "/membership", catalog.ShellMemberPortal),
			s("digital-member-card", "Digital Member Card", "/membership/card", catalog.ShellMemberPortal),
			s("membership-benefits", "Membership Benefits", "/membership/benefits", catalog.ShellMemberPortal),
			s("family-members", "Family Members", "/membership/family", catalog.ShellMemberPortal),
			s("membership-statement", "Membership Statement", "/membership/statements", catalog.ShellMemberPortal),
			s("membership-services", "Fees & Requests", "/membership/services", catalog.ShellMemberPortal),
		}},
		{Key: "vouchers", Label: "Voucher & Prepaid", Path: "/vouchers", Icon: "redeem", Module: "commercial", Permission: catalog.ShellMemberPortal},
		{Key: "order-food", Label: "Order Food", Path: "/order-food", Icon: "restaurant", Module: "commercial", Permission: catalog.ShellMemberPortal},
		{Key: "transactions", Label: "Transactions", Path: "/transactions", Icon: "receipt_long", Module: "billing", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("my-transactions", "My Transactions", "/transactions", catalog.ShellMemberPortal),
			s("member-payments", "Payments", "/transactions/payments", catalog.ShellMemberPortal),
			s("my-member-charges", "Member Charges", "/transactions/member-charges", catalog.ShellMemberPortal),
			s("my-invoices", "Invoices", "/transactions/invoices", catalog.ShellMemberPortal),
		}},
		{Key: "preferences", Label: "Preferences", Path: "/preferences", Icon: "tune", Module: "crm", Permission: catalog.ShellMemberPortal},
		{Key: "profile", Label: "Profile", Path: "/profile", Icon: "person", Permission: catalog.ShellMemberPortal},
	},
	"ops": {
		{Key: "home", Label: "Home", Path: "/ops", Icon: "home", Permission: catalog.ShellOps},
		{Key: "starter", Label: "Starter", Path: "/ops/starter", Icon: "flag", Module: "golf", Permission: "golf.starter.view", Children: []Item{
			s("ops-tee-sheet", "Tee Sheet", "/ops/starter/tee-sheet", "golf.tee_sheet.view"),
			s("queue", "Queue", "/ops/starter", "golf.starter.view"),
			s("ready-flights", "Ready Flights", "/ops/starter/ready", "golf.starter.view"),
			s("dispatch", "Dispatch", "/ops/starter/dispatch", "golf.starter.control"),
			s("starter-check-in", "Check-in", "/ops/check-in", "golf.check_in.perform"),
			s("tee-off", "Tee-Off", "/ops/starter/dispatch", "golf.starter.control"),
			s("round-status", "Round Status", "/ops/starter/rounds", "golf.starter.view"),
			s("pace-of-play", "Pace of Play", "/ops/starter/pace", "golf.pace.view"),
		}},
		{Key: "caddy-master", Label: "Caddy Master", Path: "/ops/caddy", Icon: "hiking", Module: "golf", Permission: "golf.caddy_assignment.manage", Children: []Item{
			s("caddy-queue", "Caddy Queue", "/ops/caddy", "golf.caddy.view"),
			s("caddy-availability", "Caddy Availability", "/ops/caddy/availability", "golf.caddy.view"),
			s("caddy-assignment", "Caddy Assignment", "/ops/caddy/assignment", "golf.caddy_assignment.manage"),
			s("caddy-rotation", "Caddy Rotation", "/ops/caddy/rotation", "golf.caddy_assignment.manage"),
			s("caddy-attendance", "Caddy Attendance", "/ops/caddy/availability", "golf.caddy_assignment.manage"),
			s("caddy-history", "Caddy History", "/ops/caddy/history", "golf.caddy.view"),
			s("caddy-incidents", "Incidents & Settlement", "/ops/caddy/incidents", "golf.caddy_incident.create"),
		}},
		{Key: "front-desk", Label: "Front Desk", Path: "/ops/front-desk", Icon: "concierge", Module: "golf", Permission: "golf.check_in.perform", Children: []Item{
			s("reservations", "Reservations", "/ops/front-desk", "golf.booking.view"),
			s("fd-check-in", "Check-in", "/ops/check-in", "golf.check_in.perform"),
			s("guest", "Guest", "/ops/front-desk/guest", "crm.guest.view"),
			s("fd-payments", "Payments", "/ops/front-desk/payments", "billing.payment.create"),
			s("fd-folios", "Folios", "/ops/front-desk/folios", "billing.folio.view"),
			s("fd-customer-folios", "Customer Folios", "/ops/front-desk/customer-folios", "billing.customer_folio.view"),
			s("fd-cashier", "Cashier", "/ops/front-desk/cashier", "billing.cashier_shift.operate"),
		}},
		{Key: "stay-front-desk", Label: "Stay Front Desk", Path: "/ops/stay-desk", Icon: "hotel", Module: "stay", Permission: "stay.stay.view"},
		{Key: "golf-staff", Label: "Golf Staff", Path: "/ops/golf-staff", Icon: "golf_course", Module: "golf", Permission: "golf.bag.manage", Children: []Item{
			s("bag-drop", "Bag Drop", "/ops/golf-staff", "golf.bag.manage"),
			s("bag-storage", "Bag Storage", "/ops/golf-staff/bag-storage", "golf.bag.manage"),
			s("locker-assignment", "Locker Assignment", "/ops/golf-staff/lockers", "golf.locker_assignment.manage"),
			s("golf-cart-readiness", "Golf Cart Readiness", "/ops/golf-staff/golf-carts", "golf.golf_cart.update"),
			s("golf-cart-assignment", "Golf Cart Assignment", "/ops/golf-staff/golf-cart-assignment", "golf.golf_cart_assignment.manage"),
			s("golf-cart-inspection", "Golf Cart Inspection", "/ops/golf-staff/inspection", "golf.cart_inspection.create"),
		}},
		{Key: "driving-range", Label: "Driving Range", Path: "/ops/driving-range", Icon: "sports_golf", Module: "golf", Permission: "golf.range.operate"},
		{Key: "sport-reception", Label: "Sport Reception", Path: "/ops/sport-reception", Icon: "sports_tennis", Module: "sportclub", Permission: "sportclub.access.validate"},
		{Key: "instructor", Label: "Instructor", Path: "/ops/instructor", Icon: "school", Module: "sportclub", Permission: "sportclub.class.attendance"},
		{Key: "pos", Label: "POS", Path: "/ops/pos", Icon: "point_of_sale", Module: "commercial", Permission: "commercial.order.create"},
		{Key: "package-use", Label: "Package Use", Path: "/ops/packages", Icon: "card_travel", Module: "commercial", Permission: "commercial.package_booking.consume"},
		{Key: "warehouse", Label: "Warehouse", Path: "/ops/warehouse", Icon: "warehouse", Module: "inventory", Permission: "inventory.stock_balance.view", Children: []Item{
			s("warehouse-stock", "Stock Balance", "/ops/warehouse", "inventory.stock_balance.view"),
			s("warehouse-issuing", "Issuing", "/ops/warehouse/issuing", "inventory.issue.create"),
			s("warehouse-transfer", "Transfer", "/ops/warehouse/transfer", "inventory.transfer.view"),
			s("warehouse-opname", "Stock Opname", "/ops/warehouse/opname", "inventory.stock_opname.count"),
			s("warehouse-requisition", "Store Requisition", "/ops/warehouse/requisitions", "inventory.requisition.view"),
		}},
		{Key: "outlet-store", Label: "Kitchen & Outlet Store", Path: "/ops/store", Icon: "kitchen", Module: "inventory", Permission: "inventory.requisition.create", Children: []Item{
			s("store-requisition-ops", "Store Requisition", "/ops/store", "inventory.requisition.create"),
			s("store-production", "Production", "/ops/store/production", "inventory.production.view"),
			s("store-waste", "Waste", "/ops/store/waste", "inventory.waste.create"),
			s("store-stock", "Outlet Stock", "/ops/store/stock", "inventory.stock_balance.view"),
		}},
		{Key: "spare-part-request", Label: "Spare Part Request", Path: "/ops/spare-parts", Icon: "build", Module: "inventory", Permission: "inventory.spare_part_request.create"},
		{Key: "event-operations", Label: "Event Operations", Path: "/ops/events", Icon: "celebration", Module: "banquet", Permission: "banquet.event.operate",
			Children: []Item{
				s("todays-events", "Today's Events", "/ops/events", "banquet.event.operate"),
				s("event-check-in", "Event Check-in", "/ops/events/check-in", "banquet.participant.check_in"),
			}},
		{Key: "banquet-production", Label: "Banquet Production", Path: "/ops/banquet-production", Icon: "skillet", Module: "banquet", Permission: "banquet.production.view"},
		{Key: "sync", Label: "Sync Queue", Path: "/ops/sync", Icon: "sync", Permission: catalog.ShellOps},
		{Key: "notifications", Label: "Notifications", Path: "/ops/notifications", Icon: "notifications", Permission: catalog.ShellOps},
	},
	"caddy": {
		{Key: "home", Label: "My Assignments", Path: "/tablet", Icon: "assignment", Permission: catalog.ShellCaddy},
		{Key: "earnings", Label: "Earnings", Path: "/tablet/earnings", Icon: "payments", Permission: catalog.ShellCaddy},
		{Key: "sync", Label: "Sync Queue", Path: "/tablet/sync", Icon: "sync", Permission: catalog.ShellCaddy},
		{Key: "profile", Label: "Profile", Path: "/tablet/profile", Icon: "person", Permission: catalog.ShellCaddy},
	},
	"platform-admin": {
		s("customer-instances", "Customer Instances", "/platform", catalog.ShellPlatformAdmin),
		s("instance-configuration", "Instance Configuration", "/platform/instance-configuration", "platform.instance.update"),
		s("enabled-modules", "Enabled Modules", "/platform/enabled-modules", "platform.module.update"),
		s("feature-configuration", "Feature Configuration", "/platform/feature-configuration", "platform.feature_flag.update"),
		s("branding", "Branding", "/platform/branding", "platform.branding.update"),
		s("custom-domain", "Custom Domain", "/platform/custom-domain", "platform.domain.manage"),
		s("users", "Users", "/platform/users", "platform.user.view"),
		s("roles", "Roles", "/platform/roles", "platform.role.view"),
		s("permissions", "Permissions", "/platform/permissions", "platform.permission.view"),
		s("integrations", "Integrations", "/platform/integrations", "platform.integration.manage"),
		s("feature-flags", "Feature Flags", "/platform/feature-flags", "platform.feature_flag.update"),
		s("locale", "Locale", "/platform/locale", "platform.instance.update"),
		s("currency", "Currency", "/platform/currency", "platform.instance.update"),
		s("timezone", "Timezone", "/platform/timezone", "platform.instance.update"),
	},
}

// ModuleChecker reports enabled modules.
type ModuleChecker interface {
	ModuleEnabled(ctx context.Context, module string) (bool, error)
}

// Service serves navigation.
type Service struct {
	DB      *dbtx.DB
	Modules ModuleChecker
}

// Build filters a shell's tree for the principal at the active property.
func (sv *Service) Build(ctx context.Context, shell string) (Menu, error) {
	tree, ok := Trees[shell]
	if !ok {
		return Menu{}, errs.BadRequest("invalid_shell", "unknown shell")
	}
	p := authz.From(ctx)
	var at *uuid.UUID
	if pid, ok := reqctx.Property(ctx); ok {
		at = &pid
	}
	enabled := map[string]bool{"": true}
	var err error
	var filter func(items []Item) []Item
	filter = func(items []Item) []Item {
		out := []Item{}
		for _, it := range items {
			if it.Module != "" {
				on, ok := enabled[it.Module]
				if !ok {
					on, err = sv.Modules.ModuleEnabled(ctx, it.Module)
					if err != nil {
						return nil
					}
					enabled[it.Module] = on
				}
				if !on {
					continue
				}
			}
			if it.Permission != "" && !p.Can(it.Permission, at) {
				continue
			}
			if len(it.Children) > 0 {
				it.Children = filter(it.Children)
				if len(it.Children) == 0 && it.Permission == "" {
					continue // container with nothing visible
				}
			}
			out = append(out, it)
		}
		return out
	}
	items := filter(tree)
	return Menu{Shell: shell, Items: items}, err
}

func (sv *Service) handle(w http.ResponseWriter, r *http.Request) {
	shell := r.URL.Query().Get("shell")
	if shell == "" {
		shell = "backoffice"
	}
	menu, err := sv.Build(r.Context(), shell)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, menu)
}

// Register adds GET /api/v1/platform/navigation.
func (sv *Service) Register(reg *route.Registry) {
	reg.Add(route.Route{Method: http.MethodGet, Path: "/api/v1/platform/navigation", Module: "platform", Tag: "Application Shell",
		Summary: "Menu of a shell filtered by enabled modules and my permissions", Response: Menu{},
		Query: []route.Param{{Name: "shell", Enum: []string{"backoffice", "management", "member", "ops", "platform-admin", "caddy"}}}, Handler: sv.handle})
}
