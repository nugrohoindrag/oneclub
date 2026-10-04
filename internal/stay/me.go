package stay

// Member App Stay & Venue (PRD P2 EP-25 FR-APP-P2-04): Book Bungalow, Book
// VIP Suite, Book Meeting Room (Should), My Stay, availability.

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// MyStayInput is a stay booking by the signed-in member.
type MyStayInput struct {
	StayInput
	MemberCharge bool `json:"memberCharge,omitempty" doc:"Pay the deposit / total from my member account; otherwise pay the folio online"`
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "stay", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/bungalow-availability", Summary: "Bungalow availability per type",
		Response: TypeAvailability{}, List: true, Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "nights", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TypeAvailability], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[TypeAvailability]{}, err
			}
			from, err := handle.QueryDate(r, "from", time.Now())
			if err != nil {
				return httpx.Page[TypeAvailability]{}, err
			}
			return handle.Page(m.BungalowAvailability(ctx, tx, p.PropertyID, from, max(handle.QueryInt(r, "nights", 1), 1)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stays", Summary: "Book Bungalow, VIP Suite or Meeting Room", Request: MyStayInput{},
		Response: StayResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyStayInput) (StayResult, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return StayResult{}, err
			}
			in.CustomerID, in.Guest, in.Channel, in.Segment, in.Payment = &p.ID, nil, "member_app", "", nil
			if in.MemberCharge {
				in.Payment = &PaymentInput{MethodType: "member_account"}
			}
			return m.Book(ctx, tx, p.PropertyID, in.StayInput, r.Header.Get("Idempotency-Key"))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/stays", Summary: "My Stay (bungalow, VIP suite, meeting room)", Response: Stay{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Stay], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			return handle.Page(handle.List[Stay](tx.Query(ctx, staySelect+` WHERE s.customer_id = $1 ORDER BY s.start_at DESC LIMIT $2`, p.ID, httpx.ParseList(r).Limit)))
		})})
}
