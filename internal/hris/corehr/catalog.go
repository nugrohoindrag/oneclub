package corehr

// Catalogue contribution of Core HR (contract H6: the HRIS module, its
// permissions, HR roles and HR policies seed the SaaS feature tiers of P6):
// permissions per object, their role templates (Product Overview §44: HR
// Admin, HR Manager, Employee (self-service), department heads) and the
// notification templates (ID/EN, FR-INT-P5-06).

import (
	"slices"
	"strings"

	"oneclub/internal/hris"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Custom permissions of Core HR (beyond the resource CRUD).
func customPermissions() []catalog.Permission {
	var out []catalog.Permission
	for _, g := range [][]catalog.Permission{
		{{Code: "hris.employee.view_sensitive", Description: "See NIK, NPWP, BPJS numbers, bank accounts and health data of employees"}},
		catalog.P("hris", "employee", "transfer", "terminate", "manage_account"),
		catalog.P("hris", "contract", "view", "create", "update", "activate", "renew", "end", "export"),
		{{Code: "hris.contract.view_salary", Description: "See salaries and allowances of employment contracts"}},
		catalog.P("hris", "employee_document", "view", "manage"),
		{{Code: "hris.employee_document.view_confidential", Description: "See confidential employee documents (warning letters, medical)"}},
		catalog.P("hris", "offboarding", "view", "manage"),
		catalog.P("hris", "profile_change", "view", "review"),
		catalog.P("hris", "import", "create"),
		catalog.P("hris", "letter", "generate"),
		{{Code: hris.PermissionESS, Description: "Open Employee Self Service (personal login)"},
			{Code: hris.PermissionTeam, Description: "See my team in Employee Self Service (department heads)"},
			{Code: hris.PermissionTeamApprove, Description: "Approve the requests of my team (department heads)"}},
	} {
		out = append(out, g...)
	}
	return out
}

// Permissions of Core HR.
func (m *Module) Permissions() []catalog.Permission {
	return append(resource.Permissions(m.Defs()...), customPermissions()...)
}

// managerRoles are the role templates of department heads (PRD P5 §4:
// Outlet Manager, Golf Manager, Sport Club Manager, Banquet Manager …).
var managerRoles = []string{"department_head", "general_manager", "club_manager", "resort_manager", "finance_manager", "golf_manager",
	"sport_club_manager", "membership_manager", "banquet_manager", "event_manager", "outlet_manager", "inventory_manager", "procurement_manager",
	"caddy_manager", "hr_manager"}

// Contribution adds the Core HR permissions to the role templates.
func (m *Module) Contribution() catalog.Contribution {
	var all, views []string
	for _, p := range m.Permissions() {
		all = append(all, p.Code)
		if strings.HasSuffix(p.Code, ".view") && !slices.Contains([]string{"hris.contract.view", "hris.bank_account.view", "hris.profile_change.view"}, p.Code) {
			views = append(views, p.Code)
		}
	}
	roles := map[string][]string{
		"hr_admin":        withoutOf(all, hris.PermissionTeamApprove),
		"hr_manager":      all,
		"general_manager": append(slices.Clone(views), "hris.contract.view"),
		"finance_manager": {"hris.employee.view", "hris.employee.view_sensitive", "hris.contract.view", "hris.contract.view_salary",
			"hris.org_unit.view", "hris.position.view", "hris.grade.view", "hris.bank_account.view"},
		// The Property Admin creates property users (FR-IAM-08) and may only assign roles whose permissions it holds:
		// it keeps the ESS permissions every staff role now carries.
		"property_admin": {"hris.org_unit.view", "hris.employee.view", "hris.position.view", "hris.grade.view", hris.PermissionESS,
			hris.PermissionTeam, hris.PermissionTeamApprove},
		"employee_self_service": {hris.PermissionESS},
	}
	for _, r := range managerRoles {
		roles[r] = append(roles[r], hris.PermissionESS, hris.PermissionTeam, hris.PermissionTeamApprove)
	}
	// Every staff role may open its own Employee Self Service once the user
	// is linked to an employee profile.
	for _, rt := range catalog.RoleTemplates {
		if rt.Category == "System" || rt.Category == "Portal" {
			continue
		}
		if !slices.Contains(roles[rt.Code], hris.PermissionESS) {
			roles[rt.Code] = append(roles[rt.Code], hris.PermissionESS)
		}
	}
	return catalog.Contribution{Permissions: m.Permissions(), RolePermissions: roles}
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

// Templates are the notification templates of Core HR (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"hris.contract_expiring": {
			"en": {"Contract {{.number}} ends in {{.daysRemaining}} days", "The {{.contractType}} contract of {{.employeeName}} ({{.employeeNo}}) ends on {{.endDate}}. Renew it, make the employee permanent or end it in HRIS → Employees."},
			"id": {"Kontrak {{.number}} berakhir {{.daysRemaining}} hari lagi", "Kontrak {{.contractType}} {{.employeeName}} ({{.employeeNo}}) berakhir pada {{.endDate}}. Perpanjang, angkat menjadi karyawan tetap, atau akhiri di HRIS → Employees."},
		},
		"hris.document_expiring": {
			"en": {"Document expires: {{.title}}", "The {{.documentType}} of {{.employeeName}} ({{.employeeNo}}) expires on {{.expiresOn}}."},
			"id": {"Dokumen akan kedaluwarsa: {{.title}}", "{{.documentType}} milik {{.employeeName}} ({{.employeeNo}}) berlaku sampai {{.expiresOn}}."},
		},
		"hris.certification_expiring": {
			"en": {"Certification expires in {{.daysRemaining}} days: {{.typeName}}", "The {{.typeName}} certificate of {{.holderName}} expires on {{.expiresOn}}. Plan the renewal training."},
			"id": {"Sertifikasi kedaluwarsa {{.daysRemaining}} hari lagi: {{.typeName}}", "Sertifikat {{.typeName}} milik {{.holderName}} berlaku sampai {{.expiresOn}}. Jadwalkan pelatihan perpanjangan."},
		},
		"hris.certification_expired": {
			"en": {"Certification expired: {{.typeName}}", "The {{.typeName}} certificate of {{.holderName}} expired on {{.expiresOn}}. {{.holderName}} cannot be scheduled or assigned to {{.roles}} work until it is renewed."},
			"id": {"Sertifikasi kedaluwarsa: {{.typeName}}", "Sertifikat {{.typeName}} milik {{.holderName}} kedaluwarsa pada {{.expiresOn}}. {{.holderName}} tidak dapat dijadwalkan atau ditugaskan untuk pekerjaan {{.roles}} sampai sertifikat diperpanjang."},
		},
		"hris.employee_terminated": {
			"en": {"{{.employeeName}} leaves on {{.effectiveDate}}", "{{.employeeName}} ({{.employeeNo}}) leaves the company on {{.effectiveDate}} ({{.terminationType}}). Pending approvals move to {{.supervisorName}}; complete the offboarding checklist."},
			"id": {"{{.employeeName}} keluar per {{.effectiveDate}}", "{{.employeeName}} ({{.employeeNo}}) keluar per {{.effectiveDate}} ({{.terminationType}}). Approval tertunda dialihkan ke {{.supervisorName}}; lengkapi checklist offboarding."},
		},
		"hris.employment_changed": {
			"en": {"Employment change from {{.effectiveDate}}", "Your {{.kind}} takes effect on {{.effectiveDate}}: {{.summary}}."},
			"id": {"Perubahan kepegawaian per {{.effectiveDate}}", "{{.kind}} Anda berlaku mulai {{.effectiveDate}}: {{.summary}}."},
		},
		"hris.profile_change_submitted": {
			"en": {"Personal data change from {{.employeeName}}", "{{.employeeName}} ({{.employeeNo}}) asks to change: {{.fields}}. Verify it in HRIS → Employees → Data Changes."},
			"id": {"Perubahan data pribadi dari {{.employeeName}}", "{{.employeeName}} ({{.employeeNo}}) mengajukan perubahan: {{.fields}}. Verifikasi di HRIS → Employees → Data Changes."},
		},
		"hris.profile_change_decided": {
			"en": {"Your data change was {{.status}}", "Your personal data change ({{.fields}}) was {{.status}}. {{.note}}"},
			"id": {"Perubahan data Anda {{.status}}", "Perubahan data pribadi Anda ({{.fields}}) {{.status}}. {{.note}}"},
		},
		"hris.training_scheduled": {
			"en": {"Training: {{.title}} on {{.date}}", "You are registered for {{.title}} on {{.date}} at {{.location}}."},
			"id": {"Pelatihan: {{.title}} pada {{.date}}", "Anda terdaftar pada {{.title}} tanggal {{.date}} di {{.location}}."},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	slices.SortFunc(out, func(a, b provision.Template) int {
		return strings.Compare(a.Event+a.Locale+a.Channel, b.Event+b.Locale+b.Channel)
	})
	return out
}
