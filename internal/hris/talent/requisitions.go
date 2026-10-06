package talent

// Job requisitions (FR-RCT-01): a department head requests headcount for a
// position; the request goes through the approval engine (document type
// hris.job_requisition; without a workflow it is approved at once) and is
// Open for hiring once approved. Hires count against the headcount; the
// requisition is Filled when every position is hired. Public requisitions
// show on the website careers page (FR-RCT-05).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Requisition statuses.
var RequisitionStatuses = []string{"draft", "submitted", "open", "on_hold", "filled", "closed", "rejected", "cancelled"}

// JobRequisition is a job requisition with its pipeline counts.
type JobRequisition struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	PropertyID        uuid.UUID  `json:"propertyId" db:"property_id"`
	Number            string     `json:"number" db:"number"`
	Title             string     `json:"title" db:"title"`
	OrgUnitID         uuid.UUID  `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName       string     `json:"orgUnitName" db:"org_unit_name"`
	PositionID        *uuid.UUID `json:"positionId" db:"position_id"`
	PositionName      *string    `json:"positionName" db:"position_name"`
	GradeID           *uuid.UUID `json:"gradeId" db:"grade_id"`
	GradeCode         *string    `json:"gradeCode" db:"grade_code"`
	HiringManagerID   *uuid.UUID `json:"hiringManagerId" db:"hiring_manager_id"`
	HiringManagerName *string    `json:"hiringManagerName" db:"hiring_manager_name"`
	Headcount         int        `json:"headcount" db:"headcount"`
	HiredCount        int        `json:"hiredCount" db:"hired_count"`
	Reason            string     `json:"reason" db:"reason" enum:"new_position,replacement,seasonal,other"`
	ReplacementForID  *uuid.UUID `json:"replacementForId" db:"replacement_for_id"`
	ContractType      string     `json:"contractType" db:"contract_type" enum:"pkwt,pkwtt"`
	WorkerCategory    string     `json:"workerCategory" db:"worker_category" enum:"regular,daily,intern"`
	SalaryMin         *string    `json:"salaryMin" db:"salary_min" doc:"Budget; masked without hris.job_requisition.view_budget"`
	SalaryMax         *string    `json:"salaryMax" db:"salary_max"`
	TargetStartDate   *time.Time `json:"targetStartDate" db:"target_start_date"`
	Location          *string    `json:"location" db:"location"`
	Description       *string    `json:"description" db:"description"`
	Requirements      *string    `json:"requirements" db:"requirements"`
	IsPublic          bool       `json:"isPublic" db:"is_public"`
	PublishUntil      *time.Time `json:"publishUntil" db:"publish_until"`
	Status            string     `json:"status" db:"status" enum:"draft,submitted,open,on_hold,filled,closed,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	SubmittedAt       *time.Time `json:"submittedAt" db:"submitted_at"`
	ApprovedAt        *time.Time `json:"approvedAt" db:"approved_at"`
	FilledAt          *time.Time `json:"filledAt" db:"filled_at"`
	ClosedAt          *time.Time `json:"closedAt" db:"closed_at"`
	CloseReason       *string    `json:"closeReason" db:"close_reason"`
	DecisionNote      *string    `json:"decisionNote" db:"decision_note"`
	RequestedByName   *string    `json:"requestedByName" db:"requested_by_name"`
	Applications      int        `json:"applications" db:"applications"`
	InPipeline        int        `json:"inPipeline" db:"in_pipeline" doc:"Applications not yet hired, rejected or withdrawn"`
	DaysOpen          *int       `json:"daysOpen" db:"days_open"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
	UpdatedAt         time.Time  `json:"updatedAt" db:"updated_at"`
}

const requisitionSelect = `SELECT r.id, r.property_id, r.number, r.title, r.org_unit_id, ou.name AS org_unit_name, r.position_id, p.name AS position_name,
	r.grade_id, g.code AS grade_code, r.hiring_manager_id, hm.full_name AS hiring_manager_name, r.headcount, r.hired_count, r.reason, r.replacement_for_id,
	r.contract_type, r.worker_category, trim_scale(r.salary_min)::text AS salary_min, trim_scale(r.salary_max)::text AS salary_max, r.target_start_date,
	r.location, r.description, r.requirements, r.is_public, r.publish_until, r.status, r.approval_request_id, r.submitted_at, r.approved_at, r.filled_at,
	r.closed_at, r.close_reason, r.decision_note, u.full_name AS requested_by_name,
	(SELECT count(*) FROM hris.applications a WHERE a.requisition_id = r.id)::int AS applications,
	(SELECT count(*) FROM hris.applications a WHERE a.requisition_id = r.id AND a.stage IN ('applied', 'screening', 'interview', 'offered'))::int AS in_pipeline,
	CASE WHEN r.approved_at IS NOT NULL THEN floor(extract(epoch FROM coalesce(r.filled_at, r.closed_at, now()) - r.approved_at) / 86400)::int END AS days_open,
	r.created_at, r.updated_at
	FROM hris.job_requisitions r JOIN hris.org_units ou ON ou.id = r.org_unit_id LEFT JOIN hris.positions p ON p.id = r.position_id
	LEFT JOIN hris.grades g ON g.id = r.grade_id LEFT JOIN hris.employees hm ON hm.id = r.hiring_manager_id
	LEFT JOIN platform.users u ON u.id = r.requested_by`

// RequisitionRequest creates or edits a requisition.
type RequisitionRequest struct {
	Title            string     `json:"title"`
	OrgUnitID        *uuid.UUID `json:"orgUnitId,omitempty" doc:"Default: the org unit of the position"`
	PositionID       *uuid.UUID `json:"positionId,omitempty"`
	GradeID          *uuid.UUID `json:"gradeId,omitempty" doc:"Default: the grade of the position"`
	HiringManagerID  *uuid.UUID `json:"hiringManagerId,omitempty" doc:"Default: my employee profile"`
	Headcount        int        `json:"headcount"`
	Reason           string     `json:"reason,omitempty" enum:"new_position,replacement,seasonal,other"`
	ReplacementForID *uuid.UUID `json:"replacementForId,omitempty"`
	ContractType     string     `json:"contractType,omitempty" enum:"pkwt,pkwtt"`
	WorkerCategory   string     `json:"workerCategory,omitempty" enum:"regular,daily,intern"`
	SalaryMin        string     `json:"salaryMin,omitempty"`
	SalaryMax        string     `json:"salaryMax,omitempty"`
	TargetStartDate  string     `json:"targetStartDate,omitempty"`
	Location         string     `json:"location,omitempty"`
	Description      string     `json:"description,omitempty"`
	Requirements     string     `json:"requirements,omitempty"`
	IsPublic         bool       `json:"isPublic,omitempty" doc:"Show on the website careers page once open"`
	PublishUntil     string     `json:"publishUntil,omitempty"`
}

// RequisitionUpdate edits a requisition: everything while draft or
// rejected; the publication, texts and hiring manager once open.
type RequisitionUpdate struct {
	Title           *string    `json:"title,omitempty"`
	PositionID      *uuid.UUID `json:"positionId,omitempty"`
	GradeID         *uuid.UUID `json:"gradeId,omitempty"`
	HiringManagerID *uuid.UUID `json:"hiringManagerId,omitempty"`
	Headcount       *int       `json:"headcount,omitempty"`
	Reason          *string    `json:"reason,omitempty" enum:"new_position,replacement,seasonal,other"`
	ContractType    *string    `json:"contractType,omitempty" enum:"pkwt,pkwtt"`
	WorkerCategory  *string    `json:"workerCategory,omitempty" enum:"regular,daily,intern"`
	SalaryMin       *string    `json:"salaryMin,omitempty"`
	SalaryMax       *string    `json:"salaryMax,omitempty"`
	TargetStartDate *string    `json:"targetStartDate,omitempty"`
	Location        *string    `json:"location,omitempty"`
	Description     *string    `json:"description,omitempty"`
	Requirements    *string    `json:"requirements,omitempty"`
	IsPublic        *bool      `json:"isPublic,omitempty"`
	PublishUntil    *string    `json:"publishUntil,omitempty"`
}

// RecruitmentReason carries a reason / note.
type RecruitmentReason struct {
	Reason string `json:"reason,omitempty"`
}

func (m *Module) registerRequisitions(reg *route.Registry) {
	tag := "HRIS Recruitment"
	base := "/api/v1/hris/job-requisitions"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Job requisitions", Permission: "hris.job_requisition.view",
		Response: JobRequisition{}, List: true, Query: []route.Param{{Name: "status", Enum: RequisitionStatuses}, {Name: "orgUnitId"}, {Name: "q"}},
		Handler: listRead(m.DB, m.listRequisitionsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Request staff (draft job requisition)", Permission: "hris.job_requisition.create",
		Request: RequisitionRequest{}, Response: JobRequisition{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createRequisitionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Job requisition", Permission: "hris.job_requisition.view",
		Response: JobRequisition{}, Handler: handle.Read(m.DB, m.getRequisitionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a job requisition", Permission: "hris.job_requisition.update",
		Request: RequisitionUpdate{}, Response: JobRequisition{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateRequisitionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:submit", Summary: "Submit a job requisition for approval",
		Permission: "hris.job_requisition.submit", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.submitRequisitionHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:approve", Summary: "Approve the job requisition (current approval step)",
		Permission: "hris.job_requisition.approve", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideRequisitionHTTP(true))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reject", Summary: "Reject the job requisition (current approval step)",
		Permission: "hris.job_requisition.approve", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.decideRequisitionHTTP(false))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:hold", Summary: "Put an open requisition on hold (hidden from the careers page)",
		Permission: "hris.job_requisition.close", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.requisitionStatusHTTP("hold"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reopen", Summary: "Reopen a requisition on hold or closed with positions left",
		Permission: "hris.job_requisition.close", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.requisitionStatusHTTP("reopen"))})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:close", Summary: "Close (or cancel a draft) job requisition",
		Permission: "hris.job_requisition.close", Request: RecruitmentReason{}, Response: JobRequisition{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.requisitionStatusHTTP("close"))})
}

func maskRequisition(ctx context.Context, r *JobRequisition) {
	if can(ctx, "hris.job_requisition.view_budget", r.PropertyID) {
		return
	}
	red := mask.Redacted
	if r.SalaryMin != nil {
		r.SalaryMin = &red
	}
	if r.SalaryMax != nil {
		r.SalaryMax = &red
	}
}

// Requisition loads a requisition (property of the request, masked).
func (m *Module) Requisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (JobRequisition, error) {
	r, err := getOne[JobRequisition]("job requisition")(tx.Query(ctx, requisitionSelect+` WHERE r.id = $1 AND r.property_id = $2`, rid, handle.Property(ctx)))
	if err != nil {
		return r, err
	}
	maskRequisition(ctx, &r)
	return r, nil
}

func (m *Module) listRequisitionsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]JobRequisition, error) {
	unit, err := uuidParam(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	list, err := handle.List[JobRequisition](tx.Query(ctx, requisitionSelect+` WHERE r.property_id = $1 AND ($2 = '' OR r.status = $2)
		AND ($3::uuid IS NULL OR r.org_unit_id = $3) AND ($4 = '' OR r.title ILIKE $4 OR r.number ILIKE $4)
		ORDER BY CASE r.status WHEN 'submitted' THEN 0 WHEN 'open' THEN 1 WHEN 'draft' THEN 2 WHEN 'on_hold' THEN 3 ELSE 4 END, r.created_at DESC LIMIT 500`,
		handle.Property(ctx), filterParam(r, "status"), unit, likeParam(r)))
	for i := range list {
		maskRequisition(ctx, &list[i])
	}
	return list, err
}

func (m *Module) getRequisitionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (JobRequisition, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return JobRequisition{}, err
	}
	return m.Requisition(ctx, tx, rid)
}

// money parses an optional amount ≥ 0.
func money(field, v string) (*decimal.Decimal, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	d, err := decimal.NewFromString(v)
	if err != nil || d.IsNegative() || d.Exponent() < -2 {
		return nil, handle.Invalid(field, "invalid", "an amount of 0 or more (2 decimals)")
	}
	return &d, nil
}

func decStr(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

type reqDraft struct {
	title                            string
	unit                             *uuid.UUID
	pos, grade, manager, replacement *uuid.UUID
	headcount                        int
	reason, ctype, category          string
	salMin, salMax                   *decimal.Decimal
	start, publishUntil              *time.Time
	location, desc, reqs             *string
	public                           bool
}

// validateRequisition checks a requisition and fills its defaults.
func (m *Module) validateRequisition(ctx context.Context, tx pgx.Tx, property uuid.UUID, d *reqDraft) error {
	d.title = strings.TrimSpace(d.title)
	if d.title == "" {
		if d.pos == nil {
			return handle.Invalid("title", "required", "a title or a position is required")
		}
	}
	if d.headcount < 1 || d.headcount > 500 {
		return handle.Invalid("headcount", "invalid", "between 1 and 500")
	}
	if d.reason == "" {
		d.reason = "new_position"
	}
	if !oneOf([]string{"new_position", "replacement", "seasonal", "other"}, d.reason) {
		return enumErr("reason", []string{"new_position", "replacement", "seasonal", "other"})
	}
	if d.ctype == "" {
		d.ctype = "pkwt"
	}
	if !oneOf([]string{"pkwt", "pkwtt"}, d.ctype) {
		return enumErr("contractType", []string{"pkwt", "pkwtt"})
	}
	if d.category == "" {
		d.category = "regular"
	}
	if !oneOf([]string{"regular", "daily", "intern"}, d.category) {
		return enumErr("workerCategory", []string{"regular", "daily", "intern"})
	}
	if d.pos != nil {
		var unit uuid.UUID
		var grade *uuid.UUID
		var name string
		if err := tx.QueryRow(ctx, `SELECT org_unit_id, grade_id, name FROM hris.positions WHERE id = $1 AND property_id = $2 AND archived_at IS NULL
			AND status = 'active'`, *d.pos, property).Scan(&unit, &grade, &name); err != nil {
			return handle.Invalid("positionId", "not_found", "position not found")
		}
		if d.unit != nil && *d.unit != unit {
			return handle.Invalid("positionId", "invalid", "the position belongs to another org unit")
		}
		d.unit = &unit
		if d.grade == nil {
			d.grade = grade
		}
		if d.title == "" {
			d.title = name
		}
	}
	if d.unit == nil {
		return handle.Invalid("orgUnitId", "required", "an org unit or a position is required")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.org_units WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`, *d.unit, property).
		Scan(&ok); err != nil || !ok {
		return handle.Invalid("orgUnitId", "not_found", "org unit not found")
	}
	if d.grade != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.grades WHERE id = $1 AND archived_at IS NULL)`, *d.grade).Scan(&ok); err != nil || !ok {
			return handle.Invalid("gradeId", "not_found", "grade not found")
		}
	}
	for field, e := range map[string]*uuid.UUID{"hiringManagerId": d.manager, "replacementForId": d.replacement} {
		if e == nil {
			continue
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`, *e, property).
			Scan(&ok); err != nil || !ok {
			return handle.Invalid(field, "not_found", "employee not found")
		}
	}
	if d.reason == "replacement" && d.replacement == nil {
		return handle.Invalid("replacementForId", "required", "name the employee who is replaced")
	}
	if d.salMin != nil && d.salMax != nil && d.salMax.LessThan(*d.salMin) {
		return handle.Invalid("salaryMax", "invalid", "must be at least the minimum")
	}
	return nil
}

func (m *Module) createRequisitionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RequisitionRequest) (JobRequisition, error) {
	property := handle.Property(ctx)
	d := reqDraft{title: req.Title, unit: req.OrgUnitID, pos: req.PositionID, grade: req.GradeID, manager: req.HiringManagerID,
		replacement: req.ReplacementForID, headcount: req.Headcount, reason: req.Reason, ctype: req.ContractType, category: req.WorkerCategory,
		location: nullStr(req.Location), desc: nullStr(req.Description), reqs: nullStr(req.Requirements), public: req.IsPublic}
	var err error
	if d.salMin, err = money("salaryMin", req.SalaryMin); err != nil {
		return JobRequisition{}, err
	}
	if d.salMax, err = money("salaryMax", req.SalaryMax); err != nil {
		return JobRequisition{}, err
	}
	if d.start, err = parseDate("targetStartDate", req.TargetStartDate); err != nil {
		return JobRequisition{}, err
	}
	if d.publishUntil, err = parseDate("publishUntil", req.PublishUntil); err != nil {
		return JobRequisition{}, err
	}
	if d.manager == nil {
		if e, err := hris.EmployeeByUser(ctx, tx, handle.UserID(ctx)); err == nil && e != nil && e.PropertyID == property {
			d.manager = &e.ID
		}
	}
	if err := m.validateRequisition(ctx, tx, property, &d); err != nil {
		return JobRequisition{}, err
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return JobRequisition{}, err
	}
	no, err := yearlyNumber(ctx, tx, property, strings.ToUpper(cfg.RequisitionPrefix), today(ctx, tx, property).Year())
	if err != nil {
		return JobRequisition{}, err
	}
	rid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.job_requisitions (id, property_id, number, title, org_unit_id, position_id, grade_id, hiring_manager_id,
		headcount, reason, replacement_for_id, contract_type, worker_category, salary_min, salary_max, target_start_date, location, description,
		requirements, is_public, publish_until, requested_by, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::numeric,$15::numeric,$16,$17,$18,$19,$20,$21,$22,$22,$22)`,
		rid, property, no, d.title, d.unit, d.pos, d.grade, d.manager, d.headcount, d.reason, d.replacement, d.ctype, d.category, decStr(d.salMin),
		decStr(d.salMax), d.start, d.location, d.desc, d.reqs, d.public, d.publishUntil, actor(ctx)); err != nil {
		return JobRequisition{}, err
	}
	out, err := m.Requisition(ctx, tx, rid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.job_requisition",
		EntityID: rid.String(), EntityLabel: no + " · " + d.title, PropertyID: &property, After: requisitionAudit(out)})
}

// requisitionAudit is the audit snapshot (no salary budget).
func requisitionAudit(r JobRequisition) map[string]any {
	return map[string]any{"number": r.Number, "title": r.Title, "orgUnitId": r.OrgUnitID, "positionId": r.PositionID, "headcount": r.Headcount,
		"hiredCount": r.HiredCount, "contractType": r.ContractType, "isPublic": r.IsPublic, "status": r.Status, "budgetRecorded": r.SalaryMax != nil}
}

// lockRequisition loads a requisition for update.
func lockRequisition(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (JobRequisition, error) {
	var r JobRequisition
	err := tx.QueryRow(ctx, `SELECT id, property_id, number, title, status, headcount, hired_count, approval_request_id
		FROM hris.job_requisitions WHERE id = $1 AND property_id = $2 FOR UPDATE`, rid, handle.Property(ctx)).
		Scan(&r.ID, &r.PropertyID, &r.Number, &r.Title, &r.Status, &r.Headcount, &r.HiredCount, &r.ApprovalRequestID)
	if err != nil {
		return r, errs.NotFound("job requisition")
	}
	return r, nil
}

func (m *Module) updateRequisitionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RequisitionUpdate) (JobRequisition, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return JobRequisition{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM hris.job_requisitions WHERE id = $1 AND property_id = $2 FOR UPDATE`, rid, handle.Property(ctx)); err != nil {
		return JobRequisition{}, err
	}
	before, err := m.Requisition(ctx, tx, rid)
	if err != nil {
		return before, err
	}
	editable := before.Status == "draft" || before.Status == "rejected"
	live := before.Status == "open" || before.Status == "on_hold" || before.Status == "submitted"
	structural := req.PositionID != nil || req.GradeID != nil || req.Headcount != nil || req.Reason != nil || req.ContractType != nil ||
		req.WorkerCategory != nil || req.SalaryMin != nil || req.SalaryMax != nil || req.Title != nil
	if !editable && (!live || structural) {
		if live {
			// An approved headcount can grow only through a new requisition.
			return before, errs.Conflict("requisition_approved", "position, headcount, contract and budget are fixed once submitted: close it and request again")
		}
		return before, errs.Conflict("requisition_closed", "the requisition is "+before.Status)
	}
	property := before.PropertyID
	var cur struct {
		salMin, salMax       *decimal.Decimal
		start, publish       *time.Time
		loc, desc, reqs      *string
		replacement, manager *uuid.UUID
	}
	if err := tx.QueryRow(ctx, `SELECT salary_min, salary_max, target_start_date, publish_until, location, description, requirements, replacement_for_id,
		hiring_manager_id FROM hris.job_requisitions WHERE id = $1`, rid).Scan(&cur.salMin, &cur.salMax, &cur.start, &cur.publish, &cur.loc, &cur.desc,
		&cur.reqs, &cur.replacement, &cur.manager); err != nil {
		return before, err
	}
	d := reqDraft{title: before.Title, unit: &before.OrgUnitID, pos: before.PositionID, grade: before.GradeID, manager: cur.manager,
		replacement: cur.replacement, headcount: before.Headcount, reason: before.Reason, ctype: before.ContractType, category: before.WorkerCategory,
		salMin: cur.salMin, salMax: cur.salMax, start: cur.start, publishUntil: cur.publish, location: cur.loc, desc: cur.desc, reqs: cur.reqs,
		public: before.IsPublic}
	if req.Title != nil {
		d.title = *req.Title
	}
	if req.PositionID != nil {
		d.pos, d.unit = req.PositionID, nil
	}
	if req.GradeID != nil {
		d.grade = req.GradeID
	}
	if req.HiringManagerID != nil {
		d.manager = req.HiringManagerID
	}
	if req.Headcount != nil {
		d.headcount = *req.Headcount
	}
	if req.Reason != nil {
		d.reason = *req.Reason
	}
	if req.ContractType != nil {
		d.ctype = *req.ContractType
	}
	if req.WorkerCategory != nil {
		d.category = *req.WorkerCategory
	}
	if req.SalaryMin != nil {
		if d.salMin, err = money("salaryMin", *req.SalaryMin); err != nil {
			return before, err
		}
	}
	if req.SalaryMax != nil {
		if d.salMax, err = money("salaryMax", *req.SalaryMax); err != nil {
			return before, err
		}
	}
	if req.TargetStartDate != nil {
		if d.start, err = parseDate("targetStartDate", *req.TargetStartDate); err != nil {
			return before, err
		}
	}
	if req.PublishUntil != nil {
		if d.publishUntil, err = parseDate("publishUntil", *req.PublishUntil); err != nil {
			return before, err
		}
	}
	if req.Location != nil {
		d.location = nullStr(*req.Location)
	}
	if req.Description != nil {
		d.desc = nullStr(*req.Description)
	}
	if req.Requirements != nil {
		d.reqs = nullStr(*req.Requirements)
	}
	if req.IsPublic != nil {
		d.public = *req.IsPublic
	}
	if err := m.validateRequisition(ctx, tx, property, &d); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET title = $2, org_unit_id = $3, position_id = $4, grade_id = $5, hiring_manager_id = $6,
		headcount = $7, reason = $8, replacement_for_id = $9, contract_type = $10, worker_category = $11, salary_min = $12::numeric, salary_max = $13::numeric,
		target_start_date = $14, location = $15, description = $16, requirements = $17, is_public = $18, publish_until = $19, updated_by = $20 WHERE id = $1`,
		rid, d.title, d.unit, d.pos, d.grade, d.manager, d.headcount, d.reason, d.replacement, d.ctype, d.category, decStr(d.salMin), decStr(d.salMax),
		d.start, d.location, d.desc, d.reqs, d.public, d.publishUntil, actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.Requisition(ctx, tx, rid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.job_requisition",
		EntityID: rid.String(), EntityLabel: after.Number + " · " + after.Title, PropertyID: &property, Before: requisitionAudit(before),
		After: requisitionAudit(after)})
}

func (m *Module) submitRequisitionHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobRequisition, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return JobRequisition{}, err
	}
	cur, err := lockRequisition(ctx, tx, rid)
	if err != nil {
		return JobRequisition{}, err
	}
	if cur.Status != "draft" && cur.Status != "rejected" {
		return JobRequisition{}, errs.Conflict("requisition_not_draft", "only a draft or rejected requisition can be submitted")
	}
	full, err := m.Requisition(ctx, tx, rid)
	if err != nil {
		return full, err
	}
	var salMax float64
	var unit uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT coalesce(salary_max, 0)::float8, org_unit_id FROM hris.job_requisitions WHERE id = $1`, rid).Scan(&salMax, &unit); err != nil {
		return full, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET status = 'submitted', submitted_at = now(), decision_note = NULL, updated_by = $2
		WHERE id = $1`, rid, actor(ctx)); err != nil {
		return full, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit", EntityType: "hris.job_requisition", EntityID: rid.String(),
		EntityLabel: cur.Number + " · " + cur.Title, PropertyID: &cur.PropertyID, Reason: req.Reason, Before: map[string]any{"status": cur.Status},
		After: map[string]any{"status": "submitted"}}); err != nil {
		return full, err
	}
	aid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: RequisitionDocumentType.Code, DocumentID: rid, DocumentRef: cur.Number,
		Title: "Job requisition " + cur.Number + " · " + full.Title + " × " + itoa(full.Headcount), PropertyID: cur.PropertyID,
		Attributes: map[string]any{"headcount": float64(full.Headcount), "salaryMax": salMax, "orgUnitId": unit.String(), "reason": full.Reason,
			"contractType": full.ContractType}})
	if err != nil {
		return full, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET approval_request_id = $2 WHERE id = $1`, rid, aid); err != nil {
		return full, err
	}
	return m.Requisition(ctx, tx, rid)
}

// RequisitionDecision applies the approval decision (approval engine hook).
func (m *Module) RequisitionDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status, number, title string
	var headcount int
	var requester *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, number, title, headcount, requested_by FROM hris.job_requisitions WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&status, &number, &title, &headcount, &requester); err != nil {
		if dbtx.IsNoRows(err) {
			return nil
		}
		return err
	}
	if status != "submitted" {
		return nil
	}
	next := "rejected"
	switch d.Status {
	case approval.StatusApproved:
		next = "open"
	case approval.StatusCancelled:
		next = "draft"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET status = $2, approved_at = CASE WHEN $2 = 'open' THEN now() END, decision_note = $3
		WHERE id = $1`, d.DocumentID, next, nullStr(d.Reason)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "approval_" + d.Status, EntityType: "hris.job_requisition",
		EntityID: d.DocumentID.String(), EntityLabel: number + " · " + title, PropertyID: &d.PropertyID, Reason: d.Reason,
		Before: map[string]any{"status": status}, After: map[string]any{"status": next}}); err != nil {
		return err
	}
	if requester == nil || d.Status == approval.StatusCancelled {
		return nil
	}
	decision := map[string]string{"open": "approved", "rejected": "rejected"}[next]
	users := []uuid.UUID{*requester}
	if next == "open" {
		users = append(users, holders(ctx, tx, d.PropertyID, "hris.application.manage")...)
	}
	return m.notifyUsers(ctx, tx, d.PropertyID, users, "hris.requisition_decided", "/hris/recruitment/requisitions/"+d.DocumentID.String(),
		map[string]any{"number": number, "title": title, "headcount": headcount, "decision": decision, "reason": d.Reason})
}

// decideRequisitionHTTP approves / rejects the current approval step of the
// requisition through the approval engine (the caller must be an approver).
func (m *Module) decideRequisitionHTTP(approve bool) func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobRequisition, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobRequisition, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return JobRequisition{}, err
		}
		cur, err := lockRequisition(ctx, tx, rid)
		if err != nil {
			return JobRequisition{}, err
		}
		if cur.Status != "submitted" || cur.ApprovalRequestID == nil {
			return JobRequisition{}, errs.Conflict("requisition_not_submitted", "the requisition waits for no approval")
		}
		if err := m.Approvals.Decide(ctx, tx, *cur.ApprovalRequestID, approve, req.Reason); err != nil {
			return JobRequisition{}, err
		}
		return m.Requisition(ctx, tx, rid)
	}
}

// requisitionStatusHTTP holds, reopens or closes a requisition.
func (m *Module) requisitionStatusHTTP(action string) func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobRequisition, error) {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (JobRequisition, error) {
		rid, err := handle.ID(r)
		if err != nil {
			return JobRequisition{}, err
		}
		cur, err := lockRequisition(ctx, tx, rid)
		if err != nil {
			return JobRequisition{}, err
		}
		var next string
		switch action {
		case "hold":
			if cur.Status != "open" {
				return cur, errs.Conflict("requisition_not_open", "only an open requisition can be put on hold")
			}
			next = "on_hold"
		case "reopen":
			if cur.Status != "on_hold" && cur.Status != "closed" {
				return cur, errs.Conflict("requisition_not_closed", "only a requisition on hold or closed can be reopened")
			}
			if cur.Status == "closed" && (cur.HiredCount >= cur.Headcount || cur.ApprovalRequestID == nil) {
				return cur, errs.Conflict("requisition_filled", "every position of the requisition is filled")
			}
			next = "open"
		case "close":
			switch cur.Status {
			case "draft", "rejected":
				next = "cancelled"
			case "submitted":
				if cur.ApprovalRequestID != nil {
					if err := m.Approvals.Cancel(ctx, tx, *cur.ApprovalRequestID, "requisition closed"); err != nil {
						return cur, err
					}
				}
				next = "cancelled"
			case "open", "on_hold":
				next = "closed"
			default:
				return cur, errs.Conflict("requisition_closed", "the requisition is already "+cur.Status)
			}
			if strings.TrimSpace(req.Reason) == "" {
				return cur, handle.Invalid("reason", "required", "a reason is required")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.job_requisitions SET status = $2, closed_at = CASE WHEN $2 IN ('closed', 'cancelled') THEN now() END,
			close_reason = CASE WHEN $2 IN ('closed', 'cancelled', 'on_hold') THEN $3 ELSE close_reason END, updated_by = $4 WHERE id = $1`,
			rid, next, nullStr(req.Reason), actor(ctx)); err != nil {
			return cur, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: action, EntityType: "hris.job_requisition", EntityID: rid.String(),
			EntityLabel: cur.Number + " · " + cur.Title, PropertyID: &cur.PropertyID, Reason: req.Reason, Before: map[string]any{"status": cur.Status},
			After: map[string]any{"status": next}}); err != nil {
			return cur, err
		}
		return m.Requisition(ctx, tx, rid)
	}
}
