package stay

// Member journey of the Member App, Resort branch: Book Bungalow (date →
// type → guests → review → payment → confirmation) and Book Meeting Room
// (date → time → room → capacity → purpose → confirmation). The catalog
// lists what can be booked, the meeting room day shows the busy hours and
// the quote prices a booking for the review step without keeping it.

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/crm"
	"oneclub/internal/kernel/httpx"
	"oneclub/internal/kernel/route"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// CatalogBungalowType is a bookable bungalow type.
type CatalogBungalowType struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Code        string    `json:"code" db:"code"`
	Name        string    `json:"name" db:"name"`
	MaxAdults   int       `json:"maxAdults" db:"max_adults"`
	MaxChildren int       `json:"maxChildren" db:"max_children"`
	Bedrooms    int       `json:"bedrooms" db:"bedrooms"`
	Facilities  []string  `json:"facilities" db:"facilities"`
	Description *string   `json:"description" db:"description"`
}

// CatalogRatePlan is a stay rate plan (Room Only, with breakfast…).
type CatalogRatePlan struct {
	Code              string  `json:"code" db:"code"`
	Name              string  `json:"name" db:"name"`
	Description       *string `json:"description" db:"description"`
	MinNights         int     `json:"minNights" db:"min_nights"`
	IncludesBreakfast bool    `json:"includesBreakfast" db:"includes_breakfast"`
}

// RoomLayout is the capacity of a meeting room in one layout.
type RoomLayout struct {
	Layout   string `json:"layout"`
	Capacity int    `json:"capacity"`
}

// CatalogMeetingRoom is a bookable meeting room.
type CatalogMeetingRoom struct {
	ID         uuid.UUID    `json:"id" db:"id"`
	Code       string       `json:"code" db:"code"`
	Name       string       `json:"name" db:"name"`
	SizeSqm    *string      `json:"sizeSqm" db:"size_sqm"`
	Facilities []string     `json:"facilities" db:"facilities"`
	Layouts    []RoomLayout `json:"layouts" db:"layouts"`
}

// StayCatalog is what a member can book in the resort.
type StayCatalog struct {
	BungalowTypes []CatalogBungalowType `json:"bungalowTypes"`
	RatePlans     []CatalogRatePlan     `json:"ratePlans"`
	MeetingRooms  []CatalogMeetingRoom  `json:"meetingRooms"`
}

// BusySlot is a booked period of a meeting room.
type BusySlot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// RoomDay is the day of one meeting room.
type RoomDay struct {
	RoomID uuid.UUID  `json:"roomId"`
	Busy   []BusySlot `json:"busy"`
}

// StayQuote prices a booking for the review step.
type StayQuote struct {
	Total           string      `json:"total"`
	DepositRequired string      `json:"depositRequired"`
	Lines           []QuoteLine `json:"lines"`
	UnitName        string      `json:"unitName"`
}

// QuoteLine is one priced line of a quote.
type QuoteLine struct {
	Description string `json:"description"`
	Total       string `json:"total"`
}

func (m *Module) registerMemberJourney(reg *route.Registry) {
	db := m.DB
	me := func(rt route.Route) { crm.MeRoute(reg, "stay", "Member Portal", rt) }
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/stay-catalog", Summary: "Bungalow types, rate plans and meeting rooms I can book",
		Response: StayCatalog{}, Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (StayCatalog, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return StayCatalog{}, err
			}
			var out StayCatalog
			if out.BungalowTypes, err = handle.List[CatalogBungalowType](tx.Query(ctx, `SELECT id, code, name, max_adults, max_children, bedrooms, facilities, description
				FROM stay.bungalow_types WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, p.PropertyID)); err != nil {
				return out, err
			}
			if out.RatePlans, err = handle.List[CatalogRatePlan](tx.Query(ctx, `SELECT code, name, description, min_nights, includes_breakfast FROM commercial.rate_plans
				WHERE property_id = $1 AND (service_type = 'bungalow' OR (business_line = 'stay' AND service_type IS NULL)) AND NOT day_use AND status = 'active'
				AND archived_at IS NULL AND effective_from <= billing.local_date($1) AND (effective_to IS NULL OR effective_to >= billing.local_date($1)) ORDER BY min_nights, name`,
				p.PropertyID)); err != nil {
				return out, err
			}
			out.MeetingRooms, err = handle.List[CatalogMeetingRoom](tx.Query(ctx, `SELECT r.id, r.code, r.name, trim_scale(r.size_sqm)::text AS size_sqm, r.facilities,
				coalesce((SELECT json_agg(json_build_object('layout', l.layout, 'capacity', l.capacity) ORDER BY l.capacity DESC)
				  FROM stay.room_layouts l WHERE l.meeting_room_id = r.id), '[]') AS layouts
				FROM stay.meeting_rooms r WHERE r.property_id = $1 AND r.status = 'active' AND r.archived_at IS NULL ORDER BY r.name`, p.PropertyID))
			return out, err
		})})
	me(route.Route{Method: http.MethodGet, Path: "/api/v1/member/meeting-room-availability", Summary: "Busy hours of each meeting room on a day",
		Response: RoomDay{}, List: true, Query: []route.Param{{Name: "date", Required: true, Description: "YYYY-MM-DD"}},
		Handler: handle.Read(db, func(ctx context.Context, tx pgx.Tx, r *http.Request) (httpx.Page[RoomDay], error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return httpx.Page[RoomDay]{}, err
			}
			day, err := handle.QueryDate(r, "date", time.Now())
			if err != nil {
				return httpx.Page[RoomDay]{}, err
			}
			rows, err := tx.Query(ctx, `SELECT r.id, s.start_at, s.end_at FROM stay.meeting_rooms r
				LEFT JOIN stay.stays s ON s.unit_id = r.id AND s.kind = 'meeting_room' AND s.status IN ('requested', 'reserved', 'checked_in')
				  AND tstzrange(s.start_at, s.end_at) && tstzrange($2::date::timestamp AT TIME ZONE $3, ($2::date + 1)::timestamp AT TIME ZONE $3)
				WHERE r.property_id = $1 AND r.status = 'active' AND r.archived_at IS NULL ORDER BY r.name, s.start_at`,
				p.PropertyID, day.Format("2006-01-02"), calendar.Location(ctx, tx).String())
			if err != nil {
				return httpx.Page[RoomDay]{}, err
			}
			defer rows.Close()
			out := []RoomDay{}
			for rows.Next() {
				var id uuid.UUID
				var start, end *time.Time
				if err := rows.Scan(&id, &start, &end); err != nil {
					return httpx.Page[RoomDay]{}, err
				}
				if len(out) == 0 || out[len(out)-1].RoomID != id {
					out = append(out, RoomDay{RoomID: id, Busy: []BusySlot{}})
				}
				if start != nil {
					out[len(out)-1].Busy = append(out[len(out)-1].Busy, BusySlot{Start: *start, End: *end})
				}
			}
			return httpx.Page[RoomDay]{Items: out}, rows.Err()
		})})
	me(route.Route{Method: http.MethodPost, Path: "/api/v1/member/stays:quote", Summary: "Price my bungalow / meeting room booking (review step, nothing is kept)",
		Request: MyStayInput{}, Response: StayQuote{}, NoAudit: "read-only preview, rolled back",
		Handler: handle.Write(db, http.StatusOK, func(ctx context.Context, tx pgx.Tx, r *http.Request, in MyStayInput) (StayQuote, error) {
			p, err := crm.Me(ctx, tx)
			if err != nil {
				return StayQuote{}, err
			}
			in.CustomerID, in.Guest, in.Channel, in.Segment, in.Payment = &p.ID, nil, "member_app", "", nil
			// book inside a savepoint and roll it back: the quote is the
			// exact price of the booking without keeping it
			sp, err := tx.Begin(ctx)
			if err != nil {
				return StayQuote{}, err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			res, err := m.Book(ctx, sp, p.PropertyID, in.StayInput, "")
			if err != nil {
				return StayQuote{}, err
			}
			out := StayQuote{Total: res.Total, DepositRequired: res.DepositRequired, UnitName: res.Stay.UnitName, Lines: []QuoteLine{}}
			if res.Folio != nil {
				for _, l := range res.Folio.Lines {
					out.Lines = append(out.Lines, QuoteLine{Description: l.Description, Total: l.Total})
				}
			}
			return out, nil
		})})
}
