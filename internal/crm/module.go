// Package crm is CRM & Sales. P0 provides the Customer and Guest foundation
// entities (FR-MD-05, PRD §5.2); full profiles arrive in P1.
package crm

import (
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var Customers = &resource.Def{
	Key: "crm.customer", Module: "crm", Perm: "crm.customer", Path: "/api/v1/crm/customers", Table: "crm.customers",
	Name: "Customer", Plural: "Customers", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Customer Code"), resource.Name(),
		{Name: "customerType", Column: "customer_type", Label: "Customer Type", Kind: resource.Enum, Enum: []string{"individual", "corporate"}, Default: "individual", Filter: true},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var Guests = &resource.Def{
	Key: "crm.guest", Module: "crm", Perm: "crm.guest", Path: "/api/v1/crm/guests", Table: "crm.guests",
	Name: "Guest", Plural: "Guests", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Guest Code"), resource.Name(),
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "idNumber", Column: "id_number", Label: "ID Number (NIK/Passport)", Kind: resource.String, Max: 40},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Customers, Guests),
		RolePermissions: map[string][]string{
			"property_admin":     resource.AllActions(Customers, Guests),
			"crm_admin":          resource.AllActions(Customers, Guests),
			"membership_admin":   {"crm.customer.view", "crm.guest.view"},
			"membership_manager": {"crm.customer.view", "crm.guest.view"},
			"sales_executive":    {"crm.customer.view", "crm.customer.create", "crm.customer.update"},
		},
	}
}
