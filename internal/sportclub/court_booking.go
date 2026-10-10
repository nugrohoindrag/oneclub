package sportclub

// Court booking of the Sport Club (docs/requirement-booking-sportclub-mgcc.md):
//
//   - the lines of a booking (court × hours, several courts and hours in one
//     payment) are checked against the courts, opening hours, the booking
//     window and the duration rules, consecutive hours of a court become one
//     line and a line never crosses midnight (FR-18, FR-131);
//   - one quote function prices the slot grid, the cart, the checkout, the
//     front desk, the Member App, overtime and moves (FR-122): the brochure
//     price before tax, the promotion, the tax on top and the service fee of
//     the online payment method (FR-30, FR-32, FR-123);
//   - the booking view gives every screen the same status per line and the
//     same payment status from the folio (FR-124, §13.3).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/realtime"
	"oneclub/internal/reservation"
)

// CourtInfo is a court with its sport.
type CourtInfo struct {
	ID             uuid.UUID     `json:"id" db:"id"`
	Code           string        `json:"code" db:"code"`
	Name           string        `json:"name" db:"name"`
	FacilityID     uuid.UUID     `json:"facilityId" db:"facility_id"`
	FacilityCode   string        `json:"facilityCode" db:"facility_code"`
	FacilityName   string        `json:"facilityName" db:"facility_name"`
	FacilityType   *string       `json:"facilityType" db:"facility_type"`
	Surface        *string       `json:"surface" db:"surface"`
	Indoor         bool          `json:"indoor" db:"indoor"`
	ResourceID     *uuid.UUID    `json:"resourceId" db:"resource_id"`
	PriceItem      string        `json:"priceItem" db:"price_item"`
	Online         bool          `json:"onlineBooking" db:"online_booking"`
	FacilityOnline bool          `json:"facilityOnline" db:"facility_online"`
	Status         string        `json:"status" db:"status"`
	FacilityStatus string        `json:"facilityStatus" db:"facility_status"`
	SortOrder      int           `json:"sortOrder" db:"sort_order"`
	FacilitySort   int           `json:"facilitySort" db:"facility_sort"`
	PhotoURL       *string       `json:"photoUrl" db:"photo_url"`
	Rules          FacilityRules `json:"rules" db:"booking_rules"`
}

const courtSelect = `SELECT c.id, c.code, c.name, c.facility_id, f.code AS facility_code, f.name AS facility_name, f.facility_type, c.surface, c.indoor,
	c.resource_id, coalesce(nullif(c.price_item, ''), nullif(f.price_item, ''), f.code) AS price_item, c.online_booking, f.online_booking AS facility_online,
	c.status, f.status AS facility_status, c.sort_order, f.sort_order AS facility_sort, c.photo_url, f.booking_rules
	FROM sportclub.courts c JOIN sportclub.facilities f ON f.id = c.facility_id`

func (m *Module) courts(ctx context.Context, q dbtx.Querier, property uuid.UUID, where string, args ...any) ([]CourtInfo, error) {
	return handle.List[CourtInfo](q.Query(ctx, courtSelect+` WHERE c.property_id = $1 AND c.archived_at IS NULL AND f.archived_at IS NULL`+where+
		` ORDER BY f.sort_order, f.name, c.sort_order, c.name`, append([]any{property}, args...)...))
}

func (m *Module) court(ctx context.Context, q dbtx.Querier, property, courtID uuid.UUID) (CourtInfo, error) {
	list, err := m.courts(ctx, q, property, ` AND c.id = $2`, courtID)
	if err != nil {
		return CourtInfo{}, err
	}
	if len(list) == 0 {
		return CourtInfo{}, errNotFound("court")
	}
	return list[0], nil
}

func (c CourtInfo) minHours(p CourtPolicy) int {
	if c.Rules.MinHours > 0 {
		return c.Rules.MinHours
	}
	return p.MinHours
}

func (c CourtInfo) maxHours(p CourtPolicy) int {
	if c.Rules.MaxHours > 0 {
		return c.Rules.MaxHours
	}
	return p.MaxHours
}

func (c CourtInfo) costCenter() string { return "sportclub:" + c.FacilityCode }

// courtSlot is one normalised line: a court and consecutive hours of one day.
type courtSlot struct {
	Court CourtInfo
	Start time.Time
	End   time.Time
}

func (s courtSlot) hours() int { return int(s.End.Sub(s.Start) / time.Hour) }

// slotOptions select the checks of normalizeLines.
type slotOptions struct {
	Online    bool // website / Member App: online courts only, the booking window of the website
	SkipHours bool // overtime: the rule of the minimum hours does not apply
	SkipPast  bool // recurring preview of dates
}

// normalizeLines checks the requested lines and returns them per court and
// consecutive hours of one local day.
func (m *Module) normalizeLines(ctx context.Context, q dbtx.Querier, property uuid.UUID, lines []CourtLine, pol CourtPolicy, opt slotOptions) ([]courtSlot, error) {
	if len(lines) == 0 {
		return nil, handle.Invalid("lines", "required", "choose at least one court and hour")
	}
	if len(lines) > 48 {
		return nil, errs.Validation("too_many_lines", "at most 48 hours in one booking")
	}
	loc := calendar.Location(ctx, q)
	cache := map[uuid.UUID]CourtInfo{}
	type piece struct {
		court      CourtInfo
		start, end time.Time
	}
	var pieces []piece
	seen := map[string]bool{}
	for i, l := range lines {
		c, ok := cache[l.CourtID]
		if !ok {
			var err error
			if c, err = m.court(ctx, q, property, l.CourtID); err != nil {
				return nil, err
			}
			cache[l.CourtID] = c
		}
		switch {
		case c.Status != "active" || c.FacilityStatus != "active":
			return nil, errs.Conflict("court_unavailable", c.Name+" is not available")
		case c.ResourceID == nil:
			return nil, errs.Conflict("court_not_bookable", c.Name+" has no bookable resource")
		case opt.Online && (!c.Online || !c.FacilityOnline):
			return nil, errs.Conflict("court_not_online", c.Name+" cannot be booked online; please contact the front desk")
		}
		s, e := l.Start.In(loc), l.End.In(loc)
		if !e.After(s) {
			return nil, errs.Validation("invalid_period", fmt.Sprintf("line %d: the end must be after the start", i+1))
		}
		if s.Minute() != 0 || s.Second() != 0 || e.Sub(s)%time.Hour != 0 {
			return nil, errs.Validation("invalid_slot", fmt.Sprintf("line %d: courts are booked per full hour (60 minutes)", i+1))
		}
		for t := s; t.Before(e); t = t.Add(time.Hour) {
			k := c.ID.String() + t.UTC().Format(time.RFC3339)
			if seen[k] {
				return nil, errs.Validation("duplicate_slot", fmt.Sprintf("%s %s is chosen twice", c.Name, t.Format("2 Jan 15:04")))
			}
			seen[k] = true
			pieces = append(pieces, piece{c, t, t.Add(time.Hour)})
		}
	}
	sort.Slice(pieces, func(i, j int) bool {
		if pieces[i].court.ID != pieces[j].court.ID {
			return pieces[i].court.Name < pieces[j].court.Name
		}
		return pieces[i].start.Before(pieces[j].start)
	})
	// opening hours and closed days per piece
	resCache := map[uuid.UUID]reservation.Resource{}
	rt, err := m.Res.Type(ctx, q, "sport_court")
	if err != nil {
		return nil, err
	}
	for _, p := range pieces {
		res, ok := resCache[*p.court.ResourceID]
		if !ok {
			if res, err = m.Res.Resource(ctx, q, *p.court.ResourceID); err != nil {
				return nil, err
			}
			resCache[res.ID] = res
		}
		day := time.Date(p.start.Year(), p.start.Month(), p.start.Day(), 0, 0, 0, 0, loc)
		open, close, closed, err := m.Res.OpenHours(ctx, q, property, res, rt, day)
		if err != nil {
			return nil, err
		}
		if closed || p.start.Before(open) || p.end.After(close) {
			return nil, errs.Conflict("court_closed", fmt.Sprintf("%s is closed on %s at %s", p.court.Name, p.start.Format("Mon 2 Jan"), p.start.Format("15:04")))
		}
	}
	// merge consecutive hours of a court on the same local day
	var out []courtSlot
	for _, p := range pieces {
		if n := len(out); n > 0 && out[n-1].Court.ID == p.court.ID && out[n-1].End.Equal(p.start) && sameDay(out[n-1].Start, p.start) {
			out[n-1].End = p.end
			continue
		}
		out = append(out, courtSlot{Court: p.court, Start: p.start, End: p.end})
	}
	now := clock.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	window := pol.DeskWindowDays
	if opt.Online {
		window = pol.WindowDays
	}
	last := today.AddDate(0, 0, window+1)
	for _, s := range out {
		h := s.hours()
		switch {
		case !opt.SkipPast && !s.End.After(now):
			return nil, errs.Validation("slot_past", fmt.Sprintf("%s %s has already passed", s.Court.Name, s.Start.Format("2 Jan 15:04")))
		case !s.Start.Before(last):
			return nil, errs.Validation("outside_booking_window", fmt.Sprintf("courts can be booked up to %d days ahead", window))
		case !opt.SkipHours && h < s.Court.minHours(pol):
			return nil, errs.Validation("too_short", fmt.Sprintf("%s: book at least %d hour(s)", s.Court.Name, s.Court.minHours(pol)))
		case h > s.Court.maxHours(pol):
			return nil, errs.Validation("too_long", fmt.Sprintf("%s: at most %d consecutive hours in one booking", s.Court.Name, s.Court.maxHours(pol)))
		}
	}
	return out, nil
}

// takenSlots reports the requested hours already held or booked by someone
// else (expired holds are released first): the checkout marks them (FR-23,
// FR-35).
func (m *Module) takenSlots(ctx context.Context, tx pgx.Tx, slots []courtSlot) ([]errs.FieldError, error) {
	var ids []uuid.UUID
	for _, s := range slots {
		ids = append(ids, *s.Court.ResourceID)
	}
	if err := reservation.ReleaseExpiredOn(ctx, tx, ids); err != nil {
		return nil, err
	}
	var out []errs.FieldError
	for _, s := range slots {
		for t := s.Start; t.Before(s.End); t = t.Add(time.Hour) {
			busy, err := reservation.Busy(ctx, tx, []uuid.UUID{*s.Court.ResourceID}, t, t.Add(time.Hour))
			if err != nil {
				return nil, err
			}
			if busy[*s.Court.ResourceID] {
				out = append(out, errs.Field("lines."+s.Court.ID.String()+"."+t.UTC().Format(time.RFC3339), "slot_taken",
					s.Court.Name+" "+t.In(s.Start.Location()).Format("Mon 2 Jan 15:04")+" is no longer available"))
			}
		}
	}
	return out, nil
}

// ── quote (FR-122) ──────────────────────────────────────────────────────────

// QuoteLine is the price of one court line.
type QuoteLine struct {
	CourtID      uuid.UUID `json:"courtId"`
	CourtName    string    `json:"courtName"`
	FacilityName string    `json:"facilityName"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Hours        int       `json:"hours"`
	ListPrice    string    `json:"listPrice" doc:"Brochure price of the line before promotion and tax"`
	Discount     string    `json:"discount"`
	Net          string    `json:"net" doc:"After promotion, before tax"`
	Tax          string    `json:"tax"`
	Total        string    `json:"total"`
	DayType      *string   `json:"dayType"`
	TimeBand     *string   `json:"timeBand"`
	Promotions   []string  `json:"promotions"`
}

// MethodQuote is the total with the service fee of an online method.
type MethodQuote struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Fee      string `json:"fee"`
	Total    string `json:"total"`
	Cheapest bool   `json:"cheapest"`
}

// Quote is the price of a cart.
type Quote struct {
	Currency     string        `json:"currency"`
	Lines        []QuoteLine   `json:"lines"`
	Rent         string        `json:"rent" doc:"Sum of the brochure prices (before tax)"`
	Discount     string        `json:"discount"`
	Net          string        `json:"net"`
	Tax          string        `json:"tax"`
	ServiceFee   string        `json:"serviceFee" doc:"Service fee of the chosen online method (online payments only)"`
	Total        string        `json:"total"`
	Method       string        `json:"method,omitempty"`
	PromoCode    string        `json:"promoCode,omitempty"`
	PromoApplied bool          `json:"promoApplied"`
	PromoError   string        `json:"promoError,omitempty"`
	Voucher      bool          `json:"voucher" doc:"The code is a gift / value voucher: its balance pays the booking at checkout"`
	Methods      []MethodQuote `json:"methods"`
	TaxIncluded  bool          `json:"taxIncluded"`
}

type quoteRequest struct {
	Slots    []courtSlot
	Promo    string
	Customer *uuid.UUID
	Channel  string
	Method   string // online method code: adds its service fee
	Online   bool
}

func (m *Module) priceRequest(s courtSlot, r quoteRequest) commercial.PriceRequest {
	end := s.End
	pr := commercial.PriceRequest{ServiceType: "sport_court", ItemRef: s.Court.PriceItem, Start: s.Start, End: &end, Channel: r.Channel, CustomerID: r.Customer}
	if r.Promo != "" {
		pr.PromoCodes = []string{strings.ToUpper(strings.TrimSpace(r.Promo))}
	}
	return pr
}

// quote prices court lines: the same prices as the slot grid and the
// charges of the booking.
func (m *Module) quote(ctx context.Context, q dbtx.Querier, property uuid.UUID, r quoteRequest, pol CourtPolicy) (Quote, error) {
	out := Quote{Lines: []QuoteLine{}, Methods: []MethodQuote{}, PromoCode: strings.ToUpper(strings.TrimSpace(r.Promo)), TaxIncluded: pol.TaxIncluded}
	rent, disc, net, tax, total := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	var promoLines []commercial.PromoLine
	for i, s := range r.Slots {
		lp, err := commercial.Pricer{}.Resolve(ctx, q, property, m.priceRequest(s, r))
		if err != nil {
			return out, errs.Conflict("no_rate", fmt.Sprintf("%s %s has no rate: %s", s.Court.Name, s.Start.Format("2 Jan 15:04"), errMessage(err)))
		}
		out.Currency = lp.Tax.Currency
		gross, _ := decimal.NewFromString(lp.Gross)
		d, _ := decimal.NewFromString(lp.Discount)
		ql := QuoteLine{CourtID: s.Court.ID, CourtName: s.Court.Name, FacilityName: s.Court.FacilityName, Start: s.Start, End: s.End, Hours: s.hours(),
			ListPrice: gross.StringFixed(0), Discount: d.StringFixed(0), Net: lp.Net().StringFixed(0), Tax: lp.TaxAmount().Add(lp.ServiceAmount()).StringFixed(0),
			Total: lp.Total().StringFixed(0), DayType: lp.DayType, TimeBand: lp.TimeBand, Promotions: []string{}}
		for _, a := range lp.Promotions {
			ql.Promotions = append(ql.Promotions, a.Name)
			if out.PromoCode != "" && strings.EqualFold(a.PromoCode, out.PromoCode) {
				out.PromoApplied = true
			}
		}
		out.Lines = append(out.Lines, ql)
		rent, disc, net = rent.Add(gross), disc.Add(d), net.Add(lp.Net())
		tax, total = tax.Add(lp.TaxAmount()).Add(lp.ServiceAmount()), total.Add(lp.Total())
		units, _ := decimal.NewFromString(lp.Units)
		if units.IsPositive() {
			promoLines = append(promoLines, commercial.PromoLine{Key: fmt.Sprint(i + 1), ServiceType: "sport_court", ItemRef: s.Court.PriceItem, Quantity: units,
				UnitPrice: gross.Div(units)})
		}
	}
	if out.PromoCode != "" && !out.PromoApplied {
		out.PromoError = "unknown promo code"
		if len(promoLines) > 0 {
			res, err := commercial.EvaluatePromotions(ctx, q, commercial.PromoContext{Property: property, At: r.Slots[0].Start, Channel: promoChannel(r.Channel),
				BusinessLine: "sportclub", CustomerID: r.Customer, Codes: []string{out.PromoCode}, Lines: promoLines, Currency: out.Currency})
			if err != nil {
				return out, err
			}
			for _, x := range res.Rejected {
				if strings.EqualFold(x.Code, out.PromoCode) {
					out.PromoError = x.Reason
				}
			}
		}
		if m.isValueVoucher(ctx, q, property, out.PromoCode) {
			out.PromoError, out.Voucher = "", true
		}
	}
	out.Rent, out.Discount, out.Net, out.Tax = rent.StringFixed(0), disc.StringFixed(0), net.StringFixed(0), tax.StringFixed(0)
	out.ServiceFee = "0"
	if r.Online {
		best := -1
		for _, o := range pol.Methods {
			if !o.Active {
				continue
			}
			fee := o.FeeFor(total)
			out.Methods = append(out.Methods, MethodQuote{Code: o.Code, Label: o.Label, Fee: fee.StringFixed(0), Total: total.Add(fee).StringFixed(0)})
			if best < 0 || fee.LessThan(decimal.RequireFromString(out.Methods[best].Fee)) {
				best = len(out.Methods) - 1
			}
		}
		if best >= 0 {
			out.Methods[best].Cheapest = true
		}
		if o, ok := pol.method(r.Method); ok {
			fee := o.FeeFor(total)
			out.Method, out.ServiceFee = o.Code, fee.StringFixed(0)
			total = total.Add(fee)
		}
	}
	out.Total = total.StringFixed(0)
	return out, nil
}

func promoChannel(ch string) string {
	switch ch {
	case "website", "member_app":
		return ch
	case "", "back_office":
		return "back_office"
	}
	return "ops"
}

func errMessage(err error) string {
	if de, ok := errs.As(err); ok {
		return de.Message
	}
	return err.Error()
}

// chargeLines prices the lines of a booking with the quote function and
// posts them to its folio with the sport as cost center (FR-91).
func (m *Module) chargeLines(ctx context.Context, tx pgx.Tx, r reservation.Reservation, slots []courtSlot, lines []reservation.Line, qr quoteRequest) (decimal.Decimal, uuid.UUID, error) {
	folioID, err := m.ensureFolio(ctx, tx, r)
	if err != nil {
		return decimal.Zero, folioID, err
	}
	total := decimal.Zero
	loc := calendar.Location(ctx, tx)
	for i, s := range slots {
		if i >= len(lines) {
			break
		}
		l := lines[i]
		lp, err := commercial.Pricer{}.Price(ctx, tx, r.PropertyID, m.priceRequest(s, qr))
		if err != nil {
			return total, folioID, errs.Conflict("no_rate", fmt.Sprintf("%s %s has no rate: %s", s.Court.Name, s.Start.In(loc).Format("2 Jan 15:04"), errMessage(err)))
		}
		units, _ := decimal.NewFromString(lp.Units)
		desc := fmt.Sprintf("%s · %s %s–%s", s.Court.Name, s.Start.In(loc).Format("Mon 2 Jan"), s.Start.In(loc).Format("15:04"), s.End.In(loc).Format("15:04"))
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "reservation.line", ReferenceID: &l.ID,
			Description: desc, Quantity: units, Net: lp.Net(), Service: lp.ServiceAmount(), Tax: lp.TaxAmount(), SnapshotID: lp.SnapshotID},
			BusinessLine: billing.LineSport, RevenueComponent: nonEmpty(lp.RevenueComponent, "court"), TaxLines: lp.Tax.Lines, CostCenter: s.Court.costCenter()}); err != nil {
			return total, folioID, err
		}
		if err := m.Res.SetLinePrice(ctx, tx, l.ID, lp.SnapshotID, lp.Total()); err != nil {
			return total, folioID, err
		}
		total = total.Add(lp.Total())
	}
	return total, folioID, nil
}

// ensureFolio opens the folio of a booking on first use.
func (m *Module) ensureFolio(ctx context.Context, tx pgx.Tx, r reservation.Reservation) (uuid.UUID, error) {
	if r.FolioID != nil {
		return *r.FolioID, nil
	}
	holder := deref(r.CustomerName)
	if holder == "" {
		holder = deref(r.GuestName)
	}
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: r.PropertyID, CustomerID: r.CustomerID, HolderName: holder,
		SourceType: "reservation", SourceID: &r.ID}, BusinessLine: billing.LineSport, ReservationID: &r.ID, CorporateName: deref(r.CorporateName)})
	if err != nil {
		return uuid.Nil, err
	}
	return f.ID, m.Res.SetFolio(ctx, tx, r.ID, f.ID)
}

// serviceFee posts the service fee of the online method (FR-30, FR-93).
func (m *Module) serviceFee(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, o OnlineMethod, amount decimal.Decimal) (decimal.Decimal, error) {
	fee := o.FeeFor(amount)
	if !fee.IsPositive() {
		return fee, nil
	}
	_, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "sportclub.service_fee",
		Description: "Biaya layanan " + o.Label, Net: fee}, BusinessLine: billing.LineSport, RevenueComponent: "gateway_fee", CostCenter: "sportclub"})
	return fee, err
}

// isValueVoucher tells a gift / value voucher code (redeemed at payment)
// from a promotion code (a discount on the price).
func (m *Module) isValueVoucher(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM commercial.vouchers v JOIN commercial.voucher_types t ON t.id = v.voucher_type_id
		WHERE v.property_id = $1 AND upper(v.code) = upper($2) AND t.kind = 'value' AND v.status = 'active')`, property, code).Scan(&ok)
	return err == nil && ok
}

// ── booking ────────────────────────────────────────────────────────────────

// DiscountInput is a manual discount of a supervisor (FR-121).
type DiscountInput struct {
	Amount string `json:"amount"`
	Reason string `json:"reason"`
}

type CourtBookingInput struct {
	CourtID       uuid.UUID      `json:"courtId,omitempty"`
	Start         time.Time      `json:"start,omitempty"`
	End           time.Time      `json:"end,omitempty"`
	Lines         []CourtLine    `json:"lines,omitempty" doc:"Several courts and hours in one booking and one payment (the cart)"`
	CustomerID    *uuid.UUID     `json:"customerId,omitempty"`
	Guest         *GuestInput    `json:"guest,omitempty"`
	CorporateName string         `json:"corporateName,omitempty"`
	Segment       string         `json:"segment,omitempty" doc:"Kept for reports; court prices are the same for members and guests"`
	Channel       string         `json:"channel,omitempty" enum:"ops,back_office,member_app,website,walk_in"`
	Via           string         `json:"via,omitempty" enum:"walk_in,phone" doc:"Front desk: walk-in or phone booking"`
	PackageCode   string         `json:"packageCode,omitempty" doc:"Court package (4x/8x) paying the hours"`
	PromoCode     string         `json:"promoCode,omitempty"`
	Hold          bool           `json:"hold,omitempty" doc:"Create as Draft (online checkout)"`
	Notes         string         `json:"notes,omitempty"`
	Payment       *PaymentInput  `json:"payment,omitempty" doc:"Front desk payment"`
	PayMethod     string         `json:"payMethod,omitempty" doc:"Online method code (website, Member App): the service fee is added and the gateway payment opened"`
	Discount      *DiscountInput `json:"discount,omitempty" doc:"Manual discount (supervisor)"`
	// set by the recurring booking
	RecurringGroupID *uuid.UUID `json:"-"`
}

// CourtLine is one court and time of a booking.
type CourtLine struct {
	CourtID uuid.UUID `json:"courtId"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
}

// CourtBookingResult is the booking with its folio.
type CourtBookingResult struct {
	Reservation reservation.Reservation `json:"reservation"`
	Folio       *billing.FolioDetail    `json:"folio"`
	Total       string                  `json:"total"`
	Booking     *CourtBooking           `json:"booking"`
	Checkout    *billing.Checkout       `json:"checkout"`
}

func publicToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// BookCourt books courts: lines checked and merged, held (online) or
// confirmed, priced with the quote function, paid at the desk, by a court
// package or online with the service fee of the method.
func (m *Module) BookCourt(ctx context.Context, tx pgx.Tx, property uuid.UUID, in CourtBookingInput, key string) (CourtBookingResult, error) {
	out := CourtBookingResult{Total: "0"}
	lines := in.Lines
	if len(lines) == 0 && in.CourtID != uuid.Nil {
		lines = []CourtLine{{CourtID: in.CourtID, Start: in.Start, End: in.End}}
	}
	if in.Channel == "" {
		in.Channel = "back_office"
	}
	online := in.Channel == "website" || in.Channel == "member_app"
	pol, err := m.courtPolicy(ctx, tx, property)
	if err != nil {
		return out, err
	}
	slots, err := m.normalizeLines(ctx, tx, property, lines, pol, slotOptions{Online: online})
	if err != nil {
		return out, err
	}
	if taken, err := m.takenSlots(ctx, tx, slots); err != nil {
		return out, err
	} else if len(taken) > 0 {
		e := errs.Conflict("slot_taken", "some of the chosen hours are no longer available; remove or replace them")
		e.Fields = taken
		return out, e
	}
	var method OnlineMethod
	if in.PayMethod != "" {
		var ok bool
		if method, ok = pol.method(in.PayMethod); !ok {
			return out, handle.Invalid("payMethod", "invalid_method", "this payment method is not available")
		}
	}
	cid, name, err := resolveCustomer(ctx, tx, property, in.CustomerID, in.Guest)
	if err != nil {
		return out, err
	}
	var phone, email string
	if in.Guest != nil {
		phone, email = in.Guest.Phone, in.Guest.Email
	}
	pkg := strings.ToUpper(strings.TrimSpace(in.PackageCode))
	if pkg != "" {
		if err := m.packageFits(ctx, tx, property, pkg, slots); err != nil {
			return out, err
		}
	}
	attrs := map[string]any{"publicToken": publicToken()}
	if in.Via == "phone" {
		attrs["via"] = "phone"
	}
	if in.PromoCode != "" {
		attrs["promoCode"] = strings.ToUpper(strings.TrimSpace(in.PromoCode))
	}
	channel := in.Channel
	if channel == "walk_in" || in.Via == "walk_in" {
		channel = "walk_in"
	}
	reqs := make([]reservation.LineRequest, 0, len(slots))
	for _, s := range slots {
		reqs = append(reqs, reservation.LineRequest{ResourceID: *s.Court.ResourceID, Start: s.Start, End: s.End, Description: s.Court.Name,
			Attributes: map[string]any{"courtId": s.Court.ID.String(), "facilityId": s.Court.FacilityID.String()}})
	}
	r, err := m.Res.Book(ctx, tx, property, reservation.BookRequest{BusinessLine: "sportclub", Channel: channel, CustomerID: cid, GuestName: name,
		GuestPhone: phone, GuestEmail: email, CorporateName: in.CorporateName, Hold: in.Hold, Confirm: !in.Hold, SourceType: "sportclub.court_booking",
		Notes: in.Notes, Lines: reqs, RecurringGroupID: in.RecurringGroupID, Attributes: attrs})
	if err != nil {
		return out, err
	}
	if in.Hold && pol.HoldMinutes > 0 {
		until := clock.Now().Add(time.Duration(pol.HoldMinutes) * time.Minute)
		if err := reservation.ExtendHold(ctx, tx, r.ID, until); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservation.reservations SET hold_expires_at = $2 WHERE id = $1`, r.ID, until); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservation.capacity_allocations SET expires_at = $2 WHERE reservation_id = $1 AND status = 'held'`, r.ID, until); err != nil {
			return out, err
		}
	}
	if pkg != "" {
		hours := 0
		for _, s := range slots {
			hours += s.hours()
		}
		vk := ""
		if key != "" {
			vk = "court-" + key
		}
		res, err := m.Vouchers.Redeem(ctx, tx, commercial.RedeemRequest{PropertyID: property, Code: pkg, Quantity: decimal.NewFromInt(int64(hours)),
			ServiceType: "sport_court", ItemRef: slots[0].Court.PriceItem, CustomerID: cid, SourceType: "reservation.reservation", SourceID: &r.ID, IdempotencyKey: vk})
		if err != nil {
			return out, err
		}
		if err := m.Res.SetAttribute(ctx, tx, r.ID, "packageCode", pkg); err != nil {
			return out, err
		}
		if err := m.Res.SetAttribute(ctx, tx, r.ID, "package", map[string]any{"code": pkg, "hours": hours, "remaining": res.Voucher.RemainingQuantity,
			"value": res.Recognized}); err != nil {
			return out, err
		}
	} else {
		qr := quoteRequest{Slots: slots, Promo: in.PromoCode, Customer: cid, Channel: in.Channel}
		total, folioID, err := m.chargeLines(ctx, tx, r, slots, r.Lines, qr)
		if err != nil {
			return out, err
		}
		if in.Discount != nil && in.Discount.Amount != "" {
			d, err := m.manualDiscount(ctx, tx, folioID, *in.Discount)
			if err != nil {
				return out, err
			}
			total = total.Sub(d)
		}
		if r, err = m.Res.Get(ctx, tx, r.ID, false); err != nil {
			return out, err
		}
		// a gift / value voucher code (FR-31) is redeemed first; the online
		// payment and its service fee cover only what is left
		var voucherPaid string
		if in.Payment == nil && m.isValueVoucher(ctx, tx, property, in.PromoCode) {
			co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: folioID, VoucherCode: strings.TrimSpace(in.PromoCode),
				Description: "Court booking " + r.Code, PayerName: name})
			if err != nil {
				return out, err
			}
			voucherPaid = co.VoucherPaid
			out.Checkout = &co
		}
		if method.Code != "" {
			sum, err := billing.FolioSummary(ctx, tx, folioID)
			if err != nil {
				return out, err
			}
			if due, _ := decimal.NewFromString(sum.Balance); due.IsPositive() {
				fee, err := m.serviceFee(ctx, tx, folioID, method, due)
				if err != nil {
					return out, err
				}
				total = total.Add(fee)
			}
		}
		out.Total = total.StringFixed(0)
		if in.Payment != nil {
			if err := m.settle(ctx, tx, folioID, in.Payment, key); err != nil {
				return out, err
			}
		}
		if method.Code != "" {
			co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: folioID, Method: method.MethodType, Description: "Court booking " + r.Code,
				PayerName: name})
			if err != nil {
				return out, err
			}
			if voucherPaid != "" {
				co.VoucherPaid = voucherPaid
			}
			out.Checkout = &co
		}
		if out.Checkout != nil && r.Status == reservation.StatusDraft {
			if due, _ := decimal.NewFromString(out.Checkout.AmountDue); !due.IsPositive() {
				if r, err = m.Res.Confirm(ctx, tx, r.ID, false); err != nil {
					return out, err
				}
			}
		}
		if in.Payment != nil && r.Status == reservation.StatusDraft {
			if r, err = m.Res.Confirm(ctx, tx, r.ID, false); err != nil {
				return out, err
			}
		}
		d, err := billing.GetFolio(ctx, tx, folioID)
		if err != nil {
			return out, err
		}
		out.Folio = &d
	}
	if err := m.Res.SetSource(ctx, tx, r.ID, r.ID); err != nil {
		return out, err
	}
	if out.Reservation, err = m.Res.Get(ctx, tx, r.ID, false); err != nil {
		return out, err
	}
	if b, err := m.bookingByID(ctx, tx, r.ID, true); err == nil {
		out.Booking = &b
	} else {
		return out, err
	}
	return out, m.live(ctx, tx, property, "booking_created", r.ID)
}

// manualDiscount posts a discount line of a supervisor (FR-121).
func (m *Module) manualDiscount(ctx context.Context, tx pgx.Tx, folioID uuid.UUID, in DiscountInput) (decimal.Decimal, error) {
	amt, err := handle.Decimal("discount.amount", in.Amount, decimal.Zero)
	if err != nil || !amt.IsPositive() {
		return decimal.Zero, err
	}
	if strings.TrimSpace(in.Reason) == "" {
		return decimal.Zero, handle.Invalid("discount.reason", "required", "a reason is required for a manual discount")
	}
	_, err = m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: folioID, ReferenceType: "sportclub.discount",
		Description: "Diskon manual: " + in.Reason, Net: amt.Neg(), Total: amt.Neg()}, BusinessLine: billing.LineSport, RevenueComponent: "discount",
		CostCenter: "sportclub"})
	return amt, err
}

// packageFits checks a court package against the lines: the package of a
// sport pays its hours on the day type and band of the package (FR-65,
// brochure: "paket berlaku untuk jenis hari dan jam yang sama").
func (m *Module) packageFits(ctx context.Context, q dbtx.Querier, property uuid.UUID, code string, slots []courtSlot) error {
	var typeCode, category, status string
	var items []string
	var remaining decimal.Decimal
	err := q.QueryRow(ctx, `SELECT t.code, t.category, v.status, t.applicable_items, v.remaining_quantity FROM commercial.vouchers v
		JOIN commercial.voucher_types t ON t.id = v.voucher_type_id WHERE v.property_id = $1 AND upper(v.code) = upper($2)`, property, code).
		Scan(&typeCode, &category, &status, &items, &remaining)
	if dbtx.IsNoRows(err) {
		return errs.Validation("unknown_package", "package "+code+" not found")
	}
	if err != nil {
		return err
	}
	if category != "court_package" {
		return errs.Conflict("not_court_package", code+" is not a court package")
	}
	if status != "active" && status != "partially_redeemed" {
		return errs.Conflict("package_"+status, "package "+code+" is "+status)
	}
	hours := 0
	for _, s := range slots {
		hours += s.hours()
		if len(items) > 0 && !slices.Contains(items, s.Court.PriceItem) {
			return errs.Conflict("package_other_court", "package "+code+" is not valid for "+s.Court.Name)
		}
		for t := s.Start; t.Before(s.End); t = t.Add(time.Hour) {
			if why, err := m.packageBand(ctx, q, property, typeCode, s.Court, t); err != nil {
				return err
			} else if why != "" {
				return errs.Conflict("package_other_time", "package "+code+" "+why)
			}
		}
	}
	if remaining.LessThan(decimal.NewFromInt(int64(hours))) {
		return errs.Conflict("package_used_up", fmt.Sprintf("package %s has %s hour(s) left", code, remaining.String()))
	}
	return nil
}

// packageBand checks the day type (WD = Monday–Friday, WE = Saturday,
// Sunday and holidays) and the band (D = day, E = evening) in the code of
// a package type such as FSG-WD-E-4X.
func (m *Module) packageBand(ctx context.Context, q dbtx.Querier, property uuid.UUID, typeCode string, c CourtInfo, t time.Time) (string, error) {
	parts := strings.Split(strings.ToUpper(typeCode), "-")
	kind, err := dayKind(ctx, q, property, t)
	if err != nil {
		return "", err
	}
	weekend := kind != "weekday"
	evening := c.Rules.EveningFrom
	if evening == "" {
		evening = "17:00"
	}
	for _, p := range parts {
		switch p {
		case "WD":
			if weekend {
				return "is valid Monday to Friday only", nil
			}
		case "WE":
			if !weekend {
				return "is valid on weekends and public holidays only", nil
			}
		case "D":
			if t.Format("15:04") >= evening {
				return "is valid before " + evening + " only", nil
			}
		case "E":
			if t.Format("15:04") < evening {
				return "is valid from " + evening + " only", nil
			}
		}
	}
	return "", nil
}

// live tells the open court boards that a booking changed (FR-58, FR-134).
func (m *Module) live(ctx context.Context, tx pgx.Tx, property uuid.UUID, typ string, rid uuid.UUID) error {
	return realtime.Publish(ctx, tx, "sportclub.board", typ, &property, map[string]any{"reservationId": rid})
}

// ── the booking view (§13.3) ────────────────────────────────────────────────

// Line states (one table for every screen).
const (
	StateAwaitingPayment = "awaiting_payment" // Menunggu Pembayaran
	StateExpired         = "expired"          // Kedaluwarsa
	StateScheduled       = "scheduled"        // Terjadwal
	StateLate            = "late"             // Belum Datang
	StatePlaying         = "playing"          // Sedang Main
	StateFinished        = "finished"         // Selesai
	StateNoShow          = "no_show"          // No-show
	StateVoid            = "void"             // Void
)

// CourtBookingLine is one court line of a booking.
type CourtBookingLine struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	ReservationID uuid.UUID      `json:"reservationId" db:"reservation_id"`
	LineNo        int            `json:"lineNo" db:"line_no"`
	CourtID       *uuid.UUID     `json:"courtId" db:"court_id"`
	CourtName     string         `json:"courtName" db:"court_name"`
	FacilityID    *uuid.UUID     `json:"facilityId" db:"facility_id"`
	FacilityCode  *string        `json:"facilityCode" db:"facility_code"`
	FacilityName  *string        `json:"facilityName" db:"facility_name"`
	FacilityType  *string        `json:"facilityType" db:"facility_type"`
	ResourceID    uuid.UUID      `json:"resourceId" db:"resource_id"`
	Start         time.Time      `json:"start" db:"start_at"`
	End           time.Time      `json:"end" db:"end_at"`
	Status        string         `json:"status" db:"status" enum:"held,confirmed,checked_in,completed,released,cancelled,no_show"`
	Amount        *string        `json:"amount" db:"amount"`
	Attributes    map[string]any `json:"-" db:"attributes"`
	State         string         `json:"state" db:"-" enum:"awaiting_payment,expired,scheduled,late,playing,finished,no_show,void"`
	CheckInFrom   time.Time      `json:"checkInFrom" db:"-"`
	CanCheckIn    bool           `json:"canCheckIn" db:"-"`
	Ready         bool           `json:"ready" db:"-" doc:"The court staff marked the court ready"`
}

// CourtBooking is a court booking as every screen shows it.
type CourtBooking struct {
	ID               uuid.UUID          `json:"id" db:"id"`
	Code             string             `json:"code" db:"code"`
	Status           string             `json:"status" db:"status"`
	RawChannel       string             `json:"rawChannel" db:"channel"`
	CustomerID       *uuid.UUID         `json:"customerId" db:"customer_id"`
	CustomerName     *string            `json:"customerName" db:"customer_name"`
	GuestName        *string            `json:"guestName" db:"guest_name"`
	Phone            *string            `json:"phone" db:"phone"`
	Email            *string            `json:"email" db:"email"`
	CorporateName    *string            `json:"corporateName" db:"corporate_name"`
	Notes            *string            `json:"notes" db:"notes"`
	HoldExpiresAt    *time.Time         `json:"holdExpiresAt" db:"hold_expires_at"`
	FolioID          *uuid.UUID         `json:"folioId" db:"folio_id"`
	Attributes       map[string]any     `json:"-" db:"attributes"`
	RecurringGroupID *uuid.UUID         `json:"recurringGroupId" db:"recurring_group_id"`
	CreatedAt        time.Time          `json:"createdAt" db:"created_at"`
	CheckedInAt      *time.Time         `json:"checkedInAt" db:"checked_in_at"`
	Charges          string             `json:"charges" db:"charges" doc:"Folio charges with tax and service fee"`
	Paid             string             `json:"paid" db:"paid"`
	ServiceFee       string             `json:"serviceFee" db:"service_fee"`
	Tax              string             `json:"tax" db:"tax"`
	Channel          string             `json:"channel" db:"-" enum:"website,member_app,walk_in,phone,recurring"`
	State            string             `json:"state" db:"-"`
	PayStatus        string             `json:"payStatus" db:"-" enum:"unpaid,partially_paid,paid,overpaid"`
	Balance          string             `json:"balance" db:"-"`
	HoldSeconds      int                `json:"holdSeconds" db:"-" doc:"Time left to pay (server clock)"`
	Name             string             `json:"name" db:"-" doc:"Customer, guest or company"`
	Start            *time.Time         `json:"start" db:"-"`
	End              *time.Time         `json:"end" db:"-"`
	PackageCode      string             `json:"packageCode,omitempty" db:"-"`
	Package          map[string]any     `json:"package,omitempty" db:"-"`
	PromoCode        string             `json:"promoCode,omitempty" db:"-"`
	Void             bool               `json:"void" db:"-"`
	VoidReason       string             `json:"voidReason,omitempty" db:"-"`
	Token            string             `json:"token,omitempty" db:"-" doc:"Public token of the confirmation / Cek Booking page"`
	Lines            []CourtBookingLine `json:"lines" db:"-"`
}

const bookingViewSelect = `SELECT r.id, r.code, r.status, r.channel, r.customer_id, c.name AS customer_name, r.guest_name,
	coalesce(c.phone, r.guest_phone) AS phone, coalesce(c.email, r.guest_email) AS email, r.corporate_name, r.notes, r.hold_expires_at, r.folio_id,
	r.attributes, r.recurring_group_id, r.created_at, r.checked_in_at,
	coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = r.folio_id AND x.voided_at IS NULL), 0)::text AS charges,
	coalesce((SELECT sum(amount - refunded_amount) FROM billing.payments p WHERE p.folio_id = r.folio_id AND p.status IN ('completed', 'refunded')
	  AND p.purpose = 'settlement'), 0)::text AS paid,
	coalesce((SELECT sum(total) FROM billing.folio_lines x WHERE x.folio_id = r.folio_id AND x.voided_at IS NULL AND x.revenue_component = 'gateway_fee'), 0)::text AS service_fee,
	coalesce((SELECT sum(tax_amount + service_amount) FROM billing.folio_lines x WHERE x.folio_id = r.folio_id AND x.voided_at IS NULL), 0)::text AS tax
	FROM reservation.reservations r LEFT JOIN crm.customers c ON c.id = r.customer_id
	WHERE r.kind = 'booking' AND r.source_type = 'sportclub.court_booking'`

const bookingLineSelect = `SELECT l.id, l.reservation_id, l.line_no, c.id AS court_id, coalesce(c.name, rs.name) AS court_name, f.id AS facility_id,
	f.code AS facility_code, f.name AS facility_name, f.facility_type, l.resource_id, lower(l.period) AS start_at, upper(l.period) AS end_at, l.status,
	trim_scale(l.amount)::text AS amount, l.attributes
	FROM reservation.reservation_lines l JOIN reservation.resources rs ON rs.id = l.resource_id
	LEFT JOIN sportclub.courts c ON c.resource_id = l.resource_id LEFT JOIN sportclub.facilities f ON f.id = c.facility_id`

// loadBookings returns court bookings (filter appended to bookingViewSelect).
func (m *Module) loadBookings(ctx context.Context, q dbtx.Querier, property uuid.UUID, filter string, args ...any) ([]CourtBooking, error) {
	list, err := handle.List[CourtBooking](q.Query(ctx, bookingViewSelect+` AND r.property_id = $1 `+filter, append([]any{property}, args...)...))
	if err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, b := range list {
		ids = append(ids, b.ID)
	}
	lines, err := handle.List[CourtBookingLine](q.Query(ctx, bookingLineSelect+` WHERE l.reservation_id = ANY($1) ORDER BY l.reservation_id, lower(l.period), l.line_no`, ids))
	if err != nil {
		return nil, err
	}
	ready := map[uuid.UUID]bool{}
	rows, err := q.Query(ctx, `SELECT DISTINCT reservation_id FROM sportclub.court_reports WHERE kind = 'ready' AND reservation_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ready[id] = true
	}
	rows.Close()
	pol, err := m.courtPolicy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	byID := map[uuid.UUID][]CourtBookingLine{}
	for _, l := range lines {
		byID[l.ReservationID] = append(byID[l.ReservationID], l)
	}
	now := clock.Now()
	for i := range list {
		list[i].Lines = byID[list[i].ID]
		if list[i].Lines == nil {
			list[i].Lines = []CourtBookingLine{}
		}
		finish(&list[i], pol, now, ready[list[i].ID])
	}
	return list, nil
}

// finish computes states, payment status and the display fields.
func finish(b *CourtBooking, pol CourtPolicy, now time.Time, ready bool) {
	a := b.Attributes
	b.Void, _ = a["void"].(bool)
	b.VoidReason, _ = a["voidReason"].(string)
	b.PackageCode, _ = a["packageCode"].(string)
	b.Package, _ = a["package"].(map[string]any)
	b.PromoCode, _ = a["promoCode"].(string)
	b.Channel = channelLabel(b.RawChannel, b.RecurringGroupID, a)
	b.Name = deref(b.CustomerName)
	if b.Name == "" {
		b.Name = deref(b.GuestName)
	}
	if b.Name == "" {
		b.Name = deref(b.CorporateName)
	}
	charges, _ := decimal.NewFromString(b.Charges)
	paid, _ := decimal.NewFromString(b.Paid)
	bal := charges.Sub(paid)
	b.Balance = bal.StringFixed(0)
	switch {
	case b.PackageCode != "" && !bal.IsPositive():
		b.PayStatus = "paid"
	case bal.IsNegative():
		b.PayStatus = "overpaid"
	case charges.IsPositive() && bal.IsZero():
		b.PayStatus = "paid"
	case paid.IsPositive():
		b.PayStatus = "partially_paid"
	default:
		b.PayStatus = "unpaid"
	}
	if b.Status == reservation.StatusDraft && b.HoldExpiresAt != nil {
		b.HoldSeconds = int(max(b.HoldExpiresAt.Sub(now), 0) / time.Second)
	}
	late := time.Duration(pol.NoShowMinutes) * time.Minute
	early := time.Duration(pol.CheckInMinutes) * time.Minute
	counts := map[string]int{}
	for i := range b.Lines {
		l := &b.Lines[i]
		l.Ready = ready
		l.CheckInFrom = l.Start.Add(-early)
		switch {
		case b.Void || l.Status == "cancelled":
			l.State = StateVoid
		case b.Status == reservation.StatusExpired || (l.Status == "held" && b.HoldSeconds == 0) || (l.Status == "released" && b.Status != reservation.StatusNoShow):
			l.State = StateExpired
		case l.Status == "held":
			l.State = StateAwaitingPayment
		case l.Status == "checked_in":
			l.State = StatePlaying
		case l.Status == "completed":
			l.State = StateFinished
		case l.Status == "no_show" || l.Status == "released":
			l.State = StateNoShow
		case l.Status == "confirmed" && !now.Before(l.End):
			l.State = StateLate // ended without check-in: still to decide (no-show or late check-in)
		case l.Status == "confirmed" && now.After(l.Start.Add(late)):
			l.State = StateLate
		default:
			l.State = StateScheduled
		}
		l.CanCheckIn = l.Status == "confirmed" && !now.Before(l.CheckInFrom) && now.Before(l.End) && !b.Void
		counts[l.State]++
		if b.Start == nil || l.Start.Before(*b.Start) {
			s := l.Start
			b.Start = &s
		}
		if b.End == nil || l.End.After(*b.End) {
			e := l.End
			b.End = &e
		}
	}
	for _, s := range []string{StateVoid, StateExpired, StateAwaitingPayment, StatePlaying, StateLate, StateScheduled, StateFinished, StateNoShow} {
		if counts[s] > 0 {
			b.State = s
			break
		}
	}
	if b.State == "" {
		b.State = StateScheduled
	}
}

func channelLabel(ch string, group *uuid.UUID, a map[string]any) string {
	switch {
	case group != nil:
		return "recurring"
	case a["via"] == "phone":
		return "phone"
	case ch == "website" || ch == "member_app":
		return ch
	}
	return "walk_in"
}

func (m *Module) bookingByID(ctx context.Context, q dbtx.Querier, rid uuid.UUID, withToken bool) (CourtBooking, error) {
	var property uuid.UUID
	if err := q.QueryRow(ctx, `SELECT property_id FROM reservation.reservations WHERE id = $1`, rid).Scan(&property); err != nil {
		if dbtx.IsNoRows(err) {
			return CourtBooking{}, errNotFound("booking")
		}
		return CourtBooking{}, err
	}
	list, err := m.loadBookings(ctx, q, property, `AND r.id = $2`, rid)
	if err != nil {
		return CourtBooking{}, err
	}
	if len(list) == 0 {
		return CourtBooking{}, errNotFound("court booking")
	}
	b := list[0]
	if withToken {
		b.Token, _ = b.Attributes["publicToken"].(string)
	}
	return b, nil
}

// bookingByToken finds a booking from the public token (confirmation, Cek
// Booking link).
func (m *Module) bookingByToken(ctx context.Context, q dbtx.Querier, property uuid.UUID, token string) (CourtBooking, error) {
	if len(token) < 16 {
		return CourtBooking{}, errNotFound("booking")
	}
	list, err := m.loadBookings(ctx, q, property, `AND r.attributes ->> 'publicToken' = $2`, token)
	if err != nil {
		return CourtBooking{}, err
	}
	if len(list) == 0 {
		return CourtBooking{}, errNotFound("booking")
	}
	list[0].Token = token
	return list[0], nil
}
