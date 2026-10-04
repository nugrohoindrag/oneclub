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
	ID           uuid.UUID      `json:"id" db:"id"`
	Name         string         `json:"name" db:"name"`
	FacilityType *string        `json:"facilityType" db:"facility_type"`
	UsageMode    string         `json:"usageMode" db:"usage_mode"`
	OpeningHours map[string]any `json:"openingHours" db:"opening_hours"`
}

type PublicCourt struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Name       string     `json:"name" db:"name"`
	FacilityID uuid.UUID  `json:"facilityId" db:"facility_id"`
	Surface    *string    `json:"surface" db:"surface"`
	Indoor     bool       `json:"indoor" db:"indoor"`
	ResourceID *uuid.UUID `json:"resourceId" db:"resource_id"`
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
	Facilities []PublicFacility `json:"facilities"`
	Courts     []PublicCourt    `json:"courts"`
	Programs   []PublicProgram  `json:"classPrograms"`
}

type PublicCourtBooking struct {
	PropertyID  uuid.UUID       `json:"propertyId"`
	Guest       crm.PublicGuest `json:"guest"`
	CourtID     uuid.UUID       `json:"courtId"`
	Start       time.Time       `json:"start"`
	End         time.Time       `json:"end"`
	VoucherCode string          `json:"voucherCode,omitempty"`
	PayMethod   string          `json:"payMethod,omitempty" enum:"qris,virtual_account,card"`
	Notes       string          `json:"notes,omitempty"`
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
	Reference string            `json:"reference"`
	Status    string            `json:"status"`
	Checkout  *billing.Checkout `json:"checkout"`
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
				if out.Facilities, err = handle.List[PublicFacility](tx.Query(ctx, `SELECT id, name, facility_type, usage_mode, opening_hours FROM sportclub.facilities
					WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid)); err != nil {
					return err
				}
				if out.Courts, err = handle.List[PublicCourt](tx.Query(ctx, `SELECT id, name, facility_id, surface, indoor, resource_id FROM sportclub.courts
					WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid)); err != nil {
					return err
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
			res, err := m.BookCourt(ctx, tx, pid, CourtBookingInput{CourtID: in.CourtID, Start: in.Start, End: in.End, CustomerID: &c.ID, Channel: "website",
				Hold: true, Notes: in.Notes}, "")
			if err != nil {
				return PublicBooking{}, err
			}
			out := PublicBooking{Reference: res.Reservation.Code, Status: res.Reservation.Status}
			if res.Folio != nil {
				co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: res.Folio.ID, VoucherCode: in.VoucherCode, Method: in.PayMethod,
					Description: "Court booking " + res.Reservation.Code})
				if err != nil {
					return out, err
				}
				out.Checkout = &co
				if co.AmountDue == "0" {
					r2, err := m.Res.Confirm(ctx, tx, res.Reservation.ID, false)
					if err != nil {
						return out, err
					}
					out.Status = r2.Status
				}
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
