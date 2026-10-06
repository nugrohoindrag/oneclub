package loyalty

// PRD P5 FR-LOY-P5-05 Top Spender driven engagement: per programme the top
// N customers of each period (month, quarter, year) by Top Spender rank
// receive a reward and / or an invitation (static segment + message), once
// per programme and period, within the loyalty budget.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/provision"
	"oneclub/internal/platform/resource"
)

// TopSpenderPrograms is the Top Spender Programme master.
var TopSpenderPrograms = &resource.Def{
	Key: "crm.top_spender_program", Module: "crm", Perm: "crm.top_spender_program", Path: "/api/v1/crm/loyalty/top-spender-programs",
	Table: "crm.top_spender_programs", Name: "Top Spender Programme", Plural: "Top Spender Programmes", Tag: "Loyalty", PropertyScoped: true, Archive: true,
	CodeField: "code", OrderBy: "code, id", SchemaName: "LoyaltyTopSpenderProgram",
	Fields: []resource.Field{resource.Code("Code"), resource.Name(),
		{Name: "periodType", Column: "period_type", Label: "Period", Kind: resource.Enum, Enum: []string{"month", "quarter", "year"}, Default: "month"},
		{Name: "topN", Column: "top_n", Label: "Top N", Kind: resource.Int, Default: int64(10), Min: resource.Min(1), MaxN: resource.Max(500)},
		{Name: "businessLine", Column: "business_line", Label: "Business Line (empty = all)", Kind: resource.Enum, Enum: businessLines},
		{Name: "rewardId", Column: "reward_id", Label: "Reward", Kind: resource.UUID, Ref: &resource.Ref{Table: "crm.loyalty_rewards", SameProperty: true, Label: "reward"}},
		{Name: "invitationSegmentId", Column: "invitation_segment_id", Label: "Invitation List (static segment)", Kind: resource.UUID,
			Ref: &resource.Ref{Table: "crm.segments", SameProperty: true, Label: "segment"}},
		{Name: "invitationMessage", Column: "invitation_message", Label: "Invitation Message ({{.name}}, {{.rank}}, {{.period}})", Kind: resource.Text, Max: 2000},
		resource.Status("active", "inactive")},
}

func init() {
	TopSpenderPrograms.Hooks.BeforeWrite = func(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
		rw, _ := merged(v, before, "rewardId").(string)
		sg, _ := merged(v, before, "invitationSegmentId").(string)
		if rw == "" && sg == "" {
			return errs.Validation("action_required", "choose a reward, an invitation list or both",
				errs.Field("rewardId", "required", "reward or invitation list"))
		}
		if s, ok := v["invitationSegmentId"].(string); ok && s != "" {
			var typ string
			if err := tx.QueryRow(ctx, `SELECT segment_type FROM crm.segments WHERE id = $1::uuid`, s).Scan(&typ); err != nil && !dbtx.IsNoRows(err) {
				return err
			}
			if typ != "" && typ != "static" {
				return handle.Invalid("invitationSegmentId", "segment_not_static", "the invitation list must be a static segment")
			}
		}
		return nil
	}
}

// P5Templates are the notification templates of the P5 loyalty features.
func P5Templates() []provision.Template {
	t := map[string]map[string][2]string{
		"crm.loyalty_tier_grace": {
			"en": {"Keep your {{.tier}} tier", "Hello {{.name}}, your spend of the last period is below the {{.tier}} threshold. You keep {{.tier}} until {{.date}}; reach the threshold before then to stay."},
			"id": {"Pertahankan tier {{.tier}} Anda", "Halo {{.name}}, transaksi Anda pada periode lalu di bawah ambang {{.tier}}. Tier {{.tier}} tetap berlaku sampai {{.date}}; capai ambangnya sebelum tanggal tersebut."},
		},
		"crm.loyalty_reward_issued": {
			"en": {"A reward for you: {{.reward}}", "Hello {{.name}}, you received {{.reward}} ({{.number}}).{{if .code}} Your code: {{.code}}.{{end}}"},
			"id": {"Hadiah untuk Anda: {{.reward}}", "Halo {{.name}}, Anda menerima {{.reward}} ({{.number}}).{{if .code}} Kode Anda: {{.code}}.{{end}}"},
		},
		"crm.loyalty_top_spender_invitation": {
			"en": {"You are invited, {{.name}}", "{{.message}}"},
			"id": {"Anda diundang, {{.name}}", "{{.message}}"},
		},
	}
	var out []provision.Template
	for ev, locs := range t {
		for loc, c := range locs {
			for _, ch := range []string{"in_app", "email", "whatsapp"} {
				out = append(out, provision.Template{Event: ev, Channel: ch, Locale: loc, Subject: c[0], Body: c[1]})
			}
		}
	}
	return out
}

// PeriodRange parses a programme period: YYYY-MM, YYYY-Qn or YYYY. An empty
// period is the last complete period of the type before today.
func PeriodRange(periodType, period string, today time.Time) (string, time.Time, time.Time, error) {
	bad := handle.Invalid("period", "invalid_period", "period must be YYYY-MM, YYYY-Qn or YYYY")
	if strings.TrimSpace(period) == "" {
		switch periodType {
		case "quarter":
			q := (int(today.Month())-1)/3 + 1
			y := today.Year()
			if q--; q == 0 {
				q, y = 4, y-1
			}
			period = fmt.Sprintf("%d-Q%d", y, q)
		case "year":
			period = strconv.Itoa(today.Year() - 1)
		default:
			period = today.AddDate(0, 0, -today.Day()).Format("2006-01")
		}
	}
	switch {
	case len(period) == 7 && period[4] == '-' && period[5] != 'Q':
		from, err := time.Parse("2006-01", period)
		if err != nil {
			return "", time.Time{}, time.Time{}, bad
		}
		return period, from, from.AddDate(0, 1, -1), nil
	case len(period) == 7 && period[5] == 'Q':
		y, err := strconv.Atoi(period[:4])
		q, err2 := strconv.Atoi(period[6:])
		if err != nil || err2 != nil || q < 1 || q > 4 {
			return "", time.Time{}, time.Time{}, bad
		}
		from := time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
		return period, from, from.AddDate(0, 3, -1), nil
	case len(period) == 4:
		y, err := strconv.Atoi(period)
		if err != nil {
			return "", time.Time{}, time.Time{}, bad
		}
		from := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		return period, from, from.AddDate(1, 0, -1), nil
	}
	return "", time.Time{}, time.Time{}, bad
}

// LoyaltyTopSpenderRun is one run of a programme for a period.
type LoyaltyTopSpenderRun struct {
	ID            uuid.UUID `json:"id" db:"id"`
	ProgramID     uuid.UUID `json:"programId" db:"program_id"`
	ProgramCode   string    `json:"programCode" db:"program_code"`
	Period        string    `json:"period" db:"period"`
	PeriodFrom    string    `json:"periodFrom" db:"period_from"`
	PeriodTo      string    `json:"periodTo" db:"period_to"`
	Ranked        int       `json:"ranked" db:"ranked"`
	Issued        int       `json:"issued" db:"issued"`
	Invited       int       `json:"invited" db:"invited"`
	SkippedBudget int       `json:"skippedBudget" db:"skipped_budget"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
	Created       bool      `json:"created" db:"-" doc:"false: the period had already been run"`
}

// LoyaltyTopSpenderRunInput runs a programme.
type LoyaltyTopSpenderRunInput struct {
	Period string `json:"period,omitempty" doc:"YYYY-MM, YYYY-Qn or YYYY (default: the last complete period)"`
}

const tsRunSelect = `SELECT r.id, r.program_id, p.code AS program_code, r.period, to_char(r.period_from, 'YYYY-MM-DD') AS period_from,
	to_char(r.period_to, 'YYYY-MM-DD') AS period_to, r.ranked, r.issued, r.invited, r.skipped_budget, r.created_at
	FROM crm.top_spender_program_runs r JOIN crm.top_spender_programs p ON p.id = r.program_id`

// TopSpenderRuns lists the runs of a property (optionally of one programme).
func TopSpenderRuns(ctx context.Context, q dbtx.Querier, property uuid.UUID, program *uuid.UUID) ([]LoyaltyTopSpenderRun, error) {
	return handle.List[LoyaltyTopSpenderRun](q.Query(ctx, tsRunSelect+` WHERE r.property_id = $1 AND ($2::uuid IS NULL OR r.program_id = $2)
		ORDER BY r.created_at DESC LIMIT 200`, property, program))
}

// RunTopSpenderProgram rewards / invites the top N of a period once.
func (m *Module) RunTopSpenderProgram(ctx context.Context, tx pgx.Tx, ts *topspender.Service, property, program uuid.UUID, in LoyaltyTopSpenderRunInput) (LoyaltyTopSpenderRun, error) {
	var p struct {
		Code, Name, PeriodType, Status string
		TopN                           int
		Line, Message                  *string
		Reward, Segment                *uuid.UUID
	}
	err := tx.QueryRow(ctx, `SELECT code, name, period_type, status, top_n, business_line, invitation_message, reward_id, invitation_segment_id
		FROM crm.top_spender_programs WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`, program, property).
		Scan(&p.Code, &p.Name, &p.PeriodType, &p.Status, &p.TopN, &p.Line, &p.Message, &p.Reward, &p.Segment)
	if dbtx.IsNoRows(err) {
		return LoyaltyTopSpenderRun{}, errs.NotFound("top spender programme")
	}
	if err != nil {
		return LoyaltyTopSpenderRun{}, err
	}
	if p.Status != "active" {
		return LoyaltyTopSpenderRun{}, errs.Conflict("programme_inactive", "programme "+p.Code+" is inactive")
	}
	period, from, to, err := PeriodRange(p.PeriodType, in.Period, localToday(ctx, tx, property))
	if err != nil {
		return LoyaltyTopSpenderRun{}, err
	}
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM crm.top_spender_program_runs WHERE program_id = $1 AND period = $2`, program, period).Scan(&existing)
	if err == nil {
		rows, err := tx.Query(ctx, tsRunSelect+` WHERE r.id = $1`, existing)
		return handle.One[LoyaltyTopSpenderRun](rows, err, "programme run")
	}
	if !dbtx.IsNoRows(err) {
		return LoyaltyTopSpenderRun{}, err
	}
	if ts == nil {
		return LoyaltyTopSpenderRun{}, errs.Unavailable("top spender ranking is not wired")
	}
	f := topspender.Filter{From: from, To: to, Limit: p.TopN}
	if p.Line != nil {
		f.BusinessLine = *p.Line
	}
	list, err := ts.Rank(ctx, tx, property, f)
	if err != nil {
		return LoyaltyTopSpenderRun{}, err
	}
	rid := id.New()
	run := LoyaltyTopSpenderRun{ID: rid, ProgramID: program, ProgramCode: p.Code, Period: period, PeriodFrom: from.Format("2006-01-02"),
		PeriodTo: to.Format("2006-01-02"), Created: true}
	for _, sp := range list {
		if sp.Rank > p.TopN {
			break
		}
		run.Ranked++
		if p.Reward != nil {
			pid := program
			_, status, err := m.IssueReward(ctx, tx, IssueRequest{PropertyID: property, CustomerID: sp.CustomerID, RewardID: *p.Reward, Source: "top_spender",
				SourceID: &pid, SourceRef: fmt.Sprintf("%s #%d %s", p.Code, sp.Rank, period), Period: period,
				Key: "tsp:" + program.String() + ":" + period + ":" + sp.CustomerID.String(), EnforceBudget: true})
			if err != nil {
				return run, err
			}
			switch status {
			case IssueCreated, IssueExisting:
				run.Issued++
			case IssueSkippedBudget:
				run.SkippedBudget++
			}
		}
		if p.Segment != nil {
			tag, err := tx.Exec(ctx, `INSERT INTO crm.segment_members (segment_id, property_id, customer_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				*p.Segment, property, sp.CustomerID)
			if err != nil {
				return run, err
			}
			run.Invited++
			if tag.RowsAffected() > 0 && p.Message != nil && strings.TrimSpace(*p.Message) != "" {
				msg := strings.NewReplacer("{{.name}}", sp.Name, "{{.rank}}", strconv.Itoa(sp.Rank), "{{.period}}", period).Replace(*p.Message)
				if err := m.notifyCustomer(ctx, tx, property, sp.CustomerID, "crm.loyalty_top_spender_invitation", map[string]any{"message": msg,
					"rank": sp.Rank, "period": period}); err != nil {
					return run, err
				}
			}
		}
	}
	if p.Segment != nil {
		if _, err := tx.Exec(ctx, `UPDATE crm.segments SET member_count = (SELECT count(*) FROM crm.segment_members WHERE segment_id = $1), computed_at = now()
			WHERE id = $1`, *p.Segment); err != nil {
			return run, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.top_spender_program_runs (id, property_id, program_id, period, period_from, period_to, ranked, issued, invited,
		skipped_budget, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, rid, property, program, period, from, to, run.Ranked, run.Issued, run.Invited,
		run.SkippedBudget, actor(ctx)); err != nil {
		return run, err
	}
	rows, err := tx.Query(ctx, tsRunSelect+` WHERE r.id = $1`, rid)
	out, err := handle.One[LoyaltyTopSpenderRun](rows, err, "programme run")
	out.Created = true
	return out, err
}

// ── jobs ──────────────────────────────────────────────────────────────────

// P5DailyArgs runs the tier programme and the Top Spender programmes.
type P5DailyArgs struct{}

func (P5DailyArgs) Kind() string { return "crm_loyalty_p5_daily" }

func (P5DailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// P5DailyWorker is the daily worker of the P5 loyalty features.
type P5DailyWorker struct {
	river.WorkerDefaults[P5DailyArgs]
	M  *Module
	TS *topspender.Service
}

func (w *P5DailyWorker) Work(ctx context.Context, _ *river.Job[P5DailyArgs]) error {
	if err := w.M.RunTierProgramDaily(ctx); err != nil {
		return err
	}
	return w.M.RunTopSpenderProgramsDue(ctx, w.TS)
}

// RunTopSpenderProgramsDue runs every active programme for its last
// complete period (once per period).
func (m *Module) RunTopSpenderProgramsDue(ctx context.Context, ts *topspender.Service) error {
	ctx = dbtx.System(ctx)
	type prog struct {
		ID       uuid.UUID `db:"id"`
		Property uuid.UUID `db:"property_id"`
	}
	var list []prog
	if err := m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		list, err = handle.List[prog](tx.Query(ctx, `SELECT id, property_id FROM crm.top_spender_programs WHERE status = 'active' AND archived_at IS NULL
			ORDER BY id`))
		return err
	}); err != nil {
		return err
	}
	for _, p := range list {
		if err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := m.RunTopSpenderProgram(ctx, tx, ts, p.Property, p.ID, LoyaltyTopSpenderRunInput{})
			return err
		}); err != nil {
			return fmt.Errorf("top spender programme %s: %w", p.ID, err)
		}
	}
	return nil
}

// RegisterP5Jobs adds the daily worker of the P5 loyalty features.
func (m *Module) RegisterP5Jobs(reg *jobs.Registrar, ts *topspender.Service, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &P5DailyWorker{M: m, TS: ts})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 40, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return P5DailyArgs{}, nil }, nil))
}
