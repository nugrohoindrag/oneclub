package golf

// Booking modification: cancel (FR-BKG-09), reschedule (FR-BKG-08), no-show
// (FR-BKG-10), players and flights (FR-FLT-01/09) and rain checks
// (FR-BKG-11, FR-CHK-12). Every change is recorded in the booking history
// with actor and reason (FR-BKG-12).

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
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/docno"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/approval"
	"oneclub/internal/platform/audit"
	"oneclub/internal/reservation"
)

func lockBooking(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID) (Booking, error) {
	var x uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM golf.bookings WHERE id = $1 AND property_id = $2 FOR UPDATE`, bid, property).Scan(&x)
	if dbtx.IsNoRows(err) {
		return Booking{}, errs.NotFound("booking")
	}
	if err != nil {
		return Booking{}, err
	}
	return GetBooking(ctx, tx, bid)
}

// roundTotal sums the active golf charges of a booking folio (rounds,
// carts, caddy fees) — the base of cancellation and no-show fees.
func (m *Module) golfLines(ctx context.Context, tx pgx.Tx, folioID uuid.UUID) ([]billing.LineRef, decimal.Decimal, error) {
	lines, err := billing.LinesOf(ctx, tx, folioID)
	if err != nil {
		return nil, decimal.Zero, err
	}
	var out []billing.LineRef
	total := decimal.Zero
	for _, l := range lines {
		switch l.ChargeType {
		case "golf_round", "caddy_fee", "cart_fee", "extra_cart":
			out = append(out, l)
			total = total.Add(l.Total)
		}
	}
	return out, total, nil
}

// CancelRequest cancels a booking.
type CancelRequest struct {
	Reason    string `json:"reason"`
	WaiveFee  bool   `json:"waiveFee,omitempty" doc:"Waive the cancellation fee (permission golf.booking.waive_fee, else an approval request)"`
	FreeLimit bool   `json:"-"`
}

// CancelResult reports the money outcome.
type CancelResult struct {
	Booking       Booking          `json:"booking"`
	Fee           string           `json:"fee"`
	FeeWaived     bool             `json:"feeWaived"`
	WaiverPending bool             `json:"waiverPending"`
	Refunds       []billing.Refund `json:"refunds"`
}

// Cancel applies the Cancellation Policy version stored with the booking
// (FR-BKG-09, FR-POL-03 AC).
func (m *Module) Cancel(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, req CancelRequest) (CancelResult, error) {
	var out CancelResult
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return out, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return out, errs.Validation("reason_required", "a reason is required", errs.Field("reason", "required", "reason"))
	}
	if b.PackageBookingID != nil {
		return out, errPackageBooking()
	}
	switch b.Status {
	case "draft":
		if err := m.ReleaseHold(ctx, tx, property, bid, req.Reason); err != nil {
			return out, err
		}
		out.Booking, err = GetBooking(ctx, tx, bid)
		out.Fee, out.Refunds = "0", []billing.Refund{}
		return out, err
	case "pending", "confirmed":
	default:
		return out, errs.Conflict("cannot_cancel", "a "+b.Status+" booking cannot be cancelled")
	}
	pol, err := LoadPoliciesAsOf(ctx, tx, property, b.CreatedAt)
	if err != nil {
		return out, err
	}
	fee := decimal.Zero
	var lines []billing.LineRef
	if b.FolioID != nil {
		var total decimal.Decimal
		if lines, total, err = m.golfLines(ctx, tx, *b.FolioID); err != nil {
			return out, err
		}
		hours := b.StartAt.Sub(clock.Now()).Hours()
		paidAny := false
		if paid, held, err := billing.PaidTowards(ctx, tx, *b.FolioID); err == nil {
			paidAny = paid.IsPositive() || held.IsPositive()
		}
		// Within the free window: full refund. Otherwise the late fee —
		// except for unpaid pending online bookings which simply lapse.
		if hours < float64(pol.Cancellation.FreeCancelHours) && (b.Status != "pending" || paidAny) {
			fee = total.Mul(dec(pol.Cancellation.LateCancelFeePercent)).Div(hundred).Round(0)
		}
	}
	waive := false
	if req.WaiveFee && fee.IsPositive() {
		p := property
		if pr := authzFrom(ctx); pr != nil && pr.Can("golf.booking.waive_fee", &p) {
			waive = true
		} else {
			out.WaiverPending = true
		}
	}
	for _, l := range lines {
		if err := m.Billing.VoidCharge(ctx, tx, l.ID, "booking cancelled: "+req.Reason); err != nil {
			return out, err
		}
	}
	var feeLine uuid.UUID
	if fee.IsPositive() && !waive && b.FolioID != nil {
		if feeLine, err = m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *b.FolioID, ChargeType: "cancellation_fee", Description: "Cancellation fee " + b.Code,
			UnitPrice: fee, Net: fee, Total: fee, ReferenceType: "golf_booking", ReferenceID: &bid}); err != nil {
			return out, err
		}
	}
	if b.FolioID != nil {
		if err := m.Billing.CancelPending(ctx, tx, *b.FolioID, "booking cancelled"); err != nil {
			return out, err
		}
		if out.Refunds, err = m.Billing.RefundFolio(ctx, tx, *b.FolioID, "Booking "+b.Code+" cancelled", m.Refunds); err != nil {
			return out, err
		}
	}
	if out.Refunds == nil {
		out.Refunds = []billing.Refund{}
	}
	if err := reservation.Release(ctx, tx, bid, "cancelled"); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'cancelled', cancelled_at = now(), cancel_reason = $2, updated_by = $3 WHERE id = $1`,
		bid, req.Reason, id.Ptr(actor(ctx))); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET status = 'cancelled' WHERE booking_id = $1 AND status = 'booked'`, bid); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'cancelled' WHERE booking_id = $1`, bid); err != nil {
		return out, err
	}
	if out.WaiverPending && feeLine != uuid.Nil {
		f, _ := fee.Float64()
		if _, _, err := m.Approvals.Submit(ctx, tx, approval.SubmitRequest{DocumentType: CancellationWaiverType.Code, DocumentID: feeLine, DocumentRef: b.Code,
			Title: "Waive cancellation fee " + b.Code, PropertyID: property, Attributes: map[string]any{"fee": f}}); err != nil {
			return out, err
		}
	}
	if b.FolioID != nil {
		if sum, err := billing.FolioSummary(ctx, tx, *b.FolioID); err == nil && !dec(sum.Balance).IsPositive() && !out.WaiverPending {
			if pending, err := billing.PendingRefunds(ctx, tx, *b.FolioID); err == nil && pending == 0 {
				if err := m.Billing.CloseFolio(ctx, tx, *b.FolioID); err != nil && !errs.Is(err, errs.KindConflict) {
					return out, err
				}
			}
		}
	}
	out.Fee, out.FeeWaived = fee.StringFixed(0), waive
	if err := addHistory(ctx, tx, property, bid, "cancelled", map[string]any{"status": b.Status}, map[string]any{"status": "cancelled", "fee": out.Fee,
		"waived": waive, "waiverPending": out.WaiverPending, "policyVersion": pol.Versions[PolicyCancellation]}, req.Reason); err != nil {
		return out, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventBookingCancelled, "golf.booking", &bid, &property, map[string]any{"bookingId": bid, "code": b.Code, "fee": out.Fee}); err != nil {
		return out, err
	}
	refundTotal := decimal.Zero
	for _, r := range out.Refunds {
		refundTotal = refundTotal.Add(dec(r.Amount))
	}
	extra := map[string]any{"reason": req.Reason}
	if fee.IsPositive() && !waive {
		extra["fee"] = "IDR " + fee.StringFixed(0)
	}
	if refundTotal.IsPositive() {
		extra["refund"] = "IDR " + refundTotal.StringFixed(0)
	}
	if err := m.notifyBooking(ctx, tx, property, b, "golf.booking_cancelled", extra); err != nil {
		return out, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking_cancelled", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bid.String()}); err != nil {
		return out, err
	}
	out.Booking, err = GetBooking(ctx, tx, bid)
	if err != nil {
		return out, err
	}
	return out, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "cancel", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
		PropertyID: &property, Reason: req.Reason, Before: map[string]any{"status": b.Status}, After: map[string]any{"status": "cancelled", "fee": out.Fee,
			"refunds": len(out.Refunds), "waived": waive}})
}

// WaiverDecision voids the fee and refunds it when the waiver is approved.
func (m *Module) WaiverDecision(ctx context.Context, tx pgx.Tx, d approval.Decision) error {
	if d.Status != approval.StatusApproved {
		return nil
	}
	ctx = withProperty(ctx, d.PropertyID)
	folio, ferr := billing.FolioOfLine(ctx, tx, d.DocumentID)
	if ferr != nil {
		return ferr
	}
	if st, err := billing.FolioStatus(ctx, tx, folio); err == nil && st == "closed" {
		return nil
	}
	if err := m.Billing.VoidCharge(ctx, tx, d.DocumentID, "cancellation fee waived (approval)"); err != nil {
		return err
	}
	_, err := m.Billing.RefundFolio(ctx, tx, folio, "Cancellation fee waived", m.Refunds)
	return err
}

// RescheduleRequest moves a booking to another tee time.
type RescheduleRequest struct {
	TeeTimeID uuid.UUID `json:"teeTimeId"`
	Reason    string    `json:"reason"`
}

// Reschedule moves every player to the new slot; prices are recomputed
// with new snapshots (old snapshots stay) and the difference is charged or
// refunded (FR-BKG-08).
func (m *Module) Reschedule(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, req RescheduleRequest, staff bool) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "confirmed" && b.Status != "pending" {
		return b, errs.Conflict("cannot_reschedule", "only pending or confirmed bookings can be rescheduled")
	}
	if b.PackageBookingID != nil {
		return b, errPackageBooking()
	}
	if len(b.Flights) != 1 {
		return b, errs.Conflict("group_reschedule", "group bookings are rescheduled per flight by the reservation team")
	}
	pol, err := LoadPoliciesAsOf(ctx, tx, property, b.CreatedAt)
	if err != nil {
		return b, err
	}
	now := clock.Now()
	if !staff {
		if b.RescheduleCount >= pol.Golf.MaxReschedules {
			return b, errs.Conflict("reschedule_limit", fmt.Sprintf("a booking can be rescheduled at most %d times", pol.Golf.MaxReschedules))
		}
		if b.StartAt.Sub(now).Hours() < float64(pol.Golf.RescheduleCutoffHours) {
			return b, errs.Conflict("reschedule_cutoff", fmt.Sprintf("rescheduling closes %d hours before the tee time", pol.Golf.RescheduleCutoffHours))
		}
	}
	if req.TeeTimeID == b.TeeTimeID {
		return b, errs.Validation("same_tee_time", "choose a different tee time", errs.Field("teeTimeId", "invalid", "different tee time"))
	}
	s, err := lockSlot(ctx, tx, property, req.TeeTimeID)
	if err != nil {
		return b, err
	}
	var types []string
	active := []Player{}
	for _, p := range b.Players {
		if p.Status == "booked" {
			active = append(active, p)
			types = append(types, p.PlayerType)
		}
	}
	if err := checkSlotRules(s, pol, b.Channel, types, now, staff); err != nil {
		return b, err
	}
	if len(active) > s.MaxP {
		return b, ErrSlotFull
	}
	var hold *time.Time
	if b.Status == "pending" && b.PaymentDueAt != nil {
		hold = b.PaymentDueAt
	}
	// new seats first: if the slot is full nothing changes
	allocs, err := m.allocateSeats(ctx, tx, property, s, bid, len(active), hold)
	if err != nil {
		return b, err
	}
	var oldAllocs []uuid.UUID
	for _, p := range active {
		var a *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT allocation_id FROM golf.booking_players WHERE id = $1`, p.ID).Scan(&a)
		if a != nil {
			oldAllocs = append(oldAllocs, *a)
		}
	}
	if err := reservation.ReleaseAllocations(ctx, tx, oldAllocs); err != nil {
		return b, err
	}
	var fno int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(flight_no), 0) + 1 FROM golf.flights WHERE tee_time_id = $1`, s.ID).Scan(&fno); err != nil {
		return b, err
	}
	old := b.Flights[0]
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET tee_time_id = $2, flight_no = $3, course_id = $4, play_date = $5 WHERE id = $1`,
		old.ID, s.ID, fno, s.CourseID, s.PlayDate.Format("2006-01-02")); err != nil {
		return b, err
	}
	for i, p := range active {
		if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET allocation_id = $2 WHERE id = $1`, p.ID, allocs[i]); err != nil {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET tee_time_id = $2, course_id = $3, play_date = $4, start_at = $5, reschedule_count = reschedule_count + 1,
		playing_route_id = coalesce($6, playing_route_id), updated_by = $7 WHERE id = $1`, bid, s.ID, s.CourseID, s.PlayDate.Format("2006-01-02"), s.StartAt, s.RouteID,
		id.Ptr(actor(ctx))); err != nil {
		return b, err
	}
	for _, p := range active {
		if err := m.repricePlayer(ctx, tx, property, bid, p.ID, nil, "rescheduled"); err != nil {
			return b, err
		}
	}
	// money: pay the difference or refund the excess
	if b.FolioID != nil {
		sum, err := billing.FolioSummary(ctx, tx, *b.FolioID)
		if err != nil {
			return b, err
		}
		bal := dec(sum.Balance)
		if bal.IsNegative() {
			if _, err := m.Billing.RefundFolio(ctx, tx, *b.FolioID, "Booking "+b.Code+" rescheduled", m.Refunds); err != nil {
				return b, err
			}
		} else if bal.IsPositive() && b.PaymentMode != nil && *b.PaymentMode == "member_charge" {
			var accountID *uuid.UUID
			for _, p := range b.Players {
				if p.MemberID != nil {
					if holder, err := membershipAccountHolder(ctx, tx, *p.MemberID); err == nil && holder != nil {
						if a, err := billing.AccountFor(ctx, tx, property, *holder, "member"); err == nil && a != nil {
							accountID = &a.ID
						}
					}
					break
				}
			}
			if accountID != nil {
				if _, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: b.FolioID, AccountID: accountID, MethodType: "member_account",
					Amount: bal, Description: "Reschedule difference"}); err != nil {
					return b, err
				}
			}
		}
	}
	loc := location(ctx, tx, property)
	if err := addHistory(ctx, tx, property, bid, "rescheduled", map[string]any{"teeTimeId": b.TeeTimeID, "startAt": b.StartAt},
		map[string]any{"teeTimeId": s.ID, "startAt": s.StartAt}, req.Reason); err != nil {
		return b, err
	}
	nb, err := GetBooking(ctx, tx, bid)
	if err != nil {
		return nb, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventBookingRescheduled, "golf.booking", &bid, &property, map[string]any{"bookingId": bid, "code": b.Code,
		"from": b.StartAt, "to": s.StartAt}); err != nil {
		return nb, err
	}
	if err := m.notifyBooking(ctx, tx, property, nb, "golf.booking_rescheduled", map[string]any{"oldTeeTime": b.StartAt.In(loc).Format("Mon 02 Jan 2006 15:04")}); err != nil {
		return nb, err
	}
	for _, d := range []struct {
		c   uuid.UUID
		day string
	}{{b.CourseID, b.PlayDate}, {s.CourseID, s.PlayDate.Format("2006-01-02")}} {
		if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking_rescheduled", d.c, mustDay(d.day), map[string]any{"bookingId": bid.String()}); err != nil {
			return nb, err
		}
	}
	return nb, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "reschedule", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
		PropertyID: &property, Reason: req.Reason, Before: map[string]any{"startAt": b.StartAt}, After: map[string]any{"startAt": s.StartAt}})
}

// NoShow marks a booking No-show and applies the No-show Policy
// (FR-BKG-10). system=true for the automatic run after the grace period.
func (m *Module) NoShow(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, reason string) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "confirmed" && b.Status != "pending" {
		return b, errs.Conflict("cannot_no_show", "only confirmed bookings without check-in can be marked No-show")
	}
	pol, err := LoadPoliciesAsOf(ctx, tx, property, b.CreatedAt)
	if err != nil {
		return b, err
	}
	fee := decimal.Zero
	if b.FolioID != nil {
		lines, total, err := m.golfLines(ctx, tx, *b.FolioID)
		if err != nil {
			return b, err
		}
		fee = total.Mul(dec(pol.Cancellation.NoShowFeePercent)).Div(hundred).Round(0)
		for _, l := range lines {
			if err := m.Billing.VoidCharge(ctx, tx, l.ID, "no-show"); err != nil {
				return b, err
			}
		}
		if fee.IsPositive() {
			if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: *b.FolioID, ChargeType: "no_show_fee", Description: "No-show fee " + b.Code,
				UnitPrice: fee, Net: fee, Total: fee, ReferenceType: "golf_booking", ReferenceID: &bid}); err != nil {
				return b, err
			}
		}
		if err := m.Billing.CancelPending(ctx, tx, *b.FolioID, "no-show"); err != nil {
			return b, err
		}
		if _, err := m.Billing.RefundFolio(ctx, tx, *b.FolioID, "No-show "+b.Code, m.Refunds); err != nil {
			return b, err
		}
	}
	if err := reservation.Release(ctx, tx, bid, "cancelled"); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET status = 'no_show', no_show_at = now(), updated_by = $2 WHERE id = $1`, bid, id.Ptr(actor(ctx))); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET status = 'no_show' WHERE booking_id = $1 AND status = 'booked'`, bid); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.flights SET status = 'cancelled' WHERE booking_id = $1 AND status = 'confirmed'`, bid); err != nil {
		return b, err
	}
	if err := addHistory(ctx, tx, property, bid, "no_show", map[string]any{"status": b.Status}, map[string]any{"status": "no_show", "fee": fee.StringFixed(0)}, reason); err != nil {
		return b, err
	}
	if _, err := m.Events.Publish(ctx, tx, EventBookingNoShow, "golf.booking", &bid, &property, map[string]any{"bookingId": bid, "code": b.Code, "fee": fee.String()}); err != nil {
		return b, err
	}
	extra := map[string]any{}
	if fee.IsPositive() {
		extra["fee"] = "IDR " + fee.StringFixed(0)
	}
	if err := m.notifyBooking(ctx, tx, property, b, "golf.booking_no_show", extra); err != nil {
		return b, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "booking_no_show", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bid.String()}); err != nil {
		return b, err
	}
	nb, err := GetBooking(ctx, tx, bid)
	if err != nil {
		return nb, err
	}
	return nb, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "no_show", EntityType: "golf.booking", EntityID: bid.String(), EntityLabel: b.Code,
		PropertyID: &property, Reason: reason, After: map[string]any{"status": "no_show", "fee": fee.String()}})
}

// ── players & flights ─────────────────────────────────────────────────────

// AddPlayer adds a player to a booking (Assign Player).
func (m *Module) AddPlayer(ctx context.Context, tx pgx.Tx, property, bid uuid.UUID, in PlayerInput, flightID *uuid.UUID) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	if b.Status != "confirmed" && b.Status != "pending" && b.Status != "checked_in" {
		return b, errs.Conflict("booking_closed", "players can only be added to active bookings")
	}
	pol, err := LoadPolicies(ctx, tx, property, clock.Now())
	if err != nil {
		return b, err
	}
	fl := b.Flights[0]
	if flightID != nil {
		found := false
		for _, f := range b.Flights {
			if f.ID == *flightID {
				fl, found = f, true
			}
		}
		if !found {
			return b, errs.Validation("invalid_flight", "flight not in this booking", errs.Field("flightId", "invalid", "flight of this booking"))
		}
	}
	s, err := lockSlot(ctx, tx, property, fl.TeeTimeID)
	if err != nil {
		return b, err
	}
	n := 0
	var members []PlayerInput
	for _, p := range b.Players {
		if p.FlightID == fl.ID && (p.Status == "booked" || p.Status == "checked_in") {
			n++
			if p.PlayerType == "member" && p.MemberID != nil {
				members = append(members, PlayerInput{PlayerType: "member", MemberID: p.MemberID})
			}
		}
	}
	if n+1 > min(pol.Golf.MaxPlayers, s.MaxP) {
		return b, errs.Validation("too_many_players", "the flight is full", errs.Field("players", "too_large", "flight is full"))
	}
	// validate with the existing members as possible hosts
	all := append(append([]PlayerInput{}, members...), in)
	if in.PlayerType == "guest_of_member" && in.HostIndex == nil && len(members) > 0 {
		zero := 0
		all[len(all)-1].HostIndex = &zero
	}
	resolved, err := m.resolvePlayers(ctx, tx, property, s.PlayDate, s, pol, all, true)
	if err != nil {
		return b, err
	}
	rp := resolved[len(resolved)-1]
	var hold *time.Time
	if b.Status == "pending" {
		hold = b.PaymentDueAt
	}
	allocs, err := m.allocateSeats(ctx, tx, property, s, bid, 1, hold)
	if err != nil {
		return b, err
	}
	pid := id.New()
	seq := len(b.Players) + 1
	elig, _ := json.Marshal(rp.eligibility)
	ent, _ := json.Marshal(rp.entitlement)
	if _, err := tx.Exec(ctx, `INSERT INTO golf.booking_players (id, property_id, booking_id, flight_id, seq, player_type, segment, customer_id, guest_id, member_id,
		membership_id, name, phone, tba, reciprocal_club, eligibility, entitlement, allocation_id, handicap_index, status, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19::numeric,'booked',$20,$20)`,
		pid, property, bid, fl.ID, seq, rp.playerType, rp.segments[0], rp.customerID, rp.guestID, rp.memberID, rp.membership, rp.name, nullStr(rp.phone),
		in.TBA, nullStr(in.ReciprocalClub), elig, ent, allocs[0], rp.handicap, id.Ptr(actor(ctx))); err != nil {
		return b, err
	}
	if b.Status != "pending" {
		if _, err := reservation.ConfirmReservation(ctx, tx, bid); err != nil {
			return b, err
		}
	}
	if b.FolioID != nil {
		loc := location(ctx, tx, property)
		pp, need, err := m.pricePlayer(ctx, tx, property, s, b.PlayingRouteID, b.Channel, rp, bid, loc)
		if err != nil {
			return b, err
		}
		lid, err := m.postRound(ctx, tx, *b.FolioID, pid, rp.name, pp)
		if err != nil {
			return b, err
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET segment = $2, pricing_snapshot_id = $3, folio_line_id = $4, price_total = $5::numeric WHERE id = $1`,
			pid, pp.result.Segment, pp.snapshotID, lid, pp.result.Total); err != nil {
			return b, err
		}
		if need != nil {
			if err := m.submitOverride(ctx, tx, property, bid, b.Code, pid, *need); err != nil {
				return b, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET player_count = player_count + 1 WHERE id = $1`, bid); err != nil {
		return b, err
	}
	if err := addHistory(ctx, tx, property, bid, "player_added", nil, map[string]any{"playerId": pid, "name": rp.name, "type": rp.playerType}, ""); err != nil {
		return b, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "player_added", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bid.String()}); err != nil {
		return b, err
	}
	return GetBooking(ctx, tx, bid)
}

// PlayerPatch completes a TBA player or verifies a reciprocal member.
type PlayerPatch struct {
	Name               *string    `json:"name,omitempty"`
	Phone              *string    `json:"phone,omitempty"`
	CustomerID         *uuid.UUID `json:"customerId,omitempty"`
	ReciprocalClub     *string    `json:"reciprocalClub,omitempty"`
	ReciprocalVerified *bool      `json:"reciprocalVerified,omitempty"`
	HandicapIndex      *string    `json:"handicapIndex,omitempty"`
}

// UpdatePlayer edits a player (FR-FLT-09 complete TBA before check-in).
func (m *Module) UpdatePlayer(ctx context.Context, tx pgx.Tx, property, bid, pid uuid.UUID, req PlayerPatch) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	var before Player
	for _, p := range b.Players {
		if p.ID == pid {
			before = p
		}
	}
	if before.ID == uuid.Nil {
		return b, errs.NotFound("player")
	}
	sets := []string{}
	args := []any{pid}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		add("name", strings.TrimSpace(*req.Name))
		add("tba", false)
	}
	if req.Phone != nil {
		add("phone", nullStr(*req.Phone))
	}
	if req.CustomerID != nil {
		ok, err := customerExists(ctx, tx, property, *req.CustomerID)
		if err != nil {
			return b, err
		}
		if !ok {
			return b, errs.Validation("customer_not_found", "customer not found", errs.Field("customerId", "not_found", "customer not found"))
		}
		add("customer_id", *req.CustomerID)
		add("tba", false)
	}
	if req.ReciprocalClub != nil {
		add("reciprocal_club", nullStr(*req.ReciprocalClub))
	}
	if req.ReciprocalVerified != nil {
		if before.PlayerType != "reciprocal" {
			return b, errs.Validation("not_reciprocal", "only reciprocal members are verified", errs.Field("reciprocalVerified", "invalid", "reciprocal players only"))
		}
		add("reciprocal_verified", *req.ReciprocalVerified)
	}
	if req.HandicapIndex != nil {
		h, err := decimal.NewFromString(*req.HandicapIndex)
		if err != nil || h.LessThan(decimal.NewFromInt(-10)) || h.GreaterThan(decimal.NewFromInt(54)) {
			return b, errs.Validation("invalid_handicap", "handicap index must be between -10 and 54", errs.Field("handicapIndex", "invalid", "-10 … 54"))
		}
		add("handicap_index", h.String())
	}
	if len(sets) == 0 {
		return b, nil
	}
	args = append(args, id.Ptr(actor(ctx)))
	sets = append(sets, fmt.Sprintf("updated_by = $%d", len(args)))
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		return b, err
	}
	if err := addHistory(ctx, tx, property, bid, "player_updated", map[string]any{"playerId": pid, "name": before.Name}, req, ""); err != nil {
		return b, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "player_updated", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bid.String()}); err != nil {
		return b, err
	}
	return GetBooking(ctx, tx, bid)
}

// RemovePlayer removes a player (seat released, charge voided, any excess
// payment refunded).
func (m *Module) RemovePlayer(ctx context.Context, tx pgx.Tx, property, bid, pid uuid.UUID, reason string) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bid)
	if err != nil {
		return b, err
	}
	active := 0
	var target *Player
	for i, p := range b.Players {
		if p.Status == "booked" || p.Status == "checked_in" {
			active++
		}
		if p.ID == pid {
			target = &b.Players[i]
		}
	}
	if target == nil {
		return b, errs.NotFound("player")
	}
	if target.Status != "booked" {
		return b, errs.Conflict("player_not_removable", "only booked (not checked-in) players can be removed")
	}
	if active <= 1 {
		return b, errs.Conflict("last_player", "cancel the booking instead of removing its last player")
	}
	var alloc, line *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT allocation_id, folio_line_id FROM golf.booking_players WHERE id = $1`, pid).Scan(&alloc, &line); err != nil {
		return b, err
	}
	if alloc != nil {
		if err := reservation.ReleaseAllocations(ctx, tx, []uuid.UUID{*alloc}); err != nil {
			return b, err
		}
	}
	if line != nil {
		if err := m.Billing.VoidCharge(ctx, tx, *line, "player removed: "+reason); err != nil {
			return b, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET status = 'removed', updated_by = $2 WHERE id = $1`, pid, id.Ptr(actor(ctx))); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET player_count = player_count - 1 WHERE id = $1`, bid); err != nil {
		return b, err
	}
	if b.FolioID != nil {
		if _, err := m.Billing.RefundFolio(ctx, tx, *b.FolioID, "Player removed from "+b.Code, m.Refunds); err != nil {
			return b, err
		}
	}
	if err := addHistory(ctx, tx, property, bid, "player_removed", map[string]any{"playerId": pid, "name": target.Name}, nil, reason); err != nil {
		return b, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "player_removed", b.CourseID, mustDay(b.PlayDate), map[string]any{"bookingId": bid.String()}); err != nil {
		return b, err
	}
	return GetBooking(ctx, tx, bid)
}

// MovePlayer moves a player to another flight in the same or another slot
// of the day (FR-FLT-01), keeping the price snapshot.
func (m *Module) MovePlayer(ctx context.Context, tx pgx.Tx, property, flightID, pid uuid.UUID) (Booking, error) {
	var bid, fromFlight uuid.UUID
	var status string
	var alloc *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT booking_id, flight_id, status, allocation_id FROM golf.booking_players WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		pid, property).Scan(&bid, &fromFlight, &status, &alloc); err != nil {
		if dbtx.IsNoRows(err) {
			return Booking{}, errs.NotFound("player")
		}
		return Booking{}, err
	}
	if fromFlight == flightID {
		return GetBooking(ctx, tx, bid)
	}
	if status != "booked" && status != "checked_in" {
		return Booking{}, errs.Conflict("player_inactive", "the player is not active")
	}
	var toTT, fromTT uuid.UUID
	var toStatus string
	if err := tx.QueryRow(ctx, `SELECT tee_time_id, status FROM golf.flights WHERE id = $1 AND property_id = $2`, flightID, property).Scan(&toTT, &toStatus); err != nil {
		return Booking{}, errs.NotFound("flight")
	}
	if toStatus == "in_play" || toStatus == "completed" || toStatus == "cancelled" {
		return Booking{}, errs.Conflict("flight_closed", "the target flight is no longer open")
	}
	if err := tx.QueryRow(ctx, `SELECT tee_time_id FROM golf.flights WHERE id = $1`, fromFlight).Scan(&fromTT); err != nil {
		return Booking{}, err
	}
	s, err := lockSlot(ctx, tx, property, toTT)
	if err != nil {
		return Booking{}, err
	}
	var inFlight int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.booking_players WHERE flight_id = $1 AND status IN ('booked','checked_in')`, flightID).Scan(&inFlight); err != nil {
		return Booking{}, err
	}
	if inFlight+1 > s.MaxP {
		return Booking{}, errs.Conflict("flight_full", "the target flight is full")
	}
	if toTT != fromTT {
		allocs, err := m.allocateSeats(ctx, tx, property, s, bid, 1, nil)
		if err != nil {
			return Booking{}, err
		}
		if alloc != nil {
			if err := reservation.ReleaseAllocations(ctx, tx, []uuid.UUID{*alloc}); err != nil {
				return Booking{}, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET allocation_id = $2 WHERE id = $1`, pid, allocs[0]); err != nil {
			return Booking{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.booking_players SET flight_id = $2, updated_by = $3 WHERE id = $1`, pid, flightID, id.Ptr(actor(ctx))); err != nil {
		return Booking{}, err
	}
	if err := addHistory(ctx, tx, property, bid, "player_moved", map[string]any{"flightId": fromFlight}, map[string]any{"flightId": flightID}, ""); err != nil {
		return Booking{}, err
	}
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "player_moved", s.CourseID, s.PlayDate, map[string]any{"bookingId": bid.String()}); err != nil {
		return Booking{}, err
	}
	return GetBooking(ctx, tx, bid)
}

// CreateFlight adds an empty flight for a booking in a slot (Create Flight).
func (m *Module) CreateFlight(ctx context.Context, tx pgx.Tx, property, teeTimeID uuid.UUID, bookingID *uuid.UUID) (uuid.UUID, error) {
	s, err := lockSlot(ctx, tx, property, teeTimeID)
	if err != nil {
		return uuid.Nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM golf.flights WHERE tee_time_id = $1 AND status <> 'cancelled'`, s.ID).Scan(&n); err != nil {
		return uuid.Nil, err
	}
	if n >= s.Flights {
		return uuid.Nil, errs.Conflict("slot_flights_full", fmt.Sprintf("this tee time holds at most %d flights", s.Flights))
	}
	fid := id.New()
	var fno int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(flight_no), 0) + 1 FROM golf.flights WHERE tee_time_id = $1`, s.ID).Scan(&fno); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golf.flights (id, property_id, tee_time_id, booking_id, course_id, play_date, flight_no, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'confirmed')`, fid, property, s.ID, bookingID, s.CourseID, s.PlayDate.Format("2006-01-02"), fno); err != nil {
		return uuid.Nil, err
	}
	return fid, notifyRealtime(ctx, tx, property, "golf.tee_sheet", "flight_created", s.CourseID, s.PlayDate, nil)
}

// ── rain checks ───────────────────────────────────────────────────────────

// RainCheck is the API view of a rain check.
type RainCheck struct {
	ID              uuid.UUID  `json:"id"`
	Number          string     `json:"number"`
	BookingID       uuid.UUID  `json:"bookingId"`
	BookingCode     string     `json:"bookingCode"`
	PlayerID        uuid.UUID  `json:"playerId"`
	PlayerName      string     `json:"playerName"`
	CustomerID      *uuid.UUID `json:"customerId"`
	HolesPlayed     int        `json:"holesPlayed"`
	HolesTotal      int        `json:"holesTotal"`
	CreditPercent   string     `json:"creditPercent"`
	CreditAmount    string     `json:"creditAmount"`
	Currency        string     `json:"currency"`
	ExpiresOn       string     `json:"expiresOn"`
	Status          string     `json:"status" enum:"issued,redeemed,expired,cancelled"`
	RedeemedBooking *uuid.UUID `json:"redeemedBookingId"`
	RedeemedAt      *time.Time `json:"redeemedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

const rainCheckCols = `r.id, r.number, r.booking_id, b.code, r.booking_player_id, bp.name, r.customer_id, r.holes_played, r.holes_total, r.credit_percent::text,
	r.credit_amount::text, r.currency, r.expires_on, CASE WHEN r.status = 'issued' AND r.expires_on < current_date THEN 'expired' ELSE r.status END,
	r.redeemed_booking_id, r.redeemed_at, r.created_at
	FROM golf.rain_checks r JOIN golf.bookings b ON b.id = r.booking_id JOIN golf.booking_players bp ON bp.id = r.booking_player_id`

func scanRainCheck(row pgx.Row) (RainCheck, error) {
	var r RainCheck
	var exp time.Time
	err := row.Scan(&r.ID, &r.Number, &r.BookingID, &r.BookingCode, &r.PlayerID, &r.PlayerName, &r.CustomerID, &r.HolesPlayed, &r.HolesTotal,
		&r.CreditPercent, &r.CreditAmount, &r.Currency, &exp, &r.Status, &r.RedeemedBooking, &r.RedeemedAt, &r.CreatedAt)
	r.ExpiresOn = exp.Format("2006-01-02")
	return r, err
}

// IssueRainChecks issues rain checks for the players of flights stopped by
// the weather (FR-BKG-11 / FR-CHK-12 batch).
func (m *Module) IssueRainChecks(ctx context.Context, tx pgx.Tx, property uuid.UUID, flights map[uuid.UUID]int) ([]RainCheck, error) {
	out := []RainCheck{}
	for fid, holes := range flights {
		var holesTotal int
		var day time.Time
		var bookingID *uuid.UUID
		var status string
		var route *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT f.booking_id, f.play_date, f.status, t.playing_route_id FROM golf.flights f JOIN golf.tee_times t ON t.id = f.tee_time_id
			WHERE f.id = $1 AND f.property_id = $2 FOR UPDATE OF f`, fid, property).Scan(&bookingID, &day, &status, &route); err != nil {
			return nil, errs.NotFound("flight")
		}
		if status != "in_play" && status != "completed" && status != "checked_in" && status != "ready" && status != "on_hold" {
			return nil, errs.Conflict("flight_not_started", "rain checks are issued for checked-in flights only")
		}
		if bookingID == nil {
			continue
		}
		holesTotal = 18
		var br *uuid.UUID
		_ = tx.QueryRow(ctx, `SELECT playing_route_id FROM golf.bookings WHERE id = $1`, *bookingID).Scan(&br)
		if br == nil {
			br = route
		}
		if br != nil {
			_ = tx.QueryRow(ctx, `SELECT hole_count FROM golf.playing_routes WHERE id = $1`, *br).Scan(&holesTotal)
		}
		if holes < 0 || holes > holesTotal {
			return nil, errs.Validation("invalid_holes", fmt.Sprintf("holes played must be 0–%d", holesTotal), errs.Field("holesPlayed", "invalid", "within the route"))
		}
		var created time.Time
		if err := tx.QueryRow(ctx, `SELECT created_at FROM golf.bookings WHERE id = $1`, *bookingID).Scan(&created); err != nil {
			return nil, err
		}
		pol, err := LoadPoliciesAsOf(ctx, tx, property, created)
		if err != nil {
			return nil, err
		}
		pct := decimal.Zero
		for _, r := range pol.Weather.Rules {
			if holes <= r.MaxHolesPlayed {
				pct = dec(r.CreditPercent)
				break
			}
		}
		rows, err := tx.Query(ctx, `SELECT id, customer_id, coalesce(price_total, 0)::text FROM golf.booking_players WHERE flight_id = $1 AND status = 'checked_in'
			AND NOT EXISTS (SELECT 1 FROM golf.rain_checks rc WHERE rc.booking_player_id = golf.booking_players.id)`, fid)
		if err != nil {
			return nil, err
		}
		type pl struct {
			id    uuid.UUID
			cust  *uuid.UUID
			price decimal.Decimal
		}
		var players []pl
		for rows.Next() {
			var x pl
			var p string
			if err := rows.Scan(&x.id, &x.cust, &p); err != nil {
				rows.Close()
				return nil, err
			}
			x.price = dec(p)
			players = append(players, x)
		}
		rows.Close()
		expires := localDay(clock.Now(), location(ctx, tx, property)).AddDate(0, 0, max(pol.Weather.ValidityDays, 1))
		for _, x := range players {
			credit := x.price.Mul(pct).Div(hundred).Round(0)
			num, err := docno.Daily(ctx, tx, "golf.sequences", property, "RC", day)
			if err != nil {
				return nil, err
			}
			rid := id.New()
			if _, err := tx.Exec(ctx, `INSERT INTO golf.rain_checks (id, property_id, number, booking_id, booking_player_id, customer_id, holes_played, holes_total,
				credit_percent, credit_amount, currency, expires_on, status, policy_version, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,'IDR',$11::date,'issued',$12,$13)`,
				rid, property, num, *bookingID, x.id, x.cust, holes, holesTotal, pct.String(), credit.String(), expires.Format("2006-01-02"),
				pol.Versions[PolicyWeather], id.Ptr(actor(ctx))); err != nil {
				return nil, err
			}
			rc, err := scanRainCheck(tx.QueryRow(ctx, `SELECT `+rainCheckCols+` WHERE r.id = $1`, rid))
			if err != nil {
				return nil, err
			}
			out = append(out, rc)
			if err := addHistory(ctx, tx, property, *bookingID, "rain_check_issued", nil, map[string]any{"number": num, "credit": credit.String(), "holesPlayed": holes}, ""); err != nil {
				return nil, err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "issue_rain_check", EntityType: "golf.rain_check", EntityID: rid.String(),
				EntityLabel: num, PropertyID: &property, After: rc}); err != nil {
				return nil, err
			}
		}
		// play stopped: the flight ends here
		if status == "in_play" {
			if err := m.finishFlight(ctx, tx, property, fid, &holes); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// redeemRainCheck credits an issued rain check on a new booking.
func (m *Module) redeemRainCheck(ctx context.Context, tx pgx.Tx, property, rcID, bookingID, folioID uuid.UUID, customerID *uuid.UUID) error {
	var status, number, credit string
	var expires time.Time
	var owner *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT status, number, credit_amount::text, expires_on, customer_id FROM golf.rain_checks WHERE id = $1 AND property_id = $2 FOR UPDATE`,
		rcID, property).Scan(&status, &number, &credit, &expires, &owner); err != nil {
		return errs.Validation("rain_check_not_found", "rain check not found", errs.Field("rainCheckIds", "not_found", "rain check not found"))
	}
	if status != "issued" || expires.Before(localDay(clock.Now(), location(ctx, tx, property))) {
		return errs.Conflict("rain_check_unusable", "rain check "+number+" is "+status+" or expired")
	}
	if owner != nil && customerID != nil && *owner != *customerID {
		return errs.Conflict("rain_check_owner", "rain check "+number+" belongs to another customer")
	}
	amt := dec(credit)
	if amt.IsPositive() {
		if _, err := m.Billing.AddCharge(ctx, tx, billing.Charge{FolioID: folioID, ChargeType: "rain_check_credit", Description: "Rain Check " + number,
			UnitPrice: amt.Neg(), Net: amt.Neg(), Total: amt.Neg(), ReferenceType: "golf_rain_check", ReferenceID: &rcID}); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE golf.rain_checks SET status = 'redeemed', redeemed_booking_id = $2, redeemed_at = now() WHERE id = $1`, rcID, bookingID)
	return err
}
