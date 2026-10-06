package loyalty

// "On member": the tier class of members and customers for the screens that
// show the badge (Membership → Members list and filter, member detail,
// Customer 360, front desk / POS customer picker, golf booking member
// lookup). Membership data is read through the reporting read model
// reporting.crm_member_tiers (crm cannot import membership).

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// LoyaltyMemberTier is a membership with the tier class of its member
// (Members list: column and filter by tier).
type LoyaltyMemberTier struct {
	ID            uuid.UUID  `json:"id" db:"membership_id" doc:"Membership id"`
	MemberID      uuid.UUID  `json:"memberId" db:"member_id"`
	MemberNo      string     `json:"memberNo" db:"member_no"`
	MemberName    string     `json:"memberName" db:"member_name"`
	CustomerID    *uuid.UUID `json:"customerId" db:"customer_id"`
	TypeName      string     `json:"typeName" db:"type_name"`
	Role          string     `json:"role" db:"role"`
	StartsOn      *string    `json:"startsOn" db:"starts_on"`
	EndsOn        *string    `json:"endsOn" db:"ends_on"`
	Status        string     `json:"status" db:"status"`
	AccountID     *uuid.UUID `json:"accountId" db:"account_id" doc:"Loyalty account (null: not enrolled)"`
	TierID        *uuid.UUID `json:"tierId" db:"tier_id"`
	TierCode      *string    `json:"tierCode" db:"tier_code"`
	TierName      *string    `json:"tierName" db:"tier_name"`
	TierRank      *int       `json:"tierRank" db:"tier_rank"`
	TierColor     *string    `json:"tierColor" db:"tier_color"`
	TierIcon      *string    `json:"tierIcon" db:"tier_icon"`
	TierSource    *string    `json:"tierSource" db:"tier_source" enum:"auto,manual"`
	OverrideUntil *string    `json:"overrideUntil" db:"override_until"`
}

// LoyaltyCustomerTier is the tier badge of a customer.
type LoyaltyCustomerTier struct {
	CustomerID uuid.UUID  `json:"customerId" db:"customer_id"`
	AccountID  uuid.UUID  `json:"accountId" db:"account_id"`
	TierID     *uuid.UUID `json:"tierId" db:"tier_id"`
	TierCode   *string    `json:"tierCode" db:"tier_code"`
	TierName   *string    `json:"tierName" db:"tier_name"`
	TierRank   *int       `json:"tierRank" db:"tier_rank"`
	TierColor  *string    `json:"tierColor" db:"tier_color"`
	TierIcon   *string    `json:"tierIcon" db:"tier_icon"`
	TierSource string     `json:"tierSource" db:"tier_source" enum:"auto,manual"`
	GraceUntil *string    `json:"graceUntil" db:"grace_until"`
}

const memberTierCols = `membership_id, member_id, member_no, member_name, customer_id, type_name, role, to_char(starts_on, 'YYYY-MM-DD') AS starts_on,
	to_char(ends_on, 'YYYY-MM-DD') AS ends_on, status, account_id, tier_id, tier_code, tier_name, tier_rank, tier_color, tier_icon, tier_source,
	to_char(override_until, 'YYYY-MM-DD') AS override_until FROM reporting.crm_member_tiers`

// registerTierLookup adds the badge lookups.
func (m *Module) registerTierLookup(reg *route.Registry) {
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Scope, rt.Tag = "crm", route.ScopeProperty, "Loyalty"
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/member-tiers", Summary: "Memberships with the tier class of the member (Members by tier)",
		Permission: "crm.loyalty_account.view", Response: LoyaltyMemberTier{}, List: true,
		Query: []route.Param{{Name: "q"}, {Name: "filter[status]"}, {Name: "filter[tierId]", Description: "Tier id, or none (not classified)"},
			{Name: "filter[memberNo]"}, {Name: "filter[customerId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyMemberTier], error) {
			lp := httpx.ParseList(r)
			tier := lp.Filters["tierId"]
			if tier != "" && tier != "none" {
				if _, err := uuid.Parse(tier); err != nil {
					return httpx.Page[LoyaltyMemberTier]{}, handle.Invalid("filter[tierId]", "invalid_uuid", "tierId must be a tier id or none")
				}
			}
			return handle.Page(handle.List[LoyaltyMemberTier](tx.Query(ctx, `SELECT `+memberTierCols+` WHERE property_id = $1 AND ($2 = '' OR status = $2)
				AND ($3 = '' OR ($3 = 'none' AND tier_id IS NULL) OR tier_id::text = $3) AND ($4 = '' OR upper(member_no) = upper($4))
				AND ($5 = '' OR customer_id::text = $5) AND ($6 = '' OR member_no ILIKE '%' || $6 || '%' OR member_name ILIKE '%' || $6 || '%')
				ORDER BY member_name, member_no, membership_id LIMIT $7`, handle.Property(ctx), lp.Filters["status"], tier, strings.TrimSpace(lp.Filters["memberNo"]),
				lp.Filters["customerId"], lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/crm/loyalty/customer-tiers", Summary: "Tier badges of customers (lists and pickers)",
		Permission: "crm.loyalty_account.view", Response: LoyaltyCustomerTier{}, List: true,
		Query: []route.Param{{Name: "customerIds", Required: true, Description: "Comma-separated customer ids (max 200)"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LoyaltyCustomerTier], error) {
			var ids []uuid.UUID
			for _, s := range strings.Split(r.URL.Query().Get("customerIds"), ",") {
				if s = strings.TrimSpace(s); s == "" {
					continue
				}
				u, err := uuid.Parse(s)
				if err != nil {
					return httpx.Page[LoyaltyCustomerTier]{}, handle.Invalid("customerIds", "invalid_uuid", "customer ids must be UUIDs")
				}
				ids = append(ids, u)
			}
			if len(ids) == 0 || len(ids) > 200 {
				return httpx.Page[LoyaltyCustomerTier]{}, handle.Invalid("customerIds", "required", "1 to 200 customer ids")
			}
			return handle.Page(handle.List[LoyaltyCustomerTier](tx.Query(ctx, `SELECT a.customer_id, a.id AS account_id, a.tier_id, t.code AS tier_code,
				t.name AS tier_name, t.rank AS tier_rank, t.color AS tier_color, t.icon AS tier_icon, a.tier_source,
				to_char(a.grace_until, 'YYYY-MM-DD') AS grace_until FROM crm.loyalty_accounts a LEFT JOIN crm.loyalty_tiers t ON t.id = a.tier_id
				WHERE a.property_id = $1 AND a.status = 'active' AND a.customer_id = ANY($2) ORDER BY a.customer_id`, handle.Property(ctx), ids)))
		})})
}

// RegisterTiers adds the routes of the tier master, classification
// overrides and badge lookups (wired by internal/app).
func (m *Module) RegisterTiers(reg *route.Registry) {
	m.registerTierMaster(reg)
	m.registerTierOverrides(reg)
	m.registerTierLookup(reg)
}
