package stay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/crm"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/kernel/mask"
	"oneclub/internal/membership"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/platform/numbering"
	"oneclub/internal/reservation"
)

// Stay is a bungalow stay, VIP suite block or meeting room booking.
type Stay struct {
	ID              uuid.UUID        `json:"id" db:"id"`
	PropertyID      uuid.UUID        `json:"propertyId" db:"property_id"`
	StayNo          string           `json:"stayNo" db:"stay_no"`
	Kind            string           `json:"kind" db:"kind" enum:"bungalow,vip_suite,meeting_room"`
	ReservationID   uuid.UUID        `json:"reservationId" db:"reservation_id"`
	ReservationCode string           `json:"reservationCode" db:"reservation_code"`
	CustomerID      *uuid.UUID       `json:"customerId" db:"customer_id"`
	CustomerName    *string          `json:"customerName" db:"customer_name"`
	GuestName       *string          `json:"guestName" db:"guest_name"`
	GuestPhone      *string          `json:"guestPhone" db:"guest_phone"`
	CorporateName   *string          `json:"corporateName" db:"corporate_name"`
	UnitID          uuid.UUID        `json:"unitId" db:"unit_id"`
	UnitName        string           `json:"unitName" db:"unit_name"`
	UnitTypeID      *uuid.UUID       `json:"unitTypeId" db:"unit_type_id"`
	UnitAssigned    bool             `json:"unitAssigned" db:"unit_assigned"`
	Start           time.Time        `json:"start" db:"start_at"`
	End             time.Time        `json:"end" db:"end_at"`
	ActualEnd       *time.Time       `json:"actualEnd" db:"actual_end_at"`
	Adults          int              `json:"adults" db:"adults"`
	Children        int              `json:"children" db:"children"`
	Pax             *int             `json:"pax" db:"pax"`
	Layout          *string          `json:"layout" db:"layout"`
	RatePlan        *string          `json:"ratePlan" db:"rate_plan"`
	PackageCode     *string          `json:"packageCode" db:"package_code"`
	SpecialRequests *string          `json:"specialRequests" db:"special_requests"`
	EventSchedule   []map[string]any `json:"eventSchedule" db:"event_schedule"`
	Catering        []map[string]any `json:"catering" db:"catering"`
	IDType          *string          `json:"idType" db:"id_type"`
	IDNumberMasked  *string          `json:"idNumberMasked" db:"id_number_masked"`
	Status          string           `json:"status" db:"status" enum:"requested,reserved,checked_in,checked_out,cancelled,no_show"`
	CheckedInAt     *time.Time       `json:"checkedInAt" db:"checked_in_at"`
	CheckedOutAt    *time.Time       `json:"checkedOutAt" db:"checked_out_at"`
	FolioID         *uuid.UUID       `json:"folioId" db:"folio_id"`
	Channel         string           `json:"channel" db:"channel"`
	CreatedAt       time.Time        `json:"createdAt" db:"created_at"`
}

const staySelect = `SELECT s.id, s.property_id, s.stay_no, s.kind, s.reservation_id, r.code AS reservation_code, s.customer_id, c.name AS customer_name,
	s.guest_name, s.guest_phone, s.corporate_name, s.unit_id,
	coalesce(b.name, v.name, mr.name, '') AS unit_name, s.unit_type_id, s.unit_assigned, s.start_at, s.end_at, s.actual_end_at, s.adults, s.children,
	s.pax, s.layout, s.rate_plan, s.package_code, s.special_requests, s.event_schedule, s.catering, s.id_type, s.id_number_masked, s.status,
	s.checked_in_at, s.checked_out_at, s.folio_id, s.channel, s.created_at
	FROM stay.stays s JOIN reservation.reservations r ON r.id = s.reservation_id LEFT JOIN reporting.customer_directory c ON c.id = s.customer_id
	LEFT JOIN stay.bungalows b ON b.id = s.unit_id LEFT JOIN stay.vip_suites v ON v.id = s.unit_id LEFT JOIN stay.meeting_rooms mr ON mr.id = s.unit_id`

// Get returns a stay.
func (m *Module) Get(ctx context.Context, q dbtx.Querier, sid uuid.UUID) (Stay, error) {
	rows, err := q.Query(ctx, staySelect+` WHERE s.id = $1`, sid)
	return handle.One[Stay](rows, err, "stay")
}

type GuestInput struct {
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

type EquipmentLine struct {
	EquipmentID uuid.UUID `json:"equipmentId"`
	Quantity    int       `json:"quantity"`
}

type PaymentInput struct {
	Kind       string         `json:"kind,omitempty" enum:"payment,deposit"`
	MethodType string         `json:"methodType"`
	Amount     string         `json:"amount,omitempty" doc:"Default: the deposit required (or the balance)"`
	Reference  string         `json:"reference,omitempty"`
	Tender     map[string]any `json:"tender,omitempty"`
}

// StayInput books a bungalow, VIP suite or meeting room.
type StayInput struct {
	Kind            string           `json:"kind" enum:"bungalow,vip_suite,meeting_room"`
	BungalowTypeID  *uuid.UUID       `json:"bungalowTypeId,omitempty" doc:"Book by type; the unit is assigned automatically"`
	UnitID          *uuid.UUID       `json:"unitId,omitempty" doc:"Bungalow, VIP suite or meeting room"`
	ArrivalDate     string           `json:"arrivalDate,omitempty" doc:"Bungalow: YYYY-MM-DD (check-in time from Stay Policies)"`
	DepartureDate   string           `json:"departureDate,omitempty"`
	Start           *time.Time       `json:"start,omitempty" doc:"VIP suite / meeting room / day-use start"`
	End             *time.Time       `json:"end,omitempty" doc:"Default: start + block or package duration"`
	Adults          int              `json:"adults,omitempty"`
	Children        int              `json:"children,omitempty"`
	RatePlan        string           `json:"ratePlan,omitempty" doc:"Room Only, Long Stay, with breakfast, Day-use"`
	Pax             int              `json:"pax,omitempty"`
	Layout          string           `json:"layout,omitempty" enum:"round_table,classroom,u_shape,theater,boardroom,cocktail"`
	PackageCode     string           `json:"packageCode,omitempty" doc:"Meeting package (HALF_DAY, FULL_DAY, ONE_DAY)"`
	Equipment       []EquipmentLine  `json:"equipment,omitempty"`
	Catering        []map[string]any `json:"catering,omitempty" doc:"[{productId, quantity, serveAt}] scheduled F&B (shown on KDS at serveAt)"`
	EventSchedule   []map[string]any `json:"eventSchedule,omitempty" doc:"Rundown [{time, item}]"`
	CustomerID      *uuid.UUID       `json:"customerId,omitempty"`
	Guest           *GuestInput      `json:"guest,omitempty"`
	CorporateName   string           `json:"corporateName,omitempty"`
	SpecialRequests string           `json:"specialRequests,omitempty"`
	Segment         string           `json:"segment,omitempty"`
	Channel         string           `json:"channel,omitempty" enum:"back_office,ops,member_app,website"`
	Request         bool             `json:"request,omitempty" doc:"VIP suite request from the website, confirmed by staff (FR-WEB-P2-06)"`
	Payment         *PaymentInput    `json:"payment,omitempty"`
}

// StayResult is a stay with its reservation and folio.
type StayResult struct {
	Stay            Stay                    `json:"stay"`
	Reservation     reservation.Reservation `json:"reservation"`
	Folio           *billing.FolioDetail    `json:"folio"`
	Total           string                  `json:"total"`
	DepositRequired string                  `json:"depositRequired"`
}

type unit struct {
	ID         uuid.UUID
	Name       string
	ResourceID *uuid.UUID
	PriceItem  string
	TypeID     *uuid.UUID
	Status     string
	Readiness  string
	BlockHours int
}

func (m *Module) unit(ctx context.Context, q dbtx.Querier, kind string, uid uuid.UUID) (unit, error) {
	var u unit
	var err error
	switch kind {
	case "bungalow":
		err = q.QueryRow(ctx, `SELECT b.id, b.name, b.resource_id, coalesce(t.price_item, t.code), b.type_id, b.status, b.readiness, 0
			FROM stay.bungalows b JOIN stay.bungalow_types t ON t.id = b.type_id WHERE b.id = $1`, uid).
			Scan(&u.ID, &u.Name, &u.ResourceID, &u.PriceItem, &u.TypeID, &u.Status, &u.Readiness, &u.BlockHours)
	case "vip_suite":
		err = q.QueryRow(ctx, `SELECT id, name, resource_id, coalesce(price_item, code), status, block_hours FROM stay.vip_suites WHERE id = $1`, uid).
			Scan(&u.ID, &u.Name, &u.ResourceID, &u.PriceItem, &u.Status, &u.BlockHours)
		u.Readiness = "ready"
	case "meeting_room":
		err = q.QueryRow(ctx, `SELECT id, name, resource_id, coalesce(price_item, code), status FROM stay.meeting_rooms WHERE id = $1`, uid).
			Scan(&u.ID, &u.Name, &u.ResourceID, &u.PriceItem, &u.Status)
		u.Readiness = "ready"
	default:
		return u, handle.Invalid("kind", "invalid_kind", "kind must be bungalow, vip_suite or meeting_room")
	}
	if dbtx.IsNoRows(err) {
		return u, errs.NotFound(strings.ReplaceAll(kind, "_", " "))
	}
	if err == nil && u.ResourceID == nil {
		return u, errs.Conflict("not_bookable", u.Name+" has no bookable resource")
	}
	return u, err
}

func clockOn(d time.Time, hhmm string, loc *time.Location) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), 0, 0, loc)
}

func resolveCustomer(ctx context.Context, tx pgx.Tx, property uuid.UUID, cid *uuid.UUID, g *GuestInput) (*uuid.UUID, string, string, error) {
	if cid != nil {
		p, err := crm.GetCustomer(ctx, tx, *cid)
		if err != nil {
			return nil, "", "", err
		}
		return &p.ID, p.Name, p.Phone, nil
	}
	if g == nil || strings.TrimSpace(g.Name) == "" {
		return nil, "", "", handle.Invalid("guest.name", "required", "guest information is required")
	}
	if g.Phone == "" && g.Email == "" {
		return nil, g.Name, "", nil
	}
	p, _, err := crm.FindOrCreate(ctx, tx, property, crm.Identity{Name: g.Name, Phone: g.Phone, Email: g.Email})
	if err != nil {
		return nil, "", "", err
	}
	return &p.ID, p.Name, g.Phone, nil
}

// Book creates a stay with reservation, rates, folio charges and deposit.
func (m *Module) Book(ctx context.Context, tx pgx.Tx, property uuid.UUID, in StayInput, key string) (StayResult, error) {
	loc := calendar.Location(ctx, tx)
	pol, ref, err := m.policy(ctx, tx, property)
	if err != nil {
		return StayResult{}, err
	}
	if in.Channel == "" {
		in.Channel = "back_office"
	}
	cid, name, phone, err := resolveCustomer(ctx, tx, property, in.CustomerID, in.Guest)
	if err != nil {
		return StayResult{}, err
	}
	segment := in.Segment
	if segment == "" {
		segment = "guest"
		if cid != nil {
			if s, err := membership.Segment(ctx, tx, property, *cid, ""); err != nil {
				return StayResult{}, err
			} else if s != "" {
				segment = s
			}
		}
	}
	var start, end time.Time
	var rp *commercial.RatePlanInfo
	switch in.Kind {
	case "bungalow":
		if in.RatePlan == "" {
			in.RatePlan = "ROOM_ONLY"
		}
		if rp, err = commercial.RatePlan(ctx, tx, property, in.RatePlan); err != nil {
			return StayResult{}, err
		}
		if rp != nil && rp.DayUse {
			if in.Start == nil || in.End == nil {
				return StayResult{}, handle.Invalid("start", "required", "day-use needs start and end")
			}
			start, end = *in.Start, *in.End
			break
		}
		a, err := time.ParseInLocation("2006-01-02", in.ArrivalDate, loc)
		if err != nil {
			return StayResult{}, handle.Invalid("arrivalDate", "invalid_date", "arrivalDate must be YYYY-MM-DD")
		}
		d, err := time.ParseInLocation("2006-01-02", in.DepartureDate, loc)
		if err != nil || !d.After(a) {
			return StayResult{}, handle.Invalid("departureDate", "invalid_date", "departureDate must be after arrivalDate")
		}
		start, end = clockOn(a, pol.CheckInTime, loc), clockOn(d, pol.CheckOutTime, loc)
		if rp != nil && rp.MinNights > 1 && int(d.Sub(a).Hours()/24) < rp.MinNights {
			return StayResult{}, errs.Validation("min_nights", fmt.Sprintf("%s requires at least %d nights", rp.Name, rp.MinNights))
		}
	default:
		if in.Start == nil {
			return StayResult{}, handle.Invalid("start", "required", "start is required")
		}
		start = *in.Start
		if in.End != nil {
			end = *in.End
		}
	}
	// pick the unit
	var units []uuid.UUID
	typeID := in.BungalowTypeID
	switch {
	case in.UnitID != nil:
		units = []uuid.UUID{*in.UnitID}
	case in.Kind == "bungalow" && in.BungalowTypeID != nil:
		rows, err := tx.Query(ctx, `SELECT id FROM stay.bungalows WHERE type_id = $1 AND property_id = $2 AND status = 'active' AND archived_at IS NULL ORDER BY code`,
			*in.BungalowTypeID, property)
		if units, err = collect(rows, err); err != nil {
			return StayResult{}, err
		}
		if len(units) == 0 {
			return StayResult{}, errs.Conflict("no_units", "no bungalow of this type")
		}
	default:
		return StayResult{}, handle.Invalid("unitId", "required", "unitId (or bungalowTypeId) is required")
	}
	var u unit
	var res reservation.Reservation
	booked := false
	for _, uid := range units {
		if u, err = m.unit(ctx, tx, in.Kind, uid); err != nil {
			return StayResult{}, err
		}
		if u.Status != "active" {
			continue
		}
		if in.Kind == "vip_suite" && end.IsZero() {
			end = start.Add(time.Duration(u.BlockHours) * time.Hour)
		}
		if in.Kind == "meeting_room" {
			if err := m.checkLayout(ctx, tx, uid, in.Layout, in.Pax); err != nil {
				return StayResult{}, err
			}
			if end.IsZero() && in.PackageCode != "" {
				var mins *int
				if err := tx.QueryRow(ctx, `SELECT duration_minutes FROM commercial.package_rates WHERE property_id = $1 AND code = $2`, property, in.PackageCode).Scan(&mins); err == nil && mins != nil {
					end = start.Add(time.Duration(*mins) * time.Minute)
				}
			}
		}
		if !end.After(start) {
			return StayResult{}, handle.Invalid("end", "invalid_period", "end must be after start")
		}
		if u.TypeID != nil {
			typeID = u.TypeID
		}
		lines := []reservation.LineRequest{{ResourceID: *u.ResourceID, Start: start, End: end, Description: u.Name}}
		for _, e := range in.Equipment {
			var er *uuid.UUID
			var en string
			if err := tx.QueryRow(ctx, `SELECT resource_id, name FROM stay.equipment WHERE id = $1 AND property_id = $2`, e.EquipmentID, property).Scan(&er, &en); err != nil || er == nil {
				return StayResult{}, errs.Validation("invalid_equipment", "equipment not found")
			}
			lines = append(lines, reservation.LineRequest{ResourceID: *er, Start: start, End: end, Quantity: max(e.Quantity, 1), Description: en})
		}
		attrs := map[string]any{}
		if rp != nil {
			attrs["ratePlan"] = rp.Code
			attrs["facilityAccess"] = rp.FacilityAccess
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return StayResult{}, err
		}
		res, err = m.Res.Book(ctx, sp, property, reservation.BookRequest{BusinessLine: "stay", Channel: in.Channel, CustomerID: cid, GuestName: name,
			GuestPhone: phone, CorporateName: in.CorporateName, Confirm: false, SourceType: "stay.stay", Notes: in.SpecialRequests, Lines: lines,
			Attributes: attrs, PolicyRefs: nil})
		if err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && de.Code == "slot_taken" && len(units) > 1 && strings.HasPrefix(de.Message, "line 1") {
				continue // try the next unit of the type
			}
			return StayResult{}, err
		}
		if err := sp.Commit(ctx); err != nil {
			return StayResult{}, err
		}
		booked = true
		break
	}
	if !booked {
		return StayResult{}, errs.Conflict("sold_out", "no unit of this type is available for the dates")
	}
	// rates and folio
	f, err := m.Billing.OpenLineFolio(ctx, tx, billing.LineFolioInput{FolioInput: billing.FolioInput{Property: property, CustomerID: cid, HolderName: name, SourceType: "reservation", SourceID: &res.ID}, BusinessLine: billing.LineStay, ReservationID: &res.ID, CorporateName: in.CorporateName})
	if err != nil {
		return StayResult{}, err
	}
	if err := m.Res.SetFolio(ctx, tx, res.ID, f.ID); err != nil {
		return StayResult{}, err
	}
	total := decimal.Zero
	charge := func(lineID uuid.UUID, req commercial.PriceRequest, comp, desc string) error {
		pr, err := commercial.Pricer{}.Price(ctx, tx, property, req)
		if err != nil {
			return err
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: f.ID, ReferenceType: "reservation.line", ReferenceID: &lineID, Description: desc, Quantity: decimal.RequireFromString(pr.Units), Net: pr.Net(), Service: pr.ServiceAmount(), Tax: pr.TaxAmount(), SnapshotID: pr.SnapshotID}, BusinessLine: billing.LineStay, RevenueComponent: nonEmpty(pr.RevenueComponent, comp), TaxLines: pr.Tax.Lines}); err != nil {
			return err
		}
		total = total.Add(pr.Total())
		return m.Res.SetLinePrice(ctx, tx, lineID, pr.SnapshotID, pr.Total())
	}
	main := res.Lines[0]
	e := end
	switch in.Kind {
	case "bungalow":
		if err := charge(main.ID, commercial.PriceRequest{ServiceType: "bungalow", ItemRef: u.PriceItem, Segment: segment, RatePlan: in.RatePlan,
			Start: start, End: &e, Channel: in.Channel}, "bungalow", u.Name+" · "+in.RatePlan); err != nil {
			return StayResult{}, err
		}
	case "vip_suite":
		if err := charge(main.ID, commercial.PriceRequest{ServiceType: "vip_suite", ItemRef: u.PriceItem, Segment: segment, Start: start, End: &e,
			Channel: in.Channel}, "vip_suite", u.Name+" block"); err != nil {
			return StayResult{}, err
		}
	case "meeting_room":
		if in.PackageCode != "" {
			if err := charge(main.ID, commercial.PriceRequest{ServiceType: "meeting_package", Package: in.PackageCode, Segment: segment, Start: start,
				Quantity: in.Pax, Channel: in.Channel}, "meeting", u.Name+" · "+in.PackageCode+" · "+fmt.Sprint(in.Pax)+" pax"); err != nil {
				return StayResult{}, err
			}
		} else {
			if err := charge(main.ID, commercial.PriceRequest{ServiceType: "meeting_room", ItemRef: u.PriceItem, Segment: segment, Start: start, End: &e,
				Channel: in.Channel}, "meeting", u.Name+" room rental"); err != nil {
				return StayResult{}, err
			}
		}
		for i, l := range res.Lines[1:] {
			var item string
			if err := tx.QueryRow(ctx, `SELECT coalesce(price_item, code) FROM stay.equipment WHERE id = $1`, in.Equipment[i].EquipmentID).Scan(&item); err != nil {
				return StayResult{}, err
			}
			if err := charge(l.ID, commercial.PriceRequest{ServiceType: "equipment", ItemRef: item, Segment: segment, Start: start, Quantity: l.Quantity,
				Channel: in.Channel}, "equipment", l.ResourceName+" × "+fmt.Sprint(l.Quantity)); err != nil {
				return StayResult{}, err
			}
		}
	}
	// Catering (FR-MTG-06): scheduled F&B orders charged to this folio; they
	// appear on the Kitchen Display at their serve time.
	for i, c := range in.Catering {
		pid, err := uuid.Parse(str(c["productId"]))
		if err != nil {
			return StayResult{}, handle.Invalid(fmt.Sprintf("catering[%d].productId", i), "invalid", "productId is required")
		}
		var outletID uuid.UUID
		if o, err := uuid.Parse(str(c["outletId"])); err == nil {
			outletID = o
		} else if err := tx.QueryRow(ctx, `SELECT id FROM commercial.outlets WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
			ORDER BY (outlet_type = 'banquet') DESC, (outlet_type = 'restaurant') DESC, name LIMIT 1`, property).Scan(&outletID); err != nil {
			return StayResult{}, errs.Conflict("no_outlet", "no outlet to prepare the catering")
		}
		serve := start
		if s := str(c["serveAt"]); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				serve = t
			}
		}
		qty := str(c["quantity"])
		if qty == "" {
			qty = fmt.Sprint(max(in.Pax, 1))
		}
		fid := f.ID
		o, err := m.POS.CreateOrder(ctx, tx, property, commercial.OrderInput{OutletID: outletID, OrderType: "catering", Source: "meeting_catering",
			CustomerID: cid, ServingDestination: "meeting_room", DestinationRef: u.Name, ScheduledFor: &serve, ChargeFolioID: &fid,
			Lines: []commercial.LineInput{{ProductID: pid, Quantity: qty}}, Notes: "Catering " + in.CorporateName})
		if err != nil {
			return StayResult{}, err
		}
		total = total.Add(decimal.RequireFromString(o.Total))
		c["orderId"] = o.ID.String()
	}
	pct, _ := decimal.NewFromString(pol.DepositPercent)
	deposit := total.Mul(pct).Div(decimal.NewFromInt(100)).Round(0)
	due := clock.Now().Add(24 * time.Hour)
	if err := m.Res.SetDeposit(ctx, tx, res.ID, deposit, &due); err != nil {
		return StayResult{}, err
	}
	refs, _ := json.Marshal([]any{ref})
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservations SET policy_refs = $2 WHERE id = $1`, res.ID, refs); err != nil {
		return StayResult{}, err
	}
	// stay record
	no, err := numbering.Next(ctx, tx, property, "STY", clock.Now().In(loc))
	if err != nil {
		return StayResult{}, err
	}
	sid := id.New()
	if in.Adults <= 0 {
		in.Adults = 1
	}
	status := "reserved"
	if in.Request {
		status = "requested"
	}
	eq, _ := json.Marshal(nonNilList(in.EventSchedule))
	cat, _ := json.Marshal(nonNilList(in.Catering))
	var pax *int
	if in.Pax > 0 {
		pax = &in.Pax
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stay.stays (id, property_id, stay_no, kind, reservation_id, customer_id, guest_name, guest_phone, guest_email,
		corporate_name, unit_id, unit_type_id, unit_assigned, start_at, end_at, adults, children, pax, layout, rate_plan, package_code, special_requests,
		event_schedule, catering, status, folio_id, channel, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`,
		sid, property, no, in.Kind, res.ID, cid, nzs(name), nzs(phone), guestEmail(in.Guest), nzs(in.CorporateName), u.ID, typeID,
		in.UnitID != nil, start, end, in.Adults, in.Children, pax, nzs(in.Layout), nzs(in.RatePlan), nzs(in.PackageCode), nzs(in.SpecialRequests),
		eq, cat, status, f.ID, in.Channel, actor(ctx)); err != nil {
		return StayResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservation.reservations SET source_id = $2 WHERE id = $1`, res.ID, sid); err != nil {
		return StayResult{}, err
	}
	if in.Payment != nil {
		amt, err := handle.Decimal("payment.amount", in.Payment.Amount, deposit)
		if err != nil {
			return StayResult{}, err
		}
		if !amt.IsPositive() {
			amt = total
		}
		// P1 payment purpose: a deposit is held until check-out, a payment settles
		kind := "deposit"
		if in.Payment.Kind == "payment" {
			kind = "settlement"
		}
		pk := ""
		if key != "" {
			pk = key + "-pay"
		}
		if _, err := m.Billing.TakeTender(ctx, tx, billing.TenderPaymentInput{PaymentInput: billing.PaymentInput{FolioID: &f.ID, Purpose: kind, MethodType: in.Payment.MethodType, Amount: amt, Reference: in.Payment.Reference}, Tender: in.Payment.Tender, IdempotencyKey: pk}); err != nil {
			return StayResult{}, err
		}
	}
	// a stay is confirmed once the deposit is paid (or none is required); a
	// website VIP suite request waits for staff
	if !in.Request {
		fo, err := billing.GetFolio(ctx, tx, f.ID)
		if err != nil {
			return StayResult{}, err
		}
		paid, _ := decimal.NewFromString(fo.Summary.Payments)
		if paid.GreaterThanOrEqual(deposit) {
			if _, err := m.Res.Confirm(ctx, tx, res.ID, true); err != nil {
				return StayResult{}, err
			}
		}
	}
	out, err := m.result(ctx, tx, sid)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionCreate, EntityType: "stay.stay", EntityID: sid.String(),
		EntityLabel: no + " · " + u.Name, PropertyID: &property, After: out.Stay}); err != nil {
		return out, err
	}
	return out, nil
}

func guestEmail(g *GuestInput) *string {
	if g == nil {
		return nil
	}
	return nzs(g.Email)
}

func nonNilList(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

func nonEmpty(s, d string) string {
	if s == "" || s == "other" {
		return d
	}
	return s
}

func collect(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var x uuid.UUID
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// checkLayout validates pax against the room capacity of the layout (FR-MTG-02).
func (m *Module) checkLayout(ctx context.Context, q dbtx.Querier, room uuid.UUID, layout string, pax int) error {
	if layout == "" {
		return handle.Invalid("layout", "required", "layout is required for a meeting room")
	}
	var capacity int
	err := q.QueryRow(ctx, `SELECT capacity FROM stay.room_layouts WHERE meeting_room_id = $1 AND layout = $2`, room, layout).Scan(&capacity)
	if dbtx.IsNoRows(err) {
		return errs.Validation("layout_unavailable", "this room has no "+strings.ReplaceAll(layout, "_", " ")+" layout")
	}
	if err != nil {
		return err
	}
	if pax > capacity {
		return errs.Validation("over_capacity", fmt.Sprintf("%d pax exceeds the %s capacity of %d", pax, strings.ReplaceAll(layout, "_", " "), capacity),
			errs.Field("pax", "over_capacity", fmt.Sprintf("maximum %d pax", capacity)))
	}
	return nil
}

func (m *Module) result(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (StayResult, error) {
	s, err := m.Get(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	out := StayResult{Stay: s, Reservation: r, DepositRequired: r.DepositRequired}
	if s.FolioID != nil {
		d, err := billing.GetFolio(ctx, tx, *s.FolioID)
		if err != nil {
			return out, err
		}
		out.Folio = &d
		out.Total = d.Folio.Charges
	}
	return out, nil
}

func (m *Module) lock(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (Stay, error) {
	var x uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM stay.stays WHERE id = $1 FOR UPDATE`, sid).Scan(&x); err != nil {
		if dbtx.IsNoRows(err) {
			return Stay{}, errs.NotFound("stay")
		}
		return Stay{}, err
	}
	return m.Get(ctx, tx, sid)
}

func (m *Module) setStatus(ctx context.Context, tx pgx.Tx, before Stay, status, set string, args ...any) (StayResult, error) {
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET status = $2, updated_by = $3`+set+` WHERE id = $1`, append([]any{before.ID, status, actor(ctx)}, args...)...); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, before.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionStatusChange, EntityType: "stay.stay", EntityID: before.ID.String(),
		EntityLabel: before.StayNo, PropertyID: &before.PropertyID, Before: before, After: out.Stay})
}

// ConfirmRequest confirms a requested VIP suite booking (staff).
func (m *Module) ConfirmRequest(ctx context.Context, tx pgx.Tx, sid uuid.UUID) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "requested" {
		return StayResult{}, errs.Conflict("not_requested", "only requested bookings need confirmation")
	}
	if _, err := m.Res.Confirm(ctx, tx, s.ReservationID, true); err != nil {
		return StayResult{}, err
	}
	return m.setStatus(ctx, tx, s, "reserved", "")
}

type CheckInInput struct {
	IDType   string        `json:"idType,omitempty" enum:"ktp,passport,sim,other"`
	IDNumber string        `json:"idNumber,omitempty" doc:"Verified at the desk; stored masked (UU PDP)"`
	Deposit  *PaymentInput `json:"deposit,omitempty"`
}

// CheckIn verifies identity and unit readiness and starts the stay (FR-STY-06/09).
func (m *Module) CheckIn(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CheckInInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" {
		return StayResult{}, errs.Conflict("invalid_status", "only Reserved stays can be checked in (is "+s.Status+")")
	}
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind == "bungalow" {
		u, err := m.unit(ctx, tx, s.Kind, s.UnitID)
		if err != nil {
			return StayResult{}, err
		}
		if pol.RequireReadyUnit && u.Readiness != "ready" {
			return StayResult{}, errs.Conflict("unit_not_ready", u.Name+" is Not Ready; assign another unit or mark it Ready")
		}
		if in.IDNumber == "" {
			return StayResult{}, handle.Invalid("idNumber", "required", "guest identity must be verified at check-in")
		}
	}
	if in.Deposit != nil && s.FolioID != nil {
		amt, err := handle.Decimal("deposit.amount", in.Deposit.Amount, decimal.Zero)
		if err != nil {
			return StayResult{}, err
		}
		if amt.IsPositive() {
			if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: s.FolioID, Purpose: "deposit", MethodType: in.Deposit.MethodType,
				Amount: amt, Reference: in.Deposit.Reference}); err != nil {
				return StayResult{}, err
			}
		}
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	if r.Status == reservation.StatusPending {
		if _, err := m.Res.Confirm(ctx, tx, r.ID, true); err != nil {
			return StayResult{}, err
		}
	}
	if _, err := m.Res.CheckIn(ctx, tx, r.ID); err != nil {
		return StayResult{}, err
	}
	out, err := m.setStatus(ctx, tx, s, "checked_in", `, checked_in_at = now(), id_type = $4, id_number_masked = $5`, nzs(in.IDType), nzs(mask.Phone(in.IDNumber)))
	if err != nil {
		return out, err
	}
	_, err = m.Events.Publish(ctx, tx, "stay.checked_in", "stay.stay", &s.ID, &s.PropertyID, map[string]any{"stayId": s.ID, "kind": s.Kind,
		"unit": s.UnitName, "customerId": s.CustomerID, "reservationId": s.ReservationID})
	return out, err
}

type CheckOutInput struct {
	At *time.Time `json:"at,omitempty" doc:"Actual departure / end; default now"`
}

// CheckOut charges late check-out per Stay Policies, requires the folio to
// be settled, closes it and frees the unit (FR-STY-06, EP-16 AC).
func (m *Module) CheckOut(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CheckOutInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "checked_in" {
		return StayResult{}, errs.Conflict("invalid_status", "only Checked-in stays can be checked out")
	}
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return StayResult{}, err
	}
	at := clock.Now()
	if in.At != nil {
		at = *in.At
	}
	late := at.Sub(s.End) - time.Duration(pol.LateCheckoutGraceMinutes)*time.Minute
	if s.Kind == "bungalow" && late > 0 && s.FolioID != nil {
		hours := int(late.Hours()) + 1
		fee, _ := decimal.NewFromString(pol.LateCheckoutFeePerHour)
		if fee.IsPositive() {
			if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.stay", ReferenceID: &s.ID, Description: fmt.Sprintf("Late check-out %d h", hours), Quantity: decimal.NewFromInt(int64(hours)), Net: fee.Mul(decimal.NewFromInt(int64(hours)))}, BusinessLine: billing.LineStay, RevenueComponent: "late_checkout_fee"}); err != nil {
				return StayResult{}, err
			}
		}
	}
	if s.FolioID != nil {
		f, err := billing.GetFolio(ctx, tx, *s.FolioID)
		if err != nil {
			return StayResult{}, err
		}
		if f.Status == "open" {
			if bal, _ := decimal.NewFromString(f.Balance); !bal.IsZero() {
				return StayResult{}, errs.Conflict("folio_unsettled", "settle the folio before check-out (balance "+bal.StringFixed(0)+")")
			}
			if err := m.Billing.CloseFolio(ctx, tx, f.ID); err != nil {
				return StayResult{}, err
			}
		}
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	if at.Before(s.End) && len(r.Lines) > 0 {
		// early departure / VIP suite actual end: free the rest of the period
		if err := m.Res.ShortenLine(ctx, tx, r.Lines[0].ID, at); err != nil {
			return StayResult{}, err
		}
	}
	if _, err := m.Res.Complete(ctx, tx, r.ID); err != nil {
		return StayResult{}, err
	}
	if s.Kind == "bungalow" {
		if _, err := tx.Exec(ctx, `UPDATE stay.bungalows SET readiness = 'not_ready' WHERE id = $1`, s.UnitID); err != nil {
			return StayResult{}, err
		}
	}
	out, err := m.setStatus(ctx, tx, s, "checked_out", `, checked_out_at = now(), actual_end_at = $4`, at)
	if err != nil {
		return out, err
	}
	_, err = m.Events.Publish(ctx, tx, "stay.checked_out", "stay.stay", &s.ID, &s.PropertyID, map[string]any{"stayId": s.ID, "kind": s.Kind,
		"unit": s.UnitName, "customerId": s.CustomerID, "total": out.Total})
	return out, err
}

type ExtendInput struct {
	Hours int `json:"hours"`
}

// Extend adds overtime to a VIP suite (or meeting room) booking: the
// extension is checked against other bookings and the overtime priced (FR-VIP-03).
func (m *Module) Extend(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in ExtendInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind == "bungalow" {
		return StayResult{}, errs.Validation("not_extendable", "extend a bungalow stay by rescheduling the departure date")
	}
	if s.Status != "reserved" && s.Status != "checked_in" {
		return StayResult{}, errs.Conflict("invalid_status", "the booking is "+s.Status)
	}
	if in.Hours <= 0 || in.Hours > 12 {
		return StayResult{}, handle.Invalid("hours", "invalid_hours", "extend by 1–12 hours")
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	newEnd := s.End.Add(time.Duration(in.Hours) * time.Hour)
	if err := m.Res.ExtendLine(ctx, tx, r.Lines[0].ID, newEnd); err != nil {
		return StayResult{}, err
	}
	u, err := m.unit(ctx, tx, s.Kind, s.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	svc := "vip_suite"
	if s.Kind == "meeting_room" {
		svc = "meeting_room"
	}
	// price the extension: full new period minus what was charged
	var charged string
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(total_amount),0)::text FROM billing.folio_lines WHERE folio_id = $1 AND source_id = $2 AND status = 'posted'`,
		s.FolioID, r.Lines[0].ID).Scan(&charged); err != nil {
		return StayResult{}, err
	}
	var add decimal.Decimal
	var snap *uuid.UUID
	var lines any
	if s.Kind == "vip_suite" {
		pr, err := commercial.Pricer{}.Price(ctx, tx, s.PropertyID, commercial.PriceRequest{ServiceType: svc, ItemRef: u.PriceItem, Start: s.Start, End: &newEnd})
		if err != nil {
			return StayResult{}, err
		}
		before, _ := decimal.NewFromString(charged)
		add, snap, lines = pr.Total().Sub(before), pr.SnapshotID, pr.Tax.Lines
	} else {
		from := s.End
		pr, err := commercial.Pricer{}.Price(ctx, tx, s.PropertyID, commercial.PriceRequest{ServiceType: svc, ItemRef: u.PriceItem, Start: from, End: &newEnd})
		if err != nil {
			return StayResult{}, err
		}
		add, snap, lines = pr.Total(), pr.SnapshotID, pr.Tax.Lines
	}
	if add.IsPositive() {
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "reservation.line", ReferenceID: &r.Lines[0].ID, Description: fmt.Sprintf("Overtime %d h", in.Hours), Quantity: decimal.NewFromInt(int64(in.Hours)), Net: add, SnapshotID: snap}, BusinessLine: billing.LineStay, RevenueComponent: map[string]string{"vip_suite": "vip_suite", "meeting_room": "meeting"}[svc], TaxLines: lines}); err != nil {
			return StayResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET end_at = $2 WHERE id = $1`, s.ID, newEnd); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "extend", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Before: s, After: out.Stay, Metadata: map[string]any{"hours": in.Hours, "charged": add.String()}})
}

type AssignInput struct {
	UnitID uuid.UUID `json:"unitId"`
}

// AssignUnit moves a stay booked by type to a specific unit.
func (m *Module) AssignUnit(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in AssignInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" && s.Status != "requested" {
		return StayResult{}, errs.Conflict("invalid_status", "units can be reassigned before check-in only")
	}
	u, err := m.unit(ctx, tx, s.Kind, in.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	if s.UnitTypeID != nil && u.TypeID != nil && *s.UnitTypeID != *u.TypeID {
		return StayResult{}, errs.Validation("other_type", "the unit must be of the booked type")
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	if _, err := m.Res.Reschedule(ctx, tx, r.ID, []reservation.LineChange{{LineID: r.Lines[0].ID, ResourceID: u.ResourceID, Start: s.Start, End: s.End}},
		"unit assigned"); err != nil {
		return StayResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET unit_id = $2, unit_assigned = true WHERE id = $1`, s.ID, u.ID); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionUpdate, EntityType: "stay.stay", EntityID: s.ID.String(),
		EntityLabel: s.StayNo, PropertyID: &s.PropertyID, Before: s, After: out.Stay})
}

type CancelInput struct {
	Reason   string `json:"reason"`
	WaiveFee bool   `json:"waiveFee,omitempty"`
}

// Cancel cancels per the cancellation policy of the resource type (FR-STY-07).
func (m *Module) Cancel(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in CancelInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" && s.Status != "requested" {
		return StayResult{}, errs.Conflict("invalid_status", "only Reserved stays can be cancelled")
	}
	if _, err := m.Res.Cancel(ctx, tx, s.ReservationID, in.Reason, in.WaiveFee); err != nil {
		return StayResult{}, err
	}
	return m.setStatus(ctx, tx, s, "cancelled", "")
}

// NoShow marks a stay as No-show.
func (m *Module) NoShow(ctx context.Context, tx pgx.Tx, sid uuid.UUID, reason string) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "reserved" {
		return StayResult{}, errs.Conflict("invalid_status", "only Reserved stays can be marked No-show")
	}
	if _, err := m.Res.NoShow(ctx, tx, s.ReservationID, reason); err != nil {
		return StayResult{}, err
	}
	return m.setStatus(ctx, tx, s, "no_show", "")
}

func (m *Module) setReadiness(ctx context.Context, tx pgx.Tx, bid uuid.UUID, readiness string) error {
	var before string
	var pid uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT readiness, property_id FROM stay.bungalows WHERE id = $1 FOR UPDATE`, bid).Scan(&before, &pid); err != nil {
		if dbtx.IsNoRows(err) {
			return errs.NotFound("bungalow")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.bungalows SET readiness = $2, updated_by = $3 WHERE id = $1`, bid, readiness, actor(ctx)); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionStatusChange, EntityType: "stay.bungalow", EntityID: bid.String(),
		PropertyID: &pid, Before: map[string]any{"readiness": before}, After: map[string]any{"readiness": readiness}})
}

// TypeAvailability is the free unit count of a bungalow type per night.
type TypeAvailability struct {
	TypeID   uuid.UUID `json:"typeId"`
	TypeName string    `json:"typeName"`
	Units    int       `json:"units"`
	Nights   []Night   `json:"nights"`
}

// Night is one night of type availability.
type Night struct {
	Date      string `json:"date"`
	Available int    `json:"available"`
}

// BungalowAvailability returns free units per type and night (FR-STY-03).
func (m *Module) BungalowAvailability(ctx context.Context, q dbtx.Querier, property uuid.UUID, from time.Time, nights int) ([]TypeAvailability, error) {
	loc := calendar.Location(ctx, q)
	pol, _, err := m.policy(ctx, q, property)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT t.id, t.name, count(b.id) FROM stay.bungalow_types t LEFT JOIN stay.bungalows b ON b.type_id = t.id AND b.status = 'active'
		AND b.archived_at IS NULL WHERE t.property_id = $1 AND t.status = 'active' AND t.archived_at IS NULL GROUP BY t.id, t.name ORDER BY t.name`, property)
	if err != nil {
		return nil, err
	}
	var out []TypeAvailability
	for rows.Next() {
		var ta TypeAvailability
		if err := rows.Scan(&ta.TypeID, &ta.TypeName, &ta.Units); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, ta)
	}
	rows.Close()
	for i := range out {
		for d := 0; d < nights; d++ {
			day := time.Date(from.Year(), from.Month(), from.Day()+d, 0, 0, 0, 0, loc)
			s, e := clockOn(day, pol.CheckInTime, loc), clockOn(day.AddDate(0, 0, 1), pol.CheckOutTime, loc)
			var busy int
			if err := q.QueryRow(ctx, `SELECT count(DISTINCT b.id) FROM stay.bungalows b JOIN reservation.allocations a ON a.resource_id = b.resource_id
				WHERE b.type_id = $1 AND b.status = 'active' AND a.status IN ('held', 'confirmed') AND a.period && tstzrange($2, $3, '[)')`, out[i].TypeID, s, e).Scan(&busy); err != nil {
				return nil, err
			}
			out[i].Nights = append(out[i].Nights, Night{Date: day.Format("2006-01-02"), Available: out[i].Units - busy})
		}
	}
	if out == nil {
		out = []TypeAvailability{}
	}
	return out, nil
}
