// Package membership is the Membership module. P0 provides the Member
// foundation entity (FR-MD-05); the membership lifecycle arrives in P1.
package membership

import (
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var Members = &resource.Def{
	Key: "membership.member", Module: "membership", Perm: "membership.member", Path: "/api/v1/membership/members", Table: "membership.members",
	Name: "Member", Plural: "Members", Tag: "Foundation Data", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Member No."), resource.Name(),
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "membershipType", Column: "membership_type", Label: "Membership Type", Kind: resource.String, Max: 60, Filter: true},
		{Name: "userId", Column: "user_id", Label: "Portal User", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		resource.Status("active", "inactive", "suspended", "expired"), resource.Attributes()},
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	return catalog.Contribution{
		Permissions: resource.Permissions(Members),
		RolePermissions: map[string][]string{
			"property_admin":     resource.AllActions(Members),
			"membership_admin":   resource.AllActions(Members),
			"membership_manager": {"membership.member.view", "membership.member.export"},
		},
	}
}
