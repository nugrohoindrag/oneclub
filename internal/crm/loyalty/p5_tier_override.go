package loyalty

// Manual classification override (product owner request on PRD P5 EP-18):
// staff with crm.loyalty.tier_override put a member in a tier for a reason,
// optionally until a date, through the approval engine (document type
// loyalty_tier_override; no workflow ⇒ applied at once). While the override
// is in force the tier is locked (evaluations record "locked"); when it ends
// (valid until passed, or revoked) the member returns to the tier held
// before and the evaluations classify the account automatically again.
// Every change keeps its source (auto / manual) in the tier history.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/platform/provision"
)

// PermTierOverride may classify a member manually (and approve it in a workflow).
const PermTierOverride = "crm.loyalty.tier_override"

// TierOverrideDocumentType is the approval of manual classification overrides.
var TierOverrideDocumentType = provision.DocumentType{Code: "loyalty_tier_override", Module: "crm", Name: "Loyalty Tier Override",
	Attributes: []provision.DocumentAttribute{{Key: "tierRank", Label: "Tier rank", Type: "number"},
		{Key: "rankChange", Label: "Rank change (+ up, − down)", Type: "number"}, {Key: "tierCode", Label: "Tier code", Type: "string"}}}

// TierPermissions are the permissions of the tier classification.
func TierPermissions() []catalog.Permission {
	return catalog.P("crm", "loyalty", "tier_override")
}

// TierRolePermissions grant the manual classification.
func TierRolePermissions() map[string][]string {
	p := []string{PermTierOverride}
	return map[string][]string{"property_admin": p, "crm_admin": p, "membership_manager": p, "general_manager": p, "club_manager": p}
}

// LoyaltyTierOverride is a manual classification of a member.
type LoyaltyTierOverride struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Number            string     `json:"number" db:"number"`
	AccountID         uuid.UUID  `json:"accountId" db:"account_id"`
	AccountNumber     string     `json:"accountNumber" db:"account_number"`
	CustomerID        uuid.UUID  `json:"customerId" db:"customer_id"`
	CustomerName      string     `json:"customerName" db:"customer_name"`
	TierID            uuid.UUID  `json:"tierId" db:"tier_id"`
	TierCode          string     `json:"tierCode" db:"tier_code"`
	TierName          string     `json:"tierName" db:"tier_name"`
	PreviousTierID    *uuid.UUID `json:"previousTierId" db:"previous_tier_id"`
	PreviousTierName  *string    `json:"previousTierName" db:"previous_tier_name"`
	Reason            string     `json:"reason" db:"reason"`
	ValidUntil        *string    `json:"validUntil" db:"valid_until"`
	Status            string     `json:"status" db:"status" enum:"pending,active,ended,revoked,rejected,cancelled"`
	ApprovalRequestID *uuid.UUID `json:"approvalRequestId" db:"approval_request_id"`
	AppliedAt         *time.Time `json:"appliedAt" db:"applied_at"`
	EndedAt           *time.Time `json:"endedAt" db:"ended_at"`
	EndReason         *string    `json:"endReason" db:"end_reason"`
	CreatedByName     *string    `json:"createdByName" db:"created_by_name"`
	CreatedAt         time.Time  `json:"createdAt" db:"created_at"`
}

const overrideSelect = `SELECT o.id, o.number, o.account_id, a.number AS account_number, o.customer_id, c.name AS customer_name, o.tier_id, t.code AS tier_code,
	t.name AS tier_name, o.previous_tier_id, pt.name AS previous_tier_name, o.reason, to_char(o.valid_until, 'YYYY-MM-DD') AS valid_until, o.status,
	o.approval_request_id, o.applied_at, o.ended_at, o.end_reason, u.full_name AS created_by_name, o.created_at
	FROM crm.loyalty_tier_overrides o JOIN crm.loyalty_accounts a ON a.id = o.account_id JOIN crm.customers c ON c.id = o.customer_id
	JOIN crm.loyalty_tiers t ON t.id = o.tier_id LEFT JOIN crm.loyalty_tiers pt ON pt.id = o.previous_tier_id LEFT JOIN platform.users u ON u.id = o.created_by`

// GetTierOverride loads an override.
func GetTierOverride(ctx context.Context, q dbtx.Querier, oid uuid.UUID) (LoyaltyTierOverride, error) {
	rows, err := q.Query(ctx, overrideSelect+` WHERE o.id = $1`, oid)
	return handle.One[LoyaltyTierOverride](rows, err, "tier override")
}

// LoyaltyTierOverrideInput classifies a member manually.
type LoyaltyTierOverrideInput struct {
	TierID     uuid.UUID `json:"tierId"`
	Reason     string    `json:"reason"`
	ValidUntil string    `json:"validUntil,omitempty" doc:"Last day of the override (YYYY-MM-DD); empty = until revoked"`
}

// LoyaltyTierOverrideEndInput revokes an override.
type LoyaltyTierOverrideEndInput struct {
	Reason string `json:"reason"`
}

// RequestTierOverride records an override and submits it for approval
// (applied at once without a workflow).
func (m *Module) RequestTierOverride(ctx context.Context, tx pgx.Tx, property, aid uuid.UUID, in LoyaltyTierOverrideInput) (LoyaltyTierOverride, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return LoyaltyTierOverride{}, err
	}
	a, err := requireAccount(ctx, tx, property, aid)
	if err != nil {
		return LoyaltyTierOverride{}, err
	}
	if a.Status != "active" {
		return LoyaltyTierOverride{}, errs.Conflict("account_inactive", "loyalty account "+a.Number+" is "+a.Status)
	}
	var code string
	var rank int
	if err := tx.QueryRow(ctx, `SELECT code, rank FROM crm.loyalty_tiers WHERE id = $1 AND property_id = $2 AND status = 'active' AND archived_at IS NULL`,
		in.TierID, property).Scan(&code, &rank); err != nil {
		if dbtx.IsNoRows(err) {
			return LoyaltyTierOverride{}, handle.Invalid("tierId", "not_found", "active tier not found in this property")
		}
		return LoyaltyTierOverride{}, err
	}
	today := localToday(ctx, tx, property)
	var until *time.Time
	if s := strings.TrimSpace(in.ValidUntil); s != "" {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			return LoyaltyTierOverride{}, handle.Invalid("validUntil", "invalid_date", "must be a date (YYYY-MM-DD)")
		}
		if d.Before(today) {
			return LoyaltyTierOverride{}, handle.Invalid("validUntil", "in_past", "valid until must not be before today")
		}
		until = &d
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm.loyalty_tier_overrides WHERE account_id = $1 AND status = 'pending')`, aid).Scan(&open); err != nil {
		return LoyaltyTierOverride{}, err
	}
	if open {
		return LoyaltyTierOverride{}, errs.Conflict("override_pending", "an override of "+a.Number+" is waiting for approval")
	}
	num, err := numbering.Next(ctx, tx, property, "TOV", today)
	if err != nil {
		return LoyaltyTierOverride{}, err
	}
	oid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO crm.loyalty_tier_overrides (id, property_id, number, account_id, customer_id, tier_id, reason, valid_until,
		created_by, updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`, oid, property, num, aid, a.CustomerID, in.TierID, in.Reason, until, actor(ctx)); err != nil {
		return LoyaltyTierOverride{}, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionCreate, EntityType: "crm.loyalty_tier_override", EntityID: oid.String(),
		EntityLabel: num + " · " + a.Number, PropertyID: &property, Reason: in.Reason,
		After: map[string]any{"accountId": aid, "tierId": in.TierID, "fromTierId": a.TierID, "validUntil": dateStr(until)}}); err != nil {
		return LoyaltyTierOverride{}, err
	}
	if m.Approvals == nil {
		return LoyaltyTierOverride{}, errs.Unavailable("approvals are not wired")
	}
	change := rank
	if a.TierRank != nil {
		change = rank - *a.TierRank
	}
	rid, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: TierOverrideDocumentType.Code, DocumentID: oid, DocumentRef: num,
		Title: fmt.Sprintf("Classify %s · %s as %s", a.Number, a.CustomerName, code), PropertyID: property,
		Attributes: map[string]any{"tierRank": rank, "rankChange": change, "tierCode": code}})
	if err != nil {
		return LoyaltyTierOverride{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_overrides SET approval_request_id = $2 WHERE id = $1`, oid, rid); err != nil {
		return LoyaltyTierOverride{}, err
	}
	return GetTierOverride(ctx, tx, oid)
}

// TierOverrideDecision applies the approval outcome of an override.
func (m *Module) TierOverrideDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	var status string
	var aid, tier uuid.UUID
	var reason string
	err := tx.QueryRow(ctx, `SELECT status, account_id, tier_id, reason FROM crm.loyalty_tier_overrides WHERE id = $1 FOR UPDATE`, d.DocumentID).
		Scan(&status, &aid, &tier, &reason)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil || status != "pending" {
		return err
	}
	switch d.Status {
	case approval.StatusRejected, approval.StatusCancelled:
		st := map[string]string{approval.StatusRejected: "rejected", approval.StatusCancelled: "cancelled"}[d.Status]
		_, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_overrides SET status = $2, ended_at = now(), end_reason = $3 WHERE id = $1`, d.DocumentID, st, d.Reason)
		return err
	case approval.StatusApproved:
	default:
		return nil
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return err
	}
	// An override replacing another one keeps the tier held before the first.
	prev, prevLocked := a.TierID, a.TierLocked
	var cur *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT tier_override_id FROM crm.loyalty_accounts WHERE id = $1`, aid).Scan(&cur); err != nil {
		return err
	}
	if cur != nil {
		if err := tx.QueryRow(ctx, `SELECT previous_tier_id, previous_locked FROM crm.loyalty_tier_overrides WHERE id = $1`, *cur).Scan(&prev, &prevLocked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_overrides SET status = 'ended', ended_at = now(), end_reason = 'replaced by a new override'
			WHERE id = $1 AND status = 'active'`, *cur); err != nil {
			return err
		}
	}
	oid := d.DocumentID
	if a.TierID == nil || *a.TierID != tier {
		if err := m.changeTierFrom(ctx, tx, &a, &tier, "manual override: "+reason, nil, nil, TierSourceManual, &oid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_locked = true, tier_source = 'manual', tier_override_id = $2, grace_until = NULL,
		grace_tier_id = NULL, tier_since = $3 WHERE id = $1`, aid, oid, localToday(ctx, tx, a.PropertyID)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE crm.loyalty_tier_overrides SET status = 'active', applied_at = now(), previous_tier_id = $2, previous_locked = $3
		WHERE id = $1`, oid, prev, prevLocked)
	return err
}

// EndTierOverride ends an override in force (revoked by staff or expired):
// the member returns to the tier held before.
func (m *Module) EndTierOverride(ctx context.Context, tx pgx.Tx, oid uuid.UUID, status, reason string) (LoyaltyTierOverride, error) {
	var st string
	var aid uuid.UUID
	var prev *uuid.UUID
	var prevLocked bool
	if err := tx.QueryRow(ctx, `SELECT status, account_id, previous_tier_id, previous_locked FROM crm.loyalty_tier_overrides WHERE id = $1 FOR UPDATE`, oid).
		Scan(&st, &aid, &prev, &prevLocked); err != nil {
		if dbtx.IsNoRows(err) {
			return LoyaltyTierOverride{}, errs.NotFound("tier override")
		}
		return LoyaltyTierOverride{}, err
	}
	if st != "active" {
		return LoyaltyTierOverride{}, errs.Conflict("override_not_active", "the override is "+st)
	}
	a, err := lockAccount(ctx, tx, aid)
	if err != nil {
		return LoyaltyTierOverride{}, err
	}
	src := TierSourceAuto
	if prevLocked {
		src = TierSourceManual
	}
	if (prev == nil) != (a.TierID == nil) || (prev != nil && *prev != *a.TierID) {
		if err := m.changeTierFrom(ctx, tx, &a, prev, "override "+status+": "+reason, nil, nil, src, &oid); err != nil {
			return LoyaltyTierOverride{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_accounts SET tier_locked = $2, tier_source = $3, tier_override_id = NULL WHERE id = $1`,
		aid, prevLocked, src); err != nil {
		return LoyaltyTierOverride{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm.loyalty_tier_overrides SET status = $2, ended_at = now(), end_reason = $3, updated_by = $4 WHERE id = $1`,
		oid, status, reason, actor(ctx)); err != nil {
		return LoyaltyTierOverride{}, err
	}
	return GetTierOverride(ctx, tx, oid)
}

// noOverrideInForce refuses a Set Tier while a classification override is
// in force (revoke it first).
func noOverrideInForce(ctx context.Context, q dbtx.Querier, aid uuid.UUID) error {
	var number *string
	err := q.QueryRow(ctx, `SELECT o.number FROM crm.loyalty_accounts a JOIN crm.loyalty_tier_overrides o ON o.id = a.tier_override_id
		WHERE a.id = $1 AND o.status = 'active'`, aid).Scan(&number)
	if dbtx.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errs.Conflict("override_in_force", "classification override "+*number+" is in force; revoke it first")
}

// EndExpiredOverrides ends the overrides whose last day has passed.
func (m *Module) EndExpiredOverrides(ctx context.Context, tx pgx.Tx, property uuid.UUID) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM crm.loyalty_tier_overrides WHERE property_id = $1 AND status = 'active' AND valid_until < $2::date ORDER BY id`,
		property, localToday(ctx, tx, property))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	for _, oid := range ids {
		o, err := m.EndTierOverride(ctx, tx, oid, "ended", "valid until passed")
		if err != nil {
			return 0, err
		}
		pid := property
		if err := audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_tier_override",
			EntityID: oid.String(), EntityLabel: o.Number, PropertyID: &pid, Reason: "valid until passed",
			Before: map[string]any{"status": "active"}, After: map[string]any{"status": o.Status}}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// TierOverrideDailyArgs ends the expired overrides every day.
type TierOverrideDailyArgs struct{}

func (TierOverrideDailyArgs) Kind() string { return "crm_loyalty_tier_override_daily" }

func (TierOverrideDailyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueMaintenance, MaxAttempts: 3}
}

// TierOverrideDailyWorker runs EndExpiredOverrides for every property.
type TierOverrideDailyWorker struct {
	river.WorkerDefaults[TierOverrideDailyArgs]
	M *Module
}

func (w *TierOverrideDailyWorker) Work(ctx context.Context, _ *river.Job[TierOverrideDailyArgs]) error {
	return w.M.RunTierOverridesDaily(ctx)
}

// RunTierOverridesDaily ends the expired overrides of every property.
func (m *Module) RunTierOverridesDaily(ctx context.Context) error {
	ctx = dbtx.System(ctx)
	props, err := properties(ctx, m.DB)
	if err != nil {
		return err
	}
	for _, p := range props {
		if err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := m.EndExpiredOverrides(ctx, tx, p)
			return err
		}); err != nil {
			return fmt.Errorf("tier overrides for %s: %w", p, err)
		}
	}
	return nil
}

// RegisterTierJobs adds the daily override expiry (before the tier programme run at 00:40).
func (m *Module) RegisterTierJobs(reg *jobs.Registrar, loc func() *time.Location) {
	river.AddWorker(reg.Workers, &TierOverrideDailyWorker{M: m})
	reg.Periodic = append(reg.Periodic, river.NewPeriodicJob(jobs.DailyAt{Hour: 0, Minute: 35, Location: loc},
		func() (river.JobArgs, *river.InsertOpts) { return TierOverrideDailyArgs{}, nil }, nil))
}

// LoyaltyTierHistoryEntry is one tier change of an account with its source.
type LoyaltyTierHistoryEntry struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	FromTierName   *string    `json:"fromTierName" db:"from_tier_name"`
	ToTierName     *string    `json:"toTierName" db:"to_tier_name"`
	ToTierColor    *string    `json:"toTierColor" db:"to_tier_color"`
	Direction      string     `json:"direction" db:"direction" enum:"up,down,same"`
	Source         string     `json:"source" db:"source" enum:"auto,manual"`
	Reason         string     `json:"reason" db:"reason"`
	OverrideID     *uuid.UUID `json:"overrideId" db:"override_id"`
	OverrideNumber *string    `json:"overrideNumber" db:"override_number"`
	PointsBasis    *int64     `json:"pointsBasis" db:"points_basis"`
	SpendBasis     *string    `json:"spendBasis" db:"spend_basis"`
	CreatedByName  *string    `json:"createdByName" db:"created_by_name"`
	CreatedAt      time.Time  `json:"createdAt" db:"created_at"`
}

// TierHistory lists the tier changes of an account, newest first.
func TierHistory(ctx context.Context, q dbtx.Querier, aid uuid.UUID) ([]LoyaltyTierHistoryEntry, error) {
	return handle.List[LoyaltyTierHistoryEntry](q.Query(ctx, `SELECT h.id, ft.name AS from_tier_name, tt.name AS to_tier_name, tt.color AS to_tier_color,
		CASE WHEN coalesce(tt.rank, -1) > coalesce(ft.rank, -1) THEN 'up' WHEN coalesce(tt.rank, -1) < coalesce(ft.rank, -1) THEN 'down' ELSE 'same' END AS direction,
		h.source, h.reason, h.override_id, o.number AS override_number, h.points_basis, trim_scale(h.spend_basis)::text AS spend_basis,
		u.full_name AS created_by_name, h.created_at
		FROM crm.loyalty_tier_history h LEFT JOIN crm.loyalty_tiers ft ON ft.id = h.from_tier_id LEFT JOIN crm.loyalty_tiers tt ON tt.id = h.to_tier_id
		LEFT JOIN crm.loyalty_tier_overrides o ON o.id = h.override_id LEFT JOIN platform.users u ON u.id = h.created_by
		WHERE h.account_id = $1 ORDER BY h.created_at DESC, h.id DESC LIMIT 200`, aid))
}

// registerTierOverrides adds the override and history routes.
func (m *Module) registerTierOverrides(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Loyalty"
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/accounts/{id}:override-tier",
		Summary: "Classify a member manually (tier, reason, valid until; approval)", Permission: PermTierOverride, Request: LoyaltyTierOverrideInput{},
		Response: LoyaltyTierOverride{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyTierOverrideInput) (LoyaltyTierOverride, error) {
			aid, err := handle.ID(r)
			if err != nil {
				return LoyaltyTierOverride{}, err
			}
			return m.RequestTierOverride(ctx, tx, handle.Property(ctx), aid, in)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/tier-overrides", Summary: "Manual classification overrides",
		Permission: "crm.loyalty_account.view", Response: LoyaltyTierOverride{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}, {Name: "filter[accountId]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTierOverride], error) {
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LoyaltyTierOverride](tx.Query(ctx, overrideSelect+` WHERE o.property_id = $1 AND ($2 = '' OR o.status = $2)
				AND ($3 = '' OR o.account_id::text = $3) AND ($4 = '' OR o.customer_id::text = $4) ORDER BY o.created_at DESC LIMIT $5`,
				handle.Property(ctx), lp.Filters["status"], lp.Filters["accountId"], lp.Filters["customerId"], lp.Limit)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/crm/loyalty/tier-overrides/{id}:revoke",
		Summary: "End a manual classification now (the member returns to the tier held before)", Permission: PermTierOverride,
		Request: LoyaltyTierOverrideEndInput{}, Response: LoyaltyTierOverride{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in LoyaltyTierOverrideEndInput) (LoyaltyTierOverride, error) {
			oid, err := handle.ID(r)
			if err != nil {
				return LoyaltyTierOverride{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return LoyaltyTierOverride{}, err
			}
			pid := handle.Property(ctx)
			before, err := GetTierOverride(ctx, tx, oid)
			if err != nil {
				return before, err
			}
			var owner uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT property_id FROM crm.loyalty_tier_overrides WHERE id = $1`, oid).Scan(&owner); err != nil || owner != pid {
				return before, errs.NotFound("tier override")
			}
			after, err := m.EndTierOverride(ctx, tx, oid, "revoked", in.Reason)
			if err != nil {
				return after, err
			}
			return after, audit.Record(ctx, tx, audit.Entry{Module: "crm", Action: audit.ActionStatusChange, EntityType: "crm.loyalty_tier_override",
				EntityID: oid.String(), EntityLabel: after.Number, PropertyID: &pid, Reason: in.Reason, Before: map[string]any{"status": before.Status},
				After: map[string]any{"status": after.Status}})
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/accounts/{id}/tier-history", Summary: "Tier history of an account with its source (auto / manual)",
		Permission: "crm.loyalty_account.view", Response: LoyaltyTierHistoryEntry{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyTierHistoryEntry], error) {
			aid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[LoyaltyTierHistoryEntry]{}, err
			}
			if _, err := requireAccount(ctx, tx, handle.Property(ctx), aid); err != nil {
				return httpx.Page[LoyaltyTierHistoryEntry]{}, err
			}
			return handle.Page(TierHistory(ctx, tx, aid))
		})})
}
