// Package crm is CRM & Sales. P1 (EP-01) makes the Customer and Guest
// foundation entities full: customer profile, Customer vs Guest with
// upgrade, duplicate detection and merge, family relationships, corporate
// accounts with nominees, preferences and personal data handling (UU PDP).
// Customer 360 and Customer History are read models in reporting.
package crm

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/resource"
)

// SensitivePermission reveals identity numbers (NIK / passport) unmasked.
const SensitivePermission = "crm.customer.view_sensitive"

var Customers = &resource.Def{
	Key: "crm.customer", Module: "crm", Perm: "crm.customer", Path: "/api/v1/crm/customers", Table: "crm.customers",
	Name: "Customer", Plural: "Customers", Tag: "Customers", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Customer Code"), resource.Name(),
		{Name: "customerType", Column: "customer_type", Label: "Customer Type", Kind: resource.Enum, Enum: []string{"individual", "corporate"}, Default: "individual", Filter: true},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "gender", Column: "gender", Label: "Gender", Kind: resource.Enum, Enum: []string{"male", "female"}, Filter: true},
		{Name: "birthDate", Column: "birth_date", Label: "Date of Birth", Kind: resource.Date},
		{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
		{Name: "city", Column: "city", Label: "City", Kind: resource.String, Max: 80, Filter: true},
		{Name: "idNumber", Column: "id_number", Label: "ID Number (NIK/Passport)", Kind: resource.String, Max: 40},
		{Name: "photoFileId", Column: "photo_file_id", Label: "Photo", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.files", Label: "photo"}},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 4000},
		{Name: "resident", Column: "resident", Label: "Modernland Resident", Kind: resource.Bool, Default: false, Filter: true},
		{Name: "consentAt", Column: "consent_at", Label: "Consent Given", Kind: resource.Timestamp},
		{Name: "consentChannel", Column: "consent_channel", Label: "Consent Channel", Kind: resource.String, Max: 40},
		{Name: "marketingOptIn", Column: "marketing_opt_in", Label: "Marketing Opt-in", Kind: resource.Bool, Default: false},
		{Name: "duplicateAcknowledged", Column: "duplicate_acknowledged", Label: "Duplicate warning acknowledged", Kind: resource.Bool, Default: false},
		{Name: "userId", Column: "user_id", Label: "Portal User", Kind: resource.UUID, ReadOnly: true},
		{Name: "mergedIntoId", Column: "merged_into_id", Label: "Merged Into", Kind: resource.UUID, ReadOnly: true},
		{Name: "erasedAt", Column: "erased_at", Label: "Personal Data Erased", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60, Search: true},
		resource.Status("active", "inactive", "merged"), resource.Attributes()},
}

var Guests = &resource.Def{
	Key: "crm.guest", Module: "crm", Perm: "crm.guest", Path: "/api/v1/crm/guests", Table: "crm.guests",
	Name: "Guest", Plural: "Guests", Tag: "Customers", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Guest Code"), resource.Name(),
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254, Search: true},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40, Search: true},
		{Name: "idNumber", Column: "id_number", Label: "ID Number (NIK/Passport)", Kind: resource.String, Max: 40},
		{Name: "customerId", Column: "customer_id", Label: "Upgraded To Customer", Kind: resource.UUID, ReadOnly: true, Filter: true},
		{Name: "upgradedAt", Column: "upgraded_at", Label: "Upgraded", Kind: resource.Timestamp, ReadOnly: true},
		{Name: "consentAt", Column: "consent_at", Label: "Consent Given", Kind: resource.Timestamp},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var CorporateAccounts = &resource.Def{
	Key: "crm.corporate_account", Module: "crm", Perm: "crm.corporate_account", Path: "/api/v1/crm/corporate-accounts", Table: "crm.corporate_accounts",
	Name: "Corporate Account", Plural: "Corporate Accounts", Tag: "Corporate Accounts", PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
	Fields: []resource.Field{resource.Code("Account Code"), resource.Name(),
		{Name: "npwp", Column: "npwp", Label: "NPWP", Kind: resource.String, Max: 30, Search: true},
		{Name: "contactName", Column: "contact_name", Label: "Contact Name", Kind: resource.String, Max: 120},
		{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Max: 40},
		{Name: "email", Column: "email", Label: "E-mail", Kind: resource.Email, Max: 254},
		{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
		{Name: "legacyRef", Column: "legacy_ref", Label: "Rhapsody Reference", Kind: resource.String, Max: 60},
		resource.Status("active", "inactive"), resource.Attributes()},
}

var CorporateNominees = &resource.Def{
	Key: "crm.corporate_nominee", Module: "crm", Perm: "crm.corporate_account", Path: "/api/v1/crm/corporate-nominees", Table: "crm.corporate_nominees",
	Name: "Corporate Nominee", Plural: "Corporate Nominees", Tag: "Corporate Accounts", PropertyScoped: true, OrderBy: "created_at, id",
	SchemaName: "CorporateNominee",
	Fields: []resource.Field{
		{Name: "corporateAccountId", Column: "corporate_account_id", Label: "Corporate Account", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "crm.corporate_accounts", SameProperty: true, Label: "corporate account"}},
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "title", Column: "title", Label: "Title", Kind: resource.String, Max: 80},
		{Name: "startsOn", Column: "starts_on", Label: "Starts On", Kind: resource.Date},
		{Name: "endsOn", Column: "ends_on", Label: "Ends On", Kind: resource.Date},
		resource.Status("active", "inactive")},
}

var Relationships = &resource.Def{
	Key: "crm.customer_relationship", Module: "crm", Perm: "crm.customer", Path: "/api/v1/crm/customer-relationships", Table: "crm.customer_relationships",
	Name: "Customer Relationship", Plural: "Customer Relationships", Tag: "Customers", PropertyScoped: true, OrderBy: "created_at, id",
	Fields: []resource.Field{
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "relatedCustomerId", Column: "related_customer_id", Label: "Related Customer", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "related customer"}},
		{Name: "relationship", Column: "relationship", Label: "Relationship", Kind: resource.Enum, Enum: []string{"spouse", "child", "parent", "sibling", "other"}, Required: true, Filter: true},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 500},
		resource.Status("active", "inactive")},
}

var Preferences = &resource.Def{
	Key: "crm.customer_preference", Module: "crm", Perm: "crm.customer", Path: "/api/v1/crm/customer-preferences", Table: "crm.customer_preferences",
	Name: "Customer Preference", Plural: "Customer Preferences", Tag: "Customers", PropertyScoped: true, OrderBy: "category, pref_key, id",
	Fields: []resource.Field{
		{Name: "customerId", Column: "customer_id", Label: "Customer", Kind: resource.UUID, Required: true, Filter: true, CreateOnly: true,
			Ref: &resource.Ref{Table: "crm.customers", SameProperty: true, Label: "customer"}},
		{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum, Enum: []string{"golf", "caddy", "golf_cart", "dining", "communication", "other"}, Required: true, Filter: true},
		{Name: "key", Column: "pref_key", Label: "Preference", Kind: resource.String, Required: true, Max: 60},
		{Name: "value", Column: "pref_value", Label: "Value", Kind: resource.String, Max: 200},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 1000},
		resource.Status("active", "inactive")},
}

var phoneRe = regexp.MustCompile(`^\+?[0-9 ()-]{6,24}$`)

func init() {
	Customers.Hooks = resource.Hooks{BeforeWrite: customerBeforeWrite, AfterRead: maskCustomer}
	Guests.Hooks = resource.Hooks{BeforeWrite: guestBeforeWrite, AfterRead: maskGuest}
	Relationships.Hooks = resource.Hooks{BeforeWrite: relationshipBeforeWrite}
}

// NormalizePhone keeps digits (and a leading +); 08xx becomes +628xx.
func NormalizePhone(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range p {
		if (r >= '0' && r <= '9') || (r == '+' && i == 0) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	switch {
	case strings.HasPrefix(s, "0"):
		s = "+62" + s[1:]
	case strings.HasPrefix(s, "62"):
		s = "+" + s
	}
	return s
}

// customerBeforeWrite normalises the phone and warns about duplicates by
// phone / e-mail (FR-CUS-03): the request fails with duplicate_customer and
// the existing profile id until the user acknowledges the warning.
func customerBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if p, ok := v["phone"].(string); ok && p != "" {
		if !phoneRe.MatchString(p) {
			return errs.Validation("invalid_phone", "invalid phone number", errs.Field("phone", "invalid", "digits, spaces, + ( ) - only"))
		}
		v["phone"] = NormalizePhone(p)
	}
	if ack, _ := v["duplicateAcknowledged"].(bool); ack {
		return nil
	}
	phone, _ := v["phone"].(string)
	email, _ := v["email"].(string)
	if phone == "" && email == "" {
		return nil
	}
	var self string
	if before != nil {
		self, _ = before["id"].(string)
		if b, _ := before["phone"].(string); b == phone {
			phone = ""
		}
		if b, _ := before["email"].(string); strings.EqualFold(b, email) {
			email = ""
		}
		if phone == "" && email == "" {
			return nil
		}
	}
	pid, _ := reqctx.Property(ctx)
	dups, err := FindDuplicates(ctx, tx, pid, phone, email, self)
	if err != nil {
		return err
	}
	if len(dups) > 0 {
		d := dups[0]
		e := errs.Conflict("duplicate_customer", "a customer with the same "+d.MatchedOn+" already exists: "+d.Name+" ("+d.Code+")")
		e.Fields = []errs.FieldError{errs.Field(d.MatchedOn, "duplicate", d.ID.String())}
		return e
	}
	return nil
}

func guestBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if p, ok := v["phone"].(string); ok && p != "" {
		if !phoneRe.MatchString(p) {
			return errs.Validation("invalid_phone", "invalid phone number", errs.Field("phone", "invalid", "digits, spaces, + ( ) - only"))
		}
		v["phone"] = NormalizePhone(p)
	}
	return nil
}

func relationshipBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if a, b := v["customerId"], v["relatedCustomerId"]; a != nil && a == b {
		return errs.Validation("self_relationship", "a customer cannot be related to themselves",
			errs.Field("relatedCustomerId", "invalid", "choose another customer"))
	}
	return nil
}

// maskCustomer hides the identity number unless the caller holds
// crm.customer.view_sensitive (FR-CUS-01, FR-CUS-09).
func maskCustomer(ctx context.Context, row map[string]any) {
	if s, ok := row["idNumber"].(string); ok && s != "" && !canSensitive(ctx, row) {
		row["idNumber"] = mask.Phone(s)
	}
}

func maskGuest(ctx context.Context, row map[string]any) { maskCustomer(ctx, row) }

func canSensitive(ctx context.Context, row map[string]any) bool {
	p := authz.From(ctx)
	if p == nil {
		return false
	}
	if pid, ok := reqctx.Property(ctx); ok {
		return p.Can(SensitivePermission, &pid)
	}
	return p.Can(SensitivePermission, nil)
}

// Contribution returns catalogue entries.
func Contribution() catalog.Contribution {
	perms := append(resource.Permissions(Customers, Guests, CorporateAccounts),
		catalog.P("crm", "customer", "view_sensitive", "merge", "erase", "export_personal_data")...)
	perms = append(perms, catalog.P("crm", "customer_overview", "view")...)
	all := append(resource.AllActions(Customers, Guests, CorporateAccounts), "crm.customer.view_sensitive", "crm.customer.merge",
		"crm.customer.erase", "crm.customer.export_personal_data", "crm.customer_overview.view")
	desk := []string{"crm.customer.view", "crm.customer.create", "crm.customer.update", "crm.guest.view", "crm.guest.create", "crm.guest.update",
		"crm.customer_overview.view", "crm.corporate_account.view"}
	return catalog.Contribution{
		Permissions: perms,
		RolePermissions: map[string][]string{
			"property_admin":     all,
			"crm_admin":          all,
			"general_manager":    {"crm.customer.view", "crm.customer_overview.view", "crm.corporate_account.view"},
			"club_manager":       {"crm.customer.view", "crm.customer_overview.view", "crm.corporate_account.view"},
			"membership_admin":   append(append([]string{}, desk...), "crm.corporate_account.create", "crm.corporate_account.update", "crm.customer.view_sensitive"),
			"membership_manager": append(append([]string{}, desk...), "crm.customer.merge", "crm.customer.export", "crm.customer.view_sensitive", "crm.corporate_account.create", "crm.corporate_account.update"),
			"reservation_staff":  desk,
			"front_desk":         desk,
			// Sport Reception sells tickets, courts and classes to members and guests (demo feedback 10 Oct 2026 #39)
			"sport_club_receptionist": desk,
			"golf_manager":            {"crm.customer.view", "crm.customer_overview.view", "crm.guest.view"},
			"golf_admin":              {"crm.customer.view", "crm.guest.view"},
			"finance_manager":         {"crm.customer.view", "crm.customer_overview.view", "crm.corporate_account.view"},
			"accountant":              {"crm.customer.view", "crm.corporate_account.view"},
			"sales_executive":         {"crm.customer.view", "crm.customer.create", "crm.customer.update", "crm.corporate_account.view", "crm.customer_overview.view"},
		},
	}
}
