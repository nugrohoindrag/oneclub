package talent

// Applications (FR-RCT-02): a candidate applies to a requisition and moves
// Applied → Screening → Interview → Offered → Hired, or ends Rejected /
// Withdrawn. Every move is kept in the stage history (funnel, time to
// hire). Closing the last open application of a candidate who was not hired
// starts the retention period (FR-RCT-06, §16 #10): the talent pool consent
// keeps the candidate for the HR Configuration's applicantMonths, otherwise
// for the Recruitment Configuration's noConsentRetentionDays; the daily job
// then erases the personal data. CVs are stored privately and served only
// to users who may see sensitive candidate data.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/storage"
)

// JobApplication is an application in the pipeline.
type JobApplication struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	PropertyID        uuid.UUID  `json:"propertyId" db:"property_id"`
	Number            string     `json:"number" db:"number"`
	RequisitionID     uuid.UUID  `json:"requisitionId" db:"requisition_id"`
	RequisitionNumber string     `json:"requisitionNumber" db:"requisition_number"`
	RequisitionTitle  string     `json:"requisitionTitle" db:"requisition_title"`
	OrgUnitName       string     `json:"orgUnitName" db:"org_unit_name"`
	CandidateID       uuid.UUID  `json:"candidateId" db:"candidate_id"`
	CandidateName     string     `json:"candidateName" db:"candidate_name"`
	CandidateEmail    *string    `json:"candidateEmail" db:"candidate_email"`
	CandidatePhone    *string    `json:"candidatePhone" db:"candidate_phone"`
	Source            string     `json:"source" db:"source"`
	Stage             string     `json:"stage" db:"stage" enum:"applied,screening,interview,offered,hired,rejected,withdrawn"`
	AppliedAt         time.Time  `json:"appliedAt" db:"applied_at"`
	StageChangedAt    time.Time  `json:"stageChangedAt" db:"stage_changed_at"`
	DaysInStage       int        `json:"daysInStage" db:"days_in_stage"`
	CoverLetter       *string    `json:"coverLetter" db:"cover_letter"`
	ScreeningScore    *string    `json:"screeningScore" db:"screening_score"`
	ScreeningNote     *string    `json:"screeningNote" db:"screening_note"`
	Rating            *string    `json:"rating" db:"rating" doc:"Average score of the completed interviews"`
	Interviews        int        `json:"interviews" db:"interviews"`
	OfferID           *uuid.UUID `json:"offerId" db:"offer_id"`
	OfferStatus       *string    `json:"offerStatus" db:"offer_status"`
	ClosedAt          *time.Time `json:"closedAt" db:"closed_at"`
	ClosedStage       *string    `json:"closedStage" db:"closed_stage"`
	CloseReason       *string    `json:"closeReason" db:"close_reason"`
	HiredAt           *time.Time `json:"hiredAt" db:"hired_at"`
	EmployeeID        *uuid.UUID `json:"employeeId" db:"employee_id"`
	ContractID        *uuid.UUID `json:"contractId" db:"contract_id"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const applicationSelect = `SELECT a.id, a.property_id, a.number, a.requisition_id, r.number AS requisition_number, r.title AS requisition_title,
	ou.name AS org_unit_name, a.candidate_id, c.full_name AS candidate_name, c.email AS candidate_email, c.phone AS candidate_phone, a.source, a.stage,
	a.applied_at, a.stage_changed_at, floor(extract(epoch FROM now() - a.stage_changed_at) / 86400)::int AS days_in_stage, a.cover_letter,
	trim_scale(a.screening_score)::text AS screening_score, a.screening_note, trim_scale(a.rating)::text AS rating,
	(SELECT count(*) FROM hris.interviews i WHERE i.application_id = a.id AND i.status <> 'cancelled')::int AS interviews,
	o.id AS offer_id, o.status AS offer_status, a.closed_at, a.closed_stage, a.close_reason, a.hired_at, a.employee_id, a.contract_id, a.created_at
	FROM hris.applications a JOIN hris.job_requisitions r ON r.id = a.requisition_id JOIN hris.org_units ou ON ou.id = r.org_unit_id
	JOIN hris.candidates c ON c.id = a.candidate_id
	LEFT JOIN LATERAL (SELECT id, status FROM hris.job_offers WHERE application_id = a.id ORDER BY created_at DESC LIMIT 1) o ON true`

// ApplicationStageEvent is one move of an application.
type ApplicationStageEvent struct {
	ID        uuid.UUID `json:"id" db:"id"`
	FromStage *string   `json:"fromStage" db:"from_stage"`
	ToStage   string    `json:"toStage" db:"to_stage"`
	Note      *string   `json:"note" db:"note"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	ByName    *string   `json:"byName" db:"by_name"`
}

// CandidateSummary is the candidate on an application.
type CandidateSummary struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	FullName          string     `json:"fullName" db:"full_name"`
	Email             *string    `json:"email" db:"email"`
	Phone             *string    `json:"phone" db:"phone"`
	City              *string    `json:"city" db:"city"`
	Education         *string    `json:"education" db:"education"`
	CurrentEmployer   *string    `json:"currentEmployer" db:"current_employer"`
	CurrentTitle      *string    `json:"currentTitle" db:"current_title"`
	ExperienceYears   *string    `json:"experienceYears" db:"experience_years"`
	Source            string     `json:"source" db:"source"`
	HasCV             bool       `json:"hasCv" db:"has_cv"`
	TalentPoolConsent bool       `json:"talentPoolConsent" db:"talent_pool_consent"`
	ConsentAt         *time.Time `json:"consentAt" db:"consent_at"`
	RetentionUntil    *time.Time `json:"retentionUntil" db:"retention_until"`
	Status            string     `json:"status" db:"status"`
}

// JobApplicationDetail is the application screen.
type JobApplicationDetail struct {
	Application JobApplication          `json:"application"`
	Candidate   CandidateSummary        `json:"candidate"`
	History     []ApplicationStageEvent `json:"history"`
	Interviews  []Interview             `json:"interviews"`
	Offers      []JobOffer              `json:"offers"`
	Other       []JobApplication        `json:"otherApplications" doc:"Other applications of the candidate"`
	Criteria    []hris.ScoreCriterion   `json:"criteria" doc:"InterviewScorecard criteria of the Recruitment Configuration"`
	ScoreScale  int                     `json:"scoreScale"`
}

// JobApplicationRequest adds an application.
type JobApplicationRequest struct {
	RequisitionID uuid.UUID `json:"requisitionId"`
	CandidateID   uuid.UUID `json:"candidateId"`
	Source        string    `json:"source,omitempty" enum:"website,referral,walk_in,job_portal,agency,internal,social_media,other" doc:"Default: the candidate's"`
	CoverLetter   string    `json:"coverLetter,omitempty"`
}

// ApplicationStageMoveRequest moves an application to Screening or Interview.
type ApplicationStageMoveRequest struct {
	Stage          string `json:"stage" enum:"screening,interview"`
	Note           string `json:"note,omitempty"`
	ScreeningScore string `json:"screeningScore,omitempty" doc:"1 … score scale"`
}

// CandidateEraseRequest erases a candidate on request (UU PDP).
type CandidateEraseRequest struct {
	Reason string `json:"reason"`
}

func (m *Module) registerApplications(reg *route.Registry) {
	tag := "HRIS Recruitment"
	base := "/api/v1/hris/applications"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Job applications (pipeline)", Permission: "hris.application.view",
		Response: JobApplication{}, List: true,
		Query: []route.Param{{Name: "requisitionId"}, {Name: "candidateId"}, {Name: "stage", Enum: hris.Stages}, {Name: "open", Type: "boolean",
			Description: "Only applications in Applied … Offered"}, {Name: "q"}},
		Handler: listRead(m.DB, m.listApplicationsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Add an application of a candidate to a requisition",
		Permission: "hris.application.manage", Request: JobApplicationRequest{}, Response: JobApplication{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.createApplicationHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Application with candidate, history, interviews and offers",
		Permission: "hris.application.view", Response: JobApplicationDetail{}, Handler: handle.Read(m.DB, m.applicationDetailHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:move-stage", Summary: "Move an application to Screening or Interview",
		Permission: "hris.application.manage", Request: ApplicationStageMoveRequest{}, Response: JobApplication{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.moveStageHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject an application (candidate informed)",
		Permission: "hris.application.manage", Request: RecruitmentReason{}, Response: JobApplication{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.closeApplicationHTTP(hris.StageRejected))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:withdraw", Summary: "Record that the candidate withdrew",
		Permission: "hris.application.manage", Request: RecruitmentReason{}, Response: JobApplication{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.closeApplicationHTTP(hris.StageWithdrawn))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/recruitment-files",
		Summary:    "Upload a candidate CV (multipart: file; PDF, DOC/DOCX, JPEG or PNG up to 10 MB)",
		Permission: "hris.candidate.create", Response: storage.File{}, Handler: m.uploadCVHTTP})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/candidates/{id}/cv", Summary: "Download the CV of a candidate",
		Permission: "hris.candidate.view_sensitive", RawContent: "application/octet-stream", Handler: m.cvHTTP})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/candidates/{id}:erase",
		Summary: "Erase the personal data of a candidate (data subject request, UU PDP)", Permission: "hris.candidate.erase",
		Request: CandidateEraseRequest{}, Response: CandidateSummary{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.eraseHTTP)})
}

// Application loads an application of the request's property.
func (m *Module) Application(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (JobApplication, error) {
	return getOne[JobApplication]("application")(tx.Query(ctx, applicationSelect+` WHERE a.id = $1 AND a.property_id = $2`, aid, handle.Property(ctx)))
}

func (m *Module) listApplicationsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]JobApplication, error) {
	req, err := uuidParam(r, "requisitionId")
	if err != nil {
		return nil, err
	}
	cand, err := uuidParam(r, "candidateId")
	if err != nil {
		return nil, err
	}
	open := filterParam(r, "open") == "true"
	return handle.List[JobApplication](tx.Query(ctx, applicationSelect+` WHERE a.property_id = $1 AND ($2::uuid IS NULL OR a.requisition_id = $2)
		AND ($3::uuid IS NULL OR a.candidate_id = $3) AND ($4 = '' OR a.stage = $4)
		AND (NOT $5 OR a.stage IN ('applied', 'screening', 'interview', 'offered'))
		AND ($6 = '' OR c.full_name ILIKE $6 OR a.number ILIKE $6 OR r.title ILIKE $6)
		ORDER BY a.stage_changed_at DESC LIMIT 500`, handle.Property(ctx), req, cand, filterParam(r, "stage"), open, likeParam(r)))
}

// lockApplication loads an application for update.
func lockApplication(ctx context.Context, tx pgx.Tx, aid uuid.UUID) (JobApplication, error) {
	var a JobApplication
	err := tx.QueryRow(ctx, `SELECT a.id, a.property_id, a.number, a.requisition_id, a.candidate_id, a.stage, a.source FROM hris.applications a
		WHERE a.id = $1 AND a.property_id = $2 FOR UPDATE`, aid, handle.Property(ctx)).
		Scan(&a.ID, &a.PropertyID, &a.Number, &a.RequisitionID, &a.CandidateID, &a.Stage, &a.Source)
	if err != nil {
		return a, errs.NotFound("application")
	}
	return a, nil
}

// stageEvent records a move and the new stage.
func stageEvent(ctx context.Context, tx pgx.Tx, a JobApplication, to, note string) error {
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET stage = $2, stage_changed_at = now(), updated_by = $3 WHERE id = $1`, a.ID, to, actor(ctx)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO hris.application_stage_events (id, property_id, application_id, from_stage, to_stage, note, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, id.New(), a.PropertyID, a.ID, nullStr(a.Stage), to, nullStr(note), actor(ctx))
	return err
}

// NewApplication inserts an application (stage Applied) and notifies HR and
// the hiring manager. The candidate's retention stops while it is open.
func (m *Module) NewApplication(ctx context.Context, tx pgx.Tx, property, requisition, candidate uuid.UUID, source, cover string) (uuid.UUID, string, error) {
	var status, title string
	var manager *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, title, hiring_manager_id FROM hris.job_requisitions WHERE id = $1 AND property_id = $2`, requisition, property).
		Scan(&status, &title, &manager); err != nil {
		return uuid.Nil, "", handle.Invalid("requisitionId", "not_found", "job requisition not found")
	}
	if status != "open" {
		return uuid.Nil, "", errs.Conflict("requisition_not_open", "the requisition is not open for applications")
	}
	var cname, cstatus, csource string
	if err := tx.QueryRow(ctx, `SELECT full_name, status, source FROM hris.candidates WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, candidate,
		property).Scan(&cname, &cstatus, &csource); err != nil {
		return uuid.Nil, "", handle.Invalid("candidateId", "not_found", "candidate not found")
	}
	if cstatus == "erased" {
		return uuid.Nil, "", errs.Conflict("candidate_erased", "the candidate's personal data was erased")
	}
	if source == "" {
		source = csource
	}
	if !oneOf(Sources, source) {
		return uuid.Nil, "", enumErr("source", Sources)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.applications WHERE requisition_id = $1 AND candidate_id = $2)`, requisition, candidate).
		Scan(&exists); err != nil {
		return uuid.Nil, "", err
	}
	if exists {
		return uuid.Nil, "", errs.Conflict("application_exists", "the candidate already applied to this requisition")
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return uuid.Nil, "", err
	}
	no, err := yearlyNumber(ctx, tx, property, strings.ToUpper(cfg.ApplicationPrefix), today(ctx, tx, property).Year())
	if err != nil {
		return uuid.Nil, "", err
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.applications (id, property_id, number, requisition_id, candidate_id, source, cover_letter, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`, aid, property, no, requisition, candidate, source, nullStr(cover), actor(ctx)); err != nil {
		return uuid.Nil, "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hris.application_stage_events (id, property_id, application_id, to_stage, note, created_by)
		VALUES ($1,$2,$3,'applied',$4,$5)`, id.New(), property, aid, "Applied via "+source, actor(ctx)); err != nil {
		return uuid.Nil, "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET retention_until = NULL WHERE id = $1`, candidate); err != nil {
		return uuid.Nil, "", err
	}
	users := holders(ctx, tx, property, "hris.application.manage")
	if u := userOf(ctx, tx, manager); u != nil {
		users = append(users, *u)
	}
	return aid, no, m.notifyUsers(ctx, tx, property, users, "hris.application_received", "/hris/recruitment/applications/"+aid.String(),
		map[string]any{"candidate": cname, "title": title, "number": no, "source": source})
}

func (m *Module) createApplicationHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req JobApplicationRequest) (JobApplication, error) {
	property := handle.Property(ctx)
	aid, no, err := m.NewApplication(ctx, tx, property, req.RequisitionID, req.CandidateID, req.Source, req.CoverLetter)
	if err != nil {
		return JobApplication{}, err
	}
	out, err := m.Application(ctx, tx, aid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.application", EntityID: aid.String(),
		EntityLabel: no + " · " + out.RequisitionNumber, PropertyID: &property, After: applicationAudit(out)})
}

// applicationAudit is the audit snapshot: ids and stage, no personal data
// (the candidate may be erased later, the audit log stays).
func applicationAudit(a JobApplication) map[string]any {
	return map[string]any{"number": a.Number, "requisitionId": a.RequisitionID, "candidateId": a.CandidateID, "source": a.Source, "stage": a.Stage}
}

func (m *Module) applicationDetailHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (JobApplicationDetail, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return JobApplicationDetail{}, err
	}
	a, err := m.Application(ctx, tx, aid)
	if err != nil {
		return JobApplicationDetail{}, err
	}
	out := JobApplicationDetail{Application: a}
	if out.Candidate, err = candidateSummary(ctx, tx, a.CandidateID); err != nil {
		return out, err
	}
	if out.History, err = handle.List[ApplicationStageEvent](tx.Query(ctx, `SELECT e.id, e.from_stage, e.to_stage, e.note, e.created_at, u.full_name AS by_name
		FROM hris.application_stage_events e LEFT JOIN platform.users u ON u.id = e.created_by WHERE e.application_id = $1 ORDER BY e.created_at, e.id`, aid)); err != nil {
		return out, err
	}
	if out.Interviews, err = m.interviews(ctx, tx, `i.application_id = $2`, aid); err != nil {
		return out, err
	}
	if out.Offers, err = m.offers(ctx, tx, `o.application_id = $2`, aid); err != nil {
		return out, err
	}
	if out.Other, err = handle.List[JobApplication](tx.Query(ctx, applicationSelect+` WHERE a.candidate_id = $1 AND a.id <> $2 ORDER BY a.applied_at DESC`,
		a.CandidateID, aid)); err != nil {
		return out, err
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, a.PropertyID, clock.Now())
	if err != nil {
		return out, err
	}
	out.Criteria, out.ScoreScale = cfg.InterviewCriteria, cfg.ScoreScale
	return out, nil
}

func candidateSummary(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (CandidateSummary, error) {
	return getOne[CandidateSummary]("candidate")(tx.Query(ctx, `SELECT id, full_name, email, phone, city, education, current_employer, current_title,
		trim_scale(experience_years)::text AS experience_years, source, cv_file_id IS NOT NULL AS has_cv, talent_pool_consent, consent_at, retention_until,
		status FROM hris.candidates WHERE id = $1`, cid))
}

func (m *Module) moveStageHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ApplicationStageMoveRequest) (JobApplication, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return JobApplication{}, err
	}
	a, err := lockApplication(ctx, tx, aid)
	if err != nil {
		return a, err
	}
	if req.Stage != hris.StageScreening && req.Stage != hris.StageInterview {
		return a, enumErr("stage", []string{hris.StageScreening, hris.StageInterview})
	}
	if hris.StageRank(a.Stage) < 0 || hris.StageRank(a.Stage) >= hris.StageRank(req.Stage) {
		return a, errs.Conflict("invalid_stage", "an application in "+a.Stage+" cannot move to "+req.Stage)
	}
	if err := requisitionLive(ctx, tx, a.RequisitionID); err != nil {
		return a, err
	}
	if s := strings.TrimSpace(req.ScreeningScore); s != "" {
		cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, a.PropertyID, clock.Now())
		if err != nil {
			return a, err
		}
		v, err := decimal.NewFromString(s)
		if err != nil || !hris.ValidScore(v, cfg.ScoreScale) {
			return a, handle.Invalid("screeningScore", "invalid", "a score from 1 to "+itoa(cfg.ScoreScale))
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.applications SET screening_score = $2::numeric WHERE id = $1`, aid, v.String()); err != nil {
			return a, err
		}
	}
	if n := strings.TrimSpace(req.Note); n != "" && req.Stage == hris.StageScreening {
		if _, err := tx.Exec(ctx, `UPDATE hris.applications SET screening_note = $2 WHERE id = $1`, aid, n); err != nil {
			return a, err
		}
	}
	from := a.Stage
	if err := stageEvent(ctx, tx, a, req.Stage, req.Note); err != nil {
		return a, err
	}
	out, err := m.Application(ctx, tx, aid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "move_stage", EntityType: "hris.application", EntityID: aid.String(),
		EntityLabel: a.Number, PropertyID: &a.PropertyID, Reason: req.Note, Before: map[string]any{"stage": from}, After: map[string]any{"stage": req.Stage}})
}

// requisitionLive refuses moves on a requisition that is no longer hiring.
func requisitionLive(ctx context.Context, tx pgx.Tx, rid uuid.UUID) error {
	var st string
	if err := tx.QueryRow(ctx, `SELECT status FROM hris.job_requisitions WHERE id = $1`, rid).Scan(&st); err != nil {
		return err
	}
	if st != "open" && st != "on_hold" {
		return errs.Conflict("requisition_not_open", "the requisition is "+st)
	}
	return nil
}

func (m *Module) closeApplicationHTTP(stage string) func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobApplication, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobApplication, error) {
		aid, err := handle.ID(r)
		if err != nil {
			return JobApplication{}, err
		}
		a, err := lockApplication(ctx, tx, aid)
		if err != nil {
			return a, err
		}
		if strings.TrimSpace(req.Reason) == "" {
			return a, handle.Invalid("reason", "required", "a reason is required")
		}
		if err := m.CloseApplication(ctx, tx, a, stage, req.Reason, true); err != nil {
			return a, err
		}
		return m.Application(ctx, tx, aid)
	}
}

// CloseApplication rejects or withdraws an open application: open offers
// and interviews are cancelled, the candidate is informed (rejection) and
// the retention period starts when nothing else is open.
func (m *Module) CloseApplication(ctx context.Context, tx pgx.Tx, a JobApplication, stage, reason string, inform bool) error {
	if hris.StageRank(a.Stage) < 0 || a.Stage == hris.StageHired {
		return errs.Conflict("application_closed", "the application is already "+a.Stage)
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET status = 'cancelled', decision_note = coalesce(decision_note, $2), updated_by = $3
		WHERE application_id = $1 AND status IN ('draft', 'submitted', 'approved', 'sent')`, a.ID, "Application "+stage, actor(ctx)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interviews SET status = 'cancelled', cancel_reason = $2, updated_by = $3
		WHERE application_id = $1 AND status = 'scheduled'`, a.ID, "Application "+stage, actor(ctx)); err != nil {
		return err
	}
	from := a.Stage
	if err := stageEvent(ctx, tx, a, stage, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET closed_at = now(), closed_stage = $2, close_reason = $3 WHERE id = $1`, a.ID, from, reason); err != nil {
		return err
	}
	until, err := m.startRetention(ctx, tx, a.PropertyID, a.CandidateID)
	if err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: map[string]string{hris.StageRejected: "reject", hris.StageWithdrawn: "withdraw"}[stage],
		EntityType: "hris.application", EntityID: a.ID.String(), EntityLabel: a.Number, PropertyID: &a.PropertyID, Reason: reason,
		Before: map[string]any{"stage": from}, After: map[string]any{"stage": stage, "retentionUntil": until}}); err != nil {
		return err
	}
	if !inform || stage != hris.StageRejected {
		return nil
	}
	var name, title string
	var email *string
	var talent bool
	if err := tx.QueryRow(ctx, `SELECT c.full_name, c.email, c.talent_pool_consent, r.title FROM hris.applications a JOIN hris.candidates c ON c.id = a.candidate_id
		JOIN hris.job_requisitions r ON r.id = a.requisition_id WHERE a.id = $1`, a.ID).Scan(&name, &email, &talent, &title); err != nil {
		return err
	}
	retention := ""
	if until != nil && talent {
		retention = "We keep your profile in our talent pool until " + *until + " and may contact you about other positions."
	}
	return m.notifyCandidate(ctx, tx, a.PropertyID, email, name, "hris.candidate_rejected", map[string]any{"candidate": name, "title": title,
		"retention": retention})
}

// startRetention sets the erasure date of a candidate without open or
// hired applications (FR-RCT-06); returns the date (nil = none).
func (m *Module) startRetention(ctx context.Context, tx pgx.Tx, property, candidate uuid.UUID) (*string, error) {
	var open bool
	var talent bool
	var status string
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.applications WHERE candidate_id = $1 AND stage IN ('applied', 'screening', 'interview',
		'offered', 'hired')), talent_pool_consent, status FROM hris.candidates WHERE id = $1`, candidate).Scan(&open, &talent, &status); err != nil {
		return nil, err
	}
	if open || status != "active" {
		if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET retention_until = NULL WHERE id = $1`, candidate); err != nil {
			return nil, err
		}
		return nil, nil
	}
	day := today(ctx, tx, property)
	var until time.Time
	if talent {
		hr, _, err := hris.LoadHRConfiguration(ctx, tx, property, clock.Now())
		if err != nil {
			return nil, err
		}
		until = day.AddDate(0, max(hr.Retention.ApplicantMonths, 0), 0)
	} else {
		cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
		if err != nil {
			return nil, err
		}
		until = day.AddDate(0, 0, max(cfg.NoConsentRetentionDays, 0))
	}
	s := ymd(until)
	_, err := tx.Exec(ctx, `UPDATE hris.candidates SET retention_until = $2::date WHERE id = $1`, candidate, s)
	return &s, err
}

// ── CV files ─────────────────────────────────────────────────────────────

const cvMaxBytes = 10 << 20

var cvTypes = map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true, "application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true}

// cvType detects the content type of a CV (DOCX is a zip archive).
func cvType(name string, data []byte) string {
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	lower := strings.ToLower(name)
	switch {
	case ct == "application/zip" && strings.HasSuffix(lower, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case (ct == "application/octet-stream" || ct == "application/x-ole-storage") && strings.HasSuffix(lower, ".doc"):
		return "application/msword"
	}
	return ct
}

// saveCV stores a CV privately; returns the file id.
func (m *Module) saveCV(ctx context.Context, tx pgx.Tx, name string, data []byte, limit int) (uuid.UUID, error) {
	if len(data) == 0 || len(data) > limit {
		return uuid.Nil, handle.Invalid("cv", "size", "the CV must be between 1 byte and "+itoa(limit>>20)+" MB")
	}
	ct := cvType(name, data)
	if !cvTypes[ct] {
		return uuid.Nil, handle.Invalid("cv", "type", "PDF, DOC/DOCX, JPEG or PNG")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "cv"
	}
	f, err := m.Files.Save(ctx, tx, name, ct, "attachment", false, bytes.NewReader(data), int64(len(data)))
	return f.ID, err
}

func (m *Module) uploadCVHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, cvMaxBytes+256<<10)
	if err := r.ParseMultipartForm(cvMaxBytes); err != nil { //nolint:gosec // G120: body capped by MaxBytesReader above
		httpx.WriteError(w, r, errs.BadRequest("invalid_upload", "upload must be multipart/form-data up to 10 MB"))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, errs.Validation("file_required", "file is required", errs.Field("file", "required", "choose the CV")))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, cvMaxBytes+1))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	pid := handle.Property(ctx)
	var out storage.File
	err = m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		fid, err := m.saveCV(ctx, tx, hdr.Filename, data, cvMaxBytes)
		if err != nil {
			return err
		}
		out = storage.File{ID: fid, Filename: hdr.Filename, ContentType: cvType(hdr.Filename, data), SizeBytes: int64(len(data)), Purpose: "attachment",
			URL: storage.URLFor(fid), CreatedAt: clock.Now()}
		return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "platform.file", EntityID: fid.String(),
			EntityLabel: "Candidate CV", PropertyID: &pid, After: map[string]any{"contentType": out.ContentType, "sizeBytes": out.SizeBytes}})
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (m *Module) cvHTTP(w http.ResponseWriter, r *http.Request) {
	cid, err := handle.ID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := r.Context()
	var fid *uuid.UUID
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT cv_file_id FROM hris.candidates WHERE id = $1 AND property_id = $2 AND erased_at IS NULL`, cid, handle.Property(ctx)).
			Scan(&fid)
	}); err != nil || fid == nil {
		httpx.WriteError(w, r, errs.NotFound("CV"))
		return
	}
	var ctype string
	if err := m.DB.Primary.QueryRow(ctx, `SELECT content_type FROM platform.files WHERE id = $1`, *fid).Scan(&ctype); err != nil {
		httpx.WriteError(w, r, errs.NotFound("CV"))
		return
	}
	rc, name, err := m.Files.Open(ctx, *fid)
	if err != nil {
		httpx.WriteError(w, r, errs.NotFound("CV"))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	_, _ = io.Copy(w, rc)
}

// ── erasure (FR-RCT-06) ──────────────────────────────────────────────────

func (m *Module) eraseHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req CandidateEraseRequest) (CandidateSummary, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return CandidateSummary{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return CandidateSummary{}, handle.Invalid("reason", "required", "a reason is required")
	}
	var status string
	var open bool
	if err := tx.QueryRow(ctx, `SELECT status, EXISTS (SELECT 1 FROM hris.applications WHERE candidate_id = c.id AND stage IN ('applied', 'screening',
		'interview', 'offered')) FROM hris.candidates c WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid, handle.Property(ctx)).Scan(&status, &open); err != nil {
		return CandidateSummary{}, errs.NotFound("candidate")
	}
	switch {
	case status == "erased":
		return CandidateSummary{}, errs.Conflict("candidate_erased", "the candidate is already erased")
	case status == "hired":
		return CandidateSummary{}, errs.Conflict("candidate_hired", "a hired candidate is an employee: personal data is kept in HRIS")
	case open:
		return CandidateSummary{}, errs.Conflict("application_open", "reject or withdraw the open applications first")
	}
	if err := m.erase(ctx, tx, cid, req.Reason); err != nil {
		return CandidateSummary{}, err
	}
	return candidateSummary(ctx, tx, cid)
}

// erase removes the personal data of a candidate who was not hired: the
// person, the CV (content overwritten, file record removed), cover letters,
// screening and interview notes and scorecard comments. Ids, stages, dates
// and scores stay for the funnel and time-to-hire reports.
func (m *Module) erase(ctx context.Context, tx pgx.Tx, cid uuid.UUID, reason string) error {
	var property uuid.UUID
	var fid *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, cv_file_id FROM hris.candidates WHERE id = $1`, cid).Scan(&property, &fid); err != nil {
		return err
	}
	if fid != nil {
		var key string
		if err := tx.QueryRow(ctx, `SELECT storage_key FROM platform.files WHERE id = $1`, *fid).Scan(&key); err == nil && m.Files != nil {
			if err := m.Files.Blob.Put(ctx, key, "application/octet-stream", bytes.NewReader(nil), 0); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET full_name = 'Erased applicant', email = NULL, phone = NULL, gender = NULL, birth_date = NULL,
		city = NULL, address = NULL, education = NULL, current_employer = NULL, current_title = NULL, experience_years = NULL, expected_salary = NULL,
		referred_by_id = NULL, cv_file_id = NULL, profile_url = NULL, notes = NULL, status = 'erased', erased_at = now(), retention_until = NULL,
		updated_by = $2 WHERE id = $1`, cid, actor(ctx)); err != nil {
		return err
	}
	if fid != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.files WHERE id = $1`, *fid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET cover_letter = NULL, screening_note = NULL, close_reason = NULL WHERE candidate_id = $1`, cid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.application_stage_events SET note = NULL WHERE application_id IN (SELECT id FROM hris.applications
		WHERE candidate_id = $1)`, cid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interviews SET notes = NULL, location = NULL WHERE application_id IN (SELECT id FROM hris.applications
		WHERE candidate_id = $1)`, cid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interview_scorecards SET comments = NULL, scores = (SELECT coalesce(jsonb_agg(s - 'comment'), '[]'::jsonb)
		FROM jsonb_array_elements(scores) s) WHERE interview_id IN (SELECT i.id FROM hris.interviews i JOIN hris.applications a ON a.id = i.application_id
		WHERE a.candidate_id = $1)`, cid); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_offers SET notes = NULL, response_note = NULL WHERE application_id IN (SELECT id FROM hris.applications
		WHERE candidate_id = $1)`, cid); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "erase", EntityType: KeyCandidate, EntityID: cid.String(),
		EntityLabel: "Candidate " + cid.String()[:8], PropertyID: &property, Reason: reason, After: map[string]any{"status": "erased"}})
}
