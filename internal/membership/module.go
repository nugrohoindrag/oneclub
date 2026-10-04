// Package membership is the Membership module (EP-06 Basic Membership):
// programs, types and packages with golf privileges and eligibility,
// applications with eligibility check and approval, membership fee and
// activation, cards (physical + digital QR), family members and corporate
// nominees, renewal foundation and membership history.
package membership

import (
	"regexp"

	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

var code20 = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)

func codeField(label string) resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: label, Kind: resource.String, Required: true, Max: 20, Upper: true,
		Pattern: code20, PatternMsg: "1–20 characters: A–Z, 0–9, - or _", Search: true}
}

var Members = &resource.Def{
	Key: "membership.member", Module: "membership", Perm: "membership.member", Path: "/api/v1/membership/members", Table: "membership.members",
	Name: "Member", Plural: "Members", Tag: "Members", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Member No."), resource.Name(),
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "membershipType", Column: "membership_type", Label: "Membership Type", Kind: resource.String, Max: 60, Filter: true},
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Filter: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "joinedOn", Column: "joined_on", Label: "Member Since", Kind: resource.Date},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60, Search: true},
		{Name: "userId", Column: "user_id", Label: "Portal User", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.users", Label: "user"}},
		resource.Status("pending", "active", "inactive", "suspended", "expired", "paused", "cancelled"), resource.Attributes()},
}

var Programs = &resource.Def{
	Key: "membership.program", Module: "membership", Perm: "membership.program", Path: "/api/v1/membership/programs", Table: "membership.programs",
	Name: "Membership Program", Plural: "Membership Programs", Tag: "Membership Programs", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "programKind", Column: "program_kind", Label: "Program", Kind: resource.Enum, Enum: []string{"golf", "sport_club", "corporate", "residence"}, Required: true, Filter: true},
		{Name: "operational", Column: "operational", Label: "Operational in this phase", Kind: resource.Bool, Default: true},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

var Types = &resource.Def{
	Key: "membership.type", Module: "membership", Perm: "membership.program", Path: "/api/v1/membership/types", Table: "membership.types",
	Name: "Membership Type", Plural: "Membership Types", Tag: "Membership Types", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	SchemaName: "MembershipType",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "programId", Column: "program_id", Label: "Membership Program", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "membership.programs", SameProperty: true, Label: "membership program"}},
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Enum: []string{"individual", "family", "corporate", "couple", "senior",
			"student", "junior", "residence", "bulk_entrance", "monthly", "other"}, Required: true, Filter: true},
		{Name: "memberRate", Column: "member_rate", Label: "Plays at Member Rate", Kind: resource.Bool, Default: true},
		{Name: "golfAccess", Column: "golf_access", Label: "Golf access", Kind: resource.Bool, Default: true},
		{Name: "maxGuests", Column: "max_guests", Label: "Max guests per booking", Kind: resource.Int, Default: int64(3), Min: resource.Min(0)},
		{Name: "bookingWindowDays", Column: "booking_window_days", Label: "Booking window (days)", Kind: resource.Int, Default: int64(14), Min: resource.Min(0)},
		{Name: "maxFamilyMembers", Column: "max_family_members", Label: "Max family members", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "maxNominees", Column: "max_nominees", Label: "Max corporate nominees", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "eligibility", Column: "eligibility", Label: "Eligibility rules", Kind: resource.JSON},
		// P2 lifecycle (EP-04)
		{Name: "entitlements", Column: "entitlements", Label: "Entitlements per line (FR-MBL-05)", Kind: resource.JSON, Default: "{}"},
		{Name: "annualFee", Column: "annual_fee", Label: "Annual Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "graceDays", Column: "grace_days", Label: "Grace Period (days)", Kind: resource.Int, Default: int64(30), Min: resource.Min(0)},
		{Name: "rank", Column: "rank", Label: "Rank (upgrade order)", Kind: resource.Int, Default: int64(0)},
		{Name: "cardReplacementFee", Column: "card_replacement_fee", Label: "Card Replacement Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "reactivationFee", Column: "reactivation_fee", Label: "Reactivation Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "nomineeChangeFee", Column: "nominee_change_fee", Label: "Nominee Replacement Fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

var Packages = &resource.Def{
	Key: "membership.package", Module: "membership", Perm: "membership.program", Path: "/api/v1/membership/packages", Table: "membership.packages",
	Name: "Membership Package", Plural: "Membership Packages", Tag: "Membership Packages", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{codeField("Code"), resource.Name(),
		{Name: "typeId", Column: "type_id", Label: "Membership Type", Kind: resource.UUID, Required: true, Filter: true,
			Ref: &resource.Ref{Table: "membership.types", SameProperty: true, Label: "membership type"}},
		{Name: "periodUnit", Column: "period_unit", Label: "Period", Kind: resource.Enum, Enum: []string{"year", "month"}, Required: true},
		{Name: "periodCount", Column: "period_count", Label: "Number of periods", Kind: resource.Int, Default: int64(1), Min: resource.Min(1)},
		{Name: "joiningFee", Column: "joining_fee", Label: "Joining fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "periodFee", Column: "period_fee", Label: "Period fee", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Upper: true, Default: "IDR"},
		resource.Status("active", "inactive")},
}

func init() {
	Types.Hooks = resource.Hooks{BeforeWrite: typeBeforeWrite}
}

// Contribution returns catalogue entries (P1 foundation + P2 lifecycle).
func Contribution() catalog.Contribution { return catalog.Merge(p1Contribution(), p2Contribution()) }

func p1Contribution() catalog.Contribution {
	perms := resource.Permissions(Members, Programs)
	perms = append(perms, catalog.P("membership", "application", "view", "create", "submit", "activate", "activate_unpaid")...)
	perms = append(perms, catalog.P("membership", "membership", "view", "renew")...)
	perms = append(perms, catalog.P("membership", "card", "view", "issue")...)
	admin := append(resource.AllActions(Members, Programs), "membership.application.view", "membership.application.create", "membership.application.submit",
		"membership.application.activate", "membership.membership.view", "membership.membership.renew", "membership.card.view", "membership.card.issue")
	view := []string{"membership.member.view", "membership.program.view", "membership.membership.view", "membership.card.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":     append(append([]string{}, admin...), "membership.application.activate_unpaid"),
			"membership_admin":   admin,
			"membership_manager": append(append([]string{}, admin...), "membership.application.activate_unpaid"),
			"general_manager":    append(append([]string{}, view...), "membership.application.view"),
			"club_manager":       append(append([]string{}, view...), "membership.application.view"),
			"golf_manager":       view,
			"golf_admin":         view,
			"reservation_staff":  view,
			"front_desk":         view,
			"starter_marshal":    {"membership.member.view", "membership.card.view"},
			"finance_manager":    append(append([]string{}, view...), "membership.application.view"),
			"accountant":         {"membership.member.view", "membership.membership.view"},
		},
	}
}
