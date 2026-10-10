package stay

// Rules of a bungalow booking (docs/requirement-booking-hotel-mgcc.md):
// the booking window and "today" in the club's time zone (FR-H09, FR-H94),
// the restrictions per date — stop sale, closed to arrival / departure,
// minimum nights, per channel (FR-H65) — and the supervisor actions that
// pass a rule with a reason in the audit trail (FR-H91): book on a closed
// date, a manual discount, waive a fee, override the keys, void.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/commercial"
	"oneclub/internal/kernel/authz"
	"oneclub/internal/kernel/clock"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/calendar"
	"oneclub/internal/platform/handle"
)

// publicToken is the unguessable link of a booking (My Stay, e-voucher).
func publicToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// online tells whether a booking source books without staff (the rules
// cannot be passed there).
func online(source string) bool {
	return source == "website" || source == "member_app" || source == "guest_app"
}

// supervised checks a supervisor action: the permission and a reason
// (FR-H91). The reason goes to the audit trail of the action.
func supervised(ctx context.Context, property uuid.UUID, reason, what string) error {
	p := authz.From(ctx)
	if p == nil || (!p.IsSystem() && !p.Can("stay.stay.supervise", &property)) {
		return errs.Forbidden(what + " needs a supervisor (Accommodation Manager)")
	}
	if strings.TrimSpace(reason) == "" {
		return handle.Invalid("supervisorReason", "required", "give the reason of the supervisor decision")
	}
	return nil
}

// localToday is the club's date now.
func localToday(ctx context.Context, q dbtx.Querier) time.Time {
	loc := calendar.Location(ctx, q)
	n := clock.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
}

// Restriction is a rule of a room type on some nights.
type Restriction struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	Code              string     `json:"code" db:"code"`
	BungalowTypeID    *uuid.UUID `json:"bungalowTypeId" db:"bungalow_type_id"`
	StartDate         time.Time  `json:"startDate" db:"start_date"`
	EndDate           time.Time  `json:"endDate" db:"end_date"`
	Closed            bool       `json:"closed" db:"closed"`
	ClosedToArrival   bool       `json:"closedToArrival" db:"closed_to_arrival"`
	ClosedToDeparture bool       `json:"closedToDeparture" db:"closed_to_departure"`
	MinNights         *int       `json:"minNights" db:"min_nights"`
	BookingSources    []string   `json:"bookingSources" db:"booking_sources"`
	Reason            *string    `json:"reason" db:"reason"`
}

// restrictions returns the active restrictions of a room type (or of every
// type) touching the local dates [from, to].
func restrictions(ctx context.Context, q dbtx.Querier, property uuid.UUID, typeID *uuid.UUID, from, to time.Time) ([]Restriction, error) {
	return handle.List[Restriction](q.Query(ctx, `SELECT id, code, bungalow_type_id, start_date, end_date, closed, closed_to_arrival, closed_to_departure,
		min_nights, booking_sources, reason FROM stay.rate_restrictions WHERE property_id = $1 AND status = 'active' AND archived_at IS NULL
		AND ($2::uuid IS NULL OR bungalow_type_id IS NULL OR bungalow_type_id = $2) AND start_date <= $4::date AND end_date >= $3::date
		ORDER BY start_date, code`, property, typeID, from.Format(time.DateOnly), to.Format(time.DateOnly)))
}

// restrictionError says why the stay cannot be sold under the restrictions
// for the channel ("" = allowed).
func restrictionError(list []Restriction, typeID uuid.UUID, arrival, departure time.Time, source string, lang string) (string, string) {
	in := func(r Restriction, day string) bool {
		return day >= r.StartDate.Format(time.DateOnly) && day <= r.EndDate.Format(time.DateOnly)
	}
	arr, dep := arrival.Format(time.DateOnly), departure.Format(time.DateOnly)
	nights := int(departure.Sub(arrival).Hours()/24 + 0.5)
	id := lang != "en"
	why := func(r Restriction) string {
		if r.Reason != nil && *r.Reason != "" {
			return " (" + *r.Reason + ")"
		}
		return ""
	}
	for _, r := range list {
		if (r.BungalowTypeID != nil && *r.BungalowTypeID != typeID) || (len(r.BookingSources) > 0 && source != "" && !slices.Contains(r.BookingSources, source)) {
			continue
		}
		if r.Closed {
			for i := 0; i < nights; i++ {
				day := arrival.AddDate(0, 0, i).Format(time.DateOnly)
				if in(r, day) {
					if id {
						return "closed", "Penjualan ditutup pada malam " + day + why(r)
					}
					return "closed", "Not sold on the night of " + day + why(r)
				}
			}
		}
		if r.ClosedToArrival && in(r, arr) {
			if id {
				return "closed_to_arrival", "Tidak menerima kedatangan pada " + arr + why(r)
			}
			return "closed_to_arrival", "No arrival on " + arr + why(r)
		}
		if r.ClosedToDeparture && in(r, dep) {
			if id {
				return "closed_to_departure", "Tidak menerima keberangkatan pada " + dep + why(r)
			}
			return "closed_to_departure", "No departure on " + dep + why(r)
		}
		if r.MinNights != nil && nights < *r.MinNights {
			for i := 0; i < nights; i++ {
				if in(r, arrival.AddDate(0, 0, i).Format(time.DateOnly)) {
					if id {
						return "min_nights", fmt.Sprintf("Minimal %d malam untuk tanggal ini%s", *r.MinNights, why(r))
					}
					return "min_nights", fmt.Sprintf("At least %d nights on these dates%s", *r.MinNights, why(r))
				}
			}
		}
	}
	return "", ""
}

// checkStayRules applies the booking window (online) and the restrictions
// of the dates to a bungalow booking. The front desk may pass a restriction
// with the supervisor permission and a reason; online channels never.
func (m *Module) checkStayRules(ctx context.Context, tx pgx.Tx, property uuid.UUID, pol Policy, typeID uuid.UUID, arrival, departure time.Time, in StayInput) error {
	if online(in.BookingSource) {
		today := localToday(ctx, tx)
		if arrival.Before(today) {
			return errs.Validation("arrival_in_past", "check-in paling cepat hari ini ("+today.Format("02 Jan 2006")+")",
				errs.Field("arrivalDate", "in_the_past", "check-in paling cepat hari ini"))
		}
		if pol.BookingWindowDays > 0 && arrival.After(today.AddDate(0, 0, pol.BookingWindowDays)) {
			return errs.Validation("beyond_window", fmt.Sprintf("booking paling jauh %d hari ke depan (sampai %s)", pol.BookingWindowDays,
				today.AddDate(0, 0, pol.BookingWindowDays).Format("02 Jan 2006")), errs.Field("arrivalDate", "beyond_window", "di luar booking window"))
		}
	}
	list, err := restrictions(ctx, tx, property, &typeID, arrival, departure)
	if err != nil {
		return err
	}
	code, msg := restrictionError(list, typeID, arrival, departure, in.BookingSource, "id")
	if code == "" {
		return nil
	}
	if online(in.BookingSource) || !in.OverrideRestrictions {
		e := errs.Conflict("restricted_"+code, msg)
		if !online(in.BookingSource) {
			e = errs.Conflict("restricted_"+code, msg+" — supervisor dapat melewati restriksi dengan alasan")
		}
		return e
	}
	return supervised(ctx, property, in.SupervisorReason, "Passing a restriction")
}

// applyManualDiscount lowers the room price by a supervisor discount and
// recalculates tax and service (FR-H91).
func (m *Module) applyManualDiscount(ctx context.Context, tx pgx.Tx, property uuid.UUID, sq *RoomQuote, in StayInput) error {
	amt, err := handle.Decimal("manualDiscount", in.ManualDiscount, decimal.Zero)
	if err != nil {
		return err
	}
	if !amt.IsPositive() {
		return nil
	}
	if err := supervised(ctx, property, in.SupervisorReason, "A manual discount"); err != nil {
		return err
	}
	gross := decOf(sq.Gross)
	disc := decimal.Min(decOf(sq.Discount).Add(amt), gross)
	cur, places := currencyPlaces(ctx, tx)
	pol, _, err := m.policy(ctx, tx, property)
	if err != nil {
		return err
	}
	loc := calendar.Location(ctx, tx)
	a, _ := time.ParseInLocation(time.DateOnly, in.ArrivalDate, loc)
	start := clockOn(a, pol.CheckInTime, loc)
	rules, err := commercial.RulesAt(ctx, tx, property, start)
	if err != nil {
		return err
	}
	b := commercial.CalculateMode(rules, gross.Sub(disc), cur, start, sq.PricingMode)
	sq.Discount = disc.StringFixed(places)
	sq.Net, sq.Total, sq.TaxLines = b.NetAmount, b.Total, b.Lines
	sq.Service, sq.Tax = commercial.SumKind(b.Lines, "service").String(), commercial.SumKind(b.Lines, "tax").String()
	if sq.TaxLines == nil {
		sq.TaxLines = []commercial.Line{}
	}
	return nil
}

// ── Overview alerts (FR-H63) ──────────────────────────────────────────────

// Alert is a warning on the Overview.
type Alert struct {
	Kind    string     `json:"kind" enum:"unpaid_arrival,ooo_reserved,late_departure,awaiting_payment"`
	Message string     `json:"message"`
	StayID  *uuid.UUID `json:"stayId"`
	StayNo  *string    `json:"stayNo"`
}

func (m *Module) alerts(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]Alert, error) {
	out := []Alert{}
	loc := calendar.Location(ctx, q)
	// not paid in full, arriving within three days
	soon, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'reserved'
		AND r.status IN ('pending', 'confirmed') AND s.start_at >= $2 AND s.start_at < $3 ORDER BY s.start_at`, property, day, day.AddDate(0, 0, 3)))
	if err != nil {
		return nil, err
	}
	for _, s := range soon {
		if s.PaymentStatus == "paid" || s.PaymentStatus == "overpaid" {
			continue
		}
		id, no := s.ID, s.StayNo
		out = append(out, Alert{Kind: "unpaid_arrival", StayID: &id, StayNo: &no, Message: fmt.Sprintf("%s · %s tiba %s, belum lunas (sisa Rp %s)", no,
			guestLabel(s), s.Start.In(loc).Format("02 Jan"), decOf(s.TotalDue).Sub(decOf(s.Paid)).StringFixed(0))})
	}
	// a bungalow out of order with a reservation on its nights
	type ooo struct {
		StayID uuid.UUID `db:"stay_id"`
		StayNo string    `db:"stay_no"`
		Unit   string    `db:"unit"`
		Kind   string    `db:"kind"`
	}
	list, err := handle.List[ooo](q.Query(ctx, `SELECT DISTINCT s.id AS stay_id, s.stay_no, b.code AS unit, k.kind FROM stay.room_blocks k
		JOIN stay.bungalows b ON b.id = k.bungalow_id
		JOIN stay.stays s ON s.unit_id = k.bungalow_id AND s.kind = 'bungalow' AND s.status IN ('reserved', 'checked_in')
		  AND tstzrange(s.start_at, s.end_at) && tstzrange(k.start_at, k.end_at)
		WHERE k.property_id = $1 AND k.status = 'active' AND k.kind IN ('maintenance', 'out_of_order') AND k.end_at > $2
		AND k.reservation_id IS DISTINCT FROM s.reservation_id`, property, day))
	if err != nil {
		return nil, err
	}
	for _, x := range list {
		id, no := x.StayID, x.StayNo
		out = append(out, Alert{Kind: "ooo_reserved", StayID: &id, StayNo: &no, Message: fmt.Sprintf("%s %s, ada reservasi %s: pindahkan tamu",
			x.Unit, strings.ReplaceAll(x.Kind, "_", " "), no)})
	}
	// guests still in-house after their check-out time
	late, err := handle.List[Stay](q.Query(ctx, staySelect+` WHERE s.property_id = $1 AND s.kind = 'bungalow' AND s.status = 'checked_in'
		AND coalesce(s.late_check_out_until, s.end_at) < $2 ORDER BY s.end_at`, property, clock.Now()))
	if err != nil {
		return nil, err
	}
	for _, s := range late {
		id, no := s.ID, s.StayNo
		out = append(out, Alert{Kind: "late_departure", StayID: &id, StayNo: &no, Message: fmt.Sprintf("%s · %s lewat jam check-out (%s)",
			deref(s.UnitCode), guestLabel(s), s.End.In(loc).Format("02 Jan 15:04"))})
	}
	return out, nil
}
