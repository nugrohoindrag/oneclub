package hris

// Recruitment (EP-03) and Performance Review (EP-05) — the public part the
// other areas and internal/app build on:
//
//   - Recruitment Configuration and Performance Review Configuration (HR
//     Configuration category, versioned with effective date, §6 #18)
//   - the scoring engine of reviews and interview scorecards (weighted
//     scores, rating bands, calibration guide)
//   - Onboarding, the Core HR API the hire of an application calls to
//     create the employee and the contract (and the promotion of a review
//     result through Core HR :promote), wired by internal/app
//   - the operational review inputs registry (FR-PRF-HR-03) and the latest
//     review result for salary increase / bonus (FR-PRF-HR-04, EP-09)
//   - the event hris.performance_review_completed.
//
// The implementation is hris/talent.

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/rules"
)

// Policy codes of recruitment and performance review.
const (
	RecruitmentConfigurationCode = "hris.recruitment_configuration"
	PerformanceConfigurationCode = "hris.performance_configuration"
)

// EventPerformanceReviewCompleted is published when a review cycle is
// closed, once per completed review (payroll reads the increase / bonus
// basis, FR-PRF-HR-04).
const EventPerformanceReviewCompleted = "hris.performance_review_completed"

// PerformanceReviewCompleted is the payload of
// hris.performance_review_completed.
type PerformanceReviewCompleted struct {
	ReviewID        uuid.UUID `json:"reviewId"`
	CycleID         uuid.UUID `json:"cycleId"`
	CycleCode       string    `json:"cycleCode"`
	CycleType       string    `json:"cycleType" enum:"annual,semester,probation"`
	PeriodEnd       string    `json:"periodEnd"`
	PropertyID      uuid.UUID `json:"propertyId"`
	EmployeeID      uuid.UUID `json:"employeeId"`
	EmployeeNo      string    `json:"employeeNo"`
	FinalScore      string    `json:"finalScore"`
	FinalRating     string    `json:"finalRating" doc:"Rating band code of the Performance Review Configuration"`
	Recommendation  string    `json:"recommendation"`
	IncreasePercent *string   `json:"increasePercent" doc:"Recommended salary increase (percent of the base salary)"`
	BonusMonths     *string   `json:"bonusMonths" doc:"Recommended performance bonus in months of wage"`
}

// ── Recruitment Configuration ───────────────────────────────────────────

// ScoreCriterion is one criterion of an interview scorecard.
type ScoreCriterion struct {
	Code   string `json:"code"`
	Label  string `json:"label"`
	Weight int    `json:"weight" doc:"Relative weight (> 0)"`
}

// OnboardingItem is one entry of the onboarding checklist of a hire.
type OnboardingItem struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	DueDays int    `json:"dueDays" doc:"Due N days after the join date (negative = before)"`
}

// RecruitmentConfiguration is the Recruitment Configuration (Settings → HR
// Configuration).
type RecruitmentConfiguration struct {
	RequisitionPrefix   string           `json:"requisitionPrefix" doc:"Job requisition numbers PREFIX-YYYY-NNNNN"`
	ApplicationPrefix   string           `json:"applicationPrefix"`
	OfferPrefix         string           `json:"offerPrefix"`
	ScoreScale          int              `json:"scoreScale" doc:"Interview scores from 1 to N"`
	InterviewCriteria   []ScoreCriterion `json:"interviewCriteria" doc:"Criteria of the interview scorecard"`
	PassScore           string           `json:"passScore" doc:"Overall interview score from which the result defaults to pass"`
	OfferValidityDays   int              `json:"offerValidityDays" doc:"An offer not answered within N days after it is sent expires"`
	OnboardingChecklist []OnboardingItem `json:"onboardingChecklist"`
	CreateLoginOnHire   bool             `json:"createLoginOnHire" doc:"Create the Employee Self Service login of a hire that has an e-mail"`
	CareersPage         bool             `json:"careersPage" doc:"Publish public requisitions on the website careers page"`
	CVMaxMB             int              `json:"cvMaxMb" doc:"Largest CV accepted from the website (PDF, DOC/DOCX, JPEG, PNG)"`
	// Applicants who were not hired are erased after the HR Configuration
	// retention (applicantMonths) when they consented to the talent pool,
	// otherwise after this many days (UU PDP, PRD P5 §16 #10).
	NoConsentRetentionDays int    `json:"noConsentRetentionDays" doc:"Applicants without talent pool consent are erased N days after their last application closed"`
	NotifyCandidates       bool   `json:"notifyCandidates" doc:"E-mail candidates (application received, interview, offer, decision)"`
	OfferLetterTemplate    string `json:"offerLetterTemplate" doc:"Code of the HR letter template (type offer_letter) of the offer letter; empty = built-in letter"`
}

// NewRecruitmentConfiguration returns the defaults (proposal; the PRD is
// silent beyond §16 #10).
func NewRecruitmentConfiguration() RecruitmentConfiguration {
	return RecruitmentConfiguration{
		RequisitionPrefix: "REQ", ApplicationPrefix: "APP", OfferPrefix: "OFR", ScoreScale: 5, PassScore: "3",
		InterviewCriteria: []ScoreCriterion{
			{Code: "job_knowledge", Label: "Job knowledge & skills", Weight: 3},
			{Code: "service_attitude", Label: "Service attitude & hospitality", Weight: 3},
			{Code: "communication", Label: "Communication", Weight: 2},
			{Code: "teamwork", Label: "Teamwork", Weight: 1},
			{Code: "culture_fit", Label: "Integrity & culture fit", Weight: 1},
		},
		OfferValidityDays: 7,
		OnboardingChecklist: []OnboardingItem{
			{Code: "sign_contract", Label: "Employment contract signed", DueDays: 0},
			{Code: "documents", Label: "KTP, NPWP, KK, diploma and bank account collected", DueDays: 0},
			{Code: "bpjs", Label: "BPJS Kesehatan & Ketenagakerjaan registered", DueDays: 7},
			{Code: "login", Label: "Employee Self Service login and roles created", DueDays: 0},
			{Code: "uniform_id", Label: "Uniform, ID card and locker issued", DueDays: 0},
			{Code: "biometric", Label: "Attendance enrolment (device / mobile) completed", DueDays: 1},
			{Code: "orientation", Label: "Orientation & safety induction", DueDays: 3},
			{Code: "certification", Label: "Mandatory certifications of the position on file", DueDays: 14},
		},
		CreateLoginOnHire: true, CareersPage: true, CVMaxMB: 5, NoConsentRetentionDays: 30, NotifyCandidates: true,
	}
}

// ── Performance Review Configuration ───────────────────────────────────

// RatingBand is a rating of the final score with its pay consequence and
// the calibration guide (largest share of employees in the band).
type RatingBand struct {
	Code            string `json:"code"`
	Label           string `json:"label"`
	LabelID         string `json:"labelId"`
	MinScore        string `json:"minScore" doc:"Lowest final score of the band"`
	IncreasePercent string `json:"increasePercent" doc:"Recommended salary increase (percent)"`
	BonusMonths     string `json:"bonusMonths" doc:"Recommended performance bonus (months of wage)"`
	MaxSharePercent int    `json:"maxSharePercent" doc:"Calibration guide: at most N% of the reviews of a cycle (0 = no limit)"`
}

// PerformanceConfiguration is the Performance Review Configuration
// (Settings → HR Configuration).
type PerformanceConfiguration struct {
	ScoreScale            int          `json:"scoreScale" doc:"Scores from 1 to N"`
	RatingBands           []RatingBand `json:"ratingBands" doc:"From the highest band to the lowest"`
	RequireSelfAssessment bool         `json:"requireSelfAssessment" doc:"Employees complete a self assessment before the manager review"`
	SelfWeightPercent     int          `json:"selfWeightPercent" doc:"Weight of the self score in the recommended score (0 = manager score only)"`
	ProbationPassBand     string       `json:"probationPassBand" doc:"Lowest band confirming the employment after probation"`
	PromotionMinBand      string       `json:"promotionMinBand" doc:"Lowest band allowing a promotion from a review"`
	ReminderDays          []int        `json:"reminderDays" doc:"Remind employees and managers N days before their due date"`
}

// NewPerformanceConfiguration returns the defaults (proposal; the PRD is
// silent on scale and bands).
func NewPerformanceConfiguration() PerformanceConfiguration {
	return PerformanceConfiguration{
		ScoreScale: 5,
		RatingBands: []RatingBand{
			{Code: "outstanding", Label: "Outstanding", LabelID: "Istimewa", MinScore: "4.5", IncreasePercent: "10", BonusMonths: "2", MaxSharePercent: 10},
			{Code: "exceeds", Label: "Exceeds Expectations", LabelID: "Melampaui Harapan", MinScore: "3.75", IncreasePercent: "7", BonusMonths: "1.5", MaxSharePercent: 25},
			{Code: "meets", Label: "Meets Expectations", LabelID: "Sesuai Harapan", MinScore: "3", IncreasePercent: "5", BonusMonths: "1"},
			{Code: "needs_improvement", Label: "Needs Improvement", LabelID: "Perlu Perbaikan", MinScore: "2", IncreasePercent: "0", BonusMonths: "0.5"},
			{Code: "unsatisfactory", Label: "Unsatisfactory", LabelID: "Tidak Memuaskan", MinScore: "0", IncreasePercent: "0", BonusMonths: "0"},
		},
		RequireSelfAssessment: true, SelfWeightPercent: 0, ProbationPassBand: "meets", PromotionMinBand: "exceeds", ReminderDays: []int{3},
	}
}

func init() {
	for _, c := range []string{RecruitmentConfigurationCode, PerformanceConfigurationCode} {
		if !slices.Contains(PolicyCodes, c) {
			PolicyCodes = append(PolicyCodes, c)
		}
	}
	rules.RegisterPolicy(rules.PolicyDef{Code: RecruitmentConfigurationCode, Category: CategoryHRConfiguration, Name: "Recruitment Configuration",
		Description: "Requisition, application and offer numbering, interview scorecard, offer validity, onboarding checklist, careers page, " +
			"CV upload and the retention of applicants who were not hired", Default: NewRecruitmentConfiguration()})
	rules.RegisterPolicy(rules.PolicyDef{Code: PerformanceConfigurationCode, Category: CategoryHRConfiguration, Name: "Performance Review Configuration",
		Description: "Score scale, rating bands with salary increase and bonus, calibration guide, self assessment and probation / promotion thresholds",
		Default:     NewPerformanceConfiguration()})
}

// LoadRecruitmentConfiguration returns the Recruitment Configuration in force.
func LoadRecruitmentConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (RecruitmentConfiguration, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, RecruitmentConfigurationCode, &property, at, NewRecruitmentConfiguration())
}

// LoadPerformanceConfiguration returns the Performance Review Configuration
// in force.
func LoadPerformanceConfiguration(ctx context.Context, q dbtx.Querier, property uuid.UUID, at time.Time) (PerformanceConfiguration, rules.PolicyRef, error) {
	return rules.Policy(ctx, q, PerformanceConfigurationCode, &property, at, NewPerformanceConfiguration())
}

// ── scoring engine ──────────────────────────────────────────────────────

// ScoreItem is one weighted score (a review competency / KPI or an
// interview criterion). Score nil = not scored yet.
type ScoreItem struct {
	Weight decimal.Decimal
	Score  *decimal.Decimal
}

// WeightedScore is Σ weight × score ÷ Σ weight of the items, rounded to 2
// decimals; complete is false when an item has no score (the result then
// covers the scored items only). No scored item gives 0, false.
func WeightedScore(items []ScoreItem) (score decimal.Decimal, complete bool) {
	complete = len(items) > 0
	var sum, weights decimal.Decimal
	for _, it := range items {
		if it.Score == nil {
			complete = false
			continue
		}
		w := it.Weight
		if !w.IsPositive() {
			w = decimal.NewFromInt(1)
		}
		sum = sum.Add(it.Score.Mul(w))
		weights = weights.Add(w)
	}
	if weights.IsZero() {
		return decimal.Zero, false
	}
	return sum.Div(weights).Round(2), complete
}

// CombineScores blends the competency and KPI scores of a review by the
// competency weight (percent); a part without items takes the whole weight
// of the other.
func CombineScores(competency, kpi *decimal.Decimal, competencyWeight decimal.Decimal) *decimal.Decimal {
	switch {
	case competency == nil && kpi == nil:
		return nil
	case kpi == nil:
		v := competency.Round(2)
		return &v
	case competency == nil:
		v := kpi.Round(2)
		return &v
	}
	w := decimal.Min(decimal.Max(competencyWeight, decimal.Zero), hundred)
	v := competency.Mul(w).Add(kpi.Mul(hundred.Sub(w))).Div(hundred).Round(2)
	return &v
}

// RecommendedScore blends the manager score with the self score by
// SelfWeightPercent.
func (c PerformanceConfiguration) RecommendedScore(manager, self *decimal.Decimal) *decimal.Decimal {
	if manager == nil {
		return nil
	}
	w := decimal.NewFromInt(int64(min(max(c.SelfWeightPercent, 0), 100)))
	if self == nil || w.IsZero() {
		v := manager.Round(2)
		return &v
	}
	v := manager.Mul(hundred.Sub(w)).Add(self.Mul(w)).Div(hundred).Round(2)
	return &v
}

// bands returns the rating bands from the highest minimum score down.
func (c PerformanceConfiguration) bands() []RatingBand {
	out := slices.Clone(c.RatingBands)
	sort.SliceStable(out, func(i, j int) bool { return Dec(out[i].MinScore).GreaterThan(Dec(out[j].MinScore)) })
	return out
}

// Band returns the rating band of a score (the highest band whose minimum
// the score reaches; the lowest band otherwise).
func (c PerformanceConfiguration) Band(score decimal.Decimal) RatingBand {
	bands := c.bands()
	if len(bands) == 0 {
		return RatingBand{}
	}
	for _, b := range bands {
		if score.GreaterThanOrEqual(Dec(b.MinScore)) {
			return b
		}
	}
	return bands[len(bands)-1]
}

// BandByCode returns a band by its code.
func (c PerformanceConfiguration) BandByCode(code string) (RatingBand, bool) {
	for _, b := range c.RatingBands {
		if b.Code == code {
			return b, true
		}
	}
	return RatingBand{}, false
}

// AtLeast reports whether band code a ranks at or above band code b (an
// unknown b is no threshold).
func (c PerformanceConfiguration) AtLeast(a, b string) bool {
	bb, ok := c.BandByCode(b)
	if !ok {
		return true
	}
	ab, ok := c.BandByCode(a)
	if !ok {
		return false
	}
	return Dec(ab.MinScore).GreaterThanOrEqual(Dec(bb.MinScore))
}

// BandShare is the calibration guide of one band in a cycle.
type BandShare struct {
	Code            string `json:"code"`
	Label           string `json:"label"`
	Count           int    `json:"count"`
	SharePercent    string `json:"sharePercent"`
	MaxSharePercent int    `json:"maxSharePercent"`
	Over            bool   `json:"over" doc:"More reviews in the band than the calibration guide allows"`
}

// Distribution counts ratings per band (highest first) against the
// calibration guide.
func (c PerformanceConfiguration) Distribution(ratings []string) []BandShare {
	total := decimal.NewFromInt(int64(len(ratings)))
	out := []BandShare{}
	for _, b := range c.bands() {
		n := 0
		for _, r := range ratings {
			if r == b.Code {
				n++
			}
		}
		share := decimal.Zero
		if total.IsPositive() {
			share = decimal.NewFromInt(int64(n)).Mul(hundred).Div(total).Round(1)
		}
		out = append(out, BandShare{Code: b.Code, Label: b.Label, Count: n, SharePercent: share.String(), MaxSharePercent: b.MaxSharePercent,
			Over: b.MaxSharePercent > 0 && share.GreaterThan(decimal.NewFromInt(int64(b.MaxSharePercent)))})
	}
	return out
}

// ValidScore checks a score against the scale (1 … scale, two decimals).
func ValidScore(v decimal.Decimal, scale int) bool {
	return v.GreaterThanOrEqual(decimal.NewFromInt(1)) && v.LessThanOrEqual(decimal.NewFromInt(int64(scale))) && v.Exponent() >= -2
}

// ── onboarding through Core HR ──────────────────────────────────────────

// HireRequest creates the employee and the contract of a hire (FR-RCT-04).
type HireRequest struct {
	PropertyID      uuid.UUID
	EmployeeNo      string // "" = next number
	FullName        string
	Gender          string
	BirthDate       *time.Time
	Phone           string
	PersonalEmail   string
	WorkEmail       string
	Address         string
	City            string
	OrgUnitID       *uuid.UUID
	PositionID      *uuid.UUID
	GradeID         *uuid.UUID
	SupervisorID    *uuid.UUID
	JobTitle        string
	WorkerCategory  string
	JoinDate        time.Time
	ContractType    string // pkwt | pkwtt
	EndDate         *time.Time
	ProbationMonths int
	BaseSalary      string
	Allowances      []Allowance
	WorkWeekDays    int
	Reference       string // application / offer number
	// CreateLogin provisions the Employee Self Service login (FR-HR-04)
	// with LoginEmail and RoleCodes (default: the self-service role).
	CreateLogin bool
	LoginEmail  string
	RoleCodes   []string
}

// HireResult is the employee created by a hire.
type HireResult struct {
	EmployeeID     uuid.UUID  `json:"employeeId"`
	EmployeeNo     string     `json:"employeeNo"`
	ContractID     uuid.UUID  `json:"contractId"`
	ContractNumber string     `json:"contractNumber"`
	UserID         *uuid.UUID `json:"userId"`
}

// EmploymentChangeRequest is a promotion / transfer through Core HR.
type EmploymentChangeRequest struct {
	Kind          string // promotion | demotion | transfer | rotation
	EffectiveDate string
	OrgUnitID     *uuid.UUID
	PositionID    *uuid.UUID
	GradeID       *uuid.UUID
	Reason        string
}

// Onboarding is the Core HR API of recruitment and performance review,
// wired by internal/app (Core HR creates the employee — publishing
// hris.employee_hired — the contract and the login, and applies employment
// changes).
type Onboarding interface {
	Hire(ctx context.Context, tx pgx.Tx, r HireRequest) (HireResult, error)
	ChangeEmployment(ctx context.Context, tx pgx.Tx, employeeID uuid.UUID, r EmploymentChangeRequest) (uuid.UUID, error)
}

// ── operational review inputs (FR-PRF-HR-03) ─────────────────────────────

// ReviewInput is one operational figure shown to the reviewer (attendance,
// sales target achievement, training, …).
type ReviewInput struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit" enum:"percent,count,days,hours,points,idr,text"`
	Note  string `json:"note,omitempty"`
}

// ReviewInputProvider returns the inputs of an employee for a review period
// (nil = nothing to show).
type ReviewInputProvider func(ctx context.Context, q dbtx.Querier, e Employee, from, to time.Time) ([]ReviewInput, error)

var (
	inputMu   sync.RWMutex
	inputKeys []string
	inputs    = map[string]ReviewInputProvider{}
)

// RegisterReviewInput adds (or replaces) an operational input provider; the
// time & attendance area registers attendance, internal/app the sales
// target achievement of CRM.
func RegisterReviewInput(key string, p ReviewInputProvider) {
	inputMu.Lock()
	defer inputMu.Unlock()
	if _, ok := inputs[key]; !ok {
		inputKeys = append(inputKeys, key)
	}
	inputs[key] = p
}

// ReviewInputs collects the inputs of every provider in registration order.
func ReviewInputs(ctx context.Context, q dbtx.Querier, e Employee, from, to time.Time) ([]ReviewInput, error) {
	inputMu.RLock()
	keys := slices.Clone(inputKeys)
	provs := make([]ReviewInputProvider, 0, len(keys))
	for _, k := range keys {
		provs = append(provs, inputs[k])
	}
	inputMu.RUnlock()
	out := []ReviewInput{}
	for _, p := range provs {
		list, err := p(ctx, q, e, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

// ── review results (FR-PRF-HR-04) ────────────────────────────────────────

// ReviewResult is a completed performance review: the basis of salary
// increase and bonus (payroll, EP-09) and of promotion / confirmation.
type ReviewResult struct {
	ReviewID        uuid.UUID `json:"reviewId" db:"review_id"`
	CycleID         uuid.UUID `json:"cycleId" db:"cycle_id"`
	CycleCode       string    `json:"cycleCode" db:"cycle_code"`
	CycleName       string    `json:"cycleName" db:"cycle_name"`
	CycleType       string    `json:"cycleType" db:"cycle_type"`
	PeriodEnd       time.Time `json:"periodEnd" db:"period_end"`
	FinalScore      string    `json:"finalScore" db:"final_score"`
	FinalRating     string    `json:"finalRating" db:"final_rating"`
	Recommendation  string    `json:"recommendation" db:"recommendation"`
	IncreasePercent *string   `json:"increasePercent" db:"increase_percent"`
	BonusMonths     *string   `json:"bonusMonths" db:"bonus_months"`
	CompletedAt     time.Time `json:"completedAt" db:"completed_at"`
}

// LatestReviewResult returns the latest completed review of an employee
// (nil when none).
func LatestReviewResult(ctx context.Context, q dbtx.Querier, employeeID uuid.UUID) (*ReviewResult, error) {
	rows, err := q.Query(ctx, `SELECT r.id AS review_id, c.id AS cycle_id, c.code AS cycle_code, c.name AS cycle_name, c.cycle_type, c.period_end,
		trim_scale(r.final_score)::text AS final_score, r.final_rating, r.recommendation, trim_scale(r.increase_percent)::text AS increase_percent,
		trim_scale(r.bonus_months)::text AS bonus_months, r.completed_at
		FROM hris.performance_reviews r JOIN hris.review_cycles c ON c.id = r.cycle_id
		WHERE r.employee_id = $1 AND r.status = 'completed' ORDER BY c.period_end DESC, r.completed_at DESC LIMIT 1`, employeeID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[ReviewResult])
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

// Recruitment stages (PRD P5 §7.6, Naming Convention §31).
const (
	StageApplied   = "applied"
	StageScreening = "screening"
	StageInterview = "interview"
	StageOffered   = "offered"
	StageHired     = "hired"
	StageRejected  = "rejected"
	StageWithdrawn = "withdrawn"
)

// Stages lists the application stages in pipeline order.
var Stages = []string{StageApplied, StageScreening, StageInterview, StageOffered, StageHired, StageRejected, StageWithdrawn}

// StageRank orders the open stages (closed stages rank -1).
func StageRank(stage string) int {
	switch strings.ToLower(stage) {
	case StageApplied:
		return 0
	case StageScreening:
		return 1
	case StageInterview:
		return 2
	case StageOffered:
		return 3
	case StageHired:
		return 4
	}
	return -1
}
