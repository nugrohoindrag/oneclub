package commercial

// Member App vouchers & F&B (PRD P2 EP-25 FR-APP-P2-05/06): My Vouchers,
// Prepaid Balance, Voucher History, Redemption History, buy a voucher or
// package, menu and Order Food (pre-order / on-course order).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
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

type MyOrderInput struct {
	ID                 *uuid.UUID  `json:"id,omitempty"`
	OutletID           uuid.UUID   `json:"outletId"`
	OrderType          string      `json:"orderType,omitempty" enum:"pre_order,on_course,takeaway"`
	ServingDestination string      `json:"servingDestination,omitempty" enum:"pickup,hole,halfway_house,table"`
	DestinationRef     string      `json:"destinationRef,omitempty"`
	ScheduledFor       *time.Time  `json:"scheduledFor,omitempty"`
	Lines              []LineInput `json:"lines"`
	Notes              string      `json:"notes,omitempty"`
	MemberCharge       bool        `json:"memberCharge,omitempty" doc:"Charge to my member account; otherwise the order goes on a folio to pay online"`
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "commercial", "Member App", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/vouchers", Summary: "My Vouchers", Response: Voucher{}, List: true,
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
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/prepaid-balances", Summary: "My Prepaid Balance", Response: PrepaidBalance{}, List: true,
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
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/voucher-history", Summary: "Voucher History & Redemption History", Response: LedgerEntry{}, List: true,
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
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/me/vouchers:buy", Summary: "Buy a voucher or package (member charge or online)",
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
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/outlets", Summary: "Outlets I can order from", Response: MyOutlet{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyOutlet], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MyOutlet]{}, err
			}
			return handle.Page(handle.List[MyOutlet](tx.Query(ctx, `SELECT id, code, name, outlet_type FROM commercial.outlets WHERE property_id = $1
				AND status = 'active' AND archived_at IS NULL ORDER BY name`, p.PropertyID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/outlets/{id}/menu", Summary: "Menu available now (member prices)", Response: MenuItem{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MenuItem], error) {
			if _, err := crm.Me(ctx, tx); err != nil {
				return httpx.Page[MenuItem]{}, err
			}
			oid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[MenuItem]{}, err
			}
			return handle.Page(m.MenuNow(ctx, tx, oid, "member_app"))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/me/orders", Summary: "Order Food: pre-order or on-course order", Request: MyOrderInput{},
		Response: Order{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyOrderInput) (Order, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return Order{}, err
			}
			if in.OrderType == "" {
				in.OrderType = "pre_order"
			}
			if in.ServingDestination == "" {
				in.ServingDestination = "pickup"
			}
			o, err := m.CreateOrder(ctx, tx, p.PropertyID, OrderInput{ID: in.ID, OutletID: in.OutletID, OrderType: in.OrderType, Source: "member_app",
				CustomerID: &p.ID, ServingDestination: in.ServingDestination, DestinationRef: in.DestinationRef, ScheduledFor: in.ScheduledFor,
				Lines: in.Lines, Send: in.ScheduledFor == nil, Notes: in.Notes})
			if err != nil || o.Status != "open" {
				return o, err
			}
			if in.MemberCharge {
				return m.Pay(ctx, tx, o.ID, PayInput{CustomerID: &p.ID, Tenders: []TenderInput{{MethodType: "member_account"}}}, "me-"+o.ID.String())
			}
			f, err := m.Billing.OpenFolio(ctx, tx, billing.FolioInput{Property: p.PropertyID, BusinessLine: billing.LinePOS,
				CustomerID: &p.ID, SourceType: "pos_order", SourceID: &o.ID})
			if err != nil {
				return o, err
			}
			return m.ChargeToFolio(ctx, tx, o.ID, f.ID)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/orders", Summary: "My orders & their kitchen status", Response: Order{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Order], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Order]{}, err
			}
			return handle.Page(handle.List[Order](tx.Query(ctx, orderSelect+` WHERE o.customer_id = $1 ORDER BY o.created_at DESC LIMIT $2`, p.ID, httpx.ParseList(r).Limit)))
		})})
}

// MyOutlet is an outlet shown in Order Food.
type MyOutlet struct {
	ID         uuid.UUID `json:"id" db:"id"`
	Code       string    `json:"code" db:"code"`
	Name       string    `json:"name" db:"name"`
	OutletType string    `json:"outletType" db:"outlet_type"`
}
