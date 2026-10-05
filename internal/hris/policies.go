package hris

// HR & Workforce Policies (PRD P5 EP-24, labels of §7.6 approved by §16
// #16): HR Configuration, Payroll Configuration and Attendance
// Configuration (Settings, Naming Convention §33 pattern) and the HR
// Policies Leave Policy, Overtime Policy, Attendance Policy and Service
// Charge Policy (Settings → Club Policies). Every value is configurable and
// versioned with an effective date through the P0 policy framework
// (platform.rules); the code defaults below follow PRD P5 §16 #3 (UU Cipta
// Kerja & PP 35/2021), #4, #5, #7, #9 and #10 and must be verified by the
// club's tax / legal consultant before go-live (§6 #18, FR-TAX-HR-05).
//
// Every payroll or payout run stores the PolicyRef (code + version) the
// loaders return (FR-POL-P5-07).

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes and categories.
const (
	HRConfigurationCode         = "hris.hr_configuration"
	PayrollConfigurationCode    = "hris.payroll_configuration"
	AttendanceConfigurationCode = "hris.attendance_configuration"
	LeavePolicyCode             = "hris.leave_policy"
	OvertimePolicyCode          = "hris.overtime_policy"
	AttendancePolicyCode        = "hris.attendance_policy"
	ServiceChargePolicyCode     = "hris.service_charge_policy"

	CategoryHRConfiguration         = "HR Configuration"
	CategoryPayrollConfiguration    = "Payroll Configuration"
	CategoryAttendanceConfiguration = "Attendance Configuration"
	CategoryHRPolicies              = "HR Policies"
)

// PolicyCodes lists every HR policy code (H6 seed for provisioning).
var PolicyCodes = []string{HRConfigurationCode, PayrollConfigurationCode, AttendanceConfigurationCode, LeavePolicyCode, OvertimePolicyCode,
	AttendancePolicyCode, ServiceChargePolicyCode}

// ── HR Configuration ─────────────────────────────────────────────────────

// ChecklistItem is one offboarding checklist entry (FR-HR-06).
type ChecklistItem struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// DataRetention follows PRD P5 §16 #10.
type DataRetention struct {
	ApplicantMonths        int `json:"applicantMonths" doc:"Rejected applicants are erased after N months (talent pool consent)"`
	PayrollYears           int `json:"payrollYears" doc:"Payroll, tax, BPJS and supporting attendance records are kept N years"`
	BiometricDaysAfterExit int `json:"biometricDaysAfterExit" doc:"Device biometric templates are deleted within N days after the employee leaves"`
}

// HRConfiguration is the HR Configuration (Settings).
type HRConfiguration struct {
	EmployeeNumberPrefix        string          `json:"employeeNumberPrefix" doc:"Prefix of generated employee numbers"`
	EmployeeNumberDigits        int             `json:"employeeNumberDigits"`
	ContractNumberPrefix        string          `json:"contractNumberPrefix" doc:"Contract numbers PREFIX-YYYY-NNNNN"`
	PKWTMaxMonths               int             `json:"pkwtMaxMonths" doc:"Maximum total PKWT duration including renewals (PP 35/2021: 5 years)"`
	PKWTMaxRenewals             int             `json:"pkwtMaxRenewals" doc:"Maximum PKWT renewals (0 = only the total duration applies)"`
	PKWTCompensationPer12Months string          `json:"pkwtCompensationPer12Months" doc:"PKWT end compensation in months of wage per 12 months of service, proportional"`
	ProbationMaxMonths          int             `json:"probationMaxMonths" doc:"Probation is only allowed on PKWTT, at most N months"`
	ContractReminderDays        []int           `json:"contractReminderDays" doc:"Remind HR and the supervisor N days before a PKWT ends"`
	DocumentReminderDays        []int           `json:"documentReminderDays" doc:"Remind HR N days before an employee document expires"`
	CertificationReminderDays   []int           `json:"certificationReminderDays" doc:"Default reminders before a certificate expires (a certification type may override)"`
	CertificationEnforcement    string          `json:"certificationEnforcement" enum:"expired,required,off" doc:"expired: holders whose mandatory certificate expired are refused; required: a valid certificate must exist; off: no check"`
	OffboardingChecklist        []ChecklistItem `json:"offboardingChecklist"`
	SelfServiceRole             string          `json:"selfServiceRole" doc:"Role assigned to new employee accounts at onboarding"`
	ProfileChangeFields         []string        `json:"profileChangeFields" doc:"Personal data employees may change in Employee Self Service (verified by HR)"`
	Retention                   DataRetention   `json:"retention"`
}

// NewHRConfiguration returns the HR Configuration defaults (PRD P5 §16 #3,
// #9, #10). A fresh value is built on every call.
func NewHRConfiguration() HRConfiguration {
	return HRConfiguration{
		EmployeeNumberPrefix: "EMP", EmployeeNumberDigits: 5, ContractNumberPrefix: "CTR",
		PKWTMaxMonths: 60, PKWTMaxRenewals: 0, PKWTCompensationPer12Months: "1", ProbationMaxMonths: 3,
		ContractReminderDays: []int{30, 7}, DocumentReminderDays: []int{30}, CertificationReminderDays: []int{60, 30},
		CertificationEnforcement: "expired",
		OffboardingChecklist: []ChecklistItem{
			{Code: "handover", Label: "Work handover completed"},
			{Code: "return_assets", Label: "Company assets returned (uniform, ID card, keys, devices)"},
			{Code: "revoke_access", Label: "System access revoked"},
			{Code: "biometric_removal", Label: "Biometric templates removed from attendance devices"},
			{Code: "final_settlement", Label: "Final settlement calculated (remaining salary, leave, compensation)"},
			{Code: "exit_interview", Label: "Exit interview"},
		},
		SelfServiceRole: "employee_self_service",
		ProfileChangeFields: []string{"phone", "personalEmail", "address", "city", "postalCode", "maritalStatus", "ptkpStatus", "emergencyContacts",
			"bankAccount"},
		Retention: DataRetention{ApplicantMonths: 12, PayrollYears: 10, BiometricDaysAfterExit: 30},
	}
}

// ── Payroll Configuration ────────────────────────────────────────────────

// TaxBracket is one bracket of a rate table: income up to UpTo ("" = no
// upper limit) is taxed at Rate percent.
type TaxBracket struct {
	UpTo string `json:"upTo"`
	Rate string `json:"rate"`
}

// PPh21Config holds the PPh 21 method and tables (FR-TAX-HR-01/02).
type PPh21Config struct {
	Method                         string                  `json:"method" enum:"ter" doc:"ter: monthly effective rate (PP 58/2023) and the annual calculation in the last period"`
	TERCategories                  map[string]string       `json:"terCategories" doc:"PTKP status → TER category A / B / C"`
	TERRates                       map[string][]TaxBracket `json:"terRates" doc:"Monthly gross income brackets per TER category"`
	ProgressiveRates               []TaxBracket            `json:"progressiveRates" doc:"Article 17 rates on annual taxable income"`
	OccupationalCostPercent        string                  `json:"occupationalCostPercent" doc:"Biaya jabatan %"`
	OccupationalCostMaxAnnual      string                  `json:"occupationalCostMaxAnnual"`
	NonNPWPSurchargePercent        string                  `json:"nonNpwpSurchargePercent" doc:"Surcharge when the employee has no NPWP / registered NIK"`
	NonEmployeeTaxableSharePercent string                  `json:"nonEmployeeTaxableSharePercent" doc:"PPh 21 non-employee: Article 17 rate × this share of gross income (caddies, partner instructors, PRD P5 §16 #4)"`
}

// ContributionRate is one BPJS programme: employer and employee shares of
// the wage up to WageCap ("" = no cap).
type ContributionRate struct {
	EmployerPercent string `json:"employerPercent"`
	EmployeePercent string `json:"employeePercent"`
	WageCap         string `json:"wageCap"`
}

// BPUConfig is BPJS Ketenagakerjaan for partners (Bukan Penerima Upah:
// JKK & JKM, PRD P5 §16 #4), deducted from the payout.
type BPUConfig struct {
	JKKPercent string `json:"jkkPercent" doc:"JKK % of the declared income"`
	JKMAmount  string `json:"jkmAmount" doc:"JKM per month"`
}

// BPJSConfig holds the BPJS rates (FR-TAX-HR-03).
type BPJSConfig struct {
	Kesehatan ContributionRate `json:"kesehatan"`
	JHT       ContributionRate `json:"jht"`
	JP        ContributionRate `json:"jp"`
	JKK       ContributionRate `json:"jkk" doc:"Employer only; the rate depends on the risk class"`
	JKM       ContributionRate `json:"jkm" doc:"Employer only"`
	Partner   BPUConfig        `json:"partner"`
}

// THRConfig is the religious holiday allowance (FR-PAY-04).
type THRConfig struct {
	FullAfterMonths  int    `json:"fullAfterMonths" doc:"Service of N months or more earns 1× wage"`
	MinServiceMonths int    `json:"minServiceMonths" doc:"Below FullAfterMonths the THR is proportional from this service"`
	Basis            string `json:"basis" enum:"base,base_plus_fixed_allowances"`
}

// ServiceStep maps years of service to months of wage.
type ServiceStep struct {
	MinYears int    `json:"minYears"`
	Months   string `json:"months"`
}

// SeveranceConfig is the termination pay table (PP 35/2021, FR-PAY-07).
type SeveranceConfig struct {
	SeveranceMonths    []ServiceStep     `json:"severanceMonths" doc:"Uang pesangon"`
	ServiceAwardMonths []ServiceStep     `json:"serviceAwardMonths" doc:"Uang penghargaan masa kerja"`
	Multipliers        map[string]string `json:"multipliers" doc:"Termination reason → multiplier of the severance pay"`
}

// PayComponent is a salary component (FR-PAY-01).
type PayComponent struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Kind    string `json:"kind" enum:"earning,deduction"`
	Fixed   bool   `json:"fixed" doc:"Fixed components count in the THR, BPJS and overtime wage basis"`
	Taxable bool   `json:"taxable"`
}

// PartnerPayout is the payout schedule of partners (PRD P5 §16 #4).
type PartnerPayout struct {
	CaddySchedule      string `json:"caddySchedule" enum:"semi_monthly,monthly" doc:"semi_monthly: the 15th and the end of the month"`
	InstructorSchedule string `json:"instructorSchedule" enum:"semi_monthly,monthly"`
}

// PayrollConfiguration is the Payroll Configuration (Settings, FR-POL-P5-04).
type PayrollConfiguration struct {
	Currency       string            `json:"currency"`
	PeriodStartDay int               `json:"periodStartDay" doc:"Payroll period starts on this day of the month (1 = calendar month)"`
	PayDay         int               `json:"payDay" doc:"Day of the month salaries are paid"`
	ProrationBasis string            `json:"prorationBasis" enum:"calendar_days,working_days" doc:"Proration of joiners and leavers (FR-PAY-03)"`
	RoundingUnit   string            `json:"roundingUnit" doc:"Net pay and contributions are rounded to this unit"`
	Components     []PayComponent    `json:"components"`
	PTKP           map[string]string `json:"ptkp" doc:"Annual PTKP per status"`
	PPh21          PPh21Config       `json:"pph21"`
	BPJS           BPJSConfig        `json:"bpjs"`
	THR            THRConfig         `json:"thr"`
	Severance      SeveranceConfig   `json:"severance"`
	PartnerPayout  PartnerPayout     `json:"partnerPayout"`
}

func brackets(pairs ...string) []TaxBracket {
	out := make([]TaxBracket, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, TaxBracket{UpTo: pairs[i], Rate: pairs[i+1]})
	}
	return out
}

// NewPayrollConfiguration returns the Payroll Configuration defaults: PTKP
// (PMK 101/2016), PPh 21 TER (PP 58/2023) and Article 17 rates (UU HPP),
// BPJS rates of the PRD P5 EP-10 acceptance criteria, THR (§16 #3),
// severance (PP 35/2021) and the partner rules of §16 #4. They must be
// verified by the tax consultant before go-live (FR-TAX-HR-05).
func NewPayrollConfiguration() PayrollConfiguration {
	return PayrollConfiguration{
		Currency: "IDR", PeriodStartDay: 1, PayDay: 25, ProrationBasis: "calendar_days", RoundingUnit: "1",
		Components: []PayComponent{
			{Code: "BASIC", Name: "Basic Salary", Kind: "earning", Fixed: true, Taxable: true},
			{Code: "POSITION", Name: "Position Allowance", Kind: "earning", Fixed: true, Taxable: true},
			{Code: "MEAL", Name: "Meal Allowance", Kind: "earning", Taxable: true},
			{Code: "TRANSPORT", Name: "Transport Allowance", Kind: "earning", Taxable: true},
			{Code: "OVERTIME", Name: "Overtime", Kind: "earning", Taxable: true},
			{Code: "SERVICE_CHARGE", Name: "Service Charge", Kind: "earning", Taxable: true},
			{Code: "COMMISSION", Name: "Sales Commission", Kind: "earning", Taxable: true},
			{Code: "BONUS", Name: "Bonus", Kind: "earning", Taxable: true},
			{Code: "THR", Name: "THR", Kind: "earning", Taxable: true},
			{Code: "LOAN", Name: "Loan Installment", Kind: "deduction"},
			{Code: "CASH_ADVANCE", Name: "Cash Advance", Kind: "deduction"},
			{Code: "UNPAID_LEAVE", Name: "Unpaid Leave", Kind: "deduction"},
		},
		PTKP: map[string]string{"TK/0": "54000000", "TK/1": "58500000", "TK/2": "63000000", "TK/3": "67500000",
			"K/0": "58500000", "K/1": "63000000", "K/2": "67500000", "K/3": "72000000"},
		PPh21: PPh21Config{
			Method: "ter",
			TERCategories: map[string]string{"TK/0": "A", "TK/1": "A", "K/0": "A", "TK/2": "B", "TK/3": "B", "K/1": "B", "K/2": "B",
				"K/3": "C"},
			TERRates: map[string][]TaxBracket{
				"A": brackets("5400000", "0", "5650000", "0.25", "5950000", "0.5", "6300000", "0.75", "6750000", "1", "7500000", "1.25",
					"8550000", "1.5", "9650000", "1.75", "10050000", "2", "10350000", "2.25", "10700000", "2.5", "11050000", "3", "11600000", "3.5",
					"12500000", "4", "13750000", "5", "15100000", "6", "16950000", "7", "19750000", "8", "24150000", "9", "26450000", "10",
					"28000000", "11", "30050000", "12", "32400000", "13", "35400000", "14", "39100000", "15", "43850000", "16", "47800000", "17",
					"51400000", "18", "56300000", "19", "62200000", "20", "68600000", "21", "77500000", "22", "89000000", "23", "103000000", "24",
					"125000000", "25", "157000000", "26", "206000000", "27", "337000000", "28", "454000000", "29", "550000000", "30",
					"695000000", "31", "910000000", "32", "1400000000", "33", "", "34"),
				"B": brackets("6200000", "0", "6500000", "0.25", "6850000", "0.5", "7300000", "0.75", "9200000", "1", "10750000", "1.5",
					"11250000", "2", "11600000", "2.5", "12600000", "3", "13600000", "4", "14950000", "5", "16400000", "6", "18450000", "7",
					"21850000", "8", "26000000", "9", "27700000", "10", "29350000", "11", "31450000", "12", "33950000", "13", "37100000", "14",
					"41100000", "15", "45800000", "16", "49500000", "17", "53800000", "18", "58500000", "19", "64000000", "20", "71000000", "21",
					"80000000", "22", "93000000", "23", "109000000", "24", "129000000", "25", "163000000", "26", "211000000", "27",
					"374000000", "28", "459000000", "29", "555000000", "30", "704000000", "31", "957000000", "32", "1405000000", "33", "", "34"),
				"C": brackets("6600000", "0", "6950000", "0.25", "7350000", "0.5", "7800000", "0.75", "8850000", "1", "9800000", "1.25",
					"10950000", "1.5", "11200000", "1.75", "12050000", "2", "12950000", "3", "14150000", "4", "15550000", "5", "17050000", "6",
					"19500000", "7", "22700000", "8", "26600000", "9", "28100000", "10", "30100000", "11", "32600000", "12", "35400000", "13",
					"38900000", "14", "43000000", "15", "47400000", "16", "51200000", "17", "55800000", "18", "60400000", "19", "66700000", "20",
					"74500000", "21", "83200000", "22", "95600000", "23", "110000000", "24", "134000000", "25", "169000000", "26", "221000000", "27",
					"390000000", "28", "463000000", "29", "561000000", "30", "709000000", "31", "965000000", "32", "1419000000", "33", "", "34"),
			},
			ProgressiveRates:          brackets("60000000", "5", "250000000", "15", "500000000", "25", "5000000000", "30", "", "35"),
			OccupationalCostPercent:   "5",
			OccupationalCostMaxAnnual: "6000000", NonNPWPSurchargePercent: "20", NonEmployeeTaxableSharePercent: "50",
		},
		BPJS: BPJSConfig{
			Kesehatan: ContributionRate{EmployerPercent: "4", EmployeePercent: "1", WageCap: "12000000"},
			JHT:       ContributionRate{EmployerPercent: "3.7", EmployeePercent: "2"},
			JP:        ContributionRate{EmployerPercent: "2", EmployeePercent: "1", WageCap: "10547400"},
			JKK:       ContributionRate{EmployerPercent: "0.24", EmployeePercent: "0"},
			JKM:       ContributionRate{EmployerPercent: "0.3", EmployeePercent: "0"},
			Partner:   BPUConfig{JKKPercent: "1", JKMAmount: "6800"},
		},
		THR: THRConfig{FullAfterMonths: 12, MinServiceMonths: 1, Basis: "base_plus_fixed_allowances"},
		Severance: SeveranceConfig{
			SeveranceMonths:    []ServiceStep{{0, "1"}, {1, "2"}, {2, "3"}, {3, "4"}, {4, "5"}, {5, "6"}, {6, "7"}, {7, "8"}, {8, "9"}},
			ServiceAwardMonths: []ServiceStep{{3, "2"}, {6, "3"}, {9, "4"}, {12, "5"}, {15, "6"}, {18, "7"}, {21, "8"}, {24, "10"}},
			Multipliers: map[string]string{"terminated": "1", "efficiency": "1", "company_closure": "1", "retired": "1.75", "deceased": "2",
				"resigned": "0", "serious_misconduct": "0", "long_illness": "2"},
		},
		PartnerPayout: PartnerPayout{CaddySchedule: "semi_monthly", InstructorSchedule: "monthly"},
	}
}

// ── Attendance Configuration ─────────────────────────────────────────────

// AttendanceConfiguration is the Attendance Configuration (Settings).
type AttendanceConfiguration struct {
	Methods                       []string `json:"methods" doc:"Enabled clock-in methods: fingerprint, face_recognition, mobile_gps, kiosk_qr, kiosk_pin"`
	DevicePoints                  []string `json:"devicePoints" doc:"Attendance device locations (PRD P5 §16 #7)"`
	MobileGPSOrgUnits             []string `json:"mobileGpsOrgUnits" doc:"Org unit codes whose field staff clock in with mobile GPS"`
	DefaultGeofenceRadiusMeters   int      `json:"defaultGeofenceRadiusMeters"`
	MinimumGPSAccuracyMeters      int      `json:"minimumGpsAccuracyMeters" doc:"Mobile clock-ins with a worse GPS accuracy are flagged for review"`
	BiometricConsentRequired      bool     `json:"biometricConsentRequired" doc:"Written consent before enrolment on a biometric device; templates stay on the device (PRD P5 §6 #9)"`
	OfflineSyncMaxHours           int      `json:"offlineSyncMaxHours" doc:"Offline clock-ins older than N hours need a correction instead"`
	WorkWeekDays                  int      `json:"workWeekDays" enum:"5,6"`
	StandardDailyHours            string   `json:"standardDailyHours"`
	StandardWeeklyHours           string   `json:"standardWeeklyHours"`
	ShiftScheduleRequiredForClock bool     `json:"shiftScheduleRequiredForClock" doc:"Clock-ins without a published shift are flagged for review"`
}

// NewAttendanceConfiguration returns the defaults of PRD P5 §16 #7.
func NewAttendanceConfiguration() AttendanceConfiguration {
	return AttendanceConfiguration{
		Methods: []string{"fingerprint", "face_recognition", "mobile_gps", "kiosk_qr", "kiosk_pin"},
		DevicePoints: []string{"Staff entrance clubhouse", "Caddy house / golf operations", "Sport club", "Back of house F&B", "Banquet",
			"Engineering & warehouse"},
		MobileGPSOrgUnits:           []string{"COURSE-MAINT", "SALES"},
		DefaultGeofenceRadiusMeters: 300, MinimumGPSAccuracyMeters: 100, BiometricConsentRequired: true, OfflineSyncMaxHours: 72,
		WorkWeekDays: 5, StandardDailyHours: "8", StandardWeeklyHours: "40", ShiftScheduleRequiredForClock: false,
	}
}

// ── HR Policies ──────────────────────────────────────────────────────────

// LeaveType is one leave type of the Leave Policy (FR-LVE-01).
type LeaveType struct {
	Code                string `json:"code"`
	Name                string `json:"name"`
	Paid                bool   `json:"paid"`
	Days                int    `json:"days" doc:"Entitlement per year (annual) or per event; 0 = as approved"`
	Accrual             string `json:"accrual" enum:"annual,per_event,none" doc:"annual: balance granted per service year; per_event: per occurrence"`
	EligibleAfterMonths int    `json:"eligibleAfterMonths"`
	RequiresDocument    bool   `json:"requiresDocument" doc:"e.g. a doctor's letter for sick leave"`
	Gender              string `json:"gender,omitempty" enum:"male,female" doc:"Only for employees of this gender"`
	CountsWorkingDays   bool   `json:"countsWorkingDays" doc:"Only working days count against the balance"`
}

// PermissionType is a short permission (izin) by hours.
type PermissionType struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Paid     bool   `json:"paid"`
	MaxHours string `json:"maxHours"`
}

// LeavePolicy is the Leave Policy (FR-POL-P5-02, PRD P5 §16 #3).
type LeavePolicy struct {
	LeaveTypes           []LeaveType      `json:"leaveTypes"`
	PermissionTypes      []PermissionType `json:"permissionTypes"`
	CarryOverMaxDays     int              `json:"carryOverMaxDays" doc:"Annual leave days carried into the next year"`
	CarryOverExpiry      string           `json:"carryOverExpiry" doc:"MM-DD: carried-over days lapse after this date"`
	AllowNegativeBalance bool             `json:"allowNegativeBalance"`
	MinNoticeDays        int              `json:"minNoticeDays" doc:"Annual leave must be requested N days ahead"`
	ApprovalLevels       []string         `json:"approvalLevels" doc:"Approval chain: supervisor, department_head, hr"`
}

// NewLeavePolicy returns the Leave Policy defaults (UU 13/2003 as amended
// by UU Cipta Kerja; PRD P5 §16 #3).
func NewLeavePolicy() LeavePolicy {
	return LeavePolicy{
		LeaveTypes: []LeaveType{
			{Code: "ANNUAL", Name: "Annual Leave", Paid: true, Days: 12, Accrual: "annual", EligibleAfterMonths: 12, CountsWorkingDays: true},
			{Code: "SICK", Name: "Sick Leave", Paid: true, Accrual: "none", RequiresDocument: true, CountsWorkingDays: true},
			{Code: "MATERNITY", Name: "Maternity Leave", Paid: true, Days: 90, Accrual: "per_event", Gender: "female"},
			{Code: "MISCARRIAGE", Name: "Miscarriage Leave", Paid: true, Days: 45, Accrual: "per_event", Gender: "female", RequiresDocument: true},
			{Code: "MARRIAGE", Name: "Marriage Leave", Paid: true, Days: 3, Accrual: "per_event", CountsWorkingDays: true},
			{Code: "CHILD_MARRIAGE", Name: "Child's Marriage", Paid: true, Days: 2, Accrual: "per_event", CountsWorkingDays: true},
			{Code: "CHILD_CIRCUMCISION", Name: "Child's Circumcision / Baptism", Paid: true, Days: 2, Accrual: "per_event", CountsWorkingDays: true},
			{Code: "PATERNITY", Name: "Wife Gives Birth / Miscarriage", Paid: true, Days: 2, Accrual: "per_event", Gender: "male", CountsWorkingDays: true},
			{Code: "BEREAVEMENT", Name: "Bereavement (spouse, parent, child, in-law)", Paid: true, Days: 2, Accrual: "per_event", CountsWorkingDays: true},
			{Code: "BEREAVEMENT_HOUSEHOLD", Name: "Bereavement (household member)", Paid: true, Days: 1, Accrual: "per_event", CountsWorkingDays: true},
			{Code: "LONG_SERVICE", Name: "Long Service Leave", Paid: true, Days: 30, Accrual: "per_event", EligibleAfterMonths: 72, CountsWorkingDays: true},
			{Code: "UNPAID", Name: "Unpaid Leave", Paid: false, Accrual: "none", CountsWorkingDays: true},
		},
		PermissionTypes: []PermissionType{
			{Code: "LATE_ARRIVAL", Name: "Late Arrival", Paid: true, MaxHours: "2"},
			{Code: "EARLY_LEAVE", Name: "Early Leave", Paid: true, MaxHours: "2"},
			{Code: "PERSONAL", Name: "Personal Matter", Paid: false, MaxHours: "4"},
		},
		CarryOverMaxDays: 6, CarryOverExpiry: "03-31", AllowNegativeBalance: false, MinNoticeDays: 3,
		ApprovalLevels: []string{"supervisor"},
	}
}

// OvertimeStep multiplies hours FromHour..ToHour (1-based, ToHour 0 = no
// upper limit) of one day by Factor.
type OvertimeStep struct {
	FromHour int    `json:"fromHour"`
	ToHour   int    `json:"toHour"`
	Factor   string `json:"factor"`
}

// OvertimePolicy is the Overtime Policy (FR-POL-P5-03, FR-OVT-01–03).
type OvertimePolicy struct {
	HourlyDivisor         string         `json:"hourlyDivisor" doc:"Hourly wage = monthly wage ÷ divisor"`
	RequireApproval       bool           `json:"requireApproval" doc:"Only approved overtime is paid"`
	AllowAfterTheFact     bool           `json:"allowAfterTheFact" doc:"Overtime may be requested afterwards with a reason"`
	AfterTheFactDays      int            `json:"afterTheFactDays"`
	MaxHoursPerDay        string         `json:"maxHoursPerDay"`
	MaxHoursPerWeek       string         `json:"maxHoursPerWeek"`
	MinimumMinutes        int            `json:"minimumMinutes" doc:"Shorter overtime is not counted"`
	RoundingMinutes       int            `json:"roundingMinutes" doc:"Overtime minutes are rounded down to this unit"`
	Workday               []OvertimeStep `json:"workday"`
	RestDayFiveDayWeek    []OvertimeStep `json:"restDayFiveDayWeek" doc:"Rest day / public holiday, 5-day work week"`
	RestDaySixDayWeek     []OvertimeStep `json:"restDaySixDayWeek" doc:"Rest day / public holiday, 6-day work week"`
	ShortestDaySixDayWeek []OvertimeStep `json:"shortestDaySixDayWeek" doc:"Public holiday on the shortest working day, 6-day work week"`
}

// NewOvertimePolicy returns the PP 35/2021 defaults of PRD P5 §16 #3.
func NewOvertimePolicy() OvertimePolicy {
	return OvertimePolicy{
		HourlyDivisor: "173", RequireApproval: true, AllowAfterTheFact: true, AfterTheFactDays: 3, MaxHoursPerDay: "4", MaxHoursPerWeek: "18",
		MinimumMinutes: 30, RoundingMinutes: 30,
		Workday:               []OvertimeStep{{1, 1, "1.5"}, {2, 0, "2"}},
		RestDayFiveDayWeek:    []OvertimeStep{{1, 8, "2"}, {9, 9, "3"}, {10, 0, "4"}},
		RestDaySixDayWeek:     []OvertimeStep{{1, 7, "2"}, {8, 8, "3"}, {9, 0, "4"}},
		ShortestDaySixDayWeek: []OvertimeStep{{1, 5, "2"}, {6, 6, "3"}, {7, 0, "4"}},
	}
}

// AttendancePolicy is the Attendance Policy (FR-POL-P5-01).
type AttendancePolicy struct {
	LateToleranceMinutes       int                 `json:"lateToleranceMinutes" doc:"Clock-in up to N minutes after the shift start is Present"`
	EarlyLeaveToleranceMinutes int                 `json:"earlyLeaveToleranceMinutes"`
	AbsentAfterMinutes         int                 `json:"absentAfterMinutes" doc:"No clock-in N minutes after the shift start marks the day Absent"`
	GeofenceRadiusMeters       int                 `json:"geofenceRadiusMeters" doc:"Default radius of a location geofence"`
	OutOfAreaAction            string              `json:"outOfAreaAction" enum:"review,reject" doc:"Mobile clock-in outside the geofence: flag for supervisor review or reject"`
	MethodsByLocation          map[string][]string `json:"methodsByLocation" doc:"Location / org unit code → allowed clock-in methods (empty: Attendance Configuration)"`
	CorrectionRequiresApproval bool                `json:"correctionRequiresApproval"`
	CorrectionWindowDays       int                 `json:"correctionWindowDays" doc:"Corrections may be requested up to N days back"`
}

// NewAttendancePolicy returns the Attendance Policy defaults (PRD P5 §16 #7
// geofence 300 m; EP-07 AC: outside the geofence → review queue).
func NewAttendancePolicy() AttendancePolicy {
	return AttendancePolicy{LateToleranceMinutes: 10, EarlyLeaveToleranceMinutes: 10, AbsentAfterMinutes: 240, GeofenceRadiusMeters: 300,
		OutOfAreaAction: "review", MethodsByLocation: map[string][]string{}, CorrectionRequiresApproval: true, CorrectionWindowDays: 7}
}

// ServiceChargePolicy is the Service Charge Policy, the distribution rule
// of EP-11 (FR-POL-P5-05, PRD P5 §16 #5). The pool and its reserve are
// booked by Accounting (P4 accounting.service_charge, contract H4).
type ServiceChargePolicy struct {
	DistributedPercent      string            `json:"distributedPercent" doc:"Share of the pool distributed to employees"`
	ReservePercent          string            `json:"reservePercent" doc:"Breakage & loss reserve"`
	Method                  string            `json:"method" enum:"equal,points" doc:"equal per eligible employee or by grade points"`
	AttendanceFactor        bool              `json:"attendanceFactor" doc:"Share × days present ÷ working days"`
	RedistributeForfeited   bool              `json:"redistributeForfeited" doc:"The part lost to the attendance factor is redistributed by the same rule"`
	EligibleStatuses        []string          `json:"eligibleStatuses"`
	ExcludeProbation        bool              `json:"excludeProbation"`
	ExcludeWorkerCategories []string          `json:"excludeWorkerCategories"`
	ExcludeWarningLevelFrom int               `json:"excludeWarningLevelFrom" doc:"Employees with an active warning letter of this level or higher are not eligible (0 = off)"`
	ExcludeFullPeriodUnpaid bool              `json:"excludeFullPeriodUnpaid" doc:"Unpaid leave during the whole period"`
	DepartmentShares        map[string]string `json:"departmentShares" doc:"Org unit code → % of the distributed amount (empty: one pool)"`
	GradePoints             map[string]string `json:"gradePoints" doc:"Grade code → points (method points)"`
	RoundingUnit            string            `json:"roundingUnit"`
	RoundingAccountCode     string            `json:"roundingAccountCode" doc:"GL account of the rounding difference"`
	PaidWithPayroll         bool              `json:"paidWithPayroll"`
}

// NewServiceChargePolicy returns the PRD P5 §16 #5 defaults.
func NewServiceChargePolicy() ServiceChargePolicy {
	return ServiceChargePolicy{DistributedPercent: "95", ReservePercent: "5", Method: "equal", AttendanceFactor: true, RedistributeForfeited: true,
		EligibleStatuses: []string{StatusPermanent, StatusContract}, ExcludeProbation: true, ExcludeWorkerCategories: []string{"daily"},
		ExcludeWarningLevelFrom: 2, ExcludeFullPeriodUnpaid: true, DepartmentShares: map[string]string{}, GradePoints: map[string]string{},
		RoundingUnit: "1", RoundingAccountCode: "", PaidWithPayroll: true}
}

func init() {
	for _, c := range []string{CategoryHRConfiguration, CategoryPayrollConfiguration, CategoryAttendanceConfiguration, CategoryHRPolicies} {
		if !slices.Contains(rules.PolicyCategories, c) {
			rules.PolicyCategories = append(rules.PolicyCategories, c)
		}
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: HRConfigurationCode, Category: CategoryHRConfiguration, Name: "HR Configuration",
		Description: "Employee and contract numbering, PKWT limits and compensation, probation, contract / document / certification reminders, " +
			"certification enforcement, offboarding checklist, self-service role and data retention", Default: NewHRConfiguration()})
	rules.RegisterPolicy(rules.PolicyDef{Code: PayrollConfigurationCode, Category: CategoryPayrollConfiguration, Name: "Payroll Configuration",
		Description: "Pay components, PTKP, PPh 21 (TER and Article 17), BPJS Kesehatan & Ketenagakerjaan, THR, severance, rounding and partner payouts",
		Default:     NewPayrollConfiguration()})
	rules.RegisterPolicy(rules.PolicyDef{Code: AttendanceConfigurationCode, Category: CategoryAttendanceConfiguration, Name: "Attendance Configuration",
		Description: "Clock-in methods, device points, mobile GPS staff, geofence, biometric consent, offline sync and the work week",
		Default:     NewAttendanceConfiguration()})
	rules.RegisterPolicy(rules.PolicyDef{Code: LeavePolicyCode, Category: CategoryHRPolicies, Name: "Leave Policy",
		Description: "Leave and permission types, entitlement and accrual, carry-over, notice and approval", Default: NewLeavePolicy()})
	rules.RegisterPolicy(rules.PolicyDef{Code: OvertimePolicyCode, Category: CategoryHRPolicies, Name: "Overtime Policy",
		Description: "Hourly wage divisor, multipliers per hour on working and rest days, daily / weekly limits and approval", Default: NewOvertimePolicy()})
	rules.RegisterPolicy(rules.PolicyDef{Code: AttendancePolicyCode, Category: CategoryHRPolicies, Name: "Attendance Policy",
		Description: "Late and early-leave tolerance, absence, geofence, methods per location and corrections", Default: NewAttendancePolicy()})
	rules.RegisterPolicy(rules.PolicyDef{Code: ServiceChargePolicyCode, Category: CategoryHRPolicies, Name: "Service Charge Policy",
		Description: "Distribution of the service charge pool: share, method, attendance factor, eligibility and rounding", Default: NewServiceChargePolicy()})
}

// PolicyTime is the moment a policy is resolved for a calendar day (UTC
// midnight): the end of that day, or now for today, so a version that took
// effect earlier today applies to today.
func PolicyTime(day time.Time) time.Time {
	end := day.AddDate(0, 0, 1).Add(-time.Second)
	if now := time.Now(); !now.Before(day) && now.Before(end) {
		return now
	}
	return end
}

// LoadHRConfiguration returns the HR Configuration in force at a property on
// a date.
func LoadHRConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (HRConfiguration, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, HRConfigurationCode, &property, at, NewHRConfiguration())
}

// LoadPayrollConfiguration returns the Payroll Configuration in force.
func LoadPayrollConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (PayrollConfiguration, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, PayrollConfigurationCode, &property, at, NewPayrollConfiguration())
}

// LoadAttendanceConfiguration returns the Attendance Configuration in force.
func LoadAttendanceConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (AttendanceConfiguration, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, AttendanceConfigurationCode, &property, at, NewAttendanceConfiguration())
}

// LoadLeavePolicy returns the Leave Policy in force.
func LoadLeavePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (LeavePolicy, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, LeavePolicyCode, &property, at, NewLeavePolicy())
}

// LoadOvertimePolicy returns the Overtime Policy in force.
func LoadOvertimePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (OvertimePolicy, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, OvertimePolicyCode, &property, at, NewOvertimePolicy())
}

// LoadAttendancePolicy returns the Attendance Policy in force.
func LoadAttendancePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (AttendancePolicy, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, AttendancePolicyCode, &property, at, NewAttendancePolicy())
}

// LoadServiceChargePolicy returns the Service Charge Policy in force.
func LoadServiceChargePolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (ServiceChargePolicy, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, ServiceChargePolicyCode, &property, at, NewServiceChargePolicy())
}
