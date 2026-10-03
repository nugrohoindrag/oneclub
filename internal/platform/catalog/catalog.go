// Package catalog declares the static platform catalogue that is
// synchronised into every customer instance database at migrate time:
// modules (Enabled Modules), permissions (FR-IAM-05), role templates
// (FR-IAM-06, Product Overview §44) and back-office navigation (FR-SH-02).
//
// Domain modules contribute their own entries through Contribution values
// registered by the composition root, so the catalogue grows as P1+ modules
// are built without editing this package.
package catalog

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Module is an enable-able business module (FR-INS-04).
type Module struct {
	Code      string // Go module / permission prefix, e.g. "golf"
	Name      string // Naming Convention §4 label
	Layer     string
	SortOrder int
	AlwaysOn  bool // platform & audit cannot be disabled
	Default   bool // enabled on a new instance
}

// Permission is a granular <module>.<object>.<action> permission.
type Permission struct {
	Code         string
	Description  string
	PlatformOnly bool // only Platform Admin (Platform Administration)
}

func (p Permission) Parts() (module, object, action string) {
	s := strings.Split(p.Code, ".")
	return s[0], s[1], s[2]
}

// RoleTemplate is a seeded role (Product Overview §44).
type RoleTemplate struct {
	Code        string
	Name        string
	Category    string
	Scope       string // platform | instance | property
	MFARequired bool   // FR-IAM-03
	Permissions []string
	// AllPermissions grants every permission (PlatformOnly ones only when
	// IncludePlatformOnly is set).
	AllPermissions      bool
	IncludePlatformOnly bool
}

// Contribution is what a module adds to the catalogue.
type Contribution struct {
	Permissions []Permission
	// RolePermissions adds permissions to existing role templates by code.
	RolePermissions map[string][]string
}

// Catalog is the merged catalogue.
type Catalog struct {
	Modules     []Module
	Permissions []Permission
	Roles       []RoleTemplate
}

// Modules follows Technical Doc §4.1 with Naming Convention §4 labels.
var Modules = []Module{
	{Code: "platform", Name: "Settings", Layer: "Foundation", SortOrder: 900, AlwaysOn: true, Default: true},
	{Code: "audit", Name: "Audit", Layer: "Foundation", SortOrder: 910, AlwaysOn: true, Default: true},
	{Code: "golf", Name: "Golf", Layer: "Business Line", SortOrder: 10, Default: true},
	{Code: "sportclub", Name: "Sport Club", Layer: "Business Line", SortOrder: 20, Default: true},
	{Code: "membership", Name: "Membership", Layer: "Shared Core", SortOrder: 30, Default: true},
	{Code: "reservation", Name: "Booking", Layer: "Shared Core", SortOrder: 40, Default: true},
	{Code: "stay", Name: "Stay & Venue", Layer: "Business Line", SortOrder: 50},
	{Code: "banquet", Name: "Banquet & Event", Layer: "Business Line", SortOrder: 60},
	{Code: "crm", Name: "CRM", Layer: "Customer", SortOrder: 70, Default: true},
	{Code: "commercial", Name: "Commercial", Layer: "Shared Core", SortOrder: 80, Default: true},
	{Code: "billing", Name: "Billing & Payment", Layer: "Shared Core", SortOrder: 85, Default: true},
	{Code: "inventory", Name: "Inventory", Layer: "Back Office", SortOrder: 90},
	{Code: "procurement", Name: "Procurement", Layer: "Back Office", SortOrder: 100, Default: true},
	{Code: "accounting", Name: "Accounting", Layer: "Back Office", SortOrder: 110},
	{Code: "hris", Name: "HRIS", Layer: "Back Office", SortOrder: 120},
	{Code: "reporting", Name: "Reports", Layer: "Foundation", SortOrder: 130, Default: true},
	{Code: "cms", Name: "Landing Page / CMS", Layer: "Public Channel", SortOrder: 140},
}

// ModuleByCode returns a module definition.
func ModuleByCode(code string) (Module, bool) {
	for _, m := range Modules {
		if m.Code == code {
			return m, true
		}
	}
	return Module{}, false
}

// P builds permissions for one object: P("platform", "venue", "view", "create").
func P(module, object string, actions ...string) []Permission {
	out := make([]Permission, 0, len(actions))
	for _, a := range actions {
		out = append(out, Permission{Code: module + "." + object + "." + a})
	}
	return out
}

// CRUD returns the standard master data actions.
var CRUD = []string{"view", "create", "update", "delete", "export", "import"}

func platformOnly(ps []Permission) []Permission {
	for i := range ps {
		ps[i].PlatformOnly = true
	}
	return ps
}

func flat(groups ...[]Permission) []Permission {
	var out []Permission
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// PlatformPermissions are the permissions owned by platform + audit.
var PlatformPermissions = flat(
	P("platform", "instance", "view"),
	platformOnly(P("platform", "instance", "update")),
	P("platform", "module", "view"),
	platformOnly(P("platform", "module", "update")),
	P("platform", "feature_flag", "view"),
	platformOnly(P("platform", "feature_flag", "update")),
	platformOnly(P("platform", "domain", "view", "manage")),
	platformOnly(P("platform", "platform_admin", "access")),
	P("platform", "branding", "update"),
	P("platform", "localization", "update"),
	P("platform", "organization", "view", "update"),
	P("platform", "property", "view", "create", "update", "delete"),
	P("platform", "venue", CRUD...),
	P("platform", "department", CRUD...),
	P("platform", "employee", CRUD...),
	P("platform", "user", "view", "create", "update", "deactivate", "reset_mfa", "export"),
	P("platform", "role", "view", "create", "update", "delete"),
	P("platform", "permission", "view"),
	P("platform", "role_assignment", "view", "manage"),
	P("platform", "session", "view_all", "revoke_all"),
	P("platform", "device", "view", "manage"),
	P("platform", "api_key", "view", "manage"),
	P("platform", "import", "view", "create"),
	P("platform", "notification_template", "view", "update"),
	P("platform", "notification_delivery", "view"),
	P("platform", "notification", "send_test"),
	P("platform", "approval_workflow", "view", "manage"),
	P("platform", "approval", "view_all", "request_test", "delegate"),
	P("platform", "integration", "view", "manage", "test"),
	P("platform", "integration_log", "view"),
	P("platform", "bridge_agent", "view", "manage"),
	P("platform", "job", "view", "retry", "discard"),
	P("platform", "business_rule", "view", "manage"),
	P("platform", "club_policy", "view", "manage"),
	P("platform", "system_settings", "view"),
	// Application surfaces (FR-SH-02, Exit Criteria #7).
	P("platform", "backoffice", "access"),
	P("platform", "ops", "access"),
	P("platform", "member_portal", "access"),
	P("audit", "log", "view", "export", "view_sensitive"),
)

// ModuleAccess is the permission that shows a domain module in navigation.
func ModuleAccess(module string) string { return module + ".module.access" }

func moduleAccessPermissions() []Permission {
	var out []Permission
	for _, m := range Modules {
		if m.Code == "platform" || m.Code == "audit" {
			continue
		}
		out = append(out, Permission{Code: ModuleAccess(m.Code), Description: "Open the " + m.Name + " module"})
	}
	return out
}

// Shell permission codes.
const (
	ShellBackOffice    = "platform.backoffice.access"
	ShellOps           = "platform.ops.access"
	ShellMemberPortal  = "platform.member_portal.access"
	ShellPlatformAdmin = "platform.platform_admin.access"
	ManagementView     = "reporting.dashboard.view"
)

func ma(mods ...string) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		out = append(out, ModuleAccess(m))
	}
	return out
}

func cat(groups ...[]string) []string {
	var out []string
	for _, g := range groups {
		for _, s := range g {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

var (
	bo         = []string{ShellBackOffice}
	ops        = []string{ShellOps}
	management = []string{ShellBackOffice, ManagementView, "reporting.report.view", "reporting.export.create"}
)

// PropertyAdminPermissions: property-level administration (FR-IAM-08).
var PropertyAdminPermissions = cat(bo, ops, []string{
	"platform.instance.view", "platform.module.view", "platform.feature_flag.view",
	"platform.organization.view", "platform.property.view",
	"platform.venue.view", "platform.venue.create", "platform.venue.update", "platform.venue.delete", "platform.venue.export", "platform.venue.import",
	"platform.department.view", "platform.department.create", "platform.department.update", "platform.department.delete", "platform.department.export", "platform.department.import",
	"platform.employee.view", "platform.employee.create", "platform.employee.update", "platform.employee.delete", "platform.employee.export", "platform.employee.import",
	"platform.user.view", "platform.user.create", "platform.user.update", "platform.user.deactivate", "platform.user.reset_mfa", "platform.user.export",
	"platform.role.view", "platform.permission.view", "platform.role_assignment.view", "platform.role_assignment.manage",
	"platform.device.view", "platform.device.manage",
	"platform.import.view", "platform.import.create",
	"platform.notification_template.view", "platform.notification_delivery.view",
	"platform.approval_workflow.view", "platform.approval.view_all", "platform.approval.request_test",
	"platform.integration.view", "platform.integration_log.view",
	"platform.bridge_agent.view", "platform.bridge_agent.manage",
	"platform.job.view", "platform.business_rule.view", "platform.club_policy.view",
	"platform.system_settings.view",
	"audit.log.view",
	ManagementView, "reporting.report.view", "reporting.export.create",
}, ma("golf", "sportclub", "membership", "reservation", "crm", "commercial", "billing", "procurement", "reporting"))

// RoleTemplates seeds Product Overview §44 plus the System roles.
var RoleTemplates = []RoleTemplate{
	// System
	{Code: "platform_admin", Name: "Platform Admin", Category: "System", Scope: "platform", MFARequired: true, AllPermissions: true, IncludePlatformOnly: true},
	{Code: "super_admin", Name: "Super Admin", Category: "System", Scope: "instance", MFARequired: true, AllPermissions: true},
	{Code: "property_admin", Name: "Property Admin", Category: "System", Scope: "property", MFARequired: true, Permissions: PropertyAdminPermissions},
	// Management
	{Code: "general_manager", Name: "General Manager", Category: "Management", Scope: "property", Permissions: cat(management, []string{"audit.log.view", "platform.approval.view_all"}, ma("golf", "sportclub", "membership", "reservation", "stay", "banquet", "crm", "commercial", "billing", "inventory", "procurement", "accounting", "hris", "reporting"))},
	{Code: "club_manager", Name: "Club Manager", Category: "Management", Scope: "property", Permissions: cat(management, []string{"audit.log.view"}, ma("golf", "sportclub", "membership", "reservation", "crm", "commercial", "reporting"))},
	{Code: "resort_manager", Name: "Resort Manager", Category: "Management", Scope: "property", Permissions: cat(management, ma("stay", "banquet", "reservation", "commercial", "reporting"))},
	{Code: "finance_manager", Name: "Finance Manager", Category: "Management", Scope: "property", MFARequired: true, Permissions: cat(management, []string{"audit.log.view", "audit.log.export", "platform.approval.view_all"}, ma("billing", "accounting", "commercial", "procurement", "reporting"))},
	// Golf
	{Code: "golf_manager", Name: "Golf Manager", Category: "Golf", Scope: "property", Permissions: cat(bo, ops, ma("golf", "reservation", "reporting"), []string{"reporting.report.view"})},
	{Code: "golf_admin", Name: "Golf Admin", Category: "Golf", Scope: "property", Permissions: cat(bo, ma("golf", "reservation"))},
	{Code: "starter_marshal", Name: "Starter / Marshal", Category: "Golf", Scope: "property", Permissions: cat(ops, ma("golf"))},
	{Code: "caddy_manager", Name: "Caddy Manager", Category: "Golf", Scope: "property", Permissions: cat(ops, ma("golf"))},
	{Code: "caddy", Name: "Caddy", Category: "Golf", Scope: "property", Permissions: cat(ops, ma("golf"))},
	{Code: "golf_staff", Name: "Golf Staff", Category: "Golf", Scope: "property", Permissions: cat(ops, ma("golf"))},
	{Code: "driving_range_staff", Name: "Driving Range Staff", Category: "Golf", Scope: "property", Permissions: cat(ops, ma("golf"))},
	// Sport Club
	{Code: "sport_club_manager", Name: "Sport Club Manager", Category: "Sport Club", Scope: "property", Permissions: cat(bo, ops, ma("sportclub", "reservation", "reporting"), []string{"reporting.report.view"})},
	{Code: "sport_club_receptionist", Name: "Sport Club Receptionist", Category: "Sport Club", Scope: "property", Permissions: cat(ops, ma("sportclub"))},
	{Code: "instructor_coach", Name: "Instructor / Coach", Category: "Sport Club", Scope: "property", Permissions: cat(ops, ma("sportclub"))},
	{Code: "lifeguard", Name: "Lifeguard", Category: "Sport Club", Scope: "property", Permissions: cat(ops, ma("sportclub"))},
	// Membership
	{Code: "membership_admin", Name: "Membership Admin", Category: "Membership", Scope: "property", Permissions: cat(bo, ma("membership", "crm"))},
	{Code: "membership_manager", Name: "Membership Manager", Category: "Membership", Scope: "property", Permissions: cat(bo, ma("membership", "crm", "reporting"), []string{"reporting.report.view"})},
	// Reservation
	{Code: "reservation_staff", Name: "Reservation Staff", Category: "Reservation", Scope: "property", Permissions: cat(bo, ma("reservation"))},
	{Code: "front_desk", Name: "Front Desk", Category: "Reservation", Scope: "property", Permissions: cat(ops, ma("reservation", "stay"))},
	// Banquet & Event
	{Code: "banquet_manager", Name: "Banquet Manager", Category: "Banquet & Event", Scope: "property", Permissions: cat(bo, ma("banquet", "reservation", "reporting"), []string{"reporting.report.view"})},
	{Code: "banquet_sales", Name: "Banquet Sales", Category: "Banquet & Event", Scope: "property", Permissions: cat(bo, ma("banquet", "crm"))},
	{Code: "event_manager", Name: "Event Manager", Category: "Banquet & Event", Scope: "property", Permissions: cat(bo, ma("banquet"))},
	{Code: "event_staff", Name: "Event Staff", Category: "Banquet & Event", Scope: "property", Permissions: cat(ops, ma("banquet"))},
	// Commercial
	{Code: "cashier", Name: "Cashier", Category: "Commercial", Scope: "property", Permissions: cat(ops, ma("commercial", "billing"))},
	{Code: "pos_staff", Name: "POS Staff", Category: "Commercial", Scope: "property", Permissions: cat(ops, ma("commercial"))},
	{Code: "kitchen_staff", Name: "Kitchen Staff", Category: "Commercial", Scope: "property", Permissions: cat(ops, ma("commercial"))},
	{Code: "outlet_manager", Name: "Outlet Manager", Category: "Commercial", Scope: "property", Permissions: cat(bo, ops, ma("commercial", "billing", "inventory"))},
	// Sales & CRM
	{Code: "sales_executive", Name: "Sales Executive", Category: "Sales & CRM", Scope: "property", Permissions: cat(bo, ma("crm", "banquet"))},
	{Code: "crm_admin", Name: "CRM Admin", Category: "Sales & CRM", Scope: "property", Permissions: cat(bo, ma("crm"))},
	{Code: "marketing_staff", Name: "Marketing Staff", Category: "Sales & CRM", Scope: "property", Permissions: cat(bo, ma("crm", "cms"))},
	// Warehouse
	{Code: "warehouse_staff", Name: "Warehouse Staff", Category: "Warehouse", Scope: "property", Permissions: cat(ops, ma("inventory"))},
	{Code: "inventory_manager", Name: "Inventory Manager", Category: "Warehouse", Scope: "property", Permissions: cat(bo, ma("inventory", "procurement", "reporting"), []string{"reporting.report.view"})},
	// Procurement
	{Code: "procurement_staff", Name: "Procurement Staff", Category: "Procurement", Scope: "property", Permissions: cat(bo, ma("procurement"))},
	{Code: "procurement_manager", Name: "Procurement Manager", Category: "Procurement", Scope: "property", Permissions: cat(bo, ma("procurement", "reporting"), []string{"reporting.report.view"})},
	{Code: "approver", Name: "Approver", Category: "Procurement", Scope: "property", Permissions: cat(bo)},
	// Finance (Finance Manager is seeded under Management)
	{Code: "accountant", Name: "Accountant", Category: "Finance", Scope: "property", MFARequired: true, Permissions: cat(bo, ma("accounting", "billing", "reporting"), []string{"reporting.report.view", "audit.log.view"})},
	// HR
	{Code: "hr_admin", Name: "HR Admin", Category: "HR", Scope: "property", Permissions: cat(bo, ma("hris"), []string{"platform.employee.view", "platform.department.view"})},
	{Code: "hr_manager", Name: "HR Manager", Category: "HR", Scope: "property", Permissions: cat(bo, ma("hris", "reporting"), []string{"platform.employee.view", "platform.department.view", "reporting.report.view"})},
	{Code: "employee_self_service", Name: "Employee (self-service)", Category: "HR", Scope: "property", Permissions: cat(bo)},
	// Member & Guest Portal
	{Code: "member", Name: "Member", Category: "Portal", Scope: "property", Permissions: []string{ShellMemberPortal}},
	{Code: "guest", Name: "Guest", Category: "Portal", Scope: "property", Permissions: []string{ShellMemberPortal}},
}

// Build merges the platform catalogue with module contributions and checks
// that every role permission exists.
func Build(contribs ...Contribution) (*Catalog, error) {
	c := &Catalog{Modules: Modules}
	seen := map[string]bool{}
	add := func(p Permission) error {
		if seen[p.Code] {
			return fmt.Errorf("catalog: duplicate permission %s", p.Code)
		}
		if strings.Count(p.Code, ".") != 2 {
			return fmt.Errorf("catalog: permission %q must be <module>.<object>.<action>", p.Code)
		}
		if p.Description == "" {
			_, o, a := p.Parts()
			p.Description = strings.ReplaceAll(strings.ToUpper(a[:1])+a[1:]+" "+o, "_", " ")
		}
		seen[p.Code] = true
		c.Permissions = append(c.Permissions, p)
		return nil
	}
	for _, p := range append(PlatformPermissions, moduleAccessPermissions()...) {
		if err := add(p); err != nil {
			return nil, err
		}
	}
	extra := map[string][]string{}
	for _, ct := range contribs {
		for _, p := range ct.Permissions {
			if err := add(p); err != nil {
				return nil, err
			}
		}
		for role, ps := range ct.RolePermissions {
			extra[role] = append(extra[role], ps...)
		}
	}
	sort.Slice(c.Permissions, func(i, j int) bool { return c.Permissions[i].Code < c.Permissions[j].Code })

	for _, rt := range RoleTemplates {
		rt.Permissions = cat(rt.Permissions, extra[rt.Code])
		if rt.AllPermissions {
			rt.Permissions = nil
			for _, p := range c.Permissions {
				if !p.PlatformOnly || rt.IncludePlatformOnly {
					rt.Permissions = append(rt.Permissions, p.Code)
				}
			}
		}
		for _, p := range rt.Permissions {
			if !seen[p] {
				return nil, fmt.Errorf("catalog: role %s references unknown permission %s", rt.Code, p)
			}
		}
		c.Roles = append(c.Roles, rt)
	}
	return c, nil
}

// IsPlatformOnly reports whether code is a Platform Admin–only permission.
func (c *Catalog) IsPlatformOnly(code string) bool {
	for _, p := range c.Permissions {
		if p.Code == code {
			return p.PlatformOnly
		}
	}
	return false
}

// Role returns a role template by code.
func (c *Catalog) Role(code string) (RoleTemplate, bool) {
	for _, r := range c.Roles {
		if r.Code == code {
			return r, true
		}
	}
	return RoleTemplate{}, false
}
