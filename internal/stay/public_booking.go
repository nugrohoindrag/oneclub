package stay

// Website routes of the bungalow booking engine (docs/requirement-booking-
// hotel-mgcc.md Bagian A): the room types with their content, the rate
// table of the sidebar, the search, the cart quote, the booking with the
// online deposit on the mock gateway, the booking page of a link (status,
// e-voucher PDF, calendar, payment, cancellation), Cek Booking and the
// waitlist of a full type. No account is needed (FR-H25); the writes are
// rate limited with the honeypot of the public forms.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/pdf"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// PublicBookingInput books a cart from the website (FR-H25–H35).
type PublicBookingInput struct {
	PropertyID uuid.UUID       `json:"propertyId"`
	Guest      crm.PublicGuest `json:"guest" doc:"Booker: name, phone (+62) and e-mail"`
	CartInput
	BookerTitle      string `json:"bookerTitle,omitempty" enum:"mr,mrs,ms"`
	Nationality      string `json:"nationality,omitempty"`
	ExpectedArrival  string `json:"expectedArrival,omitempty" doc:"HH:MM"`
	SpecialRequests  string `json:"specialRequests,omitempty" doc:"Up to 255 characters, subject to availability"`
	AcceptTerms      bool   `json:"acceptTerms" doc:"Terms & Conditions and the cancellation policy accepted (required)"`
	MarketingConsent bool   `json:"marketingConsent,omitempty"`
	PayMethod        string `json:"payMethod,omitempty" enum:"qris,virtual_account,card" doc:"Online method of the deposit; not needed when every rate is paid at the hotel"`
}

func (p PublicBookingInput) Property() uuid.UUID      { return p.PropertyID }
func (p PublicBookingInput) Visitor() crm.PublicGuest { return p.Guest }

// PublicQuoteInput prices a website cart.
type PublicQuoteInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
	CartInput
}

// TokenInput names the booking link.
type TokenInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
}

// PublicPayInput opens the payment again.
type PublicPayInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
	Method     string    `json:"method" enum:"qris,virtual_account,card"`
}

// PublicCancelInput cancels bungalows of a booking.
type PublicCancelInput struct {
	PropertyID uuid.UUID   `json:"propertyId"`
	StayIDs    []uuid.UUID `json:"stayIds,omitempty" doc:"Bungalows to cancel; empty = every bungalow that can be cancelled"`
	Reason     string      `json:"reason,omitempty"`
}

// BookingLookupInput is Cek Booking: code + phone or e-mail.
type BookingLookupInput struct {
	PropertyID uuid.UUID `json:"propertyId"`
	Code       string    `json:"code" doc:"Reservation code (GRP-…, RSV-… or STY-…)"`
	Contact    string    `json:"contact" doc:"Phone or e-mail of the booker"`
}

// BookingLookup is the link found.
type BookingLookup struct {
	Token string `json:"token"`
}

// PublicWaitlistInput joins the waitlist of a full room type (FR-H11).
type PublicWaitlistInput struct {
	PropertyID     uuid.UUID       `json:"propertyId"`
	Guest          crm.PublicGuest `json:"guest"`
	BungalowTypeID uuid.UUID       `json:"bungalowTypeId"`
	ArrivalDate    string          `json:"arrivalDate"`
	DepartureDate  string          `json:"departureDate"`
	Adults         int             `json:"adults,omitempty"`
	Children       int             `json:"children,omitempty"`
}

func (p PublicWaitlistInput) Property() uuid.UUID      { return p.PropertyID }
func (p PublicWaitlistInput) Visitor() crm.PublicGuest { return p.Guest }

// PublicStayPage is the content of the bungalow pages.
type PublicStayPage struct {
	Types        []PublicRoomType `json:"types"`
	CheckInTime  string           `json:"checkInTime"`
	CheckOutTime string           `json:"checkOutTime"`
	Terms        string           `json:"terms"`
	TermsEn      string           `json:"termsEn"`
	HouseRules   string           `json:"houseRules"`
	HouseRulesEn string           `json:"houseRulesEn"`
	ChildPolicy  string           `json:"childPolicy"`
	Contact      BookingContact   `json:"contact"`
	Methods      []string         `json:"methods"`
	HoldMinutes  int              `json:"holdMinutes"`
	MinDate      string           `json:"minDate"`
	MaxDate      string           `json:"maxDate"`
	Addons       []GuestAddon     `json:"addons" doc:"Add-ons bookable with the stay (FR-H22)"`
}

func publicProperty(r *http.Request) (uuid.UUID, error) {
	pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
	if err != nil {
		return uuid.Nil, errs.BadRequest("property_required", "propertyId is required")
	}
	return pid, nil
}

// pubRead runs a public read on the replica.
func (m *Module) pubRead(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error)) {
	pid, err := publicProperty(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ctx := crm.PublicCtx(r.Context(), pid)
	var out any
	err = m.DB.WithReadTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = fn(ctx, tx, pid)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// pubWrite runs a rate-limited public write (the body names the property).
func pubWrite[T interface{ prop() uuid.UUID }](m *Module, w http.ResponseWriter, r *http.Request, rollback bool, fn func(ctx context.Context, tx pgx.Tx, in T) (any, error)) {
	if !crm.PublicLimiter.Allow(r.RemoteAddr + r.URL.Path) {
		httpx.WriteError(w, r, errs.RateLimited())
		return
	}
	var in T
	if err := httpx.Decode(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pid := in.prop()
	if pid == uuid.Nil {
		httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
		return
	}
	ctx := crm.PublicCtx(r.Context(), pid)
	var out any
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		if rollback {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			out, err = fn(ctx, sp, in)
			return err
		}
		var err error
		out, err = fn(ctx, tx, in)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (p PublicQuoteInput) prop() uuid.UUID   { return p.PropertyID }
func (p TokenInput) prop() uuid.UUID         { return p.PropertyID }
func (p PublicPayInput) prop() uuid.UUID     { return p.PropertyID }
func (p PublicCancelInput) prop() uuid.UUID  { return p.PropertyID }
func (p BookingLookupInput) prop() uuid.UUID { return p.PropertyID }

func (m *Module) registerPublicBooking(reg *route.Registry) {
	db := m.DB
	pub := func(rt route.Route) { crm.PublicRoute(reg, "stay", rt) }
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay/types", Summary: "Bungalow types with their website content, policies and contact",
		Response: PublicStayPage{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.pubRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) {
				pol, _, err := m.policy(ctx, tx, pid)
				if err != nil {
					return nil, err
				}
				today := localToday(ctx, tx)
				out := PublicStayPage{CheckInTime: pol.CheckInTime, CheckOutTime: pol.CheckOutTime, Terms: pol.Terms, TermsEn: pol.TermsEn, HouseRules: pol.HouseRules,
					HouseRulesEn: pol.HouseRulesEn, ChildPolicy: pol.ChildPolicy, Contact: m.contactOf(ctx, tx, pid, pol), Methods: methodsOf(pol),
					HoldMinutes: pol.WebsiteHoldMinutes, MinDate: today.Format(time.DateOnly), MaxDate: today.AddDate(0, 0, max(pol.BookingWindowDays, 1)).Format(time.DateOnly)}
				if out.Addons, err = handle.List[GuestAddon](tx.Query(ctx, `SELECT id, name, category, description, trim_scale(price)::text AS price, unit
					FROM stay.addons WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND availability IN ('booking', 'both')
					ORDER BY category, name`, pid)); err != nil {
					return nil, err
				}
				out.Types, err = roomTypes(ctx, tx, pid, true)
				return out, err
			})
		}})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay/rate-card", Summary: "Rate table of the bungalows from the Stay prices (website sidebar)",
		Response: RateCardRow{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := publicProperty(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out []RateCardRow
			// the quote runs in savepoints that are rolled back: a write transaction
			err = db.WithTx(ctx, func(tx pgx.Tx) error {
				var err error
				out, err = m.RateCard(ctx, tx, pid)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, httpx.Page[RateCardRow]{Items: out})
		}})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay/search",
		Summary: "Search: room types with the bungalows left and the price of every website rate plan for the stay", Response: PublicSearch{},
		Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "checkin", Required: true, Description: "YYYY-MM-DD"},
			{Name: "checkout", Required: true, Description: "YYYY-MM-DD"}, {Name: "adults", Type: "integer"}, {Name: "children", Type: "integer"},
			{Name: "promo"}, {Name: "typeId"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := publicProperty(r)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out PublicSearch
			err = db.WithTx(ctx, func(tx pgx.Tx) error {
				in, err := searchQuery(ctx, tx, r)
				if err != nil {
					return err
				}
				in.Source = "website"
				out, err = m.ChannelSearch(ctx, tx, pid, in)
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stays:quote", Summary: "Price summary of a website cart (nothing is kept)",
		Request: PublicQuoteInput{}, Response: CartQuote{}, NoAudit: "read-only preview, rolled back",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pubWrite(m, w, r, true, func(ctx context.Context, tx pgx.Tx, in PublicQuoteInput) (any, error) {
				return m.QuoteCart(ctx, tx, in.PropertyID, in.CartInput, cartBase{Channel: "website", Source: "website"})
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-bookings",
		Summary: "Book the cart: the bungalows are held and the deposit opened on the payment gateway (mock); pay-at-hotel rates are confirmed at once",
		Request: PublicBookingInput{}, Response: BookingView{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicBookingInput) (BookingView, error) {
			if !in.AcceptTerms {
				return BookingView{}, handle.Invalid("acceptTerms", "required", "setujui Syarat & Ketentuan dan kebijakan pembatalan")
			}
			if len([]rune(in.SpecialRequests)) > 255 {
				return BookingView{}, handle.Invalid("specialRequests", "too_long", "permintaan khusus paling banyak 255 karakter")
			}
			pol, _, err := m.policy(ctx, tx, pid)
			if err != nil {
				return BookingView{}, err
			}
			if in.PayMethod != "" && !contains(methodsOf(pol), in.PayMethod) {
				return BookingView{}, handle.Invalid("payMethod", "invalid_method", "metode pembayaran tidak tersedia")
			}
			b := cartBase{Channel: "website", Source: "website", CustomerID: &c.ID, Guest: &GuestInput{Name: in.Guest.Name, Phone: in.Guest.Phone,
				Email: in.Guest.Email}, BookerTitle: in.BookerTitle, Nationality: in.Nationality, ExpectedArrival: in.ExpectedArrival,
				SpecialRequests: strings.TrimSpace(in.SpecialRequests), MarketingConsent: in.MarketingConsent, Hold: true}
			res, err := m.bookCart(ctx, tx, pid, in.CartInput, b, in.PayMethod, "")
			if err != nil {
				return BookingView{}, err
			}
			if decOf(res.Deposit).IsPositive() && in.PayMethod == "" {
				return BookingView{}, handle.Invalid("payMethod", "required", "pilih metode pembayaran deposit")
			}
			for _, s := range res.Stays {
				if s.GuestEmail == nil && in.Guest.Email != "" {
					if _, err := tx.Exec(ctx, `UPDATE stay.stays SET guest_email = $2, guest_phone = coalesce(guest_phone, $3) WHERE id = $1`, s.ID, in.Guest.Email,
						nzs(in.Guest.Phone)); err != nil {
						return BookingView{}, err
					}
				}
			}
			return m.BookingOf(ctx, tx, pid, res.Token)
		})})
	pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay-bookings/{token}", Summary: "Booking page of the link: status, bungalows, payment, policies",
		Response: BookingView{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			m.pubRead(w, r, func(ctx context.Context, tx pgx.Tx, pid uuid.UUID) (any, error) {
				return m.BookingOf(ctx, tx, pid, chi.URLParam(r, "token"))
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-bookings/{token}:abandon",
		Summary: "The online payment failed: the bungalows are released (Expired) and the guest goes back to the cart", Request: TokenInput{}, Response: BookingView{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pubWrite(m, w, r, false, func(ctx context.Context, tx pgx.Tx, in TokenInput) (any, error) {
				return m.AbandonBooking(ctx, tx, in.PropertyID, chi.URLParam(r, "token"), "the online payment failed")
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-bookings/{token}:pay", Summary: "Continue the payment while the bungalows are held",
		Request: PublicPayInput{}, Response: BookingView{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pubWrite(m, w, r, false, func(ctx context.Context, tx pgx.Tx, in PublicPayInput) (any, error) {
				return m.ContinuePayment(ctx, tx, in.PropertyID, chi.URLParam(r, "token"), in.Method)
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-bookings/{token}:cancel",
		Summary: "Cancel by the guest under the policy of the rate plan (non-refundable rates cannot be cancelled)", Request: PublicCancelInput{},
		Response: BookingView{},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pubWrite(m, w, r, false, func(ctx context.Context, tx pgx.Tx, in PublicCancelInput) (any, error) {
				return m.CancelByGuest(ctx, tx, in.PropertyID, chi.URLParam(r, "token"), in.StayIDs, in.Reason)
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-bookings:lookup", Summary: "Cek Booking: reservation code + phone or e-mail → the link",
		Request: BookingLookupInput{}, Response: BookingLookup{}, NoAudit: "read-only lookup (POST keeps the contact out of the URL)",
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pubWrite(m, w, r, true, func(ctx context.Context, tx pgx.Tx, in BookingLookupInput) (any, error) {
				t, err := m.tokenOfLookup(ctx, tx, in.PropertyID, in.Code, in.Contact)
				return BookingLookup{Token: t}, err
			})
		}})
	pub(route.Route{Method: http.MethodPost, Path: "/api/v1/public/stay-waitlist", Summary: "Join the waitlist of a full bungalow type (website)",
		Request: PublicWaitlistInput{}, Response: WaitlistEntry{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicWaitlistInput) (WaitlistEntry, error) {
			loc := calendar.Location(ctx, tx)
			a, aerr := time.ParseInLocation(time.DateOnly, in.ArrivalDate, loc)
			d, derr := time.ParseInLocation(time.DateOnly, in.DepartureDate, loc)
			if aerr != nil || derr != nil || !d.After(a) {
				return WaitlistEntry{}, handle.Invalid("departureDate", "invalid_period", "check-out harus setelah check-in")
			}
			return m.joinWaitlist(ctx, tx, pid, WaitlistInput{CustomerID: &c.ID, BungalowTypeID: in.BungalowTypeID, ArrivalDate: in.ArrivalDate,
				Nights: int(d.Sub(a).Hours()/24 + 0.5), Adults: max(in.Adults, 1), Children: in.Children, BookingSource: "website"})
		})})
	file := func(path, summary, contentType string, fn func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, v BookingView) ([]byte, string, error)) {
		pub(route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay-bookings/{token}/" + path, Summary: summary, RawContent: contentType,
			Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "lang", Description: "id | en"}},
			Handler: func(w http.ResponseWriter, r *http.Request) {
				pid, err := publicProperty(r)
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				ctx := crm.PublicCtx(r.Context(), pid)
				var body []byte
				var name string
				err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
					v, err := m.BookingOf(ctx, tx, pid, chi.URLParam(r, "token"))
					if err != nil {
						return err
					}
					body, name, err = fn(ctx, tx, pid, v)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write(body)
			}})
	}
	file("e-voucher.pdf", "E-voucher of the booking (PDF)", "application/pdf", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, v BookingView) ([]byte, string, error) {
		body, err := voucherPDF(v, calendar.Location(ctx, tx))
		return body, "e-voucher-" + v.Code + ".pdf", err
	})
	file("calendar.ics", "Add the stay to a calendar (.ics)", "text/calendar", func(ctx context.Context, tx pgx.Tx, pid uuid.UUID, v BookingView) ([]byte, string, error) {
		return stayICS(v, calendar.Location(ctx, tx)), "bungalow-" + v.Code + ".ics", nil
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// searchQuery reads the search of the website / Member App (FR-H08).
func searchQuery(ctx context.Context, tx pgx.Tx, r *http.Request) (channelSearch, error) {
	loc := calendar.Location(ctx, tx)
	q := r.URL.Query()
	get := func(a, b string) string {
		if v := q.Get(a); v != "" {
			return v
		}
		return q.Get(b)
	}
	a, err := time.ParseInLocation(time.DateOnly, get("checkin", "arrival"), loc)
	if err != nil {
		return channelSearch{}, handle.Invalid("checkin", "invalid_date", "pilih tanggal check-in")
	}
	d, err := time.ParseInLocation(time.DateOnly, get("checkout", "departure"), loc)
	if err != nil {
		return channelSearch{}, handle.Invalid("checkout", "invalid_date", "pilih tanggal check-out")
	}
	in := channelSearch{Arrival: a, Departure: d, Adults: max(handle.QueryInt(r, "adults", 2), 1), Children: max(handle.QueryInt(r, "children", 0), 0),
		PromoCode: get("promo", "promoCode")}
	in.TypeID, err = handle.QueryUUID(r, "typeId")
	return in, err
}

// ── e-voucher and calendar (FR-H38, FR-H39) ───────────────────────────────

func rupiah(s string) string {
	d := decOf(s).Round(0)
	neg := d.IsNegative()
	str := d.Abs().String()
	var b strings.Builder
	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-Rp " + b.String()
	}
	return "Rp " + b.String()
}

var statusID = map[string]string{"awaiting_payment": "Menunggu Pembayaran", "confirmed": "Terkonfirmasi", "in_house": "Menginap", "checked_out": "Check-out",
	"cancelled": "Dibatalkan", "no_show": "No-show", "expired": "Kedaluwarsa", "void": "Void", "requested": "Permintaan", "mixed": "Beberapa status"}

// voucherPDF renders the e-voucher: code, booker, bungalows, payment,
// cancellation policy, check-in / check-out and the contact of the club.
func voucherPDF(v BookingView, loc *time.Location) ([]byte, error) {
	d := pdf.New()
	d.Row(16, true, "E-Voucher Bungalow", v.Contact.Name)
	d.Row(10, false, "Kode reservasi: "+v.Code, "Status: "+statusID[v.Status])
	d.Row(10, false, "Pemesan: "+v.BookerName, strings.Trim(deref(v.BookerPhone)+" · "+deref(v.BookerEmail), " ·"))
	d.Space(6)
	for _, s := range v.Stays {
		if s.BookingStatus == "expired" || s.BookingStatus == "void" {
			continue
		}
		d.Row(11, true, s.TypeName+" · "+s.RatePlanName, s.StayNo)
		d.Row(9, false, fmt.Sprintf("%s (check-in %s) – %s (check-out %s) · %d malam", s.Arrival.In(loc).Format("Mon 02 Jan 2006"), v.CheckInTime,
			s.Departure.In(loc).Format("Mon 02 Jan 2006"), v.CheckOutTime, s.Nights), statusID[s.BookingStatus])
		guests := fmt.Sprintf("%d dewasa", s.Adults)
		if s.Children > 0 {
			guests += fmt.Sprintf(", %d anak", s.Children)
		}
		if s.OccupantName != nil && *s.OccupantName != "" {
			guests += " · tamu: " + *s.OccupantName
		}
		breakfast := "tanpa sarapan"
		if s.IncludesBreakfast {
			breakfast = "termasuk sarapan"
		}
		d.Row(9, false, guests+" · "+breakfast, rupiah(s.Total))
		for _, a := range s.Addons {
			if a.VoidedAt == nil {
				d.Row(8, false, fmt.Sprintf("  + %s × %d", a.Name, a.Quantity), rupiah(a.Total))
			}
		}
		switch {
		case s.NonRefundable:
			d.Row(8, false, "Kebijakan pembatalan: non-refundable (tidak dapat dibatalkan)")
		case s.FreeCancelUntil != nil:
			d.Row(8, false, fmt.Sprintf("Batal gratis sampai %s; setelahnya biaya %s%%", s.FreeCancelUntil.In(loc).Format("02 Jan 2006 15:04"), s.CancelFeePercent))
		}
		d.Space(3)
	}
	d.Space(3)
	d.Row(10, true, "Total", rupiah(v.Total))
	d.Row(10, false, "Dibayar", rupiah(v.Paid))
	d.Row(10, false, "Sisa dibayar di hotel", rupiah(v.Balance))
	d.Space(6)
	d.Row(9, true, v.Contact.Name)
	if v.Contact.Address != "" {
		d.Row(9, false, v.Contact.Address)
	}
	contact := strings.Trim(strings.Join([]string{v.Contact.Phone, v.Contact.Email, "WA " + v.Contact.WhatsApp}, " · "), " ·WA")
	if contact != "" {
		d.Row(9, false, contact)
	}
	d.Space(4)
	d.Row(9, true, "Syarat & Ketentuan")
	for _, line := range wrapText(v.Terms, 110) {
		d.Row(8, false, line)
	}
	d.Space(4)
	d.Row(8, false, "Tunjukkan e-voucher ini dan KTP/paspor saat check-in. Dibuat "+clock.Now().In(loc).Format("02 Jan 2006 15:04"))
	return d.Bytes(), nil
}

func wrapText(s string, n int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		cur := ""
		for _, w := range strings.Fields(para) {
			if len(cur)+len(w)+1 > n {
				out = append(out, cur)
				cur = w
				continue
			}
			if cur != "" {
				cur += " "
			}
			cur += w
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// stayICS puts the stays of a booking in a calendar.
func stayICS(v BookingView, loc *time.Location) []byte {
	f := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	esc := func(s string) string {
		return strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\n", `\n`).Replace(s)
	}
	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//OneClub//Bungalow//ID\r\n")
	for _, s := range v.Stays {
		if s.BookingStatus == "expired" || s.BookingStatus == "void" || s.BookingStatus == "cancelled" {
			continue
		}
		sb.WriteString("BEGIN:VEVENT\r\nUID:" + s.ID.String() + "@oneclub\r\nDTSTAMP:" + f(clock.Now()) + "\r\n")
		sb.WriteString("DTSTART:" + f(s.Arrival) + "\r\nDTEND:" + f(s.Departure) + "\r\n")
		sb.WriteString("SUMMARY:" + esc(s.TypeName+" · "+v.Contact.Name) + "\r\n")
		sb.WriteString("LOCATION:" + esc(strings.Trim(v.Contact.Name+", "+v.Contact.Address, ", ")) + "\r\n")
		sb.WriteString("DESCRIPTION:" + esc(fmt.Sprintf("Reservasi %s (%s). Check-in %s, check-out %s. Bawa KTP/paspor.", v.Code, s.StayNo, v.CheckInTime,
			v.CheckOutTime)) + "\r\nEND:VEVENT\r\n")
	}
	_ = loc
	sb.WriteString("END:VCALENDAR\r\n")
	return []byte(sb.String())
}
