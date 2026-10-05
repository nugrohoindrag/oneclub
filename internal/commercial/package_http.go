package commercial

// Package routes: Back Office (Commercial → Packages: publish, availability,
// history; package bookings with cancellation and consumption), the website
// (Packages pages and Book Package, contract K5) and the member app (Stay &
// Venue → Packages).

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// OfferComponent is a component shown to customers.
type OfferComponent struct {
	Name          string `json:"name"`
	ComponentType string `json:"componentType"`
	Quantity      string `json:"quantity"`
	PerPax        bool   `json:"perPax"`
	PerNight      bool   `json:"perNight"`
	DayOffset     int    `json:"dayOffset"`
}

// OfferAddon is an optional component customers may add.
type OfferAddon struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Price    string    `json:"price"`
	PerPax   bool      `json:"perPax"`
	PerNight bool      `json:"perNight"`
}

// PackageOffer is a package as the website and the member app show it.
type PackageOffer struct {
	ID          uuid.UUID        `json:"id"`
	Code        string           `json:"code"`
	Name        string           `json:"name"`
	PackageType string           `json:"packageType"`
	Description *string          `json:"description"`
	PricingMode string           `json:"pricingMode" enum:"fixed,per_pax,per_night,per_pax_per_night"`
	Price       string           `json:"price"`
	TaxMode     string           `json:"taxMode" enum:"nett,plus_plus"`
	MinPax      int              `json:"minPax"`
	MaxPax      *int             `json:"maxPax"`
	Nights      int              `json:"nights"`
	ValidDays   []int32          `json:"validDays"`
	SellFrom    *string          `json:"sellFrom"`
	SellTo      *string          `json:"sellTo"`
	ValidFrom   *string          `json:"validFrom"`
	ValidTo     *string          `json:"validTo"`
	Version     int              `json:"version"`
	Components  []OfferComponent `json:"components"`
	Addons      []OfferAddon     `json:"addons"`
}

func offerOf(p PackageSpec) PackageOffer {
	o := PackageOffer{ID: p.ID, Code: p.Code, Name: p.Name, PackageType: p.PackageType, Description: p.Description, PricingMode: p.PricingMode,
		Price: dec(p.Price).String(), TaxMode: p.TaxMode, MinPax: p.MinPax, MaxPax: p.MaxPax, Nights: p.Nights, ValidDays: nonNilInts(p.ValidDays),
		SellFrom: p.SellFrom, SellTo: p.SellTo, ValidFrom: p.ValidFrom, ValidTo: p.ValidTo, Version: p.Version, Components: []OfferComponent{},
		Addons: []OfferAddon{}}
	for _, c := range p.Components {
		if c.Optional {
			price := "0"
			if c.AddonPrice != nil {
				price = dec(*c.AddonPrice).String()
			}
			o.Addons = append(o.Addons, OfferAddon{ID: c.ID, Name: c.Name, Price: price, PerPax: c.PerPax, PerNight: c.PerNight})
			continue
		}
		o.Components = append(o.Components, OfferComponent{Name: c.Name, ComponentType: c.ComponentType, Quantity: dec(c.Quantity).String(),
			PerPax: c.PerPax, PerNight: c.PerNight, DayOffset: c.DayOffset})
	}
	return o
}

// PackageOffers lists the published, public packages on sale today for a
// channel and segment.
func PackageOffers(ctx context.Context, q dbtx.Querier, property uuid.UUID, channel, segment string) ([]PackageOffer, error) {
	rows, err := q.Query(ctx, `SELECT id FROM commercial.packages WHERE property_id = $1 AND status = 'active' AND public AND archived_at IS NULL
		AND version > 0 ORDER BY name`, property)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	today := clock.Now().In(calendar.Location(ctx, q)).Format("2006-01-02")
	out := []PackageOffer{}
	for _, pid := range ids {
		p, err := PublishedSpec(ctx, q, pid)
		if err != nil {
			return nil, err
		}
		if (p.SellFrom != nil && today < *p.SellFrom) || (p.SellTo != nil && today > *p.SellTo) || (p.ValidTo != nil && today > *p.ValidTo) {
			continue
		}
		if channel != "" && len(p.Channels) > 0 && !slices.Contains(p.Channels, channel) {
			continue
		}
		if segment != "" && len(p.Segments) > 0 && !slices.Contains(p.Segments, segment) {
			continue
		}
		out = append(out, offerOf(p))
	}
	return out, nil
}

// PackageDetail is a package with its availability (website detail page).
type PackageDetail struct {
	Package      PackageOffer      `json:"package"`
	Availability []DayAvailability `json:"availability"`
}

// PublicBookingInput is Book Package on the website (non-member flow of P2).
type PublicBookingInput struct {
	PropertyID    uuid.UUID       `json:"propertyId"`
	Guest         crm.PublicGuest `json:"guest"`
	PackageCode   string          `json:"packageCode"`
	StartDate     string          `json:"startDate"`
	Pax           int             `json:"pax,omitempty"`
	Nights        int             `json:"nights,omitempty"`
	Addons        []uuid.UUID     `json:"addons,omitempty"`
	PromoCodes    []string        `json:"promoCodes,omitempty"`
	PaymentMethod string          `json:"paymentMethod,omitempty" enum:"qris,virtual_account,card" doc:"Pay online now; empty = pay later before the hold expires"`
	Notes         string          `json:"notes,omitempty"`
}

func (p PublicBookingInput) Property() uuid.UUID      { return p.PropertyID }
func (p PublicBookingInput) Visitor() crm.PublicGuest { return p.Guest }

// MemberBookingInput is Book Package in the member app.
type MemberBookingInput struct {
	PackageCode   string      `json:"packageCode"`
	StartDate     string      `json:"startDate"`
	Pax           int         `json:"pax,omitempty"`
	Nights        int         `json:"nights,omitempty"`
	Addons        []uuid.UUID `json:"addons,omitempty"`
	PromoCodes    []string    `json:"promoCodes,omitempty"`
	PaymentMethod string      `json:"paymentMethod,omitempty" enum:"qris,virtual_account,card,member_account" doc:"Empty = pay later before the hold expires"`
	Notes         string      `json:"notes,omitempty"`
}

// ChannelBooking is a website / app booking with its checkout.
type ChannelBooking struct {
	Booking  PackageBooking    `json:"booking"`
	Checkout *billing.Checkout `json:"checkout"`
}

// checkout opens the online payment of a channel booking (or charges the
// member account, which confirms the booking at once).
func (m *P3Module) checkout(ctx context.Context, tx pgx.Tx, b PackageBooking, method, payer string) (ChannelBooking, error) {
	out := ChannelBooking{Booking: b}
	if method == "" || b.FolioID == nil {
		return out, nil
	}
	amount := decimal.Zero
	if b.Schedule != nil && len(b.Schedule.Lines) > 0 {
		amount = dec(b.Schedule.Lines[0].Amount)
	}
	if method == "member_account" {
		if b.CustomerID == nil {
			return out, handle.Invalid("paymentMethod", "invalid", "member charge needs a member")
		}
		if amount.IsZero() {
			amount = dec(b.Total)
		}
		if _, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: b.FolioID, MethodType: "member_account",
			Amount: amount, Description: "Package " + b.Number, PayerName: payer}}); err != nil {
			return out, err
		}
		// a member charge is final: the booking is confirmed at once
		if b.Status == "pending" {
			if err := m.confirm(ctx, tx, b); err != nil {
				return out, err
			}
		}
		nb, err := GetPackageBooking(ctx, tx, b.ID)
		out.Booking = nb
		return out, err
	}
	co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: *b.FolioID, Method: method, Amount: amount, Description: "Package " + b.Number,
		PayerName: payer})
	if err != nil {
		return out, err
	}
	out.Checkout = &co
	return out, nil
}

// ConsumptionRow is a recorded consumption of a booking.
type ConsumptionRow struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	BookingComponentID uuid.UUID  `json:"bookingComponentId" db:"booking_component_id"`
	Component          string     `json:"component" db:"component"`
	Quantity           string     `json:"quantity" db:"quantity"`
	BusinessDate       string     `json:"businessDate" db:"business_date"`
	OutletID           *uuid.UUID `json:"outletId" db:"outlet_id"`
	Reference          *string    `json:"reference" db:"reference"`
	Net                string     `json:"net" db:"net"`
	Service            string     `json:"service" db:"service"`
	Tax                string     `json:"tax" db:"tax"`
	Total              string     `json:"total" db:"total"`
	ConsumedAt         time.Time  `json:"consumedAt" db:"consumed_at"`
}

// availabilityQuery reads date, days, pax, nights of an availability request.
func availabilityQuery(r *http.Request) (time.Time, int, int, int, error) {
	d, err := handle.QueryDate(r, "date", clock.Now())
	if err != nil {
		return d, 0, 0, 0, err
	}
	return d, min(max(handle.QueryInt(r, "days", 1), 1), 31), handle.QueryInt(r, "pax", 0), handle.QueryInt(r, "nights", 0), nil
}

func (m *P3Module) registerPackages(reg routeAdder) {
	db := m.DB
	add := func(rt routeSpec) { reg.add(rt, "Packages") }
	add(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/packages/{id}:publish", summary: "Publish Package (new version; bookings keep theirs)",
		perm: "commercial.package.publish", req: handle.Empty{}, res: PackageSpec{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, _ handle.Empty) (PackageSpec, error) {
			pid, err := handle.ID(r)
			if err != nil {
				return PackageSpec{}, err
			}
			return m.Publish(ctx, tx, pid)
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/packages/{id}/availability", summary: "Package Availability: sale rules, quota and every component",
		perm: "commercial.package.view", res: DayAvailability{}, list: true,
		query: []queryParam{{name: "date"}, {name: "days", typ: "integer"}, {name: "pax", typ: "integer"}, {name: "nights", typ: "integer"},
			{name: "channel", enum: PackageChannels}, {name: "customerId"}},
		h: dryRead(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[DayAvailability], error) {
			pid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[DayAvailability]{}, err
			}
			d, days, pax, nights, err := availabilityQuery(r)
			if err != nil {
				return httpx.Page[DayAvailability]{}, err
			}
			cust, err := handle.QueryUUID(r, "customerId")
			if err != nil {
				return httpx.Page[DayAvailability]{}, err
			}
			return handle.Page(m.Availability(ctx, tx, handle.Property(ctx), pid, d, days, pax, nights, nonEmpty(r.URL.Query().Get("channel"), "back_office"), cust))
		})})
	add(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/packages/{id}/versions", summary: "Package History (published versions)",
		perm: "commercial.package.view", res: PackageVersion{}, list: true,
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PackageVersion], error) {
			pid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[PackageVersion]{}, err
			}
			return handle.Page(Versions(ctx, tx, pid))
		})})
	bk := func(rt routeSpec) { reg.add(rt, "Package Bookings") }
	bk(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/package-bookings", summary: "Package Booking (all components allocated or none)",
		perm: "commercial.package_booking.create", req: BookingInput{}, res: PackageBooking{}, status: http.StatusCreated, idempotent: true,
		h: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in BookingInput) (PackageBooking, error) {
			if in.Channel == "website" || in.Channel == "member_app" {
				return PackageBooking{}, handle.Invalid("channel", "invalid_channel", "website and member app bookings use their own endpoints")
			}
			return m.BookPackage(ctx, tx, handle.Property(ctx), in, r.Header.Get("Idempotency-Key"))
		})})
	bk(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/package-bookings", summary: "Package Bookings", perm: "commercial.package_booking.view",
		res: PackageBooking{}, list: true,
		query: []queryParam{{name: "filter[status]"}, {name: "filter[packageId]"}, {name: "filter[customerId]"}, {name: "date", doc: "Start date"}},
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PackageBooking], error) {
			lp := httpx.ParseList(r)
			d := r.URL.Query().Get("date")
			if d != "" {
				if _, err := time.Parse("2006-01-02", d); err != nil {
					return httpx.Page[PackageBooking]{}, errs.BadRequest("invalid_date", "date must be YYYY-MM-DD")
				}
			}
			list, err := handle.List[PackageBooking](tx.Query(ctx, bookingSelect+` WHERE b.property_id = $1 AND ($2 = '' OR b.status = ANY(string_to_array($2, ',')))
				AND ($3 = '' OR b.package_id::text = $3) AND ($4 = '' OR b.customer_id::text = $4) AND ($5 = '' OR b.start_date = $5::date)
				ORDER BY b.start_date DESC, b.created_at DESC LIMIT $6`, handle.Property(ctx), lp.Filters["status"], lp.Filters["packageId"],
				lp.Filters["customerId"], d, lp.Limit))
			for i := range list {
				list[i].Components, list[i].Promotions = []BookingComponent{}, []Redemption{}
			}
			return handle.Page(list, err)
		})})
	bk(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/package-bookings/{id}", summary: "Package Booking detail with revenue allocation",
		perm: "commercial.package_booking.view", res: PackageBooking{},
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (PackageBooking, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return PackageBooking{}, err
			}
			return GetPackageBooking(ctx, tx, bid)
		})})
	bk(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/package-bookings/{id}:cancel", summary: "Cancel Package Booking (unused components released, fee per policy)",
		perm: "commercial.package_booking.cancel", req: PackageCancelInput{}, res: PackageCancelResult{}, status: http.StatusOK,
		h: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in PackageCancelInput) (PackageCancelResult, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return PackageCancelResult{}, err
			}
			return m.CancelBooking(ctx, tx, bid, in.Reason, "cancelled")
		})})
	bk(routeSpec{method: http.MethodPost, path: "/api/v1/commercial/package-bookings/{id}/consumption", summary: "Package PackageConsumption: use a component (K6 event with BOM)",
		perm: "commercial.package_booking.consume", req: ConsumeInput{}, res: PackageConsumption{}, status: http.StatusCreated, idempotent: true,
		h: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in ConsumeInput) (PackageConsumption, error) {
			bid, err := handle.ID(r)
			if err != nil {
				return PackageConsumption{}, err
			}
			return m.Consume(ctx, tx, bid, in, r.Header.Get("Idempotency-Key"))
		})})
	bk(routeSpec{method: http.MethodGet, path: "/api/v1/commercial/package-bookings/{id}/consumption", summary: "Consumptions of a package booking",
		perm: "commercial.package_booking.view", res: ConsumptionRow{}, list: true,
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[ConsumptionRow], error) {
			bid, err := handle.ID(r)
			if err != nil {
				return httpx.Page[ConsumptionRow]{}, err
			}
			return handle.Page(handle.List[ConsumptionRow](tx.Query(ctx, `SELECT pc.id, pc.booking_component_id, c.name AS component,
				trim_scale(pc.quantity)::text AS quantity, to_char(pc.business_date, 'YYYY-MM-DD') AS business_date, pc.outlet_id, pc.reference,
				trim_scale(pc.net)::text AS net, trim_scale(pc.service)::text AS service, trim_scale(pc.tax)::text AS tax, trim_scale(pc.total)::text AS total,
				pc.consumed_at FROM commercial.package_consumptions pc JOIN commercial.package_booking_components c ON c.id = pc.booking_component_id
				WHERE pc.booking_id = $1 ORDER BY pc.consumed_at`, bid)))
		})})
	// website (K5) and member app
	reg.public(routeSpec{method: http.MethodGet, path: "/api/v1/public/packages", summary: "Packages on sale for the website (K5)", res: PackageOffer{}, list: true,
		query: []queryParam{{name: "propertyId", required: true}},
		h: m.publicRead(func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (any, error) {
			list, err := PackageOffers(ctx, tx, pid, "website", "")
			return httpx.Page[PackageOffer]{Items: list}, err
		})})
	reg.public(routeSpec{method: http.MethodGet, path: "/api/v1/public/packages/{code}", summary: "Package detail with availability for a date (K5)", res: PackageDetail{},
		query: []queryParam{{name: "propertyId", required: true}, {name: "date"}, {name: "days", typ: "integer"}, {name: "pax", typ: "integer"},
			{name: "nights", typ: "integer"}},
		h: m.publicDry(func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID) (any, error) {
			pkg, err := packageByCode(ctx, tx, pid, chi.URLParam(r, "code"))
			if err != nil {
				return nil, err
			}
			spec, err := PublishedSpec(ctx, tx, pkg)
			if err != nil || spec.Status != "active" || !spec.Public || (len(spec.Channels) > 0 && !slices.Contains(spec.Channels, "website")) {
				return nil, errs.NotFound("package")
			}
			out := PackageDetail{Package: offerOf(spec), Availability: []DayAvailability{}}
			if r.URL.Query().Get("date") == "" {
				return out, nil
			}
			d, days, pax, nights, err := availabilityQuery(r)
			if err != nil {
				return nil, err
			}
			out.Availability, err = m.Availability(ctx, tx, pid, pkg, d, min(days, 14), pax, nights, "website", nil)
			return out, err
		})})
	reg.public(routeSpec{method: http.MethodPost, path: "/api/v1/public/package-bookings", summary: "Book Package on the website (held until paid)",
		req: PublicBookingInput{}, res: ChannelBooking{}, status: http.StatusCreated,
		h: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer,
			in PublicBookingInput) (ChannelBooking, error) {
			b, err := m.BookPackage(ctx, tx, pid, BookingInput{PackageCode: in.PackageCode, StartDate: in.StartDate, Pax: in.Pax, Nights: in.Nights,
				Addons: in.Addons, PromoCodes: in.PromoCodes, CustomerID: &c.ID, GuestName: in.Guest.Name, GuestPhone: in.Guest.Phone,
				GuestEmail: in.Guest.Email, Channel: "website", Notes: in.Notes}, "")
			if err != nil {
				return ChannelBooking{}, err
			}
			if in.PaymentMethod == "member_account" {
				return ChannelBooking{}, handle.Invalid("paymentMethod", "invalid", "online payment methods only")
			}
			return m.checkout(ctx, tx, b, in.PaymentMethod, in.Guest.Name)
		})})
	reg.member(routeSpec{method: http.MethodGet, path: "/api/v1/member/package-bookings", summary: "My Packages", res: PackageBooking{}, list: true,
		h: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[PackageBooking], error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[PackageBooking]{}, err
			}
			rows, err := tx.Query(ctx, `SELECT id FROM commercial.package_bookings WHERE customer_id = $1 ORDER BY start_date DESC LIMIT 50`, c.ID)
			if err != nil {
				return httpx.Page[PackageBooking]{}, err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil {
				return httpx.Page[PackageBooking]{}, err
			}
			out := []PackageBooking{}
			for _, id := range ids {
				b, err := GetPackageBooking(ctx, tx, id)
				if err != nil {
					return httpx.Page[PackageBooking]{}, err
				}
				out = append(out, b)
			}
			return httpx.Page[PackageBooking]{Items: out}, nil
		})})
	reg.member(routeSpec{method: http.MethodPost, path: "/api/v1/member/package-bookings", summary: "Book a Package (member app)",
		req: MemberBookingInput{}, res: ChannelBooking{}, status: http.StatusCreated,
		h: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MemberBookingInput) (ChannelBooking, error) {
			c, err := crm.Me(ctx, tx)
			if err != nil {
				return ChannelBooking{}, err
			}
			b, err := m.BookPackage(ctx, tx, c.PropertyID, BookingInput{PackageCode: in.PackageCode, StartDate: in.StartDate, Pax: in.Pax, Nights: in.Nights,
				Addons: in.Addons, PromoCodes: in.PromoCodes, CustomerID: &c.ID, Channel: "member_app", Notes: in.Notes}, "")
			if err != nil {
				return ChannelBooking{}, err
			}
			return m.checkout(ctx, tx, b, in.PaymentMethod, c.Name)
		})})
}
