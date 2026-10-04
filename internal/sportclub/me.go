package sportclub

// Member App Sport Club (PRD P2 EP-25 FR-APP-P2-03): Book Facility, Book
// Class, My Classes, My Sessions. Facility Access uses the Digital Member
// Card QR (/api/v1/me/card). Payment in the app is a member charge; other
// methods pay the returned folio online (/api/v1/me/folios/{id}:pay-online).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

type MyCourtBookingInput struct {
	CourtID      uuid.UUID `json:"courtId"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	PackageCode  string    `json:"packageCode,omitempty"`
	MemberCharge bool      `json:"memberCharge,omitempty" doc:"Charge to my member account; otherwise pay the folio online"`
	Notes        string    `json:"notes,omitempty"`
}

type MyEnrollInput struct {
	ProgramID    uuid.UUID `json:"programId"`
	MemberCharge bool      `json:"memberCharge,omitempty"`
}

type MySessionInput struct {
	SessionID uuid.UUID `json:"sessionId"`
}

func memberCharge(on bool) *PaymentInput {
	if on {
		return &PaymentInput{MethodType: "member_account"}
	}
	return nil
}

func (m *Module) registerMe(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "sportclub", "Member App", rt) }
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/me/court-bookings", Summary: "Book Facility (court / slot)", Request: MyCourtBookingInput{},
		Response: CourtBookingResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyCourtBookingInput) (CourtBookingResult, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return CourtBookingResult{}, err
			}
			return m.BookCourt(ctx, tx, p.PropertyID, CourtBookingInput{CourtID: in.CourtID, Start: in.Start, End: in.End, CustomerID: &p.ID,
				Channel: "member_app", PackageCode: in.PackageCode, Notes: in.Notes, Payment: memberCharge(in.MemberCharge)}, r.Header.Get("Idempotency-Key"))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/class-sessions", Summary: "Class schedule to book", Response: Session{}, List: true,
		Query: []route.Param{{Name: "from"}, {Name: "days", Type: "integer"}, {Name: "filter[programId]"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Session], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Session]{}, err
			}
			lp := httpx.ParseList(r)
			now := localNow(ctx, tx)
			d, err := handle.QueryDate(r, "from", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Session]{}, err
			}
			from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, now.Location())
			days := min(max(handle.QueryInt(r, "days", 7), 1), 31)
			return handle.Page(handle.List[Session](tx.Query(ctx, sessionSelect+` WHERE s.property_id = $1 AND lower(s.period) >= greatest($2, now())
				AND lower(s.period) < $3 AND s.status <> 'cancelled' AND ($4 = '' OR s.program_id::text = $4) ORDER BY lower(s.period) LIMIT $5`,
				p.PropertyID, from, from.AddDate(0, 0, days), lp.Filters["programId"], lp.Limit)))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/me/class-enrollments", Summary: "Book Class: register for a class program",
		Request: MyEnrollInput{}, Response: Enrollment{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyEnrollInput) (Enrollment, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return Enrollment{}, err
			}
			return m.Enroll(ctx, tx, p.PropertyID, EnrollInput{ProgramID: in.ProgramID, CustomerID: p.ID, Channel: "member_app",
				Payment: memberCharge(in.MemberCharge)}, r.Header.Get("Idempotency-Key"))
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/me/session-bookings", Summary: "Book a class session (quota checked)",
		Request: MySessionInput{}, Response: SessionBooking{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MySessionInput) (SessionBooking, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return SessionBooking{}, err
			}
			return m.BookSession(ctx, tx, p.PropertyID, SessionBookingInput{SessionID: in.SessionID, CustomerID: p.ID, Channel: "member_app"})
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/classes", Summary: "My Classes (registrations)", Response: Enrollment{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Enrollment], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Enrollment]{}, err
			}
			return handle.Page(handle.List[Enrollment](tx.Query(ctx, enrollmentSelect+` WHERE e.customer_id = $1 ORDER BY e.valid_until DESC`, p.ID)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/sessions", Summary: "My Sessions (booked and attended)", Response: SessionBooking{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[SessionBooking], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[SessionBooking]{}, err
			}
			return handle.Page(handle.List[SessionBooking](tx.Query(ctx, bookingSelect+` WHERE b.customer_id = $1 ORDER BY b.created_at DESC LIMIT $2`,
				p.ID, httpx.ParseList(r).Limit)))
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/me/entries", Summary: "My facility entries (tickets & access)", Response: Entry{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Entry], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[Entry]{}, err
			}
			return handle.Page(handle.List[Entry](tx.Query(ctx, entrySelect+` WHERE e.customer_id = $1 OR e.host_customer_id = $1 ORDER BY e.created_at DESC LIMIT $2`,
				p.ID, httpx.ParseList(r).Limit)))
		})})
}
