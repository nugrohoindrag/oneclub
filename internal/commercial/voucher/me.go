package voucher

// Member Portal vouchers (PRD P2 EP-25 FR-APP-P2-05): My Vouchers, Prepaid
// Balance, Voucher History, Redemption History, buy a voucher or package.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

type MyVoucherPurchase struct {
	VoucherTypeID uuid.UUID `json:"voucherTypeId"`
	Count         int       `json:"count,omitempty"`
	MemberCharge  bool      `json:"memberCharge,omitempty" doc:"Pay from my member account; otherwise pay the folio online"`
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "commercial", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/vouchers", Summary: "My Vouchers", Response: Voucher{}, List: true,
		Query: []route.Param{{Name: "filter[status]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Voucher], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Voucher]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[Voucher](tx.Query(ctx, voucherSelect+` WHERE v.customer_id = $1 AND ($2 = '' OR v.status = $2)
				ORDER BY v.issued_at DESC LIMIT $3`, p.ID, lp.Filters["status"], lp.Limit)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/prepaid-balances", Summary: "My Prepaid Balance", Response: PrepaidBalance{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PrepaidBalance], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[PrepaidBalance]{}, err
			}
			return handle.Page(handle.List[PrepaidBalance](tx.Query(ctx, `SELECT v.customer_id, c.name AS customer_name, t.code AS type_code, t.name AS type_name,
				t.category, t.unit, trim_scale(sum(v.remaining_quantity))::text AS remaining, count(*)::int AS vouchers, min(v.expires_at) AS next_expiry
				FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id JOIN crm.customers c ON c.id = v.customer_id
				WHERE v.customer_id = $1 AND t.prepaid AND v.status IN ('active', 'partially_redeemed') GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 4`, p.ID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/voucher-history", Summary: "Voucher History & Redemption History", Response: LedgerEntry{}, List: true,
		Query: []route.Param{{Name: "filter[entryType]", Description: "issue | redemption | expiry …"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[LedgerEntry], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[LedgerEntry]{}, err
			}
			lp := httpx.ParseList(r)
			return handle.Page(handle.List[LedgerEntry](tx.Query(ctx, ledgerSelect+` WHERE v.customer_id = $1 AND ($2 = '' OR l.entry_type = $2)
				ORDER BY l.created_at DESC LIMIT $3`, p.ID, lp.Filters["entryType"], lp.Limit)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/vouchers:buy", Summary: "Buy a voucher or package (member charge or online)",
		Request: MyVoucherPurchase{}, Response: SaleResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyVoucherPurchase) (SaleResult, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return SaleResult{}, err
			}
			var pay *SalePayment
			if in.MemberCharge {
				pay = &SalePayment{MethodType: "member_account"}
			}
			return m.Sell(ctx, tx, p.PropertyID, SellInput{VoucherTypeID: in.VoucherTypeID, CustomerID: &p.ID, Count: in.Count, Channel: "member_app",
				Payment: pay}, r.Header.Get("Idempotency-Key"))
		})})
}
