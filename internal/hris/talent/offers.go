package talent

// Offers and the hire (FR-RCT-04): HR makes an offer to a candidate in the
// Interview stage (position, contract PKWT / PKWTT, start, salary and
// allowances); the offer goes through the approval engine (document type
// hris.job_offer — e.g. a GM step for offers above the requisition budget;
// without a workflow it is approved at once), is sent with its offer letter
// (PDF) and the candidate's answer is recorded. Hiring an application with
// an accepted offer creates, through Core HR, the employee (which publishes
// hris.employee_hired), the active contract and the Employee Self Service
// login, plus the onboarding checklist of the Recruitment Configuration;
// the requisition counts the hire and is Filled with its last position.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/org"
)

// Offer statuses.
var OfferStatuses = []string{"draft", "submitted", "approved", "rejected", "sent", "accepted", "declined", "cancelled", "expired"}

// JobOffer is an offer to a candidate.
type JobOffer struct {
	ID                uuid.UUID        `json:"id" db:"id"`
	PropertyID        uuid.UUID        `json:"propertyId" db:"property_id"`
	Number            string           `json:"number" db:"number"`
	ApplicationID     uuid.UUID        `json:"applicationId" db:"application_id"`
	ApplicationNo     string           `json:"applicationNumber" db:"application_number"`
	RequisitionID     uuid.UUID        `json:"requisitionId" db:"requisition_id"`
	CandidateName     string           `json:"candidateName" db:"candidate_name"`
	OrgUnitID         *uuid.UUID       `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName       *string          `json:"orgUnitName" db:"org_unit_name"`
	PositionID        *uuid.UUID       `json:"positionId" db:"position_id"`
	PositionName      *string          `json:"positionName" db:"position_name"`
	GradeID           *uuid.UUID       `json:"gradeId" db:"grade_id"`
	GradeCode         *string          `json:"gradeCode" db:"grade_code"`
	JobTitle          *string          `json:"jobTitle" db:"job_title"`
	ContractType      string           `json:"contractType" db:"contract_type" enum:"pkwt,pkwtt"`
	StartDate         time.Time        `json:"startDate" db:"start_date"`
	EndDate           *time.Time       `json:"endDate" db:"end_date"`
	ProbationMonths   int              `json:"probationMonths" db:"probation_months"`
	Currency          string           `json:"currency" db:"currency"`
	BaseSalary        string           `json:"baseSalary" db:"base_salary" doc:"Masked without hris.job_offer.view_salary"`
	Allowances        []hris.Allowance `json:"allowances" db:"allowances"`
	WorkWeekDays      int              `json:"workWeekDays" db:"work_week_days"`
	ExpiresOn         *time.Time       `json:"expiresOn" db:"expires_on"`
	Notes             *string          `json:"notes" db:"notes"`
	Status            string           `json:"status" db:"status" enum:"draft,submitted,approved,rejected,sent,accepted,declined,cancelled,expired"`
	ApprovalRequestID *uuid.UUID       `json:"approvalRequestId" db:"approval_request_id"`
	SubmittedAt       *time.Time       `json:"submittedAt" db:"submitted_at"`
	ApprovedAt        *time.Time       `json:"approvedAt" db:"approved_at"`
	SentAt            *time.Time       `json:"sentAt" db:"sent_at"`
	RespondedAt       *time.Time       `json:"respondedAt" db:"responded_at"`
	ResponseNote      *string          `json:"responseNote" db:"response_note"`
	DecisionNote      *string          `json:"decisionNote" db:"decision_note"`
	PolicyVersion     int              `json:"policyVersion" db:"policy_version" doc:"Recruitment Configuration version of the offer"`
	CreatedAt         time.Time        `json:"createdAt" db:"created_at"`
}

const offerSelect = `SELECT o.id, o.property_id, o.number, o.application_id, a.number AS application_number, a.requisition_id, c.full_name AS candidate_name,
	o.org_unit_id, ou.name AS org_unit_name, o.position_id, p.name AS position_name, o.grade_id, g.code AS grade_code, o.job_title, o.contract_type,
	o.start_date, o.end_date, o.probation_months, o.currency, trim_scale(o.base_salary)::text AS base_salary, o.allowances, o.work_week_days, o.expires_on,
	o.notes, o.status, o.approval_request_id, o.submitted_at, o.approved_at, o.sent_at, o.responded_at, o.response_note, o.decision_note, o.policy_version,
	o.created_at
	FROM hris.job_offers o JOIN hris.applications a ON a.id = o.application_id JOIN hris.candidates c ON c.id = a.candidate_id
	LEFT JOIN hris.org_units ou ON ou.id = o.org_unit_id LEFT JOIN hris.positions p ON p.id = o.position_id LEFT JOIN hris.grades g ON g.id = o.grade_id`

// JobOfferRequest makes an offer to an application.
type JobOfferRequest struct {
	PositionID      *uuid.UUID       `json:"positionId,omitempty" doc:"Default: the requisition's"`
	OrgUnitID       *uuid.UUID       `json:"orgUnitId,omitempty" doc:"Default: the position's / requisition's"`
	GradeID         *uuid.UUID       `json:"gradeId,omitempty" doc:"Default: the requisition's"`
	JobTitle        string           `json:"jobTitle,omitempty"`
	ContractType    string           `json:"contractType,omitempty" enum:"pkwt,pkwtt" doc:"Default: the requisition's"`
	StartDate       string           `json:"startDate"`
	EndDate         string           `json:"endDate,omitempty" doc:"PKWT end date"`
	ProbationMonths int              `json:"probationMonths,omitempty" doc:"PKWTT only"`
	BaseSalary      string           `json:"baseSalary"`
	Allowances      []hris.Allowance `json:"allowances,omitempty"`
	WorkWeekDays    int              `json:"workWeekDays,omitempty" enum:"5,6"`
	Notes           string           `json:"notes,omitempty"`
	Draft           bool             `json:"draft,omitempty" doc:"Keep as draft instead of submitting for approval"`
}

// JobOfferUpdate edits a draft or rejected offer.
type JobOfferUpdate struct {
	PositionID      *uuid.UUID       `json:"positionId,omitempty"`
	GradeID         *uuid.UUID       `json:"gradeId,omitempty"`
	JobTitle        *string          `json:"jobTitle,omitempty"`
	ContractType    *string          `json:"contractType,omitempty" enum:"pkwt,pkwtt"`
	StartDate       *string          `json:"startDate,omitempty"`
	EndDate         *string          `json:"endDate,omitempty"`
	ProbationMonths *int             `json:"probationMonths,omitempty"`
	BaseSalary      *string          `json:"baseSalary,omitempty"`
	Allowances      []hris.Allowance `json:"allowances,omitempty"`
	WorkWeekDays    *int             `json:"workWeekDays,omitempty"`
	Notes           *string          `json:"notes,omitempty"`
}

// JobOfferSendRequest sends an approved offer.
type JobOfferSendRequest struct {
	ExpiresOn string `json:"expiresOn,omitempty" doc:"Default: today + the offer validity of the Recruitment Configuration"`
	Email     bool   `json:"email,omitempty" doc:"E-mail the offer to the candidate"`
}

// ApplicationHireRequest hires an application with an accepted offer.
type ApplicationHireRequest struct {
	EmployeeNo   string     `json:"employeeNo,omitempty" doc:"Default: next number of the HR Configuration"`
	WorkEmail    string     `json:"workEmail,omitempty"`
	SupervisorID *uuid.UUID `json:"supervisorId,omitempty" doc:"Default: the hiring manager of the requisition"`
	CreateLogin  *bool      `json:"createLogin,omitempty" doc:"Default: Recruitment Configuration createLoginOnHire (needs an e-mail)"`
	RoleCodes    []string   `json:"roleCodes,omitempty" doc:"Roles of the login (default: the self-service role)"`
}

// ApplicationHireResult is the outcome of a hire.
type ApplicationHireResult struct {
	Application JobApplication   `json:"application"`
	Employee    hris.HireResult  `json:"employee"`
	Onboarding  []OnboardingTask `json:"onboarding"`
	Requisition JobRequisition   `json:"requisition"`
}

// OnboardingTask is an onboarding checklist item of a new employee.
type OnboardingTask struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	EmployeeID uuid.UUID  `json:"employeeId" db:"employee_id"`
	Code       string     `json:"code" db:"code"`
	Label      string     `json:"label" db:"label"`
	DueDate    *time.Time `json:"dueDate" db:"due_date"`
	Status     string     `json:"status" db:"status" enum:"pending,done,not_applicable"`
	DoneAt     *time.Time `json:"doneAt" db:"done_at"`
	DoneByName *string    `json:"doneByName" db:"done_by_name"`
	Notes      *string    `json:"notes" db:"notes"`
}

// OnboardingTaskUpdate ticks an onboarding item.
type OnboardingTaskUpdate struct {
	Status string  `json:"status" enum:"pending,done,not_applicable"`
	Notes  *string `json:"notes,omitempty"`
}

const onboardingSelect = `SELECT o.id, o.employee_id, o.code, o.label, o.due_date, o.status, o.done_at, u.full_name AS done_by_name, o.notes
	FROM hris.onboarding_items o LEFT JOIN platform.users u ON u.id = o.done_by`

func (m *Module) registerOffers(reg *route.Registry) {
	tag := "HRIS Recruitment"
	base := "/api/v1/hris/job-offers"
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/applications/{id}:offer", Summary: "Make an offer (submitted for approval)",
		Permission: "hris.job_offer.manage", Request: JobOfferRequest{}, Response: JobOffer{}, Status: http.StatusCreated, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createOfferHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Job offers", Permission: "hris.job_offer.view", Response: JobOffer{}, List: true,
		Query: []route.Param{{Name: "status", Enum: OfferStatuses}, {Name: "applicationId"}}, Handler: listRead(m.DB, m.listOffersHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Job offer", Permission: "hris.job_offer.view", Response: JobOffer{},
		Handler: handle.Read(m.DB, m.getOfferHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a draft or rejected offer", Permission: "hris.job_offer.manage",
		Request: JobOfferUpdate{}, Response: JobOffer{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateOfferHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:submit", Summary: "Submit an offer for approval", Permission: "hris.job_offer.manage",
		Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.submitOfferHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:approve", Summary: "Approve the offer (current approval step)",
		Permission: "hris.job_offer.approve", Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideOfferHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject the offer (current approval step)",
		Permission: "hris.job_offer.approve", Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideOfferHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:send", Summary: "Send an approved offer to the candidate",
		Permission: "hris.job_offer.manage", Request: JobOfferSendRequest{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.sendOfferHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:accept", Summary: "Record that the candidate accepted",
		Permission: "hris.job_offer.manage", Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.answerOfferHTTP("accepted"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:decline", Summary: "Record that the candidate declined (application withdrawn)",
		Permission: "hris.job_offer.manage", Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.answerOfferHTTP("declined"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel an offer (application back to Interview)",
		Permission: "hris.job_offer.manage", Request: RecruitmentReason{}, Response: JobOffer{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.answerOfferHTTP("cancelled"))})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/letter", Summary: "Offer letter (PDF)", Permission: "hris.job_offer.view_salary",
		RawContent: "application/pdf", Handler: m.letterHTTP})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/applications/{id}:hire",
		Summary: "Hire: create the employee, contract, login and onboarding checklist", Permission: "hris.application.hire",
		Request: ApplicationHireRequest{}, Response: ApplicationHireResult{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.hireHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/onboarding", Summary: "Onboarding checklist of an employee",
		Permission: "hris.application.view", Response: OnboardingTask{}, List: true, Handler: listRead(m.DB, m.onboardingHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/onboarding-items/{id}", Summary: "Tick an onboarding checklist item",
		Permission: "hris.application.hire", Request: OnboardingTaskUpdate{}, Response: OnboardingTask{}, Handler: handle.Write(m.DB, http.StatusOK, m.onboardingItemHTTP)})
}

func maskOffer(ctx context.Context, o *JobOffer) {
	if o.Allowances == nil {
		o.Allowances = []hris.Allowance{}
	}
	if can(ctx, "hris.job_offer.view_salary", o.PropertyID) {
		return
	}
	o.BaseSalary = mask.Redacted
	for i := range o.Allowances {
		o.Allowances[i].Amount = mask.Redacted
	}
}

// offers lists offers matching a condition ($1 = property).
func (m *Module) offers(ctx context.Context, tx pgx.Tx, cond string, args ...any) ([]JobOffer, error) {
	list, err := handle.List[JobOffer](tx.Query(ctx, offerSelect+` WHERE o.property_id = $1 AND `+cond+` ORDER BY o.created_at DESC`,
		append([]any{handle.Property(ctx)}, args...)...))
	for i := range list {
		maskOffer(ctx, &list[i])
	}
	return list, err
}

func (m *Module) offer(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (JobOffer, error) {
	list, err := m.offers(ctx, tx, `o.id = $2`, oid)
	if err != nil {
		return JobOffer{}, err
	}
	if len(list) == 0 {
		return JobOffer{}, errs.NotFound("job offer")
	}
	return list[0], nil
}

func (m *Module) listOffersHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]JobOffer, error) {
	app, err := uuidParam(r, "applicationId")
	if err != nil {
		return nil, err
	}
	return m.offers(ctx, tx, `($2 = '' OR o.status = $2) AND ($3::uuid IS NULL OR o.application_id = $3)`, filterParam(r, "status"), app)
}

func (m *Module) getOfferHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (JobOffer, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return JobOffer{}, err
	}
	return m.offer(ctx, tx, oid)
}

// offerTerms are validated offer terms.
type offerTerms struct {
	unit, pos, grade *uuid.UUID
	title            *string
	ctype            string
	start            time.Time
	end              *time.Time
	probation        int
	base             decimal.Decimal
	allowances       []hris.Allowance
	weekDays         int
	notes            *string
}

// validateTerms applies the HR Configuration contract rules (PKWT end
// date, no probation on PKWT, probation limit).
func validateTerms(ctx context.Context, tx pgx.Tx, property uuid.UUID, t *offerTerms) error {
	cfg, _, err := hris.LoadHRConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return err
	}
	switch t.ctype {
	case "pkwt":
		if t.end == nil {
			return handle.Invalid("endDate", "required", "a PKWT has an end date")
		}
		if t.end.Before(t.start) {
			return handle.Invalid("endDate", "invalid", "must be on or after the start date")
		}
		if t.probation > 0 {
			return handle.Invalid("probationMonths", "invalid", "probation is not allowed on a PKWT (PP 35/2021)")
		}
		if cfg.PKWTMaxMonths > 0 && !t.end.Before(t.start.AddDate(0, cfg.PKWTMaxMonths, 0)) {
			return handle.Invalid("endDate", "pkwt_too_long", "a PKWT lasts at most "+itoa(cfg.PKWTMaxMonths)+" months")
		}
	case "pkwtt":
		if t.end != nil {
			return handle.Invalid("endDate", "invalid", "a PKWTT has no end date")
		}
		if t.probation < 0 || t.probation > cfg.ProbationMaxMonths {
			return handle.Invalid("probationMonths", "invalid", "probation is at most "+itoa(cfg.ProbationMaxMonths)+" months")
		}
	default:
		return enumErr("contractType", []string{"pkwt", "pkwtt"})
	}
	if t.weekDays == 0 {
		t.weekDays = 5
	}
	if t.weekDays != 5 && t.weekDays != 6 {
		return handle.Invalid("workWeekDays", "invalid", "5 or 6")
	}
	if !t.base.IsPositive() {
		return handle.Invalid("baseSalary", "required", "a base salary above 0")
	}
	for i, a := range t.allowances {
		a.Code, a.Name = strings.TrimSpace(a.Code), strings.TrimSpace(a.Name)
		v, err := decimal.NewFromString(strings.TrimSpace(a.Amount))
		if a.Name == "" || err != nil || v.IsNegative() {
			return handle.Invalid("allowances", "invalid", "every allowance has a name and an amount of 0 or more")
		}
		if a.Code == "" {
			a.Code = strings.ToUpper(strings.ReplaceAll(a.Name, " ", "_"))
		}
		a.Amount = v.String()
		t.allowances[i] = a
	}
	if t.allowances == nil {
		t.allowances = []hris.Allowance{}
	}
	if t.pos != nil {
		var unit uuid.UUID
		var grade *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT org_unit_id, grade_id FROM hris.positions WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, *t.pos,
			property).Scan(&unit, &grade); err != nil {
			return handle.Invalid("positionId", "not_found", "position not found")
		}
		t.unit = &unit
		if t.grade == nil {
			t.grade = grade
		}
	}
	return nil
}

func (m *Module) createOfferHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req JobOfferRequest) (JobOffer, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return JobOffer{}, err
	}
	a, err := lockApplication(ctx, tx, aid)
	if err != nil {
		return JobOffer{}, err
	}
	if a.Stage != hris.StageInterview && a.Stage != hris.StageScreening {
		return JobOffer{}, errs.Conflict("invalid_stage", "an offer is made to an application in Screening or Interview (now "+a.Stage+")")
	}
	if err := requisitionLive(ctx, tx, a.RequisitionID); err != nil {
		return JobOffer{}, err
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.job_offers WHERE application_id = $1 AND status IN ('draft', 'submitted', 'approved', 'sent',
		'accepted'))`, aid).Scan(&live); err != nil {
		return JobOffer{}, err
	}
	if live {
		return JobOffer{}, errs.Conflict("offer_exists", "the application already has an offer in progress")
	}
	var rq struct {
		unit         uuid.UUID
		pos, grade   *uuid.UUID
		ctype, title string
	}
	if err := tx.QueryRow(ctx, `SELECT org_unit_id, position_id, grade_id, contract_type, title FROM hris.job_requisitions WHERE id = $1`, a.RequisitionID).
		Scan(&rq.unit, &rq.pos, &rq.grade, &rq.ctype, &rq.title); err != nil {
		return JobOffer{}, err
	}
	t := offerTerms{unit: req.OrgUnitID, pos: req.PositionID, grade: req.GradeID, title: nullStr(req.JobTitle), ctype: req.ContractType,
		probation: req.ProbationMonths, allowances: req.Allowances, weekDays: req.WorkWeekDays, notes: nullStr(req.Notes)}
	if t.pos == nil {
		t.pos = rq.pos
	}
	if t.unit == nil {
		t.unit = &rq.unit
	}
	if t.grade == nil {
		t.grade = rq.grade
	}
	if t.ctype == "" {
		t.ctype = rq.ctype
	}
	if t.title == nil {
		t.title = &rq.title
	}
	if t.start, err = mustDate("startDate", req.StartDate); err != nil {
		return JobOffer{}, err
	}
	if t.end, err = parseDate("endDate", req.EndDate); err != nil {
		return JobOffer{}, err
	}
	base, err := money("baseSalary", req.BaseSalary)
	if err != nil {
		return JobOffer{}, err
	}
	if base != nil {
		t.base = *base
	}
	if err := validateTerms(ctx, tx, a.PropertyID, &t); err != nil {
		return JobOffer{}, err
	}
	cfg, ref, err := hris.LoadRecruitmentConfiguration(ctx, tx, a.PropertyID, clock.Now())
	if err != nil {
		return JobOffer{}, err
	}
	no, err := yearlyNumber(ctx, tx, a.PropertyID, strings.ToUpper(cfg.OfferPrefix), today(ctx, tx, a.PropertyID).Year())
	if err != nil {
		return JobOffer{}, err
	}
	raw, _ := json.Marshal(t.allowances)
	oid := id.New()
	cur := "IDR"
	_ = tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&cur)
	if _, err := tx.Exec(ctx, `INSERT INTO hris.job_offers (id, property_id, number, application_id, org_unit_id, position_id, grade_id, job_title, contract_type,
		start_date, end_date, probation_months, currency, base_salary, allowances, work_week_days, notes, policy_version, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15,$16,$17,$18,$19,$19)`,
		oid, a.PropertyID, no, aid, t.unit, t.pos, t.grade, t.title, t.ctype, t.start, t.end, t.probation, cur, t.base.String(), raw, t.weekDays, t.notes,
		ref.Version, actor(ctx)); err != nil {
		return JobOffer{}, err
	}
	out, err := m.offer(ctx, tx, oid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.job_offer", EntityID: oid.String(),
		EntityLabel: no + " · " + a.Number, PropertyID: &a.PropertyID, After: offerAudit(out)}); err != nil {
		return out, err
	}
	if req.Draft {
		return out, nil
	}
	return m.submitOffer(ctx, tx, oid, "")
}

// offerAudit is the audit snapshot (salary recorded, never its amount).
func offerAudit(o JobOffer) map[string]any {
	return map[string]any{"number": o.Number, "applicationId": o.ApplicationID, "positionId": o.PositionID, "gradeId": o.GradeID,
		"contractType": o.ContractType, "startDate": ymd(o.StartDate), "status": o.Status, "salaryRecorded": true}
}

// lockOffer loads an offer for update.
func lockOffer(ctx context.Context, tx pgx.Tx, oid uuid.UUID) (string, uuid.UUID, uuid.UUID, *uuid.UUID, error) {
	var status string
	var property, app uuid.UUID
	var req *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, property_id, application_id, approval_request_id FROM hris.job_offers WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		oid, handle.Property(ctx)).Scan(&status, &property, &app, &req); err != nil {
		return "", property, app, req, errs.NotFound("job offer")
	}
	return status, property, app, req, nil
}

// submitOffer moves the application to Offered and asks approval.
func (m *Module) submitOffer(ctx context.Context, tx pgx.Tx, oid uuid.UUID, note string) (JobOffer, error) {
	status, property, aid, _, err := lockOffer(ctx, tx, oid)
	if err != nil {
		return JobOffer{}, err
	}
	if status != "draft" && status != "rejected" {
		return JobOffer{}, errs.Conflict("offer_not_draft", "only a draft or rejected offer can be submitted")
	}
	a, err := lockApplication(ctx, tx, aid)
	if err != nil {
		return JobOffer{}, err
	}
	if a.Stage != hris.StageOffered {
		if hris.StageRank(a.Stage) < 0 {
			return JobOffer{}, errs.Conflict("application_closed", "the application is "+a.Stage)
		}
		if err := stageEvent(ctx, tx, a, hris.StageOffered, "Offer submitted"); err != nil {
			return JobOffer{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = 'submitted', submitted_at = now(), decision_note = NULL, updated_by = $2 WHERE id = $1`,
		oid, actor(ctx)); err != nil {
		return JobOffer{}, err
	}
	o, err := m.offer(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	var salary float64
	var level *int
	var budget *float64
	if err := tx.QueryRow(ctx, `SELECT o.base_salary::float8, g.level, r.salary_max::float8 FROM hris.job_offers o
		JOIN hris.applications a ON a.id = o.application_id JOIN hris.job_requisitions r ON r.id = a.requisition_id
		LEFT JOIN hris.grades g ON g.id = o.grade_id WHERE o.id = $1`, oid).Scan(&salary, &level, &budget); err != nil {
		return o, err
	}
	above := 0.0
	if budget != nil && salary > *budget {
		above = 1
	}
	attrs := map[string]any{"baseSalary": salary, "aboveBudget": above, "contractType": o.ContractType}
	if level != nil {
		attrs["gradeLevel"] = float64(*level)
	}
	if o.OrgUnitID != nil {
		attrs["orgUnitId"] = o.OrgUnitID.String()
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit", EntityType: "hris.job_offer", EntityID: oid.String(),
		EntityLabel: o.Number + " · " + o.ApplicationNo, PropertyID: &property, Reason: note, Before: map[string]any{"status": status},
		After: map[string]any{"status": "submitted"}}); err != nil {
		return o, err
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: OfferDocumentType.Code, DocumentID: oid, DocumentRef: o.Number,
		Title: "Job offer " + o.Number + " · " + deref(o.JobTitle), PropertyID: property, Attributes: attrs})
	if err != nil {
		return o, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET approval_request_id = $2 WHERE id = $1`, oid, rid); err != nil {
		return o, err
	}
	return m.offer(ctx, tx, oid)
}

func (m *Module) submitOfferHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobOffer, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return JobOffer{}, err
	}
	return m.submitOffer(ctx, tx, oid, req.Reason)
}

func (m *Module) updateOfferHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req JobOfferUpdate) (JobOffer, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return JobOffer{}, err
	}
	status, property, _, _, err := lockOffer(ctx, tx, oid)
	if err != nil {
		return JobOffer{}, err
	}
	if status != "draft" && status != "rejected" {
		return JobOffer{}, errs.Conflict("offer_not_draft", "only a draft or rejected offer can be edited")
	}
	var t offerTerms
	var raw []byte
	var base string
	if err := tx.QueryRow(ctx, `SELECT org_unit_id, position_id, grade_id, job_title, contract_type, start_date, end_date, probation_months,
		base_salary::text, allowances, work_week_days, notes FROM hris.job_offers WHERE id = $1`, oid).Scan(&t.unit, &t.pos, &t.grade, &t.title, &t.ctype,
		&t.start, &t.end, &t.probation, &base, &raw, &t.weekDays, &t.notes); err != nil {
		return JobOffer{}, err
	}
	t.base = hris.Dec(base)
	_ = json.Unmarshal(raw, &t.allowances)
	before, err := m.offer(ctx, tx, oid)
	if err != nil {
		return before, err
	}
	if req.PositionID != nil {
		t.pos, t.grade = req.PositionID, nil
	}
	if req.GradeID != nil {
		t.grade = req.GradeID
	}
	if req.JobTitle != nil {
		t.title = nullStr(*req.JobTitle)
	}
	if req.ContractType != nil {
		t.ctype = *req.ContractType
	}
	if req.StartDate != nil {
		if t.start, err = mustDate("startDate", *req.StartDate); err != nil {
			return before, err
		}
	}
	if req.EndDate != nil {
		if t.end, err = parseDate("endDate", *req.EndDate); err != nil {
			return before, err
		}
	}
	if req.ProbationMonths != nil {
		t.probation = *req.ProbationMonths
	}
	if req.BaseSalary != nil {
		b, err := money("baseSalary", *req.BaseSalary)
		if err != nil {
			return before, err
		}
		if b != nil {
			t.base = *b
		}
	}
	if req.Allowances != nil {
		t.allowances = req.Allowances
	}
	if req.WorkWeekDays != nil {
		t.weekDays = *req.WorkWeekDays
	}
	if req.Notes != nil {
		t.notes = nullStr(*req.Notes)
	}
	if err := validateTerms(ctx, tx, property, &t); err != nil {
		return before, err
	}
	raw, _ = json.Marshal(t.allowances)
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET org_unit_id = $2, position_id = $3, grade_id = $4, job_title = $5, contract_type = $6, start_date = $7,
		end_date = $8, probation_months = $9, base_salary = $10::numeric, allowances = $11, work_week_days = $12, notes = $13, updated_by = $14 WHERE id = $1`,
		oid, t.unit, t.pos, t.grade, t.title, t.ctype, t.start, t.end, t.probation, t.base.String(), raw, t.weekDays, t.notes, actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.offer(ctx, tx, oid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.job_offer", EntityID: oid.String(),
		EntityLabel: after.Number + " · " + after.ApplicationNo, PropertyID: &property, Before: offerAudit(before), After: offerAudit(after)})
}

// OfferDecision applies the approval decision (approval engine hook).
func (m *Module) OfferDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number string
	var aid uuid.UUID
	var creator *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, number, application_id, created_by FROM hris.job_offers WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&status, &number, &aid, &creator); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != "submitted" {
		return nil
	}
	next := map[string]string{approval.StatusApproved: "approved", approval.StatusRejected: "rejected", approval.StatusCancelled: "draft"}[d.Status]
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = $2, approved_at = CASE WHEN $2 = 'approved' THEN now() END, decision_note = $3 WHERE id = $1`,
		d.DocumentID, next, nullStr(d.Reason)); err != nil {
		return err
	}
	if next != "approved" {
		// The application goes back to Interview until a new offer is submitted.
		var a JobApplication
		if err := tx.QueryRow(ctx, `SELECT id, property_id, number, stage FROM hris.applications WHERE id = $1 FOR UPDATE`, aid).
			Scan(&a.ID, &a.PropertyID, &a.Number, &a.Stage); err != nil {
			return err
		}
		if a.Stage == hris.StageOffered {
			if err := stageEvent(ctx, tx, a, hris.StageInterview, "Offer "+number+" "+next); err != nil {
				return err
			}
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.job_offer", EntityID: d.DocumentID.String(),
		EntityLabel: number, PropertyID: &d.PropertyID, Reason: d.Reason, Before: map[string]any{"status": status},
		After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if creator == nil || d.Status == approval.StatusCancelled {
		return nil
	}
	var cand, title string
	if err := tx.QueryRow(ctx, `SELECT c.full_name, coalesce(o.job_title, r.title) FROM hris.job_offers o JOIN hris.applications a ON a.id = o.application_id
		JOIN hris.candidates c ON c.id = a.candidate_id JOIN hris.job_requisitions r ON r.id = a.requisition_id WHERE o.id = $1`, d.DocumentID).
		Scan(&cand, &title); err != nil {
		return err
	}
	return m.notifyUsers(ctx, tx, d.PropertyID, []uuid.UUID{*creator}, "hris.offer_decided", "/hris/recruitment/applications/"+aid.String(),
		map[string]any{"number": number, "candidate": cand, "title": title, "decision": next, "reason": d.Reason})
}

func (m *Module) decideOfferHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobOffer, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobOffer, error) {
		oid, err := handle.ID(r)
		if err != nil {
			return JobOffer{}, err
		}
		status, _, _, rid, err := lockOffer(ctx, tx, oid)
		if err != nil {
			return JobOffer{}, err
		}
		if status != "submitted" || rid == nil {
			return JobOffer{}, errs.Conflict("offer_not_submitted", "the offer waits for no approval")
		}
		if err := m.Approvals.Decide(ctx, tx, *rid, approve, req.Reason); err != nil {
			return JobOffer{}, err
		}
		return m.offer(ctx, tx, oid)
	}
}

func (m *Module) sendOfferHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req JobOfferSendRequest) (JobOffer, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return JobOffer{}, err
	}
	status, property, _, _, err := lockOffer(ctx, tx, oid)
	if err != nil {
		return JobOffer{}, err
	}
	if status != "approved" && status != "sent" {
		return JobOffer{}, errs.Conflict("offer_not_approved", "only an approved offer can be sent")
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return JobOffer{}, err
	}
	day := today(ctx, tx, property)
	exp := day.AddDate(0, 0, max(cfg.OfferValidityDays, 1))
	if req.ExpiresOn != "" {
		e, err := mustDate("expiresOn", req.ExpiresOn)
		if err != nil {
			return JobOffer{}, err
		}
		if e.Before(day) {
			return JobOffer{}, handle.Invalid("expiresOn", "invalid", "must be today or later")
		}
		exp = e
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = 'sent', sent_at = now(), expires_on = $2, updated_by = $3 WHERE id = $1`, oid, ymd(exp),
		actor(ctx)); err != nil {
		return JobOffer{}, err
	}
	o, err := m.offer(ctx, tx, oid)
	if err != nil {
		return o, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "send", EntityType: "hris.job_offer", EntityID: oid.String(),
		EntityLabel: o.Number + " · " + o.ApplicationNo, PropertyID: &property, Before: map[string]any{"status": status},
		After: map[string]any{"status": "sent", "expiresOn": ymd(exp), "emailed": req.Email}}); err != nil {
		return o, err
	}
	if !req.Email {
		return o, nil
	}
	var email *string
	if err := tx.QueryRow(ctx, `SELECT c.email FROM hris.applications a JOIN hris.candidates c ON c.id = a.candidate_id WHERE a.id = $1`, o.ApplicationID).
		Scan(&email); err != nil {
		return o, err
	}
	return o, m.notifyCandidate(ctx, tx, property, email, o.CandidateName, "hris.candidate_offer", map[string]any{"candidate": o.CandidateName,
		"title": deref(o.JobTitle), "startDate": ymd(o.StartDate), "expiresOn": ymd(exp)})
}

// answerOfferHTTP records the candidate's answer or cancels the offer.
func (m *Module) answerOfferHTTP(next string) func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobOffer, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobOffer, error) {
		oid, err := handle.ID(r)
		if err != nil {
			return JobOffer{}, err
		}
		status, property, aid, rid, err := lockOffer(ctx, tx, oid)
		if err != nil {
			return JobOffer{}, err
		}
		switch next {
		case "accepted", "declined":
			if status != "sent" && status != "approved" {
				return JobOffer{}, errs.Conflict("offer_not_sent", "only a sent offer can be answered")
			}
		case "cancelled":
			if !oneOf([]string{"draft", "submitted", "approved", "sent", "accepted", "rejected"}, status) {
				return JobOffer{}, errs.Conflict("offer_closed", "the offer is "+status)
			}
			if strings.TrimSpace(req.Reason) == "" {
				return JobOffer{}, handle.Invalid("reason", "required", "a reason is required")
			}
			if status == "submitted" && rid != nil {
				if err := m.Approvals.Cancel(ctx, tx, *rid, "offer cancelled: "+req.Reason); err != nil {
					return JobOffer{}, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = $2, responded_at = CASE WHEN $2 IN ('accepted', 'declined') THEN now() ELSE responded_at END,
			response_note = CASE WHEN $2 IN ('accepted', 'declined') THEN $3 ELSE response_note END,
			decision_note = CASE WHEN $2 = 'cancelled' THEN $3 ELSE decision_note END, updated_by = $4 WHERE id = $1`,
			oid, next, nullStr(req.Reason), actor(ctx)); err != nil {
			return JobOffer{}, err
		}
		a, err := lockApplication(ctx, tx, aid)
		if err != nil {
			return JobOffer{}, err
		}
		switch {
		case next == "declined" && hris.StageRank(a.Stage) >= 0 && a.Stage != hris.StageHired:
			if err := m.CloseApplication(ctx, tx, a, hris.StageWithdrawn, "Offer declined: "+strings.TrimSpace(req.Reason), false); err != nil {
				return JobOffer{}, err
			}
		case next == "cancelled" && a.Stage == hris.StageOffered:
			if err := stageEvent(ctx, tx, a, hris.StageInterview, "Offer cancelled: "+req.Reason); err != nil {
				return JobOffer{}, err
			}
		}
		o, err := m.offer(ctx, tx, oid)
		if err != nil {
			return o, err
		}
		return o, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[string]string{"accepted": "accept", "declined": "decline",
			"cancelled": "cancel"}[next], EntityType: "hris.job_offer", EntityID: oid.String(), EntityLabel: o.Number + " · " + o.ApplicationNo,
			PropertyID: &property, Reason: req.Reason, Before: map[string]any{"status": status}, After: map[string]any{"status": next}})
	}
}

// ── offer letter ─────────────────────────────────────────────────────────

// OfferLetterData are the fields an offer letter template may use (HR
// letter template of type offer_letter, Recruitment Configuration
// offerLetterTemplate).
type OfferLetterData struct {
	Company   struct{ LegalName, Address, City string }
	Property  string
	Today     string
	Candidate struct{ FullName, Address, City string }
	Offer     struct {
		Number, Title, OrgUnit, Position, Grade, ContractType, StartDate, EndDate, ExpiresOn, BaseSalary, AllowanceTotal, MonthlyTotal string
		ProbationMonths, WorkWeekDays                                                                                                  int
		Allowances                                                                                                                     []hris.Allowance
	}
}

const defaultOfferLetter = `Kepada Yth. {{.Candidate.FullName}}
{{if .Candidate.City}}{{.Candidate.City}}{{end}}

Dengan hormat,

Menindaklanjuti proses seleksi, {{.Company.LegalName}} dengan senang hati menawarkan kepada Anda posisi {{.Offer.Title}}{{if .Offer.OrgUnit}} pada {{.Offer.OrgUnit}}{{end}} dengan ketentuan sebagai berikut:

- Nomor penawaran: {{.Offer.Number}}
- Jenis hubungan kerja: {{.Offer.ContractType}}{{if .Offer.EndDate}} sampai {{.Offer.EndDate}}{{end}}{{if .Offer.ProbationMonths}} dengan masa percobaan {{.Offer.ProbationMonths}} bulan{{end}}
- Tanggal mulai bekerja: {{.Offer.StartDate}}
- Hari kerja: {{.Offer.WorkWeekDays}} hari per minggu
- Gaji pokok: {{.Offer.BaseSalary}} per bulan
{{range .Offer.Allowances}}- {{.Name}}: {{.Amount}} per bulan
{{end}}- Total upah tetap: {{.Offer.MonthlyTotal}} per bulan

Upah dipotong PPh 21 dan iuran BPJS sesuai ketentuan yang berlaku. Penawaran ini berlaku sampai {{.Offer.ExpiresOn}}. Mohon konfirmasi penerimaan Anda kepada tim HR.

We are pleased to offer you the position above under these terms. Please confirm your acceptance to our HR team before the date stated.

Hormat kami,
Human Resources — {{.Property}}
{{.Today}}`

// rupiah formats an amount as Rp 1.234.567.
func rupiah(v decimal.Decimal) string {
	s := v.Round(0).StringFixed(0)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-Rp " + b.String()
	}
	return "Rp " + b.String()
}

// OfferLetter renders the offer letter PDF.
func (m *Module) OfferLetter(ctx context.Context, tx pgx.Tx, oid uuid.UUID) ([]byte, string, error) {
	o, err := m.offer(ctx, tx, oid)
	if err != nil {
		return nil, "", err
	}
	var raw []byte
	var base string
	if err := tx.QueryRow(ctx, `SELECT base_salary::text, allowances FROM hris.job_offers WHERE id = $1`, oid).Scan(&base, &raw); err != nil {
		return nil, "", err
	}
	var allowances []hris.Allowance
	_ = json.Unmarshal(raw, &allowances)
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, o.PropertyID, clock.Now())
	if err != nil {
		return nil, "", err
	}
	title, body := "SURAT PENAWARAN KERJA / OFFER LETTER", defaultOfferLetter
	if code := strings.TrimSpace(cfg.OfferLetterTemplate); code != "" {
		if err := tx.QueryRow(ctx, `SELECT title, body FROM hris.letter_templates WHERE code = upper($1) AND letter_type = 'offer_letter' AND status = 'active'
			AND archived_at IS NULL`, code).Scan(&title, &body); err != nil {
			return nil, "", errs.Conflict("offer_letter_template", "the offer letter template "+code+" of the Recruitment Configuration is missing")
		}
	}
	tpl, err := template.New("offer").Option("missingkey=error").Parse(body)
	if err != nil {
		return nil, "", errs.Conflict("invalid_template", "the offer letter template is invalid: "+err.Error())
	}
	var d OfferLetterData
	d.Today = today(ctx, tx, o.PropertyID).Format("2 January 2006")
	var legal, addr, city *string
	_ = tx.QueryRow(ctx, `SELECT legal_name, address, city FROM platform.organization LIMIT 1`).Scan(&legal, &addr, &city)
	d.Company.LegalName, d.Company.Address, d.Company.City = deref(legal), deref(addr), deref(city)
	d.Property, _ = org.PropertyName(ctx, tx, o.PropertyID)
	if d.Company.LegalName == "" {
		d.Company.LegalName = d.Property
	}
	var caddr, ccity *string
	if err := tx.QueryRow(ctx, `SELECT c.address, c.city FROM hris.applications a JOIN hris.candidates c ON c.id = a.candidate_id WHERE a.id = $1`,
		o.ApplicationID).Scan(&caddr, &ccity); err != nil {
		return nil, "", err
	}
	d.Candidate.FullName, d.Candidate.Address, d.Candidate.City = o.CandidateName, deref(caddr), deref(ccity)
	d.Offer.Number, d.Offer.Title, d.Offer.OrgUnit, d.Offer.Position = o.Number, deref(o.JobTitle), deref(o.OrgUnitName), deref(o.PositionName)
	d.Offer.Grade, d.Offer.ContractType, d.Offer.StartDate = deref(o.GradeCode), strings.ToUpper(o.ContractType), o.StartDate.Format("2 January 2006")
	if o.EndDate != nil {
		d.Offer.EndDate = o.EndDate.Format("2 January 2006")
	}
	if o.ExpiresOn != nil {
		d.Offer.ExpiresOn = o.ExpiresOn.Format("2 January 2006")
	} else {
		d.Offer.ExpiresOn = today(ctx, tx, o.PropertyID).AddDate(0, 0, max(cfg.OfferValidityDays, 1)).Format("2 January 2006")
	}
	d.Offer.ProbationMonths, d.Offer.WorkWeekDays = o.ProbationMonths, o.WorkWeekDays
	total := hris.Dec(base)
	allow := decimal.Zero
	for _, a := range allowances {
		v := hris.Dec(a.Amount)
		allow = allow.Add(v)
		d.Offer.Allowances = append(d.Offer.Allowances, hris.Allowance{Code: a.Code, Name: a.Name, Amount: rupiah(v)})
	}
	d.Offer.BaseSalary, d.Offer.AllowanceTotal, d.Offer.MonthlyTotal = rupiah(total), rupiah(allow), rupiah(total.Add(allow))
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, d); err != nil {
		return nil, "", errs.Conflict("invalid_template", "the offer letter template cannot be filled: "+err.Error())
	}
	doc := pdf.New()
	if m.Logo != nil {
		if logo := m.Logo(ctx, tx); len(logo) > 0 {
			_ = doc.Logo(logo, 140, 48)
		}
	}
	doc.Row(9, false, d.Company.LegalName)
	doc.Space(12)
	doc.Row(14, true, title)
	doc.Row(9, false, "No. "+o.Number)
	doc.Rule(doc.Y + 8)
	doc.Space(8)
	for _, line := range wrapText(buf.String(), 95) {
		if doc.Y < 60 {
			doc.AddPage()
		}
		doc.Row(10, false, line)
	}
	return doc.Bytes(), "Offer " + o.Number, nil
}

// wrapText splits text into lines of at most n characters (words kept).
func wrapText(text string, n int) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			if line != "" && len(line)+1+len(w) > n {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += w
		}
		out = append(out, line)
	}
	return out
}

func (m *Module) letterHTTP(w http.ResponseWriter, r *http.Request) {
	oid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var out []byte
	var name string
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		out, name, err = m.OfferLetter(ctx, tx, oid)
		if err != nil {
			return err
		}
		pid := handle.Property(ctx)
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "generate_letter", EntityType: "hris.job_offer", EntityID: oid.String(),
			EntityLabel: name, PropertyID: &pid})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`.pdf"`)
	_, _ = w.Write(out)
}

// ── hire ─────────────────────────────────────────────────────────────────

func (m *Module) hireHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ApplicationHireRequest) (ApplicationHireResult, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return ApplicationHireResult{}, err
	}
	if m.Onboarding == nil {
		return ApplicationHireResult{}, errs.Unavailable("Core HR onboarding is not wired")
	}
	a, err := lockApplication(ctx, tx, aid)
	if err != nil {
		return ApplicationHireResult{}, err
	}
	if a.Stage != hris.StageOffered {
		return ApplicationHireResult{}, errs.Conflict("invalid_stage", "only an application in Offered can be hired (now "+a.Stage+")")
	}
	var oid uuid.UUID
	var t offerTerms
	var raw []byte
	var base string
	if err := tx.QueryRow(ctx, `SELECT id, org_unit_id, position_id, grade_id, job_title, contract_type, start_date, end_date, probation_months,
		base_salary::text, allowances, work_week_days FROM hris.job_offers WHERE application_id = $1 AND status = 'accepted' FOR UPDATE`, aid).
		Scan(&oid, &t.unit, &t.pos, &t.grade, &t.title, &t.ctype, &t.start, &t.end, &t.probation, &base, &raw, &t.weekDays); err != nil {
		return ApplicationHireResult{}, errs.Conflict("offer_not_accepted", "the candidate has not accepted an offer yet")
	}
	_ = json.Unmarshal(raw, &t.allowances)
	var rq struct {
		number, status, category string
		headcount, hired         int
		manager                  *uuid.UUID
	}
	if err := tx.QueryRow(ctx, `SELECT number, status, worker_category, headcount, hired_count, hiring_manager_id FROM hris.job_requisitions WHERE id = $1
		FOR UPDATE`, a.RequisitionID).Scan(&rq.number, &rq.status, &rq.category, &rq.headcount, &rq.hired, &rq.manager); err != nil {
		return ApplicationHireResult{}, err
	}
	if rq.status != "open" && rq.status != "on_hold" {
		return ApplicationHireResult{}, errs.Conflict("requisition_not_open", "the requisition is "+rq.status)
	}
	if rq.hired >= rq.headcount {
		return ApplicationHireResult{}, errs.Conflict("requisition_filled", "every position of the requisition is filled")
	}
	var c struct {
		name                                string
		gender, email, phone, address, city *string
		birth                               *time.Time
	}
	if err := tx.QueryRow(ctx, `SELECT full_name, gender, email, phone, address, city, birth_date FROM hris.candidates WHERE id = $1 FOR UPDATE`, a.CandidateID).
		Scan(&c.name, &c.gender, &c.email, &c.phone, &c.address, &c.city, &c.birth); err != nil {
		return ApplicationHireResult{}, err
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, a.PropertyID, clock.Now())
	if err != nil {
		return ApplicationHireResult{}, err
	}
	login := cfg.CreateLoginOnHire
	if req.CreateLogin != nil {
		login = *req.CreateLogin
	}
	loginEmail := strings.TrimSpace(req.WorkEmail)
	if loginEmail == "" {
		loginEmail = deref(c.email)
	}
	if login && loginEmail == "" {
		if req.CreateLogin != nil {
			return ApplicationHireResult{}, handle.Invalid("workEmail", "required", "an e-mail is required to create the login")
		}
		login = false
	}
	sup := req.SupervisorID
	if sup == nil {
		sup = rq.manager
	}
	hr := hris.HireRequest{PropertyID: a.PropertyID, EmployeeNo: strings.TrimSpace(req.EmployeeNo), FullName: c.name, Gender: deref(c.gender),
		BirthDate: c.birth, Phone: deref(c.phone), PersonalEmail: deref(c.email), WorkEmail: strings.TrimSpace(req.WorkEmail), Address: deref(c.address),
		City: deref(c.city), OrgUnitID: t.unit, PositionID: t.pos, GradeID: t.grade, SupervisorID: sup, JobTitle: deref(t.title), WorkerCategory: rq.category,
		JoinDate: t.start, ContractType: t.ctype, EndDate: t.end, ProbationMonths: t.probation, BaseSalary: base, Allowances: t.allowances,
		WorkWeekDays: t.weekDays, Reference: a.Number, CreateLogin: login, LoginEmail: loginEmail, RoleCodes: req.RoleCodes}
	res, err := m.Onboarding.Hire(ctx, tx, hr)
	if err != nil {
		return ApplicationHireResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET hired_at = now(), employee_id = $2, contract_id = $3, closed_at = now(), closed_stage = 'offered'
		WHERE id = $1`, aid, res.EmployeeID, res.ContractID); err != nil {
		return ApplicationHireResult{}, err
	}
	if err := stageEvent(ctx, tx, a, hris.StageHired, "Hired as "+res.EmployeeNo); err != nil {
		return ApplicationHireResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET status = 'hired', employee_id = $2, retention_until = NULL WHERE id = $1`, a.CandidateID,
		res.EmployeeID); err != nil {
		return ApplicationHireResult{}, err
	}
	filled := rq.hired+1 >= rq.headcount
	if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET hired_count = hired_count + 1,
		status = CASE WHEN $2 THEN 'filled' ELSE status END, filled_at = CASE WHEN $2 THEN now() ELSE filled_at END WHERE id = $1`,
		a.RequisitionID, filled); err != nil {
		return ApplicationHireResult{}, err
	}
	// Other open applications of the candidate end: the candidate is hired.
	rows, err := tx.Query(ctx, `SELECT id, property_id, number, requisition_id, candidate_id, stage, source FROM hris.applications
		WHERE candidate_id = $1 AND id <> $2 AND stage IN ('applied', 'screening', 'interview', 'offered')`, a.CandidateID, aid)
	if err != nil {
		return ApplicationHireResult{}, err
	}
	others, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (JobApplication, error) {
		var x JobApplication
		return x, row.Scan(&x.ID, &x.PropertyID, &x.Number, &x.RequisitionID, &x.CandidateID, &x.Stage, &x.Source)
	})
	if err != nil {
		return ApplicationHireResult{}, err
	}
	for _, x := range others {
		if err := m.CloseApplication(ctx, tx, x, hris.StageWithdrawn, "Hired through "+a.Number, false); err != nil {
			return ApplicationHireResult{}, err
		}
	}
	for i, it := range cfg.OnboardingChecklist {
		due := t.start.AddDate(0, 0, it.DueDays)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.onboarding_items (id, property_id, employee_id, application_id, code, label, sort_order, due_date)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (employee_id, code) DO NOTHING`, id.New(), a.PropertyID, res.EmployeeID, aid, it.Code, it.Label, i,
			due); err != nil {
			return ApplicationHireResult{}, err
		}
	}
	if login && res.UserID != nil {
		if _, err := tx.Exec(ctx, `UPDATE hris.onboarding_items SET status = 'done', done_at = now(), done_by = $2, notes = 'Login created at hire'
			WHERE employee_id = $1 AND code = 'login'`, res.EmployeeID, actor(ctx)); err != nil {
			return ApplicationHireResult{}, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "hire", EntityType: "hris.application", EntityID: aid.String(),
		EntityLabel: a.Number + " → " + res.EmployeeNo, PropertyID: &a.PropertyID, Before: map[string]any{"stage": a.Stage},
		After: map[string]any{"stage": hris.StageHired, "employeeId": res.EmployeeID, "contractId": res.ContractID, "offerId": oid,
			"loginCreated": res.UserID != nil, "requisitionFilled": filled}}); err != nil {
		return ApplicationHireResult{}, err
	}
	out := ApplicationHireResult{Employee: res}
	if out.Application, err = m.Application(ctx, tx, aid); err != nil {
		return out, err
	}
	if out.Onboarding, err = handle.List[OnboardingTask](tx.Query(ctx, onboardingSelect+` WHERE o.employee_id = $1 ORDER BY o.sort_order, o.code`,
		res.EmployeeID)); err != nil {
		return out, err
	}
	if out.Requisition, err = m.Requisition(ctx, tx, a.RequisitionID); err != nil {
		return out, err
	}
	users := holders(ctx, tx, a.PropertyID, "hris.application.hire")
	if u := userOf(ctx, tx, rq.manager); u != nil {
		users = append(users, *u)
	}
	return out, m.notifyUsers(ctx, tx, a.PropertyID, users, "hris.candidate_hired", "/hris/employees/"+res.EmployeeID.String(),
		map[string]any{"employeeName": c.name, "employeeNo": res.EmployeeNo, "title": deref(t.title), "joinDate": ymd(t.start)})
}

func (m *Module) onboardingHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]OnboardingTask, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	return handle.List[OnboardingTask](tx.Query(ctx, onboardingSelect+` WHERE o.employee_id = $1 AND o.property_id = $2 ORDER BY o.sort_order, o.code`,
		eid, handle.Property(ctx)))
}

func (m *Module) onboardingItemHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req OnboardingTaskUpdate) (OnboardingTask, error) {
	oid, err := handle.ID(r)
	if err != nil {
		return OnboardingTask{}, err
	}
	if !oneOf([]string{"pending", "done", "not_applicable"}, req.Status) {
		return OnboardingTask{}, enumErr("status", []string{"pending", "done", "not_applicable"})
	}
	var before string
	var property, eid uuid.UUID
	var label string
	if err := tx.QueryRow(ctx, `SELECT status, property_id, employee_id, label FROM hris.onboarding_items WHERE id = $1 AND property_id = $2 FOR UPDATE`, oid,
		handle.Property(ctx)).Scan(&before, &property, &eid, &label); err != nil {
		return OnboardingTask{}, errs.NotFound("onboarding item")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.onboarding_items SET status = $2, notes = coalesce($3, notes),
		done_at = CASE WHEN $2 = 'pending' THEN NULL ELSE now() END, done_by = CASE WHEN $2 = 'pending' THEN NULL ELSE $4::uuid END WHERE id = $1`,
		oid, req.Status, req.Notes, actor(ctx)); err != nil {
		return OnboardingTask{}, err
	}
	out, err := getOne[OnboardingTask]("onboarding item")(tx.Query(ctx, onboardingSelect+` WHERE o.id = $1`, oid))
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "onboarding_" + req.Status, EntityType: "hris.onboarding_item",
		EntityID: oid.String(), EntityLabel: label, PropertyID: &property, Before: map[string]any{"status": before},
		After: map[string]any{"status": req.Status, "employeeId": eid}})
}
