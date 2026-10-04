package stay

// Website Bungalow, VIP Suite and Meeting & MICE (PRD P2 EP-26): page data,
// availability and non-member booking with additional services, deposit /
// prepayment online and voucher code. VIP Suite is a request confirmed by
// staff (FR-WEB-P2-06).

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/handle"
)

type PublicUnit struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	Facilities  []string  `json:"facilities" db:"facilities"`
	Description *string   `json:"description" db:"description"`
	MaxAdults   *int      `json:"maxAdults" db:"max_adults"`
	Bedrooms    *int      `json:"bedrooms" db:"bedrooms"`
	SizeSqm     *string   `json:"sizeSqm" db:"size_sqm"`
	BlockHours  *int      `json:"blockHours" db:"block_hours"`
}

// PublicStay is the Bungalow / VIP Suite / Meeting & MICE page data.
type PublicStay struct {
	BungalowTypes []PublicUnit `json:"bungalowTypes"`
	VIPSuites     []PublicUnit `json:"vipSuites"`
	MeetingRooms  []PublicUnit `json:"meetingRooms"`
}

// PublicStayInput is a website booking.
type PublicStayInput struct {
	PropertyID uuid.UUID       `json:"propertyId"`
	Guest      crm.PublicGuest `json:"guest"`
	StayInput
	VoucherCode string `json:"voucherCode,omitempty"`
	PayMethod   string `json:"payMethod,omitempty" enum:"qris,virtual_account,card" doc:"Pays the deposit (or the total when no deposit applies)"`
}

func (p PublicStayInput) Property() uuid.UUID      { return p.PropertyID }
func (p PublicStayInput) Visitor() crm.PublicGuest { return p.Guest }

// PublicStayResult is the Booking Confirmation.
type PublicStayResult struct {
	Reference string            `json:"reference"`
	StayNo    string            `json:"stayNo"`
	Status    string            `json:"status"`
	Total     string            `json:"total"`
	Deposit   string            `json:"depositRequired"`
	Checkout  *billing.Checkout `json:"checkout"`
}

func (m *Module) registerPublic(reg *route.Registry) {
	db := m.DB
	crm.PublicRoute(reg, "stay", route.Route{Method: http.MethodGet, Path: "/api/v1/public/stay", Summary: "Bungalow, VIP Suite and Meeting & MICE pages",
		Response: PublicStay{}, Query: []route.Param{{Name: "propertyId", Required: true}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out PublicStay
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				var err error
				if out.BungalowTypes, err = handle.List[PublicUnit](tx.Query(ctx, `SELECT id, code, name, facilities, description, max_adults, bedrooms
					FROM stay.bungalow_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid)); err != nil {
					return err
				}
				if out.VIPSuites, err = handle.List[PublicUnit](tx.Query(ctx, `SELECT id, code, name, facilities, block_hours FROM stay.vip_suites
					WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid)); err != nil {
					return err
				}
				out.MeetingRooms, err = handle.List[PublicUnit](tx.Query(ctx, `SELECT id, code, name, facilities, trim_scale(size_sqm)::text AS size_sqm
					FROM stay.meeting_rooms WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, pid))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
		}})
	crm.PublicRoute(reg, "stay", route.Route{Method: http.MethodGet, Path: "/api/v1/public/bungalow-availability", Summary: "Bungalow availability per type (website)",
		Response: TypeAvailability{}, List: true, Query: []route.Param{{Name: "propertyId", Required: true}, {Name: "from"}, {Name: "nights", Type: "integer"}},
		Handler: func(w http.ResponseWriter, r *http.Request) {
			pid, err := uuid.Parse(r.URL.Query().Get("propertyId"))
			if err != nil {
				httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
				return
			}
			ctx := crm.PublicCtx(r.Context(), pid)
			var out []TypeAvailability
			err = db.WithReadTx(ctx, func(tx pgx.Tx) error {
				from, err := handle.QueryDate(r, "from", time.Now())
				if err != nil {
					return err
				}
				out, err = m.BungalowAvailability(ctx, tx, pid, from, min(max(handle.QueryInt(r, "nights", 1), 1), 30))
				return err
			})
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, httpx.Page[TypeAvailability]{Items: out})
		}})
	crm.PublicRoute(reg, "stay", route.Route{Method: http.MethodPost, Path: "/api/v1/public/stays",
		Summary: "Book Bungalow / Meeting Room, or request a VIP Suite (non-member; additional services; online deposit)",
		Request: PublicStayInput{}, Response: PublicStayResult{},
		Handler: crm.PublicWrite(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, pid uuid.UUID, c crm.Customer, in PublicStayInput) (PublicStayResult, error) {
			s := in.StayInput
			s.CustomerID, s.Guest, s.Channel, s.Payment, s.Segment = &c.ID, nil, "website", nil, ""
			if s.Kind == "vip_suite" {
				s.Request = true
			}
			res, err := m.Book(ctx, tx, pid, s, "")
			if err != nil {
				return PublicStayResult{}, err
			}
			out := PublicStayResult{Reference: res.Reservation.Code, StayNo: res.Stay.StayNo, Status: res.Stay.Status, Total: res.Total, Deposit: res.DepositRequired}
			if res.Folio != nil && s.Kind != "vip_suite" {
				dep, _ := decimal.NewFromString(res.DepositRequired)
				co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: res.Folio.Folio.ID, VoucherCode: in.VoucherCode, Method: in.PayMethod,
					Amount: dep, Description: "Booking " + res.Reservation.Code})
				if err != nil {
					return out, err
				}
				out.Checkout = &co
			}
			return out, nil
		})})
}
