package corehr

// Master data of Core HR through the generic resource engine (list, view,
// add, edit, archive, CSV / XLSX export and import, audit): grades, org
// units, positions, employees, bank accounts, emergency contacts,
// certification types, certifications, training programs and sessions and
// HR letter templates.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/reqctx"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var (
	unitCodeRe  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	codeRe30    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)
	nikRe       = regexp.MustCompile(`^\d{16}$`)
	npwpRe      = regexp.MustCompile(`^\d{15,16}$`)
	accountNoRe = regexp.MustCompile(`^[0-9][0-9 -]{3,33}$`)
)

const (
	codeMsg20 = "1–20 characters: A–Z, 0–9, - or _"
	codeMsg30 = "1–30 characters: A–Z, 0–9, - or _"
)

func unitCode() resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 20, Upper: true,
		Pattern: unitCodeRe, PatternMsg: codeMsg20, Search: true}
}

func code30() resource.Field {
	return resource.Field{Name: "code", Column: "code", Label: "Code", Kind: resource.String, Required: true, Max: 30, Upper: true,
		Pattern: codeRe30, PatternMsg: codeMsg30, Search: true}
}

func name() resource.Field { return resource.Name() }

func status() resource.Field { return resource.Status("active", "inactive") }

func text(n, c, label string, max int) resource.Field {
	return resource.Field{Name: n, Column: c, Label: label, Kind: resource.String, Max: max}
}

func ref(n, c, label, table string, required bool) resource.Field {
	return resource.Field{Name: n, Column: c, Label: label, Kind: resource.UUID, Required: required, Filter: true,
		Ref: &resource.Ref{Table: table, SameProperty: table != "hris.grades" && table != "hris.certification_types", Label: strings.ToLower(label)}}
}

// Resource keys.
const (
	KeyGrade             = "hris.grade"
	KeyOrgUnit           = "hris.org_unit"
	KeyPosition          = "hris.position"
	KeyEmployee          = "hris.employee"
	KeyBankAccount       = "hris.bank_account"
	KeyEmergencyContact  = "hris.emergency_contact"
	KeyCertificationType = "hris.certification_type"
	KeyCertification     = "hris.certification"
	KeyTrainingProgram   = "hris.training_program"
	KeyTrainingSession   = "hris.training_session"
	KeyLetterTemplate    = "hris.letter_template"
)

// Defs returns the resource definitions of Core HR.
func (m *Module) Defs() []*resource.Def {
	grades := &resource.Def{
		Key: KeyGrade, Module: hris.Module, Perm: KeyGrade, Path: "/api/v1/hris/grades", Table: "hris.grades", Name: "Grade", Plural: "Grades",
		SchemaName: "EmployeeGrade", Tag: "HRIS Organization", Archive: true, CodeField: "code", OrderBy: "level, code, id",
		Fields: []resource.Field{unitCode(), name(),
			{Name: "level", Column: "level", Label: "Level", Kind: resource.Int, Required: true, Min: resource.Min(1), MaxN: resource.Max(99)},
			{Name: "minSalary", Column: "min_salary", Label: "Minimum Salary", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "maxSalary", Column: "max_salary", Label: "Maximum Salary", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			status()},
		Hooks: resource.Hooks{AfterRead: maskSalaryRange},
	}
	orgUnits := &resource.Def{
		Key: KeyOrgUnit, Module: hris.Module, Perm: KeyOrgUnit, Path: "/api/v1/hris/org-units", Table: "hris.org_units", Name: "Org Unit",
		Plural: "Org Units", SchemaName: "EmployeeOrgUnit", Tag: "HRIS Organization", PropertyScoped: true, Archive: true, CodeField: "code",
		OrderBy: "sort_order, name, id",
		Fields: []resource.Field{unitCode(), name(),
			ref("parentId", "parent_id", "Parent Unit", "hris.org_units", false),
			{Name: "unitType", Column: "unit_type", Label: "Unit Type", Kind: resource.Enum, Enum: []string{"division", "department", "section", "outlet", "team"},
				Default: "department", Filter: true},
			{Name: "costCenter", Column: "cost_center", Label: "Cost Center", Kind: resource.String, Max: 40, Upper: true, Filter: true},
			ref("headEmployeeId", "head_employee_id", "Head", "hris.employees", false),
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			{Name: "sortOrder", Column: "sort_order", Label: "Sort Order", Kind: resource.Int, Default: int64(0)},
			status()},
		Hooks: resource.Hooks{BeforeWrite: orgUnitBeforeWrite},
	}
	positions := &resource.Def{
		Key: KeyPosition, Module: hris.Module, Perm: KeyPosition, Path: "/api/v1/hris/positions", Table: "hris.positions", Name: "Position",
		Plural: "Positions", SchemaName: "EmployeePosition", Tag: "HRIS Organization", PropertyScoped: true, Archive: true, CodeField: "code",
		OrderBy: "name, id",
		Fields: []resource.Field{code30(), name(),
			ref("orgUnitId", "org_unit_id", "Org Unit", "hris.org_units", true),
			ref("gradeId", "grade_id", "Grade", "hris.grades", false),
			ref("reportsToPositionId", "reports_to_position_id", "Reports To", "hris.positions", false),
			{Name: "isHead", Column: "is_head", Label: "Head of the Unit", Kind: resource.Bool, Default: false},
			{Name: "workforceRole", Column: "workforce_role", Label: "Workforce Role", Kind: resource.Enum, Enum: hris.WorkforceRoles, Filter: true},
			{Name: "requiredCertifications", Column: "required_certifications", Label: "Required Certifications (type codes)", Kind: resource.StringList,
				Upper: true, Default: []string{}},
			{Name: "headcount", Column: "headcount", Label: "Budgeted Headcount", Kind: resource.Int, Min: resource.Min(0)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			status()},
		Hooks: resource.Hooks{BeforeWrite: positionBeforeWrite},
	}
	employees := &resource.Def{
		Key: KeyEmployee, Module: hris.Module, Perm: KeyEmployee, Path: "/api/v1/hris/employees", Table: "hris.employees", Name: "Employee",
		Plural: "Employees", SchemaName: "EmployeeProfile", Tag: "HRIS Employees", PropertyScoped: true, Archive: true, CodeField: "employeeNo",
		OrderBy: "full_name, id",
		Fields: []resource.Field{
			{Name: "employeeNo", Column: "employee_no", Label: "Employee No. (empty = next number)", Kind: resource.String, Max: 30, Upper: true, Search: true},
			{Name: "fullName", Column: "full_name", Label: "Full Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			text("preferredName", "preferred_name", "Preferred Name", 60),
			{Name: "gender", Column: "gender", Label: "Gender", Kind: resource.Enum, Enum: []string{"male", "female"}, Filter: true},
			{Name: "birthDate", Column: "birth_date", Label: "Date of Birth", Kind: resource.Date},
			text("birthPlace", "birth_place", "Place of Birth", 80),
			text("religion", "religion", "Religion", 40),
			{Name: "maritalStatus", Column: "marital_status", Label: "Marital Status", Kind: resource.Enum, Enum: []string{"single", "married", "divorced", "widowed"}},
			text("nationality", "nationality", "Nationality", 40),
			{Name: "nik", Column: "nik", Label: "NIK (16 digits)", Kind: resource.String, Max: 20},
			{Name: "npwp", Column: "npwp", Label: "NPWP", Kind: resource.String, Max: 20},
			{Name: "ptkpStatus", Column: "ptkp_status", Label: "PTKP Status", Kind: resource.Enum, Enum: []string{"TK/0", "TK/1", "TK/2", "TK/3", "K/0", "K/1", "K/2", "K/3"}},
			text("bpjsKesehatanNo", "bpjs_kesehatan_no", "BPJS Kesehatan No.", 30),
			text("bpjsKetenagakerjaanNo", "bpjs_ketenagakerjaan_no", "BPJS Ketenagakerjaan No.", 30),
			text("bloodType", "blood_type", "Blood Type", 5),
			{Name: "healthNotes", Column: "health_notes", Label: "Health Notes", Kind: resource.Text, Max: 2000},
			{Name: "email", Column: "email", Label: "Work E-mail", Kind: resource.Email, Max: 254, Search: true},
			{Name: "personalEmail", Column: "personal_email", Label: "Personal E-mail", Kind: resource.Email, Max: 254},
			text("phone", "phone", "Phone", 40),
			{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
			text("city", "city", "City", 80),
			text("postalCode", "postal_code", "Postal Code", 10),
			{Name: "orgUnitId", Column: "org_unit_id", Label: "Org Unit", Kind: resource.UUID, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.org_units", SameProperty: true, Label: "org unit"}},
			{Name: "positionId", Column: "position_id", Label: "Position", Kind: resource.UUID, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.positions", SameProperty: true, Label: "position"}},
			{Name: "gradeId", Column: "grade_id", Label: "Grade", Kind: resource.UUID, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.grades", Label: "grade"}},
			{Name: "supervisorId", Column: "supervisor_id", Label: "Supervisor", Kind: resource.UUID, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "supervisor"}},
			text("jobTitle", "job_title", "Job Title", 120),
			{Name: "costCenter", Column: "cost_center", Label: "Cost Center (empty = org unit)", Kind: resource.String, Max: 40, Upper: true},
			{Name: "employmentStatus", Column: "employment_status", Label: "Employment Status", Kind: resource.Enum, Enum: hris.EmploymentStatuses,
				Default: hris.StatusProbation, CreateOnly: true, Filter: true},
			{Name: "workerCategory", Column: "worker_category", Label: "Worker Category", Kind: resource.Enum, Enum: []string{"regular", "daily", "intern"},
				Default: "regular", Filter: true},
			{Name: "joinDate", Column: "join_date", Label: "Join Date", Kind: resource.Date, Filter: true},
			{Name: "probationEndDate", Column: "probation_end_date", Label: "Probation Ends", Kind: resource.Date, ReadOnly: true},
			{Name: "permanentDate", Column: "permanent_date", Label: "Permanent Since", Kind: resource.Date, ReadOnly: true},
			{Name: "terminationDate", Column: "termination_date", Label: "Leaves On", Kind: resource.Date, ReadOnly: true},
			{Name: "terminationType", Column: "termination_type", Label: "Leaving Type", Kind: resource.Enum,
				Enum: []string{"resigned", "terminated", "contract_ended", "retired", "deceased"}, ReadOnly: true},
			{Name: "terminationStatus", Column: "termination_status", Label: "Leaving Status", Kind: resource.Enum, Enum: []string{"scheduled", "completed"},
				ReadOnly: true, Filter: true},
			text("legacyRef", "legacy_ref", "Legacy Reference", 60),
			// front desk areas the employee may open (golf, sportclub; empty = as the role allows — docs/requirement-booking-sportclub-mgcc.md FR-99)
			{Name: "workAreas", Column: "work_areas", Label: "Front Desk Areas (golf, sportclub)", Kind: resource.StringList, Enum: []string{"golf", "sportclub"}, Default: []string{}, Filter: true},
			// draft: prepared, not yet hired (HRIS phase B §34); activated with :activate
			{Name: "status", Column: "status", Label: "Status (draft = not yet hired)", Kind: resource.Enum, Enum: []string{"active", "draft", "inactive"},
				Default: "active", CreateOnly: true, Filter: true},
			{Name: "suspendedFrom", Column: "suspended_from", Label: "Suspended From", Kind: resource.Date, ReadOnly: true},
			{Name: "suspendedUntil", Column: "suspended_until", Label: "Suspended Until", Kind: resource.Date, ReadOnly: true},
			{Name: "suspensionReason", Column: "suspension_reason", Label: "Suspension Reason", Kind: resource.Text, ReadOnly: true},
		},
		Hooks: resource.Hooks{BeforeWrite: m.employeeBeforeWrite, AfterCreate: m.employeeAfterCreate, AfterRead: maskEmployee},
	}
	bankAccounts := &resource.Def{
		Key: KeyBankAccount, Module: hris.Module, Perm: KeyBankAccount, Path: "/api/v1/hris/bank-accounts", Table: "hris.employee_bank_accounts",
		Name: "Bank Account", Plural: "Bank Accounts", SchemaName: "EmployeeBankAccount", Tag: "HRIS Employees", PropertyScoped: true, Archive: true,
		OrderBy: "is_primary DESC, created_at, id",
		Fields: []resource.Field{
			{Name: "employeeId", Column: "employee_id", Label: "Employee", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "bankCode", Column: "bank_code", Label: "Bank Code", Kind: resource.String, Max: 20, Upper: true},
			{Name: "bankName", Column: "bank_name", Label: "Bank", Kind: resource.String, Required: true, Max: 80},
			{Name: "accountNo", Column: "account_no", Label: "Account No.", Kind: resource.String, Required: true, Max: 34},
			{Name: "accountName", Column: "account_name", Label: "Account Name", Kind: resource.String, Required: true, Max: 120},
			text("branch", "branch", "Branch", 80),
			{Name: "isPrimary", Column: "is_primary", Label: "Primary (salary)", Kind: resource.Bool, Default: true},
			status()},
		Hooks: resource.Hooks{BeforeWrite: bankBeforeWrite, AfterRead: maskBank},
	}
	contacts := &resource.Def{
		Key: KeyEmergencyContact, Module: hris.Module, Perm: KeyEmergencyContact, Path: "/api/v1/hris/emergency-contacts",
		Table: "hris.employee_emergency_contacts", Name: "Emergency Contact", Plural: "Emergency Contacts", SchemaName: "EmployeeEmergencyContact",
		Tag: "HRIS Employees", PropertyScoped: true, Archive: true, OrderBy: "is_primary DESC, name, id",
		Fields: []resource.Field{
			{Name: "employeeId", Column: "employee_id", Label: "Employee", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "name", Column: "name", Label: "Name", Kind: resource.String, Required: true, Max: 120, Search: true},
			{Name: "relationship", Column: "relationship", Label: "Relationship", Kind: resource.Enum,
				Enum: []string{"spouse", "parent", "child", "sibling", "relative", "friend", "other"}, Default: "other"},
			{Name: "phone", Column: "phone", Label: "Phone", Kind: resource.String, Required: true, Max: 40},
			{Name: "address", Column: "address", Label: "Address", Kind: resource.Text, Max: 500},
			{Name: "isPrimary", Column: "is_primary", Label: "Primary", Kind: resource.Bool, Default: false},
			status()},
	}
	certTypes := &resource.Def{
		Key: KeyCertificationType, Module: hris.Module, Perm: KeyCertificationType, Path: "/api/v1/hris/certification-types",
		Table: "hris.certification_types", Name: "Certification Type", Plural: "Certification Types", SchemaName: "EmployeeCertificationType",
		Tag: "HRIS Training & Certification", Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{code30(), name(),
			text("issuer", "issuer", "Issuing Body", 120),
			{Name: "validityMonths", Column: "validity_months", Label: "Validity (months, empty = no expiry)", Kind: resource.Int, Min: resource.Min(1)},
			{Name: "mandatoryFor", Column: "mandatory_for", Label: "Mandatory For (workforce roles)", Kind: resource.StringList, Enum: hris.WorkforceRoles,
				Default: []string{}},
			{Name: "reminderDays", Column: "reminder_days", Label: "Reminders (days before expiry)", Kind: resource.IntList, Default: []int64{60, 30},
				Min: resource.Min(1), MaxN: resource.Max(365)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			status()},
	}
	certs := &resource.Def{
		Key: KeyCertification, Module: hris.Module, Perm: KeyCertification, Path: "/api/v1/hris/certifications", Table: "hris.certifications",
		Name: "Certification", Plural: "Certifications", SchemaName: "EmployeeCertification", Tag: "HRIS Training & Certification",
		PropertyScoped: true, Archive: true, OrderBy: "expires_on NULLS LAST, holder_name, id",
		Fields: []resource.Field{
			{Name: "certificationTypeId", Column: "certification_type_id", Label: "Certification Type", Kind: resource.UUID, Required: true, CreateOnly: true,
				Filter: true, Ref: &resource.Ref{Table: "hris.certification_types", Label: "certification type"}},
			{Name: "holderKind", Column: "holder_kind", Label: "Holder", Kind: resource.Enum, Enum: []string{hris.HolderEmployee, hris.HolderCaddy, hris.HolderInstructor},
				Default: hris.HolderEmployee, CreateOnly: true, Filter: true},
			{Name: "employeeId", Column: "employee_id", Label: "Employee", Kind: resource.UUID, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.employees", SameProperty: true, Label: "employee"}},
			{Name: "partnerId", Column: "partner_id", Label: "Caddy / Instructor (partner)", Kind: resource.UUID, CreateOnly: true, Filter: true},
			{Name: "holderName", Column: "holder_name", Label: "Holder Name", Kind: resource.String, ReadOnly: true, Search: true},
			text("certificateNo", "certificate_no", "Certificate No.", 60),
			text("issuer", "issuer", "Issued By (empty = type)", 120),
			{Name: "issuedOn", Column: "issued_on", Label: "Issued On", Kind: resource.Date},
			{Name: "expiresOn", Column: "expires_on", Label: "Expires On (empty = from validity)", Kind: resource.Date, Filter: true},
			{Name: "fileId", Column: "file_id", Label: "Certificate File", Kind: resource.UUID, Ref: &resource.Ref{Table: "platform.files", Label: "file"}},
			{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"active", "expired", "renewed", "revoked"}, Default: "active",
				ReadOnly: true, Filter: true},
			{Name: "renewedById", Column: "renewed_by_id", Label: "Renewed By", Kind: resource.UUID, ReadOnly: true},
			{Name: "revokedReason", Column: "revoked_reason", Label: "Revocation Reason", Kind: resource.Text, ReadOnly: true},
			{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		},
		Hooks: resource.Hooks{BeforeWrite: m.certificationBeforeWrite, AfterCreate: m.certificationAfterCreate},
	}
	programs := &resource.Def{
		Key: KeyTrainingProgram, Module: hris.Module, Perm: KeyTrainingProgram, Path: "/api/v1/hris/training-programs", Table: "hris.training_programs",
		Name: "Training Program", Plural: "Training Programs", SchemaName: "EmployeeTrainingProgram", Tag: "HRIS Training & Certification",
		PropertyScoped: true, Archive: true, CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{code30(), name(),
			{Name: "category", Column: "category", Label: "Category", Kind: resource.Enum,
				Enum: []string{"mandatory", "safety", "service", "technical", "leadership", "compliance", "other"}, Default: "technical", Filter: true},
			text("provider", "provider", "Provider", 120),
			{Name: "durationHours", Column: "duration_hours", Label: "Duration (hours)", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "costPerParticipant", Column: "cost_per_participant", Label: "Cost per Participant", Kind: resource.Decimal, Default: "0", Min: resource.Min(0)},
			{Name: "certificationTypeId", Column: "certification_type_id", Label: "Grants Certification", Kind: resource.UUID, Filter: true,
				Ref: &resource.Ref{Table: "hris.certification_types", Label: "certification type"}},
			{Name: "requiredPositions", Column: "required_positions", Label: "Mandatory for Positions (codes)", Kind: resource.StringList, Upper: true,
				Default: []string{}},
			{Name: "refresherMonths", Column: "refresher_months", Label: "Refresher Every (months)", Kind: resource.Int, Min: resource.Min(1)},
			{Name: "description", Column: "description", Label: "Description", Kind: resource.Text, Max: 2000},
			status()},
		Hooks: resource.Hooks{BeforeWrite: programBeforeWrite},
	}
	sessions := &resource.Def{
		Key: KeyTrainingSession, Module: hris.Module, Perm: KeyTrainingSession, Path: "/api/v1/hris/training-sessions", Table: "hris.training_sessions",
		Name: "Training Session", Plural: "Training Sessions", SchemaName: "EmployeeTrainingSession", Tag: "HRIS Training & Certification",
		PropertyScoped: true, Archive: true, OrderBy: "starts_at DESC, id",
		Fields: []resource.Field{
			{Name: "programId", Column: "program_id", Label: "Training Program", Kind: resource.UUID, Required: true, CreateOnly: true, Filter: true,
				Ref: &resource.Ref{Table: "hris.training_programs", SameProperty: true, Label: "training program"}},
			{Name: "title", Column: "title", Label: "Title (empty = program)", Kind: resource.String, Max: 160, Search: true},
			{Name: "startsAt", Column: "starts_at", Label: "Starts", Kind: resource.Timestamp, Required: true},
			{Name: "endsAt", Column: "ends_at", Label: "Ends", Kind: resource.Timestamp, Required: true},
			text("location", "location", "Location", 120),
			text("trainer", "trainer", "Trainer", 120),
			{Name: "capacity", Column: "capacity", Label: "Capacity", Kind: resource.Int, Min: resource.Min(1)},
			{Name: "costTotal", Column: "cost_total", Label: "Total Cost (empty = per participant)", Kind: resource.Decimal, Min: resource.Min(0)},
			{Name: "status", Column: "status", Label: "Status", Kind: resource.Enum, Enum: []string{"planned", "completed", "cancelled"}, Default: "planned",
				Filter: true},
			{Name: "notes", Column: "notes", Label: "Notes", Kind: resource.Text, Max: 2000},
		},
		Hooks: resource.Hooks{BeforeWrite: sessionBeforeWrite},
	}
	letters := &resource.Def{
		Key: KeyLetterTemplate, Module: hris.Module, Perm: KeyLetterTemplate, Path: "/api/v1/hris/letter-templates", Table: "hris.letter_templates",
		Name: "Letter Template", Plural: "Letter Templates", SchemaName: "EmployeeLetterTemplate", Tag: "HRIS Employees", Archive: true,
		CodeField: "code", OrderBy: "name, id",
		Fields: []resource.Field{code30(), name(),
			{Name: "letterType", Column: "letter_type", Label: "Letter Type", Kind: resource.Enum,
				Enum: []string{"employment_agreement", "employment_certificate", "warning_letter", "offer_letter", "other"}, Default: "other", Filter: true},
			{Name: "language", Column: "language", Label: "Language", Kind: resource.Enum, Enum: []string{"id", "en"}, Default: "id"},
			{Name: "title", Column: "title", Label: "Letter Title", Kind: resource.String, Required: true, Max: 160},
			{Name: "body", Column: "body", Label: "Body ({{.Employee.FullName}}, {{.Contract.Number}} …)", Kind: resource.Text, Required: true, Max: 20000},
			status()},
		Hooks: resource.Hooks{BeforeWrite: letterBeforeWrite},
	}
	return []*resource.Def{grades, orgUnits, positions, employees, bankAccounts, contacts, certTypes, certs, programs, sessions, letters}
}

// ── hooks ─────────────────────────────────────────────────────────────────

// orgUnitBeforeWrite rejects a parent that is the unit itself or below it,
// and a head from another property.
func orgUnitBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	parent, ok := v["parentId"].(string)
	if !ok || parent == "" || before == nil {
		return nil
	}
	self, _ := before["id"].(string)
	var cycle bool
	if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
		SELECT id, parent_id FROM hris.org_units WHERE id = $1::uuid
		UNION ALL SELECT d.id, d.parent_id FROM hris.org_units d JOIN up ON d.id = up.parent_id)
		SELECT EXISTS (SELECT 1 FROM up WHERE id = $2::uuid)`, parent, self).Scan(&cycle); err != nil {
		return err
	}
	if cycle {
		return handle.Invalid("parentId", "cycle", "a unit cannot be below itself")
	}
	return nil
}

// certificationCodes checks that type codes exist.
func certificationCodes(ctx context.Context, tx pgx.Tx, field string, v any) error {
	codes, ok := v.([]string)
	if !ok || len(codes) == 0 {
		return nil
	}
	var missing []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(c), '{}') FROM unnest($1::text[]) c
		WHERE NOT EXISTS (SELECT 1 FROM hris.certification_types t WHERE t.code = c AND t.archived_at IS NULL)`, codes).Scan(&missing); err != nil {
		return err
	}
	if len(missing) > 0 {
		return handle.Invalid(field, "not_found", "unknown certification type: "+strings.Join(missing, ", "))
	}
	return nil
}

func positionBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if before != nil {
		if p, ok := v["reportsToPositionId"].(string); ok && p == before["id"] {
			return handle.Invalid("reportsToPositionId", "cycle", "a position cannot report to itself")
		}
	}
	return certificationCodes(ctx, tx, "requiredCertifications", v["requiredCertifications"])
}

func programBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	codes, ok := v["requiredPositions"].([]string)
	if !ok || len(codes) == 0 {
		return nil
	}
	var missing []string
	if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(c), '{}') FROM unnest($1::text[]) c
		WHERE NOT EXISTS (SELECT 1 FROM hris.positions p WHERE p.code = c AND p.property_id = $2 AND p.archived_at IS NULL)`,
		codes, propertyOf(ctx, before)).Scan(&missing); err != nil {
		return err
	}
	if len(missing) > 0 {
		return handle.Invalid("requiredPositions", "not_found", "unknown position: "+strings.Join(missing, ", "))
	}
	return nil
}

// propertyOf is the property of the row (update) or of the request (create).
func propertyOf(ctx context.Context, before map[string]any) uuid.UUID {
	if before != nil {
		if s, ok := before["propertyId"].(string); ok {
			if u, err := uuid.Parse(s); err == nil {
				return u
			}
		}
	}
	return handle.Property(ctx)
}

func sessionBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if before != nil && before["status"] == "completed" {
		return errs.Conflict("session_completed", "a completed training session cannot be changed")
	}
	if st, ok := v["status"].(string); ok && st == "completed" && (before == nil || before["status"] != "completed") {
		return handle.Invalid("status", "invalid", "complete a session with Complete Session (attendance and results)")
	}
	start, end := v["startsAt"], v["endsAt"]
	if before != nil {
		if start == nil {
			start = before["startsAt"]
		}
		if end == nil {
			end = before["endsAt"]
		}
	}
	if s, e := asTime(start), asTime(end); !s.IsZero() && !e.IsZero() && !e.After(s) {
		return handle.Invalid("endsAt", "invalid", "must be after the start")
	}
	if t := v["title"]; before == nil && (t == nil || t == "") {
		if pid, ok := v["programId"].(string); ok {
			var n string
			if err := tx.QueryRow(ctx, `SELECT name FROM hris.training_programs WHERE id = $1::uuid`, pid).Scan(&n); err == nil {
				v["title"] = n
			}
		}
	}
	return nil
}

func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		p, _ := time.Parse(time.RFC3339, t)
		return p
	}
	return time.Time{}
}

func letterBeforeWrite(_ context.Context, _ pgx.Tx, v map[string]any, _ map[string]any) error {
	if b, ok := v["body"].(string); ok {
		if _, err := parseLetter(b); err != nil {
			return handle.Invalid("body", "invalid_template", err.Error())
		}
	}
	return nil
}

func bankBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	if s, ok := v["accountNo"].(string); ok {
		switch {
		case strings.Contains(s, "*") && before != nil:
			delete(v, "accountNo") // the masked value sent back unchanged
		case !accountNoRe.MatchString(s):
			return handle.Invalid("accountNo", "invalid_format", "digits, spaces or -")
		}
	}
	primary, _ := v["isPrimary"].(bool)
	if !primary {
		return nil
	}
	emp, _ := v["employeeId"].(string)
	self := ""
	if before != nil {
		emp, _ = before["employeeId"].(string)
		self, _ = before["id"].(string)
	}
	_, err := tx.Exec(ctx, `UPDATE hris.employee_bank_accounts SET is_primary = false WHERE employee_id = $1::uuid AND is_primary
		AND ($2 = '' OR id <> $2::uuid)`, emp, self)
	return err
}

// ── masking (FR-HR-03) ────────────────────────────────────────────────────

// MaskTail keeps the last 4 characters of an identity or account number.
func MaskTail(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-4) + string(r[len(r)-4:])
}

var sensitiveEmployeeFields = []string{"nik", "npwp", "bpjsKesehatanNo", "bpjsKetenagakerjaanNo"}

func rowProperty(ctx context.Context, row map[string]any) uuid.UUID {
	if s, ok := row["propertyId"].(string); ok {
		if u, err := uuid.Parse(s); err == nil {
			return u
		}
	}
	pid, _ := reqctx.Property(ctx)
	return pid
}

func maskEmployee(ctx context.Context, row map[string]any) {
	if can(ctx, "hris.employee.view_sensitive", rowProperty(ctx, row)) {
		return
	}
	for _, f := range sensitiveEmployeeFields {
		if s, ok := row[f].(string); ok && s != "" {
			row[f] = MaskTail(s)
		}
	}
	for _, f := range []string{"healthNotes", "bloodType"} {
		if s, ok := row[f].(string); ok && s != "" {
			row[f] = mask.Redacted
		}
	}
}

func maskBank(ctx context.Context, row map[string]any) {
	if can(ctx, "hris.employee.view_sensitive", rowProperty(ctx, row)) {
		return
	}
	if s, ok := row["accountNo"].(string); ok {
		row["accountNo"] = MaskTail(s)
	}
}

func maskSalaryRange(ctx context.Context, row map[string]any) {
	p := authzPrincipal(ctx)
	if p != nil {
		if all, ids := p.PropertiesFor("hris.contract.view_salary"); all || len(ids) > 0 {
			return
		}
	}
	row["minSalary"], row["maxSalary"] = nil, nil
}

// ── employees ─────────────────────────────────────────────────────────────

// employeeBeforeWrite numbers new employees, validates identity numbers and
// keeps masked values the client sent back unchanged.
func (m *Module) employeeBeforeWrite(ctx context.Context, tx pgx.Tx, v map[string]any, before map[string]any) error {
	for _, f := range append(sensitiveEmployeeFields, "healthNotes", "bloodType") {
		if s, ok := v[f].(string); ok && (strings.Contains(s, "*") || s == mask.Redacted) {
			delete(v, f)
		}
	}
	if s, ok := v["nik"].(string); ok && s != "" && !nikRe.MatchString(s) {
		return handle.Invalid("nik", "invalid_format", "NIK has 16 digits")
	}
	if s, ok := v["npwp"].(string); ok && s != "" {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, s)
		if !npwpRe.MatchString(digits) {
			return handle.Invalid("npwp", "invalid", "NPWP has 15 or 16 digits")
		}
		v["npwp"] = digits
	}
	property := propertyOf(ctx, before)
	if before == nil {
		if s, _ := v["status"].(string); s == "inactive" {
			return handle.Invalid("status", "invalid", "a new employee is active or a draft")
		}
		st, _ := v["employmentStatus"].(string)
		if st == hris.StatusResigned || st == hris.StatusTerminated {
			return handle.Invalid("employmentStatus", "invalid", "a new employee starts as probation, contract or permanent")
		}
		if no, _ := v["employeeNo"].(string); no == "" {
			n, err := m.nextEmployeeNo(ctx, tx, property)
			if err != nil {
				return err
			}
			v["employeeNo"] = n
		}
		if err := checkPlacement(ctx, tx, v); err != nil {
			return err
		}
		if jt, _ := v["jobTitle"].(string); jt == "" {
			if pid, ok := v["positionId"].(string); ok && pid != "" {
				var n string
				if err := tx.QueryRow(ctx, `SELECT name FROM hris.positions WHERE id = $1::uuid`, pid).Scan(&n); err == nil {
					v["jobTitle"] = n
				}
			}
		}
		if _, ok := v["joinDate"]; !ok || v["joinDate"] == nil {
			v["joinDate"] = ymd(today(ctx, tx, property))
		}
	}
	return nil
}

// checkPlacement checks that the position belongs to the org unit (the unit
// defaults to the position's) and takes the position grade by default.
func checkPlacement(ctx context.Context, tx pgx.Tx, v map[string]any) error {
	pos, _ := v["positionId"].(string)
	if pos == "" {
		return nil
	}
	var unit uuid.UUID
	var grade *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT org_unit_id, grade_id FROM hris.positions WHERE id = $1::uuid`, pos).Scan(&unit, &grade); err != nil {
		return handle.Invalid("positionId", "not_found", "position not found")
	}
	if u, _ := v["orgUnitId"].(string); u == "" {
		v["orgUnitId"] = unit.String()
	} else if u != unit.String() {
		return handle.Invalid("positionId", "invalid", "the position belongs to another org unit")
	}
	if g, _ := v["gradeId"].(string); g == "" && grade != nil {
		v["gradeId"] = grade.String()
	}
	return nil
}

// nextEmployeeNo issues the next PREFIX-NNNNN employee number.
func (m *Module) nextEmployeeNo(ctx context.Context, tx pgx.Tx, property uuid.UUID) (string, error) {
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return "", err
	}
	prefix := strings.ToUpper(strings.TrimSpace(cfg.EmployeeNumberPrefix))
	digits := max(cfg.EmployeeNumberDigits, 3)
	for {
		var n int
		if err := tx.QueryRow(ctx, `INSERT INTO hris.sequences (property_id, prefix, year, last_value) VALUES ($1, $2, 0, 1)
			ON CONFLICT (property_id, prefix, year) DO UPDATE SET last_value = hris.sequences.last_value + 1 RETURNING last_value`,
			property, "EMP:"+prefix).Scan(&n); err != nil {
			return "", err
		}
		no := fmt.Sprintf("%s%0*d", prefix, digits, n)
		if prefix != "" {
			no = fmt.Sprintf("%s-%0*d", prefix, digits, n)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE property_id = $1 AND employee_no = $2)`, property, no).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return no, nil
		}
	}
}

// employeeAfterCreate records the hire in the employment history and
// publishes hris.employee_hired.
func (m *Module) employeeAfterCreate(ctx context.Context, tx pgx.Tx, row map[string]any) error {
	if row["status"] == "draft" { // hired on :activate
		return nil
	}
	eid, _ := uuid.Parse(row["id"].(string))
	pid, _ := uuid.Parse(row["propertyId"].(string))
	join, _ := row["joinDate"].(string)
	return m.hire(ctx, tx, eid, pid, join)
}

// hire records the hire in the employment history and publishes
// hris.employee_hired (create, or activation of a draft).
func (m *Module) hire(ctx context.Context, tx pgx.Tx, eid, pid uuid.UUID, join string) error {
	if join == "" {
		join = ymd(today(ctx, tx, pid))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, to_org_unit_id, to_position_id,
		to_grade_id, to_supervisor_id, to_status, reason, status, applied_at, created_by, updated_by)
		SELECT gen_random_uuid(), property_id, id, 'hire', $2::date, org_unit_id, position_id, grade_id, supervisor_id, employment_status, 'Hired',
		  'applied', now(), $3, $3 FROM hris.employees WHERE id = $1`, eid, join, actor(ctx)); err != nil {
		return err
	}
	e, err := hris.EmployeeByID(ctx, tx, eid)
	if err != nil {
		return err
	}
	_, err = m.Events.Publish(ctx, tx, hris.EventEmployeeHired, "hris.employee", &eid, &pid, hris.EmployeeHired{
		EmployeeID: eid, EmployeeNo: e.EmployeeNo, FullName: e.FullName, PropertyID: pid, OrgUnitID: e.OrgUnitID, PositionID: e.PositionID,
		GradeID: e.GradeID, EmploymentStatus: e.EmploymentStatus, WorkerCategory: e.WorkerCategory, JoinDate: join})
	return err
}
