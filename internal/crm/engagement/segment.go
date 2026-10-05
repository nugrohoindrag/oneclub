package engagement

// Advanced Segmentation (FR-C360-02/03): P2's rules (member type,
// frequency, spend, age, residence, customer type) plus loyalty tier,
// recency / frequency / monetary (RFM), Top Spender rank, corporate, outlet,
// business line and tags contributed by other modules (tournament
// participant, wedding …). Dynamic segments are refreshed daily; static
// segments are snapshots with manually added members.

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm/topspender"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
)

// Rules are the P3 segment rules, stored with P2's rules in the same JSON.
type Rules struct {
	LookbackDays   int      `json:"lookbackDays,omitempty"`
	LoyaltyMember  *bool    `json:"loyaltyMember,omitempty"`
	LoyaltyTiers   []string `json:"loyaltyTiers,omitempty" doc:"Tier codes"`
	MinRecencyDays *int     `json:"minRecencyDays,omitempty" doc:"Days since the last charge"`
	MaxRecencyDays *int     `json:"maxRecencyDays,omitempty"`
	MinFrequency   *int     `json:"minFrequency,omitempty" doc:"Days with charges in the lookback"`
	MaxFrequency   *int     `json:"maxFrequency,omitempty"`
	MinMonetary    string   `json:"minMonetary,omitempty" doc:"Net spend in the lookback"`
	RFMSegments    []string `json:"rfmSegments,omitempty" doc:"champions, loyal, potential, new, at_risk, hibernating, inactive"`
	TopSpenderRank *int     `json:"topSpenderRank,omitempty" doc:"Top N by spend in the lookback"`
	Corporate      *bool    `json:"corporate,omitempty" doc:"Corporate customer or nominee"`
	OutletIDs      []string `json:"outletIds,omitempty" doc:"Spent at one of these POS outlets in the lookback"`
	BusinessLines  []string `json:"businessLines,omitempty" doc:"Spent in one of these business lines in the lookback"`
	Tags           []string `json:"tags,omitempty" doc:"Any of: loyalty_member, complainant, campaign_responder, nps_promoter, nps_detractor and module tags"`
}

func (r Rules) any() bool {
	return r.LoyaltyMember != nil || len(r.LoyaltyTiers) > 0 || r.MinRecencyDays != nil || r.MaxRecencyDays != nil || r.MinFrequency != nil ||
		r.MaxFrequency != nil || r.MinMonetary != "" || len(r.RFMSegments) > 0 || r.TopSpenderRank != nil || r.Corporate != nil ||
		len(r.OutletIDs) > 0 || len(r.BusinessLines) > 0 || len(r.Tags) > 0
}

// RFMFacts are the P3 facts of one customer.
type RFMFacts struct {
	RecencyDays *int   `json:"recencyDays"`
	Frequency   int    `json:"frequency"`
	Monetary    string `json:"monetary"`
	RFM         string `json:"rfm" enum:"champions,loyal,potential,new,at_risk,hibernating,inactive"`
	LoyaltyTier string `json:"loyaltyTier,omitempty"`
}

type activity struct {
	Customer uuid.UUID  `db:"customer_id"`
	Last     *time.Time `db:"last_at"`
	Visits   int        `db:"visits"`
	Spend    string     `db:"spend"`
}

// facts computes recency, frequency, monetary and the RFM label of the
// customers of a property (charged folio lines, reporting views).
func facts(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time) (map[uuid.UUID]RFMFacts, error) {
	tz := location(ctx, q, property).String()
	acts, err := handle.List[activity](q.Query(ctx, `SELECT customer_id, max(posted_at) AS last_at,
		count(DISTINCT (posted_at AT TIME ZONE $3)::date) FILTER (WHERE posted_at >= $2)::int AS visits,
		coalesce(sum(net_amount) FILTER (WHERE posted_at >= $2 AND NOT liability), 0)::text AS spend
		FROM reporting.eng_folio_lines WHERE property_id = $1 AND customer_id IS NOT NULL GROUP BY customer_id`, property, since, tz))
	if err != nil {
		return nil, err
	}
	var spends []decimal.Decimal
	for _, a := range acts {
		if d, _ := decimal.NewFromString(a.Spend); d.IsPositive() {
			spends = append(spends, d)
		}
	}
	sort.Slice(spends, func(i, j int) bool { return spends[i].LessThan(spends[j]) })
	tertile := func(d decimal.Decimal) int {
		if !d.IsPositive() || len(spends) == 0 {
			return 0
		}
		idx := sort.Search(len(spends), func(i int) bool { return !spends[i].LessThan(d) })
		switch {
		case idx*3 >= 2*len(spends):
			return 3
		case idx*3 >= len(spends):
			return 2
		}
		return 1
	}
	now := clock.Now()
	out := map[uuid.UUID]RFMFacts{}
	for _, a := range acts {
		f := RFMFacts{Frequency: a.Visits, Monetary: a.Spend, RFM: "inactive"}
		r := 1
		if a.Last != nil {
			d := int(now.Sub(*a.Last).Hours() / 24)
			f.RecencyDays = &d
			switch {
			case d <= 30:
				r = 3
			case d <= 90:
				r = 2
			}
		}
		fr := 0
		switch {
		case a.Visits >= 8:
			fr = 3
		case a.Visits >= 3:
			fr = 2
		case a.Visits >= 1:
			fr = 1
		}
		spend, _ := decimal.NewFromString(a.Spend)
		m := tertile(spend)
		switch {
		case a.Last == nil:
			f.RFM = "inactive"
		case r == 3 && fr >= 2 && m == 3:
			f.RFM = "champions"
		case r >= 2 && fr >= 2 && m >= 2:
			f.RFM = "loyal"
		case r == 3 && fr <= 1:
			f.RFM = "new"
		case r == 1 && fr >= 2:
			f.RFM = "at_risk"
		case r == 1:
			f.RFM = "hibernating"
		default:
			f.RFM = "potential"
		}
		out[a.Customer] = f
	}
	return out, nil
}

// CustomerFacts are the P3 facts of one customer (Customer 360).
func CustomerFacts(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID) (RFMFacts, error) {
	all, err := facts(ctx, q, property, clock.Now().AddDate(0, 0, -90))
	if err != nil {
		return RFMFacts{}, err
	}
	f, ok := all[customer]
	if !ok {
		f = RFMFacts{RFM: "inactive", Monetary: "0"}
	}
	_ = q.QueryRow(ctx, `SELECT t.code FROM crm.loyalty_accounts a JOIN crm.loyalty_tiers t ON t.id = a.tier_id WHERE a.customer_id = $1`, customer).Scan(&f.LoyaltyTier)
	return f, nil
}

func idSet(ctx context.Context, q dbtx.Querier, sql string, args ...any) (map[uuid.UUID]bool, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, x := range ids {
		out[x] = true
	}
	return out, nil
}

// tags returns the customers carrying any of the tags.
func (s *Service) tags(ctx context.Context, q dbtx.Querier, property uuid.UUID, since time.Time, names []string) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, t := range names {
		var set map[uuid.UUID]bool
		var err error
		switch t {
		case "loyalty_member":
			set, err = idSet(ctx, q, `SELECT customer_id FROM crm.loyalty_accounts WHERE property_id = $1 AND status = 'active'`, property)
		case "complainant":
			set, err = idSet(ctx, q, `SELECT DISTINCT customer_id FROM crm.tickets WHERE property_id = $1 AND customer_id IS NOT NULL AND created_at >= $2`, property, since)
		case "campaign_responder":
			set, err = idSet(ctx, q, `SELECT DISTINCT customer_id FROM crm.campaign_recipients WHERE property_id = $1 AND (clicked_at >= $2 OR converted_at >= $2)`,
				property, since)
		case "nps_promoter", "nps_detractor":
			cond := `score >= 9`
			if t == "nps_detractor" {
				cond = `score <= 6`
			}
			set, err = idSet(ctx, q, `SELECT customer_id FROM (SELECT DISTINCT ON (customer_id) customer_id, score FROM reporting.eng_nps
				WHERE property_id = $1 AND customer_id IS NOT NULL ORDER BY customer_id, created_at DESC) x WHERE `+cond, property)
		default:
			if f, ok := s.Tags[t]; ok && f != nil {
				set, err = f(ctx, q, property, since)
			} else {
				set, err = idSet(ctx, q, `SELECT customer_id FROM crm.customer_tags WHERE property_id = $1 AND tag = $2`, property, t)
			}
		}
		if err != nil {
			return nil, err
		}
		for k := range set {
			out[k] = true
		}
	}
	return out, nil
}

// SegmentRefresh reports a refresh.
type SegmentRefresh struct {
	SegmentID   uuid.UUID `json:"segmentId"`
	SegmentType string    `json:"segmentType"`
	MemberCount int       `json:"memberCount"`
	P2Matches   int       `json:"p2Matches" doc:"Members matching the P2 rules before the P3 dimensions"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

// Refresh recomputes a segment: P2's rules first (crm root), then the P3
// dimensions remove the members that do not match.
func (s *Service) Refresh(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID) (SegmentRefresh, error) {
	if s.CRM == nil {
		return SegmentRefresh{}, errs.Unavailable("segmentation is not wired")
	}
	var raw []byte
	var segType string
	if err := tx.QueryRow(ctx, `SELECT rules, segment_type FROM crm.segments WHERE id = $1 AND property_id = $2 AND archived_at IS NULL`, sid, property).
		Scan(&raw, &segType); err != nil {
		if dbtx.IsNoRows(err) {
			return SegmentRefresh{}, errs.NotFound("segment")
		}
		return SegmentRefresh{}, err
	}
	var r Rules
	if err := json.Unmarshal(raw, &r); err != nil {
		return SegmentRefresh{}, handle.Invalid("rules", "invalid_rules", "segment rules are not valid")
	}
	p2, err := s.CRM.ComputeSegment(ctx, tx, property, sid)
	if err != nil {
		return SegmentRefresh{}, err
	}
	out := SegmentRefresh{SegmentID: sid, SegmentType: segType, MemberCount: p2.MemberCount, P2Matches: p2.MemberCount, RefreshedAt: clock.Now()}
	if r.any() {
		keep, err := s.matchP3(ctx, tx, property, sid, r)
		if err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM crm.segment_members WHERE segment_id = $1 AND NOT (customer_id = ANY($2::uuid[]))`, sid, keep); err != nil {
			return out, err
		}
		out.MemberCount = len(keep)
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.segments SET member_count = $2, refreshed_at = $3 WHERE id = $1`, sid, out.MemberCount, out.RefreshedAt); err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: "refresh", EntityType: "crm.segment", EntityID: sid.String(),
		EntityLabel: "segment refresh", PropertyID: &property, After: out})
}

// matchP3 returns the current members that match the P3 dimensions.
func (s *Service) matchP3(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, r Rules) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT customer_id FROM crm.segment_members WHERE segment_id = $1`, sid)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	lookback := r.LookbackDays
	if lookback <= 0 {
		lookback = 90
	}
	since := clock.Now().AddDate(0, 0, -lookback)
	needFacts := r.MinRecencyDays != nil || r.MaxRecencyDays != nil || r.MinFrequency != nil || r.MaxFrequency != nil || r.MinMonetary != "" || len(r.RFMSegments) > 0
	var fs map[uuid.UUID]RFMFacts
	if needFacts {
		if fs, err = facts(ctx, tx, property, since); err != nil {
			return nil, err
		}
	}
	minMon, err := handle.Decimal("minMonetary", r.MinMonetary, decimal.Zero)
	if err != nil {
		return nil, err
	}
	loyal := map[uuid.UUID]string{}
	if r.LoyaltyMember != nil || len(r.LoyaltyTiers) > 0 {
		type acct struct {
			Customer uuid.UUID `db:"customer_id"`
			Tier     *string   `db:"tier_code"`
		}
		as, err := handle.List[acct](tx.Query(ctx, `SELECT a.customer_id, t.code AS tier_code FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t
			ON t.id = a.tier_id WHERE a.property_id = $1 AND a.status = 'active'`, property))
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			loyal[a.Customer] = deref(a.Tier)
		}
	}
	var top map[uuid.UUID]bool
	if r.TopSpenderRank != nil && s.TopSpender != nil {
		today := localToday(ctx, tx, property)
		list, err := s.TopSpender.Rank(ctx, tx, property, topspender.Filter{From: today.AddDate(0, 0, -lookback), To: today, Limit: max(*r.TopSpenderRank, 1)})
		if err != nil {
			return nil, err
		}
		top = map[uuid.UUID]bool{}
		for _, sp := range list {
			top[sp.CustomerID] = true
		}
	}
	var corporate map[uuid.UUID]bool
	if r.Corporate != nil {
		if corporate, err = idSet(ctx, tx, `SELECT c.id FROM crm.customers c WHERE c.property_id = $1 AND (c.customer_type = 'corporate' OR EXISTS
			(SELECT 1 FROM crm.corporate_nominees n WHERE n.customer_id = c.id AND n.status = 'active'))`, property); err != nil {
			return nil, err
		}
	}
	var outlets, lines map[uuid.UUID]bool
	if len(r.OutletIDs) > 0 {
		if outlets, err = idSet(ctx, tx, `SELECT DISTINCT customer_id FROM reporting.eng_folio_lines WHERE property_id = $1 AND customer_id IS NOT NULL
			AND posted_at >= $2 AND outlet_id::text = ANY($3::text[])`, property, since, r.OutletIDs); err != nil {
			return nil, err
		}
	}
	if len(r.BusinessLines) > 0 {
		if lines, err = idSet(ctx, tx, `SELECT DISTINCT customer_id FROM reporting.eng_folio_lines WHERE property_id = $1 AND customer_id IS NOT NULL
			AND posted_at >= $2 AND business_line = ANY($3::text[])`, property, since, r.BusinessLines); err != nil {
			return nil, err
		}
	}
	var tagged map[uuid.UUID]bool
	if len(r.Tags) > 0 {
		if tagged, err = s.tags(ctx, tx, property, since, r.Tags); err != nil {
			return nil, err
		}
	}
	keep := []uuid.UUID{}
	for _, c := range members {
		if r.LoyaltyMember != nil {
			if _, ok := loyal[c]; ok != *r.LoyaltyMember {
				continue
			}
		}
		if len(r.LoyaltyTiers) > 0 {
			t, ok := loyal[c]
			if !ok || !slices.Contains(r.LoyaltyTiers, t) {
				continue
			}
		}
		if needFacts {
			f, ok := fs[c]
			if !ok {
				f = RFMFacts{RFM: "inactive", Monetary: "0"}
			}
			if r.MinRecencyDays != nil && (f.RecencyDays == nil || *f.RecencyDays < *r.MinRecencyDays) {
				continue
			}
			if r.MaxRecencyDays != nil && (f.RecencyDays == nil || *f.RecencyDays > *r.MaxRecencyDays) {
				continue
			}
			if (r.MinFrequency != nil && f.Frequency < *r.MinFrequency) || (r.MaxFrequency != nil && f.Frequency > *r.MaxFrequency) {
				continue
			}
			if minMon.IsPositive() {
				if m, _ := decimal.NewFromString(f.Monetary); m.LessThan(minMon) {
					continue
				}
			}
			if len(r.RFMSegments) > 0 && !slices.Contains(r.RFMSegments, f.RFM) {
				continue
			}
		}
		if top != nil && !top[c] {
			continue
		}
		if corporate != nil && corporate[c] != *r.Corporate {
			continue
		}
		if outlets != nil && !outlets[c] {
			continue
		}
		if lines != nil && !lines[c] {
			continue
		}
		if tagged != nil && !tagged[c] {
			continue
		}
		keep = append(keep, c)
	}
	return keep, nil
}

// SegmentMembersInput adds or removes customers of a static segment.
type SegmentMembersInput struct {
	CustomerIDs []uuid.UUID `json:"customerIds"`
}

// SegmentMembersResult reports the change.
type SegmentMembersResult struct {
	SegmentID   uuid.UUID `json:"segmentId"`
	Changed     int       `json:"changed"`
	MemberCount int       `json:"memberCount"`
}

// ChangeMembers adds (or removes) customers of a static segment.
func ChangeMembers(ctx context.Context, tx pgx.Tx, property, sid uuid.UUID, in SegmentMembersInput, remove bool) (SegmentMembersResult, error) {
	if len(in.CustomerIDs) == 0 {
		return SegmentMembersResult{}, handle.Invalid("customerIds", "required", "choose at least one customer")
	}
	var typ, code string
	if err := tx.QueryRow(ctx, `SELECT segment_type, code FROM crm.segments WHERE id = $1 AND property_id = $2 AND archived_at IS NULL FOR UPDATE`, sid, property).
		Scan(&typ, &code); err != nil {
		if dbtx.IsNoRows(err) {
			return SegmentMembersResult{}, errs.NotFound("segment")
		}
		return SegmentMembersResult{}, err
	}
	if typ != "static" {
		return SegmentMembersResult{}, errs.Conflict("segment_not_static", "members of a dynamic segment come from its rules; use a static segment")
	}
	var n int64
	if remove {
		tag, err := tx.Exec(ctx, `DELETE FROM crm.segment_members WHERE segment_id = $1 AND customer_id = ANY($2::uuid[])`, sid, in.CustomerIDs)
		if err != nil {
			return SegmentMembersResult{}, err
		}
		n = tag.RowsAffected()
	} else {
		tag, err := tx.Exec(ctx, `INSERT INTO crm.segment_members (segment_id, property_id, customer_id) SELECT $1, $2, c.id FROM crm.customers c
			WHERE c.id = ANY($3::uuid[]) AND c.property_id = $2 ON CONFLICT DO NOTHING`, sid, property, in.CustomerIDs)
		if err != nil {
			return SegmentMembersResult{}, err
		}
		n = tag.RowsAffected()
	}
	out := SegmentMembersResult{SegmentID: sid, Changed: int(n)}
	if err := tx.QueryRow(ctx, `UPDATE crm.segments SET member_count = (SELECT count(*) FROM crm.segment_members WHERE segment_id = $1), computed_at = now()
		WHERE id = $1 RETURNING member_count`, sid).Scan(&out.MemberCount); err != nil {
		return out, err
	}
	action := "add_members"
	if remove {
		action = "remove_members"
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: "crm.segment", EntityID: sid.String(), EntityLabel: code,
		PropertyID: &property, After: out, Metadata: map[string]any{"customerIds": in.CustomerIDs}})
}

// RefreshDynamic refreshes every active dynamic segment (daily job).
func (s *Service) RefreshDynamic(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	type seg struct {
		ID       uuid.UUID `db:"id"`
		Property uuid.UUID `db:"property_id"`
	}
	var list []seg
	if err := s.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		list, err = handle.List[seg](tx.Query(ctx, `SELECT id, property_id FROM crm.segments WHERE segment_type = 'dynamic' AND status = 'active'
			AND archived_at IS NULL ORDER BY id`))
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	for _, sg := range list {
		if err := s.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := s.Refresh(ctx, tx, sg.Property, sg.ID)
			return err
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// TagCustomer tags a customer for segmentation (idempotent; the latest
// source wins). Other modules tag through events wired by internal/app.
func TagCustomer(ctx context.Context, tx pgx.Tx, property, customer uuid.UUID, tag, sourceType string, sourceID *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO crm.customer_tags (property_id, customer_id, tag, source_type, source_id) SELECT $1, c.id, $3, $4, $5
		FROM crm.customers c WHERE c.id = $2 AND c.property_id = $1
		ON CONFLICT (customer_id, tag) DO UPDATE SET source_type = EXCLUDED.source_type, source_id = EXCLUDED.source_id, tagged_at = now()`,
		property, customer, tag, sourceType, sourceID)
	return err
}

// CustomerTags lists the tags of a customer.
func CustomerTags(ctx context.Context, q dbtx.Querier, customer uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT tag FROM crm.customer_tags WHERE customer_id = $1 ORDER BY tag`, customer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
