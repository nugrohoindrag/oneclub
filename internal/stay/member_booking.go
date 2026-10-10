package stay

// Member App bungalow booking (docs/requirement-booking-hotel-mgcc.md
// Bagian E): the same search, quote and booking as the website (FR-H87) —
// with the Member rate next to the public rates (FR-H88), several
// bungalows in one cart, the add-ons, the cancellation policy and the
// e-voucher — paid online on the mock gateway or by member charge within
// the limit of the member account; My Activity › Stays lists the bookings
// with their link (FR-H89).

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// MemberBookingInput is a cart booked by the signed-in member.
type MemberBookingInput struct {
	CartInput
	ExpectedArrival string `json:"expectedArrival,omitempty"`
	SpecialRequests string `json:"specialRequests,omitempty"`
	PayMethod       string `json:"payMethod,omitempty" enum:"qris,virtual_account,card,member_charge" doc:"member_charge: the deposit goes to the member account (credit limit)"`
}

// MemberCancelInput cancels bungalows of my booking.
type MemberCancelInput struct {
	StayIDs []uuid.UUID `json:"stayIds,omitempty"`
	Reason  string      `json:"reason,omitempty"`
}

// MemberPayInput opens the payment again.
type MemberPayInput struct {
	Method string `json:"method" enum:"qris,virtual_account,card"`
}

func (m *Module) registerMemberBooking(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "stay", "Member Portal", rt) }
	// mine checks that the booking link belongs to the signed-in member.
	mine := func(ctx context.Context, tx pgx.Tx, token string) (crm.Customer, error) {
		p, err := crm.Me(ctx, tx)
		if err != nil {
			return p, err
		}
		stays, _, err := m.staysOfToken(ctx, tx, p.PropertyID, token)
		if err != nil {
			return p, err
		}
		for _, s := range stays {
			if s.CustomerID == nil || *s.CustomerID != p.ID {
				return p, errs.NotFound("booking")
			}
		}
		return p, nil
	}
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/stay/search", Summary: "Bungalows and rates for my stay (member rates included)",
		Response: PublicSearch{}, Query: []route.Param{{Name: "checkin", Required: true}, {Name: "checkout", Required: true}, {Name: "adults", Type: "integer"},
			{Name: "children", Type: "integer"}, {Name: "promo"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PublicSearch, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return PublicSearch{}, err
			}
			in, err := searchQuery(ctx, tx, r)
			if err != nil {
				return PublicSearch{}, err
			}
			in.Source, in.CustomerID = "member_app", &p.ID
			return m.ChannelSearch(ctx, tx, p.PropertyID, in)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stays:quote-cart", Summary: "Price my cart of bungalows (nothing is kept)",
		Request: CartInput{}, Response: CartQuote{}, NoAudit: "read-only preview, rolled back",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CartInput) (CartQuote, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return CartQuote{}, err
			}
			sp, err := tx.Begin(ctx)
			if err != nil {
				return CartQuote{}, err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			return m.QuoteCart(ctx, sp, p.PropertyID, in, cartBase{Channel: "member_app", Source: "member_app", CustomerID: &p.ID})
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stay-bookings", Summary: "Book my cart: online deposit (mock) or member charge",
		Request: MemberBookingInput{}, Response: BookingView{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberBookingInput) (BookingView, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return BookingView{}, err
			}
			pol, _, err := m.policy(ctx, tx, p.PropertyID)
			if err != nil {
				return BookingView{}, err
			}
			b := cartBase{Channel: "member_app", Source: "member_app", CustomerID: &p.ID, ExpectedArrival: in.ExpectedArrival,
				SpecialRequests: strings.TrimSpace(in.SpecialRequests), Hold: true}
			method := in.PayMethod
			switch {
			case method == "member_charge":
				b.Payment, b.Hold, method = &PaymentInput{MethodType: "member_account"}, false, ""
			case method != "" && !slices.Contains(methodsOf(pol), method):
				return BookingView{}, handle.Invalid("payMethod", "invalid_method", "metode pembayaran tidak tersedia")
			}
			res, err := m.bookCart(ctx, tx, p.PropertyID, in.CartInput, b, method, r.Header.Get("Idempotency-Key"))
			if err != nil {
				return BookingView{}, err
			}
			if b.Payment == nil && method == "" && decOf(res.Deposit).IsPositive() {
				return BookingView{}, handle.Invalid("payMethod", "required", "pilih metode pembayaran deposit")
			}
			return m.BookingOf(ctx, tx, p.PropertyID, res.Token)
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/stay-bookings/{token}", Summary: "My booking: status, bungalows, payment, policies",
		Response: BookingView{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (BookingView, error) {
			p, err := mine(ctx, tx, chi.URLParam(r, "token"))
			if err != nil {
				return BookingView{}, err
			}
			return m.BookingOf(ctx, tx, p.PropertyID, chi.URLParam(r, "token"))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stay-bookings/{token}:pay", Summary: "Continue paying my booking while it is held",
		Request: MemberPayInput{}, Response: BookingView{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberPayInput) (BookingView, error) {
			p, err := mine(ctx, tx, chi.URLParam(r, "token"))
			if err != nil {
				return BookingView{}, err
			}
			return m.ContinuePayment(ctx, tx, p.PropertyID, chi.URLParam(r, "token"), in.Method)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stay-bookings/{token}:abandon", Summary: "My online payment failed: release the bungalows",
		Response: BookingView{}, Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (BookingView, error) {
			p, err := mine(ctx, tx, chi.URLParam(r, "token"))
			if err != nil {
				return BookingView{}, err
			}
			return m.AbandonBooking(ctx, tx, p.PropertyID, chi.URLParam(r, "token"), "the online payment failed")
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stay-bookings/{token}:cancel", Summary: "Cancel my bungalows under the policy of the rate plan",
		Request: MemberCancelInput{}, Response: BookingView{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberCancelInput) (BookingView, error) {
			p, err := mine(ctx, tx, chi.URLParam(r, "token"))
			if err != nil {
				return BookingView{}, err
			}
			return m.CancelByGuest(ctx, tx, p.PropertyID, chi.URLParam(r, "token"), in.StayIDs, in.Reason)
		})})
}
