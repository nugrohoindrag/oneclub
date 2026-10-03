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

func m(module, label, icon, path, phase string) Item {
	return Item{Key: module, Label: label, Path: path, Icon: icon, Module: module, Permission: catalog.ModuleAccess(module), ComingSoon: true, Phase: phase}
}

func s(key, label, path, perm string, children ...Item) Item {
	return Item{Key: key, Label: label, Path: path, Permission: perm, Children: children}
}

// Trees are the static menu definitions per shell.
var Trees = map[string][]Item{
	"backoffice": {
		{Key: "dashboard", Label: "Dashboard", Path: "/", Icon: "space_dashboard"},
		{Key: "approvals", Label: "Approvals", Path: "/approvals", Icon: "approval"},
		m("golf", "Golf", "golf_course", "/golf", "P1"),
		m("sportclub", "Sport Club", "sports_tennis", "/sport-club", "P3"),
		m("membership", "Membership", "card_membership", "/membership", "P1"),
		m("reservation", "Booking", "event_available", "/booking", "P1"),
		m("stay", "Stay & Venue", "hotel", "/stay-venue", "P3"),
		m("banquet", "Banquet & Event", "celebration", "/banquet-event", "P3"),
		m("crm", "CRM", "groups", "/crm", "P4"),
		m("commercial", "Commercial", "storefront", "/commercial", "P2"),
		m("inventory", "Inventory", "inventory_2", "/inventory", "P4"),
		m("procurement", "Procurement", "shopping_cart", "/procurement", "P4"),
		m("accounting", "Accounting", "account_balance", "/accounting", "P2"),
		m("hris", "HRIS", "badge", "/hris", "P5"),
		{Key: "reports", Label: "Reports", Path: "/reports", Icon: "monitoring", Module: "reporting", Permission: "reporting.report.view", Children: []Item{
			s("reports-list", "Reports", "/reports", "reporting.report.view"),
			s("reports-exports", "Exports", "/reports/exports", "reporting.export.create"),
			s("management-dashboard", "Management Dashboard", "/management", catalog.ManagementView),
		}},
		{Key: "settings", Label: "Settings", Path: "/settings", Icon: "settings", Module: "platform", Children: []Item{
			s("organization", "Organization", "/settings/organization", "platform.organization.view",
				s("properties", "Properties", "/settings/organization/properties", "platform.property.view"),
				s("departments", "Departments", "/settings/organization/departments", "platform.department.view"),
				s("employees", "Employees", "/settings/organization/employees", "platform.employee.view"),
			),
			s("customer-instance", "Customer Instance", "/settings/customer-instance", "platform.instance.view"),
			s("venues", "Venues", "/settings/venues", "platform.venue.view"),
			{Key: "courses", Label: "Courses", Path: "/settings/courses", Module: "golf", Permission: "golf.course.view"},
			s("users", "Users", "/settings/users", "platform.user.view"),
			s("roles", "Roles & Permissions", "/settings/roles", "platform.role.view"),
			s("features", "Features", "/settings/features", "platform.module.view"),
			s("feature-configuration", "Feature Configuration", "/settings/feature-configuration", "platform.feature_flag.view"),
			s("business-rules", "Business Rules", "/settings/business-rules", "platform.business_rule.view"),
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
		{Key: "golf-performance", Label: "Golf Performance", Path: "/management/golf", Module: "golf", Permission: catalog.ManagementView, ComingSoon: true, Phase: "P1"},
		{Key: "membership-performance", Label: "Membership Performance", Path: "/management/membership", Module: "membership", Permission: catalog.ManagementView, ComingSoon: true, Phase: "P1"},
		{Key: "booking-performance", Label: "Booking Performance", Path: "/management/booking", Module: "reservation", Permission: catalog.ManagementView, ComingSoon: true, Phase: "P1"},
		{Key: "financial-performance", Label: "Financial Performance", Path: "/management/financial", Module: "accounting", Permission: catalog.ManagementView, ComingSoon: true, Phase: "P2"},
	},
	"member": {
		{Key: "home", Label: "Home", Path: "/", Icon: "home", Permission: catalog.ShellMemberPortal},
		{Key: "golf", Label: "Golf", Path: "/golf", Icon: "golf_course", Module: "golf", Permission: catalog.ShellMemberPortal, ComingSoon: true, Phase: "P1"},
		{Key: "bookings", Label: "Bookings", Path: "/bookings", Icon: "event_available", Module: "reservation", Permission: catalog.ShellMemberPortal, ComingSoon: true, Phase: "P1"},
		{Key: "membership", Label: "Membership", Path: "/membership", Icon: "card_membership", Module: "membership", Permission: catalog.ShellMemberPortal, ComingSoon: true, Phase: "P1"},
		{Key: "profile", Label: "Profile", Path: "/profile", Icon: "person", Permission: catalog.ShellMemberPortal},
	},
	"ops": {
		{Key: "home", Label: "Home", Path: "/", Icon: "home", Permission: catalog.ShellOps},
		{Key: "sync", Label: "Sync Queue", Path: "/sync", Icon: "sync", Permission: catalog.ShellOps},
		{Key: "notifications", Label: "Notifications", Path: "/notifications", Icon: "notifications", Permission: catalog.ShellOps},
	},
	"platform-admin": {
		s("customer-instances", "Customer Instances", "/", catalog.ShellPlatformAdmin),
		s("instance-configuration", "Instance Configuration", "/instance-configuration", "platform.instance.update"),
		s("enabled-modules", "Enabled Modules", "/enabled-modules", "platform.module.update"),
		s("feature-configuration", "Feature Configuration", "/feature-configuration", "platform.feature_flag.update"),
		s("branding", "Branding", "/branding", "platform.branding.update"),
		s("custom-domain", "Custom Domain", "/custom-domain", "platform.domain.manage"),
		s("users", "Users", "/users", "platform.user.view"),
		s("roles", "Roles", "/roles", "platform.role.view"),
		s("permissions", "Permissions", "/permissions", "platform.permission.view"),
		s("integrations", "Integrations", "/integrations", "platform.integration.manage"),
		s("feature-flags", "Feature Flags", "/feature-flags", "platform.feature_flag.update"),
		s("locale", "Locale", "/locale", "platform.instance.update"),
		s("currency", "Currency", "/currency", "platform.instance.update"),
		s("timezone", "Timezone", "/timezone", "platform.instance.update"),
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
		Query: []route.Param{{Name: "shell", Enum: []string{"backoffice", "management", "member", "ops", "platform-admin"}}}, Handler: sv.handle})
}
