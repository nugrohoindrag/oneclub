package loyalty

// Routes of the PRD P5 loyalty features (§11: /crm/loyalty/tier-evaluations,
// /crm/loyalty/reward-rules) — CRM → Loyalty (Tiers, Rewards, Eligibility)
// and the Member App Loyalty (tier progress, eligible rewards; EP-26).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

// P5Permissions are the permissions of the P5 loyalty features.
func P5Permissions() []catalog.Permission {
	perms := resource.Permissions(RewardRules, TopSpenderPrograms)
	perms = append(perms, catalog.P("crm", "loyalty_reward_issue", "view", "create", "fulfil")...)
	return append(perms, catalog.P("crm", "loyalty_budget", "view")...)
}

// P5RolePermissions grant the P5 loyalty features.
func P5RolePermissions() map[string][]string {
	all := append(resource.AllActions(RewardRules, TopSpenderPrograms), "crm.loyalty_reward_issue.view", "crm.loyalty_reward_issue.create",
		"crm.loyalty_reward_issue.fulfil", "crm.loyalty_budget.view")
	view := []string{"crm.loyalty_reward_rule.view", "crm.top_spender_program.view", "crm.loyalty_reward_issue.view", "crm.loyalty_budget.view"}
	desk := []string{"crm.loyalty_reward_rule.view", "crm.loyalty_reward_issue.view", "crm.loyalty_reward_issue.fulfil"}
	return map[string][]string{
		"property_admin":          all,
		"crm_admin":               all,
		"marketing_staff":         all,
		"general_manager":         view,
		"club_manager":            view,
		"finance_manager":         view,
		"membership_manager":      append(append([]string{}, view...), "crm.loyalty_reward_issue.create"),
		"sales_executive":         view,
		"front_desk":              desk,
		"sport_club_receptionist": desk,
		"cashier":                 desk,
		"outlet_manager":          desk,
	}
}

// RegisterP5 adds the routes of the P5 loyalty features (wired by internal/app).
func (m *Module) RegisterP5(reg *route.Registry, eng *resource.Engine, ts *topspender.Service) {
	for _, d := range []*resource.Def{RewardRules, TopSpenderPrograms} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Loyalty"
		reg.Add(rt)
	}
	// Tier evaluations (FR-LOY-P5-01).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-evaluations", Summary: "Tier evaluation runs", Permission: "crm.loyalty_tier.view",
		Response: LoyaltyTierEvaluationRun{}, List: true, Query: []route.Param{{Name: "filter[kind]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTierEvaluationRun], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LoyaltyTierEvaluationRun](tx.Query(ctx, evalRunSelect+` WHERE property_id = $1 AND ($2 = '' OR kind = $2)
				ORDER BY created_at DESC LIMIT $3`, handle.Property(ctx), lp.Filters["kind"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/tier-evaluations", Summary: "Run a tier evaluation (annual, periodic, grace review)",
		Permission: "crm.loyalty_tier.update", Request: LoyaltyTierEvaluationInput{}, Response: LoyaltyTierEvaluationRun{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyTierEvaluationInput) (LoyaltyTierEvaluationRun, error) {
			pid := handle.Property(ctx)
			run, err := m.RunTierEvaluation(ctx, tx, pid, in)
			if err != nil {
				return run, err
			}
			return run, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "evaluate_tiers", EntityType: "crm.loyalty_tier_evaluation",
				EntityID: run.ID.String(), EntityLabel: run.Number, PropertyID: &pid, After: run})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-evaluations/{id}", Summary: "Tier evaluation with its results per account",
		Permission: "crm.loyalty_tier.view", Response: LoyaltyTierEvaluationDetail{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyTierEvaluationDetail, error) {
			rid, err := handle.ID(r)
			if err != nil {
				return LoyaltyTierEvaluationDetail{}, err
			}
			return TierEvaluation(ctx, tx, handle.Property(ctx), rid)
		})})
	// Tier benefits as pricing / booking read them (FR-LOY-P5-02).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-benefits", Summary: "Tier benefits of a customer (pricing / booking interface)",
		Permission: "crm.loyalty_account.view", Response: LoyaltyTierBenefits{}, Query: []route.Param{{Name: "customerId", Required: true}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyTierBenefits, error) {
			cid, err := handle.QueryUUID(r, "customerId")
			if err != nil {
				return LoyaltyTierBenefits{}, err
			}
			if cid == nil {
				return LoyaltyTierBenefits{}, handle.Invalid("customerId", "required", "customerId is required")
			}
			if ok, err := crm.ExistsInProperty(ctx, tx, handle.Property(ctx), *cid); err != nil || !ok {
				if err == nil {
					err = handle.Invalid("customerId", "not_found", "customer not found in this property")
				}
				return LoyaltyTierBenefits{}, err
			}
			return BenefitsOf(ctx, tx, *cid)
		})})
	// Eligibility (FR-LOY-P5-04).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/accounts/{id}/eligible-rewards", Summary: "Rewards with the eligibility of an account",
		Permission: "crm.loyalty_account.view", Response: LoyaltyEligibleReward{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyEligibleReward], error) {
			aid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[LoyaltyEligibleReward]{}, err
			}
			a, err := requireAccount(ctx, tx, handle.Property(ctx), aid)
			if err != nil {
				return httpx.Page[LoyaltyEligibleReward]{}, err
			}
			return handle.Page(EligibleRewards(ctx, tx, a))
		})})
	// Rewards issued without points (FR-LOY-P5-05).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/reward-issues", Summary: "Rewards issued without points (Top Spender, journeys, staff)",
		Permission: "crm.loyalty_reward_issue.view", Response: LoyaltyRewardIssue{}, List: true,
		Query: []route.Param{{Name: "filter[customerId]"}, {Name: "filter[status]"}, {Name: "filter[source]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyRewardIssue], error) {
			lp := httpx.ParseList(r)
			var cid *uuid.UUID
			if s := lp.Filters["customerId"]; s != "" {
				u, err := uuid.Parse(s)
				if err != nil {
					return httpx.Page[LoyaltyRewardIssue]{}, handle.Invalid("filter[customerId]", "invalid_uuid", "customerId must be a UUID")
				}
				cid = &u
			}
			return handle.Page(RewardIssues(ctx, tx, handle.Property(ctx), cid, lp.Filters["status"], lp.Filters["source"], lp.Limit))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/reward-issues", Summary: "Issue a reward without points (staff, reason required)",
		Permission: "crm.loyalty_reward_issue.create", Request: LoyaltyRewardIssueInput{}, Response: LoyaltyRewardIssue{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyRewardIssueInput) (LoyaltyRewardIssue, error) {
			if err := handle.Required("note", in.Note); err != nil {
				return LoyaltyRewardIssue{}, err
			}
			pid := handle.Property(ctx)
			i, _, err := m.IssueReward(ctx, tx, IssueRequest{PropertyID: pid, CustomerID: in.CustomerID, RewardID: in.RewardID, Quantity: in.Quantity,
				Source: "staff", Note: in.Note})
			if err != nil {
				return i, err
			}
			return i, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.loyalty_reward_issue",
				EntityID: i.ID.String(), EntityLabel: i.Number + " · " + i.RewardName, PropertyID: &pid, Reason: in.Note, After: i})
		})})
	for _, act := range []struct{ path, to, summary string }{{"fulfil", "fulfilled", "Mark an issued reward as handed over"},
		{"cancel", "cancelled", "Cancel an issued reward (stock back)"}} {
		act := act
		add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/reward-issues/{id}:" + act.path, Summary: act.summary,
			Permission: "crm.loyalty_reward_issue.fulfil", Request: LoyaltyRewardIssueAction{}, Response: LoyaltyRewardIssue{}, Status: http.StatusOK,
			Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyRewardIssueAction) (LoyaltyRewardIssue, error) {
				iid, err := handle.ID(r)
				if err != nil {
					return LoyaltyRewardIssue{}, err
				}
				pid := handle.Property(ctx)
				before, after, err := SetIssueStatus(ctx, tx, pid, iid, act.to, in)
				if err != nil {
					return after, err
				}
				return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_reward_issue",
					EntityID: iid.String(), EntityLabel: after.Number, PropertyID: &pid, Reason: in.Reason, Before: map[string]any{"status": before.Status},
					After: map[string]any{"status": after.Status}})
			})})
	}
	// Top Spender programmes (FR-LOY-P5-05).
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/top-spender-programs/{id}:run", Summary: "Reward / invite the top spenders of a period",
		Permission: "crm.top_spender_program.update", Request: LoyaltyTopSpenderRunInput{}, Response: LoyaltyTopSpenderRun{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyTopSpenderRunInput) (LoyaltyTopSpenderRun, error) {
			pid := handle.Property(ctx)
			prog, err := handle.ID(r)
			if err != nil {
				return LoyaltyTopSpenderRun{}, err
			}
			run, err := m.RunTopSpenderProgram(ctx, tx, ts, pid, prog, in)
			if err != nil {
				return run, err
			}
			return run, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "run", EntityType: "crm.top_spender_program", EntityID: prog.String(),
				EntityLabel: run.ProgramCode + " " + run.Period, PropertyID: &pid, After: run})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/top-spender-program-runs", Summary: "Runs of the Top Spender programmes",
		Permission: "crm.top_spender_program.view", Response: LoyaltyTopSpenderRun{}, List: true, Query: []route.Param{{Name: "filter[programId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTopSpenderRun], error) {
			lp := httpx.ParseList(r)
			var prog *uuid.UUID
			if s := lp.Filters["programId"]; s != "" {
				u, err := uuid.Parse(s)
				if err != nil {
					return httpx.Page[LoyaltyTopSpenderRun]{}, handle.Invalid("filter[programId]", "invalid_uuid", "programId must be a UUID")
				}
				prog = &u
			}
			return handle.Page(TopSpenderRuns(ctx, tx, handle.Property(ctx), prog))
		})})
	// Programme cost against the budget (FR-LOY-P5-06).
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/budget", Summary: "Loyalty programme cost of a month against the budget",
		Permission: "crm.loyalty_budget.view", Response: LoyaltyBudget{}, Query: []route.Param{{Name: "period", Description: "YYYY-MM; default this month"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyBudget, error) {
			return Budget(ctx, tx, handle.Property(ctx), r.URL.Query().Get("period"))
		})})
	m.registerP5Member(reg)
}

// ── Member App (FR-OPS-P5 EP-26: Loyalty tier progress, eligible rewards) ──

// MyLoyaltyProgress is the tier progress of the Member App.
type MyLoyaltyProgress struct {
	Enrolled          bool                `json:"enrolled"`
	Benefits          LoyaltyTierBenefits `json:"benefits"`
	NextTier          *MyLoyaltyTier      `json:"nextTier"`
	NextTierBenefits  *MyTierBenefitInfo  `json:"nextTierBenefits"`
	WindowFrom        string              `json:"windowFrom"`
	WindowTo          string              `json:"windowTo"`
	Spend             string              `json:"spend" doc:"Qualifying spend of the window"`
	Points            int64               `json:"points" doc:"Qualifying points of the window"`
	SpendToNext       string              `json:"spendToNext"`
	PointsToNext      int64               `json:"pointsToNext"`
	ProgressPercent   int                 `json:"progressPercent"`
	NextEvaluationOn  string              `json:"nextEvaluationOn"`
	GraceUntil        *string             `json:"graceUntil"`
	CurrentTierStatus string              `json:"currentTierStatus" enum:"none,qualified,grace"`
}

// MyTierBenefitInfo are the benefits of a tier as the member sees them.
type MyTierBenefitInfo struct {
	PointsMultiplier   string `json:"pointsMultiplier"`
	BookingWindowDays  int    `json:"bookingWindowDays"`
	FnbDiscountPercent string `json:"fnbDiscountPercent"`
	EventAccess        bool   `json:"eventAccess"`
	PriorityService    bool   `json:"priorityService"`
}

func (m *Module) myProgress(ctx context.Context, q dbtx.Querier, c crm.Customer) (MyLoyaltyProgress, error) {
	pol, _, err := LoadTierProgram(ctx, q, c.PropertyID)
	if err != nil {
		return MyLoyaltyProgress{}, err
	}
	today := localToday(ctx, q, c.PropertyID)
	from := today.AddDate(0, -pol.PeriodMonths, 0)
	next := time.Date(today.Year(), time.Month(pol.EvaluationMonth), pol.EvaluationDay, 0, 0, 0, 0, time.UTC)
	if !next.After(today) {
		next = next.AddDate(1, 0, 0)
	}
	out := MyLoyaltyProgress{WindowFrom: from.Format("2006-01-02"), WindowTo: today.Format("2006-01-02"), Spend: "0", SpendToNext: "0",
		NextEvaluationOn: next.Format("2006-01-02"), CurrentTierStatus: "none"}
	b, err := BenefitsOf(ctx, q, c.ID)
	if err != nil {
		return out, err
	}
	out.Benefits, out.Enrolled, out.GraceUntil = b, b.Enrolled, b.GraceUntil
	if !b.Enrolled || b.AccountID == nil {
		return out, nil
	}
	pts, spend, err := tierBasis(ctx, q, c.PropertyID, *b.AccountID, from, today)
	if err != nil {
		return out, err
	}
	out.Points, out.Spend = pts, spend.String()
	rank := -1
	if b.TierID != nil {
		out.CurrentTierStatus = "qualified"
		if b.GraceUntil != nil {
			out.CurrentTierStatus = "grace"
		}
		_ = q.QueryRow(ctx, `SELECT rank FROM crm.loyalty_tiers WHERE id = $1`, *b.TierID).Scan(&rank)
	}
	type nt struct {
		MyLoyaltyTier
		Window int    `db:"booking_window_days"`
		Disc   string `db:"fnb_discount_percent"`
		Event  bool   `db:"event_access"`
		Prio   bool   `db:"priority_service"`
	}
	ts, err := handle.List[nt](q.Query(ctx, `SELECT id, code, name, rank, min_points, trim_scale(min_spend)::text AS min_spend,
		trim_scale(multiplier)::text AS multiplier, benefits, booking_window_days, trim_scale(fnb_discount_percent)::text AS fnb_discount_percent,
		event_access, priority_service FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND rank > $2
		ORDER BY rank, code LIMIT 1`, c.PropertyID, rank))
	if err != nil || len(ts) == 0 {
		if err == nil && b.TierID != nil {
			out.ProgressPercent = 100
		}
		return out, err
	}
	n := ts[0]
	out.NextTier = &n.MyLoyaltyTier
	out.NextTierBenefits = &MyTierBenefitInfo{PointsMultiplier: n.Multiplier, BookingWindowDays: n.Window, FnbDiscountPercent: n.Disc, EventAccess: n.Event,
		PriorityService: n.Prio}
	need := dec(n.MinSpend)
	out.SpendToNext = decimal.Max(decimal.Zero, need.Sub(spend)).String()
	out.PointsToNext = max(0, n.MinPoints-pts)
	pct := 100
	if need.IsPositive() {
		pct = int(spend.Div(need).Mul(decimal.NewFromInt(100)).IntPart())
	} else if n.MinPoints > 0 {
		pct = int(pts * 100 / n.MinPoints)
	}
	out.ProgressPercent = min(100, max(0, pct))
	return out, nil
}

func (m *Module) registerP5Member(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "crm", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/progress", Summary: "My tier, benefits and progress to the next tier",
		Response: MyLoyaltyProgress{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyLoyaltyProgress, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return MyLoyaltyProgress{}, err
			}
			return m.myProgress(ctx, tx, c)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/eligible-rewards", Summary: "Rewards I am eligible for",
		Response: LoyaltyEligibleReward{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyEligibleReward], error) {
			_, a, err := m.myAccount(ctx, tx)
			if err != nil {
				return httpx.Page[LoyaltyEligibleReward]{}, err
			}
			return handle.Page(EligibleRewards(ctx, tx, a))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/loyalty/issued-rewards", Summary: "Rewards given to me (Top Spender, offers)",
		Response: LoyaltyRewardIssue{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyRewardIssue], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[LoyaltyRewardIssue]{}, err
			}
			return handle.Page(RewardIssues(ctx, tx, c.PropertyID, &c.ID, "", "", 100))
		})})
}
