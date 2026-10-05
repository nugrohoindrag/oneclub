package corehr

// Employee Self Service (EP-16, EP-26 FR-OPS-P5-01): the personal-login
// area of the ops shell. The employee is the one linked to the signed-in
// user (platform.users.employee_id). Core HR serves me / profile, My
// Documents, My Training, personal data changes verified by HR (FR-ESS-06)
// and the manager's team (FR-ESS-05); the time and payroll areas add their
// sections through hris.RegisterESSSection.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

var decimalZero = decimal.Zero

// SelfProfile is the employee's own profile in ESS.
type SelfProfile struct {
	ID                uuid.UUID               `json:"id" db:"id"`
	PropertyID        uuid.UUID               `json:"propertyId" db:"property_id"`
	PropertyName      string                  `json:"propertyName" db:"property_name"`
	EmployeeNo        string                  `json:"employeeNo" db:"employee_no"`
	FullName          string                  `json:"fullName" db:"full_name"`
	PreferredName     *string                 `json:"preferredName" db:"preferred_name"`
	JobTitle          *string                 `json:"jobTitle" db:"job_title"`
	OrgUnit           *string                 `json:"orgUnit" db:"org_unit"`
	Position          *string                 `json:"position" db:"position"`
	Grade             *string                 `json:"grade" db:"grade"`
	Supervisor        *string                 `json:"supervisor" db:"supervisor"`
	EmploymentStatus  string                  `json:"employmentStatus" db:"employment_status"`
	JoinDate          *time.Time              `json:"joinDate" db:"join_date"`
	ProbationEndDate  *time.Time              `json:"probationEndDate" db:"probation_end_date"`
	Email             *string                 `json:"email" db:"email"`
	PersonalEmail     *string                 `json:"personalEmail" db:"personal_email"`
	Phone             *string                 `json:"phone" db:"phone"`
	Address           *string                 `json:"address" db:"address"`
	City              *string                 `json:"city" db:"city"`
	PostalCode        *string                 `json:"postalCode" db:"postal_code"`
	MaritalStatus     *string                 `json:"maritalStatus" db:"marital_status"`
	PTKPStatus        *string                 `json:"ptkpStatus" db:"ptkp_status"`
	NIK               *string                 `json:"nik" db:"nik" doc:"Masked (last 4 digits)"`
	NPWP              *string                 `json:"npwp" db:"npwp" doc:"Masked (last 4 digits)"`
	BankName          *string                 `json:"bankName" db:"bank_name"`
	BankAccountNo     *string                 `json:"bankAccountNo" db:"bank_account_no" doc:"Masked (last 4 digits)"`
	BankAccountName   *string                 `json:"bankAccountName" db:"bank_account_name"`
	EmergencyContacts []EmergencyContactInput `json:"emergencyContacts" db:"emergency_contacts"`
}

// ESSMe is the start of Employee Self Service.
type ESSMe struct {
	Employee       SelfProfile       `json:"employee"`
	Sections       []hris.ESSSection `json:"sections"`
	IsManager      bool              `json:"isManager"`
	TeamSize       int               `json:"teamSize"`
	PendingChanges int               `json:"pendingChanges"`
	ExpiringItems  int               `json:"expiringItems" doc:"My documents and certificates expiring within 30 days"`
}

// EmergencyContactInput is an emergency contact in a data change.
type EmergencyContactInput struct {
	Name         string  `json:"name"`
	Relationship string  `json:"relationship" enum:"spouse,parent,child,sibling,relative,friend,other"`
	Phone        string  `json:"phone"`
	Address      *string `json:"address,omitempty"`
}

// BankAccountInput is a new salary account in a data change.
type BankAccountInput struct {
	BankCode    *string `json:"bankCode,omitempty"`
	BankName    string  `json:"bankName"`
	AccountNo   string  `json:"accountNo"`
	AccountName string  `json:"accountName"`
	Branch      *string `json:"branch,omitempty"`
}

// ProfileChanges are the personal data an employee asks to change.
type ProfileChanges struct {
	Phone             *string                 `json:"phone,omitempty"`
	PersonalEmail     *string                 `json:"personalEmail,omitempty"`
	Address           *string                 `json:"address,omitempty"`
	City              *string                 `json:"city,omitempty"`
	PostalCode        *string                 `json:"postalCode,omitempty"`
	MaritalStatus     *string                 `json:"maritalStatus,omitempty" enum:"single,married,divorced,widowed"`
	PTKPStatus        *string                 `json:"ptkpStatus,omitempty" enum:"TK/0,TK/1,TK/2,TK/3,K/0,K/1,K/2,K/3"`
	EmergencyContacts []EmergencyContactInput `json:"emergencyContacts,omitempty" doc:"Replaces the emergency contacts"`
	BankAccount       *BankAccountInput       `json:"bankAccount,omitempty" doc:"New salary account"`
}

// fields lists the changed fields.
func (c ProfileChanges) fields() []string {
	var out []string
	add := func(ok bool, n string) {
		if ok {
			out = append(out, n)
		}
	}
	add(c.Phone != nil, "phone")
	add(c.PersonalEmail != nil, "personalEmail")
	add(c.Address != nil, "address")
	add(c.City != nil, "city")
	add(c.PostalCode != nil, "postalCode")
	add(c.MaritalStatus != nil, "maritalStatus")
	add(c.PTKPStatus != nil, "ptkpStatus")
	add(c.EmergencyContacts != nil, "emergencyContacts")
	add(c.BankAccount != nil, "bankAccount")
	return out
}

// ProfileChangeRequest submits a data change.
type ProfileChangeRequest struct {
	Changes ProfileChanges `json:"changes"`
}

// ReviewRequest approves / rejects a data change.
type ReviewRequest struct {
	Note string `json:"note,omitempty"`
}

// ProfileChange is a personal data change request.
type ProfileChange struct {
	ID           uuid.UUID      `json:"id" db:"id"`
	PropertyID   uuid.UUID      `json:"propertyId" db:"property_id"`
	EmployeeID   uuid.UUID      `json:"employeeId" db:"employee_id"`
	EmployeeNo   string         `json:"employeeNo" db:"employee_no"`
	EmployeeName string         `json:"employeeName" db:"employee_name"`
	Changes      ProfileChanges `json:"changes" db:"changes"`
	Fields       []string       `json:"fields" db:"-"`
	Status       string         `json:"status" db:"status" enum:"submitted,approved,rejected,cancelled"`
	ReviewedBy   *string        `json:"reviewedBy" db:"reviewed_by_name"`
	ReviewedAt   *time.Time     `json:"reviewedAt" db:"reviewed_at"`
	ReviewNote   *string        `json:"reviewNote" db:"review_note"`
	CreatedAt    time.Time      `json:"createdAt" db:"created_at"`
}

const changeRequestSelect = `SELECT r.id, r.property_id, r.employee_id, e.employee_no, e.full_name AS employee_name, r.changes, r.status,
	u.full_name AS reviewed_by_name, r.reviewed_at, r.review_note, r.created_at
	FROM hris.profile_change_requests r JOIN hris.employees e ON e.id = r.employee_id LEFT JOIN platform.users u ON u.id = r.reviewed_by`

// TeamMember is an employee of the manager's team.
type TeamMember struct {
	ID                uuid.UUID               `json:"id"`
	EmployeeNo        string                  `json:"employeeNo"`
	FullName          string                  `json:"fullName"`
	JobTitle          *string                 `json:"jobTitle"`
	OrgUnit           *string                 `json:"orgUnit"`
	Phone             *string                 `json:"phone"`
	EmploymentStatus  string                  `json:"employmentStatus"`
	Direct            bool                    `json:"direct" doc:"Reports directly to me"`
	ContractEnds      *time.Time              `json:"contractEnds"`
	CertificationGaps []hris.CertificationGap `json:"certificationGaps"`
}

// MyTraining is the training record of the employee.
type MyTraining struct {
	Sessions       []MySession         `json:"sessions"`
	Certifications []CertificationView `json:"certifications"`
	Mandatory      []MatrixRow         `json:"mandatory"`
}

// MySession is a training session of the employee.
type MySession struct {
	SessionID  uuid.UUID `json:"sessionId" db:"session_id"`
	Title      string    `json:"title" db:"title"`
	Program    string    `json:"program" db:"program"`
	StartsAt   time.Time `json:"startsAt" db:"starts_at"`
	EndsAt     time.Time `json:"endsAt" db:"ends_at"`
	Location   *string   `json:"location" db:"location"`
	Status     string    `json:"status" db:"status"`
	Attendance string    `json:"attendance" db:"attendance"`
	Result     *string   `json:"result" db:"result"`
}

func (m *Module) registerESS(reg *route.Registry) {
	tag := "Employee Self Service"
	ess := func(rt route.Route) {
		if rt.Permission == "" {
			rt.Permission = hris.PermissionESS
		}
		add(reg, tag, rt)
	}
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/me", Summary: "My employee profile and Employee Self Service sections",
		Response: ESSMe{}, Handler: handle.Read(m.DB, m.essMeHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/documents", Summary: "My documents", Response: EmployeeDocument{}, List: true,
		Handler: listRead(m.DB, m.essDocumentsHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/documents/{id}/file", Summary: "Download my document", RawContent: "application/octet-stream",
		Handler: m.essDocumentFileHTTP})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/training", Summary: "My training and certifications", Response: MyTraining{},
		Handler: handle.Read(m.DB, m.essTrainingHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/profile-changes", Summary: "My personal data changes", Response: ProfileChange{}, List: true,
		Handler: listRead(m.DB, m.essChangesHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/profile-changes", Summary: "Ask HR to change my personal data",
		Request: ProfileChangeRequest{}, Response: ProfileChange{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.essSubmitChangeHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/profile-changes/{id}:cancel", Summary: "Withdraw my data change",
		Request: handle.Empty{}, Response: ProfileChange{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essCancelChangeHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team", Summary: "My team (department heads)", Permission: hris.PermissionTeam,
		Response: TeamMember{}, List: true, Handler: listRead(m.DB, m.essTeamHTTP)})

	hr := "HRIS Employees"
	add(reg, hr, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/profile-changes", Summary: "Personal data changes from Employee Self Service",
		Permission: "hris.profile_change.view", Response: ProfileChange{}, List: true,
		Query: []route.Param{{Name: "status", Enum: []string{"submitted", "approved", "rejected", "cancelled"}}}, Handler: listRead(m.DB, m.hrChangesHTTP)})
	add(reg, hr, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/profile-changes/{id}:approve", Summary: "Verify and apply a personal data change",
		Permission: "hris.profile_change.review", Request: ReviewRequest{}, Response: ProfileChange{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reviewHTTP(true))})
	add(reg, hr, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/profile-changes/{id}:reject", Summary: "Reject a personal data change",
		Permission: "hris.profile_change.review", Request: ReviewRequest{}, Response: ProfileChange{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reviewHTTP(false))})
}

// me returns the employee of the signed-in user.
func (m *Module) me(ctx context.Context, tx pgx.Tx) (hris.Employee, error) {
	uid := handle.UserID(ctx)
	if uid == uuid.Nil {
		return hris.Employee{}, errs.Unauthorized("sign in with your personal account")
	}
	e, err := hris.EmployeeByUser(ctx, tx, uid)
	if err != nil {
		return hris.Employee{}, err
	}
	if e == nil {
		return hris.Employee{}, errs.NotFound("employee profile linked to your account")
	}
	return *e, nil
}

const selfSelect = `SELECT e.id, e.property_id, pr.name AS property_name, e.employee_no, e.full_name, e.preferred_name, coalesce(e.job_title, p.name) AS job_title,
	ou.name AS org_unit, p.name AS position, g.code AS grade, s.full_name AS supervisor, e.employment_status, e.join_date, e.probation_end_date, e.email,
	e.personal_email, e.phone, e.address, e.city, e.postal_code, e.marital_status, e.ptkp_status, e.nik, e.npwp, b.bank_name, b.account_no AS bank_account_no,
	b.account_name AS bank_account_name,
	coalesce((SELECT jsonb_agg(jsonb_build_object('name', c.name, 'relationship', c.relationship, 'phone', c.phone, 'address', c.address)
	  ORDER BY c.is_primary DESC, c.name) FROM hris.employee_emergency_contacts c WHERE c.employee_id = e.id AND c.archived_at IS NULL AND c.status = 'active'),
	  '[]'::jsonb) AS emergency_contacts
	FROM hris.employees e JOIN platform.properties pr ON pr.id = e.property_id LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id
	LEFT JOIN hris.positions p ON p.id = e.position_id LEFT JOIN hris.grades g ON g.id = e.grade_id LEFT JOIN hris.employees s ON s.id = e.supervisor_id
	LEFT JOIN LATERAL (SELECT bank_name, account_no, account_name FROM hris.employee_bank_accounts WHERE employee_id = e.id AND status = 'active'
	  AND archived_at IS NULL ORDER BY is_primary DESC, created_at LIMIT 1) b ON true`

func (m *Module) essMeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ESSMe, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return ESSMe{}, err
	}
	self, err := getOne[SelfProfile]("employee")(tx.Query(ctx, selfSelect+` WHERE e.id = $1`, e.ID))
	if err != nil {
		return ESSMe{}, err
	}
	for _, s := range []**string{&self.NIK, &self.NPWP, &self.BankAccountNo} {
		if *s != nil {
			v := MaskTail(**s)
			*s = &v
		}
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return ESSMe{}, err
	}
	out := ESSMe{Employee: self, IsManager: len(team) > 0, TeamSize: len(team), Sections: []hris.ESSSection{}}
	p := authzPrincipal(ctx)
	for _, s := range hris.ESSSections() {
		if s.Manager && !out.IsManager {
			continue
		}
		if p == nil || !p.Can(s.Permission, &e.PropertyID) {
			continue
		}
		if s.Module != "" && s.Module != hris.Module {
			var on bool
			if err := tx.QueryRow(ctx, `SELECT enabled FROM platform.modules WHERE code = $1`, s.Module).Scan(&on); err != nil || !on {
				continue
			}
		}
		out.Sections = append(out.Sections, s)
	}
	day := ymd(today(ctx, tx, e.PropertyID))
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM hris.profile_change_requests WHERE employee_id = $1 AND status = 'submitted'),
		(SELECT count(*) FROM hris.employee_documents WHERE employee_id = $1 AND archived_at IS NULL AND status = 'active' AND expires_on IS NOT NULL
		   AND expires_on <= $2::date + 30) +
		(SELECT count(*) FROM hris.certifications WHERE employee_id = $1 AND archived_at IS NULL AND status IN ('active', 'expired')
		   AND expires_on IS NOT NULL AND expires_on <= $2::date + 30)`, e.ID, day).Scan(&out.PendingChanges, &out.ExpiringItems); err != nil {
		return out, err
	}
	return out, nil
}

func (m *Module) essDocumentsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]EmployeeDocument, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[EmployeeDocument](tx.Query(ctx, documentSelect+` WHERE d.employee_id = $2 AND d.archived_at IS NULL
		ORDER BY d.status, d.document_type, d.created_at DESC`, ymd(today(ctx, tx, e.PropertyID)), e.ID))
	for i := range list {
		if list[i].DocumentNo != nil {
			s := MaskTail(*list[i].DocumentNo)
			list[i].DocumentNo = &s
		}
	}
	return list, err
}

func (m *Module) essDocumentFileHTTP(w http.ResponseWriter, r *http.Request) {
	m.fileOf(w, r, func(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (*uuid.UUID, error) {
		e, err := m.me(ctx, tx)
		if err != nil {
			return nil, err
		}
		var fid *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT file_id FROM hris.employee_documents WHERE id = $1 AND employee_id = $2 AND archived_at IS NULL`, rid, e.ID).
			Scan(&fid); err != nil {
			return nil, errs.NotFound("document")
		}
		return fid, nil
	})
}

func (m *Module) essTrainingHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (MyTraining, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return MyTraining{}, err
	}
	day := ymd(today(ctx, tx, e.PropertyID))
	out := MyTraining{}
	if out.Sessions, err = handle.List[MySession](tx.Query(ctx, `SELECT s.id AS session_id, s.title, tp.name AS program, s.starts_at, s.ends_at, s.location,
		s.status, p.attendance, p.result FROM hris.training_participants p JOIN hris.training_sessions s ON s.id = p.session_id
		JOIN hris.training_programs tp ON tp.id = s.program_id WHERE p.employee_id = $1 AND s.archived_at IS NULL ORDER BY s.starts_at DESC LIMIT 100`,
		e.ID)); err != nil {
		return out, err
	}
	if out.Certifications, err = handle.List[CertificationView](tx.Query(ctx, certificationSelect+` WHERE c.employee_id = $2 AND c.archived_at IS NULL
		AND c.status IN ('active', 'expired') ORDER BY c.expires_on NULLS LAST`, day, e.ID)); err != nil {
		return out, err
	}
	out.Mandatory, err = handle.List[MatrixRow](tx.Query(ctx, `SELECT e.id AS employee_id, e.full_name AS employee_name, NULL::text AS org_unit,
		p.name AS position, tp.id AS program_id, tp.code AS program_code, tp.name AS program_name, last.completed AS last_completed,
		CASE WHEN last.completed IS NOT NULL AND tp.refresher_months IS NOT NULL THEN (last.completed + make_interval(months => tp.refresher_months))::date END
		  AS due_date,
		CASE WHEN last.completed IS NULL THEN 'missing' WHEN tp.refresher_months IS NOT NULL
		  AND (last.completed + make_interval(months => tp.refresher_months))::date < $2::date THEN 'due' ELSE 'compliant' END AS status
		FROM hris.employees e JOIN hris.positions p ON p.id = e.position_id
		JOIN hris.training_programs tp ON tp.property_id = e.property_id AND p.code = ANY (tp.required_positions) AND tp.archived_at IS NULL AND tp.status = 'active'
		LEFT JOIN LATERAL (SELECT (max(s.ends_at) AT TIME ZONE coalesce((SELECT nullif(x.timezone, '') FROM platform.properties x WHERE x.id = e.property_id),
		  (SELECT timezone FROM platform.instance)))::date AS completed FROM hris.training_participants pa JOIN hris.training_sessions s ON s.id = pa.session_id
		  WHERE pa.employee_id = e.id AND s.program_id = tp.id AND s.status = 'completed' AND pa.attendance = 'attended'
		  AND coalesce(pa.result, 'passed') = 'passed') last ON true
		WHERE e.id = $1 ORDER BY tp.name`, e.ID, day))
	return out, err
}

func (m *Module) loadChangeRequest(ctx context.Context, tx pgx.Tx, where string, args ...any) (ProfileChange, error) {
	c, err := getOne[ProfileChange]("data change")(tx.Query(ctx, changeRequestSelect+` WHERE `+where, args...))
	c.Fields = c.Changes.fields()
	return c, err
}

func maskChange(ctx context.Context, c *ProfileChange) {
	c.Fields = c.Changes.fields()
	if c.Changes.BankAccount != nil && !can(ctx, "hris.employee.view_sensitive", c.PropertyID) {
		b := *c.Changes.BankAccount
		b.AccountNo = MaskTail(b.AccountNo)
		c.Changes.BankAccount = &b
	}
}

func (m *Module) essChangesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ProfileChange, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[ProfileChange](tx.Query(ctx, changeRequestSelect+` WHERE r.employee_id = $1 ORDER BY r.created_at DESC LIMIT 50`, e.ID))
	for i := range list {
		list[i].Fields = list[i].Changes.fields()
		if b := list[i].Changes.BankAccount; b != nil {
			cp := *b
			cp.AccountNo = MaskTail(cp.AccountNo)
			list[i].Changes.BankAccount = &cp
		}
	}
	return list, err
}

// validateChanges checks a data change against the HR Configuration.
func validateChanges(c *ProfileChanges, allowed []string) error {
	fields := c.fields()
	if len(fields) == 0 {
		return handle.Invalid("changes", "required", "change at least one field")
	}
	for _, f := range fields {
		if !slices.Contains(allowed, f) {
			return handle.Invalid("changes."+f, "not_allowed", "this field is changed by HR only")
		}
	}
	trim := func(p **string) {
		if *p != nil {
			s := strings.TrimSpace(**p)
			*p = &s
		}
	}
	for _, p := range []**string{&c.Phone, &c.PersonalEmail, &c.Address, &c.City, &c.PostalCode} {
		trim(p)
	}
	if c.PersonalEmail != nil && *c.PersonalEmail != "" && !strings.Contains(*c.PersonalEmail, "@") {
		return handle.Invalid("changes.personalEmail", "invalid", "must be a valid e-mail address")
	}
	if c.MaritalStatus != nil && !oneOf([]string{"single", "married", "divorced", "widowed"}, *c.MaritalStatus) {
		return enumErr("changes.maritalStatus", []string{"single", "married", "divorced", "widowed"})
	}
	if c.PTKPStatus != nil && !oneOf([]string{"TK/0", "TK/1", "TK/2", "TK/3", "K/0", "K/1", "K/2", "K/3"}, *c.PTKPStatus) {
		return handle.Invalid("changes.ptkpStatus", "invalid", "one of TK/0–TK/3, K/0–K/3")
	}
	rel := []string{"spouse", "parent", "child", "sibling", "relative", "friend", "other"}
	for i, ec := range c.EmergencyContacts {
		if strings.TrimSpace(ec.Name) == "" || strings.TrimSpace(ec.Phone) == "" {
			return handle.Invalid("changes.emergencyContacts", "invalid", "contact "+itoa(i+1)+": name and phone are required")
		}
		if ec.Relationship == "" {
			c.EmergencyContacts[i].Relationship = "other"
		} else if !oneOf(rel, ec.Relationship) {
			return enumErr("changes.emergencyContacts", rel)
		}
	}
	if b := c.BankAccount; b != nil {
		if strings.TrimSpace(b.BankName) == "" || strings.TrimSpace(b.AccountName) == "" || !accountNoRe.MatchString(strings.TrimSpace(b.AccountNo)) {
			return handle.Invalid("changes.bankAccount", "invalid", "bank, account number (digits) and account name are required")
		}
		b.AccountNo = strings.TrimSpace(b.AccountNo)
	}
	return nil
}

func (m *Module) essSubmitChangeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ProfileChangeRequest) (ProfileChange, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return ProfileChange{}, err
	}
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, e.PropertyID, clock.Now())
	if err != nil {
		return ProfileChange{}, err
	}
	if err := validateChanges(&req.Changes, cfg.ProfileChangeFields); err != nil {
		return ProfileChange{}, err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.profile_change_requests WHERE employee_id = $1 AND status = 'submitted')`, e.ID).
		Scan(&open); err != nil {
		return ProfileChange{}, err
	}
	if open {
		return ProfileChange{}, errs.Conflict("change_pending", "a data change is waiting for HR; withdraw it to send a new one")
	}
	raw, _ := json.Marshal(req.Changes)
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.profile_change_requests (id, property_id, employee_id, changes, submitted_by) VALUES ($1,$2,$3,$4,$5)`,
		rid, e.PropertyID, e.ID, raw, actor(ctx)); err != nil {
		return ProfileChange{}, err
	}
	out, err := m.loadChangeRequest(ctx, tx, `r.id = $1`, rid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.profile_change", EntityID: rid.String(),
		EntityLabel: e.EmployeeNo + " · " + e.FullName, PropertyID: &e.PropertyID, After: map[string]any{"fields": out.Fields}}); err != nil {
		return out, err
	}
	if err := m.notifyUsers(ctx, tx, e.PropertyID, hrUsers(ctx, tx, e.PropertyID, "hris.profile_change.review"), "hris.profile_change_submitted",
		"/hris/profile-changes", map[string]any{"employeeName": e.FullName, "employeeNo": e.EmployeeNo, "fields": strings.Join(out.Fields, ", ")}); err != nil {
		return out, err
	}
	maskChange(ctx, &out)
	if b := out.Changes.BankAccount; b != nil {
		cp := *b
		cp.AccountNo = MaskTail(cp.AccountNo)
		out.Changes.BankAccount = &cp
	}
	return out, nil
}

func (m *Module) essCancelChangeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (ProfileChange, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return ProfileChange{}, err
	}
	rid, err := handle.ID(r)
	if err != nil {
		return ProfileChange{}, err
	}
	before, err := m.loadChangeRequest(ctx, tx, `r.id = $1 AND r.employee_id = $2`, rid, e.ID)
	if err != nil {
		return before, err
	}
	if before.Status != "submitted" {
		return before, errs.Conflict("change_closed", "the data change was already "+before.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.profile_change_requests SET status = 'cancelled' WHERE id = $1`, rid); err != nil {
		return before, err
	}
	after, err := m.loadChangeRequest(ctx, tx, `r.id = $1`, rid)
	if err != nil {
		return after, err
	}
	maskChange(ctx, &after)
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionStatusChange, EntityType: "hris.profile_change",
		EntityID: rid.String(), EntityLabel: e.EmployeeNo + " · " + e.FullName, PropertyID: &e.PropertyID,
		Before: map[string]any{"status": before.Status}, After: map[string]any{"status": after.Status}})
}

func (m *Module) hrChangesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ProfileChange, error) {
	list, err := handle.List[ProfileChange](tx.Query(ctx, changeRequestSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
		ORDER BY (r.status = 'submitted') DESC, r.created_at DESC LIMIT 500`, handle.Property(ctx), r.URL.Query().Get("status")))
	for i := range list {
		maskChange(ctx, &list[i])
	}
	return list, err
}

func (m *Module) reviewHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewRequest) (ProfileChange, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewRequest) (ProfileChange, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return ProfileChange{}, err
		}
		property := handle.Property(ctx)
		before, err := m.loadChangeRequest(ctx, tx, `r.id = $1 AND r.property_id = $2`, rid, property)
		if err != nil {
			return before, err
		}
		if before.Status != "submitted" {
			return before, errs.Conflict("change_closed", "the data change was already "+before.Status)
		}
		if !approve && strings.TrimSpace(req.Note) == "" {
			return before, handle.Invalid("note", "required", "tell the employee why")
		}
		status := "rejected"
		if approve {
			status = "approved"
			if err := m.applyChanges(ctx, tx, before.EmployeeID, before.PropertyID, before.Changes); err != nil {
				return before, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.profile_change_requests SET status = $2, reviewed_by = $3, reviewed_at = now(), review_note = $4 WHERE id = $1`,
			rid, status, actor(ctx), nullStr(req.Note)); err != nil {
			return before, err
		}
		after, err := m.loadChangeRequest(ctx, tx, `r.id = $1`, rid)
		if err != nil {
			return after, err
		}
		maskChange(ctx, &after)
		if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[bool]string{true: "approve", false: "reject"}[approve],
			EntityType: "hris.profile_change", EntityID: rid.String(), EntityLabel: before.EmployeeNo + " · " + before.EmployeeName, PropertyID: &property,
			Reason: req.Note, Before: map[string]any{"status": before.Status}, After: map[string]any{"status": status, "fields": after.Fields}}); err != nil {
			return after, err
		}
		var user *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1 AND status = 'active'`, before.EmployeeID).Scan(&user)
		if user != nil {
			if err := m.notifyUsers(ctx, tx, property, []uuid.UUID{*user}, "hris.profile_change_decided", "/ops/ess/profile",
				map[string]any{"status": status, "fields": strings.Join(after.Fields, ", "), "note": req.Note}); err != nil {
				return after, err
			}
		}
		return after, nil
	}
}

// applyChanges writes a verified data change to the employee.
func (m *Module) applyChanges(ctx context.Context, tx pgx.Tx, eid, property uuid.UUID, c ProfileChanges) error {
	if _, err := tx.Exec(ctx, `UPDATE hris.employees SET phone = coalesce($2, phone), personal_email = coalesce(nullif($3, ''), personal_email),
		address = coalesce($4, address), city = coalesce($5, city), postal_code = coalesce($6, postal_code), marital_status = coalesce($7, marital_status),
		ptkp_status = coalesce($8, ptkp_status), updated_by = $9 WHERE id = $1`,
		eid, c.Phone, c.PersonalEmail, c.Address, c.City, c.PostalCode, c.MaritalStatus, c.PTKPStatus, actor(ctx)); err != nil {
		return err
	}
	if c.EmergencyContacts != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.employee_emergency_contacts SET archived_at = now() WHERE employee_id = $1 AND archived_at IS NULL`, eid); err != nil {
			return err
		}
		for i, ec := range c.EmergencyContacts {
			if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_emergency_contacts (id, property_id, employee_id, name, relationship, phone, address, is_primary,
				created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, id.New(), property, eid, strings.TrimSpace(ec.Name), ec.Relationship,
				strings.TrimSpace(ec.Phone), ec.Address, i == 0, actor(ctx)); err != nil {
				return err
			}
		}
	}
	if b := c.BankAccount; b != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.employee_bank_accounts SET is_primary = false WHERE employee_id = $1 AND is_primary`, eid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employee_bank_accounts (id, property_id, employee_id, bank_code, bank_name, account_no, account_name, branch,
			is_primary, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,$9,$9)`, id.New(), property, eid, b.BankCode, b.BankName, b.AccountNo,
			b.AccountName, b.Branch, actor(ctx)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) essTeamHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]TeamMember, error) {
	e, err := m.me(ctx, tx)
	if err != nil {
		return nil, err
	}
	team, err := hris.Team(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	day := today(ctx, tx, e.PropertyID)
	out := []TeamMember{}
	for _, t := range team {
		tm := TeamMember{ID: t.ID, EmployeeNo: t.EmployeeNo, FullName: t.FullName, JobTitle: t.JobTitle, OrgUnit: t.OrgUnitName, Phone: t.Phone,
			EmploymentStatus: t.EmploymentStatus, Direct: t.SupervisorID != nil && *t.SupervisorID == e.ID, CertificationGaps: []hris.CertificationGap{}}
		c, err := hris.ContractAt(ctx, tx, t.ID, day)
		if err != nil {
			return nil, err
		}
		if c != nil && c.EndDate != nil {
			tm.ContractEnds = c.EndDate
		}
		chk, err := hris.CheckEmployee(ctx, tx, t.ID, "", day)
		if err != nil {
			return nil, err
		}
		tm.CertificationGaps = chk.Gaps
		out = append(out, tm)
	}
	return out, nil
}
