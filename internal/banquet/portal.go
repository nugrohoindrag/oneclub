package banquet

// Website and Member App of Banquet & Event: the Wedding & Banquet and
// Events pages from structured data (PRD P3 FR-WEB-P3-01, contract K5),
// Book Event — registration for open events with online payment
// (FR-WEB-P3-03) — and the Member App Events: list, registration, QR ticket
// and My Events with the payment schedule of the member's own banquet
// (FR-APP-P3-03, FR-APP-P3-07).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

// PublicEvent is an open event on the website and in the Member App.
type PublicEvent struct {
	ID                   uuid.UUID  `json:"id" db:"id"`
	Number               string     `json:"number" db:"number"`
	Title                string     `json:"title" db:"title"`
	EventType            string     `json:"eventType" db:"event_type"`
	Category             string     `json:"category" db:"category"`
	Start                time.Time  `json:"start" db:"start_at"`
	End                  time.Time  `json:"end" db:"end_at"`
	Venues               []string   `json:"venues" db:"venues"`
	Description          *string    `json:"description" db:"description"`
	Capacity             *int       `json:"capacity" db:"capacity"`
	SeatsLeft            *int       `json:"seatsLeft" db:"seats_left" doc:"Empty: no capacity limit"`
	RegistrationOpen     bool       `json:"registrationOpen" db:"registration_open"`
	RegistrationClosesAt *time.Time `json:"registrationClosesAt" db:"registration_closes_at"`
	Fee                  string     `json:"fee" db:"fee" doc:"Registration fee per seat"`
	Currency             string     `json:"currency" db:"currency"`
	MembersOnly          bool       `json:"membersOnly" db:"members_only"`
}

const publicEventSelect = `SELECT e.id, e.number, e.title, t.name AS event_type, e.category, e.start_at, e.end_at,
	coalesce((SELECT array_agg(DISTINCT v.name) FROM banquet.event_venues h JOIN banquet.venues v ON v.id = h.venue_id
	  WHERE h.event_id = e.id AND h.status IN ('tentative', 'definite')), '{}') AS venues,
	e.description, e.capacity,
	CASE WHEN e.capacity IS NULL THEN NULL ELSE greatest(e.capacity - coalesce((SELECT sum(p.party_size) FROM banquet.participants p
	  WHERE p.event_id = e.id AND p.status IN ('registered', 'checked_in')), 0), 0)::int END AS seats_left,
	e.registration_open, e.registration_closes_at, trim_scale(e.registration_fee)::text AS fee, e.currency, e.members_only
	FROM banquet.events e JOIN banquet.event_types t ON t.id = e.event_type_id`

// publicWhere: published, live and not over.
const publicWhere = ` WHERE e.property_id = $1 AND e.public AND e.status IN ('tentative', 'definite') AND e.end_at > now()`

// PublicPackage is a banquet package on the Wedding & Banquet page.
type PublicBanquetPackage struct {
	ID            uuid.UUID `json:"id" db:"id"`
	Code          string    `json:"code" db:"code"`
	Name          string    `json:"name" db:"name"`
	Category      string    `json:"category" db:"category"`
	PricingMethod string    `json:"pricingMethod" db:"pricing_method" enum:"per_pax,per_pax_per_day,fixed"`
	Price         string    `json:"price" db:"price"`
	PricingMode   string    `json:"pricingMode" db:"pricing_mode" enum:"nett,plus_plus"`
	MinPax        int       `json:"minPax" db:"min_pax"`
	IncludedPax   int       `json:"includedPax" db:"included_pax"`
	DurationHours int       `json:"durationHours" db:"duration_hours"`
	Inclusions    []string  `json:"inclusions" db:"inclusions"`
	Description   *string   `json:"description" db:"description"`
}

// PublicVenue is a venue on the website.
type PublicBanquetVenue struct {
	ID          uuid.UUID                  `json:"id" db:"id"`
	Code        string                     `json:"code" db:"code"`
	Name        string                     `json:"name" db:"name"`
	VenueType   string                     `json:"venueType" db:"venue_type"`
	SizeSqm     *string                    `json:"sizeSqm" db:"size_sqm"`
	MaxCapacity *int                       `json:"maxCapacity" db:"max_capacity"`
	MinPax      int                        `json:"minPax" db:"min_pax"`
	AddonPrice  string                     `json:"addonPrice" db:"addon_price"`
	Facilities  []string                   `json:"facilities" db:"facilities"`
	Description *string                    `json:"description" db:"description"`
	Layouts     []PublicBanquetVenueLayout `json:"layouts" db:"-"`
}

// PublicVenueLayout is a capacity per setup style.
type PublicBanquetVenueLayout struct {
	Layout   string `json:"layout" db:"layout"`
	Capacity int    `json:"capacity" db:"capacity"`
}

// PublicMenu is a banquet menu on the website.
type PublicBanquetMenu struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	MenuType    string    `json:"menuType" db:"menu_type"`
	PricePerPax string    `json:"pricePerPax" db:"price_per_pax"`
	PricingMode string    `json:"pricingMode" db:"pricing_mode"`
	Description *string   `json:"description" db:"description"`
	Items       []string  `json:"items" db:"items"`
}

// PublicBanquetPage is the structured data of Wedding & Banquet.
type PublicBanquetPage struct {
	Currency string                 `json:"currency"`
	Packages []PublicBanquetPackage `json:"packages"`
	Venues   []PublicBanquetVenue   `json:"venues"`
	Menus    []PublicBanquetMenu    `json:"menus"`
}

// PublicEventRegistration is Book Event on the website.
type PublicEventRegistration struct {
	PropertyID   uuid.UUID       `json:"propertyId"`
	Guest        crm.PublicGuest `json:"guest"`
	PartySize    int             `json:"partySize,omitempty"`
	Company      string          `json:"company,omitempty"`
	DietaryNotes string          `json:"dietaryNotes,omitempty"`
	PayMethod    string          `json:"payMethod,omitempty" enum:"qris,virtual_account,card" doc:"Paid events: opens the online payment"`
}

func (b PublicEventRegistration) Property() uuid.UUID      { return b.PropertyID }
func (b PublicEventRegistration) Visitor() crm.PublicGuest { return b.Guest }

// EventTicket is a registration with its QR ticket code.
type EventTicket struct {
	TicketCode    string            `json:"ticketCode" doc:"QR content for the check-in"`
	Status        string            `json:"status" enum:"registered,waitlisted,withdrawn,checked_in"`
	WaitlistRank  *int              `json:"waitlistRank"`
	Name          string            `json:"name"`
	PartySize     int               `json:"partySize"`
	Fee           string            `json:"fee"`
	PaymentStatus string            `json:"paymentStatus" enum:"none,unpaid,paid,refunded"`
	EventID       uuid.UUID         `json:"eventId"`
	EventTitle    string            `json:"eventTitle"`
	EventStart    time.Time         `json:"eventStart"`
	Checkout      *billing.Checkout `json:"checkout" doc:"Online payment of the fee (paid events)"`
}

func ticketOf(p EventParticipant) EventTicket {
	return EventTicket{TicketCode: p.TicketCode, Status: p.Status, WaitlistRank: p.WaitlistRank, Name: p.Name, PartySize: p.PartySize, Fee: p.Fee,
		PaymentStatus: p.PaymentStatus, EventID: p.EventID, EventTitle: p.EventTitle, EventStart: p.EventStart}
}

// ticketLimiter limits ticket lookups per client (codes are secrets).
var ticketLimiter = &handle.Limiter{N: 30, Period: time.Minute}

func publicProperty(r *http.Request) (uuid.UUID, error) {
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		return uuid.Nil, errs.BadRequest("property_required", "propertyId is required")
	}
	return pid, nil
}

// publicRead runs a read for an anonymous website request.
func publicRead[Res any](db *dbtx.DB, fn func(ctx context.Context, tx pgx.Tx, r *http.Request, property uuid.UUID) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := publicProperty(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		ctx := crm.PublicCtx(r.Context(), pid)
		var out Res
		err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
			var err error
			out, err = fn(ctx, tx, r, pid)
			return err
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}
}

func publicEvent(ctx context.Context, q dbtx.Querier, property, eid uuid.UUID) (PublicEvent, error) {
	rows, err := q.Query(ctx, publicEventSelect+publicWhere+` AND e.id = $2`, property, eid)
	return handle.One[PublicEvent](rows, err, "event")
}

// checkout opens the online payment of an unpaid registration fee.
func (m *Module) checkout(ctx context.Context, tx pgx.Tx, p EventParticipant, method, payer string) (*billing.Checkout, error) {
	if p.FolioID == nil || p.PaymentStatus != "unpaid" || p.Status != "registered" {
		return nil, nil
	}
	co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: *p.FolioID, Method: method, Description: "Registration " + p.TicketCode,
		PayerName: payer})
	if err != nil {
		return nil, err
	}
	return &co, nil
}

func (m *Module) registerPublic(reg *route.Registry) {
	db := m.DB
	crm.PublicRoute(reg, "banquet", route.Route{Method: http.MethodGet, Path: "/api/v1/public/events", Summary: "Events page: open and published events",
		Response: PublicEvent{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "category"}},
		Handler: publicRead(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (httpx.Page[PublicEvent], error) {
			return handle.Page(handle.List[PublicEvent](tx.Query(ctx, publicEventSelect+publicWhere+` AND ($2 = '' OR e.category = $2)
				ORDER BY e.start_at LIMIT 200`, pid, r.URL.Query().Get("category"))))
		})})
	crm.PublicRoute(reg, "banquet", route.Route{Method: http.MethodGet, Path: "/api/v1/public/events/{id}", Summary: "An open event (Book Event)",
		Response: PublicEvent{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: publicRead(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (PublicEvent, error) {
			eid, err := handle.ID(r)
			if err != nil {
				return PublicEvent{}, err
			}
			return publicEvent(ctx, tx, pid, eid)
		})})
	crm.PublicRoute(reg, "banquet", route.Route{Method: http.MethodPost, Path: "/api/v1/public/events/{id}/registrations",
		Summary: "Book Event: register for an open event (capacity, waitlist, online payment of the fee)", Request: PublicEventRegistration{},
		Response: EventTicket{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer,
			in PublicEventRegistration) (EventTicket, error) {
			eid, err := handle.ID(r)
			if err != nil {
				return EventTicket{}, err
			}
			if _, err := publicEvent(ctx, tx, pid, eid); err != nil {
				return EventTicket{}, err
			}
			p, err := m.register(ctx, tx, eid, EventParticipantInput{CustomerID: &c.ID, Name: in.Guest.Name, Email: in.Guest.Email, Phone: in.Guest.Phone,
				Company: in.Company, PartySize: in.PartySize, DietaryNotes: in.DietaryNotes}, "website")
			if err != nil {
				return EventTicket{}, err
			}
			out := ticketOf(p)
			out.Checkout, err = m.checkout(ctx, tx, p, in.PayMethod, p.Name)
			return out, err
		})})
	crm.PublicRoute(reg, "banquet", route.Route{Method: http.MethodGet, Path: "/api/v1/public/event-tickets/{code}",
		Summary: "Event ticket behind its QR code (status, payment)", Response: EventTicket{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if !ticketLimiter.Allow(r.RemoteAddr) {
				httpx.WriteError(w, r, errs.RateLimited())
				return
			}
			code := strings.ToUpper(strings.TrimSpace(chi.URLParam(r, "code")))
			ctx := dbtx.System(r.Context())
			var out EventTicket
			err := db.WithReadTx(ctx, func(tx pgx.Tx) error {
				rows, err := tx.Query(ctx, participantSelect+` WHERE p.ticket_code = $1`, code)
				p, err := handle.One[EventParticipant](rows, err, "ticket")
				out = ticketOf(p)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	crm.PublicRoute(reg, "banquet", route.Route{Method: http.MethodGet, Path: "/api/v1/public/wedding-banquet",
		Summary: "Wedding & Banquet page: packages, venues with capacities and menus (structured data)", Response: PublicBanquetPage{},
		Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: publicRead(db, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (PublicBanquetPage, error) {
			out := PublicBanquetPage{Currency: "IDR"}
			if err := tx.QueryRow(ctx, `SELECT currency FROM platform.instance`).Scan(&out.Currency); err != nil {
				return out, err
			}
			var err error
			if out.Packages, err = handle.List[PublicBanquetPackage](tx.Query(ctx, `SELECT id, code, name, category, pricing_method, trim_scale(price)::text AS price,
				pricing_mode, min_pax, included_pax, duration_hours,
				coalesce((SELECT array_agg(x->>'label') FROM jsonb_array_elements(inclusions) x), '{}') AS inclusions, description
				FROM banquet.packages WHERE property_id = $1 AND public AND status = 'active' AND archived_at IS NULL ORDER BY category, price, name`, pid)); err != nil {
				return out, err
			}
			if out.Venues, err = handle.List[PublicBanquetVenue](tx.Query(ctx, `SELECT id, code, name, venue_type, trim_scale(size_sqm)::text AS size_sqm, max_capacity,
				min_pax, trim_scale(addon_price)::text AS addon_price, facilities, description FROM banquet.venues
				WHERE property_id = $1 AND public AND status = 'active' AND archived_at IS NULL ORDER BY venue_type, name`, pid)); err != nil {
				return out, err
			}
			for i := range out.Venues {
				if out.Venues[i].Layouts, err = handle.List[PublicBanquetVenueLayout](tx.Query(ctx, `SELECT layout, capacity FROM banquet.venue_layouts
					WHERE venue_id = $1 ORDER BY capacity DESC`, out.Venues[i].ID)); err != nil {
					return out, err
				}
			}
			out.Menus, err = handle.List[PublicBanquetMenu](tx.Query(ctx, `SELECT m.id, m.code, m.name, m.menu_type, trim_scale(m.price_per_pax)::text AS price_per_pax,
				m.pricing_mode, m.description, coalesce((SELECT array_agg(i.name ORDER BY i.name) FROM banquet.menu_items i WHERE i.menu_id = m.id
				AND i.status = 'active' AND i.archived_at IS NULL), '{}') AS items
				FROM banquet.menus m WHERE m.property_id = $1 AND m.public AND m.status = 'active' AND m.archived_at IS NULL ORDER BY m.menu_type, m.name`, pid))
			return out, err
		})})
}

// ── Member App ────────────────────────────────────────────────────────────

// MemberEvent is an event of the Member App with my registration.
type MemberEvent struct {
	PublicEvent
	MyTicket *EventTicket `json:"myTicket"`
}

// MemberRegistration registers the signed-in member.
type MemberEventRegistration struct {
	PartySize    int    `json:"partySize,omitempty"`
	DietaryNotes string `json:"dietaryNotes,omitempty"`
	PayMethod    string `json:"payMethod,omitempty" enum:"qris,virtual_account,card" doc:"Paid events: opens the online payment"`
}

// HostedEvent is a banquet the member books (My Events: own wedding,
// birthday …) with its payment schedule.
type HostedEvent struct {
	ID        uuid.UUID         `json:"id"`
	Number    string            `json:"number"`
	Title     string            `json:"title"`
	Status    string            `json:"status"`
	Start     time.Time         `json:"start"`
	End       time.Time         `json:"end"`
	Venues    []string          `json:"venues"`
	Pax       int               `json:"pax"`
	Total     string            `json:"contractTotal"`
	Currency  string            `json:"currency"`
	FolioID   *uuid.UUID        `json:"folioId"`
	Schedule  *billing.Schedule `json:"schedule" doc:"DP and settlement terms, payable online"`
	BEOStatus *string           `json:"beoStatus"`
}

// MyEvents is My Events of the Member App.
type MyEvents struct {
	Tickets []EventTicket `json:"tickets"`
	Hosted  []HostedEvent `json:"hosted"`
}

func myTickets(ctx context.Context, q dbtx.Querier, customer uuid.UUID, eid *uuid.UUID) ([]EventTicket, error) {
	list, err := handle.List[EventParticipant](q.Query(ctx, participantSelect+` WHERE p.customer_id = $1 AND ($2::uuid IS NULL OR p.event_id = $2)
		ORDER BY e.start_at DESC, p.registered_at DESC LIMIT 200`, customer, eid))
	out := make([]EventTicket, 0, len(list))
	for _, p := range list {
		out = append(out, ticketOf(p))
	}
	return out, err
}

func (m *Module) memberEvent(ctx context.Context, q dbtx.Querier, me crm.Customer, eid uuid.UUID) (MemberEvent, error) {
	e, err := publicEvent(ctx, q, me.PropertyID, eid)
	if err != nil {
		return MemberEvent{}, err
	}
	out := MemberEvent{PublicEvent: e}
	ts, err := myTickets(ctx, q, me.ID, &eid)
	for i := range ts {
		if ts[i].Status != "withdrawn" {
			out.MyTicket = &ts[i]
			break
		}
	}
	return out, err
}

func (m *Module) registerMember(reg *route.Registry) {
	db := m.DB
	crm.MeRoute(reg, "banquet", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/events", Summary: "Upcoming Events with my registration",
		Response: MemberEvent{}, List: true,
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[MemberEvent], error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[MemberEvent]{}, err
			}
			evs, err := handle.List[PublicEvent](tx.Query(ctx, publicEventSelect+publicWhere+` ORDER BY e.start_at LIMIT 200`, me.PropertyID))
			if err != nil {
				return httpx.Page[MemberEvent]{}, err
			}
			ts, err := myTickets(ctx, tx, me.ID, nil)
			if err != nil {
				return httpx.Page[MemberEvent]{}, err
			}
			out := make([]MemberEvent, 0, len(evs))
			for _, e := range evs {
				x := MemberEvent{PublicEvent: e}
				for i := range ts {
					if ts[i].EventID == e.ID && ts[i].Status != "withdrawn" {
						x.MyTicket = &ts[i]
						break
					}
				}
				out = append(out, x)
			}
			return handle.Page(out, nil)
		})})
	crm.MeRoute(reg, "banquet", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/events/{id}", Summary: "An event with my registration",
		Response: MemberEvent{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MemberEvent, error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return MemberEvent{}, err
			}
			eid, err := handle.ID(r)
			if err != nil {
				return MemberEvent{}, err
			}
			return m.memberEvent(ctx, tx, me, eid)
		})})
	crm.MeRoute(reg, "banquet", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/events/{id}/registrations",
		Summary: "Register for an event (QR ticket; online payment of the fee)", Request: MemberEventRegistration{}, Response: EventTicket{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberEventRegistration) (EventTicket, error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return EventTicket{}, err
			}
			eid, err := handle.ID(r)
			if err != nil {
				return EventTicket{}, err
			}
			if _, err := publicEvent(ctx, tx, me.PropertyID, eid); err != nil {
				return EventTicket{}, err
			}
			p, err := m.register(ctx, tx, eid, EventParticipantInput{CustomerID: &me.ID, PartySize: in.PartySize, DietaryNotes: in.DietaryNotes}, "member_app")
			if err != nil {
				return EventTicket{}, err
			}
			out := ticketOf(p)
			out.Checkout, err = m.checkout(ctx, tx, p, in.PayMethod, me.Name)
			return out, err
		})})
	crm.MeRoute(reg, "banquet", "Member Portal", route.Route{Method: http.MethodGet, Path: "/api/v1/member/my-events",
		Summary: "My Events: tickets and the banquets I booked with their payment schedule", Response: MyEvents{},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (MyEvents, error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return MyEvents{}, err
			}
			out := MyEvents{Hosted: []HostedEvent{}}
			if out.Tickets, err = myTickets(ctx, tx, me.ID, nil); err != nil {
				return out, err
			}
			rows, err := tx.Query(ctx, `SELECT id FROM banquet.events WHERE customer_id = $1 AND status <> 'cancelled' AND end_at > now() - interval '30 days'
				ORDER BY start_at`, me.ID)
			if err != nil {
				return out, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return out, err
			}
			for _, eid := range ids {
				d, err := m.Detail(ctx, tx, eid)
				if err != nil {
					return out, err
				}
				h := HostedEvent{ID: d.ID, Number: d.Number, Title: d.Title, Status: d.Status, Start: d.Start, End: d.End, Venues: []string{}, Pax: d.paxBasis(),
					Total: d.ContractTotal, Currency: d.Currency, FolioID: d.FolioID}
				for _, v := range d.Venues {
					if activeHold(v.Status) || v.Status == "completed" {
						h.Venues = append(h.Venues, v.VenueName)
					}
				}
				if d.ScheduleID != nil {
					sc, err := billing.GetSchedule(ctx, tx, *d.ScheduleID)
					if err != nil {
						return out, err
					}
					h.Schedule = &sc
				}
				if d.BEO != nil {
					h.BEOStatus = &d.BEO.Status
				}
				out.Hosted = append(out.Hosted, h)
			}
			return out, nil
		})})
	crm.MeRoute(reg, "banquet", "Member Portal", route.Route{Method: http.MethodPost, Path: "/api/v1/member/event-registrations/{ticketCode}:withdraw",
		Summary: "Withdraw my registration (Event Policies deadline and refund)", Request: RegistrationWithdrawInput{}, Response: EventTicket{},
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in RegistrationWithdrawInput) (EventTicket, error) {
			me, err := crm.Me(ctx, tx)
			if err != nil {
				return EventTicket{}, err
			}
			var pid uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM banquet.participants WHERE ticket_code = $1 AND customer_id = $2`,
				strings.ToUpper(chi.URLParam(r, "ticketCode")), me.ID).Scan(&pid); err != nil {
				if dbtx.IsNoRows(err) {
					return EventTicket{}, errs.NotFound("registration")
				}
				return EventTicket{}, err
			}
			reason := in.Reason
			if reason == "" {
				reason = "withdrawn by the member"
			}
			p, err := m.withdraw(ctx, tx, pid, reason, true)
			return ticketOf(p), err
		})})
}
