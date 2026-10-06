package talent

// Demo data of recruitment and performance review (Modern Golf & Country
// Club): review templates (staff annual review, probation review), two open
// public requisitions — Lifeguard and Waiter / Waitress — with candidates in
// every stage (applied, screening, interview scheduled, offer sent, rejected
// and withdrawn with their retention dates) and an annual review cycle of
// Food & Beverage in progress (self assessment open for the ESS demo
// employee, a review waiting for the F&B Manager, one ready for
// calibration). Idempotent.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/hris"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/reqctx"
)

var demoTemplates = []struct {
	code, name, kind, weight string
	comps, kpis              []ReviewTemplateItem
}{
	{"STAFF-ANNUAL", "Staff Annual Review", "any", "60",
		[]ReviewTemplateItem{
			{Code: "service_excellence", Label: "Service excellence", Description: "Greets, anticipates and resolves member and guest needs", Weight: "3"},
			{Code: "job_knowledge", Label: "Job knowledge", Description: "Knows the standards, products and procedures of the position", Weight: "2"},
			{Code: "teamwork", Label: "Teamwork", Description: "Helps colleagues, shares information, accepts feedback", Weight: "2"},
			{Code: "reliability", Label: "Reliability & discipline", Description: "Punctual, follows the schedule and grooming standards", Weight: "2"},
			{Code: "safety_hygiene", Label: "Safety & hygiene", Description: "Follows K3, food safety and pool / course safety rules", Weight: "1"},
		},
		[]ReviewTemplateItem{
			{Code: "guest_satisfaction", Label: "Guest satisfaction", Description: "Feedback / NPS of the outlet or team", Weight: "1", Target: "NPS ≥ 60"},
			{Code: "attendance", Label: "Attendance", Description: "Days present ÷ scheduled days", Weight: "1", Target: "≥ 97%"},
		}},
	{"PROBATION", "Probation Review", "probation", "100",
		[]ReviewTemplateItem{
			{Code: "job_knowledge", Label: "Job knowledge", Weight: "1"},
			{Code: "attitude", Label: "Attitude & service", Weight: "1"},
			{Code: "learning", Label: "Learning speed", Weight: "1"},
			{Code: "reliability", Label: "Reliability & discipline", Weight: "1"},
		}, nil},
}

type demoCandidate struct {
	name, email, phone, city, education, source string
	exp                                         string
	talent                                      bool
	stage                                       string
	daysAgo                                     int
}

// SeedDemo seeds the recruitment and performance review demo (idempotent).
func SeedDemo(ctx context.Context, tx pgx.Tx, property uuid.UUID) error {
	ctx = reqctx.WithProperty(ctx, property)
	var seeded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.review_templates WHERE property_id = $1 AND code = 'STAFF-ANNUAL')`, property).
		Scan(&seeded); err != nil || seeded {
		return err
	}
	var hasCore bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hris.positions WHERE property_id = $1 AND code = 'LIFEGUARD')`, property).Scan(&hasCore); err != nil ||
		!hasCore {
		return err
	}
	day := today(ctx, tx, property)
	for _, t := range demoTemplates {
		comps, _ := json.Marshal(t.comps)
		kpis, _ := json.Marshal(nonNilItems(t.kpis))
		if _, err := tx.Exec(ctx, `INSERT INTO hris.review_templates (id, property_id, code, name, review_type, competencies, kpis, competency_weight, description)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9)`, id.New(), property, t.code, t.name, t.kind, comps, kpis, t.weight,
			"Demo template ("+t.name+")"); err != nil {
			return fmt.Errorf("review template %s: %w", t.code, err)
		}
	}
	emp := func(no string) (uuid.UUID, error) {
		var e uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM hris.employees WHERE property_id = $1 AND employee_no = $2`, property, no).Scan(&e)
		return e, err
	}
	reqs := []struct {
		title, position, manager, location, desc, reqs string
		headcount                                      int
		cands                                          []demoCandidate
	}{
		{"Lifeguard", "LIFEGUARD", "EMP-00004", "Sport Club pool",
			"Keep swimmers safe at the Sport Club pools; rescue and first aid, pool rules and water quality checks with the Sport Club team.",
			"Lifeguard certificate (Balawista) and CPR / BLS, or willing to certify; good swimmer; shift work incl. weekends.", 2,
			[]demoCandidate{
				{"Rina Wulandari", "rina.wulandari@example.com", "081234500101", "Tangerang", "sma", "website", "1", true, "applied", 2},
				{"Taufik Hidayat", "taufik.h@example.com", "081234500102", "Tangerang Selatan", "d3", "job_portal", "3", false, "screening", 6},
				{"Yusuf Maulana", "yusuf.maulana@example.com", "081234500103", "Jakarta Barat", "sma", "referral", "2", true, "interview", 9},
			}},
		{"Waiter / Waitress", "WAITER", "EMP-00005", "Clubhouse restaurant",
			"Serve members and guests at the clubhouse restaurant, terrace and banquet events to the club's service standards.",
			"Min. SMA / SMK hospitality; 1 year F&B service; friendly, well-groomed; basic English; food handler certificate is a plus.", 3,
			[]demoCandidate{
				{"Dian Puspita", "dian.puspita@example.com", "081234500201", "Tangerang", "d3", "website", "2", true, "offered", 18},
				{"Fikri Ananda", "fikri.ananda@example.com", "081234500202", "Serpong", "sma", "walk_in", "0.5", false, "rejected", 25},
				{"Lestari Handayani", "lestari.h@example.com", "081234500203", "Bogor", "s1", "website", "4", true, "withdrawn", 30},
			}},
	}
	m := &Module{}
	for i, rq := range reqs {
		var unit, pos uuid.UUID
		var grade *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT org_unit_id, id, grade_id FROM hris.positions WHERE property_id = $1 AND code = $2`, property, rq.position).
			Scan(&unit, &pos, &grade); err != nil {
			return fmt.Errorf("position %s: %w", rq.position, err)
		}
		mgr, err := emp(rq.manager)
		if err != nil {
			return err
		}
		no, err := yearlyNumber(ctx, tx, property, "REQ", day.Year())
		if err != nil {
			return err
		}
		rid := id.New()
		approved := day.AddDate(0, 0, -35+i*5)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.job_requisitions (id, property_id, number, title, org_unit_id, position_id, grade_id, hiring_manager_id,
			headcount, reason, contract_type, salary_min, salary_max, target_start_date, location, description, requirements, is_public, status, submitted_at,
			approved_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'new_position','pkwt',5000000,6500000,$10,$11,$12,$13,true,'open',$14,$14)`,
			rid, property, no, rq.title, unit, pos, grade, mgr, rq.headcount, day.AddDate(0, 0, 30), rq.location, rq.desc, rq.reqs, approved); err != nil {
			return fmt.Errorf("requisition %s: %w", rq.title, err)
		}
		for _, c := range rq.cands {
			if err := seedCandidate(ctx, tx, m, property, rid, pos, grade, unit, mgr, day, c); err != nil {
				return fmt.Errorf("candidate %s: %w", c.name, err)
			}
		}
	}
	return seedCycle(ctx, tx, m, property, day, emp)
}

func nonNilItems(in []ReviewTemplateItem) []ReviewTemplateItem {
	if in == nil {
		return []ReviewTemplateItem{}
	}
	return in
}

func seedCandidate(ctx context.Context, tx pgx.Tx, m *Module, property, rid, pos uuid.UUID, grade *uuid.UUID, unit, mgr uuid.UUID, day time.Time,
	c demoCandidate) error {
	at := clock.Now().AddDate(0, 0, -c.daysAgo)
	cid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.candidates (id, property_id, full_name, email, phone, city, education, experience_years, source, consent_at,
		talent_pool_consent, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9,$10,$11,$10)`, cid, property, c.name, c.email, c.phone, c.city, c.education,
		c.exp, c.source, at, c.talent); err != nil {
		return err
	}
	no, err := yearlyNumber(ctx, tx, property, "APP", day.Year())
	if err != nil {
		return err
	}
	aid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO hris.applications (id, property_id, number, requisition_id, candidate_id, source, applied_at, stage_changed_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$7)`, aid, property, no, rid, cid, c.source, at); err != nil {
		return err
	}
	event := func(from *string, to string, when time.Time, note string) error {
		_, err := tx.Exec(ctx, `INSERT INTO hris.application_stage_events (id, property_id, application_id, from_stage, to_stage, note, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, id.New(), property, aid, from, to, note, when)
		return err
	}
	if err := event(nil, "applied", at, "Applied via "+c.source); err != nil {
		return err
	}
	path := map[string][]string{"applied": nil, "screening": {"screening"}, "interview": {"screening", "interview"},
		"offered": {"screening", "interview", "offered"}, "rejected": {"screening", "rejected"}, "withdrawn": {"screening", "interview", "withdrawn"}}[c.stage]
	prev := "applied"
	when := at
	for _, s := range path {
		when = when.Add(48 * time.Hour)
		p := prev
		if err := event(&p, s, when, "Demo: "+strings.ReplaceAll(s, "_", " ")); err != nil {
			return err
		}
		prev = s
	}
	if _, err := tx.Exec(ctx, `UPDATE hris.applications SET stage = $2, stage_changed_at = $3, screening_score = CASE WHEN $2 <> 'applied' THEN 4 END
		WHERE id = $1`, aid, c.stage, when); err != nil {
		return err
	}
	loc := location(ctx, tx, property)
	switch c.stage {
	case "interview":
		start := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, loc).AddDate(0, 0, 1)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.interviews (id, property_id, application_id, round, interview_type, scheduled_at, duration_minutes, location,
			interviewer_ids) VALUES ($1,$2,$3,1,'practical',$4,60,'Sport Club pool',$5)`, id.New(), property, aid, start.UTC(), []uuid.UUID{mgr}); err != nil {
			return err
		}
	case "offered", "withdrawn":
		iid := id.New()
		held := when.Add(-24 * time.Hour)
		if _, err := tx.Exec(ctx, `INSERT INTO hris.interviews (id, property_id, application_id, round, interview_type, scheduled_at, location, interviewer_ids,
			status, result, score, completed_at) VALUES ($1,$2,$3,1,'onsite',$4,'Clubhouse restaurant',$5,'completed','pass',4.2,$4)`, iid, property, aid, held,
			[]uuid.UUID{mgr}); err != nil {
			return err
		}
		scores := `[{"code":"job_knowledge","score":"4"},{"code":"service_attitude","score":"5"},{"code":"communication","score":"4"},` +
			`{"code":"teamwork","score":"4"},{"code":"culture_fit","score":"4"}]`
		var mgrUser *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT id FROM platform.users WHERE employee_id = $1`, mgr).Scan(&mgrUser)
		u := id.New()
		if mgrUser != nil {
			u = *mgrUser
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.interview_scorecards (id, property_id, interview_id, interviewer_id, interviewer_user_id, scores, overall_score,
			recommendation, comments) VALUES ($1,$2,$3,$4,$5,$6,4.2,'yes','Warm service attitude, ready for the terrace shift')`, id.New(), property, iid, mgr, u,
			scores); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.applications SET rating = 4.2 WHERE id = $1`, aid); err != nil {
			return err
		}
	}
	switch c.stage {
	case "offered":
		no, err := yearlyNumber(ctx, tx, property, "OFR", day.Year())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hris.job_offers (id, property_id, number, application_id, org_unit_id, position_id, grade_id, job_title, contract_type,
			start_date, end_date, base_salary, allowances, status, submitted_at, approved_at, sent_at, expires_on)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'Waiter','pkwt',$8,$9,5500000,'[{"code":"MEAL","name":"Meal allowance","amount":"400000"}]','sent',$10,$10,$10,$11)`,
			id.New(), property, no, aid, unit, pos, grade, day.AddDate(0, 0, 14), day.AddDate(1, 0, 13), when, day.AddDate(0, 0, 5)); err != nil {
			return err
		}
	case "rejected", "withdrawn":
		reason := "Not enough F&B service experience"
		if c.stage == "withdrawn" {
			reason = "Accepted another offer"
		}
		if _, err := tx.Exec(ctx, `UPDATE hris.applications SET closed_at = $2, closed_stage = $4, close_reason = $3 WHERE id = $1`, aid, when,
			reason, path[len(path)-2]); err != nil {
			return err
		}
		if _, err := m.startRetention(ctx, tx, property, cid); err != nil {
			return err
		}
		// The demo dates the retention from the closing day.
		if _, err := tx.Exec(ctx, `UPDATE hris.candidates SET retention_until = retention_until - $2::int WHERE id = $1`, cid,
			int(clock.Now().Sub(when).Hours()/24)); err != nil {
			return err
		}
	}
	return nil
}

// seedCycle opens the annual review of Food & Beverage.
func seedCycle(ctx context.Context, tx pgx.Tx, m *Module, property uuid.UUID, day time.Time, emp func(string) (uuid.UUID, error)) error {
	var unit uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM hris.org_units WHERE property_id = $1 AND code = 'FNB'`, property).Scan(&unit); err != nil {
		return err
	}
	var tpl uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM hris.review_templates WHERE property_id = $1 AND code = 'STAFF-ANNUAL'`, property).Scan(&tpl); err != nil {
		return err
	}
	cid := id.New()
	code := fmt.Sprintf("FNB-ANNUAL-%d", day.Year())
	start := time.Date(day.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(day.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(ctx, `INSERT INTO hris.review_cycles (id, property_id, code, name, cycle_type, period_start, period_end, self_due, manager_due,
		calibration_due, org_unit_id, default_template_id, notes) VALUES ($1,$2,$3,$4,'annual',$5,$6,$7,$8,$9,$10,$11,'Demo cycle')`, cid, property, code,
		fmt.Sprintf("F&B Annual Review %d", day.Year()), start, end, day.AddDate(0, 0, 7), day.AddDate(0, 0, 21), day.AddDate(0, 0, 30), unit, tpl); err != nil {
		return err
	}
	c, err := lockCycle(ctx, tx, cid)
	if err != nil {
		return err
	}
	if _, err := m.Launch(ctx, tx, c, nil); err != nil {
		return err
	}
	scoreAll := func(no string, self, manager bool, value string) error {
		e, err := emp(no)
		if err != nil {
			return err
		}
		var rid uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM hris.performance_reviews WHERE cycle_id = $1 AND employee_id = $2`, cid, e).Scan(&rid); err != nil {
			return nil //nolint:nilerr // not reviewed in this cycle
		}
		if self {
			if _, err := tx.Exec(ctx, `UPDATE hris.review_scores SET self_score = $2::numeric, self_comment = 'Demo self assessment' WHERE review_id = $1`, rid,
				value); err != nil {
				return err
			}
			s, _, err := reviewScore(ctx, tx, rid, false)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'manager_review', self_score = $2::numeric, self_submitted_at = now(),
				self_comment = 'I improved my upselling and helped train new waiters.' WHERE id = $1`, rid, decStr(s)); err != nil {
				return err
			}
		}
		if manager {
			if _, err := tx.Exec(ctx, `UPDATE hris.review_scores SET manager_score = CASE WHEN item_kind = 'kpi' THEN 4 ELSE $2::numeric END,
				manager_comment = 'Demo manager review', actual = CASE code WHEN 'attendance' THEN '98%' WHEN 'guest_satisfaction' THEN 'NPS 64' END
				WHERE review_id = $1`, rid, value); err != nil {
				return err
			}
			s, _, err := reviewScore(ctx, tx, rid, true)
			if err != nil {
				return err
			}
			pc, _, err := hris.LoadPerformanceConfiguration(ctx, tx, property, clock.Now())
			if err != nil {
				return err
			}
			band := pc.Band(*s)
			if _, err := tx.Exec(ctx, `UPDATE hris.performance_reviews SET status = 'submitted', manager_score = $2::numeric, manager_submitted_at = now(),
				recommended_rating = $3, strengths = 'Reliable on busy weekends; members ask for her by name.',
				improvements = 'Wine knowledge for the new menu.', goals = 'Food handler certificate; train two new waiters.' WHERE id = $1`, rid,
				decStr(s), band.Code); err != nil {
				return err
			}
		}
		return nil
	}
	if err := scoreAll("EMP-00024", true, false, "4"); err != nil {
		return err
	}
	return scoreAll("EMP-00025", true, true, "4")
}
