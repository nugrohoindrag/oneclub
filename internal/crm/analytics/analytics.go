// Package analytics is Advanced Segmentation & VIP (EP-17) and CRM & Sales
// Analytics (EP-20) of PRD P5, the P5-owned crm/analytics sub-package (PRD
// P5 §5.4.1): cross-business behaviour (recency / frequency / monetary per
// line), RFM scores and groups (Champions, Loyal, Potential, New, At Risk,
// Lapsed) with their movement between periods, customer lifetime value and
// cohort retention, VIP segmentation (Top Spender, tier, manual) with the
// check-in / POS marker, and NPS, complaint SLA, sales funnel and
// commission analytics.
//
// The scores are computed on schedule into the analytics store tables of
// crm (crm.customer_line_behavior, crm.rfm_scores, crm.vip_customers) from
// the reporting read models, so screens and segments never aggregate the
// transaction tables (FR-SEG-04).
package analytics

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
)

// RFM groups (PRD P5 FR-SEG-02).
var Groups = []string{"champions", "loyal", "potential", "new", "at_risk", "lapsed"}

// Service is the analytics service.
type Service struct {
	DB         *dbtx.DB
	TopSpender *topspender.Service
}

// ── policy ────────────────────────────────────────────────────────────────

// SegmentationPolicy is the "Segmentation, RFM & VIP" club policy.
type SegmentationPolicy struct {
	LookbackDays       int                 `json:"lookbackDays" doc:"Window of frequency and monetary (days)"`
	RecencyBreaks      []int               `json:"recencyBreaks" doc:"Days since the last visit for recency scores 5, 4, 3, 2 (above the last: 1)"`
	LapsedDays         int                 `json:"lapsedDays" doc:"No visit for this long: Lapsed"`
	ExpectedYears      string              `json:"expectedYears" doc:"Expected remaining relationship (years) for the customer lifetime value"`
	VIPTopSpenderRank  int                 `json:"vipTopSpenderRank" doc:"Top N spenders of the last 12 months are VIP (0 = off)"`
	VVIPTopSpenderRank int                 `json:"vvipTopSpenderRank" doc:"Top N spenders are VVIP"`
	VIPTierCodes       []string            `json:"vipTierCodes" doc:"Loyalty tiers whose members are VIP"`
	VIPBenefits        string              `json:"vipBenefits" doc:"Default benefits shown at check-in / POS"`
	VIPHandling        string              `json:"vipHandling" doc:"Default handling note"`
	NPSDrivers         map[string][]string `json:"npsDrivers" doc:"Driver → keywords found in NPS comments"`
	RetentionDays      int                 `json:"retentionDays" doc:"A customer is retained with a visit within N days after the NPS answer"`
}

// DefaultSegmentationPolicy follows PRD P5 §16 #14–15 (Platinum and the top
// 20 spenders are VIP; 60 days without a visit is At Risk, 180 Lapsed).
var DefaultSegmentationPolicy = SegmentationPolicy{LookbackDays: 365, RecencyBreaks: []int{14, 30, 60, 120}, LapsedDays: 180, ExpectedYears: "3",
	VIPTopSpenderRank: 20, VVIPTopSpenderRank: 5, VIPTierCodes: []string{"PLATINUM"},
	VIPBenefits: "Priority tee time, welcome drink, personal greeting", VIPHandling: "Greet by name; inform the duty manager",
	NPSDrivers: map[string][]string{
		"course_condition": {"green", "fairway", "bunker", "rumput", "lapangan", "course"},
		"pace_of_play":     {"pace", "slow", "lambat", "antri", "queue", "wait"},
		"service":          {"service", "staff", "pelayanan", "ramah", "friendly", "caddy"},
		"food_beverage":    {"food", "makanan", "minuman", "drink", "restaurant", "menu"},
		"price":            {"price", "harga", "mahal", "expensive", "value"},
		"facilities":       {"locker", "toilet", "parking", "parkir", "clubhouse", "facility", "fasilitas"},
	},
	RetentionDays: 90}

// PolicyCode is the club policy code.
const PolicyCode = "crm.segmentation"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Campaign Policies", Name: "Segmentation, RFM, VIP & NPS drivers",
		Description: "RFM window, recency breaks, lapsed days, lifetime value horizon, VIP criteria (Top Spender rank, tiers) and handling, NPS driver keywords",
		Default:     DefaultSegmentationPolicy})
}

// LoadPolicy returns the policy in force.
func LoadPolicy(ctx context.Context, q dbtx.Querier, property uuid.UUID) (SegmentationPolicy, error) {
	p, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultSegmentationPolicy)
	if p.LookbackDays <= 0 {
		p.LookbackDays = 365
	}
	if len(p.RecencyBreaks) != 4 {
		p.RecencyBreaks = DefaultSegmentationPolicy.RecencyBreaks
	}
	if p.LapsedDays <= 0 {
		p.LapsedDays = 180
	}
	if p.RetentionDays <= 0 {
		p.RetentionDays = 90
	}
	if p.NPSDrivers == nil {
		p.NPSDrivers = DefaultSegmentationPolicy.NPSDrivers
	}
	return p, err
}

// ── scoring engine (pure) ─────────────────────────────────────────────────

// RecencyScore maps days since the last visit to 5 (most recent) … 1.
func RecencyScore(days int, breaks []int) int {
	for i, b := range breaks {
		if days <= b {
			return 5 - i
		}
	}
	return 1
}

// QuintileScores scores values 1–5 by their rank: the lowest 1, the highest
// 5, evenly in between (ties share the score of their lowest rank; equal
// values always score the same).
func QuintileScores(values []decimal.Decimal) []int {
	n := len(values)
	out := make([]int, n)
	if n == 0 {
		return out
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return values[idx[a]].LessThan(values[idx[b]]) })
	for pos := 0; pos < n; {
		end := pos
		for end+1 < n && values[idx[end+1]].Equal(values[idx[pos]]) {
			end++
		}
		score := 3 // a single value is in the middle
		if n > 1 {
			score = 1 + (pos*4)/(n-1) // lowest 1 … highest 5
		}
		for k := pos; k <= end; k++ {
			out[idx[k]] = score
		}
		pos = end + 1
	}
	return out
}

// RFMGroup classifies scores: Champions (recent, frequent, high spend),
// Loyal (frequent), At Risk (good customers who stopped), New (recent
// first visits), Lapsed (no visit for the lapsed days), Potential.
func RFMGroup(r, f, m, recencyDays, lapsedDays int) string {
	switch {
	case recencyDays > lapsedDays:
		return "lapsed"
	case r >= 4 && f >= 4 && m >= 4:
		return "champions"
	case r >= 3 && (f >= 4 || (f >= 3 && m >= 3)):
		return "loyal"
	case r <= 2 && (f >= 3 || m >= 3):
		return "at_risk"
	case r >= 4 && f <= 2:
		return "new"
	case r <= 1:
		return "lapsed"
	}
	return "potential"
}

// CLV is the customer lifetime value: the spend per year of the window ×
// the expected remaining years.
func CLV(monetary decimal.Decimal, lookbackDays int, years decimal.Decimal) decimal.Decimal {
	if lookbackDays <= 0 || !monetary.IsPositive() {
		return decimal.Zero
	}
	return monetary.Mul(decimal.NewFromInt(365)).Div(decimal.NewFromInt(int64(lookbackDays))).Mul(years).Round(2)
}

// ── refresh (scheduled; FR-SEG-04) ────────────────────────────────────────

// AnalyticsRefresh reports a refresh.
type AnalyticsRefresh struct {
	AsOf        string    `json:"asOf"`
	Customers   int       `json:"customers" doc:"Customers scored"`
	LineRows    int       `json:"lineRows" doc:"Customer × business line behaviour rows"`
	VIP         int       `json:"vip" doc:"Active VIP customers after the refresh"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

type activityRow struct {
	Customer uuid.UUID `db:"customer_id"`
	Line     string    `db:"business_line"`
	Last     time.Time `db:"last_day"`
	Visits   int       `db:"visits"`
	Spend    string    `db:"spend"`
}

func localToday(ctx context.Context, q dbtx.Querier, property uuid.UUID) time.Time {
	loc, err := org.Location(ctx, q, property)
	if err != nil || loc == nil {
		loc = time.UTC
	}
	n := clock.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// Refresh recomputes the behaviour per line, the RFM snapshot of today and
// the automatic VIPs of a property.
func (s *Service) Refresh(ctx context.Context, tx pgx.Tx, property uuid.UUID) (AnalyticsRefresh, error) {
	started := clock.Now()
	pol, err := LoadPolicy(ctx, tx, property)
	if err != nil {
		return AnalyticsRefresh{}, err
	}
	today := localToday(ctx, tx, property)
	since := today.AddDate(0, 0, -max(pol.LookbackDays, pol.LapsedDays*2))
	rows, err := handle.List[activityRow](tx.Query(ctx, `SELECT customer_id, business_line, max(business_date) AS last_day,
		count(DISTINCT business_date) FILTER (WHERE business_date > $3::date - $4::int)::int AS visits,
		coalesce(sum(net_amount) FILTER (WHERE business_date > $3::date - $4::int), 0)::text AS spend
		FROM reporting.eng_folio_lines WHERE property_id = $1 AND customer_id IS NOT NULL AND NOT liability AND business_date >= $2::date
		AND business_date <= $3::date GROUP BY customer_id, business_line`, property, since, today, pol.LookbackDays))
	if err != nil {
		return AnalyticsRefresh{}, err
	}
	// behaviour per line
	if _, err := tx.Exec(ctx, `DELETE FROM crm.customer_line_behavior WHERE property_id = $1`, property); err != nil {
		return AnalyticsRefresh{}, err
	}
	type agg struct {
		last   time.Time
		visits int
		spend  decimal.Decimal
		lines  []string
	}
	per := map[uuid.UUID]*agg{}
	var order []uuid.UUID
	for _, r := range rows {
		days := int(today.Sub(r.Last).Hours() / 24)
		if _, err := tx.Exec(ctx, `INSERT INTO crm.customer_line_behavior (property_id, customer_id, business_line, last_at, recency_days, frequency, monetary)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric)`, property, r.Customer, r.Line, r.Last, days, r.Visits, r.Spend); err != nil {
			return AnalyticsRefresh{}, err
		}
		a := per[r.Customer]
		if a == nil {
			a = &agg{}
			per[r.Customer] = a
			order = append(order, r.Customer)
		}
		if r.Last.After(a.last) {
			a.last = r.Last
		}
		a.spend = a.spend.Add(dec(r.Spend))
		if r.Visits > 0 && !slices.Contains(a.lines, r.Line) {
			a.lines = append(a.lines, r.Line)
		}
	}
	// distinct visit days across lines
	type freq struct {
		Customer uuid.UUID `db:"customer_id"`
		Visits   int       `db:"visits"`
	}
	fs, err := handle.List[freq](tx.Query(ctx, `SELECT customer_id, count(DISTINCT business_date)::int AS visits FROM reporting.eng_folio_lines
		WHERE property_id = $1 AND customer_id IS NOT NULL AND NOT liability AND business_date > $2::date - $3::int AND business_date <= $2::date
		GROUP BY customer_id`, property, today, pol.LookbackDays))
	if err != nil {
		return AnalyticsRefresh{}, err
	}
	for _, f := range fs {
		if a := per[f.Customer]; a != nil {
			a.visits = f.Visits
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].String() < order[j].String() })
	fv := make([]decimal.Decimal, len(order))
	mv := make([]decimal.Decimal, len(order))
	for i, c := range order {
		fv[i] = decimal.NewFromInt(int64(per[c].visits))
		mv[i] = per[c].spend
	}
	fScores, mScores := QuintileScores(fv), QuintileScores(mv)
	years := dec(pol.ExpectedYears)
	if !years.IsPositive() {
		years = decimal.NewFromInt(3)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crm.rfm_scores WHERE property_id = $1 AND as_of = $2`, property, today); err != nil {
		return AnalyticsRefresh{}, err
	}
	for i, c := range order {
		a := per[c]
		days := int(today.Sub(a.last).Hours() / 24)
		r := RecencyScore(days, pol.RecencyBreaks)
		f, m := fScores[i], mScores[i]
		if a.visits == 0 {
			f = 1
		}
		if !a.spend.IsPositive() {
			m = 1
		}
		group := RFMGroup(r, f, m, days, pol.LapsedDays)
		lines := a.lines
		if lines == nil {
			lines = []string{}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm.rfm_scores (property_id, as_of, customer_id, recency_days, frequency, monetary, r_score, f_score, m_score,
			rfm_group, lines, clv) VALUES ($1,$2,$3,$4,$5,$6::numeric,$7,$8,$9,$10,$11,$12::numeric)`, property, today, c, days, a.visits, a.spend.String(),
			r, f, m, group, lines, CLV(a.spend, pol.LookbackDays, years).String()); err != nil {
			return AnalyticsRefresh{}, err
		}
	}
	vip, err := s.refreshVIP(ctx, tx, property, pol, today)
	if err != nil {
		return AnalyticsRefresh{}, err
	}
	out := AnalyticsRefresh{AsOf: today.Format("2006-01-02"), Customers: len(order), LineRows: len(rows), VIP: vip, RefreshedAt: clock.Now()}
	if _, err := tx.Exec(ctx, `INSERT INTO crm.analytics_runs (id, property_id, kind, as_of, rows, started_at, created_by) VALUES ($1,$2,'rfm',$3,$4,$5,$6)`,
		id.New(), property, today, len(order), started, actor(ctx)); err != nil {
		return out, err
	}
	return out, nil
}

// refreshVIP marks the automatic VIPs (Top Spender rank, tier); manual VIPs
// stay as staff set them.
func (s *Service) refreshVIP(ctx context.Context, tx pgx.Tx, property uuid.UUID, pol SegmentationPolicy, today time.Time) (int, error) {
	want := map[uuid.UUID]vipCandidate{}
	if pol.VIPTopSpenderRank > 0 && s.TopSpender != nil {
		list, err := s.TopSpender.Rank(ctx, tx, property, topspender.Filter{From: today.AddDate(-1, 0, 0), To: today, Limit: pol.VIPTopSpenderRank})
		if err != nil {
			return 0, err
		}
		for _, sp := range list {
			if sp.Rank > pol.VIPTopSpenderRank || !dec(sp.Spend).IsPositive() {
				continue
			}
			level := "vip"
			if sp.Rank <= pol.VVIPTopSpenderRank {
				level = "vvip"
			}
			want[sp.CustomerID] = vipCandidate{level, "top_spender", fmt.Sprintf("Top Spender #%d (12 months)", sp.Rank)}
		}
	}
	if len(pol.VIPTierCodes) > 0 {
		rows, err := tx.Query(ctx, `SELECT a.customer_id, t.name FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id
			WHERE a.property_id = $1 AND a.status = 'active' AND t.code = ANY($2::text[])`, property, pol.VIPTierCodes)
		if err != nil {
			return 0, err
		}
		type tr struct {
			c    uuid.UUID
			name string
		}
		var ts []tr
		for rows.Next() {
			var x tr
			if err := rows.Scan(&x.c, &x.name); err != nil {
				rows.Close()
				return 0, err
			}
			ts = append(ts, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		for _, x := range ts {
			if _, ok := want[x.c]; !ok {
				want[x.c] = vipCandidate{"vip", "tier", "Loyalty tier " + x.name}
			}
		}
	}
	// automatic VIPs that no longer qualify
	if _, err := tx.Exec(ctx, `UPDATE crm.vip_customers SET status = 'inactive' WHERE property_id = $1 AND source <> 'manual' AND status = 'active'
		AND NOT (customer_id = ANY($2::uuid[]))`, property, keys(want)); err != nil {
		return 0, err
	}
	for c, w := range want {
		if _, err := tx.Exec(ctx, `INSERT INTO crm.vip_customers (id, property_id, customer_id, level, source, reason, benefits, handling_note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (customer_id) DO UPDATE SET level = EXCLUDED.level, source = EXCLUDED.source,
			reason = EXCLUDED.reason, status = 'active' WHERE crm.vip_customers.source <> 'manual' OR crm.vip_customers.status <> 'active'`,
			id.New(), property, c, w.level, w.source, w.reason, pol.VIPBenefits, pol.VIPHandling); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.vip_customers SET status = 'inactive' WHERE property_id = $1 AND status = 'active' AND valid_until < $2::date`,
		property, today); err != nil {
		return 0, err
	}
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM crm.vip_customers WHERE property_id = $1 AND status = 'active'`, property).Scan(&n)
	return n, err
}

type vipCandidate struct {
	level, source, reason string
}

func keys(m map[uuid.UUID]vipCandidate) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// RefreshAll refreshes every property (daily job).
func (s *Service) RefreshAll(ctx context.Context) error {
	ctx = dbtx.System(ctx)
	var props []uuid.UUID
	if err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM platform.properties WHERE status = 'active' AND archived_at IS NULL ORDER BY id`)
		if err != nil {
			return err
		}
		props, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		return err
	}); err != nil {
		return err
	}
	for _, p := range props {
		if err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := s.Refresh(ctx, tx, p)
			return err
		}); err != nil {
			return fmt.Errorf("crm analytics for %s: %w", p, err)
		}
	}
	return nil
}

// RefreshArgs runs the daily analytics refresh.
type RefreshArgs struct{}

func (RefreshArgs) Kind() string { return "crm_analytics_refresh" }

func (RefreshArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

type refreshWorker struct {
	river.WorkerDefaults[RefreshArgs]
	S *Service
}

func (w *refreshWorker) Work(ctx context.Context, _ *river.Job[RefreshArgs]) error {
	return w.S.RefreshAll(ctx)
}

// RegisterJobs refreshes the analytics store daily (before the segment refresh at 01:30).
func (s *Service) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &refreshWorker{S: s})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 1, Minute: 5, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return RefreshArgs{}, nil }, nil))
}

// ── tags for segments (engagement.Service.Tags) ───────────────────────────

// TagSet returns the customers of an analytics tag: rfm_<group>, vip, vvip,
// multi_line (active in 2+ lines) or line_<business line>.
func TagSet(tag string) func(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]bool, error) {
	return func(ctx context.Context, q dbtx.Querier, property uuid.UUID, _ time.Time) (map[uuid.UUID]bool, error) {
		var sql string
		var arg any
		switch {
		case len(tag) > 4 && tag[:4] == "rfm_":
			sql, arg = `SELECT customer_id FROM crm.rfm_scores s WHERE property_id = $1 AND as_of = (SELECT max(as_of) FROM crm.rfm_scores WHERE property_id = $1)
				AND rfm_group = $2`, tag[4:]
		case tag == "vip" || tag == "vvip":
			sql, arg = `SELECT customer_id FROM crm.vip_customers WHERE property_id = $1 AND status = 'active' AND ($2 = 'vip' OR level = $2)`, tag
		case tag == "multi_line":
			sql, arg = `SELECT customer_id FROM crm.rfm_scores WHERE property_id = $1 AND as_of = (SELECT max(as_of) FROM crm.rfm_scores WHERE property_id = $1)
				AND cardinality(lines) >= 2 AND $2 = ''`, ""
		case len(tag) > 5 && tag[:5] == "line_":
			sql, arg = `SELECT customer_id FROM crm.customer_line_behavior WHERE property_id = $1 AND business_line = $2 AND frequency > 0`, tag[5:]
		default:
			return map[uuid.UUID]bool{}, nil
		}
		rows, err := q.Query(ctx, sql, property, arg)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		out := make(map[uuid.UUID]bool, len(ids))
		for _, x := range ids {
			out[x] = true
		}
		return out, err
	}
}

// Tags are the segmentation tags analytics contributes.
var Tags = []string{"rfm_champions", "rfm_loyal", "rfm_potential", "rfm_new", "rfm_at_risk", "rfm_lapsed", "vip", "vvip", "multi_line",
	"line_golf", "line_pos", "line_sportclub", "line_stay", "line_banquet"}
