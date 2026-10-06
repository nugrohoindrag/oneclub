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
	Section    bool   `json:"section,omitempty" doc:"Heading of a group of items (role menus); not a link"`
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

func m(module, label, icon, path, phase string) Item { //nolint:unused // Coming Soon modules of later phases
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
			// PRD P3 EP-16 Tournament Management (§7.1)
			s("tournaments", "Tournaments", "/golf/tournaments", "golf.tournament.view",
				s("tournament-schedule", "Tournament Schedule", "/golf/tournaments", "golf.tournament.view"),
				s("tournament-registration", "Registration", "/golf/tournament-section/registration", "golf.tournament_registration.manage"),
				s("tournament-participants", "Participants", "/golf/tournament-section/participants", "golf.tournament_registration.view"),
				s("tournament-flighting", "Flighting", "/golf/tournament-section/flighting", "golf.tournament.view"),
				s("tournament-tee-assignment", "Tee Assignment", "/golf/tournament-section/tee-assignment", "golf.tournament.view"),
				s("tournament-scoring", "Scoring", "/golf/tournament-section/scoring", "golf.tournament_score.view"),
				s("tournament-leaderboard", "Leaderboard", "/golf/tournament-section/leaderboard", "golf.tournament_leaderboard.view"),
				s("tournament-packages", "Tournament Packages", "/golf/tournament-section/packages", "golf.tournament.view"),
				s("tournament-fees", "Tournament Fees", "/golf/tournament-section/fees", "golf.tournament.view"),
				s("tournament-sponsors", "Sponsors", "/golf/tournament-section/sponsors", "golf.tournament_sponsor.view"),
				s("tournament-prizes", "Prizes", "/golf/tournament-section/prizes", "golf.tournament_prize.view"),
				s("tournament-reports", "Tournament Reports", "/reports/golf.tournament", "reporting.golf_tournament.view"),
				// PRD P5 EP-23 (§7.1, §7.6): Series (Order of Merit), Team Formats, Tournament History
				s("tournament-series", "Series", "/golf/tournament-series", "golf.tournament_series.view"),
				s("team-formats", "Team Formats", "/golf/team-formats", "golf.team_format.view"),
				s("tournament-history", "Tournament History", "/golf/tournament-history", "golf.tournament_history.view"),
			),
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
			s("campaigns", "Campaigns", "/crm/campaigns", "crm.campaign.view"),
			s("journeys", "Journeys", "/crm/journeys", "crm.journey.view"), // PRD P5 §7.1 / §7.6
			s("vip-customers", "VIP Customers", "/crm/vip", "crm.vip.view"),
			s("loyalty", "Loyalty", "/crm/loyalty", "crm.loyalty_account.view",
				s("loyalty-accounts", "Loyalty Accounts", "/crm/loyalty", "crm.loyalty_account.view"),
				s("loyalty-tiers", "Tiers", "/crm/loyalty-tiers", "crm.loyalty_tier.view"),
				s("loyalty-rewards", "Rewards", "/crm/loyalty-rewards", "crm.loyalty_reward.view"),
				s("loyalty-eligibility", "Eligibility", "/crm/loyalty-eligibility", "crm.loyalty_reward_rule.view"),
			),
			s("top-spender", "Top Spender", "/crm/top-spender", "crm.top_spender.view"),
			s("feedback", "Feedback", "/crm/feedback", "crm.feedback.view"),
			s("complaints", "Complaints", "/crm/complaints", "crm.ticket.view"),
			s("follow-ups", "Follow-ups", "/crm/follow-ups", "crm.follow_up.view"),
			s("crm-analytics", "CRM Analytics", "/crm/analytics", "crm.analytics.view"), // PRD P5 EP-20
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
				s("discount-limits", "Discount Limits", "/commercial/pricing/discount-limits", "commercial.pricing.view"), // PO decision 4b
			),
			s("promotions", "Promotions", "/commercial/promotions", "commercial.promotion.view"),
			s("packages", "Packages", "/commercial/packages", "commercial.package.view",
				s("package-list", "Packages", "/commercial/packages", "commercial.package.view"),
				s("package-bookings", "Package Bookings", "/commercial/package-bookings", "commercial.package_booking.view"),
				// PRD P5 EP-22 (§7.1): Profitability, Capacity
				s("package-profitability", "Profitability", "/commercial/package-profitability", "commercial.package_profitability.view"),
				s("package-capacity", "Capacity", "/commercial/package-capacity", "commercial.package_capacity.view"),
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
		// Procurement (PRD P4 EP-10–15, §7.1, Naming Convention §20).
		mod("procurement", "Procurement", "shopping_cart", "/procurement/requisitions",
			s("procurement-suppliers", "Suppliers", "/procurement/suppliers", "procurement.supplier.view"),
			s("purchase-requisitions", "Purchase Requisitions", "/procurement/requisitions", "procurement.requisition.view"),
			s("procurement-approvals", "Approvals", "/procurement/approvals", "procurement.requisition.view"),
			s("procurement-rfq", "RFQ", "/procurement/rfqs", "procurement.rfq.view"),
			s("vendor-quotations", "Vendor Quotations", "/procurement/vendor-quotations", "procurement.vendor_quotation.view"),
			s("purchase-orders", "Purchase Orders", "/procurement/purchase-orders", "procurement.purchase_order.view"),
			s("goods-receipts", "Goods Receipts", "/procurement/goods-receipts", "procurement.goods_receipt.view"),
			s("purchase-returns", "Purchase Returns", "/procurement/purchase-returns", "procurement.purchase_return.view"),
			s("vendor-invoices", "Vendor Invoices", "/procurement/vendor-invoices", "procurement.vendor_invoice.view"),
			s("vendor-performance", "Vendor Performance", "/procurement/vendor-performance", "procurement.vendor_performance.view"),
			s("procurement-migration", "Procurement Migration", "/procurement/migration", "procurement.migration.import"),
			s("procurement-configuration", "Procurement Configuration", "/settings/club-policies", "platform.club_policy.view"),
			s("procurement-reports", "Procurement Reports", "/procurement/reports", "reporting.report.view"),
		),
		mod("accounting", "Accounting", "account_balance", "/accounting/general-ledger",
			s("finance-dashboard", "Finance Dashboard", "/accounting/dashboard", "accounting.dashboard.view"),
			s("general-ledger", "General Ledger", "/accounting/general-ledger", "accounting.journal.view"),
			s("accounts-receivable", "Accounts Receivable", "/accounting/receivables", "accounting.receivable.view"),
			s("accounts-payable", "Accounts Payable", "/accounting/payables", "accounting.payable.view"),
			s("cash-bank", "Cash & Bank", "/accounting/cash-bank", "accounting.bank_transaction.view"),
			s("revenue-billing", "Revenue & Billing", "/accounting/revenue", "billing.invoice.view"),
			s("revenue-recognition", "Revenue Recognition", "/accounting/revenue-recognition", "accounting.revenue.view"),
			s("tax", "Tax", "/accounting/tax", "accounting.tax_invoice.view"),
			s("financial-periods", "Financial Periods", "/accounting/periods", "accounting.period.view"),
			s("closing", "Closing", "/accounting/closing", "accounting.posting.view"),
			s("financial-reports", "Financial Reports", "/accounting/reports", "accounting.report.view"),
			s("accounting-transition", "Accounting Transition", "/accounting/setup", "accounting.setup.view"),
		),
		// HRIS (PRD P5 §7.1, Naming Convention §22), grouped as an HR workspace
		// (HRIS improvement specification §3): the HR Dashboard first, then
		// People, Time & Attendance, Payroll, Performance and Reports; a group
		// without a visible item is hidden.
		mod("hris", "HRIS", "badge", "/hris/dashboard",
			s("hris-dashboard", "HR Dashboard", "/hris/dashboard", "hris.employee.view"),
			section("hris-people", "People",
				s("hris-employees", "Employees", "/hris/employees", "hris.employee.view"),
				s("hris-organization", "Organization", "/hris/organization", "hris.org_unit.view"),
				s("hris-recruitment", "Recruitment", "/hris/recruitment", "hris.job_requisition.view"),      // PRD P5 EP-03
				s("hris-training", "Training & Certification", "/hris/training", "hris.certification.view"), // PRD P5 EP-04
			),
			section("hris-time", "Time & Attendance",
				s("hris-attendance", "Attendance", "/hris/attendance", "hris.attendance.view"),           // PRD P5 EP-07
				s("hris-schedules", "Shift & Roster", "/hris/schedules", "hris.schedule.view"),           // PRD P5 EP-06
				s("hris-leave", "Leave & Permission", "/hris/leave", "hris.leave_request.view"),          // PRD P5 EP-08
				s("hris-overtime", "Overtime", "/hris/overtime", "hris.overtime_request.view"),           // PRD P5 EP-08
				s("hris-workforce", "Workforce Planning", "/hris/workforce", "hris.workforce_plan.view"), // HRIS phase B (spec §8)
				s("hris-timesheets", "Timesheets", "/hris/timesheets", "hris.timesheet.view"),            // HRIS phase C (spec §16)
				// PRD P5 FR-ATT-08 (partner caddies / instructors on the attendance devices).
				s("hris-partner-attendance", "Partner Clock-in", "/hris/partner-attendance", "hris.partner_attendance.view"),
			),
			section("hris-pay", "Payroll",
				s("hris-payroll", "Payroll", "/hris/payroll", "hris.payroll_run.view"),                         // PRD P5 EP-09/10/15
				s("hris-benefits", "Tax & BPJS", "/hris/benefits", "hris.payroll_profile.view"),                // PRD P5 EP-10 (PTKP, BPJS)
				s("hris-benefit-plans", "Benefits", "/hris/benefit-plans", "hris.benefit_enrollment.view"),     // HRIS phase C (spec §24)
				s("hris-reimbursements", "Reimbursement", "/hris/reimbursements", "hris.reimbursement.view"),   // HRIS phase C (spec §24)
				s("hris-service-charge", "Service Charge", "/hris/service-charge", "hris.service_charge.view"), // PRD P5 EP-11
				s("hris-commissions", "Commissions", "/hris/commissions", "hris.commission_payout.view"),       // PRD P5 EP-12
				s("hris-caddy", "Caddy", "/hris/caddy", "hris.payout_run.view"),                                // PRD P5 EP-13
				s("hris-instructors", "Instructors", "/hris/instructors", "hris.payout_run.view"),              // PRD P5 EP-14
			),
			section("hris-talent", "Performance",
				// PRD P5 EP-05 (performance review cycles, calibration).
				s("hris-performance", "Performance Review", "/hris/performance", "hris.review_cycle.view"),
			),
			section("hris-insight", "Reports & Migration",
				s("hris-reports", "HR Reports", "/reports?module=hris", "reporting.report.view"), // PRD P5 EP-27 (BI registry)
				// PRD P5 EP-28 (migration wave 5).
				s("hris-migration", "HR Migration", "/hris/import", "hris.import.create"),
				s("hris-migration-reconciliation", "Migration Reconciliation", "/hris/migration-reconciliation", "hris.migration_reconciliation.view"),
			),
		),
		// Employee Self Service for office staff (PRD P5 EP-16; the ops shell
		// has the same area at /ops/ess).
		{Key: "self-service", Label: "Employee Self Service", Path: "/ess", Icon: "person_pin", Module: "hris", Permission: "hris.ess.use"},
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
			{Key: "financial-reports-list", Label: "Financial Reports", Path: "/reports?module=accounting", Module: "accounting", Permission: "reporting.report.view"},
			s("reports-exports", "Exports", "/reports/exports", "reporting.export.create"),
			s("management-dashboard", "Management Dashboard", "/management", catalog.ManagementView),
			s("kpi-dashboards", "KPI Dashboards", "/dashboards/golf-performance", catalog.ManagementView),
			// PRD P5 EP-21 / EP-27: HR Reports, scheduled reports, report builder.
			s("hr-reports-list", "HR Reports", "/reports?module=hris", "reporting.report.view"),
			s("scheduled-reports", "Scheduled Reports", "/reports/scheduled", "reporting.scheduled_report.view"),
			s("report-builder", "Report Builder", "/reports/builder", "reporting.dataset.view"),
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
			{Key: "discount-limits-settings", Label: "Discount Limits", Path: "/commercial/pricing/discount-limits", Module: "commercial",
				Permission: "commercial.pricing.view"}, // PO decision 4b
			s("club-policies", "Club Policies", "/settings/club-policies", "platform.club_policy.view"),
			s("notifications", "Notifications", "/settings/notifications", "platform.notification_template.view"),
			s("integrations", "Integrations", "/settings/integrations", "platform.integration.view"),
			{Key: "payment-methods", Label: "Payment Methods", Path: "/settings/payment-methods", Module: "billing", Permission: "billing.payment_method.view"},
			{Key: "tax-service", Label: "Tax & Service", Path: "/settings/tax-service", Module: "commercial", Permission: "commercial.tax_service.view"},
			{Key: "accounting-configuration", Label: "Accounting Configuration", Path: "/accounting/setup", Module: "accounting", Permission: "accounting.setup.view"},
			// PRD P5 §7.1 / §7.6: HR Configuration, Payroll Configuration and Attendance Configuration; HR Policies in Club Policies.
			{Key: "hr-configuration", Label: "HR Configuration", Path: "/settings/club-policies?category=HR%20Configuration", Module: "hris",
				Permission: "platform.club_policy.view"},
			{Key: "payroll-configuration", Label: "Payroll Configuration", Path: "/settings/club-policies?category=Payroll%20Configuration", Module: "hris",
				Permission: "platform.club_policy.view"},
			{Key: "attendance-configuration", Label: "Attendance Configuration", Path: "/settings/club-policies?category=Attendance%20Configuration",
				Module: "hris", Permission: "platform.club_policy.view"},
			{Key: "hr-policies", Label: "HR Policies", Path: "/settings/club-policies?category=HR%20Policies", Module: "hris", Permission: "platform.club_policy.view"},
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
		{Key: "golf-performance", Label: "Golf Performance", Path: "/management/golf", Icon: "golf_course", Module: "golf", Permission: catalog.ManagementView},
		{Key: "sport-club-performance", Label: "Sport Club Performance", Path: "/management/sport-club-performance", Icon: "sports_tennis", Module: "sportclub", Permission: catalog.ManagementView},
		{Key: "membership-performance", Label: "Membership Performance", Path: "/management/membership", Icon: "card_membership", Module: "membership", Permission: catalog.ManagementView},
		{Key: "booking-performance", Label: "Booking Performance", Path: "/management/booking", Icon: "event_available", Module: "reservation", Permission: catalog.ManagementView},
		{Key: "commercial-performance", Label: "Commercial Performance", Path: "/management/commercial-performance", Icon: "local_offer", Module: "commercial", Permission: catalog.ManagementView},
		{Key: "crm-performance", Label: "CRM Performance", Path: "/management/crm-performance", Icon: "support_agent", Module: "crm", Permission: catalog.ManagementView},
		{Key: "inventory-performance", Label: "Inventory Performance", Path: "/management/inventory-performance", Icon: "warehouse", Module: "inventory", Permission: catalog.ManagementView},
		{Key: "banquet-performance", Label: "Banquet Performance", Path: "/management/banquet-performance", Icon: "celebration", Module: "banquet", Permission: catalog.ManagementView},
		{Key: "procurement-performance", Label: "Procurement Performance", Path: "/management/procurement-performance", Icon: "request_quote", Module: "procurement", Permission: catalog.ManagementView},
		{Key: "financial-performance", Label: "Financial Performance", Path: "/management/financial", Icon: "payments", Module: "accounting", Permission: catalog.ManagementView},
		// PRD P5 EP-21 / EP-27: HR Performance and the KPI targets of the Executive Overview.
		{Key: "hr-performance", Label: "HR Performance", Path: "/management/hr-performance", Icon: "badge", Module: "reporting", Permission: "reporting.hr_performance.view"},
		{Key: "kpi-targets", Label: "KPI Targets", Path: "/management/targets", Icon: "monitoring", Module: "reporting", Permission: "reporting.kpi_target.view"},
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
			// PRD P3 §7.4: Tournaments — Register, My Tournaments, Leaderboard
			s("tournaments", "Tournaments", "/golf/tournaments", catalog.ShellMemberPortal),
			s("my-tournaments", "My Tournaments", "/golf/my-tournaments", catalog.ShellMemberPortal),
			s("tournament-leaderboard", "Leaderboard", "/golf/tournaments/leaderboard", catalog.ShellMemberPortal),
			// PRD P5 §7.4: series standing (Order of Merit) and tournament history
			s("order-of-merit", "Order of Merit", "/golf/order-of-merit", catalog.ShellMemberPortal),
			s("tournament-history", "Tournament History", "/golf/tournament-history", catalog.ShellMemberPortal),
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
		{Key: "loyalty", Label: "Loyalty", Path: "/loyalty", Icon: "loyalty", Module: "crm", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("my-points", "My Points", "/loyalty", catalog.ShellMemberPortal),
			s("loyalty-tier", "Tier", "/loyalty/tier", catalog.ShellMemberPortal),
			s("loyalty-rewards", "Rewards", "/loyalty/rewards", catalog.ShellMemberPortal),
			s("points-history", "Points History", "/loyalty/history", catalog.ShellMemberPortal),
			s("loyalty-progress", "Tier Progress", "/loyalty/progress", catalog.ShellMemberPortal), // PRD P5 §7.4
			s("loyalty-offers", "Personal Offers", "/loyalty/offers", catalog.ShellMemberPortal),   // PRD P5 §7.4 (journey offers)
		}},
		{Key: "preferences", Label: "Preferences", Path: "/preferences", Icon: "tune", Module: "crm", Permission: catalog.ShellMemberPortal},
		{Key: "profile", Label: "Profile", Path: "/profile", Icon: "person", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("my-profile", "Profile", "/profile", catalog.ShellMemberPortal),
			s("communication-preferences", "Communication Preferences", "/profile/communication-preferences", catalog.ShellMemberPortal),
		}},
		{Key: "support", Label: "Support", Path: "/support", Icon: "support_agent", Module: "crm", Permission: catalog.ShellMemberPortal, Children: []Item{
			s("support-feedback", "Feedback", "/support/feedback", catalog.ShellMemberPortal),
			s("support-complaints", "Complaints", "/support/complaints", catalog.ShellMemberPortal),
		}},
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
			s("shotgun-start", "Shotgun Start", "/ops/tournament-desk/start", "golf.tournament.start"), // PRD P3 §7.6
		}},
		// PRD P3 FR-OPS-P3-02 Tournament Desk
		{Key: "tournament-desk", Label: "Tournament Desk", Path: "/ops/tournament-desk", Icon: "emoji_events", Module: "golf",
			Permission: "golf.tournament_registration.check_in", Children: []Item{
				s("tournament-check-in", "Registration Check-in", "/ops/tournament-desk", "golf.tournament_registration.check_in"),
				s("tournament-draw", "Draw", "/ops/tournament-desk/draw", "golf.tournament.view"),
				s("tournament-desk-scoring", "Scoring", "/ops/tournament-desk/scoring", "golf.tournament_score.enter"),
				s("tournament-desk-leaderboard", "Leaderboard", "/ops/tournament-desk/leaderboard", "golf.tournament_leaderboard.view"),
				s("tournament-desk-team-scoring", "Team Scoring", "/ops/tournament-desk/team-scoring", "golf.tournament_score.enter"), // PRD P5 EP-23
			}},
		{Key: "caddy-master", Label: "Caddy Master", Path: "/ops/caddy", Icon: "hiking", Module: "golf", Permission: "golf.caddy_assignment.manage", Children: []Item{
			s("caddy-queue", "Caddy Queue", "/ops/caddy", "golf.caddy.view"),
			s("caddy-availability", "Caddy Availability", "/ops/caddy/availability", "golf.caddy.view"),
			s("caddy-assignment", "Caddy Assignment", "/ops/caddy/assignment", "golf.caddy_assignment.manage"),
			s("caddy-rotation", "Caddy Rotation", "/ops/caddy/rotation", "golf.caddy_assignment.manage"),
			s("caddy-attendance", "Caddy Attendance", "/ops/caddy/availability", "golf.caddy_assignment.manage"),
			s("caddy-history", "Caddy History", "/ops/caddy/history", "golf.caddy.view"),
			s("caddy-incidents", "Incidents & Settlement", "/ops/caddy/incidents", "golf.caddy_incident.create"),
			// PRD P5 FR-ATT-08: caddies clocking in on the caddy house device join the queue.
			{Key: "caddy-device-clock-ins", Label: "Device Clock-ins", Path: "/ops/caddy/clock-ins", Module: "hris", Permission: "hris.partner_attendance.view"},
		}},
		{Key: "front-desk", Label: "Front Desk", Path: "/ops/front-desk", Icon: "concierge", Module: "golf", Permission: "golf.check_in.perform", Children: []Item{
			s("reservations", "Reservations", "/ops/front-desk", "golf.booking.view"),
			s("fd-check-in", "Check-in", "/ops/check-in", "golf.check_in.perform"),
			s("guest", "Guest", "/ops/front-desk/guest", "crm.guest.view"),
			s("fd-payments", "Payments", "/ops/front-desk/payments", "billing.payment.create"),
			s("fd-folios", "Folios", "/ops/front-desk/folios", "billing.folio.view"),
			s("fd-customer-folios", "Customer Folios", "/ops/front-desk/customer-folios", "billing.customer_folio.view"),
			s("fd-cashier", "Cashier", "/ops/front-desk/cashier", "billing.cashier_shift.operate"),
			s("fd-redeem-points", "Redeem Points", "/ops/front-desk/redeem-points", "crm.loyalty_account.redeem"),
			s("fd-vip-lookup", "VIP Lookup", "/ops/front-desk/vip", "crm.vip.view"), // PRD P5 FR-SEG-03
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
		// PRD P5 §7.2, FR-INS-HR-04: Honor Statement of partner instructors (payout runs).
		{Key: "honor-statement", Label: "Honor Statement", Path: "/ops/instructor/honor", Icon: "request_quote", Module: "hris", Permission: "hris.payout.own"},
		{Key: "pos", Label: "POS", Path: "/ops/pos", Icon: "point_of_sale", Module: "commercial", Permission: "commercial.order.create"},
		{Key: "package-use", Label: "Package Use", Path: "/ops/packages", Icon: "card_travel", Module: "commercial", Permission: "commercial.package_booking.consume"},
		{Key: "warehouse", Label: "Warehouse", Path: "/ops/warehouse", Icon: "warehouse", Module: "inventory", Permission: "inventory.stock_balance.view", Children: []Item{
			s("warehouse-stock", "Stock Balance", "/ops/warehouse", "inventory.stock_balance.view"),
			{Key: "warehouse-goods-receipt", Label: "Goods Receipt", Path: "/ops/warehouse/goods-receipt", Module: "procurement", Permission: "procurement.goods_receipt.create"},
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
		// Employee Self Service (PRD P5 §7.2, EP-16, §16 #6): personal login of employees; the time and payroll areas switch their
		// sections on (comingSoon: false).
		{Key: "ess", Label: "Employee Self Service", Path: "/ops/ess", Icon: "badge", Module: "hris", Permission: "hris.ess.use", Children: []Item{
			s("ess-profile", "Profile", "/ops/ess/profile", "hris.ess.use"),
			s("ess-schedule", "My Schedule", "/ops/ess/schedule", "hris.ess.use"),
			s("ess-open-shifts", "Open Shifts", "/ops/ess/open-shifts", "hris.ess.use"), // HRIS phase C
			s("ess-timesheets", "Timesheet", "/ops/ess/timesheets", "hris.ess.use"),     // HRIS phase C
			s("ess-clock", "Clock In / Out", "/ops/ess/clock", "hris.ess.use"),
			s("ess-attendance", "Attendance History", "/ops/ess/attendance", "hris.ess.use"),
			s("ess-leave", "Leave & Permission", "/ops/ess/leave", "hris.ess.use"),
			s("ess-overtime", "Overtime", "/ops/ess/overtime", "hris.ess.use"),
			s("ess-payslip", "Payslip", "/ops/ess/payslip", "hris.ess.use"),
			s("ess-loans", "Loans & Advances", "/ops/ess/loans", "hris.ess.use"),                 // HRIS phase B (spec §29)
			s("ess-reimbursements", "Reimbursement", "/ops/ess/reimbursements", "hris.ess.use"),  // HRIS phase C
			s("ess-benefits", "My Benefits", "/ops/ess/benefits", "hris.ess.use"),                // HRIS phase C
			s("ess-service-charge", "Service Charge", "/ops/ess/service-charge", "hris.ess.use"), // PRD P5 EP-11
			s("ess-commissions", "Commission & Bonus", "/ops/ess/commissions", "hris.ess.use"),   // PRD P5 EP-12
			s("ess-documents", "My Documents", "/ops/ess/documents", "hris.ess.use"),
			s("ess-training", "My Training", "/ops/ess/training", "hris.ess.use"),
			s("ess-team", "Team", "/ops/ess/team", "hris.team.view"),
			s("ess-approvals", "Approvals", "/ops/ess/approvals", "hris.team.approve"),
			s("ess-team-schedule", "Team Schedule", "/ops/ess/team-schedule", "hris.team.view"),
			s("ess-team-attendance", "Team Attendance", "/ops/ess/team-attendance", "hris.team.view"),
		}},
		// Attendance Kiosk (PRD P5 §7.2, §7.6, FR-ATT-02): clock-in with QR / PIN on a registered device.
		{Key: "attendance-kiosk", Label: "Attendance Kiosk", Path: "/ops/attendance-kiosk", Icon: "qr_code_scanner", Module: "hris",
			Permission: "hris.attendance.kiosk"},
		{Key: "sync", Label: "Sync Queue", Path: "/ops/sync", Icon: "sync", Permission: catalog.ShellOps},
		{Key: "notifications", Label: "Notifications", Path: "/ops/notifications", Icon: "notifications", Permission: catalog.ShellOps},
	},
	"caddy": {
		{Key: "home", Label: "My Assignments", Path: "/tablet", Icon: "assignment", Permission: catalog.ShellCaddy},
		{Key: "earnings", Label: "Earnings", Path: "/tablet/earnings", Icon: "payments", Permission: catalog.ShellCaddy},
		// PRD P5 §7.3, FR-OPS-P5-03: Payout History & Statement (caddy payout runs).
		{Key: "payout-history", Label: "Payout History", Path: "/tablet/payouts", Icon: "account_balance_wallet", Module: "hris", Permission: "hris.payout.own"},
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

// RoleTrees replace a shell's menu for a role template whose work sits in
// one domain: a user whose only role is listed sees this tree instead of the
// module menus. The permissions are unchanged, so the items still filter by
// permission and links into another module (payroll run, vendor invoice)
// still open.
var RoleTrees = map[string]map[string][]Item{
	"accountant": {"backoffice": accountantTree},
}

// soon is a menu of the role tree whose screen does not exist yet.
func soon(key, label, icon string) Item {
	return Item{Key: key, Label: label, Path: "/soon/" + key, Icon: icon, ComingSoon: true, Phase: "Soon"}
}

// si is a role-tree item with an icon.
func si(key, label, icon, path, perm string) Item {
	return Item{Key: key, Label: label, Path: path, Icon: icon, Permission: perm}
}

// group is a role-tree item with sub-items (opens its first one).
func group(key, label, icon string, items ...Item) Item {
	path := ""
	if len(items) > 0 {
		path = items[0].Path
	}
	return Item{Key: key, Label: label, Path: path, Icon: icon, Children: items}
}

// section groups role-tree items under a heading (always expanded).
func section(key, label string, items ...Item) Item {
	return Item{Key: key, Label: label, Section: true, Children: items}
}

// inModule ties a role-tree item to a module (hidden while it is disabled).
func inModule(module string, it Item) Item {
	it.Module = module
	return it
}

// accountantTree is the Back Office of the Accountant: finance only.
var accountantTree = []Item{
	si("dashboard", "Dashboard", "space_dashboard", "/", "accounting.dashboard.view"),
	section("transactions", "Transactions",
		// What must be billed and what was billed (Revenue & Billing), who owes
		// us (AR), whom we pay (AP), what money we hold (Cash & Bank).
		group("revenue-billing", "Revenue & Billing", "point_of_sale",
			inModule("billing", si("billing-queue", "Billing", "pending_actions", "/accounting/revenue?tab=billing", "billing.folio.view")),
			inModule("billing", si("invoices", "Invoices", "receipt_long", "/accounting/revenue?tab=invoices", "billing.invoice.view")),
			inModule("billing", si("credit-notes", "Credit / Debit Notes", "receipt", "/accounting/revenue?tab=notes", "billing.invoice.view")),
			inModule("billing", si("revenue-adjustments", "Revenue Adjustments", "swap_horiz", "/accounting/revenue?tab=adjustments", "billing.invoice.view")),
			si("revenue-reconciliation", "Revenue Reconciliation", "balance", "/accounting/revenue?tab=reconciliation", "accounting.posting.view"),
		),
		group("accounts-receivable", "Accounts Receivable", "request_quote",
			si("ar-receivables", "Receivables", "request_quote", "/accounting/receivables?tab=receivables", "accounting.receivable.view"),
			inModule("billing", si("ar-payments", "Payments", "payments", "/accounting/receivables?tab=payments", "billing.payment.view")),
			si("ar-collections", "Collections", "support_agent", "/accounting/receivables?tab=collections", "accounting.receivable.view"),
			si("ar-reconciliation", "Reconciliation", "sync", "/accounting/receivables?tab=reconciliation", "accounting.receivable.view"),
			si("ar-aging", "AR Aging", "hourglass_bottom", "/accounting/receivables?tab=aging", "accounting.receivable.view"),
			si("ar-statements", "Customer Statements", "description", "/accounting/receivables?tab=statements", "accounting.receivable.view"),
			si("ar-allowance", "Allowance & Write-off", "money_off", "/accounting/receivables?tab=allowance", "accounting.receivable.view"),
		),
		group("accounts-payable", "Accounts Payable", "receipt_long",
			si("ap-bills", "Bills", "receipt_long", "/accounting/payables?tab=bills", "accounting.payable.view"),
			si("ap-payments", "Payments", "payments", "/accounting/payables?tab=payments", "accounting.payable.view"),
			inModule("procurement", si("ap-notes", "Debit/Credit Notes", "receipt", "/accounting/payables?tab=notes", "procurement.debit_note.view")),
			si("ap-follow-up", "Vendor Follow-up", "support_agent", "/accounting/payables?tab=follow-up", "accounting.payable.view"),
			si("ap-reconciliation", "Reconciliation", "sync", "/accounting/payables?tab=reconciliation", "accounting.payable.view"),
			si("ap-aging", "AP Aging", "hourglass_bottom", "/accounting/payables?tab=aging", "accounting.payable.view"),
			si("ap-statements", "Vendor Statements", "description", "/accounting/payables?tab=statements", "accounting.payable.view"),
		),
		group("cash-bank", "Cash & Bank", "account_balance",
			si("cash-management", "Cash Management", "account_balance_wallet", "/accounting/cash-bank?tab=cash", "accounting.cash.view"),
			si("bank-accounts", "Bank Accounts", "account_balance", "/accounting/cash-bank?tab=accounts", "accounting.bank_transaction.view"),
			si("bank-reconciliation", "Bank Reconciliation", "sync", "/accounting/cash-bank?tab=reconciliations", "accounting.bank_transaction.view"),
		),
	),
	section("accounting", "Accounting",
		si("general-ledger", "General Ledger", "menu_book", "/accounting/general-ledger?tab=ledger", "accounting.ledger.view"),
		si("chart-of-accounts", "Chart of Accounts", "account_tree", "/accounting/general-ledger?tab=accounts", "accounting.journal.view"),
		si("journal-entries", "Journal Entries", "edit_note", "/accounting/general-ledger?tab=journals", "accounting.journal.view"),
		inModule("inventory", si("fixed-assets", "Fixed Assets", "inventory_2", "/inventory/assets", "inventory.asset.view")),
		si("period-closing", "Period Closing", "event_available", "/accounting/periods", "accounting.period.view"),
	),
	section("revenue-tax", "Revenue & Tax",
		group("revenue-recognition", "Revenue Recognition", "insights",
			si("revenue-allocation", "Revenue Allocation", "hub", "/accounting/revenue-recognition?tab=allocations", "accounting.revenue.view"),
			si("deferred-revenue", "Deferred Revenue", "schedule", "/accounting/revenue-recognition?tab=deferred", "accounting.revenue.view"),
			si("recognition-schedule", "Recognition Schedule", "event_upcoming", "/accounting/revenue-recognition?tab=schedule", "accounting.revenue.view"),
			si("service-charge", "Service Charge", "payments", "/accounting/revenue-recognition?tab=service-charge", "accounting.revenue.view"),
		),
		group("tax", "Tax", "receipt",
			si("tax-transactions", "Tax Transactions", "receipt", "/accounting/tax?tab=transactions", "accounting.tax_invoice.view"),
			si("tax-reports", "Tax Reports", "summarize", "/accounting/tax?tab=reports", "accounting.tax_invoice.view"),
			si("tax-configuration", "Tax Configuration", "tune", "/accounting/tax?tab=configuration", "accounting.tax_invoice.view"),
		),
	),
	section("budget-control", "Budget & Control",
		soon("budget", "Budget", "savings"),
		si("budget-vs-actual", "Budget vs Actual", "monitoring", "/accounting/budget-vs-actual", "accounting.dashboard.view"),
		soon("cost-center", "Cost Center", "hub"),
	),
	section("reports", "Reports",
		si("financial-statements", "Financial Statements", "description", "/accounting/reports", "accounting.report.view"),
		si("revenue-reports", "Revenue Reports", "bar_chart", "/reports/accounting.revenue_by_business_line", "reporting.accounting_revenue_by_business_line.view"),
		si("ar-reports", "AR Reports", "request_quote", "/accounting/receivables?tab=aging", "accounting.receivable.view"),
		si("ap-reports", "AP Reports", "receipt_long", "/accounting/payables?tab=aging", "accounting.payable.view"),
		si("cash-flow", "Cash Flow", "bar_chart", "/reports/accounting.cash_flow", "reporting.accounting_cash_flow.view"),
		si("tax-reports-list", "Tax Reports", "summarize", "/reports/accounting.tax_ppn", "reporting.accounting_tax_ppn.view"),
	),
	si("approvals", "Approvals", "approval", "/approvals", ""),
	si("notifications", "Notifications", "notifications", "/notifications", ""),
	si("settings", "Settings", "settings", "/accounting/setup?tab=configuration", "accounting.setup.view"),
}

// roleTree returns the role tree of a shell when the principal's only role
// has one.
func roleTree(p *authz.Principal, shell string) []Item {
	codes := p.RoleCodes()
	if len(codes) != 1 {
		return nil
	}
	return RoleTrees[codes[0]][shell]
}

// Build filters a shell's tree for the principal at the active property.
func (sv *Service) Build(ctx context.Context, shell string) (Menu, error) {
	tree, ok := Trees[shell]
	if !ok {
		return Menu{}, errs.BadRequest("invalid_shell", "unknown shell")
	}
	p := authz.From(ctx)
	if t := roleTree(p, shell); t != nil {
		tree = t
	}
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
