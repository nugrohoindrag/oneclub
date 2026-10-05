// Package topspender is Top Spender & Leaderboard of PRD P3 (EP-08), the
// P3-owned crm/topspender sub-package: ranking of customers by spend from
// folios (net charges − refunds − credit notes) per period and business
// line, filters (member type, outlet, segment), leaderboards of rounds and
// activity, the monthly rank snapshot and the VIP list action (static
// segment). Internal only (FR-TOP-06): never exposed to members or the web.
package topspender

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/org"
	"oneclub/internal/platform/rules"
)

// Service serves Top Spender.
type Service struct {
	DB *dbtx.DB
}

// Policy is how Top Spender counts spend (FR-TOP-07).
type Policy struct {
	ExcludePointsPaid  bool `json:"excludePointsPaid" doc:"Spend paid with loyalty points does not count"`
	ExcludeVoucherPaid bool `json:"excludeVoucherPaid" doc:"Spend paid with Voucher & Prepaid does not count (bought earlier)"`
	SnapshotSize       int  `json:"snapshotSize" doc:"Customers kept in the monthly rank snapshot"`
}

// DefaultPolicy counts every charge; points / voucher payments are spend.
var DefaultPolicy = Policy{SnapshotSize: 100}

// PolicyCode is the club policy code.
const PolicyCode = "crm.top_spender"

func init() {
	rules.RegisterPolicy(rules.PolicyDef{Code: PolicyCode, Category: "Loyalty Policies", Name: "Top Spender spend rules",
		Description: "Whether spend paid with loyalty points or Voucher & Prepaid counts, size of the monthly rank snapshot", Default: DefaultPolicy})
}

// Lines are the spend columns (FR-TOP-02).
var Lines = []string{"golf", "pos", "sportclub", "stay", "banquet"}

// Filter selects the ranking.
type Filter struct {
	From, To     time.Time
	BusinessLine string // golf | pos | sportclub | stay | banquet | …
	MemberType   string // member | non_member | corporate
	OutletID     *uuid.UUID
	SegmentID    *uuid.UUID
	Limit        int
}

// TopSpender is one ranked customer.
type TopSpender struct {
	Rank         int       `json:"rank" db:"rank"`
	PreviousRank *int      `json:"previousRank" db:"-" doc:"Rank in the previous month's snapshot (monthly periods)"`
	CustomerID   uuid.UUID `json:"customerId" db:"customer_id"`
	Code         string    `json:"code" db:"code"`
	Name         string    `json:"name" db:"name"`
	CustomerType string    `json:"customerType" db:"customer_type"`
	Member       bool      `json:"member" db:"member"`
	Corporate    bool      `json:"corporate" db:"corporate"`
	Charges      string    `json:"charges" db:"charges" doc:"Net charges of the period"`
	Refunds      string    `json:"refunds" db:"refunds"`
	Credits      string    `json:"credits" db:"credits" doc:"Credit notes"`
	Excluded     string    `json:"excluded" db:"excluded" doc:"Points / voucher payments excluded by the Top Spender policy"`
	Spend        string    `json:"spend" db:"spend" doc:"Charges − refunds − credit notes − excluded"`
	Golf         string    `json:"golf" db:"golf"`
	FnB          string    `json:"fnb" db:"pos"`
	Sport        string    `json:"sport" db:"sportclub"`
	Bungalow     string    `json:"bungalow" db:"stay"`
	Banquet      string    `json:"banquet" db:"banquet"`
	Other        string    `json:"other" db:"other"`
	Visits       int       `json:"visits" db:"visits" doc:"Days with charges"`
}

func loc(ctx context.Context, q dbtx.Querier, property uuid.UUID) *time.Location {
	l, err := org.Location(ctx, q, property)
	if err != nil || l == nil {
		return time.UTC
	}
	return l
}

// Rank ranks the customers of a property by spend.
func (s *Service) Rank(ctx context.Context, q dbtx.Querier, property uuid.UUID, f Filter) ([]TopSpender, error) {
	pol, _, err := rules.PolicyAt(ctx, q, PolicyCode, property, DefaultPolicy)
	if err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var excl []string
	if pol.ExcludePointsPaid {
		excl = append(excl, "loyalty_points")
	}
	if pol.ExcludeVoucherPaid {
		excl = append(excl, "voucher_prepaid")
	}
	outlet := ""
	if f.OutletID != nil {
		outlet = f.OutletID.String()
	}
	seg := ""
	if f.SegmentID != nil {
		seg = f.SegmentID.String()
	}
	tz := loc(ctx, q, property).String()
	rows, err := q.Query(ctx, `WITH fl AS (
		  SELECT l.customer_id, l.business_line, l.net_amount, (l.posted_at AT TIME ZONE $4)::date AS day
		  FROM reporting.eng_folio_lines l
		  WHERE l.property_id = $1 AND l.customer_id IS NOT NULL AND NOT l.liability
		  AND (l.posted_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date
		  AND ($5 = '' OR l.business_line = $5) AND ($6 = '' OR l.outlet_id::text = $6)),
		ch AS (SELECT customer_id, business_line, sum(net_amount) AS net FROM fl GROUP BY 1, 2),
		vd AS (SELECT customer_id, count(DISTINCT day) AS days FROM fl GROUP BY 1),
		agg AS (SELECT ch.customer_id, sum(net) AS net, max(vd.days) AS days,
		  sum(net) FILTER (WHERE business_line = 'golf') AS golf, sum(net) FILTER (WHERE business_line = 'pos') AS pos,
		  sum(net) FILTER (WHERE business_line = 'sportclub') AS sportclub, sum(net) FILTER (WHERE business_line = 'stay') AS stay,
		  sum(net) FILTER (WHERE business_line = 'banquet') AS banquet,
		  sum(net) FILTER (WHERE business_line NOT IN ('golf', 'pos', 'sportclub', 'stay', 'banquet')) AS other
		  FROM ch JOIN vd ON vd.customer_id = ch.customer_id GROUP BY 1),
		rf AS (SELECT r.customer_id, sum(r.amount) AS amount FROM reporting.eng_refunds r LEFT JOIN reporting.eng_folios f ON f.folio_id = r.folio_id
		  WHERE r.property_id = $1 AND r.status = 'completed' AND r.customer_id IS NOT NULL AND $6 = ''
		  AND (r.processed_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date AND ($5 = '' OR f.business_line = $5) GROUP BY 1),
		cn AS (SELECT customer_id, sum(amount) AS amount FROM reporting.eng_credit_notes WHERE property_id = $1 AND customer_id IS NOT NULL AND $5 = '' AND $6 = ''
		  AND (created_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date GROUP BY 1),
		ex AS (SELECT p.customer_id, sum(p.amount - p.refunded_amount) AS amount FROM reporting.eng_payments p
		  WHERE p.property_id = $1 AND p.customer_id IS NOT NULL AND p.method_type = ANY($7::text[]) AND p.status IN ('completed', 'refunded')
		  AND $5 = '' AND $6 = '' AND (p.paid_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date GROUP BY 1),
		ranked AS (SELECT c.id AS customer_id, c.code, c.name, c.customer_type,
		  EXISTS (SELECT 1 FROM reporting.membership_lifecycle m WHERE m.customer_id = c.id AND m.status = 'active') AS member,
		  (c.customer_type = 'corporate' OR EXISTS (SELECT 1 FROM crm.corporate_nominees n WHERE n.customer_id = c.id AND n.status = 'active')) AS corporate,
		  a.net AS charges, coalesce(rf.amount, 0) AS refunds, coalesce(cn.amount, 0) AS credits, coalesce(ex.amount, 0) AS excluded,
		  a.net - coalesce(rf.amount, 0) - coalesce(cn.amount, 0) - coalesce(ex.amount, 0) AS spend,
		  a.golf, a.pos, a.sportclub, a.stay, a.banquet, a.other, a.days
		  FROM agg a JOIN crm.customers c ON c.id = a.customer_id LEFT JOIN rf ON rf.customer_id = a.customer_id
		  LEFT JOIN cn ON cn.customer_id = a.customer_id LEFT JOIN ex ON ex.customer_id = a.customer_id
		  WHERE ($8 = '' OR EXISTS (SELECT 1 FROM crm.segment_members s WHERE s.segment_id::text = $8 AND s.customer_id = c.id)))
		SELECT (rank() OVER (ORDER BY spend DESC, name, customer_id))::int AS rank, customer_id, code, name, customer_type, member, corporate,
		  trim_scale(charges)::text AS charges, trim_scale(refunds)::text AS refunds, trim_scale(credits)::text AS credits,
		  trim_scale(excluded)::text AS excluded, trim_scale(spend)::text AS spend,
		  trim_scale(coalesce(golf, 0))::text AS golf, trim_scale(coalesce(pos, 0))::text AS pos, trim_scale(coalesce(sportclub, 0))::text AS sportclub,
		  trim_scale(coalesce(stay, 0))::text AS stay, trim_scale(coalesce(banquet, 0))::text AS banquet, trim_scale(coalesce(other, 0))::text AS other,
		  days::int AS visits
		FROM ranked WHERE spend > 0 AND ($9 = '' OR ($9 = 'member' AND member) OR ($9 = 'non_member' AND NOT member) OR ($9 = 'corporate' AND corporate))
		ORDER BY spend DESC, name, customer_id LIMIT $10`,
		property, f.From.Format("2006-01-02"), f.To.Format("2006-01-02"), tz, f.BusinessLine, outlet, excl, seg, f.MemberType, f.Limit)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[TopSpender])
	if err != nil {
		return nil, err
	}
	// Rank movement against the snapshot of the previous calendar month.
	if f.From.Day() == 1 && f.To.Equal(f.From.AddDate(0, 1, -1)) && f.BusinessLine == "" && f.OutletID == nil && f.MemberType == "" && f.SegmentID == nil {
		prev := f.From.AddDate(0, -1, 0).Format("2006-01")
		prows, err := q.Query(ctx, `SELECT customer_id, rank FROM crm.top_spender_snapshots WHERE property_id = $1 AND period = $2`, property, prev)
		if err != nil {
			return out, err
		}
		ranks := map[uuid.UUID]int{}
		for prows.Next() {
			var c uuid.UUID
			var r int
			if err := prows.Scan(&c, &r); err != nil {
				prows.Close()
				return out, err
			}
			ranks[c] = r
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			return out, err
		}
		for i := range out {
			if r, ok := ranks[out[i].CustomerID]; ok {
				out[i].PreviousRank = &r
			}
		}
	}
	return out, nil
}

// LeaderboardEntry is one entry of a leaderboard.
type LeaderboardEntry struct {
	Rank       int       `json:"rank" db:"rank"`
	CustomerID uuid.UUID `json:"customerId" db:"customer_id"`
	Code       string    `json:"code" db:"code"`
	Name       string    `json:"name" db:"name"`
	Value      int       `json:"value" db:"value" doc:"Rounds or active days"`
}

// MostRounds is the Most Rounds leaderboard (finished golf rounds).
func (s *Service) MostRounds(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, limit int) ([]LeaderboardEntry, error) {
	return handle.List[LeaderboardEntry](q.Query(ctx, `SELECT (rank() OVER (ORDER BY count(*) DESC))::int AS rank, c.id AS customer_id, c.code, c.name,
		count(*)::int AS value FROM reporting.golf_rounds g JOIN crm.customers c ON c.id = g.customer_id
		WHERE g.property_id = $1 AND g.play_date BETWEEN $2::date AND $3::date GROUP BY c.id, c.code, c.name ORDER BY value DESC, c.name LIMIT $4`,
		property, from.Format("2006-01-02"), to.Format("2006-01-02"), limitOf(limit)))
}

// MostActive is the Most Active Member leaderboard: members by days with
// charges across all lines.
func (s *Service) MostActive(ctx context.Context, q dbtx.Querier, property uuid.UUID, from, to time.Time, limit int) ([]LeaderboardEntry, error) {
	tz := loc(ctx, q, property).String()
	return handle.List[LeaderboardEntry](q.Query(ctx, `SELECT (rank() OVER (ORDER BY count(DISTINCT (l.posted_at AT TIME ZONE $4)::date) DESC))::int AS rank,
		c.id AS customer_id, c.code, c.name, count(DISTINCT (l.posted_at AT TIME ZONE $4)::date)::int AS value
		FROM reporting.eng_folio_lines l JOIN crm.customers c ON c.id = l.customer_id
		WHERE l.property_id = $1 AND (l.posted_at AT TIME ZONE $4)::date BETWEEN $2::date AND $3::date
		AND EXISTS (SELECT 1 FROM reporting.membership_lifecycle m WHERE m.customer_id = c.id AND m.status = 'active')
		GROUP BY c.id, c.code, c.name ORDER BY value DESC, c.name LIMIT $5`, property, from.Format("2006-01-02"), to.Format("2006-01-02"), tz, limitOf(limit)))
}

func limitOf(n int) int {
	if n <= 0 || n > 500 {
		return 20
	}
	return n
}

// ── snapshot (monthly job) ────────────────────────────────────────────────

// Snapshot stores the ranking of a closed month (idempotent).
func (s *Service) Snapshot(ctx context.Context, tx pgx.Tx, property uuid.UUID, month time.Time) (int, error) {
	pol, _, err := rules.PolicyAt(ctx, tx, PolicyCode, property, DefaultPolicy)
	if err != nil {
		return 0, err
	}
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	list, err := s.Rank(ctx, tx, property, Filter{From: from, To: from.AddDate(0, 1, -1), Limit: max(pol.SnapshotSize, 1)})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sp := range list {
		tag, err := tx.Exec(ctx, `INSERT INTO crm.top_spender_snapshots (property_id, period, customer_id, rank, spend, visits) VALUES ($1,$2,$3,$4,$5::numeric,$6)
			ON CONFLICT DO NOTHING`, property, from.Format("2006-01"), sp.CustomerID, sp.Rank, sp.Spend, sp.Visits)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// SnapshotArgs runs the monthly Top Spender snapshot.
type SnapshotArgs struct{}

func (SnapshotArgs) Kind() string { return "crm_top_spender_snapshot" }

func (SnapshotArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// SnapshotWorker snapshots the previous month on the 1st of every month.
type SnapshotWorker struct {
	river.WorkerDefaults[SnapshotArgs]
	S *Service
}

func (w *SnapshotWorker) Work(ctx context.Context, _ *river.Job[SnapshotArgs]) error {
	ctx = dbtx.System(ctx)
	var props []uuid.UUID
	if err := w.S.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
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
		if err := w.S.DB.WithTx(ctx, func(tx pgx.Tx) error {
			now := clock.Now().In(loc(ctx, tx, p))
			if now.Day() != 1 {
				return nil
			}
			_, err := w.S.Snapshot(ctx, tx, p, now.AddDate(0, -1, 0))
			return err
		}); err != nil {
			return fmt.Errorf("top spender snapshot for %s: %w", p, err)
		}
	}
	return nil
}

// RegisterJobs adds the snapshot worker (daily check, runs on the 1st).
func (s *Service) RegisterJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &SnapshotWorker{S: s})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 2, Minute: 40, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return SnapshotArgs{}, nil }, nil))
}

// ── HTTP ──────────────────────────────────────────────────────────────────

// TopSpenderRanking is the Top Spender list.
type TopSpenderRanking struct {
	From    string            `json:"from"`
	To      string            `json:"to"`
	Items   []TopSpender      `json:"items"`
	Total   string            `json:"total" doc:"Spend of the listed customers"`
	Filters TopSpenderFilters `json:"filters"`
}

// TopSpenderFilters echoes the filters.
type TopSpenderFilters struct {
	Period       string `json:"period,omitempty"`
	BusinessLine string `json:"businessLine,omitempty"`
	MemberType   string `json:"memberType,omitempty"`
	OutletID     string `json:"outletId,omitempty"`
	SegmentID    string `json:"segmentId,omitempty"`
}

// Leaderboard is a rounds / activity leaderboard.
type Leaderboard struct {
	Kind  string             `json:"kind" enum:"rounds,activity"`
	From  string             `json:"from"`
	To    string             `json:"to"`
	Items []LeaderboardEntry `json:"items"`
}

// TopSpenderSegmentInput adds customers of the ranking to a static segment
// (VIP invitation list, FR-TOP-05).
type TopSpenderSegmentInput struct {
	SegmentID    *uuid.UUID  `json:"segmentId,omitempty" doc:"An existing static segment"`
	SegmentCode  string      `json:"segmentCode,omitempty" doc:"Or create a new static segment with this code"`
	SegmentName  string      `json:"segmentName,omitempty"`
	CustomerIDs  []uuid.UUID `json:"customerIds,omitempty" doc:"Default: the top N of the filters below"`
	Top          int         `json:"top,omitempty" doc:"Top N (default 10)"`
	From         string      `json:"from,omitempty"`
	To           string      `json:"to,omitempty"`
	Period       string      `json:"period,omitempty" enum:"month,quarter,year"`
	BusinessLine string      `json:"businessLine,omitempty"`
	MemberType   string      `json:"memberType,omitempty" enum:"member,non_member,corporate"`
}

// TopSpenderSegmentResult reports the VIP list.
type TopSpenderSegmentResult struct {
	SegmentID   uuid.UUID `json:"segmentId"`
	SegmentCode string    `json:"segmentCode"`
	Added       int       `json:"added"`
	MemberCount int       `json:"memberCount"`
}

// period resolves from / to: explicit dates, or the current month,
// quarter or year (local dates).
func period(ctx context.Context, q dbtx.Querier, property uuid.UUID, kind, fromS, toS string) (time.Time, time.Time, error) {
	n := clock.Now().In(loc(ctx, q, property))
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	var from, to time.Time
	switch kind {
	case "quarter":
		m := (int(today.Month())-1)/3*3 + 1
		from = time.Date(today.Year(), time.Month(m), 1, 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 3, -1)
	case "year":
		from = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		to = time.Date(today.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	case "", "month":
		from = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 1, -1)
	default:
		return from, to, handle.Invalid("period", "invalid_period", "period must be month, quarter or year")
	}
	var err error
	if fromS != "" {
		if from, err = time.Parse("2006-01-02", fromS); err != nil {
			return from, to, handle.Invalid("from", "invalid_date", "from must be YYYY-MM-DD")
		}
	}
	if toS != "" {
		if to, err = time.Parse("2006-01-02", toS); err != nil {
			return from, to, handle.Invalid("to", "invalid_date", "to must be YYYY-MM-DD")
		}
	}
	if to.Before(from) {
		return from, to, handle.Invalid("to", "invalid_period", "to must not be before from")
	}
	return from, to, nil
}

func (s *Service) filter(ctx context.Context, q dbtx.Querier, r *http.Request) (Filter, TopSpenderFilters, error) {
	pid := handle.Property(ctx)
	v := r.URL.Query()
	from, to, err := period(ctx, q, pid, v.Get("period"), v.Get("from"), v.Get("to"))
	if err != nil {
		return Filter{}, TopSpenderFilters{}, err
	}
	f := Filter{From: from, To: to, BusinessLine: v.Get("businessLine"), MemberType: v.Get("memberType"), Limit: handle.QueryInt(r, "limit", 50)}
	if f.MemberType != "" && !slices.Contains([]string{"member", "non_member", "corporate"}, f.MemberType) {
		return f, TopSpenderFilters{}, handle.Invalid("memberType", "invalid_member_type", "memberType must be member, non_member or corporate")
	}
	if f.OutletID, err = handle.QueryUUID(r, "outletId"); err != nil {
		return f, TopSpenderFilters{}, err
	}
	if f.SegmentID, err = handle.QueryUUID(r, "segmentId"); err != nil {
		return f, TopSpenderFilters{}, err
	}
	a := TopSpenderFilters{Period: v.Get("period"), BusinessLine: f.BusinessLine, MemberType: f.MemberType}
	if f.OutletID != nil {
		a.OutletID = f.OutletID.String()
	}
	if f.SegmentID != nil {
		a.SegmentID = f.SegmentID.String()
	}
	return f, a, nil
}

func actor(ctx context.Context) *uuid.UUID {
	if p := authz.From(ctx); p != nil && p.UserID != uuid.Nil {
		return id.Ptr(p.UserID)
	}
	return nil
}

// AddToSegment puts customers (or the top N) into a static segment.
func (s *Service) AddToSegment(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TopSpenderSegmentInput) (TopSpenderSegmentResult, error) {
	var sid uuid.UUID
	var code, typ string
	switch {
	case in.SegmentID != nil:
		if err := tx.QueryRow(ctx, `SELECT id, code, segment_type FROM crm.segments WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`,
			*in.SegmentID, property).Scan(&sid, &code, &typ); err != nil {
			if dbtx.IsNoRows(err) {
				return TopSpenderSegmentResult{}, errs.NotFound("segment")
			}
			return TopSpenderSegmentResult{}, err
		}
		if typ != "static" {
			return TopSpenderSegmentResult{}, errs.Conflict("segment_not_static", "customers can only be added to a static segment")
		}
	case strings.TrimSpace(in.SegmentCode) != "":
		code = strings.ToUpper(strings.TrimSpace(in.SegmentCode))
		name := strings.TrimSpace(in.SegmentName)
		if name == "" {
			name = code
		}
		sid = id.New()
		if _, err := tx.Exec(ctx, `INSERT INTO crm.segments (id, property_id, code, name, segment_type, rules, created_by, updated_by)
			VALUES ($1,$2,$3,$4,'static','{}'::jsonb,$5,$5)`, sid, property, code, name, actor(ctx)); err != nil {
			if ok, _ := dbtx.IsUniqueViolation(err); ok {
				return TopSpenderSegmentResult{}, errs.Conflict("duplicate_code", "segment "+code+" already exists")
			}
			return TopSpenderSegmentResult{}, err
		}
		if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.segment", EntityID: sid.String(),
			EntityLabel: code, PropertyID: &property, After: map[string]any{"code": code, "name": name, "segmentType": "static", "source": "top_spender"}}); err != nil {
			return TopSpenderSegmentResult{}, err
		}
	default:
		return TopSpenderSegmentResult{}, errs.Validation("segment_required", "choose a static segment or a new segment code",
			errs.Field("segmentId", "required", "segmentId or segmentCode"))
	}
	ids := in.CustomerIDs
	if len(ids) == 0 {
		from, to, err := period(ctx, tx, property, in.Period, in.From, in.To)
		if err != nil {
			return TopSpenderSegmentResult{}, err
		}
		top := in.Top
		if top <= 0 {
			top = 10
		}
		list, err := s.Rank(ctx, tx, property, Filter{From: from, To: to, BusinessLine: in.BusinessLine, MemberType: in.MemberType, Limit: top})
		if err != nil {
			return TopSpenderSegmentResult{}, err
		}
		for _, sp := range list {
			ids = append(ids, sp.CustomerID)
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO crm.segment_members (segment_id, property_id, customer_id) SELECT $1, $2, c.id FROM crm.customers c
		WHERE c.id = ANY($3::uuid[]) AND c.property_id = $2 ON CONFLICT DO NOTHING`, sid, property, ids)
	if err != nil {
		return TopSpenderSegmentResult{}, err
	}
	out := TopSpenderSegmentResult{SegmentID: sid, SegmentCode: code, Added: int(tag.RowsAffected())}
	if err := tx.QueryRow(ctx, `UPDATE crm.segments SET member_count = (SELECT count(*) FROM crm.segment_members WHERE segment_id = $1), computed_at = now()
		WHERE id = $1 RETURNING member_count`, sid).Scan(&out.MemberCount); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "add_members", EntityType: "crm.segment", EntityID: sid.String(),
		EntityLabel: code, PropertyID: &property, After: out, Metadata: map[string]any{"source": "top_spender"}})
}

// TierNoteInput records a note for the loyalty tier of a top spender
// (FR-TOP-05).
type TierNoteInput struct {
	CustomerID      uuid.UUID  `json:"customerId"`
	Note            string     `json:"note"`
	SuggestedTierID *uuid.UUID `json:"suggestedTierId,omitempty" doc:"Tier to consider at the next evaluation"`
}

// TierNoteResult is the stored note.
type TierNoteResult struct {
	ID         uuid.UUID `json:"id"`
	CustomerID uuid.UUID `json:"customerId"`
	Note       string    `json:"note"`
}

// AddTierNote stores a tier note; it shows on the loyalty account.
func (s *Service) AddTierNote(ctx context.Context, tx pgx.Tx, property uuid.UUID, in TierNoteInput) (TierNoteResult, error) {
	in.Note = strings.TrimSpace(in.Note)
	if err := handle.Required("note", in.Note); err != nil {
		return TierNoteResult{}, err
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM crm.customers WHERE id = $1 AND property_id = $2`, in.CustomerID, property).Scan(&name); err != nil {
		if dbtx.IsNoRows(err) {
			return TierNoteResult{}, handle.Invalid("customerId", "not_found", "customer not found in this property")
		}
		return TierNoteResult{}, err
	}
	if in.SuggestedTierID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_tiers WHERE id = $1 AND property_id = $2 AND archived_at IS NULL)`,
			*in.SuggestedTierID, property).Scan(&ok); err != nil {
			return TierNoteResult{}, err
		}
		if !ok {
			return TierNoteResult{}, handle.Invalid("suggestedTierId", "not_found", "tier not found in this property")
		}
	}
	nid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_notes (id, property_id, customer_id, suggested_tier_id, note, source, created_by)
		VALUES ($1,$2,$3,$4,$5,'top_spender',$6)`, nid, property, in.CustomerID, in.SuggestedTierID, in.Note, actor(ctx)); err != nil {
		return TierNoteResult{}, err
	}
	out := TierNoteResult{ID: nid, CustomerID: in.CustomerID, Note: in.Note}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.loyalty_tier_note", EntityID: nid.String(),
		EntityLabel: name, PropertyID: &property, After: map[string]any{"note": in.Note, "suggestedTierId": in.SuggestedTierID}})
}

// Register adds the Top Spender routes (wired by internal/app).
func (s *Service) Register(reg *route.Registry) {
	db := s.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Top Spender"
		reg.Add(rt)
	}
	q := []route.Param{{Name: "period", Enum: []string{"month", "quarter", "year"}}, {Name: "from"}, {Name: "to"}, {Name: "limit", Type: "integer"}}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/top-spenders", Summary: "Top Spender ranking (internal only)", Permission: "crm.top_spender.view",
		Response: TopSpenderRanking{}, Query: append(q, route.Param{Name: "businessLine"}, route.Param{Name: "memberType", Enum: []string{"member", "non_member", "corporate"}},
			route.Param{Name: "outletId"}, route.Param{Name: "segmentId"}),
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (TopSpenderRanking, error) {
			f, a, err := s.filter(ctx, tx, r)
			if err != nil {
				return TopSpenderRanking{}, err
			}
			items, err := s.Rank(ctx, tx, handle.Property(ctx), f)
			if err != nil {
				return TopSpenderRanking{}, err
			}
			total := decimal.Zero
			for _, it := range items {
				if d, err := decimal.NewFromString(it.Spend); err == nil {
					total = total.Add(d)
				}
			}
			return TopSpenderRanking{From: f.From.Format("2006-01-02"), To: f.To.Format("2006-01-02"), Items: items, Total: total.String(), Filters: a}, nil
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/leaderboards/{kind}", Summary: "Leaderboards: rounds (Most Rounds) or activity (Most Active Member)",
		Permission: "crm.top_spender.view", Response: Leaderboard{}, Query: q,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Leaderboard, error) {
			kind := chi.URLParam(r, "kind")
			pid := handle.Property(ctx)
			from, to, err := period(ctx, tx, pid, r.URL.Query().Get("period"), r.URL.Query().Get("from"), r.URL.Query().Get("to"))
			if err != nil {
				return Leaderboard{}, err
			}
			out := Leaderboard{Kind: kind, From: from.Format("2006-01-02"), To: to.Format("2006-01-02")}
			limit := handle.QueryInt(r, "limit", 20)
			switch kind {
			case "rounds":
				out.Items, err = s.MostRounds(ctx, tx, pid, from, to, limit)
			case "activity":
				out.Items, err = s.MostActive(ctx, tx, pid, from, to, limit)
			default:
				return out, errs.NotFound("leaderboard")
			}
			return out, err
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/top-spenders:tier-note", Summary: "Note for the loyalty tier of a top spender",
		Permission: "crm.top_spender.manage", Request: TierNoteInput{}, Response: TierNoteResult{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TierNoteInput) (TierNoteResult, error) {
			return s.AddTierNote(ctx, tx, handle.Property(ctx), in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/top-spenders:add-to-segment", Summary: "Add top spenders to a static segment (VIP invitation list)",
		Permission: "crm.top_spender.manage", Request: TopSpenderSegmentInput{}, Response: TopSpenderSegmentResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in TopSpenderSegmentInput) (TopSpenderSegmentResult, error) {
			return s.AddToSegment(ctx, tx, handle.Property(ctx), in)
		})})
}

// Permissions of Top Spender.
func Permissions() []catalog.Permission {
	return catalog.P("crm", "top_spender", "view", "manage")
}

// RolePermissions grant Top Spender to sales and management only (FR-TOP-06).
func RolePermissions() map[string][]string {
	both := []string{"crm.top_spender.view", "crm.top_spender.manage"}
	view := []string{"crm.top_spender.view"}
	return map[string][]string{
		"property_admin": both, "crm_admin": both, "marketing_staff": both, "sales_executive": both,
		"general_manager": view, "club_manager": view, "membership_manager": view, "finance_manager": view,
	}
}
