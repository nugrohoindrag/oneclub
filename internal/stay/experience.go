package stay

// Guest stay experience (requirements §26): My Stay shows the guest the
// bungalow and dates, check-in and check-out times, the reservation, guest
// services (requests and the add-ons that can be ordered in stay), the
// folio, the stay information, house rules, amenities and property
// information. Members open it in the Member App; guests without an account
// open it from the confirmation link (reservation reference + e-mail or
// phone). Access Control and Digital Key are out of scope.

import (
	"context"
	"net/http"
	"strings"
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

// GuestAddon is an add-on the guest can order during the stay.
type GuestAddon struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Category    string    `json:"category" db:"category"`
	Description *string   `json:"description" db:"description"`
	Price       string    `json:"price" db:"price"`
	Unit        string    `json:"unit" db:"unit"`
}

// FolioItem is a line of the guest folio.
type FolioItem struct {
	Description string    `json:"description"`
	Quantity    string    `json:"quantity"`
	Total       string    `json:"total"`
	PostedAt    time.Time `json:"postedAt"`
}

// StayExperience is My Stay.
type StayExperience struct {
	Stay            Stay           `json:"stay"`
	TypeName        string         `json:"typeName"`
	Description     *string        `json:"description"`
	Photos          []string       `json:"photos"`
	Amenities       []string       `json:"amenities"`
	BedConfig       *string        `json:"bedConfiguration"`
	View            *string        `json:"view"`
	CheckInTime     string         `json:"checkInTime"`
	CheckOutTime    string         `json:"checkOutTime"`
	HouseRules      string         `json:"houseRules"`
	PropertyInfo    string         `json:"propertyInfo"`
	Requests        []GuestRequest `json:"requests"`
	Addons          []StayAddon    `json:"addons"`
	OrderableAddons []GuestAddon   `json:"orderableAddons"`
	Folio           []FolioItem    `json:"folio"`
	FolioTotal      string         `json:"folioTotal"`
	FolioPaid       string         `json:"folioPaid"`
	FolioBalance    string         `json:"folioBalance"`
	CanRequest      bool           `json:"canRequest"`
	BookingToken    *string        `json:"bookingToken" doc:"Link of the whole booking (group or bungalow): status, e-voucher, payment, cancellation"`
	HouseRulesEn    string         `json:"houseRulesEn"`
	Contact         BookingContact `json:"contact"`
}

// Experience builds My Stay of a stay.
func (m *Module) Experience(ctx context.Context, tx pgx.Tx, s Stay) (StayExperience, error) {
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return StayExperience{}, err
	}
	out := StayExperience{Stay: s, CheckInTime: pol.CheckInTime, CheckOutTime: pol.CheckOutTime, HouseRules: pol.HouseRules, PropertyInfo: pol.PropertyInfo,
		Photos: []string{}, Amenities: []string{}, Folio: []FolioItem{}, CanRequest: s.Status == "checked_in" || s.BookingStatus == "confirmed"}
	out.HouseRulesEn, out.Contact, out.BookingToken = pol.HouseRulesEn, m.contactOf(ctx, tx, s.PropertyID, pol), s.PublicToken
	if s.GroupID != nil {
		var t *string
		if err := tx.QueryRow(ctx, `SELECT public_token FROM stay.stay_groups WHERE id = $1`, *s.GroupID).Scan(&t); err == nil && t != nil {
			out.BookingToken = t
		}
	}
	if s.UnitTypeID != nil {
		if err := tx.QueryRow(ctx, `SELECT name, description, photos, facilities, bed_configuration, view FROM stay.bungalow_types WHERE id = $1`, *s.UnitTypeID).
			Scan(&out.TypeName, &out.Description, &out.Photos, &out.Amenities, &out.BedConfig, &out.View); err != nil {
			return out, err
		}
	}
	if out.Requests, err = handle.List[GuestRequest](tx.Query(ctx, requestSelect+` WHERE g.stay_id = $1 ORDER BY g.created_at DESC`, s.ID)); err != nil {
		return out, err
	}
	if out.Addons, err = stayAddons(ctx, tx, s.ID); err != nil {
		return out, err
	}
	if out.OrderableAddons, err = handle.List[GuestAddon](tx.Query(ctx, `SELECT id, name, category, description, trim_scale(price)::text AS price, unit
		FROM stay.addons WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL AND availability IN ('in_stay', 'both') ORDER BY category, name`,
		s.PropertyID)); err != nil {
		return out, err
	}
	out.FolioTotal, out.FolioPaid, out.FolioBalance = s.TotalDue, s.Paid, decOf(s.TotalDue).Sub(decOf(s.Paid)).String()
	if s.FolioID != nil {
		f, err := billing.GetFolio(ctx, tx, *s.FolioID)
		if err != nil {
			return out, err
		}
		for _, l := range f.Lines {
			if l.VoidedAt != nil {
				continue
			}
			out.Folio = append(out.Folio, FolioItem{Description: l.Description, Quantity: l.Quantity, Total: l.Total, PostedAt: l.PostedAt})
		}
	}
	return out, nil
}

// MyRequestInput is a request from My Stay.
type MyRequestInput struct {
	RequestType string     `json:"requestType" enum:"extra_towel,extra_bed,room_cleaning,maintenance,laundry,transportation,food_beverage,other"`
	Description string     `json:"description,omitempty"`
	Quantity    int        `json:"quantity,omitempty"`
	AddonID     *uuid.UUID `json:"addonId,omitempty" doc:"Order a paid service (charged to the folio when delivered)"`
}

// PublicStayLookup opens My Stay without an account.
type PublicStayLookup struct {
	PropertyID uuid.UUID  `json:"propertyId"`
	Reference  string     `json:"reference,omitempty" doc:"Stay or reservation number"`
	Contact    string     `json:"contact,omitempty" doc:"E-mail or phone of the booking"`
	Token      string     `json:"token,omitempty" doc:"Link of the booking or of one bungalow instead of reference + contact"`
	StayID     *uuid.UUID `json:"stayId,omitempty" doc:"With a group link: which bungalow"`
}

// PublicStayRequest is a request from the public My Stay.
type PublicStayRequest struct {
	PublicStayLookup
	MyRequestInput
}

// publicStay finds a stay by its reference and the contact of the booking.
func (m *Module) publicStay(ctx context.Context, tx pgx.Tx, in PublicStayLookup) (Stay, error) {
	if strings.TrimSpace(in.Token) != "" {
		stays, _, err := m.staysOfToken(ctx, tx, in.PropertyID, in.Token)
		if err != nil {
			return Stay{}, err
		}
		for _, s := range stays {
			if in.StayID != nil && s.ID == *in.StayID {
				return s, nil
			}
		}
		// the bungalow in-house, else the next one, else the first
		for _, want := range []string{"in_house", "confirmed", "awaiting_payment"} {
			for _, s := range stays {
				if s.BookingStatus == want {
					return s, nil
				}
			}
		}
		return stays[0], nil
	}
	ref, contact := strings.TrimSpace(in.Reference), strings.ToLower(strings.TrimSpace(in.Contact))
	if ref == "" || contact == "" {
		return Stay{}, handle.Invalid("reference", "required", "reference and e-mail or phone are required")
	}
	digits := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	rows, err := tx.Query(ctx, staySelect+` LEFT JOIN crm.customers cu ON cu.id = s.customer_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND (upper(s.stay_no) = upper($2) OR upper(r.code) = upper($2))
		AND (lower(coalesce(s.guest_email, cu.email, '')) = $3 OR ($4 <> '' AND right(regexp_replace(coalesce(s.guest_phone, cu.phone, ''), '\D', '', 'g'), 8) = right($4, 8)))`,
		in.PropertyID, ref, contact, digits(contact))
	list, err := handle.List[Stay](rows, err)
	if err != nil {
		return Stay{}, err
	}
	if len(list) == 0 {
		return Stay{}, errs.NotFound("stay")
	}
	return list[0], nil
}

func (m *Module) registerExperience(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "stay", "Member Portal", rt) }
	myStay := func(ctx context.Context, tx pgx.Tx, r *http.Request) (Stay, error) {
		p, err := crm.Me(ctx, tx)
		if err != nil {
			return Stay{}, err
		}
		sid, err := handle.ID(r)
		if err != nil {
			return Stay{}, err
		}
		s, err := m.Get(ctx, tx, sid)
		if err != nil {
			return Stay{}, err
		}
		if s.CustomerID == nil || *s.CustomerID != p.ID {
			return Stay{}, errs.NotFound("stay")
		}
		return s, nil
	}
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/stays/{id}/experience", Summary: "My Stay: bungalow, times, folio, guest services, house rules",
		Response: StayExperience{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StayExperience, error) {
			s, err := myStay(ctx, tx, r)
			if err != nil {
				return StayExperience{}, err
			}
			return m.Experience(ctx, tx, s)
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stays/{id}/requests", Summary: "Ask for a guest service (towels, cleaning, laundry …)",
		Request: MyRequestInput{}, Response: GuestRequest{},
		Handler: handle.Write(db, http.StatusCreated, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyRequestInput) (GuestRequest, error) {
			s, err := myStay(ctx, tx, r)
			if err != nil {
				return GuestRequest{}, err
			}
			return m.createRequest(ctx, tx, GuestRequestInput{StayID: s.ID, RequestType: in.RequestType, Description: in.Description, Quantity: in.Quantity,
				AddonID: in.AddonID, Source: "member_app"})
		})})
	public := func(path, summary string, req any, res any, write bool) {
		noAudit := ""
		if !write {
			noAudit = "read-only lookup (POST keeps the guest contact out of the URL)"
		}
		crm.PublicRoute(reg, "stay", route.Route{Method: http.MethodPost, Path: path, Summary: summary, Request: req, Response: res, NoAudit: noAudit,
			Handler: func(w http.ResponseWriter, r *http.Request) {
				if !crm.PublicLimiter.Allow(r.RemoteAddr + r.URL.Path) {
					httpx.WriteError(w, r, errs.RateLimited())
					return
				}
				var lk PublicStayRequest
				if err := httpx.Decode(r, &lk); err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				if lk.PropertyID == uuid.Nil {
					httpx.WriteError(w, r, errs.BadRequest("property_required", "propertyId is required"))
					return
				}
				ctx := crm.PublicCtx(r.Context(), lk.PropertyID)
				var out any
				run := db.WithReadTx
				if write {
					run = db.WithTx
				}
				err := run(ctx, func(tx pgx.Tx) error {
					s, err := m.publicStay(ctx, tx, lk.PublicStayLookup)
					if err != nil {
						return err
					}
					if write {
						out, err = m.createRequest(ctx, tx, GuestRequestInput{StayID: s.ID, RequestType: lk.RequestType, Description: lk.Description,
							Quantity: lk.Quantity, AddonID: lk.AddonID, Source: "guest_app"})
						return err
					}
					out, err = m.Experience(ctx, tx, s)
					return err
				})
				if err != nil {
					httpx.WriteError(w, r, err)
					return
				}
				status := http.StatusOK
				if write {
					status = http.StatusCreated
				}
				httpx.JSON(w, status, out)
			}})
	}
	public("/api/v1/public/my-stay", "My Stay for a guest without an account (reference + e-mail or phone)", PublicStayLookup{}, StayExperience{}, false)
	public("/api/v1/public/my-stay/requests", "Guest service request from the public My Stay", PublicStayRequest{}, GuestRequest{}, true)
}
