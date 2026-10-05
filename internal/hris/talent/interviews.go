package talent

// Interviews and scorecards (FR-RCT-03): HR schedules an interview with one
// or more interviewers (employees, notified; the candidate is invited by
// e-mail); every interviewer fills a scorecard with a score per criterion of
// the Recruitment Configuration and a recommendation; completing the
// interview records the result (pass / fail / hold) and the average score,
// which becomes the rating of the application.

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

// Interview types.
var InterviewTypes = []string{"phone", "video", "onsite", "panel", "practical"}

// Interview is a scheduled interview with its scorecards.
type Interview struct {
	ID              uuid.UUID            `json:"id" db:"id"`
	PropertyID      uuid.UUID            `json:"propertyId" db:"property_id"`
	ApplicationID   uuid.UUID            `json:"applicationId" db:"application_id"`
	ApplicationNo   string               `json:"applicationNumber" db:"application_number"`
	CandidateName   string               `json:"candidateName" db:"candidate_name"`
	RequisitionID   uuid.UUID            `json:"requisitionId" db:"requisition_id"`
	Title           string               `json:"title" db:"title"`
	Round           int                  `json:"round" db:"round"`
	InterviewType   string               `json:"interviewType" db:"interview_type" enum:"phone,video,onsite,panel,practical"`
	ScheduledAt     time.Time            `json:"scheduledAt" db:"scheduled_at"`
	DurationMinutes int                  `json:"durationMinutes" db:"duration_minutes"`
	Location        *string              `json:"location" db:"location"`
	InterviewerIDs  []uuid.UUID          `json:"interviewerIds" db:"interviewer_ids"`
	Interviewers    []string             `json:"interviewers" db:"interviewers"`
	Status          string               `json:"status" db:"status" enum:"scheduled,completed,cancelled,no_show"`
	Result          *string              `json:"result" db:"result" enum:"pass,fail,hold"`
	Score           *string              `json:"score" db:"score"`
	Notes           *string              `json:"notes" db:"notes"`
	CancelReason    *string              `json:"cancelReason" db:"cancel_reason"`
	CompletedAt     *time.Time           `json:"completedAt" db:"completed_at"`
	Scorecards      []InterviewScorecard `json:"scorecards" db:"scorecards"`
	CanScore        bool                 `json:"canScore" db:"-" doc:"The signed-in user is an interviewer without a scorecard yet"`
	CreatedAt       time.Time            `json:"createdAt" db:"created_at"`
}

// InterviewScorecard is the evaluation of one interviewer.
type InterviewScorecard struct {
	ID                uuid.UUID                 `json:"id"`
	InterviewerID     *uuid.UUID                `json:"interviewerId"`
	InterviewerUserID uuid.UUID                 `json:"interviewerUserId"`
	InterviewerName   string                    `json:"interviewerName"`
	Scores            []InterviewCriterionScore `json:"scores"`
	OverallScore      string                    `json:"overallScore"`
	Recommendation    string                    `json:"recommendation" enum:"strong_yes,yes,no,strong_no"`
	Comments          *string                   `json:"comments"`
	CreatedAt         time.Time                 `json:"createdAt"`
}

// InterviewCriterionScore is the score of one criterion.
type InterviewCriterionScore struct {
	Code    string `json:"code"`
	Label   string `json:"label,omitempty"`
	Weight  int    `json:"weight,omitempty"`
	Score   string `json:"score"`
	Comment string `json:"comment,omitempty"`
}

const interviewSelect = `SELECT i.id, i.property_id, i.application_id, a.number AS application_number, c.full_name AS candidate_name, a.requisition_id,
	r.title, i.round, i.interview_type, i.scheduled_at, i.duration_minutes, i.location, i.interviewer_ids,
	coalesce((SELECT array_agg(e.full_name ORDER BY e.full_name) FROM hris.employees e WHERE e.id = ANY (i.interviewer_ids)), '{}') AS interviewers,
	i.status, i.result, trim_scale(i.score)::text AS score, i.notes, i.cancel_reason, i.completed_at,
	coalesce((SELECT jsonb_agg(jsonb_build_object('id', s.id, 'interviewerId', s.interviewer_id, 'interviewerUserId', s.interviewer_user_id,
	  'interviewerName', coalesce(e.full_name, u.full_name, ''), 'scores', s.scores, 'overallScore', trim_scale(s.overall_score)::text,
	  'recommendation', s.recommendation, 'comments', s.comments, 'createdAt', s.created_at) ORDER BY s.created_at)
	  FROM hris.interview_scorecards s LEFT JOIN hris.employees e ON e.id = s.interviewer_id LEFT JOIN platform.users u ON u.id = s.interviewer_user_id
	  WHERE s.interview_id = i.id), '[]'::jsonb) AS scorecards, i.created_at
	FROM hris.interviews i JOIN hris.applications a ON a.id = i.application_id JOIN hris.candidates c ON c.id = a.candidate_id
	JOIN hris.job_requisitions r ON r.id = a.requisition_id`

// InterviewRequest schedules an interview.
type InterviewRequest struct {
	ScheduledAt     string      `json:"scheduledAt" doc:"RFC 3339 date-time"`
	DurationMinutes int         `json:"durationMinutes,omitempty" doc:"Default 60"`
	InterviewType   string      `json:"interviewType,omitempty" enum:"phone,video,onsite,panel,practical"`
	Location        string      `json:"location,omitempty"`
	InterviewerIDs  []uuid.UUID `json:"interviewerIds" doc:"Employees who interview and score"`
	Round           int         `json:"round,omitempty" doc:"Default: the next round"`
	InviteCandidate bool        `json:"inviteCandidate,omitempty" doc:"E-mail the invitation to the candidate"`
}

// InterviewUpdate reschedules a scheduled interview.
type InterviewUpdate struct {
	ScheduledAt     *string     `json:"scheduledAt,omitempty"`
	DurationMinutes *int        `json:"durationMinutes,omitempty"`
	InterviewType   *string     `json:"interviewType,omitempty" enum:"phone,video,onsite,panel,practical"`
	Location        *string     `json:"location,omitempty"`
	InterviewerIDs  []uuid.UUID `json:"interviewerIds,omitempty"`
}

// InterviewScorecardRequest is the scorecard of the signed-in interviewer.
type InterviewScorecardRequest struct {
	Scores         []InterviewCriterionScore `json:"scores" doc:"One score per criterion of the Recruitment Configuration"`
	Recommendation string                    `json:"recommendation" enum:"strong_yes,yes,no,strong_no"`
	Comments       string                    `json:"comments,omitempty"`
}

// InterviewCompleteRequest records the outcome of an interview.
type InterviewCompleteRequest struct {
	Result string `json:"result,omitempty" enum:"pass,fail,hold" doc:"Default: pass when the average score reaches the pass score"`
	NoShow bool   `json:"noShow,omitempty" doc:"The candidate did not come"`
	Notes  string `json:"notes,omitempty"`
}

func (m *Module) registerInterviews(reg *route.Registry) {
	tag := "HRIS Recruitment"
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/interviews", Summary: "Interviews (calendar)", Permission: "hris.interview.view",
		Response: Interview{}, List: true, Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "to", Description: "YYYY-MM-DD"},
			{Name: "status", Enum: []string{"scheduled", "completed", "cancelled", "no_show"}}, {Name: "mine", Type: "boolean", Description: "Where I interview"}},
		Handler: listRead(m.DB, m.listInterviewsHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/applications/{id}/interviews", Summary: "Schedule an interview",
		Permission: "hris.interview.manage", Request: InterviewRequest{}, Response: Interview{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.scheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodGet, Path: "/api/v1/hris/interviews/{id}", Summary: "Interview with scorecards", Permission: "hris.interview.view",
		Response: Interview{}, Handler: handle.Read(m.DB, m.getInterviewHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPatch, Path: "/api/v1/hris/interviews/{id}", Summary: "Reschedule an interview",
		Permission: "hris.interview.manage", Request: InterviewUpdate{}, Response: Interview{}, Handler: handle.Write(m.DB, http.StatusOK, m.rescheduleHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/interviews/{id}:cancel", Summary: "Cancel an interview",
		Permission: "hris.interview.manage", Request: RecruitmentReason{}, Response: Interview{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.cancelInterviewHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/interviews/{id}/scorecards", Summary: "Submit my interview scorecard",
		Permission: "hris.interview.score", Request: InterviewScorecardRequest{}, Response: Interview{}, Idempotent: true,
		Handler: handle.Write(m.DB, http.StatusCreated, m.scorecardHTTP)})
	add(reg, tag, route.Route{Method: http.MethodPost, Path: "/api/v1/hris/interviews/{id}:complete", Summary: "Record the result of an interview",
		Permission: "hris.interview.manage", Request: InterviewCompleteRequest{}, Response: Interview{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.completeInterviewHTTP)})
}

// interviews lists interviews matching a condition ($1 = property).
func (m *Module) interviews(ctx context.Context, tx pgx.Tx, cond string, args ...any) ([]Interview, error) {
	list, err := handle.List[Interview](tx.Query(ctx, interviewSelect+` WHERE i.property_id = $1 AND `+cond+` ORDER BY i.scheduled_at, i.round`,
		append([]any{handle.Property(ctx)}, args...)...))
	if err != nil {
		return nil, err
	}
	me := myEmployeeID(ctx, tx)
	uid := handle.UserID(ctx)
	for i := range list {
		it := &list[i]
		if it.Scorecards == nil {
			it.Scorecards = []InterviewScorecard{}
		}
		mine := me != nil && slices.Contains(it.InterviewerIDs, *me)
		scored := slices.ContainsFunc(it.Scorecards, func(s InterviewScorecard) bool { return s.InterviewerUserID == uid })
		it.CanScore = mine && !scored && it.Status != "cancelled"
	}
	return list, nil
}

// myEmployeeID is the employee of the signed-in user (nil when none).
func myEmployeeID(ctx context.Context, tx pgx.Tx) *uuid.UUID {
	uid := handle.UserID(ctx)
	if uid == uuid.Nil {
		return nil
	}
	e, err := hris.EmployeeByUser(ctx, tx, uid)
	if err != nil || e == nil {
		return nil
	}
	return &e.ID
}

func (m *Module) interview(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (Interview, error) {
	list, err := m.interviews(ctx, tx, `i.id = $2`, iid)
	if err != nil {
		return Interview{}, err
	}
	if len(list) == 0 {
		return Interview{}, errs.NotFound("interview")
	}
	return list[0], nil
}

func (m *Module) listInterviewsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]Interview, error) {
	day := today(ctx, tx, handle.Property(ctx))
	from, err := handle.QueryDate(r, "from", day.AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	to, err := handle.QueryDate(r, "to", day.AddDate(0, 0, 60))
	if err != nil {
		return nil, err
	}
	loc := location(ctx, tx, handle.Property(ctx)).String()
	var me *uuid.UUID
	if filterParam(r, "mine") == "true" {
		me = myEmployeeID(ctx, tx)
		if me == nil {
			return []Interview{}, nil
		}
	}
	return m.interviews(ctx, tx, `(i.scheduled_at AT TIME ZONE $2)::date BETWEEN $3::date AND $4::date AND ($5 = '' OR i.status = $5)
		AND ($6::uuid IS NULL OR $6 = ANY (i.interviewer_ids))`, loc, ymd(from), ymd(to), filterParam(r, "status"), me)
}

func (m *Module) getInterviewHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (Interview, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	return m.interview(ctx, tx, iid)
}

// checkInterviewers validates the interviewers (active employees).
func checkInterviewers(ctx context.Context, tx pgx.Tx, property uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	ids = dedupe(ids)
	if len(ids) == 0 {
		return nil, handle.Invalid("interviewerIds", "required", "choose one or more interviewers")
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hris.employees WHERE id = ANY ($1) AND property_id = $2 AND status = 'active' AND archived_at IS NULL`,
		ids, property).Scan(&n); err != nil {
		return nil, err
	}
	if n != len(ids) {
		return nil, handle.Invalid("interviewerIds", "not_found", "an interviewer is not an active employee")
	}
	return ids, nil
}

func (m *Module) scheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req InterviewRequest) (Interview, error) {
	aid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	a, err := lockApplication(ctx, tx, aid)
	if err != nil {
		return Interview{}, err
	}
	if hris.StageRank(a.Stage) < 0 || hris.StageRank(a.Stage) > hris.StageRank(hris.StageInterview) {
		return Interview{}, errs.Conflict("invalid_stage", "interviews are scheduled for applications in Applied, Screening or Interview")
	}
	if err := requisitionLive(ctx, tx, a.RequisitionID); err != nil {
		return Interview{}, err
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ScheduledAt))
	if err != nil {
		return Interview{}, handle.Invalid("scheduledAt", "invalid", "an RFC 3339 date-time")
	}
	if req.DurationMinutes == 0 {
		req.DurationMinutes = 60
	}
	if req.DurationMinutes < 5 || req.DurationMinutes > 480 {
		return Interview{}, handle.Invalid("durationMinutes", "invalid", "between 5 and 480 minutes")
	}
	if req.InterviewType == "" {
		req.InterviewType = "onsite"
	}
	if !oneOf(InterviewTypes, req.InterviewType) {
		return Interview{}, enumErr("interviewType", InterviewTypes)
	}
	ids, err := checkInterviewers(ctx, tx, a.PropertyID, req.InterviewerIDs)
	if err != nil {
		return Interview{}, err
	}
	round := req.Round
	if round == 0 {
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(round), 0) + 1 FROM hris.interviews WHERE application_id = $1 AND status <> 'cancelled'`, aid).
			Scan(&round); err != nil {
			return Interview{}, err
		}
	}
	if round < 1 || round > 20 {
		return Interview{}, handle.Invalid("round", "invalid", "between 1 and 20")
	}
	iid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.interviews (id, property_id, application_id, round, interview_type, scheduled_at, duration_minutes, location,
		interviewer_ids, created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, iid, a.PropertyID, aid, round, req.InterviewType, at.UTC(),
		req.DurationMinutes, nullStr(req.Location), ids, actor(ctx)); err != nil {
		return Interview{}, err
	}
	if a.Stage != hris.StageInterview {
		if err := stageEvent(ctx, tx, a, hris.StageInterview, "Interview round "+itoa(round)+" scheduled"); err != nil {
			return Interview{}, err
		}
	}
	out, err := m.interview(ctx, tx, iid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionCreate, EntityType: "hris.interview", EntityID: iid.String(),
		EntityLabel: a.Number + " · round " + itoa(round), PropertyID: &a.PropertyID, After: interviewAudit(out)}); err != nil {
		return out, err
	}
	return out, m.notifyInterview(ctx, tx, out, ids, req.InviteCandidate)
}

func interviewAudit(i Interview) map[string]any {
	return map[string]any{"applicationId": i.ApplicationID, "round": i.Round, "type": i.InterviewType, "scheduledAt": i.ScheduledAt,
		"interviewerIds": i.InterviewerIDs, "status": i.Status, "result": i.Result, "score": i.Score}
}

// notifyInterview informs the interviewers (and invites the candidate).
func (m *Module) notifyInterview(ctx context.Context, tx pgx.Tx, i Interview, interviewers []uuid.UUID, invite bool) error {
	var users []uuid.UUID
	for _, e := range interviewers {
		e := e
		if u := userOf(ctx, tx, &e); u != nil {
			users = append(users, *u)
		}
	}
	date := i.ScheduledAt.In(location(ctx, tx, i.PropertyID)).Format("2006-01-02 15:04")
	data := map[string]any{"candidate": i.CandidateName, "title": i.Title, "date": date, "type": i.InterviewType, "location": deref(i.Location)}
	if err := m.notifyUsers(ctx, tx, i.PropertyID, users, "hris.interview_scheduled", "/hris/recruitment/applications/"+i.ApplicationID.String(), data); err != nil {
		return err
	}
	if !invite {
		return nil
	}
	var email *string
	if err := tx.QueryRow(ctx, `SELECT c.email FROM hris.applications a JOIN hris.candidates c ON c.id = a.candidate_id WHERE a.id = $1`, i.ApplicationID).
		Scan(&email); err != nil {
		return err
	}
	return m.notifyCandidate(ctx, tx, i.PropertyID, email, i.CandidateName, "hris.candidate_interview_invitation", data)
}

// lockInterview loads an interview for update.
func lockInterview(ctx context.Context, tx pgx.Tx, iid uuid.UUID) (uuid.UUID, uuid.UUID, string, []uuid.UUID, error) {
	var property, app uuid.UUID
	var status string
	var ids []uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT property_id, application_id, status, interviewer_ids FROM hris.interviews WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		iid, handle.Property(ctx)).Scan(&property, &app, &status, &ids); err != nil {
		return property, app, status, ids, errs.NotFound("interview")
	}
	return property, app, status, ids, nil
}

func (m *Module) rescheduleHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req InterviewUpdate) (Interview, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	property, _, status, ids, err := lockInterview(ctx, tx, iid)
	if err != nil {
		return Interview{}, err
	}
	if status != "scheduled" {
		return Interview{}, errs.Conflict("interview_closed", "the interview is "+status)
	}
	before, err := m.interview(ctx, tx, iid)
	if err != nil {
		return before, err
	}
	at, dur, typ, loc := before.ScheduledAt, before.DurationMinutes, before.InterviewType, before.Location
	if req.ScheduledAt != nil {
		if at, err = time.Parse(time.RFC3339, strings.TrimSpace(*req.ScheduledAt)); err != nil {
			return before, handle.Invalid("scheduledAt", "invalid", "an RFC 3339 date-time")
		}
	}
	if req.DurationMinutes != nil {
		if dur = *req.DurationMinutes; dur < 5 || dur > 480 {
			return before, handle.Invalid("durationMinutes", "invalid", "between 5 and 480 minutes")
		}
	}
	if req.InterviewType != nil {
		if typ = *req.InterviewType; !oneOf(InterviewTypes, typ) {
			return before, enumErr("interviewType", InterviewTypes)
		}
	}
	if req.Location != nil {
		loc = nullStr(*req.Location)
	}
	if req.InterviewerIDs != nil {
		if ids, err = checkInterviewers(ctx, tx, property, req.InterviewerIDs); err != nil {
			return before, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interviews SET scheduled_at = $2, duration_minutes = $3, interview_type = $4, location = $5, interviewer_ids = $6,
		updated_by = $7 WHERE id = $1`, iid, at.UTC(), dur, typ, loc, ids, actor(ctx)); err != nil {
		return before, err
	}
	after, err := m.interview(ctx, tx, iid)
	if err != nil {
		return after, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: audit.ActionUpdate, EntityType: "hris.interview", EntityID: iid.String(),
		EntityLabel: after.ApplicationNo + " · round " + itoa(after.Round), PropertyID: &property, Before: interviewAudit(before),
		After: interviewAudit(after)}); err != nil {
		return after, err
	}
	return after, m.notifyInterview(ctx, tx, after, ids, false)
}

func (m *Module) cancelInterviewHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req RecruitmentReason) (Interview, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	property, _, status, _, err := lockInterview(ctx, tx, iid)
	if err != nil {
		return Interview{}, err
	}
	if status != "scheduled" {
		return Interview{}, errs.Conflict("interview_closed", "the interview is "+status)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return Interview{}, handle.Invalid("reason", "required", "a reason is required")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interviews SET status = 'cancelled', cancel_reason = $2, updated_by = $3 WHERE id = $1`, iid, req.Reason,
		actor(ctx)); err != nil {
		return Interview{}, err
	}
	out, err := m.interview(ctx, tx, iid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "cancel", EntityType: "hris.interview", EntityID: iid.String(),
		EntityLabel: out.ApplicationNo + " · round " + itoa(out.Round), PropertyID: &property, Reason: req.Reason,
		Before: map[string]any{"status": status}, After: map[string]any{"status": "cancelled"}})
}

// scoreCriteria validates the criterion scores against the configuration
// and returns them with the weighted overall score.
func scoreCriteria(cfg hris.RecruitmentConfiguration, in []InterviewCriterionScore) ([]InterviewCriterionScore, decimal.Decimal, error) {
	byCode := map[string]InterviewCriterionScore{}
	for _, s := range in {
		byCode[strings.TrimSpace(s.Code)] = s
	}
	var out []InterviewCriterionScore
	var items []hris.ScoreItem
	for _, c := range cfg.InterviewCriteria {
		s, ok := byCode[c.Code]
		if !ok {
			return nil, decimal.Zero, handle.Invalid("scores", "required", "score every criterion: "+c.Label)
		}
		v, err := decimal.NewFromString(strings.TrimSpace(s.Score))
		if err != nil || !hris.ValidScore(v, cfg.ScoreScale) {
			return nil, decimal.Zero, handle.Invalid("scores", "invalid", c.Label+": a score from 1 to "+itoa(cfg.ScoreScale))
		}
		delete(byCode, c.Code)
		out = append(out, InterviewCriterionScore{Code: c.Code, Label: c.Label, Weight: c.Weight, Score: v.String(), Comment: strings.TrimSpace(s.Comment)})
		items = append(items, hris.ScoreItem{Weight: decimal.NewFromInt(int64(c.Weight)), Score: &v})
	}
	for code := range byCode {
		return nil, decimal.Zero, handle.Invalid("scores", "unknown", "unknown criterion "+code)
	}
	overall, _ := hris.WeightedScore(items)
	return out, overall, nil
}

func (m *Module) scorecardHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req InterviewScorecardRequest) (Interview, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	property, _, status, ids, err := lockInterview(ctx, tx, iid)
	if err != nil {
		return Interview{}, err
	}
	if status == "cancelled" || status == "no_show" {
		return Interview{}, errs.Conflict("interview_closed", "the interview is "+status)
	}
	me := myEmployeeID(ctx, tx)
	if (me == nil || !slices.Contains(ids, *me)) && !can(ctx, "hris.interview.manage", property) {
		return Interview{}, errs.Forbidden("only an interviewer of this interview can submit a scorecard")
	}
	if !oneOf([]string{"strong_yes", "yes", "no", "strong_no"}, req.Recommendation) {
		return Interview{}, enumErr("recommendation", []string{"strong_yes", "yes", "no", "strong_no"})
	}
	cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
	if err != nil {
		return Interview{}, err
	}
	scores, overall, err := scoreCriteria(cfg, req.Scores)
	if err != nil {
		return Interview{}, err
	}
	raw, _ := json.Marshal(scores)
	uid := handle.UserID(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO hris.interview_scorecards (id, property_id, interview_id, interviewer_id, interviewer_user_id, scores, overall_score,
		recommendation, comments) VALUES ($1,$2,$3,$4,$5,$6,$7::numeric,$8,$9) ON CONFLICT (interview_id, interviewer_user_id) DO NOTHING`,
		id.New(), property, iid, me, uid, raw, overall.String(), req.Recommendation, nullStr(req.Comments))
	if err != nil {
		return Interview{}, err
	}
	if tag.RowsAffected() == 0 {
		return Interview{}, errs.Conflict("scorecard_exists", "you already submitted a scorecard for this interview")
	}
	out, err := m.interview(ctx, tx, iid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "scorecard", EntityType: "hris.interview", EntityID: iid.String(),
		EntityLabel: out.ApplicationNo + " · round " + itoa(out.Round), PropertyID: &property,
		After: map[string]any{"overallScore": overall.String(), "recommendation": req.Recommendation}})
}

func (m *Module) completeInterviewHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req InterviewCompleteRequest) (Interview, error) {
	iid, err := handle.ID(r)
	if err != nil {
		return Interview{}, err
	}
	property, app, status, _, err := lockInterview(ctx, tx, iid)
	if err != nil {
		return Interview{}, err
	}
	if status != "scheduled" {
		return Interview{}, errs.Conflict("interview_closed", "the interview is "+status)
	}
	var avg *decimal.Decimal
	if err := tx.QueryRow(ctx, `SELECT round(avg(overall_score), 2) FROM hris.interview_scorecards WHERE interview_id = $1`, iid).Scan(&avg); err != nil {
		return Interview{}, err
	}
	next, result := "completed", strings.TrimSpace(req.Result)
	switch {
	case req.NoShow:
		next, result = "no_show", "fail"
	case result == "":
		if avg == nil {
			return Interview{}, handle.Invalid("result", "required", "no scorecard yet: choose the result")
		}
		cfg, _, err := hris.LoadRecruitmentConfiguration(ctx, tx, property, clock.Now())
		if err != nil {
			return Interview{}, err
		}
		result = "fail"
		if avg.GreaterThanOrEqual(hris.Dec(cfg.PassScore)) {
			result = "pass"
		}
	case !oneOf([]string{"pass", "fail", "hold"}, result):
		return Interview{}, enumErr("result", []string{"pass", "fail", "hold"})
	}
	var score *string
	if avg != nil {
		s := avg.String()
		score = &s
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.interviews SET status = $2, result = $3, score = $4::numeric, notes = coalesce($5, notes), completed_at = now(),
		updated_by = $6 WHERE id = $1`, iid, next, result, score, nullStr(req.Notes), actor(ctx)); err != nil {
		return Interview{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET rating = (SELECT round(avg(score), 2) FROM hris.interviews WHERE application_id = $1
		AND status = 'completed' AND score IS NOT NULL) WHERE id = $1`, app); err != nil {
		return Interview{}, err
	}
	out, err := m.interview(ctx, tx, iid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "complete", EntityType: "hris.interview", EntityID: iid.String(),
		EntityLabel: out.ApplicationNo + " · round " + itoa(out.Round), PropertyID: &property, Before: map[string]any{"status": status},
		After: map[string]any{"status": next, "result": result, "score": score}})
}
