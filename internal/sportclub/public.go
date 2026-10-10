package sportclub

// Website Sport Club (PRD P2 EP-26 FR-WEB-P2-01/02): Sport Club page data
// and non-member booking of courts and classes with online payment.

import (
	"context"
	"net/http"
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

type PublicFacility struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	Code          string          `json:"code" db:"code"`
	Name          string          `json:"name" db:"name"`
	FacilityType  *string         `json:"facilityType" db:"facility_type"`
	UsageMode     string          `json:"usageMode" db:"usage_mode"`
	OpeningHours  map[string]any  `json:"openingHours" db:"opening_hours"`
	SortOrder     int             `json:"sortOrder" db:"sort_order"`
	OnlineBooking bool            `json:"onlineBooking" db:"online_booking"`
	Content       FacilityContent `json:"content" db:"content"`
	Rules         FacilityRules   `json:"rules" db:"booking_rules"`
	Courts        int             `json:"courts" db:"courts" doc:"Courts bookable online"`
	Indoor        bool            `json:"indoor" db:"indoor"`
	FromPrice     *string         `json:"fromPrice" db:"from_price" doc:"Lowest hourly rate before tax (rate card)"`
}

type PublicCourt struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	Code          string     `json:"code" db:"code"`
	Name          string     `json:"name" db:"name"`
	FacilityID    uuid.UUID  `json:"facilityId" db:"facility_id"`
	Surface       *string    `json:"surface" db:"surface"`
	Indoor        bool       `json:"indoor" db:"indoor"`
	ResourceID    *uuid.UUID `json:"resourceId" db:"resource_id"`
	SortOrder     int        `json:"sortOrder" db:"sort_order"`
	OnlineBooking bool       `json:"onlineBooking" db:"online_booking"`
	PhotoURL      *string    `json:"photoUrl" db:"photo_url"`
	PriceItem     string     `json:"priceItem" db:"price_item"`
}

type PublicProgram struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Discipline  string    `json:"discipline" db:"discipline"`
	Level       *string   `json:"level" db:"level"`
	MinAge      *int      `json:"minAge" db:"min_age"`
	MaxAge      *int      `json:"maxAge" db:"max_age"`
	Description *string   `json:"description" db:"description"`
}

// PublicSportClub is the Sport Club page.
type PublicSportClub struct {
	Facilities  []PublicFacility  `json:"facilities"`
	Courts      []PublicCourt     `json:"courts"`
	Programs    []PublicProgram   `json:"classPrograms"`
	WindowDays  int               `json:"windowDays"`
	HoldMinutes int               `json:"holdMinutes"`
	TaxIncluded bool              `json:"taxIncluded"`
	Methods     []OnlineMethod    `json:"methods"`
	Terms       map[string]string `json:"terms"`
}

type PublicCourtBooking struct {
	PropertyID  uuid.UUID       `json:"propertyId"`
	Guest       crm.PublicGuest `json:"guest"`
	CourtID     uuid.UUID       `json:"courtId"`
	Start       time.Time       `json:"start"`
	End         time.Time       `json:"end"`
	VoucherCode string          `json:"voucherCode,omitempty"`
	PayMethod   string          `json:"payMethod,omitempty" enum:"qris,virtual_account,ewallet,card"`
	Notes       string          `json:"notes,omitempty"`
	Lines       []CourtLine     `json:"lines,omitempty" doc:"Several courts / hours in one booking and one payment (the cart)"`
	Consent     bool            `json:"consent,omitempty" doc:"Marketing consent (UU PDP): ticked by the visitor, never pre-checked"`
	Terms       bool            `json:"terms,omitempty" doc:"Terms accepted (no cancellation, no refund)"`
}

func (b PublicCourtBooking) Property() uuid.UUID      { return b.PropertyID }
func (b PublicCourtBooking) Visitor() crm.PublicGuest { return b.Guest }

type PublicEnrollment struct {
	PropertyID uuid.UUID       `json:"propertyId"`
	Guest      crm.PublicGuest `json:"guest"`
	ProgramID  uuid.UUID       `json:"programId"`
	BirthDate  string          `json:"birthDate,omitempty" doc:"Participant birth date (age check)"`
	PayMethod  string          `json:"payMethod,omitempty" enum:"qris,virtual_account,card"`
}

func (b PublicEnrollment) Property() uuid.UUID      { return b.PropertyID }
func (b PublicEnrollment) Visitor() crm.PublicGuest { return b.Guest }

// PublicBooking is the Booking Confirmation of the website.
type PublicBooking struct {
	Reference   string            `json:"reference"`
	Status      string            `json:"status"`
	Checkout    *billing.Checkout `json:"checkout"`
	Token       string            `json:"token,omitempty" doc:"Court booking: the confirmation / payment page"`
	HoldSeconds int               `json:"holdSeconds,omitempty"`
}

func (m *Module) registerPublic(reg *route.Registry) {
	db := m.DB
	crm.PublicRoute(reg, "sportclub", route.Route{Method: http.MethodGet, Path: "/api/v1/public/sport-club", Summary: "Sport Club page: facilities, courts, classes",
		Response: PublicSportClub{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out PublicSportClub
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var err error
				// a sport shows when it is bookable online (FR-05), in the order of the back office (FR-07)
				if out.Facilities, err = handle.List[PublicFacility](tx.Query(ctx, `SELECT f.id, f.code, f.name, f.facility_type, f.usage_mode, f.opening_hours,
					f.sort_order, f.online_booking, f.content, f.booking_rules,
					(SELECT count(*) FROM sportclub.courts c WHERE c.facility_id = f.id AND c.status = 'active' AND c.archived_at IS NULL AND c.online_booking
					  AND c.resource_id IS NOT NULL)::int AS courts,
					coalesce((SELECT bool_or(c.indoor) FROM sportclub.courts c WHERE c.facility_id = f.id AND c.status = 'active' AND c.archived_at IS NULL), false) AS indoor,
					(SELECT trim_scale(min(r.price))::text FROM commercial.pricing_rules r WHERE r.property_id = f.property_id AND r.service_type = 'sport_court'
					  AND r.status = 'active' AND r.effective_from <= billing.local_date(r.property_id) AND (r.effective_to IS NULL OR r.effective_to >= billing.local_date(r.property_id)) AND r.min_quantity <= 1
					  AND r.item_ref IN (SELECT coalesce(nullif(c.price_item, ''), nullif(f.price_item, ''), f.code) FROM sportclub.courts c WHERE c.facility_id = f.id)) AS from_price
					FROM sportclub.facilities f WHERE f.property_id = $1 AND f.status = 'active' AND f.archived_at IS NULL
					AND (f.usage_mode <> 'slot_booking' OR f.online_booking) ORDER BY f.sort_order, f.name`, pid)); err != nil {
					return err
				}
				if out.Courts, err = handle.List[PublicCourt](tx.Query(ctx, `SELECT c.id, c.code, c.name, c.facility_id, c.surface, c.indoor, c.resource_id, c.sort_order,
					c.online_booking, c.photo_url, coalesce(nullif(c.price_item, ''), nullif(f.price_item, ''), f.code) AS price_item
					FROM sportclub.courts c JOIN sportclub.facilities f ON f.id = c.facility_id
					WHERE c.property_id = $1 AND c.status = 'active' AND c.archived_at IS NULL AND c.online_booking ORDER BY c.sort_order, c.name`, pid)); err != nil {
					return err
				}
				pol, err := m.courtPolicy(ctx, tx, pid)
				if err != nil {
					return err
				}
				out.WindowDays, out.HoldMinutes, out.TaxIncluded, out.Terms = pol.WindowDays, pol.HoldMinutes, pol.TaxIncluded, pol.Terms
				out.Methods = []OnlineMethod{}
				for _, o := range pol.Methods {
					if o.Active {
						out.Methods = append(out.Methods, o)
					}
				}
				out.Programs, err = handle.List[PublicProgram](tx.Query(ctx, `SELECT id, name, discipline, level, min_age, max_age, description
					FROM sportclub.class_programs WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	crm.PublicRoute(reg, "sportclub", route.Route{Method: http.MethodPost, Path: "/api/v1/public/court-bookings", Summary: "Book Sport Club court (non-member; online payment)",
		Request: PublicCourtBooking{}, Response: PublicBooking{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicCourtBooking) (PublicBooking, error) {
			// the booking window, opening hours, duration and online courts are checked by BookCourt (FR-11, FR-18, FR-35)
			if !in.Terms {
				return PublicBooking{}, handle.Invalid("terms", "required", "accept the terms: bookings cannot be cancelled and are not refunded")
			}
			if in.PayMethod == "" {
				return PublicBooking{}, handle.Invalid("payMethod", "required", "choose a payment method")
			}
			if in.Consent { // an unticked box never revokes an earlier consent
				yes := true
				if _, err := crm.SetConsent(ctx, tx, pid, c.ID, nil, &yes); err != nil {
					return PublicBooking{}, err
				}
			}
			lines := in.Lines
			if len(lines) == 0 {
				lines = []CourtLine{{CourtID: in.CourtID, Start: in.Start, End: in.End}}
			}
			res, err := m.BookCourt(ctx, tx, pid, CourtBookingInput{Lines: lines, CustomerID: &c.ID, Guest: &GuestInput{Name: in.Guest.Name, Phone: in.Guest.Phone,
				Email: in.Guest.Email}, Channel: "website", Hold: true, Notes: in.Notes, PromoCode: in.VoucherCode, PayMethod: in.PayMethod}, "")
			if err != nil {
				return PublicBooking{}, err
			}
			out := PublicBooking{Reference: res.Reservation.Code, Status: res.Reservation.Status, Checkout: res.Checkout}
			if res.Booking != nil {
				out.Token, out.HoldSeconds = res.Booking.Token, res.Booking.HoldSeconds
			}
			return out, nil
		})})
	crm.PublicRoute(reg, "sportclub", route.Route{Method: http.MethodPost, Path: "/api/v1/public/class-enrollments", Summary: "Register for a class (non-member)",
		Request: PublicEnrollment{}, Response: PublicBooking{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicEnrollment) (PublicBooking, error) {
			if in.BirthDate != "" {
				if err := crm.FillBirthDate(ctx, tx, c.ID, in.BirthDate); err != nil {
					return PublicBooking{}, err
				}
			}
			e, err := m.Enroll(ctx, tx, pid, EnrollInput{ProgramID: in.ProgramID, CustomerID: c.ID, Segment: "guest", Channel: "website"}, "")
			if err != nil {
				return PublicBooking{}, err
			}
			out := PublicBooking{Reference: e.ProgramName, Status: e.Status}
			if e.FolioID != nil {
				co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: *e.FolioID, Method: in.PayMethod, Description: "Class registration"})
				if err != nil {
					return out, err
				}
				out.Checkout = &co
			}
			return out, nil
		})})
}
