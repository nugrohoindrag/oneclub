package talent

// Catalogue contribution (H6): permissions of recruitment and performance
// review, their role templates (HR Admin, HR Manager, department heads as
// requesters / interviewers / reviewers, General Manager), the approval
// document types and the notification templates (ID/EN, FR-INT-P5-06).

import (
	"slices"
	"strings"

	"oneclub/internal/hris"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Approval document types: the job requisition (FR-RCT-01) and the offer
// (FR-RCT-04). Without a workflow a submitted document is approved at once.
var (
	RequisitionDocumentType = provision.DocumentType{Code: "hris.job_requisition", Module: hris.Module, Name: "Job Requisition",
		Attributes: []provision.DocumentAttribute{{Key: "headcount", Label: "Headcount", Type: "number"},
			{Key: "salaryMax", Label: "Maximum Salary", Type: "number"}, {Key: "orgUnitId", Label: "Org Unit", Type: "uuid"},
			{Key: "reason", Label: "Reason", Type: "string"}, {Key: "contractType", Label: "Contract Type", Type: "string"}}}
	OfferDocumentType = provision.DocumentType{Code: "hris.job_offer", Module: hris.Module, Name: "Job Offer",
		Attributes: []provision.DocumentAttribute{{Key: "baseSalary", Label: "Base Salary", Type: "number"},
			{Key: "aboveBudget", Label: "Above the Requisition Budget (1 = yes)", Type: "number"}, {Key: "gradeLevel", Label: "Grade Level", Type: "number"},
			{Key: "contractType", Label: "Contract Type", Type: "string"}, {Key: "orgUnitId", Label: "Org Unit", Type: "uuid"}}}
)

// DocumentTypes are the approval document types.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{RequisitionDocumentType, OfferDocumentType}
}

// Custom permissions (beyond the resource CRUD of candidates and review
// templates).
func customPermissions() []catalog.Permission {
	var out []catalog.Permission
	for _, g := range [][]catalog.Permission{
		catalog.P(hris.Module, "job_requisition", "view", "create", "update", "submit", "approve", "close"),
		{{Code: "hris.job_requisition.view_budget", Description: "See the salary budget of job requisitions"}},
		{{Code: "hris.candidate.view_sensitive", Description: "See expected salaries and the CV of candidates"},
			{Code: "hris.candidate.erase", Description: "Erase a candidate's personal data (UU PDP request)"}},
		catalog.P(hris.Module, "application", "view", "manage", "hire"),
		catalog.P(hris.Module, "interview", "view", "manage", "score"),
		catalog.P(hris.Module, "job_offer", "view", "manage", "approve"),
		{{Code: "hris.job_offer.view_salary", Description: "See the salary of job offers"}},
		catalog.P(hris.Module, "review_cycle", "view", "manage"),
		catalog.P(hris.Module, "performance_review", "view", "manage", "calibrate", "promote"),
	} {
		out = append(out, g...)
	}
	return out
}

// Permissions of recruitment and performance review.
func (m *Module) Permissions() []catalog.Permission {
	return append(resource.Permissions(m.Defs()...), customPermissions()...)
}

// requesterRoles are the department heads (PRD P5 §4) who request staff,
// interview candidates and review their team.
var requesterRoles = []string{"department_head", "club_manager", "resort_manager", "finance_manager", "golf_manager", "sport_club_manager",
	"membership_manager", "banquet_manager", "event_manager", "outlet_manager", "inventory_manager", "procurement_manager", "caddy_manager"}

// Contribution grants the permissions to the role templates.
func (m *Module) Contribution() catalog.Contribution {
	var all, views []string
	for _, p := range m.Permissions() {
		all = append(all, p.Code)
		if strings.HasSuffix(p.Code, ".view") {
			views = append(views, p.Code)
		}
	}
	requester := []string{"hris.job_requisition.view", "hris.job_requisition.create", "hris.job_requisition.update", "hris.job_requisition.submit",
		"hris.application.view", "hris.candidate.view", "hris.interview.view", "hris.interview.score", "hris.job_offer.view",
		"hris.review_cycle.view", "hris.performance_review.view"}
	roles := map[string][]string{
		"hr_manager": all,
		"hr_admin": withoutOf(all, "hris.job_requisition.approve", "hris.job_offer.approve", "hris.performance_review.calibrate",
			"hris.candidate.erase"),
		"general_manager": append(slices.Clone(views), "hris.job_requisition.approve", "hris.job_offer.approve", "hris.job_requisition.view_budget",
			"hris.job_offer.view_salary", "hris.interview.score", "hris.performance_review.calibrate"),
	}
	for _, r := range requesterRoles {
		roles[r] = append(roles[r], requester...)
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

// Templates are the notification templates (ID/EN).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"hris.requisition_decided": {
			"en": {"Job requisition {{.number}} {{.decision}}", "Your job requisition {{.number}} ({{.title}}, {{.headcount}} position(s)) was {{.decision}}. {{.reason}}"},
			"id": {"Permintaan tenaga kerja {{.number}} {{.decision}}", "Permintaan tenaga kerja {{.number}} ({{.title}}, {{.headcount}} posisi) {{.decision}}. {{.reason}}"},
		},
		"hris.application_received": {
			"en": {"New application: {{.title}}", "{{.candidate}} applied for {{.title}} ({{.number}}) via {{.source}}. Screen the application in HRIS → Recruitment."},
			"id": {"Lamaran baru: {{.title}}", "{{.candidate}} melamar posisi {{.title}} ({{.number}}) melalui {{.source}}. Lakukan screening di HRIS → Recruitment."},
		},
		"hris.candidate_application_received": {
			"en": {"We received your application for {{.title}}", "Dear {{.candidate}}, thank you for applying for {{.title}} at {{.property}}. Your application number is {{.number}}. We will contact you if your profile matches the position."},
			"id": {"Lamaran Anda untuk {{.title}} telah kami terima", "Yth. {{.candidate}}, terima kasih telah melamar posisi {{.title}} di {{.property}}. Nomor lamaran Anda {{.number}}. Kami akan menghubungi Anda bila profil Anda sesuai."},
		},
		"hris.interview_scheduled": {
			"en": {"Interview: {{.candidate}} on {{.date}}", "You interview {{.candidate}} for {{.title}} on {{.date}} ({{.type}}, {{.location}}). Submit your scorecard after the interview."},
			"id": {"Wawancara: {{.candidate}} pada {{.date}}", "Anda mewawancarai {{.candidate}} untuk posisi {{.title}} pada {{.date}} ({{.type}}, {{.location}}). Isi scorecard setelah wawancara."},
		},
		"hris.candidate_interview_invitation": {
			"en": {"Interview invitation: {{.title}}", "Dear {{.candidate}}, we invite you to an interview for {{.title}} on {{.date}} ({{.type}}) at {{.location}}."},
			"id": {"Undangan wawancara: {{.title}}", "Yth. {{.candidate}}, kami mengundang Anda wawancara untuk posisi {{.title}} pada {{.date}} ({{.type}}) di {{.location}}."},
		},
		"hris.offer_decided": {
			"en": {"Offer {{.number}} {{.decision}}", "The offer {{.number}} to {{.candidate}} for {{.title}} was {{.decision}}. {{.reason}}"},
			"id": {"Penawaran {{.number}} {{.decision}}", "Penawaran {{.number}} kepada {{.candidate}} untuk posisi {{.title}} {{.decision}}. {{.reason}}"},
		},
		"hris.candidate_offer": {
			"en": {"Your offer for {{.title}}", "Dear {{.candidate}}, we are pleased to offer you the position of {{.title}} starting {{.startDate}}. Please reply before {{.expiresOn}}; HR will send you the offer letter."},
			"id": {"Penawaran kerja untuk {{.title}}", "Yth. {{.candidate}}, dengan senang hati kami menawarkan posisi {{.title}} mulai {{.startDate}}. Mohon tanggapi sebelum {{.expiresOn}}; HR akan mengirimkan surat penawaran."},
		},
		"hris.candidate_rejected": {
			"en": {"Your application for {{.title}}", "Dear {{.candidate}}, thank you for your interest in {{.title}}. We decided to continue with other candidates. {{.retention}}"},
			"id": {"Lamaran Anda untuk {{.title}}", "Yth. {{.candidate}}, terima kasih atas minat Anda pada posisi {{.title}}. Kami memutuskan untuk melanjutkan dengan kandidat lain. {{.retention}}"},
		},
		"hris.candidate_hired": {
			"en": {"New hire: {{.employeeName}} starts on {{.joinDate}}", "{{.employeeName}} ({{.employeeNo}}) joins as {{.title}} on {{.joinDate}}. Complete the onboarding checklist in HRIS → Employees."},
			"id": {"Karyawan baru: {{.employeeName}} mulai {{.joinDate}}", "{{.employeeName}} ({{.employeeNo}}) bergabung sebagai {{.title}} pada {{.joinDate}}. Lengkapi checklist onboarding di HRIS → Employees."},
		},
		"hris.review_assigned": {
			"en": {"Performance review: {{.cycle}}", "Your {{.cycle}} performance review is open. {{.step}} before {{.dueDate}} in Employee Self Service → My Reviews."},
			"id": {"Penilaian kinerja: {{.cycle}}", "Penilaian kinerja {{.cycle}} Anda telah dibuka. {{.step}} sebelum {{.dueDate}} di Employee Self Service → My Reviews."},
		},
		"hris.review_manager_due": {
			"en": {"Review {{.employeeName}}: {{.cycle}}", "{{.employeeName}} is ready for your manager review ({{.cycle}}). Complete it before {{.dueDate}} in Employee Self Service → Team Reviews."},
			"id": {"Nilai {{.employeeName}}: {{.cycle}}", "{{.employeeName}} siap Anda nilai ({{.cycle}}). Selesaikan sebelum {{.dueDate}} di Employee Self Service → Team Reviews."},
		},
		"hris.review_submitted": {
			"en": {"Ready for calibration: {{.employeeName}}", "{{.reviewer}} submitted the {{.cycle}} review of {{.employeeName}}. Calibrate it in HRIS → Performance Review."},
			"id": {"Siap dikalibrasi: {{.employeeName}}", "{{.reviewer}} telah mengirim penilaian {{.cycle}} untuk {{.employeeName}}. Kalibrasi di HRIS → Performance Review."},
		},
		"hris.review_reminder": {
			"en": {"Reminder: {{.cycle}} review due {{.dueDate}}", "{{.count}} performance review step(s) of {{.cycle}} wait for you. Due {{.dueDate}}."},
			"id": {"Pengingat: penilaian {{.cycle}} jatuh tempo {{.dueDate}}", "{{.count}} langkah penilaian kinerja {{.cycle}} menunggu Anda. Jatuh tempo {{.dueDate}}."},
		},
		"hris.review_completed": {
			"en": {"Your {{.cycle}} review result", "Your performance review {{.cycle}} is complete: {{.rating}} ({{.score}}). Open Employee Self Service → My Reviews to read and acknowledge it."},
			"id": {"Hasil penilaian {{.cycle}} Anda", "Penilaian kinerja {{.cycle}} Anda telah selesai: {{.rating}} ({{.score}}). Buka Employee Self Service → My Reviews untuk membaca dan mengonfirmasi."},
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
