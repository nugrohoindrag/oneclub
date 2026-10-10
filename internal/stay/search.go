package stay

// Search of the booking engine (docs/requirement-booking-hotel-mgcc.md
// §4.2, §4.3, §8): one card per room type — photos, size, bedrooms, beds,
// capacity, views, amenities by group and the bungalows left — with a row
// per rate plan the channel may sell (website: everyone + channel website;
// Member App: members too), each priced for the whole stay by the quote of
// the booking (FR-H17): total, average per night, strike price and saving
// of a promotion, breakfast, tax status, cancellation and payment policy,
// the nights. A full type stays on the list with "Penuh" (waitlist), a type
// too small for the guests says so (FR-H11), a closed date gives its reason
// (FR-H65). The rate card of the website sidebar comes from the same
// prices (FR-H01).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// Amenity is an amenity of a room type, by group (FR-H18).
type Amenity struct {
	Group   string `json:"group" doc:"bathroom, entertainment, internet, kitchen, general"`
	Icon    string `json:"icon"`
	Label   string `json:"label"`
	LabelEn string `json:"labelEn,omitempty"`
}

// FAQ is a question of a room type page.
type FAQ struct {
	Q   string `json:"q"`
	A   string `json:"a"`
	QEn string `json:"qEn,omitempty"`
	AEn string `json:"aEn,omitempty"`
}

// PublicRoomType is a room type on the website and in the Member App.
type PublicRoomType struct {
	ID               uuid.UUID `json:"id" db:"id"`
	Code             string    `json:"code" db:"code"`
	Name             string    `json:"name" db:"name"`
	NameEn           *string   `json:"nameEn" db:"name_en"`
	Slug             string    `json:"slug" db:"slug"`
	Description      *string   `json:"description" db:"description"`
	DescriptionEn    *string   `json:"descriptionEn" db:"description_en"`
	MaxAdults        int       `json:"maxAdults" db:"max_adults"`
	MaxChildren      int       `json:"maxChildren" db:"max_children"`
	Bedrooms         int       `json:"bedrooms" db:"bedrooms"`
	BedConfiguration *string   `json:"bedConfiguration" db:"bed_configuration"`
	SizeSqm          *string   `json:"sizeSqm" db:"size_sqm"`
	View             *string   `json:"view" db:"view"`
	Views            []string  `json:"views" db:"views" doc:"Views of the bungalows of the type (golf, lake, pool …): depends on availability when several"`
	Photos           []string  `json:"photos" db:"photos"`
	Facilities       []string  `json:"facilities" db:"facilities"`
	Amenities        []Amenity `json:"amenities" db:"amenities"`
	FAQ              []FAQ     `json:"faq" db:"faq"`
	HouseRules       *string   `json:"houseRules" db:"house_rules"`
	Units            int       `json:"units" db:"units"`
	SortOrder        int       `json:"sortOrder" db:"sort_order"`
}

const roomTypeSelect = `SELECT t.id, t.code, t.name, t.name_en, coalesce(nullif(t.slug, ''), lower(t.code)) AS slug, t.description, t.description_en,
	t.max_adults, t.max_children, t.bedrooms, t.bed_configuration, trim_scale(t.size_sqm)::text AS size_sqm, t.view,
	coalesce((SELECT array_agg(DISTINCT b.view) FROM stay.bungalows b WHERE b.type_id = t.id AND b.status = 'active' AND b.archived_at IS NULL
	  AND b.view IS NOT NULL), '{}') AS views, t.photos, t.facilities, t.amenities, t.faq, t.house_rules,
	(SELECT count(*) FROM stay.bungalows b WHERE b.type_id = t.id AND b.status = 'active' AND b.archived_at IS NULL)::int AS units, t.sort_order
	FROM stay.bungalow_types t`

// roomTypes lists the active room types (on the website when web is set).
func roomTypes(ctx context.Context, q dbtx.Querier, property uuid.UUID, web bool) ([]PublicRoomType, error) {
	rows, err := q.Query(ctx, roomTypeSelect+` WHERE t.property_id = $1 AND t.status = 'active' AND t.archived_at IS NULL AND (NOT $2 OR t.on_website)
		ORDER BY t.sort_order, t.name`, property, web)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PublicRoomType{}
	for rows.Next() {
		var t PublicRoomType
		var am, faq []byte
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.NameEn, &t.Slug, &t.Description, &t.DescriptionEn, &t.MaxAdults, &t.MaxChildren, &t.Bedrooms,
			&t.BedConfiguration, &t.SizeSqm, &t.View, &t.Views, &t.Photos, &t.Facilities, &am, &faq, &t.HouseRules, &t.Units, &t.SortOrder); err != nil {
			return nil, err
		}
		t.Amenities, t.FAQ = []Amenity{}, []FAQ{}
		_ = json.Unmarshal(am, &t.Amenities)
		_ = json.Unmarshal(faq, &t.FAQ)
		if t.Photos == nil {
			t.Photos = []string{}
		}
		if t.Facilities == nil {
			t.Facilities = []string{}
		}
		if t.Views == nil {
			t.Views = []string{}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PublicRate is a rate plan (or stay package) of a room type for a stay.
type PublicRate struct {
	Code              string       `json:"code"`
	Name              string       `json:"name"`
	Kind              string       `json:"kind" enum:"rate_plan,package"`
	Description       *string      `json:"description"`
	Eligibility       string       `json:"eligibility" enum:"everyone,member,corporate"`
	Total             string       `json:"total" doc:"The whole stay with tax and service, after the promotion"`
	AveragePerNight   string       `json:"averagePerNight"`
	ListTotal         string       `json:"listTotal" doc:"Before the promotion (strike-through)"`
	SavePercent       int          `json:"savePercent"`
	Discount          string       `json:"discount"`
	PromotionName     *string      `json:"promotionName"`
	IncludesBreakfast bool         `json:"includesBreakfast"`
	TaxIncluded       bool         `json:"taxIncluded" doc:"Prices include tax & service (nett); else tax & service are added"`
	PaymentPolicy     string       `json:"paymentPolicy" enum:"deposit,full_prepayment,pay_at_hotel"`
	DepositPercent    string       `json:"depositPercent"`
	FreeCancelHours   int          `json:"freeCancelHours"`
	CancelFeePercent  string       `json:"cancelFeePercent"`
	NoShowFeePercent  string       `json:"noShowFeePercent"`
	NonRefundable     bool         `json:"nonRefundable"`
	MinNights         int          `json:"minNights"`
	MaxNights         *int         `json:"maxNights"`
	Nights            []NightPrice `json:"nights" doc:"Price per night (weekend / season nights may differ)"`
	Inclusions        []AddonLine  `json:"inclusions"`
}

// PublicSearchType is a room type in the search results.
type PublicSearchType struct {
	PublicRoomType
	Available       int          `json:"available" doc:"Bungalows free for every night of the stay"`
	Full            bool         `json:"full"`
	LowAvailability bool         `json:"lowAvailability" doc:"Sisa ≤ 2 bungalow"`
	FitsGuests      bool         `json:"fitsGuests"`
	CapacityNote    *string      `json:"capacityNote"`
	ClosedReason    *string      `json:"closedReason" doc:"A restriction closes the dates for this channel"`
	FromPerNight    *string      `json:"fromPerNight" doc:"Lowest average price per night"`
	Rates           []PublicRate `json:"rates"`
	Packages        []PublicRate `json:"packages"`
}

// PublicSearch is the result of a search.
type PublicSearch struct {
	ArrivalDate   string             `json:"arrivalDate"`
	DepartureDate string             `json:"departureDate"`
	Nights        int                `json:"nights"`
	Adults        int                `json:"adults"`
	Children      int                `json:"children"`
	PromoCode     string             `json:"promoCode,omitempty"`
	PromoError    *string            `json:"promoError,omitempty" doc:"Why the promo code does not apply (expired, room type, minimum nights …)"`
	Types         []PublicSearchType `json:"types"`
	MinDate       string             `json:"minDate" doc:"Earliest check-in (today at the club)"`
	MaxDate       string             `json:"maxDate" doc:"Latest check-in (booking window)"`
	HoldMinutes   int                `json:"holdMinutes"`
	Methods       []string           `json:"methods"`
}

// channelSearch is a search of a channel.
type channelSearch struct {
	Arrival, Departure time.Time
	Adults, Children   int
	PromoCode          string
	Source             string // website | member_app
	Segment            string
	CustomerID         *uuid.UUID
	TypeID             *uuid.UUID
}

// channelPlans lists the rate plans a channel may sell: everyone (and
// member plans in the Member App for a member), the channel among the
// booking sources (empty = every channel).
func channelPlans(ctx context.Context, q dbtx.Querier, property uuid.UUID, source, segment string) ([]ratePlanRow, error) {
	rows, err := q.Query(ctx, `SELECT `+ratePlanCols+` FROM stay.rate_plans WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND (cardinality(booking_sources) = 0 OR $2 = ANY(booking_sources)) ORDER BY is_default DESC, sort_order, name`, property, source)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByName[ratePlanRow])
	if err != nil {
		return nil, err
	}
	out := []ratePlanRow{}
	for _, p := range all {
		if p.Eligibility == "everyone" || (p.Eligibility == "member" && source == "member_app" && isMemberSegment(segment)) {
			out = append(out, p)
		}
	}
	return out, nil
}

func publicRate(kind string, p *ratePlanRow, q *RoomQuote) PublicRate {
	r := PublicRate{Code: q.RatePlan, Name: q.RatePlanName, Kind: kind, Eligibility: "everyone", Total: q.Total, ListTotal: q.Total, Discount: q.Discount,
		PromotionName: q.PromotionName, IncludesBreakfast: q.IncludesBreakfast, TaxIncluded: q.PricingMode == "nett", PaymentPolicy: q.PaymentPolicy,
		FreeCancelHours: q.FreeCancelHours, CancelFeePercent: q.CancelFeePercent, NoShowFeePercent: q.NoShowFeePercent, NonRefundable: q.NonRefundable,
		Nights: q.Nights, Inclusions: q.Inclusions, MinNights: 1, DepositPercent: deref(q.DepositPercent)}
	if p != nil {
		r.Description, r.Eligibility, r.MinNights, r.MaxNights = nil, p.Eligibility, p.MinNights, p.MaxNights
	}
	total := decOf(q.Total)
	for _, inc := range q.Inclusions {
		if !inc.Included {
			total = total.Add(decOf(inc.Total))
		}
	}
	r.Total = total.String()
	r.AveragePerNight = total.Div(decimalN(len(q.Nights))).Round(0).String()
	if g, d := decOf(q.Gross), decOf(q.Discount); d.IsPositive() && g.GreaterThan(d) {
		list := total.Mul(g).Div(g.Sub(d)).Round(0)
		r.ListTotal = list.String()
		r.SavePercent = int(list.Sub(total).Mul(decimal.NewFromInt(100)).Div(list).Round(0).IntPart())
	}
	if r.Nights == nil {
		r.Nights = []NightPrice{}
	}
	if r.Inclusions == nil {
		r.Inclusions = []AddonLine{}
	}
	return r
}

// ChannelSearch searches the room types and prices of a channel.
func (m *Module) ChannelSearch(ctx context.Context, tx pgx.Tx, property uuid.UUID, in channelSearch) (PublicSearch, error) {
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return PublicSearch{}, err
	}
	loc := calendar.Location(ctx, tx)
	today := localToday(ctx, tx)
	out := PublicSearch{ArrivalDate: in.Arrival.Format(time.DateOnly), DepartureDate: in.Departure.Format(time.DateOnly), Adults: max(in.Adults, 1),
		Children: in.Children, PromoCode: strings.ToUpper(strings.TrimSpace(in.PromoCode)), Types: []PublicSearchType{}, MinDate: today.Format(time.DateOnly),
		HoldMinutes: pol.WebsiteHoldMinutes, Methods: methodsOf(pol)}
	out.MaxDate = today.AddDate(0, 0, max(pol.BookingWindowDays, 1)).Format(time.DateOnly)
	out.Nights = int(in.Departure.Sub(in.Arrival).Hours()/24 + 0.5)
	switch {
	case out.Nights < 1:
		return out, handle.Invalid("checkout", "invalid_period", "check-out harus setelah check-in")
	case out.Nights > 60:
		return out, handle.Invalid("checkout", "too_long", "paling lama 60 malam per pencarian")
	case in.Arrival.Before(today):
		return out, handle.Invalid("checkin", "in_the_past", "check-in paling cepat hari ini ("+today.Format("02 Jan 2006")+")")
	case pol.BookingWindowDays > 0 && in.Arrival.After(today.AddDate(0, 0, pol.BookingWindowDays)):
		return out, handle.Invalid("checkin", "beyond_window", fmt.Sprintf("booking paling jauh %d hari ke depan (sampai %s)", pol.BookingWindowDays,
			today.AddDate(0, 0, pol.BookingWindowDays).Format("02 Jan 2006")))
	}
	if in.Segment == "" {
		in.Segment = "guest"
		if in.CustomerID != nil {
			if s, err := memberSegment(ctx, tx, property, *in.CustomerID); err == nil && s != "" {
				in.Segment = s
			}
		}
	}
	types, err := roomTypes(ctx, tx, property, in.Source == "website")
	if err != nil {
		return out, err
	}
	plans, err := channelPlans(ctx, tx, property, in.Source, in.Segment)
	if err != nil {
		return out, err
	}
	pkgs, err := handle.List[struct {
		Code string `db:"code"`
	}](tx.Query(ctx, `SELECT code FROM stay.packages WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL ORDER BY name`, property))
	if err != nil {
		return out, err
	}
	rlist, err := restrictions(ctx, tx, property, nil, in.Arrival, in.Departure)
	if err != nil {
		return out, err
	}
	start, end := clockOn(in.Arrival, pol.CheckInTime, loc), clockOn(in.Departure, pol.CheckOutTime, loc)
	promo := out.PromoCode
	quote := func(req roomRequest) (*RoomQuote, error) {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = sp.Rollback(ctx) }()
		q, err := m.quoteRoom(ctx, sp, property, req)
		if de, ok := errs.As(err); ok && req.PromoCode != "" && (de.Code == "invalid_promo_code" || de.Code == "promo_not_applicable") {
			if out.PromoError == nil {
				msg := de.Message
				out.PromoError = &msg
			}
			req.PromoCode = ""
			return m.quoteRoom(ctx, sp, property, req)
		}
		return q, err
	}
	for _, t := range types {
		if in.TypeID != nil && *in.TypeID != t.ID {
			continue
		}
		st := PublicSearchType{PublicRoomType: t, Rates: []PublicRate{}, Packages: []PublicRate{}}
		rows, err := tx.Query(ctx, `SELECT resource_id FROM stay.bungalows WHERE type_id = $1 AND status = 'active' AND archived_at IS NULL
			AND resource_id IS NOT NULL`, t.ID)
		if err != nil {
			return out, err
		}
		resources, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return out, err
		}
		for _, rid := range resources {
			busy, err := m.Res.BusyResources(ctx, tx, []uuid.UUID{rid}, start, end)
			if err != nil {
				return out, err
			}
			if busy == 0 {
				st.Available++
			}
		}
		st.Full = st.Available == 0
		st.LowAvailability = st.Available > 0 && st.Available <= 2
		st.FitsGuests = out.Adults <= t.MaxAdults && in.Children <= t.MaxChildren
		if !st.FitsGuests {
			note := fmt.Sprintf("Maksimal %d dewasa dan %d anak per bungalow — tambah bungalow atau pilih tipe lain", t.MaxAdults, t.MaxChildren)
			st.CapacityNote = &note
		}
		if code, msg := restrictionError(rlist, t.ID, in.Arrival, in.Departure, in.Source, "id"); code != "" {
			st.ClosedReason = &msg
		}
		adults := min(out.Adults, t.MaxAdults)
		children := min(in.Children, t.MaxChildren)
		req := roomRequest{TypeID: t.ID, Arrival: in.Arrival, Departure: in.Departure, Adults: max(adults, 1), Children: children, Segment: in.Segment,
			Source: in.Source, PromoCode: promo, BookedAt: clock.Now(), Explicit: true}
		for i := range plans {
			p := &plans[i]
			r := req
			r.RatePlan = p.Code
			q, err := quote(r)
			if err != nil {
				if _, ok := errs.As(err); ok {
					continue // not for this stay (minimum nights, validity, no price …)
				}
				return out, err
			}
			if q != nil {
				st.Rates = append(st.Rates, publicRate("rate_plan", p, q))
			}
		}
		for _, pk := range pkgs {
			r := req
			r.PackageCode = pk.Code
			q, err := quote(r)
			if err != nil {
				if _, ok := errs.As(err); ok {
					continue
				}
				return out, err
			}
			if q != nil {
				st.Packages = append(st.Packages, publicRate("package", nil, q))
			}
		}
		for _, r := range append(append([]PublicRate{}, st.Rates...), st.Packages...) {
			if st.FromPerNight == nil || decOf(r.AveragePerNight).LessThan(decOf(*st.FromPerNight)) {
				v := r.AveragePerNight
				st.FromPerNight = &v
			}
		}
		out.Types = append(out.Types, st)
	}
	return out, nil
}

// RateCardRow is a line of the rate table of the website sidebar: the
// price of a room type under a rate plan, from the Stay prices (FR-H01).
type RateCardRow struct {
	TypeID            uuid.UUID `json:"typeId"`
	TypeName          string    `json:"typeName"`
	TypeNameEn        *string   `json:"typeNameEn"`
	Slug              string    `json:"slug"`
	RatePlan          string    `json:"ratePlan"`
	RatePlanName      string    `json:"ratePlanName"`
	MinNights         int       `json:"minNights"`
	IncludesBreakfast bool      `json:"includesBreakfast"`
	TaxIncluded       bool      `json:"taxIncluded"`
	Weekday           string    `json:"weekday" doc:"Price per night on a weekday night"`
	Weekend           string    `json:"weekend" doc:"Price per night on a weekend night (Stay Policies weekend nights)"`
	SortOrder         int       `json:"sortOrder"`
}

// RateCard prices one weekday and one weekend night of every room type
// under every public website rate plan with the quote of the booking, so
// the sidebar table is the price of the search and of the bill.
func (m *Module) RateCard(ctx context.Context, tx pgx.Tx, property uuid.UUID) ([]RateCardRow, error) {
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return nil, err
	}
	types, err := roomTypes(ctx, tx, property, true)
	if err != nil {
		return nil, err
	}
	plans, err := channelPlans(ctx, tx, property, "website", "guest")
	if err != nil {
		return nil, err
	}
	loc := calendar.Location(ctx, tx)
	wk := weekendNights(pol.WeekendNights)
	from := localToday(ctx, tx).AddDate(0, 0, 1)
	night := func(weekend bool) time.Time {
		for i := 0; i < 14; i++ {
			d := from.AddDate(0, 0, i)
			if slices.Contains(wk, d.Weekday()) == weekend {
				return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			}
		}
		return from
	}
	out := []RateCardRow{}
	for _, t := range types {
		for i := range plans {
			p := &plans[i]
			row := RateCardRow{TypeID: t.ID, TypeName: t.Name, TypeNameEn: t.NameEn, Slug: t.Slug, RatePlan: p.Code, RatePlanName: p.Name, MinNights: p.MinNights,
				IncludesBreakfast: p.IncludesBreakfast, TaxIncluded: p.PricingMode == "nett", SortOrder: t.SortOrder}
			ok := true
			for _, weekend := range []bool{false, true} {
				a := night(weekend)
				n := max(p.MinNights, 1)
				sp, err := tx.Begin(ctx)
				if err != nil {
					return nil, err
				}
				q, err := m.quoteRoom(ctx, sp, property, roomRequest{TypeID: t.ID, RatePlan: p.Code, Arrival: a, Departure: a.AddDate(0, 0, n), Adults: 1,
					Segment: "guest", Source: "website", BookedAt: clock.Now(), Explicit: true})
				_ = sp.Rollback(ctx)
				if err != nil || q == nil || len(q.Nights) == 0 {
					if _, isDomain := errs.As(err); err != nil && !isDomain {
						return nil, err
					}
					ok = false
					break
				}
				// the first night is the night of the kind asked for (the plan price, before tax under ++)
				price := q.Nights[0].Price
				if weekend {
					row.Weekend = price
				} else {
					row.Weekday = price
				}
			}
			if ok {
				out = append(out, row)
			}
		}
	}
	return out, nil
}
