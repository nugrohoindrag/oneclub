package hrtime

// Catalogue contribution of time & attendance: permissions per object, the
// role templates (HR Admin / HR Manager everything; department heads
// schedule their units and approve their teams; the General Manager and
// Finance read), approval document types and the notification templates
// (ID/EN, FR-INT-P5-06).

import (
	"slices"
	"strings"

	"oneclub/internal/hris"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// Permission codes of time & attendance.
const (
	PermScheduleView      = "hris.schedule.view"
	PermScheduleCreate    = "hris.schedule.create"
	PermScheduleUpdate    = "hris.schedule.update"
	PermSchedulePublish   = "hris.schedule.publish"
	PermSwapView          = "hris.shift_swap.view"
	PermSwapApprove       = "hris.shift_swap.approve"
	PermAttendanceView    = "hris.attendance.view"
	PermAttendanceRecord  = "hris.attendance.record"
	PermAttendanceReview  = "hris.attendance.review"
	PermKiosk             = "hris.attendance.kiosk"
	PermProfileView       = "hris.attendance_profile.view"
	PermProfileManage     = "hris.attendance_profile.manage"
	PermCorrectionView    = "hris.attendance_correction.view"
	PermCorrectionCreate  = "hris.attendance_correction.create"
	PermCorrectionApprove = "hris.attendance_correction.approve"
	PermBalanceView       = "hris.leave_balance.view"
	PermBalanceAdjust     = "hris.leave_balance.adjust"
	PermBalanceImport     = "hris.leave_balance.import"
	PermLeaveView         = "hris.leave_request.view"
	PermLeaveCreate       = "hris.leave_request.create"
	PermLeaveApprove      = "hris.leave_request.approve"
	PermLeaveCancel       = "hris.leave_request.cancel"
	PermPermissionView    = "hris.permission_request.view"
	PermPermissionCreate  = "hris.permission_request.create"
	PermPermissionApprove = "hris.permission_request.approve"
	PermOvertimeView      = "hris.overtime_request.view"
	PermOvertimeCreate    = "hris.overtime_request.create"
	PermOvertimeApprove   = "hris.overtime_request.approve"
	PermLockView          = "hris.time_lock.view"
	PermLockManage        = "hris.time_lock.manage"
	// PermSalary (Core HR) shows the estimated overtime pay.
	PermSalary = "hris.contract.view_salary"
)

func customPermissions() []catalog.Permission {
	d := func(code, desc string) catalog.Permission { return catalog.Permission{Code: code, Description: desc} }
	return []catalog.Permission{
		d(PermScheduleView, "View shift schedules"), d(PermScheduleCreate, "Create shift schedules"),
		d(PermScheduleUpdate, "Edit shift schedules (assignments, copy, patterns)"), d(PermSchedulePublish, "Publish shift schedules to Employee Self Service"),
		d(PermSwapView, "View shift swaps"), d(PermSwapApprove, "Approve or reject shift swaps"),
		d(PermAttendanceView, "View attendance days and clock events"), d(PermAttendanceRecord, "Record attendance on behalf of employees and recalculate days"),
		d(PermAttendanceReview, "Review clock-ins flagged out of area or with low GPS accuracy"),
		d(PermKiosk, "Operate the Attendance Kiosk on a registered device"),
		d(PermProfileView, "View attendance profiles (device numbers, biometric consent)"),
		d(PermProfileManage, "Manage attendance profiles (kiosk PIN reset, biometric consent, device enrolment)"),
		d(PermCorrectionView, "View attendance corrections"), d(PermCorrectionCreate, "Request attendance corrections for employees"),
		d(PermCorrectionApprove, "Approve or reject attendance corrections"),
		d(PermBalanceView, "View leave balances"), d(PermBalanceAdjust, "Adjust leave balances and run the accrual"),
		d(PermBalanceImport, "Import leave balances (migration)"),
		d(PermLeaveView, "View leave requests and the leave calendar"), d(PermLeaveCreate, "Request leave for employees"),
		d(PermLeaveApprove, "Approve or reject leave requests"), d(PermLeaveCancel, "Cancel approved leave"),
		d(PermPermissionView, "View permission (izin) requests"), d(PermPermissionCreate, "Request permission for employees"),
		d(PermPermissionApprove, "Approve or reject permission requests"),
		d(PermOvertimeView, "View overtime requests and exceptions"), d(PermOvertimeCreate, "Request overtime for employees"),
		d(PermOvertimeApprove, "Approve or reject overtime requests"),
		d(PermLockView, "View payroll period locks and time summaries"), d(PermLockManage, "Lock and release payroll periods of attendance"),
		d(PermPartnerView, "View the device clock-in of partner caddies and instructors"),
		d(PermPartnerManage, "Enrol partner caddies and instructors for device clock-in (consent, device numbers)"),
	}
}

// Permissions of time & attendance.
func (m *Module) Permissions() []catalog.Permission {
	return append(resource.Permissions(m.Defs()...), customPermissions()...)
}

// managerRoles are the role templates of department heads (Core HR uses the
// same list): they schedule their units and approve their teams.
var managerRoles = []string{"department_head", "general_manager", "club_manager", "resort_manager", "finance_manager", "golf_manager",
	"sport_club_manager", "membership_manager", "banquet_manager", "event_manager", "outlet_manager", "inventory_manager", "procurement_manager",
	"caddy_manager", "hr_manager"}

// Contribution adds the permissions to the role templates.
func (m *Module) Contribution() catalog.Contribution {
	var all, views []string
	for _, p := range m.Permissions() {
		all = append(all, p.Code)
		if strings.HasSuffix(p.Code, ".view") {
			views = append(views, p.Code)
		}
	}
	scheduling := []string{PermScheduleView, PermScheduleCreate, PermScheduleUpdate, PermSchedulePublish, PermSwapView, PermAttendanceView,
		PermLeaveView, PermOvertimeView, PermPermissionView, PermCorrectionView, PermBalanceView, "hris.shift_template.view",
		"hris.staffing_requirement.view", "hris.holiday.view", PermKiosk}
	roles := map[string][]string{
		"hr_admin":        all,
		"hr_manager":      all,
		"general_manager": slices.Clone(views),
		"finance_manager": {PermAttendanceView, PermOvertimeView, PermLeaveView, PermBalanceView, PermLockView, PermLockManage, "hris.holiday.view"},
		"accountant":      {PermAttendanceView, PermOvertimeView, PermLockView},
		// The Property Admin may assign every staff role (FR-IAM-08): it keeps the permissions those roles carry.
		"property_admin": append(slices.Clone(scheduling), PermKiosk),
	}
	for _, r := range managerRoles {
		roles[r] = append(roles[r], scheduling...)
	}
	// FR-ATT-08: Caddy Master and Sport Club enrol their partners for device clock-in.
	for _, r := range []string{"caddy_manager", "golf_manager", "sport_club_manager"} {
		roles[r] = append(roles[r], PermPartnerView, PermPartnerManage)
	}
	for r := range roles {
		roles[r] = uniq(roles[r])
	}
	return catalog.Contribution{Permissions: m.Permissions(), RolePermissions: roles}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Approval document types: the configurable workflow after the manager
// levels (no workflow = approved at once).
var (
	LeaveDocumentType = provision.DocumentType{Code: "hris.leave_request", Module: hris.Module, Name: "Leave Request",
		Attributes: []provision.DocumentAttribute{{Key: "days", Label: "Days", Type: "number"}, {Key: "leaveType", Label: "Leave Type", Type: "string"},
			{Key: "paid", Label: "Paid", Type: "string"}, {Key: "orgUnit", Label: "Org Unit", Type: "string"}}}
	PermissionDocumentType = provision.DocumentType{Code: "hris.permission_request", Module: hris.Module, Name: "Permission Request",
		Attributes: []provision.DocumentAttribute{{Key: "hours", Label: "Hours", Type: "number"}, {Key: "permissionType", Label: "Permission Type", Type: "string"},
			{Key: "orgUnit", Label: "Org Unit", Type: "string"}}}
	OvertimeDocumentType = provision.DocumentType{Code: "hris.overtime_request", Module: hris.Module, Name: "Overtime Request",
		Attributes: []provision.DocumentAttribute{{Key: "hours", Label: "Hours", Type: "number"}, {Key: "dayKind", Label: "Day Kind", Type: "string"},
			{Key: "timing", Label: "Requested", Type: "string"}, {Key: "orgUnit", Label: "Org Unit", Type: "string"}}}
	SwapDocumentType = provision.DocumentType{Code: "hris.shift_swap", Module: hris.Module, Name: "Shift Swap",
		Attributes: []provision.DocumentAttribute{{Key: "orgUnit", Label: "Org Unit", Type: "string"}}}
	CorrectionDocumentType = provision.DocumentType{Code: "hris.attendance_correction", Module: hris.Module, Name: "Attendance Correction",
		Attributes: []provision.DocumentAttribute{{Key: "daysBack", Label: "Days Back", Type: "number"}, {Key: "orgUnit", Label: "Org Unit", Type: "string"}}}
)

// DocumentTypes are the approval document types of the area.
func DocumentTypes() []provision.DocumentType {
	return []provision.DocumentType{LeaveDocumentType, PermissionDocumentType, OvertimeDocumentType, SwapDocumentType, CorrectionDocumentType}
}

// Notification events.
const (
	NotifySchedulePublished = "hris.schedule_published"
	NotifyScheduleChanged   = "hris.schedule_changed"
	NotifyApprovalNeeded    = "hris.time_request_pending"
	NotifyRequestDecided    = "hris.time_request_decided"
	NotifySwapRequested     = "hris.shift_swap_requested"
	NotifyOutOfArea         = "hris.attendance_review"
)

// Templates are the notification templates (ID/EN; in-app, e-mail and
// WhatsApp for the ESS events, FR-ESS-07).
func Templates() []provision.Template {
	t := map[string]map[string][2]string{
		NotifySchedulePublished: {
			"en": {"Your schedule {{.periodStart}} – {{.periodEnd}} is published", "{{.orgUnit}} schedule {{.name}}: {{.shifts}} shifts for you from {{.periodStart}} to {{.periodEnd}}. Open My Schedule in Employee Self Service."},
			"id": {"Jadwal Anda {{.periodStart}} – {{.periodEnd}} telah terbit", "Jadwal {{.orgUnit}} {{.name}}: {{.shifts}} shift untuk Anda dari {{.periodStart}} sampai {{.periodEnd}}. Buka Jadwal Saya di Employee Self Service."},
		},
		NotifyScheduleChanged: {
			"en": {"Your schedule changed on {{.dates}}", "The {{.orgUnit}} schedule {{.name}} changed for you on {{.dates}}. Check My Schedule."},
			"id": {"Jadwal Anda berubah pada {{.dates}}", "Jadwal {{.orgUnit}} {{.name}} berubah untuk Anda pada {{.dates}}. Periksa Jadwal Saya."},
		},
		NotifyApprovalNeeded: {
			"en": {"{{.kind}} {{.number}} from {{.employeeName}} needs your approval", "{{.employeeName}} ({{.employeeNo}}) requests {{.kind}}: {{.summary}}. Approve or reject it in Approvals."},
			"id": {"{{.kind}} {{.number}} dari {{.employeeName}} menunggu persetujuan Anda", "{{.employeeName}} ({{.employeeNo}}) mengajukan {{.kind}}: {{.summary}}. Setujui atau tolak di Persetujuan."},
		},
		NotifyRequestDecided: {
			"en": {"Your {{.kind}} {{.number}} was {{.status}}", "Your {{.kind}} ({{.summary}}) was {{.status}}. {{.note}}"},
			"id": {"{{.kind}} {{.number}} Anda {{.status}}", "{{.kind}} Anda ({{.summary}}) {{.status}}. {{.note}}"},
		},
		NotifySwapRequested: {
			"en": {"{{.requesterName}} asks to swap a shift with you", "{{.requesterName}} asks you to take the shift of {{.date}} ({{.shift}}){{.giveBack}}. Accept or decline in My Schedule."},
			"id": {"{{.requesterName}} meminta tukar shift dengan Anda", "{{.requesterName}} meminta Anda mengambil shift {{.date}} ({{.shift}}){{.giveBack}}. Terima atau tolak di Jadwal Saya."},
		},
		NotifyOutOfArea: {
			"en": {"Clock-in to review: {{.employeeName}}", "{{.employeeName}} clocked {{.direction}} at {{.time}} {{.distance}} m from {{.geofence}} ({{.reason}}). Review it in Team Attendance."},
			"id": {"Absensi perlu ditinjau: {{.employeeName}}", "{{.employeeName}} absen {{.direction}} pukul {{.time}} berjarak {{.distance}} m dari {{.geofence}} ({{.reason}}). Tinjau di Kehadiran Tim."},
		},
	}
	whatsApp := map[string]bool{NotifySchedulePublished: true, NotifyScheduleChanged: true, NotifyRequestDecided: true, NotifySwapRequested: true}
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
