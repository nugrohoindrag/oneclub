package pos

// Member Portal F&B (PRD P2 EP-25 FR-APP-P2-06): menu and Order Food
// (pre-order / on-course order).

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
	me := func(rt route.Route) { crm.MeRoute(reg, "commercial", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/outlets", Summary: "Outlets I can order from", Response: MyOutlet{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MyOutlet], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MyOutlet]{}, err
			}
			return handle.Page(handle.List[MyOutlet](tx.Query(ctx, `SELECT id, code, name, outlet_type FROM commercial.outlets WHERE property_id = $1
				AND status = 'active' AND archived_at IS NULL ORDER BY name`, p.PropertyID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/outlets/{id}/menu", Summary: "Menu available now (member prices)", Response: MenuItem{}, List: true,
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
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/orders", Summary: "Order Food: pre-order or on-course order", Request: MyOrderInput{},
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
			f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: p.PropertyID, CustomerID: &p.ID, SourceType: "pos_order", SourceID: &o.ID}, BusinessLine: billing.LinePOS})
			if err != nil {
				return o, err
			}
			return m.ChargeToFolio(ctx, tx, o.ID, f.ID)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/orders", Summary: "My orders & their kitchen status", Response: Order{}, List: true,
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
