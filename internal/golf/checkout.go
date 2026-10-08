package golf

// Golfer Check-out (PRD P1 FR-CHK-05, member journey "Payment / Settlement
// → Leave Club"): after the round the booking folio carries what was added
// on the day (caddy tip, on-course F&B, golf cart surcharge, locker); the
// front desk settles the balance, the folio is closed, daily lockers return
// to Available and the dropped bags are handed back.

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"oneclub/internal/billing"
	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/kernel/id"
	"oneclub/internal/platform/audit"
)

// CheckOutRequest settles the balance (when any) and checks the booking out.
type CheckOutRequest struct {
	MethodType string `json:"methodType,omitempty" enum:"cash,card,qris,bank_transfer,member_account" doc:"Pays the outstanding balance first; empty when nothing is due"`
	Reference  string `json:"reference,omitempty" doc:"EDC approval code, QRIS or transfer reference"`
}

// CheckOutEntry is a booking of the day at the check-out desk.
type CheckOutEntry struct {
	BookingID     uuid.UUID  `json:"bookingId"`
	Code          string     `json:"code"`
	LocalTime     string     `json:"localTime"`
	CourseName    string     `json:"courseName"`
	ContactName   string     `json:"contactName"`
	Status        string     `json:"status" enum:"checked_in,completed"`
	PaymentMode   *string    `json:"paymentMode"`
	Players       []string   `json:"players"`
	InPlay        int        `json:"inPlay" doc:"Flights still on the course"`
	FolioID       *uuid.UUID `json:"folioId"`
	Charges       string     `json:"charges"`
	Payments      string     `json:"payments"`
	Balance       string     `json:"balance"`
	MemberAccount bool       `json:"memberAccount" doc:"The balance can be charged to a member account"`
	Lockers       []string   `json:"lockers" doc:"Daily lockers still in use"`
	Bags          []string   `json:"bags" doc:"Bag tags not handed back yet"`
	CheckedOutAt  *time.Time `json:"checkedOutAt"`
	TeeOffAt      *time.Time `json:"teeOffAt" doc:"Actual tee-off (play time starts)"`
	RoundFinishAt *time.Time `json:"roundFinishAt" doc:"Round finish of the last flight"`
	PausedAt      *time.Time `json:"pausedAt"`
	PausedSeconds int        `json:"pausedSeconds"`
}

// CheckOutDesk lists the checked-in bookings of a day with what is still
// open: balance, lockers, bags.
func (m *Module) CheckOutDesk(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time) ([]CheckOutEntry, error) {
	loc := location(ctx, q, property)
	rows, err := q.Query(ctx, `SELECT b.id, b.code, b.start_at, c.name, b.contact_name, b.status, b.payment_mode, b.folio_id, b.checked_out_at,
		coalesce((SELECT array_agg(bp.name ORDER BY bp.seq) FROM golf.booking_players bp WHERE bp.booking_id = b.id AND bp.status = 'checked_in'), '{}'),
		(SELECT count(*) FROM golf.flights f WHERE f.booking_id = b.id AND f.status = 'in_play'),
		coalesce((SELECT array_agg(l.code ORDER BY l.code) FROM golf.locker_assignments la JOIN golf.lockers l ON l.id = la.locker_id
			JOIN golf.booking_players bp ON bp.id = la.booking_player_id WHERE bp.booking_id = b.id AND la.status = 'active' AND la.assignment_type = 'daily'), '{}'),
		coalesce((SELECT array_agg(d.tag_number ORDER BY d.tag_number) FROM golf.bag_drops d JOIN golf.booking_players bp ON bp.id = d.booking_player_id
			WHERE bp.booking_id = b.id AND d.status <> 'collected'), '{}'),
		(SELECT min(f.tee_off_at) FROM golf.flights f WHERE f.booking_id = b.id AND f.status <> 'cancelled'),
		(SELECT CASE WHEN bool_and(f.round_finish_at IS NOT NULL) THEN max(f.round_finish_at) END FROM golf.flights f WHERE f.booking_id = b.id AND f.status <> 'cancelled'),
		(SELECT max(f.paused_at) FROM golf.flights f WHERE f.booking_id = b.id),
		(SELECT coalesce(max(f.paused_seconds), 0)::int FROM golf.flights f WHERE f.booking_id = b.id)
		FROM golf.bookings b JOIN golf.courses c ON c.id = b.course_id
		WHERE b.property_id = $1 AND b.play_date = $2::date AND b.status IN ('checked_in', 'completed') AND b.checked_in_at IS NOT NULL
		ORDER BY b.checked_out_at NULLS FIRST, b.start_at, b.code`, property, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckOutEntry{}
	for rows.Next() {
		var e CheckOutEntry
		var start time.Time
		if err := rows.Scan(&e.BookingID, &e.Code, &start, &e.CourseName, &e.ContactName, &e.Status, &e.PaymentMode, &e.FolioID, &e.CheckedOutAt,
			&e.Players, &e.InPlay, &e.Lockers, &e.Bags, &e.TeeOffAt, &e.RoundFinishAt, &e.PausedAt, &e.PausedSeconds); err != nil {
			return nil, err
		}
		e.LocalTime = start.In(loc).Format("15:04")
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		e := &out[i]
		e.Charges, e.Payments, e.Balance = "0", "0", "0"
		if e.FolioID != nil {
			s, err := billing.FolioSummary(ctx, q, *e.FolioID)
			if err != nil {
				return nil, err
			}
			e.Charges, e.Payments, e.Balance = s.Charges, s.Payments, s.Balance
		}
		acct, err := memberAccountOf(ctx, q, property, e.BookingID)
		if err != nil {
			return nil, err
		}
		e.MemberAccount = acct != nil
	}
	return out, nil
}

// memberAccountOf is the member account a booking's balance can be charged
// to: the booking member's, else the first member player's account holder.
func memberAccountOf(ctx context.Context, q dbtx.Querier, property, bookingID uuid.UUID) (*uuid.UUID, error) {
	members, err := collectIDs(q.Query(ctx, `SELECT member_id FROM (SELECT b.member_id, 0 AS seq FROM golf.bookings b WHERE b.id = $1 AND b.member_id IS NOT NULL
		UNION ALL SELECT bp.member_id, bp.seq FROM golf.booking_players bp WHERE bp.booking_id = $1 AND bp.member_id IS NOT NULL) x ORDER BY seq`, bookingID))
	if err != nil {
		return nil, err
	}
	for _, mid := range members {
		holder, err := membershipAccountHolder(ctx, q, mid)
		if err != nil || holder == nil {
			continue
		}
		a, err := billing.AccountFor(ctx, q, property, *holder, "member")
		if err != nil {
			return nil, err
		}
		if a != nil {
			return &a.ID, nil
		}
	}
	return nil, nil
}

// CheckOut settles and closes the booking folio, releases the daily lockers
// and hands the bags back.
func (m *Module) CheckOut(ctx context.Context, tx pgx.Tx, property, bookingID uuid.UUID, req CheckOutRequest) (Booking, error) {
	b, err := lockBooking(ctx, tx, property, bookingID)
	if err != nil {
		return b, err
	}
	if b.CheckedOutAt != nil {
		return b, errs.Conflict("already_checked_out", "booking "+b.Code+" is already checked out")
	}
	if b.Status != "checked_in" && b.Status != "completed" {
		return b, errs.Conflict("not_checked_in", "the booking is "+b.Status+"; only checked-in bookings can check out")
	}
	for _, f := range b.Flights {
		if f.Status == "in_play" {
			return b, errs.Conflict("round_in_play", "a flight is still on the course; finish the round first")
		}
	}
	if b.FolioID != nil {
		// the check-out takes the payment and closes the folio: the desk's billing permissions
		if err := need(ctx, property, "billing.folio.close"); err != nil {
			return b, err
		}
		sum, err := billing.FolioSummary(ctx, tx, *b.FolioID)
		if err != nil {
			return b, err
		}
		if bal := dec(sum.Balance); bal.IsPositive() {
			if err := need(ctx, property, "billing.payment.create"); err != nil {
				return b, err
			}
			if err := m.settleBalance(ctx, tx, property, b, bal, req); err != nil {
				return b, err
			}
		} else if bal.IsNegative() {
			return b, errs.Conflict("folio_credit", "the folio has a credit of "+bal.Neg().StringFixed(0)+"; refund it before check-out")
		}
		f, err := billing.GetFolio(ctx, tx, *b.FolioID)
		if err != nil {
			return b, err
		}
		if f.Status == "open" {
			if err := m.Billing.CloseFolio(ctx, tx, f.ID); err != nil {
				return b, err
			}
		}
	}
	// daily lockers return to Available when the players leave (FR-CHK-05)
	lockers, err := collectIDs(tx.Query(ctx, `UPDATE golf.locker_assignments SET status = 'released', released_at = now(),
		period = tstzrange(lower(period), greatest(lower(period) + interval '1 minute', now()), '[)')
		WHERE status = 'active' AND assignment_type = 'daily' AND booking_player_id IN (SELECT id FROM golf.booking_players WHERE booking_id = $1)
		RETURNING locker_id`, b.ID))
	if err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.lockers SET locker_status = 'available' WHERE id = ANY($1) AND locker_status = 'occupied'`, lockers); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bag_drops SET status = 'collected', collected_at = now() WHERE status <> 'collected'
		AND booking_player_id IN (SELECT id FROM golf.booking_players WHERE booking_id = $1)`, b.ID); err != nil {
		return b, err
	}
	if _, err := tx.Exec(ctx, `UPDATE golf.bookings SET checked_out_at = now(), checked_out_by = $2 WHERE id = $1`, b.ID, id.Ptr(actor(ctx))); err != nil {
		return b, err
	}
	if err := addHistory(ctx, tx, property, b.ID, "checked_out", nil, map[string]any{"lockers": len(lockers), "methodType": req.MethodType}, ""); err != nil {
		return b, err
	}
	day := mustDay(b.PlayDate)
	if err := notifyRealtime(ctx, tx, property, "golf.tee_sheet", "checked_out", b.CourseID, day, map[string]any{"bookingId": b.ID.String()}); err != nil {
		return b, err
	}
	if err := realtimeBoards(ctx, tx, property, day); err != nil {
		return b, err
	}
	after, err := GetBooking(ctx, tx, b.ID)
	if err != nil {
		return after, err
	}
	return after, audit.Record(ctx, tx, audit.Entry{Module: "golf", Action: "check_out", EntityType: "golf.booking", EntityID: b.ID.String(), EntityLabel: b.Code,
		PropertyID: &property, After: map[string]any{"lockers": len(lockers), "methodType": req.MethodType, "folio": after.Folio}})
}

func need(ctx context.Context, property uuid.UUID, perm string) error {
	if p := authzFrom(ctx); p == nil || !p.Can(perm, &property) {
		return errs.Forbidden("missing permission " + perm)
	}
	return nil
}

// settleBalance pays what the day added to the folio: at the desk, or to
// the member account.
func (m *Module) settleBalance(ctx context.Context, tx pgx.Tx, property uuid.UUID, b Booking, bal decimal.Decimal, req CheckOutRequest) error {
	method := strings.TrimSpace(req.MethodType)
	switch method {
	case "":
		return errs.Conflict("folio_unsettled", "settle the balance of "+bal.StringFixed(0)+" before check-out")
	case "member_account":
		acct, err := memberAccountOf(ctx, tx, property, b.ID)
		if err != nil {
			return err
		}
		if acct == nil {
			return errs.Validation("member_account_required", "no member account on this booking", errs.Field("methodType", "invalid", "member account"))
		}
		_, err = m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: b.FolioID, AccountID: acct, MethodType: "member_account",
			Amount: bal, Description: "Check-out " + b.Code})
		return err
	case "cash", "card", "qris", "bank_transfer":
		_, err := m.Billing.TakePayment(ctx, tx, billing.PaymentInput{FolioID: b.FolioID, MethodType: method, Channel: "venue", Amount: bal,
			Reference: strings.TrimSpace(req.Reference), PayerName: b.ContactName, Description: "Check-out " + b.Code})
		return err
	default:
		return errs.Validation("invalid_method", "unknown payment method", errs.Field("methodType", "invalid", "cash, card, qris, bank transfer or member account"))
	}
}

// ── charge targets for the POS (FR-POS-07, FR-FNB-02) ─────────────────────

// ChargeTarget is a golfer on the course or in the clubhouse whose booking
// folio a POS order can be charged to (halfway house, bar, restaurant).
type ChargeTarget struct {
	FolioID     uuid.UUID  `json:"folioId"`
	BookingID   uuid.UUID  `json:"bookingId"`
	BookingCode string     `json:"bookingCode"`
	PlayerName  string     `json:"playerName"`
	CustomerID  *uuid.UUID `json:"customerId"`
	LocalTime   string     `json:"localTime"`
	FlightNo    int        `json:"flightNo"`
	FlightState string     `json:"flightStatus" enum:"checked_in,ready,on_hold,in_play,completed"`
	BagTag      *string    `json:"bagTag"`
	Locker      *string    `json:"locker"`
}

// ChargeTargets lists the checked-in golfers of a day (the POS asks for
// today) whose booking folio is still open, searchable by name, booking
// code, bag tag or locker.
func (m *Module) ChargeTargets(ctx context.Context, q dbtx.Querier, property uuid.UUID, day time.Time, search string) ([]ChargeTarget, error) {
	loc := location(ctx, q, property)
	rows, err := q.Query(ctx, `SELECT b.folio_id, b.id, b.code, bp.name, bp.customer_id, t.start_at, f.flight_no, f.status,
		(SELECT d.tag_number FROM golf.bag_drops d WHERE d.booking_player_id = bp.id ORDER BY d.dropped_at DESC LIMIT 1),
		(SELECT l.code FROM golf.locker_assignments la JOIN golf.lockers l ON l.id = la.locker_id WHERE la.booking_player_id = bp.id AND la.status = 'active' LIMIT 1)
		FROM golf.booking_players bp JOIN golf.bookings b ON b.id = bp.booking_id JOIN golf.flights f ON f.id = bp.flight_id
		JOIN golf.tee_times t ON t.id = f.tee_time_id JOIN billing.folios fo ON fo.id = b.folio_id
		WHERE b.property_id = $1 AND b.play_date = $2::date AND bp.status = 'checked_in' AND b.checked_out_at IS NULL AND fo.status = 'open'
		ORDER BY t.start_at, b.code, bp.seq LIMIT 300`, property, day.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	needle := strings.ToLower(strings.TrimSpace(search))
	out := []ChargeTarget{}
	for rows.Next() {
		var t ChargeTarget
		var start time.Time
		if err := rows.Scan(&t.FolioID, &t.BookingID, &t.BookingCode, &t.PlayerName, &t.CustomerID, &start, &t.FlightNo, &t.FlightState, &t.BagTag, &t.Locker); err != nil {
			return nil, err
		}
		t.LocalTime = start.In(loc).Format("15:04")
		if needle != "" {
			hay := strings.ToLower(t.PlayerName + " " + t.BookingCode + " " + deref(t.BagTag) + " " + deref(t.Locker))
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func collectIDs(rows pgx.Rows, err error) ([]uuid.UUID, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ReleaseDailyLockers frees the daily lockers whose day is over (players who
// left without a check-out).
func (m *Module) ReleaseDailyLockers(ctx context.Context) (int, error) {
	ctx = dbtx.System(ctx)
	n := 0
	err := m.DB.WithTx(ctx, func(tx pgx.Tx) error {
		lockers, err := collectIDs(tx.Query(ctx, `UPDATE golf.locker_assignments SET status = 'released', released_at = now()
			WHERE status = 'active' AND assignment_type = 'daily' AND NOT upper_inf(period) AND upper(period) <= now() RETURNING locker_id`))
		if err != nil {
			return err
		}
		n = len(lockers)
		_, err = tx.Exec(ctx, `UPDATE golf.lockers SET locker_status = 'available' WHERE id = ANY($1) AND locker_status = 'occupied'`, lockers)
		return err
	})
	return n, err
}
