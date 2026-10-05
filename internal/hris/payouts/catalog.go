package payouts

// Catalogue contribution (H6): permissions of service charge, payout runs,
// commission & bonus payout and partner profiles, their role templates (HR
// Manager / HR Admin prepare, Finance Manager and General Manager approve,
// Finance pays; caddies and instructors see their own statements), the
// approval document types and the notification templates (ID/EN,
// FR-INT-P5-06).

import (
	"slices"
	"strings"

	"oneclub/internal/hris"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Approval document types (no workflow configured ⇒ approved at once).
var (
	DistributionDocumentType = provision.DocumentType{Code: "hris.service_charge_distribution", Module: hris.Module, Name: "Service Charge Distribution",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Distributed Amount", Type: "number"},
			{Key: "employees", Label: "Employees", Type: "number"}}}
	PayoutRunDocumentType = provision.DocumentType{Code: "hris.payout_run", Module: hris.Module, Name: "Payout Run",
		Attributes: []provision.DocumentAttribute{{Key: "kind", Label: "Kind (caddy / instructor)", Type: "string"},
			{Key: "amount", Label: "Net Amount", Type: "number"}, {Key: "partners", Label: "Partners", Type: "number"}}}
	BonusDocumentType = provision.DocumentType{Code: "hris.bonus_programme", Module: hris.Module, Name: "Bonus Programme",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Total Bonus", Type: "number"},
			{Key: "employees", Label: "Employees", Type: "number"}, {Key: "bonusType", Label: "Bonus Type", Type: "string"}}}
)

// DocumentTypes are the approval document types of the area.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{DistributionDocumentType, PayoutRunDocumentType, BonusDocumentType}
}

// Permission codes.
const (
	PermServiceChargeView    = "hris.service_charge.view"
	PermServiceChargeManage  = "hris.service_charge.manage"
	PermServiceChargeApprove = "hris.service_charge.approve"
	PermRunView              = "hris.payout_run.view"
	PermRunCreate            = "hris.payout_run.create"
	PermRunCalculate         = "hris.payout_run.calculate"
	PermRunApprove           = "hris.payout_run.approve"
	PermRunPay               = "hris.payout_run.pay"
	PermRunExport            = "hris.payout_run.export"
	PermCommissionView       = "hris.commission_payout.view"
	PermCommissionManage     = "hris.commission_payout.manage"
	PermBonusView            = "hris.bonus_programme.view"
	PermBonusManage          = "hris.bonus_programme.manage"
	PermBonusApprove         = "hris.bonus_programme.approve"
	PermProfileSensitive     = "hris.partner_profile.view_sensitive"
	PermOwnPayout            = "hris.payout.own"
)

func customPermissions() []catalog.Permission {
	var out []catalog.Permission
	for _, g := range [][]catalog.Permission{
		catalog.P(hris.Module, "service_charge", "view", "manage", "approve"),
		catalog.P(hris.Module, "payout_run", "view", "create", "calculate", "approve", "pay", "export"),
		catalog.P(hris.Module, "commission_payout", "view", "manage"),
		catalog.P(hris.Module, "bonus_programme", "view", "manage", "approve"),
		{{Code: PermProfileSensitive, Description: "See the tax id (NIK / NPWP) and bank account of caddies and partner instructors"},
			{Code: PermOwnPayout, Description: "See my own payout history and statements (caddy / partner instructor)"}},
	} {
		out = append(out, g...)
	}
	return out
}

// Permissions of the area.
func (m *Module) Permissions() []catalog.Permission {
	return append(resource.Permissions(m.Defs()...), customPermissions()...)
}

// Contribution grants the permissions to the role templates: HR prepares
// (service charge, commissions, bonuses, payout runs), Finance and the
// General Manager approve and pay; payroll-like amounts stay with these
// roles (FR-PPY-05).
func (m *Module) Contribution() catalog.Contribution {
	var all, views []string
	for _, p := range m.Permissions() {
		if p.Code == PermOwnPayout {
			continue
		}
		all = append(all, p.Code)
		if strings.HasSuffix(p.Code, ".view") || strings.HasSuffix(p.Code, ".view_sensitive") {
			views = append(views, p.Code)
		}
	}
	approvals := []string{PermServiceChargeApprove, PermRunApprove, PermBonusApprove}
	finance := append(slices.Clone(views), PermServiceChargeApprove, PermRunApprove, PermRunPay, PermRunExport, PermBonusApprove,
		catalog.ModuleAccess(hris.Module))
	return catalog.Contribution{Permissions: m.Permissions(), RolePermissions: map[string][]string{
		"hr_manager":         all,
		"hr_admin":           withoutOf(all, append(slices.Clone(approvals), PermRunPay)...),
		"finance_manager":    finance,
		"accountant":         append(slices.Clone(views), PermRunPay, PermRunExport, catalog.ModuleAccess(hris.Module)),
		"general_manager":    append(slices.Clone(views), approvals...),
		"caddy_manager":      {PermRunView, "hris.partner_profile.view", catalog.ModuleAccess(hris.Module)},
		"sport_club_manager": {PermRunView, "hris.partner_profile.view", catalog.ModuleAccess(hris.Module)},
		"caddy":              {PermOwnPayout},
		"instructor_coach":   {PermOwnPayout},
	}}
}

func withoutOf(list []string, drop ...string) []string {
	var out []string
	for _, s := range list {
		if !slices.Contains(drop, s) {
			out = append(out, s)
		}
	}
	return out
}

// Templates are the notification templates (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"hris.service_charge_statement": {
			"en": {"Service charge {{.period}}: {{.amount}}", "Your service charge share of {{.period}} is {{.amount}} (attendance factor {{.factor}}). It is paid with the {{.payPeriod}} payroll. See Employee Self Service → Service Charge."},
			"id": {"Service charge {{.period}}: {{.amount}}", "Bagian service charge Anda periode {{.period}} sebesar {{.amount}} (faktor kehadiran {{.factor}}). Dibayarkan bersama payroll {{.payPeriod}}. Lihat Employee Self Service → Service Charge."},
		},
		"hris.service_charge_decided": {
			"en": {"Service charge distribution {{.number}} {{.decision}}", "The service charge distribution {{.number}} of {{.period}} ({{.amount}} to {{.employees}} employees) was {{.decision}}. {{.reason}}"},
			"id": {"Distribusi service charge {{.number}} {{.decision}}", "Distribusi service charge {{.number}} periode {{.period}} ({{.amount}} untuk {{.employees}} karyawan) {{.decision}}. {{.reason}}"},
		},
		"hris.payout_run_decided": {
			"en": {"Payout run {{.number}} {{.decision}}", "The {{.kind}} payout run {{.number}} ({{.period}}, net {{.amount}} to {{.partners}} partners) was {{.decision}}. {{.reason}}"},
			"id": {"Payout run {{.number}} {{.decision}}", "Payout run {{.kind}} {{.number}} ({{.period}}, neto {{.amount}} untuk {{.partners}} mitra) {{.decision}}. {{.reason}}"},
		},
		"hris.payout_statement_ready": {
			"en": {"Your payout statement {{.period}}", "Your statement {{.number}} of {{.period}}: gross {{.gross}}, PPh 21 {{.pph21}}, BPJS {{.bpu}}, deductions {{.deductions}}, net {{.net}}. {{.status}}"},
			"id": {"Statement pembayaran Anda {{.period}}", "Statement {{.number}} periode {{.period}}: bruto {{.gross}}, PPh 21 {{.pph21}}, BPJS {{.bpu}}, potongan {{.deductions}}, neto {{.net}}. {{.status}}"},
		},
		"hris.commission_unmatched": {
			"en": {"Commission {{.number}} without an employee", "The approved commission statement {{.number}} ({{.period}}, {{.user}}) has no employee profile linked to the user. Link the login to the employee in HRIS, then match it in HRIS → Commissions."},
			"id": {"Komisi {{.number}} tanpa karyawan", "Commission statement {{.number}} ({{.period}}, {{.user}}) yang disetujui belum terhubung ke profil karyawan. Hubungkan login ke karyawan di HRIS, lalu cocokkan di HRIS → Commissions."},
		},
		"hris.bonus_decided": {
			"en": {"Bonus programme {{.number}} {{.decision}}", "The bonus programme {{.number}} {{.name}} ({{.amount}} to {{.employees}} employees, payroll {{.payPeriod}}) was {{.decision}}. {{.reason}}"},
			"id": {"Program bonus {{.number}} {{.decision}}", "Program bonus {{.number}} {{.name}} ({{.amount}} untuk {{.employees}} karyawan, payroll {{.payPeriod}}) {{.decision}}. {{.reason}}"},
		},
	}
	var out []provision.Template
	for ev, locales := range t {
		for loc, v := range locales {
			out = append(out, provision.Template{Event: ev, Channel: "in_app", Locale: loc, Subject: v[0], Body: v[1]},
				provision.Template{Event: ev, Channel: "email", Locale: loc, Subject: v[0], Body: v[1]})
		}
	}
	slices.SortFunc(out, func(a, b provision.Template) int {
		return strings.Compare(a.Event+a.Locale+a.Channel, b.Event+b.Locale+b.Channel)
	})
	return out
}
