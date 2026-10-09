package stay

// Front office operations (requirements §15, §16, §18, §19, §24, §33): the
// day of the front office (arrivals, departures, in-house guests), the
// reschedule of a stay with availability and price recalculation, the late
// check-out request, Charge to Room and add-ons during the stay, the
// details of a reservation and its audit trail.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/commercial"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/audit"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
	"oneclub/internal/reservation"
)

// FrontOffice is the day of the front office.
type FrontOffice struct {
	Date       string         `json:"date"`
	Arrivals   []Stay         `json:"arrivals"`
	Departures []Stay         `json:"departures"`
	InHouse    []Stay         `json:"inHouse"`
	Counts     map[string]int `json:"counts"`
	Rooms      map[string]int `json:"rooms" doc:"Bungalows per operational status"`
}

// FrontOfficeDay lists the arrivals (and late arrivals), departures (and
// overdue departures) and in-house guests of a local day.
func (m *Module) FrontOfficeDay(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) (FrontOffice, error) {
	loc := calendar.Location(ctx, q)
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	out := FrontOffice{Date: from.Format(time.DateOnly), Counts: map[string]int{}, Rooms: map[string]int{}}
	var err error
	if out.Arrivals, err = handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status IN ('reserved', 'requested')
		AND s.start_at < $3 AND s.end_at > $2 ORDER BY s.vip DESC, s.start_at, s.stay_no`, property, from, to)); err != nil {
		return out, err
	}
	if out.Departures, err = handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		AND s.end_at < $2 ORDER BY s.end_at, s.stay_no`, property, to)); err != nil {
		return out, err
	}
	if out.InHouse, err = handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		ORDER BY b.code`, property)); err != nil {
		return out, err
	}
	out.Counts["arrivals"], out.Counts["departures"], out.Counts["inHouse"] = len(out.Arrivals), len(out.Departures), len(out.InHouse)
	for _, s := range out.Arrivals {
		if s.PaymentStatus != "paid" {
			out.Counts["pendingPayment"]++
		}
		if s.VIP {
			out.Counts["vip"]++
		}
	}
	guests := 0
	for _, s := range out.InHouse {
		guests += s.Adults + s.Children
	}
	out.Counts["inHouseGuests"] = guests
	states, err := m.RoomStates(ctx, q, property, day, nil)
	if err != nil {
		return out, err
	}
	ready := map[uuid.UUID]bool{}
	for _, r := range states {
		out.Rooms[r.OperationalStatus]++
		ready[r.ID] = r.OperationalStatus == "ready"
	}
	for _, s := range out.Arrivals {
		if !s.UnitAssigned || !ready[s.UnitID] {
			out.Counts["pendingPreparation"]++
		}
	}
	return out, nil
}

// ── reschedule (§19) ──────────────────────────────────────────────────────

// RescheduleInput changes a bungalow stay: dates, room type, bungalow,
// guests and rate plan (before check-in), or the departure (in-house).
type RescheduleInput struct {
	ArrivalDate    string     `json:"arrivalDate,omitempty"`
	DepartureDate  string     `json:"departureDate,omitempty"`
	BungalowTypeID *uuid.UUID `json:"bungalowTypeId,omitempty"`
	UnitID         *uuid.UUID `json:"unitId,omitempty"`
	Adults         int        `json:"adults,omitempty"`
	Children       *int       `json:"children,omitempty"`
	RatePlan       string     `json:"ratePlan,omitempty"`
	PromoCode      string     `json:"promoCode,omitempty"`
	Reason         string     `json:"reason"`
}

// Reschedule moves a stay to new dates / type / bungalow / rate with an
// availability check and a new price.
func (m *Module) Reschedule(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in RescheduleInput) (StayResult, error) {
	if err := handle.Required("reason", in.Reason); err != nil {
		return StayResult{}, err
	}
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind != "bungalow" {
		return StayResult{}, errs.Validation("not_bungalow", "reschedule VIP suites and meeting rooms by extending them")
	}
	if s.PackageBooking != nil {
		return StayResult{}, errPackageStay()
	}
	loc := calendar.Location(ctx, tx)
	pol, _, err := m.policy(ctx, tx, s.PropertyID)
	if err != nil {
		return StayResult{}, err
	}
	sa, se := s.Start.In(loc), s.End.In(loc)
	arr := time.Date(sa.Year(), sa.Month(), sa.Day(), 0, 0, 0, 0, loc)
	dep := time.Date(se.Year(), se.Month(), se.Day(), 0, 0, 0, 0, loc)
	if in.ArrivalDate != "" {
		if arr, err = time.ParseInLocation(time.DateOnly, in.ArrivalDate, loc); err != nil {
			return StayResult{}, handle.Invalid("arrivalDate", "invalid_date", "arrivalDate must be YYYY-MM-DD")
		}
	}
	if in.DepartureDate != "" {
		if dep, err = time.ParseInLocation(time.DateOnly, in.DepartureDate, loc); err != nil {
			return StayResult{}, handle.Invalid("departureDate", "invalid_date", "departureDate must be YYYY-MM-DD")
		}
	}
	if !dep.After(arr) {
		return StayResult{}, handle.Invalid("departureDate", "invalid_date", "the departure must be after the arrival")
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	if len(r.Lines) == 0 {
		return StayResult{}, errs.Conflict("no_line", "the reservation has no line")
	}
	main := r.Lines[0]
	segment := "guest"
	if s.CustomerID != nil {
		if seg, err := memberSegment(ctx, tx, s.PropertyID, *s.CustomerID); err == nil && seg != "" {
			segment = seg
		}
	}
	switch s.Status {
	case "checked_in":
		if in.ArrivalDate != "" && arr.Format(time.DateOnly) != time.Date(sa.Year(), sa.Month(), sa.Day(), 0, 0, 0, 0, loc).Format(time.DateOnly) {
			return StayResult{}, errs.Conflict("in_house", "the guest is in-house: only the departure can change")
		}
		if in.UnitID != nil || in.BungalowTypeID != nil {
			return StayResult{}, errs.Conflict("in_house", "move an in-house guest by checking out and in again")
		}
		return m.changeDeparture(ctx, tx, s, main, clockOn(dep, pol.CheckOutTime, loc), segment, in.Reason)
	case "reserved", "requested":
	default:
		return StayResult{}, errs.Conflict("invalid_status", "a "+s.Status+" stay cannot be rescheduled")
	}
	typeID := s.UnitTypeID
	if in.BungalowTypeID != nil {
		typeID = in.BungalowTypeID
	}
	if in.UnitID != nil {
		var t uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT type_id FROM stay.bungalows WHERE id = $1 AND property_id = $2`, *in.UnitID, s.PropertyID).Scan(&t); err != nil {
			return StayResult{}, errs.NotFound("bungalow")
		}
		typeID = &t
	}
	adults, children := s.Adults, s.Children
	if in.Adults > 0 {
		adults = in.Adults
	}
	if in.Children != nil {
		children = *in.Children
	}
	if typeID != nil {
		if err := checkCapacity(ctx, tx, *typeID, adults, children); err != nil {
			return StayResult{}, err
		}
	}
	start, end := clockOn(arr, pol.CheckInTime, loc), clockOn(dep, pol.CheckOutTime, loc)
	// the bungalows to try: the one asked for, else the current one, then the others of the type
	var units []uuid.UUID
	switch {
	case in.UnitID != nil:
		units = []uuid.UUID{*in.UnitID}
	default:
		if typeID != nil && s.UnitTypeID != nil && *typeID == *s.UnitTypeID {
			units = append(units, s.UnitID)
		}
		rows, err := tx.Query(ctx, `SELECT id FROM stay.bungalows WHERE type_id = $1 AND status = 'active' AND archived_at IS NULL AND id <> $2 ORDER BY code`,
			typeID, s.UnitID)
		if err != nil {
			return StayResult{}, err
		}
		more, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return StayResult{}, err
		}
		units = append(units, more...)
	}
	var moved *unit
	for _, uid := range units {
		u, err := m.unit(ctx, tx, "bungalow", uid)
		if err != nil {
			return StayResult{}, err
		}
		if u.Status != "active" {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return StayResult{}, err
		}
		if _, err := m.Res.Reschedule(ctx, sp, r.ID, []reservation.LineChange{{LineID: main.ID, ResourceID: u.ResourceID, Start: start, End: end}}, in.Reason); err != nil {
			_ = sp.Rollback(ctx)
			if de, ok := errs.As(err); ok && (de.Code == "slot_taken" || de.Code == "capacity_full") {
				continue
			}
			return StayResult{}, err
		}
		if err := sp.Commit(ctx); err != nil {
			return StayResult{}, err
		}
		moved = &u
		break
	}
	if moved == nil {
		return StayResult{}, errs.Conflict("sold_out", "no bungalow of this type is available for the new dates")
	}
	// the new price
	plan := s.RatePlan
	if in.RatePlan != "" {
		plan = &in.RatePlan
	}
	pkgCode := ""
	if s.StayPackageID != nil {
		_ = tx.QueryRow(ctx, `SELECT code FROM stay.packages WHERE id = $1`, *s.StayPackageID).Scan(&pkgCode)
	}
	var sq *RoomQuote
	stayPlan := false
	if plan != nil && *plan != "" {
		p, err := ratePlan(ctx, tx, s.PropertyID, *plan)
		if err != nil {
			return StayResult{}, err
		}
		stayPlan = p != nil
	}
	if stayPlan || pkgCode != "" {
		if err := m.reverseAccommodation(ctx, tx, Stay{ID: s.ID}); err != nil {
			return StayResult{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.stay_addons SET voided_at = NULL WHERE stay_id = $1 AND folio_line_id IS NOT NULL`, s.ID); err != nil {
			return StayResult{}, err
		}
		if sq, err = m.quoteRoom(ctx, tx, s.PropertyID, roomRequest{TypeID: *moved.TypeID, RatePlan: deref(plan), PackageCode: pkgCode, Arrival: arr, Departure: dep,
			Adults: adults, Children: children, Segment: segment, CorporateID: s.CorporateAccountID, Source: deref(s.BookingSource),
			PromoCode: nonEmpty(in.PromoCode, deref(s.PromotionCode)), BookedAt: s.CreatedAt, Explicit: true}); err != nil {
			return StayResult{}, err
		}
	}
	if err := m.repriceRoom(ctx, tx, s, main.ID, *moved, sq, segment, deref(plan), start, end); err != nil {
		return StayResult{}, err
	}
	var promo *uuid.UUID
	var promoCode *string
	discount := "0"
	if sq != nil {
		promo, promoCode, discount = sq.PromotionID, sq.PromotionCode, sq.Discount
		if promo != nil && decOf(discount).IsPositive() {
			if _, err := tx.Exec(ctx, `INSERT INTO stay.promotion_redemptions (id, property_id, promotion_id, stay_id, discount) VALUES (gen_random_uuid(),$1,$2,$3,$4::numeric)`,
				s.PropertyID, *promo, s.ID, discount); err != nil {
				return StayResult{}, err
			}
			if _, err := tx.Exec(ctx, `UPDATE stay.promotions SET used_count = used_count + 1 WHERE id = $1`, *promo); err != nil {
				return StayResult{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET start_at = $2, end_at = $3, unit_id = $4, unit_type_id = $5, unit_assigned = $6, adults = $7, children = $8,
		rate_plan = $9, discount = $10::numeric, promotion_id = $11, promotion_code = $12, updated_reason = $13, updated_by = $14, checkin_reminded_at = NULL
		WHERE id = $1`, s.ID, start, end, moved.ID, moved.TypeID, s.UnitAssigned || in.UnitID != nil, adults, children, plan, discount, promo, promoCode,
		in.Reason, actor(ctx)); err != nil {
		return StayResult{}, err
	}
	if err := m.redoDeposit(ctx, tx, s, sq, pol); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "reschedule", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Before: s, After: out.Stay, Reason: in.Reason}); err != nil {
		return out, err
	}
	if err := m.notifyGuest(ctx, tx, out.Stay, "stay.reservation_modified"); err != nil {
		return out, err
	}
	if s.UnitTypeID != nil {
		return out, m.offerWaitlist(ctx, tx, s.PropertyID, *s.UnitTypeID)
	}
	return out, nil
}

// repriceRoom replaces the room charge of a stay: the nightly quote, or
// the room charge posted at booking (voided and posted again).
func (m *Module) repriceRoom(ctx context.Context, tx pgx.Tx, s Stay, lineID uuid.UUID, u unit, sq *RoomQuote, segment, plan string, start, end time.Time) error {
	loc := calendar.Location(ctx, tx)
	var q roomQuote
	if sq != nil {
		gross := decOf(sq.Gross).Sub(decOf(sq.Discount))
		snap, _, err := commercial.Pricer{}.ManualSnapshot(ctx, tx, s.PropertyID, "bungalow", u.PriceItem, segment, decimal.NewFromInt(1), gross, sq.PricingMode,
			nil, start, map[string]any{"source": "accommodation_rates", "ratePlan": sq.RatePlan, "nights": sq.Nights, "reschedule": true})
		if err != nil {
			return err
		}
		q = roomQuote{LineID: lineID, Net: sq.Net, Service: sq.Service, Tax: sq.Tax, Total: sq.Total, Nights: len(sq.Nights), SnapshotID: &snap,
			RevenueComponent: "bungalow", Description: u.Name + " · " + sq.RatePlanName, Shares: nightShares(sq)}
	} else {
		e := end
		pr, err := commercial.Pricer{}.Price(ctx, tx, s.PropertyID, commercial.PriceRequest{ServiceType: "bungalow", ItemRef: u.PriceItem, Segment: segment,
			RatePlan: nonEmpty(plan, "ROOM_ONLY"), Start: start, End: &e, Channel: s.Channel})
		if err != nil {
			return err
		}
		q = roomQuote{LineID: lineID, Net: pr.Net().String(), Service: pr.ServiceAmount().String(), Tax: pr.TaxAmount().String(), Total: pr.Total().String(),
			Nights: nightsBetween(start, end, loc), SnapshotID: pr.SnapshotID, RevenueComponent: nonEmpty(pr.RevenueComponent, "bungalow"),
			Description: u.Name + " · " + nonEmpty(plan, "ROOM_ONLY")}
	}
	if err := m.Res.SetLinePrice(ctx, tx, lineID, q.SnapshotID, decOf(q.Total)); err != nil {
		return err
	}
	if s.RoomPosting == "nightly" {
		raw, _ := json.Marshal(q)
		_, err := tx.Exec(ctx, `UPDATE stay.stays SET room_quote = $2 WHERE id = $1`, s.ID, raw)
		return err
	}
	if s.FolioID == nil {
		return nil
	}
	lines, err := billing.LinesOf(ctx, tx, *s.FolioID)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if l.ReferenceType == "reservation.line" && l.ReferenceID != nil && *l.ReferenceID == lineID {
			if err := m.Billing.VoidCharge(ctx, tx, l.ID, "rescheduled"); err != nil {
				return err
			}
		}
	}
	_, err = m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "reservation.line", ReferenceID: &lineID,
		Description: fmt.Sprintf("%s · %d night(s)", q.Description, q.Nights), Quantity: decimal.NewFromInt(int64(max(q.Nights, 1))), Net: decOf(q.Net),
		Service: decOf(q.Service), Tax: decOf(q.Tax), SnapshotID: q.SnapshotID}, BusinessLine: billing.LineStay, RevenueComponent: q.RevenueComponent})
	return err
}

// redoDeposit recomputes the deposit required of a changed stay.
func (m *Module) redoDeposit(ctx context.Context, tx pgx.Tx, s Stay, sq *RoomQuote, pol Policy) error {
	after, err := m.Get(ctx, tx, s.ID)
	if err != nil {
		return err
	}
	pct := pol.DepositPercent
	if sq != nil {
		if sq.DepositPercent != nil {
			pct = *sq.DepositPercent
		}
		switch sq.PaymentPolicy {
		case "full_prepayment":
			pct = "100"
		case "pay_at_hotel":
			pct = "0"
		}
	}
	deposit := decOf(after.TotalDue).Mul(decOf(pct)).Div(decimal.NewFromInt(100)).Round(0)
	due := clock.Now().Add(24 * time.Hour)
	return m.Res.SetDeposit(ctx, tx, s.ReservationID, deposit, &due)
}

// changeDeparture extends or shortens an in-house stay and reprices it.
func (m *Module) changeDeparture(ctx context.Context, tx pgx.Tx, s Stay, main reservation.Line, newEnd time.Time, segment, reason string) (StayResult, error) {
	if newEnd.Equal(s.End) {
		return m.result(ctx, tx, s.ID)
	}
	loc := calendar.Location(ctx, tx)
	if newEnd.After(s.End) {
		if err := m.Res.ExtendLine(ctx, tx, main.ID, newEnd); err != nil {
			return StayResult{}, err
		}
	} else {
		if !newEnd.After(clock.Now()) {
			return StayResult{}, errs.Validation("in_the_past", "check the guest out instead")
		}
		if s.RoomPosting != "nightly" {
			return StayResult{}, errs.Conflict("posted", "the room was charged at booking: check the guest out early instead")
		}
		if err := m.Res.ShortenLine(ctx, tx, main.ID, newEnd); err != nil {
			return StayResult{}, err
		}
	}
	u, err := m.unit(ctx, tx, "bungalow", s.UnitID)
	if err != nil {
		return StayResult{}, err
	}
	sa := s.Start.In(loc)
	arr := time.Date(sa.Year(), sa.Month(), sa.Day(), 0, 0, 0, 0, loc)
	ne := newEnd.In(loc)
	dep := time.Date(ne.Year(), ne.Month(), ne.Day(), 0, 0, 0, 0, loc)
	var sq *RoomQuote
	if p, err := m.stayPlan(ctx, tx, s); err != nil {
		return StayResult{}, err
	} else if p != nil || s.StayPackageID != nil {
		pkgCode := ""
		if s.StayPackageID != nil {
			_ = tx.QueryRow(ctx, `SELECT code FROM stay.packages WHERE id = $1`, *s.StayPackageID).Scan(&pkgCode)
		}
		if sq, err = m.quoteRoom(ctx, tx, s.PropertyID, roomRequest{TypeID: *u.TypeID, RatePlan: deref(s.RatePlan), PackageCode: pkgCode, Arrival: arr,
			Departure: dep, Adults: s.Adults, Children: s.Children, Segment: segment, CorporateID: s.CorporateAccountID, Source: deref(s.BookingSource),
			PromoCode: deref(s.PromotionCode), BookedAt: s.CreatedAt, Explicit: true}); err != nil {
			return StayResult{}, err
		}
	}
	if s.RoomPosting == "nightly" {
		if err := m.repriceRoom(ctx, tx, s, main.ID, u, sq, segment, deref(s.RatePlan), s.Start, newEnd); err != nil {
			return StayResult{}, err
		}
	} else if s.FolioID != nil {
		// charged at booking: the added nights are charged now
		var add commercial.LinePrice
		ext := s.End
		var net, svc, tax decimal.Decimal
		if sq != nil {
			gross := decimal.Zero
			for _, n := range sq.Nights[min(nightsBetween(s.Start, s.End, loc), len(sq.Nights)):] {
				gross = gross.Add(decOf(n.Price))
			}
			rules, err := commercial.RulesAt(ctx, tx, s.PropertyID, ext)
			if err != nil {
				return StayResult{}, err
			}
			cur, _ := currencyPlaces(ctx, tx)
			b := commercial.CalculateMode(rules, gross, cur, ext, sq.PricingMode)
			net, svc, tax = decOf(b.NetAmount), commercial.SumKind(b.Lines, "service"), commercial.SumKind(b.Lines, "tax")
		} else {
			e := newEnd
			if add, err = (commercial.Pricer{}).Price(ctx, tx, s.PropertyID, commercial.PriceRequest{ServiceType: "bungalow", ItemRef: u.PriceItem, Segment: segment,
				RatePlan: nonEmpty(deref(s.RatePlan), "ROOM_ONLY"), Start: ext, End: &e, Channel: s.Channel}); err != nil {
				return StayResult{}, err
			}
			net, svc, tax = add.Net(), add.ServiceAmount(), add.TaxAmount()
		}
		if _, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "reservation.line",
			ReferenceID: &main.ID, Description: u.Name + " · extension to " + newEnd.In(loc).Format("02 Jan"), Quantity: decimal.NewFromInt(1), Net: net,
			Service: svc, Tax: tax}, BusinessLine: billing.LineStay, RevenueComponent: "bungalow"}); err != nil {
			return StayResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET end_at = $2, updated_reason = $3, updated_by = $4, checkout_reminded_at = NULL WHERE id = $1`,
		s.ID, newEnd, reason, actor(ctx)); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "change_departure", EntityType: "stay.stay", EntityID: s.ID.String(),
		EntityLabel: s.StayNo, PropertyID: &s.PropertyID, Before: s, After: out.Stay, Reason: reason}); err != nil {
		return out, err
	}
	return out, m.notifyGuest(ctx, tx, out.Stay, "stay.reservation_modified")
}

// ── late check-out (§18) ──────────────────────────────────────────────────

// LateCheckoutInput approves a late check-out.
type LateCheckoutInput struct {
	Until  string `json:"until" doc:"HH:MM on the departure day"`
	Reason string `json:"reason,omitempty"`
}

// LateCheckout keeps the bungalow for the guest until the time (checked
// against the next stay); the fee of Stay Policies is charged at check-out.
func (m *Module) LateCheckout(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in LateCheckoutInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Kind != "bungalow" || (s.Status != "checked_in" && s.Status != "reserved") {
		return StayResult{}, errs.Conflict("invalid_status", "late check-out is for reserved or in-house bungalow stays")
	}
	if _, err := time.Parse("15:04", in.Until); err != nil {
		return StayResult{}, handle.Invalid("until", "invalid_time", "until must be HH:MM")
	}
	loc := calendar.Location(ctx, tx)
	d := s.End.In(loc)
	until := clockOn(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), in.Until, loc)
	if !until.After(s.End) {
		return StayResult{}, handle.Invalid("until", "too_early", "the late check-out must be after the check-out time")
	}
	r, err := m.Res.Get(ctx, tx, s.ReservationID, false)
	if err != nil {
		return StayResult{}, err
	}
	cur := s.End
	if s.LateCheckOutUntil != nil {
		cur = *s.LateCheckOutUntil
	}
	if until.After(cur) && len(r.Lines) > 0 {
		if err := m.Res.ExtendLine(ctx, tx, r.Lines[0].ID, until); err != nil {
			return StayResult{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET late_check_out_until = $2, updated_by = $3 WHERE id = $1`, s.ID, until, actor(ctx)); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	if err := audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "late_checkout", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Before: map[string]any{"lateCheckOutUntil": s.LateCheckOutUntil}, After: map[string]any{"lateCheckOutUntil": until},
		Reason: in.Reason}); err != nil {
		return out, err
	}
	return out, m.notifyGuest(ctx, tx, out.Stay, "stay.late_checkout")
}

// ── Charge to Room and add-ons (§13, §24) ─────────────────────────────────

// ChargeInput posts an additional charge to the folio of a stay.
type ChargeInput struct {
	Category      string `json:"category" enum:"fnb,minibar,laundry,transportation,damage,extra_bed,other"`
	Description   string `json:"description"`
	UnitPrice     string `json:"unitPrice"`
	Quantity      int    `json:"quantity,omitempty"`
	Tax           bool   `json:"tax,omitempty" doc:"Apply the tax rules"`
	ServiceCharge bool   `json:"serviceCharge,omitempty" doc:"Apply the service charge"`
	PricingMode   string `json:"pricingMode,omitempty" enum:"nett,plus_plus" doc:"nett: the price includes tax & service"`
}

// ChargeToRoom posts a charge (with its tax and service) to the stay folio.
func (m *Module) ChargeToRoom(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in ChargeInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "checked_in" && s.Status != "reserved" {
		return StayResult{}, errs.Conflict("invalid_status", "charges are posted to reserved or in-house stays")
	}
	if s.FolioID == nil {
		return StayResult{}, errs.Conflict("no_folio", "the stay has no folio")
	}
	if err := handle.Required("description", in.Description); err != nil {
		return StayResult{}, err
	}
	price, err := handle.Decimal("unitPrice", in.UnitPrice, decimal.Zero)
	if err != nil {
		return StayResult{}, err
	}
	if !price.IsPositive() {
		return StayResult{}, handle.Invalid("unitPrice", "invalid", "the price must be positive")
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	component := map[string]string{"fnb": "fnb", "minibar": "fnb", "damage": "damage_charge", "extra_bed": "bungalow"}[in.Category]
	if component == "" {
		component = "other"
	}
	cur, _ := currencyPlaces(ctx, tx)
	rules, err := commercial.RulesAt(ctx, tx, s.PropertyID, clock.Now())
	if err != nil {
		return StayResult{}, err
	}
	var sel []commercial.Rule
	for _, r := range rules {
		if (r.Kind == "tax" && in.Tax) || (r.Kind == "service" && in.ServiceCharge) {
			sel = append(sel, r)
		}
	}
	b := commercial.CalculateMode(sel, price.Mul(decimal.NewFromInt(int64(in.Quantity))), cur, clock.Now(), nonEmpty(in.PricingMode, "nett"))
	lid, err := m.Billing.AddLineCharge(ctx, tx, billing.LineCharge{Charge: billing.Charge{FolioID: *s.FolioID, ReferenceType: "stay.stay", ReferenceID: &s.ID,
		Description: in.Description, Quantity: decimal.NewFromInt(int64(in.Quantity)), Net: decOf(b.NetAmount),
		Service: commercial.SumKind(b.Lines, "service"), Tax: commercial.SumKind(b.Lines, "tax")}, BusinessLine: billing.LineStay, RevenueComponent: component,
		TaxLines: b.Lines})
	if err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "charge_to_room", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, After: map[string]any{"description": in.Description, "category": in.Category, "total": b.Total, "folioLineId": lid,
			"bungalow": s.UnitName}})
}

// StayAddonInput adds an add-on to a stay.
type StayAddonInput struct {
	AddonID  uuid.UUID `json:"addonId"`
	Quantity int       `json:"quantity,omitempty"`
}

// AddAddon charges an add-on to a reserved or in-house stay.
func (m *Module) AddAddon(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in StayAddonInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if s.Status != "checked_in" && s.Status != "reserved" {
		return StayResult{}, errs.Conflict("invalid_status", "add-ons are added to reserved or in-house stays")
	}
	if s.FolioID == nil {
		return StayResult{}, errs.Conflict("no_folio", "the stay has no folio")
	}
	a, err := loadAddon(ctx, tx, s.PropertyID, in.AddonID)
	if err != nil {
		return StayResult{}, err
	}
	if a.Status != "active" {
		return StayResult{}, errs.Conflict("inactive", a.Name+" is not available")
	}
	if s.Status == "checked_in" && a.Availability == "booking" {
		return StayResult{}, errs.Conflict("booking_only", a.Name+" is only sold with a booking")
	}
	l, err := priceAddon(ctx, tx, s.PropertyID, a, in.Quantity, s.Nights, max(s.Adults, 1)+s.Children, decimal.Zero, clock.Now())
	if err != nil {
		return StayResult{}, err
	}
	p := pendingAddon{AddonLine: l, Source: "front_desk"}
	if decOf(l.Total).IsPositive() {
		lid, err := m.chargeAddon(ctx, tx, *s.FolioID, s.ReservationID, l)
		if err != nil {
			return StayResult{}, err
		}
		p.FolioLineID = &lid
	}
	if err := m.insertAddon(ctx, tx, s.PropertyID, s.ID, p); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "add_addon", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, After: l})
}

// VoidAddon removes an add-on of a stay (its folio line is voided).
func (m *Module) VoidAddon(ctx context.Context, tx pgx.Tx, sid, aid uuid.UUID, reason string) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	var line *uuid.UUID
	var voided *time.Time
	if err := tx.QueryRow(ctx, `SELECT folio_line_id, voided_at FROM stay.stay_addons WHERE id = $1 AND stay_id = $2`, aid, sid).Scan(&line, &voided); err != nil {
		if dbtx.IsNoRows(err) {
			return StayResult{}, errs.NotFound("add-on")
		}
		return StayResult{}, err
	}
	if voided == nil {
		if line != nil {
			if err := m.Billing.VoidCharge(ctx, tx, *line, nonEmpty(reason, "add-on removed")); err != nil {
				return StayResult{}, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE stay.stay_addons SET voided_at = now() WHERE id = $1`, aid); err != nil {
			return StayResult{}, err
		}
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: "void_addon", EntityType: "stay.stay", EntityID: s.ID.String(), EntityLabel: s.StayNo,
		PropertyID: &s.PropertyID, Reason: reason, After: map[string]any{"stayAddonId": aid}})
}

// ── details and audit trail (§4, §33) ────────────────────────────────────

// StayUpdateInput changes the details of a stay.
type StayUpdateInput struct {
	GuestName       *string `json:"guestName,omitempty"`
	GuestPhone      *string `json:"guestPhone,omitempty"`
	GuestEmail      *string `json:"guestEmail,omitempty"`
	SpecialRequests *string `json:"specialRequests,omitempty"`
	Notes           *string `json:"notes,omitempty"`
	ExpectedArrival *string `json:"expectedArrival,omitempty"`
	VIP             *bool   `json:"vip,omitempty"`
	BookingSource   *string `json:"bookingSource,omitempty" enum:"website,member_app,guest_app,front_desk,phone,walk_in,corporate"`
	Reason          string  `json:"reason,omitempty"`
}

// UpdateDetails changes the guest contact, requests, notes and flags.
func (m *Module) UpdateDetails(ctx context.Context, tx pgx.Tx, sid uuid.UUID, in StayUpdateInput) (StayResult, error) {
	s, err := m.lock(ctx, tx, sid)
	if err != nil {
		return StayResult{}, err
	}
	if in.BookingSource != nil && !slices.Contains(BookingSources, *in.BookingSource) {
		return StayResult{}, handle.Invalid("bookingSource", "invalid", "unknown booking source")
	}
	if _, err := tx.Exec(ctx, `UPDATE stay.stays SET guest_name = coalesce($2, guest_name), guest_phone = coalesce($3, guest_phone),
		guest_email = coalesce($4, guest_email), special_requests = coalesce($5, special_requests), notes = coalesce($6, notes),
		expected_arrival = coalesce($7, expected_arrival), vip = coalesce($8, vip), booking_source = coalesce($9, booking_source), updated_reason = $10,
		updated_by = $11 WHERE id = $1`, s.ID, in.GuestName, in.GuestPhone, in.GuestEmail, in.SpecialRequests, in.Notes, in.ExpectedArrival, in.VIP,
		in.BookingSource, nzs(in.Reason), actor(ctx)); err != nil {
		return StayResult{}, err
	}
	out, err := m.result(ctx, tx, s.ID)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "stay", Action: audit.ActionUpdate, EntityType: "stay.stay", EntityID: s.ID.String(),
		EntityLabel: s.StayNo, PropertyID: &s.PropertyID, Before: s, After: out.Stay, Reason: in.Reason})
}

// HistoryEntry is one change of a stay (§33).
type HistoryEntry struct {
	At      time.Time      `json:"at"`
	Action  string         `json:"action"`
	Entity  string         `json:"entity"`
	Label   *string        `json:"label"`
	Actor   *string        `json:"actor"`
	Reason  *string        `json:"reason"`
	Source  string         `json:"source" doc:"Module that recorded the change"`
	Changes []FieldChange  `json:"changes"`
	Details map[string]any `json:"details,omitempty"`
}

// FieldChange is a previous and new value.
type FieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

// History returns the audit trail of a stay: the stay, its reservation,
// folio, payments and guest requests.
func (m *Module) History(ctx context.Context, q dbtx.Querier, sid uuid.UUID) ([]HistoryEntry, error) {
	s, err := m.Get(ctx, q, sid)
	if err != nil {
		return nil, err
	}
	folio := ""
	if s.FolioID != nil {
		folio = s.FolioID.String()
	}
	rows, err := q.Query(ctx, `SELECT occurred_at, action, entity_type, entity_label, actor_name, reason, module, before, after, metadata FROM audit.audit_log
		WHERE (entity_type = 'stay.stay' AND entity_id = $1) OR (entity_type = 'reservation.reservation' AND entity_id = $2)
		OR ($3 <> '' AND entity_type = 'billing.folio' AND entity_id = $3)
		OR ($3 <> '' AND entity_type = 'billing.payment' AND entity_id IN (SELECT payment_id::text FROM reporting.eng_payments WHERE folio_id::text = $3))
		OR (entity_type = 'stay.guest_request' AND entity_id IN (SELECT id::text FROM stay.guest_requests WHERE stay_id = $4))
		ORDER BY occurred_at DESC LIMIT 300`, sid.String(), s.ReservationID.String(), folio, sid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		var before, after, meta []byte
		if err := rows.Scan(&h.At, &h.Action, &h.Entity, &h.Label, &h.Actor, &h.Reason, &h.Source, &before, &after, &meta); err != nil {
			return nil, err
		}
		h.Changes = diff(before, after)
		if len(meta) > 2 {
			_ = json.Unmarshal(meta, &h.Details)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// diff lists the top-level fields that changed between two snapshots.
func diff(before, after []byte) []FieldChange {
	var a, b map[string]any
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	out := []FieldChange{}
	skip := map[string]bool{"updatedAt": true, "createdAt": true, "totalDue": true, "paid": true}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		if !skip[k] {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		x, _ := json.Marshal(a[k])
		y, _ := json.Marshal(b[k])
		if string(x) != string(y) {
			out = append(out, FieldChange{Field: k, Before: a[k], After: b[k]})
		}
	}
	return out
}
