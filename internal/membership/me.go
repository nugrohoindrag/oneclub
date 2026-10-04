package membership

// Member Portal membership self-service of PRD P2 (EP-25 FR-APP-P2-03,
// FR-MBL-15) next to P1's /api/v1/member/membership: every program I belong
// to, my fees with online payment, pause request, renewal and card
// replacement.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PauseInput asks for a pause.
type PauseInput struct {
	From   string `json:"from" doc:"YYYY-MM-DD"`
	Until  string `json:"until" doc:"YYYY-MM-DD"`
	Reason string `json:"reason"`
}

// ReasonInput carries a mandatory reason.
type ReasonInput struct {
	Reason string `json:"reason"`
}

// PayFeeInput pays a fee online.
type PayFeeInput struct {
	Method string `json:"method,omitempty" enum:"qris,virtual_account,card"`
}

// MyRenewal is a renewal started from the Member Portal.
type MyRenewal struct {
	RenewalID uuid.UUID        `json:"renewalId"`
	Payment   *billing.Payment `json:"payment" doc:"Online payment of the renewal fee (none when the fee is zero)"`
}

func parseDay(field, s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return t, handle.Invalid(field, "invalid_date", field+" must be YYYY-MM-DD")
	}
	return t, nil
}

// mine returns my principal membership by id (members only see their own).
func (m *Module) mine(ctx context.Context, tx pgx.Tx, r *http.Request) (context.Context, uuid.UUID, crm.Customer, error) {
	me, err := crm.Me(ctx, tx)
	if err != nil {
		return ctx, uuid.Nil, me, err
	}
	ctx = withProperty(ctx, me.PropertyID)
	mid, err := handle.ID(r)
	if err != nil {
		return ctx, mid, me, err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM membership.memberships ms JOIN membership.members mb ON mb.id = ms.member_id
		WHERE ms.id = $1 AND mb.customer_id = $2 AND ms.role = 'principal')`, mid, me.ID).Scan(&ok); err != nil {
		return ctx, mid, me, err
	}
	if !ok {
		return ctx, mid, me, errs.NotFound("membership")
	}
	return ctx, mid, me, nil
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "membership", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/memberships", Summary: "All my memberships (every program, with entitlements)",
		Response: Active{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Active], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Active]{}, err
			}
			return handle.Page(Memberships(ctx, tx, c.PropertyID, c.ID))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/membership-fees", Summary: "My membership fees (annual fee history and due dates)",
		Response: Fee{}, List: true, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Fee], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Fee]{}, err
			}
			return handle.Page(handle.List[Fee](tx.Query(ctx, feeSelect+` WHERE mb.customer_id = $1 ORDER BY f.due_date DESC`, c.ID)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/membership-fees/{id}:pay-online", Summary: "Pay my membership fee online (QRIS, VA, card)",
		Request: PayFeeInput{}, Response: billing.Payment{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayFeeInput) (billing.Payment, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return billing.Payment{}, err
			}
			fid, err := handle.ID(r)
			if err != nil {
				return billing.Payment{}, err
			}
			var folio *uuid.UUID
			var status string
			if err := tx.QueryRow(ctx, `SELECT f.folio_id, f.status FROM membership.fees f JOIN membership.memberships ms ON ms.id = f.membership_id
				JOIN membership.members mb ON mb.id = ms.member_id WHERE f.id = $1 AND mb.customer_id = $2`, fid, c.ID).Scan(&folio, &status); err != nil {
				return billing.Payment{}, errs.NotFound("membership fee")
			}
			if folio == nil || (status != "due" && status != "postponed") {
				return billing.Payment{}, errs.Conflict("no_fee_due", "this fee is not due")
			}
			sum, err := billing.FolioSummary(ctx, tx, *folio)
			if err != nil {
				return billing.Payment{}, err
			}
			method := in.Method
			if method == "" {
				method = "qris"
			}
			return m.Billing.TakePayment(withProperty(ctx, c.PropertyID), tx, billing.PaymentInput{FolioID: folio, MethodType: method, Channel: "online",
				Amount: decimalOf(sum.Balance), Description: "Membership fee", PayerName: c.Name})
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/memberships/{id}:pause", Summary: "Request a pause of my membership (approval)",
		Request: PauseInput{}, Response: LifecycleRequest{}, Status: http.StatusAccepted,
		Handler: handle.Write(db, http.StatusAccepted, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PauseInput) (LifecycleRequest, error) {
			ctx, mid, _, err := m.mine(ctx, tx, r)
			if err != nil {
				return LifecycleRequest{}, err
			}
			from, err := parseDay("from", in.From)
			if err != nil {
				return LifecycleRequest{}, err
			}
			until, err := parseDay("until", in.Until)
			if err != nil {
				return LifecycleRequest{}, err
			}
			return m.RequestPause(ctx, tx, mid, from, until, in.Reason, "member_portal")
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/memberships/{id}:renew", Summary: "Renew my membership (pay the renewal fee online)",
		Request: PayFeeInput{}, Response: MyRenewal{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PayFeeInput) (MyRenewal, error) {
			ctx, mid, c, err := m.mine(ctx, tx, r)
			if err != nil {
				return MyRenewal{}, err
			}
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM membership.memberships WHERE id = $1`, mid).Scan(&status); err != nil {
				return MyRenewal{}, err
			}
			if status == "suspended" || status == "cancelled" {
				return MyRenewal{}, errs.Conflict("not_renewable", "a "+status+" membership cannot be renewed (FR-MBL-09)")
			}
			rid, err := m.Renew(ctx, tx, mid, nil)
			if err != nil {
				return MyRenewal{}, err
			}
			out := MyRenewal{RenewalID: rid}
			var folio *uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT folio_id FROM membership.renewals WHERE id = $1`, rid).Scan(&folio); err != nil || folio == nil {
				return out, err
			}
			sum, err := billing.FolioSummary(ctx, tx, *folio)
			if err != nil {
				return out, err
			}
			method := in.Method
			if method == "" {
				method = "qris"
			}
			p, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: folio, MethodType: method, Channel: "online",
				Amount: decimalOf(sum.Balance), Description: "Membership renewal", PayerName: c.Name})
			out.Payment = &p
			return out, err
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/card:replace", Summary: "Replace my member card (old card blocked, fee charged)",
		Request: ReasonInput{}, Response: Card{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (Card, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return Card{}, err
			}
			ctx = withProperty(ctx, c.PropertyID)
			var cid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT k.id FROM membership.cards k JOIN membership.members mb ON mb.id = k.member_id
				WHERE mb.customer_id = $1 AND k.status = 'active' ORDER BY k.issued_at DESC LIMIT 1`, c.ID).Scan(&cid); err != nil {
				return Card{}, errs.NotFound("member card")
			}
			return m.ReplaceCard(ctx, tx, cid, in.Reason, false)
		})})
}
