package payouts

// Partner payout profiles (FR-CDY-01, FR-INS-HR-01) through the generic
// resource engine: the partnership status, NIK / NPWP (PPh 21 surcharge
// without a tax id), bank account (bank file), payment method and the BPJS
// Ketenagakerjaan BPU enrolment of a golf caddy or a partner instructor.
// Level, rating and certification stay with golf / sport club / Core HR.
// Tax ids and bank accounts are masked without
// hris.partner_profile.view_sensitive (UU PDP).

import (
	"context"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// KeyPartnerProfile is the resource key of partner profiles.
const KeyPartnerProfile = "hris.partner_profile"

var (
	nikRe  = regexp.MustCompile(`^\d{16}$`)
	npwpRe = regexp.MustCompile(`^[0-9.\-]{15,20}$`)
	acctRe = regexp.MustCompile(`^[0-9\-]{5,30}$`)
)

// Defs returns the resource definitions.
func (m *Module) Defs() []*resource.Def {
	return []*resource.Def{{
		Key: KeyPartnerProfile, Module: hris.Module, Perm: KeyPartnerProfile, Path: "/api/v1/hris/partner-profiles", Table: "hris.partner_profiles",
		Name: "Partner Profile", Plural: "Partner Profiles", SchemaName: "PayoutPartnerProfile", Tag: "HRIS Payouts", PropertyScoped: true,
		Archive: true, OrderBy: "partner_kind, partner_name, id",
		Fields: []resource.Field{
			{Name: "partnerKind", Column: "partner_kind", Label: "Partner", Kind: resource.Enum, Enum: hris.PayoutKinds, Required: true, CreateOnly: true,
				Filter: true},
			{Name: "partnerId", Column: "partner_id", Label: "Caddy / Instructor", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true},
			{Name: "partnerCode", Column: "partner_code", Label: "Code", Kind: resource.String, ReadOnly: true, Search: true},
			{Name: "partnerName", Column: "partner_name", Label: "Name", Kind: resource.String, ReadOnly: true, Search: true},
			{Name: "partnershipStatus", Column: "partnership_status", Label: "Partnership Status", Kind: resource.Enum,
				Enum: []string{"active", "suspended", "ended"}, Default: "active", Filter: true},
			{Name: "nik", Column: "nik", Label: "NIK", Kind: resource.String, Pattern: nikRe, PatternMsg: "16 digits"},
			{Name: "npwp", Column: "npwp", Label: "NPWP", Kind: resource.String, Pattern: npwpRe, PatternMsg: "15–16 digit NPWP"},
			{Name: "bankCode", Column: "bank_code", Label: "Bank Code", Kind: resource.String, Max: 20, Upper: true},
			{Name: "bankName", Column: "bank_name", Label: "Bank", Kind: resource.String, Max: 80},
			{Name: "bankAccountNo", Column: "bank_account_no", Label: "Account No.", Kind: resource.String, Pattern: acctRe, PatternMsg: "digits"},
			{Name: "bankAccountName", Column: "bank_account_name", Label: "Account Name", Kind: resource.String, Max: 120},
			{Name: "paymentMethod", Column: "payment_method", Label: "Payment Method", Kind: resource.Enum, Enum: []string{"bank_transfer", "cash"},
				Default: "bank_transfer", Filter: true},
			{Name: "bpuEnrolled", Column: "bpu_enrolled", Label: "BPJS Ketenagakerjaan BPU", Kind: resource.Bool, Default: false, Filter: true},
			{Name: "bpuNo", Column: "bpu_no", Label: "BPU Membership No.", Kind: resource.String, Max: 30},
			{Name: "bpuDeclaredIncome", Column: "bpu_declared_income", Label: "BPU Declared Income", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		},
		Hooks: resource.Hooks{BeforeWrite: m.profileBeforeWrite, AfterRead: maskProfile},
	}}
}

func (m *Module) profileBeforeWrite(ctx context.Context, tx pgx.Tx, vals, before map[string]any) error {
	if before != nil {
		return nil
	}
	kind, _ := vals["partnerKind"].(string)
	var pid uuid.UUID
	switch v := vals["partnerId"].(type) {
	case string:
		pid, _ = uuid.Parse(v)
	case uuid.UUID:
		pid = v
	}
	if pid == uuid.Nil {
		return handle.Invalid("partnerId", "required", "a caddy or an instructor")
	}
	if m.Directory == nil {
		return errs.Unavailable("partner directory")
	}
	p, err := m.Directory.Partner(ctx, tx, handle.Property(ctx), kind, pid)
	if err != nil {
		return err
	}
	if p == nil {
		return handle.Invalid("partnerId", "not_found", "no "+kind+" with this id at the property")
	}
	if kind == hris.PayoutKindInstructor && p.Partnership == "employee" {
		return handle.Invalid("partnerId", "employee_instructor", "the instructor is an employee: the honorarium is paid with payroll")
	}
	vals["partnerName"] = p.Name
	vals["partnerCode"] = p.Code
	return nil
}

// maskProfile hides tax ids and bank accounts without the sensitive permission.
func maskProfile(ctx context.Context, row map[string]any) {
	if pid, ok := row["propertyId"].(string); ok {
		if u, err := uuid.Parse(pid); err == nil && can(ctx, PermProfileSensitive, u) {
			return
		}
	}
	for _, k := range []string{"nik", "npwp", "bankAccountNo"} {
		if s, ok := row[k].(string); ok && s != "" {
			row[k] = mask.Text(s)
		}
	}
}
