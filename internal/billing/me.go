package billing

// Online payment of a folio balance (PRD P2 FR-APP-P2-07, FR-WEB-P2-04):
// staff send a payment link / QRIS for any folio, the member pays their
// own folios from the Member App. Both create a P1 gateway payment that the
// signed webhook settles. The member's folio list, payments and statements
// are P1's /api/v1/member routes.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PayOnlineInput opens a gateway payment for a folio balance.
type PayOnlineInput struct {
	Method string `json:"method,omitempty" enum:"qris,virtual_account,card"`
	Amount string `json:"amount,omitempty" doc:"Default: the folio balance"`
}

// payOnline creates the pending gateway payment for (part of) the balance.
func (s *Service) payOnline(ctx context.Context, tx pgx.Tx, folio FolioDetail, in PayOnlineInput, payer string) (Payment, error) {
	amt, err := handle.Decimal("amount", in.Amount, decimal.Zero)
	if err != nil {
		return Payment{}, err
	}
	due := dec(folio.Summary.Balance)
	if !due.IsPositive() {
		return Payment{}, errs.Conflict("nothing_due", "the folio has no balance to pay")
	}
	if amt.IsZero() || amt.GreaterThan(due) {
		amt = due
	}
	method := in.Method
	if method == "" {
		method = "qris"
	}
	return s.TakePayment(ctx, tx, PaymentInput{FolioID: &folio.ID, MethodType: method, Channel: "online", Amount: amt,
		Description: folio.Number, PayerName: payer})
}

func (s *Service) registerOnline(reg *route.Registry) {
	db := s.DB
	reg.Add(route.Route{Method: http.MethodPost, Path: "/api/v1/billing/folios/{id}:online-payment", Summary: "Create a payment link / QRIS for a folio balance",
		Module: "billing", Tag: "Billing", Scope: route.ScopeProperty, Permission: "billing.payment.create", Request: PayOnlineInput{}, Response: Payment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayOnlineInput) (Payment, error) {
			fid, err := handle.ID(r)
			if err != nil {
				return Payment{}, err
			}
			f, err := GetFolio(ctx, tx, fid)
			if err != nil {
				return Payment{}, err
			}
			return s.payOnline(ctx, tx, f, in, f.HolderName)
		})})
	myFolio := func(ctx context.Context, tx pgx.Tx, r *http.Request) (FolioDetail, crm.Customer, error) {
		me, err := crm.Me(ctx, tx)
		if err != nil {
			return FolioDetail{}, me, err
		}
		fid, err := handle.ID(r)
		if err != nil {
			return FolioDetail{}, me, err
		}
		f, err := GetFolio(ctx, tx, fid)
		if err != nil {
			return f, me, err
		}
		if f.CustomerID == nil || *f.CustomerID != me.ID {
			return FolioDetail{}, me, errs.NotFound("folio")
		}
		return f, me, nil
	}
	crm.MeRoute(reg, "billing", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/folios/{id}", Summary: "My folio with charges and payments",
		Response: FolioDetail{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (FolioDetail, error) {
			f, _, err := myFolio(ctx, tx, r)
			return f, err
		})})
	crm.MeRoute(reg, "billing", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/folios/{id}:pay-online", Summary: "Pay my folio online (QRIS, VA, card)",
		Request: PayOnlineInput{}, Response: Payment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayOnlineInput) (Payment, error) {
			f, me, err := myFolio(ctx, tx, r)
			if err != nil {
				return Payment{}, err
			}
			return s.payOnline(ctx, tx, f, in, me.Name)
		})})
	crm.MeRoute(reg, "billing", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/payments/{id}", Summary: "My payment (online payment status)",
		Response: Payment{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (Payment, error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return Payment{}, err
			}
			pid, err := handle.ID(r)
			if err != nil {
				return Payment{}, err
			}
			p, err := GetPayment(ctx, tx, pid)
			if err != nil {
				return p, err
			}
			var owner *string
			if p.FolioID != nil {
				_ = tx.QueryRow(ctx, `SELECT customer_id::text FROM billing.folios WHERE id = $1`, *p.FolioID).Scan(&owner)
			}
			if owner == nil || *owner != me.ID.String() {
				return Payment{}, errs.NotFound("payment")
			}
			return p, nil
		})})
}
