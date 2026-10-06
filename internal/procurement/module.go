// Package procurement is Procurement. P0 provides the Supplier foundation
// entity (FR-MD-05); PRD P4 (EP-10..EP-15) completes the supplier master and
// adds the procure-to-pay documents (see service.go).
package procurement

import (
	"context"
	"regexp"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
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
		resource.Status("active", "inactive", "blocked"), resource.Attributes(),
		// PRD P4 FR-SUP-01: supplier master.
		{Name: "legalName", Column: "legal_name", Label: "Legal Name", Kind: resource.String, Max: 200, Search: true},
		{Name: "categories", Column: "categories", Label: "Supply Categories", Kind: resource.StringList, Default: []string{}},
		{Name: "pkp", Column: "pkp", Label: "PKP (issues Faktur Pajak)", Kind: resource.Bool, Default: false},
		{Name: "withholdingType", Column: "withholding_type", Label: "PPh Withholding", Kind: resource.Enum, Enum: []string{"none", "pph23", "pph4_2"},
			Default: "none"},
		{Name: "paymentTermDays", Column: "payment_term_days", Label: "Payment Term (days)", Kind: resource.Int, Default: int64(30),
			Min: resource.Min(0), MaxN: resource.Max(365)},
		{Name: "currency", Column: "currency", Label: "Currency", Kind: resource.String, Max: 3, Upper: true, Default: "IDR", Pattern: currencyRe,
			PatternMsg: "ISO 4217 code, e.g. IDR"},
		{Name: "leadTimeDays", Column: "lead_time_days", Label: "Lead Time (days)", Kind: resource.Int, Default: int64(0), Min: resource.Min(0)},
		{Name: "contractSupplier", Column: "contract_supplier", Label: "Contract Supplier (RFQ may be skipped)", Kind: resource.Bool, Default: false, Filter: true},
		{Name: "website", Column: "website", Label: "Website", Kind: resource.String, Max: 200},
		{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		{Name: "blockedReason", Column: "blocked_reason", Label: "Blocked Reason", Kind: resource.Text, ReadOnly: true},
		{Name: "blockedAt", Column: "blocked_at", Label: "Blocked At", Kind: resource.Timestamp, ReadOnly: true}},
	Hooks: resource.Hooks{BeforeWrite: supplierBeforeWrite, AfterRead: maskSupplierNPWP},
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// supplierBeforeWrite keeps Blocked under the approval flow (FR-SUP-04):
// a supplier is blocked or unblocked only through :block / :unblock.
func supplierBeforeWrite(_ context.Context, _ pgx.Tx, values map[string]any, before map[string]any) error {
	st, ok := values["status"]
	if !ok {
		return nil
	}
	was := ""
	if before != nil {
		was, _ = before["status"].(string)
	}
	if st == "blocked" && was != "blocked" {
		return errs.Validation("block_requires_approval", "use Block Supplier (approval) to block a supplier",
			errs.Field("status", "invalid", "blocked is set by the block approval"))
	}
	if was == "blocked" && st != "blocked" {
		return errs.Conflict("supplier_blocked", "use Unblock Supplier (approval) to reactivate a blocked supplier")
	}
	return nil
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
