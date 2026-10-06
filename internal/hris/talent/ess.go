package talent

// Employee Self Service sections of performance review (EP-05 in ESS,
// §16 #6): My Reviews — the self assessment and, once the cycle is closed,
// the result to acknowledge — and, for managers, Team Reviews — the manager
// review of the team (the reviewer or anyone above in the line).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

func init() {
	hris.RegisterESSSection(hris.ESSSection{Key: "reviews", Label: "My Reviews", LabelID: "Penilaian Saya", Icon: "star_rate", Path: "/ops/ess/reviews",
		Order: 85})
	hris.RegisterESSSection(hris.ESSSection{Key: "team-reviews", Label: "Team Reviews", LabelID: "Penilaian Tim", Icon: "reviews",
		Path: "/ops/ess/team-reviews", Permission: hris.PermissionTeam, Manager: true, Order: 105})
}

func (m *Module) registerESS(reg *route.Registry) {
	tag := "Employee Self Service"
	ess := func(rt route.Route) {
		if rt.Permission == "" {
			rt.Permission = hris.PermissionESS
		}
		add(reg, tag, rt)
	}
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/reviews", Summary: "My performance reviews", Response: PerformanceReview{}, List: true,
		Handler: listRead(m.DB, m.essReviewsHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/reviews/{id}", Summary: "My performance review", Response: PerformanceReviewDetail{},
		Handler: handle.Read(m.DB, m.essReviewHTTP)})
	ess(route.Route{Method: http.MethodPatch, Path: "/api/v1/ess/reviews/{id}", Summary: "Save my self assessment", Request: ReviewSelfInput{},
		Response: PerformanceReviewDetail{}, Handler: handle.Write(m.DB, http.StatusOK, m.essSaveSelfHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/reviews/{id}:submit", Summary: "Submit my self assessment to my manager",
		Request: handle.Empty{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essSubmitSelfHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/reviews/{id}:acknowledge", Summary: "Acknowledge my review result",
		Request: handle.Empty{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK, Handler: handle.Write(m.DB, http.StatusOK, m.essAcknowledgeHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team-reviews", Summary: "Reviews of my team (managers)", Permission: hris.PermissionTeam,
		Response: PerformanceReview{}, List: true, Query: []route.Param{{Name: "status", Enum: ReviewStatuses}}, Handler: listRead(m.DB, m.essTeamReviewsHTTP)})
	ess(route.Route{Method: http.MethodGet, Path: "/api/v1/ess/team-reviews/{id}", Summary: "A review of my team", Permission: hris.PermissionTeam,
		Response: PerformanceReviewDetail{}, Handler: handle.Read(m.DB, m.essTeamReviewHTTP)})
	ess(route.Route{Method: http.MethodPatch, Path: "/api/v1/ess/team-reviews/{id}", Summary: "Save my manager review", Permission: hris.PermissionTeam,
		Request: ReviewManagerInput{}, Response: PerformanceReviewDetail{}, Handler: handle.Write(m.DB, http.StatusOK, m.essSaveManagerHTTP)})
	ess(route.Route{Method: http.MethodPost, Path: "/api/v1/ess/team-reviews/{id}:submit", Summary: "Submit my manager review for calibration",
		Permission: hris.PermissionTeam, Request: handle.Empty{}, Response: PerformanceReviewDetail{}, Status: http.StatusOK,
		Handler: handle.Write(m.DB, http.StatusOK, m.essSubmitManagerHTTP)})
}

// me is the employee of the signed-in user.
func me(ctx context.Context, tx pgx.Tx) (hris.Employee, error) {
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

func (m *Module) essReviewsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PerformanceReview, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.employee_id = $1 AND r.status <> 'cancelled' AND c.status <> 'draft'
		ORDER BY c.period_end DESC`, e.ID))
	for i := range list {
		if list[i].Status != "completed" {
			hideResult(&list[i])
		}
		list[i].IncreasePercent, list[i].BonusMonths = nil, nil
	}
	return list, err
}

func hideResult(rv *PerformanceReview) {
	rv.ManagerScore, rv.ManagerComment, rv.Strengths, rv.Improvements = nil, nil, nil, nil
	rv.RecommendedRating, rv.FinalScore, rv.FinalRating, rv.CalibrationNote = nil, nil, nil, nil
	rv.Recommendation = "none"
}

// myReview loads a review of the signed-in employee.
func (m *Module) myReview(ctx context.Context, tx pgx.Tx, r *http.Request, lock bool) (PerformanceReview, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReview{}, err
	}
	e, err := me(ctx, tx)
	if err != nil {
		return PerformanceReview{}, err
	}
	var rv PerformanceReview
	if lock {
		rv, err = lockReview(ctx, tx, rid)
	} else {
		rv, err = m.review(ctx, tx, rid)
	}
	if err != nil || rv.EmployeeID != e.ID || rv.Status == "cancelled" {
		return PerformanceReview{}, errs.NotFound("performance review")
	}
	return rv, nil
}

func (m *Module) essDetail(ctx context.Context, tx pgx.Tx, rv PerformanceReview) (PerformanceReviewDetail, error) {
	out, err := m.detail(ctx, tx, rv, true)
	if err != nil {
		return out, err
	}
	out.CanEditSelf = rv.Status == "self_assessment" && rv.CycleStatus == "in_progress"
	return out, nil
}

func (m *Module) essReviewHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PerformanceReviewDetail, error) {
	rv, err := m.myReview(ctx, tx, r, false)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.essDetail(ctx, tx, rv)
}

func (m *Module) essSaveSelfHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewSelfInput) (PerformanceReviewDetail, error) {
	rv, err := m.myReview(ctx, tx, r, true)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if rv.Status != "self_assessment" || rv.CycleStatus != "in_progress" {
		return PerformanceReviewDetail{}, errs.Conflict("self_assessment_closed", "the self assessment is closed")
	}
	cfg, _, err := hris.LoadPerformanceConfiguration(ctx, tx, rv.PropertyID, clock.Now())
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := saveScores(ctx, tx, rv.ID, cfg.ScoreScale, false, req.Scores); err != nil {
		return PerformanceReviewDetail{}, err
	}
	score, _, err := reviewScore(ctx, tx, rv.ID, false)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET self_score = $2::numeric, self_comment = coalesce($3, self_comment), updated_by = $4
		WHERE id = $1`, rv.ID, decStr(score), req.Comment, actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "self_assessment", EntityType: "hris.performance_review", EntityID: rv.ID.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, After: map[string]any{"selfScore": decStr(score),
			"items": len(req.Scores)}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rv.ID)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.essDetail(ctx, tx, after)
}

func (m *Module) essSubmitSelfHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PerformanceReviewDetail, error) {
	rv, err := m.myReview(ctx, tx, r, true)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if rv.Status != "self_assessment" || rv.CycleStatus != "in_progress" {
		return PerformanceReviewDetail{}, errs.Conflict("self_assessment_closed", "the self assessment is closed")
	}
	score, complete, err := reviewScore(ctx, tx, rv.ID, false)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if !complete || score == nil {
		return PerformanceReviewDetail{}, errs.Conflict("scores_missing", "score every competency and KPI before submitting")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'manager_review', self_score = $2::numeric, self_submitted_at = now(), updated_by = $3
		WHERE id = $1`, rv.ID, score.String(), actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "submit_self_assessment", EntityType: "hris.performance_review",
		EntityID: rv.ID.String(), EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID,
		Before: map[string]any{"status": rv.Status}, After: map[string]any{"status": "manager_review", "selfScore": score.String()}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if u := userOf(ctx, tx, rv.ReviewerID); u != nil {
		if err := m.notifyUsers(ctx, tx, rv.PropertyID, []uuid.UUID{*u}, "hris.review_manager_due", "/ops/ess/team-reviews",
			map[string]any{"employeeName": rv.EmployeeName, "cycle": rv.CycleName, "dueDate": dateOr(rv.ManagerDue)}); err != nil {
			return PerformanceReviewDetail{}, err
		}
	}
	after, err := m.review(ctx, tx, rv.ID)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.essDetail(ctx, tx, after)
}

func (m *Module) essAcknowledgeHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PerformanceReviewDetail, error) {
	rv, err := m.myReview(ctx, tx, r, true)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if rv.Status != "completed" {
		return PerformanceReviewDetail{}, errs.Conflict("review_not_completed", "the result is not published yet")
	}
	if rv.AcknowledgedAt != nil {
		return PerformanceReviewDetail{}, errs.Conflict("already_acknowledged", "you already acknowledged this review")
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET acknowledged_at = now(), updated_by = $2 WHERE id = $1`, rv.ID, actor(ctx)); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "acknowledge", EntityType: "hris.performance_review", EntityID: rv.ID.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rv.ID)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.essDetail(ctx, tx, after)
}

// teamReview loads a review the signed-in manager may do: the reviewer or a
// manager above the employee.
func (m *Module) teamReview(ctx context.Context, tx pgx.Tx, r *http.Request, lock bool) (PerformanceReview, hris.Employee, error) {
	rid, err := handle.ID(r)
	if err != nil {
		return PerformanceReview{}, hris.Employee{}, err
	}
	e, err := me(ctx, tx)
	if err != nil {
		return PerformanceReview{}, e, err
	}
	var rv PerformanceReview
	if lock {
		rv, err = lockReview(ctx, tx, rid)
	} else {
		rv, err = m.review(ctx, tx, rid)
	}
	if err != nil || rv.Status == "cancelled" || rv.EmployeeID == e.ID {
		return PerformanceReview{}, e, errs.NotFound("performance review")
	}
	if rv.ReviewerID == nil || *rv.ReviewerID != e.ID {
		lead, err := hris.IsManagerOf(ctx, tx, e.ID, rv.EmployeeID)
		if err != nil {
			return rv, e, err
		}
		if !lead {
			return PerformanceReview{}, e, errs.NotFound("performance review")
		}
	}
	return rv, e, nil
}

func (m *Module) essTeamReviewsHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) ([]PerformanceReview, error) {
	e, err := me(ctx, tx)
	if err != nil {
		return nil, err
	}
	list, err := handle.List[PerformanceReview](tx.Query(ctx, reviewSelect+` WHERE r.status <> 'cancelled' AND c.status IN ('in_progress', 'calibration',
		'completed') AND r.employee_id <> $1 AND (r.reviewer_id = $1 OR r.employee_id IN (`+teamSQL+`)) AND ($2 = '' OR r.status = $2)
		ORDER BY CASE r.status WHEN 'manager_review' THEN 0 WHEN 'self_assessment' THEN 1 ELSE 2 END, c.period_end DESC, e.full_name`, e.ID,
		filterParam(r, "status")))
	for i := range list {
		list[i].IncreasePercent, list[i].BonusMonths = nil, nil
	}
	return list, err
}

// teamSQL lists the employees a manager ($1) leads (hris.Team).
const teamSQL = `WITH RECURSIVE reports AS (
	  SELECT id, 1 AS depth FROM hris.employees WHERE supervisor_id = $1
	  UNION ALL SELECT x.id, rp.depth + 1 FROM hris.employees x JOIN reports rp ON x.supervisor_id = rp.id WHERE rp.depth < 20),
	units AS (
	  SELECT id FROM hris.org_units WHERE head_employee_id = $1
	  UNION ALL SELECT ch.id FROM hris.org_units ch JOIN units ON ch.parent_id = units.id)
	SELECT id FROM reports UNION SELECT y.id FROM hris.employees y JOIN units ON y.org_unit_id = units.id WHERE y.id <> $1`

func (m *Module) teamDetail(ctx context.Context, tx pgx.Tx, rv PerformanceReview) (PerformanceReviewDetail, error) {
	out, err := m.detail(ctx, tx, rv, false)
	if err != nil {
		return out, err
	}
	out.Review.IncreasePercent, out.Review.BonusMonths = nil, nil
	out.CanEditManager = rv.Status == "manager_review" && (rv.CycleStatus == "in_progress" || rv.CycleStatus == "calibration")
	return out, nil
}

func (m *Module) essTeamReviewHTTP(ctx context.Context, tx pgx.Tx, r *http.Request) (PerformanceReviewDetail, error) {
	rv, _, err := m.teamReview(ctx, tx, r, false)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.teamDetail(ctx, tx, rv)
}

func (m *Module) essSaveManagerHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, req ReviewManagerInput) (PerformanceReviewDetail, error) {
	rv, _, err := m.teamReview(ctx, tx, r, true)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := m.saveManager(ctx, tx, rv, req, false); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rv.ID)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	raw, _ := json.Marshal(req.Scores)
	if err := audit.Record(ctx, tx, audit.Entry{Module: hris.Module, Action: "manager_review", EntityType: "hris.performance_review", EntityID: rv.ID.String(),
		EntityLabel: rv.CycleCode + " · " + rv.EmployeeNo, PropertyID: &rv.PropertyID, Before: map[string]any{"managerScore": rv.ManagerScore},
		After: map[string]any{"managerScore": after.ManagerScore, "items": strings.Count(string(raw), "\"code\"")}}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.teamDetail(ctx, tx, after)
}

func (m *Module) essSubmitManagerHTTP(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PerformanceReviewDetail, error) {
	rv, _, err := m.teamReview(ctx, tx, r, true)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := m.submitManager(ctx, tx, rv, ""); err != nil {
		return PerformanceReviewDetail{}, err
	}
	if err := m.notifyUsers(ctx, tx, rv.PropertyID, holders(ctx, tx, rv.PropertyID, "hris.performance_review.calibrate"), "hris.review_submitted",
		"/hris/performance/reviews/"+rv.ID.String(), map[string]any{"employeeName": rv.EmployeeName, "cycle": rv.CycleName,
			"reviewer": deref(rv.ReviewerName)}); err != nil {
		return PerformanceReviewDetail{}, err
	}
	after, err := m.review(ctx, tx, rv.ID)
	if err != nil {
		return PerformanceReviewDetail{}, err
	}
	return m.teamDetail(ctx, tx, after)
}
