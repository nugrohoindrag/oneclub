package payroll

// Catalogue contribution of payroll (H6): permissions, their role templates
// (salary and tax data only for payroll roles, FR-PPY-05: HR Manager, HR
// Admin preparing runs, Finance Manager approving / posting / paying,
// Accountant posting and paying, General Manager approving), the approval
// document types and the notification templates (ID/EN, FR-INT-P5-06).

import (
	"slices"
	"strings"

	"oneclub/internal/hris"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Permissions of payroll.
const (
	PermRunView       = "hris.payroll_run.view"
	PermRunManage     = "hris.payroll_run.manage"
	PermRunApprove    = "hris.payroll_run.approve"
	PermRunPost       = "hris.payroll_run.post"
	PermRunPay        = "hris.payroll_run.pay"
	PermRunExport     = "hris.payroll_run.export"
	PermRunSignOff    = "hris.payroll_run.sign_off"
	PermStructView    = "hris.salary_structure.view"
	PermStructManage  = "hris.salary_structure.manage"
	PermStructApprove = "hris.salary_structure.activate"
	PermRateView      = "hris.statutory_rate.view"
	PermRateManage    = "hris.statutory_rate.manage"
	PermRateActivate  = "hris.statutory_rate.activate"
	PermRateVerify    = "hris.statutory_rate.verify"
	PermAdjView       = "hris.payroll_adjustment.view"
	PermAdjManage     = "hris.payroll_adjustment.manage"
	PermAdjApprove    = "hris.payroll_adjustment.approve"
	PermImport        = "hris.payroll_import.create"
	PermProfileView   = "hris.payroll_profile.view"
)

// Approval document types: the payroll run (FR-PAY-06, approved by Finance &
// HR through the workflow) and the payroll adjustment (FR-PAY-05 bonus,
// corrections). Without a workflow a submitted document is approved at once.
var (
	RunDocumentType = provision.DocumentType{Code: "hris.payroll_run", Module: hris.Module, Name: "Payroll Run",
		Attributes: []provision.DocumentAttribute{{Key: "net", Label: "Net Pay Total", Type: "number"}, {Key: "gross", Label: "Gross Total", Type: "number"},
			{Key: "headcount", Label: "Employees", Type: "number"}, {Key: "runType", Label: "Run Type", Type: "string"},
			{Key: "periodCode", Label: "Period (YYYY-MM)", Type: "string"}}}
	AdjustmentDocumentType = provision.DocumentType{Code: "hris.payroll_adjustment", Module: hris.Module, Name: "Payroll Adjustment",
		Attributes: []provision.DocumentAttribute{{Key: "amount", Label: "Amount", Type: "number"}, {Key: "componentCode", Label: "Component", Type: "string"},
			{Key: "source", Label: "Source (manual / performance_review)", Type: "string"}}}
)

// DocumentTypes are the approval document types of payroll.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{RunDocumentType, AdjustmentDocumentType}
}

func customPermissions() []catalog.Permission {
	var out []catalog.Permission
	for _, g := range [][]catalog.Permission{
		{{Code: PermRunView, Description: "See payroll runs, payslips and salary amounts"},
			{Code: PermRunManage, Description: "Create, calculate, recalculate and cancel payroll runs"},
			{Code: PermRunApprove, Description: "Submit a calculated payroll run for approval (approved at once without a workflow)"},
			{Code: PermRunPost, Description: "Post an approved payroll run (payroll journal, payslips released)"},
			{Code: PermRunPay, Description: "Download the bank file and confirm the payment of a payroll run"},
			{Code: PermRunExport, Description: "Export e-Bupot PPh 21, BPJS and 1721-A1 files"},
			{Code: PermRunSignOff, Description: "Sign off the parallel run of a payroll period (migration)"}},
		{{Code: PermStructView, Description: "See salary structures"}, {Code: PermStructManage, Description: "Create and revise salary structures"},
			{Code: PermStructApprove, Description: "Activate a salary structure version (second person)"}},
		{{Code: PermRateView, Description: "See statutory rate sets (PTKP, PPh 21, BPJS)"},
			{Code: PermRateManage, Description: "Create and edit draft statutory rate sets"},
			{Code: PermRateActivate, Description: "Activate a statutory rate set (second person)"},
			{Code: PermRateVerify, Description: "Record the tax consultant's verification of a statutory rate set"}},
		{{Code: PermAdjView, Description: "See payroll adjustments"}, {Code: PermAdjManage, Description: "Request payroll adjustments and bonuses"},
			{Code: PermAdjApprove, Description: "Approve payroll adjustments (approval workflow)"}},
		{{Code: PermImport, Description: "Import opening year-to-date payroll and legacy payroll for the parallel run"},
			{Code: PermProfileView, Description: "See the payroll profiles (PTKP, TER category, BPJS, bank) of employees"}},
	} {
		out = append(out, g...)
	}
	return out
}

// Permissions of payroll (custom + the resources pay components and loans).
func (m *Module) Permissions() []catalog.Permission {
	return append(resource.Permissions(m.Defs()...), customPermissions()...)
}

// Contribution grants the permissions to the role templates.
func (m *Module) Contribution() catalog.Contribution {
	var all []string
	for _, p := range m.Permissions() {
		all = append(all, p.Code)
	}
	res := resource.AllActions(m.Defs()...)
	roles := map[string][]string{
		"hr_manager": withoutOf(all, PermRunPost, PermRunPay, PermRateActivate),
		"hr_admin": append([]string{PermRunView, PermRunManage, PermRunExport, PermStructView, PermStructManage, PermRateView, PermAdjView, PermAdjManage,
			PermImport, PermProfileView}, res...),
		"finance_manager": {PermRunView, PermRunApprove, PermRunPost, PermRunPay, PermRunExport, PermRunSignOff, PermStructView, PermStructApprove,
			PermRateView, PermRateActivate, PermRateVerify, PermAdjView, PermAdjApprove, PermProfileView, "hris.pay_component.view",
			"hris.employee_loan.view"},
		"accountant":      {PermRunView, PermRunPost, PermRunPay, PermRunExport, PermRateView, "hris.pay_component.view"},
		"general_manager": {PermRunView, PermRunApprove, PermStructView, PermStructApprove, PermAdjView, PermAdjApprove, PermRateView},
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

// Notification events.
const (
	NotifyPayslip     = "hris.payslip_published"
	NotifyRunDecided  = "hris.payroll_run_decided"
	NotifyAdjDecided  = "hris.payroll_adjustment_decided"
	NotifyRunApproved = "hris.payroll_run_approved"
)

// Templates are the notification templates of payroll (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		NotifyPayslip: {
			"en": {"Your payslip {{.period}} is ready", "Your payslip for {{.period}} ({{.runName}}) is available in Employee Self Service → Payslip. Payment date: {{.paymentDate}}."},
			"id": {"Slip gaji {{.period}} Anda sudah tersedia", "Slip gaji periode {{.period}} ({{.runName}}) dapat dilihat di Employee Self Service → Slip Gaji. Tanggal pembayaran: {{.paymentDate}}."},
		},
		NotifyRunDecided: {
			"en": {"Payroll run {{.number}} {{.decision}}", "The payroll run {{.number}} ({{.name}}, {{.headcount}} employees) was {{.decision}}. {{.reason}}"},
			"id": {"Payroll run {{.number}} {{.decision}}", "Payroll run {{.number}} ({{.name}}, {{.headcount}} karyawan) {{.decision}}. {{.reason}}"},
		},
		NotifyRunApproved: {
			"en": {"Payroll run {{.number}} ready to post", "The payroll run {{.number}} ({{.name}}) is approved. Post it in HRIS → Payroll to book the payroll journal and release the payslips."},
			"id": {"Payroll run {{.number}} siap diposting", "Payroll run {{.number}} ({{.name}}) telah disetujui. Posting di HRIS → Payroll untuk membukukan jurnal payroll dan menerbitkan slip gaji."},
		},
		NotifyAdjDecided: {
			"en": {"Payroll adjustment {{.number}} {{.decision}}", "The {{.component}} adjustment {{.number}} for {{.employeeName}} (period {{.period}}) was {{.decision}}. {{.reason}}"},
			"id": {"Penyesuaian payroll {{.number}} {{.decision}}", "Penyesuaian {{.component}} {{.number}} untuk {{.employeeName}} (periode {{.period}}) {{.decision}}. {{.reason}}"},
		},
	}
	whatsApp := map[string]bool{NotifyPayslip: true}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			chs := []string{"email", "in_app"}
			if whatsApp[ev] {
				chs = append(chs, "whatsapp")
			}
			for _, ch := range chs {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	slices.SortFunc(out, func(a, b provision.Template) int {
		return strings.Compare(a.Event+a.Locale+a.Channel, b.Event+b.Locale+b.Channel)
	})
	return out
}
