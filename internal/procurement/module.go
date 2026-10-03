// Package procurement is Procurement. P0 provides the Supplier foundation
// entity (FR-MD-05).
package procurement

import (
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var Suppliers = &resource.Def{
	Key: "procurement.supplier", Module: "procurement", Perm: "procurement.supplier", Path: "/api/v1/procurement/suppliers", Table: "procurement.suppliers",
	Name: "Supplier", Plural: "Suppliers", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Supplier Code"), resource.Name(),
		{Name: "npwp", Column: "npwp", Label: "NPWP", Kind: resource.String, Max: 30},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
		resource.Status("active", "inactive"), resource.Attributes()},
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Suppliers),
		RolePermissions: map[string][]string{
			"property_admin":      resource.AllActions(Suppliers),
			"procurement_manager": resource.AllActions(Suppliers),
			"procurement_staff":   {"procurement.supplier.view", "procurement.supplier.create", "procurement.supplier.update"},
		},
	}
}
