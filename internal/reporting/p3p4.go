package reporting

// PRD P3 (EP-23 KPI, Dashboard & Reports) and PRD P4 (EP-28) reports and
// dashboards. Each area keeps its reports in its own file (p3_<area>.go,
// p4_<area>.go) on its own read models (reporting views); dashboards are
// added to the dashboards map from an init() in the same file, and extra
// reporting routes through p3p4Routes.

import (
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
)

// P3Reports are the reports of PRD P3.
func P3Reports() []*Report {
	return concatReports(billingP3Reports, salesReports, engagementReports, commercialP3Reports, banquetReports, tournamentReports)
}

// P4Reports are the reports of PRD P4.
func P4Reports() []*Report {
	return concatReports(inventoryReports, procurementReports, accountingReports, cmsReports)
}

func concatReports(lists ...[]*Report) []*Report {
	var out []*Report
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// p3p4Routes are extra reporting routes of the P3 / P4 areas (registered
// from init()).
var p3p4Routes []func(s *Service, reg *route.Registry)

// RegisterP3P4 adds the P3 / P4 reporting routes (wired by internal/app).
func (s *Service) RegisterP3P4(reg *route.Registry) {
	for _, f := range p3p4Routes {
		f(s, reg)
	}
}

// reportRoles grants the reports of a module to the role templates that
// own the module (Product Overview §44).
var reportRoles = map[string][]string{
	"billing":     {"property_admin", "general_manager", "finance_manager", "accountant"},
	"crm":         {"property_admin", "general_manager", "club_manager", "crm_admin", "sales_executive", "marketing_staff", "finance_manager"},
	"commercial":  {"property_admin", "general_manager", "club_manager", "resort_manager", "outlet_manager", "finance_manager", "marketing_staff"},
	"banquet":     {"property_admin", "general_manager", "resort_manager", "banquet_manager", "banquet_sales", "event_manager", "finance_manager"},
	"golf":        {"property_admin", "general_manager", "club_manager", "golf_manager", "golf_admin"},
	"inventory":   {"property_admin", "general_manager", "inventory_manager", "finance_manager", "accountant", "outlet_manager"},
	"procurement": {"property_admin", "general_manager", "procurement_manager", "procurement_staff", "finance_manager"},
	"accounting":  {"property_admin", "general_manager", "finance_manager", "accountant"},
	"cms":         {"property_admin", "marketing_staff"},
}

// P3P4Contribution adds one permission per P3 / P4 report and grants it
// with the dashboards to the owning roles.
func P3P4Contribution() catalog.Contribution {
	var perms []catalog.Permission
	roles := map[string][]string{}
	seen := map[string]bool{}
	for _, r := range append(P3Reports(), P4Reports()...) {
		if seen[r.Permission] {
			continue
		}
		seen[r.Permission] = true
		perms = append(perms, catalog.Permission{Code: r.Permission, Description: r.Name})
		for _, role := range reportRoles[r.Module] {
			roles[role] = append(roles[role], r.Permission, "reporting.report.view", "reporting.dashboard.view", "reporting.export.create")
		}
	}
	return catalog.Contribution{Permissions: perms, RolePermissions: roles}
}
