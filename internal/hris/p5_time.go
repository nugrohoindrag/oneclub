package hris

// Time & attendance of PRD P5 (EP-06 Shift Scheduling, EP-07 Attendance,
// EP-08 Leave, Permission & Overtime): the public part other P5 areas build
// on — the domain events and payloads (docs/p5-contracts.md), the request
// statuses, the ESS sections of the area and the payroll queries
// (p5_time_payroll.go). The implementation (HTTP, jobs, devices, demo)
// lives in hris/hrtime.

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Domain events of time & attendance (PRD P5 §11).
const (
	EventSchedulePublished  = "hris.schedule_published"
	EventAttendanceRecorded = "hris.attendance_recorded"
	EventLeaveApproved      = "hris.leave_approved"
	EventOvertimeApproved   = "hris.overtime_approved"
)

// Attendance day statuses (PRD P5 §7.6 Present, Late, Absent, On Leave,
// Off; FR-ATT-03 Early Leave; Holiday; Scheduled while the day is open).
const (
	DayScheduled  = "scheduled"
	DayPresent    = "present"
	DayLate       = "late"
	DayEarlyLeave = "early_leave"
	DayAbsent     = "absent"
	DayOnLeave    = "on_leave"
	DayOff        = "off"
	DayHoliday    = "holiday"
)

// DayStatuses lists the attendance day statuses.
var DayStatuses = []string{DayScheduled, DayPresent, DayLate, DayEarlyLeave, DayAbsent, DayOnLeave, DayOff, DayHoliday}

// Request statuses of leave, permission, overtime and corrections (PRD P5
// §7.6: Submitted, Approved, Rejected, Cancelled).
const (
	RequestSubmitted = "submitted"
	RequestApproved  = "approved"
	RequestRejected  = "rejected"
	RequestCancelled = "cancelled"
)

// Attendance methods (FR-ATT-01/02).
const (
	MethodMobileGPS       = "mobile_gps"
	MethodKioskQR         = "kiosk_qr"
	MethodKioskPIN        = "kiosk_pin"
	MethodFingerprint     = "fingerprint"
	MethodFaceRecognition = "face_recognition"
	MethodManual          = "manual"
	MethodCorrection      = "correction"
)

// Attendance event flags.
const (
	FlagOutOfArea          = "out_of_area"
	FlagLowAccuracy        = "low_accuracy"
	FlagNoSchedule         = "no_schedule"
	FlagMissingOut         = "missing_out"
	FlagMissingIn          = "missing_in"
	FlagCorrected          = "corrected"
	FlagOffline            = "offline"
	FlagUnapprovedOvertime = "unapproved_overtime"
	FlagWorkedOnLeave      = "worked_on_leave"
	FlagPendingReview      = "pending_review"
)

// SchedulePublished is the payload of hris.schedule_published (FR-SCH-05),
// published when a Shift Schedule is published and again (Republished
// true) when a published schedule changes.
type SchedulePublished struct {
	ScheduleID     uuid.UUID   `json:"scheduleId"`
	PropertyID     uuid.UUID   `json:"propertyId"`
	OrgUnitID      uuid.UUID   `json:"orgUnitId"`
	OrgUnitCode    string      `json:"orgUnitCode"`
	OrgUnitName    string      `json:"orgUnitName"`
	Name           string      `json:"name"`
	PeriodStart    string      `json:"periodStart"`
	PeriodEnd      string      `json:"periodEnd"`
	Version        int         `json:"version" doc:"1 at the first publication, +1 per change after publishing"`
	Republished    bool        `json:"republished"`
	Shifts         int         `json:"shifts" doc:"Shift assignments (days off excluded)"`
	EmployeeIDs    []uuid.UUID `json:"employeeIds" doc:"Employees with an assignment (all of them on the first publication, the changed ones afterwards)"`
	ScheduledHours string      `json:"scheduledHours"`
}

// AttendanceDaySnapshot is the attendance day after an event.
type AttendanceDaySnapshot struct {
	Status            string  `json:"status" enum:"scheduled,present,late,early_leave,absent,on_leave,off,holiday"`
	FirstIn           *string `json:"firstIn"`
	LastOut           *string `json:"lastOut"`
	WorkedMinutes     int     `json:"workedMinutes"`
	LateMinutes       int     `json:"lateMinutes"`
	EarlyLeaveMinutes int     `json:"earlyLeaveMinutes"`
	OvertimeMinutes   int     `json:"overtimeMinutes"`
}

// AttendanceRecorded is the payload of hris.attendance_recorded, published
// for every clock-in / out accepted from ESS, the kiosk, a device or an
// approved correction (an event resubmitted offline is published once).
type AttendanceRecorded struct {
	EventID      uuid.UUID             `json:"eventId"`
	PropertyID   uuid.UUID             `json:"propertyId"`
	EmployeeID   uuid.UUID             `json:"employeeId"`
	EmployeeNo   string                `json:"employeeNo"`
	WorkDate     string                `json:"workDate"`
	Direction    string                `json:"direction" enum:"in,out"`
	OccurredAt   string                `json:"occurredAt"`
	Method       string                `json:"method" enum:"mobile_gps,kiosk_qr,kiosk_pin,fingerprint,face_recognition,manual,correction"`
	Source       string                `json:"source" enum:"ess,kiosk,device,hr,correction"`
	DeviceID     *uuid.UUID            `json:"deviceId"`
	Offline      bool                  `json:"offline"`
	Flags        []string              `json:"flags"`
	ReviewStatus string                `json:"reviewStatus" enum:"not_required,pending,accepted,rejected"`
	Day          AttendanceDaySnapshot `json:"day"`
}

// LeaveApproved is the payload of hris.leave_approved (FR-LVE-02).
type LeaveApproved struct {
	LeaveRequestID   uuid.UUID `json:"leaveRequestId"`
	Number           string    `json:"number"`
	PropertyID       uuid.UUID `json:"propertyId"`
	EmployeeID       uuid.UUID `json:"employeeId"`
	EmployeeNo       string    `json:"employeeNo"`
	FullName         string    `json:"fullName"`
	LeaveType        string    `json:"leaveType" doc:"Leave Policy code, e.g. ANNUAL, SICK, UNPAID"`
	LeaveTypeName    string    `json:"leaveTypeName"`
	Paid             bool      `json:"paid"`
	StartDate        string    `json:"startDate"`
	EndDate          string    `json:"endDate"`
	HalfDay          *string   `json:"halfDay" enum:"am,pm"`
	Days             string    `json:"days" doc:"Working days taken (decimal)"`
	BalanceYear      *int      `json:"balanceYear"`
	RemainingBalance *string   `json:"remainingBalance" doc:"Balance left after the leave (types with a balance)"`
	PolicyVersion    int       `json:"policyVersion"`
}

// OvertimeTier is the hours of one multiplier.
type OvertimeTier struct {
	Factor string          `json:"factor" doc:"Multiplier of the hourly wage, e.g. 1.5"`
	Hours  decimal.Decimal `json:"hours"`
}

// OvertimeApproved is the payload of hris.overtime_approved (FR-OVT-01/03).
// payableHours is the approved time capped by the attendance of the day
// (FR-OVT-02); it is 0 until the employee clocked the work (planned
// overtime) and follows later attendance.
type OvertimeApproved struct {
	OvertimeRequestID uuid.UUID      `json:"overtimeRequestId"`
	Number            string         `json:"number"`
	PropertyID        uuid.UUID      `json:"propertyId"`
	EmployeeID        uuid.UUID      `json:"employeeId"`
	EmployeeNo        string         `json:"employeeNo"`
	WorkDate          string         `json:"workDate"`
	Hours             string         `json:"hours" doc:"Approved hours"`
	PayableHours      string         `json:"payableHours"`
	DayKind           string         `json:"dayKind" enum:"workday,rest_day,shortest_day"`
	WorkWeekDays      int            `json:"workWeekDays"`
	Tiers             []OvertimeTier `json:"tiers"`
	MultipliedHours   string         `json:"multipliedHours"`
	PolicyVersion     int            `json:"policyVersion"`
}

func init() {
	for _, s := range []ESSSection{
		{Key: "schedule", Label: "My Schedule", LabelID: "Jadwal Saya", Icon: "calendar_month", Path: "/ops/ess/schedule", Order: 20, Offline: true},
		{Key: "clock", Label: "Clock In / Out", LabelID: "Absen Masuk / Pulang", Icon: "fingerprint", Path: "/ops/ess/clock", Order: 25, Offline: true},
		{Key: "attendance", Label: "Attendance History", LabelID: "Riwayat Kehadiran", Icon: "event_available", Path: "/ops/ess/attendance", Order: 30,
			Offline: true},
		{Key: "leave", Label: "Leave & Permission", LabelID: "Cuti & Izin", Icon: "beach_access", Path: "/ops/ess/leave", Order: 40},
		{Key: "overtime", Label: "Overtime", LabelID: "Lembur", Icon: "more_time", Path: "/ops/ess/overtime", Order: 50},
		{Key: "approvals", Label: "Approvals", LabelID: "Persetujuan", Icon: "fact_check", Path: "/ops/ess/approvals", Permission: PermissionTeamApprove,
			Manager: true, Order: 102},
		{Key: "team-schedule", Label: "Team Schedule", LabelID: "Jadwal Tim", Icon: "calendar_view_week", Path: "/ops/ess/team-schedule",
			Permission: PermissionTeam, Manager: true, Order: 104},
		{Key: "team-attendance", Label: "Team Attendance", LabelID: "Kehadiran Tim", Icon: "how_to_reg", Path: "/ops/ess/team-attendance",
			Permission: PermissionTeam, Manager: true, Order: 106},
	} {
		RegisterESSSection(s)
	}
}
