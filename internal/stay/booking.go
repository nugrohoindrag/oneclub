package stay

// Booking engine of the bungalows (docs/requirement-booking-hotel-mgcc.md
// §3–§4, §8, §10): a cart of one or more bungalows of the same dates —
// each with its room type, rate plan or stay package, guests, add-ons and
// the name of the guest staying — priced by the one booking function
// (Book, FR-H17) and booked as one reservation: a group with a rooming list
// when there are several bungalows (FR-H64), every bungalow its own stay,
// reservation and folio. Online (website, Member App) the bungalows are
// held while the guest pays the deposit on the mock gateway (FR-H33,
// FR-H35); an unpaid hold expires and the bungalows are released (FR-H36).
// The guest finds the booking again from its link (token) or its code and
// phone / e-mail: status, e-voucher, calendar, My Stay, cancellation under
// the policy of the rate plan and the payment still to make (FR-H38–H43).

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/reservation"
)

// CartItem is one bungalow of a cart.
type CartItem struct {
	BungalowTypeID uuid.UUID    `json:"bungalowTypeId"`
	RatePlan       string       `json:"ratePlan,omitempty" doc:"Rate plan code (Room Only, Long Stay …); default: the default plan"`
	StayPackage    string       `json:"stayPackage,omitempty" doc:"Stay package code instead of a rate plan"`
	Adults         int          `json:"adults,omitempty"`
	Children       int          `json:"children,omitempty"`
	Addons         []AddonInput `json:"addons,omitempty" doc:"Add-ons of this bungalow (breakfast, extra bed, BBQ, transfer …)"`
	OccupantName   string       `json:"occupantName,omitempty" doc:"Guest staying in this bungalow when not the booker (rooming list)"`
	UnitID         *uuid.UUID   `json:"unitId,omitempty" doc:"Front desk: this bungalow"`
}

// CartInput is a cart of bungalows for the same dates.
type CartInput struct {
	ArrivalDate   string     `json:"arrivalDate" doc:"Check-in date YYYY-MM-DD"`
	DepartureDate string     `json:"departureDate" doc:"Check-out date YYYY-MM-DD"`
	Items         []CartItem `json:"items"`
	PromoCode     string     `json:"promoCode,omitempty" doc:"Stay promotion code (not a billing voucher)"`
}

// CartLine is the price of one bungalow of the cart.
type CartLine struct {
	Index             int          `json:"index"`
	TypeID            uuid.UUID    `json:"typeId"`
	TypeName          string       `json:"typeName"`
	RatePlan          string       `json:"ratePlan"`
	RatePlanName      string       `json:"ratePlanName"`
	Adults            int          `json:"adults"`
	Children          int          `json:"children"`
	OccupantName      string       `json:"occupantName,omitempty"`
	Nights            []NightPrice `json:"nights"`
	RoomTotal         string       `json:"roomTotal" doc:"Room nights with tax and service, after the promotion"`
	RoomList          string       `json:"roomList" doc:"Room nights before the promotion (strike-through price)"`
	Discount          string       `json:"discount"`
	PromotionName     *string      `json:"promotionName"`
	Addons            []StayAddon  `json:"addons"`
	AddonsTotal       string       `json:"addonsTotal"`
	Net               string       `json:"net"`
	Service           string       `json:"service"`
	Tax               string       `json:"tax"`
	Total             string       `json:"total"`
	DepositRequired   string       `json:"depositRequired"`
	PaymentPolicy     string       `json:"paymentPolicy" enum:"deposit,full_prepayment,pay_at_hotel"`
	IncludesBreakfast bool         `json:"includesBreakfast"`
	TaxIncluded       bool         `json:"taxIncluded" doc:"Prices include tax & service (nett)"`
	FreeCancelHours   int          `json:"freeCancelHours"`
	CancelFeePercent  string       `json:"cancelFeePercent"`
	NonRefundable     bool         `json:"nonRefundable"`
	Error             *string      `json:"error,omitempty" doc:"Why this bungalow cannot be booked (sold out, capacity, minimum nights …)"`
	ErrorCode         *string      `json:"errorCode,omitempty"`
}

// CartQuote is the price summary of a cart (FR-H30).
type CartQuote struct {
	ArrivalDate   string     `json:"arrivalDate"`
	DepartureDate string     `json:"departureDate"`
	Nights        int        `json:"nights"`
	Items         []CartLine `json:"items"`
	Subtotal      string     `json:"subtotal" doc:"Before tax and service"`
	Discount      string     `json:"discount"`
	Service       string     `json:"service"`
	Tax           string     `json:"tax"`
	Total         string     `json:"total"`
	DepositNow    string     `json:"depositNow" doc:"Paid online now (deposit or full payment of the rate plans)"`
	PayAtHotel    string     `json:"payAtHotel" doc:"Left to pay at the hotel"`
	PromoCode     string     `json:"promoCode,omitempty"`
	PromoError    *string    `json:"promoError,omitempty" doc:"Why the promo code does not apply"`
	OK            bool       `json:"ok" doc:"Every bungalow can be booked"`
	HoldMinutes   int        `json:"holdMinutes" doc:"The bungalows are held this long while the guest pays"`
}

// cartBase are the details of the booker shared by the bungalows of a cart.
type cartBase struct {
	Channel, Source       string
	CustomerID            *uuid.UUID
	Guest                 *GuestInput
	CorporateAccountID    *uuid.UUID
	BookerTitle           string
	Nationality           string
	ExpectedArrival       string
	SpecialRequests       string
	MarketingConsent      bool
	Segment               string
	VIP                   bool
	Notes                 string
	Hold                  bool
	OverrideRestrictions  bool
	SupervisorReason      string
	Payment               *PaymentInput
	GroupName             string
	FolioMode             string
	RequireSameDatesItems bool
}

func (b cartBase) stayInput(c CartInput, it CartItem) StayInput {
	return StayInput{Kind: "bungalow", BungalowTypeID: &it.BungalowTypeID, UnitID: it.UnitID, ArrivalDate: c.ArrivalDate, DepartureDate: c.DepartureDate,
		Adults: max(it.Adults, 1), Children: it.Children, RatePlan: it.RatePlan, StayPackage: it.StayPackage, Addons: it.Addons, PromoCode: c.PromoCode,
		CustomerID: b.CustomerID, Guest: b.Guest, CorporateAccountID: b.CorporateAccountID, SpecialRequests: b.SpecialRequests, Segment: b.Segment,
		Channel: b.Channel, BookingSource: b.Source, ExpectedArrival: b.ExpectedArrival, VIP: b.VIP, Notes: b.Notes, OccupantName: it.OccupantName,
		BookerTitle: b.BookerTitle, Nationality: b.Nationality, MarketingConsent: b.MarketingConsent, OverrideRestrictions: b.OverrideRestrictions,
		SupervisorReason: b.SupervisorReason}
}

// lineOf prices one booked bungalow of a cart from its stay.
func lineOf(i int, res StayResult, it CartItem) CartLine {
	l := CartLine{Index: i, TypeID: it.BungalowTypeID, TypeName: deref(res.Stay.TypeName), RatePlan: deref(res.Stay.RatePlan), Adults: res.Stay.Adults,
		Children: res.Stay.Children, OccupantName: it.OccupantName, Nights: []NightPrice{}, Addons: res.Addons, Total: res.Total, DepositRequired: res.DepositRequired,
		Discount: "0", PaymentPolicy: "deposit"}
	if l.Addons == nil {
		l.Addons = []StayAddon{}
	}
	net, svc, tax, addons := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
	if res.Folio != nil {
		for _, fl := range res.Folio.Lines {
			if fl.VoidedAt != nil {
				continue
			}
			net, svc, tax = net.Add(decOf(fl.NetAmount)), svc.Add(decOf(fl.ServiceAmount)), tax.Add(decOf(fl.TaxAmount))
		}
	}
	for _, a := range res.Addons {
		if a.VoidedAt == nil && !a.Included {
			addons = addons.Add(decOf(a.Total))
		}
	}
	l.AddonsTotal = addons.String()
	if q := res.Quote; q != nil {
		l.RatePlan, l.RatePlanName, l.Nights, l.Discount, l.PromotionName = q.RatePlan, q.RatePlanName, q.Nights, q.Discount, q.PromotionName
		l.PaymentPolicy, l.IncludesBreakfast, l.TaxIncluded = q.PaymentPolicy, q.IncludesBreakfast, q.PricingMode == "nett"
		l.FreeCancelHours, l.CancelFeePercent, l.NonRefundable = q.FreeCancelHours, q.CancelFeePercent, q.NonRefundable
		l.RoomTotal, l.RoomList = q.Total, q.Total
		if g, d := decOf(q.Gross), decOf(q.Discount); d.IsPositive() && g.GreaterThan(d) {
			l.RoomList = decOf(q.Total).Mul(g).Div(g.Sub(d)).Round(0).String()
		}
		if res.Stay.RoomPosting == "nightly" {
			// the nights are posted by the night audit: they are not on the folio yet
			net, svc, tax = net.Add(decOf(q.Net)), svc.Add(decOf(q.Service)), tax.Add(decOf(q.Tax))
		}
	}
	l.Net, l.Service, l.Tax = net.String(), svc.String(), tax.String()
	return l
}

// QuoteCart prices a cart with the booking function itself, inside a
// savepoint that is rolled back: the price, the availability (the
// bungalows of the cart count against each other) and the rules are those
// of the booking (FR-H17). A bungalow that cannot be booked carries its
// reason; the others are still priced.
func (m *Module) QuoteCart(ctx context.Context, tx pgx.Tx, property uuid.UUID, c CartInput, b cartBase) (CartQuote, error) {
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return CartQuote{}, err
	}
	out := CartQuote{ArrivalDate: c.ArrivalDate, DepartureDate: c.DepartureDate, Items: []CartLine{}, PromoCode: strings.ToUpper(strings.TrimSpace(c.PromoCode)),
		HoldMinutes: pol.WebsiteHoldMinutes, OK: len(c.Items) > 0}
	loc := calendar.Location(ctx, tx)
	a, aerr := time.ParseInLocation(time.DateOnly, c.ArrivalDate, loc)
	d, derr := time.ParseInLocation(time.DateOnly, c.DepartureDate, loc)
	if aerr != nil || derr != nil || !d.After(a) {
		return out, handle.Invalid("departureDate", "invalid_period", "check-out harus setelah check-in")
	}
	out.Nights = int(d.Sub(a).Hours()/24 + 0.5)
	outer, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = outer.Rollback(ctx) }()
	if b.CustomerID == nil && b.Guest == nil {
		b.Guest = &GuestInput{Name: "Quote"}
	}
	sum := func(f func(CartLine) string) string {
		t := decimal.Zero
		for _, l := range out.Items {
			if l.Error == nil {
				t = t.Add(decOf(f(l)))
			}
		}
		return t.String()
	}
	for i, it := range c.Items {
		in := b.stayInput(c, it)
		sp, err := outer.Begin(ctx)
		if err != nil {
			return out, err
		}
		res, err := m.Book(ctx, sp, property, in, "")
		if de, ok := errs.As(err); ok && (de.Code == "invalid_promo_code" || de.Code == "promo_not_applicable") && in.PromoCode != "" {
			// the code does not apply: say why and price without it (FR-H16)
			_ = sp.Rollback(ctx)
			msg := de.Message
			out.PromoError, c.PromoCode, in.PromoCode = &msg, "", ""
			if sp, err = outer.Begin(ctx); err != nil {
				return out, err
			}
			res, err = m.Book(ctx, sp, property, in, "")
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			de, ok := errs.As(err)
			if !ok || de.Kind == errs.KindInternal {
				return out, err
			}
			msg, code := de.Message, de.Code
			var name string
			_ = tx.QueryRow(ctx, `SELECT name FROM stay.bungalow_types WHERE id = $1`, it.BungalowTypeID).Scan(&name)
			out.Items = append(out.Items, CartLine{Index: i, TypeID: it.BungalowTypeID, TypeName: name, RatePlan: it.RatePlan, Adults: it.Adults,
				Children: it.Children, Nights: []NightPrice{}, Addons: []StayAddon{}, Error: &msg, ErrorCode: &code})
			out.OK = false
			continue
		}
		// the savepoint is kept (released into outer) so the next bungalow sees this one booked
		if err := sp.Commit(ctx); err != nil {
			return out, err
		}
		out.Items = append(out.Items, lineOf(i, res, it))
	}
	out.Subtotal, out.Service, out.Tax, out.Total = sum(func(l CartLine) string { return l.Net }), sum(func(l CartLine) string { return l.Service }),
		sum(func(l CartLine) string { return l.Tax }), sum(func(l CartLine) string { return l.Total })
	out.Discount, out.DepositNow = sum(func(l CartLine) string { return l.Discount }), sum(func(l CartLine) string { return l.DepositRequired })
	out.PayAtHotel = decOf(out.Total).Sub(decOf(out.DepositNow)).String()
	return out, nil
}

// CartBooking is a booked cart: the group (or the one stay) and the online
// payments waiting on the gateway.
type CartBooking struct {
	Code     string            `json:"code" doc:"Reservation code: the group number, or the reservation of the one bungalow"`
	Token    string            `json:"token" doc:"Link of the booking (confirmation, e-voucher, My Stay)"`
	GroupID  *uuid.UUID        `json:"groupId"`
	Stays    []Stay            `json:"stays"`
	Payments []billing.Payment `json:"payments" doc:"Online payments opened on the gateway (one per bungalow folio)"`
	Total    string            `json:"total"`
	Deposit  string            `json:"depositRequired"`
}

// bookCart books every bungalow of a cart in one transaction: when one is
// sold out or refused the whole cart is refused with the item that failed
// (FR-H35). Several bungalows make a group with a rooming list (FR-H64).
func (m *Module) bookCart(ctx context.Context, tx pgx.Tx, property uuid.UUID, c CartInput, b cartBase, payMethod, key string) (CartBooking, error) {
	if len(c.Items) == 0 {
		return CartBooking{}, handle.Invalid("items", "required", "pilih minimal satu bungalow")
	}
	if len(c.Items) > 15 {
		return CartBooking{}, handle.Invalid("items", "too_many", "paling banyak 15 bungalow per reservasi")
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return CartBooking{}, err
	}
	cid, name, phone, err := resolveCustomer(ctx, tx, property, b.CustomerID, b.Guest)
	if err != nil {
		return CartBooking{}, err
	}
	b.CustomerID = cid
	email := ""
	if b.Guest != nil {
		email = b.Guest.Email
		if phone == "" {
			phone = b.Guest.Phone
		}
	}
	out := CartBooking{Stays: []Stay{}, Payments: []billing.Payment{}}
	var gid *uuid.UUID
	if len(c.Items) > 1 || b.GroupName != "" {
		g, err := m.createGroup(ctx, tx, property, groupInput{Name: nonEmpty(b.GroupName, name), CustomerID: cid, ContactName: name, ContactPhone: phone,
			ContactEmail: email, CorporateAccountID: b.CorporateAccountID, FolioMode: b.FolioMode, Source: b.Source, Channel: b.Channel, Notes: b.Notes})
		if err != nil {
			return out, err
		}
		gid, out.GroupID, out.Code, out.Token = &g.ID, &g.ID, g.GroupNo, deref(g.PublicToken)
	}
	total, deposit := decimal.Zero, decimal.Zero
	for i, it := range c.Items {
		in := b.stayInput(c, it)
		in.GroupID, in.hold, in.holdMinutes = gid, b.Hold, pol.WebsiteHoldMinutes
		if b.Payment != nil {
			p := *b.Payment
			in.Payment = &p
		}
		res, err := m.Book(ctx, tx, property, in, "")
		if err != nil {
			if de, ok := errs.As(err); ok && de.Kind != errs.KindInternal {
				var tname string
				_ = tx.QueryRow(ctx, `SELECT name FROM stay.bungalow_types WHERE id = $1`, it.BungalowTypeID).Scan(&tname)
				e := *de
				e.Message = tname + ": " + de.Message
				e.Fields = append(e.Fields, errs.FieldError{Field: fmt.Sprintf("items[%d]", i), Code: de.Code, Message: de.Message})
				return out, &e
			}
			return out, err
		}
		out.Stays = append(out.Stays, res.Stay)
		total, deposit = total.Add(decOf(res.Total)), deposit.Add(decOf(res.DepositRequired))
		if gid != nil && b.FolioMode == "combined" && res.Folio != nil && (cid != nil || b.CorporateAccountID != nil) {
			// one bill for the group: the folios of the bungalows join the payer's customer folio
			payer := billing.CustomerFolioInput{CustomerID: cid}
			if b.CorporateAccountID != nil {
				payer = billing.CustomerFolioInput{CorporateAccountID: b.CorporateAccountID}
			}
			cf, err := m.Billing.AttachFolio(ctx, tx, property, res.Folio.ID, payer)
			if err != nil {
				return out, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.stay_groups SET customer_folio_id = $2 WHERE id = $1`, *gid, cf); err != nil {
				return out, err
			}
		}
		if gid == nil {
			out.Code, out.Token = res.Reservation.Code, deref(res.Stay.PublicToken)
		}
		if b.MarketingConsent && cid != nil {
			yes := true
			if _, err := crm.SetConsent(ctx, tx, property, *cid, nil, &yes); err != nil {
				return out, err
			}
		}
	}
	out.Total, out.Deposit = total.String(), deposit.String()
	if payMethod != "" {
		pays, err := m.openPayments(ctx, tx, out.Stays, payMethod, name)
		if err != nil {
			return out, err
		}
		out.Payments = pays
	}
	if gid != nil {
		if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.group", EntityID: gid.String(),
			EntityLabel: out.Code, PropertyID: &property, After: map[string]any{"stays": len(out.Stays), "total": out.Total, "source": b.Source}}); err != nil {
			return out, err
		}
	}
	_ = key
	return out, nil
}

// openPayments opens the online payment of the deposit (or the full
// prepayment) of every bungalow still to pay, on the mock gateway while the
// real one is on hold (FR-H31, FR-H37): one payment per bungalow folio.
func (m *Module) openPayments(ctx context.Context, tx pgx.Tx, stays []Stay, method, payer string) ([]billing.Payment, error) {
	out := []billing.Payment{}
	for _, s := range stays {
		if s.FolioID == nil || s.BookingStatus != "awaiting_payment" {
			continue
		}
		r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
		if err != nil {
			return nil, err
		}
		f, err := billing.GetFolio(ctx, tx, *s.FolioID)
		if err != nil {
			return nil, err
		}
		paid := decOf(f.Summary.Payments).Add(decOf(f.Summary.HeldDeposits))
		due := decOf(r.DepositRequired).Sub(paid)
		if !due.IsPositive() {
			continue
		}
		if err := m.Billing.CancelPending(ctx, tx, *s.FolioID, "a new payment was opened"); err != nil {
			return nil, err
		}
		unposted := decOf(s.TotalDue).Sub(decOf(f.Charges))
		co, err := m.Billing.Checkout(ctx, tx, billing.CheckoutRequest{FolioID: *s.FolioID, Method: method, Amount: due, Description: "Bungalow " + s.StayNo,
			PayerName: payer, Unposted: unposted})
		if err != nil {
			return nil, err
		}
		if co.Online != nil {
			out = append(out, *co.Online)
		}
	}
	return out, nil
}

// ── groups (FR-H64) ────────────────────────────────────────────────────────

// Group is a reservation of several bungalows (rooming list).
type Group struct {
	ID                 uuid.UUID  `json:"id" db:"id"`
	GroupNo            string     `json:"groupNo" db:"group_no"`
	Name               string     `json:"name" db:"name"`
	CustomerID         *uuid.UUID `json:"customerId" db:"customer_id"`
	ContactName        *string    `json:"contactName" db:"contact_name"`
	ContactPhone       *string    `json:"contactPhone" db:"contact_phone"`
	ContactEmail       *string    `json:"contactEmail" db:"contact_email"`
	CorporateAccountID *uuid.UUID `json:"corporateAccountId" db:"corporate_account_id"`
	FolioMode          string     `json:"folioMode" db:"folio_mode" enum:"combined,per_stay"`
	CustomerFolioID    *uuid.UUID `json:"customerFolioId" db:"customer_folio_id"`
	BookingSource      *string    `json:"bookingSource" db:"booking_source"`
	Channel            string     `json:"channel" db:"channel"`
	PublicToken        *string    `json:"publicToken" db:"public_token"`
	Notes              *string    `json:"notes" db:"notes"`
	Status             string     `json:"status" db:"status"`
	CreatedAt          time.Time  `json:"createdAt" db:"created_at"`
	Bungalows          int        `json:"bungalows" db:"bungalows"`
	Arrival            *time.Time `json:"arrival" db:"arrival"`
	Departure          *time.Time `json:"departure" db:"departure"`
	Total              string     `json:"total" db:"total"`
	Paid               string     `json:"paid" db:"paid"`
}

const groupSelect = `SELECT g.id, g.group_no, g.name, g.customer_id, g.contact_name, g.contact_phone, g.contact_email, g.corporate_account_id, g.folio_mode,
	g.customer_folio_id, g.booking_source, g.channel, g.public_token, g.notes, g.status, g.created_at,
	(SELECT count(*) FROM stay.stays s WHERE s.group_id = g.id AND s.status NOT IN ('expired', 'void'))::int AS bungalows,
	(SELECT min(s.start_at) FROM stay.stays s WHERE s.group_id = g.id AND s.status NOT IN ('expired', 'void')) AS arrival,
	(SELECT max(s.end_at) FROM stay.stays s WHERE s.group_id = g.id AND s.status NOT IN ('expired', 'void')) AS departure,
	trim_scale(coalesce((SELECT sum(ef.charges) FROM stay.stays s JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id WHERE s.group_id = g.id
	  AND s.status NOT IN ('expired', 'void')), 0) + coalesce((SELECT sum(coalesce((s.room_quote->>'total')::numeric, 0) - coalesce((SELECT sum(np.total)
	  FROM stay.night_postings np WHERE np.stay_id = s.id), 0)) FROM stay.stays s WHERE s.group_id = g.id AND s.room_posting = 'nightly'
	  AND s.status NOT IN ('expired', 'void', 'cancelled', 'no_show')), 0))::text AS total,
	trim_scale(coalesce((SELECT sum(ef.paid) FROM stay.stays s JOIN reporting.eng_folios ef ON ef.folio_id = s.folio_id WHERE s.group_id = g.id), 0))::text AS paid
	FROM stay.stay_groups g`

type groupInput struct {
	Name, ContactName, ContactPhone, ContactEmail, FolioMode, Source, Channel, Notes string
	CustomerID, CorporateAccountID                                                   *uuid.UUID
}

func (m *Module) createGroup(ctx context.Context, tx pgx.Tx, property uuid.UUID, in groupInput) (Group, error) {
	loc := calendar.Location(ctx, tx)
	no, err := numbering.Next(ctx, tx, property, "GRP", clock.Now().In(loc))
	if err != nil {
		return Group{}, err
	}
	if in.FolioMode != "combined" {
		in.FolioMode = "per_stay"
	}
	if in.Channel == "" {
		in.Channel = "back_office"
	}
	gid := id.New()
	if _, err := tx.Exec(ctx, `INSERT INTO stay.stay_groups (id, property_id, group_no, name, customer_id, contact_name, contact_phone, contact_email,
		corporate_account_id, folio_mode, booking_source, channel, public_token, notes, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		gid, property, no, in.Name, in.CustomerID, nzs(in.ContactName), nzs(in.ContactPhone), nzs(in.ContactEmail), in.CorporateAccountID, in.FolioMode,
		nzs(in.Source), in.Channel, publicToken(), nzs(in.Notes), actor(ctx)); err != nil {
		return Group{}, err
	}
	return m.group(ctx, tx, gid)
}

func (m *Module) group(ctx context.Context, q dbtx.Querier, gid uuid.UUID) (Group, error) {
	rows, err := q.Query(ctx, groupSelect+` WHERE g.id = $1`, gid)
	return handle.One[Group](rows, err, "group")
}

// ── expiry of an unpaid hold (FR-H36, §12.3 Kedaluwarsa) ──────────────────

// expireStay releases a bungalow whose online payment failed or whose hold
// ran out: the stay is Expired, the pending payment cancelled, the charges
// voided and the promotion given back.
func (m *Module) expireStay(ctx context.Context, tx pgx.Tx, s Stay, reason string) error {
	if s.Status != "reserved" {
		return nil
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return err
	}
	switch r.Status {
	case reservation.StatusDraft:
		if _, err := m.Res.ExpireNow(ctx, tx, r.ID, reason); err != nil {
			return err
		}
	case reservation.StatusExpired:
	default:
		return nil // paid meanwhile: the stay is kept
	}
	if s.FolioID != nil {
		if err := m.Billing.CancelPending(ctx, tx, *s.FolioID, reason); err != nil {
			return err
		}
		if st, err := billing.FolioStatus(ctx, tx, *s.FolioID); err == nil && st == "open" {
			lines, err := billing.LinesOf(ctx, tx, *s.FolioID)
			if err != nil {
				return err
			}
			for _, l := range lines {
				if err := m.Billing.VoidCharge(ctx, tx, l.ID, reason); err != nil {
					return err
				}
			}
		}
	}
	if err := m.reverseAccommodation(ctx, tx, s); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET status = 'expired', cancel_reason = $2, updated_by = $3 WHERE id = $1`, s.ID, reason, actor(ctx)); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "expire", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Reason: reason, Before: map[string]any{"status": s.Status}, After: map[string]any{"status": "expired"}}); err != nil {
		return err
	}
	if s.UnitTypeID != nil {
		return m.offerWaitlist(ctx, tx, s.PropertyID, *s.UnitTypeID)
	}
	return nil
}

// ExpireHolds marks the stays whose hold ran out Expired (every minute).
func (m *Module) ExpireHolds(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	if _, err := reservation.ExpireHolds(ctx, m.DB); err != nil {
		return 0, err
	}
	n := 0
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		list, err := handle.List[Stay](tx.Query(ctx, staySelect+` WHERE s.status = 'reserved' AND r.status = 'expired' AND s.kind = 'bungalow' LIMIT 200`))
		if err != nil {
			return err
		}
		for _, s := range list {
			if err := m.expireStay(ctx, tx, s, "the payment time ran out"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ── the booking of a guest: link, status, payment, cancellation ──────────

// BookingStay is one bungalow of a guest's booking.
type BookingStay struct {
	ID                uuid.UUID   `json:"id"`
	StayNo            string      `json:"stayNo"`
	ReservationCode   string      `json:"reservationCode"`
	TypeID            *uuid.UUID  `json:"typeId"`
	TypeName          string      `json:"typeName"`
	UnitName          *string     `json:"unitName" doc:"The bungalow, once assigned"`
	Arrival           time.Time   `json:"arrival"`
	Departure         time.Time   `json:"departure"`
	Nights            int         `json:"nights"`
	Adults            int         `json:"adults"`
	Children          int         `json:"children"`
	OccupantName      *string     `json:"occupantName"`
	RatePlan          string      `json:"ratePlan"`
	RatePlanName      string      `json:"ratePlanName"`
	IncludesBreakfast bool        `json:"includesBreakfast"`
	TaxIncluded       bool        `json:"taxIncluded"`
	Addons            []StayAddon `json:"addons"`
	Total             string      `json:"total"`
	Paid              string      `json:"paid"`
	DepositRequired   string      `json:"depositRequired"`
	BookingStatus     string      `json:"bookingStatus"`
	PaymentStatus     string      `json:"paymentStatus"`
	FreeCancelHours   int         `json:"freeCancelHours"`
	CancelFeePercent  string      `json:"cancelFeePercent"`
	NonRefundable     bool        `json:"nonRefundable"`
	FreeCancelUntil   *time.Time  `json:"freeCancelUntil"`
	CanCancel         bool        `json:"canCancel" doc:"The guest may cancel this bungalow online (rate plan allows it)"`
	CancelFee         string      `json:"cancelFee" doc:"Fee if cancelled now"`
	Token             *string     `json:"token" doc:"My Stay of this bungalow"`
}

// BookingContact is the address of the club on the confirmation (FR-H06).
type BookingContact struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
	WhatsApp string `json:"whatsApp"`
	MapURL   string `json:"mapUrl"`
}

// BookingPayment is the online payment waiting on the gateway (the sum of
// the payments of the bungalows).
type BookingPayment struct {
	Numbers   []string   `json:"numbers"`
	Method    string     `json:"method"`
	Amount    string     `json:"amount"`
	QRString  *string    `json:"qrString"`
	VANumber  *string    `json:"vaNumber"`
	ExpiresAt *time.Time `json:"expiresAt" doc:"Server time the payment must be completed by"`
	Sandbox   bool       `json:"sandbox" doc:"Mock gateway: pay on the website payment page"`
}

// BookingView is the confirmation / Cek Booking page of a guest.
type BookingView struct {
	PropertyID    uuid.UUID       `json:"propertyId"`
	Code          string          `json:"code"`
	Token         string          `json:"token"`
	GroupNo       *string         `json:"groupNo"`
	Status        string          `json:"status" enum:"awaiting_payment,confirmed,in_house,checked_out,cancelled,expired,mixed"`
	BookerName    string          `json:"bookerName"`
	BookerPhone   *string         `json:"bookerPhone"`
	BookerEmail   *string         `json:"bookerEmail"`
	Stays         []BookingStay   `json:"stays"`
	Total         string          `json:"total"`
	Paid          string          `json:"paid"`
	Balance       string          `json:"balance"`
	DepositDue    string          `json:"depositDue" doc:"Deposit still to pay now"`
	HoldSeconds   int             `json:"holdSeconds" doc:"Time left to pay (server clock)"`
	HoldExpiresAt *time.Time      `json:"holdExpiresAt"`
	Payment       *BookingPayment `json:"payment"`
	CanPay        bool            `json:"canPay" doc:"Lanjutkan Pembayaran"`
	CheckInTime   string          `json:"checkInTime"`
	CheckOutTime  string          `json:"checkOutTime"`
	Terms         string          `json:"terms"`
	TermsEn       string          `json:"termsEn"`
	HouseRules    string          `json:"houseRules"`
	HouseRulesEn  string          `json:"houseRulesEn"`
	ChildPolicy   string          `json:"childPolicy"`
	Contact       BookingContact  `json:"contact"`
	Methods       []string        `json:"methods"`
}

// contactOf is the contact of the club: Stay Policies, else the property.
func (m *Module) contactOf(ctx context.Context, q dbtx.Querier, property uuid.UUID, pol Policy) BookingContact {
	c := BookingContact{Address: pol.ContactAddress, Phone: pol.ContactPhone, Email: pol.ContactEmail, WhatsApp: pol.ContactWhatsApp, MapURL: pol.MapURL}
	var name string
	var addr, city, phone, email *string
	if err := q.QueryRow(ctx, `SELECT name, address, city, phone, email FROM platform.properties WHERE id = $1`, property).Scan(&name, &addr, &city, &phone,
		&email); err == nil {
		c.Name = name
		if c.Address == "" {
			c.Address = strings.TrimSpace(strings.Trim(deref(addr)+", "+deref(city), ", "))
		}
		if c.Phone == "" {
			c.Phone = deref(phone)
		}
		if c.Email == "" {
			c.Email = deref(email)
		}
	}
	return c
}

// methodsOf are the online methods offered on the website (FR-H72).
func methodsOf(pol Policy) []string {
	out := []string{}
	for _, m := range strings.Split(pol.OnlineMethods, ",") {
		if m = strings.TrimSpace(m); m != "" && slices.Contains([]string{"qris", "virtual_account", "card"}, m) {
			out = append(out, m)
		}
	}
	return out
}

// staysOfToken returns the stays of a booking link: a group or one stay.
func (m *Module) staysOfToken(ctx context.Context, q dbtx.Querier, property uuid.UUID, token string) ([]Stay, *Group, error) {
	token = strings.TrimSpace(token)
	if len(token) < 16 {
		return nil, nil, errs.NotFound("booking")
	}
	var gid uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM stay.stay_groups WHERE property_id = $1 AND public_token = $2`, property, token).Scan(&gid)
	if err == nil {
		g, err := m.group(ctx, q, gid)
		if err != nil {
			return nil, nil, err
		}
		list, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.group_id = $1 ORDER BY s.stay_no`, gid))
		return list, &g, err
	}
	if !dbtx.IsNoRows(err) {
		return nil, nil, err
	}
	list, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.public_token = $2 AND s.kind = 'bungalow'`, property, token))
	if err != nil {
		return nil, nil, err
	}
	if len(list) == 0 {
		return nil, nil, errs.NotFound("booking")
	}
	return list, nil, nil
}

// BookingOf builds the guest view of a booking link.
func (m *Module) BookingOf(ctx context.Context, tx pgx.Tx, property uuid.UUID, token string) (BookingView, error) {
	stays, g, err := m.staysOfToken(ctx, tx, property, token)
	if err != nil {
		return BookingView{}, err
	}
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return BookingView{}, err
	}
	out := BookingView{PropertyID: property, Token: token, Stays: []BookingStay{}, CheckInTime: pol.CheckInTime, CheckOutTime: pol.CheckOutTime, Terms: pol.Terms, TermsEn: pol.TermsEn,
		HouseRules: pol.HouseRules, HouseRulesEn: pol.HouseRulesEn, ChildPolicy: pol.ChildPolicy, Contact: m.contactOf(ctx, tx, property, pol), Methods: methodsOf(pol)}
	if g != nil {
		out.Code, out.GroupNo, out.BookerName, out.BookerPhone, out.BookerEmail = g.GroupNo, &g.GroupNo, deref(g.ContactName), g.ContactPhone, g.ContactEmail
		if out.BookerName == "" {
			out.BookerName = g.Name
		}
	} else {
		s := stays[0]
		out.Code, out.BookerName, out.BookerPhone, out.BookerEmail = s.ReservationCode, guestLabel(s), s.GuestPhone, s.GuestEmail
	}
	total, paid, depositDue := decimal.Zero, decimal.Zero, decimal.Zero
	statuses := map[string]bool{}
	var pay *BookingPayment
	for _, s := range stays {
		bs := BookingStay{ID: s.ID, StayNo: s.StayNo, ReservationCode: s.ReservationCode, TypeID: s.UnitTypeID, TypeName: deref(s.TypeName), Arrival: s.Start,
			Departure: s.End, Nights: s.Nights, Adults: s.Adults, Children: s.Children, OccupantName: s.OccupantName, RatePlan: deref(s.RatePlan),
			RatePlanName: deref(s.RatePlan), Total: s.TotalDue, Paid: s.Paid, BookingStatus: s.BookingStatus, PaymentStatus: s.PaymentStatus, CancelFee: "0",
			TaxIncluded: s.TaxIncluded != nil && *s.TaxIncluded, Token: s.PublicToken, Addons: []StayAddon{}}
		if s.Status == "checked_in" || s.Status == "checked_out" {
			bs.UnitName = &s.UnitName
		}
		if bs.Addons, err = stayAddons(ctx, tx, s.ID); err != nil {
			return out, err
		}
		r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
		if err != nil {
			return out, err
		}
		bs.DepositRequired = r.DepositRequired
		if p, err := m.stayPlan(ctx, tx, s); err != nil {
			return out, err
		} else if p != nil {
			bs.RatePlanName, bs.IncludesBreakfast, bs.FreeCancelHours, bs.CancelFeePercent, bs.NonRefundable = p.Name, p.IncludesBreakfast, p.FreeCancelHours,
				p.CancelFeePercent, p.NonRefundable
			if !p.NonRefundable {
				t := s.Start.Add(-time.Duration(p.FreeCancelHours) * time.Hour)
				bs.FreeCancelUntil = &t
			}
		}
		if s.BookingStatus == "confirmed" && s.PackageBooking == nil {
			bs.CanCancel = !bs.NonRefundable
			if fee, _, err := m.cancellationFee(ctx, tx, s, false); err == nil {
				bs.CancelFee = fee.String()
			}
		}
		if s.Status != "expired" && s.Status != "void" {
			total, paid = total.Add(decOf(s.TotalDue)), paid.Add(decOf(s.Paid))
		}
		if s.BookingStatus == "awaiting_payment" {
			due := decOf(r.DepositRequired).Sub(decOf(s.Paid))
			if due.IsPositive() {
				depositDue = depositDue.Add(due)
			}
			if s.HoldExpiresAt != nil && (out.HoldExpiresAt == nil || s.HoldExpiresAt.Before(*out.HoldExpiresAt)) {
				out.HoldExpiresAt = s.HoldExpiresAt
			}
			if s.FolioID != nil {
				p, err := billing.PendingOnline(ctx, tx, *s.FolioID)
				if err != nil {
					return out, err
				}
				if p != nil {
					if pay == nil {
						pay = &BookingPayment{Method: p.MethodType, Amount: "0", QRString: p.QRString, VANumber: p.VANumber, Numbers: []string{},
							Sandbox: p.CheckoutURL != nil && strings.Contains(*p.CheckoutURL, "sandbox.pay.local")}
					}
					pay.Numbers = append(pay.Numbers, p.Number)
					pay.Amount = decOf(pay.Amount).Add(decOf(p.Amount)).String()
				}
			}
		}
		statuses[s.BookingStatus] = true
		out.Stays = append(out.Stays, bs)
	}
	out.Total, out.Paid, out.Balance, out.DepositDue = total.String(), paid.String(), total.Sub(paid).String(), depositDue.String()
	if out.HoldExpiresAt != nil {
		out.HoldSeconds = int(max(out.HoldExpiresAt.Sub(clock.Now()), 0) / time.Second)
	}
	if pay != nil {
		pay.ExpiresAt = out.HoldExpiresAt
		out.Payment = pay
	}
	out.CanPay = statuses["awaiting_payment"] && out.HoldSeconds > 0 && depositDue.IsPositive()
	delete(statuses, "")
	switch {
	case statuses["awaiting_payment"]:
		out.Status = "awaiting_payment"
	case len(statuses) == 1:
		for k := range statuses {
			out.Status = k
		}
	case statuses["in_house"]:
		out.Status = "in_house"
	case statuses["confirmed"]:
		out.Status = "confirmed"
	default:
		out.Status = "mixed"
	}
	return out, nil
}

// AbandonBooking releases the bungalows of a booking whose online payment
// failed: the guest goes back to the cart with the data kept (FR-H36, FR-H99).
func (m *Module) AbandonBooking(ctx context.Context, tx pgx.Tx, property uuid.UUID, token, reason string) (BookingView, error) {
	stays, _, err := m.staysOfToken(ctx, tx, property, token)
	if err != nil {
		return BookingView{}, err
	}
	for _, s := range stays {
		if s.BookingStatus == "awaiting_payment" {
			if err := m.expireStay(ctx, tx, s, nonEmpty(reason, "the online payment failed")); err != nil {
				return BookingView{}, err
			}
		}
	}
	return m.BookingOf(ctx, tx, property, token)
}

// ContinuePayment opens the payment again while the bungalows are still
// held (FR-H43).
func (m *Module) ContinuePayment(ctx context.Context, tx pgx.Tx, property uuid.UUID, token, method string) (BookingView, error) {
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return BookingView{}, err
	}
	if !slices.Contains(methodsOf(pol), method) {
		return BookingView{}, handle.Invalid("method", "invalid_method", "metode pembayaran tidak tersedia")
	}
	v, err := m.BookingOf(ctx, tx, property, token)
	if err != nil {
		return v, err
	}
	if !v.CanPay {
		return v, errs.Conflict("hold_expired", "batas waktu pembayaran sudah lewat: buat reservasi baru")
	}
	stays, _, err := m.staysOfToken(ctx, tx, property, token)
	if err != nil {
		return v, err
	}
	if _, err := m.openPayments(ctx, tx, stays, method, v.BookerName); err != nil {
		return v, err
	}
	return m.BookingOf(ctx, tx, property, token)
}

// CancelByGuest cancels bungalows of a booking under the policy of their
// rate plan; a non-refundable rate cannot be cancelled by the guest and a
// date change goes through the front desk (FR-H42). Refunds follow Finance.
func (m *Module) CancelByGuest(ctx context.Context, tx pgx.Tx, property uuid.UUID, token string, ids []uuid.UUID, reason string) (BookingView, error) {
	stays, _, err := m.staysOfToken(ctx, tx, property, token)
	if err != nil {
		return BookingView{}, err
	}
	done := 0
	for _, s := range stays {
		if len(ids) > 0 && !slices.Contains(ids, s.ID) {
			continue
		}
		switch s.BookingStatus {
		case "awaiting_payment":
			if err := m.expireStay(ctx, tx, s, "cancelled by the guest before paying"); err != nil {
				return BookingView{}, err
			}
			done++
		case "confirmed":
			p, err := m.stayPlan(ctx, tx, s)
			if err != nil {
				return BookingView{}, err
			}
			if p != nil && p.NonRefundable {
				return BookingView{}, errs.Conflict("non_refundable", s.StayNo+": tarif non-refundable tidak dapat dibatalkan")
			}
			if _, err := m.Cancel(ctx, tx, s.ID, CancelInput{Reason: "Dibatalkan tamu: " + nonEmpty(strings.TrimSpace(reason), "tanpa alasan")}); err != nil {
				return BookingView{}, err
			}
			done++
		}
	}
	if done == 0 {
		return BookingView{}, errs.Conflict("not_cancellable", "tidak ada bungalow yang dapat dibatalkan")
	}
	return m.BookingOf(ctx, tx, property, token)
}

// tokenOfLookup finds the link of a booking from its code (group number,
// reservation code or stay number) and the phone or e-mail of the booker.
func (m *Module) tokenOfLookup(ctx context.Context, q dbtx.Querier, property uuid.UUID, code, contact string) (string, error) {
	code, contact = strings.ToUpper(strings.TrimSpace(code)), strings.ToLower(strings.TrimSpace(contact))
	digits := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, contact)
	if len(digits) > 8 {
		digits = digits[len(digits)-8:]
	}
	if code == "" || contact == "" {
		return "", handle.Invalid("code", "required", "kode reservasi dan nomor HP / e-mail wajib diisi")
	}
	match := `(lower(coalesce(%[1]s_email, '')) = $3 OR ($4 <> '' AND right(regexp_replace(coalesce(%[1]s_phone, ''), '\D', '', 'g'), 8) = $4))`
	var token *string
	err := q.QueryRow(ctx, `SELECT g.public_token FROM stay.stay_groups g WHERE g.property_id = $1 AND upper(g.group_no) = $2 AND `+
		fmt.Sprintf(match, "g.contact"), property, code, contact, digits).Scan(&token)
	if err == nil && token != nil {
		return *token, nil
	}
	if err != nil && !dbtx.IsNoRows(err) {
		return "", err
	}
	err = q.QueryRow(ctx, `SELECT coalesce(g.public_token, s.public_token) FROM stay.stays s JOIN reservation.reservations r ON r.id = s.reservation_id
		LEFT JOIN stay.stay_groups g ON g.id = s.group_id LEFT JOIN crm.customers cu ON cu.id = s.customer_id
		WHERE s.property_id = $1 AND s.kind = 'bungalow' AND (upper(s.stay_no) = $2 OR upper(r.code) = $2)
		AND (lower(coalesce(s.guest_email, cu.email, '')) = $3 OR ($4 <> '' AND right(regexp_replace(coalesce(s.guest_phone, cu.phone, ''), '\D', '', 'g'), 8) = $4))
		LIMIT 1`, property, code, contact, digits).Scan(&token)
	if dbtx.IsNoRows(err) || (err == nil && token == nil) {
		return "", errs.NotFound("reservasi dengan kode dan HP / e-mail ini")
	}
	if err != nil {
		return "", err
	}
	return *token, nil
}
