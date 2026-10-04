package billing

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/catalog"
	"oneclub/internal/platform/handle"
)

// DeferredSummaryRow is one liability type × entry type total.
type DeferredSummaryRow struct {
	LiabilityType string `json:"liabilityType" db:"liability_type"`
	EntryType     string `json:"entryType" db:"entry_type"`
	Amount        string `json:"amount" db:"amount"`
}

// DeferredSummary is the deferred revenue sub-ledger position.
type DeferredSummary struct {
	At        time.Time            `json:"at"`
	Liability string               `json:"liability"`
	ByType    []DeferredSummaryRow `json:"byType"`
}

// AccountLimit is the cached member charge limit of an offline POS terminal.
type AccountLimit struct {
	AccountID   string  `json:"accountId" db:"id"`
	Number      string  `json:"number" db:"number"`
	CustomerID  string  `json:"customerId" db:"customer_id"`
	CreditLimit *string `json:"creditLimit" db:"credit_limit" doc:"null: the Member Policy default"`
	Balance     string  `json:"balance" db:"balance"`
}

// registerP2 adds the P2 billing routes (PRD P2 EP-03).
func (h *HTTP) registerP2(reg *route.Registry) {
	s, db := h.Svc, h.Svc.DB
	s.registerOnline(reg)
	add := func(rt route.Route) {
		rt.Module, rt.Scope = "billing", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/customer-accounts:limit-cache", Tag: "Customer Accounts",
		Summary: "Member charge limits and balances for the offline POS (C2 offline cache)", Permission: "billing.customer_account.charge",
		Response: AccountLimit{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[AccountLimit], error) {
			return handle.Page(handle.List[AccountLimit](tx.Query(ctx, `SELECT a.id::text AS id, a.number, a.customer_id::text AS customer_id,
				a.credit_limit::text AS credit_limit, coalesce((SELECT sum(amount) FROM billing.account_entries e WHERE e.account_id = a.id), 0)::text AS balance
				FROM billing.customer_accounts a WHERE a.property_id = $1 AND a.account_type = 'member' AND a.status = 'active' ORDER BY a.number`,
				handle.Property(ctx))))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/billing/deferred-revenue", Tag: "Deferred Revenue",
		Summary: "Deferred revenue sub-ledger position (voucher, prepaid, annual fee)", Permission: "billing.deferred_revenue.view",
		Response: DeferredSummary{}, Query: []route.Param{{Name: "at", Description: "RFC 3339; default now"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (DeferredSummary, error) {
			at, err := handle.QueryTime(r, "at", clock.Now())
			if err != nil {
				return DeferredSummary{}, err
			}
			pid := handle.Property(ctx)
			total, err := Liability(ctx, tx, pid, "", at)
			if err != nil {
				return DeferredSummary{}, err
			}
			rows, err := handle.List[DeferredSummaryRow](tx.Query(ctx, `SELECT liability_type, entry_type, sum(amount)::text AS amount
				FROM billing.deferred_revenue_entries WHERE property_id = $1 AND occurred_at <= $2 GROUP BY 1, 2 ORDER BY 1, 2`, pid, at))
			for i := range rows {
				rows[i].Amount = dec(rows[i].Amount).String()
			}
			return DeferredSummary{At: at, Liability: total.String(), ByType: rows}, err
		})})
}

// p2Contribution is the P2 part of the billing catalogue.
func p2Contribution() catalog.Contribution {
	// frontline of the P2 business lines: open folios, post charges, take
	// payments and member charges (incl. the offline POS limit cache).
	frontline := []string{"billing.folio.view", "billing.folio.create", "billing.folio.add_charge", "billing.folio.close", "billing.payment.view",
		"billing.payment.create", "billing.customer_account.view", "billing.customer_account.charge", "billing.payment_method.view"}
	lead := append(append([]string{}, frontline...), "billing.folio.void", "billing.refund.view", "billing.refund.create", "billing.reconciliation.view")
	return catalog.Contribution{
		Permissions: append(catalog.P("billing", "deferred_revenue", "view"), catalog.P("billing", "customer_account", "charge")...),
		RolePermissions: map[string][]string{
			"property_admin":          {"billing.deferred_revenue.view", "billing.customer_account.charge"},
			"finance_manager":         {"billing.deferred_revenue.view"},
			"accountant":              {"billing.deferred_revenue.view"},
			"general_manager":         {"billing.deferred_revenue.view"},
			"cashier":                 frontline,
			"front_desk":              frontline,
			"reservation_staff":       frontline,
			"pos_staff":               {"billing.folio.view", "billing.customer_account.charge"},
			"outlet_manager":          lead,
			"sport_club_receptionist": frontline,
			"sport_club_manager":      lead,
			"driving_range_staff":     frontline,
			"membership_admin":        {"billing.customer_account.charge"},
			"golf_manager":            {"billing.folio.add_charge", "billing.folio.void"},
		},
	}
}
