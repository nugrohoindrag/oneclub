package loyalty

// Tier master (product owner request on PRD P5 EP-18, §16 #14): the Loyalty
// Tier resource gets its class badge (colour, icon), qualification rules
// (spend and / or points over a window, membership types) and evaluation
// settings (window, grace months, downgrade allowed), validated as a ladder
// (unique rank, thresholds increasing with the rank). Thresholds are
// versioned: a change through the master stays pending — the evaluation
// engine keeps reading the thresholds in force (eff_* columns) — until the
// next full evaluation run or "re-evaluate now", whose preview shows how
// many members would move up or down.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

var (
	colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	iconRe  = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
)

// thresholdFields are the versioned qualification fields of a tier.
var thresholdFields = []string{"minPoints", "minSpend", "qualifyMode", "membershipTypeIds", "periodMonths"}

func init() {
	Tiers.Fields = append(Tiers.Fields,
		resource.Field{Name: "color", Column: "color", Label: "Badge Colour", Kind: resource.String, Default: "#6B7280", Max: 7, Pattern: colorRe,
			PatternMsg: "a colour #RRGGBB"},
		resource.Field{Name: "icon", Column: "icon", Label: "Badge Icon", Kind: resource.String, Default: "workspace_premium", Max: 40, Pattern: iconRe,
			PatternMsg: "an icon name (letters, digits, _)"},
		resource.Field{Name: "qualifyMode", Column: "qualify_mode", Label: "Qualification (all thresholds / any threshold)", Kind: resource.Enum,
			Enum: []string{QualifyAll, QualifyAny}, Default: QualifyAll},
		resource.Field{Name: "membershipTypeIds", Column: "membership_type_ids", Label: "Membership Types (empty = any customer)", Kind: resource.StringList,
			Default: []string{}},
		resource.Field{Name: "periodMonths", Column: "period_months", Label: "Qualifying Window (months; empty = programme)", Kind: resource.Int,
			Min: resource.Min(1), MaxN: resource.Max(60)},
		resource.Field{Name: "graceMonths", Column: "grace_months", Label: "Grace Before Downgrade (months; empty = programme)", Kind: resource.Int,
			Min: resource.Min(0), MaxN: resource.Max(24)},
		resource.Field{Name: "downgradeAllowed", Column: "downgrade_allowed", Label: "Downgrade Allowed", Kind: resource.Bool, Default: true},
		resource.Field{Name: "thresholdVersion", Column: "threshold_version", Label: "Threshold Version", Kind: resource.Int, ReadOnly: true},
		resource.Field{Name: "effectiveVersion", Column: "effective_version", Label: "Version in Force", Kind: resource.Int, ReadOnly: true},
		resource.Field{Name: "pendingThresholds", Column: "pending_thresholds", Label: "Thresholds Pending (next evaluation)", Kind: resource.Bool,
			ReadOnly: true})
	Tiers.Hooks.BeforeWrite = tierBeforeWrite
	Tiers.Hooks.AfterUpdate = tierAfterUpdate
	// Delete only a tier no account has ever been in; otherwise archive
	// (DELETE /crm/loyalty/tiers/{id} of registerTierMaster).
	Tiers.NoDelete = true
}

// LoyaltyTierRemoval is the result of deleting a tier.
type LoyaltyTierRemoval struct {
	ID     uuid.UUID `json:"id"`
	Result string    `json:"result" enum:"deleted,archived" doc:"archived: an account has been in the tier (history kept)"`
}

// RemoveTier deletes a tier no account has ever been in (and nothing else
// refers to); any other tier is archived with its history.
func RemoveTier(ctx context.Context, tx pgx.Tx, property, tid uuid.UUID) (LoyaltyTierRemoval, map[string]any, error) {
	var code, name string
	var archived *time.Time
	if err := tx.QueryRow(ctx, `SELECT code, name, archived_at FROM crm.loyalty_tiers WHERE id = $1 AND property_id = $2 FOR UPDATE`, tid, property).
		Scan(&code, &name, &archived); err != nil || archived != nil {
		if err == nil || dbtx.IsNoRows(err) {
			return LoyaltyTierRemoval{}, nil, errs.NotFound("loyalty tier")
		}
		return LoyaltyTierRemoval{}, nil, err
	}
	before := map[string]any{"id": tid, "code": code, "name": name}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_accounts WHERE tier_id = $1 OR grace_tier_id = $1)
		OR EXISTS (SELECT 1 FROM crm.loyalty_tier_history WHERE from_tier_id = $1 OR to_tier_id = $1)
		OR EXISTS (SELECT 1 FROM crm.loyalty_tier_evaluation_lines WHERE from_tier_id = $1 OR to_tier_id = $1)
		OR EXISTS (SELECT 1 FROM crm.loyalty_tier_overrides WHERE tier_id = $1 OR previous_tier_id = $1)`, tid).Scan(&used); err != nil {
		return LoyaltyTierRemoval{}, nil, err
	}
	if !used {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return LoyaltyTierRemoval{}, nil, err
		}
		_, err = sp.Exec(ctx, `DELETE FROM crm.loyalty_tiers WHERE id = $1`, tid)
		if err == nil {
			return LoyaltyTierRemoval{ID: tid, Result: "deleted"}, before, sp.Commit(ctx)
		}
		_ = sp.Rollback(ctx)
		if !dbtx.IsForeignKeyViolation(err) { // still referred to (rewards, rules, notes, evaluations): archive
			return LoyaltyTierRemoval{}, nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tiers SET archived_at = now(), status = 'inactive', updated_by = $2 WHERE id = $1`, tid, actor(ctx)); err != nil {
		return LoyaltyTierRemoval{}, nil, err
	}
	return LoyaltyTierRemoval{ID: tid, Result: "archived"}, before, nil
}

func anyInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	}
	return 0, false
}

func anyDec(v any) decimal.Decimal {
	switch t := v.(type) {
	case string:
		return dec(t)
	case float64:
		return decimal.NewFromFloat(t)
	case int64:
		return decimal.NewFromInt(t)
	}
	return decimal.Zero
}

func anyStrings(v any) []string {
	out := []string{}
	switch t := v.(type) {
	case []string:
		out = append(out, t...)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func sameValue(k string, a, b any) bool {
	switch k {
	case "minSpend":
		return anyDec(a).Equal(anyDec(b))
	case "membershipTypeIds":
		return slices.Equal(anyStrings(a), anyStrings(b))
	case "minPoints", "periodMonths":
		x, okx := anyInt(a)
		y, oky := anyInt(b)
		return okx == oky && x == y
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// ladderTier is another tier of the property for the ladder validation.
type ladderTier struct {
	ID        string `db:"id"`
	Code      string `db:"code"`
	Name      string `db:"name"`
	Rank      int64  `db:"rank"`
	MinPoints int64  `db:"min_points"`
	MinSpend  string `db:"min_spend"`
}

// tierBeforeWrite validates a tier and versions a threshold change:
// classification rank 1..n unique among the active tiers, thresholds
// increasing with the rank (a higher tier needs at least as much spend and
// points and more of one of them), membership types of the property.
func tierBeforeWrite(ctx context.Context, tx pgx.Tx, v, before map[string]any) error {
	get := func(k string) any { return merged(v, before, k) }
	rank, _ := anyInt(get("rank"))
	if _, set := v["rank"]; set || before == nil {
		if rank < 1 {
			return handle.Invalid("rank", "too_small", "the classification rank starts at 1 (1 = lowest tier)")
		}
	}
	for _, s := range anyStrings(v["membershipTypeIds"]) {
		if _, err := uuid.Parse(s); err != nil {
			return handle.Invalid("membershipTypeIds", "invalid_uuid", "membership type ids must be UUIDs")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM reporting.crm_membership_types WHERE id = $1::uuid AND property_id = $2)`, s,
			handle.Property(ctx)).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return handle.Invalid("membershipTypeIds", "not_found", "membership type not found in this property")
		}
	}
	status, _ := get("status").(string)
	if before != nil {
		if before["archivedAt"] != nil {
			return errs.Conflict("tier_archived", "an archived tier cannot be changed")
		}
	}
	if status == "" || status == "active" {
		self := ""
		if before != nil {
			self, _ = before["id"].(string)
		}
		others, err := handle.List[ladderTier](tx.Query(ctx, `SELECT id::text AS id, code, name, rank::int8 AS rank, min_points, min_spend::text AS min_spend
			FROM crm.loyalty_tiers WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND id::text <> $2 ORDER BY rank`,
			handle.Property(ctx), self))
		if err != nil {
			return err
		}
		pts, _ := anyInt(get("minPoints"))
		spend := anyDec(get("minSpend"))
		for _, o := range others {
			op, os := o.MinPoints, dec(o.MinSpend)
			switch {
			case o.Rank == rank:
				return handle.Invalid("rank", "taken", fmt.Sprintf("rank %d is used by %s (%s)", rank, o.Name, o.Code))
			case o.Rank < rank && !harder(pts, spend, op, os):
				return errs.Validation("thresholds_not_increasing", fmt.Sprintf("a tier ranked above %s (rank %d) needs higher thresholds", o.Name, o.Rank),
					errs.Field("minSpend", "too_small", fmt.Sprintf("at least %s spend and %d points, and more of one of them", os.String(), op)))
			case o.Rank > rank && !harder(op, os, pts, spend):
				return errs.Validation("thresholds_not_increasing", fmt.Sprintf("a tier ranked below %s (rank %d) needs lower thresholds", o.Name, o.Rank),
					errs.Field("minSpend", "too_large", fmt.Sprintf("at most %s spend and %d points, and less of one of them", os.String(), op)))
			}
		}
	}
	if before == nil {
		return nil // a new tier is in force at once (trigger crm.loyalty_tier_thresholds)
	}
	changed := false
	for _, k := range thresholdFields {
		if nv, ok := v[k]; ok && !sameValue(k, nv, before[k]) {
			changed = true
		}
	}
	if changed {
		ver, _ := anyInt(before["thresholdVersion"])
		v["thresholdVersion"], v["pendingThresholds"] = ver+1, true
	}
	return nil
}

// harder: thresholds (hp, hs) are at least (lp, ls) and more in one of them.
func harder(hp int64, hs decimal.Decimal, lp int64, ls decimal.Decimal) bool {
	return hp >= lp && hs.GreaterThanOrEqual(ls) && (hp > lp || hs.GreaterThan(ls))
}

// tierAfterUpdate records the pending threshold version of a change.
func tierAfterUpdate(ctx context.Context, tx pgx.Tx, before, after map[string]any) error {
	bv, _ := anyInt(before["thresholdVersion"])
	av, _ := anyInt(after["thresholdVersion"])
	if av == bv || after["pendingThresholds"] != true {
		return nil
	}
	tid, _ := after["id"].(string)
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_versions SET status = 'superseded' WHERE tier_id = $1::uuid AND status = 'pending'`, tid); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_versions (id, property_id, tier_id, version, min_points, min_spend, qualify_mode, membership_type_ids,
		period_months, status, created_by) SELECT gen_random_uuid(), property_id, id, threshold_version, min_points, min_spend, qualify_mode, membership_type_ids,
		period_months, 'pending', $2 FROM crm.loyalty_tiers WHERE id = $1::uuid`, tid, actor(ctx))
	return err
}

// AppliedTierVersion is a threshold version put in force by an evaluation run.
type AppliedTierVersion struct {
	VersionID uuid.UUID `json:"versionId"`
	TierID    uuid.UUID `json:"tierId"`
	TierCode  string    `json:"tierCode"`
	Version   int       `json:"version"`
}

// applyPendingVersions puts the pending threshold versions of a property in
// force (thresholds read by the engine) and returns them.
func applyPendingVersions(ctx context.Context, tx pgx.Tx, property uuid.UUID, today time.Time) ([]AppliedTierVersion, error) {
	rows, err := tx.Query(ctx, `SELECT v.id, v.tier_id, t.code, v.version FROM crm.loyalty_tier_versions v JOIN crm.loyalty_tiers t ON t.id = v.tier_id
		WHERE v.property_id = $1 AND v.status = 'pending' AND t.threshold_version = v.version ORDER BY t.rank, t.code FOR UPDATE OF v`, property)
	if err != nil {
		return nil, err
	}
	out := []AppliedTierVersion{}
	for rows.Next() {
		var a AppliedTierVersion
		if err := rows.Scan(&a.VersionID, &a.TierID, &a.TierCode, &a.Version); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, a := range out {
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_versions SET status = 'superseded' WHERE tier_id = $1 AND status = 'effective'`, a.TierID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_versions SET status = 'effective', effective_on = $2 WHERE id = $1`, a.VersionID, today); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tiers t SET eff_min_points = v.min_points, eff_min_spend = v.min_spend, eff_qualify_mode = v.qualify_mode,
			eff_membership_type_ids = v.membership_type_ids, eff_period_months = v.period_months, effective_version = v.version, pending_thresholds = false
			FROM crm.loyalty_tier_versions v WHERE v.id = $1 AND t.id = v.tier_id`, a.VersionID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ── views ─────────────────────────────────────────────────────────────────

// LoyaltyTierVersion is a threshold version of a tier.
type LoyaltyTierVersion struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	TierID            uuid.UUID  `json:"tierId" db:"tier_id"`
	Version           int        `json:"version" db:"version"`
	MinPoints         int64      `json:"minPoints" db:"min_points"`
	MinSpend          string     `json:"minSpend" db:"min_spend"`
	QualifyMode       string     `json:"qualifyMode" db:"qualify_mode" enum:"all,any"`
	MembershipTypeIDs []string   `json:"membershipTypeIds" db:"membership_type_ids"`
	PeriodMonths      *int       `json:"periodMonths" db:"period_months"`
	Status            string     `json:"status" db:"status" enum:"pending,effective,superseded"`
	EffectiveOn       *string    `json:"effectiveOn" db:"effective_on"`
	EvaluationID      *uuid.UUID `json:"evaluationId" db:"evaluation_id"`
	CreatedByName     *string    `json:"createdByName" db:"created_by_name"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

// LoyaltyTierClass is a tier with its badge and the accounts classified in it.
type LoyaltyTierClass struct {
	ID                 uuid.UUID `json:"id" db:"id"`
	Code               string    `json:"code" db:"code"`
	Name               string    `json:"name" db:"name"`
	Rank               int       `json:"rank" db:"rank"`
	Color              string    `json:"color" db:"color"`
	Icon               string    `json:"icon" db:"icon"`
	Status             string    `json:"status" db:"status" enum:"active,inactive"`
	MinSpend           string    `json:"minSpend" db:"min_spend" doc:"Spend threshold in force"`
	MinPoints          int64     `json:"minPoints" db:"min_points" doc:"Points threshold in force"`
	Multiplier         string    `json:"multiplier" db:"multiplier"`
	FnbDiscountPercent string    `json:"fnbDiscountPercent" db:"fnb_discount_percent"`
	BookingWindowDays  int       `json:"bookingWindowDays" db:"booking_window_days"`
	PendingThresholds  bool      `json:"pendingThresholds" db:"pending_thresholds"`
	Accounts           int       `json:"accounts" db:"accounts" doc:"Active loyalty accounts in the tier"`
	ManualAccounts     int       `json:"manualAccounts" db:"manual_accounts" doc:"Of which classified manually"`
}

// LoyaltyTierPreviewLine is the would-be result for one account.
type LoyaltyTierPreviewLine struct {
	AccountID         uuid.UUID `json:"accountId"`
	AccountNumber     string    `json:"accountNumber"`
	CustomerID        uuid.UUID `json:"customerId"`
	CustomerName      string    `json:"customerName"`
	FromTierName      *string   `json:"fromTierName"`
	QualifiedTierName *string   `json:"qualifiedTierName"`
	ToTierName        *string   `json:"toTierName"`
	Outcome           string    `json:"outcome" enum:"upgraded,retained,grace_started,in_grace,downgraded,locked"`
	Spend             string    `json:"spend"`
	Points            int64     `json:"points"`
}

// LoyaltyTierMove counts the accounts moving between two tiers.
type LoyaltyTierMove struct {
	FromTierName *string `json:"fromTierName"`
	ToTierName   *string `json:"toTierName"`
	Direction    string  `json:"direction" enum:"up,down"`
	Accounts     int     `json:"accounts"`
}

// LoyaltyTierEvaluationPreview shows what an evaluation would do now,
// without changing anything (re-evaluate now, FR-LOY-P5-01).
type LoyaltyTierEvaluationPreview struct {
	Kind              string                   `json:"kind" enum:"annual,periodic,grace_review"`
	PendingThresholds bool                     `json:"pendingThresholds" doc:"Evaluated with the pending threshold versions"`
	PendingTiers      []string                 `json:"pendingTiers" doc:"Codes of the tiers with pending thresholds"`
	Accounts          int                      `json:"accounts"`
	Upgraded          int                      `json:"upgraded"`
	Downgraded        int                      `json:"downgraded"`
	Retained          int                      `json:"retained"`
	GraceStarted      int                      `json:"graceStarted"`
	InGrace           int                      `json:"inGrace"`
	Locked            int                      `json:"locked"`
	Moves             []LoyaltyTierMove        `json:"moves"`
	Lines             []LoyaltyTierPreviewLine `json:"lines" doc:"Accounts whose tier or grace would change (first 500)"`
}

// PreviewTierEvaluation runs the evaluation engine without writing.
func (m *Module) PreviewTierEvaluation(ctx context.Context, tx pgx.Tx, property uuid.UUID, kind string, pending bool) (LoyaltyTierEvaluationPreview, error) {
	if !slices.Contains([]string{EvalAnnual, EvalPeriodic, EvalGraceReview}, kind) {
		return LoyaltyTierEvaluationPreview{}, handle.Invalid("kind", "invalid_kind", "kind must be annual, periodic or grace_review")
	}
	out := LoyaltyTierEvaluationPreview{Kind: kind, PendingThresholds: pending, PendingTiers: []string{}, Moves: []LoyaltyTierMove{},
		Lines: []LoyaltyTierPreviewLine{}}
	rows, err := tx.Query(ctx, `SELECT code FROM crm.loyalty_tiers WHERE property_id = $1 AND pending_thresholds AND archived_at IS NULL ORDER BY rank, code`, property)
	if err != nil {
		return out, err
	}
	if out.PendingTiers, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return out, err
	}
	plan, _, _, err := m.planTierEvaluation(ctx, tx, property, kind, nil, false, pending)
	if err != nil {
		return out, err
	}
	names := map[uuid.UUID]string{}
	nrows, err := tx.Query(ctx, `SELECT id, name FROM crm.loyalty_tiers WHERE property_id = $1`, property)
	if err != nil {
		return out, err
	}
	for nrows.Next() {
		var tid uuid.UUID
		var n string
		if err := nrows.Scan(&tid, &n); err != nil {
			nrows.Close()
			return out, err
		}
		names[tid] = n
	}
	nrows.Close()
	name := func(t *uuid.UUID) *string {
		if t == nil {
			return nil
		}
		n := names[*t]
		return &n
	}
	moves := map[string]*LoyaltyTierMove{}
	var keys []string
	for _, it := range plan {
		out.Accounts++
		switch it.D.Outcome {
		case OutcomeUpgraded:
			out.Upgraded++
		case OutcomeDowngraded:
			out.Downgraded++
		case OutcomeRetained:
			out.Retained++
		case OutcomeGraceStarted:
			out.GraceStarted++
		case OutcomeInGrace:
			out.InGrace++
		case OutcomeLocked:
			out.Locked++
		}
		if it.D.Outcome == OutcomeUpgraded || it.D.Outcome == OutcomeDowngraded {
			dir := "up"
			if it.D.Outcome == OutcomeDowngraded {
				dir = "down"
			}
			k := fmt.Sprint(it.Account.TierID, it.D.To, dir)
			if moves[k] == nil {
				moves[k] = &LoyaltyTierMove{FromTierName: name(it.Account.TierID), ToTierName: name(it.D.To), Direction: dir}
				keys = append(keys, k)
			}
			moves[k].Accounts++
		}
		if it.D.Outcome != OutcomeRetained && it.D.Outcome != OutcomeLocked && it.D.Outcome != OutcomeInGrace && len(out.Lines) < 500 {
			var q *uuid.UUID
			if it.Qual != nil {
				q = &it.Qual.ID
			}
			out.Lines = append(out.Lines, LoyaltyTierPreviewLine{AccountID: it.Account.ID, AccountNumber: it.Account.Number, CustomerID: it.Account.CustomerID,
				CustomerName: it.Account.CustomerName, FromTierName: name(it.Account.TierID), QualifiedTierName: name(q), ToTierName: name(it.D.To),
				Outcome: it.D.Outcome, Spend: it.Basis.Spend.String(), Points: it.Basis.Points})
		}
	}
	for _, k := range keys {
		out.Moves = append(out.Moves, *moves[k])
	}
	sort.SliceStable(out.Lines, func(i, j int) bool { return out.Lines[i].CustomerName < out.Lines[j].CustomerName })
	return out, nil
}

// registerTierMaster adds the routes of the tier master (versions, classes,
// re-evaluate now preview).
func (m *Module) registerTierMaster(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Loyalty"
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodDelete, Path: "/api/v1/crm/loyalty/tiers/{id}",
		Summary: "Delete Loyalty Tier (only when no account has ever been in it; otherwise archived)", Permission: "crm.loyalty_tier.delete",
		Response: LoyaltyTierRemoval{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (LoyaltyTierRemoval, error) {
			tid, err := handle.ID(r)
			if err != nil {
				return LoyaltyTierRemoval{}, err
			}
			pid := handle.Property(ctx)
			out, before, err := RemoveTier(ctx, tx, pid, tid)
			if err != nil {
				return out, err
			}
			action := audit.ActionDelete
			if out.Result == "archived" {
				action = audit.ActionArchive
			}
			return out, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: action, EntityType: Tiers.Key, EntityID: tid.String(),
				EntityLabel: fmt.Sprint(before["code"], " · ", before["name"]), PropertyID: &pid, Before: before})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-classes", Summary: "Tier classes: badge, thresholds in force and accounts per tier",
		Permission: "crm.loyalty_tier.view", Response: LoyaltyTierClass{}, List: true, Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTierClass], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LoyaltyTierClass](tx.Query(ctx, `SELECT t.id, t.code, t.name, t.rank, t.color, t.icon, t.status,
				trim_scale(t.eff_min_spend)::text AS min_spend, t.eff_min_points AS min_points, trim_scale(t.multiplier)::text AS multiplier,
				trim_scale(t.fnb_discount_percent)::text AS fnb_discount_percent, t.booking_window_days, t.pending_thresholds,
				(SELECT count(*) FROM crm.loyalty_accounts a WHERE a.tier_id = t.id AND a.status = 'active')::int AS accounts,
				(SELECT count(*) FROM crm.loyalty_accounts a WHERE a.tier_id = t.id AND a.status = 'active' AND a.tier_source = 'manual')::int AS manual_accounts
				FROM crm.loyalty_tiers t WHERE t.property_id = $1 AND t.archived_at IS NULL AND ($2 = '' OR t.status = $2) ORDER BY t.rank, t.code`,
				handle.Property(ctx), lp.Filters["status"])))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tiers/{id}/versions", Summary: "Threshold versions of a tier (pending, in force, superseded)",
		Permission: "crm.loyalty_tier.view", Response: LoyaltyTierVersion{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTierVersion], error) {
			tid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[LoyaltyTierVersion]{}, err
			}
			return handle.Page(handle.List[LoyaltyTierVersion](tx.Query(ctx, `SELECT v.id, v.tier_id, v.version, v.min_points, trim_scale(v.min_spend)::text AS min_spend,
				v.qualify_mode, v.membership_type_ids, v.period_months, v.status, to_char(v.effective_on, 'YYYY-MM-DD') AS effective_on, v.evaluation_id,
				u.full_name AS created_by_name, v.created_at FROM crm.loyalty_tier_versions v LEFT JOIN platform.users u ON u.id = v.created_by
				WHERE v.tier_id = $1 AND v.property_id = $2 ORDER BY v.version DESC`, tid, handle.Property(ctx))))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-evaluations:preview",
		Summary: "Preview a tier evaluation (re-evaluate now): members moving up / down, nothing changed", Permission: "crm.loyalty_tier.view",
		Response: LoyaltyTierEvaluationPreview{}, Query: []route.Param{{Name: "kind", Description: "annual (default), periodic, grace_review"},
			{Name: "pending", Type: "boolean", Description: "Evaluate with the pending thresholds (default true)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (LoyaltyTierEvaluationPreview, error) {
			kind := r.URL.Query().Get("kind")
			if kind == "" {
				kind = EvalAnnual
			}
			pending := strings.ToLower(r.URL.Query().Get("pending")) != "false"
			return m.PreviewTierEvaluation(ctx, tx, handle.Property(ctx), kind, pending)
		})})
}
