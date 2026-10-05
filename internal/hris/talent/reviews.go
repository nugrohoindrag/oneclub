package talent

// Performance Review (EP-05): HR opens a review cycle (annual, semester or
// probation) and launches it — every eligible employee gets a review from
// the template of their position (competencies and KPIs with weights) and
// their supervisor as reviewer, with the operational inputs of the period
// (FR-PRF-HR-03). The employee completes the self assessment in Employee
// Self Service, the manager scores and submits (ESS Team Reviews), HR
// calibrates the rating against the guide of the Performance Review
// Configuration (:calibrate), and closing the cycle completes the reviews:
// the result is written to the employment history, published as
// hris.performance_review_completed (salary increase / bonus basis,
// FR-PRF-HR-04) and shown to the employee. A strong result can promote the
// employee through Core HR (:promote).

import (
	"context"
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

// Review statuses.
var (
	CycleStatuses   = []string{"draft", "in_progress", "calibration", "completed", "cancelled"}
	ReviewStatuses  = []string{"self_assessment", "manager_review", "submitted", "calibrated", "completed", "cancelled"}
	Recommendations = []string{"none", "salary_increase", "bonus", "promotion", "confirm_employment", "extend_probation", "improvement_plan", "terminate"}
)

// ReviewCycle is a review cycle with its progress.
type ReviewCycle struct {
	ID                   uuid.UUID  `json:"id" db:"id"`
	PropertyID           uuid.UUID  `json:"propertyId" db:"property_id"`
	Code                 string     `json:"code" db:"code"`
	Name                 string     `json:"name" db:"name"`
	CycleType            string     `json:"cycleType" db:"cycle_type" enum:"annual,semester,probation"`
	PeriodStart          time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd            time.Time  `json:"periodEnd" db:"period_end"`
	SelfDue              *time.Time `json:"selfDue" db:"self_due"`
	ManagerDue           *time.Time `json:"managerDue" db:"manager_due"`
	CalibrationDue       *time.Time `json:"calibrationDue" db:"calibration_due"`
	OrgUnitID            *uuid.UUID `json:"orgUnitId" db:"org_unit_id"`
	OrgUnitName          *string    `json:"orgUnitName" db:"org_unit_name"`
	DefaultTemplateID    *uuid.UUID `json:"defaultTemplateId" db:"default_template_id"`
	DefaultTemplateName  *string    `json:"defaultTemplateName" db:"default_template_name"`
	Status               string     `json:"status" db:"status" enum:"draft,in_progress,calibration,completed,cancelled"`
	LaunchedAt           *time.Time `json:"launchedAt" db:"launched_at"`
	CalibrationStartedAt *time.Time `json:"calibrationStartedAt" db:"calibration_started_at"`
	ClosedAt             *time.Time `json:"closedAt" db:"closed_at"`
	PolicyVersion        int        `json:"policyVersion" db:"policy_version" doc:"Performance Review Configuration version the cycle was launched with"`
	Notes                *string    `json:"notes" db:"notes"`
	Reviews              int        `json:"reviews" db:"reviews"`
	SelfPending          int        `json:"selfPending" db:"self_pending"`
	ManagerPending       int        `json:"managerPending" db:"manager_pending"`
	Submitted            int        `json:"submitted" db:"submitted"`
	Calibrated           int        `json:"calibrated" db:"calibrated"`
	Completed            int        `json:"completed" db:"completed"`
	CompletionRate       string     `json:"completionRate" db:"completion_rate" doc:"Reviews submitted, calibrated or completed ÷ reviews"`
	CreatedAt            time.Time  `json:"createdAt" db:"created_at"`
}

const cycleSelect = `SELECT c.id, c.property_id, c.code, c.name, c.cycle_type, c.period_start, c.period_end, c.self_due, c.manager_due, c.calibration_due,
	c.org_unit_id, ou.name AS org_unit_name, c.default_template_id, t.name AS default_template_name, c.status, c.launched_at, c.calibration_started_at,
	c.closed_at, c.policy_version, c.notes, coalesce(x.reviews, 0) AS reviews, coalesce(x.self_pending, 0) AS self_pending,
	coalesce(x.manager_pending, 0) AS manager_pending, coalesce(x.submitted, 0) AS submitted, coalesce(x.calibrated, 0) AS calibrated,
	coalesce(x.completed, 0) AS completed,
	trim_scale(round(coalesce((coalesce(x.submitted, 0) + coalesce(x.calibrated, 0) + coalesce(x.completed, 0))::numeric / nullif(x.reviews, 0), 0), 4))::text
	  AS completion_rate, c.created_at
	FROM hris.review_cycles c LEFT JOIN hris.org_units ou ON ou.id = c.org_unit_id LEFT JOIN hris.review_templates t ON t.id = c.default_template_id
	LEFT JOIN LATERAL (SELECT count(*)::int AS reviews, count(*) FILTER (WHERE status = 'self_assessment')::int AS self_pending,
	  count(*) FILTER (WHERE status = 'manager_review')::int AS manager_pending, count(*) FILTER (WHERE status = 'submitted')::int AS submitted,
	  count(*) FILTER (WHERE status = 'calibrated')::int AS calibrated, count(*) FILTER (WHERE status = 'completed')::int AS completed
	  FROM hris.performance_reviews r WHERE r.cycle_id = c.id AND r.status <> 'cancelled') x ON true`

// ReviewCycleRequest creates a cycle.
type ReviewCycleRequest struct {
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	CycleType         string     `json:"cycleType" enum:"annual,semester,probation"`
	PeriodStart       string     `json:"periodStart"`
	PeriodEnd         string     `json:"periodEnd"`
	SelfDue           string     `json:"selfDue,omitempty"`
	ManagerDue        string     `json:"managerDue,omitempty"`
	CalibrationDue    string     `json:"calibrationDue,omitempty"`
	OrgUnitID         *uuid.UUID `json:"orgUnitId,omitempty" doc:"Limit to an org unit and its sub-units (empty = whole property)"`
	DefaultTemplateID *uuid.UUID `json:"defaultTemplateId,omitempty" doc:"Template of employees whose position has none"`
	Notes             string     `json:"notes,omitempty"`
}

// ReviewCycleUpdate edits a cycle (dates and template while draft).
type ReviewCycleUpdate struct {
	Name              *string    `json:"name,omitempty"`
	PeriodStart       *string    `json:"periodStart,omitempty"`
	PeriodEnd         *string    `json:"periodEnd,omitempty"`
	SelfDue           *string    `json:"selfDue,omitempty"`
	ManagerDue        *string    `json:"managerDue,omitempty"`
	CalibrationDue    *string    `json:"calibrationDue,omitempty"`
	OrgUnitID         *uuid.UUID `json:"orgUnitId,omitempty"`
	DefaultTemplateID *uuid.UUID `json:"defaultTemplateId,omitempty"`
	Notes             *string    `json:"notes,omitempty"`
}

// ReviewCycleLaunchRequest launches a cycle (or adds late joiners).
type ReviewCycleLaunchRequest struct {
	EmployeeIDs []uuid.UUID `json:"employeeIds,omitempty" doc:"Only these employees (default: every eligible employee)"`
}

// ReviewCycleLaunchResult lists what the launch did.
type ReviewCycleLaunchResult struct {
	Cycle   ReviewCycle `json:"cycle"`
	Created int         `json:"created"`
	Skipped []string    `json:"skipped" doc:"Employees without a template or reviewer"`
}

// ReviewCycleCloseRequest closes a cycle.
type ReviewCycleCloseRequest struct {
	Force  bool   `json:"force,omitempty" doc:"Cancel the reviews that are not calibrated yet"`
	Reason string `json:"reason,omitempty"`
}

// PerformanceReview is a review of an employee in a cycle.
type PerformanceReview struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	PropertyID         uuid.UUID  `json:"propertyId" db:"property_id"`
	CycleID            uuid.UUID  `json:"cycleId" db:"cycle_id"`
	CycleCode          string     `json:"cycleCode" db:"cycle_code"`
	CycleName          string     `json:"cycleName" db:"cycle_name"`
	CycleType          string     `json:"cycleType" db:"cycle_type"`
	CycleStatus        string     `json:"cycleStatus" db:"cycle_status"`
	PeriodStart        time.Time  `json:"periodStart" db:"period_start"`
	PeriodEnd          time.Time  `json:"periodEnd" db:"period_end"`
	SelfDue            *time.Time `json:"selfDue" db:"self_due"`
	ManagerDue         *time.Time `json:"managerDue" db:"manager_due"`
	EmployeeID         uuid.UUID  `json:"employeeId" db:"employee_id"`
	EmployeeNo         string     `json:"employeeNo" db:"employee_no"`
	EmployeeName       string     `json:"employeeName" db:"employee_name"`
	OrgUnitName        *string    `json:"orgUnitName" db:"org_unit_name"`
	PositionName       *string    `json:"positionName" db:"position_name"`
	GradeCode          *string    `json:"gradeCode" db:"grade_code"`
	ReviewerID         *uuid.UUID `json:"reviewerId" db:"reviewer_id"`
	ReviewerName       *string    `json:"reviewerName" db:"reviewer_name"`
	TemplateID         *uuid.UUID `json:"templateId" db:"template_id"`
	TemplateName       *string    `json:"templateName" db:"template_name"`
	CompetencyWeight   string     `json:"competencyWeight" db:"competency_weight"`
	Status             string     `json:"status" db:"status" enum:"self_assessment,manager_review,submitted,calibrated,completed,cancelled"`
	SelfScore          *string    `json:"selfScore" db:"self_score"`
	SelfComment        *string    `json:"selfComment" db:"self_comment"`
	SelfSubmittedAt    *time.Time `json:"selfSubmittedAt" db:"self_submitted_at"`
	ManagerScore       *string    `json:"managerScore" db:"manager_score"`
	ManagerComment     *string    `json:"managerComment" db:"manager_comment"`
	Strengths          *string    `json:"strengths" db:"strengths"`
	Improvements       *string    `json:"improvements" db:"improvements"`
	Goals              *string    `json:"goals" db:"goals"`
	ManagerSubmittedAt *time.Time `json:"managerSubmittedAt" db:"manager_submitted_at"`
	RecommendedRating  *string    `json:"recommendedRating" db:"recommended_rating"`
	FinalScore         *string    `json:"finalScore" db:"final_score"`
	FinalRating        *string    `json:"finalRating" db:"final_rating"`
	CalibratedAt       *time.Time `json:"calibratedAt" db:"calibrated_at"`
	CalibrationNote    *string    `json:"calibrationNote" db:"calibration_note"`
	Recommendation     string     `json:"recommendation" db:"recommendation" enum:"none,salary_increase,bonus,promotion,confirm_employment,extend_probation,improvement_plan,terminate"`
	IncreasePercent    *string    `json:"increasePercent" db:"increase_percent"`
	BonusMonths        *string    `json:"bonusMonths" db:"bonus_months"`
	EmploymentChangeID *uuid.UUID `json:"employmentChangeId" db:"employment_change_id" doc:"Promotion recorded through Core HR"`
	AcknowledgedAt     *time.Time `json:"acknowledgedAt" db:"acknowledged_at"`
	CompletedAt        *time.Time `json:"completedAt" db:"completed_at"`
	PolicyVersion      int        `json:"policyVersion" db:"policy_version"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
}

const reviewSelect = `SELECT r.id, r.property_id, r.cycle_id, c.code AS cycle_code, c.name AS cycle_name, c.cycle_type, c.status AS cycle_status, c.period_start,
	c.period_end, c.self_due, c.manager_due, r.employee_id, e.employee_no, e.full_name AS employee_name, ou.name AS org_unit_name, p.name AS position_name,
	g.code AS grade_code, r.reviewer_id, rv.full_name AS reviewer_name, r.template_id, t.name AS template_name,
	trim_scale(r.competency_weight)::text AS competency_weight, r.status, trim_scale(r.self_score)::text AS self_score, r.self_comment, r.self_submitted_at,
	trim_scale(r.manager_score)::text AS manager_score, r.manager_comment, r.strengths, r.improvements, r.goals, r.manager_submitted_at,
	r.recommended_rating, trim_scale(r.final_score)::text AS final_score, r.final_rating, r.calibrated_at, r.calibration_note, r.recommendation,
	trim_scale(r.increase_percent)::text AS increase_percent, trim_scale(r.bonus_months)::text AS bonus_months, r.employment_change_id,
	r.acknowledged_at, r.completed_at, r.policy_version, r.created_at
	FROM hris.performance_reviews r JOIN hris.review_cycles c ON c.id = r.cycle_id JOIN hris.employees e ON e.id = r.employee_id
	LEFT JOIN hris.org_units ou ON ou.id = e.org_unit_id LEFT JOIN hris.positions p ON p.id = e.position_id LEFT JOIN hris.grades g ON g.id = e.grade_id
	LEFT JOIN hris.employees rv ON rv.id = r.reviewer_id LEFT JOIN hris.review_templates t ON t.id = r.template_id`

// ReviewScore is the score of a competency or KPI.
type ReviewScore struct {
	ID             uuid.UUID `json:"id" db:"id"`
	ItemKind       string    `json:"itemKind" db:"item_kind" enum:"competency,kpi"`
	Code           string    `json:"code" db:"code"`
	Label          string    `json:"label" db:"label"`
	Description    *string   `json:"description" db:"description"`
	Weight         string    `json:"weight" db:"weight"`
	Target         *string   `json:"target" db:"target"`
	Actual         *string   `json:"actual" db:"actual"`
	SelfScore      *string   `json:"selfScore" db:"self_score"`
	SelfComment    *string   `json:"selfComment" db:"self_comment"`
	ManagerScore   *string   `json:"managerScore" db:"manager_score"`
	ManagerComment *string   `json:"managerComment" db:"manager_comment"`
}

const scoreSelect = `SELECT id, item_kind, code, label, description, trim_scale(weight)::text AS weight, target, actual, trim_scale(self_score)::text AS self_score,
	self_comment, trim_scale(manager_score)::text AS manager_score, manager_comment FROM hris.review_scores`

// PerformanceReviewDetail is the review screen.
type PerformanceReviewDetail struct {
	Review         PerformanceReview   `json:"review"`
	Scores         []ReviewScore       `json:"scores"`
	Inputs         []hris.ReviewInput  `json:"inputs" doc:"Operational data of the period (attendance, sales target, training …)"`
	Bands          []hris.RatingBand   `json:"bands"`
	ScoreScale     int                 `json:"scoreScale"`
	Previous       []PerformanceReview `json:"previous" doc:"Earlier completed reviews of the employee"`
	CanEditSelf    bool                `json:"canEditSelf"`
	CanEditManager bool                `json:"canEditManager"`
}

// ReviewScoreInput is one item score of a self or manager review.
type ReviewScoreInput struct {
	ItemKind string `json:"itemKind" enum:"competency,kpi"`
	Code     string `json:"code"`
	Score    string `json:"score,omitempty" doc:"1 … score scale; empty clears"`
	Comment  string `json:"comment,omitempty"`
	Actual   string `json:"actual,omitempty" doc:"KPI result (manager review)"`
}

// ReviewSelfInput saves the self assessment.
type ReviewSelfInput struct {
	Scores  []ReviewScoreInput `json:"scores,omitempty"`
	Comment *string            `json:"comment,omitempty"`
}

// ReviewManagerInput saves the manager review.
type ReviewManagerInput struct {
	Scores         []ReviewScoreInput `json:"scores,omitempty"`
	Comment        *string            `json:"comment,omitempty"`
	Strengths      *string            `json:"strengths,omitempty"`
	Improvements   *string            `json:"improvements,omitempty"`
	Goals          *string            `json:"goals,omitempty"`
	Recommendation *string            `json:"recommendation,omitempty" enum:"none,salary_increase,bonus,promotion,confirm_employment,extend_probation,improvement_plan,terminate"`
	ReviewerID     *uuid.UUID         `json:"reviewerId,omitempty" doc:"Reassign the reviewer (HR only)"`
}

// ReviewCalibrateRequest calibrates a submitted review.
type ReviewCalibrateRequest struct {
	FinalScore      string `json:"finalScore,omitempty" doc:"Default: the recommended score"`
	FinalRating     string `json:"finalRating,omitempty" doc:"Rating band code; default: the band of the final score"`
	Recommendation  string `json:"recommendation,omitempty" enum:"none,salary_increase,bonus,promotion,confirm_employment,extend_probation,improvement_plan,terminate"`
	IncreasePercent string `json:"increasePercent,omitempty" doc:"Default: the band's"`
	BonusMonths     string `json:"bonusMonths,omitempty" doc:"Default: the band's"`
	Note            string `json:"note,omitempty" doc:"Required when the rating differs from the band of the score"`
}

// ReviewPromoteRequest promotes the employee through Core HR.
type ReviewPromoteRequest struct {
	PositionID    *uuid.UUID `json:"positionId,omitempty"`
	GradeID       *uuid.UUID `json:"gradeId,omitempty"`
	EffectiveDate string     `json:"effectiveDate"`
	Reason        string     `json:"reason,omitempty"`
}

// CalibrationView is the calibration screen of a cycle.
type CalibrationView struct {
	Cycle        ReviewCycle         `json:"cycle"`
	Distribution []hris.BandShare    `json:"distribution" doc:"Final (or recommended) ratings against the calibration guide"`
	Reviews      []PerformanceReview `json:"reviews"`
	Bands        []hris.RatingBand   `json:"bands"`
}

func (m *Module) registerCycles(reg *route.Registry) {
	tag := "HRIS Performance Review"
	base := "/api/v1/hris/review-cycles"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Review cycles", Permission: "hris.review_cycle.view", Response: ReviewCycle{},
		List: true, Query: []route.Param{{Name: "status", Enum: CycleStatuses}}, Handler: listRead(m.DB, m.listCyclesHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base, Summary: "Create a review cycle (draft)", Permission: "hris.review_cycle.manage",
		Request: ReviewCycleRequest{}, Response: ReviewCycle{}, Idempotent: true, Handler: handle.Write(m.DB, http.StatusCreated, m.createCycleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Review cycle", Permission: "hris.review_cycle.view",
		Response: ReviewCycle{}, Handler: handle.Read(m.DB, m.getCycleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Edit a review cycle", Permission: "hris.review_cycle.manage",
		Request: ReviewCycleUpdate{}, Response: ReviewCycle{}, Handler: handle.Write(m.DB, http.StatusOK, m.updateCycleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:launch", Summary: "Launch the cycle: create the reviews of the eligible employees",
		Permission: "hris.review_cycle.manage", Request: ReviewCycleLaunchRequest{}, Response: ReviewCycleLaunchResult{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.launchHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:start-calibration", Summary: "Start the HR calibration of the cycle",
		Permission: "hris.review_cycle.manage", Request: RecruitmentReason{}, Response: ReviewCycle{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.startCalibrationHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}/calibration", Summary: "Calibration: rating distribution against the guide",
		Permission: "hris.performance_review.view", Response: CalibrationView{}, Handler: handle.Read(m.DB, m.calibrationHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:close", Summary: "Close the cycle: complete the calibrated reviews",
		Permission: "hris.review_cycle.manage", Request: ReviewCycleCloseRequest{}, Response: ReviewCycle{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.closeCycleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:cancel", Summary: "Cancel a review cycle", Permission: "hris.review_cycle.manage",
		Request: RecruitmentReason{}, Response: ReviewCycle{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.cancelCycleHTTP)})
}

func (m *Module) registerReviews(reg *route.Registry) {
	tag := "HRIS Performance Review"
	base := "/api/v1/hris/reviews"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base, Summary: "Performance reviews", Permission: "hris.performance_review.view",
		Response: PerformanceReview{}, List: true, Query: []route.Param{{Name: "cycleId"}, {Name: "status", Enum: ReviewStatuses}, {Name: "employeeId"},
			{Name: "orgUnitId"}, {Name: "q"}}, Handler: listRead(m.DB, m.listReviewsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: base + "/{id}", Summary: "Performance review with scores and inputs",
		Permission: "hris.performance_review.view", Response: PerformanceReviewDetail{}, Handler: handle.Read(m.DB, m.reviewDetailHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: base + "/{id}", Summary: "Fill the manager review (HR)", Permission: "hris.performance_review.manage",
		Request: ReviewManagerInput{}, Response: PerformanceReviewDetail{}, Handler: handle.Write(m.DB, http.StatusOK, m.hrManagerSaveHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:submit", Summary: "Submit the manager review for calibration",
		Permission: "hris.performance_review.manage", Request: RecruitmentReason{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.hrSubmitHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:calibrate", Summary: "Calibrate the final rating (HR)",
		Permission: "hris.performance_review.calibrate", Request: ReviewCalibrateRequest{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.calibrateHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:reopen", Summary: "Send a submitted review back to the manager",
		Permission: "hris.performance_review.manage", Request: RecruitmentReason{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.reopenHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: base + "/{id}:promote", Summary: "Promote the employee from the review (Core HR :promote)",
		Permission: "hris.performance_review.promote", Request: ReviewPromoteRequest{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.promoteHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/employees/{id}/reviews", Summary: "Performance review history of an employee",
		Permission: "hris.performance_review.view", Response: PerformanceReview{}, List: true, Handler: listRead(m.DB, m.employeeReviewsHTTP)})
}

// ── cycles ───────────────────────────────────────────────────────────────

func (m *Module) cycle(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (ReviewCycle, error) {
	return getOne[ReviewCycle]("review cycle")(tx.Query(ctx, cycleSelect+` WHERE c.id = $1 AND c.property_id = $2`, cid, handle.Property(ctx)))
}

func (m *Module) listCyclesHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]ReviewCycle, error) {
	return handle.List[ReviewCycle](tx.Query(ctx, cycleSelect+` WHERE c.property_id = $1 AND ($2 = '' OR c.status = $2) AND ($3 = '' OR c.name ILIKE $3
		OR c.code ILIKE $3) ORDER BY c.period_end DESC, c.code`, handle.Property(ctx), filterParam(r, "status"), likeParam(r)))
}

func (m *Module) getCycleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (ReviewCycle, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycle{}, err
	}
	return m.cycle(ctx, tx, cid)
}

type cycleDraft struct {
	name                        string
	start, end                  time.Time
	selfDue, managerDue, calDue *time.Time
	unit, template              *uuid.UUID
	notes                       *string
}

func validateCycle(ctx context.Context, tx pgx.Tx, property uuid.UUID, d *cycleDraft) error {
	d.name = strings.TrimSpace(d.name)
	if d.name == "" {
		return handle.Invalid("name", "required", "a name is required")
	}
	if d.end.Before(d.start) {
		return handle.Invalid("periodEnd", "invalid", "must be on or after the period start")
	}
	if d.selfDue != nil && d.managerDue != nil && d.managerDue.Before(*d.selfDue) {
		return handle.Invalid("managerDue", "invalid", "must be on or after the self assessment due date")
	}
	if d.managerDue != nil && d.calDue != nil && d.calDue.Before(*d.managerDue) {
		return handle.Invalid("calibrationDue", "invalid", "must be on or after the manager review due date")
	}
	var ok bool
	if d.unit != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.org_units WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`, *d.unit, property).
			Scan(&ok); err != nil || !ok {
			return handle.Invalid("orgUnitId", "not_found", "org unit not found")
		}
	}
	if d.template != nil {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.review_templates WHERE id = $1 AND property_id = $2 AND archived_at IS NULL
			AND status = 'active')`, *d.template, property).Scan(&ok); err != nil || !ok {
			return handle.Invalid("defaultTemplateId", "not_found", "active review template not found")
		}
	}
	return nil
}

func (m *Module) createCycleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewCycleRequest) (ReviewCycle, error) {
	property := handle.Property(ctx)
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if !codeRe30.MatchString(code) {
		return ReviewCycle{}, handle.Invalid("code", "invalid_format", "1–30 characters: A–Z, 0–9, - or _")
	}
	if !oneOf([]string{"annual", "semester", "probation"}, req.CycleType) {
		return ReviewCycle{}, enumErr("cycleType", []string{"annual", "semester", "probation"})
	}
	d := cycleDraft{name: req.Name, unit: req.OrgUnitID, template: req.DefaultTemplateID, notes: nullStr(req.Notes)}
	var err error
	if d.start, err = mustDate("periodStart", req.PeriodStart); err != nil {
		return ReviewCycle{}, err
	}
	if d.end, err = mustDate("periodEnd", req.PeriodEnd); err != nil {
		return ReviewCycle{}, err
	}
	if d.selfDue, err = parseDate("selfDue", req.SelfDue); err != nil {
		return ReviewCycle{}, err
	}
	if d.managerDue, err = parseDate("managerDue", req.ManagerDue); err != nil {
		return ReviewCycle{}, err
	}
	if d.calDue, err = parseDate("calibrationDue", req.CalibrationDue); err != nil {
		return ReviewCycle{}, err
	}
	if err := validateCycle(ctx, tx, property, &d); err != nil {
		return ReviewCycle{}, err
	}
	var dup bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.review_cycles WHERE property_id = $1 AND code = $2)`, property, code).Scan(&dup); err != nil {
		return ReviewCycle{}, err
	}
	if dup {
		return ReviewCycle{}, errs.Conflict("duplicate_code", "a review cycle with this code exists")
	}
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.review_cycles (id, property_id, code, name, cycle_type, period_start, period_end, self_due, manager_due,
		calibration_due, org_unit_id, default_template_id, notes, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)`,
		cid, property, code, d.name, req.CycleType, d.start, d.end, d.selfDue, d.managerDue, d.calDue, d.unit, d.template, d.notes, actor(ctx)); err != nil {
		return ReviewCycle{}, err
	}
	out, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: code + " · " + d.name, PropertyID: &property, After: cycleAudit(out)})
}

func cycleAudit(c ReviewCycle) map[string]any {
	return map[string]any{"code": c.Code, "name": c.Name, "cycleType": c.CycleType, "periodStart": ymd(c.PeriodStart), "periodEnd": ymd(c.PeriodEnd),
		"status": c.Status, "reviews": c.Reviews}
}

func lockCycle(ctx context.Context, tx pgx.Tx, cid uuid.UUID) (ReviewCycle, error) {
	var c ReviewCycle
	if err := tx.QueryRow(ctx, `SELECT id, property_id, code, name, cycle_type, status, period_start, period_end, self_due, manager_due, org_unit_id,
		default_template_id FROM hris.review_cycles WHERE id = $1 AND property_id = $2 FOR UPDATE`, cid, handle.Property(ctx)).
		Scan(&c.ID, &c.PropertyID, &c.Code, &c.Name, &c.CycleType, &c.Status, &c.PeriodStart, &c.PeriodEnd, &c.SelfDue, &c.ManagerDue, &c.OrgUnitID,
			&c.DefaultTemplateID); err != nil {
		return c, errs.NotFound("review cycle")
	}
	return c, nil
}

func (m *Module) updateCycleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewCycleUpdate) (ReviewCycle, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycle{}, err
	}
	cur, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return cur, err
	}
	if cur.Status == "completed" || cur.Status == "cancelled" {
		return cur, errs.Conflict("cycle_closed", "the review cycle is "+cur.Status)
	}
	if cur.Status != "draft" && (req.PeriodStart != nil || req.PeriodEnd != nil || req.OrgUnitID != nil || req.DefaultTemplateID != nil) {
		return cur, errs.Conflict("cycle_launched", "period, org unit and template are fixed once the cycle is launched")
	}
	before, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return before, err
	}
	d := cycleDraft{name: before.Name, start: before.PeriodStart, end: before.PeriodEnd, selfDue: before.SelfDue, managerDue: before.ManagerDue,
		calDue: before.CalibrationDue, unit: before.OrgUnitID, template: before.DefaultTemplateID, notes: before.Notes}
	if req.Name != nil {
		d.name = *req.Name
	}
	for _, f := range []struct {
		field string
		in    *string
		out   **time.Time
	}{{"selfDue", req.SelfDue, &d.selfDue}, {"managerDue", req.ManagerDue, &d.managerDue}, {"calibrationDue", req.CalibrationDue, &d.calDue}} {
		if f.in != nil {
			if *f.out, err = parseDate(f.field, *f.in); err != nil {
				return before, err
			}
		}
	}
	if req.PeriodStart != nil {
		if d.start, err = mustDate("periodStart", *req.PeriodStart); err != nil {
			return before, err
		}
	}
	if req.PeriodEnd != nil {
		if d.end, err = mustDate("periodEnd", *req.PeriodEnd); err != nil {
			return before, err
		}
	}
	if req.OrgUnitID != nil {
		d.unit = req.OrgUnitID
	}
	if req.DefaultTemplateID != nil {
		d.template = req.DefaultTemplateID
	}
	if req.Notes != nil {
		d.notes = nullStr(*req.Notes)
	}
	if err := validateCycle(ctx, tx, cur.PropertyID, &d); err != nil {
		return before, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.review_cycles SET name = $2, period_start = $3, period_end = $4, self_due = $5, manager_due = $6, calibration_due = $7,
		org_unit_id = $8, default_template_id = $9, notes = $10, updated_by = $11 WHERE id = $1`, cid, d.name, d.start, d.end, d.selfDue, d.managerDue, d.calDue,
		d.unit, d.template, d.notes, actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: after.Code + " · " + after.Name, PropertyID: &cur.PropertyID, Before: cycleAudit(before), After: cycleAudit(after)})
}

type templateRow struct {
	id           uuid.UUID
	reviewType   string
	positions    []string
	competencies []ReviewTemplateItem
	kpis         []ReviewTemplateItem
	weight       string
}

// pickTemplate is the template of an employee: the active template of the
// cycle type listing the position, else one for every position, else the
// cycle default.
func pickTemplate(list []templateRow, cycleType string, position *string, def *uuid.UUID) *templateRow {
	fits := func(t templateRow) bool { return t.reviewType == cycleType || t.reviewType == "any" }
	if position != nil {
		for i, t := range list {
			if fits(t) && slices.Contains(t.positions, *position) {
				return &list[i]
			}
		}
	}
	for i, t := range list {
		if fits(t) && len(t.positions) == 0 && t.reviewType == cycleType {
			return &list[i]
		}
	}
	if def != nil {
		for i, t := range list {
			if t.id == *def {
				return &list[i]
			}
		}
	}
	for i, t := range list {
		if fits(t) && len(t.positions) == 0 {
			return &list[i]
		}
	}
	return nil
}

func loadTemplates(ctx context.Context, tx pgx.Tx, property uuid.UUID) ([]templateRow, error) {
	rows, err := tx.Query(ctx, `SELECT id, review_type, position_codes, competencies, kpis, competency_weight::text FROM hris.review_templates
		WHERE property_id = $1 AND archived_at IS NULL AND status = 'active' ORDER BY code`, property)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (templateRow, error) {
		var t templateRow
		err := row.Scan(&t.id, &t.reviewType, &t.positions, &t.competencies, &t.kpis, &t.weight)
		return t, err
	})
}

func (m *Module) launchHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewCycleLaunchRequest) (ReviewCycleLaunchResult, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycleLaunchResult{}, err
	}
	c, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return ReviewCycleLaunchResult{}, err
	}
	if c.Status != "draft" && c.Status != "in_progress" {
		return ReviewCycleLaunchResult{}, errs.Conflict("cycle_not_open", "only a draft or running cycle can be launched")
	}
	res, err := m.Launch(ctx, tx, c, req.EmployeeIDs)
	if err != nil {
		return res, err
	}
	if res.Created == 0 && c.Status == "draft" {
		return res, errs.Conflict("no_reviews", "no eligible employee with a review template and a reviewer: check the templates")
	}
	return res, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "launch", EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: c.Code + " · " + c.Name, PropertyID: &c.PropertyID, Before: map[string]any{"status": c.Status},
		After: map[string]any{"status": res.Cycle.Status, "created": res.Created, "skipped": len(res.Skipped)}})
}

// Launch creates the reviews of the eligible employees of a cycle (draft →
// in progress); employees who already have a review are left alone.
func (m *Module) Launch(ctx context.Context, tx pgx.Tx, c ReviewCycle, only []uuid.UUID) (ReviewCycleLaunchResult, error) {
	cfg, ref, err := hris.LoadPerformanceConfiguration(ctx, tx, c.PropertyID, clock.Now())
	if err != nil {
		return ReviewCycleLaunchResult{}, err
	}
	templates, err := loadTemplates(ctx, tx, c.PropertyID)
	if err != nil {
		return ReviewCycleLaunchResult{}, err
	}
	end := c.PeriodEnd
	f := hris.EmployeeFilter{PropertyID: c.PropertyID, OrgUnitID: c.OrgUnitID, ActiveOn: &end, IDs: only}
	if c.CycleType == "probation" {
		f.Statuses = []string{hris.StatusProbation}
	}
	emps, err := hris.Employees(ctx, tx, f)
	if err != nil {
		return ReviewCycleLaunchResult{}, err
	}
	res := ReviewCycleLaunchResult{Skipped: []string{}}
	status := "manager_review"
	if cfg.RequireSelfAssessment {
		status = "self_assessment"
	}
	for _, e := range emps {
		if e.WorkerCategory == "daily" {
			continue
		}
		if c.CycleType == "probation" && (e.ProbationEndDate == nil || e.ProbationEndDate.Before(c.PeriodStart) || e.ProbationEndDate.After(c.PeriodEnd)) {
			continue
		}
		if c.CycleType != "probation" && e.JoinDate != nil && e.JoinDate.After(c.PeriodEnd) {
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.performance_reviews WHERE cycle_id = $1 AND employee_id = $2)`, c.ID, e.ID).
			Scan(&exists); err != nil {
			return res, err
		}
		if exists {
			continue
		}
		t := pickTemplate(templates, c.CycleType, e.PositionCode, c.DefaultTemplateID)
		if t == nil || len(t.competencies)+len(t.kpis) == 0 {
			res.Skipped = append(res.Skipped, e.FullName+" ("+e.EmployeeNo+"): no review template for the position")
			continue
		}
		sup, err := hris.Supervisor(ctx, tx, e.ID)
		if err != nil {
			return res, err
		}
		if sup == nil {
			res.Skipped = append(res.Skipped, e.FullName+" ("+e.EmployeeNo+"): no supervisor to review")
			continue
		}
		inputs, err := hris.ReviewInputs(ctx, tx, e, c.PeriodStart, c.PeriodEnd)
		if err != nil {
			return res, err
		}
		rid := id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO hris.performance_reviews (id, property_id, cycle_id, employee_id, reviewer_id, template_id, competency_weight,
			status, inputs, policy_version, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9,$10,$11,$11)`,
			rid, c.PropertyID, c.ID, e.ID, sup.ID, t.id, t.weight, status, jsonOf(inputs), ref.Version, actor(ctx)); err != nil {
			return res, err
		}
		order := 0
		for _, set := range []struct {
			kind  string
			items []ReviewTemplateItem
		}{{"competency", t.competencies}, {"kpi", t.kpis}} {
			for _, it := range set.items {
				order++
				if _, err := tx.Exec(ctx, `INSERT INTO hris.review_scores (id, property_id, review_id, item_kind, code, label, description, weight, target, sort_order)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10)`, id.New(), c.PropertyID, rid, set.kind, it.Code, it.Label, nullStr(it.Description),
					hris.Dec(it.Weight).String(), nullStr(it.Target), order); err != nil {
					return res, err
				}
			}
		}
		res.Created++
		due := c.SelfDue
		step := "Complete your self assessment"
		users := []uuid.UUID{}
		if u := userOf(ctx, tx, &e.ID); u != nil {
			users = append(users, *u)
		}
		if status == "manager_review" {
			due, step = c.ManagerDue, "Your manager reviews you"
			if u := userOf(ctx, tx, &sup.ID); u != nil {
				if err := m.notifyUsers(ctx, tx, c.PropertyID, []uuid.UUID{*u}, "hris.review_manager_due", "/ops/ess/team-reviews",
					map[string]any{"employeeName": e.FullName, "cycle": c.Name, "dueDate": dateOr(c.ManagerDue)}); err != nil {
					return res, err
				}
			}
		}
		if err := m.notifyUsers(ctx, tx, c.PropertyID, users, "hris.review_assigned", "/ops/ess/reviews", map[string]any{"cycle": c.Name, "step": step,
			"dueDate": dateOr(due)}); err != nil {
			return res, err
		}
	}
	if c.Status == "draft" && res.Created > 0 {
		if _, err := tx.Exec(ctx, `UPDATE hris.review_cycles SET status = 'in_progress', launched_at = now(), policy_version = $2, updated_by = $3 WHERE id = $1`,
			c.ID, ref.Version, actor(ctx)); err != nil {
			return res, err
		}
	}
	res.Cycle, err = m.cycle(ctx, tx, c.ID)
	return res, err
}

func dateOr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return ymd(*t)
}

func (m *Module) startCalibrationHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (ReviewCycle, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycle{}, err
	}
	c, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return c, err
	}
	if c.Status != "in_progress" {
		return c, errs.Conflict("cycle_not_running", "calibration starts on a running cycle")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.review_cycles SET status = 'calibration', calibration_started_at = now(), updated_by = $2 WHERE id = $1`, cid,
		actor(ctx)); err != nil {
		return c, err
	}
	out, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "start_calibration", EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: c.Code + " · " + c.Name, PropertyID: &c.PropertyID, Reason: req.Reason, Before: map[string]any{"status": c.Status},
		After: map[string]any{"status": "calibration"}})
}

func (m *Module) calibrationHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (CalibrationView, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return CalibrationView{}, err
	}
	c, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return CalibrationView{}, err
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, c.PropertyID, clock.Now())
	if err != nil {
		return CalibrationView{}, err
	}
	list, err := handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.cycle_id = $1 AND r.status IN ('submitted', 'calibrated', 'completed')
		ORDER BY coalesce(r.final_score, r.manager_score) DESC NULLS LAST, e.full_name`, cid))
	if err != nil {
		return CalibrationView{}, err
	}
	var ratings []string
	for _, rv := range list {
		switch {
		case rv.FinalRating != nil:
			ratings = append(ratings, *rv.FinalRating)
		case rv.RecommendedRating != nil:
			ratings = append(ratings, *rv.RecommendedRating)
		}
	}
	return CalibrationView{Cycle: c, Distribution: cfg.Distribution(ratings), Reviews: list, Bands: cfg.RatingBands}, nil
}

func (m *Module) closeCycleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewCycleCloseRequest) (ReviewCycle, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycle{}, err
	}
	c, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return c, err
	}
	if c.Status != "in_progress" && c.Status != "calibration" {
		return c, errs.Conflict("cycle_not_running", "only a running cycle can be closed")
	}
	var open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hris.performance_reviews WHERE cycle_id = $1 AND status IN ('self_assessment', 'manager_review',
		'submitted')`, cid).Scan(&open); err != nil {
		return c, err
	}
	if open > 0 && !req.Force {
		return c, errs.Conflict("reviews_open", itoa(open)+" review(s) are not calibrated yet: calibrate them or close with force (they are cancelled)")
	}
	if open > 0 && strings.TrimSpace(req.Reason) == "" {
		return c, handle.Invalid("reason", "required", "a reason is required to cancel the open reviews")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'cancelled', updated_by = $2 WHERE cycle_id = $1 AND status IN ('self_assessment',
		'manager_review', 'submitted')`, cid, actor(ctx)); err != nil {
		return c, err
	}
	n, err := m.completeReviews(ctx, tx, c)
	if err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.review_cycles SET status = 'completed', closed_at = now(), updated_by = $2 WHERE id = $1`, cid, actor(ctx)); err != nil {
		return c, err
	}
	out, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "close", EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: c.Code + " · " + c.Name, PropertyID: &c.PropertyID, Reason: req.Reason, Before: map[string]any{"status": c.Status},
		After: map[string]any{"status": "completed", "completed": n, "cancelled": open}})
}

// completeReviews completes the calibrated reviews of a cycle: employment
// history entry, hris.performance_review_completed, notification.
func (m *Module) completeReviews(ctx context.Context, tx pgx.Tx, c ReviewCycle) (int, error) {
	list, err := handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.cycle_id = $1 AND r.status = 'calibrated' ORDER BY e.full_name`, c.ID))
	if err != nil {
		return 0, err
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, c.PropertyID, clock.Now())
	if err != nil {
		return 0, err
	}
	for _, rv := range list {
		band, _ := cfg.BandByCode(deref(rv.FinalRating))
		label := band.Label
		if label == "" {
			label = deref(rv.FinalRating)
		}
		hid := id.New()
		reason := c.Name + ": " + label + " (" + deref(rv.FinalScore) + ")"
		if rv.Recommendation != "none" {
			reason += " · " + strings.ReplaceAll(rv.Recommendation, "_", " ")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.employment_history (id, property_id, employee_id, kind, effective_date, from_status, to_status, reference, reason,
			status, applied_at, created_by, updated_by) SELECT $1, property_id, id, 'performance_review', $3::date, employment_status, employment_status, $4, $5,
			'applied', now(), $6, $6 FROM hris.employees WHERE id = $2`, hid, rv.EmployeeID, ymd(c.PeriodEnd), c.Code, reason, actor(ctx)); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'completed', completed_at = now(), history_id = $2, updated_by = $3 WHERE id = $1`,
			rv.ID, hid, actor(ctx)); err != nil {
			return 0, err
		}
		if _, err := m.Events.Publish(ctx, tx, hris.EventPerformanceReviewCompleted, "hris.performance_review", &rv.ID, &c.PropertyID,
			hris.PerformanceReviewCompleted{ReviewID: rv.ID, CycleID: c.ID, CycleCode: c.Code, CycleType: c.CycleType, PeriodEnd: ymd(c.PeriodEnd),
				PropertyID: c.PropertyID, EmployeeID: rv.EmployeeID, EmployeeNo: rv.EmployeeNo, FinalScore: deref(rv.FinalScore),
				FinalRating: deref(rv.FinalRating), Recommendation: rv.Recommendation, IncreasePercent: rv.IncreasePercent, BonusMonths: rv.BonusMonths}); err != nil {
			return 0, err
		}
		if u := userOf(ctx, tx, &rv.EmployeeID); u != nil {
			if err := m.notifyUsers(ctx, tx, c.PropertyID, []uuid.UUID{*u}, "hris.review_completed", "/ops/ess/reviews", map[string]any{"cycle": c.Name,
				"rating": label, "score": deref(rv.FinalScore)}); err != nil {
				return 0, err
			}
		}
	}
	return len(list), nil
}

func (m *Module) cancelCycleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (ReviewCycle, error) {
	cid, err := handle.ID(r)
	if err != nil {
		return ReviewCycle{}, err
	}
	c, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return c, err
	}
	if c.Status == "completed" || c.Status == "cancelled" {
		return c, errs.Conflict("cycle_closed", "the review cycle is "+c.Status)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return c, handle.Invalid("reason", "required", "a reason is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'cancelled', updated_by = $2 WHERE cycle_id = $1 AND status <> 'completed'`, cid,
		actor(ctx)); err != nil {
		return c, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.review_cycles SET status = 'cancelled', closed_at = now(), notes = coalesce(notes || E'\n', '') || $2, updated_by = $3
		WHERE id = $1`, cid, "Cancelled: "+req.Reason, actor(ctx)); err != nil {
		return c, err
	}
	out, err := m.cycle(ctx, tx, cid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.review_cycle", EntityID: cid.String(),
		EntityLabel: c.Code + " · " + c.Name, PropertyID: &c.PropertyID, Reason: req.Reason, Before: map[string]any{"status": c.Status},
		After: map[string]any{"status": "cancelled"}})
}

// ── reviews ──────────────────────────────────────────────────────────────

func (m *Module) listReviewsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PerformanceReview, error) {
	cycle, err := uuidParam(r, "cycleId")
	if err != nil {
		return nil, err
	}
	emp, err := uuidParam(r, "employeeId")
	if err != nil {
		return nil, err
	}
	unit, err := uuidParam(r, "orgUnitId")
	if err != nil {
		return nil, err
	}
	return handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.property_id = $1 AND ($2::uuid IS NULL OR r.cycle_id = $2)
		AND ($3 = '' OR r.status = $3) AND ($4::uuid IS NULL OR r.employee_id = $4) AND ($5::uuid IS NULL OR e.org_unit_id = $5)
		AND ($6 = '' OR e.full_name ILIKE $6 OR e.employee_no ILIKE $6) ORDER BY c.period_end DESC, e.full_name LIMIT 1000`,
		handle.Property(ctx), cycle, filterParam(r, "status"), emp, unit, likeParam(r)))
}

func (m *Module) employeeReviewsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PerformanceReview, error) {
	eid, err := handle.ID(r)
	if err != nil {
		return nil, err
	}
	return handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.property_id = $1 AND r.employee_id = $2 AND r.status <> 'cancelled'
		ORDER BY c.period_end DESC`, handle.Property(ctx), eid))
}

// review loads a review (no property filter: ESS reads across properties
// under RLS).
func (m *Module) review(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PerformanceReview, error) {
	return getOne[PerformanceReview]("performance review")(tx.Query(ctx, reviewSelect+` WHERE r.id = $1`, rid))
}

// detail builds the review screen; hideManager hides the manager scores
// from the employee until the review is completed.
func (m *Module) detail(ctx context.Context, tx pgx.Tx, rv PerformanceReview, hideManager bool) (PerformanceReviewDetail, error) {
	out := PerformanceReviewDetail{Review: rv}
	var err error
	if out.Scores, err = handle.List[ReviewScore](tx.Query(ctx, scoreSelect+` WHERE review_id = $1 ORDER BY sort_order, code`, rv.ID)); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT inputs FROM hris.performance_reviews WHERE id = $1`, rv.ID).Scan(&out.Inputs); err != nil {
		return out, err
	}
	if out.Inputs == nil {
		out.Inputs = []hris.ReviewInput{}
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return out, err
	}
	out.Bands, out.ScoreScale = cfg.RatingBands, cfg.ScoreScale
	if out.Previous, err = handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.employee_id = $1 AND r.id <> $2 AND r.status = 'completed'
		ORDER BY c.period_end DESC LIMIT 5`, rv.EmployeeID, rv.ID)); err != nil {
		return out, err
	}
	if hideManager && rv.Status != "completed" {
		out.Review.ManagerScore, out.Review.ManagerComment, out.Review.Strengths, out.Review.Improvements = nil, nil, nil, nil
		out.Review.RecommendedRating, out.Review.FinalScore, out.Review.FinalRating, out.Review.CalibrationNote = nil, nil, nil, nil
		out.Review.IncreasePercent, out.Review.BonusMonths, out.Review.Recommendation = nil, nil, "none"
		for i := range out.Scores {
			out.Scores[i].ManagerScore, out.Scores[i].ManagerComment = nil, nil
		}
	}
	if hideManager {
		// Pay consequences are HR's: the employee sees the rating only.
		out.Review.IncreasePercent, out.Review.BonusMonths = nil, nil
	}
	return out, nil
}

func (m *Module) reviewDetailHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := m.review(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	out, err := m.detail(ctx, tx, rv, false)
	if err != nil {
		return out, err
	}
	open := rv.CycleStatus == "in_progress" || rv.CycleStatus == "calibration"
	out.CanEditManager = open && rv.Status == "manager_review" && can(ctx, "hris.performance_review.manage", rv.PropertyID)
	return out, nil
}

// lockReview loads a review for update.
func lockReview(ctx context.Context, tx pgx.Tx, rid uuid.UUID) (PerformanceReview, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM hris.performance_reviews WHERE id = $1 FOR UPDATE`, rid); err != nil {
		return PerformanceReview{}, err
	}
	return getOne[PerformanceReview]("performance review")(tx.Query(ctx, reviewSelect+` WHERE r.id = $1`, rid))
}

// saveScores writes the self or manager scores of items.
func saveScores(ctx context.Context, tx pgx.Tx, rid uuid.UUID, scale int, manager bool, in []ReviewScoreInput) error {
	for _, s := range in {
		var score *string
		if v := strings.TrimSpace(s.Score); v != "" {
			d, err := decimal.NewFromString(v)
			if err != nil || !hris.ValidScore(d, scale) {
				return handle.Invalid("scores", "invalid", s.Code+": a score from 1 to "+itoa(scale))
			}
			x := d.String()
			score = &x
		}
		var tag interface{ RowsAffected() int64 }
		var err error
		if manager {
			tag, err = tx.Exec(ctx, `UPDATE hris.review_scores SET manager_score = $4::numeric, manager_comment = $5, actual = coalesce($6, actual)
				WHERE review_id = $1 AND item_kind = $2 AND code = $3`, rid, s.ItemKind, s.Code, score, nullStr(s.Comment), nullStr(s.Actual))
		} else {
			tag, err = tx.Exec(ctx, `UPDATE hris.review_scores SET self_score = $4::numeric, self_comment = $5, actual = coalesce($6, actual)
				WHERE review_id = $1 AND item_kind = $2 AND code = $3`, rid, s.ItemKind, s.Code, score, nullStr(s.Comment), nullStr(s.Actual))
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return handle.Invalid("scores", "unknown", "unknown item "+s.ItemKind+" "+s.Code)
		}
	}
	return nil
}

// reviewScore computes the self or manager score of a review (nil, false
// when an item is not scored).
func reviewScore(ctx context.Context, tx pgx.Tx, rid uuid.UUID, manager bool) (*decimal.Decimal, bool, error) {
	col := "self_score"
	if manager {
		col = "manager_score"
	}
	rows, err := tx.Query(ctx, `SELECT item_kind, weight, `+col+` FROM hris.review_scores WHERE review_id = $1`, rid)
	if err != nil {
		return nil, false, err
	}
	type item struct {
		kind   string
		weight decimal.Decimal
		score  *decimal.Decimal
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
		var it item
		return it, row.Scan(&it.kind, &it.weight, &it.score)
	})
	if err != nil {
		return nil, false, err
	}
	var comp, kpi []hris.ScoreItem
	for _, it := range list {
		si := hris.ScoreItem{Weight: it.weight, Score: it.score}
		if it.kind == "competency" {
			comp = append(comp, si)
		} else {
			kpi = append(kpi, si)
		}
	}
	complete := true
	part := func(items []hris.ScoreItem) *decimal.Decimal {
		if len(items) == 0 {
			return nil
		}
		v, ok := hris.WeightedScore(items)
		if !ok {
			complete = false
		}
		return &v
	}
	c, k := part(comp), part(kpi)
	var weight decimal.Decimal
	if err := tx.QueryRow(ctx, `SELECT competency_weight FROM hris.performance_reviews WHERE id = $1`, rid).Scan(&weight); err != nil {
		return nil, false, err
	}
	return hris.CombineScores(c, k, weight), complete, nil
}

// saveManager stores the manager part of a review.
func (m *Module) saveManager(ctx context.Context, tx pgx.Tx, rv PerformanceReview, in ReviewManagerInput, hr bool) error {
	if rv.CycleStatus != "in_progress" && rv.CycleStatus != "calibration" {
		return errs.Conflict("cycle_not_running", "the review cycle is "+rv.CycleStatus)
	}
	if rv.Status != "manager_review" {
		return errs.Conflict("review_not_with_manager", "the review is "+strings.ReplaceAll(rv.Status, "_", " "))
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return err
	}
	if err := saveScores(ctx, tx, rv.ID, cfg.ScoreScale, true, in.Scores); err != nil {
		return err
	}
	if in.Recommendation != nil && !oneOf(Recommendations, *in.Recommendation) {
		return enumErr("recommendation", Recommendations)
	}
	reviewer := rv.ReviewerID
	if in.ReviewerID != nil {
		if !hr {
			return errs.Forbidden("only HR reassigns the reviewer")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.employees WHERE id = $1 AND property_id = $2 AND status = 'active')`, *in.ReviewerID,
			rv.PropertyID).Scan(&ok); err != nil || !ok || *in.ReviewerID == rv.EmployeeID {
			return handle.Invalid("reviewerId", "invalid", "an active employee other than the reviewed one")
		}
		reviewer = in.ReviewerID
	}
	score, _, err := reviewScore(ctx, tx, rv.ID, true)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE hris.performance_reviews SET manager_score = $2::numeric, manager_comment = coalesce($3, manager_comment),
		strengths = coalesce($4, strengths), improvements = coalesce($5, improvements), goals = coalesce($6, goals),
		recommendation = coalesce($7, recommendation), reviewer_id = $8, updated_by = $9 WHERE id = $1`, rv.ID, decStr(score), in.Comment, in.Strengths,
		in.Improvements, in.Goals, in.Recommendation, reviewer, actor(ctx))
	return err
}

// submitManager submits the manager review: every item scored; the
// recommended score blends the self score (Performance Review
// Configuration) and gives the recommended rating.
func (m *Module) submitManager(ctx context.Context, tx pgx.Tx, rv PerformanceReview, note string) error {
	if rv.CycleStatus != "in_progress" && rv.CycleStatus != "calibration" {
		return errs.Conflict("cycle_not_running", "the review cycle is "+rv.CycleStatus)
	}
	if rv.Status != "manager_review" {
		return errs.Conflict("review_not_with_manager", "the review is "+strings.ReplaceAll(rv.Status, "_", " "))
	}
	score, complete, err := reviewScore(ctx, tx, rv.ID, true)
	if err != nil {
		return err
	}
	if !complete || score == nil {
		return errs.Conflict("scores_missing", "score every competency and KPI before submitting")
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return err
	}
	var self *decimal.Decimal
	if rv.SelfScore != nil {
		v := hris.Dec(*rv.SelfScore)
		self = &v
	}
	rec := cfg.RecommendedScore(score, self)
	band := cfg.Band(*rec)
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'submitted', manager_score = $2::numeric, recommended_rating = $3,
		manager_submitted_at = now(), manager_submitted_by = $4, updated_by = $4 WHERE id = $1`, rv.ID, score.String(), band.Code, actor(ctx)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit_manager_review", EntityType: "hris.performance_review",
		EntityID: rv.ID.String(), EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Reason: note,
		Before: map[string]any{"status": rv.Status}, After: map[string]any{"status": "submitted", "managerScore": score.String(),
			"recommendedRating": band.Code}}); err != nil {
		return err
	}
	return nil
}

func (m *Module) hrManagerSaveHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewManagerInput) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := lockReview(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	if err := m.saveManager(ctx, tx, rv, req, true); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rid)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "manager_review", EntityType: "hris.performance_review", EntityID: rid.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Before: map[string]any{"managerScore": rv.ManagerScore},
		After: map[string]any{"managerScore": after.ManagerScore, "reviewerId": after.ReviewerID}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.detail(ctx, tx, after, false)
}

func (m *Module) hrSubmitHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := lockReview(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	if err := m.submitManager(ctx, tx, rv, req.Reason); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rid)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.detail(ctx, tx, after, false)
}

func (m *Module) calibrateHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewCalibrateRequest) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := lockReview(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	if rv.CycleStatus != "in_progress" && rv.CycleStatus != "calibration" {
		return PerformanceReviewDetail{}, errs.Conflict("cycle_not_running", "the review cycle is "+rv.CycleStatus)
	}
	if rv.Status != "submitted" && rv.Status != "calibrated" {
		return PerformanceReviewDetail{}, errs.Conflict("review_not_submitted", "the manager has not submitted the review")
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	var self *decimal.Decimal
	if rv.SelfScore != nil {
		v := hris.Dec(*rv.SelfScore)
		self = &v
	}
	mgr := hris.Dec(deref(rv.ManagerScore))
	score := *cfg.RecommendedScore(&mgr, self)
	if s := strings.TrimSpace(req.FinalScore); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil || !hris.ValidScore(v, cfg.ScoreScale) {
			return PerformanceReviewDetail{}, handle.Invalid("finalScore", "invalid", "a score from 1 to "+itoa(cfg.ScoreScale))
		}
		score = v
	}
	band := cfg.Band(score)
	rating := band.Code
	if code := strings.TrimSpace(req.FinalRating); code != "" {
		b, ok := cfg.BandByCode(code)
		if !ok {
			return PerformanceReviewDetail{}, handle.Invalid("finalRating", "invalid", "a rating band of the Performance Review Configuration")
		}
		if b.Code != band.Code && strings.TrimSpace(req.Note) == "" {
			return PerformanceReviewDetail{}, handle.Invalid("note", "required", "explain why the rating differs from the band of the score")
		}
		band, rating = b, b.Code
	}
	inc, bonus := band.IncreasePercent, band.BonusMonths
	if s := strings.TrimSpace(req.IncreasePercent); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(100)) {
			return PerformanceReviewDetail{}, handle.Invalid("increasePercent", "invalid", "0 to 100")
		}
		inc = v.String()
	}
	if s := strings.TrimSpace(req.BonusMonths); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(24)) {
			return PerformanceReviewDetail{}, handle.Invalid("bonusMonths", "invalid", "0 to 24")
		}
		bonus = v.String()
	}
	rec := strings.TrimSpace(req.Recommendation)
	switch {
	case rec != "":
		if !oneOf(Recommendations, rec) {
			return PerformanceReviewDetail{}, enumErr("recommendation", Recommendations)
		}
	case rv.Recommendation != "none":
		rec = rv.Recommendation
	case rv.CycleType == "probation":
		rec = "extend_probation"
		if cfg.AtLeast(rating, cfg.ProbationPassBand) {
			rec = "confirm_employment"
		}
	case hris.Dec(inc).IsPositive():
		rec = "salary_increase"
	default:
		rec = "improvement_plan"
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'calibrated', final_score = $2::numeric, final_rating = $3, recommendation = $4,
		increase_percent = $5::numeric, bonus_months = $6::numeric, calibration_note = $7, calibrated_at = now(), calibrated_by = $8, updated_by = $8
		WHERE id = $1`, rid, score.String(), rating, rec, nullStr(inc), nullStr(bonus), nullStr(req.Note), actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "calibrate", EntityType: "hris.performance_review", EntityID: rid.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Reason: req.Note,
		Before: map[string]any{"status": rv.Status, "recommendedRating": rv.RecommendedRating, "finalRating": rv.FinalRating},
		After:  map[string]any{"status": "calibrated", "finalScore": score.String(), "finalRating": rating, "recommendation": rec}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rid)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.detail(ctx, tx, after, false)
}

func (m *Module) reopenHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := lockReview(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	if rv.CycleStatus != "in_progress" && rv.CycleStatus != "calibration" {
		return PerformanceReviewDetail{}, errs.Conflict("cycle_not_running", "the review cycle is "+rv.CycleStatus)
	}
	if rv.Status != "submitted" && rv.Status != "calibrated" {
		return PerformanceReviewDetail{}, errs.Conflict("review_not_submitted", "only a submitted or calibrated review can be sent back")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return PerformanceReviewDetail{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'manager_review', recommended_rating = NULL, final_score = NULL, final_rating = NULL,
		calibrated_at = NULL, calibrated_by = NULL, manager_submitted_at = NULL, updated_by = $2 WHERE id = $1`, rid, actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "reopen", EntityType: "hris.performance_review", EntityID: rid.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Reason: req.Reason, Before: map[string]any{"status": rv.Status},
		After: map[string]any{"status": "manager_review"}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if u := userOf(ctx, tx, rv.ReviewerID); u != nil {
		if err := m.notifyUsers(ctx, tx, rv.PropertyID, []uuid.UUID{*u}, "hris.review_manager_due", "/ops/ess/team-reviews",
			map[string]any{"employeeName": rv.EmployeeName, "cycle": rv.CycleName, "dueDate": dateOr(rv.ManagerDue)}); err != nil {
			return PerformanceReviewDetail{}, err
		}
	}
	after, err := m.review(ctx, tx, rid)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.detail(ctx, tx, after, false)
}

func (m *Module) promoteHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewPromoteRequest) (PerformanceReviewDetail, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	rv, err := lockReview(ctx, tx, rid)
	if err != nil || rv.PropertyID != handle.Property(ctx) {
		return PerformanceReviewDetail{}, errs.NotFound("performance review")
	}
	if rv.Status != "calibrated" && rv.Status != "completed" {
		return PerformanceReviewDetail{}, errs.Conflict("review_not_calibrated", "promote from a calibrated or completed review")
	}
	if rv.EmploymentChangeID != nil {
		return PerformanceReviewDetail{}, errs.Conflict("already_promoted", "the review already led to an employment change")
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if !cfg.AtLeast(deref(rv.FinalRating), cfg.PromotionMinBand) {
		return PerformanceReviewDetail{}, errs.Conflict("rating_too_low", "a promotion needs at least the rating "+cfg.PromotionMinBand)
	}
	if m.Onboarding == nil {
		return PerformanceReviewDetail{}, errs.Unavailable("Core HR is not wired")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "Performance review " + rv.CycleCode + ": " + deref(rv.FinalRating)
	}
	hid, err := m.Onboarding.ChangeEmployment(ctx, tx, rv.EmployeeID, hris.EmploymentChangeRequest{Kind: "promotion", EffectiveDate: req.EffectiveDate,
		PositionID: req.PositionID, GradeID: req.GradeID, Reason: reason})
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET employment_change_id = $2, recommendation = 'promotion', updated_by = $3 WHERE id = $1`,
		rid, hid, actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "promote", EntityType: "hris.performance_review", EntityID: rid.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Reason: reason,
		After: map[string]any{"employmentChangeId": hid, "positionId": req.PositionID, "gradeId": req.GradeID, "effectiveDate": req.EffectiveDate}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rid)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.detail(ctx, tx, after, false)
}
