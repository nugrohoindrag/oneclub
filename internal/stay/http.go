package stay

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/resource"
)

type ReasonInput struct {
	Reason string `json:"reason,omitempty"`
}

type ReadinessInput struct {
	Readiness string `json:"readiness" enum:"ready,not_ready"`
}

// Register adds the Stay & Venue routes.
func (m *Module) Register(reg *route.Registry, eng *resource.Engine) {
	m.hooks()
	m.registerMe(reg)
	m.registerMemberJourney(reg)
	m.registerPublic(reg)
	for _, d := range []*resource.Def{BungalowTypes, Bungalows, VIPSuites, MeetingRooms, RoomLayouts, Equipment} {
		eng.Register(reg, d)
	}
	db := m.DB
	add := func(rt route.Route) {
		rt.Module, rt.Tag, rt.Scope = "stay", "Stay & Venue", route.ScopeProperty
		reg.Add(rt)
	}
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays", Summary: "Book bungalow, VIP suite or meeting room (rates, deposit, folio)",
		Permission: "stay.stay.create", Request: StayInput{}, Response: StayResult{}, Idempotent: true,
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in StayInput) (StayResult, error) {
			return m.Book(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays", Summary: "Reservations (stay & venue)", Permission: "stay.stay.view",
		Response: Stay{}, List: true, Query: []route.Param{{Name: "filter[kind]"}, {Name: "filter[status]"}, {Name: "from"}, {Name: "to"}, {Name: "q"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[Stay], error) {
			lp := httpx.ParseList(r)
			from, err := handle.QueryTime(r, "from", time.Time{})
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			to, err := handle.QueryTime(r, "to", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
			if err != nil {
				return httpx.Page[Stay]{}, err
			}
			return handle.Page(handle.List[Stay](tx.Query(ctx, staySelect+` WHERE s.property_id = $1 AND ($2 = '' OR s.kind = $2)
				AND ($3 = '' OR s.status = ANY(string_to_array($3, ','))) AND s.end_at > $4 AND s.start_at < $5
				AND ($6 = '' OR s.stay_no ILIKE '%' || $6 || '%' OR c.name ILIKE '%' || $6 || '%' OR s.guest_name ILIKE '%' || $6 || '%'
				  OR s.corporate_name ILIKE '%' || $6 || '%')
				ORDER BY s.start_at LIMIT $7`, handle.Property(ctx), lp.Filters["kind"], lp.Filters["status"], from, to, lp.Q, lp.Limit)))
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/stays/{id}", Summary: "Stay detail with reservation and folio", Permission: "stay.stay.view",
		Response: StayResult{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.result(ctx, tx, sid)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:check-in", Summary: "Guest Check-in (identity, deposit, unit readiness)",
		Permission: "stay.stay.check_in", Request: CheckInInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CheckInInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.CheckIn(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:check-out", Summary: "Guest Check-out (late fee, settled folio closed)",
		Permission: "stay.stay.check_out", Request: CheckOutInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CheckOutInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.CheckOut(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:extend", Summary: "Overtime: extend a VIP suite or meeting room booking",
		Permission: "stay.stay.update", Request: ExtendInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ExtendInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.Extend(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:assign-unit", Summary: "Assign a specific unit (booked by type)",
		Permission: "stay.stay.update", Request: AssignInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in AssignInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.AssignUnit(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:confirm", Summary: "Confirm a requested booking (VIP suite request)",
		Permission: "stay.stay.update", Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.ConfirmRequest(ctx, tx, sid)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:cancel", Summary: "Cancel (Cancellation Policy, deposit refund)",
		Permission: "stay.stay.cancel", Request: CancelInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in CancelInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			if err := handle.Required("reason", in.Reason); err != nil {
				return StayResult{}, err
			}
			return m.Cancel(ctx, tx, sid, in)
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/stays/{id}:no-show", Summary: "Mark No-show", Permission: "stay.stay.update",
		Request: ReasonInput{}, Response: StayResult{}, Status: http.StatusOK,
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReasonInput) (StayResult, error) {
			sid, err := handle.ID(r)
			if err != nil {
				return StayResult{}, err
			}
			return m.NoShow(ctx, tx, sid, in.Reason)
		})})
	add(route.Route{Method: http.MethodGet, Path: "/api/v1/stay/availability", Summary: "Bungalow availability per type and night",
		Permission: "stay.stay.view", Response: TypeAvailability{}, List: true, Query: []route.Param{{Name: "from", Description: "YYYY-MM-DD"}, {Name: "nights", Type: "integer"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[TypeAvailability], error) {
			from, err := handle.QueryDate(r, "from", time.Now())
			if err != nil {
				return httpx.Page[TypeAvailability]{}, err
			}
			return handle.Page(m.BungalowAvailability(ctx, tx, handle.Property(ctx), from, min(max(handle.QueryInt(r, "nights", 7), 1), 31)))
		})})
	add(route.Route{Method: http.MethodPost, Path: "/api/v1/stay/bungalows/{id}:readiness", Summary: "Mark unit Ready / Not Ready", Permission: "stay.bungalow.update",
		Request: ReadinessInput{}, Status: http.StatusNoContent,
		Handler: handle.Write(db, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ReadinessInput) (any, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return nil, err
			}
			if in.Readiness != "ready" && in.Readiness != "not_ready" {
				return nil, handle.Invalid("readiness", "invalid", "readiness must be ready or not_ready")
			}
			return nil, m.setReadiness(ctx, tx, bid, in.Readiness)
		})})
}
